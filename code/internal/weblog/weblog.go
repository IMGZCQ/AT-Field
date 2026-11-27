// Package weblog 增量采集 nginx access.log 并按小时聚合，用于网络页的
// 「访问分析」：来源 IP、被扫路径、状态码分布与请求量趋势。
//
// 只读取日志、不修改任何系统文件；按 inode + 偏移量增量读取，日志轮转
// （新文件 inode 变化或文件变短）时自动从头开始。聚合结果按小时写入
// data/weblog/h-YYYYMMDD-HH.json，默认保留 30 天。
package weblog

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// RetentionDays 聚合数据保留天数
	RetentionDays = 30
	// maxIPsPerHour 单小时最多保留的 IP 数（按访问量取 Top，防止被扫时无限膨胀）
	maxIPsPerHour = 400
	// maxPathsPerHour 单小时最多保留的路径数
	maxPathsPerHour = 200
	readChunk       = 2 << 20 // 单次最多读取 2MB，避免大日志阻塞采集循环
	pollEvery       = 5 * time.Second
	saveEvery       = 20 * time.Second
	pendingLimit    = 64 << 10

	// 内存中的近期异常窗口：用于「扫描防护」自动封禁，不落盘
	recentBadWindow   = 10 * time.Minute
	maxRecentBadIPs   = 2000
	maxRecentBadPerIP = 600
	// scanBadMin 判定「疑似扫描」的最少异常请求数
	scanBadMin = 20
)

var logCandidates = []string{
	"/usr/trim/nginx/logs/access.log",
	"/usr/trim/nginx/logs/access_log",
	"/var/log/nginx/access.log",
	"/var/log/nginx/access_log",
}

// nginx 日志可能被部署在其它位置，可用 ATFF_NGINX_LOG 显式指定（优先于自动探测）
func candidates() []string {
	if p := strings.TrimSpace(os.Getenv("ATFF_NGINX_LOG")); p != "" {
		return append([]string{p}, logCandidates...)
	}
	return logCandidates
}

// nginx combined / main 格式：remote_addr - user [time] "request" status size ...
var reLine = regexp.MustCompile(`^(\S+) \S+ \S+ \[([^\]]+)\] "([^"]*)" ([0-9]{3}) (\S+)`)
var reQuoted = regexp.MustCompile(`"([^"]*)"`)

// hourBucket 一个小时的聚合结果，落盘为单独 JSON 文件。
type hourBucket struct {
	Hour     int64          `json:"hour"`            // 该小时起点（Unix 秒）
	Total    int            `json:"total"`           // 请求总数
	Slots    [6]int         `json:"slots"`           // 每 10 分钟一个计数，用于 1 小时趋势
	Bad      [6]int         `json:"bad,omitempty"`   // 每 10 分钟一个异常（4xx / 5xx）计数
	IPs      map[string]int `json:"ips"`             // 来源 IP 访问次数
	IPBad    map[string]int `json:"ipBad,omitempty"` // 来源 IP 的异常请求次数
	Paths    map[string]int `json:"paths"`           // 请求路径（不含查询串）次数
	Statuses map[string]int `json:"statuses"`        // HTTP 状态码分布
}

func newBucket(hour int64) *hourBucket {
	return &hourBucket{
		Hour:     hour,
		IPs:      map[string]int{},
		IPBad:    map[string]int{},
		Paths:    map[string]int{},
		Statuses: map[string]int{},
	}
}

func (b *hourBucket) clone() *hourBucket {
	c := &hourBucket{Hour: b.Hour, Total: b.Total, Slots: b.Slots, Bad: b.Bad,
		IPs:      make(map[string]int, len(b.IPs)),
		IPBad:    make(map[string]int, len(b.IPBad)),
		Paths:    make(map[string]int, len(b.Paths)),
		Statuses: make(map[string]int, len(b.Statuses)),
	}
	for k, v := range b.IPs {
		c.IPs[k] = v
	}
	for k, v := range b.IPBad {
		c.IPBad[k] = v
	}
	for k, v := range b.Paths {
		c.Paths[k] = v
	}
	for k, v := range b.Statuses {
		c.Statuses[k] = v
	}
	return c
}

// Point 趋势图上的一个点
type Point struct {
	Ts    int64 `json:"ts"`
	Count int   `json:"count"`
	Bad   int   `json:"bad,omitempty"` // 异常请求（4xx / 5xx）数
}

// IPCount 来源 IP 访问次数
type IPCount struct {
	IP    string `json:"ip"`
	Count int    `json:"count"`
	Bad   int    `json:"bad,omitempty"`  // 异常请求次数
	Scan  bool   `json:"scan,omitempty"` // 是否判定为疑似扫描
}

// PathCount 路径访问次数
type PathCount struct {
	Path  string `json:"path"`
	Count int    `json:"count"`
}

// StatusCount 状态码分布
type StatusCount struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

// Query 一次访问分析的聚合结果
type Query struct {
	Available bool          `json:"available"` // 是否找到可读的访问日志
	LogPath   string        `json:"logPath"`
	Error     string        `json:"error,omitempty"`
	From      int64         `json:"from"` // 统计窗口（按整点/整天对齐）
	To        int64         `json:"to"`
	Total     int           `json:"total"`
	Bad       int           `json:"bad"`       // 异常请求（4xx / 5xx）数
	Loopback  int           `json:"loopback"`  // 来源为本机回环的请求数（可能经过反向代理）
	PrevTotal int           `json:"prevTotal"` // 上一个等长窗口的请求数，用于环比
	PrevBad   int           `json:"prevBad"`   // 上一个等长窗口的异常请求数
	LastEvent int64         `json:"lastEvent"`
	Since     int64         `json:"since"` // 本次采集开始时间
	Points    []Point       `json:"points"`
	IPs       []IPCount     `json:"ips"`
	Scans     []IPCount     `json:"scans"` // 疑似扫描的 IP
	Paths     []PathCount   `json:"paths"`
	Statuses  []StatusCount `json:"statuses"`
}

// Collector 日志采集器
type Collector struct {
	dir      string
	basePath string

	mu        sync.Mutex
	logPath   string
	inode     uint64
	offset    int64
	pending   string
	hour      int64
	bucket    *hourBucket
	recentBad map[string][]int64 // 近期异常请求时间戳（按 IP），仅内存
	dirty     bool
	lastErr   string
	lastEvt   int64
	started   int64
	lastSave  time.Time

	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// Open 创建采集器：发现日志文件、载入当前小时已有聚合、清理过期文件。
// basePath 为本应用的网关前缀（如 /app/AT-ForceField），用于过滤自身接口请求。
func Open(dataDir, basePath string) (*Collector, error) {
	dir := filepath.Join(dataDir, "weblog")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	c := &Collector{
		dir:       dir,
		basePath:  strings.TrimSuffix(basePath, "/"),
		hour:      time.Now().Truncate(time.Hour).Unix(),
		started:   time.Now().Unix(),
		lastSave:  time.Now(),
		recentBad: map[string][]int64{},
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	c.bucket = c.loadHour(c.hour)
	c.discover()
	// 从文件末尾开始，避免重启后把历史日志重复计入
	if c.logPath != "" {
		if st, err := os.Stat(c.logPath); err == nil {
			c.offset = st.Size()
			c.inode = fileID(st)
		}
	}
	c.prune()
	return c, nil
}

// Start 启动后台采集循环（每 5 秒读一次新增内容，每 20 秒落盘一次）
func (c *Collector) Start() {
	go func() {
		defer close(c.done)
		t := time.NewTicker(pollEvery)
		defer t.Stop()
		for {
			select {
			case <-c.stop:
				c.flush(true)
				return
			case <-t.C:
				c.poll()
				c.flush(false)
			}
		}
	}()
}

// Close 停止采集并落盘
func (c *Collector) Close() {
	c.once.Do(func() { close(c.stop) })
	<-c.done
}

func (c *Collector) pathFor(hour int64) string {
	return filepath.Join(c.dir, "h-"+time.Unix(hour, 0).Format("20060102-15")+".json")
}

// discover 依次尝试常见 nginx 日志路径，选中第一个存在的
func (c *Collector) discover() {
	for _, p := range candidates() {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			c.mu.Lock()
			c.logPath = p
			c.inode = fileID(st)
			c.offset = st.Size()
			c.mu.Unlock()
			return
		}
	}
}

func (c *Collector) poll() {
	c.mu.Lock()
	path := c.logPath
	c.mu.Unlock()
	if path == "" {
		c.discover()
		return
	}
	f, err := os.Open(path)
	if err != nil {
		c.fail("打开访问日志失败：" + err.Error())
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		c.fail("读取访问日志状态失败：" + err.Error())
		return
	}
	c.mu.Lock()
	if id := fileID(st); id != 0 && c.inode != 0 && id != c.inode {
		// 日志轮转：新文件从头读
		c.offset = 0
		c.pending = ""
	}
	c.inode = fileID(st)
	if st.Size() < c.offset {
		c.offset = 0
		c.pending = ""
	}
	offset := c.offset
	c.mu.Unlock()
	if st.Size() == offset {
		return
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		c.fail("定位访问日志失败：" + err.Error())
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, readChunk))
	if err != nil {
		c.fail("读取访问日志失败：" + err.Error())
		return
	}
	c.mu.Lock()
	c.offset = offset + int64(len(data))
	c.mu.Unlock()
	c.consume(string(data))
}

func (c *Collector) fail(msg string) {
	c.mu.Lock()
	c.lastErr = msg
	c.logPath = ""
	c.mu.Unlock()
}

// consume 按完整行切分并聚合；不完整的尾行留到下一轮
func (c *Collector) consume(chunk string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	text := c.pending + chunk
	c.pending = ""
	idx := strings.LastIndexByte(text, '\n')
	if idx < 0 {
		if len(text) <= pendingLimit {
			c.pending = text
		}
		return
	}
	if rest := text[idx+1:]; len(rest) <= pendingLimit {
		c.pending = rest
	}
	for _, line := range strings.Split(text[:idx], "\n") {
		c.addLineLocked(line)
	}
	c.dirty = true
}

func (c *Collector) addLineLocked(line string) {
	line = strings.TrimRight(line, "\r")
	if line == "" {
		return
	}
	m := reLine.FindStringSubmatch(line)
	if m == nil {
		return
	}
	ts, err := time.Parse("02/Jan/2006:15:04:05 -0700", m[2])
	if err != nil {
		ts = time.Now()
	}
	if time.Since(ts) > RetentionDays*24*time.Hour {
		return
	}
	fields := strings.Fields(m[3])
	if len(fields) < 2 {
		return
	}
	switch fields[0] {
	case "GET", "POST", "HEAD", "PUT", "DELETE", "OPTIONS", "PATCH":
	default:
		return
	}
	path := fields[1]
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	if path == "" {
		path = "/"
	}
	if len(path) > 120 {
		path = path[:120]
	}
	if c.isSelf(path) {
		return
	}
	ip := clientIP(m[1], line, m[3])
	if ip == "" {
		return
	}
	hour := ts.Truncate(time.Hour).Unix()
	if hour != c.hour || c.bucket == nil {
		c.saveLocked()
		c.hour = hour
		c.bucket = c.loadHour(hour)
	}
	b := c.bucket
	b.Total++
	b.Slots[ts.Minute()/10]++
	b.IPs[ip]++
	b.Paths[path]++
	b.Statuses[m[4]]++
	if m[4][0] == '4' || m[4][0] == '5' {
		b.Bad[ts.Minute()/10]++
		b.IPBad[ip]++
		c.noteBadLocked(ip, ts.Unix())
	}
	c.lastEvt = ts.Unix()
}

// noteBadLocked 记录一次异常请求时间戳（供扫描防护使用）。
// 只保留最近 10 分钟、单 IP 最多 maxRecentBadPerIP 条，IP 总数超出时先清理过期项。
func (c *Collector) noteBadLocked(ip string, ts int64) {
	if c.recentBad == nil {
		c.recentBad = map[string][]int64{}
	}
	cutoff := ts - int64(recentBadWindow/time.Second)
	list := c.recentBad[ip]
	i := 0
	for i < len(list) && list[i] <= cutoff {
		i++
	}
	list = list[i:]
	if len(list) >= maxRecentBadPerIP {
		list = list[len(list)-maxRecentBadPerIP+1:]
	}
	c.recentBad[ip] = append(list, ts)
	if len(c.recentBad) <= maxRecentBadIPs {
		return
	}
	// IP 数量超限：先丢掉窗口内已无记录的 IP；仍然超限就不再接纳新 IP
	for k, v := range c.recentBad {
		if len(v) == 0 || v[len(v)-1] <= cutoff {
			delete(c.recentBad, k)
		}
	}
	if len(c.recentBad) > maxRecentBadIPs {
		delete(c.recentBad, ip)
	}
}

// RecentBadIPs 返回最近 window 内异常请求达到 min 次的来源 IP，按次数降序（最多 20 个）。
func (c *Collector) RecentBadIPs(window time.Duration, min int) []string {
	if window <= 0 {
		window = recentBadWindow
	}
	if min < 1 {
		min = 1
	}
	cutoff := time.Now().Unix() - int64(window/time.Second)
	type kv struct {
		ip string
		n  int
	}
	c.mu.Lock()
	list := make([]kv, 0, len(c.recentBad))
	for ip, stamps := range c.recentBad {
		n := 0
		for i := len(stamps) - 1; i >= 0 && stamps[i] > cutoff; i-- {
			n++
		}
		if n >= min {
			list = append(list, kv{ip, n})
		}
	}
	c.mu.Unlock()
	sort.Slice(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		return list[i].ip < list[j].ip
	})
	if len(list) > 20 {
		list = list[:20]
	}
	out := make([]string, len(list))
	for i, it := range list {
		out[i] = it.ip
	}
	return out
}

// isSelf 过滤本应用自身的接口轮询与 SSE，避免把监控流量算进访问分析
func (c *Collector) isSelf(path string) bool {
	if strings.HasSuffix(path, "/api/stream") || path == "/api/stream" {
		return true
	}
	if c.basePath != "" && strings.HasPrefix(path, c.basePath+"/api/") {
		return true
	}
	return strings.HasSuffix(path, ".map")
}

// clientIP 取来源 IP；若日志记录的是回环地址（反向代理），尝试从引号字段里取 X-Forwarded-For
func clientIP(remote, line, req string) string {
	ip := strings.TrimSpace(remote)
	if ip == "" {
		return ""
	}
	if !isLoopback(ip) {
		return ip
	}
	for _, seg := range reQuoted.FindAllStringSubmatch(line, -1) {
		val := strings.TrimSpace(seg[1])
		if val == "" || val == req {
			continue
		}
		for _, part := range strings.FieldsFunc(val, func(r rune) bool { return r == ',' || r == ' ' }) {
			cand := strings.Trim(part, "[]")
			if net.ParseIP(cand) == nil || isLoopback(cand) {
				continue
			}
			return cand
		}
	}
	return ip
}

func isLoopback(s string) bool {
	ip := net.ParseIP(strings.Trim(strings.TrimSpace(s), "[]"))
	return ip != nil && ip.IsLoopback()
}

func (c *Collector) flush(force bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty {
		return
	}
	if !force && time.Since(c.lastSave) < saveEvery {
		return
	}
	c.saveLocked()
}

// saveLocked 把当前小时聚合写入磁盘（先写临时文件再改名，避免读到半截 JSON）
func (c *Collector) saveLocked() {
	if c.bucket == nil || !c.dirty {
		return
	}
	c.bucket.IPs = topN(c.bucket.IPs, maxIPsPerHour)
	c.bucket.IPBad = topN(c.bucket.IPBad, maxIPsPerHour)
	c.bucket.Paths = topN(c.bucket.Paths, maxPathsPerHour)
	raw, err := json.Marshal(c.bucket)
	if err != nil {
		return
	}
	p := c.pathFor(c.bucket.Hour)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return
	}
	c.dirty = false
	c.lastSave = time.Now()
}

func (c *Collector) loadHour(hour int64) *hourBucket {
	raw, err := os.ReadFile(c.pathFor(hour))
	if err != nil {
		return newBucket(hour)
	}
	var b hourBucket
	if err := json.Unmarshal(raw, &b); err != nil {
		return newBucket(hour)
	}
	if b.IPs == nil {
		b.IPs = map[string]int{}
	}
	if b.IPBad == nil {
		b.IPBad = map[string]int{}
	}
	if b.Paths == nil {
		b.Paths = map[string]int{}
	}
	if b.Statuses == nil {
		b.Statuses = map[string]int{}
	}
	b.Hour = hour
	return &b
}

// prune 删除超过保留期的聚合文件
func (c *Collector) prune() {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -RetentionDays)
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "h-") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(c.dir, e.Name()))
	}
}

// Query 汇总指定范围（1h / 1d / 7d / 30d）的访问数据。
// 统计窗口按整点（7 天 / 30 天按整天）对齐，保证聚合文件与总量一致。
func (c *Collector) Query(rangeKey string) Query {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	var from time.Time
	switch rangeKey {
	case "1h":
		from = now.Truncate(time.Hour)
	case "7d":
		from = today.AddDate(0, 0, -6)
	case "30d":
		from = today.AddDate(0, 0, -(RetentionDays - 1))
	default:
		from = now.Truncate(time.Hour).Add(-23 * time.Hour)
	}

	c.mu.Lock()
	q := Query{
		Available: c.logPath != "",
		LogPath:   c.logPath,
		Error:     c.lastErr,
		From:      from.Unix(),
		To:        now.Unix(),
		Since:     c.started,
		LastEvent: c.lastEvt,
	}
	curHour := c.hour
	var cur *hourBucket
	if c.bucket != nil {
		cur = c.bucket.clone()
	}
	c.mu.Unlock()

	cache := map[int64]*hourBucket{}
	get := func(h int64) *hourBucket {
		if b, ok := cache[h]; ok {
			return b
		}
		var b *hourBucket
		if h == curHour && cur != nil {
			b = cur
		} else {
			b = c.loadHour(h)
		}
		cache[h] = b
		return b
	}

	// 聚合窗口内的 IP / 路径 / 状态码 / 异常请求
	ips := map[string]int{}
	ipBad := map[string]int{}
	paths := map[string]int{}
	statuses := map[string]int{}
	total, bad := 0, 0
	loopback := 0
	for h := from.Truncate(time.Hour).Unix(); h <= now.Truncate(time.Hour).Unix(); h += int64(time.Hour / time.Second) {
		b := get(h)
		total += b.Total
		bad += badOf(b)
		for k, v := range b.IPs {
			ips[k] += v
			if isLoopback(k) {
				loopback += v
			}
		}
		for k, v := range b.IPBad {
			ipBad[k] += v
		}
		for k, v := range b.Paths {
			paths[k] += v
		}
		for k, v := range b.Statuses {
			statuses[k] += v
		}
	}
	q.Total = total
	q.Bad = bad
	q.Loopback = loopback
	q.IPs = topIPs(ips, 10)
	q.Paths = topPaths(paths, 10)
	q.Statuses = topStatuses(statuses, 8)

	// 环比：与上一个等长窗口对比（30 天窗口较长，不做环比）
	if rangeKey != "30d" {
		prevFrom := from
		switch rangeKey {
		case "1h":
			prevFrom = from.Add(-time.Hour)
		case "7d":
			prevFrom = from.AddDate(0, 0, -7)
		default:
			prevFrom = from.Add(-24 * time.Hour)
		}
		for h := prevFrom.Truncate(time.Hour).Unix(); h < from.Unix(); h += int64(time.Hour / time.Second) {
			b := get(h)
			q.PrevTotal += b.Total
			q.PrevBad += badOf(b)
		}
	}

	// 疑似扫描：异常次数达到阈值，且占该 IP 请求量的一半以上
	flagged := map[string]bool{}
	scans := make([]IPCount, 0)
	for ip, n := range ipBad {
		cnt := ips[ip]
		if cnt > 0 && n >= scanBadMin && n*2 >= cnt {
			flagged[ip] = true
			scans = append(scans, IPCount{IP: ip, Count: cnt, Bad: n, Scan: true})
		}
	}
	sort.Slice(scans, func(i, j int) bool {
		if scans[i].Bad != scans[j].Bad {
			return scans[i].Bad > scans[j].Bad
		}
		return scans[i].Count > scans[j].Count
	})
	if len(scans) > 10 {
		scans = scans[:10]
	}
	for i := range q.IPs {
		q.IPs[i].Bad = ipBad[q.IPs[i].IP]
		q.IPs[i].Scan = flagged[q.IPs[i].IP]
	}
	q.Scans = scans

	// 趋势点位
	switch rangeKey {
	case "1h":
		// 统计窗口就是本小时，趋势只画到当前 10 分钟，避免出现「未来时段为 0」的假下跌
		b := get(from.Unix())
		last := now.Minute() / 10
		for i := 0; i <= last; i++ {
			ts := from.Add(time.Duration(i) * 10 * time.Minute)
			q.Points = append(q.Points, Point{Ts: ts.Unix(), Count: b.Slots[i], Bad: b.Bad[i]})
		}
	case "7d", "30d":
		days := 7
		if rangeKey == "30d" {
			days = RetentionDays
		}
		for i := 0; i < days; i++ {
			d := from.AddDate(0, 0, i)
			sum, badSum := 0, 0
			for h := 0; h < 24; h++ {
				b := get(d.Add(time.Duration(h) * time.Hour).Unix())
				sum += b.Total
				badSum += badOf(b)
			}
			q.Points = append(q.Points, Point{Ts: d.Unix(), Count: sum, Bad: badSum})
		}
	default:
		for i := 23; i >= 0; i-- {
			ts := now.Truncate(time.Hour).Add(-time.Duration(i) * time.Hour)
			b := get(ts.Unix())
			q.Points = append(q.Points, Point{Ts: ts.Unix(), Count: b.Total, Bad: badOf(b)})
		}
	}
	if q.Points == nil {
		q.Points = []Point{}
	}
	return q
}

// badOf 统计一个小时内 4xx / 5xx 请求数（由状态码分布推导，兼容旧版聚合文件）
func badOf(b *hourBucket) int {
	n := 0
	for code, v := range b.Statuses {
		if code != "" && (code[0] == '4' || code[0] == '5') {
			n += v
		}
	}
	return n
}

func topIPs(m map[string]int, n int) []IPCount {
	out := make([]IPCount, 0, len(m))
	for k, v := range m {
		out = append(out, IPCount{IP: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].IP < out[j].IP
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func topPaths(m map[string]int, n int) []PathCount {
	out := make([]PathCount, 0, len(m))
	for k, v := range m {
		out = append(out, PathCount{Path: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Path < out[j].Path
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func topStatuses(m map[string]int, n int) []StatusCount {
	out := make([]StatusCount, 0, len(m))
	for k, v := range m {
		out = append(out, StatusCount{Code: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Code < out[j].Code
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// topN 保留访问量最高的 n 个键，返回新 map
func topN(m map[string]int, n int) map[string]int {
	if len(m) <= n {
		return m
	}
	type kv struct {
		k string
		v int
	}
	list := make([]kv, 0, len(m))
	for k, v := range m {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].v != list[j].v {
			return list[i].v > list[j].v
		}
		return list[i].k < list[j].k
	})
	out := make(map[string]int, n)
	for _, it := range list[:n] {
		out[it.k] = it.v
	}
	return out
}

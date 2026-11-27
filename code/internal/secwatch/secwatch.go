// Package secwatch 周期巡检真实的安全状态：SSH 暴力破解记录（lastb / journalctl）、
// 防火墙规则（nft / iptables）、自动封禁（fail2ban）、硬盘健康（smartctl）
// 以及 SSH 密码登录配置。巡检需要 root 权限，读取失败时按“未知”处理。
package secwatch

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"atforcefield/internal/audit"
	"atforcefield/internal/shield"
)

// Interval 安全巡检间隔
const Interval = 5 * time.Minute

// State 最近一次安全巡检结果
type State struct {
	CheckedAt       int64     `json:"checkedAt"`
	FailedLogins    int       `json:"failedLogins"`    // 近 24h 失败登录次数
	FailedIPs       int       `json:"failedIPs"`       // 去重来源 IP 数
	FailedList      []FailIP  `json:"failedList"`      // 失败登录来源 Top（最多 10 个）
	FirewallOn      bool      `json:"firewallOn"`      // 飞牛防火墙是否真正开启（以 fw.conf 的 enable 为准）
	FirewallRules   int       `json:"firewallRules"`   // 防火墙规则条数（不含 Docker 自动规则）
	FirewallKnown   bool      `json:"firewallKnown"`   // 是否成功读到飞牛防火墙配置
	Fail2BanOn      bool      `json:"fail2banOn"`      // 是否启用 fail2ban
	NftBanOn        bool      `json:"nftBanOn"`        // 本应用 nft 兜底封禁是否就绪
	Banned          int       `json:"banned"`          // 当前被封禁 IP 数
	BannedIPs       []string  `json:"bannedIPs"`       // 当前被封禁的 IP 列表（最多 50 个）
	Bans            []BanInfo `json:"bans"`            // 封禁明细（含来源后端与剩余时间）
	SmartChecked    int       `json:"smartChecked"`    // 已检查硬盘数
	SmartFailed     int       `json:"smartFailed"`     // 健康异常硬盘数
	SSHPasswordAuth bool      `json:"sshPasswordAuth"` // SSH 是否允许密码登录
	SSHConfigKnown  bool      `json:"sshConfigKnown"`  // 是否成功读到 sshd 配置
	Real            bool      `json:"real"`            // 是否为真实巡检结果
}

// FailIP 一个失败登录来源 IP 的统计信息
type FailIP struct {
	IP     string `json:"ip"`
	Count  int    `json:"count"`
	LastTS int64  `json:"lastTs"` // 最近一次尝试时间
	User   string `json:"user"`   // 最近一次尝试使用的账号
	Banned bool   `json:"banned"` // 是否已在 fail2ban 封禁名单
}

// Event 一条真实安全事件
type Event struct {
	Time   int64
	Level  string
	Module string
	Msg    string
	Object string // 涉及对象（如来源 IP）
	Detail string // 结构化详情
}

var (
	reJournalFailed = regexp.MustCompile(`Failed password for (?:invalid user )?(\S+) from (\S+) port (\d+)`)
	reInvalidUser   = regexp.MustCompile(`Invalid user (\S+) from (\S+)`)
	reBanned        = regexp.MustCompile(`Currently banned:\s*(\d+)`)
	reBannedList    = regexp.MustCompile(`Banned IP list:\s*(.+)`)
	reJailList      = regexp.MustCompile(`Jail list:\s*(.+)`)
)

// Watcher 安全巡检器
type Watcher struct {
	au *audit.Store

	mu        sync.RWMutex
	state     State
	lastEvent int64
	nftUsed   bool // 是否曾用 nft 兜底封禁过，用于识别规则被外部清空
}

// NewWatcher 创建巡检器
func NewWatcher(au *audit.Store) *Watcher {
	return &Watcher{au: au}
}

// Start 启动后台巡检（首次延迟 3 秒，避免与启动争抢 IO）
func (w *Watcher) Start() {
	go func() {
		time.Sleep(3 * time.Second)
		w.Refresh()
		t := time.NewTicker(Interval)
		defer t.Stop()
		for range t.C {
			w.Refresh()
		}
	}()
}

// State 返回最近一次巡检结果
func (w *Watcher) State() State {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.state
}

// FailedIn 按指定时间窗口重新统计失败登录，供网络页的时限选择对齐；
// 只读采集，不写入审计日志，封禁状态沿用最近一次巡检结果。
func (w *Watcher) FailedIn(win time.Duration) (int, []FailIP) {
	count, ips, _ := scanFailedLogins(win)
	if ips == nil {
		return 0, nil
	}
	st := w.State()
	if len(st.BannedIPs) > 0 {
		banned := make(map[string]bool, len(st.BannedIPs))
		for _, ip := range st.BannedIPs {
			banned[ip] = true
		}
		for i := range ips {
			ips[i].Banned = banned[ips[i].IP]
		}
	}
	return count, ips
}

// Refresh 立即执行一次巡检，并把新事件写入审计日志
func (w *Watcher) Refresh() State {
	st := State{CheckedAt: time.Now().Unix()}

	failed, ips, events := scanFailedLogins(24 * time.Hour)
	st.FailedLogins, st.FailedIPs = failed, len(ips)
	st.FailedList = ips
	if failed > 0 || len(events) > 0 {
		st.Real = true
	}
	if on, rules, known := scanFirewall(); known || on || rules > 0 {
		st.FirewallOn, st.FirewallRules, st.FirewallKnown = on, rules, known
		st.Real = true
	}
	bans := make([]BanInfo, 0, 8)
	bannedTotal := 0
	if on, count, list := scanFail2Ban(); on {
		st.Fail2BanOn = on
		bannedTotal = count
		for _, ip := range list {
			bans = append(bans, BanInfo{IP: ip, Backend: "fail2ban"})
		}
		st.Real = true
	}
	// nft 兜底封禁：fail2ban 不可用时由本应用维护独立封禁表 inet atff
	if !st.Fail2BanOn {
		existed := nftTableExists()
		if err := nftEnsure(); err == nil {
			st.NftBanOn = true
			st.Real = true
			if !existed {
				if w.nftUsedBefore() {
					events = append(events, Event{
						Time: time.Now().Unix(), Level: "warn", Module: "IP 封禁",
						Msg:    "nft 封禁规则被外部清空，已自动重建",
						Object: "inet " + nftTable,
						Detail: "表=inet " + nftTable + "；集合=ban4/ban6；来源=巡检",
					})
				}
				w.setNftUsed()
			}
			nftList := nftBans()
			bannedTotal += len(nftList)
			bans = append(bans, nftList...)
		}
	}
	if len(bans) > 0 {
		st.Bans = bans
		st.Banned = bannedTotal
		for _, b := range bans {
			st.BannedIPs = append(st.BannedIPs, b.IP)
		}
	}
	if len(st.BannedIPs) > 0 {
		banned := make(map[string]bool, len(st.BannedIPs))
		for _, ip := range st.BannedIPs {
			banned[ip] = true
		}
		for i := range st.FailedList {
			st.FailedList[i].Banned = banned[st.FailedList[i].IP]
		}
	}
	if checked, failed := scanSMART(); checked > 0 {
		st.SmartChecked, st.SmartFailed = checked, failed
		st.Real = true
	}
	if pwauth, known := scanSSHD(); known {
		st.SSHPasswordAuth, st.SSHConfigKnown = pwauth, true
		st.Real = true
	}

	w.mu.Lock()
	w.state = st
	last := w.lastEvent
	w.mu.Unlock()

	if w.au != nil && len(events) > 0 {
		// 只写入比上次更晚的事件，避免每轮巡检重复记录
		var maxTS int64
		for i := len(events) - 1; i >= 0; i-- {
			e := events[i]
			if e.Time > last {
				w.au.AddDetail(shield.Log{
					Time: e.Time, Level: e.Level, Module: e.Module, Msg: e.Msg,
					Object: e.Object, Detail: e.Detail, Source: "巡检", Actor: "系统",
				})
			}
			if e.Time > maxTS {
				maxTS = e.Time
			}
		}
		if maxTS > last {
			w.mu.Lock()
			w.lastEvent = maxTS
			w.mu.Unlock()
		}
	}
	return st
}

// scanFailedLogins 采集指定时间窗口内的失败登录：优先 lastb，其次 journalctl。
// win 为统计窗口（如 1h / 24h / 7*24h），返回失败总次数、按次数排序的来源 IP 明细（最多 10 个）与新产生的事件。
func scanFailedLogins(win time.Duration) (int, []FailIP, []Event) {
	if win < time.Minute {
		win = 24 * time.Hour
	}
	agg := map[string]*FailIP{}
	events := make([]Event, 0, 10)
	count := 0

	addHit := func(ip, user string, ts int64) {
		if ip == "" {
			return
		}
		count++
		it, ok := agg[ip]
		if !ok {
			it = &FailIP{IP: ip}
			agg[ip] = it
		}
		it.Count++
		if ts >= it.LastTS {
			it.LastTS = ts
			it.User = user
		}
		if len(events) < 10 {
			events = append(events, Event{
				Time: ts, Level: "block", Module: "SSH 防护",
				Msg:    "拦截 SSH 登录尝试：" + user + " @" + ip,
				Object: ip,
				Detail: "账号=" + user + "；来源 IP=" + ip,
			})
		}
	}

	lines := 200
	if win >= 24*time.Hour {
		lines = 1000
	}
	if out, ok := run(4*time.Second, "lastb", "-n", strconv.Itoa(lines), "--time-format", "iso"); ok && strings.TrimSpace(out) != "" {
		for _, line := range strings.Split(out, "\n") {
			f := strings.Fields(line)
			if len(f) < 4 {
				continue
			}
			ts, err := parseLogTime(f[3])
			if err != nil || time.Since(ts) > win {
				continue
			}
			addHit(f[2], f[0], ts.Unix())
		}
		return count, topFailIPs(agg), events
	}

	// since 精度按窗口选择：短窗口用分钟，避免为 1 分钟窗口去拉一小时的日志
	since := "-" + strconv.Itoa(int(win/time.Hour)) + "h"
	if win < time.Hour {
		mins := int(win / time.Minute)
		if mins < 1 {
			mins = 1
		}
		since = "-" + strconv.Itoa(mins) + "min"
	}
	nLines := 500
	if win > 24*time.Hour {
		nLines = 3000
	}
	out, ok := run(5*time.Second, "journalctl", "-u", "ssh", "-u", "sshd", "-u", "sshd@*",
		"--since", since, "-o", "short-iso", "-n", strconv.Itoa(nLines), "--no-pager")
	if !ok {
		return 0, nil, nil
	}
	for _, line := range strings.Split(out, "\n") {
		var user, ip string
		if m := reJournalFailed.FindStringSubmatch(line); m != nil {
			user, ip = m[1], m[2]
		} else if m := reInvalidUser.FindStringSubmatch(line); m != nil {
			user, ip = m[1], m[2]
		} else {
			continue
		}
		ts := time.Now()
		if f := strings.Fields(line); len(f) > 0 {
			if v, err := parseLogTime(f[0]); err == nil {
				ts = v
			}
		}
		if time.Since(ts) > win {
			continue
		}
		addHit(ip, user, ts.Unix())
	}
	return count, topFailIPs(agg), events
}

// topFailIPs 把聚合结果按失败次数降序排列，最多返回 10 条
func topFailIPs(agg map[string]*FailIP) []FailIP {
	out := make([]FailIP, 0, len(agg))
	for _, v := range agg {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].LastTS > out[j].LastTS
	})
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

// parseLogTime 兼容 lastb / journalctl 的常见时间格式
func parseLogTime(s string) (time.Time, error) {
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05-0700",
		"2006-01-02T15:04:05-07:00",
		"2006-01-02 15:04:05",
	}
	var err error
	for _, l := range layouts {
		var t time.Time
		if t, err = time.Parse(l, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, err
}

// fwConfPath 飞牛防火墙配置（由飞牛 firewall / security_service 读写）
const fwConfPath = "/usr/trim/etc/fw.conf"

// scanFirewall 判定飞牛防火墙是否真正开启：以 /usr/trim/etc/fw.conf 的 enable 为准，
// 规则条数只统计非 Docker 的丢弃类规则（仅用于展示，不影响判定）。
func scanFirewall() (on bool, rules int, known bool) {
	rules = countFirewallRules()
	raw, err := os.ReadFile(fwConfPath)
	if err != nil {
		return rules > 0, rules, false
	}
	if v, ok := fwConfEnabled(raw); ok {
		return v, rules, true
	}
	return rules > 0, rules, false
}

// fwConfEnabled 解析飞牛防火墙配置：顶层为 profile 数组，取 default 或首项
func fwConfEnabled(raw []byte) (bool, bool) {
	type fwProfile struct {
		Profile string `json:"profile"`
		Enable  bool   `json:"enable"`
	}
	var arr []fwProfile
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 {
		for _, p := range arr {
			if p.Profile == "default" {
				return p.Enable, true
			}
		}
		return arr[0].Enable, true
	}
	var one fwProfile
	if err := json.Unmarshal(raw, &one); err == nil {
		return one.Enable, true
	}
	return false, false
}

// countFirewallRules 统计 nft / iptables 中真正生效的丢弃类规则条数，
// 排除 Docker 自己生成的链以及本应用的封禁表。
func countFirewallRules() int {
	if out, ok := run(4*time.Second, "nft", "list", "ruleset"); ok && strings.TrimSpace(out) != "" {
		rules := 0
		table, chain := "", ""
		for _, line := range strings.Split(out, "\n") {
			l := strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(l, "table "):
				table = l
				chain = ""
				continue
			case strings.HasPrefix(l, "chain "):
				chain = strings.TrimSuffix(strings.TrimPrefix(l, "chain "), " {")
				continue
			}
			if l == "" || l == "}" || strings.HasPrefix(l, "type ") || strings.HasPrefix(l, "policy ") {
				continue
			}
			if strings.HasSuffix(table, " "+nftTable) {
				continue // 本应用封禁表不计入系统防火墙规则
			}
			if strings.HasPrefix(chain, "DOCKER") || strings.HasPrefix(chain, "BR-") {
				continue
			}
			if strings.Contains(l, "drop") || strings.Contains(l, "reject") {
				rules++
			}
		}
		return rules
	}
	if out, ok := run(4*time.Second, "iptables", "-S"); ok {
		rules := 0
		for _, line := range strings.Split(out, "\n") {
			l := strings.TrimSpace(line)
			if !strings.HasPrefix(l, "-A ") {
				continue
			}
			f := strings.Fields(l)
			if len(f) < 2 {
				continue
			}
			if strings.HasPrefix(f[1], "DOCKER") || strings.HasPrefix(f[1], "BR-") {
				continue
			}
			if strings.Contains(l, "DROP") || strings.Contains(l, "REJECT") {
				rules++
			}
		}
		return rules
	}
	return 0
}

// scanFail2Ban 检测 fail2ban 是否启用、当前封禁数以及被封禁的 IP 列表
func scanFail2Ban() (bool, int, []string) {
	out, ok := run(5*time.Second, "fail2ban-client", "status")
	if !ok {
		return false, 0, nil
	}
	m := reJailList.FindStringSubmatch(out)
	if m == nil {
		return false, 0, nil
	}
	jails := strings.Split(m[1], ",")
	banned := 0
	seen := map[string]bool{}
	list := make([]string, 0, 16)
	for i, jail := range jails {
		if i >= 3 {
			break
		}
		jail = strings.TrimSpace(jail)
		if jail == "" {
			continue
		}
		if res, ok := run(5*time.Second, "fail2ban-client", "status", jail); ok {
			if bm := reBanned.FindStringSubmatch(res); bm != nil {
				if v, err := strconv.Atoi(bm[1]); err == nil {
					banned += v
				}
			}
			if lm := reBannedList.FindStringSubmatch(res); lm != nil {
				for _, ip := range strings.Fields(lm[1]) {
					if ip == "" || seen[ip] {
						continue
					}
					seen[ip] = true
					if len(list) < 50 {
						list = append(list, ip)
					}
				}
			}
		}
	}
	return true, banned, list
}

// scanSMART 用 smartctl 检查各硬盘健康状态（最多 8 块）
func scanSMART() (int, int) {
	out, ok := run(4*time.Second, "smartctl", "--scan")
	if !ok {
		return 0, 0
	}
	checked, failed := 0, 0
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 1 || !strings.HasPrefix(f[0], "/dev/") {
			continue
		}
		if checked >= 8 {
			break
		}
		res, ok := runAny(6*time.Second, "smartctl", "-H", f[0])
		if !ok || strings.TrimSpace(res) == "" {
			continue
		}
		checked++
		if strings.Contains(res, "FAILED!") || strings.Contains(res, "result: FAILED") {
			failed++
		}
	}
	return checked, failed
}

// scanSSHD 读取 sshd 配置，判断是否允许密码登录
func scanSSHD() (bool, bool) {
	files := []string{"/etc/ssh/sshd_config"}
	if extra, err := filepath.Glob("/etc/ssh/sshd_config.d/*.conf"); err == nil {
		files = append(files, extra...)
	}
	known := false
	passwordAuth := true
	for _, p := range files {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		known = true
		for _, line := range strings.Split(string(raw), "\n") {
			l := strings.TrimSpace(line)
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			f := strings.Fields(strings.ToLower(l))
			if len(f) < 2 {
				continue
			}
			if f[0] == "passwordauthentication" {
				passwordAuth = f[1] == "yes"
			}
		}
	}
	return passwordAuth, known
}

// nftUsedBefore 是否曾用 nft 兜底封禁过
func (w *Watcher) nftUsedBefore() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.nftUsed
}

// setNftUsed 标记已使用 nft 兜底封禁
func (w *Watcher) setNftUsed() {
	w.mu.Lock()
	w.nftUsed = true
	w.mu.Unlock()
}

// SetBanned 封禁（ban=true）或解封指定 IP，返回实际使用的后端（fail2ban / nft）。
// dur 为封禁时长；解封时忽略该参数。
// 优先 fail2ban；未安装时改用本应用自建的 nft 兜底封禁表。
func (w *Watcher) SetBanned(ip string, ban bool, dur time.Duration) (string, error) {
	ip = strings.TrimSpace(ip)
	if err := banAddressAllowed(ip); err != nil {
		return "", err
	}
	if jail, err := jailName(); err == nil {
		if ban {
			if err := fail2banBan(jail, ip, dur); err != nil {
				return "", err
			}
			return "fail2ban", nil
		}
		if _, ok := run(6*time.Second, "fail2ban-client", "set", jail, "unbanip", ip); !ok {
			return "", errors.New("fail2ban 执行失败，请确认 fail2ban 服务正在运行")
		}
		return "fail2ban", nil
	}
	if ban {
		if err := nftBan(ip, dur); err != nil {
			return "", err
		}
		w.setNftUsed()
		return "nft", nil
	}
	if err := nftUnban(ip); err != nil {
		return "", err
	}
	return "nft", nil
}

// fail2banBan 按指定时长封禁：fail2ban 的封禁时长由 jail 的 bantime 决定，
// 因此先临时改写 bantime，封禁后立即恢复原值，避免影响 fail2ban 自身的自动封禁策略。
func fail2banBan(jail, ip string, dur time.Duration) error {
	orig, known := fail2banBantime(jail)
	if dur >= time.Second {
		_ = setFail2banBantime(jail, strconv.FormatInt(int64(dur/time.Second), 10))
	}
	_, ok := run(6*time.Second, "fail2ban-client", "set", jail, "banip", ip)
	if known {
		_ = setFail2banBantime(jail, orig)
	}
	if !ok {
		return errors.New("fail2ban 执行失败，请确认 fail2ban 服务正在运行")
	}
	return nil
}

// fail2banBantime 读取 jail 当前的 bantime（秒），用于封禁后恢复
func fail2banBantime(jail string) (string, bool) {
	out, ok := run(5*time.Second, "fail2ban-client", "get", jail, "bantime")
	if !ok {
		return "", false
	}
	v := strings.TrimSpace(out)
	if v == "" {
		return "", false
	}
	return v, true
}

// setFail2banBantime 临时设置 jail 的 bantime
func setFail2banBantime(jail, val string) bool {
	_, ok := run(6*time.Second, "fail2ban-client", "set", jail, "bantime", val)
	return ok
}

// jailName 选择封禁使用的 jail：优先 sshd，其次第一个可用 jail。
func jailName() (string, error) {
	out, ok := run(5*time.Second, "fail2ban-client", "status")
	if !ok {
		return "", errors.New("未检测到 fail2ban，请先在应用中心安装并启动")
	}
	m := reJailList.FindStringSubmatch(out)
	if m == nil {
		return "", errors.New("fail2ban 未启用任何 jail，无法执行封禁")
	}
	first := ""
	for _, raw := range strings.Split(m[1], ",") {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if first == "" {
			first = name
		}
		if name == "sshd" {
			return name, nil
		}
	}
	if first == "" {
		return "", errors.New("fail2ban 未启用任何 jail，无法执行封禁")
	}
	return first, nil
}

// run 执行命令，只关心是否成功（退出码 0）
func run(timeout time.Duration, name string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

// runAny 执行命令并忽略退出码（smartctl 正常退出码可能是 0/4/64…）
func runAny(timeout time.Duration, name string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if len(out) == 0 && err != nil {
		return "", false
	}
	return string(out), true
}

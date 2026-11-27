// Package metrics 定时采样真实系统指标，计算健康指数并持久化到
// data/samples.jsonl（保留 7 天），为趋势图提供真实历史数据。
package metrics

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"atforcefield/internal/config"
	"atforcefield/internal/shield"
	"atforcefield/internal/system"
)

// Interval 采样间隔
const Interval = 30 * time.Second

// FastInterval 实时指标（网速 / CPU / 磁盘吞吐）刷新间隔。
// 只更新「当前值」，不落盘、不参与告警，因此可以远小于 Interval。
const FastInterval = 2 * time.Second

// Retain 采样数据保留时长
const Retain = 7 * 24 * time.Hour

// maxLines 磁盘行数超过该值时按内存中的有效数据重写文件
const maxLines = 30000

// Sample 一个采样点
type Sample struct {
	TS      int64   `json:"ts"`
	Health  int     `json:"health"`
	CPU     int     `json:"cpu"`
	MemPct  int     `json:"memPct"`
	DiskPct int     `json:"diskPct"`
	Load1   float64 `json:"load1"`
	NetRx   int64   `json:"netRx"`
	NetTx   int64   `json:"netTx"`
	Conns   int     `json:"conns"`
	TempC   float64 `json:"tempC"`
}

// Point 趋势图上的一个点（保持与原接口一致的 JSON 结构）
type Point struct {
	TS      int64 `json:"ts"`
	Percent int   `json:"percent"`
}

// Collector 周期采样器
type Collector struct {
	sampler *system.Sampler
	path    string
	file    *os.File

	mu      sync.RWMutex
	samples []Sample
	last    system.Metrics
	lines   int

	// OnSample 每次采样落盘后被回调，用于阈值告警评估（由 main 注入，避免反向依赖）
	onSample func(system.Metrics, int)
}

// SetOnSample 注册采样回调（健康指数告警等）
func (c *Collector) SetOnSample(fn func(system.Metrics, int)) {
	c.mu.Lock()
	c.onSample = fn
	c.mu.Unlock()
}

// Open 读取历史采样并打开追加写入的文件
func Open(dir string) (*Collector, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	c := &Collector{sampler: system.NewSampler(), path: filepath.Join(dir, "samples.jsonl")}
	c.load()

	f, err := os.OpenFile(c.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	c.file = f

	// 立刻采一帧，保证首屏就有真实指标（CPU/吞吐需要上一帧，首次为 -1）
	c.last = c.sampler.Sample()
	if len(c.samples) == 0 {
		c.record(c.last)
	}
	return c, nil
}

// Path 返回采样文件路径
func (c *Collector) Path() string { return c.path }

// Start 启动后台采样：完整采样每 Interval 落盘一次（历史 + 告警），
// 另外每 FastInterval 只刷新一次「当前值」，让前端实时网速/CPU 不必等 30 秒。
func (c *Collector) Start() {
	go func() {
		time.Sleep(1200 * time.Millisecond)
		c.tick()
		fast := time.NewTicker(FastInterval)
		defer fast.Stop()
		slow := time.NewTicker(Interval)
		defer slow.Stop()
		for {
			select {
			case <-fast.C:
				c.liveTick()
			case <-slow.C:
				c.tick()
			}
		}
	}()
}

// liveTick 只刷新当前值，不写历史、不触发告警（供实时刷新用）
func (c *Collector) liveTick() {
	m := c.sampler.SampleFast()
	c.mu.Lock()
	c.last = m
	c.mu.Unlock()
}

// Close 关闭采样文件
func (c *Collector) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.file != nil {
		_ = c.file.Close()
		c.file = nil
	}
}

// Current 返回最近一次采样结果
func (c *Collector) Current() system.Metrics {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.last
}

func (c *Collector) tick() {
	m := c.sampler.Sample()
	c.mu.Lock()
	c.last = m
	c.mu.Unlock()
	c.record(m)
}

// record 计算健康指数并落盘
func (c *Collector) record(m system.Metrics) {
	s := Sample{
		TS:      time.Now().Unix(),
		Health:  shield.HealthIndex(m),
		CPU:     m.CPUUsagePct,
		MemPct:  memPct(m),
		DiskPct: diskPct(m),
		Load1:   m.LoadAvg1,
		NetRx:   m.NetRxKBps,
		NetTx:   m.NetTxKBps,
		Conns:   m.EstablishedConns,
		TempC:   m.TempC,
	}

	c.mu.Lock()
	c.samples = append(c.samples, s)
	c.trimLocked()
	c.mu.Unlock()

	c.appendLine(s)

	c.mu.RLock()
	fn := c.onSample
	c.mu.RUnlock()
	if fn != nil {
		fn(m, s.Health)
	}
}

// History 按时间范围聚合真实采样：把窗口切成 n 个桶取平均，
// 缺失的桶沿用上一个已知值（没有历史数据时返回 nil）。
func (c *Collector) History(n int, stepSecs int) []Point {
	if n < 2 {
		n = 2
	}
	if stepSecs <= 0 {
		stepSecs = 21600
	}
	c.mu.RLock()
	samples := make([]Sample, len(c.samples))
	copy(samples, c.samples)
	c.mu.RUnlock()
	if len(samples) == 0 {
		return nil
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].TS < samples[j].TS })

	now := time.Now().Unix()
	step := int64(stepSecs)
	start := now - int64(n-1)*step

	sums := make([]int, n)
	counts := make([]int, n)
	for _, s := range samples {
		if s.TS < start-step/2 || s.TS > now+step/2 {
			continue
		}
		idx := int((s.TS - start + step/2) / step)
		if idx < 0 || idx >= n {
			continue
		}
		sums[idx] += s.Health
		counts[idx]++
	}

	first := -1
	for i := 0; i < n; i++ {
		if counts[i] > 0 {
			first = int(math.Round(float64(sums[i]) / float64(counts[i])))
			break
		}
	}
	if first < 0 {
		return nil
	}

	out := make([]Point, 0, n)
	val := first
	for i := 0; i < n; i++ {
		if counts[i] > 0 {
			val = int(math.Round(float64(sums[i]) / float64(counts[i])))
		}
		out = append(out, Point{TS: start + int64(i)*step, Percent: val})
	}
	// 末点直接取最近一次采样值，保证趋势图终点与总览的健康指数一致
	out[n-1].Percent = samples[len(samples)-1].Health
	return out
}

// NetPoint 网络吞吐趋势点（KB/s）
type NetPoint struct {
	TS int64 `json:"ts"`
	Rx int64 `json:"rx"`
	Tx int64 `json:"tx"`
}

// NetHistory 按时间范围聚合网络吞吐：与 History 相同的分桶逻辑，取桶内均值。
func (c *Collector) NetHistory(n int, stepSecs int) []NetPoint {
	if n < 2 {
		n = 2
	}
	if stepSecs <= 0 {
		stepSecs = 600
	}
	c.mu.RLock()
	samples := make([]Sample, len(c.samples))
	copy(samples, c.samples)
	c.mu.RUnlock()
	if len(samples) == 0 {
		return nil
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].TS < samples[j].TS })

	now := time.Now().Unix()
	step := int64(stepSecs)
	start := now - int64(n-1)*step

	rx := make([]int64, n)
	tx := make([]int64, n)
	counts := make([]int, n)
	for _, s := range samples {
		if s.TS < start-step/2 || s.TS > now+step/2 {
			continue
		}
		idx := int((s.TS - start + step/2) / step)
		if idx < 0 || idx >= n {
			continue
		}
		if s.NetRx > 0 {
			rx[idx] += s.NetRx
		}
		if s.NetTx > 0 {
			tx[idx] += s.NetTx
		}
		counts[idx]++
	}

	out := make([]NetPoint, 0, n)
	var lastRx, lastTx int64
	for i := 0; i < n; i++ {
		v := NetPoint{TS: start + int64(i)*step}
		if counts[i] > 0 {
			v.Rx = rx[i] / int64(counts[i])
			v.Tx = tx[i] / int64(counts[i])
			lastRx, lastTx = v.Rx, v.Tx
		} else {
			v.Rx, v.Tx = lastRx, lastTx
		}
		out = append(out, v)
	}
	// 末点取最近一次采样，保证与总览实时速率一致
	out[n-1].Rx = samples[len(samples)-1].NetRx
	out[n-1].Tx = samples[len(samples)-1].NetTx
	return out
}

// TrafficPoint 一个时间桶内的流量（字节）
type TrafficPoint struct {
	TS int64 `json:"ts"`
	Rx int64 `json:"rx"`
	Tx int64 `json:"tx"`
}

// NetTraffic 按时间范围累计各桶流量：用 30 秒采样的速率 × 实际间隔积分得出。
// 采样中断（应用停机 > 2 个周期）的区间直接跳过，避免把停机时长算成流量。
func (c *Collector) NetTraffic(n int, stepSecs int) []TrafficPoint {
	if n < 2 {
		n = 2
	}
	if stepSecs <= 0 {
		stepSecs = 600
	}
	c.mu.RLock()
	samples := make([]Sample, len(c.samples))
	copy(samples, c.samples)
	c.mu.RUnlock()
	if len(samples) < 2 {
		return nil
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].TS < samples[j].TS })

	now := time.Now().Unix()
	step := int64(stepSecs)
	start := now - int64(n-1)*step

	out := make([]TrafficPoint, n)
	for i := range out {
		out[i].TS = start + int64(i)*step
	}
	maxGap := int64(2 * Interval / time.Second)
	for i := 1; i < len(samples); i++ {
		cur, prev := samples[i], samples[i-1]
		if cur.TS < start-step/2 || cur.TS > now+step/2 {
			continue
		}
		dt := cur.TS - prev.TS
		if dt <= 0 || dt > maxGap {
			continue
		}
		idx := int((cur.TS - start + step/2) / step)
		if idx < 0 || idx >= n {
			continue
		}
		if cur.NetRx > 0 {
			out[idx].Rx += cur.NetRx * 1024 * dt
		}
		if cur.NetTx > 0 {
			out[idx].Tx += cur.NetTx * 1024 * dt
		}
	}
	return out
}

// Count 返回当前的采样点数（用于“数据来源”展示）
func (c *Collector) Count() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.samples)
}

func (c *Collector) load() {
	f, err := os.Open(c.path)
	if err != nil {
		return
	}
	defer f.Close()

	cutoff := time.Now().Add(-Retain).Unix()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	dropped := false
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var s Sample
		if err := json.Unmarshal(line, &s); err != nil || s.TS == 0 {
			dropped = true
			continue
		}
		if s.TS < cutoff {
			dropped = true
			continue
		}
		c.samples = append(c.samples, s)
	}
	if dropped {
		c.rewrite()
	}
	c.lines = len(c.samples)
}

func (c *Collector) appendLine(s Sample) {
	raw, err := json.Marshal(s)
	if err != nil {
		return
	}
	c.mu.Lock()
	f := c.file
	c.lines++
	lines := c.lines
	total := len(c.samples)
	c.mu.Unlock()

	if f != nil {
		if _, err := f.Write(append(raw, '\n')); err != nil {
			return
		}
	}
	if lines > maxLines && lines > total*2 {
		c.rewrite()
	}
}

// rewrite 用内存中的有效采样重写文件，实现环形裁剪
func (c *Collector) rewrite() {
	c.mu.Lock()
	samples := make([]Sample, len(c.samples))
	copy(samples, c.samples)
	c.mu.Unlock()

	tmp := c.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return
	}
	w := bufio.NewWriter(f)
	for _, s := range samples {
		raw, err := json.Marshal(s)
		if err != nil {
			continue
		}
		_, _ = w.Write(append(raw, '\n'))
	}
	_ = w.Flush()
	_ = f.Close()
	_ = os.Rename(tmp, filepath.Clean(c.path))

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.file != nil {
		_ = c.file.Close()
	}
	if nf, err := os.OpenFile(c.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		c.file = nf
	} else {
		c.file = nil
	}
	c.lines = len(samples)
}

func (c *Collector) trimLocked() {
	cutoff := time.Now().Add(-Retain).Unix()
	i := 0
	for i < len(c.samples) && c.samples[i].TS < cutoff {
		i++
	}
	if i > 0 {
		c.samples = append([]Sample(nil), c.samples[i:]...)
	}
}

func memPct(m system.Metrics) int {
	if m.TotalMemMB <= 0 {
		return 0
	}
	return clampInt(int(math.Round(float64(m.TotalMemMB-m.AvailableMB)/float64(m.TotalMemMB)*100)), 0, 100)
}

func diskPct(m system.Metrics) int {
	if m.DiskTotalGB <= 0 {
		return 0
	}
	return clampInt(int(math.Round(float64(m.DiskUsedGB)/float64(m.DiskTotalGB)*100)), 0, 100)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Dir 返回采样数据目录（供外部打印路径）
func Dir() string { return config.DataDir() }

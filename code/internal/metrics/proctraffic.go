package metrics

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"atforcefield/internal/system"
)

// 进程级流量：内核只对 TCP socket 暴露累计字节数，所以这里每 3 秒读一次
// ss -tinp 的 bytes_sent / bytes_received 做差分，再按进程累加。
// 分钟级聚合先放内存，每分钟落盘一行，避免每次采样都写硬盘。
const (
	procSampleInterval = 3 * time.Second
	procRetain         = 7 * 24 * time.Hour // 明细与采样数据同样保留 7 天
	procMinuteKeep     = 25 * time.Hour     // 分钟桶只留最近 25 小时，更早的合并为小时桶
	procMaxLines       = 20000
	procMaxGap         = 120 // 采样中断超过该秒数则丢弃旧快照，避免把缺口算进一分钟
)

// ProcTrafficPoint 单个进程在时间窗口内的流量（字节）
type ProcTrafficPoint struct {
	Name string `json:"name"`
	PID  int    `json:"pid"`
	Rx   int64  `json:"rx"`
	Tx   int64  `json:"tx"`
}

// procAgg 一个进程的累计量
type procAgg struct {
	pid int
	rx  int64
	tx  int64
}

// procLine 落盘的一行：某分钟内各进程的增量
type procLine struct {
	TS int64               `json:"ts"`
	P  map[string][3]int64 `json:"p"` // 进程名 -> [pid, rx, tx]
}

// ProcTraffic 按进程累计 TCP 流量
type ProcTraffic struct {
	path string
	file *os.File
	w    *bufio.Writer

	mu       sync.RWMutex
	started  bool
	snapshot map[string][2]uint64 // socket key -> 上次累计 [sent, recv]
	lastSnap int64
	cur      map[string]*procAgg // 本分钟累计
	curStart int64
	minute   map[int64]map[string]*procAgg
	hour     map[int64]map[string]*procAgg
	lines    int
	updated  int64

	stop chan struct{}
	done chan struct{}
}

// OpenProcTraffic 读取历史明细并打开追加写入的文件
func OpenProcTraffic(dir string) (*ProcTraffic, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	p := &ProcTraffic{
		path:     filepath.Join(dir, "proc_traffic.jsonl"),
		snapshot: map[string][2]uint64{},
		cur:      map[string]*procAgg{},
		minute:   map[int64]map[string]*procAgg{},
		hour:     map[int64]map[string]*procAgg{},
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	p.load()

	f, err := os.OpenFile(p.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	p.file = f
	p.w = bufio.NewWriterSize(f, 32*1024)
	p.curStart = time.Now().Unix() / 60 * 60
	return p, nil
}

// Start 启动后台采样（每 3 秒一次，关闭时把当前分钟落盘）
func (p *ProcTraffic) Start() {
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return
	}
	p.started = true
	p.mu.Unlock()

	go func() {
		defer close(p.done)
		t := time.NewTicker(procSampleInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				p.sample()
			case <-p.stop:
				p.closeMinute()
				return
			}
		}
	}()
}

// Close 停止采样并刷新缓冲区
func (p *ProcTraffic) Close() {
	p.mu.Lock()
	started := p.started
	p.mu.Unlock()
	if started {
		select {
		case <-p.stop:
		default:
			close(p.stop)
		}
		<-p.done
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.w != nil {
		_ = p.w.Flush()
		p.w = nil
	}
	if p.file != nil {
		_ = p.file.Close()
		p.file = nil
	}
}

// Updated 最近一次成功采样的时间
func (p *ProcTraffic) Updated() int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.updated
}

// Top 返回窗口内流量最大的若干进程（按下行 + 上行排序）
func (p *ProcTraffic) Top(window time.Duration, top int) []ProcTrafficPoint {
	now := time.Now().Unix()
	start := now - int64(window/time.Second)

	agg := map[string]*procAgg{}
	p.mu.RLock()
	for ts, b := range p.minute {
		if ts+60 <= start {
			continue
		}
		mergeBucket(agg, b)
	}
	for h, b := range p.hour {
		if h+3600 <= start {
			continue
		}
		mergeBucket(agg, b)
	}
	p.mu.RUnlock()

	out := make([]ProcTrafficPoint, 0, len(agg))
	for name, a := range agg {
		if a.rx == 0 && a.tx == 0 {
			continue
		}
		out = append(out, ProcTrafficPoint{Name: name, PID: a.pid, Rx: a.rx, Tx: a.tx})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Rx+out[i].Tx > out[j].Rx+out[j].Tx
	})
	if top > 0 && len(out) > top {
		out = out[:top]
	}
	return out
}

// sample 读一次全部 socket 的累计字节，与上次快照差分后累加到进程
func (p *ProcTraffic) sample() {
	socks, ok := system.TrafficSockets()
	now := time.Now().Unix()

	p.mu.Lock()
	if !ok {
		// ss 不可用：保留旧快照，下次成功时再按实际增量结算
		p.updated = now
		p.mu.Unlock()
		return
	}
	if p.lastSnap > 0 && now-p.lastSnap > procMaxGap {
		p.snapshot = map[string][2]uint64{}
	}
	next := make(map[string][2]uint64, len(socks))
	for _, s := range socks {
		cur := [2]uint64{s.Sent, s.Recv}
		next[s.Key] = cur
		prev, ok := p.snapshot[s.Key]
		if !ok {
			continue
		}
		var dtx, drx uint64
		if cur[0] >= prev[0] {
			dtx = cur[0] - prev[0]
		}
		if cur[1] >= prev[1] {
			drx = cur[1] - prev[1]
		}
		if drx == 0 && dtx == 0 {
			continue
		}
		a := p.cur[s.Process]
		if a == nil {
			a = &procAgg{}
			p.cur[s.Process] = a
		}
		a.pid = s.PID
		a.rx += int64(drx)
		a.tx += int64(dtx)
	}
	p.snapshot = next
	p.lastSnap = now
	p.updated = now
	p.rollLocked(now)
	p.mu.Unlock()
}

// rollLocked 跨分钟时把上一分钟存档并落盘（调用方需持有锁）
func (p *ProcTraffic) rollLocked(now int64) {
	m := now - now%60
	if p.curStart == 0 {
		p.curStart = m
		return
	}
	if m <= p.curStart {
		return
	}
	if len(p.cur) > 0 {
		p.minute[p.curStart] = p.cur
		p.appendLineLocked(p.curStart, p.cur)
	}
	p.cur = map[string]*procAgg{}
	p.curStart = m
	p.pruneLocked(now)
}

// closeMinute 退出前把当前不足一分钟的数据也写下去，避免丢最后一段
func (p *ProcTraffic) closeMinute() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.cur) > 0 {
		p.minute[p.curStart] = p.cur
		p.appendLineLocked(p.curStart, p.cur)
		p.cur = map[string]*procAgg{}
	}
	if p.w != nil {
		_ = p.w.Flush()
	}
}

// pruneLocked 把超过 25 小时的分钟桶并入小时桶，并清掉 7 天前的数据
func (p *ProcTraffic) pruneLocked(now int64) {
	cut := now - int64(procRetain/time.Second)
	mCut := now - int64(procMinuteKeep/time.Second)
	for ts, bucket := range p.minute {
		if ts >= mCut {
			continue
		}
		h := ts - ts%3600
		dst := p.hour[h]
		if dst == nil {
			dst = map[string]*procAgg{}
			p.hour[h] = dst
		}
		mergeBucket(dst, bucket)
		delete(p.minute, ts)
	}
	for h := range p.hour {
		if h < cut {
			delete(p.hour, h)
		}
	}
}

// appendLineLocked 写入一行的分钟增量（每分钟一次，写后立即 flush）
func (p *ProcTraffic) appendLineLocked(ts int64, bucket map[string]*procAgg) {
	if p.w == nil {
		return
	}
	rec := procLine{TS: ts, P: make(map[string][3]int64, len(bucket))}
	for name, a := range bucket {
		if a.rx == 0 && a.tx == 0 {
			continue
		}
		rec.P[name] = [3]int64{int64(a.pid), a.rx, a.tx}
	}
	if len(rec.P) == 0 {
		return
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return
	}
	if _, err := p.w.Write(append(raw, '\n')); err != nil {
		return
	}
	_ = p.w.Flush()
	p.lines++
	if p.lines > procMaxLines {
		p.rewriteLocked()
	}
}

// rewriteLocked 用内存中的聚合数据重写文件（环形裁剪）并重开追加句柄
func (p *ProcTraffic) rewriteLocked() {
	p.writeFile()
	if p.file != nil {
		_ = p.file.Close()
	}
	if nf, err := os.OpenFile(p.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		p.file = nf
		p.w = bufio.NewWriterSize(nf, 32*1024)
	} else {
		p.file = nil
		p.w = nil
	}
}

// writeFile 把内存中的小时桶与分钟桶写成新文件并原子替换
func (p *ProcTraffic) writeFile() {
	tmp := p.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return
	}
	w := bufio.NewWriter(f)
	write := func(ts int64, b map[string]*procAgg) {
		if len(b) == 0 {
			return
		}
		rec := procLine{TS: ts, P: make(map[string][3]int64, len(b))}
		for name, a := range b {
			if a.rx == 0 && a.tx == 0 {
				continue
			}
			rec.P[name] = [3]int64{int64(a.pid), a.rx, a.tx}
		}
		if len(rec.P) == 0 {
			return
		}
		raw, err := json.Marshal(rec)
		if err != nil {
			return
		}
		_, _ = w.Write(append(raw, '\n'))
	}
	for ts, b := range p.hour {
		write(ts, b)
	}
	for ts, b := range p.minute {
		write(ts, b)
	}
	_ = w.Flush()
	_ = f.Close()
	_ = os.Rename(tmp, filepath.Clean(p.path))
	p.lines = len(p.hour) + len(p.minute)
}

func (p *ProcTraffic) load() {
	f, err := os.Open(p.path)
	if err != nil {
		return
	}
	defer f.Close()

	now := time.Now().Unix()
	cut := now - int64(procRetain/time.Second)
	mCut := now - int64(24*time.Hour/time.Second)
	dropped := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec procLine
		if err := json.Unmarshal(line, &rec); err != nil || rec.TS == 0 {
			dropped = true
			continue
		}
		if rec.TS < cut {
			dropped = true
			continue
		}
		bucket := make(map[string]*procAgg, len(rec.P))
		for name, v := range rec.P {
			if v[1] == 0 && v[2] == 0 {
				continue
			}
			bucket[name] = &procAgg{pid: int(v[0]), rx: v[1], tx: v[2]}
		}
		if len(bucket) == 0 {
			continue
		}
		if rec.TS < mCut {
			h := rec.TS - rec.TS%3600
			dst := p.hour[h]
			if dst == nil {
				dst = map[string]*procAgg{}
				p.hour[h] = dst
			}
			mergeBucket(dst, bucket)
		} else {
			p.minute[rec.TS] = bucket
		}
		p.lines++
	}
	if dropped || p.lines > procMaxLines {
		p.writeFile()
	}
}

// mergeBucket 把 src 累加进 dst（同名进程合并，PID 取非零的最近值）
func mergeBucket(dst, src map[string]*procAgg) {
	for name, s := range src {
		d := dst[name]
		if d == nil {
			d = &procAgg{}
			dst[name] = d
		}
		if s.pid > 0 {
			d.pid = s.pid
		}
		d.rx += s.rx
		d.tx += s.tx
	}
}

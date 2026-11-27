// Package audit 记录应用自身的真实操作事件（启用/关闭防护、修改配置等），
// 写入 data/audit.jsonl 并供“防护日志”页展示，替代原来的随机模拟日志。
package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"atforcefield/internal/shield"
)

// maxEntries 内存中保留的最大条数（约最近数千次操作）
const maxEntries = 2000

// maxFileBytes 日志文件达到该大小时轮转，防止长期运行无限增长
const maxFileBytes = 8 << 20

// Store 审计日志仓库
type Store struct {
	mu      sync.Mutex
	path    string
	file    *os.File
	entries []shield.Log
}

// Open 读取历史审计日志并打开追加写入
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "audit.jsonl")}
	s.load()
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	s.file = f
	return s, nil
}

// Add 追加一条审计事件（默认来源：应用，操作者：系统）
func (s *Store) Add(level, module, msg string) {
	s.AddDetail(shield.Log{Level: level, Module: module, Msg: msg, Source: "应用", Actor: "系统"})
}

// AddAt 以指定时间追加一条审计事件（用于回填真实安全事件的时间戳）
func (s *Store) AddAt(ts int64, level, module, msg string) {
	s.AddDetail(shield.Log{Time: ts, Level: level, Module: module, Msg: msg, Source: "巡检", Actor: "系统"})
}

// AddDetail 追加一条带结构化字段的事件（对象 / 详情 / 来源 / 操作者），
// 未填写来源时默认「应用」，未填写操作者时默认「系统」。
func (s *Store) AddDetail(e shield.Log) {
	if e.Time <= 0 {
		e.Time = time.Now().Unix()
	}
	if e.Source == "" {
		e.Source = "应用"
	}
	if e.Actor == "" {
		e.Actor = "系统"
	}

	s.mu.Lock()
	s.entries = append(s.entries, e)
	if len(s.entries) > maxEntries {
		s.entries = append([]shield.Log(nil), s.entries[len(s.entries)-maxEntries:]...)
	}
	f := s.file
	// 文件超限时轮转：当前文件改名为 .1（覆盖旧备份），再重开新文件
	if f != nil {
		if fi, err := f.Stat(); err == nil && fi.Size() > maxFileBytes {
			_ = f.Close()
			_ = os.Rename(s.path, s.path+".1")
			s.file = nil
			f = nil
			if nf, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
				s.file = nf
				f = nf
			}
		}
	}
	s.mu.Unlock()

	if f == nil {
		return
	}
	if raw, err := json.Marshal(e); err == nil {
		_, _ = f.Write(append(raw, '\n'))
	}
}

// List 返回最近 n 条审计日志（时间倒序）
func (s *Store) List(n int) []shield.Log {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n <= 0 || n > len(s.entries) {
		n = len(s.entries)
	}
	out := make([]shield.Log, 0, n)
	for i := len(s.entries) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, s.entries[i])
	}
	return out
}

// Count 返回已记录的条数
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// Query 按级别、关键字与对象过滤后分页返回（时间倒序），同时返回命中总数。
// level 为空表示全部；q 会在模块、内容、对象与详情中做不区分大小写的包含匹配；
// ip 非空时只返回对象/详情/内容中命中该 IP 的记录。
func (s *Store) Query(n, offset int, level, q, ip string) ([]shield.Log, int) {
	if n <= 0 || n > 500 {
		n = 50
	}
	if offset < 0 {
		offset = 0
	}
	q = strings.ToLower(strings.TrimSpace(q))
	ip = strings.ToLower(strings.TrimSpace(ip))

	s.mu.Lock()
	defer s.mu.Unlock()

	matched := make([]shield.Log, 0, len(s.entries))
	for i := len(s.entries) - 1; i >= 0; i-- {
		e := s.entries[i]
		if level != "" && e.Level != level {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(e.Module+" "+e.Msg+" "+e.Object+" "+e.Detail), q) {
			continue
		}
		if ip != "" {
			hay := strings.ToLower(e.Object + " " + e.Detail + " " + e.Msg)
			if !strings.Contains(hay, ip) {
				continue
			}
		}
		matched = append(matched, e)
	}
	total := len(matched)
	if offset >= total {
		return []shield.Log{}, total
	}
	end := offset + n
	if end > total {
		end = total
	}
	return matched[offset:end], total
}

// Close 关闭文件
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
}

func (s *Store) load() {
	f, err := os.Open(s.path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e shield.Log
		if err := json.Unmarshal(line, &e); err != nil || e.Time == 0 {
			continue
		}
		s.entries = append(s.entries, e)
	}
	if len(s.entries) > maxEntries {
		s.entries = append([]shield.Log(nil), s.entries[len(s.entries)-maxEntries:]...)
	}
}

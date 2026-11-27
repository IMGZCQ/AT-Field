package weblog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCollectAndQuery(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "access.log")
	now := time.Now()
	ts := now.Format("02/Jan/2006:15:04:05 -0700")
	lines := []string{
		fmt.Sprintf(`1.2.3.4 - - [%s] "GET /index?x=1 HTTP/1.1" 200 12 "-" "UA"`, ts),
		fmt.Sprintf(`1.2.3.4 - - [%s] "GET /admin HTTP/1.1" 404 12 "-" "UA"`, ts),
		fmt.Sprintf(`5.6.7.8 - - [%s] "POST /login HTTP/1.1" 403 12 "-" "UA"`, ts),
		// 本应用自身的 SSE 轮询应被过滤
		fmt.Sprintf(`127.0.0.1 - - [%s] "GET /app/AT-ForceField/api/stream HTTP/1.1" 200 12 "-" "UA"`, ts),
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hour := now.Truncate(time.Hour).Unix()
	c := &Collector{
		dir: dir, basePath: "/app/AT-ForceField", hour: hour,
		bucket: newBucket(hour), lastSave: now,
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	c.logPath = logPath
	c.poll()

	q := c.Query("1h")
	if q.Total != 3 {
		t.Fatalf("总请求数 = %d, 期望 3", q.Total)
	}
	if len(q.IPs) != 2 || q.IPs[0].Count != 2 {
		t.Fatalf("来源 IP 聚合异常: %+v", q.IPs)
	}
	if len(q.Paths) != 3 || q.Paths[0].Path != "/admin" {
		t.Fatalf("路径聚合异常: %+v", q.Paths)
	}
	if len(q.Statuses) != 3 {
		t.Fatalf("状态码聚合异常: %+v", q.Statuses)
	}
	if q.Points[len(q.Points)-1].Count != 3 {
		t.Fatalf("当前 10 分钟计数 = %d, 期望 3", q.Points[len(q.Points)-1].Count)
	}

	// 保存后重新加载应按小时文件恢复
	c.mu.Lock()
	c.saveLocked()
	c.mu.Unlock()
	b := c.loadHour(hour)
	if b.Total != 3 || b.IPs["1.2.3.4"] != 2 {
		t.Fatalf("落盘/加载异常: %+v", b)
	}
}

func TestScanDetectionAndRecentBad(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	ts := now.Format("02/Jan/2006:15:04:05 -0700")
	var lines []string
	for i := 0; i < 25; i++ {
		lines = append(lines, fmt.Sprintf(`9.9.9.9 - - [%s] "GET /wp-admin HTTP/1.1" 404 12 "-" "UA"`, ts))
	}
	for i := 0; i < 5; i++ {
		lines = append(lines, fmt.Sprintf(`8.8.8.8 - - [%s] "GET / HTTP/1.1" 500 12 "-" "UA"`, ts))
	}
	hour := now.Truncate(time.Hour).Unix()
	c := &Collector{
		dir: dir, hour: hour, bucket: newBucket(hour), lastSave: now,
		recentBad: map[string][]int64{},
		stop:      make(chan struct{}), done: make(chan struct{}),
	}
	c.consume(strings.Join(lines, "\n") + "\n")

	q := c.Query("1h")
	if q.Bad != 30 {
		t.Fatalf("异常请求总数 = %d, 期望 30", q.Bad)
	}
	if len(q.Scans) != 1 || q.Scans[0].IP != "9.9.9.9" || q.Scans[0].Bad != 25 {
		t.Fatalf("疑似扫描判定异常: %+v", q.Scans)
	}
	flagged := false
	for _, it := range q.IPs {
		if it.IP == "9.9.9.9" {
			flagged = it.Scan && it.Bad == 25
		}
		if it.IP == "8.8.8.8" && it.Scan {
			t.Fatalf("异常次数未达阈值的 IP 不应标记扫描: %+v", it)
		}
	}
	if !flagged {
		t.Fatalf("疑似扫描 IP 未标记: %+v", q.IPs)
	}
	// 自动封禁用：近 10 分钟异常 ≥ 20 次的 IP
	got := c.RecentBadIPs(10*time.Minute, 20)
	if len(got) != 1 || got[0] != "9.9.9.9" {
		t.Fatalf("RecentBadIPs = %v, 期望 [9.9.9.9]", got)
	}
}

func TestQueryPrevWindowAnd30d(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	prev := now.Truncate(time.Hour).Add(-time.Hour)
	b := newBucket(prev.Unix())
	b.Total = 4
	b.Statuses["500"] = 1
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	hour := now.Truncate(time.Hour).Unix()
	c := &Collector{dir: dir, hour: hour, bucket: newBucket(hour)}
	if err := os.WriteFile(c.pathFor(prev.Unix()), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	q := c.Query("1h")
	if q.PrevTotal != 4 || q.PrevBad != 1 {
		t.Fatalf("环比窗口 = %d/%d, 期望 4/1", q.PrevTotal, q.PrevBad)
	}

	q30 := c.Query("30d")
	if len(q30.Points) != RetentionDays {
		t.Fatalf("30 天趋势点 = %d, 期望 %d", len(q30.Points), RetentionDays)
	}
	first := time.Unix(q30.Points[0].Ts, 0)
	want := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -(RetentionDays - 1))
	if !first.Equal(want) {
		t.Fatalf("30 天起点 = %v, 期望 %v", first, want)
	}
	if q30.PrevTotal != 0 || q30.PrevBad != 0 {
		t.Fatalf("30 天窗口不应计算环比: %d/%d", q30.PrevTotal, q30.PrevBad)
	}
}

func TestClientIPFromProxy(t *testing.T) {
	line := `127.0.0.1 - - [27/Nov/2026:10:00:00 +0800] "GET / HTTP/1.1" 200 1 "-" "8.8.8.8, 9.9.9.9"`
	if got := clientIP("127.0.0.1", line, "GET / HTTP/1.1"); got != "8.8.8.8" {
		t.Fatalf("X-Forwarded-For 解析 = %q, 期望 8.8.8.8", got)
	}
	if got := clientIP("1.2.3.4", line, "GET / HTTP/1.1"); got != "1.2.3.4" {
		t.Fatalf("公网来源 IP 不应被替换: %q", got)
	}
}

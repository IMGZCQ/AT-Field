// Package system 采集真实的 NAS/系统指标（运行时长、CPU、内存、磁盘、网络、
// 磁盘 IO、连接数、温度）。读取失败（非 Linux 或权限不足）时退化为模拟值，
// 并通过 Metrics.Source 标记为 "mock"，前端据此展示数据来源。
package system

import (
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Metrics 一次采样结果
type Metrics struct {
	BootTime    int64 `json:"bootTime"`    // 系统启动时间（Unix 秒）
	UptimeSecs  int64 `json:"uptimeSecs"`  // 已运行秒数
	DiskTotalGB int64 `json:"diskTotalGB"` // 根文件系统总容量（GB）
	DiskUsedGB  int64 `json:"diskUsedGB"`  // 已用（GB）
	TotalMemMB  int64 `json:"totalMemMB"`  // 物理内存（MB）
	AvailableMB int64 `json:"availableMB"` // 可用内存（MB）

	CPUUsagePct      int     `json:"cpuUsagePct"`      // CPU 使用率 0-100，-1 表示暂无有效差值
	CPUCores         int     `json:"cpuCores"`         // 逻辑核心数（线程数）
	CPUPhysical      int     `json:"cpuPhysical"`      // 物理核心数，0 表示无法识别
	LoadAvg1         float64 `json:"loadAvg1"`         // 1 分钟平均负载，-1 表示不可用
	NetRxKBps        int64   `json:"netRxKBps"`        // 网络下行（KB/s）
	NetTxKBps        int64   `json:"netTxKBps"`        // 网络上行（KB/s）
	DiskReadKBps     int64   `json:"diskReadKBps"`     // 磁盘读（KB/s）
	DiskWriteKBps    int64   `json:"diskWriteKBps"`    // 磁盘写（KB/s）
	EstablishedConns int     `json:"establishedConns"` // ESTABLISHED 的 TCP 连接数
	TempC            float64 `json:"tempC"`            // CPU 温度（摄氏度），-1 表示无传感器
	DiskTempC        float64 `json:"diskTempC"`        // 硬盘/SSD 最高温度，-1 表示无传感器
	LoggedInUsers    int     `json:"loggedInUsers"`    // 当前登录用户数
	UPSBattery       int     `json:"upsBattery"`       // UPS 电量百分比，-1 表示未接 UPS

	DiskVolumes []Volume `json:"diskVolumes"` // 各数据分区/存储池

	Source string `json:"source"` // "real" | "mock"
}

// Volume 单个挂载点的容量
type Volume struct {
	Device  string `json:"device"`
	Mount   string `json:"mount"`
	TotalGB int64  `json:"totalGB"`
	UsedGB  int64  `json:"usedGB"`
}

// Sampler 缓存上一帧快照，用于计算 CPU 使用率与网络/磁盘吞吐。
// 采样间隔过短（<500ms）时沿用上一次的计算值，避免出现巨大抖动。
type Sampler struct {
	mu       sync.Mutex
	last     time.Time
	prevCPU  uint64
	prevIdle uint64
	prevRx   uint64
	prevTx   uint64
	prevRd   uint64
	prevWr   uint64

	cpuPct         int
	rxKBps, txKBps int64
	rdKBps, wrKBps int64

	// lastFull 上一次完整采样的结果，供 SampleFast 复用慢变量（分区、温度、在线用户等）
	lastFull Metrics
}

// NewSampler 创建带差值缓存的采样器
func NewSampler() *Sampler {
	return &Sampler{cpuPct: -1}
}

// Sample 采集一次系统指标
func (s *Sampler) Sample() Metrics {
	m := Metrics{
		Source: "mock", CPUUsagePct: -1, LoadAvg1: -1,
		TempC: -1, DiskTempC: -1, UPSBattery: -1, CPUCores: runtime.NumCPU(),
	}
	m.BootTime, m.UptimeSecs = readBootTime()
	m.DiskVolumes = readDisks()
	for _, v := range m.DiskVolumes {
		m.DiskTotalGB += v.TotalGB
		m.DiskUsedGB += v.UsedGB
	}
	m.TotalMemMB, m.AvailableMB = readMem()
	realParts := 0
	if m.DiskTotalGB > 0 {
		realParts++
	}
	if m.TotalMemMB > 0 {
		realParts++
	}
	// 完全没有真实读数（非 Linux 或路径不可访问）时返回模拟值，供开发预览
	if realParts == 0 {
		return mockMetrics()
	}
	m.Source = "real"
	m.CPUPhysical = physicalCores(m.CPUCores)

	if v, ok := readLoadAvg(); ok {
		m.LoadAvg1 = v
	}
	m.EstablishedConns = readEstablished()
	m.TempC = readTemp()
	m.DiskTempC = readDiskTemp()
	m.LoggedInUsers = readWho()
	m.UPSBattery = readUPS()

	cpuTotal, cpuIdle, okCPU := readCPUTimes()
	rx, tx, okNet := readNetBytes()
	rd, wr, okIO := readDiskIO()

	now := time.Now()
	s.mu.Lock()
	elapsed := now.Sub(s.last)
	valid := !s.last.IsZero() && elapsed >= 500*time.Millisecond

	if okCPU && valid && cpuTotal > s.prevCPU {
		dt := float64(cpuTotal - s.prevCPU)
		di := float64(cpuIdle - s.prevIdle)
		v := int(math.Round((dt - di) / dt * 100))
		s.cpuPct = clampInt(v, 0, 100)
	}
	if okNet && valid && rx >= s.prevRx && tx >= s.prevTx {
		secs := elapsed.Seconds()
		s.rxKBps = int64(float64(rx-s.prevRx) / 1024 / secs)
		s.txKBps = int64(float64(tx-s.prevTx) / 1024 / secs)
	}
	if okIO && valid && rd >= s.prevRd && wr >= s.prevWr {
		secs := elapsed.Seconds()
		s.rdKBps = int64(float64(rd-s.prevRd) / 1024 / secs)
		s.wrKBps = int64(float64(wr-s.prevWr) / 1024 / secs)
	}
	if okCPU {
		s.prevCPU, s.prevIdle = cpuTotal, cpuIdle
	}
	if okNet {
		s.prevRx, s.prevTx = rx, tx
	}
	if okIO {
		s.prevRd, s.prevWr = rd, wr
	}
	s.last = now

	m.CPUUsagePct = s.cpuPct
	m.NetRxKBps, m.NetTxKBps = s.rxKBps, s.txKBps
	m.DiskReadKBps, m.DiskWriteKBps = s.rdKBps, s.wrKBps
	s.lastFull = m
	s.mu.Unlock()

	return m
}

// SampleFast 只刷新变化快的指标（CPU、网络、磁盘 IO、内存、负载），
// 分区用量、温度、在线用户、UPS 等沿用上次完整采样的结果。
// 供前端「实时网速」做 2 秒级刷新，不执行 df / who 等外部命令，开销很小。
func (s *Sampler) SampleFast() Metrics {
	s.mu.Lock()
	snap := s.lastFull
	s.mu.Unlock()
	if snap.Source != "real" {
		// 还没完整采样过（或非 Linux 模拟环境）时直接走完整采样
		return s.Sample()
	}
	if total, avail := readMem(); total > 0 {
		snap.TotalMemMB, snap.AvailableMB = total, avail
	}
	if v, ok := readLoadAvg(); ok {
		snap.LoadAvg1 = v
	}

	cpuTotal, cpuIdle, okCPU := readCPUTimes()
	rx, tx, okNet := readNetBytes()
	rd, wr, okIO := readDiskIO()

	now := time.Now()
	s.mu.Lock()
	elapsed := now.Sub(s.last)
	valid := !s.last.IsZero() && elapsed >= 500*time.Millisecond
	if okCPU && valid && cpuTotal > s.prevCPU {
		dt := float64(cpuTotal - s.prevCPU)
		di := float64(cpuIdle - s.prevIdle)
		s.cpuPct = clampInt(int(math.Round((dt-di)/dt*100)), 0, 100)
	}
	if okNet && valid && rx >= s.prevRx && tx >= s.prevTx {
		secs := elapsed.Seconds()
		s.rxKBps = int64(float64(rx-s.prevRx) / 1024 / secs)
		s.txKBps = int64(float64(tx-s.prevTx) / 1024 / secs)
	}
	if okIO && valid && rd >= s.prevRd && wr >= s.prevWr {
		secs := elapsed.Seconds()
		s.rdKBps = int64(float64(rd-s.prevRd) / 1024 / secs)
		s.wrKBps = int64(float64(wr-s.prevWr) / 1024 / secs)
	}
	if okCPU {
		s.prevCPU, s.prevIdle = cpuTotal, cpuIdle
	}
	if okNet {
		s.prevRx, s.prevTx = rx, tx
	}
	if okIO {
		s.prevRd, s.prevWr = rd, wr
	}
	s.last = now
	snap.CPUUsagePct = s.cpuPct
	snap.NetRxKBps, snap.NetTxKBps = s.rxKBps, s.txKBps
	snap.DiskReadKBps, snap.DiskWriteKBps = s.rdKBps, s.wrKBps
	s.mu.Unlock()

	return snap
}

// Sample 使用临时采样器采集一次指标（无历史差值时 CPU/吞吐为 0）
func Sample() Metrics {
	return NewSampler().Sample()
}

// readBootTime 读 /proc/stat 的 btime 与当前时间求差
func readBootTime() (int64, int64) {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "btime ") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			return 0, 0
		}
		boot, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return 0, 0
		}
		now := time.Now().Unix()
		return boot, now - boot
	}
	return 0, 0
}

// readCPUTimes 读 /proc/stat 首行，返回总时间片与空闲时间片（jiffies）
func readCPUTimes() (uint64, uint64, bool) {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 5 {
			return 0, 0, false
		}
		var total uint64
		for _, col := range f[1:] {
			if v, err := strconv.ParseUint(col, 10, 64); err == nil {
				total += v
			}
		}
		idle, _ := strconv.ParseUint(f[4], 10, 64)
		var iowait uint64
		if len(f) > 5 {
			iowait, _ = strconv.ParseUint(f[5], 10, 64)
		}
		return total, idle + iowait, true
	}
	return 0, 0, false
}

// physicalCores 识别物理核心数（区别于逻辑线程数）：
// 1) /proc/cpuinfo 的 (physical id, core id) 去重；2) 退化为 cpu cores × 插槽数；
// 3) ARM 等无 core id 的平台用 sysfs 的 thread_siblings_list 由线程数反推。
// 全部失败返回 0（未知），调用方不应把 0 当成“没有核心”。
func physicalCores(logical int) int {
	if raw, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		s := string(raw)
		if n := parsePhysicalCores(s); n > 0 {
			return n
		}
		if n := parseCoreCounts(s); n > 0 {
			return n
		}
	}
	if raw, err := os.ReadFile("/sys/devices/system/cpu/cpu0/topology/thread_siblings_list"); err == nil {
		if per := countCPUList(strings.TrimSpace(string(raw))); per > 1 && logical > 0 && logical%per == 0 {
			return logical / per
		}
	}
	return 0
}

// parsePhysicalCores 按 physical id + core id 去重统计物理核心数
func parsePhysicalCores(raw string) int {
	seen := map[string]struct{}{}
	pid, cid := "", ""
	flush := func() {
		if cid != "" {
			seen[pid+":"+cid] = struct{}{}
		}
		pid, cid = "", ""
	}
	for _, line := range strings.Split(raw, "\n") {
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "physical id":
			pid = strings.TrimSpace(v)
		case "core id":
			cid = strings.TrimSpace(v)
		}
	}
	flush()
	return len(seen)
}

// parseCoreCounts 在缺少 core id 时，用 “cpu cores” × 物理插槽数 估算
func parseCoreCounts(raw string) int {
	cores, packages := 0, map[string]struct{}{}
	for _, line := range strings.Split(raw, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "cpu cores":
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				cores = n
			}
		case "physical id":
			packages[v] = struct{}{}
		}
	}
	if cores == 0 {
		return 0
	}
	if len(packages) > 1 {
		return cores * len(packages)
	}
	return cores
}

// countCPUList 统计 “0-3,8” 形式的 CPU 列表个数
func countCPUList(s string) int {
	total := 0
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, "-") {
			total++
			continue
		}
		lo, hi, _ := strings.Cut(part, "-")
		a, err1 := strconv.Atoi(strings.TrimSpace(lo))
		b, err2 := strconv.Atoi(strings.TrimSpace(hi))
		if err1 != nil || err2 != nil || b < a {
			continue
		}
		total += b - a + 1
	}
	return total
}

// readLoadAvg 读 /proc/loadavg 的 1 分钟负载
func readLoadAvg() (float64, bool) {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, false
	}
	f := strings.Fields(string(raw))
	if len(f) < 1 {
		return 0, false
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// readNetBytes 汇总 /proc/net/dev 中各物理网卡的收发字节数
func readNetBytes() (uint64, uint64, bool) {
	raw, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0, false
	}
	var rx, tx uint64
	found := false
	for _, line := range strings.Split(string(raw), "\n") {
		i := strings.Index(line, ":")
		if i < 0 {
			continue
		}
		iface := strings.TrimSpace(line[:i])
		if iface == "" || iface == "lo" || strings.HasPrefix(iface, "Inter-") || strings.HasPrefix(iface, "face") {
			continue
		}
		f := strings.Fields(line[i+1:])
		if len(f) < 9 {
			continue
		}
		r, err1 := strconv.ParseUint(f[0], 10, 64)
		t, err2 := strconv.ParseUint(f[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		rx += r
		tx += t
		found = true
	}
	return rx, tx, found
}

// readDiskIO 汇总 /proc/diskstats 中物理磁盘的读写字节数
func readDiskIO() (uint64, uint64, bool) {
	raw, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		return 0, 0, false
	}
	var rd, wr uint64
	found := false
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 10 {
			continue
		}
		name := f[2]
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") ||
			strings.HasPrefix(name, "zram") || strings.HasPrefix(name, "dm-") {
			continue
		}
		sr, err1 := strconv.ParseUint(f[5], 10, 64)
		sw, err2 := strconv.ParseUint(f[9], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		rd += sr * 512
		wr += sw * 512
		found = true
	}
	return rd, wr, found
}

// readEstablished 统计 /proc/net/tcp{,6} 中状态为 ESTABLISHED(0x01) 的连接数
func readEstablished() int {
	n := 0
	for _, p := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if i == 0 {
				continue
			}
			f := strings.Fields(line)
			if len(f) < 4 {
				continue
			}
			if f[3] == "01" {
				n++
			}
		}
	}
	return n
}

// readTemp 读取 CPU 温度（摄氏度）：只认 CPU 相关的传感器，不取全系统最大值，
// 避免 GPU、主板、电池等其他 thermal zone 误触发 CPU 温度告警。
// 优先级：/sys/class/thermal 中的 CPU 类型 zone → hwmon 的 coretemp/k10temp → 无传感器返回 -1。
func readTemp() float64 {
	if v, ok := readCPUThermalZone(); ok {
		return v
	}
	return readCPUHwmon()
}

// readCPUThermalZone 遍历 /sys/class/thermal/thermal_zone*，只统计 CPU 类型传感器的最大读数；
// 若平台没有 CPU 类型 zone（老平台常见），退回唯一的 ACPI thermal zone（acpitz）。
func readCPUThermalZone() (float64, bool) {
	paths, _ := filepath.Glob("/sys/class/thermal/thermal_zone*/temp")
	cpuBest := -1.0
	acpiBest := -1.0
	for _, p := range paths {
		typRaw, err := os.ReadFile(filepath.Join(filepath.Dir(p), "type"))
		if err != nil {
			continue
		}
		c, ok := readMilliCelsius(p)
		if !ok {
			continue
		}
		typ := strings.ToLower(strings.TrimSpace(string(typRaw)))
		switch {
		case isCPUThermalType(typ):
			if c > cpuBest {
				cpuBest = c
			}
		case typ == "acpitz":
			if c > acpiBest {
				acpiBest = c
			}
		}
	}
	if cpuBest >= 0 {
		return cpuBest, true
	}
	if acpiBest >= 0 {
		return acpiBest, true
	}
	return -1, false
}

// isCPUThermalType 判断 thermal zone 的 type 是否属于 CPU 温度传感器。
// 覆盖 x86 的 x86_pkg_temp、ARM 的 cpu-thermal / soc-thermal 以及常见 hwmon 同名类型。
func isCPUThermalType(t string) bool {
	switch {
	case strings.Contains(t, "cpu"), strings.Contains(t, "pkg"),
		strings.Contains(t, "coretemp"), strings.Contains(t, "k10temp"),
		strings.Contains(t, "soc-thermal"), strings.Contains(t, "soc_thermal"):
		return true
	}
	return false
}

// readCPUHwmon 从 hwmon 读取 CPU 封装/核心温度（coretemp / k10temp / cpu_thermal / soc_thermal），
// 取各核心读数中的最大值；找不到 CPU 传感器时返回 -1。
func readCPUHwmon() float64 {
	names, _ := filepath.Glob("/sys/class/hwmon/hwmon*/name")
	best := -1.0
	for _, p := range names {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(string(raw))) {
		case "coretemp", "k10temp", "cpu_thermal", "soc_thermal":
		default:
			continue
		}
		inputs, _ := filepath.Glob(filepath.Join(filepath.Dir(p), "temp*_input"))
		for _, in := range inputs {
			if c, ok := readMilliCelsius(in); ok && c > best {
				best = c
			}
		}
	}
	return best
}

// readMilliCelsius 读取 sysfs 中的毫摄氏度文件并换算为摄氏度，过滤 0~150℃ 之外的无效值
func readMilliCelsius(path string) (float64, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
	if err != nil {
		return 0, false
	}
	c := v / 1000
	if c < 0 || c > 150 {
		return 0, false
	}
	return c, true
}

// readDisks 通过 `df -kP` 汇总所有真实块设备挂载点（数据盘/存储池）。
// 只保留 /dev/ 下的设备，自动跳过 boot/run 等系统分区并去重（btrfs 多挂载）。
func readDisks() []Volume {
	out, err := exec.Command("df", "-kP").Output()
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return nil
	}
	seen := map[string]bool{}
	vols := make([]Volume, 0, 4)
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		if len(f) < 6 {
			continue
		}
		dev, mount := f[0], f[5]
		if !strings.HasPrefix(dev, "/dev/") {
			continue
		}
		if strings.HasPrefix(mount, "/boot") || strings.HasPrefix(mount, "/run") || strings.HasPrefix(mount, "/var/lib") {
			continue
		}
		if seen[dev] {
			continue
		}
		totalKB, err1 := strconv.ParseInt(f[1], 10, 64)
		usedKB, err2 := strconv.ParseInt(f[2], 10, 64)
		if err1 != nil || err2 != nil || totalKB <= 0 {
			continue
		}
		seen[dev] = true
		vols = append(vols, Volume{
			Device:  dev,
			Mount:   mount,
			TotalGB: totalKB / 1024 / 1024,
			UsedGB:  usedKB / 1024 / 1024,
		})
	}
	// 容器/overlay 环境下可能拿不到 /dev/ 设备，退回根分区
	if len(vols) == 0 {
		if total, used := readDisk("/"); total > 0 {
			vols = append(vols, Volume{Device: "rootfs", Mount: "/", TotalGB: total, UsedGB: used})
		}
	}
	return vols
}

// readDisk 读取单个挂载点容量（KB -> GB），作为 readDisks 的兜底
func readDisk(root string) (int64, int64) {
	out, err := exec.Command("df", "-kP", root).Output()
	if err != nil {
		return 0, 0
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return 0, 0
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 6 {
		return 0, 0
	}
	totalKB, err1 := strconv.ParseInt(fields[1], 10, 64)
	usedKB, err2 := strconv.ParseInt(fields[2], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0
	}
	return totalKB / 1024 / 1024, usedKB / 1024 / 1024
}

// readDiskTemp 从 hwmon 读取硬盘/SSD 温度（drivetemp / nvme 传感器）
func readDiskTemp() float64 {
	names, _ := filepath.Glob("/sys/class/hwmon/hwmon*/name")
	best := -1.0
	for _, p := range names {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		name := strings.TrimSpace(string(raw))
		if name != "drivetemp" && name != "nvme" {
			continue
		}
		traw, err := os.ReadFile(filepath.Join(filepath.Dir(p), "temp1_input"))
		if err != nil {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(string(traw)), 64)
		if err != nil {
			continue
		}
		c := v / 1000
		if c <= 0 || c > 120 {
			continue
		}
		if c > best {
			best = c
		}
	}
	return best
}

// readWho 统计当前登录用户数（who 的行数）
func readWho() int {
	out, err := exec.Command("who").Output()
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "wtmp") {
			continue
		}
		n++
	}
	return n
}

// readUPS 读取 UPS 电量百分比（/sys/class/power_supply/*/capacity），无则返回 -1
func readUPS() int {
	paths, _ := filepath.Glob("/sys/class/power_supply/*/capacity")
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		v, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil || v < 0 || v > 100 {
			continue
		}
		return v
	}
	return -1
}

// readMem 读 /proc/meminfo 的 MemTotal 与 MemAvailable（单位 KB -> MB）
func readMem() (int64, int64) {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	var total, avail int64
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total = v
		case "MemAvailable:":
			avail = v
		}
	}
	if total == 0 {
		return 0, 0
	}
	return total / 1024, avail / 1024
}

// mockMetrics 在无法读取真实指标时（如 Windows 开发机）返回一组平稳的模拟值，
// 仅用于界面预览；Source 保持 "mock"，前端会在设置页标注“模拟指标”。
func mockMetrics() Metrics {
	now := time.Now()
	phase := float64(now.Unix()%3600) / 3600 * 2 * math.Pi
	r := rand.New(rand.NewSource(now.UnixNano()))

	totalMem := int64(16384)
	avail := int64(10800+int64(600*math.Sin(phase))) - int64(r.Intn(240))
	cpu := clampInt(int(math.Round(24+8*math.Sin(phase)+float64(r.Intn(7)-3))), 1, 99)
	uptime := int64(3*86400 + 7*3600 + 12*60 + now.Unix()%60)

	return Metrics{
		BootTime:         now.Unix() - uptime,
		UptimeSecs:       uptime,
		DiskTotalGB:      2048,
		DiskUsedGB:       1180 + int64(r.Intn(6)),
		TotalMemMB:       totalMem,
		AvailableMB:      avail,
		CPUUsagePct:      cpu,
		CPUCores:         runtime.NumCPU(),
		LoadAvg1:         math.Round((0.55+0.25*math.Sin(phase))*100) / 100,
		NetRxKBps:        int64(320 + r.Intn(900)),
		NetTxKBps:        int64(120 + r.Intn(380)),
		DiskReadKBps:     int64(40 + r.Intn(260)),
		DiskWriteKBps:    int64(30 + r.Intn(180)),
		EstablishedConns: 18 + r.Intn(24),
		TempC:            math.Round((44+3*math.Sin(phase))*10) / 10,
		DiskTempC:        math.Round((38+2*math.Sin(phase))*10) / 10,
		LoggedInUsers:    1 + r.Intn(2),
		UPSBattery:       100,
		DiskVolumes: []Volume{
			{Device: "/dev/sda1", Mount: "/vol1", TotalGB: 2048, UsedGB: 1180 + int64(r.Intn(6))},
		},
		Source: "mock",
	}
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

// FormatUptime 把秒数格式化为“X 天 Y 小时”
func FormatUptime(secs int64) string {
	if secs < 0 {
		secs = 0
	}
	d := secs / 86400
	h := (secs % 86400) / 3600
	m := (secs % 3600) / 60
	switch {
	case d > 0:
		return strconv.FormatInt(d, 10) + " 天 " + strconv.FormatInt(h, 10) + " 小时"
	case h > 0:
		return strconv.FormatInt(h, 10) + " 小时 " + strconv.FormatInt(m, 10) + " 分钟"
	default:
		return strconv.FormatInt(m, 10) + " 分钟"
	}
}

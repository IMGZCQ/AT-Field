// 本文件提供网络连接与服务状态的真实采集（仅 Linux 有效，其他平台返回空）。
package system

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ConnDetail 单个远端连接的明细（来自 ss -tanp）
type ConnDetail struct {
	Remote   string `json:"remote"`   // 远端 IP:端口
	Local    string `json:"local"`    // 本机地址:端口
	Protocol string `json:"protocol"` // tcp / tcp6
	State    string `json:"state"`    // ESTAB 等
	Process  string `json:"process"`  // 进程名
	PID      int    `json:"pid"`
	Count    int    `json:"count"`
}

// ListenPort 一个处于监听状态的端口
type ListenPort struct {
	Port    int    `json:"port"`
	Addr    string `json:"addr"` // 监听地址（0.0.0.0 / :: / 具体 IP）
	Proto   string `json:"proto"`
	Process string `json:"process"`
	PID     int    `json:"pid"`
	Public  bool   `json:"public"` // 是否对所有网卡开放（可能被公网访问）
}

// RunningService 运行中的 systemd 服务
type RunningService struct {
	Unit string `json:"unit"`
	Desc string `json:"desc"`
}

var reSSProc = regexp.MustCompile(`\("([^"]+)",pid=(\d+)`)

// ConnInfo 一个远端连接聚合条目
type ConnInfo struct {
	Remote string `json:"remote"` // IP:端口
	Count  int    `json:"count"`  // 已建立连接数
}

// ServiceStatus 系统服务运行状态
type ServiceStatus struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"` // systemd | docker | appcenter
	Active bool   `json:"active"`
	Known  bool   `json:"known"` // 是否成功检测到该服务
	Note   string `json:"note"`
}

// Connections 聚合 /proc/net/tcp{,6} 中 ESTABLISHED 的远端地址，返回前 top 条。
func Connections(top int) []ConnInfo {
	if top <= 0 {
		top = 8
	}
	counts := map[string]int{}
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 4 || fields[3] != "01" { // 01 = ESTABLISHED
				continue
			}
			if addr := decodeProcAddr(fields[2]); addr != "" {
				counts[addr]++
			}
		}
		_ = f.Close()
	}
	out := make([]ConnInfo, 0, len(counts))
	for k, v := range counts {
		out = append(out, ConnInfo{Remote: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Remote < out[j].Remote
	})
	if len(out) > top {
		out = out[:top]
	}
	return out
}

// decodeProcAddr 把 /proc/net/tcp 的 HEX 地址转换为 "IP:端口"（IPv4 为小端序）。
func decodeProcAddr(s string) string {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return ""
	}
	port, err := strconv.ParseInt(parts[1], 16, 32)
	if err != nil {
		return ""
	}
	hexIP := parts[0]
	var ip string
	if len(hexIP) == 8 {
		b := make([]byte, 4)
		for i := 0; i < 4; i++ {
			v, err := strconv.ParseUint(hexIP[i*2:i*2+2], 16, 8)
			if err != nil {
				return ""
			}
			b[3-i] = byte(v)
		}
		ip = strconv.Itoa(int(b[0])) + "." + strconv.Itoa(int(b[1])) + "." + strconv.Itoa(int(b[2])) + "." + strconv.Itoa(int(b[3]))
	} else if len(hexIP) == 32 {
		// IPv6：/proc/net/tcp6 中地址为 4 个 32 位字，每个字按小端序存放。
		// 先按字节还原，再在每个 4 字节字内做反转，得到标准 16 字节地址。
		raw := make([]byte, 16)
		for i := 0; i < 32; i += 2 {
			v, err := strconv.ParseUint(hexIP[i:i+2], 16, 8)
			if err != nil {
				return ""
			}
			raw[i/2] = byte(v)
		}
		for w := 0; w < 16; w += 4 {
			raw[w], raw[w+3] = raw[w+3], raw[w]
			raw[w+1], raw[w+2] = raw[w+2], raw[w+1]
		}
		v6 := net.IP(raw)
		if v6.IsUnspecified() {
			return ""
		}
		// IPv4 映射地址（::ffff:a.b.c.d）直接按 IPv4 呈现
		if v4 := v6.To4(); v4 != nil {
			return v4.String() + ":" + strconv.FormatInt(port, 10)
		}
		return "[" + v6.String() + "]:" + strconv.FormatInt(port, 10)
	} else {
		return ""
	}
	if ip == "0.0.0.0" || ip == "::" {
		return ""
	}
	return ip + ":" + strconv.FormatInt(port, 10)
}

// ConnectionDetails 用 ss -tanp 采集已建立连接的明细，按远端地址聚合后返回前 top 条。
// 采集不到 ss（非 Linux 或无权限）时回退到 /proc 版本，进程信息留空。
func ConnectionDetails(top int) []ConnDetail {
	if top <= 0 {
		top = 10
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := runCmd(ctx, "ss", "-tanp")
	if err != nil && strings.TrimSpace(out) == "" {
		return nil
	}
	type agg struct {
		d     ConnDetail
		count int
		local map[string]bool
	}
	byRemote := map[string]*agg{}
	order := make([]string, 0, 32)
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || f[0] != "ESTAB" {
			continue
		}
		local, peer := f[3], f[4]
		if peer == "*:*" || peer == "0.0.0.0:*" {
			continue
		}
		a, ok := byRemote[peer]
		if !ok {
			a = &agg{d: ConnDetail{Remote: peer, Local: local, State: "ESTAB", Protocol: protoOf(local)}, local: map[string]bool{}}
			a.d.Process, a.d.PID = parseSSProc(strings.Join(f[5:], " "))
			byRemote[peer] = a
			order = append(order, peer)
		}
		a.count++
		a.local[local] = true
	}
	res := make([]ConnDetail, 0, len(order))
	for _, k := range order {
		a := byRemote[k]
		a.d.Count = a.count
		if len(a.local) > 1 {
			ports := make([]string, 0, len(a.local))
			for p := range a.local {
				ports = append(ports, p)
			}
			sort.Strings(ports)
			a.d.Local = strings.Join(ports, " / ")
		}
		res = append(res, a.d)
	}
	sort.Slice(res, func(i, j int) bool {
		if res[i].Count != res[j].Count {
			return res[i].Count > res[j].Count
		}
		return res[i].Remote < res[j].Remote
	})
	if len(res) > top {
		res = res[:top]
	}
	return res
}

// ConnStream 一条已建立的 TCP 连接（ss -tanp 的一行，不做聚合）
type ConnStream struct {
	Protocol string `json:"protocol"` // tcp / tcp6
	Local    string `json:"local"`    // 本机地址:端口
	Remote   string `json:"remote"`   // 远端地址:端口
	Process  string `json:"process"`  // 进程名（需 root）
	PID      int    `json:"pid"`
	RecvQ    int64  `json:"recvQ"` // 接收队列积压（字节）
	SendQ    int64  `json:"sendQ"` // 发送队列积压（字节）
	// 以下三个字段由 API 层按策略填写，供前端标记「受保护 / 敏感」并禁用按钮
	Locked     bool   `json:"locked,omitempty"`     // 禁止断开（本应用自身端口）
	LockReason string `json:"lockReason,omitempty"` // 禁止断开的原因
	Sensitive  string `json:"sensitive,omitempty"`  // 敏感端口说明（SSH / SMB 等），断开前需额外提示
}

// ActiveConnections 逐条列出已建立（ESTAB）的 TCP 连接，最多 limit 条。
// 与 ConnectionDetails 的区别是不聚合：一行就是一条真实连接，可据此逐条断开。
func ActiveConnections(limit int) []ConnStream {
	if limit <= 0 {
		limit = 200
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := runCmd(ctx, "ss", "-tanp")
	if err != nil && strings.TrimSpace(out) == "" {
		return nil
	}
	res := make([]ConnStream, 0, 64)
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || f[0] != "ESTAB" {
			continue
		}
		local, peer := f[3], f[4]
		if peer == "*:*" || peer == "0.0.0.0:*" || peer == "[::]:*" {
			continue
		}
		c := ConnStream{Protocol: protoOf(local), Local: local, Remote: peer}
		c.RecvQ, _ = strconv.ParseInt(f[1], 10, 64)
		c.SendQ, _ = strconv.ParseInt(f[2], 10, 64)
		c.Process, c.PID = parseSSProc(strings.Join(f[5:], " "))
		res = append(res, c)
	}
	// 按本机端口升序，同一端口再按地址/远端排，读起来像在翻 ss 的输出
	sort.Slice(res, func(i, j int) bool {
		_, pi := splitHostPort(res[i].Local)
		_, pj := splitHostPort(res[j].Local)
		if pi != pj {
			return pi < pj
		}
		if res[i].Local != res[j].Local {
			return res[i].Local < res[j].Local
		}
		return res[i].Remote < res[j].Remote
	})
	if len(res) > limit {
		res = res[:limit]
	}
	return res
}

// KillConnection 用 ss -K（内核 SOCK_DESTROY）强制中断一条已建立的连接。
// 只断这一条 TCP 连接（对端立刻收到 RST），不封禁 IP、不结束进程。
// 失败原因（无权限 / 内核不支持）原样返回给调用方。
func KillConnection(local, remote string) (string, error) {
	lAddr, lPort := splitHostPort(local)
	rAddr, rPort := splitHostPort(remote)
	if lAddr == "" || rAddr == "" || lPort == 0 || rPort == 0 {
		return "", fmt.Errorf("连接地址不合法：%s → %s", local, remote)
	}
	args := []string{"-K", "-t"}
	if ip := net.ParseIP(rAddr); ip != nil && ip.To4() == nil {
		args = append(args, "-6")
	}
	// 同时锁定本机与远端两端，避免误伤到同一远端的其它连接
	args = append(args, "dst", rAddr, "dport", "=", strconv.Itoa(rPort), "src", lAddr, "sport", "=", strconv.Itoa(lPort))
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	out, err := runCmd(ctx, "ss", args...)
	msg := strings.TrimSpace(out)
	if err != nil {
		if msg == "" {
			msg = err.Error()
		}
		return msg, fmt.Errorf("%s", msg)
	}
	return msg, nil
}

// ListenPorts 用 ss -lntp 列出所有监听端口（按端口升序）
func ListenPorts() []ListenPort {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := runCmd(ctx, "ss", "-lntp")
	if err != nil && strings.TrimSpace(out) == "" {
		return nil
	}
	res := make([]ListenPort, 0, 24)
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[0] != "LISTEN" {
			continue
		}
		addr, port := splitHostPort(f[3])
		if port <= 0 {
			continue
		}
		p := ListenPort{Port: port, Addr: addr, Proto: protoOf(f[3]), Public: isPublicBind(addr)}
		p.Process, p.PID = parseSSProc(strings.Join(f[4:], " "))
		res = append(res, p)
	}
	sort.Slice(res, func(i, j int) bool {
		if res[i].Port != res[j].Port {
			return res[i].Port < res[j].Port
		}
		return res[i].Addr < res[j].Addr
	})
	if len(res) > 60 {
		res = res[:60]
	}
	return res
}

// RunningServices 列出运行中的 systemd 服务（最多 40 个）
func RunningServices() []RunningService {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	out, err := runCmd(ctx, "systemctl", "list-units", "--type=service", "--state=running", "--no-legend", "--no-pager", "--plain")
	if err != nil && strings.TrimSpace(out) == "" {
		return nil
	}
	res := make([]RunningService, 0, 24)
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || !strings.HasSuffix(f[0], ".service") {
			continue
		}
		desc := ""
		if len(f) > 4 {
			desc = strings.Join(f[4:], " ")
		}
		res = append(res, RunningService{Unit: strings.TrimSuffix(f[0], ".service"), Desc: desc})
		if len(res) >= 40 {
			break
		}
	}
	return res
}

// splitHostPort 解析 ss 的 "地址:端口"（兼容 [IPv6]:端口 与 *:端口）
func splitHostPort(s string) (string, int) {
	i := strings.LastIndex(s, ":")
	if i <= 0 {
		return s, 0
	}
	addr := strings.Trim(s[:i], "[]")
	port, err := strconv.Atoi(s[i+1:])
	if err != nil {
		return addr, 0
	}
	return addr, port
}

// isPublicBind 判断监听地址是否对所有网卡开放
func isPublicBind(addr string) bool {
	switch addr {
	case "", "*", "0.0.0.0", "::":
		return true
	}
	if ip := net.ParseIP(addr); ip != nil {
		return !ip.IsLoopback()
	}
	return false
}

// protoOf 根据地址判断协议族
func protoOf(addr string) string {
	if strings.HasPrefix(addr, "[") || strings.Count(addr, ":") > 1 {
		return "tcp6"
	}
	return "tcp"
}

// parseSSProc 从 ss 的 users:(("name",pid=1,fd=3)) 中提取进程名与 PID
func parseSSProc(s string) (string, int) {
	m := reSSProc.FindStringSubmatch(s)
	if m == nil {
		return "", 0
	}
	pid, _ := strconv.Atoi(m[2])
	return m[1], pid
}

// 需要监控的常用 NAS 服务
var monitorServices = []struct {
	Unit string
	Name string
}{
	{"smbd", "SMB 文件共享"},
	{"nfs-server", "NFS 服务"},
	{"docker", "Docker 容器"},
	{"sshd", "SSH 远程登录"},
	{"nginx", "Nginx 反向代理"},
	{"postgresql", "PostgreSQL"},
	{"mariadb", "MariaDB"},
	{"transmission", "Transmission"},
	{"qbittorrent", "qBittorrent"},
	{"jellyfin", "Jellyfin 媒体"},
	{"emby", "Emby 媒体"},
	{"plex", "Plex 媒体"},
	{"frpc", "frp 内网穿透"},
	{"tailscaled", "Tailscale"},
	{"zerotier-one", "ZeroTier"},
	{"rsyncd", "Rsync 同步"},
	{"vsftpd", "FTP 服务"},
	{"smartd", "硬盘监控"},
	{"chronyd", "时间同步"},
	{"avahi-daemon", "局域网发现"},
}

// Services 检测本机真实存在的服务状态，共三路来源：
// 1) 固定清单中确实安装了的 systemd unit（未安装的不再占位显示）；
// 2) Docker 容器（自部署的 Emby / Jellyfin 等大多在这里）；
// 3) 飞牛应用中心应用进程（/volN/@appcenter/<应用>/）。
func Services() []ServiceStatus {
	out := make([]ServiceStatus, 0, 24)
	found := make([]ServiceStatus, len(monitorServices))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, s := range monitorServices {
		wg.Add(1)
		go func(i int, unit, name string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			state, err := systemctlState(unit)
			if err != nil || state == "" {
				return // 未安装或无法检测：直接不显示
			}
			st := ServiceStatus{Name: name, Kind: "systemd", Known: true, Active: state == "active"}
			switch state {
			case "active":
				st.Note = "运行中"
			case "failed":
				st.Note = "运行异常"
			case "activating":
				st.Note = "启动中"
			default:
				st.Note = "已停止"
			}
			found[i] = st
		}(i, s.Unit, s.Name)
	}
	wg.Wait()
	for _, st := range found {
		if st.Known {
			out = append(out, st)
		}
	}
	out = append(out, dockerServices()...)
	out = append(out, appCenterServices()...)
	// 运行中的排在前面，其余保持原有顺序
	sort.SliceStable(out, func(i, j int) bool { return out[i].Active && !out[j].Active })
	return out
}

// dockerServices 列出 Docker 容器及状态；Docker 未安装或未运行时返回空
func dockerServices() []ServiceStatus {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := runCmd(ctx, "docker", "ps", "-a", "--format", "{{.Names}}|{{.Image}}|{{.State}}")
	if strings.TrimSpace(out) == "" {
		_ = err
		return nil
	}
	res := make([]ServiceStatus, 0, 12)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "|")
		if len(f) < 3 {
			continue
		}
		name := strings.TrimSpace(f[0])
		image := strings.TrimSpace(f[1])
		state := strings.ToLower(strings.TrimSpace(f[2]))
		if name == "" {
			continue
		}
		st := ServiceStatus{Name: name, Kind: "docker", Known: true, Active: state == "running"}
		switch state {
		case "running":
			st.Note = "容器运行中"
		case "paused":
			st.Note = "容器已暂停"
		case "restarting":
			st.Note = "容器重启中"
		case "exited", "dead", "removing":
			st.Note = "容器已停止"
		default:
			st.Note = "容器 " + state
		}
		if image != "" {
			st.Note += " · " + image
		}
		res = append(res, st)
		if len(res) >= 20 {
			break
		}
	}
	return res
}

var reAppCenter = regexp.MustCompile(`/vol\d+/@appcenter/([^/\s]+)/`)

// appCenterServices 从进程命令行识别飞牛应用中心安装的应用
func appCenterServices() []ServiceStatus {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	out, err := runCmd(ctx, "ps", "-eo", "args=")
	if err != nil && strings.TrimSpace(out) == "" {
		return nil
	}
	seen := make(map[string]bool, 8)
	res := make([]ServiceStatus, 0, 8)
	for _, line := range strings.Split(out, "\n") {
		m := reAppCenter.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := m[1]
		if name == "" || seen[name] || name == "AT-ForceField" {
			continue
		}
		seen[name] = true
		res = append(res, ServiceStatus{Name: name, Kind: "appcenter", Known: true, Active: true, Note: "飞牛应用运行中"})
		if len(res) >= 20 {
			break
		}
	}
	return res
}

// runCmd 执行外部命令并返回合并输出（退出码非 0 时同时返回错误）。
func runCmd(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// systemctlState 返回 unit 状态；unit 不存在时返回空字符串。
func systemctlState(unit string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := runCmd(ctx, "systemctl", "is-active", unit)
	if err != nil && strings.TrimSpace(out) == "" {
		return "", err
	}
	state := strings.TrimSpace(strings.Split(out, "\n")[0])
	switch state {
	case "active", "inactive", "failed", "activating", "deactivating", "unknown":
		if state == "unknown" {
			return "", nil
		}
		return state, nil
	default:
		return "", nil
	}
}

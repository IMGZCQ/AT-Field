// Package api 提供飞牛AT力场的 REST 接口与前端静态资源服务。
package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"atforcefield/internal/alert"
	"atforcefield/internal/audit"
	"atforcefield/internal/config"
	"atforcefield/internal/geo"
	"atforcefield/internal/metrics"
	"atforcefield/internal/secwatch"
	"atforcefield/internal/shield"
	"atforcefield/internal/store"
	"atforcefield/internal/system"
	"atforcefield/internal/weblog"
)

// Version 由 main 注入，用于“关于”页展示
var Version = "dev"

// Overview 总览页聚合数据
type Overview struct {
	Version string       `json:"version"`
	Active  bool         `json:"active"`
	Config  store.Config `json:"config"`

	DefensePower  int                   `json:"defensePower"` // 安全基线总分（0-100）
	BaselineItems []shield.BaselineItem `json:"baselineItems"`
	EnabledAt     int64                 `json:"enabledAt"`
	HealthIndex   int                   `json:"healthIndex"`
	HealthText    string                `json:"healthText"`
	HealthParts   []shield.HealthPart   `json:"healthParts"` // 健康指数逐项构成

	SyncRate int `json:"syncRate"` // 同步率（%）=（健康指数 + 防护能力）× 1.3，上限 260

	UptimeSecs int64  `json:"uptimeSecs"`
	UptimeText string `json:"uptimeText"`
	BootTime   int64  `json:"bootTime"`

	TotalMemMB  int64 `json:"totalMemMB"`
	AvailableMB int64 `json:"availableMB"`
	MemUsedPct  int   `json:"memUsedPct"`
	DiskTotalGB int64 `json:"diskTotalGB"`
	DiskUsedGB  int64 `json:"diskUsedGB"`
	DiskUsedPct int   `json:"diskUsedPct"`

	CPUUsagePct      int     `json:"cpuUsagePct"`
	CPUCores         int     `json:"cpuCores"`
	CPUPhysical      int     `json:"cpuPhysical"`
	LoadAvg1         float64 `json:"loadAvg1"`
	NetRxKBps        int64   `json:"netRxKBps"`
	NetTxKBps        int64   `json:"netTxKBps"`
	DiskReadKBps     int64   `json:"diskReadKBps"`
	DiskWriteKBps    int64   `json:"diskWriteKBps"`
	EstablishedConns int     `json:"establishedConns"`
	TempC            float64 `json:"tempC"`
	DiskTempC        float64 `json:"diskTempC"`
	LoggedInUsers    int     `json:"loggedInUsers"`
	UPSBattery       int     `json:"upsBattery"`

	DiskVolumes []system.Volume `json:"diskVolumes"`
	Sec         secwatch.State  `json:"sec"`

	Source      string `json:"source"`
	SampleCount int    `json:"sampleCount"`
	StatusText  string `json:"statusText"`
}

// Server 聚合 REST 接口与前端静态资源
type Server struct {
	store    *store.Store
	coll     *metrics.Collector
	proc     *metrics.ProcTraffic
	audit    *audit.Store
	sec      *secwatch.Watcher
	alert    *alert.Evaluator
	geoip    *geo.Resolver
	web      *weblog.Collector
	mux      *http.ServeMux
	basePath string
}

// New 创建 handler。dist 为前端构建产物（可为 nil，仅提供 API）。
func New(st *store.Store, coll *metrics.Collector, au *audit.Store, sec *secwatch.Watcher, ev *alert.Evaluator, web *weblog.Collector, dist fs.FS, basePath string) *Server {
	s := &Server{store: st, coll: coll, audit: au, sec: sec, alert: ev, geoip: geo.New(), web: web, mux: http.NewServeMux(), basePath: strings.TrimSuffix(basePath, "/")}
	s.routes()
	if dist != nil {
		s.mux.Handle("/", spaHandler(dist))
	}
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/overview", s.handleOverview)
	s.mux.HandleFunc("GET /api/stream", s.handleStream)
	s.mux.HandleFunc("GET /api/health/history", s.handleHistory)
	s.mux.HandleFunc("GET /api/net", s.handleNet)
	s.mux.HandleFunc("GET /api/net/proctraffic", s.handleProcTraffic)
	s.mux.HandleFunc("GET /api/ipinfo", s.handleIPInfo)
	s.mux.HandleFunc("POST /api/ip/ban", s.handleIPBan)
	s.mux.HandleFunc("POST /api/ip/unban", s.handleIPUnban)
	s.mux.HandleFunc("GET /api/web/summary", s.handleWebSummary)
	s.mux.HandleFunc("GET /api/web/pathsources", s.handleWebPathSources)
	s.mux.HandleFunc("GET /api/net/connections", s.handleConnList)
	s.mux.HandleFunc("POST /api/net/connections/kill", s.handleConnKill)
	s.mux.HandleFunc("GET /api/logs", s.handleLogs)
	s.mux.HandleFunc("GET /api/alerts", s.handleAlerts)
	s.mux.HandleFunc("POST /api/alert/test", s.handleAlertTest)
	s.mux.HandleFunc("GET /api/diagnostics", s.handleDiagnostics)
	s.mux.HandleFunc("GET /api/config", s.handleGetConfig)
	s.mux.HandleFunc("PUT /api/config", s.handlePutConfig)
	s.mux.HandleFunc("POST /api/config/reset", s.handleResetConfig)
	s.mux.HandleFunc("POST /api/shield/enable", s.handleEnable)
	s.mux.HandleFunc("POST /api/shield/disable", s.handleDisable)
}

// ServeHTTP 处理统一网关前缀后分发请求
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.basePath != "" && strings.HasPrefix(r.URL.Path, s.basePath) {
		if r.URL.Path == s.basePath {
			http.Redirect(w, r, s.basePath+"/", http.StatusMovedPermanently)
			return
		}
		trimmed := strings.TrimPrefix(r.URL.Path, s.basePath)
		if trimmed == "" {
			trimmed = "/"
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = trimmed
		r = r2
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.buildOverview())
}

// handleStream 使用 SSE 每 2 秒推送一次总览数据（与实时采样同频），前端断线可自动回退轮询。
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "当前连接不支持流式推送")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()

	send := func() bool {
		raw, err := json.Marshal(s.buildOverview())
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !send() {
		return
	}
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-t.C:
			if !send() {
				return
			}
		}
	}
}

// buildOverview 汇总当前配置、真实指标与安全基线
func (s *Server) buildOverview() Overview {
	cfg := s.store.Config()
	active := cfg.AtDefense || cfg.InvadeScan || cfg.DataSeal
	feats := shield.EnabledFeatures{AtDefense: cfg.AtDefense, InvadeScan: cfg.InvadeScan, DataSeal: cfg.DataSeal}
	m := s.coll.Current()
	health := shield.HealthIndex(m)

	sec := secwatch.State{}
	if s.sec != nil {
		sec = s.sec.State()
	}
	checks := shield.SecChecks{
		FirewallOn:      sec.FirewallOn,
		FirewallRules:   sec.FirewallRules,
		FirewallKnown:   sec.FirewallKnown,
		Fail2BanOn:      sec.Fail2BanOn,
		NftBanOn:        sec.NftBanOn,
		Banned:          sec.Banned,
		FailedLogins:    sec.FailedLogins,
		FailedIPs:       sec.FailedIPs,
		SmartChecked:    sec.SmartChecked,
		SmartFailed:     sec.SmartFailed,
		SSHPasswordAuth: sec.SSHPasswordAuth,
		SSHConfigKnown:  sec.SSHConfigKnown,
	}
	baseline, items := shield.BaselineScore(feats, checks)
	// 同步率：由健康指数与防护能力共同决定，(健康 + 防护) × 1.3，上限 260%。
	// 200% 为「绝对领域展开」门槛（前端在进度条上画刻度线），满分需两项均达 100。
	syncRate := (health + baseline) * 13 / 10
	if syncRate > 260 {
		syncRate = 260
	}
	if syncRate < 0 {
		syncRate = 0
	}

	return Overview{
		Version:          Version,
		Active:           active,
		Config:           cfg,
		DefensePower:     baseline,
		BaselineItems:    items,
		EnabledAt:        s.store.EnabledAt(),
		HealthIndex:      health,
		HealthText:       shield.HealthText(health),
		SyncRate:         syncRate,
		HealthParts:      shield.HealthBreakdown(m),
		UptimeSecs:       m.UptimeSecs,
		UptimeText:       system.FormatUptime(m.UptimeSecs),
		BootTime:         m.BootTime,
		TotalMemMB:       m.TotalMemMB,
		AvailableMB:      m.AvailableMB,
		MemUsedPct:       pct(m.TotalMemMB-m.AvailableMB, m.TotalMemMB),
		DiskTotalGB:      m.DiskTotalGB,
		DiskUsedGB:       m.DiskUsedGB,
		DiskUsedPct:      pct(m.DiskUsedGB, m.DiskTotalGB),
		CPUUsagePct:      m.CPUUsagePct,
		CPUCores:         m.CPUCores,
		CPUPhysical:      m.CPUPhysical,
		LoadAvg1:         m.LoadAvg1,
		NetRxKBps:        m.NetRxKBps,
		NetTxKBps:        m.NetTxKBps,
		DiskReadKBps:     m.DiskReadKBps,
		DiskWriteKBps:    m.DiskWriteKBps,
		EstablishedConns: m.EstablishedConns,
		TempC:            m.TempC,
		DiskTempC:        m.DiskTempC,
		LoggedInUsers:    m.LoggedInUsers,
		UPSBattery:       m.UPSBattery,
		DiskVolumes:      m.DiskVolumes,
		Sec:              sec,
		Source:           m.Source,
		SampleCount:      s.coll.Count(),
		StatusText:       shield.StatusText(active),
	}
}

// historyRange 根据 range 参数返回采样点数与间隔秒数。
// 统一 24 点：1h 每 2.5 分钟、1d 每小时、7d 每 7 小时一点。
func historyRange(r *http.Request) (n int, stepSecs int) {
	switch r.URL.Query().Get("range") {
	case "1h":
		return 24, 150
	case "1d":
		return 24, 3600
	default: // 7d
		return 24, 7 * 3600
	}
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	n, stepSecs := historyRange(r)
	if n <= 0 || n > 60 {
		n = 24
	}
	health := shield.HealthIndex(s.coll.Current())
	points := s.coll.History(n, stepSecs)
	if points == nil {
		points = []metrics.Point{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"target": health, "points": points})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	n, _ := strconv.Atoi(q.Get("n"))
	if n <= 0 || n > 200 {
		n = 50
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	logs, total := s.audit.Query(n, offset, strings.TrimSpace(q.Get("level")), q.Get("q"), q.Get("ip"))
	writeJSON(w, http.StatusOK, map[string]any{
		"logs":    logs,
		"total":   total,
		"offset":  offset,
		"hasMore": offset+len(logs) < total,
	})
}

// handleAlerts 返回告警类事件（warn 级别，时间倒序）
func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	if n <= 0 || n > 200 {
		n = 20
	}
	logs, total := s.audit.Query(n, 0, "warn", "", "")
	writeJSON(w, http.StatusOK, map[string]any{"logs": logs, "total": total})
}

// handleAlertTest 发送一条测试告警到已配置的飞书机器人
func (s *Server) handleAlertTest(w http.ResponseWriter, r *http.Request) {
	if s.alert == nil {
		writeError(w, http.StatusServiceUnavailable, "告警模块未启用")
		return
	}
	if err := s.alert.Test(); err != nil {
		s.audit.Add("warn", "告警设置", "测试推送失败："+err.Error())
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit.Add("info", "告警设置", "已发送测试告警，请检查飞书 / 企业微信 / 钉钉机器人是否收到消息")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleNet 返回网络吞吐趋势、连接 Top 与服务状态
func (s *Server) handleNet(w http.ResponseWriter, r *http.Request) {
	n, step := historyRange(r)
	if n <= 0 || n > 60 {
		n = 7
	}
	points := s.coll.NetHistory(n, step)
	if points == nil {
		points = []metrics.NetPoint{}
	}
	traffic := s.coll.NetTraffic(n, step)
	if traffic == nil {
		traffic = []metrics.TrafficPoint{}
	}
	details := system.ConnectionDetails(10)
	if details == nil {
		details = []system.ConnDetail{}
	}
	// 连接聚合优先由 ss 明细生成，保证与可展开明细一一对应；取不到时回退 /proc。
	conns := make([]system.ConnInfo, 0, 8)
	if len(details) == 0 {
		conns = system.Connections(8)
	} else {
		for _, d := range details {
			conns = append(conns, system.ConnInfo{Remote: d.Remote, Count: d.Count})
			if len(conns) >= 8 {
				break
			}
		}
	}
	if conns == nil {
		conns = []system.ConnInfo{}
	}
	svcs := system.Services()
	if svcs == nil {
		svcs = []system.ServiceStatus{}
	}
	ports := system.ListenPorts()
	if ports == nil {
		ports = []system.ListenPort{}
	}
	running := system.RunningServices()
	if running == nil {
		running = []system.RunningService{}
	}
	sec := s.sec.State()
	failed := sec.FailedList
	// 失败登录统计对齐前端所选的时限（1 小时 / 24 小时 / 7 天），按需重新采集。
	if _, list := s.sec.FailedIn(netWindow(r)); list != nil {
		failed = list
	}
	if failed == nil {
		failed = []secwatch.FailIP{}
	}
	banned := sec.BannedIPs
	if banned == nil {
		banned = []string{}
	}
	bans := sec.Bans
	if bans == nil {
		bans = []secwatch.BanInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"points":          points,
		"traffic":         traffic,
		"connections":     conns,
		"connDetail":      details,
		"services":        svcs,
		"listenPorts":     ports,
		"runningServices": running,
		"failedIPs":       failed,
		"bannedIPs":       banned,
		"bans":            bans,
		"banBackend":      banBackend(sec),
		"checkedAt":       sec.CheckedAt,
	})
}

// SetProcTraffic 注入进程级流量采样器（可为 nil，此时明细接口返回空列表）
func (s *Server) SetProcTraffic(p *metrics.ProcTraffic) { s.proc = p }

// handleProcTraffic 返回所选时间窗口内按进程聚合的 TCP 流量，并附带全机总量用于展示覆盖率。
func (s *Server) handleProcTraffic(w http.ResponseWriter, r *http.Request) {
	procs := []metrics.ProcTrafficPoint{}
	var hostRx, hostTx int64
	var updated int64
	if s.proc != nil {
		procs = s.proc.Top(netWindow(r), 30)
		updated = s.proc.Updated()
	}
	n, step := historyRange(r)
	for _, p := range s.coll.NetTraffic(n, step) {
		hostRx += p.Rx
		hostTx += p.Tx
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"procs":      procs,
		"hostRx":     hostRx,
		"hostTx":     hostTx,
		"updatedAt":  updated,
		"sampleSecs": 3,
	})
}

// webIP 访问来源 IP 及其归属地
type webIP struct {
	IP      string `json:"ip"`
	Count   int    `json:"count"`
	Bad     int    `json:"bad,omitempty"`  // 异常请求（4xx / 5xx）次数
	Scan    bool   `json:"scan,omitempty"` // 疑似扫描
	Country string `json:"country"`
	Code    string `json:"code"` // ISO 3166-1 alpha-2
	Region  string `json:"region"`
	City    string `json:"city"`
	ISP     string `json:"isp"`
	AS      string `json:"as,omitempty"`     // ASN 名称
	Approx  bool   `json:"approx,omitempty"` // 仅查询到注册机构级数据，地址可能不准
	// ProvinceHint 城市无法互相印证，但 zxinc 给出了中国 IPv6 的省级参考
	ProvinceHint bool `json:"provinceHint,omitempty"`
	Internal     bool `json:"internal"`
}

// webCountry 国家 / 地区访问占比
type webCountry struct {
	Name  string `json:"name"`
	Code  string `json:"code,omitempty"`
	Count int    `json:"count"`
}

// handleWebSummary 返回 Web 访问日志的聚合分析（来源 IP、路径、状态码、趋势）。
// nginx 的 access.log 可能是反代后的回环地址，此时由采集器尽量取 X-Forwarded-For。
func (s *Server) handleWebSummary(w http.ResponseWriter, r *http.Request) {
	if s.web == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "ips": []webIP{}, "points": []weblog.Point{}, "paths": []weblog.PathCount{}, "statuses": []weblog.StatusCount{}, "countries": []webCountry{}})
		return
	}
	q := s.web.Query(r.URL.Query().Get("range"))

	ips := make([]webIP, len(q.IPs))
	var wg sync.WaitGroup
	for i, it := range q.IPs {
		ips[i] = webIP{IP: it.IP, Count: it.Count, Bad: it.Bad, Scan: it.Scan}
		wg.Add(1)
		go func(i int, ip string) {
			defer wg.Done()
			info := s.geoip.Lookup(ip)
			ips[i].Internal = info.Private
			ips[i].Country = info.Country
			ips[i].Code = info.Code
			ips[i].Region = info.Region
			ips[i].City = info.City
			ips[i].ISP = info.ISP
			ips[i].AS = info.AS
			ips[i].Approx = info.Approx
			ips[i].ProvinceHint = info.ProvinceHint
		}(i, it.IP)
	}
	wg.Wait()

	// 地区占比按访问量最高的这批 IP 估算（全量查询会触发大量第三方请求）
	counts := map[string]int{}
	codes := map[string]string{}
	abroad, domestic := 0, 0
	for _, it := range ips {
		name := it.Country
		if it.Internal {
			// 内网 / 回环地址与境外判断无关，不计入比例
			name = "内网/保留地址"
		} else if name == "" {
			// 归属地查询失败：既不算境内也不算境外，避免把未知数据算成境外
			name = "未知"
		} else if name != "中国" {
			abroad += it.Count
		} else {
			domestic += it.Count
		}
		counts[name] += it.Count
		if it.Code != "" {
			codes[name] = it.Code
		}
	}
	countries := make([]webCountry, 0, len(counts))
	for k, v := range counts {
		countries = append(countries, webCountry{Name: k, Code: codes[k], Count: v})
	}
	for i := 1; i < len(countries); i++ {
		for j := i; j > 0 && countries[j].Count > countries[j-1].Count; j-- {
			countries[j], countries[j-1] = countries[j-1], countries[j]
		}
	}
	// 没有任何可判定的公网访问时返回 null，前端显示“—”，而不是误报 100% 境外
	var domesticPct any
	if domestic+abroad > 0 {
		domesticPct = domestic * 100 / (domestic + abroad)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"available":     q.Available,
		"logPath":       q.LogPath,
		"error":         q.Error,
		"from":          q.From,
		"to":            q.To,
		"total":         q.Total,
		"bad":           q.Bad,
		"prevTotal":     q.PrevTotal,
		"prevBad":       q.PrevBad,
		"loopback":      q.Loopback,
		"lastEvent":     q.LastEvent,
		"since":         q.Since,
		"retentionDays": weblog.RetentionDays,
		"points":        q.Points,
		"ips":           ips,
		"scans":         q.Scans,
		"paths":         q.Paths,
		"statuses":      q.Statuses,
		"countries":     countries,
		"domesticPct":   domesticPct,
	})
}

// handleWebPathSources 返回某条访问路径在所选时限内的来源 IP Top。
// 路径-IP 关联只保留最近 7 天，窗口更长时按 7 天统计并标记 partial。
func (s *Server) handleWebPathSources(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("path"))
	if name == "" {
		writeError(w, http.StatusBadRequest, "缺少 path 参数")
		return
	}
	if s.web == nil {
		writeJSON(w, http.StatusOK, map[string]any{"path": name, "ips": []webIP{}, "covered": 0})
		return
	}
	q := s.web.PathSources(name, r.URL.Query().Get("range"), 5)
	ips := make([]webIP, len(q.IPs))
	var wg sync.WaitGroup
	for i, it := range q.IPs {
		ips[i] = webIP{IP: it.IP, Count: it.Count}
		wg.Add(1)
		go func(i int, ip string) {
			defer wg.Done()
			info := s.geoip.Lookup(ip)
			ips[i].Internal = info.Private
			ips[i].Country = info.Country
			ips[i].Code = info.Code
			ips[i].Region = info.Region
			ips[i].City = info.City
			ips[i].ISP = info.ISP
			ips[i].AS = info.AS
			ips[i].Approx = info.Approx
			ips[i].ProvinceHint = info.ProvinceHint
		}(i, it.IP)
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]any{
		"path":          q.Path,
		"from":          q.From,
		"to":            q.To,
		"covered":       q.Covered,
		"partial":       q.Partial,
		"retentionDays": weblog.PathIPRetentionDays,
		"ips":           ips,
	})
}

// netWindow 网络页所选时限对应的统计窗口
func netWindow(r *http.Request) time.Duration {
	switch r.URL.Query().Get("range") {
	case "1h":
		return time.Hour
	case "7d":
		return 7 * 24 * time.Hour
	case "30d":
		return 30 * 24 * time.Hour
	default:
		return 24 * time.Hour
	}
}

// banBackend 当前实际生效的封禁后端
func banBackend(sec secwatch.State) string {
	if sec.Fail2BanOn {
		return "fail2ban"
	}
	if sec.NftBanOn {
		return "nft"
	}
	return ""
}

// handleIPBan 立即通过 fail2ban 封禁指定 IP
func (s *Server) handleIPBan(w http.ResponseWriter, r *http.Request) {
	s.ipAction(w, r, true)
}

// handleIPUnban 解除对指定 IP 的封禁
func (s *Server) handleIPUnban(w http.ResponseWriter, r *http.Request) {
	s.ipAction(w, r, false)
}

func (s *Server) ipAction(w http.ResponseWriter, r *http.Request, ban bool) {
	if s.sec == nil {
		writeError(w, http.StatusServiceUnavailable, "安全巡检模块未启用")
		return
	}
	var req struct {
		IP       string `json:"ip"`
		Duration string `json:"duration"` // 1h | 24h | 7d | permanent，缺省 1h
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}
	ip := strings.TrimSpace(req.IP)
	if ip == "" {
		writeError(w, http.StatusBadRequest, "缺少 ip 参数")
		return
	}
	dur, durText := banDuration(req.Duration)
	backend, err := s.sec.SetBanned(ip, ban, dur)
	if err != nil {
		s.audit.AddDetail(shield.Log{
			Level: "warn", Module: "IP 封禁", Msg: "操作失败：" + err.Error(),
			Object: ip, Source: "应用", Actor: "用户",
		})
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.sec.Refresh()
	if ban {
		s.audit.AddDetail(shield.Log{Level: "warn", Module: "IP 封禁", Msg: "已封禁 IP " + ip, Object: ip, Source: "应用", Actor: "用户", Detail: "方式=" + backend + "；时长=" + durText + "；手动封禁"})
	} else {
		s.audit.AddDetail(shield.Log{Level: "info", Module: "IP 封禁", Msg: "已解封 IP " + ip, Object: ip, Source: "应用", Actor: "用户", Detail: "方式=" + backend + "；手动解封"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ip": ip, "banned": ban, "backend": backend, "duration": durText})
}

// StartAutoBan 启动「扫描防护」后台巡检：每 10 秒读一次配置，开启时把近 1 分钟内
// 异常请求次数达到阈值的公网 IP 自动封禁。只处理公网单播地址，回环 / 内网 / 链路本地
// 一律跳过，避免把 NAS 自身或局域网设备挡在门外。进程退出时无需特别清理。
func (s *Server) StartAutoBan() {
	if s.web == nil || s.sec == nil {
		return
	}
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		cooldown := map[string]time.Time{}
		for now := range t.C {
			cfg := s.store.Config().ScanBan
			if !cfg.Enabled {
				continue
			}
			for ip, until := range cooldown {
				if !now.Before(until) {
					delete(cooldown, ip)
				}
			}
			dur, durText := banDuration(cfg.Duration)
			var banned []string
			failed := 0
			for i, ip := range s.web.RecentBadIPs(time.Minute, cfg.Threshold) {
				if i >= 10 {
					break
				}
				if _, seen := cooldown[ip]; seen || !publicAddress(ip) {
					continue
				}
				backend, err := s.sec.SetBanned(ip, true, dur)
				if err != nil {
					// 失败后 30 分钟内不重试，避免每 10 秒刷错误日志
					cooldown[ip] = now.Add(30 * time.Minute)
					failed++
					s.audit.AddDetail(shield.Log{
						Level: "warn", Module: "扫描防护", Msg: "自动封禁失败：" + err.Error(),
						Object: ip, Source: "应用", Actor: "系统",
					})
					continue
				}
				cooldown[ip] = now.Add(dur)
				banned = append(banned, ip)
				s.sec.Refresh()
				s.audit.AddDetail(shield.Log{
					Level: "warn", Module: "扫描防护", Msg: "已自动封禁疑似扫描 IP " + ip,
					Object: ip, Source: "应用", Actor: "系统",
					Detail: fmt.Sprintf("触发阈值=%d 次/分钟；方式=%s；时长=%s", cfg.Threshold, backend, durText),
				})
			}
			// 配置里的安全事件推送开关：默认关闭，开启后按轮汇总推送一条，避免逐个 IP 刷屏
			if s.alert != nil && s.store.Config().Alert.WatchAutoBan && (len(banned) > 0 || failed > 0) {
				s.alert.Notify("auto-ban", "", autoBanSummary(banned, failed, cfg.Threshold, durText))
			}
		}
	}()
}

// autoBanSummary 汇总一轮扫描防护的封禁结果用于推送；IP 过多时只列前 5 个。
func autoBanSummary(banned []string, failed, threshold int, durText string) string {
	var b strings.Builder
	if len(banned) > 0 {
		shown := banned
		if len(shown) > 5 {
			shown = shown[:5]
		}
		b.WriteString("扫描防护已自动封禁 " + strconv.Itoa(len(banned)) + " 个疑似扫描 IP：")
		b.WriteString(strings.Join(shown, "、"))
		if len(banned) > len(shown) {
			b.WriteString(" 等")
		}
		b.WriteString("（阈值 " + strconv.Itoa(threshold) + " 次/分钟；封禁时长 " + durText + "）")
	}
	if failed > 0 {
		if b.Len() > 0 {
			b.WriteString("；")
		}
		b.WriteString(strconv.Itoa(failed) + " 个 IP 封禁失败，详见防护日志")
	}
	return b.String()
}

// StartSecurityAlerts 启动安全事件推送巡检：每 30 秒统计最近 1 分钟的 SSH 失败登录
// 与异常访问（4xx/5xx），达到设置页阈值时推送 Webhook。同类事件 30 分钟内只推一次。
func (s *Server) StartSecurityAlerts() {
	if s.alert == nil {
		return
	}
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			s.checkSecurityAlerts()
		}
	}()
}

// checkSecurityAlerts 读取配置后分别检查 SSH 失败登录与异常访问，命中阈值即推送。
func (s *Server) checkSecurityAlerts() {
	a := s.store.Config().Alert
	if !a.Monitor || !a.Push {
		return
	}
	if a.WatchLoginFail && s.sec != nil {
		count, ips := s.sec.FailedIn(time.Minute)
		if count >= a.LoginFailCount {
			msg := "最近 1 分钟 SSH 失败登录 " + strconv.Itoa(count) + " 次，超过阈值 " + strconv.Itoa(a.LoginFailCount) + " 次"
			if len(ips) > 0 {
				n := len(ips)
				if n > 3 {
					n = 3
				}
				parts := make([]string, 0, n)
				for _, it := range ips[:n] {
					parts = append(parts, it.IP+"（"+strconv.Itoa(it.Count)+" 次）")
				}
				msg += "；主要来源：" + strings.Join(parts, "、")
			}
			s.alert.Notify("ssh-login-fail", "SSH 防护", msg)
		}
	}
	if a.WatchBadAccess && s.web != nil {
		ips := s.web.RecentBadIPs(time.Minute, a.BadAccessCount)
		if len(ips) > 0 {
			shown := ips
			if len(shown) > 5 {
				shown = shown[:5]
			}
			msg := "最近 1 分钟异常访问（4xx/5xx）达到阈值的 IP " + strconv.Itoa(len(ips)) + " 个：" + strings.Join(shown, "、")
			if len(ips) > len(shown) {
				msg += " 等"
			}
			msg += "（单个 IP ≥ " + strconv.Itoa(a.BadAccessCount) + " 次）"
			s.alert.Notify("bad-access", "访问异常", msg)
		}
	}
}

// publicAddress 判断是否为可自动封禁的公网单播地址
func publicAddress(s string) bool {
	ip := net.ParseIP(strings.TrimSpace(s))
	if ip == nil {
		return false
	}
	return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsUnspecified() &&
		!ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsMulticast()
}

// banDuration 把前端提交的时长标识映射为时长与展示文案。
func banDuration(v string) (time.Duration, string) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "24h", "1d":
		return 24 * time.Hour, "24 小时"
	case "7d", "1w":
		return 7 * 24 * time.Hour, "一周"
	case "365d", "1y":
		return 365 * 24 * time.Hour, "一年"
	default: // 1h 及缺省
		return time.Hour, "1 小时"
	}
}

// splitLocalPort 拆出 ss 地址里的端口（兼容 [IPv6]:port 与 *:port）
func splitLocalPort(s string) (string, int) {
	i := strings.LastIndex(s, ":")
	if i <= 0 {
		return strings.Trim(s, "[]"), 0
	}
	port, err := strconv.Atoi(s[i+1:])
	if err != nil {
		return strings.Trim(s[:i], "[]"), 0
	}
	return strings.Trim(s[:i], "[]"), port
}

// connSensitiveReason 判断本机端口是否属于「断了会打断远程操作」的敏感端口。
// 这类连接仍然允许断开（踢掉可疑 SSH 会话是正当需求），但前端会在确认框里额外警告。
func connSensitiveReason(local string) string {
	_, port := splitLocalPort(local)
	switch port {
	case 22:
		return "SSH 远程登录"
	case 445:
		return "SMB 文件共享"
	case 111, 2049:
		return "NFS / rpcbind 共享"
	case 5666:
		return "fnOS 系统服务"
	}
	return ""
}

// connLockedReason 判断该连接是否禁止断开。只拦本应用自身监听的端口：
// 断掉它界面会立刻失联，纯副作用、没有任何收益，所以在后端直接拦死（前端拦不住直接打 API 的情况）。
func connLockedReason(local string) string {
	_, appPort := splitLocalPort(config.Addr())
	if appPort <= 0 {
		return ""
	}
	if _, port := splitLocalPort(local); port == appPort {
		return fmt.Sprintf("本应用自身端口（%d），断开会让界面立刻失联", appPort)
	}
	return ""
}

// annotateConns 给每条连接补上「受保护 / 敏感」标记
func annotateConns(list []system.ConnStream) {
	for i := range list {
		if reason := connLockedReason(list[i].Local); reason != "" {
			list[i].Locked = true
			list[i].LockReason = reason
			continue
		}
		list[i].Sensitive = connSensitiveReason(list[i].Local)
	}
}

// handleConnList 逐条列出当前已建立的 TCP 连接（不聚合）
func (s *Server) handleConnList(w http.ResponseWriter, r *http.Request) {
	list := system.ActiveConnections(300)
	if list == nil {
		list = []system.ConnStream{}
	}
	annotateConns(list)
	writeJSON(w, http.StatusOK, map[string]any{"connections": list, "count": len(list)})
}

// handleConnKill 强制断开指定的一条已建立连接（ss -K / 内核 SOCK_DESTROY）
func (s *Server) handleConnKill(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Local  string `json:"local"`
		Remote string `json:"remote"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}
	local, remote := strings.TrimSpace(req.Local), strings.TrimSpace(req.Remote)
	if local == "" || remote == "" {
		writeError(w, http.StatusBadRequest, "缺少 local / remote 参数")
		return
	}
	// 本应用自身端口一律拒绝断开，并记一条日志说明被拦下了
	if reason := connLockedReason(local); reason != "" {
		s.audit.AddDetail(shield.Log{Level: "info", Module: "连接管理", Msg: "已阻止断开受保护的连接 " + local, Object: remote, Source: "应用", Actor: "用户", Detail: reason})
		writeError(w, http.StatusForbidden, "已阻止："+reason)
		return
	}
	if msg, err := system.KillConnection(local, remote); err != nil {
		writeError(w, http.StatusBadGateway, "断开失败："+msg)
		return
	}
	// ss -K 在没匹配到套接字时也会静默成功，这里复核一次是否真的消失
	for _, c := range system.ActiveConnections(300) {
		if c.Local == local && c.Remote == remote {
			writeError(w, http.StatusBadGateway, "内核未断开该连接（可能不支持 SOCK_DESTROY，或被权限拦住）")
			return
		}
	}
	s.audit.AddDetail(shield.Log{Level: "warn", Module: "连接管理", Msg: "已手动断开连接 " + local + " → " + remote, Object: remote, Source: "应用", Actor: "用户"})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "local": local, "remote": remote})
}

// handleIPInfo 查询单个 IP 的归属地（内网本地判定，公网转发 ip-api.com）
func (s *Server) handleIPInfo(w http.ResponseWriter, r *http.Request) {
	ip := strings.TrimSpace(r.URL.Query().Get("ip"))
	if ip == "" {
		writeError(w, http.StatusBadRequest, "缺少 ip 参数")
		return
	}
	writeJSON(w, http.StatusOK, s.geoip.Lookup(ip))
}

// handleDiagnostics 打包配置与日志，便于反馈问题时一键导出
func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)

	dir := config.DataDir()
	entries, err := os.ReadDir(dir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() || strings.HasSuffix(e.Name(), ".tmp") {
				continue
			}
			info, err := e.Info()
			if err != nil || info.Size() > 8*1024*1024 {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			fw, err := zw.Create(e.Name())
			if err != nil {
				continue
			}
			_, _ = fw.Write(raw)
		}
	}

	// 附上当前总览快照，便于定位
	if fw, err := zw.Create("overview.json"); err == nil {
		if raw, err := json.MarshalIndent(s.buildOverview(), "", "  "); err == nil {
			_, _ = fw.Write(raw)
		}
	}
	if err := zw.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	name := "atforcefield-diagnostics-" + time.Now().Format("20060102-150405") + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Config())
}

// handleResetConfig 恢复默认配置（防护三项重新开启、阈值回到默认、清空 Webhook）
func (s *Server) handleResetConfig(w http.ResponseWriter, r *http.Request) {
	cfg := store.Default()
	if err := s.store.Save(cfg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.store.SetEnabledAt(nowUnix())
	s.audit.Add("warn", "配置变更", "已恢复默认配置（含清空 Webhook 与签名密钥）")
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	// 以当前配置为基底再解码：请求里未出现的字段（如新增的单项监控开关）保留原值，
	// 避免旧版前端只提交部分字段时把开关意外清零
	cfg := s.store.Config()
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}
	// 先归一化（补默认端口 / 发件人等），再校验，避免用户只填了服务器时被默认值拒掉
	cfg.Normalize()
	// 保存前先校验 Webhook 地址与邮件设置，不合法直接拒绝，避免静默存进配置
	for _, h := range []struct{ name, val string }{
		{"飞书", cfg.Alert.Webhook},
		{"企业微信", cfg.Alert.WecomWebhook},
		{"钉钉", cfg.Alert.DingWebhook},
	} {
		if err := alert.ValidateHook(h.val); err != nil {
			writeError(w, http.StatusBadRequest, h.name+" Webhook 地址不合法："+err.Error())
			return
		}
	}
	if alert.MailReady(cfg.Alert.Mail) {
		if err := alert.ValidateMail(cfg.Alert.Mail); err != nil {
			writeError(w, http.StatusBadRequest, "邮件推送配置不合法："+err.Error())
			return
		}
	}
	old := s.store.Config()
	if err := s.store.Save(cfg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	active := cfg.AtDefense || cfg.InvadeScan || cfg.DataSeal
	if active {
		s.store.SetEnabledAt(nowUnix())
	}
	changed := describeChanges(old, cfg)
	if len(changed) > 0 {
		s.audit.Add("info", "配置变更", strings.Join(changed, "，"))
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) handleEnable(w http.ResponseWriter, r *http.Request) {
	cfg := s.store.Config()
	cfg.AtDefense = true
	cfg.InvadeScan = true
	cfg.DataSeal = true
	if err := s.store.Save(cfg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.store.SetEnabledAt(nowUnix())
	s.audit.Add("info", "AT 力场", "已一键启用全部防护功能")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDisable(w http.ResponseWriter, r *http.Request) {
	cfg := s.store.Config()
	cfg.AtDefense = false
	cfg.InvadeScan = false
	cfg.DataSeal = false
	if err := s.store.Save(cfg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit.Add("warn", "AT 力场", "已关闭全部防护功能，NAS 处于无防护状态")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

// spaHandler 提供前端静态资源，未命中路径回退到 index.html
func spaHandler(dist fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(dist, name); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}

func nowUnix() int64 {
	return time.Now().Unix()
}

func pct(used, total int64) int {
	if total <= 0 {
		return 0
	}
	v := int(used * 100 / total)
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// describeChanges 生成配置变更的可读描述
func describeChanges(old, next store.Config) []string {
	out := make([]string, 0, 5)
	if old.AtDefense != next.AtDefense {
		out = append(out, "AT 力场防护"+onOff(next.AtDefense))
	}
	if old.InvadeScan != next.InvadeScan {
		out = append(out, "实时入侵检测"+onOff(next.InvadeScan))
	}
	if old.DataSeal != next.DataSeal {
		out = append(out, "数据安全加固"+onOff(next.DataSeal))
	}
	if old.ScanBan.Enabled != next.ScanBan.Enabled {
		out = append(out, "扫描防护自动封禁"+onOff(next.ScanBan.Enabled))
	} else if next.ScanBan.Enabled && (old.ScanBan.Threshold != next.ScanBan.Threshold || old.ScanBan.Duration != next.ScanBan.Duration) {
		out = append(out, "扫描防护阈值/封禁时长已调整")
	}
	if old.Alert.Monitor != next.Alert.Monitor {
		out = append(out, "监控与告警"+onOff(next.Alert.Monitor))
	}
	if old.Alert.Push != next.Alert.Push {
		out = append(out, "告警消息推送"+onOff(next.Alert.Push))
	}
	if old.Alert.WatchAutoBan != next.Alert.WatchAutoBan {
		out = append(out, "自动封禁事件推送"+onOff(next.Alert.WatchAutoBan))
	}
	if old.Alert.WatchLoginFail != next.Alert.WatchLoginFail {
		out = append(out, "SSH 异常登录推送"+onOff(next.Alert.WatchLoginFail))
	}
	if old.Alert.WatchBadAccess != next.Alert.WatchBadAccess {
		out = append(out, "异常访问推送"+onOff(next.Alert.WatchBadAccess))
	}
	if old.Alert.Webhook != next.Alert.Webhook {
		if next.Alert.Webhook == "" {
			out = append(out, "已清除飞书 Webhook")
		} else {
			out = append(out, "已更新飞书 Webhook")
		}
	}
	if old.Alert.WecomWebhook != next.Alert.WecomWebhook {
		if next.Alert.WecomWebhook == "" {
			out = append(out, "已清除企业微信 Webhook")
		} else {
			out = append(out, "已更新企业微信 Webhook")
		}
	}
	if old.Alert.DingWebhook != next.Alert.DingWebhook {
		if next.Alert.DingWebhook == "" {
			out = append(out, "已清除钉钉 Webhook")
		} else {
			out = append(out, "已更新钉钉 Webhook")
		}
	}
	if old.Alert.DingSecret != next.Alert.DingSecret {
		out = append(out, "钉钉加签密钥已更新")
	}
	if old.Alert.Mail != next.Alert.Mail {
		// 与启用判定一致：SMTP 服务器与收件人都填了才算开启
		wasOn := strings.TrimSpace(old.Alert.Mail.Host) != "" && strings.TrimSpace(old.Alert.Mail.To) != ""
		isOn := strings.TrimSpace(next.Alert.Mail.Host) != "" && strings.TrimSpace(next.Alert.Mail.To) != ""
		switch {
		case wasOn && !isOn:
			out = append(out, "已关闭邮件推送")
		case !wasOn && isOn:
			out = append(out, "已开启邮件推送")
		default:
			out = append(out, "已更新邮件推送设置")
		}
	}
	if old.Alert.HealthMin != next.Alert.HealthMin ||
		old.Alert.DiskPct != next.Alert.DiskPct ||
		old.Alert.MemPct != next.Alert.MemPct ||
		old.Alert.TempC != next.Alert.TempC ||
		old.Alert.DiskTempC != next.Alert.DiskTempC ||
		old.Alert.VolumePct != next.Alert.VolumePct ||
		old.Alert.LoginFailCount != next.Alert.LoginFailCount ||
		old.Alert.BadAccessCount != next.Alert.BadAccessCount {
		out = append(out, "告警阈值已调整")
	}
	return out
}

func onOff(v bool) string {
	if v {
		return "已开启"
	}
	return "已关闭"
}

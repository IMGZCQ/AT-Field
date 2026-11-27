// Package geo 提供 IP 归属地查询：内网/保留地址本地判定，公网地址按需向
// ip-api.com 发起查询，结果缓存 24 小时（最多 512 条），避免频繁请求第三方。
package geo

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// endpoint ip-api.com 免费接口（仅 HTTP）
	endpoint = "http://ip-api.com/json/%s?lang=zh-CN&fields=status,message,country,countryCode,regionName,city,isp,org"
	// cacheTTL 查询结果缓存时长
	cacheTTL = 24 * time.Hour
	// cacheMax 缓存条目上限
	cacheMax = 512
)

// Info 一个 IP 的归属地信息
type Info struct {
	IP      string `json:"ip"`
	Private bool   `json:"private"` // 内网/保留地址（未发出网络请求）
	Country string `json:"country"`
	Code    string `json:"code"` // ISO 3166-1 alpha-2，用于地图填色
	Region  string `json:"region"`
	City    string `json:"city"`
	ISP     string `json:"isp"`
	Org     string `json:"org"`
	Source  string `json:"source"` // local / ip-api / cache
	Error   string `json:"error,omitempty"`
}

type entry struct {
	info Info
	at   time.Time
}

// Resolver IP 归属地解析器（带缓存）
type Resolver struct {
	client *http.Client

	mu    sync.Mutex
	cache map[string]entry
}

// New 创建解析器
func New() *Resolver {
	return &Resolver{
		client: &http.Client{Timeout: 5 * time.Second},
		cache:  make(map[string]entry),
	}
}

// Lookup 查询 IP 归属地。内网地址直接本地判定，公网地址在线查询（结果缓存）。
func (r *Resolver) Lookup(raw string) Info {
	ip := strings.TrimSpace(raw)
	if ip == "" {
		return Info{Error: "缺少 IP 参数"}
	}
	// 去掉 IPv6 zone 标识，如 fe80::1%eth0
	if i := strings.Index(ip, "%"); i > 0 {
		ip = ip[:i]
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return Info{IP: raw, Error: "IP 格式不正确"}
	}
	hint := classify(parsed)
	if hint != "" {
		return Info{IP: ip, Private: true, Country: "内网/保留地址", ISP: hint, Source: "local"}
	}

	r.mu.Lock()
	if e, ok := r.cache[ip]; ok && time.Since(e.at) < cacheTTL {
		r.mu.Unlock()
		info := e.info
		info.Source = "cache"
		return info
	}
	r.mu.Unlock()

	info := r.query(ip)
	if info.Error != "" {
		return info
	}

	r.mu.Lock()
	if len(r.cache) >= cacheMax {
		r.evictLocked()
	}
	r.cache[ip] = entry{info: info, at: time.Now()}
	r.mu.Unlock()
	return info
}

// query 向 ip-api.com 发起查询
func (r *Resolver) query(ip string) Info {
	resp, err := r.client.Get(fmt.Sprintf(endpoint, ip))
	if err != nil {
		return Info{IP: ip, Error: "查询失败：" + err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Info{IP: ip, Error: fmt.Sprintf("查询失败：HTTP %d", resp.StatusCode)}
	}
	var body struct {
		Status      string `json:"status"`
		Message     string `json:"message"`
		Country     string `json:"country"`
		CountryCode string `json:"countryCode"`
		RegionName  string `json:"regionName"`
		City        string `json:"city"`
		ISP         string `json:"isp"`
		Org         string `json:"org"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Info{IP: ip, Error: "解析响应失败"}
	}
	if body.Status != "success" {
		msg := body.Message
		if msg == "" {
			msg = "未查询到归属地"
		}
		return Info{IP: ip, Error: msg}
	}
	return Info{
		IP:      ip,
		Country: body.Country,
		Code:    body.CountryCode,
		Region:  body.RegionName,
		City:    body.City,
		ISP:     body.ISP,
		Org:     body.Org,
		Source:  "ip-api",
	}
}

// evictLocked 清理过期条目；若仍超限则清空缓存（简单可靠的兜底策略）
func (r *Resolver) evictLocked() {
	for k, v := range r.cache {
		if time.Since(v.at) >= cacheTTL {
			delete(r.cache, k)
		}
	}
	if len(r.cache) >= cacheMax {
		r.cache = make(map[string]entry)
	}
}

// classify 判断是否为内网/保留地址，返回说明文本（非内网返回空串）
func classify(ip net.IP) string {
	switch {
	case ip.IsLoopback():
		return "本机环回地址"
	case ip.IsUnspecified():
		return "未指定地址"
	case ip.IsLinkLocalUnicast():
		return "链路本地地址"
	case ip.IsLinkLocalMulticast(), ip.IsMulticast():
		return "组播地址"
	case ip.IsPrivate():
		return "私有网段"
	}
	return ""
}

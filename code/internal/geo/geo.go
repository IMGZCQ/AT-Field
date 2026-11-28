// Package geo 提供 IP 归属地查询：内网/保留地址本地判定；公网地址并发查询
// ip-api、ipwho.is、ip2location、iplocation 多个数据源，只有至少两个数据源的
// 城市坐标互相接近时才采信省市，否则只到国家/运营商并标记 Approx（地址可能不准）。
// 结果缓存 24 小时（最多 512 条），避免频繁请求第三方。
package geo

import (
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// ipAPIEndpoint ip-api.com 免费接口（仅 HTTP，返回中文地名）
	ipAPIEndpoint = "http://ip-api.com/json/%s?lang=zh-CN&fields=status,message,country,countryCode,regionName,city,isp,org,as,lat,lon"
	// ipWhoEndpoint ipwho.is 免费接口（支持中文地名）
	ipWhoEndpoint = "https://ipwho.is/%s?lang=zh-CN"
	// ip2LocEndpoint ip2location.io 免费接口（1000 次/天）
	ip2LocEndpoint = "https://api.ip2location.io/?ip=%s"
	// iplocEndpoint iplocation.net 免费接口（主要用来确认国家与运营商）
	iplocEndpoint = "https://api.iplocation.net/?ip=%s"
	// zxincEndpoint zxinc 社区 IPv6 库（只在中国 IPv6 上提供省级参考，无经纬度）
	zxincEndpoint = "https://ip.zxinc.org/api.php?type=json&ip=%s"
	// cacheTTL 查询结果缓存时长
	cacheTTL = 24 * time.Hour
	// cacheMax 缓存条目上限
	cacheMax = 512
	// agreeKm 两个数据源给出的城市坐标相距小于该值视为一致
	agreeKm = 200
	// userAgent 第三方接口要求的常规 UA
	userAgent = "AT-ForceField/1.0"
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
	AS      string `json:"as,omitempty"` // ASN，如 "AS56047 China Mobile communications corporation"
	// Approx 多个数据源未能互相印证城市（常见于中国 IPv6 / 注册机构级数据），省市不可信
	Approx bool `json:"approx,omitempty"`
	// ProvinceHint 城市无法印证，但 zxinc 给出了中国 IPv6 的省级数据，Region 仅供参考
	ProvinceHint bool   `json:"provinceHint,omitempty"`
	Source       string `json:"source"` // local / multi / cache
	Error        string `json:"error,omitempty"`
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

// Lookup 查询 IP 归属地。内网地址直接本地判定，公网地址多源在线查询（结果缓存）。
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

// sourceData 单个数据源的查询结果
type sourceData struct {
	name     string
	prio     int
	info     Info
	registry bool // 运营商/机构只是注册机构，城市不可信
	lat, lon float64
	hasGeo   bool
}

// querySource 一个待查询的数据源
type querySource struct {
	name string
	prio int
	fn   func(string) (sourceData, bool)
}

// query 并发查询多个数据源并合并
func (r *Resolver) query(ip string) Info {
	srcs := []querySource{
		{"ip-api", 0, r.fetchIPAPI},
		{"iplocation.net", 1, r.fetchIPLocation},
		{"ipwho.is", 2, r.fetchIPWho},
		{"ip2location.io", 3, r.fetchIP2Location},
	}
	// IPv6 额外查 zxinc：它的国内 IPv6 省级数据比其他源细，但没有经纬度，
	// 所以只作省份参考（ProvinceHint），不参与城市印证；境外 v6 结果直接丢弃。
	if p := net.ParseIP(ip); p != nil && p.To4() == nil {
		srcs = append([]querySource{{"zxinc", 0, r.fetchZXINC}}, srcs...)
		for i := 1; i < len(srcs); i++ {
			srcs[i].prio++
		}
	}

	out := make(chan sourceData, len(srcs))
	var wg sync.WaitGroup
	for _, s := range srcs {
		wg.Add(1)
		go func(name string, prio int, fn func(string) (sourceData, bool)) {
			defer wg.Done()
			d, ok := fn(ip)
			if !ok {
				return
			}
			d.name, d.prio = name, prio
			out <- d
		}(s.name, s.prio, s.fn)
	}
	wg.Wait()
	close(out)

	var results []sourceData
	for d := range out {
		results = append(results, d)
	}
	if len(results) == 0 {
		return Info{IP: ip, Error: "查询失败：所有数据源均不可用"}
	}
	return merge(ip, results)
}

// merge 合并多源结果：地名以 ip-api（中文）优先，城市需至少两个源坐标一致才采信
func merge(ip string, ds []sourceData) Info {
	sortByPrio(ds)

	info := Info{IP: ip, Source: "multi"}
	// 国家 / 国家代码：优先 ip-api，其次第一个有值的源
	for _, d := range ds {
		if info.Country == "" && d.info.Country != "" {
			info.Country = d.info.Country
		}
		if info.Code == "" && d.info.Code != "" {
			info.Code = d.info.Code
		}
	}

	if cityDS, ok := agreeingCity(ds); ok {
		for _, d := range cityDS {
			if info.Region == "" && d.info.Region != "" {
				info.Region = d.info.Region
			}
			if info.City == "" && d.info.City != "" {
				info.City = d.info.City
			}
		}
	} else {
		info.Approx = true
		info.Region, info.ProvinceHint = provinceHint(ds)
	}

	// 运营商：优先非注册机构的 ISP，其次用 ASN 名称识别
	bestPrio := int(^uint(0) >> 1)
	for _, d := range ds {
		name := operatorName(d.info.ISP)
		if name == "" {
			continue
		}
		prio := d.prio
		if d.registry {
			// 注册机构名不能当运营商，改用其 ASN 识别结果
			name = operatorName(d.info.AS)
			prio = len(ds) + 1
		}
		if name != "" && prio < bestPrio {
			bestPrio = prio
			info.ISP = name
		}
	}
	for _, d := range ds {
		if info.AS == "" && d.info.AS != "" {
			info.AS = d.info.AS
		}
		if info.Org == "" && d.info.Org != "" && !isRegistry(d.info.Org, "") {
			info.Org = d.info.Org
		}
	}
	return info
}

// provinceHint 取 zxinc 给出的省级结果。zxinc 没有经纬度、只有单源，
// 因此只在城市无法印证时作为参考省份，不参与 agreeingCity 的坐标印证。
func provinceHint(ds []sourceData) (string, bool) {
	for _, d := range ds {
		if d.name == "zxinc" && d.info.Region != "" {
			return d.info.Region, true
		}
	}
	return "", false
}

// agreeingCity 找出至少两个坐标互相接近的非注册机构来源，城市可信时返回该聚类
func agreeingCity(ds []sourceData) ([]sourceData, bool) {
	var cands []sourceData
	for _, d := range ds {
		if d.registry || !d.hasGeo || d.info.City == "" {
			continue
		}
		cands = append(cands, d)
	}
	bestIdx, bestN := -1, 0
	for i := range cands {
		n := 1
		for j := range cands {
			if i != j && distanceKm(cands[i].lat, cands[i].lon, cands[j].lat, cands[j].lon) <= agreeKm {
				n++
			}
		}
		if n > bestN {
			bestN, bestIdx = n, i
		}
	}
	if bestN < 2 {
		return nil, false
	}
	// 只返回互相印证的来源，避免用离群源的地名；按优先级排序，让 ip-api 的中文地名优先
	cluster := []sourceData{cands[bestIdx]}
	for j := range cands {
		if j != bestIdx && distanceKm(cands[bestIdx].lat, cands[bestIdx].lon, cands[j].lat, cands[j].lon) <= agreeKm {
			cluster = append(cluster, cands[j])
		}
	}
	sortByPrio(cluster)
	return cluster, true
}

func distanceKm(lat1, lon1, lat2, lon2 float64) float64 {
	const rad = math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLon := (lon2 - lon1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * 6371 * math.Asin(math.Sqrt(a))
}

func sortByPrio(ds []sourceData) {
	for i := 1; i < len(ds); i++ {
		for j := i; j > 0 && ds[j].prio < ds[j-1].prio; j-- {
			ds[j], ds[j-1] = ds[j-1], ds[j]
		}
	}
}

// fetchJSON 请求并解析 JSON，失败返回 false
func (r *Resolver) fetchJSON(rawURL string, dst any) bool {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := r.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	return json.NewDecoder(resp.Body).Decode(dst) == nil
}

// fetchIPAPI ip-api.com（中文地名，主数据源）
func (r *Resolver) fetchIPAPI(ip string) (sourceData, bool) {
	var body struct {
		Status      string  `json:"status"`
		Country     string  `json:"country"`
		CountryCode string  `json:"countryCode"`
		RegionName  string  `json:"regionName"`
		City        string  `json:"city"`
		ISP         string  `json:"isp"`
		Org         string  `json:"org"`
		AS          string  `json:"as"`
		Lat         float64 `json:"lat"`
		Lon         float64 `json:"lon"`
	}
	if !r.fetchJSON(fmt.Sprintf(ipAPIEndpoint, ip), &body) || body.Status != "success" {
		return sourceData{}, false
	}
	d := sourceData{registry: isRegistry(body.ISP, body.Org), lat: body.Lat, lon: body.Lon, hasGeo: body.Lat != 0 || body.Lon != 0}
	d.info = Info{
		IP: ip, Country: body.Country, Code: body.CountryCode,
		Region: body.RegionName, City: body.City,
		ISP: body.ISP, Org: body.Org, AS: body.AS,
	}
	return d, true
}

// fetchIPWho ipwho.is
func (r *Resolver) fetchIPWho(ip string) (sourceData, bool) {
	var body struct {
		Success     bool    `json:"success"`
		Country     string  `json:"country"`
		CountryCode string  `json:"country_code"`
		Region      string  `json:"region"`
		City        string  `json:"city"`
		Lat         float64 `json:"latitude"`
		Lon         float64 `json:"longitude"`
		Connection  struct {
			ASN int    `json:"asn"`
			Org string `json:"org"`
			ISP string `json:"isp"`
		} `json:"connection"`
	}
	if !r.fetchJSON(fmt.Sprintf(ipWhoEndpoint, ip), &body) || !body.Success {
		return sourceData{}, false
	}
	as := ""
	if body.Connection.ASN > 0 {
		as = fmt.Sprintf("AS%d %s", body.Connection.ASN, body.Connection.Org)
	}
	d := sourceData{registry: isRegistry(body.Connection.ISP, body.Connection.Org), lat: body.Lat, lon: body.Lon, hasGeo: body.Lat != 0 || body.Lon != 0}
	d.info = Info{
		IP: ip, Country: body.Country, Code: body.CountryCode,
		Region: body.Region, City: body.City,
		ISP: body.Connection.ISP, Org: body.Connection.Org, AS: strings.TrimSpace(as),
	}
	return d, true
}

// fetchIP2Location ip2location.io（免费额度 1000 次/天）
func (r *Resolver) fetchIP2Location(ip string) (sourceData, bool) {
	var body struct {
		CountryCode string  `json:"country_code"`
		CountryName string  `json:"country_name"`
		RegionName  string  `json:"region_name"`
		CityName    string  `json:"city_name"`
		Lat         float64 `json:"latitude"`
		Lon         float64 `json:"longitude"`
		ASN         string  `json:"asn"`
		AS          string  `json:"as"`
	}
	if !r.fetchJSON(fmt.Sprintf(ip2LocEndpoint, ip), &body) || body.CountryCode == "" {
		return sourceData{}, false
	}
	as := body.AS
	if body.ASN != "" && body.AS != "" {
		as = "AS" + body.ASN + " " + body.AS
	}
	d := sourceData{registry: isRegistry(body.AS, ""), lat: body.Lat, lon: body.Lon, hasGeo: body.Lat != 0 || body.Lon != 0}
	d.info = Info{
		IP: ip, Country: body.CountryName, Code: body.CountryCode,
		Region: body.RegionName, City: body.CityName,
		ISP: body.AS, AS: as,
	}
	return d, true
}

// fetchIPLocation iplocation.net（只提供国家与运营商，用于确认）
func (r *Resolver) fetchIPLocation(ip string) (sourceData, bool) {
	var body struct {
		CountryName string `json:"country_name"`
		CountryCode string `json:"country_code2"`
		ISP         string `json:"isp"`
		Code        string `json:"response_code"`
	}
	if !r.fetchJSON(fmt.Sprintf(iplocEndpoint, url.QueryEscape(ip)), &body) || body.CountryCode == "" || body.Code != "200" {
		return sourceData{}, false
	}
	d := sourceData{registry: isRegistry(body.ISP, "")}
	d.info = Info{IP: ip, Country: body.CountryName, Code: body.CountryCode, ISP: body.ISP}
	return d, true
}

// fetchZXINC zxinc 社区库（IPv6 专用）。返回的是分隔文本、没有经纬度，
// 因此只取国家 / 省 / 运营商；境外结果不稳定，直接丢弃。
func (r *Resolver) fetchZXINC(ip string) (sourceData, bool) {
	var body struct {
		Code int `json:"code"`
		Data struct {
			Country string `json:"country"`
			Local   string `json:"local"`
		} `json:"data"`
	}
	if !r.fetchJSON(fmt.Sprintf(zxincEndpoint, url.QueryEscape(ip)), &body) || body.Code != 0 {
		return sourceData{}, false
	}
	area := splitZXinc(body.Data.Country) // 国家 / 省 /（市）
	if len(area) < 2 || !strings.Contains(area[0], "中国") {
		return sourceData{}, false
	}
	d := sourceData{}
	d.info = Info{IP: ip, Country: "中国", Region: area[1]}
	if local := splitZXinc(body.Data.Local); len(local) > 0 {
		d.info.ISP = local[0]
		if len(local) > 1 {
			d.info.Org = local[1]
		}
	}
	return d, true
}

// splitZXinc 拆分 zxinc 的字段：IPv6 用制表符，IPv4 / 部分记录用短横线
func splitZXinc(s string) []string {
	fields := strings.FieldsFunc(s, func(c rune) bool {
		return c == '\t' || c == '–' || c == '\u3000'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// isRegistry 判断 ISP / 机构名是否只是 IP 注册机构（此时地址不可信）
func isRegistry(isp, org string) bool {
	s := strings.ToLower(isp + " " + org)
	for _, k := range []string{
		"internet network information center", "cnnic",
		"apnic", "ripe ncc", "american registry for internet numbers", "lacnic", "afrinic",
	} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// operatorName 从 ISP / ASN 名称里识别运营商，识别不出时返回清洗后的名称
func operatorName(name string) string {
	s := strings.TrimSpace(name)
	if s == "" {
		return ""
	}
	// 去掉 "AS56047 " 前缀，只留机构名
	if i := strings.IndexByte(s, ' '); i > 0 && strings.HasPrefix(strings.ToUpper(s), "AS") {
		s = strings.TrimSpace(s[i+1:])
	}
	low := strings.ToLower(s)
	switch {
	// 中文库（如 zxinc）直接用中文名，先做匹配
	case strings.Contains(s, "中国移动"):
		return "中国移动"
	case strings.Contains(s, "中国电信"):
		return "中国电信"
	case strings.Contains(s, "中国联通"):
		return "中国联通"
	case strings.Contains(s, "中国广播电视网络"), strings.Contains(s, "中国广电"), strings.Contains(s, "中国有线"):
		return "中国广电"
	case strings.Contains(s, "中国教育和科研"), strings.Contains(s, "教育网"):
		return "教育网"
	case strings.Contains(s, "阿里云"):
		return "阿里云"
	case strings.Contains(s, "腾讯云"):
		return "腾讯云"
	case strings.Contains(s, "华为云"):
		return "华为云"
	case strings.Contains(low, "china mobile"), strings.Contains(low, "chinamobile"):
		return "中国移动"
	case strings.Contains(low, "china telecom"), strings.Contains(low, "chinanet"):
		return "中国电信"
	case strings.Contains(low, "china unicom"), strings.Contains(low, "cncgroup"), strings.Contains(low, "china169"):
		return "中国联通"
	case strings.Contains(low, "china broadcasting network"):
		return "中国广电"
	case strings.Contains(low, "cernet"), strings.Contains(low, "china education"):
		return "教育网"
	case strings.Contains(low, "aliyun"), strings.Contains(low, "alibaba"):
		return "阿里云"
	case strings.Contains(low, "tencent"):
		return "腾讯云"
	case strings.Contains(low, "huawei"):
		return "华为云"
	}
	return s
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

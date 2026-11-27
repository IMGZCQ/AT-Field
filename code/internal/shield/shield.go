// Package shield 负责“防护能力”与“NAS 健康指数”的评估逻辑。
// 健康指数完全由真实系统资源加权得出：内存余量、磁盘余量、CPU 空闲、
// 系统负载、温度。缺少某项数据时自动剔除该项并归一化权重。
package shield

import (
	"math"
	"strconv"
	"time"

	"atforcefield/internal/system"
)

// EnabledFeatures 三个防护功能开关
type EnabledFeatures struct {
	AtDefense  bool
	InvadeScan bool
	DataSeal   bool
}

// Log 一条防护/审计日志
type Log struct {
	Time   int64  `json:"time"`
	Level  string `json:"level"` // info | warn | block
	Module string `json:"module"`
	Msg    string `json:"msg"`
	// 以下为 0.1.43 新增的结构化字段，旧记录缺失时为空白（前端显示「—」）
	Object string `json:"object,omitempty"` // 对象：IP / 分区 / 硬盘 / 服务 / 渠道
	Detail string `json:"detail,omitempty"` // 结构化详情，形如 "账号=root; 端口=2222"
	Source string `json:"source,omitempty"` // 来源：应用 / 巡检 / 告警 / 推送
	Actor  string `json:"actor,omitempty"`  // 操作者：系统 / 用户
}

// HealthIndex 由真实系统指标计算 NAS 健康指数（0-100）。
// 权重：内存 30、磁盘 30、CPU 20、负载 10、温度 10；
// 数据不足时按现有项的权重归一化，完全无数据时返回 0。
// healthPart 单项评分（内部）
type healthPart struct {
	label  string
	score  float64
	weight float64
}

// HealthPart 健康指数构成项，用于前端解释分数来源
type HealthPart struct {
	Label  string  `json:"label"`
	Score  float64 `json:"score"`  // 该项 0-100 得分
	Weight float64 `json:"weight"` // 权重（折算为百分比）
	Loss   float64 `json:"loss"`   // 折算到总分的扣分，0 表示该项满分
}

// healthParts 计算各单项得分
func healthParts(m system.Metrics) []healthPart {
	parts := make([]healthPart, 0, 5)

	// 内存：可用率 30% 即得满分
	if m.TotalMemMB > 0 {
		ratio := float64(m.AvailableMB) / float64(m.TotalMemMB)
		parts = append(parts, healthPart{"内存余量", headroom(ratio, 0.30) * 100, 30})
	}
	// 磁盘：剩余空间 25% 即得满分
	if m.DiskTotalGB > 0 {
		free := 1 - float64(m.DiskUsedGB)/float64(m.DiskTotalGB)
		parts = append(parts, healthPart{"磁盘余量", headroom(free, 0.25) * 100, 30})
	}
	// CPU：空闲率
	if m.CPUUsagePct >= 0 {
		parts = append(parts, healthPart{"CPU 空闲", float64(100 - m.CPUUsagePct), 20})
	}
	// 负载：每核负载 1.0 记 50 分，2.0 及以上记 0 分
	if m.LoadAvg1 >= 0 {
		cores := float64(m.CPUCores)
		if cores <= 0 {
			cores = 1
		}
		ratio := m.LoadAvg1 / cores
		parts = append(parts, healthPart{"系统负载", clamp01(1-ratio/2) * 100, 10})
	}
	// 温度：45℃ 及以下满分，85℃ 归零
	if m.TempC > 0 {
		parts = append(parts, healthPart{"系统温度", clamp01((85-m.TempC)/40) * 100, 10})
	}
	return parts
}

// HealthIndex 综合健康指数（0-100），由各项加权平均得到
func HealthIndex(m system.Metrics) int {
	parts := healthParts(m)
	if len(parts) == 0 {
		return 0
	}
	var sumScore, sumWeight float64
	for _, p := range parts {
		sumScore += p.score * p.weight
		sumWeight += p.weight
	}
	if sumWeight == 0 {
		return 0
	}
	v := int(math.Round(sumScore / sumWeight))
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return v
}

// HealthBreakdown 返回健康指数的逐项构成，便于前端说明「为什么是这个分数」
func HealthBreakdown(m system.Metrics) []HealthPart {
	parts := healthParts(m)
	if len(parts) == 0 {
		return nil
	}
	var sumWeight float64
	for _, p := range parts {
		sumWeight += p.weight
	}
	if sumWeight == 0 {
		return nil
	}
	out := make([]HealthPart, 0, len(parts))
	for _, p := range parts {
		out = append(out, HealthPart{
			Label:  p.label,
			Score:  math.Round(p.score),
			Weight: math.Round(p.weight / sumWeight * 100),
			Loss:   math.Round((100 - p.score) * p.weight / sumWeight),
		})
	}
	return out
}

// HealthText 健康指数对应的状态文案
func HealthText(v int) string {
	switch {
	case v >= 85:
		return "系统状态优良"
	case v >= 70:
		return "系统状态良好"
	case v >= 50:
		return "系统负载偏高"
	case v > 0:
		return "系统资源紧张"
	default:
		return "等待采样数据"
	}
}

// DefensePower 根据启用功能数计算防护能力（%）
func DefensePower(cfg EnabledFeatures) int {
	enabled := 0
	if cfg.AtDefense {
		enabled++
	}
	if cfg.InvadeScan {
		enabled++
	}
	if cfg.DataSeal {
		enabled++
	}
	switch enabled {
	case 0:
		return 0
	case 1:
		return 60
	case 2:
		return 85
	default:
		return 100
	}
}

// BaselineItem 安全基线中的单个检查项
type BaselineItem struct {
	Name  string `json:"name"`
	Score int    `json:"score"`
	Max   int    `json:"max"`
	OK    bool   `json:"ok"`
	Note  string `json:"note"`
}

// SecChecks 真实安全检查结果（由 secwatch 巡检提供）
type SecChecks struct {
	FirewallOn      bool // 飞牛防火墙是否真正开启（读 /usr/trim/etc/fw.conf）
	FirewallRules   int  // 防火墙规则条数（不含 Docker）
	FirewallKnown   bool // 是否读到飞牛防火墙配置
	Fail2BanOn      bool // fail2ban 是否启用
	NftBanOn        bool // 本应用 nft 兜底封禁是否就绪
	Banned          int
	FailedLogins    int
	FailedIPs       int
	SmartChecked    int
	SmartFailed     int
	SSHPasswordAuth bool
	SSHConfigKnown  bool
}

// BaselineScore 计算安全基线分（0-100）。检查项按真实系统状态打分，
// 其中“防护功能”按开关数量折算，全部关闭时总分自然归零。
func BaselineScore(f EnabledFeatures, c SecChecks) (int, []BaselineItem) {
	items := make([]BaselineItem, 0, 6)

	feat := DefensePower(f) * 25 / 100
	items = append(items, BaselineItem{Name: "防护功能", Score: feat, Max: 25, OK: feat == 25, Note: featureNote(f)})

	fw := 0
	switch {
	case c.FirewallOn && c.FirewallRules >= 3:
		fw = 20
	case c.FirewallOn:
		fw = 12
	}
	items = append(items, BaselineItem{Name: "网络防火墙", Score: fw, Max: 20, OK: fw == 20, Note: firewallNote(c)})

	ban := 0
	if c.Fail2BanOn || c.NftBanOn {
		ban = 15
	}
	items = append(items, BaselineItem{Name: "自动封禁", Score: ban, Max: 15, OK: ban == 15, Note: banNote(c)})

	brute := 0
	switch {
	case c.FailedLogins == 0:
		brute = 20
	case c.FailedLogins <= 10:
		brute = 14
	case c.FailedLogins <= 50:
		brute = 6
	}
	items = append(items, BaselineItem{Name: "防暴力破解", Score: brute, Max: 20, OK: brute == 20, Note: bruteNote(c)})

	smart := 5
	if c.SmartChecked > 0 {
		if c.SmartFailed == 0 {
			smart = 10
		} else {
			smart = 0
		}
	}
	items = append(items, BaselineItem{Name: "硬盘健康", Score: smart, Max: 10, OK: smart == 10, Note: smartNote(c)})

	ssh := 5
	if c.SSHConfigKnown {
		if c.SSHPasswordAuth {
			ssh = 0
		} else {
			ssh = 10
		}
	}
	items = append(items, BaselineItem{Name: "SSH 安全登录", Score: ssh, Max: 10, OK: ssh == 10, Note: sshNote(c)})

	total := 0
	for _, it := range items {
		total += it.Score
	}
	return total, items
}

func featureNote(f EnabledFeatures) string {
	n := 0
	for _, v := range []bool{f.AtDefense, f.InvadeScan, f.DataSeal} {
		if v {
			n++
		}
	}
	if n == 3 {
		return "三项防护功能均已开启"
	}
	return "已开启 " + strconv.Itoa(n) + " / 3 项防护功能"
}

func firewallNote(c SecChecks) string {
	switch {
	case c.FirewallKnown && !c.FirewallOn:
		return "飞牛防火墙未开启（fw.conf enable=false）"
	case c.FirewallOn && c.FirewallRules > 0:
		return "已启用，共 " + strconv.Itoa(c.FirewallRules) + " 条规则（不含 Docker）"
	case c.FirewallOn:
		return "飞牛防火墙已开启"
	default:
		return "未检测到 nft / iptables 规则"
	}
}

func banNote(c SecChecks) string {
	switch {
	case c.Fail2BanOn:
		return "fail2ban 已启用，当前封禁 " + strconv.Itoa(c.Banned) + " 个 IP"
	case c.NftBanOn:
		return "本应用 nft 兜底封禁已就绪，当前封禁 " + strconv.Itoa(c.Banned) + " 个 IP"
	default:
		return "未检测到 fail2ban，nft 兜底封禁不可用"
	}
}

func bruteNote(c SecChecks) string {
	if c.FailedLogins == 0 {
		return "近 24 小时无失败登录"
	}
	return "近 24 小时失败登录 " + strconv.Itoa(c.FailedLogins) + " 次，来源 " + strconv.Itoa(c.FailedIPs) + " 个 IP"
}

func smartNote(c SecChecks) string {
	if c.SmartChecked == 0 {
		return "未检测到 SMART 数据"
	}
	if c.SmartFailed > 0 {
		return strconv.Itoa(c.SmartFailed) + " / " + strconv.Itoa(c.SmartChecked) + " 块硬盘健康异常"
	}
	return strconv.Itoa(c.SmartChecked) + " 块硬盘 SMART 全部正常"
}

func sshNote(c SecChecks) string {
	if !c.SSHConfigKnown {
		return "未读取到 sshd 配置"
	}
	if c.SSHPasswordAuth {
		return "仍允许密码登录，建议改用密钥"
	}
	return "已禁用密码登录"
}

// StatusText 运行状态文案
func StatusText(active bool) string {
	if active {
		return "已为您的 NAS 提供全面保护"
	}
	return "防护未启用"
}

// FormatTime 把 Unix 秒格式化为 "MM/DD HH:mm"
func FormatTime(ts int64) string {
	return time.Unix(ts, 0).Format("01/02 15:04")
}

func headroom(ratio, good float64) float64 {
	if good <= 0 {
		return 0
	}
	return clamp01(ratio / good)
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

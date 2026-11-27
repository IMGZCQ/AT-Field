package secwatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// 未安装 fail2ban 时的兜底封禁：本应用自建独立 nft 表 inet atff，
// 用带 timeout 的集合记录封禁 IP，由内核在到期后自动解封，不依赖任何外部服务。

const (
	nftTable = "atff" // 独立表名，卸载时整体删除
)

// BanInfo 一条当前生效的封禁记录
type BanInfo struct {
	IP      string `json:"ip"`
	Backend string `json:"backend"` // fail2ban | nft
	Expires int64  `json:"expires"` // 剩余秒数，0 表示未知
}

// nftBin 返回可用的 nft 可执行文件路径
func nftBin() (string, bool) {
	if p, err := exec.LookPath("nft"); err == nil {
		return p, true
	}
	for _, p := range []string{"/usr/sbin/nft", "/sbin/nft", "/usr/bin/nft"} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
	}
	return "", false
}

// nftRun 以参数数组方式执行 nft（不经过 shell，杜绝命令注入）
func nftRun(args ...string) (string, error) {
	bin, ok := nftBin()
	if !ok {
		return "", errors.New("未找到 nft 命令")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := firstLine(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), errors.New(msg)
	}
	return out.String(), nil
}

// firstLine 取 nft 报错的第一行（后续为定位用的尖号示意图）
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '\n'); i > 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// NftAvailable 报告本机是否可用 nft
func NftAvailable() bool {
	_, ok := nftBin()
	return ok
}

// nftTableExists 判断封禁表是否已存在
func nftTableExists() bool {
	out, err := nftRun("list", "tables")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "table inet "+nftTable {
			return true
		}
	}
	return false
}

// nftEnsure 确保封禁表 / 集合 / 链 / 丢弃规则齐备（幂等，可反复调用）
func nftEnsure() error {
	if !NftAvailable() {
		return errors.New("未找到 nft 命令")
	}
	if !nftTableExists() {
		if _, err := nftRun("add", "table", "inet", nftTable); err != nil {
			return errors.New("创建 nft 封禁表失败：" + err.Error())
		}
	}
	sets := []struct{ name, typ string }{
		{"ban4", "ipv4_addr"},
		{"ban6", "ipv6_addr"},
	}
	for _, s := range sets {
		if _, err := nftRun("list", "set", "inet", nftTable, s.name); err == nil {
			continue
		}
		if _, err := nftRun("add", "set", "inet", nftTable, s.name, "{", "type", s.typ+";", "flags", "timeout;", "}"); err != nil {
			return errors.New("创建 nft 封禁集合失败：" + err.Error())
		}
	}
	hooks := []struct{ chain, hook string }{
		{"input", "input"},
		{"forward", "forward"},
	}
	for _, h := range hooks {
		if _, err := nftRun("list", "chain", "inet", nftTable, h.chain); err == nil {
			continue
		}
		if _, err := nftRun("add", "chain", "inet", nftTable, h.chain, "{",
			"type", "filter", "hook", h.hook, "priority", "-10;", "policy", "accept;", "}"); err != nil {
			return errors.New("创建 nft 封禁链失败：" + err.Error())
		}
	}
	for _, h := range hooks {
		body, err := nftRun("list", "chain", "inet", nftTable, h.chain)
		if err != nil {
			continue
		}
		if !strings.Contains(body, "@ban4") {
			_, _ = nftRun("add", "rule", "inet", nftTable, h.chain, "ip", "saddr", "@ban4", "counter", "drop")
		}
		if !strings.Contains(body, "@ban6") {
			_, _ = nftRun("add", "rule", "inet", nftTable, h.chain, "ip6", "saddr", "@ban6", "counter", "drop")
		}
	}
	return nil
}

// nftSetOf 依据 IP 版本选择集合名
func nftSetOf(ip net.IP) string {
	if ip.To4() != nil {
		return "ban4"
	}
	return "ban6"
}

// nftBan 把 IP 写入封禁集合，重复封禁时刷新到期时间
func nftBan(ip string, timeout time.Duration) error {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return errors.New("IP 地址不合法")
	}
	if err := nftEnsure(); err != nil {
		return err
	}
	set := nftSetOf(parsed)
	dur := strconv.Itoa(int(timeout/time.Second)) + "s"
	if _, err := nftRun("add", "element", "inet", nftTable, set, "{", ip, "timeout", dur, "}"); err != nil {
		// 元素已存在时会报错，先删后加以刷新计时
		if _, e := nftRun("delete", "element", "inet", nftTable, set, "{", ip, "}"); e != nil {
			return errors.New("写入封禁规则失败：" + err.Error())
		}
		if _, e := nftRun("add", "element", "inet", nftTable, set, "{", ip, "timeout", dur, "}"); e != nil {
			return errors.New("写入封禁规则失败：" + e.Error())
		}
	}
	return nil
}

// nftUnban 从封禁集合中移除 IP（不存在时视为成功）
func nftUnban(ip string) error {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return errors.New("IP 地址不合法")
	}
	if !nftTableExists() {
		return nil
	}
	set := nftSetOf(parsed)
	list, err := nftListSet(set)
	if err != nil {
		return errors.New("读取 nft 封禁名单失败：" + err.Error())
	}
	found := false
	for _, b := range list {
		if b.IP == ip {
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	if _, err := nftRun("delete", "element", "inet", nftTable, set, "{", ip, "}"); err != nil {
		return errors.New("删除封禁规则失败：" + err.Error())
	}
	return nil
}

// nftListSet 读取某个集合内的封禁 IP 与剩余时间
func nftListSet(set string) ([]BanInfo, error) {
	if !nftTableExists() {
		return nil, nil
	}
	raw, err := nftRun("-j", "list", "set", "inet", nftTable, set)
	if err == nil {
		if list, ok := parseNftSetJSON(raw); ok {
			for i := range list {
				list[i].Backend = "nft"
			}
			return list, nil
		}
	}
	text, terr := nftRun("list", "set", "inet", nftTable, set)
	if terr != nil {
		if err != nil {
			return nil, err
		}
		return nil, terr
	}
	return parseNftSetText(text), nil
}

// parseNftSetJSON 解析 nft -j list set 的 JSON 输出
func parseNftSetJSON(raw string) ([]BanInfo, bool) {
	var doc struct {
		Nftables []struct {
			Set *struct {
				Elem []struct {
					Elem struct {
						Val     string `json:"val"`
						Timeout int64  `json:"timeout"`
						Expires int64  `json:"expires"`
					} `json:"elem"`
				} `json:"elem"`
			} `json:"set"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return nil, false
	}
	list := make([]BanInfo, 0, 8)
	for _, item := range doc.Nftables {
		if item.Set == nil {
			continue
		}
		for _, e := range item.Set.Elem {
			if e.Elem.Val == "" {
				continue
			}
			list = append(list, BanInfo{IP: e.Elem.Val, Expires: e.Elem.Expires})
		}
	}
	return list, true
}

// parseNftSetText 兜底解析 `nft list set` 的文本输出
func parseNftSetText(text string) []BanInfo {
	list := make([]BanInfo, 0, 8)
	idx := strings.Index(text, "elements = {")
	if idx < 0 {
		return list
	}
	body := text[idx+len("elements = {"):]
	if end := strings.Index(body, "}"); end >= 0 {
		body = body[:end]
	}
	for _, part := range strings.Split(body, ",") {
		f := strings.Fields(part)
		if len(f) == 0 || net.ParseIP(f[0]) == nil {
			continue
		}
		b := BanInfo{IP: f[0], Backend: "nft"}
		for i := 1; i+1 < len(f); i++ {
			if f[i] == "expires" {
				b.Expires = parseNftDuration(f[i+1])
			}
		}
		list = append(list, b)
	}
	return list
}

// parseNftDuration 把 1m30s / 59m59s996ms 这类时长解析为秒
func parseNftDuration(s string) int64 {
	if d, err := time.ParseDuration(s); err == nil {
		return int64(d / time.Second)
	}
	return 0
}

// nftBans 汇总当前 nft 封禁名单（v4 + v6）
func nftBans() []BanInfo {
	list := make([]BanInfo, 0, 8)
	for _, set := range []string{"ban4", "ban6"} {
		if items, err := nftListSet(set); err == nil {
			list = append(list, items...)
		}
	}
	return list
}

// nftDropTable 删除整个封禁表（卸载时清理）
func nftDropTable() {
	if !NftAvailable() || !nftTableExists() {
		return
	}
	_, _ = nftRun("delete", "table", "inet", nftTable)
}

// banAddressAllowed 校验被操作地址：拒绝回环 / 组播 / 本机网卡地址，避免把 NAS 自己封掉
func banAddressAllowed(ip string) error {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return errors.New("IP 地址不合法")
	}
	if parsed.IsLoopback() || parsed.IsUnspecified() || parsed.IsMulticast() || parsed.IsLinkLocalUnicast() {
		return errors.New("不允许封禁回环 / 组播 / 链路本地地址")
	}
	for _, local := range localAddresses() {
		if local.Equal(parsed) {
			return errors.New("该地址是本机网卡地址，封禁会导致 NAS 自身断网")
		}
	}
	return nil
}

// localAddresses 返回本机所有网卡地址
func localAddresses() []net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok {
			out = append(out, ipnet.IP)
		}
	}
	return out
}

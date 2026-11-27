// Package alert 在每次采样后评估告警阈值：触发时写入审计日志（默认 30 分钟内去重），
// 并可按设置推送到飞书 / 企业微信 / 钉钉群机器人（webhook 在设置页填写，飞书还可用
// ATFF_ALERT_WEBHOOK 兜底）。推送格式按地址自动识别：qyapi.weixin.qq.com 走企业微信，
// oapi.dingtalk.com 走钉钉（支持加签）。
package alert

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"atforcefield/internal/audit"
	"atforcefield/internal/shield"
	"atforcefield/internal/store"
	"atforcefield/internal/system"
)

// cooldown 同一条告警的抑制时间
const cooldown = 30 * time.Minute

// eventCooldown 安全事件推送的抑制时间：同一类事件（按 key 区分）在该时间内只推一次，
// 避免扫描 / 爆破持续期间反复刷屏。
const eventCooldown = 30 * time.Minute

// stateFile 落盘结构：last 记录上次触发时间（冷却用），active 记录当前处于告警中的项，
// 用于跨重启恢复状态，避免重启后重复推送、漏发恢复通知。
type stateFile struct {
	Last   map[string]int64 `json:"last"`
	Active map[string]bool  `json:"active"`
}

// Evaluator 阈值告警评估器
type Evaluator struct {
	au        *audit.Store
	cfg       func() store.Config
	statePath string // 去重/状态落盘路径，重启后不会重复推送

	mu        sync.Mutex
	lastFired map[string]time.Time
	active    map[string]bool
}

// New 创建评估器，cfg 用于实时读取用户设置的阈值与 webhook；
// statePath 用于持久化去重时间戳（空字符串表示不持久化）。
func New(au *audit.Store, cfg func() store.Config, statePath string) *Evaluator {
	e := &Evaluator{
		au:        au,
		cfg:       cfg,
		statePath: statePath,
		lastFired: map[string]time.Time{},
		active:    map[string]bool{},
	}
	e.loadState()
	return e
}

// loadState 读回上次运行的冷却时间戳与告警状态；冷却期内的保留，避免重启后重复推送
func (e *Evaluator) loadState() {
	if e.statePath == "" {
		return
	}
	raw, err := os.ReadFile(e.statePath)
	if err != nil {
		return
	}
	var sf stateFile
	if json.Unmarshal(raw, &sf) == nil && (sf.Last != nil || sf.Active != nil) {
		now := time.Now()
		for k, ts := range sf.Last {
			if t := time.Unix(ts, 0); now.Sub(t) < cooldown {
				e.lastFired[k] = t
			}
		}
		for k, v := range sf.Active {
			if v {
				e.active[k] = true
			}
		}
		return
	}
	// 兼容旧格式（纯 map[string]int64）
	var legacy map[string]int64
	if json.Unmarshal(raw, &legacy) != nil {
		return
	}
	now := time.Now()
	for k, ts := range legacy {
		if t := time.Unix(ts, 0); now.Sub(t) < cooldown {
			e.lastFired[k] = t
		}
	}
}

// saveStateLocked 落盘冷却时间戳与告警状态，调用方需持有锁
func (e *Evaluator) saveStateLocked() {
	if e.statePath == "" {
		return
	}
	sf := stateFile{Last: make(map[string]int64, len(e.lastFired)), Active: make(map[string]bool, len(e.active))}
	for k, t := range e.lastFired {
		sf.Last[k] = t.Unix()
	}
	for k, v := range e.active {
		if v {
			sf.Active[k] = true
		}
	}
	raw, err := json.Marshal(sf)
	if err != nil {
		return
	}
	tmp := e.statePath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, e.statePath)
}

// Evaluate 按真实指标检查阈值并触发告警
// cond 一条阈值条件：higher=true 表示数值越高越危险（达到阈值触发）
type cond struct {
	key    string
	module string
	level  string // 触发级别：warn / severe
	value  float64
	thr    float64
	hys    float64 // 恢复所需回差，避免在阈值附近反复触发
	higher bool
	firing string
	ok     string
}

// Evaluate 按真实指标检查阈值：进入告警发通知，回落到「阈值±回差」才发恢复通知
func (e *Evaluator) Evaluate(m system.Metrics, health int) {
	if m.Source != "real" {
		return
	}
	a := store.DefaultAlert()
	if e.cfg != nil {
		a = e.cfg().Alert
	}
	if !a.Monitor {
		// 监控与告警已关闭：不判定、不写日志、不推送
		return
	}
	e.pruneDisabled(a)

	conds := make([]cond, 0, 8)
	if health > 0 && a.WatchHealth {
		level := "warn"
		if health < 40 {
			level = "severe"
		}
		conds = append(conds, cond{
			key: "health", module: "健康指数", level: level,
			value: float64(health), thr: float64(a.HealthMin), hys: 3, higher: false,
			firing: "健康指数降至 " + itoa(health) + "%，低于设定阈值 " + itoa(a.HealthMin) + "%",
			ok:     "健康指数已回升至 " + itoa(health) + "%（阈值 " + itoa(a.HealthMin) + "%）",
		})
	}
	if m.DiskTotalGB > 0 && a.WatchDisk {
		pct := m.DiskUsedGB * 100 / m.DiskTotalGB
		level := "warn"
		if pct >= 95 {
			level = "severe"
		}
		conds = append(conds, cond{
			key: "disk", module: "存储空间", level: level,
			value: float64(pct), thr: float64(a.DiskPct), hys: 3, higher: true,
			firing: "磁盘使用率已达 " + itoa(int(pct)) + "%，请及时清理或扩容",
			ok:     "磁盘使用率已回落至 " + itoa(int(pct)) + "%",
		})
	}
	if m.TotalMemMB > 0 && a.WatchMem {
		pct := float64(m.TotalMemMB-m.AvailableMB) * 100 / float64(m.TotalMemMB)
		level := "warn"
		if pct >= 97 {
			level = "severe"
		}
		conds = append(conds, cond{
			key: "mem", module: "内存压力", level: level,
			value: pct, thr: float64(a.MemPct), hys: 3, higher: true,
			firing: "内存使用率已达 " + itoa(int(pct)) + "%，可能影响服务响应",
			ok:     "内存使用率已回落至 " + itoa(int(pct)) + "%",
		})
	}
	if m.TempC > 0 && a.WatchTemp {
		level := "warn"
		if m.TempC >= a.TempC+10 {
			level = "severe"
		}
		conds = append(conds, cond{
			key: "cpu-temp", module: "温度异常", level: level,
			value: m.TempC, thr: a.TempC, hys: 3, higher: true,
			firing: "CPU 温度达 " + ftoa(m.TempC) + "℃，超过阈值 " + ftoa(a.TempC) + "℃",
			ok:     "CPU 温度已回落至 " + ftoa(m.TempC) + "℃",
		})
	}
	if m.DiskTempC > 0 && a.WatchDiskTemp {
		level := "warn"
		if m.DiskTempC >= a.DiskTempC+10 {
			level = "severe"
		}
		conds = append(conds, cond{
			key: "disk-temp", module: "温度异常", level: level,
			value: m.DiskTempC, thr: a.DiskTempC, hys: 3, higher: true,
			firing: "硬盘温度达 " + ftoa(m.DiskTempC) + "℃，超过阈值 " + ftoa(a.DiskTempC) + "℃",
			ok:     "硬盘温度已回落至 " + ftoa(m.DiskTempC) + "℃",
		})
	}
	if a.WatchVolume {
		for _, v := range m.DiskVolumes {
			if v.TotalGB <= 0 {
				continue
			}
			pct := v.UsedGB * 100 / v.TotalGB
			level := "warn"
			if pct >= 95 {
				level = "severe"
			}
			conds = append(conds, cond{
				key: "vol:" + v.Mount, module: "存储空间", level: level,
				value: float64(pct), thr: float64(a.VolumePct), hys: 3, higher: true,
				firing: v.Mount + " 使用率已达 " + itoa(int(pct)) + "%，超过阈值 " + itoa(a.VolumePct) + "%",
				ok:     v.Mount + " 使用率已回落至 " + itoa(int(pct)) + "%",
			})
		}
	}
	for _, c := range conds {
		e.transition(c)
	}
}

// pruneDisabled 单项监控被关闭时，静默清掉该项的告警状态（不发恢复通知），
// 否则重新开启时会误判为「一直处于告警中」而漏发通知。
func (e *Evaluator) pruneDisabled(a store.AlertConfig) {
	e.mu.Lock()
	defer e.mu.Unlock()
	changed := false
	for k := range e.active {
		switch {
		case !a.WatchHealth && k == "health",
			!a.WatchDisk && k == "disk",
			!a.WatchMem && k == "mem",
			!a.WatchTemp && k == "cpu-temp",
			!a.WatchDiskTemp && k == "disk-temp",
			!a.WatchVolume && strings.HasPrefix(k, "vol:"):
			delete(e.active, k)
			changed = true
		}
	}
	if changed {
		e.saveStateLocked()
	}
}

// transition 处理单条条件的状态变化：未告警→告警发通知，告警→恢复发恢复通知
func (e *Evaluator) transition(c cond) {
	e.mu.Lock()
	wasActive := e.active[c.key]
	var firing bool
	if wasActive {
		// 已告警：必须回落到带内（阈值∓回差）才算恢复，避免阈值附近反复触发
		if c.higher {
			firing = c.value >= c.thr-c.hys
		} else {
			firing = c.value < c.thr+c.hys
		}
	} else if c.higher {
		firing = c.value >= c.thr
	} else {
		firing = c.value < c.thr
	}
	if firing == wasActive {
		e.mu.Unlock()
		return
	}
	now := time.Now()
	blocked := false
	if firing {
		e.active[c.key] = true
		// 冷却只抑制「触发」通知，恢复通知总是发
		if t, ok := e.lastFired[c.key]; ok && now.Sub(t) < cooldown {
			blocked = true
		}
		e.lastFired[c.key] = now
	} else {
		delete(e.active, c.key)
	}
	e.saveStateLocked()
	e.mu.Unlock()

	if blocked {
		return
	}

	level, module, msg := c.level, c.module, c.firing
	if !firing {
		level, msg = "info", c.ok
	}
	if e.au != nil {
		e.au.AddDetail(shield.Log{Level: level, Module: module, Msg: msg, Source: "告警", Actor: "系统"})
	}
	// 消息推送已关闭：照常写日志，只是不发 webhook
	if e.cfg != nil && !e.cfg().Alert.Push {
		return
	}
	for _, h := range e.hooks() {
		e.pushAsync(h, msg)
	}
}

// Notify 推送一条安全事件通知（自动封禁 / SSH 异常登录 / 异常访问等）。
// 受「监控与告警」「消息推送」两个开关约束；同类事件 30 分钟内只推送一次。
// module 为空表示调用方已自行写入审计日志，这里不再重复记录。
func (e *Evaluator) Notify(key, module, msg string) {
	a := store.DefaultAlert()
	if e.cfg != nil {
		a = e.cfg().Alert
	}
	if !a.Monitor {
		return
	}
	e.mu.Lock()
	if t, ok := e.lastFired[key]; ok && time.Since(t) < eventCooldown {
		e.mu.Unlock()
		return
	}
	e.lastFired[key] = time.Now()
	e.saveStateLocked()
	e.mu.Unlock()

	if module != "" && e.au != nil {
		e.au.AddDetail(shield.Log{Level: "warn", Module: module, Msg: msg, Source: "告警", Actor: "系统"})
	}
	if !a.Push {
		return
	}
	for _, h := range e.hooks() {
		e.pushAsync(h, msg)
	}
}

// Test 发送测试消息到所有已配置的机器人（飞书 / 企业微信），返回失败原因汇总
func (e *Evaluator) Test() error {
	hooks := e.hooks()
	if len(hooks) == 0 {
		return errors.New("尚未填写飞书 / 企业微信 / 钉钉 Webhook 地址")
	}
	var errs []string
	for _, h := range hooks {
		if err := ValidateHook(h); err != nil {
			errs = append(errs, channel(h)+"："+err.Error())
			continue
		}
		if err := e.push(h, "这是一条测试消息，收到即表示告警推送配置成功。"); err != nil {
			errs = append(errs, channel(h)+"："+err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "；"))
	}
	return nil
}

// pushAsync 异步推送，失败重试一次并写入审计日志，
// 避免网络抖动或 token 失效时静默丢失且用户毫不知情。
func (e *Evaluator) pushAsync(hook, msg string) {
	go func() {
		var err error
		for i := 0; i < 2; i++ {
			if err = e.push(hook, msg); err == nil {
				return
			}
			time.Sleep(time.Second)
		}
		if e.au != nil {
			e.au.AddDetail(shield.Log{Level: "warn", Module: "告警推送", Msg: channel(hook) + "推送失败（已重试 2 次）：" + err.Error(), Source: "推送", Actor: "系统", Object: channel(hook)})
		}
	}()
}

// ValidateHook 校验 webhook 地址：仅允许 https 且为飞书 / 企业微信 / 钉钉官方域名，
// 防止用户误填或把内网地址写进配置造成 SSRF。空字符串表示未配置，返回 nil。
func ValidateHook(hook string) error {
	h := strings.TrimSpace(hook)
	if h == "" {
		return nil
	}
	u, err := url.Parse(h)
	if err != nil || u.Host == "" {
		return errors.New("不是有效的网址")
	}
	if u.Scheme != "https" {
		return errors.New("必须以 https:// 开头")
	}
	switch strings.ToLower(u.Hostname()) {
	case "open.feishu.cn", "open.larksuite.com", "open.larkoffice.com",
		"qyapi.weixin.qq.com", "oapi.dingtalk.com":
		return nil
	}
	return errors.New("只支持飞书（open.feishu.cn / open.larksuite.com）、企业微信（qyapi.weixin.qq.com）或钉钉（oapi.dingtalk.com）机器人地址")
}

// hooks 返回已配置的推送地址：飞书（支持环境变量兜底）、企业微信与钉钉
func (e *Evaluator) hooks() []string {
	var out []string
	a := store.AlertConfig{}
	if e.cfg != nil {
		a = e.cfg().Alert
	}
	if v := strings.TrimSpace(a.Webhook); v != "" {
		out = append(out, v)
	} else if v := strings.TrimSpace(os.Getenv("ATFF_ALERT_WEBHOOK")); v != "" {
		out = append(out, v)
	}
	if v := strings.TrimSpace(a.WecomWebhook); v != "" {
		out = append(out, v)
	}
	if v := strings.TrimSpace(a.DingWebhook); v != "" {
		out = append(out, v)
	}
	return out
}

// isWecom 判断是否为企业微信群机器人地址
func isWecom(hook string) bool {
	return strings.Contains(hook, "qyapi.weixin.qq.com")
}

// isDing 判断是否为钉钉群机器人地址
func isDing(hook string) bool {
	return strings.Contains(hook, "oapi.dingtalk.com")
}

// channel 渠道名称，用于错误提示
func channel(hook string) string {
	switch {
	case isWecom(hook):
		return "企业微信"
	case isDing(hook):
		return "钉钉"
	default:
		return "飞书"
	}
}

// push 按渠道格式推送消息：企业微信/钉钉用 msgtype/text，飞书用 msg_type/content
func (e *Evaluator) push(hook, msg string) error {
	if err := ValidateHook(hook); err != nil {
		return err
	}
	text := "【飞牛AT力场】" + msg
	postURL := hook
	var body any
	switch {
	case isWecom(hook), isDing(hook):
		body = map[string]any{"msgtype": "text", "text": map[string]string{"content": text}}
		// 钉钉机器人开启「加签」时，必须在 URL 上追加 timestamp + sign
		if isDing(hook) {
			if secret := e.dingSecret(); secret != "" {
				ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
				sep := "?"
				if strings.Contains(postURL, "?") {
					sep = "&"
				}
				postURL += sep + "timestamp=" + ts + "&sign=" + url.QueryEscape(dingSign(ts, secret))
			}
		}
	default:
		body = map[string]any{"msg_type": "text", "content": map[string]string{"text": text}}
		// 飞书机器人开启「签名校验」时，必须带 timestamp + sign，否则返回 19021
		if secret := e.feishuSecret(); secret != "" {
			ts := strconv.FormatInt(time.Now().Unix(), 10)
			body = map[string]any{
				"timestamp": ts,
				"sign":      feishuSign(ts, secret),
				"msg_type":  "text",
				"content":   map[string]string{"text": text},
			}
		}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 6 * time.Second}
	resp, err := client.Post(postURL, "application/json", bytes.NewReader(raw))
	if err != nil {
		return errors.New("推送失败：" + err.Error())
	}
	defer resp.Body.Close()
	brief, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("机器人返回 HTTP " + itoa(resp.StatusCode) + "：" + string(brief))
	}
	if isDing(hook) {
		// 钉钉返回 errcode != 0 表示业务失败（如签名不匹配 310000）
		var dr struct {
			ErrCode int    `json:"errcode"`
			ErrMsg  string `json:"errmsg"`
		}
		if err := json.Unmarshal(brief, &dr); err == nil && dr.ErrCode != 0 {
			return errors.New("返回错误 " + itoa(dr.ErrCode) + "：" + dr.ErrMsg)
		}
		return nil
	}
	if isWecom(hook) {
		// 企业微信返回 errcode != 0 表示业务失败
		var wr struct {
			ErrCode int    `json:"errcode"`
			ErrMsg  string `json:"errmsg"`
		}
		if err := json.Unmarshal(brief, &wr); err == nil && wr.ErrCode != 0 {
			return errors.New("返回错误 " + itoa(wr.ErrCode) + "：" + wr.ErrMsg)
		}
		return nil
	}
	// 飞书返回 code != 0 表示业务失败
	var fb struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(brief, &fb); err == nil && fb.Code != 0 {
		return errors.New("机器人返回错误：" + fb.Msg)
	}
	return nil
}

// feishuSecret 飞书签名密钥：优先使用界面配置，其次环境变量
func (e *Evaluator) feishuSecret() string {
	if e.cfg != nil {
		if v := strings.TrimSpace(e.cfg().Alert.FeishuSecret); v != "" {
			return v
		}
	}
	return strings.TrimSpace(os.Getenv("ATFF_ALERT_SECRET"))
}

// feishuSign 飞书机器人签名：以 "timestamp\nsecret" 为 HMAC-SHA256 的密钥，对空串签名后 base64
func feishuSign(ts, secret string) string {
	h := hmac.New(sha256.New, []byte(ts+"\n"+secret))
	h.Write(nil)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// dingSecret 钉钉签名密钥：优先使用界面配置，其次环境变量
func (e *Evaluator) dingSecret() string {
	if e.cfg != nil {
		if v := strings.TrimSpace(e.cfg().Alert.DingSecret); v != "" {
			return v
		}
	}
	return strings.TrimSpace(os.Getenv("ATFF_DING_SECRET"))
}

// dingSign 钉钉机器人加签：timestamp(毫秒) + "\n" + secret 作为密钥，
// 对空串做 HMAC-SHA256 后 base64（外层再做 URL 转义）
func dingSign(ts, secret string) string {
	h := hmac.New(sha256.New, []byte(ts+"\n"+secret))
	h.Write(nil)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func itoa(v int) string {
	return strconv.Itoa(v)
}

func ftoa(v float64) string {
	return strconv.FormatFloat(v, 'f', 1, 64)
}

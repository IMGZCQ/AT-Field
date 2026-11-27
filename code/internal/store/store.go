// Package store 持久化飞牛AT力场的运行偏好（同步率目标、防护开关等）。
package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"atforcefield/internal/config"
)

// AlertConfig 告警设置：飞书 / 企业微信 / 钉钉 webhook 与各项阈值
type AlertConfig struct {
	Monitor      bool    `json:"monitor"` // 监控与告警（关闭则不判定、不写日志、不推送）
	Push         bool    `json:"push"`    // 消息推送（关闭时照常判定并写日志，只是不发 webhook）
	Webhook      string  `json:"webhook"`
	WecomWebhook string  `json:"wecomWebhook"` // 企业微信群机器人 Webhook
	FeishuSecret string  `json:"feishuSecret"` // 飞书机器人「签名校验」密钥（可选）
	DingWebhook  string  `json:"dingWebhook"`  // 钉钉群机器人 Webhook（含 access_token）
	DingSecret   string  `json:"dingSecret"`   // 钉钉机器人「加签」密钥（可选）
	HealthMin    int     `json:"healthMin"`    // 健康指数低于该值告警
	DiskPct      int     `json:"diskPct"`      // 磁盘使用率阈值（%）
	MemPct       int     `json:"memPct"`       // 内存使用率阈值（%）
	TempC        float64 `json:"tempC"`        // CPU/系统最高温度阈值（℃）
	DiskTempC    float64 `json:"diskTempC"`    // 硬盘温度阈值（℃）
	VolumePct    int     `json:"volumePct"`    // 单个分区使用率阈值（%）

	// 以下为单项监控开关（默认全部开启）：关闭后该项不判定阈值、不写日志
	WatchHealth   bool `json:"watchHealth"`
	WatchDisk     bool `json:"watchDisk"`
	WatchMem      bool `json:"watchMem"`
	WatchVolume   bool `json:"watchVolume"`
	WatchTemp     bool `json:"watchTemp"`
	WatchDiskTemp bool `json:"watchDiskTemp"`
}

// ScanBanConfig 扫描防护：访问日志里异常（4xx / 5xx）请求过多时自动封禁来源 IP。
// 默认关闭，需在设置页手动开启。
type ScanBanConfig struct {
	Enabled   bool   `json:"enabled"`
	Threshold int    `json:"threshold"` // 10 分钟内的异常请求次数阈值
	Duration  string `json:"duration"`  // 1h | 24h | 7d | 365d
}

// Config 持久化偏好：三个防护功能开关 + 扫描防护 + 告警设置。
// 健康指数与资源指标均为实时采集的真实数据，不再持久化。
type Config struct {
	AtDefense  bool          `json:"atDefense"`  // AT 力场防护
	InvadeScan bool          `json:"invadeScan"` // 实时入侵检测
	DataSeal   bool          `json:"dataSeal"`   // 数据安全加固
	ScanBan    ScanBanConfig `json:"scanBan"`
	Alert      AlertConfig   `json:"alert"`
}

// DefaultScanBan 返回默认扫描防护设置（关闭）
func DefaultScanBan() ScanBanConfig {
	return ScanBanConfig{Enabled: false, Threshold: 60, Duration: "24h"}
}

// DefaultAlert 返回默认告警设置
func DefaultAlert() AlertConfig {
	return AlertConfig{
		Monitor: true, Push: true,
		HealthMin: 60, DiskPct: 90, MemPct: 92, TempC: 80, DiskTempC: 65, VolumePct: 92,
		WatchHealth: true, WatchDisk: true, WatchMem: true,
		WatchVolume: true, WatchTemp: true, WatchDiskTemp: true,
	}
}

// Default 返回默认偏好
func Default() Config {
	return Config{AtDefense: true, InvadeScan: true, DataSeal: true, ScanBan: DefaultScanBan(), Alert: DefaultAlert()}
}

// Normalize 补齐缺失或非法的告警阈值（兼容旧版本配置文件）
func (c *Config) Normalize() {
	d := DefaultAlert()
	ds := DefaultScanBan()
	if c.ScanBan.Threshold < 10 || c.ScanBan.Threshold > 10000 {
		c.ScanBan.Threshold = ds.Threshold
	}
	switch c.ScanBan.Duration {
	case "1h", "24h", "7d", "365d":
	default:
		c.ScanBan.Duration = ds.Duration
	}
	c.Alert.Webhook = strings.TrimSpace(c.Alert.Webhook)
	c.Alert.WecomWebhook = strings.TrimSpace(c.Alert.WecomWebhook)
	c.Alert.FeishuSecret = strings.TrimSpace(c.Alert.FeishuSecret)
	c.Alert.DingWebhook = strings.TrimSpace(c.Alert.DingWebhook)
	c.Alert.DingSecret = strings.TrimSpace(c.Alert.DingSecret)
	if c.Alert.HealthMin <= 0 || c.Alert.HealthMin > 100 {
		c.Alert.HealthMin = d.HealthMin
	}
	if c.Alert.DiskPct <= 0 || c.Alert.DiskPct > 100 {
		c.Alert.DiskPct = d.DiskPct
	}
	if c.Alert.MemPct <= 0 || c.Alert.MemPct > 100 {
		c.Alert.MemPct = d.MemPct
	}
	if c.Alert.VolumePct <= 0 || c.Alert.VolumePct > 100 {
		c.Alert.VolumePct = d.VolumePct
	}
	if c.Alert.TempC <= 0 || c.Alert.TempC > 120 {
		c.Alert.TempC = d.TempC
	}
	if c.Alert.DiskTempC <= 0 || c.Alert.DiskTempC > 100 {
		c.Alert.DiskTempC = d.DiskTempC
	}
}

type fileData struct {
	Config    Config `json:"config"`
	StartedAt int64  `json:"startedAt"`
	EnabledAt int64  `json:"enabledAt"`
}

// migrateLegacyAlert 把旧版（0.1.33 及更早）的反向开关 muted / mutePush
// 迁移成新版的正向开关 monitor / push，保证升级后告警行为不变：
//
//	muted=false, mutePush=false → monitor=true,  push=true   （都开启）
//	muted=true                 → monitor=false, push=false   （全部静音）
//	muted=false, mutePush=true → monitor=true,  push=false   （只记日志不推送）
//
// 配置里已经有 monitor / push 字段时不做任何改动。
func migrateLegacyAlert(raw []byte, c *Config) {
	var probe struct {
		Config struct {
			Alert map[string]json.RawMessage `json:"alert"`
		} `json:"config"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		return
	}
	field := probe.Config.Alert
	if _, ok := field["monitor"]; ok {
		return
	}
	if _, ok := field["push"]; ok {
		return
	}
	boolOf := func(key string) (bool, bool) {
		r, ok := field[key]
		if !ok {
			return false, false
		}
		var v bool
		if json.Unmarshal(r, &v) != nil {
			return false, false
		}
		return v, true
	}
	muted, hasMuted := boolOf("muted")
	mutePush, hasMutePush := boolOf("mutePush")
	if !hasMuted && !hasMutePush {
		return // 全新配置：沿用默认值（监控与推送均开启）
	}
	c.Alert.Monitor = !muted
	c.Alert.Push = !(muted || mutePush)
}

// Store 线程安全的偏好仓库
type Store struct {
	mu   sync.RWMutex
	path string
	data fileData
}

// Open 读取（或初始化）持久化文件
func Open() (*Store, error) {
	if err := config.EnsureDirs(); err != nil {
		return nil, err
	}
	s := &Store{path: config.ConfigFile()}
	now := time.Now().Unix()
	s.data = fileData{Config: Default(), StartedAt: now, EnabledAt: now}

	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, s.flush()
		}
		return nil, err
	}
	if len(trimSpace(string(raw))) > 0 {
		// 先用默认值填充，配置里缺失的字段（含新增的开关）不会退化成 false
		fd := fileData{Config: Default(), StartedAt: now, EnabledAt: now}
		if err := json.Unmarshal(raw, &fd); err != nil {
			return nil, err
		}
		migrateLegacyAlert(raw, &fd.Config)
		fd.Config.Normalize()
		if fd.StartedAt == 0 {
			fd.StartedAt = now
		}
		if fd.EnabledAt == 0 {
			fd.EnabledAt = now
		}
		s.data = fd
	}
	return s, nil
}

// Config 返回偏好的副本
func (s *Store) Config() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.Config
}

// Save 更新偏好并持久化
func (s *Store) Save(c Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.Normalize()
	s.data.Config = c
	return s.flushLocked()
}

// EnabledAt 返回防护启用时间（Unix 秒）
func (s *Store) EnabledAt() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.EnabledAt
}

// SetEnabledAt 更新启用时间（一键启用/关闭时调用）
func (s *Store) SetEnabledAt(t int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.EnabledAt = t
	_ = s.flushLocked()
}

// flush 原子写盘
func (s *Store) flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushLocked()
}

func (s *Store) flushLocked() error {
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	// 0600：配置文件含 Webhook 地址与签名密钥，等同凭据，不对其他用户开放
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Clean(s.path))
}

func trimSpace(s string) string {
	i, j := 0, len(s)
	for i < j {
		if s[i] < ' ' {
			i++
			continue
		}
		break
	}
	for i < j && s[j-1] < ' ' {
		j--
	}
	return s[i:j]
}

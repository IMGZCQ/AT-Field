package store

import "testing"

// 旧版本配置文件里没有新加的阈值 / 监控项，Normalize 必须补成默认值而不是留 0
func TestNormalizeFillsDefaults(t *testing.T) {
	var c Config
	c.Normalize()
	d := DefaultAlert()
	if c.Alert.HealthMin != d.HealthMin || c.Alert.DiskPct != d.DiskPct || c.Alert.MemPct != d.MemPct ||
		c.Alert.VolumePct != d.VolumePct || c.Alert.TempC != d.TempC || c.Alert.DiskTempC != d.DiskTempC {
		t.Fatalf("零值配置未被补齐：%+v", c.Alert)
	}
}

// 用户自己调过的合法阈值必须原样保留，不能被默认值盖掉
func TestNormalizeKeepsValidValues(t *testing.T) {
	c := Config{Alert: AlertConfig{HealthMin: 75, DiskPct: 95, MemPct: 88, VolumePct: 80, TempC: 85, DiskTempC: 70}}
	c.Normalize()
	if c.Alert.HealthMin != 75 || c.Alert.DiskPct != 95 || c.Alert.MemPct != 88 ||
		c.Alert.VolumePct != 80 || c.Alert.TempC != 85 || c.Alert.DiskTempC != 70 {
		t.Fatalf("合法阈值被改写：%+v", c.Alert)
	}
}

// 超出合理范围的阈值（负数、>100、>120）要回落到默认
func TestNormalizeClampsOutOfRange(t *testing.T) {
	c := Config{Alert: AlertConfig{HealthMin: -1, DiskPct: 101, MemPct: 0, VolumePct: 200, TempC: 200, DiskTempC: 120}}
	c.Normalize()
	d := DefaultAlert()
	if c.Alert.HealthMin != d.HealthMin || c.Alert.DiskPct != d.DiskPct || c.Alert.MemPct != d.MemPct ||
		c.Alert.VolumePct != d.VolumePct || c.Alert.TempC != d.TempC || c.Alert.DiskTempC != d.DiskTempC {
		t.Fatalf("越界阈值未回落：%+v", c.Alert)
	}
}

// Webhook 两侧空白要清掉，否则校验和推送都会因为多余空格失败
func TestNormalizeTrimsWebhooks(t *testing.T) {
	c := Config{Alert: AlertConfig{
		Webhook:       "  https://open.feishu.cn/a  ",
		WecomWebhook:  "\thttps://qyapi.weixin.qq.com/b\n",
		FeishuSecret:  " s3cret ",
		DingWebhook:   " https://oapi.dingtalk.com/c ",
		DingSecret:    " d3cret ",
	}}
	c.Normalize()
	if c.Alert.Webhook != "https://open.feishu.cn/a" || c.Alert.WecomWebhook != "https://qyapi.weixin.qq.com/b" ||
		c.Alert.FeishuSecret != "s3cret" || c.Alert.DingWebhook != "https://oapi.dingtalk.com/c" || c.Alert.DingSecret != "d3cret" {
		t.Fatalf("Webhook 空白未被清掉：%+v", c.Alert)
	}
}

// 0.1.33 及更早的 muted / mutePush 反向开关要迁移成正向的 monitor / push，
// 保证升级后「什么都没变」；配置里已经有 monitor 时不覆盖。
func TestMigrateLegacyAlert(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		base    Config // 迁移前从文件里读到的值
		monitor bool
		push    bool
	}{
		{"都未静音", `{"config":{"alert":{"muted":false,"mutePush":false}}}`, Default(), true, true},
		{"全部静音", `{"config":{"alert":{"muted":true}}}`, Default(), false, false},
		{"只静音推送", `{"config":{"alert":{"muted":false,"mutePush":true}}}`, Default(), true, false},
		// 已经是新格式（有 monitor 字段）：迁移必须完全不碰，哪怕状态是关的
		{"已有 monitor 时不覆盖", `{"config":{"alert":{"monitor":false,"push":false,"muted":false}}}`,
			Config{Alert: AlertConfig{Monitor: false, Push: false}}, false, false},
		{"没有 alert 字段", `{"config":{}}`, Default(), true, true},
	}
	for _, tc := range cases {
		c := tc.base
		migrateLegacyAlert([]byte(tc.raw), &c)
		if c.Alert.Monitor != tc.monitor || c.Alert.Push != tc.push {
			t.Errorf("%s：monitor=%v push=%v，期望 monitor=%v push=%v", tc.name, c.Alert.Monitor, c.Alert.Push, tc.monitor, tc.push)
		}
	}
}

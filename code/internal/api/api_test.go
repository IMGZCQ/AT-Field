package api

import (
	"testing"

	"atforcefield/internal/config"
	"atforcefield/internal/system"
)

// splitLocalPort 与 system 包里的 splitHostPort 同构，但要能容忍空串与缺失端口
func TestSplitLocalPort(t *testing.T) {
	cases := []struct {
		in   string
		addr string
		port int
	}{
		{"127.0.0.1:9971", "127.0.0.1", 9971},
		{"[::1]:22", "::1", 22},
		{"[2408:8207::5]:443", "2408:8207::5", 443},
		{"*:*", "*", 0},
		{"", "", 0},
	}
	for _, c := range cases {
		addr, port := splitLocalPort(c.in)
		if addr != c.addr || port != c.port {
			t.Errorf("splitLocalPort(%q) = (%q, %d)，期望 (%q, %d)", c.in, addr, port, c.addr, c.port)
		}
	}
}

// 敏感端口只做提醒，不禁止断开；普通业务端口不应被标记
func TestConnSensitiveReason(t *testing.T) {
	cases := map[string]bool{
		"192.168.1.10:22":   true,
		"192.168.1.10:445":  true,
		"192.168.1.10:2049": true,
		"192.168.1.10:5666": true,
		"192.168.1.10:5668": false,
		"192.168.1.10:8080": false,
		"127.0.0.1:9971":    false,
	}
	for in, want := range cases {
		got := connSensitiveReason(in) != ""
		if got != want {
			t.Errorf("connSensitiveReason(%q) 敏感=%v，期望 %v", in, got, want)
		}
	}
}

// 本应用自身端口必须被硬拦：断了它界面立刻失联，没有任何收益
func TestConnLockedReason(t *testing.T) {
	t.Setenv(config.EnvAddr, "127.0.0.1:9971")
	if connLockedReason("127.0.0.1:9971") == "" {
		t.Error("本应用自身端口应判定为受保护")
	}
	if connLockedReason("192.168.1.10:22") != "" {
		t.Error("其它端口不应被判定为受保护")
	}
	// 监听地址换成自定义端口时，跟着变
	t.Setenv(config.EnvAddr, "127.0.0.1:12345")
	if connLockedReason("127.0.0.1:12345") == "" {
		t.Error("自定义端口应判定为受保护")
	}
	if connLockedReason("127.0.0.1:9971") != "" {
		t.Error("旧端口不应再受保护")
	}
}

// annotateConns 同时只给出一种标记：受保护的优先，其次才是敏感提示
func TestAnnotateConns(t *testing.T) {
	t.Setenv(config.EnvAddr, "127.0.0.1:9971")
	list := []system.ConnStream{
		{Local: "127.0.0.1:9971", Remote: "127.0.0.1:51000"},
		{Local: "192.168.1.10:22", Remote: "192.168.1.50:52000"},
		{Local: "192.168.1.10:5668", Remote: "192.168.1.50:52001"},
	}
	annotateConns(list)
	if !list[0].Locked || list[0].LockReason == "" {
		t.Errorf("本应用端口应被标记为受保护：%+v", list[0])
	}
	if list[0].Sensitive != "" {
		t.Errorf("受保护的连接不应再带敏感说明：%+v", list[0])
	}
	if list[1].Locked || list[1].Sensitive == "" {
		t.Errorf("22 端口应只有敏感标记：%+v", list[1])
	}
	if list[2].Locked || list[2].Sensitive != "" {
		t.Errorf("普通业务端口不应有任何标记：%+v", list[2])
	}
}

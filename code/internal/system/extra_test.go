package system

import "testing"

// splitHostPort 是 ss 输出解析的地基，IPv4 / IPv6 / 通配三种形态都要对
func TestSplitHostPort(t *testing.T) {
	cases := []struct {
		in   string
		addr string
		port int
	}{
		{"127.0.0.1:5668", "127.0.0.1", 5668},
		{"0.0.0.0:80", "0.0.0.0", 80},
		{"[::]:443", "::", 443},
		{"[::1]:9971", "::1", 9971},
		{"[2408:8207:1f2:3a10::5]:33112", "2408:8207:1f2:3a10::5", 33112},
		{"*:*", "*", 0},
		{"127.0.0.1:*", "127.0.0.1", 0},
		{"", "", 0},
		{"nope", "nope", 0},
	}
	for _, c := range cases {
		addr, port := splitHostPort(c.in)
		if addr != c.addr || port != c.port {
			t.Errorf("splitHostPort(%q) = (%q, %d)，期望 (%q, %d)", c.in, addr, port, c.addr, c.port)
		}
	}
}

// protoOf 靠方括号与冒号数量区分 tcp / tcp6
func TestProtoOf(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:22":   "tcp",
		"0.0.0.0:5668":   "tcp",
		"[::]:443":       "tcp6",
		"[::1]:9971":     "tcp6",
		"2408:8207::5:22": "tcp6",
	}
	for in, want := range cases {
		if got := protoOf(in); got != want {
			t.Errorf("protoOf(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// parseSSProc 取的是 users:(("名字",pid=N,fd=M)) 里的名字与 PID；
// 多个 socket 归属同一个进程时（nginx worker）应当取第一个。
func TestParseSSProc(t *testing.T) {
	cases := []struct {
		in   string
		name string
		pid  int
	}{
		{`users:(("sshd",pid=2751,fd=3))`, "sshd", 2751},
		{`users:(("nginx",pid=823,fd=6),("nginx",pid=824,fd=6))`, "nginx", 823},
		{`users:(("at-forcefield",pid=2104,fd=11))`, "at-forcefield", 2104},
		{"", "", 0},
		{"users:()", "", 0},
	}
	for _, c := range cases {
		name, pid := parseSSProc(c.in)
		if name != c.name || pid != c.pid {
			t.Errorf("parseSSProc(%q) = (%q, %d)，期望 (%q, %d)", c.in, name, pid, c.name, c.pid)
		}
	}
}

// decodeProcAddr 要处理 IPv4 小端序、IPv6 的 4 个 32 位字各自小端、以及 ::ffff: 映射
func TestDecodeProcAddr(t *testing.T) {
	cases := map[string]string{
		"0100007F:1F90":                     "127.0.0.1:8080", // 127.0.0.1:8080
		"0100007F:0016":                     "127.0.0.1:22",
		"00000000:0016":                     "",             // 0.0.0.0 未连接，丢弃
		"00000000000000000000000000000000:0016": "",          // :: 未连接，丢弃
		"0000000000000000FFFF00000A01A8C0:0016": "192.168.1.10:22", // IPv4 映射地址按 IPv4 呈现
		"garbage":                           "",
	}
	for in, want := range cases {
		if got := decodeProcAddr(in); got != want {
			t.Errorf("decodeProcAddr(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// isPublicBind 决定监听端口是否会在「公网暴露」里被标出来
func TestIsPublicBind(t *testing.T) {
	cases := map[string]bool{
		"":           true,
		"*":          true,
		"0.0.0.0":    true,
		"::":         true,
		"127.0.0.1":  false,
		"::1":        false,
		"192.168.1.10": true,
	}
	for in, want := range cases {
		if got := isPublicBind(in); got != want {
			t.Errorf("isPublicBind(%q) = %v，期望 %v", in, got, want)
		}
	}
}

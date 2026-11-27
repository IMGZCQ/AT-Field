package alert

import "testing"

// ValidateHook 是防止 SSRF / 误填的唯一入口：只放行 https + 三个厂商官方域名
func TestValidateHook(t *testing.T) {
	ok := []string{
		"",
		"   ",
		"https://open.feishu.cn/open-apis/bot/v2/hook/abc-123",
		"https://open.larksuite.com/open-apis/bot/v2/hook/abc",
		"https://open.larkoffice.com/open-apis/bot/v2/hook/abc",
		"https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=1",
		"https://oapi.dingtalk.com/robot/send?access_token=1",
	}
	for _, h := range ok {
		if err := ValidateHook(h); err != nil {
			t.Errorf("ValidateHook(%q) 应通过，实际报错：%v", h, err)
		}
	}
	bad := []string{
		"not-a-url",
		"open.feishu.cn/open-apis/bot/v2/hook/abc",   // 缺 scheme
		"http://open.feishu.cn/open-apis/bot/v2/hook", // 非 https
		"https://evil.example.com/hook",
		"https://127.0.0.1/hook",
		"https://192.168.1.10/hook",
		"https://open.feishu.cn.evil.com/hook", // 后缀伪装
	}
	for _, h := range bad {
		if err := ValidateHook(h); err == nil {
			t.Errorf("ValidateHook(%q) 应被拒绝，实际通过", h)
		}
	}
}

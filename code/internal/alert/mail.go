// 邮件推送：以用户自填的 SMTP 账号发送告警邮件，支持 SSL（465）、
// STARTTLS（587）与无加密（25，内网中继）三种方式。
package alert

import (
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"atforcefield/internal/store"
)

// MailReady 判断邮件推送是否已填写到可用状态（服务器与收件人都已填）
func MailReady(m store.MailConfig) bool {
	return strings.TrimSpace(m.Host) != "" && strings.TrimSpace(m.To) != ""
}

// ValidateMail 校验邮件推送配置，仅在 MailReady 后调用；未填完整时给出具体原因
func ValidateMail(m store.MailConfig) error {
	if strings.TrimSpace(m.Host) == "" {
		return errors.New("未填写 SMTP 服务器")
	}
	if m.Port <= 0 || m.Port > 65535 {
		return errors.New("SMTP 端口需在 1-65535 之间")
	}
	if _, err := mail.ParseAddress(addrOf(m.From, m.User)); err != nil {
		return errors.New("发件人地址不合法：" + err.Error())
	}
	if _, err := mail.ParseAddressList(strings.ReplaceAll(m.To, ";", ",")); err != nil {
		return errors.New("收件人地址不合法：" + err.Error())
	}
	if (strings.TrimSpace(m.User) == "") != (strings.TrimSpace(m.Pass) == "") {
		return errors.New("SMTP 账号与密码 / 授权码需同时填写")
	}
	switch m.TLS {
	case "ssl", "starttls", "none":
	default:
		return errors.New("加密方式无效，应为 ssl / starttls / none")
	}
	// net/smtp 的 PlainAuth 拒绝在明文连接上发送账号密码；内网中继通常无需认证
	if m.TLS == "none" && strings.TrimSpace(m.User) != "" {
		return errors.New("无加密（25）方式不支持账号认证，请改用 SSL / STARTTLS，或清空账号密码（内网中继）")
	}
	return nil
}

// sendMail 发送一封纯文本告警邮件；返回的错误已带上具体环节，便于前端直接展示。
func sendMail(m store.MailConfig, msg string) error {
	if err := ValidateMail(m); err != nil {
		return err
	}
	from, err := mail.ParseAddress(addrOf(m.From, m.User))
	if err != nil {
		return errors.New("发件人地址不合法：" + err.Error())
	}
	rcpts, err := mail.ParseAddressList(strings.ReplaceAll(m.To, ";", ","))
	if err != nil {
		return errors.New("收件人地址不合法：" + err.Error())
	}
	host := hostOnly(m.Host)
	addr := net.JoinHostPort(host, strconv.Itoa(m.Port))

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	if m.TLS == "ssl" {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("连接 SMTP 服务器失败：%v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(25 * time.Second))

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("SMTP 握手失败：%v", err)
	}
	defer c.Close()
	if m.TLS == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("服务器未提供 STARTTLS，请把加密方式改为 SSL（465）")
		}
		if err := c.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("STARTTLS 失败：%v", err)
		}
	}
	if u := strings.TrimSpace(m.User); u != "" {
		if err := c.Auth(smtp.PlainAuth("", u, m.Pass, host)); err != nil {
			return fmt.Errorf("SMTP 认证失败（请检查账号与密码 / 授权码）：%v", err)
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return fmt.Errorf("SMTP 发件人被拒绝：%v", err)
	}
	for _, r := range rcpts {
		if err := c.Rcpt(r.Address); err != nil {
			return fmt.Errorf("SMTP 收件人被拒绝：%v", err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("SMTP 正文发送失败：%v", err)
	}
	if _, err := w.Write(buildMail(from, rcpts, msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// addrOf 发件人留空时回落到登录账号
func addrOf(from, user string) string {
	if f := strings.TrimSpace(from); f != "" {
		return f
	}
	return strings.TrimSpace(user)
}

// hostOnly 容忍用户在「服务器」里连端口一起填：smtp.qq.com:465
func hostOnly(h string) string {
	h = strings.TrimSpace(h)
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}

// subjectOf 取消息首行作为标题，过长时截断（标题承载不了多行详情）
func subjectOf(msg string) string {
	s := strings.TrimSpace(strings.SplitN(strings.TrimSpace(msg), "\n", 2)[0])
	r := []rune(s)
	if len(r) > 60 {
		return string(r[:60]) + "…"
	}
	return s
}

func buildMail(from *mail.Address, to []*mail.Address, msg string) []byte {
	toList := make([]string, len(to))
	for i, a := range to {
		toList[i] = a.String()
	}
	body := strings.TrimSpace(msg) + "\r\n\r\n--\r\n飞牛AT力场 · 系统告警\r\n时间：" +
		time.Now().Format("2006-01-02 15:04:05") + "\r\n（本邮件由系统自动发送，请勿直接回复）\r\n"

	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from.String())
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(toList, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", encodeHeader("【飞牛AT力场】"+subjectOf(msg)))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	enc := base64.StdEncoding.EncodeToString([]byte(body))
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return []byte(b.String())
}

// encodeHeader 非 ASCII 标题按 RFC 2047 编码，避免邮件客户端显示乱码
func encodeHeader(s string) string {
	for _, r := range s {
		if r > 126 {
			return mime.QEncoding.Encode("UTF-8", s)
		}
	}
	return s
}

// Package mail provides a small SMTP sender that reads its configuration
// from admin settings (live-reloaded on each send — no restart required).
//
// The HTML template is inline to keep the deployment to a single binary;
// it uses the Aivory brand palette via CSS variables that we compile to
// literal values at render time so the HTML works in any email client.
package mail

import (
	"bytes"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/smtp"
	"strings"
	"time"

	"aivory/server/internal/store"
)

// Sender is the surface the auth handlers use. Implementations must be safe
// for concurrent use.
type Sender interface {
	SendCode(to, code, purpose string) error
}

// smtpConfig is the resolved SMTP config read from admin settings.
type smtpConfig struct {
	Host     string
	Port     string
	User     string
	Password string
	From     string
	TLS      bool
}

// SMTPSender reads SMTP config from admin settings on every send so changes
// in the admin UI take effect immediately without a restart.
type SMTPSender struct {
	DB     *sql.DB
	Logger *log.Logger
}

// NewSMTPSender creates a new sender backed by admin-configurable SMTP.
func NewSMTPSender(db *sql.DB, logger *log.Logger) *SMTPSender {
	return &SMTPSender{DB: db, Logger: logger}
}

func (s *SMTPSender) loadConfig() (smtpConfig, error) {
	var cfg smtpConfig
	readStr := func(key string) string {
		raw, err := store.GetSetting(s.DB, key)
		if err != nil {
			return ""
		}
		var v string
		_ = json.Unmarshal(raw, &v)
		return v
	}
	readBool := func(key string) bool {
		raw, err := store.GetSetting(s.DB, key)
		if err != nil {
			return false
		}
		var v bool
		_ = json.Unmarshal(raw, &v)
		return v
	}
	cfg.Host = readStr("smtp_host")
	cfg.Port = readStr("smtp_port")
	cfg.User = readStr("smtp_user")
	cfg.Password = readStr("smtp_password")
	cfg.From = readStr("smtp_from")
	cfg.TLS = readBool("smtp_tls")
	if cfg.Host == "" {
		return cfg, fmt.Errorf("smtp_host is not configured")
	}
	if cfg.Port == "" {
		cfg.Port = "587"
	}
	if cfg.From == "" {
		cfg.From = cfg.User
	}
	return cfg, nil
}

// SendCode sends a branded HTML email with a 6-digit verification code.
// purpose is one of "verify" | "reset" — controls the subject line and body text.
func (s *SMTPSender) SendCode(to, code, purpose string) error {
	cfg, err := s.loadConfig()
	if err != nil {
		s.Logger.Printf("[mail] SMTP not configured, logging code: %s → %s code=%s", purpose, to, code)
		return nil // graceful fallback: don't block registration
	}

	subject := "Your verification code"
	heading := "Verify your email"
	body := "Use the code below to verify your email address. It expires in 10 minutes."
	if purpose == "reset" {
		subject = "Password reset code"
		heading = "Reset your password"
		body = "Use the code below to reset your password. It expires in 10 minutes."
	}

	html, err := renderEmail(emailData{
		Heading: heading,
		Body:    body,
		Code:    code,
		Year:    time.Now().Year(),
	})
	if err != nil {
		return fmt.Errorf("render email: %w", err)
	}

	return s.send(cfg, to, subject, html)
}

func (s *SMTPSender) send(cfg smtpConfig, to, subject, htmlBody string) error {
	addr := net.JoinHostPort(cfg.Host, cfg.Port)

	// Build the raw MIME message.
	var msg bytes.Buffer
	msg.WriteString("From: " + cfg.From + "\r\n")
	msg.WriteString("To: " + to + "\r\n")
	msg.WriteString("Subject: " + subject + "\r\n")
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	msg.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(htmlBody)

	auth := smtp.PlainAuth("", cfg.User, cfg.Password, cfg.Host)
	tlsCfg := &tls.Config{ServerName: cfg.Host}

	// Bound EVERY network step. The standard library's tls.Dial / smtp.SendMail
	// have NO timeout, so a wrong port or a TLS-mode mismatch (587 expects
	// STARTTLS, 465 expects implicit TLS) hangs forever — which froze the
	// register request on an infinite spinner. A 10s dial + a 25s overall
	// deadline make a misconfigured server fail fast instead.
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	deadline := time.Now().Add(25 * time.Second)

	var c *smtp.Client
	if cfg.TLS || cfg.Port == "465" {
		// Implicit TLS (port 465): the connection is TLS from the first byte.
		conn, err := tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
		if err != nil {
			return fmt.Errorf("tls dial: %w", err)
		}
		_ = conn.SetDeadline(deadline)
		c, err = smtp.NewClient(conn, cfg.Host)
		if err != nil {
			_ = conn.Close()
			return fmt.Errorf("smtp client: %w", err)
		}
	} else {
		// Plain connect, then upgrade with STARTTLS when the server offers it
		// (port 587 / 25). Auto-detect so the common 587 setup just works.
		conn, err := dialer.Dial("tcp", addr)
		if err != nil {
			return fmt.Errorf("dial: %w", err)
		}
		_ = conn.SetDeadline(deadline)
		c, err = smtp.NewClient(conn, cfg.Host)
		if err != nil {
			_ = conn.Close()
			return fmt.Errorf("smtp client: %w", err)
		}
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsCfg); err != nil {
				_ = c.Close()
				return fmt.Errorf("starttls: %w", err)
			}
		}
	}
	defer c.Close()

	// Authenticate only when credentials are set AND the server advertises AUTH.
	if cfg.User != "" {
		if ok, _ := c.Extension("AUTH"); ok {
			if err := c.Auth(auth); err != nil {
				return fmt.Errorf("smtp auth: %w", err)
			}
		}
	}
	if err := c.Mail(cfg.From); err != nil {
		return fmt.Errorf("smtp mail: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(msg.Bytes()); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp write close: %w", err)
	}
	return c.Quit()
}

type emailData struct {
	Heading string
	Body    string
	Code    string
	Year    int
}

var emailTmpl = template.Must(template.New("email").Parse(`<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1.0"></head>
<body style="margin:0;padding:0;background-color:#F6F5FA;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background-color:#F6F5FA;padding:40px 20px;">
<tr><td align="center">
<table role="presentation" width="460" cellpadding="0" cellspacing="0" style="background-color:#FFFFFF;border-radius:18px;border:1px solid #E4E2EC;overflow:hidden;">
  <!-- Header -->
  <tr><td style="padding:32px 36px 20px;text-align:center;">
    <div style="display:inline-block;width:40px;height:40px;background:linear-gradient(135deg,#6A4DE6,#4A9B7F);border-radius:10px;margin-bottom:16px;"></div>
    <h1 style="margin:0;font-size:22px;font-weight:600;color:#1B1830;letter-spacing:-0.02em;">{{.Heading}}</h1>
    <p style="margin:12px 0 0;font-size:14px;color:#5F5677;line-height:1.6;">{{.Body}}</p>
  </td></tr>
  <!-- Code -->
  <tr><td style="padding:8px 36px 28px;text-align:center;">
    <div style="display:inline-block;background-color:#F6F5FA;border:2px solid #E4E2EC;border-radius:14px;padding:18px 40px;letter-spacing:0.35em;font-size:32px;font-weight:700;color:#1B1830;font-family:'SF Mono',Monaco,Consolas,monospace;">
      {{.Code}}
    </div>
    <p style="margin:16px 0 0;font-size:12px;color:#8C87A0;">This code expires in 10 minutes. Do not share it with anyone.</p>
  </td></tr>
  <!-- Divider -->
  <tr><td style="padding:0 36px;"><div style="height:1px;background-color:#E4E2EC;"></div></td></tr>
  <!-- Footer -->
  <tr><td style="padding:20px 36px;text-align:center;">
    <p style="margin:0;font-size:11px;color:#8C87A0;">
      &copy; {{.Year}} Aivory &middot; You received this because someone used your email to sign up or reset a password.
    </p>
  </td></tr>
</table>
</td></tr>
</table>
</body>
</html>`))

func renderEmail(data emailData) (string, error) {
	var buf bytes.Buffer
	if err := emailTmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// CheckDomainWhitelist validates that the email's domain is in the admin-configured
// whitelist. An empty whitelist means all domains are allowed.
func CheckDomainWhitelist(db *sql.DB, email string) error {
	raw, err := store.GetSetting(db, "email_domain_whitelist")
	if err != nil {
		return nil // no setting = allow all
	}
	var whitelist string
	_ = json.Unmarshal(raw, &whitelist)
	whitelist = strings.TrimSpace(whitelist)
	if whitelist == "" {
		return nil // empty = allow all
	}

	parts := strings.SplitN(email, "@", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid email")
	}
	domain := strings.ToLower(parts[1])

	for _, raw := range strings.FieldsFunc(whitelist, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n'
	}) {
		allowed := strings.ToLower(strings.TrimSpace(raw))
		allowed = strings.TrimPrefix(allowed, "@")
		if allowed == domain {
			return nil
		}
	}
	return fmt.Errorf("email domain @%s is not allowed", domain)
}

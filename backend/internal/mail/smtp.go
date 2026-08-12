package mail

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"mime/multipart"
	"net"
	"net/smtp"
	"net/textproto"
	"time"
)

// smtpDialTimeout bounds the TCP connect when the context carries no deadline.
const smtpDialTimeout = 15 * time.Second

// SMTPMailer delivers via a real SMTP server (net/smtp). It is selected in
// main.go when SMTP_HOST is set. Credentials live only in this struct and are
// passed straight to smtp.Auth — they are never logged.
type SMTPMailer struct {
	Host     string
	Port     string
	User     string
	Pass     string
	From     string
	StartTLS bool
}

// Send opens a connection, optionally upgrades to STARTTLS, authenticates (when
// a username is set), and delivers the message as a multipart/alternative body
// (text + HTML) or a single text part when HTMLBody is empty. It honors ctx for
// the dial deadline.
func (m SMTPMailer) Send(ctx context.Context, msg Message) error {
	addr := net.JoinHostPort(m.Host, m.Port)

	d := net.Dialer{Timeout: smtpDialTimeout}
	if dl, ok := ctx.Deadline(); ok {
		d.Deadline = dl
	}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("mail: dial %s: %w", addr, err)
	}

	c, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("mail: smtp client: %w", err)
	}
	defer func() { _ = c.Close() }()

	if m.StartTLS {
		ok, _ := c.Extension("STARTTLS")
		if !ok {
			return fmt.Errorf("mail: server %s does not advertise STARTTLS", addr)
		}
		if err := c.StartTLS(&tls.Config{ServerName: m.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("mail: starttls: %w", err)
		}
	}

	if m.User != "" {
		if ok, _ := c.Extension("AUTH"); ok {
			if err := c.Auth(smtp.PlainAuth("", m.User, m.Pass, m.Host)); err != nil {
				return fmt.Errorf("mail: auth: %w", err)
			}
		}
	}

	if err := c.Mail(m.From); err != nil {
		return fmt.Errorf("mail: MAIL FROM: %w", err)
	}
	if err := c.Rcpt(msg.To); err != nil {
		return fmt.Errorf("mail: RCPT TO: %w", err)
	}
	wc, err := c.Data()
	if err != nil {
		return fmt.Errorf("mail: DATA: %w", err)
	}
	if _, err := wc.Write(m.render(msg)); err != nil {
		_ = wc.Close()
		return fmt.Errorf("mail: write body: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("mail: close body: %w", err)
	}
	return c.Quit()
}

// render builds the RFC 5322 message. When HTMLBody is present it emits a
// multipart/alternative body so clients pick their preferred representation.
func (m SMTPMailer) render(msg Message) []byte {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: %s\r\n", m.From)
	fmt.Fprintf(&buf, "To: %s\r\n", msg.To)
	fmt.Fprintf(&buf, "Subject: %s\r\n", msg.Subject)
	buf.WriteString("MIME-Version: 1.0\r\n")

	if msg.HTMLBody == "" {
		buf.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
		buf.WriteString(msg.TextBody)
		return buf.Bytes()
	}

	mw := multipart.NewWriter(&buf)
	fmt.Fprintf(&buf, "Content-Type: multipart/alternative; boundary=%s\r\n\r\n", mw.Boundary())

	textPart, _ := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/plain; charset=UTF-8"}})
	_, _ = textPart.Write([]byte(msg.TextBody))
	htmlPart, _ := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/html; charset=UTF-8"}})
	_, _ = htmlPart.Write([]byte(msg.HTMLBody))
	_ = mw.Close()
	return buf.Bytes()
}

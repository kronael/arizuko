package main

import (
	"crypto/tls"
	"fmt"
	"net/smtp"
	"strings"
	"time"
)

func sanitizeHeader(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	return s
}

// smtpSender is the send primitive swapped out in tests. Signature mirrors
// smtp.SendMail: addr, auth, from, to[], msg. Production callers get the
// full STARTTLS + AUTH + MAIL/RCPT/DATA dance; tests record args.
var smtpSender = defaultSMTPSender

// defaultSMTPSender drives net/smtp's client explicitly so we can require
// STARTTLS before auth. smtp.SendMail does this too but this adapter
// historically kept the state machine visible, so we preserve it.
func defaultSMTPSender(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	defer c.Quit()

	host := addr
	if i := strings.LastIndex(addr, ":"); i > 0 {
		host = addr[:i]
	}
	if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
		return fmt.Errorf("starttls: %w", err)
	}
	if err := c.Auth(a); err != nil {
		return fmt.Errorf("smtp auth: %w", err)
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("smtp MAIL: %w", err)
	}
	for _, r := range to {
		if err := c.Rcpt(r); err != nil {
			return fmt.Errorf("smtp RCPT: %w", err)
		}
	}
	wc, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := wc.Write(msg); err != nil {
		return fmt.Errorf("smtp DATA write: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("smtp DATA close: %w", err)
	}
	return nil
}

func sendReply(cfg config, to, rootMsgID, text string) error {
	// Sanitize both envelope addresses symmetrically — net/smtp validates
	// newlines, but defense-in-depth against header injection in case the
	// validation is ever loosened.
	fromAddr := sanitizeHeader(cfg.Account)
	toAddr := sanitizeHeader(to)

	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: Re: (arizuko)\r\nDate: %s\r\n",
		fromAddr, toAddr, time.Now().Format(time.RFC1123Z))
	if rootMsgID != "" {
		ref := "<" + sanitizeHeader(strings.Trim(rootMsgID, "<>")) + ">"
		fmt.Fprintf(&b, "In-Reply-To: %s\r\nReferences: %s\r\n", ref, ref)
	}
	fmt.Fprintf(&b, "Content-Type: text/plain; charset=utf-8\r\n\r\n%s", text)

	auth := smtp.PlainAuth("", cfg.Account, cfg.Password, cfg.SMTPHost)
	addr := cfg.SMTPHost + ":" + cfg.SMTPPort
	return smtpSender(addr, auth, fromAddr, []string{toAddr}, []byte(b.String()))
}

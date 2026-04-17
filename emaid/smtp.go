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

func sendReply(cfg config, to, rootMsgID, text string) error {
	addr := cfg.SMTPHost + ":" + cfg.SMTPPort
	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	defer c.Quit()

	if err := c.StartTLS(&tls.Config{ServerName: cfg.SMTPHost}); err != nil {
		return fmt.Errorf("starttls: %w", err)
	}

	auth := smtp.PlainAuth("", cfg.Account, cfg.Password, cfg.SMTPHost)
	if err := c.Auth(auth); err != nil {
		return fmt.Errorf("smtp auth: %w", err)
	}

	// Sanitize both envelope addresses symmetrically — net/smtp validates
	// newlines, but defense-in-depth against header injection in case the
	// validation is ever loosened.
	fromAddr := sanitizeHeader(cfg.Account)
	toAddr := sanitizeHeader(to)
	if err := c.Mail(fromAddr); err != nil {
		return fmt.Errorf("smtp MAIL: %w", err)
	}
	if err := c.Rcpt(toAddr); err != nil {
		return fmt.Errorf("smtp RCPT: %w", err)
	}

	wc, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}

	fmt.Fprintf(wc, "From: %s\r\nTo: %s\r\nSubject: Re: (arizuko)\r\nDate: %s\r\n",
		fromAddr, toAddr, time.Now().Format(time.RFC1123Z))
	if rootMsgID != "" {
		ref := "<" + sanitizeHeader(strings.Trim(rootMsgID, "<>")) + ">"
		fmt.Fprintf(wc, "In-Reply-To: %s\r\nReferences: %s\r\n", ref, ref)
	}
	fmt.Fprintf(wc, "Content-Type: text/plain; charset=utf-8\r\n\r\n%s", text)
	// Capture the Close error — the server's accept/reject reply to the
	// DATA payload arrives here. defer would silently drop the error and
	// claim success even when the mail was rejected at end-of-data.
	if err := wc.Close(); err != nil {
		return fmt.Errorf("smtp DATA close: %w", err)
	}
	return nil
}

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

	if err := c.Mail(cfg.Account); err != nil {
		return fmt.Errorf("smtp MAIL: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp RCPT: %w", err)
	}

	wc, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	defer wc.Close()

	msgIDRef := ""
	if rootMsgID != "" {
		msgIDRef = "<" + sanitizeHeader(strings.Trim(rootMsgID, "<>")) + ">"
	}
	date := time.Now().Format(time.RFC1123Z)
	safeTo := sanitizeHeader(to)

	fmt.Fprintf(wc, "From: %s\r\nTo: %s\r\nSubject: Re: (arizuko)\r\nDate: %s\r\n", cfg.Account, safeTo, date)
	if msgIDRef != "" {
		fmt.Fprintf(wc, "In-Reply-To: %s\r\nReferences: %s\r\n", msgIDRef, msgIDRef)
	}
	fmt.Fprintf(wc, "Content-Type: text/plain; charset=utf-8\r\n\r\n%s", text)
	return nil
}

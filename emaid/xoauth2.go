package main

import (
	"errors"
	"net/smtp"
)

// The XOAUTH2 SASL mechanism, as Google defines it:
// https://developers.google.com/workspace/gmail/imap/xoauth2-protocol
//
//	base64("user=" {User} "^Aauth=Bearer " {Access Token} "^A^A")   (^A = 0x01)
//
// The same initial response serves IMAP (AUTHENTICATE XOAUTH2) and SMTP
// (AUTH XOAUTH2). On failure the server sends a base64 JSON challenge and the
// client "must send an empty response" so the server can finish with its
// final NO / 535. go-sasl has no XOAUTH2 client, so this is the whole of it.
//
// Each login trades EMAIL_REFRESH_TOKEN for a fresh access token through the
// surrogate Google provider. A revoked refresh token stops the poller like a
// refused password. Neither token nor the response is ever logged.

// xoauth2Response is the raw initial response, before the transport's base64.
func xoauth2Response(user, token string) []byte {
	return []byte("user=" + user + "\x01auth=Bearer " + token + "\x01\x01")
}

// xoauth2IMAP is a sasl.Client for imapclient.Authenticate.
type xoauth2IMAP struct{ user, token string }

func (a xoauth2IMAP) Start() (string, []byte, error) {
	return "XOAUTH2", xoauth2Response(a.user, a.token), nil
}

// Next answers the server's error challenge with the empty response the
// mechanism requires; the server then ends the exchange with a refusal.
func (a xoauth2IMAP) Next([]byte) ([]byte, error) { return []byte{}, nil }

// xoauth2SMTP is a net/smtp.Auth.
type xoauth2SMTP struct{ user, token string }

// Start refuses a connection that is not yet TLS, as smtp.PlainAuth does:
// the token must not cross the wire in clear.
func (a xoauth2SMTP) Start(s *smtp.ServerInfo) (string, []byte, error) {
	if !s.TLS {
		return "", nil, errors.New("xoauth2: refusing to send the access token over an unencrypted connection")
	}
	return "XOAUTH2", xoauth2Response(a.user, a.token), nil
}

func (a xoauth2SMTP) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return []byte{}, nil
	}
	return nil, nil
}

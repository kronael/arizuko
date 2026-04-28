package api

import (
	"testing"
	"time"
)

func TestDeliverMessage_ConsumesLinkCode(t *testing.T) {
	srv, reg, s := setup(t)
	h := srv.Handler()
	token, _ := reg.Register("tg", "http://tg:9001", []string{"tg:"}, nil)

	idn, err := s.CreateIdentity("alice")
	if err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}
	code, err := s.MintLinkCode(idn.ID, 10*time.Minute)
	if err != nil {
		t.Fatalf("MintLinkCode: %v", err)
	}

	w := postJSON(h, "/v1/messages", messageReq{
		ChatJID: "tg:123", Sender: "tg:456", Content: code,
	}, token)
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	got, subs, ok := s.GetIdentityForSub("tg:456")
	if !ok {
		t.Fatal("sender not bound to identity")
	}
	if got.ID != idn.ID {
		t.Errorf("identity mismatch: %s vs %s", got.ID, idn.ID)
	}
	if len(subs) != 1 || subs[0] != "tg:456" {
		t.Errorf("subs = %v, want [tg:456]", subs)
	}
}

func TestDeliverMessage_NonCodeContentIgnored(t *testing.T) {
	srv, reg, s := setup(t)
	h := srv.Handler()
	token, _ := reg.Register("tg", "http://tg:9001", []string{"tg:"}, nil)

	w := postJSON(h, "/v1/messages", messageReq{
		ChatJID: "tg:123", Sender: "tg:456",
		Content: "hello world link-deadbeef0000",
	}, token)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if _, _, ok := s.GetIdentityForSub("tg:456"); ok {
		t.Error("non-bare-code content should not bind sender")
	}
}

func TestDeliverMessage_StaleCodeIgnored(t *testing.T) {
	srv, reg, s := setup(t)
	h := srv.Handler()
	token, _ := reg.Register("tg", "http://tg:9001", []string{"tg:"}, nil)

	idn, _ := s.CreateIdentity("alice")
	code, _ := s.MintLinkCode(idn.ID, -1*time.Second) // already expired

	w := postJSON(h, "/v1/messages", messageReq{
		ChatJID: "tg:123", Sender: "tg:456", Content: code,
	}, token)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if _, _, ok := s.GetIdentityForSub("tg:456"); ok {
		t.Error("expired code should not bind sender")
	}
}

package spec

import (
	"strings"
	"testing"
)

// TestCatalogRouteMutationHygiene pins the invariant a live run depends on: no
// case may leave the eval chat unable to wake the agent. A leaked #observe rule
// on a route-table-resolved eval chat stores every later inject as silent
// context and fires no turn, so every case ordered after it times out as a fake
// agent failure (found 2026-08-20 on route-observe-silent, which configured
// "THIS chat" observe-only and never reverted). A prompt is natural language,
// so the pin is textual, on the only artifact a case has:
//
//  1. a prompt that instructs a route mutation must also instruct delete_route
//     — the table is left as found when the agent completes;
//  2. every chat_jid= it names must embed {nonce} — the rule matches a
//     case-owned throwaway chat, so even an agent that dies before the delete
//     cannot silence the shared eval chat;
//  3. it must not aim the mutation at the eval chat ("this chat" / {chat}).
func TestCatalogRouteMutationHygiene(t *testing.T) {
	cases, err := Load("../../cases")
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no cases loaded")
	}
	seen := false
	for _, c := range cases {
		p := c.Prompt
		if !strings.Contains(p, "add_route") && !strings.Contains(p, "set_routes") {
			continue
		}
		seen = true
		if !strings.Contains(p, "delete_route") {
			t.Errorf("case %s: instructs a route mutation but never delete_route — a leaked rule outlives the run", c.ID)
		}
		rest := p
		for {
			i := strings.Index(rest, "chat_jid=")
			if i < 0 {
				break
			}
			rest = rest[i+len("chat_jid="):]
			jid, _, _ := strings.Cut(rest, " ")
			if !strings.Contains(jid, "{nonce}") {
				t.Errorf("case %s: route rule names chat_jid=%s without {nonce} — not a case-owned throwaway chat", c.ID, jid)
			}
		}
		if strings.Contains(strings.ToLower(p), "this chat") || strings.Contains(p, "{chat}") {
			t.Errorf("case %s: aims a route mutation at the eval chat — a leaked observe rule silences every later case", c.ID)
		}
	}
	if !seen {
		t.Fatal("no route-mutation case found — route-observe-silent gone? update or delete this test with it")
	}
}

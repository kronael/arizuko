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

// A prompt may not OPEN with a slash command. routd's command handler consumes
// the command and re-injects the trailing text under a fresh message id, so the
// id anteval is waiting on never runs a turn — the case then burns its whole
// settle wait and reports a timeout that names the agent (BUGS L164).
//
// Textual, like the pin above, because a prompt is natural language and the id
// it will be injected under is the only thing anteval can hold.
func TestNoCaseOpensWithASlashCommand(t *testing.T) {
	cases, err := Load("../../cases")
	if err != nil {
		t.Fatalf("load the shipped cases: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("no cases loaded; this test checks nothing")
	}
	for _, c := range cases {
		if strings.HasPrefix(strings.TrimSpace(c.Prompt), "/") {
			t.Errorf("case %q opens with a slash command; anteval waits on an id that never runs a turn", c.ID)
		}
	}
}

// A skill outlives the case that built it — nothing tears ~/.claude/skills down
// between cases — so a case that has the agent BUILD one must not bake its
// nonce into it. A later case then invokes the stale skill and calls back with
// the wrong nonce, which reads as that case failing (BUGS L153).
func TestASkillBuildingCaseBakesNoNonce(t *testing.T) {
	cases, err := Load("../../cases")
	if err != nil {
		t.Fatalf("load the shipped cases: %v", err)
	}
	checked := 0
	for _, c := range cases {
		if !strings.Contains(c.Prompt, ".claude/skills") {
			continue
		}
		checked++
		if !strings.Contains(c.Prompt, "argument") {
			t.Errorf("case %q builds a skill but does not say it takes the token as an argument", c.ID)
		}
		// The nonce may be named when INVOKING the skill, never inside the
		// command the skill is asked to contain.
		if strings.Contains(c.Prompt, "runs `curl -s {sink}/cb/{nonce}`") {
			t.Errorf("case %q bakes {nonce} into the skill it builds", c.ID)
		}
	}
	if checked == 0 {
		t.Fatal("no skill-building case found; this test checks nothing")
	}
}

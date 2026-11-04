package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/kronael/arizuko/store"
)

// CI output naming the bot asks for ONE turn: on the PR it belongs to, not
// also on the repository row beside it. With no PR it asks on the repository row.
func TestCIOutputMentionStartsOneTurn(t *testing.T) {
	e := newTestEnv(t, secretMap{"repo/kronael/arizuko": "fixture-secret"})
	e.srv.bot.login.Store(botLogin)
	for _, scenario := range []struct {
		name    string
		text    string
		numbers []int
		want    []string
	}{
		{"mention with a PR", "@arizuko-bot look", []int{5}, []string{"check_run", "mention"}},
		{"mention without a PR", "@arizuko-bot look", nil, []string{"mention"}},
		{"no mention with a PR", "3 failed", []int{5}, []string{"check_run", "check_run"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			e.resetDeliveries()
			var payload map[string]any
			if err := json.Unmarshal(ciBody(t, "check_run", "completed", "completed", scenario.numbers), &payload); err != nil {
				t.Fatal(err)
			}
			payload["check_run"].(map[string]any)["output"] = map[string]string{"summary": scenario.text}
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			got := e.deliver(t, "/github/repo/kronael/arizuko", "fixture-secret", "check_run", "ci-mention", raw)
			var verbs []string
			for _, m := range e.routd.messages() {
				verbs = append(verbs, m.Verb)
			}
			if got.Code != 200 || !slices.Equal(verbs, scenario.want) {
				t.Fatalf("status=%d verbs=%v, want %v", got.Code, verbs, scenario.want)
			}
		})
	}
}

// The bot's own text never asks, even when it names itself.
func TestOwnTextNamingTheBotIsNotAMention(t *testing.T) {
	for _, scenario := range []struct{ name, event, fields, verb, drop string }{
		{"issue comment", "issue_comment", `"action":"created","issue":{"number":5},"comment":{"id":1,"body":"@arizuko-bot"}`, "", "self"},
		{"discussion comment", "discussion_comment", `"action":"created","discussion":{"title":"t"},"comment":{"id":1,"body":"@arizuko-bot"}`, "", "self"},
		{"check run", "check_run", `"action":"completed","check_run":{"status":"completed","output":{"text":"@arizuko-bot"}}`, "check_run", ""},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			raw := strings.Replace(string(body(scenario.fields)), `"login":"rhias","type":"User"`, `"login":"arizuko-bot","type":"User"`, 1)
			msg, drop, err := mapEvent(store.GitHubSourceRepo, "kronael/arizuko", scenario.event, "d", []byte(raw), fixedNow, botLogin)
			if err != nil || drop != scenario.drop || msg.Verb != scenario.verb {
				t.Fatalf("verb=%q drop=%q err=%v, want verb=%q drop=%q", msg.Verb, drop, err, scenario.verb, scenario.drop)
			}
		})
	}
}

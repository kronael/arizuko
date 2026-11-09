package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

func TestTurnStatusCommentLifecycle(t *testing.T) {
	for _, inline := range []bool{false, true} {
		t.Run(map[bool]string{false: "issue", true: "review"}[inline], func(t *testing.T) {
			post := "POST /repos/kronael/arizuko/issues/12/comments"
			path := "/repos/kronael/arizuko/issues/comments/777"
			replyTo := ""
			responses := map[string]string{}
			want := []string{}
			if inline {
				replyTo = "gh-rc100"
				lookup := "GET /repos/kronael/arizuko/pulls/comments/100"
				responses[lookup] = `{"id":100}`
				want = append(want, lookup)
				post = "POST /repos/kronael/arizuko/pulls/12/comments/100/replies"
				path = "/repos/kronael/arizuko/pulls/comments/777"
			}
			responses[post] = `{"id":777}`
			responses["PATCH "+path] = `{}`
			responses["DELETE "+path] = ""
			gh, seen := fakeGitHubAPI(t, responses)
			b := testBot(config{Token: "ghp_test", APIBase: gh.URL})
			jid := "github:kronael/arizuko/pr/12"
			id, err := b.Send(chanlib.SendRequest{ChatJID: jid, ReplyTo: replyTo, Content: "Server storage is full."})
			if err != nil {
				t.Fatal(err)
			}
			if err := b.Edit(chanlib.EditRequest{ChatJID: jid, TargetID: id, Content: "Retry 1/3 is running."}); err != nil {
				t.Fatal(err)
			}
			if err := b.Delete(chanlib.DeleteRequest{ChatJID: jid, TargetID: id}); err != nil {
				t.Fatal(err)
			}
			want = append(want, post, "PATCH "+path, "DELETE "+path)
			if !slices.Equal(*seen, want) {
				t.Fatalf("lifecycle requests = %v, want %v", *seen, want)
			}
		})
	}
}

func TestEditComment(t *testing.T) {
	for _, tc := range []struct{ jid, id, path string }{
		{"github:kronael/arizuko/issue/12", "gh-c777", "/repos/kronael/arizuko/issues/comments/777"},
		{"github:kronael/arizuko/pr/12", "gh-c777", "/repos/kronael/arizuko/issues/comments/777"},
		{"github:kronael/arizuko/pr/12", "gh-rc888", "/repos/kronael/arizuko/pulls/comments/888"},
	} {
		t.Run(tc.jid+"/"+tc.id, func(t *testing.T) {
			gh, reqs, bodies := fakeGitHub(t, http.StatusOK, `{}`)
			b := testBot(config{Token: "ghp_test", APIBase: gh.URL})
			if err := b.Edit(chanlib.EditRequest{ChatJID: tc.jid, TargetID: tc.id, Content: "Retry 1/3 is running."}); err != nil {
				t.Fatal(err)
			}
			if len(*reqs) != 1 || (*reqs)[0].Method != http.MethodPatch || (*reqs)[0].URL.Path != tc.path || (*reqs)[0].Header.Get("Authorization") != "Bearer ghp_test" {
				t.Fatalf("requests = %v", *reqs)
			}
			var body map[string]string
			if err := json.Unmarshal([]byte((*bodies)[0]), &body); err != nil || len(body) != 1 || body["body"] != "Retry 1/3 is running." {
				t.Fatalf("body = %v, error = %v", body, err)
			}
		})
	}
}

func TestDeleteComment(t *testing.T) {
	for _, tc := range []struct{ jid, id, path string }{
		{"github:kronael/arizuko/issue/12", "gh-c777", "/repos/kronael/arizuko/issues/comments/777"},
		{"github:kronael/arizuko/pr/12", "gh-c777", "/repos/kronael/arizuko/issues/comments/777"},
		{"github:kronael/arizuko/pr/12", "gh-rc888", "/repos/kronael/arizuko/pulls/comments/888"},
	} {
		t.Run(tc.jid+"/"+tc.id, func(t *testing.T) {
			gh, reqs, bodies := fakeGitHub(t, http.StatusNoContent, "")
			b := testBot(config{Token: "ghp_test", APIBase: gh.URL})
			if err := b.Delete(chanlib.DeleteRequest{ChatJID: tc.jid, TargetID: tc.id}); err != nil {
				t.Fatal(err)
			}
			if len(*reqs) != 1 || (*reqs)[0].Method != http.MethodDelete || (*reqs)[0].URL.Path != tc.path || (*reqs)[0].Header.Get("Authorization") != "Bearer ghp_test" || (*bodies)[0] != "" {
				t.Fatalf("requests = %v, bodies = %v", *reqs, *bodies)
			}
		})
	}
}

func TestCommentOperationRefusals(t *testing.T) {
	for _, tc := range []struct {
		status       int
		edit, delete error
	}{
		{403, chanlib.ErrPermanent, chanlib.ErrPermanent},
		{404, chanlib.ErrPermanent, nil},
		{429, chanlib.ErrNotDelivered, chanlib.ErrNotDelivered},
		{503, chanlib.ErrNotDelivered, chanlib.ErrNotDelivered},
	} {
		gh, reqs, _ := fakeGitHub(t, tc.status, `{}`)
		b := testBot(config{Token: "ghp_test", APIBase: gh.URL})
		if err := b.Edit(chanlib.EditRequest{ChatJID: "github:kronael/arizuko/pr/12", TargetID: "gh-c777", Content: "retry"}); !errors.Is(err, tc.edit) {
			t.Errorf("edit status %d: %v", tc.status, err)
		}
		if err := b.Delete(chanlib.DeleteRequest{ChatJID: "github:kronael/arizuko/pr/12", TargetID: "gh-c777"}); !errors.Is(err, tc.delete) {
			t.Errorf("delete status %d: %v", tc.status, err)
		}
		if len(*reqs) != 2 {
			t.Fatalf("refused operation posted again: %v", *reqs)
		}
	}
}

func TestCommentOperationInvalidTarget(t *testing.T) {
	gh, reqs, _ := fakeGitHub(t, 200, `{}`)
	b := testBot(config{Token: "ghp_test", APIBase: gh.URL})
	for _, tc := range []struct{ jid, id string }{
		{"github:kronael/arizuko", "gh-c777"},
		{"slack:channel", "gh-c777"},
		{"github:kronael/arizuko/issue/12", "gh-rc888"},
		{"github:kronael/arizuko/pr/12", "gh-r777"},
		{"github:kronael/arizuko/pr/12", "777"},
	} {
		if err := b.Edit(chanlib.EditRequest{ChatJID: tc.jid, TargetID: tc.id, Content: "retry"}); !errors.Is(err, chanlib.ErrInvalidRequest) {
			t.Errorf("edit %+v: %v", tc, err)
		}
		if err := b.Delete(chanlib.DeleteRequest{ChatJID: tc.jid, TargetID: tc.id}); !errors.Is(err, chanlib.ErrInvalidRequest) {
			t.Errorf("delete %+v: %v", tc, err)
		}
	}
	if len(*reqs) != 0 {
		t.Fatalf("invalid targets reached github: %v", *reqs)
	}
}

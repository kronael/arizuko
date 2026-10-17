package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/kronael/arizuko/chanlib"
)

// typingReaction is the reaction gitd sets as its typing indicator.
const typingReaction = "eyes"

// liveReaction is one 👀 gitd has set and not yet removed. GitHub deletes a
// reaction by its own id, so the id the create call returned is kept beside
// the comment's reactions path.
type liveReaction struct {
	path  string
	id    int64
	timer *time.Timer
}

// typingState is the indicator's whole memory, in process: the comment that
// last could start a turn per chat, and the 👀 currently standing per chat.
// Typing carries only the chat JID, which names no comment, so ingest records
// the comment and Typing(on) pins it.
type typingState struct {
	mu      sync.Mutex
	trigger map[string]string
	live    map[string]*liveReaction
}

func newTypingState() typingState {
	return typingState{trigger: map[string]string{}, live: map[string]*liveReaction{}}
}

// reactionsPath answers the reactions collection of the comment msgID names.
// The two comment kinds live behind different endpoints and id spaces, so the
// id prefix picks the path. A review, an issue body and a repo event carry no
// reactable comment id: ok=false.
func reactionsPath(j chanlib.GitHubJID, msgID string) (string, bool) {
	if id, ok := reviewCommentID(msgID); ok {
		return fmt.Sprintf("/repos/%s/%s/pulls/comments/%d/reactions", j.Owner, j.Repo, id), true
	}
	if id, ok := issueCommentID(msgID); ok {
		return fmt.Sprintf("/repos/%s/%s/issues/comments/%d/reactions", j.Owner, j.Repo, id), true
	}
	return "", false
}

// noteTrigger records the comment a message in msg's chat could start a turn
// from. A message that can start one but has no reactable comment CLEARS the
// record, so the 👀 never lands on an older comment than the one asked.
func (b *bot) noteTrigger(msg chanlib.InboundMsg) {
	if b.cfg.Token == "" || (msg.Verb != "message" && msg.Verb != "mention") {
		return
	}
	var path string
	var ok bool
	if j, parsed := chanlib.ParseGitHubJID(msg.ChatJID); parsed {
		path, ok = reactionsPath(j, msg.ID)
	}
	b.typing.mu.Lock()
	defer b.typing.mu.Unlock()
	if ok {
		b.typing.trigger[msg.ChatJID] = path
	} else {
		delete(b.typing.trigger, msg.ChatJID)
	}
}

// Typing sets (on) or removes (off) a 👀 on the comment that started the turn.
// The port returns nothing and the turn must not wait on decoration, so a
// GitHub refusal is logged at warn and goes no further; the reply is the
// product.
func (b *bot) Typing(jid string, on bool) {
	if b.cfg.Token == "" {
		return
	}
	var err error
	if on {
		err = b.addEyes(jid)
	} else {
		err = b.removeEyes(jid, nil)
	}
	if err != nil {
		slog.Warn("github typing reaction failed", "jid", jid, "on", on, "err", err)
	}
}

func (b *bot) addEyes(jid string) error {
	if err := b.removeEyes(jid, nil); err != nil {
		slog.Warn("github typing reaction failed", "jid", jid, "on", false, "err", err)
	}
	b.typing.mu.Lock()
	path, ok := b.typing.trigger[jid]
	b.typing.mu.Unlock()
	if !ok {
		slog.Debug("github typing: no reactable trigger comment", "jid", jid)
		return nil
	}
	body, _ := json.Marshal(map[string]string{"content": typingReaction})
	// 200 is GitHub answering that this reaction already stands.
	raw, err := b.call("POST", b.cfg.APIBase+path, body, []int{200, 201})
	if err != nil {
		return err
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.ID == 0 {
		return fmt.Errorf("github reaction response without id: %s", raw)
	}
	live := &liveReaction{path: path, id: created.ID}
	b.typing.mu.Lock()
	defer b.typing.mu.Unlock()
	live.timer = time.AfterFunc(chanlib.DefaultTypingMaxTTL, func() {
		if err := b.removeEyes(jid, live); err != nil {
			slog.Warn("github typing reaction failed", "jid", jid, "on", false, "err", err)
		}
	})
	b.typing.live[jid] = live
	return nil
}

// removeEyes deletes the 👀 standing for jid. Given only, it acts solely if
// that exact reaction still stands, so a TTL timer cannot remove a newer one.
func (b *bot) removeEyes(jid string, only *liveReaction) error {
	b.typing.mu.Lock()
	live := b.typing.live[jid]
	if live == nil || (only != nil && live != only) {
		b.typing.mu.Unlock()
		return nil
	}
	delete(b.typing.live, jid)
	live.timer.Stop()
	b.typing.mu.Unlock()
	_, err := b.call("DELETE", b.cfg.APIBase+live.path+"/"+strconv.FormatInt(live.id, 10), nil, []int{204})
	return err
}

// clearAllEyes removes every standing 👀 at a graceful shutdown. A crash or a
// SIGKILL skips it and leaves them on their comments.
func (b *bot) clearAllEyes() {
	b.typing.mu.Lock()
	jids := make([]string, 0, len(b.typing.live))
	for jid := range b.typing.live {
		jids = append(jids, jid)
	}
	b.typing.mu.Unlock()
	for _, jid := range jids {
		if err := b.removeEyes(jid, nil); err != nil {
			slog.Warn("github typing reaction failed", "jid", jid, "on", false, "err", err)
		}
	}
}

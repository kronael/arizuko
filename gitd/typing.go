package main

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/chanlib/chanstore"
)

// typingReaction is the reaction gitd sets as its typing indicator.
const typingReaction = "eyes"

// useMark wires the shared acknowledgement mark (chanlib.AckMark) over the
// adapter store. The mark decides when to set and clear the 👀; addEyes and
// removeEyes are the two GitHub calls it makes.
func (b *bot) useMark(rc *chanlib.RouterClient, st *chanstore.Store) {
	b.mark = chanlib.NewAckMark(rc, st, b.addEyes, b.removeEyes)
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

// targetPath is reactionsPath for a chat JID and the message id the mark was
// pinned to.
func targetPath(jid, target string) (string, bool) {
	j, ok := chanlib.ParseGitHubJID(jid)
	if !ok {
		return "", false
	}
	return reactionsPath(j, target)
}

// Typing sets (on) or removes (off) a 👀 on the comment that started the turn.
// The port returns nothing and the turn must not wait on decoration, so a
// GitHub refusal is logged at warn by the mark and goes no further; the reply
// is the product.
func (b *bot) Typing(jid string, on bool) {
	if b.cfg.Token == "" || b.mark == nil {
		return
	}
	b.mark.Typing(jid, on)
}

// addEyes puts the 👀 on the comment target names and returns GitHub's reaction
// id, which the delete needs. A target with no reactable comment is
// chanlib.ErrNoMarkTarget.
func (b *bot) addEyes(jid, target string) (string, error) {
	path, ok := targetPath(jid, target)
	if !ok {
		return "", chanlib.ErrNoMarkTarget
	}
	body, _ := json.Marshal(map[string]string{"content": typingReaction})
	// 200 is GitHub answering that this reaction already stands.
	raw, err := b.call("POST", b.cfg.APIBase+path, body, []int{200, 201})
	if err != nil {
		return "", err
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.ID == 0 {
		return "", fmt.Errorf("github reaction response without id: %s", raw)
	}
	return strconv.FormatInt(created.ID, 10), nil
}

// removeEyes deletes the 👀 addEyes made. 404 is the reaction (or its comment)
// already gone, which is what the caller wanted.
func (b *bot) removeEyes(jid, target, handle string) error {
	path, ok := targetPath(jid, target)
	if !ok {
		return fmt.Errorf("github mark target %q has no reactions path", target)
	}
	_, err := b.call("DELETE", b.cfg.APIBase+path+"/"+handle, nil, []int{204, 404})
	return err
}

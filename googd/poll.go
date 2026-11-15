package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/googd/googstore"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

const commentFields = "nextPageToken,comments(id,createdTime,modifiedTime,resolved,deleted,author(displayName,me),content,quotedFileContent(value),mentionedEmailAddresses,assigneeEmailAddress,replies(id,createdTime,modifiedTime,deleted,action,author(displayName,me),content,mentionedEmailAddresses,assigneeEmailAddress))"

type filePoll struct {
	cursor, status, name, lastPoll string
	next                           time.Time
	delay                          time.Duration
}
type poller struct {
	b        *bot
	bindings *store.Store
	db       objstore.Store
	router   interface {
		SendMessage(chanlib.InboundMsg) error
	}
	name       string
	interval   time.Duration
	snapshot   objstore.Snapshot[*[]store.GDriveFile]
	reconciled *[]store.GDriveFile
}

func pollInterval() time.Duration {
	return max(10*time.Second, chanlib.EnvDur("GDRIVE_POLL_INTERVAL", 30*time.Second))
}

func (p *poller) pollLoop(ctx context.Context) {
	for {
		p.sweep(ctx, time.Now())
		timer := time.NewTimer(p.interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
func (p *poller) sweep(ctx context.Context, now time.Time) {
	started := time.Now()
	if !p.b.cred.usable() {
		return
	}
	if err := p.reconcileBindings(ctx); err != nil {
		slog.Error("reconcile Drive bindings", "err", err)
		return
	}
	work, err := googstore.Due(ctx, p.db, p.name, now)
	if err != nil {
		slog.Error("read due Drive work", "err", err)
		return
	}
	for _, item := range work {
		state, err := pollState(item)
		if err != nil {
			slog.Error("read Drive poll state", "file", item.Key.Sort, "err", err)
			continue
		}
		if item.Attrs["active"] != "1" || now.Before(state.next) {
			continue
		}
		var exists bool
		var binding store.GDriveFile
		_, err = objstore.ReadConsistent(ctx, p.bindings.Obj(), store.OwnerScope(store.OwnerRoutd), func(ctx context.Context) error {
			binding, exists, err = p.bindings.GDriveFile(ctx, item.Key.Sort)
			return err
		})
		if err != nil {
			slog.Error("verify Drive binding", "file", item.Key.Sort, "err", err)
			continue
		}
		if !exists || core.TS(binding.CreatedAt) != item.Attrs["binding_created_at"] {
			continue
		}
		status, retry, err := p.pollFile(ctx, item.Key.Sort, state)
		if err != nil {
			slog.Warn("poll Drive file", "file", item.Key.Sort, "err", err)
		}
		state.status = status
		state.lastPoll = now.UTC().Format(time.RFC3339Nano)
		if retry {
			if state.delay == 0 {
				state.delay = 30 * time.Second
			} else {
				state.delay = min(state.delay*2, 15*time.Minute)
			}
		} else {
			state.delay = 0
		}
		delay := state.delay
		if delay == 0 {
			delay = p.interval
		}
		state.next = now.Add(time.Since(started) + delay)
		if err := p.save(ctx, item, state); err != nil {
			slog.Error("save Drive status", "file", item.Key.Sort, "err", err)
		}
	}
}
func (p *poller) save(ctx context.Context, item objstore.Item, state *filePoll) error {
	ok, err := p.db.GuardedBatch(ctx, nil, []objstore.Write{{Op: objstore.Update, Entity: googstore.PollEntity, Key: item.Key,
		Attrs: map[string]string{"cursor": state.cursor, "status": state.status, "name": state.name, "last_poll": state.lastPoll, "next_poll_at": core.TS(state.next), "backoff": state.delay.String()},
		Cond:  objstore.Cond{Where: []objstore.Cmp{{Attr: objstore.IndexRevision, Op: objstore.Eq, Value: item.Attrs[objstore.IndexRevision]}}}}})
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("poll state changed during the Drive request")
	}
	return nil
}
func (p *poller) pollFile(ctx context.Context, id string, state *filePoll) (string, bool, error) {
	base := p.b.cred.apiBase + "/drive/v3/files/" + url.PathEscape(id)
	name := id
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?fields=name", nil)
	if err != nil {
		return "error", false, err
	}
	resp, err := p.b.cred.http.Do(req)
	if err != nil {
		return "error", false, err
	}
	if resp.StatusCode == http.StatusOK {
		var file struct {
			Name string `json:"name"`
		}
		if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&file) == nil && file.Name != "" {
			name = file.Name
		}
	}
	_ = resp.Body.Close()
	state.name = name
	cursor := state.cursor
	maxTime := time.Time{}
	if cursor != "" {
		maxTime, err = time.Parse(time.RFC3339Nano, cursor)
		if err != nil {
			return "error", false, err
		}
	}
	nextCursor := cursor
	token := ""
	for {
		params := url.Values{"fields": {commentFields}, "pageSize": {"100"}}
		if cursor != "" {
			params.Set("startModifiedTime", cursor)
		}
		if token != "" {
			params.Set("pageToken", token)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/comments?"+params.Encode(), nil)
		if err != nil {
			return "error", false, err
		}
		resp, err := p.b.cred.http.Do(req)
		if err != nil {
			return "error", false, err
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if err != nil {
			return "error", false, err
		}
		if resp.StatusCode != http.StatusOK {
			var body struct {
				Error struct {
					Errors []struct {
						Reason string `json:"reason"`
					} `json:"errors"`
				} `json:"error"`
			}
			_ = json.Unmarshal(raw, &body)
			retry := resp.StatusCode == 429 || resp.StatusCode >= 500
			for _, e := range body.Error.Errors {
				if resp.StatusCode == 403 && e.Reason == "userRateLimitExceeded" {
					retry = true
				}
			}
			return strconv.Itoa(resp.StatusCode), retry, fmt.Errorf("comments.list: %s", resp.Status)
		}
		var page struct {
			Next     string         `json:"nextPageToken"`
			Comments []driveComment `json:"comments"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return "error", false, err
		}
		for _, comment := range page.Comments {
			modified, err := time.Parse(time.RFC3339Nano, comment.ModifiedTime)
			if err != nil {
				return "error", false, err
			}
			for _, msg := range mapThread(id, name, p.name, p.b.cred.account(), comment) {
				if err := p.router.SendMessage(msg); err != nil {
					return "error", false, err
				}
				p.b.lastInboundAt.Store(time.Now().Unix())
			}
			if modified.After(maxTime) {
				maxTime = modified
				nextCursor = comment.ModifiedTime
			}
		}
		token = page.Next
		if token == "" {
			state.cursor = nextCursor
			return "ok", false, nil
		}
	}
}

func (p *poller) dashSection() chanlib.DashSection {
	section := chanlib.DashSection{Title: "Bound files"}
	files, err := p.boundFiles(context.Background())
	if err != nil {
		section.Rows = append(section.Rows, [2]string{"error", err.Error()})
		return section
	}
	for _, file := range *files {
		rows, err := p.db.Query(context.Background(), objstore.Query{Entity: googstore.PollEntity, Partition: p.name, Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: file.FileID}})
		if err != nil {
			section.Rows = append(section.Rows, [2]string{file.FileID, "error: " + err.Error()})
			continue
		}
		attrs := map[string]string{}
		if len(rows) > 0 {
			attrs = rows[0].Attrs
		}
		section.Rows = append(section.Rows, [2]string{file.FileID, fmt.Sprintf("name: %s; last poll: %s; status: %s; cursor: %s", attrs["name"], attrs["last_poll"], attrs["status"], attrs["cursor"])})
	}
	return section
}

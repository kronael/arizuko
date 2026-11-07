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
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

const pollEntity = "file_poll"
const commentFields = "nextPageToken,comments(id,createdTime,modifiedTime,resolved,deleted,author(displayName,me),content,quotedFileContent(value),mentionedEmailAddresses,assigneeEmailAddress,replies(id,createdTime,modifiedTime,deleted,action,author(displayName,me),content,mentionedEmailAddresses,assigneeEmailAddress))"

type filePoll struct {
	cursor, status, name, lastPoll string
	next                           time.Time
	delay                          time.Duration
}
type poller struct {
	b        *bot
	bindings interface {
		ListGDriveFiles(string) ([]store.GDriveFile, error)
	}
	db     objstore.Store
	router interface {
		SendMessage(chanlib.InboundMsg) error
	}
	name     string
	interval time.Duration
	files    map[string]*filePoll
}

func pollInterval() time.Duration {
	return max(10*time.Second, chanlib.EnvDur("GDRIVE_POLL_INTERVAL", 30*time.Second))
}

func (p *poller) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		p.sweep(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (p *poller) sweep(ctx context.Context, now time.Time) {
	if !p.b.cred.usable() {
		return
	}
	files, err := p.bindings.ListGDriveFiles("")
	if err != nil {
		slog.Error("read Drive bindings", "err", err)
		return
	}
	active := make(map[string]bool, len(files))
	for _, file := range files {
		active[file.FileID] = true
		state := p.files[file.FileID]
		if state == nil {
			rows, err := p.db.Query(ctx, objstore.Query{Entity: pollEntity, Partition: p.name, Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: file.FileID}})
			if err != nil {
				slog.Error("read Drive cursor", "file", file.FileID, "err", err)
				continue
			}
			state = &filePoll{}
			if len(rows) > 0 {
				state.cursor = rows[0].Attrs["cursor"]
				state.status = rows[0].Attrs["status"]
				state.name = rows[0].Attrs["name"]
				state.lastPoll = rows[0].Attrs["last_poll"]
			}
			p.files[file.FileID] = state
		}
		if now.Before(state.next) {
			continue
		}
		cursor := state.cursor
		status, retry, err := p.pollFile(ctx, file.FileID, state)
		if err != nil {
			slog.Warn("poll Drive file", "file", file.FileID, "err", err)
		}
		state.status = status
		state.lastPoll = now.UTC().Format(time.RFC3339Nano)
		if retry {
			if state.delay == 0 {
				state.delay = 30 * time.Second
			} else {
				state.delay = min(state.delay*2, 15*time.Minute)
			}
			state.next = now.Add(state.delay)
		} else {
			state.delay = 0
			state.next = time.Time{}
		}
		if err := p.save(ctx, file.FileID, state); err != nil {
			state.cursor = cursor
			slog.Error("save Drive status", "file", file.FileID, "err", err)
		}
	}
	for id := range p.files {
		if !active[id] {
			delete(p.files, id)
		}
	}
}
func (p *poller) save(ctx context.Context, id string, state *filePoll) error {
	_, err := p.db.GuardedBatch(ctx, nil, []objstore.Write{{Op: objstore.Put, Entity: pollEntity, Key: objstore.Key{Partition: p.name, Sort: id}, Attrs: map[string]string{"cursor": state.cursor, "status": state.status, "name": state.name, "last_poll": state.lastPoll}}})
	return err
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
	files, err := p.bindings.ListGDriveFiles("")
	if err != nil {
		section.Rows = append(section.Rows, [2]string{"error", err.Error()})
		return section
	}
	for _, file := range files {
		rows, err := p.db.Query(context.Background(), objstore.Query{Entity: pollEntity, Partition: p.name, Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: file.FileID}})
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

package routd

import (
	"context"
	"fmt"
	"strings"
	"time"

	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/routd/msgsearch"
)

// FindMessages is the one handler behind `find_messages` and `GET
// /v1/messages/find`: a bounded newest-first scan of the store
// (specs/6/42-dynamo-over-postgres.md). The caller has already confined
// req.Scope to what it may read.
func (d *DB) FindMessages(ctx context.Context, req apiv1.FindRequest) (apiv1.FindResponse, error) {
	since, err := findInstant("since", req.Since)
	if err != nil {
		return apiv1.FindResponse{}, err
	}
	before, err := findInstant("before", req.Before)
	if err != nil {
		return apiv1.FindResponse{}, err
	}
	var pages msgsearch.Pages
	if strings.Contains(req.Scope, ":") {
		pages = d.store().ScanChat(req.Scope, before)
	} else {
		folders, err := d.store().ScanFolders(ctx, req.Scope, before)
		if err != nil {
			return apiv1.FindResponse{}, err
		}
		pages = folders
	}
	return msgsearch.Search(ctx, pages, req.Query, req.Sender, since, req.Limit)
}

func findInstant(name, raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s %q is not an RFC3339 instant, so the search has "+
			"no bound to stop at: %w", name, raw, err)
	}
	return t, nil
}

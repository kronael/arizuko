package chanreg

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const (
	healthInterval = 30 * time.Second
	maxHealthFails = 3
)

func (r *Registry) StartHealthLoop(ctx context.Context) {
	go func() {
		t := time.NewTicker(healthInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				r.checkAll()
			}
		}
	}()
}

var healthClient = &http.Client{Timeout: 10 * time.Second}

// healthPing returns nil for ok or stale (adapter alive, platform quiet);
// returns an error for disconnected, transport failures, or unexpected status.
func healthPing(c *http.Client, baseURL string) error {
	resp, err := c.Get(baseURL + "/health")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	var b struct{ Status string }
	_ = json.Unmarshal(body, &b)
	if b.Status == "stale" {
		return nil
	}
	return fmt.Errorf("health: status %d %s", resp.StatusCode, b.Status)
}

func (r *Registry) checkAll() {
	entries := r.All()
	for name, e := range entries {
		if err := healthPing(healthClient, e.URL); err != nil {
			fails := r.RecordHealthFail(name)
			slog.Warn("channel health failed",
				"channel", name, "fails", fails, "err", err)
			if fails >= maxHealthFails {
				slog.Error("channel auto-deregistered",
					"channel", name, "fails", fails)
				r.Deregister(name)
			}
		} else {
			r.ResetHealth(name)
		}
	}
}

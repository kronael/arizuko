package chanreg

import (
	"context"
	"log/slog"
	"time"
)

const (
	healthInterval = 30 * time.Second
	maxHealthFails = 3
)

// StartHealthLoop pings all registered channels every 30s.
// Three consecutive failures triggers auto-deregister.
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

func (r *Registry) checkAll() {
	entries := r.All()
	for name, e := range entries {
		ch := NewHTTPChannel(e, r.secret)
		if err := ch.HealthCheck(); err != nil {
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

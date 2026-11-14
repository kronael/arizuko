package routd

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type verdictWaiters struct {
	mu         sync.Mutex
	waiters    map[string]map[chan struct{}]struct{}
	wake, done chan struct{}
	cancel     context.CancelFunc
}

func (d *DB) waitForApproval(id string) (<-chan struct{}, func()) {
	v := &d.verdicts
	v.mu.Lock()
	if v.waiters == nil {
		v.waiters = map[string]map[chan struct{}]struct{}{}
	}
	if v.done == nil {
		v.wake, v.done = make(chan struct{}, 1), make(chan struct{})
		ctx, cancel := context.WithCancel(context.Background())
		v.cancel = cancel
		go d.consumeApprovals(ctx, v.done)
	}
	ch := make(chan struct{}, 1)
	if v.waiters[id] == nil {
		v.waiters[id] = map[chan struct{}]struct{}{}
	}
	v.waiters[id][ch] = struct{}{}
	select {
	case v.wake <- struct{}{}:
	default:
	}
	v.mu.Unlock()
	return ch, func() {
		v.mu.Lock()
		delete(v.waiters[id], ch)
		if len(v.waiters[id]) == 0 {
			delete(v.waiters, id)
		}
		select {
		case v.wake <- struct{}{}:
		default:
		}
		v.mu.Unlock()
	}
}
func (v *verdictWaiters) close() {
	v.mu.Lock()
	cancel, done := v.cancel, v.done
	v.cancel = nil
	v.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}
func (d *DB) consumeApprovals(ctx context.Context, done chan struct{}) {
	v := &d.verdicts
	defer close(done)
	for {
		v.mu.Lock()
		active := len(v.waiters) > 0
		v.mu.Unlock()
		var next <-chan time.Time
		var timer *time.Timer
		if active {
			d.publishApprovalWork(ctx)
			timer = time.NewTimer(hitlPoll)
			next = timer.C
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-v.wake:
		case <-next:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}
func (d *DB) publishApprovalWork(ctx context.Context) {
	work, err := d.store().DueWork(ctx, "approvals", time.Now())
	if err != nil {
		slog.Error("read approval work", "err", err)
		return
	}
	for _, item := range work {
		d.verdicts.mu.Lock()
		for ch := range d.verdicts.waiters[item.Key.Sort] {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
		d.verdicts.mu.Unlock()
		if err := d.store().AcknowledgeWork(ctx, item); err != nil {
			slog.Error("acknowledge approval work", "err", err)
		}
	}
}

package chanlib

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/kronael/arizuko/chanlib/chanstore"
	"github.com/kronael/arizuko/core"
)

// ErrNoMarkTarget is what an adapter's add returns for a message that cannot
// carry a mark (a GitHub review has no reactable comment). The helper logs it
// at debug and sets nothing.
var ErrNoMarkTarget = errors.New("message cannot carry a mark")

// AckMark is the acknowledgement mark: the reaction an adapter without a native
// presence indicator (slakd, gitd) puts on the message that started a turn
// while the turn runs (specs/6/46-turn-progress.md §"The mark, implemented
// once"). The adapter supplies the two platform calls; AckMark decides when,
// remembers where the mark stands, and clears it again.
//
// WHICH MESSAGE. Typing(jid, on) names a chat, not a message. AckMark attaches
// to the RouterClient and records, per chat, the newest inbound that can start
// a turn, at the one place every inbound passes on its way to routd. Typing(on)
// pins that message; Typing(off) removes the mark from the message it was
// pinned to, even when newer messages arrived since.
//
// DURABILITY. Each mark is recorded in the adapter store before Typing(on)
// returns, because the end of the turn arrives later and names only the chat: a
// restart in between must still find the message and the platform's handle. The
// record is deleted only after the platform confirmed the removal.
//
//	on   release this chat's earlier mark; add on the pinned message; record it
//	off  release the recorded mark
//	boot Sweep: a mark older than DefaultTypingMaxTTL is released; a younger one
//	     stays, because its turn may still be running and routd's off will come
//	     (it keeps the in-process backstop for the remaining time)
//
// A platform refusal is logged at warn and goes no further: the port returns
// nothing and the reply is the product.
type AckMark struct {
	st     *chanstore.Store
	add    func(jid, target string) (handle string, err error)
	remove func(jid, target, handle string) error
	ttl    time.Duration

	// cand is a hint, not state of record: the newest turn-capable inbound id per
	// chat. Its own mutex keeps a slow platform call in Typing from stalling the
	// inbound path.
	cmu  sync.Mutex
	cand map[string]string

	// mu serialises the platform calls and the store writes, so an off can never
	// overtake the on it follows.
	mu      sync.Mutex
	timers  map[string]*backstop
	lastGen uint64
}

// A backstop is one mark's expiry timer. gen tells a firing timer whether it is
// still the one armed for its chat.
type backstop struct {
	timer *time.Timer
	gen   uint64
}

// NewAckMark builds the helper over the adapter's store and attaches it to rc.
// add puts the mark on target and returns the handle remove needs ("" when the
// platform removes by coordinate); remove takes it off. Both must treat "already
// gone" as success. It does no I/O: the adapter calls Sweep once at boot.
func NewAckMark(rc *RouterClient, st *chanstore.Store,
	add func(jid, target string) (string, error),
	remove func(jid, target, handle string) error) *AckMark {
	m := &AckMark{
		st: st, add: add, remove: remove,
		ttl:    DefaultTypingMaxTTL,
		cand:   map[string]string{},
		timers: map[string]*backstop{},
	}
	rc.regMu.Lock()
	rc.mark = m
	rc.regMu.Unlock()
	return m
}

// note records msg as its chat's candidate when it can start a turn. A message
// that can but has no reactable form still becomes the candidate: the adapter's
// add refuses it, so the mark never lands on an older message than the one
// asked.
func (m *AckMark) note(msg InboundMsg) {
	if msg.ChatJID == "" || msg.ID == "" {
		return
	}
	switch msg.Verb {
	case "", "message", "mention":
	default:
		return
	}
	m.cmu.Lock()
	m.cand[msg.ChatJID] = msg.ID
	m.cmu.Unlock()
}

// Typing is the adapter's BotHandler.Typing.
func (m *AckMark) Typing(jid string, on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mk, held, err := m.st.MarkFor(context.Background(), jid); err != nil {
		slog.Warn("ack mark: read failed", "jid", jid, "on", on, "err", err)
	} else if held {
		m.release(mk)
	}
	if !on {
		return
	}
	m.cmu.Lock()
	target := m.cand[jid]
	m.cmu.Unlock()
	if target == "" {
		slog.Debug("ack mark: no recorded trigger message", "jid", jid)
		return
	}
	handle, err := m.add(jid, target)
	switch {
	case errors.Is(err, ErrNoMarkTarget):
		slog.Debug("ack mark: the trigger message cannot carry a mark", "jid", jid, "target", target)
		return
	case err != nil:
		slog.Warn("ack mark: add failed", "jid", jid, "target", target, "err", err)
		return
	}
	if err := m.st.SetMark(context.Background(), jid, target, handle, core.NowTS()); err != nil {
		// An unrecorded mark could never be found again, so it does not stay.
		slog.Error("ack mark: record failed; removing the mark again", "jid", jid, "err", err)
		if rerr := m.remove(jid, target, handle); rerr != nil {
			slog.Error("ack mark: removal after a failed record failed", "jid", jid, "err", rerr)
		}
		return
	}
	m.arm(jid, m.ttl)
}

// Sweep releases every recorded mark older than the backstop and re-arms the
// backstop for the rest. An adapter runs it once at boot.
func (m *AckMark) Sweep(ctx context.Context) {
	marks, err := m.st.Marks(ctx)
	if err != nil {
		slog.Error("ack mark: boot sweep could not read the marks", "err", err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, mk := range marks {
		if ctx.Err() != nil {
			return
		}
		set, perr := time.Parse(core.TSLayout, mk.SetAt)
		age := time.Since(set)
		if perr != nil || age >= m.ttl {
			// An unreadable set_at cannot be aged, so it is treated as old.
			m.release(mk)
			continue
		}
		m.arm(mk.ChatJID, m.ttl-age)
	}
}

// Stop ends the in-process backstops. It removes no mark: a restart in the
// middle of a turn keeps the mark standing, and the recorded one is found again
// by Typing(off) or the next Sweep.
func (m *AckMark) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for jid, b := range m.timers {
		b.timer.Stop()
		delete(m.timers, jid)
	}
}

// release takes mk off the platform and forgets it. A failed removal keeps the
// record, so the next boot's sweep tries again. m.mu is held.
func (m *AckMark) release(mk chanstore.Mark) {
	if b := m.timers[mk.ChatJID]; b != nil {
		b.timer.Stop()
		delete(m.timers, mk.ChatJID)
	}
	if err := m.remove(mk.ChatJID, mk.Target, mk.Handle); err != nil {
		slog.Warn("ack mark: remove failed; the record stays for the next sweep",
			"jid", mk.ChatJID, "target", mk.Target, "err", err)
		return
	}
	if err := m.st.DropMark(context.Background(), mk.ChatJID); err != nil {
		slog.Warn("ack mark: the mark is off the platform but its record stays",
			"jid", mk.ChatJID, "err", err)
	}
}

// arm starts the backstop for jid, replacing any earlier one. m.mu is held.
func (m *AckMark) arm(jid string, d time.Duration) {
	if old := m.timers[jid]; old != nil {
		old.timer.Stop()
	}
	m.lastGen++
	gen := m.lastGen
	m.timers[jid] = &backstop{gen: gen, timer: time.AfterFunc(d, func() { m.expire(jid, gen) })}
}

// expire is the backstop firing: it releases the mark only if gen is still the
// backstop armed for the chat, so an earlier mark's timer cannot take a newer
// mark down.
func (m *AckMark) expire(jid string, gen uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b := m.timers[jid]; b == nil || b.gen != gen {
		return
	}
	delete(m.timers, jid)
	mk, held, err := m.st.MarkFor(context.Background(), jid)
	if err != nil {
		slog.Warn("ack mark: backstop could not read the mark", "jid", jid, "err", err)
		return
	}
	if held {
		m.release(mk)
	}
}

package runed

import (
	"context"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
	routdv1 "github.com/kronael/arizuko/routd/api/v1"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// FakeRuntime backs the contract test + standalone acceptance: it invokes a
// caller-supplied function with the RunSpec (which can drive the federated
// callbacks against routd) and returns its outcome. No docker, no socket
// (spec 5/P § acceptance: FakeRuntime backs unit tests of the envelope
// without spawning anything).
type FakeRuntime struct {
	Fn func(ctx context.Context, spec RunSpec) RunResult
}

// Run invokes the injected function.
func (f FakeRuntime) Run(ctx context.Context, spec RunSpec) RunResult {
	if f.Fn == nil {
		return RunResult{Outcome: runedv1.OutcomeSilent}
	}
	return f.Fn(ctx, spec)
}

// dockerRuntime is the production Runtime: it stands up the per-tenant MCP
// host (ipc.ServeMCP) with GatedFns repointed at HTTP forwards into routd
// (the Federator), then spawns the per-turn container via container.Run.
// The envelope (socket, spawn, stream, teardown) is owned here; frames
// arrive out-of-band via the federated callbacks. runed never appends.
type dockerRuntime struct {
	cfg     *core.Config
	folders *groupfolder.Resolver
	runner  container.Runner
	fed     *Federator
}

// NewDockerRuntime builds the production Runtime around the docker runner +
// the federation forward to routd. fed forwards the agent's message tools
// to routd /v1/turns/{turn_id}/* (the sole appender).
func NewDockerRuntime(cfg *core.Config, folders *groupfolder.Resolver, fed *Federator) Runtime {
	return &dockerRuntime{cfg: cfg, folders: folders, runner: container.DockerRunner{}, fed: fed}
}

// Run spawns one container turn. GatedFns are repointed at the Federator so
// the agent's reply/send/like/... tool calls forward to routd over HTTP,
// stamped with this run's turn_id + the brokered token. submit_turn fans to
// routd's /result twin.
func (d *dockerRuntime) Run(ctx context.Context, spec RunSpec) RunResult {
	gated := d.gatedFns(ctx, spec)
	out := d.runner.Run(d.cfg, d.folders, container.Input{
		Prompt:    spec.MessageBatch,
		SessionID: spec.SessionID,
		ChatJID:   spec.ChatJID,
		Folder:    spec.Folder,
		Topic:     spec.Topic,
		MessageID: spec.TurnID,
		Sender:    spec.TriggerSender,
		GatedFns:  gated,
	})
	res := RunResult{
		Outcome:      outcomeFor(out),
		NewSessionID: out.NewSessionID,
		Error:        out.Error,
	}
	return res
}

// gatedFns builds the federation forward: every message tool the agent
// calls is HTTP-forwarded to routd, stamped with spec.TurnID + spec.Token.
// idemKey is per-call; the agent's tool layer is at-least-once, so a stable
// per-call key keeps the routd ledger honest.
func (d *dockerRuntime) gatedFns(ctx context.Context, spec RunSpec) ipc.GatedFns {
	idem := func() string { return "fed-" + randHex(8) }
	return ipc.GatedFns{
		SendReply: func(jid, text, replyTo string) (string, error) {
			r, err := d.fed.Forward(ctx, "reply", spec.TurnID, spec.Token, idem(),
				map[string]any{"jid": jid, "text": text, "reply_to_id": replyTo})
			return platformID(r), err
		},
		SendMessage: func(jid, text string) (string, error) {
			r, err := d.fed.Forward(ctx, "send", spec.TurnID, spec.Token, idem(),
				map[string]any{"jid": jid, "text": text})
			return platformID(r), err
		},
		SendDocument: func(jid, path, name, caption, replyTo, _ string) error {
			_, err := d.fed.Forward(ctx, "send_file", spec.TurnID, spec.Token, idem(),
				map[string]any{"jid": jid, "path": path, "name": name, "caption": caption, "reply_to_id": replyTo})
			return err
		},
		Like: func(jid, target, reaction string) error {
			_, err := d.fed.Forward(ctx, "like", spec.TurnID, spec.Token, idem(),
				map[string]any{"jid": jid, "platform_id": target, "reaction": reaction})
			return err
		},
		Dislike: func(jid, target string) error {
			_, err := d.fed.Forward(ctx, "dislike", spec.TurnID, spec.Token, idem(),
				map[string]any{"jid": jid, "platform_id": target})
			return err
		},
		Edit: func(jid, target, content string) error {
			_, err := d.fed.Forward(ctx, "edit", spec.TurnID, spec.Token, idem(),
				map[string]any{"jid": jid, "platform_id": target, "content": content})
			return err
		},
		Delete: func(jid, target string) error {
			_, err := d.fed.Forward(ctx, "delete", spec.TurnID, spec.Token, idem(),
				map[string]any{"jid": jid, "platform_id": target})
			return err
		},
		Pin: func(jid, target string) error {
			_, err := d.fed.Forward(ctx, "pin_message", spec.TurnID, spec.Token, idem(),
				map[string]any{"jid": jid, "platform_id": target})
			return err
		},
		Unpin: func(jid, target string, all bool) error {
			tool := "unpin_message"
			if all {
				tool = "unpin_all"
			}
			_, err := d.fed.Forward(ctx, tool, spec.TurnID, spec.Token, idem(),
				map[string]any{"jid": jid, "platform_id": target})
			return err
		},
		SubmitTurn: func(folder string, t ipc.TurnResult) error {
			_, err := d.fed.Result(ctx, spec.TurnID, spec.Token, "turn-"+spec.TurnID, routdv1.TurnResult{
				TurnID: spec.TurnID, SessionID: t.SessionID, Status: t.Status,
				Result: t.Result, Error: t.Error,
			})
			return err
		},
	}
}

func outcomeFor(o container.Output) string {
	switch {
	case o.Status == "error":
		return runedv1.OutcomeError
	case !o.HadOutput && o.Result == "":
		return runedv1.OutcomeSilent
	default:
		return runedv1.OutcomeOK
	}
}

func platformID(r any) string {
	if sr, ok := r.(routdv1.SendResult); ok {
		return sr.PlatformID
	}
	return ""
}

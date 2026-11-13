package store

// run_turns.go holds `run_turns`: one item per turn a run carried, partition
// the run id, sort the turn id, carrying the chat whose typing indicator the
// run owns. A follow-up steered into a live run finishes, and leaves
// `turn_context`, while the run still shows typing in its chat, so the run-end
// clear reads the run's own partition (BUGS T1, L823).

import (
	"context"
	"fmt"

	"github.com/kronael/arizuko/objstore"
)

func runTurnWrite(runID string, turn objstore.Item) objstore.Write {
	chat := turn.Attrs[attrTurnReturnTo]
	if chat == "" {
		chat = turn.Attrs[attrTurnChatJID]
	}
	return objstore.Write{Op: objstore.Put, Entity: RunTurns,
		Key:   objstore.Key{Partition: runID, Sort: turn.Key.Sort},
		Attrs: map[string]string{attrTurnChatJID: chat}}
}

// SetTurnRunID records the run handle runed assigned to a turn, and the turn's
// chat under the run. A turn nobody bound records nothing.
func (s *Store) SetTurnRunID(turnID, runID string) error {
	ctx := context.Background()
	key, err := turnKey(turnID)
	if err != nil {
		return err
	}
	it, ok, err := s.itemAt(ctx, TurnContexts, key)
	if err != nil || !ok {
		return err
	}
	// A refusal is a turn that left turn_context since the read.
	if _, err := s.TryWriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Update, Entity: TurnContexts, Key: key,
		Attrs: map[string]string{attrTurnRunID: runID},
	}, runTurnWrite(runID, it)); err != nil {
		return fmt.Errorf("record the run of turn %s: %w", turnID, err)
	}
	return nil
}

// TypingJIDsForRun answers the chats whose typing indicator a run owns: one per
// turn recorded against the run, the return address before the turn's chat.
func (s *Store) TypingJIDsForRun(runID string) []string {
	if runID == "" {
		return nil
	}
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: RunTurns, Partition: runID,
	})
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, it := range items {
		jid := it.Attrs[attrTurnChatJID]
		if jid == "" || seen[jid] {
			continue
		}
		seen[jid] = true
		out = append(out, jid)
	}
	return out
}

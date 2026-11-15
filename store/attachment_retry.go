package store

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/retry"
)

const (
	attachmentFailures = "attachment_failures"
	attachmentRetryAt  = "attachment_retry_at"
	attachmentState    = "attachment_retry_state"
)

var attachmentAttrs = []string{attachmentFailures, attachmentRetryAt, attachmentState}

type AttachmentRetry struct {
	Message  core.Message
	Observed objstore.Item
	Failures int
	NextAt   time.Time
	State    string
}

func attachmentRetryFrom(it objstore.Item) (AttachmentRetry, error) {
	result := AttachmentRetry{Message: messageFrom(it), Observed: it}
	a := it.Attrs
	present := false
	for _, name := range attachmentAttrs {
		_, ok := a[name]
		present = present || ok
	}
	if !present {
		return result, nil
	}
	n, err := strconv.Atoi(a[attachmentFailures])
	if err != nil || n < 0 {
		return result, fmt.Errorf("invalid attachment retry metadata on %s/%s", it.Key.Partition, it.Key.Sort)
	}
	result.Failures, result.State = n, a[attachmentState]
	switch result.State {
	case "pending":
		result.NextAt, err = time.Parse(time.RFC3339Nano, a[attachmentRetryAt])
		if err != nil {
			return result, fmt.Errorf("invalid attachment retry deadline: %w", err)
		}
	case "failed":
		if _, ok := a[attachmentRetryAt]; ok {
			return result, fmt.Errorf("failed attachment has deadline")
		}
	default:
		return result, fmt.Errorf("invalid attachment retry state %q", result.State)
	}
	return result, nil
}

func (s *Store) ReadAttachmentRetry(ctx context.Context, m core.Message) (AttachmentRetry, bool, error) {
	it, found, err := s.messageAt(ctx, m.ChatJID, m.ID)
	if err != nil || !found {
		return AttachmentRetry{}, found, err
	}
	result, err := attachmentRetryFrom(it)
	return result, true, err
}

func attachmentGuard(it objstore.Item) objstore.Cond {
	guard := objstore.Cond{Item: objstore.ItemPresent}
	for _, key := range []string{objstore.IndexRevision, attrMsgAttachments, attrMsgContent} {
		cmp := objstore.Cmp{Attr: key, Op: objstore.Absent}
		if value, ok := it.Attrs[key]; ok {
			cmp.Op, cmp.Value = objstore.Eq, value
		}
		guard.Where = append(guard.Where, cmp)
	}
	return guard
}

func (s *Store) DeferAttachments(ctx context.Context, observed AttachmentRetry, decision retry.Decision, terminalContent string) (bool, error) {
	attrs := map[string]string{attachmentFailures: strconv.Itoa(decision.Failures)}
	var remove []string
	if decision.Stop {
		attrs[attachmentState] = "failed"
		attrs[attrMsgContent] = terminalContent
		remove = []string{attachmentRetryAt}
	} else {
		attrs[attachmentState] = "pending"
		attrs[attachmentRetryAt] = core.TS(decision.NextAt)
	}
	return s.TryWriteEntities(ctx, nil, objstore.Write{Op: objstore.Update, Entity: Messages, Key: observed.Observed.Key, Attrs: attrs, Remove: remove, Cond: attachmentGuard(observed.Observed)})
}

func (s *Store) CompleteAttachments(ctx context.Context, observed AttachmentRetry, content string) (bool, error) {
	remove := append([]string{attrMsgAttachments}, attachmentAttrs...)
	return s.TryWriteEntities(ctx, nil, objstore.Write{Op: objstore.Update, Entity: Messages, Key: observed.Observed.Key, Attrs: map[string]string{attrMsgContent: content}, Remove: remove, Cond: attachmentGuard(observed.Observed)})
}

func (s *Store) DueAttachments(ctx context.Context, now time.Time) ([]AttachmentRetry, error) {
	items, err := objstore.IndexItems(ctx, s.Obj(), "attachments/due", objstore.KeyPredicate{Op: objstore.LtKey, Value: core.TS(now) + "\x7f"}, 200)
	if err != nil {
		return nil, err
	}
	var due []AttachmentRetry
	for _, it := range items {
		work, err := attachmentRetryFrom(it)
		if err != nil {
			return nil, err
		}
		if work.State != "pending" || work.NextAt.After(now) {
			continue
		}
		chat, found, err := s.chatItem(ctx, it.Key.Partition)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		if it.Key.Sort <= chat.Attrs[AttrChatAgentCursor] {
			turns, err := s.LiveTurnsInChat(ctx, it.Key.Partition)
			if err != nil {
				return nil, err
			}
			owned := false
			for _, turn := range turns {
				if IsActiveTurnState(turn.State) && slices.Contains(turn.InputKeys, it.Key) {
					owned = true
					break
				}
			}
			if !owned {
				witness := objstore.Check{Entity: Chats, Key: chat.Key, Cond: objstore.Cond{Item: objstore.ItemPresent, Where: []objstore.Cmp{attrIs(objstore.IndexRevision, chat.Attrs[objstore.IndexRevision])}}}
				// A refusal means the chat or the message changed since it was read;
				// the next discovery rereads both and decides again.
				_, err = s.TryWriteEntities(ctx, []objstore.Check{witness}, objstore.Write{Op: objstore.Update, Entity: Messages, Key: it.Key, Remove: attachmentAttrs, Cond: attachmentGuard(it)})
				if err != nil {
					return nil, err
				}
				continue
			}
		}
		due = append(due, work)
	}
	return due, nil
}

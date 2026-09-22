package store

// pending_actions.go declares `pending_actions`: one item per tool call held
// for a human's verdict, addressed by its id. This package declares the shape
// and routd writes it (`routd/pending_actions.go`): the group delete reads the
// folder off an item and routd's handlers write it, which justifies a second
// READER, never a second format. Two spellings would put routd's items where
// folder_scope.go's purge does not look.
//
// The partition is OwnerRoutd because resreg's entity path writes these items
// too (resreg.SubsystemRoutd IS store.OwnerRoutd). The id is the coordinate;
// every other selection is a partition read with a filter. There is no
// item→struct reader here: the struct is routd's PendingAction, with three
// fields derived from `args` that no item carries.

import (
	"fmt"

	"github.com/kronael/arizuko/objstore"
)

// The attributes a held call carries; `id` is the coordinate. EXPORTED because
// the writer is in another package. `created_at`, `reviewed_at` and
// `expires_at` are NOT stamped fields: the writer decides their layout.
const (
	AttrPendGroupFolder  = "group_folder"
	AttrPendCallerAgent  = "caller_agent"
	AttrPendTool         = "tool"
	AttrPendArgs         = "args"
	AttrPendStatus       = "status"
	AttrPendChatJID      = "chat_jid"
	AttrPendCreatedAt    = "created_at"
	AttrPendReviewedBy   = "reviewed_by"
	AttrPendReviewedAt   = "reviewed_at"
	AttrPendReviewerNote = "reviewer_note"
	AttrPendExpiresAt    = "expires_at"
	AttrPendNoticeID     = "notice_id"
)

// PendingActionKey is the coordinate of one held call.
func PendingActionKey(id string) (objstore.Key, error) {
	if id == "" {
		return objstore.Key{}, fmt.Errorf("pending action carries no id, which is its coordinate")
	}
	return objstore.Key{Partition: OwnerRoutd, Sort: id}, nil
}

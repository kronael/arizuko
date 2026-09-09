package store

// pending_actions.go declares `pending_actions`: one item per tool call held for
// a human's verdict, addressed by its id.
//
// THIS PACKAGE DECLARES THE SHAPE AND routd WRITES IT. The hold rule is an `acl`
// row and the suspended CALL is routd's (`routd/pending_actions.go`), so no read
// or write of one lives here. What lives here is the coordinate and the
// attribute names, because a second consumer needs the same facts in a different
// SHAPE — this package's group delete reads them off an item, routd's handlers
// write them onto one — and that justifies a second READER, never a second
// format. Two spellings would put routd's items where folder_scope.go's purge
// does not look, and a deleted folder would keep held calls the next tenant of
// the path answers.
//
// THE PARTITION IS OwnerRoutd AND IT IS NOT A CHOICE THIS FILE MAKES.
// `pending_actions` is a registered resreg resource, and an entity-backed
// resource's items live at its owner subsystem (resreg.SubsystemRoutd, which IS
// store.OwnerRoutd).
//
// THE ID IS THE COORDINATE. Every other selection — the folder's held calls, the
// (folder, tool, args_hash) release match, the expiry sweep — is on an
// attribute, and an attribute is not a leading fragment of the key, so each of
// those is a partition read with a filter.
//
// THERE IS NO item→struct READER HERE, and that is the same rule: the struct is
// routd's `PendingAction`, with three fields derived from `args_final` that no
// item carries, and a second struct beside it would be a second format of the
// row. The reader belongs with the struct.

import (
	"fmt"

	"github.com/kronael/arizuko/objstore"
)

// The attributes a held call carries. `id` is NOT among them: it is the
// coordinate, and storing it twice is two facts that can disagree.
//
// They are EXPORTED because the writer is in another package; the exported half
// is the marker of that. `created_at`, `reviewed_at` and `expires_at` are NOT
// stamped fields — the resource declares none — so the writer decides their
// layout and every reader of one parses what that writer renders.
const (
	AttrPendGroupFolder  = "group_folder"
	AttrPendCallerAgent  = "caller_agent"
	AttrPendTool         = "tool"
	AttrPendArgs         = "args"
	AttrPendArgsFinal    = "args_final"
	AttrPendArgsHash     = "args_hash"
	AttrPendStatus       = "status"
	AttrPendChatJID      = "chat_jid"
	AttrPendCreatedAt    = "created_at"
	AttrPendReviewedBy   = "reviewed_by"
	AttrPendReviewedAt   = "reviewed_at"
	AttrPendReviewerNote = "reviewer_note"
	AttrPendResult       = "result"
	AttrPendError        = "error"
	AttrPendExpiresAt    = "expires_at"
	AttrPendMode         = "mode"
	AttrPendNoticeID     = "notice_id"
)

// PendingActionKey is the coordinate of one held call.
func PendingActionKey(id string) (objstore.Key, error) {
	if id == "" {
		return objstore.Key{}, fmt.Errorf("pending action carries no id, which is its coordinate")
	}
	return objstore.Key{Partition: OwnerRoutd, Sort: id}, nil
}

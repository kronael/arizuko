package routd

import (
	"fmt"

	"github.com/kronael/arizuko/chanlib"
)

type deliveryPlace struct {
	jid, threadID, threadRoot string
}

func (p deliveryPlace) topic() string {
	if p.threadID != "" {
		return p.threadID
	}
	return p.threadRoot
}

func summaryThreadEnabled(db *DB, dl Deliverer, tc TurnContext, jid string) bool {
	if dl == nil {
		return false
	}
	if tc.Topic != "" {
		return dl.HasCapability(jid, "thread_id")
	}
	return dl.HasCapability(jid, "thread_root") &&
		db.ChatIsGroup(jid) && db.ThreadReplies(tc.Folder, true)
}

func placeTurnDelivery(db *DB, dl Deliverer, tc TurnContext, jid, explicitThread string, reply bool) (deliveryPlace, error) {
	p := deliveryPlace{jid: jid, threadID: explicitThread}
	if jid == tc.ChatJID {
		p.jid = returnTarget(tc, jid)
	}
	if jid != tc.ChatJID && jid != returnTarget(tc, tc.ChatJID) {
		return p, nil
	}
	if tc.Topic != "" {
		if p.threadID == "" {
			p.threadID = tc.Topic
		}
		return p, nil
	}
	if summaryThreadEnabled(db, dl, tc, p.jid) {
		if row, ok := db.MessageByID(p.jid, summaryID(tc.TurnID)); ok {
			if p.threadID != "" && p.threadID != row.PlatformID {
				return p, nil
			}
			if row.PlatformID == "" {
				pid, err := dl.Send(p.jid, row.Content, "", row.Topic, "", row.ID)
				if err != nil || pid == "" {
					return p, fmt.Errorf("%w: task summary is pending; this message was not delivered; retry after the opener arrives", chanlib.ErrPermanent)
				}
				if err := db.MarkDelivered(row, pid); err != nil {
					return p, fmt.Errorf("%w: summary delivery could not be recorded: %v", chanlib.ErrPermanent, err)
				}
				row.PlatformID = pid
			}
			p.threadID = ""
			p.threadRoot = row.PlatformID
			return p, nil
		}
	}
	if reply && p.threadID == "" && p.jid == tc.ChatJID &&
		db.ThreadReplies(tc.Folder, db.ChatIsGroup(p.jid)) {
		p.threadRoot = tc.TriggerMsgID
	}
	return p, nil
}

func (s *Server) sendTurnNotice(tc TurnContext, text string) error {
	p, err := placeTurnDelivery(s.db, s.deliver, tc, tc.ChatJID, "", true)
	if err != nil {
		return err
	}
	_, err = s.deliver.Send(p.jid, text, "", p.threadID, p.threadRoot, "silent-"+tc.TurnID)
	return err
}

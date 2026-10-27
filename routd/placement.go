package routd

import (
	"fmt"
	"strings"

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

func summaryThreadEnabled(db *DB, dl Deliverer, tc TurnContext, jid string) (bool, error) {
	if dl == nil {
		return false, nil
	}
	if tc.Topic != "" {
		return dl.HasCapability(jid, "thread_id"), nil
	}
	if !dl.HasCapability(jid, "thread_root") {
		return false, nil
	}
	on, err := db.store().ChatThreadReplies(jid)
	if err != nil || !on {
		return false, err
	}
	return db.store().ChatIsGroup(jid)
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
	// A thread id is the bare platform id every adapter sends as a topic; teled
	// and slakd spell the trigger row id "<jid>/<id>".
	trigger := strings.TrimPrefix(tc.TriggerMsgID, tc.ChatJID+"/")
	if reply && p.threadID != "" && p.threadID == trigger &&
		p.jid == tc.ChatJID {
		p.threadRoot, p.threadID = p.threadID, ""
		return p, nil
	}
	enabled, err := summaryThreadEnabled(db, dl, tc, p.jid)
	if err != nil {
		return p, fmt.Errorf("%w: chat thread preference could not be read: %v", chanlib.ErrPermanent, err)
	}
	if enabled {
		row, ok, err := readTaskSummary(db, p.jid, tc.TurnID)
		if err != nil {
			return p, fmt.Errorf("%w: task summary could not be read; this message was not delivered; retry after storage recovers: %v", chanlib.ErrPermanent, err)
		}
		if ok {
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
	if reply && p.threadID == "" && p.jid == tc.ChatJID {
		on, err := db.store().ChatThreadReplies(p.jid)
		if err != nil {
			return p, fmt.Errorf("%w: chat thread preference could not be read: %v", chanlib.ErrPermanent, err)
		}
		if on {
			p.threadRoot = trigger
		}
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

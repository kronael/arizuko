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

func placeTurnDelivery(db *DB, tc TurnContext, jid, explicitThread string, reply bool) (deliveryPlace, error) {
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
	p, err := placeTurnDelivery(s.db, tc, tc.ChatJID, "", true)
	if err != nil {
		return err
	}
	_, err = s.deliver.Send(p.jid, text, "", p.threadID, p.threadRoot, "silent-"+tc.TurnID)
	return err
}

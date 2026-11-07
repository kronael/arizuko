package ipc

import (
	"encoding/json"
	"testing"
)

func TestSubmitStatusReturnsReminderAck(t *testing.T) {
	delay := int64(5000)
	var reply any
	handleSubmitStatus([]byte(`{"params":{"turn_id":"t"}}`), 1, RoutdFns{
		SubmitStatus: func(_ string, status TurnStatus) (TurnStatusAck, error) {
			if status.TurnID != "t" {
				t.Fatalf("turn: %+v", status)
			}
			return TurnStatusAck{Remind: true, ReminderAfterMS: &delay}, nil
		},
	}, "demo", func(value any) { reply = value })
	raw, err := json.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Result TurnStatusAck `json:"result"`
	}
	if err := json.Unmarshal(raw, &response); err != nil || !response.Result.Remind || response.Result.ReminderAfterMS == nil || *response.Result.ReminderAfterMS != delay {
		t.Fatalf("response: %s %v", raw, err)
	}
}

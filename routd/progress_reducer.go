package routd

import (
	"encoding/json"
	"time"

	"github.com/kronael/arizuko/ipc"
)

type progressItem struct {
	ID         string  `json:"id"`
	Content    string  `json:"content"`
	Subject    string  `json:"subject"`
	Step       string  `json:"step"`
	Status     string  `json:"status"`
	ActiveForm *string `json:"activeForm"`
}

type progressEvent struct {
	Event    string          `json:"hook_event_name"`
	Tool     string          `json:"tool_name"`
	Session  string          `json:"session_id"`
	Call     string          `json:"tool_use_id"`
	Agent    string          `json:"agent_id"`
	Input    json.RawMessage `json:"tool_input"`
	Response json.RawMessage `json:"tool_response"`
}

type turnProgress struct {
	active        bool
	sources       map[string][]progressItem
	seen          map[string]bool
	lastPublished time.Time
	lastAttempt   time.Time
	pendingText   string
}

func validProgressItem(item progressItem) bool {
	return item.Content != "" && (item.Status == "pending" || item.Status == "in_progress" || item.Status == "completed")
}

func validProgressFields(raw json.RawMessage, fields ...string) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return false
	}
	for _, field := range fields {
		if value, ok := object[field]; ok {
			var text string
			if string(value) == "null" || json.Unmarshal(value, &text) != nil {
				return false
			}
		}
	}
	return true
}

func (db *DB) reduceProgress(status ipc.TurnStatus) (string, bool) {
	value, exists := db.progress.Load(status.TurnID)
	if len(status.Snapshot) == 0 || string(status.Snapshot) == "null" {
		if exists && value.(*turnProgress).active {
			return "", false
		}
		return status.Text, status.Text != ""
	}
	if status.Source != "claude" && status.Source != "codex" {
		return "", false
	}
	var event progressEvent
	if json.Unmarshal(status.Snapshot, &event) != nil || event.Event != "PostToolUse" || event.Agent != "" || event.Session == "" || event.Call == "" {
		return "", false
	}
	var response struct {
		Success *bool           `json:"success"`
		IsError bool            `json:"isError"`
		Error   json.RawMessage `json:"error"`
		Task    *progressItem   `json:"task"`
		Tasks   []progressItem  `json:"tasks"`
	}
	if len(event.Response) > 0 && string(event.Response) != "null" && event.Response[0] == '{' {
		if json.Unmarshal(event.Response, &response) != nil || response.IsError || (response.Success != nil && !*response.Success) || (len(response.Error) > 0 && string(response.Error) != "null" && string(response.Error) != `""`) {
			return "", false
		}
	}
	var input struct {
		Todos      []progressItem `json:"todos"`
		Plan       []progressItem `json:"plan"`
		TaskID     string         `json:"taskId"`
		Subject    *string        `json:"subject"`
		Status     *string        `json:"status"`
		ActiveForm *string        `json:"activeForm"`
	}
	if json.Unmarshal(event.Input, &input) != nil {
		return "", false
	}
	if !validProgressFields(event.Input, "taskId", "subject", "status", "activeForm") {
		return "", false
	}
	var rawInput struct {
		Todos []json.RawMessage `json:"todos"`
		Plan  []json.RawMessage `json:"plan"`
	}
	if json.Unmarshal(event.Input, &rawInput) != nil {
		return "", false
	}
	for _, raw := range append(rawInput.Todos, rawInput.Plan...) {
		if !validProgressFields(raw, "content", "step", "status", "activeForm") {
			return "", false
		}
	}
	if event.Tool == "TaskGet" || event.Tool == "TaskList" {
		var rawResponse struct {
			Task  json.RawMessage   `json:"task"`
			Tasks []json.RawMessage `json:"tasks"`
		}
		if json.Unmarshal(event.Response, &rawResponse) != nil {
			return "", false
		}
		if event.Tool == "TaskGet" && !validProgressFields(rawResponse.Task, "id", "subject", "status", "activeForm") {
			return "", false
		}
		for _, raw := range rawResponse.Tasks {
			if !validProgressFields(raw, "id", "subject", "status", "activeForm") {
				return "", false
			}
		}
	}
	var items []progressItem
	var replace bool
	switch {
	case status.Source == "claude" && event.Tool == "TodoWrite":
		if input.Todos == nil {
			return "", false
		}
		items, replace = input.Todos, true
	case status.Source == "codex" && event.Tool == "update_plan":
		if input.Plan == nil {
			return "", false
		}
		items, replace = input.Plan, true
		for i := range items {
			items[i].Content = items[i].Step
		}
	case status.Source == "claude" && event.Tool == "TaskCreate":
		if response.Task == nil || response.Task.ID == "" || input.Subject == nil {
			return "", false
		}
		items = []progressItem{{ID: response.Task.ID, Content: *input.Subject, Status: "pending", ActiveForm: input.ActiveForm}}
	case status.Source == "claude" && event.Tool == "TaskGet":
		if response.Task == nil || response.Task.ID == "" {
			return "", false
		}
		items = []progressItem{*response.Task}
		items[0].Content = items[0].Subject
	case status.Source == "claude" && event.Tool == "TaskList":
		if response.Tasks == nil {
			return "", false
		}
		items = response.Tasks
		for i := range items {
			if items[i].ID == "" {
				return "", false
			}
			items[i].Content = items[i].Subject
		}
	case status.Source == "claude" && event.Tool == "TaskUpdate":
		if input.TaskID == "" || response.Success == nil || !*response.Success {
			return "", false
		}
		if input.Subject != nil && *input.Subject == "" {
			return "", false
		}
		if input.Status != nil && *input.Status != "deleted" && *input.Status != "pending" && *input.Status != "in_progress" && *input.Status != "completed" {
			return "", false
		}
	default:
		return "", false
	}
	for _, item := range items {
		if !validProgressItem(item) {
			return "", false
		}
	}
	if !exists {
		value = &turnProgress{sources: make(map[string][]progressItem), seen: make(map[string]bool)}
		db.progress.Store(status.TurnID, value)
	}
	progress := value.(*turnProgress)
	key := status.Source + "\x00" + event.Session + "\x00" + event.Call
	if progress.seen[key] {
		return "", false
	}
	progress.seen[key] = true
	if replace {
		progress.sources[status.Source] = items
	} else {
		list := progress.sources[status.Source]
		if event.Tool == "TaskUpdate" {
			for i, item := range list {
				if item.ID == input.TaskID {
					if input.Status != nil && *input.Status == "deleted" {
						list = append(list[:i], list[i+1:]...)
						break
					}
					if input.Subject != nil {
						list[i].Content = *input.Subject
					}
					if input.Status != nil {
						list[i].Status = *input.Status
					}
					if input.ActiveForm != nil {
						list[i].ActiveForm = input.ActiveForm
					}
					break
				}
			}
		} else {
			for _, item := range items {
				found := false
				for i := range list {
					if list[i].ID == item.ID {
						list[i] = item
						found = true
						break
					}
				}
				if !found {
					list = append(list, item)
				}
			}
		}
		progress.sources[status.Source] = list
	}
	if len(items) > 0 {
		progress.active = true
	}
	if !progress.active {
		return "", false
	}
	return renderChecklist(progress), true
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/chanlib"
)

const maxCIPRs = 10

type ciRun struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
	HTMLURL      string `json:"html_url"`
	PullRequests []struct {
		Number int `json:"number"`
	} `json:"pull_requests"`
}

func (p *payload) ciRun(event string) (*ciRun, bool) {
	switch event {
	case "check_run":
		return p.CheckRun, true
	case "check_suite":
		return p.CheckSuite, true
	case "workflow_run":
		return p.WorkflowRun, true
	}
	return nil, false
}

func (p *payload) ciParts(event string, run *ciRun) (location, string) {
	return location{kind: event, url: run.HTMLURL},
		fmt.Sprintf("%s %s: %s (%s)", event, p.Action, run.Name, run.Conclusion)
}

func (s *server) ciMessages(ctx context.Context, event string, body []byte, base chanlib.InboundMsg) ([]chanlib.InboundMsg, error) {
	messages := []chanlib.InboundMsg{base}
	var p payload
	if _, known := p.ciRun(event); !known {
		return messages, nil
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, err
	}
	run, _ := p.ciRun(event)
	if run == nil {
		return nil, errors.New("CI event without its run")
	}
	if p.Action != "completed" || run.Status != "completed" || s.bot.Login() == "" {
		return messages, nil
	}
	numbers := make([]int, 0, maxCIPRs)
	seen := map[int]bool{}
	for _, pr := range run.PullRequests {
		if pr.Number <= 0 {
			return nil, errors.New("CI association without a positive PR number")
		}
		if !seen[pr.Number] {
			seen[pr.Number] = true
			numbers = append(numbers, pr.Number)
			if len(numbers) > maxCIPRs {
				return nil, errors.New("CI delivery exceeds ten PR associations")
			}
		}
	}
	j := p.repoJID()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for _, number := range numbers {
		url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", s.bot.cfg.APIBase, j.Owner, j.Repo, number)
		raw, err := s.bot.callContext(ctx, http.MethodGet, url, nil, []int{http.StatusOK})
		if err != nil {
			return nil, fmt.Errorf("read CI pull-request assignment: %w", err)
		}
		var pr struct {
			Number    int        `json:"number"`
			Assignees *[]account `json:"assignees"`
			Base      struct {
				Repo struct {
					FullName string `json:"full_name"`
				} `json:"repo"`
			} `json:"base"`
		}
		if err := json.Unmarshal(raw, &pr); err != nil {
			return nil, err
		}
		if pr.Number != number || pr.Assignees == nil || !strings.EqualFold(pr.Base.Repo.FullName, j.Owner+"/"+j.Repo) {
			return nil, errors.New("CI pull-request response does not match the verified repository")
		}
		j.Kind, j.Number = "pr", number
		msg := base
		msg.ID += "-pr" + strconv.Itoa(number)
		loc, body := p.ciParts(event, run)
		msg.Content = loc.render(msg.ID, body)
		msg.ChatJID, msg.ChatName, msg.Topic = j.String(), strings.TrimPrefix(j.String(), "github:"), j.Topic()
		if hasAssignee(s.bot.Login(), *pr.Assignees) {
			msg.Verb = "mention"
		}
		messages = append(messages, msg)
	}
	return messages, nil
}

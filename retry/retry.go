package retry

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Assets contains the browser reader and its policy declaration.
//
//go:embed policy.json retry.mjs
var Assets embed.FS

var policy struct {
	BaseMS               int               `json:"base_ms"`
	MaxMS                int               `json:"max_ms"`
	MaxFailures          int               `json:"max_failures"`
	LatestRetryMS        int64             `json:"latest_retry_ms"`
	TransientStatuses    []int             `json:"transient_statuses"`
	TransientStatusRange [2]int            `json:"transient_status_range"`
	Notices              map[string]string `json:"notices"`
}

func init() {
	data, err := Assets.ReadFile("policy.json")
	if err != nil {
		panic(err)
	}
	if err := json.Unmarshal(data, &policy); err != nil {
		panic(err)
	}
}

type Failure struct {
	Kind    string
	RetryAt time.Time
}

type Error struct {
	Failure Failure
	Err     error
}

func (e *Error) Error() string { return e.Err.Error() }

func (e *Error) Unwrap() error { return e.Err }

func FromError(err error, now time.Time) Failure {
	if errors.Is(err, context.Canceled) {
		return Failure{Kind: "canceled"}
	}
	var fact *Error
	if errors.As(err, &fact) {
		return fact.Failure
	}
	var status interface{ HTTPStatusCode() int }
	var retryAfter interface{ HTTPRetryAfter() string }
	code, after := 0, ""
	if errors.As(err, &status) {
		code = status.HTTPStatusCode()
	}
	if errors.As(err, &retryAfter) {
		after = retryAfter.HTTPRetryAfter()
	}
	return HTTPFailure(code, after, err, now)
}

type Decision struct {
	Failures int
	NextAt   time.Time
	Stop     bool
}

func Next(failures int, failure Failure, now, deadline time.Time) Decision {
	decision := Decision{Failures: failures + 1}
	if (failure.Kind != "transient" && failure.Kind != "timeout") || decision.Failures >= policy.MaxFailures {
		decision.Stop = true
		return decision
	}
	delay := policy.BaseMS
	for i := 1; i < decision.Failures && delay < policy.MaxMS; i++ {
		delay *= 2
		if delay > policy.MaxMS {
			delay = policy.MaxMS
		}
	}
	decision.NextAt = now.Add(time.Duration(delay) * time.Millisecond)
	if failure.RetryAt.After(decision.NextAt) {
		decision.NextAt = failure.RetryAt
	}
	if !deadline.IsZero() && !decision.NextAt.Before(deadline) {
		decision.Stop = true
		decision.NextAt = time.Time{}
	}
	return decision
}

func HTTPFailure(status int, retryAfter string, err error, now time.Time) Failure {
	if errors.Is(err, context.Canceled) {
		return Failure{Kind: "canceled"}
	}
	transient := status >= policy.TransientStatusRange[0] && status <= policy.TransientStatusRange[1]
	for _, code := range policy.TransientStatuses {
		transient = transient || status == code
	}
	if status == 0 {
		dnsError := false
		for pending := []error{err}; len(pending) > 0; {
			current := pending[len(pending)-1]
			pending = pending[:len(pending)-1]
			if current == nil {
				continue
			}
			if networkError, ok := current.(net.Error); ok && networkError.Timeout() {
				return Failure{Kind: "timeout"}
			}
			if _, ok := current.(*net.DNSError); ok {
				dnsError = true
			}
			switch wrapped := current.(type) {
			case interface{ Unwrap() []error }:
				pending = append(pending, wrapped.Unwrap()...)
			case interface{ Unwrap() error }:
				pending = append(pending, wrapped.Unwrap())
			}
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || dnsError || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) {
			transient = true
		}
	}
	if !transient {
		return Failure{Kind: "permanent"}
	}
	failure := Failure{Kind: "transient"}
	value := strings.TrimSpace(retryAfter)
	if value != "" && strings.Trim(value, "0123456789") == "" {
		seconds, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil || seconds > (policy.LatestRetryMS-now.UnixMilli())/1000 {
			failure.RetryAt = time.UnixMilli(policy.LatestRetryMS)
		} else {
			failure.RetryAt = time.Unix(now.Unix()+seconds, int64(now.Nanosecond()))
		}
	} else if date, parseErr := http.ParseTime(retryAfter); parseErr == nil {
		failure.RetryAt = date
	}
	return failure
}

func Wait(ctx context.Context, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	timer := time.NewTimer(time.Until(at))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func Notice(cause string, decision Decision) string {
	key := "retry"
	if decision.Stop {
		key = "terminal"
	}
	return strings.ReplaceAll(policy.Notices[key], "{cause}", cause)
}

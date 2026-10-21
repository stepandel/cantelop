package model

import (
	"errors"
	"regexp"
	"time"
)

const MaxRequestEventPageSize = 500

var (
	ErrInvalidRequestEvent = errors.New("invalid request event")
	requestEventIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
)

// RequestEvent is one completed request handled by the App reverse proxy.
// ReceivedAt records ingress time; status and duration describe the matching
// response. Sequence is assigned by persistence and provides a stable cursor.
type RequestEvent struct {
	Sequence    int64
	EventID     string
	RequestID   string
	WorkspaceID string
	SessionID   string
	ReceivedAt  time.Time
	StatusCode  int
	Duration    time.Duration
}

func (e RequestEvent) Validate() error {
	if e.Sequence < 0 || !requestEventIDPattern.MatchString(e.EventID) || e.ReceivedAt.IsZero() ||
		e.StatusCode < 100 || e.StatusCode > 599 || e.Duration < 0 {
		return ErrInvalidRequestEvent
	}
	if !workspaceIDPattern.MatchString(e.WorkspaceID) ||
		(e.RequestID != "" && !requestEventIDPattern.MatchString(e.RequestID)) ||
		(e.SessionID != "" && ValidateSessionID(e.SessionID) != nil) {
		return ErrInvalidRequestEvent
	}
	return nil
}

type RequestEventPage struct {
	Items              []RequestEvent
	NextBeforeSequence int64
}

// RequestEventQuery is the bounded read contract shared by Runtime and its
// read-only Observer. A page holds the newest Limit matching events, or, with
// AfterSequence, the oldest Limit events after it; either is newest first.
type RequestEventQuery struct {
	WorkspaceID    string
	AppID          string
	BeforeSequence int64
	AfterSequence  int64
	Limit          int
}

func (q RequestEventQuery) Validate() error {
	if q.BeforeSequence < 0 || q.AfterSequence < 0 || (q.BeforeSequence > 0 && q.AfterSequence > 0) ||
		q.Limit < 1 || q.Limit > MaxRequestEventPageSize ||
		(q.WorkspaceID != "" && !workspaceIDPattern.MatchString(q.WorkspaceID)) ||
		(q.AppID != "" && !resourceIDPattern.MatchString(q.AppID)) ||
		(q.WorkspaceID != "" && q.AppID != "") {
		return ErrInvalidRequestEvent
	}
	return nil
}

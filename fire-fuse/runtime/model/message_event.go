package model

import (
	"errors"
	"regexp"
)

const MaxMessageEventPageSize = 500

var (
	ErrInvalidMessageEventQuery    = errors.New("invalid Message event query")
	messageEventMessageIDPattern   = regexp.MustCompile(`^msg_[0-9a-f]{32}$`)
	messageEventWorkspaceIDPattern = regexp.MustCompile(`^wsp_[0-9a-f]{32}$`)
)

// MessageEventQuery is the bounded, payload-free read contract shared by the
// Runtime control API and its read-only host Observer.
type MessageEventQuery struct {
	WorkspaceID    string
	MessageID      string
	BeforeSequence int64
	Limit          int
}

func (query MessageEventQuery) Validate() error {
	if query.BeforeSequence < 0 || query.Limit < 1 || query.Limit > MaxMessageEventPageSize ||
		(query.WorkspaceID != "" && !messageEventWorkspaceIDPattern.MatchString(query.WorkspaceID)) ||
		(query.MessageID != "" && !messageEventMessageIDPattern.MatchString(query.MessageID)) {
		return ErrInvalidMessageEventQuery
	}
	return nil
}

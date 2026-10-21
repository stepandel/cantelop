// Package session contains provider-neutral Session contracts.
package session

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var sandboxIDPattern = regexp.MustCompile(`^sbx-[0-9a-f]{32}$`)

// State describes whether a Session currently has an allocated Sandbox.
// It is derived from Sandbox lifecycle state and is never terminal.
type State string

const (
	StateActive State = "active"
	StateIdle   State = "idle"
)

// Valid reports whether state is a supported Session allocation state.
func (s State) Valid() bool {
	switch s {
	case StateActive, StateIdle:
		return true
	default:
		return false
	}
}

// Startup describes the completed startup of a Session.
type Startup struct {
	CompletedAt time.Time     `json:"completed_at"`
	Duration    time.Duration `json:"duration"`
}

// Session is Cantelop's provider-neutral runtime identity.
type Session struct {
	ID               string    `json:"id"`
	WorkspaceID      string    `json:"workspace_id"`
	BuildID          string    `json:"build_id"`
	KeepAliveSeconds int       `json:"keep_alive_seconds"`
	State            State     `json:"state"`
	Startup          *Startup  `json:"startup,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// WorkLease is an expiring claim that prevents a Session's Sandbox from
// entering its configured idle period while a coordinator owns actor work.
type WorkLease struct {
	SessionID string    `json:"session_id"`
	LeaseID   string    `json:"lease_id"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Endpoint is the private HTTP destination returned by a runtime provider.
// Headers contain provider-owned credentials or routing metadata and must be
// merged under the gateway's platform-owned header policy.
type Endpoint struct {
	Scheme    string      `json:"scheme"`
	Authority string      `json:"authority"`
	Headers   http.Header `json:"headers,omitempty"`
}

// TargetURL validates and constructs the endpoint's HTTP origin URL.
func (e Endpoint) TargetURL() (*url.URL, error) {
	if e.Scheme != "http" && e.Scheme != "https" {
		return nil, errors.New("runtime endpoint scheme must be HTTP or HTTPS")
	}
	if e.Authority == "" || strings.TrimSpace(e.Authority) != e.Authority || strings.ContainsAny(e.Authority, "/?#") {
		return nil, errors.New("runtime endpoint authority is invalid")
	}
	target, err := url.Parse(e.Scheme + "://" + e.Authority)
	if err != nil || target.User != nil || target.Hostname() == "" || target.Host != e.Authority {
		return nil, errors.New("runtime endpoint authority is invalid")
	}
	return target, nil
}

// Route combines a Session with the endpoint selected by its provider.
type Route struct {
	Session   Session  `json:"session"`
	SandboxID string   `json:"sandbox_id"`
	Endpoint  Endpoint `json:"endpoint"`
}

func ValidSandboxID(id string) bool { return sandboxIDPattern.MatchString(id) }

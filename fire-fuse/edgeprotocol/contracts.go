// Package edgeprotocol defines the trusted request metadata exchanged between
// Cantelop's Cloudflare Workers and the Hetzner ingress gateway.
package edgeprotocol

import (
	"errors"
	"strings"

	"github.com/stepandel/cantelop/internal/resourceid"
)

const (
	PublicAppDomain = "cantelop.dev"

	DispatchHeader      = "X-Cantelop-Edge-Dispatch"
	AuthorizationHeader = "X-Cantelop-Edge-Authorization"
	RequestIDHeader     = "X-Cantelop-Edge-Request-ID"
	AppIDHeader         = "X-Cantelop-Edge-App-ID"
	WorkspaceIDHeader   = "X-Cantelop-Edge-Workspace-ID"
	HostnameHeader      = "X-Cantelop-Edge-Hostname"
)

// ReservedHeaders returns the headers removed from public requests before
// platform metadata is attached. Their values are trusted only on the
// broker-to-origin boundary.
func ReservedHeaders() []string {
	return []string{
		DispatchHeader,
		AuthorizationHeader,
		RequestIDHeader,
		AppIDHeader,
		WorkspaceIDHeader,
		HostnameHeader,
	}
}

// APIInvocation is the identity supplied by the public dispatcher to the API
// provider. Workspace selection deliberately does not happen here.
type APIInvocation struct {
	RequestID string `json:"request_id"`
	AppID     string `json:"app_id"`
	Hostname  string `json:"hostname"`
}

// Validate rejects malformed dispatcher metadata. The trusted dispatcher owns
// the App slug-to-ID mapping; this transport contract validates each value's
// shape without duplicating that routing registry.
func (i APIInvocation) Validate() error {
	if !resourceid.ValidRequestID(i.RequestID) {
		return errors.New("invalid edge request ID")
	}
	if !resourceid.ValidAppID(i.AppID) {
		return errors.New("invalid App ID")
	}
	if _, err := AppSlugFromHostname(i.Hostname); err != nil {
		return err
	}
	return nil
}

// DispatchTarget is the complete routing decision supplied by the API provider
// to the transport-only origin broker. Workspace selection travels in the
// request body; Hetzner performs the authoritative App/Workspace check.
type DispatchTarget struct {
	RequestID string `json:"request_id"`
	AppID     string `json:"app_id"`
	Hostname  string `json:"hostname"`
}

// Validate requires well-formed App metadata.
func (t DispatchTarget) Validate() error {
	return APIInvocation(t).Validate()
}

// PublicAppHostname returns the canonical hostname for an App slug.
func PublicAppHostname(appSlug string) (string, error) {
	if !validAppSlug(appSlug) {
		return "", errors.New("invalid App slug")
	}
	return appSlug + "." + PublicAppDomain, nil
}

// AppSlugFromHostname decodes one canonical public App hostname. It rejects
// ports, trailing dots, uppercase aliases, and additional labels.
func AppSlugFromHostname(hostname string) (string, error) {
	suffix := "." + PublicAppDomain
	if strings.TrimSpace(hostname) != hostname || !strings.HasSuffix(hostname, suffix) {
		return "", errors.New("invalid public App hostname")
	}
	label := strings.TrimSuffix(hostname, suffix)
	if !validAppSlug(label) {
		return "", errors.New("invalid public App hostname")
	}
	return label, nil
}

func validAppSlug(slug string) bool {
	return resourceid.ValidSlug(slug)
}

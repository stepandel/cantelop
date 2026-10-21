// Package edgeprotocol defines the trusted request metadata exchanged between
// Cantelop's Cloudflare Workers and the Hetzner ingress gateway.
package edgeprotocol

import (
	"errors"
	"regexp"
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
func (i APIInvocation) Validate(domains ...string) error {
	if !resourceid.ValidRequestID(i.RequestID) {
		return errors.New("invalid edge request ID")
	}
	if !resourceid.ValidAppID(i.AppID) {
		return errors.New("invalid App ID")
	}
	if _, err := AppSlugFromHostname(i.Hostname, domains...); err != nil {
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
func (t DispatchTarget) Validate(domains ...string) error {
	return APIInvocation(t).Validate(domains...)
}

var domainLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidateAppDomain requires one canonical DNS suffix and leaves room for a
// maximum-length App label. Schemes, ports, wildcards, IPs and aliases fail.
func ValidateAppDomain(domain string) error {
	labels := strings.Split(domain, ".")
	if len(domain) > 189 || len(labels) < 2 || (len(labels[len(labels)-1]) == 0 || labels[len(labels)-1][0] < 'a' || labels[len(labels)-1][0] > 'z') {
		return errors.New("invalid public App domain")
	}
	for _, label := range labels {
		if !domainLabel.MatchString(label) {
			return errors.New("invalid public App domain")
		}
	}
	return nil
}

func selectedDomain(domains []string) (string, error) {
	if len(domains) == 0 {
		return PublicAppDomain, nil
	}
	if len(domains) != 1 {
		return "", errors.New("exactly one public App domain is required")
	}
	return domains[0], ValidateAppDomain(domains[0])
}

// PublicAppHostname returns the canonical hostname for an App slug.
func PublicAppHostname(appSlug string, domains ...string) (string, error) {
	if !validAppSlug(appSlug) {
		return "", errors.New("invalid App slug")
	}
	domain, err := selectedDomain(domains)
	if err != nil {
		return "", err
	}
	return appSlug + "." + domain, nil
}

// AppSlugFromHostname decodes one canonical public App hostname. It rejects
// ports, trailing dots, uppercase aliases, and additional labels.
func AppSlugFromHostname(hostname string, domains ...string) (string, error) {
	domain, err := selectedDomain(domains)
	if err != nil {
		return "", err
	}
	suffix := "." + domain
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

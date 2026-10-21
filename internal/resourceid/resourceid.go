// Package resourceid defines the canonical formats of Cantelop resource
// identifiers exchanged between the edge, Console, and Fire Fuse.
package resourceid

import (
	"regexp"
	"strings"
)

var (
	sessionIDPattern = regexp.MustCompile(`^ses_[A-Za-z0-9][A-Za-z0-9_-]{0,59}$`)
	slugPattern      = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
)

// ValidAppID reports whether id is a product-issued App ID.
func ValidAppID(id string) bool { return prefixedHex128(id, "app_") }

// ValidWorkspaceID reports whether id is a product-issued Workspace ID.
func ValidWorkspaceID(id string) bool { return prefixedHex128(id, "wsp_") }

// ValidSandboxID reports whether id is a Fire Fuse Sandbox ID.
func ValidSandboxID(id string) bool { return prefixedHex128(id, "sbx-") }

// ValidRequestID reports whether id is an edge-minted Request ID.
func ValidRequestID(id string) bool { return prefixedHex128(id, "req_") }

// ValidMessageID reports whether id is a Message ID.
func ValidMessageID(id string) bool { return prefixedHex128(id, "msg_") }

// ValidAttemptID reports whether id is a Message delivery attempt ID.
func ValidAttemptID(id string) bool { return prefixedHex128(id, "att_") }

// ValidSessionID reports whether id is a concrete runtime Session ID.
func ValidSessionID(id string) bool { return sessionIDPattern.MatchString(id) }

// ValidSlug reports whether slug is a valid App or Workspace slug: a lowercase
// DNS label without consecutive hyphens.
func ValidSlug(slug string) bool {
	return slugPattern.MatchString(slug) && !strings.Contains(slug, "--")
}

// prefixedHex128 matches prefix followed by 32 lowercase hexadecimal digits.
func prefixedHex128(id, prefix string) bool {
	digits, ok := strings.CutPrefix(id, prefix)
	if !ok || len(digits) != 32 {
		return false
	}
	for index := 0; index < len(digits); index++ {
		if c := digits[index]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

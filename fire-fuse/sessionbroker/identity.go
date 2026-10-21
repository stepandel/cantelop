// Package sessionbroker owns public-to-internal Session identity and lifecycle
// coordination for request brokers on a Fire Fuse host.
package sessionbroker

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrIdentity identifies a failure to generate a new Session resource ID.
var ErrIdentity = errors.New("generate Session identity")

// NewID creates a Session resource ID whose human-readable prefix identifies
// the Workspace without allowing the complete ID to exceed 64 characters.
func NewID(workspaceSlug string) (string, error) {
	return newID(workspaceSlug, rand.Reader)
}

func newID(workspaceSlug string, entropy io.Reader) (string, error) {
	var suffix [8]byte
	if _, err := io.ReadFull(entropy, suffix[:]); err != nil {
		return "", errors.Join(ErrIdentity, fmt.Errorf("generate Session ID: %w", err))
	}
	const suffixLength = 21
	if len(workspaceSlug) > 64-suffixLength {
		workspaceSlug = strings.TrimRight(workspaceSlug[:64-suffixLength], "-")
	}
	return "ses_" + workspaceSlug + "_" + hex.EncodeToString(suffix[:]), nil
}

// KeyHash scopes an application Session key to one Workspace.
func KeyHash(workspaceID, key string) string {
	digest := sha256.Sum256([]byte(workspaceID + "\x00" + key))
	return hex.EncodeToString(digest[:])
}

// LogicalID maps an application Session identity to its stable,
// App-scoped internal Session resource ID.
func LogicalID(appID, logicalID string) string {
	digest := sha256.Sum256([]byte(appID + "\x00" + logicalID))
	return "ses_" + hex.EncodeToString(digest[:16])
}

// LogicalKeyHash scopes a logical identity to one Workspace while
// keeping it distinct from legacy keyed Session bindings.
func LogicalKeyHash(workspaceID, logicalID string) string {
	digest := sha256.Sum256([]byte("v4\x00" + workspaceID + "\x00" + logicalID))
	return hex.EncodeToString(digest[:])
}

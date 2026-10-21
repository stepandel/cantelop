// Package syncprotocol defines the internal wire contract behind workspace.sync().
package syncprotocol

import (
	"path"
	"strings"
	"unicode/utf8"
)

const (
	MaxFileBytes = 16 << 20
	MaxEntries   = 10000
	MaxScanBytes = 128 << 20
	// MaxManifestBytes bounds an encoded scan: MaxEntries paths of up to 1024
	// bytes, each byte escaping to at most six JSON bytes, plus entry fields.
	MaxManifestBytes = 64 << 20
	TempPrefix       = ".cantelop-sync-"
)

type Entry struct {
	Path       string `json:"path"`
	Version    string `json:"version"`
	Size       int64  `json:"size"`
	Executable bool   `json:"executable"`
}

func ValidPath(p string) bool {
	if p == "" || !utf8.ValidString(p) || len(p) > 1024 || path.Clean(p) != p || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00\r\n") {
		return false
	}
	parts := strings.Split(p, "/")
	if len(parts) > 32 {
		return false
	}
	for _, part := range parts {
		// Paths may be published on a case-insensitive local filesystem.
		folded := strings.ToLower(part)
		if part == "." || part == ".." || strings.HasPrefix(folded, TempPrefix) || folded == ".git" || folded == "node_modules" || folded == ".cantelop" || folded == ".env" || strings.HasPrefix(folded, ".env.") {
			return false
		}
	}
	return true
}

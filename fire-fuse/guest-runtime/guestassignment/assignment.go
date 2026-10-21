// Package guestassignment defines the one-shot contract used to assign work to
// a booted Fire Fuse snapshot guest.
package guestassignment

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"path"
	"regexp"
	"strings"

	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestconfig"
)

const (
	SchemaVersion    = 2
	maxDocumentBytes = 1 << 20
	maxIDBytes       = 64
	minClockUnixNano = int64(1577836800) * 1_000_000_000
	maxClockUnixNano = int64(4102444800) * 1_000_000_000
)

var (
	ErrInvalid = errors.New("invalid guest assignment")
	safeID     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
)

// Assignment contains all request-specific state installed after a generic
// snapshot has been restored.
type Assignment struct {
	SchemaVersion int                `json:"schema_version"`
	AssignmentID  string             `json:"assignment_id"`
	ClockUnixNano int64              `json:"clock_unix_nano"`
	Network       Network            `json:"network"`
	Workload      guestconfig.Config `json:"workload"`
	Workspace     Workspace          `json:"workspace"`
}

// Network describes the unique point-to-point interface assigned to the VM.
type Network struct {
	Address string `json:"address"`
	Gateway string `json:"gateway"`
	Prefix  int    `json:"prefix"`
}

// Workspace contains the host NFS export coordinates. It is empty when the
// workload does not request a workspace.
type Workspace struct {
	Enabled    bool   `json:"enabled"`
	ID         string `json:"id"`
	Server     string `json:"server"`
	ExportPath string `json:"export_path"`
}

// Decode strictly reads and validates one assignment.
func Decode(reader io.Reader) (Assignment, error) {
	if reader == nil {
		return Assignment{}, invalid("document reader is required")
	}
	document, err := io.ReadAll(io.LimitReader(reader, maxDocumentBytes+1))
	if err != nil {
		return Assignment{}, fmt.Errorf("%w: read document: %v", ErrInvalid, err)
	}
	if len(document) > maxDocumentBytes {
		return Assignment{}, invalid("document exceeds 1 MiB")
	}

	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var assignment Assignment
	if err := decoder.Decode(&assignment); err != nil {
		return Assignment{}, fmt.Errorf("%w: decode JSON: %v", ErrInvalid, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Assignment{}, invalid("document must contain exactly one JSON object")
	}
	if err := assignment.Validate(); err != nil {
		return Assignment{}, err
	}
	return assignment, nil
}

// Validate rejects assignments that would require the guest to infer network,
// process, or mount behavior.
func (a Assignment) Validate() error {
	if a.SchemaVersion != SchemaVersion {
		return invalid(fmt.Sprintf("schema_version must be %d", SchemaVersion))
	}
	if len(a.AssignmentID) > maxIDBytes || !safeID.MatchString(a.AssignmentID) {
		return invalid("assignment_id must be a safe identifier of at most 64 bytes")
	}
	if a.ClockUnixNano < minClockUnixNano || a.ClockUnixNano >= maxClockUnixNano {
		return invalid("clock_unix_nano must be between 2020 and 2100")
	}
	if err := a.Network.validate(); err != nil {
		return err
	}
	if err := a.Workload.Validate(); err != nil {
		return fmt.Errorf("%w: workload: %v", ErrInvalid, err)
	}
	if a.Workload.Workspace.Enabled != a.Workspace.Enabled {
		return invalid("workload and workspace enabled values must match")
	}
	if err := a.Workspace.validate(); err != nil {
		return err
	}
	return nil
}

func (n Network) validate() error {
	address, err := netip.ParseAddr(n.Address)
	if err != nil || !address.Is4() || address.IsUnspecified() || address.IsMulticast() {
		return invalid("network address must be a usable IPv4 address")
	}
	gateway, err := netip.ParseAddr(n.Gateway)
	if err != nil || !gateway.Is4() || gateway.IsUnspecified() || gateway.IsMulticast() {
		return invalid("network gateway must be a usable IPv4 address")
	}
	if n.Prefix < 1 || n.Prefix > 32 {
		return invalid("network prefix must be between 1 and 32")
	}
	prefix := netip.PrefixFrom(address, n.Prefix).Masked()
	if !prefix.Contains(gateway) || gateway == address {
		return invalid("network gateway must be a different address in the assigned subnet")
	}
	return nil
}

func (w Workspace) validate() error {
	if !w.Enabled {
		if w.ID != "" || w.Server != "" || w.ExportPath != "" {
			return invalid("disabled workspace coordinates must be empty")
		}
		return nil
	}
	if !safeID.MatchString(w.ID) {
		return invalid("workspace id must be a safe identifier of at most 64 bytes")
	}
	server, err := netip.ParseAddr(w.Server)
	if err != nil || !server.Is4() || !server.IsPrivate() {
		return invalid("workspace server must be a private IPv4 address")
	}
	if !path.IsAbs(w.ExportPath) || path.Clean(w.ExportPath) != w.ExportPath ||
		w.ExportPath == "/" || strings.ContainsAny(w.ExportPath, " \t\r\n:") {
		return invalid("workspace export_path must be a clean absolute path without whitespace or colons")
	}
	if path.Base(w.ExportPath) != w.ID {
		return invalid("workspace export_path must end with the workspace id")
	}
	return nil
}

func invalid(detail string) error {
	return fmt.Errorf("%w: %s", ErrInvalid, detail)
}

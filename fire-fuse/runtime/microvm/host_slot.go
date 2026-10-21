package microvm

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	ErrHostSlotNotFound    = errors.New("host slot not found")
	ErrInvalidHostSlot     = errors.New("invalid host slot")
	hostSlotProfilePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

type HostSlotState string

const (
	HostSlotPreparing HostSlotState = "preparing"
	HostSlotReady     HostSlotState = "ready"
	HostSlotDraining  HostSlotState = "draining"
)

// HostSlot is the durable owner of request-independent host resources before
// they are atomically transferred to a tenant microVM.
type HostSlot struct {
	ID        string           `json:"id"`
	Profile   string           `json:"profile"`
	State     HostSlotState    `json:"state"`
	Runtime   RuntimeOwnership `json:"runtime"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
}

func (s HostSlot) Validate() error {
	if !hostSlotIDPattern.MatchString(s.ID) || !hostSlotProfilePattern.MatchString(s.Profile) {
		return ErrInvalidHostSlot
	}
	if s.State != HostSlotPreparing && s.State != HostSlotReady && s.State != HostSlotDraining {
		return ErrInvalidHostSlot
	}
	if s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() || s.UpdatedAt.Before(s.CreatedAt) {
		return ErrInvalidHostSlot
	}
	if s.Runtime != (RuntimeOwnership{}) {
		if err := s.Runtime.Validate(); err != nil || s.Runtime.HostSlotID != s.ID {
			return errors.Join(ErrInvalidHostSlot, err)
		}
	}
	if s.State == HostSlotReady && !isReusableHostRuntime(s.Runtime) {
		return fmt.Errorf("%w: ready slot has incomplete resources", ErrInvalidHostSlot)
	}
	return nil
}

func isReusableHostRuntime(runtime RuntimeOwnership) bool {
	return runtime.HostSlotID != "" && runtime.JailPrepared && runtime.AddressAllocated &&
		runtime.TAPCreated && runtime.ForwardingApplied && !runtime.WorkspaceExported &&
		!runtime.WorkspaceNFSAllowed && runtime.Process == (ProcessIdentity{}) &&
		strings.TrimSpace(runtime.TAPName) != ""
}

// matchesHostSlotClaim permits the atomic transfer to add the sandbox's
// deterministic cgroup claim and, when needed, its paired workspace setup
// intent. The reusable slot owns neither request-specific resource. Recording
// both intents during transfer avoids later transactions while recovery can
// safely remove resources that startup had not created yet.
func matchesHostSlotClaim(slot, claimed RuntimeOwnership) bool {
	if slot == claimed {
		return true
	}
	if slot.CgroupCreated || !claimed.CgroupCreated ||
		slot.WorkspaceExported || slot.WorkspaceNFSAllowed ||
		claimed.WorkspaceExported != claimed.WorkspaceNFSAllowed {
		return false
	}
	claimed.CgroupCreated = false
	claimed.WorkspaceExported = false
	claimed.WorkspaceNFSAllowed = false
	return slot == claimed
}

// HostSlotStore persists prepared resources and transfers their ownership.
type HostSlotStore interface {
	CreateHostSlot(HostSlot) error
	CheckpointHostSlot(string, RuntimeOwnership, time.Time) (HostSlot, error)
	ReadyHostSlot(string, time.Time) (HostSlot, error)
	DrainHostSlot(string, time.Time) (HostSlot, error)
	ListHostSlots() ([]HostSlot, error)
	DeleteHostSlot(string) error
	ClaimHostSlot(string, string, RuntimeOwnership, time.Time) (Sandbox, error)
}

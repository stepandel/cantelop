package microvm

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
)

var ErrInvalidRuntimeOwnership = errors.New("invalid runtime ownership")

var (
	bootIDPattern     = regexp.MustCompile(`^[[:xdigit:]]{8}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{12}$`)
	tapNamePattern    = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)
	hostSlotIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)
)

// ProcessIdentity identifies one Linux process without relying on its reusable
// numeric PID alone. StartTimeTicks is field 22 from /proc/PID/stat.
type ProcessIdentity struct {
	PID            int    `json:"pid"`
	BootID         string `json:"boot_id"`
	StartTimeTicks uint64 `json:"start_time_ticks"`
}

// RuntimeOwnership records which host resources fire-fuse has acquired for a
// microVM. Recovery can safely unwind a partially completed startup by walking
// these flags in reverse order. Paths are deliberately omitted because they are
// derived from trusted host configuration and either the validated host slot
// ID or, when the prepared host pool is disabled, the Sandbox ID.
type RuntimeOwnership struct {
	// HostSlotID identifies reusable host resources independently from the
	// Sandbox ID. It is empty for direct, non-pooled runtimes whose layout and
	// network identity are derived from the Sandbox ID.
	HostSlotID          string            `json:"host_slot_id,omitempty"`
	JailPrepared        bool              `json:"jail_prepared"`
	CgroupCreated       bool              `json:"cgroup_created"`
	AddressAllocated    bool              `json:"address_allocated"`
	TAPCreated          bool              `json:"tap_created"`
	ForwardingApplied   bool              `json:"forwarding_applied"`
	WorkspaceExported   bool              `json:"workspace_exported"`
	WorkspaceNFSAllowed bool              `json:"workspace_nfs_allowed"`
	TAPName             string            `json:"tap_name"`
	Network             NetworkAssignment `json:"network"`
	Process             ProcessIdentity   `json:"process"`
}

// Validate rejects incomplete or contradictory ownership records before they
// are used for recovery.
func (o RuntimeOwnership) Validate() error {
	if o == (RuntimeOwnership{}) {
		return nil
	}
	if !o.JailPrepared {
		return fmt.Errorf("%w: acquired resources require a prepared jail", ErrInvalidRuntimeOwnership)
	}
	if o.HostSlotID != "" && !hostSlotIDPattern.MatchString(o.HostSlotID) {
		return fmt.Errorf("%w: invalid host slot ID", ErrInvalidRuntimeOwnership)
	}
	if o.AddressAllocated {
		if err := validateOwnedNetwork(o.Network); err != nil {
			return err
		}
	} else if o.Network != (NetworkAssignment{}) {
		return fmt.Errorf("%w: network assignment requires address ownership", ErrInvalidRuntimeOwnership)
	}
	if o.TAPCreated {
		if !o.AddressAllocated {
			return fmt.Errorf("%w: TAP ownership requires address ownership", ErrInvalidRuntimeOwnership)
		}
		if !tapNamePattern.MatchString(o.TAPName) {
			return fmt.Errorf("%w: invalid TAP name", ErrInvalidRuntimeOwnership)
		}
	} else if o.TAPName != "" {
		return fmt.Errorf("%w: TAP name requires TAP ownership", ErrInvalidRuntimeOwnership)
	}
	if o.ForwardingApplied && !o.TAPCreated {
		return fmt.Errorf("%w: forwarding ownership requires TAP ownership", ErrInvalidRuntimeOwnership)
	}
	if o.WorkspaceExported && !o.ForwardingApplied {
		return fmt.Errorf(
			"%w: workspace export ownership requires forwarding ownership",
			ErrInvalidRuntimeOwnership,
		)
	}
	if o.WorkspaceNFSAllowed && !o.WorkspaceExported {
		return fmt.Errorf(
			"%w: workspace NFS access requires workspace export ownership",
			ErrInvalidRuntimeOwnership,
		)
	}
	if err := o.Process.Validate(); err != nil {
		return err
	}
	return nil
}

// Validate rejects incomplete identities that could mistake a reused PID for
// the originally supervised process.
func (p ProcessIdentity) Validate() error {
	if p == (ProcessIdentity{}) {
		return nil
	}
	if p.PID < 1 || p.StartTimeTicks == 0 || !bootIDPattern.MatchString(p.BootID) {
		return fmt.Errorf("%w: incomplete process identity", ErrInvalidRuntimeOwnership)
	}
	return nil
}

func validateOwnedNetwork(assignment NetworkAssignment) error {
	guestIP, guestErr := netip.ParseAddr(assignment.GuestIP)
	gatewayIP, gatewayErr := netip.ParseAddr(assignment.GatewayIP)
	subnet, subnetErr := netip.ParsePrefix(assignment.Subnet)
	mac, macErr := net.ParseMAC(assignment.GuestMAC)
	if guestErr != nil || gatewayErr != nil || subnetErr != nil || macErr != nil ||
		!guestIP.Is4() || !gatewayIP.Is4() || !subnet.Addr().Is4() || subnet.Bits() != 30 ||
		subnet != subnet.Masked() || !subnet.Addr().IsPrivate() ||
		gatewayIP != subnet.Addr().Next() || guestIP != gatewayIP.Next() ||
		len(mac) != 6 || mac[0]&0x01 != 0 || mac[0]&0x02 == 0 {
		return fmt.Errorf("%w: invalid network assignment", ErrInvalidRuntimeOwnership)
	}
	return nil
}

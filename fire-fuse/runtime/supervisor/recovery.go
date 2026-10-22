package supervisor

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/host/firecracker"
	"github.com/stepandel/cantelop/fire-fuse/host/network"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

// CheckForOrphans identifies resources in fire-fuse's reserved namespaces that
// have no durable owner. It never mutates or deletes the discovered resources.
func (s *Supervisor) CheckForOrphans(machines []microvm.Sandbox) error {
	if s.deps.readJailRoot == nil || s.deps.listInterfaces == nil ||
		s.deps.readTAPAlias == nil || s.deps.listFirewall == nil {
		return errors.New("inspect orphaned resources: inventory dependencies are unavailable")
	}

	expectedJails := make(map[string]struct{})
	expectedTAPs := make(map[string]string)
	expectedFirewall := make(map[string]struct{})
	// The snapshot builder may run while the service is live; its template TAP
	// is owned by that build, not by a persisted Sandbox.
	if tapName, err := network.TAPNameInNamespace(network.SnapshotTemplateVMID, s.config.Network.Namespace); err == nil {
		if alias, err := network.TAPAliasInNamespace(network.SnapshotTemplateVMID, s.config.Network.Namespace); err == nil {
			expectedTAPs[tapName] = alias
		}
	}
	jailRoot := filepath.Join(s.config.RuntimeDir, filepath.Base(s.config.FirecrackerBin))
	for _, machine := range machines {
		if err := machine.Runtime.Validate(); err != nil {
			return fmt.Errorf("validate runtime ownership for orphan detection: %w", err)
		}
		hostID := runtimeHostID(machine.ID, machine.Runtime)
		if machine.Runtime.JailPrepared {
			if err := firecracker.ValidateVMID(hostID); err != nil {
				return fmt.Errorf("derive owned jail for orphan detection: %w", err)
			}
			expectedJails[hostID] = struct{}{}
		}
		if machine.Runtime.TAPCreated {
			tapName, err := network.TAPNameInNamespace(hostID, s.config.Network.Namespace)
			if err != nil {
				return fmt.Errorf("derive owned TAP name for orphan detection: %w", err)
			}
			if machine.Runtime.TAPName != tapName {
				return fmt.Errorf(
					"validate TAP ownership for orphan detection: persisted name %q does not match %q",
					machine.Runtime.TAPName,
					tapName,
				)
			}
			alias, err := network.TAPAliasInNamespace(hostID, s.config.Network.Namespace)
			if err != nil {
				return fmt.Errorf("derive owned TAP alias for orphan detection: %w", err)
			}
			expectedTAPs[tapName] = alias
		}
		if machine.Runtime.ForwardingApplied {
			expectedFirewall[network.FirewallOwnershipComment(machine.Runtime.TAPName)] = struct{}{}
		}
		if machine.Runtime.WorkspaceNFSAllowed {
			comment := network.WorkspaceNFSFirewallOwnershipComment(
				machine.Runtime.TAPName,
			)
			expectedFirewall[comment] = struct{}{}
		}
	}

	var orphanJails []string
	entries, err := s.deps.readJailRoot(jailRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inventory fire-fuse jail root: %w", err)
	}
	for _, entry := range entries {
		_, expected := expectedJails[entry.Name()]
		safeDirectory := entry.IsDir() && entry.Type()&os.ModeSymlink == 0
		validManifest := false
		if safeDirectory {
			layout, layoutErr := firecracker.NewJailLayout(
				s.config.RuntimeDir,
				s.config.FirecrackerBin,
				entry.Name(),
			)
			validManifest = layoutErr == nil && layout.ValidateRuntimeManifest() == nil
		}
		if !expected || !safeDirectory || !validManifest {
			orphanJails = append(orphanJails, filepath.Join(jailRoot, entry.Name()))
		}
	}

	interfaces, err := s.deps.listInterfaces()
	if err != nil {
		return fmt.Errorf("inventory host network interfaces: %w", err)
	}
	var orphanTAPs []string
	for _, hostInterface := range interfaces {
		if !network.IsTAPNameInNamespace(hostInterface.Name, s.config.Network.Namespace) {
			continue
		}
		alias, err := s.deps.readTAPAlias(hostInterface.Name)
		if err != nil {
			return fmt.Errorf("inventory TAP %s ownership alias: %w", hostInterface.Name, err)
		}
		expectedAlias, expected := expectedTAPs[hostInterface.Name]
		if expected && alias == expectedAlias {
			continue
		}
		classification := "ambiguous"
		if !expected && network.ValidateTAPOwnership(hostInterface.Name, alias) == nil {
			classification = "owned-orphan"
		}
		orphanTAPs = append(orphanTAPs, fmt.Sprintf("%s(alias=%q,%s)", hostInterface.Name, alias, classification))
	}
	firewallRules, err := s.deps.listFirewall(context.Background())
	if err != nil {
		return fmt.Errorf("inventory fire-fuse firewall rules: %w", err)
	}
	var orphanFirewall []string
	for _, rule := range firewallRules {
		if _, expected := expectedFirewall[rule.Comment]; !expected {
			orphanFirewall = append(orphanFirewall, fmt.Sprintf("%s/%s:%s", rule.Table, rule.Chain, rule.Comment))
		}
	}

	if len(orphanJails) == 0 && len(orphanTAPs) == 0 && len(orphanFirewall) == 0 {
		return nil
	}
	sort.Strings(orphanJails)
	sort.Strings(orphanTAPs)
	sort.Strings(orphanFirewall)
	return fmt.Errorf(
		"%w: jail paths [%s]; TAP names [%s]; firewall rules [%s]",
		ErrOrphanedResources,
		strings.Join(orphanJails, ", "),
		strings.Join(orphanTAPs, ", "),
		strings.Join(orphanFirewall, ", "),
	)
}

// Recover safely releases host resources recorded by a previous fire-fuse
// process. The caller must first move the persisted lifecycle state to stopping.
func (s *Supervisor) Recover(
	ctx context.Context,
	machine microvm.Sandbox,
	checkpoint microvm.RuntimeCheckpoint,
) error {
	ownership := machine.Runtime
	if ownership == (microvm.RuntimeOwnership{}) {
		return nil
	}
	if machine.State != microvm.StateStopping {
		return fmt.Errorf("recover runtime ownership: machine state is %s, want stopping", machine.State)
	}
	if err := ownership.Validate(); err != nil {
		return fmt.Errorf("validate recovered runtime ownership: %w", err)
	}

	hostID := runtimeHostID(machine.ID, ownership)
	layout, err := s.deps.newLayout(s.config.RuntimeDir, s.config.FirecrackerBin, hostID)
	if err != nil {
		return fmt.Errorf("derive recovered Firecracker jail layout: %w", err)
	}
	owned := &instance{
		hostSlotID:          ownership.HostSlotID,
		layout:              layout,
		jailPrepared:        ownership.JailPrepared,
		cgroupCreated:       ownership.CgroupCreated,
		workspaceID:         machine.WorkspaceID,
		workspaceExported:   ownership.WorkspaceExported,
		workspaceNFSAllowed: ownership.WorkspaceNFSAllowed,
		processIdentity:     ownership.Process,
	}
	if ownership.AddressAllocated {
		if s.deps.network == nil {
			return errors.New("recover network ownership: network dependencies are unavailable")
		}
		allocation, err := recoveredNetworkAllocation(ownership.Network, s.config.Network.AddressPool)
		if err != nil {
			return err
		}
		if ownership.TAPCreated {
			expectedTAPName, err := network.TAPNameInNamespace(hostID, s.config.Network.Namespace)
			if err != nil {
				return fmt.Errorf("derive recovered TAP name: %w", err)
			}
			if ownership.TAPName != expectedTAPName {
				return fmt.Errorf(
					"recover TAP ownership: persisted name %q does not match derived name %q",
					ownership.TAPName,
					expectedTAPName,
				)
			}
		}
		owned.network = &instanceNetwork{
			addressAllocated: ownership.AddressAllocated,
			tapOwned:         ownership.TAPCreated,
			forwardingOwned:  ownership.ForwardingApplied,
			tapName:          ownership.TAPName,
			allocation:       allocation,
		}
	}

	if owned.processIdentity != (microvm.ProcessIdentity{}) {
		if s.deps.terminateProcess == nil {
			return errors.New("recover Firecracker process: pidfd termination is unavailable")
		}
		if _, err := s.deps.terminateProcess(
			ctx,
			owned.processIdentity,
			time.Duration(s.config.Timeouts.ShutdownGrace),
			time.Duration(s.config.Timeouts.ShutdownForce),
		); err != nil {
			return fmt.Errorf("terminate recovered Firecracker process: %w", err)
		}
		identity := owned.processIdentity
		owned.processIdentity = microvm.ProcessIdentity{}
		if err := checkpointOwnership(checkpoint, owned, "recovering the Firecracker process"); err != nil {
			owned.processIdentity = identity
			return err
		}
	}

	if err := s.cleanupInstance(ctx, machine.ID, owned, checkpoint); err != nil {
		return fmt.Errorf("clean up recovered Firecracker runtime: %w", err)
	}
	return nil
}

// RecoverHostSlot releases durable resources left by an interrupted pool.
func (s *Supervisor) RecoverHostSlot(
	ctx context.Context,
	slot microvm.HostSlot,
	checkpoint microvm.RuntimeCheckpoint,
) error {
	return s.Recover(ctx, microvm.Sandbox{
		ID:      slot.ID,
		State:   microvm.StateStopping,
		Runtime: slot.Runtime,
	}, checkpoint)
}

func recoveredNetworkAllocation(
	assignment microvm.NetworkAssignment,
	configuredPool string,
) (network.Allocation, error) {
	subnet, subnetErr := netip.ParsePrefix(assignment.Subnet)
	hostIP, hostErr := netip.ParseAddr(assignment.GatewayIP)
	guestIP, guestErr := netip.ParseAddr(assignment.GuestIP)
	if subnetErr != nil || hostErr != nil || guestErr != nil {
		return network.Allocation{}, errors.New("parse recovered network assignment")
	}
	pool, err := netip.ParsePrefix(configuredPool)
	if err != nil {
		return network.Allocation{}, fmt.Errorf("parse configured network pool for recovery: %w", err)
	}
	lastAddress := guestIP.Next()
	if !pool.Contains(subnet.Addr()) || !pool.Contains(lastAddress) {
		return network.Allocation{}, fmt.Errorf(
			"recover network assignment: subnet %s is outside configured pool %s",
			subnet,
			pool,
		)
	}
	return network.Allocation{
		Subnet:   subnet,
		HostIP:   hostIP,
		GuestIP:  guestIP,
		GuestMAC: assignment.GuestMAC,
	}, nil
}

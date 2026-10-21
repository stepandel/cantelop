package supervisor

import (
	"errors"
	"fmt"

	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

func runtimeInfo(owned *instance) microvm.RuntimeInfo {
	if owned == nil {
		return microvm.RuntimeInfo{}
	}
	return microvm.RuntimeInfo{
		Network:   networkAssignment(owned.network),
		Ownership: runtimeOwnership(owned),
	}
}

func runtimeOwnership(owned *instance) microvm.RuntimeOwnership {
	if owned == nil {
		return microvm.RuntimeOwnership{}
	}
	ownership := microvm.RuntimeOwnership{
		HostSlotID:          owned.hostSlotID,
		JailPrepared:        owned.jailPrepared,
		CgroupCreated:       owned.cgroupCreated,
		WorkspaceExported:   owned.workspaceExported,
		WorkspaceNFSAllowed: owned.workspaceNFSAllowed,
		Process:             owned.processIdentity,
	}
	if owned.network != nil {
		ownership.AddressAllocated = owned.network.addressAllocated
		ownership.TAPCreated = owned.network.tapOwned
		ownership.ForwardingApplied = owned.network.forwardingOwned
		ownership.TAPName = owned.network.tapName
		if owned.network.addressAllocated {
			ownership.Network = networkAssignment(owned.network)
		}
	}
	return ownership
}

func runtimeHostID(machineID string, ownership microvm.RuntimeOwnership) string {
	if ownership.HostSlotID != "" {
		return ownership.HostSlotID
	}
	return machineID
}

func networkAssignment(owned *instanceNetwork) microvm.NetworkAssignment {
	if owned == nil || !owned.addressAllocated {
		return microvm.NetworkAssignment{}
	}
	allocation := owned.allocation
	return microvm.NetworkAssignment{
		GuestIP:   allocation.GuestIP.String(),
		GatewayIP: allocation.HostIP.String(),
		Subnet:    allocation.Subnet.String(),
		GuestMAC:  allocation.GuestMAC,
	}
}

func checkpointOwnership(
	checkpoint microvm.RuntimeCheckpoint,
	owned *instance,
	action string,
) error {
	if process, ok := owned.process.(*timedProcess); ok {
		if _, err := process.persist(); err != nil {
			return err
		}
	}
	if checkpoint == nil {
		return errors.New("checkpoint runtime ownership: callback is nil")
	}
	if err := checkpoint(runtimeOwnership(owned)); err != nil {
		return fmt.Errorf("checkpoint runtime ownership after %s: %w", action, err)
	}
	return nil
}

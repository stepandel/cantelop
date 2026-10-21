package microvm

import (
	"errors"
	"testing"
)

func TestRuntimeOwnershipValidate(t *testing.T) {
	network := NetworkAssignment{
		GuestIP:   "172.16.0.2",
		GatewayIP: "172.16.0.1",
		Subnet:    "172.16.0.0/30",
		GuestMAC:  "02:fc:ac:10:00:02",
	}
	process := ProcessIdentity{
		PID:            123,
		BootID:         "01234567-89ab-cdef-0123-456789abcdef",
		StartTimeTicks: 456,
	}

	tests := []struct {
		name      string
		ownership RuntimeOwnership
		wantErr   bool
	}{
		{name: "empty record"},
		{name: "prepared jail", ownership: RuntimeOwnership{JailPrepared: true}},
		{name: "prepared host slot", ownership: RuntimeOwnership{HostSlotID: "slot-01", JailPrepared: true}},
		{name: "host slot without resources", ownership: RuntimeOwnership{HostSlotID: "slot-01"}, wantErr: true},
		{name: "invalid host slot ID", ownership: RuntimeOwnership{HostSlotID: "slot/01", JailPrepared: true}, wantErr: true},
		{name: "complete runtime", ownership: RuntimeOwnership{
			JailPrepared:        true,
			AddressAllocated:    true,
			TAPCreated:          true,
			ForwardingApplied:   true,
			WorkspaceExported:   true,
			WorkspaceNFSAllowed: true,
			TAPName:             "ff0123456789abc",
			Network:             network,
			Process:             process,
		}},
		{name: "resource without jail", ownership: RuntimeOwnership{AddressAllocated: true, Network: network}, wantErr: true},
		{name: "assignment without ownership", ownership: RuntimeOwnership{JailPrepared: true, Network: network}, wantErr: true},
		{name: "invalid assignment", ownership: RuntimeOwnership{JailPrepared: true, AddressAllocated: true}, wantErr: true},
		{name: "noncanonical subnet", ownership: RuntimeOwnership{JailPrepared: true, AddressAllocated: true, Network: NetworkAssignment{
			GuestIP: "172.16.0.2", GatewayIP: "172.16.0.1", Subnet: "172.16.0.1/30", GuestMAC: "02:fc:ac:10:00:02",
		}}, wantErr: true},
		{name: "unexpected guest position", ownership: RuntimeOwnership{JailPrepared: true, AddressAllocated: true, Network: NetworkAssignment{
			GuestIP: "172.16.0.1", GatewayIP: "172.16.0.2", Subnet: "172.16.0.0/30", GuestMAC: "02:fc:ac:10:00:02",
		}}, wantErr: true},
		{name: "TAP without address", ownership: RuntimeOwnership{JailPrepared: true, TAPCreated: true, TAPName: "ff0123456789abc"}, wantErr: true},
		{name: "invalid TAP name", ownership: RuntimeOwnership{JailPrepared: true, AddressAllocated: true, TAPCreated: true, TAPName: "tap name", Network: network}, wantErr: true},
		{name: "TAP name without ownership", ownership: RuntimeOwnership{JailPrepared: true, TAPName: "ff0123456789abc"}, wantErr: true},
		{name: "forwarding without TAP", ownership: RuntimeOwnership{JailPrepared: true, ForwardingApplied: true}, wantErr: true},
		{name: "workspace export without forwarding", ownership: RuntimeOwnership{
			JailPrepared: true, WorkspaceExported: true,
		}, wantErr: true},
		{name: "workspace NFS access without export", ownership: RuntimeOwnership{
			JailPrepared: true, WorkspaceNFSAllowed: true,
		}, wantErr: true},
		{name: "partial process identity", ownership: RuntimeOwnership{JailPrepared: true, Process: ProcessIdentity{PID: 123}}, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.ownership.Validate()
			if test.wantErr && !errors.Is(err, ErrInvalidRuntimeOwnership) {
				t.Fatalf("Validate() error = %v, want ErrInvalidRuntimeOwnership", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

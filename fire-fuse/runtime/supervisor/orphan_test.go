package supervisor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stepandel/cantelop/fire-fuse/host/network"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

func TestSupervisorDetectsOrphanedJailsAndTAPs(t *testing.T) {
	cfg := testConfig()
	runtimeDir, err := os.MkdirTemp("/tmp", "fire-fuse-orphans-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	cfg.RuntimeDir = runtimeDir
	jailRoot := filepath.Join(cfg.RuntimeDir, filepath.Base(cfg.FirecrackerBin))
	for _, id := range []string{"vm-known", "vm-orphan", "vm-ambiguous"} {
		if err := os.MkdirAll(filepath.Join(jailRoot, id), 0o700); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", id, err)
		}
	}
	writeRuntimeManifest(t, filepath.Join(jailRoot, "vm-known"), "vm-known")
	writeRuntimeManifest(t, filepath.Join(jailRoot, "vm-orphan"), "vm-orphan")
	knownTAP, err := network.TAPName("vm-known")
	if err != nil {
		t.Fatalf("known TAPName() error = %v", err)
	}
	orphanTAP, err := network.TAPName("vm-orphan")
	if err != nil {
		t.Fatalf("orphan TAPName() error = %v", err)
	}
	supervisor := newSupervisor(cfg, dependencies{
		readJailRoot: os.ReadDir,
		listInterfaces: func() ([]net.Interface, error) {
			return []net.Interface{{Name: "eth0"}, {Name: knownTAP}, {Name: orphanTAP}}, nil
		},
		readTAPAlias: func(name string) (string, error) {
			switch name {
			case knownTAP:
				return network.TAPAlias("vm-known")
			case orphanTAP:
				return network.TAPAlias("vm-orphan")
			default:
				return "", errors.New("unexpected TAP alias request")
			}
		},
		listFirewall: func(context.Context) ([]network.OwnedFirewallRule, error) {
			return []network.OwnedFirewallRule{
				{Table: "filter", Chain: "FORWARD", Comment: network.FirewallOwnershipComment(knownTAP)},
				{Table: "nat", Chain: "POSTROUTING", Comment: network.FirewallOwnershipComment(orphanTAP)},
			}, nil
		},
	})
	machine := testMachine()
	machine.ID = "vm-tenant"
	machine.Runtime = microvm.RuntimeOwnership{
		HostSlotID:          "vm-known",
		JailPrepared:        true,
		AddressAllocated:    true,
		TAPCreated:          true,
		ForwardingApplied:   true,
		WorkspaceExported:   true,
		WorkspaceNFSAllowed: true,
		TAPName:             knownTAP,
		Network: microvm.NetworkAssignment{
			GuestIP:   "172.16.0.2",
			GatewayIP: "172.16.0.1",
			Subnet:    "172.16.0.0/30",
			GuestMAC:  "02:fc:ac:10:00:02",
		},
	}

	err = supervisor.CheckForOrphans([]microvm.Sandbox{machine})
	if !errors.Is(err, ErrOrphanedResources) {
		t.Fatalf("CheckForOrphans() error = %v, want ErrOrphanedResources", err)
	}
	orphanJail := filepath.Join(jailRoot, "vm-orphan")
	ambiguousJail := filepath.Join(jailRoot, "vm-ambiguous")
	if !strings.Contains(err.Error(), orphanJail) ||
		!strings.Contains(err.Error(), ambiguousJail) ||
		!strings.Contains(err.Error(), orphanTAP) ||
		!strings.Contains(err.Error(), "owned-orphan") ||
		!strings.Contains(err.Error(), network.FirewallOwnershipComment(orphanTAP)) {
		t.Errorf("CheckForOrphans() error = %v, want orphan jail, TAP, and firewall rule", err)
	}
	if strings.Contains(err.Error(), filepath.Join(jailRoot, "vm-known")) {
		t.Errorf("CheckForOrphans() reported owned jail: %v", err)
	}
	if strings.Contains(err.Error(), network.FirewallOwnershipComment(knownTAP)) {
		t.Errorf("CheckForOrphans() reported owned firewall rule: %v", err)
	}
}

func TestSupervisorReportsMismatchedTAPAlias(t *testing.T) {
	name, err := network.TAPName("vm-known")
	if err != nil {
		t.Fatal(err)
	}
	supervisor := newSupervisor(testConfig(), dependencies{
		readJailRoot: func(string) ([]os.DirEntry, error) { return nil, os.ErrNotExist },
		listInterfaces: func() ([]net.Interface, error) {
			return []net.Interface{{Name: name}}, nil
		},
		readTAPAlias: func(string) (string, error) { return "fire-fuse:vm-other", nil },
		listFirewall: func(context.Context) ([]network.OwnedFirewallRule, error) {
			return nil, nil
		},
	})
	machine := testMachine()
	machine.ID = "vm-known"
	machine.Runtime = microvm.RuntimeOwnership{
		JailPrepared:     true,
		AddressAllocated: true,
		TAPCreated:       true,
		TAPName:          name,
		Network: microvm.NetworkAssignment{
			GuestIP: "172.16.0.2", GatewayIP: "172.16.0.1", Subnet: "172.16.0.0/30", GuestMAC: "02:fc:ac:10:00:02",
		},
	}

	err = supervisor.CheckForOrphans([]microvm.Sandbox{machine})
	if !errors.Is(err, ErrOrphanedResources) || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("CheckForOrphans() error = %v, want ambiguous TAP orphan", err)
	}
}

func writeRuntimeManifest(t *testing.T, vmDir, vmID string) {
	t.Helper()
	record := fmt.Sprintf(`{"schema_version":1,"owner":"fire-fuse","vm_id":%q}`, vmID)
	if err := os.WriteFile(filepath.Join(vmDir, "fire-fuse-runtime.json"), []byte(record), 0o600); err != nil {
		t.Fatalf("WriteFile(runtime manifest) error = %v", err)
	}
}

func TestSupervisorOrphanInventoryAllowsCleanHost(t *testing.T) {
	cfg := testConfig()
	cfg.RuntimeDir = t.TempDir()
	supervisor := newSupervisor(cfg, dependencies{
		readJailRoot: func(string) ([]os.DirEntry, error) { return nil, os.ErrNotExist },
		listInterfaces: func() ([]net.Interface, error) {
			return []net.Interface{{Name: "lo"}, {Name: "eth0"}}, nil
		},
		readTAPAlias: func(string) (string, error) {
			t.Fatal("TAP alias read without a managed interface")
			return "", nil
		},
		listFirewall: func(context.Context) ([]network.OwnedFirewallRule, error) { return nil, nil },
	})

	if err := supervisor.CheckForOrphans(nil); err != nil {
		t.Fatalf("CheckForOrphans() error = %v", err)
	}
}

func TestSupervisorOrphanInventoryAllowsSnapshotTemplateTAP(t *testing.T) {
	cfg := testConfig()
	cfg.RuntimeDir = t.TempDir()
	templateTAP, err := network.TAPName(network.SnapshotTemplateVMID)
	if err != nil {
		t.Fatalf("TAPName() error = %v", err)
	}
	supervisor := newSupervisor(cfg, dependencies{
		readJailRoot: func(string) ([]os.DirEntry, error) { return nil, os.ErrNotExist },
		listInterfaces: func() ([]net.Interface, error) {
			return []net.Interface{{Name: templateTAP}}, nil
		},
		readTAPAlias: func(string) (string, error) {
			return network.TAPAlias(network.SnapshotTemplateVMID)
		},
		listFirewall: func(context.Context) ([]network.OwnedFirewallRule, error) { return nil, nil },
	})

	if err := supervisor.CheckForOrphans(nil); err != nil {
		t.Fatalf("CheckForOrphans() error = %v", err)
	}
}

func TestSupervisorOrphanInventoryReportsFirewallFailure(t *testing.T) {
	wantErr := errors.New("firewall inventory failed")
	supervisor := newSupervisor(testConfig(), dependencies{
		readJailRoot:   func(string) ([]os.DirEntry, error) { return nil, os.ErrNotExist },
		listInterfaces: func() ([]net.Interface, error) { return nil, nil },
		readTAPAlias:   func(string) (string, error) { return "", nil },
		listFirewall: func(context.Context) ([]network.OwnedFirewallRule, error) {
			return nil, wantErr
		},
	})

	if err := supervisor.CheckForOrphans(nil); !errors.Is(err, wantErr) {
		t.Fatalf("CheckForOrphans() error = %v, want firewall inventory error", err)
	}
}

func TestSupervisorOrphanInventoryReportsReadFailures(t *testing.T) {
	wantErr := errors.New("inventory failed")
	supervisor := newSupervisor(testConfig(), dependencies{
		readJailRoot: func(string) ([]os.DirEntry, error) { return nil, wantErr },
		listInterfaces: func() ([]net.Interface, error) {
			t.Fatal("interfaces listed after jail inventory failure")
			return nil, nil
		},
		readTAPAlias: func(string) (string, error) { return "", nil },
		listFirewall: func(context.Context) ([]network.OwnedFirewallRule, error) {
			return nil, nil
		},
	})

	if err := supervisor.CheckForOrphans(nil); !errors.Is(err, wantErr) {
		t.Fatalf("CheckForOrphans() error = %v, want inventory error", err)
	}
}

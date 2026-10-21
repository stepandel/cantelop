package network

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stepandel/cantelop/fire-fuse/telemetry/warmuptrace"
)

func TestForwardingManagerApplyAndRemove(t *testing.T) {
	runner := &scriptedRunner{}
	manager, err := newForwardingManager("/usr/sbin/iptables", "/usr/sbin/ip6tables", forwardingTestPolicy(), runner.run)
	if err != nil {
		t.Fatalf("newForwardingManager() error = %v", err)
	}

	trace := warmuptrace.New()
	ctx := warmuptrace.WithContext(context.Background(), trace)
	if err := manager.Apply(ctx, "ff0123456789abc", forwardingTestAllocation()); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	applied := append([]commandCall(nil), runner.calls...)
	if err := manager.Remove(context.Background(), "ff0123456789abc", forwardingTestAllocation()); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	if got, want := len(applied), 33; got != want {
		t.Fatalf("apply commands = %d, want %d", got, want)
	}
	if got, want := len(runner.calls), len(applied)+12; got != want {
		t.Fatalf("total commands = %d, want %d", got, want)
	}
	for _, want := range []commandCall{
		iptablesCall("-N", "filter", "FFE-0123456789abc"),
		iptablesCall("-I", "filter", "INPUT", "-m", "comment", "--comment", "fire-fuse:ff0123456789abc", "-i", "ff0123456789abc", "-j", "DROP"),
		iptablesCall("-I", "filter", "INPUT", "-m", "comment", "--comment", "fire-fuse:ff0123456789abc", "-i", "ff0123456789abc", "-s", "172.16.0.2/32", "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"),
		iptablesCall("-I", "filter", "INPUT", "-m", "comment", "--comment", "fire-fuse:ff0123456789abc", "-i", "ff0123456789abc", "-s", "172.16.0.2/32", "-p", "tcp", "--dport", "443", "-m", "conntrack", "--ctstate", "NEW,ESTABLISHED", "-j", "ACCEPT"),
		iptablesCall("-A", "filter", "FFE-0123456789abc", "-m", "comment", "--comment", "fire-fuse:ff0123456789abc", "-s", "172.16.0.2/32", "-p", "tcp", "-m", "multiport", "--dports", "22,80,443", "-m", "conntrack", "--ctstate", "NEW,ESTABLISHED", "-j", "ACCEPT"),
		iptablesCall("-A", "filter", "FFE-0123456789abc", "-m", "comment", "--comment", "fire-fuse:ff0123456789abc", "-s", "172.16.0.2/32", "-d", "169.254.0.0/16", "-j", "REJECT", "--reject-with", "icmp-net-prohibited"),
		iptablesCall("-A", "filter", "FFE-0123456789abc", "-m", "comment", "--comment", "fire-fuse:ff0123456789abc", "-j", "DROP"),
		ip6tablesCall("-I", "filter", "INPUT", "-m", "comment", "--comment", "fire-fuse:ff0123456789abc", "-i", "ff0123456789abc", "-j", "DROP"),
		ip6tablesCall("-I", "filter", "FORWARD", "-m", "comment", "--comment", "fire-fuse:ff0123456789abc", "-o", "ff0123456789abc", "-j", "DROP"),
	} {
		if !containsCommand(applied, want) {
			t.Errorf("apply commands do not contain %#v", want)
		}
	}
	for _, want := range []commandCall{
		iptablesCall("-F", "filter", "FFE-0123456789abc"),
		iptablesCall("-X", "filter", "FFE-0123456789abc"),
	} {
		if !containsCommand(runner.calls[len(applied):], want) {
			t.Errorf("cleanup commands do not contain %#v", want)
		}
	}
	assertWarmupSteps(t, trace, []string{
		"network.firewall.create_guest_egress_chain",
		"network.firewall.block_guest_host_access",
		"network.firewall.allow_guest_host_return",
		"network.firewall.allow_guest_gateway_https",
		"network.firewall.block_special_use_egress.4",
		"network.firewall.allow_guest_tcp",
		"network.firewall.allow_guest_dns.1_1_1_1.udp",
		"network.firewall.block_guest_ipv6_egress",
		"network.firewall.allow_guest_return",
		"network.firewall.masquerade_guest",
	})
}

func TestForwardingManagerAppliesAndRemovesWorkspaceNFS(t *testing.T) {
	runner := &scriptedRunner{}
	manager, err := newForwardingManager("/usr/sbin/iptables", "/usr/sbin/ip6tables", forwardingTestPolicy(), runner.run)
	if err != nil {
		t.Fatalf("newForwardingManager() error = %v", err)
	}

	allocation := forwardingTestAllocation()
	trace := warmuptrace.New()
	ctx := warmuptrace.WithContext(context.Background(), trace)
	if err := manager.ApplyWorkspaceNFS(
		ctx,
		"ff0123456789abc",
		allocation,
	); err != nil {
		t.Fatalf("ApplyWorkspaceNFS() error = %v", err)
	}
	if err := manager.RemoveWorkspaceNFS(
		context.Background(),
		"ff0123456789abc",
		allocation,
	); err != nil {
		t.Fatalf("RemoveWorkspaceNFS() error = %v", err)
	}

	ruleArgs := []string{
		"-m", "comment",
		"--comment", "fire-fuse:ff0123456789abc:workspace-nfs",
		"-i", "ff0123456789abc",
		"-s", "172.16.0.2/32",
		"-p", "tcp",
		"--dport", "2049",
		"-m", "conntrack",
		"--ctstate", "NEW,ESTABLISHED",
		"-j", "ACCEPT",
	}
	want := []commandCall{
		iptablesCall("-I", "filter", "INPUT", ruleArgs...),
		iptablesCall("-D", "filter", "INPUT", ruleArgs...),
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Errorf("commands = %#v, want %#v", runner.calls, want)
	}
	assertWarmupSteps(t, trace, []string{"network.firewall.allow_workspace_nfs"})
}

func TestForwardingRulesPutDenialsAheadOfPortAllows(t *testing.T) {
	rules, err := forwardingRules(
		"ff0123456789abc",
		forwardingTestAllocation(),
		forwardingTestPolicy(),
	)
	if err != nil {
		t.Fatalf("forwardingRules() error = %v", err)
	}
	var egress, forward, input []firewallRule
	for _, rule := range rules {
		if rule.ipv6 || rule.table != "filter" {
			continue
		}
		switch rule.chain {
		case egressChainName("ff0123456789abc"):
			if rule.operation == "-A" {
				egress = append(egress, rule)
			}
		case "FORWARD":
			forward = append([]firewallRule{rule}, forward...)
		case "INPUT":
			input = append([]firewallRule{rule}, input...)
		}
	}
	requireRuleBefore(t, egress,
		"network.firewall.block_special_use_egress.1",
		"network.firewall.allow_guest_tcp",
	)
	requireRuleBefore(t, egress,
		"network.firewall.allow_guest_tcp",
		"network.firewall.block_guest_egress_default",
	)
	requireRuleBefore(t, forward,
		"network.firewall.allow_guest_return",
		"network.firewall.block_unsolicited_guest_ingress",
	)
	requireRuleBefore(t, input,
		"network.firewall.allow_guest_gateway_https",
		"network.firewall.block_guest_host_access",
	)
	requireRuleBefore(t, input,
		"network.firewall.allow_guest_host_return",
		"network.firewall.block_guest_host_access",
	)
}

func TestEgressPolicyRejectsUnsafeConfiguration(t *testing.T) {
	for _, test := range []struct {
		name      string
		resolvers []string
		tcp       []uint16
		udp       []uint16
	}{
		{name: "private resolver", resolvers: []string{"10.0.0.53"}},
		{name: "IPv6 resolver", resolvers: []string{"2606:4700:4700::1111"}},
		{name: "duplicate resolver", resolvers: []string{"1.1.1.1", "1.1.1.1"}},
		{name: "zero port", tcp: []uint16{0}},
		{name: "duplicate port", udp: []uint16{443, 443}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewEgressPolicy(test.resolvers, test.tcp, test.udp); !errors.Is(err, ErrInvalidForwardingConfig) {
				t.Fatalf("NewEgressPolicy() error = %v, want ErrInvalidForwardingConfig", err)
			}
		})
	}
}

func TestForwardingManagerListsOnlyOwnedRules(t *testing.T) {
	manager, err := newForwardingManager("/usr/sbin/iptables", "/usr/sbin/ip6tables", forwardingTestPolicy(), (&scriptedRunner{}).run)
	if err != nil {
		t.Fatalf("newForwardingManager() error = %v", err)
	}
	var calls int
	manager.list = func(_ context.Context, name string, args ...string) (string, error) {
		calls++
		switch calls {
		case 1:
			wantArgs := []string{"--wait", "5", "-t", "filter", "-S"}
			if name != "/usr/sbin/iptables" || !reflect.DeepEqual(args, wantArgs) {
				t.Errorf("IPv4 filter list = %s %v, want %v", name, args, wantArgs)
			}
			return `-P INPUT ACCEPT
-N FFE-0123456789abc
-N FFE-backup
-A INPUT -m comment --comment "fire-fuse:ff0123456789abc" -j DROP
-A FORWARD -m comment --comment "fire-fuse:ff0123456789abc" -j ACCEPT
-A FORWARD -m comment --comment unrelated -j ACCEPT
-A FFE-0123456789abc -s 172.16.0.2/32 -m comment --comment "fire-fuse:ff0123456789abc" -j ACCEPT`, nil
		case 2:
			wantArgs := []string{"--wait", "5", "-t", "nat", "-S"}
			if !reflect.DeepEqual(args, wantArgs) {
				t.Errorf("NAT args = %v, want %v", args, wantArgs)
			}
			return `-A POSTROUTING -m comment --comment fire-fuse:ffabcdef0123456 -j MASQUERADE`, nil
		case 3:
			wantArgs := []string{"--wait", "5", "-t", "filter", "-S"}
			if name != "/usr/sbin/ip6tables" || !reflect.DeepEqual(args, wantArgs) {
				t.Errorf("IPv6 filter list = %s %v, want %v", name, args, wantArgs)
			}
			return `-A FORWARD -m comment --comment unrelated -j DROP`, nil
		default:
			t.Fatalf("unexpected list call %d with args %v", calls, args)
			return "", nil
		}
	}

	rules, err := manager.ListOwnedRules(context.Background())
	if err != nil {
		t.Fatalf("ListOwnedRules() error = %v", err)
	}
	want := []OwnedFirewallRule{
		{Table: "filter", Chain: "FFE-0123456789abc", Comment: "fire-fuse:ff0123456789abc"},
		{Table: "filter", Chain: "INPUT", Comment: "fire-fuse:ff0123456789abc"},
		{Table: "filter", Chain: "FORWARD", Comment: "fire-fuse:ff0123456789abc"},
		{Table: "nat", Chain: "POSTROUTING", Comment: "fire-fuse:ffabcdef0123456"},
	}
	if !reflect.DeepEqual(rules, want) {
		t.Errorf("rules = %#v, want %#v", rules, want)
	}
}

// Teardown removes the built-in rules first and drops the guest egress chain
// last, so an interrupted teardown leaves an empty chain and nothing else. That
// residue must still be attributable to the TAP that owned it, or orphan
// detection reports a clean host while the chain leaks.
func TestForwardingManagerListsEmptyOrphanedEgressChain(t *testing.T) {
	manager, err := newForwardingManager("/usr/sbin/iptables", "/usr/sbin/ip6tables", forwardingTestPolicy(), (&scriptedRunner{}).run)
	if err != nil {
		t.Fatalf("newForwardingManager() error = %v", err)
	}
	manager.list = func(_ context.Context, _ string, args ...string) (string, error) {
		if len(args) > 3 && args[3] == "filter" {
			return "-P FORWARD ACCEPT\n-N FFE-0123456789abc", nil
		}
		return "", nil
	}

	rules, err := manager.ListOwnedRules(context.Background())
	if err != nil {
		t.Fatalf("ListOwnedRules() error = %v", err)
	}
	want := []OwnedFirewallRule{
		{Table: "filter", Chain: "FFE-0123456789abc", Comment: "fire-fuse:ff0123456789abc"},
	}
	if !reflect.DeepEqual(rules, want) {
		t.Errorf("rules = %#v, want %#v", rules, want)
	}
}

func TestForwardingManagerReportsInventoryFailure(t *testing.T) {
	wantErr := errors.New("inventory failed")
	manager, err := newForwardingManager("/usr/sbin/iptables", "/usr/sbin/ip6tables", forwardingTestPolicy(), (&scriptedRunner{}).run)
	if err != nil {
		t.Fatalf("newForwardingManager() error = %v", err)
	}
	manager.list = func(context.Context, string, ...string) (string, error) {
		return "", wantErr
	}
	if _, err := manager.ListOwnedRules(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("ListOwnedRules() error = %v, want inventory failure", err)
	}
}

func TestForwardingManagerRollsBackPartialApply(t *testing.T) {
	wantErr := errors.New("rule rejected")
	runner := &scriptedRunner{errors: []error{nil, wantErr, nil}}
	manager, err := newForwardingManager("/usr/sbin/iptables", "/usr/sbin/ip6tables", forwardingTestPolicy(), runner.run)
	if err != nil {
		t.Fatalf("newForwardingManager() error = %v", err)
	}

	err = manager.Apply(context.Background(), "ff0123456789abc", forwardingTestAllocation())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Apply() error = %v, want rule error", err)
	}
	if len(runner.calls) != 14 ||
		!containsCommand(runner.calls, iptablesCall("-F", "filter", "FFE-0123456789abc")) ||
		!containsCommand(runner.calls, iptablesCall("-X", "filter", "FFE-0123456789abc")) {
		t.Errorf("rollback commands = %#v, want complete boundary teardown", runner.calls)
	}
}

func TestForwardingManagerReportsRollbackFailure(t *testing.T) {
	applyErr := errors.New("rule rejected")
	rollbackErr := errors.New("rollback rejected")
	runner := &scriptedRunner{errors: []error{nil, applyErr, rollbackErr}}
	manager, err := newForwardingManager("/usr/sbin/iptables", "/usr/sbin/ip6tables", forwardingTestPolicy(), runner.run)
	if err != nil {
		t.Fatalf("newForwardingManager() error = %v", err)
	}

	err = manager.Apply(context.Background(), "ff0123456789abc", forwardingTestAllocation())
	if !errors.Is(err, applyErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("Apply() error = %v, want apply and rollback errors", err)
	}
}

func TestForwardingManagerRollbackSurvivesRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	run := func(commandCtx context.Context, _ string, _ ...string) error {
		calls++
		switch calls {
		case 1:
			return nil
		case 2:
			cancel()
			return context.Canceled
		default:
			if err := commandCtx.Err(); err != nil {
				t.Errorf("rollback context error = %v, want active cleanup context", err)
			}
			return nil
		}
	}
	manager, err := newForwardingManager("/usr/sbin/iptables", "/usr/sbin/ip6tables", forwardingTestPolicy(), run)
	if err != nil {
		t.Fatalf("newForwardingManager() error = %v", err)
	}

	if err := manager.Apply(ctx, "ff0123456789abc", forwardingTestAllocation()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Apply() error = %v, want context.Canceled", err)
	}
	if calls != 14 {
		t.Errorf("command calls = %d, want 14", calls)
	}
}

func TestForwardingManagerRemoveIsIdempotent(t *testing.T) {
	runner := &scriptedRunner{errors: []error{ErrFirewallRuleNotFound, ErrFirewallRuleNotFound, ErrFirewallRuleNotFound}}
	manager, err := newForwardingManager("/usr/sbin/iptables", "/usr/sbin/ip6tables", forwardingTestPolicy(), runner.run)
	if err != nil {
		t.Fatalf("newForwardingManager() error = %v", err)
	}
	if err := manager.Remove(context.Background(), "ff0123456789abc", forwardingTestAllocation()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}

	nfsRunner := &scriptedRunner{errors: []error{ErrFirewallRuleNotFound}}
	nfsManager, err := newForwardingManager("/usr/sbin/iptables", "/usr/sbin/ip6tables", forwardingTestPolicy(), nfsRunner.run)
	if err != nil {
		t.Fatalf("newForwardingManager() error = %v", err)
	}
	if err := nfsManager.RemoveWorkspaceNFS(
		context.Background(),
		"ff0123456789abc",
		forwardingTestAllocation(),
	); err != nil {
		t.Fatalf("RemoveWorkspaceNFS() error = %v, want nil", err)
	}
}

func TestForwardingManagerValidatesInput(t *testing.T) {
	if _, err := NewForwardingManager("iptables", "/usr/sbin/ip6tables", forwardingTestPolicy()); !errors.Is(err, ErrInvalidForwardingConfig) {
		t.Fatalf("NewForwardingManager() error = %v, want ErrInvalidForwardingConfig", err)
	}
	runner := &scriptedRunner{}
	manager, err := newForwardingManager("/usr/sbin/iptables", "/usr/sbin/ip6tables", forwardingTestPolicy(), runner.run)
	if err != nil {
		t.Fatalf("newForwardingManager() error = %v", err)
	}
	if err := manager.Apply(context.Background(), "tap/name", forwardingTestAllocation()); !errors.Is(err, ErrInvalidForwardingConfig) {
		t.Fatalf("Apply() error = %v, want ErrInvalidForwardingConfig", err)
	}
	if err := manager.Apply(context.Background(), "tap0", forwardingTestAllocation()); !errors.Is(err, ErrInvalidForwardingConfig) {
		t.Fatalf("Apply() unmanaged TAP error = %v, want ErrInvalidForwardingConfig", err)
	}
	if err := manager.Apply(context.Background(), "ff0123456789abc", Allocation{}); !errors.Is(err, ErrInvalidAllocation) {
		t.Fatalf("Apply() allocation error = %v, want ErrInvalidAllocation", err)
	}
	if err := manager.ApplyWorkspaceNFS(
		context.Background(),
		"tap0",
		forwardingTestAllocation(),
	); !errors.Is(err, ErrInvalidForwardingConfig) {
		t.Fatalf(
			"ApplyWorkspaceNFS() unmanaged TAP error = %v, want ErrInvalidForwardingConfig",
			err,
		)
	}
	if err := manager.ApplyWorkspaceNFS(
		context.Background(),
		"ff0123456789abc",
		Allocation{},
	); !errors.Is(err, ErrInvalidAllocation) {
		t.Fatalf(
			"ApplyWorkspaceNFS() allocation error = %v, want ErrInvalidAllocation",
			err,
		)
	}
	if len(runner.calls) != 0 {
		t.Errorf("commands = %#v, want none", runner.calls)
	}
}

func TestForwardingManagerReportsWorkspaceNFSErrors(t *testing.T) {
	applyErr := errors.New("workspace NFS rule rejected")
	applyRunner := &scriptedRunner{errors: []error{applyErr}}
	applyManager, err := newForwardingManager(
		"/usr/sbin/iptables",
		"/usr/sbin/ip6tables",
		forwardingTestPolicy(),
		applyRunner.run,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyManager.ApplyWorkspaceNFS(
		context.Background(),
		"ff0123456789abc",
		forwardingTestAllocation(),
	); !errors.Is(err, applyErr) {
		t.Fatalf("ApplyWorkspaceNFS() error = %v, want %v", err, applyErr)
	}

	removeErr := errors.New("workspace NFS removal rejected")
	removeRunner := &scriptedRunner{errors: []error{removeErr}}
	removeManager, err := newForwardingManager(
		"/usr/sbin/iptables",
		"/usr/sbin/ip6tables",
		forwardingTestPolicy(),
		removeRunner.run,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := removeManager.RemoveWorkspaceNFS(
		context.Background(),
		"ff0123456789abc",
		forwardingTestAllocation(),
	); !errors.Is(err, removeErr) {
		t.Fatalf("RemoveWorkspaceNFS() error = %v, want %v", err, removeErr)
	}
}

func TestWorkspaceNFSFirewallOwnershipComment(t *testing.T) {
	got := WorkspaceNFSFirewallOwnershipComment("ff0123456789abc")
	want := "fire-fuse:ff0123456789abc:workspace-nfs"
	if got != want {
		t.Fatalf("WorkspaceNFSFirewallOwnershipComment() = %q, want %q", got, want)
	}
}

func TestRunIPTablesCommandClassifiesMissingRule(t *testing.T) {
	err := runIPTablesCommand(
		context.Background(),
		"/bin/sh",
		"-c",
		"echo 'iptables: Bad rule (does a matching rule exist in that chain?).' >&2; exit 1",
	)
	if !errors.Is(err, ErrFirewallRuleNotFound) {
		t.Fatalf("runIPTablesCommand() error = %v, want ErrFirewallRuleNotFound", err)
	}
}

func iptablesCall(operation, table, chain string, args ...string) commandCall {
	commandArgs := []string{"--wait", "5", "-t", table, operation, chain}
	commandArgs = append(commandArgs, args...)
	return commandCall{name: "/usr/sbin/iptables", args: commandArgs}
}

func ip6tablesCall(operation, table, chain string, args ...string) commandCall {
	call := iptablesCall(operation, table, chain, args...)
	call.name = "/usr/sbin/ip6tables"
	return call
}

func containsCommand(calls []commandCall, want commandCall) bool {
	for _, call := range calls {
		if reflect.DeepEqual(call, want) {
			return true
		}
	}
	return false
}

func requireRuleBefore(t *testing.T, rules []firewallRule, first, second string) {
	t.Helper()
	positions := make(map[string]int, len(rules))
	for index, rule := range rules {
		positions[rule.step] = index
	}
	firstIndex, firstExists := positions[first]
	secondIndex, secondExists := positions[second]
	if !firstExists || !secondExists || firstIndex >= secondIndex {
		t.Fatalf(
			"rule order %q=%d/%t, %q=%d/%t; rules=%v",
			first,
			firstIndex,
			firstExists,
			second,
			secondIndex,
			secondExists,
			rules,
		)
	}
}

func forwardingTestPolicy() EgressPolicy {
	policy, err := NewEgressPolicy(
		[]string{"1.1.1.1", "8.8.8.8"},
		[]uint16{22, 80, 443},
		[]uint16{443},
	)
	if err != nil {
		panic(err)
	}
	return policy
}

func forwardingTestAllocation() Allocation {
	allocator, err := NewAddressAllocator("172.16.0.0/30")
	if err != nil {
		panic(err)
	}
	allocation, err := allocator.Allocate("vm-test")
	if err != nil {
		panic(err)
	}
	return allocation
}

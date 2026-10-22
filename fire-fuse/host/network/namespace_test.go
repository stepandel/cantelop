package network

import (
	"context"
	"strings"
	"testing"
)

func TestIndependentRuntimeOwnershipNamespaces(t *testing.T) {
	standard, _ := TAPNameInNamespace("vm-identical", "")
	managed, _ := TAPNameInNamespace("vm-identical", "managed")
	if standard == managed || len(standard) != 15 || len(managed) != 15 {
		t.Fatal("runtime TAP namespaces overlap")
	}
	for _, scope := range []struct{ namespace, own, other string }{{"", standard, managed}, {"managed", managed, standard}} {
		if !IsTAPNameInNamespace(scope.own, scope.namespace) || IsTAPNameInNamespace(scope.other, scope.namespace) {
			t.Fatal("foreign runtime interface accepted")
		}
		alias, _ := TAPAliasInNamespace("vm-identical", scope.namespace)
		if ValidateTAPOwnership(scope.own, alias) != nil || ValidateTAPOwnership(scope.other, alias) == nil {
			t.Fatal("TAP alias crossed runtime ownership")
		}
		manager := &ForwardingManager{namespace: scope.namespace, list: func(context.Context, string, ...string) (string, error) {
			return "-N " + egressChainName(standard) + "\n-N " + egressChainName(managed) + "\n-A INPUT -m comment --comment " + FirewallOwnershipComment(standard) + " -j DROP\n-A INPUT -m comment --comment " + FirewallOwnershipComment(managed) + " -j DROP", nil
		}}
		rules, err := manager.ListOwnedRules(context.Background())
		// IPv4 and IPv6 filter rules share the same ownership identity;
		// inventory deduplicates them while preserving both tables.
		if err != nil || len(rules) != 4 {
			t.Fatalf("namespace inventory: %d rules, %v", len(rules), err)
		}
		for _, rule := range rules {
			if rule.Comment != FirewallOwnershipComment(scope.own) || strings.Contains(rule.Comment, scope.other) {
				t.Fatalf("foreign firewall inventoried: %+v", rule)
			}
		}
		if manager.Apply(context.Background(), scope.other, Allocation{}) == nil || manager.RemoveWorkspaceNFS(context.Background(), scope.other, Allocation{}) == nil {
			t.Fatal("foreign runtime firewall mutation accepted")
		}
	}
	if _, err := TAPNameInNamespace("vm-identical", "unknown"); err == nil {
		t.Fatal("unknown namespace accepted")
	}
}

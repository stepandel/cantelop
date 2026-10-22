package network

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/telemetry/warmuptrace"
)

const (
	firewallRollbackTimeout = 5 * time.Second
	firewallCommentPrefix   = "fire-fuse:"
	egressChainPrefix       = "FFE-"
	workspaceNFSDataPort    = "2049"
	workspaceNFSComment     = ":workspace-nfs"
)

var (
	ErrInvalidForwardingConfig = errors.New("invalid forwarding manager configuration")
	ErrFirewallRuleNotFound    = errors.New("firewall rule not found")
	iptablesCommentPattern     = regexp.MustCompile(`(^|[[:space:]])--comment[[:space:]]+("([^"]*)"|'([^']*)'|([^[:space:]]+))`)
	blockedEgressDestinations  = []string{
		"0.0.0.0/8",
		"10.0.0.0/8",
		"100.64.0.0/10",
		"127.0.0.0/8",
		"169.254.0.0/16",
		"172.16.0.0/12",
		"192.0.0.0/24",
		"192.0.2.0/24",
		"192.88.99.0/24",
		"192.168.0.0/16",
		"198.18.0.0/15",
		"198.51.100.0/24",
		"203.0.113.0/24",
		"224.0.0.0/4",
		"240.0.0.0/4",
	}
)

// OwnedFirewallRule identifies a rule carrying a fire-fuse ownership comment.
type OwnedFirewallRule struct {
	Table   string
	Chain   string
	Comment string
}

type firewallRule struct {
	ipv6      bool
	operation string
	table     string
	chain     string
	step      string
	args      []string
}

// EgressPolicy is the host-enforced allowlist for guest-initiated traffic.
type EgressPolicy struct {
	DNSResolvers    []netip.Addr
	AllowedTCPPorts []uint16
	AllowedUDPPorts []uint16
}

// NewEgressPolicy parses and normalizes a validated host configuration.
func NewEgressPolicy(
	dnsResolvers []string,
	allowedTCPPorts,
	allowedUDPPorts []uint16,
) (EgressPolicy, error) {
	policy := EgressPolicy{
		AllowedTCPPorts: append([]uint16(nil), allowedTCPPorts...),
		AllowedUDPPorts: append([]uint16(nil), allowedUDPPorts...),
	}
	for _, value := range dnsResolvers {
		address, err := netip.ParseAddr(value)
		if err != nil || !address.Is4() || !address.IsGlobalUnicast() ||
			address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
			return EgressPolicy{}, fmt.Errorf("%w: DNS resolver %q is not a public IPv4 address", ErrInvalidForwardingConfig, value)
		}
		policy.DNSResolvers = append(policy.DNSResolvers, address)
	}
	sort.Slice(policy.DNSResolvers, func(i, j int) bool {
		return policy.DNSResolvers[i].Less(policy.DNSResolvers[j])
	})
	sort.Slice(policy.AllowedTCPPorts, func(i, j int) bool {
		return policy.AllowedTCPPorts[i] < policy.AllowedTCPPorts[j]
	})
	sort.Slice(policy.AllowedUDPPorts, func(i, j int) bool {
		return policy.AllowedUDPPorts[i] < policy.AllowedUDPPorts[j]
	})
	if err := validateEgressPolicy(policy); err != nil {
		return EgressPolicy{}, err
	}
	return policy, nil
}

// ForwardingManager owns per-VM forwarding and source-NAT firewall rules.
type ForwardingManager struct {
	namespace    string
	iptablesBin  string
	ip6tablesBin string
	policy       EgressPolicy
	run          commandRunner
	list         func(context.Context, string, ...string) (string, error)
}

// NewForwardingManager constructs a production iptables forwarding manager.
func NewForwardingManager(
	iptablesBin,
	ip6tablesBin string,
	policy EgressPolicy,
) (*ForwardingManager, error) {
	return newForwardingManager(iptablesBin, ip6tablesBin, policy, runIPTablesCommand)
}

func newForwardingManager(
	iptablesBin,
	ip6tablesBin string,
	policy EgressPolicy,
	run commandRunner,
) (*ForwardingManager, error) {
	for name, path := range map[string]string{
		"iptables":  iptablesBin,
		"ip6tables": ip6tablesBin,
	} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
			return nil, fmt.Errorf("%w: %s binary must be a clean absolute file path", ErrInvalidForwardingConfig, name)
		}
	}
	if run == nil {
		return nil, fmt.Errorf("%w: command runner is required", ErrInvalidForwardingConfig)
	}
	if err := validateEgressPolicy(policy); err != nil {
		return nil, err
	}
	return &ForwardingManager{
		iptablesBin:  iptablesBin,
		ip6tablesBin: ip6tablesBin,
		policy: EgressPolicy{
			DNSResolvers:    append([]netip.Addr(nil), policy.DNSResolvers...),
			AllowedTCPPorts: append([]uint16(nil), policy.AllowedTCPPorts...),
			AllowedUDPPorts: append([]uint16(nil), policy.AllowedUDPPorts...),
		},
		run:  run,
		list: runCommandOutput,
	}, nil
}

// ListOwnedRules inventories fire-fuse-tagged rules without mutating them.
//
// Each table is dumped whole rather than a fixed set of built-in chains. The
// per-guest egress chains carry the same ownership comments, and teardown
// removes the built-in rules first and drops the chain last, so a teardown that
// stops early leaves residue that only lives inside those chains.
func (m *ForwardingManager) ListOwnedRules(ctx context.Context) ([]OwnedFirewallRule, error) {
	tables := []struct {
		binary string
		family string
		table  string
	}{
		{binary: m.iptablesBin, family: "ipv4", table: "filter"},
		{binary: m.iptablesBin, family: "ipv4", table: "nat"},
		{binary: m.ip6tablesBin, family: "ipv6", table: "filter"},
	}

	var owned []OwnedFirewallRule
	seen := make(map[OwnedFirewallRule]struct{})
	record := func(rule OwnedFirewallRule) {
		if _, exists := seen[rule]; exists {
			return
		}
		seen[rule] = struct{}{}
		owned = append(owned, rule)
	}
	for _, table := range tables {
		output, err := m.list(ctx, table.binary, "--wait", "5", "-t", table.table, "-S")
		if err != nil {
			return nil, fmt.Errorf("list %s %s firewall rules: %w", table.family, table.table, err)
		}
		for _, line := range strings.Split(output, "\n") {
			chain, declaration, ok := iptablesChainReference(line)
			if !ok {
				continue
			}
			if declaration {
				// A guest egress chain emptied by a partial teardown carries no
				// comment of its own. Name it after the TAP it was created for
				// so it reports the same ownership a live guest's rules do.
				if comment, isEgress := egressChainOwnershipComment(chain); isEgress && strings.HasPrefix(comment, namespaceOwnershipPrefix(m.namespace)) {
					record(OwnedFirewallRule{Table: table.table, Chain: chain, Comment: comment})
				}
				continue
			}
			comment, exists := parseIPTablesComment(line)
			if !exists || !strings.HasPrefix(comment, namespaceOwnershipPrefix(m.namespace)) {
				continue
			}
			record(OwnedFirewallRule{Table: table.table, Chain: chain, Comment: comment})
		}
	}
	return owned, nil
}

// iptablesChainReference reports the chain an `iptables -S` line refers to and
// whether the line declares that chain rather than appending a rule to it.
func iptablesChainReference(line string) (chain string, declaration, ok bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return "", false, false
	}
	switch fields[0] {
	case "-N":
		return fields[1], true, true
	case "-A":
		return fields[1], false, true
	default:
		return "", false, false
	}
}

// egressChainOwnershipComment maps a guest egress chain back to the ownership
// comment carried by the rules inside it.
func egressChainOwnershipComment(chain string) (string, bool) {
	prefix := tapNamePrefix
	suffix, found := strings.CutPrefix(chain, egressChainPrefix)
	if !found {
		suffix, found = strings.CutPrefix(chain, "FME-")
		prefix = "fm"
	}
	if !found || suffix == "" {
		return "", false
	}
	tapName := prefix + suffix
	if !IsManagedTAPName(tapName) {
		return "", false
	}
	return firewallOwnershipComment(tapName), true
}

// Apply allows one guest to initiate forwarded connections and masquerades its source address.
func (m *ForwardingManager) Apply(ctx context.Context, tapName string, allocation Allocation) error {
	if !IsTAPNameInNamespace(tapName, m.namespace) {
		return ErrInvalidForwardingConfig
	}
	rules, err := forwardingRules(tapName, allocation, m.policy)
	if err != nil {
		return err
	}

	for _, rule := range rules {
		finishRule := warmuptrace.Start(ctx, rule.step)
		if err := m.runRule(ctx, rule.operation, rule); err != nil {
			finishRule()
			rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), firewallRollbackTimeout)
			defer cancel()
			return errors.Join(
				fmt.Errorf("add forwarding rule: %w", err),
				m.removeForwardingRules(rollbackCtx, tapName, allocation),
			)
		}
		finishRule()
	}
	return nil
}

// Remove deletes one guest's exact rules. Rules already absent are successful.
func (m *ForwardingManager) Remove(ctx context.Context, tapName string, allocation Allocation) error {
	if !IsTAPNameInNamespace(tapName, m.namespace) {
		return ErrInvalidForwardingConfig
	}
	return m.removeForwardingRules(ctx, tapName, allocation)
}

func (m *ForwardingManager) removeForwardingRules(
	ctx context.Context,
	tapName string,
	allocation Allocation,
) error {
	rules, err := forwardingCleanupRules(tapName, allocation)
	if err != nil {
		return err
	}
	var removeErrors []error
	for _, rule := range rules {
		if err := m.runRule(ctx, rule.operation, rule); err != nil &&
			!errors.Is(err, ErrFirewallRuleNotFound) {
			removeErrors = append(removeErrors, fmt.Errorf("delete forwarding boundary: %w", err))
		}
	}
	return errors.Join(removeErrors...)
}

// ApplyWorkspaceNFS allows one guest to reach only the host NFSv4 data port.
// The direct INPUT insertion stays ahead of this guest's terminal host-input
// deny rule; the separately provisioned NFS chain remains defense in depth.
func (m *ForwardingManager) ApplyWorkspaceNFS(
	ctx context.Context,
	tapName string,
	allocation Allocation,
) error {
	if !IsTAPNameInNamespace(tapName, m.namespace) {
		return ErrInvalidForwardingConfig
	}
	rule, err := workspaceNFSFirewallRule(tapName, allocation)
	if err != nil {
		return err
	}
	finishRule := warmuptrace.Start(ctx, "network.firewall.allow_workspace_nfs")
	if err := m.runRule(ctx, "-I", rule); err != nil {
		finishRule()
		return fmt.Errorf("allow workspace NFS input: %w", err)
	}
	finishRule()
	return nil
}

// RemoveWorkspaceNFS removes one guest's exact host NFSv4 authorization.
// An already absent rule is successful.
func (m *ForwardingManager) RemoveWorkspaceNFS(
	ctx context.Context,
	tapName string,
	allocation Allocation,
) error {
	if !IsTAPNameInNamespace(tapName, m.namespace) {
		return ErrInvalidForwardingConfig
	}
	rule, err := workspaceNFSFirewallRule(tapName, allocation)
	if err != nil {
		return err
	}
	if err := m.removeRules(ctx, []firewallRule{rule}); err != nil {
		return fmt.Errorf("remove workspace NFS input: %w", err)
	}
	return nil
}

func (m *ForwardingManager) removeRules(ctx context.Context, rules []firewallRule) error {
	var removeErrors []error
	for _, rule := range rules {
		if err := m.runRule(ctx, "-D", rule); err != nil && !errors.Is(err, ErrFirewallRuleNotFound) {
			removeErrors = append(removeErrors, fmt.Errorf("delete forwarding rule: %w", err))
		}
	}
	return errors.Join(removeErrors...)
}

func (m *ForwardingManager) runRule(ctx context.Context, operation string, rule firewallRule) error {
	args := []string{"--wait", "5", "-t", rule.table, operation, rule.chain}
	args = append(args, rule.args...)
	binary := m.iptablesBin
	if rule.ipv6 {
		binary = m.ip6tablesBin
	}
	return m.run(ctx, binary, args...)
}

func workspaceNFSFirewallRule(tapName string, allocation Allocation) (firewallRule, error) {
	if !IsManagedTAPName(tapName) {
		return firewallRule{}, fmt.Errorf(
			"%w: invalid TAP name %q",
			ErrInvalidForwardingConfig,
			tapName,
		)
	}
	if err := validateAllocation(allocation); err != nil {
		return firewallRule{}, err
	}

	return firewallRule{
		table: "filter",
		chain: "INPUT",
		args: []string{
			"-m", "comment",
			"--comment", firewallOwnershipComment(tapName) + workspaceNFSComment,
			"-i", tapName,
			"-s", allocation.GuestIP.String() + "/32",
			"-p", "tcp",
			"--dport", workspaceNFSDataPort,
			"-m", "conntrack",
			"--ctstate", "NEW,ESTABLISHED",
			"-j", "ACCEPT",
		},
	}, nil
}

func isMissingFirewallChainError(err error) bool {
	message := err.Error()
	return strings.Contains(message, "No chain/target/match by that name") ||
		strings.Contains(message, "does not exist")
}

func forwardingRules(
	tapName string,
	allocation Allocation,
	policy EgressPolicy,
) ([]firewallRule, error) {
	if !IsManagedTAPName(tapName) {
		return nil, fmt.Errorf("%w: invalid TAP name %q", ErrInvalidForwardingConfig, tapName)
	}
	if err := validateAllocation(allocation); err != nil {
		return nil, err
	}
	if err := validateEgressPolicy(policy); err != nil {
		return nil, err
	}
	guestSource := allocation.GuestIP.String() + "/32"
	comment := []string{"-m", "comment", "--comment", firewallOwnershipComment(tapName)}
	egressChain := egressChainName(tapName)
	rules := []firewallRule{
		{
			operation: "-N",
			table:     "filter",
			chain:     egressChain,
			step:      "network.firewall.create_guest_egress_chain",
		},
	}
	for index, destination := range blockedEgressDestinations {
		rules = append(rules, firewallRule{
			operation: "-A",
			table:     "filter",
			chain:     egressChain,
			step:      "network.firewall.block_special_use_egress." + strconv.Itoa(index),
			args: appendRuleArgs(
				comment,
				"-s", guestSource,
				"-d", destination,
				"-j", "REJECT", "--reject-with", "icmp-net-prohibited",
			),
		})
	}
	for _, resolver := range policy.DNSResolvers {
		for _, protocol := range []string{"udp", "tcp"} {
			rules = append(rules, firewallRule{
				operation: "-A",
				table:     "filter",
				chain:     egressChain,
				step: fmt.Sprintf(
					"network.firewall.allow_guest_dns.%s.%s",
					strings.ReplaceAll(resolver.String(), ".", "_"),
					protocol,
				),
				args: appendRuleArgs(
					comment,
					"-s", guestSource,
					"-d", resolver.String()+"/32",
					"-p", protocol, "--dport", "53",
					"-m", "conntrack", "--ctstate", "NEW,ESTABLISHED",
					"-j", "ACCEPT",
				),
			})
		}
	}
	for _, protocol := range []struct {
		name  string
		ports []uint16
	}{
		{name: "tcp", ports: policy.AllowedTCPPorts},
		{name: "udp", ports: policy.AllowedUDPPorts},
	} {
		if ports := multiportList(protocol.ports); ports != "" {
			rules = append(rules, firewallRule{
				operation: "-A",
				table:     "filter",
				chain:     egressChain,
				step:      "network.firewall.allow_guest_" + protocol.name,
				args: appendRuleArgs(
					comment,
					"-s", guestSource,
					"-p", protocol.name,
					"-m", "multiport", "--dports", ports,
					"-m", "conntrack", "--ctstate", "NEW,ESTABLISHED",
					"-j", "ACCEPT",
				),
			})
		}
	}
	rules = append(rules, firewallRule{
		operation: "-A",
		table:     "filter",
		chain:     egressChain,
		step:      "network.firewall.block_guest_egress_default",
		args:      appendRuleArgs(comment, "-j", "DROP"),
	})
	rules = append(rules, forwardingFixedRules(tapName, allocation)...)
	return rules, nil
}

func forwardingFixedRules(tapName string, allocation Allocation) []firewallRule {
	guestSource := allocation.GuestIP.String() + "/32"
	comment := []string{"-m", "comment", "--comment", firewallOwnershipComment(tapName)}
	insertRule := func(chain, step string, args ...string) firewallRule {
		return firewallRule{
			operation: "-I",
			table:     "filter",
			chain:     chain,
			step:      step,
			args:      appendRuleArgs(comment, args...),
		}
	}
	return []firewallRule{
		insertRule("INPUT", "network.firewall.block_guest_host_access",
			"-i", tapName, "-j", "DROP",
		),
		insertRule("INPUT", "network.firewall.allow_guest_host_return",
			"-i", tapName, "-s", guestSource,
			"-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED",
			"-j", "ACCEPT",
		),
		// Runtime completion is a capability-authenticated HTTPS callback to
		// the public App gateway. Traffic to a host-owned public address takes
		// the INPUT path rather than the guest egress FORWARD chain.
		insertRule("INPUT", "network.firewall.allow_guest_gateway_https",
			"-i", tapName, "-s", guestSource,
			"-p", "tcp", "--dport", "443",
			"-m", "conntrack", "--ctstate", "NEW,ESTABLISHED",
			"-j", "ACCEPT",
		),
		insertRule("FORWARD", "network.firewall.route_guest_egress_policy",
			"-i", tapName, "-j", egressChainName(tapName),
		),
		insertRule("FORWARD", "network.firewall.block_unsolicited_guest_ingress",
			"-o", tapName, "-j", "DROP",
		),
		insertRule("FORWARD", "network.firewall.allow_guest_return",
			"-o", tapName, "-d", guestSource,
			"-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT",
		),
		firewallRule{
			ipv6:      true,
			operation: "-I",
			table:     "filter",
			chain:     "INPUT",
			step:      "network.firewall.block_guest_ipv6_host_access",
			args:      appendRuleArgs(comment, "-i", tapName, "-j", "DROP"),
		},
		firewallRule{
			ipv6:      true,
			operation: "-I",
			table:     "filter",
			chain:     "FORWARD",
			step:      "network.firewall.block_guest_ipv6_egress",
			args:      appendRuleArgs(comment, "-i", tapName, "-j", "DROP"),
		},
		firewallRule{
			ipv6:      true,
			operation: "-I",
			table:     "filter",
			chain:     "FORWARD",
			step:      "network.firewall.block_guest_ipv6_ingress",
			args:      appendRuleArgs(comment, "-o", tapName, "-j", "DROP"),
		},
		{
			operation: "-A",
			table:     "nat",
			chain:     "POSTROUTING",
			step:      "network.firewall.masquerade_guest",
			args:      appendRuleArgs(comment, "-s", guestSource, "-j", "MASQUERADE"),
		},
	}
}

func forwardingCleanupRules(
	tapName string,
	allocation Allocation,
) ([]firewallRule, error) {
	if !IsManagedTAPName(tapName) {
		return nil, fmt.Errorf("%w: invalid TAP name %q", ErrInvalidForwardingConfig, tapName)
	}
	if err := validateAllocation(allocation); err != nil {
		return nil, err
	}
	fixed := forwardingFixedRules(tapName, allocation)
	rules := make([]firewallRule, 0, len(fixed)+2)
	for _, rule := range reverseRules(fixed) {
		rule.operation = "-D"
		rules = append(rules, rule)
	}
	rules = append(rules,
		firewallRule{operation: "-F", table: "filter", chain: egressChainName(tapName)},
		firewallRule{operation: "-X", table: "filter", chain: egressChainName(tapName)},
	)
	return rules, nil
}

func egressChainName(tapName string) string {
	if strings.HasPrefix(tapName, "fm") {
		return "FME-" + strings.TrimPrefix(tapName, "fm")
	}
	return egressChainPrefix + strings.TrimPrefix(tapName, tapNamePrefix)
}

func validateEgressPolicy(policy EgressPolicy) error {
	if len(policy.DNSResolvers) > 4 {
		return fmt.Errorf("%w: DNS resolver allowlist supports at most 4 addresses", ErrInvalidForwardingConfig)
	}
	seenResolvers := make(map[netip.Addr]struct{}, len(policy.DNSResolvers))
	for _, resolver := range policy.DNSResolvers {
		if !resolver.IsValid() || !resolver.Is4() || !resolver.IsGlobalUnicast() ||
			resolver.IsPrivate() || resolver.IsLoopback() || resolver.IsLinkLocalUnicast() {
			return fmt.Errorf("%w: DNS resolvers must be public IPv4 addresses", ErrInvalidForwardingConfig)
		}
		if _, exists := seenResolvers[resolver]; exists {
			return fmt.Errorf("%w: duplicate DNS resolver %s", ErrInvalidForwardingConfig, resolver)
		}
		seenResolvers[resolver] = struct{}{}
	}
	for name, ports := range map[string][]uint16{
		"TCP": policy.AllowedTCPPorts,
		"UDP": policy.AllowedUDPPorts,
	} {
		if len(ports) > 15 {
			return fmt.Errorf("%w: %s allowlist exceeds iptables multiport limit", ErrInvalidForwardingConfig, name)
		}
		seen := make(map[uint16]struct{}, len(ports))
		for _, port := range ports {
			if port == 0 {
				return fmt.Errorf("%w: %s allowlist contains port 0", ErrInvalidForwardingConfig, name)
			}
			if _, exists := seen[port]; exists {
				return fmt.Errorf("%w: duplicate %s port %d", ErrInvalidForwardingConfig, name, port)
			}
			seen[port] = struct{}{}
		}
	}
	return nil
}

func multiportList(ports []uint16) string {
	values := make([]string, len(ports))
	for index, port := range ports {
		values[index] = strconv.FormatUint(uint64(port), 10)
	}
	return strings.Join(values, ",")
}

func firewallOwnershipComment(tapName string) string {
	if strings.HasPrefix(tapName, "fm") {
		return namespaceOwnershipPrefix("managed") + tapName
	}
	return firewallCommentPrefix + tapName
}

// FirewallOwnershipComment returns the exact comment used for one managed TAP.
func FirewallOwnershipComment(tapName string) string {
	return firewallOwnershipComment(tapName)
}

// WorkspaceNFSFirewallOwnershipComment returns the ownership marker for one
// managed TAP's host NFS authorization.
func WorkspaceNFSFirewallOwnershipComment(tapName string) string {
	return firewallOwnershipComment(tapName) + workspaceNFSComment
}

func parseIPTablesComment(line string) (string, bool) {
	match := iptablesCommentPattern.FindStringSubmatch(line)
	if match == nil {
		return "", false
	}
	for _, index := range []int{3, 4, 5} {
		if match[index] != "" {
			return match[index], true
		}
	}
	return "", true
}

func appendRuleArgs(prefix []string, args ...string) []string {
	combined := make([]string, 0, len(prefix)+len(args))
	combined = append(combined, prefix...)
	return append(combined, args...)
}

func reverseRules(rules []firewallRule) []firewallRule {
	reversed := make([]firewallRule, len(rules))
	for index := range rules {
		reversed[len(rules)-1-index] = rules[index]
	}
	return reversed
}

func runIPTablesCommand(ctx context.Context, name string, args ...string) error {
	err := runCommand(ctx, name, args...)
	if err != nil && (strings.Contains(err.Error(), "Bad rule") ||
		strings.Contains(err.Error(), "does a matching rule exist") ||
		isMissingFirewallChainError(err)) {
		return errors.Join(ErrFirewallRuleNotFound, err)
	}
	return err
}

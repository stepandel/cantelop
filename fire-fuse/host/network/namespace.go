package network

import (
	"errors"
	"strings"
)

// Resource namespaces let independent runtime state/cgroup owners share a
// machine without treating each other's network resources as orphans.
func ValidateNamespace(namespace string) error {
	if namespace != "" && namespace != "managed" {
		return errors.New("unknown runtime network namespace")
	}
	return nil
}

func namespaceTAPPrefix(namespace string) string {
	if namespace == "managed" {
		return "fm"
	}
	return tapNamePrefix
}

func namespaceOwnershipPrefix(namespace string) string {
	if namespace == "managed" {
		return "fire-fuse-managed:"
	}
	return tapAliasPrefix
}

func IsTAPNameInNamespace(name, namespace string) bool {
	return ValidateNamespace(namespace) == nil && IsManagedTAPName(name) && strings.HasPrefix(name, namespaceTAPPrefix(namespace))
}

func TAPNameInNamespace(vmID, namespace string) (string, error) {
	if err := ValidateNamespace(namespace); err != nil {
		return "", err
	}
	name, err := TAPName(vmID)
	if err != nil {
		return "", err
	}
	return namespaceTAPPrefix(namespace) + strings.TrimPrefix(name, tapNamePrefix), nil
}

func TAPAliasInNamespace(vmID, namespace string) (string, error) {
	if err := ValidateNamespace(namespace); err != nil {
		return "", err
	}
	alias, err := TAPAlias(vmID)
	if err != nil {
		return "", err
	}
	return namespaceOwnershipPrefix(namespace) + strings.TrimPrefix(alias, tapAliasPrefix), nil
}

func NewTAPManagerInNamespace(ipBin, namespace string) (*TAPManager, error) {
	if err := ValidateNamespace(namespace); err != nil {
		return nil, err
	}
	manager, err := NewTAPManager(ipBin)
	if err == nil {
		manager.namespace = namespace
	}
	return manager, err
}

func NewForwardingManagerInNamespace(iptablesBin, ip6tablesBin string, policy EgressPolicy, namespace string) (*ForwardingManager, error) {
	if err := ValidateNamespace(namespace); err != nil {
		return nil, err
	}
	manager, err := NewForwardingManager(iptablesBin, ip6tablesBin, policy)
	if err == nil {
		manager.namespace = namespace
	}
	return manager, err
}

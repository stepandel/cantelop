// Package firecracker adapts the current microVM lifecycle to provider-neutral
// Session contracts.
package firecracker

import (
	"errors"
	"fmt"
	"net"
	"net/netip"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
	"github.com/stepandel/cantelop/fire-fuse/runtime/session"
)

// MapState converts the current microVM lifecycle state into the provider-neutral contract.
func MapState(state model.State) (session.State, error) {
	switch state {
	case model.StateActive:
		return session.StateActive, nil
	case model.StateIdle:
		return session.StateIdle, nil
	default:
		return "", fmt.Errorf("unknown Firecracker Session state %q", state)
	}
}

// MapSession converts the current microVM runtime identity into the provider-neutral contract.
func MapSession(value model.Session) (session.Session, error) {
	state, err := MapState(value.State)
	if err != nil {
		return session.Session{}, err
	}
	mapped := session.Session{
		ID: value.ID, WorkspaceID: value.WorkspaceID,
		BuildID: value.BuildID, KeepAliveSeconds: value.KeepAliveSeconds, State: state,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
	if value.Startup != nil {
		mapped.Startup = &session.Startup{
			CompletedAt: value.Startup.CompletedAt,
			Duration:    value.Startup.Duration,
		}
	}
	return mapped, nil
}

// MapEndpoint converts a private Firecracker guest route into an HTTP endpoint.
func MapEndpoint(network model.NetworkAssignment, port int) (session.Endpoint, error) {
	if port < 1 || port > 65535 {
		return session.Endpoint{}, errors.New("Firecracker runtime port is invalid")
	}
	address, err := netip.ParseAddr(network.GuestIP)
	if err != nil || !address.Is4() || !address.IsPrivate() {
		return session.Endpoint{}, errors.New("Firecracker Session guest IP is not private IPv4")
	}
	endpoint := session.Endpoint{
		Scheme:    "http",
		Authority: net.JoinHostPort(address.String(), fmt.Sprint(port)),
	}
	if _, err := endpoint.TargetURL(); err != nil {
		return session.Endpoint{}, fmt.Errorf("construct Firecracker runtime endpoint: %w", err)
	}
	return endpoint, nil
}

// MapRoute converts the current microVM route into the provider-neutral contract.
func MapRoute(route model.SessionRoute, port int) (session.Route, error) {
	if !session.ValidSandboxID(route.SandboxID) {
		return session.Route{}, errors.New("Firecracker Session route has an invalid Sandbox ID")
	}
	value, err := MapSession(route.Session)
	if err != nil {
		return session.Route{}, err
	}
	endpoint, err := MapEndpoint(route.Network, port)
	if err != nil {
		return session.Route{}, err
	}
	return session.Route{Session: value, SandboxID: route.SandboxID, Endpoint: endpoint}, nil
}

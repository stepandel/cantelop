package observer

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

var errNoRequestEvents = errors.New("no request events")

func (s *CompleteSource) Snapshot(ctx context.Context) (Snapshot, error) {
	return s.runtimeSnapshot(ctx, nil)
}

func (s *CompleteSource) WorkspaceSnapshot(ctx context.Context, ids []string) (Snapshot, error) {
	return s.runtimeSnapshot(ctx, workspaceSet(ids))
}

// ManagedDatabaseAccess returns the isolated runtime for the dedicated broker
// route, or a nil interface when no managed runtime is configured.
func (s *CompleteSource) ManagedDatabaseAccess() DatabaseAccessSource {
	if s.managed == nil {
		return nil
	}
	return s.managed
}

func (s *CompleteSource) pools() []*ControlClient {
	if s.managed == nil {
		return []*ControlClient{s.ControlClient}
	}
	return []*ControlClient{s.ControlClient, s.managed}
}

func (s *CompleteSource) runtimeSnapshot(ctx context.Context, wanted map[string]struct{}) (Snapshot, error) {
	pools := s.pools()
	snapshots := make([]Snapshot, len(pools))
	if err := forEachBounded(ctx, len(pools), len(pools), func(ctx context.Context, i int) error {
		var err error
		snapshots[i], err = pools[i].snapshot(ctx, wanted)
		return err
	}); err != nil {
		// Never report the reachable pool as a complete, potentially empty host.
		return Snapshot{}, err
	}
	merged := snapshots[0]
	if len(snapshots) == 1 {
		return merged, nil
	}
	workspaceIDs, sessionIDs, sandboxIDs := make(map[string]bool), make(map[string]bool), make(map[string]bool)
	for i, snapshot := range snapshots {
		for _, workspace := range snapshot.Workspaces {
			if workspaceIDs[workspace.ID] {
				return Snapshot{}, fmt.Errorf("Workspace %s belongs to multiple runtime pools", workspace.ID)
			}
			workspaceIDs[workspace.ID] = true
		}
		for _, session := range snapshot.Sessions {
			if sessionIDs[session.ID] {
				return Snapshot{}, fmt.Errorf("Session %s belongs to multiple runtime pools", session.ID)
			}
			sessionIDs[session.ID] = true
		}
		for _, sandbox := range snapshot.Sandboxes {
			if sandboxIDs[sandbox.ID] {
				return Snapshot{}, fmt.Errorf("Sandbox %s belongs to multiple runtime pools", sandbox.ID)
			}
			sandboxIDs[sandbox.ID] = true
		}
		if i == 0 {
			continue
		}
		merged.Ready = merged.Ready && snapshot.Ready
		if snapshot.ObservedAt.Before(merged.ObservedAt) {
			merged.ObservedAt = snapshot.ObservedAt
		}
		merged.Capacity.WarmSlots.Target += snapshot.Capacity.WarmSlots.Target
		merged.Capacity.WarmSlots.Ready += snapshot.Capacity.WarmSlots.Ready
		merged.Capacity.WarmSlots.Claimed += snapshot.Capacity.WarmSlots.Claimed
		merged.Capacity.WarmSlots.Preparing += snapshot.Capacity.WarmSlots.Preparing
		merged.Workspaces = append(merged.Workspaces, snapshot.Workspaces...)
		merged.Sessions = append(merged.Sessions, snapshot.Sessions...)
		merged.Sandboxes = append(merged.Sandboxes, snapshot.Sandboxes...)
	}
	return merged, nil
}

// fromOwningPool runs read against every pool concurrently and returns the
// result of the one pool that owns the identity; a pool answering 404 does
// not own it. An owner's answer wins over another pool's failure, so one
// pool's outage never blocks records the other owns. Without an owner, a
// failure is reported rather than not found, so an error never falls through
// to a different pool, and two owners are a conflict.
func fromOwningPool[T any](
	ctx context.Context,
	pools []*ControlClient,
	read func(context.Context, *ControlClient) (T, int, error),
) (T, *ControlClient, int, error) {
	type result struct {
		value  T
		status int
		err    error
	}
	var zero T
	results := make([]result, len(pools))
	if err := forEachBounded(ctx, len(pools), len(pools), func(ctx context.Context, i int) error {
		r := &results[i]
		r.value, r.status, r.err = read(ctx, pools[i])
		return nil
	}); err != nil {
		return zero, nil, 0, err
	}
	owner := -1
	for i := range results {
		if results[i].err != nil {
			continue
		}
		if owner != -1 {
			return zero, nil, http.StatusConflict, errors.New("identity belongs to multiple runtime pools")
		}
		owner = i
	}
	if owner != -1 {
		return results[owner].value, pools[owner], results[owner].status, nil
	}
	for _, r := range results {
		if r.status != http.StatusNotFound {
			return zero, nil, r.status, r.err
		}
	}
	return zero, nil, http.StatusNotFound, errors.New("runtime identity not found")
}

// sessionOwner resolves a Session from the Session listings, since the
// observer cannot read individual Session routing records.
func (s *CompleteSource) sessionOwner(ctx context.Context, id string) (*ControlClient, int, error) {
	_, owner, status, err := fromOwningPool(ctx, s.pools(), func(ctx context.Context, c *ControlClient) (struct{}, int, error) {
		found, err := c.hasSession(ctx, id)
		if err != nil {
			return struct{}{}, 0, err
		}
		if !found {
			return struct{}{}, http.StatusNotFound, errors.New("Session not found")
		}
		return struct{}{}, http.StatusOK, nil
	})
	return owner, status, err
}

func (s *CompleteSource) sandboxOwner(ctx context.Context, id string) (*ControlClient, int, error) {
	_, owner, status, err := fromOwningPool(ctx, s.pools(), func(ctx context.Context, c *ControlClient) (struct{}, int, error) {
		status, err := c.sandboxExists(ctx, id)
		return struct{}{}, status, err
	})
	return owner, status, err
}

func (s *CompleteSource) TerminateSession(ctx context.Context, id string) (Session, int, error) {
	if s.managed == nil {
		return s.ControlClient.TerminateSession(ctx, id)
	}
	client, status, err := s.sessionOwner(ctx, id)
	if err != nil {
		return Session{}, status, err
	}
	return client.TerminateSession(ctx, id)
}

func (s *CompleteSource) TerminateSandbox(ctx context.Context, id string) (Sandbox, int, error) {
	if s.managed == nil {
		return s.ControlClient.TerminateSandbox(ctx, id)
	}
	client, status, err := s.sandboxOwner(ctx, id)
	if err != nil {
		return Sandbox{}, status, err
	}
	return client.TerminateSandbox(ctx, id)
}

func (s *CompleteSource) CrashSandbox(ctx context.Context, id string) (Sandbox, int, error) {
	if s.managed == nil {
		return s.ControlClient.CrashSandbox(ctx, id)
	}
	client, status, err := s.sandboxOwner(ctx, id)
	if err != nil {
		return Sandbox{}, status, err
	}
	return client.CrashSandbox(ctx, id)
}

// AppBuilds reads the pool that owns the App.
func (s *CompleteSource) AppBuilds(ctx context.Context, id string) ([]AppBuild, int, error) {
	builds, _, status, err := fromOwningPool(ctx, s.pools(), func(ctx context.Context, c *ControlClient) ([]AppBuild, int, error) {
		return c.AppBuilds(ctx, id)
	})
	return builds, status, err
}

// RequestEvents reads the history of the pool that owns the scoped App or
// Workspace. Sequences are local to a pool, so a page never mixes pools.
func (s *CompleteSource) RequestEvents(ctx context.Context, query model.RequestEventQuery) (RequestEventPage, error) {
	if s.managed == nil {
		return s.ControlClient.RequestEvents(ctx, query)
	}
	page, _, status, err := fromOwningPool(ctx, s.pools(), func(ctx context.Context, c *ControlClient) (RequestEventPage, int, error) {
		page, err := c.RequestEvents(ctx, query)
		if err != nil {
			return page, 0, err
		}
		if len(page.Items) == 0 {
			return page, http.StatusNotFound, errNoRequestEvents
		}
		return page, http.StatusOK, nil
	})
	if status == http.StatusNotFound {
		return RequestEventPage{Items: []RequestEvent{}}, nil
	}
	return page, err
}

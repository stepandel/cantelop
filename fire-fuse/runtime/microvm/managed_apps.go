package microvm

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
	"sync"
	"time"
)

var ErrManagedAdmission = model.ErrManagedAdmission

type ManagedPolicy = model.ManagedPolicy
type ManagedGrant = model.ManagedGrant
type ManagedProjection = model.ManagedProjection
type ManagedState = model.ManagedState
type ManagedUsage = model.ManagedUsage

type managedStore interface {
	ReadManaged(string) (ManagedState, error)
	WriteManaged(string, ManagedState, *ManagedUsage) error
	ManagedUsage(string) ([]ManagedUsage, error)
	ManagedUsageExists(string) (bool, error)
	AckManagedUsage(string, string) error
	ListManagedSandboxes(string) ([]Sandbox, error)
	ReserveManagedStorage(string, string, int, int64) error
	ReleaseManagedStorage(string) error
}

func (s *Service) managedMachines(app string) ([]Sandbox, error) {
	if store, ok := s.store.(managedStore); ok {
		return store.ListManagedSandboxes(app)
	}
	return s.store.List()
}

func (s *Service) ReadManagedForUser(user, app string) (ManagedState, []ManagedUsage, error) {
	if _, err := s.store.GetAppForComputeTenant(user, app); err != nil {
		return ManagedState{}, nil, err
	}
	s.managedMu.Lock()
	defer s.managedMu.Unlock()
	store, ok := s.store.(managedStore)
	if !ok {
		return ManagedState{}, nil, ErrManagedAdmission
	}
	state, err := store.ReadManaged(app)
	if err != nil {
		return state, nil, err
	}
	usage, err := store.ManagedUsage(app)
	return state, usage, err
}
func (s *Service) AckManagedForUser(user, app, id string) error {
	if _, err := s.store.GetAppForComputeTenant(user, app); err != nil {
		return err
	}
	store, ok := s.store.(managedStore)
	if !ok {
		return ErrManagedAdmission
	}
	return store.AckManagedUsage(app, id)
}
func (s *Service) ProjectManagedForUser(user, app string, next ManagedProjection) error {
	s.resourceMu.Lock()
	defer s.resourceMu.Unlock()
	a, err := s.store.GetAppForComputeTenant(user, app)
	if err != nil {
		return err
	}
	if !a.Managed {
		return ErrManagedAdmission
	}
	p, g, now := next.Policy, next.Grant, s.now().UTC()
	if s.managedHealth != nil {
		if err := s.managedHealth(); err != nil {
			return err
		}
	}
	if next.Revision < 1 || p.MaxSandboxes < 0 || p.MaxSandboxes > 1000 || p.MaxVcpus < 0 || p.MaxVcpus > 10000 || p.MaxMemoryMiB < 0 || p.MaxMemoryMiB > 1000000 || p.MaxWorkspaces < 0 || p.MaxWorkspaces > 10000 || p.MaxStorageBytes < 0 {
		return ErrManagedAdmission
	}
	if p.Active && (s.managedStorage == nil || p.MaxWorkspaces < 1 || p.MaxStorageBytes/int64(p.MaxWorkspaces) < (1<<30)+(256<<20)) {
		return ErrManagedAdmission
	}
	storageChanged := true
	if storage, ok := s.store.(managedStore); ok {
		previous, e := storage.ReadManaged(app)
		if e != nil {
			return e
		}
		storageChanged = previous.Policy.MaxStorageBytes != p.MaxStorageBytes || previous.Policy.MaxWorkspaces != p.MaxWorkspaces
	}
	if p.Active && storageChanged {
		workspaces, e := s.store.ListWorkspaces(app)
		if e != nil {
			return e
		}
		if len(workspaces) > p.MaxWorkspaces {
			return ErrManagedAdmission
		}
		for _, w := range workspaces {
			if w.ArchivedAt == nil {
				if e = s.managedStorage(w.ID, p.MaxStorageBytes/int64(p.MaxWorkspaces)-(256<<20)); e != nil {
					return e
				}
			}
		}
	}
	rate := int64(p.MaxVcpus)*1024 + int64(p.MaxMemoryMiB)
	if p.Active && g.ID != "" && (g.HostID != s.managedHostID || len(g.ID) < 16 || len(g.ID) > 128 || len(g.Epoch) == 0 || g.Credits < 1 || !g.ExpiresAt.After(now.Add(5*time.Second)) || g.ExpiresAt.After(now.Add(60*time.Second)) || g.ExpiresAt.After(p.ExpiresAt) || g.Credits < rate*int64(g.ExpiresAt.Sub(now)/time.Second+1)) {
		return ErrManagedAdmission
	}
	now = s.now().UTC()
	if p.Active && g.ID != "" && !g.ExpiresAt.After(now.Add(5*time.Second)) {
		return ErrManagedAdmission
	}
	s.managedMu.Lock()
	store, ok := s.store.(managedStore)
	if !ok {
		s.managedMu.Unlock()
		return ErrManagedAdmission
	}
	old, err := store.ReadManaged(app)
	if err != nil {
		s.managedMu.Unlock()
		return err
	}
	if next.Revision < old.Revision {
		s.managedMu.Unlock()
		return ErrManagedAdmission
	}
	if next.Revision == old.Revision && old.Revision > 0 {
		x, _ := json.Marshal(p)
		y, _ := json.Marshal(old.Policy)
		if string(x) != string(y) {
			s.managedMu.Unlock()
			return ErrManagedAdmission
		}
	}
	if g.ID != "" && g.ID != old.Grant.ID {
		used, e := store.ManagedUsageExists(g.ID)
		if e != nil || used {
			s.managedMu.Unlock()
			return ErrManagedAdmission
		}
	}
	s.meterManaged(app, &old, now)
	var unused *ManagedUsage
	if g.ID != "" && !s.managedFits(app, p) {
		// A grant already installed is finalized below as the old grant.
		if g.ID != old.Grant.ID {
			unused = &ManagedUsage{ID: g.ID, HostID: g.HostID, Credits: 0}
		}
		g = ManagedGrant{}
		next.Grant = g
	}
	var usage *ManagedUsage
	if old.Grant.ID != "" && g.ID != old.Grant.ID {
		consumed := old.Consumed
		if g.ID == "" {
			consumed = min(old.Grant.Credits, old.Consumed+5*old.Rate)
		}
		usage = &ManagedUsage{ID: old.Grant.ID, HostID: old.Grant.HostID, Credits: consumed}
	}
	state := old
	if g.ID != old.Grant.ID {
		state = ManagedState{LastTick: now}
	}
	state.ManagedProjection = next
	state.Rate = s.managedRate(app)
	if err = store.WriteManaged(app, state, usage); err == nil {
		if unused != nil {
			err = store.WriteManaged(app, state, unused)
		}
		s.scheduleManagedLocked(app, state)
	}
	s.managedMu.Unlock()
	if err != nil {
		return err
	}
	if !p.Active || g.ID == "" || s.managedRate(app) > int64(p.MaxVcpus)*1024+int64(p.MaxMemoryMiB) {
		return s.stopManaged(app)
	}
	return nil
}
func (s *Service) managedRate(app string) int64 {
	machines, err := s.managedMachines(app)
	if err != nil {
		return 1 << 50
	}
	var rate int64
	for _, m := range machines {
		if m.AppID == app && managedActive(m) {
			rate += int64(m.VCPUs)*1024 + int64(m.MemoryMiB+SandboxHotplugMemoryMiB)
		}
	}
	return rate
}
func managedActive(m Sandbox) bool {
	return m.State == StateStarting || m.State == StateRunning || m.State == StateStopping || m.State == StateFailed
}
func (s *Service) meterManaged(app string, state *ManagedState, now time.Time) {
	if !state.LastTick.IsZero() {
		elapsed := now.Sub(state.LastTick)
		if elapsed < 0 || elapsed > 65*time.Second {
			state.Consumed = state.Grant.Credits
		} else if elapsed > 0 {
			if state.Rate > int64(state.Policy.MaxVcpus)*1024+int64(state.Policy.MaxMemoryMiB) {
				state.Consumed = state.Grant.Credits
				state.Rate = 0
			}
			n := elapsed.Nanoseconds()*state.Rate + state.Remainder
			state.Consumed = min(state.Grant.Credits, state.Consumed+n/int64(time.Second))
			state.Remainder = n % int64(time.Second)
		}
	}
	state.LastTick = now
	state.Rate = s.managedRate(app)
}
func (s *Service) managedAllowed(state ManagedState) bool {
	now := s.now()
	if s.managedHealth != nil && s.managedHealth() != nil {
		return false
	}
	return state.Policy.Active && now.Before(state.Policy.ExpiresAt) && now.Before(state.Grant.ExpiresAt.Add(-5*time.Second)) && state.Consumed < state.Grant.Credits && now.Sub(state.LastTick) < 65*time.Second && now.Sub(state.LastTick) >= 0
}
func (s *Service) checkManaged(m Sandbox) error {
	s.managedMu.Lock()
	defer s.managedMu.Unlock()
	if store, ok := s.store.(managedStore); ok {
		state, err := store.ReadManaged(m.AppID)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil || !s.managedAllowed(state) {
			return ErrManagedAdmission
		}
		return nil
	}
	a, err := s.store.GetAppRecord(m.AppID)
	if err != nil {
		return err
	}
	if a.Managed {
		return ErrManagedAdmission
	}
	return nil
}
func (s *Service) admitManaged(m Sandbox) (func(), error) {
	a, err := s.store.GetAppRecord(m.AppID)
	if err != nil {
		return nil, err
	}
	if !a.Managed {
		return func() {}, nil
	}
	s.managedMu.Lock()
	unlock := func() { s.managedMu.Unlock() }
	store, ok := s.store.(managedStore)
	if !ok {
		unlock()
		return nil, ErrManagedAdmission
	}
	state, err := store.ReadManaged(a.ID)
	if err != nil || state.Draining || !s.managedAllowed(state) {
		unlock()
		return nil, ErrManagedAdmission
	}
	machines, err := s.managedMachines(a.ID)
	if err != nil {
		unlock()
		return nil, err
	}
	count, cpus, mem := 1, m.VCPUs, m.MemoryMiB+SandboxHotplugMemoryMiB
	for _, old := range machines {
		if old.AppID == m.AppID && old.ID != m.ID && managedActive(old) {
			count++
			cpus += old.VCPUs
			mem += old.MemoryMiB + SandboxHotplugMemoryMiB
		}
	}
	p := state.Policy
	if count > p.MaxSandboxes || cpus > p.MaxVcpus || mem > p.MaxMemoryMiB {
		unlock()
		return nil, ErrManagedAdmission
	}
	return func() {
		s.meterManaged(a.ID, &state, s.now().UTC())
		if err := store.WriteManaged(a.ID, state, nil); err != nil {
			unlock()
			go s.stopManaged(a.ID)
			return
		}
		s.scheduleManagedLocked(a.ID, state)
		unlock()
	}, nil
}
func (s *Service) scheduleManagedLocked(app string, state ManagedState) {
	if timer := s.managedTimers[app]; timer != nil {
		timer.Stop()
	}
	if state.Rate == 0 {
		delete(s.managedTimers, app)
		return
	}
	s.managedTimers[app] = time.AfterFunc(time.Second, func() { s.tickManaged(app) })
}
func (s *Service) tickManaged(app string) {
	s.managedMu.Lock()
	store, ok := s.store.(managedStore)
	if !ok {
		s.managedMu.Unlock()
		return
	}
	state, err := store.ReadManaged(app)
	if err != nil {
		s.managedMu.Unlock()
		_ = s.stopManaged(app)
		return
	}
	s.meterManaged(app, &state, s.now().UTC())
	allowed := s.managedAllowed(state)
	err = store.WriteManaged(app, state, nil)
	s.scheduleManagedLocked(app, state)
	s.managedMu.Unlock()
	if !allowed || err != nil {
		_ = s.stopManaged(app)
	} else {
		machines, _ := s.managedMachines(app)
		for _, m := range machines {
			if m.State == StateFailed {
				ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
				_, _ = s.Terminate(ctx, m.ID)
				cancel()
			}
		}
	}
}
func (s *Service) stopManaged(app string) error {
	machines, err := s.managedMachines(app)
	if err != nil {
		return err
	}
	var failures []error
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, m := range machines {
		if m.AppID == app && managedActive(m) {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
				defer cancel()
				_, err := s.Terminate(ctx, id)
				if err != nil {
					mu.Lock()
					failures = append(failures, err)
					mu.Unlock()
				}
			}(m.ID)
		}
	}
	wg.Wait()
	return errors.Join(failures...)
}
func (s *Service) managedFits(app string, p ManagedPolicy) bool {
	machines, err := s.managedMachines(app)
	if err != nil {
		return false
	}
	count, cpus, memory := 0, 0, 0
	for _, m := range machines {
		if m.AppID == app && managedActive(m) {
			count++
			cpus += m.VCPUs
			memory += m.MemoryMiB + SandboxHotplugMemoryMiB
		}
	}
	return count <= p.MaxSandboxes && cpus <= p.MaxVcpus && memory <= p.MaxMemoryMiB
}

// Restart conservatively consumes old grants and stops native compute before
// either listener opens. A new grant must be obtained from the central ledger.
func (s *Service) RestoreManagedSchedules() error {
	apps, err := s.store.ListApps()
	if err != nil {
		return err
	}
	store, ok := s.store.(managedStore)
	for _, a := range apps {
		if !a.Managed {
			continue
		}
		if !ok {
			return ErrManagedAdmission
		}
		state, err := store.ReadManaged(a.ID)
		if err != nil {
			return err
		}
		state.Consumed = state.Grant.Credits
		state.LastTick = s.now().UTC()
		if err = store.WriteManaged(a.ID, state, nil); err != nil {
			return err
		}
		if err = s.stopManaged(a.ID); err != nil {
			return err
		}
	}
	return nil
}

// ConfigureManagedStorage is called once at startup. Managed Apps fail closed
// on hosts that have not installed filesystem quota support.
func (s *Service) ConfigureManagedStorage(quota func(string, int64) error) { s.managedStorage = quota }
func (s *Service) managedWorkspaceQuota(app, workspace string, count int) error {
	a, err := s.store.GetAppRecord(app)
	if err != nil {
		return err
	}
	if !a.Managed {
		return nil
	}
	store, ok := s.store.(managedStore)
	if !ok || s.managedStorage == nil {
		return ErrManagedAdmission
	}
	state, err := store.ReadManaged(app)
	if err != nil {
		return err
	}
	p := state.Policy
	if count >= p.MaxWorkspaces || p.MaxWorkspaces < 1 || !p.Active || !s.now().Before(p.ExpiresAt) {
		return ErrManagedAdmission
	}
	allocation := p.MaxStorageBytes / int64(p.MaxWorkspaces)
	// 80% files (minimum 1 GiB), 20% database (fixed maximum 256 MiB).
	if allocation < (1<<30)+(256<<20) {
		return ErrManagedAdmission
	}
	if err := store.ReserveManagedStorage(app, workspace, p.MaxWorkspaces, allocation); err != nil {
		return err
	}
	if err := s.managedStorage(workspace, allocation-(256<<20)); err != nil {
		// The Workspace does not exist yet; do not retain its slot.
		return errors.Join(err, store.ReleaseManagedStorage(workspace))
	}
	return nil
}

func (s *Service) ManagedActivityForUser(user, app string) (int, error) {
	if _, err := s.store.GetAppForComputeTenant(user, app); err != nil {
		return 0, err
	}
	machines, err := s.managedMachines(app)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range machines {
		if m.AppID == app && managedActive(m) {
			n++
		}
	}
	return n, nil
}

func (s *Service) ConfigureManagedHost(health func() error, hostID string) error {
	apps, err := s.store.ListApps()
	if err != nil {
		return err
	}
	for _, app := range apps {
		if !app.Managed {
			return errors.New("managed host contains unmanaged Apps")
		}
	}
	if hostID == "" {
		return ErrManagedAdmission
	}
	s.managedHostID = hostID
	s.managedOnly = true
	s.managedHealth = health
	return nil
}

func (s *Service) ManagedHostID() string { return s.managedHostID }

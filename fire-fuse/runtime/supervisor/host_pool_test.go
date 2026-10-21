package supervisor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/host/firecracker"
	"github.com/stepandel/cantelop/fire-fuse/host/network"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
	"github.com/stepandel/cantelop/fire-fuse/telemetry/warmuptrace"
)

func TestSupervisorPreparesRequestIndependentHostSlot(t *testing.T) {
	cfg := testConfig()
	var operations []string
	supervisor := newSupervisor(cfg, dependencies{
		newLayout: firecracker.NewJailLayout,
		prepareBase: func(
			_ context.Context,
			layout firecracker.JailLayout,
			kernel,
			rootfs string,
			uid,
			gid int,
		) error {
			operations = append(operations, "prepare base jail")
			if layout.VMID != "slot-01" || kernel != cfg.Profiles["default"].KernelImage ||
				rootfs != cfg.Profiles["default"].RootFSImage ||
				uid != cfg.JailerUID || gid != cfg.JailerGID {
				t.Errorf("unexpected base jail input: %#v %q %q %d:%d", layout, kernel, rootfs, uid, gid)
			}
			return nil
		},
		prepareRestore: func(
			_ context.Context,
			_ firecracker.JailLayout,
			state,
			memory string,
			_, _ int,
		) error {
			operations = append(operations, "prepare snapshot")
			if state != "/snapshots/template/vmstate" || memory != "/snapshots/template/memory" {
				t.Errorf("snapshot inputs = %q/%q", state, memory)
			}
			return nil
		},
		cleanup: func(layout firecracker.JailLayout) error {
			operations = append(operations, "clean jail")
			if layout.VMID != "slot-01" {
				t.Errorf("cleaned jail = %q", layout.VMID)
			}
			return nil
		},
		network: &networkDependencies{
			allocate: func(id string) (network.Allocation, error) {
				operations = append(operations, "allocate address")
				if id != "slot-01" {
					t.Errorf("allocated owner = %q", id)
				}
				return testAllocation(), nil
			},
			createTAP: func(_ context.Context, id string, _ network.Allocation, _, _ int) (string, error) {
				operations = append(operations, "create TAP")
				if id != "slot-01" {
					t.Errorf("TAP owner = %q", id)
				}
				return network.TAPName(id)
			},
			applyForwarding: func(context.Context, string, network.Allocation) error {
				operations = append(operations, "apply forwarding")
				return nil
			},
			removeForwarding: func(context.Context, string, network.Allocation) error {
				operations = append(operations, "remove forwarding")
				return nil
			},
			deleteTAP: func(_ context.Context, id string) error {
				operations = append(operations, "delete TAP")
				if id != "slot-01" {
					t.Errorf("deleted TAP owner = %q", id)
				}
				return nil
			},
			release: func(id string) error {
				operations = append(operations, "release address")
				if id != "slot-01" {
					t.Errorf("released owner = %q", id)
				}
				return nil
			},
		},
	})

	slot, err := supervisor.prepareHostSlot(
		context.Background(), "default", "slot-01", discardRuntimeCheckpoint,
	)
	if err != nil {
		t.Fatalf("prepareHostSlot() error = %v", err)
	}
	if slot.hostSlotID != "slot-01" || !slot.jailPrepared || slot.containerRootFSBound ||
		slot.network == nil || !slot.network.forwardingOwned {
		t.Fatalf("prepared slot = %#v", slot)
	}
	if err := supervisor.cleanupHostSlot(context.Background(), slot, discardRuntimeCheckpoint); err != nil {
		t.Fatalf("cleanupHostSlot() error = %v", err)
	}
	want := []string{
		"prepare base jail", "prepare snapshot", "allocate address", "create TAP",
		"apply forwarding", "remove forwarding", "delete TAP", "release address", "clean jail",
	}
	if fmt.Sprint(operations) != fmt.Sprint(want) {
		t.Fatalf("operations = %v, want %v", operations, want)
	}
}

func TestSupervisorRollsBackFailedHostSlotPreparation(t *testing.T) {
	wantErr := errors.New("forwarding failed")
	var cleaned []string
	supervisor := newSupervisor(testConfig(), dependencies{
		newLayout: firecracker.NewJailLayout,
		prepareBase: func(context.Context, firecracker.JailLayout, string, string, int, int) error {
			return nil
		},
		prepareRestore: func(context.Context, firecracker.JailLayout, string, string, int, int) error {
			return nil
		},
		cleanup: func(firecracker.JailLayout) error {
			cleaned = append(cleaned, "jail")
			return nil
		},
		network: &networkDependencies{
			allocate: func(string) (network.Allocation, error) { return testAllocation(), nil },
			createTAP: func(_ context.Context, id string, _ network.Allocation, _, _ int) (string, error) {
				return network.TAPName(id)
			},
			applyForwarding: func(context.Context, string, network.Allocation) error { return wantErr },
			removeForwarding: func(context.Context, string, network.Allocation) error {
				cleaned = append(cleaned, "forwarding")
				return nil
			},
			deleteTAP: func(context.Context, string) error {
				cleaned = append(cleaned, "TAP")
				return nil
			},
			release: func(string) error {
				cleaned = append(cleaned, "address")
				return nil
			},
		},
	})

	if slot, err := supervisor.prepareHostSlot(
		context.Background(), "default", "slot-01", discardRuntimeCheckpoint,
	); slot != nil || !errors.Is(err, wantErr) {
		t.Fatalf("prepareHostSlot() = %#v/%v, want nil/%v", slot, err, wantErr)
	}
	wantCleaned := []string{"forwarding", "TAP", "address", "jail"}
	if fmt.Sprint(cleaned) != fmt.Sprint(wantCleaned) {
		t.Fatalf("cleanup = %v, want %v", cleaned, wantCleaned)
	}
}

func TestSupervisorDurablyMaintainsAndClosesHostSlotPool(t *testing.T) {
	store := newTestStore(t)
	var cleaned atomic.Int64
	var observationMu sync.Mutex
	var observations []hostPoolObservation
	supervisor := newSupervisor(testConfig(), dependencies{
		newLayout:      firecracker.NewJailLayout,
		prepareBase:    func(context.Context, firecracker.JailLayout, string, string, int, int) error { return nil },
		prepareRestore: func(context.Context, firecracker.JailLayout, string, string, int, int) error { return nil },
		cleanup:        func(firecracker.JailLayout) error { cleaned.Add(1); return nil },
		recordHostPool: func(observation hostPoolObservation) {
			observationMu.Lock()
			defer observationMu.Unlock()
			observations = append(observations, observation)
		},
		network: &networkDependencies{
			allocate: func(string) (network.Allocation, error) { return testAllocation(), nil },
			createTAP: func(_ context.Context, id string, _ network.Allocation, _, _ int) (string, error) {
				return network.TAPName(id)
			},
			applyForwarding:  func(context.Context, string, network.Allocation) error { return nil },
			removeForwarding: func(context.Context, string, network.Allocation) error { return nil },
			deleteTAP:        func(context.Context, string) error { return nil },
			release:          func(string) error { return nil },
		},
	})
	if err := supervisor.ConfigureHostSlotPool(store, 1, time.Now); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.StartHostSlotPool(context.Background()); err != nil {
		t.Fatal(err)
	}
	readyCtx, cancelReady := context.WithTimeout(context.Background(), time.Second)
	defer cancelReady()
	if err := supervisor.WaitHostSlotPoolReady(readyCtx); err != nil {
		t.Fatal(err)
	}
	readySlots, err := store.ListHostSlots()
	if err != nil {
		t.Fatal(err)
	}
	if len(readySlots) != 1 || readySlots[0].State != microvm.HostSlotReady {
		t.Fatalf("ready slots = %#v", readySlots)
	}
	if err := supervisor.CloseHostSlotPool(context.Background()); err != nil {
		t.Fatal(err)
	}
	slots, err := store.ListHostSlots()
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 0 || cleaned.Load() != 1 {
		t.Fatalf("closed slots = %#v, cleanup count = %d", slots, cleaned.Load())
	}
	observationMu.Lock()
	defer observationMu.Unlock()
	if len(observations) != 2 || observations[0].Event != "prepare" || observations[1].Event != "cleanup" {
		t.Fatalf("host pool observations = %#v", observations)
	}
}

func TestHostSlotPoolReportsAggregateCapacity(t *testing.T) {
	pool := &hostSlotPool{
		targetPerProfile: 3,
		profiles:         []string{"large", "small"},
		available: map[string][]*instance{
			"large": {{}, {}},
			"small": {{}},
		},
		claimed:   map[string]int{"large": 1, "small": 1},
		preparing: map[string]int{"small": 1},
	}
	capacity := pool.capacity()
	if capacity.Target != 6 || capacity.Ready != 3 || capacity.Claimed != 2 || capacity.Preparing != 1 {
		t.Fatalf("capacity = %#v", capacity)
	}
}

func TestSupervisorStartClaimsPreparedSlotAndBindsSelectedImage(t *testing.T) {
	cfg := testConfig()
	cfg.Workspace = &config.WorkspaceConfig{
		ExportFSBin:   "/usr/sbin/exportfs",
		MountpointBin: "/usr/bin/mountpoint",
		Root:          "/var/lib/fire-fuse/workspaces",
	}
	machine := testMachine()
	layout, err := firecracker.NewJailLayout(cfg.RuntimeDir, cfg.FirecrackerBin, "slot-01")
	if err != nil {
		t.Fatal(err)
	}
	allocation := testAllocation()
	tapName, err := network.TAPName("slot-01")
	if err != nil {
		t.Fatal(err)
	}
	prepared := &instance{
		hostSlotID: "slot-01", layout: layout, jailPrepared: true,
		network: &instanceNetwork{
			addressAllocated: true, tapOwned: true, forwardingOwned: true,
			tapName: tapName, allocation: allocation,
		},
	}
	process := newFakeProcess()
	var boundImage string
	var poolHit bool
	supervisor := newSupervisor(cfg, dependencies{
		prepare: func(context.Context, firecracker.JailLayout, string, string, string, int, int) error {
			t.Fatal("synchronous jail preparation ran on a host-pool hit")
			return nil
		},
		bindContainer: func(_ context.Context, got firecracker.JailLayout, image string, _, _ int) error {
			if got != layout {
				t.Errorf("bound layout = %#v, want %#v", got, layout)
			}
			boundImage = image
			return nil
		},
		launch: func(context.Context, firecracker.JailLayout) (managedProcess, error) { return process, nil },
		recordStartup: func(_ string, _ bool, timings startupTimings) {
			poolHit = timings.HostPoolHit
		},
	})
	supervisor.hostPool = &hostSlotPool{
		targetPerProfile: 1,
		available:        map[string][]*instance{machine.Profile: {prepared}},
		claimed:          map[string]int{machine.Profile: 0},
		preparing:        map[string]int{machine.Profile: 0},
		wake:             map[string]chan struct{}{machine.Profile: make(chan struct{}, 1)},
		changed:          map[string]chan struct{}{machine.Profile: make(chan struct{})},
	}
	var checkpoints []microvm.RuntimeOwnership
	trace := warmuptrace.New()
	ctx := warmuptrace.WithContext(context.Background(), trace)
	runtime, err := supervisor.Start(ctx, machine, func(ownership microvm.RuntimeOwnership) error {
		checkpoints = append(checkpoints, ownership)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if boundImage != cfg.Images[machine.BuildID].RootFSImage {
		t.Fatalf("bound image = %q", boundImage)
	}
	if len(checkpoints) != 2 || checkpoints[0].HostSlotID != "slot-01" || !checkpoints[0].CgroupCreated ||
		!checkpoints[0].WorkspaceExported || !checkpoints[0].WorkspaceNFSAllowed ||
		runtime.Ownership.HostSlotID != "slot-01" || runtime.Ownership.Process == (microvm.ProcessIdentity{}) {
		t.Fatalf("checkpoints/runtime = %#v / %#v", checkpoints, runtime)
	}
	steps := make(map[string]bool)
	for _, step := range trace.Snapshot().Steps {
		steps[step.Name] = true
	}
	if !poolHit || !steps["host_pool.take_slot"] || !steps["host_pool.hit"] || steps["jail.prepare_assets"] {
		t.Fatalf("pool hit/trace = %t / %#v", poolHit, steps)
	}
	if steps["store.checkpoint_cgroup_claimed"] {
		t.Fatal("pool-hit trace contains a standalone cgroup checkpoint")
	}
	if steps["store.checkpoint_workspace_setup_claimed"] {
		t.Fatal("pool-hit trace contains a standalone workspace setup checkpoint")
	}
}

func TestSupervisorConfiguredPoolExhaustionDoesNotFallBack(t *testing.T) {
	cfg := testConfig()
	machine := testMachine()
	supervisor := newSupervisor(cfg, dependencies{
		prepare: func(context.Context, firecracker.JailLayout, string, string, string, int, int) error {
			t.Fatal("configured host-pool exhaustion fell back to synchronous preparation")
			return nil
		},
	})
	supervisor.hostPool = &hostSlotPool{
		targetPerProfile: 1,
		available:        map[string][]*instance{machine.Profile: nil},
		claimed:          map[string]int{machine.Profile: 1},
		preparing:        map[string]int{machine.Profile: 0},
		wake:             map[string]chan struct{}{machine.Profile: make(chan struct{}, 1)},
		changed:          map[string]chan struct{}{machine.Profile: make(chan struct{})},
	}

	_, err := supervisor.Start(context.Background(), machine, discardRuntimeCheckpoint)
	if !errors.Is(err, microvm.ErrAdmissionLimit) {
		t.Fatalf("Start() error = %v, want ErrAdmissionLimit", err)
	}
}

func TestSupervisorStopReplenishesClaimedCapacity(t *testing.T) {
	cfg := testConfig()
	machine := testMachine()
	prepared := make(chan string, 1)
	pool, err := newHostSlotPool(
		[]string{machine.Profile},
		1,
		func(_ context.Context, profile, hostID string) (*instance, error) {
			prepared <- profile
			return &instance{hostSlotID: hostID, hostProfile: profile, jailPrepared: true}, nil
		},
		func(context.Context, *instance) error { return nil },
		func() (string, error) { return "slot-replacement", nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	pool.claimed[machine.Profile] = 1
	if err := pool.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close(context.Background()) })

	allocation := testAllocation()
	owned := &instance{
		hostSlotID:          "slot-claimed",
		hostProfile:         machine.Profile,
		hostCapacityClaimed: true,
		jailPrepared:        true,
		network: &instanceNetwork{
			addressAllocated: true,
			tapOwned:         true,
			forwardingOwned:  true,
			tapName:          "ff-slot-claimed",
			allocation:       allocation,
		},
	}
	supervisor := newSupervisor(cfg, dependencies{
		cleanup: func(firecracker.JailLayout) error { return nil },
		network: &networkDependencies{
			removeForwarding: func(context.Context, string, network.Allocation) error { return nil },
			deleteTAP:        func(context.Context, string) error { return nil },
			release:          func(string) error { return nil },
		},
	})
	supervisor.hostPool = pool
	supervisor.slots[machine.ID] = &operationSlot{instance: owned}

	if err := supervisor.Stop(context.Background(), machine, discardRuntimeCheckpoint); err != nil {
		t.Fatal(err)
	}
	select {
	case profile := <-prepared:
		if profile != machine.Profile {
			t.Fatalf("prepared profile = %q, want %q", profile, machine.Profile)
		}
	default:
		t.Fatal("termination returned before released capacity was replenished")
	}
	if replacement, ok := pool.Take(machine.Profile); !ok || replacement.hostSlotID != "slot-replacement" {
		t.Fatalf("replacement = %#v/%t", replacement, ok)
	}
}

func TestHostSlotPoolReplenishesOnlyAfterClaimRelease(t *testing.T) {
	var sequence atomic.Int64
	prepared := make(chan string, 8)
	pool, err := newHostSlotPool(
		[]string{"small", "large"},
		2,
		func(_ context.Context, profile, hostID string) (*instance, error) {
			prepared <- profile
			return &instance{hostSlotID: hostID, jailPrepared: true}, nil
		},
		func(context.Context, *instance) error { return nil },
		func() (string, error) {
			return fmt.Sprintf("slot-%02d", sequence.Add(1)), nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close(context.Background()) })

	waitForPreparedSlots(t, prepared, 4)
	first, ok := pool.Take("small")
	if !ok || first.hostSlotID == "" {
		t.Fatalf("Take(small) = %#v/%t", first, ok)
	}
	select {
	case profile := <-prepared:
		t.Fatalf("prepared %q while claimed capacity was still active", profile)
	case <-time.After(25 * time.Millisecond):
	}
	if err := pool.Release("small"); err != nil {
		t.Fatal(err)
	}
	restored := make(chan error, 1)
	go func() { restored <- pool.WaitRestored(context.Background(), "small") }()
	waitForPreparedSlots(t, prepared, 1)
	select {
	case err := <-restored:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitRestored did not wait for replenishment")
	}
	if replacement, ok := pool.Take("small"); !ok || replacement.hostSlotID == first.hostSlotID {
		t.Fatalf("replacement Take(small) = %#v/%t", replacement, ok)
	}
	if _, ok := pool.Take("unknown"); ok {
		t.Fatal("Take(unknown) unexpectedly returned a slot")
	}
}

func TestHostSlotPoolRestorationWaitIsRetryable(t *testing.T) {
	var attempts atomic.Int64
	preparing := make(chan struct{}, 1)
	allowPreparation := make(chan struct{})
	var unblock sync.Once
	allow := func() { unblock.Do(func() { close(allowPreparation) }) }
	pool, err := newHostSlotPool(
		[]string{"small"},
		1,
		func(_ context.Context, _ string, hostID string) (*instance, error) {
			if attempts.Add(1) > 1 {
				preparing <- struct{}{}
				<-allowPreparation
			}
			return &instance{hostSlotID: hostID, jailPrepared: true}, nil
		},
		func(context.Context, *instance) error { return nil },
		func() (string, error) { return fmt.Sprintf("slot-%02d", attempts.Load()+1), nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		allow()
		_ = pool.Close(context.Background())
	})

	claimed := waitForPoolTake(t, pool, "small")
	if err := pool.Release("small"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-preparing:
	case <-time.After(time.Second):
		t.Fatal("replacement preparation did not start")
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	err = pool.WaitRestored(waitCtx, "small")
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first WaitRestored error = %v, want deadline exceeded", err)
	}

	allow()
	retryCtx, retryCancel := context.WithTimeout(context.Background(), time.Second)
	defer retryCancel()
	if err := pool.WaitRestored(retryCtx, "small"); err != nil {
		t.Fatalf("retry WaitRestored: %v", err)
	}
	replacement, ok := pool.Take("small")
	if !ok || replacement.hostSlotID == claimed.hostSlotID {
		t.Fatalf("replacement Take(small) = %#v/%t", replacement, ok)
	}
	if !pool.Return("small", replacement) {
		t.Fatal("Return(small) rejected the replacement slot")
	}
}

func TestHostSlotPoolRetriesPreparationFailure(t *testing.T) {
	wantErr := errors.New("prepare failed")
	var attempts atomic.Int64
	failures := make(chan error, 1)
	pool, err := newHostSlotPool(
		[]string{"small"},
		1,
		func(_ context.Context, _ string, hostID string) (*instance, error) {
			if attempts.Add(1) == 1 {
				return nil, wantErr
			}
			return &instance{hostSlotID: hostID, jailPrepared: true}, nil
		},
		func(context.Context, *instance) error { return nil },
		func() (string, error) { return "slot-retry", nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	pool.retryDelay = time.Millisecond
	pool.onPrepareFailure = func(_ string, err error) { failures <- err }
	if err := pool.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close(context.Background()) })

	select {
	case err := <-failures:
		if !errors.Is(err, wantErr) {
			t.Fatalf("failure = %v, want %v", err, wantErr)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for preparation failure")
	}
	waitForPoolTake(t, pool, "small")
}

func TestHostSlotPoolCloseCleansOnlyUnclaimedSlots(t *testing.T) {
	var sequence atomic.Int64
	prepared := make(chan string, 2)
	var mu sync.Mutex
	var cleaned []string
	pool, err := newHostSlotPool(
		[]string{"small"},
		2,
		func(_ context.Context, _ string, hostID string) (*instance, error) {
			prepared <- hostID
			return &instance{hostSlotID: hostID, jailPrepared: true}, nil
		},
		func(_ context.Context, slot *instance) error {
			mu.Lock()
			defer mu.Unlock()
			cleaned = append(cleaned, slot.hostSlotID)
			return nil
		},
		func() (string, error) {
			return fmt.Sprintf("slot-%02d", sequence.Add(1)), nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForPreparedSlots(t, prepared, 2)
	claimed, ok := pool.Take("small")
	if !ok {
		t.Fatal("expected one claimed slot")
	}
	if err := pool.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, id := range cleaned {
		if id == claimed.hostSlotID {
			t.Fatalf("claimed slot %q was cleaned by pool", id)
		}
	}
	if len(cleaned) == 0 {
		t.Fatal("Close did not clean any unclaimed slots")
	}
}

func TestHostSlotPoolCloseReportsLatePreparedSlotCleanupFailure(t *testing.T) {
	cleanupErr := errors.New("TAP device busy")
	preparing := make(chan struct{})
	pool, err := newHostSlotPool(
		[]string{"small"},
		1,
		func(ctx context.Context, _ string, hostID string) (*instance, error) {
			close(preparing)
			<-ctx.Done()
			// Preparation finishes after shutdown began, so the pool owns cleanup.
			return &instance{hostSlotID: hostID, jailPrepared: true}, nil
		},
		func(context.Context, *instance) error { return cleanupErr },
		func() (string, error) { return "slot-late", nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-preparing:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for preparation")
	}
	if err := pool.Close(context.Background()); !errors.Is(err, cleanupErr) {
		t.Fatalf("Close() error = %v, want late cleanup failure", err)
	}
}

func TestHostSlotPoolRejectsInvalidPreparedOwnership(t *testing.T) {
	failures := make(chan error, 1)
	cleaned := make(chan string, 1)
	pool, err := newHostSlotPool(
		[]string{"small"},
		1,
		func(context.Context, string, string) (*instance, error) {
			return &instance{hostSlotID: "wrong", jailPrepared: true}, nil
		},
		func(_ context.Context, slot *instance) error {
			cleaned <- slot.hostSlotID
			return nil
		},
		func() (string, error) { return "slot-valid", nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	pool.retryDelay = time.Hour
	pool.onPrepareFailure = func(_ string, err error) { failures <- err }
	if err := pool.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close(context.Background()) })
	select {
	case err := <-failures:
		if !errors.Is(err, errInvalidHostPool) {
			t.Fatalf("failure = %v, want errInvalidHostPool", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ownership validation")
	}
	select {
	case id := <-cleaned:
		if id != "wrong" {
			t.Fatalf("cleaned slot = %q, want wrong", id)
		}
	case <-time.After(time.Second):
		t.Fatal("invalid prepared slot was not cleaned")
	}
}

func waitForPreparedSlots(t *testing.T, prepared <-chan string, count int) {
	t.Helper()
	deadline := time.After(time.Second)
	for range count {
		select {
		case <-prepared:
		case <-deadline:
			t.Fatalf("timed out after %d prepared slots", count)
		}
	}
}

func waitForPoolTake(t *testing.T, pool *hostSlotPool, profile string) *instance {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if slot, ok := pool.Take(profile); ok {
			return slot
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s slot", profile)
	return nil
}

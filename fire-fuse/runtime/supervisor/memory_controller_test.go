package supervisor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/host/firecracker"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

type fakeMemoryClient struct {
	status     firecracker.MemoryHotplugStatus
	readErr    error
	requestErr error
	requests   []int
	onRead     func(context.Context)
}

func (f *fakeMemoryClient) ReadMemoryHotplug(ctx context.Context) (firecracker.MemoryHotplugStatus, error) {
	if f.onRead != nil {
		f.onRead(ctx)
	}
	return f.status, f.readErr
}

func (f *fakeMemoryClient) RequestMemoryHotplug(_ context.Context, target int) error {
	f.requests = append(f.requests, target)
	return f.requestErr
}

type memoryControllerFixture struct {
	c         *memoryController
	client    *fakeMemoryClient
	now       time.Time
	available int
	total     int
	probeErr  error
	logs      bytes.Buffer
}

func newMemoryControllerFixture() *memoryControllerFixture {
	f := &memoryControllerFixture{
		now: time.Date(2026, 10, 22, 19, 0, 0, 0, time.UTC), available: 200, total: 480,
		client: &fakeMemoryClient{status: firecracker.MemoryHotplugStatus{TotalSizeMiB: 4096}},
	}
	s := &Supervisor{
		config:          config.Config{Limits: config.LimitsConfig{MaxVMs: 2, MaxTotalMemoryMiB: 8192}},
		memoryRequested: map[string]int{"sandbox": 0, "peer": 0},
		memoryPlugged:   map[string]int{"sandbox": 0},
	}
	f.c = newMemoryController(s, "sandbox", f.client, func(context.Context) (int, int, error) {
		return f.available, f.total, f.probeErr
	})
	f.c.log = slog.New(slog.NewJSONHandler(&f.logs, nil)).With("sandbox_id", "sandbox")
	return f
}

func (f *memoryControllerFixture) allocation(requested, plugged int) {
	f.client.status.RequestedSizeMiB = requested
	f.client.status.PluggedSizeMiB = plugged
}

func (f *memoryControllerFixture) poll(t *testing.T, after time.Duration) {
	t.Helper()
	if !f.c.poll(context.Background(), f.now.Add(after)) {
		t.Fatal("controller stopped resizing")
	}
}

func TestMemoryControllerGrowsBeforeBurstExhaustsGuest(t *testing.T) {
	f := newMemoryControllerFixture()
	// 200 MiB was above the previous 128 MiB / 25% growth threshold.
	f.poll(t, 0)
	if f.c.desired != 512 || f.c.supervisor.memoryRequested["sandbox"] != 512 {
		t.Fatalf("burst headroom target/reservation = %d/%d", f.c.desired, f.c.supervisor.memoryRequested["sandbox"])
	}
	f.allocation(512, 256)
	f.poll(t, time.Second)
	if len(f.client.requests) != 1 || f.c.supervisor.pluggedMemory("sandbox") != 256 {
		t.Fatal("controller stacked growth before the prior resize completed")
	}
	f.allocation(512, 512)
	f.available, f.total = 64, 3072
	f.poll(t, 2*time.Second)
	if f.c.desired != 1536 {
		t.Fatalf("severe pressure should add 1 GiB, got hotplug target %d", f.c.desired)
	}
}

func TestMemoryControllerUsesSmallerGrowthWhenHostBudgetIsTight(t *testing.T) {
	f := newMemoryControllerFixture()
	f.c.supervisor.config.Limits.MaxTotalMemoryMiB = 1280 // 1024 boot + 256 hotplug
	f.poll(t, 0)
	if f.c.desired != 256 {
		t.Fatalf("remaining host capacity was not used: target %d", f.c.desired)
	}
	f.allocation(256, 256)
	f.poll(t, time.Second)
	if len(f.client.requests) != 1 || f.c.supervisor.reserveMemoryGrowth("peer", 256) {
		t.Fatal("growth exceeded shared host capacity")
	}
	if !strings.Contains(f.logs.String(), "denied by host capacity") {
		t.Fatal("capacity denial was not reported")
	}
}

func TestMemoryControllersShareHostBudgetDuringConcurrentBursts(t *testing.T) {
	first, second := newMemoryControllerFixture(), newMemoryControllerFixture()
	first.c.supervisor.config.Limits.MaxTotalMemoryMiB = 1792 // 1024 boot + 768 hotplug
	second.c.supervisor = first.c.supervisor
	second.c.id = "peer"
	var workers sync.WaitGroup
	for _, f := range []*memoryControllerFixture{first, second} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if !f.c.poll(context.Background(), f.now) {
				t.Error("controller stopped during concurrent growth")
			}
		}()
	}
	workers.Wait()
	if first.c.desired+second.c.desired != 768 || first.c.desired == 0 || second.c.desired == 0 {
		t.Fatalf("concurrent growth failed to share capacity: %d + %d", first.c.desired, second.c.desired)
	}
	reserved := first.c.supervisor.memoryRequested
	if reserved["sandbox"]+reserved["peer"] != 768 {
		t.Fatalf("shared reservations exceeded host budget: %v", reserved)
	}
}

func TestMemoryControllerKeepsExistingCeiling(t *testing.T) {
	f := newMemoryControllerFixture()
	f.c.desired = 3840
	f.c.supervisor.memoryRequested["sandbox"] = 3840
	f.allocation(3840, 3840)
	f.available, f.total = 64, 4300
	f.poll(t, 0)
	if f.c.desired != microvm.SandboxHotplugMemoryMiB {
		t.Fatalf("growth did not stop at existing ceiling: %d", f.c.desired)
	}
	f.allocation(4096, 4096)
	f.poll(t, time.Second)
	if len(f.client.requests) != 1 {
		t.Fatal("controller requested growth past the ceiling")
	}
}

func TestMemoryControllerGivesGuestProbeAnIndependentDeadline(t *testing.T) {
	f := newMemoryControllerFixture()
	apiStarted, probeStarted := make(chan struct{}), make(chan struct{})
	f.client.onRead = func(context.Context) {
		close(apiStarted)
		select {
		case <-probeStarted:
		case <-time.After(time.Second):
			t.Error("Firecracker status read waited for the guest probe")
		}
	}
	f.c.probe = func(ctx context.Context) (int, int, error) {
		close(probeStarted)
		select {
		case <-apiStarted:
		case <-time.After(time.Second):
			t.Error("guest probe waited for the Firecracker status read")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < memoryProbeTimeout-time.Second/2 {
			t.Error("guest did not receive its own probe timeout")
		}
		return 200, 480, nil
	}
	f.poll(t, 0)
	if len(f.client.requests) != 1 {
		t.Fatal("successful independent guest probe did not allow growth")
	}
}

func TestMemoryControllerProbeOutageGrantsHeadroomOnceAndKeepsUsageUnknown(t *testing.T) {
	f := newMemoryControllerFixture()
	f.probeErr = errors.New("guest probe timed out")
	f.poll(t, 0)
	f.poll(t, time.Second)
	if len(f.client.requests) != 0 {
		t.Fatal("a transient probe failure should not immediately allocate memory")
	}
	f.poll(t, 2*time.Second)
	if f.c.desired != 512 {
		t.Fatalf("prolonged probe outage did not receive bounded headroom: %d", f.c.desired)
	}
	f.allocation(512, 512)
	for i := 3; i < 10; i++ {
		f.poll(t, time.Duration(i)*time.Second)
	}
	if len(f.client.requests) != 1 || f.c.supervisor.pluggedMemory("sandbox") != 512 {
		t.Fatal("outage should grant headroom only once and still track device progress")
	}
	if f.c.supervisor.guestMemoryUsed("sandbox", f.now.Add(9*time.Second)) != nil {
		t.Fatal("missing guest probe was converted into a usage reading")
	}
	if strings.Count(f.logs.String(), "sandbox memory probe failed") != 1 {
		t.Fatal("probe warnings should be rate limited")
	}
	f.poll(t, time.Minute)
	if strings.Count(f.logs.String(), "sandbox memory probe failed") != 2 {
		t.Fatal("a prolonged outage should continue reporting failures")
	}
	f.probeErr = nil
	f.available, f.total = 600, 992
	f.poll(t, time.Minute+time.Second)
	if !strings.Contains(f.logs.String(), "sandbox memory probe recovered") {
		t.Fatal("guest probe recovery was not reported")
	}
}

func TestMemoryControllerIntermittentProbeOutagesDoNotRatchetOrBlockShrink(t *testing.T) {
	f := newMemoryControllerFixture()
	f.c.desired = 1024
	f.c.supervisor.memoryRequested["sandbox"] = 1024
	f.allocation(1024, 1024)
	f.available, f.total = 1300, 1472
	f.poll(t, 0)
	f.probeErr = errors.New("guest probe timed out")
	f.poll(t, time.Minute)
	f.probeErr = nil
	f.poll(t, 5*time.Minute)
	if f.c.desired != 768 {
		t.Fatalf("a brief probe miss restarted the shrink delay: %d", f.c.desired)
	}
	f.allocation(768, 768)
	for outage := 0; outage < 2; outage++ {
		f.probeErr = errors.New("guest probe timed out")
		start := 6*time.Minute + time.Duration(outage)*time.Minute
		for i := 0; i < memoryProbeFailureGrace; i++ {
			f.poll(t, start+time.Duration(i)*time.Second)
		}
		f.allocation(f.c.desired, f.c.desired)
		f.probeErr = nil
		f.poll(t, start+30*time.Second)
	}
	if f.c.desired != 1280 {
		t.Fatalf("repeated outages should share one headroom allowance until memory is reclaimed: %d", f.c.desired)
	}
}

func TestMemoryControllerRejectedGrowthKeepsResizing(t *testing.T) {
	f := newMemoryControllerFixture()
	f.client.requestErr = &firecracker.APIError{StatusCode: 400}
	f.poll(t, 0)
	f.poll(t, time.Second)
	if f.c.desired != 0 || strings.Count(f.logs.String(), "growth request rejected") != 1 {
		t.Fatal("a rejected growth request changed the target or was not rate limited")
	}
	f.client.requestErr = nil
	f.poll(t, 2*time.Second)
	if f.c.desired != 512 {
		t.Fatalf("growth did not resume after a definitive rejection: %d", f.c.desired)
	}
}

func TestMemoryControllerVMExitDuringGrowthIsNotALostReply(t *testing.T) {
	f := newMemoryControllerFixture()
	ctx, cancel := context.WithCancel(context.Background())
	f.client.requestErr = context.Canceled
	f.client.onRead = func(context.Context) {}
	f.c.client = cancelOnRequest{f.client, cancel}
	if !f.c.poll(ctx, f.now) || strings.Contains(f.logs.String(), "resizing stopped") {
		t.Fatal("VM exit during a growth request was reported as a lost reply")
	}
}

type cancelOnRequest struct {
	*fakeMemoryClient
	cancel context.CancelFunc
}

func (c cancelOnRequest) RequestMemoryHotplug(ctx context.Context, target int) error {
	c.cancel()
	return c.fakeMemoryClient.RequestMemoryHotplug(ctx, target)
}

func TestMemoryControllerOutageHeadroomStillRequiresHostCapacity(t *testing.T) {
	f := newMemoryControllerFixture()
	f.c.supervisor.config.Limits.MaxTotalMemoryMiB = 1024 // all reserved for boot
	f.probeErr = errors.New("guest unavailable")
	for i := 0; i < 4; i++ {
		f.poll(t, time.Duration(i)*time.Second)
	}
	if len(f.client.requests) != 0 || f.c.supervisor.memoryRequested["sandbox"] != 0 {
		t.Fatal("emergency headroom bypassed the host memory budget")
	}
}

func TestMemoryControllerShrinkNeedsContinuousSpareMemoryAndConfirmedRemoval(t *testing.T) {
	f := newMemoryControllerFixture()
	f.c.supervisor.config.Limits.MaxTotalMemoryMiB = 2048 // no spare capacity for outage headroom
	f.c.desired = 1024
	f.c.supervisor.memoryRequested["sandbox"] = 1024
	f.allocation(1024, 1024)
	f.available, f.total = 1300, 1472
	f.poll(t, 0)
	f.poll(t, 4*time.Minute)
	if len(f.client.requests) != 0 {
		t.Fatal("idle allocation shrank too early")
	}
	f.probeErr = errors.New("missing availability sample")
	for i := 1; i <= memoryProbeFailureGrace; i++ {
		f.poll(t, 4*time.Minute+time.Duration(i)*time.Second)
	}
	f.probeErr = nil
	f.poll(t, 5*time.Minute)
	f.poll(t, 9*time.Minute)
	if len(f.client.requests) != 0 {
		t.Fatal("unobserved time counted as sustained spare memory")
	}
	f.poll(t, 10*time.Minute)
	if f.c.desired != 768 || f.c.supervisor.memoryRequested["sandbox"] != 1024 {
		t.Fatal("shrink must keep the old reservation until memory is unplugged")
	}
	f.allocation(768, 896)
	f.poll(t, 10*time.Minute+time.Second)
	if f.c.supervisor.memoryRequested["sandbox"] != 1024 {
		t.Fatal("partial memory removal released capacity early")
	}
	f.allocation(768, 768)
	f.poll(t, 10*time.Minute+2*time.Second)
	if f.c.supervisor.memoryRequested["sandbox"] != 768 {
		t.Fatal("completed shrink did not release host capacity")
	}
}

func TestMemoryControllerCancelsPendingShrinkWhenPressureReturnsOrProbeFails(t *testing.T) {
	for _, missingProbe := range []bool{false, true} {
		t.Run(map[bool]string{false: "pressure", true: "missing probe"}[missingProbe], func(t *testing.T) {
			f := newMemoryControllerFixture()
			f.c.desired = 768
			f.c.supervisor.memoryRequested["sandbox"] = 1024
			f.allocation(768, 896)
			if missingProbe {
				f.probeErr = errors.New("guest unavailable")
			}
			f.poll(t, 0)
			if f.c.desired != 1024 || f.c.supervisor.memoryRequested["sandbox"] != 1024 {
				t.Fatalf("pending shrink was not cancelled: %d/%d", f.c.desired, f.c.supervisor.memoryRequested["sandbox"])
			}
		})
	}
}

func TestMemoryControllerLostGrowthReplyKeepsCapacityReserved(t *testing.T) {
	f := newMemoryControllerFixture()
	f.client.requestErr = context.DeadlineExceeded
	if f.c.poll(context.Background(), f.now) {
		t.Fatal("ambiguous growth reply must stop further writes")
	}
	if f.c.supervisor.memoryRequested["sandbox"] != 512 {
		t.Fatal("a lost growth reply released potentially allocated capacity")
	}
}

func TestMemoryControllerLostShrinkReplyReconcilesWithoutGuestProbe(t *testing.T) {
	f := newMemoryControllerFixture()
	f.c.desired = 1024
	f.c.supervisor.memoryRequested["sandbox"] = 1024
	f.allocation(1024, 1024)
	f.available, f.total = 1300, 1472
	f.poll(t, 0)
	f.client.requestErr = context.DeadlineExceeded
	f.poll(t, 5*time.Minute)
	if f.c.desired != 1024 || f.c.supervisor.memoryRequested["sandbox"] != 1024 {
		t.Fatal("failed shrink reply changed capacity accounting")
	}
	f.client.requestErr = nil
	f.probeErr = errors.New("guest unavailable")
	f.allocation(768, 768)
	f.poll(t, 5*time.Minute+time.Second)
	if f.c.desired != 768 || f.c.supervisor.memoryRequested["sandbox"] != 768 {
		t.Fatal("device-confirmed shrink should reconcile even without guest telemetry")
	}
}

func TestMemoryControllerReportsStalledResizeAndTracksProgressWithoutGuestProbe(t *testing.T) {
	f := newMemoryControllerFixture()
	f.poll(t, 0)
	f.allocation(512, 128)
	f.probeErr = errors.New("guest unavailable")
	f.poll(t, 15*time.Second)
	f.poll(t, 16*time.Second)
	if f.c.supervisor.pluggedMemory("sandbox") != 128 || f.c.supervisor.memoryRequested["sandbox"] != 512 {
		t.Fatal("missing probe prevented safe device accounting")
	}
	if strings.Count(f.logs.String(), "sandbox memory resize stalled") != 1 {
		t.Fatal("stalled resize warning was missing or not rate limited")
	}
	f.poll(t, 75*time.Second)
	if strings.Count(f.logs.String(), "sandbox memory resize stalled") != 2 {
		t.Fatal("prolonged resize stall was not reported again")
	}
}

func TestMemoryControllerNeverResizesWithoutVerifiedDeviceStatus(t *testing.T) {
	for _, failure := range []string{"unavailable API", "changed target", "changed ceiling"} {
		t.Run(failure, func(t *testing.T) {
			f := newMemoryControllerFixture()
			switch failure {
			case "unavailable API":
				f.client.readErr = errors.New("API unavailable")
			case "changed target":
				f.client.status.RequestedSizeMiB = 256
			case "changed ceiling":
				f.client.status.TotalSizeMiB = 8192
			}
			keepRunning := f.c.poll(context.Background(), f.now)
			if keepRunning != (failure == "unavailable API") {
				t.Fatal("incompatible device must stop resizing; an unavailable API should be retried")
			}
			if len(f.client.requests) != 0 {
				t.Fatal("resizing proceeded without verified device ownership")
			}
		})
	}
}

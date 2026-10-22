package supervisor

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/host/firecracker"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

const (
	memoryMinAvailableMiB    = 256
	memoryTargetAvailableMiB = 512
	memoryMaxGrowthMiB       = 1024
	memoryProbeFailureGrace  = 3
	memoryDiagnosticInterval = time.Minute
	memoryResizeWarnDelay    = 15 * time.Second
)

type memoryClient interface {
	ReadMemoryHotplug(context.Context) (firecracker.MemoryHotplugStatus, error)
	RequestMemoryHotplug(context.Context, int) error
}

// One controller owns the targets for one VM. Host reservations include an
// in-flight shrink until the device confirms completion, even without a guest
// availability sample. Guest usage still requires a fresh, successful probe.
type memoryController struct {
	supervisor *Supervisor
	id         string
	client     memoryClient
	probe      func(context.Context) (int, int, error)
	log        *slog.Logger

	desired           int
	unconfirmedShrink int
	idleSince         time.Time
	resizeSince       time.Time
	lastResizeWarning time.Time
	lastRejection     time.Time
	growthDenied      bool
	probeHeadroom     bool
	guestHealth       memoryProbeHealth
	apiHealth         memoryProbeHealth
}

func newMemoryController(s *Supervisor, id string, client memoryClient, probe func(context.Context) (int, int, error)) *memoryController {
	return &memoryController{
		supervisor: s, id: id, client: client, probe: probe,
		log: slog.Default().With("sandbox_id", id), unconfirmedShrink: -1,
	}
}

// poll returns false only when further writes cannot be safely reconciled.
func (c *memoryController) poll(ctx context.Context, now time.Time) bool {
	// The probes run concurrently with separate deadlines, so a slow Firecracker
	// request neither consumes nor delays the guest's probe.
	type guestReading struct {
		available, total int
		err              error
	}
	guestDone := make(chan guestReading, 1)
	go func() {
		guestCtx, cancelGuest := context.WithTimeout(ctx, memoryProbeTimeout)
		defer cancelGuest()
		available, total, err := c.probe(guestCtx)
		guestDone <- guestReading{available, total, err}
	}()
	apiCtx, cancelAPI := context.WithTimeout(ctx, memoryAPITimeout)
	status, statusErr := c.client.ReadMemoryHotplug(apiCtx)
	cancelAPI()
	guest := <-guestDone
	available, total, probeErr := guest.available, guest.total, guest.err
	if ctx.Err() != nil {
		return true
	}
	c.apiHealth.observe(c.log, now, "firecracker", statusErr)
	c.guestHealth.observe(c.log, now, "guest", probeErr)
	if probeErr == nil {
		c.supervisor.recordGuestMemory(c.id, available, total, now)
	}
	// Brief guest probe misses do not restart the shrink delay; failed polls
	// never shrink, and a sustained outage still resets it.
	probeOutage := c.guestHealth.failures >= memoryProbeFailureGrace
	if statusErr != nil || probeOutage {
		c.idleSince = time.Time{}
	}
	if statusErr != nil {
		return true
	}
	if status.TotalSizeMiB != microvm.SandboxHotplugMemoryMiB ||
		status.RequestedSizeMiB < 0 || status.RequestedSizeMiB > status.TotalSizeMiB ||
		status.PluggedSizeMiB < 0 || status.PluggedSizeMiB > status.TotalSizeMiB {
		c.log.Error("sandbox hotplug status is incompatible", "ceiling_mib", status.TotalSizeMiB,
			"requested_mib", status.RequestedSizeMiB, "plugged_mib", status.PluggedSizeMiB)
		return false
	}
	c.supervisor.memoryMu.Lock()
	c.supervisor.memoryPlugged[c.id] = status.PluggedSizeMiB
	c.supervisor.memoryMu.Unlock()
	if c.unconfirmedShrink >= 0 && status.RequestedSizeMiB == c.unconfirmedShrink {
		c.desired = c.unconfirmedShrink
	}
	c.unconfirmedShrink = -1
	if status.RequestedSizeMiB != c.desired {
		c.log.Error("sandbox memory target changed externally", "requested_mib", status.RequestedSizeMiB, "expected_mib", c.desired)
		return false
	}
	if status.PluggedSizeMiB != c.desired {
		c.idleSince = time.Time{}
		c.observeResize(now, status.PluggedSizeMiB)
		// Do not wait for memory removal while pressure returns or its probe is
		// unavailable. Reassert at least the still-plugged allocation instead.
		if status.PluggedSizeMiB > c.desired && (probeOutage || shouldGrowMemory(available, total)) {
			next := roundMemoryStep(status.PluggedSizeMiB)
			return c.grow(ctx, now, next, "cancel_shrink")
		}
		return true
	}
	if !c.resizeSince.IsZero() {
		c.log.Info("sandbox memory resize completed", "hotplug_mib", c.desired, "elapsed", now.Sub(c.resizeSince))
		c.resizeSince = time.Time{}
		c.lastResizeWarning = time.Time{}
	}
	c.supervisor.setMemoryReservation(c.id, c.desired)
	if probeErr != nil {
		// One bounded allowance until memory is next reclaimed can keep a
		// stressed guest alive long enough to answer again. Never infer usage,
		// shrink, or keep growing blindly. Every extra block still requires host
		// capacity.
		if probeOutage && !c.probeHeadroom {
			previous := c.desired
			keepRunning := c.grow(ctx, now, c.desired+memoryTargetAvailableMiB, "guest_probe_unavailable")
			c.probeHeadroom = c.desired > previous
			return keepRunning
		}
		return true
	}
	if shouldGrowMemory(available, total) {
		c.idleSince = time.Time{}
		return c.grow(ctx, now, c.desired+memoryGrowthMiB(available, total), "memory_pressure")
	}
	if c.desired == 0 || !shouldShrinkMemory(available, total) {
		c.idleSince = time.Time{}
		return true
	}
	if c.idleSince.IsZero() {
		c.idleSince = now
	}
	if now.Sub(c.idleSince) < memoryShrinkDelay {
		return true
	}
	next := c.desired - memoryStepMiB
	requestCtx, cancel := context.WithTimeout(ctx, memoryAPITimeout)
	err := c.client.RequestMemoryHotplug(requestCtx, next)
	cancel()
	c.idleSince = time.Time{}
	if err != nil {
		c.log.Warn("sandbox memory shrink request failed", "requested_mib", next, "error", err)
		c.unconfirmedShrink = next
		return true
	}
	c.desired = next
	c.resizeSince = now
	c.lastResizeWarning = time.Time{}
	c.probeHeadroom = false
	c.log.Info("sandbox memory shrink requested", "hotplug_mib", next)
	return true
}

func (c *memoryController) grow(ctx context.Context, now time.Time, target int, reason string) bool {
	target = min(target, microvm.SandboxHotplugMemoryMiB)
	if target <= c.desired {
		return true
	}
	next := target
	// A burst-sized request must not miss the last smaller block of available
	// host capacity. Reservation checks and updates remain atomic across VMs.
	for next > c.desired && !c.supervisor.reserveMemoryGrowth(c.id, next) {
		next -= memoryStepMiB
	}
	if next <= c.desired {
		if !c.growthDenied {
			c.log.Warn("sandbox memory growth denied by host capacity", "requested_mib", target, "reason", reason)
			c.growthDenied = true
		}
		return true
	}
	c.growthDenied = false
	requestCtx, cancel := context.WithTimeout(ctx, memoryAPITimeout)
	err := c.client.RequestMemoryHotplug(requestCtx, next)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return true // the VM exited; its reservations are released on return
		}
		var apiErr *firecracker.APIError
		if errors.Is(err, firecracker.ErrInvalidConfiguration) ||
			errors.As(err, &apiErr) && apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 {
			// Firecracker rejected the target, so nothing changed. The reservation
			// returns to the confirmed allocation once the device settles.
			if c.lastRejection.IsZero() || now.Sub(c.lastRejection) >= memoryDiagnosticInterval {
				c.log.Warn("sandbox memory growth request rejected", "requested_mib", next, "reason", reason, "error", err)
				c.lastRejection = now
			}
			return true
		}
		// The request may have reached Firecracker even when its reply was lost.
		// The caller keeps this reservation until the VM exits.
		c.log.Error("sandbox memory growth request failed; resizing stopped", "requested_mib", next, "reason", reason, "error", err)
		return false
	}
	c.desired = next
	c.resizeSince = now
	c.lastResizeWarning = time.Time{}
	c.idleSince = time.Time{}
	c.log.Info("sandbox memory growth requested", "hotplug_mib", next, "reason", reason)
	return true
}

func (c *memoryController) observeResize(now time.Time, plugged int) {
	if c.resizeSince.IsZero() {
		c.resizeSince = now
	}
	if now.Sub(c.resizeSince) >= memoryResizeWarnDelay &&
		(c.lastResizeWarning.IsZero() || now.Sub(c.lastResizeWarning) >= memoryDiagnosticInterval) {
		c.log.Warn("sandbox memory resize stalled", "requested_mib", c.desired, "plugged_mib", plugged, "elapsed", now.Sub(c.resizeSince))
		c.lastResizeWarning = now
	}
}

type memoryProbeHealth struct {
	failures int
	since    time.Time
	lastLog  time.Time
}

func (h *memoryProbeHealth) observe(log *slog.Logger, now time.Time, source string, err error) {
	if err == nil {
		if h.failures != 0 {
			log.Info("sandbox memory probe recovered", "source", source, "consecutive_failures", h.failures, "unavailable_for", now.Sub(h.since))
			*h = memoryProbeHealth{}
		}
		return
	}
	h.failures++
	if h.failures == 1 {
		h.since = now
	}
	if h.failures == 1 || now.Sub(h.lastLog) >= memoryDiagnosticInterval {
		log.Warn("sandbox memory probe failed", "source", source, "consecutive_failures", h.failures, "unavailable_for", now.Sub(h.since), "error", err)
		h.lastLog = now
	}
}

func shouldGrowMemory(available, total int) bool {
	return total > 0 && (available < memoryMinAvailableMiB || available*3 < total)
}

func memoryGrowthMiB(available, total int) int {
	headroom := max(memoryTargetAvailableMiB, total/2)
	return roundMemoryStep(min(memoryMaxGrowthMiB, max(memoryTargetAvailableMiB, headroom-available)))
}

func shouldShrinkMemory(available, total int) bool {
	return total > memoryStepMiB && available*10 > total*7 &&
		!shouldGrowMemory(available-memoryStepMiB, total-memoryStepMiB)
}

func roundMemoryStep(value int) int {
	return (value + memoryStepMiB - 1) / memoryStepMiB * memoryStepMiB
}

package supervisor

import (
	"context"
	"log/slog"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/telemetry/warmuptrace"
)

type workspaceLifecycleObservation struct {
	MachineID string
	Event     string
	Outcome   string
	Duration  time.Duration
	Err       error
}

type startupTimings struct {
	Total             time.Duration
	JailPrepare       time.Duration
	NetworkSetup      time.Duration
	FirecrackerLaunch time.Duration
	SnapshotRestore   time.Duration
	GuestAssignment   time.Duration
	HostPoolHit       bool
}

type hostPoolObservation struct {
	Profile  string
	Event    string
	Outcome  string
	Duration time.Duration
	Err      error
}

func observedDuration(started time.Time) time.Duration {
	return max(time.Since(started), time.Microsecond)
}

func traceValue[T any](ctx context.Context, name string, call func() (T, error)) (T, error) {
	finish := warmuptrace.Start(ctx, name)
	defer finish()
	return call()
}

func traceError(ctx context.Context, name string, call func() error) error {
	finish := warmuptrace.Start(ctx, name)
	defer finish()
	return call()
}

func traceAction(ctx context.Context, name string, call func()) {
	finish := warmuptrace.Start(ctx, name)
	defer finish()
	call()
}

func (s *Supervisor) observeHostPool(profile, event string, started time.Time, err error) {
	if s.deps.recordHostPool == nil {
		return
	}
	outcome := "succeeded"
	if err != nil {
		outcome = "failed"
	}
	s.deps.recordHostPool(hostPoolObservation{
		Profile: profile, Event: event, Outcome: outcome,
		Duration: time.Since(started), Err: err,
	})
}

// logStartup reports per-phase startup latency for completed microVM starts.
func logStartup(logger *slog.Logger) func(string, bool, startupTimings) {
	return func(machineID string, workspaceEnabled bool, timings startupTimings) {
		logger.Info(
			"microVM startup completed",
			"machine_id", machineID,
			"workspace_enabled", workspaceEnabled,
			"total_us", timings.Total.Microseconds(),
			"jail_prepare_us", timings.JailPrepare.Microseconds(),
			"network_setup_us", timings.NetworkSetup.Microseconds(),
			"firecracker_launch_us", timings.FirecrackerLaunch.Microseconds(),
			"snapshot_restore_us", timings.SnapshotRestore.Microseconds(),
			"guest_assignment_us", timings.GuestAssignment.Microseconds(),
			"host_pool_hit", timings.HostPoolHit,
		)
	}
}

func logHostPool(logger *slog.Logger) func(hostPoolObservation) {
	return func(observation hostPoolObservation) {
		arguments := []any{
			"profile", observation.Profile,
			"event", observation.Event,
			"outcome", observation.Outcome,
			"duration_us", observation.Duration.Microseconds(),
		}
		if observation.Err != nil {
			arguments = append(arguments, "error", observation.Err)
		}
		level := slog.LevelInfo
		if observation.Err != nil {
			level = slog.LevelError
		}
		logger.Log(context.Background(), level, "host slot pool event", arguments...)
	}
}

func logWorkspaceLifecycle(logger *slog.Logger) func(workspaceLifecycleObservation) {
	return func(observation workspaceLifecycleObservation) {
		arguments := []any{
			"machine_id", observation.MachineID,
			"event", observation.Event,
			"outcome", observation.Outcome,
			"duration_us", observation.Duration.Microseconds(),
		}
		if observation.Err != nil {
			arguments = append(arguments, "error", observation.Err)
		}
		level := slog.LevelInfo
		if observation.Outcome == "forced" {
			level = slog.LevelWarn
		}
		if observation.Outcome == "failed" {
			level = slog.LevelError
		}
		logger.Log(context.Background(), level, "workspace lifecycle event", arguments...)
	}
}

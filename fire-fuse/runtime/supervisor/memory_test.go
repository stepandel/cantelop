package supervisor

import (
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/config"
)

func TestGuestMemoryConsumptionFallsWithoutChangingAllocation(t *testing.T) {
	s := &Supervisor{memoryPlugged: map[string]int{"sandbox": 3584}}
	now := time.Now()
	s.recordGuestMemory("sandbox", 900, 4096, now)
	if used := s.guestMemoryUsed("sandbox", now); used == nil || *used != 3196<<20 {
		t.Fatalf("busy guest consumption = %v", used)
	}
	s.recordGuestMemory("sandbox", 3996, 4096, now)
	if used := s.guestMemoryUsed("sandbox", now); used == nil || *used != 100<<20 {
		t.Fatalf("idle guest consumption = %v", used)
	}
	if s.pluggedMemory("sandbox") != 3584 {
		t.Fatal("guest consumption must not change the allocated memory")
	}
}

func TestGuestMemoryConsumptionUnavailableAndZeroAreDistinct(t *testing.T) {
	s := &Supervisor{}
	now := time.Now()
	if s.guestMemoryUsed("missing", now) != nil {
		t.Fatal("missing observation must be unavailable")
	}
	s.recordGuestMemory("sandbox", 512, 512, now)
	if used := s.guestMemoryUsed("sandbox", now); used == nil || *used != 0 {
		t.Fatal("zero consumption is a valid measurement")
	}
	if s.guestMemoryUsed("sandbox", now.Add(memoryUsageMaxAge+time.Nanosecond)) != nil {
		t.Fatal("stale observation must be unavailable")
	}
	s.recordGuestMemory("invalid", 600, 512, now)
	if s.guestMemoryUsed("invalid", now) != nil {
		t.Fatal("invalid observation must not underflow to huge consumption")
	}
}

func TestMemoryGrowthReservesHostCapacity(t *testing.T) {
	s := &Supervisor{
		config:          config.Config{Limits: config.LimitsConfig{MaxVMs: 2, MaxTotalMemoryMiB: 1536}},
		memoryRequested: map[string]int{"first": 0, "second": 0},
	}
	if !s.reserveMemoryGrowth("first", 256) || !s.reserveMemoryGrowth("second", 256) {
		t.Fatal("initial growth should fit in remaining 512 MiB")
	}
	if s.reserveMemoryGrowth("first", 512) {
		t.Fatal("growth exceeded aggregate host capacity")
	}
	s.setMemoryReservation("second", 0)
	if !s.reserveMemoryGrowth("first", 512) {
		t.Fatal("completed shrink did not release capacity")
	}
}

func TestMemoryPressurePolicy(t *testing.T) {
	if !shouldGrowMemory(100, 512) || shouldGrowMemory(300, 512) {
		t.Fatal("growth threshold does not track guest availability")
	}
	if !shouldShrinkMemory(400, 512) || shouldShrinkMemory(200, 512) {
		t.Fatal("shrink threshold does not leave hysteresis")
	}
}

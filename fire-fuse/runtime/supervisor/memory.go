package supervisor

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/host/firecracker"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

const (
	memoryStatusPort   = 19095
	memoryPollInterval = time.Second
	memoryShrinkDelay  = 5 * time.Minute
	memoryStepMiB      = 256
	memoryProbeTimeout = 2 * time.Second
	memoryAPITimeout   = 2 * time.Second
	memoryUsageMaxAge  = 10 * time.Second
)

type guestMemorySample struct {
	usedBytes  uint64
	observedAt time.Time
}

// startMemoryController is called only after the restored guest is assigned.
// The process completion channel owns the lifetime of this loop, including
// when the sandbox is stopped or the VMM crashes.
func (s *Supervisor) startMemoryController(id string) {
	slot := s.lookupSlot(id)
	if slot == nil {
		return
	}
	slot.mu.Lock()
	owned := slot.instance
	if owned == nil || owned.process == nil || owned.network == nil {
		slot.mu.Unlock()
		return
	}
	guestIP := owned.network.allocation.GuestIP.String()
	apiSocket := owned.layout.HostAPISocket
	done := owned.process.Done()
	slot.mu.Unlock()

	s.memoryMu.Lock()
	if s.memoryRequested == nil {
		s.memoryRequested = make(map[string]int)
	}
	if s.memoryPlugged == nil {
		s.memoryPlugged = make(map[string]int)
	}
	if s.memoryGuestUsed == nil {
		s.memoryGuestUsed = make(map[string]guestMemorySample)
	}
	if _, exists := s.memoryRequested[id]; exists {
		s.memoryMu.Unlock()
		return
	}
	s.memoryRequested[id] = 0
	s.memoryPlugged[id] = 0
	s.memoryMu.Unlock()
	go s.controlMemory(id, guestIP, apiSocket, done)
}

func (s *Supervisor) controlMemory(id, guestIP, apiSocket string, done <-chan struct{}) {
	defer func() {
		s.memoryMu.Lock()
		delete(s.memoryRequested, id)
		delete(s.memoryPlugged, id)
		delete(s.memoryGuestUsed, id)
		s.memoryMu.Unlock()
	}()
	client, err := firecracker.NewClient(apiSocket)
	if err != nil {
		slog.Error("create sandbox memory controller", "sandbox_id", id, "error", err)
		return
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()
	controller := newMemoryController(s, id, client, func(ctx context.Context) (int, int, error) {
		return readGuestMemory(ctx, guestIP)
	})
	ticker := time.NewTicker(memoryPollInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if !controller.poll(ctx, time.Now()) {
			// An ambiguous growth reply or changed machine identity makes further
			// resizing unsafe. Keep its reservations until the process exits.
			<-done
			return
		}
		select {
		case <-done:
			return
		case <-ticker.C:
		}
	}
}

// Reserve against all possible boot allocations. This conservatively leaves
// room for every admitted VM to use its 512 MiB boot memory, even if some VM
// slots are currently idle. Growth remains safe alongside new admissions.
func (s *Supervisor) reserveMemoryGrowth(id string, target int) bool {
	s.memoryMu.Lock()
	defer s.memoryMu.Unlock()
	if _, exists := s.memoryRequested[id]; !exists {
		return false
	}
	budget := s.config.Limits.MaxTotalMemoryMiB - s.config.Limits.MaxVMs*microvm.DefaultSandboxMemoryMiB
	used := 0
	for _, value := range s.memoryRequested {
		used += value
	}
	current := s.memoryRequested[id]
	if target > microvm.SandboxHotplugMemoryMiB || target-current > budget-used {
		return false
	}
	s.memoryRequested[id] = target
	return true
}

func (s *Supervisor) setMemoryReservation(id string, target int) {
	s.memoryMu.Lock()
	defer s.memoryMu.Unlock()
	if _, exists := s.memoryRequested[id]; exists {
		s.memoryRequested[id] = target
	}
}

func (s *Supervisor) pluggedMemory(id string) int {
	s.memoryMu.Lock()
	defer s.memoryMu.Unlock()
	return s.memoryPlugged[id]
}

func (s *Supervisor) recordGuestMemory(id string, availableMiB, totalMiB int, observedAt time.Time) {
	if availableMiB < 0 || totalMiB < 1 || availableMiB > totalMiB {
		return
	}
	s.memoryMu.Lock()
	defer s.memoryMu.Unlock()
	if s.memoryGuestUsed == nil {
		s.memoryGuestUsed = make(map[string]guestMemorySample)
	}
	s.memoryGuestUsed[id] = guestMemorySample{
		usedBytes:  uint64(totalMiB-availableMiB) << 20,
		observedAt: observedAt,
	}
}

func (s *Supervisor) guestMemoryUsed(id string, now time.Time) *uint64 {
	s.memoryMu.Lock()
	defer s.memoryMu.Unlock()
	sample, exists := s.memoryGuestUsed[id]
	if !exists || now.Sub(sample.observedAt) > memoryUsageMaxAge {
		return nil
	}
	used := sample.usedBytes
	return &used
}

func readGuestMemory(ctx context.Context, guestIP string) (availableMiB, totalMiB int, err error) {
	address := net.JoinHostPort(guestIP, strconv.Itoa(memoryStatusPort))
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return 0, 0, err
	}
	defer connection.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	line, err := bufio.NewReader(connection).ReadString('\n')
	if err != nil {
		return 0, 0, err
	}
	fields := strings.Fields(line)
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("invalid guest memory response")
	}
	availableMiB, err = strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, err
	}
	totalMiB, err = strconv.Atoi(fields[1])
	if err != nil || availableMiB < 0 || totalMiB < 1 || availableMiB > totalMiB {
		return 0, 0, fmt.Errorf("invalid guest memory values")
	}
	return availableMiB, totalMiB, nil
}

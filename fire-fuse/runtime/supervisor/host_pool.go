package supervisor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/host/firecracker"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

const hostPoolRetryDelay = time.Second

var (
	errInvalidHostPool = errors.New("invalid host slot pool")
	errHostPoolStarted = errors.New("host slot pool already started")
)

type hostSlotPrepare func(context.Context, string, string) (*instance, error)
type hostSlotCleanup func(context.Context, *instance) error
type hostSlotIDGenerator func() (string, error)
type hostSlotPrepareFailure func(string, error)

// hostSlotPool keeps a fixed number of request-independent capacity units per
// profile. A unit is either ready, claimed by a sandbox, or being prepared.
// Claiming a ready slot does not create more capacity; Release is the only
// operation that permits the worker to prepare a replacement.
type hostSlotPool struct {
	targetPerProfile int
	profiles         []string
	prepare          hostSlotPrepare
	cleanup          hostSlotCleanup
	nextID           hostSlotIDGenerator
	onPrepareFailure hostSlotPrepareFailure
	retryDelay       time.Duration

	mu        sync.Mutex
	available map[string][]*instance
	claimed   map[string]int
	preparing map[string]int
	wake      map[string]chan struct{}
	changed   map[string]chan struct{}
	started   bool
	closed    bool
	cancel    context.CancelFunc
	workers   sync.WaitGroup
	ready     chan struct{}
	readyOnce sync.Once

	// lateCleanupErrors holds failures cleaning slots whose preparation
	// finished after shutdown began; Close reports them.
	lateCleanupErrors []error
}

func (p *hostSlotPool) capacity() microvm.HostCapacity {
	p.mu.Lock()
	defer p.mu.Unlock()
	capacity := microvm.HostCapacity{Target: p.targetPerProfile * len(p.profiles)}
	for _, profile := range p.profiles {
		capacity.Ready += len(p.available[profile])
		capacity.Claimed += p.claimed[profile]
		capacity.Preparing += p.preparing[profile]
	}
	return capacity
}

func (p *hostSlotPool) profileCapacity(profile string) microvm.HostCapacity {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.wake[profile]; !ok {
		return microvm.HostCapacity{}
	}
	return microvm.HostCapacity{Target: p.targetPerProfile, Ready: len(p.available[profile]),
		Claimed: p.claimed[profile], Preparing: p.preparing[profile]}
}

func newHostSlotPool(
	profiles []string,
	targetPerProfile int,
	prepare hostSlotPrepare,
	cleanup hostSlotCleanup,
	nextID hostSlotIDGenerator,
) (*hostSlotPool, error) {
	if targetPerProfile < 1 || len(profiles) == 0 || prepare == nil || cleanup == nil {
		return nil, errInvalidHostPool
	}
	if nextID == nil {
		nextID = randomHostSlotID
	}
	unique := make(map[string]struct{}, len(profiles))
	for _, profile := range profiles {
		if profile == "" {
			return nil, errInvalidHostPool
		}
		if _, exists := unique[profile]; exists {
			return nil, fmt.Errorf("%w: duplicate profile %q", errInvalidHostPool, profile)
		}
		unique[profile] = struct{}{}
	}
	profiles = append([]string(nil), profiles...)
	sort.Strings(profiles)
	pool := &hostSlotPool{
		targetPerProfile: targetPerProfile,
		profiles:         profiles,
		prepare:          prepare,
		cleanup:          cleanup,
		nextID:           nextID,
		retryDelay:       hostPoolRetryDelay,
		available:        make(map[string][]*instance, len(profiles)),
		claimed:          make(map[string]int, len(profiles)),
		preparing:        make(map[string]int, len(profiles)),
		wake:             make(map[string]chan struct{}, len(profiles)),
		changed:          make(map[string]chan struct{}, len(profiles)),
		ready:            make(chan struct{}),
	}
	for _, profile := range profiles {
		pool.wake[profile] = make(chan struct{}, 1)
		pool.changed[profile] = make(chan struct{})
	}
	return pool, nil
}

// Start launches one independent replenisher per profile.
func (p *hostSlotPool) Start(parent context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started {
		return errHostPoolStarted
	}
	if p.closed {
		return fmt.Errorf("%w: pool is closed", errInvalidHostPool)
	}
	ctx, cancel := context.WithCancel(parent)
	p.cancel = cancel
	p.started = true
	for _, profile := range p.profiles {
		p.workers.Add(1)
		go p.maintain(ctx, profile)
	}
	return nil
}

// Take claims one fixed-capacity unit without waiting. It deliberately does
// not wake the preparation worker: capacity remains consumed until Release.
func (p *hostSlotPool) Take(profile string) (*instance, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, false
	}
	available, exists := p.available[profile]
	if !exists || len(available) == 0 {
		return nil, false
	}
	slot := available[0]
	p.available[profile] = available[1:]
	p.claimed[profile]++
	p.notifyLocked(profile)
	return slot, true
}

// Return puts an unmodified, durably ready slot back after a claim failure.
func (p *hostSlotPool) Return(profile string, slot *instance) bool {
	if slot == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.wake[profile] == nil || p.claimed[profile] < 1 ||
		len(p.available[profile]) >= p.targetPerProfile {
		return false
	}
	p.claimed[profile]--
	p.available[profile] = append([]*instance{slot}, p.available[profile]...)
	p.notifyLocked(profile)
	return true
}

// Release returns one claimed capacity unit after its sandbox resources have
// been fully cleaned. The caller can separately wait for warm replacement via
// WaitRestored, which keeps release idempotent across termination retries.
func (p *hostSlotPool) Release(profile string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.wake[profile] == nil || p.claimed[profile] < 1 {
		return errInvalidHostPool
	}
	p.claimed[profile]--
	p.notifyLocked(profile)
	p.signalLocked(profile)
	return nil
}

// WaitRestored waits until all configured units for one profile are either
// ready or claimed and no replacement remains under preparation.
func (p *hostSlotPool) WaitRestored(ctx context.Context, profile string) error {
	p.mu.Lock()
	for {
		if p.closed || p.wake[profile] == nil {
			p.mu.Unlock()
			return errInvalidHostPool
		}
		if len(p.available[profile])+p.claimed[profile] == p.targetPerProfile &&
			p.preparing[profile] == 0 {
			p.mu.Unlock()
			return nil
		}
		changed := p.changed[profile]
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
		p.mu.Lock()
	}
}

func (p *hostSlotPool) Discard(ctx context.Context, slot *instance) error {
	if slot == nil {
		return nil
	}
	return p.cleanup(ctx, slot)
}

func (p *hostSlotPool) maintain(ctx context.Context, profile string) {
	defer p.workers.Done()
	for {
		p.mu.Lock()
		needsSlot := !p.closed && len(p.available[profile])+p.claimed[profile]+
			p.preparing[profile] < p.targetPerProfile
		wake := p.wake[profile]
		if needsSlot {
			p.preparing[profile]++
			p.notifyLocked(profile)
		}
		p.mu.Unlock()

		if !needsSlot {
			select {
			case <-ctx.Done():
				return
			case <-wake:
				continue
			}
		}

		hostID, err := p.nextID()
		if err == nil {
			if validateErr := firecracker.ValidateVMID(hostID); validateErr != nil {
				err = validateErr
			}
		}
		var slot *instance
		if err == nil {
			slot, err = p.prepare(ctx, profile, hostID)
			if err == nil && (slot == nil || slot.hostSlotID != hostID || !slot.jailPrepared) {
				err = fmt.Errorf("%w: prepared slot has inconsistent ownership", errInvalidHostPool)
			}
		}
		if err != nil {
			if slot != nil {
				err = errors.Join(err, p.cleanup(context.WithoutCancel(ctx), slot))
			}
			p.mu.Lock()
			p.preparing[profile]--
			p.notifyLocked(profile)
			p.mu.Unlock()
			if p.onPrepareFailure != nil && ctx.Err() == nil {
				p.onPrepareFailure(profile, err)
			}
			if !waitForHostPoolRetry(ctx, wake, p.retryDelay) {
				return
			}
			continue
		}

		p.mu.Lock()
		p.preparing[profile]--
		if p.closed || ctx.Err() != nil {
			p.notifyLocked(profile)
			p.mu.Unlock()
			if err := p.cleanup(context.WithoutCancel(ctx), slot); err != nil {
				p.mu.Lock()
				p.lateCleanupErrors = append(p.lateCleanupErrors, err)
				p.mu.Unlock()
			}
			return
		}
		p.available[profile] = append(p.available[profile], slot)
		p.notifyLocked(profile)
		p.markReadyLocked()
		p.mu.Unlock()
	}
}

func (p *hostSlotPool) markReadyLocked() {
	for _, profile := range p.profiles {
		if len(p.available[profile]) < p.targetPerProfile {
			return
		}
	}
	p.readyOnce.Do(func() { close(p.ready) })
}

func (p *hostSlotPool) WaitReady(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.ready:
		return nil
	}
}

func waitForHostPoolRetry(ctx context.Context, wake <-chan struct{}, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-wake:
		return true
	case <-timer.C:
		return true
	}
}

func (p *hostSlotPool) signalLocked(profile string) {
	wake, exists := p.wake[profile]
	if !exists {
		return
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (p *hostSlotPool) notifyLocked(profile string) {
	changed, exists := p.changed[profile]
	if !exists {
		return
	}
	close(changed)
	p.changed[profile] = make(chan struct{})
}

// Close stops replenishment and cleans every slot that was not claimed.
func (p *hostSlotPool) Close(ctx context.Context) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	if p.cancel != nil {
		p.cancel()
	}
	for _, profile := range p.profiles {
		p.notifyLocked(profile)
	}
	p.mu.Unlock()

	p.workers.Wait()
	p.mu.Lock()
	var slots []*instance
	for _, profile := range p.profiles {
		slots = append(slots, p.available[profile]...)
		p.available[profile] = nil
	}
	cleanupErrors := p.lateCleanupErrors
	p.lateCleanupErrors = nil
	p.mu.Unlock()

	for _, slot := range slots {
		if err := p.cleanup(ctx, slot); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	return errors.Join(cleanupErrors...)
}

func randomHostSlotID() (string, error) {
	var entropy [12]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", fmt.Errorf("generate host slot ID: %w", err)
	}
	return "slot-" + hex.EncodeToString(entropy[:]), nil
}

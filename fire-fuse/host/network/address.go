package network

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"sync"
)

const microVMSubnetBits = 30

var (
	ErrInvalidAddressPool   = errors.New("invalid microVM address pool")
	ErrAddressPoolExhausted = errors.New("microVM address pool exhausted")
	ErrInvalidAllocation    = errors.New("invalid microVM network allocation")
)

// Allocation assigns one private /30 subnet to a microVM. The first usable
// address belongs to the host TAP and the second belongs to the guest.
type Allocation struct {
	Subnet   netip.Prefix
	HostIP   netip.Addr
	GuestIP  netip.Addr
	GuestMAC string
}

func validateAllocation(allocation Allocation) error {
	if !allocation.Subnet.IsValid() ||
		!allocation.Subnet.Addr().Is4() ||
		!allocation.Subnet.Addr().IsPrivate() ||
		allocation.Subnet.Bits() != microVMSubnetBits ||
		allocation.Subnet != allocation.Subnet.Masked() {
		return fmt.Errorf("%w: subnet must be a canonical IPv4 /30", ErrInvalidAllocation)
	}
	if allocation.HostIP != addIPv4(allocation.Subnet.Addr(), 1) {
		return fmt.Errorf("%w: host IP must be the first usable address", ErrInvalidAllocation)
	}
	if allocation.GuestIP != addIPv4(allocation.Subnet.Addr(), 2) {
		return fmt.Errorf("%w: guest IP must be the second usable address", ErrInvalidAllocation)
	}
	return nil
}

// AddressAllocator owns active microVM address assignments from an IPv4 pool.
type AddressAllocator struct {
	mu          sync.Mutex
	pool        netip.Prefix
	slotCount   uint64
	allocations map[string]Allocation
	owners      map[uint64]string
}

// NewAddressAllocator divides a private IPv4 pool into one /30 subnet per VM.
func NewAddressAllocator(cidr string) (*AddressAllocator, error) {
	pool, err := netip.ParsePrefix(cidr)
	if err != nil || !pool.Addr().Is4() || pool != pool.Masked() {
		return nil, fmt.Errorf("%w: must be a canonical IPv4 CIDR", ErrInvalidAddressPool)
	}
	if pool.Bits() > microVMSubnetBits {
		return nil, fmt.Errorf("%w: pool must contain at least one /30 subnet", ErrInvalidAddressPool)
	}
	last := addIPv4(pool.Addr(), uint64(1)<<(32-pool.Bits())-1)
	if !pool.Addr().IsPrivate() || !last.IsPrivate() {
		return nil, fmt.Errorf("%w: pool must be entirely private", ErrInvalidAddressPool)
	}

	return &AddressAllocator{
		pool:        pool,
		slotCount:   uint64(1) << (microVMSubnetBits - pool.Bits()),
		allocations: make(map[string]Allocation),
		owners:      make(map[uint64]string),
	}, nil
}

// Allocate returns the existing assignment for a VM or reserves a free /30.
// Hashing gives each VM a stable preferred slot; linear probing resolves active
// collisions without ever assigning the same addresses twice.
func (a *AddressAllocator) Allocate(vmID string) (Allocation, error) {
	if !vmIDPattern.MatchString(vmID) {
		return Allocation{}, fmt.Errorf("%w: %q", ErrInvalidVMID, vmID)
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if allocation, exists := a.allocations[vmID]; exists {
		return allocation, nil
	}

	preferred := preferredSlot(vmID, a.slotCount)
	for offset := uint64(0); offset < a.slotCount; offset++ {
		slot := (preferred + offset) % a.slotCount
		if _, occupied := a.owners[slot]; occupied {
			continue
		}

		allocation := a.allocationForSlot(slot)
		a.allocations[vmID] = allocation
		a.owners[slot] = vmID
		return allocation, nil
	}

	return Allocation{}, ErrAddressPoolExhausted
}

// Release makes a VM's subnet available again. Releasing an unknown VM is safe.
func (a *AddressAllocator) Release(vmID string) error {
	if !vmIDPattern.MatchString(vmID) {
		return fmt.Errorf("%w: %q", ErrInvalidVMID, vmID)
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	allocation, exists := a.allocations[vmID]
	if !exists {
		return nil
	}
	delete(a.allocations, vmID)
	delete(a.owners, a.slotForSubnet(allocation.Subnet))
	return nil
}

func (a *AddressAllocator) allocationForSlot(slot uint64) Allocation {
	subnetAddress := addIPv4(a.pool.Addr(), slot*4)
	hostIP := addIPv4(subnetAddress, 1)
	guestIP := addIPv4(subnetAddress, 2)
	guestBytes := guestIP.As4()

	return Allocation{
		Subnet:  netip.PrefixFrom(subnetAddress, microVMSubnetBits),
		HostIP:  hostIP,
		GuestIP: guestIP,
		GuestMAC: fmt.Sprintf(
			"02:fc:%02x:%02x:%02x:%02x",
			guestBytes[0], guestBytes[1], guestBytes[2], guestBytes[3],
		),
	}
}

func (a *AddressAllocator) slotForSubnet(subnet netip.Prefix) uint64 {
	base := binary.BigEndian.Uint32(a.pool.Addr().AsSlice())
	address := binary.BigEndian.Uint32(subnet.Addr().AsSlice())
	return uint64(address-base) / 4
}

func preferredSlot(vmID string, slotCount uint64) uint64 {
	digest := sha256.Sum256([]byte(vmID))
	return binary.BigEndian.Uint64(digest[:8]) % slotCount
}

func addIPv4(address netip.Addr, offset uint64) netip.Addr {
	bytes := address.As4()
	value := uint64(binary.BigEndian.Uint32(bytes[:])) + offset
	return netip.AddrFrom4([4]byte{
		byte(value >> 24),
		byte(value >> 16),
		byte(value >> 8),
		byte(value),
	})
}

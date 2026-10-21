package network

import (
	"errors"
	"net/netip"
	"reflect"
	"strconv"
	"sync"
	"testing"
)

func TestAddressAllocatorAssignsStableAddresses(t *testing.T) {
	allocator, err := NewAddressAllocator("172.16.0.0/24")
	if err != nil {
		t.Fatalf("NewAddressAllocator() error = %v", err)
	}

	first, err := allocator.Allocate("vm-test")
	if err != nil {
		t.Fatalf("Allocate() error = %v", err)
	}
	again, err := allocator.Allocate("vm-test")
	if err != nil {
		t.Fatalf("second Allocate() error = %v", err)
	}
	if !reflect.DeepEqual(first, again) {
		t.Fatalf("allocation changed from %#v to %#v", first, again)
	}
	if first.Subnet.Bits() != microVMSubnetBits || !first.Subnet.Contains(first.HostIP) || !first.Subnet.Contains(first.GuestIP) {
		t.Errorf("allocation %#v is not contained in one /30", first)
	}
	if first.HostIP.Next() != first.GuestIP {
		t.Errorf("host IP %s and guest IP %s are not consecutive", first.HostIP, first.GuestIP)
	}
	if first.GuestMAC[:6] != "02:fc:" {
		t.Errorf("GuestMAC = %q, want locally administered 02:fc prefix", first.GuestMAC)
	}
}

func TestAddressAllocatorResolvesHashCollisions(t *testing.T) {
	allocator, err := NewAddressAllocator("192.168.50.0/29")
	if err != nil {
		t.Fatalf("NewAddressAllocator() error = %v", err)
	}

	firstID, secondID := collidingVMIDs(t, allocator.slotCount)
	first, err := allocator.Allocate(firstID)
	if err != nil {
		t.Fatalf("Allocate(%q) error = %v", firstID, err)
	}
	second, err := allocator.Allocate(secondID)
	if err != nil {
		t.Fatalf("Allocate(%q) error = %v", secondID, err)
	}
	if first.Subnet == second.Subnet || first.GuestIP == second.GuestIP || first.GuestMAC == second.GuestMAC {
		t.Fatalf("colliding IDs received overlapping allocations: %#v and %#v", first, second)
	}
}

func TestAddressAllocatorSupportsConcurrentAllocations(t *testing.T) {
	allocator, err := NewAddressAllocator("10.20.0.0/22")
	if err != nil {
		t.Fatalf("NewAddressAllocator() error = %v", err)
	}

	const count = 100
	allocations := make(chan Allocation, count)
	errorsFound := make(chan error, count)
	var callers sync.WaitGroup
	for index := 0; index < count; index++ {
		callers.Add(1)
		go func(vmID string) {
			defer callers.Done()
			allocation, err := allocator.Allocate(vmID)
			if err != nil {
				errorsFound <- err
				return
			}
			allocations <- allocation
		}("vm-" + strconv.Itoa(index))
	}
	callers.Wait()
	close(allocations)
	close(errorsFound)

	for err := range errorsFound {
		t.Errorf("Allocate() error = %v", err)
	}
	seen := make(map[netip.Prefix]struct{}, count)
	for allocation := range allocations {
		if _, exists := seen[allocation.Subnet]; exists {
			t.Errorf("duplicate concurrent allocation for subnet %s", allocation.Subnet)
		}
		seen[allocation.Subnet] = struct{}{}
	}
	if len(seen) != count {
		t.Errorf("unique allocations = %d, want %d", len(seen), count)
	}
}

func TestAddressAllocatorExhaustionAndRelease(t *testing.T) {
	allocator, err := NewAddressAllocator("10.0.0.0/30")
	if err != nil {
		t.Fatalf("NewAddressAllocator() error = %v", err)
	}
	first, err := allocator.Allocate("vm-first")
	if err != nil {
		t.Fatalf("Allocate(vm-first) error = %v", err)
	}
	if _, err := allocator.Allocate("vm-second"); !errors.Is(err, ErrAddressPoolExhausted) {
		t.Fatalf("Allocate(vm-second) error = %v, want ErrAddressPoolExhausted", err)
	}
	if err := allocator.Release("vm-first"); err != nil {
		t.Fatalf("Release(vm-first) error = %v", err)
	}
	if err := allocator.Release("vm-first"); err != nil {
		t.Fatalf("second Release(vm-first) error = %v", err)
	}
	second, err := allocator.Allocate("vm-second")
	if err != nil {
		t.Fatalf("Allocate(vm-second) after release error = %v", err)
	}
	if first.Subnet != second.Subnet {
		t.Errorf("reused Subnet = %s, want %s", second.Subnet, first.Subnet)
	}
}

func TestAddressAllocatorUsesSubnetBoundaries(t *testing.T) {
	allocator, err := NewAddressAllocator("172.31.255.248/29")
	if err != nil {
		t.Fatalf("NewAddressAllocator() error = %v", err)
	}

	for _, vmID := range []string{"vm-a", "vm-b"} {
		allocation, err := allocator.Allocate(vmID)
		if err != nil {
			t.Fatalf("Allocate(%q) error = %v", vmID, err)
		}
		if allocation.HostIP == allocation.Subnet.Addr() {
			t.Errorf("HostIP %s is the network address", allocation.HostIP)
		}
		broadcast := allocation.Subnet.Addr().Next().Next().Next()
		if allocation.GuestIP == broadcast {
			t.Errorf("GuestIP %s is the broadcast address", allocation.GuestIP)
		}
	}
}

func TestNewAddressAllocatorRejectsInvalidPools(t *testing.T) {
	for _, cidr := range []string{
		"not-a-cidr",
		"172.16.0.1/24",
		"172.16.0.0/31",
		"203.0.113.0/24",
		"fd00::/64",
	} {
		if _, err := NewAddressAllocator(cidr); !errors.Is(err, ErrInvalidAddressPool) {
			t.Errorf("NewAddressAllocator(%q) error = %v, want ErrInvalidAddressPool", cidr, err)
		}
	}
}

func TestAddressAllocatorRejectsInvalidVMIDs(t *testing.T) {
	allocator, err := NewAddressAllocator("10.0.0.0/24")
	if err != nil {
		t.Fatalf("NewAddressAllocator() error = %v", err)
	}
	if _, err := allocator.Allocate("../vm"); !errors.Is(err, ErrInvalidVMID) {
		t.Fatalf("Allocate() error = %v, want ErrInvalidVMID", err)
	}
	if err := allocator.Release("../vm"); !errors.Is(err, ErrInvalidVMID) {
		t.Fatalf("Release() error = %v, want ErrInvalidVMID", err)
	}
}

func collidingVMIDs(t *testing.T, slotCount uint64) (string, string) {
	t.Helper()
	seen := make(map[uint64]string)
	for index := 0; index < 100; index++ {
		id := "vm-collision-" + string(rune('a'+index))
		slot := preferredSlot(id, slotCount)
		if previous, exists := seen[slot]; exists {
			return previous, id
		}
		seen[slot] = id
	}
	t.Fatal("could not find colliding VM IDs")
	return "", ""
}

func TestAllocationValues(t *testing.T) {
	allocator, err := NewAddressAllocator("10.0.0.0/30")
	if err != nil {
		t.Fatalf("NewAddressAllocator() error = %v", err)
	}
	allocation, err := allocator.Allocate("vm-test")
	if err != nil {
		t.Fatalf("Allocate() error = %v", err)
	}
	want := Allocation{
		Subnet:   netip.MustParsePrefix("10.0.0.0/30"),
		HostIP:   netip.MustParseAddr("10.0.0.1"),
		GuestIP:  netip.MustParseAddr("10.0.0.2"),
		GuestMAC: "02:fc:0a:00:00:02",
	}
	if !reflect.DeepEqual(allocation, want) {
		t.Errorf("allocation = %#v, want %#v", allocation, want)
	}
}

func TestValidateAllocationRejectsInvalidInput(t *testing.T) {
	valid := Allocation{
		Subnet:  netip.MustParsePrefix("172.16.4.8/30"),
		HostIP:  netip.MustParseAddr("172.16.4.9"),
		GuestIP: netip.MustParseAddr("172.16.4.10"),
	}
	tests := []struct {
		name       string
		allocation Allocation
	}{
		{name: "missing subnet", allocation: Allocation{}},
		{name: "public subnet", allocation: Allocation{
			Subnet:  netip.MustParsePrefix("203.0.113.0/30"),
			HostIP:  netip.MustParseAddr("203.0.113.1"),
			GuestIP: netip.MustParseAddr("203.0.113.2"),
		}},
		{name: "wrong host", allocation: Allocation{Subnet: valid.Subnet, HostIP: valid.GuestIP, GuestIP: valid.GuestIP}},
		{name: "wrong guest", allocation: Allocation{Subnet: valid.Subnet, HostIP: valid.HostIP, GuestIP: valid.HostIP}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateAllocation(test.allocation); !errors.Is(err, ErrInvalidAllocation) {
				t.Fatalf("validateAllocation() error = %v, want ErrInvalidAllocation", err)
			}
		})
	}
}

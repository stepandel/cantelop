package microvm

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestSQLiteStorePersistsCreateOrGet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	at := time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC)
	machine := persistedStartingMachine("sandbox-first", at)
	created, isNew, err := store.CreateOrGet(machine)
	if err != nil || !isNew || created != machine {
		t.Fatalf("first CreateOrGet() = (%#v, %t, %v), want created sandbox", created, isNew, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	store, err = OpenSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen SQLite store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	replayed, isNew, err := store.CreateOrGet(machine)
	if err != nil || isNew || replayed != machine {
		t.Fatalf("replayed CreateOrGet() = (%#v, %t, %v), want original sandbox", replayed, isNew, err)
	}
	conflict := machine
	conflict.MemoryMiB++
	if _, _, err := store.CreateOrGet(conflict); !errors.Is(err, ErrSandboxConflict) {
		t.Fatalf("conflicting CreateOrGet() error = %v, want ErrSandboxConflict", err)
	}
}

func TestSQLiteStorePersistsCreateGetAndList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	createdAt := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	vmB := persistedStartingMachine("vm-b", createdAt)
	vmA := persistedStartingMachine("vm-a", createdAt.Add(time.Second))
	if err := store.Create(vmB); err != nil {
		t.Fatalf("Create(vm-b) error = %v", err)
	}
	if err := store.Create(vmA); err != nil {
		t.Fatalf("Create(vm-a) error = %v", err)
	}
	if err := store.Create(vmA); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("second Create(vm-a) error = %v, want ErrAlreadyExists", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	store, err = OpenSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen SQLite store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	got, err := store.Get(vmB.ID)
	if err != nil {
		t.Fatalf("Get(vm-b) error = %v", err)
	}
	if got != vmB {
		t.Errorf("Get(vm-b) = %#v, want %#v", got, vmB)
	}
	listed, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if want := []Sandbox{vmA, vmB}; !reflect.DeepEqual(listed, want) {
		t.Errorf("List() = %#v, want %#v", listed, want)
	}
}

func TestSandboxRecordRoundTripsCompleteRuntimeOwnership(t *testing.T) {
	at := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	network := NetworkAssignment{
		GuestIP: "172.16.0.2", GatewayIP: "172.16.0.1",
		Subnet: "172.16.0.0/30", GuestMAC: "02:fc:ac:10:00:02",
	}
	machine := Sandbox{
		ID: "vm-running", AppID: "app-test", WorkspaceID: "wsp_project123",
		Profile: "ubuntu-24", BuildID: testImageID, VCPUs: 2, MemoryMiB: 512,
		State: StateRunning, Network: network,
		Runtime: RuntimeOwnership{
			JailPrepared: true, AddressAllocated: true, TAPCreated: true,
			ForwardingApplied: true, TAPName: "ff0123456789abc", Network: network,
			Process: ProcessIdentity{
				PID: 123, BootID: "01234567-89ab-cdef-0123-456789abcdef", StartTimeTicks: 456,
			},
		},
		StartupCompletedAt: at.Add(time.Second), StartupDuration: time.Second,
		CreatedAt: at, UpdatedAt: at.Add(time.Second),
	}

	record := mustEncodeSandboxRecord(t, machine)
	decoded, err := decodeSandboxRecord(record)
	if err != nil {
		t.Fatalf("decodeSandboxRecord() error = %v", err)
	}
	if decoded != machine {
		t.Errorf("decodeSandboxRecord() = %#v, want %#v", decoded, machine)
	}
}

func TestSandboxRecordRejectsInvalidStartupMeasurement(t *testing.T) {
	at := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		change func(*Sandbox)
	}{
		{name: "missing completion", change: func(machine *Sandbox) { machine.StartupDuration = time.Second }},
		{name: "missing duration", change: func(machine *Sandbox) { machine.StartupCompletedAt = at.Add(time.Second) }},
		{name: "starting with measurement", change: func(machine *Sandbox) {
			machine.StartupCompletedAt, machine.StartupDuration = at.Add(time.Second), time.Second
			machine.UpdatedAt = at.Add(time.Second)
		}},
		{name: "duration mismatch", change: func(machine *Sandbox) {
			machine.State = StateFailed
			machine.Failure = Failure{Reason: FailureRuntimeStart}
			machine.StartupCompletedAt, machine.StartupDuration = at.Add(2*time.Second), time.Second
			machine.UpdatedAt = at.Add(2 * time.Second)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			machine := persistedStartingMachine("vm-startup", at)
			test.change(&machine)
			if _, err := encodeSandboxRecord(machine); !errors.Is(err, ErrInvalidSandboxRecord) {
				t.Fatalf("encodeSandboxRecord() error = %v, want ErrInvalidSandboxRecord", err)
			}
		})
	}
}

func TestSandboxRecordRejectsMissingImageIdentity(t *testing.T) {
	machine := persistedStartingMachine("vm-missing-image", time.Now().UTC())
	machine.BuildID = ""
	record, err := json.Marshal(machine)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeSandboxRecord(record); !errors.Is(err, ErrInvalidSandboxRecord) {
		t.Fatalf("decodeSandboxRecord() error = %v, want ErrInvalidSandboxRecord", err)
	}
}

func TestSQLiteStoreGetNotFound(t *testing.T) {
	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.Get("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() error = %v, want ErrNotFound", err)
	}
}

func TestSQLiteStoreRejectsInvalidMachine(t *testing.T) {
	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Create(Sandbox{ID: "vm-incomplete"}); !errors.Is(err, ErrInvalidSandboxRecord) {
		t.Fatalf("Create() error = %v, want ErrInvalidSandboxRecord", err)
	}
	items, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("len(List()) = %d, want 0", len(items))
	}
}

func TestSQLiteStoreRejectsCorruptOrMismatchedRecord(t *testing.T) {
	tests := []struct {
		name   string
		id     string
		state  State
		record string
	}{
		{name: "malformed JSON", id: "vm-a", state: StateStarting, record: `{"id":`},
		{name: "unknown field", id: "vm-a", state: StateStarting, record: `{"extra":true}`},
		{name: "trailing JSON", id: "vm-a", state: StateStarting, record: string(mustEncodeSandboxRecord(t, persistedStartingMachine("vm-a", time.Now().UTC()))) + `{}`},
		{name: "mismatched index", id: "vm-index", state: StateStarting, record: string(mustEncodeSandboxRecord(t, persistedStartingMachine("vm-record", time.Now().UTC())))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatalf("OpenSQLiteStore() error = %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if _, err := store.database.SQL().Exec(
				"INSERT INTO sandboxes (id, state, record) VALUES (?, ?, ?)",
				test.id, test.state, []byte(test.record),
			); err != nil {
				t.Fatalf("insert test record: %v", err)
			}
			if _, err := store.Get(test.id); !errors.Is(err, ErrInvalidSandboxRecord) {
				t.Fatalf("Get() error = %v, want ErrInvalidSandboxRecord", err)
			}
		})
	}
}

func persistedStartingMachine(id string, at time.Time) Sandbox {
	return Sandbox{
		ID: id, AppID: "app-test", WorkspaceID: "wsp_" + id, Profile: "ubuntu-24", BuildID: testImageID, VCPUs: 1, MemoryMiB: 128,
		State: StateStarting, CreatedAt: at, UpdatedAt: at,
	}
}

func mustEncodeSandboxRecord(t *testing.T, machine Sandbox) []byte {
	t.Helper()
	record, err := encodeSandboxRecord(machine)
	if err != nil {
		t.Fatalf("encodeSandboxRecord() error = %v", err)
	}
	return record
}

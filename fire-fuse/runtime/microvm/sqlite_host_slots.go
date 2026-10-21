package microvm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	sqlitepersistence "github.com/stepandel/cantelop/fire-fuse/runtime/persistence/sqlite"
)

func encodeHostSlotRecord(slot HostSlot) ([]byte, error) {
	if err := slot.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(slot)
}

func decodeHostSlotRecord(storedID string, storedState HostSlotState, record []byte) (HostSlot, error) {
	decoder := json.NewDecoder(bytes.NewReader(record))
	decoder.DisallowUnknownFields()
	var slot HostSlot
	if err := decoder.Decode(&slot); err != nil {
		return HostSlot{}, fmt.Errorf("%w: decode host slot: %v", ErrInvalidHostSlot, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return HostSlot{}, fmt.Errorf("%w: host slot record must contain one object", ErrInvalidHostSlot)
	}
	if slot.ID != storedID || slot.State != storedState {
		return HostSlot{}, fmt.Errorf("%w: indexed host slot identity differs", ErrInvalidHostSlot)
	}
	if err := slot.Validate(); err != nil {
		return HostSlot{}, err
	}
	return slot, nil
}

func (s *SQLiteStore) CreateHostSlot(slot HostSlot) error {
	record, err := encodeHostSlotRecord(slot)
	if err != nil {
		return err
	}
	created, err := s.hostSlotRecords.Create(sqlitepersistence.HostSlotRecord{
		ID: slot.ID, State: string(slot.State), Record: record,
	})
	if err != nil {
		return err
	}
	if !created {
		return fmt.Errorf("%w: %s", ErrAlreadyExists, slot.ID)
	}
	return nil
}

func (s *SQLiteStore) CheckpointHostSlot(id string, ownership RuntimeOwnership, at time.Time) (HostSlot, error) {
	if err := ownership.Validate(); err != nil {
		return HostSlot{}, err
	}
	return s.mutateHostSlot(id, func(slot HostSlot) (HostSlot, error) {
		if (slot.State != HostSlotPreparing && slot.State != HostSlotDraining) ||
			(ownership != (RuntimeOwnership{}) && ownership.HostSlotID != id) {
			return HostSlot{}, ErrInvalidHostSlot
		}
		slot.Runtime, slot.UpdatedAt = ownership, at
		return slot, nil
	})
}

func (s *SQLiteStore) ReadyHostSlot(id string, at time.Time) (HostSlot, error) {
	return s.mutateHostSlot(id, func(slot HostSlot) (HostSlot, error) {
		if slot.State != HostSlotPreparing {
			return HostSlot{}, ErrInvalidHostSlot
		}
		slot.State, slot.UpdatedAt = HostSlotReady, at
		return slot, nil
	})
}

func (s *SQLiteStore) DrainHostSlot(id string, at time.Time) (HostSlot, error) {
	return s.mutateHostSlot(id, func(slot HostSlot) (HostSlot, error) {
		slot.State, slot.UpdatedAt = HostSlotDraining, at
		return slot, nil
	})
}

func (s *SQLiteStore) ListHostSlots() ([]HostSlot, error) {
	records, err := s.hostSlotRecords.List()
	if err != nil {
		return nil, err
	}
	slots := make([]HostSlot, 0, len(records))
	for _, record := range records {
		slot, err := decodeHostSlotRecord(record.ID, HostSlotState(record.State), record.Record)
		if err != nil {
			return nil, err
		}
		slots = append(slots, slot)
	}
	return slots, nil
}

func (s *SQLiteStore) DeleteHostSlot(id string) error {
	err := s.hostSlotRecords.Delete(id, func(record sqlitepersistence.HostSlotRecord) error {
		slot, err := decodeHostSlotRecord(record.ID, HostSlotState(record.State), record.Record)
		if err != nil {
			return err
		}
		if slot.Runtime != (RuntimeOwnership{}) {
			return fmt.Errorf("%w: slot still owns resources", ErrInvalidHostSlot)
		}
		return nil
	})
	if errors.Is(err, sqlitepersistence.ErrHostSlotRecordNotFound) {
		return fmt.Errorf("%w: %s", ErrHostSlotNotFound, id)
	}
	return err
}

func (s *SQLiteStore) ClaimHostSlot(machineID, slotID string, ownership RuntimeOwnership, at time.Time) (Sandbox, error) {
	if err := ownership.Validate(); err != nil {
		return Sandbox{}, err
	}
	record, err := s.hostSlotRecords.Claim(machineID, slotID, func(
		sandboxRecord sqlitepersistence.SandboxRecord,
		slotRecord sqlitepersistence.HostSlotRecord,
	) (sqlitepersistence.SandboxRecord, error) {
		machine, err := decodeSQLiteSandbox(sandboxRecord.ID, sandboxRecord.State, sandboxRecord.Record)
		if err != nil {
			return sqlitepersistence.SandboxRecord{}, err
		}
		slot, err := decodeHostSlotRecord(slotRecord.ID, HostSlotState(slotRecord.State), slotRecord.Record)
		if err != nil {
			return sqlitepersistence.SandboxRecord{}, err
		}
		if machine.State != StateStarting || machine.Runtime != (RuntimeOwnership{}) ||
			slot.State != HostSlotReady || !matchesHostSlotClaim(slot.Runtime, ownership) || ownership.HostSlotID != slotID {
			return sqlitepersistence.SandboxRecord{}, ErrInvalidTransition
		}
		machine.Runtime, machine.UpdatedAt = ownership, at
		next, err := encodeSandboxRecord(machine)
		if err != nil {
			return sqlitepersistence.SandboxRecord{}, err
		}
		return sqlitepersistence.SandboxRecord{ID: machine.ID, State: machine.State, Record: next}, nil
	})
	if errors.Is(err, sqlitepersistence.ErrSandboxRecordNotFound) {
		return Sandbox{}, fmt.Errorf("%w: %s", ErrNotFound, machineID)
	}
	if errors.Is(err, sqlitepersistence.ErrHostSlotRecordNotFound) {
		return Sandbox{}, fmt.Errorf("%w: %s", ErrHostSlotNotFound, slotID)
	}
	if err != nil {
		return Sandbox{}, err
	}
	return decodeSQLiteSandbox(record.ID, record.State, record.Record)
}

func (s *SQLiteStore) mutateHostSlot(id string, change func(HostSlot) (HostSlot, error)) (HostSlot, error) {
	record, err := s.hostSlotRecords.Mutate(id, func(current sqlitepersistence.HostSlotRecord) (sqlitepersistence.HostSlotRecord, error) {
		slot, err := decodeHostSlotRecord(current.ID, HostSlotState(current.State), current.Record)
		if err != nil {
			return sqlitepersistence.HostSlotRecord{}, err
		}
		next, err := change(slot)
		if err != nil {
			return sqlitepersistence.HostSlotRecord{}, err
		}
		nextRecord, err := encodeHostSlotRecord(next)
		if err != nil {
			return sqlitepersistence.HostSlotRecord{}, err
		}
		return sqlitepersistence.HostSlotRecord{ID: next.ID, State: string(next.State), Record: nextRecord}, nil
	})
	if errors.Is(err, sqlitepersistence.ErrHostSlotRecordNotFound) {
		return HostSlot{}, fmt.Errorf("%w: %s", ErrHostSlotNotFound, id)
	}
	if err != nil {
		return HostSlot{}, err
	}
	return decodeHostSlotRecord(record.ID, HostSlotState(record.State), record.Record)
}

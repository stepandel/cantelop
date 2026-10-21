package observability

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSpoolPersistsAndAcknowledgesBatch(t *testing.T) {
	spool := testSpool(t, 2<<20, time.Hour)
	event := NewEvent("msg_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa:accepted", "message.accepted", "app_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", time.Now())
	if err := spool.Record(event); err != nil {
		t.Fatal(err)
	}
	entry, err := spool.Next()
	if err != nil || entry == nil {
		t.Fatalf("Next() = %#v, %v", entry, err)
	}
	if err := spool.Acknowledge(entry); err != nil {
		t.Fatal(err)
	}
	if next, err := spool.Next(); err != nil || next != nil {
		t.Fatalf("Next() after acknowledge = %#v, %v", next, err)
	}
}

func TestSpoolRejectsNewBatchAtBound(t *testing.T) {
	spool := testSpool(t, maxBatchBytes, time.Hour)
	spool.maxBytes = 1
	if err := spool.Record(NewEvent("event", "message.accepted", "app_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", time.Now())); !errors.Is(err, ErrSpoolFull) {
		t.Fatalf("Record() error = %v", err)
	}
}

func TestSpoolPrunesExpiredBatches(t *testing.T) {
	spool := testSpool(t, 2<<20, time.Minute)
	now := time.Now().UTC()
	spool.now = func() time.Time { return now.Add(-2 * time.Minute) }
	if err := spool.Record(NewEvent("event", "message.accepted", "app_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", now)); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(spool.directory, "*.json"))
	if err := os.Chtimes(files[0], now.Add(-2*time.Minute), now.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	spool.now = func() time.Time { return now }
	if entry, err := spool.Next(); err != nil || entry != nil {
		t.Fatalf("Next() = %#v, %v", entry, err)
	}
}

func TestSpoolGroupCommitsConcurrentRecords(t *testing.T) {
	spool := testSpool(t, 64<<20, time.Hour)
	const writers = 64
	var group sync.WaitGroup
	errs := make(chan error, writers)
	for index := range writers {
		group.Add(1)
		go func() {
			defer group.Done()
			errs <- spool.Record(NewEvent(fmt.Sprint("event-", index), "message.accepted", "app_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", time.Now()))
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	files, _ := filepath.Glob(filepath.Join(spool.directory, "*.json"))
	if len(files) < 1 || len(files) > writers || len(files) != len(spool.files) {
		t.Fatalf("files = %d, index = %d", len(files), len(spool.files))
	}
	seen := map[string]bool{}
	for {
		entries, err := spool.Collect()
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) == 0 {
			break
		}
		for _, entry := range entries {
			for _, batch := range entry.batches {
				for _, raw := range batch.events {
					var event Event
					if err := json.Unmarshal(raw, &event); err != nil {
						t.Fatal(err)
					}
					seen[event.EventID] = true
				}
			}
		}
		if err := spool.Acknowledge(entries...); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != writers || spool.used != 0 {
		t.Fatalf("delivered %d events, %d bytes still accounted", len(seen), spool.used)
	}
}

func TestSpoolReopenRestoresQueueAndUsage(t *testing.T) {
	spool := testSpool(t, 2<<20, time.Hour)
	for _, id := range []string{"first", "second"} {
		if err := spool.Record(NewEvent(id, "message.accepted", "app_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", time.Now())); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := OpenSpool(SpoolConfig{Directory: spool.directory, MaxBytes: spool.maxBytes, MaxAge: spool.maxAge})
	if err != nil {
		t.Fatal(err)
	}
	if reopened.used != spool.used || len(reopened.files) != 2 {
		t.Fatalf("reopened used %d of %d bytes with %d files", reopened.used, spool.used, len(reopened.files))
	}
	entry, err := reopened.Next()
	if err != nil || entry == nil || !strings.Contains(string(entry.Data), `"first"`) {
		t.Fatalf("Next() = %v, %v", entry, err)
	}
}

func TestSpoolReadsLegacySingleBatchFile(t *testing.T) {
	spool := testSpool(t, 2<<20, time.Hour)
	data, err := encodeBatch([]Event{NewEvent("legacy", "message.accepted", "app_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", time.Now())})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spool.directory, "00000000000000000001-0000000000000000.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSpool(SpoolConfig{Directory: spool.directory, MaxBytes: spool.maxBytes, MaxAge: spool.maxAge})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := reopened.Collect()
	if err != nil || len(entries) != 1 || len(entries[0].batches) != 1 || len(entries[0].batches[0].events) != 1 {
		t.Fatalf("Collect() = %v, %v", entries, err)
	}
}

func TestSpoolDropsInvalidStoredFile(t *testing.T) {
	spool := testSpool(t, 2<<20, time.Hour)
	if err := os.WriteFile(filepath.Join(spool.directory, "00000000000000000001-0000000000000000.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSpool(SpoolConfig{Directory: spool.directory, MaxBytes: spool.maxBytes, MaxAge: spool.maxAge})
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Record(NewEvent("valid", "message.accepted", "app_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", time.Now())); err != nil {
		t.Fatal(err)
	}
	entry, err := reopened.Next()
	if err != nil || entry == nil || !strings.Contains(string(entry.Data), `"valid"`) {
		t.Fatalf("Next() = %v, %v", entry, err)
	}
	if files, _ := filepath.Glob(filepath.Join(spool.directory, "*.json")); len(files) != 1 {
		t.Fatalf("invalid file retained: %v", files)
	}
}

func TestSpoolPrunesExpiredFilesOnOpen(t *testing.T) {
	spool := testSpool(t, 2<<20, time.Minute)
	if err := spool.Record(NewEvent("event", "message.accepted", "app_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", time.Now())); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(spool.directory, "*.json"))
	old := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(files[0], old, old); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSpool(SpoolConfig{Directory: spool.directory, MaxBytes: spool.maxBytes, MaxAge: spool.maxAge})
	if err != nil {
		t.Fatal(err)
	}
	if entry, err := reopened.Next(); err != nil || entry != nil || reopened.used != 0 {
		t.Fatalf("Next() = %#v, %v with %d bytes used", entry, err, reopened.used)
	}
}

func testSpool(t *testing.T, maxBytes int64, maxAge time.Duration) *Spool {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	spool, err := OpenSpool(SpoolConfig{Directory: directory, MaxBytes: maxBytes, MaxAge: maxAge})
	if err != nil {
		t.Fatal(err)
	}
	return spool
}

package observability

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrSpoolFull = errors.New("observability spool is full")

// errFlushLeadership hands the group commit to a waiting Record call.
var errFlushLeadership = errors.New("observation spool flush leadership")

const (
	// maxBatchBytes bounds one recorded batch, one spool file's payload, and
	// one ingest request body. Console accepts at most 1 MiB per request.
	maxBatchBytes = 1 << 20
	// maxBatchEvents is the ingest limit for one request.
	maxBatchEvents = 256
	// pruneInterval bounds how often every queued file is checked for expiry.
	// Expired files at the head of the queue are removed on every operation.
	pruneInterval = time.Minute
)

type SpoolConfig struct {
	Directory string
	MaxBytes  int64
	MaxAge    time.Duration
}

// Spool is a crash-safe FIFO of observation batches. Record returns only after
// its batch has been written and synced to disk. Concurrent Record calls are
// group-committed into one file so a burst costs one file and directory sync.
//
// A file holds one or more batches, one JSON-encoded Batch per line. The
// process owning a Spool must be the only writer of its directory; the queue
// index and byte usage are loaded once by OpenSpool and maintained in memory.
type Spool struct {
	directory string
	maxBytes  int64
	maxAge    time.Duration
	now       func() time.Time

	mu        sync.Mutex
	files     []spoolFile // committed files ordered by name, oldest first
	used      int64       // committed bytes plus bytes reserved by pending records
	pending   []*pendingRecord
	flushing  bool
	lastStamp int64
	lastPrune time.Time
}

type spoolFile struct {
	name    string
	size    int64
	written time.Time
}

type pendingRecord struct {
	data   []byte
	events int
	done   chan error
}

// Entry is one stored spool file and the batches it contains.
type Entry struct {
	path    string
	Data    []byte
	batches []storedBatch
}

type storedBatch struct {
	events []json.RawMessage
	size   int
}

func OpenSpool(config SpoolConfig) (*Spool, error) {
	if !filepath.IsAbs(config.Directory) || filepath.Clean(config.Directory) != config.Directory ||
		config.MaxBytes < maxBatchBytes || config.MaxAge < time.Minute {
		return nil, errors.New("invalid observability spool configuration")
	}
	if err := os.MkdirAll(config.Directory, 0o700); err != nil {
		return nil, fmt.Errorf("create observability spool: %w", err)
	}
	info, err := os.Lstat(config.Directory)
	if err != nil {
		return nil, fmt.Errorf("inspect observability spool: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("observability spool must be a private directory")
	}
	spool := &Spool{
		directory: config.Directory, maxBytes: config.MaxBytes, maxAge: config.MaxAge, now: time.Now,
	}
	if err := spool.loadLocked(); err != nil {
		return nil, err
	}
	return spool, nil
}

// Record durably appends one batch. It returns ErrSpoolFull without blocking
// when the spool has no room for the batch.
func (s *Spool) Record(events ...Event) error {
	data, err := encodeBatch(events)
	if err != nil {
		return err
	}
	if len(data) > maxBatchBytes {
		return errors.New("observation batch exceeds 1 MiB")
	}
	record := &pendingRecord{data: data, events: len(events), done: make(chan error, 1)}
	cost := int64(len(data) + 1)
	s.mu.Lock()
	if err := s.pruneLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	if s.used+cost > s.maxBytes {
		s.mu.Unlock()
		return ErrSpoolFull
	}
	s.used += cost
	s.pending = append(s.pending, record)
	if s.flushing {
		s.mu.Unlock()
		if err := <-record.done; !errors.Is(err, errFlushLeadership) {
			return err
		}
		s.mu.Lock()
	}
	// This call leads the next group commit. Its record is first in the queue
	// because leadership passes only to the oldest waiting record.
	s.flushing = true
	group := s.takePendingLocked()
	stamp := s.nextStampLocked()
	s.mu.Unlock()

	file, err := s.writeFile(stamp, group)

	s.mu.Lock()
	if err == nil {
		s.files = append(s.files, file)
	} else {
		for _, pending := range group {
			s.used -= int64(len(pending.data) + 1)
		}
	}
	if len(s.pending) > 0 {
		s.pending[0].done <- errFlushLeadership
	} else {
		s.flushing = false
	}
	s.mu.Unlock()
	for _, pending := range group[1:] {
		pending.done <- err
	}
	return err
}

// Next returns the oldest stored file, or nil when the spool is empty.
func (s *Spool) Next() (*Entry, error) {
	entries, err := s.collect(1, maxBatchEvents, maxBatchBytes)
	if err != nil || len(entries) == 0 {
		return nil, err
	}
	return entries[0], nil
}

// Collect returns the oldest stored files whose batches fit together in one
// ingest request. It always returns at least one file when any are stored.
func (s *Spool) Collect() ([]*Entry, error) {
	return s.collect(maxBatchEvents, maxBatchEvents, maxBatchBytes)
}

func (s *Spool) collect(maxFiles, maxEvents, maxBytes int) ([]*Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.pruneLocked(); err != nil {
		return nil, err
	}
	var entries []*Entry
	events, size := 0, 0
	for index := 0; index < len(s.files) && len(entries) < maxFiles; {
		file := s.files[index]
		entry, err := s.readLocked(file)
		if errors.Is(err, errInvalidStoredBatch) {
			// A malformed file can never be accepted. Discard it rather than
			// blocking every later observation behind it.
			slog.Warn("Dropping invalid stored observation batch", "file", file.name, "error", err)
			if err := s.removeLocked(file.name); err != nil {
				return nil, err
			}
			if err := syncDirectory(s.directory); err != nil {
				return nil, err
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		entryEvents, entrySize := entry.counts()
		if len(entries) > 0 && (events+entryEvents > maxEvents || size+entrySize > maxBytes) {
			break
		}
		entries = append(entries, entry)
		events, size = events+entryEvents, size+entrySize
		index++
	}
	return entries, nil
}

// Acknowledge removes delivered or deliberately discarded files.
func (s *Spool) Acknowledge(entries ...*Entry) error {
	for _, entry := range entries {
		if entry == nil || filepath.Dir(entry.path) != s.directory || !strings.HasSuffix(entry.path, ".json") {
			return errors.New("invalid observation spool entry")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range entries {
		if err := s.removeLocked(filepath.Base(entry.path)); err != nil {
			return fmt.Errorf("remove acknowledged observation batch: %w", err)
		}
	}
	return syncDirectory(s.directory)
}

func (s *Spool) takePendingLocked() []*pendingRecord {
	count, events, size := 0, 0, 0
	for _, record := range s.pending {
		if count > 0 && (events+record.events > maxBatchEvents || size+len(record.data) > maxBatchBytes) {
			break
		}
		count, events, size = count+1, events+record.events, size+len(record.data)
	}
	group := s.pending[:count:count]
	s.pending = append([]*pendingRecord(nil), s.pending[count:]...)
	return group
}

// nextStampLocked keeps file names strictly increasing so name order remains
// the delivery order across restarts, even if the wall clock steps backwards.
func (s *Spool) nextStampLocked() int64 {
	stamp := s.now().UTC().UnixNano()
	if stamp <= s.lastStamp {
		stamp = s.lastStamp + 1
	}
	s.lastStamp = stamp
	return stamp
}

func (s *Spool) writeFile(stamp int64, group []*pendingRecord) (spoolFile, error) {
	var payload bytes.Buffer
	for _, record := range group {
		payload.Write(record.data)
		payload.WriteByte('\n')
	}
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return spoolFile{}, fmt.Errorf("name observation batch: %w", err)
	}
	name := fmt.Sprintf("%020d-%x.json", stamp, random)
	temporary := filepath.Join(s.directory, "."+name+".tmp")
	final := filepath.Join(s.directory, name)
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return spoolFile{}, fmt.Errorf("create observation batch: %w", err)
	}
	removeTemporary := true
	defer func() {
		_ = file.Close()
		if removeTemporary {
			_ = os.Remove(temporary)
		}
	}()
	if _, err := file.Write(payload.Bytes()); err != nil {
		return spoolFile{}, fmt.Errorf("write observation batch: %w", err)
	}
	if err := file.Sync(); err != nil {
		return spoolFile{}, fmt.Errorf("sync observation batch: %w", err)
	}
	if err := file.Close(); err != nil {
		return spoolFile{}, fmt.Errorf("close observation batch: %w", err)
	}
	if err := os.Rename(temporary, final); err != nil {
		return spoolFile{}, fmt.Errorf("commit observation batch: %w", err)
	}
	removeTemporary = false
	if err := syncDirectory(s.directory); err != nil {
		return spoolFile{}, err
	}
	return spoolFile{name: name, size: int64(payload.Len()), written: s.now().UTC()}, nil
}

// pruneLocked removes expired files. Files are written in name order, so the
// head check is usually sufficient; a periodic full pass also catches files
// whose times are out of order, such as those inherited from a previous run.
func (s *Spool) pruneLocked() error {
	now := s.now().UTC()
	cutoff := now.Add(-s.maxAge)
	expired := func(file spoolFile) bool { return file.written.Before(cutoff) }
	var names []string
	if now.Sub(s.lastPrune) >= pruneInterval || now.Before(s.lastPrune) {
		s.lastPrune = now
		for _, file := range s.files {
			if expired(file) {
				names = append(names, file.name)
			}
		}
	} else {
		for _, file := range s.files {
			if !expired(file) {
				break
			}
			names = append(names, file.name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	for _, name := range names {
		if err := s.removeLocked(name); err != nil {
			return fmt.Errorf("prune expired observation batch: %w", err)
		}
	}
	return syncDirectory(s.directory)
}

// removeLocked deletes one file and its index entry. Missing files are not an
// error because expiry can race with delivery.
func (s *Spool) removeLocked(name string) error {
	if err := os.Remove(filepath.Join(s.directory, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	index := sort.Search(len(s.files), func(i int) bool { return s.files[i].name >= name })
	if index < len(s.files) && s.files[index].name == name {
		s.used -= s.files[index].size
		s.files = append(s.files[:index], s.files[index+1:]...)
	}
	return nil
}

func (s *Spool) loadLocked() error {
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return fmt.Errorf("list observation spool: %w", err)
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect observation batch: %w", err)
		}
		s.files = append(s.files, spoolFile{name: entry.Name(), size: info.Size(), written: info.ModTime().UTC()})
		s.used += info.Size()
		stamp, _, _ := strings.Cut(entry.Name(), "-")
		if parsed, err := strconv.ParseInt(stamp, 10, 64); err == nil && parsed > s.lastStamp {
			s.lastStamp = parsed
		}
	}
	// os.ReadDir sorts by file name. The zero lastPrune forces a full pass.
	return s.pruneLocked()
}

var errInvalidStoredBatch = errors.New("stored observation batch is invalid")

func (s *Spool) readLocked(file spoolFile) (*Entry, error) {
	path := filepath.Join(s.directory, file.name)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read observation batch: %w", err)
	}
	if len(data) < 1 || len(data) > maxBatchBytes+maxBatchEvents {
		return nil, fmt.Errorf("%w: invalid size", errInvalidStoredBatch)
	}
	entry := &Entry{path: path, Data: data}
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var batch struct {
			Events []json.RawMessage `json:"events"`
		}
		if err := json.Unmarshal(line, &batch); err != nil || len(batch.Events) < 1 || len(batch.Events) > maxBatchEvents {
			return nil, fmt.Errorf("%w: malformed batch", errInvalidStoredBatch)
		}
		entry.batches = append(entry.batches, storedBatch{events: batch.Events, size: len(line)})
	}
	events, size := entry.counts()
	if events < 1 || events > maxBatchEvents || size > maxBatchBytes {
		return nil, fmt.Errorf("%w: exceeds ingest limits", errInvalidStoredBatch)
	}
	return entry, nil
}

func (e *Entry) counts() (events, size int) {
	for _, batch := range e.batches {
		events += len(batch.events)
		size += batch.size
	}
	return events, size
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

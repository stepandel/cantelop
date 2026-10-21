package sqlite

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/persistence/configcipher"
	"github.com/stepandel/cantelop/fire-fuse/sessionevents"
)

const maxSessionEventFingerprints = 100000

// sessionEventPollInterval bounds how long a reader waits for writes made by
// another SessionEvents instance; writes through this instance wake it at once.
const sessionEventPollInterval = time.Second

// SessionEvents commits output and application views before the Sandbox receives
// an acknowledgment. Stream identity and projection boundaries survive restart.
type SessionEvents struct {
	db     *sql.DB
	cipher *configcipher.Cipher

	// mu serializes writers so the cached usage totals stay consistent.
	mu sync.Mutex
	// Cached global usage. Other writers (Workspace purge, other instances)
	// only make it stale; an apparent capacity rejection reloads it exactly.
	usageLoaded bool
	entries     int64
	storedBytes int64
	changed     chan struct{}
}

func NewSessionEvents(db *sql.DB, cipher *configcipher.Cipher) *SessionEvents {
	return &SessionEvents{db: db, cipher: cipher, changed: make(chan struct{})}
}

func (s *SessionEvents) notify() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *SessionEvents) waitChannel() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changed
}

func loadSessionEventUsage(tx *sql.Tx) (entries, storedBytes int64, err error) {
	err = tx.QueryRow(`SELECT count(*),coalesce(sum(length(ciphertext)),0)+(SELECT coalesce(sum(length(view_ciphertext)),0) FROM session_streams) FROM session_stream_entries`).Scan(&entries, &storedBytes)
	return entries, storedBytes, err
}

// ensure reserves a stream for key and reports the usage it reclaimed.
func (s *SessionEvents) ensure(tx *sql.Tx, key sessionevents.Key) (reclaimedEntries, reclaimedBytes int64, err error) {
	if key.WorkspaceID == "" || key.SessionID == "" {
		return 0, 0, sessionevents.ErrInvalidEvent
	}
	var count int
	if err := tx.QueryRow(`SELECT count(*) FROM session_streams WHERE workspace_id=? AND session_id=?`, key.WorkspaceID, key.SessionID).Scan(&count); err != nil {
		return 0, 0, err
	}
	if count > 0 {
		return 0, 0, nil
	}
	if err := tx.QueryRow(`SELECT count(*) FROM session_streams`).Scan(&count); err != nil {
		return 0, 0, err
	}
	if count >= sessionevents.DefaultMaxStreams {
		// Reclaim the oldest stream in order of least loss: read-only
		// reservations, then closed streams, then the least recently updated
		// view. Live streams without a view carry replay that cannot be rebuilt.
		var reclaim sql.NullString
		if err := tx.QueryRow(`SELECT stream_id FROM session_streams WHERE sequence=0 OR closed=1 OR view_revision IS NOT NULL
ORDER BY view_revision IS NOT NULL, sequence<>0, closed=0, coalesce(view_updated_at,''), rowid LIMIT 1`).Scan(&reclaim); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, 0, err
		}
		if !reclaim.Valid {
			return 0, 0, sessionevents.ErrCapacity
		}
		if err := tx.QueryRow(`SELECT count(*),coalesce(sum(length(ciphertext)),0)+(SELECT coalesce(length(view_ciphertext),0) FROM session_streams WHERE stream_id=?) FROM session_stream_entries WHERE stream_id=?`, reclaim.String, reclaim.String).Scan(&reclaimedEntries, &reclaimedBytes); err != nil {
			return 0, 0, err
		}
		if _, err := tx.Exec(`DELETE FROM session_stream_entries WHERE stream_id=?`, reclaim.String); err != nil {
			return 0, 0, err
		}
		if _, err := tx.Exec(`DELETE FROM session_streams WHERE stream_id=?`, reclaim.String); err != nil {
			return 0, 0, err
		}
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return 0, 0, err
	}
	_, err = tx.Exec(`INSERT INTO session_streams(workspace_id,session_id,stream_id)VALUES(?,?,?)`, key.WorkspaceID, key.SessionID, hex.EncodeToString(id[:]))
	return reclaimedEntries, reclaimedBytes, err
}

// reserve creates the stream for key when missing. Existing streams need no write.
func (s *SessionEvents) reserve(key sessionevents.Key) error {
	if key.WorkspaceID == "" || key.SessionID == "" {
		return sessionevents.ErrInvalidEvent
	}
	var exists int
	err := s.db.QueryRow(`SELECT 1 FROM session_streams WHERE workspace_id=? AND session_id=?`, key.WorkspaceID, key.SessionID).Scan(&exists)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	reclaimedEntries, reclaimedBytes, err := s.ensure(tx, key)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if s.usageLoaded {
		s.entries -= reclaimedEntries
		s.storedBytes -= reclaimedBytes
	}
	return nil
}

func (s *SessionEvents) Append(input sessionevents.Append) (sessionevents.Event, error) {
	data := input.Data
	projection := input.View != nil
	if projection {
		data = *input.View
	}
	if len(data) == 0 || len(data) > sessionevents.DefaultMaxEventBytes || !json.Valid(data) || input.MessageID == "" || (input.SourceSandboxID == "") != (input.SourceCursor == 0) {
		return sessionevents.Event{}, sessionevents.ErrInvalidEvent
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return sessionevents.Event{}, err
	}
	defer tx.Rollback()
	entries, storedBytes := s.entries, s.storedBytes
	exact := !s.usageLoaded
	if exact {
		if entries, storedBytes, err = loadSessionEventUsage(tx); err != nil {
			return sessionevents.Event{}, err
		}
	}
	reclaimedEntries, reclaimedBytes, err := s.ensure(tx, input.Key)
	if err != nil {
		return sessionevents.Event{}, err
	}
	entries -= reclaimedEntries
	storedBytes -= reclaimedBytes
	var streamID string
	var sequence uint64
	var closed bool
	if err = tx.QueryRow(`SELECT stream_id,sequence,closed FROM session_streams WHERE workspace_id=? AND session_id=?`, input.WorkspaceID, input.SessionID).Scan(&streamID, &sequence, &closed); err != nil {
		return sessionevents.Event{}, err
	}
	if closed {
		return sessionevents.Event{}, sessionevents.ErrInvalidEvent
	}
	hash := sha256.Sum256(data)
	if input.SourceSandboxID != "" {
		var priorSequence uint64
		var priorMessage, priorAt string
		var priorProjection bool
		var fingerprint []byte
		err = tx.QueryRow(`SELECT sequence,message_id,created_at,projection,fingerprint FROM session_stream_entries WHERE stream_id=? AND source_sandbox_id=? AND source_cursor=?`, streamID, input.SourceSandboxID, input.SourceCursor).Scan(&priorSequence, &priorMessage, &priorAt, &priorProjection, &fingerprint)
		if err == nil {
			if priorMessage != input.MessageID || priorProjection != projection || !bytes.Equal(fingerprint, hash[:]) {
				return sessionevents.Event{}, sessionevents.ErrInvalidEvent
			}
			at, err := parseTime(priorAt)
			return sessionevents.Event{StreamID: streamID, Sequence: priorSequence, SessionID: input.SessionID, MessageID: input.MessageID, CreatedAt: at, Data: input.Data, SourceSandboxID: input.SourceSandboxID, SourceCursor: input.SourceCursor}, err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return sessionevents.Event{}, err
		}
	}
	// Projections are not replayed, so only application output occupies the
	// replay window. Make room for the next retained entry inside the same
	// transaction; a rejected commit rolls this pruning back.
	if !projection {
		var threshold sql.NullInt64
		if err := tx.QueryRow(`SELECT sequence FROM session_stream_entries WHERE stream_id=? AND projection=0 AND ciphertext IS NOT NULL ORDER BY sequence DESC LIMIT 1 OFFSET ?`, streamID, sessionevents.DefaultMaxEvents-1).Scan(&threshold); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return sessionevents.Event{}, err
		}
		if threshold.Valid {
			var freed int64
			if err := tx.QueryRow(`SELECT coalesce(sum(length(ciphertext)),0) FROM session_stream_entries WHERE stream_id=? AND projection=0 AND ciphertext IS NOT NULL AND sequence<=?`, streamID, threshold.Int64).Scan(&freed); err != nil {
				return sessionevents.Event{}, err
			}
			if _, err := tx.Exec(`UPDATE session_stream_entries SET nonce=NULL,ciphertext=NULL WHERE stream_id=? AND projection=0 AND ciphertext IS NOT NULL AND sequence<=?`, streamID, threshold.Int64); err != nil {
				return sessionevents.Event{}, err
			}
			storedBytes -= freed
		}
	}
	var priorViewBytes int64
	requiredBytes := int64(len(data) + 28)
	if projection {
		if err := tx.QueryRow(`SELECT coalesce(length(view_ciphertext),0) FROM session_streams WHERE stream_id=?`, streamID).Scan(&priorViewBytes); err != nil {
			return sessionevents.Event{}, err
		}
		requiredBytes -= priorViewBytes
	}
	overCapacity := func() bool {
		return entries >= maxSessionEventFingerprints || storedBytes+requiredBytes > sessionevents.DefaultMaxTotalBytes
	}
	if overCapacity() && !exact {
		// The cache may be stale after external deletes; decide on exact usage.
		if entries, storedBytes, err = loadSessionEventUsage(tx); err != nil {
			return sessionevents.Event{}, err
		}
		exact = true
	}
	if entries >= maxSessionEventFingerprints {
		// Fingerprints whose replay ciphertext has expired only guard stale
		// acknowledgment retries; drop the oldest in a batch instead of
		// refusing all output.
		result, err := tx.Exec(`DELETE FROM session_stream_entries WHERE rowid IN (SELECT rowid FROM session_stream_entries WHERE ciphertext IS NULL ORDER BY created_at LIMIT ?)`, entries-maxSessionEventFingerprints+1+maxSessionEventFingerprints/100)
		if err != nil {
			return sessionevents.Event{}, err
		}
		deleted, err := result.RowsAffected()
		if err != nil {
			return sessionevents.Event{}, err
		}
		entries -= deleted
	}
	if overCapacity() {
		return sessionevents.Event{}, sessionevents.ErrCapacity
	}
	sequence++
	if sequence > 9007199254740991 {
		return sessionevents.Event{}, sessionevents.ErrCapacity
	}
	sourceCursor := input.SourceCursor
	if input.SourceSandboxID == "" {
		// Unsourced appends have no retry identity; the sequence keeps the
		// source key unique without colliding with Sandbox cursors.
		sourceCursor = sequence
	}
	var nonce, ciphertext []byte
	if !projection {
		nonce, ciphertext, err = s.cipher.Seal("session_event", streamID, strconv.FormatUint(sequence, 10), string(data))
		if err != nil {
			return sessionevents.Event{}, err
		}
	}
	at := time.Now().UTC()
	_, err = tx.Exec(`INSERT INTO session_stream_entries VALUES(?,?,?,?,?,?,?,?,?,?)`, streamID, sequence, input.SourceSandboxID, sourceCursor, input.MessageID, formatTime(at), projection, nonce, ciphertext, hash[:])
	if err != nil {
		return sessionevents.Event{}, err
	}
	addedBytes := int64(len(ciphertext))
	if projection {
		var viewNonce, viewCiphertext []byte
		viewNonce, viewCiphertext, err = s.cipher.Seal("session_view", streamID, "state", string(data))
		if err != nil {
			return sessionevents.Event{}, err
		}
		addedBytes = int64(len(viewCiphertext)) - priorViewBytes
		_, err = tx.Exec(`UPDATE session_streams SET sequence=?,view_revision=?,view_updated_at=?,view_nonce=?,view_ciphertext=? WHERE stream_id=?`, sequence, sequence, formatTime(at), viewNonce, viewCiphertext, streamID)
	} else {
		_, err = tx.Exec(`UPDATE session_streams SET sequence=? WHERE stream_id=?`, sequence, streamID)
	}
	if err != nil {
		return sessionevents.Event{}, err
	}
	if err = tx.Commit(); err != nil {
		return sessionevents.Event{}, err
	}
	s.usageLoaded, s.entries, s.storedBytes = true, entries+1, storedBytes+addedBytes
	s.notify()
	return sessionevents.Event{StreamID: streamID, Sequence: sequence, SessionID: input.SessionID, MessageID: input.MessageID, CreatedAt: at, Data: input.Data, SourceSandboxID: input.SourceSandboxID, SourceCursor: input.SourceCursor}, nil
}
func (s *SessionEvents) ReadAvailable(key sessionevents.Key, after uint64) (sessionevents.Batch, error) {
	if err := s.reserve(key); err != nil {
		return sessionevents.Batch{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return sessionevents.Batch{}, err
	}
	defer tx.Rollback()
	batch := sessionevents.Batch{Events: []sessionevents.Event{}}
	if err = tx.QueryRow(`SELECT stream_id,sequence,closed FROM session_streams WHERE workspace_id=? AND session_id=?`, key.WorkspaceID, key.SessionID).Scan(&batch.StreamID, &batch.LatestSequence, &batch.Closed); err != nil {
		return batch, err
	}
	// The window starts after the newest output whose replay was pruned, or
	// after any entries whose fingerprints were dropped entirely.
	var pruned uint64
	var oldest sql.NullInt64
	if err = tx.QueryRow(`SELECT coalesce((SELECT max(sequence) FROM session_stream_entries WHERE stream_id=? AND projection=0 AND ciphertext IS NULL),0),(SELECT min(sequence) FROM session_stream_entries WHERE stream_id=?)`, batch.StreamID, batch.StreamID).Scan(&pruned, &oldest); err != nil {
		return batch, err
	}
	batch.EarliestSequence = max(pruned+1, batch.LatestSequence+1)
	if oldest.Valid {
		batch.EarliestSequence = max(pruned+1, uint64(oldest.Int64))
	}
	if after > batch.LatestSequence || after+1 < batch.EarliestSequence {
		return batch, sessionevents.ErrCursorExpired
	}
	rows, err := tx.Query(`SELECT sequence,message_id,created_at,nonce,ciphertext,source_sandbox_id,source_cursor FROM session_stream_entries WHERE stream_id=? AND sequence>? AND projection=0 ORDER BY sequence LIMIT 16`, batch.StreamID, after)
	if err != nil {
		return batch, err
	}
	for rows.Next() {
		var event sessionevents.Event
		var at string
		var nonce, ciphertext []byte
		if err = rows.Scan(&event.Sequence, &event.MessageID, &at, &nonce, &ciphertext, &event.SourceSandboxID, &event.SourceCursor); err != nil {
			rows.Close()
			return batch, err
		}
		plaintext, err := s.cipher.Open("session_event", batch.StreamID, strconv.FormatUint(event.Sequence, 10), nonce, ciphertext)
		if err != nil {
			rows.Close()
			return batch, err
		}
		if event.SourceSandboxID == "" {
			event.SourceCursor = 0
		}
		event.Data = json.RawMessage(plaintext)
		event.StreamID = batch.StreamID
		event.SessionID = key.SessionID
		event.CreatedAt, err = parseTime(at)
		if err != nil {
			rows.Close()
			return batch, err
		}
		batch.Events = append(batch.Events, event)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return batch, err
	}
	return batch, tx.Commit()
}
func (s *SessionEvents) Read(ctx context.Context, key sessionevents.Key, after uint64) (sessionevents.Batch, error) {
	for {
		changed := s.waitChannel()
		batch, err := s.ReadAvailable(key, after)
		if err != nil || len(batch.Events) > 0 || batch.Closed {
			return batch, err
		}
		timer := time.NewTimer(sessionEventPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return batch, ctx.Err()
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
	}
}
func (s *SessionEvents) Close(key sessionevents.Key) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE session_streams SET closed=1 WHERE workspace_id=? AND session_id=?`, key.WorkspaceID, key.SessionID)
	if err == nil {
		s.notify()
	}
	return err
}
func (s *SessionEvents) View(key sessionevents.Key) (sessionevents.View, error) {
	var value sessionevents.View
	var revision uint64
	var at string
	var nonce, ciphertext []byte
	err := s.db.QueryRow(`SELECT stream_id,view_revision,view_updated_at,view_nonce,view_ciphertext FROM session_streams WHERE workspace_id=? AND session_id=? AND view_revision IS NOT NULL`, key.WorkspaceID, key.SessionID).Scan(&value.Cursor.StreamID, &revision, &at, &nonce, &ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return value, sessionevents.ErrViewNotFound
	}
	if err != nil {
		return value, err
	}
	state, err := s.cipher.Open("session_view", value.Cursor.StreamID, "state", nonce, ciphertext)
	if err != nil {
		return value, err
	}
	value.State = json.RawMessage(state)
	value.Revision = strconv.FormatUint(revision, 10)
	value.Cursor.Sequence = revision
	value.UpdatedAt, err = parseTime(at)
	return value, err
}

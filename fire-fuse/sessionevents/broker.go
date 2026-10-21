// Package sessionevents provides bounded, process-local replay for Session output.
package sessionevents

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

const (
	DefaultMaxEvents                   = 256
	DefaultMaxBytes                    = 1 << 20
	DefaultMaxEventBytes               = 64 << 10
	DefaultMaxStreams                  = 1024
	DefaultMaxTotalBytes               = 32 << 20
	DefaultMaxEmptyStreamsPerWorkspace = 64
)

var (
	ErrInvalidEvent  = errors.New("invalid Session event")
	ErrCursorExpired = errors.New("Session event cursor expired")
	ErrCapacity      = errors.New("Session event subscription capacity reached")
)

type Key struct {
	WorkspaceID string
	SessionID   string
}

type Event struct {
	StreamID        string          `json:"stream_id"`
	SourceSandboxID string          `json:"source_sandbox_id,omitempty"`
	SourceCursor    uint64          `json:"source_cursor,omitempty"`
	Sequence        uint64          `json:"sequence"`
	SessionID       string          `json:"session_id"`
	MessageID       string          `json:"message_id"`
	CreatedAt       time.Time       `json:"created_at"`
	Data            json.RawMessage `json:"data"`
}

type View struct {
	Revision  string          `json:"revision"`
	UpdatedAt time.Time       `json:"updated_at"`
	State     json.RawMessage `json:"state"`
	Cursor    struct {
		StreamID string `json:"streamId"`
		Sequence uint64 `json:"sequence"`
	} `json:"cursor"`
}

var ErrViewNotFound = errors.New("Session view has not been committed")

type Store interface {
	Append(Append) (Event, error)
	Read(context.Context, Key, uint64) (Batch, error)
	ReadAvailable(Key, uint64) (Batch, error)
	Close(Key) error
}

type Append struct {
	View            *json.RawMessage
	SourceSandboxID string
	SourceCursor    uint64
	Key
	MessageID string
	Data      json.RawMessage
}

type Batch struct {
	StreamID         string  `json:"stream_id"`
	Events           []Event `json:"events"`
	EarliestSequence uint64  `json:"earliest_sequence,omitempty"`
	LatestSequence   uint64  `json:"latest_sequence,omitempty"`
	Closed           bool    `json:"closed,omitempty"`
}

type Broker struct {
	mu            sync.Mutex
	streams       map[Key]*stream
	maxEvents     int
	maxBytes      int
	maxEventBytes int
	maxStreams    int
	maxTotalBytes int
	totalBytes    int
	tick          uint64
	now           func() time.Time
}

type stream struct {
	id      string
	events  []Event
	bytes   int
	next    uint64
	closed  bool
	changed chan struct{}
	touched uint64
}

func NewBroker() *Broker {
	return NewBrokerWithLimits(DefaultMaxEvents, DefaultMaxBytes, DefaultMaxEventBytes)
}

func NewBrokerWithLimits(maxEvents, maxBytes, maxEventBytes int) *Broker {
	return NewBrokerWithGlobalLimits(maxEvents, maxBytes, maxEventBytes, DefaultMaxStreams, DefaultMaxTotalBytes)
}

func NewBrokerWithGlobalLimits(maxEvents, maxBytes, maxEventBytes, maxStreams, maxTotalBytes int) *Broker {
	return &Broker{
		streams: make(map[Key]*stream), maxEvents: maxEvents, maxBytes: maxBytes,
		maxEventBytes: maxEventBytes, maxStreams: maxStreams, maxTotalBytes: maxTotalBytes, now: time.Now,
	}
}

// Append records Data only. The in-memory Broker keeps no durable view, so an
// attached View is dropped rather than failing the output collector.
func (b *Broker) Append(value Append) (Event, error) {
	if value.WorkspaceID == "" || value.SessionID == "" || value.MessageID == "" ||
		len(value.Data) == 0 || len(value.Data) > b.maxEventBytes || len(value.Data) > b.maxBytes || b.maxEvents < 1 || !json.Valid(value.Data) {
		return Event{}, ErrInvalidEvent
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	current := b.streams[value.Key]
	if current != nil && current.closed {
		return Event{}, ErrInvalidEvent
	}
	var prior []Event
	if current != nil {
		prior = current.events
	}
	for _, event := range prior {
		if value.SourceSandboxID != "" && event.SourceSandboxID == value.SourceSandboxID && event.SourceCursor == value.SourceCursor {
			if event.MessageID != value.MessageID || !bytes.Equal(event.Data, value.Data) {
				return Event{}, ErrInvalidEvent
			}
			return cloneEvent(event), nil
		}
	}
	currentBytes := 0
	if current != nil {
		currentBytes = current.bytes
	}
	projectedBytes := currentBytes + len(value.Data)
	projectedCount := len(prior) + 1
	drop := 0
	for drop < len(prior) && (projectedCount > b.maxEvents || projectedBytes > b.maxBytes) {
		projectedBytes -= len(prior[drop].Data)
		projectedCount--
		drop++
	}
	streamCount := len(b.streams)
	if current == nil {
		streamCount++
	}
	if err := b.admit(value.Key, projectedBytes, streamCount, b.totalBytes-currentBytes+projectedBytes, true); err != nil {
		return Event{}, err
	}
	if current == nil {
		current = b.stream(value.Key)
	} else {
		b.tick++
		current.touched = b.tick
	}
	current.next++
	event := Event{
		StreamID: current.id, SourceSandboxID: value.SourceSandboxID, SourceCursor: value.SourceCursor, Sequence: current.next, SessionID: value.SessionID, MessageID: value.MessageID,
		CreatedAt: b.now().UTC(), Data: append(json.RawMessage(nil), value.Data...),
	}
	current.events = append(current.events[drop:], event)
	b.totalBytes += projectedBytes - current.bytes
	current.bytes = projectedBytes
	b.signal(current)
	return cloneEvent(event), nil
}

func (b *Broker) Read(ctx context.Context, key Key, after uint64) (Batch, error) {
	if key.WorkspaceID == "" || key.SessionID == "" {
		return Batch{}, ErrInvalidEvent
	}
	var subscribed *stream
	for {
		b.mu.Lock()
		// An evicted waiter must terminate, not recreate its reservation and
		// evict another waiter in an endless wake/recreate cycle.
		if subscribed != nil && b.streams[key] != subscribed {
			b.mu.Unlock()
			return Batch{}, ErrCursorExpired
		}
		current, err := b.readStream(key, after)
		if err != nil {
			b.mu.Unlock()
			return Batch{}, err
		}
		batch, ready, err := readLocked(current, after)
		subscribed = current
		changed := current.changed
		b.mu.Unlock()
		if err != nil || ready {
			return batch, err
		}
		select {
		case <-ctx.Done():
			return Batch{}, ctx.Err()
		case <-changed:
		}
	}
}

func (b *Broker) ReadAvailable(key Key, after uint64) (Batch, error) {
	if key.WorkspaceID == "" || key.SessionID == "" {
		return Batch{}, ErrInvalidEvent
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	current, err := b.readStream(key, after)
	if err != nil {
		return Batch{}, err
	}
	batch, _, err := readLocked(current, after)
	return batch, err
}

// Subscribe-before-Session is supported, but reads must never evict replay
// data. Recycle only this Workspace's empty reservations under cache pressure.
func (b *Broker) readStream(key Key, after uint64) (*stream, error) {
	if current := b.streams[key]; current != nil {
		return current, nil
	}
	if after != 0 {
		return nil, ErrCursorExpired
	}
	var oldestKey Key
	var oldest *stream
	empty := 0
	for candidateKey, candidate := range b.streams {
		if candidateKey.WorkspaceID == key.WorkspaceID && candidate.next == 0 && !candidate.closed {
			empty++
			if oldest == nil || candidate.touched < oldest.touched {
				oldestKey, oldest = candidateKey, candidate
			}
		}
	}
	if empty >= DefaultMaxEmptyStreamsPerWorkspace || len(b.streams) >= b.maxStreams {
		if oldest != nil {
			b.signal(oldest)
			delete(b.streams, oldestKey)
		} else if err := b.admit(key, 0, len(b.streams)+1, b.totalBytes, false); err != nil {
			return nil, err
		}
	}
	return b.stream(key), nil
}

func (b *Broker) Close(key Key) error {
	if key.WorkspaceID == "" || key.SessionID == "" {
		return ErrInvalidEvent
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	current, err := b.readStream(key, 0)
	if err != nil {
		return err
	}
	if !current.closed {
		current.closed = true
		b.signal(current)
	}
	return nil
}

func (b *Broker) stream(key Key) *stream {
	current := b.streams[key]
	if current == nil {
		var identity [16]byte
		if _, err := rand.Read(identity[:]); err != nil {
			panic(err)
		}
		current = &stream{id: hex.EncodeToString(identity[:]), changed: make(chan struct{})}
		b.streams[key] = current
	}
	b.tick++
	current.touched = b.tick
	return current
}

// admit reclaims streams until the projected totals fit. The caller's own
// Workspace yields first; another Workspace yields only while it is the
// largest holder and holds more than the caller would after admission, so a
// full cache cannot lock out small tenants and small tenants' replay is never
// evicted by a larger one. Without replay, only streams that never carried
// events may be reclaimed.
func (b *Broker) admit(keep Key, keepBytes, streamCount, totalBytes int, replay bool) error {
	selected := make(map[Key]bool)
	for streamCount > b.maxStreams || totalBytes > b.maxTotalBytes {
		// Empty reservations relieve stream-count pressure but free no bytes.
		preferEmpty := streamCount > b.maxStreams
		oldestKey, oldest := b.reclaimCandidate(keep, selected, preferEmpty, replay, func(workspaceID string) bool {
			return workspaceID == keep.WorkspaceID
		})
		if oldest == nil {
			usage := b.workspaceUsage(keep, keepBytes, selected, preferEmpty)
			largest := 0
			for workspaceID, used := range usage {
				if workspaceID != keep.WorkspaceID && used > largest {
					largest = used
				}
			}
			if largest > usage[keep.WorkspaceID] {
				oldestKey, oldest = b.reclaimCandidate(keep, selected, preferEmpty, replay, func(workspaceID string) bool {
					return workspaceID != keep.WorkspaceID && usage[workspaceID] == largest
				})
			}
		}
		if oldest == nil {
			return ErrCapacity
		}
		selected[oldestKey] = true
		streamCount--
		totalBytes -= oldest.bytes
	}
	for key := range selected {
		oldest := b.streams[key]
		b.totalBytes -= oldest.bytes
		b.signal(oldest)
		delete(b.streams, key)
	}
	return nil
}

func (b *Broker) reclaimCandidate(keep Key, selected map[Key]bool, preferEmpty, replay bool, eligible func(string) bool) (Key, *stream) {
	var oldestKey Key
	var oldest *stream
	for key, candidate := range b.streams {
		if key == keep || selected[key] || !eligible(key.WorkspaceID) || (!replay && candidate.next != 0) || (!preferEmpty && candidate.bytes == 0) {
			continue
		}
		if oldest == nil || (preferEmpty && candidate.next == 0 && oldest.next != 0) ||
			((!preferEmpty || (candidate.next == 0) == (oldest.next == 0)) && candidate.touched < oldest.touched) {
			oldestKey, oldest = key, candidate
		}
	}
	return oldestKey, oldest
}

// workspaceUsage reports each Workspace's projected stream count, or bytes
// under byte pressure, after admission and the already selected evictions.
func (b *Broker) workspaceUsage(keep Key, keepBytes int, selected map[Key]bool, byCount bool) map[string]int {
	usage := make(map[string]int)
	measure := func(bytes int) int {
		if byCount {
			return 1
		}
		return bytes
	}
	for key, candidate := range b.streams {
		if selected[key] || key == keep {
			continue
		}
		usage[key.WorkspaceID] += measure(candidate.bytes)
	}
	usage[keep.WorkspaceID] += measure(keepBytes)
	return usage
}

func (b *Broker) signal(current *stream) {
	close(current.changed)
	current.changed = make(chan struct{})
}

func readLocked(current *stream, after uint64) (Batch, bool, error) {
	if after > current.next {
		return Batch{StreamID: current.id, LatestSequence: current.next}, true, ErrCursorExpired
	}
	earliest := uint64(0)
	if len(current.events) > 0 {
		earliest = current.events[0].Sequence
		if after < earliest-1 {
			return Batch{StreamID: current.id, EarliestSequence: earliest, LatestSequence: current.next}, true, ErrCursorExpired
		}
	}
	batch := Batch{StreamID: current.id, EarliestSequence: earliest, LatestSequence: current.next, Closed: current.closed}
	for _, event := range current.events {
		if event.Sequence > after {
			batch.Events = append(batch.Events, cloneEvent(event))
		}
	}
	return batch, len(batch.Events) > 0 || current.closed, nil
}

func cloneEvent(event Event) Event {
	event.Data = append(json.RawMessage(nil), event.Data...)
	return event
}

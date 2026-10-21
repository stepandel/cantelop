package sessionevents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestBrokerOrdersAndReplaysSessionEvents(t *testing.T) {
	broker := NewBrokerWithLimits(2, 1024, 128)
	key := Key{WorkspaceID: "wsp_a", SessionID: "orders"}
	for _, value := range []string{`{"token":"a"}`, `{"token":"b"}`, `{"token":"c"}`} {
		if _, err := broker.Append(Append{Key: key, MessageID: "msg_0123456789abcdef0123456789abcdef", Data: []byte(value)}); err != nil {
			t.Fatal(err)
		}
	}
	batch, err := broker.Read(context.Background(), key, 1)
	if err != nil || len(batch.Events) != 2 || batch.Events[0].Sequence != 2 || batch.Events[1].Sequence != 3 {
		t.Fatalf("unexpected replay: %#v, %v", batch, err)
	}
	if _, err := broker.Read(context.Background(), key, 0); !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("expected expired cursor, got %v", err)
	}
}

func TestBrokerReadWaitsAndCloseEndsStream(t *testing.T) {
	broker := NewBroker()
	key := Key{WorkspaceID: "wsp_a", SessionID: "orders"}
	result := make(chan Batch, 1)
	go func() {
		batch, _ := broker.Read(context.Background(), key, 0)
		result <- batch
	}()
	select {
	case <-result:
		t.Fatal("empty active stream returned before an event")
	case <-time.After(10 * time.Millisecond):
	}
	if err := broker.Close(key); err != nil {
		t.Fatal(err)
	}
	select {
	case batch := <-result:
		if !batch.Closed {
			t.Fatalf("expected closed batch: %#v", batch)
		}
	case <-time.After(time.Second):
		t.Fatal("closed stream did not wake reader")
	}
}

func TestBrokerGloballyBoundsCachedStreams(t *testing.T) {
	broker := NewBrokerWithGlobalLimits(2, 1024, 128, 1, 1024)
	messageID := "msg_0123456789abcdef0123456789abcdef"
	first := Key{WorkspaceID: "wsp_a", SessionID: "first"}
	second := Key{WorkspaceID: "wsp_a", SessionID: "second"}
	if _, err := broker.Append(Append{Key: first, MessageID: messageID, Data: []byte(`{"n":1}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Append(Append{Key: second, MessageID: messageID, Data: []byte(`{"n":2}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Read(context.Background(), first, 1); !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("expected evicted cursor to expire, got %v", err)
	}
}

func TestAttackerAppendAndCloseCannotEvictAnotherWorkspace(t *testing.T) {
	victim := Key{WorkspaceID: "victim", SessionID: "real"}
	attacker := Key{WorkspaceID: "attacker", SessionID: "first"}
	input := func(key Key, data string) Append { return Append{Key: key, MessageID: "m", Data: []byte(data)} }

	countBroker := NewBrokerWithGlobalLimits(2, 1024, 128, 1, 1024)
	victimEvent, err := countBroker.Append(input(victim, `{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := countBroker.Append(input(attacker, `{"a":1}`)); !errors.Is(err, ErrCapacity) {
		t.Fatalf("cross-Workspace stream admission = %v", err)
	}
	if err := countBroker.Close(attacker); !errors.Is(err, ErrCapacity) {
		t.Fatalf("cross-Workspace close admission = %v", err)
	}
	batch, err := countBroker.ReadAvailable(victim, 0)
	if err != nil || batch.StreamID != victimEvent.StreamID || len(batch.Events) != 1 {
		t.Fatalf("victim stream changed: %#v %v", batch, err)
	}

	byteBroker := NewBrokerWithGlobalLimits(2, 1024, 128, 3, 14)
	victimEvent, err = byteBroker.Append(input(victim, `{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	first, err := byteBroker.Append(input(attacker, `{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := byteBroker.Append(input(attacker, `{"a":2}`)); !errors.Is(err, ErrCapacity) {
		t.Fatalf("cross-Workspace byte admission = %v", err)
	}
	batch, err = byteBroker.ReadAvailable(attacker, 0)
	if err != nil || batch.StreamID != first.StreamID || batch.LatestSequence != 1 || len(batch.Events) != 1 {
		t.Fatalf("rejected append changed attacker stream: %#v %v", batch, err)
	}
	batch, err = byteBroker.ReadAvailable(victim, 0)
	if err != nil || batch.StreamID != victimEvent.StreamID || len(batch.Events) != 1 {
		t.Fatalf("victim replay changed: %#v %v", batch, err)
	}
}

func TestLargestWorkspaceYieldsToSmallerWorkspace(t *testing.T) {
	b := NewBrokerWithGlobalLimits(2, 1024, 128, 3, 1024)
	for _, session := range []string{"fake1", "fake2", "fake3"} {
		if err := b.Close(Key{WorkspaceID: "attacker", SessionID: session}); err != nil {
			t.Fatal(err)
		}
	}
	victim := Key{WorkspaceID: "victim", SessionID: "real"}
	if _, err := b.Append(Append{Key: victim, MessageID: "m", Data: []byte(`{"v":1}`)}); err != nil {
		t.Fatalf("new Workspace locked out: %v", err)
	}
	if _, err := b.ReadAvailable(Key{WorkspaceID: "other", SessionID: "s"}, 0); err != nil {
		t.Fatalf("new subscription locked out: %v", err)
	}
	for _, session := range []string{"x", "y"} {
		_, _ = b.Append(Append{Key: Key{WorkspaceID: "attacker", SessionID: session}, MessageID: "m", Data: []byte(`{"a":1}`)})
	}
	if b.streams[victim] == nil || b.streams[Key{WorkspaceID: "other", SessionID: "s"}] == nil {
		t.Fatal("attacker evicted a smaller Workspace")
	}
}

func TestEventLargerThanStreamIsInvalid(t *testing.T) {
	b := NewBrokerWithLimits(2, 8, 64)
	if _, err := b.Append(Append{Key: Key{WorkspaceID: "w", SessionID: "s"}, MessageID: "m", Data: []byte(`{"x":"0123456789"}`)}); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("oversized event = %v", err)
	}
}

func TestSourceReplayDeduplicatesOnlyWithinRetainedStream(t *testing.T) {
	b := NewBrokerWithLimits(1, 1024, 512)
	input := Append{Key: Key{WorkspaceID: "w", SessionID: "s"}, MessageID: "m", SourceSandboxID: "sbx-one", SourceCursor: 1, Data: json.RawMessage(`{"x":1}`)}
	first, err := b.Append(input)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := b.Append(input)
	if err != nil || retry.Sequence != first.Sequence || retry.StreamID != first.StreamID {
		t.Fatalf("retry=%#v %v", retry, err)
	}
	input.Data = json.RawMessage(`{"x":2}`)
	if _, err = b.Append(input); err == nil {
		t.Fatal("conflicting source accepted")
	}
	input.SourceSandboxID = "sbx-two"
	next, err := b.Append(input)
	if err != nil || next.Sequence != 2 {
		t.Fatal("replacement counter was deduplicated against old runtime")
	}
	fresh := NewBroker()
	reset, err := fresh.Append(input)
	if err != nil || reset.Sequence != 1 || reset.StreamID == first.StreamID {
		t.Fatal("broker restart reused stream identity")
	}
}
func TestEvictionChangesStreamIdentity(t *testing.T) {
	b := NewBrokerWithGlobalLimits(1, 1024, 512, 1, 2048)
	a := Key{WorkspaceID: "w", SessionID: "a"}
	first, _ := b.ReadAvailable(a, 0)
	_, _ = b.ReadAvailable(Key{WorkspaceID: "w", SessionID: "b"}, 0)
	last, _ := b.ReadAvailable(a, 0)
	if first.StreamID == last.StreamID {
		t.Fatal("evicted stream identity reused")
	}
}

func TestUntrustedReadsCannotEvictReplay(t *testing.T) {
	for _, wait := range []bool{false, true} {
		t.Run(fmt.Sprint(wait), func(t *testing.T) {
			b := NewBrokerWithGlobalLimits(2, 1024, 128, 3, 2048)
			victim := Key{WorkspaceID: "victim", SessionID: "real"}
			event, err := b.Append(Append{Key: victim, MessageID: "m", Data: []byte(`{"ok":true}`)})
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 100; i++ {
				key := Key{WorkspaceID: "attacker", SessionID: fmt.Sprint(i)}
				if wait {
					ctx, cancel := context.WithCancel(context.Background())
					cancel()
					_, _ = b.Read(ctx, key, 0)
				} else {
					_, _ = b.ReadAvailable(key, 0)
				}
				if _, err := b.ReadAvailable(Key{WorkspaceID: "attacker", SessionID: "missing"}, 999); !errors.Is(err, ErrCursorExpired) {
					t.Fatal(err)
				}
			}
			batch, err := b.ReadAvailable(victim, 0)
			if err != nil || len(batch.Events) != 1 || batch.StreamID != event.StreamID {
				t.Fatalf("replay lost: %#v %v", batch, err)
			}
			if len(b.streams) > 3 {
				t.Fatal("cache bound exceeded")
			}
		})
	}
}

func TestEmptySubscriptionKeepsIdentityOnFirstEvent(t *testing.T) {
	b := NewBroker()
	key := Key{WorkspaceID: "w", SessionID: "future"}
	initial, err := b.ReadAvailable(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	event, err := b.Append(Append{Key: key, MessageID: "m", Data: []byte(`{}`)})
	if err != nil || event.StreamID != initial.StreamID {
		t.Fatalf("first event reset stream: %#v %v", event, err)
	}
}

func TestReadCapacityDoesNotDiscardOutput(t *testing.T) {
	b := NewBrokerWithGlobalLimits(2, 1024, 128, 1, 2048)
	key := Key{WorkspaceID: "w", SessionID: "real"}
	_, _ = b.Append(Append{Key: key, MessageID: "m", Data: []byte(`{}`)})
	if _, err := b.ReadAvailable(Key{WorkspaceID: "other", SessionID: "future"}, 0); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	batch, err := b.ReadAvailable(key, 0)
	if err != nil || len(batch.Events) != 1 {
		t.Fatal("lost output")
	}
}

func TestEvictedWaiterDoesNotRecreateReservation(t *testing.T) {
	b := NewBrokerWithGlobalLimits(2, 1024, 128, 1, 2048)
	first := Key{WorkspaceID: "w", SessionID: "first"}
	second := Key{WorkspaceID: "w", SessionID: "second"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := b.Read(ctx, first, 0); result <- err }()
	for {
		b.mu.Lock()
		started := b.streams[first] != nil
		b.mu.Unlock()
		if started {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("reader did not start")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	initial, err := b.ReadAvailable(second, 0)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrCursorExpired) {
			t.Fatalf("evicted reader = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("evicted reader kept waiting")
	}
	last, err := b.ReadAvailable(second, 0)
	if err != nil || last.StreamID != initial.StreamID {
		t.Fatal("evicted waiter recreated its reservation")
	}
}

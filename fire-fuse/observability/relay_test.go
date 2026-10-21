package observability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRelayPostsAuthenticatedBatchAndAcknowledges(t *testing.T) {
	token := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	requests := make(chan *http.Request, 1)
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests <- request.Clone(request.Context())
		return &http.Response{
			StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader(`{"accepted":1,"duplicated":0}`)),
			Header: make(http.Header), Request: request,
		}, nil
	})}
	spool := testSpool(t, 2<<20, time.Hour)
	if err := spool.Record(NewEvent("event", "message.accepted", "app_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", time.Now())); err != nil {
		t.Fatal(err)
	}
	relay, err := NewRelay("http://127.0.0.1:3000", token, spool, client)
	if err != nil {
		t.Fatal(err)
	}
	relay.pollInterval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- relay.Run(ctx) }()
	request := <-requests
	if request.URL.Path != ingestPath || request.Header.Get("Authorization") != "Bearer "+token {
		t.Fatalf("request = %s, auth %q", request.URL.Path, request.Header.Get("Authorization"))
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if entry, err := spool.Next(); err != nil || entry != nil {
		t.Fatalf("Next() = %#v, %v", entry, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestRelayRejectsUnencryptedRemoteOrigin(t *testing.T) {
	spool := testSpool(t, 2<<20, time.Hour)
	_, err := NewRelay("http://console.example.com", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", spool, nil)
	if err == nil {
		t.Fatal("NewRelay accepted an unencrypted remote origin")
	}
}

func TestRelayCoalescesSpooledBatchesIntoOneRequest(t *testing.T) {
	spool := testSpool(t, 2<<20, time.Hour)
	for _, id := range []string{"first", "second", "third"} {
		if err := spool.Record(NewEvent(id, "message.accepted", "app_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", time.Now())); err != nil {
			t.Fatal(err)
		}
	}
	var bodies []string
	relay := testRelay(t, spool, func(body string) int {
		bodies = append(bodies, body)
		return http.StatusAccepted
	})
	if delivered, err := relay.deliverNext(context.Background()); err != nil || !delivered {
		t.Fatalf("deliverNext() = %v, %v", delivered, err)
	}
	if len(bodies) != 1 {
		t.Fatalf("requests = %d, want 1", len(bodies))
	}
	var batch Batch
	if err := json.Unmarshal([]byte(bodies[0]), &batch); err != nil || len(batch.Events) != 3 ||
		batch.Events[0].EventID != "first" || batch.Events[2].EventID != "third" {
		t.Fatalf("coalesced batch = %#v, %v", batch, err)
	}
	if entry, err := spool.Next(); err != nil || entry != nil {
		t.Fatalf("spool did not drain: %v %v", entry, err)
	}
}

func TestRelayCoalescesWithinIngestEventLimit(t *testing.T) {
	spool := testSpool(t, 8<<20, time.Hour)
	events := make([]Event, 200)
	for index := range events {
		events[index] = NewEvent(fmt.Sprint("event-", index), "message.accepted", "app_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", time.Now())
	}
	for range 2 {
		if err := spool.Record(events...); err != nil {
			t.Fatal(err)
		}
	}
	var counts []int
	relay := testRelay(t, spool, func(body string) int {
		var batch Batch
		if err := json.Unmarshal([]byte(body), &batch); err != nil {
			t.Fatal(err)
		}
		counts = append(counts, len(batch.Events))
		return http.StatusAccepted
	})
	for range 2 {
		if _, err := relay.deliverNext(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if fmt.Sprint(counts) != "[200 200]" {
		t.Fatalf("request sizes = %v", counts)
	}
}

func TestRelayIsolatesRejectedBatchFromCoalescedRequest(t *testing.T) {
	for _, rejected := range []int{400, 413, 422} {
		t.Run(fmt.Sprint(rejected), func(t *testing.T) {
			spool := testSpool(t, 2<<20, time.Hour)
			for _, id := range []string{"invalid-span", "next-message"} {
				if err := spool.Record(NewEvent(id, "message.accepted", "app_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", time.Now())); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var bodies []string
			relay := testRelay(t, spool, func(body string) int {
				bodies = append(bodies, body)
				if len(bodies) > 3 {
					t.Fatal("rejected batch retried")
				}
				if strings.Contains(body, "invalid-span") {
					return rejected
				}
				cancel()
				return http.StatusAccepted
			})
			if err := relay.Run(ctx); err != nil {
				t.Fatal(err)
			}
			if len(bodies) != 3 || !strings.Contains(bodies[0], "next-message") ||
				!strings.Contains(bodies[1], "invalid-span") || strings.Contains(bodies[1], "next-message") ||
				!strings.Contains(bodies[2], "next-message") || strings.Contains(bodies[2], "invalid-span") {
				t.Fatalf("requests: %v", bodies)
			}
			if entry, err := spool.Next(); err != nil || entry != nil {
				t.Fatalf("spool did not drain: %v %v", entry, err)
			}
		})
	}
}

func TestRelayDropsSingleRejectedBatch(t *testing.T) {
	spool := testSpool(t, 2<<20, time.Hour)
	if err := spool.Record(NewEvent("invalid-span", "message.accepted", "app_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", time.Now())); err != nil {
		t.Fatal(err)
	}
	requests := 0
	relay := testRelay(t, spool, func(string) int {
		requests++
		return http.StatusUnprocessableEntity
	})
	if delivered, err := relay.deliverNext(context.Background()); err != nil || !delivered || requests != 1 {
		t.Fatalf("deliverNext() = %v, %v after %d requests", delivered, err, requests)
	}
	if entry, err := spool.Next(); err != nil || entry != nil {
		t.Fatalf("spool did not drain: %v %v", entry, err)
	}
}

func TestRelayPreservesBatchOnRecoverableFailure(t *testing.T) {
	for _, status := range []int{0, 401, 403, 429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			spool := testSpool(t, 2<<20, time.Hour)
			if err := spool.Record(NewEvent("retry-me", "message.accepted", "app_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", time.Now())); err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				if status == 0 {
					return nil, errors.New("network unavailable")
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
			})}
			relay, err := NewRelay("http://127.0.0.1:3000", strings.Repeat("a", 64), spool, client)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := relay.deliverNext(context.Background()); err == nil {
				t.Fatal("failure was not retried")
			}
			if entry, err := spool.Next(); err != nil || entry == nil {
				t.Fatalf("recoverable batch dropped: %v %v", entry, err)
			}
		})
	}
}

func TestRelayKeepsUnsentFilesWhenIsolationIsInterrupted(t *testing.T) {
	spool := testSpool(t, 2<<20, time.Hour)
	for _, id := range []string{"invalid-span", "next-message"} {
		if err := spool.Record(NewEvent(id, "message.accepted", "app_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", time.Now())); err != nil {
			t.Fatal(err)
		}
	}
	relay := testRelay(t, spool, func(body string) int {
		if strings.Contains(body, "invalid-span") {
			return http.StatusUnprocessableEntity
		}
		return http.StatusServiceUnavailable
	})
	if _, err := relay.deliverNext(context.Background()); err == nil {
		t.Fatal("unavailable ingest was not retried")
	}
	entry, err := spool.Next()
	if err != nil || entry == nil || !strings.Contains(string(entry.Data), "next-message") {
		t.Fatalf("Next() = %v, %v", entry, err)
	}
}

func testRelay(t *testing.T, spool *Spool, respond func(body string) int) *Relay {
	t.Helper()
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		data, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: respond(string(data)), Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	relay, err := NewRelay("http://127.0.0.1:3000", strings.Repeat("a", 64), spool, client)
	if err != nil {
		t.Fatal(err)
	}
	relay.pollInterval = time.Millisecond
	return relay
}

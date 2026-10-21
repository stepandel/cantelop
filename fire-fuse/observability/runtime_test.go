package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/messagedelivery"
	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
)

func TestRuntimeRecorderIsDisabledWithoutSink(t *testing.T) {
	if RuntimeRecorder(nil, nil) != nil {
		t.Fatal("RuntimeRecorder(nil) returned a recorder")
	}
}

func TestRuntimeRecorderDoesNotFailUserWorkWhenSpoolIsFull(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	spool, err := OpenSpool(SpoolConfig{
		Directory: directory, MaxBytes: 1 << 20, MaxAge: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	filler := NewEvent("filler", "log.recorded", "app_0123456789abcdef0123456789abcdef", time.Now())
	filler.Body = string(make([]byte, 32<<10))
	for index := 0; index < 40; index++ {
		filler.EventID = fmt.Sprintf("filler:%d", index)
		if err := spool.Record(filler); err != nil {
			break
		}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	recorder := RuntimeRecorder(spool, logger)
	err = recorder(context.Background(), messagedelivery.ObservationRecord{
		AppID:     "app_0123456789abcdef0123456789abcdef",
		AttemptID: "att_0123456789abcdef0123456789abcdef",
		TraceID:   "abcdefabcdefabcdefabcdefabcdefab",
		Observation: messageprotocol.RuntimeObservation{
			Type: "log.recorded", LogID: "log_11111111111111111111111111111111",
			Severity: "info", Body: "must not fail", OccurredAt: "2026-09-01T12:00:00Z",
			Attributes: json.RawMessage(`{}`),
		},
	})
	if err != nil {
		t.Fatalf("runtime observation changed user execution: %v", err)
	}
}

func TestRuntimeRecorderBuildsSDKIngestEvent(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	spool, err := OpenSpool(SpoolConfig{
		Directory: directory, MaxBytes: 2 << 20, MaxAge: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := RuntimeRecorder(spool, nil)
	err = recorder(context.Background(), messagedelivery.ObservationRecord{
		AppID:       "app_0123456789abcdef0123456789abcdef",
		WorkspaceID: "wsp_0123456789abcdef0123456789abcdef",
		SessionID:   "agent:primary", MessageID: "msg_0123456789abcdef0123456789abcdef",
		AttemptID: "att_0123456789abcdef0123456789abcdef", TraceID: "abcdefabcdefabcdefabcdefabcdefab",
		Observation: messageprotocol.RuntimeObservation{
			Type: "log.recorded", LogID: "log_11111111111111111111111111111111",
			Severity: "info", Body: "handler entered", OccurredAt: "2026-09-01T12:00:00Z",
			Attributes: json.RawMessage(`{"model":"test"}`),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := spool.Next()
	if err != nil || entry == nil {
		t.Fatalf("Next() = %#v, %v", entry, err)
	}
	var batch Batch
	if err := json.Unmarshal(entry.Data, &batch); err != nil || len(batch.Events) != 1 {
		t.Fatalf("batch = %#v, %v", batch, err)
	}
	event := batch.Events[0]
	if event.Source != "sdk" || event.Type != "log.recorded" || event.AttemptID == "" || event.Body != "handler entered" {
		t.Fatalf("event = %#v", event)
	}
}

func TestRuntimeRecorderPreservesSandboxScopeWithoutAttempt(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	spool, err := OpenSpool(SpoolConfig{
		Directory: directory, MaxBytes: 2 << 20, MaxAge: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := RuntimeRecorder(spool, nil)
	err = recorder(context.Background(), messagedelivery.ObservationRecord{
		AppID: "app_0123456789abcdef0123456789abcdef", WorkspaceID: "wsp_0123456789abcdef0123456789abcdef",
		SessionID: "agent:primary", ConcreteSessionID: "ses_actor",
		SandboxID: "sbx-0123456789abcdef0123456789abcdef",
		Observation: messageprotocol.RuntimeObservation{
			Type: "log.recorded", LogID: "log_11111111111111111111111111111111",
			Severity: "error", Body: "startup failed", OccurredAt: "2026-09-01T12:00:00Z",
			Attributes: json.RawMessage(`{"source":"stderr"}`),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := spool.Next()
	if err != nil || entry == nil {
		t.Fatalf("Next() = %#v, %v", entry, err)
	}
	var batch Batch
	if err := json.Unmarshal(entry.Data, &batch); err != nil || len(batch.Events) != 1 {
		t.Fatalf("batch = %#v, %v", batch, err)
	}
	event := batch.Events[0]
	if event.SandboxID == "" || event.ConcreteSessionID == "" || event.AttemptID != "" || event.TraceID != "" {
		t.Fatalf("event = %#v", event)
	}
}

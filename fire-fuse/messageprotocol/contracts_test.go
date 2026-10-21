package messageprotocol

import (
	"encoding/json"
	"testing"
)

const testMessageID = "msg_0123456789abcdef0123456789abcdef"

func TestRequestMatchesSDKSessionRuntimeEnvelope(t *testing.T) {
	request := Request{
		Session: SessionEnvelope{
			ID: "agent:primary", WorkspaceID: "wsp_0123456789abcdef0123456789abcdef",
			KeepAliveSeconds: 300,
		},
		Message: Message{ID: testMessageID, Payload: json.RawMessage(`{"type":"prompt"}`)},
	}
	body, err := request.Body()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"session":{"id":"agent:primary","workspace_id":"wsp_0123456789abcdef0123456789abcdef","keep_alive_seconds":300},"message":{"id":"msg_0123456789abcdef0123456789abcdef","payload":{"type":"prompt"}}}`
	if string(body) != want {
		t.Fatalf("Body() = %s", body)
	}
}

func TestRequestRejectsInvalidIdentityAndPayload(t *testing.T) {
	valid := Request{
		Session: SessionEnvelope{ID: "agent", WorkspaceID: "wsp_0123456789abcdef0123456789abcdef"},
		Message: Message{ID: testMessageID, Payload: json.RawMessage(`{}`)},
	}
	tests := []Request{valid, valid, valid}
	tests[0].Session.WorkspaceID = "wsp_bad"
	tests[1].Message.ID = "exec_0123456789abcdef0123456789abcdef"
	tests[2].Message.Payload = json.RawMessage(`{`)
	for _, request := range tests {
		if err := request.Validate(); err == nil {
			t.Fatalf("Validate() accepted %#v", request)
		}
	}
}

func TestObservabilityContextCorrelatesRequestAndAttempt(t *testing.T) {
	requestID := "req_abcdefabcdefabcdefabcdefabcdefab"
	observability, err := NewObservabilityContext(requestID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if observability.Attempt != 2 || observability.RequestID != requestID ||
		observability.TraceParent[3:35] != requestID[len("req_"):] || observability.Validate() != nil {
		t.Fatalf("observability context = %#v", observability)
	}
	request := Request{
		Session:       SessionEnvelope{ID: "agent", WorkspaceID: "wsp_0123456789abcdef0123456789abcdef"},
		Message:       Message{ID: testMessageID, Payload: json.RawMessage(`{}`)},
		Observability: &observability,
	}
	if request.Validate() != nil {
		t.Fatal("Request rejected valid observability context")
	}
	request.Observability.TraceParent = "00-00000000000000000000000000000000-0123456789abcdef-01"
	if request.Validate() == nil {
		t.Fatal("Request accepted an invalid observability context")
	}
}

func TestOutputBatchRequiresOrderedMessageEvents(t *testing.T) {
	batch := OutputBatch{Events: []OutputEvent{
		{Cursor: 4, MessageID: testMessageID, Data: json.RawMessage(`{"token":"a"}`)},
		{Cursor: 5, MessageID: testMessageID, Data: json.RawMessage(`{"token":"b"}`)},
	}}
	if err := batch.Validate(3, false); err != nil {
		t.Fatal(err)
	}
	batch.Events[1].Cursor = 6
	if err := batch.Validate(3, false); err == nil {
		t.Fatal("Validate accepted a cursor gap")
	}
}

func TestRuntimeObservationBatchValidatesOrderedSDKRecords(t *testing.T) {
	batch := ObservationBatch{Observations: []ObservationEvent{
		{Cursor: 1, MessageID: testMessageID, Observation: RuntimeObservation{
			Type: "span.started", SpanID: "1111111111111111", Name: "session.receive", Kind: "internal",
			StartedAt: "2026-09-01T12:00:00Z", Attributes: json.RawMessage(`{"model":"test"}`),
		}},
		{Cursor: 2, MessageID: testMessageID, Observation: RuntimeObservation{
			Type: "log.recorded", LogID: "log_22222222222222222222222222222222", SpanID: "1111111111111111",
			Severity: "info", Body: "handler entered", OccurredAt: "2026-09-01T12:00:01Z", Attributes: json.RawMessage(`{}`),
		}},
		{Cursor: 3, MessageID: testMessageID, Observation: RuntimeObservation{
			Type: "span.completed", SpanID: "1111111111111111", Status: "ok",
			FinishedAt: "2026-09-01T12:00:02Z", Attributes: json.RawMessage(`{}`),
		}},
	}}
	if err := batch.Validate(0, false); err != nil {
		t.Fatal(err)
	}
	batch.Observations[1].Observation.Body = ""
	if err := batch.Validate(0, false); err == nil {
		t.Fatal("ObservationBatch accepted an empty log body")
	}
}

func TestRuntimeObservationBatchAllowsOnlyUnscopedRuntimeLogsWithoutMessageIdentity(t *testing.T) {
	log := ObservationEvent{Cursor: 1, Observation: RuntimeObservation{
		Type: "log.recorded", LogID: "log_22222222222222222222222222222222",
		Severity: "error", Body: "startup failed", OccurredAt: "2026-09-01T12:00:01Z",
		Attributes: json.RawMessage(`{"source":"stderr"}`),
	}}
	if err := (ObservationBatch{Observations: []ObservationEvent{log}}).Validate(0, false); err != nil {
		t.Fatal(err)
	}
	log.Observation.SpanID = "1111111111111111"
	if err := (ObservationBatch{Observations: []ObservationEvent{log}}).Validate(0, false); err == nil {
		t.Fatal("ObservationBatch accepted an unscoped span log")
	}
}

func TestRecoveryContractsBindOneInterruptedMessage(t *testing.T) {
	request := RecoveryRequest{
		RecoveryID: "rcv_11111111111111111111111111111111",
		MessageID:  testMessageID,
		Session:    SessionEnvelope{ID: "thread", WorkspaceID: "wsp_33333333333333333333333333333333"},
	}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	receipt := RecoveryReceipt{RecoveryID: request.RecoveryID, Generation: 1, State: "completed"}
	if err := receipt.Validate(request.RecoveryID); err != nil {
		t.Fatal(err)
	}
	receipt.State = "running"
	if err := receipt.Validate(request.RecoveryID); err == nil {
		t.Fatal("recovery receipt accepted a nonterminal state")
	}
}

package sessionevents

import (
	"encoding/json"

	"github.com/stepandel/cantelop/internal/jsonwire"
)

// MarshalEvent preserves the payload byte budget when returning an event.
func MarshalEvent(event Event) ([]byte, error) {
	metadata := struct {
		Event
		Data json.RawMessage `json:"data,omitempty"`
	}{Event: event}
	return jsonwire.MarshalWithRawField(metadata, "data", event.Data)
}

// MarshalBatch keeps bounded replay data within the control client's wire budget.
func MarshalBatch(batch Batch) ([]byte, error) {
	events := []byte{'['}
	for i, event := range batch.Events {
		document, err := MarshalEvent(event)
		if err != nil {
			return nil, err
		}
		if i > 0 {
			events = append(events, ',')
		}
		events = append(events, document...)
	}
	events = append(events, ']')
	if batch.Events == nil {
		events = []byte("null")
	}
	metadata := struct {
		Batch
		Events json.RawMessage `json:"events,omitempty"`
	}{Batch: batch}
	return jsonwire.MarshalWithRawField(metadata, "events", events)
}

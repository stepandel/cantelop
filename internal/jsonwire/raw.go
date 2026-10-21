// Package jsonwire preserves bounded, already-encoded JSON payloads in envelopes.
package jsonwire

import (
	"bytes"
	"encoding/json"
	"errors"
)

// MarshalWithRawField adds one raw JSON field to an object envelope. The envelope
// must omit that field. Unlike re-marshalling RawMessage, this does not expand
// HTML characters or Unicode line separators inside the payload.
func MarshalWithRawField(envelope any, field string, payload json.RawMessage) ([]byte, error) {
	metadata, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	if len(metadata) < 2 || metadata[0] != '{' || metadata[len(metadata)-1] != '}' {
		return nil, errors.New("JSON envelope must be an object")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, payload); err != nil {
		return nil, err
	}
	name, err := json.Marshal(field)
	if err != nil {
		return nil, err
	}
	document := metadata[:len(metadata)-1]
	if len(document) > 1 {
		document = append(document, ',')
	}
	document = append(document, name...)
	document = append(document, ':')
	document = append(document, compact.Bytes()...)
	return append(document, '}'), nil
}

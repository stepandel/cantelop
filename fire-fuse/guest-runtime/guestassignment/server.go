package guestassignment

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"time"
)

const (
	VSockPort               = 19092
	EventTemplateReady      = "template_ready"
	EventAssignmentAccepted = "assignment_accepted"
	EventAssignmentFailed   = "assignment_failed"
	protocolWriteTimeout    = 5 * time.Second
	maxErrorBytes           = 1024
)

// Event is one newline-delimited response from the guest assignment agent.
type Event struct {
	SchemaVersion int    `json:"schema_version"`
	Event         string `json:"event"`
	AssignmentID  string `json:"assignment_id,omitempty"`
	Error         string `json:"error,omitempty"`
}

// Handler applies the accepted assignment and reports completed milestones.
type Handler func(context.Context, Assignment, Milestone) error

// Serve accepts readiness probes until one assignment is received. It returns
// after that assignment succeeds or fails, preventing VM reuse.
func Serve(ctx context.Context, listener net.Listener, handler Handler) error {
	if listener == nil {
		return errors.New("guest assignment listener is required")
	}
	if handler == nil {
		return errors.New("guest assignment handler is required")
	}
	type connectionResult struct {
		assigned bool
		err      error
	}
	connections := make(chan net.Conn)
	acceptErrors := make(chan error, 1)
	results := make(chan connectionResult, 16)
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				acceptErrors <- err
				return
			}
			select {
			case connections <- connection:
			case <-ctx.Done():
				connection.Close()
				return
			}
		}
	}()

	var claimed atomic.Bool
	for {
		select {
		case <-ctx.Done():
			_ = listener.Close()
			return ctx.Err()
		case err := <-acceptErrors:
			return fmt.Errorf("accept assignment connection: %w", err)
		case connection := <-connections:
			go func() {
				assigned, serveErr := serveConnection(
					ctx,
					connection,
					handler,
					&claimed,
				)
				closeErr := connection.Close()
				if serveErr == nil && closeErr != nil {
					serveErr = fmt.Errorf("close assignment connection: %w", closeErr)
				}
				results <- connectionResult{assigned: assigned, err: serveErr}
			}()
		case result := <-results:
			if !result.assigned {
				continue
			}
			_ = listener.Close()
			return result.err
		}
	}
}

func serveConnection(
	ctx context.Context,
	connection net.Conn,
	handler Handler,
	claimed *atomic.Bool,
) (bool, error) {
	if err := writeEvent(connection, Event{
		SchemaVersion: SchemaVersion,
		Event:         EventTemplateReady,
	}); err != nil {
		return false, fmt.Errorf("report template readiness: %w", err)
	}

	document, err := readDocument(connection)
	if errors.Is(err, io.EOF) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read assignment: %w", err)
	}
	assignment, err := Decode(bytes.NewReader(document))
	if err != nil {
		_ = writeFailure(connection, "", err)
		return true, err
	}
	if !claimed.CompareAndSwap(false, true) {
		err := errors.New("guest already has an accepted assignment")
		_ = writeFailure(connection, assignment.AssignmentID, err)
		return false, nil
	}
	if err := writeEvent(connection, Event{
		SchemaVersion: SchemaVersion,
		Event:         EventAssignmentAccepted,
		AssignmentID:  assignment.AssignmentID,
	}); err != nil {
		return true, fmt.Errorf("accept assignment: %w", err)
	}

	report := func(event string) error {
		return writeEvent(connection, Event{
			SchemaVersion: SchemaVersion,
			Event:         event,
			AssignmentID:  assignment.AssignmentID,
		})
	}
	if err := handler(ctx, assignment, report); err != nil {
		_ = writeFailure(connection, assignment.AssignmentID, err)
		return true, fmt.Errorf("apply assignment %s: %w", assignment.AssignmentID, err)
	}
	return true, nil
}

func readDocument(reader io.Reader) ([]byte, error) {
	limited := io.LimitReader(reader, maxDocumentBytes+2)
	line, err := bufio.NewReaderSize(limited, maxDocumentBytes+2).ReadBytes('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && len(line) == 0 {
			return nil, io.EOF
		}
		return nil, invalid("assignment must be one newline-terminated JSON object")
	}
	if len(line) > maxDocumentBytes+1 {
		return nil, invalid("document exceeds 1 MiB")
	}
	return bytes.TrimSuffix(line, []byte{'\n'}), nil
}

func writeFailure(connection net.Conn, assignmentID string, cause error) error {
	detail := cause.Error()
	if len(detail) > maxErrorBytes {
		detail = detail[:maxErrorBytes]
	}
	return writeEvent(connection, Event{
		SchemaVersion: SchemaVersion,
		Event:         EventAssignmentFailed,
		AssignmentID:  assignmentID,
		Error:         detail,
	})
}

func writeEvent(connection net.Conn, event Event) error {
	if !validEventName(event.Event) {
		return fmt.Errorf("invalid event name %q", event.Event)
	}
	document, err := json.Marshal(event)
	if err != nil {
		return err
	}
	document = append(document, '\n')
	if err := connection.SetWriteDeadline(time.Now().Add(protocolWriteTimeout)); err != nil {
		return err
	}
	_, err = connection.Write(document)
	return err
}

func validEventName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, character := range name {
		if (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') &&
			character != '_' {
			return false
		}
	}
	return !strings.HasPrefix(name, "_")
}

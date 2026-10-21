package guestassignment

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"
)

func TestServeAllowsProbeThenAppliesOneAssignment(t *testing.T) {
	listener := newPipeListener()
	defer listener.Close()

	handled := make(chan Assignment, 1)
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- Serve(
			context.Background(),
			listener,
			func(_ context.Context, assignment Assignment, report Milestone) error {
				handled <- assignment
				for _, event := range []string{
					EventClockSynchronized,
					EventNetworkConfigured,
					EventContainerMounted,
					EventWorkspaceMounted,
					EventWorkloadStarted,
				} {
					if err := report(event); err != nil {
						return err
					}
				}
				return nil
			},
		)
	}()

	probe := listener.Dial()
	assertEvent(t, bufio.NewReader(probe), EventTemplateReady)
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}

	connection := listener.Dial()
	defer connection.Close()
	reader := bufio.NewReader(connection)
	assertEvent(t, reader, EventTemplateReady)
	if err := json.NewEncoder(connection).Encode(validAssignment()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		EventAssignmentAccepted,
		EventClockSynchronized,
		EventNetworkConfigured,
		EventContainerMounted,
		EventWorkspaceMounted,
		EventWorkloadStarted,
	} {
		event := assertEvent(t, reader, want)
		if event.AssignmentID != "assignment-01" {
			t.Errorf("%s assignment ID = %q", want, event.AssignmentID)
		}
	}

	select {
	case assignment := <-handled:
		if assignment.AssignmentID != "assignment-01" {
			t.Fatalf("handled assignment = %q", assignment.AssignmentID)
		}
	case <-time.After(time.Second):
		t.Fatal("handler was not called")
	}
	select {
	case err := <-serveResult:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve() did not stop after one assignment")
	}
}

func TestServeRejectsInvalidAssignmentAndStops(t *testing.T) {
	listener := newPipeListener()
	defer listener.Close()
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- Serve(
			context.Background(),
			listener,
			func(context.Context, Assignment, Milestone) error {
				return errors.New("handler must not run")
			},
		)
	}()

	connection := listener.Dial()
	defer connection.Close()
	reader := bufio.NewReader(connection)
	assertEvent(t, reader, EventTemplateReady)
	if _, err := fmt.Fprintln(connection, `{"schema_version":1}`); err != nil {
		t.Fatal(err)
	}
	failed := assertEvent(t, reader, EventAssignmentFailed)
	if failed.Error == "" {
		t.Fatal("failure event has no error")
	}
	select {
	case err := <-serveResult:
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("Serve() error = %v, want ErrInvalid", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve() did not stop after invalid assignment")
	}
}

func TestServeAcceptsAssignmentWhileReadinessProbeRemainsOpen(t *testing.T) {
	listener := newPipeListener()
	defer listener.Close()
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- Serve(
			context.Background(),
			listener,
			func(context.Context, Assignment, Milestone) error { return nil },
		)
	}()

	probe := listener.Dial()
	defer probe.Close()
	assertEvent(t, bufio.NewReader(probe), EventTemplateReady)

	assignment := listener.Dial()
	defer assignment.Close()
	reader := bufio.NewReader(assignment)
	assertEvent(t, reader, EventTemplateReady)
	if err := json.NewEncoder(assignment).Encode(validAssignment()); err != nil {
		t.Fatal(err)
	}
	assertEvent(t, reader, EventAssignmentAccepted)

	select {
	case err := <-serveResult:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("open readiness probe prevented assignment")
	}
}

func TestReadDocumentRequiresBoundedNewlineTerminatedJSON(t *testing.T) {
	for _, document := range []string{
		`{}`,
		string(make([]byte, maxDocumentBytes+2)) + "\n",
	} {
		if _, err := readDocument(&oneReadReader{document: []byte(document)}); !errors.Is(err, ErrInvalid) {
			t.Errorf("readDocument() error = %v, want ErrInvalid", err)
		}
	}
}

type oneReadReader struct {
	document []byte
	read     bool
}

func (r *oneReadReader) Read(buffer []byte) (int, error) {
	if r.read {
		return 0, errors.New("unexpected extra read")
	}
	r.read = true
	return copy(buffer, r.document), nil
}

func assertEvent(t *testing.T, reader *bufio.Reader, want string) Event {
	t.Helper()
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read %s event: %v", want, err)
	}
	var event Event
	if err := json.Unmarshal(line, &event); err != nil {
		t.Fatalf("decode %s event: %v", want, err)
	}
	if event.SchemaVersion != SchemaVersion || event.Event != want {
		t.Fatalf("event = %#v, want %q", event, want)
	}
	return event
}

type pipeListener struct {
	connections chan net.Conn
	closed      chan struct{}
	closeOnce   sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{
		connections: make(chan net.Conn),
		closed:      make(chan struct{}),
	}
}

func (l *pipeListener) Dial() net.Conn {
	client, server := net.Pipe()
	select {
	case l.connections <- server:
		return client
	case <-l.closed:
		client.Close()
		server.Close()
		return client
	}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case connection := <-l.connections:
		return connection, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.closeOnce.Do(func() {
		close(l.closed)
	})
	return nil
}

func (l *pipeListener) Addr() net.Addr {
	return pipeAddress{}
}

type pipeAddress struct{}

func (pipeAddress) Network() string { return "pipe" }
func (pipeAddress) String() string  { return "pipe" }

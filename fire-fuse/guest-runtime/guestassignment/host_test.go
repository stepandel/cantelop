package guestassignment

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestconfig"
)

func TestAssignSendsDocumentAndWaitsForMilestones(t *testing.T) {
	host, guest := net.Pipe()
	t.Cleanup(func() {
		host.Close()
		guest.Close()
	})
	assignment := validAssignment()
	assignment.Workload.Process.Env = append(
		assignment.Workload.Process.Env,
		guestconfig.InternalPortEnvironmentName+"=8080",
	)
	serverErrors := make(chan error, 1)
	go func() {
		defer close(serverErrors)
		if err := writeEvent(guest, Event{
			SchemaVersion: SchemaVersion,
			Event:         EventTemplateReady,
		}); err != nil {
			serverErrors <- err
			return
		}
		document, err := bufio.NewReader(guest).ReadBytes('\n')
		if err != nil {
			serverErrors <- err
			return
		}
		got, err := Decode(bytes.NewReader(bytes.TrimSpace(document)))
		if err != nil {
			serverErrors <- err
			return
		}
		if !reflect.DeepEqual(got, assignment) {
			serverErrors <- errors.New("unexpected assignment document")
			return
		}
		for _, name := range []string{
			EventAssignmentAccepted,
			EventClockSynchronized,
			EventNetworkConfigured,
			EventContainerMounted,
			EventWorkspaceMounted,
			EventWorkloadStarted,
			EventSDKListenerReady,
		} {
			if err := writeEvent(guest, Event{
				SchemaVersion: SchemaVersion,
				Event:         name,
				AssignmentID:  assignment.AssignmentID,
			}); err != nil {
				serverErrors <- err
				return
			}
		}
	}()

	var milestones []string
	dialCalls := 0
	err := assign(
		context.Background(),
		"/run/fire-fuse.vsock",
		assignment,
		func(name string) error {
			milestones = append(milestones, name)
			return nil
		},
		func(context.Context, string, uint32) (net.Conn, error) {
			dialCalls++
			if dialCalls == 1 {
				return nil, errors.New("listener not resumed yet")
			}
			return host, nil
		},
	)
	if err != nil {
		t.Fatalf("assign() error = %v", err)
	}
	if err := <-serverErrors; err != nil {
		t.Fatalf("server error = %v", err)
	}
	if dialCalls != 2 {
		t.Fatalf("dial calls = %d, want 2", dialCalls)
	}
	want := []string{
		EventAssignmentAccepted,
		EventClockSynchronized,
		EventNetworkConfigured,
		EventContainerMounted,
		EventWorkspaceMounted,
		EventWorkloadStarted,
		EventSDKListenerReady,
	}
	if !reflect.DeepEqual(milestones, want) {
		t.Fatalf("milestones = %q, want %q", milestones, want)
	}
}

func TestAssignReturnsGuestFailure(t *testing.T) {
	host, guest := net.Pipe()
	t.Cleanup(func() {
		host.Close()
		guest.Close()
	})
	assignment := validAssignment()
	go func() {
		_ = writeEvent(guest, Event{
			SchemaVersion: SchemaVersion,
			Event:         EventTemplateReady,
		})
		_, _ = bufio.NewReader(guest).ReadBytes('\n')
		_ = writeEvent(guest, Event{
			SchemaVersion: SchemaVersion,
			Event:         EventAssignmentFailed,
			AssignmentID:  assignment.AssignmentID,
			Error:         "mount failed",
		})
	}()
	err := assign(
		context.Background(),
		"/run/fire-fuse.vsock",
		assignment,
		nil,
		func(context.Context, string, uint32) (net.Conn, error) {
			return host, nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "mount failed") {
		t.Fatalf("assign() error = %v", err)
	}
}

func TestDialHostCompletesFirecrackerHandshakeWithoutLosingGuestData(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	serverResult := make(chan error, 1)
	go func() {
		command, err := bufio.NewReader(server).ReadString('\n')
		if err != nil {
			serverResult <- err
			return
		}
		if command != "CONNECT 19092\n" {
			serverResult <- errors.New("unexpected connect command")
			return
		}
		event, err := json.Marshal(Event{
			SchemaVersion: SchemaVersion,
			Event:         EventTemplateReady,
		})
		if err == nil {
			_, err = server.Write(append([]byte("OK 123\n"), append(event, '\n')...))
		}
		serverResult <- err
	}()

	connection, err := dialHost(
		context.Background(),
		"/run/fire-fuse.vsock",
		VSockPort,
		func(context.Context, string, string) (net.Conn, error) {
			return client, nil
		},
	)
	if err != nil {
		t.Fatalf("dialHost() error = %v", err)
	}
	defer connection.Close()
	event, err := ReadEvent(connection)
	if err != nil {
		t.Fatalf("ReadEvent() error = %v", err)
	}
	if event.Event != EventTemplateReady {
		t.Fatalf("event = %#v", event)
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
}

func TestDialHostRejectsInvalidAcknowledgement(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	go func() {
		_, _ = bufio.NewReader(server).ReadString('\n')
		_, _ = server.Write([]byte("NO\n"))
	}()
	_, err := dialHost(
		context.Background(),
		"/run/fire-fuse.vsock",
		VSockPort,
		func(context.Context, string, string) (net.Conn, error) {
			return client, nil
		},
	)
	if err == nil {
		t.Fatal("dialHost() error = nil")
	}
}

func TestReadEventRejectsUnknownFields(t *testing.T) {
	_, err := ReadEvent(
		bufio.NewReaderSize(
			&staticReader{value: `{"schema_version":2,"event":"template_ready","unknown":true}` + "\n"},
			128,
		),
	)
	if err == nil {
		t.Fatal("ReadEvent() error = nil")
	}
}

type staticReader struct {
	value string
	read  bool
}

func (r *staticReader) Read(buffer []byte) (int, error) {
	if r.read {
		return 0, errors.New("unexpected read")
	}
	r.read = true
	return copy(buffer, r.value), nil
}

// The guest writes each event with its own write, so several can land in a
// single read. A connection must keep the extras queued: a reader that lives
// only for one call buffers them and then throws them away, and the next read
// blocks for an event that was already delivered.
//
// net.Pipe cannot show this — it is synchronous and unbuffered, so writes never
// coalesce. This uses a real socket.
func TestReadEventKeepsCoalescedEventsQueued(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	written := []string{EventAssignmentAccepted, EventClockSynchronized, EventNetworkConfigured}
	accepted := make(chan error, 1)
	go func() {
		server, acceptErr := listener.Accept()
		if acceptErr != nil {
			accepted <- acceptErr
			return
		}
		defer server.Close()
		var batch []byte
		for _, name := range written {
			document, marshalErr := json.Marshal(Event{
				SchemaVersion: SchemaVersion, Event: name, AssignmentID: "asn-1",
			})
			if marshalErr != nil {
				accepted <- marshalErr
				return
			}
			batch = append(batch, document...)
			batch = append(batch, '\n')
		}
		// One write, so all three events arrive together.
		_, writeErr := server.Write(batch)
		accepted <- writeErr
	}()

	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := <-accepted; err != nil {
		t.Fatal(err)
	}

	connection := &bufferedConnection{
		Conn:   client,
		reader: bufio.NewReaderSize(client, maxEventBytes+1),
	}
	for _, want := range written {
		event, err := readEventWithin(context.Background(), connection)
		if err != nil {
			t.Fatalf("ReadEvent() for %s error = %v", want, err)
		}
		if event.Event != want {
			t.Fatalf("event = %q, want %q", event.Event, want)
		}
	}
}

// dialHost clears the handshake deadline so the connection can outlive the
// context that dialed it. Reads taken afterwards must set their own bound, or a
// guest that accepts the connection and then goes quiet blocks the caller past
// its own deadline.
func TestReadEventWithinStopsAtContextDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		server, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		// Accept the connection and never write, like a hung guest agent.
		<-time.After(time.Minute)
		server.Close()
	}()

	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.SetDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, readErr := readEventWithin(ctx, client)
		done <- readErr
	}()
	select {
	case readErr := <-done:
		if readErr == nil {
			t.Fatal("readEventWithin() error = nil, want a deadline failure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("readEventWithin() ignored the context deadline")
	}
}

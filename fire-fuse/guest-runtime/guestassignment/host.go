package guestassignment

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestconfig"
	"github.com/stepandel/cantelop/fire-fuse/telemetry/warmuptrace"
)

const (
	hostHandshakeTimeout = 5 * time.Second
	maxHandshakeBytes    = 128
	maxEventBytes        = 4096
	readyRetryInterval   = 20 * time.Millisecond
	readyAttemptTimeout  = 500 * time.Millisecond
)

var vsockAcknowledgement = regexp.MustCompile(`^OK [0-9]+\n$`)

type dialContextFunc func(context.Context, string, string) (net.Conn, error)
type assignmentDialFunc func(context.Context, string, uint32) (net.Conn, error)

// DialHost connects through Firecracker's host Unix socket to a guest AF_VSOCK
// listener.
func DialHost(ctx context.Context, socketPath string, port uint32) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: hostHandshakeTimeout}
	return dialHost(ctx, socketPath, port, dialer.DialContext)
}

func dialHost(
	ctx context.Context,
	socketPath string,
	port uint32,
	dial dialContextFunc,
) (net.Conn, error) {
	if !filepath.IsAbs(socketPath) || filepath.Clean(socketPath) != socketPath ||
		socketPath == string(filepath.Separator) {
		return nil, errors.New("vsock host path must be clean and absolute")
	}
	if port == 0 {
		return nil, errors.New("vsock guest port must be non-zero")
	}
	connection, err := dial(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("connect to Firecracker vsock: %w", err)
	}
	success := false
	defer func() {
		if !success {
			connection.Close()
		}
	}()

	deadline := time.Now().Add(hostHandshakeTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("set vsock handshake deadline: %w", err)
	}
	if _, err := fmt.Fprintf(connection, "CONNECT %d\n", port); err != nil {
		return nil, fmt.Errorf("request guest vsock connection: %w", err)
	}
	reader := bufio.NewReaderSize(connection, maxEventBytes+1)
	acknowledgement, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("read Firecracker vsock acknowledgement: %w", err)
	}
	if len(acknowledgement) > maxHandshakeBytes ||
		!vsockAcknowledgement.MatchString(acknowledgement) {
		return nil, fmt.Errorf("invalid Firecracker vsock acknowledgement %q", acknowledgement)
	}
	if err := connection.SetDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("clear vsock handshake deadline: %w", err)
	}
	success = true
	return &bufferedConnection{Conn: connection, reader: reader}, nil
}

// eventLineReader is implemented by connections that own a persistent buffered
// reader across calls.
type eventLineReader interface {
	readEventLine() ([]byte, error)
}

// ReadEvent reads one bounded, strict, newline-delimited agent event.
func ReadEvent(reader io.Reader) (Event, error) {
	if reader == nil {
		return Event{}, errors.New("event reader is required")
	}
	line, err := readEventLine(reader)
	if err != nil {
		return Event{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(line)))
	decoder.DisallowUnknownFields()
	var event Event
	if err := decoder.Decode(&event); err != nil {
		return Event{}, fmt.Errorf("decode guest assignment event: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Event{}, errors.New("guest assignment event must contain one JSON object")
	}
	if event.SchemaVersion != SchemaVersion || !validEventName(event.Event) {
		return Event{}, errors.New("guest assignment event is invalid")
	}
	if event.AssignmentID != "" && !safeID.MatchString(event.AssignmentID) {
		return Event{}, errors.New("guest assignment event has an invalid assignment ID")
	}
	return event, nil
}

// ProbeTemplate confirms that the guest assignment listener reached its
// snapshot-safe readiness barrier.
func ProbeTemplate(ctx context.Context, socketPath string) error {
	connection, err := DialHost(ctx, socketPath, VSockPort)
	if err != nil {
		return err
	}
	defer connection.Close()
	event, err := readEventWithin(ctx, connection)
	if err != nil {
		return err
	}
	if event.Event != EventTemplateReady || event.AssignmentID != "" || event.Error != "" {
		return fmt.Errorf("unexpected template readiness event: %s", event.Event)
	}
	// Wait for the guest to close the probe before the builder pauses it.
	// Otherwise the snapshot can retain a live probe connection that collides
	// with the first host connection after restore.
	if err := connection.(*bufferedConnection).CloseWrite(); err != nil {
		return fmt.Errorf("finish template readiness probe: %w", err)
	}
	deadline := time.Now().Add(hostHandshakeTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok {
		deadline = contextDeadline
	}
	if err := connection.SetReadDeadline(deadline); err != nil {
		return fmt.Errorf("set template probe close deadline: %w", err)
	}
	var buffer [1]byte
	if n, err := connection.Read(buffer[:]); n != 0 || !errors.Is(err, io.EOF) {
		return fmt.Errorf("wait for template probe close: %w", errors.Join(err, errors.New("expected guest EOF")))
	}
	return nil
}

// readEventWithin bounds one event read by ctx. dialHost clears the handshake
// deadline so a returned connection can outlive the context that dialed it,
// which leaves every later read unbounded unless it sets its own: a guest that
// accepts the vsock connection but never writes would otherwise block forever.
func readEventWithin(ctx context.Context, connection net.Conn) (Event, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(hostHandshakeTimeout)
	}
	if err := connection.SetReadDeadline(deadline); err != nil {
		return Event{}, fmt.Errorf("set guest assignment read deadline: %w", err)
	}
	event, err := ReadEvent(connection)
	if clearErr := connection.SetReadDeadline(time.Time{}); err == nil && clearErr != nil {
		return Event{}, fmt.Errorf("clear guest assignment read deadline: %w", clearErr)
	}
	return event, err
}

// Assign sends one request-specific assignment to a restored guest and waits
// until the assigned workload reports that it has started.
func Assign(
	ctx context.Context,
	socketPath string,
	assignment Assignment,
	milestone Milestone,
) error {
	return assign(ctx, socketPath, assignment, milestone, DialHost)
}

func assign(
	ctx context.Context,
	socketPath string,
	assignment Assignment,
	milestone Milestone,
	dial assignmentDialFunc,
) error {
	if err := assignment.Validate(); err != nil {
		return err
	}
	if milestone == nil {
		milestone = func(string) error { return nil }
	}
	finishDial := warmuptrace.Start(ctx, "guest.assignment.connect_vsock")
	connection, err := waitForAssignmentReady(ctx, socketPath, dial)
	finishDial()
	if err != nil {
		return err
	}
	defer connection.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return fmt.Errorf("set assignment deadline: %w", err)
		}
	}
	document, err := json.Marshal(assignment)
	if err != nil {
		return fmt.Errorf("encode guest assignment: %w", err)
	}
	document = append(document, '\n')
	finishWrite := warmuptrace.Start(ctx, "guest.assignment.send")
	_, err = connection.Write(document)
	finishWrite()
	if err != nil {
		return fmt.Errorf("send guest assignment: %w", err)
	}

	expected := []string{
		EventAssignmentAccepted,
		EventClockSynchronized,
		EventNetworkConfigured,
		EventContainerMounted,
	}
	if assignment.Workspace.Enabled {
		expected = append(expected, EventWorkspaceMounted)
	}
	expected = append(expected, EventWorkloadStarted)
	if _, sdkWorkload, err := guestconfig.SDKListenerPort(assignment.Workload.Process.Env); err != nil {
		return fmt.Errorf("resolve SDK listener readiness: %w", err)
	} else if sdkWorkload {
		expected = append(expected, EventSDKListenerReady)
	}
	for _, expectedEvent := range expected {
		finishEvent := warmuptrace.Start(ctx, "guest.assignment.wait_"+expectedEvent)
		event, err := ReadEvent(connection)
		finishEvent()
		if err != nil {
			return err
		}
		if event.AssignmentID != assignment.AssignmentID {
			return fmt.Errorf(
				"guest assignment event has unexpected assignment ID %q",
				event.AssignmentID,
			)
		}
		if event.Event == EventAssignmentFailed {
			if event.Error == "" {
				event.Error = "guest rejected assignment without an error"
			}
			return fmt.Errorf("guest assignment failed: %s", event.Error)
		}
		if event.Event != expectedEvent || event.Error != "" {
			return fmt.Errorf(
				"unexpected guest assignment event %q, want %q",
				event.Event,
				expectedEvent,
			)
		}
		if err := milestone(event.Event); err != nil {
			return fmt.Errorf("record guest assignment milestone %s: %w", event.Event, err)
		}
	}
	return nil
}

func waitForAssignmentReady(
	ctx context.Context,
	socketPath string,
	dial assignmentDialFunc,
) (net.Conn, error) {
	ticker := time.NewTicker(readyRetryInterval)
	defer ticker.Stop()
	var lastErr error
	for {
		attemptCtx, cancel := context.WithTimeout(ctx, readyAttemptTimeout)
		connection, err := dial(attemptCtx, socketPath, VSockPort)
		if err == nil {
			event, readErr := readEventWithin(attemptCtx, connection)
			if readErr == nil &&
				event.Event == EventTemplateReady &&
				event.AssignmentID == "" &&
				event.Error == "" {
				cancel()
				return connection, nil
			}
			if readErr != nil {
				err = readErr
			} else {
				err = fmt.Errorf("unexpected initial assignment event %q", event.Event)
			}
			_ = connection.Close()
		}
		cancel()
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf(
				"wait for restored guest assignment listener: %w",
				errors.Join(ctx.Err(), lastErr),
			)
		case <-ticker.C:
		}
	}
}

// readEventLine returns one newline-terminated event.
//
// The guest writes each event with a separate write, so several of them can
// arrive in one packet. Only a reader that outlives the call keeps the extras
// queued: a per-call reader would buffer them and then discard them, and the
// next read would block waiting for an event that has already been consumed.
// Connections from dialHost carry such a reader, so they are read through it.
func readEventLine(reader io.Reader) ([]byte, error) {
	if buffered, ok := reader.(eventLineReader); ok {
		return buffered.readEventLine()
	}
	line, err := bufio.NewReaderSize(
		io.LimitReader(reader, maxEventBytes+1),
		maxEventBytes+1,
	).ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("read guest assignment event: %w", err)
	}
	if len(line) > maxEventBytes {
		return nil, errors.New("guest assignment event exceeds limit")
	}
	return line, nil
}

type bufferedConnection struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConnection) Read(buffer []byte) (int, error) {
	return c.reader.Read(buffer)
}

func (c *bufferedConnection) CloseWrite() error {
	connection, ok := c.Conn.(interface{ CloseWrite() error })
	if !ok {
		return errors.New("vsock transport does not support closing writes")
	}
	return connection.CloseWrite()
}

// readEventLine bounds the event by the reader's buffer instead of a
// LimitReader, so bytes past this event stay buffered for the next call.
func (c *bufferedConnection) readEventLine() ([]byte, error) {
	line, err := c.reader.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return nil, errors.New("guest assignment event exceeds limit")
	}
	if err != nil {
		return nil, fmt.Errorf("read guest assignment event: %w", err)
	}
	if len(line) > maxEventBytes {
		return nil, errors.New("guest assignment event exceeds limit")
	}
	return line, nil
}

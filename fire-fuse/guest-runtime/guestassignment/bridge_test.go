package guestassignment

import (
	"context"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestSDKBridgeQueuesRequestUntilWorkloadListens(t *testing.T) {
	targetReservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	targetPort := targetReservation.Addr().(*net.TCPAddr).Port
	if err := targetReservation.Close(); err != nil {
		t.Fatal(err)
	}

	bridge, err := startSDKBridge(context.Background(), "127.0.0.1", 0, targetPort)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close() })
	runtimeBridge := bridge.(*runtimeSDKBridge)
	client, err := net.Dial("tcp4", runtimeBridge.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Write([]byte("request")); err != nil {
		t.Fatal(err)
	}

	// The edge connection and its request arrive before Bun owns its loopback
	// port. Starting the workload listener later must still deliver every byte.
	time.Sleep(25 * time.Millisecond)
	target, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(targetPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	serverDone := make(chan error, 1)
	go func() {
		connection, err := target.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer connection.Close()
		request := make([]byte, len("request"))
		if _, err := io.ReadFull(connection, request); err != nil {
			serverDone <- err
			return
		}
		if string(request) != "request" {
			serverDone <- &unexpectedBridgePayload{got: string(request)}
			return
		}
		_, err = connection.Write([]byte("response"))
		serverDone <- err
	}()

	if err := client.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len("response"))
	if _, err := io.ReadFull(client, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != "response" {
		t.Fatalf("response = %q", response)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

type unexpectedBridgePayload struct{ got string }

func (e *unexpectedBridgePayload) Error() string { return "unexpected bridge payload: " + e.got }

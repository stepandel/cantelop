package guestassignment

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTemplateProbeWaitsForGuestClose(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout bool
	}{{name: "guest closes"}, {name: "guest close times out", timeout: true}} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := os.MkdirTemp("/tmp", "probe-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(root)
			socket := filepath.Join(root, "vsock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			halfClosed := make(chan struct{})
			release := make(chan struct{})
			defer close(release)
			peerErrors := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					peerErrors <- err
					return
				}
				defer conn.Close()
				line, err := bufio.NewReader(conn).ReadString('\n')
				if err != nil || line != "CONNECT 19092\n" {
					peerErrors <- errors.New("invalid probe handshake")
					return
				}
				if _, err = io.WriteString(conn, "OK 1234\n"); err != nil {
					peerErrors <- err
					return
				}
				if err = writeEvent(conn, Event{SchemaVersion: SchemaVersion, Event: EventTemplateReady}); err != nil {
					peerErrors <- err
					return
				}
				var b [1]byte
				if n, err := conn.Read(b[:]); n != 0 || !errors.Is(err, io.EOF) {
					peerErrors <- errors.New("probe did not close writes")
					return
				}
				close(halfClosed)
				<-release
				peerErrors <- nil
			}()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- ProbeTemplate(ctx, socket) }()
			select {
			case <-halfClosed:
			case err := <-peerErrors:
				t.Fatalf("guest handshake: %v", err)
			case <-ctx.Done():
				t.Fatal("probe never closed writes")
			}
			select {
			case err := <-result:
				t.Fatalf("probe returned before guest closed: %v", err)
			default:
			}
			if tc.timeout {
				cancel()
				// A socket deadline bounds the EOF wait even when the peer stays open.
				select {
				case err := <-result:
					if err == nil || !strings.Contains(err.Error(), "probe close") {
						t.Fatalf("error = %v", err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("probe close wait was unbounded")
				}
			} else {
				release <- struct{}{}
				if err := <-result; err != nil {
					t.Fatal(err)
				}
				if err := <-peerErrors; err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

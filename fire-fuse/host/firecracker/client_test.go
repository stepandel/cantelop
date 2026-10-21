package firecracker

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestClientPut(t *testing.T) {
	var gotMethod string
	var gotPath string
	var gotContentType string
	var gotBody map[string]any
	socketPath := serveUnixHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("Decode() error = %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	client, err := NewClient(socketPath)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(client.CloseIdleConnections)

	err = client.Put(context.Background(), "/machine-config", map[string]any{
		"vcpu_count":   2,
		"mem_size_mib": 512,
	})
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/machine-config" {
		t.Errorf("path = %q, want /machine-config", gotPath)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotBody["vcpu_count"] != float64(2) || gotBody["mem_size_mib"] != float64(512) {
		t.Errorf("body = %#v, want machine configuration", gotBody)
	}
}

func TestClientPatch(t *testing.T) {
	var method string
	socketPath := serveUnixHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		w.WriteHeader(http.StatusNoContent)
	}))
	client, err := NewClient(socketPath)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(client.CloseIdleConnections)

	if err := client.Patch(context.Background(), "/vm", map[string]string{"state": "Paused"}); err != nil {
		t.Fatalf("Patch() error = %v", err)
	}
	if method != http.MethodPatch {
		t.Fatalf("method = %q, want PATCH", method)
	}
}

func TestClientPutReturnsAPIError(t *testing.T) {
	socketPath := serveUnixHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"fault_message":"invalid machine config"}`))
	}))
	client, err := NewClient(socketPath)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(client.CloseIdleConnections)

	err = client.Put(context.Background(), "/machine-config", struct{}{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("Put() error = %v, want APIError", err)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, http.StatusBadRequest)
	}
	if apiErr.Body != `{"fault_message":"invalid machine config"}` {
		t.Errorf("Body = %q", apiErr.Body)
	}
}

func TestNewClientRejectsRelativeSocketPath(t *testing.T) {
	if _, err := NewClient("run/firecracker.sock"); !errors.Is(err, ErrInvalidSocketPath) {
		t.Fatalf("NewClient() error = %v, want ErrInvalidSocketPath", err)
	}
}

func TestClientPutRejectsInvalidEndpoint(t *testing.T) {
	client, err := NewClient("/tmp/firecracker.sock")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	if err := client.Put(context.Background(), "machine-config", struct{}{}); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("Put() error = %v, want ErrInvalidEndpoint", err)
	}
}

func TestClientPutHonorsCanceledContext(t *testing.T) {
	client, err := NewClient("/tmp/fire-fuse-missing.sock")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := client.Put(ctx, "/actions", struct{}{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put() error = %v, want context.Canceled", err)
	}
}

func serveUnixHTTP(t *testing.T, handler http.Handler) string {
	t.Helper()

	tempDir, err := os.MkdirTemp("/tmp", "fire-fuse-test-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })
	socketPath := filepath.Join(tempDir, "firecracker.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	server := &http.Server{Handler: handler}
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-serveResult; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("Serve() error = %v", err)
		}
	})

	return socketPath
}

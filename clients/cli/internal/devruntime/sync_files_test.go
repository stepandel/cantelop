package devruntime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNativeSyncHelperPrivateHandshakeAndParentLifetime(t *testing.T) {
	input, parent := io.Pipe()
	output, childOutput := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunSyncFiles(context.Background(), []string{"--directory", t.TempDir(), "--workspace", "wsp_11111111111111111111111111111111", "--state-directory", t.TempDir()}, input, childOutput)
		childOutput.Close()
	}()
	defer parent.Close()
	defer output.Close()
	var hello struct {
		ProtocolVersion int    `json:"protocolVersion"`
		Origin          string `json:"origin"`
		Token           string `json:"token"`
	}
	if err := json.NewDecoder(output).Decode(&hello); err != nil {
		t.Fatal(err)
	}
	if hello.ProtocolVersion != 1 || !strings.HasPrefix(hello.Origin, "http://127.0.0.1:") || len(hello.Token) != 64 {
		t.Fatal("invalid private handshake")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(hello.Origin + "/files?op=scan")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("unauthenticated local filesystem access")
	}
	r, _ := http.NewRequest("GET", hello.Origin+"/files?op=scan", nil)
	r.Header.Set("Authorization", "Bearer "+hello.Token)
	response, err = client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	client.CloseIdleConnections()
	parent.Close()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("helper survived its parent pipe")
	}
}

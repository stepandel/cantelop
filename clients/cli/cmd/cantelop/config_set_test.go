package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/clients/cli/internal/controlapi"
	"github.com/stepandel/cantelop/clients/cli/internal/projectconfig"
)

func TestBulkConfigSet(t *testing.T) {
	const appID = "app_0123456789abcdef0123456789abcdef"
	for _, kind := range []string{"env", "secret"} {
		t.Run(kind, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), ".env")
			if err := os.WriteFile(file, []byte("# production\nB='sensitive=value'\nexport A=\n"), 0600); err != nil {
				t.Fatal(err)
			}
			var names []string
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
				names = append(names, name)
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				expected := map[string]string{"A": "", "B": "sensitive=value"}[name]
				if body["value"] != expected {
					t.Fatalf("unexpected value for %s", name)
				}
				responseKind, segment := "variable", "variables"
				if kind == "secret" {
					responseKind, segment = "secret", "secrets"
				}
				if r.Method != http.MethodPut || r.URL.Path != "/v1/apps/"+appID+"/config/"+segment+"/"+name {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"name":"` + name + `","kind":"` + responseKind + `"}`))}, nil
			})}
			session := &controlapi.Session{Client: client, Credentials: controlapi.Credentials{ControlURL: "http://127.0.0.1:12345"}, Token: controlapi.TokenResponse{AccessToken: "token"}, ExpiresAt: time.Now().Add(time.Hour)}
			var out bytes.Buffer
			if err := runConfigCommand(context.Background(), []string{kind, "set", appID, "--env-file", file}, nil, &out, session); err != nil {
				t.Fatal(err)
			}
			if strings.Join(names, ",") != "A,B" || out.String() != "Set A\nSet B\n" {
				t.Fatalf("unexpected result: %v %q", names, out.String())
			}
			if kind == "env" {
				if err := runConfigCommand(context.Background(), []string{kind, "set", appID, "B=sensitive=value", "A="}, nil, &out, session); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestBulkConfigValidatesBeforeMutation(t *testing.T) {
	for _, args := range [][]string{
		{"GOOD=ok", "INVALID"}, {"GOOD=ok", "GOOD=again"}, {"GOOD=ok", "CANTELOP_RESERVED=x"}, {"GOOD=ok", "LARGE=" + strings.Repeat("x", projectconfig.MaxConfigInputBytes+1)}, {},
	} {
		// A nil session ensures invalid input cannot reach the network.
		if err := runConfigSet(context.Background(), "env", args, "", nil, io.Discard, nil); err == nil {
			t.Fatal("accepted invalid batch")
		}
	}
	file := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(file, []byte("GOOD=ok\nBAD LINE\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runConfigSet(context.Background(), "secret", []string{"--env-file", file}, "", nil, io.Discard, nil); err == nil {
		t.Fatal("accepted invalid file")
	}
}

func TestBulkConfigStopsOnFailure(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		status, body := 200, `{"name":"A","kind":"variable"}`
		if calls == 2 {
			status, body = 409, `{"error":"conflict"}`
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	session := &controlapi.Session{Client: client, Credentials: controlapi.Credentials{ControlURL: "http://127.0.0.1:12345"}, Token: controlapi.TokenResponse{AccessToken: "token"}, ExpiresAt: time.Now().Add(time.Hour)}
	var out bytes.Buffer
	err := runConfigSet(context.Background(), "env", []string{"C=3", "B=2", "A=1"}, "/config/variables", nil, &out, session)
	if err == nil || !strings.Contains(err.Error(), "B after 1 of 3 confirmed updates") || calls != 2 || out.String() != "Set A\n" {
		t.Fatalf("calls=%d output=%q err=%v", calls, out.String(), err)
	}
}

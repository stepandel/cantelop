package messagedelivery

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"github.com/stepandel/cantelop/fire-fuse/runtime/session"
)

type mailboxRoundTripFunc func(*http.Request) (*http.Response, error)

func (f mailboxRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestAdmissionKeepsNormalEnvelopeCompatibleWithOlderActors(t *testing.T) {
	for _, priority := range []string{"dispatch", "steer", "cancel"} {
		t.Run(priority, func(t *testing.T) {
			at := time.Now().UTC()
			deadline := at.Add(time.Minute)
			sandboxID := "sbx-0123456789abcdef0123456789abcdef"
			messageID := "msg_0123456789abcdef0123456789abcdef"
			client := NewMailboxClient(mailboxRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				var envelope map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
					t.Fatal(err)
				}
				if _, present := envelope["priority"]; present {
					t.Fatal("transport interpreted an SDK command")
				}

				body, _ := json.Marshal(messageprotocol.Receipt{SandboxID: sandboxID, MessageID: messageID, Sequence: 1, Generation: 1, AcceptedAt: at, Deadline: deadline, State: "queued"})
				headers := http.Header{}
				headers.Set(messageprotocol.SandboxHeader, sandboxID)
				headers.Set(messageprotocol.ProtocolHeader, "2")
				return &http.Response{StatusCode: 202, Header: headers, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			}))
			route := session.Route{SandboxID: sandboxID, Endpoint: session.Endpoint{Scheme: "http", Authority: "sandbox.invalid"}}
			_, err := client.Admit(context.Background(), route, messageprotocol.Request{Session: messageprotocol.SessionEnvelope{ID: "thread", WorkspaceID: "wsp_0123456789abcdef0123456789abcdef", KeepAliveSeconds: 900}, Message: messageprotocol.Message{ID: messageID, Payload: json.RawMessage(`{"__cantelop_command":2,"type":"` + priority + `"}`)}}, deadline)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCompletionWaitValidatesGenerationAndRuntimeIdentity(t *testing.T) {
	sandboxID := "sbx-0123456789abcdef0123456789abcdef"
	for _, test := range []struct {
		name, identity, body string
		valid                bool
	}{
		{"completion", sandboxID, `{"generation":2}`, true},
		{"unchanged", sandboxID, `{"generation":1}`, false},
		{"backwards", sandboxID, `{"generation":0}`, false},
		{"wrong Sandbox", "sbx-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", `{"generation":2}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := NewMailboxClient(mailboxRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != messageprotocol.RuntimePath+"/completions" || r.URL.Query().Get("after") != "1" {
					t.Fatal(r.URL)
				}
				header := http.Header{}
				header.Set(messageprotocol.SandboxHeader, test.identity)
				header.Set(messageprotocol.ProtocolHeader, "2")
				return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(test.body))}, nil
			}))
			route := session.Route{SandboxID: sandboxID, Endpoint: session.Endpoint{Scheme: "http", Authority: "sandbox.invalid"}}
			generation, err := client.WaitForCompletions(context.Background(), route, 1)
			if test.valid && (err != nil || generation != 2) {
				t.Fatal(generation, err)
			}
			if !test.valid && err == nil {
				t.Fatal("invalid notification accepted")
			}
		})
	}
}

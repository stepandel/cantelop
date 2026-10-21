package controlclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWaitForMessageDeliveries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/message-delivery-notifications" || r.URL.Query().Get("after") != "42" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"generation":43}`))
	}))
	defer server.Close()
	client, err := New(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	generation, err := client.WaitForMessageDeliveries(context.Background(), 42)
	if err != nil || generation != 43 {
		t.Fatalf("generation=%d err=%v", generation, err)
	}
}

func TestWaitForMessageReply(t *testing.T) {
	id := "msg_0123456789abcdef0123456789abcdef"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/message-deliveries/"+id+"/wait" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"` + id + `","state":"handled","reply":"ready"}`))
	}))
	defer server.Close()
	client, err := New(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	reply, err := client.WaitForMessageReply(context.Background(), id)
	if err != nil || reply.State != "handled" || string(reply.Reply) != `"ready"` {
		t.Fatalf("reply=%+v err=%v", reply, err)
	}
}

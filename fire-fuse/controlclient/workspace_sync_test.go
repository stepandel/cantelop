package controlclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stepandel/cantelop/fire-fuse/controlauth"
)

func TestSyncTransportDoesNotForwardCallerCredentials(t *testing.T) {
	app := "app_" + strings.Repeat("1", 32)
	workspace := "wsp_" + strings.Repeat("2", 32)
	token := strings.Repeat("a", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/apps/"+app+"/workspace-sync/"+workspace || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("Cookie") != "" || r.Header.Get("X-Cantelop-Edge-App-ID") != "" {
			t.Error("authority leaked or wrong private target")
		}
		data, _ := io.ReadAll(r.Body)
		if string(data) != "binary" {
			t.Error("body altered")
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	httpClient, _ := controlauth.AuthenticateClient(server.Client(), token)
	client, err := New(server.URL, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("PUT", "http://edge/sync?op=write&path=a&base=missing", strings.NewReader("binary"))
	r.Header.Set("Authorization", "Bearer user")
	r.Header.Set("Cookie", "secret")
	r.Header.Set("X-Cantelop-Edge-App-ID", "forged")
	response, err := client.SyncWorkspace(context.Background(), app, workspace, r)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatal(response.StatusCode)
	}
}

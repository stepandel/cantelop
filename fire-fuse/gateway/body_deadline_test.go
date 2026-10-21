package gateway

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEdgeJSONBodyDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Value string `json:"value"`
		}
		if err := decodeEdgeJSONWithin(w, r, &input, 1024, 100*time.Millisecond); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if input.Value != "ok" {
			t.Errorf("input = %#v", input)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	for _, framing := range []string{"Content-Length: 100\r\n\r\n{", "Transfer-Encoding: chunked\r\n\r\n64\r\n{"} {
		conn, err := net.Dial("tcp", server.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, err = fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: localhost\r\n%s", framing)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatalf("slow body was not rejected within deadline: %v", err)
		}
		if response.StatusCode != http.StatusBadRequest {
			t.Fatal(response.Status)
		}
		_ = response.Body.Close()
		_ = conn.Close()
	}
	response, err := server.Client().Post(server.URL, "application/json", strings.NewReader(`{"value":"ok"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatal(response.Status)
	}
}

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (w *deadlineRecorder) SetReadDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return nil
}

func TestEdgeJSONClearsDeadlineOnlyAfterCompleteBody(t *testing.T) {
	for _, body := range []string{`{"ok":true}`, `{"unknown":true}`, strings.Repeat("x", 33)} {
		w := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		var input struct {
			OK bool `json:"ok"`
		}
		_ = decodeEdgeJSON(w, r, &input, 32)
		if len(w.deadlines) == 0 || w.deadlines[0].IsZero() {
			t.Fatal("missing body deadline")
		}
		if len(body) <= 32 {
			if len(w.deadlines) != 2 || !w.deadlines[1].IsZero() {
				t.Fatal("completed body retained deadline")
			}
		} else if len(w.deadlines) != 1 {
			t.Fatal("partial body lost deadline")
		}
	}
}

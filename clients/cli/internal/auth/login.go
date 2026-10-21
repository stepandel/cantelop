package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

type loginResult struct {
	code string
	err  error
}

func BrowserLogin(ctx context.Context, consoleURL string, output io.Writer) (string, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("start login callback: %w", err)
	}
	defer listener.Close()
	stateBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", fmt.Errorf("create login state: %w", err)
	}
	state := base64.RawURLEncoding.EncodeToString(stateBytes)
	callback := "http://" + listener.Addr().String() + "/callback"
	authorize, _ := url.Parse(consoleURL + "/api/cli/authorize")
	query := authorize.Query()
	query.Set("callback", callback)
	query.Set("state", state)
	authorize.RawQuery = query.Encode()
	_, _ = fmt.Fprintf(output, "Open this URL to log in:\n%s\n", authorize.String())

	result := make(chan loginResult, 1)
	server := &http.Server{Handler: loginCallbackHandler(state, result), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			select {
			case result <- loginResult{err: serveErr}:
			default:
			}
		}
	}()
	waitContext, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	select {
	case completed := <-result:
		_ = server.Close()
		if completed.err != nil {
			return "", completed.err
		}
		if len(completed.code) > 8192 || !loginCodePattern.MatchString(completed.code) {
			return "", errors.New("console returned an invalid CLI credential")
		}
		return completed.code, nil
	case <-waitContext.Done():
		_ = server.Close()
		return "", errors.New("login timed out")
	}
}

func loginCallbackHandler(state string, result chan<- loginResult) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<!doctype html><meta charset="utf-8"><title>Cantelop login</title>
<p id="status">Completing Cantelop login…</p><script>
const values = new URLSearchParams(location.hash.slice(1));
history.replaceState(null, "", "/callback");
fetch("/callback", {method: "POST", headers: {"Content-Type": "application/x-www-form-urlencoded"}, body: values})
  .then(async response => { document.getElementById("status").textContent = await response.text(); })
  .catch(() => { document.getElementById("status").textContent = "Cantelop login could not be completed."; });
</script>`)
			return
		}
		request.Body = http.MaxBytesReader(w, request.Body, 16<<10)
		if request.Method != http.MethodPost || request.ParseForm() != nil || request.Form.Get("state") != state || request.Form.Get("code") == "" {
			http.Error(w, "Login could not be completed.", http.StatusBadRequest)
			return
		}
		select {
		case result <- loginResult{code: request.Form.Get("code")}:
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = io.WriteString(w, "Cantelop login complete. You can close this window.\n")
		default:
			http.Error(w, "Login was already completed.", http.StatusConflict)
		}
	})
	return mux
}

var (
	loginCodePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

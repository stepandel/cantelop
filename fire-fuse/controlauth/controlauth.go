package controlauth

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var tokenPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func LoadTokenFile(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("control-plane token file must be a clean absolute path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect control-plane token file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o027 != 0 {
		return "", errors.New("control-plane token file must be a regular file without group-write or other permissions")
	}
	document, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read control-plane token file: %w", err)
	}
	token := strings.TrimSpace(string(document))
	if !tokenPattern.MatchString(token) {
		return "", errors.New("control-plane token must contain exactly 64 lowercase hexadecimal characters")
	}
	return token, nil
}

func Require(token string, next http.Handler) (http.Handler, error) {
	if !tokenPattern.MatchString(token) || next == nil {
		return nil, errors.New("valid control-plane token and handler are required")
	}
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		got := []byte(r.Header.Get("Authorization"))
		if len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "control-plane authentication required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	}), nil
}

type Transport struct {
	Token string
	Base  http.RoundTripper
}

// Scope is an immutable method-and-path capability compiled with the same
// routing grammar as net/http. Query strings never affect authorization.
type Scope struct {
	mux    *http.ServeMux
	denied map[string]bool
}

// NewScope constructs a least-privilege route set. Patterns use net/http's
// method-aware ServeMux syntax, for example "GET /v1/workspaces/{id}". A
// pattern prefixed with "!" denies a more specific route that a wildcard
// would otherwise grant, for example "!GET /v1/workspaces/reserved".
func NewScope(patterns ...string) (_ Scope, err error) {
	if len(patterns) == 0 {
		return Scope{}, errors.New("at least one control-plane route is required")
	}
	mux := http.NewServeMux()
	denied := make(map[string]bool)
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("invalid control-plane route scope: %v", recovered)
		}
	}()
	for _, pattern := range patterns {
		if denial, ok := strings.CutPrefix(pattern, "!"); ok {
			pattern = denial
			denied[pattern] = true
		}
		mux.Handle(pattern, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	}
	return Scope{mux: mux, denied: denied}, nil
}

func (s Scope) allows(request *http.Request) bool {
	if s.mux == nil || request == nil {
		return false
	}
	_, pattern := s.mux.Handler(request)
	return pattern != "" && !s.denied[pattern]
}

// Credential grants one bearer token only the routes in Scope.
type Credential struct {
	Token string
	Scope Scope
}

// ValidateDistinctTokens rejects malformed or aliased credentials before any
// listener or client can combine otherwise separate authority boundaries.
func ValidateDistinctTokens(tokens ...string) error {
	if len(tokens) == 0 {
		return errors.New("at least one control-plane credential is required")
	}
	seen := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		if !tokenPattern.MatchString(token) {
			return errors.New("valid control-plane credentials are required")
		}
		if _, exists := seen[token]; exists {
			return errors.New("control-plane credentials must be distinct")
		}
		seen[token] = struct{}{}
	}
	return nil
}

// RequireScoped authenticates distinct credentials and enforces their route
// capabilities before the shared runtime handler can observe the request.
func RequireScoped(credentials []Credential, next http.Handler) (http.Handler, error) {
	if len(credentials) == 0 || next == nil {
		return nil, errors.New("control-plane credentials and handler are required")
	}
	tokens := make([]string, 0, len(credentials))
	for _, credential := range credentials {
		if credential.Scope.mux == nil {
			return nil, errors.New("valid control-plane credentials and scopes are required")
		}
		tokens = append(tokens, credential.Token)
	}
	if err := ValidateDistinctTokens(tokens...); err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		values := r.Header.Values("Authorization")
		if len(values) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "control-plane authentication required", http.StatusUnauthorized)
			return
		}
		presented := []byte(values[0])
		for _, credential := range credentials {
			want := []byte("Bearer " + credential.Token)
			if len(presented) == len(want) && subtle.ConstantTimeCompare(presented, want) == 1 {
				if !credential.Scope.allows(r) {
					http.Error(w, "control-plane credential does not permit this operation", http.StatusForbidden)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "control-plane authentication required", http.StatusUnauthorized)
	}), nil
}

func (t Transport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || !tokenPattern.MatchString(t.Token) {
		return nil, errors.New("valid control-plane request and token are required")
	}
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+t.Token)
	return base.RoundTrip(clone)
}

func AuthenticateClient(client *http.Client, token string) (*http.Client, error) {
	if !tokenPattern.MatchString(token) {
		return nil, errors.New("valid control-plane token is required")
	}
	if client == nil {
		client = &http.Client{}
	}
	clone := *client
	clone.Transport = Transport{Token: token, Base: client.Transport}
	return &clone, nil
}

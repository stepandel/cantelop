package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

type fakeRuntimeWorkspaceStore struct {
	workspace      []string
	sessionRuntime microvm.AppSessionRuntime
}

func (s fakeRuntimeWorkspaceStore) ResolveAppConfig(appID string) ([]string, error) {
	if appID != "app_test" {
		return nil, errors.New("unexpected identity")
	}
	return append([]string(nil), s.workspace...), nil
}

func (s fakeRuntimeWorkspaceStore) GetAppSessionRuntime(appID string) (microvm.AppSessionRuntime, error) {
	if appID != "app_test" {
		return microvm.AppSessionRuntime{}, errors.New("unexpected App")
	}
	return s.sessionRuntime, nil
}

func TestResolveRuntimeWorkspaceInjectsAppSessionRuntimePort(t *testing.T) {
	store := fakeRuntimeWorkspaceStore{
		workspace: []string{"MODEL=test"},
		sessionRuntime: microvm.AppSessionRuntime{
			ActiveBuildID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Runtime: microvm.AppRuntimeSpec{
				Profile: microvm.DefaultSandboxProfileName, InternalPort: 3456,
			},
		},
	}

	got, err := resolveRuntimeWorkspace(store, "app_test")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"MODEL=test", "CANTELOP_INTERNAL_PORT=3456"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveRuntimeWorkspace() = %#v, want %#v", got, want)
	}
}

func TestValidateListenAddressAcceptsLoopbackIPs(t *testing.T) {
	for _, address := range []string{
		"127.0.0.1:8080",
		"127.255.255.254:1",
		"[::1]:8080",
		"[::ffff:127.0.0.1]:8080",
	} {
		t.Run(address, func(t *testing.T) {
			if err := validateListenAddress(address); err != nil {
				t.Errorf("validateListenAddress(%q) error = %v", address, err)
			}
		})
	}
}

func TestValidateListenAddressRejectsRemoteOrAmbiguousEndpoints(t *testing.T) {
	tests := []struct {
		name    string
		address string
	}{
		{name: "IPv4 wildcard", address: "0.0.0.0:8080"},
		{name: "IPv6 wildcard", address: "[::]:8080"},
		{name: "private interface", address: "192.168.1.10:8080"},
		{name: "hostname", address: "localhost:8080"},
		{name: "missing host", address: ":8080"},
		{name: "missing port", address: "127.0.0.1"},
		{name: "zero port", address: "127.0.0.1:0"},
		{name: "service port", address: "127.0.0.1:http"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateListenAddress(test.address); err == nil {
				t.Errorf("validateListenAddress(%q) succeeded, want error", test.address)
			}
		})
	}
}

func Example_validateListenAddress() {
	fmt.Println(validateListenAddress("127.0.0.1:8080"))
	fmt.Println(validateListenAddress("0.0.0.0:8080"))
	// Output:
	// <nil>
	// IP must be a loopback address
}

func TestValidateProviderIngressListenAddress(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8084", "[::1]:8084", "10.77.0.1:8084"} {
		if err := validateProviderIngressListenAddress(address); err != nil {
			t.Errorf("validateProviderIngressListenAddress(%q) error = %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:8084", "10.77.0.2:8084", "192.168.1.10:8084", "localhost:8084", "10.77.0.1:0"} {
		if err := validateProviderIngressListenAddress(address); err == nil {
			t.Errorf("validateProviderIngressListenAddress(%q) succeeded, want error", address)
		}
	}
}

func TestLoadAndCheckHostReturnsValidatedConfiguration(t *testing.T) {
	want := config.Config{RuntimeDir: "/var/lib/fire-fuse/runtime"}
	loadedPath := ""
	checked := false

	got, err := loadAndCheckHost(
		"/etc/fire-fuse/config.json",
		func(path string) (config.Config, error) {
			loadedPath = path
			return want, nil
		},
		func(cfg config.Config) error {
			checked = true
			if cfg.RuntimeDir != want.RuntimeDir {
				t.Fatalf("checked RuntimeDir = %q, want %q", cfg.RuntimeDir, want.RuntimeDir)
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("loadAndCheckHost() error = %v", err)
	}
	if loadedPath != "/etc/fire-fuse/config.json" {
		t.Errorf("loaded path = %q, want /etc/fire-fuse/config.json", loadedPath)
	}
	if !checked {
		t.Fatal("host checker was not called")
	}
	if got.RuntimeDir != want.RuntimeDir {
		t.Errorf("returned RuntimeDir = %q, want %q", got.RuntimeDir, want.RuntimeDir)
	}
}

func TestLoadAndCheckHostStopsAfterLoadFailure(t *testing.T) {
	loadFailure := errors.New("untrusted configuration")
	checked := false

	_, err := loadAndCheckHost(
		"/etc/fire-fuse/config.json",
		func(string) (config.Config, error) {
			return config.Config{}, loadFailure
		},
		func(config.Config) error {
			checked = true
			return nil
		},
	)
	if !errors.Is(err, loadFailure) {
		t.Fatalf("loadAndCheckHost() error = %v, want load failure", err)
	}
	if checked {
		t.Fatal("host checker ran after configuration load failure")
	}
}

func TestLoadAndCheckHostReturnsPreflightFailure(t *testing.T) {
	checkFailure := errors.New("KVM unavailable")

	_, err := loadAndCheckHost(
		"/etc/fire-fuse/config.json",
		func(string) (config.Config, error) {
			return config.Config{}, nil
		},
		func(config.Config) error {
			return checkFailure
		},
	)
	if !errors.Is(err, checkFailure) {
		t.Fatalf("loadAndCheckHost() error = %v, want preflight failure", err)
	}
}

func TestCheckHostWithCurrentCatalogRefreshesImages(t *testing.T) {
	cfg := config.Config{
		ImageCatalog: "/etc/fire-fuse/image-catalog.json",
		Images:       map[string]config.ContainerImage{"stale": {}},
	}
	wantImages := map[string]config.ContainerImage{
		"sha256:current": {RootFSImage: "/var/lib/fire-fuse/images/sha256-current.ext4"},
	}
	loaderCalled := false
	checkerCalled := false

	err := checkHostWithCurrentCatalog(cfg, func(path string) (map[string]config.ContainerImage, error) {
		loaderCalled = true
		if path != cfg.ImageCatalog {
			t.Fatalf("catalog path = %q, want %q", path, cfg.ImageCatalog)
		}
		return wantImages, nil
	}, func(got config.Config) error {
		checkerCalled = true
		if !reflect.DeepEqual(got.Images, wantImages) {
			t.Fatalf("checker images = %#v, want %#v", got.Images, wantImages)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("checkHostWithCurrentCatalog() error = %v", err)
	}
	if !loaderCalled || !checkerCalled {
		t.Fatalf("loader called = %v, checker called = %v; want both", loaderCalled, checkerCalled)
	}
}

func TestCheckHostWithCurrentCatalogStopsOnLoadFailure(t *testing.T) {
	wantErr := errors.New("catalog unavailable")
	checkerCalled := false

	err := checkHostWithCurrentCatalog(config.Config{ImageCatalog: "/catalog.json"}, func(string) (map[string]config.ContainerImage, error) {
		return nil, wantErr
	}, func(config.Config) error {
		checkerCalled = true
		return nil
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("checkHostWithCurrentCatalog() error = %v, want %v", err, wantErr)
	}
	if checkerCalled {
		t.Fatal("checker was called after catalog load failure")
	}
}

func TestNewHTTPServerUsesConfiguredReadDeadlines(t *testing.T) {
	handler := http.NewServeMux()
	server := newHTTPServer("127.0.0.1:8080", handler, config.TimeoutsConfig{
		HTTPRequestRead: config.Duration(11 * time.Second),
		HTTPIdle:        config.Duration(37 * time.Second),
	})

	if server.Addr != "127.0.0.1:8080" || server.Handler != handler {
		t.Fatalf("server address/handler = %q/%T", server.Addr, server.Handler)
	}
	if server.ReadHeaderTimeout != httpReadHeaderTimeout {
		t.Errorf("ReadHeaderTimeout = %s, want %s", server.ReadHeaderTimeout, httpReadHeaderTimeout)
	}
	if server.ReadTimeout != 11*time.Second {
		t.Errorf("ReadTimeout = %s, want 11s", server.ReadTimeout)
	}
	if server.IdleTimeout != 37*time.Second {
		t.Errorf("IdleTimeout = %s, want 37s", server.IdleTimeout)
	}
	if server.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %s, want zero for synchronous VM operations", server.WriteTimeout)
	}
}

type fakeShutdownServer struct {
	shutdown func(context.Context) error
	close    func() error
}

func (f fakeShutdownServer) Shutdown(ctx context.Context) error { return f.shutdown(ctx) }

func (f fakeShutdownServer) Close() error { return f.close() }

type terminateRuntimesFunc func(context.Context) error

func (f terminateRuntimesFunc) TerminateAll(ctx context.Context) error { return f(ctx) }

func TestShutdownServicesUsesIndependentConfiguredDeadlines(t *testing.T) {
	serverFailure := errors.New("server drain failed")
	runtimeFailure := errors.New("runtime cleanup failed")
	timeouts := config.TimeoutsConfig{
		ServerShutdown: config.Duration(2 * time.Second),
		GuestShutdown:  config.Duration(6 * time.Second),
		ShutdownGrace:  config.Duration(3 * time.Second),
		ShutdownForce:  config.Duration(4 * time.Second),
		Cleanup:        config.Duration(5 * time.Second),
	}
	serverCalled := false
	serverClosed := false
	runtimesCalled := false

	serverErr, runtimeErr := shutdownServices(
		fakeShutdownServer{
			shutdown: func(ctx context.Context) error {
				serverCalled = true
				assertDeadlineNear(t, ctx, 2*time.Second)
				return serverFailure
			},
			close: func() error {
				serverClosed = true
				return nil
			},
		},
		terminateRuntimesFunc(func(ctx context.Context) error {
			runtimesCalled = true
			assertDeadlineNear(t, ctx, 18*time.Second)
			return runtimeFailure
		}),
		timeouts,
	)
	if !serverCalled || !serverClosed || !runtimesCalled {
		t.Fatalf(
			"shutdown calls server/close/runtimes = %v/%v/%v, want all",
			serverCalled,
			serverClosed,
			runtimesCalled,
		)
	}
	if !errors.Is(serverErr, serverFailure) || !errors.Is(runtimeErr, runtimeFailure) {
		t.Fatalf("shutdown errors = %v/%v, want independent failures", serverErr, runtimeErr)
	}
}

func TestRuntimeShutdownBudgetSaturatesOnOverflow(t *testing.T) {
	const maximumDuration = time.Duration(1<<63 - 1)
	got := runtimeShutdownBudget(config.TimeoutsConfig{
		GuestShutdown: config.Duration(maximumDuration),
		ShutdownGrace: config.Duration(maximumDuration),
		ShutdownForce: config.Duration(time.Second),
		Cleanup:       config.Duration(time.Second),
	})
	if got != maximumDuration {
		t.Fatalf("runtimeShutdownBudget() = %s, want saturation at %s", got, maximumDuration)
	}
}

func assertDeadlineNear(t *testing.T, ctx context.Context, want time.Duration) {
	t.Helper()
	deadline, exists := ctx.Deadline()
	if !exists {
		t.Fatal("shutdown context has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining < want-time.Second || remaining > want+time.Second {
		t.Fatalf("shutdown deadline remaining = %s, want approximately %s", remaining, want)
	}
}

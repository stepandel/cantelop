package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/controlauth"
	"github.com/stepandel/cantelop/fire-fuse/host/observer"
)

const (
	defaultListenAddress   = "127.0.0.1:8082"
	defaultControlPlaneURL = "http://127.0.0.1:8080"
	readHeaderTimeout      = 5 * time.Second
	idleTimeout            = 60 * time.Second
	shutdownTimeout        = 15 * time.Second
	// Snapshot and Netdata reads fan out over several concurrent loopback
	// connections; keep that many idle between polls instead of the default
	// two, so each poll does not reconnect.
	loopbackIdleConnsPerHost = 16
)

func main() {
	listenAddress := flag.String("listen", defaultListenAddress, "plaintext HTTP listen address for a local TLS proxy")
	controlPlaneURL := flag.String("control-plane", defaultControlPlaneURL, "fire-fuse loopback HTTP origin")
	netdataURL := flag.String("netdata", "http://127.0.0.1:19999", "Netdata loopback HTTP origin")
	controlTimeout := flag.Duration("control-timeout", 10*time.Second, "maximum time to read current state")
	tokenFile := flag.String("token-file", "", "absolute path to the observer bearer token")
	mutationTokenFile := flag.String("mutation-token-file", "", "absolute path to the Session mutation bearer token")
	controlTokenFile := flag.String("control-token-file", "/etc/fire-fuse-observer/runtime-control.token", "private observer runtime bearer token")
	managedControlPlaneURL := flag.String("managed-control-plane", "", "optional managed runtime loopback HTTP origin")
	managedControlTokenFile := flag.String("managed-control-token-file", "", "private managed observer runtime bearer token")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := validateListenAddress(*listenAddress); err != nil || *controlTimeout <= 0 {
		logger.Error("invalid observer configuration", "error", err)
		os.Exit(1)
	}
	token, err := loadToken(*tokenFile)
	if err != nil {
		logger.Error("could not load observer token", "error", err)
		os.Exit(1)
	}
	mutationToken, err := loadToken(*mutationTokenFile)
	if err != nil {
		logger.Error("could not load Session mutation token", "error", err)
		os.Exit(1)
	}
	controlToken, err := controlauth.LoadTokenFile(*controlTokenFile)
	if err != nil {
		logger.Error("could not load control-plane token", "error", err)
		os.Exit(1)
	}
	loopbackTransport := http.DefaultTransport.(*http.Transport).Clone()
	loopbackTransport.MaxIdleConnsPerHost = loopbackIdleConnsPerHost
	controlHTTPClient, err := controlauth.AuthenticateClient(&http.Client{Timeout: *controlTimeout, Transport: loopbackTransport}, controlToken)
	if err != nil {
		logger.Error("could not authenticate control-plane client", "error", err)
		os.Exit(1)
	}
	client, err := observer.NewControlClient(*controlPlaneURL, controlHTTPClient)
	if err != nil {
		logger.Error("invalid control-plane URL", "error", err)
		os.Exit(1)
	}
	netdataClient, err := observer.NewNetdataClient(*netdataURL, &http.Client{Timeout: *controlTimeout, Transport: loopbackTransport})
	if err != nil {
		logger.Error("invalid Netdata URL", "error", err)
		os.Exit(1)
	}
	var managed *observer.ControlClient
	if *managedControlPlaneURL != "" || *managedControlTokenFile != "" {
		managed, err = managedControlClient(*managedControlPlaneURL, *managedControlTokenFile, *controlTimeout, loopbackTransport)
		if err != nil {
			logger.Error("invalid managed observer configuration", "error", err)
			os.Exit(1)
		}
	}
	source, err := observer.NewCompleteSourceWithManagedRuntime(client, managed, netdataClient)
	if err != nil {
		logger.Error("could not construct observer sources", "error", err)
		os.Exit(1)
	}
	handler, err := observer.NewHandlerWithManagedDatabaseAccess(source, source.ManagedDatabaseAccess(), token, mutationToken)
	if err != nil {
		logger.Error("could not construct observer", "error", err)
		os.Exit(1)
	}
	server := &http.Server{
		Addr: *listenAddress, Handler: handler,
		ReadHeaderTimeout: readHeaderTimeout, IdleTimeout: idleTimeout,
	}

	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("observer and Session control listening", "address", server.Addr)
		serverErrors <- server.ListenAndServe()
	}()

	var serveErr error
	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
			logger.Error("observer stopped unexpectedly", "error", err)
		}
	case <-signalContext.Done():
		logger.Info("observer shutdown signal received")
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	shutdownErr := server.Shutdown(shutdownContext)
	cancel()
	if shutdownErr != nil {
		shutdownErr = errors.Join(shutdownErr, server.Close())
		logger.Error("observer shutdown failed", "error", shutdownErr)
	}
	if serveErr != nil || shutdownErr != nil {
		os.Exit(1)
	}
}

func managedControlClient(origin, tokenFile string, timeout time.Duration, transport http.RoundTripper) (*observer.ControlClient, error) {
	token, err := controlauth.LoadTokenFile(tokenFile)
	if err != nil {
		return nil, err
	}
	client, err := controlauth.AuthenticateClient(&http.Client{Timeout: timeout, Transport: transport}, token)
	if err != nil {
		return nil, err
	}
	return observer.NewControlClient(origin, client)
}

func validateListenAddress(address string) error {
	endpoint, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("parse numeric IP and port: %w", err)
	}
	if endpoint.Port() == 0 {
		return errors.New("port must be greater than zero")
	}
	if !endpoint.Addr().Unmap().IsLoopback() {
		return errors.New("IP must be loopback; terminate public TLS in the front proxy")
	}
	return nil
}

func loadToken(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("token file must be a clean absolute path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect token file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o027 != 0 {
		return "", errors.New("token file must be regular without group write or other permissions")
	}
	document, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read token file: %w", err)
	}
	token := strings.TrimSuffix(string(document), "\n")
	if len(token) < 32 || len(token) > 512 || strings.TrimSpace(token) != token {
		return "", errors.New("token must contain 32-512 non-whitespace-trimmed bytes")
	}
	return token, nil
}

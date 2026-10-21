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
	"syscall"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/controlauth"
	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/fire-fuse/gateway"
	"github.com/stepandel/cantelop/fire-fuse/sessionbroker"
)

const (
	defaultListenAddress     = "127.0.0.1:8081"
	defaultControlPlaneURL   = "http://127.0.0.1:8080"
	defaultProductControlURL = "https://console.cantelop.dev"
	defaultEdgeTokenPath     = "/etc/fire-fuse-gateway/edge-ingress.token"
	defaultRuntimeTokenPath  = "/etc/fire-fuse-gateway/runtime-control.token"
	defaultProductTokenPath  = "/etc/fire-fuse-gateway/product-control.token"
	readHeaderTimeout        = 5 * time.Second
	idleTimeout              = 60 * time.Second
	shutdownTimeout          = 15 * time.Second
)

func main() {
	listenAddress := flag.String("listen", defaultListenAddress, "plaintext HTTP listen address for a local TLS proxy")
	controlPlaneURL := flag.String("control-plane", defaultControlPlaneURL, "fire-fuse loopback HTTP origin")
	productControlURL := flag.String("product-control", defaultProductControlURL, "Cantelop product-control HTTPS origin")
	controlTimeout := flag.Duration("control-timeout", 45*time.Second, "maximum control-plane operation duration")
	controlTokenFile := flag.String("control-token-file", defaultRuntimeTokenPath, "private runtime control-plane bearer token")
	productTokenFile := flag.String("product-token-file", defaultProductTokenPath, "private console product-control bearer token")
	edgeTokenFile := flag.String("edge-token-file", defaultEdgeTokenPath, "private edge broker bearer token")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := validateListenAddress(*listenAddress); err != nil {
		logger.Error("invalid gateway listen address", "address", *listenAddress, "error", err)
		os.Exit(1)
	}
	if *controlTimeout <= 0 {
		logger.Error("invalid gateway duration", "control_timeout", *controlTimeout)
		os.Exit(1)
	}
	controlToken, err := controlauth.LoadTokenFile(*controlTokenFile)
	if err != nil {
		logger.Error("could not load control-plane token", "error", err)
		os.Exit(1)
	}
	productToken, err := controlauth.LoadTokenFile(*productTokenFile)
	if err != nil {
		logger.Error("could not load product-control token", "error", err)
		os.Exit(1)
	}
	edgeToken, err := controlauth.LoadTokenFile(*edgeTokenFile)
	if err != nil {
		logger.Error("could not load edge ingress token", "error", err)
		os.Exit(1)
	}
	controlHTTPClient, productHTTPClient, err := authenticatedClients(*controlTimeout, controlToken, productToken, edgeToken)
	if err != nil {
		logger.Error("could not authenticate control-plane client", "error", err)
		os.Exit(1)
	}
	control, err := controlclient.New(*controlPlaneURL, controlHTTPClient)
	if err != nil {
		logger.Error("invalid control-plane URL", "url", *controlPlaneURL, "error", err)
		os.Exit(1)
	}
	products, err := gateway.NewProductClient(*productControlURL, productHTTPClient)
	if err != nil {
		logger.Error("invalid product-control URL", "url", *productControlURL, "error", err)
		os.Exit(1)
	}
	sessions, err := sessionbroker.New(control)
	if err != nil {
		logger.Error("could not construct Session broker", "error", err)
		os.Exit(1)
	}
	dependencies := gateway.Dependencies{
		SyncFiles:           control,
		DatabaseCredentials: products,
		Environment:         products,
		Workspaces:          control,
		WorkspaceCreator:    products,
		SessionBroker:       sessions,
		Messages:            control,
		Events:              control,
		EdgeToken:           edgeToken,
	}

	handler, err := gateway.NewHandler(dependencies, logger)
	if err != nil {
		logger.Error("could not construct gateway", "error", err)
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
		logger.Info("activation gateway listening", "address", server.Addr)
		serverErrors <- server.ListenAndServe()
	}()
	var serveErr error
	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
			logger.Error("activation gateway stopped unexpectedly", "error", err)
		}
	case <-signalContext.Done():
		logger.Info("activation gateway shutdown signal received")
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	shutdownErr := server.Shutdown(shutdownContext)
	cancel()
	if shutdownErr != nil {
		shutdownErr = errors.Join(shutdownErr, server.Close())
		logger.Error("activation gateway shutdown failed", "error", shutdownErr)
	}
	if serveErr != nil || shutdownErr != nil {
		os.Exit(1)
	}
}

func authenticatedClients(timeout time.Duration, controlToken, productToken, edgeToken string) (*http.Client, *http.Client, error) {
	if err := controlauth.ValidateDistinctTokens(controlToken, productToken, edgeToken); err != nil {
		return nil, nil, err
	}
	control, err := controlauth.AuthenticateClient(&http.Client{Timeout: timeout}, controlToken)
	if err != nil {
		return nil, nil, err
	}
	product, err := controlauth.AuthenticateClient(&http.Client{Timeout: timeout}, productToken)
	if err != nil {
		return nil, nil, err
	}
	return control, product, nil
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

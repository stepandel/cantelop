package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/controlauth"
	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/fire-fuse/dispatcher"
	"github.com/stepandel/cantelop/fire-fuse/messagedelivery"
	"github.com/stepandel/cantelop/fire-fuse/observability"
	"github.com/stepandel/cantelop/fire-fuse/sandboxhttp"
	"github.com/stepandel/cantelop/fire-fuse/sessionoutput"
)

const (
	defaultControlPlaneURL = "http://127.0.0.1:8080"
	defaultControlToken    = "/etc/fire-fuse-dispatcher/runtime-control.token"
	observationOrigin      = "https://console.cantelop.dev"
	defaultObservationDir  = "/var/lib/fire-fuse-observability"
	defaultObservationMax  = 64 << 20
	defaultObservationAge  = 24 * time.Hour
)

func main() {
	controlPlaneURL := flag.String("control-plane", defaultControlPlaneURL, "fire-fuse loopback HTTP origin")
	controlTimeout := flag.Duration("control-timeout", 45*time.Second, "maximum control-plane operation duration")
	controlTokenFile := flag.String("control-token-file", defaultControlToken, "private control-plane bearer token")
	observationTokenFile := flag.String("observability-token-file", "", "private observation ingestion bearer token")
	observationURL := flag.String("observability-origin", observationOrigin, "HTTPS origin accepting runtime observations")
	observationDirectory := flag.String("observability-spool", defaultObservationDir, "private observation retry spool")
	observationMaxBytes := flag.Int64("observability-spool-max-bytes", defaultObservationMax, "maximum observation retry spool bytes")
	observationMaxAge := flag.Duration("observability-spool-max-age", defaultObservationAge, "maximum observation retry age")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if *controlTimeout <= 0 {
		logger.Error("invalid dispatcher duration", "control_timeout", *controlTimeout)
		os.Exit(1)
	}
	controlToken, err := controlauth.LoadTokenFile(*controlTokenFile)
	if err != nil {
		logger.Error("could not load control-plane token", "error", err)
		os.Exit(1)
	}
	controlHTTPClient, err := controlauth.AuthenticateClient(&http.Client{Timeout: *controlTimeout}, controlToken)
	if err != nil {
		logger.Error("could not authenticate control-plane client", "error", err)
		os.Exit(1)
	}
	control, err := controlclient.New(*controlPlaneURL, controlHTTPClient)
	if err != nil {
		logger.Error("invalid control-plane URL", "url", *controlPlaneURL, "error", err)
		os.Exit(1)
	}
	observationSpool, observationRelay, err := configureObservability(
		*observationTokenFile, *observationDirectory, *observationMaxBytes, *observationMaxAge, *observationURL,
	)
	if err != nil {
		logger.Error("could not configure observation relay", "error", err)
		os.Exit(1)
	}
	// Avoid a non-nil interface containing a nil *Spool when tracing is disabled.
	var lifecycle observability.Sink
	if observationSpool != nil {
		lifecycle = observationSpool
	}
	runtimeTransport := sandboxhttp.NewTransport()
	messageWorker, err := dispatcher.NewExecutionDispatcher(control, messagedelivery.NewMailboxClient(runtimeTransport), sessionoutput.NewCollector(runtimeTransport), observability.RuntimeRecorder(lifecycle, logger), lifecycle, logger)
	if err != nil {
		logger.Error("could not construct execution dispatcher", "error", err)
		os.Exit(1)
	}

	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runContext, cancel := context.WithCancel(signalContext)
	defer cancel()
	relayDone := make(chan error, 1)
	if observationRelay == nil {
		relayDone <- nil
	} else {
		go func() { relayDone <- observationRelay.Run(runContext) }()
	}
	logger.Info("Message dispatcher started")
	runErr := messageWorker.Run(runContext)
	cancel()
	relayErr := <-relayDone
	if runErr != nil {
		logger.Error("dispatcher stopped unexpectedly", "error", runErr)
		os.Exit(1)
	}
	if relayErr != nil {
		logger.Error("observation relay stopped unexpectedly", "error", relayErr)
		os.Exit(1)
	}
	logger.Info("Message dispatcher stopped")
}

func configureObservability(
	tokenFile, directory string,
	maxBytes int64,
	maxAge time.Duration,
	origins ...string,
) (*observability.Spool, *observability.Relay, error) {
	if tokenFile == "" {
		return nil, nil, nil
	}
	token, err := controlauth.LoadTokenFile(tokenFile)
	if err != nil {
		return nil, nil, fmt.Errorf("load observation ingestion token: %w", err)
	}
	spool, err := observability.OpenSpool(observability.SpoolConfig{
		Directory: directory, MaxBytes: maxBytes, MaxAge: maxAge,
	})
	if err != nil {
		return nil, nil, err
	}
	origin := observationOrigin
	if len(origins) > 1 {
		return nil, nil, errors.New("exactly one observability origin is required")
	}
	if len(origins) == 1 {
		origin = origins[0]
	}
	relay, err := observability.NewRelay(origin, token, spool, nil)
	if err != nil {
		return nil, nil, err
	}
	return spool, relay, nil
}

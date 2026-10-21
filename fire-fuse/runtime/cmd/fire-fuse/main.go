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
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/controlauth"
	"github.com/stepandel/cantelop/fire-fuse/host/nfs"
	"github.com/stepandel/cantelop/fire-fuse/host/preflight"
	"github.com/stepandel/cantelop/fire-fuse/runtime/httpapi"
	"github.com/stepandel/cantelop/fire-fuse/runtime/imagecatalog"
	"github.com/stepandel/cantelop/fire-fuse/runtime/managedwatchdog"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
	"github.com/stepandel/cantelop/fire-fuse/runtime/recovery"
	firecrackerruntime "github.com/stepandel/cantelop/fire-fuse/runtime/runtimeprovider/firecracker"
	"github.com/stepandel/cantelop/fire-fuse/runtime/supervisor"
	"github.com/stepandel/cantelop/fire-fuse/workspacesync"
	"github.com/stepandel/cantelop/internal/releaseinfo"
)

const defaultConfigPath = "/etc/fire-fuse/config.json"
const httpReadHeaderTimeout = 5 * time.Second
const (
	defaultOperatorTokenPath   = "/etc/fire-fuse/control-clients/operator.token"
	defaultGatewayTokenPath    = "/etc/fire-fuse/control-clients/gateway.token"
	defaultDispatcherTokenPath = "/etc/fire-fuse/control-clients/dispatcher.token"
	defaultObserverTokenPath   = "/etc/fire-fuse/control-clients/observer.token"
	defaultComputeTokenPath    = "/etc/fire-fuse/control-clients/compute.token"
)
const internalPortWorkspaceName = "CANTELOP_INTERNAL_PORT"

type configLoader func(string) (config.Config, error)
type imageCatalogLoader func(string) (map[string]config.ContainerImage, error)
type hostChecker func(config.Config) error

type options struct {
	syncWrites             bool
	listenAddress          string
	ingressListenAddress   string
	artifactDownloadOrigin string
	imageDirectory         string
	imageCollectionGrace   time.Duration
	configPath             string
	operatorTokenPath      string
	gatewayTokenPath       string
	dispatcherTokenPath    string
	observerTokenPath      string
	computeTokenPath       string
	preflightOnly          bool
	checkStateOnly         bool
	showVersion            bool
}

func main() {
	opts := parseFlags()
	if opts.showVersion {
		fmt.Println(releaseinfo.String("fire-fuse"))
		return
	}
	if opts.preflightOnly && opts.checkStateOnly {
		fmt.Fprintln(os.Stderr, "-preflight-only and -check-state-only are mutually exclusive")
		os.Exit(2)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(opts, logger); err != nil {
		os.Exit(1)
	}
}

func parseFlags() options {
	var opts options
	flag.BoolVar(&opts.syncWrites, "workspace-sync-writes", false, "enable qualified workspace sync mutations (disabled until storage concurrency qualification)")
	flag.StringVar(&opts.listenAddress, "listen", "127.0.0.1:8080", "HTTP listen address")
	flag.StringVar(&opts.ingressListenAddress, "provider-ingress-listen", "127.0.0.1:8084", "private provider ingress HTTP listen address")
	flag.StringVar(
		&opts.artifactDownloadOrigin,
		"artifact-download-origin",
		strings.TrimSpace(os.Getenv("CANTELOP_ARTIFACT_DOWNLOAD_ORIGIN")),
		"allowed HTTPS origin for signed Build artifact downloads",
	)
	flag.StringVar(&opts.imageDirectory, "image-directory", "/var/lib/fire-fuse/images", "immutable workload rootfs directory")
	flag.DurationVar(
		&opts.imageCollectionGrace,
		"image-collection-grace",
		0,
		"collect customer Builds unreferenced for this long (at least 1h); 0 disables automatic collection",
	)
	flag.StringVar(&opts.configPath, "config", defaultConfigPath, "absolute path to trusted host configuration")
	flag.StringVar(&opts.operatorTokenPath, "operator-token-file", defaultOperatorTokenPath, "private unrestricted operator bearer token")
	flag.StringVar(&opts.gatewayTokenPath, "gateway-token-file", defaultGatewayTokenPath, "private gateway control-plane bearer token")
	flag.StringVar(&opts.dispatcherTokenPath, "dispatcher-token-file", defaultDispatcherTokenPath, "private dispatcher control-plane bearer token")
	flag.StringVar(&opts.observerTokenPath, "observer-token-file", defaultObserverTokenPath, "private observer control-plane bearer token")
	flag.StringVar(&opts.computeTokenPath, "compute-token-file", defaultComputeTokenPath, "private console compute-ingress bearer token")
	flag.BoolVar(
		&opts.preflightOnly,
		"preflight-only",
		false,
		"validate trusted configuration and host prerequisites, then exit",
	)
	flag.BoolVar(
		&opts.checkStateOnly,
		"check-state-only",
		false,
		"validate host prerequisites and existing lifecycle state, then exit",
	)
	flag.BoolVar(&opts.showVersion, "version", false, "print release identity and exit")
	flag.Parse()
	return opts
}

// run starts fire-fuse and serves until a shutdown signal or server failure.
// Every failure is logged where it happens; a non-nil result only tells main
// to exit unsuccessfully. Resources are released by deferred cleanup in
// reverse order of acquisition.
func run(opts options, logger *slog.Logger) (err error) {
	if err := validateListenAddress(opts.listenAddress); err != nil {
		logger.Error("invalid HTTP listen address", "address", opts.listenAddress, "error", err)
		return err
	}
	if err := validateProviderIngressListenAddress(opts.ingressListenAddress); err != nil || opts.ingressListenAddress == opts.listenAddress {
		if err == nil {
			err = errors.New("provider ingress and HTTP listen addresses must differ")
		}
		logger.Error("invalid provider ingress listen address", "address", opts.ingressListenAddress, "error", err)
		return err
	}
	cfg, err := loadAndCheckHost(opts.configPath, config.LoadTrusted, preflight.Check)
	if err != nil {
		logger.Error("host configuration preflight failed", "path", opts.configPath, "error", err)
		return err
	}
	controlTokens, err := loadControlTokens(map[string]string{
		"operator": opts.operatorTokenPath, "gateway": opts.gatewayTokenPath, "dispatcher": opts.dispatcherTokenPath,
		"observer": opts.observerTokenPath, "compute": opts.computeTokenPath,
	})
	if err != nil {
		logger.Error("could not load scoped control-plane tokens", "error", err)
		return err
	}
	configEncryptionKey, err := config.LoadConfigEncryptionKey(cfg.ConfigEncryptionKeyFile)
	if err != nil {
		logger.Error("could not load configuration encryption key", "error", err)
		return err
	}
	if opts.preflightOnly {
		clear(configEncryptionKey)
		logger.Info("host configuration preflight passed", "path", opts.configPath)
		return nil
	}
	store, err := microvm.OpenSQLiteStore(cfg.StateDB, microvm.WithConfigEncryptionKey(configEncryptionKey))
	clear(configEncryptionKey)
	if err != nil {
		logger.Error("could not open lifecycle state database", "path", cfg.StateDB, "error", err)
		return err
	}
	if opts.checkStateOnly {
		if err := store.Close(); err != nil {
			logger.Error("could not close lifecycle state database after validation", "path", cfg.StateDB, "error", err)
			return err
		}
		logger.Info("host and lifecycle state validation passed", "path", cfg.StateDB)
		return nil
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			logger.Error("could not close lifecycle state database", "path", cfg.StateDB, "error", closeErr)
			err = errors.Join(err, closeErr)
		}
		// Every other deferred release has run by now, so a nil error here
		// means the whole shutdown completed.
		if err == nil {
			logger.Info("HTTP server and microVMs stopped")
		}
	}()
	runtimeSupervisor, err := supervisor.New(
		cfg,
		os.Stdout,
		func(appID string) ([]string, error) {
			return resolveRuntimeWorkspace(store, appID)
		},
	)
	if err != nil {
		logger.Error("could not construct Firecracker supervisor", "error", err)
		return err
	}
	if err := runtimeSupervisor.ConfigureDatabaseAccess(
		strings.TrimSpace(os.Getenv("CANTELOP_WORKSPACE_DATABASE_CREDENTIALS_URL")),
		strings.TrimSpace(os.Getenv("CANTELOP_WORKSPACE_DATABASE_CAPABILITY_KEY")),
	); err != nil {
		logger.Error("invalid workspace database access configuration", "error", err)
		return err
	}
	if err := recovery.Reconcile(context.Background(), store, runtimeSupervisor, time.Now); err != nil {
		logger.Error("could not recover persisted microVM runtimes", "error", err)
		return err
	}
	if err := recovery.ReconcileHostSlots(context.Background(), store, runtimeSupervisor, time.Now); err != nil {
		logger.Error("could not recover persisted host slots", "error", err)
		return err
	}
	if err := recovery.CheckForOrphans(store, runtimeSupervisor); err != nil {
		logger.Error("orphaned host resources prevent startup", "error", err)
		return err
	}
	lifecycle, err := microvm.NewServiceWithSupervisorAndLimits(store, runtimeSupervisor, microvm.AdmissionLimits{
		MaxVMs:              cfg.Limits.MaxVMs,
		MaxTotalMemoryMiB:   cfg.Limits.MaxTotalMemoryMiB,
		MaxConcurrentStarts: cfg.Limits.MaxConcurrentStarts,
	})
	if err != nil {
		logger.Error("could not configure lifecycle admission", "error", err)
		return err
	}
	if os.Getenv("CANTELOP_MANAGED_HOST") == "true" {
		instance, err := managedwatchdog.HostID()
		if err != nil {
			logger.Error("managed host identity unavailable")
			_ = store.Close()
			os.Exit(1)
		}
		if err := lifecycle.ConfigureManagedHost(func() error { return managedwatchdog.Check(os.Getenv("CANTELOP_MANAGED_WATCHDOG_HEARTBEAT")) }, instance); err != nil {
			logger.Error("managed host configuration rejected")
			_ = store.Close()
			os.Exit(1)
		}
	}
	if metadata := os.Getenv("CANTELOP_MANAGED_JUICEFS_META_URL"); metadata != "" && os.Getenv("CANTELOP_MANAGED_HOST") == "true" {
		lifecycle.ConfigureManagedStorage(nfs.DirectoryQuota(cfg.Workspace.JuiceFSBin, metadata, cfg.Workspace.Root, os.Getenv("CANTELOP_MANAGED_JUICEFS_PREFIX")))
	}
	if err := lifecycle.RestoreManagedSchedules(); err != nil {
		logger.Error("could not restore managed App enforcement", "error", err)
		_ = store.Close()
		os.Exit(1)
	}
	if err := lifecycle.RestoreSessionDeadlineSchedules(); err != nil {
		logger.Error("could not restore Session deadline schedules", "error", err)
		return err
	}
	if err := lifecycle.RestoreSessionWorkLeaseSchedules(); err != nil {
		logger.Error("could not restore Session work lease schedules", "error", err)
		return err
	}
	runtimeProvider, err := firecrackerruntime.New(lifecycle)
	if err != nil {
		logger.Error("could not configure Session provider", "error", err)
		return err
	}
	if cfg.HostPool.SlotsPerProfile > 0 {
		if err := runtimeSupervisor.ConfigureHostSlotPool(
			store,
			cfg.HostPool.SlotsPerProfile,
			time.Now,
		); err != nil {
			logger.Error("could not configure host slot pool", "error", err)
			return err
		}
		defer func() {
			if closeErr := runtimeSupervisor.CloseHostSlotPool(context.Background()); closeErr != nil {
				logger.Error("host slot pool shutdown incomplete", "error", closeErr)
				err = errors.Join(err, closeErr)
			}
		}()
		if err := runtimeSupervisor.StartHostSlotPool(context.Background()); err != nil {
			logger.Error("could not start host slot pool", "error", err)
			return err
		}
		poolCtx, cancelPool := context.WithTimeout(
			context.Background(),
			time.Duration(cfg.Timeouts.Startup),
		)
		err := runtimeSupervisor.WaitHostSlotPoolReady(poolCtx)
		cancelPool()
		if err != nil {
			logger.Error("host slot pool did not become ready", "error", err)
			return err
		}
	}
	if cfg.Workspace != nil {
		files, err := workspacesync.New(cfg.Workspace.Root, opts.syncWrites)
		if err != nil {
			logger.Error("invalid sync storage configuration", "error", err)
			return err
		}
		files.CheckRoot = cachedMountCheck(func() error { return nfs.CheckWorkspaceMount(cfg.Workspace.Root) }, 5*time.Second)
		lifecycle.ConfigureWorkspaceSync(files)
	}
	credentials, err := controlCredentials(controlTokens["operator"], controlTokens["gateway"], controlTokens["dispatcher"], controlTokens["observer"])
	if err != nil {
		logger.Error("could not configure scoped control-plane credentials", "error", err)
		return err
	}
	controlHandler, err := controlauth.RequireScoped(credentials, httpapi.NewHandlerWithRuntimeProviderAndReadinessAndLogger(lifecycle, runtimeProvider, func() error {
		return checkHostWithCurrentCatalog(
			cfg,
			config.LoadTrustedImageCatalog,
			preflight.Check,
		)
	}, logger))
	if err != nil {
		logger.Error("could not secure control-plane handler", "error", err)
		return err
	}
	buildIngressHandler, err := httpapi.NewPreparedBuildIngressHandler(
		lifecycle,
		opts.artifactDownloadOrigin,
		opts.imageDirectory,
		cfg.ImageCatalog,
		&http.Client{Timeout: 30 * time.Minute},
	)
	if err != nil {
		logger.Error("could not configure Build ingress", "error", err)
		return err
	}
	uploadScope, err := controlauth.NewScope("/")
	if err != nil {
		logger.Error("could not configure Build upload authorization scope", "error", err)
		return err
	}
	uploadHandler, err := controlauth.RequireScoped([]controlauth.Credential{
		{Token: controlTokens["operator"], Scope: uploadScope},
		{Token: controlTokens["compute"], Scope: uploadScope},
	}, buildIngressHandler)
	if err != nil {
		logger.Error("could not secure Build upload handler", "error", err)
		return err
	}
	if opts.imageCollectionGrace != 0 {
		stopImageCollector, err := startImageCollector(cfg.ImageCatalog, opts.imageDirectory, opts.imageCollectionGrace, store, logger)
		if err != nil {
			logger.Error("could not configure image collection", "error", err)
			return err
		}
		defer stopImageCollector()
	}
	server := newHTTPServer(
		opts.listenAddress,
		controlHandler,
		cfg.Timeouts,
	)
	providerIngressServer := newProviderIngressHTTPServer(
		opts.ingressListenAddress,
		uploadHandler,
		cfg.Timeouts,
	)

	signalContext, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()
	type serverError struct {
		name string
		err  error
	}
	serverErrors := make(chan serverError, 2)
	go func() {
		logger.Info("HTTP server listening", "address", server.Addr)
		serverErrors <- serverError{name: "control", err: server.ListenAndServe()}
	}()
	go func() {
		logger.Info("provider ingress server listening", "address", providerIngressServer.Addr)
		serverErrors <- serverError{name: "provider ingress", err: providerIngressServer.ListenAndServe()}
	}()

	var shutdownErrors []error
	select {
	case stopped := <-serverErrors:
		if !errors.Is(stopped.err, http.ErrServerClosed) {
			logger.Error("HTTP server stopped unexpectedly", "server", stopped.name, "error", stopped.err)
			shutdownErrors = append(shutdownErrors, stopped.err)
		}
	case <-signalContext.Done():
		logger.Info("shutdown signal received")
	}

	if err := shutdownHTTPServer(providerIngressServer, cfg.Timeouts); err != nil {
		logger.Error("provider ingress server shutdown failed", "error", err)
		shutdownErrors = append(shutdownErrors, err)
	}
	serverShutdownErr, runtimeShutdownErr := shutdownServices(server, lifecycle, cfg.Timeouts)
	if serverShutdownErr != nil {
		logger.Error("graceful shutdown failed", "error", serverShutdownErr)
		shutdownErrors = append(shutdownErrors, serverShutdownErr)
	}
	if runtimeShutdownErr != nil {
		logger.Error("microVM shutdown incomplete", "error", runtimeShutdownErr)
		shutdownErrors = append(shutdownErrors, runtimeShutdownErr)
	}
	return errors.Join(shutdownErrors...)
}

const imageCollectionInterval = 15 * time.Minute

// startImageCollector runs automatic Build collection until the returned stop
// function, which waits for an in-progress pass to finish.
func startImageCollector(
	catalogPath, imageDirectory string,
	grace time.Duration,
	references imagecatalog.ImageReferenceSource,
	logger *slog.Logger,
) (func(), error) {
	collector, err := imagecatalog.NewCollector(catalogPath, imageDirectory, grace, references)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		collector.Run(ctx, imageCollectionInterval, func(report imagecatalog.CollectionReport, err error) {
			switch {
			case err != nil:
				logger.Error("image collection failed", "error", err,
					"retired_builds", report.RetiredBuilds, "removed_files", report.RemovedFiles)
			case len(report.RetiredBuilds) > 0 || len(report.RemovedFiles) > 0:
				logger.Info("image collection freed disk",
					"retired_builds", report.RetiredBuilds, "removed_files", report.RemovedFiles,
					"freed_bytes", report.FreedBytes)
			}
			if report.Skipped != "" || report.OrphanSweepSkipped != "" {
				logger.Info("image collection deferred", "reason", report.Skipped,
					"orphan_sweep", report.OrphanSweepSkipped)
			}
		})
	}()
	logger.Info("image collection enabled", "grace", grace.String(), "interval", imageCollectionInterval.String())
	return func() {
		cancel()
		<-done
	}, nil
}

type runtimeWorkspaceStore interface {
	ResolveAppConfig(appID string) ([]string, error)
	GetAppSessionRuntime(appID string) (microvm.AppSessionRuntime, error)
}

func resolveRuntimeWorkspace(
	store runtimeWorkspaceStore,
	appID string,
) ([]string, error) {
	workspace, err := store.ResolveAppConfig(appID)
	if err != nil {
		return nil, err
	}
	sessionRuntime, err := store.GetAppSessionRuntime(appID)
	if err != nil {
		return nil, err
	}
	if err := sessionRuntime.Validate(); err != nil {
		return nil, fmt.Errorf("load App Session runtime: %w", err)
	}
	return append(
		workspace,
		fmt.Sprintf("%s=%d", internalPortWorkspaceName, sessionRuntime.Runtime.InternalPort),
	), nil
}

func checkHostWithCurrentCatalog(
	cfg config.Config,
	loadCatalog imageCatalogLoader,
	check hostChecker,
) error {
	images, err := loadCatalog(cfg.ImageCatalog)
	if err != nil {
		return fmt.Errorf("load current image catalog: %w", err)
	}
	cfg.Images = images
	return check(cfg)
}

func loadAndCheckHost(
	path string,
	load configLoader,
	check hostChecker,
) (config.Config, error) {
	cfg, err := load(path)
	if err != nil {
		return config.Config{}, fmt.Errorf("load trusted configuration: %w", err)
	}
	if err := check(cfg); err != nil {
		return config.Config{}, fmt.Errorf("check host prerequisites: %w", err)
	}
	return cfg, nil
}

func newHTTPServer(address string, handler http.Handler, timeouts config.TimeoutsConfig) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: httpReadHeaderTimeout,
		ReadTimeout:       time.Duration(timeouts.HTTPRequestRead),
		IdleTimeout:       time.Duration(timeouts.HTTPIdle),
	}
}

func newProviderIngressHTTPServer(address string, handler http.Handler, timeouts config.TimeoutsConfig) *http.Server {
	return &http.Server{
		Addr: address, Handler: handler, ReadHeaderTimeout: httpReadHeaderTimeout,
		IdleTimeout: time.Duration(timeouts.HTTPIdle),
	}
}

func shutdownHTTPServer(server serverShutdowner, timeouts config.TimeoutsConfig) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeouts.ServerShutdown))
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		return errors.Join(err, server.Close())
	}
	return nil
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
		return errors.New("IP must be a loopback address")
	}
	return nil
}

func validateProviderIngressListenAddress(address string) error {
	endpoint, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("parse numeric IP and port: %w", err)
	}
	if endpoint.Port() == 0 {
		return errors.New("port must be greater than zero")
	}
	addr := endpoint.Addr().Unmap()
	if !addr.IsLoopback() && addr != netip.MustParseAddr("10.77.0.1") {
		return errors.New("IP must be loopback or the WireGuard host address")
	}
	return nil
}

type serverShutdowner interface {
	Shutdown(context.Context) error
	Close() error
}

type runtimeTerminator interface {
	TerminateAll(context.Context) error
}

func shutdownServices(
	server serverShutdowner,
	runtimes runtimeTerminator,
	timeouts config.TimeoutsConfig,
) (serverErr, runtimeErr error) {
	serverCtx, cancelServer := context.WithTimeout(
		context.Background(),
		time.Duration(timeouts.ServerShutdown),
	)
	serverErr = server.Shutdown(serverCtx)
	cancelServer()
	if serverErr != nil {
		serverErr = errors.Join(serverErr, server.Close())
	}

	runtimeCtx, cancelRuntimes := context.WithTimeout(
		context.Background(),
		runtimeShutdownBudget(timeouts),
	)
	runtimeErr = runtimes.TerminateAll(runtimeCtx)
	cancelRuntimes()
	return serverErr, runtimeErr
}

func runtimeShutdownBudget(timeouts config.TimeoutsConfig) time.Duration {
	const maximumDuration = time.Duration(1<<63 - 1)
	budget := time.Duration(timeouts.GuestShutdown)
	for _, duration := range []config.Duration{
		timeouts.ShutdownGrace,
		timeouts.ShutdownForce,
		timeouts.Cleanup,
	} {
		addition := time.Duration(duration)
		if addition > maximumDuration-budget {
			return maximumDuration
		}
		budget += addition
	}
	return budget
}

// cachedMountCheck reuses a successful mount-policy check for ttl so sync
// requests do not reparse the host mount table on every operation. Failures
// are never cached.
func cachedMountCheck(check func() error, ttl time.Duration) func() error {
	var verifiedAt atomic.Int64
	return func() error {
		if last := verifiedAt.Load(); last != 0 && time.Since(time.Unix(0, last)) < ttl {
			return nil
		}
		if err := check(); err != nil {
			verifiedAt.Store(0)
			return err
		}
		verifiedAt.Store(time.Now().UnixNano())
		return nil
	}
}

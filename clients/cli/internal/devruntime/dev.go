// Package devruntime runs a project locally for cantelop dev: it builds and
// watches the SDK artifacts, hosts the API, emulates the control plane's Session
// and Workspace routes, starts native or Docker Sandboxes, and serves the local
// Workspace databases.
package devruntime

import (
	"bufio"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stepandel/cantelop/clients/cli/internal/boundedio"
	"github.com/stepandel/cantelop/clients/cli/internal/integration"
	"github.com/stepandel/cantelop/clients/cli/internal/projectconfig"
	"github.com/stepandel/cantelop/clients/cli/internal/sessionimage"
	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestconfig"
	"github.com/stepandel/cantelop/fire-fuse/workspacesync"
)

const (
	DefaultAPIPort = 8787
)

var sdkLocalBuildProgram = projectconfig.SDKLoadProgram + `
const sdk = await loadSDK("buildSessionRuntime");
if (sdk.CANTELOP_CLI_BUILD_PROTOCOL_VERSION === 6) {
 await sdk.buildEdgeApi({definition:process.argv[1],app:process.argv[3],outdir:process.argv[2],runtimeOrigin:process.argv[5]});
 await sdk.buildSessionRuntime({definition:process.argv[1],app:process.argv[3],outdir:process.argv[4],projectRoot:process.argv[6]});
} else {

if (sdk.CANTELOP_LOCAL_DATABASE_PROTOCOL_VERSION !== 1) throw new Error("Local Workspace databases require a newer @cantelop/sdk; run npm install @cantelop/sdk@latest");
await sdk.buildLocalApi({ entrypoint: process.argv[1], outdir: process.argv[2], runtimeOrigin: process.argv[5] });
await sdk.buildSessionRuntime({ entrypoint: process.argv[3], outdir: process.argv[4] });
}
`

//go:embed project_dev_watch.mjs
var localProjectWatchProgram string

var sdkLocalWatchProgram = projectconfig.SDKLoadProgram + localProjectWatchProgram

//go:embed project_dev_host.mjs
var localAPIHostProgram string

type builtLocalProject struct {
	apiModule            string
	sessionRuntimeModule string
	watcher              *localBuildWatcher
	image                string
	temporaryRoot        string
	baseImage            string
}

type localBuildEvent struct {
	Component string `json:"component"`
	Error     string `json:"error"`
}

type localBuildWatcher struct {
	*supervisedProcess
	events chan localBuildEvent
}

type localAPIHost struct {
	*supervisedProcess
}

// Options are the dev command's settings.
type Options struct {
	ConfigPath         string
	App                string
	Port               int
	Container          bool
	EnvironmentPath    string
	EnvironmentPathSet bool
}

// Run builds the project and serves its API and Session runtime
// locally until ctx is cancelled or a component stops.
func Run(ctx context.Context, options Options, output io.Writer) error {
	config, err := projectconfig.LoadSelected(options.ConfigPath, options.App)
	if err != nil {
		return err
	}
	environment, err := projectconfig.LoadLocalEnvironment(config.Root, options.EnvironmentPath, options.EnvironmentPathSet)
	if err != nil {
		return err
	}
	environment = applyProjectEnvironmentDefaults(config, environment)

	bridgeBind := "127.0.0.1:0"
	if options.Container {
		bridgeBind = "0.0.0.0:0"
	}
	bridgeListener, err := net.Listen("tcp", bridgeBind)
	if err != nil {
		return fmt.Errorf("reserve local Session bridge: %w", err)
	}
	bridgeAddress := net.JoinHostPort("127.0.0.1", strconv.Itoa(bridgeListener.Addr().(*net.TCPAddr).Port))
	bridgeOrigin := "http://" + bridgeAddress
	built, err := buildLocalProject(ctx, config, bridgeOrigin, options.Container, output)
	if err != nil {
		_ = bridgeListener.Close()
		return err
	}
	defer built.cleanup()
	bridgeTokenBytes := make([]byte, 32)
	if _, err := rand.Read(bridgeTokenBytes); err != nil {
		_ = bridgeListener.Close()
		return fmt.Errorf("generate local Session bridge credential: %w", err)
	}
	bridgeToken := hex.EncodeToString(bridgeTokenBytes)

	environmentFile, bindingsFile, hostFile, err := writeLocalRuntimeFiles(built.temporaryRoot, environment)
	if err != nil {
		_ = bridgeListener.Close()
		return err
	}
	if config.Definition != "" {
		if err := integration.ConfigureLocal(config, options.Port, bindingsFile, bridgeOrigin); err != nil {
			return err
		}
	}
	workspaceRoot := filepath.Join(config.Root, ".cantelop", "dev", config.AppSlug, "workspaces")
	sandboxOrigin := bridgeOrigin
	if options.Container {
		sandboxOrigin = "http://host.docker.internal:" + strconv.Itoa(bridgeListener.Addr().(*net.TCPAddr).Port)
	}
	databases, err := newLocalDatabases(filepath.Join(config.Root, ".cantelop", "dev", config.AppSlug, "databases"), bridgeOrigin, sandboxOrigin)
	if err != nil {
		_ = bridgeListener.Close()
		return fmt.Errorf("prepare local Workspace databases: %w", err)
	}
	defer databases.Close()
	schemaModule := built.apiModule
	if config.Definition != "" {
		schemaModule = built.sessionRuntimeModule
	}
	configureLocalDatabaseSchema(databases, config.Root, schemaModule)
	var factory localSandboxFactory
	if options.Container {
		factory, err = newDockerLocalSandboxFactory(built.image, environmentFile, workspaceRoot)
	} else {
		factory, err = newNativeLocalSandboxFactory(built.sessionRuntimeModule, environment, workspaceRoot, output)
	}
	if err != nil {
		_ = bridgeListener.Close()
		return err
	}
	runtime, err := newLocalRuntime(projectconfig.LocalAppID(config.AppSlug), factory)
	if err != nil {
		_ = bridgeListener.Close()
		return err
	}
	if err = os.MkdirAll(workspaceRoot, 0700); err != nil {
		_ = bridgeListener.Close()
		return err
	}
	runtime.syncFiles, err = workspacesync.New(workspaceRoot, true)
	if err != nil {
		_ = bridgeListener.Close()
		return err
	}
	// Match the local Sandbox factories: the workload reaches the Workspace
	// through its setgid group, so synced entries must stay group-writable.
	runtime.syncFiles.WorkspaceMode = os.ModeSetgid | 0o770
	closeState, err := runtime.enableDurableEvents(filepath.Join(config.Root, ".cantelop", "dev", config.AppSlug))
	if err != nil {
		return fmt.Errorf("prepare durable local events: %w", err)
	}
	defer closeState()
	defer runtime.Close()
	runtime.bridgeToken = bridgeToken
	runtime.databases = databases
	switch factory := factory.(type) {
	case *nativeLocalSandboxFactory:
		factory.databases = databases
	case *dockerLocalSandboxFactory:
		factory.databases = databases
	}
	buildFailure := make(chan error, 1)
	if built.watcher != nil {
		go monitorLocalBuilds(ctx, built.watcher, runtime, output, buildFailure)
	}

	bridgeServer := &http.Server{Handler: runtime, ReadHeaderTimeout: 5 * time.Second}
	bridgeDone := make(chan error, 1)
	go func() { bridgeDone <- bridgeServer.Serve(bridgeListener) }()
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = bridgeServer.Shutdown(shutdownContext)
	}()

	host, err := startLocalAPIHost(ctx, hostFile, built.apiModule, bindingsFile, bridgeOrigin, bridgeToken, options.Port, output)
	if err != nil {
		return err
	}
	defer host.Close()
	mode := "native"
	if options.Container {
		mode = "container"
	}
	_, _ = fmt.Fprintf(output, "Cantelop App running at http://127.0.0.1:%d (%s mode)\n", options.Port, mode)

	select {
	case <-ctx.Done():
		return nil
	case <-host.done:
		err := host.WaitError()
		if err == nil {
			return errors.New("local API host stopped unexpectedly")
		}
		return fmt.Errorf("local API host stopped: %w", err)
	case err := <-bridgeDone:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("local Session bridge stopped: %w", err)
	case err := <-buildFailure:
		return err
	}
}

func buildLocalProject(
	ctx context.Context,
	config projectconfig.Config,
	bridgeOrigin string,
	containerMode bool,
	output io.Writer,
) (builtLocalProject, error) {
	temporaryRoot, err := os.MkdirTemp("", "cantelop-dev-*")
	if err != nil {
		return builtLocalProject{}, fmt.Errorf("create local build directory: %w", err)
	}
	built := builtLocalProject{temporaryRoot: temporaryRoot}
	keep := false
	defer func() {
		if !keep {
			built.cleanup()
		}
	}()
	apiDirectory := filepath.Join(temporaryRoot, "api")
	sessionRuntimeDirectory := filepath.Join(temporaryRoot, "session-runtime")
	if containerMode {
		_, _ = fmt.Fprintln(output, "Building local API and Session runtime artifacts")
		command := exec.CommandContext(ctx, "node", "--input-type=module", "--eval", sdkLocalBuildProgram,
			projectconfig.BuildDefinition(config), apiDirectory, projectconfig.BuildApp(config), sessionRuntimeDirectory, bridgeOrigin, config.Root)
		command.Dir = config.Root
		buildError := boundedio.NewLimitedBuffer(64 << 10)
		command.Stderr = io.MultiWriter(output, buildError)
		if err := command.Run(); err != nil {
			detail := strings.TrimSpace(buildError.String())
			if detail == "" {
				detail = err.Error()
			}
			return builtLocalProject{}, fmt.Errorf("build local artifacts with project-installed @cantelop/sdk: %s", detail)
		}
	} else {
		_, _ = fmt.Fprintln(output, "Building and watching local API and Session runtime artifacts")
		built.watcher, err = startLocalBuildWatcher(ctx, config, apiDirectory, sessionRuntimeDirectory, bridgeOrigin, output)
		if err != nil {
			return builtLocalProject{}, err
		}
	}
	apiArtifact, err := projectconfig.LoadAPIArtifact(apiDirectory)
	if err != nil {
		return builtLocalProject{}, fmt.Errorf("SDK produced an invalid local API artifact: %w", err)
	}
	if _, err := boundedio.ReadRegularFile(filepath.Join(sessionRuntimeDirectory, "session-runtime.mjs"), 512<<20); err != nil {
		return builtLocalProject{}, fmt.Errorf("SDK produced an invalid local Session runtime artifact: %w", err)
	}
	built.apiModule = filepath.Join(apiDirectory, apiArtifact.Manifest.MainModule)
	built.sessionRuntimeModule = filepath.Join(sessionRuntimeDirectory, "session-runtime.mjs")
	if !containerMode {
		if err := os.WriteFile(filepath.Join(sessionRuntimeDirectory, "local-workspace.mjs"), []byte(sessionimage.LocalWorkspaceBootstrap), 0o644); err != nil {
			return builtLocalProject{}, err
		}
		keep = true
		return built, nil
	}

	suffix := localID("")
	built.baseImage = "cantelop-dev-base:" + suffix
	built.image = "cantelop-dev:" + suffix
	dockerfile, contextDirectory, err := projectconfig.PrepareSessionBuild(config, temporaryRoot)
	if err != nil {
		return builtLocalProject{}, err
	}
	_, _ = fmt.Fprintln(output, "Building local Session runtime base image for linux/amd64")
	if err := runLocalDockerBuild(ctx, output, config.Root, dockerfile,
		[]string{"--tag", built.baseImage}, contextDirectory); err != nil {
		return builtLocalProject{}, fmt.Errorf("build local Session runtime base image: %w", err)
	}
	if err := sessionimage.PrepareManagedSessionImage(ctx, built.baseImage, sessionRuntimeDirectory); err != nil {
		return builtLocalProject{}, err
	}
	dockerfilePath := filepath.Join(sessionRuntimeDirectory, "Dockerfile.local")
	if err := os.WriteFile(dockerfilePath, []byte(localSessionRuntimeDockerfile()), 0o600); err != nil {
		return builtLocalProject{}, fmt.Errorf("write local Session runtime Dockerfile: %w", err)
	}
	_, _ = fmt.Fprintln(output, "Layering Cantelop local Session runtime")
	if err := runLocalDockerBuild(ctx, output, sessionRuntimeDirectory, dockerfilePath,
		[]string{"--build-arg", "BASE_IMAGE=" + built.baseImage, "--tag", built.image}, sessionRuntimeDirectory); err != nil {
		return builtLocalProject{}, fmt.Errorf("build local Session runtime image: %w", err)
	}
	keep = true
	return built, nil
}

func localSessionRuntimeDockerfile() string {
	return fmt.Sprintf(`ARG BASE_IMAGE
FROM %s AS cantelop_runtime
FROM ${BASE_IMAGE}
%s%sCOPY --from=cantelop_runtime /usr/local/bin/bun /__cantelop/bin/bun
COPY --chown=0:0 --chmod=0644 .cantelop-runtime/local-workspace.mjs /__cantelop/local-workspace.mjs
CMD [%q, "--library-path", %q, %q, "--no-install", "--preload", "/__cantelop/local-workspace.mjs", %q]
`, sessionimage.SessionRuntimeImage, sessionimage.SessionRuntimeLibrariesDockerfile(), sessionimage.ManagedSessionDockerfile(), guestconfig.SessionLoader, guestconfig.SessionLibraryPath, guestconfig.PlatformRuntimeContainerPath, guestconfig.SessionModule)
}

func runLocalDockerBuild(
	ctx context.Context,
	output io.Writer,
	directory string,
	dockerfile string,
	extra []string,
	contextPath string,
) error {
	args := []string{"build", "--platform", "linux/amd64", "--file", dockerfile}
	args = append(args, extra...)
	args = append(args, contextPath)
	command := exec.CommandContext(ctx, "docker", args...)
	command.Dir = directory
	commandOutput := boundedio.NewLimitedBuffer(64 << 10)
	command.Stdout = output
	command.Stderr = io.MultiWriter(output, commandOutput)
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(commandOutput.String())
		if detail == "" {
			detail = err.Error()
		}
		return errors.New(detail)
	}
	return nil
}

func (built builtLocalProject) cleanup() {
	if built.watcher != nil {
		built.watcher.Close()
	}
	for _, image := range []string{built.image, built.baseImage} {
		if image == "" {
			continue
		}
		command := exec.Command("docker", "image", "rm", image)
		_ = command.Run()
	}
	if built.temporaryRoot != "" {
		_ = os.RemoveAll(built.temporaryRoot)
	}
}

func startLocalBuildWatcher(
	ctx context.Context,
	config projectconfig.Config,
	apiDirectory, sessionRuntimeDirectory, bridgeOrigin string,
	output io.Writer,
) (*localBuildWatcher, error) {
	return startLocalBuildWatcherProgram(
		ctx, config, apiDirectory, sessionRuntimeDirectory, bridgeOrigin, output, sdkLocalWatchProgram,
	)
}

func startLocalBuildWatcherProgram(
	ctx context.Context,
	config projectconfig.Config,
	apiDirectory, sessionRuntimeDirectory, bridgeOrigin string,
	output io.Writer,
	program string,
) (*localBuildWatcher, error) {
	command := exec.CommandContext(ctx, "node", "--input-type=module", "--eval", program,
		projectconfig.BuildDefinition(config), apiDirectory, projectconfig.BuildApp(config), sessionRuntimeDirectory, bridgeOrigin, config.Root)
	command.Dir = config.Root
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	command.Stderr = output
	process, err := startSupervisedProcess(command, nil)
	if err != nil {
		return nil, fmt.Errorf("start incremental local build: %w", err)
	}
	watcher := &localBuildWatcher{supervisedProcess: process, events: make(chan localBuildEvent, 32)}
	ready := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "CANTELOP_LOCAL_BUILD_READY" {
				select {
				case <-ready:
				default:
					close(ready)
				}
				continue
			}
			if document, found := strings.CutPrefix(line, "CANTELOP_LOCAL_BUILD_EVENT "); found {
				var event localBuildEvent
				if json.Unmarshal([]byte(document), &event) == nil &&
					(event.Component == "api" || event.Component == "session-runtime") {
					watcher.events <- event
					continue
				}
			}
			_, _ = fmt.Fprintln(output, line)
		}
	}()
	select {
	case <-ready:
		return watcher, nil
	case <-watcher.done:
		if err := watcher.WaitError(); err != nil {
			return nil, fmt.Errorf("start incremental local build: %w", err)
		}
		return nil, errors.New("incremental local build stopped before becoming ready")
	case <-time.After(15 * time.Second):
		watcher.Close()
		return nil, errors.New("incremental local build did not become ready")
	case <-ctx.Done():
		watcher.Close()
		return nil, ctx.Err()
	}
}

func monitorLocalBuilds(
	ctx context.Context,
	watcher *localBuildWatcher,
	runtime *localRuntime,
	output io.Writer,
	failure chan<- error,
) {
	for {
		select {
		case event := <-watcher.events:
			if event.Error != "" {
				_, _ = fmt.Fprintf(output, "Local %s rebuild failed; continuing with the last good build: %s\n",
					event.Component, event.Error)
				continue
			}
			if (event.Component == "session-runtime" || event.Component == "api") && runtime.databases != nil {
				if err := runtime.databases.refreshSchema(); err != nil {
					_, _ = fmt.Fprintf(output, "Local database migration failed: %s\n", err)
					continue
				}
			}
			if event.Component == "session-runtime" {
				if err := runtime.ReleaseSandboxes(); err != nil {
					failure <- fmt.Errorf("release local Sandboxes after Session runtime rebuild: %w", err)
					return
				}
			}
			_, _ = fmt.Fprintf(output, "Rebuilt local %s\n", event.Component)
		case <-watcher.done:
			if ctx.Err() != nil {
				return
			}
			err := watcher.WaitError()
			if err == nil {
				err = errors.New("stopped unexpectedly")
			}
			failure <- fmt.Errorf("incremental local build stopped: %w", err)
			return
		case <-ctx.Done():
			return
		}
	}
}

func applyProjectEnvironmentDefaults(config projectconfig.Config, values map[string]string) map[string]string {
	merged := make(map[string]string, len(config.Environment)+len(values))
	for name, declaration := range config.Environment {
		if declaration.Default != nil {
			merged[name] = *declaration.Default
		}
	}
	for name, value := range values {
		merged[name] = value
	}
	return merged
}

func writeLocalRuntimeFiles(root string, environment map[string]string) (string, string, string, error) {
	names := make([]string, 0, len(environment))
	for name := range environment {
		names = append(names, name)
	}
	sort.Strings(names)
	var dockerEnvironment strings.Builder
	for _, name := range names {
		_, _ = fmt.Fprintf(&dockerEnvironment, "%s=%s\n", name, environment[name])
	}
	environmentFile := filepath.Join(root, "app.env")
	if err := os.WriteFile(environmentFile, []byte(dockerEnvironment.String()), 0o600); err != nil {
		return "", "", "", fmt.Errorf("write local Sandbox environment: %w", err)
	}
	bindings, err := json.Marshal(environment)
	if err != nil {
		return "", "", "", err
	}
	bindingsFile := filepath.Join(root, "bindings.json")
	if err := os.WriteFile(bindingsFile, bindings, 0o600); err != nil {
		return "", "", "", fmt.Errorf("write local API bindings: %w", err)
	}
	hostFile := filepath.Join(root, "api-host.mjs")
	if err := os.WriteFile(hostFile, []byte(localAPIHostProgram), 0o600); err != nil {
		return "", "", "", fmt.Errorf("write local API host: %w", err)
	}
	return environmentFile, bindingsFile, hostFile, nil
}

func startLocalAPIHost(
	ctx context.Context,
	hostFile, apiModule, bindingsFile, bridgeOrigin, bridgeToken string,
	port int,
	output io.Writer,
) (*localAPIHost, error) {
	command := exec.CommandContext(ctx, "node", hostFile, apiModule, strconv.Itoa(port), bindingsFile, bridgeOrigin)
	credentialPipe, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	command.Stderr = output
	process, err := startSupervisedProcess(command, nil)
	if err != nil {
		return nil, fmt.Errorf("start local API host: %w", err)
	}
	if _, err := io.WriteString(credentialPipe, bridgeToken); err != nil {
		_ = credentialPipe.Close()
		_ = command.Process.Kill()
		return nil, fmt.Errorf("send local API host bridge credential: %w", err)
	}
	if err := credentialPipe.Close(); err != nil {
		_ = command.Process.Kill()
		return nil, fmt.Errorf("close local API host bridge credential pipe: %w", err)
	}
	host := &localAPIHost{supervisedProcess: process}
	ready := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "CANTELOP_LOCAL_READY "+strconv.Itoa(port) {
				select {
				case <-ready:
				default:
					close(ready)
				}
				continue
			}
			_, _ = fmt.Fprintln(output, line)
		}
	}()
	select {
	case <-ready:
		return host, nil
	case <-host.done:
		if err := host.WaitError(); err != nil {
			return nil, fmt.Errorf("start local API host: %w", err)
		}
		return nil, errors.New("local API host stopped before becoming ready")
	case <-time.After(15 * time.Second):
		_ = command.Process.Kill()
		return nil, errors.New("local API host did not become ready")
	case <-ctx.Done():
		_ = command.Process.Kill()
		return nil, ctx.Err()
	}
}

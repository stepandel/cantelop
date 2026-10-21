// Package deploy builds a project's release artifacts, uploads them and follows
// the release until it is active.
package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/stepandel/cantelop/clients/cli/internal/boundedio"
	"github.com/stepandel/cantelop/clients/cli/internal/ids"
	"github.com/stepandel/cantelop/clients/cli/internal/projectconfig"
	"github.com/stepandel/cantelop/clients/cli/internal/sessionimage"
	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestconfig"
)

// SDKBuildProgram also warns, without failing, when the SDK predates the
// application-owned mailbox; applicationMailboxMinimumSDK is generated from ApplicationMailboxMinimumSDKVersion.
var SDKBuildProgram = projectconfig.SDKLoadProgram + `
const sdk = await loadSDK("buildSessionRuntime");
if (sdk.CANTELOP_CLI_BUILD_PROTOCOL_VERSION === 6) {
 const result = await sdk.buildAppArtifacts({definition:process.argv[1],outdir:process.argv[2]+"-apps",projectRoot:process.argv[5]});
 const app = result.apps.find(app => app.name === process.argv[3]);
 if (!app) throw new Error("Selected App is not defined");
 const {cp} = await import("node:fs/promises");
 await cp(app.edge.directory,process.argv[2],{recursive:true});
 await cp(app.runtime.directory,process.argv[4],{recursive:true});
 await cp(result.backend.directory,process.argv[5]+"/.cantelop/client",{recursive:true});
} else {

{
  const applicationMailboxMinimumSDK = "` + projectconfig.ApplicationMailboxMinimumSDKVersion + `";
  const { readFile } = await import("node:fs/promises");
  const { dirname, join } = await import("node:path");
  const { fileURLToPath } = await import("node:url");
  const parse = (value) => {
    const match = /^(\d+)\.(\d+)\.(\d+)(-[^+]*)?(\+.*)?$/.exec(value ?? "");
    return match && { core: match.slice(1, 4).map(Number), prerelease: match[4] !== undefined };
  };
  let directory = dirname(fileURLToPath(import.meta.resolve("@cantelop/sdk/build")));
  let version;
  for (let depth = 0; depth < 3 && version === undefined; depth += 1, directory = dirname(directory)) {
    try {
      const manifest = JSON.parse(await readFile(join(directory, "package.json"), "utf8"));
      if (manifest.name === "@cantelop/sdk") version = manifest.version;
    } catch {}
  }
  const have = parse(version);
  const want = parse(applicationMailboxMinimumSDK);
  const difference = have && have.core.map((part, index) => part - want.core[index]).find((part) => part !== 0);
  if (have && (difference === undefined ? have.prerelease : difference < 0)) {
    process.stderr.write("Warning: the project-installed @cantelop/sdk ` + projectconfig.ApplicationMailboxSDKWarning + `\n");
  }
}
const api = await sdk.buildApi({ entrypoint: process.argv[1], outdir: process.argv[2] });
if (typeof api.routeDiscoveryError === "string") {
  process.stderr.write("Route discovery failed: " + api.routeDiscoveryError + "\n");
}
await sdk.buildSessionRuntime({ entrypoint: process.argv[3], outdir: process.argv[4] });
}
`

var bundledSessionRuntimeDockerfile = fmt.Sprintf(`ARG BASE_IMAGE
FROM %s AS cantelop_runtime
FROM ${BASE_IMAGE}
%s%sCMD [%q, "/opt/cantelop/session-runtime.mjs"]
`, sessionimage.SessionRuntimeImage, sessionimage.SessionRuntimeLibrariesDockerfile(), sessionimage.ManagedSessionDockerfile(), guestconfig.PlatformRuntimeContainerPath)

type Built struct {
	ArtifactDirectory string
	ImageID           string
	TemporaryRoot     string
}

// writeAPIRoutes reports the route table the release will publish to the console.
func writeAPIRoutes(output io.Writer, manifest projectconfig.APIArtifactManifest) error {
	routes, discovered, err := manifest.DeclaredRoutes()
	if err != nil {
		return err
	}
	if manifest.SchemaVersion < 3 && manifest.Kind != "cantelop-protocol-edge" {
		return nil
	}
	if !discovered {
		_, err = fmt.Fprintln(output, "API routes could not be discovered; the console will list observed routes only")
		return err
	}
	if _, err = fmt.Fprintf(output, "API routes (%d)\n", len(routes)); err != nil {
		return err
	}
	for _, route := range routes {
		if _, err = fmt.Fprintf(output, "  %-7s %s\n", route.Method, route.Path); err != nil {
			return err
		}
	}
	return nil
}

func BuildConfigured(ctx context.Context, configPath string, output io.Writer) (projectconfig.Config, Built, error) {
	return BuildSelected(ctx, configPath, "", output)
}

func BuildSelected(ctx context.Context, configPath, app string, output io.Writer) (projectconfig.Config, Built, error) {
	config, err := projectconfig.LoadSelected(configPath, app)
	if err != nil {
		return projectconfig.Config{}, Built{}, err
	}
	built, err := Build(ctx, config, output)
	if err != nil {
		return projectconfig.Config{}, Built{}, err
	}
	return config, built, nil
}

func Build(ctx context.Context, config projectconfig.Config, output io.Writer) (Built, error) {
	temporaryRoot, err := os.MkdirTemp("", "cantelop-build-*")
	if err != nil {
		return Built{}, fmt.Errorf("create build directory: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(temporaryRoot)
		}
	}()
	artifactDirectory := filepath.Join(temporaryRoot, "api")
	sessionRuntimeArtifactDirectory := filepath.Join(temporaryRoot, "session-runtime")
	_, _ = fmt.Fprintln(output, "Building API artifact")
	apiBuild := exec.CommandContext(ctx, "node", "--input-type=module", "--eval", SDKBuildProgram,
		projectconfig.BuildDefinition(config), artifactDirectory, projectconfig.BuildApp(config), sessionRuntimeArtifactDirectory, config.Root)
	apiBuild.Dir = config.Root
	apiError := boundedio.NewLimitedBuffer(64 << 10)
	apiBuild.Stderr = io.MultiWriter(output, apiError)
	if err := apiBuild.Run(); err != nil {
		message := strings.TrimSpace(apiError.String())
		if message == "" {
			message = err.Error()
		}
		return Built{}, fmt.Errorf("build API artifact with project-installed @cantelop/sdk: %s", message)
	}
	apiArtifact, err := projectconfig.LoadAPIArtifact(artifactDirectory)
	if err == nil {
		err = writeAPIRoutes(output, apiArtifact.Manifest)
	}
	if err != nil {
		return Built{}, fmt.Errorf("SDK produced an invalid API artifact: %w", err)
	}
	if config.Definition != "" {
		if err := projectconfig.ValidateRuntimePair(apiArtifact.Manifest, sessionRuntimeArtifactDirectory); err != nil {
			return Built{}, err
		}
	}
	if _, err := boundedio.ReadRegularFile(filepath.Join(sessionRuntimeArtifactDirectory, "session-runtime.mjs"), 512<<20); err != nil {
		return Built{}, fmt.Errorf("SDK produced an invalid Session runtime artifact: %w", err)
	}

	imageIDPath := filepath.Join(temporaryRoot, "session-runtime-base-image-id")
	baseImageReference := "cantelop-local:" + filepath.Base(temporaryRoot)
	defer func() {
		remove := exec.CommandContext(context.Background(), "docker", "image", "rm", baseImageReference)
		_ = remove.Run()
	}()
	dockerfile, contextDirectory, err := projectconfig.PrepareSessionBuild(config, temporaryRoot)
	if err != nil {
		return Built{}, err
	}
	_, _ = fmt.Fprintln(output, "Building Session runtime image for linux/amd64")
	sessionRuntimeBuild := exec.CommandContext(ctx, "docker", "build", "--platform", "linux/amd64", "--file", dockerfile,
		"--tag", baseImageReference, "--iidfile", imageIDPath, contextDirectory)
	sessionRuntimeBuild.Dir = config.Root
	sessionRuntimeError := boundedio.NewLimitedBuffer(64 << 10)
	sessionRuntimeBuild.Stdout = output
	sessionRuntimeBuild.Stderr = io.MultiWriter(output, sessionRuntimeError)
	if err := sessionRuntimeBuild.Run(); err != nil {
		message := strings.TrimSpace(sessionRuntimeError.String())
		if message == "" {
			message = err.Error()
		}
		return Built{}, fmt.Errorf("build Session runtime image: %s", message)
	}
	imageIDDocument, err := boundedio.ReadRegularFile(imageIDPath, 256)
	if err != nil {
		return Built{}, fmt.Errorf("read built Session runtime image ID: %w", err)
	}
	imageID := strings.TrimSpace(string(imageIDDocument))
	if !ids.DockerImageIDPattern.MatchString(imageID) {
		return Built{}, errors.New("Docker build returned an invalid immutable image ID")
	}
	imageID, err = optimizeSessionRuntimeImage(ctx, baseImageReference, sessionRuntimeArtifactDirectory, temporaryRoot, output)
	if err != nil {
		return Built{}, err
	}
	keep = true
	return Built{ArtifactDirectory: artifactDirectory, ImageID: imageID, TemporaryRoot: temporaryRoot}, nil
}

func optimizeSessionRuntimeImage(
	ctx context.Context,
	baseImageReference string,
	artifactDirectory string,
	temporaryRoot string,
	output io.Writer,
) (string, error) {
	if err := sessionimage.PrepareManagedSessionImage(ctx, baseImageReference, artifactDirectory); err != nil {
		return "", err
	}
	dockerfilePath := filepath.Join(temporaryRoot, "Dockerfile.session-runtime")
	dockerfile := bundledSessionRuntimeDockerfile
	if document, err := os.ReadFile(filepath.Join(artifactDirectory, "cantelop-runtime.json")); err == nil {
		var manifest projectconfig.SessionRuntimeManifest
		if json.Unmarshal(document, &manifest) != nil {
			return "", errors.New("invalid runtime manifest")
		}
		names := make([]string, 0, len(manifest.Environment))
		for name := range manifest.Environment {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			declaration := manifest.Environment[name]
			if declaration.Default != nil {
				dockerfile = strings.Replace(dockerfile, "CMD [", "ENV "+name+"="+strings.ReplaceAll(strconv.Quote(*declaration.Default), "$", `\$`)+"\nCMD [", 1)
			}
		}
	}
	if err := os.WriteFile(dockerfilePath, []byte(dockerfile), 0o600); err != nil {
		return "", fmt.Errorf("write optimized Session runtime Dockerfile: %w", err)
	}
	imageIDPath := filepath.Join(temporaryRoot, "session-runtime-image-id")
	_, _ = fmt.Fprintln(output, "Bundling SDK Session runtime for platform Bun")
	command := exec.CommandContext(ctx, "docker", "build", "--platform", "linux/amd64", "--file", dockerfilePath,
		"--build-arg", "BASE_IMAGE="+baseImageReference, "--iidfile", imageIDPath, artifactDirectory)
	commandError := boundedio.NewLimitedBuffer(64 << 10)
	command.Stdout = output
	command.Stderr = io.MultiWriter(output, commandError)
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(commandError.String())
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("optimize Session runtime image: %s", message)
	}
	document, err := boundedio.ReadRegularFile(imageIDPath, 256)
	if err != nil {
		return "", fmt.Errorf("read optimized Session runtime image ID: %w", err)
	}
	imageID := strings.TrimSpace(string(document))
	if !ids.DockerImageIDPattern.MatchString(imageID) {
		return "", errors.New("optimized Docker build returned an invalid immutable image ID")
	}
	return imageID, nil
}

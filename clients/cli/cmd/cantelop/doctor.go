package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stepandel/cantelop/clients/cli/internal/auth"
	"github.com/stepandel/cantelop/clients/cli/internal/controlapi"
	"github.com/stepandel/cantelop/clients/cli/internal/projectconfig"
	"github.com/stepandel/cantelop/clients/cli/internal/sessionimage"
)

// sdkVersionBefore reports whether version is a release before minimum. A
// prerelease sorts before its release; an unparseable version is not flagged.
func sdkVersionBefore(version, minimum string) bool {
	parse := func(value string) ([3]int, bool, bool) {
		core, suffix, _ := strings.Cut(strings.SplitN(value, "+", 2)[0], "-")
		parts := strings.Split(core, ".")
		var numbers [3]int
		if len(parts) != 3 {
			return numbers, false, false
		}
		for i, part := range parts {
			number, err := strconv.Atoi(part)
			if err != nil || number < 0 {
				return numbers, false, false
			}
			numbers[i] = number
		}
		return numbers, suffix != "", true
	}
	have, prerelease, ok := parse(version)
	want, _, wantOK := parse(minimum)
	if !ok || !wantOK {
		return false
	}
	for i := range have {
		if have[i] != want[i] {
			return have[i] < want[i]
		}
	}
	return prerelease
}

const sdkDoctorProgram = `
import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
const sdk = await import("@cantelop/sdk/build");
const buildFile = fileURLToPath(import.meta.resolve("@cantelop/sdk/build"));
const manifest = JSON.parse(await readFile(resolve(dirname(buildFile), "..", "package.json"), "utf8"));
process.stdout.write(JSON.stringify({
  version: manifest.version,
  protocol: sdk.CANTELOP_CLI_BUILD_PROTOCOL_VERSION,
  localDatabaseProtocol: sdk.CANTELOP_LOCAL_DATABASE_PROTOCOL_VERSION,
  buildApi: typeof sdk.buildApi === "function" || typeof sdk.buildAppArtifacts === "function",
  buildLocalApi: typeof sdk.buildLocalApi === "function" || typeof sdk.buildEdgeApi === "function",
  buildSessionRuntime: typeof sdk.buildSessionRuntime === "function",
  watchLocalProject: typeof sdk.watchLocalProject === "function",
}));
`

type doctorStatus string

const (
	doctorPass doctorStatus = "pass"
	doctorFail doctorStatus = "fail"
	doctorSkip doctorStatus = "skip"
	doctorWarn doctorStatus = "warn"
)

type doctorFinding struct {
	Status doctorStatus
	Name   string
	Detail string
}

type sdkDoctorResult struct {
	Version               string `json:"version"`
	Protocol              int    `json:"protocol"`
	LocalDatabaseProtocol int    `json:"localDatabaseProtocol"`
	BuildAPI              bool   `json:"buildApi"`
	BuildLocalAPI         bool   `json:"buildLocalApi"`
	BuildSessionRuntime   bool   `json:"buildSessionRuntime"`
	WatchLocalProject     bool   `json:"watchLocalProject"`
}

type doctorFailure struct{ failures int }

func (failure doctorFailure) Error() string {
	return fmt.Sprintf("doctor found %d problem(s)", failure.failures)
}

func runDoctor(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	const usage = "usage: cantelop doctor [--app name] [-config cantelop.json]"
	flags := flag.NewFlagSet("cantelop doctor", flag.ContinueOnError)
	configPath := flags.String("config", projectconfig.DefaultPath, "project configuration path")
	app := flags.String("app", "", "App name")
	if err := parseFlags(flags, args, usage); err != nil {
		return err
	}
	if strings.TrimSpace(*configPath) == "" {
		return errors.New(usage)
	}
	if client == nil {
		client = http.DefaultClient
	}

	absoluteConfig, err := filepath.Abs(*configPath)
	if err != nil {
		absoluteConfig = *configPath
	}
	projectRoot := filepath.Dir(absoluteConfig)
	findings := make([]doctorFinding, 0, 10)

	nodeAvailable := false
	if path, lookupErr := exec.LookPath("node"); lookupErr != nil {
		findings = append(findings, doctorFinding{doctorFail, "Node.js", "not found on PATH (Node.js 22 or newer is required)"})
	} else {
		document, commandErr := doctorCommand(ctx, projectRoot, "node", "--version")
		major, versionErr := majorVersion(document)
		switch {
		case commandErr != nil:
			findings = append(findings, doctorFinding{doctorFail, "Node.js", commandDetail(commandErr, document)})
		case versionErr != nil || major < 22:
			findings = append(findings, doctorFinding{doctorFail, "Node.js", fmt.Sprintf("%s at %s; Node.js 22 or newer is required", cleanDetail(document), path)})
		default:
			nodeAvailable = true
			findings = append(findings, doctorFinding{doctorPass, "Node.js", fmt.Sprintf("%s at %s", cleanDetail(document), path)})
		}
	}

	if path, lookupErr := exec.LookPath("bun"); lookupErr != nil {
		findings = append(findings, doctorFinding{doctorFail, "Bun", "not found on PATH (native development requires Bun " + sessionimage.LocalBunVersion + ")"})
	} else {
		document, commandErr := doctorCommand(ctx, projectRoot, "bun", "--version")
		if commandErr != nil {
			findings = append(findings, doctorFinding{doctorFail, "Bun", commandDetail(commandErr, document)})
		} else if cleanDetail(document) != sessionimage.LocalBunVersion {
			findings = append(findings, doctorFinding{doctorFail, "Bun", fmt.Sprintf("%s at %s; version %s is required", cleanDetail(document), path, sessionimage.LocalBunVersion)})
		} else {
			findings = append(findings, doctorFinding{doctorPass, "Bun", fmt.Sprintf("%s at %s", cleanDetail(document), path)})
		}
	}

	dockerAvailable := false
	if path, lookupErr := exec.LookPath("docker"); lookupErr != nil {
		findings = append(findings, doctorFinding{doctorFail, "Docker CLI", "not found on PATH"})
	} else {
		document, commandErr := doctorCommand(ctx, projectRoot, "docker", "--version")
		if commandErr != nil {
			findings = append(findings, doctorFinding{doctorFail, "Docker CLI", commandDetail(commandErr, document)})
		} else {
			dockerAvailable = true
			findings = append(findings, doctorFinding{doctorPass, "Docker CLI", fmt.Sprintf("%s at %s", cleanDetail(document), path)})
		}
	}

	dockerDaemon := ""
	if !dockerAvailable {
		findings = append(findings,
			doctorFinding{doctorSkip, "Docker daemon", "Docker CLI is unavailable"},
			doctorFinding{doctorSkip, "Docker architecture", "Docker daemon is unavailable"},
		)
	} else {
		document, commandErr := doctorCommand(ctx, projectRoot, "docker", "info", "--format", "{{.OSType}}/{{.Architecture}}")
		if commandErr != nil || cleanDetail(document) == "" {
			findings = append(findings,
				doctorFinding{doctorFail, "Docker daemon", commandDetail(commandErr, document)},
				doctorFinding{doctorSkip, "Docker architecture", "Docker daemon is unavailable"},
			)
		} else {
			dockerDaemon = cleanDetail(document)
			findings = append(findings, doctorFinding{doctorPass, "Docker daemon", dockerDaemon})
			if dockerDaemon == "linux/amd64" {
				findings = append(findings, doctorFinding{doctorPass, "Docker architecture", "linux/amd64 is native"})
			} else {
				platforms, inspectErr := doctorCommand(ctx, projectRoot, "docker", "buildx", "inspect")
				if inspectErr != nil || !strings.Contains(platforms, "linux/amd64") {
					findings = append(findings, doctorFinding{doctorFail, "Docker architecture", "linux/amd64 is not advertised by the current builder"})
				} else {
					findings = append(findings, doctorFinding{doctorPass, "Docker architecture", "current builder advertises linux/amd64"})
				}
			}
		}
	}

	config, projectErr := projectconfig.LoadSelected(*configPath, *app)
	if projectErr != nil {
		findings = append(findings, doctorFinding{doctorFail, "Project files", projectErr.Error()})
	} else {
		findings = append(findings, doctorFinding{doctorPass, "Project files", "cantelop.json and declared entrypoints are readable"})
	}

	if !nodeAvailable {
		findings = append(findings, doctorFinding{doctorSkip, "CLI/SDK compatibility", "Node.js is unavailable"})
	} else {
		document, probeErr := doctorCommand(ctx, projectRoot, "node", "--input-type=module", "--eval", sdkDoctorProgram)
		var probe sdkDoctorResult
		if detail := commandDetail(probeErr, document); probeErr != nil && strings.Contains(detail, "ERR_MODULE_NOT_FOUND") &&
			strings.Contains(detail, "Cannot find package '@cantelop/sdk'") {
			findings = append(findings, doctorFinding{doctorFail, "CLI/SDK compatibility",
				"@cantelop/sdk is not installed in this project; run npm install @cantelop/sdk@latest"})
		} else if probeErr != nil || json.Unmarshal([]byte(document), &probe) != nil {
			findings = append(findings, doctorFinding{doctorFail, "CLI/SDK compatibility", "cannot load project-installed @cantelop/sdk/build: " + detail})
		} else if !slices.Contains(projectconfig.SupportedSDKBuildProtocolVersions, probe.Protocol) || !probe.BuildAPI || !probe.BuildLocalAPI ||
			!probe.BuildSessionRuntime || !probe.WatchLocalProject {
			remedy := "is too old for this CLI; run npm install @cantelop/sdk@latest"
			if probe.Protocol > slices.Max(projectconfig.SupportedSDKBuildProtocolVersions) {
				remedy = "is newer than this CLI supports; upgrade the Cantelop CLI"
			}
			findings = append(findings, doctorFinding{doctorFail, "CLI/SDK compatibility",
				fmt.Sprintf("@cantelop/sdk %s %s", probe.Version, remedy)})
		} else if probe.LocalDatabaseProtocol != 1 {
			findings = append(findings, doctorFinding{doctorFail, "CLI/SDK compatibility", "Local Workspace databases require a newer @cantelop/sdk; run npm install @cantelop/sdk@latest"})
		} else if sdkVersionBefore(probe.Version, projectconfig.ApplicationMailboxMinimumSDKVersion) {
			findings = append(findings, doctorFinding{doctorWarn, "CLI/SDK compatibility",
				fmt.Sprintf("@cantelop/sdk %s, build protocol %d, %s", probe.Version, probe.Protocol, projectconfig.ApplicationMailboxSDKWarning)})
		} else {
			findings = append(findings, doctorFinding{doctorPass, "CLI/SDK compatibility",
				fmt.Sprintf("@cantelop/sdk %s, build protocol %d", probe.Version, probe.Protocol)})
		}
	}

	credentials, credentialErr := auth.LoadCredentials(auth.CredentialsPath())
	var token controlapi.TokenResponse
	if credentialErr != nil {
		findings = append(findings, doctorFinding{doctorFail, "Authentication", credentialErr.Error()})
	} else {
		requestContext, cancel := context.WithTimeout(ctx, 10*time.Second)
		token, credentialErr = controlapi.ExchangeTenantToken(requestContext, client, credentials)
		cancel()
		if credentialErr != nil {
			findings = append(findings, doctorFinding{doctorFail, "Authentication", "stored login is not usable: " + credentialErr.Error()})
		} else {
			findings = append(findings, doctorFinding{doctorPass, "Authentication", "logged in to " + credentials.ConsoleURL})
		}
	}

	appID := ""
	if projectErr == nil && credentialErr == nil {
		requestContext, cancel := context.WithTimeout(ctx, 10*time.Second)
		appID, err = controlapi.ResolveAppID(requestContext, &controlapi.Session{
			Client: client, Credentials: credentials, Token: token,
			ExpiresAt: time.Now().Add(time.Duration(token.ExpiresIn-10) * time.Second),
		}, config.AppSlug)
		cancel()
		if err != nil {
			findings = append(findings, doctorFinding{doctorFail, "App target", err.Error()})
		} else {
			findings = append(findings, doctorFinding{doctorPass, "App target", fmt.Sprintf("%s (%s)", config.AppSlug, appID)})
		}
	} else if projectErr != nil {
		findings = append(findings, doctorFinding{doctorSkip, "App target", "project configuration is invalid"})
	} else {
		findings = append(findings, doctorFinding{doctorSkip, "App target", "live authentication is required to resolve the App slug"})
	}

	switch {
	case projectErr != nil:
		findings = append(findings, doctorFinding{doctorSkip, "App configuration", "project configuration is invalid"})
	case !hasRequiredProjectConfiguration(config):
		findings = append(findings, doctorFinding{doctorPass, "App configuration", "no required variables or secrets are declared"})
	case credentialErr != nil:
		findings = append(findings, doctorFinding{doctorSkip, "App configuration", "live authentication is required to inspect App configuration"})
	case appID == "":
		findings = append(findings, doctorFinding{doctorSkip, "App configuration", "resolve the App target first"})
	default:
		session := &controlapi.Session{
			Client: client, Credentials: credentials, Token: token,
			ExpiresAt: time.Now().Add(time.Duration(token.ExpiresIn-10) * time.Second),
		}
		var page controlapi.ConfigValuePage
		requestContext, cancel := context.WithTimeout(ctx, 10*time.Second)
		configurationErr := session.Request(requestContext, http.MethodGet, "/v1/apps/"+appID+"/config", "", nil, &page)
		cancel()
		if configurationErr != nil {
			findings = append(findings, doctorFinding{doctorFail, "App configuration", "cannot inspect declared configuration: " + configurationErr.Error()})
		} else {
			missingVariables, missingSecrets := missingProjectConfiguration(config, page)
			if len(missingVariables) == 0 && len(missingSecrets) == 0 {
				findings = append(findings, doctorFinding{doctorPass, "App configuration", "all required configuration is available"})
			} else {
				findings = append(findings, doctorFinding{doctorFail, "App configuration", missingConfigurationDetail(missingVariables, missingSecrets)})
			}
		}
	}

	failures := 0
	for _, finding := range findings {
		_, _ = fmt.Fprintf(output, "[%s] %s: %s\n", finding.Status, finding.Name, finding.Detail)
		if finding.Status == doctorFail {
			failures++
		}
	}
	if failures != 0 {
		return doctorFailure{failures: failures}
	}
	_, _ = fmt.Fprintln(output, "Cantelop doctor found no problems.")
	return nil
}

func doctorCommand(ctx context.Context, directory, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = directory
	document, err := command.CombinedOutput()
	return string(document), err
}

func majorVersion(document string) (int, error) {
	value := strings.TrimPrefix(cleanDetail(document), "v")
	major, _, _ := strings.Cut(value, ".")
	return strconv.Atoi(major)
}

func cleanDetail(document string) string {
	detail := strings.Join(strings.Fields(strings.TrimSpace(document)), " ")
	if len(detail) > 400 {
		return detail[:400] + "…"
	}
	return detail
}

func commandDetail(err error, document string) string {
	if detail := cleanDetail(document); detail != "" {
		return detail
	}
	if err != nil {
		return err.Error()
	}
	return "command returned no output"
}

func missingProjectConfiguration(config projectconfig.Config, page controlapi.ConfigValuePage) ([]string, []string) {
	configuredVariables := make(map[string]bool)
	configuredSecrets := make(map[string]bool)
	for _, item := range page.Items {
		switch item.Kind {
		case "variable":
			configuredVariables[item.Name] = item.Value != nil
		case "secret":
			configuredSecrets[item.Name] = item.Value == nil
		}
	}
	missingVariables := make([]string, 0)
	missingSecrets := make([]string, 0)
	for name, declaration := range config.Environment {
		if !declaration.Required {
			continue
		}
		if declaration.Secret {
			if !configuredSecrets[name] {
				missingSecrets = append(missingSecrets, name)
			}
		} else if !configuredVariables[name] {
			missingVariables = append(missingVariables, name)
		}
	}
	sort.Strings(missingVariables)
	sort.Strings(missingSecrets)
	return missingVariables, missingSecrets
}

func hasRequiredProjectConfiguration(config projectconfig.Config) bool {
	for _, declaration := range config.Environment {
		if declaration.Required {
			return true
		}
	}
	return false
}

func missingConfigurationDetail(variables, secrets []string) string {
	parts := make([]string, 0, 2)
	if len(variables) != 0 {
		parts = append(parts, "missing variables: "+strings.Join(variables, ", "))
	}
	if len(secrets) != 0 {
		parts = append(parts, "missing secrets: "+strings.Join(secrets, ", "))
	}
	return strings.Join(parts, "; ")
}

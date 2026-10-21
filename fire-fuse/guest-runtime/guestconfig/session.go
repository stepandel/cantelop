package guestconfig

import "strings"

// Session images use this versioned contract. Unversioned catalog images keep
// their original process contract until rebuilt with the CLI.
const (
	SessionContractEnvironment = "CANTELOP_RUNTIME_CONTRACT"
	SessionContractVersion     = "1"
	SessionUID                 = 10001
	SessionGID                 = 10001
	SessionWorkspace           = "/workspace"
	SessionHome                = "/__cantelop/home"
	SessionTemp                = "/__cantelop/tmp"
	SessionModule              = "/opt/cantelop/session-runtime.mjs"
	SessionLoader              = "/__cantelop/runtime/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2"
	SessionLibraryPath         = "/__cantelop/runtime/lib/x86_64-linux-gnu:/__cantelop/runtime/usr/lib/x86_64-linux-gnu"
)

// SessionEnvironment is applied after image and App environment defaults.
func SessionEnvironment() []string {
	return []string{
		SessionContractEnvironment + "=" + SessionContractVersion,
		"HOME=" + SessionHome,
		"USER=cantelop",
		"LOGNAME=cantelop",
		"TMPDIR=" + SessionTemp,
		"TMP=" + SessionTemp,
		"TEMP=" + SessionTemp,
		"XDG_CACHE_HOME=" + SessionHome + "/.cache",
		"XDG_CONFIG_HOME=" + SessionHome + "/.config",
		"XDG_DATA_HOME=" + SessionHome + "/.local/share",
		"XDG_STATE_HOME=" + SessionHome + "/.local/state",
	}
}

func (p Process) SessionContract() (string, bool) {
	for _, entry := range p.Env {
		if value, found := strings.CutPrefix(entry, SessionContractEnvironment+"="); found {
			return value, true
		}
	}
	return "", false
}

// MergeProcessEnvironment keeps managed runtime defaults authoritative while
// retaining the historical behavior of already-published, unversioned Builds.
func MergeProcessEnvironment(process Process, overrides []string) ([]string, error) {
	merged, err := MergeEnvironment(process.Env, overrides)
	if err != nil {
		return nil, err
	}
	if version, managed := process.SessionContract(); managed {
		if version != SessionContractVersion {
			return nil, invalid("unsupported Session runtime contract")
		}
		return MergeEnvironment(merged, SessionEnvironment())
	}
	return merged, nil
}

func (c Config) validateSessionContract() error {
	version, managed := c.Process.SessionContract()
	if !managed {
		return nil
	}
	if version != SessionContractVersion {
		return invalid("unsupported Session runtime contract")
	}
	if c.Process.UID != SessionUID || c.Process.GID != SessionGID {
		return invalid("Session runtime identity must be 10001:10001")
	}
	if c.Process.WorkingDirectory != SessionWorkspace {
		return invalid("Session runtime working directory must be /workspace")
	}
	if len(c.Process.Args) != 2 || c.Process.Args[0] != PlatformRuntimeContainerPath || c.Process.Args[1] != SessionModule {
		return invalid("Session runtime must use the platform Bun and bundled Session module")
	}
	if c.Workspace.Enabled && c.Workspace.ContainerPath != SessionWorkspace {
		return invalid("Session workspace must be mounted at /workspace")
	}
	values := make(map[string]string)
	for _, entry := range c.Process.Env {
		name, value, _ := strings.Cut(entry, "=")
		values[name] = value
	}
	for _, entry := range SessionEnvironment() {
		name, value, _ := strings.Cut(entry, "=")
		if values[name] != value {
			return invalid("Session runtime owns environment variable " + name)
		}
	}
	return nil
}

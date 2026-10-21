// Package sessionimage builds the managed Session runtime image layers that
// local container development and deployment share.
package sessionimage

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/stepandel/cantelop/clients/cli/internal/ids"
	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestconfig"
)

const sessionAssetsDirectory = ".cantelop-runtime"

const LocalWorkspaceBootstrap = "// Share newly created local files with the host group.\nprocess.umask(0o002);\n"

const SessionRuntimeImage = "oven/bun:" + LocalBunVersion

func SessionRuntimeLibrariesDockerfile() string {
	return "COPY --from=cantelop_runtime /lib /__cantelop/runtime/lib\n" +
		"COPY --from=cantelop_runtime /usr/lib /__cantelop/runtime/usr/lib\n"
}

// This layer is shared by local Docker and deployment builds. It requires no
// shell, package manager, or account-management utilities in the customer image.
func ManagedSessionDockerfile() string {
	var layer strings.Builder
	layer.WriteString("COPY --chown=0:0 --chmod=0755 " + sessionAssetsDirectory + "/root/ /\n")
	layer.WriteString("COPY --chown=0:0 --chmod=0644 " + sessionAssetsDirectory + "/passwd " + sessionAssetsDirectory + "/group /etc/\n")
	for _, directory := range []string{"home", "tmp"} {
		fmt.Fprintf(&layer, "COPY --chown=%d:%d %s/%s/ /__cantelop/%s/\n", guestconfig.SessionUID, guestconfig.SessionGID, sessionAssetsDirectory, directory, directory)
	}
	layer.WriteString("COPY --chown=0:0 --chmod=0644 session-runtime.mjs " + guestconfig.SessionModule + "\n")
	for _, entry := range guestconfig.SessionEnvironment() {
		name, value, _ := strings.Cut(entry, "=")
		fmt.Fprintf(&layer, "ENV %s=%s\n", name, strconv.Quote(value))
	}
	fmt.Fprintf(&layer, "USER %d:%d\nWORKDIR %s\nHEALTHCHECK NONE\nSTOPSIGNAL SIGTERM\nENTRYPOINT []\n", guestconfig.SessionUID, guestconfig.SessionGID, guestconfig.SessionWorkspace)
	return layer.String()
}

type sessionBaseMetadata struct {
	Architecture string
	OS           string
	Config       struct {
		Volumes map[string]json.RawMessage
		OnBuild []string
		Env     []string
	}
}

func validateSessionBaseMetadata(document []byte) error {
	var images []sessionBaseMetadata
	if len(document) > 1<<20 || json.Unmarshal(document, &images) != nil || len(images) != 1 {
		return errors.New("invalid Session base image metadata")
	}
	metadata := images[0]
	if metadata.OS != "linux" || metadata.Architecture != "amd64" {
		return errors.New("Session base image must target linux/amd64")
	}
	if len(metadata.Config.Volumes) != 0 {
		return errors.New("Session base image declares VOLUME; Cantelop owns mounts and supplies durable storage at /workspace")
	}
	if len(metadata.Config.OnBuild) != 0 {
		return errors.New("Session base image contains pending ONBUILD instructions; complete dependency installation in the customer Dockerfile")
	}
	for _, entry := range metadata.Config.Env {
		if strings.HasPrefix(entry, "CANTELOP_") {
			name, _, _ := strings.Cut(entry, "=")
			return fmt.Errorf("Session base image sets reserved environment variable %s; Cantelop supplies runtime configuration", name)
		}
	}
	return nil
}

// Exporting a never-started container lets us merge account files and inspect
// reserved paths without executing any customer code. Supply an explicit dummy
// entrypoint: a dependency-only image need not declare a command at all.
func PrepareManagedSessionImage(ctx context.Context, image, artifactDirectory string) error {
	inspect := exec.CommandContext(ctx, "docker", "image", "inspect", image)
	document, err := inspect.Output()
	if err != nil {
		return fmt.Errorf("inspect Session base image: %w", err)
	}
	if err := validateSessionBaseMetadata(document); err != nil {
		return err
	}
	create := exec.CommandContext(ctx, "docker", "create", "--network", "none", "--entrypoint", "/__cantelop/inspect-only", image)
	output, err := create.Output()
	if err != nil {
		return fmt.Errorf("create Session image inspection container: %w", err)
	}
	containerID := strings.TrimSpace(string(output))
	if !ids.LocalContainerIDPattern.MatchString(containerID) {
		return errors.New("Docker returned an invalid Session image inspection container ID")
	}
	defer func() {
		remove := exec.Command("docker", "container", "rm", "--force", "--volumes", containerID)
		_ = remove.Run()
	}()
	export := exec.CommandContext(ctx, "docker", "export", containerID)
	archive, err := export.StdoutPipe()
	if err != nil {
		return err
	}
	if err := export.Start(); err != nil {
		return fmt.Errorf("export Session base image: %w", err)
	}
	base, readErr := inspectSessionBaseArchive(archive)
	if readErr != nil {
		_ = archive.Close()
	}
	waitErr := export.Wait()
	if readErr != nil {
		return readErr
	}
	if waitErr != nil {
		return fmt.Errorf("export Session base image: %w", waitErr)
	}
	return writeSessionAssets(artifactDirectory, base)
}

type sessionBaseFiles struct {
	passwd string
	group  string
}

func inspectSessionBaseArchive(reader io.Reader) (sessionBaseFiles, error) {
	var base sessionBaseFiles
	archive := tar.NewReader(reader)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return base, fmt.Errorf("inspect Session filesystem: %w", err)
		}
		name := path.Clean(strings.TrimPrefix(header.Name, "./"))
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return base, errors.New("invalid path in Session image export")
		}
		if name == "__cantelop" || strings.HasPrefix(name, "__cantelop/") || name == "opt/cantelop" || strings.HasPrefix(name, "opt/cantelop/") {
			return base, fmt.Errorf("Session base image contains reserved path /%s; Cantelop supplies its runtime files", name)
		}
		if name == "etc" || name == "opt" || name == "workspace" {
			if header.Typeflag != tar.TypeDir {
				return base, fmt.Errorf("Session base image /%s must be a directory", name)
			}
		}
		if strings.HasPrefix(name, "workspace/") {
			return base, errors.New("Session base image contains files under /workspace that the durable mount would hide; install dependencies and assets elsewhere")
		}
		switch name {
		case "etc/passwd", "etc/group":
			if header.Typeflag != tar.TypeReg || header.Size > 1<<20 {
				return base, fmt.Errorf("Session base image /%s must be a regular account file no larger than 1 MiB", name)
			}
			document, err := io.ReadAll(archive)
			if err != nil {
				return base, err
			}
			if name == "etc/passwd" {
				base.passwd = string(document)
			} else {
				base.group = string(document)
			}
		}
	}
	return base, nil
}

func writeSessionAssets(artifactDirectory string, base sessionBaseFiles) error {
	root := filepath.Join(artifactDirectory, sessionAssetsDirectory)
	for _, directory := range []string{"root/etc", "root/opt/cantelop", "root/__cantelop/bin", "root/workspace", "home/.cache", "home/.config", "home/.local/share", "home/.local/state", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			return err
		}
	}
	accounts := map[string]string{
		"passwd":              managedAccountFile(base.passwd, "cantelop:x:10001:10001:Cantelop Session:"+guestconfig.SessionHome+":/bin/sh"),
		"group":               managedAccountFile(base.group, "cantelop:x:10001:"),
		"local-workspace.mjs": LocalWorkspaceBootstrap,
	}
	for name, contents := range accounts {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func managedAccountFile(original, managed string) string {
	var result strings.Builder
	for _, line := range strings.Split(original, "\n") {
		fields := strings.Split(line, ":")
		// Reserve the managed name and numeric identity while retaining all
		// unrelated system accounts from the customer's distribution.
		if len(fields) >= 3 && (fields[0] == "cantelop" || fields[2] == "10001") {
			continue
		}
		if line != "" {
			result.WriteString(line + "\n")
		}
	}
	result.WriteString(managed + "\n")
	return result.String()
}

const (
	LocalBunVersion = "1.3.10"
)

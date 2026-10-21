package materializer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestconfig"
	"golang.org/x/sys/unix"
)

const (
	minimumRootFSBytes           int64 = 256 << 20
	deterministicBuildUnixSecond int64 = 946684800
	// An ext4 directory entry is an 8-byte header plus the name padded to 4
	// bytes. Every path, including each hard link, needs one.
	rootFSDirectoryEntryHeaderBytes int64 = 8
	// The largest estimate an extracted rootfs can produce. Deriving it from the
	// extraction limits keeps the image cap from rejecting extractable trees.
	maximumRootFSDataBytes  = maxExtractedRootFSBytes + maxExtractedRootFSEntries*(rootFSDirectoryEntryHeaderBytes+256)
	maximumRootFSImageBytes = maximumRootFSDataBytes + maximumRootFSDataBytes/4 + 64<<20
)

var containerIDPattern = regexp.MustCompile(`^[0-9a-f]{12,64}$`)

type retryableError struct{ err error }

func (e retryableError) Error() string   { return e.err.Error() }
func (e retryableError) Unwrap() error   { return e.err }
func (e retryableError) Retryable() bool { return true }

func retryable(label string, err error) error {
	return retryableError{err: fmt.Errorf("%s: %w", label, err)}
}

type Runner interface {
	Run(context.Context, []string, string, ...string) ([]byte, error)
}

type Publisher interface {
	Publish(config.ContainerImage) (string, error)
}

type Materializer struct {
	dockerBin      string
	mkfsBin        string
	workDirectory  string
	imageDirectory string
	runner         Runner
	publisher      Publisher
}

func New(dockerBin, mkfsBin, workDirectory, imageDirectory string, publisher Publisher) (*Materializer, error) {
	for label, value := range map[string]string{"Docker binary": dockerBin, "mkfs.ext4 binary": mkfsBin, "Build work directory": workDirectory, "image directory": imageDirectory} {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value || value == string(filepath.Separator) {
			return nil, fmt.Errorf("%s must be a clean absolute path", label)
		}
	}
	if publisher == nil {
		return nil, errors.New("Build publisher is required")
	}
	for _, directory := range []string{workDirectory, imageDirectory} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, err
		}
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("Build directories must be non-symlink directories")
		}
	}
	return &Materializer{dockerBin: dockerBin, mkfsBin: mkfsBin, workDirectory: workDirectory, imageDirectory: imageDirectory, runner: commandRunner{}, publisher: publisher}, nil
}

func (m *Materializer) Materialize(ctx context.Context, operationID, archivePath string) (buildID string, resultErr error) {
	imageID, err := inspectDockerArchive(archivePath)
	if err != nil {
		return "", err
	}
	workspace, err := os.MkdirTemp(m.workDirectory, operationID+"-")
	if err != nil {
		return "", retryable("create Build workspace", err)
	}
	defer func() {
		if cleanupErr := os.RemoveAll(workspace); cleanupErr != nil {
			resultErr = errors.Join(resultErr, retryable("remove Build workspace", cleanupErr))
		}
	}()
	if _, err := m.runner.Run(ctx, nil, m.dockerBin, "image", "load", "--input", archivePath); err != nil {
		if dockerUnavailable(err) {
			return "", retryable("load Docker archive", err)
		}
		return "", fmt.Errorf("load Docker archive: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, cleanupErr := m.runner.Run(cleanupCtx, nil, m.dockerBin, "image", "rm", "--force", imageID)
		if cleanupErr != nil {
			resultErr = errors.Join(resultErr, retryable("remove loaded Docker image", cleanupErr))
		}
	}()
	inspectDocument, err := m.runner.Run(ctx, nil, m.dockerBin, "image", "inspect", imageID)
	if err != nil {
		return "", retryable("inspect loaded Docker image", err)
	}
	metadata, process, err := decodeDockerImage(inspectDocument)
	if err != nil {
		return "", err
	}
	containerDocument, err := m.runner.Run(ctx, nil, m.dockerBin, "create", "--network", "none", imageID)
	if err != nil {
		return "", retryable("create non-running Docker container", err)
	}
	containerID := strings.TrimSpace(string(containerDocument))
	if !containerIDPattern.MatchString(containerID) {
		return "", errors.New("Docker returned an invalid container ID")
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, cleanupErr := m.runner.Run(cleanupCtx, nil, m.dockerBin, "rm", "--force", "--volumes", containerID)
		if cleanupErr != nil {
			resultErr = errors.Join(resultErr, retryable("remove materialization container", cleanupErr))
		}
	}()
	exportPath := filepath.Join(workspace, "rootfs.tar")
	if _, err := m.runner.Run(ctx, nil, m.dockerBin, "export", "--output", exportPath, containerID); err != nil {
		return "", retryable("export Docker rootfs", err)
	}
	root := filepath.Join(workspace, "rootfs")
	if err := os.Mkdir(root, 0o755); err != nil {
		return "", err
	}
	if info, err := os.Stat(exportPath); err != nil || info.Size() > maxExtractedRootFSBytes {
		return "", errors.New("exported rootfs archive exceeds size limit")
	}
	if err := extractRootFS(ctx, exportPath, root); err != nil {
		return "", err
	}
	process.UID, process.GID, err = resolveProcessIdentity(root, metadata.User)
	if err != nil {
		return "", err
	}
	if process.UID == 0 || process.GID == 0 {
		return "", errors.New("Docker image must configure a non-root user and primary group")
	}
	if err := (guestconfig.Config{SchemaVersion: guestconfig.SchemaVersion, Process: process}).Validate(); err != nil {
		return "", fmt.Errorf("validate Docker process contract: %w", err)
	}
	imageBytes, err := rootFSImageSize(root)
	if err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(m.imageDirectory, ".build-rootfs-*.ext4")
	if err != nil {
		return "", retryable("create rootfs image", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if err := temporary.Truncate(imageBytes); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	treeDigest, err := rootFSTreeDigest(root)
	if err != nil {
		return "", retryable("derive rootfs tree identity", err)
	}
	uuid := fmt.Sprintf("%s-%s-%s-%s-%s", treeDigest[0:8], treeDigest[8:12], treeDigest[12:16], treeDigest[16:20], treeDigest[20:32])
	features := "hash_seed=" + uuid + ",lazy_itable_init=0,lazy_journal_init=0"
	// Normalize only after all identity and sizing reads so their atime updates
	// cannot leak host wall-clock time into mkfs.ext4's source tree.
	if err := normalizeRootFSTimestamps(root); err != nil {
		return "", retryable("normalize rootfs timestamps", err)
	}
	if _, err := m.runner.Run(ctx, []string{"E2FSPROGS_FAKE_TIME=" + strconv.FormatInt(deterministicBuildUnixSecond, 10)}, m.mkfsBin, "-q", "-F", "-U", uuid, "-E", features, "-d", root, temporaryPath); err != nil {
		return "", retryable("create ext4 rootfs", err)
	}
	digest, size, err := hashFile(temporaryPath)
	if err != nil {
		return "", retryable("hash materialized rootfs", err)
	}
	// Each conversion owns its local path. Equal-content conversions still have
	// the same Build identity, but one request's cleanup cannot unlink a peer's
	// upload or verification source.
	var installedPath string
	installed := false
	for attempt := 0; attempt < 3; attempt++ {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", retryable("name rootfs image", err)
		}
		installedPath = filepath.Join(m.imageDirectory, "sha256-"+digest+"-"+hex.EncodeToString(nonce[:])+".ext4")
		if err := installRootFS(temporaryPath, installedPath); errors.Is(err, os.ErrExist) {
			continue
		} else if err != nil {
			return "", retryable("install immutable rootfs", err)
		}
		installed = true
		break
	}
	if !installed {
		return "", retryable("install immutable rootfs", os.ErrExist)
	}
	image := config.ContainerImage{RootFSImage: installedPath, RootFSSHA256: digest, RootFSSizeBytes: size, Process: process}
	buildID, err = m.publisher.Publish(image)
	if err != nil {
		return "", retryable("publish immutable Build", err)
	}
	return buildID, nil
}

func dockerUnavailable(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "cannot connect to the docker daemon") ||
		strings.Contains(message, "connection refused") || strings.Contains(message, "context deadline exceeded")
}

type dockerInspect struct {
	Architecture string `json:"Architecture"`
	OS           string `json:"Os"`
	Config       struct {
		Entrypoint []string                   `json:"Entrypoint"`
		Cmd        []string                   `json:"Cmd"`
		Env        []string                   `json:"Env"`
		WorkingDir string                     `json:"WorkingDir"`
		User       string                     `json:"User"`
		Volumes    map[string]json.RawMessage `json:"Volumes"`
		OnBuild    []string                   `json:"OnBuild"`
	} `json:"Config"`
}

type dockerMetadata struct{ User string }

func decodeDockerImage(document []byte) (dockerMetadata, guestconfig.Process, error) {
	var images []dockerInspect
	if len(document) > 1<<20 || json.Unmarshal(document, &images) != nil || len(images) != 1 {
		return dockerMetadata{}, guestconfig.Process{}, errors.New("Docker image inspection response is invalid")
	}
	image := images[0]
	if image.OS != "linux" || image.Architecture != "amd64" {
		return dockerMetadata{}, guestconfig.Process{}, errors.New("Docker image must target linux/amd64")
	}
	arguments := append(append([]string{}, image.Config.Entrypoint...), image.Config.Cmd...)
	if len(arguments) > 0 && arguments[0] == guestconfig.PlatformRuntimeContainerPath {
		if len(image.Config.Volumes) != 0 || len(image.Config.OnBuild) != 0 {
			return dockerMetadata{}, guestconfig.Process{}, errors.New("Session images cannot declare volumes or pending build hooks")
		}
		process := guestconfig.Process{Env: image.Config.Env}
		if version, managed := process.SessionContract(); !managed || version != guestconfig.SessionContractVersion {
			return dockerMetadata{}, guestconfig.Process{}, errors.New("rebuild the Session image with a CLI that supports the managed runtime contract")
		}
	}
	workingDirectory := image.Config.WorkingDir
	if workingDirectory == "" {
		workingDirectory = "/"
	}
	return dockerMetadata{User: image.Config.User}, guestconfig.Process{
		Args: arguments, Env: append([]string{}, image.Config.Env...), WorkingDirectory: workingDirectory,
	}, nil
}

func resolveProcessIdentity(root, user string) (uint32, uint32, error) {
	userPart, groupPart, hasGroup := strings.Cut(user, ":")
	if userPart == "" {
		userPart = "0"
	}
	uid, defaultGID, err := resolveUser(root, userPart)
	if err != nil {
		return 0, 0, err
	}
	if !hasGroup || groupPart == "" {
		return uid, defaultGID, nil
	}
	gid, err := resolveGroup(root, groupPart)
	return uid, gid, err
}

func resolveUser(root, value string) (uint32, uint32, error) {
	if numeric, err := parseUint32(value); err == nil {
		return numeric, numeric, nil
	}
	fields, err := findAccount(root, "passwd", value, 7)
	if err != nil {
		return 0, 0, fmt.Errorf("resolve Docker user: %w", err)
	}
	uid, err := parseUint32(fields[2])
	if err != nil {
		return 0, 0, err
	}
	gid, err := parseUint32(fields[3])
	return uid, gid, err
}

func resolveGroup(root, value string) (uint32, error) {
	if numeric, err := parseUint32(value); err == nil {
		return numeric, nil
	}
	fields, err := findAccount(root, "group", value, 4)
	if err != nil {
		return 0, fmt.Errorf("resolve Docker group: %w", err)
	}
	return parseUint32(fields[2])
}

func findAccount(root, accountFile, name string, fields int) ([]string, error) {
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(rootFD)
	etcFD, err := unix.Openat(rootFD, "etc", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(etcFD)
	accountFD, err := unix.Openat(etcFD, accountFile, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(accountFD), accountFile)
	if file == nil {
		_ = unix.Close(accountFD)
		return nil, errors.New("open rootfs account file")
	}
	defer file.Close()
	scanner := bufio.NewScanner(io.LimitReader(file, 1<<20))
	for scanner.Scan() {
		parts := strings.Split(scanner.Text(), ":")
		if len(parts) == fields && parts[0] == name {
			return parts, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("account %q not found", name)
}

func parseUint32(value string) (uint32, error) {
	parsed, err := strconv.ParseUint(value, 10, 32)
	return uint32(parsed), err
}

func rootFSImageSize(root string) (int64, error) {
	return rootFSImageSizeWithLimit(root, maximumRootFSImageBytes)
}

func rootFSImageSizeWithLimit(root string, maxBytes int64) (int64, error) {
	var bytes int64
	seen := make(map[fileIdentity]struct{})
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		bytes += (rootFSDirectoryEntryHeaderBytes + int64(len(entry.Name())) + 3) &^ 3
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			identity, err := rootFSFileIdentity(info)
			if err != nil {
				return err
			}
			if _, exists := seen[identity]; exists {
				return nil
			}
			seen[identity] = struct{}{}
			bytes += info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	bytes += bytes/4 + 64<<20
	if bytes > maxBytes {
		return 0, errors.New("rootfs image exceeds size limit")
	}
	if bytes < minimumRootFSBytes {
		bytes = minimumRootFSBytes
	}
	const mib = int64(1 << 20)
	return ((bytes + mib - 1) / mib) * mib, nil
}

type fileIdentity struct {
	device uint64
	inode  uint64
}

func rootFSFileIdentity(info os.FileInfo) (fileIdentity, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileIdentity{}, errors.New("rootfs file identity is unavailable")
	}
	return fileIdentity{device: uint64(stat.Dev), inode: uint64(stat.Ino)}, nil
}

func rootFSTreeDigest(root string) (string, error) {
	digest := sha256.New()
	// Hard links share one inode, so later paths reference the first path that
	// hashed its content instead of reading the same data again.
	firstPaths := make(map[fileIdentity]string)
	err := filepath.Walk(root, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, filePath)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("rootfs ownership metadata is unavailable")
		}
		_, _ = fmt.Fprintf(digest, "%s\x00%d\x00%d\x00%d\x00%d\x00", filepath.ToSlash(relative), info.Mode(), stat.Uid, stat.Gid, info.Size())
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(filePath)
			if err != nil {
				return err
			}
			_, _ = io.WriteString(digest, target)
		} else if info.Mode().IsRegular() {
			identity, err := rootFSFileIdentity(info)
			if err != nil {
				return err
			}
			if firstPath, exists := firstPaths[identity]; exists {
				_, _ = fmt.Fprintf(digest, "\x00link\x00%s", firstPath)
				_, _ = digest.Write([]byte{0})
				return nil
			}
			firstPaths[identity] = filepath.ToSlash(relative)
			file, err := os.Open(filePath)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(digest, file)
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil {
				return errors.Join(copyErr, closeErr)
			}
		}
		_, _ = digest.Write([]byte{0})
		return nil
	})
	return fmt.Sprintf("%x", digest.Sum(nil)), err
}

func hashFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	digest := sha256.New()
	size, err := io.Copy(digest, file)
	return fmt.Sprintf("%x", digest.Sum(nil)), size, err
}

func installRootFS(source, destination string) error {
	return installRootFSWithSync(source, destination, syncInstalledRootFS)
}

func installRootFSWithSync(source, destination string, syncImage func(string) error) (resultErr error) {
	if err := os.Link(source, destination); err != nil {
		return err
	}
	defer func() {
		if resultErr != nil {
			_ = os.Remove(destination)
		}
	}()
	return syncImage(destination)
}

func syncInstalledRootFS(destination string) error {
	file, err := os.Open(destination)
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil || closeErr != nil {
		return errors.Join(syncErr, closeErr)
	}
	directory, err := os.Open(filepath.Dir(destination))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

type commandRunner struct{}

func (commandRunner) Run(ctx context.Context, environment []string, name string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Env = append(os.Environ(), environment...)
	var output limitedBuffer
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %s", filepath.Base(name), err, strings.TrimSpace(output.String()))
	}
	return output.Bytes(), nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(document []byte) (int, error) {
	const maximum = 1 << 20
	remaining := maximum - b.Len()
	if remaining > 0 {
		_, _ = b.Buffer.Write(document[:min(len(document), remaining)])
	}
	return len(document), nil
}

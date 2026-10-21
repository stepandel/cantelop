package firecracker

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
)

const (
	jailedAPISocketPath        = "/run/firecracker.socket"
	jailedKernelImagePath      = "/vmlinux"
	jailedRootFSImagePath      = "/rootfs.ext4"
	jailedContainerRootFSPath  = "/container-rootfs.ext4"
	jailedVSockPath            = "/run/fire-fuse.vsock"
	jailedSnapshotDirectory    = "/snapshot"
	jailedSnapshotStatePath    = jailedSnapshotDirectory + "/vmstate"
	jailedSnapshotMemoryPath   = jailedSnapshotDirectory + "/memory"
	maxLinuxUnixSocketPathSize = 107
)

var (
	ErrInvalidJailLayout  = errors.New("invalid Firecracker jail layout")
	ErrSocketPathTooLong  = errors.New("Firecracker API socket path is too long")
	executableNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	vmIDPattern           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)
)

// JailLayout contains host-visible and jailed paths for one microVM.
type JailLayout struct {
	ChrootBaseDir            string
	ExecutableName           string
	VMID                     string
	VMDir                    string
	RootDir                  string
	ExecutableCopy           string
	PIDFile                  string
	RuntimeManifest          string
	HostAPISocket            string
	JailedAPISocket          string
	HostKernelImage          string
	JailedKernel             string
	HostRootFSImage          string
	JailedRootFS             string
	HostContainerRootFSImage string
	JailedContainerRootFS    string
	HostVSock                string
	JailedVSock              string
	HostSnapshotDirectory    string
	HostSnapshotState        string
	JailedSnapshotState      string
	HostSnapshotMemory       string
	JailedSnapshotMemory     string
}

// NewJailLayout derives the directory convention implemented by the jailer.
func NewJailLayout(chrootBaseDir, firecrackerBin, vmID string) (JailLayout, error) {
	if err := validateCleanAbsolutePath(chrootBaseDir); err != nil {
		return JailLayout{}, fmt.Errorf("%w: chroot base: %v", ErrInvalidJailLayout, err)
	}
	if chrootBaseDir == string(filepath.Separator) {
		return JailLayout{}, fmt.Errorf("%w: chroot base cannot be the filesystem root", ErrInvalidJailLayout)
	}
	if err := validateCleanAbsolutePath(firecrackerBin); err != nil {
		return JailLayout{}, fmt.Errorf("%w: Firecracker binary: %v", ErrInvalidJailLayout, err)
	}
	if firecrackerBin == string(filepath.Separator) {
		return JailLayout{}, fmt.Errorf("%w: Firecracker binary cannot be the filesystem root", ErrInvalidJailLayout)
	}
	if err := ValidateVMID(vmID); err != nil {
		return JailLayout{}, err
	}

	executableName := filepath.Base(firecrackerBin)
	if !executableNamePattern.MatchString(executableName) {
		return JailLayout{}, fmt.Errorf("%w: invalid executable name %q", ErrInvalidJailLayout, executableName)
	}
	return buildJailLayout(chrootBaseDir, executableName, vmID)
}

// ValidateVMID checks the identifier grammar used to derive jail paths.
func ValidateVMID(vmID string) error {
	if !vmIDPattern.MatchString(vmID) {
		return fmt.Errorf("%w: invalid VM ID %q", ErrInvalidJailLayout, vmID)
	}
	return nil
}

func buildJailLayout(chrootBaseDir, executableName, vmID string) (JailLayout, error) {
	vmDir := filepath.Join(chrootBaseDir, executableName, vmID)
	rootDir := filepath.Join(vmDir, "root")
	hostAPISocket := filepath.Join(rootDir, jailedAPISocketPath)
	hostVSock := filepath.Join(rootDir, jailedVSockPath)
	for name, path := range map[string]string{
		"API":   hostAPISocket,
		"vsock": hostVSock,
	} {
		if len([]byte(path)) > maxLinuxUnixSocketPathSize {
			return JailLayout{}, fmt.Errorf(
				"%w: %s path is %d bytes, exceeds %d",
				ErrSocketPathTooLong,
				name,
				len([]byte(path)),
				maxLinuxUnixSocketPathSize,
			)
		}
	}
	hostSnapshotDirectory := filepath.Join(rootDir, jailedSnapshotDirectory)

	return JailLayout{
		ChrootBaseDir:            chrootBaseDir,
		ExecutableName:           executableName,
		VMID:                     vmID,
		VMDir:                    vmDir,
		RootDir:                  rootDir,
		ExecutableCopy:           filepath.Join(rootDir, executableName),
		PIDFile:                  filepath.Join(rootDir, executableName+".pid"),
		RuntimeManifest:          filepath.Join(vmDir, runtimeManifestFilename),
		HostAPISocket:            hostAPISocket,
		JailedAPISocket:          jailedAPISocketPath,
		HostKernelImage:          filepath.Join(rootDir, jailedKernelImagePath),
		JailedKernel:             jailedKernelImagePath,
		HostRootFSImage:          filepath.Join(rootDir, jailedRootFSImagePath),
		JailedRootFS:             jailedRootFSImagePath,
		HostContainerRootFSImage: filepath.Join(rootDir, jailedContainerRootFSPath),
		JailedContainerRootFS:    jailedContainerRootFSPath,
		HostVSock:                hostVSock,
		JailedVSock:              jailedVSockPath,
		HostSnapshotDirectory:    hostSnapshotDirectory,
		HostSnapshotState:        filepath.Join(rootDir, jailedSnapshotStatePath),
		JailedSnapshotState:      jailedSnapshotStatePath,
		HostSnapshotMemory:       filepath.Join(rootDir, jailedSnapshotMemoryPath),
		JailedSnapshotMemory:     jailedSnapshotMemoryPath,
	}, nil
}

func (l JailLayout) validate() error {
	if err := validateCleanAbsolutePath(l.ChrootBaseDir); err != nil {
		return fmt.Errorf("%w: chroot base: %v", ErrInvalidJailLayout, err)
	}
	if l.ChrootBaseDir == string(filepath.Separator) {
		return fmt.Errorf("%w: chroot base cannot be the filesystem root", ErrInvalidJailLayout)
	}
	if !executableNamePattern.MatchString(l.ExecutableName) {
		return fmt.Errorf("%w: invalid executable name %q", ErrInvalidJailLayout, l.ExecutableName)
	}
	if err := ValidateVMID(l.VMID); err != nil {
		return err
	}

	expected, err := buildJailLayout(l.ChrootBaseDir, l.ExecutableName, l.VMID)
	if err != nil {
		return err
	}
	if l != expected {
		return fmt.Errorf("%w: derived paths were modified", ErrInvalidJailLayout)
	}
	return nil
}

func validateCleanAbsolutePath(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("path must be absolute")
	}
	if filepath.Clean(path) != path {
		return errors.New("path must already be clean")
	}
	return nil
}

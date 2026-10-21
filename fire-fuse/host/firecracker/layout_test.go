package firecracker

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewJailLayout(t *testing.T) {
	layout, err := NewJailLayout(
		"/var/lib/fire-fuse",
		"/usr/local/bin/firecracker",
		"vm-abc123",
	)
	if err != nil {
		t.Fatalf("NewJailLayout() error = %v", err)
	}

	wantVMDir := "/var/lib/fire-fuse/firecracker/vm-abc123"
	assertPath(t, "VMDir", layout.VMDir, wantVMDir)
	assertPath(t, "RootDir", layout.RootDir, wantVMDir+"/root")
	assertPath(t, "ExecutableCopy", layout.ExecutableCopy, wantVMDir+"/root/firecracker")
	assertPath(t, "PIDFile", layout.PIDFile, wantVMDir+"/root/firecracker.pid")
	assertPath(t, "RuntimeManifest", layout.RuntimeManifest, wantVMDir+"/fire-fuse-runtime.json")
	assertPath(t, "HostAPISocket", layout.HostAPISocket, wantVMDir+"/root/run/firecracker.socket")
	assertPath(t, "JailedAPISocket", layout.JailedAPISocket, "/run/firecracker.socket")
	assertPath(t, "HostKernelImage", layout.HostKernelImage, wantVMDir+"/root/vmlinux")
	assertPath(t, "JailedKernel", layout.JailedKernel, "/vmlinux")
	assertPath(t, "HostRootFSImage", layout.HostRootFSImage, wantVMDir+"/root/rootfs.ext4")
	assertPath(t, "JailedRootFS", layout.JailedRootFS, "/rootfs.ext4")
	assertPath(
		t,
		"HostContainerRootFSImage",
		layout.HostContainerRootFSImage,
		wantVMDir+"/root/container-rootfs.ext4",
	)
	assertPath(t, "JailedContainerRootFS", layout.JailedContainerRootFS, "/container-rootfs.ext4")
	assertPath(t, "HostVSock", layout.HostVSock, wantVMDir+"/root/run/fire-fuse.vsock")
	assertPath(t, "JailedVSock", layout.JailedVSock, "/run/fire-fuse.vsock")
	assertPath(t, "HostSnapshotDirectory", layout.HostSnapshotDirectory, wantVMDir+"/root/snapshot")
	assertPath(t, "HostSnapshotState", layout.HostSnapshotState, wantVMDir+"/root/snapshot/vmstate")
	assertPath(t, "JailedSnapshotState", layout.JailedSnapshotState, "/snapshot/vmstate")
	assertPath(t, "HostSnapshotMemory", layout.HostSnapshotMemory, wantVMDir+"/root/snapshot/memory")
	assertPath(t, "JailedSnapshotMemory", layout.JailedSnapshotMemory, "/snapshot/memory")
	if layout.ExecutableName != "firecracker" {
		t.Errorf("ExecutableName = %q, want firecracker", layout.ExecutableName)
	}
	if layout.VMID != "vm-abc123" {
		t.Errorf("VMID = %q, want vm-abc123", layout.VMID)
	}
}

func TestNewJailLayoutRejectsUnsafeInputs(t *testing.T) {
	tests := []struct {
		name    string
		base    string
		binary  string
		vmID    string
		wantErr error
	}{
		{name: "relative base", base: "var/lib/fire-fuse", binary: "/usr/bin/firecracker", vmID: "vm-1", wantErr: ErrInvalidJailLayout},
		{name: "root base", base: "/", binary: "/usr/bin/firecracker", vmID: "vm-1", wantErr: ErrInvalidJailLayout},
		{name: "unclean base", base: "/var/lib/../fire-fuse", binary: "/usr/bin/firecracker", vmID: "vm-1", wantErr: ErrInvalidJailLayout},
		{name: "relative binary", base: "/var/lib/fire-fuse", binary: "bin/firecracker", vmID: "vm-1", wantErr: ErrInvalidJailLayout},
		{name: "root binary", base: "/var/lib/fire-fuse", binary: "/", vmID: "vm-1", wantErr: ErrInvalidJailLayout},
		{name: "unsafe binary name", base: "/var/lib/fire-fuse", binary: "/usr/bin/firecracker@bad", vmID: "vm-1", wantErr: ErrInvalidJailLayout},
		{name: "slash in ID", base: "/var/lib/fire-fuse", binary: "/usr/bin/firecracker", vmID: "../vm-1", wantErr: ErrInvalidJailLayout},
		{name: "underscore in ID", base: "/var/lib/fire-fuse", binary: "/usr/bin/firecracker", vmID: "vm_1", wantErr: ErrInvalidJailLayout},
		{name: "long ID", base: "/var/lib/fire-fuse", binary: "/usr/bin/firecracker", vmID: strings.Repeat("a", 65), wantErr: ErrInvalidJailLayout},
		{name: "long socket path", base: "/" + strings.Repeat("a", 70), binary: "/usr/bin/firecracker", vmID: "vm-1", wantErr: ErrSocketPathTooLong},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewJailLayout(test.base, test.binary, test.vmID)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("NewJailLayout() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func assertPath(t *testing.T, name, got, want string) {
	t.Helper()
	if filepath.Clean(got) != filepath.Clean(want) {
		t.Errorf("%s = %q, want %q", name, got, want)
	}
}

package hostpath

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestCheckTrustedDirectory(t *testing.T) {
	safe := map[string]directoryInfo{
		"/":                {isDirectory: true, mode: 0o755, uid: 0},
		"/var":             {isDirectory: true, mode: 0o755, uid: 0},
		"/var/lib":         {isDirectory: true, mode: 0o755, uid: 0},
		"/var/lib/service": {isDirectory: true, mode: 0o750, uid: 0},
	}
	tests := []struct {
		name       string
		change     func(map[string]directoryInfo)
		inspectErr error
		wantDetail string
	}{
		{name: "trusted hierarchy"},
		{
			name: "wrong owner",
			change: func(entries map[string]directoryInfo) {
				entry := entries["/var/lib"]
				entry.uid = 1000
				entries["/var/lib"] = entry
			},
			wantDetail: "owned by UID 1000",
		},
		{
			name: "group writable",
			change: func(entries map[string]directoryInfo) {
				entry := entries["/var/lib/service"]
				entry.mode = 0o770
				entries["/var/lib/service"] = entry
			},
			wantDetail: "group or world writable",
		},
		{
			name: "not a directory",
			change: func(entries map[string]directoryInfo) {
				entry := entries["/var/lib"]
				entry.isDirectory = false
				entries["/var/lib"] = entry
			},
			wantDetail: "is not a directory",
		},
		{
			name:       "inspection failure",
			inspectErr: errors.New("stat failed"),
			wantDetail: "stat failed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entries := make(map[string]directoryInfo, len(safe))
			for path, info := range safe {
				entries[path] = info
			}
			if test.change != nil {
				test.change(entries)
			}
			inspect := func(path string) (directoryInfo, error) {
				if test.inspectErr != nil {
					return directoryInfo{}, test.inspectErr
				}
				return entries[path], nil
			}

			err := checkTrustedDirectory(
				"runtime_dir",
				"/var/lib/service",
				0,
				func(path string) (string, error) { return path, nil },
				inspect,
			)
			if test.wantDetail == "" {
				if err != nil {
					t.Fatalf("checkTrustedDirectory() error = %v", err)
				}
				return
			}
			if !errors.Is(err, ErrUntrustedDirectory) || !strings.Contains(err.Error(), test.wantDetail) {
				t.Fatalf("checkTrustedDirectory() error = %v, want %q", err, test.wantDetail)
			}
		})
	}
}

func TestCheckTrustedDirectoryValidatesSymlinkAndResolvedHierarchies(t *testing.T) {
	entries := map[string]directoryInfo{
		"/":                {isDirectory: true, mode: 0o755, uid: 0},
		"/links":           {isDirectory: true, mode: 0o755, uid: 0},
		"/links/service":   {isSymlink: true, mode: os.ModeSymlink, uid: 1000},
		"/trusted":         {isDirectory: true, mode: 0o755, uid: 0},
		"/trusted/service": {isDirectory: true, mode: 0o750, uid: 0},
	}
	var inspected []string
	inspect := func(path string) (directoryInfo, error) {
		inspected = append(inspected, path)
		return entries[path], nil
	}

	if err := checkTrustedDirectory(
		"runtime_dir",
		"/links/service",
		0,
		func(string) (string, error) { return "/trusted/service", nil },
		inspect,
	); err != nil {
		t.Fatalf("checkTrustedDirectory() error = %v", err)
	}
	wantInspected := []string{
		"/links/service", "/links", "/",
		"/trusted/service", "/trusted", "/",
	}
	if strings.Join(inspected, ",") != strings.Join(wantInspected, ",") {
		t.Errorf("inspected paths = %v, want %v", inspected, wantInspected)
	}
}

func TestCheckTrustedDirectoryRejectsWritableSymlinkParent(t *testing.T) {
	entries := map[string]directoryInfo{
		"/":                {isDirectory: true, mode: 0o755, uid: 0},
		"/unsafe":          {isDirectory: true, mode: 0o777, uid: 0},
		"/unsafe/service":  {isSymlink: true, mode: os.ModeSymlink, uid: 1000},
		"/trusted":         {isDirectory: true, mode: 0o755, uid: 0},
		"/trusted/service": {isDirectory: true, mode: 0o750, uid: 0},
	}
	inspect := func(path string) (directoryInfo, error) { return entries[path], nil }

	err := checkTrustedDirectory(
		"runtime_dir",
		"/unsafe/service",
		0,
		func(string) (string, error) { return "/trusted/service", nil },
		inspect,
	)
	if !errors.Is(err, ErrUntrustedDirectory) || !strings.Contains(err.Error(), "group or world writable") {
		t.Fatalf("checkTrustedDirectory() error = %v, want writable symlink parent", err)
	}
}

func TestCheckTrustedDirectoryReportsResolutionFailure(t *testing.T) {
	wantErr := errors.New("resolution failed")
	err := checkTrustedDirectory(
		"runtime_dir",
		"/var/lib/service",
		0,
		func(string) (string, error) { return "", wantErr },
		func(string) (directoryInfo, error) {
			return directoryInfo{isDirectory: true, mode: 0o755, uid: 0}, nil
		},
	)
	if !errors.Is(err, ErrUntrustedDirectory) || !errors.Is(err, wantErr) {
		t.Fatalf("checkTrustedDirectory() error = %v, want resolution failure", err)
	}
}

func TestCheckTrustedFile(t *testing.T) {
	safe := map[string]directoryInfo{
		"/":                     {isDirectory: true, mode: 0o755, uid: 0},
		"/usr":                  {isDirectory: true, mode: 0o755, uid: 0},
		"/usr/local":            {isDirectory: true, mode: 0o755, uid: 0},
		"/usr/local/bin":        {isDirectory: true, mode: 0o755, uid: 0},
		"/usr/local/bin/jailer": {mode: 0o755, uid: 0},
	}
	tests := []struct {
		name       string
		change     func(map[string]directoryInfo)
		wantDetail string
	}{
		{name: "trusted file"},
		{
			name: "wrong owner",
			change: func(entries map[string]directoryInfo) {
				entry := entries["/usr/local/bin/jailer"]
				entry.uid = 1000
				entries["/usr/local/bin/jailer"] = entry
			},
			wantDetail: "owned by UID 1000",
		},
		{
			name: "writable file",
			change: func(entries map[string]directoryInfo) {
				entry := entries["/usr/local/bin/jailer"]
				entry.mode = 0o775
				entries["/usr/local/bin/jailer"] = entry
			},
			wantDetail: "group or world writable",
		},
		{
			name: "non-regular file",
			change: func(entries map[string]directoryInfo) {
				entry := entries["/usr/local/bin/jailer"]
				entry.mode = os.ModeNamedPipe | 0o700
				entries["/usr/local/bin/jailer"] = entry
			},
			wantDetail: "is not regular",
		},
		{
			name: "writable parent",
			change: func(entries map[string]directoryInfo) {
				entry := entries["/usr/local/bin"]
				entry.mode = 0o777
				entries["/usr/local/bin"] = entry
			},
			wantDetail: "group or world writable",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entries := make(map[string]directoryInfo, len(safe))
			for path, info := range safe {
				entries[path] = info
			}
			if test.change != nil {
				test.change(entries)
			}
			inspect := func(path string) (directoryInfo, error) { return entries[path], nil }

			err := checkTrustedFile(
				"jailer_bin",
				"/usr/local/bin/jailer",
				0,
				func(path string) (string, error) { return path, nil },
				inspect,
			)
			if test.wantDetail == "" {
				if err != nil {
					t.Fatalf("checkTrustedFile() error = %v", err)
				}
				return
			}
			if !errors.Is(err, ErrUntrustedFile) || !strings.Contains(err.Error(), test.wantDetail) {
				t.Fatalf("checkTrustedFile() error = %v, want %q", err, test.wantDetail)
			}
		})
	}
}

func TestCheckTrustedFileValidatesConfiguredAndResolvedParents(t *testing.T) {
	entries := map[string]directoryInfo{
		"/":                        {isDirectory: true, mode: 0o755, uid: 0},
		"/links":                   {isDirectory: true, mode: 0o755, uid: 0},
		"/trusted":                 {isDirectory: true, mode: 0o755, uid: 0},
		"/trusted/bin":             {isDirectory: true, mode: 0o755, uid: 0},
		"/trusted/bin/firecracker": {mode: 0o755, uid: 0},
	}
	var inspected []string
	inspect := func(path string) (directoryInfo, error) {
		inspected = append(inspected, path)
		return entries[path], nil
	}

	if err := checkTrustedFile(
		"firecracker_bin",
		"/links/firecracker",
		0,
		func(string) (string, error) { return "/trusted/bin/firecracker", nil },
		inspect,
	); err != nil {
		t.Fatalf("checkTrustedFile() error = %v", err)
	}
	wantInspected := []string{
		"/links", "/",
		"/trusted/bin", "/trusted", "/",
		"/trusted/bin/firecracker",
	}
	if strings.Join(inspected, ",") != strings.Join(wantInspected, ",") {
		t.Errorf("inspected paths = %v, want %v", inspected, wantInspected)
	}
}

package supervisor

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

func TestReadProcessIdentity(t *testing.T) {
	const pid = 123
	want := microvm.ProcessIdentity{
		PID:            pid,
		BootID:         "01234567-89ab-cdef-0123-456789abcdef",
		StartTimeTicks: 4242,
	}
	files := map[string][]byte{
		linuxBootIDPath: []byte(want.BootID + "\n"),
		"/proc/123/stat": []byte(
			"123 (fire cracker) worker) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 4242 99\n",
		),
	}
	var paths []string
	identity, err := readProcessIdentity(pid, func(path string) ([]byte, error) {
		paths = append(paths, path)
		contents, exists := files[path]
		if !exists {
			return nil, fmt.Errorf("unexpected path %s", path)
		}
		return contents, nil
	})
	if err != nil {
		t.Fatalf("readProcessIdentity() error = %v", err)
	}
	if identity != want {
		t.Errorf("readProcessIdentity() = %#v, want %#v", identity, want)
	}
	if wantPaths := []string{linuxBootIDPath, "/proc/123/stat"}; !reflect.DeepEqual(paths, wantPaths) {
		t.Errorf("read paths = %v, want %v", paths, wantPaths)
	}
}

func TestVerifyProcessIdentity(t *testing.T) {
	expected := microvm.ProcessIdentity{
		PID:            123,
		BootID:         "01234567-89ab-cdef-0123-456789abcdef",
		StartTimeTicks: 4242,
	}
	matchingStat := []byte(
		"123 (fire cracker) worker) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 4242 99\n",
	)
	reusedStat := []byte(
		"123 (firecracker) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 9000 99\n",
	)

	tests := []struct {
		name         string
		bootID       string
		stat         []byte
		statErr      error
		want         processIdentityStatus
		wantStatRead bool
	}{
		{
			name:         "exact match",
			bootID:       expected.BootID,
			stat:         matchingStat,
			want:         processIdentityMatch,
			wantStatRead: true,
		},
		{
			name:         "process exited",
			bootID:       expected.BootID,
			statErr:      os.ErrNotExist,
			want:         processIdentityMissing,
			wantStatRead: true,
		},
		{
			name:   "host rebooted",
			bootID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			want:   processIdentityDifferentBoot,
		},
		{
			name:         "PID reused",
			bootID:       expected.BootID,
			stat:         reusedStat,
			want:         processIdentityReusedPID,
			wantStatRead: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			statRead := false
			status, err := verifyProcessIdentity(expected, func(path string) ([]byte, error) {
				switch path {
				case linuxBootIDPath:
					return []byte(test.bootID + "\n"), nil
				case "/proc/123/stat":
					statRead = true
					return test.stat, test.statErr
				default:
					return nil, fmt.Errorf("unexpected path %s", path)
				}
			})
			if err != nil {
				t.Fatalf("verifyProcessIdentity() error = %v", err)
			}
			if status != test.want {
				t.Errorf("verifyProcessIdentity() = %q, want %q", status, test.want)
			}
			if statRead != test.wantStatRead {
				t.Errorf("process stat read = %t, want %t", statRead, test.wantStatRead)
			}
		})
	}
}

func TestVerifyProcessIdentityRejectsUnsafeInputs(t *testing.T) {
	expected := microvm.ProcessIdentity{
		PID:            123,
		BootID:         "01234567-89ab-cdef-0123-456789abcdef",
		StartTimeTicks: 4242,
	}
	readErr := errors.New("permission denied")
	validBoot := func(path string) ([]byte, error) {
		if path == linuxBootIDPath {
			return []byte(expected.BootID), nil
		}
		return nil, readErr
	}

	tests := []struct {
		name     string
		expected microvm.ProcessIdentity
		readFile readFileFunc
		wantErr  error
	}{
		{
			name:     "empty persisted identity",
			readFile: func(string) ([]byte, error) { return nil, nil },
			wantErr:  microvm.ErrInvalidRuntimeOwnership,
		},
		{
			name: "partial persisted identity",
			expected: microvm.ProcessIdentity{
				PID: 123,
			},
			readFile: func(string) ([]byte, error) { return nil, nil },
			wantErr:  microvm.ErrInvalidRuntimeOwnership,
		},
		{
			name:     "invalid current boot ID",
			expected: expected,
			readFile: func(string) ([]byte, error) { return []byte("not-a-boot-id"), nil },
			wantErr:  microvm.ErrInvalidRuntimeOwnership,
		},
		{
			name:     "process stat read failure",
			expected: expected,
			readFile: validBoot,
			wantErr:  readErr,
		},
		{
			name:     "malformed process stat",
			expected: expected,
			readFile: func(path string) ([]byte, error) {
				if path == linuxBootIDPath {
					return []byte(expected.BootID), nil
				}
				return []byte("malformed"), nil
			},
			wantErr: ErrInvalidProcStat,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := verifyProcessIdentity(test.expected, test.readFile)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("verifyProcessIdentity() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestParseProcessStartTimeRejectsInvalidStat(t *testing.T) {
	tests := []struct {
		name string
		stat string
	}{
		{name: "wrong PID", stat: "124 (firecracker) S 1 2 3"},
		{name: "missing name", stat: "123 firecracker S 1 2 3"},
		{name: "unterminated name", stat: "123 (firecracker S 1 2 3"},
		{name: "too few fields", stat: "123 (firecracker) S 1 2 3"},
		{name: "invalid start time", stat: "123 (firecracker) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 nope"},
		{name: "zero start time", stat: "123 (firecracker) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseProcessStartTime(123, test.stat); !errors.Is(err, ErrInvalidProcStat) {
				t.Fatalf("parseProcessStartTime() error = %v, want ErrInvalidProcStat", err)
			}
		})
	}
}

func TestReadProcessIdentityRejectsInvalidInputs(t *testing.T) {
	readErr := errors.New("read failed")
	tests := []struct {
		name     string
		pid      int
		readFile readFileFunc
		wantErr  error
	}{
		{name: "invalid PID", pid: 0, readFile: func(string) ([]byte, error) { return nil, nil }},
		{name: "boot read failure", pid: 123, readFile: func(string) ([]byte, error) { return nil, readErr }, wantErr: readErr},
		{name: "invalid boot ID", pid: 123, readFile: func(path string) ([]byte, error) {
			if path == linuxBootIDPath {
				return []byte("not-a-boot-id"), nil
			}
			return []byte("123 (firecracker) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 4242"), nil
		}, wantErr: microvm.ErrInvalidRuntimeOwnership},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := readProcessIdentity(test.pid, test.readFile)
			if err == nil {
				t.Fatal("readProcessIdentity() error = nil")
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("readProcessIdentity() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

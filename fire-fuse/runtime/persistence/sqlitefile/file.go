// Package sqlitefile securely prepares and lifetime-locks a SQLite state file.
package sqlitefile

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

var (
	ErrUnsafe = errors.New("unsafe SQLite state file")
	ErrLocked = errors.New("SQLite state store is already owned by another process")
)

type Lock struct {
	file *os.File
}

// Prepare validates the opened inode without following symlinks and enforces
// owner-only permissions before SQLite opens the pathname.
func Prepare(path string) (err error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("%w: open without following symlinks: %w", ErrUnsafe, err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return fmt.Errorf("%w: create state file handle", ErrUnsafe)
	}
	defer func() { err = errors.Join(err, wrapCloseError(file.Close())) }()
	if err := Validate(file, uint32(os.Geteuid())); err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("%w: set mode 0600: %w", ErrUnsafe, err)
	}
	return nil
}

func Validate(file *os.File, expectedUID uint32) error {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return fmt.Errorf("%w: inspect opened file: %w", ErrUnsafe, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("%w: opened inode is not a regular file", ErrUnsafe)
	}
	if stat.Uid != expectedUID {
		return fmt.Errorf("%w: opened inode is owned by UID %d, want effective UID %d", ErrUnsafe, stat.Uid, expectedUID)
	}
	return nil
}

func AcquireLock(path string) (*Lock, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open SQLite state lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open SQLite state lock: create file handle")
	}
	fail := func(err error) (*Lock, error) { return nil, errors.Join(err, file.Close()) }
	info, err := file.Stat()
	if err != nil {
		return fail(fmt.Errorf("inspect SQLite state lock: %w", err))
	}
	if !info.Mode().IsRegular() {
		return fail(errors.New("SQLite state lock is not a regular file"))
	}
	if err := file.Chmod(0o600); err != nil {
		return fail(fmt.Errorf("secure SQLite state lock: %w", err))
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return fail(ErrLocked)
		}
		return fail(fmt.Errorf("lock SQLite state store: %w", err))
	}
	return &Lock{file: file}, nil
}

func (l *Lock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	return errors.Join(wrapUnlockError(unix.Flock(int(file.Fd()), unix.LOCK_UN)), file.Close())
}

func wrapCloseError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("close SQLite state file: %w", err)
}

func wrapUnlockError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("unlock SQLite state store: %w", err)
}

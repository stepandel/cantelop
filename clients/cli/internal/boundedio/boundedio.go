// Package boundedio reads files and command output with size limits.
package boundedio

import (
	"bytes"
	"errors"
	"io"
	"os"
)

func ReadRegularFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("expected a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	document, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(document)) > limit {
		return nil, errors.New("file exceeds size limit")
	}
	return document, nil
}

type LimitedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func NewLimitedBuffer(limit int) *LimitedBuffer { return &LimitedBuffer{limit: limit} }

func (b *LimitedBuffer) Write(value []byte) (int, error) {
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		_, _ = b.buffer.Write(value[:min(len(value), remaining)])
	}
	return len(value), nil
}

func (b *LimitedBuffer) String() string { return b.buffer.String() }

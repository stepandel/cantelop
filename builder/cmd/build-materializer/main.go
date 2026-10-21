// build-materializer converts one already verified Docker archive into a local
// ext4 image. It has no network or control-plane credentials; its caller owns
// artifact transfer and host installation.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"

	"github.com/stepandel/cantelop/builder/materializer"
	"github.com/stepandel/cantelop/fire-fuse/config"
)

var operationIDPattern = regexp.MustCompile(`^op_[0-9a-f]{32}$`)

const socketPath = "/run/cantelop-builder/materializer.sock"

type preparedImage struct {
	SchemaVersion int                   `json:"schema_version"`
	BuildID       string                `json:"build_id"`
	Image         config.ContainerImage `json:"image"`
}

type capturePublisher struct{ image *config.ContainerImage }

func (p capturePublisher) Publish(image config.ContainerImage) (string, error) {
	*p.image = image
	return config.ContainerImageID(image)
}

func main() {
	if err := serve(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func serve() error {
	if os.Geteuid() != 0 {
		return errors.New("build materializer requires root to preserve filesystem ownership")
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o750); err != nil {
		return err
	}
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := os.Chmod(socketPath, 0o660); err != nil {
		return err
	}
	var conversionMu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/prepare", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 256)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var command struct {
			CommandID string `json:"command_id"`
		}
		if decoder.Decode(&command) != nil || decoder.Decode(&struct{}{}) != io.EOF || !operationIDPattern.MatchString(command.CommandID) {
			http.Error(w, "invalid conversion command", http.StatusBadRequest)
			return
		}
		conversionMu.Lock()
		defer conversionMu.Unlock()
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Minute)
		defer cancel()
		prepared, err := run(ctx,
			filepath.Join("/var/lib/cantelop-builder/incoming", command.CommandID+".tar"),
			"/var/lib/cantelop-builder/work", "/var/lib/cantelop-builder/images", command.CommandID,
		)
		if err != nil {
			status := http.StatusUnprocessableEntity
			var retryable interface{ Retryable() bool }
			if (errors.As(err, &retryable) && retryable.Retryable()) ||
				errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EIO) || errors.Is(err, syscall.EROFS) {
				status = http.StatusServiceUnavailable
			}
			http.Error(w, "conversion failed", status)
			fmt.Fprintln(os.Stderr, err)
			return
		}
		// The caller owns the image only once it receives the result. A caller
		// that disconnected or timed out cannot learn its path to remove it.
		if r.Context().Err() != nil {
			_ = os.Remove(prepared.Image.RootFSImage)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(prepared); err != nil {
			_ = os.Remove(prepared.Image.RootFSImage)
		}
	})
	return http.Serve(listener, mux)
}

func run(ctx context.Context, archive, workDirectory, imageDirectory, operationID string) (preparedImage, error) {
	if os.Geteuid() != 0 {
		return preparedImage{}, errors.New("build materializer requires root to preserve filesystem ownership")
	}
	if !operationIDPattern.MatchString(operationID) || !filepath.IsAbs(archive) || filepath.Clean(archive) != archive {
		return preparedImage{}, errors.New("invalid materializer input")
	}
	archiveInfo, err := os.Lstat(archive)
	if err != nil || !archiveInfo.Mode().IsRegular() || archiveInfo.Mode()&os.ModeSymlink != 0 {
		return preparedImage{}, errors.New("archive must be a regular file")
	}
	var image config.ContainerImage
	imageMaterializer, err := materializer.New(
		"/usr/bin/docker", "/usr/sbin/mkfs.ext4", workDirectory, imageDirectory,
		capturePublisher{image: &image},
	)
	if err != nil {
		return preparedImage{}, err
	}
	buildID, err := imageMaterializer.Materialize(ctx, "bop_"+operationID[3:], archive)
	if err != nil {
		// Publish runs after the image is installed; do not strand it on failure.
		if image.RootFSImage != "" {
			_ = os.Remove(image.RootFSImage)
		}
		return preparedImage{}, err
	}
	return preparedImage{SchemaVersion: 1, BuildID: buildID, Image: image}, nil
}

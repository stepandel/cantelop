package controlapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func fastUploadRetries(t *testing.T) {
	t.Helper()
	delay := TransientRetryDelay
	TransientRetryDelay = time.Millisecond
	t.Cleanup(func() { TransientRetryDelay = delay })
}

func sessionRuntimeUploadFixture(t *testing.T, parts int) (string, int64, DeploymentUpload) {
	t.Helper()
	const partSize = 5 << 20
	size := int64(parts-1)*partSize + 1234
	archive := make([]byte, size)
	for index := range archive {
		archive[index] = byte(index / partSize)
	}
	path := filepath.Join(t.TempDir(), "session-runtime.tar.gz")
	if err := os.WriteFile(path, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	upload := DeploymentUpload{SessionRuntime: &struct {
		PartSizeBytes int64                      `json:"part_size_bytes"`
		Parts         []SessionRuntimeUploadPart `json:"parts"`
	}{PartSizeBytes: partSize}}
	for number := 1; number <= parts; number++ {
		upload.SessionRuntime.Parts = append(upload.SessionRuntime.Parts, SessionRuntimeUploadPart{
			PartNumber: number, Method: http.MethodPut, URL: "http://127.0.0.1:12345/r2/part/" + strconv.Itoa(number), Headers: map[string]string{},
		})
	}
	return path, size, upload
}

func TestSessionRuntimePartsUploadConcurrentlyRetryAndStayOrdered(t *testing.T) {
	fastUploadRetries(t)
	path, size, upload := sessionRuntimeUploadFixture(t, 6)
	var mu sync.Mutex
	attempts := map[int]int{}
	active, peak := 0, 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		number, _ := strconv.Atoi(strings.TrimPrefix(request.URL.Path, "/r2/part/"))
		body, _ := io.ReadAll(request.Body)
		mu.Lock()
		attempts[number]++
		attempt := attempts[number]
		active++
		peak = max(peak, active)
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
		want := 5 << 20
		if number == 6 {
			want = 1234
		}
		if len(body) != want || body[0] != byte(number-1) || body[len(body)-1] != byte(number-1) {
			t.Errorf("part %d body has %d bytes starting %d", number, len(body), body[0])
		}
		switch {
		case number == 2 && attempt == 1:
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
		case number == 4 && attempt == 1:
			return nil, errors.New("connection reset by peer")
		}
		headers := make(http.Header)
		headers.Set("ETag", fmt.Sprintf(`"part-%d"`, number))
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(nil)), Header: headers}, nil
	})}
	completed, err := UploadSessionRuntimeParts(context.Background(), client, path, size, upload)
	if err != nil {
		t.Fatal(err)
	}
	for index, part := range completed {
		if part.PartNumber != index+1 || part.ETag != fmt.Sprintf(`"part-%d"`, index+1) {
			t.Fatalf("completed parts = %#v", completed)
		}
	}
	if attempts[2] != 2 || attempts[4] != 2 || attempts[1] != 1 {
		t.Fatalf("attempts = %v", attempts)
	}
	if peak < 2 || peak > SessionRuntimeUploadConcurrency {
		t.Fatalf("peak concurrent uploads = %d", peak)
	}
}

func TestSessionRuntimePartRejectionIsNotRetried(t *testing.T) {
	fastUploadRetries(t)
	path, size, upload := sessionRuntimeUploadFixture(t, 2)
	var mu sync.Mutex
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		mu.Lock()
		requests++
		mu.Unlock()
		status := http.StatusOK
		if request.URL.Path == "/r2/part/2" {
			status = http.StatusForbidden
		}
		headers := make(http.Header)
		headers.Set("ETag", `"part"`)
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("")), Header: headers}, nil
	})}
	_, err := UploadSessionRuntimeParts(context.Background(), client, path, size, upload)
	if err == nil || err.Error() != "part 2: artifact store returned HTTP 403" || requests != 2 {
		t.Fatalf("err = %v after %d requests", err, requests)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

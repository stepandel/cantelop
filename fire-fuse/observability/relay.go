package observability

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

const ingestPath = "/api/internal/observability/events"

var ingestTokenPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Relay struct {
	target       *url.URL
	token        string
	spool        *Spool
	client       *http.Client
	pollInterval time.Duration
}

func NewRelay(origin, token string, spool *Spool, client *http.Client) (*Relay, error) {
	target, err := url.Parse(origin)
	if err != nil || target.Host == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" ||
		(target.Path != "" && target.Path != "/") || !ingestTokenPattern.MatchString(token) || spool == nil {
		return nil, errors.New("invalid observability relay configuration")
	}
	if target.Scheme != "https" && !(target.Scheme == "http" && loopbackHost(target.Hostname())) {
		return nil, errors.New("observability relay requires HTTPS outside loopback")
	}
	target.Path = ingestPath
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &Relay{target: target, token: token, spool: spool, client: client, pollInterval: time.Second}, nil
}

func (r *Relay) Run(ctx context.Context) error {
	delay := r.pollInterval
	for {
		delivered, err := r.deliverNext(ctx)
		if err == nil && delivered {
			delay = r.pollInterval
			continue
		}
		if err != nil && ctx.Err() == nil {
			slog.Warn("Observation relay deferred", "error", err)
		}
		if !wait(ctx, delay) {
			return nil
		}
		if err != nil && delay < 30*time.Second {
			delay *= 2
		}
	}
}

// deliverNext sends the oldest spooled batches in one request. It reports
// whether any spool files were acknowledged.
func (r *Relay) deliverNext(ctx context.Context) (bool, error) {
	entries, err := r.spool.Collect()
	if err != nil || len(entries) == 0 {
		return false, err
	}
	var batches []storedBatch
	for _, entry := range entries {
		batches = append(batches, entry.batches...)
	}
	status, err := r.post(ctx, batches)
	if err != nil {
		return false, err
	}
	if rejectedStatus(status) && len(batches) > 1 {
		// Console ingests a request atomically. Retry each recorded batch on its
		// own so one rejected batch cannot discard unrelated observations.
		return r.deliverIndividually(ctx, entries)
	}
	if rejectedStatus(status) {
		slog.Warn("Dropping rejected observation batch", "status", status, "bytes", batches[0].size)
	} else if status != http.StatusAccepted {
		return false, fmt.Errorf("observation ingest returned HTTP %d", status)
	}
	return true, r.spool.Acknowledge(entries...)
}

func (r *Relay) deliverIndividually(ctx context.Context, entries []*Entry) (bool, error) {
	delivered := false
	for _, entry := range entries {
		for _, batch := range entry.batches {
			status, err := r.post(ctx, []storedBatch{batch})
			if err != nil {
				return delivered, err
			}
			if rejectedStatus(status) {
				slog.Warn("Dropping rejected observation batch", "status", status, "bytes", batch.size)
			} else if status != http.StatusAccepted {
				// Batches already accepted from this file are sent again on
				// retry; stable event IDs make ingestion idempotent.
				return delivered, fmt.Errorf("observation ingest returned HTTP %d", status)
			}
		}
		if err := r.spool.Acknowledge(entry); err != nil {
			return delivered, err
		}
		delivered = true
	}
	return delivered, nil
}

func (r *Relay) post(ctx context.Context, batches []storedBatch) (int, error) {
	body := encodeStoredBatches(batches)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.target.String(), bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+r.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := r.client.Do(request)
	if err != nil {
		return 0, fmt.Errorf("send observation batch: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	return response.StatusCode, nil
}

// rejectedStatus reports statuses that reject the batch itself. Retrying it at
// the head of the shared spool would suppress every later trace, including
// other Apps'. Authentication, rate limits, network errors and 5xx responses
// still retry.
func rejectedStatus(status int) bool {
	switch status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

// encodeStoredBatches joins already-encoded events into one ingest Batch
// without decoding them. Its size never exceeds the stored batches' total.
func encodeStoredBatches(batches []storedBatch) []byte {
	var body bytes.Buffer
	body.WriteString(`{"events":[`)
	first := true
	for _, batch := range batches {
		for _, event := range batch.events {
			if !first {
				body.WriteByte(',')
			}
			first = false
			body.Write(event)
		}
	}
	body.WriteString(`]}`)
	return body.Bytes()
}

func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

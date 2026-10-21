package observer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxNetdataResponseBytes = 1 << 20

// netdataChartConcurrency bounds the chart reads one host keeps in flight.
// Hosts are read in parallel too, so the parent sees at most this many
// requests per host at once.
const netdataChartConcurrency = 4

type InfrastructureRange struct {
	Name    string
	Seconds int
	Points  int
}

var infrastructureRanges = map[string]InfrastructureRange{
	"1h":  {Name: "1h", Seconds: 60 * 60, Points: 120},
	"6h":  {Name: "6h", Seconds: 6 * 60 * 60, Points: 144},
	"24h": {Name: "24h", Seconds: 24 * 60 * 60, Points: 288},
}

type infrastructureChartSpec struct {
	ID         string
	Title      string
	Units      string
	Dimensions []string
}

// infrastructureHostSpec is one machine in the deployment. Compute runs the
// Netdata parent the observer reads over loopback; storage and the builder
// stream to it and are addressed by their fixed Netdata hostnames, so the
// observer never needs a route to either machine.
type infrastructureHostSpec struct {
	ID     string
	Title  string
	Role   string
	Prefix string
	Charts []infrastructureChartSpec
}

// A host whose newest system.cpu sample is older than this is reported as
// unreachable: its agent has stopped collecting or stopped streaming.
const infrastructureHostStaleAfter = 60 * time.Second

func hostResourceCharts(root infrastructureChartSpec) []infrastructureChartSpec {
	return []infrastructureChartSpec{
		{
			ID: "system.cpu", Title: "CPU utilization", Units: "percent",
			Dimensions: []string{"user", "system", "iowait", "steal"},
		},
		{
			ID: "system.ram", Title: "Memory", Units: "MiB",
			Dimensions: []string{"used", "cached", "free"},
		},
		root,
	}
}

var rootFilesystemChart = infrastructureChartSpec{
	ID: "disk_space./", Title: "Root filesystem", Units: "GiB",
	Dimensions: []string{"used", "avail"},
}

func healthChart(dimensions ...string) infrastructureChartSpec {
	return infrastructureChartSpec{
		ID: "fire_fuse.health", Title: "Service health", Units: "status", Dimensions: dimensions,
	}
}

var infrastructureHostSpecs = []infrastructureHostSpec{
	{
		ID: "compute", Title: "Compute", Role: "Firecracker runtime · JuiceFS gateway",
		Charts: append([]infrastructureChartSpec{healthChart("fire_fuse", "juicefs")}, append(hostResourceCharts(rootFilesystemChart),
			infrastructureChartSpec{ID: "fire_fuse.juicefs_cache", Title: "JuiceFS gateway cache", Units: "MiB"},
			infrastructureChartSpec{ID: "fire_fuse.juicefs_operations", Title: "JuiceFS cache and object operations", Units: "operations/s"},
		)...),
	},
	{
		ID: "storage", Title: "Storage", Role: "Redis metadata · MinIO objects", Prefix: "/host/storage",
		Charts: append([]infrastructureChartSpec{healthChart("redis", "minio")}, append(hostResourceCharts(rootFilesystemChart),
			infrastructureChartSpec{ID: "fire_fuse.backend_latency", Title: "Storage backend probe latency", Units: "milliseconds"},
			infrastructureChartSpec{ID: "fire_fuse.capacity_available", Title: "Storage capacity available", Units: "percent"},
			infrastructureChartSpec{ID: "fire_fuse.redis_memory", Title: "Redis memory", Units: "MiB"},
			infrastructureChartSpec{ID: "fire_fuse.minio_storage", Title: "MinIO backing filesystem", Units: "GiB"},
			infrastructureChartSpec{ID: "fire_fuse.redis_rejections", Title: "Redis rejected connections", Units: "connections/s"},
		)...),
	},
	{
		ID: "builder", Title: "Builder", Role: "Control worker · build materializer", Prefix: "/host/builder",
		Charts: append([]infrastructureChartSpec{healthChart("control_worker", "build_materializer")}, hostResourceCharts(rootFilesystemChart)...),
	},
}

type InfrastructureSnapshot struct {
	ObservedAt time.Time            `json:"observed_at"`
	Range      string               `json:"range"`
	Hosts      []InfrastructureHost `json:"hosts"`
	// Charts repeats the compute host's charts for consoles that predate
	// per-host metrics.
	Charts []InfrastructureChart `json:"charts"`
}

type InfrastructureHost struct {
	ID              string                `json:"id"`
	Title           string                `json:"title"`
	Role            string                `json:"role"`
	Reachable       bool                  `json:"reachable"`
	LastCollectedAt *time.Time            `json:"last_collected_at,omitempty"`
	Charts          []InfrastructureChart `json:"charts"`
}

type InfrastructureChart struct {
	ID         string                `json:"id"`
	Title      string                `json:"title"`
	Units      string                `json:"units"`
	Dimensions []string              `json:"dimensions"`
	Points     []InfrastructurePoint `json:"points"`
}

type InfrastructurePoint struct {
	At     time.Time  `json:"at"`
	Values []*float64 `json:"values"`
}

type InfrastructureSource interface {
	Infrastructure(context.Context, InfrastructureRange) (InfrastructureSnapshot, error)
}

// CompleteSource combines the control-plane inventory and Netdata without
// giving either service access to the other's credentials or transport.
type CompleteSource struct {
	*ControlClient
	InfrastructureSource
}

func NewCompleteSource(control *ControlClient, infrastructure InfrastructureSource) (*CompleteSource, error) {
	if control == nil || infrastructure == nil {
		return nil, errors.New("control and infrastructure sources are required")
	}
	return &CompleteSource{ControlClient: control, InfrastructureSource: infrastructure}, nil
}

type NetdataClient struct {
	baseURL *url.URL
	client  *http.Client
	now     func() time.Time
}

func NewNetdataClient(rawURL string, client *http.Client) (*NetdataClient, error) {
	baseURL, err := parseLoopbackOrigin(rawURL)
	if err != nil {
		return nil, fmt.Errorf("Netdata URL: %w", err)
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &NetdataClient{baseURL: baseURL, client: client, now: time.Now}, nil
}

func (c *NetdataClient) Infrastructure(ctx context.Context, window InfrastructureRange) (InfrastructureSnapshot, error) {
	allowed, ok := infrastructureRanges[window.Name]
	if !ok || allowed != window {
		return InfrastructureSnapshot{}, errors.New("unsupported infrastructure range")
	}
	now := c.now().UTC()
	hosts := make([]InfrastructureHost, len(infrastructureHostSpecs))
	errs := make([]error, len(infrastructureHostSpecs))
	var wait sync.WaitGroup
	for i, spec := range infrastructureHostSpecs {
		wait.Add(1)
		go func() {
			defer wait.Done()
			hosts[i], errs[i] = c.readHost(ctx, spec, window, now)
		}()
	}
	wait.Wait()
	// The compute agent is the parent every read passes through; without it
	// there is nothing trustworthy to report. A child that has never
	// connected, or whose charts fail, is shown as unreachable instead.
	if errs[0] != nil {
		return InfrastructureSnapshot{}, errs[0]
	}
	return InfrastructureSnapshot{
		ObservedAt: now, Range: window.Name, Hosts: hosts, Charts: hosts[0].Charts,
	}, nil
}

func (c *NetdataClient) readHost(
	ctx context.Context,
	spec infrastructureHostSpec,
	window InfrastructureRange,
	now time.Time,
) (InfrastructureHost, error) {
	host := InfrastructureHost{
		ID: spec.ID, Title: spec.Title, Role: spec.Role, Charts: []InfrastructureChart{},
	}
	lastEntry, err := c.readLastEntry(ctx, spec.Prefix)
	if err != nil {
		return host, err
	}
	collected := time.Unix(lastEntry, 0).UTC()
	host.LastCollectedAt = &collected
	host.Reachable = now.Sub(collected) <= infrastructureHostStaleAfter
	// History is still returned for an unreachable host, so the graphs show
	// when it stopped reporting. Charts are read concurrently, but every read
	// runs to completion and the results are assembled in spec order, so a
	// failure yields the same charts and error a sequential read would.
	charts := make([]InfrastructureChart, len(spec.Charts))
	chartErrs := make([]error, len(spec.Charts))
	// Only cancellation of ctx itself can stop the loop early.
	if err := forEachBounded(ctx, len(spec.Charts), netdataChartConcurrency, func(ctx context.Context, i int) error {
		charts[i], chartErrs[i] = c.readChart(ctx, spec.Prefix, spec.Charts[i], window)
		return nil
	}); err != nil {
		return host, err
	}
	for i := range spec.Charts {
		if errors.Is(chartErrs[i], errNetdataChartMissing) {
			continue
		}
		if chartErrs[i] != nil {
			return host, chartErrs[i]
		}
		host.Charts = append(host.Charts, charts[i])
	}
	return host, nil
}

var errNetdataChartMissing = errors.New("Netdata chart is not collected")

func (c *NetdataClient) readLastEntry(ctx context.Context, prefix string) (int64, error) {
	values := url.Values{"chart": {"system.cpu"}}
	var payload struct {
		LastEntry *int64 `json:"last_entry"`
	}
	if err := c.readJSON(ctx, prefix+"/api/v1/chart", values, "system.cpu", &payload); err != nil {
		return 0, err
	}
	if payload.LastEntry == nil || *payload.LastEntry <= 0 {
		return 0, errors.New("Netdata chart system.cpu has no last entry")
	}
	return *payload.LastEntry, nil
}

func (c *NetdataClient) readChart(
	ctx context.Context,
	prefix string,
	spec infrastructureChartSpec,
	window InfrastructureRange,
) (InfrastructureChart, error) {
	values := url.Values{
		"after":   {strconv.Itoa(-window.Seconds)},
		"before":  {"0"},
		"chart":   {spec.ID},
		"format":  {"json"},
		"group":   {"average"},
		"options": {"seconds"},
		"points":  {strconv.Itoa(window.Points)},
	}
	values["dimension"] = append([]string(nil), spec.Dimensions...)
	var payload struct {
		Labels []string     `json:"labels"`
		Data   [][]*float64 `json:"data"`
	}
	if err := c.readJSON(ctx, prefix+"/api/v1/data", values, spec.ID, &payload); err != nil {
		return InfrastructureChart{}, err
	}
	if len(payload.Labels) < 2 || payload.Labels[0] != "time" || len(payload.Data) > window.Points {
		return InfrastructureChart{}, fmt.Errorf("Netdata chart %s returned an invalid series", spec.ID)
	}
	dimensions := append([]string(nil), payload.Labels[1:]...)
	points := make([]InfrastructurePoint, 0, len(payload.Data))
	for _, row := range payload.Data {
		if len(row) != len(payload.Labels) || row[0] == nil {
			return InfrastructureChart{}, fmt.Errorf("Netdata chart %s returned an invalid point", spec.ID)
		}
		points = append(points, InfrastructurePoint{
			At: time.Unix(int64(*row[0]), 0).UTC(), Values: append([]*float64(nil), row[1:]...),
		})
	}
	return InfrastructureChart{
		ID: spec.ID, Title: spec.Title, Units: spec.Units,
		Dimensions: dimensions, Points: points,
	}, nil
}

// readJSON performs one bounded GET against a fixed Netdata API path and
// decodes exactly one JSON value. A 404 means the chart (or the streamed
// host) is not known to the parent.
func (c *NetdataClient) readJSON(ctx context.Context, path string, values url.Values, chartID string, target any) error {
	reference := &url.URL{Path: path, RawQuery: values.Encode()}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL.ResolveReference(reference).String(), nil)
	if err != nil {
		return fmt.Errorf("build Netdata request: %w", err)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf("read Netdata chart %s: %w", chartID, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %s", errNetdataChartMissing, chartID)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Netdata chart %s returned HTTP %d", chartID, response.StatusCode)
	}
	document, err := io.ReadAll(io.LimitReader(response.Body, maxNetdataResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read Netdata chart %s: %w", chartID, err)
	}
	if len(document) > maxNetdataResponseBytes {
		return fmt.Errorf("Netdata chart %s exceeds 1 MiB", chartID)
	}
	decoder := json.NewDecoder(strings.NewReader(string(document)))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode Netdata chart %s: %w", chartID, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("Netdata chart %s must contain one JSON value", chartID)
	}
	return nil
}

func parseInfrastructureRange(r *http.Request) (InfrastructureRange, error) {
	values := r.URL.Query()
	for key, entries := range values {
		if key != "range" || len(entries) != 1 {
			return InfrastructureRange{}, errors.New("invalid infrastructure query")
		}
	}
	name := values.Get("range")
	if name == "" {
		name = "1h"
	}
	window, ok := infrastructureRanges[name]
	if !ok {
		return InfrastructureRange{}, errors.New("invalid infrastructure range")
	}
	return window, nil
}

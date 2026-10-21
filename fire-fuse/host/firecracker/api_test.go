package firecracker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"
)

type recordedRequest struct {
	Path string
	Body map[string]any
}

func TestConfigureSnapshotTemplate(t *testing.T) {
	var requests []recordedRequest
	socketPath := serveUnixHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("Decode() error = %v", err)
		}
		requests = append(requests, recordedRequest{Path: r.URL.Path, Body: body})
		w.WriteHeader(http.StatusNoContent)
	}))
	client, err := NewClient(socketPath)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(client.CloseIdleConnections)

	err = client.ConfigureSnapshotTemplate(context.Background(), SnapshotTemplateConfig{
		VCPUCount:           2,
		MemoryMiB:           512,
		HotplugMemoryMiB:    4096,
		KernelImagePath:     "/vmlinux",
		RootFSPath:          "/rootfs.ext4",
		ContainerRootFSPath: "/container-rootfs.ext4",
		BootArgs:            "console=ttyS0 systemd.unit=fire-fuse-snapshot.target",
		NetworkInterface: &NetworkInterface{
			InterfaceID:    "eth0",
			GuestMAC:       "02:fc:ac:10:00:02",
			HostDeviceName: "ff0123456789abc",
		},
		VSock: VSock{GuestCID: 3, UDSPath: "/run/fire-fuse.vsock"},
	})
	if err != nil {
		t.Fatalf("ConfigureSnapshotTemplate() error = %v", err)
	}

	want := []recordedRequest{
		{Path: "/machine-config", Body: map[string]any{
			"vcpu_count": float64(2), "mem_size_mib": float64(512),
		}},
		{Path: "/hotplug/memory", Body: map[string]any{"total_size_mib": float64(4096)}},
		{Path: "/boot-source", Body: map[string]any{
			"kernel_image_path": "/vmlinux",
			"boot_args":         "console=ttyS0 systemd.unit=fire-fuse-snapshot.target",
		}},
		{Path: "/drives/rootfs", Body: map[string]any{
			"drive_id": "rootfs", "path_on_host": "/rootfs.ext4",
			"is_root_device": true, "is_read_only": true,
		}},
		{Path: "/drives/container_rootfs", Body: map[string]any{
			"drive_id": "container_rootfs", "path_on_host": "/container-rootfs.ext4",
			"is_root_device": false, "is_read_only": true,
		}},
		{Path: "/network-interfaces/eth0", Body: map[string]any{
			"iface_id": "eth0", "guest_mac": "02:fc:ac:10:00:02",
			"host_dev_name": "ff0123456789abc",
		}},
		{Path: "/vsock", Body: map[string]any{
			"guest_cid": float64(3), "uds_path": "/run/fire-fuse.vsock",
		}},
		{Path: "/actions", Body: map[string]any{"action_type": "InstanceStart"}},
	}
	if !reflect.DeepEqual(requests, want) {
		t.Errorf("requests = %#v, want %#v", requests, want)
	}
}

func TestMemoryHotplugControls(t *testing.T) {
	var methods []string
	socketPath := serveUnixHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"total_size_mib":4096,"requested_size_mib":512,"plugged_size_mib":256}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	client, err := NewClient(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	if err := client.ConfigureMemoryHotplug(context.Background(), 4096); err != nil {
		t.Fatal(err)
	}
	if err := client.RequestMemoryHotplug(context.Background(), 512); err != nil {
		t.Fatal(err)
	}
	status, err := client.ReadMemoryHotplug(context.Background())
	if err != nil || status.TotalSizeMiB != 4096 || status.RequestedSizeMiB != 512 || status.PluggedSizeMiB != 256 {
		t.Fatalf("ReadMemoryHotplug() = %#v, %v", status, err)
	}
	if want := []string{"PUT /hotplug/memory", "PATCH /hotplug/memory", "GET /hotplug/memory"}; !reflect.DeepEqual(methods, want) {
		t.Fatalf("methods = %v, want %v", methods, want)
	}
}

func TestSnapshotLifecycleRequests(t *testing.T) {
	type request struct {
		method string
		path   string
		body   map[string]any
	}
	var requests []request
	socketPath := serveUnixHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("Decode() error = %v", err)
		}
		requests = append(requests, request{method: r.Method, path: r.URL.Path, body: body})
		w.WriteHeader(http.StatusNoContent)
	}))
	client, err := NewClient(socketPath)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(client.CloseIdleConnections)

	if err := client.Pause(context.Background()); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	if err := client.CreateFullSnapshot(
		context.Background(),
		"/snapshot/template.state",
		"/snapshot/template.memory",
	); err != nil {
		t.Fatalf("CreateFullSnapshot() error = %v", err)
	}
	if err := client.Resume(context.Background()); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	want := []request{
		{
			method: http.MethodPatch,
			path:   "/vm",
			body:   map[string]any{"state": "Paused"},
		},
		{
			method: http.MethodPut,
			path:   "/snapshot/create",
			body: map[string]any{
				"snapshot_type": "Full",
				"snapshot_path": "/snapshot/template.state",
				"mem_file_path": "/snapshot/template.memory",
			},
		},
		{
			method: http.MethodPatch,
			path:   "/vm",
			body:   map[string]any{"state": "Resumed"},
		},
	}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests = %#v, want %#v", requests, want)
	}
}

func TestRestoreLifecycleRequests(t *testing.T) {
	type request struct {
		method string
		path   string
		body   map[string]any
	}
	var requests []request
	socketPath := serveUnixHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("Decode() error = %v", err)
		}
		requests = append(requests, request{method: r.Method, path: r.URL.Path, body: body})
		w.WriteHeader(http.StatusNoContent)
	}))
	client, err := NewClient(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)

	networkInterface := NetworkInterface{
		InterfaceID:    "eth0",
		GuestMAC:       "02:fc:00:00:00:01",
		HostDeviceName: "ff0123456789abc",
	}
	if err := client.LoadSnapshot(
		context.Background(),
		"/snapshot/vmstate",
		"/snapshot/memory",
		networkInterface,
	); err != nil {
		t.Fatalf("LoadSnapshot() error = %v", err)
	}
	if err := client.PatchContainerRootFS(
		context.Background(),
		"/container-rootfs.ext4",
	); err != nil {
		t.Fatalf("PatchContainerRootFS() error = %v", err)
	}
	if err := client.Resume(context.Background()); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}

	want := []request{
		{
			method: http.MethodPut,
			path:   "/snapshot/load",
			body: map[string]any{
				"snapshot_path": "/snapshot/vmstate",
				"mem_backend": map[string]any{
					"backend_path": "/snapshot/memory",
					"backend_type": "File",
				},
				"track_dirty_pages": false,
				"resume_vm":         false,
				"network_overrides": []any{
					map[string]any{
						"iface_id":      "eth0",
						"host_dev_name": "ff0123456789abc",
					},
				},
			},
		},
		{
			method: http.MethodPatch,
			path:   "/drives/container_rootfs",
			body: map[string]any{
				"drive_id":     "container_rootfs",
				"path_on_host": "/container-rootfs.ext4",
			},
		},
		{
			method: http.MethodPatch,
			path:   "/vm",
			body:   map[string]any{"state": "Resumed"},
		},
	}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests = %#v, want %#v", requests, want)
	}
}

func TestCreateFullSnapshotValidatesPaths(t *testing.T) {
	client, err := NewClient("/tmp/fire-fuse-missing.sock")
	if err != nil {
		t.Fatal(err)
	}
	for _, paths := range [][2]string{
		{"relative", "/memory"},
		{"/snapshot", "relative"},
		{"/same", "/same"},
		{"/", "/memory"},
	} {
		if err := client.CreateFullSnapshot(
			context.Background(),
			paths[0],
			paths[1],
		); !errors.Is(err, ErrInvalidConfiguration) {
			t.Errorf("CreateFullSnapshot(%q, %q) error = %v", paths[0], paths[1], err)
		}
	}
}

func TestSendCtrlAltDel(t *testing.T) {
	var request recordedRequest
	socketPath := serveUnixHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("Decode() error = %v", err)
		}
		request = recordedRequest{Path: r.URL.Path, Body: body}
		w.WriteHeader(http.StatusNoContent)
	}))
	client, err := NewClient(socketPath)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	if err := client.SendCtrlAltDel(context.Background()); err != nil {
		t.Fatalf("SendCtrlAltDel() error = %v", err)
	}
	want := recordedRequest{
		Path: "/actions",
		Body: map[string]any{"action_type": "SendCtrlAltDel"},
	}
	if !reflect.DeepEqual(request, want) {
		t.Errorf("request = %#v, want %#v", request, want)
	}
}

func TestConfigureSnapshotTemplateStopsAtFirstAPIError(t *testing.T) {
	var paths []string
	socketPath := serveUnixHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/boot-source" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"fault_message":"bad kernel"}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	client, err := NewClient(socketPath)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	err = client.ConfigureSnapshotTemplate(context.Background(), validSnapshotTemplateConfig())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("ConfigureSnapshotTemplate() error = %v, want APIError", err)
	}
	wantPaths := []string{"/machine-config", "/hotplug/memory", "/boot-source"}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Errorf("paths = %#v, want %#v", paths, wantPaths)
	}
}

func TestConfigureSnapshotTemplateStopsAtNetworkAPIError(t *testing.T) {
	var paths []string
	socketPath := serveUnixHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/network-interfaces/eth0" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"fault_message":"missing TAP device"}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	client, err := NewClient(socketPath)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	err = client.ConfigureSnapshotTemplate(context.Background(), validSnapshotTemplateConfig())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("ConfigureSnapshotTemplate() error = %v, want APIError", err)
	}
	wantPaths := []string{
		"/machine-config",
		"/hotplug/memory",
		"/boot-source",
		"/drives/rootfs",
		"/drives/container_rootfs",
		"/network-interfaces/eth0",
	}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Errorf("paths = %#v, want %#v", paths, wantPaths)
	}
}

func TestConfigureSnapshotTemplateValidatesBeforeCallingAPI(t *testing.T) {
	tests := []struct {
		name   string
		change func(*SnapshotTemplateConfig)
	}{
		{name: "no vCPUs", change: func(c *SnapshotTemplateConfig) { c.VCPUCount = 0 }},
		{name: "no memory", change: func(c *SnapshotTemplateConfig) { c.MemoryMiB = 0 }},
		{name: "relative kernel", change: func(c *SnapshotTemplateConfig) { c.KernelImagePath = "vmlinux" }},
		{name: "relative rootfs", change: func(c *SnapshotTemplateConfig) { c.RootFSPath = "rootfs.ext4" }},
		{name: "missing container rootfs", change: func(c *SnapshotTemplateConfig) {
			c.ContainerRootFSPath = ""
		}},
		{name: "relative container rootfs", change: func(c *SnapshotTemplateConfig) {
			c.ContainerRootFSPath = "container-rootfs.ext4"
		}},
		{name: "blank boot args", change: func(c *SnapshotTemplateConfig) { c.BootArgs = " " }},
		{name: "invalid interface ID", change: func(c *SnapshotTemplateConfig) { c.NetworkInterface.InterfaceID = "eth/0" }},
		{name: "invalid host device name", change: func(c *SnapshotTemplateConfig) { c.NetworkInterface.HostDeviceName = "interface-name-is-too-long" }},
		{name: "invalid guest MAC", change: func(c *SnapshotTemplateConfig) { c.NetworkInterface.GuestMAC = "not-a-mac" }},
		{name: "multicast guest MAC", change: func(c *SnapshotTemplateConfig) { c.NetworkInterface.GuestMAC = "01:00:5e:00:00:01" }},
		{name: "missing network", change: func(c *SnapshotTemplateConfig) { c.NetworkInterface = nil }},
		{name: "reserved vsock CID", change: func(c *SnapshotTemplateConfig) { c.VSock.GuestCID = 2 }},
		{name: "relative vsock endpoint", change: func(c *SnapshotTemplateConfig) { c.VSock.UDSPath = "fire-fuse.vsock" }},
	}
	client, err := NewClient("/tmp/fire-fuse-missing.sock")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validSnapshotTemplateConfig()
			test.change(&config)
			if err := client.ConfigureSnapshotTemplate(context.Background(), config); !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("ConfigureSnapshotTemplate() error = %v, want ErrInvalidConfiguration", err)
			}
		})
	}
}

func validSnapshotTemplateConfig() SnapshotTemplateConfig {
	return SnapshotTemplateConfig{
		VCPUCount:           1,
		MemoryMiB:           128,
		HotplugMemoryMiB:    4096,
		KernelImagePath:     "/vmlinux",
		RootFSPath:          "/rootfs.ext4",
		ContainerRootFSPath: "/container-rootfs.ext4",
		BootArgs:            "console=ttyS0 reboot=k panic=1",
		NetworkInterface: &NetworkInterface{
			InterfaceID:    "eth0",
			GuestMAC:       "06:00:ac:10:00:02",
			HostDeviceName: "ff0123456789abc",
		},
		VSock: VSock{GuestCID: 3, UDSPath: "/run/fire-fuse.vsock"},
	}
}

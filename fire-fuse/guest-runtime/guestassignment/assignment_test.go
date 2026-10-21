package guestassignment

import (
	"errors"
	"strings"
	"testing"

	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestconfig"
)

func validAssignment() Assignment {
	return Assignment{
		SchemaVersion: SchemaVersion,
		AssignmentID:  "assignment-01",
		ClockUnixNano: 1785525535000000000,
		Network: Network{
			Address: "172.16.4.10",
			Gateway: "172.16.4.9",
			Prefix:  30,
		},
		Workload: guestconfig.Config{
			SchemaVersion: guestconfig.SchemaVersion,
			Process: guestconfig.Process{
				Args:             []string{"/bin/server"},
				Env:              []string{"PATH=/bin"},
				WorkingDirectory: "/",
				UID:              1000,
				GID:              1000,
			},
			Workspace: guestconfig.Workspace{
				Enabled:       true,
				ContainerPath: "/workspace",
			},
		},
		Workspace: Workspace{
			Enabled:    true,
			ID:         "workspace-01",
			Server:     "172.16.4.1",
			ExportPath: "/srv/fire-fuse/workspaces/workspace-01",
		},
	}
}

func TestAssignmentValidate(t *testing.T) {
	if err := validAssignment().Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestAssignmentValidateWithoutWorkspace(t *testing.T) {
	assignment := validAssignment()
	assignment.Workload.Workspace = guestconfig.Workspace{}
	assignment.Workspace = Workspace{}
	if err := assignment.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestAssignmentRejectsInvalidFields(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Assignment)
	}{
		{name: "schema", change: func(a *Assignment) { a.SchemaVersion++ }},
		{name: "assignment id", change: func(a *Assignment) { a.AssignmentID = "../escape" }},
		{name: "clock", change: func(a *Assignment) { a.ClockUnixNano = 0 }},
		{name: "address", change: func(a *Assignment) { a.Network.Address = "not-an-address" }},
		{name: "gateway", change: func(a *Assignment) { a.Network.Gateway = "172.16.5.1" }},
		{name: "same address", change: func(a *Assignment) { a.Network.Gateway = a.Network.Address }},
		{name: "prefix", change: func(a *Assignment) { a.Network.Prefix = 0 }},
		{name: "workload", change: func(a *Assignment) { a.Workload.Process.Args = nil }},
		{name: "workspace mismatch", change: func(a *Assignment) { a.Workspace.Enabled = false }},
		{name: "workspace id", change: func(a *Assignment) { a.Workspace.ID = "../escape" }},
		{name: "workspace server", change: func(a *Assignment) { a.Workspace.Server = "8.8.8.8" }},
		{name: "workspace export", change: func(a *Assignment) { a.Workspace.ExportPath = "/srv/other" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assignment := validAssignment()
			test.change(&assignment)
			if err := assignment.Validate(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestDecodeStrictlyAcceptsOneAssignment(t *testing.T) {
	document := `{
		"schema_version": 2,
		"assignment_id": "assignment-01",
		"clock_unix_nano": 1785525535000000000,
		"network": {
			"address": "172.16.4.10",
			"gateway": "172.16.4.9",
			"prefix": 30
		},
		"workload": {
			"schema_version": 1,
			"process": {
				"args": ["/bin/server"],
				"env": ["PATH=/bin"],
				"working_directory": "/",
				"uid": 1000,
				"gid": 1000
			},
			"workspace": {
				"enabled": true,
				"container_path": "/workspace"
			}
		},
		"workspace": {
			"enabled": true,
			"id": "workspace-01",
			"server": "172.16.4.1",
			"export_path": "/srv/fire-fuse/workspaces/workspace-01"
		}
	}`
	assignment, err := Decode(strings.NewReader(document))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if assignment.AssignmentID != "assignment-01" {
		t.Fatalf("AssignmentID = %q", assignment.AssignmentID)
	}
}

func TestDecodeRejectsUnknownAndTrailingDocuments(t *testing.T) {
	for _, document := range []string{
		`{"schema_version":1,"unknown":true}`,
		`{} {}`,
		strings.Repeat(" ", maxDocumentBytes+1),
	} {
		if _, err := Decode(strings.NewReader(document)); !errors.Is(err, ErrInvalid) {
			t.Errorf("Decode() error = %v, want ErrInvalid", err)
		}
	}
}

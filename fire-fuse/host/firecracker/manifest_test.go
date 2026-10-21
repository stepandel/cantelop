package firecracker

import (
	"errors"
	"os"
	"testing"
)

func TestValidateRuntimeManifestRejectsUntrustedRecords(t *testing.T) {
	tests := []struct {
		name   string
		record string
	}{
		{name: "future schema", record: `{"schema_version":2,"owner":"fire-fuse","vm_id":"vm-test"}`},
		{name: "wrong owner", record: `{"schema_version":1,"owner":"someone-else","vm_id":"vm-test"}`},
		{name: "wrong VM", record: `{"schema_version":1,"owner":"fire-fuse","vm_id":"vm-other"}`},
		{name: "unknown field", record: `{"schema_version":1,"owner":"fire-fuse","vm_id":"vm-test","path":"/tmp"}`},
		{name: "trailing document", record: `{"schema_version":1,"owner":"fire-fuse","vm_id":"vm-test"} {}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			layout, _, _, _ := testAssetLayout(t)
			if err := os.MkdirAll(layout.VMDir, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(layout.RuntimeManifest, []byte(test.record), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := layout.ValidateRuntimeManifest(); !errors.Is(err, ErrInvalidRuntimeManifest) {
				t.Fatalf("ValidateRuntimeManifest() error = %v, want ErrInvalidRuntimeManifest", err)
			}
		})
	}
}

func TestValidateRuntimeManifestRejectsSymlink(t *testing.T) {
	layout, _, _, _ := testAssetLayout(t)
	if err := os.MkdirAll(layout.VMDir, 0o750); err != nil {
		t.Fatal(err)
	}
	target := layout.RuntimeManifest + ".target"
	if err := os.WriteFile(target, []byte(`{"schema_version":1,"owner":"fire-fuse","vm_id":"vm-test"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, layout.RuntimeManifest); err != nil {
		t.Fatal(err)
	}
	if err := layout.ValidateRuntimeManifest(); !errors.Is(err, ErrInvalidRuntimeManifest) {
		t.Fatalf("ValidateRuntimeManifest() error = %v, want ErrInvalidRuntimeManifest", err)
	}
}

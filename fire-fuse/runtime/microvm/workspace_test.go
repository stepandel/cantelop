package microvm

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateWorkspaceID(t *testing.T) {
	tests := []struct {
		name string
		id   string
	}{
		{name: "short", id: "wsp_a"},
		{name: "descriptive", id: "wsp_project123"},
		{name: "underscore", id: "wsp_customer_workspace"},
		{name: "maximum length", id: "wsp_" + strings.Repeat("a", maxWorkspaceIDLength-len("wsp_"))},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateWorkspaceID(test.id); err != nil {
				t.Fatalf("ValidateWorkspaceID(%q) error = %v", test.id, err)
			}
		})
	}
}

func TestValidateWorkspaceIDRejectsUnsafeValues(t *testing.T) {
	tests := []struct {
		name string
		id   string
	}{
		{name: "empty"},
		{name: "absolute path", id: "/workspace"},
		{name: "relative path", id: "../workspace"},
		{name: "dot", id: "."},
		{name: "whitespace", id: "project 123"},
		{name: "leading hyphen", id: "-project"},
		{name: "leading underscore", id: "_project"},
		{name: "wrong prefix", id: "project123"},
		{name: "too long", id: strings.Repeat("a", maxWorkspaceIDLength+1)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateWorkspaceID(test.id)
			if !errors.Is(err, ErrInvalidWorkspaceID) {
				t.Fatalf(
					"ValidateWorkspaceID(%q) error = %v, want ErrInvalidWorkspaceID",
					test.id,
					err,
				)
			}
		})
	}
}

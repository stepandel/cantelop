package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestRunRejectsRetiredHostAdministrationCommands(t *testing.T) {
	for _, command := range [][]string{
		{"list"},
		{"set-status", "usr_alice", "suspended"},
		{"set-quota", "usr_alice", "2", "3", "4", "4096"},
	} {
		if err := run(context.Background(), command, io.Discard, nil); err == nil {
			t.Errorf("run(%q) accepted retired host administration command", command)
		}
	}
}

func TestVersionDoesNotRequireCredentials(t *testing.T) {
	previousVersion, previousCommit := version, commit
	t.Cleanup(func() { version, commit = previousVersion, previousCommit })
	version, commit = "0.1.0-rc.1", "0123456789ab"
	for _, argument := range []string{"version", "--version", "-v"} {
		t.Run(argument, func(t *testing.T) {
			var output bytes.Buffer
			if err := run(context.Background(), []string{argument}, &output, nil); err != nil {
				t.Fatalf("run(%s) error = %v", argument, err)
			}
			if output.String() != "cantelop 0.1.0-rc.1 (0123456789ab)\n" {
				t.Fatalf("version output = %q", output.String())
			}
		})
	}
}

func TestHelpDoesNotRequireCredentials(t *testing.T) {
	for _, argument := range []string{"--help", "-h", "help"} {
		t.Run(argument, func(t *testing.T) {
			var output bytes.Buffer
			if err := run(context.Background(), []string{argument}, &output, nil); err != nil {
				t.Fatalf("run(%s) error = %v", argument, err)
			}
			for _, expected := range []string{
				"Usage:\n  cantelop <command> [options]",
				"Project:",
				"env sync",
				"traces [ID]",
				"sandboxes [ID]",
				"session inspect SESSION_ID",
				"app secret",
				"-h, --help",
				"-v, --version",
			} {
				if !strings.Contains(output.String(), expected) {
					t.Errorf("help output does not contain %q:\n%s", expected, output.String())
				}
			}
		})
	}
}

func TestUnknownCommandPrintsHelp(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "top level", args: []string{"frobnicate"}, want: `unknown command "frobnicate"`},
		{name: "env", args: []string{"env", "frobnicate"}, want: `unknown env command "frobnicate"`},
		{name: "app", args: []string{"app", "frobnicate"}, want: `unknown app command "frobnicate"`},
		{name: "session", args: []string{"session", "frobnicate"}, want: `unknown session command "frobnicate"`},
		{name: "workspace", args: []string{"workspace", "frobnicate"}, want: `unknown command "workspace"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			err := run(context.Background(), test.args, &output, nil)
			if err == nil || err.Error() != test.want {
				t.Fatalf("run(%q) error = %v, want %q", test.args, err, test.want)
			}
			if output.String() != helpText {
				t.Fatalf("run(%q) output = %q, want help text", test.args, output.String())
			}
		})
	}
}

func TestDevCommandDoesNotRequireCredentials(t *testing.T) {
	err := run(context.Background(), []string{"dev", "-port", "0"}, io.Discard, nil)
	if err == nil || !strings.Contains(err.Error(), "usage: cantelop dev") {
		t.Fatalf("run(dev -port 0) error = %v", err)
	}
}

func TestInitCommandDoesNotRequireCredentials(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "cantelop.json")
	if err := run(context.Background(), []string{"init", "-app", "vera", "-provider", "openai", "-config", path}, io.Discard, nil); err != nil {
		t.Fatalf("run(init) error = %v", err)
	}
}

func TestBuildCommandDoesNotRequireCredentials(t *testing.T) {
	err := run(context.Background(), []string{"build", "-config", ""}, io.Discard, nil)
	if err == nil || !strings.Contains(err.Error(), "usage: cantelop build") {
		t.Fatalf("run(build -config '') error = %v", err)
	}
}

func TestDeployDryRunDoesNotRequireCredentials(t *testing.T) {
	t.Setenv("CANTELOP_CONFIG", filepath.Join(t.TempDir(), "missing-credentials.json"))
	missingProject := filepath.Join(t.TempDir(), "missing-cantelop.json")
	err := run(context.Background(), []string{"deploy", "--dry-run", "-config", missingProject}, io.Discard, nil)
	if err == nil || strings.Contains(err.Error(), "not logged in") || !strings.Contains(err.Error(), "project configuration") {
		t.Fatalf("run(deploy --dry-run) error = %v", err)
	}
}

func TestCommandTableDrivesUsageAndHelp(t *testing.T) {
	err := run(context.Background(), nil, io.Discard, nil)
	if err == nil || err.Error() != commandUsage() {
		t.Fatalf("run() error = %v, want the generated usage", err)
	}
	for _, form := range []string{"|database migrations|", "|routes|", "|env sync|", "|app credentials|"} {
		if !strings.Contains(commandUsage()+"|", form) {
			t.Errorf("usage %q does not list %q", commandUsage(), form)
		}
	}
	for _, command := range cliCommands {
		if !strings.Contains(helpText, "\n  "+command.name) {
			t.Errorf("help does not describe %q", command.name)
		}
	}
	if !strings.HasPrefix(helpText, "Cantelop CLI\n\nBuild, deploy, and operate Cantelop Apps.\n") ||
		!strings.Contains(helpText, "  database migrations WORKSPACE_ID\n                Inspect system and application migration history\n") {
		t.Fatalf("help text is misordered:\n%s", helpText)
	}
}

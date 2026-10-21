package main

import (
	"context"
	"flag"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestParseErrorsNameTheProblemAndAppendUsage(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{args: []string{"deploy", "--frobnicate"}, want: "flag provided but not defined: -frobnicate\nusage: cantelop deploy "},
		{args: []string{"deploy", "extra"}, want: "unexpected argument \"extra\"\nusage: cantelop deploy "},
		{args: []string{"logs", "--config"}, want: "flag needs an argument: -config\nusage: cantelop logs "},
		{args: []string{"releases", "--follow"}, want: "flag provided but not defined: -follow\nusage: cantelop releases "},
		{args: []string{"dev", "-port", "many"}, want: "invalid value \"many\" for flag -port: parse error\nusage: cantelop dev "},
	} {
		err := run(context.Background(), test.args, io.Discard, nil)
		if err == nil || !strings.HasPrefix(err.Error(), test.want) {
			t.Errorf("run(%q) error = %v, want prefix %q", test.args, err, test.want)
		}
	}
	if err := run(context.Background(), []string{"deploy", "-h"}, io.Discard, nil); err == nil || !strings.HasPrefix(err.Error(), "usage: cantelop deploy ") {
		t.Errorf("run(deploy -h) error = %v, want the bare usage", err)
	}
}

func TestOperationalOptionsAcceptInterspersedFlags(t *testing.T) {
	options, err := parseOperationalOptions([]string{"--json", "ses_one", "--config=other.json", "--follow"}, true, 1, "usage")
	if err != nil || !options.jsonOutput || !options.follow || options.configPath != "other.json" || !slices.Equal(options.positionals, []string{"ses_one"}) {
		t.Fatalf("options = %+v, err = %v", options, err)
	}
	options, err = parseOperationalOptions([]string{"GET", "-config", "c.json", "/users"}, false, -1, "usage")
	if err != nil || options.configPath != "c.json" || !slices.Equal(options.positionals, []string{"GET", "/users"}) {
		t.Fatalf("options = %+v, err = %v", options, err)
	}
	for _, args := range [][]string{{"--config", ""}, {"one", "two"}, {}} {
		if _, err := parseOperationalOptions(args, false, 1, "usage"); err == nil || err.Error() != "usage" {
			t.Errorf("parseOperationalOptions(%q) error = %v, want usage", args, err)
		}
	}
}

func TestInterspersedFlagsStopAtTerminator(t *testing.T) {
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	verbose := flags.Bool("verbose", false, "")
	positionals, err := parseInterspersedFlags(flags, []string{"a", "--verbose", "--", "--b", "c"}, "usage")
	if err != nil || !*verbose || !slices.Equal(positionals, []string{"a", "--b", "c"}) {
		t.Fatalf("positionals = %q, verbose = %v, err = %v", positionals, *verbose, err)
	}
}

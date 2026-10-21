package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"strings"

	"github.com/stepandel/cantelop/clients/cli/internal/devruntime"
	"github.com/stepandel/cantelop/clients/cli/internal/projectconfig"
)

func runProjectDev(ctx context.Context, args []string, output io.Writer) error {
	const usage = "usage: cantelop dev [--container] [--app name] [-config cantelop.json] [-port 8787] [-env-file .env]"
	flags := flag.NewFlagSet("cantelop dev", flag.ContinueOnError)
	configPath := flags.String("config", projectconfig.DefaultPath, "project configuration path")
	app := flags.String("app", "", "App name when the definition contains multiple Apps")
	port := flags.Int("port", devruntime.DefaultAPIPort, "local API port")
	environmentPath := flags.String("env-file", ".env", "local App environment file")
	containerMode := flags.Bool("container", false, "run Session runtime Sandboxes in Docker")
	if err := parseFlags(flags, args, usage); err != nil {
		return err
	}
	if strings.TrimSpace(*configPath) == "" ||
		*port < 1 || *port > 65535 || strings.TrimSpace(*environmentPath) == "" {
		return errors.New(usage)
	}
	return devruntime.Run(ctx, devruntime.Options{
		ConfigPath: *configPath, App: *app, Port: *port, Container: *containerMode,
		EnvironmentPath: *environmentPath, EnvironmentPathSet: flagWasSet(flags, "env-file"),
	}, output)
}

func flagWasSet(flags *flag.FlagSet, name string) bool {
	set := false
	flags.Visit(func(flag *flag.Flag) {
		if flag.Name == name {
			set = true
		}
	})
	return set
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/host/hostpath"
	"github.com/stepandel/cantelop/fire-fuse/host/preflight"
	"github.com/stepandel/cantelop/fire-fuse/host/snapshotbuilder"
	"github.com/stepandel/cantelop/fire-fuse/host/snapshotbundle"
	"github.com/stepandel/cantelop/fire-fuse/runtime/sandboxprofile"
)

const defaultConfigPath = "/etc/fire-fuse/config.json"

type buildFunc func(context.Context, snapshotbuilder.Config) (snapshotbundle.Manifest, error)

type dependencies struct {
	loadConfig    func(string) (config.Config, error)
	checkHost     func(config.Config, string) error
	checkFile     func(string, string) error
	checkDir      func(string, string) error
	build         buildFunc
	output        io.Writer
	processOutput io.Writer
}

type options struct {
	configPath  string
	placeholder string
	destination string
}

func main() {
	if err := run(context.Background(), os.Args[1:], productionDependencies()); err != nil {
		fmt.Fprintln(os.Stderr, "fire-fuse-snapshot:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, deps dependencies) error {
	opts, err := parseOptions(args)
	if err != nil {
		return err
	}
	cfg, err := deps.loadConfig(opts.configPath)
	if err != nil {
		return fmt.Errorf("load trusted configuration: %w", err)
	}
	profile, err := cfg.GetProfile(sandboxprofile.DefaultName)
	if err != nil {
		return err
	}
	if err := deps.checkHost(cfg, sandboxprofile.DefaultName); err != nil {
		return fmt.Errorf("check host prerequisites: %w", err)
	}
	if err := deps.checkFile("snapshot placeholder", opts.placeholder); err != nil {
		return fmt.Errorf("validate placeholder: %w", err)
	}
	if err := deps.checkDir("snapshot destination parent", filepath.Dir(opts.destination)); err != nil {
		return fmt.Errorf("validate destination parent: %w", err)
	}
	manifest, err := deps.build(ctx, snapshotbuilder.Config{
		FirecrackerBin:    cfg.FirecrackerBin,
		JailerBin:         cfg.JailerBin,
		IPBin:             cfg.Network.IPBin,
		RuntimeDir:        cfg.RuntimeDir,
		KernelImage:       profile.KernelImage,
		PlatformRootFS:    profile.RootFSImage,
		Placeholder:       opts.placeholder,
		Destination:       opts.destination,
		JailerUID:         cfg.JailerUID,
		JailerGID:         cfg.JailerGID,
		VCPUCount:         sandboxprofile.DefaultVCPUs,
		MemoryMiB:         sandboxprofile.DefaultMemoryMiB,
		HotplugMemoryMiB:  sandboxprofile.HotplugMemoryMiB,
		BootArgs:          profile.BootArgs,
		APISocketTimeout:  time.Duration(cfg.Timeouts.APISocket),
		ShutdownGrace:     time.Duration(cfg.Timeouts.ShutdownGrace),
		ShutdownForce:     time.Duration(cfg.Timeouts.ShutdownForce),
		GuestReadyTimeout: time.Duration(cfg.Timeouts.Startup),
		CleanupTimeout:    time.Duration(cfg.Timeouts.Cleanup),
		Output:            deps.processOutput,
	})
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(deps.output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		return fmt.Errorf("write snapshot manifest: %w", err)
	}
	return nil
}

func parseOptions(args []string) (options, error) {
	var opts options
	flags := flag.NewFlagSet("fire-fuse-snapshot", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.configPath, "config", defaultConfigPath, "trusted host configuration")
	flags.StringVar(&opts.placeholder, "placeholder", "", "placeholder container rootfs image")
	flags.StringVar(&opts.destination, "output", "", "new immutable snapshot bundle directory")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 {
		return options{}, errors.New("positional arguments are not supported")
	}
	for _, required := range []struct {
		name  string
		value string
	}{
		{name: "placeholder", value: opts.placeholder},
		{name: "output", value: opts.destination},
	} {
		if required.value == "" {
			return options{}, fmt.Errorf("-%s is required", required.name)
		}
	}
	for _, path := range []struct {
		name  string
		value string
	}{
		{name: "placeholder", value: opts.placeholder},
		{name: "output", value: opts.destination},
	} {
		if !filepath.IsAbs(path.value) || filepath.Clean(path.value) != path.value ||
			path.value == string(filepath.Separator) {
			return options{}, fmt.Errorf("-%s must be a clean absolute path", path.name)
		}
	}
	return opts, nil
}

func productionDependencies() dependencies {
	return dependencies{
		loadConfig:    config.LoadTrusted,
		checkHost:     preflight.CheckSnapshotBuilder,
		checkFile:     hostpath.CheckTrustedFile,
		checkDir:      hostpath.CheckTrustedDirectory,
		build:         snapshotbuilder.Build,
		output:        os.Stdout,
		processOutput: os.Stderr,
	}
}

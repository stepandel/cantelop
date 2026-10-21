package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/runtime/imagecatalog"
)

const defaultConfigPath = "/etc/fire-fuse/config.json"

type dependencies struct {
	loadConfig func(string) (config.Config, error)
	publish    func(string, string) (imagecatalog.Result, error)
	retire     func(string, string, string) (imagecatalog.RetirementResult, error)
	collect    func(string, string, string) (imagecatalog.CollectionResult, error)
}

type commandResult struct {
	BuildID     string `json:"build_id,omitempty"`
	RootFSImage string `json:"rootfs_image,omitempty"`
	Changed     bool   `json:"changed"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, dependencies{
		loadConfig: config.LoadTrusted,
		publish:    imagecatalog.Publish,
		retire:     imagecatalog.Retire,
		collect:    imagecatalog.CollectRootFS,
	}))
}

func run(args []string, stdout, stderr io.Writer, deps dependencies) int {
	flags := flag.NewFlagSet("fire-fuse-image", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", defaultConfigPath, "trusted host configuration path for offline operations")
	manifestPath := flags.String("manifest", "", "trusted image manifest to publish")
	retireID := flags.String("retire", "", "unreferenced build ID to retire while fire-fuse is stopped")
	collectPath := flags.String("gc-rootfs", "", "unpublished immutable rootfs to delete while fire-fuse is stopped")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage:")
		fmt.Fprintln(stderr, "  fire-fuse-image -manifest /absolute/path/image.json [-config /etc/fire-fuse/config.json]")
		fmt.Fprintln(stderr, "  fire-fuse-image -retire sha256:DIGEST [-config /etc/fire-fuse/config.json]")
		fmt.Fprintln(stderr, "  fire-fuse-image -gc-rootfs /var/lib/fire-fuse/images/sha256-DIGEST.ext4 [-config /etc/fire-fuse/config.json]")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	operationCount := 0
	for _, selected := range []bool{*manifestPath != "", *retireID != "", *collectPath != ""} {
		if selected {
			operationCount++
		}
	}
	if flags.NArg() != 0 || operationCount != 1 {
		flags.Usage()
		return 2
	}
	cfg, err := deps.loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "load trusted host configuration: %v\n", err)
		return 1
	}

	var output commandResult
	if *manifestPath != "" {
		result, err := deps.publish(cfg.ImageCatalog, *manifestPath)
		if err != nil {
			fmt.Fprintf(stderr, "publish image: %v\n", err)
			return 1
		}
		output = commandResult{BuildID: result.BuildID, Changed: result.Changed}
	} else if *retireID != "" {
		result, err := deps.retire(cfg.ImageCatalog, cfg.StateDB, *retireID)
		if err != nil {
			fmt.Fprintf(stderr, "retire image: %v\n", err)
			return 1
		}
		output = commandResult{
			BuildID:     result.BuildID,
			RootFSImage: result.RootFSImage,
			Changed:     result.Changed,
		}
	} else {
		result, err := deps.collect(cfg.ImageCatalog, cfg.StateDB, *collectPath)
		if err != nil {
			fmt.Fprintf(stderr, "collect image rootfs: %v\n", err)
			return 1
		}
		output = commandResult{RootFSImage: result.RootFSImage, Changed: result.Changed}
	}

	if err := json.NewEncoder(stdout).Encode(output); err != nil {
		fmt.Fprintf(stderr, "write result: %v\n", err)
		return 1
	}
	return 0
}

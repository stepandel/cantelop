package main

import (
	"fmt"
	"os"

	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestconfig"
	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestrunner"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fire-fuse-guest: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	file, err := os.Open(guestrunner.ConfigPath)
	if err != nil {
		return fmt.Errorf("open workload configuration: %w", err)
	}
	config, decodeErr := guestconfig.Decode(file)
	closeErr := file.Close()
	if decodeErr != nil {
		return fmt.Errorf("decode workload configuration: %w", decodeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close workload configuration: %w", closeErr)
	}
	return guestrunner.Run(config)
}

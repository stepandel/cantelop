package main

import (
	"context"
	"fmt"
	"os"

	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestassignment"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fire-fuse-assignment-agent: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	listener, err := guestassignment.ListenVSock(guestassignment.VSockPort)
	if err != nil {
		return err
	}
	defer listener.Close()
	return guestassignment.Serve(
		context.Background(),
		listener,
		guestassignment.Apply,
	)
}

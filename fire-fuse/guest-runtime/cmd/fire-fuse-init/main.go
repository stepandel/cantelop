package main

import (
	"fmt"
	"os"

	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestinit"
)

func main() {
	if err := guestinit.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "fire-fuse-init: %v\n", err)
		os.Exit(1)
	}
}

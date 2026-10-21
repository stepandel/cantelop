package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/stepandel/cantelop/clients/cli/internal/devruntime"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	// One signal-aware context for every command. The first interrupt cancels
	// it so commands can stop and clean up; once it is cancelled, default
	// signal handling returns so a second interrupt still terminates the CLI.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	err := run(ctx, os.Args[1:], os.Stdout, &http.Client{Timeout: 10 * time.Second})
	interrupted := ctx.Err() != nil
	stop()
	if err != nil {
		if interrupted && errors.Is(err, context.Canceled) {
			os.Exit(130)
		}
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	if len(args) > 0 && args[0] == "internal-sync-files" {
		return devruntime.RunSyncFiles(ctx, args[1:], os.Stdin, output)
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		_, err := io.WriteString(output, helpText)
		return err
	}
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-v") {
		_, err := fmt.Fprintf(output, "cantelop %s (%s)\n", version, commit)
		return err
	}
	if err := unknownCommand(args); err != nil {
		if _, writeErr := io.WriteString(output, helpText); writeErr != nil {
			return writeErr
		}
		return err
	}
	return runCustomer(ctx, args, output, client)
}

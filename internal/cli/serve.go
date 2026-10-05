// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/muster-io/muster/internal/runtime"
)

// The runtime entry points, the environment and the signal context, replaced by tests.
var (
	runServer  = runtime.Run
	runMigrate = runtime.Migrate
	environ    = os.Environ

	// signals ends its context on SIGTERM or an interrupt.
	signals = func() (context.Context, context.CancelFunc) {
		return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	}
)

// runServe is muster without a command: the server, until SIGTERM or an interrupt, after which it exits 0.
func runServe(stdout, stderr io.Writer) int {
	ctx, stop := signals()
	defer stop()
	// After the first signal, a second one kills the process as usual.
	context.AfterFunc(ctx, stop)
	return report(stderr, runServer(ctx, runtime.Options{Environ: environ(), Stdout: stdout}))
}

// report prints err, which never carries a secret, and returns the exit code.
func report(stderr io.Writer, err error) int {
	if err != nil {
		fmt.Fprintf(stderr, "muster: %v\n", err)
		return exitFailure
	}
	return exitOK
}

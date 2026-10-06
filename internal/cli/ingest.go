// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/runtime"
)

const ingestUsage = `Usage: muster ingest replay --since <duration> [--integration <name>] --actor <name>

Reprocesses Stored Snapshots (C-06.FR-17). --since is the period back from now, such as 1h or 90m, within the
retention of Stored Snapshots; --integration limits the replay to the Integration with that name; --actor names the
person who runs the command and is required. The command sets the Stored Snapshots of the period back to pending in one
transaction and records ingest.replayed in the Audit log with the --actor name and the Transport cli; the running
replicas process them again in arrival order. Processing is idempotent per Alert, so Snapshots that were processed
change nothing, while one that failed is processed with the current code.
`

// runReplayIngest is the runtime entry point, replaced by tests.
var runReplayIngest = runtime.ReplayIngest

// runIngest is `muster ingest <command>`; development is set under `muster dev`, whose development clock the command
// then follows.
func runIngest(args []string, stdout, stderr io.Writer, development bool) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, ingestUsage)
		return exitUsage
	}
	switch args[0] {
	case "replay":
		return runReplayCommand(args[1:], stdout, stderr, development)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, ingestUsage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "muster ingest: unknown command %q\n\n%s", args[0], ingestUsage)
		return exitUsage
	}
}

// runReplayCommand is `muster ingest replay`. Without --actor, without a positive --since or with arguments after the
// flags, it exits 2 before it connects, so nothing changes.
func runReplayCommand(args []string, stdout, stderr io.Writer, development bool) int {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	actor := fs.String("actor", "", "")
	since := fs.String("since", "", "")
	integration := fs.String("integration", "", "")
	if err := fs.Parse(args); err != nil {
		return ingestUsageError(stderr, err.Error())
	}
	name := strings.TrimSpace(*actor)
	period, err := time.ParseDuration(strings.TrimSpace(*since))
	switch {
	case name == "":
		return ingestUsageError(stderr, "--actor is required")
	case *since == "":
		return ingestUsageError(stderr, "--since is required")
	case err != nil || period <= 0:
		return ingestUsageError(stderr, "--since is a positive duration such as 1h or 90m")
	case fs.NArg() != 0:
		return ingestUsageError(stderr, "replay takes no arguments after the flags")
	}
	ctx, stop := signals()
	defer stop()
	context.AfterFunc(ctx, stop)
	out, err := runReplayIngest(ctx, runtime.Options{Environ: environ(), Stdout: stdout, Development: development},
		runtime.IngestReplay{Since: period, SinceText: strings.TrimSpace(*since),
			Integration: strings.TrimSpace(*integration), Actor: name})
	if errors.Is(err, ingest.ErrUnknownIntegration) {
		fmt.Fprintf(stderr, "muster ingest replay: %v\n", err)
		return exitUsage
	}
	if err != nil {
		fmt.Fprintf(stderr, "muster ingest replay: %v\n", err)
		return exitFailure
	}
	of := "every Integration"
	if out.Integration != nil {
		of = out.Integration.Name
	}
	fmt.Fprintf(stderr, "replayed %d Stored Snapshots of %s\n", out.Count, of)
	return exitOK
}

func ingestUsageError(stderr io.Writer, msg string) int {
	fmt.Fprintf(stderr, "muster ingest replay: %s\n\n%s", msg, ingestUsage)
	return exitUsage
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/runtime"
)

const devUsage = `Usage: muster dev [--replica]
       muster dev <command> [arguments]

Development mode. Every MUSTER_* variable with a development default takes it unless the variable is set; a set
variable replaces its default.

  muster dev             start the fake Alertmanager, Mattermost and Telegram servers
                         on 127.0.0.1:19093, 127.0.0.1:18065 and 127.0.0.1:18081, then Muster against the
                         development database (make dev-db), migrated on start
  muster dev --replica   run an additional replica on :9080, :9081 and :9082, without fake servers
  muster dev <command>   run another command with the development defaults, such as muster dev migrate
`

// devMode holds what `muster dev` takes from the process, so tests can replace it.
type devMode struct {
	env    devmode.Env
	fakes  devmode.Addresses
	notify func() (context.Context, context.CancelFunc)
	// server runs Muster with the development defaults, writing its log lines to stdout.
	server func(ctx context.Context, stdout io.Writer) error
}

func runDev(args []string, stdout, stderr io.Writer) int {
	return devMode{
		env:    devmode.OSEnv{},
		fakes:  devmode.FakeAddresses(),
		notify: signals,
		server: func(ctx context.Context, stdout io.Writer) error {
			return runServer(ctx, runtime.Options{Environ: environ(), Stdout: stdout, Development: true})
		},
	}.run(args, stdout, stderr)
}

func (d devMode) run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return d.serve(false, stdout, stderr)
	}
	switch arg := args[0]; {
	case arg == "--replica" || arg == "-replica":
		if len(args) > 1 {
			return devUsageError(stderr, "--replica takes no arguments")
		}
		return d.serve(true, stdout, stderr)
	case arg == "-h" || arg == "-help" || arg == "--help":
		fmt.Fprint(stdout, devUsage)
		return exitOK
	case strings.HasPrefix(arg, "-"):
		return devUsageError(stderr, fmt.Sprintf("unknown flag %q", arg))
	case arg == "dev":
		return devUsageError(stderr, "dev cannot run dev")
	}
	if _, err := devmode.Apply(d.env, false); err != nil {
		fmt.Fprintf(stderr, "muster dev: %v\n", err)
		return exitFailure
	}
	return run(args, stdout, stderr, true)
}

func (d devMode) serve(replica bool, stdout, stderr io.Writer) int {
	applied, err := devmode.Apply(d.env, replica)
	if err != nil {
		fmt.Fprintf(stderr, "muster dev: %v\n", err)
		return exitFailure
	}
	printApplied(stdout, applied)
	ctx, stop := d.notify()
	defer stop()
	serve := func(ctx context.Context) error { return d.server(ctx, stdout) }
	if replica {
		err = devmode.RunReplica(ctx, stdout, serve)
	} else {
		err = devmode.Run(ctx, stdout, d.fakes, serve)
	}
	if err != nil {
		fmt.Fprintf(stderr, "muster dev: %v\n", err)
		return exitFailure
	}
	return exitOK
}

// printApplied names the variables, never their values.
func printApplied(w io.Writer, a devmode.Applied) {
	if len(a.Defaults) > 0 {
		names := make([]string, len(a.Defaults))
		for i, name := range a.Defaults {
			if name == "MUSTER_SECRET_KEYS" {
				name += " (the published development key)"
			}
			names[i] = name
		}
		fmt.Fprintf(w, "muster dev: development defaults for %s\n", strings.Join(names, ", "))
	}
	if len(a.FromEnv) > 0 {
		fmt.Fprintf(w, "muster dev: from the environment: %s\n", strings.Join(a.FromEnv, ", "))
	}
}

func devUsageError(stderr io.Writer, msg string) int {
	fmt.Fprintf(stderr, "muster dev: %s\n\n%s", msg, devUsage)
	return exitUsage
}

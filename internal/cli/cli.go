// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package cli parses the command line of the muster binary and runs its subcommands.
package cli

import (
	"fmt"
	"io"
	"os"
)

const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

const usage = `Usage: muster [command]

Without a command, muster runs the server: settings from the MUSTER_* variables, the database checks, the migrations
when MUSTER_MIGRATE_ON_START is true, then the app, ingest and internal listeners until SIGTERM.

Commands:
  migrate   Apply the database migrations under the migration lock
  doctor    Check the database, its connections, the master keys and the clock; only reads
  admin     Emergency access with --actor <name> <login>: admin reset-password sets a password read from standard
            input, admin reset-totp removes a lost second factor
  dev       Development mode: fake servers and development defaults (muster dev [--replica] | muster dev <command>)
  version   Print the version and commit
  help      Show this help
`

func Main() int {
	return Run(os.Args[1:], os.Stdout, os.Stderr)
}

func Run(args []string, stdout, stderr io.Writer) int {
	return run(args, stdout, stderr, false)
}

// run runs a command; development is set when `muster dev <command>` runs it with the development defaults.
func run(args []string, stdout, stderr io.Writer, development bool) int {
	if len(args) == 0 {
		return runServe(stdout, stderr)
	}
	switch args[0] {
	case "migrate":
		if len(args) > 1 {
			fmt.Fprintf(stderr, "muster: migrate takes no arguments\n\n%s", usage)
			return exitUsage
		}
		return runMigrateCommand(stdout, stderr)
	case "doctor":
		if len(args) > 1 {
			fmt.Fprintf(stderr, "muster: doctor takes no arguments\n\n%s", usage)
			return exitUsage
		}
		return runDoctor(stdout, stderr, development)
	case "dev":
		return runDev(args[1:], stdout, stderr)
	case "admin":
		return runAdmin(args[1:], stdout, stderr)
	case "version":
		if len(args) > 1 {
			fmt.Fprintf(stderr, "muster: version takes no arguments\n\n%s", usage)
			return exitUsage
		}
		return runVersion(stdout)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "muster: unknown command %q\n\n%s", args[0], usage)
		return exitUsage
	}
}

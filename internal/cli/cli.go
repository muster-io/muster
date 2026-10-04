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
	exitOK    = 0
	exitUsage = 2
)

const usage = `Usage: muster <command>

Commands:
  version   Print the version and commit
  help      Show this help
`

func Main() int {
	return Run(os.Args[1:], os.Stdout, os.Stderr)
}

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "version":
		return runVersion(stdout)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "muster: unknown command %q\n\n%s", args[0], usage)
		return exitUsage
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build lint

// Command muster-archlint runs the architecture lints 1 to 4 of ADR-0016 over a module tree. Rule 5 runs as a test
// of internal/archlint, and rules 6 to 8 run in golangci-lint; `make lint-arch` runs all of them.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/muster-io/muster/internal/archlint"
)

const (
	exitFindings = 1
	exitError    = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("muster-archlint", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "root `directory` of the module to check")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return exitError
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "muster-archlint: unexpected arguments %q\n", flags.Args())
		return exitError
	}
	diags, err := archlint.Run(*root, archlint.DefaultConfig())
	if err != nil {
		fmt.Fprintf(stderr, "muster-archlint: %v\n", err)
		return exitError
	}
	for _, d := range diags {
		fmt.Fprintln(stdout, d)
	}
	if len(diags) > 0 {
		fmt.Fprintf(stderr, "muster-archlint: %d finding(s); a false positive is fixed in the lint, not worked around in the code\n", len(diags))
		return exitFindings
	}
	return 0
}

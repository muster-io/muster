// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package main

import (
	"os"

	"github.com/muster-io/muster/internal/cli"
)

func main() {
	os.Exit(cli.Main())
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package cli

import (
	"fmt"
	"io"

	"github.com/muster-io/muster/internal/buildinfo"
)

func runVersion(stdout io.Writer) int {
	fmt.Fprintf(stdout, "muster %s (commit %s)\n", buildinfo.Version, buildinfo.Commit)
	return exitOK
}

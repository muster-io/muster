// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package cli

import (
	"context"
	"io"

	"github.com/muster-io/muster/internal/runtime"
)

// runMigrateCommand is `muster migrate`: the migrations under the migration lock, logged as migrations_applied. It
// takes no --actor (C-02.FR-15) and exits non-zero on a schema newer than the binary knows.
func runMigrateCommand(stdout, stderr io.Writer) int {
	ctx, stop := signals()
	defer stop()
	context.AfterFunc(ctx, stop)
	return report(stderr, runMigrate(ctx, runtime.Options{Environ: environ(), Stdout: stdout}))
}

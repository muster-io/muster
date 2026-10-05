// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package cli

import (
	"context"
	"io"

	"github.com/muster-io/muster/internal/doctor"
)

// runDoctorChecks is internal/doctor, replaced by tests.
var runDoctorChecks = doctor.Run

// runDoctor is `muster doctor`: one line per check on stdout, exit 1 when a check failed or the settings cannot be
// read. It only reads and takes no --actor (C-02.FR-15). Only `muster dev doctor` accepts the development key.
func runDoctor(stdout, stderr io.Writer, development bool) int {
	ctx, stop := signals()
	defer stop()
	context.AfterFunc(ctx, stop)
	ok, err := runDoctorChecks(ctx, doctor.Options{Environ: environ(), Out: stdout, Development: development})
	if code := report(stderr, err); code != exitOK {
		return code
	}
	if !ok {
		return exitFailure
	}
	return exitOK
}

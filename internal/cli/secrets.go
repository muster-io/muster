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

	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/runtime"
)

const rotateKeyUsage = `Usage: muster secrets rotate-key --activate <id> --actor <name>

Activates a master key (C-20.FR-7). Add the new key to MUSTER_SECRET_KEYS of every replica first, next to the
current one, and roll the change out: the activation is refused while a live replica does not hold the key, and when
this process does not hold it. --activate is the key id the Keyring status shows; --actor names the person who runs
the command and is required.

In one transaction, the key becomes active, the key canary and every stored Secret are encrypted with it, and
keyring.key_activated is recorded in the Audit log with the --actor name and the Transport cli. The replicas switch to
the key without a restart, and their Leader re-signs the buttons of the open Root messages. Remove the old key from
MUSTER_SECRET_KEYS once the Keyring status shows it as removable.
`

// runRotateKey is the runtime entry point, replaced by tests.
var runRotateKey = runtime.RotateKey

// runSecrets is `muster secrets <command>`; development is set under `muster dev`, whose development clock the
// command then follows.
func runSecrets(args []string, stdout, stderr io.Writer, development bool) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, rotateKeyUsage)
		return exitUsage
	}
	switch args[0] {
	case "rotate-key":
		return runRotateKeyCommand(args[1:], stdout, stderr, development)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, rotateKeyUsage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "muster secrets: unknown command %q\n\n%s", args[0], rotateKeyUsage)
		return exitUsage
	}
}

// runRotateKeyCommand is `muster secrets rotate-key --activate <id> --actor <name>` (RotateKeyCommand) with the
// process environment and runtime.RotateKey.
func runRotateKeyCommand(args []string, stdout, stderr io.Writer, development bool) int {
	ctx, stop := signals()
	defer stop()
	context.AfterFunc(ctx, stop)
	return RotateKeyCommand(ctx, args, runtime.Options{Environ: environ(), Stdout: stdout, Development: development},
		runRotateKey, stderr)
}

// RotateKeyCommand is `muster secrets rotate-key --activate <id> --actor <name>` with the options opts and the
// activation rotate; the secret-leak probe of lint 5 runs it with an activation that needs no database. Without
// --activate or --actor, or with arguments after the flags, it exits 2 before rotate, so nothing changes. A refusal — a
// live replica that lacks the key, or a key this process does not hold — exits 1, as does any other failure.
func RotateKeyCommand(ctx context.Context, args []string, opts runtime.Options,
	rotate func(context.Context, runtime.Options, runtime.KeyRotation) (keyring.Rotated, error), stderr io.Writer,
) int {
	fs := flag.NewFlagSet("rotate-key", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	activate := fs.String("activate", "", "")
	actor := fs.String("actor", "", "")
	if err := fs.Parse(args); err != nil {
		return rotateKeyUsageError(stderr, err.Error())
	}
	id, name := strings.TrimSpace(*activate), strings.TrimSpace(*actor)
	switch {
	case id == "":
		return rotateKeyUsageError(stderr, "--activate is required")
	case name == "":
		return rotateKeyUsageError(stderr, "--actor is required")
	case fs.NArg() != 0:
		return rotateKeyUsageError(stderr, "rotate-key takes no arguments after the flags")
	}
	out, err := rotate(ctx, opts, runtime.KeyRotation{KeyID: id, Actor: name})
	if lacking, ok := errors.AsType[*keyring.ReplicasLackKeyError](err); ok {
		fmt.Fprintln(stderr, lacking.Error())
		return exitFailure
	}
	switch {
	case errors.Is(err, keyring.ErrKeyNotHeld):
		fmt.Fprintf(stderr, "muster secrets rotate-key: key %s is not in the master keys of this process\n", id)
		return exitFailure
	case err != nil:
		fmt.Fprintf(stderr, "muster secrets rotate-key: %v\n", err)
		return exitFailure
	case out.AlreadyActive && out.Reencrypted > 0:
		fmt.Fprintf(stderr, "key %s is already active; %d secrets re-encrypted\n", id, out.Reencrypted)
	case out.AlreadyActive:
		fmt.Fprintf(stderr, "key %s is already active\n", id)
	default:
		fmt.Fprintf(stderr, "key %s is active: %d secrets re-encrypted; the Leader re-signs the buttons of %d open "+
			"Root messages\n", id, out.Reencrypted, out.ToResign)
	}
	return exitOK
}

func rotateKeyUsageError(stderr io.Writer, msg string) int {
	fmt.Fprintf(stderr, "muster secrets rotate-key: %s\n\n%s", msg, rotateKeyUsage)
	return exitUsage
}

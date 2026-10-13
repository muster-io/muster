// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/runtime"
)

// fakeRotation replaces the runtime entry point of muster secrets rotate-key for one test.
type fakeRotation struct {
	calls       []runtime.KeyRotation
	development []bool
	out         keyring.Rotated
	err         error
}

func newFakeRotation(t *testing.T) *fakeRotation {
	t.Helper()
	f := &fakeRotation{}
	orig, origEnv := runRotateKey, environ
	t.Cleanup(func() { runRotateKey, environ = orig, origEnv })
	runRotateKey = func(_ context.Context, opts runtime.Options, r runtime.KeyRotation) (keyring.Rotated, error) {
		if len(opts.Environ) != 1 || opts.Environ[0] != "MUSTER_SECRET_KEYS=x" {
			t.Errorf("environ = %v", opts.Environ)
		}
		f.calls, f.development = append(f.calls, r), append(f.development, opts.Development)
		return f.out, f.err
	}
	environ = func() []string { return []string{"MUSTER_SECRET_KEYS=x"} }
	return f
}

// TestRotateKeyUsage is C-02.FR-15 and C-20.FR-7: without --actor or --activate, or with arguments after the flags,
// muster secrets rotate-key exits 2 before it reaches the database, so nothing changes.
func TestRotateKeyUsage(t *testing.T) {
	for name, args := range map[string][]string{
		"no actor":      {"secrets", "rotate-key", "--activate", "k-1"},
		"empty actor":   {"secrets", "rotate-key", "--activate", "k-1", "--actor", " "},
		"no key":        {"secrets", "rotate-key", "--actor", "ops"},
		"argument":      {"secrets", "rotate-key", "--activate", "k-1", "--actor", "ops", "now"},
		"unknown flag":  {"secrets", "rotate-key", "--force"},
		"no subcommand": {"secrets"},
		"unknown":       {"secrets", "rotate"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeRotation(t)
			var stdout, stderr bytes.Buffer
			if code := Run(args, &stdout, &stderr); code != exitUsage {
				t.Errorf("exit code = %d", code)
			}
			if len(f.calls) != 0 {
				t.Error("the command went on")
			}
			if !strings.Contains(stderr.String(), "Usage: muster secrets rotate-key") {
				t.Errorf("stderr = %q", stderr.String())
			}
		})
	}
	var stdout, stderr bytes.Buffer
	newFakeRotation(t)
	if Run([]string{"secrets", "rotate-key", "--activate", "k-1"}, &stdout, &stderr); !strings.HasPrefix(
		stderr.String(), "muster secrets rotate-key: --actor is required\n") {
		t.Errorf("stderr = %q", stderr.String())
	}
	stdout.Reset()
	if code := Run([]string{"secrets", "help"}, &stdout, &stderr); code != exitOK ||
		!strings.HasPrefix(stdout.String(), "Usage: muster secrets rotate-key") {
		t.Errorf("help = %d %q", code, stdout.String())
	}
}

// TestRotateKey is C-20.FR-7: the key and the actor reach the runtime, under muster dev with the development
// defaults; an activation, a key already active and the refusals print their lines with the exit codes 0 and 1.
func TestRotateKey(t *testing.T) {
	for _, tc := range []struct {
		name   string
		out    keyring.Rotated
		err    error
		code   int
		stderr string
	}{
		{"activated", keyring.Rotated{KeyID: "k-1", Previous: "k-0", Reencrypted: 7, ToResign: 3}, nil, exitOK,
			"key k-1 is active: 7 secrets re-encrypted; the Leader re-signs the buttons of 3 open Root messages\n"},
		{"already active", keyring.Rotated{KeyID: "k-1", AlreadyActive: true}, nil, exitOK,
			"key k-1 is already active\n"},
		{"leftovers", keyring.Rotated{KeyID: "k-1", AlreadyActive: true, Reencrypted: 2}, nil, exitOK,
			"key k-1 is already active; 2 secrets re-encrypted\n"},
		{"replicas lack it", keyring.Rotated{}, fmt.Errorf("activate: %w",
			&keyring.ReplicasLackKeyError{KeyID: "k-1", Replicas: []string{"ghost-1", "ghost-2"}}), exitFailure,
			"Replicas ghost-1, ghost-2 do not hold key k-1 yet.\n"},
		{"not held", keyring.Rotated{}, fmt.Errorf("%w: k-1", keyring.ErrKeyNotHeld), exitFailure,
			"muster secrets rotate-key: key k-1 is not in the master keys of this process\n"},
		{"failed", keyring.Rotated{}, errors.New("the database is down"), exitFailure,
			"muster secrets rotate-key: the database is down\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeRotation(t)
			f.out, f.err = tc.out, tc.err
			var stdout, stderr bytes.Buffer
			code := Run([]string{"secrets", "rotate-key", "--activate", " k-1 ", "--actor", " ops "}, &stdout, &stderr)
			if code != tc.code || stderr.String() != tc.stderr {
				t.Errorf("exit %d, stderr %q", code, stderr.String())
			}
			if len(f.calls) != 1 || f.calls[0] != (runtime.KeyRotation{KeyID: "k-1", Actor: "ops"}) || f.development[0] {
				t.Errorf("calls %+v %v", f.calls, f.development)
			}
		})
	}
	f := newFakeRotation(t)
	f.out = keyring.Rotated{KeyID: "k-1", Previous: "k-0"}
	var stdout, stderr bytes.Buffer
	d := devMode{env: mapEnv{}}
	if code := d.run([]string{"secrets", "rotate-key", "--activate", "k-1", "--actor", "ops"}, &stdout,
		&stderr); code != exitOK || len(f.development) != 1 || !f.development[0] {
		t.Errorf("muster dev secrets rotate-key = %d, %v, %q", code, f.development, stderr.String())
	}
}

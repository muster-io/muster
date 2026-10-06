// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/runtime"
)

// fakeReset replaces the runtime entry point of reset-password and standard input for one test.
type fakeReset struct {
	calls []runtime.PasswordReset
	err   error
}

func newFakeReset(t *testing.T, input io.Reader) *fakeReset {
	t.Helper()
	f := &fakeReset{}
	origReset, origStdin, origEnv := runResetPassword, stdin, environ
	t.Cleanup(func() { runResetPassword, stdin, environ = origReset, origStdin, origEnv })
	runResetPassword = func(_ context.Context, opts runtime.Options, r runtime.PasswordReset) (string, error) {
		if len(opts.Environ) != 1 || opts.Environ[0] != "MUSTER_DATABASE_URL=x" {
			t.Errorf("environ = %v", opts.Environ)
		}
		f.calls = append(f.calls, r)
		return "SRBBBBBBBBBBBB", f.err
	}
	stdin = input
	environ = func() []string { return []string{"MUSTER_DATABASE_URL=x"} }
	return f
}

// TestResetPasswordNeedsActor is C-02.FR-15: without --actor, or without exactly one login, the command exits 2
// before it reads the password or reaches the database.
func TestResetPasswordNeedsActor(t *testing.T) {
	for name, args := range map[string][]string{
		"no actor":      {"admin", "reset-password", "bob"},
		"empty actor":   {"admin", "reset-password", "--actor", " ", "bob"},
		"no login":      {"admin", "reset-password", "--actor", "ops"},
		"two logins":    {"admin", "reset-password", "--actor", "ops", "bob", "alice"},
		"flag after":    {"admin", "reset-password", "bob", "--actor", "ops"},
		"unknown flag":  {"admin", "reset-password", "--force", "bob"},
		"no subcommand": {"admin"},
		"unknown":       {"admin", "reset-everything"},
	} {
		t.Run(name, func(t *testing.T) {
			in := strings.NewReader("a-good-password-1\n")
			f := newFakeReset(t, in)
			var stdout, stderr bytes.Buffer
			if code := Run(args, &stdout, &stderr); code != exitUsage {
				t.Errorf("exit code = %d", code)
			}
			if len(f.calls) != 0 || in.Len() == 0 {
				t.Error("the command went on")
			}
			if !strings.Contains(stderr.String(), "Usage: muster admin reset-password") {
				t.Errorf("stderr = %q", stderr.String())
			}
		})
	}
	var stdout, stderr bytes.Buffer
	newFakeReset(t, strings.NewReader(""))
	if Run([]string{"admin", "reset-password", "bob"}, &stdout, &stderr); !strings.HasPrefix(stderr.String(),
		"muster admin reset-password: --actor is required\n") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

// TestResetPassword is C-03.FR-11: the password is the first line of standard input, and the actor and login reach
// the runtime; the password is never printed.
func TestResetPassword(t *testing.T) {
	f := newFakeReset(t, strings.NewReader("bob-new-password\r\nignored\n"))
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"admin", "reset-password", "--actor", "ops", "bob"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit code = %d, stderr %q", code, stderr.String())
	}
	if len(f.calls) != 1 || f.calls[0].Actor != "ops" || f.calls[0].Login != "bob" ||
		string(f.calls[0].Password) != "bob-new-password" {
		t.Errorf("calls = %+v", f.calls)
	}
	if !strings.Contains(stderr.String(), "bob (SRBBBBBBBBBBBB)") ||
		strings.Contains(stdout.String()+stderr.String(), "bob-new-password") {
		t.Errorf("stdout %q, stderr %q", stdout.String(), stderr.String())
	}
	f = newFakeReset(t, strings.NewReader("last-line-without-newline"))
	if code := Run([]string{"admin", "reset-password", "--actor=ops", "bob"}, &stdout, &stderr); code != exitOK ||
		string(f.calls[0].Password) != "last-line-without-newline" {
		t.Errorf("exit code = %d, calls %+v", code, f.calls)
	}
}

func TestResetPasswordFailures(t *testing.T) {
	f := newFakeReset(t, strings.NewReader("\n"))
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"admin", "reset-password", "--actor", "ops", "bob"}, &stdout, &stderr); code != exitFailure ||
		len(f.calls) != 0 || !strings.Contains(stderr.String(), "standard input carries no password") {
		t.Errorf("an empty password: %d, %q", code, stderr.String())
	}
	f = newFakeReset(t, errReader{})
	stderr.Reset()
	if code := Run([]string{"admin", "reset-password", "--actor", "ops", "bob"}, &stdout, &stderr); code != exitFailure ||
		len(f.calls) != 0 {
		t.Errorf("a failed read: %d, %q", code, stderr.String())
	}
	f = newFakeReset(t, strings.NewReader("a-good-password-1\n"))
	f.err = errors.New("no such user")
	stderr.Reset()
	if code := Run([]string{"admin", "reset-password", "--actor", "ops", "bob"}, &stdout, &stderr); code != exitFailure ||
		!strings.Contains(stderr.String(), "no such user") {
		t.Errorf("a refused reset: %d, %q", code, stderr.String())
	}
	stdout.Reset()
	if code := Run([]string{"admin", "help"}, &stdout, &stderr); code != exitOK || stdout.String() != adminUsage {
		t.Errorf("help = %d", code)
	}
}

// TestReadPasswordFromFile: a file that is not a terminal is read like a pipe.
func TestReadPasswordFromFile(t *testing.T) {
	path := t.TempDir() + "/password"
	if err := os.WriteFile(path, []byte("from-a-file-123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fh, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	var prompt bytes.Buffer
	p, err := readPassword(fh, &prompt)
	if err != nil || string(p) != "from-a-file-123" || prompt.Len() != 0 {
		t.Errorf("= %q, %v, prompt %q", p, err, prompt.String())
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("broken pipe") }

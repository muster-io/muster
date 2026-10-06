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
	"time"

	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/runtime"
)

// fakeReplay replaces the runtime entry point of muster ingest replay for one test.
type fakeReplay struct {
	calls       []runtime.IngestReplay
	development []bool
	out         ingest.Replayed
	err         error
}

func newFakeReplay(t *testing.T) *fakeReplay {
	t.Helper()
	f := &fakeReplay{}
	orig, origEnv := runReplayIngest, environ
	t.Cleanup(func() { runReplayIngest, environ = orig, origEnv })
	runReplayIngest = func(_ context.Context, opts runtime.Options, r runtime.IngestReplay) (ingest.Replayed, error) {
		if len(opts.Environ) != 1 || opts.Environ[0] != "MUSTER_DATABASE_URL=x" {
			t.Errorf("environ = %v", opts.Environ)
		}
		f.calls, f.development = append(f.calls, r), append(f.development, opts.Development)
		return f.out, f.err
	}
	environ = func() []string { return []string{"MUSTER_DATABASE_URL=x"} }
	return f
}

// TestReplayNeedsActor is C-02.FR-15 and C-06.AC-6: without --actor, without a positive --since or with arguments
// after the flags, muster ingest replay exits 2 before it reaches the database, so nothing changes.
func TestReplayNeedsActor(t *testing.T) {
	for name, args := range map[string][]string{
		"no actor":      {"ingest", "replay", "--since", "1h"},
		"empty actor":   {"ingest", "replay", "--since", "1h", "--actor", " "},
		"no since":      {"ingest", "replay", "--actor", "ops"},
		"bad since":     {"ingest", "replay", "--since", "1d", "--actor", "ops"},
		"zero since":    {"ingest", "replay", "--since", "0s", "--actor", "ops"},
		"argument":      {"ingest", "replay", "--since", "1h", "--actor", "ops", "lab"},
		"unknown flag":  {"ingest", "replay", "--force"},
		"no subcommand": {"ingest"},
		"unknown":       {"ingest", "rewind"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeReplay(t)
			var stdout, stderr bytes.Buffer
			if code := Run(args, &stdout, &stderr); code != exitUsage {
				t.Errorf("exit code = %d", code)
			}
			if len(f.calls) != 0 {
				t.Error("the command went on")
			}
			if !strings.Contains(stderr.String(), "Usage: muster ingest replay") {
				t.Errorf("stderr = %q", stderr.String())
			}
		})
	}
	var stdout, stderr bytes.Buffer
	newFakeReplay(t)
	if Run([]string{"ingest", "replay", "--since", "1h", "--integration", "lab-eu"}, &stdout,
		&stderr); !strings.HasPrefix(stderr.String(), "muster ingest replay: --actor is required\n") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

// TestReplay is C-06.FR-17: the period, the Integration and the actor reach the runtime, and the count is printed.
func TestReplay(t *testing.T) {
	f := newFakeReplay(t)
	f.out = ingest.Replayed{Count: 42, Integration: &ingest.Ref{PublicID: "NTAAAAAAAAAAAA", Name: "lab-eu"}}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"ingest", "replay", "--since", "90m", "--integration", "lab-eu", "--actor",
		"ops-alice"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit code = %d, stderr %q", code, stderr.String())
	}
	if len(f.calls) != 1 || f.calls[0] != (runtime.IngestReplay{Since: 90 * time.Minute, SinceText: "90m",
		Integration: "lab-eu", Actor: "ops-alice"}) || f.development[0] {
		t.Errorf("calls %+v", f.calls)
	}
	if stderr.String() != "replayed 42 Stored Snapshots of lab-eu\n" {
		t.Errorf("stderr = %q", stderr.String())
	}
	f.out = ingest.Replayed{Count: 3}
	stderr.Reset()
	if code := Run([]string{"ingest", "replay", "--since", "1h", "--actor", "ops"}, &stdout, &stderr); code != exitOK ||
		stderr.String() != "replayed 3 Stored Snapshots of every Integration\n" {
		t.Errorf("exit code = %d, stderr = %q", code, stderr.String())
	}
	stdout.Reset()
	if code := Run([]string{"ingest", "help"}, &stdout, &stderr); code != exitOK ||
		!strings.Contains(stdout.String(), "Usage: muster ingest replay") {
		t.Errorf("help = %d %q", code, stdout.String())
	}
}

func TestReplayFails(t *testing.T) {
	f := newFakeReplay(t)
	f.err = fmt.Errorf("%w: nope", ingest.ErrUnknownIntegration)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"ingest", "replay", "--since", "1h", "--integration", "nope", "--actor", "ops"}, &stdout,
		&stderr); code != exitUsage || stderr.String() != "muster ingest replay: no integration has this name: nope\n" {
		t.Errorf("unknown integration: %d %q", code, stderr.String())
	}
	f.err = errors.New("database down")
	stderr.Reset()
	if code := Run([]string{"ingest", "replay", "--since", "1h", "--actor", "ops"}, &stdout, &stderr); code !=
		exitFailure || stderr.String() != "muster ingest replay: database down\n" {
		t.Errorf("failure: %d %q", code, stderr.String())
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/muster-io/muster/internal/buildinfo"
	"github.com/muster-io/muster/internal/doctor"
	"github.com/muster-io/muster/internal/runtime"
)

func TestRun(t *testing.T) {
	origVersion, origCommit := buildinfo.Version, buildinfo.Commit
	t.Cleanup(func() { buildinfo.Version, buildinfo.Commit = origVersion, origCommit })
	buildinfo.Version, buildinfo.Commit = "1.2.3", "0123456789ab"

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "version",
			args:       []string{"version"},
			wantCode:   0,
			wantStdout: "muster 1.2.3 (commit 0123456789ab)\n",
		},
		{
			name:       "version with arguments",
			args:       []string{"version", "extra"},
			wantCode:   2,
			wantStderr: "muster: version takes no arguments\n\n" + usage,
		},
		{name: "help", args: []string{"help"}, wantCode: 0, wantStdout: usage},
		{name: "short help flag", args: []string{"-h"}, wantCode: 0, wantStdout: usage},
		{name: "long help flag", args: []string{"--help"}, wantCode: 0, wantStdout: usage},
		{
			name:       "migrate with arguments",
			args:       []string{"migrate", "--actor", "alice"},
			wantCode:   2,
			wantStderr: "muster: migrate takes no arguments\n\n" + usage,
		},
		{
			name:       "doctor takes no --actor",
			args:       []string{"doctor", "--actor", "alice"},
			wantCode:   2,
			wantStderr: "muster: doctor takes no arguments\n\n" + usage,
		},
		{
			name:       "unknown command",
			args:       []string{"frobnicate"},
			wantCode:   2,
			wantStderr: "muster: unknown command \"frobnicate\"\n\n" + usage,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if got := stdout.String(); got != tt.wantStdout {
				t.Errorf("stdout = %q, want %q", got, tt.wantStdout)
			}
			if got := stderr.String(); got != tt.wantStderr {
				t.Errorf("stderr = %q, want %q", got, tt.wantStderr)
			}
		})
	}
}

// fakeRuntime replaces the runtime entry points and the signal context for one test.
func fakeRuntime(t *testing.T, serve, migrate func(context.Context, runtime.Options) error) context.CancelFunc {
	t.Helper()
	origServer, origMigrate, origSignals, origEnviron := runServer, runMigrate, signals, environ
	t.Cleanup(func() { runServer, runMigrate, signals, environ = origServer, origMigrate, origSignals, origEnviron })
	ctx, cancel := context.WithCancel(t.Context())
	runServer, runMigrate = serve, migrate
	signals = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	environ = func() []string { return []string{"MUSTER_PUBLIC_URL=http://muster.test"} }
	return cancel
}

func TestServe(t *testing.T) {
	var got runtime.Options
	cancel := fakeRuntime(t, func(ctx context.Context, o runtime.Options) error {
		got = o
		<-ctx.Done()
		return nil
	}, nil)
	cancel()
	var stdout, stderr bytes.Buffer
	if code := Run(nil, &stdout, &stderr); code != exitOK || stderr.Len() != 0 {
		t.Errorf("muster = %d, stderr %q", code, stderr.String())
	}
	if got.Stdout != &stdout || got.Development || len(got.Environ) != 1 || got.Environ[0] != "MUSTER_PUBLIC_URL=http://muster.test" {
		t.Errorf("options %+v", got)
	}

	fakeRuntime(t, func(context.Context, runtime.Options) error {
		return errors.New(`invalid MUSTER_DATABASE_PORT: "abc" is not a port number`)
	}, nil)
	stderr.Reset()
	if code := Run(nil, &stdout, &stderr); code != exitFailure ||
		stderr.String() != "muster: invalid MUSTER_DATABASE_PORT: \"abc\" is not a port number\n" {
		t.Errorf("muster with an invalid port = %d, stderr %q", code, stderr.String())
	}
}

func TestMigrateCommand(t *testing.T) {
	migrated := false
	fakeRuntime(t, nil, func(_ context.Context, o runtime.Options) error {
		migrated = o.Stdout != nil && len(o.Environ) == 1
		return nil
	})
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"migrate"}, &stdout, &stderr); code != exitOK || !migrated || stderr.Len() != 0 {
		t.Errorf("muster migrate = %d, migrated %v, stderr %q", code, migrated, stderr.String())
	}

	fakeRuntime(t, nil, func(context.Context, runtime.Options) error {
		return errors.New("database schema version 999 is newer than this binary knows (1)")
	})
	if code := Run([]string{"migrate"}, &stdout, &stderr); code != exitFailure ||
		stderr.String() != "muster: database schema version 999 is newer than this binary knows (1)\n" {
		t.Errorf("muster migrate on a newer schema = %d, stderr %q", code, stderr.String())
	}
}

func TestDoctorCommand(t *testing.T) {
	fakeRuntime(t, nil, nil)
	orig := runDoctorChecks
	t.Cleanup(func() { runDoctorChecks = orig })
	for _, tt := range []struct {
		name       string
		ok         bool
		err        error
		wantCode   int
		wantStderr string
	}{
		{name: "every check passes", ok: true, wantCode: exitOK},
		{name: "a check fails", ok: false, wantCode: exitFailure},
		{name: "the settings cannot be read", err: errors.New("MUSTER_PUBLIC_URL is required"), wantCode: exitFailure,
			wantStderr: "muster: MUSTER_PUBLIC_URL is required\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var got doctor.Options
			runDoctorChecks = func(_ context.Context, o doctor.Options) (bool, error) {
				got = o
				return tt.ok, tt.err
			}
			var stdout, stderr bytes.Buffer
			if code := Run([]string{"doctor"}, &stdout, &stderr); code != tt.wantCode || stderr.String() != tt.wantStderr {
				t.Errorf("muster doctor = %d, stderr %q", code, stderr.String())
			}
			if got.Out != &stdout || len(got.Environ) != 1 || got.Development {
				t.Errorf("options %+v", got)
			}
		})
	}

	t.Run("muster dev doctor accepts the development key", func(t *testing.T) {
		clearDevEnv(t)
		var got doctor.Options
		runDoctorChecks = func(_ context.Context, o doctor.Options) (bool, error) {
			got = o
			return true, nil
		}
		var stdout, stderr bytes.Buffer
		if code := Run([]string{"dev", "doctor"}, &stdout, &stderr); code != exitOK || !got.Development {
			t.Errorf("muster dev doctor = %d, options %+v, stderr %q", code, got, stderr.String())
		}
	})
}

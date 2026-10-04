// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package cli

import (
	"bytes"
	"testing"

	"github.com/muster-io/muster/internal/buildinfo"
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
		{name: "help", args: []string{"help"}, wantCode: 0, wantStdout: usage},
		{name: "short help flag", args: []string{"-h"}, wantCode: 0, wantStdout: usage},
		{name: "long help flag", args: []string{"--help"}, wantCode: 0, wantStdout: usage},
		{name: "no arguments", args: nil, wantCode: 2, wantStderr: usage},
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

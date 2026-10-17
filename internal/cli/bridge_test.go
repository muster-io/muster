// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package cli

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/fakes/fakebridge"
)

// secretFile writes content to a file of the test and returns its path.
func secretFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestBridgeCheckUsage: a broken invocation exits 2 with the usage before any request, and never shows what a secret
// file holds.
func TestBridgeCheckUsage(t *testing.T) {
	token := secretFile(t, "token", fakebridge.SecondToken+"\n")
	malformed := "whsec_not-base64-" + fakebridge.SecondToken
	missing := filepath.Join(t.TempDir(), "none")
	for _, tc := range []struct {
		name, want string
		args       []string
	}{
		{"no URL", "the bridge URL is required", []string{"--token-file", token, "--channel", "c"}},
		{"two URLs", "give exactly one bridge URL", []string{"http://a", "--token-file", token, "http://b"}},
		{"ftp", "the bridge URL is not an absolute http", []string{"ftp://a", "--token-file", token, "--channel", "c"}},
		{"query", "the bridge URL is not an absolute http", []string{"http://a/?x=1", "--token-file", token}},
		{"no token file", "--token-file is required", []string{"http://a", "--channel", "c"}},
		{"no channel", "--channel is required", []string{"http://a", "--token-file", token, "--channel", " "}},
		{"unknown flag", "flag provided but not defined: -nope", []string{"http://a", "--nope"}},
		{"push URL", "--push-url is not an absolute http or https URL", []string{"http://a", "--token-file", token,
			"--channel", "c", "--push-url", "localhost:1"}},
		{"push listen", "--push-listen is not a host and port", []string{"http://a", "--token-file", token,
			"--channel", "c", "--push-listen", "127.0.0.1"}},
		{"missing token file", "--token-file: open " + missing + ": ", []string{"http://a", "--token-file", missing,
			"--channel", "c"}},
		{"empty token file", "--token-file: the file is empty", []string{"http://a", "--token-file",
			secretFile(t, "empty", " \n"), "--channel", "c"}},
		{"two lines", "--token-file: the file holds more than the secret alone on one line", []string{"http://a",
			"--token-file", secretFile(t, "two", "a\nb\n"), "--channel", "c"}},
		{"large token file", "--token-file: the file is larger than 64 KiB", []string{"http://a", "--token-file",
			secretFile(t, "large", strings.Repeat("x", maxSecretFile+1)), "--channel", "c"}},
		{"directory", "--token-file: read ", []string{"http://a", "--token-file", t.TempDir(), "--channel", "c"}},
		{"malformed push secret", "--push-secret-file does not hold whsec_ followed by base64", []string{"http://a",
			"--token-file", token, "--channel", "c", "--push-secret-file", secretFile(t, "push", malformed)}},
		{"unreadable push secret", "--push-secret-file: open ", []string{"http://a", "--token-file", token,
			"--channel", "c", "--push-secret-file", missing}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := BridgeCheckCommand(t.Context(), tc.args, &stdout, &stderr)
			if code != exitUsage || stdout.Len() != 0 ||
				!strings.HasPrefix(stderr.String(), "muster bridge check: "+tc.want) ||
				!strings.HasSuffix(stderr.String(), "\n\n"+bridgeUsage) {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
			}
			if strings.Contains(stderr.String(), fakebridge.SecondToken) {
				t.Fatalf("a secret reached standard error: %q", stderr.String())
			}
		})
	}
}

// TestBridgeCommand: muster bridge without a command or with an unknown one exits 2, and help prints the usage.
func TestBridgeCommand(t *testing.T) {
	for _, tc := range []struct {
		args               []string
		code               int
		stdout, stderrHead string
	}{
		{[]string{"bridge"}, exitUsage, "", bridgeUsage},
		{[]string{"bridge", "frobnicate"}, exitUsage, "", "muster bridge: unknown command \"frobnicate\"\n\n"},
		{[]string{"bridge", "help"}, exitOK, bridgeUsage, ""},
		{[]string{"bridge", "--help"}, exitOK, bridgeUsage, ""},
		{[]string{"bridge", "check", "--help"}, exitOK, bridgeUsage, ""},
		{[]string{"bridge", "check"}, exitUsage, "", "muster bridge check: the bridge URL is required\n\n"},
	} {
		var stdout, stderr bytes.Buffer
		code := Run(tc.args, &stdout, &stderr)
		if code != tc.code || stdout.String() != tc.stdout || !strings.HasPrefix(stderr.String(), tc.stderrHead) {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", tc.args, code, stdout.String(), stderr.String())
		}
	}
	if !strings.Contains(usage, "  bridge    bridge check <url>") {
		t.Error("the usage does not name muster bridge")
	}
}

// TestBridgeCheckAgainstTheFake is C-22.FR-21 through the command: flags before or after the URL, every step against
// the fake with every feature, exit 1 when a step fails, and the token in no output.
func TestBridgeCheckAgainstTheFake(t *testing.T) {
	f := fakebridge.New()
	if err := f.Start(t.Context(), "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close(context.WithoutCancel(t.Context())) })
	token := secretFile(t, "token", fakebridge.SecondToken+"\n")
	flags := []string{"--token-file", token, "--channel", "ch-alerts", "--forbidden-channel", "ch-nobot", "--handle",
		"alice", "--no-interactive"}
	var outs []string
	for _, args := range [][]string{append([]string{f.URL()}, flags...), append(append([]string{}, flags...),
		f.URL()+"/")} {
		var stdout, stderr bytes.Buffer
		if code := BridgeCheckCommand(t.Context(), args, &stdout, &stderr); code != exitOK || stderr.Len() != 0 {
			t.Fatalf("%v: exit %d, stdout %s, stderr %s", args, code, stdout.String(), stderr.String())
		}
		outs = append(outs, stdout.String())
	}
	want := "PASS info\nPASS channel_check\nPASS post\nPASS replayed_key\nPASS buttons\nPASS edit\nPASS thread_reply\n" +
		"PASS thread_ref\nPASS forbidden_channel\nPASS user_lookup\nPASS direct_message\nPASS delete\nPASS poll\n" +
		"SKIP press: --no-interactive\nSKIP push: --no-interactive\nPASS answer\n"
	if outs[0] != want || outs[1] != want {
		t.Fatalf("stdout\n%s\n%s", outs[0], outs[1])
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, f.URL()+"/_fake/config",
		strings.NewReader(`{"replays":false}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	var stdout, stderr bytes.Buffer
	code := BridgeCheckCommand(t.Context(), append([]string{f.URL()}, flags...), &stdout, &stderr)
	if code != exitFailure || !strings.Contains(stdout.String(), "\nFAIL replayed_key: ") || stderr.Len() != 0 ||
		strings.Contains(stdout.String()+stderr.String(), fakebridge.SecondToken) {
		t.Fatalf("exit %d, stdout %s, stderr %s", code, stdout.String(), stderr.String())
	}
}

// TestBridgeCheckInterrupted: a check whose context ended says so and exits 1.
func TestBridgeCheckInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var stdout, stderr bytes.Buffer
	code := BridgeCheckCommand(ctx, []string{"http://127.0.0.1:1", "--token-file",
		secretFile(t, "token", "t"), "--channel", "c", "--push-secret-file",
		secretFile(t, "push", "whsec_YQ==\n")}, &stdout, &stderr)
	if code != exitFailure || stdout.Len() != 0 || stderr.String() != "muster bridge check: interrupted\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}

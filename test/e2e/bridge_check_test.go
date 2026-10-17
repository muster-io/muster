// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
)

// checkPassed are the lines of a check in which every step that ran passed, without press and push.
var checkPassed = []string{"PASS info", "PASS channel_check", "PASS post", "PASS replayed_key", "PASS buttons",
	"PASS edit", "PASS thread_reply", "PASS thread_ref", "PASS forbidden_channel", "PASS user_lookup",
	"PASS direct_message", "PASS delete", "PASS poll"}

// TestBridgeCheck is C-22.FR-21 and AC-22 against the fake bridge of `muster dev`: `muster bridge check` with the
// token of the fake's second bot, which no Connection polls, run with every feature and --no-interactive; with the
// features cut, with replays off and with another protocol version; and interactively, pointing the fake's push target
// at the PUSH line and pressing the button the WAIT line names.
func TestBridgeCheck(t *testing.T) {
	h := Start(t, DevProcess)
	fb := h.Fakes.Bridge
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte(devmode.BridgeSecondBotToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	capabilities, err := json.Marshal(fakebridge.DefaultConfig().Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	full := `{"protocol_version":"1","replays":true,"push":null,"capabilities":` + string(capabilities) +
		`,"user_lookup":"login","event_delivery":["poll","push"]}`
	configure := func(t *testing.T, patch string) {
		t.Helper()
		if a := call(t, http.MethodPut, fb+"/_fake/config", patch); a.status != http.StatusOK {
			t.Fatalf("PUT /_fake/config %s = %d %s", patch, a.status, a.body)
		}
	}
	reset := func(t *testing.T) {
		configure(t, full)
		t.Cleanup(func() { restore(t, http.MethodPut, fb+"/_fake/config", full) })
	}
	check := []string{"bridge", "check", fb, "--token-file", token, "--channel", fakebridge.ChannelAlerts}
	every := append(slices.Clone(check), "--forbidden-channel", fakebridge.ChannelNoBot, "--handle", "alice")
	lines := func(stdout string) []string { return strings.Split(strings.TrimSuffix(stdout, "\n"), "\n") }
	noToken := func(t *testing.T, out ...string) {
		t.Helper()
		for _, o := range out {
			if strings.Contains(o, devmode.BridgeSecondBotToken) {
				t.Fatalf("the token reached the output:\n%s", o)
			}
		}
	}

	t.Run("every feature", func(t *testing.T) {
		reset(t)
		code, stdout, stderr := h.runCLIEnv(t, nil, append(slices.Clone(every), "--no-interactive")...)
		want := append(slices.Clone(checkPassed), "SKIP press: --no-interactive", "SKIP push: --no-interactive",
			"PASS answer")
		if code != 0 || !slices.Equal(lines(stdout), want) {
			t.Fatalf("exit %d, stdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		noToken(t, stdout, stderr)
		var ms []fakebridge.Message
		decode(t, call(t, http.MethodGet, fb+"/_fake/messages", ""), &ms)
		ms = slices.DeleteFunc(ms, func(m fakebridge.Message) bool { return m.Bot != 2 })
		if len(ms) != 2 {
			t.Fatalf("messages of the second bot %+v", ms)
		}
		post, buttons := ms[0], ms[1]
		if post.Envelope.Title != "muster bridge check: post" || !post.Deleted || len(post.Edits) != 1 ||
			len(post.Replies) != 2 || post.Envelope.Kind != "test" || post.Envelope.Notify {
			t.Errorf("post %+v", post)
		}
		if len(buttons.Envelope.Buttons) != 4 || buttons.Deleted {
			t.Errorf("buttons %+v", buttons)
		}
		for _, b := range buttons.Envelope.Buttons {
			if len(b.Data) != 64 {
				t.Errorf("button %+v", b)
			}
		}
	})

	t.Run("reduced features", func(t *testing.T) {
		reset(t)
		configure(t, `{"capabilities":{"thread_messages":"all"},"user_lookup":"none","event_delivery":["poll"]}`)
		code, stdout, stderr := h.runCLIEnv(t, nil, append(slices.Clone(check), "--no-interactive")...)
		want := []string{"PASS info", "PASS channel_check", "PASS post", "PASS replayed_key",
			"SKIP buttons: the bridge does not offer buttons", "PASS edit", "PASS thread_reply", "PASS thread_ref",
			"SKIP forbidden_channel: no --forbidden-channel", "SKIP user_lookup: the bridge looks up nobody",
			"SKIP direct_message: the bridge does not offer direct_messages",
			"SKIP delete: the bridge does not offer delete", "PASS poll",
			"SKIP press: the bridge does not offer buttons", "SKIP push: the bridge does not offer push", "PASS answer"}
		if code != 0 || !slices.Equal(lines(stdout), want) {
			t.Fatalf("exit %d, stdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}

		configure(t, `{"replays":false}`)
		code, stdout, stderr = h.runCLIEnv(t, nil, append(slices.Clone(check), "--no-interactive")...)
		replayed := regexp.MustCompile(`(?m)^FAIL replayed_key: the second request with the same Idempotency-Key ` +
			`created message m\d+ instead of returning m\d+$`)
		if code != 1 || !replayed.MatchString(stdout) {
			t.Fatalf("replays off: exit %d, stdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		noToken(t, stdout, stderr)

		configure(t, `{"protocol_version":"2"}`)
		code, stdout, stderr = h.runCLIEnv(t, nil, append(slices.Clone(check), "--no-interactive")...)
		if code != 1 || stdout != "FAIL info: The bridge speaks protocol version 2; this Muster speaks 1.\n" {
			t.Fatalf("protocol version 2: exit %d, stdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
	})

	t.Run("interactive", func(t *testing.T) {
		reset(t)
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		//nolint:gosec // G204: the binary under test, from MUSTER_E2E_BINARY
		cmd := exec.CommandContext(ctx, h.binary, every...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		pushLine := regexp.MustCompile(`^PUSH (http://\S+) (whsec_\S+)$`)
		var got []string
		var event string
		for sc := bufio.NewScanner(out); sc.Scan(); {
			line := sc.Text()
			got = append(got, line)
			if m := pushLine.FindStringSubmatch(line); m != nil {
				b, _ := json.Marshal(map[string]any{"push": map[string]string{"url": m[1], "secret": m[2]}})
				configure(t, string(b))
				continue
			}
			rest, ok := strings.CutPrefix(line, "WAIT press: ")
			if !ok {
				continue
			}
			if !slices.ContainsFunc(got, pushLine.MatchString) {
				t.Fatalf("WAIT before PUSH: %q", got)
			}
			_, id, _ := strings.Cut(rest, ", message_id ")
			a := call(t, http.MethodPost, fb+"/_fake/press", fmt.Sprintf(`{"message_id":%q,"user_id":%q,`+
				`"button":"primary"}`, id, fakebridge.AliceID))
			var pressed fakebridge.Event
			decode(t, a, &pressed)
			var ev struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(pressed.Event, &ev); err != nil || a.status != http.StatusOK || pressed.Bot != 2 {
				t.Fatalf("press %s = %d %s", id, a.status, a.body)
			}
			event = ev.ID
		}
		if err := cmd.Wait(); err != nil {
			t.Fatalf("exit %v, stdout:\n%s\nstderr:\n%s", err, strings.Join(got, "\n"), stderr.String())
		}
		passed := slices.DeleteFunc(slices.Clone(got), func(l string) bool {
			return strings.HasPrefix(l, "PUSH ") || strings.HasPrefix(l, "WAIT ")
		})
		if want := append(slices.Clone(checkPassed), "PASS press", "PASS push", "PASS answer"); event == "" ||
			len(got) != len(want)+2 || !slices.Equal(passed, want) {
			t.Fatalf("stdout:\n%s\nstderr:\n%s", strings.Join(got, "\n"), stderr.String())
		}
		noToken(t, strings.Join(got, "\n"), stderr.String())
		var answers []fakebridge.Answer
		decode(t, call(t, http.MethodGet, fb+"/_fake/answers", ""), &answers)
		if !slices.ContainsFunc(answers, func(a fakebridge.Answer) bool {
			return a.Bot == 2 && a.EventID == event && a.EventType == fakebridge.TypePress && a.ShownAs == "ephemeral"
		}) {
			t.Errorf("answers %+v, event %s", answers, event)
		}
	})
}

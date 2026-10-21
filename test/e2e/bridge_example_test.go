// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/bridge"
	"github.com/muster-io/muster/internal/devmode"
)

// exampleImage is the example bridge of muster-bridge-go (ADR-0017): the image of a library release, pinned by digest.
// Renovate moves the version and the digest together when the library releases.
// renovate: datasource=docker depName=ghcr.io/muster-io/muster-example-bridge
const exampleImage = "ghcr.io/muster-io/muster-example-bridge:v0.1.0@sha256:70a84be3673676435b75c7d1e3b9a6a10c7350318fe3c33962b7fb2d0872114e"

const (
	exampleListen = "127.0.0.1:18095"
	exampleURL    = "http://" + exampleListen
	// examplePush is where the push listener of `muster bridge check` listens in the interactive run.
	examplePush = "127.0.0.1:18096"
	// exampleName is the container's name, fixed so that each start first removes a container that a killed run left
	// holding the port; exampleLabel marks it for `docker ps --filter label=muster-e2e`.
	exampleName  = "muster-e2e-example-bridge"
	exampleLabel = "muster-e2e=example-bridge"
)

// exampleBridge is the example bridge in a Docker container on the host's network. Its token and push secret are in
// files of mode 0644, which the image's user 65532 reads; they never appear on a command line or in the container's
// environment, and secrets are masked in what the test prints.
type exampleBridge struct {
	t                  *testing.T
	name               string
	token, tokenFile   string
	secret, secretFile string
	secrets            *[]string
}

// exampleRun is what a start of the example sets: BRIDGE_FEATURES, every feature when empty, and BRIDGE_PUSH_URL with
// the push secret, polling only when empty.
type exampleRun struct {
	features, push string
}

func newExampleBridge(t *testing.T) *exampleBridge {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("the example bridge runs with the command docker, which is not found: %v", err)
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	dir := t.TempDir()
	x := &exampleBridge{t: t, name: exampleName,
		token: "example-" + rand.Text(), tokenFile: filepath.Join(dir, "token"),
		secret: "whsec_" + base64.StdEncoding.EncodeToString(key), secretFile: filepath.Join(dir, "push-secret")}
	x.secrets = &[]string{x.token, x.secret, strings.TrimPrefix(x.secret, "whsec_")}
	for file, value := range map[string]string{x.tokenFile: x.token, x.secretFile: x.secret} {
		if err := os.WriteFile(file, []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(file, 0o644); err != nil { //nolint:gosec // G302: the image's user 65532 reads the file
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("docker logs %s:\n%s", x.name, x.logs())
		}
		x.remove()
	})
	return x
}

// on is x reporting to t, for a subtest.
func (x *exampleBridge) on(t *testing.T) *exampleBridge {
	c := *x
	c.t = t
	return &c
}

// restart removes the container, if any, once its output is checked for secrets, and starts the example again with
// run, which loses everything it kept; it waits until the page answers.
func (x *exampleBridge) restart(run exampleRun) {
	x.t.Helper()
	x.leaked(x.t, "the example's output", x.output())
	x.remove()
	if answers(x.t.Context(), exampleURL+"/") {
		x.t.Fatalf("something else answers at %s once %s is removed (docker ps)", exampleURL, x.name)
	}
	args := []string{"run", "-d", "--name", x.name, "--label", exampleLabel, "--network", "host",
		"-v", x.tokenFile + ":/token:ro"}
	flags := []string{"-listen", exampleListen, "-token-file", "/token"}
	if run.features != "" {
		args = append(args, "-e", "BRIDGE_FEATURES="+run.features)
	}
	if run.push != "" {
		args = append(args, "-v", x.secretFile+":/push-secret:ro", "-e", "BRIDGE_PUSH_URL="+run.push)
		flags = append(flags, "-push-secret-file", "/push-secret")
	}
	if out, err := x.docker(x.t.Context(), append(append(args, exampleImage), flags...)...); err != nil {
		x.t.Fatalf("docker run %s: %v\n%s", exampleImage, err, out)
	}
	deadline := time.Now().Add(readyTimeout)
	for !answers(x.t.Context(), exampleURL+"/") {
		if out, _ := x.docker(x.t.Context(), "inspect", "-f", "{{.State.Running}}", x.name); strings.TrimSpace(out) !=
			"true" || time.Now().After(deadline) {
			x.t.Fatalf("the example bridge does not answer at %s; docker logs %s:\n%s", exampleURL, x.name, x.logs())
		}
		time.Sleep(pollInterval)
	}
}

// remove removes the container, if it exists, also when the test's context has ended.
func (x *exampleBridge) remove() {
	if out, err := x.docker(context.WithoutCancel(x.t.Context()), "rm", "-f", x.name); err != nil &&
		!strings.Contains(out, "No such container") {
		x.t.Errorf("docker rm -f %s: %v\n%s", x.name, err, out)
	}
}

// output is what the container printed.
func (x *exampleBridge) output() string {
	out, err := x.docker(context.WithoutCancel(x.t.Context()), "logs", x.name)
	if err != nil {
		return fmt.Sprintf("(%v) %s", err, out)
	}
	return out
}

// logs is the output with every secret masked, for a failure message.
func (x *exampleBridge) logs() string {
	out := x.output()
	for _, s := range *x.secrets {
		out = strings.ReplaceAll(out, s, "[redacted]")
	}
	return out
}

func (x *exampleBridge) docker(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	return string(out), err
}

// examplePost is a message on the example's page: a Root message of the bot with its buttons, Thread and answers to
// one person, or a message in a Thread.
type examplePost struct {
	ID      string `json:"id"`
	Bot     bool   `json:"bot"`
	Status  string `json:"status"`
	Title   string `json:"title"`
	Text    string `json:"text"`
	Buttons []struct {
		Label string `json:"label"`
	} `json:"buttons"`
	Edits   int           `json:"edits"`
	Deleted bool          `json:"deleted"`
	Thread  []examplePost `json:"thread"`
	Notes   []struct {
		For  string `json:"for"`
		Text string `json:"text"`
	} `json:"notes"`
}

func (p examplePost) labels() []string {
	var out []string
	for _, b := range p.Buttons {
		out = append(out, b.Label)
	}
	return out
}

// button is the index of the button label of p, as the page's press takes it.
func (x *exampleBridge) button(p examplePost, label string) int {
	x.t.Helper()
	i := slices.Index(p.labels(), label)
	if i < 0 {
		x.t.Fatalf("no button %q on %s %q: %v", label, p.ID, p.Title, p.labels())
	}
	return i
}

// messages are the Root messages of the channel on the page, oldest first.
func (x *exampleBridge) messages(channel string) []examplePost {
	x.t.Helper()
	a := call(x.t, http.MethodGet, exampleURL+"/chat/api/messages?channel="+channel, "")
	if a.status != http.StatusOK {
		x.t.Fatalf("the messages of %s = %d %s", channel, a.status, a.body)
	}
	var out []examplePost
	decode(x.t, a, &out)
	return out
}

// message is the Root message id of the channel alerts.
func (x *exampleBridge) message(id string) examplePost {
	x.t.Helper()
	for _, p := range x.messages("alerts") {
		if p.ID == id {
			return p
		}
	}
	x.t.Fatalf("no message %s in alerts", id)
	return examplePost{}
}

// root waits for the Root message of the Alert Group number n in the channel alerts and returns it.
func (x *exampleBridge) root(n int) examplePost {
	x.t.Helper()
	prefix := fmt.Sprintf("#%d ", n)
	var got examplePost
	eventually(x.t, "the Root message "+prefix+"in alerts", func() bool {
		for _, p := range x.messages("alerts") {
			if strings.HasPrefix(p.Title, prefix) && !p.Deleted {
				got = p
				return true
			}
		}
		return false
	})
	return got
}

// botDirect are the bot's answers in the direct chat of the person login. The direct chat holds the link token, so
// its body is never printed.
func (x *exampleBridge) botDirect(login string) []string {
	x.t.Helper()
	a := call(x.t, http.MethodGet, exampleURL+"/chat/api/direct-messages?user="+login, "")
	if a.status != http.StatusOK {
		x.t.Fatalf("the direct chat of %s = %d", login, a.status)
	}
	var chat []struct {
		Bot  bool   `json:"bot"`
		Text string `json:"text"`
	}
	decode(x.t, a, &chat)
	var out []string
	for _, m := range chat {
		if m.Bot {
			out = append(out, m.Text)
		}
	}
	return out
}

// act sends a request of the page's JSON API as a person would, and fails unless it is answered want. The request is
// never printed: a direct message carries a link token.
func (x *exampleBridge) act(path string, body map[string]any, want int) {
	x.t.Helper()
	b, _ := json.Marshal(body)
	a := call(x.t, http.MethodPost, exampleURL+"/chat/api/"+path, string(b), "Content-Type", "application/json")
	if a.status != want {
		x.t.Fatalf("POST /chat/api/%s = %d %s", path, a.status, a.body)
	}
}

func (x *exampleBridge) press(message, user string, button int) {
	x.t.Helper()
	x.act("press", map[string]any{"message_id": message, "user": user, "button": button}, http.StatusOK)
}

// leaked fails t when a secret of the example is in one of out.
func (x *exampleBridge) leaked(t *testing.T, what string, out ...string) {
	t.Helper()
	for _, o := range out {
		for _, s := range *x.secrets {
			if strings.Contains(o, s) {
				t.Errorf("%s holds a secret", what)
				return
			}
		}
	}
}

// TestBridgeExampleCheck is C-22.AC-22 against the pinned image of the example bridge: `muster bridge check` from the
// binary under test passes every step with all its features and --no-interactive but press and push, skips the
// optional steps with BRIDGE_FEATURES=thread_messages, and interactively, with the example pushing to the check's
// listener with the secret of --push-secret-file, passes press, push and answer once the test presses the button the
// WAIT line names through the page's JSON API. The token and the push secret reach neither the check's output nor the
// example's.
func TestBridgeExampleCheck(t *testing.T) {
	bin := binary(t)
	x := newExampleBridge(t)
	check := []string{"bridge", "check", exampleURL, "--token-file", x.tokenFile, "--channel", "alerts",
		"--forbidden-channel", "nobot", "--handle", "alice"}
	run := func(t *testing.T, args ...string) (int, string, string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		//nolint:gosec // G204: the binary under test, from MUSTER_E2E_BINARY
		cmd := exec.CommandContext(ctx, bin, append(slices.Clone(check), args...)...)
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), stdout.String(), stderr.String()
		}
		if err != nil {
			t.Fatalf("muster bridge check: %v; stderr:\n%s", err, stderr.String())
		}
		return 0, stdout.String(), stderr.String()
	}
	lines := func(stdout string) []string { return strings.Split(strings.TrimSuffix(stdout, "\n"), "\n") }

	t.Run("every feature", func(t *testing.T) {
		x := x.on(t)
		x.restart(exampleRun{})
		code, stdout, stderr := run(t, "--no-interactive")
		want := append(slices.Clone(checkPassed), "SKIP press: --no-interactive", "SKIP push: --no-interactive",
			"PASS answer")
		if code != 0 || !slices.Equal(lines(stdout), want) {
			t.Fatalf("exit %d, stdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		x.leaked(t, "the check's output", stdout, stderr)
		var buttons []examplePost
		for _, p := range x.messages("alerts") {
			if p.Title == "muster bridge check: buttons" {
				buttons = append(buttons, p)
			}
		}
		if len(buttons) != 1 || len(buttons[0].Buttons) == 0 {
			t.Errorf("the buttons message on the page %+v", buttons)
		}
	})

	t.Run("thread_messages only", func(t *testing.T) {
		x := x.on(t)
		x.restart(exampleRun{features: "thread_messages"})
		code, stdout, stderr := run(t, "--no-interactive")
		want := []string{"PASS info", "PASS channel_check", "PASS post", "PASS replayed_key",
			"SKIP buttons: the bridge does not offer buttons", "PASS edit", "PASS thread_reply", "PASS thread_ref",
			"PASS forbidden_channel", "SKIP user_lookup: the bridge looks up nobody",
			"SKIP direct_message: the bridge does not offer direct_messages",
			"SKIP delete: the bridge does not offer delete", "PASS poll",
			"SKIP press: the bridge does not offer buttons", "SKIP push: the bridge does not offer buttons",
			"PASS answer"}
		if code != 0 || !slices.Equal(lines(stdout), want) {
			t.Fatalf("exit %d, stdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		x.leaked(t, "the check's output", stdout, stderr)
	})

	t.Run("interactive", func(t *testing.T) {
		x := x.on(t)
		x.restart(exampleRun{push: "http://" + examplePush + "/"})
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		//nolint:gosec // G204: the binary under test, from MUSTER_E2E_BINARY
		cmd := exec.CommandContext(ctx, bin, append(slices.Clone(check), "--push-listen", examplePush,
			"--push-secret-file", x.secretFile)...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		pushLine := "PUSH http://" + examplePush + "/"
		var got []string
		var pressed string
		for sc := bufio.NewScanner(out); sc.Scan(); {
			line := sc.Text()
			got = append(got, line)
			rest, ok := strings.CutPrefix(line, "WAIT press: ")
			if !ok {
				continue
			}
			if !slices.Contains(got, pushLine) {
				t.Fatalf("WAIT before %q: %q", pushLine, got)
			}
			_, pressed, _ = strings.Cut(rest, ", message_id ")
			x.press(pressed, "alice", 0)
		}
		if err := cmd.Wait(); err != nil {
			t.Fatalf("exit %v, stdout:\n%s\nstderr:\n%s", err, strings.Join(got, "\n"), stderr.String())
		}
		passed := slices.DeleteFunc(slices.Clone(got), func(l string) bool {
			return l == pushLine || strings.HasPrefix(l, "WAIT ")
		})
		if want := append(slices.Clone(checkPassed), "PASS press", "PASS push", "PASS answer"); pressed == "" ||
			len(got) != len(want)+2 || !slices.Equal(passed, want) {
			t.Fatalf("stdout:\n%s\nstderr:\n%s", strings.Join(got, "\n"), stderr.String())
		}
		x.leaked(t, "the check's output", strings.Join(got, "\n"), stderr.String())
		if notes := x.message(pressed).Notes; len(notes) != 1 || notes[0].For != "alice" {
			t.Errorf("the answer to alice's press on the page %+v", notes)
		}
	})

	x.leaked(t, "the example's output", x.output())
}

// TestBridgeExampleFlows is C-22.AC-23 against `muster dev` and the pinned image of the example bridge: a Bridge
// Connection polls the example, and its Destination for the channel alerts posts each new Alert Group there as a Root
// message #N. Alice, signed in to the web UI, starts an Account link and sends its token to the bot in her direct chat
// on the page, which links her account; her press of Ack acknowledges the Alert Group with the Transport bridge and the
// page shows the buttons of the acknowledged status. With the example restarted offering only thread_messages and the
// Connection checked again, her `ack` in the Thread of a new Alert Group acknowledges it and is answered there. With the
// Connection in push mode and the example pushing to its callback with the push secret, her press reaches Muster as a
// signed push, recorded in the Request log. No secret reaches a log.
func TestBridgeExampleFlows(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	x := newExampleBridge(t)
	x.restart(exampleRun{})
	b := &brd{mm: newMM(t, h, r)}
	e := &bridgeLinksEnv{linksEnv: &linksEnv{timersEnv: &timersEnv{tgd: &tgd{mm: b.mm}}}, b: b}
	created, _ := json.Marshal(map[string]any{"type": "bridge", "name": "Example bridge", "base_url": exampleURL,
		"bot_token": x.token, "event_delivery": "poll", "proxy": map[string]bool{"enabled": false},
		"limiter": map[string]int{"limit": 10, "per_seconds": 1}})
	b.conn, _ = b.api.json(http.MethodPost, "/api/v1/connections", string(created), http.StatusCreated)["id"].(string)
	if b.conn == "" {
		t.Fatal("the created Connection has no id")
	}
	e.check(b.conn)
	b.route("example", "example", []string{b.destination("example", "alerts")}, calm)
	alice := e.person("Alice A.", "alice", "responder")
	// recheck checks the Connection, which reads info again. A check refreshes only the replica that runs it, and
	// another replica keeps its copy of info in memory until it is bridge.info_max_age old, so with more than one
	// replica the development clock moves past that and the test waits until every replica runs on it.
	recheck := func() {
		t.Helper()
		e.check(b.conn)
		if len(h.Replicas) > 1 {
			c := advance(t, r, int(bridge.DefaultInfoMaxAge/time.Second)+1)
			for _, rep := range h.Replicas {
				eventually(t, rep.name()+" on the advanced clock", func() bool {
					return readClock(t, rep).OffsetSeconds == c.OffsetSeconds
				})
			}
		}
	}
	acknowledged := func(g string) {
		t.Helper()
		eventually(t, g+" acknowledged", func() bool { return b.status(g) == "acknowledged" })
		if tl := b.timeline(g, "&kind=status"); len(tl) == 0 || tl[0].Event != "acknowledged" ||
			tl[0].Actor.Transport != "bridge" || tl[0].Actor.Name != "Alice A." {
			t.Errorf("the last status change of %s: %+v", g, tl)
		}
	}
	// edited waits until the page shows the Root message id with the status and buttons of acknowledged.
	edited := func(id string) {
		t.Helper()
		eventually(t, "the acknowledged Root message "+id+" on the page", func() bool {
			p := x.message(id)
			return p.Status == "acknowledged" && p.Edits > 0 && slices.Equal(p.labels()[:min(2, len(p.Buttons))],
				[]string{"Unack", "Resolve"}) && !slices.Contains(p.labels(), "Ack")
		})
	}

	// Poll mode: the Root message on the page, the Account link by token and a press of Ack.
	g1 := b.start("e1", "ExampleOne", "example", "one")
	root := x.root(b.number(g1))
	if !slices.Contains(root.labels(), "Ack") || root.Status != "firing" {
		t.Fatalf("the Root message %+v", root)
	}
	link, _ := alice.session.json(http.MethodPost, "/api/v1/me/account-links/bridge",
		`{"connection_id":"`+b.conn+`"}`, http.StatusCreated)["token"].(string)
	if !bridgeToken.MatchString(link) {
		t.Fatal("the started link has no token of 43 base64url characters")
	}
	*x.secrets = append(*x.secrets, link)
	x.act("direct-message", map[string]any{"user": "alice", "text": link}, http.StatusCreated)
	eventually(t, "the bot's answer in alice's direct chat", func() bool {
		return slices.Contains(x.botDirect("alice"), "Linked to Muster user Alice A. (alice)")
	})
	l := linkOf(t, alice.session.json(http.MethodGet, "/api/v1/me/account-links", "", http.StatusOK), "bridge")
	if connection, _ := l["connection"].(map[string]any); l["external_id"] != "alice" ||
		l["display_name"] != "Alice Smith" || connection["name"] != "Example bridge" {
		t.Errorf("alice's bridge link %v", l)
	}
	x.press(root.ID, "alice", x.button(root, "Ack"))
	acknowledged(g1)
	edited(root.ID)

	// Without buttons: ack written in the Thread of a new Alert Group.
	x.restart(exampleRun{features: "thread_messages"})
	recheck()
	g2 := b.start("e2", "ExampleTwo", "example", "two")
	n2 := b.number(g2)
	root = x.root(n2)
	if len(root.Buttons) != 0 {
		t.Errorf("buttons without the feature: %v", root.labels())
	}
	x.act("thread", map[string]any{"message_id": root.ID, "user": "alice", "text": "ack"}, http.StatusCreated)
	acknowledged(g2)
	done := fmt.Sprintf("Done: Acknowledged #%d", n2)
	eventually(t, "the answer "+done+" in the Thread", func() bool {
		return slices.ContainsFunc(x.message(root.ID).Thread, func(p examplePost) bool { return p.Bot && p.Text == done })
	})

	// Push mode: a press reaches Muster as a signed push to the Connection's callback. No delivery is pending when the
	// Connection changes, so that none reads info for its new version while the example offers thread_messages only.
	b.settled()
	a := b.api.do(http.MethodGet, "/api/v1/connections/"+b.conn, "")
	var conn map[string]any
	decode(t, a, &conn)
	changed, _ := json.Marshal(map[string]any{"type": "bridge", "name": conn["name"], "base_url": conn["base_url"],
		"event_delivery": "push", "push_secret": x.secret, "proxy": map[string]bool{"enabled": false},
		"limiter": conn["limiter"]})
	conn = b.api.json(http.MethodPut, "/api/v1/connections/"+b.conn, string(changed), http.StatusOK, "If-Match",
		a.header.Get("ETag"))
	callback := fmt.Sprint(conn["callback_url"])
	if callback != devmode.IngestURL+"/api/v1/callbacks/bridge/"+b.conn {
		t.Fatalf("callback_url %s", callback)
	}
	x.restart(exampleRun{push: callback})
	recheck()
	g3 := b.start("e3", "ExampleThree", "example", "three")
	root = x.root(b.number(g3))
	x.press(root.ID, "alice", x.button(root, "Ack"))
	acknowledged(g3)
	var push map[string]any
	eventually(t, "the signed push of the press in the Request log", func() bool {
		for _, rec := range b.inbound(b.conn, "push") {
			id, ok := rec["id"].(string)
			if !ok {
				t.Fatalf("a Request log record without id: %v", rec)
			}
			full, _ := b.requestRecord(id)
			var ev struct {
				Type      string `json:"type"`
				MessageID string `json:"message_id"`
			}
			if body, ok := full["request_body"].(string); ok && json.Unmarshal([]byte(body), &ev) == nil &&
				ev.Type == "press" && ev.MessageID == root.ID {
				push = full
				return true
			}
		}
		return false
	})
	headers, _ := push["request_headers"].(map[string]any)
	signature, _ := headers["Webhook-Signature"].([]any)
	if push["status"] != 204.0 || len(signature) != 1 || signature[0] != "[redacted]" {
		t.Errorf("the push of the press: status %v, signature %v", push["status"], signature)
	}
	edited(root.ID)

	for _, rep := range h.Replicas {
		x.leaked(t, rep.name()+"'s output", rep.Output())
	}
	x.leaked(t, "the example's output", x.output())
}

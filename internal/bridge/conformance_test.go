// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/webhooks"
)

// everyFeature are the lines of a run with --no-interactive against the fake bridge with every feature.
var everyFeature = []string{"PASS info", "PASS channel_check", "PASS post", "PASS replayed_key", "PASS buttons",
	"PASS edit", "PASS thread_reply", "PASS thread_ref", "PASS forbidden_channel", "PASS user_lookup",
	"PASS direct_message", "PASS delete", "PASS poll", "SKIP press: --no-interactive", "SKIP push: --no-interactive",
	"PASS answer"}

func startFake(t *testing.T) *fakebridge.Fake {
	t.Helper()
	f := fakebridge.New()
	if err := f.Start(t.Context(), "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close(context.WithoutCancel(t.Context())) })
	return f
}

// configure changes the fake's configuration by patch, as PUT /_fake/config does.
func configure(t *testing.T, f *fakebridge.Fake, patch string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, f.URL()+"/_fake/config",
		strings.NewReader(patch))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /_fake/config %s = %d", patch, resp.StatusCode)
	}
}

// lineWriter collects the lines of a run and hands each to on, if set, as soon as it is written.
type lineWriter struct {
	pending string
	lines   []string
	on      func(line string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.pending += string(p)
	for {
		line, rest, ok := strings.Cut(w.pending, "\n")
		if !ok {
			return len(p), nil
		}
		w.pending = rest
		w.lines = append(w.lines, line)
		if w.on != nil {
			w.on(line)
		}
	}
}

// conf is a run against the fake's second bot, with every flag.
func conf(f *fakebridge.Fake, out io.Writer) ConformanceConfig {
	return ConformanceConfig{BaseURL: f.URL(), Token: fakebridge.SecondToken, Channel: fakebridge.ChannelAlerts,
		ForbiddenChannel: fakebridge.ChannelNoBot, Handle: "alice", Out: out}
}

func run(t *testing.T, cfg ConformanceConfig) ([]string, bool) {
	t.Helper()
	w, ok := cfg.Out.(*lineWriter)
	if !ok {
		w = &lineWriter{}
		cfg.Out = w
	}
	failed, err := RunConformance(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return w.lines, failed
}

func sameLines(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("lines\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestConformanceEveryFeature is C-22.FR-21 and AC-22 against the fake with every feature: every step passes but
// press and push, which --no-interactive skips; the test messages are Quiet messages of the kind test titled by their
// step, the buttons one of each style hint with 64 bytes of data, and the post message was edited, replied to twice and
// deleted by the bot without a message_deleted event. A second run passes too: its keys are new.
func TestConformanceEveryFeature(t *testing.T) {
	f := startFake(t)
	lines, failed := run(t, conf(f, nil))
	sameLines(t, lines, everyFeature)
	if failed {
		t.Fatal("failed")
	}
	ms := f.Messages()
	if len(ms) != 2 {
		t.Fatalf("messages %+v", ms)
	}
	post, buttons := ms[0], ms[1]
	if post.Bot != 2 || !post.Deleted || post.Envelope.Title != "muster bridge check: post" ||
		len(post.Edits) != 1 || post.Edits[0].Title != "muster bridge check: edit" || len(post.Replies) != 2 ||
		post.Replies[0].SentThreadRef != nil || post.Replies[1].SentThreadRef == nil ||
		*post.Replies[1].SentThreadRef != "t-m1" {
		t.Errorf("post %+v", post)
	}
	var styles []string
	for _, b := range buttons.Envelope.Buttons {
		styles = append(styles, string(b.Style))
		if len(b.Data) != 64 || b.Label != string(b.Style) {
			t.Errorf("button %+v", b)
		}
	}
	if !slices.Equal(styles, []string{"primary", "success", "neutral", "danger"}) || buttons.Deleted {
		t.Errorf("buttons %+v", buttons)
	}
	for _, m := range ms {
		if m.Envelope.Kind != "test" || m.Envelope.Notify || m.ChannelID != fakebridge.ChannelAlerts {
			t.Errorf("message %+v", m.Envelope)
		}
	}
	if evs := f.Events(); len(evs) != 0 {
		t.Errorf("events %+v", evs)
	}
	if dms := f.DirectMessages(); len(dms) != 1 || dms[0].UserExternalID != fakebridge.AliceID || dms[0].Bot != 2 {
		t.Errorf("direct messages %+v", dms)
	}
	lines, failed = run(t, conf(f, nil))
	sameLines(t, lines, everyFeature)
	if failed {
		t.Fatal("the second run failed")
	}
}

// TestConformanceReducedFeatures: the steps of a feature the bridge does not offer, or of a flag not given, are
// skipped with the reason, and the run passes; a person the bridge does not find fails user_lookup.
func TestConformanceReducedFeatures(t *testing.T) {
	f := startFake(t)
	configure(t, f, `{"capabilities":{"thread_messages":"all"},"user_lookup":"none","event_delivery":["poll"]}`)
	cfg := conf(f, nil)
	cfg.ForbiddenChannel, cfg.Handle = "", ""
	lines, failed := run(t, cfg)
	sameLines(t, lines, []string{"PASS info", "PASS channel_check", "PASS post", "PASS replayed_key",
		"SKIP buttons: the bridge does not offer buttons", "PASS edit", "PASS thread_reply", "PASS thread_ref",
		"SKIP forbidden_channel: no --forbidden-channel", "SKIP user_lookup: the bridge looks up nobody",
		"SKIP direct_message: the bridge does not offer direct_messages",
		"SKIP delete: the bridge does not offer delete", "PASS poll", "SKIP press: the bridge does not offer buttons",
		"SKIP push: the bridge does not offer push", "PASS answer"})
	if failed {
		t.Fatal("failed")
	}

	configure(t, f, `{"capabilities":{"direct_messages":{"bot_can_start":false}},"user_lookup":"login"}`)
	lines, _ = run(t, conf(f, nil))
	if !slices.Contains(lines, "PASS user_lookup") ||
		!slices.Contains(lines, "SKIP direct_message: the bot cannot start a direct chat (bot_can_start is false)") {
		t.Errorf("lines %q", lines)
	}

	configure(t, f, `{"capabilities":{"direct_messages":{"bot_can_start":true}}}`)
	cfg = conf(f, nil)
	cfg.Handle = "nobody"
	lines, failed = run(t, cfg)
	if !failed || !slices.Contains(lines, "FAIL user_lookup: the bridge found nobody with the handle nobody") ||
		!slices.Contains(lines, "SKIP direct_message: no person found") {
		t.Errorf("lines %q", lines)
	}
	cfg.Handle = ""
	if lines, _ = run(t, cfg); !slices.Contains(lines, "SKIP user_lookup: no --handle") {
		t.Errorf("lines %q", lines)
	}
}

// TestConformanceReplaysOff: a bridge that posts again under a repeated Idempotency-Key fails replayed_key.
func TestConformanceReplaysOff(t *testing.T) {
	f := startFake(t)
	configure(t, f, `{"replays":false}`)
	lines, failed := run(t, conf(f, nil))
	re := regexp.MustCompile(`^FAIL replayed_key: the second request with the same Idempotency-Key created message ` +
		`m\d+ instead of returning m\d+$`)
	if !failed || !re.MatchString(lines[3]) {
		t.Fatalf("failed %v, lines %q", failed, lines)
	}
}

// TestConformanceInfoFails: a bridge of another protocol version and a token it refuses fail info, and nothing else
// runs.
func TestConformanceInfoFails(t *testing.T) {
	f := startFake(t)
	configure(t, f, `{"protocol_version":"2"}`)
	lines, failed := run(t, conf(f, nil))
	sameLines(t, lines, []string{"FAIL info: The bridge speaks protocol version 2; this Muster speaks 1."})
	if !failed {
		t.Fatal("passed")
	}
	if reqs := f.Requests(); len(reqs) != 1 || reqs[0].Method != http.MethodGet || reqs[0].Path != "/v1/info" {
		t.Errorf("requests %+v", reqs)
	}
	configure(t, f, `{"protocol_version":"1"}`)
	cfg := conf(f, nil)
	cfg.Token = "not-a-token"
	lines, _ = run(t, cfg)
	sameLines(t, lines, []string{"FAIL info: answered 401 forbidden: The bridge refused the token."})
}

// TestConformanceStepFailures: a channel the bot can use is not forbidden, a failed post fails the steps that need
// its message, and an event that is not one of the protocol fails poll.
func TestConformanceStepFailures(t *testing.T) {
	f := startFake(t)
	cfg := conf(f, nil)
	cfg.ForbiddenChannel = fakebridge.ChannelAlerts
	if lines, _ := run(t, cfg); !slices.Contains(lines, "FAIL forbidden_channel: answered 200 instead of forbidden") {
		t.Errorf("lines %q", lines)
	}
	cfg.ForbiddenChannel = "ch-unreachable"
	if err := f.SetFault(fakeserver.Fault{Path: "/v1/channels/ch-unreachable/check", Status: http.StatusBadRequest,
		ContentType: "application/problem+json", Body: `{"class":"invalid","detail":"odd"}`, Times: 1}); err != nil {
		t.Fatal(err)
	}
	if lines, _ := run(t, cfg); !slices.Contains(lines,
		"FAIL forbidden_channel: expected forbidden; answered 400 invalid: odd") {
		t.Errorf("lines %q", lines)
	}

	if err := f.SetFault(fakeserver.Fault{Path: "/v1/messages", Status: http.StatusBadRequest,
		ContentType: "application/problem+json", Body: `{"class":"invalid","detail":"no"}`, Times: 1}); err != nil {
		t.Fatal(err)
	}
	lines, failed := run(t, conf(f, nil))
	for _, want := range []string{"FAIL post: answered 400 invalid: no", "FAIL replayed_key: the post step failed",
		"FAIL edit: the post step failed", "FAIL thread_reply: the post step failed",
		"FAIL thread_ref: the thread_reply step failed", "FAIL delete: the post step failed", "PASS buttons"} {
		if !slices.Contains(lines, want) {
			t.Errorf("no %q in %q", want, lines)
		}
	}
	if !failed {
		t.Error("passed")
	}

	if err := f.SetFault(fakeserver.Fault{Path: "/v1/events", Status: http.StatusOK, ContentType: "application/json",
		Body: `{"events":[{"id":"x"}],"next_cursor":"0"}`, Times: 1}); err != nil {
		t.Fatal(err)
	}
	lines, _ = run(t, conf(f, nil))
	if !slices.ContainsFunc(lines, func(l string) bool {
		return strings.HasPrefix(l, "FAIL poll: event 1 of the first answer: the body is not an event")
	}) {
		t.Errorf("lines %q", lines)
	}
	if err := f.SetFault(fakeserver.Fault{Path: "/v1/messages/*", Status: http.StatusOK,
		ContentType: "application/json", Body: `{}`, Times: 2}); err != nil {
		t.Fatal(err)
	}
	if lines, _ = run(t, conf(f, nil)); !slices.Contains(lines, "FAIL delete: answered 200 instead of 204") {
		t.Errorf("lines %q", lines)
	}
}

// interactive is an interactive run against f that points the fake's push target at the listener, with secret when it
// is not empty, and presses the button primary of the message the WAIT line names, with data when it is not nil.
func interactive(t *testing.T, f *fakebridge.Fake, secret string, data *string) (*lineWriter, ConformanceConfig) {
	t.Helper()
	w := &lineWriter{}
	w.on = func(line string) {
		switch kind, rest, _ := strings.Cut(line, " "); kind {
		case "PUSH":
			fields := strings.Fields(rest)
			if secret == "" {
				secret = fields[len(fields)-1]
			}
			b, _ := json.Marshal(map[string]any{"push": map[string]string{"url": fields[0], "secret": secret}})
			configure(t, f, string(b))
		case "WAIT":
			_, id, _ := strings.Cut(rest, ", message_id ")
			if _, err := f.Press(t.Context(), fakebridge.Press{MessageID: id, UserID: fakebridge.AliceID,
				Button: "primary", Data: data}); err != nil {
				t.Error(err)
			}
		}
	}
	cfg := conf(f, w)
	cfg.Interactive = true
	return w, cfg
}

// TestConformanceInteractive is the interactive run: the PUSH line before the WAIT line names the message by its id,
// the press arrives by polling and as a signed push under the same id, and the check answers it.
func TestConformanceInteractive(t *testing.T) {
	f := startFake(t)
	w, cfg := interactive(t, f, "", nil)
	lines, failed := run(t, cfg)
	push := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "PUSH http://127.0.0.1:") })
	wait := slices.IndexFunc(lines, func(l string) bool {
		return strings.HasPrefix(l, `WAIT press: press a button of the message "muster bridge check: buttons" within `+
			`5m0s, message_id m2`)
	})
	if failed || push < 0 || wait != push+1 || len(strings.Fields(lines[push])) != 3 {
		t.Fatalf("failed %v, lines %q", failed, w.lines)
	}
	sameLines(t, slices.Delete(slices.Clone(lines), push, wait+1), slices.Concat(everyFeature[:13],
		[]string{"PASS press", "PASS push", "PASS answer"}))
	answers := f.Answers()
	if len(answers) != 1 || answers[0].Bot != 2 || answers[0].EventType != "press" || answers[0].ShownAs != "ephemeral" ||
		answers[0].Text != "muster bridge check: answer" {
		t.Errorf("answers %+v", answers)
	}
}

// TestConformanceWrongPushSecret: a push signed with another secret fails push, though the press came by polling.
func TestConformanceWrongPushSecret(t *testing.T) {
	f := startFake(t)
	_, cfg := interactive(t, f, string(webhooks.NewSigningSecret()), nil)
	lines, failed := run(t, cfg)
	re := regexp.MustCompile(`^FAIL push: the push of event e\d+ has a signature that does not match the push secret$`)
	if !failed || !slices.Contains(lines, "PASS press") || !slices.ContainsFunc(lines, re.MatchString) {
		t.Fatalf("failed %v, lines %q", failed, lines)
	}
}

// TestConformancePushNeverArrives: a bridge that pushes nothing fails push once the wait for it ends.
func TestConformancePushNeverArrives(t *testing.T) {
	f := startFake(t)
	w, cfg := interactive(t, f, "", nil)
	on := w.on
	w.on = func(line string) {
		if !strings.HasPrefix(line, "PUSH ") {
			on(line)
		}
	}
	cfg.timer = firesAtOnce
	lines, failed := run(t, cfg)
	re := regexp.MustCompile(`^FAIL push: no push of event e\d+ reached the listener within 30s$`)
	if !failed || !slices.Contains(lines, "PASS press") || !slices.ContainsFunc(lines, re.MatchString) ||
		!slices.Contains(lines, "PASS answer") {
		t.Fatalf("failed %v, lines %q", failed, lines)
	}
}

// TestConformancePushOnly: a bridge that only pushes is pressed through the listener; a push secret given is not
// printed.
func TestConformancePushOnly(t *testing.T) {
	f := startFake(t)
	configure(t, f, `{"event_delivery":["push"]}`)
	given := webhooks.NewSigningSecret()
	_, cfg := interactive(t, f, string(given), nil)
	cfg.PushSecret = given
	lines, failed := run(t, cfg)
	push := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "PUSH ") })
	if failed || push < 0 || len(strings.Fields(lines[push])) != 2 || strings.Contains(strings.Join(lines, "\n"),
		string(given)) {
		t.Fatalf("failed %v, lines %q", failed, lines)
	}
	for _, want := range []string{"SKIP poll: the bridge does not offer poll", "PASS press", "PASS push",
		"PASS answer"} {
		if !slices.Contains(lines, want) {
			t.Errorf("no %q in %q", want, lines)
		}
	}
}

// TestConformancePushOnlyFailures: a listener that cannot listen fails press and push, and one that receives no press
// fails them once Wait passed, naming the pushes it refused.
func TestConformancePushOnlyFailures(t *testing.T) {
	f := startFake(t)
	configure(t, f, `{"event_delivery":["push"]}`)
	taken, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = taken.Close() }()
	cfg := conf(f, nil)
	cfg.Interactive, cfg.PushListen = true, taken.Addr().String()
	lines, failed := run(t, cfg)
	prefix := "the push listener cannot listen on " + taken.Addr().String() + ": "
	if !failed || !slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "FAIL press: "+prefix) }) ||
		!slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "FAIL push: "+prefix) }) {
		t.Fatalf("lines %q", lines)
	}

	cfg = conf(f, nil)
	cfg.Interactive, cfg.timer = true, firesAtOnce
	lines, failed = run(t, cfg)
	if !failed || !slices.Contains(lines, "FAIL press: no press of message m4 within 5m0s") ||
		!slices.Contains(lines, "FAIL push: the press step saw no press") {
		t.Fatalf("lines %q", lines)
	}

	// The summary of the pushes the listener refused.
	l := newPushListener(webhooks.NewSigningSecret(), clock.Real{})
	l.record(pushAttempt{id: "e1", problem: "has a signature that does not match the push secret"})
	cn := &conformance{cfg: ConformanceConfig{Wait: time.Minute, timer: firesAtOnce}, listener: l, buttonsID: "m2"}
	if ev, why := cn.pressByPush(t.Context()); ev != nil || why != "no press of message m2 within 1m0s; the push "+
		"listener refused 1 push, which has a signature that does not match the push secret" {
		t.Errorf("press %+v, %q", ev, why)
	}
}

// firesAtOnce is a timer that has fired.
func firesAtOnce(time.Duration) (<-chan time.Time, func()) {
	ch := make(chan time.Time, 1)
	ch <- time.Time{}
	return ch, func() {}
}

// TestConformancePressTimeout: without a press within Wait, polled on the real clock, press fails and answer checks
// an unknown event id.
func TestConformancePressTimeout(t *testing.T) {
	f := startFake(t)
	configure(t, f, `{"event_delivery":["poll"]}`)
	manual := clock.NewManual(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	cfg := conf(f, nil)
	cfg.Interactive, cfg.Real, cfg.Wait, cfg.longPoll = true, manual, 3*time.Second, time.Nanosecond
	cfg.sleep = func(_ context.Context, d time.Duration) { manual.Advance(d) }
	lines, failed := run(t, cfg)
	if !failed || !slices.Contains(lines, "FAIL press: no press of message m2 within 3s") ||
		!slices.Contains(lines, "SKIP push: the bridge does not offer push") || lines[len(lines)-1] != "PASS answer" {
		t.Fatalf("lines %q", lines)
	}
}

// TestConformancePressDataChanged: a press whose data is none of the buttons' fails press, and is still answered.
func TestConformancePressDataChanged(t *testing.T) {
	f := startFake(t)
	configure(t, f, `{"event_delivery":["poll"]}`)
	changed := "changed"
	_, cfg := interactive(t, f, "", &changed)
	lines, failed := run(t, cfg)
	if !failed || !slices.Contains(lines, "FAIL press: the press of message m2 carries data that none of its "+
		"buttons has") || lines[len(lines)-1] != "PASS answer" || len(f.Answers()) != 1 {
		t.Fatalf("lines %q", lines)
	}
}

// TestConformanceInterrupted: a run whose context ends while it waits for a person fails the step and returns the
// end of the context.
func TestConformanceInterrupted(t *testing.T) {
	f := startFake(t)
	for _, mode := range []string{`["poll"]`, `["push"]`} {
		configure(t, f, `{"event_delivery":`+mode+`}`)
		ctx, cancel := context.WithCancel(t.Context())
		w := &lineWriter{on: func(line string) {
			if strings.HasPrefix(line, "WAIT ") {
				cancel()
			}
		}}
		cfg := conf(f, w)
		cfg.Interactive = true
		failed, err := RunConformance(ctx, cfg)
		cancel()
		if !failed || !errors.Is(err, context.Canceled) || w.lines[len(w.lines)-1] != "FAIL press: interrupted" {
			t.Fatalf("%s: failed %v, err %v, lines %q", mode, failed, err, w.lines)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if failed, err := RunConformance(ctx, conf(f, nil)); !failed || !errors.Is(err, context.Canceled) {
		t.Fatalf("a run interrupted before it started: %v %v", failed, err)
	}
	if _, err := RunConformance(t.Context(), ConformanceConfig{BaseURL: "ftp://x"}); !errors.Is(err, ErrBaseURL) {
		t.Fatalf("a bad base URL: %v", err)
	}
}

// TestConformanceKeepsSecrets is the secret half of C-22.FR-21: against a bridge whose problems echo the token and the
// push secret, no line, log line or error shows either.
func TestConformanceKeepsSecrets(t *testing.T) {
	echo := "token " + testToken + " push " + testPushSecret
	b := startStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/info" {
			answerJSON(http.StatusOK, "application/json", string(validInfo(t, nil)))(w, r)
			return
		}
		p, _ := json.Marshal(map[string]any{"class": "forbidden", "detail": echo + " " + r.Header.Get("Authorization"),
			"type": "urn:" + testToken})
		answerJSON(http.StatusForbidden, "application/problem+json", string(p))(w, r)
	})
	var log bytes.Buffer
	cfg := ConformanceConfig{BaseURL: b.srv.URL, Token: testToken, PushSecret: testPushSecret, Channel: "c",
		ForbiddenChannel: "f", Handle: "h", Log: logging.New(&log, logging.LevelInfo)}
	lines, failed := run(t, cfg)
	all := strings.Join(lines, "\n") + log.String()
	if !failed || len(lines) != 16 || !strings.Contains(all, "[redacted]") || strings.Contains(all, testToken) ||
		strings.Contains(all, testPushSecret) {
		t.Fatalf("lines %q, log %s", lines, log.String())
	}
	if !slices.Contains(lines, "FAIL answer: expected gone for an unknown event id; answered 403 forbidden: token "+
		"[redacted] push [redacted] Bearer [redacted]") {
		t.Errorf("lines %q", lines)
	}
}

// signedPush is a push of body under id, signed with secret at at.
func signedPush(t *testing.T, secret logging.Secret, id string, at time.Time, body string) *http.Request {
	t.Helper()
	sig, err := webhooks.Sign([]logging.Secret{secret}, id, at, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/any", strings.NewReader(body))
	r.Header.Set(webhooks.HeaderID, id)
	r.Header.Set(webhooks.HeaderTimestamp, strconv.FormatInt(at.Unix(), 10))
	r.Header.Set(webhooks.HeaderSignature, sig)
	return r
}

// TestPushListener: the listener answers like bridgeEvent and keeps every push, the refused ones with why.
func TestPushListener(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	secret := webhooks.NewSigningSecret()
	l := newPushListener(secret, clock.NewManual(now))
	press := `{"id":"e1","type":"press","occurred_at":"2026-10-01T12:00:00Z","message_id":"m2",` +
		`"user":{"external_id":"u-alice","display_name":"Alice"},"data":"d1"}`
	serve := func(r *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		l.ServeHTTP(rec, r)
		return rec
	}
	unsigned := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(press))
	badSig := signedPush(t, secret, "e1", now, press)
	badSig.Header.Set(webhooks.HeaderSignature, "v1,AAAA")
	for _, tc := range []struct {
		name   string
		req    *http.Request
		status int
	}{
		{"GET", httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil), http.StatusMethodNotAllowed},
		{"unsigned", unsigned, http.StatusUnauthorized},
		{"wrong signature", badSig, http.StatusUnauthorized},
		{"stale", signedPush(t, secret, "e1", now.Add(-6*time.Minute), press), http.StatusUnauthorized},
		{"too large", signedPush(t, secret, "e1", now, strings.Repeat("x", MaxEventBytes+1)), http.StatusBadRequest},
		{"not an event", signedPush(t, secret, "e1", now, `{"id":"e1"}`), http.StatusBadRequest},
		{"id mismatch", signedPush(t, secret, "e9", now, press), http.StatusBadRequest},
		{"valid", signedPush(t, secret, "e1", now, press), http.StatusNoContent},
	} {
		if rec := serve(tc.req); rec.Code != tc.status {
			t.Errorf("%s = %d %s", tc.name, rec.Code, rec.Body)
		}
	}
	if ev := l.press("m2"); ev == nil || ev.ID != "e1" || ev.Data != "d1" || l.press("m1") != nil {
		t.Errorf("press %+v", ev)
	}
	if a, ok := l.attempt("e1"); !ok || a.event == nil {
		t.Errorf("attempt %+v", a)
	}
	if a, ok := l.attempt("e9"); !ok || a.problem != "carries an event whose id is not its webhook-id" {
		t.Errorf("attempt %+v", a)
	}
	if _, ok := l.attempt("e5"); ok {
		t.Error("an attempt that never came")
	}
	if got := l.refusals(); got != "; the push listener refused 6 pushes, the last of which carries an event whose id "+
		"is not its webhook-id" {
		t.Errorf("refusals %q", got)
	}
	l = newPushListener(secret, clock.NewManual(now))
	if l.refusals() != "" {
		t.Error("refusals of none")
	}
	serve(signedPush(t, secret, "e1", now.Add(6*time.Minute), press))
	if got := l.refusals(); got != "; the push listener refused 1 push, which has a webhook-timestamp more than "+
		"5m0s from this machine's clock" {
		t.Errorf("refusals %q", got)
	}
	for range maxAttempts + 5 {
		serve(signedPush(t, secret, "e1", now, press))
	}
	if n := len(l.attempts); n != maxAttempts {
		t.Errorf("%d attempts kept", n)
	}
}

func TestValidPushSecret(t *testing.T) {
	for s, want := range map[logging.Secret]bool{
		webhooks.NewSigningSecret(): true,
		"whsec_YQ==":                true,
		"whsec_":                    false,
		"whsec_not base64":          false,
		"YQ==":                      false,
		"":                          false,
	} {
		if ValidPushSecret(s) != want {
			t.Errorf("%q: %v", s, !want)
		}
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package slack

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/outbound"
)

// notInChannel is chat.postEphemeral refusing a person who is not in the channel.
var notInChannel = answerJSON(http.StatusOK, `{"ok":false,"error":"user_not_in_channel"}`)

// TestAnswerEphemeral is C-24.FR-11 and AC-13: a refusal goes to the person with chat.postEphemeral in the channel of
// the press, without thread_ts on a Root message and with the Root message's ts on a Reminder, through the interactive
// path limited by the Connection alone, within the budget, as an answer; slack_press logs it as ephemeral.
func TestAnswerEphemeral(t *testing.T) {
	e := newPressEnv(t)
	resolve := e.act(t, buttons.CommandResolve, 0)
	line := e.press(t, pressIn{user: "U0VERA", value: resolve})
	if field(line, "answer") != answeredEphemeral || line["error"] != nil {
		t.Fatalf("line %v", line)
	}
	e.press(t, pressIn{user: "U0VERA", ts: pressReminderTS, value: e.remind(t, buttons.CommandUnacknowledge)})
	got := e.ephemerals(t)
	if len(got) != 2 || got[0]["channel"] != pressChannel || got[0]["user"] != "U0VERA" || got[0]["thread_ts"] != nil ||
		got[1]["thread_ts"] != pressRootTS || got[1]["text"] != "You are not permitted to do this" {
		t.Fatalf("answers %v", got)
	}
	subjects := e.path.calls()
	if len(subjects) != 2 || subjects[0].Connection == nil || *subjects[0].Connection != pressConnID ||
		subjects[0].Destination != nil || subjects[0].NoLimiter {
		t.Errorf("subjects %+v", subjects)
	}
	e.path.mu.Lock()
	ops, deadlines := slices.Clone(e.path.ops), slices.Clone(e.path.deadlines)
	e.path.mu.Unlock()
	for i := range ops {
		if ops[i] != "delivery.answerOp" || deadlines[i].IsZero() || time.Until(deadlines[i]) > 2*time.Second {
			t.Errorf("call %d: %s until %v", i, ops[i], deadlines[i])
		}
	}
	if n := len(e.responses()); n != 0 {
		t.Errorf("%d answers to the response_url", n)
	}
}

// TestAnswerFallsBack is C-24.FR-11 and AC-13: when chat.postEphemeral fails — the person is not in the channel, or
// no limiter token is free within the budget — the same text goes to the press's response_url as an ephemeral answer
// that replaces nothing, on the interactive path without a limiter; slack_press logs response_url and why the
// ephemeral failed. No request carries replace_original true.
func TestAnswerFallsBack(t *testing.T) {
	e := newPressEnv(t)
	e.answerWith(notInChannel, nil)
	line := e.press(t, pressIn{user: "U0CAROL", value: e.act(t, buttons.CommandResolve, 0)})
	if field(line, "answer") != answeredResponseURL || !strings.Contains(field(line, "error"),
		"the ephemeral answer was not sent: user_not_in_channel") {
		t.Fatalf("line %v", line)
	}
	want := `{"response_type":"ephemeral","replace_original":false,"text":"Your Slack account is not linked to ` +
		`Muster. Link it in your profile: http://localhost:8080/profile"}`
	if got := e.responses(); len(got) != 1 || got[0] != want {
		t.Fatalf("response_url bodies %q", got)
	}
	subjects := e.path.calls()
	if len(subjects) != 2 || !subjects[1].NoLimiter || subjects[1].Connection != nil {
		t.Errorf("subjects %+v", subjects)
	}

	e.path.mu.Lock()
	e.path.limited = true
	e.path.mu.Unlock()
	e.answerWith(answerJSON(http.StatusOK, `{"ok":true}`), nil)
	line = e.press(t, pressIn{user: "U0VERA", value: e.act(t, buttons.CommandResolve, 0)})
	if field(line, "answer") != answeredResponseURL || !strings.Contains(field(line, "error"),
		"no limiter token was free") {
		t.Fatalf("limited: %v", line)
	}
	if n := len(e.ephemerals(t)); n != 1 {
		t.Errorf("%d calls of chat.postEphemeral, want only the first press's", n)
	}
	for _, s := range e.slack.seen() {
		if strings.Contains(s.Body, `"replace_original":true`) || strings.Contains(s.Body, "delete_original") {
			t.Errorf("a request replaces the message: %s", s.Body)
		}
	}
}

// TestAnswerFailsBothWays is C-24.FR-11: an answer that fails both ways is logged and made once, never again; a press
// without a response_url, or with one that is not Slack's, is answered by chat.postEphemeral only.
func TestAnswerFailsBothWays(t *testing.T) {
	e := newPressEnv(t)
	e.answerWith(notInChannel, answerJSON(http.StatusNotFound, `{"ok":false,"error":"expired_url"}`))
	line := e.press(t, pressIn{user: "U0VERA", value: e.act(t, buttons.CommandResolve, 0)})
	if line["answer"] != nil || field(line, "error") != "the ephemeral answer was not sent: user_not_in_channel\n"+
		"the response_url answer was not sent: response_url answered 404: expired_url" {
		t.Fatalf("line %v", line)
	}
	if n, m := len(e.ephemerals(t)), len(e.responses()); n != 1 || m != 1 {
		t.Errorf("%d ephemerals and %d response_url answers", n, m)
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("an address that is not Slack's was called")
		w.WriteHeader(http.StatusOK)
	}))
	defer other.Close()
	line = e.press(t, pressIn{user: "U0VERA", value: e.act(t, buttons.CommandResolve, 0),
		responseURL: other.URL + "/actions/T0/1/abc"})
	if line["answer"] != nil || !strings.Contains(field(line, "error"), "the response_url is not Slack's") {
		t.Errorf("not Slack's: %v", line)
	}
	e.path.mu.Lock()
	e.path.err = &delivery.LimitedError{RetryAfter: time.Second}
	e.path.mu.Unlock()
	before := len(e.slack.seen())
	line = e.press(t, pressIn{user: "U0VERA", value: e.act(t, buttons.CommandResolve, 0)})
	if line["answer"] != nil || strings.Count(field(line, "error"), "no limiter token was free") != 2 ||
		len(e.slack.seen()) != before {
		t.Errorf("both refused by the path: %v", line)
	}
	e.path.mu.Lock()
	e.path.err = nil
	e.path.mu.Unlock()
	p := e.payload(t, pressIn{user: "U0VERA", channel: pressChannel, ts: pressRootTS,
		value: e.act(t, buttons.CommandResolve, 0)})
	var m map[string]any
	_ = json.Unmarshal(p, &m)
	delete(m, "response_url")
	p, _ = json.Marshal(m)
	e.presses.Handle(t.Context(), e.conn, p)
	lines := e.log.lines(t, "slack_press")
	if l := lines[len(lines)-1]; l["answer"] != nil || !strings.HasSuffix(field(l, "error"),
		"the press has no response_url") {
		t.Errorf("without a response_url: %v", l)
	}
}

// TestResponseURLPinned is C-24.FR-11 and AC-13: a response_url is called only when it is https on hooks.slack.com,
// without a port, user information or fragment — or, for a client `muster dev` routes to the fake server, on that
// server — and only in the interactive class; the client of a Connection that goes to Slack never calls another host.
func TestResponseURLPinned(t *testing.T) {
	s := startStandIn(t, answerJSON(http.StatusOK, `{"ok":true}`))
	n := network(t, nil)
	routed := newClient(t, n, settings(s.base()))
	direct := newClient(t, n, Settings{BotToken: testBot})
	for _, c := range []struct {
		client *Client
		url    string
		ok     bool
	}{
		{direct, "https://hooks.slack.com/actions/T0/1/abc", true},
		{direct, "https://HOOKS.slack.com/actions/T0/1/abc", true},
		{routed, "https://hooks.slack.com/actions/T0/1/abc", true},
		{routed, s.srv.URL + "/actions/T0/1/abc", true},
		{direct, s.srv.URL + "/actions/T0/1/abc", false},
		{direct, "http://hooks.slack.com/actions/T0/1/abc", false},
		{direct, "https://hooks.slack.com:8443/actions/T0/1/abc", false},
		{direct, "https://user@hooks.slack.com/actions/T0/1/abc", false},
		{direct, "https://hooks.slack.com/actions/T0/1/abc#x", false},
		{direct, "https://hooks.slack.com.example.org/actions/T0/1/abc", false},
		{direct, "https://evil.example.org/hooks.slack.com/actions", false},
		{direct, "https://hooks.slack.com", false},
		{direct, "::not a url", false},
		{routed, "http://127.0.0.1:1/actions/T0/1/abc", false},
	} {
		if _, ok := c.client.responseURLAllowed(c.url); ok != c.ok {
			t.Errorf("%s via %s: %v", c.url, c.client.Base(), ok)
		}
	}
	for _, url := range []string{s.srv.URL + "/actions/T0/1/abc", "http://hooks.slack.com/actions/T0/1/abc"} {
		if r := direct.respond(t.Context(), outbound.ClassInteractive, url, "x"); r.OK() ||
			string(r.Error) != "the response_url is not Slack's" {
			t.Errorf("%s: %+v", url, r)
		}
	}
	if r := routed.respond(t.Context(), outbound.ClassDelivery, s.srv.URL+"/actions/T0/1/abc", "x"); r.OK() {
		t.Errorf("the delivery class answered: %+v", r)
	}
	if got := s.seen(); len(got) != 0 {
		t.Fatalf("requests %+v", got)
	}
	if r := routed.respond(t.Context(), outbound.ClassInteractive, s.srv.URL+"/actions/T0/1/abc", "hello"); !r.OK() {
		t.Fatalf("the fake's address: %+v", r)
	}
	got := s.seen()
	if len(got) != 1 || got[0].Method != http.MethodPost || got[0].Path != "/actions/T0/1/abc" ||
		got[0].Body != `{"response_type":"ephemeral","replace_original":false,"text":"hello"}` ||
		got[0].Header.Get("Authorization") != "" || !strings.HasPrefix(got[0].Header.Get("Content-Type"),
		"application/json") {
		t.Errorf("request %+v", got)
	}
}

// TestRespondFailures: a response_url that answers ok: false, a status, a 429 with Retry-After, nothing at all, or that
// the outbound address policy blocks fails with a Result that names the status, Slack's error code or what failed —
// never the address, nor a body that echoes it.
func TestRespondFailures(t *testing.T) {
	const path = "/actions/T0/1/0123456789abcdef"
	echo := func(status int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"ok":false,"error":"`+r.URL.Path+`"}`) //nolint:gosec // G705: echoes the address on purpose
		}
	}
	for _, c := range []struct {
		name   string
		answer http.HandlerFunc
		class  Class
		text   string
	}{
		{"ok false", answerJSON(http.StatusOK, `{"ok":false,"error":"invalid_payload"}`), ClassUnknown,
			"response_url answered 200: invalid_payload"},
		{"echoed address", echo(http.StatusInternalServerError), ClassTransient, "response_url answered 500"},
		{"retry after", answerJSON(http.StatusTooManyRequests, `{}`, "Retry-After", "3"), ClassRetryAfter,
			"response_url answered 429"},
		{"gone", answerAs(http.StatusGone, "text/plain", "used_url"), ClassUnknown, "response_url answered 410"},
	} {
		s := startStandIn(t, c.answer)
		r := newClient(t, network(t, nil), settings(s.base())).respond(t.Context(), outbound.ClassInteractive,
			s.srv.URL+path, "x")
		if r.OK() || r.Classification.Class != c.class || string(r.Error) != c.text ||
			strings.Contains(string(r.Error), "actions") {
			t.Errorf("%s: %+v", c.name, r)
		}
		if c.class == ClassRetryAfter && r.Classification.RetryAfter != 3*time.Second {
			t.Errorf("%s: retry after %v", c.name, r.Classification.RetryAfter)
		}
	}
	s := startStandIn(t, answerJSON(http.StatusOK, `{}`))
	base, url := s.base(), s.srv.URL+path
	s.srv.Close()
	r := newClient(t, network(t, nil), settings(base)).respond(t.Context(), outbound.ClassInteractive, url, "x")
	if r.OK() || r.Status != 0 || r.Network != outbound.NetworkConnect || string(r.Error) != "response_url: connect" {
		t.Errorf("no answer: %+v", r)
	}
	p, err := outbound.ParsePolicy("standard", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	n := network(t, nil)
	n.Policy = outbound.StaticPolicy(p)
	r = newClient(t, n, settings(base)).respond(t.Context(), outbound.ClassInteractive, url, "x")
	if r.OK() || !r.Blocked || r.Classification.Class != ClassFatal ||
		string(r.Error) != "response_url: blocked by the outbound address policy" {
		t.Errorf("blocked: %+v", r)
	}
}

// TestAnswerLanguage is C-24.FR-11 and AC-13: answers are in the profile language of the linked User — Russian for a
// Russian-speaking User, its "Done" too — and in English for an account without a link; `&`, `<` and `>` are escaped
// for mrkdwn.
func TestAnswerLanguage(t *testing.T) {
	e := newPressEnv(t)
	other := e.sign(t, buttons.Action{Subject: buttons.SubjectRoot, PublicID: otherGroup,
		Command: buttons.CommandAcknowledge})
	e.press(t, pressIn{user: "U0IVAN", value: other})
	e.press(t, pressIn{user: "U0IVAN", ts: pressReminderTS, value: e.remind(t, buttons.CommandStillOnIt)})
	e.press(t, pressIn{user: "U0CAROL", value: other})
	e.presses.PublicURL = "http://localhost:8080/a&b<c>"
	e.press(t, pressIn{user: "U0CAROL", value: e.act(t, buttons.CommandResolve, 0)})
	got := e.ephemerals(t)
	want := []string{"Не удалось проверить эту кнопку; ничего не изменилось.", "Готово: Ещё работаю",
		"This button could not be verified; nothing was changed.",
		"Your Slack account is not linked to Muster. Link it in your profile: http://localhost:8080/a&amp;b&lt;c&gt;/profile"}
	if len(got) != len(want) {
		t.Fatalf("answers %v", got)
	}
	for i, w := range want {
		if got[i]["text"] != w {
			t.Errorf("answer %d = %q, want %q", i, got[i]["text"], w)
		}
	}
}

// TestAnswerRecords is C-24.FR-14, AC-17 and C-23.FR-2, FR-3: the answers are in the Connection's Request log as
// chat.postEphemeral and response_url, linked to the Destination and the Alert Group of the press; the response_url is
// [redacted] in the record's address and appears in no record, and the bot token is masked.
func TestAnswerRecords(t *testing.T) {
	e := newPressEnv(t)
	e.answerWith(notInChannel, nil)
	e.press(t, pressIn{user: "U0VERA", value: e.act(t, buttons.CommandResolve, 0)})
	recs := e.rec.got()
	if len(recs) != 2 {
		t.Fatalf("records %+v", recs)
	}
	want := outbound.Links{Destination: pressDest.ID, AlertGroup: pressAlertGroup}
	if recs[0].Operation != "chat.postEphemeral" || recs[0].Links != want ||
		recs[0].Owner.Connection != pressConnID || recs[0].RequestHeader.Get("Authorization") != "[redacted]" {
		t.Errorf("chat.postEphemeral %+v", recs[0])
	}
	if recs[1].Operation != "response_url" || recs[1].Links != want || recs[1].Owner.Connection != pressConnID ||
		recs[1].Method != http.MethodPost || recs[1].URL != e.slack.srv.URL+"[redacted]" || recs[1].Status != 200 {
		t.Errorf("response_url %+v", recs[1])
	}
	raw, _ := json.Marshal(recs)
	for _, s := range []string{"/actions/", "0123456789abcdef01234567", testBot, testSigning} {
		if strings.Contains(string(raw), s) {
			t.Errorf("a record shows %q: %s", s, raw)
		}
	}
	if strings.Contains(e.log.String(), "/actions/") {
		t.Errorf("the log shows the response_url: %s", e.log)
	}
}

// TestPostEphemeralArgs: chat.postEphemeral sends thread_ts only when it is set.
func TestPostEphemeralArgs(t *testing.T) {
	s := startStandIn(t, answerJSON(http.StatusOK, `{"ok":true,"message_ts":"1.1"}`))
	c := newClient(t, network(t, nil), settings(s.base()))
	for _, thread := range []string{"", "1.000001"} {
		if r := c.postEphemeral(t.Context(), outbound.ClassInteractive, "C0", "U0", thread, "hi"); !r.OK() {
			t.Fatalf("postEphemeral = %+v", r)
		}
	}
	got := s.seen()
	if len(got) != 2 || got[0].Path != "/api/chat.postEphemeral" ||
		got[0].Body != `{"channel":"C0","text":"hi","user":"U0"}` ||
		got[1].Body != `{"channel":"C0","text":"hi","thread_ts":"1.000001","user":"U0"}` {
		t.Errorf("requests %+v", got)
	}
}

// TestHooksClientIsBuiltOnce: the client of the response_url answers is built on the first answer and kept, and a
// proxy setting that outbound refuses fails the answer without a request.
func TestHooksClientIsBuiltOnce(t *testing.T) {
	s := startStandIn(t, answerJSON(http.StatusOK, `{"ok":true}`))
	c := newClient(t, network(t, nil), settings(s.base()))
	a, err := c.hooksClient()
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := c.hooksClient(); a != b {
		t.Error("the client is built again")
	}
	broken := &Client{network: Network{Log: logging.New(io.Discard, logging.LevelInfo), Real: clock.Real{}},
		base: s.base(), origin: s.srv.URL}
	if r := broken.respond(t.Context(), outbound.ClassInteractive, s.srv.URL+"/actions/T0/1/abc", "x"); r.OK() ||
		string(r.Error) != "response_url: the client could not be built" {
		t.Errorf("respond = %+v", r)
	}
}

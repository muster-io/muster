// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package mattermost_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mattermost"
	"github.com/muster-io/muster/internal/messages"
)

func (f *fakeBindings) TestDestination(_ context.Context, conn int64, publicID, channel string) (delivery.Destination,
	bool, error) {
	if f.testErr != nil {
		return delivery.Destination{}, false, f.testErr
	}
	d, ok := f.tests[postKey{publicID, channel}]
	return d, ok && conn == connID, nil
}

// presses records the bot's presses the callback reported.
type presses struct {
	nonces []string
	err    error
}

func (p *presses) Pressed(_ context.Context, nonce string) error {
	p.nonces = append(p.nonces, nonce)
	return p.err
}

// testBody is a MattermostActionRequest of a press of a test message, with the nonce of its test.
func testBody(user, channel, action, keyID, nonce string) string {
	b, _ := json.Marshal(map[string]any{"user_id": user, "user_name": "someone", "channel_id": channel,
		"team_id": fakemattermost.TeamID, "post_id": "post-test", "trigger_id": "tr", "type": "button",
		"context": map[string]string{"action": action, "key_id": keyID, "test": nonce}})
	return string(b)
}

// TestPressTestMessage is C-16.FR-3 and AC-4 at the callback: a person's press of a test message changes nothing and
// is answered privately "This is a test message; nothing was changed" — an ephemeral post, or the answer's
// ephemeral_text for a Member bot — in a channel of the Destination it names; elsewhere it is not verified and gets the
// empty answer. The bot's own press reports its nonce and gets the empty answer, with no ephemeral post.
func TestPressTestMessage(t *testing.T) {
	bots(t, func(t *testing.T, e *callbackEnv) {
		rec := &presses{}
		e.cfg.TestPresses = rec
		e.conns.conn.BotUserID = fakemattermost.BotUserID
		e.bindings.tests = map[postKey]delivery.Destination{{destA.PublicID, fakemattermost.ChannelAlerts}: destA}
		id, kid := e.action(t, buttons.SubjectTest, destA.PublicID, buttons.CommandAcknowledge, 0)
		text := e.press(t, http.MethodPost, connectionPublicID, testBody(fakemattermost.BobUserID,
			fakemattermost.ChannelAlerts, id, kid, "n1"))
		want := "This is a test message; nothing was changed"
		if e.admin {
			if eph := e.fake.Ephemeral(); text != "" || len(eph) != 1 || eph[0].Message != want {
				t.Errorf("admin bot: answer %q, ephemeral %+v", text, eph)
			}
		} else if text != want {
			t.Errorf("member bot: answer %q", text)
		}
		if l := e.last(t); l["outcome"] != "test" || l["command"] != "acknowledge" || len(e.commands.calls) != 0 ||
			len(rec.nonces) != 0 {
			t.Errorf("person %v %+v %v", l, e.commands.calls, rec.nonces)
		}
		// Another channel than the Destination's is not verified and answered with nothing.
		posted := len(e.ephemeralCalls())
		if text := e.press(t, http.MethodPost, connectionPublicID, testBody(fakemattermost.BobUserID,
			fakemattermost.ChannelAlertsProd, id, kid, "n1")); text != "" || len(e.ephemeralCalls()) != posted ||
			e.last(t)["outcome"] != "not_verified" {
			t.Errorf("other channel %q %v", text, e.last(t))
		}
		// The bot's own press during a test.
		if text := e.press(t, http.MethodPost, connectionPublicID, testBody(fakemattermost.BotUserID,
			fakemattermost.ChannelAlerts, id, kid, "n2")); text != "" || len(e.ephemeralCalls()) != posted ||
			e.last(t)["outcome"] != "test_press" || len(rec.nonces) != 1 || rec.nonces[0] != "n2" {
			t.Errorf("bot press %q %v %v", text, e.last(t), rec.nonces)
		}
		rec.err = errors.New("notify failed")
		e.press(t, http.MethodPost, connectionPublicID, testBody(fakemattermost.BotUserID,
			fakemattermost.ChannelAlerts, id, kid, "n3"))
		if l := e.last(t); l["outcome"] != "test_press" || l["error"] != "notify failed" {
			t.Errorf("failed report %v", l)
		}
		// A failed read of the Destination.
		e.bindings.testErr = errors.New("db down")
		e.press(t, http.MethodPost, connectionPublicID, testBody(fakemattermost.BobUserID,
			fakemattermost.ChannelAlerts, id, kid, ""))
		if l := e.last(t); l["outcome"] != "failed" || len(e.commands.calls) != 0 {
			t.Errorf("failed read %v", l)
		}
	})
}

// testerEnv is a Tester over the fake server, whose presses reach the callback served on loopback; the press waits
// are connected directly, as the notification of the runtime does.
type testerEnv struct {
	*callbackEnv
	tester  *mattermost.Tester
	targets *targets
	waits   *mattermost.PressWaits
	src     *messages.Source
}

func newTester(t *testing.T) *testerEnv {
	t.Helper()
	e := newCallback(t)
	e.conns.conn.BotUserID = fakemattermost.BotUserID
	waits := &mattermost.PressWaits{}
	waits.Notify = func(_ context.Context, channel, payload string) error {
		if channel != mattermost.TestPressChannel {
			return errors.New("wrong channel")
		}
		waits.Arrived(payload)
		return nil
	}
	e.cfg.TestPresses = waits
	mux := http.NewServeMux()
	mux.Handle(mattermost.CallbackPattern, mattermost.NewCallback(e.cfg))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	e.fake.SetAllowedUntrustedInternalConnections("127.0.0.1")
	c := e.conns.conn.Client
	ts := &targets{byID: map[int64]mattermost.Target{
		destA.ID: {Client: c, ConnectionID: connID, ConnectionPublicID: connectionPublicID, TeamID: fakemattermost.TeamID,
			TeamName: fakemattermost.TeamName, ChannelID: fakemattermost.ChannelAlerts,
			BotUserID: fakemattermost.BotUserID}}}
	r := messages.New(messages.Config{OrgID: 1, PublicURL: "http://localhost:8080", Business: e.business,
		Real: clock.Real{}, Keys: e.keys, Log: logging.New(&bytes.Buffer{}, logging.LevelInfo)})
	env := &testerEnv{callbackEnv: e, targets: ts, waits: waits,
		tester: &mattermost.Tester{Adapter: &mattermost.Adapter{Targets: ts, PublicURL: "http://localhost:8080",
			IngestURL: srv.URL + "/", Version: "v1.2.3"}, Path: e.cfg.Path, Renderer: r, Waits: waits,
			Budget: 2 * time.Second},
		src: &messages.Source{Number: 7, Title: "disk full", Status: messages.ColourFiring, SeverityLevel: "warning",
			TimeZone: "UTC", StartedAt: press0, Route: messages.RouteRef{Name: "db", Language: "en",
				SnoozeDurations: []int64{3600}}, TotalAlerts: 1,
			Alerts: []messages.SourceAlert{{Fingerprint: "f1", Labels: map[string]string{"alertname": "DiskFull",
				"pod": "db-1"}, StartsAt: press0, Firing: true}}}}
	return env
}

func (e *testerEnv) run(t *testing.T) []delivery.TestStep {
	t.Helper()
	steps, err := e.tester.Test(t.Context(), delivery.TestInput{Destination: destA, Source: e.src})
	if err != nil {
		t.Fatal(err)
	}
	return steps
}

// TestMattermostTest is C-16.FR-1, FR-2, FR-3, AC-1 and AC-7 against the fake server: one post marked as a test with
// the buttons of its status, whose action ids name the Destination; the bot's press of its first button reaches the
// callback, which answers it with nothing; the request shows the token masked and the response its status and body.
func TestMattermostTest(t *testing.T) {
	e := newTester(t)
	steps := e.run(t)
	if len(steps) != 2 || steps[0].Name != "message" || !steps[0].OK() || steps[1].Name != "press" ||
		!steps[1].OK() || steps[1].Error != "" {
		t.Fatalf("steps %+v", steps)
	}
	posts := e.fake.Posts()
	if len(posts) != 1 {
		t.Fatalf("posts %+v", posts)
	}
	att := attachmentOf(t, posts[0])
	if !strings.HasPrefix(att["title"].(string), "🧪 Test message · #7 disk full") ||
		!strings.HasPrefix(posts[0].Message, "🧪 Test message · 🔴 #7 disk full") {
		t.Errorf("mark %q %q", att["title"], posts[0].Message)
	}
	actions := att["actions"].([]any)
	ctx := actions[0].(map[string]any)["integration"].(map[string]any)["context"].(map[string]any)
	a, err := buttons.Verify(e.keys, ctx["action"].(string), ctx["key_id"].(string))
	if err != nil || a.Subject != buttons.SubjectTest || a.PublicID != destA.PublicID || ctx["test"] == "" {
		t.Errorf("action %+v %v %v", a, err, ctx)
	}
	if len(e.fake.Ephemeral()) != 0 || len(e.commands.calls) != 0 {
		t.Errorf("the bot's press was answered %+v %+v", e.fake.Ephemeral(), e.commands.calls)
	}
	req := steps[0].Request
	if req.Method != http.MethodPost || !strings.HasSuffix(req.URL, "/api/v4/posts") ||
		req.Headers[0] != [2]string{"Authorization", "Bearer [redacted]"} || !strings.Contains(*req.Body, "🧪") {
		t.Errorf("request %+v", req)
	}
	if steps[0].ResponseStatus != http.StatusCreated || !strings.Contains(*steps[0].ResponseBody, posts[0].ID) {
		t.Errorf("response %d %v", steps[0].ResponseStatus, steps[0].ResponseBody)
	}
	if p := steps[1].Request; !strings.HasSuffix(p.URL, "/api/v4/posts/"+posts[0].ID+"/actions/ack") ||
		steps[1].ResponseStatus != http.StatusOK {
		t.Errorf("press %+v %d", p, steps[1].ResponseStatus)
	}
	for _, st := range steps {
		b, _ := json.Marshal(st)
		if strings.Contains(string(b), token) {
			t.Errorf("the token shows: %s", b)
		}
	}
}

// TestMattermostPressNotReached is C-16.AC-7: when the server does not allow Muster's address, its answer "Action
// integration error" fails the step press with the class unknown and names AllowedUntrustedInternalConnections.
func TestMattermostPressNotReached(t *testing.T) {
	e := newTester(t)
	e.fake.SetAllowedUntrustedInternalConnections("")
	steps := e.run(t)
	if len(steps) != 2 || !steps[0].OK() || steps[1].ErrorClass != "unknown" ||
		steps[1].Error != "The button press did not reach Muster. Add the host of "+
			strings.TrimSuffix(e.tester.Adapter.IngestURL, "/")+
			" to ServiceSettings.AllowedUntrustedInternalConnections on the Mattermost server." ||
		steps[1].ResponseStatus != http.StatusBadRequest {
		t.Errorf("steps %+v", steps)
	}
	// A press that reaches the server but never the callback of this test fails the same way within the budget.
	e.tester.Waits = &mattermost.PressWaits{Notify: func(context.Context, string, string) error { return nil }}
	e.cfg.TestPresses = e.tester.Waits
	e.fake.SetAllowedUntrustedInternalConnections("127.0.0.1")
	e.tester.Budget = 300 * time.Millisecond
	steps = e.run(t)
	if len(steps) != 2 || steps[1].ErrorClass != "unknown" || !strings.HasPrefix(steps[1].Error,
		"The button press did not reach Muster.") {
		t.Errorf("no notification %+v", steps)
	}
}

// TestMattermostTestFailures: a failed post has no step press; a limited one sends nothing; a Destination without a
// Connection fails its step message; a source without buttons has no step press; a failure of the path is an error.
func TestMattermostTestFailures(t *testing.T) {
	e := newTester(t)
	control(t, http.MethodDelete, e.fake.URL()+"/_fake/channels/"+fakemattermost.ChannelAlerts+"/members/"+
		fakemattermost.BotUserID, "")
	if steps := e.run(t); len(steps) != 1 || steps[0].ErrorClass != "fatal" || steps[0].ResponseStatus != 403 {
		t.Errorf("not a member %+v", steps)
	}
	control(t, http.MethodPut, e.fake.URL()+"/_fake/channels/"+fakemattermost.ChannelAlerts+"/members/"+
		fakemattermost.BotUserID, "")
	e.tester.Path = &limitedPath{}
	if steps := e.run(t); len(steps) != 1 || steps[0].ErrorClass != "limited" || len(e.fake.Posts()) != 0 {
		t.Errorf("limited %+v", steps)
	}
	e.tester.Path = e.cfg.Path
	// A Connection whose bot no check learned does not press: its press would look like a person's.
	tg := e.targets.byID[destA.ID]
	tg.BotUserID = ""
	e.targets.byID[destA.ID] = tg
	if steps := e.run(t); len(steps) != 2 || !steps[0].OK() || steps[1].ErrorClass != "unknown" ||
		!strings.HasPrefix(steps[1].Error, "Muster does not know the bot") || len(e.fake.Ephemeral()) != 0 {
		t.Errorf("bot unknown %+v", steps)
	}
	tg.BotUserID = fakemattermost.BotUserID
	e.targets.byID[destA.ID] = tg
	e.src.Status, e.src.Alerts[0].Firing = messages.ColourResolved, false
	if steps := e.run(t); len(steps) != 1 || !steps[0].OK() {
		t.Errorf("resolved %+v", steps)
	}
	e.targets.err = errors.New("db down")
	if steps := e.run(t); len(steps) != 1 || steps[0].ErrorClass != "transient" {
		t.Errorf("no target %+v", steps)
	}
	e.targets.err = nil
	e.tester.Path = failingPath{}
	if _, err := e.tester.Test(t.Context(), delivery.TestInput{Destination: destA, Source: e.src}); err == nil {
		t.Error("a failed path is not an error")
	}
	e.src.Status, e.src.Alerts[0].Firing = messages.ColourFiring, true
	e.tester.Path = &pressFails{inner: e.cfg.Path}
	if _, err := e.tester.Test(t.Context(), delivery.TestInput{Destination: destA, Source: e.src}); err == nil {
		t.Error("a failed path of the press is not an error")
	}
}

// failingPath is an interactive path whose limiter cannot be read.
type failingPath struct{}

func (failingPath) Do(context.Context, delivery.Subject, delivery.Op) (delivery.Outcome, error) {
	return delivery.Outcome{}, errors.New("db down")
}

// pressFails is a path that fails its second call.
type pressFails struct {
	inner mattermost.Path
	n     int
}

func (p *pressFails) Do(ctx context.Context, s delivery.Subject, op delivery.Op) (delivery.Outcome, error) {
	p.n++
	if p.n > 1 {
		return delivery.Outcome{}, errors.New("db down")
	}
	return p.inner.Do(ctx, s, op)
}

// TestMattermostPreview is C-16.FR-4: the Root message as the Destination would receive it, in Markdown, its request
// the post with the attachment and its buttons, unmarked; nothing is sent.
func TestMattermostPreview(t *testing.T) {
	e := newTester(t)
	items, err := e.tester.Preview(t.Context(), delivery.TestInput{Destination: destA, Source: e.src})
	if err != nil || len(items) != 1 || items[0].Name != "message" || items[0].Format != "markdown" ||
		*items[0].Text != "🔴 #7 disk full" || !strings.Contains(*items[0].Request.Body, `"attachments"`) ||
		strings.Contains(*items[0].Request.Body, "🧪") || strings.Contains(*items[0].Request.Body, token) {
		t.Fatalf("preview %+v %v", items, err)
	}
	if len(e.fake.Requests()) != 0 {
		t.Errorf("sent %+v", e.fake.Requests())
	}
	e.targets.err = errors.New("db down")
	if _, err := e.tester.Preview(t.Context(), delivery.TestInput{Destination: destA, Source: e.src}); err == nil {
		t.Error("no target")
	}
}

// TestTestPublisherEdits: a test message is never edited and has no Thread.
func TestTestPublisherEdits(t *testing.T) {
	p := mattermost.NewTestPublisher()
	if p.Update(t.Context(), delivery.Call{}, "x", delivery.Message{}).Kind != delivery.OutcomeFatal ||
		p.Reply(t.Context(), delivery.Call{}, delivery.Root{}, delivery.Message{}).Kind != delivery.OutcomeFatal ||
		p.LengthLimit() != mattermost.LengthLimit {
		t.Error("a test publisher edits")
	}
	(&mattermost.PressWaits{}).Arrived("unknown")
}

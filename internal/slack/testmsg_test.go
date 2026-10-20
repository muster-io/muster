// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package slack

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/fakes/fakeslack"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/outbound"
)

// testerEnv is a Tester over the fake server's app b, on the interactive path without limits, and the source of Alert
// Group #12, firing, that its test messages render.
type testerEnv struct {
	adapterEnv
	tester *Tester
	src    *messages.Source
}

func newTesterEnv(t *testing.T) testerEnv {
	t.Helper()
	e := newAdapterEnv(t)
	c := clock.NewManual(layoutNow)
	path := deliverytest.Unlimited(1, clock.Clocks{Business: c, Real: c})
	return testerEnv{adapterEnv: e, src: layoutSource(messages.ColourFiring),
		tester: &Tester{Adapter: e.adapter, Path: path, Renderer: e.render.r, Budget: 2 * time.Second, Real: c}}
}

var testerDest = delivery.Destination{ID: destAlerts, PublicID: "DS0000000000T1", Type: delivery.TypeSlack}

func (e testerEnv) run(ctx context.Context, t *testing.T) []delivery.TestStep {
	t.Helper()
	steps, err := e.tester.Test(ctx, delivery.TestInput{Destination: testerDest, Source: e.src})
	if err != nil {
		t.Fatal(err)
	}
	return steps
}

// sentBody is the JSON body of the last request of the fake to method.
func (e testerEnv) sentBody(t *testing.T, method string) map[string]any {
	t.Helper()
	bodies := e.bodies(t, method)
	if len(bodies) == 0 {
		t.Fatalf("no request to %s", method)
	}
	return bodies[len(bodies)-1]
}

// TestTesterSendsOneMessage is C-24.FR-13, AC-16 and C-16.FR-1 to FR-3 against the fake server: one chat.postMessage,
// through the interactive class, laid out as a Root message marked "🧪 Test message", Quiet, with the buttons of its
// status signed for a test of the Destination; one step message with its request — the bot token masked — and the
// answer; nothing else is sent, and the message has no Thread.
func TestTesterSendsOneMessage(t *testing.T) {
	e := newTesterEnv(t)
	steps := e.run(t.Context(), t)
	if len(steps) != 1 || steps[0].Name != StepMessage || !steps[0].OK() || steps[0].ErrorClass != delivery.ClassNone {
		t.Fatalf("steps %+v", steps)
	}
	if got := e.paths(); !slices.Equal(got, []string{"chat.postMessage"}) {
		t.Fatalf("requests %v", got)
	}
	msgs := e.messages()
	if len(msgs) != 1 || len(msgs[0].Replies) != 0 {
		t.Fatalf("messages %+v", msgs)
	}
	m := msgs[0]
	if !strings.HasPrefix(m.Text, "🧪 Test message · 🔴 #12 DiskFull") || strings.Contains(m.Text, "<!") {
		t.Errorf("text %q", m.Text)
	}
	var top []block
	var atts []struct {
		Color  string  `json:"color"`
		Blocks []block `json:"blocks"`
	}
	if json.Unmarshal(m.Blocks, &top) != nil || json.Unmarshal(m.Attachments, &atts) != nil || len(top) != 1 ||
		len(atts) != 1 || !strings.HasPrefix(top[0].Text.Text, "🧪 Test message\n🔴 #12 DiskFull") ||
		atts[0].Color != "#d32f2f" {
		t.Fatalf("blocks %s, attachments %s", m.Blocks, m.Attachments)
	}
	var types []string
	for _, b := range atts[0].Blocks {
		types = append(types, b.Type)
	}
	if !slices.Equal(types, []string{"section", "section", "context", "actions"}) {
		t.Errorf("attachment blocks %v", types)
	}
	sent := e.sentBody(t, "chat.postMessage")
	var values []string
	for _, el := range sent["attachments"].([]any)[0].(map[string]any)["blocks"].([]any)[3].(map[string]any)["elements"].([]any) {
		switch x := el.(map[string]any); x["type"] {
		case "button":
			values = append(values, x["value"].(string))
		case "static_select":
			for _, o := range x["options"].([]any) {
				values = append(values, o.(map[string]any)["value"].(string))
			}
		}
	}
	if len(values) == 0 {
		t.Fatal("no buttons")
	}
	for _, v := range values {
		a, err := buttons.Verify(e.render.keys, v, "")
		if err != nil || a.Subject != buttons.SubjectTest || a.PublicID != testerDest.PublicID {
			t.Errorf("button %q: %+v %v", v, a, err)
		}
	}
	st := steps[0]
	if st.Request == nil || st.Request.Method != http.MethodPost ||
		st.Request.URL != e.fake.URL()+"/api/chat.postMessage" || st.Request.Body == nil ||
		!slices.Contains(st.Request.Headers, [2]string{"Authorization", "Bearer [redacted]"}) ||
		st.ResponseStatus != http.StatusOK || st.ResponseBody == nil || !strings.Contains(*st.ResponseBody, `"ts":"`+m.TS) {
		t.Errorf("step %+v %v", st.Request, st.ResponseBody)
	}
	var shown map[string]any
	if err := json.Unmarshal([]byte(*st.Request.Body), &shown); err != nil || shown["text"] != sent["text"] ||
		shown["channel"] != fakeslack.ChannelAlerts {
		t.Errorf("the request shown %s: %v", *st.Request.Body, err)
	}
	b, _ := json.Marshal(steps)
	if strings.Contains(string(b), fakeslack.SecondBotToken) {
		t.Errorf("the token shows: %s", b)
	}
}

// TestTesterPlainAfterRejection: Slack rejecting the blocks of the test message makes the Tester post it once more as
// text alone, still marked, within the budget; a rejection of the plain text too is the step's failure, with Slack's
// answer shown.
func TestTesterPlainAfterRejection(t *testing.T) {
	e := newTesterEnv(t)
	rejected := `{"ok":false,"error":"invalid_blocks","errors":["no more than 50 items allowed [json-pointer:/blocks]"]}`
	e.fault(t, "chat.postMessage", http.StatusOK, 0, rejected)
	steps := e.run(t.Context(), t)
	if len(steps) != 1 || !steps[0].OK() || !slices.Equal(e.paths(), []string{"chat.postMessage", "chat.postMessage"}) {
		t.Fatalf("steps %+v, requests %v", steps, e.paths())
	}
	plain := e.sentBody(t, "chat.postMessage")
	if len(plain["blocks"].([]any)) != 0 || len(plain["attachments"].([]any)) != 0 ||
		!strings.HasPrefix(plain["text"].(string), "🧪 Test message\n🔴 #12 DiskFull") ||
		len(plain["text"].(string)) > plainLimit || !strings.Contains(*steps[0].Request.Body, `"blocks":[]`) {
		t.Errorf("the plain post %v; shown %s", plain, *steps[0].Request.Body)
	}
	if err := e.fake.SetFault(fakeserver.Fault{Path: "/api/chat.postMessage", Status: http.StatusOK, Body: rejected,
		ContentType: "application/json", Times: 2}); err != nil {
		t.Fatal(err)
	}
	steps = e.run(t.Context(), t)
	if len(steps) != 1 || steps[0].ErrorClass != string(delivery.OutcomeUnknown) || steps[0].ResponseBody == nil ||
		!strings.Contains(*steps[0].ResponseBody, "invalid_blocks") {
		t.Errorf("rejected twice %+v", steps)
	}
}

// TestTesterFailures: a refusal is the step's class with Slack's error; a step that found no limiter token sends nothing;
// a Destination without a Slack Connection, or whose Connection cannot be read, fails its step; a failed path is an
// error of Muster's.
func TestTesterFailures(t *testing.T) {
	e := newTesterEnv(t)
	e.channel(t, false, false)
	if steps := e.run(t.Context(), t); len(steps) != 1 || steps[0].ErrorClass != string(delivery.OutcomeFatal) ||
		!strings.Contains(steps[0].Error, "not_in_channel") || steps[0].ResponseStatus != http.StatusOK ||
		!strings.Contains(*steps[0].ResponseBody, `"error":"not_in_channel"`) {
		t.Errorf("not in channel %+v", steps)
	}
	e.channel(t, true, false)
	posted := len(e.paths())
	e.tester.Path = limitedPath{}
	if steps := e.run(t.Context(), t); len(steps) != 1 || steps[0].ErrorClass != delivery.ClassLimited ||
		len(e.paths()) != posted || steps[0].Request == nil || !strings.Contains(*steps[0].Request.Body, "Test message") {
		t.Errorf("limited %+v", steps)
	}
	e.tester.Path = failedPath{}
	if _, err := e.tester.Test(t.Context(), delivery.TestInput{Destination: testerDest, Source: e.src}); err == nil {
		t.Error("a failed path")
	}
	none := testerDest
	none.ID = 99
	if steps, err := e.tester.Test(t.Context(), delivery.TestInput{Destination: none, Source: e.src}); err != nil ||
		len(steps) != 1 || steps[0].ErrorClass != string(delivery.OutcomeFatal) {
		t.Errorf("no connection %+v %v", steps, err)
	}
	e.targets.err = errors.New("db down")
	if steps := e.run(t.Context(), t); len(steps) != 1 || steps[0].ErrorClass != string(delivery.OutcomeTransient) {
		t.Errorf("unreadable %+v", steps)
	}
	p := &testPublisher{}
	if p.Update(t.Context(), delivery.Call{}, "", delivery.Message{}).Kind != delivery.OutcomeFatal ||
		p.Reply(t.Context(), delivery.Call{}, delivery.Root{}, delivery.Message{}).Kind != delivery.OutcomeFatal ||
		p.LengthLimit() != LengthLimit || (&Tester{}).budget() != delivery.InteractiveBudget || (&Tester{}).real() == nil {
		t.Error("publisher")
	}
}

type limitedPath struct{}

func (limitedPath) Do(context.Context, delivery.Subject, delivery.Op) (delivery.Outcome, error) {
	return delivery.Outcome{}, &delivery.LimitedError{RetryAfter: time.Second}
}

type failedPath struct{}

func (failedPath) Do(context.Context, delivery.Subject, delivery.Op) (delivery.Outcome, error) {
	return delivery.Outcome{}, errors.New("db down")
}

// TestTesterRequestLog is C-24.FR-14 for a test: its chat.postMessage is recorded under the check run of the test,
// with the method as its operation, the Destination linked and the bot token masked.
func TestTesterRequestLog(t *testing.T) {
	e := newTesterEnv(t)
	ctx, run := outbound.StartCheck(t.Context(), outbound.CheckTest, destAlerts)
	e.run(ctx, t)
	recs := e.rec.got()
	if len(recs) != 1 {
		t.Fatalf("records %+v", recs)
	}
	r := recs[0]
	raw, _ := json.Marshal(r)
	if r.Operation != "chat.postMessage" || r.Links.Destination != destAlerts || r.Links.Check != outbound.CheckTest ||
		r.Links.Run != run || r.Owner.Connection != adapterConnection || r.Status != http.StatusOK ||
		r.RequestHeader.Get("Authorization") != "[redacted]" || strings.Contains(string(raw), fakeslack.SecondBotToken) {
		t.Errorf("record %s", raw)
	}
}

// TestPreviewIsTheRequestBody is C-24.FR-13 and C-16.FR-4: the preview is the item message in the format json, the
// body of chat.postMessage that the Root message would be posted with — Quiet, unmarked, with the buttons a test
// message carries — and its request that call with the bot token masked; nothing is sent. A resolved Alert Group has
// no buttons; a Destination whose Connection cannot be read has no preview.
func TestPreviewIsTheRequestBody(t *testing.T) {
	e := newTesterEnv(t)
	items, err := e.tester.Preview(t.Context(), delivery.TestInput{Destination: testerDest, Source: e.src})
	if err != nil || len(items) != 1 || items[0].Name != StepMessage || items[0].Format != FormatJSON ||
		items[0].Text == nil || items[0].Request == nil || items[0].Request.Body == nil {
		t.Fatalf("preview %+v %v", items, err)
	}
	var shown, sent map[string]any
	if err := json.Unmarshal([]byte(*items[0].Text), &shown); err != nil {
		t.Fatalf("the text is not JSON: %v\n%s", err, *items[0].Text)
	}
	if err := json.Unmarshal([]byte(*items[0].Request.Body), &sent); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(shown)
	b, _ := json.Marshal(sent)
	if string(a) != string(b) || shown["channel"] != fakeslack.ChannelAlerts ||
		!strings.HasPrefix(shown["text"].(string), "🔴 #12 DiskFull") || strings.Contains(*items[0].Text, "Test message") ||
		!strings.Contains(*items[0].Text, `"action_id": "ack"`) || !strings.Contains(*items[0].Text, "&lt;!here&gt;") {
		t.Errorf("the text %s is not the request body %s", *items[0].Text, *items[0].Request.Body)
	}
	if r := items[0].Request; r.URL != e.fake.URL()+"/api/chat.postMessage" || r.Method != http.MethodPost ||
		!slices.Contains(r.Headers, [2]string{"Authorization", "Bearer [redacted]"}) ||
		strings.Contains(*r.Body+*items[0].Text, fakeslack.SecondBotToken) {
		t.Errorf("request %+v", r)
	}
	if got := e.paths(); len(got) != 0 {
		t.Errorf("a preview sent %v", got)
	}
	e.src = layoutSource(messages.ColourResolved)
	if items, err := e.tester.Preview(t.Context(), delivery.TestInput{Destination: testerDest, Source: e.src}); err != nil ||
		strings.Contains(*items[0].Text, `"actions"`) {
		t.Errorf("resolved %+v %v", items, err)
	}
	e.targets.err = errors.New("db down")
	if _, err := e.tester.Preview(t.Context(), delivery.TestInput{Destination: testerDest, Source: e.src}); err == nil {
		t.Error("no target")
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/messages"
)

// testerEnv is a Tester over the fake Bot API with Destination 1 on @muster_alerts.
type testerEnv struct {
	*adapterEnv
	tester *Tester
	keys   interface {
		buttons.Keys
	}
	src *messages.Source
}

func newTesterEnv(t *testing.T) *testerEnv {
	t.Helper()
	e := newAdapter(t)
	keys := openKeyring(t, 'b')
	r := messages.New(messages.Config{OrgID: 1, PublicURL: "http://localhost:8080", Business: e.clock,
		Real: clock.Real{}, Keys: keys, Log: logging.New(&bytes.Buffer{}, logging.LevelInfo)})
	path := deliverytest.Unlimited(1, clock.Clocks{Business: e.clock, Real: e.clock})
	return &testerEnv{adapterEnv: e, keys: keys,
		tester: &Tester{Adapter: e.adapter, Path: path, Renderer: r, Budget: 2 * time.Second},
		src: &messages.Source{Number: 7, Title: "CertExpiry", Status: messages.ColourFiring, SeverityLevel: "warning",
			TimeZone: "UTC", StartedAt: e.clock.Now(), Route: messages.RouteRef{Name: "db", Language: "en",
				SnoozeDurations: []int64{3600}}, TotalAlerts: 1,
			Alerts: []messages.SourceAlert{{Fingerprint: "f1", Labels: map[string]string{"alertname": "CertExpiry",
				"host": "<b>a</b>"}, StartsAt: e.clock.Now(), Firing: true}}}}
}

var testDest = delivery.Destination{ID: 1, PublicID: "DS0000000000T1", Type: delivery.TypeTelegram}

func (e *testerEnv) run(t *testing.T) []delivery.TestStep {
	t.Helper()
	steps, err := e.tester.Test(t.Context(), delivery.TestInput{Destination: testDest, Source: e.src})
	if err != nil {
		t.Fatal(err)
	}
	return steps
}

// TestTelegramTest is C-16.FR-1, FR-2 and AC-1 against the fake Bot API: one channel post, Quiet, marked as a test on
// its first line, with the keyboard of its status whose data name the Destination; the request shows the token
// masked in its URL, the response its status and body.
func TestTelegramTest(t *testing.T) {
	e := newTesterEnv(t)
	steps := e.run(t)
	if len(steps) != 1 || steps[0].Name != "message" || !steps[0].OK() {
		t.Fatalf("steps %+v", steps)
	}
	m := only(t, e.fake)
	if !strings.HasPrefix(m.Text, "🧪 Test message\n🔴 <b>") || !m.DisableNotification || m.ParseMode != "HTML" {
		t.Errorf("post %+v", m)
	}
	var kb inlineKeyboard
	if err := json.Unmarshal(m.ReplyMarkup, &kb); err != nil || len(kb.InlineKeyboard) == 0 {
		t.Fatalf("keyboard %s %v", m.ReplyMarkup, err)
	}
	a, err := buttons.Verify(e.keys, kb.InlineKeyboard[0][0].CallbackData, "")
	if err != nil || a.Subject != buttons.SubjectTest || a.PublicID != testDest.PublicID {
		t.Errorf("button %+v %v", a, err)
	}
	st := steps[0]
	if st.Request.URL != e.fake.URL()+"/bot[redacted]/sendMessage" || st.ResponseStatus != 200 ||
		!strings.Contains(*st.ResponseBody, fmt.Sprint(m.ID)) {
		t.Errorf("step %+v %v", st.Request, st.ResponseBody)
	}
	b, _ := json.Marshal(steps)
	if strings.Contains(string(b), testToken) {
		t.Errorf("the token shows: %s", b)
	}
}

// TestTelegramTestFailures: rejected markup is sent again as plain text within the budget; a refusal is the step's
// class; a limited step sends nothing; a Destination without a Connection fails; a failed path is an error.
func TestTelegramTestFailures(t *testing.T) {
	e := newTesterEnv(t)
	fault(t, e.fake, "sendMessage", 400,
		`{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities: unsupported start tag"}`, 1)
	if steps := e.run(t); len(steps) != 1 || !steps[0].OK() || only(t, e.fake).ParseMode != "" ||
		strings.Contains(*steps[0].Request.Body, `"parse_mode"`) {
		t.Errorf("plain %+v", steps)
	}
	fault(t, e.fake, "sendMessage", 403,
		`{"ok":false,"error_code":403,"description":"Forbidden: bot was kicked from the channel chat"}`, 1)
	if steps := e.run(t); len(steps) != 1 || steps[0].ErrorClass != "fatal" || steps[0].ResponseStatus != 403 ||
		!strings.Contains(steps[0].Error, "kicked") {
		t.Errorf("kicked %+v", steps)
	}
	e.tester.Path = limitedPath{}
	if steps := e.run(t); len(steps) != 1 || steps[0].ErrorClass != "limited" ||
		len(e.fake.Messages(faketelegram.ChannelID)) != 1 {
		t.Errorf("limited %+v", steps)
	}
	e.tester.Path = failedPath{}
	if _, err := e.tester.Test(t.Context(), delivery.TestInput{Destination: testDest, Source: e.src}); err == nil {
		t.Error("a failed path")
	}
	e.targets.err = errors.New("db down")
	if steps := e.run(t); len(steps) != 1 || steps[0].ErrorClass != "transient" {
		t.Errorf("no target %+v", steps)
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

// TestTelegramPreview is C-16.FR-4: the Root message as the channel would receive it, in HTML, unmarked, with the
// keyboard of its status; nothing is sent.
func TestTelegramPreview(t *testing.T) {
	e := newTesterEnv(t)
	items, err := e.tester.Preview(t.Context(), delivery.TestInput{Destination: testDest, Source: e.src})
	if err != nil || len(items) != 1 || items[0].Name != "message" || items[0].Format != "html" ||
		!strings.HasPrefix(*items[0].Text, "🔴 <b>") || strings.Contains(*items[0].Request.Body, testToken) ||
		!strings.Contains(*items[0].Request.Body, `"inline_keyboard"`) {
		t.Fatalf("preview %+v %v", items, err)
	}
	if n := len(e.fake.Messages(faketelegram.ChannelID)); n != 0 {
		t.Errorf("sent %d", n)
	}
	e.src.Status, e.src.Alerts[0].Firing = messages.ColourResolved, false
	if items, err := e.tester.Preview(t.Context(), delivery.TestInput{Destination: testDest, Source: e.src}); err != nil ||
		strings.Contains(*items[0].Request.Body, "inline_keyboard") {
		t.Errorf("resolved %+v %v", items, err)
	}
	e.targets.err = errors.New("db down")
	if _, err := e.tester.Preview(t.Context(), delivery.TestInput{Destination: testDest, Source: e.src}); err == nil {
		t.Error("no target")
	}
	p := &testPublisher{}
	if p.Update(t.Context(), delivery.Call{}, "", delivery.Message{}).Kind != delivery.OutcomeFatal ||
		p.Reply(t.Context(), delivery.Call{}, delivery.Root{}, delivery.Message{}).Kind != delivery.OutcomeFatal ||
		p.LengthLimit() != LengthLimit || (&Tester{}).budget() != delivery.InteractiveBudget || (&Tester{}).real() == nil {
		t.Error("publisher")
	}
}

// TestPressTestMessageTelegram is C-16.FR-3 and AC-4: a press of a test message changes nothing and is answered
// "This is a test message; nothing was changed", on the Connection's limiter only.
func TestPressTestMessageTelegram(t *testing.T) {
	e := newPressEnv(t)
	post := e.send(t, faketelegram.ChannelID, 0, e.keyboard(t, e.keys, buttons.SubjectTest, "DS0000000000T1"))
	text := e.handle(t, e.press(t, fmt.Sprintf(`{"chat":%d,"message_id":%d,"from":{"id":5001},"button":"Ack"}`,
		faketelegram.ChannelID, post)))
	if text != "This is a test message; nothing was changed" || len(e.commands.calls) != 0 ||
		e.last(t, "telegram_press")["outcome"] != "test" {
		t.Errorf("answer %q %+v %v", text, e.commands.calls, e.last(t, "telegram_press"))
	}
	if s := e.path.subjects[len(e.path.subjects)-1]; s.Destination != nil || s.Connection == nil {
		t.Errorf("subject %+v", s)
	}
}

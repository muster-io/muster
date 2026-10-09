// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package webhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/outbound"
)

// testerEnv is the Tester of the outgoing webhook 1 of adapterEnv.
type testerEnv struct {
	*adapterEnv
	tester *Tester
	in     delivery.TestInput
}

func newTesterEnv(t *testing.T) *testerEnv {
	t.Helper()
	e := newAdapterEnv(t)
	r := messages.New(messages.Config{OrgID: 1, PublicURL: "http://localhost:8080", Business: clock.NewManual(t0),
		Real: clock.Real{}, Log: logging.New(&bytes.Buffer{}, logging.LevelInfo)})
	ends := t0.Add(-time.Minute)
	src := &messages.Source{PublicID: "AGAAAAAAAAAA21", Number: 7, Title: "Disk", Summary: "Disk full",
		Status: messages.ColourFiring, SeverityLevel: "critical", TimeZone: "UTC", StartedAt: t0.Add(-time.Hour),
		Route: messages.RouteRef{PublicID: "RTAAAAAAAAAAA1", Name: "db", Language: "en"}, TotalAlerts: 2,
		Alerts: []messages.SourceAlert{
			{Fingerprint: "f1", Labels: map[string]string{"alertname": "Disk", "pod": "db-1"}, StartsAt: t0,
				GeneratorURL: "https://prometheus.example.org/graph", Firing: true},
			{Fingerprint: "f2", Labels: map[string]string{"alertname": "Disk", "pod": "db-2"}, StartsAt: t0,
				EndsAt: &ends}}}
	return &testerEnv{adapterEnv: e, tester: &Tester{Adapter: e.a, Data: r,
		Path: deliverytest.Unlimited(1, clock.Clocks{Business: clock.NewManual(t0), Real: clock.NewManual(t0)})},
		in: delivery.TestInput{Destination: delivery.Destination{ID: 1, PublicID: "DSAAAAAAAAAAA1", Type: TypeWebhook},
			Source: src, Actor: groups.Actor{Kind: audit.ActorUser, Transport: audit.TransportUI,
				Person: audit.User(5, "SRAAAAAAAAAAA5")}}}
}

func (e *testerEnv) run(t *testing.T) []delivery.TestStep {
	t.Helper()
	steps, err := e.tester.Test(t.Context(), e.in)
	if err != nil {
		t.Fatal(err)
	}
	return steps
}

// noSecrets fails when a value of the Destination shows in v.
func (e *testerEnv) noSecrets(t *testing.T, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	for _, s := range []string{"s3cr3t-token-value", string(e.signing)} {
		if strings.Contains(string(b), s) {
			t.Errorf("a secret shows: %s", b)
		}
	}
}

// TestTestEvent is C-16.FR-1, FR-2, AC-6 and C-15.FR-2: one test event — version 1, test, sequence 0, Quiet, no
// Mentions, the source as its Alert Group and Alerts, the person who started it as its actor — signed with the Signing
// secret under a new webhook-id; the result shows the request with the Secret masked in its URL and header, the
// signature headers, and the response.
func TestTestEvent(t *testing.T) {
	e := newTesterEnv(t)
	e.ep.answers = []answer{{status: 202, body: `{"ok":"s3cr3t-token-value"}`}}
	steps := e.run(t)
	if len(steps) != 1 || steps[0].Name != "event" || !steps[0].OK() || steps[0].ResponseStatus != 202 ||
		*steps[0].ResponseBody != `{"ok":"[redacted]"}` {
		t.Fatalf("steps %+v", steps)
	}
	r := e.ep.reqs[0]
	if !Verify(e.signing, r.Header.Get(HeaderID), r.Header.Get(HeaderTimestamp), e.ep.bodies[0],
		r.Header.Get(HeaderSignature)) || !strings.HasPrefix(r.Header.Get(HeaderID), "msg_") ||
		r.Header.Get("Authorization") != "Bearer s3cr3t-token-value" {
		t.Fatalf("request %v", r.Header)
	}
	var b Body
	if err := json.Unmarshal(e.ep.bodies[0], &b); err != nil || b.Version != 1 || b.Event != "test" || !b.Test ||
		b.Sequence != 0 || b.Notify || len(b.Mentions) != 0 || b.AlertGroup.Number != 7 ||
		b.AlertGroup.ID != "AGAAAAAAAAAA21" || *b.AlertGroup.Summary != "Disk full" ||
		b.AlertGroup.URL != "http://localhost:8080/alert-groups/AGAAAAAAAAAA21" || len(b.Alerts) != 2 ||
		b.Alerts[0].Status != "firing" || b.Alerts[1].Status != "resolved" || b.Alerts[1].ResolvedAt == nil ||
		*b.Alerts[0].GeneratorURL == "" || b.Actor.Kind != "user" || *b.Actor.ID != "SRAAAAAAAAAAA5" ||
		b.Actor.Transport != "ui" || !b.OccurredAt.Equal(t0) {
		t.Fatalf("body %s %v", e.ep.bodies[0], err)
	}
	if !strings.Contains(string(e.ep.bodies[0]), `"test":true`) {
		t.Errorf("test field %s", e.ep.bodies[0])
	}
	v := steps[0].Request
	if v.Method != http.MethodPost || !strings.HasSuffix(v.URL, "/hook/auto?k=[redacted]") ||
		!slices.Contains(v.Headers, [2]string{"Authorization", "Bearer [redacted]"}) ||
		!slices.Contains(v.Headers, [2]string{HeaderID, r.Header.Get(HeaderID)}) ||
		!slices.Contains(v.Headers, [2]string{HeaderSignature, r.Header.Get(HeaderSignature)}) ||
		*v.Body != string(e.ep.bodies[0]) {
		t.Errorf("view %+v", v)
	}
	e.noSecrets(t, steps)
	// A resolved source carries the last end of its Alerts as its time of resolution; the system as actor.
	e.in.Source.Status, e.in.Actor = messages.ColourResolved, groups.System
	e.run(t)
	if err := json.Unmarshal(e.ep.bodies[1], &b); err != nil || b.AlertGroup.ResolvedAt == nil ||
		b.Actor.Kind != "system" {
		t.Errorf("resolved %s", e.ep.bodies[1])
	}
}

// TestTestBoth is C-16.FR-1 and AC-3 with C-15.FR-10: in the mode both the step event comes before the step create,
// whose request shows every Secret as [redacted] and whose extracted values come from the response; nothing is stored.
func TestTestBoth(t *testing.T) {
	e := newTesterEnv(t)
	e.templateMode(chatConfig(e.url))
	e.ep.answers = []answer{{status: 200}, {status: 200, body: `{"data":{"id":"m1"}}`}}
	steps := e.run(t)
	if len(steps) != 2 || steps[0].Name != "event" || steps[1].Name != "create" || !steps[1].OK() ||
		steps[1].Extracted["id"] != "m1" {
		t.Fatalf("steps %+v", steps)
	}
	if string(e.ep.bodies[1]) != `{"text":"#7 Disk","who":}` && !strings.HasPrefix(string(e.ep.bodies[1]), `{"text":"#7 Disk"`) {
		t.Errorf("create body %s", e.ep.bodies[1])
	}
	v := steps[1].Request
	if !slices.Contains(v.Headers, [2]string{"Authorization", "Bearer [redacted]"}) ||
		!slices.Contains(v.Headers, [2]string{"Content-Type", "application/json"}) ||
		!strings.HasSuffix(v.URL, "/chat/ops/messages") {
		t.Errorf("create view %+v", v)
	}
	e.noSecrets(t, steps)
	// An extracted value that holds a Secret is masked.
	e.ep.answers = []answer{{status: 200}, {status: 200, body: `{"data":{"id":"s3cr3t-token-value"}}`}}
	if steps := e.run(t); steps[1].Extracted["id"] != "[redacted]" {
		t.Errorf("extracted %+v", steps[1].Extracted)
	}
}

// TestTestFailures is C-16.FR-2 and AC-2: a request template that fails sends nothing and is template_error; a refused
// connection is transient; an address the policy refuses is blocked; no limiter token in time is limited and sends
// nothing; a path that fails, or a Destination that cannot be read, is an error.
func TestTestFailures(t *testing.T) {
	blocked := newTesterEnv(t)
	policy, _ := outbound.ParsePolicy("strict", nil, nil)
	blocked.a.Network.Policy = outbound.StaticPolicy(policy)
	if steps := blocked.run(t); steps[0].ErrorClass != "blocked" || len(blocked.ep.reqs) != 0 {
		t.Errorf("blocked %+v", steps)
	}
	e := newTesterEnv(t)
	e.f.dests[0].events = `{"url":"http://x/{{ .Secrets.missing }}","headers":[]}`
	if steps := e.run(t); len(steps) != 1 || steps[0].ErrorClass != "template_error" || len(e.ep.reqs) != 0 {
		t.Errorf("template %+v", steps)
	}
	e.f.dests[0].events = `{"url":"http://127.0.0.1:1/x?k={{ .Secrets.token }}","headers":[]}`
	if steps := e.run(t); steps[0].ErrorClass != "transient" || steps[0].ResponseStatus != 0 {
		t.Errorf("refused %+v", steps)
	} else {
		e.noSecrets(t, steps)
	}
	e.f.dests[0].events = `{"url":"` + e.url + `/hook/auto","headers":[]}`
	e.templateMode(chatConfig(e.url))
	tc := chatConfig(e.url)
	tc.Create.URL = e.url + "/{{ .Secrets.missing }}"
	e.f.dests[0].template = string(tc.JSON())
	e.tester.Path = limited{}
	if steps := e.run(t); len(steps) != 2 || steps[0].ErrorClass != "limited" ||
		steps[1].ErrorClass != "template_error" || len(e.ep.reqs) != 0 {
		t.Errorf("limited %+v", steps)
	}
	e.tester.Path = failing{}
	if _, err := e.tester.Test(t.Context(), e.in); err == nil {
		t.Error("a failed path of the event")
	}
	e.f.dests[0].mode = ModeTemplate
	e.f.dests[0].template = string(chatConfig(e.url).JSON())
	if _, err := e.tester.Test(t.Context(), e.in); err == nil {
		t.Error("a failed path of create")
	}
	e.f.fail["GetTarget"] = errBoom
	if _, err := e.tester.Test(t.Context(), e.in); !errors.Is(err, errBoom) {
		t.Errorf("unreadable %v", err)
	}
	if _, err := e.tester.Preview(t.Context(), e.in); !errors.Is(err, errBoom) {
		t.Errorf("unreadable preview %v", err)
	}
	if (&Tester{}).budget() != delivery.InteractiveBudget {
		t.Error("budget")
	}
}

type limited struct{}

func (limited) Do(context.Context, delivery.Subject, delivery.Op) (delivery.Outcome, error) {
	return delivery.Outcome{}, &delivery.LimitedError{RetryAfter: time.Second}
}

type failing struct{}

func (failing) Do(context.Context, delivery.Subject, delivery.Op) (delivery.Outcome, error) {
	return delivery.Outcome{}, errBoom
}

// TestTesterPreview is C-16.FR-4 and C-15.FR-10: the event body of the first event in the events mode, and "create",
// "update", "open thread" and "reply in thread" in the template mode, the values extracted from responses as
// example-<name> and every Secret as [redacted]; nothing is sent. A request that fails shows its error as text.
func TestTesterPreview(t *testing.T) {
	e := newTesterEnv(t)
	e.templateMode(chatConfig(e.url))
	items, err := e.tester.Preview(t.Context(), e.in)
	if err != nil || len(items) != 5 {
		t.Fatalf("items %+v %v", items, err)
	}
	var names []string
	for _, it := range items {
		names = append(names, it.Name)
	}
	if !slices.Equal(names, []string{"event", "create", "update", "open_thread", "reply_in_thread"}) {
		t.Errorf("names %v", names)
	}
	var b Body
	if err := json.Unmarshal([]byte(*items[0].Request.Body), &b); err != nil || b.Event != "created" || b.Test ||
		b.Sequence != 1 || items[0].Format != "json" {
		t.Errorf("event %+v %v", items[0], err)
	}
	if items[2].Request.URL != e.url+"/chat/ops/messages/example-id" || items[2].Format != "json" ||
		!slices.Contains(items[4].Request.Headers, [2]string{"X-Key", "[redacted]"}) ||
		!strings.Contains(*items[4].Request.Body, "alerts_added") {
		t.Errorf("template items %+v %+v", items[2].Request, items[4].Request)
	}
	e.noSecrets(t, items)
	if len(e.ep.reqs) != 0 {
		t.Errorf("sent %d", len(e.ep.reqs))
	}
	// Failing templates show their error; a body without a JSON type is plain.
	e.f.dests[0].events = `{"url":"http://x/{{ len 3 }}","headers":[]}`
	tc := chatConfig(e.url)
	tc.Update.URL = e.url + "/{{ len 3 }}"
	tc.Create.Headers = append(tc.Create.Headers, Header{Name: "Content-Type", Value: "text/plain"})
	e.f.dests[0].template = string(tc.JSON())
	items, err = e.tester.Preview(t.Context(), e.in)
	if err != nil || items[0].Request != nil || items[0].Format != "plain" ||
		!strings.Contains(*items[0].Text, "len") || items[2].Request != nil ||
		!strings.Contains(*items[2].Text, "len") || items[1].Format != "plain" {
		t.Errorf("failures %+v %v", items, err)
	}
}

// TestRedactorJSONForm: a Secret that an answer echoes as a JSON string, escaped, is masked too.
func TestRedactorJSONForm(t *testing.T) {
	red := newRedactor([]logging.Secret{`a"b<c>\d`})
	b, _ := json.Marshal(map[string]string{"echo": `a"b<c>\d`})
	if got := red.redact(string(b)); got != `{"echo":"[redacted]"}` {
		t.Errorf("redacted %s", got)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package webhooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/templates"
	"github.com/muster-io/muster/internal/webhooks/dbgen"
)

// The Destination test of outgoing webhooks (C-16.FR-1, FR-2, C-15.FR-2, FR-10): in the events mode one `test` event —
// version 1, sequence 0, Quiet, with the source as its Alert Group — signed like every request, with a new webhook-id
// and never queued; in the template mode the "create" request rendered for the source, whose extraction rules are
// applied and shown but stored nowhere; in the mode both, the event first. Each request goes through the interactive
// path on the Destination's limiter. Requests, responses and errors are shown with every Secret, the Signing secret and
// the proxy password masked.

// EventTest is the event of a Destination test.
const EventTest = "test"

// The steps of a test and the items of a preview.
const (
	StepEvent  = "event"
	StepCreate = RequestCreate
)

// The formats of the items of a preview.
const (
	formatJSON  = "json"
	formatPlain = "plain"
)

// previewEvent is the lifecycle event the events-mode item of a preview shows: the first event of an Alert Group.
const previewEvent = groups.EventCreated

// TestData is what a test and a preview read of the renderer of messages, declared by their consumer;
// *messages.Renderer implements it: the template data of the source and the link to an Alert Group page.
type TestData interface {
	Data(src *messages.Source) templates.Data
	GroupURL(publicID string) string
}

// Tester is the Destination test and the preview of outgoing webhooks: Adapter sends, on the interactive path Path,
// each step within Budget (delivery.interactive_budget; zero is delivery.InteractiveBudget); Data renders the source;
// DB, the main pool, reads the name and login of the person who started a test, which the test event names.
type Tester struct {
	Adapter *Adapter
	Path    interface {
		Do(ctx context.Context, s delivery.Subject, op delivery.Op) (delivery.Outcome, error)
	}
	Data   TestData
	DB     dbgen.DBTX
	Budget time.Duration
}

// prepared is a request ready to send: what is sent, and what a test shows of it, masked.
type prepared struct {
	method string
	url    string
	header [][2]string
	body   []byte
	view   *delivery.TestRequest
}

// Test runs the test of the outgoing webhook of in (C-16.FR-1): the step event in the modes events and both, then the
// step create in the modes template and both.
func (t *Tester) Test(ctx context.Context, in delivery.TestInput) ([]delivery.TestStep, error) {
	tg, err := t.Adapter.Service.target(ctx, in.Destination.ID)
	if err != nil {
		return nil, fmt.Errorf("read the outgoing webhook %s: %w", in.Destination.PublicID, err)
	}
	red := newRedactor(tg.all())
	var steps []delivery.TestStep
	if tg.events != nil {
		st, err := t.event(ctx, in, tg, red)
		if err != nil {
			return nil, err
		}
		steps = append(steps, st)
	}
	if tg.template != nil {
		st, err := t.create(ctx, in, tg, red)
		if err != nil {
			return nil, err
		}
		steps = append(steps, st)
	}
	return steps, nil
}

// event sends the test event of the events mode.
func (t *Tester) event(ctx context.Context, in delivery.TestInput, tg target, red redactor) (delivery.TestStep,
	error) {
	body, err := t.body(ctx, in, EventTest, 0, true)
	if err != nil {
		return delivery.TestStep{}, err
	}
	p, err := t.eventRequest(tg, body)
	if err != nil {
		return templateStep(StepEvent, red, RequestEvents, err), nil
	}
	p.view = view(p, red)
	return t.send(ctx, in.Destination, tg, red, StepEvent, p, nil)
}

// eventRequest renders the URL and the headers of the events mode with the Destination's Secrets around body.
func (t *Tester) eventRequest(tg target, body []byte) (prepared, error) {
	u, err := t.Adapter.render(tg, "url", tg.events.URL)
	if err == nil && (strings.ContainsAny(u, "\r\n\x00") || !validURL(u)) {
		err = errors.New("url: the URL is not an absolute http or https URL")
	}
	if err != nil {
		return prepared{}, err
	}
	p := prepared{method: http.MethodPost, url: u, body: body}
	for i, h := range tg.events.Headers {
		name := "headers/" + strconv.Itoa(i)
		v, err := t.Adapter.render(tg, name, h.Value)
		if err == nil && strings.ContainsAny(v, "\r\n\x00") {
			err = errors.New(name + ": the header value contains a line break or a NUL")
		}
		if err != nil {
			return prepared{}, err
		}
		p.header = append(p.header, [2]string{h.Name, v})
	}
	p.header = append(p.header, [2]string{"Content-Type", "application/json"})
	return p, nil
}

// create sends the "create" request of the template mode for the source, Quiet, and shows the values its extraction
// rules find.
func (t *Tester) create(ctx context.Context, in delivery.TestInput, tg target, red redactor) (delivery.TestStep,
	error) {
	rt := tg.template.Create
	d := t.requestData(in, RequestCreate)
	d.Secrets = tg.secrets
	b, err := renderer{sandbox: t.Adapter.Sandbox}.request(RequestCreate, rt, d)
	if err != nil {
		return templateStep(StepCreate, red, RequestCreate, err), nil
	}
	p := prepared{method: b.method, url: b.url, header: b.header, body: b.body}
	if len(p.body) > 0 && headerValue(p.header, "Content-Type") == "" {
		p.header = append(p.header, [2]string{"Content-Type", "application/json"})
	}
	p.view = view(p, red)
	if shown, err := (renderer{sandbox: t.Adapter.Sandbox, fill: masked}).request(RequestCreate, rt,
		t.requestData(in, RequestCreate)); err == nil {
		p.view = view(prepared{method: shown.method, url: shown.url, header: shown.header, body: shown.body}, red)
		if len(shown.body) > 0 && headerValue(shown.header, "Content-Type") == "" {
			p.view.Headers = append(p.view.Headers, [2]string{"Content-Type", "application/json"})
		}
	}
	return t.send(ctx, in.Destination, tg, red, StepCreate, p, rt.Extract)
}

// send signs p and sends it once on the interactive path, within the budget, and shows the result: the request with
// its signature headers, the status and the start of the body of the answer, masked, and the values the rules extract
// from a successful answer, which are stored nowhere.
func (t *Tester) send(ctx context.Context, d delivery.Destination, tg target, red redactor, name string, p prepared,
	rules []ExtractionRule) (delivery.TestStep, error) {
	ctx, cancel := context.WithTimeout(ctx, t.budget())
	defer cancel()
	id := newRequestID()
	var res outbound.Result
	var sendErr error
	var header http.Header
	start := t.Adapter.Network.Real.Now()
	out, err := t.Path.Do(ctx, delivery.Subject{Destination: &d}, delivery.TestOp(
		func(ctx context.Context, c delivery.Call) delivery.Outcome {
			at := t.Adapter.Network.Real.Now()
			sig, err := Sign(tg.signing, id, at, p.body)
			if err != nil {
				return delivery.Outcome{Kind: delivery.OutcomeFatal, Error: outbound.Untrusted(err.Error())}
			}
			header = http.Header{}
			for _, h := range p.header {
				header.Set(h[0], h[1])
			}
			header[HeaderID] = []string{id}
			header[HeaderTimestamp] = []string{strconv.FormatInt(at.Unix(), 10)}
			header[HeaderSignature] = []string{sig}
			client, err := t.Adapter.client(d.ID, c.Class, tg)
			if err != nil {
				return delivery.Outcome{Kind: delivery.OutcomeFatal, Error: outbound.Untrusted(red.redact(err.Error()))}
			}
			res, sendErr = client.Do(ctx, outbound.Request{Method: p.method, URL: p.url, Header: header, Body: p.body,
				Mapping: Mapping})
			return outcomeOf(res, sendErr, red)
		}))
	st, err := delivery.Step(name, out, err, t.Adapter.Network.Real.Now().Sub(start))
	if err != nil {
		return delivery.TestStep{}, err
	}
	st.Request = p.view
	for _, h := range []string{HeaderID, HeaderTimestamp, HeaderSignature} {
		if v := header[h]; len(v) > 0 {
			st.Request.Headers = append(st.Request.Headers, [2]string{h, v[0]})
		}
	}
	st.ResponseStatus = res.Status
	var oe *outbound.Error
	if errors.As(sendErr, &oe) {
		st.ResponseStatus = oe.Status
		if oe.Outcome == outbound.OutcomeBlocked {
			st.ErrorClass = delivery.ClassBlocked
		}
	}
	st.ResponseBody = delivery.ResponseText(res.Body, red.redact)
	if st.OK() && len(rules) > 0 {
		values, _ := extract(res.Body, rules)
		st.Extracted = make(map[string]string, len(values))
		for k, v := range values {
			st.Extracted[k] = red.redact(v)
		}
	}
	return st, nil
}

// templateStep is the step name whose request template failed: nothing was sent.
func templateStep(name string, red redactor, request string, err error) delivery.TestStep {
	out := templateFailure(red, request, err)
	return delivery.TestStep{Name: name, ErrorClass: delivery.ErrorClass(out.Kind), Error: string(out.Error)}
}

// view is p as a test shows it: every secret masked in its URL, headers and body.
func view(p prepared, red redactor) *delivery.TestRequest {
	v := &delivery.TestRequest{Method: p.method, URL: red.redact(p.url), Headers: make([][2]string, 0, len(p.header))}
	for _, h := range p.header {
		v.Headers = append(v.Headers, [2]string{h[0], red.redact(h[1])})
	}
	if p.body != nil {
		b := red.redact(string(p.body))
		v.Body = &b
	}
	return v
}

func headerValue(header [][2]string, name string) string {
	for _, h := range header {
		if strings.EqualFold(h[0], name) {
			return h[1]
		}
	}
	return ""
}

func (t *Tester) budget() time.Duration {
	if t.Budget > 0 {
		return t.Budget
	}
	return delivery.InteractiveBudget
}

// requestData is what the request templates read for the source of in: Quiet, with the values extracted from earlier
// responses empty, which only a preview fills; a request in a Thread carries the event alerts_added.
func (t *Tester) requestData(in delivery.TestInput, request string) RequestData {
	data := t.Data.Data(in.Source)
	d := dataOf(delivery.RequestState{Language: in.Source.Route.Language, Group: &data})
	if request == RequestOpenThread || request == RequestReplyInThread {
		d.Event = string(groups.EventAlertsAdded)
	}
	return d
}

// body is the body of an events-mode request about the source of in: the event, its sequence, and test for the test
// event. A test or preview event is Quiet and mentions nobody; its actor is the person who started the test.
func (t *Tester) body(ctx context.Context, in delivery.TestInput, event string, seq int64, test bool) ([]byte, error) {
	src := in.Source
	ag := AlertGroup{Number: src.Number, ID: src.PublicID, Title: src.Title, Status: src.Status,
		Route: RouteRef{ID: src.Route.PublicID, Name: src.Route.Name}, Urgent: src.Urgent,
		SeverityLevel: src.SeverityLevel, StartedAt: src.StartedAt.UTC(), SnoozeUntil: utc(src.SnoozeUntil),
		ReopenCount: src.ReopenCount, URL: t.Data.GroupURL(src.PublicID)}
	if src.Summary != "" {
		summary := src.Summary
		ag.Summary = &summary
	}
	if src.Status == messages.ColourResolved {
		// The source names no time of resolution; the last end of its Alerts stands for it.
		for _, a := range src.Alerts {
			if a.EndsAt != nil && (ag.ResolvedAt == nil || a.EndsAt.After(*ag.ResolvedAt)) {
				ag.ResolvedAt = utc(a.EndsAt)
			}
		}
	}
	alerts := make([]Alert, 0, len(src.Alerts))
	for _, a := range src.Alerts {
		out := Alert{Fingerprint: a.Fingerprint, Status: "resolved", Labels: a.Labels, Annotations: a.Annotations,
			StartsAt: a.StartsAt.UTC()}
		if a.Firing {
			out.Status = "firing"
		} else {
			out.ResolvedAt = utc(a.EndsAt)
		}
		if a.GeneratorURL != "" {
			u := a.GeneratorURL
			out.GeneratorURL = &u
		}
		alerts = append(alerts, out)
	}
	actor := t.actor(ctx, in.Actor)
	return json.Marshal(Body{Version: BodyVersion, Event: event, Notify: false, Mentions: []MentionTarget{},
		Sequence: seq, OccurredAt: t.Adapter.Service.cfg.Business.Now().UTC(), Actor: actor, AlertGroup: ag,
		Alerts: alerts, Test: test})
}

// actor is the person who started a test as the test event names them; without the main pool, or when they cannot be
// read, by kind and id only.
func (t *Tester) actor(ctx context.Context, a groups.Actor) Actor {
	if t.DB != nil && a.Kind != "" {
		if out, err := t.Adapter.Service.actor(ctx, dbgen.New(t.DB), a); err == nil {
			return out
		}
	}
	out := Actor{Kind: string(a.Kind), Transport: string(a.Transport)}
	if out.Kind == "" || out.Kind == string(audit.ActorSystem) {
		out.Kind, out.Transport = string(audit.ActorSystem), string(audit.TransportSystem)
		return out
	}
	if a.Person.PublicID != "" {
		id := a.Person.PublicID
		out.ID = &id
	}
	return out
}

// Preview renders, without sending anything, the requests of the outgoing webhook of in for its source (C-16.FR-4,
// C-15.FR-10): in the events mode the item event, the body of the first event of an Alert Group; in the template mode
// the items create, update, open_thread and reply_in_thread of the requests it has, with the values extracted from
// responses as example-<name>. Every Secret shows as [redacted]; a request whose template fails shows its error as the
// item's text.
func (t *Tester) Preview(ctx context.Context, in delivery.TestInput) ([]delivery.PreviewItem, error) {
	tg, err := t.Adapter.Service.target(ctx, in.Destination.ID)
	if err != nil {
		return nil, fmt.Errorf("read the outgoing webhook %s: %w", in.Destination.PublicID, err)
	}
	red := newRedactor(tg.all())
	var items []delivery.PreviewItem
	if tg.events != nil {
		body, err := t.body(ctx, in, string(previewEvent), 1, false)
		if err != nil {
			return nil, err
		}
		item := delivery.PreviewItem{Name: StepEvent, Format: formatJSON}
		if p, err := t.eventRequest(tg, body); err != nil {
			text := templateStep(StepEvent, red, RequestEvents, err).Error
			item.Format, item.Text = formatPlain, &text
		} else {
			item.Request = view(p, red)
		}
		items = append(items, item)
	}
	if tg.template != nil {
		for _, r := range tg.template.all() {
			items = append(items, t.previewRequest(in, r, red))
		}
	}
	return items, nil
}

// previewRequest renders the request r for the preview, masked.
func (t *Tester) previewRequest(in delivery.TestInput, r namedRequest, red redactor) delivery.PreviewItem {
	item := delivery.PreviewItem{Name: r.name}
	b, err := renderer{sandbox: t.Adapter.Sandbox, fill: masked}.request(r.name, *r.req, t.requestData(in, r.name))
	if err != nil {
		text := templateStep(r.name, red, r.name, err).Error
		item.Format, item.Text = formatPlain, &text
		return item
	}
	item.Format = formatPlain
	if ct := headerValue(b.header, "Content-Type"); len(b.body) > 0 && (ct == "" || strings.Contains(ct, "json")) {
		item.Format = formatJSON
		if ct == "" {
			b.header = append(b.header, [2]string{"Content-Type", "application/json"})
		}
	}
	item.Request = view(prepared{method: b.method, url: b.url, header: slices.Clone(b.header), body: b.body}, red)
	return item
}

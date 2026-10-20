// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/messages"
)

// The Destination test of Slack Destinations (C-24.FR-13, C-16.FR-1 to FR-4): the step message posts the Root message
// of the source once with chat.postMessage, Quiet, marked as a test, with the buttons of its status whose action ids
// name the Destination, through the interactive path, and once more as text alone when Slack rejects its blocks. A bot
// cannot press its own button in Slack (F-094), so the test has no step press. Nothing is stored and the message has no
// Thread.

// StepMessage is the step of a test and the item of a preview that is the Root message.
const StepMessage = "message"

// FormatJSON is the format of the preview item of a Slack Destination: the body of chat.postMessage as JSON.
const FormatJSON = "json"

// redacted replaces a secret in what a test or a preview shows.
const redacted = "[redacted]"

// TestRenderer renders the Root message of a test or a preview, declared by its consumer; *messages.Renderer
// implements it.
type TestRenderer interface {
	TestRoot(src *messages.Source, markup messages.Markup, destination string) messages.Rendered
}

// Tester is the Destination test and the preview of Slack Destinations, on the interactive path Path, the test within
// Budget (delivery.interactive_budget; zero is delivery.InteractiveBudget), timed on the real clock Real.
type Tester struct {
	Adapter  *Adapter
	Path     Path
	Renderer TestRenderer
	Budget   time.Duration
	Real     clock.Clock
}

// testPublisher posts one prepared test message for the interactive path, as an adapter whose Publish only it calls,
// and keeps the request it sent and the answer, which the step shows; asked for plain text, it posts the text alone.
type testPublisher struct {
	target Target
	out    outgoing
	plain  outgoing
	sent   outgoing
	result Result
}

var _ delivery.Adapter = (*testPublisher)(nil)

// Publish posts the prepared test message in the client class of c, as text alone when c asks for it.
func (p *testPublisher) Publish(ctx context.Context, c delivery.Call, _ delivery.Message) delivery.Outcome {
	return p.send(ctx, c, c.Plain)
}

// send posts the prepared test message in the client class of c, its text alone when plain is set.
func (p *testPublisher) send(ctx context.Context, c delivery.Call, plain bool) delivery.Outcome {
	p.sent = p.out
	if plain {
		p.sent = p.plain
	}
	var answer posted
	answer, p.result = p.target.Client.postMessage(ctx, c.Class, p.sent)
	return published(p.target, p.result, answer)
}

// Update is never called: a test message is never edited.
func (p *testPublisher) Update(context.Context, delivery.Call, string, delivery.Message) delivery.Outcome {
	return delivery.Outcome{Kind: delivery.OutcomeFatal, Error: "a test message is never edited"}
}

// Reply is never called: a test message has no Thread.
func (p *testPublisher) Reply(context.Context, delivery.Call, delivery.Root, delivery.Message) delivery.Outcome {
	return delivery.Outcome{Kind: delivery.OutcomeFatal, Error: "a test message has no thread"}
}

// LengthLimit is the longest text of a section.
func (p *testPublisher) LengthLimit() int { return LengthLimit }

// plainPublisher is the publisher of a test message asked for its text alone, after Slack rejected its blocks.
type plainPublisher struct{ *testPublisher }

// Publish posts the text alone.
func (p plainPublisher) Publish(ctx context.Context, c delivery.Call, _ delivery.Message) delivery.Outcome {
	return p.send(ctx, c, true)
}

// Test runs the test of the Slack Destination of in (C-24.FR-13, C-16.FR-1): the step message, posted again as text
// alone once when Slack rejects its blocks, within the one budget. A Destination whose Connection cannot be read fails
// it.
func (t *Tester) Test(ctx context.Context, in delivery.TestInput) ([]delivery.TestStep, error) {
	d := in.Destination
	tg, fail := t.Adapter.target(ctx, delivery.Call{Destination: d})
	if fail != nil {
		st, err := delivery.Step(StepMessage, *fail, nil, 0)
		return []delivery.TestStep{st}, err
	}
	d.Connection = &tg.ConnectionID
	m := t.Renderer.TestRoot(in.Source, messages.MarkupMrkdwn, d.PublicID).Message
	l := t.Adapter.look(delivery.Call{Destination: d}, tg)
	pub := &testPublisher{target: tg, out: testRequest(m, l)}
	l.plain = true
	pub.plain, pub.sent = testRequest(m, l), pub.out
	ctx, cancel := context.WithTimeout(ctx, t.budget())
	defer cancel()
	start := t.real().Now()
	out, err := t.Path.Do(ctx, delivery.Subject{Destination: &d}, delivery.PublishOp(pub, m))
	if err == nil && out.Kind == delivery.OutcomeMarkupRejected {
		out, err = t.Path.Do(ctx, delivery.Subject{Destination: &d}, delivery.PublishOp(plainPublisher{pub}, m))
	}
	st, err := delivery.Step(StepMessage, out, err, t.real().Now().Sub(start))
	if err != nil {
		return nil, err
	}
	st.Request = postRequest(tg, pub.sent)
	st.ResponseStatus, st.ResponseBody = pub.result.Status, delivery.ResponseText(pub.result.Body, tg.Client.redact)
	return []delivery.TestStep{st}, nil
}

func (t *Tester) budget() time.Duration {
	if t.Budget > 0 {
		return t.Budget
	}
	return delivery.InteractiveBudget
}

func (t *Tester) real() clock.Clock {
	if t.Real == nil {
		return clock.Real{}
	}
	return t.Real
}

// Preview renders, without sending anything, the Root message of the source of in as the Slack Destination would
// receive it (C-24.FR-13, C-16.FR-4): the item message, the body of chat.postMessage as JSON, Quiet, with the buttons a
// test message would carry, whose presses change nothing; its request is that call with the bot token masked.
func (t *Tester) Preview(ctx context.Context, in delivery.TestInput) ([]delivery.PreviewItem, error) {
	d := in.Destination
	tg, fail := t.Adapter.target(ctx, delivery.Call{Destination: d})
	if fail != nil {
		return nil, errors.New(string(fail.Error))
	}
	m := t.Renderer.TestRoot(in.Source, messages.MarkupMrkdwn, d.PublicID).Message
	o := layout(m, t.Adapter.look(delivery.Call{Destination: d}, tg), nil)
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(o.args()); err != nil {
		return nil, err
	}
	text := tg.Client.redact(strings.TrimSuffix(b.String(), "\n"))
	return []delivery.PreviewItem{{Name: StepMessage, Format: FormatJSON, Text: &text, Request: postRequest(tg, o)}},
		nil
}

// postRequest is the chat.postMessage call of o as a test or a preview shows it: the body as the client sends it, the
// bot token masked.
func postRequest(tg Target, o outgoing) *delivery.TestRequest {
	r := &delivery.TestRequest{Method: http.MethodPost, URL: tg.Client.Base() + "chat.postMessage",
		Headers: [][2]string{{"Authorization", "Bearer " + redacted},
			{"Content-Type", "application/json; charset=utf-8"}}}
	if b, err := json.Marshal(o.args()); err == nil {
		s := tg.Client.redact(string(b))
		r.Body = &s
	}
	return r
}

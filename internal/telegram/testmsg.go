// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package telegram

import (
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

// The Destination test of Telegram Destinations (C-16.FR-1 to FR-4): the step message sends the Root message of the
// source once as a channel post, Quiet, marked as a test, with buttons whose data name the Destination, through the
// interactive path; a press of one is answered that nothing changed. Nothing is stored and the post has no Thread.

// StepMessage is the step of a test and the item of a preview that is the Root message.
const StepMessage = "message"

// redacted replaces the bot token in what a test or a preview shows.
const redacted = "[redacted]"

// TestRenderer renders the Root message of a test or a preview, declared by its consumer; *messages.Renderer
// implements it.
type TestRenderer interface {
	TestRoot(src *messages.Source, markup messages.Markup, destination string) messages.Rendered
}

// Tester is the Destination test and the preview of Telegram Destinations, on the interactive path Path, the test
// within Budget (delivery.interactive_budget; zero is delivery.InteractiveBudget), timed on the real clock Real.
type Tester struct {
	Adapter  *Adapter
	Path     Path
	Renderer TestRenderer
	Budget   time.Duration
	Real     clock.Clock
}

// testPublisher sends one prepared test message for the interactive path, as an adapter whose Publish only it calls,
// and keeps the answer, which the step shows; after the messenger rejected its markup it sends the same text plain.
type testPublisher struct {
	adapter *Adapter
	target  Target
	out     outgoing
	plain   outgoing
	result  Result
	sent    outgoing
}

var _ delivery.Adapter = (*testPublisher)(nil)

// Publish sends the prepared test message in the client class of c, plain when c asks for it.
func (p *testPublisher) Publish(ctx context.Context, c delivery.Call, _ delivery.Message) delivery.Outcome {
	return p.send(ctx, c, c.Plain)
}

// send sends the prepared test message in the client class of c, its plain text when plain is set.
func (p *testPublisher) send(ctx context.Context, c delivery.Call, plain bool) delivery.Outcome {
	p.sent = p.out
	if plain {
		p.sent = p.plain
	}
	var m sentMessage
	m, p.result = p.target.Client.sendMessage(ctx, c.Class, p.sent)
	return p.adapter.sent(p.target, c, p.result, m)
}

// Update is never called: a test message is never edited.
func (p *testPublisher) Update(context.Context, delivery.Call, string, delivery.Message) delivery.Outcome {
	return delivery.Outcome{Kind: delivery.OutcomeFatal, Error: "a test message is never edited"}
}

// Reply is never called: a test message has no Thread.
func (p *testPublisher) Reply(context.Context, delivery.Call, delivery.Root, delivery.Message) delivery.Outcome {
	return delivery.Outcome{Kind: delivery.OutcomeFatal, Error: "a test message has no thread"}
}

// LengthLimit is the longest message.
func (p *testPublisher) LengthLimit() int { return LengthLimit }

// Test runs the test of the Telegram Destination of in (C-16.FR-1): the step message, sent again as plain text once
// when Telegram rejects its markup, within the one budget. A Destination whose Connection cannot be read fails it.
func (t *Tester) Test(ctx context.Context, in delivery.TestInput) ([]delivery.TestStep, error) {
	d := in.Destination
	tg, fail := t.Adapter.target(ctx, delivery.Call{Destination: d})
	if fail != nil {
		st, err := delivery.Step(StepMessage, *fail, nil, 0)
		return []delivery.TestStep{st}, err
	}
	d.Connection = &tg.ConnectionID
	m := t.Renderer.TestRoot(in.Source, messages.MarkupHTML, d.PublicID).Message
	pub := &testPublisher{adapter: t.Adapter, target: tg, out: testMessage(tg, m, false),
		plain: testMessage(tg, m, true)}
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
	st.Request = sendRequest(tg, pub.sent)
	st.ResponseStatus, st.ResponseBody = pub.result.Status, delivery.ResponseText(pub.result.Body, tg.mask)
	return []delivery.TestStep{st}, nil
}

// plainPublisher is the publisher of a test message asked for plain text, after its markup was rejected.
type plainPublisher struct{ *testPublisher }

// Publish sends the plain text.
func (p plainPublisher) Publish(ctx context.Context, c delivery.Call, _ delivery.Message) delivery.Outcome {
	return p.send(ctx, c, true)
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

// Preview renders, without sending anything, the Root message of the source of in as the Telegram Destination would
// receive it (C-16.FR-4): the item message in HTML, its text the message and its request the sendMessage call, with
// the keyboard a test message would carry, whose presses change nothing.
func (t *Tester) Preview(ctx context.Context, in delivery.TestInput) ([]delivery.PreviewItem, error) {
	tg, fail := t.Adapter.target(ctx, delivery.Call{Destination: in.Destination})
	if fail != nil {
		return nil, errors.New(string(fail.Error))
	}
	m := t.Renderer.TestRoot(in.Source, messages.MarkupHTML, in.Destination.PublicID).Message
	out := t.Adapter.message(delivery.Call{}, writer{}, m, "")
	out.ChatID = chatRef(tg.channel())
	if len(out.ReplyMarkup.InlineKeyboard) == 0 {
		out.ReplyMarkup = nil
	}
	text := out.Text
	return []delivery.PreviewItem{{Name: StepMessage, Format: string(messages.MarkupHTML), Text: &text,
		Request: sendRequest(tg, out)}}, nil
}

// testMessage lays out the test message m for the channel of tg: the mark of a test as its first line, then the Root
// message within the length limit, Quiet, with its keyboard; plain without markup.
func testMessage(tg Target, m messages.Message, plain bool) outgoing {
	w := writer{plain: plain}
	mark := w.esc(messages.TestMark(m.Language))
	out := outgoing{Text: mark + "\n" + fit(m, w, "", LengthLimit-length(mark)-1), ReplyMarkup: keyboard(m.Buttons),
		LinkPreview: linkPreview{IsDisabled: true}, ChatID: chatRef(tg.channel()), DisableNotification: true}
	if !plain {
		out.ParseMode = "HTML"
	}
	if len(out.ReplyMarkup.InlineKeyboard) == 0 {
		out.ReplyMarkup = nil
	}
	return out
}

// sendRequest is the sendMessage call of m as a test shows it: the token in the URL masked.
func sendRequest(tg Target, m outgoing) *delivery.TestRequest {
	r := &delivery.TestRequest{Method: http.MethodPost,
		URL:     strings.TrimSuffix(tg.Client.settings.BaseURL, "/") + "/bot" + redacted + "/sendMessage",
		Headers: [][2]string{{"Content-Type", "application/json"}}}
	if b, err := json.Marshal(m); err == nil {
		s := tg.mask(string(b))
		r.Body = &s
	}
	return r
}

// mask replaces the bot token of the Target in s.
func (t Target) mask(s string) string {
	if tok := string(t.Client.settings.Token); tok != "" {
		s = strings.ReplaceAll(s, tok, redacted)
	}
	return s
}

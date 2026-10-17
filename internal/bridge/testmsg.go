// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/muster-io/muster/internal/bridge/gen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/outbound"
)

// The Destination test of Bridge Destinations (C-22.FR-17, C-16.FR-1 to FR-4): the step message posts the Root message
// of the source once with postMessage, of the kind test, Quiet, with "🧪 Test message" before its body and the buttons
// of its status whose data name the Destination, through the interactive path, under an idempotency key new for each
// test. Nothing is stored, no Timeline changes and the message has no Thread. The preview is the envelope that test
// would post, as JSON, and posts nothing.

// StepMessage is the step of a test and the item of a preview that is the test message.
const StepMessage = "message"

// FormatJSON is the format of the preview item of a Bridge Destination: the envelope as JSON.
const FormatJSON = "json"

// redacted replaces the token in what a test shows.
const redacted = "[redacted]"

// TestRenderer renders the Root message of a test or a preview, declared by its consumer; *messages.Renderer
// implements it.
type TestRenderer interface {
	TestRoot(src *messages.Source, markup messages.Markup, destination string) messages.Rendered
}

// Tester is the Destination test and the preview of Bridge Destinations, on the interactive path Path, the test within
// Budget (delivery.interactive_budget; zero is delivery.InteractiveBudget), timed on the real clock Real.
type Tester struct {
	Adapter  *Adapter
	Path     Path
	Renderer TestRenderer
	Budget   time.Duration
	Real     clock.Clock
}

// testPublisher posts one test message for the interactive path, as an adapter whose Publish only it calls, and keeps
// the envelope it posted and the answer, which the step shows.
type testPublisher struct {
	adapter *Adapter
	target  Target
	key     string
	sent    *gen.MessageInput
	answer  outbound.Result
	result  Result
}

var _ delivery.Adapter = (*testPublisher)(nil)

// Publish reads the info of the Connection and posts the test message m in the client class of c.
func (p *testPublisher) Publish(ctx context.Context, c delivery.Call, m delivery.Message) delivery.Outcome {
	i, r, fail := p.adapter.info(ctx, p.target, c)
	if fail != nil {
		p.result = r
		return *fail
	}
	in := testInput(p.target, m, i)
	p.sent = &in
	_, p.answer, p.result = p.target.Conn.Client.postMessage(ctx, c.Class, p.key, in)
	return p.result.Outcome()
}

// Update is never called: a test message is never edited.
func (p *testPublisher) Update(context.Context, delivery.Call, string, delivery.Message) delivery.Outcome {
	return delivery.Outcome{Kind: delivery.OutcomeFatal, Error: "a test message is never edited"}
}

// Reply is never called: a test message has no Thread.
func (p *testPublisher) Reply(context.Context, delivery.Call, delivery.Root, delivery.Message) delivery.Outcome {
	return delivery.Outcome{Kind: delivery.OutcomeFatal, Error: "a test message has no thread"}
}

// LengthLimit is the protocol's floor; the envelope fits itself to the Connection's limit.
func (p *testPublisher) LengthLimit() int { return MinBodyLength }

// Test runs the test of the Bridge Destination of in (C-22.FR-17): the step message, within the budget. A Destination
// whose Connection cannot be read fails it; a request the outbound address policy refused is blocked.
func (t *Tester) Test(ctx context.Context, in delivery.TestInput) ([]delivery.TestStep, error) {
	d := in.Destination
	tg, fail := t.Adapter.target(ctx, delivery.Call{Destination: d})
	if fail != nil {
		st, err := delivery.Step(StepMessage, *fail, nil, 0)
		return []delivery.TestStep{st}, err
	}
	d.Connection = &tg.Conn.ID
	m := t.Renderer.TestRoot(in.Source, messages.MarkupCommonMark, d.PublicID).Message
	pub := &testPublisher{adapter: t.Adapter, target: tg, key: testKey(d)}
	ctx, cancel := context.WithTimeout(ctx, t.budget())
	defer cancel()
	start := t.real().Now()
	out, err := t.Path.Do(ctx, delivery.Subject{Destination: &d}, delivery.PublishOp(pub, m))
	st, err := delivery.Step(StepMessage, out, err, t.real().Now().Sub(start))
	if err != nil {
		return nil, err
	}
	if pub.result.Blocked {
		st.ErrorClass = delivery.ClassBlocked
	}
	if pub.sent != nil {
		st.Request = postRequest(tg, pub.key, *pub.sent)
	}
	st.ResponseStatus = pub.answer.Status
	st.ResponseBody = delivery.ResponseText(pub.answer.Body, tg.Conn.Client.settings.mask)
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

// Preview renders, without posting anything, the test message of the source of in as the Bridge Destination would
// receive it (C-22.FR-17, C-16.FR-4): the item message, the envelope as JSON. The info of the Connection is read on
// the interactive path; without it the body has the protocol's floor as its limit and no buttons.
func (t *Tester) Preview(ctx context.Context, in delivery.TestInput) ([]delivery.PreviewItem, error) {
	d := in.Destination
	tg, fail := t.Adapter.target(ctx, delivery.Call{Destination: d})
	if fail != nil {
		return nil, errors.New(string(fail.Error))
	}
	d.Connection = &tg.Conn.ID
	var i Info
	ctx, cancel := context.WithTimeout(ctx, t.budget())
	defer cancel()
	_, _ = t.Path.Do(ctx, delivery.Subject{Destination: &d}, delivery.ReadOp(
		func(ctx context.Context, c delivery.Call) delivery.Outcome {
			read, _, fail := t.Adapter.info(ctx, tg, c)
			if fail != nil {
				return *fail
			}
			i = read
			return delivery.Outcome{Kind: delivery.OutcomeOK}
		}))
	m := t.Renderer.TestRoot(in.Source, messages.MarkupCommonMark, d.PublicID).Message
	b, err := json.MarshalIndent(testInput(tg, m, i), "", "  ")
	if err != nil {
		return nil, err
	}
	text := tg.Conn.Client.settings.mask(string(b))
	return []delivery.PreviewItem{{Name: StepMessage, Format: FormatJSON, Text: &text}}, nil
}

// testInput is the test message m posted to the channel of tg for a bridge whose info is i: of the kind test, Quiet,
// without Mentions, with the mark of a test before its body.
func testInput(tg Target, m messages.Message, i Info) gen.MessageInput {
	mark := messages.EscapeCommonMark(messages.TestMark(m.Language))
	return postInput(gen.Test, tg.ChannelID, content(m, i, []string{mark}), false)
}

// postRequest is the postMessage of in under key as a test shows it: the token masked.
func postRequest(tg Target, key string, in gen.MessageInput) *delivery.TestRequest {
	s := tg.Conn.Client.settings
	r := &delivery.TestRequest{Method: http.MethodPost, URL: strings.TrimSuffix(s.BaseURL, "/") + "/v1/messages",
		Headers: [][2]string{{"Authorization", "Bearer " + redacted}, {"Content-Type", "application/json"},
			{"Idempotency-Key", key}}}
	if b, err := json.Marshal(in); err == nil {
		body := s.mask(string(b))
		r.Body = &body
	}
	return r
}

// testKey is the idempotency key of one test message to d (C-22.FR-6): test-<Destination public_id>-<16 random bytes
// in base64url>, new for each test.
func testKey(d delivery.Destination) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	return "test-" + d.PublicID + "-" + base64.RawURLEncoding.EncodeToString(b)
}

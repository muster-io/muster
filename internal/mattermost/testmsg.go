// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package mattermost

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/messages"
)

// The Destination test of Mattermost Destinations (C-16.FR-1 to FR-4): the step message posts the Root message of the
// source once, marked as a test, with buttons whose action ids name the Destination, through the interactive path; the
// step press then presses its first button through the bot (F-054), a second request on the interactive path, and
// waits for the callback, on any replica, to report that press with the test's nonce. Nothing is stored.

// TestPressChannel is the LISTEN/NOTIFY channel on which the callback of any replica reports the bot's own press of a
// test message, with the nonce of its test as the payload.
const TestPressChannel = "muster_test_press"

// The texts of the step press: what the bot's press of the test message got when it did not reach the callback, the
// error id Mattermost answers it with then (F-022), and the error of the step, which names the most common cause.
const (
	errorActionIntegration = "api.post.do_action.action_integration.app_error"
	pressNotReached        = "The button press did not reach Muster. Add the host of %s to " +
		"ServiceSettings.AllowedUntrustedInternalConnections on the Mattermost server."
	// botUnknown fails the step press of a Connection whose bot no check has learned yet: its own press would look
	// like a person's to the callback.
	botUnknown = "Muster does not know the bot of this Connection yet. Run the Connection check, then test again."
)

// The names of the steps of a test and of the items of a preview.
const (
	StepMessage = "message"
	StepPress   = "press"
)

// redacted replaces a secret in what a test or a preview shows.
const redacted = "[redacted]"

// TestPresses tell the waiting Destination test that the bot pressed its test message, declared by their consumer, the
// callback; *PressWaits implements them.
type TestPresses interface {
	Pressed(ctx context.Context, nonce string) error
}

// PressWaits connect the bot's press of a test message, which reaches the callback of any replica, with the test that
// waits for it: the callback sends the test's nonce on TestPressChannel through Notify, and every replica's listener
// hands it to Arrived, which ends the wait of that nonce on the replica running the test.
type PressWaits struct {
	// Notify sends payload on a LISTEN/NOTIFY channel: pg_notify through the main pool.
	Notify func(ctx context.Context, channel, payload string) error

	mu    sync.Mutex
	waits map[string]chan struct{}
}

var _ TestPresses = (*PressWaits)(nil)

// Pressed reports the bot's press of the test message of nonce to every replica.
func (w *PressWaits) Pressed(ctx context.Context, nonce string) error {
	return w.Notify(ctx, TestPressChannel, nonce)
}

// Arrived ends the wait of the nonce payload, if this replica runs its test.
func (w *PressWaits) Arrived(payload string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ch, ok := w.waits[payload]; ok {
		close(ch)
		delete(w.waits, payload)
	}
}

// wait registers nonce and returns the channel its arrival closes and the function that forgets it.
func (w *PressWaits) wait(nonce string) (<-chan struct{}, func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.waits == nil {
		w.waits = map[string]chan struct{}{}
	}
	ch := make(chan struct{})
	w.waits[nonce] = ch
	return ch, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		delete(w.waits, nonce)
	}
}

// TestRenderer renders the Root message of a test or a preview, declared by its consumer; *messages.Renderer
// implements it.
type TestRenderer interface {
	TestRoot(src *messages.Source, markup messages.Markup, destination string) messages.Rendered
}

// Tester is the Destination test and the preview of Mattermost Destinations, on the interactive path Path, each step
// within Budget (delivery.interactive_budget; zero is delivery.InteractiveBudget), timed on the real clock Real.
type Tester struct {
	Adapter  *Adapter
	Path     Path
	Renderer TestRenderer
	Waits    *PressWaits
	Budget   time.Duration
	Real     clock.Clock
}

// testPublisher posts one prepared test message for the interactive path, as an adapter whose Publish only it calls,
// and keeps the answer, which the step shows.
type testPublisher struct {
	client *Client
	post   post
	answer post
	result Result
}

var _ delivery.Adapter = (*testPublisher)(nil)

// Publish posts the prepared test message in the client class of c.
func (p *testPublisher) Publish(ctx context.Context, c delivery.Call, _ delivery.Message) delivery.Outcome {
	p.answer, p.result = p.client.createPost(ctx, c.Class, p.post)
	return created(p.result, p.answer, "")
}

// Update is never called: a test message is never edited.
func (p *testPublisher) Update(context.Context, delivery.Call, string, delivery.Message) delivery.Outcome {
	return delivery.Outcome{Kind: delivery.OutcomeFatal, Error: "a test message is never edited"}
}

// Reply is never called: a test message has no Thread.
func (p *testPublisher) Reply(context.Context, delivery.Call, delivery.Root, delivery.Message) delivery.Outcome {
	return delivery.Outcome{Kind: delivery.OutcomeFatal, Error: "a test message has no thread"}
}

// LengthLimit is the server's post length limit.
func (p *testPublisher) LengthLimit() int { return LengthLimit }

// Test runs the test of the Mattermost Destination of in (C-16.FR-1, FR-3): the step message and, once it was posted
// with a button, the step press, which fails without pressing while the bot of the Connection is not known. A
// Destination whose Connection cannot be read fails the step message.
func (t *Tester) Test(ctx context.Context, in delivery.TestInput) ([]delivery.TestStep, error) {
	d := in.Destination
	tg, fail := t.Adapter.target(ctx, delivery.Call{Destination: d})
	if fail != nil {
		st, err := delivery.Step(StepMessage, *fail, nil, 0)
		return []delivery.TestStep{st}, err
	}
	d.Connection = &tg.ConnectionID
	nonce := newNonce()
	p := testPost(t.Renderer.TestRoot(in.Source, messages.MarkupMarkdown, d.PublicID).Message,
		t.Adapter.look(delivery.Call{}, tg), nonce)
	p.ChannelID = tg.ChannelID
	pub := &testPublisher{client: tg.Client, post: p}
	start := t.real().Now()
	out, err := t.do(ctx, d, delivery.PublishOp(pub, delivery.Message{}))
	st, err := delivery.Step(StepMessage, out, err, t.real().Now().Sub(start))
	if err != nil {
		return nil, err
	}
	st.Request = postRequest(tg, http.MethodPost, "/api/v4/posts", p)
	st.ResponseStatus, st.ResponseBody = pub.result.Status, delivery.ResponseText(pub.result.Body, tg.mask)
	steps := []delivery.TestStep{st}
	actions := firstActions(p)
	if !st.OK() || len(actions) == 0 || pub.answer.ID == "" {
		return steps, nil
	}
	if tg.BotUserID == "" {
		return append(steps, delivery.TestStep{Name: StepPress, ErrorClass: string(delivery.OutcomeUnknown),
			Error: botUnknown}), nil
	}
	press, err := t.press(ctx, d, tg, pub.answer.ID, actions[0].ID, nonce)
	if err != nil {
		return nil, err
	}
	return append(steps, press), nil
}

// press presses the button actionID of the test message postID through the bot and waits, within the budget, for the
// callback to report it with nonce.
func (t *Tester) press(ctx context.Context, d delivery.Destination, tg Target, postID, actionID, nonce string) (
	delivery.TestStep, error) {
	arrived, forget := t.Waits.wait(nonce)
	defer forget()
	ctx, cancel := context.WithTimeout(ctx, t.budget())
	defer cancel()
	path := "/api/v4/posts/" + postID + "/actions/" + actionID
	var r Result
	start := t.real().Now()
	out, err := t.Path.Do(ctx, delivery.Subject{Destination: &d}, delivery.TestOp(
		func(ctx context.Context, c delivery.Call) delivery.Outcome {
			r = tg.Client.doAction(ctx, c.Class, postID, actionID)
			return r.Outcome
		}))
	reached := false
	if err == nil && out.Kind == delivery.OutcomeOK {
		select {
		case <-arrived:
			reached = true
		case <-ctx.Done():
		}
	}
	st, err := delivery.Step(StepPress, out, err, t.real().Now().Sub(start))
	if err != nil {
		return delivery.TestStep{}, err
	}
	st.Request = postRequest(tg, http.MethodPost, path, struct{}{})
	st.ResponseStatus, st.ResponseBody = r.Status, delivery.ResponseText(r.Body, tg.mask)
	notReached := (out.Kind == delivery.OutcomeOK && !reached) ||
		(r.Status == http.StatusBadRequest && r.ErrorID == errorActionIntegration)
	if notReached {
		st.ErrorClass = string(delivery.OutcomeUnknown)
		st.Error = fmt.Sprintf(pressNotReached, strings.TrimSuffix(t.Adapter.IngestURL, "/"))
	}
	return st, nil
}

// do makes op on the interactive path, limited by d, within the budget.
func (t *Tester) do(ctx context.Context, d delivery.Destination, op delivery.Op) (delivery.Outcome, error) {
	ctx, cancel := context.WithTimeout(ctx, t.budget())
	defer cancel()
	return t.Path.Do(ctx, delivery.Subject{Destination: &d}, op)
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

// Preview renders, without sending anything, the Root message of the source of in as the Mattermost Destination would
// receive it (C-16.FR-4): the item message in Markdown, its text the post's message and its request the post, with the
// attachment, as it would be created, with the buttons a test message would carry, whose presses change nothing.
func (t *Tester) Preview(ctx context.Context, in delivery.TestInput) ([]delivery.PreviewItem, error) {
	tg, fail := t.Adapter.target(ctx, delivery.Call{Destination: in.Destination})
	if fail != nil {
		return nil, errors.New(string(fail.Error))
	}
	m := t.Renderer.TestRoot(in.Source, messages.MarkupMarkdown, in.Destination.PublicID).Message
	p := rootPost(m, t.Adapter.look(delivery.Call{}, tg), "")
	p.ChannelID = tg.ChannelID
	text := p.Message
	return []delivery.PreviewItem{{Name: StepMessage, Format: string(messages.MarkupMarkdown), Text: &text,
		Request: postRequest(tg, http.MethodPost, "/api/v4/posts", p)}}, nil
}

// testPost lays out the test message m: the Root message of its source, Quiet, with the mark of a test before its
// summary line and its attachment's title, and the nonce of the test in the context of its buttons.
func testPost(m messages.Message, l look, nonce string) post {
	p := rootPost(m, l, "")
	mark := messages.TestMark(m.Language)
	p.Message = cutEnd(mark+" · "+p.Message, l.limit)
	if p.Props == nil {
		return p
	}
	for i := range p.Props.Attachments {
		a := &p.Props.Attachments[i]
		if i == 0 && a.Title != "" {
			a.Title = mark + " · " + a.Title
		}
		for j := range a.Actions {
			a.Actions[j].Integration.Context.Test = nonce
		}
	}
	return p
}

// firstActions are the buttons of the first attachment of p that has any.
func firstActions(p post) []action {
	if p.Props == nil {
		return nil
	}
	for _, a := range p.Props.Attachments {
		if len(a.Actions) > 0 {
			return a.Actions
		}
	}
	return nil
}

// postRequest is a request of the bot to path of the Target's server as a test shows it: the token masked.
func postRequest(tg Target, method, path string, body any) *delivery.TestRequest {
	r := &delivery.TestRequest{Method: method,
		URL:     strings.TrimSuffix(tg.Client.settings.ServerURL, "/") + path,
		Headers: [][2]string{{"Authorization", "Bearer " + redacted}, {"Content-Type", "application/json"}}}
	if b, err := json.Marshal(body); err == nil {
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

// newNonce is the nonce of one test: 16 random bytes in base64url.
func newNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	return base64.RawURLEncoding.EncodeToString(b)
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/outbound"
)

// Interactive is the interactive path (C-11.FR-2, ADR-0005): the one way a call that a person waits for — an answer to
// a button press, an ephemeral or account-linking message, a check or a test started by a person — reaches a
// messenger. It takes a token from the same limiters as the delivery worker, polling them during its budget, and
// since a delivery without a token waits TokenMargin past the time its tokens are due, it takes the next token ahead
// of waiting deliveries. It never queues and never writes deliveries or Thread replies; it calls in the interactive
// client class. The Broken probe never uses it (S-035).
type Interactive struct {
	OrgID int64
	Store *Store
	// Clocks: the limiters follow the business clock, the budget the real clock.
	Clocks clock.Clocks
	// Budget is delivery.interactive_budget; zero is InteractiveBudget.
	Budget time.Duration
	// Sleep waits for d unless ctx ends first, and reports whether the whole wait passed; nil sleeps on the real
	// clock.
	Sleep func(ctx context.Context, d time.Duration) bool
}

// pollStep is the shortest wait between two polls of the limiters.
const pollStep = 10 * time.Millisecond

// Subject is what an interactive call is limited by: a Destination with its Connection, or a Connection alone.
type Subject struct {
	Destination *Destination
	Connection  *int64
}

// LimitedError is an interactive call that found no limiter token within the budget: nothing was sent, and a token
// is due after RetryAfter. The API answers it with 503 interactive-budget-exhausted and Retry-After, a Destination
// test records the error class limited (S-047).
type LimitedError struct {
	RetryAfter time.Duration
}

func (e *LimitedError) Error() string {
	return fmt.Sprintf("no limiter token was free within the interactive budget; retry after %s", e.RetryAfter)
}

// Seconds is the wait in whole seconds, rounded up, at least 1, for Retry-After.
func (e *LimitedError) Seconds() int {
	return max(int(math.Ceil(e.RetryAfter.Seconds())), 1)
}

// Op is one interactive call. A send or an edit is an adapter call made here, in interactive.go, so that lint 3 allows
// it nowhere else: PublishOp, UpdateOp and ReplyOp. CheckOp is an adapter's Destination check, and ReadOp a read that
// changes no message, such as a Connection check or a channel list, whose answer its function keeps.
type Op interface {
	call(ctx context.Context, c Call) Outcome
}

type readOp func(ctx context.Context, c Call) Outcome

func (f readOp) call(ctx context.Context, c Call) Outcome { return f(ctx, c) }

// ReadOp is a read that sends and edits nothing; a messenger send or edit inside read is still refused by lint 3.
func ReadOp(read func(ctx context.Context, c Call) Outcome) Op { return readOp(read) }

type checkOp struct{ checker Checker }

func (o checkOp) call(ctx context.Context, c Call) Outcome { return o.checker.Check(ctx, c) }

// CheckOp is the Destination check of an adapter (C-13.FR-10, C-14.FR-14).
func CheckOp(ch Checker) Op { return checkOp{checker: ch} }

type publishOp struct {
	adapter Adapter
	message Message
}

func (o publishOp) call(ctx context.Context, c Call) Outcome {
	return o.adapter.Publish(ctx, c, o.message)
}

// PublishOp sends m as a new message through a.
func PublishOp(a Adapter, m Message) Op { return publishOp{adapter: a, message: m} }

type webhookOp struct {
	op      Op
	webhook *WebhookCall
}

func (o webhookOp) call(ctx context.Context, c Call) Outcome {
	c.Webhook = o.webhook
	return o.op.call(ctx, c)
}

// WebhookOp is op with what a request of an outgoing webhook in the template mode reads in its call (C-15.FR-3).
func WebhookOp(op Op, w *WebhookCall) Op { return webhookOp{op: op, webhook: w} }

type updateOp struct {
	adapter   Adapter
	messageID string
	message   Message
}

func (o updateOp) call(ctx context.Context, c Call) Outcome {
	return o.adapter.Update(ctx, c, o.messageID, o.message)
}

// UpdateOp edits the message messageID to m through a.
func UpdateOp(a Adapter, messageID string, m Message) Op {
	return updateOp{adapter: a, messageID: messageID, message: m}
}

type replyOp struct {
	adapter Adapter
	root    Root
	message Message
}

func (o replyOp) call(ctx context.Context, c Call) Outcome {
	return o.adapter.Reply(ctx, c, o.root, o.message)
}

// ReplyOp sends m into the Thread of root through a.
func ReplyOp(a Adapter, root Root, m Message) Op { return replyOp{adapter: a, root: root, message: m} }

type answerOp func(ctx context.Context, c Call) Outcome

func (f answerOp) call(ctx context.Context, c Call) Outcome { return f(ctx, c) }

// AnswerOp is a private answer to the person who pressed a button, such as a Mattermost ephemeral post (C-11.FR-2): it
// changes no Root message and no Thread reply, and send is the adapter's own call.
func AnswerOp(send func(ctx context.Context, c Call) Outcome) Op { return answerOp(send) }

// Do makes op once a token of s is taken, in the interactive client class. When no token frees within the budget it
// returns a *LimitedError and calls nothing. A RetryAfter the call answers holds the bucket of its scope, as for the
// worker.
func (i *Interactive) Do(ctx context.Context, s Subject, op Op) (Outcome, error) {
	budget := i.Budget
	if budget <= 0 {
		budget = InteractiveBudget
	}
	sleep := i.Sleep
	if sleep == nil {
		sleep = sleepReal
	}
	if s.Destination == nil && s.Connection == nil {
		return Outcome{}, errors.New("an interactive call needs a destination or a connection")
	}
	sub := subject{connection: s.Connection}
	c := Call{Class: outbound.ClassInteractive}
	if s.Destination != nil {
		c.Destination = *s.Destination
		sub = subjectOf(*s.Destination)
	}
	end := i.Clocks.Real.Now().Add(budget)
	for {
		var t taken
		err := i.Store.inTx(ctx, func(q queries) error {
			var err error
			t, err = takeTokens(ctx, q, i.OrgID, sub, i.Clocks.Business.Now().UTC())
			return err
		})
		if err != nil {
			return Outcome{}, err
		}
		if t.ok {
			break
		}
		wait := max(t.due.Sub(i.Clocks.Business.Now()), 0)
		left := end.Sub(i.Clocks.Real.Now())
		if left <= 0 {
			return Outcome{}, &LimitedError{RetryAfter: wait}
		}
		if !sleep(ctx, min(max(wait, pollStep), left)) {
			return Outcome{}, ctx.Err()
		}
	}
	out := op.call(ctx, c)
	if out.Kind != OutcomeRetryAfter || (s.Destination == nil && out.Scope != ScopeConnection) {
		return out, nil
	}
	d := c.Destination
	if s.Destination == nil {
		d = Destination{Connection: s.Connection}
	}
	// The call was made: the end of ctx, such as the budget of a Destination test, must not lose its RetryAfter.
	hold := context.WithoutCancel(ctx)
	err := i.Store.inTx(hold, func(q queries) error {
		return holdBucket(hold, q, i.OrgID, d, out, i.Clocks.Business.Now().UTC().Add(out.RetryAfter))
	})
	return out, err
}

// sleepReal waits for d unless ctx ends first.
func sleepReal(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

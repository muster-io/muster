// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package deliverytest is the recording test adapter that the tests of C-11 to C-16 deliver to through the adapter
// interface (C-11, Acceptance): it records every call with its time, Destination, message and loudness, and answers
// from a script — any outcome, after a delay, with a hook that runs once it accepted the call.
package deliverytest

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/outbound"
)

// The methods a Call records.
const (
	MethodPublish = "publish"
	MethodUpdate  = "update"
	MethodReply   = "reply"
	MethodCheck   = "check"
)

// Call is one recorded call.
type Call struct {
	// At is the time of the call on the recorder's clock.
	At          time.Time
	Method      string
	Class       outbound.Class
	Destination delivery.Destination
	// MessageID is the edited message of an update and the Root message of a reply.
	MessageID string
	Root      delivery.Root
	Message   delivery.Message
	Loudness  groups.Loudness
	Mentions  []groups.Mention
	// Answered is the outcome the recorder answered with.
	Answered delivery.Outcome
}

// Answer is one scripted answer: an outcome, a delay before it, and a hook that runs after the call was accepted and
// before the outcome returns, for changes that race a call in flight.
type Answer struct {
	Outcome delivery.Outcome
	Delay   time.Duration
	Then    func()
}

// OK answers ok with a new message id.
func OK() Answer { return Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK}} }

// NotModified answers an edit with "not modified", which is ok.
func NotModified() Answer { return Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK}} }

// RetryAfter answers a RetryAfter of d scoped to scope.
func RetryAfter(d time.Duration, scope delivery.Scope) Answer {
	return Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeRetryAfter, RetryAfter: d, Scope: scope,
		Error: "Too Many Requests"}}
}

// Failure answers kind, one of the failed outcomes, with the provider text text.
func Failure(kind delivery.OutcomeKind, text string) Answer {
	return Answer{Outcome: delivery.Outcome{Kind: kind, Error: outbound.Untrusted(text)}}
}

// Recorder is the recording test adapter; its zero value answers every call ok. It is safe for concurrent use.
type Recorder struct {
	// Clock dates the calls; nil is the real clock.
	Clock clock.Clock
	// Limit is LengthLimit; zero is 4000.
	Limit int

	mu     sync.Mutex
	calls  []Call
	script map[string][]Answer
	next   int
}

var (
	_ delivery.Adapter = (*Recorder)(nil)
	_ delivery.Checker = (*Recorder)(nil)
)

// Script queues answers for the next calls of method, in order; a method without one answers ok.
func (r *Recorder) Script(method string, answers ...Answer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.script == nil {
		r.script = map[string][]Answer{}
	}
	r.script[method] = append(r.script[method], answers...)
}

// Calls are the recorded calls so far, in order.
func (r *Recorder) Calls() []Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// Count is how many calls of method were recorded.
func (r *Recorder) Count(method string) int {
	n := 0
	for _, c := range r.Calls() {
		if c.Method == method {
			n++
		}
	}
	return n
}

// Reset forgets the recorded calls and the answers not given yet.
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls, r.script = nil, nil
}

func (r *Recorder) now() time.Time {
	if r.Clock == nil {
		return time.Now().UTC()
	}
	return r.Clock.Now().UTC()
}

// answer records the call and answers it from the script.
func (r *Recorder) answer(ctx context.Context, c Call) delivery.Outcome {
	r.mu.Lock()
	c.At = r.now()
	a := OK()
	if q := r.script[c.Method]; len(q) > 0 {
		a, r.script[c.Method] = q[0], q[1:]
	}
	if a.Outcome.Kind == delivery.OutcomeOK && c.Method != MethodCheck && a.Outcome.MessageID == "" {
		switch c.Method {
		case MethodUpdate:
			a.Outcome.MessageID = c.MessageID
		default:
			r.next++
			a.Outcome.MessageID = fmt.Sprintf("m%d", r.next)
			a.Outcome.MessageURL = "https://chat.example.org/m/" + a.Outcome.MessageID
		}
	}
	c.Answered = a.Outcome
	r.calls = append(r.calls, c)
	r.mu.Unlock()
	if a.Delay > 0 {
		t := time.NewTimer(a.Delay)
		select {
		case <-ctx.Done():
		case <-t.C:
		}
		t.Stop()
	}
	if a.Then != nil {
		a.Then()
	}
	return a.Outcome
}

// Publish records a new Root message.
func (r *Recorder) Publish(ctx context.Context, c delivery.Call, m delivery.Message) delivery.Outcome {
	return r.answer(ctx, Call{Method: MethodPublish, Class: c.Class, Destination: c.Destination, Message: m,
		Loudness: c.Loudness, Mentions: c.Mentions})
}

// Update records an edit of the message messageID.
func (r *Recorder) Update(ctx context.Context, c delivery.Call, messageID string, m delivery.Message) delivery.Outcome {
	return r.answer(ctx, Call{Method: MethodUpdate, Class: c.Class, Destination: c.Destination, MessageID: messageID,
		Message: m, Loudness: c.Loudness, Mentions: c.Mentions})
}

// Reply records a Thread reply under root.
func (r *Recorder) Reply(ctx context.Context, c delivery.Call, root delivery.Root, m delivery.Message) delivery.Outcome {
	return r.answer(ctx, Call{Method: MethodReply, Class: c.Class, Destination: c.Destination,
		MessageID: root.MessageID, Root: root, Message: m, Loudness: c.Loudness, Mentions: c.Mentions})
}

// Check records a Destination check.
func (r *Recorder) Check(ctx context.Context, c delivery.Call) delivery.Outcome {
	return r.answer(ctx, Call{Method: MethodCheck, Class: c.Class, Destination: c.Destination})
}

// LengthLimit is Limit, or 4000.
func (r *Recorder) LengthLimit() int {
	if r.Limit <= 0 {
		return 4000
	}
	return r.Limit
}

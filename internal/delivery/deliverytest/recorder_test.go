// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package deliverytest

import (
	"context"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/groups"
)

// TestRecorder drives the recording adapter with every outcome: it records each call with its time, Destination,
// message and loudness, answers from the script of its method in order — after a delay, running a hook once it
// accepted the call — and answers ok with a new message id otherwise.
func TestRecorder(t *testing.T) {
	c := clock.NewManual(time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	r := &Recorder{Clock: c}
	d := delivery.Destination{ID: 1, PublicID: "DSAAAAAAAAAAA1", Type: delivery.TypeMattermost}
	call := delivery.Call{Class: "delivery", Destination: d, Loudness: groups.Loud,
		Mentions: []groups.Mention{groups.MentionNewAlertGroup}}
	msg := delivery.Message{Lines: []string{"#1 disk"}}
	hooked := false
	var answers []Answer
	for _, k := range delivery.Outcomes {
		if k == delivery.OutcomeOK {
			continue
		}
		answers = append(answers, Failure(k, "provider says "+string(k)))
	}
	answers = append(answers, RetryAfter(7*time.Second, delivery.ScopeConnection),
		Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK, MessageID: "given"}, Delay: time.Millisecond,
			Then: func() { hooked = true }})
	r.Script(MethodPublish, answers...)
	for _, want := range answers {
		got := r.Publish(t.Context(), call, msg)
		if got.Kind != want.Outcome.Kind || got.Error != want.Outcome.Error || got.RetryAfter != want.Outcome.RetryAfter ||
			got.Scope != want.Outcome.Scope {
			t.Errorf("answered %+v, want %+v", got, want.Outcome)
		}
	}
	if !hooked || r.Calls()[len(answers)-1].Answered.MessageID != "given" {
		t.Errorf("the scripted ok %+v", r.Calls()[len(answers)-1])
	}
	p := r.Publish(t.Context(), call, msg)
	if p.Kind != delivery.OutcomeOK || p.MessageID != "m1" || p.MessageURL != "https://chat.example.org/m/m1" {
		t.Errorf("default publish %+v", p)
	}
	r.Script(MethodUpdate, NotModified())
	if u := r.Update(t.Context(), call, "m1", msg); u.Kind != delivery.OutcomeOK || u.MessageID != "m1" {
		t.Errorf("update %+v", u)
	}
	if rp := r.Reply(t.Context(), call, delivery.Root{MessageID: "m1", ThreadAnchorID: "a"}, msg); rp.MessageID != "m2" {
		t.Errorf("reply %+v", rp)
	}
	r.Script(MethodCheck, Failure(delivery.OutcomeFatal, "chat not found"))
	if ch := r.Check(t.Context(), call); ch.Kind != delivery.OutcomeFatal {
		t.Errorf("check %+v", ch)
	}
	if ch := r.Check(t.Context(), call); ch.Kind != delivery.OutcomeOK || ch.MessageID != "" {
		t.Errorf("default check %+v", ch)
	}
	calls := r.Calls()
	last := calls[len(calls)-3]
	if last.Method != MethodReply || last.Root.ThreadAnchorID != "a" || last.MessageID != "m1" ||
		last.Loudness != groups.Loud || len(last.Mentions) != 1 || !last.At.Equal(c.Now()) ||
		last.Destination.PublicID != d.PublicID || last.Message.Text() != "#1 disk" || last.Class != "delivery" {
		t.Errorf("recorded %+v", last)
	}
	if r.Count(MethodPublish) != len(answers)+1 || r.Count(MethodCheck) != 2 {
		t.Errorf("counts %d %d", r.Count(MethodPublish), r.Count(MethodCheck))
	}
	if r.LengthLimit() != 4000 || (&Recorder{Limit: 10}).LengthLimit() != 10 {
		t.Error("length limit")
	}
	r.Reset()
	if len(r.Calls()) != 0 {
		t.Error("reset kept calls")
	}
	// The real clock without one; a delay cut short by the context.
	rr := &Recorder{}
	rr.Script(MethodPublish, Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK}, Delay: time.Hour})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if out := rr.Publish(ctx, call, msg); out.Kind != delivery.OutcomeOK || rr.Calls()[0].At.IsZero() {
		t.Errorf("cancelled delay %+v", out)
	}
}

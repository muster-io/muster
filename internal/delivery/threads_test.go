// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
)

// alertsAdded is the alerts_added event of new Alerts joining an Alert Group in a status.
func alertsAdded(seq int64, status groups.Status, fps ...string) groups.Recorded {
	ev := groups.Recorded{Seq: seq, Event: groups.EventAlertsAdded, Variant: groups.VariantFiring,
		Loudness: groups.Loud, Mentions: []groups.Mention{groups.MentionNewAlerts}, Fingerprints: fps}
	if status == groups.StatusAcknowledged {
		ev.Variant, ev.Loudness, ev.Mentions = groups.VariantAcknowledged, groups.Quiet, nil
	}
	return ev
}

// published is an env whose Alert Group has its Root message.
func published(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.db.dests[destMM].limit = 600
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	e.round(t)
	e.rec.Reset()
	return e
}

func (e *env) replies() []deliverytest.Call {
	var out []deliverytest.Call
	for _, c := range e.rec.Calls() {
		if c.Method == deliverytest.MethodReply {
			out = append(out, c)
		}
	}
	return out
}

// TestThreadBatching is C-11.FR-4 and FR-5: the first new Alerts reach the Thread at once and open the Thread batching
// window; those arriving within it form one reply when it closes, listing at most delivery.thread_alerts_listed and
// "…and K more — open in Muster"; Alerts that keep arriving make one reply per window; after a quiet period longer
// than the window the next ones go at once again.
func TestThreadBatching(t *testing.T) {
	e := published(t)
	g := e.group(groups.StatusFiring, "a")
	e.enqueue(t, g, groups.System, alertsAdded(2, groups.StatusFiring, "fp00"))
	e.round(t)
	if r := e.replies(); len(r) != 1 || r[0].Message.Text() != "#7 alerts_added\nfp00" ||
		r[0].Loudness != groups.Loud || !slices.Equal(r[0].Mentions, []groups.Mention{groups.MentionNewAlerts}) {
		t.Fatalf("leading reply %+v", r)
	}
	n := 1
	for step := range 5 {
		e.business.Set(business0.Add(time.Duration(10*(step+1)) * time.Second))
		var fps []string
		for range 2 + step%2*1 {
			fps = append(fps, fmt.Sprintf("fp%02d", n))
			n++
		}
		e.enqueue(t, g, groups.System, alertsAdded(int64(3+step), groups.StatusFiring, fps...))
		e.round(t)
	}
	if len(e.replies()) != 1 {
		t.Fatalf("replies within the window %d", len(e.replies()))
	}
	// 12 Alerts collected in the window: the reply lists 10 and the rest.
	for ; n < 13; n++ {
		e.enqueue(t, g, groups.System, alertsAdded(int64(10+n), groups.StatusFiring, fmt.Sprintf("fp%02d", n)))
	}
	e.business.Set(business0.Add(60 * time.Second))
	e.round(t)
	r := e.replies()
	if len(r) != 2 || len(r[1].Message.Sections) != 12 || r[1].Message.Sections[11] != "…and 2 more — open in Muster" ||
		r[1].Message.Sections[1] != "fp01" {
		t.Fatalf("batch %+v", r)
	}
	// Alerts keep arriving: the next ones wait for the next window.
	e.business.Set(business0.Add(70 * time.Second))
	e.enqueue(t, g, groups.System, alertsAdded(40, groups.StatusFiring, "fp40"))
	e.round(t)
	if len(e.replies()) != 2 {
		t.Errorf("a reply inside the next window")
	}
	e.business.Set(business0.Add(120 * time.Second))
	e.round(t)
	if r := e.replies(); len(r) != 3 || r[2].Message.Text() != "#7 alerts_added\nfp40" {
		t.Fatalf("second batch %+v", r)
	}
	// A quiet period longer than the window: at once again.
	e.business.Set(business0.Add(200 * time.Second))
	e.round(t)
	if len(e.replies()) != 3 {
		t.Errorf("an empty reply")
	}
	e.business.Set(business0.Add(250 * time.Second))
	e.enqueue(t, g, groups.System, alertsAdded(41, groups.StatusFiring, "fp41"))
	e.round(t)
	if r := e.replies(); len(r) != 4 || r[3].At != business0.Add(250*time.Second) {
		t.Errorf("after the quiet period %+v", r)
	}
}

// TestBatchLoudness: a batch takes the loudness and Mentions of the loudest event it carries.
func TestBatchLoudness(t *testing.T) {
	e := published(t)
	ack := e.group(groups.StatusAcknowledged, "a")
	e.enqueue(t, ack, groups.System, alertsAdded(2, groups.StatusAcknowledged, "fp1"))
	e.round(t)
	if r := e.replies(); len(r) != 1 || r[0].Loudness != groups.Quiet {
		t.Fatalf("leading %+v", r)
	}
	e.business.Advance(10 * time.Second)
	e.enqueue(t, ack, groups.System, alertsAdded(3, groups.StatusAcknowledged, "fp2"))
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, alertsAdded(4, groups.StatusFiring, "fp3"))
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, alertsAdded(5, groups.StatusFiring, "fp4"))
	e.business.Advance(60 * time.Second)
	e.round(t)
	r := e.replies()
	if len(r) != 2 || r[1].Loudness != groups.Loud || !slices.Equal(r[1].Mentions,
		[]groups.Mention{groups.MentionNewAlerts}) || r[1].Message.Text() != "#7 alerts_added\nfp2\nfp3\nfp4" {
		t.Errorf("batch %+v", r)
	}
	var batch *fakeReply
	for _, rp := range e.db.replies {
		if len(rp.seqs) == 3 {
			batch = rp
		}
	}
	if batch == nil || !slices.Equal(batch.seqs, []int64{3, 4, 5}) || batch.state != "sent" {
		t.Errorf("batch row %+v", batch)
	}
}

// TestRepliesInOrder: a reply waits while its delivery has no Root message, and the replies of one delivery are sent
// in id order: a reply waits while an earlier one is pending.
func TestRepliesInOrder(t *testing.T) {
	e := newEnv(t)
	e.db.dests[destMM].limit = 600
	e.rec.Script(deliverytest.MethodPublish, deliverytest.RetryAfter(5*time.Second, delivery.ScopeDestination))
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created(),
		groups.Recorded{Seq: 2, Event: groups.EventTakeover, Loudness: groups.Loud,
			Mentions: []groups.Mention{groups.MentionPreviousOwner}})
	e.round(t)
	if len(e.replies()) != 0 {
		t.Fatal("a reply before its Root message")
	}
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, groups.Recorded{Seq: 3,
		Event: groups.EventReopened, Variant: groups.VariantFiring, Loudness: groups.Loud,
		Mentions: []groups.Mention{groups.MentionReopen}})
	// The Publication goes once its RetryAfter ended; the first reply gets a RetryAfter and holds the later one back.
	e.rec.Script(deliverytest.MethodReply, deliverytest.RetryAfter(3*time.Second, delivery.ScopeDestination))
	e.business.Advance(5*time.Second + delivery.TokenMargin)
	e.round(t)
	r := e.replies()
	if e.rec.Count(deliverytest.MethodPublish) != 2 || len(r) != 1 || !strings.Contains(r[0].Message.Text(), "takeover") {
		t.Fatalf("first round %+v", e.rec.Calls())
	}
	e.business.Advance(3*time.Second + delivery.TokenMargin)
	e.round(t)
	e.business.Advance(time.Second)
	e.round(t)
	r = e.replies()
	if len(r) != 3 || !strings.Contains(r[1].Message.Text(), "takeover") ||
		!strings.Contains(r[2].Message.Text(), "reopened") || r[2].Root.MessageID != "m1" {
		t.Errorf("in order %+v", r)
	}
}

// TestReplyOutcomes: a RetryAfter holds the Destination's bucket and leaves the reply pending until then; any other
// outcome waits the first step of delivery.transient_backoff; a reply without tokens waits for them; a missing adapter
// is unknown; failed queries are logged.
func TestReplyOutcomes(t *testing.T) {
	e := published(t)
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, groups.Recorded{Seq: 2,
		Event: groups.EventTakeover, Loudness: groups.Loud})
	rp := e.db.replies[0]
	e.rec.Script(deliverytest.MethodReply, deliverytest.Failure(delivery.OutcomeThreadLost, "thread gone"))
	e.round(t)
	if rp.state != "pending" || !rp.next.Equal(e.business.Now().Add(delivery.TransientFirstStep)) ||
		*rp.lastError != "thread gone" || rp.errorClass != nil {
		t.Errorf("thread lost %+v", rp)
	}
	e.business.Set(rp.next)
	e.db.dests[destMM].limit = 1
	e.db.buckets[bucketKey{"destination", destMM}].tokens = 0
	e.round(t)
	if rp.state != "pending" || !rp.next.After(e.business.Now()) || len(e.replies()) != 1 {
		t.Errorf("without a token %+v", rp)
	}
	e.business.Set(rp.next)
	e.db.dests[destMM].limit = 600
	delete(e.w.Adapters, delivery.TypeMattermost)
	e.round(t)
	if *rp.errorClass != "unknown" || !strings.Contains(*rp.lastError, "no adapter") {
		t.Errorf("no adapter %+v", rp)
	}
	e.w.Adapters[delivery.TypeMattermost] = e.rec
	e.business.Set(rp.next)
	for _, q := range []string{"GetLeasedReply", "TakeTokens", "RescheduleReply", "RenewReplyLease", "RecordReplySent",
		"RecordReplyRetry", "HoldBucket"} {
		if q == "RescheduleReply" {
			e.db.buckets[bucketKey{"destination", destMM}] = &fakeBucket{tokens: 0, refilled: e.business.Now()}
			e.db.dests[destMM].limit = 1
		}
		if q == "RecordReplyRetry" {
			e.rec.Script(deliverytest.MethodReply, deliverytest.Failure(delivery.OutcomeTransient, "x"))
		}
		if q == "HoldBucket" {
			e.rec.Script(deliverytest.MethodReply, deliverytest.RetryAfter(time.Second, delivery.ScopeDestination))
		}
		e.db.fail[q] = errBoom
		e.log.Reset()
		rp.owner, rp.state = "", "pending"
		e.round(t)
		if !strings.Contains(e.log.String(), `"work":"thread_reply"`) {
			t.Errorf("%s: log %s", q, e.log)
		}
		delete(e.db.fail, q)
		e.db.dests[destMM].limit = 600
		e.business.Advance(time.Minute)
	}
	rp.owner, rp.state, rp.next = "", "pending", e.business.Now()
	e.rec.Script(deliverytest.MethodReply, deliverytest.RetryAfter(4*time.Second, delivery.ScopeDestination))
	e.round(t)
	if !rp.next.Equal(e.business.Now().Add(4*time.Second+delivery.TokenMargin)) || *rp.errorClass != "retry_after" {
		t.Errorf("retry after %+v", rp)
	}
	e.business.Advance(4*time.Second + delivery.TokenMargin)
	e.round(t)
	if rp.state != "sent" || rp.sentAt == nil || rp.messageID == nil {
		t.Errorf("sent %+v", rp)
	}
	if !strings.Contains(scrape(t), `kind="thread_reply",outcome="retry_after"`) {
		t.Error("thread reply attempts not counted")
	}
	// Another replica took the reply between the claim and the attempt.
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, groups.Recorded{Seq: 3,
		Event: groups.EventTakeover, Loudness: groups.Loud})
	sent := len(e.replies())
	e.db.before["GetLeasedReply"] = func() { e.db.replies[len(e.db.replies)-1].owner = "r2" }
	e.round(t)
	if len(e.replies()) != sent {
		t.Error("sent a reply of another replica")
	}
	e.begin.err = errBoom
	e.db.calls = map[string]int{}
	if _, err := e.w.Round(t.Context()); err == nil || e.db.calls["ClaimDueReplies"] != 0 {
		t.Errorf("a failed claim = %v", err)
	}
}

// customRenderer renders replies as its name, to show that the worker uses the Renderer it is given.
type customRenderer struct{ delivery.MinimalRenderer }

func (customRenderer) RenderReply(r delivery.ReplyView, _ delivery.GroupView, _ delivery.Destination) delivery.Message {
	return delivery.Message{Sections: []string{"custom " + string(r.Event)}}
}

func TestReplyRenderer(t *testing.T) {
	e := published(t)
	e.w.Renderer = customRenderer{}
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, groups.Recorded{Seq: 2,
		Event: groups.EventTakeover, Loudness: groups.Loud})
	e.round(t)
	if r := e.replies(); len(r) != 1 || r[0].Message.Text() != "custom takeover" {
		t.Errorf("replies %+v", r)
	}
}

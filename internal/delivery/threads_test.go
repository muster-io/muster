// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/outbound"
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
	if r := e.replies(); len(r) != 1 || textOf(r[0]) != "#7 alerts_added\nfp00" ||
		r[0].Loudness != groups.Loud || !slices.Equal(r[0].Mentions, []groups.Mention{groups.MentionNewAlerts}) ||
		r[0].Links != (outbound.Links{Destination: r[0].Destination.ID, AlertGroup: groupID}) {
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
	if len(r) != 2 || len(r[1].Message.Lines) != 12 || r[1].Message.Lines[11] != "…and 2 more — open in Muster" ||
		r[1].Message.Lines[1] != "fp01" {
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
	if r := e.replies(); len(r) != 3 || textOf(r[2]) != "#7 alerts_added\nfp40" {
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
		[]groups.Mention{groups.MentionNewAlerts}) || textOf(r[1]) != "#7 alerts_added\nfp2\nfp3\nfp4" {
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
	if e.rec.Count(deliverytest.MethodPublish) != 2 || len(r) != 1 || !strings.Contains(textOf(r[0]), "takeover") {
		t.Fatalf("first round %+v", e.rec.Calls())
	}
	e.business.Advance(3*time.Second + delivery.TokenMargin)
	e.round(t)
	e.business.Advance(time.Second)
	e.round(t)
	r = e.replies()
	if len(r) != 3 || !strings.Contains(textOf(r[1]), "takeover") ||
		!strings.Contains(textOf(r[2]), "reopened") || r[2].Root.MessageID != "m1" {
		t.Errorf("in order %+v", r)
	}
}

// TestReplyOutcomes: a RetryAfter holds the Destination's bucket and leaves the reply pending until then; a lost Thread
// waits the first step of delivery.transient_backoff; a reply without tokens waits for them; a missing adapter is
// unknown, so Not delivered; failed queries are logged. The other rules are in outcomes_test.go.
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

// customRenderer renders replies as its name, to show that the worker uses the Renderer it is given, or fails.
type customRenderer struct {
	stubRenderer
	err error
}

func (c customRenderer) Reply(_ context.Context, _ messages.DBTX, r delivery.ReplyView, _ delivery.GroupView) (
	delivery.Message, error) {
	return delivery.Message{Lines: []string{"custom " + string(r.Event)}}, c.err
}

func TestReplyRenderer(t *testing.T) {
	e := published(t)
	e.w.Renderer = customRenderer{}
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, groups.Recorded{Seq: 2,
		Event: groups.EventTakeover, Loudness: groups.Loud})
	e.round(t)
	if r := e.replies(); len(r) != 1 || textOf(r[0]) != "custom takeover" {
		t.Errorf("replies %+v", r)
	}
	// A reply that cannot be rendered is not sent; its lease runs out and it is tried again.
	e.w.Renderer = customRenderer{err: errBoom}
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, groups.Recorded{Seq: 3,
		Event: groups.EventTakeover, Loudness: groups.Loud})
	e.round(t)
	if r := e.replies(); len(r) != 1 || !strings.Contains(e.log.String(), "render the thread reply") {
		t.Errorf("replies %+v, log %s", r, e.log)
	}
}

// fakeMentioner resolves each symbolic Mention into everyone with its name, and records the requests.
type fakeMentioner struct {
	reqs []mentions.Request
	fail error
}

func (m *fakeMentioner) Resolve(_ context.Context, _ mentions.DBTX, org int64, r mentions.Request) (
	[]mentions.Target, error) {
	if org != orgID {
		return nil, fmt.Errorf("org %d", org)
	}
	m.reqs = append(m.reqs, r)
	out := make([]mentions.Target, len(r.Mentions))
	for i, name := range r.Mentions {
		out[i] = mentions.Target{Kind: mentions.TargetEveryone, Everyone: name}
	}
	return out, m.fail
}

// TestMentionTargets (C-12.FR-8): delivery passes the targets of a Loud message to the adapter — a Loud Publication
// without a lifecycle event, a Loud Thread reply with its event — and a Quiet message gets none; a Mention that cannot
// be resolved leaves the call for later.
func TestMentionTargets(t *testing.T) {
	e := newEnv(t)
	e.db.dests[destMM].limit = 600
	m := &fakeMentioner{}
	e.w.Mentions = m
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	e.round(t)
	calls := e.rec.Calls()
	if len(calls) != 1 || len(calls[0].Targets) != 1 || calls[0].Targets[0].Everyone != "new_alert_group" {
		t.Fatalf("publication %+v", calls)
	}
	if r := m.reqs[0]; r.DestinationID != destMM || r.DestinationType != delivery.TypeMattermost ||
		r.ConnectionID == nil || *r.ConnectionID != connID || r.AlertGroupID != groupID || r.Seq != 0 {
		t.Errorf("request %+v", r)
	}
	e.rec.Reset()
	e.enqueue(t, e.group(groups.StatusAcknowledged, "a"), groups.System, alertsAdded(2, groups.StatusAcknowledged, "f1"))
	e.round(t)
	if r := e.replies(); len(r) != 1 || r[0].Loudness != groups.Quiet || r[0].Targets != nil || len(m.reqs) != 1 {
		t.Fatalf("quiet reply %+v", r)
	}
	e.business.Advance(5 * time.Minute)
	e.rec.Reset()
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, alertsAdded(3, groups.StatusFiring, "f2"))
	e.round(t)
	if r := e.replies(); len(r) != 1 || len(r[0].Targets) != 1 || r[0].Targets[0].Everyone != "new_alerts" ||
		m.reqs[1].Seq != 3 {
		t.Fatalf("loud reply %+v %+v", r, m.reqs)
	}
	e.business.Advance(5 * time.Minute)
	e.rec.Reset()
	m.fail = errBoomMention
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, alertsAdded(4, groups.StatusFiring, "f3"))
	e.round(t)
	if r := e.replies(); len(r) != 0 || !strings.Contains(e.log.String(), "resolve the mentions") {
		t.Errorf("a failing resolution sends nothing: %+v", r)
	}
	f := newEnv(t)
	f.w.Mentions = &fakeMentioner{fail: errBoomMention}
	f.enqueue(t, f.group(groups.StatusFiring, "a"), groups.System, created())
	f.round(t)
	if calls := f.rec.Calls(); len(calls) != 0 || !strings.Contains(f.log.String(), "resolve the mentions") {
		t.Errorf("a failing resolution publishes nothing: %+v", calls)
	}
}

var errBoomMention = fmt.Errorf("no users")

// TestCopyWaitAndUnattachedChain is C-14.AC-2 and C-11.FR-16: with the copy withheld, the reply waits
// telegram.copy_wait from the Publication, then goes to the discussion group as the first link of a chain; the Thread
// is unattached with one thread_not_attached delivery event and the alert-group hint; the next reply follows the
// chain; a comment's copy attaches the replies after it.
func TestCopyWaitAndUnattachedChain(t *testing.T) {
	e := telegramEnv(t)
	d := e.publish(t)
	e.newAlerts(t, 2, "fp2")
	e.round(t)
	if len(e.replies()) != 0 {
		t.Fatal("the reply did not wait for the copy")
	}
	e.business.Set(business0.Add(delivery.CopyWait))
	e.rec.Script(deliverytest.MethodReply, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK,
		MessageID: "601"}}, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK, MessageID: "602"}})
	hints := len(e.db.hints)
	e.round(t)
	r := e.replies()
	if len(r) != 1 || r[0].Root != (delivery.Root{MessageID: tgPost}) {
		t.Fatalf("first link %+v", r)
	}
	if d.threadState != "unattached" || str(d.chainLastID) != "601" || e.threadEvents() != 1 ||
		!slices.ContainsFunc(e.db.hints[hints:], func(h db.Hint) bool { return h.ID == "AGAAAAAAAAAA21" }) {
		t.Fatalf("thread %s %s events %d hints %v", d.threadState, str(d.chainLastID), e.threadEvents(), e.db.hints)
	}
	ev := e.db.events[len(e.db.events)-1]
	if ev.Kind != "thread_not_attached" || ev.Loudness != "quiet" || ev.AlertGroupID.Int64 != groupID ||
		ev.DestinationID != destTG || ev.Error.Valid {
		t.Errorf("event %+v", ev)
	}
	states, err := e.svc.States(t.Context(), "AGAAAAAAAAAA21")
	if err != nil || len(states) != 1 || !states[0].ThreadNotAttached || states[0].State != "delivered" {
		t.Errorf("states %+v %v", states, err)
	}
	e.business.Set(business0.Add(2 * delivery.CopyWait))
	e.newAlerts(t, 3, "fp3")
	e.round(t)
	r = e.replies()
	if len(r) != 2 || r[1].Root != (delivery.Root{MessageID: tgPost, ChainLastID: "601"}) ||
		str(d.chainLastID) != "602" || e.threadEvents() != 1 {
		t.Fatalf("second link %+v, chain %s", r, str(d.chainLastID))
	}
	e.learn(t, tgCopy, delivery.LearnedFromComment)
	if d.threadState != "attached" || d.chainLastID != nil {
		t.Fatalf("after the comment %s", d.threadState)
	}
	e.business.Set(business0.Add(4 * delivery.CopyWait))
	e.newAlerts(t, 4, "fp4")
	e.round(t)
	if r = e.replies(); len(r) != 3 || r[2].Root.ThreadAnchorID != "9001" || r[2].Root.ChainLastID != "" {
		t.Fatalf("attached reply %+v", r)
	}
}

// TestChainLinkWhileCopyArrives: a chain link sent while the copy is learned leaves the attached Thread alone.
func TestChainLinkWhileCopyArrives(t *testing.T) {
	e := telegramEnv(t)
	d := e.publish(t)
	e.business.Set(business0.Add(delivery.CopyWait))
	e.newAlerts(t, 2, "fp2")
	e.rec.Script(deliverytest.MethodReply, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK,
		MessageID: "601"}, Then: func() { e.learn(t, tgCopy, delivery.LearnedFromAutomaticForward) }})
	e.round(t)
	if d.threadState != "attached" || str(d.anchorID) != "9001" || e.threadEvents() != 0 {
		t.Fatalf("thread %s %s events %d", d.threadState, str(d.anchorID), e.threadEvents())
	}
}

// TestChainLinkOfRepublishedRoot: a chain link under a Root message that was published again while it was sent
// leaves the new Root message's Thread alone.
func TestChainLinkOfRepublishedRoot(t *testing.T) {
	e := telegramEnv(t)
	d := e.publish(t)
	e.business.Set(business0.Add(delivery.CopyWait))
	e.newAlerts(t, 2, "fp2")
	e.rec.Script(deliverytest.MethodReply, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK,
		MessageID: "601"}, Then: func() {
		e.db.mu.Lock()
		d.messageID, d.threadState = at2("502"), "waiting_for_copy"
		e.db.mu.Unlock()
	}})
	e.round(t)
	if d.threadState != "waiting_for_copy" || d.chainLastID != nil || e.threadEvents() != 0 {
		t.Fatalf("thread %s %s events %d", d.threadState, str(d.chainLastID), e.threadEvents())
	}
}

func (f *fakeDB) ReminderReached(_ context.Context, arg dbgen.ReminderReachedParams) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ReminderReached"); err != nil {
		return false, err
	}
	sent := slices.ContainsFunc(f.replies, func(r *fakeReply) bool {
		return r.group == arg.AlertGroupID && r.state == "sent" && slices.Contains(r.seqs, arg.Seq)
	})
	delivered := slices.ContainsFunc(f.webhookEvents, func(e *fakeEvent) bool {
		return e.group == arg.AlertGroupID && e.seq == arg.Seq && e.state == "delivered" &&
			f.dests[e.dest].typ == delivery.TypeWebhook
	})
	return sent || delivered, nil
}

// reminder is the reminder lifecycle event seq of the Owner.
func reminder(seq int64) groups.Recorded {
	return groups.Recorded{Seq: seq, Event: groups.EventReminder, Loudness: groups.Loud,
		Mentions: []groups.Mention{groups.MentionOwner}}
}

// TestReminderReached is the check of the answer rule (C-17.FR-5, D298): a Reminder reached a Destination when a
// Thread reply carrying it was sent, or its reminder event was delivered to an events-mode outgoing webhook; one whose
// reply was dropped while its Destination was Broken, or that was never sent, reached nobody.
func TestReminderReached(t *testing.T) {
	e := published(t)
	ctx := t.Context()
	e.enqueue(t, e.group(groups.StatusAcknowledged, "a"), groups.System, reminder(5))
	e.round(t)
	reached := func(seq int64) bool {
		t.Helper()
		ok, err := e.svc.ReminderReached(ctx, nil, groupID, seq)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if len(e.replies()) != 1 || !reached(5) {
		t.Errorf("a sent reminder: replies %d, reached %v", len(e.replies()), reached(5))
	}
	if reached(6) {
		t.Error("a reminder never sent reached a destination")
	}
	e.breakDest(destMM, "unavailable")
	e.enqueue(t, e.group(groups.StatusAcknowledged, "a"), groups.System, reminder(7))
	e.round(t)
	if reached(7) {
		t.Error("a reminder of a broken destination reached it")
	}
	// Delivered only as the reminder event of an events-mode outgoing webhook.
	at := e.business.Now()
	e.db.webhookEvents = append(e.db.webhookEvents, &fakeEvent{id: 900, dest: destWH, group: groupID, seq: 8,
		event: "reminder", state: "delivered", deliveredAt: &at},
		&fakeEvent{id: 901, dest: destWH, group: groupID, seq: 9, event: "reminder", state: "pending"})
	if !reached(8) || reached(9) {
		t.Errorf("events mode: delivered %v, pending %v", reached(8), reached(9))
	}
	e.db.fail["ReminderReached"] = errBoom
	if _, err := e.svc.ReminderReached(ctx, nil, groupID, 8); !errors.Is(err, errBoom) {
		t.Errorf("failing = %v", err)
	}
}

// noticeRenderer renders the ack timeout notice as a failing Route template does: it reports the failure and leaves
// its counter to run once the transaction committed, and records what the worker handed it.
type noticeRenderer struct {
	stubRenderer
	views *[]delivery.ReplyView
	ran   *[]int
	begin *fakeBeginner
}

func (n noticeRenderer) Reply(_ context.Context, _ messages.DBTX, r delivery.ReplyView, _ delivery.GroupView) (
	delivery.Message, error) {
	*n.views = append(*n.views, r)
	r.Fallback("ack_timeout_notice template failed: line 1: boom")
	r.After(func(context.Context) { *n.ran = append(*n.ran, n.begin.open) })
	return delivery.Message{Lines: []string{"notice"}}, nil
}

// TestAckTimeoutNoticeReply is C-12.FR-4 and FR-6 for the ack timeout notice: the worker renders it for the markup of
// its Destination, records a failing Route template in the Timeline as fallback_template_used in the transaction that
// prepared it, and runs what the render left once that transaction committed, before the call.
func TestAckTimeoutNoticeReply(t *testing.T) {
	e := published(t)
	var views []delivery.ReplyView
	var ran []int
	e.w.Renderer = noticeRenderer{views: &views, ran: &ran, begin: e.begin}
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, groups.Recorded{Seq: 2,
		Event: groups.EventAckTimeout, Loudness: groups.Loud, Mentions: []groups.Mention{groups.MentionAckTimeout}})
	e.round(t)
	if len(views) != 1 || views[0].Markup != messages.MarkupMarkdown || len(e.replies()) != 1 {
		t.Fatalf("views %+v, replies %d", views, len(e.replies()))
	}
	if len(ran) != 1 || ran[0] != 0 {
		t.Errorf("after ran %v, want once with no transaction open", ran)
	}
	last := e.db.system[len(e.db.system)-1]
	if last.System != groups.SystemFallbackTemplateUsed || last.AlertGroupID != groupID ||
		!strings.Contains(last.Detail, "ack_timeout_notice template failed") {
		t.Errorf("system entry %+v", last)
	}
	// A failure to record it leaves the reply for later.
	e.db.fail["RecordSystemEntry"] = errBoom
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, groups.Recorded{Seq: 3,
		Event: groups.EventAckTimeout, Loudness: groups.Loud, Mentions: []groups.Mention{groups.MentionAckTimeout}})
	e.round(t)
	if len(e.replies()) != 1 || len(ran) != 1 {
		t.Errorf("recording failed: replies %d, after ran %v", len(e.replies()), ran)
	}
}

// TestReplyRecordLocksItsDeliveryFirst: the record of a Thread reply's outcome locks the reply's delivery before it
// writes the reply (the lock order of worker.go); a failure of that lock fails the record, and the reply is attempted
// again.
func TestReplyRecordLocksItsDeliveryFirst(t *testing.T) {
	e := published(t)
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, alertsAdded(2, groups.StatusFiring, "fp"))
	e.db.replyLocks = nil
	e.round(t)
	if len(e.replies()) != 1 || !slices.Equal(e.db.replyLocks, []string{"reply", "delivery", "sent"}) {
		t.Fatalf("replies %d, locks %v", len(e.replies()), e.db.replyLocks)
	}
	e = published(t)
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, alertsAdded(2, groups.StatusFiring, "fp"))
	e.db.fail["LockReplyDelivery"] = errBoom
	e.round(t)
	rp := e.db.replies[len(e.db.replies)-1]
	if rp.state == "sent" || len(e.replies()) != 1 ||
		!strings.Contains(e.log.String(), "lock the delivery of the thread reply") {
		t.Errorf("reply %s, %d sent\n%s", rp.state, len(e.replies()), e.log)
	}
	delete(e.db.fail, "LockReplyDelivery")
	rp.owner, rp.next = "", e.business.Now()
	e.round(t)
	if rp.state != "sent" {
		t.Errorf("again: reply %s", rp.state)
	}
}

// takeover queues a Takeover reply of the Alert Group of the fixtures, which goes at once.
func (e *env) takeover(t *testing.T, seq int64) {
	t.Helper()
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, groups.Recorded{Seq: seq,
		Event: groups.EventTakeover, Loudness: groups.Loud})
}

// TestBridgeThreadRef is C-22.FR-6 and AC-4 through the recorder: the first Thread reply of a Bridge goes with the
// thread_ref its post returned — none here, null — then each reply's returned thread_ref is stored as the delivery's
// Thread anchor and sent with the next reply; a reply that returns none keeps the stored one; a Root message published
// again while a reply was in flight keeps its new Thread. A post that returns a thread_ref sends it with the first
// reply.
func TestBridgeThreadRef(t *testing.T) {
	e, d, _ := publishedBridge(t, deliverytest.OK())
	if d.anchorID != nil {
		t.Fatalf("a post without a thread_ref anchored %+v", d)
	}
	ref := func(r string) deliverytest.Answer {
		return deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK, ThreadRef: r}}
	}
	e.rec.Script(deliverytest.MethodReply, ref("t-1"), ref("t-2"), deliverytest.OK(), ref("t-4"))
	for seq := int64(2); seq <= 5; seq++ {
		e.takeover(t, seq)
		e.round(t)
	}
	var sent []string
	for _, r := range e.replies() {
		sent = append(sent, r.Root.ThreadAnchorID)
		if r.Root.MessageID != "m1" || r.Root.ChainLastID != "" {
			t.Errorf("reply %+v", r)
		}
	}
	if !slices.Equal(sent, []string{"", "t-1", "t-2", "t-2"}) || d.anchorID == nil || *d.anchorID != "t-4" {
		t.Errorf("sent %q, anchor %v", sent, d.anchorID)
	}
	// The Root message was published again while the reply was in flight: its new Thread is left alone.
	e.rec.Script(deliverytest.MethodReply, deliverytest.Answer{
		Outcome: delivery.Outcome{Kind: delivery.OutcomeOK, ThreadRef: "t-old"},
		Then:    func() { d.messageID, d.anchorID = at2("m9"), nil }})
	e.takeover(t, 6)
	e.round(t)
	if d.anchorID != nil {
		t.Errorf("the old thread_ref anchored the new root message: %q", *d.anchorID)
	}

	e, d, _ = publishedBridge(t, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK,
		MessageID: "m-1", ThreadRef: "t-m-1"}})
	e.takeover(t, 2)
	e.round(t)
	if r := e.replies(); len(r) != 1 || r[0].Root.ThreadAnchorID != "t-m-1" || *d.anchorID != "t-m-1" {
		t.Errorf("replies %+v", r)
	}
	// A failed record of the thread_ref is logged; the reply waits for its lease.
	e.rec.Script(deliverytest.MethodReply, ref("t-2"))
	e.db.fail["RecordThreadRef"] = errBoom
	e.takeover(t, 3)
	e.round(t)
	if !strings.Contains(e.log.String(), `"error":"record the thread_ref of the thread reply: boom"`) {
		t.Errorf("log %s", e.log)
	}
}

// TestBridgeReplyKey is C-22.FR-6 and AC-4: each Thread reply to a Bridge carries its own key,
// reply-<Destination public_id>-<thread reply id>, the same for every retry of it.
func TestBridgeReplyKey(t *testing.T) {
	e, _, idem := publishedBridge(t, deliverytest.OK())
	e.rec.Script(deliverytest.MethodReply, timedOut())
	e.takeover(t, 2)
	e.round(t)
	e.business.Set(e.db.replies[0].next)
	e.round(t)
	e.takeover(t, 3)
	e.round(t)
	r1 := fmt.Sprintf("reply-%s-%d", bridgePublicID, e.db.replies[0].id)
	r2 := fmt.Sprintf("reply-%s-%d", bridgePublicID, e.db.replies[1].id)
	if !slices.Equal(idem.Keys(), []string{r1, r1, r2}) || r1 == r2 || e.db.replies[1].state != "sent" {
		t.Errorf("keys %v", idem.Keys())
	}
}

// TestBridgeReplyGone is C-22.FR-8 and C-11.FR-13 for a Bridge: gone on a Thread reply starts the deleted Root message
// flow once — the Root message published again, Quietly, its Thread starting over from the new post's thread_ref — and
// gone again marks the pair deleted in the messenger.
func TestBridgeReplyGone(t *testing.T) {
	e, d, _ := publishedBridge(t, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK,
		MessageID: "m-1", ThreadRef: "t-m-1"}})
	e.rec.Script(deliverytest.MethodReply, deliverytest.Failure(delivery.OutcomeGone, "The message no longer exists."))
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK,
		MessageID: "m-2", ThreadRef: "t-m-2"}})
	e.takeover(t, 2)
	e.round(t)
	if !d.republished || d.anchorID != nil || d.messageID != nil {
		t.Fatalf("after gone %+v", d)
	}
	e.round(t)
	cs := e.rec.Calls()
	if callMethods(cs) != "reply,publish,reply" || cs[1].Loudness != groups.Quiet || cs[2].Root.MessageID != "m-2" ||
		cs[2].Root.ThreadAnchorID != "t-m-2" {
		t.Fatalf("calls %s %+v", callMethods(cs), cs)
	}
	if n := strings.Count(strings.Join(e.eventKinds(), ","), "republished"); n != 1 {
		t.Errorf("republished %d times: %v", n, e.eventKinds())
	}
	e.rec.Script(deliverytest.MethodReply, deliverytest.Failure(delivery.OutcomeGone, "The message no longer exists."))
	e.takeover(t, 3)
	e.round(t)
	if d.state != "deleted_in_messenger" || e.rec.Count(deliverytest.MethodPublish) != 1 {
		t.Errorf("gone again %+v", d)
	}
}

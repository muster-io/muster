// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/mentions"
)

// The people of the tests: bob, whom the entry "On-call of the Team from the label" names without an Account link,
// and alice, whom a person entry names with one.
var (
	onCallEntry = mentions.EntryRef{ID: 71, Position: 0,
		Target: mentions.EntryTarget{Kind: mentions.TargetOnCallOfTeamsFromLabel}}
	aliceEntry = mentions.EntryRef{ID: 72, Position: 1,
		Target: mentions.EntryTarget{Kind: mentions.TargetUser, UserID: "SRAAAAAAAAAAA1"}}
	bobUndelivered = mentions.Undelivered{UserID: 2, Entry: onCallEntry, Event: mentions.KindNewAlertGroup,
		Reason: mentions.ReasonNoAccountLink}
	aliceMentioned = mentions.Mentioned{UserID: 1, Entry: aliceEntry, Event: mentions.KindNewAlertGroup}
	bobMentioned   = mentions.Mentioned{UserID: 2, Entry: onCallEntry, Event: mentions.KindNewAlerts}
)

// mentionEvents are the mention events recorded, in order.
func (e *env) mentionEvents() []dbgen.InsertDeliveryEventParams {
	var out []dbgen.InsertDeliveryEventParams
	for _, ev := range e.db.events {
		if ev.Kind == string(delivery.EventMentionNotDelivered) || ev.Kind == string(delivery.EventMentionDelivered) {
			out = append(out, ev)
		}
	}
	return out
}

// detailOf is the detail of a mention event.
func detailOf(t *testing.T, ev dbgen.InsertDeliveryEventParams) delivery.MentionDetail {
	t.Helper()
	var d delivery.MentionDetail
	if err := json.Unmarshal(ev.Detail, &d); err != nil {
		t.Fatalf("detail %s: %v", ev.Detail, err)
	}
	return d
}

// TestMentionEvents is C-28.FR-3, AC-4 and C-11.FR-21 revised for L2: once a Loud Publication is delivered, its
// delivery records one mention_not_delivered per undelivered person and entry, after the publication, Quiet, without
// Mentions, with the person, the entry's id, position and target, the event and the reason, and nothing is posted for
// it; a mentioned person without an open warning records nothing. A later Loud Thread reply that mentions the person
// through that entry records mention_delivered once, which closes the warning, and a Quiet reply records nothing.
func TestMentionEvents(t *testing.T) {
	e := newEnv(t)
	e.db.dests[destMM].limit = 600
	m := &fakeMentioner{undelivered: []mentions.Undelivered{bobUndelivered},
		mentioned: []mentions.Mentioned{aliceMentioned}}
	e.w.Mentions = m
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	e.round(t)
	if kinds := e.eventKinds(); !slices.Equal(kinds, []string{"publication", "mention_not_delivered"}) {
		t.Fatalf("events %v", kinds)
	}
	if calls := e.rec.Calls(); len(calls) != 1 || calls[0].Method != deliverytest.MethodPublish {
		t.Errorf("calls %+v: an undelivered mention posts nothing", calls)
	}
	ev := e.mentionEvents()[0]
	want := delivery.MentionDetail{UserID: 2, EntryID: 71, Position: 0, Target: onCallEntry.Target,
		Event: mentions.KindNewAlertGroup, Reason: mentions.ReasonNoAccountLink}
	if d := detailOf(t, ev); d != want || ev.DestinationID != destMM || ev.AlertGroupID.Int64 != groupID ||
		ev.StormID.Valid || ev.Loudness != "quiet" || len(ev.Mentions) != 0 || ev.ErrorClass.Valid {
		t.Errorf("mention_not_delivered %+v %+v", ev, detailOf(t, ev))
	}

	// A Quiet reply resolves nothing and records nothing.
	e.rec.Reset()
	m.undelivered, m.mentioned = nil, []mentions.Mentioned{bobMentioned}
	e.enqueue(t, e.group(groups.StatusAcknowledged, "a"), groups.System, alertsAdded(2, groups.StatusAcknowledged, "f1"))
	e.round(t)
	if r := e.replies(); len(r) != 1 || len(e.mentionEvents()) != 1 {
		t.Fatalf("quiet reply %+v, mention events %d", r, len(e.mentionEvents()))
	}
	// A Loud reply that mentions bob through the entry closes his warning, once.
	for seq := int64(3); seq <= 4; seq++ {
		e.business.Advance(5 * time.Minute)
		e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, alertsAdded(seq, groups.StatusFiring, "f2"))
		e.round(t)
	}
	got := e.mentionEvents()
	if len(got) != 2 || got[1].Kind != "mention_delivered" {
		t.Fatalf("after two loud replies %v", e.eventKinds())
	}
	if d := detailOf(t, got[1]); d != (delivery.MentionDetail{UserID: 2, EntryID: 71, Target: onCallEntry.Target,
		Event: mentions.KindNewAlerts}) || got[1].AlertGroupID.Int64 != groupID {
		t.Errorf("mention_delivered %+v", d)
	}
}

// TestMentionEventsLeftOut is C-28.FR-3 and C-12.FR-11 for a Mention line longer than a bridge's body allows: a
// person whose Mention the delivered message left out for its length is not delivered, with the reason
// message_too_long, after those the resolution found undelivered; a later Loud reply that leaves out a person with an
// open warning records no mention_delivered, so the warning stays open, and one that mentions them closes it.
func TestMentionEventsLeftOut(t *testing.T) {
	e := newEnv(t)
	e.db.dests[destMM].limit = 600
	alice, bob := aliceMentioned, bobMentioned
	alice.PublicID, bob.PublicID = "SRAAAAAAAAAAA1", "SRAAAAAAAAAAA2"
	m := &fakeMentioner{undelivered: []mentions.Undelivered{bobUndelivered}, mentioned: []mentions.Mentioned{alice}}
	e.w.Mentions = m
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK,
		LeftOut: []string{alice.PublicID}}})
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	e.round(t)
	if kinds := e.eventKinds(); !slices.Equal(kinds,
		[]string{"publication", "mention_not_delivered", "mention_not_delivered"}) {
		t.Fatalf("events %v", kinds)
	}
	got := e.mentionEvents()
	if d := detailOf(t, got[1]); d != (delivery.MentionDetail{UserID: 1, EntryID: 72, Position: 1,
		Target: aliceEntry.Target, Event: mentions.KindNewAlertGroup, Reason: mentions.ReasonMessageTooLong}) ||
		got[1].Loudness != "quiet" {
		t.Errorf("the left-out mention %+v", d)
	}

	// bob's warning is open: a Loud reply whose Mention line leaves him out keeps it open.
	m.undelivered, m.mentioned = nil, []mentions.Mentioned{bob}
	e.rec.Script(deliverytest.MethodReply, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK,
		LeftOut: []string{bob.PublicID}}})
	e.business.Advance(5 * time.Minute)
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, alertsAdded(2, groups.StatusFiring, "f1"))
	e.round(t)
	got = e.mentionEvents()
	if len(got) != 3 || got[2].Kind != "mention_not_delivered" {
		t.Fatalf("after a reply that leaves bob out %v", e.eventKinds())
	}
	if d := detailOf(t, got[2]); d != (delivery.MentionDetail{UserID: 2, EntryID: 71, Target: onCallEntry.Target,
		Event: mentions.KindNewAlerts, Reason: mentions.ReasonMessageTooLong}) {
		t.Errorf("the left-out mention of the reply %+v", d)
	}
	// A Loud reply that mentions him closes it.
	e.business.Advance(5 * time.Minute)
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, alertsAdded(3, groups.StatusFiring, "f2"))
	e.round(t)
	if got = e.mentionEvents(); len(got) != 4 || got[3].Kind != "mention_delivered" {
		t.Errorf("after a reply that mentions bob %v", e.eventKinds())
	}
}

// TestMentionEventsPlain is C-28.FR-3 and C-22.FR-16 (#652) for a person whom a bridge's message names by the
// plain name, because no marker could be built for their Account link: they are not delivered, with the reason
// bridge_cannot_mention_people, after those the resolution found undelivered and those left out for length; a later
// Loud reply that names a person with an open warning so records no mention_delivered, and one that mentions them
// closes it.
func TestMentionEventsPlain(t *testing.T) {
	e := newEnv(t)
	e.db.dests[destMM].limit = 600
	alice, bob := aliceMentioned, bobMentioned
	alice.PublicID, bob.PublicID = "SRAAAAAAAAAAA1", "SRAAAAAAAAAAA2"
	m := &fakeMentioner{undelivered: []mentions.Undelivered{bobUndelivered}, mentioned: []mentions.Mentioned{alice}}
	e.w.Mentions = m
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK,
		Plain: []string{alice.PublicID}}})
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	e.round(t)
	if kinds := e.eventKinds(); !slices.Equal(kinds,
		[]string{"publication", "mention_not_delivered", "mention_not_delivered"}) {
		t.Fatalf("events %v", kinds)
	}
	got := e.mentionEvents()
	if d := detailOf(t, got[1]); d != (delivery.MentionDetail{UserID: 1, EntryID: 72, Position: 1,
		Target: aliceEntry.Target, Event: mentions.KindNewAlertGroup,
		Reason: mentions.ReasonBridgeCannotMentionPeople}) || got[1].Loudness != "quiet" {
		t.Errorf("the mention by the plain name %+v", d)
	}

	// bob's warning is open: a Loud reply that names him by the plain name, and leaves nobody out, keeps it open.
	m.undelivered, m.mentioned = nil, []mentions.Mentioned{bob}
	e.rec.Script(deliverytest.MethodReply, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK,
		Plain: []string{bob.PublicID}}})
	e.business.Advance(5 * time.Minute)
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, alertsAdded(2, groups.StatusFiring, "f1"))
	e.round(t)
	got = e.mentionEvents()
	if len(got) != 3 || got[2].Kind != "mention_not_delivered" {
		t.Fatalf("after a reply that names bob by the plain name %v", e.eventKinds())
	}
	if d := detailOf(t, got[2]); d != (delivery.MentionDetail{UserID: 2, EntryID: 71, Target: onCallEntry.Target,
		Event: mentions.KindNewAlerts, Reason: mentions.ReasonBridgeCannotMentionPeople}) {
		t.Errorf("the mention of the reply by the plain name %+v", d)
	}
	// A Loud reply that mentions him closes it.
	e.business.Advance(5 * time.Minute)
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, alertsAdded(3, groups.StatusFiring, "f2"))
	e.round(t)
	if got = e.mentionEvents(); len(got) != 4 || got[3].Kind != "mention_delivered" {
		t.Errorf("after a reply that mentions bob %v", e.eventKinds())
	}
}

// TestMentionEventsNotDelivered is C-28.FR-3 and C-11.FR-10: a Loud Publication that ends Not delivered records
// not_delivered and no mention event, and the warnings stay as they were.
func TestMentionEventsNotDelivered(t *testing.T) {
	e := newEnv(t)
	e.w.Mentions = &fakeMentioner{undelivered: []mentions.Undelivered{bobUndelivered},
		mentioned: []mentions.Mentioned{aliceMentioned}}
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeUnknown, "boom"))
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	e.round(t)
	if kinds := e.eventKinds(); !slices.Equal(kinds, []string{"not_delivered"}) {
		t.Errorf("events %v", kinds)
	}
}

// TestMentionEventsOnce is C-11.FR-15 for the mention events: a Publication or a Thread reply whose lease went to
// another replica during the call records no mention event, and the attempt that delivers it records each once.
func TestMentionEventsOnce(t *testing.T) {
	e := newEnv(t)
	e.db.dests[destMM].limit = 600
	m := &fakeMentioner{undelivered: []mentions.Undelivered{bobUndelivered}}
	e.w.Mentions = m
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	d := e.only(t)
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK},
		Then: func() { d.owner = "r2" }})
	e.round(t)
	if len(e.db.events) != 0 {
		t.Fatalf("recorded without the lease: %v", e.eventKinds())
	}
	d.until = real0.Add(delivery.Lease)
	e.real.Advance(delivery.Lease)
	e.round(t)
	if kinds := e.eventKinds(); d.state != "delivered" ||
		!slices.Equal(kinds, []string{"possible_duplicate", "publication", "mention_not_delivered"}) {
		t.Fatalf("after the retry: %s %v", d.state, kinds)
	}
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, alertsAdded(2, groups.StatusFiring, "f1"))
	e.rec.Script(deliverytest.MethodReply, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK},
		Then: func() { e.db.replies[0].owner = "r2" }})
	e.business.Advance(5 * time.Minute)
	e.round(t)
	if n := len(e.mentionEvents()); n != 1 {
		t.Fatalf("a reply recorded without the lease: %d mention events", n)
	}
	e.db.replies[0].until = e.real.Now()
	e.real.Advance(delivery.Lease)
	e.round(t)
	if n := len(e.mentionEvents()); n != 2 || e.db.replies[0].state != "sent" {
		t.Errorf("after the retry of the reply: %d mention events, %s", n, e.db.replies[0].state)
	}
}

// TestMentionDeliveredRetention is C-11.FR-21 for the warnings: a mention_not_delivered older than
// retention.alert_details is no open warning, so a later delivered Mention of the person through the entry records
// nothing; one within it is closed.
func TestMentionDeliveredRetention(t *testing.T) {
	e := newEnv(t)
	e.db.dests[destMM].limit = 600
	e.db.details = 1
	m := &fakeMentioner{undelivered: []mentions.Undelivered{bobUndelivered}}
	e.w.Mentions = m
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	e.round(t)
	m.undelivered, m.mentioned = nil, []mentions.Mentioned{bobMentioned}
	e.business.Advance(25 * time.Hour)
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, alertsAdded(2, groups.StatusFiring, "f1"))
	e.round(t)
	if n := len(e.mentionEvents()); n != 1 || len(e.replies()) != 1 {
		t.Errorf("past retention: %d mention events, %d replies", n, len(e.replies()))
	}
	e.db.details = 90
	e.business.Advance(5 * time.Minute)
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, alertsAdded(3, groups.StatusFiring, "f2"))
	e.round(t)
	if got := e.mentionEvents(); len(got) != 2 || got[1].Kind != "mention_delivered" {
		t.Errorf("within retention: %v", e.eventKinds())
	}
	// A failure fails the transaction that records the delivery, which is then rolled back and retried.
	for name, text := range map[string]string{"GetRetentionDetailsDays": "read the retention of the delivery events",
		"ListOpenMentions":    "read the open warnings about mentions of destination",
		"InsertDeliveryEvent": "record the mention_not_delivered delivery event"} {
		f := newEnv(t)
		f.w.Mentions = &fakeMentioner{undelivered: []mentions.Undelivered{bobUndelivered},
			mentioned: []mentions.Mentioned{aliceMentioned}}
		f.enqueue(t, f.group(groups.StatusFiring, "a"), groups.System, created())
		if name == "InsertDeliveryEvent" {
			// The publication's event is written, the mention's fails.
			f.db.before[name] = func() { f.db.before[name] = func() { f.db.fail[name] = errBoomMention } }
		} else {
			f.db.fail[name] = errBoomMention
		}
		f.round(t)
		if !strings.Contains(f.log.String(), text) || !strings.Contains(f.log.String(), "delivery_work_failed") {
			t.Errorf("%s fails: log %s", name, f.log)
		}
	}
}

// TestMentionEventsStorm is C-28.FR-7 with C-28.FR-3: the undelivered Mentions of the first publication of a Storm
// summary are recorded with its Storm and no Alert Group, so that the Destination warns and no Timeline shows them.
func TestMentionEventsStorm(t *testing.T) {
	e := stormEnv(t)
	m := &fakeMentioner{}
	e.w.Mentions = m
	for i := int64(1); i <= 21; i++ {
		e.business.Set(business0.Add(time.Duration(i) * time.Second))
		if i == 21 {
			m.undelivered = []mentions.Undelivered{{UserID: 2, Entry: onCallEntry, Event: mentions.EventStormStarted,
				Reason: mentions.ReasonNoAccountLink}}
		}
		e.addGroup(t, 1000+i, false)
		e.round(t)
	}
	st := e.onlyStorm(t)
	got := e.mentionEvents()
	if len(got) != 1 || got[0].AlertGroupID.Valid || got[0].StormID.Int64 != st.id ||
		detailOf(t, got[0]).Event != mentions.EventStormStarted {
		t.Errorf("storm summary mention events %+v", got)
	}
}

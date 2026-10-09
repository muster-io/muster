// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/outbound"
)

// fakeEvent is a row of webhook_events.
type fakeEvent struct {
	id, dest, group, seq  int64
	webhookID, event      string
	notify                bool
	body                  []byte
	state                 string
	attempts              int64
	firstFailed           *time.Time
	next                  time.Time
	owner                 string
	until                 time.Time
	errorClass, lastError *string
	deliveredAt, received *time.Time
	created               time.Time
}

// head reports whether e is the pending event with the lowest sequence of its Alert Group and Destination.
func (f *fakeDB) head(e *fakeEvent) bool {
	return e.state == "pending" && !slices.ContainsFunc(f.webhookEvents, func(p *fakeEvent) bool {
		return p.dest == e.dest && p.group == e.group && p.state == "pending" && p.seq < e.seq
	})
}

func (f *fakeDB) webhookEvent(id int64) *fakeEvent {
	for _, e := range f.webhookEvents {
		if e.id == id {
			return e
		}
	}
	return nil
}

// eventDue reports whether an event of the Destination dest came due at due with its lease free at now.
func (f *fakeDB) eventDue(dest int64, due, now time.Time) bool {
	return slices.ContainsFunc(f.webhookEvents, func(e *fakeEvent) bool {
		return e.dest == dest && e.state == "pending" && !e.next.After(due) && (e.owner == "" || !e.until.After(now))
	})
}

// eventWork adds the head events of healthy Destinations, or of those whose next event is the probe, to the next
// work, as NextDeliveryWork does.
func (f *fakeDB) eventWork(add func(next time.Time, owner string, until time.Time)) {
	for _, e := range f.webhookEvents {
		ds := f.dests[e.dest]
		if f.head(e) && !ds.deleted && (ds.health == "healthy" || ds.probeOnNext) {
			add(e.next, e.owner, e.until)
		}
	}
}

// pendingEvents counts the pending events of a Destination.
func (f *fakeDB) pendingEvents(dest int64) int64 {
	var n int64
	for _, e := range f.webhookEvents {
		if e.dest == dest && e.state == "pending" {
			n++
		}
	}
	return n
}

// leasedOf reports whether a call of the Destination holds a lease at now.
func (f *fakeDB) leasedOf(dest int64, now time.Time) bool {
	return slices.ContainsFunc(f.deliveries, func(d *fakeDelivery) bool {
		return d.dest == dest && d.owner != "" && d.until.After(now)
	}) || slices.ContainsFunc(f.webhookEvents, func(e *fakeEvent) bool {
		return e.dest == dest && e.owner != "" && e.until.After(now)
	})
}

func (f *fakeDB) InsertWebhookEvent(_ context.Context, arg dbgen.InsertWebhookEventParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("InsertWebhookEvent"); err != nil {
		return err
	}
	if slices.ContainsFunc(f.webhookEvents, func(e *fakeEvent) bool {
		return e.dest == arg.DestinationID && e.group == arg.AlertGroupID && e.seq == arg.Sequence
	}) {
		return nil
	}
	e := &fakeEvent{id: f.id(), dest: arg.DestinationID, group: arg.AlertGroupID, seq: arg.Sequence,
		webhookID: arg.WebhookID, event: arg.Event, notify: arg.Notify, body: arg.Body, state: "pending",
		next: arg.Now, created: arg.Now}
	if arg.ReceivedAt.Valid {
		e.received = at(arg.ReceivedAt.Time)
	}
	f.webhookEvents = append(f.webhookEvents, e)
	return nil
}

func (f *fakeDB) ClaimDueWebhookEvents(_ context.Context, arg dbgen.ClaimDueWebhookEventsParams) (
	[]dbgen.ClaimDueWebhookEventsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ClaimDueWebhookEvents"); err != nil {
		return nil, err
	}
	var due []*fakeEvent
	for _, e := range f.webhookEvents {
		ds := f.dests[e.dest]
		if f.head(e) && !e.next.After(arg.Due) && (e.owner == "" || !e.until.After(arg.Now)) &&
			ds.health == "healthy" && !ds.deleted {
			due = append(due, e)
		}
	}
	slices.SortFunc(due, func(a, b *fakeEvent) int { return cmp.Or(a.next.Compare(b.next), cmp.Compare(a.id, b.id)) })
	var out []dbgen.ClaimDueWebhookEventsRow
	for _, e := range due {
		if len(out) == int(arg.Lim) {
			break
		}
		e.owner, e.until = arg.Owner, arg.LeaseUntil
		out = append(out, dbgen.ClaimDueWebhookEventsRow{ID: e.id, NextAttemptAt: e.next})
	}
	return out, nil
}

func (f *fakeDB) GetLeasedWebhookEvent(_ context.Context, arg dbgen.GetLeasedWebhookEventParams) (
	dbgen.GetLeasedWebhookEventRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetLeasedWebhookEvent"); err != nil {
		return dbgen.GetLeasedWebhookEventRow{}, err
	}
	e := f.webhookEvent(arg.ID)
	if e == nil || e.state != "pending" || e.owner != arg.Owner || !e.until.After(arg.Now) {
		return dbgen.GetLeasedWebhookEventRow{}, pgx.ErrNoRows
	}
	ds, g := f.dests[e.dest], f.groups[e.group]
	row := dbgen.GetLeasedWebhookEventRow{ID: e.id, AlertGroupID: e.group, Sequence: e.seq, WebhookID: e.webhookID,
		Event: e.event, Body: e.body, Attempts: e.attempts, DestinationID: ds.id, DestinationPublicID: ds.publicID,
		DestinationName: ds.name, DestinationType: ds.typ, DestinationHealth: ds.health,
		AlertGroupPublicID: g.publicID, Number: g.number}
	if e.received != nil {
		row.ReceivedAt = pgtype.Timestamptz{Time: *e.received, Valid: true}
	}
	return row, nil
}

func (f *fakeDB) RenewWebhookEventLease(_ context.Context, arg dbgen.RenewWebhookEventLeaseParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("RenewWebhookEventLease"); err != nil {
		return err
	}
	if e := f.webhookEvent(arg.ID); e.owner == arg.Owner {
		e.until = arg.LeaseUntil
	}
	return nil
}

func (f *fakeDB) RescheduleWebhookEvent(_ context.Context, arg dbgen.RescheduleWebhookEventParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("RescheduleWebhookEvent"); err != nil {
		return err
	}
	if e := f.webhookEvent(arg.ID); e.owner == arg.Owner {
		e.next, e.owner, e.until = arg.At, "", time.Time{}
	}
	return nil
}

// leased is the pending event id this replica holds, or nil.
func (f *fakeDB) leased(id int64, owner string) *fakeEvent {
	if e := f.webhookEvent(id); e != nil && e.owner == owner && e.state == "pending" {
		return e
	}
	return nil
}

func (f *fakeDB) RecordWebhookEventDelivered(_ context.Context, arg dbgen.RecordWebhookEventDeliveredParams) (int64,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("RecordWebhookEventDelivered"); err != nil {
		return 0, err
	}
	e := f.leased(arg.ID, arg.Owner)
	if e == nil {
		return 0, pgx.ErrNoRows
	}
	e.state, e.deliveredAt, e.errorClass, e.lastError, e.owner, e.until = "delivered", at(arg.Now), nil, nil, "",
		time.Time{}
	return e.id, nil
}

func (f *fakeDB) RecordWebhookEventRetry(_ context.Context, arg dbgen.RecordWebhookEventRetryParams) (
	dbgen.RecordWebhookEventRetryRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("RecordWebhookEventRetry"); err != nil {
		return dbgen.RecordWebhookEventRetryRow{}, err
	}
	e := f.leased(arg.ID, arg.Owner)
	if e == nil {
		return dbgen.RecordWebhookEventRetryRow{}, pgx.ErrNoRows
	}
	e.next, e.owner, e.until = arg.At, "", time.Time{}
	if arg.Counted {
		e.attempts++
		if e.firstFailed == nil {
			e.firstFailed = at(arg.Now)
		}
	}
	e.errorClass, e.lastError = textPtr(arg.ErrorClass), textPtr(arg.Error)
	row := dbgen.RecordWebhookEventRetryRow{Attempts: e.attempts}
	if e.firstFailed != nil {
		row.FirstFailedAt = pgtype.Timestamptz{Time: *e.firstFailed, Valid: true}
	}
	return row, nil
}

func textPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func (f *fakeDB) RecordWebhookEventNotDelivered(_ context.Context, arg dbgen.RecordWebhookEventNotDeliveredParams) (
	int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("RecordWebhookEventNotDelivered"); err != nil {
		return 0, err
	}
	e := f.leased(arg.ID, arg.Owner)
	if e == nil {
		return 0, pgx.ErrNoRows
	}
	e.state, e.errorClass, e.lastError, e.owner, e.until = "not_delivered", at2(arg.ErrorClass), at2(arg.Error), "",
		time.Time{}
	return e.id, nil
}

func (f *fakeDB) ReleaseWebhookEventLease(_ context.Context, arg dbgen.ReleaseWebhookEventLeaseParams) (int64,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ReleaseWebhookEventLease"); err != nil {
		return 0, err
	}
	e := f.webhookEvent(arg.ID)
	if e == nil || e.owner != arg.Owner {
		return 0, pgx.ErrNoRows
	}
	e.owner, e.until = "", time.Time{}
	return e.dest, nil
}

func (f *fakeDB) LeaseOldestWaitingEvent(_ context.Context, arg dbgen.LeaseOldestWaitingEventParams) (
	dbgen.LeaseOldestWaitingEventRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("LeaseOldestWaitingEvent"); err != nil {
		return dbgen.LeaseOldestWaitingEventRow{}, err
	}
	var oldest *fakeEvent
	for _, e := range f.webhookEvents { // in id order
		if e.dest != arg.DestinationID || !f.head(e) {
			continue
		}
		if oldest == nil || (oldest.next.After(arg.Due) && !e.next.After(arg.Due)) {
			oldest = e
		}
	}
	if e := oldest; e != nil {
		free := e.owner == "" || !e.until.After(arg.Now)
		if free {
			e.owner, e.until = arg.Owner, arg.LeaseUntil
		}
		return dbgen.LeaseOldestWaitingEventRow{ID: e.id, Free: free}, nil
	}
	return dbgen.LeaseOldestWaitingEventRow{}, pgx.ErrNoRows
}

func (f *fakeDB) AbandonWebhookEvents(_ context.Context, arg dbgen.AbandonWebhookEventsParams) (
	[]dbgen.AbandonWebhookEventsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("AbandonWebhookEvents"); err != nil {
		return nil, err
	}
	var out []dbgen.AbandonWebhookEventsRow
	for _, e := range f.webhookEvents {
		if e.dest != arg.DestinationID || e.state != "pending" {
			continue
		}
		e.state, e.errorClass, e.lastError = "not_delivered", at2("unknown"), at2(arg.Error)
		out = append(out, dbgen.AbandonWebhookEventsRow{AlertGroupID: e.group, Event: e.event,
			AlertGroupPublicID: f.groups[e.group].publicID})
	}
	return out, nil
}

func (f *fakeDB) ResetWebhookEventBudgets(_ context.Context, arg dbgen.ResetWebhookEventBudgetsParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ResetWebhookEventBudgets"); err != nil {
		return err
	}
	for _, e := range f.webhookEvents {
		if e.dest == arg.DestinationID && e.state == "pending" {
			e.attempts, e.firstFailed = 0, nil
			if (e.errorClass == nil || *e.errorClass != "retry_after") && e.next.After(arg.Now) {
				e.next = arg.Now
			}
		}
	}
	return nil
}

func (f *fakeDB) DeleteExpiredWebhookEvents(_ context.Context, arg dbgen.DeleteExpiredWebhookEventsParams) (int64,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("DeleteExpiredWebhookEvents"); err != nil {
		return 0, err
	}
	var n int64
	f.webhookEvents = slices.DeleteFunc(f.webhookEvents, func(e *fakeEvent) bool {
		if n < int64(arg.BatchSize) && e.state != "pending" && e.created.Before(arg.Cutoff) {
			n++
			return true
		}
		return false
	})
	return n, nil
}

// fakeSender is the events-mode endpoint: it records each request and answers the scripted outcomes in order, then
// ok.
type fakeSender struct {
	mu      sync.Mutex
	calls   []delivery.EventCall
	answers []delivery.Outcome
	before  func()
}

func (s *fakeSender) SendEvent(_ context.Context, c delivery.EventCall) delivery.Outcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, c)
	if s.before != nil {
		b := s.before
		s.before = nil
		s.mu.Unlock()
		b()
		s.mu.Lock()
	}
	if len(s.answers) == 0 {
		return delivery.Outcome{Kind: delivery.OutcomeOK, Status: 200}
	}
	out := s.answers[0]
	s.answers = s.answers[1:]
	return out
}

// sent are the events of the calls, as "event@group" in order.
func (s *fakeSender) sent(t *testing.T) []string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.calls))
	for i, c := range s.calls {
		var b stubBody
		if err := json.Unmarshal(c.Body, &b); err != nil {
			t.Fatal(err)
		}
		out[i] = fmt.Sprintf("%s@%d", b.Event, b.Group)
	}
	return out
}

// stubBody is the body stubBodies renders: what delivery hands over for each event.
type stubBody struct {
	Version  int                `json:"version"`
	Event    string             `json:"event"`
	Notify   bool               `json:"notify"`
	Sequence int64              `json:"sequence"`
	Group    int64              `json:"group"`
	Route    string             `json:"route"`
	Actor    string             `json:"actor"`
	Mentions []mentions.Target  `json:"mentions"`
	At       time.Time          `json:"at"`
	Status   groups.Status      `json:"status"`
	Kind     audit.ActorKind    `json:"kind"`
	Extra    map[string]float64 `json:"extra,omitempty"`
}

// stubBodies renders the stub body of each event of a change; err fails reading the change, and failRender the
// rendering of an event.
type stubBodies struct {
	err, failRender error
	sources         []delivery.EventSource
}

func (b *stubBodies) EventBodies(_ context.Context, _ groups.DBTX, in delivery.EventSource) (delivery.BodyFunc,
	error) {
	if b.err != nil {
		return nil, b.err
	}
	b.sources = append(b.sources, in)
	return func(e delivery.EventBody) ([]byte, error) {
		if b.failRender != nil {
			return nil, b.failRender
		}
		return json.Marshal(stubBody{Version: 1, Event: string(e.Event.Event), Notify: e.Notify, Sequence: e.Event.Seq,
			Group: in.Group.ID, Route: in.RoutePublicID, Actor: string(in.Actor.Transport), Mentions: e.Mentions,
			At: e.OccurredAt, Status: in.Group.Status, Kind: in.Actor.Kind})
	}, nil
}

// eventsEnv is the env with the webhook Destination 12 in the events mode on Route 1 alone, its sender and bodies.
type eventsEnv struct {
	*env
	sender *fakeSender
	bodies *stubBodies
}

func newEventsEnv(t *testing.T, mentioner delivery.Mentioner) *eventsEnv {
	t.Helper()
	e := newEnv(t)
	// Its own public_id keeps its series apart from those the other tests check.
	e.db.dests[destWH].mode, e.db.dests[destWH].publicID = "events", "DSAAAAAAAAAAEV"
	e.db.routeDests[routeID] = []int64{destWH}
	delete(e.w.Adapters, delivery.TypeWebhook)
	ee := &eventsEnv{env: e, sender: &fakeSender{}, bodies: &stubBodies{}}
	e.svc = delivery.New(delivery.Config{OrgID: orgID, Store: e.store, Business: e.business, Real: e.real,
		Log: e.w.Log, Renderer: stubRenderer{}, Bodies: ee.bodies, Mentions: mentioner})
	e.w.Events = ee.sender
	return ee
}

// addGroup adds the Alert Group id on Route 1.
func (e *eventsEnv) addGroup(id int64) *groups.Group {
	e.db.groups[id] = &fakeGroup{id: id, publicID: fmt.Sprintf("AGAAAAAAAAAA%02d", id%100), number: id,
		title: "g", status: "firing", route: routeID, created: e.business.Now()}
	return &groups.Group{ID: id, PublicID: e.db.groups[id].publicID, Number: id, RouteID: routeID, Title: "g",
		Status: groups.StatusFiring}
}

func event(seq int64, ev groups.Event, v groups.Variant) groups.Recorded {
	return groups.Recorded{Seq: seq, Event: ev, Variant: v}
}

var webUser = groups.Actor{Kind: audit.ActorUser, Transport: audit.TransportAPI, Person: audit.User(3, "SR3")}

// drain runs rounds until the worker has nothing due now.
func (e *eventsEnv) drain(t *testing.T) {
	t.Helper()
	for range 20 {
		n := len(e.sender.calls)
		e.round(t)
		if len(e.sender.calls) == n {
			return
		}
	}
}

// TestWebhookEventsEveryRow is C-15.FR-2 and AC-11: every row of the lifecycle event tables produces exactly one
// events-mode request whose event is the row's name and whose notify its loudness, Quiet rows and rows with nothing to
// show in a messenger included, with version 1 and the event's number as sequence; delivery events are never sent.
func TestWebhookEventsEveryRow(t *testing.T) {
	for i, row := range delivery.Table {
		t.Run(fmt.Sprintf("%s/%s", row.Event, row.Variant), func(t *testing.T) {
			e := newEventsEnv(t, &fakeMentioner{})
			actor, v := webUser, row.Variant
			if row.Event == groups.EventResolved {
				v = groups.VariantAny
				if row.Variant == delivery.VariantSystem {
					actor = groups.System
				}
			}
			ev := groups.Recorded{Seq: int64(i + 1), Event: row.Event, Variant: v, Loudness: row.Loudness,
				Mentions: row.Mentions}
			if row.EventMentions {
				ev.Mentions = []groups.Mention{groups.MentionOwner}
			}
			e.enqueue(t, e.group(groups.StatusFiring, "t"), actor, ev)
			if len(e.db.webhookEvents) != 1 {
				t.Fatalf("events %d", len(e.db.webhookEvents))
			}
			we := e.db.webhookEvents[0]
			if we.event != string(row.Event) || we.notify != (row.Loudness == groups.Loud) || we.seq != int64(i+1) ||
				!strings.HasPrefix(we.webhookID, "msg_") || len(we.webhookID) != 30 {
				t.Fatalf("event %+v", we)
			}
			e.drain(t)
			if len(e.sender.calls) != 1 {
				t.Fatalf("calls %d", len(e.sender.calls))
			}
			var b stubBody
			if err := json.Unmarshal(e.sender.calls[0].Body, &b); err != nil {
				t.Fatal(err)
			}
			loud := row.Loudness == groups.Loud
			if b.Version != 1 || b.Event != string(row.Event) || b.Notify != loud || b.Sequence != int64(i+1) {
				t.Errorf("body %+v", b)
			}
			if wantMentions := loud && (len(row.Mentions) > 0 || row.EventMentions); (len(b.Mentions) > 0) !=
				wantMentions {
				t.Errorf("mentions %+v", b.Mentions)
			}
			if c := e.sender.calls[0]; c.Class != outbound.ClassDelivery || c.WebhookID != we.webhookID ||
				c.Destination.ID != destWH {
				t.Errorf("call %+v", c)
			}
			if we.state != "delivered" || len(e.rec.Calls()) != 0 {
				t.Errorf("state %s, messenger calls %d", we.state, len(e.rec.Calls()))
			}
		})
	}
	for _, r := range delivery.DeliveryEvents {
		if slices.ContainsFunc(delivery.Table, func(row delivery.Row) bool { return string(row.Event) == r.Name }) {
			t.Errorf("the delivery event %s is a lifecycle event", r.Name)
		}
	}
}

// TestWebhookEventsBody is C-15.FR-2 and FR-11: the body is rendered when the event is queued, with the Route, the
// actor, the time and the Alert Group after the change; a Loud event carries its Mentions in the Destination and a
// Quiet one none; the receipt time of the Snapshot is kept for the latency.
func TestWebhookEventsBody(t *testing.T) {
	m := &fakeMentioner{}
	e := newEventsEnv(t, m)
	received := business0.Add(-3 * time.Second)
	g := e.group(groups.StatusFiring, "t")
	if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: g, Actor: groups.System,
		Events:     []groups.Recorded{created(), event(2, groups.EventAlertsAdded, groups.VariantAcknowledged)},
		ReceivedAt: &received}); err != nil {
		t.Fatal(err)
	}
	if len(e.db.webhookEvents) != 2 {
		t.Fatalf("events %d", len(e.db.webhookEvents))
	}
	var first, second stubBody
	_ = json.Unmarshal(e.db.webhookEvents[0].body, &first)
	_ = json.Unmarshal(e.db.webhookEvents[1].body, &second)
	if first.Route != "RTAAAAAAAAAAA1" || first.Group != groupID || !first.At.Equal(business0) ||
		first.Kind != audit.ActorSystem || len(first.Mentions) != 1 || first.Mentions[0].Everyone != "new_alert_group" {
		t.Errorf("created %+v", first)
	}
	if second.Notify || len(second.Mentions) != 0 {
		t.Errorf("alerts_added %+v", second)
	}
	if len(m.reqs) != 1 || m.reqs[0].DestinationType != delivery.TypeWebhook || m.reqs[0].Seq != 1 ||
		m.reqs[0].ConnectionID != nil {
		t.Errorf("mention requests %+v", m.reqs)
	}
	if r := e.db.webhookEvents[0].received; r == nil || !r.Equal(received) {
		t.Errorf("received %v", r)
	}
	if e.db.notified == 0 {
		t.Error("the workers were not woken")
	}
	e.business.Advance(time.Second)
	e.drain(t)
	if !strings.Contains(scrape(t), `muster_delivery_latency_seconds_count{destination="DSAAAAAAAAAAEV"}`) {
		t.Error("no latency")
	}
	if !strings.Contains(scrape(t),
		`muster_delivery_attempts_total{destination="DSAAAAAAAAAAEV",kind="webhook_event",outcome="delivered"}`) {
		t.Error("no attempts")
	}
}

// TestWebhookEventsQueueing covers what queues no event: no events-mode Destination or no Bodies, and the failures
// of reading the change, of the Mentions, of the body and of the insert.
func TestWebhookEventsQueueing(t *testing.T) {
	e := newEventsEnv(t, nil)
	e.db.dests[destWH].mode = "template"
	e.enqueue(t, e.group(groups.StatusFiring, "t"), groups.System, created())
	if len(e.db.webhookEvents) != 0 || len(e.db.deliveries) != 1 {
		t.Fatalf("template mode: events %d, deliveries %d", len(e.db.webhookEvents), len(e.db.deliveries))
	}

	e = newEventsEnv(t, nil)
	e.db.dests[destWH].mode = "both"
	e.enqueue(t, e.group(groups.StatusFiring, "t"), groups.System, created())
	if len(e.db.webhookEvents) != 1 || len(e.db.deliveries) != 1 {
		t.Fatalf("mode both: events %d, deliveries %d", len(e.db.webhookEvents), len(e.db.deliveries))
	}

	e = newEventsEnv(t, nil)
	e.svc = delivery.New(delivery.Config{OrgID: orgID, Store: e.store, Business: e.business, Log: e.w.Log,
		Renderer: stubRenderer{}})
	e.enqueue(t, e.group(groups.StatusFiring, "t"), groups.System, created())
	if len(e.db.webhookEvents) != 0 || len(e.db.deliveries) != 0 {
		t.Fatalf("no bodies: events %d, deliveries %d", len(e.db.webhookEvents), len(e.db.deliveries))
	}

	boom := errors.New("boom")
	for name, set := range map[string]func(e *eventsEnv){
		"bodies":   func(e *eventsEnv) { e.bodies.err = boom },
		"render":   func(e *eventsEnv) { e.bodies.failRender = boom },
		"insert":   func(e *eventsEnv) { e.db.fail["InsertWebhookEvent"] = boom },
		"mentions": func(e *eventsEnv) { e.svc = nil },
	} {
		t.Run(name, func(t *testing.T) {
			m := &fakeMentioner{}
			e := newEventsEnv(t, m)
			if name == "mentions" {
				m.fail = boom
			} else {
				set(e)
			}
			err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: e.group(groups.StatusFiring, "t"),
				Actor: groups.System, Events: []groups.Recorded{created()}})
			if !errors.Is(err, boom) {
				t.Fatalf("err %v", err)
			}
		})
	}
}

// TestWebhookEventsOrder is C-15.FR-2 and AC-6: created, acknowledged and resolved of one Alert Group arrive in this
// order, each with its own webhook-id; acknowledged answered once with 503 and Retry-After is retried exactly as asked
// with the same webhook-id and body, and resolved is not sent before it succeeds; the events of another Alert Group
// do not wait.
func TestWebhookEventsOrder(t *testing.T) {
	e := newEventsEnv(t, nil)
	g := e.group(groups.StatusFiring, "t")
	e.enqueue(t, g, groups.System, created())
	e.enqueue(t, g, webUser, event(2, groups.EventAcknowledged, groups.VariantAny))
	e.enqueue(t, g, webUser, event(3, groups.EventResolved, groups.VariantAny))
	e.sender.answers = []delivery.Outcome{{Kind: delivery.OutcomeOK},
		{Kind: delivery.OutcomeRetryAfter, RetryAfter: 2 * time.Second, Status: 503, Error: "answered 503"}}
	e.drain(t)
	if got := e.sender.sent(t); !slices.Equal(got, []string{"created@21", "acknowledged@21"}) {
		t.Fatalf("sent %v", got)
	}
	other := e.addGroup(22)
	e.enqueue(t, other, groups.System, created())
	e.drain(t)
	if got := e.sender.sent(t); len(got) != 3 || got[2] != "created@22" {
		t.Fatalf("other group waited: %v", got)
	}
	ack := e.db.webhookEvents[1]
	if ack.state != "pending" || ack.attempts != 0 || *ack.errorClass != "retry_after" ||
		!ack.next.Equal(business0.Add(2*time.Second)) {
		t.Fatalf("ack %+v", ack)
	}
	e.business.Advance(2 * time.Second)
	e.drain(t)
	got := e.sender.sent(t)
	if !slices.Equal(got[3:], []string{"acknowledged@21", "resolved@21"}) {
		t.Fatalf("sent %v", got)
	}
	calls := e.sender.calls
	if calls[1].WebhookID != calls[3].WebhookID || string(calls[1].Body) != string(calls[3].Body) ||
		calls[0].WebhookID == calls[1].WebhookID || calls[3].WebhookID == calls[4].WebhookID {
		t.Error("webhook ids")
	}
	for _, we := range e.db.webhookEvents {
		if we.state != "delivered" || we.deliveredAt == nil {
			t.Errorf("event %+v", we)
		}
	}
}

// TestWebhookEventsNotDelivered is C-15.FR-6 and AC-7, AC-5: an unknown answer — 400, 413, 422 or a redirect — ends
// that event as Not delivered with the Destination healthy, with the not_delivered delivery event and the
// webhook_event_not_delivered line, and the next event follows; a template error ends it the same way with its class.
func TestWebhookEventsNotDelivered(t *testing.T) {
	e := newEventsEnv(t, nil)
	g := e.group(groups.StatusAcknowledged, "t")
	for i := range 5 {
		e.enqueue(t, g, webUser, event(int64(i+1), groups.EventAcknowledged, groups.VariantAny))
	}
	e.sender.answers = []delivery.Outcome{
		{Kind: delivery.OutcomeUnknown, Status: 400, Error: "answered 400: bad"},
		{Kind: delivery.OutcomeUnknown, Status: 413, Error: "answered 413"},
		{Kind: delivery.OutcomeUnknown, Status: 422, Error: "answered 422"},
		{Kind: delivery.OutcomeUnknown, Status: 302, Error: "redirect to http://elsewhere.example.org/x refused"},
		{Kind: delivery.OutcomeTemplateError, Error: "the request template failed: url: the Secret token is not set"},
	}
	e.drain(t)
	if len(e.sender.calls) != 5 || e.db.dests[destWH].health != "healthy" {
		t.Fatalf("calls %d, health %s", len(e.sender.calls), e.db.dests[destWH].health)
	}
	for i, we := range e.db.webhookEvents {
		if we.state != "not_delivered" {
			t.Errorf("event %d: %s", i, we.state)
		}
	}
	if c := *e.db.webhookEvents[4].errorClass; c != "template_error" {
		t.Errorf("class %s", c)
	}
	if len(e.db.events) != 5 || e.db.events[3].Kind != string(delivery.EventNotDelivered) ||
		e.db.events[3].Error.String != "redirect to http://elsewhere.example.org/x refused" ||
		!e.db.events[3].AlertGroupID.Valid || e.db.events[4].ErrorClass.String != "template_error" {
		t.Fatalf("delivery events %+v", e.db.events)
	}
	log := e.log.String()
	if !strings.Contains(log, `"event":"webhook_event_not_delivered"`) || !strings.Contains(log, `"status":"302"`) ||
		!strings.Contains(log, `"group":"AGAAAAAAAAAA21"`) || !strings.Contains(log, `"status":""`) {
		t.Errorf("log %s", log)
	}
}

// TestWebhookEventsBroken is C-15.FR-6, AC-7 and AC-9, C-11.FR-9 and FR-19: 404 makes the Destination Broken and the
// event waits; events that come due meanwhile are queued, none dropped; the probe attempts the oldest waiting event
// and, once it succeeds, the rest follow in order with a fresh budget.
func TestWebhookEventsBroken(t *testing.T) {
	e := newEventsEnv(t, nil)
	g := e.group(groups.StatusFiring, "t")
	e.enqueue(t, g, webUser, event(1, groups.EventUnacknowledged, groups.VariantCommand))
	e.sender.answers = []delivery.Outcome{{Kind: delivery.OutcomeFatal, Status: 404, Error: "answered 404"}}
	e.drain(t)
	ds := e.db.dests[destWH]
	if ds.health != "broken" || *ds.brokenCause != "fatal" || *ds.brokenReason != "answered 404" {
		t.Fatalf("dest %+v", ds)
	}
	e.enqueue(t, g, webUser, event(2, groups.EventAcknowledged, groups.VariantAny))
	e.enqueue(t, g, webUser, event(3, groups.EventResolved, groups.VariantAny))
	e.business.Advance(48 * time.Hour)
	e.real.Advance(time.Hour)
	e.drain(t)
	if got := e.sender.sent(t); !slices.Equal(got, []string{"unacknowledged@21", "unacknowledged@21",
		"acknowledged@21", "resolved@21"}) {
		t.Fatalf("sent %v", got)
	}
	if ds.health != "healthy" || e.db.calls["ResetWebhookEventBudgets"] != 1 {
		t.Fatalf("dest %+v", ds)
	}
}

// TestWebhookEventsProbeOnNext is C-11.FR-9: an outgoing webhook has no Destination check, so with nothing waiting
// the probe marks it, and the next event is attempted as soon as it comes due; one leased elsewhere waits.
func TestWebhookEventsProbeOnNext(t *testing.T) {
	e := newEventsEnv(t, nil)
	e.breakDest(destWH, "down")
	e.business.Advance(delivery.BrokenProbeInterval)
	e.round(t)
	ds := e.db.dests[destWH]
	if !ds.probeOnNext || len(e.sender.calls) != 0 {
		t.Fatalf("dest %+v", ds)
	}
	e.enqueue(t, e.group(groups.StatusFiring, "t"), groups.System, created())
	if next := e.round(t); len(e.sender.calls) != 1 || ds.health != "healthy" {
		t.Fatalf("calls %d, health %s, next %v", len(e.sender.calls), ds.health, next)
	}

	e = newEventsEnv(t, nil)
	e.enqueue(t, e.group(groups.StatusFiring, "t"), groups.System, created())
	e.breakDest(destWH, "down")
	e.db.webhookEvents[0].owner, e.db.webhookEvents[0].until = "r2", real0.Add(time.Hour)
	e.business.Advance(delivery.BrokenProbeInterval)
	e.round(t)
	if len(e.sender.calls) != 0 || e.db.dests[destWH].health != "broken" {
		t.Fatal("the event leased elsewhere was sent")
	}
	e.db.fail["LeaseOldestWaitingEvent"] = errBoom
	e.business.Advance(delivery.BrokenProbeInterval)
	e.round(t)
	if !strings.Contains(e.log.String(), "lease the oldest waiting webhook event") {
		t.Error("no failure logged")
	}
}

// TestWebhookEventsTransient is C-11.FR-8: a Transient answer is retried after the backoff, an attempt of the budget;
// when the budget runs out the Destination becomes Broken as unavailable, and a failing probe keeps it Broken with the
// error as its reason.
func TestWebhookEventsTransient(t *testing.T) {
	e := newEventsEnv(t, nil)
	e.w.Random = func() float64 { return 0 }
	e.enqueue(t, e.group(groups.StatusFiring, "t"), groups.System, created())
	for range delivery.TransientBudgetAttempts {
		e.sender.answers = append(e.sender.answers, delivery.Outcome{Kind: delivery.OutcomeTransient,
			Error: "connection refused"})
	}
	for range delivery.TransientBudgetAttempts {
		e.drain(t)
		e.business.Advance(delivery.TransientBackoffMax)
	}
	ds, we := e.db.dests[destWH], e.db.webhookEvents[0]
	if ds.health != "broken" || *ds.brokenCause != "unavailable" || we.state != "pending" ||
		we.attempts != delivery.TransientBudgetAttempts {
		t.Fatalf("dest %+v event %+v", ds, we)
	}
	e.sender.answers = []delivery.Outcome{{Kind: delivery.OutcomeTransient, Error: "still refused"}}
	e.business.Advance(delivery.BrokenProbeInterval)
	e.drain(t)
	if ds.health != "broken" || *ds.brokenReason != "still refused" {
		t.Fatalf("dest %+v", ds)
	}
}

// TestWebhookEventsWorkerPaths covers the paths of a claimed event: a limiter without a token reschedules it, a lease
// lost or an event ended while its call was in flight gives up the lease, the failures of each step are logged, and
// without a sender the event is Not delivered.
func TestWebhookEventsWorkerPaths(t *testing.T) {
	e := newEventsEnv(t, nil)
	e.db.dests[destWH].limit, e.db.dests[destWH].per = 1, 60
	g := e.group(groups.StatusFiring, "t")
	e.enqueue(t, g, groups.System, created())
	e.enqueue(t, e.addGroup(22), groups.System, created())
	e.round(t)
	if len(e.sender.calls) != 1 || !e.db.webhookEvents[1].next.After(business0) || e.db.webhookEvents[1].owner != "" {
		t.Fatalf("calls %d, second %+v", len(e.sender.calls), e.db.webhookEvents[1])
	}

	for _, name := range []string{"ClaimDueWebhookEvents", "GetLeasedWebhookEvent", "TakeTokens",
		"RenewWebhookEventLease", "RescheduleWebhookEvent"} {
		t.Run(name, func(t *testing.T) {
			e := newEventsEnv(t, nil)
			if name == "RescheduleWebhookEvent" {
				e.db.dests[destWH].limit = 1
				e.enqueue(t, e.addGroup(22), groups.System, created())
			}
			e.enqueue(t, e.group(groups.StatusFiring, "t"), groups.System, created())
			e.db.fail[name] = errBoom
			if _, err := e.w.Round(t.Context()); name == "ClaimDueWebhookEvents" && !errors.Is(err, errBoom) {
				t.Fatalf("err %v", err)
			}
			if name != "ClaimDueWebhookEvents" && !strings.Contains(e.log.String(), "delivery_work_failed") {
				t.Errorf("log %s", e.log.String())
			}
		})
	}

	for _, out := range []delivery.Outcome{{Kind: delivery.OutcomeOK}, {Kind: delivery.OutcomeRetryAfter,
		RetryAfter: time.Second}, {Kind: delivery.OutcomeTransient}, {Kind: delivery.OutcomeFatal},
		{Kind: delivery.OutcomeUnknown}} {
		t.Run("ended/"+string(out.Kind), func(t *testing.T) {
			e := newEventsEnv(t, nil)
			e.enqueue(t, e.group(groups.StatusFiring, "t"), groups.System, created())
			e.sender.answers = []delivery.Outcome{out}
			e.sender.before = func() {
				e.db.mu.Lock()
				defer e.db.mu.Unlock()
				e.db.webhookEvents[0].state = "not_delivered"
			}
			e.round(t)
			we := e.db.webhookEvents[0]
			if we.state != "not_delivered" || we.owner != "" || e.db.dests[destWH].health != "healthy" {
				t.Fatalf("event %+v", we)
			}
		})
	}

	for _, name := range []string{"RecordWebhookEventDelivered", "RecordWebhookEventRetry",
		"RecordWebhookEventNotDelivered", "ReleaseWebhookEventLease", "InsertDeliveryEvent", "Notify",
		"WipeDestinationSecrets"} {
		t.Run(name, func(t *testing.T) {
			e := newEventsEnv(t, nil)
			e.enqueue(t, e.group(groups.StatusFiring, "t"), groups.System, created())
			switch name {
			case "RecordWebhookEventRetry":
				e.sender.answers = []delivery.Outcome{{Kind: delivery.OutcomeTransient}}
			case "RecordWebhookEventNotDelivered", "InsertDeliveryEvent", "Notify":
				e.sender.answers = []delivery.Outcome{{Kind: delivery.OutcomeUnknown}}
			case "ReleaseWebhookEventLease", "WipeDestinationSecrets":
				e.sender.before = func() {
					e.db.mu.Lock()
					defer e.db.mu.Unlock()
					e.db.webhookEvents[0].state = "not_delivered"
				}
			}
			e.db.fail[name] = errBoom
			e.round(t)
			if !strings.Contains(e.log.String(), "delivery_work_failed") {
				t.Errorf("log %s", e.log.String())
			}
		})
	}

	e = newEventsEnv(t, nil)
	e.w.Events = nil
	e.enqueue(t, e.group(groups.StatusFiring, "t"), groups.System, created())
	e.round(t)
	if we := e.db.webhookEvents[0]; we.state != "not_delivered" || *we.lastError != "no sender for outgoing webhook events" {
		t.Fatalf("event %+v", we)
	}
	if !strings.Contains(scrape(t), `kind="webhook_event",outcome="unknown"`) {
		t.Error("no unknown attempt")
	}
}

// TestWebhookEventsDeletion is C-15.FR-12, AC-13 and C-11.FR-14: deleting an events-mode Destination with three events
// queued ends them as Not delivered, each with its delivery event, sends nothing more and wipes its secrets at once;
// an event whose call is in flight keeps the secrets until that call ends, and the worker that records it wipes them.
func TestWebhookEventsDeletion(t *testing.T) {
	e := newEventsEnv(t, nil)
	g := e.group(groups.StatusFiring, "t")
	e.breakDest(destWH, "answered 404")
	for i, ev := range []groups.Event{groups.EventUnresolved, groups.EventAcknowledged, groups.EventResolved} {
		e.enqueue(t, g, webUser, event(int64(i+1), ev, groups.VariantAny))
	}
	ds := e.db.dests[destWH]
	ds.deleted, ds.secrets, ds.named = true, 3, 2
	if err := e.svc.RetireDestination(t.Context(), nil, destWH); err != nil {
		t.Fatal(err)
	}
	for _, we := range e.db.webhookEvents {
		if we.state != "not_delivered" || *we.lastError != "the Destination was deleted" {
			t.Fatalf("event %+v", we)
		}
	}
	if ds.secrets != 0 || ds.named != 0 || len(e.db.events) != 3 ||
		e.db.events[0].Error.String != "the Destination was deleted" {
		t.Fatalf("secrets %d/%d, delivery events %+v", ds.secrets, ds.named, e.db.events)
	}
	e.business.Advance(time.Hour)
	e.drain(t)
	if len(e.sender.calls) != 0 {
		t.Fatalf("sent %d after the deletion", len(e.sender.calls))
	}

	e = newEventsEnv(t, nil)
	e.enqueue(t, e.group(groups.StatusFiring, "t"), groups.System, created())
	ds = e.db.dests[destWH]
	ds.secrets, ds.named = 2, 1
	var inFlight int
	e.sender.before = func() {
		ds.deleted = true
		if err := e.svc.RetireDestination(t.Context(), nil, destWH); err != nil {
			t.Error(err)
		}
		inFlight = ds.secrets + ds.named
	}
	e.round(t)
	if inFlight != 3 || ds.secrets != 0 || ds.named != 0 || e.db.webhookEvents[0].owner != "" {
		t.Fatalf("secrets in flight %d, after %d/%d", inFlight, ds.secrets, ds.named)
	}

	e = newEventsEnv(t, nil)
	e.db.fail["AbandonWebhookEvents"] = errBoom
	e.db.dests[destWH].deleted = true
	if err := e.svc.RetireDestination(t.Context(), nil, destWH); !errors.Is(err, errBoom) {
		t.Fatalf("err %v", err)
	}
	e = newEventsEnv(t, nil)
	e.enqueue(t, e.group(groups.StatusFiring, "t"), groups.System, created())
	e.db.dests[destWH].deleted = true
	for _, name := range []string{"InsertDeliveryEvent", "Notify"} {
		e.db.fail[name] = errBoom
		e.db.webhookEvents[0].state = "pending"
		if err := e.svc.RetireDestination(t.Context(), nil, destWH); !errors.Is(err, errBoom) {
			t.Fatalf("%s: err %v", name, err)
		}
		delete(e.db.fail, name)
	}
}

// TestWebhookEventsStorm is C-15.FR-2, AC-10 and C-11.FR-6: during a Storm every new Alert Group is a created event,
// while the messenger of the Route gets the Storm summary.
func TestWebhookEventsStorm(t *testing.T) {
	e := newEventsEnv(t, nil)
	e.db.routeDests[routeID] = []int64{destMM, destWH}
	r := e.db.routes[routeID]
	r.threshold = 2
	e.db.routes[routeID] = r
	for i := range 6 {
		e.enqueue(t, e.addGroup(int64(30+i)), groups.System, created())
	}
	if len(e.db.webhookEvents) != 6 || len(e.db.storms) != 1 {
		t.Fatalf("events %d, storms %d", len(e.db.webhookEvents), len(e.db.storms))
	}
	e.drain(t)
	if len(e.sender.calls) != 6 {
		t.Fatalf("sent %d", len(e.sender.calls))
	}
}

// TestWebhookEventsJoin is C-15.FR-2: an events-mode Destination added to a Route publishes nothing; it receives the
// events from then on.
func TestWebhookEventsJoin(t *testing.T) {
	e := newEventsEnv(t, nil)
	e.db.routeDests[routeID] = []int64{destWH}
	if err := e.svc.RouteDestinationsChanged(t.Context(), nil, routeID, []int64{destWH}, nil); err != nil {
		t.Fatal(err)
	}
	if len(e.db.deliveries) != 0 || len(e.db.webhookEvents) != 0 {
		t.Fatalf("deliveries %d, events %d", len(e.db.deliveries), len(e.db.webhookEvents))
	}
}

// TestPruneWebhookEvents is the retention of outgoing webhook events: those delivered or Not delivered and older than
// retention.alert_details are deleted in batches, pending ones stay, and the secrets of a deleted Destination whose
// last call ended without wiping them are wiped.
func TestPruneWebhookEvents(t *testing.T) {
	e := newEventsEnv(t, nil)
	e.db.details = 1
	g := e.group(groups.StatusFiring, "t")
	for i := range delivery.RetentionBatch + 3 {
		state := "delivered"
		if i == 0 {
			state = "pending"
		}
		e.db.webhookEvents = append(e.db.webhookEvents, &fakeEvent{id: int64(1000 + i), dest: destWH, group: g.ID,
			seq: int64(i + 1), state: state, created: business0})
	}
	ds := e.db.dests[destWH]
	ds.deleted, ds.secrets = true, 2
	n, err := e.svc.PruneWebhookEvents(t.Context(), business0.Add(25*time.Hour))
	if err != nil || n != delivery.RetentionBatch+2 || len(e.db.webhookEvents) != 1 || ds.secrets != 0 {
		t.Fatalf("pruned %d (%v), left %d, secrets %d", n, err, len(e.db.webhookEvents), ds.secrets)
	}
	for _, name := range []string{"GetRetentionDetailsDays", "DeleteExpiredWebhookEvents", "WipeDestinationSecrets"} {
		e.db.fail[name] = errBoom
		if _, err := e.svc.PruneWebhookEvents(t.Context(), business0); !errors.Is(err, errBoom) {
			t.Errorf("%s: err %v", name, err)
		}
		delete(e.db.fail, name)
	}
}

// TestWebhookEventsQueueAndNext covers the queue gauge and the next wake with events.
func TestWebhookEventsQueueAndNext(t *testing.T) {
	e := newEventsEnv(t, nil)
	e.db.dests[destWH].limit = 1
	e.enqueue(t, e.group(groups.StatusFiring, "t"), groups.System, created())
	e.enqueue(t, e.addGroup(22), groups.System, created())
	if err := e.svc.ExportQueue(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(scrape(t), `muster_delivery_queue{destination="DSAAAAAAAAAAEV"} 2`) {
		t.Error("queue")
	}
	if next := e.round(t); next <= 0 || next > time.Minute {
		t.Fatalf("next %v", next)
	}
}

// TestWebhookEventsDeletedBeforeTheCall is C-15.FR-12: a Destination deleted after its event was claimed and before
// the call ends the event and keeps the lease, so that its secrets wait; the worker that finds the event ended gives
// up the lease and wipes them at once, and sends nothing.
func TestWebhookEventsDeletedBeforeTheCall(t *testing.T) {
	e := newEventsEnv(t, nil)
	e.enqueue(t, e.group(groups.StatusFiring, "t"), groups.System, created())
	ds := e.db.dests[destWH]
	ds.secrets, ds.named = 2, 1
	e.db.before["GetLeasedWebhookEvent"] = func() {
		ds.deleted = true
		if err := e.svc.RetireDestination(t.Context(), nil, destWH); err != nil {
			t.Error(err)
		}
		if ds.secrets == 0 {
			t.Error("the secrets were wiped while the event was leased")
		}
	}
	e.round(t)
	we := e.db.webhookEvents[0]
	if len(e.sender.calls) != 0 || we.state != "not_delivered" || we.owner != "" || ds.secrets != 0 || ds.named != 0 {
		t.Fatalf("calls %d, event %+v, secrets %d/%d", len(e.sender.calls), we, ds.secrets, ds.named)
	}
}

// TestWebhookEventsProbeKeepsRetryAfter is C-11.FR-9: the probe takes the oldest head event that is due, so that an
// event that asked to wait is not sent early; the recovery makes the events that waited for a backoff due at once.
func TestWebhookEventsProbeKeepsRetryAfter(t *testing.T) {
	e := newEventsEnv(t, nil)
	e.enqueue(t, e.group(groups.StatusFiring, "t"), groups.System, created())
	e.enqueue(t, e.addGroup(22), groups.System, created())
	e.enqueue(t, e.addGroup(23), groups.System, created())
	first, second, third := e.db.webhookEvents[0], e.db.webhookEvents[1], e.db.webhookEvents[2]
	first.next, first.errorClass = business0.Add(time.Hour), at2("retry_after")
	third.next, third.errorClass = business0.Add(time.Hour), at2("transient")
	e.breakDest(destWH, "down")
	e.business.Advance(delivery.BrokenProbeInterval)
	e.round(t)
	if got := e.sender.sent(t); len(got) == 0 || got[0] != "created@22" {
		t.Fatalf("probe sent %v", got)
	}
	if second.state != "delivered" || !first.next.Equal(business0.Add(time.Hour)) || third.state != "delivered" {
		t.Errorf("after recovery: first %+v, third %+v", first, third)
	}
}

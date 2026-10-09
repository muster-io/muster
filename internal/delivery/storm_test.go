// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
)

// The queries of Storms over the fake database.

func (f *fakeDB) activeStorm(route int64) *fakeStorm {
	for _, st := range f.storms {
		if st.route == route && st.ended == nil {
			return st
		}
	}
	return nil
}

func (f *fakeDB) CountRecentGroups(_ context.Context, arg dbgen.CountRecentGroupsParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("CountRecentGroups"); err != nil {
		return 0, err
	}
	var n int64
	for _, g := range f.groups {
		if g.route == arg.RouteID && g.created.After(arg.Since) {
			n++
		}
	}
	return n, nil
}

func (f *fakeDB) JoinStorm(_ context.Context, arg dbgen.JoinStormParams) (dbgen.JoinStormRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("JoinStorm"); err != nil {
		return dbgen.JoinStormRow{}, err
	}
	st := f.activeStorm(arg.RouteID)
	if st == nil {
		return dbgen.JoinStormRow{}, pgx.ErrNoRows
	}
	st.alertGroupCount++
	if arg.Urgent {
		st.urgentCount++
	}
	return dbgen.JoinStormRow{ID: st.id, StartedAt: st.started, AlertGroupCount: st.alertGroupCount,
		UrgentCount: st.urgentCount}, nil
}

func (f *fakeDB) StartStorm(_ context.Context, arg dbgen.StartStormParams) (dbgen.StartStormRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("StartStorm"); err != nil {
		return dbgen.StartStormRow{}, err
	}
	if f.activeStorm(arg.RouteID) != nil {
		return dbgen.StartStormRow{}, pgx.ErrNoRows
	}
	st := &fakeStorm{id: f.id(), route: arg.RouteID, started: arg.Now, alertGroupCount: 1}
	if arg.Urgent {
		st.urgentCount = 1
	}
	f.storms[st.id] = st
	return dbgen.StartStormRow{ID: st.id, StartedAt: st.started, AlertGroupCount: 1, UrgentCount: st.urgentCount},
		nil
}

func (f *fakeDB) SetStormTimer(_ context.Context, arg dbgen.SetStormTimerParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("SetStormTimer"); err != nil {
		return err
	}
	f.timers[arg.StormID] = arg.Deadline
	return nil
}

func (f *fakeDB) EnsureStormSummary(_ context.Context, arg dbgen.EnsureStormSummaryParams) (
	dbgen.EnsureStormSummaryRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("EnsureStormSummary"); err != nil {
		return dbgen.EnsureStormSummaryRow{}, err
	}
	for _, d := range f.deliveries {
		if d.storm == arg.StormID.Int64 && d.dest == arg.DestinationID {
			return dbgen.EnsureStormSummaryRow{ID: d.id, DesiredHash: d.hash}, nil
		}
	}
	d := &fakeDelivery{id: f.id(), dest: arg.DestinationID, storm: arg.StormID.Int64, state: "pending",
		loud: pgtype.Bool{Bool: true, Valid: true}, next: arg.Now, threadState: "none", updated: arg.Now}
	f.deliveries = append(f.deliveries, d)
	return dbgen.EnsureStormSummaryRow{ID: d.id}, nil
}

func (f *fakeDB) ListStormSummaries(_ context.Context, arg dbgen.ListStormSummariesParams) (
	[]dbgen.ListStormSummariesRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListStormSummaries"); err != nil {
		return nil, err
	}
	var out []dbgen.ListStormSummariesRow
	for _, d := range f.deliveries {
		if d.storm == arg.StormID {
			out = append(out, dbgen.ListStormSummariesRow{ID: d.id, DesiredHash: d.hash, DestinationID: d.dest,
				DestinationType: f.dests[d.dest].typ})
		}
	}
	return out, nil
}

func (f *fakeDB) ReleaseHeld(_ context.Context, arg dbgen.ReleaseHeldParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ReleaseHeld"); err != nil {
		return err
	}
	if d := f.delivery(arg.ID); d.heldBy != 0 {
		d.heldBy, d.next, d.updated = 0, arg.Now, arg.Now
	}
	return nil
}

func (f *fakeDB) LockStorm(_ context.Context, arg dbgen.LockStormParams) (dbgen.LockStormRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("LockStorm"); err != nil {
		return dbgen.LockStormRow{}, err
	}
	st := f.storms[arg.ID]
	if st == nil || st.ended != nil {
		return dbgen.LockStormRow{}, pgx.ErrNoRows
	}
	r := f.routes[st.route]
	return dbgen.LockStormRow{ID: st.id, RouteID: st.route, StartedAt: st.started, CalmSince: tz(st.calmSince),
		AlertGroupCount: st.alertGroupCount, UrgentCount: st.urgentCount, RoutePublicID: r.publicID,
		RouteName: r.name, StormThreshold: r.threshold}, nil
}

func (f *fakeDB) GetStormRankTime(_ context.Context, arg dbgen.GetStormRankTimeParams) (time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetStormRankTime"); err != nil {
		return time.Time{}, err
	}
	var recent []*fakeGroup
	for _, g := range f.groups {
		if g.route == arg.RouteID && g.created.After(arg.Floor) {
			recent = append(recent, g)
		}
	}
	slices.SortFunc(recent, func(a, b *fakeGroup) int {
		return cmp.Or(b.created.Compare(a.created), cmp.Compare(b.id, a.id))
	})
	if int64(len(recent)) < arg.Rank {
		return time.Time{}, pgx.ErrNoRows
	}
	return recent[arg.Rank-1].created, nil
}

func (f *fakeDB) SetStormCalmSince(_ context.Context, arg dbgen.SetStormCalmSinceParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("SetStormCalmSince"); err != nil {
		return err
	}
	f.storms[arg.ID].calmSince = nil
	if arg.CalmSince.Valid {
		f.storms[arg.ID].calmSince = at(arg.CalmSince.Time)
	}
	return nil
}

// held are the pending deliveries a Storm holds, with whether their Alert Group is resolved.
func (f *fakeDB) held(storm int64, each func(d *fakeDelivery, resolved bool)) {
	for _, d := range f.deliveries {
		if d.heldBy == storm {
			each(d, f.groups[d.group].status == "resolved")
		}
	}
}

func (f *fakeDB) CountHeldOpen(_ context.Context, arg dbgen.CountHeldOpenParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("CountHeldOpen"); err != nil {
		return 0, err
	}
	open := map[int64]bool{}
	f.held(arg.StormID, func(d *fakeDelivery, resolved bool) {
		if d.state == "pending" && !resolved {
			open[d.group] = true
		}
	})
	return int64(len(open)), nil
}

func (f *fakeDB) ReleaseHeldOpen(_ context.Context, arg dbgen.ReleaseHeldOpenParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ReleaseHeldOpen"); err != nil {
		return err
	}
	f.held(arg.StormID, func(d *fakeDelivery, resolved bool) {
		if !resolved {
			d.heldBy, d.loud, d.next, d.updated = 0, pgtype.Bool{Bool: arg.Loud, Valid: true}, arg.Now, arg.Now
		}
	})
	return nil
}

func (f *fakeDB) WithholdHeldResolved(_ context.Context, arg dbgen.WithholdHeldResolvedParams) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("WithholdHeldResolved"); err != nil {
		return nil, err
	}
	var out []string
	f.held(arg.StormID, func(d *fakeDelivery, resolved bool) {
		if resolved && d.state == "pending" {
			d.heldBy, d.state, d.updated = 0, "withheld", arg.Now
			out = append(out, f.groups[d.group].publicID)
		}
	})
	return out, nil
}

func (f *fakeDB) EndStorm(_ context.Context, arg dbgen.EndStormParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("EndStorm"); err != nil {
		return err
	}
	st := f.storms[arg.ID]
	st.ended = at(arg.Now)
	if st.calmSince == nil && arg.CalmSince.Valid {
		st.calmSince = at(arg.CalmSince.Time)
	}
	return nil
}

func (f *fakeDB) QuietStormSummaries(_ context.Context, arg dbgen.QuietStormSummariesParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("QuietStormSummaries"); err != nil {
		return err
	}
	for _, d := range f.deliveries {
		if d.storm == arg.StormID && d.publishedAt == nil {
			d.loud, d.updated = pgtype.Bool{Bool: false, Valid: true}, arg.Now
		}
	}
	return nil
}

func (f *fakeDB) GetActiveStorm(_ context.Context, arg dbgen.GetActiveStormParams) (dbgen.GetActiveStormRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetActiveStorm"); err != nil {
		return dbgen.GetActiveStormRow{}, err
	}
	st := f.activeStorm(arg.RouteID)
	if st == nil {
		return dbgen.GetActiveStormRow{}, pgx.ErrNoRows
	}
	return dbgen.GetActiveStormRow{ID: st.id, StartedAt: st.started, AlertGroupCount: st.alertGroupCount,
		UrgentCount: st.urgentCount}, nil
}

func (f *fakeDB) RetireStormSummary(_ context.Context, arg dbgen.RetireStormSummaryParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("RetireStormSummary"); err != nil {
		return err
	}
	st := f.activeStorm(arg.RouteID)
	for _, d := range f.deliveries {
		if st != nil && d.storm == st.id && d.dest == arg.DestinationID && !terminal(d) {
			d.state = "retired"
			if d.publishedAt == nil {
				d.state = "withheld"
			}
			d.updated = arg.Now
		}
	}
	return nil
}

func (f *fakeDB) ListStormActivity(context.Context, int64) ([]dbgen.ListStormActivityRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListStormActivity"); err != nil {
		return nil, err
	}
	var out []dbgen.ListStormActivityRow
	for id, r := range f.routes {
		out = append(out, dbgen.ListStormActivityRow{PublicID: r.publicID, Active: f.activeStorm(id) != nil})
	}
	return out, nil
}

// view is the Alert Group id of the fake in a status, as the dispatcher hands it over; a resolution is dated now.
func (e *env) view(id int64, status groups.Status) *groups.Group {
	g := e.db.groups[id]
	g.status = string(status)
	if status == groups.StatusResolved {
		g.resolvedAt = at(e.business.Now())
	}
	return &groups.Group{ID: id, PublicID: g.publicID, Number: g.number, RouteID: g.route, Title: g.title,
		Status: status, Urgent: g.urgent}
}

// addGroup creates the firing Alert Group id of the Route now, Urgent or not, and renders its creation.
func (e *env) addGroup(t *testing.T, id int64, urgent bool) {
	t.Helper()
	e.db.groups[id] = &fakeGroup{id: id, publicID: fmt.Sprintf("AGAAAAAAAA%04d", id), number: id, title: "g",
		status: "firing", urgent: urgent, route: routeID, created: e.business.Now()}
	e.enqueue(t, e.view(id, groups.StatusFiring), groups.System, created())
}

// stormEnv is an env without the Alert Group of the fixtures, whose limiter never runs dry.
func stormEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	delete(e.db.groups, groupID)
	e.db.dests[destMM].limit = 6000
	return e
}

// onlyStorm is the one Storm of the fake.
func (e *env) onlyStorm(t *testing.T) *fakeStorm {
	t.Helper()
	if len(e.db.storms) != 1 {
		t.Fatalf("%d storms", len(e.db.storms))
	}
	for _, st := range e.db.storms {
		return st
	}
	return nil
}

// calmCheck fires the calm check of the Storm at its deadline, as the timer worker does, and runs what it returns.
func (e *env) calmCheck(t *testing.T, st *fakeStorm) {
	t.Helper()
	e.business.Set(e.db.timers[st.id])
	after, err := e.svc.CheckStormCalm(t.Context(), nil, st.id)
	if err != nil {
		t.Fatal(err)
	}
	if after != nil {
		after(t.Context())
	}
}

// TestStorm is C-11.FR-6, FR-17 and AC-3 through the recorder: with 30 new Alert Groups within a minute on a Route
// with route.storm_threshold 20, the first 20 get Root messages; the 21st starts a Storm, so the Destination gets one
// Storm summary, Loud with new_alert_group, kept current with Quiet edits, and of the last 10 only the 3 Urgent ones
// are published; after delivery.storm_calm_period at or below the threshold the summary gets its final state in one
// Quiet edit, the 5 of the last 10 still open are published as Quiet new messages, and the 2 resolved are never
// published; muster_storm_active shows the Storm while it lasts.
func TestStorm(t *testing.T) {
	e := stormEnv(t)
	urgent := map[int64]bool{23: true, 26: true, 29: true}
	for i := int64(1); i <= 30; i++ {
		e.business.Set(business0.Add(time.Duration(i) * time.Second))
		e.addGroup(t, 1000+i, urgent[i])
		e.round(t)
		if i == 20 && len(e.db.storms) != 0 {
			t.Fatal("a storm at the threshold")
		}
	}
	st := e.onlyStorm(t)
	start := business0.Add(21 * time.Second)
	if !st.started.Equal(start) || st.alertGroupCount != 10 || st.urgentCount != 3 || st.ended != nil {
		t.Fatalf("storm %+v", st)
	}
	if !e.db.timers[st.id].Equal(start.Add(delivery.StormCalmPeriod)) || e.db.timersNotified != 1 ||
		!slices.Contains(e.db.hints, db.Hint{OrgID: orgID, Type: "route", ID: "RTAAAAAAAAAAA1"}) ||
		!strings.Contains(e.log.String(), `"event":"storm_started","route":"RTAAAAAAAAAAA1","alert_groups":1,"urgent":0`) {
		t.Errorf("start: timer %v, %d wakes, hints %+v, log %s", e.db.timers, e.db.timersNotified, e.db.hints, e.log)
	}
	var loud, summaries, edits int
	for _, c := range e.rec.Calls() {
		switch {
		case c.Method == deliverytest.MethodPublish && strings.HasPrefix(textOf(c), "Storm on Route payments"):
			summaries++
			if c.Loudness != groups.Loud || !slices.Equal(c.Mentions, []groups.Mention{groups.MentionNewAlertGroup}) ||
				!strings.Contains(textOf(c), "1 new Alert Groups, 0 Urgent") {
				t.Errorf("summary %+v", c)
			}
		case c.Method == deliverytest.MethodPublish:
			loud++
			if c.Loudness != groups.Loud {
				t.Errorf("publication %+v", c)
			}
		case c.Method == deliverytest.MethodUpdate:
			edits++
			if c.Loudness != groups.Quiet || len(c.Mentions) != 0 {
				t.Errorf("summary update %+v", c)
			}
		}
	}
	last := e.rec.Calls()[len(e.rec.Calls())-1]
	if loud != 23 || summaries != 1 || edits != 9 ||
		!strings.Contains(textOf(last), "10 new Alert Groups, 3 Urgent") {
		t.Errorf("publications %d, summaries %d, edits %d, last %q", loud, summaries, edits, textOf(last))
	}
	var held []*fakeDelivery
	for _, d := range e.db.deliveries {
		if d.heldBy == st.id {
			held = append(held, d)
		}
	}
	if len(held) != 7 || slices.ContainsFunc(held, func(d *fakeDelivery) bool { return d.messageID != nil }) {
		t.Errorf("held %d", len(held))
	}
	i := slices.IndexFunc(e.db.events, func(ev dbgen.InsertDeliveryEventParams) bool { return ev.Kind == "storm_summary" })
	if ev := e.db.events[i]; ev.StormID.Int64 != st.id || ev.AlertGroupID.Valid || ev.Loudness != "loud" ||
		!slices.Equal(ev.Mentions, []string{"new_alert_group"}) {
		t.Errorf("event %+v", ev)
	}
	if err := errors.Join(e.svc.ExportStorms(t.Context()), e.svc.ExportQueue(t.Context())); err != nil {
		t.Fatal(err)
	}
	if out := scrape(t); !strings.Contains(out, `muster_storm_active{route="RTAAAAAAAAAAA1"} 1`) ||
		!strings.Contains(out, `muster_delivery_queue{destination="DSAAAAAAAAAA11"} 0`) {
		t.Errorf("gauges %s", out)
	}

	// Two of the held Alert Groups resolve: their Thread replies are dropped and nothing is published.
	for _, id := range []int64{1021, 1022} {
		e.enqueue(t, e.view(id, groups.StatusResolved), groups.System,
			groups.Recorded{Seq: 2, Event: groups.EventResolved, Loudness: groups.Quiet})
	}
	if r := e.db.replies; len(r) != 2 || r[0].state != "dropped" || r[1].state != "dropped" {
		t.Errorf("replies %+v", r)
	}
	calls := len(e.rec.Calls())
	e.round(t)
	if len(e.rec.Calls()) != calls {
		t.Errorf("held alert groups were sent: %+v", e.rec.Calls()[calls:])
	}

	// The first calm check finds the count at the threshold since one minute after the 10th Alert Group: it moves to
	// the end of the calm period from then.
	e.calmCheck(t, st)
	calm := business0.Add(70 * time.Second)
	if st.ended != nil || st.calmSince == nil || !st.calmSince.Equal(calm) ||
		!e.db.timers[st.id].Equal(calm.Add(delivery.StormCalmPeriod)) {
		t.Fatalf("pushed back %+v, timer %v", st, e.db.timers[st.id])
	}
	e.calmCheck(t, st)
	if st.ended == nil || !strings.Contains(e.log.String(), `"event":"storm_ended","route":"RTAAAAAAAAAAA1",`+
		`"alert_groups":10,"urgent":3`) {
		t.Fatalf("not ended %+v, log %s", st, e.log)
	}
	e.rec.Reset()
	e.round(t)
	var quiet []deliverytest.Call
	var final []deliverytest.Call
	for _, c := range e.rec.Calls() {
		switch c.Method {
		case deliverytest.MethodPublish:
			quiet = append(quiet, c)
		case deliverytest.MethodUpdate:
			final = append(final, c)
		}
	}
	if len(final) != 1 || final[0].Loudness != groups.Quiet || textOf(final[0]) !=
		"Storm over: 5 Alert Groups still open" {
		t.Errorf("final summary %+v", final)
	}
	if len(quiet) != 5 || slices.ContainsFunc(quiet, func(c deliverytest.Call) bool {
		return c.Loudness != groups.Quiet || len(c.Mentions) != 0
	}) {
		t.Errorf("gradual publications %+v", quiet)
	}
	for _, d := range held {
		want := "delivered"
		if d.group == 1021 || d.group == 1022 {
			want = "withheld"
		}
		if d.state != want || d.heldBy != 0 {
			t.Errorf("held %d: %s", d.group, d.state)
		}
	}
	if err := e.svc.ExportStorms(t.Context()); err != nil ||
		!strings.Contains(scrape(t), `muster_storm_active{route="RTAAAAAAAAAAA1"} 0`) {
		t.Errorf("gauge after the storm: %v", err)
	}
	if after, err := e.svc.CheckStormCalm(t.Context(), nil, st.id); after != nil || err != nil {
		t.Errorf("an ended storm = %v", err)
	}
	delete(e.db.routes, routeID)
	if err := e.svc.ExportStorms(t.Context()); err != nil ||
		strings.Contains(scrape(t), `muster_storm_active{route="RTAAAAAAAAAAA1"}`) {
		t.Errorf("a deleted route keeps its series: %v", err)
	}
}

// TestStormDoesNotCalm is C-11.FR-6: while new Alert Groups keep the count of the last minute above the threshold,
// the calm check moves on and the Storm stays, calm_since unset; an Alert Group the Storm holds that becomes Urgent is
// published at once; once they stop the Storm ends delivery.storm_calm_period after the count fell to the threshold.
func TestStormDoesNotCalm(t *testing.T) {
	e := stormEnv(t)
	r := e.db.routes[routeID]
	r.threshold = 2
	e.db.routes[routeID] = r
	id := int64(2000)
	add := func() {
		id++
		e.addGroup(t, id, false)
	}
	for range 3 {
		add()
		e.business.Advance(20 * time.Second)
	}
	st := e.onlyStorm(t)
	for range 30 {
		add()
		if !e.business.Now().Before(e.db.timers[st.id]) {
			e.calmCheck(t, st)
			if st.ended != nil || st.calmSince != nil {
				t.Fatalf("calmed while it raged %+v", st)
			}
		}
		e.business.Advance(20 * time.Second)
	}
	lastAt := e.db.groups[id].created

	// A held Alert Group that becomes Urgent is published at once, Loud.
	e.round(t)
	e.rec.Reset()
	g := e.view(id, groups.StatusFiring)
	g.Urgent = true
	e.db.groups[id].urgent = true
	e.enqueue(t, g, groups.System, groups.Recorded{Seq: 2, Event: groups.EventUrgencyRaised,
		Variant: groups.VariantRemovesNone, Loudness: groups.Quiet})
	e.round(t)
	if p := e.publications(); len(p) != 1 || p[0].Loudness != groups.Loud || !strings.HasPrefix(textOf(p[0]),
		fmt.Sprintf("#%d ", id)) {
		t.Errorf("released %+v", p)
	}

	for st.ended == nil {
		e.calmCheck(t, st)
	}
	if want := lastAt.Add(20*time.Second + delivery.StormCalmPeriod); !st.ended.Equal(want) {
		t.Errorf("ended at %v, want %v", st.ended, want)
	}
}

// TestStormRace: a Storm another transaction started first is joined; a Storm whose Alert Groups left the Route ends
// one calm period after it started.
func TestStormRace(t *testing.T) {
	e := stormEnv(t)
	r := e.db.routes[routeID]
	r.threshold = 1
	e.db.routes[routeID] = r
	e.addGroup(t, 3001, false)
	e.db.before["StartStorm"] = func() {
		e.db.storms[99] = &fakeStorm{id: 99, route: routeID, started: e.business.Now(), alertGroupCount: 4}
	}
	e.addGroup(t, 3002, true)
	st := e.onlyStorm(t)
	if st.id != 99 || st.alertGroupCount != 5 || st.urgentCount != 1 || e.db.timersNotified != 0 {
		t.Fatalf("joined %+v", st)
	}
	delete(e.db.groups, 3001)
	delete(e.db.groups, 3002)
	e.db.timers[99] = e.business.Now().Add(time.Minute)
	e.calmCheck(t, st)
	if st.ended != nil || !e.db.timers[99].Equal(st.started.Add(delivery.StormCalmPeriod)) {
		t.Fatalf("pushed %+v %v", st, e.db.timers[99])
	}
	e.calmCheck(t, st)
	if st.ended == nil || !st.calmSince.Equal(st.started) {
		t.Errorf("ended %+v", st)
	}
}

// TestStormFailures: a failed query of a Storm fails the change that needed it.
func TestStormFailures(t *testing.T) {
	start := func(t *testing.T) *env {
		e := stormEnv(t)
		r := e.db.routes[routeID]
		r.threshold = 1
		e.db.routes[routeID] = r
		e.addGroup(t, 4001, false)
		return e
	}
	for _, q := range []string{"JoinStorm", "CountRecentGroups", "StartStorm", "SetStormTimer", "NotifyDelivery",
		"Notify", "EnsureStormSummary", "SetDesired"} {
		e := start(t)
		e.db.fail[q] = errBoom
		e.db.groups[4002] = &fakeGroup{id: 4002, publicID: "AGAAAAAAAA4002", number: 4002, title: "g",
			status: "firing", route: routeID, created: e.business.Now()}
		err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: e.view(4002, groups.StatusFiring),
			Actor: groups.System, Events: []groups.Recorded{created()}})
		if !errors.Is(err, errBoom) {
			t.Errorf("start with %s failing = %v", q, err)
		}
	}
	e := start(t)
	e.db.fail["JoinStorm"] = errBoom
	e.db.before["StartStorm"] = func() { e.db.storms[98] = &fakeStorm{id: 98, route: routeID} }
	e.addGroupErr(t, 4003)
	for _, q := range []string{"LockStorm", "GetStormRankTime", "SetStormCalmSince", "SetStormTimer", "CountHeldOpen",
		"ListStormSummaries", "SetDesired", "ReleaseHeldOpen", "WithholdHeldResolved", "EndStorm", "NotifyDelivery",
		"Notify"} {
		e := start(t)
		e.addGroup(t, 4002, false)
		e.round(t)
		st := e.onlyStorm(t)
		if q == "SetStormCalmSince" || q == "SetStormTimer" {
			e.business.Set(st.started)
		} else {
			e.business.Set(st.started.Add(time.Hour))
		}
		e.db.fail[q] = errBoom
		if _, err := e.svc.CheckStormCalm(t.Context(), nil, st.id); !errors.Is(err, errBoom) {
			t.Errorf("calm check with %s failing = %v", q, err)
		}
	}
	e = stormEnv(t)
	e.db.fail["ListStormActivity"] = errBoom
	if err := e.svc.ExportStorms(t.Context()); !errors.Is(err, errBoom) {
		t.Errorf("export = %v", err)
	}
	e = stormEnv(t)
	e.addGroup(t, 4004, false)
	d := e.only(t)
	d.heldBy = 77
	e.db.fail["ReleaseHeld"] = errBoom
	g := e.view(4004, groups.StatusFiring)
	g.Urgent = true
	if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: g, Actor: groups.System}); !errors.Is(err,
		errBoom) {
		t.Errorf("release = %v", err)
	}
}

// addGroupErr renders the creation of a new Alert Group and expects the failure of the join.
func (e *env) addGroupErr(t *testing.T, id int64) {
	t.Helper()
	e.db.groups[id] = &fakeGroup{id: id, publicID: fmt.Sprintf("AGAAAAAAAA%04d", id), number: id, title: "g",
		status: "firing", route: routeID, created: e.business.Now()}
	err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: e.view(id, groups.StatusFiring),
		Actor: groups.System, Events: []groups.Recorded{created()}})
	if !errors.Is(err, errBoom) {
		t.Errorf("join after a race = %v", err)
	}
}

// TestStormStartedAfterCommit: the storm_started line of the Storm that a new Alert Group starts is logged once the
// dispatcher's transaction committed, never inside it, with the Storm's own counts as storm_ended has them.
func TestStormStartedAfterCommit(t *testing.T) {
	e := stormEnv(t)
	r := e.db.routes[routeID]
	r.threshold = 1
	e.db.routes[routeID] = r
	e.addGroup(t, 6001, false)
	e.db.groups[6002] = &fakeGroup{id: 6002, publicID: "AGAAAAAAAA6002", number: 6002, title: "g",
		status: "firing", urgent: true, route: routeID, created: e.business.Now()}
	var committed []func(context.Context)
	if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: e.view(6002, groups.StatusFiring),
		Actor: groups.System, Events: []groups.Recorded{created()},
		After: func(f func(context.Context)) { committed = append(committed, f) }}); err != nil {
		t.Fatal(err)
	}
	if len(e.db.storms) != 1 || strings.Contains(e.log.String(), "storm_started") || len(committed) != 1 {
		t.Fatalf("logged inside the transaction: %s (%d after)", e.log, len(committed))
	}
	committed[0](t.Context())
	if !strings.Contains(e.log.String(), `"event":"storm_started","route":"RTAAAAAAAAAAA1","alert_groups":1,`+
		`"urgent":1`) {
		t.Errorf("log %s", e.log)
	}
}

// TestStormSummaryAfterEndIsQuiet: a Storm summary first published after its Storm ended announces nothing new: its
// Publication is Quiet.
func TestStormSummaryAfterEndIsQuiet(t *testing.T) {
	e := stormEnv(t)
	r := e.db.routes[routeID]
	r.threshold = 1
	e.db.routes[routeID] = r
	e.addGroup(t, 7001, false)
	e.addGroup(t, 7002, false)
	st := e.onlyStorm(t)
	for st.ended == nil {
		e.calmCheck(t, st)
	}
	e.round(t)
	var summary []deliverytest.Call
	for _, c := range e.publications() {
		if strings.HasPrefix(textOf(c), "Storm over") {
			summary = append(summary, c)
		}
	}
	if len(summary) != 1 || summary[0].Loudness != groups.Quiet || len(summary[0].Mentions) != 0 {
		t.Errorf("summary %+v", summary)
	}
	i := slices.IndexFunc(e.db.events, func(ev dbgen.InsertDeliveryEventParams) bool { return ev.Kind == "storm_summary" })
	if i < 0 || e.db.events[i].Loudness != "quiet" {
		t.Errorf("events %v", e.eventKinds())
	}
	e2 := stormEnv(t)
	e2.db.fail["QuietStormSummaries"] = errBoom
	r2 := e2.db.routes[routeID]
	r2.threshold = 1
	e2.db.routes[routeID] = r2
	e2.addGroup(t, 7001, false)
	e2.addGroup(t, 7002, false)
	st2 := e2.onlyStorm(t)
	e2.business.Set(st2.started.Add(time.Hour))
	if _, err := e2.svc.CheckStormCalm(t.Context(), nil, st2.id); !errors.Is(err, errBoom) {
		t.Errorf("calm check with QuietStormSummaries failing = %v", err)
	}
}

// TestStormDestinationJoinsAndLeaves is C-11.FR-6 with C-11.FR-14: a Destination added to the Route during a Storm
// gets its Storm summary, Loud, holds the Alert Groups the Storm holds and publishes the others Quietly; one removed
// during the Storm retires its summary, which the Storm's later changes and its end no longer edit.
func TestStormDestinationJoinsAndLeaves(t *testing.T) {
	e := stormEnv(t)
	e.db.dests[destWH].limit = 6000
	r := e.db.routes[routeID]
	r.threshold = 2
	e.db.routes[routeID] = r
	e.addGroup(t, 8001, false) // before the Storm
	e.business.Advance(time.Second)
	e.addGroup(t, 8002, false) // before the Storm
	e.business.Advance(time.Second)
	e.addGroup(t, 8003, false) // starts it: held
	e.addGroup(t, 8004, true)  // Urgent: published
	e.addGroup(t, 8005, false) // held
	e.round(t)
	st := e.onlyStorm(t)
	e.rec.Reset()

	e.db.routeDests[routeID] = []int64{destMM, destWH}
	if err := e.svc.RouteDestinationsChanged(t.Context(), nil, routeID, []int64{destWH}, nil); err != nil {
		t.Fatal(err)
	}
	e.round(t)
	wh := e.db.dests[destWH].publicID
	var quiet []string
	var summary *deliverytest.Call
	for _, c := range e.rec.Calls() {
		switch {
		case c.Destination.PublicID != wh || c.Method != deliverytest.MethodPublish:
			t.Errorf("call %s to %s", c.Method, c.Destination.PublicID)
		case strings.HasPrefix(textOf(c), "Storm on Route"):
			summary = &c
		case c.Loudness == groups.Quiet:
			quiet = append(quiet, strings.Fields(textOf(c))[0])
		default:
			t.Errorf("loud publication %+v", c)
		}
	}
	slices.Sort(quiet)
	if summary == nil || summary.Loudness != groups.Loud || !strings.Contains(textOf(*summary), "3 new Alert Groups") ||
		!slices.Equal(quiet, []string{"#8001", "#8002", "#8004"}) {
		t.Errorf("summary %+v, quiet %v", summary, quiet)
	}
	for _, d := range e.db.deliveries {
		held := d.dest == destWH && (d.group == 8003 || d.group == 8005)
		if d.dest == destWH && d.storm == 0 && (d.heldBy == st.id) != held {
			t.Errorf("held in the added destination %+v", d)
		}
	}

	// It leaves during the Storm: final edits of its Root messages, its summary retired, nothing more there.
	e.rec.Reset()
	e.db.routeDests[routeID] = []int64{destMM}
	if err := e.svc.RouteDestinationsChanged(t.Context(), nil, routeID, nil, []int64{destWH}); err != nil {
		t.Fatal(err)
	}
	var whSummary *fakeDelivery
	for _, d := range e.db.deliveries {
		if d.storm == st.id && d.dest == destWH {
			whSummary = d
		}
	}
	if whSummary == nil || whSummary.state != "retired" {
		t.Fatalf("summary in the destination that left %+v", whSummary)
	}
	e.addGroup(t, 8006, false)
	for st.ended == nil {
		e.calmCheck(t, st)
	}
	e.round(t)
	for _, c := range e.rec.Calls() {
		if c.Destination.PublicID == wh && !strings.Contains(textOf(c), "No longer updated here") {
			t.Errorf("a call to the destination that left %+v", c)
		}
	}
	if whSummary.state != "retired" {
		t.Errorf("summary revived %+v", whSummary)
	}
	for _, q := range []string{"GetActiveStorm", "RetireStormSummary"} {
		e.db.fail[q] = errBoom
		if err := e.svc.RouteDestinationsChanged(t.Context(), nil, routeID, []int64{destWH},
			[]int64{destMM}); !errors.Is(err, errBoom) {
			t.Errorf("%s = %v", q, err)
		}
		delete(e.db.fail, q)
	}
}

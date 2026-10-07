// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/ingest"
)

// TestGrouping is C-09.AC-6, C-09.FR-3, C-09.FR-1 and C-08.FR-4: Alerts with the same Group key values on one Route
// join one Alert Group, a missing label counting as the empty value; another cluster starts a second one; each new
// one takes the next #N and an opaque public_id, and the Alerts of one Snapshot are one `created` entry with their
// fingerprints and Static label conflicts.
func TestGrouping(t *testing.T) {
	h := newHarness(t)
	a := h.alert(2, "warning", map[string]string{"alertname": "DiskFull", "cluster": "a", "pod": "i1"},
		"summary", "Disk on i1 is full")
	b := h.alert(2, "warning", map[string]string{"alertname": "DiskFull", "cluster": "a", "pod": "i2"})
	c := h.alert(2, "warning", map[string]string{"alertname": "DiskFull", "cluster": "b", "pod": "j1"})
	h.db.alerts[b].StaticLabelConflicts = []string{"team"}
	out := h.changes(t, ingest.ChangeFired, a, b, c)
	g1, g2 := h.groupOf(t, a), h.groupOf(t, c)
	if h.groupOf(t, b) != g1 || g1 == g2 {
		t.Fatalf("groups %d %d %d", g1.ID, h.groupOf(t, b).ID, g2.ID)
	}
	if g1.Number != 1 || g2.Number != 2 || !strings.HasPrefix(g1.PublicID, "AG") || len(g1.PublicID) != 14 ||
		g1.Title != "DiskFull" || g1.Summary.String != "Disk on i1 is full" || g1.Status != "firing" ||
		g1.SeverityLevel != "warning" || g1.Urgent || g1.FiringAlertCount != 2 || g1.RouteID != 2 ||
		string(g1.GroupKeyValues) != `{"alertname":"DiskFull","cluster":"a"}` || len(g1.GroupKeySha256) != 32 {
		t.Errorf("alert group %+v", g1)
	}
	if string(g1.CommonLabels) != `{"alertname":"DiskFull","cluster":"a"}` || !slices.Equal(g1.IntegrationIds, []int64{5}) {
		t.Errorf("common labels %s, integrations %v", g1.CommonLabels, g1.IntegrationIds)
	}
	e := h.last(t, g1)
	if h.events(g1)[0] != "created" || len(h.entriesOf(g1)) != 1 || !slices.Equal(e.Fingerprints,
		[]string{fingerprintOf(a), fingerprintOf(b)}) || !slices.Equal(e.LabelConflicts, []string{"team"}) ||
		e.ToStatus.String != "firing" || e.FromStatus.Valid {
		t.Errorf("created %+v", e.InsertTimelineEntryParams)
	}
	if !slices.Equal(out.AlertGroups, []int64{1, 2}) || out.IDs != nil {
		t.Errorf("routed %+v", out)
	}
	if !strings.Contains(h.log.String(), `"event":"alert_group_status_changed","group":"#1","route":"RTDBAAAAAAAAAA",`+
		`"from":"","to":"firing","reason":"created","transport":"system"`) {
		t.Errorf("log: %s", h.log.String())
	}
	// A missing label counts as the empty value.
	d := h.alert(2, "warning", map[string]string{"alertname": "DiskFull", "pod": "k1"})
	e2 := h.alert(2, "warning", map[string]string{"alertname": "DiskFull", "cluster": "", "pod": "k2"})
	h.changes(t, ingest.ChangeFired, d)
	h.changes(t, ingest.ChangeFired, e2)
	if g := h.groupOf(t, d); h.groupOf(t, e2) != g || g.Number != 3 || h.last(t, g).Event.String != "alerts_added" {
		t.Errorf("a missing label grouped apart")
	}
	if *h.db.counter != 3 || !h.db.notifiedNone() {
		t.Errorf("counter %d", *h.db.counter)
	}
}

func (f *fakeDB) notifiedNone() bool { return f.notified == 0 }

// TestTitleAndSummary is C-09.FR-23 and C-09.AC-20: in a Route whose Group key lacks alertname, an Alert with another
// alertname switches the title once to the Group key values; later Alerts and resolutions leave it, and the summary
// is the first summary annotation, set once.
func TestTitleAndSummary(t *testing.T) {
	h := newHarness(t)
	a := h.alert(3, "info", map[string]string{"alertname": "LinkDown", "cluster": "x", "if": "eth0"},
		"summary", "eth0 is down")
	h.changes(t, ingest.ChangeFired, a)
	g := h.groupOf(t, a)
	if g.Title != "LinkDown" || g.TitleFromGroupKey {
		t.Fatalf("title %q", g.Title)
	}
	b := h.alert(3, "info", map[string]string{"alertname": "HighLatency", "cluster": "x", "if": "eth1"},
		"summary", "latency")
	h.changes(t, ingest.ChangeFired, b)
	if g.Title != "cluster=x" || !g.TitleFromGroupKey || g.Summary.String != "eth0 is down" {
		t.Errorf("after the switch %q %v %q", g.Title, g.TitleFromGroupKey, g.Summary.String)
	}
	c := h.alert(3, "info", map[string]string{"alertname": "Third", "cluster": "x", "if": "eth2"})
	h.changes(t, ingest.ChangeFired, c)
	h.resolve(t, a, b)
	if g.Title != "cluster=x" || g.Summary.String != "eth0 is down" {
		t.Errorf("after later alerts %q %q", g.Title, g.Summary.String)
	}
	if string(g.CommonLabels) != `{"cluster":"x"}` {
		t.Errorf("common labels %s", g.CommonLabels)
	}
	// Without alertname the title is the Group key values, which count as the switch; without either, the
	// fingerprint. A summary arrives with the first Alert that has one.
	n := h.alert(3, "info", map[string]string{"cluster": "y"})
	h.changes(t, ingest.ChangeFired, n)
	if g := h.groupOf(t, n); g.Title != "cluster=y" || !g.TitleFromGroupKey || g.Summary.Valid {
		t.Errorf("no alertname: %q %v", g.Title, g.TitleFromGroupKey)
	}
	m := h.alert(3, "info", map[string]string{"cluster": "y"}, "summary", "later")
	h.changes(t, ingest.ChangeFired, m)
	if g := h.groupOf(t, n); g.Summary.String != "later" {
		t.Errorf("summary %q", g.Summary.String)
	}
	h.db.routes[3].GroupKey = []string{}
	f := h.alert(3, "info", map[string]string{"x": "1"})
	h.changes(t, ingest.ChangeFired, f)
	if g := h.groupOf(t, f); g.Title != fingerprintOf(f) {
		t.Errorf("no key: %q", g.Title)
	}
	o := h.alert(3, "info", map[string]string{"alertname": "Other"})
	h.changes(t, ingest.ChangeFired, o)
	if g := h.groupOf(t, f); g.Title != fingerprintOf(f) || g.TitleFromGroupKey {
		t.Errorf("an empty key switched the title to %q", g.Title)
	}
}

// TestFiringAlertsStay is C-08.FR-8 and C-09.AC-7: an Alert that fires in an open Alert Group is not grouped again,
// whatever routing says now; a resolved Alert that fired at once again is not grouped either.
func TestFiringAlertsStay(t *testing.T) {
	h := newHarness(t)
	a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"})
	h.changes(t, ingest.ChangeFired, a)
	g := h.groupOf(t, a)
	h.db.alerts[a].RouteID = i8(3)
	out := h.changes(t, ingest.ChangeFired, a)
	if h.groupOf(t, a) != g || len(h.db.groups) != 1 || out.AlertGroups != nil {
		t.Errorf("grouped again: %d groups, %+v", len(h.db.groups), out)
	}
	r := h.alert(2, "warning", map[string]string{"alertname": "B", "cluster": "x"})
	h.db.alerts[r].Status = "resolved"
	h.changes(t, ingest.ChangeFired, r)
	if len(h.db.groups) != 1 {
		t.Error("a resolved alert was grouped")
	}
	// Changes of Alerts in no Alert Group, and of unknown Alerts, change nothing.
	h.changes(t, ingest.ChangeContinued, r, 999)
	if out := h.changes(t, ingest.ChangeFired); out.AlertGroups != nil {
		t.Errorf("no changes = %+v", out)
	}
}

// TestDeletedRoute is C-09.FR-19 and C-08.FR-9: grouping locks the Route FOR SHARE before it groups on it; an Alert
// whose Route was deleted meanwhile is grouped on the Default route and routing records that.
func TestDeletedRoute(t *testing.T) {
	h := newHarness(t)
	a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"})
	b := h.alert(1, "warning", map[string]string{"alertname": "A", "cluster": "x"})
	h.db.routes[2].deleted = true
	out := h.changes(t, ingest.ChangeFired, a, b)
	g := h.groupOf(t, a)
	if g.RouteID != 1 || h.groupOf(t, b) != g || string(g.GroupKeyValues) != `{"alertname":"A"}` {
		t.Errorf("alert group %+v", g)
	}
	if len(h.restamps) != 1 || !slices.Equal(h.restamps[0], []int64{a}) || !slices.Equal(out.IDs, []int64{1}) ||
		!slices.Equal(out.PublicIDs, []string{"RTDEFAAAAAAAAA"}) {
		t.Errorf("restamps %v, routed %+v", h.restamps, out)
	}
	if h.db.calls["LockRoutes"] != 1 || h.db.calls["LockDefaultRoute"] != 1 {
		t.Errorf("calls %v", h.db.calls)
	}
	// An Alert without a Route goes to the Default route too.
	c := h.alert(0, "", map[string]string{"alertname": "A", "cluster": "y"})
	h.changes(t, ingest.ChangeFired, c)
	if h.groupOf(t, c) != g || h.groupOf(t, c).SeverityLevel != "warning" {
		t.Error("an alert without a route")
	}
}

// TestLockOrder: the Routes, then the counter row when an Alert Group may become open, then the Alert Groups in id
// order, so that two transactions never wait for each other.
func TestLockOrder(t *testing.T) {
	h := newHarness(t)
	var ids []int64
	for _, cluster := range []string{"a", "b", "c"} {
		id := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": cluster})
		h.changes(t, ingest.ChangeFired, id)
		ids = append(ids, id)
	}
	h.resolve(t, ids[1])
	h.db.alerts[ids[1]].Status = "firing"
	h.db.calls = map[string]int{}
	h.db.locked = nil
	var order []string
	for _, q := range []string{"LockRoutes", "LockCounter", "LockGroups"} {
		h.db.before[q] = func() { order = append(order, q) }
	}
	x := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "c", "n": "2"})
	y := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "a", "n": "2"})
	z := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "new"})
	h.changes(t, ingest.ChangeFired, x, y, z)
	if !slices.Equal(order, []string{"LockRoutes", "LockCounter", "LockGroups"}) {
		t.Errorf("lock order %v", order)
	}
	last := h.db.locked[len(h.db.locked)-1]
	if !slices.IsSorted(last) || len(last) != 2 {
		t.Errorf("locked %v", last)
	}
	// Grouping takes the counter row also to join an open Alert Group; a Snapshot that only resolves does not.
	h.db.calls = map[string]int{}
	w := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "a", "n": "3"})
	h.changes(t, ingest.ChangeFired, w)
	if h.db.calls["LockCounter"] != 1 || h.db.calls["NextNumber"] != 0 {
		t.Errorf("calls %v", h.db.calls)
	}
	h.db.calls = map[string]int{}
	h.resolve(t, w)
	if h.db.calls["LockCounter"] != 0 || h.db.calls["LockRoutes"] != 0 {
		t.Errorf("a resolution took %v", h.db.calls)
	}
}

// TestConcurrentChanges: what a concurrent transaction changed between the lookup and the lock is looked up again —
// an open Alert Group resolved meanwhile, or one another transaction created while this one waited for the counter.
func TestConcurrentChanges(t *testing.T) {
	h := newHarness(t)
	a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"})
	h.changes(t, ingest.ChangeFired, a)
	g := h.groupOf(t, a)
	// The open Alert Group is resolved by a person before this transaction locks it: a new one starts.
	h.db.before["LockGroups"] = func() {
		g.Status, g.ResolvedAt, g.ResolvedByKind, g.ResolvedByUserID = "resolved", ts(t0), txt("user"), i8(9)
	}
	b := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "2"})
	h.db.calls = map[string]int{}
	h.changes(t, ingest.ChangeFired, b)
	if n := h.groupOf(t, b); n == g || n.Number != 2 || h.db.calls["LockCounter"] != 1 {
		t.Errorf("joined a resolved alert group: #%d, counter locks %d", n.Number, h.db.calls["LockCounter"])
	}
	// Another transaction creates the Alert Group of the key while this one waits for the counter: it joins it.
	var other *dbgen.LockGroupsRow
	h.db.before["LockCounter"] = func() {
		o := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "y", "n": "1"})
		h.db.mu.Lock()
		delete(h.db.before, "LockCounter")
		h.db.mu.Unlock()
		h.changes(t, ingest.ChangeFired, o)
		other = h.groupOf(t, o)
	}
	c := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "y", "n": "2"})
	h.changes(t, ingest.ChangeFired, c)
	if other == nil || h.groupOf(t, c) != other || len(h.db.groups) != 3 {
		t.Errorf("created a second alert group of the key: %d groups", len(h.db.groups))
	}
	// An Alert that moved to another Alert Group meanwhile is locked there too.
	d := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "z"})
	h.changes(t, ingest.ChangeFired, d)
	gd := h.groupOf(t, d)
	h.db.before["LockGroups"] = func() {
		for _, m := range h.db.members {
			if m.alert == d {
				m.group = other.ID
			}
		}
	}
	h.resolve(t, d)
	if gd.Status != "firing" || h.last(t, other).Event.String != "alert_resolved" {
		t.Errorf("resolved where it no longer fired: %s, %v", gd.Status, h.events(other))
	}
}

// TestUnstableLocks: a lock check that keeps failing gives up instead of looping.
func TestUnstableLocks(t *testing.T) {
	h := newHarness(t)
	a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"})
	h.changes(t, ingest.ChangeFired, a)
	g := h.groupOf(t, a)
	var flip func()
	flip = func() {
		g.RouteID = 3 - g.RouteID + 2
		h.db.before["LockGroups"] = flip
	}
	b := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "2"})
	h.db.before["LockGroups"] = flip
	h.db.fail["NextNumber"] = errBoom
	if _, err := h.apply(ingest.ChangeFired, b); err == nil {
		t.Error("no error")
	}
}

// TestChangesOfMembers is C-09.FR-11, C-09.AC-10, C-06.FR-11 and FR-12: a Continuation records alert_continued and no
// Reopen or new firing, an annotation change annotations_changed, both Quiet, as one entry per Snapshot each, and the
// membership keeps the startsAt and annotations.
func TestChangesOfMembers(t *testing.T) {
	h := newHarness(t)
	a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "1"}, "summary", "s")
	b := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "2"})
	h.changes(t, ingest.ChangeFired, a, b)
	g := h.groupOf(t, a)
	h.db.alerts[a].StartsAt = t0.Add(time.Hour)
	h.db.alerts[a].Annotations = []byte(`{"summary":"t"}`)
	h.db.alerts[b].StartsAt = t0.Add(2 * time.Hour)
	_, err := h.svc.AlertChanges(t.Context(), nil, []ingest.AlertChange{{Kind: ingest.ChangeContinued, AlertID: a},
		{Kind: ingest.ChangeAnnotations, AlertID: a}, {Kind: ingest.ChangeContinued, AlertID: b}})
	if err != nil {
		t.Fatal(err)
	}
	if got := h.events(g); !slices.Equal(got, []string{"created", "annotations_changed", "alert_continued"}) {
		t.Errorf("events %v", got)
	}
	if e := h.last(t, g); !slices.Equal(e.Fingerprints, []string{fingerprintOf(a), fingerprintOf(b)}) {
		t.Errorf("continued %v", e.Fingerprints)
	}
	for _, m := range h.db.members {
		if m.alert == a && (!m.startsAt.Equal(t0.Add(time.Hour)) || string(m.annotations) != `{"summary":"t"}`) {
			t.Errorf("membership %+v", m)
		}
		if m.alert == b && (!m.startsAt.Equal(t0.Add(2*time.Hour)) || string(m.annotations) != `{}`) {
			t.Errorf("membership %+v", m)
		}
	}
	if g.ReopenCount != 0 || g.Status != "firing" || len(h.db.groups) != 1 || h.db.calls["UpdateMemberships"] != 1 {
		t.Errorf("a continuation reopened or fired: %+v", g)
	}
}

// TestContinuationLog: each Continuation is logged once the transaction committed, with the #N and the fingerprint.
func TestContinuationLog(t *testing.T) {
	h := newHarness(t)
	a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"})
	h.changes(t, ingest.ChangeFired, a)
	h.log.Reset()
	out, err := h.svc.AlertChanges(t.Context(), nil, []ingest.AlertChange{{Kind: ingest.ChangeContinued,
		AlertID: a}})
	if err != nil || h.log.Len() != 0 || out.Committed == nil {
		t.Fatalf("logged before the commit: %s, %v", h.log.String(), err)
	}
	out.Committed(t.Context())
	if !strings.Contains(h.log.String(), `"event":"alert_continued","group":"#1","fingerprint":"`+fingerprintOf(a)+`"`) {
		t.Errorf("log: %s", h.log.String())
	}
}

// TestReplacement is C-09.FR-7 and C-09.AC-4: a new Alert that differs from a firing one only in Instance labels is a
// Quiet alert_replaced naming the labels; one that differs in another label is an addition.
func TestReplacement(t *testing.T) {
	h := newHarness(t)
	a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "pod": "p1", "instance": "i1"})
	h.changes(t, ingest.ChangeFired, a)
	g := h.groupOf(t, a)
	b := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "pod": "p2", "instance": "i2"})
	c := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "pod": "p3", "team": "t"})
	d := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "pod": "p1"})
	h.db.alerts[b].StaticLabelConflicts = []string{"cluster"}
	h.changes(t, ingest.ChangeFired, b, c, d)
	es := h.entriesOf(g)[1:]
	if len(es) != 3 || es[0].Event.String != "alert_replaced" || es[0].ReplacedLabel.String != "instance, pod" ||
		!slices.Equal(es[0].Fingerprints, []string{fingerprintOf(b)}) || !slices.Equal(es[0].LabelConflicts,
		[]string{"cluster"}) || es[1].Event.String != "alert_replaced" || es[1].ReplacedLabel.String != "instance" ||
		es[2].Event.String != "alerts_added" || !slices.Equal(es[2].Fingerprints, []string{fingerprintOf(c)}) {
		for _, e := range es {
			t.Logf("%s %s %v", e.Event.String, e.ReplacedLabel.String, e.Fingerprints)
		}
		t.Error("replacements")
	}
}

// TestFailures: every query error reaches the caller, so that the Snapshot rolls back.
func TestFailures(t *testing.T) {
	queries := []string{"ListChangedAlerts", "ListFiringMemberships", "LockRoutes", "LockDefaultRoute", "Restamp",
		"FindOpenGroup", "FindReopenableGroup", "EnsureCounter", "LockCounter", "LockGroups", "NextNumber", "InsertGroup",
		"InsertMemberships", "InsertTimelineEntry", "SaveGroup", "GetRoutePolicy", "GetGroupingSettings",
		"ListGroupFiringAlerts", "UpdateMemberships", "ResolveMemberships", "UpsertTimer", "NotifyTimers",
		"DeleteTimer", "IsActiveUser"}
	for _, query := range queries {
		t.Run(query, func(t *testing.T) {
			h := newHarness(t)
			h.db.routes[2].deleted = true
			a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "pod": "1"})
			b := h.alert(1, "warning", map[string]string{"alertname": "B", "cluster": "x", "pod": "1"})
			h.changes(t, ingest.ChangeFired, b)
			gb := h.groupOf(t, b)
			h.acknowledge(gb)
			h.resolve(t, b)
			h.db.alerts[b].Status = "firing"
			c := h.alert(1, "warning", map[string]string{"alertname": "C", "pod": "1"})
			h.changes(t, ingest.ChangeFired, c)
			d := h.alert(1, "critical", map[string]string{"alertname": "C", "pod": "2"})
			e := h.alert(1, "warning", map[string]string{"alertname": "E"})
			h.changes(t, ingest.ChangeFired, e)
			h.db.alerts[e].Status = "resolved"
			h.db.fail[query] = errBoom
			h.db.alerts[c].StartsAt = t0.Add(time.Hour)
			h.db.alerts[c].Status = "resolved"
			_, err := h.svc.AlertChanges(t.Context(), nil, []ingest.AlertChange{
				{Kind: ingest.ChangeFired, AlertID: a}, {Kind: ingest.ChangeFired, AlertID: b},
				{Kind: ingest.ChangeFired, AlertID: d}, {Kind: ingest.ChangeContinued, AlertID: c},
				{Kind: ingest.ChangeResolved, AlertID: c, Reason: "gone", ReasonText: "gone"},
				{Kind: ingest.ChangeResolved, AlertID: e, Reason: "stale", ReasonText: "stale"}})
			if h.db.calls[query] == 0 && query != "Restamp" {
				t.Skipf("%s not called", query)
			}
			if !errors.Is(err, errBoom) {
				t.Errorf("%s failing = %v", query, err)
			}
		})
	}
}

// TestBadRows: labels or annotations that do not decode fail the Snapshot.
func TestBadRows(t *testing.T) {
	h := newHarness(t)
	a := h.alert(2, "warning", map[string]string{"alertname": "A"})
	h.db.alerts[a].Labels = []byte("{")
	if _, err := h.apply(ingest.ChangeFired, a); err == nil {
		t.Error("bad labels")
	}
	h.db.alerts[a].Labels, h.db.alerts[a].Annotations = []byte(`{}`), []byte("[")
	if _, err := h.apply(ingest.ChangeFired, a); err == nil {
		t.Error("bad annotations")
	}
	h.db.alerts[a].Annotations = []byte(`{}`)
	h.changes(t, ingest.ChangeFired, a)
	h.db.members[0].annotations = []byte("[")
	if _, err := h.apply(ingest.ChangeContinued, a); err == nil {
		t.Error("bad membership annotations")
	}
	h.db.members[0].annotations = []byte(`{}`)
	h.db.groups[h.db.members[0].group].CommonLabels = []byte("[")
	if _, err := h.apply(ingest.ChangeContinued, a); err == nil {
		t.Error("a bad alert group row")
	}
}

// TestListedAlertsWithoutAlertGroup: every firing, routed Alert belongs to an Alert Group. A Snapshot that lists
// firing Alerts without a change groups those that fire in none — such as Alerts that fired before grouping existed
// — as if they had just fired: one joins the open Alert Group of its key with a Loud alerts_added, another starts a
// new one with `created`; an Alert without a Route stays out. The same Snapshot again changes nothing and takes no
// lock.
func TestListedAlertsWithoutAlertGroup(t *testing.T) {
	h := newHarness(t)
	a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "1"})
	h.changes(t, ingest.ChangeFired, a)
	g1 := h.groupOf(t, a)
	b := h.alert(2, "critical", map[string]string{"alertname": "A", "cluster": "x", "n": "2"})
	c := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "y"})
	u := h.alert(0, "warning", map[string]string{"alertname": "U"})
	out := h.changes(t, ingest.ChangeListed, a, b, c, u)
	g2 := h.groupOf(t, c)
	if h.groupOf(t, b) != g1 || g2 == g1 || g2.Number != 2 || len(h.db.groups) != 2 {
		t.Fatalf("grouped into #%d and #%d, %d alert groups", h.groupOf(t, b).Number, g2.Number, len(h.db.groups))
	}
	if !slices.Equal(h.events(g1), []string{"created", "alerts_added", "urgency_raised"}) {
		t.Errorf("events of #1 %v", h.events(g1))
	}
	added := h.entriesOf(g1)[1]
	if added.Event.String != "alerts_added" || added.Loudness.String != "loud" ||
		!slices.Equal(added.Fingerprints, []string{fingerprintOf(b)}) || g1.FiringAlertCount != 2 ||
		g1.SeverityLevel != "critical" {
		t.Errorf("alerts_added %+v, alert group %+v", added.InsertTimelineEntryParams, g1)
	}
	if !slices.Equal(h.events(g2), []string{"created"}) || g2.FiringAlertCount != 1 {
		t.Errorf("events of #2 %v", h.events(g2))
	}
	for _, m := range h.db.members {
		if m.alert == u {
			t.Errorf("an alert without a route was grouped")
		}
	}
	if !slices.Equal(out.AlertGroups, []int64{1, 2}) {
		t.Errorf("routed %+v", out)
	}
	entries, members := len(h.db.entries), len(h.db.members)
	h.db.calls = map[string]int{}
	out = h.changes(t, ingest.ChangeListed, a, b, c)
	if len(h.db.entries) != entries || len(h.db.members) != members || out.AlertGroups != nil ||
		h.db.calls["LockRoutes"]+h.db.calls["LockCounter"]+h.db.calls["LockGroups"]+h.db.calls["ListChangedAlerts"] != 0 {
		t.Errorf("a repeat changed %d entries, %d members, routed %+v, calls %v", len(h.db.entries)-entries,
			len(h.db.members)-members, out, h.db.calls)
	}
	// A listed Alert with a change of its own and no Alert Group is grouped too; the change applies to nothing.
	d := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "z"})
	if _, err := h.svc.AlertChanges(t.Context(), nil, []ingest.AlertChange{{Kind: ingest.ChangeContinued, AlertID: d},
		{Kind: ingest.ChangeListed, AlertID: d}}); err != nil {
		t.Fatal(err)
	}
	if g := h.groupOf(t, d); g.Number != 3 || !slices.Equal(h.events(g), []string{"created"}) {
		t.Errorf("alert with a continuation: #%d %v", g.Number, h.events(g))
	}
}

// TestListedAlertGroupedMeanwhile: a listed Alert that another transaction groups while this one waits for the counter
// row is left where it went: one membership, and its Alert Group neither locked nor changed again.
func TestListedAlertGroupedMeanwhile(t *testing.T) {
	h := newHarness(t)
	a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"})
	b := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "y"})
	h.db.before["LockCounter"] = func() {
		h.changes(t, ingest.ChangeFired, a)
	}
	h.db.locked = nil
	out := h.changes(t, ingest.ChangeListed, a, b)
	ga, gb := h.groupOf(t, a), h.groupOf(t, b)
	count := 0
	for _, m := range h.db.members {
		if m.alert == a {
			count++
		}
	}
	if count != 1 || ga == gb || !slices.Equal(h.events(ga), []string{"created"}) || len(h.db.groups) != 2 ||
		!slices.Equal(out.AlertGroups, []int64{gb.Number}) {
		t.Errorf("memberships of a %d, events %v, %d alert groups, routed %+v", count, h.events(ga), len(h.db.groups),
			out)
	}
	for _, ids := range h.db.locked {
		if slices.Contains(ids, ga.ID) {
			t.Errorf("locked #%d: %v", ga.Number, h.db.locked)
		}
	}
	// Grouped meanwhile with nothing else to group: nothing is created.
	c := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "w"})
	h.db.before["LockCounter"] = func() {
		h.changes(t, ingest.ChangeFired, c)
	}
	if out := h.changes(t, ingest.ChangeListed, c); out.AlertGroups != nil || len(h.db.groups) != 3 {
		t.Errorf("routed %+v, %d alert groups", out, len(h.db.groups))
	}
}

// TestListedFailures: a failing read of where the listed Alerts fire fails the Snapshot, before and under the counter
// row.
func TestListedFailures(t *testing.T) {
	h := newHarness(t)
	a := h.alert(2, "warning", map[string]string{"alertname": "A"})
	h.db.fail["ListFiringMemberships"] = errBoom
	if _, err := h.apply(ingest.ChangeListed, a); !errors.Is(err, errBoom) {
		t.Errorf("before the counter = %v", err)
	}
	delete(h.db.fail, "ListFiringMemberships")
	h.db.before["LockCounter"] = func() { h.db.fail["ListFiringMemberships"] = errBoom }
	if _, err := h.apply(ingest.ChangeListed, a); !errors.Is(err, errBoom) {
		t.Errorf("under the counter = %v", err)
	}
}

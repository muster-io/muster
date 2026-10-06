// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"maps"
	"slices"
	"testing"
	"time"
)

// sim keeps the rows processing reads and writes in memory and applies Snapshots through the engine as the database
// path does: the engine changes the rows it is given, and sim stores the presences and ids the way write does.
type sim struct {
	dw        time.Duration
	static    map[string]string
	groups    map[string]*group
	routes    map[string]*route
	alerts    map[string]*alert
	byID      map[int64]*alert
	presences map[[2]int64]*presence
	next      int64
	clockMs   int64
}

func newSim() *sim {
	return &sim{dw: 45 * time.Second, static: map[string]string{}, groups: map[string]*group{},
		routes: map[string]*route{}, alerts: map[string]*alert{}, byID: map[int64]*alert{},
		presences: map[[2]int64]*presence{}}
}

func (s *sim) id() int64 {
	s.next++
	return s.next
}

// send applies the Snapshot p received at.
func (s *sim) send(at time.Time, p Payload) *engine {
	g := s.groups[p.GroupKey]
	if g == nil {
		g = &group{ID: s.id(), LastSnapshotAt: at}
		s.groups[p.GroupKey] = g
	}
	path := AlertmanagerRoutePath(p.GroupKey)
	r := s.routes[path]
	if r == nil {
		r = &route{ID: s.id()}
		s.routes[path] = r
	}
	var rows []*alert
	seen := map[int64]bool{}
	for _, pa := range p.Alerts {
		if a := s.alerts[pa.Fingerprint]; a != nil && !seen[a.ID] {
			rows, seen[a.ID] = append(rows, a), true
		}
	}
	var active []*presence
	for key, pr := range s.presences {
		if key[1] != g.ID || (pr.State != PresenceListed && pr.State != PresenceMissed) {
			continue
		}
		cp := *pr
		cp.ActiveElsewhere = false
		for other, o := range s.presences {
			if other[0] == key[0] && other[1] != g.ID && (o.State == PresenceListed || o.State == PresenceMissed) {
				cp.ActiveElsewhere = true
			}
		}
		active = append(active, &cp)
		if a := s.byID[key[0]]; !seen[a.ID] {
			rows, seen[a.ID] = append(rows, a), true
		}
	}
	e := newEngine(snapshotIn{ReceivedAt: at, Payload: p, StaticLabels: s.static, DuplicateWindow: s.dw,
		ClockMs: s.clockMs}, g, r, rows, active)
	e.run()
	for _, a := range e.alerts {
		if a.ID == 0 {
			a.ID = s.id()
			s.alerts[a.Fingerprint], s.byID[a.ID] = a, a
		}
	}
	for _, a := range e.listedAlerts {
		key := [2]int64{a.ID, g.ID}
		pr := s.presences[key]
		if pr == nil {
			pr = &presence{AlertID: a.ID}
			s.presences[key] = pr
		}
		pr.State, pr.LastListedWindow, pr.MissedSince = PresenceListed, g.WindowSeq, nil
	}
	for _, id := range e.missed {
		start := *g.WindowStartedAt
		s.presences[[2]int64{id, g.ID}].State = PresenceMissed
		s.presences[[2]int64{id, g.ID}].MissedSince = &start
	}
	for _, id := range e.gone {
		s.presences[[2]int64{id, g.ID}].State, s.presences[[2]int64{id, g.ID}].MissedSince = PresenceGone, nil
	}
	for _, a := range e.ended {
		for key, pr := range s.presences {
			if key[0] == a.ID && (pr.State == PresenceListed || pr.State == PresenceMissed) {
				pr.State, pr.MissedSince = PresenceGone, nil
			}
		}
	}
	return e
}

func (s *sim) alert(t *testing.T, fp string) *alert {
	t.Helper()
	a := s.alerts[fp]
	if a == nil {
		t.Fatalf("no alert %s", fp)
	}
	return a
}

func firing(fp string, startsAt time.Time) PayloadAlert {
	return PayloadAlert{Fingerprint: fp, Status: StatusFiring, Labels: map[string]string{"name": fp},
		Annotations: map[string]string{}, StartsAt: startsAt}
}

func resolved(fp string, startsAt time.Time) PayloadAlert {
	a := firing(fp, startsAt)
	a.Status = StatusResolved
	end := startsAt.Add(time.Hour)
	a.EndsAt = &end
	return a
}

const gk = `{}/{team="db"}:{alertname="DiskFull"}`

func snap(reason string, alerts ...PayloadAlert) Payload {
	return Payload{GroupKey: gk, Status: StatusFiring, Reason: reason, HasReason: reason != "", Alerts: alerts}
}

func kinds(e *engine) []string {
	out := make([]string, len(e.changes))
	for i, c := range e.changes {
		out[i] = string(c.kind) + ":" + c.alert.Fingerprint
		if c.reason != "" {
			out[i] += ":" + c.reason
		}
	}
	return out
}

// TestAlertRules covers every row of the Alerts table of the contract (C-06.FR-2, FR-4, FR-11, FR-12).
func TestAlertRules(t *testing.T) {
	older, same, newer := t0.Add(-time.Hour), t0, t0.Add(time.Hour)
	type seed struct {
		status, reason string
		startsAt       time.Time
	}
	for _, tt := range []struct {
		name    string
		seed    *seed
		listed  PayloadAlert
		changes []string
		status  string
		reason  string
		episode int64
		starts  time.Time
		dropped int
	}{
		{"firing, no row", nil, firing("a", same), []string{"fired:a"}, StatusFiring, "", 1, same, 0},
		{"firing, resolved with an older startsAt", &seed{StatusResolved, ResolveResolved, older},
			firing("a", same), []string{"fired:a"}, StatusFiring, "", 2, same, 0},
		{"firing, resolved as Gone with the same startsAt", &seed{StatusResolved, ResolveGone, same},
			firing("a", same), []string{"fired:a"}, StatusFiring, "", 2, same, 0},
		{"firing, resolved as Stale with the same startsAt", &seed{StatusResolved, ResolveStale, same},
			firing("a", same), []string{"fired:a"}, StatusFiring, "", 2, same, 0},
		{"firing, firing with the same startsAt", &seed{StatusFiring, "", same}, firing("a", same), []string{},
			StatusFiring, "", 1, same, 0},
		{"firing, firing with an older startsAt: Continuation", &seed{StatusFiring, "", older}, firing("a", same),
			[]string{"continued:a"}, StatusFiring, "", 1, same, 0},
		{"firing, firing with a newer startsAt: an old copy", &seed{StatusFiring, "", newer}, firing("a", same),
			[]string{}, StatusFiring, "", 1, newer, 0},
		{"firing, resolved with a newer startsAt: an old copy", &seed{StatusResolved, ResolveResolved, newer},
			firing("a", same), []string{}, StatusResolved, ResolveResolved, 1, newer, 0},
		{"firing, resolved by a resolve with the same startsAt: an old copy",
			&seed{StatusResolved, ResolveResolved, same}, firing("a", same), []string{}, StatusResolved,
			ResolveResolved, 1, same, 0},
		{"resolved, firing with the same startsAt", &seed{StatusFiring, "", same}, resolved("a", same),
			[]string{"resolved:a:resolved"}, StatusResolved, ResolveResolved, 1, same, 0},
		{"resolved, firing with an older startsAt", &seed{StatusFiring, "", older}, resolved("a", same),
			[]string{"resolved:a:resolved"}, StatusResolved, ResolveResolved, 1, older, 0},
		{"resolved, already resolved", &seed{StatusResolved, ResolveResolved, same}, resolved("a", same),
			[]string{}, StatusResolved, ResolveResolved, 1, same, 0},
		{"resolved, firing with a newer startsAt", &seed{StatusFiring, "", newer}, resolved("a", same),
			[]string{}, StatusFiring, "", 1, newer, 0},
		{"resolved, no row: dropped", nil, resolved("a", same), []string{}, "", "", 0, time.Time{}, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newSim()
			if tt.seed != nil {
				a := &alert{ID: s.id(), Fingerprint: "a", Labels: map[string]string{"name": "a"},
					Annotations: map[string]string{}, Status: tt.seed.status, StartsAt: tt.seed.startsAt, Episode: 1,
					FiredAt: t0.Add(-2 * time.Hour), LastSeenAt: t0.Add(-time.Minute)}
				if tt.seed.status == StatusResolved {
					at := t0.Add(-time.Minute)
					a.ResolvedAt, a.Reason = &at, tt.seed.reason
				}
				s.alerts["a"], s.byID[a.ID] = a, a
			}
			e := s.send(t0, snap("repeat interval elapsed", tt.listed))
			if got := kinds(e); !slices.Equal(got, tt.changes) {
				t.Errorf("changes %v, want %v", got, tt.changes)
			}
			if e.stats.Dropped != tt.dropped {
				t.Errorf("dropped %d, want %d", e.stats.Dropped, tt.dropped)
			}
			a := s.alerts["a"]
			if tt.status == "" {
				if a != nil {
					t.Fatalf("a resolve created an alert: %+v", a)
				}
				return
			}
			if a.Status != tt.status || a.Reason != tt.reason || a.Episode != tt.episode || !a.StartsAt.Equal(tt.starts) {
				t.Errorf("alert %s/%s episode %d starts %v, want %s/%s %d %v", a.Status, a.Reason, a.Episode,
					a.StartsAt, tt.status, tt.reason, tt.episode, tt.starts)
			}
		})
	}
}

// TestNewFiring checks what a new firing records: fired_at and last seen at receipt (C-06.FR-13), Static labels with
// the Alert's own value winning and the conflict named (C-06.FR-3), and the resolution cleared.
func TestNewFiring(t *testing.T) {
	s := newSim()
	s.static = map[string]string{"cluster": "b", "region": "eu"}
	pa := firing("a", t0.Add(-time.Hour))
	pa.Labels = map[string]string{"alertname": "DiskFull", "cluster": "a"}
	pa.Annotations = map[string]string{"summary": "full"}
	pa.GeneratorURL = "http://prometheus/graph"
	s.send(t0, snap("first notification", pa))
	a := s.alert(t, "a")
	want := map[string]string{"alertname": "DiskFull", "cluster": "a", "region": "eu"}
	if !maps.Equal(a.Labels, want) || !slices.Equal(a.Conflicts, []string{"cluster"}) ||
		a.Annotations["summary"] != "full" || a.GeneratorURL != "http://prometheus/graph" || !a.FiredAt.Equal(t0) ||
		!a.LastSeenAt.Equal(t0) {
		t.Errorf("alert %+v", a)
	}
	s.send(t0.Add(time.Minute), snap("all alerts resolved", resolved("a", t0.Add(-time.Hour))))
	if a.Status != StatusResolved || a.ResolvedAt == nil || !a.ResolvedAt.Equal(t0.Add(time.Minute)) ||
		a.EndsAt == nil || a.Reason != ResolveResolved {
		t.Fatalf("resolved %+v", a)
	}
	e := s.send(t0.Add(2*time.Minute), snap("first notification", firing("a", t0.Add(time.Minute))))
	if a.Status != StatusFiring || a.Episode != 2 || a.ResolvedAt != nil || a.Reason != "" || a.EndsAt != nil ||
		!a.FiredAt.Equal(t0.Add(2*time.Minute)) || e.stats.Fired != 1 {
		t.Errorf("new firing %+v", a)
	}
}

// TestAnnotationChange is C-06.FR-12: changed annotations are an Alert change of their own, also with a
// Continuation; unchanged ones record nothing.
func TestAnnotationChange(t *testing.T) {
	s := newSim()
	pa := firing("a", t0)
	pa.Annotations = map[string]string{"summary": "one"}
	s.send(t0, snap("first notification", pa))
	if e := s.send(t0.Add(time.Minute), snap("repeat interval elapsed", pa)); len(e.changes) != 0 {
		t.Errorf("unchanged annotations: %v", kinds(e))
	}
	pa.Annotations = map[string]string{"summary": "two"}
	e := s.send(t0.Add(2*time.Minute), snap("repeat interval elapsed", pa))
	if got := kinds(e); !slices.Equal(got, []string{"annotations_changed:a"}) || s.alert(t, "a").Annotations["summary"] != "two" {
		t.Errorf("changes %v", got)
	}
	pa.StartsAt, pa.Annotations = t0.Add(time.Minute), map[string]string{"summary": "three"}
	e = s.send(t0.Add(3*time.Minute), snap("repeat interval elapsed", pa))
	if got := kinds(e); !slices.Equal(got, []string{"continued:a", "annotations_changed:a"}) || e.stats.Continued != 1 {
		t.Errorf("continuation changes %v", got)
	}
}

// TestHACopies is C-06.FR-2 and F-037: the identical copy of an HA pair, and the same Snapshot sent again, change
// nothing but the time last seen.
func TestHACopies(t *testing.T) {
	s := newSim()
	p := snap("first notification", firing("a", t0), firing("b", t0), firing("c", t0))
	s.send(t0, p)
	for _, at := range []time.Duration{0, 15 * time.Second, time.Minute} {
		if e := s.send(t0.Add(at), p); len(e.changes) != 0 || e.stats.Fired != 0 {
			t.Errorf("copy at %v: %v", at, kinds(e))
		}
	}
	for _, fp := range []string{"a", "b", "c"} {
		if a := s.alert(t, fp); a.Episode != 1 || a.Status != StatusFiring || !a.LastSeenAt.Equal(t0.Add(time.Minute)) {
			t.Errorf("%s: %+v", fp, a)
		}
	}
}

// TestResolvedGroup is C-06.FR-21 and C-06.AC-13: a Snapshot with status resolved that lists 6 of 20 firing Alerts
// resolves all 20; the Alert of another groupKey still fires.
func TestResolvedGroup(t *testing.T) {
	s := newSim()
	var all, six []PayloadAlert
	for i := range 20 {
		fp := string(rune('A' + i))
		all = append(all, firing(fp, t0))
		if i < 6 {
			six = append(six, resolved(fp, t0))
		}
	}
	s.send(t0, snap("first notification", all...))
	other := snap("first notification", firing("other", t0))
	other.GroupKey = `{}:{alertname="Other"}`
	s.send(t0, other)
	p := snap(ReasonAllResolved, six...)
	p.Status = StatusResolved
	e := s.send(t0.Add(time.Minute), p)
	if e.stats.Resolved != 20 || e.stats.Dropped != 0 {
		t.Errorf("resolved %d, dropped %d", e.stats.Resolved, e.stats.Dropped)
	}
	for _, a := range all {
		if got := s.alert(t, a.Fingerprint); got.Status != StatusResolved || got.Reason != ResolveResolved {
			t.Errorf("%s: %s/%s", a.Fingerprint, got.Status, got.Reason)
		}
	}
	if s.alert(t, "other").Status != StatusFiring {
		t.Error("the alert of the other groupKey resolved")
	}
	// An Alert listed as resolved with an older startsAt keeps its newer firing, also in a resolved group.
	s.send(t0.Add(2*time.Minute), snap("first notification", firing("A", t0.Add(time.Minute))))
	p = snap(ReasonAllResolved, resolved("A", t0))
	p.Status = StatusResolved
	s.send(t0.Add(3*time.Minute), p)
	if a := s.alert(t, "A"); a.Status != StatusFiring || a.Episode != 2 {
		t.Errorf("newer firing %+v", a)
	}
}

// TestRepeatedResolves is C-06.AC-14 and F-046: a resolve arriving again, alone and inside firing Snapshots of its
// groupKey, changes nothing and is not counted; a resolve with the old startsAt leaves a new firing firing.
func TestRepeatedResolves(t *testing.T) {
	s := newSim()
	s.send(t0, snap("first notification", firing("a", t0), firing("b", t0)))
	e := s.send(t0.Add(2*time.Minute), snap("some alerts resolved", resolved("a", t0), firing("b", t0)))
	if e.stats.Resolved != 1 {
		t.Fatalf("resolved %d", e.stats.Resolved)
	}
	for i, p := range []Payload{
		snap("some alerts resolved", resolved("a", t0)),
		snap("repeat interval elapsed", resolved("a", t0), firing("b", t0)),
		snap("some alerts resolved", resolved("a", t0), firing("b", t0)),
		snap("repeat interval elapsed", resolved("a", t0), firing("b", t0)),
	} {
		e := s.send(t0.Add(time.Duration(4+2*i)*time.Minute), p)
		if len(e.changes) != 0 || e.stats.Dropped != 0 {
			t.Errorf("repeat %d: %v, dropped %d", i, kinds(e), e.stats.Dropped)
		}
	}
	s.send(t0.Add(15*time.Minute), snap("new alerts added", firing("a", t0.Add(14*time.Minute)), firing("b", t0)))
	e = s.send(t0.Add(16*time.Minute), snap("some alerts resolved", resolved("a", t0), firing("b", t0)))
	if a := s.alert(t, "a"); a.Status != StatusFiring || a.Episode != 2 || len(e.changes) != 0 {
		t.Errorf("after the old resolve: %+v, %v", a, kinds(e))
	}
}

// TestLateSnapshot: a Snapshot received before the current window of its groupKey started, as in a replay, applies
// only its explicit resolves and changes no presence, window or learned interval.
func TestLateSnapshot(t *testing.T) {
	s := newSim()
	s.send(t0, snap("first notification", firing("a", t0), firing("b", t0)))
	s.send(t0.Add(10*time.Minute), snap("repeat interval elapsed", firing("a", t0), firing("b", t0)))
	g := *s.groups[gk]
	r := *s.routes[AlertmanagerRoutePath(gk)]
	e := s.send(t0.Add(5*time.Minute), snap("repeat interval elapsed", resolved("a", t0), firing("c", t0),
		resolved("ghost", t0)))
	if !e.late || e.stats.Resolved != 1 || e.stats.Fired != 0 || e.stats.Dropped != 1 || s.alerts["c"] != nil {
		t.Errorf("late snapshot: %+v, %v", e.stats, kinds(e))
	}
	if got := *s.groups[gk]; got.WindowSeq != g.WindowSeq || !got.WindowStartedAt.Equal(*g.WindowStartedAt) ||
		!got.LastSnapshotAt.Equal(g.LastSnapshotAt) {
		t.Errorf("the window moved: %+v", got)
	}
	if got := s.routes[AlertmanagerRoutePath(gk)]; got.Observations != r.Observations {
		t.Errorf("the late snapshot was learned: %+v", got)
	}
	if s.presences[[2]int64{s.alert(t, "b").ID, g.ID}].State != PresenceListed {
		t.Error("the late snapshot changed a presence")
	}
}

// TestEarlySnapshot: a Snapshot received less than the duplicate window before the current window started — a request
// that committed after a later one — counts in the current window: its Alerts are listed there, a truncated one marks
// the window, and it opens no window and decides no absence.
func TestEarlySnapshot(t *testing.T) {
	s := newSim()
	s.send(t0, snap("first notification", firing("a", t0), firing("b", t0), firing("c", t0)))
	s.send(t0.Add(10*time.Minute), snap(ReasonRepeat, firing("a", t0), firing("b", t0)))
	g := s.groups[gk]
	if s.presence(t, "c", gk).State != PresenceMissed {
		t.Fatal("c was not missed")
	}
	cut := snap(ReasonRepeat, firing("c", t0), firing("d", t0))
	cut.TruncatedAlerts = 1
	e := s.send(t0.Add(10*time.Minute-5*time.Second), cut)
	if e.late || !e.early || g.WindowSeq != 2 || !g.WindowStartedAt.Equal(t0.Add(10*time.Minute)) ||
		!g.WindowTruncated || g.Truncated || e.stats.Fired != 1 {
		t.Errorf("early copy: %+v, %+v", g, e.stats)
	}
	for _, fp := range []string{"c", "d"} {
		if p := s.presence(t, fp, gk); p.State != PresenceListed || p.LastListedWindow != 2 {
			t.Errorf("%s: %+v", fp, p)
		}
	}
	if len(e.missed) != 0 || len(e.gone) != 0 {
		t.Errorf("the early copy decided absence: %v %v", e.missed, e.gone)
	}
}

// TestOldCopyIsListed: an old copy of an earlier firing changes nothing of the Alert, but its fingerprint is listed,
// so the firing Alert is never Gone while Alertmanager lists it.
func TestOldCopyIsListed(t *testing.T) {
	s := newSim()
	s.send(t0, snap("first notification", firing("a", t0.Add(time.Minute)), firing("b", t0)))
	s.send(t0.Add(10*time.Minute), snap(ReasonRepeat, firing("b", t0)))
	e := s.send(t0.Add(20*time.Minute), snap(ReasonRepeat, firing("a", t0), firing("b", t0)))
	a := s.alert(t, "a")
	if p := s.presence(t, "a", gk); p.State != PresenceListed || len(e.changes) != 0 || a.Status != StatusFiring ||
		!a.StartsAt.Equal(t0.Add(time.Minute)) {
		t.Errorf("presence %+v, alert %+v, changes %v", p, a, kinds(e))
	}
}

// TestDuplicateFingerprint: a fingerprint listed twice in one Snapshot is applied in order, and a new Alert that the
// same Snapshot resolves is stored resolved.
func TestDuplicateFingerprint(t *testing.T) {
	s := newSim()
	e := s.send(t0, snap("first notification", firing("a", t0), resolved("a", t0)))
	if a := s.alert(t, "a"); a.Status != StatusResolved || e.stats.Fired != 1 || e.stats.Resolved != 1 {
		t.Errorf("alert %+v, stats %+v", a, e.stats)
	}
}

func TestWithStaticLabels(t *testing.T) {
	labels, conflicts := withStaticLabels(map[string]string{"a": "1", "c": "x"},
		map[string]string{"c": "y", "b": "2", "a": "1"})
	if !maps.Equal(labels, map[string]string{"a": "1", "b": "2", "c": "x"}) || !slices.Equal(conflicts, []string{"a", "c"}) {
		t.Errorf("labels %v, conflicts %v", labels, conflicts)
	}
	labels, conflicts = withStaticLabels(map[string]string{"a": "1"}, nil)
	if !maps.Equal(labels, map[string]string{"a": "1"}) || conflicts == nil || len(conflicts) != 0 {
		t.Errorf("without static labels: %v %v", labels, conflicts)
	}
}

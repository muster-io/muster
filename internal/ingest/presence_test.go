// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"testing"
	"time"
)

func (s *sim) state(t *testing.T, fp string) string {
	t.Helper()
	a := s.alert(t, fp)
	if a.Status == StatusResolved {
		return a.Reason
	}
	return a.Status
}

func (s *sim) presence(t *testing.T, fp, key string) *presence {
	t.Helper()
	return s.presences[[2]int64{s.alert(t, fp).ID, s.groups[key].ID}]
}

// TestDuplicateWindow is C-06.FR-5: Snapshots of one groupKey within the duplicate window are one window, and a later
// one opens the next.
func TestDuplicateWindow(t *testing.T) {
	s := newSim()
	p := snap("first notification", firing("a", t0))
	for _, tt := range []struct {
		at   time.Duration
		seq  int64
		open bool
	}{
		{0, 1, true}, {20 * time.Second, 1, false}, {44 * time.Second, 1, false}, {45 * time.Second, 2, true},
		{89 * time.Second, 2, false}, {10 * time.Minute, 3, true},
	} {
		s.send(t0.Add(tt.at), p)
		g := s.groups[gk]
		if g.WindowSeq != tt.seq || (tt.open && !g.WindowStartedAt.Equal(t0.Add(tt.at))) {
			t.Errorf("at %v: window %d started %v", tt.at, g.WindowSeq, g.WindowStartedAt)
		}
		if !g.LastSnapshotAt.Equal(t0.Add(tt.at)) {
			t.Errorf("at %v: last snapshot %v", tt.at, g.LastSnapshotAt)
		}
	}
}

// TestGoneAC1AC2 is C-06.AC-2 and C-06.AC-1: a copy 20 seconds later without the Alert proves nothing; missing from a
// later window it stays firing; missing from the next one processing.gone_min_absence after the first miss, it is
// Gone with the reason of C-06.FR-10.
func TestGoneAC1AC2(t *testing.T) {
	s := newSim()
	s.clockMs = 7
	all := snap("first notification", firing("a", t0), firing("b", t0), firing("c", t0))
	ab := snap(ReasonRepeat, firing("a", t0), firing("b", t0))
	s.send(t0, all)
	s.send(t0.Add(20*time.Second), ab)
	if p := s.presence(t, "c", gk); p.State != PresenceListed {
		t.Fatalf("the near-duplicate missed c: %+v", p)
	}
	s.send(t0.Add(80*time.Second), ab)
	if p := s.presence(t, "c", gk); s.state(t, "c") != StatusFiring || p.State != PresenceMissed ||
		!p.MissedSince.Equal(t0.Add(80*time.Second)) {
		t.Fatalf("after the first miss: %s %+v", s.state(t, "c"), p)
	}
	e := s.send(t0.Add(80*time.Second+GoneMinAbsence-time.Second), ab)
	if s.state(t, "c") != StatusFiring || e.stats.Gone != 0 {
		t.Fatalf("Gone before processing.gone_min_absence")
	}
	e = s.send(t0.Add(80*time.Second+GoneMinAbsence), ab)
	c := s.alert(t, "c")
	if c.Reason != ResolveGone || c.ReasonText != GoneReasonText || e.stats.Gone != 1 || e.stats.Resolved != 0 ||
		s.presence(t, "c", gk).State != PresenceGone {
		t.Errorf("c = %+v, stats %+v", c, e.stats)
	}
	if got := kinds(e); len(got) != 1 || got[0] != "resolved:c:gone" {
		t.Errorf("changes %v", got)
	}
	if s.state(t, "a") != StatusFiring || s.presence(t, "a", gk).State != PresenceListed {
		t.Error("a listed alert went")
	}
}

// TestGoneSecondMissInSameWindow: the second copy of the window that first missed the Alert never makes it Gone,
// however long after.
func TestGoneSecondMissInSameWindow(t *testing.T) {
	s := newSim()
	s.dw = time.Hour
	s.send(t0, snap("first notification", firing("a", t0), firing("c", t0)))
	s.send(t0.Add(2*time.Hour), snap(ReasonRepeat, firing("a", t0)))
	s.send(t0.Add(2*time.Hour+30*time.Minute), snap(ReasonRepeat, firing("a", t0)))
	if s.state(t, "c") != StatusFiring || s.presence(t, "c", gk).State != PresenceMissed {
		t.Errorf("c went in the window that missed it first")
	}
}

// TestRestartAC15 is C-06.AC-15 (F-042): Snapshots at T, T+60 s and T+120 s listing 3, 9 and 10 of 10 firing
// Alerts leave every Alert firing; A missing at T, T+60 s and T+6 min stays firing until T+6 min and is Gone then.
func TestRestartAC15(t *testing.T) {
	s := newSim()
	var ten, nine, three []PayloadAlert
	for i := range 10 {
		fp := string(rune('0' + i))
		ten = append(ten, firing(fp, t0))
		if i != 4 {
			nine = append(nine, firing(fp, t0))
		}
		if i < 3 {
			three = append(three, firing(fp, t0))
		}
	}
	s.send(t0, snap("first notification", ten...))
	at := t0.Add(10 * time.Minute)
	s.send(at, snap("first notification", three...))
	s.send(at.Add(time.Minute), snap("new alerts added", nine...))
	s.send(at.Add(2*time.Minute), snap("new alerts added", ten...))
	for _, a := range ten {
		if s.state(t, a.Fingerprint) != StatusFiring || s.presence(t, a.Fingerprint, gk).State != PresenceListed {
			t.Errorf("%s after the restart: %s", a.Fingerprint, s.state(t, a.Fingerprint))
		}
	}
	at = at.Add(10 * time.Minute)
	s.send(at, snap(ReasonRepeat, nine...))
	s.send(at.Add(time.Minute), snap(ReasonRepeat, nine...))
	if s.state(t, "4") != StatusFiring {
		t.Fatal("4 went at T+60 s")
	}
	s.send(at.Add(6*time.Minute), snap(ReasonRepeat, nine...))
	if s.state(t, "4") != ResolveGone {
		t.Errorf("4 at T+6 min: %s", s.state(t, "4"))
	}
}

// TestTruncation is C-06.FR-6: a truncated Snapshot marks its groupKey truncated until the next untruncated one and
// is never evidence of absence, nor is the rest of its window.
func TestTruncation(t *testing.T) {
	s := newSim()
	s.send(t0, snap("first notification", firing("a", t0), firing("b", t0), firing("c", t0)))
	cut := snap(ReasonRepeat, firing("a", t0))
	cut.TruncatedAlerts = 2
	at := t0
	for i := range 4 {
		at = at.Add(6 * time.Minute)
		e := s.send(at, cut)
		g := s.groups[gk]
		if !g.Truncated || !g.TruncatedSince.Equal(t0.Add(6*time.Minute)) || !g.WindowTruncated || e.stats.Truncated != 2 {
			t.Fatalf("round %d: %+v", i, g)
		}
	}
	// An untruncated copy in the truncated window clears the groupKey, but its window still proves nothing.
	e := s.send(at.Add(10*time.Second), snap(ReasonRepeat, firing("a", t0)))
	g := s.groups[gk]
	if g.Truncated || g.TruncatedSince != nil || !g.WindowTruncated || len(e.missed) != 0 {
		t.Fatalf("after the untruncated copy: %+v, missed %v", g, e.missed)
	}
	for _, fp := range []string{"b", "c"} {
		if s.state(t, fp) != StatusFiring || s.presence(t, fp, gk).State != PresenceListed {
			t.Errorf("%s while truncated: %s", fp, s.state(t, fp))
		}
	}
	// The next untruncated window counts again.
	s.send(at.Add(time.Minute), snap(ReasonRepeat, firing("a", t0)))
	if g.WindowTruncated || s.presence(t, "b", gk).State != PresenceMissed {
		t.Errorf("the next window: %+v", s.presence(t, "b", gk))
	}
}

// TestSeveralGroupKeys is C-06.FR-7: an Alert listed in two groupKeys is not Gone while one of them still lists it,
// and is Gone once it is Gone in both.
func TestSeveralGroupKeys(t *testing.T) {
	s := newSim()
	other := `{}/{team="ops"}:{alertname="DiskFull"}`
	in := func(key, reason string, alerts ...PayloadAlert) Payload {
		p := snap(reason, alerts...)
		p.GroupKey = key
		return p
	}
	s.send(t0, in(gk, "first notification", firing("a", t0), firing("x", t0)))
	s.send(t0, in(other, "first notification", firing("a", t0)))
	for i := 1; i <= 3; i++ {
		at := t0.Add(time.Duration(i) * 6 * time.Minute)
		s.send(at, in(gk, ReasonRepeat, firing("x", t0)))
		s.send(at, in(other, ReasonRepeat, firing("a", t0)))
	}
	if s.state(t, "a") != StatusFiring || s.presence(t, "a", gk).State != PresenceGone ||
		s.presence(t, "a", other).State != PresenceListed {
		t.Fatalf("a while the other groupKey lists it: %s", s.state(t, "a"))
	}
	s.send(t0.Add(30*time.Minute), in(other, ReasonRepeat, firing("y", t0)))
	s.send(t0.Add(36*time.Minute), in(other, ReasonRepeat, firing("y", t0)))
	if s.state(t, "a") != ResolveGone {
		t.Errorf("a Gone in both: %s", s.state(t, "a"))
	}
	// Listed again, it fires anew in that groupKey alone.
	s.send(t0.Add(40*time.Minute), in(other, "new alerts added", firing("a", t0), firing("y", t0)))
	if a := s.alert(t, "a"); a.Status != StatusFiring || a.Episode != 2 || s.presence(t, "a", gk).State != PresenceGone {
		t.Errorf("a again: %+v", a)
	}
}

// TestResolveEndsPresences: an Alert that resolves leaves no active presence, so that a later window never misses
// it.
func TestResolveEndsPresences(t *testing.T) {
	s := newSim()
	s.send(t0, snap("first notification", firing("a", t0), firing("b", t0)))
	s.send(t0.Add(time.Minute), snap("some alerts resolved", resolved("a", t0), firing("b", t0)))
	if p := s.presence(t, "a", gk); p.State != PresenceGone {
		t.Errorf("presence of the resolved alert: %+v", p)
	}
	e := s.send(t0.Add(10*time.Minute), snap(ReasonRepeat, firing("b", t0)))
	if len(e.missed) != 0 || len(e.gone) != 0 {
		t.Errorf("a resolved alert was missed: %v %v", e.missed, e.gone)
	}
}

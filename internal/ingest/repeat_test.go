// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"bytes"
	"slices"
	"testing"
	"time"
)

func TestAlertmanagerRoutePath(t *testing.T) {
	for _, tt := range []struct{ key, want string }{
		{`{}/{team="db"}:{alertname="DiskFull"}`, `{}/{team="db"}`},
		{`{}:{alertname="DiskFull", service="api"}`, `{}`},
		{`{}/{url=~"https://a:8080/.*"}:{alertname="A"}`, `{}/{url=~"https://a:8080/.*"}`},
		{`{}/{msg="say \"a:b\""}:{x="y:z"}`, `{}/{msg="say \"a:b\""}`},
		{`{}/{team="}"}:{alertname="A"}`, `{}/{team="}"}`},
		{`{}:{}`, `{}`},
		{`no-colon`, `no-colon`},
		{`}}{}:{a="b"}`, `}}{}`},
	} {
		if got := AlertmanagerRoutePath(tt.key); got != tt.want {
			t.Errorf("AlertmanagerRoutePath(%s) = %s, want %s", tt.key, got, tt.want)
		}
	}
}

func TestMedian(t *testing.T) {
	for _, tt := range []struct {
		in   []int64
		want int64
	}{
		{[]int64{5}, 5}, {[]int64{3, 1, 2}, 2}, {[]int64{345, 315, 345, 315}, 330}, {[]int64{9, 1, 1, 9, 5}, 5},
	} {
		in := slices.Clone(tt.in)
		if got := median(tt.in); got != tt.want || !slices.Equal(in, tt.in) {
			t.Errorf("median(%v) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestRing(t *testing.T) {
	r := &route{}
	for i := int64(1); i <= 12; i++ {
		r.add(i * 1000)
	}
	if !slices.Equal(r.Gaps, []int64{4000, 5000, 6000, 7000, 8000, 9000, 10000, 11000, 12000}) || r.Observations != 12 ||
		*r.Learned != 8000 || !r.changed {
		t.Errorf("ring %+v", r)
	}
}

// TestRepeatLearning is C-06.FR-8 (F-033, F-050): the gaps that end in a repeat interval elapsed Snapshot are learned,
// a near-duplicate inside the duplicate window is ignored, and the learned interval is the median of the gaps.
func TestRepeatLearning(t *testing.T) {
	s := newSim()
	p := snap("first notification", firing("a", t0))
	s.send(t0, p)
	r := s.routes[AlertmanagerRoutePath(gk)]
	at := t0
	for _, gap := range []time.Duration{360 * time.Second, 345 * time.Second, 375 * time.Second} {
		at = at.Add(gap)
		s.send(at, snap(ReasonRepeat, firing("a", t0)))
		s.send(at.Add(15*time.Second), snap(ReasonRepeat, firing("a", t0))) // the HA copy
	}
	if !slices.Equal(r.Gaps, []int64{360000, 345000, 375000}) || *r.Learned != 360000 {
		t.Errorf("route %+v", r)
	}
	g := s.groups[gk]
	if !g.LastRepeatAt.Equal(at) || !g.LastContentAt.Equal(at) {
		t.Errorf("group %+v", g)
	}
	// Any other reason opens a window without a sample.
	s.send(at.Add(10*time.Minute), snap("new alerts added", firing("a", t0), firing("b", t0)))
	if len(r.Gaps) != 3 {
		t.Errorf("a new alert was learned as a repeat: %v", r.Gaps)
	}
}

// TestRepeatLearningWithoutReason: an Alertmanager without notification_reason repeats identical Snapshots; a gap is
// learned when the content of the window that opens equals the previous window's.
func TestRepeatLearningWithoutReason(t *testing.T) {
	s := newSim()
	p := snap("", firing("a", t0), firing("b", t0))
	s.send(t0, p)
	s.send(t0.Add(5*time.Minute), snap("", firing("b", t0), firing("a", t0)))
	r := s.routes[AlertmanagerRoutePath(gk)]
	if *r.Learned != 300000 {
		t.Fatalf("route %+v", r)
	}
	s.send(t0.Add(11*time.Minute), snap("", firing("a", t0)))
	s.send(t0.Add(17*time.Minute), snap("", firing("a", t0), resolved("b", t0)))
	if len(r.Gaps) != 1 {
		t.Errorf("a changed snapshot was learned: %v", r.Gaps)
	}
	s.send(t0.Add(23*time.Minute), snap("", firing("a", t0), resolved("b", t0)))
	if !slices.Equal(r.Gaps, []int64{300000, 360000}) {
		t.Errorf("gaps %v", r.Gaps)
	}
}

func TestContentSum(t *testing.T) {
	a := contentSum([]PayloadAlert{firing("a", t0), resolved("b", t0)})
	b := contentSum([]PayloadAlert{resolved("b", t0.Add(time.Hour)), firing("a", t0)})
	c := contentSum([]PayloadAlert{firing("a", t0), firing("b", t0)})
	if !bytes.Equal(a, b) || bytes.Equal(a, c) || len(a) != 32 {
		t.Error("contentSum")
	}
}

func TestStaleAfter(t *testing.T) {
	five := 5 * time.Minute
	if StaleAfter(nil) != 25*time.Hour || StaleAfter(&five) != 15*time.Minute {
		t.Error("StaleAfter")
	}
}

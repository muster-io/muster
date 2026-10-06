// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/ingest/dbgen"
	"github.com/muster-io/muster/internal/integrations"
	"github.com/muster-io/muster/internal/matchers"
)

// fakeView is the Alerts of one Integration in memory; the sorted queries filter by state, containment and cursor as
// the SQL does.
type fakeView struct {
	ProcessQueries
	rows    []viewRow
	batches []int32
	fail    error
}

func (f *fakeView) FindViewIntegration(_ context.Context, arg dbgen.FindViewIntegrationParams) (
	dbgen.FindViewIntegrationRow, error) {
	if arg.PublicID != "NTAAAAAAAAAAAA" {
		return dbgen.FindViewIntegrationRow{}, pgx.ErrNoRows
	}
	return dbgen.FindViewIntegrationRow{ID: 5, RetentionAlertDetailsDays: 90}, f.fail
}

func (f *fakeView) pick(status pgtype.Text, contains []byte, resolvedSince time.Time, after func(viewRow) bool,
	less func(a, b viewRow) int, size int32) []viewRow {
	f.batches = append(f.batches, size)
	var want map[string]string
	_ = json.Unmarshal(contains, &want)
	var out []viewRow
	for _, r := range f.rows {
		var labels map[string]string
		_ = json.Unmarshal(r.Labels, &labels)
		ok := true
		for k, v := range want {
			ok = ok && labels[k] == v
		}
		if !ok || (status.Valid && r.Status != status.String) ||
			(r.Status == StatusResolved && r.ResolvedAt.Time.Before(resolvedSince)) || !after(r) {
			continue
		}
		out = append(out, r)
	}
	slices.SortFunc(out, less)
	return out[:min(len(out), int(size))]
}

func cmpAt(a, b time.Time, ai, bi int64) int {
	if c := a.Compare(b); c != 0 {
		return c
	}
	return int(ai - bi)
}

func (f *fakeView) ListViewAlertsByLastSeen(_ context.Context, arg dbgen.ListViewAlertsByLastSeenParams) (
	[]dbgen.ListViewAlertsByLastSeenRow, error) {
	rows := f.pick(arg.Status, arg.Contains, arg.ResolvedSince, func(r viewRow) bool {
		return !arg.AfterAt.Valid || cmpAt(r.LastSeenAt, arg.AfterAt.Time, r.ID, arg.AfterID.Int64) < 0
	}, func(a, b viewRow) int { return -cmpAt(a.LastSeenAt, b.LastSeenAt, a.ID, b.ID) }, arg.BatchSize)
	out := make([]dbgen.ListViewAlertsByLastSeenRow, len(rows))
	for i, r := range rows {
		out[i] = dbgen.ListViewAlertsByLastSeenRow(r)
	}
	return out, f.fail
}

func (f *fakeView) ListViewAlertsByLastSeenAsc(_ context.Context, arg dbgen.ListViewAlertsByLastSeenAscParams) (
	[]dbgen.ListViewAlertsByLastSeenAscRow, error) {
	rows := f.pick(arg.Status, arg.Contains, arg.ResolvedSince, func(r viewRow) bool {
		return !arg.AfterAt.Valid || cmpAt(r.LastSeenAt, arg.AfterAt.Time, r.ID, arg.AfterID.Int64) > 0
	}, func(a, b viewRow) int { return cmpAt(a.LastSeenAt, b.LastSeenAt, a.ID, b.ID) }, arg.BatchSize)
	out := make([]dbgen.ListViewAlertsByLastSeenAscRow, len(rows))
	for i, r := range rows {
		out[i] = dbgen.ListViewAlertsByLastSeenAscRow(r)
	}
	return out, f.fail
}

func (f *fakeView) ListViewAlertsByStartsAt(_ context.Context, arg dbgen.ListViewAlertsByStartsAtParams) (
	[]dbgen.ListViewAlertsByStartsAtRow, error) {
	rows := f.pick(arg.Status, arg.Contains, arg.ResolvedSince, func(r viewRow) bool {
		return !arg.AfterAt.Valid || cmpAt(r.StartsAt, arg.AfterAt.Time, r.ID, arg.AfterID.Int64) < 0
	}, func(a, b viewRow) int { return -cmpAt(a.StartsAt, b.StartsAt, a.ID, b.ID) }, arg.BatchSize)
	out := make([]dbgen.ListViewAlertsByStartsAtRow, len(rows))
	for i, r := range rows {
		out[i] = dbgen.ListViewAlertsByStartsAtRow(r)
	}
	return out, f.fail
}

func (f *fakeView) ListViewAlertsByStartsAtAsc(_ context.Context, arg dbgen.ListViewAlertsByStartsAtAscParams) (
	[]dbgen.ListViewAlertsByStartsAtAscRow, error) {
	rows := f.pick(arg.Status, arg.Contains, arg.ResolvedSince, func(r viewRow) bool {
		return !arg.AfterAt.Valid || cmpAt(r.StartsAt, arg.AfterAt.Time, r.ID, arg.AfterID.Int64) > 0
	}, func(a, b viewRow) int { return cmpAt(a.StartsAt, b.StartsAt, a.ID, b.ID) }, arg.BatchSize)
	out := make([]dbgen.ListViewAlertsByStartsAtAscRow, len(rows))
	for i, r := range rows {
		out[i] = dbgen.ListViewAlertsByStartsAtAscRow(r)
	}
	return out, f.fail
}

func (f *fakeView) ListViewGroupKeys(_ context.Context, arg dbgen.ListViewGroupKeysParams) (
	[]dbgen.ListViewGroupKeysRow, error) {
	var out []dbgen.ListViewGroupKeysRow
	for _, id := range arg.AlertIds {
		out = append(out, dbgen.ListViewGroupKeysRow{AlertID: id, GroupKey: fmt.Sprintf("{}:{n=\"%d\"}", id)})
	}
	return out, nil
}

// newView has 300 Alerts: odd ids firing, even ones resolved, every tenth resolved too long ago to show.
func newView() *fakeView {
	f := &fakeView{}
	for i := int64(1); i <= 300; i++ {
		labels, _ := json.Marshal(map[string]string{"n": fmt.Sprint(i), "team": []string{"db", "web"}[i%2],
			"pod": fmt.Sprintf("api-%d", i)})
		r := viewRow{ID: i, Fingerprint: fmt.Sprintf("%016x", i), Labels: labels, Annotations: []byte(`{"summary":"s"}`),
			Status: StatusFiring, StartsAt: t0.Add(time.Duration(300-i) * time.Minute),
			LastSeenAt: t0.Add(time.Duration(i%7) * time.Minute), FiredAt: t0}
		if i%2 == 0 {
			at := t0.Add(-time.Hour)
			if i%10 == 0 {
				at = t0.Add(-91 * 24 * time.Hour)
			}
			r.Status, r.ResolvedAt = StatusResolved, pgtype.Timestamptz{Time: at, Valid: true}
			r.ResolveReason = pgtype.Text{String: ResolveGone, Valid: true}
			r.ResolveReasonText = pgtype.Text{String: GoneReasonText, Valid: true}
		}
		if i == 3 {
			r.StaticLabelConflicts = []string{"cluster"}
		}
		f.rows = append(f.rows, r)
	}
	return f
}

func listAll(t *testing.T, v *AlertsView, f AlertFilter) []ViewAlert {
	t.Helper()
	var all []ViewAlert
	for range 100 {
		page, err := v.List(t.Context(), f)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, page.Alerts...)
		if page.Next == nil {
			return all
		}
		f.After = page.Next
	}
	t.Fatal("the pages never ended")
	return nil
}

// TestAlertsView is C-06.FR-19: firing Alerts and those resolved within retention.alert_details, filtered by state,
// Matchers and text, sorted by last seen or startsAt with a cursor, each with its groupKeys and warnings.
func TestAlertsView(t *testing.T) {
	store := newView()
	v := NewAlertsView(1, store, clock.NewManual(t0))
	all := listAll(t, v, AlertFilter{Integration: "NTAAAAAAAAAAAA", Limit: 7})
	if len(all) != 270 {
		t.Fatalf("%d alerts, want 270", len(all))
	}
	for i := 1; i < len(all); i++ {
		if cmpAt(all[i].LastSeenAt, all[i-1].LastSeenAt, all[i].ID, all[i-1].ID) >= 0 {
			t.Fatalf("not sorted by -last_seen_at at %d", i)
		}
	}
	a3 := all[slices.IndexFunc(all, func(a ViewAlert) bool { return a.ID == 3 })]
	if !slices.Equal(a3.Warnings, []string{"cluster"}) || !slices.Equal(a3.GroupKeys, []string{`{}:{n="3"}`}) ||
		a3.State != StatusFiring || a3.Reason != nil || a3.Annotations["summary"] != "s" {
		t.Errorf("alert 3 = %+v", a3)
	}
	a2 := all[slices.IndexFunc(all, func(a ViewAlert) bool { return a.ID == 2 })]
	if a2.State != StatusResolved || *a2.Reason != ResolveGone || *a2.ReasonText != GoneReasonText ||
		a2.ResolvedAt == nil || a2.Warnings == nil {
		t.Errorf("alert 2 = %+v", a2)
	}
	for _, sort := range []string{SortLastSeen, SortStarts, SortStartsDesc} {
		got := listAll(t, v, AlertFilter{Integration: "NTAAAAAAAAAAAA", Sort: sort, Limit: 50})
		if len(got) != 270 {
			t.Errorf("%s: %d alerts", sort, len(got))
		}
		for i := 1; i < len(got); i++ {
			at, prev := positionOf(got[i], sort), positionOf(got[i-1], sort)
			c := cmpAt(at, prev, got[i].ID, got[i-1].ID)
			if (sort == SortStartsDesc && c >= 0) || (sort != SortStartsDesc && c <= 0) {
				t.Fatalf("%s: not sorted at %d", sort, i)
			}
		}
	}
	m := func(s string) matchers.Matcher {
		mm, err := matchers.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		return mm
	}
	for _, tt := range []struct {
		f    AlertFilter
		want int
	}{
		{AlertFilter{State: StatusFiring}, 150},
		{AlertFilter{State: StatusResolved}, 120},
		{AlertFilter{Matchers: []matchers.Matcher{m(`team="db"`)}}, 120},
		{AlertFilter{Matchers: []matchers.Matcher{m(`team="db"`), m(`team="web"`)}}, 0},
		{AlertFilter{Matchers: []matchers.Matcher{m(`pod=~"api-1.*"`), m(`team!="web"`)}}, 44},
		{AlertFilter{Matchers: []matchers.Matcher{m(`missing=""`)}, State: StatusFiring}, 150},
		{AlertFilter{Query: "API-29"}, 10},
		{AlertFilter{Query: "nothing"}, 0},
	} {
		tt.f.Integration, tt.f.Limit = "NTAAAAAAAAAAAA", 9
		if got := listAll(t, v, tt.f); len(got) != tt.want {
			t.Errorf("%+v: %d alerts, want %d", tt.f, len(got), tt.want)
		}
	}
}

// TestAlertsViewScanCap: a filter that matches few Alerts reads at most maxScan of them per request and answers a
// short page with a cursor to continue from.
func TestAlertsViewScanCap(t *testing.T) {
	store := &fakeView{}
	for i := int64(1); i <= maxScan+500; i++ {
		store.rows = append(store.rows, viewRow{ID: i, Fingerprint: fmt.Sprint(i), Labels: []byte(`{"a":"b"}`),
			Annotations: []byte(`{}`), Status: StatusFiring, StartsAt: t0, LastSeenAt: t0})
	}
	labels, _ := json.Marshal(map[string]string{"a": "needle"})
	store.rows[0].Labels = labels
	v := NewAlertsView(1, store, clock.NewManual(t0))
	f := AlertFilter{Integration: "NTAAAAAAAAAAAA", Query: "needle", Limit: 50}
	page, err := v.List(t.Context(), f)
	if err != nil || len(page.Alerts) != 0 || page.Next == nil {
		t.Fatalf("first request: %d alerts, next %v, %v", len(page.Alerts), page.Next, err)
	}
	f.After = page.Next
	page, err = v.List(t.Context(), f)
	if err != nil || len(page.Alerts) != 1 || page.Alerts[0].ID != 1 || page.Next != nil {
		t.Errorf("second request: %+v, %v", page, err)
	}
}

func TestAlertsViewErrors(t *testing.T) {
	store := newView()
	v := NewAlertsView(1, store, clock.NewManual(t0))
	for _, id := range []string{"nope", "NTBBBBBBBBBBBB"} {
		if _, err := v.List(t.Context(), AlertFilter{Integration: id}); !errors.Is(err, integrations.ErrNotFound) {
			t.Errorf("%s: %v", id, err)
		}
	}
	store.fail = errors.New("down")
	if _, err := v.List(t.Context(), AlertFilter{Integration: "NTAAAAAAAAAAAA"}); err == nil {
		t.Error("no error")
	}
	store.fail = nil
	store.rows[0].Labels = []byte(`[]`)
	if _, err := v.List(t.Context(), AlertFilter{Integration: "NTAAAAAAAAAAAA", Sort: SortStartsDesc}); err == nil {
		t.Error("bad labels: no error")
	}
	store.rows[0].Labels, store.rows[0].Annotations = []byte(`{}`), []byte(`1`)
	if _, err := v.List(t.Context(), AlertFilter{Integration: "NTAAAAAAAAAAAA", Sort: SortStartsDesc}); err == nil {
		t.Error("bad annotations: no error")
	}
	if !containsValue(map[string]string{"a": "Hello"}, "") || !containsValue(map[string]string{"a": "Hello"}, "ell") ||
		containsValue(maps.Clone(map[string]string{}), "x") {
		t.Error("containsValue")
	}
}

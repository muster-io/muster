// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/ingest/dbgen"
)

func (s *fakeStore) ListPreviewIntegrations(_ context.Context, org int64) ([]dbgen.ListPreviewIntegrationsRow,
	error) {
	if err := s.fail["ListPreviewIntegrations"]; err != nil || org != orgID {
		return nil, err
	}
	var out []dbgen.ListPreviewIntegrationsRow
	for id, labels := range s.static {
		out = append(out, dbgen.ListPreviewIntegrationsRow{ID: id, StaticLabels: []byte(labels)})
	}
	return out, nil
}

// ListPreviewBodies groups the Stored Snapshots of the day as the query does, the failed ones left out.
func (s *fakeStore) ListPreviewBodies(_ context.Context, arg dbgen.ListPreviewBodiesParams) (
	[]dbgen.ListPreviewBodiesRow, error) {
	if err := s.fail["ListPreviewBodies"]; err != nil || arg.OrgID != orgID {
		return nil, err
	}
	s.pages = append(s.pages, arg)
	byKey := map[string]*dbgen.ListPreviewBodiesRow{}
	var rows []*dbgen.ListPreviewBodiesRow
	for _, sn := range s.snapshots {
		if !sn.BodyDay.Time.Equal(arg.BodyDay.Time) || sn.ReceivedAt.Before(arg.ReceivedFrom) ||
			!sn.ReceivedAt.Before(arg.ReceivedTo) || s.failed[sn.ReceivedAt] {
			continue
		}
		k := fmt.Sprint(sn.IntegrationID, string(sn.BodySha256))
		r, ok := byKey[k]
		if !ok {
			r = &dbgen.ListPreviewBodiesRow{IntegrationID: sn.IntegrationID, BodySha256: sn.BodySha256}
			byKey[k] = r
			rows = append(rows, r)
		}
		if sn.ReceivedAt.After(r.ReceivedAt) {
			r.ReceivedAt = sn.ReceivedAt
		}
		r.SizeBytes = max(r.SizeBytes, sn.SizeBytes)
	}
	order := func(a, b *dbgen.ListPreviewBodiesRow) int {
		return cmp.Or(b.ReceivedAt.Compare(a.ReceivedAt), cmp.Compare(a.IntegrationID, b.IntegrationID),
			bytes.Compare(a.BodySha256, b.BodySha256))
	}
	slices.SortFunc(rows, order)
	var out []dbgen.ListPreviewBodiesRow
	for _, r := range rows {
		if arg.AfterAt.Valid && order(r, &dbgen.ListPreviewBodiesRow{ReceivedAt: arg.AfterAt.Time,
			IntegrationID: arg.AfterIntegrationID.Int64, BodySha256: arg.AfterSha256}) <= 0 {
			continue
		}
		if len(out) == int(arg.PageSize) {
			break
		}
		out = append(out, *r)
	}
	return out, nil
}

func (s *fakeStore) ListPreviewSnapshotBodies(_ context.Context, arg dbgen.ListPreviewSnapshotBodiesParams) (
	[]dbgen.ListPreviewSnapshotBodiesRow, error) {
	if err := s.fail["ListPreviewSnapshotBodies"]; err != nil || arg.OrgID != orgID {
		return nil, err
	}
	s.batches = append(s.batches, len(arg.BodySha256s))
	var out []dbgen.ListPreviewSnapshotBodiesRow
	for i, sha := range arg.BodySha256s {
		if b, ok := s.bodies[string(sha)+arg.BodyDays[i].Time.Format(time.DateOnly)]; ok && !s.dropped {
			out = append(out, dbgen.ListPreviewSnapshotBodiesRow{BodySha256: sha, BodyDay: arg.BodyDays[i], Body: b})
		}
	}
	return out, nil
}

// previewBody is a webhook whose Alerts are given as name=status, each with the labels alertname=Disk, node=name
// and, for a name starting with c, cluster=a.
func previewBody(alerts ...string) []byte {
	var wire []string
	for _, a := range alerts {
		name, status, _ := strings.Cut(a, "=")
		cluster := ""
		if strings.HasPrefix(name, "c") {
			cluster = `,"cluster":"a"`
		}
		wire = append(wire, `{"status":"`+status+`","labels":{"alertname":"Disk","node":"`+name+`"`+cluster+
			`},"startsAt":"2026-10-06T11:00:00Z","fingerprint":"`+name+`"}`)
	}
	return []byte(`{"groupKey":"{}:{}","status":"firing","alerts":[` + strings.Join(wire, ",") + `]}`)
}

// received stores body for the Integration at.
func received(t *testing.T, svc *Service, c *clock.Manual, at time.Time, integration int64, body []byte) {
	t.Helper()
	c.Set(at)
	if _, err := svc.Store(t.Context(), Received{IntegrationID: integration, Body: body}); err != nil {
		t.Fatal(err)
	}
}

// visited is what Firing handed to visit: one line per body, its Alerts as integration/fingerprint/labels.
func visited(batches [][]FiringAlert) []string {
	var out []string
	for _, b := range batches {
		var line []string
		for _, a := range b {
			labels := make([]string, 0, len(a.Labels))
			for _, k := range slices.Sorted(maps.Keys(a.Labels)) {
				labels = append(labels, k+"="+a.Labels[k])
			}
			line = append(line, fmt.Sprintf("%d/%s/%s", a.IntegrationID, a.Fingerprint, strings.Join(labels, ",")))
		}
		out = append(out, strings.Join(line, " "))
	}
	return out
}

// TestFiring is the read of the Group key preview (C-08.FR-5): the webhook Stored Snapshots since the start of the
// period, newest first across days and Integrations, each distinct body of an Integration once, the failed ones and
// the bodies that do not parse skipped, only the Alerts reported firing, with the current Static labels of their
// Integration, the Alert's own label winning.
func TestFiring(t *testing.T) {
	store := newStore()
	store.static = map[int64]string{5: `{"env":"prod","cluster":"z"}`, 6: `{}`}
	store.failed = map[time.Time]bool{}
	c := clock.NewManual(t0)
	svc := New(orgID, store, c)
	day := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	repeat := previewBody("c1=firing", "m1=resolved")
	received(t, svc, c, day.Add(-time.Hour), 5, previewBody("old=firing"))       // before the period
	received(t, svc, c, day.Add(2*time.Hour), 5, previewBody("m2=firing"))       // first day
	received(t, svc, c, day.Add(3*time.Hour), 5, repeat)                         // first day, repeated the next day
	received(t, svc, c, day.Add(26*time.Hour), 5, repeat)                        // second day: the newest copy
	received(t, svc, c, day.Add(27*time.Hour), 6, repeat)                        // another Integration
	received(t, svc, c, day.Add(28*time.Hour), 5, []byte(`{"not":"a webhook"}`)) // does not parse
	received(t, svc, c, day.Add(29*time.Hour), 5, previewBody("m3=resolved"))    // nothing firing
	received(t, svc, c, day.Add(30*time.Hour), 5, previewBody("f1=firing"))      // failed
	store.failed[day.Add(30*time.Hour)] = true
	c.Set(day.Add(31 * time.Hour))

	var got [][]FiringAlert
	read, err := svc.Firing(t.Context(), day.Add(time.Hour), func(b []FiringAlert) bool {
		got = append(got, b)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"6/c1/alertname=Disk,cluster=a,node=c1",
		"5/c1/alertname=Disk,cluster=a,env=prod,node=c1",
		"5/m2/alertname=Disk,cluster=z,env=prod,node=m2",
	}
	if !slices.Equal(visited(got), want) || read.Bodies != 5 || read.Unread {
		t.Errorf("read %+v\n%s", read, strings.Join(visited(got), "\n"))
	}
	if len(store.pages) != 2 || !store.pages[0].ReceivedFrom.Equal(day.AddDate(0, 0, 1)) ||
		!store.pages[0].ReceivedTo.Equal(day.Add(31*time.Hour+time.Microsecond)) ||
		!store.pages[1].ReceivedFrom.Equal(day.Add(time.Hour)) || !store.pages[1].ReceivedTo.Equal(day.AddDate(0, 0, 1)) {
		t.Errorf("pages %+v", store.pages)
	}
	// The same body of two Integrations is read once.
	if !slices.Equal(store.batches, []int{4}) {
		t.Errorf("batches %v", store.batches)
	}

	// Stopping: on the last body, nothing is left; before it, the rest is unread.
	store.pages = nil
	read, err = svc.Firing(t.Context(), day.Add(time.Hour), func([]FiringAlert) bool { return false })
	if err != nil || !read.Unread || read.Bodies != 3 {
		t.Errorf("stop at the first = %+v, %v", read, err)
	}
	n := 0
	read, err = svc.Firing(t.Context(), day.Add(time.Hour), func([]FiringAlert) bool { n++; return n < 3 })
	if err != nil || read.Unread {
		t.Errorf("stop at the last = %+v, %v", read, err)
	}
	n = 0
	read, err = svc.Firing(t.Context(), day.Add(25*time.Hour), func([]FiringAlert) bool { n++; return n < 2 })
	if err != nil || read.Unread {
		t.Errorf("stop at the last of a shorter period = %+v, %v", read, err)
	}
}

// TestFiringBounds reads a period of many bodies a bounded page and batch at a time: pages of firingPage distinct
// bodies, batches of at most firingBodyBatch bodies and about firingBatchBytes; a stop at the end of a batch looks
// ahead for what is unread.
func TestFiringBounds(t *testing.T) {
	page := firingPage
	firingPage = 100
	t.Cleanup(func() { firingPage = page })
	n := int(firingPage)
	store := newStore()
	store.static = map[int64]string{5: `{}`}
	c := clock.NewManual(t0)
	svc := New(orgID, store, c)
	start := t0.Add(-10 * time.Hour)
	for i := range n + 10 {
		received(t, svc, c, start.Add(time.Duration(i)*time.Second), 5, previewBody(fmt.Sprintf("n%d=firing", i)))
	}
	count := 0
	read, err := svc.Firing(t.Context(), start, func(b []FiringAlert) bool {
		count += len(b)
		return true
	})
	if err != nil || count != n+10 || read.Bodies != n+10 || len(store.pages) != 2 ||
		slices.Max(store.batches) != firingBodyBatch {
		t.Errorf("read %+v of %d, pages %d, batches %v, %v", read, count, len(store.pages), store.batches, err)
	}
	count = 0
	read, err = svc.Firing(t.Context(), start, func([]FiringAlert) bool {
		count++
		return count < firingBodyBatch
	})
	if err != nil || !read.Unread || read.Bodies != firingBodyBatch {
		t.Errorf("stop at the end of the first batch = %+v, %v", read, err)
	}

	big := newStore()
	big.static = map[int64]string{5: `{}`}
	svc = New(orgID, big, c)
	for i := range 4 {
		body := previewBody(fmt.Sprintf("b%d=firing", i))
		body = append(body[:len(body)-1], []byte(`,"pad":"`+strings.Repeat("x", 3<<20)+`"}`)...)
		received(t, svc, c, start.Add(time.Duration(i)*time.Second), 5, body)
	}
	if _, err := svc.Firing(t.Context(), start, func([]FiringAlert) bool { return true }); err != nil ||
		!slices.Equal(big.batches, []int{3, 1}) {
		t.Errorf("batches of large bodies %v, %v", big.batches, err)
	}
}

// TestFiringFailures returns the errors of the queries, and Retention is retention.stored_snapshots.
func TestFiringFailures(t *testing.T) {
	boom := errors.New("boom")
	for _, name := range []string{"ListPreviewIntegrations", "ListPreviewBodies", "ListPreviewSnapshotBodies"} {
		store := newStore()
		store.static = map[int64]string{5: `{}`}
		c := clock.NewManual(t0)
		svc := New(orgID, store, c)
		received(t, svc, c, t0, 5, previewBody("a=firing"))
		store.fail[name] = boom
		if _, err := svc.Firing(t.Context(), t0.Add(-time.Hour), func([]FiringAlert) bool { return true }); !errors.Is(err,
			boom) {
			t.Errorf("%s: %v", name, err)
		}
	}
	store := newStore()
	store.static = map[int64]string{5: `[]`}
	svc := New(orgID, store, clock.NewManual(t0))
	if _, err := svc.Firing(t.Context(), t0, func([]FiringAlert) bool { return true }); err == nil {
		t.Error("static labels that are not an object")
	}

	// A body dropped by retention between the listing and the read is skipped.
	store = newStore()
	store.static = map[int64]string{5: `{}`}
	c := clock.NewManual(t0)
	svc = New(orgID, store, c)
	received(t, svc, c, t0, 5, previewBody("a=firing"))
	store.dropped = true
	if read, err := svc.Firing(t.Context(), t0.Add(-time.Hour), func([]FiringAlert) bool { return true }); err != nil ||
		read.Bodies != 0 {
		t.Errorf("dropped = %+v, %v", read, err)
	}

	if d, err := svc.Retention(t.Context()); err != nil || d != 14*24*time.Hour {
		t.Errorf("retention = %v, %v", d, err)
	}
	store.fail["GetRetention"] = boom
	if _, err := svc.Retention(t.Context()); !errors.Is(err, boom) {
		t.Errorf("retention: %v", err)
	}
}

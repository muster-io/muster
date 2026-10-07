// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/logging"
)

// fakeSnapshots are the Stored Snapshots of the period, newest first, each as the firing Alerts of one body.
type fakeSnapshots struct {
	retention    time.Duration
	retentionErr error
	bodies       [][]ingest.FiringAlert
	err          error
	since        []time.Time
}

func (f *fakeSnapshots) Retention(context.Context) (time.Duration, error) {
	return f.retention, f.retentionErr
}

func (f *fakeSnapshots) Firing(_ context.Context, since time.Time, visit func([]ingest.FiringAlert) bool) (
	ingest.FiringRead, error) {
	f.since = append(f.since, since)
	if f.err != nil {
		return ingest.FiringRead{}, f.err
	}
	for i, b := range f.bodies {
		if !visit(b) {
			return ingest.FiringRead{Bodies: i + 1, Unread: i < len(f.bodies)-1}, nil
		}
	}
	return ingest.FiringRead{Bodies: len(f.bodies)}, nil
}

// firing is a body of the Integration 5 with Alerts given as fingerprint=labels, labels as name:value,...
func firing(alerts ...string) []ingest.FiringAlert {
	out := make([]ingest.FiringAlert, 0, len(alerts))
	for _, a := range alerts {
		fp, labels, _ := strings.Cut(a, "=")
		m := map[string]string{}
		for l := range strings.SplitSeq(labels, ",") {
			if name, value, ok := strings.Cut(l, ":"); ok {
				m[name] = value
			}
		}
		out = append(out, ingest.FiringAlert{IntegrationID: 5, Fingerprint: fp, Labels: m})
	}
	return out
}

func newPreviewService(t *testing.T) (*Service, *fakeStore, *fakeSnapshots, *bytes.Buffer) {
	t.Helper()
	store := newStore()
	c := clock.NewManual(t0)
	if err := EnsureDefault(t.Context(), store, orgID, t0); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	logger := logging.New(&log, logging.LevelInfo)
	snaps := &fakeSnapshots{retention: 14 * 24 * time.Hour}
	return New(Config{OrgID: orgID, Store: store, Audit: audit.NewWriter(logger, c), Business: c, Real: c,
		Snapshots: snaps, Log: logger, Router: newRouter(store)}), store, snaps, &log
}

// examples is a side as count:[value of label,alerts]...
func examples(side PreviewSide, label string) string {
	var out []string
	for _, e := range side.Examples {
		out = append(out, fmt.Sprintf("[%s,%d]", e.GroupKeyValues[label], e.AlertCount))
	}
	return fmt.Sprintf("%d:%s", side.AlertGroupCount, strings.Join(out, ""))
}

func previewed(t *testing.T, log *bytes.Buffer) map[string]any {
	t.Helper()
	var last map[string]any
	for line := range strings.Lines(log.String()) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		if m["event"] == "group_key_previewed" {
			last = m
		}
	}
	if last == nil {
		t.Fatal("no group_key_previewed line")
	}
	return last
}

// TestPreview is C-08.FR-5, C-08.AC-2 and C-08.AC-12: an unsaved Route stands just before the Default route and
// shows the proposed side alone; a saved Route at its place shows both, with its own Matchers or the given ones.
// Each Alert counts once, with its labels from the newest body listing it, and only when the Route takes it in the
// evaluation order; a missing label groups with the others missing it.
func TestPreview(t *testing.T) {
	svc, _, snaps, log := newPreviewService(t)
	ctx := t.Context()
	if _, err := svc.Create(ctx, by, input("team-x", Matcher{Label: "team", Op: "=", Value: "x"})); err != nil {
		t.Fatal(err)
	}
	snaps.bodies = [][]ingest.FiringAlert{
		firing("a1=alertname:Disk,cluster:a,node:a1", "m1=alertname:Disk,node:m1"),
		firing("a2=alertname:Disk,cluster:a,node:a2", "x1=alertname:Disk,team:x", "o1=alertname:CPU"),
		// Older copies: m1 with another cluster counts with its newest labels, a1 once.
		firing("m1=alertname:Disk,cluster:b,node:m1", "a1=alertname:Disk,cluster:a,node:a1",
			"m2=alertname:Disk,node:m2"),
	}
	snaps.bodies[2] = append(snaps.bodies[2], ingest.FiringAlert{IntegrationID: 6, Fingerprint: "a1",
		Labels: map[string]string{"alertname": "Disk", "cluster": "c"}})
	disk := []Matcher{{Label: "alertname", Op: "=", Value: "Disk"}}

	p, err := svc.Preview(ctx, PreviewRequest{Matchers: disk, ProposedGroupKey: []string{"alertname", "cluster"}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Current != nil || p.Truncated || p.PeriodSeconds != 86400 || examples(p.Proposed, "cluster") != "3:[,2][a,2][c,1]" ||
		!snaps.since[0].Equal(t0.Add(-24*time.Hour)) {
		t.Errorf("unsaved %+v %s", p, examples(p.Proposed, "cluster"))
	}
	if v, ok := p.Proposed.Examples[0].GroupKeyValues["cluster"]; !ok || v != "" ||
		p.Proposed.Examples[0].GroupKeyValues["alertname"] != "Disk" {
		t.Errorf("a missing label is not the empty value: %+v", p.Proposed.Examples[0])
	}
	line := previewed(t, log)
	if line["route"] != "" || line["period_seconds"] != 86400.0 || line["snapshots_read"] != 3.0 ||
		line["truncated"] != false || line["duration_ms"] != 0.0 || line["level"] != "INFO" {
		t.Errorf("log %v", line)
	}

	saved, err := svc.Create(ctx, by, Input{Name: "disk", Matchers: disk, GroupKey: []string{"alertname"},
		Policy: onCall().Policy})
	if err != nil {
		t.Fatal(err)
	}
	period := int64(3600)
	p, err = svc.Preview(ctx, PreviewRequest{RouteID: saved.PublicID, ProposedGroupKey: []string{"alertname", "cluster"},
		PeriodSeconds: &period})
	if err != nil {
		t.Fatal(err)
	}
	if p.Current == nil || examples(*p.Current, "alertname") != "1:[Disk,5]" ||
		examples(p.Proposed, "cluster") != "3:[,2][a,2][c,1]" || p.PeriodSeconds != 3600 ||
		!snaps.since[1].Equal(t0.Add(-time.Hour)) {
		t.Errorf("saved %+v", p)
	}
	if line = previewed(t, log); line["route"] != saved.PublicID || line["period_seconds"] != 3600.0 {
		t.Errorf("log %v", line)
	}

	// Other Matchers on the saved Route, and an unsaved Route, leave the Router's order alone.
	cached := svc.router.cached
	routes := slices.Clone(cached.routes)
	p, err = svc.Preview(ctx, PreviewRequest{RouteID: saved.PublicID, ProposedGroupKey: []string{"node"},
		Matchers: []Matcher{{Label: "node", Op: "=~", Value: "a.*"}}})
	if err != nil || examples(*p.Current, "alertname") != "1:[Disk,2]" || examples(p.Proposed, "node") != "2:[a1,1][a2,1]" {
		t.Errorf("other matchers %+v, %v", p, err)
	}
	if _, err := svc.Preview(ctx, PreviewRequest{Matchers: disk}); err != nil {
		t.Fatal(err)
	}
	if svc.router.cached != cached || len(cached.routes) != len(routes) || !slices.EqualFunc(cached.routes, routes,
		func(a, b compiled) bool { return a.id == b.id && slices.Equal(a.matchers, b.matchers) }) {
		t.Errorf("the cached order changed: %+v", svc.router.cached.routes)
	}

	// A Route after one that takes the same Alerts takes none; the Default route takes what no other Route takes.
	if _, err := svc.Reorder(ctx, by, nil, []string{saved.PublicID, routeIDOf(t, svc, "team-x")}); err != nil {
		t.Fatal(err)
	}
	p, err = svc.Preview(ctx, PreviewRequest{RouteID: routeIDOf(t, svc, "team-x"), ProposedGroupKey: []string{}})
	if err != nil || examples(*p.Current, "alertname") != "0:" || p.Proposed.AlertGroupCount != 0 ||
		p.Proposed.Examples == nil {
		t.Errorf("shadowed %+v, %v", p, err)
	}
	p, err = svc.Preview(ctx, PreviewRequest{RouteID: routeIDOf(t, svc, DefaultName), ProposedGroupKey: []string{}})
	if err != nil || examples(*p.Current, "alertname") != "1:[CPU,1]" || p.Proposed.AlertGroupCount != 1 ||
		len(p.Proposed.Examples[0].GroupKeyValues) != 0 {
		t.Errorf("default %+v, %v", p, err)
	}
	// An unsaved Route with no Matchers takes what the Routes before the Default route leave.
	p, err = svc.Preview(ctx, PreviewRequest{Matchers: []Matcher{}, ProposedGroupKey: []string{"alertname"}})
	if err != nil || examples(p.Proposed, "alertname") != "1:[CPU,1]" {
		t.Errorf("no matchers %+v, %v", p, err)
	}
}

func routeIDOf(t *testing.T, svc *Service, name string) string {
	t.Helper()
	list, err := svc.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range list.Routes {
		if r.Name == name {
			return r.PublicID
		}
	}
	t.Fatalf("no route %s", name)
	return ""
}

// TestPreviewRefusals: neither a Route nor Matchers is one_of_required, a period past retention.stored_snapshots or
// below a second is out_of_range, a bad proposed key or Matcher points at its field, and an unknown Route is
// ErrNotFound; the default period is at most the retention.
func TestPreviewRefusals(t *testing.T) {
	svc, _, snaps, _ := newPreviewService(t)
	ctx := t.Context()
	disk := []Matcher{{Label: "alertname", Op: "=", Value: "Disk"}}
	for _, tt := range []struct {
		req           PreviewRequest
		pointer, code string
	}{
		{PreviewRequest{ProposedGroupKey: []string{"alertname"}}, "", CodeOneOfRequired},
		{PreviewRequest{Matchers: disk, PeriodSeconds: ptr(int64(14*86400 + 1))}, "/period_seconds", CodeOutOfRange},
		{PreviewRequest{Matchers: disk, PeriodSeconds: ptr(int64(0))}, "/period_seconds", CodeOutOfRange},
		{PreviewRequest{Matchers: disk, ProposedGroupKey: []string{"a", "a"}}, "/proposed_group_key/1", CodeDuplicate},
		{PreviewRequest{Matchers: disk, ProposedGroupKey: []string{""}}, "/proposed_group_key/0", CodeInvalidFormat},
		{PreviewRequest{Matchers: []Matcher{{Label: "a", Op: "=~", Value: "("}}}, "/matchers/0/value", CodeInvalidRegex},
	} {
		_, err := svc.Preview(ctx, tt.req)
		if f := fieldError(t, err); f.Pointer != tt.pointer || f.Code != tt.code {
			t.Errorf("%+v = %+v", tt.req, f)
		}
	}
	if _, err := svc.Preview(ctx, PreviewRequest{RouteID: "RTZZZZZZZZZZZZ"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown route: %v", err)
	}
	if p, err := svc.Preview(ctx, PreviewRequest{Matchers: disk, PeriodSeconds: ptr(int64(14 * 86400))}); err != nil ||
		p.PeriodSeconds != 14*86400 {
		t.Errorf("the whole retention = %+v, %v", p, err)
	}
	snaps.retention = 12 * time.Hour
	if p, err := svc.Preview(ctx, PreviewRequest{Matchers: disk}); err != nil || p.PeriodSeconds != 12*3600 {
		t.Errorf("default past the retention = %+v, %v", p, err)
	}
}

// TestPreviewLimit is routing.group_key_preview_max_alerts: the preview stops at PreviewMaxAlerts Alerts the Route
// takes, and is truncated only when one more, or a body, was left unread.
func TestPreviewLimit(t *testing.T) {
	svc, _, snaps, log := newPreviewService(t)
	alerts := func(prefix string, n int) []ingest.FiringAlert {
		out := make([]ingest.FiringAlert, n)
		for i := range out {
			out[i] = ingest.FiringAlert{IntegrationID: 5, Fingerprint: fmt.Sprintf("%s%d", prefix, i),
				Labels: map[string]string{"alertname": "Disk", "node": fmt.Sprint(i % 7)}}
		}
		return out
	}
	other := ingest.FiringAlert{IntegrationID: 5, Fingerprint: "other", Labels: map[string]string{"alertname": "CPU"}}
	disk := []Matcher{{Label: "alertname", Op: "=", Value: "Disk"}}
	for _, tt := range []struct {
		name      string
		bodies    [][]ingest.FiringAlert
		truncated bool
	}{
		{"exactly the limit", [][]ingest.FiringAlert{alerts("a", PreviewMaxAlerts)}, false},
		{"one more in the body", [][]ingest.FiringAlert{alerts("a", PreviewMaxAlerts+1)}, true},
		{"only repeats and other Alerts after it", [][]ingest.FiringAlert{
			append(alerts("a", PreviewMaxAlerts), append(alerts("a", 3), other)...)}, false},
		{"a body after it", [][]ingest.FiringAlert{alerts("a", PreviewMaxAlerts), alerts("b", 1)}, true},
	} {
		snaps.bodies = tt.bodies
		p, err := svc.Preview(t.Context(), PreviewRequest{Matchers: disk, ProposedGroupKey: []string{"node"}})
		if err != nil {
			t.Fatal(err)
		}
		if p.Truncated != tt.truncated || previewed(t, log)["truncated"] != tt.truncated {
			t.Errorf("%s: truncated %v", tt.name, p.Truncated)
		}
		if got := examples(p.Proposed, "node"); got != "7:[0,1429][1,1429][2,1429][3,1429][4,1428]" {
			t.Errorf("%s: examples %s", tt.name, got)
		}
	}
}

// TestPreviewFailures returns the errors of the reads, and a saved Route that left the evaluation order since it
// was read is ErrNotFound.
func TestPreviewFailures(t *testing.T) {
	boom := errors.New("boom")
	disk := []Matcher{{Label: "alertname", Op: "=", Value: "Disk"}}
	svc, store, snaps, _ := newPreviewService(t)
	snaps.retentionErr = boom
	if _, err := svc.Preview(t.Context(), PreviewRequest{Matchers: disk}); !errors.Is(err, boom) {
		t.Errorf("retention: %v", err)
	}
	snaps.retentionErr, snaps.err = nil, boom
	if _, err := svc.Preview(t.Context(), PreviewRequest{Matchers: disk}); !errors.Is(err, boom) {
		t.Errorf("firing: %v", err)
	}
	snaps.err = nil
	for _, name := range []string{"GetRoutingStamp", "ListRoutes", "GetRoute"} {
		store.fail[name] = boom
		svc.router.Invalidate()
		if _, err := svc.Preview(t.Context(), PreviewRequest{RouteID: defaultRouteID(store), Matchers: disk}); !errors.Is(err,
			boom) {
			t.Errorf("%s: %v", name, err)
		}
		delete(store.fail, name)
	}

	rt, err := svc.Create(t.Context(), by, input("gone", disk...))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.GetRoutingStamp(t.Context(), orgID)
	if err != nil {
		t.Fatal(err)
	}
	// The cached order of the current stamp without the Route, as if it was deleted between the two reads.
	svc.router.cached = &order{stamp: stamp{order: st.RouteOrderVersion, versions: st.RouteVersions},
		routes: []compiled{{id: 1}}}
	if _, err := svc.Preview(t.Context(), PreviewRequest{RouteID: rt.PublicID}); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted meanwhile: %v", err)
	}
}

// defaultRouteID is the public_id of the Default route of the store.
func defaultRouteID(store *fakeStore) string {
	for _, r := range store.live() {
		if r.IsDefault {
			return r.PublicID
		}
	}
	return ""
}

// TestSideOf groups by the Group key: the largest groups first and, at the same size, by their values; no Alerts
// make no group.
func TestSideOf(t *testing.T) {
	var alerts []map[string]string
	for i, n := range []int{1, 3, 2, 3, 1, 1, 2} {
		for range n {
			alerts = append(alerts, map[string]string{"k": fmt.Sprint(i)})
		}
	}
	side := sideOf(alerts, []string{"k"})
	if got := examples(side, "k"); got != "7:[1,3][3,3][2,2][6,2][0,1]" {
		t.Errorf("side %s", got)
	}
	if side = sideOf(nil, []string{"k"}); side.AlertGroupCount != 0 || side.Examples == nil ||
		len(side.Examples) != 0 {
		t.Errorf("empty %+v", side)
	}
	if side = sideOf(alerts, nil); side.AlertGroupCount != 1 || side.Examples[0].AlertCount != len(alerts) ||
		len(side.Examples[0].GroupKeyValues) != 0 {
		t.Errorf("no key %+v", side)
	}
}

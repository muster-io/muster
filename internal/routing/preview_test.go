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

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/routing/dbgen"
	teamsdb "github.com/muster-io/muster/internal/teams/dbgen"
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

// firingAt is firing with the startsAt of every Alert set to at.
func firingAt(at time.Time, alerts ...string) []ingest.FiringAlert {
	out := firing(alerts...)
	for i := range out {
		out[i].StartsAt = at
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
		Policy: paging().Policy})
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
	var alerts []previewAlert
	for i, n := range []int{1, 3, 2, 3, 1, 1, 2} {
		for range n {
			alerts = append(alerts, previewAlert{labels: map[string]string{"k": fmt.Sprint(i)}})
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

func (s *fakeStore) ListPreviewTeams(_ context.Context, arg dbgen.ListPreviewTeamsParams) (
	[]dbgen.ListPreviewTeamsRow, error) {
	if err := s.call("ListPreviewTeams"); err != nil || arg.OrgID != orgID {
		return nil, err
	}
	var out []dbgen.ListPreviewTeamsRow
	for _, r := range s.previewTeams {
		if slices.Contains(arg.TeamIds, r.ID) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *fakeStore) ListPreviewTeamEntries(_ context.Context, arg dbgen.ListPreviewTeamEntriesParams) (
	[]dbgen.ListPreviewTeamEntriesRow, error) {
	if err := s.call("ListPreviewTeamEntries"); err != nil || arg.OrgID != orgID {
		return nil, err
	}
	var out []dbgen.ListPreviewTeamEntriesRow
	for _, r := range s.teamEntries {
		if slices.Contains(arg.TeamIds, r.TeamID) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *fakeStore) ListPreviewDestinations(_ context.Context, arg dbgen.ListPreviewDestinationsParams) (
	[]dbgen.ListPreviewDestinationsRow, error) {
	if err := s.call("ListPreviewDestinations"); err != nil || arg.OrgID != orgID {
		return nil, err
	}
	var out []dbgen.ListPreviewDestinationsRow
	for _, r := range s.previewDests {
		if slices.Contains(arg.Ids, r.ID) {
			out = append(out, r)
		}
	}
	return out, nil
}

const (
	destCritical = "DSAAAAAAAAAAB1"
	destNotice   = "DSAAAAAAAAAAB2"
	destPayments = "DSAAAAAAAAAAB3"
)

// deliveryService is the setup of S-121's Route "Infrastructure": the Destinations infra-critical, infra-notice and
// payments-notice, the Team payments (id 1) with payments-notice below critical, and Alerts of payments (warning)
// and devops (critical) in the period.
func deliveryService(t *testing.T) (*Service, *fakeStore) {
	t.Helper()
	svc, store, snaps, _ := newPreviewService(t)
	store.dests[destCritical], store.dests[destNotice], store.dests[destPayments] = 61, 62, 63
	store.previewDests = []dbgen.ListPreviewDestinationsRow{
		{ID: 61, PublicID: destCritical, Name: "infra-critical", Type: "mattermost", Health: "healthy"},
		{ID: 62, PublicID: destNotice, Name: "infra-notice", Type: "mattermost", Health: "healthy"},
		{ID: 63, PublicID: destPayments, Name: "payments-notice", Type: "telegram", Health: "broken",
			BrokenSince: pgtype.Timestamptz{Time: t0, Valid: true}, BrokenReason: pgtype.Text{String: "no", Valid: true}}}
	store.classTeams = []teamsdb.ListClassifiedTeamsRow{{ID: 1, Slug: "payments"}, {ID: 2, Slug: "checkout"}}
	store.classMatchers = []teamsdb.ListClassifiedMatchersRow{{TeamID: 2, Label: "namespace", Op: "=", Value: "shop"}}
	store.previewTeams = []dbgen.ListPreviewTeamsRow{{ID: 1, PublicID: "TMAAAAAAAAAAA1", Name: "Payments", Slug: "payments"},
		{ID: 2, PublicID: "TMAAAAAAAAAAA2", Name: "Checkout", Slug: "checkout"}}
	store.teamEntries = []dbgen.ListPreviewTeamEntriesRow{{TeamID: 1, DestinationID: 63, SeverityCondition: "below_critical"},
		{TeamID: 2, DestinationID: 62, SeverityCondition: "any"}}
	snaps.bodies = [][]ingest.FiringAlert{firing(
		"p1=alertname:Disk,category:infra,team:payments,severity:warning",
		"d1=alertname:Disk,category:infra,team:devops,severity:critical")}
	return svc, store
}

func infraEntries() *[]DestinationEntry {
	return &[]DestinationEntry{{DestinationID: destCritical, Condition: ConditionCritical},
		{DestinationID: destNotice, Condition: ConditionBelowCritical}}
}

// deliveryLine is a group of the delivery preview as team slugs>level>destination:kinds.
func deliveryLine(g DeliveryGroup) string {
	var teams, dests []string
	for _, t := range g.Teams {
		teams = append(teams, t.Slug)
	}
	for _, d := range g.Destinations {
		var kinds []string
		for _, e := range d.Entries {
			kinds = append(kinds, e.Kind)
		}
		dests = append(dests, d.Destination.Name+":"+strings.Join(kinds, "+"))
	}
	return fmt.Sprintf("%s>%s>%s", strings.Join(teams, ","), g.SeverityLevel, strings.Join(dests, ","))
}

func deliveryLines(d *DeliveryPreview) []string {
	var out []string
	for _, g := range d.AlertGroups {
		out = append(out, deliveryLine(g))
	}
	slices.Sort(out)
	return out
}

// TestPreviewDelivery is C-26.FR-12 with the Route "Infrastructure" of S-121 sent unsaved: the Alert Group of payments
// reaches infra-notice by the Route and payments-notice by the Team, that of devops infra-critical only; without
// deliver_to_teams the first reaches infra-notice only; the preview writes nothing.
func TestPreviewDelivery(t *testing.T) {
	svc, store := deliveryService(t)
	ctx := t.Context()
	req := PreviewRequest{Matchers: []Matcher{{Label: "category", Op: "=", Value: "infra"}},
		ProposedGroupKey: []string{"alertname", "team"}, DestinationEntries: infraEntries(), DeliverToTeams: ptr(true)}
	p, err := svc.Preview(ctx, req)
	if err != nil || p.Delivery == nil {
		t.Fatalf("%+v, %v", p, err)
	}
	if got := deliveryLines(p.Delivery); !slices.Equal(got, []string{">critical>infra-critical:route",
		"payments>warning>infra-notice:route,payments-notice:team"}) {
		t.Errorf("deliver to Teams: %q", got)
	}
	if p.Delivery.SharedKeyWarning {
		t.Error("the Group key has the Team label")
	}
	counts := map[string]int{}
	for _, c := range p.Delivery.DestinationCounts {
		counts[c.Destination.Name] = c.AlertGroupCount
	}
	if len(counts) != 3 || counts["infra-critical"] != 1 || counts["infra-notice"] != 1 || counts["payments-notice"] != 1 {
		t.Errorf("counts %v", counts)
	}
	var team *PreviewTeam
	for _, g := range p.Delivery.AlertGroups {
		for _, d := range g.Destinations {
			for _, e := range d.Entries {
				if e.Kind == EntryTeam {
					team = e.Team
				}
			}
			if d.Destination.Name == "payments-notice" && (d.Destination.Health != "broken" ||
				d.Destination.BrokenSince == nil || *d.Destination.BrokenReason != "no") {
				t.Errorf("destination %+v", d.Destination)
			}
		}
	}
	if team == nil || team.Slug != "payments" || team.PublicID != "TMAAAAAAAAAAA1" {
		t.Errorf("team entry %+v", team)
	}

	req.DeliverToTeams = ptr(false)
	p, err = svc.Preview(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if got := deliveryLines(p.Delivery); !slices.Equal(got, []string{">critical>infra-critical:route",
		"payments>warning>infra-notice:route"}) {
		t.Errorf("without deliver_to_teams: %q", got)
	}
	if len(store.audit) != 0 || len(store.hints) != 0 || len(store.updates) != 0 || len(store.notified) != 0 ||
		store.calls["InsertRoute"]+store.calls["UpdateRoute"]+store.calls["LockRoute"]+
			store.calls["LockRouteMembership"]+store.calls["LockTeamMembership"] != 0 {
		t.Errorf("the preview wrote or locked: %+v %v", store.audit, store.calls)
	}
}

// TestPreviewDeliverySettings: absent settings take the saved Route's when it is given, else none, no entries and
// false; given ones replace them; the delivery is absent for a request that gives nothing of the three and no Route.
func TestPreviewDeliverySettings(t *testing.T) {
	svc, _ := deliveryService(t)
	ctx := t.Context()
	infra := []Matcher{{Label: "category", Op: "=", Value: "infra"}}
	key := []string{"alertname", "team"}
	if p, err := svc.Preview(ctx, PreviewRequest{Matchers: infra, ProposedGroupKey: key,
		MinimumSeverity: ptr(MinimumCritical)}); err != nil || p.Delivery != nil {
		t.Errorf("neither entries nor deliver_to_teams: %+v, %v", p.Delivery, err)
	}
	p, err := svc.Preview(ctx, PreviewRequest{Matchers: infra, ProposedGroupKey: key, DeliverToTeams: ptr(true)})
	if err != nil || !slices.Equal(deliveryLines(p.Delivery), []string{">critical>",
		"payments>warning>payments-notice:team"}) {
		t.Errorf("no entries: %+v, %v", p.Delivery, err)
	}

	in := input("infra", infra...)
	in.MinimumSeverity, in.DeliverToTeams, in.DestinationEntries = ptr(MinimumCritical), ptr(true), infraEntries()
	saved, err := svc.Create(ctx, by, in)
	if err != nil {
		t.Fatal(err)
	}
	p, err = svc.Preview(ctx, PreviewRequest{RouteID: saved.PublicID, ProposedGroupKey: key})
	if err != nil {
		t.Fatal(err)
	}
	// The saved Minimum severity critical raises both groups to critical: infra-critical for both, no notice, and the
	// Team's below_critical entry is not met.
	if got := deliveryLines(p.Delivery); !slices.Equal(got, []string{">critical>infra-critical:route",
		"payments>critical>infra-critical:route"}) {
		t.Errorf("saved: %q", got)
	}
	p, err = svc.Preview(ctx, PreviewRequest{RouteID: saved.PublicID, ProposedGroupKey: key,
		MinimumSeverity: ptr(MinimumNone), DeliverToTeams: ptr(false)})
	if err != nil {
		t.Fatal(err)
	}
	if got := deliveryLines(p.Delivery); !slices.Equal(got, []string{">critical>infra-critical:route",
		"payments>warning>infra-notice:route"}) {
		t.Errorf("overridden: %q", got)
	}
	p, err = svc.Preview(ctx, PreviewRequest{RouteID: saved.PublicID, ProposedGroupKey: key,
		DestinationEntries: &[]DestinationEntry{}, MinimumSeverity: ptr(MinimumNone)})
	if err != nil || len(p.Delivery.DestinationCounts) != 1 || p.Delivery.DestinationCounts[0].Destination.Name !=
		"payments-notice" {
		t.Errorf("no entries given, Teams saved: %+v, %v", p.Delivery, err)
	}
}

// TestPreviewDeliveryMinimumSeverity: the Minimum severity raises the Severity level of a group and never lowers it,
// and a group's level is the highest of its Alerts.
func TestPreviewDeliveryMinimumSeverity(t *testing.T) {
	svc, _ := deliveryService(t)
	snaps := svc.snapshots.(*fakeSnapshots)
	snaps.bodies = [][]ingest.FiringAlert{firing("a=alertname:A,severity:info", "b=alertname:B,severity:warning",
		"c=alertname:C,severity:critical", "d=alertname:C,severity:info", "e=alertname:D,severity:page",
		"f=alertname:E")}
	for _, tt := range []struct{ minimum, want string }{
		{MinimumNone, "A=info B=warning C=critical D=warning E=info"},
		{MinimumWarning, "A=warning B=warning C=critical D=warning E=warning"},
		{MinimumCritical, "A=critical B=critical C=critical D=critical E=critical"},
	} {
		p, err := svc.Preview(t.Context(), PreviewRequest{Matchers: []Matcher{{Label: "alertname", Op: "=~", Value: ".+"}},
			ProposedGroupKey: []string{"alertname"}, MinimumSeverity: ptr(tt.minimum), DeliverToTeams: ptr(false)})
		if err != nil {
			t.Fatal(err)
		}
		levels := map[string]string{}
		for _, g := range p.Delivery.AlertGroups {
			levels[g.GroupKeyValues["alertname"]] = g.SeverityLevel
		}
		var got []string
		for _, n := range []string{"A", "B", "C", "D", "E"} {
			got = append(got, n+"="+levels[n])
		}
		if strings.Join(got, " ") != tt.want {
			t.Errorf("minimum %s: %v, want %s", tt.minimum, got, tt.want)
		}
	}
}

// TestPreviewDeliveryTeams: an Alert Group of Alerts of two Teams, by the label and by a Team matcher, lists both and
// reaches the Destinations of both; the warning is on while the Route delivers to Teams and the Group key lacks the
// Team label.
func TestPreviewDeliveryTeams(t *testing.T) {
	svc, _ := deliveryService(t)
	snaps := svc.snapshots.(*fakeSnapshots)
	snaps.bodies = [][]ingest.FiringAlert{firing("p=alertname:Disk,team:payments,severity:warning",
		"c=alertname:Disk,namespace:shop,severity:warning", "o=alertname:Disk,severity:warning")}
	infra := []Matcher{{Label: "alertname", Op: "=", Value: "Disk"}}
	for _, tt := range []struct {
		key  []string
		to   bool
		warn bool
		line string
	}{
		{[]string{"alertname"}, true, true, "payments,checkout>warning>infra-notice:route+team,payments-notice:team"},
		{[]string{"alertname"}, false, false, "payments,checkout>warning>infra-notice:route"},
		{[]string{"alertname", "team"}, true, false, ""},
	} {
		p, err := svc.Preview(t.Context(), PreviewRequest{Matchers: infra, ProposedGroupKey: tt.key,
			DestinationEntries: infraEntries(), DeliverToTeams: ptr(tt.to)})
		if err != nil {
			t.Fatal(err)
		}
		if p.Delivery.SharedKeyWarning != tt.warn {
			t.Errorf("%v %v: warning %v", tt.key, tt.to, p.Delivery.SharedKeyWarning)
		}
		if tt.line != "" && (len(p.Delivery.AlertGroups) != 1 || deliveryLine(p.Delivery.AlertGroups[0]) != tt.line) {
			t.Errorf("%v %v: %q", tt.key, tt.to, deliveryLines(p.Delivery))
		}
	}
}

// previewRise previews the Route "Infrastructure" with the Group key alertname, system, team, which lacks the
// severity, over Alerts given oldest first as startsAt offsets in minutes and labels.
func previewRise(t *testing.T, key []string, toTeams bool, alerts ...string) *DeliveryPreview {
	t.Helper()
	svc, _ := deliveryService(t)
	snaps := svc.snapshots.(*fakeSnapshots)
	snaps.bodies = nil
	// Stored Snapshots are read newest first.
	for i := len(alerts) - 1; i >= 0; i-- {
		snaps.bodies = append(snaps.bodies, firingAt(t0.Add(time.Duration(i)*time.Minute), alerts[i]))
	}
	p, err := svc.Preview(t.Context(), PreviewRequest{Matchers: []Matcher{{Label: "category", Op: "=", Value: "infra"}},
		ProposedGroupKey: key, DestinationEntries: infraEntries(), DeliverToTeams: ptr(toTeams)})
	if err != nil || p.Delivery == nil {
		t.Fatalf("%+v, %v", p, err)
	}
	return p.Delivery
}

func countsOf(d *DeliveryPreview) string {
	var out []string
	for _, c := range d.DestinationCounts {
		out = append(out, fmt.Sprintf("%s=%d", c.Destination.Name, c.AlertGroupCount))
	}
	return strings.Join(out, ",")
}

// TestPreviewDeliveryRise is C-26.FR-4 (PRD scenario 4): a warning Alert of payments and later a critical one in one
// Alert Group reach infra-notice and payments-notice first and infra-critical after, and all three stay; the other
// way round only the critical Destinations are reached, as a level that falls adds nothing.
func TestPreviewDeliveryRise(t *testing.T) {
	key := []string{"alertname", "category"}
	warning := "w=alertname:Disk,category:infra,team:payments,severity:warning"
	critical := "c=alertname:Disk,category:infra,severity:critical"

	d := previewRise(t, key, true, warning, critical)
	if len(d.AlertGroups) != 1 || deliveryLine(d.AlertGroups[0]) !=
		"payments>critical>infra-critical:route,infra-notice:route,payments-notice:team" {
		t.Errorf("warning then critical: %q", deliveryLines(d))
	}
	if got := countsOf(d); got != "infra-critical=1,infra-notice=1,payments-notice=1" {
		t.Errorf("counts %s", got)
	}

	d = previewRise(t, key, true, critical, warning)
	if len(d.AlertGroups) != 1 || deliveryLine(d.AlertGroups[0]) != "payments>critical>infra-critical:route" {
		t.Errorf("critical then warning: %q", deliveryLines(d))
	}
	if got := countsOf(d); got != "infra-critical=1" {
		t.Errorf("counts %s", got)
	}

	// The Team of a later Alert adds its Destinations at the level the group has by then, the Team of an earlier one at
	// the level before.
	checkout := "k=alertname:Disk,category:infra,namespace:shop,severity:critical"
	d = previewRise(t, key, true, warning, checkout)
	if got := deliveryLine(d.AlertGroups[0]); got !=
		"payments,checkout>critical>infra-critical:route,infra-notice:route+team,payments-notice:team" {
		t.Errorf("a Team joins on the rise: %q", got)
	}
	d = previewRise(t, key, false, warning, critical)
	if got := deliveryLine(d.AlertGroups[0]); got != "payments>critical>infra-critical:route,infra-notice:route" {
		t.Errorf("without deliver_to_teams: %q", got)
	}
}

// TestPreviewDeliveryRiseTogether: Alerts that start at the same time take effect together, in whatever order they
// were read, so the lower one adds nothing.
func TestPreviewDeliveryRiseTogether(t *testing.T) {
	for _, alerts := range [][]string{
		{"w=alertname:Disk,category:infra,severity:warning", "c=alertname:Disk,category:infra,severity:critical"},
		{"c=alertname:Disk,category:infra,severity:critical", "w=alertname:Disk,category:infra,severity:warning"}} {
		svc, _ := deliveryService(t)
		svc.snapshots.(*fakeSnapshots).bodies = [][]ingest.FiringAlert{firingAt(t0, alerts...)}
		p, err := svc.Preview(t.Context(), PreviewRequest{Matchers: []Matcher{{Label: "category", Op: "=",
			Value: "infra"}}, ProposedGroupKey: []string{"alertname"}, DestinationEntries: infraEntries(),
			DeliverToTeams: ptr(false)})
		if err != nil {
			t.Fatal(err)
		}
		if got := deliveryLine(p.Delivery.AlertGroups[0]); got != ">critical>infra-critical:route" {
			t.Errorf("%v: %q", alerts, got)
		}
	}
}

// TestPreviewDeliveryRiseWithoutOrder: Alerts without a startsAt have no order, so a group reaches what each Alert
// alone would reach: the Destinations it may reach.
func TestPreviewDeliveryRiseWithoutOrder(t *testing.T) {
	svc, _ := deliveryService(t)
	svc.snapshots.(*fakeSnapshots).bodies = [][]ingest.FiringAlert{firing(
		"c=alertname:Disk,category:infra,severity:critical", "w=alertname:Disk,category:infra,severity:warning")}
	p, err := svc.Preview(t.Context(), PreviewRequest{Matchers: []Matcher{{Label: "category", Op: "=", Value: "infra"}},
		ProposedGroupKey: []string{"alertname"}, DestinationEntries: infraEntries(), DeliverToTeams: ptr(false)})
	if err != nil {
		t.Fatal(err)
	}
	if got := deliveryLine(p.Delivery.AlertGroups[0]); got != ">critical>infra-critical:route,infra-notice:route" {
		t.Errorf("%q", got)
	}
}

// TestPreviewDeliveryCounts: only the PreviewExamples largest groups are listed, and the counts cover all of them.
func TestPreviewDeliveryCounts(t *testing.T) {
	svc, _ := deliveryService(t)
	snaps := svc.snapshots.(*fakeSnapshots)
	var alerts []string
	for i := range 8 {
		alerts = append(alerts, fmt.Sprintf("a%d=alertname:A%d,severity:critical", i, i))
		if i < 3 {
			alerts = append(alerts, fmt.Sprintf("b%d=alertname:A%d,node:x%d,severity:critical", i, i, i))
		}
	}
	snaps.bodies = [][]ingest.FiringAlert{firing(alerts...)}
	p, err := svc.Preview(t.Context(), PreviewRequest{Matchers: []Matcher{{Label: "alertname", Op: "=~", Value: "A.*"}},
		ProposedGroupKey: []string{"alertname"}, DestinationEntries: infraEntries(), DeliverToTeams: ptr(false)})
	if err != nil {
		t.Fatal(err)
	}
	d := p.Delivery
	if len(d.AlertGroups) != PreviewExamples || d.AlertGroups[0].AlertCount != 2 || len(d.DestinationCounts) != 1 ||
		d.DestinationCounts[0].Destination.Name != "infra-critical" || d.DestinationCounts[0].AlertGroupCount != 8 {
		t.Errorf("%d listed, counts %+v", len(d.AlertGroups), d.DestinationCounts)
	}
}

// TestPreviewDeliveryRefusals: a Minimum severity outside the three, an unknown or doubled Destination and a bad
// condition point at their fields, and a failing read fails the preview.
func TestPreviewDeliveryRefusals(t *testing.T) {
	svc, _ := deliveryService(t)
	base := PreviewRequest{Matchers: []Matcher{{Label: "category", Op: "=", Value: "infra"}},
		ProposedGroupKey: []string{"alertname"}, DestinationEntries: infraEntries(), DeliverToTeams: ptr(true)}
	for _, tt := range []struct {
		change        func(*PreviewRequest)
		pointer, code string
	}{
		{func(r *PreviewRequest) { r.MinimumSeverity = ptr("high") }, "/minimum_severity", CodeInvalidFormat},
		{func(r *PreviewRequest) {
			r.DestinationEntries = &[]DestinationEntry{{DestinationID: "DSZZZZZZZZZZZZ"}}
		}, "/destination_entries/0/destination_id", CodeUnknownID},
		{func(r *PreviewRequest) {
			r.DestinationEntries = &[]DestinationEntry{{DestinationID: destNotice}, {DestinationID: destNotice}}
		}, "/destination_entries/1/destination_id", CodeDuplicate},
		{func(r *PreviewRequest) {
			r.DestinationEntries = &[]DestinationEntry{{DestinationID: destNotice, Condition: "high"}}
		}, "/destination_entries/0/severity_condition", CodeInvalidFormat},
	} {
		req := base
		tt.change(&req)
		_, err := svc.Preview(t.Context(), req)
		if f := fieldError(t, err); f.Pointer != tt.pointer || f.Code != tt.code {
			t.Errorf("%+v", f)
		}
	}
	for _, name := range []string{"ListPreviewTeams", "ListPreviewTeamEntries", "ListPreviewDestinations",
		"ListClassifiedTeams", "ListClassifiedMatchers", "ResolveDestinations"} {
		var store *fakeStore
		svc, store = deliveryService(t)
		store.fail[name] = errBoom
		snaps := svc.snapshots.(*fakeSnapshots)
		snaps.bodies = [][]ingest.FiringAlert{firing("p=alertname:Disk,category:infra,team:payments,severity:warning")}
		if _, err := svc.Preview(t.Context(), base); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", name, err)
		}
	}
	svc, store := deliveryService(t)
	store.mapping = []byte(`{`)
	if _, err := svc.Preview(t.Context(), base); err == nil {
		t.Error("a broken severity mapping")
	}
}

// TestPreviewDeliveryDeletedMidRequest: a Team or a Destination that a read no longer returns, deleted while the
// preview ran, is left out instead of being named with an empty public_id.
func TestPreviewDeliveryDeletedMidRequest(t *testing.T) {
	svc, store := deliveryService(t)
	store.previewTeams, store.previewDests = nil, store.previewDests[:1]
	p, err := svc.Preview(t.Context(), PreviewRequest{Matchers: []Matcher{{Label: "category", Op: "=", Value: "infra"}},
		ProposedGroupKey: []string{"alertname", "team"}, DestinationEntries: infraEntries(), DeliverToTeams: ptr(true)})
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range p.Delivery.AlertGroups {
		if len(g.Teams) != 0 || len(g.Destinations) > 1 {
			t.Errorf("%+v", g)
		}
		for _, d := range g.Destinations {
			if d.Destination.PublicID == "" || len(d.Entries) == 0 {
				t.Errorf("destination %+v", d)
			}
		}
	}
	if len(p.Delivery.DestinationCounts) != 1 || p.Delivery.DestinationCounts[0].Destination.PublicID != destCritical {
		t.Errorf("counts %+v", p.Delivery.DestinationCounts)
	}
}

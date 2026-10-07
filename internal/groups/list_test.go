// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/live"
	"github.com/muster-io/muster/internal/matchers"
	"github.com/muster-io/muster/internal/organization"
)

// The fake's list, counts, related Alert Groups, statistics, retention and hints, with the semantics of the queries.

func (f *fakeDB) Notify(_ context.Context, h db.Hint) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("Notify"); err != nil {
		return err
	}
	f.hints = append(f.hints, h)
	return nil
}

func (f *fakeDB) CustomPlans() Queries { return f }

// fakeFilter are the filter parameters that the list and the counts share.
type fakeFilter struct {
	number        pgtype.Int8
	from, to      time.Time
	routes, ints  []int64
	severities    []string
	urgent        pgtype.Bool
	resolvedBy    pgtype.Text
	reason        pgtype.Text
	reopened      pgtype.Bool
	contains      []byte
	pattern       pgtype.Text
	statuses      []string
	withoutStatus bool
}

func (f *fakeDB) matches(g *dbgen.LockGroupsRow, p fakeFilter) bool {
	if p.number.Valid {
		if g.Number != p.number.Int64 {
			return false
		}
	} else if !g.CreatedAt.Before(p.to) || (g.ResolvedAt.Valid && g.ResolvedAt.Time.Before(p.from)) {
		return false
	}
	if !p.withoutStatus && !slices.Contains(p.statuses, g.Status) {
		return false
	}
	if len(p.routes) > 0 && !slices.Contains(p.routes, g.RouteID) {
		return false
	}
	if len(p.ints) > 0 && !slices.ContainsFunc(g.IntegrationIds, func(id int64) bool {
		return slices.Contains(p.ints, id)
	}) {
		return false
	}
	if len(p.severities) > 0 && !slices.Contains(p.severities, g.SeverityLevel) {
		return false
	}
	if p.urgent.Valid && f.urgent(g) != p.urgent.Bool {
		return false
	}
	if p.resolvedBy.Valid && g.ResolvedByKind.String != p.resolvedBy.String {
		return false
	}
	if p.reason.Valid && g.ResolveReason.String != p.reason.String {
		return false
	}
	if p.reopened.Valid && (g.ReopenCount > 0) != p.reopened.Bool {
		return false
	}
	if p.contains != nil {
		var want, have map[string]string
		_ = json.Unmarshal(p.contains, &want)
		_ = json.Unmarshal(g.CommonLabels, &have)
		for k, v := range want {
			if have[k] != v {
				return false
			}
		}
	}
	if p.pattern.Valid {
		text := strings.Trim(p.pattern.String, "%")
		text = strings.NewReplacer(`\%`, `%`, `\_`, `_`, `\\`, `\`).Replace(strings.ToLower(text))
		if !strings.Contains(strings.ToLower(g.Title), text) && !strings.Contains(strings.ToLower(g.Summary.String),
			text) {
			return false
		}
	}
	return true
}

func (f *fakeDB) listRow(g *dbgen.LockGroupsRow) listRow {
	r := f.routes[g.RouteID]
	return listRow{ID: g.ID, PublicID: g.PublicID, Number: g.Number, Title: g.Title, Summary: g.Summary,
		Status: g.Status, SeverityLevel: g.SeverityLevel, Urgent: f.urgent(g), CommonLabels: g.CommonLabels,
		IntegrationIds: g.IntegrationIds, ReopenCount: g.ReopenCount, FiringAlertCount: g.FiringAlertCount,
		ResolvedAlertCount: g.ResolvedAlertCount, ResolvedAt: g.ResolvedAt, ResolvedByKind: g.ResolvedByKind,
		ResolvedByUserID: g.ResolvedByUserID, ResolvedByServiceAccountID: g.ResolvedByServiceAccountID,
		ResolveReason: g.ResolveReason, ResolveReasonText: g.ResolveReasonText, CreatedAt: g.CreatedAt,
		LastChangedAt: g.LastChangedAt, RoutePublicID: r.PublicID, RouteName: r.name}
}

func (f *fakeDB) list(name string, p dbgen.ListGroupsStartedDescParams, changed, asc bool) ([]listRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call(name); err != nil {
		return nil, err
	}
	ff := fakeFilter{number: p.Number, from: p.RangeFrom, to: p.RangeTo, routes: p.RouteIds, ints: p.IntegrationIds,
		severities: p.Severities, urgent: p.Urgent, resolvedBy: p.ResolvedBy, reason: p.ResolveReason,
		reopened: p.Reopened, contains: p.Contains, pattern: p.Pattern, statuses: p.Statuses}
	key := func(g *dbgen.LockGroupsRow) time.Time {
		if changed {
			return g.LastChangedAt
		}
		return g.CreatedAt
	}
	order := func(a, b *dbgen.LockGroupsRow) int {
		c := cmp.Or(key(a).Compare(key(b)), cmp.Compare(a.ID, b.ID))
		if asc {
			return c
		}
		return -c
	}
	var rows []*dbgen.LockGroupsRow
	for _, g := range f.groups {
		if !f.matches(g, ff) {
			continue
		}
		if p.AfterAt.Valid {
			pos := &dbgen.LockGroupsRow{ID: p.AfterID.Int64, CreatedAt: p.AfterAt.Time, LastChangedAt: p.AfterAt.Time}
			if order(g, pos) <= 0 {
				continue
			}
		}
		rows = append(rows, g)
	}
	slices.SortFunc(rows, order)
	out := []listRow{}
	for _, g := range rows {
		if len(out) == int(p.Lim) {
			break
		}
		out = append(out, f.listRow(g))
	}
	return out, nil
}

func (f *fakeDB) ListGroupsStartedDesc(_ context.Context, arg dbgen.ListGroupsStartedDescParams) (
	[]dbgen.ListGroupsStartedDescRow, error) {
	return f.list("ListGroupsStartedDesc", arg, false, false)
}

func (f *fakeDB) ListGroupsStartedAsc(_ context.Context, arg dbgen.ListGroupsStartedAscParams) (
	[]dbgen.ListGroupsStartedAscRow, error) {
	rows, err := f.list("ListGroupsStartedAsc", dbgen.ListGroupsStartedDescParams(arg), false, true)
	out := make([]dbgen.ListGroupsStartedAscRow, len(rows))
	for i, r := range rows {
		out[i] = dbgen.ListGroupsStartedAscRow(r)
	}
	return out, err
}

func (f *fakeDB) ListGroupsChangedDesc(_ context.Context, arg dbgen.ListGroupsChangedDescParams) (
	[]dbgen.ListGroupsChangedDescRow, error) {
	rows, err := f.list("ListGroupsChangedDesc", dbgen.ListGroupsStartedDescParams(arg), true, false)
	out := make([]dbgen.ListGroupsChangedDescRow, len(rows))
	for i, r := range rows {
		out[i] = dbgen.ListGroupsChangedDescRow(r)
	}
	return out, err
}

func (f *fakeDB) ListGroupsChangedAsc(_ context.Context, arg dbgen.ListGroupsChangedAscParams) (
	[]dbgen.ListGroupsChangedAscRow, error) {
	rows, err := f.list("ListGroupsChangedAsc", dbgen.ListGroupsStartedDescParams(arg), true, true)
	out := make([]dbgen.ListGroupsChangedAscRow, len(rows))
	for i, r := range rows {
		out[i] = dbgen.ListGroupsChangedAscRow(r)
	}
	return out, err
}

func (f *fakeDB) CountGroups(_ context.Context, p dbgen.CountGroupsParams) ([]dbgen.CountGroupsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("CountGroups"); err != nil {
		return nil, err
	}
	ff := fakeFilter{number: p.Number, from: p.RangeFrom, to: p.RangeTo, routes: p.RouteIds, ints: p.IntegrationIds,
		severities: p.Severities, urgent: p.Urgent, resolvedBy: p.ResolvedBy, reason: p.ResolveReason,
		reopened: p.Reopened, contains: p.Contains, pattern: p.Pattern, withoutStatus: true}
	counts := map[[2]string]int64{}
	for _, g := range f.groups {
		if !f.matches(g, ff) {
			continue
		}
		labels := ""
		if p.WithLabels {
			labels = string(g.CommonLabels)
		}
		counts[[2]string{g.Status, labels}]++
	}
	var out []dbgen.CountGroupsRow
	for _, k := range slices.SortedFunc(maps.Keys(counts), func(a, b [2]string) int {
		return cmp.Or(cmp.Compare(a[0], b[0]), cmp.Compare(a[1], b[1]))
	}) {
		r := dbgen.CountGroupsRow{Status: k[0], Count: counts[k]}
		if p.WithLabels {
			r.CommonLabels = []byte(k[1])
		}
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeDB) GetListSettings(context.Context, int64) (dbgen.GetListSettingsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return dbgen.GetListSettingsRow{RetentionAlertDetailsDays: f.details, TimeZone: f.timeZone},
		f.call("GetListSettings")
}

func (f *fakeDB) ListRoutesByPublicID(_ context.Context, arg dbgen.ListRoutesByPublicIDParams) (
	[]dbgen.ListRoutesByPublicIDRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListRoutesByPublicID"); err != nil {
		return nil, err
	}
	var out []dbgen.ListRoutesByPublicIDRow
	for _, r := range f.routes {
		if slices.Contains(arg.PublicIds, r.PublicID) {
			out = append(out, dbgen.ListRoutesByPublicIDRow{ID: r.ID, PublicID: r.PublicID, Name: r.name})
		}
	}
	return out, nil
}

func (f *fakeDB) ListIntegrationsByPublicID(_ context.Context, arg dbgen.ListIntegrationsByPublicIDParams) (
	[]dbgen.ListIntegrationsByPublicIDRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListIntegrationsByPublicID"); err != nil {
		return nil, err
	}
	var out []dbgen.ListIntegrationsByPublicIDRow
	for _, r := range f.ints {
		if slices.Contains(arg.PublicIds, r.PublicID) {
			out = append(out, dbgen.ListIntegrationsByPublicIDRow(r))
		}
	}
	return out, nil
}

func (f *fakeDB) GetGroupKey(_ context.Context, arg dbgen.GetGroupKeyParams) (dbgen.GetGroupKeyRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetGroupKey"); err != nil {
		return dbgen.GetGroupKeyRow{}, err
	}
	g := f.byPublicID(arg.PublicID)
	if g == nil {
		return dbgen.GetGroupKeyRow{}, pgx.ErrNoRows
	}
	return dbgen.GetGroupKeyRow{ID: g.ID, RouteID: g.RouteID, GroupKeySha256: g.GroupKeySha256}, nil
}

func (f *fakeDB) ListRelatedGroups(_ context.Context, arg dbgen.ListRelatedGroupsParams) (
	[]dbgen.ListRelatedGroupsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListRelatedGroups"); err != nil {
		return nil, err
	}
	var rows []*dbgen.LockGroupsRow
	for _, g := range f.groups {
		if g.RouteID != arg.RouteID || string(g.GroupKeySha256) != string(arg.GroupKeySha256) || g.ID == arg.ID {
			continue
		}
		if arg.AfterAt.Valid && cmp.Or(g.CreatedAt.Compare(arg.AfterAt.Time), cmp.Compare(g.ID, arg.AfterID.Int64)) >= 0 {
			continue
		}
		rows = append(rows, g)
	}
	slices.SortFunc(rows, func(a, b *dbgen.LockGroupsRow) int {
		return -cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID, b.ID))
	})
	var out []dbgen.ListRelatedGroupsRow
	for _, g := range rows[:min(len(rows), int(arg.Lim))] {
		out = append(out, dbgen.ListRelatedGroupsRow{ID: g.ID, PublicID: g.PublicID, Number: g.Number,
			Status: g.Status, CreatedAt: g.CreatedAt, ResolvedAt: g.ResolvedAt, ResolvedByKind: g.ResolvedByKind,
			ResolvedByUserID: g.ResolvedByUserID, ResolvedByServiceAccountID: g.ResolvedByServiceAccountID,
			ResolveReason: g.ResolveReason, ResolveReasonText: g.ResolveReasonText})
	}
	return out, nil
}

func (f *fakeDB) RouteStatistics(_ context.Context, arg dbgen.RouteStatisticsParams) ([]dbgen.RouteStatisticsRow,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["RouteStatistics "+arg.TimeZone]++
	return f.stats, f.call("RouteStatistics")
}

func (f *fakeDB) IntegrationStatistics(_ context.Context, arg dbgen.IntegrationStatisticsParams) (
	[]dbgen.IntegrationStatisticsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["IntegrationStatistics "+arg.TimeZone]++
	out := make([]dbgen.IntegrationStatisticsRow, len(f.stats))
	for i, r := range f.stats {
		out[i] = dbgen.IntegrationStatisticsRow(r)
	}
	return out, f.call("IntegrationStatistics")
}

func (f *fakeDB) ListStatisticsRoutes(_ context.Context, arg dbgen.ListStatisticsRoutesParams) (
	[]dbgen.ListStatisticsRoutesRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListStatisticsRoutes"); err != nil {
		return nil, err
	}
	var out []dbgen.ListStatisticsRoutesRow
	for _, r := range f.routes {
		if (len(arg.OnlyIds) > 0 && slices.Contains(arg.OnlyIds, r.ID)) ||
			(len(arg.OnlyIds) == 0 && (!r.deleted || slices.Contains(arg.WithIds, r.ID))) {
			out = append(out, dbgen.ListStatisticsRoutesRow{ID: r.ID, PublicID: r.PublicID, Name: r.name})
		}
	}
	slices.SortFunc(out, func(a, b dbgen.ListStatisticsRoutesRow) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

func (f *fakeDB) ListStatisticsIntegrations(_ context.Context, arg dbgen.ListStatisticsIntegrationsParams) (
	[]dbgen.ListStatisticsIntegrationsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListStatisticsIntegrations"); err != nil {
		return nil, err
	}
	var out []dbgen.ListStatisticsIntegrationsRow
	for _, r := range f.ints {
		if len(arg.OnlyIds) == 0 || slices.Contains(arg.OnlyIds, r.ID) {
			out = append(out, dbgen.ListStatisticsIntegrationsRow(r))
		}
	}
	slices.SortFunc(out, func(a, b dbgen.ListStatisticsIntegrationsRow) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

func (f *fakeDB) CountOpenGroupsByIntegration(_ context.Context, arg dbgen.CountOpenGroupsByIntegrationParams) (
	[]dbgen.CountOpenGroupsByIntegrationRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("CountOpenGroupsByIntegration"); err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	for _, g := range f.groups {
		if g.Status == string(StatusResolved) {
			continue
		}
		for _, id := range g.IntegrationIds {
			if r, ok := f.ints[id]; ok && slices.Contains(arg.PublicIds, r.PublicID) {
				counts[r.PublicID]++
			}
		}
	}
	var out []dbgen.CountOpenGroupsByIntegrationRow
	for id, n := range counts {
		out = append(out, dbgen.CountOpenGroupsByIntegrationRow{PublicID: id, Count: n})
	}
	return out, nil
}

func (f *fakeDB) GetRetentionPeriods(context.Context, int64) (dbgen.GetRetentionPeriodsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return dbgen.GetRetentionPeriodsRow{RetentionAlertDetailsDays: f.details,
		RetentionAlertGroupSummariesDays: f.summaries}, f.call("GetRetentionPeriods")
}

func (f *fakeDB) DeleteExpiredMemberships(_ context.Context, arg dbgen.DeleteExpiredMembershipsParams) (int64,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("DeleteExpiredMemberships"); err != nil {
		return 0, err
	}
	var n int64
	f.members = slices.DeleteFunc(f.members, func(m *fakeMember) bool {
		if n < int64(arg.BatchSize) && m.state != "firing" && m.ended != nil && m.ended.Before(arg.Cutoff) {
			n++
			return true
		}
		return false
	})
	return n, nil
}

func (f *fakeDB) DeleteExpiredGroups(_ context.Context, arg dbgen.DeleteExpiredGroupsParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("DeleteExpiredGroups"); err != nil {
		return 0, err
	}
	var n int64
	for _, id := range slices.Sorted(maps.Keys(f.groups)) {
		g := f.groups[id]
		if n == int64(arg.BatchSize) || g.Status != string(StatusResolved) || !g.ResolvedAt.Time.Before(arg.Cutoff) ||
			slices.ContainsFunc(f.members, func(m *fakeMember) bool { return m.movedTo == id }) {
			continue
		}
		delete(f.groups, id)
		n++
		f.members = slices.DeleteFunc(f.members, func(m *fakeMember) bool { return m.group == id })
		for _, other := range f.groups {
			if other.FiringAgainAfterID.Int64 == id {
				other.FiringAgainAfterID = pgtype.Int8{}
			}
		}
	}
	return n, nil
}

// summary adds an Alert Group row as the list reads it: number n on the Route, started at start, resolved at
// resolved when it is not zero, with the common labels and the title.
func (h *harness) summary(n int64, route int64, start, resolved time.Time, title string,
	labels map[string]string) *dbgen.LockGroupsRow {
	lb, _ := json.Marshal(labels)
	g := &dbgen.LockGroupsRow{ID: 1000 + n, PublicID: fmt.Sprintf("AG%012d", n), Number: n, RouteID: route,
		Title: title, Status: string(StatusFiring), SeverityLevel: "warning", CommonLabels: lb,
		CommonAnnotations: []byte(`{}`), GroupKeyValues: []byte(`{}`), GroupKeySha256: []byte("key"),
		IntegrationIds: []int64{5}, CreatedAt: start, LastChangedAt: start}
	if !resolved.IsZero() {
		g.Status, g.ResolvedAt = string(StatusResolved), pgtype.Timestamptz{Time: resolved, Valid: true}
		g.ResolvedByKind = pgtype.Text{String: ResolvedBySystem, Valid: true}
		g.ResolveReason = pgtype.Text{String: "resolved", Valid: true}
		g.LastChangedAt = resolved
	}
	h.db.groups[g.ID] = g
	return g
}

func numbers(page ListPage) []int64 {
	out := []int64{}
	for _, v := range page.Groups {
		out = append(out, v.Number)
	}
	return out
}

func (h *harness) list(t *testing.T, r ListRequest) ListPage {
	t.Helper()
	if r.Limit == 0 {
		r.Limit = 50
	}
	page, err := h.svc.List(t.Context(), r)
	if err != nil {
		t.Fatalf("list %+v: %v", r, err)
	}
	return page
}

func eq(m matchers.Op, name, value string) matchers.Matcher {
	out, err := matchers.New(name, m, value)
	if err != nil {
		panic(err)
	}
	return out
}

// TestList covers the list of C-09.FR-13 on the fake: the default open tab over the last ListRange with an open Alert
// Group started earlier (C-09.AC-5), the status, the overlap of a range, #N and number ignoring it (C-09.AC-23),
// text search escaped for ILIKE, the common-labels rule of Matchers (C-09.AC-25), label_values, and each filter.
func TestList(t *testing.T) {
	h := newHarness(t)
	h.clock.Set(t0)
	payments := map[string]string{"namespace": "payments", "alertname": "KubePodCrashLooping"}
	h.summary(1, 2, t0.Add(-10*24*time.Hour), time.Time{}, "OldButOpen", payments)
	pay := h.summary(2, 2, t0.Add(-time.Hour), time.Time{}, "KubePodCrashLooping", payments)
	pay.Summary = pgtype.Text{String: "Pod postgres-0 is crash looping", Valid: true}
	pay.SeverityLevel = "critical"
	h.summary(3, 3, t0.Add(-2*time.Hour), time.Time{}, "Mixed 100%_done", map[string]string{"alertname": "M"})
	gone := h.summary(4, 2, t0.Add(-60*24*time.Hour), t0.Add(-59*24*time.Hour), "Gone", payments)
	recent := h.summary(5, 3, t0.Add(-3*24*time.Hour), t0.Add(-2*24*time.Hour), "Recent", map[string]string{
		"namespace": "billing"})
	recent.ReopenCount = 2
	recent.ResolvedByKind, recent.ResolveReason = pgtype.Text{String: ResolvedByUser, Valid: true}, pgtype.Text{}
	recent.ResolvedByUserID = pgtype.Int8{Int64: 9, Valid: true}
	h.db.users[9] = dbgen.ListUserRefsRow{ID: 9, PublicID: "SRAAAAAAAAAAAA", Name: "Ann", Login: "ann",
		Status: "active"}

	if got := numbers(h.list(t, ListRequest{})); !slices.Equal(got, []int64{2, 3, 1}) {
		t.Errorf("default open = %v", got)
	}
	if got := numbers(h.list(t, ListRequest{Filter: Filter{Statuses: allStatuses}})); !slices.Equal(got,
		[]int64{2, 3, 5, 1}) {
		t.Errorf("all statuses in the default range = %v", got)
	}
	from, to := t0.Add(-100*24*time.Hour), t0.Add(-58*24*time.Hour)
	if got := numbers(h.list(t, ListRequest{Filter: Filter{Statuses: []Status{StatusResolved}, From: &from,
		To: &to}})); !slices.Equal(got, []int64{4}) {
		t.Errorf("an old range = %v", got)
	}
	n := int64(4)
	for _, r := range []ListRequest{{Filter: Filter{Number: &n}}, {Filter: Filter{Query: " #4 "}}} {
		if got := h.list(t, r); len(got.Groups) != 1 || got.Groups[0].PublicID != gone.PublicID {
			t.Errorf("number %+v = %v", r.Filter, numbers(got))
		}
	}
	if got := numbers(h.list(t, ListRequest{Filter: Filter{Number: &n, Statuses: []Status{StatusFiring}}})); len(got) != 0 {
		t.Errorf("an explicit status with a number = %v", got)
	}
	if got := h.list(t, ListRequest{Filter: Filter{Query: "POSTGRES"}}); len(got.Groups) != 1 ||
		got.Groups[0].Number != 2 || !got.Groups[0].Urgent {
		t.Errorf("search = %+v", got.Groups)
	}
	if got := numbers(h.list(t, ListRequest{Filter: Filter{Query: "100%_"}})); !slices.Equal(got, []int64{3}) {
		t.Errorf("search with wildcards = %v", got)
	}
	if got := numbers(h.list(t, ListRequest{Filter: Filter{Query: "#x"}})); len(got) != 0 {
		t.Errorf("a #-search that is no number = %v", got)
	}
	ns := eq(matchers.Equal, "namespace", "payments")
	if got := numbers(h.list(t, ListRequest{Filter: Filter{Matchers: []matchers.Matcher{ns}}})); !slices.Equal(got,
		[]int64{2, 1}) {
		t.Errorf("namespace=payments = %v", got)
	}
	urgent, notUrgent := true, false
	if got := numbers(h.list(t, ListRequest{Filter: Filter{Matchers: []matchers.Matcher{ns}, Urgent: &urgent}})); !slices.Equal(got, []int64{2}) {
		t.Errorf("payments and urgent = %v", got)
	}
	if got := numbers(h.list(t, ListRequest{Filter: Filter{Urgent: &notUrgent}})); !slices.Equal(got, []int64{3, 1}) {
		t.Errorf("not urgent = %v", got)
	}
	// Matchers other than = with a value go to Go, on the common labels: an absent label is empty.
	for _, c := range []struct {
		m    matchers.Matcher
		want []int64
	}{
		{eq(matchers.NotEqual, "namespace", "payments"), []int64{3}},
		{eq(matchers.Regexp, "namespace", "pay.*"), []int64{2, 1}},
		{eq(matchers.NotRegexp, "namespace", "pay.*"), []int64{3}},
		{eq(matchers.Equal, "namespace", ""), []int64{3}},
	} {
		if got := numbers(h.list(t, ListRequest{Filter: Filter{Matchers: []matchers.Matcher{c.m}}})); !slices.Equal(got,
			c.want) {
			t.Errorf("%s = %v, want %v", c.m, got, c.want)
		}
	}
	two := ListRequest{Filter: Filter{Matchers: []matchers.Matcher{ns, eq(matchers.Equal, "namespace", "payments")}}}
	if got := numbers(h.list(t, two)); !slices.Equal(got, []int64{2, 1}) {
		t.Errorf("two matchers on one label = %v", got)
	}
	all := Filter{Statuses: allStatuses, From: &from}
	byUser, bySystem, reason := ResolvedByUser, ResolvedBySystem, "resolved"
	reopened := true
	for _, c := range []struct {
		name string
		f    func(*Filter)
		want []int64
	}{
		{"route", func(f *Filter) { f.Routes = []string{"RTNETAAAAAAAAA"} }, []int64{3, 5}},
		{"integration", func(f *Filter) { f.Integrations = []string{"NTAAAAAAAAAAAA"} }, []int64{2, 3, 5, 1, 4}},
		{"severity", func(f *Filter) { f.Severities = []organization.SeverityLevel{"critical"} }, []int64{2}},
		{"resolved by user", func(f *Filter) { f.ResolvedBy = &byUser }, []int64{5}},
		{"resolved by system", func(f *Filter) { f.ResolvedBy, f.ResolveReason = &bySystem, &reason }, []int64{4}},
		{"reopened", func(f *Filter) { f.Reopened = &reopened }, []int64{5}},
		{"not reopened", func(f *Filter) { f.Reopened = &notUrgent }, []int64{2, 3, 1, 4}},
	} {
		f := all
		c.f(&f)
		if got := numbers(h.list(t, ListRequest{Filter: f})); !slices.Equal(got, c.want) {
			t.Errorf("%s = %v, want %v", c.name, got, c.want)
		}
	}
	// label_values, the resolution with its User, Integrations and details_removed.
	page := h.list(t, ListRequest{Filter: all, LabelColumns: []string{"namespace", "pod"}})
	byNumber := map[int64]View{}
	for _, v := range page.Groups {
		byNumber[v.Number] = v
	}
	if v := byNumber[2]; !maps.Equal(v.LabelValues, map[string]string{"namespace": "payments"}) ||
		len(v.Integrations) != 1 || v.Integrations[0].Name != "lab" || v.Route.Name != "db" || v.DetailsRemoved {
		t.Errorf("#2 = %+v", v)
	}
	if v := byNumber[3]; len(v.LabelValues) != 0 || v.LabelValues == nil {
		t.Errorf("#3 label values = %v", v.LabelValues)
	}
	if v := byNumber[5]; v.Resolution == nil || v.Resolution.By != ResolvedByUser || v.Resolution.Actor == nil ||
		v.Resolution.Actor.Name != "Ann" || v.ReopenCount != 2 {
		t.Errorf("#5 = %+v", v)
	}
	if v := byNumber[4]; v.DetailsRemoved || v.Resolution == nil || v.Resolution.ReasonCode == nil {
		t.Errorf("#4 = %+v", v)
	}
	h.db.details = 30
	if v := h.list(t, ListRequest{Filter: Filter{Number: &n}}).Groups[0]; !v.DetailsRemoved || v.LabelValues != nil {
		t.Errorf("#4 after 30 days of details = %+v", v)
	}
	if got := h.list(t, ListRequest{Filter: all}); got.Groups[0].DetailsRemoved {
		t.Error("an open alert group has its details removed")
	}
}

// TestListCursor pages each of the four orders by cursor: positions on (time, id) keep their place when Alert Groups
// are inserted before them, and the pages put together are the whole list.
func TestListCursor(t *testing.T) {
	h := newHarness(t)
	for i := range int64(7) {
		g := h.summary(i+1, 2, t0.Add(-time.Duration(i)*time.Hour), time.Time{}, "G", nil)
		g.LastChangedAt = t0.Add(-time.Duration(i%3) * time.Minute)
	}
	for _, c := range []struct {
		sort Sort
		want []int64
	}{
		{SortStartedDesc, []int64{1, 2, 3, 4, 5, 6, 7}},
		{SortStarted, []int64{7, 6, 5, 4, 3, 2, 1}},
		{SortChangedDesc, []int64{7, 4, 1, 5, 2, 6, 3}},
		{SortChanged, []int64{3, 6, 2, 5, 1, 4, 7}},
		{"", []int64{1, 2, 3, 4, 5, 6, 7}},
	} {
		var got []int64
		var after *ListPosition
		for range 10 {
			page := h.list(t, ListRequest{Sort: c.sort, After: after, Limit: 3})
			got = append(got, numbers(page)...)
			if page.Next == nil {
				break
			}
			after = page.Next
			// An Alert Group that starts meanwhile does not move the next page.
			h.summary(100+int64(len(got)), 2, t0.Add(time.Duration(len(got))*time.Minute), time.Time{}, "New", nil)
		}
		desc := c.sort != SortStarted && c.sort != SortChanged
		if len(got) < 7 || !slices.Equal(got[:7], c.want) || (desc && len(got) != 7) {
			t.Errorf("%q pages = %v, want %v", c.sort, got, c.want)
		}
		for id, g := range h.db.groups {
			if g.Number >= 100 {
				delete(h.db.groups, id)
			}
		}
	}
}

// TestListRowwiseBatches: with Matchers that Go applies, the list reads batch after batch until the page is full,
// and returns the position to go on from once it has read maxScan rows.
func TestListRowwiseBatches(t *testing.T) {
	h := newHarness(t)
	for i := range int64(maxScan + 500) {
		labels := map[string]string{"pod": "other"}
		if i%1000 == 999 {
			labels["pod"] = "api-1"
		}
		h.summary(i+1, 2, t0.Add(-time.Duration(i)*time.Second), time.Time{}, "G", labels)
	}
	re := eq(matchers.Regexp, "pod", "api-.*")
	page := h.list(t, ListRequest{Filter: Filter{Matchers: []matchers.Matcher{re}}, Limit: 3})
	if !slices.Equal(numbers(page), []int64{1000, 2000, 3000}) || page.Next == nil || page.Next.ID != 1000+3000 {
		t.Fatalf("first page %v next %+v", numbers(page), page.Next)
	}
	if h.db.calls["ListGroupsStartedDesc"] != 40 {
		t.Errorf("batches %d", h.db.calls["ListGroupsStartedDesc"])
	}
	page = h.list(t, ListRequest{Filter: Filter{Matchers: []matchers.Matcher{re}}, Limit: 50})
	if got := numbers(page); len(got) != 10 || got[9] != 10000 || page.Next == nil || page.Next.ID != 1000+10000 {
		t.Fatalf("a page that hit maxScan %v next %+v", got, page.Next)
	}
	page = h.list(t, ListRequest{Filter: Filter{Matchers: []matchers.Matcher{re}}, Limit: 50, After: page.Next})
	if got := numbers(page); len(got) != 0 || page.Next != nil {
		t.Errorf("the last page %v next %+v", got, page.Next)
	}
}

// TestListErrors: unknown Routes and Integrations, a range that ends before it starts, and failing queries.
func TestListErrors(t *testing.T) {
	h := newHarness(t)
	h.summary(1, 2, t0, time.Time{}, "G", map[string]string{"a": "b"})
	ctx := t.Context()
	from := t0.Add(time.Hour)
	for _, c := range []struct {
		f       Filter
		pointer string
		code    string
	}{
		{Filter{Routes: []string{"RTZZZZZZZZZZZZ"}}, "/query/route", CodeUnknownID},
		{Filter{Routes: []string{"bad"}}, "/query/route", CodeUnknownID},
		{Filter{Integrations: []string{"NTZZZZZZZZZZZZ"}}, "/query/integration", CodeUnknownID},
		{Filter{From: &from}, "/query/from", CodeOutOfRange},
	} {
		_, err := h.svc.List(ctx, ListRequest{Filter: c.f, Limit: 5})
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Pointer != c.pointer || fe.Code != c.code || fe.Error() == "" {
			t.Errorf("%+v = %v", c.f, err)
		}
		if _, err := h.svc.Counts(ctx, c.f); !errors.As(err, &fe) {
			t.Errorf("counts %+v = %v", c.f, err)
		}
	}
	re := eq(matchers.Regexp, "a", "b")
	for _, q := range []string{"ListRoutesByPublicID", "ListIntegrationsByPublicID", "GetListSettings",
		"ListGroupsStartedDesc", "ListIntegrationRefs"} {
		h.db.fail[q] = errBoom
		_, err := h.svc.List(ctx, ListRequest{Filter: Filter{Routes: []string{"RTDBAAAAAAAAAA"},
			Integrations: []string{"NTAAAAAAAAAAAA"}}, Limit: 5})
		if !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", q, err)
		}
		delete(h.db.fail, q)
	}
	for _, sort := range []Sort{SortStarted, SortChanged, SortChangedDesc} {
		name := map[Sort]string{SortStarted: "ListGroupsStartedAsc", SortChanged: "ListGroupsChangedAsc",
			SortChangedDesc: "ListGroupsChangedDesc"}[sort]
		h.db.fail[name] = errBoom
		if _, err := h.svc.List(ctx, ListRequest{Sort: sort, Limit: 5}); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", name, err)
		}
		delete(h.db.fail, name)
	}
	h.db.groups[1001].CommonLabels = []byte(`[`)
	if _, err := h.svc.List(ctx, ListRequest{Filter: Filter{Matchers: []matchers.Matcher{re}}, Limit: 5}); err == nil {
		t.Error("broken common labels with a matcher")
	}
	if _, err := h.svc.List(ctx, ListRequest{LabelColumns: []string{"a"}, Limit: 5}); err == nil {
		t.Error("broken common labels with a label column")
	}
	if _, err := h.svc.Counts(ctx, Filter{Matchers: []matchers.Matcher{re}}); err == nil {
		t.Error("broken common labels in the counts")
	}
	h.db.fail["CountGroups"] = errBoom
	if _, err := h.svc.Counts(ctx, Filter{}); !errors.Is(err, errBoom) {
		t.Errorf("counts: %v", err)
	}
}

// TestCounts counts per status tab with the filters of the list, its status and number aside, and Matchers that Go
// applies on the common labels.
func TestCounts(t *testing.T) {
	h := newHarness(t)
	payments := map[string]string{"namespace": "payments"}
	h.summary(1, 2, t0.Add(-time.Hour), time.Time{}, "A", payments)
	g := h.summary(2, 2, t0.Add(-time.Hour), time.Time{}, "B", payments)
	g.Status, g.OwnerUserID = string(StatusAcknowledged), pgtype.Int8{Int64: 9, Valid: true}
	g = h.summary(3, 2, t0.Add(-time.Hour), time.Time{}, "C", map[string]string{"namespace": "billing"})
	g.Status = string(StatusSnoozed)
	h.summary(4, 2, t0.Add(-2*time.Hour), t0.Add(-time.Hour), "D", payments)
	h.summary(5, 2, t0.Add(-30*24*time.Hour), t0.Add(-29*24*time.Hour), "E", payments)
	if c, err := h.svc.Counts(t.Context(), Filter{Statuses: []Status{StatusFiring}}); err != nil ||
		c != (Counts{Firing: 1, Acknowledged: 1, Snoozed: 1, Resolved: 1, All: 4}) {
		t.Errorf("counts = %+v, %v", c, err)
	}
	ns := eq(matchers.Equal, "namespace", "payments")
	if c, _ := h.svc.Counts(t.Context(), Filter{Matchers: []matchers.Matcher{ns}}); c != (Counts{Firing: 1,
		Acknowledged: 1, Resolved: 1, All: 3}) {
		t.Errorf("payments = %+v", c)
	}
	re := eq(matchers.NotRegexp, "namespace", "pay.*")
	if c, _ := h.svc.Counts(t.Context(), Filter{Matchers: []matchers.Matcher{re}}); c != (Counts{Snoozed: 1,
		All: 1}) {
		t.Errorf("not payments = %+v", c)
	}
	if c, _ := h.svc.Counts(t.Context(), Filter{Query: "#5"}); c != (Counts{Resolved: 1, All: 1}) {
		t.Errorf("#5 = %+v", c)
	}
}

// TestRelated lists the other Alert Groups of the Route and Group key, newest first by cursor, with their duration
// and resolution (C-09.FR-20, C-09.AC-16).
func TestRelated(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	cur := h.summary(4, 2, t0, time.Time{}, "G", nil)
	h.summary(1, 2, t0.Add(-3*time.Hour), t0.Add(-3*time.Hour+10*time.Minute), "G", nil)
	h.summary(2, 2, t0.Add(-2*time.Hour), t0.Add(-2*time.Hour+20*time.Minute), "G", nil)
	other := h.summary(3, 2, t0.Add(-time.Hour), time.Time{}, "G", nil)
	other.GroupKeySha256 = []byte("other")
	h.summary(5, 3, t0.Add(-time.Hour), time.Time{}, "G", nil)
	later := h.summary(6, 2, t0.Add(time.Hour), t0.Add(2*time.Hour), "G", nil)
	later.ResolvedByKind, later.ResolveReason = pgtype.Text{String: ResolvedByUser, Valid: true}, pgtype.Text{}
	later.ResolvedByServiceAccountID = pgtype.Int8{Int64: 4, Valid: true}
	h.db.accounts[4] = dbgen.ListServiceAccountRefsRow{ID: 4, PublicID: "SAAAAAAAAAAAAA", Name: "bot",
		Status: "active"}
	page, err := h.svc.Related(ctx, cur.PublicID, nil, 2)
	if err != nil || len(page.Groups) != 2 || page.Next == nil {
		t.Fatalf("page = %+v, %v", page, err)
	}
	if g := page.Groups[0]; g.Number != 6 || g.Status != StatusResolved || *g.Duration != time.Hour ||
		g.Resolution.Actor == nil || g.Resolution.Actor.Name != "bot" {
		t.Errorf("first = %+v", g)
	}
	page, err = h.svc.Related(ctx, cur.PublicID, page.Next, 2)
	if err != nil || len(page.Groups) != 1 || page.Next != nil || page.Groups[0].Number != 1 ||
		*page.Groups[0].Duration != 10*time.Minute || page.Groups[0].Resolution.By != ResolvedBySystem {
		t.Errorf("second page = %+v, %v", page, err)
	}
	open, _ := h.svc.Related(ctx, later.PublicID, nil, 0)
	if len(open.Groups) != 1 || open.Groups[0].Number != 4 || open.Groups[0].Duration != nil ||
		open.Groups[0].Resolution != nil {
		t.Errorf("an open one = %+v", open)
	}
	for _, id := range []string{"bad", "AGZZZZZZZZZZZZ"} {
		if _, err := h.svc.Related(ctx, id, nil, 5); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v", id, err)
		}
	}
	for _, q := range []string{"GetGroupKey", "ListRelatedGroups", "ListServiceAccountRefs"} {
		h.db.fail[q] = errBoom
		if _, err := h.svc.Related(ctx, cur.PublicID, nil, 5); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", q, err)
		}
		delete(h.db.fail, q)
	}
}

// TestOpenCounts counts the open Alert Groups with an Alert from each Integration (C-09.FR-21).
func TestOpenCounts(t *testing.T) {
	h := newHarness(t)
	h.db.ints[6] = dbgen.ListIntegrationRefsRow{ID: 6, PublicID: "NTBBBBBBBBBBBB", Name: "other"}
	g := h.summary(1, 2, t0, time.Time{}, "A", nil)
	g.IntegrationIds = []int64{5, 6}
	h.summary(2, 2, t0, time.Time{}, "B", nil)
	h.summary(3, 2, t0, t0, "C", nil)
	got, err := h.svc.OpenCounts(t.Context(), []string{"NTAAAAAAAAAAAA", "NTBBBBBBBBBBBB", "NTCCCCCCCCCCCC"})
	if err != nil || !maps.Equal(got, map[string]int64{"NTAAAAAAAAAAAA": 2, "NTBBBBBBBBBBBB": 1}) {
		t.Errorf("counts = %v, %v", got, err)
	}
	if got, err := h.svc.OpenCounts(t.Context(), nil); err != nil || len(got) != 0 {
		t.Errorf("none = %v, %v", got, err)
	}
	h.db.fail["CountOpenGroupsByIntegration"] = errBoom
	if _, err := h.svc.OpenCounts(t.Context(), []string{"NTAAAAAAAAAAAA"}); !errors.Is(err, errBoom) {
		t.Errorf("failure = %v", err)
	}
}

// TestHints: every change sends alert-group with the public_id; a new Alert Group and a Reopen also send
// alert-groups (C-09.FR-25, C-09.AC-13).
func TestHints(t *testing.T) {
	h := newHarness(t)
	a := h.alert(2, "warning", map[string]string{"alertname": "DiskFull", "cluster": "a"})
	b := h.alert(2, "warning", map[string]string{"alertname": "DiskFull", "cluster": "a", "pod": "b"})
	h.changes(t, ingest.ChangeFired, a)
	g := h.groupOf(t, a)
	want := []db.Hint{{OrgID: orgID, Type: live.HintAlertGroup, ID: g.PublicID},
		{OrgID: orgID, Type: live.HintAlertGroups}}
	if !slices.Equal(h.db.hints, want) {
		t.Errorf("creation hints %v", h.db.hints)
	}
	h.db.hints = nil
	h.changes(t, ingest.ChangeFired, b)
	if !slices.Equal(h.db.hints, want[:1]) {
		t.Errorf("join hints %v", h.db.hints)
	}
	h.db.hints = nil
	h.resolve(t, a, b)
	if !slices.Equal(h.db.hints, want[:1]) {
		t.Errorf("resolve hints %v", h.db.hints)
	}
	h.db.hints = nil
	h.refire(t, a)
	if !slices.Equal(h.db.hints, want) {
		t.Errorf("reopen hints %v", h.db.hints)
	}
	// A failing hint fails the change: the first, alert-group, or the second, alert-groups.
	for _, second := range []bool{false, true} {
		c := h.alert(2, "warning", map[string]string{"alertname": "DiskFull", "cluster": fmt.Sprint(second)})
		if second {
			h.db.before["Notify"] = func() { h.db.before["Notify"] = func() { h.db.fail["Notify"] = errBoom } }
		} else {
			h.db.fail["Notify"] = errBoom
		}
		if _, err := h.apply(ingest.ChangeFired, c); !errors.Is(err, errBoom) {
			t.Errorf("a failing hint (second %v) = %v", second, err)
		}
		delete(h.db.fail, "Notify")
	}
}

// TestUrgencyFollowsConfiguration is the decision on stale urgency: reads derive it from the Route and
// organization.critical_is_urgent as they are now, so marking a Route urgent shows at once on its open Alert Groups,
// without changing their status, and the urgent filter follows.
func TestUrgencyFollowsConfiguration(t *testing.T) {
	h := newHarness(t)
	g := h.summary(1, 2, t0.Add(-time.Hour), time.Time{}, "A", nil)
	urgent := true
	if v, _ := h.svc.Get(t.Context(), g.PublicID); v.Urgent {
		t.Fatal("urgent before the route is")
	}
	h.db.routes[2].Urgent = true
	if v, _ := h.svc.Get(t.Context(), g.PublicID); !v.Urgent || v.Status != StatusFiring {
		t.Errorf("after the route became urgent %+v", v)
	}
	if got := numbers(h.list(t, ListRequest{Filter: Filter{Urgent: &urgent}})); !slices.Equal(got, []int64{1}) {
		t.Errorf("urgent filter = %v", got)
	}
	h.db.routes[2].Urgent = false
	g.SeverityLevel = "critical"
	h.db.settings.CriticalIsUrgent = false
	if v, _ := h.svc.Get(t.Context(), g.PublicID); v.Urgent {
		t.Error("critical is urgent is off")
	}
}

// recordingDB is a dbgen.DBTX that keeps the arguments of the last statement.
type recordingDB struct{ args []any }

func (r *recordingDB) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	r.args = args
	return pgconn.CommandTag{}, nil
}

func (r *recordingDB) Query(_ context.Context, _ string, args ...any) (pgx.Rows, error) {
	r.args = args
	return nil, errBoom
}

func (r *recordingDB) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	r.args = args
	return nil
}

// TestCustomPlans: the list's connection runs every statement as an unnamed statement, which PostgreSQL plans with
// the parameters, by passing pgx the mode before the arguments.
func TestCustomPlans(t *testing.T) {
	rec := &recordingDB{}
	c := customPlans{db: rec}
	for _, run := range []func(){
		func() { _, _ = c.Exec(t.Context(), "SELECT $1", 1) },
		func() { _, _ = c.Query(t.Context(), "SELECT $1", 1) },
		func() { _ = c.QueryRow(t.Context(), "SELECT $1", 1) },
	} {
		rec.args = nil
		run()
		if len(rec.args) != 2 || rec.args[0] != pgx.QueryExecModeDescribeExec || rec.args[1] != 1 {
			t.Errorf("args %v", rec.args)
		}
	}
}

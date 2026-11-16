// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/matchers"
	"github.com/muster-io/muster/internal/organization"
)

func mustMatcher(t *testing.T, raw string) matchers.Matcher {
	t.Helper()
	m, err := matchers.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// october is a day of October 2026.
func october(d int) pgtype.Date {
	return pgtype.Date{Time: time.Date(2026, time.October, d, 0, 0, 0, 0, time.UTC), Valid: true}
}

// TestStatistics assembles the aggregation of C-09.FR-15 (C-09.AC-17): one item per Route that is not deleted, with
// its totals and the days with Alert Groups, medians rounded to seconds and null without any duration; a deleted
// Route with Alert Groups in the period, the default period and time zone, and the narrowing by id.
func TestStatistics(t *testing.T) {
	h := newHarness(t)
	h.db.routes[4] = &fakeRoute{LockRoutesRow: dbgen.LockRoutesRow{ID: 4, PublicID: "RTOLDAAAAAAAAA"}, name: "old",
		deleted: true}
	h.db.routes[5] = &fakeRoute{LockRoutesRow: dbgen.LockRoutesRow{ID: 5, PublicID: "RTGONEAAAAAAAA"}, name: "gone",
		deleted: true}
	h.db.timeZone = "Europe/Berlin"
	h.db.stats = []dbgen.RouteStatisticsRow{
		{SubjectID: 2, Total: true, AlertGroupCount: 3, ResolveCount: 3, ResolveMedian: 1200, ResolveP95: 1740.4},
		{SubjectID: 2, Day: october(6), AlertGroupCount: 1, ResolveCount: 1, ResolveMedian: 600,
			ResolveP95: 600},
		{SubjectID: 2, Day: october(7), AlertGroupCount: 2, ResolveCount: 2, ResolveMedian: 1500,
			ResolveP95: 1770, AckCount: 1, AckMedian: 59.5, AckP95: 59.5},
		{SubjectID: 4, Total: true, AlertGroupCount: 1},
		{SubjectID: 4, Day: october(1), AlertGroupCount: 1},
		{SubjectID: 99, Total: true, AlertGroupCount: 1},
	}
	st, err := h.svc.Statistics(t.Context(), StatisticsRequest{GroupBy: ByRoute})
	if err != nil {
		t.Fatal(err)
	}
	if !st.From.Equal(t0.Add(-ListRange+time.Microsecond)) || !st.To.Equal(t0.Add(time.Microsecond)) ||
		st.GroupBy != ByRoute || h.db.calls["RouteStatistics Europe/Berlin"] != 1 {
		t.Errorf("period %v – %v, calls %v", st.From, st.To, h.db.calls)
	}
	var names []string
	for _, it := range st.Items {
		names = append(names, it.Subject.Name)
	}
	if len(names) != 4 || names[0] != "Default" || names[1] != "db" || names[2] != "net" || names[3] != "old" {
		t.Fatalf("items %v", names)
	}
	db := st.Items[1]
	if db.AlertGroupCount != 3 || *db.TimeToResolve.Median != 1200 || *db.TimeToResolve.P95 != 1740 ||
		db.TimeToAcknowledge.Count != 0 || db.TimeToAcknowledge.Median != nil || len(db.PerDay) != 2 ||
		!db.PerDay[1].Date.Equal(time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)) || db.PerDay[1].AlertGroupCount != 2 ||
		*db.PerDay[1].TimeToAcknowledge.Median != 60 || db.PerDay[1].TimeToResolve.Count != 2 {
		t.Errorf("db = %+v", db)
	}
	if net := st.Items[2]; net.AlertGroupCount != 0 || len(net.PerDay) != 0 || net.PerDay == nil {
		t.Errorf("a route without alert groups = %+v", net)
	}
	// Narrowed to one Route, in another zone and period; per Integration with the Integration of the rows.
	from, to := t0.Add(-30*24*time.Hour), t0
	st, err = h.svc.Statistics(t.Context(), StatisticsRequest{GroupBy: ByRoute,
		Filter: Filter{Routes: []string{"RTDBAAAAAAAAAA"}, From: &from, To: &to}, TimeZone: "UTC"})
	if err != nil || len(st.Items) != 1 || st.Items[0].AlertGroupCount != 3 || !st.From.Equal(from) {
		t.Errorf("narrowed = %+v, %v", st, err)
	}
	h.db.stats = []dbgen.RouteStatisticsRow{{SubjectID: 5, Total: true, AlertGroupCount: 5}}
	st, err = h.svc.Statistics(t.Context(), StatisticsRequest{GroupBy: ByIntegration,
		Filter: Filter{Integrations: []string{"NTAAAAAAAAAAAA"}}})
	if err != nil || len(st.Items) != 1 || st.Items[0].Subject.Name != "lab" || st.Items[0].AlertGroupCount != 5 {
		t.Errorf("per integration = %+v, %v", st, err)
	}
	if st, _ = h.svc.Statistics(t.Context(), StatisticsRequest{GroupBy: ByIntegration}); len(st.Items) != 1 {
		t.Errorf("every integration = %+v", st)
	}
}

// TestStatisticsErrors: an unknown time zone, Route, Integration, Team or User is a FieldError, as is a period that
// does not start before it ends, me or mine from a Service account, and a Service account's id as who made the
// first acknowledgement; the failures of the queries.
func TestStatisticsErrors(t *testing.T) {
	h := newHarness(t)
	h.people()
	h.withTeams()
	ctx := t.Context()
	for _, c := range []struct {
		r       StatisticsRequest
		pointer string
		code    string
	}{
		{StatisticsRequest{GroupBy: ByRoute, TimeZone: "Mars/Olympus"}, "/query/time_zone", CodeInvalid},
		{StatisticsRequest{GroupBy: ByRoute, TimeZone: "Local"}, "/query/time_zone", CodeInvalid},
		{StatisticsRequest{GroupBy: ByRoute, Filter: Filter{Routes: []string{"RTZZZZZZZZZZZZ"}}}, "/query/route",
			CodeUnknownID},
		{StatisticsRequest{GroupBy: ByIntegration, Filter: Filter{Integrations: []string{"NTZZZZZZZZZZZZ"}}},
			"/query/integration", CodeUnknownID},
		{StatisticsRequest{GroupBy: ByTeam, Filter: Filter{Team: "TMZZZZZZZZZZZZ"}}, "/query/team", CodeUnknownID},
		{StatisticsRequest{GroupBy: ByRoute, Filter: Filter{To: &t0, From: &t0}}, "/query/from", CodeOutOfRange},
		{StatisticsRequest{GroupBy: ByRoute, Filter: Filter{FirstAcknowledgedBy: "SRZZZZZZZZZZZZ"}},
			"/query/first_acknowledged_by", CodeUnknownID},
		{StatisticsRequest{GroupBy: ByRoute, Filter: Filter{FirstAcknowledgedBy: "SAAAAAAAAAAA20"}},
			"/query/first_acknowledged_by", CodeUnknownID},
		{StatisticsRequest{GroupBy: ByRoute, Filter: Filter{FirstAcknowledgedBy: OwnerMe, Me: robot.Actor}},
			"/query/first_acknowledged_by", CodeUnsupported},
		{StatisticsRequest{GroupBy: ByTeam, Filter: Filter{Team: TeamMine, Me: robot.Actor}}, "/query/team",
			CodeUnsupported},
	} {
		_, err := h.svc.Statistics(ctx, c.r)
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Pointer != c.pointer || fe.Code != c.code {
			t.Errorf("%+v = %v", c.r, err)
		}
		if _, err := h.svc.ActorStatistics(ctx, c.r.Filter); c.pointer != "/query/time_zone" &&
			(!errors.As(err, &fe) || fe.Pointer != c.pointer || fe.Code != c.code) {
			t.Errorf("by people %+v = %v", c.r.Filter, err)
		}
	}
	// A zone that the database does not know, as its time zone data is older than Go's.
	for _, groupBy := range []string{ByRoute, ByIntegration, ByTeam} {
		query := map[string]string{ByRoute: "RouteStatistics", ByIntegration: "IntegrationStatistics",
			ByTeam: "TeamStatistics"}[groupBy]
		h.db.fail[query] = &pgconn.PgError{Code: "22023", Message: `time zone "America/Ciudad_Juarez" not recognized`}
		_, err := h.svc.Statistics(ctx, StatisticsRequest{GroupBy: groupBy, TimeZone: "America/Ciudad_Juarez"})
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Pointer != "/query/time_zone" {
			t.Errorf("%s with an unknown zone = %v", groupBy, err)
		}
		delete(h.db.fail, query)
	}
	matcher := []matchers.Matcher{mustMatcher(t, `pod=~"api-.*"`)}
	for _, c := range []struct {
		query   string
		groupBy string
		f       Filter
	}{{"GetListSettings", ByRoute, Filter{}}, {"RouteStatistics", ByRoute, Filter{}},
		{"ListStatisticsRoutes", ByRoute, Filter{}}, {"IntegrationStatistics", ByIntegration, Filter{}},
		{"ListStatisticsIntegrations", ByIntegration, Filter{}}, {"TeamStatistics", ByTeam, Filter{}},
		{"ListStatisticsTeams", ByTeam, Filter{}}, {"ListUsersByPublicID", ByRoute,
			Filter{FirstAcknowledgedBy: "SRAAAAAAAAAAA9"}},
		{"ListStatisticsGroupLabels", ByRoute, Filter{Matchers: matcher}}} {
		h.db.fail[c.query] = errBoom
		if _, err := h.svc.Statistics(ctx, StatisticsRequest{GroupBy: c.groupBy, Filter: c.f}); !errors.Is(err,
			errBoom) {
			t.Errorf("%s: %v", c.query, err)
		}
		delete(h.db.fail, c.query)
	}
	// Common labels that are not JSON.
	h.db.groups[1] = &dbgen.LockGroupsRow{ID: 1, CreatedAt: t0, CommonLabels: []byte("{")}
	if _, err := h.svc.Statistics(ctx, StatisticsRequest{GroupBy: ByRoute, Filter: Filter{Matchers: matcher}}); err ==
		nil {
		t.Error("labels that are not JSON were matched")
	}
	delete(h.db.groups, 1)
	for _, query := range []string{"ActorStatistics", "ListUserRefs", "ListServiceAccountRefs",
		"ListStatisticsGroupLabels"} {
		h.db.fail[query] = errBoom
		h.db.actorStats = []dbgen.ActorStatisticsRow{{AcknowledgedByUserID: i8(owner), AcknowledgedFirstCount: 1},
			{ServiceAccountID: i8(robotID), ResolvedCount: 1}}
		if _, err := h.svc.ActorStatistics(ctx, Filter{Matchers: matcher}); !errors.Is(err, errBoom) {
			t.Errorf("by people, %s: %v", query, err)
		}
		delete(h.db.fail, query)
	}
	// A row that names a User the database does not have.
	h.db.actorStats = []dbgen.ActorStatisticsRow{{ResolvedByUserID: i8(404), ResolvedCount: 1}}
	if _, err := h.svc.ActorStatistics(ctx, Filter{}); !errors.Is(err, errUnknownActor) {
		t.Errorf("an unknown user = %v", err)
	}
}

// TestStatisticsFilters is C-09.FR-30 in the statistics: every filter reaches the aggregations, resolved as the list
// resolves it — the Routes and Integrations as filters for every group_by, the Severity levels, Urgent, Reopened, a
// Team or the caller's Teams, the = Matchers by containment and the User of the first acknowledgement, a User or me — and a Matcher
// that Go applies narrows them to the ids of the Alert Groups whose common labels match (C-09.AC-32).
func TestStatisticsFilters(t *testing.T) {
	h := newHarness(t)
	h.people()
	h.withTeams()
	h.db.teamsOf().users[owner] = []int64{payments, search}
	yes := true
	f := Filter{Routes: []string{"RTDBAAAAAAAAAA"}, Integrations: []string{"NTAAAAAAAAAAAA"},
		Severities: []organization.SeverityLevel{"critical"}, Urgent: &yes, Reopened: &yes, Team: TeamMine,
		Matchers:            []matchers.Matcher{{Name: "namespace", Op: matchers.Equal, Value: "payments"}},
		FirstAcknowledgedBy: OwnerMe, Me: alice.Actor}
	for _, groupBy := range []string{ByRoute, ByIntegration, ByTeam} {
		if _, err := h.svc.Statistics(t.Context(), StatisticsRequest{GroupBy: groupBy, Filter: f}); err != nil {
			t.Fatal(err)
		}
		p := h.db.statsArgs
		if !slices.Equal(p.RouteIds, []int64{2}) || !slices.Equal(p.IntegrationIds, []int64{5}) ||
			!slices.Equal(p.Severities, []string{"critical"}) || !p.Urgent.Bool || !p.Reopened.Bool || !p.TeamSet ||
			!slices.Equal(p.TeamIds, []int64{payments, search}) || string(p.Contains) != `{"namespace":"payments"}` ||
			p.FirstAcknowledgedBy != i8(owner) || p.IdsSet {
			t.Errorf("%s: params %+v", groupBy, p)
		}
	}
	f.FirstAcknowledgedBy = "SRAAAAAAAAAAB0"
	if _, err := h.svc.ActorStatistics(t.Context(), f); err != nil || h.db.actorArgs.FirstAcknowledgedBy != i8(bobID) ||
		!h.db.actorArgs.TeamSet || h.db.actorArgs.IdsSet {
		t.Errorf("by people = %v, params %+v", err, h.db.actorArgs)
	}
	// A Matcher =~ is applied in Go: only the Alert Groups of the period whose common labels match are aggregated.
	for id, labels := range map[int64]string{1: `{"pod":"api-1"}`, 2: `{"pod":"web-1"}`, 3: `{"pod":"api-2"}`,
		4: `{"pod":"api-3"}`} {
		h.db.groups[id] = &dbgen.LockGroupsRow{ID: id, CreatedAt: t0.Add(-time.Hour), CommonLabels: []byte(labels)}
	}
	h.db.groups[4].CreatedAt = t0.Add(-30 * 24 * time.Hour)
	g := Filter{Matchers: []matchers.Matcher{mustMatcher(t, `pod=~"api-.*"`)}}
	if _, err := h.svc.Statistics(t.Context(), StatisticsRequest{GroupBy: ByRoute, Filter: g}); err != nil ||
		!h.db.statsArgs.IdsSet || !slices.Equal(h.db.statsArgs.Ids, []int64{1, 3}) {
		t.Errorf("rowwise = %v, params %+v", err, h.db.statsArgs)
	}
	if _, err := h.svc.ActorStatistics(t.Context(), g); err != nil || !h.db.actorArgs.IdsSet ||
		!slices.Equal(h.db.actorArgs.Ids, []int64{1, 3}) {
		t.Errorf("rowwise by people = %v, params %+v", err, h.db.actorArgs)
	}
	g.Matchers[0] = mustMatcher(t, `pod=~"none-.*"`)
	if _, err := h.svc.ActorStatistics(t.Context(), g); err != nil || !h.db.actorArgs.IdsSet ||
		h.db.actorArgs.Ids == nil || len(h.db.actorArgs.Ids) != 0 {
		t.Errorf("no match = %v, params %+v", err, h.db.actorArgs)
	}
}

// TestStatisticsByTeam is C-09.FR-29 (C-09.AC-31): one item per Team that is not deleted and per deleted Team with
// Alert Groups in the period, by name, with the values of the rows; a Team chosen by the filter is the only item, the
// caller's Teams for mine, and none when the caller is in no Team.
func TestStatisticsByTeam(t *testing.T) {
	h := newHarness(t)
	h.people()
	h.withTeams()
	ts := h.db.teamsOf()
	ts.refs[13] = dbgen.ListTeamRefsRow{ID: 13, PublicID: "TMGONEAAAAAAAA", Name: "Gone", Deleted: true}
	ts.refs[14] = dbgen.ListTeamRefsRow{ID: 14, PublicID: "TMOLDAAAAAAAAA", Name: "Old", Deleted: true}
	ts.users[owner] = []int64{search}
	h.db.stats = []dbgen.RouteStatisticsRow{
		{SubjectID: payments, Total: true, AlertGroupCount: 2, AckCount: 2, AckMedian: 120, AckP95: 177},
		{SubjectID: payments, Day: october(7), AlertGroupCount: 2, AckCount: 2, AckMedian: 120, AckP95: 177},
		{SubjectID: search, Total: true, AlertGroupCount: 1, AckCount: 1, AckMedian: 60, AckP95: 60},
		{SubjectID: search, Day: october(7), AlertGroupCount: 1, AckCount: 1, AckMedian: 60, AckP95: 60},
		{SubjectID: 13, Total: true, AlertGroupCount: 1},
	}
	st, err := h.svc.Statistics(t.Context(), StatisticsRequest{GroupBy: ByTeam})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range st.Items {
		got = append(got, fmt.Sprintf("%s:%d", it.Subject.Name, it.AlertGroupCount))
	}
	if st.GroupBy != ByTeam || !slices.Equal(got, []string{"Checkout:0", "Gone:1", "Payments:2", "Search:1"}) ||
		h.db.calls["TeamStatistics UTC"] != 1 {
		t.Fatalf("items %v", got)
	}
	if p := st.Items[2]; *p.TimeToAcknowledge.P95 != 177 || len(p.PerDay) != 1 || p.Subject.PublicID != "TMPAYMENTSAAAA" {
		t.Errorf("payments = %+v", p)
	}
	st, err = h.svc.Statistics(t.Context(), StatisticsRequest{GroupBy: ByTeam, Filter: Filter{Team: "TMPAYMENTSAAAA"}})
	if err != nil || len(st.Items) != 1 || st.Items[0].Subject.Name != "Payments" || !h.db.statsArgs.TeamSet {
		t.Errorf("one team = %+v, %v", st.Items, err)
	}
	st, err = h.svc.Statistics(t.Context(), StatisticsRequest{GroupBy: ByTeam, Filter: Filter{Team: TeamMine,
		Me: alice.Actor}})
	if err != nil || len(st.Items) != 1 || st.Items[0].Subject.Name != "Search" {
		t.Errorf("my teams = %+v, %v", st.Items, err)
	}
	calls := h.db.calls["TeamStatistics UTC"]
	st, err = h.svc.Statistics(t.Context(), StatisticsRequest{GroupBy: ByTeam, Filter: Filter{Team: TeamMine,
		Me: bob.Actor}})
	if err != nil || len(st.Items) != 0 || st.Items == nil || h.db.calls["TeamStatistics UTC"] != calls {
		t.Errorf("in no team = %+v, %v", st, err)
	}
}

// TestActorStatistics is C-09.FR-28 (C-09.AC-28, AC-30): Alice with three Alert Groups acknowledged first, Bob with one
// and two resolved, the Service account robot with one resolved, a deleted User who resolved one, and the row of the
// Alert Groups whose first acknowledgement names nobody, last: by acknowledged_first_count, most first, then by name.
// Times are rounded to seconds and null without an acknowledgement.
func TestActorStatistics(t *testing.T) {
	h := newHarness(t)
	h.people()
	h.db.users[12] = dbgen.ListUserRefsRow{ID: 12, PublicID: "SRAAAAAAAAAAB2", Name: "Deleted user 12",
		Login: "deleted-12", Status: "deleted"}
	h.db.accounts[21] = dbgen.ListServiceAccountRefsRow{ID: 21, PublicID: "SAAAAAAAAAAA21", Name: "robot",
		Status: "deleted"}
	h.db.actorStats = []dbgen.ActorStatisticsRow{
		{ServiceAccountID: i8(robotID), ResolvedCount: 1},
		{AcknowledgedFirstCount: 2, AckMedian: 450.4, AckP95: 870.6},
		{AcknowledgedByUserID: i8(bobID), ResolvedByUserID: i8(bobID), AcknowledgedFirstCount: 1, ResolvedCount: 2,
			AckMedian: 600, AckP95: 600},
		{ResolvedByUserID: i8(12), ResolvedCount: 1},
		{AcknowledgedByUserID: i8(owner), AcknowledgedFirstCount: 3, AckMedian: 240, AckP95: 1644},
		{ServiceAccountID: i8(21), ResolvedCount: 3},
	}
	from := t0.Add(-24 * time.Hour)
	st, err := h.svc.ActorStatistics(t.Context(), Filter{From: &from})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range st.Items {
		name := "unknown"
		if it.Actor != nil {
			name = fmt.Sprintf("%s/%s/%v", it.Actor.Kind, it.Actor.Name, it.Actor.Deactivated)
		}
		m, p := "-", "-"
		if it.TimeToAcknowledge.Median != nil {
			m, p = fmt.Sprint(*it.TimeToAcknowledge.Median), fmt.Sprint(*it.TimeToAcknowledge.P95)
		}
		got = append(got, fmt.Sprintf("%s %d %d %d %s %s", name, it.AcknowledgedFirst, it.Resolved,
			it.TimeToAcknowledge.Count, m, p))
	}
	want := []string{"user/Alice/false 3 0 3 240 1644", "user/Bob/false 1 2 1 600 600",
		"user/Deleted user 12/true 0 1 0 - -", "service_account/robot/false 0 1 0 - -",
		"service_account/robot/true 0 3 0 - -", "unknown 2 0 2 450 871"}
	if !slices.Equal(got, want) || !st.From.Equal(from) || !st.To.Equal(t0.Add(time.Microsecond)) {
		t.Errorf("items\n%s\nwant\n%s\nperiod %v – %v", strings.Join(got, "\n"), strings.Join(want, "\n"), st.From,
			st.To)
	}
	if st.Items[4].Actor.PublicID != "SAAAAAAAAAAA21" {
		t.Errorf("same name, by public_id: %+v", st.Items[3:5])
	}
	h.db.actorStats = nil
	if st, err := h.svc.ActorStatistics(t.Context(), Filter{}); err != nil || st.Items == nil || len(st.Items) != 0 {
		t.Errorf("nobody = %+v, %v", st, err)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/groups/dbgen"
)

func day(y int, m time.Month, d int) pgtype.Date {
	return pgtype.Date{Time: time.Date(y, m, d, 0, 0, 0, 0, time.UTC), Valid: true}
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
		{SubjectID: 2, Day: day(2026, 10, 6), AlertGroupCount: 1, ResolveCount: 1, ResolveMedian: 600,
			ResolveP95: 600},
		{SubjectID: 2, Day: day(2026, 10, 7), AlertGroupCount: 2, ResolveCount: 2, ResolveMedian: 1500,
			ResolveP95: 1770, AckCount: 1, AckMedian: 59.5, AckP95: 59.5},
		{SubjectID: 4, Total: true, AlertGroupCount: 1},
		{SubjectID: 4, Day: day(2026, 10, 1), AlertGroupCount: 1},
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
	st, err = h.svc.Statistics(t.Context(), StatisticsRequest{GroupBy: ByRoute, Routes: []string{"RTDBAAAAAAAAAA"},
		From: &from, To: &to, TimeZone: "UTC"})
	if err != nil || len(st.Items) != 1 || st.Items[0].AlertGroupCount != 3 || !st.From.Equal(from) {
		t.Errorf("narrowed = %+v, %v", st, err)
	}
	h.db.stats = []dbgen.RouteStatisticsRow{{SubjectID: 5, Total: true, AlertGroupCount: 5}}
	st, err = h.svc.Statistics(t.Context(), StatisticsRequest{GroupBy: ByIntegration,
		Integrations: []string{"NTAAAAAAAAAAAA"}})
	if err != nil || len(st.Items) != 1 || st.Items[0].Subject.Name != "lab" || st.Items[0].AlertGroupCount != 5 {
		t.Errorf("per integration = %+v, %v", st, err)
	}
	if st, _ = h.svc.Statistics(t.Context(), StatisticsRequest{GroupBy: ByIntegration}); len(st.Items) != 1 {
		t.Errorf("every integration = %+v", st)
	}
}

// TestStatisticsErrors: the ids of the other kind are unsupported, an unknown time zone or id is invalid, and the
// failures of the queries.
func TestStatisticsErrors(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	for _, c := range []struct {
		r       StatisticsRequest
		pointer string
		code    string
	}{
		{StatisticsRequest{GroupBy: ByRoute, Integrations: []string{"NTAAAAAAAAAAAA"}}, "/query/integration",
			CodeUnsupported},
		{StatisticsRequest{GroupBy: ByIntegration, Routes: []string{"RTDBAAAAAAAAAA"}}, "/query/route",
			CodeUnsupported},
		{StatisticsRequest{GroupBy: ByRoute, TimeZone: "Mars/Olympus"}, "/query/time_zone", CodeInvalid},
		{StatisticsRequest{GroupBy: ByRoute, TimeZone: "Local"}, "/query/time_zone", CodeInvalid},
		{StatisticsRequest{GroupBy: ByRoute, Routes: []string{"RTZZZZZZZZZZZZ"}}, "/query/route", CodeUnknownID},
		{StatisticsRequest{GroupBy: ByIntegration, Integrations: []string{"NTZZZZZZZZZZZZ"}}, "/query/integration",
			CodeUnknownID},
		{StatisticsRequest{GroupBy: ByRoute, To: &t0, From: &t0}, "/query/from", CodeOutOfRange},
	} {
		_, err := h.svc.Statistics(ctx, c.r)
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Pointer != c.pointer || fe.Code != c.code {
			t.Errorf("%+v = %v", c.r, err)
		}
	}
	// A zone that the database does not know, as its time zone data is older than Go's.
	for _, groupBy := range []string{ByRoute, ByIntegration} {
		query := map[string]string{ByRoute: "RouteStatistics", ByIntegration: "IntegrationStatistics"}[groupBy]
		h.db.fail[query] = &pgconn.PgError{Code: "22023", Message: `time zone "America/Ciudad_Juarez" not recognized`}
		_, err := h.svc.Statistics(ctx, StatisticsRequest{GroupBy: groupBy, TimeZone: "America/Ciudad_Juarez"})
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Pointer != "/query/time_zone" {
			t.Errorf("%s with an unknown zone = %v", groupBy, err)
		}
		delete(h.db.fail, query)
	}
	for _, c := range []struct {
		query   string
		groupBy string
	}{{"GetListSettings", ByRoute}, {"RouteStatistics", ByRoute}, {"ListStatisticsRoutes", ByRoute},
		{"IntegrationStatistics", ByIntegration}, {"ListStatisticsIntegrations", ByIntegration}} {
		h.db.fail[c.query] = errBoom
		if _, err := h.svc.Statistics(ctx, StatisticsRequest{GroupBy: c.groupBy}); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", c.query, err)
		}
		delete(h.db.fail, c.query)
	}
}

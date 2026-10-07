// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package groups_test

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/integrations"
	"github.com/muster-io/muster/internal/matchers"
	"github.com/muster-io/muster/internal/organization"
)

// row is one summary row the list tests insert, and what the oracle of the test knows about it.
type row struct {
	id                 int64
	publicID           string
	number             int64
	route              int64
	ints               []int64
	severity           string
	status             string
	created, changed   time.Time
	resolved           *time.Time
	by, reason         string
	reopens            int64
	labels             map[string]string
	title, summary     string
	firstAck           *time.Time
	firingAgainAfterID *int64
}

// insert writes the summary row r as the list reads it, without its details.
func (e *env) insert(t *testing.T, r *row) {
	t.Helper()
	labels, _ := json.Marshal(r.labels)
	var resolvedAt, by, reason, summary any
	if r.resolved != nil {
		resolvedAt, by, reason = *r.resolved, r.by, r.reason
		if r.by == groups.ResolvedByUser {
			reason = nil
		}
	}
	if r.summary != "" {
		summary = r.summary
	}
	r.publicID = fmt.Sprintf("AG%012X", r.number)
	userID := any(nil)
	if r.by == groups.ResolvedByUser {
		userID = e.userID(t)
	}
	if err := e.d.Pool.QueryRow(t.Context(), `INSERT INTO alert_groups (org_id, public_id, number, route_id,
		group_key_labels, group_key_values, group_key_sha256, title, summary, common_labels, common_annotations,
		integration_ids, status, severity_level, urgent, reopen_count, resolved_at, resolved_by_kind,
		resolved_by_user_id, resolve_reason, first_acknowledged_at, firing_again_after_id, created_at,
		last_changed_at)
		VALUES ($1, $2, $3, $4, '{alertname}', '{}', sha256(convert_to($2::text, 'UTF8')), $5, $6, $7, '{}', $8, $9, $10, false, $11,
		        $12, $13, $14, $15, $16, $17, $18, $19)
		RETURNING id`, e.orgID, r.publicID, r.number, r.route, r.title, summary, labels, r.ints, r.status,
		r.severity, r.reopens, resolvedAt, by, userID, reason, r.firstAck, r.firingAgainAfterID, r.created,
		r.changed).Scan(&r.id); err != nil {
		t.Fatalf("insert #%d: %v", r.number, err)
	}
}

// userID is a User that resolves Alert Groups, created once.
func (e *env) userID(t *testing.T) int64 {
	t.Helper()
	var id int64
	err := e.d.Pool.QueryRow(t.Context(), `SELECT id FROM users WHERE login = 'resolver'`).Scan(&id)
	if err == nil {
		return id
	}
	if err := e.d.Pool.QueryRow(t.Context(), `INSERT INTO users (org_id, public_id, login, name, role, source,
		status, created_at, updated_at)
		VALUES ($1, 'SRRESAAAAAAAAA', 'resolver', 'Resolver', 'responder', 'local', 'active', $2, $2)
		RETURNING id`, e.orgID, t0).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// by2 creates the second Integration of a test.
var by2 = integrations.Requester{Actor: audit.System, Transport: audit.TransportSystem}

func integrationInput(name string) integrations.Input {
	return integrations.Input{Name: name, ConnectionMode: "webhook_only", DuplicateWindowSeconds: 45}
}

// fixture inserts 200 summary rows over the last 200 hours on two Routes and two Integrations, and returns them.
func (e *env) fixture(t *testing.T) ([]*row, map[string]int64) {
	t.Helper()
	ra, rb := e.route(t, "a", "a", "alertname"), e.route(t, "b", "b", "alertname")
	second, err := e.ints.Create(t.Context(), by2, integrationInput("lab2"))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{"a": ra.ID, "b": rb.ID, "lab": e.intID, "lab2": second.ID}
	var rows []*row
	for i := range 200 {
		r := &row{number: int64(i + 1), route: ra.ID, ints: []int64{e.intID}, severity: "warning",
			status: string(groups.StatusFiring), created: t0.Add(-time.Duration(i) * time.Hour),
			labels: map[string]string{"alertname": "A", "namespace": "billing"}, title: fmt.Sprintf("Alert %d", i)}
		r.changed = r.created
		if i%2 == 1 {
			r.route = rb.ID
		}
		if i%3 == 0 {
			r.ints = []int64{e.intID, second.ID}
		}
		if i%4 == 0 {
			r.severity = "critical"
		}
		if i%10 == 0 {
			r.labels["namespace"] = "payments"
		}
		if i%20 == 0 {
			r.labels["pod"] = fmt.Sprintf("api-%d", i)
		}
		if i%7 == 0 {
			r.reopens = 1
		}
		if i%5 != 0 {
			at := r.created.Add(30 * time.Minute)
			r.status, r.resolved, r.changed, r.by, r.reason = string(groups.StatusResolved), &at, at,
				groups.ResolvedBySystem, "resolved"
			if i%3 == 1 {
				r.reason = "gone"
			}
			if i%11 == 0 {
				r.by = groups.ResolvedByUser
			}
		}
		if i == 42 {
			r.title, r.summary = "KubePodCrashLooping", "Pod postgres-0 is crash looping"
		}
		e.insert(t, r)
		rows = append(rows, r)
	}
	return rows, ids
}

// all reads every page of the list by cursor.
func (e *env) all(t *testing.T, r groups.ListRequest) []int64 {
	t.Helper()
	r.Limit = 7
	var out []int64
	for range 100 {
		page, err := e.groups.List(t.Context(), r)
		if err != nil {
			t.Fatalf("list %+v: %v", r.Filter, err)
		}
		for _, v := range page.Groups {
			out = append(out, v.Number)
		}
		if page.Next == nil {
			return out
		}
		r.After = page.Next
	}
	t.Fatal("too many pages")
	return nil
}

// expect is the oracle: the numbers of the rows that keep matches, newest start first.
func expect(rows []*row, keep func(*row) bool) []int64 {
	var out []int64
	for _, r := range rows {
		if keep(r) {
			out = append(out, r.number)
		}
	}
	if out == nil {
		out = []int64{}
	}
	return out
}

func m(t *testing.T, s string) matchers.Matcher {
	t.Helper()
	out, err := matchers.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestIntegrationList is C-09.FR-13 on PostgreSQL with 200 summary rows: the default open tab over the last
// alert_group.list_range with an open Alert Group that started earlier (C-09.AC-5), the overlap rule, each filter
// against an oracle, Label Matchers on the common labels (C-09.AC-15, AC-25), search inside words and #N without the
// range (C-09.AC-20, AC-23), the counts, and the cursor of each order, stable under inserts.
func TestIntegrationList(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		rows, ids := e.fixture(t)
		from := t0.Add(-groups.ListRange)
		inRange := func(r *row) bool {
			return r.created.Before(t0.Add(time.Microsecond)) && (r.resolved == nil || !r.resolved.Before(from))
		}
		open := func(r *row) bool { return r.status != string(groups.StatusResolved) }
		if got, want := e.all(t, groups.ListRequest{}), expect(rows, func(r *row) bool {
			return open(r) && inRange(r)
		}); !slices.Equal(got, want) {
			t.Errorf("default = %v, want %v", got, want)
		}
		// An open Alert Group that started long before the range.
		old := &row{number: 1000, route: ids["a"], ints: []int64{e.intID}, severity: "info", status: "firing",
			created: t0.Add(-10 * 24 * time.Hour), labels: map[string]string{}, title: "Old"}
		old.changed = old.created
		e.insert(t, old)
		rows = append(rows, old)
		if got := e.all(t, groups.ListRequest{}); !slices.Contains(got, 1000) {
			t.Errorf("C-09.AC-5: the old open alert group is not listed: %v", got)
		}
		all := []groups.Status{groups.StatusFiring, groups.StatusAcknowledged, groups.StatusSnoozed,
			groups.StatusResolved}
		since, until := t0.Add(-100*time.Hour), t0.Add(-50*time.Hour)
		urgent, yes, no := true, true, false
		criticalIsUrgent := e.count(t, `SELECT count(*) FROM organizations WHERE critical_is_urgent`) == 1
		byUser, bySystem, gone := groups.ResolvedByUser, groups.ResolvedBySystem, "gone"
		for _, c := range []struct {
			name string
			f    groups.Filter
			keep func(*row) bool
		}{
			{"every status", groups.Filter{Statuses: all}, inRange},
			{"a range", groups.Filter{Statuses: all, From: &since, To: &until}, func(r *row) bool {
				return r.created.Before(until) && (r.resolved == nil || !r.resolved.Before(since))
			}},
			{"resolved", groups.Filter{Statuses: []groups.Status{groups.StatusResolved}}, func(r *row) bool {
				return !open(r) && inRange(r)
			}},
			{"route", groups.Filter{Statuses: all, Routes: []string{routePublicID(t, e, ids["b"])}}, func(r *row) bool {
				return inRange(r) && r.route == ids["b"]
			}},
			{"integration", groups.Filter{Statuses: all, Integrations: []string{intPublicID(t, e, ids["lab2"])}},
				func(r *row) bool { return inRange(r) && slices.Contains(r.ints, ids["lab2"]) }},
			{"severity", groups.Filter{Statuses: all, Severities: []organization.SeverityLevel{"critical"}},
				func(r *row) bool { return inRange(r) && r.severity == "critical" }},
			{"urgent", groups.Filter{Statuses: all, Urgent: &urgent}, func(r *row) bool {
				return inRange(r) && r.severity == "critical" && criticalIsUrgent
			}},
			{"resolved by a person", groups.Filter{Statuses: all, ResolvedBy: &byUser}, func(r *row) bool {
				return inRange(r) && !open(r) && r.by == groups.ResolvedByUser
			}},
			{"gone", groups.Filter{Statuses: all, ResolvedBy: &bySystem, ResolveReason: &gone}, func(r *row) bool {
				return inRange(r) && !open(r) && r.by == groups.ResolvedBySystem && r.reason == "gone"
			}},
			{"reopened", groups.Filter{Statuses: all, Reopened: &yes}, func(r *row) bool {
				return inRange(r) && r.reopens > 0
			}},
			{"not reopened", groups.Filter{Statuses: all, Reopened: &no}, func(r *row) bool {
				return inRange(r) && r.reopens == 0
			}},
			{"namespace=payments", groups.Filter{Statuses: all, Matchers: []matchers.Matcher{
				m(t, `namespace="payments"`)}}, func(r *row) bool {
				return inRange(r) && r.labels["namespace"] == "payments"
			}},
			{"pod=~api-.*", groups.Filter{Statuses: all, Matchers: []matchers.Matcher{m(t, `pod=~"api-.*"`)}},
				func(r *row) bool { return inRange(r) && strings.HasPrefix(r.labels["pod"], "api-") }},
			{"namespace!=payments", groups.Filter{Statuses: all, Matchers: []matchers.Matcher{
				m(t, `namespace!="payments"`)}}, func(r *row) bool {
				return inRange(r) && r.labels["namespace"] != "payments"
			}},
			{"pod absent", groups.Filter{Statuses: all, Matchers: []matchers.Matcher{m(t, `pod=""`)}},
				func(r *row) bool { return inRange(r) && r.labels["pod"] == "" }},
			{"payments and urgent", groups.Filter{Statuses: all, Urgent: &urgent, Matchers: []matchers.Matcher{
				m(t, `namespace="payments"`)}}, func(r *row) bool {
				return inRange(r) && r.labels["namespace"] == "payments" && r.severity == "critical" &&
					criticalIsUrgent
			}},
			{"text inside words", groups.Filter{Statuses: all, Query: "POSTGRES"}, func(r *row) bool {
				return inRange(r) && r.number == 43
			}},
			{"title", groups.Filter{Statuses: all, Query: "crashloop"}, func(r *row) bool {
				return inRange(r) && r.number == 43
			}},
			{"wildcards are text", groups.Filter{Statuses: all, Query: "Alert_1%"}, func(*row) bool { return false }},
		} {
			for _, sort := range []groups.Sort{groups.SortStartedDesc, groups.SortStarted, groups.SortChangedDesc,
				groups.SortChanged} {
				got := e.all(t, groups.ListRequest{Filter: c.f, Sort: sort})
				want := expect(rows, c.keep)
				ordered(rows, want, sort)
				if !slices.Equal(got, want) {
					t.Errorf("%s %s = %v, want %v", c.name, sort, got, want)
				}
			}
			counts, err := e.groups.Counts(t.Context(), c.f)
			if err != nil {
				t.Fatal(err)
			}
			want := expect(rows, c.keep)
			if c.f.Statuses == nil || !slices.Equal(c.f.Statuses, all) {
				continue // the counts are per status: only a filter of every status has them all
			}
			if counts.All != int64(len(want)) || counts.Firing+counts.Resolved != counts.All {
				t.Errorf("%s counts %+v, want %d", c.name, counts, len(want))
			}
		}
		// #N and number ignore the range and the default status: #200 started 199 hours ago and was resolved.
		for _, f := range []groups.Filter{{Query: "#200"}, {Number: ptr(int64(200))}} {
			if got := e.all(t, groups.ListRequest{Filter: f}); !slices.Equal(got, []int64{200}) {
				t.Errorf("number %+v = %v", f, got)
			}
		}
		if c, _ := e.groups.Counts(t.Context(), groups.Filter{Query: "#200"}); c.All != 1 || c.Resolved != 1 {
			t.Errorf("counts of #200 = %+v", c)
		}
		// label_values and the item fields.
		page, err := e.groups.List(t.Context(), groups.ListRequest{Filter: groups.Filter{Statuses: all,
			Number: ptr(int64(1))}, Limit: 1, LabelColumns: []string{"namespace", "pod", "cluster"}})
		if err != nil || len(page.Groups) != 1 {
			t.Fatalf("#1 = %+v, %v", page, err)
		}
		if v := page.Groups[0]; !maps.Equal(v.LabelValues, map[string]string{"namespace": "payments",
			"pod": "api-0"}) || !v.Urgent || len(v.Integrations) != 2 || v.Route.Name != "a" {
			t.Errorf("#1 = %+v", v)
		}
		// The cursor keeps its place when Alert Groups start between pages.
		first, err := e.groups.List(t.Context(), groups.ListRequest{Filter: groups.Filter{Statuses: all}, Limit: 5})
		if err != nil {
			t.Fatal(err)
		}
		e.insert(t, &row{number: 2000, route: ids["a"], ints: []int64{e.intID}, severity: "info", status: "firing",
			created: t0, changed: t0, labels: map[string]string{}, title: "New"})
		next, err := e.groups.List(t.Context(), groups.ListRequest{Filter: groups.Filter{Statuses: all}, Limit: 5,
			After: first.Next})
		if err != nil || len(next.Groups) != 5 || next.Groups[0].Number != first.Groups[4].Number+1 {
			t.Errorf("the next page after an insert = %v", next.Groups)
		}
	})
}

// ordered sorts the numbers of want in the order of the list.
func ordered(rows []*row, want []int64, sort groups.Sort) {
	byNumber := map[int64]*row{}
	for _, r := range rows {
		byNumber[r.number] = r
	}
	key := func(n int64) time.Time {
		if sort == groups.SortChanged || sort == groups.SortChangedDesc {
			return byNumber[n].changed
		}
		return byNumber[n].created
	}
	desc := sort == groups.SortStartedDesc || sort == groups.SortChangedDesc
	slices.SortFunc(want, func(a, b int64) int {
		c := key(a).Compare(key(b))
		if c == 0 {
			c = int(byNumber[a].id - byNumber[b].id)
		}
		if desc {
			return -c
		}
		return c
	})
}

func ptr[T any](v T) *T { return &v }

func routePublicID(t *testing.T, e *env, id int64) string {
	t.Helper()
	var p string
	if err := e.d.Pool.QueryRow(t.Context(), `SELECT public_id FROM routes WHERE id = $1`, id).Scan(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

func intPublicID(t *testing.T, e *env, id int64) string {
	t.Helper()
	var p string
	if err := e.d.Pool.QueryRow(t.Context(), `SELECT public_id FROM integrations WHERE id = $1`, id).Scan(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestIntegrationRelatedAndStatistics is C-09.FR-20 and C-09.FR-15 on PostgreSQL: the other Alert Groups of a Route
// and key, newest first; for a Route whose three Alert Groups resolved 10, 20 and 30 minutes after they started, 3
// Alert Groups with a median time to resolve of 1200 seconds, split by the days they started in Europe/Berlin, and the
// same Alert Groups under their Integration, one of two Integrations counted for both (C-09.AC-16, AC-17).
func TestIntegrationRelatedAndStatistics(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		st := e.route(t, "st", "st", "alertname")
		e.route(t, "idle", "idle", "alertname")
		second, err := e.ints.Create(ctx, by2, integrationInput("lab2"))
		if err != nil {
			t.Fatal(err)
		}
		// 21:55 and 22:10 UTC on 6 October are the 6th and the 7th in Berlin.
		starts := []time.Time{time.Date(2026, 10, 6, 21, 55, 0, 0, time.UTC),
			time.Date(2026, 10, 6, 22, 10, 0, 0, time.UTC), time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)}
		var made []*row
		for i, start := range starts {
			resolved := start.Add(time.Duration(10*(i+1)) * time.Minute)
			r := &row{number: int64(i + 1), route: st.ID, ints: []int64{e.intID}, severity: "warning",
				status: "resolved", created: start, changed: resolved, resolved: &resolved, by: "system",
				reason: "resolved", labels: map[string]string{}, title: "Stat"}
			if i == 2 {
				r.ints = []int64{e.intID, second.ID}
				ack := start.Add(time.Minute)
				r.firstAck = &ack
			}
			e.insert(t, r)
			made = append(made, r)
		}
		out, err := e.groups.Statistics(ctx, groups.StatisticsRequest{GroupBy: groups.ByRoute, TimeZone: "Europe/Berlin",
			Routes: []string{st.PublicID}})
		if err != nil || len(out.Items) != 1 {
			t.Fatalf("statistics = %+v, %v", out, err)
		}
		item := out.Items[0]
		if item.AlertGroupCount != 3 || *item.TimeToResolve.Median != 1200 || *item.TimeToResolve.P95 != 1740 ||
			item.TimeToAcknowledge.Count != 1 || *item.TimeToAcknowledge.Median != 60 || len(item.PerDay) != 2 ||
			item.PerDay[0].Date.Format(time.DateOnly) != "2026-10-06" || item.PerDay[0].AlertGroupCount != 1 ||
			item.PerDay[1].AlertGroupCount != 2 || *item.PerDay[1].TimeToResolve.Median != 1500 {
			t.Errorf("st = %+v", item)
		}
		// In UTC the first two started on the 6th.
		if out, _ := e.groups.Statistics(ctx, groups.StatisticsRequest{GroupBy: groups.ByRoute, TimeZone: "UTC",
			Routes: []string{st.PublicID}}); len(out.Items[0].PerDay) != 2 || out.Items[0].PerDay[0].AlertGroupCount != 2 {
			t.Errorf("in UTC = %+v", out.Items[0].PerDay)
		}
		// Every Route that is not deleted, one item each (C-09.AC-23).
		out, _ = e.groups.Statistics(ctx, groups.StatisticsRequest{GroupBy: groups.ByRoute})
		if n := e.count(t, `SELECT count(*) FROM routes WHERE deleted_at IS NULL`); int64(len(out.Items)) != n {
			t.Errorf("%d items for %d routes", len(out.Items), n)
		}
		out, err = e.groups.Statistics(ctx, groups.StatisticsRequest{GroupBy: groups.ByIntegration})
		if err != nil {
			t.Fatal(err)
		}
		counts := map[string]int64{}
		for _, it := range out.Items {
			counts[it.Subject.Name] = it.AlertGroupCount
		}
		if counts["lab"] != 3 || counts["lab2"] != 1 {
			t.Errorf("per integration %v", counts)
		}
		// Related: the others of the Route and key, newest first; another key is not related.
		var key []byte
		if err := e.d.Pool.QueryRow(ctx, `SELECT group_key_sha256 FROM alert_groups WHERE id = $1`,
			made[0].id).Scan(&key); err != nil {
			t.Fatal(err)
		}
		if _, err := e.d.Pool.Exec(ctx, `UPDATE alert_groups SET group_key_sha256 = $1 WHERE route_id = $2`, key,
			st.ID); err != nil {
			t.Fatal(err)
		}
		e.insert(t, &row{number: 9, route: st.ID, ints: []int64{e.intID}, severity: "info", status: "firing",
			created: t0, changed: t0, labels: map[string]string{}, title: "Other key"})
		page, err := e.groups.Related(ctx, made[2].publicID, nil, 1)
		if err != nil || len(page.Groups) != 1 || page.Groups[0].Number != 2 || page.Next == nil ||
			*page.Groups[0].Duration != 20*time.Minute || page.Groups[0].Resolution.By != "system" {
			t.Fatalf("related = %+v, %v", page, err)
		}
		page, err = e.groups.Related(ctx, made[2].publicID, page.Next, 5)
		if err != nil || len(page.Groups) != 1 || page.Groups[0].Number != 1 || page.Next != nil {
			t.Errorf("related next = %+v, %v", page, err)
		}
	})
}

// TestIntegrationRetention is C-09.FR-16 and C-09.AC-18 on PostgreSQL through Snapshot processing: once
// retention.alert_details has passed, the Leader's retention removes the Alerts inside the Alert Group, the reads hide
// the Timeline but the Notes, getAlertGroup carries details_removed, search and the label filter still find it; summary
// rows go after retention.alert_group_summaries with their Notes, also one that a later Alert Group fires again after
// (migration 0003) and one that an Alert of another Alert Group moved into once that Alert is gone. Running it twice
// deletes nothing more.
func TestIntegrationRetention(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		e.route(t, "db", "db", "alertname")
		const s1 = "2026-10-07T11:00:00Z"
		e.process(t, gk, alert("firing", s1, "alertname", "DiskFull", "team", "db", "namespace", "payments", "pod",
			"a"))
		g := e.groupOf(t, "a")
		e.process(t, gk, alert("resolved", s1, "alertname", "DiskFull", "team", "db", "namespace", "payments", "pod",
			"a"))
		if _, err := e.d.Pool.Exec(ctx, `INSERT INTO notes (org_id, public_id, alert_group_id, body, actor_kind,
			actor_user_id, transport, event_seq, created_at)
			VALUES ($1, 'NEAAAAAAAAAAAA', $2, 'checked', 'user', $3, 'ui', 99, $4)`, e.orgID, e.groupID(t, g),
			e.userID(t), t0); err != nil {
			t.Fatal(err)
		}
		store := groups.NewStore(e.d.Pool)
		if p, err := groups.Purge(ctx, store, e.orgID, e.clock.Now()); err != nil || p != (groups.Purged{}) {
			t.Fatalf("an early purge = %+v, %v", p, err)
		}
		e.clock.Advance(91 * 24 * time.Hour)
		p, err := groups.Purge(ctx, store, e.orgID, e.clock.Now())
		if err != nil || p.Details != 1 || p.Summaries != 0 {
			t.Fatalf("purge = %+v, %v", p, err)
		}
		if p, err := groups.Purge(ctx, store, e.orgID, e.clock.Now()); err != nil || p != (groups.Purged{}) {
			t.Errorf("a second purge = %+v, %v", p, err)
		}
		v, err := e.groups.Get(ctx, g)
		if err != nil || !v.DetailsRemoved || len(v.Notices) != 1 || *v.Notices[0].RetentionDays != 90 {
			t.Errorf("get = %+v, %v", v, err)
		}
		timeline, err := e.groups.Timeline(ctx, g, groups.TimelineFilter{Limit: 50})
		if err != nil || len(timeline.Entries) != 1 || timeline.Entries[0].Note.Body != "checked" {
			t.Errorf("timeline = %+v, %v", timeline, err)
		}
		if alerts, _ := e.groups.Alerts(ctx, g, groups.AlertFilter{Limit: 5}); len(alerts.Alerts) != 0 {
			t.Errorf("alerts = %+v", alerts)
		}
		since := t0.Add(-24 * time.Hour)
		f := groups.Filter{Statuses: []groups.Status{groups.StatusResolved}, From: &since, Query: "DiskFull",
			Matchers: []matchers.Matcher{m(t, `namespace="payments"`)}}
		if page, err := e.groups.List(ctx, groups.ListRequest{Filter: f, Limit: 5}); err != nil ||
			len(page.Groups) != 1 || page.Groups[0].PublicID != g || !page.Groups[0].DetailsRemoved {
			t.Errorf("search after the details went = %+v, %v", page, err)
		}
		// Summary rows: one resolved by a person that a later one fires again after, and one an Alert moved into.
		ancient := e.clock.Now().Add(-731 * 24 * time.Hour)
		manual := &row{number: 100, route: e.defaultRoute(t), ints: []int64{e.intID}, severity: "info",
			status: "resolved", created: ancient.Add(-time.Hour), changed: ancient, resolved: &ancient,
			by: groups.ResolvedByUser, labels: map[string]string{}, title: "Manual"}
		e.insert(t, manual)
		later := &row{number: 101, route: e.defaultRoute(t), ints: []int64{e.intID}, severity: "info",
			status: "firing", created: e.clock.Now(), changed: e.clock.Now(), labels: map[string]string{},
			title: "Later", firingAgainAfterID: &manual.id}
		e.insert(t, later)
		target := &row{number: 102, route: e.defaultRoute(t), ints: []int64{e.intID}, severity: "info",
			status: "resolved", created: ancient, changed: ancient, resolved: &ancient, by: "system",
			reason: "resolved", labels: map[string]string{}, title: "Target"}
		e.insert(t, target)
		var alertID int64
		if err := e.d.Pool.QueryRow(ctx, `SELECT id FROM alerts LIMIT 1`).Scan(&alertID); err != nil {
			t.Fatal(err)
		}
		recent := e.clock.Now().Add(-time.Hour)
		if _, err := e.d.Pool.Exec(ctx, `INSERT INTO alert_group_alerts (org_id, alert_group_id, alert_id, episode,
			state, joined_at, starts_at, annotations, ended_at, moved_to_alert_group_id)
			VALUES ($1, $2, $3, 7, 'moved', $4, $4, '{}', $4, $5)`, e.orgID, later.id, alertID, recent,
			target.id); err != nil {
			t.Fatal(err)
		}
		p, err = groups.Purge(ctx, store, e.orgID, e.clock.Now())
		if err != nil || p.Summaries != 1 {
			t.Fatalf("summaries = %+v, %v", p, err)
		}
		if e.count(t, `SELECT count(*) FROM alert_groups WHERE id = $1`, manual.id) != 0 ||
			e.count(t, `SELECT count(*) FROM alert_groups WHERE id = $1 AND firing_again_after_id IS NULL`,
				later.id) != 1 || e.count(t, `SELECT count(*) FROM alert_groups WHERE id = $1`, target.id) != 1 {
			t.Error("the summaries left are wrong")
		}
		// Once the moved Alert's details are gone too, the target goes.
		e.clock.Advance(91 * 24 * time.Hour)
		p, err = groups.Purge(ctx, store, e.orgID, e.clock.Now())
		if err != nil || p.Details != 1 || p.Summaries != 1 ||
			e.count(t, `SELECT count(*) FROM alert_groups WHERE id = $1`, target.id) != 0 {
			t.Errorf("target purge = %+v, %v", p, err)
		}
		// The summary of g goes 2 years after its resolution, with its Note.
		e.clock.Advance(731 * 24 * time.Hour)
		if p, err := groups.Purge(ctx, store, e.orgID, e.clock.Now()); err != nil || p.Summaries != 1 ||
			e.count(t, `SELECT count(*) FROM notes`) != 0 {
			t.Errorf("the last purge = %+v, %v", p, err)
		}
	})
}

func (e *env) defaultRoute(t *testing.T) int64 {
	t.Helper()
	return e.count(t, `SELECT id FROM routes WHERE is_default`)
}

// TestIntegrationListPlans checks, against 200,000 summary rows as in the spike of design/db/schema.md §8, that the
// list, the counts, the related Alert Groups and the statistics run with the plans of their parameters on the indexes
// of §4.9: the partial open index for the default tab, both trigram indexes for text, the jsonb_path_ops index for =
// Matchers, the unique (org_id, number) for #N, the related index, and no sequential scan of alert_groups.
func TestIntegrationListPlans(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		route := e.route(t, "plans", "plans", "alertname")
		if _, err := e.d.Pool.Exec(ctx, `INSERT INTO alert_groups (org_id, public_id, number, route_id,
			group_key_labels, group_key_values, group_key_sha256, title, summary, common_labels, common_annotations,
			integration_ids, status, severity_level, urgent, resolved_at, resolved_by_kind, resolve_reason,
			created_at, last_changed_at)
			SELECT $1, 'AG' || lpad(upper(to_hex(n)), 12, '0'), n, $2, '{alertname}', '{}',
			       sha256(n::text::bytea), 'Alert' || (n % 500),
			       CASE WHEN n % 1000 = 0 THEN 'Pod postgres-' || n || ' is crash looping'
			            ELSE 'Pod svc-' || n || ' restarted' END,
			       jsonb_build_object('alertname', 'Alert' || (n % 500), 'namespace', 'ns' || (n % 200)), '{}',
			       ARRAY[$3::bigint], CASE WHEN n % 100 = 0 THEN 'firing' ELSE 'resolved' END,
			       CASE WHEN n % 4 = 0 THEN 'critical' ELSE 'warning' END, false,
			       CASE WHEN n % 100 = 0 THEN NULL ELSE $4::timestamptz - n * interval '5 minutes' + interval '30 minutes' END,
			       CASE WHEN n % 100 = 0 THEN NULL ELSE 'system' END,
			       CASE WHEN n % 100 = 0 THEN NULL ELSE 'resolved' END,
			       $4::timestamptz - n * interval '5 minutes',
			       $4::timestamptz - n * interval '5 minutes'
			FROM generate_series(1, 200000) AS n`, e.orgID, route.ID, e.intID, t0); err != nil {
			t.Fatal(err)
		}
		if _, err := e.d.Pool.Exec(ctx, `ANALYZE alert_groups`); err != nil {
			t.Fatal(err)
		}
		capture := &captured{}
		q := dbgen.New(capture)
		base := func() dbgen.ListGroupsStartedDescParams {
			return dbgen.ListGroupsStartedDescParams{OrgID: e.orgID, Statuses: []string{"firing", "acknowledged",
				"snoozed"}, RangeTo: t0, RangeFrom: t0.Add(-groups.ListRange), RouteIds: []int64{},
				IntegrationIds: []int64{}, Severities: []string{}, Lim: 51}
		}
		allStatuses := []string{"firing", "acknowledged", "snoozed", "resolved"}
		for _, c := range []struct {
			name string
			run  func()
			uses []string
		}{
			{"default tab", func() { _, _ = q.ListGroupsStartedDesc(ctx, base()) }, []string{"alert_groups_open_idx"}},
			{"text", func() {
				p := base()
				p.Statuses, p.Pattern = allStatuses, pgtype.Text{String: "%postgres%", Valid: true}
				_, _ = q.ListGroupsStartedDesc(ctx, p)
			}, []string{"alert_groups_title_trgm_idx", "alert_groups_summary_trgm_idx"}},
			{"label", func() {
				p := base()
				p.Statuses, p.Contains = allStatuses, []byte(`{"namespace":"ns7"}`)
				_, _ = q.ListGroupsStartedDesc(ctx, p)
			}, []string{"alert_groups_labels_idx"}},
			{"number", func() {
				p := base()
				p.Statuses, p.Number = allStatuses, pgtype.Int8{Int64: 4242, Valid: true}
				_, _ = q.ListGroupsStartedDesc(ctx, p)
			}, []string{"alert_groups_org_id_number_key"}},
			{"range of every status", func() {
				p := base()
				p.Statuses = allStatuses
				_, _ = q.ListGroupsStartedDesc(ctx, p)
			}, []string{"alert_groups_"}},
			{"by last change", func() {
				p := base()
				p.Statuses = allStatuses
				_, _ = q.ListGroupsChangedDesc(ctx, dbgen.ListGroupsChangedDescParams(p))
			}, []string{"alert_groups_"}},
			{"counts of the default range", func() {
				p := base()
				_, _ = q.CountGroups(ctx, dbgen.CountGroupsParams{OrgID: p.OrgID, RangeTo: p.RangeTo,
					RangeFrom: p.RangeFrom, RouteIds: p.RouteIds, IntegrationIds: p.IntegrationIds,
					Severities: p.Severities})
			}, []string{"alert_groups_"}},
			{"related", func() {
				_, _ = q.ListRelatedGroups(ctx, dbgen.ListRelatedGroupsParams{OrgID: e.orgID, RouteID: route.ID,
					GroupKeySha256: make([]byte, 32), ID: 1, Lim: 51})
			}, []string{"alert_groups_related_idx"}},
			{"statistics per route", func() {
				_, _ = q.RouteStatistics(ctx, dbgen.RouteStatisticsParams{OrgID: e.orgID, TimeZone: "UTC",
					RangeFrom: t0.Add(-groups.ListRange), RangeTo: t0, RouteIds: []int64{}})
			}, []string{"alert_groups_"}},
		} {
			c.run()
			plan := explain(t, e, capture)
			for _, idx := range c.uses {
				if !strings.Contains(plan, idx) {
					t.Errorf("%s does not use %s:\n%s", c.name, idx, plan)
				}
			}
			if strings.Contains(plan, "Seq Scan on alert_groups") {
				t.Errorf("%s scans alert_groups sequentially:\n%s", c.name, plan)
			}
		}
	})
}

// captured is a dbgen.DBTX that keeps the last statement and its arguments instead of running them.
type captured struct {
	sql  string
	args []any
}

func (c *captured) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	c.sql, c.args = sql, args
	return pgconn.CommandTag{}, nil
}

func (c *captured) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	c.sql, c.args = sql, args
	return nil, errCaptured
}

func (c *captured) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	c.sql, c.args = sql, args
	return capturedRow{}
}

type capturedRow struct{}

func (capturedRow) Scan(...any) error { return errCaptured }

var errCaptured = fmt.Errorf("captured")

// explain is the plan of the captured statement as the list runs it: an unnamed statement planned with its
// parameters (Store.CustomPlans).
func explain(t *testing.T, e *env, c *captured) string {
	t.Helper()
	args := append([]any{pgx.QueryExecModeDescribeExec}, c.args...)
	rows, err := e.d.Pool.Query(t.Context(), "EXPLAIN "+c.sql, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	return strings.Join(lines, "\n")
}

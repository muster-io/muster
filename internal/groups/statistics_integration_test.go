// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package groups_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/matchers"
	"github.com/muster-io/muster/internal/organization"
)

// statRow is one summary row of the statistics tests: when it started, its first acknowledgement after the start and
// by whom (0 for nobody), who resolved it (a User, a Service account, or the system with resolvedBySystem), its
// labels, Severity level and Teams.
type statRow struct {
	id               int64
	route            int64
	labels           map[string]string
	severity         string
	created          time.Time
	ack              time.Duration
	ackBy            int64
	acked            bool
	resolverUser     int64
	resolverAccount  int64
	resolvedBySystem bool
	teams            []int64
}

// statEnv is the integration env with the people and Teams of the statistics tests.
type statEnv struct {
	*env
	route                        int64
	alice, bob, dora, terraform  int64
	payments, search, old, gone  int64
	alicePublic, terraformPublic string
	paymentsPublic, searchPublic string
	next                         int64
	alicesActor, terraformsActor audit.Actor
}

func newStatEnv(t *testing.T, e *env) *statEnv {
	t.Helper()
	se := &statEnv{env: e, route: e.route(t, "stats", "stats", "alertname").ID, next: 1000}
	user := func(public, login, status string) int64 {
		t.Helper()
		var deleted any
		if status == "deleted" {
			deleted = t0
		}
		var id int64
		if err := e.d.Pool.QueryRow(t.Context(), `INSERT INTO users (org_id, public_id, login, name, role, source,
			status, deleted_at, created_at, updated_at)
			VALUES ($1, $2, $3, $3, 'responder', 'local', $4, $5, $6, $6) RETURNING id`, e.orgID, public, login, status,
			deleted, t0).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	se.alice, se.alicePublic = user("SR0000000000A1", "alice", "active"), "SR0000000000A1"
	se.bob = user("SR0000000000B1", "bob", "active")
	se.dora = user("SR0000000000D1", "deleted-user-SR0000000000D1", "deleted")
	se.terraformPublic = "SA0000000000T1"
	if err := e.d.Pool.QueryRow(t.Context(), `INSERT INTO service_accounts (org_id, public_id, name, role, status,
		created_at, updated_at) VALUES ($1, $2, 'terraform', 'responder', 'active', $3, $3) RETURNING id`, e.orgID,
		se.terraformPublic, t0).Scan(&se.terraform); err != nil {
		t.Fatal(err)
	}
	team := func(public, name string, deleted bool) int64 {
		t.Helper()
		var at any
		if deleted {
			at = t0
		}
		var id int64
		if err := e.d.Pool.QueryRow(t.Context(), `INSERT INTO teams (org_id, public_id, name, slug, deleted_at,
			created_at, updated_at) VALUES ($1, $2, $3, lower($3), $4, $5, $5) RETURNING id`, e.orgID, public, name,
			at, t0).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	se.payments, se.paymentsPublic = team("TM0000000000PA", "payments", false), "TM0000000000PA"
	se.search, se.searchPublic = team("TM0000000000SE", "search", false), "TM0000000000SE"
	se.old = team("TM0000000000D1", "old", true)
	se.gone = team("TM0000000000G1", "gone", true)
	if _, err := e.d.Pool.Exec(t.Context(), `INSERT INTO team_users (org_id, team_id, user_id, by_hand, created_at)
		VALUES ($1, $2, $3, true, $4)`, e.orgID, se.payments, se.alice, t0); err != nil {
		t.Fatal(err)
	}
	se.alicesActor = audit.User(se.alice, se.alicePublic)
	se.terraformsActor = audit.ServiceAccount(se.terraform, se.terraformPublic)
	return se
}

// insert writes the summary row r with its Teams.
func (se *statEnv) insert(t *testing.T, r *statRow) {
	t.Helper()
	se.next++
	if r.route == 0 {
		r.route = se.route
	}
	if r.severity == "" {
		r.severity = "warning"
	}
	labels, _ := json.Marshal(r.labels)
	var firstAck, ackBy, resolvedAt, kind, user, account, reason any
	status := "firing"
	if r.acked || r.ackBy != 0 {
		firstAck = r.created.Add(r.ack)
	}
	if r.ackBy != 0 {
		ackBy = r.ackBy
	}
	if r.resolverUser != 0 || r.resolverAccount != 0 || r.resolvedBySystem {
		status, resolvedAt, kind = "resolved", r.created.Add(r.ack+time.Hour), "user"
		switch {
		case r.resolvedBySystem:
			kind, reason = "system", "resolved"
		case r.resolverUser != 0:
			user = r.resolverUser
		default:
			account = r.resolverAccount
		}
	}
	public := fmt.Sprintf("AG%012d", se.next)
	if err := se.d.Pool.QueryRow(t.Context(), `INSERT INTO alert_groups (org_id, public_id, number, route_id,
		group_key_labels, group_key_values, group_key_sha256, title, common_labels, common_annotations,
		integration_ids, status, severity_level, urgent, resolved_at, resolved_by_kind, resolved_by_user_id,
		resolved_by_service_account_id, resolve_reason, first_acknowledged_at, first_acknowledged_by_user_id,
		created_at, last_changed_at)
		VALUES ($1, $2, $3, $4, '{alertname}', '{}', sha256(convert_to($2::text, 'UTF8')), 'Stat', $5, '{}', $6, $7,
		        $8, false, $9, $10, $11, $12, $13, $14, $15, $16, $16)
		RETURNING id`, se.orgID, public, se.next, r.route, labels, []int64{se.intID}, status, r.severity, resolvedAt,
		kind, user, account, reason, firstAck, ackBy, r.created).Scan(&r.id); err != nil {
		t.Fatalf("insert %v: %v", r.labels, err)
	}
	for _, team := range r.teams {
		if _, err := se.d.Pool.Exec(t.Context(), `INSERT INTO alert_group_teams (org_id, alert_group_id, team_id, rule,
			joined_at) VALUES ($1, $2, $3, 'label', $4)`, se.orgID, r.id, team, r.created); err != nil {
			t.Fatal(err)
		}
	}
}

func mustMatchers(t *testing.T, raw ...string) []matchers.Matcher {
	t.Helper()
	var out []matchers.Matcher
	for _, r := range raw {
		m, err := matchers.Parse(r)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

// people is the statistics by people as name/kind/acknowledged first/resolved/median/p95, "-" for a null time and
// "unknown" for the item of nobody.
func people(t *testing.T, e *env, f groups.Filter) []string {
	t.Helper()
	st, err := e.groups.ActorStatistics(t.Context(), f)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, it := range st.Items {
		name := "unknown"
		if it.Actor != nil {
			name = it.Actor.Name + "/" + it.Actor.Kind
			if it.Actor.Deactivated {
				name += "/deactivated"
			}
		}
		m, p := "-", "-"
		if it.TimeToAcknowledge.Median != nil {
			m, p = fmt.Sprint(*it.TimeToAcknowledge.Median), fmt.Sprint(*it.TimeToAcknowledge.P95)
		}
		if it.TimeToAcknowledge.Count != it.AcknowledgedFirst {
			t.Errorf("%s: time to acknowledge counts %d, acknowledged first %d", name, it.TimeToAcknowledge.Count,
				it.AcknowledgedFirst)
		}
		out = append(out, fmt.Sprintf("%s %d %d %s %s", name, it.AcknowledgedFirst, it.Resolved, m, p))
	}
	return out
}

// ackCount is the sum of time_to_acknowledge.count and of alert_group_count over the items of a group_by.
func ackCount(t *testing.T, e *env, groupBy string, f groups.Filter) (int64, int64) {
	t.Helper()
	st, err := e.groups.Statistics(t.Context(), groups.StatisticsRequest{GroupBy: groupBy, Filter: f})
	if err != nil {
		t.Fatal(err)
	}
	var acks, n int64
	for _, it := range st.Items {
		acks, n = acks+it.TimeToAcknowledge.Count, n+it.AlertGroupCount
	}
	return acks, n
}

// TestIntegrationActorStatistics is C-09.FR-28 and C-09.AC-28 on PostgreSQL: Alice acknowledged three Alert Groups
// first, after 2, 4 and 30 minutes, Bob one after 10 minutes, Bob resolved two of Alice's, the Service account
// terraform Bob's and the system Alice's third; a deleted User resolved one nobody acknowledged. By people gives Alice
// 3, 0, a median of 240 and a 95th percentile of 1644 seconds, Bob 1, 2, 600 and 600, the deleted User and terraform
// with 0, 1 and no times, by acknowledged_first_count and then by name; with two Alert Groups acknowledged by nobody
// known, their item comes last with their own times and adds up with By Route (C-09.AC-30); with
// first_acknowledged_by there is no such item.
func TestIntegrationActorStatistics(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		se := newStatEnv(t, e)
		start := t0.Add(-24 * time.Hour)
		rows := []*statRow{
			{ack: 2 * time.Minute, ackBy: se.alice, resolverUser: se.bob},
			{ack: 4 * time.Minute, ackBy: se.alice, resolverUser: se.bob},
			{ack: 30 * time.Minute, ackBy: se.alice, resolvedBySystem: true},
			{ack: 10 * time.Minute, ackBy: se.bob, resolverAccount: se.terraform},
			{resolverUser: se.dora},
			{},
		}
		for i, r := range rows {
			r.created = start.Add(time.Duration(i) * time.Hour)
			r.labels = map[string]string{"cluster": fmt.Sprintf("s%d", i), "scope": "people"}
			se.insert(t, r)
		}
		f := groups.Filter{Matchers: mustMatchers(t, `cluster=~"s.*"`)}
		want := []string{"alice/user 3 0 240 1644", "bob/user 1 2 600 600",
			"deleted-user-SR0000000000D1/user/deactivated 0 1 - -", "terraform/service_account 0 1 - -"}
		if got := people(t, e, f); !slices.Equal(got, want) {
			t.Errorf("by people\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		// The same with an = Matcher, which SQL applies by containment instead of Go, and with no Matcher.
		for _, f := range []groups.Filter{{Matchers: mustMatchers(t, `scope="people"`)}, {}} {
			if got := people(t, e, f); !slices.Equal(got, want) {
				t.Errorf("%v:\n%s", f.Matchers, strings.Join(got, "\n"))
			}
		}
		// Two Alert Groups acknowledged before who made their first acknowledgement was recorded.
		for i, d := range []time.Duration{time.Minute, 3 * time.Minute} {
			se.insert(t, &statRow{created: start.Add(time.Duration(10+i) * time.Hour), ack: d, acked: true,
				labels: map[string]string{"cluster": fmt.Sprintf("u%d", i)}})
		}
		want = append(want, "unknown 2 0 120 174")
		got := people(t, e, groups.Filter{})
		if !slices.Equal(got, want) {
			t.Errorf("with unknown\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		st, err := e.groups.ActorStatistics(t.Context(), groups.Filter{})
		if err != nil {
			t.Fatal(err)
		}
		var sum int64
		for _, it := range st.Items {
			sum += it.AcknowledgedFirst
		}
		if acks, n := ackCount(t, e, groups.ByRoute, groups.Filter{}); acks != sum || sum != 6 || n != 8 {
			t.Errorf("acknowledged first %d, time to acknowledge by route %d of %d", sum, acks, n)
		}
		f = groups.Filter{FirstAcknowledgedBy: se.alicePublic}
		if got := people(t, e, f); !slices.Equal(got, []string{"alice/user 3 0 240 1644", "bob/user 0 2 - -"}) {
			t.Errorf("first acknowledged by alice: %v", got)
		}
		if acks, n := ackCount(t, e, groups.ByRoute, f); acks != 3 || n != 3 {
			t.Errorf("by route, first acknowledged by alice: %d of %d", acks, n)
		}
		f = groups.Filter{FirstAcknowledgedBy: groups.OwnerMe, Me: se.alicesActor}
		if got := people(t, e, f); len(got) != 2 {
			t.Errorf("first acknowledged by me: %v", got)
		}
		// A deleted User can have made a first acknowledgement too; a Service account's id names no User.
		if got := people(t, e, groups.Filter{FirstAcknowledgedBy: "SR0000000000D1"}); len(got) != 0 {
			t.Errorf("first acknowledged by the deleted user: %v", got)
		}
		for _, c := range []struct {
			f    groups.Filter
			code string
		}{
			{groups.Filter{FirstAcknowledgedBy: se.terraformPublic}, groups.CodeUnknownID},
			{groups.Filter{FirstAcknowledgedBy: "SR0000000000ZZ"}, groups.CodeUnknownID},
			{groups.Filter{FirstAcknowledgedBy: groups.OwnerMe, Me: se.terraformsActor}, groups.CodeUnsupported},
			{groups.Filter{Team: groups.TeamMine, Me: se.terraformsActor}, groups.CodeUnsupported},
			{groups.Filter{Team: "TM0000000000ZZ"}, groups.CodeUnknownID},
		} {
			_, err := e.groups.ActorStatistics(t.Context(), c.f)
			_, err2 := e.groups.Statistics(t.Context(), groups.StatisticsRequest{GroupBy: groups.ByTeam, Filter: c.f})
			for _, err := range []error{err, err2} {
				if fe, ok := errors.AsType[*groups.FieldError](err); !ok || fe.Code != c.code {
					t.Errorf("%+v = %v", c.f, err)
				}
			}
		}
	})
}

// TestIntegrationStatisticsPeriod is C-09.FR-15 and C-09.AC-29 on PostgreSQL: both operations select the Alert
// Groups that started in [from, to): one that started on the last day of a 7-day period and that Alice acknowledged
// first a day after it counts for her, with a time to acknowledge of more than a day; one that started the day before
// the period and that Bob acknowledged first inside it counts for nobody.
func TestIntegrationStatisticsPeriod(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		se := newStatEnv(t, e)
		from, to := t0.Add(-7*24*time.Hour), t0
		se.insert(t, &statRow{created: to.Add(-2 * time.Hour), ack: 26 * time.Hour, ackBy: se.alice,
			labels: map[string]string{"cluster": "late"}})
		se.insert(t, &statRow{created: from.Add(-24 * time.Hour), ack: 25 * time.Hour, ackBy: se.bob,
			labels: map[string]string{"cluster": "early"}})
		f := groups.Filter{From: &from, To: &to}
		if got := people(t, e, f); !slices.Equal(got, []string{"alice/user 1 0 93600 93600"}) {
			t.Errorf("by people %v", got)
		}
		if acks, n := ackCount(t, e, groups.ByRoute, f); acks != 1 || n != 1 {
			t.Errorf("by route %d of %d", acks, n)
		}
		st, err := e.groups.ActorStatistics(t.Context(), f)
		if err != nil || !st.From.Equal(from) || !st.To.Equal(to) {
			t.Errorf("period %+v, %v", st, err)
		}
	})
}

// TestIntegrationTeamStatistics is C-09.FR-29, C-09.AC-31 and C-09.AC-32 on PostgreSQL: with an Alert Group of the
// Teams payments and search, one of payments only, one without a Team and one of the deleted Team old, By Team gives
// payments 2, search 1 and old 1, and no item for the deleted Team gone; with team the items are only that Team, or
// the caller's for mine, and By people only who acted on its Alert Groups. Of four Alert Groups of payments, severity
// critical and namespace="payments" count 2 in every group_by and in By people, also through a Matcher =~ that Go
// applies, and 1 with the first acknowledgement of Alice; route with group_by=integration and integration with
// group_by=route are filters.
func TestIntegrationTeamStatistics(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		se := newStatEnv(t, e)
		start := t0.Add(-24 * time.Hour)
		for i, r := range []*statRow{
			{teams: []int64{se.payments, se.search}, ackBy: se.alice, ack: time.Minute},
			{teams: []int64{se.payments}, ackBy: se.bob, ack: 3 * time.Minute},
			{},
			{teams: []int64{se.old}, resolverUser: se.bob},
		} {
			r.created, r.labels = start.Add(time.Duration(i)*time.Minute), map[string]string{"cluster": fmt.Sprintf("t%d", i)}
			se.insert(t, r)
		}
		f := groups.Filter{Matchers: mustMatchers(t, `cluster=~"t."`)}
		teams := func(f groups.Filter) []string {
			t.Helper()
			st, err := e.groups.Statistics(t.Context(), groups.StatisticsRequest{GroupBy: groups.ByTeam, Filter: f})
			if err != nil {
				t.Fatal(err)
			}
			var out []string
			for _, it := range st.Items {
				out = append(out, fmt.Sprintf("%s:%d:%d:%d", it.Subject.Name, it.AlertGroupCount,
					it.TimeToAcknowledge.Count, len(it.PerDay)))
			}
			return out
		}
		if got := teams(f); !slices.Equal(got, []string{"old:1:0:1", "payments:2:2:1", "search:1:1:1"}) {
			t.Errorf("by team %v", got)
		}
		if got := teams(groups.Filter{}); !slices.Equal(got, []string{"old:1:0:1", "payments:2:2:1", "search:1:1:1"}) {
			t.Errorf("by team without matchers %v", got)
		}
		f.Team = se.searchPublic
		if got := teams(f); !slices.Equal(got, []string{"search:1:1:1"}) {
			t.Errorf("search %v", got)
		}
		if got := people(t, e, f); !slices.Equal(got, []string{"alice/user 1 0 60 60"}) {
			t.Errorf("by people of search %v", got)
		}
		f.Team = se.paymentsPublic
		if got := people(t, e, f); !slices.Equal(got, []string{"alice/user 1 0 60 60", "bob/user 1 0 180 180"}) {
			t.Errorf("by people of payments %v", got)
		}
		f.Team, f.Me = groups.TeamMine, se.alicesActor
		if got := teams(f); !slices.Equal(got, []string{"payments:2:2:1"}) {
			t.Errorf("my teams %v", got)
		}
		if got := teams(groups.Filter{Team: groups.TeamMine, Me: audit.User(se.bob, "SR0000000000B1")}); len(got) != 0 {
			t.Errorf("bob's teams %v", got)
		}

		// C-09.AC-32: four Alert Groups of payments.
		critical := organization.SeverityLevel("critical")
		for i, r := range []*statRow{
			{severity: "critical", labels: map[string]string{"namespace": "payments"}, ackBy: se.alice, ack: time.Minute},
			{severity: "critical", labels: map[string]string{"namespace": "payments"}, ackBy: se.bob, ack: time.Minute},
			{severity: "warning", labels: map[string]string{"namespace": "payments"}},
			{severity: "critical", labels: map[string]string{"namespace": "search"}},
		} {
			r.created, r.teams = start.Add(time.Duration(i)*time.Minute+time.Hour), []int64{se.payments}
			r.labels["cluster"] = fmt.Sprintf("f%d", i)
			se.insert(t, r)
		}
		var routePublic string
		if err := e.d.Pool.QueryRow(t.Context(), `SELECT public_id FROM routes WHERE id = $1`, se.route).Scan(
			&routePublic); err != nil {
			t.Fatal(err)
		}
		var integrationPublic string
		if err := e.d.Pool.QueryRow(t.Context(), `SELECT public_id FROM integrations WHERE id = $1`, se.intID).Scan(
			&integrationPublic); err != nil {
			t.Fatal(err)
		}
		for _, m := range [][]string{{`namespace="payments"`}, {`namespace="payments"`, `cluster=~"f."`}} {
			g := groups.Filter{Severities: []organization.SeverityLevel{critical}, Matchers: mustMatchers(t, m...),
				Routes: []string{routePublic}, Integrations: []string{integrationPublic}}
			for _, by := range []string{groups.ByRoute, groups.ByIntegration, groups.ByTeam} {
				if _, n := ackCount(t, e, by, g); n != 2 {
					t.Errorf("%v by %s: %d", m, by, n)
				}
			}
			if got := people(t, e, g); !slices.Equal(got, []string{"alice/user 1 0 60 60", "bob/user 1 0 60 60"}) {
				t.Errorf("%v by people %v", m, got)
			}
			g.FirstAcknowledgedBy, g.Me = groups.OwnerMe, se.alicesActor
			for _, by := range []string{groups.ByRoute, groups.ByIntegration, groups.ByTeam} {
				if _, n := ackCount(t, e, by, g); n != 1 {
					t.Errorf("%v by %s, first acknowledged by me: %d", m, by, n)
				}
			}
			if got := people(t, e, g); !slices.Equal(got, []string{"alice/user 1 0 60 60"}) {
				t.Errorf("%v by people, first acknowledged by me: %v", m, got)
			}
		}
		yes, no := true, false
		if _, n := ackCount(t, e, groups.ByRoute, groups.Filter{Urgent: &yes}); n != 3 {
			t.Errorf("urgent %d", n)
		}
		if _, n := ackCount(t, e, groups.ByRoute, groups.Filter{Reopened: &no}); n != 8 {
			t.Errorf("not reopened %d", n)
		}
		if _, n := ackCount(t, e, groups.ByRoute, groups.Filter{Reopened: &yes}); n != 0 {
			t.Errorf("reopened %d", n)
		}
	})
}

// TestIntegrationStatisticsPlans checks, against 200,000 summary rows with Teams and first acknowledgements, that
// every statistics query starts from the period on alert_groups_started_idx and never scans alert_groups
// sequentially, with every filter set and with none (design/db/schema.md §4.9): no index is added for them.
func TestIntegrationStatisticsPlans(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		se := newStatEnv(t, e)
		ctx := t.Context()
		if _, err := e.d.Pool.Exec(ctx, `INSERT INTO alert_groups (org_id, public_id, number, route_id,
			group_key_labels, group_key_values, group_key_sha256, title, common_labels, common_annotations,
			integration_ids, status, severity_level, urgent, resolved_at, resolved_by_kind, resolved_by_user_id,
			resolve_reason, first_acknowledged_at, first_acknowledged_by_user_id, created_at, last_changed_at)
			SELECT $1, 'AG' || lpad(upper(to_hex(n)), 12, '0'), n, $2, '{alertname}', '{}', sha256(n::text::bytea),
			       'Alert', jsonb_build_object('alertname', 'Alert' || (n % 500), 'namespace', 'ns' || (n % 200)), '{}',
			       ARRAY[$3::bigint], 'resolved', CASE WHEN n % 4 = 0 THEN 'critical' ELSE 'warning' END, false,
			       $4::timestamptz - n * interval '5 minutes' + interval '30 minutes',
			       CASE WHEN n % 3 = 0 THEN 'system' ELSE 'user' END, CASE WHEN n % 3 = 0 THEN NULL ELSE $5::bigint END,
			       CASE WHEN n % 3 = 0 THEN 'resolved' END,
			       CASE WHEN n % 2 = 0 THEN $4::timestamptz - n * interval '5 minutes' + interval '2 minutes' END,
			       CASE WHEN n % 2 = 0 THEN $5::bigint END,
			       $4::timestamptz - n * interval '5 minutes', $4::timestamptz - n * interval '5 minutes'
			FROM generate_series(1, 200000) AS n`, e.orgID, se.route, e.intID, t0, se.alice); err != nil {
			t.Fatal(err)
		}
		if _, err := e.d.Pool.Exec(ctx, `INSERT INTO alert_group_teams (org_id, alert_group_id, team_id, rule,
			joined_at) SELECT org_id, id, $1, 'label', created_at FROM alert_groups WHERE number % 5 = 0`,
			se.payments); err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"alert_groups", "alert_group_teams"} {
			if _, err := e.d.Pool.Exec(ctx, `ANALYZE `+table); err != nil {
				t.Fatal(err)
			}
		}
		capture := &captured{}
		q := dbgen.New(capture)
		none := dbgen.RouteStatisticsParams{TimeZone: "UTC", OrgID: e.orgID, RangeFrom: t0.Add(-groups.ListRange),
			RangeTo: t0, RouteIds: []int64{}, IntegrationIds: []int64{}, Severities: []string{}, TeamIds: []int64{},
			Ids: []int64{}}
		all := none
		all.RouteIds, all.IntegrationIds, all.Severities = []int64{se.route}, []int64{e.intID}, []string{"critical"}
		all.Urgent, all.Reopened = pgtype.Bool{Bool: true, Valid: true}, pgtype.Bool{Bool: false, Valid: true}
		all.TeamSet, all.TeamIds, all.Contains = true, []int64{se.payments}, []byte(`{"namespace":"ns4"}`)
		all.FirstAcknowledgedBy, all.IdsSet, all.Ids = pgtype.Int8{Int64: se.alice, Valid: true}, true, []int64{1, 2, 3}
		actor := func(p dbgen.RouteStatisticsParams) dbgen.ActorStatisticsParams {
			return dbgen.ActorStatisticsParams{OrgID: p.OrgID, RangeFrom: p.RangeFrom, RangeTo: p.RangeTo,
				RouteIds: p.RouteIds, IntegrationIds: p.IntegrationIds, Severities: p.Severities, Urgent: p.Urgent,
				Reopened: p.Reopened, TeamSet: p.TeamSet, TeamIds: p.TeamIds, Contains: p.Contains,
				FirstAcknowledgedBy: p.FirstAcknowledgedBy, IdsSet: p.IdsSet, Ids: p.Ids}
		}
		// Every filter of SQL without the ids of the Matchers Go applies.
		sql := all
		sql.IdsSet, sql.Ids = false, []int64{}
		for name, p := range map[string]dbgen.RouteStatisticsParams{"no filter": none, "every filter": all,
			"every filter but the ids": sql} {
			for _, c := range []struct {
				query string
				run   func()
			}{
				{"route", func() { _, _ = q.RouteStatistics(ctx, p) }},
				{"integration", func() { _, _ = q.IntegrationStatistics(ctx, dbgen.IntegrationStatisticsParams(p)) }},
				{"team", func() { _, _ = q.TeamStatistics(ctx, dbgen.TeamStatisticsParams(p)) }},
				{"people", func() { _, _ = q.ActorStatistics(ctx, actor(p)) }},
				{"labels", func() {
					a := actor(p)
					_, _ = q.ListStatisticsGroupLabels(ctx, dbgen.ListStatisticsGroupLabelsParams{OrgID: a.OrgID,
						RangeFrom: a.RangeFrom, RangeTo: a.RangeTo, RouteIds: a.RouteIds,
						IntegrationIds: a.IntegrationIds, Severities: a.Severities, Urgent: a.Urgent,
						Reopened: a.Reopened, TeamSet: a.TeamSet, TeamIds: a.TeamIds, Contains: a.Contains,
						FirstAcknowledgedBy: a.FirstAcknowledgedBy})
				}},
			} {
				c.run()
				plan := explain(t, e, capture)
				if strings.Contains(plan, "Seq Scan on alert_groups") || !strings.Contains(plan, "alert_groups_") {
					t.Errorf("%s, %s:\n%s", c.query, name, plan)
				}
				if name == "no filter" && !strings.Contains(plan, "alert_groups_started_idx") {
					t.Errorf("%s, %s does not start from the period:\n%s", c.query, name, plan)
				}
				t.Logf("%s, %s:\n%s", c.query, name, plan)
			}
		}
	})
}

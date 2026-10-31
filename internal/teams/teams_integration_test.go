// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package teams_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/destinations"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/oidc"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/routing"
	"github.com/muster-io/muster/internal/teams"
	teamsdb "github.com/muster-io/muster/internal/teams/dbgen"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

var t0 = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

var by = teams.Requester{Actor: audit.System, Transport: audit.TransportSystem}

type env struct {
	d      *db.DB
	orgID  int64
	svc    *teams.Service
	routes *routing.Service
	dests  *destinations.Service
	bob    string
	bobID  int64
}

func setup(t *testing.T, s dbtest.Server) *env {
	t.Helper()
	ctx := t.Context()
	conn := config.Database{URL: logging.Secret(s.NewDatabase(t)), SSLMode: "disable"}
	d, err := db.Open(ctx, conn, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	logger := logging.New(&bytes.Buffer{}, logging.LevelInfo)
	if err := d.Migrate(ctx, logger); err != nil {
		t.Fatal(err)
	}
	if err := organization.Ensure(ctx, organization.NewStore(d.Pool), logger, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool.Exec(ctx, `CREATE TABLE audit_log_p202610 PARTITION OF audit_log
		FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2026-11-01 00:00:00+00')`); err != nil {
		t.Fatal(err)
	}
	org, err := organization.NewStore(d.Pool).GetOrganization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := clock.NewManual(t0)
	w := audit.NewWriter(logger, c)
	rs := routing.NewStore(d.Pool)
	if err := routing.EnsureDefault(ctx, rs, org.ID, t0); err != nil {
		t.Fatal(err)
	}
	e := &env{d: d, orgID: org.ID, bob: "SR0000000000BB",
		svc:    teams.New(teams.Config{OrgID: org.ID, Store: teams.NewStore(d.Pool), Audit: w, Business: c}),
		routes: routing.New(routing.Config{OrgID: org.ID, Store: rs, Audit: w, Business: c}),
		dests:  destinations.New(org.ID, destinations.NewStore(d.Pool))}
	if err := d.Pool.QueryRow(ctx, `INSERT INTO users (org_id, public_id, login, name, role, source, status,
		created_at, updated_at) VALUES ($1, $2, 'bob', 'bob', 'responder', 'local', 'active', $3, $3) RETURNING id`,
		org.ID, e.bob, t0).Scan(&e.bobID); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.d.Pool.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// infoSeries counts the muster_team_info series on /metrics.
func infoSeries(t *testing.T, id string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	metrics.Handler(nil).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	return strings.Count(rec.Body.String(), `muster_team_info{team="`+id+`"`)
}

// TestIntegrationTeams is C-25.FR-1, FR-2, FR-9, FR-10 and FR-11 against PostgreSQL: names unique ignoring case and
// slugs unique among the Teams that are not deleted, own Working hours stored and the Organization's followed, a Route
// and a Destination owned and filtered by the Team and by "My Teams", and the deletion, which keeps the row marked
// deleted, removes the people, leaves the owned objects without an owner with a new version and frees the slug.
func TestIntegrationTeams(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		e := setup(t, s)
		pay, err := e.svc.Create(ctx, by, teams.Input{Name: "Payments", Slug: "payments"})
		if err != nil {
			t.Fatal(err)
		}
		if pay.WorkingHours.Start != "09:00" || pay.WorkingHours.End != "18:00" || len(pay.WorkingHours.Days) != 5 ||
			pay.WorkingHours.TimeZone != "UTC" {
			t.Errorf("the Organization's hours %+v", pay.WorkingHours)
		}
		if infoSeries(t, pay.PublicID) != 1 {
			t.Error("no muster_team_info series")
		}
		if _, err := e.svc.Create(ctx, by, teams.Input{Name: "PAYMENTS", Slug: "pay"}); !errors.Is(err,
			teams.ErrNameTaken) {
			t.Errorf("name taken = %v", err)
		}
		if _, err := e.svc.Create(ctx, by, teams.Input{Name: "Pay", Slug: "payments"}); !errors.Is(err,
			teams.ErrSlugTaken) {
			t.Errorf("slug taken = %v", err)
		}
		hours := teams.WorkingHours{Days: []string{"monday", "friday"}, Start: "11:00", End: "19:00",
			TimeZone: "Europe/Berlin"}
		search, err := e.svc.Create(ctx, by, teams.Input{Name: "Search", Slug: "search", OwnWorkingHours: true,
			WorkingHours: &hours})
		if err != nil || search.Own == nil || search.WorkingHours.TimeZone != "Europe/Berlin" {
			t.Fatalf("own hours = %+v, %v", search, err)
		}
		if _, err := e.d.Pool.Exec(ctx, `UPDATE organizations SET workday_start = '08:00'`); err != nil {
			t.Fatal(err)
		}
		if got, _ := e.svc.Get(ctx, pay.PublicID); got.WorkingHours.Start != "08:00" {
			t.Errorf("the start follows the Organization's: %+v", got.WorkingHours)
		}
		if got, _ := e.svc.Get(ctx, search.PublicID); got.WorkingHours.Start != "11:00" {
			t.Errorf("own hours stay: %+v", got.WorkingHours)
		}
		page, err := e.svc.List(ctx, teams.ListFilter{Query: "SEA", Limit: 10})
		if err != nil || len(page.Teams) != 1 || page.Teams[0].PublicID != search.PublicID {
			t.Errorf("q = %+v, %v", page, err)
		}

		if _, added, err := e.svc.AddUser(ctx, by, pay.PublicID, e.bob); err != nil || !added {
			t.Fatalf("add = %v, %v", added, err)
		}
		if _, _, err := e.svc.AddUser(ctx, by, search.PublicID, e.bob); err != nil {
			t.Fatal(err)
		}
		if _, added, err := e.svc.AddUser(ctx, by, pay.PublicID, e.bob); err != nil || added {
			t.Errorf("again = %v, %v", added, err)
		}
		// A person in a Team through the identity provider is added by hand too.
		if _, err := e.d.Pool.Exec(ctx, `UPDATE team_users SET by_hand = false, by_idp = true WHERE user_id = $1
			AND team_id = (SELECT id FROM teams WHERE public_id = $2)`, e.bobID, search.PublicID); err != nil {
			t.Fatal(err)
		}
		if m, added, err := e.svc.AddUser(ctx, by, search.PublicID, e.bob); err != nil || !added ||
			strings.Join(m.Sources, ",") != "by_hand,idp" {
			t.Errorf("by hand too = %+v, %v, %v", m, added, err)
		}
		// A deleted User is neither counted nor listed, should a row of theirs be left.
		if _, err := e.d.Pool.Exec(ctx, `WITH u AS (INSERT INTO users (org_id, public_id, login, name, role, source,
			status, deleted_at, created_at, updated_at) VALUES ($1, 'SR0000000000DD', 'deleted-user-SR0000000000DD',
			'deleted-user-SR0000000000DD', 'viewer', 'local', 'deleted', $2, $2, $2) RETURNING id)
			INSERT INTO team_users (org_id, team_id, user_id, by_hand, created_at)
			SELECT $1, t.id, u.id, true, $2 FROM teams t, u WHERE t.public_id = $3`, e.orgID, t0,
			pay.PublicID); err != nil {
			t.Fatal(err)
		}
		if got, err := e.svc.Get(ctx, pay.PublicID); err != nil || got.UserCount != 1 {
			t.Errorf("user_count with a deleted User = %+v, %v", got.UserCount, err)
		}
		people, err := e.svc.Users(ctx, pay.PublicID)
		if err != nil || len(people) != 1 || people[0].Role != "responder" || people[0].Sources[0] != "by_hand" {
			t.Errorf("people = %+v, %v", people, err)
		}

		// A Route and a Destination owned by Payments, and one Destination owned by Search.
		in := routing.Input{Name: "pay", GroupKey: []string{"alertname"}, Policy: routing.Profiles()[0].Policy,
			OwningTeamID: &pay.PublicID}
		rt, err := e.routes.Create(ctx, routing.Requester{Actor: audit.System, Transport: audit.TransportSystem}, in)
		if err != nil || rt.OwningTeam == nil || rt.OwningTeam.Slug != "payments" {
			t.Fatalf("route = %+v, %v", rt.OwningTeam, err)
		}
		for i, owner := range []string{pay.PublicID, search.PublicID} {
			if _, err := e.d.Pool.Exec(ctx, `INSERT INTO destinations (org_id, public_id, type, name, webhook_mode,
				webhook_events_config, proxy, signing_secret_ciphertext, signing_secret_key_id, mentions,
				limiter_limit, limiter_per_seconds, health, created_at, updated_at, owning_team_id)
				SELECT $1, $2, 'webhook', $3, 'events', '{"url":"https://example.org","headers":[]}', '{}', '\x00',
				'k', '{}', 1, 1, 'healthy', $4, $4, id FROM teams WHERE public_id = $5`, e.orgID,
				[]string{"DS0000000000PA", "DS0000000000SE"}[i], "hook"+owner, t0, owner); err != nil {
				t.Fatal(err)
			}
		}
		owned, err := e.routes.ListOwned(ctx, routing.OwnerFilter{UserID: e.bobID})
		if err != nil || len(owned.Routes) != 1 || owned.Routes[0].PublicID != rt.PublicID {
			t.Errorf("my teams' routes = %+v, %v", owned.Routes, err)
		}
		dp, err := e.dests.List(ctx, destinations.ListFilter{Team: &pay.PublicID, Limit: 10})
		if err != nil || len(dp.Destinations) != 1 || dp.Destinations[0].OwningTeam == nil ||
			dp.Destinations[0].OwningTeam.Name != "Payments" {
			t.Errorf("payments' destinations = %+v, %v", dp.Destinations, err)
		}
		if dp, err := e.dests.List(ctx, destinations.ListFilter{TeamUser: &e.bobID, Limit: 10}); err != nil ||
			len(dp.Destinations) != 2 {
			t.Errorf("my teams' destinations = %+v, %v", dp.Destinations, err)
		}

		before := e.count(t, `SELECT version FROM routes WHERE public_id = $1`, rt.PublicID)
		destBefore := e.count(t, `SELECT version FROM destinations WHERE public_id = 'DS0000000000PA'`)
		if err := e.svc.Delete(ctx, by, pay.PublicID, &pay.Version); err != nil {
			t.Fatal(err)
		}
		if got := e.count(t, `SELECT count(*) FROM teams WHERE public_id = $1 AND deleted_at IS NOT NULL
			AND NOT own_working_hours`, pay.PublicID); got != 1 {
			t.Error("the row is not kept, marked deleted")
		}
		if got := e.count(t, `SELECT count(*) FROM team_users tu JOIN teams t ON t.id = tu.team_id
			WHERE t.public_id = $1`, pay.PublicID); got != 0 {
			t.Errorf("%d people are left", got)
		}
		if got := e.count(t, `SELECT version FROM routes WHERE public_id = $1 AND owning_team_id IS NULL`,
			rt.PublicID); got != before+1 {
			t.Errorf("route version %d, want %d without an owner", got, before+1)
		}
		if got := e.count(t, `SELECT version FROM destinations WHERE public_id = 'DS0000000000PA'
			AND owning_team_id IS NULL`); got != destBefore+1 {
			t.Errorf("destination version %d, want %d without an owner", got, destBefore+1)
		}
		if got := e.count(t, `SELECT count(*) FROM destinations WHERE public_id = 'DS0000000000SE'
			AND owning_team_id IS NOT NULL`); got != 1 {
			t.Error("another Team's Destination lost its owner")
		}
		for action, actor := range map[string]string{teams.ActionDeleted: "system", "route.updated": "system",
			"destination.updated": "system"} {
			if got := e.count(t, `SELECT count(*) FROM audit_log WHERE action = $1 AND actor_kind = $2`, action,
				actor); got < 1 {
				t.Errorf("no %s by %s", action, actor)
			}
		}
		if infoSeries(t, pay.PublicID) != 0 {
			t.Error("the deleted Team keeps its muster_team_info series")
		}
		again, err := e.svc.Create(ctx, by, teams.Input{Name: "Payments", Slug: "payments"})
		if err != nil || again.PublicID == pay.PublicID {
			t.Errorf("the slug is free = %+v, %v", again, err)
		}
		if err := e.svc.RefreshInfo(ctx); err != nil || infoSeries(t, again.PublicID) != 1 ||
			infoSeries(t, pay.PublicID) != 0 {
			t.Errorf("refresh = %v", err)
		}

		// The CHECKs keep what the package refuses first.
		for _, sql := range []string{
			`UPDATE teams SET slug = 'Payments' WHERE public_id = '` + again.PublicID + `'`,
			`UPDATE teams SET working_end = '10:00' WHERE public_id = '` + search.PublicID + `'`,
			`UPDATE teams SET own_working_hours = false WHERE public_id = '` + search.PublicID + `'`,
		} {
			if _, err := e.d.Pool.Exec(ctx, sql); err == nil {
				t.Errorf("%s was stored", sql)
			}
		}
	})
}

// TestIntegrationTeamsOfAlertGroups is C-25.FR-1, FR-5, FR-10, FR-11 and C-25.AC-9 against PostgreSQL: Team matchers
// are written and read back in their order and replaced as a whole; every change of a Team raises teams_version; the
// classification reads the Teams that are not deleted with their compiled matchers; after a Team is deleted, its
// matchers go, an Alert Group that had it among its Teams still lists it by name with deleted true, and its slug no
// longer gives Alerts that Team.
func TestIntegrationTeamsOfAlertGroups(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		version := func(t *testing.T) int64 {
			t.Helper()
			return int64(e.count(t, `SELECT teams_version FROM organizations WHERE id = $1`, e.orgID))
		}
		payments, err := e.svc.Create(ctx, by, teams.Input{Name: "Payments", Slug: "payments"})
		if err != nil {
			t.Fatal(err)
		}
		ms := []teams.Matcher{{Label: "namespace", Op: "=~", Value: "checkout|billing"},
			{Label: "pod", Op: "=~", Value: "checkout-.*"}}
		checkout, err := e.svc.Create(ctx, by, teams.Input{Name: "Checkout", Slug: "checkout-team", Matchers: &ms})
		if err != nil {
			t.Fatal(err)
		}
		if got, err := e.svc.Get(ctx, checkout.PublicID); err != nil || len(got.Matchers) != 2 ||
			got.Matchers[0] != ms[0] || got.Matchers[1] != ms[1] || version(t) != 2 {
			t.Fatalf("matchers %+v, %v, teams_version %d", got.Matchers, err, version(t))
		}
		one := ms[1:]
		if _, err := e.svc.Update(ctx, by, checkout.PublicID, nil, teams.Input{Name: "Checkout",
			Slug: "checkout-team", Matchers: &one}); err != nil || version(t) != 3 ||
			e.count(t, `SELECT count(*) FROM team_matchers WHERE org_id = $1 AND position = 0 AND label = 'pod'`,
				e.orgID) != 1 {
			t.Errorf("replaced: %v, teams_version %d", err, version(t))
		}
		classes, err := teams.NewClassifier(e.orgID).At(ctx, teamsdb.New(e.d.Pool),
			teams.Stamp{Label: "team", Version: version(t)})
		if err != nil {
			t.Fatal(err)
		}
		got := classes.Classify(map[string]string{"team": "payments", "pod": "checkout-api-1"})
		if len(got.Teams) != 2 || got.Teams[0] != (teams.Membership{TeamID: payments.ID, Rule: teams.RuleLabel}) ||
			got.Teams[1] != (teams.Membership{TeamID: checkout.ID, Rule: teams.RuleMatcher,
				Matcher: `pod=~"checkout-.*"`}) || got.UnknownValue != "" {
			t.Errorf("classified %+v", got)
		}

		var route int64
		if err := e.d.Pool.QueryRow(ctx, `SELECT id FROM routes WHERE org_id = $1 AND is_default`,
			e.orgID).Scan(&route); err != nil {
			t.Fatal(err)
		}
		var group int64
		if err := e.d.Pool.QueryRow(ctx, `INSERT INTO alert_groups (org_id, public_id, number, route_id,
			group_key_labels, group_key_values, group_key_sha256, title, common_labels, common_annotations,
			integration_ids, status, severity_level, urgent, created_at, last_changed_at)
			VALUES ($1, 'AG0000000000T1', 1, $2, '{alertname}', '{}', sha256('t1'), 't', '{}', '{}', '{}', 'firing',
				'warning', false, $3, $3) RETURNING id`, e.orgID, route, t0).Scan(&group); err != nil {
			t.Fatal(err)
		}
		if _, err := e.d.Pool.Exec(ctx, `INSERT INTO alert_group_teams (org_id, alert_group_id, team_id, rule,
			matcher, joined_at) VALUES ($1, $2, $3, 'label', NULL, $5), ($1, $2, $4, 'matcher', 'pod=~"x"', $5)`,
			e.orgID, group, payments.ID, checkout.ID, t0); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.Delete(ctx, by, payments.PublicID, nil); err != nil || version(t) != 4 {
			t.Fatalf("delete: %v, teams_version %d", err, version(t))
		}
		if err := e.svc.Delete(ctx, by, checkout.PublicID, nil); err != nil ||
			e.count(t, `SELECT count(*) FROM team_matchers WHERE org_id = $1`, e.orgID) != 0 {
			t.Fatalf("delete of checkout: %v", err)
		}
		gs := groups.New(groups.Config{OrgID: e.orgID, Store: groups.NewStore(e.d.Pool),
			Business: clock.NewManual(t0)})
		v, err := gs.Get(ctx, "AG0000000000T1")
		if err != nil {
			t.Fatal(err)
		}
		if len(v.Teams) != 2 || v.Teams[0].Team.Name != "Payments" || !v.Teams[0].Team.Deleted ||
			v.Teams[0].Team.Slug != "payments" || v.Teams[0].Team.PublicID != payments.PublicID ||
			v.Teams[1].Team.Name != "Checkout" || *v.Teams[1].Matcher != `pod=~"x"` {
			t.Errorf("teams of the alert group %+v", v.Teams)
		}
		classes, err = teams.NewClassifier(e.orgID).At(ctx, teamsdb.New(e.d.Pool),
			teams.Stamp{Label: "team", Version: version(t)})
		if err != nil {
			t.Fatal(err)
		}
		if got := classes.Classify(map[string]string{"team": "payments", "pod": "checkout-api-1"}); len(got.Teams) != 0 ||
			got.UnknownValue != "payments" {
			t.Errorf("after the deletion %+v", got)
		}
	})
}

// sync follows the entries of the Teams for the User userID with claims, in a transaction of its own, as a sign-in
// does after it locked the user.
func (e *env) sync(t *testing.T, userID int64, claims oidc.Claims) {
	t.Helper()
	if err := pgx.BeginFunc(t.Context(), e.d.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT 1 FROM users WHERE id = $1 FOR NO KEY UPDATE`, userID); err != nil {
			return err
		}
		return e.svc.SyncMembership(t.Context(), tx, userID, "groups", claims)
	}); err != nil {
		t.Fatal(err)
	}
}

// TestIntegrationIdpMembership is C-25.FR-4 and C-25.AC-3 against PostgreSQL: the entries of a Team are stored in their
// order and replaced as a whole; teams/payments/* takes in teams/payments and teams/payments/oncall, not
// teams/paymentsx; a person added by hand who matches has both sources, and leaves only through the identity provider
// when the entry no longer matches; removing by hand keeps the source idp; every change is in the Audit log by the
// system; the CHECK refuses a row with no source; deleting the Team removes its entries.
func TestIntegrationIdpMembership(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		e := setup(t, s)
		ids := map[string]int64{"bob": e.bobID}
		for login, pid := range map[string]string{"olga": "SR00000000000A", "petr": "SR00000000000P",
			"rita": "SR00000000000R"} {
			var id int64
			if err := e.d.Pool.QueryRow(ctx, `INSERT INTO users (org_id, public_id, login, name, role, source, status,
				created_at, updated_at) VALUES ($1, $4, $2, $2, 'viewer', 'oidc', 'active', $3, $3) RETURNING id`,
				e.orgID, login, t0, pid).Scan(&id); err != nil {
				t.Fatal(err)
			}
			ids[login] = id
		}
		maintainer := "https://gitlab.org/claims/groups/maintainer"
		pay, err := e.svc.Create(ctx, by, teams.Input{Name: "Payments", Slug: "payments",
			IdpEntries: &[]teams.IdpEntry{{Group: "teams/payments/*"}, {Claim: maintainer, Group: "teams/billing"}}})
		if err != nil {
			t.Fatal(err)
		}
		if got, err := e.svc.EntryClaims(ctx); err != nil || strings.Join(got, ",") != ","+maintainer {
			t.Errorf("EntryClaims = %q, %v", got, err)
		}
		if got, err := e.svc.Get(ctx, pay.PublicID); err != nil || len(got.IdpEntries) != 2 ||
			got.IdpEntries[1] != (teams.IdpEntry{Claim: maintainer, Group: "teams/billing"}) {
			t.Errorf("read entries = %+v, %v", got.IdpEntries, err)
		}
		if _, _, err := e.svc.AddUser(ctx, by, pay.PublicID, e.bob); err != nil {
			t.Fatal(err)
		}
		e.sync(t, ids["olga"], oidc.Claims{"groups": {"teams/payments"}, maintainer: {}})
		e.sync(t, ids["petr"], oidc.Claims{"groups": {"teams/payments/oncall"}, maintainer: {}})
		e.sync(t, ids["rita"], oidc.Claims{"groups": {"teams/paymentsx"}, maintainer: {}})
		e.sync(t, ids["bob"], oidc.Claims{"groups": {}, maintainer: {"teams/billing"}})
		members := func(t *testing.T) string {
			t.Helper()
			people, err := e.svc.Users(ctx, pay.PublicID)
			if err != nil {
				t.Fatal(err)
			}
			var out []string
			for _, p := range people {
				out = append(out, p.Login+":"+strings.Join(p.Sources, "+"))
			}
			return strings.Join(out, " ")
		}
		if got := members(t); got != "bob:by_hand+idp olga:idp petr:idp" {
			t.Errorf("Payments = %s", got)
		}
		e.sync(t, ids["petr"], oidc.Claims{"groups": {"teams/other"}, maintainer: {}})
		e.sync(t, ids["bob"], oidc.Claims{"groups": {}, maintainer: {}})
		if got := members(t); got != "bob:by_hand olga:idp" {
			t.Errorf("after the groups changed = %s", got)
		}
		if err := e.svc.RemoveUser(ctx, by, pay.PublicID, "SR00000000000A"); !errors.Is(err, teams.ErrNotInTeam) {
			t.Errorf("removing by hand olga = %v", err)
		}
		if _, _, err := e.svc.AddUser(ctx, by, pay.PublicID, "SR00000000000A"); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.RemoveUser(ctx, by, pay.PublicID, "SR00000000000A"); err != nil {
			t.Fatal(err)
		}
		if got := members(t); got != "bob:by_hand olga:idp" {
			t.Errorf("after removing olga by hand = %s", got)
		}
		if got := e.count(t, `SELECT count(*) FROM audit_log WHERE actor_kind = 'system'
			AND action IN ('team.user_added', 'team.user_removed') AND details ->> 'source' = 'idp'
			AND details -> 'entry' ? 'group'`); got != 5 {
			t.Errorf("%d entries by the identity provider, want 5", got)
		}
		_, err = e.d.Pool.Exec(ctx, `UPDATE team_users SET by_idp = false WHERE user_id = $1`, ids["olga"])
		if pe, ok := errors.AsType[*pgconn.PgError](err); !ok || pe.ConstraintName != "team_users_source_check" {
			t.Errorf("a row with no source = %v", err)
		}
		if err := e.svc.Delete(ctx, by, pay.PublicID, nil); err != nil {
			t.Fatal(err)
		}
		if got := e.count(t, `SELECT count(*) FROM team_idp_entries`); got != 0 {
			t.Errorf("%d entries of a deleted Team are left", got)
		}
		if got, err := e.svc.EntryClaims(ctx); err != nil || len(got) != 0 {
			t.Errorf("EntryClaims after the deletion = %q, %v", got, err)
		}
	})
}

// TestIntegrationRemoveByHandWaitsForSignIn is the race of a removal by hand with a sign-in that changes the same row:
// a sign-in that takes the source idp of bob, who has both sources, holds his row while the Admin removes him by hand;
// the removal waits for it and decides from what it committed, so bob leaves the Team and team.user_removed records
// by_hand before and nothing after, instead of the removal being lost.
func TestIntegrationRemoveByHandWaitsForSignIn(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		e := setup(t, s)
		pay, err := e.svc.Create(ctx, by, teams.Input{Name: "Payments", Slug: "payments",
			IdpEntries: &[]teams.IdpEntry{{Group: "teams/payments"}}})
		if err != nil {
			t.Fatal(err)
		}
		e.sync(t, e.bobID, oidc.Claims{"groups": {"teams/payments"}})
		if _, _, err := e.svc.AddUser(ctx, by, pay.PublicID, e.bob); err != nil {
			t.Fatal(err)
		}
		tx, err := e.d.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
		if err := e.svc.SyncMembership(ctx, tx, e.bobID, "groups", oidc.Claims{"groups": {}}); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- e.svc.RemoveUser(ctx, by, pay.PublicID, e.bob) }()
		deadline := time.Now().Add(10 * time.Second)
		for e.count(t, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()
			AND wait_event_type = 'Lock'`) == 0 {
			if time.Now().After(deadline) {
				t.Fatal("the removal did not wait for the sign-in")
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatalf("the removal = %v", err)
		}
		if n := e.count(t, `SELECT count(*) FROM team_users WHERE user_id = $1`, e.bobID); n != 0 {
			t.Errorf("bob is still in the Team (%d rows)", n)
		}
		var diff string
		if err := e.d.Pool.QueryRow(ctx, `SELECT diff::text FROM audit_log WHERE action = 'team.user_removed'
			AND actor_kind = 'system' AND details ->> 'source' IS NULL ORDER BY id DESC LIMIT 1`).Scan(&diff); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(diff, `"before": {"sources": ["by_hand"]}`) || strings.Contains(diff, `"after": {`) {
			t.Errorf("the removal by hand recorded %s", diff)
		}
	})
}

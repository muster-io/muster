// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package escalation_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	"github.com/muster-io/muster/internal/escalation"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/routing"
	"github.com/muster-io/muster/internal/schedules"
	"github.com/muster-io/muster/internal/teams"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

var t0 = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

var (
	by        = escalation.Requester{Actor: audit.System, Transport: audit.TransportSystem}
	teamAdmin = teams.Requester{Actor: audit.System, Transport: audit.TransportSystem}
	schAdmin  = schedules.Requester{Actor: audit.System, Transport: audit.TransportSystem, Admin: true}
)

func ptr[T any](v T) *T { return &v }

type env struct {
	d         *db.DB
	orgID     int64
	svc       *escalation.Service
	teams     *teams.Service
	schedules *schedules.Service
	payments  teams.Team
	main      schedules.Schedule
	people    map[string]string
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.d.Pool.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// setup has the Team payments with petr and its Schedule Main, the Viewer vic and the disabled dora, and a
// Notification gateway.
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
	if _, err := d.Pool.Exec(ctx, `CREATE TABLE audit_log_p202611 PARTITION OF audit_log
		FOR VALUES FROM ('2026-11-01 00:00:00+00') TO ('2026-12-01 00:00:00+00')`); err != nil {
		t.Fatal(err)
	}
	org, err := organization.NewStore(d.Pool).GetOrganization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := clock.NewManual(t0)
	w := audit.NewWriter(logger, c)
	e := &env{d: d, orgID: org.ID, people: map[string]string{},
		svc:       escalation.New(escalation.Config{OrgID: org.ID, Store: escalation.NewStore(d.Pool), Audit: w, Business: c}),
		teams:     teams.New(teams.Config{OrgID: org.ID, Store: teams.NewStore(d.Pool), Audit: w, Business: c}),
		schedules: schedules.New(schedules.Config{OrgID: org.ID, Store: schedules.NewStore(d.Pool), Audit: w, Business: c})}
	if e.payments, err = e.teams.Create(ctx, teamAdmin, teams.Input{Name: "payments", Slug: "payments"}); err != nil {
		t.Fatal(err)
	}
	for i, p := range []struct{ login, role, status string }{{"petr", "responder", "active"},
		{"vic", "viewer", "active"}, {"dora", "responder", "disabled"}} {
		id := "SR00000000000" + string(rune('A'+i))
		if _, err := d.Pool.Exec(ctx, `INSERT INTO users (org_id, public_id, login, name, role, source, status,
			created_at, updated_at) VALUES ($1, $2, $3, $3, $4, 'local', $5, $6, $6)`, org.ID, id, p.login, p.role,
			p.status, t0); err != nil {
			t.Fatal(err)
		}
		e.people[p.login] = id
	}
	if _, _, err := e.teams.AddUser(ctx, teamAdmin, e.payments.PublicID, e.people["petr"]); err != nil {
		t.Fatal(err)
	}
	e.main, err = e.schedules.Create(ctx, schAdmin, schedules.Input{TeamID: e.payments.PublicID, Name: "Main",
		RotationLayers: []schedules.RotationLayerInput{{Name: "Week", UserIDs: []string{e.people["petr"]},
			ShiftLength: schedules.ShiftWeek, HandoverTime: "10:00", HandoverDay: "monday",
			StartDate: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool.Exec(ctx, `INSERT INTO connections (org_id, public_id, type, name, bridge_base_url,
		bridge_event_delivery, bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit,
		limiter_per_seconds, bridge_info, created_at, updated_at)
		VALUES ($1, 'CN0000000000G1', 'bridge', 'gateway', 'http://gw', 'poll', '\x00', 'k1', $2, 1, 1,
			'{"roles": ["gateway"]}', $2, $2)`, org.ID, t0); err != nil {
		t.Fatal(err)
	}
	return e
}

// TestIntegrationChains is C-32.FR-1, FR-2, FR-3, FR-19, C-32.AC-15 and C-25.FR-10 on PostgreSQL: the payments chain
// with its owning Team, its steps and a call through the only gateway, numbered from 1 and read with what they name;
// name_taken ignoring case; a Viewer and a disabled person refused with not_pageable; the CHECK of each kind's fields;
// a Team that names the chain keeps it from being deleted (chain_in_use), and the owning Team from being deleted
// (team_in_use); a Schedule the steps name refuses its deletion; the Audit log; muster_escalation_chain_info; up to
// the deletion, which takes its steps.
func TestIntegrationChains(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		steps := []escalation.StepInput{
			{Kind: escalation.KindPageOnCall, ScheduleID: ptr(e.main.PublicID), Importance: ptr("normal")},
			{Kind: escalation.KindWait, WaitSeconds: ptr(600)},
			{Kind: escalation.KindPagePeople, UserIDs: &[]string{e.people["petr"]}, Importance: ptr("important"),
				Condition: ptr("critical")},
			{Kind: escalation.KindPageTeam, WorkingHoursOnly: ptr(true)},
			{Kind: escalation.KindCall, CallTarget: ptr("people"), UserIDs: &[]string{e.people["petr"]}},
			{Kind: escalation.KindRepeat, RepeatFrom: ptr(3), RepeatTimes: ptr(2)},
		}
		c, err := e.svc.Create(ctx, by, escalation.Input{Name: "payments", OwningTeamID: ptr(e.payments.PublicID),
			Steps: steps})
		if err != nil {
			t.Fatal(err)
		}
		if len(c.Steps) != 6 || c.Steps[0].Schedule.Name != "Main" || c.Steps[2].Users[0].Login != "petr" ||
			c.Steps[4].Gateway == nil || c.Steps[4].Gateway.PublicID != "CN0000000000G1" || c.Steps[5].Number != 6 ||
			c.OwningTeam.Slug != "payments" {
			t.Fatalf("created %+v", c)
		}
		if _, err := e.svc.Create(ctx, by, escalation.Input{Name: "PAYMENTS"}); !errors.Is(err, escalation.ErrNameTaken) {
			t.Errorf("a taken name = %v", err)
		}
		for login, reason := range map[string]string{"vic": "viewer", "dora": "disabled"} {
			_, err := e.svc.Create(ctx, by, escalation.Input{Name: "x", Steps: []escalation.StepInput{
				{Kind: escalation.KindPagePeople, UserIDs: &[]string{e.people[login]}}}})
			if f, ok := errors.AsType[*escalation.FieldError](err); !ok || f.Code != "not_pageable" || f.Reason != reason ||
				f.Pointer != "/steps/0/user_ids/0" {
				t.Errorf("%s: %v", login, err)
			}
		}
		_, err = e.d.Pool.Exec(ctx, `INSERT INTO escalation_steps (org_id, chain_id, position, kind, wait_seconds,
			working_hours_only) VALUES ($1, $2, 7, 'wait', 60, true)`, e.orgID, c.ID)
		if pe, ok := errors.AsType[*pgconn.PgError](err); !ok || pe.ConstraintName != "escalation_steps_fields_check" {
			t.Errorf("a wait limited to working hours: %v", err)
		}
		_, err = e.d.Pool.Exec(ctx, `INSERT INTO escalation_steps (org_id, chain_id, position, kind, wait_seconds)
			VALUES ($1, $2, 7, 'wait', 90)`, e.orgID, c.ID)
		if pe, ok := errors.AsType[*pgconn.PgError](err); !ok || pe.ConstraintName != "escalation_steps_wait_seconds_check" {
			t.Errorf("a wait of 90 s: %v", err)
		}

		// The Team names the chain: the chain reads the Team as its referrer and cannot be deleted.
		if _, err := e.teams.Update(ctx, teamAdmin, e.payments.PublicID, nil, teams.Input{Name: "payments",
			Slug: "payments", ChainEntries: &[]teams.ChainEntry{{ChainID: c.PublicID, Condition: "critical"}}}); err != nil {
			t.Fatal(err)
		}
		team, err := e.teams.Get(ctx, e.payments.PublicID)
		if err != nil || len(team.Chains) != 1 || team.Chains[0].Name != "payments" ||
			team.ChainEntries[0].Condition != "critical" {
			t.Fatalf("the team's chains %+v, %v", team.Chains, err)
		}
		got, err := e.svc.Get(ctx, c.PublicID)
		if err != nil || len(got.Referrers.Teams) != 1 || got.Referrers.Teams[0].Condition != "critical" {
			t.Fatalf("referrers %+v, %v", got.Referrers, err)
		}
		err = e.svc.Delete(ctx, by, c.PublicID, nil)
		if use, ok := errors.AsType[*escalation.InUseError](err); !ok || len(use.Holders) != 1 ||
			use.Holders[0].Type != escalation.HolderTeam {
			t.Errorf("deleting a named chain = %v", err)
		}
		err = e.teams.Delete(ctx, teamAdmin, e.payments.PublicID, nil)
		if use, ok := errors.AsType[*teams.InUseError](err); !ok || !strings.Contains(
			func() string {
				var out []string
				for _, h := range use.Holders {
					out = append(out, h.Type)
				}
				return strings.Join(out, ",")
			}(), teams.HolderChain) {
			t.Errorf("deleting the owning team = %v", err)
		}
		err = e.schedules.Delete(ctx, schAdmin, e.main.PublicID, nil)
		if use, ok := errors.AsType[*schedules.InUseError](err); !ok || len(use.Referrers) != 1 ||
			use.Referrers[0].Name != "payments" {
			t.Errorf("deleting a schedule a step names = %v", err)
		}

		// The Default route names the chain too, which a read of the Route gives.
		if err := routing.EnsureDefault(ctx, routing.NewStore(e.d.Pool), e.orgID, t0); err != nil {
			t.Fatal(err)
		}
		if _, err := e.d.Pool.Exec(ctx, `INSERT INTO route_escalation_chains (org_id, route_id, chain_id, added_at)
			SELECT org_id, id, $1, $2 FROM routes WHERE is_default`, c.ID, t0); err != nil {
			t.Fatal(err)
		}
		if got, err = e.svc.Get(ctx, c.PublicID); err != nil || len(got.Referrers.Routes) != 1 {
			t.Errorf("route referrers %+v, %v", got.Referrers, err)
		}

		// A rename keeps the people and the gateway; the update and the creation are in the Audit log.
		if got, err = e.svc.Update(ctx, by, c.PublicID, ptr(c.Version), escalation.Input{Name: "payments-main",
			Steps: steps}); err != nil || got.Version != 2 || got.OwningTeam == nil {
			t.Fatalf("renamed %+v, %v", got, err)
		}
		if _, err := e.svc.Update(ctx, by, c.PublicID, ptr(c.Version), escalation.Input{Name: "x"}); !errors.Is(err,
			escalation.ErrVersionMismatch) {
			t.Errorf("stale = %v", err)
		}
		for _, action := range []string{escalation.ActionCreated, escalation.ActionUpdated} {
			if n := e.count(t, `SELECT count(*) FROM audit_log WHERE action = $1 AND resource_public_id = $2`, action,
				c.PublicID); n != 1 {
				t.Errorf("%d %s entries", n, action)
			}
		}
		hours, err := e.svc.WorkingHoursOf(ctx, got)
		if err != nil || hours.Start != "09:00" {
			t.Errorf("working hours of the chain %+v, %v", hours, err)
		}
		page, err := e.svc.List(ctx, escalation.ListFilter{Team: e.payments.PublicID, Limit: 50})
		if err != nil || len(page.Chains) != 1 || page.Chains[0].Name != "payments-main" {
			t.Errorf("list by team %+v, %v", page.Chains, err)
		}
		if err := e.svc.RefreshInfo(ctx); err != nil {
			t.Fatal(err)
		}
		if n := infoLines(t, c.PublicID); n != 1 {
			t.Errorf("%d info series", n)
		}

		// Nothing names it any more: it is deleted with its steps.
		if _, err := e.teams.Update(ctx, teamAdmin, e.payments.PublicID, nil, teams.Input{Name: "payments",
			Slug: "payments", ChainEntries: &[]teams.ChainEntry{}}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.d.Pool.Exec(ctx, `DELETE FROM route_escalation_chains`); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.Delete(ctx, by, c.PublicID, nil); err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM escalation_steps`) + e.count(t, `SELECT count(*) FROM
			escalation_step_users`); n != 0 {
			t.Errorf("%d rows left", n)
		}
		if n := e.count(t, `SELECT count(*) FROM audit_log WHERE action = $1`, escalation.ActionDeleted); n != 1 {
			t.Errorf("%d deletion entries", n)
		}
		if err := e.schedules.Delete(ctx, schAdmin, e.main.PublicID, nil); err != nil {
			t.Errorf("deleting the schedule once no step names it: %v", err)
		}
		if err := e.teams.Delete(ctx, teamAdmin, e.payments.PublicID, nil); err != nil {
			t.Errorf("deleting the team once it owns no chain: %v", err)
		}
	})
}

func infoLines(t *testing.T, id string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	metrics.Handler(nil).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	return strings.Count(rec.Body.String(), "\nmuster_escalation_chain_info{chain=\""+id+"\",")
}

// waitUntil polls cond until it holds, failing after 5 s.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen in time", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waiting counts the sessions of the database that wait for a lock in a statement whose text contains like, such as
// the name of a query of sqlc.
func (e *env) waiting(t *testing.T, like string) int {
	t.Helper()
	return e.count(t, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()
		AND wait_event_type = 'Lock' AND query LIKE '%' || $1 || '%'`, like)
}

// hold begins a transaction that runs sql with args and leaves it open; the test's end rolls it back.
func (e *env) hold(t *testing.T, sql string, args ...any) pgx.Tx {
	t.Helper()
	tx, err := e.d.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) })
	if _, err := tx.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
	return tx
}

// TestIntegrationChainRefusalRaces is the lock order of internal/escalation on PostgreSQL 14 and 17, each case with a
// transaction held open as the other path holds it: (a) a Team save that has read the chain FOR KEY SHARE and named
// it makes the chain's deletion wait, which then answers chain_in_use; (b) a Schedule's deletion that holds the
// Schedule makes a chain save naming it wait, which then answers unknown_id at the step; (c) a Team's deletion that
// holds the Team makes a chain save owned by it wait, which then answers unknown_id at /owning_team_id; (d) making a
// Schedule main, which locks the new main Schedule and then updates the old one, while a chain save names both, ends
// without a deadlock.
func TestIntegrationChainRefusalRaces(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		wait := escalation.StepInput{Kind: escalation.KindWait, WaitSeconds: ptr(60)}
		onCall := func(id string) escalation.StepInput {
			return escalation.StepInput{Kind: escalation.KindPageOnCall, ScheduleID: ptr(id)}
		}
		fieldAt := func(err error) string {
			if f, ok := errors.AsType[*escalation.FieldError](err); ok {
				return f.Pointer + " " + f.Code
			}
			return fmt.Sprint(err)
		}

		t.Run("a team save naming the chain", func(t *testing.T) {
			c, err := e.svc.Create(ctx, by, escalation.Input{Name: "race-a", Steps: []escalation.StepInput{wait}})
			if err != nil {
				t.Fatal(err)
			}
			tx := e.hold(t, `SELECT id FROM teams WHERE org_id = $1 AND id = $2 FOR NO KEY UPDATE`, e.orgID,
				e.payments.ID)
			if _, err := tx.Exec(ctx, `SELECT id FROM escalation_chains WHERE org_id = $1 AND id = $2 FOR KEY SHARE`,
				e.orgID, c.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO team_escalation_chains (org_id, team_id, chain_id, added_at)
				VALUES ($1, $2, $3, $4)`, e.orgID, e.payments.ID, c.ID, t0); err != nil {
				t.Fatal(err)
			}
			deleted := make(chan error, 1)
			go func() { deleted <- e.svc.Delete(ctx, by, c.PublicID, nil) }()
			waitUntil(t, "the deletion waiting for the chain", func() bool {
				return e.waiting(t, "LockChainForDeletion") == 1
			})
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			err = <-deleted
			if use, ok := errors.AsType[*escalation.InUseError](err); !ok || len(use.Holders) != 1 ||
				use.Holders[0].Type != escalation.HolderTeam {
				t.Errorf("the deletion after the save = %v", err)
			}
			if _, err := e.d.Pool.Exec(ctx, `DELETE FROM team_escalation_chains`); err != nil {
				t.Fatal(err)
			}
		})

		t.Run("a schedule deletion", func(t *testing.T) {
			gone, err := e.schedules.Create(ctx, schAdmin, schedules.Input{TeamID: e.payments.PublicID, Name: "Gone"})
			if err != nil {
				t.Fatal(err)
			}
			tx := e.hold(t, `SELECT id FROM schedules WHERE org_id = $1 AND id = $2 FOR UPDATE`, e.orgID, gone.ID)
			saved := make(chan error, 1)
			go func() {
				_, err := e.svc.Create(ctx, by, escalation.Input{Name: "race-b",
					Steps: []escalation.StepInput{onCall(gone.PublicID)}})
				saved <- err
			}()
			waitUntil(t, "the save waiting for the schedule", func() bool {
				return e.waiting(t, "ResolveSchedules") == 1
			})
			if _, err := tx.Exec(ctx, `DELETE FROM schedules WHERE id = $1`, gone.ID); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if got := fieldAt(<-saved); got != "/steps/0/schedule_id unknown_id" {
				t.Errorf("the save after the deletion = %s", got)
			}
		})

		t.Run("a team deletion", func(t *testing.T) {
			gone, err := e.teams.Create(ctx, teamAdmin, teams.Input{Name: "gone", Slug: "gone"})
			if err != nil {
				t.Fatal(err)
			}
			tx := e.hold(t, `SELECT id FROM teams WHERE org_id = $1 AND id = $2 FOR NO KEY UPDATE`, e.orgID, gone.ID)
			saved := make(chan error, 1)
			go func() {
				_, err := e.svc.Create(ctx, by, escalation.Input{Name: "race-c", OwningTeamID: ptr(gone.PublicID),
					Steps: []escalation.StepInput{wait}})
				saved <- err
			}()
			waitUntil(t, "the save waiting for the team", func() bool { return e.waiting(t, "LockOwningTeam") == 1 })
			if _, err := tx.Exec(ctx, `UPDATE teams SET deleted_at = $1 WHERE id = $2`, t0, gone.ID); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if got := fieldAt(<-saved); got != "/owning_team_id unknown_id" {
				t.Errorf("the save after the deletion = %s", got)
			}
		})

		t.Run("making a schedule main", func(t *testing.T) {
			first, err := e.schedules.Create(ctx, schAdmin, schedules.Input{TeamID: e.payments.PublicID, Name: "First"})
			if err != nil {
				t.Fatal(err)
			}
			second, err := e.schedules.Create(ctx, schAdmin, schedules.Input{TeamID: e.payments.PublicID, Name: "Next"})
			if err != nil {
				t.Fatal(err)
			}
			if first.ID > second.ID {
				first, second = second, first
			}
			tx := e.hold(t, `SELECT id FROM schedules WHERE org_id = $1 AND id = $2 FOR UPDATE`, e.orgID, second.ID)
			saved := make(chan error, 1)
			go func() {
				_, err := e.svc.Create(ctx, by, escalation.Input{Name: "race-d",
					Steps: []escalation.StepInput{onCall(second.PublicID), onCall(first.PublicID)}})
				saved <- err
			}()
			waitUntil(t, "the save waiting for the new main schedule", func() bool {
				return e.waiting(t, "ResolveSchedules") == 1
			})
			if _, err := tx.Exec(ctx, `UPDATE schedules SET main = false, version = version + 1 WHERE id = $1`,
				first.ID); err != nil {
				t.Fatalf("clearing the old main schedule while the save holds it: %v", err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-saved; err != nil {
				t.Errorf("the save after making a schedule main = %v", err)
			}
		})
	})
}

// TestIntegrationDeletedRouteReferrer is C-32.FR-2 with a Route deleted after it named a chain: the chain reads no
// referrer, its deletion succeeds, and the entry of the deleted Route goes with it (ON DELETE CASCADE).
func TestIntegrationDeletedRouteReferrer(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		if err := routing.EnsureDefault(ctx, routing.NewStore(e.d.Pool), e.orgID, t0); err != nil {
			t.Fatal(err)
		}
		// A copy of the Default route that is not the default, named "Infrastructure".
		var cols []string
		rows, err := e.d.Pool.Query(ctx, `SELECT column_name FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'routes' AND column_name <> 'id'
			ORDER BY ordinal_position`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				t.Fatal(err)
			}
			cols = append(cols, c)
		}
		rows.Close()
		values := make([]string, len(cols))
		for i, c := range cols {
			switch c {
			case "public_id":
				values[i] = `'RT0000000000NF'`
			case "name":
				values[i] = `'Infrastructure'`
			case "is_default":
				values[i] = `false`
			default:
				values[i] = c
			}
		}
		var route int64
		if err := e.d.Pool.QueryRow(ctx, `INSERT INTO routes (`+strings.Join(cols, ", ")+`) SELECT `+
			strings.Join(values, ", ")+` FROM routes WHERE is_default RETURNING id`).Scan(&route); err != nil {
			t.Fatal(err)
		}
		c, err := e.svc.Create(ctx, by, escalation.Input{Name: "infra", Steps: []escalation.StepInput{}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.d.Pool.Exec(ctx, `INSERT INTO route_escalation_chains (org_id, route_id, chain_id, added_at)
			VALUES ($1, $2, $3, $4)`, e.orgID, route, c.ID, t0); err != nil {
			t.Fatal(err)
		}
		if got, err := e.svc.Get(ctx, c.PublicID); err != nil || len(got.Referrers.Routes) != 1 {
			t.Fatalf("referrers of a route %+v, %v", got.Referrers, err)
		}
		if err := e.svc.Delete(ctx, by, c.PublicID, nil); !errors.Is(err, escalation.ErrInUse) {
			t.Errorf("deleting a chain a route names = %v", err)
		}
		if _, err := e.d.Pool.Exec(ctx, `UPDATE routes SET deleted_at = $1 WHERE id = $2`, t0, route); err != nil {
			t.Fatal(err)
		}
		if got, err := e.svc.Get(ctx, c.PublicID); err != nil || len(got.Referrers.Routes) != 0 {
			t.Errorf("referrers of a deleted route %+v, %v", got.Referrers, err)
		}
		if err := e.svc.Delete(ctx, by, c.PublicID, nil); err != nil {
			t.Fatalf("deleting a chain only a deleted route names = %v", err)
		}
		if n := e.count(t, `SELECT count(*) FROM route_escalation_chains WHERE route_id = $1`, route); n != 0 {
			t.Errorf("%d entries of the deleted route left", n)
		}
	})
}

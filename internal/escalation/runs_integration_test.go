// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package escalation_test

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/escalation"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/routing"
	"github.com/muster-io/muster/internal/teams"
	"github.com/muster-io/muster/internal/timers"
	timersdb "github.com/muster-io/muster/internal/timers/dbgen"
)

// runsEnv is the setup of the chains with a Route, the dispatcher, the runs of two replicas on one business clock, and
// the chains devops, of the Route, and payments, of the Route and of the Team payments.
type runsEnv struct {
	*env
	clock    *clock.Manual
	routes   *routing.Service
	groups   *groups.Service
	runs     *escalation.Runs
	other    *escalation.Runs
	route    routing.Route
	devops   escalation.Chain
	payments escalation.Chain
	log      *bytes.Buffer
}

func setupRuns(t *testing.T, s dbtest.Server) *runsEnv {
	t.Helper()
	ctx := t.Context()
	e := &runsEnv{env: setup(t, s), clock: clock.NewManual(t0), log: &bytes.Buffer{}}
	for _, stmt := range []string{
		`CREATE TABLE timeline_entries_p202611 PARTITION OF timeline_entries
			FOR VALUES FROM ('2026-11-01 00:00:00+00') TO ('2026-12-01 00:00:00+00')`,
	} {
		if _, err := e.d.Pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	logger := logging.New(e.log, logging.LevelInfo)
	w := audit.NewWriter(logger, e.clock)
	store := routing.NewStore(e.d.Pool)
	if err := routing.EnsureDefault(ctx, store, e.orgID, t0); err != nil {
		t.Fatal(err)
	}
	e.routes = routing.New(routing.Config{OrgID: e.orgID, Store: store, Audit: w, Business: e.clock,
		Real: clock.Real{}, Router: routing.NewRouter(e.orgID), Log: logger})
	e.groups = groups.New(groups.Config{OrgID: e.orgID, Store: groups.NewStore(e.d.Pool), Audit: w,
		Business: e.clock, Log: logger})
	chains := escalation.New(escalation.Config{OrgID: e.orgID, Store: escalation.NewStore(e.d.Pool), Audit: w,
		Business: e.clock})
	cfg := escalation.RunsConfig{DB: e.d.Pool, Groups: e.groups, OnCalls: e.schedules, Log: logger}
	e.runs, e.other = chains.Runs(cfg), chains.Runs(cfg)
	e.groups.SetChainRuns(e.runs)
	var err error
	page := func(importance string) escalation.StepInput {
		return escalation.StepInput{Kind: escalation.KindPageOnCall, ScheduleID: ptr(e.main.PublicID),
			Importance: ptr(importance)}
	}
	if e.devops, err = chains.Create(ctx, by, escalation.Input{Name: "devops", Steps: []escalation.StepInput{
		page("normal"), {Kind: escalation.KindWait, WaitSeconds: ptr(600)}, page("important")}}); err != nil {
		t.Fatal(err)
	}
	if e.payments, err = chains.Create(ctx, by, escalation.Input{Name: "payments",
		OwningTeamID: ptr(e.env.payments.PublicID), Steps: []escalation.StepInput{page("normal"),
			{Kind: escalation.KindWait, WaitSeconds: ptr(600)},
			{Kind: escalation.KindPagePeople, UserIDs: &[]string{e.people["petr"]}, Importance: ptr("important"),
				Condition: ptr("critical")}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.teams.Update(ctx, teamAdmin, e.env.payments.PublicID, nil, teams.Input{Name: "payments",
		Slug: "payments", ChainEntries: &[]teams.ChainEntry{{ChainID: e.payments.PublicID}}}); err != nil {
		t.Fatal(err)
	}
	e.route = e.newRoute(t, "infra", true, e.devops.PublicID, e.payments.PublicID)
	return e
}

// newRoute creates a Route with the Paging policy that delivers to the Teams and, with teamChains, runs their chains,
// naming the chains.
func (e *runsEnv) newRoute(t *testing.T, name string, teamChains bool, chains ...string) routing.Route {
	t.Helper()
	entries := make([]routing.ChainEntry, len(chains))
	for i, c := range chains {
		entries[i] = routing.ChainEntry{ChainID: c, Condition: routing.ConditionAny}
	}
	r, err := e.routes.Create(t.Context(), routing.Requester{Actor: audit.System, Transport: audit.TransportSystem},
		routing.Input{Name: name, GroupKey: []string{"alertname"},
			Policy: routing.Profiles()[0].Policy, Matchers: []routing.Matcher{{Label: "team", Op: "=", Value: name}},
			ChainEntries: &entries, DeliverToTeams: ptr(true), RunTeamChains: ptr(teamChains)})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// group inserts a firing warning Alert Group of the Route with the Team payments, and returns its id and public_id.
func (e *runsEnv) group(t *testing.T, number int64, route int64) (int64, string) {
	t.Helper()
	publicID := fmt.Sprintf("AG%012d", number)
	var id int64
	if err := e.d.Pool.QueryRow(t.Context(), `INSERT INTO alert_groups (org_id, public_id, number, route_id,
		group_key_labels, group_key_values, group_key_sha256, title, common_labels, common_annotations,
		integration_ids, status, severity_level, urgent, created_at, last_changed_at)
		VALUES ($1, $2, $3, $4, '{alertname}', '{}', sha256(convert_to($2::text, 'UTF8')), 'OOMKilled', '{}', '{}',
		        '{}', 'firing', 'warning', false, $5, $5)
		RETURNING id`, e.orgID, publicID, number, route, e.clock.Now()).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.Pool.Exec(t.Context(), `INSERT INTO alert_group_teams (org_id, alert_group_id, team_id, rule,
		joined_at) VALUES ($1, $2, $3, 'label', $4)`, e.orgID, id, e.env.payments.ID, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	return id, publicID
}

// fire fires the timer of a run at its deadline as the worker of runs does, in a transaction of its own.
func fireRun(t *testing.T, e *runsEnv, r *escalation.Runs, run int64) {
	t.Helper()
	var deadline time.Time
	if err := e.d.Pool.QueryRow(t.Context(), `SELECT deadline FROM timers WHERE escalation_run_id = $1`,
		run).Scan(&deadline); err != nil {
		t.Fatalf("the timer of run %d: %v", run, err)
	}
	var after func(context.Context)
	if err := pgx.BeginFunc(t.Context(), e.d.Pool, func(tx pgx.Tx) error {
		var err error
		after, err = r.FireStep(t.Context(), tx, run, deadline)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if after != nil {
		after(t.Context())
	}
}

// TestIntegrationRuns is C-32.FR-4, FR-5, FR-7, FR-14, FR-18, C-32.AC-5, C-32.AC-12 and P-70 on PostgreSQL: the first
// Publication of an Alert Group starts the Route's chain devops and the chain payments, which the Route and the Team
// both name, once with both sources; each run has its own escalation_step timer, naming the run and not the Alert
// Group, beside the Alert Group's Reminder timer; both steps fire through the dispatcher, then two timer workers with
// leases of their own run rounds at once over one database, and each step is made once; payments skips its step for critical; once both ran out the
// Alert Group is Unclaimed with one unclaimed; "How it went" lists the runs; deleting the Alert Group deletes its runs
// and their timers. A Route without "Run the Teams' Escalation chains" starts no chain of its Teams.
func TestIntegrationRuns(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setupRuns(t, s)
		ctx := t.Context()
		id, publicID := e.group(t, 1, e.route.ID)
		if _, err := e.d.Pool.Exec(ctx, `INSERT INTO timers (org_id, alert_group_id, kind, deadline, notice_number,
			created_at, updated_at) VALUES ($1, $2, 'reminder', $3, 1, $3, $3)`, e.orgID, id,
			t0.Add(4*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := e.groups.MarkPublished(ctx, id, t0, true); err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM timers WHERE alert_group_id = $1 OR escalation_run_id IN
			(SELECT id FROM escalation_runs WHERE alert_group_id = $1)`, id); n != 3 {
			t.Errorf("%d timers, want the two of the runs and the reminder", n)
		}
		if n := e.count(t, `SELECT count(*) FROM timers WHERE kind = 'escalation_step' AND alert_group_id IS NULL
			AND storm_id IS NULL AND escalation_run_id IS NOT NULL`); n != 2 {
			t.Errorf("%d timers of runs naming their run", n)
		}
		if n := e.count(t, `SELECT count(*) FROM timers WHERE alert_group_id = $1 AND kind = 'ack_timeout'`,
			id); n != 0 {
			t.Error("the ack timeout runs beside the chains")
		}
		chains, err := e.runs.GroupChains(ctx, []string{publicID})
		if err != nil {
			t.Fatal(err)
		}
		runs := chains[publicID]
		if len(runs) != 2 || runs[0].Chain.Name != "devops" || runs[1].Chain.Name != "payments" ||
			len(runs[1].Sources) != 2 || runs[1].Sources[1].Team == nil || runs[0].Step.Number != 1 {
			t.Fatalf("runs %+v", runs)
		}
		var devops, payments int64
		if err := e.d.Pool.QueryRow(ctx, `SELECT (SELECT id FROM escalation_runs WHERE chain_id = $1),
			(SELECT id FROM escalation_runs WHERE chain_id = $2)`, e.devops.ID, e.payments.ID).Scan(&devops,
			&payments); err != nil {
			t.Fatal(err)
		}
		fireRun(t, e, e.runs, devops)
		fireRun(t, e, e.other, payments)
		chains, _ = e.runs.GroupChains(ctx, []string{publicID})
		for _, r := range chains[publicID] {
			if r.Step == nil || r.Step.Number != 2 || r.NextStepAt == nil || !r.NextStepAt.Equal(t0.Add(10*time.Minute)) {
				t.Errorf("after the first steps %+v", r)
			}
		}

		// Two replicas, each with its timer worker and its lease, run rounds at once: the claim gives each step's timer
		// to one of them, and each step is made once.
		e.clock.Set(t0.Add(10 * time.Minute))
		var wg sync.WaitGroup
		for i, r := range []*escalation.Runs{e.runs, e.other} {
			w := &timers.Worker{Store: timers.NewStore(e.d.Pool), Log: logging.New(e.log, logging.LevelInfo),
				Lease: db.Lease{Owner: fmt.Sprintf("replica-%d", i), Duration: timers.Lease,
					Clocks: clock.Clocks{Business: e.clock, Real: clock.Real{}}},
				Organizations: func(context.Context) ([]int64, error) { return []int64{e.orgID}, nil },
				Handlers: map[string]timers.Handler{escalation.TimerKind: func(ctx context.Context, tx timersdb.DBTX,
					_ int64, tm timers.Timer) (func(context.Context), error) {
					return r.FireStep(ctx, tx, *tm.EscalationRunID, tm.Deadline)
				}}}
			for range 3 {
				wg.Go(func() {
					if _, err := w.Round(ctx); err != nil {
						t.Error(err)
					}
				})
			}
		}
		wg.Wait()
		if n := e.count(t, `SELECT count(*) FROM timers WHERE kind = 'escalation_step'`); n != 0 {
			t.Errorf("%d timers of runs left after the rounds", n)
		}
		if n := e.count(t, `SELECT count(*) FROM escalation_run_steps WHERE number = 3`); n != 2 {
			t.Errorf("%d steps 3 made, want one per run", n)
		}
		if n := e.count(t, `SELECT count(*) FROM escalation_run_steps WHERE run_id = $1 AND number = 3
			AND outcome = 'skipped' AND reason = 'severity'`, payments); n != 1 {
			t.Errorf("payments' step for critical: %d skipped", n)
		}
		if n := e.count(t, `SELECT count(*) FROM timeline_entries WHERE alert_group_id = $1
			AND event = 'escalation_step_skipped' AND reason = 'severity' AND escalation_run_id = $2
			AND escalation_chain_id = $3 AND step_number = 3 AND kind = 'escalation' AND loudness = 'quiet'`, id,
			payments, e.payments.ID); n != 1 {
			t.Errorf("%d escalation_step_skipped", n)
		}
		if n := e.count(t, `SELECT count(*) FROM timeline_entries WHERE alert_group_id = $1 AND event = 'unclaimed'`,
			id); n != 1 || e.count(t, `SELECT count(*) FROM alert_groups WHERE id = $1 AND unclaimed`, id) != 1 {
			t.Errorf("%d unclaimed entries", n)
		}
		if n := e.count(t, `SELECT count(*) FROM escalation_runs WHERE alert_group_id = $1 AND state = 'ran_out'
			AND ended_at IS NOT NULL`, id); n != 2 {
			t.Errorf("%d runs ran out", n)
		}
		if n := e.count(t, `SELECT count(*) FROM timers WHERE kind = 'escalation_step'`); n != 0 {
			t.Errorf("%d timers of runs left", n)
		}
		page, err := e.runs.ChainRuns(ctx, e.payments.PublicID, nil, 10)
		if err != nil || len(page.Runs) != 1 || page.Runs[0].State != escalation.RunRanOut ||
			len(page.Runs[0].Steps) != 3 || page.Runs[0].Steps[2].Reason != groups.SkipSeverity ||
			page.Runs[0].Group.PublicID != publicID || page.Runs[0].StoppedBy != nil {
			t.Errorf("how it went %+v, %v", page, err)
		}

		// A Route that does not run the Teams' chains starts none of them.
		other := e.newRoute(t, "test", false)
		second, _ := e.group(t, 2, other.ID)
		if err := e.groups.MarkPublished(ctx, second, t0.Add(10*time.Minute), true); err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM escalation_runs WHERE alert_group_id = $1`, second); n != 0 {
			t.Errorf("%d runs without the Teams' chains", n)
		}
		if n := e.count(t, `SELECT count(*) FROM timers WHERE alert_group_id = $1 AND kind = 'ack_timeout'`,
			second); n != 1 {
			t.Errorf("no ack timeout on an alert group without chains")
		}

		// Deleting the Alert Group deletes its runs and their timers.
		if _, err := e.d.Pool.Exec(ctx, `DELETE FROM alert_groups WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM escalation_runs WHERE alert_group_id = $1`, id) +
			e.count(t, `SELECT count(*) FROM escalation_run_steps WHERE run_id IN ($1, $2)`, devops, payments) +
			e.count(t, `SELECT count(*) FROM timers WHERE alert_group_id = $1`, id); n != 0 {
			t.Errorf("%d rows left of the deleted alert group", n)
		}
	})
}

// TestIntegrationRunTimerCascade: deleting an Alert Group whose runs still run deletes the runs and their live timers,
// and no other's; a stop deletes the timers in the stop's transaction.
func TestIntegrationRunTimerCascade(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setupRuns(t, s)
		ctx := t.Context()
		id, _ := e.group(t, 1, e.route.ID)
		if err := e.groups.MarkPublished(ctx, id, t0, true); err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM timers WHERE kind = 'escalation_step'`); n != 2 {
			t.Fatalf("%d timers", n)
		}
		// Deleting an Alert Group whose runs still run deletes the runs and their live timers.
		other, _ := e.group(t, 2, e.route.ID)
		if err := e.groups.MarkPublished(ctx, other, t0, true); err != nil {
			t.Fatal(err)
		}
		if _, err := e.d.Pool.Exec(ctx, `DELETE FROM alert_groups WHERE id = $1`, other); err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM escalation_runs WHERE alert_group_id = $1`, other) +
			e.count(t, `SELECT count(*) FROM timers WHERE kind = 'escalation_step'`); n != 2 {
			t.Errorf("after deleting an alert group with live runs: %d rows, want the two runs and timers of the other", n)
		}
		if err := pgx.BeginFunc(ctx, e.d.Pool, func(tx pgx.Tx) error {
			return e.runs.Stop(ctx, tx, &groups.Group{ID: id, PublicID: "AG000000000001", Number: 1},
				groups.ChainStop{Command: groups.StopResolve, At: t0})
		}); err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM timers WHERE kind = 'escalation_step'`) +
			e.count(t, `SELECT count(*) FROM escalation_runs WHERE state <> 'stopped'`); n != 0 {
			t.Errorf("%d timers or running runs after the stop", n)
		}
		if _, err := e.d.Pool.Exec(ctx, `DELETE FROM alert_groups WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM escalation_runs`); n != 0 {
			t.Errorf("%d runs left", n)
		}
	})
}

// TestIntegrationRunStepLimit is C-32.FR-15 and FR-7 on PostgreSQL: a chain of repeats nested four deep without a wait
// runs out at escalation.MaxStepsPerRun steps, over several firings, and its Alert Group is Unclaimed with one
// unclaimed and no timer left.
func TestIntegrationRunStepLimit(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setupRuns(t, s)
		ctx := t.Context()
		repeat := escalation.StepInput{Kind: escalation.KindRepeat, RepeatFrom: ptr(1), RepeatTimes: ptr(10)}
		c, err := escalation.New(escalation.Config{OrgID: e.orgID, Store: escalation.NewStore(e.d.Pool),
			Audit: audit.NewWriter(logging.New(e.log, logging.LevelInfo), e.clock), Business: e.clock}).Create(ctx, by,
			escalation.Input{Name: "nested", Steps: []escalation.StepInput{{Kind: escalation.KindPageOnCall,
				ScheduleID: ptr(e.main.PublicID), Importance: ptr("normal")}, repeat, repeat, repeat, repeat}})
		if err != nil {
			t.Fatal(err)
		}
		r := e.newRoute(t, "nested", false, c.PublicID)
		id, _ := e.group(t, 3, r.ID)
		if err := e.groups.MarkPublished(ctx, id, t0, true); err != nil {
			t.Fatal(err)
		}
		var run int64
		if err := e.d.Pool.QueryRow(ctx, `SELECT id FROM escalation_runs WHERE alert_group_id = $1`, id).
			Scan(&run); err != nil {
			t.Fatal(err)
		}
		for range 10 {
			if e.count(t, `SELECT count(*) FROM timers WHERE escalation_run_id = $1`, run) == 0 {
				break
			}
			fireRun(t, e, e.runs, run)
		}
		if n := e.count(t, `SELECT count(*) FROM escalation_run_steps WHERE run_id = $1`, run); n !=
			escalation.MaxStepsPerRun {
			t.Errorf("%d steps, want %d", n, escalation.MaxStepsPerRun)
		}
		if n := e.count(t, `SELECT count(*) FROM escalation_runs WHERE id = $1 AND state = 'ran_out'`, run) +
			e.count(t, `SELECT count(*) FROM alert_groups WHERE id = $1 AND unclaimed`, id) +
			e.count(t, `SELECT count(*) FROM timeline_entries WHERE alert_group_id = $1 AND event = 'unclaimed'`,
				id); n != 3 {
			t.Errorf("ran out, Unclaimed and one unclaimed: %d of 3", n)
		}
		if n := e.count(t, `SELECT count(*) FROM timers WHERE escalation_run_id = $1`, run); n != 0 {
			t.Errorf("%d timers left", n)
		}
	})
}

func (e *runsEnv) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.d.Pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// petr is the responder petr as the caller of Commands.
func (e *runsEnv) petr(t *testing.T) groups.Caller {
	t.Helper()
	var id int64
	if err := e.d.Pool.QueryRow(t.Context(), `SELECT id FROM users WHERE public_id = $1`, e.people["petr"]).
		Scan(&id); err != nil {
		t.Fatal(err)
	}
	return groups.Caller{Actor: audit.User(id, e.people["petr"]), Transport: audit.TransportUI,
		Permissions: []auth.Permission{"alert-groups:read", groups.PermissionAcknowledge, groups.PermissionResolve,
			groups.PermissionSnooze}}
}

// TestIntegrationStartOver is C-32.FR-5, FR-7 and FR-18 on PostgreSQL: Acknowledge stops only the run still running,
// leaving the one that ran out as it was; Unacknowledge opens a second start whose runs are new and are the only ones
// the read of the Alert Group gives; once those ran out, the Alert Group is Unclaimed, the stopped run of the first
// start not counted.
func TestIntegrationStartOver(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setupRuns(t, s)
		ctx := t.Context()
		single, err := escalation.New(escalation.Config{OrgID: e.orgID, Store: escalation.NewStore(e.d.Pool),
			Audit: audit.NewWriter(logging.New(e.log, logging.LevelInfo), e.clock), Business: e.clock}).Create(ctx, by,
			escalation.Input{Name: "single", Steps: []escalation.StepInput{{Kind: escalation.KindPageOnCall,
				ScheduleID: ptr(e.main.PublicID), Importance: ptr("normal")}}})
		if err != nil {
			t.Fatal(err)
		}
		r := e.newRoute(t, "restart", false, single.PublicID, e.devops.PublicID)
		id, publicID := e.group(t, 4, r.ID)
		if err := e.groups.MarkPublished(ctx, id, t0, true); err != nil {
			t.Fatal(err)
		}
		runOf := func(chain int64, generation int) int64 {
			t.Helper()
			var run int64
			if err := e.d.Pool.QueryRow(ctx, `SELECT id FROM escalation_runs WHERE alert_group_id = $1 AND chain_id = $2
				AND generation = $3`, id, chain, generation).Scan(&run); err != nil {
				t.Fatalf("the run of chain %d in start %d: %v", chain, generation, err)
			}
			return run
		}
		fireRun(t, e, e.runs, runOf(single.ID, 1))
		fireRun(t, e, e.runs, runOf(e.devops.ID, 1))
		petr := e.petr(t)
		if _, err := e.groups.Acknowledge(ctx, petr, publicID); err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM escalation_runs WHERE alert_group_id = $1 AND
			((chain_id = $2 AND state = 'ran_out' AND stop_command IS NULL) OR
			 (chain_id = $3 AND state = 'stopped' AND stop_command = 'acknowledge'))`, id, single.ID,
			e.devops.ID); n != 2 {
			t.Errorf("after the acknowledge: %d of 2 runs as expected", n)
		}
		if _, err := e.groups.Unacknowledge(ctx, petr, publicID); err != nil {
			t.Fatal(err)
		}
		chains, err := e.runs.GroupChains(ctx, []string{publicID})
		if err != nil {
			t.Fatal(err)
		}
		if got := chains[publicID]; len(got) != 2 || got[0].State != escalation.RunRunning ||
			got[1].State != escalation.RunRunning {
			t.Fatalf("the second start %+v", got)
		}
		fireRun(t, e, e.runs, runOf(single.ID, 2))
		devops := runOf(e.devops.ID, 2)
		fireRun(t, e, e.runs, devops)
		if e.count(t, `SELECT count(*) FROM alert_groups WHERE id = $1 AND unclaimed`, id) != 0 {
			t.Error("Unclaimed while devops still runs")
		}
		e.clock.Set(t0.Add(10 * time.Minute))
		fireRun(t, e, e.runs, devops)
		if n := e.count(t, `SELECT count(*) FROM alert_groups WHERE id = $1 AND unclaimed`, id) +
			e.count(t, `SELECT count(*) FROM timeline_entries WHERE alert_group_id = $1 AND event = 'unclaimed'`,
				id); n != 2 {
			t.Errorf("once the second start ran out: Unclaimed and one unclaimed, %d of 2", n)
		}
		// Unclaimed ends with the acknowledge that stops being firing (alert_groups_unclaimed_check).
		if _, err := e.groups.Acknowledge(ctx, petr, publicID); err != nil {
			t.Fatal(err)
		}
	})
}

// TestIntegrationUnclaimedUnpublished is C-17.FR-3 and C-32.FR-7 on PostgreSQL: an Alert Group that no Destination
// reached, whose chains ran out so that it is Unclaimed without a Publication, can be acknowledged, snoozed and
// resolved, Unclaimed ending each time, under alert_groups_unclaimed_check.
func TestIntegrationUnclaimedUnpublished(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setupRuns(t, s)
		ctx := t.Context()
		petr := e.petr(t)
		for i, run := range []func(publicID string) error{
			func(id string) error { _, err := e.groups.Acknowledge(ctx, petr, id); return err },
			func(id string) error {
				_, err := e.groups.Snooze(ctx, petr, id, groups.SnoozeEnd{Until: ptr(e.clock.Now().Add(time.Hour))})
				return err
			},
			func(id string) error { _, err := e.groups.Resolve(ctx, petr, id, nil); return err },
		} {
			id, publicID := e.group(t, int64(10+i), e.route.ID)
			e.exec(t, `UPDATE alert_groups SET chain_generation = 1, unclaimed = true WHERE id = $1`, id)
			if err := run(publicID); err != nil {
				t.Fatalf("command %d: %v", i, err)
			}
			if n := e.count(t, `SELECT count(*) FROM alert_groups WHERE id = $1 AND NOT unclaimed
				AND first_published_at IS NULL AND status <> 'firing'`, id); n != 1 {
				t.Errorf("command %d: still Unclaimed or firing", i)
			}
		}
	})
}

// TestIntegrationRunStepsRetention is C-09.FR-16 and C-32.FR-18 on PostgreSQL: "How it went" hides the steps of the
// runs of an Alert Group resolved retention.alert_details ago before they are deleted; the retention of the details
// then deletes them, keeping its runs with its summary row, and keeps those of a recent one.
func TestIntegrationRunStepsRetention(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setupRuns(t, s)
		ctx := t.Context()
		old, _ := e.group(t, 20, e.route.ID)
		recent, _ := e.group(t, 21, e.route.ID)
		for _, id := range []int64{old, recent} {
			if err := e.groups.MarkPublished(ctx, id, t0, true); err != nil {
				t.Fatal(err)
			}
			var run int64
			if err := e.d.Pool.QueryRow(ctx, `SELECT id FROM escalation_runs WHERE alert_group_id = $1
				AND chain_id = $2`, id, e.devops.ID).Scan(&run); err != nil {
				t.Fatal(err)
			}
			fireRun(t, e, e.runs, run)
		}
		e.exec(t, `UPDATE alert_groups SET status = 'resolved', resolved_at = $2, resolved_by_kind = 'system',
			resolve_reason = 'resolved', unclaimed = false WHERE id = ANY($1)`, []int64{old, recent}, t0)
		e.exec(t, `UPDATE alert_groups SET resolved_at = $2 WHERE id = $1`, recent, t0.Add(90*24*time.Hour))
		// Before the retention runs, "How it went" already hides the steps of the old Alert Group.
		e.clock.Set(t0.Add(91 * 24 * time.Hour))
		page, err := e.runs.ChainRuns(ctx, e.devops.PublicID, nil, 10)
		if err != nil {
			t.Fatal(err)
		}
		shown := map[int64]int{}
		for _, run := range page.Runs {
			if run.Group.Number == 20 || run.Group.Number == 21 {
				shown[run.Group.Number] = len(run.Steps)
			}
		}
		if shown[20] != 0 || shown[21] == 0 {
			t.Errorf("steps shown by alert group %v", shown)
		}
		got, err := groups.Purge(ctx, groups.NewStore(e.d.Pool), e.orgID, t0.Add(91*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM escalation_run_steps s JOIN escalation_runs r ON r.id = s.run_id
			WHERE r.alert_group_id = $1`, old); n != 0 || got.Details == 0 {
			t.Errorf("%d steps of the old alert group left, purged %+v", n, got)
		}
		if n := e.count(t, `SELECT count(*) FROM escalation_runs WHERE alert_group_id = $1`, old); n == 0 {
			t.Error("the runs of the old alert group went with their steps")
		}
		if n := e.count(t, `SELECT count(*) FROM escalation_run_steps s JOIN escalation_runs r ON r.id = s.run_id
			WHERE r.alert_group_id = $1`, recent); n == 0 {
			t.Error("the steps of the recent alert group went")
		}
	})
}

// TestIntegrationRunOwningTeam is C-25.FR-9, C-32.FR-14 and P-73 on PostgreSQL: a run takes the owning Team its chain
// has when it starts and keeps it; a step limited to Working hours is skipped outside that Team's, at a time inside
// the Organization's, and still after the chain was given another owning Team whose hours are the Organization's.
func TestIntegrationRunOwningTeam(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setupRuns(t, s)
		ctx := t.Context()
		// Monday 2026-11-16 12:00 UTC: inside the Organization's Working hours, outside those of payments.
		e.exec(t, `UPDATE organizations SET working_days = '{monday,tuesday,wednesday,thursday,friday}',
			workday_start = '09:00', workday_end = '18:00', time_zone = 'UTC' WHERE id = $1`, e.orgID)
		if _, err := e.teams.Update(ctx, teamAdmin, e.env.payments.PublicID, nil, teams.Input{Name: "payments",
			Slug: "payments", OwnWorkingHours: true, WorkingHours: &teams.WorkingHours{Days: []string{"saturday"},
				Start: "09:00", End: "18:00", TimeZone: "UTC"}}); err != nil {
			t.Fatal(err)
		}
		ops, err := e.teams.Create(ctx, teamAdmin, teams.Input{Name: "ops", Slug: "ops"})
		if err != nil {
			t.Fatal(err)
		}
		chains := escalation.New(escalation.Config{OrgID: e.orgID, Store: escalation.NewStore(e.d.Pool),
			Audit: audit.NewWriter(logging.New(e.log, logging.LevelInfo), e.clock), Business: e.clock})
		steps := []escalation.StepInput{{Kind: escalation.KindPageTeam, WorkingHoursOnly: ptr(true)},
			{Kind: escalation.KindWait, WaitSeconds: ptr(600)}, {Kind: escalation.KindPageTeam, WorkingHoursOnly: ptr(true)}}
		c, err := chains.Create(ctx, by, escalation.Input{Name: "hours", OwningTeamID: ptr(e.env.payments.PublicID),
			Steps: steps})
		if err != nil {
			t.Fatal(err)
		}
		r := e.newRoute(t, "hours", false, c.PublicID)
		id, _ := e.group(t, 30, r.ID)
		if err := e.groups.MarkPublished(ctx, id, t0, true); err != nil {
			t.Fatal(err)
		}
		var run int64
		if err := e.d.Pool.QueryRow(ctx, `SELECT id FROM escalation_runs WHERE alert_group_id = $1
			AND owning_team_id = $2`, id, e.env.payments.ID).Scan(&run); err != nil {
			t.Fatalf("the run with the owning team of its chain: %v", err)
		}
		fireRun(t, e, e.runs, run)
		// The chain changes its owning Team while the run runs: the run keeps the one it started with.
		if _, err := chains.Update(ctx, by, c.PublicID, nil, escalation.Input{Name: "hours",
			OwningTeamID: ptr(ops.PublicID), Steps: steps}); err != nil {
			t.Fatal(err)
		}
		e.clock.Set(t0.Add(10 * time.Minute))
		fireRun(t, e, e.runs, run)
		if n := e.count(t, `SELECT count(*) FROM escalation_run_steps WHERE run_id = $1 AND number IN (1, 3)
			AND outcome = 'skipped' AND reason = 'outside_working_hours'`, run); n != 2 {
			t.Errorf("%d of the 2 steps for the working hours of payments skipped", n)
		}
		// A run that starts now takes the new owning Team, whose hours are the Organization's.
		other, _ := e.group(t, 31, r.ID)
		if err := e.groups.MarkPublished(ctx, other, t0.Add(10*time.Minute), true); err != nil {
			t.Fatal(err)
		}
		var next int64
		if err := e.d.Pool.QueryRow(ctx, `SELECT id FROM escalation_runs WHERE alert_group_id = $1
			AND owning_team_id = $2`, other, ops.ID).Scan(&next); err != nil {
			t.Fatalf("the next run with the new owning team: %v", err)
		}
		fireRun(t, e, e.runs, next)
		if n := e.count(t, `SELECT count(*) FROM escalation_run_steps WHERE run_id = $1 AND number = 1
			AND outcome = 'made'`, next); n != 1 {
			t.Error("the step of the next run was not made in the working hours of the organization")
		}
	})
}

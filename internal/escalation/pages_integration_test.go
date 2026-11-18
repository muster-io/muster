// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package escalation_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/escalation"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/notificationrules"
	"github.com/muster-io/muster/internal/schedules"
	"github.com/muster-io/muster/internal/timers"
	timersdb "github.com/muster-io/muster/internal/timers/dbgen"
)

// everywhere is where the Pages go in the integration tests: one Destination of the Route, where everyone the Page
// names has an Account link.
type everywhere struct{}

func (everywhere) PageTargets(_ context.Context, _ groups.DBTX, _ *groups.Group, r groups.PageRequest) (
	[]groups.PageTarget, error) {
	return []groups.PageTarget{{DestinationID: 1, Source: groups.PageSourceRoute, Linked: r.People}}, nil
}

// TestIntegrationPages is C-31.FR-3, C-31.AC-1 and C-32.FR-3, FR-19 on PostgreSQL: a page_people step that Pages two
// people importantly starts a run of each one's important rule — the Organization's default — and one Page naming
// both, recorded as paged with its people and its importance; each rule run keeps its notification_rule_action timer,
// naming the rule run, beside the next step's escalation_step timer and the Reminder of the same Alert Group; after
// the wait, a timer worker fires both actions, each a later Page of its person, and the rule runs are done; the outcome
// of a Page's replies reaches one person and not the other, which records page_not_delivered; a stop of the run of
// the chain stops the rule runs that still run and removes their timers; a later Mention of a page_on_call step whose
// Schedule was deleted after the chain changed is made without it; deleting the Alert Group deletes them all.
func TestIntegrationPages(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setupRuns(t, s)
		ctx := t.Context()
		if _, err := e.d.Pool.Exec(ctx, `INSERT INTO users (org_id, public_id, login, name, role, source, status,
			created_at, updated_at) VALUES ($1, 'SR0000000000JV', 'ivan', 'Ivan', 'responder', 'local', 'active', $2,
			$2)`, e.orgID, t0); err != nil {
			t.Fatal(err)
		}
		logger := logging.New(e.log, logging.LevelInfo)
		rules := notificationrules.New(notificationrules.Config{OrgID: e.orgID,
			Store: notificationrules.NewStore(e.d.Pool), Business: e.clock}).Runs(notificationrules.RunsConfig{
			Business: e.clock, TimersChannel: groups.TimersChannel})
		pages := escalation.NewPages(escalation.PagesConfig{OnCalls: e.schedules, Rules: rules, Targets: everywhere{},
			Business: e.clock})
		chains := escalation.New(escalation.Config{OrgID: e.orgID, Store: escalation.NewStore(e.d.Pool),
			Audit: audit.NewWriter(logger, e.clock), Business: e.clock})
		runs := chains.Runs(escalation.RunsConfig{DB: e.d.Pool, Groups: e.groups, OnCalls: e.schedules, Pages: pages,
			Log: logger})
		e.groups.SetChainRuns(runs)
		e.groups.SetPageTargets(everywhere{})
		two, err := chains.Create(ctx, by, escalation.Input{Name: "two", Steps: []escalation.StepInput{
			{Kind: escalation.KindPagePeople, UserIDs: &[]string{e.people["petr"], "SR0000000000JV"},
				Importance: ptr("important")},
			{Kind: escalation.KindWait, WaitSeconds: ptr(600)},
			{Kind: escalation.KindPageOnCall, ScheduleID: ptr(e.main.PublicID)}}})
		if err != nil {
			t.Fatal(err)
		}
		route := e.newRoute(t, "two", false, two.PublicID)
		id, _ := e.group(t, 1, route.ID)
		if _, err := e.d.Pool.Exec(ctx, `INSERT INTO timers (org_id, alert_group_id, kind, deadline, notice_number,
			created_at, updated_at) VALUES ($1, $2, 'reminder', $3, 1, $3, $3)`, e.orgID, id,
			t0.Add(4*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := e.groups.MarkPublished(ctx, id, t0, true); err != nil {
			t.Fatal(err)
		}
		var run int64
		if err := e.d.Pool.QueryRow(ctx, `SELECT id FROM escalation_runs WHERE alert_group_id = $1`, id).Scan(
			&run); err != nil {
			t.Fatal(err)
		}
		fireRun(t, e, runs, run)
		if n := e.count(t, `SELECT count(*) FROM notification_rule_runs WHERE alert_group_id = $1
			AND escalation_run_id = $2 AND state = 'running' AND importance = 'important' AND position = 2`, id,
			run); n != 2 {
			t.Errorf("%d rule runs", n)
		}
		if n := e.count(t, `SELECT count(*) FROM timers WHERE kind = 'notification_rule_action'
			AND alert_group_id IS NULL AND escalation_run_id IS NULL AND storm_id IS NULL
			AND deadline = $1 AND notification_rule_run_id IN (SELECT id FROM notification_rule_runs
			WHERE alert_group_id = $2)`, t0.Add(5*time.Minute), id); n != 2 {
			t.Errorf("%d timers of the rule runs", n)
		}
		if n := e.count(t, `SELECT count(*) FROM timers WHERE alert_group_id = $1 OR escalation_run_id = $2
			OR notification_rule_run_id IN (SELECT id FROM notification_rule_runs WHERE alert_group_id = $1)`, id,
			run); n != 4 {
			t.Errorf("%d timers, want the Reminder, the run's and the two rule runs'", n)
		}
		if n := e.count(t, `SELECT count(*) FROM pages p WHERE p.alert_group_id = $1 AND p.escalation_run_id = $2
			AND p.step_number = 1 AND p.importance = 'important' AND p.again_seconds = 300 AND NOT p.again
			AND p.state = 'pending' AND (SELECT count(*) FROM page_users u WHERE u.page_id = p.id
			AND u.outcome = 'pending') = 2`, id, run); n != 1 {
			t.Errorf("%d first pages", n)
		}
		// The first Page and both rule runs carry the seq of the run step of step 1, which orders the steps of the run
		// for the Assignees (C-32.FR-12).
		if n := e.count(t, `SELECT count(*) FROM escalation_run_steps s WHERE s.run_id = $1 AND s.number = 1
			AND s.seq = (SELECT p.run_step_seq FROM pages p WHERE p.escalation_run_id = $1 AND p.step_number = 1)
			AND (SELECT count(*) FROM notification_rule_runs r WHERE r.escalation_run_id = $1
				AND r.run_step_seq = s.seq) = 2`, run); n != 1 {
			t.Errorf("%d run steps carried by the page and the rule runs", n)
		}
		if n := e.count(t, `SELECT count(*) FROM timeline_entries WHERE alert_group_id = $1 AND event = 'paged'
			AND kind = 'escalation' AND loudness = 'loud' AND mentions = '{paged}' AND cardinality(paged_user_ids) = 2
			AND page_importance = 'important' AND escalation_run_id = $2 AND step_number = 1`, id, run); n != 1 {
			t.Errorf("%d paged entries", n)
		}

		// After the wait a timer worker fires both actions: each a later Mention of its person.
		e.clock.Set(t0.Add(5 * time.Minute))
		w := &timers.Worker{Store: timers.NewStore(e.d.Pool), Log: logger,
			Lease: db.Lease{Owner: "replica", Duration: timers.Lease,
				Clocks: clock.Clocks{Business: e.clock, Real: clock.Real{}}},
			Organizations: func(context.Context) ([]int64, error) { return []int64{e.orgID}, nil },
			Handlers: map[string]timers.Handler{notificationrules.TimerKind: func(ctx context.Context,
				tx timersdb.DBTX, _ int64, tm timers.Timer) (func(context.Context), error) {
				return pages.FireRule(ctx, e.groups, tx, *tm.NotificationRuleRunID, tm.Deadline)
			}}}
		if _, err := w.Round(ctx); err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM notification_rule_runs WHERE alert_group_id = $1
			AND state = 'done' AND ended_at IS NOT NULL`, id); n != 2 {
			t.Errorf("%d rule runs done", n)
		}
		if n := e.count(t, `SELECT count(*) FROM pages WHERE alert_group_id = $1 AND again
			AND notification_rule_run_id IS NOT NULL AND again_seconds IS NULL`, id); n != 2 {
			t.Errorf("%d later Mentions", n)
		}
		if n := e.count(t, `SELECT count(*) FROM timers WHERE kind = 'notification_rule_action'`); n != 0 {
			t.Errorf("%d timers of rule runs left", n)
		}

		// The replies of the first Page reached petr only.
		var page, petr int64
		if err := e.d.Pool.QueryRow(ctx, `SELECT p.id, u.id FROM pages p, users u WHERE p.alert_group_id = $1
			AND NOT p.again AND u.login = 'petr'`, id).Scan(&page, &petr); err != nil {
			t.Fatal(err)
		}
		var after func(context.Context)
		if err := pgx.BeginFunc(ctx, e.d.Pool, func(tx pgx.Tx) error {
			after, err = e.groups.SettlePage(ctx, tx, id, groups.PageOutcome{Page: page, Reached: []int64{petr},
				Ended: true, Delivered: true})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		after(ctx)
		if n := e.count(t, `SELECT count(*) FROM pages WHERE id = $1 AND state = 'delivered'`, page); n != 1 {
			t.Error("the page is not delivered")
		}
		if n := e.count(t, `SELECT count(*) FROM page_users u JOIN users p ON p.id = u.user_id WHERE u.page_id = $1
			AND ((p.login = 'petr' AND u.outcome = 'delivered') OR (p.login = 'ivan' AND u.outcome = 'not_delivered'
			AND u.reason = 'not_delivered'))`, page); n != 2 {
			t.Errorf("%d outcomes of the people", n)
		}
		if n := e.count(t, `SELECT count(*) FROM timeline_entries WHERE alert_group_id = $1
			AND event = 'page_not_delivered' AND reason = 'not_delivered' AND cardinality(paged_user_ids) = 1`,
			id); n != 1 {
			t.Errorf("%d page_not_delivered", n)
		}

		// A stop of the run of the chain stops the rule runs that still run, with their timers.
		id2, _ := e.group(t, 2, route.ID)
		e.clock.Set(t0.Add(10 * time.Minute))
		if err := e.groups.MarkPublished(ctx, id2, e.clock.Now(), true); err != nil {
			t.Fatal(err)
		}
		var run2 int64
		if err := e.d.Pool.QueryRow(ctx, `SELECT id FROM escalation_runs WHERE alert_group_id = $1`, id2).Scan(
			&run2); err != nil {
			t.Fatal(err)
		}
		fireRun(t, e, runs, run2)
		if err := rules.StopForGroup(ctx, e.d.Pool, id2, e.clock.Now()); err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM notification_rule_runs WHERE alert_group_id = $1
			AND state = 'stopped'`, id2); n != 2 {
			t.Errorf("%d rule runs stopped", n)
		}
		if n := e.count(t, `SELECT count(*) FROM timers WHERE kind = 'notification_rule_action'`); n != 0 {
			t.Errorf("%d timers of stopped rule runs", n)
		}

		// A later Mention after the chain lost its page_on_call step and the Schedule was deleted is made without
		// the Schedule: the run's steps are a snapshot that outlives it.
		spare, err := e.schedules.Create(ctx, schAdmin, schedules.Input{TeamID: e.env.payments.PublicID,
			Name: "Spare", RotationLayers: []schedules.RotationLayerInput{{Name: "Week",
				UserIDs: []string{e.people["petr"]}, ShiftLength: schedules.ShiftWeek, HandoverTime: "10:00",
				HandoverDay: "monday", StartDate: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)}}})
		if err != nil {
			t.Fatal(err)
		}
		oncall, err := chains.Create(ctx, by, escalation.Input{Name: "oncall", Steps: []escalation.StepInput{
			{Kind: escalation.KindPageOnCall, ScheduleID: ptr(spare.PublicID), Importance: ptr("important")}}})
		if err != nil {
			t.Fatal(err)
		}
		id3, _ := e.group(t, 3, e.newRoute(t, "oncall", false, oncall.PublicID).ID)
		if err := e.groups.MarkPublished(ctx, id3, e.clock.Now(), true); err != nil {
			t.Fatal(err)
		}
		var run3 int64
		if err := e.d.Pool.QueryRow(ctx, `SELECT id FROM escalation_runs WHERE alert_group_id = $1`, id3).Scan(
			&run3); err != nil {
			t.Fatal(err)
		}
		fireRun(t, e, runs, run3)
		if n := e.count(t, `SELECT count(*) FROM notification_rule_runs WHERE alert_group_id = $1
			AND state = 'running'`, id3); n != 1 {
			t.Fatalf("%d rule runs of the on-call", n)
		}
		// Stopping the rule runs of another Alert Group leaves these running, with their timers.
		if err := rules.StopForGroup(ctx, e.d.Pool, id2, e.clock.Now()); err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM notification_rule_runs r JOIN timers m
			ON m.notification_rule_run_id = r.id WHERE r.alert_group_id = $1 AND r.state = 'running'`, id3); n != 1 {
			t.Errorf("%d rule runs of the on-call running after a stop of another Alert Group's", n)
		}
		if _, err := chains.Update(ctx, by, oncall.PublicID, nil, escalation.Input{Name: "oncall",
			Steps: []escalation.StepInput{{Kind: escalation.KindPagePeople, UserIDs: &[]string{e.people["petr"]},
				Importance: ptr("normal")}}}); err != nil {
			t.Fatal(err)
		}
		if err := e.schedules.Delete(ctx, schAdmin, spare.PublicID, nil); err != nil {
			t.Fatal(err)
		}
		e.clock.Set(t0.Add(15 * time.Minute))
		if _, err := w.Round(ctx); err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM pages WHERE alert_group_id = $1 AND again
			AND schedule_id IS NULL`, id3); n != 1 {
			t.Errorf("%d later Mentions without the deleted Schedule", n)
		}
		if n := e.count(t, `SELECT count(*) FROM notification_rule_runs WHERE alert_group_id = $1
			AND state = 'done'`, id3); n != 1 {
			t.Errorf("%d rule runs of the on-call done", n)
		}
		if _, err := e.d.Pool.Exec(ctx, `DELETE FROM alert_groups WHERE id = ANY($1)`, []int64{id, id2, id3}); err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"notification_rule_runs", "pages", "page_users"} {
			if n := e.count(t, fmt.Sprintf(`SELECT count(*) FROM %s`, table)); n != 0 {
				t.Errorf("%d %s left", n, table)
			}
		}
	})
}

// TestIntegrationPageRunSteps is C-32.FR-12 on PostgreSQL for the run steps of Pages: across a wait, a second firing
// and a repeat, the Page of each step's first Mentions and the rule runs the step starts carry the seq of the
// escalation_run_steps row the step is recorded with — steps 1, 3 and 1 again as the seqs 1, 3 and 5.
func TestIntegrationPageRunSteps(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setupRuns(t, s)
		ctx := t.Context()
		logger := logging.New(e.log, logging.LevelInfo)
		rules := notificationrules.New(notificationrules.Config{OrgID: e.orgID,
			Store: notificationrules.NewStore(e.d.Pool), Business: e.clock}).Runs(notificationrules.RunsConfig{
			Business: e.clock, TimersChannel: groups.TimersChannel})
		pages := escalation.NewPages(escalation.PagesConfig{OnCalls: e.schedules, Rules: rules, Targets: everywhere{},
			Business: e.clock})
		chains := escalation.New(escalation.Config{OrgID: e.orgID, Store: escalation.NewStore(e.d.Pool),
			Audit: audit.NewWriter(logger, e.clock), Business: e.clock})
		runs := chains.Runs(escalation.RunsConfig{DB: e.d.Pool, Groups: e.groups, OnCalls: e.schedules, Pages: pages,
			Log: logger})
		e.groups.SetChainRuns(runs)
		e.groups.SetPageTargets(everywhere{})
		petr := escalation.StepInput{Kind: escalation.KindPagePeople, UserIDs: &[]string{e.people["petr"]},
			Importance: ptr("normal")}
		looped, err := chains.Create(ctx, by, escalation.Input{Name: "looped", Steps: []escalation.StepInput{petr,
			{Kind: escalation.KindWait, WaitSeconds: ptr(600)}, petr,
			{Kind: escalation.KindRepeat, RepeatFrom: ptr(1), RepeatTimes: ptr(1)}}})
		if err != nil {
			t.Fatal(err)
		}
		route := e.newRoute(t, "looped", false, looped.PublicID)
		id, _ := e.group(t, 1, route.ID)
		if err := e.groups.MarkPublished(ctx, id, t0, true); err != nil {
			t.Fatal(err)
		}
		var run int64
		if err := e.d.Pool.QueryRow(ctx, `SELECT id FROM escalation_runs WHERE alert_group_id = $1`, id).Scan(
			&run); err != nil {
			t.Fatal(err)
		}
		fireRun(t, e, runs, run)
		e.clock.Set(t0.Add(10 * time.Minute))
		fireRun(t, e, runs, run)
		var pairs string
		if err := e.d.Pool.QueryRow(ctx, `SELECT string_agg(p.step_number || '@' || coalesce(p.run_step_seq::text, '-'),
			' ' ORDER BY p.id) FROM pages p WHERE p.escalation_run_id = $1 AND p.notification_rule_run_id IS NULL`,
			run).Scan(&pairs); err != nil || pairs != "1@1 3@3 1@5" {
			t.Errorf("the run steps of the first Mentions %q %v", pairs, err)
		}
		if n := e.count(t, `SELECT count(*) FROM pages p WHERE p.escalation_run_id = $1
			AND p.notification_rule_run_id IS NULL AND NOT EXISTS (SELECT 1 FROM escalation_run_steps s
			WHERE s.run_id = p.escalation_run_id AND s.seq = p.run_step_seq AND s.number = p.step_number)`,
			run); n != 0 {
			t.Errorf("%d pages whose run step is not their step's", n)
		}
		if n := e.count(t, `SELECT count(*) FROM notification_rule_runs r WHERE r.escalation_run_id = $1
			AND EXISTS (SELECT 1 FROM escalation_run_steps s WHERE s.run_id = r.escalation_run_id
			AND s.seq = r.run_step_seq AND s.number = r.step_number)`, run); n != 3 {
			t.Errorf("%d rule runs with their step's run step, want 3", n)
		}
	})
}

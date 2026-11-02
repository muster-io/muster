// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package groups_test

import (
	"bytes"
	"context"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
)

// inputOf is the update of the Organization that keeps every setting of o.
func inputOf(o organization.Organization) organization.Input {
	return organization.Input{Name: o.Name, TimeZone: o.TimeZone, SeverityLabel: o.SeverityLabel,
		SeverityMapping: o.SeverityMapping, SeverityStyles: o.SeverityStyles, CriticalIsUrgent: o.CriticalIsUrgent,
		InstanceLabels: o.InstanceLabels, Retention: o.Retention, TOTPRequired: o.TOTPRequired,
		OIDCTokenGraceSeconds: int64(o.OIDCTokenGrace / time.Second)}
}

// TestIntegrationMessageSettingsUpdates is C-29.FR-1 to FR-3 on every supported PostgreSQL: an update of the
// Organization that changes its Severity label, Team label, Instance labels, time zone, start of the working day or
// first day of the week writes one open_group_updates row organization_message_settings in the update's transaction —
// the Team label also its row team_evaluation — and one that changes none of them, or changes them back while a row is
// still unclaimed, writes no further row. A worker claims the row and runs the re-render step, as the system, marked as
// a change of configuration and without a lifecycle event, on each open Alert Group of the Organization, and on no
// resolved one; it records no Timeline entry and deletes the row.
func TestIntegrationMessageSettingsUpdates(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		e.route(t, "db", "db", "alertname", "pod")
		const s1 = "2026-10-07T11:00:00Z"
		e.process(t, gk, alert("firing", s1, "alertname", "Slow", "team", "db", "pod", "p1"),
			alert("firing", s1, "alertname", "Slow", "team", "db", "pod", "p2"),
			alert("firing", s1, "alertname", "Slow", "team", "db", "pod", "p3"))
		e.process(t, gk, alert("resolved", s1, "alertname", "Slow", "team", "db", "pod", "p3"))
		open := []string{e.groupOf(t, "p1"), e.groupOf(t, "p2")}
		if v, err := e.groups.Get(ctx, e.groupOf(t, "p3")); err != nil || v.Status != groups.StatusResolved {
			t.Fatalf("the alert group of p3: %+v, %v", v, err)
		}
		var rendered []string
		e.groups.SetRerender(func(_ context.Context, _ groups.DBTX, r groups.Rendering) error {
			if r.Actor != groups.System || !r.Configuration || len(r.Events) != 0 {
				t.Errorf("a rendering as %+v, configuration %v, events %+v", r.Actor, r.Configuration, r.Events)
			}
			rendered = append(rendered, r.Group.PublicID)
			r.Changed()
			return nil
		})
		var log bytes.Buffer
		logger := logging.New(&log, logging.LevelInfo)
		worker := &groups.UpdateWorker{Service: e.groups, Batch: 1, Log: logger, Lease: db.Lease{Owner: "r1",
			Duration: groups.UpdateLease, Clocks: clock.Clocks{Business: e.clock, Real: clock.Real{}}}}
		svc := organization.NewService(e.orgID, organization.NewSettingsStore(e.d.Pool),
			audit.NewWriter(logger, e.clock), e.clock)
		update := func(t *testing.T, change func(in *organization.Input)) {
			t.Helper()
			o, err := svc.Get(ctx)
			if err != nil {
				t.Fatal(err)
			}
			in := inputOf(o)
			label := o.TeamLabel
			in.TeamLabel = &label
			if change != nil {
				change(&in)
			}
			if _, err := svc.Update(ctx, audit.System, audit.TransportAPI, netip.Addr{}, nil, in); err != nil {
				t.Fatal(err)
			}
		}
		rows := func(t *testing.T, kind string) int64 {
			t.Helper()
			return e.count(t, `SELECT count(*) FROM open_group_updates WHERE org_id = $1 AND kind = $2
				AND team_id IS NULL AND route_id IS NULL AND destination_id IS NULL AND leased_by IS NULL
				AND requested_at = $3`, e.orgID, kind, e.clock.Now())
		}
		entries := e.count(t, `SELECT count(*) FROM timeline_entries`)
		for _, c := range []struct {
			name   string
			change func(in *organization.Input)
		}{
			{"severity_label", func(in *organization.Input) { in.SeverityLabel = "priority" }},
			{"team_label", func(in *organization.Input) { in.TeamLabel = new("owner_team") }},
			{"instance_labels", func(in *organization.Input) { in.InstanceLabels = []string{"pod"} }},
			{"time_zone", func(in *organization.Input) { in.TimeZone = "Europe/Berlin" }},
			{"workday_start", func(in *organization.Input) { in.WorkdayStart = new("08:30") }},
			{"week_start", func(in *organization.Input) { in.WeekStart = new("sunday") }},
		} {
			e.clock.Advance(time.Minute)
			update(t, c.change)
			if n := rows(t, organization.KindMessageSettings); n != 1 {
				t.Errorf("%s: %d rows organization_message_settings", c.name, n)
			}
			evaluations := int64(0)
			if c.name == "team_label" {
				evaluations = 1
			}
			if n := rows(t, organization.KindTeamEvaluation); n != evaluations {
				t.Errorf("%s: %d rows team_evaluation", c.name, n)
			}
			// A second save of the same values writes no row.
			update(t, nil)
			if n := e.count(t, `SELECT count(*) FROM open_group_updates WHERE org_id = $1`, e.orgID); n != 1+evaluations {
				t.Errorf("%s: %d rows after the same values again", c.name, n)
			}
			rendered = nil
			if _, err := worker.Round(ctx); err != nil {
				t.Fatal(err)
			}
			slices.Sort(rendered)
			want := slices.Sorted(slices.Values(open))
			if !slices.Equal(rendered, want) {
				t.Errorf("%s: rendered %v, want %v", c.name, rendered, want)
			}
			if n := e.count(t, `SELECT count(*) FROM open_group_updates WHERE org_id = $1`, e.orgID); n != 0 ||
				!strings.Contains(log.String(), `"event":"open_group_update_done","kind":"organization_message_settings",`+
					`"alert_groups":2`) {
				t.Errorf("%s: %d rows left, log %s", c.name, n, log.String())
			}
			log.Reset()
		}
		if n := e.count(t, `SELECT count(*) FROM timeline_entries`); n != entries {
			t.Errorf("the re-renders recorded %d timeline entries", n-entries)
		}

		// A setting the Root messages do not show writes no row; a change of one while a row is unclaimed writes
		// none either, since that row renders the settings as they are when it runs.
		update(t, func(in *organization.Input) { in.Name = "Ops" })
		if n := e.count(t, `SELECT count(*) FROM open_group_updates WHERE org_id = $1`, e.orgID); n != 0 {
			t.Errorf("a new name wrote %d rows", n)
		}
		update(t, func(in *organization.Input) { in.TimeZone = "Asia/Tokyo" })
		update(t, func(in *organization.Input) { in.TimeZone = "UTC" })
		if n := e.count(t, `SELECT count(*) FROM open_group_updates WHERE org_id = $1`, e.orgID); n != 1 {
			t.Errorf("two changes before a claim wrote %d rows", n)
		}
	})
}

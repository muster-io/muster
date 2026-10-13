// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package organization_test

import (
	"bytes"
	"fmt"
	"net/netip"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
)

// auditPartition creates the partition of the Audit log that the updates at t0 write to.
func auditPartition(t *testing.T, d *db.DB) {
	t.Helper()
	if _, err := d.Pool.Exec(t.Context(), `CREATE TABLE audit_log_p202610 PARTITION OF audit_log
		FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2026-11-01 00:00:00+00')`); err != nil {
		t.Fatal(err)
	}
}

// inputOf is the update that changes nothing of o.
func inputOf(o organization.Organization) organization.Input {
	return organization.Input{Name: o.Name, TimeZone: o.TimeZone, SeverityLabel: o.SeverityLabel,
		SeverityMapping: slices.Clone(o.SeverityMapping), SeverityStyles: o.SeverityStyles,
		CriticalIsUrgent: o.CriticalIsUrgent, InstanceLabels: o.InstanceLabels, Retention: o.Retention,
		TOTPRequired: o.TOTPRequired, OIDCTokenGraceSeconds: int64(o.OIDCTokenGrace / time.Second),
		HeartbeatProxy: organization.ProxyInput{Enabled: o.OutgoingHeartbeat.Proxy.Enabled}}
}

// TestIntegrationSeverityValues is C-20.FR-1 and AC-4 on every supported PostgreSQL: the values of the severity label
// among the Alerts last seen within 14 days, the most frequent first, with the level of the current mapping; values
// seen earlier, Alerts without the label and empty values are left out. A new mapping and a new label apply at once.
func TestIntegrationSeverityValues(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		d, _, org := setupOrganization(t, s)
		auditPartition(t, d)
		var integration int64
		if err := d.Pool.QueryRow(ctx, `INSERT INTO integrations (org_id, public_id, name, connection_mode,
			static_labels, duplicate_window_seconds, heartbeat_enabled, heartbeat_timeout_seconds, heartbeat_state,
			created_at, updated_at) VALUES ($1, 'NTAAAAAAAAAAAA', 'lab', 'webhook_only', '{}', 45, false, 300,
			'not_configured', $2, $2) RETURNING id`, org, t0).Scan(&integration); err != nil {
			t.Fatal(err)
		}
		n := 0
		alert := func(labels string, seen time.Time) {
			t.Helper()
			n++
			if _, err := d.Pool.Exec(ctx, `INSERT INTO alerts (org_id, integration_id, fingerprint, labels,
				annotations, status, starts_at, fired_at, first_seen_at, last_seen_at, updated_at)
				VALUES ($1, $2, $3, $4, '{}', 'firing', $5, $5, $5, $5, $5)`, org, integration,
				fmt.Sprintf("%016x", n), labels, seen); err != nil {
				t.Fatal(err)
			}
		}
		day := 24 * time.Hour
		alert(`{"severity":"P5","host":"a"}`, t0.Add(-day))
		alert(`{"severity":"P5","host":"b"}`, t0)
		alert(`{"severity":"P1","level":"high"}`, t0.Add(-13*day))
		alert(`{"severity":"critical"}`, t0.Add(-2*day))
		alert(`{"severity":"P9"}`, t0.Add(-15*day))
		alert(`{"host":"c","level":"low"}`, t0)
		alert(`{"severity":""}`, t0)

		c := clock.NewManual(t0)
		svc := organization.NewService(org, organization.NewSettingsStore(d.Pool),
			audit.NewWriter(logging.New(&bytes.Buffer{}, logging.LevelInfo), c), c)
		values := func() organization.SeverityValues {
			t.Helper()
			v, err := svc.SeverityValues(ctx)
			if err != nil {
				t.Fatal(err)
			}
			return v
		}
		want := organization.SeverityValues{WindowDays: 14, Items: []organization.SeverityValue{
			{Value: "P5", Count: 2, Level: organization.SeverityWarning},
			{Value: "P1", Count: 1, Level: organization.SeverityWarning},
			{Value: "critical", Count: 1, Level: organization.SeverityCritical, Mapped: true},
		}}
		if got := values(); !reflect.DeepEqual(got, want) {
			t.Errorf("values = %+v", got)
		}
		update := func(change func(*organization.Input)) {
			t.Helper()
			o, err := svc.Get(ctx)
			if err != nil {
				t.Fatal(err)
			}
			in := inputOf(o)
			change(&in)
			if _, err := svc.Update(ctx, audit.System, audit.TransportAPI, netip.Addr{}, &o.Version, in); err != nil {
				t.Fatal(err)
			}
		}
		update(func(in *organization.Input) {
			in.SeverityMapping = append(in.SeverityMapping, organization.SeverityMapping{Value: "P1",
				Level: organization.SeverityCritical})
		})
		want.Items[1].Level, want.Items[1].Mapped = organization.SeverityCritical, true
		if got := values(); !reflect.DeepEqual(got, want) {
			t.Errorf("after mapping P1 = %+v", got)
		}
		// A day later P1 is outside the window.
		c.Advance(day + time.Second)
		if got := values(); len(got.Items) != 2 || got.Items[0].Value != "P5" || got.Items[1].Value != "critical" {
			t.Errorf("a day later = %+v", got)
		}
		update(func(in *organization.Input) { in.SeverityLabel = "level" })
		if got := values(); len(got.Items) != 1 || got.Items[0] != (organization.SeverityValue{Value: "low", Count: 1,
			Level: organization.SeverityWarning}) {
			t.Errorf("by the label level = %+v", got)
		}
	})
}

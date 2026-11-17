// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package escalation_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/escalation/dbgen"
)

// TestIntegrationStepDestination is the read of a webhook step's Destination (GetStepDestination, C-32.FR-14) on
// PostgreSQL: an outgoing webhook in events mode with its type and mode, one in the template mode, one soft-deleted
// reported as deleted, and a Destination of another Organization not found, as one that does not exist.
func TestIntegrationStepDestination(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		var other int64
		if err := e.d.Pool.QueryRow(ctx, `INSERT INTO organizations (public_id, name, time_zone, severity_label,
			severity_mapping, severity_styles, critical_is_urgent, instance_labels, retention_stored_snapshots_days,
			retention_alert_details_days, retention_alert_group_summaries_days, retention_audit_log_days,
			totp_required, oidc_token_grace_seconds, created_at, updated_at)
			SELECT 'RG0000000000ZZ', 'Other', time_zone, severity_label, severity_mapping, severity_styles,
				critical_is_urgent, instance_labels, retention_stored_snapshots_days, retention_alert_details_days,
				retention_alert_group_summaries_days, retention_audit_log_days, totp_required,
				oidc_token_grace_seconds, created_at, updated_at
			FROM organizations WHERE id = $1
			RETURNING id`, e.orgID).Scan(&other); err != nil {
			t.Fatal(err)
		}
		hook := func(org int64, publicID, name, mode string, deleted bool) int64 {
			t.Helper()
			var id int64
			if err := e.d.Pool.QueryRow(ctx, `INSERT INTO destinations (org_id, public_id, type, name, webhook_mode,
				webhook_events_config, webhook_template_config, signing_secret_ciphertext, signing_secret_key_id,
				signing_secret_updated_at, mentions, limiter_limit, limiter_per_seconds, health, created_at,
				updated_at, deleted_at)
				VALUES ($1, $2, 'webhook', $3, $4, CASE WHEN $4 <> 'template' THEN '{"url":"http://127.0.0.1/","headers":[]}'::jsonb END,
				CASE WHEN $4 <> 'events' THEN '{}'::jsonb END, '\x01', 'k1', $5, '{}', 5, 1, 'healthy', $5, $5,
				CASE WHEN $6 THEN $5::timestamptz END) RETURNING id`, org, publicID, name, mode, t0,
				deleted).Scan(&id); err != nil {
				t.Fatal(err)
			}
			return id
		}
		ops := hook(e.orgID, "DSAAAAAAAAAAWB", "ops-bot", "events", false)
		tmpl := hook(e.orgID, "DSAAAAAAAAAATM", "tmpl-bot", "template", false)
		gone := hook(e.orgID, "DSAAAAAAAAAAGN", "gone-bot", "both", true)
		foreign := hook(other, "DSAAAAAAAAAAFR", "foreign", "events", false)
		q := dbgen.New(e.d.Pool)
		read := func(id int64) (dbgen.GetStepDestinationRow, error) {
			return q.GetStepDestination(ctx, dbgen.GetStepDestinationParams{OrgID: e.orgID, ID: id})
		}
		if d, err := read(ops); err != nil || d.PublicID != "DSAAAAAAAAAAWB" || d.Name != "ops-bot" ||
			d.Type != "webhook" || d.WebhookMode.String != "events" || d.Deleted {
			t.Errorf("events mode: %+v %v", d, err)
		}
		if d, err := read(tmpl); err != nil || d.WebhookMode.String != "template" || d.Deleted {
			t.Errorf("template mode: %+v %v", d, err)
		}
		if d, err := read(gone); err != nil || !d.Deleted || d.WebhookMode.String != "both" {
			t.Errorf("soft-deleted: %+v %v", d, err)
		}
		if d, err := read(foreign); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("another organization's destination: %+v %v", d, err)
		}
		if d, err := read(999999); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("no destination: %+v %v", d, err)
		}
	})
}

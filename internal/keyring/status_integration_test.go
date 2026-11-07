// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package keyring_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db/dbtest"
	deliverydb "github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/routing"
)

// TestIntegrationStatusSupersededButtons covers C-20.FR-6 with C-22.FR-9 on PostgreSQL: a Bridge Root message signed
// by the old key and republished past its bridge's edit window after a rotation, with buttons the new key signs,
// leaves the older Root message's buttons working, so the old key stays needed while the Alert Group is open, and the
// check of the delivery worker agrees that it is still in use; a delivery deleted in the messenger, retired or
// withheld keeps no key needed; once the Alert Group resolves, the old key is removable.
func TestIntegrationStatusSupersededButtons(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		keys, err := keyring.New([][]byte{material('a'), material('b')}, false)
		if err != nil {
			t.Fatal(err)
		}
		d, org := migratedForRotation(t, s, keys)
		a, b := keyring.KeyID(material('a')), keyring.KeyID(material('b'))
		if err := routing.EnsureDefault(ctx, routing.NewStore(d.Pool), org, t0); err != nil {
			t.Fatal(err)
		}
		token, tokenKey, err := keys.Encrypt("connections.bot_token", []byte("bridge token"))
		if err != nil || tokenKey != a {
			t.Fatal(err)
		}

		// An open Alert Group whose Bridge Root message m1 carries buttons the key a signed.
		var group, delivery int64
		if err := d.Pool.QueryRow(ctx, `WITH c AS (INSERT INTO connections (org_id, public_id, type, name,
				bridge_base_url, bridge_event_delivery, bot_token_ciphertext, bot_token_key_id, bot_token_updated_at,
				limiter_limit, limiter_per_seconds, created_at, updated_at)
				VALUES ($1, 'CNAAAAAAAAAAB1', 'bridge', 'br', 'https://bridge.example.org', 'poll', $2, $3, $4, 10, 1,
				$4, $4) RETURNING id),
			ds AS (INSERT INTO destinations (org_id, public_id, type, name, connection_id, bridge_channel_id, mentions,
				limiter_limit, limiter_per_seconds, health, created_at, updated_at)
				SELECT $1, 'DSAAAAAAAAAAB1', 'bridge', 'alerts', c.id, 'ch-alerts', '{}', 10, 1, 'healthy', $4, $4
				FROM c RETURNING id),
			g AS (INSERT INTO alert_groups (org_id, public_id, number, route_id, group_key_labels, group_key_values,
				group_key_sha256, title, common_labels, common_annotations, integration_ids, status, severity_level,
				urgent, created_at, last_changed_at)
				SELECT $1, 'AGAAAAAAAAAAB1', 1, r.id, '{alertname}', '{}', sha256('superseded'::bytea), 'Latency', '{}',
				'{}', '{}', 'firing', 'critical', false, $4, $4
				FROM routes r WHERE r.org_id = $1 AND r.is_default RETURNING id)
			INSERT INTO deliveries (org_id, destination_id, alert_group_id, state, desired_version, desired_button_key_id,
				actual_version, actual_button_key_id, message_id, published_at, last_delivered_at, publications,
				next_attempt_at, created_at, updated_at)
			SELECT $1, ds.id, g.id, 'delivered', 1, $3, 1, $3, 'm1', $4, $4, 1, $4, $4, $4 FROM ds, g
			RETURNING alert_group_id, id`, org, token, a, t0).Scan(&group, &delivery); err != nil {
			t.Fatal(err)
		}

		rotation := keyring.Rotation{Store: keyring.NewRotationStore(d.Pool), Keys: keys,
			Audit: audit.NewWriter(logging.New(&bytes.Buffer{}, logging.LevelInfo), clock.NewManual(t0)),
			Log:   logging.New(&bytes.Buffer{}, logging.LevelInfo), Business: clock.NewManual(t0), Real: clock.Real{}}
		if err := d.WithMigrationLock(ctx, func(ctx context.Context) error {
			_, err := rotation.Activate(ctx, b, "ops")
			return err
		}); err != nil {
			t.Fatal(err)
		}

		deliveries := deliverydb.New(d.Pool)
		// check reads the status of the old key and whether the delivery worker sees it in use, which must agree.
		check := func(step string, state keyring.KeyState, messages int64) {
			t.Helper()
			st, err := keyring.ReadStatus(ctx, keyring.NewStatusQueries(d.Pool), keys.KeyIDs(), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			k, ok := st.Key(a)
			inUse, err := deliveries.ButtonKeyInUse(ctx, deliverydb.ButtonKeyInUseParams{OrgID: org, KeyID: a})
			if err != nil {
				t.Fatal(err)
			}
			if !ok || k.State != state || k.OpenMessages != messages || k.Removable == inUse ||
				k.EncryptedValues != 0 {
				t.Errorf("%s: old key %+v, in use %v", step, k, inUse)
			}
		}
		check("signed by the old key", keyring.KeyNeeded, 1)

		// The republication past the edit window, through the delivery worker's record: m2 with buttons b signed.
		if _, err := d.Pool.Exec(ctx, `UPDATE deliveries SET desired_version = 2, desired_button_key_id = $2,
			state = 'pending', past_edit_window = true, lease_owner = 'w', lease_until = $3 WHERE id = $1`,
			delivery, b, t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		at := t0.Add(11 * time.Minute)
		if n, err := deliveries.RecordRepublished(ctx, deliverydb.RecordRepublishedParams{OrgID: org, ID: delivery,
			Owner: "w", MessageID: "m2", Version: 2, Hash: []byte{2}, ActualStatus: pgtype.Text{String: "acknowledged",
				Valid: true}, ButtonKeyID: pgtype.Text{String: b, Valid: true}, Now: at}); err != nil || n != 1 {
			t.Fatalf("republished %d, %v", n, err)
		}
		if n := countRows(t, d, `SELECT count(*) FROM deliveries WHERE id = $1 AND state = 'delivered'
			AND actual_button_key_id = $2 AND superseded_button_key_ids = ARRAY[$3::text]
			AND superseded_message_ids = '{m1}'`, delivery, b, a); n != 1 {
			t.Fatalf("the republication was not recorded")
		}
		check("superseded by a republication", keyring.KeyNeeded, 1)
		st, err := keyring.ReadStatus(ctx, keyring.NewStatusQueries(d.Pool), keys.KeyIDs(), time.Now())
		if k, ok := st.Key(b); err != nil || !ok || k.State != keyring.KeyActive || k.OpenMessages != 1 {
			t.Errorf("new key %+v, %v", k, err)
		}

		// A delivery that the re-render never re-signs keeps no key needed.
		for _, state := range []string{"deleted_in_messenger", "retired", "withheld"} {
			if _, err := d.Pool.Exec(ctx, `UPDATE deliveries SET state = $2 WHERE id = $1`, delivery,
				state); err != nil {
				t.Fatal(err)
			}
			check(state, keyring.KeyRemovable, 0)
		}
		if _, err := d.Pool.Exec(ctx, `UPDATE deliveries SET state = 'delivered' WHERE id = $1`, delivery); err != nil {
			t.Fatal(err)
		}
		check("delivered again", keyring.KeyNeeded, 1)

		// Once the Alert Group resolves, nothing depends on the old key.
		if _, err := d.Pool.Exec(ctx, `UPDATE alert_groups SET status = 'resolved', resolved_at = $2,
			resolved_by_kind = 'system', resolve_reason = 'resolved' WHERE id = $1`, group, at); err != nil {
			t.Fatal(err)
		}
		check("resolved", keyring.KeyRemovable, 0)
	})
}

// TestIntegrationStatusGatewayCalls covers the Note of S-173 on PostgreSQL: a call through a Notification gateway whose
// action data the old key signed keeps that key needed while the call is being sent, and once accepted while any of
// its Alert Groups is open; a call accepted whose Alert Groups all resolved, one Not delivered, and an Alert Group
// dropped from a call before its first attempt keep nothing needed. A call without a key id, which a replica of an
// earlier version started, counts for no key.
func TestIntegrationStatusGatewayCalls(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		keys, err := keyring.New([][]byte{material('a'), material('b')}, false)
		if err != nil {
			t.Fatal(err)
		}
		d, org := migratedForRotation(t, s, keys)
		a, b := keyring.KeyID(material('a')), keyring.KeyID(material('b'))
		if err := routing.EnsureDefault(ctx, routing.NewStore(d.Pool), org, t0); err != nil {
			t.Fatal(err)
		}
		token, _, err := keys.Encrypt("connections.bot_token", []byte("gateway token"))
		if err != nil {
			t.Fatal(err)
		}
		var call, group int64
		if err := d.Pool.QueryRow(ctx, `WITH c AS (INSERT INTO connections (org_id, public_id, type, name,
				bridge_base_url, bridge_event_delivery, bot_token_ciphertext, bot_token_key_id, bot_token_updated_at,
				limiter_limit, limiter_per_seconds, created_at, updated_at)
				VALUES ($1, 'CNAAAAAAAAAAG1', 'bridge', 'gw', 'https://gateway.example.org', 'poll', $2, $3, $4, 10, 1,
				$4, $4) RETURNING id),
			u AS (INSERT INTO users (org_id, public_id, login, name, role, source, status, created_at, updated_at)
				VALUES ($1, 'SRAAAAAAAAAAP1', 'petr', 'Petr', 'responder', 'local', 'active', $4, $4) RETURNING id),
			g AS (INSERT INTO alert_groups (org_id, public_id, number, route_id, group_key_labels, group_key_values,
				group_key_sha256, title, common_labels, common_annotations, integration_ids, status, severity_level,
				urgent, created_at, last_changed_at)
				SELECT $1, 'AGAAAAAAAAAAG1', 1, r.id, '{alertname}', '{}', sha256('call'::bytea), 'Disk', '{}', '{}',
				'{}', 'firing', 'critical', false, $4, $4
				FROM routes r WHERE r.org_id = $1 AND r.is_default RETURNING id),
			k AS (INSERT INTO gateway_calls (org_id, public_id, connection_id, user_id, state, language, text,
				first_attempt_at, next_attempt_at, action_data, button_key_id, created_at)
				SELECT $1, 'CAAAAAAAAAAAG1', c.id, u.id, 'sending', 'en', 'Muster: 1 Alert Group needs you.', $4, $4,
				'data', $3, $4
				FROM c, u RETURNING id)
			INSERT INTO gateway_call_items (org_id, call_id, alert_group_id, queued_at)
			SELECT $1, k.id, g.id, $4 FROM k, g
			RETURNING call_id, alert_group_id`, org, token, a, t0).Scan(&call, &group); err != nil {
			t.Fatal(err)
		}
		// The call of an earlier version, without a key id, about the same Alert Group.
		if _, err := d.Pool.Exec(ctx, `INSERT INTO gateway_calls (org_id, public_id, connection_id, user_id, state,
			language, text, first_attempt_at, next_attempt_at, created_at)
			SELECT org_id, 'CAAAAAAAAAAAG2', connection_id, user_id, 'sending', 'en', 'x', $2, $2, $2
			FROM gateway_calls WHERE id = $1`, call, t0); err != nil {
			t.Fatal(err)
		}
		rotation := keyring.Rotation{Store: keyring.NewRotationStore(d.Pool), Keys: keys,
			Audit: audit.NewWriter(logging.New(&bytes.Buffer{}, logging.LevelInfo), clock.NewManual(t0)),
			Log:   logging.New(&bytes.Buffer{}, logging.LevelInfo), Business: clock.NewManual(t0), Real: clock.Real{}}
		if err := d.WithMigrationLock(ctx, func(ctx context.Context) error {
			_, err := rotation.Activate(ctx, b, "ops")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		check := func(step string, state keyring.KeyState, n int64) {
			t.Helper()
			st, err := keyring.ReadStatus(ctx, keyring.NewStatusQueries(d.Pool), keys.KeyIDs(), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if k, ok := st.Key(a); !ok || k.State != state || k.OpenMessages != n {
				t.Errorf("%s: old key %+v", step, k)
			}
		}
		set := func(sql string, args ...any) {
			t.Helper()
			if _, err := d.Pool.Exec(ctx, sql, args...); err != nil {
				t.Fatal(err)
			}
		}
		check("being sent", keyring.KeyNeeded, 1)
		set(`UPDATE gateway_calls SET state = 'accepted', text = NULL, action_data = NULL, ended_at = $2,
			next_attempt_at = NULL WHERE id = $1`, call, t0)
		check("accepted, its Alert Group open", keyring.KeyNeeded, 1)
		set(`UPDATE gateway_call_items SET dropped_at = $2 WHERE call_id = $1`, call, t0)
		check("its Alert Group dropped before the first attempt", keyring.KeyRemovable, 0)
		set(`UPDATE gateway_call_items SET dropped_at = NULL WHERE call_id = $1`, call)
		set(`UPDATE alert_groups SET status = 'resolved', resolved_at = $2, resolved_by_kind = 'system',
			resolve_reason = 'resolved' WHERE id = $1`, group, t0)
		check("accepted, its Alert Group resolved", keyring.KeyRemovable, 0)
		set(`UPDATE alert_groups SET status = 'firing', resolved_at = NULL, resolved_by_kind = NULL,
			resolve_reason = NULL WHERE id = $1`, group)
		set(`UPDATE gateway_calls SET state = 'not_delivered', reason = 'call_refused' WHERE id = $1`, call)
		check("not delivered", keyring.KeyRemovable, 0)
	})
}

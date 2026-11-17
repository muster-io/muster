// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package delivery_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/db/dbtest"
	deliverydb "github.com/muster-io/muster/internal/delivery/dbgen"
)

// eventsHook inserts an outgoing webhook Destination in the events mode on no Route, soft-deleted when deleted, and
// returns its id.
func (l *live) eventsHook(t *testing.T, publicID, name string, deleted bool) int64 {
	t.Helper()
	var id int64
	if err := l.d.Pool.QueryRow(t.Context(), `INSERT INTO destinations (org_id, public_id, type, name, webhook_mode,
		webhook_events_config, signing_secret_ciphertext, signing_secret_key_id, signing_secret_updated_at,
		mentions, limiter_limit, limiter_per_seconds, health, created_at, updated_at, deleted_at)
		VALUES ($1, $2, 'webhook', $3, 'events', '{"url":"http://127.0.0.1/","headers":[]}', '\x01', 'k1', $4, '{}',
		1000, 1, 'healthy', $4, $4, CASE WHEN $5 THEN $4::timestamptz END) RETURNING id`, l.orgID, publicID, name,
		l.business.Now(), deleted).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// groupID is the internal id of the Alert Group public.
func (l *live) groupID(t *testing.T, public string) int64 {
	t.Helper()
	return l.count(t, `SELECT id FROM alert_groups WHERE public_id = $1`, public)
}

// TestInsertWebhookEventOnPostgreSQL is InsertWebhookEvent as PostgreSQL runs it (C-15.FR-2, C-32.FR-13): an event
// for an events-mode Destination is queued; the same sequence again queues nothing; and nothing is queued for a
// soft-deleted Destination, as one whose deletion committed while a webhook step read it.
func TestInsertWebhookEventOnPostgreSQL(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		l.fresh(t, 1000, 1)
		l.fire(t, "ins", "ins/a")
		g := l.groupID(t, l.group(t, "ins"))
		ops := l.eventsHook(t, "DSAAAAAAAAAAWB", "ops-bot", false)
		gone := l.eventsHook(t, "DSAAAAAAAAAAGN", "gone-bot", true)
		q := deliverydb.New(l.d.Pool)
		insert := func(dest int64, seq int64, id string) {
			t.Helper()
			if err := q.InsertWebhookEvent(t.Context(), deliverydb.InsertWebhookEventParams{OrgID: l.orgID,
				DestinationID: dest, AlertGroupID: g, Sequence: seq, WebhookID: id, Event: "escalation_webhook",
				Now: l.business.Now(), Body: []byte(`{"version":1}`),
				ReceivedAt: pgtype.Timestamptz{}}); err != nil {
				t.Fatal(err)
			}
		}
		events := func(dest int64) int64 {
			return l.count(t, `SELECT count(*) FROM webhook_events WHERE destination_id = $1`, dest)
		}
		insert(ops, 7, "msg_first")
		if n := events(ops); n != 1 {
			t.Fatalf("%d events for ops-bot", n)
		}
		insert(ops, 7, "msg_again")
		if n := l.count(t, `SELECT count(*) FROM webhook_events WHERE destination_id = $1 AND webhook_id = 'msg_first'
			AND state = 'pending'`, ops); n != 1 || events(ops) != 1 {
			t.Errorf("the same sequence again: %d events", events(ops))
		}
		insert(gone, 7, "msg_gone")
		if n := events(gone); n != 0 {
			t.Errorf("%d events for a deleted destination", n)
		}
	})
}

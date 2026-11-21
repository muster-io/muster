// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package delivery_test

import (
	"testing"
	"time"

	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/delivery"
)

// TestPruneDeliveriesLive is #660 against PostgreSQL 14 and 17: once retention.alert_details passed after an Alert
// Group resolved, the retention removes its deliveries with their Thread replies, more than one batch of them, and
// keeps those of an Alert Group resolved more recently or still open, and every delivery that is pending, under a lease
// still running on the real clock, has a Thread reply still to send or changed after the cutoff; a lease that ran out
// keeps nothing. A second run removes nothing more; with the delivery changed 80 days ago then 11 days older, a third
// removes it.
func TestPruneDeliveriesLive(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		l.exec(t, `UPDATE organizations SET retention_alert_details_days = 90 WHERE id = $1`, l.orgID)
		// The retention compares leases with the real clock of the test, never with the business cutoff.
		svc := delivery.New(delivery.Config{OrgID: l.orgID, Store: delivery.NewStore(l.d.Pool, l.d.Pool),
			Business: l.business, Real: l.real, Log: l.logger})

		// An Alert Group delivered, resolved and given its final edit by the worker.
		l.fresh(t, 1000, 1)
		l.fire(t, "retained", "Retained/a")
		gid := l.group(t, "Retained")
		for range 3 {
			l.business.Advance(time.Minute)
			l.round(t, l.a)
		}
		l.resolve(t, gid)
		for range 3 {
			l.business.Advance(time.Minute)
			l.round(t, l.a)
		}
		ofGroup := `SELECT count(*) FROM deliveries d JOIN alert_groups g ON g.id = d.alert_group_id
			WHERE g.public_id = $1`
		if n := l.count(t, ofGroup+` AND d.state = 'delivered'`, gid); n != 1 {
			t.Fatalf("%d delivered deliveries of the resolved alert group", n)
		}
		if n, err := svc.PruneDeliveries(t.Context(), l.business.Now()); err != nil || n != 0 {
			t.Fatalf("pruned %d before retention.alert_details passed, %v", n, err)
		}
		l.business.Advance(91 * 24 * time.Hour)
		now := l.business.Now()

		// More Alert Groups resolved 100 days ago than one batch removes, two of them delivered to both Destinations;
		// and the ones that keep their deliveries, named by their numbers.
		const bulk = delivery.RetentionBatch + 5
		l.exec(t, `INSERT INTO alert_groups (org_id, public_id, number, route_id, group_key_labels, group_key_values,
			group_key_sha256, title, summary, common_labels, common_annotations, integration_ids, status,
			severity_level, urgent, resolved_at, resolved_by_kind, resolve_reason, created_at, last_changed_at)
			SELECT $1, 'AGRT' || lpad(upper(to_hex(n)), 10, '0'), 100000 + n, $2, '{alertname}', '{}',
			       sha256(('ret' || n)::bytea), 'Bulk ' || n, '', '{}', '{}', ARRAY[$3::bigint],
			       CASE WHEN n = 6 THEN 'firing' ELSE 'resolved' END, 'warning', false,
			       CASE WHEN n = 6 THEN NULL
			            WHEN n = 5 THEN $4::timestamptz - interval '10 days'
			            ELSE $4::timestamptz - interval '100 days' END,
			       CASE WHEN n = 6 THEN NULL ELSE 'system' END, CASE WHEN n = 6 THEN NULL ELSE 'resolved' END,
			       $4::timestamptz - interval '200 days', $4::timestamptz - interval '100 days'
			FROM generate_series(1, $5::bigint + 6) AS n`, l.orgID, l.routeID, l.intID, now, bulk)
		// 100002 is under a lease that runs another minute on the real clock, which keeps it; the lease of 100009 ran
		// out a minute ago, as one kept by a withheld Publication or by a replica that stopped, and holds nothing.
		l.exec(t, `INSERT INTO deliveries (org_id, destination_id, alert_group_id, state, next_attempt_at,
			lease_owner, lease_until, created_at, updated_at)
			SELECT $1, $2, g.id, CASE WHEN g.number = 100001 THEN 'pending' ELSE 'delivered' END,
			       $3::timestamptz - interval '100 days',
			       CASE WHEN g.number = 100002 THEN 'r1' WHEN g.number = 100009 THEN 'r9' END,
			       CASE WHEN g.number = 100002 THEN $4::timestamptz + interval '1 minute'
			            WHEN g.number = 100009 THEN $4::timestamptz - interval '1 minute' END,
			       $3::timestamptz - interval '200 days', $3::timestamptz - interval '100 days'
			FROM alert_groups g
			WHERE g.org_id = $1 AND g.number > 100000`, l.orgID, l.dests[0], now, l.real.Now())
		l.exec(t, `INSERT INTO deliveries (org_id, destination_id, alert_group_id, state, next_attempt_at, created_at,
			updated_at)
			SELECT $1, $2, g.id, 'delivered', $3, $3, $3
			FROM alert_groups g
			WHERE g.org_id = $1 AND g.number IN (100004, 100007)`, l.orgID, l.dests[1], now.AddDate(0, 0, -100))
		// A delivery last changed 80 days ago waits until that change is retention.alert_details old.
		l.exec(t, `INSERT INTO deliveries (org_id, destination_id, alert_group_id, state, next_attempt_at, created_at,
			updated_at)
			SELECT $1, $2, g.id, 'retired', $3, $3, $4
			FROM alert_groups g
			WHERE g.org_id = $1 AND g.number = 100008`, l.orgID, l.dests[1], now.AddDate(0, 0, -100),
			now.AddDate(0, 0, -80))
		// A Thread reply still collecting keeps its delivery; a sent one goes with it.
		l.exec(t, `INSERT INTO thread_replies (org_id, delivery_id, alert_group_id, destination_id, event, event_seqs,
			loudness, state, next_attempt_at, created_at)
			SELECT d.org_id, d.id, d.alert_group_id, d.destination_id, 'alerts_added', '{1}', 'quiet',
			       CASE WHEN g.number = 100003 THEN 'collecting' ELSE 'sent' END, $3, $3
			FROM deliveries d
			JOIN alert_groups g ON g.id = d.alert_group_id
			WHERE d.org_id = $1 AND d.destination_id = $2 AND g.number IN (100003, 100004)`, l.orgID, l.dests[0],
			now.AddDate(0, 0, -100))

		kept := `SELECT count(*) FROM deliveries d JOIN alert_groups g ON g.id = d.alert_group_id
			WHERE g.number IN (100001, 100002, 100003, 100005, 100006) OR (g.number = 100008 AND d.state = 'retired')`
		n, err := svc.PruneDeliveries(t.Context(), now)
		// Every bulk delivery to the first Destination but the 5 to keep, the 2 to the second, and the worker's.
		if want := int64(bulk + 6 - 5 + 2 + 1); err != nil || n != want {
			t.Errorf("pruned %d of %d, %v", n, want, err)
		}
		if c := l.count(t, `SELECT count(*) FROM deliveries d JOIN alert_groups g ON g.id = d.alert_group_id
			WHERE g.number = 100009`); c != 0 {
			t.Errorf("the delivery under a lease that ran out is left")
		}
		if c := l.count(t, ofGroup, gid); c != 0 {
			t.Errorf("%d deliveries of the alert group resolved by the worker left", c)
		}
		if c := l.count(t, kept); c != 6 {
			t.Errorf("%d of the 6 deliveries to keep left", c)
		}
		if c := l.count(t, `SELECT count(*) FROM deliveries`); c != 6 {
			t.Errorf("%d deliveries left", c)
		}
		if c := l.count(t, `SELECT count(*) FROM thread_replies WHERE state = 'collecting'`); c != 1 {
			t.Errorf("%d collecting replies left", c)
		}
		if c := l.count(t, `SELECT count(*) FROM thread_replies WHERE state = 'sent' AND delivery_id NOT IN
			(SELECT id FROM deliveries)`); c != 0 {
			t.Errorf("%d replies of deleted deliveries left", c)
		}
		if c := l.count(t, `SELECT count(*) FROM alert_groups WHERE number > 100000`); c != bulk+6 {
			t.Errorf("%d summary rows left", c)
		}
		if n, err := svc.PruneDeliveries(t.Context(), now); err != nil || n != 0 {
			t.Errorf("second run pruned %d, %v", n, err)
		}
		if n, err := svc.PruneDeliveries(t.Context(), now.AddDate(0, 0, 11)); err != nil || n != 1 {
			t.Errorf("11 days later pruned %d, %v", n, err)
		}
	})
}

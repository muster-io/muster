// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package delivery_test

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/dbgen"
)

// TestIntegrationTakeTokensDueTimes is C-11.FR-3 on PostgreSQL for #547: a refused TakeTokens says when each empty
// bucket holds a token — the Destination's, the Connection's or both — and LimiterDues reads the same times without a
// take, by the current limits: after a raise, the token is due at once.
func TestIntegrationTakeTokensDueTimes(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		dest := l.dests[0]
		q := dbgen.New(l.d.Pool)
		now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		d, c := pgtype.Int8{Int64: dest, Valid: true}, pgtype.Int8{Int64: l.conn, Valid: true}
		take := func(at time.Time) dbgen.TakeTokensRow {
			t.Helper()
			if err := q.EnsureBuckets(t.Context(), dbgen.EnsureBucketsParams{OrgID: l.orgID, Now: at,
				DestinationID: d, ConnectionID: c}); err != nil {
				t.Fatal(err)
			}
			r, err := q.TakeTokens(t.Context(), dbgen.TakeTokensParams{Now: at, OrgID: l.orgID, DestinationID: d,
				ConnectionID: c})
			if err != nil {
				t.Fatal(err)
			}
			return r
		}
		dues := func(at time.Time) map[string]time.Time {
			t.Helper()
			rows, err := q.LimiterDues(t.Context(), dbgen.LimiterDuesParams{OrgID: l.orgID, Now: at,
				DestinationIds: []int64{dest}, ConnectionIds: []int64{l.conn}})
			if err != nil {
				t.Fatal(err)
			}
			out := map[string]time.Time{}
			for _, r := range rows {
				out[r.Kind] = r.Due.UTC()
			}
			return out
		}
		set := func(dl, dp, cl, cp int) {
			t.Helper()
			l.exec(t, `UPDATE destinations SET limiter_limit = $1, limiter_per_seconds = $2 WHERE id = $3`, dl, dp,
				dest)
			l.exec(t, `UPDATE connections SET limiter_limit = $1, limiter_per_seconds = $2 WHERE id = $3`, cl, cp,
				l.conn)
		}
		zero := func(at time.Time) bool { return at.Year() <= 1 }
		set(1, 2, 1, 1)
		l.exec(t, `DELETE FROM rate_limit_buckets`)
		if got := dues(now); len(got) != 0 {
			t.Fatalf("LimiterDues without buckets %v", got)
		}
		take(now)
		r := take(now)
		if r.Taken || !r.Due.Equal(now.Add(2*time.Second)) || !r.DestinationDue.Equal(now.Add(2*time.Second)) ||
			!r.ConnectionDue.Equal(now.Add(time.Second)) {
			t.Errorf("both empty: %+v", r)
		}
		if got := dues(now); !got["destination"].Equal(now.Add(2*time.Second)) ||
			!got["connection"].Equal(now.Add(time.Second)) {
			t.Errorf("LimiterDues with both empty: %v", got)
		}
		set(100, 1, 1, 1)
		if got := dues(now.Add(500 * time.Millisecond)); !got["destination"].Equal(now.Add(500*time.Millisecond)) ||
			!got["connection"].Equal(now.Add(time.Second)) {
			t.Errorf("LimiterDues after the Destination's limit was raised: %v", got)
		}
		l.exec(t, `DELETE FROM rate_limit_buckets`)
		take(now)
		if r = take(now); r.Taken || !zero(r.DestinationDue) || !r.ConnectionDue.Equal(now.Add(time.Second)) {
			t.Errorf("the Connection empty: %+v", r)
		}
		set(1, 1, 100, 1)
		l.exec(t, `DELETE FROM rate_limit_buckets`)
		take(now)
		if r = take(now); r.Taken || !zero(r.ConnectionDue) || !r.DestinationDue.Equal(now.Add(time.Second)) {
			t.Errorf("the Destination empty: %+v", r)
		}
		if r = take(now.Add(5 * time.Second)); !r.Taken || !zero(r.Due) || !zero(r.DestinationDue) ||
			!zero(r.ConnectionDue) {
			t.Errorf("taken: %+v", r)
		}
	})
}

// TestIntegrationSkipsOfRefusedLimiters is C-11.FR-3 on PostgreSQL for #547: the claims of deliveries and of Thread
// replies and NextDeliveryWork leave out the work of the limiters the memo skips — the deliveries and Thread replies of
// a skipped Destination or of any Destination of a skipped Connection, and the events of a skipped outgoing webhook.
func TestIntegrationSkipsOfRefusedLimiters(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		ctx := t.Context()
		l.fresh(t, 1000, 1)
		dest := l.dests[0]
		var hook int64
		if err := l.d.Pool.QueryRow(ctx, `INSERT INTO destinations (org_id, public_id, type, name, webhook_mode,
			webhook_events_config, signing_secret_ciphertext, signing_secret_key_id, signing_secret_updated_at,
			mentions, limiter_limit, limiter_per_seconds, health, created_at, updated_at)
			VALUES ($1, 'DSAAAAAAAAAAWS', 'webhook', 'hook', 'events', '{"url":"http://127.0.0.1/","headers":[]}',
			'\x01', 'k1', $2, '{}', 1000, 1, 'healthy', $2, $2) RETURNING id`, l.orgID,
			l.business.Now()).Scan(&hook); err != nil {
			t.Fatal(err)
		}
		l.attach(t, dest, hook)
		svc := delivery.New(delivery.Config{OrgID: l.orgID, Store: delivery.NewStore(l.d.Pool, l.d.Pool),
			Business: l.business, Real: l.real, Log: l.logger, Renderer: delivery.MessageRenderer{Renderer: l.renderer},
			Bodies: &stubBodies{}})
		l.groups.SetRerender(svc.Enqueue)
		defer l.groups.SetRerender(l.svc.Enqueue)
		w := l.worker("skips")
		w.Events = &fakeSender{}
		l.fire(t, "skips", "Skip/a")
		l.round(t, w)
		l.business.Advance(time.Minute)
		l.fire(t, "skips", "Skip/a", "Skip/b")
		if n := l.count(t, `SELECT count(*) FROM thread_replies WHERE destination_id = $1
			AND state IN ('collecting', 'pending')`, dest); n != 1 {
			t.Fatalf("%d Thread replies waiting, want 1\n%s", n, l.log)
		}
		if n := l.count(t, `SELECT count(*) FROM webhook_events WHERE destination_id = $1 AND state = 'pending'`,
			hook); n == 0 {
			t.Fatalf("no webhook event waiting\n%s", l.log)
		}
		// One pending delivery is left, the edit of the channel's Root message that the new Alert made.
		l.exec(t, `UPDATE deliveries SET state = 'delivered' WHERE state = 'pending' AND id <> (SELECT min(id)
			FROM deliveries WHERE state = 'pending' AND destination_id = $1 AND NOT urgent AND NOT configuration_only)`,
			dest)
		if n := l.count(t, `SELECT count(*) FROM deliveries WHERE state = 'pending' AND destination_id = $1`,
			dest); n != 1 {
			t.Fatalf("%d pending deliveries in the channel, want 1\n%s", n, l.log)
		}
		q := dbgen.New(l.d.Pool)
		free := func(dests, conns []int64) time.Time {
			t.Helper()
			r, err := q.NextDeliveryWork(ctx, dbgen.NextDeliveryWorkParams{OrgID: l.orgID, Now: l.real.Now(),
				SkipDestinations: dests, SkipConnections: conns})
			if err != nil {
				t.Fatal(err)
			}
			return r.FreeAt
		}
		if at := free(nil, nil); at.Year() <= 1 {
			t.Fatal("no work without skips")
		}
		for _, c := range []struct {
			name         string
			dests, conns []int64
			want         bool
		}{
			{"the webhook skipped", []int64{hook}, nil, true},
			{"the channel skipped, the webhook not", []int64{dest}, nil, true},
			{"the Connection skipped, the webhook not", nil, []int64{l.conn}, true},
			{"both skipped", []int64{dest, hook}, nil, false},
			{"the webhook and the Connection skipped", []int64{hook}, []int64{l.conn}, false},
		} {
			if at := free(c.dests, c.conns); (at.Year() > 1) != c.want {
				t.Errorf("%s: work at %v, want some %v", c.name, at, c.want)
			}
		}
		claim := func(dests, conns []int64) int {
			t.Helper()
			rows, err := q.ClaimDueReplies(ctx, dbgen.ClaimDueRepliesParams{OrgID: l.orgID, Owner: "skips",
				LeaseUntil: l.real.Now().Add(time.Minute), Due: l.business.Now(), Now: l.real.Now(),
				SkipDestinations: dests, SkipConnections: conns, Lim: 10})
			if err != nil {
				t.Fatal(err)
			}
			return len(rows)
		}
		if n, m := claim([]int64{dest}, nil), claim(nil, []int64{l.conn}); n != 0 || m != 0 {
			t.Errorf("claimed %d Thread replies with the channel skipped and %d with its Connection skipped", n, m)
		}
		if n := claim(nil, nil); n != 1 {
			t.Errorf("claimed %d Thread replies without skips, want 1", n)
		}
		claimDeliveries := func(dests, conns []int64) int {
			t.Helper()
			rows, err := q.ClaimDueDeliveries(ctx, dbgen.ClaimDueDeliveriesParams{OrgID: l.orgID, Owner: "skips",
				LeaseUntil: l.real.Now().Add(time.Minute), Due: l.business.Now(), Now: l.real.Now(),
				SkipDestinations: dests, SkipConnections: conns, Lim: 10})
			if err != nil {
				t.Fatal(err)
			}
			return len(rows)
		}
		if n, m := claimDeliveries([]int64{dest}, nil), claimDeliveries(nil, []int64{l.conn}); n != 0 || m != 0 {
			t.Errorf("claimed %d deliveries with the channel skipped and %d with its Connection skipped", n, m)
		}
		if n := claimDeliveries(nil, nil); n != 1 {
			t.Errorf("claimed %d deliveries without skips, want 1", n)
		}
	})
}

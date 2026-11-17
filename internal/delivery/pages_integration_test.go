// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package delivery_test

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/db/dbtest"
	deliverydb "github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/routing"
)

// TestIntegrationPageOwners is C-32.FR-8 for the owners of a Destination on PostgreSQL: ListPageRoots returns the
// entries that EnsureDelivery recorded on the delivery, and a source owns the Destination only by its own entry among
// them — the Route's chain Pages where the Route's entry reached the delivery, and nowhere once the entries recorded
// there are only another source's, though the Route still names the Destination.
func TestIntegrationPageOwners(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		ctx := t.Context()
		l.fire(t, "owners", "Owners/a")
		l.round(t, l.a)
		gid := l.group(t, "Owners")
		var id, number int64
		if err := l.d.Pool.QueryRow(ctx, `SELECT id, number FROM alert_groups WHERE public_id = $1`, gid).Scan(&id,
			&number); err != nil {
			t.Fatal(err)
		}
		rows, err := deliverydb.New(l.d.Pool).ListPageRoots(ctx, deliverydb.ListPageRootsParams{OrgID: l.orgID,
			AlertGroupID: id})
		if err != nil || len(rows) != 1 {
			t.Fatalf("roots %+v: %v", rows, err)
		}
		var entries []routing.Reaching
		if err := json.Unmarshal(rows[0].Entries, &entries); err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Kind != routing.EntryRoute {
			t.Fatalf("the entries of the root %s, want the Route's", rows[0].Entries)
		}
		g := &groups.Group{ID: id, PublicID: gid, Number: number, RouteID: l.routeID, Title: "Owners",
			Status: groups.StatusFiring}
		targets := func() []int64 {
			t.Helper()
			var out []int64
			if err := pgxTx(t, l, func(tx groups.DBTX) error {
				got, err := l.svc.PageTargets(ctx, tx, g, groups.PageRequest{Sources: []groups.PageSource{
					{Kind: groups.PageSourceRoute}}})
				for _, x := range got {
					out = append(out, x.DestinationID)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			return out
		}
		if got := targets(); !slices.Equal(got, []int64{l.dests[0]}) {
			t.Errorf("the Route's chain Pages in %v, want %d", got, l.dests[0])
		}
		l.exec(t, `UPDATE deliveries SET entries = '[{"kind":"team","team_id":7,"severity_condition":"any"}]'
			WHERE alert_group_id = $1`, id)
		if got := targets(); len(got) != 0 {
			t.Errorf("the Route's chain Pages in %v by another source's entry", got)
		}
	})
}

// TestIntegrationPageRoots is C-32.FR-8 for ListPageRoots on PostgreSQL: a Destination whose first Publication of the
// Alert Group is still on its way — its delivery pending, without published_at and without a message — counts as one
// with the Root message, as the chains start with the first Publication elsewhere; once published it still counts,
// by its message.
func TestIntegrationPageRoots(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		ctx := t.Context()
		l.fire(t, "roots", "Roots/a")
		var group int64
		if err := l.d.Pool.QueryRow(ctx, `SELECT id FROM alert_groups WHERE public_id = $1`,
			l.group(t, "Roots")).Scan(&group); err != nil {
			t.Fatal(err)
		}
		roots := func() []int64 {
			t.Helper()
			rows, err := deliverydb.New(l.d.Pool).ListPageRoots(ctx, deliverydb.ListPageRootsParams{OrgID: l.orgID,
				AlertGroupID: group})
			if err != nil {
				t.Fatal(err)
			}
			var out []int64
			for _, r := range rows {
				out = append(out, r.ID)
			}
			return out
		}
		if n := l.count(t, `SELECT count(*) FROM deliveries WHERE alert_group_id = $1 AND destination_id = $2
			AND state = 'pending' AND published_at IS NULL AND message_id IS NULL`, group, l.dests[0]); n != 1 {
			t.Fatalf("%d deliveries on their way", n)
		}
		if got := roots(); !slices.Equal(got, []int64{l.dests[0]}) {
			t.Errorf("on its way: %v, want %d", got, l.dests[0])
		}
		l.round(t, l.a)
		if n := l.count(t, `SELECT count(*) FROM deliveries WHERE alert_group_id = $1 AND destination_id = $2
			AND message_id IS NOT NULL`, group, l.dests[0]); n != 1 {
			t.Fatalf("%d deliveries published", n)
		}
		if got := roots(); !slices.Equal(got, []int64{l.dests[0]}) {
			t.Errorf("published: %v, want %d", got, l.dests[0])
		}
	})
}

// TestIntegrationEndedPages is C-32.FR-10 for ListEndedPages on PostgreSQL: a pending Page with a Thread reply still
// to be sent, pending or collecting, is not listed; once its replies all ended it is, in id order after @after; an
// ended Page is not.
func TestIntegrationEndedPages(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		ctx := t.Context()
		l.fire(t, "ended", "Ended/a")
		l.round(t, l.a)
		var group, del int64
		if err := l.d.Pool.QueryRow(ctx, `SELECT a.id, x.id FROM alert_groups a JOIN deliveries x
			ON x.alert_group_id = a.id WHERE a.public_id = $1 AND x.destination_id = $2`, l.group(t, "Ended"),
			l.dests[0]).Scan(&group, &del); err != nil {
			t.Fatal(err)
		}
		page := func(state string) int64 {
			t.Helper()
			var id int64
			if err := l.d.Pool.QueryRow(ctx, `INSERT INTO pages (org_id, alert_group_id, step_number, kind, state,
				created_at, ended_at) VALUES ($1, $2, 1, 'mention', $3, $4, CASE WHEN $3 = 'pending' THEN NULL
				ELSE $4::timestamptz END) RETURNING id`, l.orgID, group, state, t0).Scan(&id); err != nil {
				t.Fatal(err)
			}
			return id
		}
		reply := func(pageID int64, state string) int64 {
			t.Helper()
			var id int64
			if err := l.d.Pool.QueryRow(ctx, `INSERT INTO thread_replies (org_id, delivery_id, alert_group_id,
				destination_id, event, event_seqs, loudness, mentions, state, next_attempt_at, created_at, page_id)
				VALUES ($1, $2, $3, $4, 'paged', '{1}', 'loud', '{paged}', $5, $6, $6, $7) RETURNING id`, l.orgID, del,
				group, l.dests[0], state, t0.Add(time.Hour), pageID).Scan(&id); err != nil {
				t.Fatal(err)
			}
			return id
		}
		ended := func(after int64) []int64 {
			t.Helper()
			rows, err := deliverydb.New(l.d.Pool).ListEndedPages(ctx, deliverydb.ListEndedPagesParams{OrgID: l.orgID,
				After: after, Lim: 10})
			if err != nil {
				t.Fatal(err)
			}
			var out []int64
			for _, r := range rows {
				out = append(out, r.ID)
			}
			return out
		}
		waiting, collecting, done, over := page("pending"), page("pending"), page("pending"), page("delivered")
		r1 := reply(waiting, "sent")
		r2 := reply(waiting, "pending")
		r3 := reply(collecting, "collecting")
		reply(done, "sent")
		reply(done, "not_delivered")
		reply(over, "sent")
		if got := ended(0); !slices.Equal(got, []int64{done}) {
			t.Errorf("with replies still to be sent: %v, want %d", got, done)
		}
		l.exec(t, `UPDATE thread_replies SET state = 'sent' WHERE id = ANY($1)`, []int64{r1, r2})
		l.exec(t, `UPDATE thread_replies SET state = 'dropped' WHERE id = $1`, r3)
		if got := ended(0); !slices.Equal(got, []int64{waiting, collecting, done}) {
			t.Errorf("once every reply ended: %v, want %v", got, []int64{waiting, collecting, done})
		}
		if got := ended(collecting); !slices.Equal(got, []int64{done}) {
			t.Errorf("after %d: %v, want %d", collecting, got, done)
		}
	})
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package delivery_test

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

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

// TestIntegrationDropPageReplies is C-32.FR-10 for DropPageReplies on PostgreSQL (#603): the Page replies of the
// delivery still to be sent are dropped and their Pages returned, so that ListEndedPages lists them; a reply of no
// Page keeps waiting, and a Page reply that ended is left as it is.
func TestIntegrationDropPageReplies(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		ctx := t.Context()
		l.fire(t, "dropped", "Dropped/a")
		l.round(t, l.a)
		var group, del int64
		if err := l.d.Pool.QueryRow(ctx, `SELECT a.id, x.id FROM alert_groups a JOIN deliveries x
			ON x.alert_group_id = a.id WHERE a.public_id = $1 AND x.destination_id = $2`, l.group(t, "Dropped"),
			l.dests[0]).Scan(&group, &del); err != nil {
			t.Fatal(err)
		}
		var waiting, sent int64
		for _, p := range []*int64{&waiting, &sent} {
			if err := l.d.Pool.QueryRow(ctx, `INSERT INTO pages (org_id, alert_group_id, step_number, kind, state,
				created_at) VALUES ($1, $2, 1, 'mention', 'pending', $3) RETURNING id`, l.orgID, group, t0).
				Scan(p); err != nil {
				t.Fatal(err)
			}
		}
		reply := func(event string, page any, state string) int64 {
			t.Helper()
			var id int64
			if err := l.d.Pool.QueryRow(ctx, `INSERT INTO thread_replies (org_id, delivery_id, alert_group_id,
				destination_id, event, event_seqs, loudness, state, next_attempt_at, created_at, page_id)
				VALUES ($1, $2, $3, $4, $5, '{1}', 'loud', $6, $7, $7, $8) RETURNING id`, l.orgID, del, group,
				l.dests[0], event, state, t0.Add(time.Hour), page).Scan(&id); err != nil {
				t.Fatal(err)
			}
			return id
		}
		pageReply := reply("paged", waiting, "pending")
		sentReply := reply("paged", sent, "sent")
		other := reply("reminder", nil, "pending")
		got, err := deliverydb.New(l.d.Pool).DropPageReplies(ctx, deliverydb.DropPageRepliesParams{OrgID: l.orgID,
			DeliveryID: del})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, []int64{waiting}) {
			t.Errorf("pages %v, want %d", got, waiting)
		}
		states := map[int64]string{}
		rows, err := l.d.Pool.Query(ctx, `SELECT id, state FROM thread_replies WHERE id = ANY($1)`,
			[]int64{pageReply, sentReply, other})
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id int64
			var state string
			if err := rows.Scan(&id, &state); err != nil {
				t.Fatal(err)
			}
			states[id] = state
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if states[pageReply] != "dropped" || states[sentReply] != "sent" || states[other] != "pending" {
			t.Errorf("states %v", states)
		}
		ended, err := deliverydb.New(l.d.Pool).ListEndedPages(ctx, deliverydb.ListEndedPagesParams{OrgID: l.orgID,
			Lim: 10})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(ended, func(r deliverydb.ListEndedPagesRow) bool { return r.ID == waiting }) {
			t.Errorf("ended pages %v, want %d among them", ended, waiting)
		}
		// EnsureDelivery tells a delivery without a Root message for the replies to wait for: the published one has
		// it, a new one, as one whose Publication ended Not delivered, has not.
		ensure := func(dest int64) deliverydb.EnsureDeliveryRow {
			t.Helper()
			row, err := deliverydb.New(l.d.Pool).EnsureDelivery(ctx, deliverydb.EnsureDeliveryParams{OrgID: l.orgID,
				DestinationID: dest, AlertGroupID: pgtype.Int8{Int64: group, Valid: true}, Entries: []byte("[]"),
				Now: l.business.Now()})
			if err != nil {
				t.Fatal(err)
			}
			return row
		}
		if row := ensure(l.dests[0]); row.ID != del || row.Inserted || row.Unpublished {
			t.Errorf("the published delivery %+v, want it not unpublished", row)
		}
		row := ensure(l.dests[1])
		if !row.Inserted || !row.Unpublished {
			t.Errorf("a new delivery %+v, want it unpublished", row)
		}
		l.exec(t, `UPDATE deliveries SET state = 'not_delivered', last_error_class = 'unknown',
			last_error = 'channel not found' WHERE id = $1`, row.ID)
		if again := ensure(l.dests[1]); again.ID != row.ID || again.Inserted || !again.Unpublished ||
			again.State != "not_delivered" {
			t.Errorf("the delivery whose publication ended not delivered %+v, want it unpublished", again)
		}
	})
}

// TestIntegrationPageReplyLeftOut is C-32.FR-10 and C-12.FR-11 for RecordReplySent on PostgreSQL (#625): a Page reply
// recorded sent with the people its Mention line did not leave out mentions only those, which ListPageReplies
// reports, an empty list included; one recorded without them keeps the people decided when it was queued; and a reply
// whose lease went to another replica is left as it is.
func TestIntegrationPageReplyLeftOut(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		ctx := t.Context()
		l.fire(t, "leftout", "LeftOut/a")
		l.round(t, l.a)
		var group, del, page int64
		if err := l.d.Pool.QueryRow(ctx, `SELECT a.id, x.id FROM alert_groups a JOIN deliveries x
			ON x.alert_group_id = a.id WHERE a.public_id = $1 AND x.destination_id = $2`, l.group(t, "LeftOut"),
			l.dests[0]).Scan(&group, &del); err != nil {
			t.Fatal(err)
		}
		if err := l.d.Pool.QueryRow(ctx, `INSERT INTO pages (org_id, alert_group_id, step_number, kind, state,
			created_at) VALUES ($1, $2, 1, 'mention', 'pending', $3) RETURNING id`, l.orgID, group, t0).
			Scan(&page); err != nil {
			t.Fatal(err)
		}
		reply := func() int64 {
			t.Helper()
			var id int64
			if err := l.d.Pool.QueryRow(ctx, `INSERT INTO thread_replies (org_id, delivery_id, alert_group_id,
				destination_id, event, event_seqs, loudness, mentions, state, next_attempt_at, created_at, page_id,
				paged_user_ids, lease_owner, lease_until) VALUES ($1, $2, $3, $4, 'paged', '{1}', 'loud', '{paged}',
				'pending', $5, $5, $6, '{11,12,13}', 'w', $7) RETURNING id`, l.orgID, del, group, l.dests[0], t0,
				page, t0.Add(time.Hour)).Scan(&id); err != nil {
				t.Fatal(err)
			}
			return id
		}
		q := deliverydb.New(l.d.Pool)
		sent := func(id int64, owner string, paged []int64) int64 {
			t.Helper()
			n, err := q.RecordReplySent(ctx, deliverydb.RecordReplySentParams{OrgID: l.orgID, ID: id, Owner: owner,
				PagedUserIds: paged, Now: t0})
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
		leftOut, kept, none, other := reply(), reply(), reply(), reply()
		if sent(leftOut, "w", []int64{11, 13}) != 1 || sent(kept, "w", nil) != 1 || sent(none, "w", []int64{}) != 1 ||
			sent(other, "another", []int64{11}) != 0 {
			t.Fatal("a reply not recorded as its lease says")
		}
		got := map[int64][]int64{}
		rows, err := l.d.Pool.Query(ctx, `SELECT id, paged_user_ids FROM thread_replies WHERE page_id = $1`, page)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id int64
			var paged []int64
			if err := rows.Scan(&id, &paged); err != nil {
				t.Fatal(err)
			}
			got[id] = paged
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got[leftOut], []int64{11, 13}) || !slices.Equal(got[kept], []int64{11, 12, 13}) ||
			got[none] == nil || len(got[none]) != 0 || !slices.Equal(got[other], []int64{11, 12, 13}) {
			t.Errorf("paged_user_ids %v", got)
		}
		replies, err := q.ListPageReplies(ctx, deliverydb.ListPageRepliesParams{OrgID: l.orgID, PageID: page})
		if err != nil {
			t.Fatal(err)
		}
		var reached []int64
		for _, r := range replies {
			if r.State == "sent" {
				reached = append(reached, r.PagedUserIds...)
			}
		}
		slices.Sort(reached)
		if want := []int64{11, 11, 12, 13, 13}; !slices.Equal(reached, want) {
			t.Errorf("the sent replies mention %v, want %v", reached, want)
		}
	})
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package delivery_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/delivery"
	deliverydb "github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
)

// TestIntegrationConfigurationEditsLast is C-11.FR-3 and FR-15 on PostgreSQL, for a re-render of many open Root
// messages: 100 open Alert Groups are published in one Telegram channel; a change of the Destination's button set
// re-renders them through open_group_updates, and the channel's limiter (10 per minute) lets the first 10 edits through.
// New Alerts of an open one arrive then, and a second re-render on top of their pending edit keeps that edit's place; a
// new Alert Group arrives last, due after every edit of the re-render. The Publication and the edit of the Alerts are
// the next calls to the channel, before the 90 remaining edits of the re-render; then every edit of the re-render goes
// out, by age. The mark follows SetDesired's rule and is cleared by a delivered edit, a final edit, a republication and
// a rejoin.
func TestIntegrationConfigurationEditsLast(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		ctx := t.Context()
		l.fresh(t, 1000, 1)
		dest := l.telegramChannel(t)
		w := l.worker("cfg")
		w.Adapters[delivery.TypeTelegram] = l.rec
		updates := l.updates()
		names := make([]string, 100)
		for i := range names {
			names[i] = fmt.Sprintf("Cfg%03d/a", i)
		}
		l.fire(t, "cfg", names...)
		l.round(t, w)
		if n := l.count(t, `SELECT count(*) FROM deliveries WHERE destination_id = $1 AND state = 'delivered'`,
			dest); n != 100 || l.rec.Count(deliverytest.MethodPublish) != 100 {
			t.Fatalf("%d of 100 published, %d publications\n%s", n, l.rec.Count(deliverytest.MethodPublish), l.log)
		}

		// The channel's limiter of a Telegram Destination, its bucket full; then a re-render of every Root message.
		l.exec(t, `UPDATE destinations SET limiter_limit = 10, limiter_per_seconds = 60 WHERE id = $1`, dest)
		l.exec(t, `DELETE FROM rate_limit_buckets`)
		rerender := func(buttons string) { t.Helper(); l.rerender(t, updates, dest, buttons) }
		marked := func() int64 { t.Helper(); return l.marked(t, dest) }
		rerender(`[{"button": "acknowledge"}, {"button": "resolve"}]`)
		if n := marked(); n != 100 {
			t.Fatalf("%d edits of the re-render marked, want 100\n%s", n, l.log)
		}
		l.rec.Reset()
		l.business.Advance(time.Second)
		l.round(t, w)
		if n := l.rec.Count(deliverytest.MethodUpdate); n != 10 || marked() != 90 {
			t.Fatalf("%d edits within the bucket, %d marked; want 10, 90", n, marked())
		}

		// A new Alert of the open Cfg050 while 90 edits wait, then a second re-render on top of its edit.
		l.business.Advance(time.Second)
		l.fire(t, "cfg-050", "Cfg050/b")
		flag := func(name string) (bool, string) {
			t.Helper()
			var only bool
			var state string
			if err := l.d.Pool.QueryRow(ctx, `SELECT d.configuration_only, d.state FROM deliveries d
				JOIN alert_groups g ON g.id = d.alert_group_id WHERE g.public_id = $1 AND d.destination_id = $2`,
				l.group(t, name), dest).Scan(&only, &state); err != nil {
				t.Fatal(err)
			}
			return only, state
		}
		if only, state := flag("Cfg050"); only || state != "pending" {
			t.Errorf("Cfg050: configuration only %v, %s", only, state)
		}
		// Cfg050 keeps the place of its edit of Alerts, the others stay marked.
		rerender(`[{"button": "resolve"}]`)
		if only, _ := flag("Cfg050"); only {
			t.Error("a re-render on top of the edit of Alerts marked it")
		}
		if n := marked(); n != 99 {
			t.Errorf("%d marked after the second re-render, want 99", n)
		}
		// A new Alert Group, due later than every edit of the re-render: only the rank puts it first, as age, and
		// last_delivered_at NULLS FIRST behind it, would not.
		l.business.Advance(10 * time.Second)
		l.fire(t, "cfg-new", "CfgNew/a")
		if n := l.count(t, `SELECT count(*) FROM deliveries d JOIN alert_groups g ON g.id = d.alert_group_id
			WHERE g.title LIKE 'CfgNew%' AND d.next_attempt_at > (SELECT max(next_attempt_at) FROM deliveries
				WHERE destination_id = $1 AND configuration_only AND state = 'pending')`, dest); n != 1 {
			t.Fatalf("the Publication of CfgNew is not due after the edits of the re-render")
		}
		if only, state := flag("CfgNew"); only || state != "pending" {
			t.Errorf("CfgNew: configuration only %v, %s", only, state)
		}

		l.rec.Reset()
		for i := 0; marked() > 0 || l.count(t, `SELECT count(*) FROM deliveries WHERE destination_id = $1
			AND state = 'pending'`, dest) > 0; i++ {
			if i == 200 {
				t.Fatalf("still %d marked after 200 rounds", marked())
			}
			l.business.Advance(6*time.Second + delivery.TokenMargin)
			l.round(t, w)
		}
		var order []string
		for _, c := range l.rec.Calls() {
			order = append(order, c.Method+" "+head(c.Message))
		}
		first := slices.Clone(order[:min(len(order), 2)])
		slices.Sort(first)
		if len(first) != 2 || !strings.HasPrefix(first[0], deliverytest.MethodPublish+" #") ||
			!strings.Contains(first[0], " CfgNew") || !strings.HasPrefix(first[1], deliverytest.MethodUpdate+" #") ||
			!strings.Contains(first[1], " Cfg050") {
			t.Fatalf("the first calls after the new arrivals: %q", order[:min(len(order), 5)])
		}
		if n := l.rec.Count(deliverytest.MethodUpdate); n != 100 {
			t.Errorf("%d edits after the new arrivals, want Cfg050 and the 99 left of the re-render", n)
		}
		if n := l.count(t, `SELECT count(*) FROM deliveries WHERE destination_id = $1 AND state = 'delivered'`,
			dest); n != 101 {
			t.Errorf("%d of 101 delivered", n)
		}
		if n := l.count(t, `SELECT count(*) FROM deliveries WHERE destination_id = $1 AND configuration_only`,
			dest); n != 0 {
			t.Errorf("%d delivered edits keep the mark", n)
		}

		// A final edit, a republication and a rejoin clear the mark; a Publication never takes it.
		q := deliverydb.New(l.d.Pool)
		rerender(`[{"button": "acknowledge"}]`)
		ids := func(name string) int64 {
			t.Helper()
			return l.count(t, `SELECT d.id FROM deliveries d JOIN alert_groups g ON g.id = d.alert_group_id
				WHERE g.public_id = $1 AND d.destination_id = $2`, l.group(t, name), dest)
		}
		if _, err := q.RetireDeliveries(ctx, deliverydb.RetireDeliveriesParams{Now: l.business.Now(), OrgID: l.orgID,
			Ids: []int64{ids("Cfg001")}}); err != nil {
			t.Fatal(err)
		}
		if _, err := q.ResetForRepublish(ctx, deliverydb.ResetForRepublishParams{Now: l.business.Now(),
			OrgID: l.orgID, ID: ids("Cfg002")}); err != nil {
			t.Fatal(err)
		}
		rerender(`[{"button": "unacknowledge"}]`)
		l.exec(t, `UPDATE deliveries SET state = 'retired', configuration_only = true WHERE id = $1`, ids("Cfg003"))
		if _, err := q.RejoinDelivery(ctx, deliverydb.RejoinDeliveryParams{Now: l.business.Now(), OrgID: l.orgID,
			AlertGroupID:  l.count(t, `SELECT id FROM alert_groups WHERE public_id = $1`, l.group(t, "Cfg003")),
			DestinationID: dest}); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"Cfg001", "Cfg002", "Cfg003"} {
			if only, state := flag(name); only || state != "pending" {
				t.Errorf("%s: configuration only %v, %s", name, only, state)
			}
		}
		if only, _ := flag("Cfg004"); !only {
			t.Error("Cfg004 lost its mark")
		}
		l.noDeadlock(t)
	})
}

// TestIntegrationRepliesBeforeConfigurationEdits is C-11.FR-3 and FR-4 on PostgreSQL, for a re-render of many open
// Root messages: 100 open Alert Groups, one of them Urgent, are published in one Telegram channel; a change of the
// Destination's button set re-renders them, and the channel's limiter (10 per minute) lets the first 10 edits through,
// the Urgent one first. New Alerts join the open Rep050 and the Urgent RepU then: their Root-message edits and their
// alerts_added Thread replies are the next four calls to the channel, before the 89 remaining edits of the re-render
// (the edit of Rep050's new Alerts takes the place of its own), which then all go out.
func TestIntegrationRepliesBeforeConfigurationEdits(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		ctx := t.Context()
		l.fresh(t, 1000, 1)
		dest := l.telegramChannel(t)
		w := l.worker("rep")
		w.Adapters[delivery.TypeTelegram] = l.rec
		updates := l.updates()
		alerts := []alert{{name: "RepU/a", team: "db", severity: "critical"}}
		for i := range 99 {
			alerts = append(alerts, alert{name: fmt.Sprintf("Rep%03d/a", i), team: "db"})
		}
		l.fireAlerts(t, "rep", alerts...)
		l.round(t, w)
		if n := l.count(t, `SELECT count(*) FROM deliveries WHERE destination_id = $1 AND state = 'delivered'`,
			dest); n != 100 || l.count(t, `SELECT count(*) FROM deliveries WHERE destination_id = $1 AND urgent`,
			dest) != 1 {
			t.Fatalf("%d of 100 published, the Urgent one among them\n%s", n, l.log)
		}
		// Past telegram.copy_wait, a reply starts a chain at once instead of waiting for the automatic copy.
		l.business.Advance(delivery.CopyWait)

		l.exec(t, `UPDATE destinations SET limiter_limit = 10, limiter_per_seconds = 60 WHERE id = $1`, dest)
		l.exec(t, `DELETE FROM rate_limit_buckets`)
		l.rerender(t, updates, dest, `[{"button": "acknowledge"}, {"button": "resolve"}]`)
		if n := l.marked(t, dest); n != 100 {
			t.Fatalf("%d edits of the re-render marked, want 100\n%s", n, l.log)
		}
		l.rec.Reset()
		l.business.Advance(time.Second)
		l.round(t, w)
		if n := l.rec.Count(deliverytest.MethodUpdate); n != 10 || l.marked(t, dest) != 90 ||
			!strings.Contains(head(l.rec.Calls()[0].Message), " RepU") {
			t.Fatalf("%d edits within the bucket, %d marked, the first %q; want 10, 90, RepU", n, l.marked(t, dest),
				head(l.rec.Calls()[0].Message))
		}

		// New Alerts join the open Rep050, whose edit of the re-render still waits and becomes theirs, and the Urgent
		// RepU, whose edit of the re-render went out.
		l.business.Advance(time.Second)
		l.fire(t, "rep-050", "Rep050/b")
		l.fireAlerts(t, "rep-u", alert{name: "RepU/b", team: "db", severity: "critical"})
		if n := l.count(t, `SELECT count(*) FROM thread_replies WHERE destination_id = $1 AND state = 'pending'
			AND event = 'alerts_added' AND next_attempt_at <= $2`, dest, l.business.Now()); n != 2 {
			t.Fatalf("%d alerts_added replies due, want 2\n%s", n, l.log)
		}
		if n := l.marked(t, dest); n != 89 {
			t.Fatalf("%d marked after the new Alerts, want 89", n)
		}

		l.rec.Reset()
		for i := 0; l.count(t, `SELECT (SELECT count(*) FROM deliveries WHERE destination_id = $1 AND state = 'pending')
			+ (SELECT count(*) FROM thread_replies WHERE destination_id = $1 AND state IN ('collecting', 'pending'))`,
			dest) > 0; i++ {
			if i == 200 {
				t.Fatalf("still %d marked after 200 rounds", l.marked(t, dest))
			}
			l.business.Advance(6*time.Second + delivery.TokenMargin)
			l.round(t, w)
		}
		titles := map[string]string{}
		rows, err := l.d.Pool.Query(ctx, `SELECT d.message_id, g.title FROM deliveries d
			JOIN alert_groups g ON g.id = d.alert_group_id WHERE d.destination_id = $1`, dest)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id, title string
			if err := rows.Scan(&id, &title); err != nil {
				t.Fatal(err)
			}
			titles[id] = strings.Fields(title)[0]
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		var order []string
		for _, c := range l.rec.Calls() {
			order = append(order, c.Method+" "+titles[c.MessageID])
		}
		first := slices.Clone(order[:min(len(order), 4)])
		slices.Sort(first)
		if want := []string{deliverytest.MethodReply + " Rep050", deliverytest.MethodReply + " RepU",
			deliverytest.MethodUpdate + " Rep050", deliverytest.MethodUpdate + " RepU"}; !slices.Equal(first, want) {
			t.Fatalf("the first calls after the new Alerts: %q", order[:min(len(order), 6)])
		}
		if n := l.rec.Count(deliverytest.MethodUpdate); n != 91 || l.rec.Count(deliverytest.MethodReply) != 2 {
			t.Errorf("%d edits and %d replies after the new Alerts, want 91 and 2", n,
				l.rec.Count(deliverytest.MethodReply))
		}
		if n := l.count(t, `SELECT count(*) FROM deliveries WHERE destination_id = $1 AND state = 'delivered'
			AND NOT configuration_only`, dest); n != 100 {
			t.Errorf("%d of 100 delivered and unmarked", n)
		}
		l.noDeadlock(t)
	})
}

// telegramChannel creates a Telegram Connection and Destination, both at 1000 per second, and puts the Destination
// alone on the Route db.
func (l *live) telegramChannel(t *testing.T) int64 {
	t.Helper()
	var conn, dest int64
	if err := l.d.Pool.QueryRow(t.Context(), `INSERT INTO connections (org_id, public_id, type, name,
		telegram_bot_api_base_url, telegram_update_mode, bot_token_ciphertext, bot_token_key_id,
		bot_token_updated_at, limiter_limit, limiter_per_seconds, created_at, updated_at)
		VALUES ($1, 'CNAAAAAAAAAAC1', 'telegram', 'tg', 'https://api.telegram.org', 'long_polling', '\x00', 'k1',
		$2, 1000, 1, $2, $2) RETURNING id`, l.orgID, t0).Scan(&conn); err != nil {
		t.Fatal(err)
	}
	if err := l.d.Pool.QueryRow(t.Context(), `INSERT INTO destinations (org_id, public_id, type, name, connection_id,
		telegram_channel_id, telegram_channel_chat_id, telegram_discussion_chat_id, mentions, limiter_limit,
		limiter_per_seconds, health, created_at, updated_at) VALUES ($1, 'DSAAAAAAAAAAC1', 'telegram', 'alerts',
		$2, '@muster_alerts', -1001000000011, -1001000000012, '{}', 1000, 1, 'healthy', $3, $3)
		RETURNING id`, l.orgID, conn, t0).Scan(&dest); err != nil {
		t.Fatal(err)
	}
	l.attach(t, dest)
	return dest
}

// updates is the worker of open_group_updates.
func (l *live) updates() *groups.UpdateWorker {
	return &groups.UpdateWorker{Service: l.groups, Log: l.logger, Lease: db.Lease{Owner: "u1",
		Duration: groups.UpdateLease, Clocks: clock.Clocks{Business: l.business, Real: l.real}}}
}

// rerender sets the Destination's button set and re-renders its open Root messages through open_group_updates.
func (l *live) rerender(t *testing.T, updates *groups.UpdateWorker, dest int64, buttons string) {
	t.Helper()
	l.exec(t, `UPDATE destinations SET buttons = $2::jsonb WHERE id = $1`, dest, buttons)
	l.exec(t, `INSERT INTO open_group_updates (org_id, kind, destination_id, requested_at)
		VALUES ($1, 'destination_buttons', $2, $3)`, l.orgID, dest, l.business.Now())
	if _, err := updates.Round(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// marked counts the pending configuration edits of the Destination.
func (l *live) marked(t *testing.T, dest int64) int64 {
	t.Helper()
	return l.count(t, `SELECT count(*) FROM deliveries WHERE destination_id = $1 AND state = 'pending'
		AND configuration_only`, dest)
}

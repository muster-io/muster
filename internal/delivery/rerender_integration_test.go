// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package delivery_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/keyring"
)

// TestIntegrationRerenderAfterRotation covers C-20.FR-7 and AC-5 on PostgreSQL: after a rotation the re-render
// selects only the deliveries of open Alert Groups signed by another key that have a Root message to edit or still to
// publish; deliveries deleted in the messenger, withheld or retired, final edits and resolved Alert Groups keep their
// Desired state; a delivery held by a Storm is re-rendered and stays held; an Alert Group a transaction holds is
// skipped and picked up by the next run; the worker edits the Root messages with buttons the new key signed and
// records it as the actual key, after which no open Root message carries the old key; a run after that changes
// nothing.
func TestIntegrationRerenderAfterRotation(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		ctx := t.Context()
		l.fresh(t, 1000, 1)
		l.attach(t, l.dests...)
		l.fire(t, "rerender", "RA/a", "RB/a", "RC/a", "RD/a", "RE/a")
		l.round(t, l.a)
		old := l.keys.ActiveKeyID()
		groups := map[string]string{}
		for _, name := range []string{"RA", "RB", "RC", "RD", "RE"} {
			groups[name] = l.group(t, name)
		}
		if n := l.count(t, `SELECT count(*) FROM deliveries WHERE state = 'delivered' AND actual_button_key_id = $1
			AND desired_button_key_id = $1`, old); n != 10 {
			t.Fatalf("%d deliveries delivered with the old key\n%s", n, l.log)
		}
		l.resolve(t, groups["RE"])
		l.round(t, l.a)
		set := func(name string, dest int64, set string) {
			t.Helper()
			l.exec(t, `UPDATE deliveries d SET `+set+` FROM alert_groups g
				WHERE g.id = d.alert_group_id AND g.public_id = $1 AND d.destination_id = $2`, groups[name], dest)
		}
		set("RB", l.dests[0], `state = 'deleted_in_messenger'`)
		set("RB", l.dests[1], `state = 'withheld'`)
		set("RC", l.dests[0], `state = 'retired'`)
		set("RC", l.dests[1], `desired_retire = true, state = 'pending'`)
		set("RD", l.dests[1], `state = 'not_delivered', last_error = 'unknown response'`)
		var storm int64
		if err := l.d.Pool.QueryRow(ctx, `INSERT INTO storms (org_id, route_id, started_at, alert_group_count)
			VALUES ($1, $2, $3, 2) RETURNING id`, l.orgID, l.routeID, l.business.Now()).Scan(&storm); err != nil {
			t.Fatal(err)
		}
		set("RD", l.dests[0], `held_by_storm_id = `+strconv.FormatInt(storm, 10)+`, state = 'pending', published_at = NULL,
			message_id = NULL, actual_button_key_id = NULL`)

		active := l.rotate(t)
		// A Command holds RA meanwhile: its own re-render signs with the active key, the next run picks it up.
		tx, err := l.d.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
		if _, err := tx.Exec(ctx, `SELECT id FROM alert_groups WHERE public_id = $1 FOR NO KEY UPDATE`,
			groups["RA"]); err != nil {
			t.Fatal(err)
		}
		n, err := l.svc.RerenderOpen(ctx, active)
		if err != nil || n != 2 {
			t.Fatalf("re-rendered %d while RA was held, %v\n%s", n, err, l.log)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if n, err = l.svc.RerenderOpen(ctx, active); err != nil || n != 2 {
			t.Fatalf("re-rendered %d once RA was free, %v", n, err)
		}
		keyOf := func(name string, dest int64) (string, string, bool) {
			t.Helper()
			var key, state string
			var held bool
			if err := l.d.Pool.QueryRow(ctx, `SELECT coalesce(d.desired_button_key_id, ''), d.state,
				d.held_by_storm_id IS NOT NULL FROM deliveries d JOIN alert_groups g ON g.id = d.alert_group_id
				WHERE g.public_id = $1 AND d.destination_id = $2`, groups[name], dest).Scan(&key, &state,
				&held); err != nil {
				t.Fatal(err)
			}
			return key, state, held
		}
		for _, c := range []struct {
			name  string
			dest  int64
			key   string
			state string
			held  bool
		}{
			{"RA", l.dests[0], active, "pending", false}, {"RA", l.dests[1], active, "pending", false},
			{"RB", l.dests[0], old, "deleted_in_messenger", false}, {"RB", l.dests[1], old, "withheld", false},
			{"RC", l.dests[0], old, "retired", false}, {"RC", l.dests[1], old, "pending", false},
			{"RD", l.dests[0], active, "pending", true}, {"RD", l.dests[1], active, "pending", false},
		} {
			if key, state, held := keyOf(c.name, c.dest); key != c.key || state != c.state || held != c.held {
				t.Errorf("%s in %d: key %s, %s, held %v; want %s, %s, %v", c.name, c.dest, key, state, held, c.key,
					c.state, c.held)
			}
		}
		if l.count(t, `SELECT count(*) FROM deliveries d JOIN alert_groups g ON g.id = d.alert_group_id
			WHERE g.public_id = $1 AND d.desired_button_key_id = $2`, groups["RE"], active) != 0 {
			t.Error("the resolved alert group was re-rendered")
		}

		// Until the worker edits them, the Root messages of RA, RC's pending final edit and RD's Not delivered one
		// keep the old key needed; the withheld and the retired one never do: the re-render does not re-sign them.
		st, err := keyring.ReadStatus(ctx, keyring.NewStatusQueries(l.d.Pool), l.keys.KeyIDs(), l.real.Now())
		if err != nil {
			t.Fatal(err)
		}
		if k, ok := st.Key(old); !ok || k.State != keyring.KeyNeeded || k.OpenMessages != 4 {
			t.Errorf("the old key before the edits %+v", k)
		}

		l.rec.Reset()
		l.round(t, l.a)
		edits := 0
		for _, c := range l.rec.Calls() {
			if c.Method == deliverytest.MethodUpdate && len(c.Message.Buttons) > 0 {
				edits++
				if c.Message.Buttons[0].KeyID != active {
					t.Errorf("an edit signed by %s", c.Message.Buttons[0].KeyID)
				}
			}
		}
		if edits != 3 {
			t.Errorf("%d edits with buttons, want RA twice and RD once: %+v", edits, l.rec.Calls())
		}
		// The final edit of RC cleared its key; the rows set withheld and retired above keep theirs.
		if n := l.count(t, `SELECT count(*) FROM deliveries d JOIN alert_groups g ON g.id = d.alert_group_id
			WHERE g.status <> 'resolved' AND d.state <> 'deleted_in_messenger' AND d.actual_button_key_id = $1`,
			old); n != 2 {
			t.Errorf("%d open root messages with the old key", n)
		}
		if n := l.count(t, `SELECT count(*) FROM deliveries WHERE actual_button_key_id = $1`, active); n != 3 {
			t.Errorf("%d deliveries carry the new key", n)
		}
		// The Keyring status counts the Root messages of open Alert Groups that still carry the old key and that the
		// re-render would re-sign: not the one deleted in the messenger, the withheld or the retired one, nor those of
		// the resolved Alert Group: only the Secrets it still encrypts may need it (C-20.FR-6).
		st, err = keyring.ReadStatus(ctx, keyring.NewStatusQueries(l.d.Pool), l.keys.KeyIDs(), l.real.Now())
		if err != nil {
			t.Fatal(err)
		}
		if k, ok := st.Key(old); !ok || k.OpenMessages != 0 || k.Needed != (k.EncryptedValues > 0) {
			t.Errorf("the old key %+v", k)
		}
		if k, ok := st.Key(active); !ok || k.State != keyring.KeyActive || k.OpenMessages != 3 {
			t.Errorf("the active key %+v", k)
		}
		if n, err := l.svc.RerenderOpen(ctx, active); err != nil || n != 0 {
			t.Errorf("a second run re-rendered %d, %v", n, err)
		}
		l.noDeadlock(t)
	})
}

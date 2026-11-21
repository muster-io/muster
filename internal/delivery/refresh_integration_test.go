// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package delivery_test

import (
	"testing"

	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
)

// TestIntegrationRefreshButtons covers the refresh of a Root message whose buttons lack the callback secret (C-13.FR-4)
// on PostgreSQL: a delivered Root message whose stored Desired state is the rendering is made due again, Quietly, as
// its next version, and the worker edits it once with the same buttons; a second refresh, before and after that edit,
// changes nothing; the other Destination's delivery, an unknown Destination and a resolved Alert Group are left alone.
func TestIntegrationRefreshButtons(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		ctx := t.Context()
		l.fresh(t, 1000, 1)
		l.attach(t, l.dests...)
		l.fire(t, "refresh", "FA/a")
		l.round(t, l.a)
		public := l.group(t, "FA")
		group := l.count(t, `SELECT id FROM alert_groups WHERE public_id = $1`, public)
		version := func(dest int64) int64 {
			t.Helper()
			return l.count(t, `SELECT desired_version FROM deliveries WHERE alert_group_id = $1
				AND destination_id = $2`, group, dest)
		}
		v0, other := version(l.dests[0]), version(l.dests[1])
		if n := l.count(t, `SELECT count(*) FROM deliveries WHERE alert_group_id = $1 AND state = 'delivered'
			AND actual_hash = desired_hash AND desired_button_key_id IS NOT NULL`, group); n != 2 {
			t.Fatalf("%d deliveries delivered with buttons\n%s", n, l.log)
		}

		changed, err := l.svc.RefreshButtons(ctx, group, l.dests[0])
		if err != nil || !changed {
			t.Fatalf("refresh = %v, %v", changed, err)
		}
		if v := version(l.dests[0]); v != v0+1 || l.count(t, `SELECT count(*) FROM deliveries
			WHERE alert_group_id = $1 AND destination_id = $2 AND state = 'pending' AND configuration_only
			AND desired_hash <> actual_hash`, group, l.dests[0]) != 1 {
			t.Errorf("version %d after the refresh, want %d and a pending edit of configuration", v, v0+1)
		}
		if changed, err := l.svc.RefreshButtons(ctx, group, l.dests[0]); err != nil || changed {
			t.Errorf("a second refresh = %v, %v", changed, err)
		}
		l.rec.Reset()
		l.round(t, l.a)
		var edits int
		for _, c := range l.rec.Calls() {
			if c.Method == deliverytest.MethodUpdate {
				edits++
				if c.Destination.ID != l.dests[0] || len(c.Message.Buttons) == 0 {
					t.Errorf("edit %+v", c)
				}
			}
		}
		if edits != 1 || l.rec.Count(deliverytest.MethodPublish) != 0 {
			t.Errorf("%d edits after the refresh, calls %+v", edits, l.rec.Calls())
		}
		if changed, err := l.svc.RefreshButtons(ctx, group, l.dests[0]); err != nil || changed {
			t.Errorf("a refresh after the edit = %v, %v", changed, err)
		}
		if v := version(l.dests[1]); v != other {
			t.Errorf("the other destination's version %d, want %d", v, other)
		}
		if changed, err := l.svc.RefreshButtons(ctx, group, 1<<40); err != nil || changed {
			t.Errorf("a refresh of an unknown destination = %v, %v", changed, err)
		}
		l.resolve(t, public)
		l.round(t, l.a)
		if changed, err := l.svc.RefreshButtons(ctx, group, l.dests[0]); err != nil || changed {
			t.Errorf("a refresh of a resolved alert group = %v, %v", changed, err)
		}
		l.noDeadlock(t)
	})
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"bytes"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
)

// TestRefreshButtons covers the refresh of a Root message whose buttons lack the callback secret (C-13.FR-4): in the
// lock order of the re-render after a rotation, the delivered Root message whose stored Desired state is the rendering
// becomes due again as an edit of configuration with a new hash and the same buttons, which the worker sends once; a
// second refresh, before and after that edit, changes nothing, nor does a refresh of the other Destination's delivery
// change this one. A stored state that differs from the rendering, such as one an older key signed, is replaced by it.
func TestRefreshButtons(t *testing.T) {
	e, key := rotationEnv(t)
	ctx := t.Context()
	d, other := e.deliveryTo(t, destMM), e.deliveryTo(t, destMM2)
	version, hash, otherVersion := d.version, bytes.Clone(d.hash), other.version
	var order []string
	for _, q := range []string{"LockRerenderGroup", "ShareRouteMembership", "LockRerenderDeliveries", "SetDesired"} {
		e.db.before[q] = func() { order = append(order, q) }
	}
	notified := e.db.notified
	if changed, err := e.svc.RefreshButtons(ctx, d.group, destMM); err != nil || !changed {
		t.Fatalf("refresh = %v, %v", changed, err)
	}
	if !slices.Equal(order, []string{"LockRerenderGroup", "ShareRouteMembership", "LockRerenderDeliveries",
		"SetDesired"}) {
		t.Errorf("lock order %v", order)
	}
	if d.version != version+1 || bytes.Equal(d.hash, hash) || d.state != "pending" || !d.configOnly ||
		d.buttonKeyID != "k-old" || e.db.notified != notified+1 {
		t.Errorf("after the refresh: version %d, hash changed %v, %s, configuration only %v, key %s, wakes %d",
			d.version, !bytes.Equal(d.hash, hash), d.state, d.configOnly, d.buttonKeyID, e.db.notified-notified)
	}
	if other.version != otherVersion {
		t.Errorf("the other destination's version %d, want %d", other.version, otherVersion)
	}
	if changed, err := e.svc.RefreshButtons(ctx, d.group, destMM); err != nil || changed {
		t.Errorf("a second refresh = %v, %v", changed, err)
	}
	e.round(t)
	calls := e.rec.Calls()
	if len(calls) != 1 || calls[0].Method != deliverytest.MethodUpdate || calls[0].Destination.ID != destMM ||
		calls[0].Message.Buttons[0].KeyID != "k-old" {
		t.Fatalf("calls %+v", calls)
	}
	if changed, err := e.svc.RefreshButtons(ctx, d.group, destMM); err != nil || changed {
		t.Errorf("a refresh after the edit = %v, %v", changed, err)
	}
	if changed, err := e.svc.RefreshButtons(ctx, d.group, 999); err != nil || changed {
		t.Errorf("a refresh of a destination without a delivery = %v, %v", changed, err)
	}

	*key = "k-new"
	if changed, err := e.svc.RefreshButtons(ctx, d.group, destMM2); err != nil || !changed ||
		other.buttonKeyID != "k-new" {
		t.Errorf("a refresh of a state an older key signed = %v, %v, key %s", changed, err, other.buttonKeyID)
	}
}

// TestRefreshButtonsSkipsAndFails: a refresh changes nothing while a Command holds the Alert Group or once it resolved,
// and returns the error of each query and of the rendering.
func TestRefreshButtonsSkipsAndFails(t *testing.T) {
	e, _ := rotationEnv(t)
	group := e.deliveryTo(t, destMM).group
	e.db.fail["LockRerenderGroup"] = pgx.ErrNoRows
	if changed, err := e.svc.RefreshButtons(t.Context(), group, destMM); err != nil || changed {
		t.Errorf("a refresh while the alert group is held = %v, %v", changed, err)
	}
	boom := errors.New("boom")
	for _, q := range []string{"LockRerenderGroup", "ShareRouteMembership", "LockRerenderDeliveries", "SetDesired",
		"NotifyDelivery"} {
		t.Run(q, func(t *testing.T) {
			e, _ := rotationEnv(t)
			e.db.fail[q] = boom
			if _, err := e.svc.RefreshButtons(t.Context(), group, destMM); !errors.Is(err, boom) {
				t.Errorf("err %v", err)
			}
		})
	}
	t.Run("render", func(t *testing.T) {
		e, key := rotationEnv(t)
		e.svc = delivery.New(delivery.Config{OrgID: orgID, Store: e.store, Business: e.business, Log: e.w.Log,
			Renderer: keyRenderer{stubRenderer: stubRenderer{err: boom}, key: key}})
		if _, err := e.svc.RefreshButtons(t.Context(), group, destMM); !errors.Is(err, boom) {
			t.Errorf("err %v", err)
		}
	})
}

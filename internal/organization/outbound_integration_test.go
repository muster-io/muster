// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package organization_test

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/outbound"
)

func allowLoopback(t *testing.T, d *db.DB, org int64) {
	t.Helper()
	if _, err := d.Pool.Exec(t.Context(), `UPDATE outbound_policies SET allowed = '{127.0.0.0/8}' WHERE org_id = $1`,
		org); err != nil {
		t.Fatal(err)
	}
}

// TestIntegrationOutboundPolicyUpdate is C-20.FR-3 on every supported PostgreSQL: an update with If-Match replaces
// the policy and moves its version, is recorded as outbound_policy.updated, and sends the organization hint and NOTIFY
// muster_outbound_policy, which listeners receive only once the change commits; a stale version changes nothing.
func TestIntegrationOutboundPolicyUpdate(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		d, _, org := setupOrganization(t, s)
		auditPartition(t, d)
		allowLoopback(t, d, org)
		c := clock.NewManual(t0)
		svc := organization.NewService(org, organization.NewSettingsStore(d.Pool),
			audit.NewWriter(logging.New(&bytes.Buffer{}, logging.LevelInfo), c), c)
		before, err := svc.OutboundPolicy(ctx)
		if err != nil || before.Version != 1 || !slices.Equal(before.Allowed, []string{"127.0.0.0/8"}) {
			t.Fatalf("read = %+v, %v", before, err)
		}
		conn, err := pgx.Connect(ctx, d.Pool.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close(context.Background()) }()
		for _, ch := range []string{organization.OutboundPolicyChannel, db.HintChannel} {
			if _, err := conn.Exec(ctx, "LISTEN "+ch); err != nil {
				t.Fatal(err)
			}
		}
		stale := int64(7)
		if _, err := svc.UpdateOutboundPolicy(ctx, audit.System, audit.TransportAPI, netip.Addr{}, &stale,
			organization.OutboundPolicyInput{Mode: organization.OutboundStrict}); !errors.Is(err,
			organization.ErrVersionMismatch) {
			t.Fatalf("a stale version: %v", err)
		}
		v := before.Version
		got, err := svc.UpdateOutboundPolicy(ctx, audit.System, audit.TransportAPI, netip.Addr{}, &v,
			organization.OutboundPolicyInput{Mode: organization.OutboundStandard, Allowed: []string{},
				Denied: []string{"*.corp.example"}})
		if err != nil || got.Version != 2 || len(got.Allowed) != 0 || got.Denied[0] != "*.corp.example" {
			t.Fatalf("update = %+v, %v", got, err)
		}
		channels := map[string]string{}
		for range 2 {
			wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			n, err := conn.WaitForNotification(wctx)
			cancel()
			if err != nil {
				t.Fatalf("notifications %v: %v", channels, err)
			}
			channels[n.Channel] = n.Payload
		}
		if channels[organization.OutboundPolicyChannel] == "" || channels[db.HintChannel] == "" {
			t.Errorf("notifications = %v", channels)
		}
		var action, diff string
		if err := d.Pool.QueryRow(ctx, `SELECT action, diff::text FROM audit_log WHERE org_id = $1
			ORDER BY id DESC LIMIT 1`, org).Scan(&action, &diff); err != nil || action != "outbound_policy.updated" {
			t.Errorf("audit %s %s, %v", action, diff, err)
		}
		var stored []string
		if err := d.Pool.QueryRow(ctx, `SELECT allowed FROM outbound_policies WHERE org_id = $1`, org).Scan(
			&stored); err != nil || len(stored) != 0 {
			t.Errorf("stored %v, %v", stored, err)
		}
	})
}

// TestIntegrationOutboundPolicySecondReplica is C-20.FR-3 across replicas: two replicas cache the policy, with a real
// clock that never moves, so that the age of the cache cannot explain a refresh. A change on replica A applies on A
// as soon as it commits, and on replica B as soon as B's LISTEN delivers the notification, which also closes B's idle
// outbound connections.
func TestIntegrationOutboundPolicySecondReplica(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		d, _, org := setupOrganization(t, s)
		auditPartition(t, d)
		allowLoopback(t, d, org)
		stopped := clock.NewManual(t0)
		a := organization.NewOutboundPolicies(organization.NewStore(d.Pool), org, stopped)
		b := organization.NewOutboundPolicies(organization.NewStore(d.Pool), org, stopped)
		var closedB atomic.Int32
		b.SetCloseIdle(func() { closedB.Add(1) })
		loopback := func(o *organization.OutboundPolicies) bool {
			t.Helper()
			p, err := o.OutboundPolicy(ctx)
			if err != nil {
				t.Fatal(err)
			}
			return len(p.Allowed) == 1
		}
		if !loopback(a) || !loopback(b) {
			t.Fatal("the replicas did not read the policy")
		}
		log := logging.New(&bytes.Buffer{}, logging.LevelInfo)
		listener := db.NewListener(d.SessionListenConn, log)
		invalidated := make(chan struct{}, 1)
		listener.Listen(organization.OutboundPolicyChannel, func(string) {
			b.Invalidate()
			invalidated <- struct{}{}
		})
		lctx, stop := context.WithCancel(ctx)
		defer stop()
		listening := make(chan bool, 4)
		go listener.Run(lctx, func(db.Hint) {}, func(restored bool) { listening <- restored })
		<-listening

		c := clock.NewManual(t0)
		svc := organization.NewService(org, organization.NewSettingsStore(d.Pool),
			audit.NewWriter(log, c), c).WithOutboundPolicyChanged(a.Invalidate)
		if _, err := svc.UpdateOutboundPolicy(ctx, audit.System, audit.TransportAPI, netip.Addr{}, nil,
			organization.OutboundPolicyInput{Mode: organization.OutboundStandard}); err != nil {
			t.Fatal(err)
		}
		if loopback(a) {
			t.Error("replica A still allows loopback after its own change")
		}
		select {
		case <-invalidated:
		case <-time.After(10 * time.Second):
			t.Fatal("replica B got no notification")
		}
		if loopback(b) {
			t.Error("replica B still allows loopback after the notification")
		}
		if closedB.Load() < 1 {
			t.Error("replica B did not close its idle connections")
		}
		// The policy B serves now blocks loopback.
		p, _ := b.OutboundPolicy(ctx)
		if p.Mode != outbound.ModeStandard || len(p.Allowed) != 0 {
			t.Errorf("B serves %+v", p)
		}
	})
}

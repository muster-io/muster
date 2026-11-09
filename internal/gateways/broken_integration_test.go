// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package gateways

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/connections"
	connectionsdb "github.com/muster-io/muster/internal/connections/dbgen"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/delivery"
	deliverydb "github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/integrations"
	"github.com/muster-io/muster/internal/internalalerts"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mattermost"
	"github.com/muster-io/muster/internal/outbound"
)

// brokenIntEnv is a callEnv whose gateway is a bridge of both roles, with the built-in Integration, the partitions of the
// synthetic Stored Snapshots, and a Bridge Destination of the same Connection with a pending delivery.
type brokenIntEnv struct {
	*callEnv
	dest   int64
	prober *prober
}

func setupBroken(t *testing.T, s dbtest.Server, people int) *brokenIntEnv {
	t.Helper()
	e := &brokenIntEnv{callEnv: setupCalls(t, s, people), prober: &prober{}}
	ctx := t.Context()
	if err := integrations.EnsureBuiltin(ctx, integrations.NewStore(e.d.Pool), e.orgID, c0); err != nil {
		t.Fatal(err)
	}
	for day := c0.Truncate(24*time.Hour).AddDate(0, 0, -1); day.Before(c0.AddDate(0, 0, 3)); day = day.AddDate(0, 0,
		1) {
		next, name := day.AddDate(0, 0, 1), day.Format("20060102")
		e.exec(t, `CREATE TABLE stored_snapshots_p`+name+` PARTITION OF stored_snapshots FOR VALUES FROM ('`+
			day.Format(time.RFC3339)+`') TO ('`+next.Format(time.RFC3339)+`')`)
		e.exec(t, `CREATE TABLE snapshot_bodies_p`+name+` PARTITION OF snapshot_bodies FOR VALUES FROM ('`+
			day.Format(time.DateOnly)+`') TO ('`+next.Format(time.DateOnly)+`')`)
	}
	// The Audit log of the Connection's changes and the delivery events of its Destination.
	for _, table := range []string{"audit_log", "delivery_events"} {
		e.exec(t, `CREATE TABLE `+table+`_p202611 PARTITION OF `+table+`
			FOR VALUES FROM ('2026-11-01 00:00:00+00') TO ('2026-12-01 00:00:00+00')`)
		e.exec(t, `CREATE TABLE `+table+`_p202612 PARTITION OF `+table+`
			FOR VALUES FROM ('2026-12-01 00:00:00+00') TO ('2027-01-01 00:00:00+00')`)
	}
	e.exec(t, `UPDATE connections SET bridge_info = '{"roles": ["messenger", "gateway"]}' WHERE id = $1`, e.gw)
	e.dest = e.id(t, `INSERT INTO destinations (org_id, public_id, type, name, connection_id, bridge_channel_id, mentions,
		limiter_limit, limiter_per_seconds, health, created_at, updated_at)
		VALUES ($1, 'DS0000000000B1', 'bridge', 'alerts', $2, 'C1', '{}', 1000, 1, 'healthy', $3, $3) RETURNING id`,
		e.orgID, e.gw, c0)
	g := e.group(t, "Delivered", "warning")
	e.exec(t, `INSERT INTO deliveries (org_id, destination_id, alert_group_id, state, next_attempt_at, created_at,
		updated_at) VALUES ($1, $2, $3, 'pending', $4, $4, $4)`, e.orgID, e.dest, g, c0)
	return e
}

// brokenSender is a gateway sender of the replica owner with the Broken state over the database.
func (e *brokenIntEnv) brokenSender(owner string, g delivery.Gateway) *Sender {
	s := e.sender(owner, g)
	s.Broken = &Broken{OrgID: e.orgID, Store: NewBrokenStore(e.d.Pool), Lease: s.Lease, Prober: e.prober,
		Log: s.Log}
	return s
}

// state is the Broken state of the gateway as the read of its Connection gives it: broken_since, the reason and the
// next probe, "-" for null.
func (e *brokenIntEnv) state(t *testing.T) string {
	t.Helper()
	var s string
	if err := e.d.Pool.QueryRow(t.Context(), `SELECT coalesce(to_char(gateway_broken_since AT TIME ZONE 'UTC',
		'HH24:MI:SS'), '-') || ' ' || coalesce(gateway_broken_reason, '-') || ' ' ||
		coalesce(to_char(gateway_next_probe_at AT TIME ZONE 'UTC', 'HH24:MI:SS'), '-')
		FROM connections WHERE id = $1`, e.gw).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// raises counts the synthetic Stored Snapshots of MusterNotificationGatewayBroken with status about the gateway, a raise
// with its name.
func (e *brokenIntEnv) raises(t *testing.T, status string) int {
	t.Helper()
	return e.count(t, `SELECT count(*) FROM stored_snapshots s
		JOIN snapshot_bodies b ON b.body_sha256 = s.body_sha256 AND b.body_day = s.body_day
		WHERE s.source = 'internal'
		  AND convert_from(b.body, 'UTF8')::jsonb->'groupLabels'->>'alertname' = 'MusterNotificationGatewayBroken'
		  AND convert_from(b.body, 'UTF8')::jsonb->>'status' = $1
		  AND convert_from(b.body, 'UTF8')::jsonb->'alerts'->0->'labels'->>'connection' = 'CN0000000000G1'
		  AND ($1 = 'resolved'
		       OR convert_from(b.body, 'UTF8')::jsonb->'alerts'->0->'labels'->>'connection_name' = 'Example Gateway')`,
		status)
}

// claimDelivery claims the pending delivery of the Destination, as the delivery worker does, and frees it again.
func (e *brokenIntEnv) claimDelivery(t *testing.T) int {
	t.Helper()
	rows, err := deliverydb.New(e.d.Pool).ClaimDueDeliveries(t.Context(), deliverydb.ClaimDueDeliveriesParams{
		OrgID: e.orgID, Owner: "w1", LeaseUntil: e.real.Now().Add(time.Minute), Due: e.business.Now(),
		Now: e.real.Now(), Lim: 10})
	if err != nil {
		t.Fatal(err)
	}
	e.exec(t, `UPDATE deliveries SET lease_owner = NULL, lease_until = NULL`)
	return len(rows)
}

// TestIntegrationGatewayBroken is C-30.FR-8 and AC-5 on PostgreSQL, with a bridge of both roles: a call the gateway
// answers 401 ends Not delivered with call_refused and makes the gateway Broken, MusterNotificationGatewayBroken
// fires about the Connection with its name and the gauge is 1, while the Destination of the same bridge stays healthy
// and its delivery is claimed; a call that comes due while Broken ends Not delivered with gateway_broken, unsent, and
// records page_not_delivered with it; the probe through info fails and keeps the state with its reason, then succeeds:
// the state ends, the alert resolves, the gauge is 0, and nothing is sent afterwards.
func TestIntegrationGatewayBroken(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, srv dbtest.Server) {
		e := setupBroken(t, srv, 1)
		e.gateway.fail = []delivery.Outcome{{Kind: delivery.OutcomeFatal, Status: 401,
			Error: "POST notifications: 401 Unauthorized"}}
		s := e.brokenSender("r1", e.gateway)
		p1 := e.users[0]
		g1 := e.group(t, "Disk1", "critical")
		e.page(t, p1, g1)
		e.round(t, s)
		if n := e.count(t, `SELECT count(*) FROM gateway_calls WHERE state = 'not_delivered'
			AND reason = 'call_refused'`); n != 1 {
			t.Fatalf("%d refused calls", n)
		}
		if got := e.state(t); got != "03:25:00 POST notifications: 401 Unauthorized 03:30:00" {
			t.Fatalf("the state %q", got)
		}
		if n := e.raises(t, "firing"); n != 1 {
			t.Errorf("%d raises", n)
		}
		if err := s.Broken.Export(t.Context()); err != nil || gauge(t, "CN0000000000G1") != "1" {
			t.Errorf("the gauge %q, %v", gauge(t, "CN0000000000G1"), err)
		}
		if e.count(t, `SELECT count(*) FROM destinations WHERE id = $1 AND health = 'healthy'`, e.dest) != 1 ||
			e.claimDelivery(t) != 1 {
			t.Error("the Destination of the Broken gateway does not deliver")
		}
		if !strings.Contains(e.log.String(), `"event":"gateway_broken","connection":"CN0000000000G1","cause":"fatal"`) {
			t.Errorf("the log:\n%s", e.log)
		}

		// A call due while Broken: Not delivered with gateway_broken, unsent.
		e.business.Advance(2 * CombineWindow)
		g2 := e.group(t, "Disk2", "warning")
		e.page(t, p1, g2)
		e.round(t, s)
		if len(e.gateway.sentTexts()) != 0 {
			t.Fatalf("a call went through the Broken gateway: %v", e.gateway.sentTexts())
		}
		if n := e.count(t, `SELECT count(*) FROM timeline_entries WHERE alert_group_id = $1
			AND event = 'page_not_delivered' AND reason = 'gateway_broken' AND gateway_connection_id = $2
			AND paged_user_ids = ARRAY[$3]::bigint[]`, g2, e.gw, p1); n != 1 {
			t.Errorf("%d entries of page_not_delivered with gateway_broken", n)
		}

		// The probe fails, then succeeds.
		e.prober.outcomes = []delivery.Outcome{{Kind: delivery.OutcomeTransient, Status: 503, Error: "GET info: 503"}}
		e.business.Advance(BrokenProbeInterval)
		e.round(t, s)
		if got := e.state(t); got != "03:25:00 GET info: 503 03:37:00" || len(e.prober.probed) != 1 {
			t.Fatalf("after the failed probe %q, probed %v", got, e.prober.probed)
		}
		e.business.Advance(BrokenProbeInterval)
		e.round(t, s)
		if got := e.state(t); got != "- - -" || e.raises(t, "resolved") != 1 {
			t.Fatalf("after the probe %q, %d resolves", got, e.raises(t, "resolved"))
		}
		if e.count(t, `SELECT count(*) FROM connections WHERE id = $1 AND gateway_probe_lease_owner IS NULL`,
			e.gw) != 1 {
			t.Error("the lease of the probe is left")
		}
		if err := s.Broken.Export(t.Context()); err != nil || gauge(t, "CN0000000000G1") != "0" {
			t.Errorf("the gauge %q, %v", gauge(t, "CN0000000000G1"), err)
		}
		if !strings.Contains(e.log.String(), `"event":"gateway_recovered","connection":"CN0000000000G1","by":"probe"`) {
			t.Errorf("the log:\n%s", e.log)
		}
		e.business.Advance(time.Hour)
		e.round(t, s)
		if len(e.gateway.sentTexts()) != 0 {
			t.Errorf("a call due while Broken was sent after the recovery: %v", e.gateway.sentTexts())
		}
	})
}

// TestIntegrationGatewayBrokenByBudget is C-30.FR-8 on PostgreSQL: a call that used up delivery.transient_budget
// makes the gateway Broken with the cause unavailable; a Broken Destination of the same bridge leaves the gateway as it
// is and its calls go; a check of the Connection ends the state, and its deletion and rename work in the
// transaction of the change.
func TestIntegrationGatewayBrokenByBudget(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, srv dbtest.Server) {
		e := setupBroken(t, srv, 1)
		ctx := t.Context()
		// A Broken Destination: the gateway stays in order and calls.
		if _, err := deliverydb.New(e.d.Pool).BreakDestination(ctx, deliverydb.BreakDestinationParams{OrgID: e.orgID,
			ID: e.dest, Cause: "fatal", Reason: "channel gone", Now: c0, NextProbe: c0.Add(BrokenProbeInterval)}); err != nil {
			t.Fatal(err)
		}
		s := e.brokenSender("r1", e.gateway)
		p1 := e.users[0]
		e.page(t, p1, e.group(t, "Disk1", "warning"))
		e.round(t, s)
		if got := e.state(t); got != "- - -" || len(e.gateway.sentTexts()) != 1 {
			t.Fatalf("with a Broken Destination: %q, %d calls", got, len(e.gateway.sentTexts()))
		}

		// The budget runs out.
		e.business.Advance(2 * CombineWindow)
		for range delivery.TransientBudgetAttempts {
			e.gateway.fail = append(e.gateway.fail, delivery.Outcome{Kind: delivery.OutcomeTransient,
				Error: "bridge down"})
		}
		e.page(t, p1, e.group(t, "Disk2", "warning"))
		for range delivery.TransientBudgetAttempts {
			e.round(t, s)
			e.business.Advance(delivery.TransientBackoffMax)
			if e.count(t, `SELECT count(*) FROM gateway_calls WHERE reason = 'call_budget_used'`) == 1 {
				break
			}
		}
		if !strings.Contains(e.state(t), " unavailable after repeated failures: bridge down ") ||
			!strings.Contains(e.log.String(), `"event":"gateway_broken","connection":"CN0000000000G1",`+
				`"cause":"unavailable"`) {
			t.Fatalf("after the budget %q", e.state(t))
		}
		if e.count(t, `SELECT count(*) FROM destinations WHERE id = $1 AND health = 'broken'`, e.dest) != 1 {
			t.Error("the gateway changed the Destination")
		}

		// A rename in the transaction of the change raises the alert again with the new name.
		if err := pgx.BeginFunc(ctx, e.d.Pool, func(tx pgx.Tx) error {
			return s.Broken.Renamed(ctx, tx, "CN0000000000G1", "Voice")
		}); err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM stored_snapshots s
			JOIN snapshot_bodies b ON b.body_sha256 = s.body_sha256 AND b.body_day = s.body_day
			WHERE convert_from(b.body, 'UTF8')::jsonb->'alerts'->0->'labels'->>'connection_name' = 'Voice'`); n != 1 {
			t.Errorf("%d raises with the new name", n)
		}
		// The check ends it.
		if err := s.Broken.EndBroken(ctx, e.gw); err != nil || e.state(t) != "- - -" || e.raises(t, "resolved") != 1 {
			t.Fatalf("after the check %q, %v", e.state(t), err)
		}
		if !strings.Contains(e.log.String(), `"by":"check"`) {
			t.Errorf("the log:\n%s", e.log)
		}
		// Broken again, then deleted: the alert resolves in the deletion's transaction.
		if err := s.Broken.Break(ctx, brokenGateway{id: e.gw, publicID: "CN0000000000G1"}, causeFatal,
			"401"); err != nil {
			t.Fatal(err)
		}
		if err := pgx.BeginFunc(ctx, e.d.Pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE connections SET deleted_at = $2 WHERE id = $1`, e.gw, c0); err != nil {
				return err
			}
			return s.Broken.Deleted(ctx, tx, e.gw)
		}); err != nil {
			t.Fatal(err)
		}
		if e.state(t) != "- - -" || e.raises(t, "resolved") != 2 {
			t.Errorf("after the deletion %q", e.state(t))
		}
		if err := s.Broken.Export(ctx); err != nil || gauge(t, "CN0000000000G1") != "" {
			t.Errorf("the gauge of a deleted gateway %q, %v", gauge(t, "CN0000000000G1"), err)
		}
	})
}

// slowProber holds every probe until released, so that two replicas probe at once.
type slowProber struct {
	mu      sync.Mutex
	probes  int
	started chan struct{}
	release chan struct{}
}

func (p *slowProber) ProbeGateway(context.Context, int64) delivery.Outcome {
	p.mu.Lock()
	p.probes++
	p.mu.Unlock()
	p.started <- struct{}{}
	<-p.release
	return delivery.Outcome{Kind: delivery.OutcomeTransient, Error: "still down"}
}

// TestIntegrationGatewayBrokenConcurrency is NFR-4 and the lock order of broken.go on PostgreSQL: two replicas probe
// a Broken gateway once between them, the other skipping the row its claim locked or leased; and the transactions of
// the Broken state — Break after a call's end, the end of a check, the claim and record of a probe — run against the
// calls of the same gateway — Pages, attempts and ends Not delivered, which hold Alert Groups, the person's lock and
// calls — and against the changes of its Connection — renames through connections.Service, the Broken state of its
// Destination, which delivery makes and ends, and one deletion during the storm — on many goroutines without a
// deadlock: none waits for a lock another holds while waiting for one of its own, as the state takes only the
// Connection's row, the calls never take it, and the Connection's changes take its row before the Internal alerts.
// Between the first storm and the second, which deletes the Connection, a call that comes due after a last End is
// accepted.
func TestIntegrationGatewayBrokenConcurrency(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, srv dbtest.Server) {
		e := setupBroken(t, srv, 4)
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		slow := &slowProber{started: make(chan struct{}, 2), release: make(chan struct{})}
		a, b := e.brokenSender("r1", e.gateway), e.brokenSender("r2", e.gateway)
		a.Broken.Prober, b.Broken.Prober = slow, slow
		if err := a.Broken.Break(ctx, brokenGateway{id: e.gw, publicID: "CN0000000000G1"}, causeFatal,
			"401"); err != nil {
			t.Fatal(err)
		}
		e.business.Advance(BrokenProbeInterval)
		var wg sync.WaitGroup
		wg.Go(func() {
			if err := a.Broken.Probe(ctx); err != nil {
				t.Error(err)
			}
		})
		<-slow.started
		if err := b.Broken.Probe(ctx); err != nil {
			t.Error(err)
		}
		close(slow.release)
		wg.Wait()
		a.Broken.Wait()
		if slow.probes != 1 || !strings.Contains(e.state(t), " still down ") {
			t.Fatalf("%d probes, the state %q", slow.probes, e.state(t))
		}

		// The calls and the state at once.
		gw := &flakyGateway{}
		for _, s := range []*Sender{a, b} {
			s.Gateway = gw
			s.Broken.Prober = &prober{}
		}
		groups := make([]int64, 8)
		for i := range groups {
			groups[i] = e.group(t, "Disk", "warning")
		}
		var errs sync.Map
		var deleted atomic.Bool
		gone := func(err error) error {
			if deleted.Load() && (errors.Is(err, connections.ErrNotFound) || errors.Is(err, ErrNoPage)) {
				return nil
			}
			return err
		}
		run := func(name string, f func(i int) error) {
			wg.Go(func() {
				for i := range 30 {
					if ctx.Err() != nil {
						return
					}
					if err := f(i); err != nil {
						errs.Store(name, err)
						return
					}
				}
			})
		}
		page := func(i int) error {
			return pgx.BeginFunc(ctx, e.d.Pool, func(tx pgx.Tx) error {
				// The Alert Group first, as a chain's step does, then the Page.
				if _, err := tx.Exec(ctx, `SELECT id FROM alert_groups WHERE id = $1 FOR UPDATE`,
					groups[i%len(groups)]); err != nil {
					return err
				}
				_, err := e.calls.Page(ctx, tx, PageCall{UserID: e.users[i%len(e.users)], GatewayID: e.gw,
					AlertGroupID: groups[i%len(groups)], Stop: StopNotFiring})
				return err
			})
		}
		// The Connection's own changes: a rename, which raises the open alert again in its transaction; the Broken
		// state of its Destination, which delivery makes and ends; and, in the second storm, its deletion, which ends
		// the state.
		conns := e.connectionsService(t, a.Broken)
		dsvc := delivery.New(delivery.Config{OrgID: e.orgID, Store: delivery.NewStore(e.d.Pool, e.d.Pool),
			Business: e.business, Log: logging.New(&syncWriter{w: e.log}, logging.LevelInfo)})
		by := connections.Requester{Actor: audit.User(e.users[0], "SR00000000000001"), Transport: audit.TransportAPI}
		storm := func(withDelete bool) {
			var pages atomic.Int32
			run("page", func(i int) error {
				pages.Add(1)
				return gone(page(i))
			})
			run("page2", func(i int) error { return gone(page(i + 1)) })
			run("r1", func(int) error {
				e.business.Advance(CombineWindow)
				_, err := a.Round(ctx)
				return err
			})
			run("r2", func(int) error {
				_, err := b.Round(ctx)
				return err
			})
			run("check", func(int) error { return a.Broken.EndBroken(ctx, e.gw) })
			run("break", func(int) error {
				return b.Broken.Break(ctx, brokenGateway{id: e.gw, publicID: "CN0000000000G1"}, causeFatal, "401")
			})
			run("gauge", func(int) error { return a.Broken.Export(ctx) })
			// The Leader's reconciliation, which locks the Connection's row before it resolves an orphan alert.
			run("reconcile", func(int) error { return b.Broken.Reconcile(ctx) })
			run("rename", func(i int) error {
				_, err := conns.Update(ctx, by, "CN0000000000G1", nil, connections.Input{Type: connections.TypeBridge,
					Name: fmt.Sprintf("Gateway %d", i), BridgeBaseURL: "http://gw", EventDelivery: "poll",
					Limiter: connections.Limiter{Limit: 1000, PerSeconds: 1}})
				return gone(err)
			})
			run("destination", func(i int) error {
				if i%2 == 0 {
					_, err := dsvc.MarkHealthy(ctx, nil, e.dest)
					return err
				}
				return pgx.BeginFunc(ctx, e.d.Pool, func(tx pgx.Tx) error {
					row, err := deliverydb.New(tx).BreakDestination(ctx, deliverydb.BreakDestinationParams{OrgID: e.orgID,
						ID: e.dest, Cause: "fatal", Reason: "gone", Now: e.business.Now(),
						NextProbe: e.business.Now().Add(BrokenProbeInterval)})
					if errors.Is(err, pgx.ErrNoRows) {
						return nil
					}
					if err != nil {
						return err
					}
					return internalalerts.NewRaiser(e.orgID, "").Raise(ctx, internalalerts.NewStore(tx), e.business.Now(),
						internalalerts.DestinationBroken, internalalerts.Entity{ID: row.PublicID, Name: row.Name}, nil)
				})
			})
			if withDelete {
				wg.Go(func() {
					// Mid-storm: after a third of the Pages of one runner, which always makes all of its.
					for pages.Load() < 10 && ctx.Err() == nil {
						time.Sleep(10 * time.Millisecond)
					}
					e.exec(t, `UPDATE destinations SET deleted_at = $2 WHERE id = $1`, e.dest, c0)
					deleted.Store(true)
					if err := conns.Delete(ctx, by, "CN0000000000G1", nil); err != nil {
						errs.Store("delete", err)
					}
				})
			}
			// Pages and the sender's rounds take the deletion as their gateway being gone.
			run("page3", func(i int) error { return gone(page(i + 2)) })
			wg.Wait()
			a.Broken.Wait()
			b.Broken.Wait()
			if ctx.Err() != nil {
				t.Fatal("the transactions did not finish: a lock wait that never ends")
			}
		}

		storm(false)
		// Liveness: once a last End follows every Break, a call that comes due afterwards goes, to a person without
		// earlier calls, so that no combine window or earlier due time decides it.
		if err := a.Broken.EndBroken(ctx, e.gw); err != nil || !strings.HasPrefix(e.state(t), "- - -") {
			t.Fatalf("the last end %q, %v", e.state(t), err)
		}
		e.business.Advance(time.Second)
		fresh := e.id(t, `INSERT INTO users (org_id, public_id, login, name, role, source, status, created_at,
			updated_at) VALUES ($1, 'SR0000000000FR', 'fresh', 'fresh', 'responder', 'local', 'active', $2, $2)
			RETURNING id`, e.orgID, c0)
		e.id(t, `INSERT INTO gateway_contacts (org_id, public_id, user_id, connection_id, contact, confirmed_at,
			created_at) VALUES ($1, 'GC0000000000FR', $2, $3, '+10000009999', $4, $4) RETURNING id`, e.orgID, fresh,
			e.gw, c0)
		e.page(t, fresh, e.group(t, "Fresh", "warning"))
		accepting := &keyedGateway{}
		a.Gateway = accepting
		e.round(t, a)
		if n := e.count(t, `SELECT count(*) FROM gateway_calls WHERE user_id = $1 AND state = 'accepted'`,
			fresh); n != 1 || len(accepting.sentTexts()) == 0 {
			t.Fatalf("the call after the last end: %d accepted, %d sent", n, len(accepting.sentTexts()))
		}
		a.Gateway = gw
		storm(true)
		errs.Range(func(k, v any) bool {
			var pe *pgconn.PgError
			if err := v.(error); errors.As(err, &pe) && pe.Code == "40P01" {
				t.Errorf("%s: a deadlock: %v; %s", k, err, pe.Detail)
			} else {
				t.Errorf("%s: %v", k, err)
			}
			return true
		})
		if n := e.count(t, `SELECT count(*) FROM connections WHERE id = $1 AND gateway_probe_lease_owner IS NOT NULL
			AND gateway_broken_since IS NULL`, e.gw); n != 0 {
			t.Error("a lease outlived the state")
		}
		if n := e.count(t, `SELECT count(*) FROM connections WHERE id = $1 AND deleted_at IS NOT NULL
			AND gateway_broken_since IS NULL`, e.gw); n != 1 {
			t.Error("the deleted gateway is not deleted, or still Broken")
		}
		if n := e.count(t, `SELECT count(*) FROM audit_log WHERE action = 'connection.updated'`); n == 0 {
			t.Error("no rename went through")
		}
		// The attempts log their failures instead of returning them.
		if strings.Contains(e.log.String(), "40P01") || strings.Contains(e.log.String(), "deadlock") ||
			strings.Contains(e.log.String(), `"event":"gateway_call_failed"`) {
			t.Errorf("a call failed:\n%s", e.log)
		}
		// A call that came due before the state ended last is never sent (P-68), so most calls end gateway_broken.
		if e.count(t, `SELECT count(*) FROM gateway_calls WHERE reason = 'gateway_broken'`) == 0 {
			t.Errorf("the calls did not meet the state: %d sent", gw.n)
		}
	})
}

// connectionsService is the connections.Service of the Organization over the database, with the hooks of the Broken
// state of broken; it opens a Keyring of its own.
func (e *brokenIntEnv) connectionsService(t *testing.T, broken *Broken) *connections.Service {
	t.Helper()
	ctx := t.Context()
	logger := logging.New(&syncWriter{w: e.log}, logging.LevelInfo)
	keys, err := keyring.New([][]byte{bytes.Repeat([]byte{'k'}, keyring.KeySize)}, false)
	if err != nil {
		t.Fatal(err)
	}
	state, err := keys.Establish(ctx, keyring.NewStore(e.d.Pool), c0)
	if err != nil {
		t.Fatal(err)
	}
	if err := keys.Open(ctx, logger, state); err != nil {
		t.Fatal(err)
	}
	policy, err := outbound.ParsePolicy("standard", []string{"127.0.0.0/8"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return connections.New(connections.Config{OrgID: e.orgID, Store: connections.NewStore(e.d.Pool), Keyring: keys,
		Audit: audit.NewWriter(logger, e.business), Clocks: clock.Clocks{Business: e.business, Real: e.real},
		Log: logger, IngestURL: &url.URL{Scheme: "http", Host: "localhost:8081"},
		Network: mattermost.Network{Policy: outbound.StaticPolicy(policy), Log: logger, Real: clock.Real{}},
		Gateways: connections.Gateways{Checked: broken.EndBroken,
			Renamed: func(ctx context.Context, tx connectionsdb.DBTX, publicID, name string) error {
				return broken.Renamed(ctx, tx, publicID, name)
			},
			Deleted: func(ctx context.Context, tx connectionsdb.DBTX, id int64) error {
				return broken.Deleted(ctx, tx, id)
			}}})
}

// TestIntegrationGatewayBrokenEndedWhilePrepared is P-68 across replicas on PostgreSQL: replica r2 claims a call that
// came due while the gateway was Broken, replica r1's probe ends the state before r2 prepares it, and the call ends
// Not delivered with gateway_broken all the same, unsent, as the end of the state is kept; a call that comes due after
// it goes.
func TestIntegrationGatewayBrokenEndedWhilePrepared(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, srv dbtest.Server) {
		e := setupBroken(t, srv, 1)
		a, b := e.brokenSender("r1", e.gateway), e.brokenSender("r2", e.gateway)
		if err := a.Broken.Break(t.Context(), brokenGateway{id: e.gw, publicID: "CN0000000000G1"}, causeFatal,
			"401"); err != nil {
			t.Fatal(err)
		}
		g := e.group(t, "Disk", "warning")
		e.page(t, e.users[0], g)
		rows, err := b.Store.ClaimDueCalls(t.Context(), claimParams(b))
		if err != nil || len(rows) != 1 {
			t.Fatal(rows, err)
		}
		e.business.Advance(BrokenProbeInterval)
		err = a.Broken.Probe(t.Context())
		a.Broken.Wait()
		if err != nil || !strings.HasPrefix(e.state(t), "- - -") {
			t.Fatalf("the probe %q, %v", e.state(t), err)
		}
		b.attempt(t.Context(), rows[0])
		if len(e.gateway.sentTexts()) != 0 || e.count(t, `SELECT count(*) FROM gateway_calls
			WHERE state = 'not_delivered' AND reason = 'gateway_broken' AND attempts = 0`) != 1 {
			t.Fatalf("the call claimed while Broken: %d sent", len(e.gateway.sentTexts()))
		}
		e.business.Advance(2 * CombineWindow)
		e.page(t, e.users[0], e.group(t, "Disk2", "warning"))
		e.round(t, b)
		if len(e.gateway.sentTexts()) != 1 {
			t.Errorf("the call due after the end: %d sent", len(e.gateway.sentTexts()))
		}
	})
}

// TestIntegrationGatewayBrokenReconcile is C-30.FR-8 and ADR-0007 on PostgreSQL: a replica that ended a call Not
// delivered after a 401 and stopped before it made the gateway Broken, which a sender without the Broken state stands
// for, leaves it to the Leader's reconciliation, which makes the gateway Broken with the call's error, once however
// often it runs; after the state ended, the old failure breaks nothing; neither do a refusal of Muster that never
// reached the gateway nor a used budget older than ReconcileWindow, while a recent one does; and an open
// MusterNotificationGatewayBroken of a gateway in order, and of a deleted Connection, is resolved.
func TestIntegrationGatewayBrokenReconcile(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, srv dbtest.Server) {
		e := setupBroken(t, srv, 2)
		ctx := t.Context()
		crashed := e.sender("r1", e.gateway)
		e.gateway.fail = []delivery.Outcome{{Kind: delivery.OutcomeFatal, Status: 401,
			Error: "POST notifications: 401 Unauthorized"}}
		e.page(t, e.users[0], e.group(t, "Disk1", "warning"))
		e.round(t, crashed)
		if got := e.state(t); got != "- - -" {
			t.Fatalf("Broken without the reconciliation: %q", got)
		}
		leader := e.brokenSender("r2", e.gateway).Broken
		for range 2 {
			if err := leader.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if got := e.state(t); !strings.Contains(got, " POST notifications: 401 Unauthorized ") ||
			e.raises(t, "firing") != 1 {
			t.Fatalf("after the reconciliation %q, %d raises", got, e.raises(t, "firing"))
		}
		e.business.Advance(time.Second)
		if err := leader.EndBroken(ctx, e.gw); err != nil {
			t.Fatal(err)
		}
		if err := leader.Reconcile(ctx); err != nil || e.state(t) != "- - -" {
			t.Fatalf("broken again for the old failure: %q, %v", e.state(t), err)
		}

		// A refusal of Muster that never reached the gateway ends gateway_deleted and breaks nothing.
		e.business.Advance(2 * CombineWindow)
		e.gateway.fail = []delivery.Outcome{{Kind: delivery.OutcomeFatal, Error: "the bridge does not declare the role " +
			"gateway", NotSent: true}}
		e.page(t, e.users[1], e.group(t, "Disk2", "warning"))
		e.round(t, crashed)
		if n := e.count(t, `SELECT count(*) FROM gateway_calls WHERE reason = 'gateway_deleted'`); n != 1 {
			t.Fatalf("%d calls ended gateway_deleted", n)
		}
		if err := leader.Reconcile(ctx); err != nil || e.state(t) != "- - -" {
			t.Fatalf("a refusal of Muster broke the gateway: %q, %v", e.state(t), err)
		}
		// A used budget older than the window breaks nothing; a recent one does.
		budget := func(id string, ended time.Time) {
			e.exec(t, `INSERT INTO gateway_calls (org_id, public_id, connection_id, user_id, state, language,
				first_attempt_at, ended_at, reason, attempts, last_error_class, last_error, created_at)
				VALUES ($1, $2, $3, $4, 'not_delivered', 'en', $5, $5, 'call_budget_used', 10, 'transient', 'busy', $5)`,
				e.orgID, id, e.gw, e.users[0], ended)
		}
		budget("CA00000000AAA1", e.business.Now().Add(-ReconcileWindow-time.Second))
		if err := leader.Reconcile(ctx); err != nil || e.state(t) != "- - -" {
			t.Fatalf("an old used budget broke the gateway: %q, %v", e.state(t), err)
		}
		budget("CA00000000BBB1", e.business.Now())
		if err := leader.Reconcile(ctx); err != nil ||
			!strings.Contains(e.state(t), " unavailable after repeated failures: busy ") {
			t.Fatalf("a recent used budget: %q, %v", e.state(t), err)
		}
		if err := leader.EndBroken(ctx, e.gw); err != nil {
			t.Fatal(err)
		}

		// Orphan alerts, as a down and up migration leaves them: of the gateway in order, and of a deleted Connection.
		e.exec(t, `INSERT INTO connections (org_id, public_id, type, name, bridge_base_url, bridge_event_delivery,
			bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds, created_at,
			updated_at, deleted_at)
			VALUES ($1, 'CN0000000000G2', 'bridge', 'gone', 'http://gw2', 'poll', '\x00', 'k1', $2, 1, 1, $2, $2, $2)`,
			e.orgID, c0)
		raiser := internalalerts.NewRaiser(e.orgID, "")
		for _, id := range []string{"CN0000000000G1", "CN0000000000G2"} {
			if err := pgx.BeginFunc(ctx, e.d.Pool, func(tx pgx.Tx) error {
				return raiser.Raise(ctx, internalalerts.NewStore(tx), e.business.Now(),
					internalalerts.NotificationGatewayBroken, internalalerts.Entity{ID: id, Name: "x"}, nil)
			}); err != nil {
				t.Fatal(err)
			}
		}
		resolves := func(id string) int {
			return e.count(t, `SELECT count(*) FROM stored_snapshots s
				JOIN snapshot_bodies b ON b.body_sha256 = s.body_sha256 AND b.body_day = s.body_day
				WHERE s.source = 'internal' AND convert_from(b.body, 'UTF8')::jsonb->>'status' = 'resolved'
				  AND convert_from(b.body, 'UTF8')::jsonb->'alerts'->0->'labels'->>'connection' = $1`, id)
		}
		g1, g2 := resolves("CN0000000000G1"), resolves("CN0000000000G2")
		for range 2 {
			if err := leader.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if resolves("CN0000000000G1") != g1+1 || resolves("CN0000000000G2") != g2+1 {
			t.Errorf("the orphan alerts: %d and %d resolves, before %d and %d", resolves("CN0000000000G1"),
				resolves("CN0000000000G2"), g1, g2)
		}
	})
}

// flakyGateway answers every other call 401 and accepts the others, so that the state changes while the calls go.
type flakyGateway struct {
	mu sync.Mutex
	n  int
}

func (g *flakyGateway) Notify(context.Context, delivery.Call, delivery.Notification) delivery.Outcome {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	if g.n%2 == 0 {
		return delivery.Outcome{Kind: delivery.OutcomeFatal, Status: 401, Error: "401"}
	}
	return delivery.Outcome{Kind: delivery.OutcomeOK, MessageID: "n"}
}

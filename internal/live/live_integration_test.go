// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package live_test

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/leader"
	"github.com/muster-io/muster/internal/live"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func next(t *testing.T, sub <-chan string) string {
	t.Helper()
	select {
	case s := <-sub:
		return s
	case <-time.After(10 * time.Second):
		t.Fatal("no hint arrived")
		return ""
	}
}

// TestIntegrationHints runs the hints of ADR-0009 against PostgreSQL: an Organization update sends an organization
// hint through NOTIFY in its transaction, which the Listener's LISTEN on a session connection passes to the Hub and
// its stream; a refused update, whose transaction rolls back, sends none; and the notice watcher sends a
// system-notices hint when the recovery window that the Leader recorded starts and when it ends on the business
// clock.
func TestIntegrationHints(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		conn := config.Database{URL: logging.Secret(s.NewDatabase(t)), SSLMode: "disable"}
		d, err := db.Open(ctx, conn, conn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(d.Close)
		log := logging.New(&bytes.Buffer{}, logging.LevelInfo)
		if err := d.Migrate(ctx, log); err != nil {
			t.Fatal(err)
		}
		if err := organization.Ensure(ctx, organization.NewStore(d.Pool), log, t0); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Pool.Exec(ctx, `CREATE TABLE audit_log_p202610 PARTITION OF audit_log
			FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2026-11-01 00:00:00+00')`); err != nil {
			t.Fatal(err)
		}
		row, err := organization.NewStore(d.Pool).GetOrganization(ctx)
		if err != nil {
			t.Fatal(err)
		}
		hub := live.NewHub(row.ID, func(_ context.Context, ids []int64) ([]int64, error) { return ids, nil })
		sub, err := hub.Subscribe(live.Subscriber{SessionID: 1, Admin: true})
		if err != nil {
			t.Fatal(err)
		}
		events := make(chan string, 8)
		w := writerFunc(func(p []byte) (int, error) {
			events <- string(p)
			return len(p), nil
		})
		lctx, stop := context.WithCancel(ctx)
		defer stop()
		listening := make(chan bool, 4)
		go db.NewListener(d.SessionListenConn, log).Run(lctx, hub.Receive, func(restored bool) { listening <- restored })
		go func() { _ = hub.Stream(lctx, w, func() error { return nil }, sub) }()
		if next(t, events) != "retry: 3000\n\n" {
			t.Fatal("the stream did not start")
		}
		<-listening

		business := clock.NewManual(t0)
		svc := organization.NewService(row.ID, organization.NewSettingsStore(d.Pool), audit.NewWriter(log, business),
			business)
		o, err := svc.Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		in := organization.Input{Name: "Renamed", TimeZone: o.TimeZone, SeverityLabel: o.SeverityLabel,
			SeverityMapping: o.SeverityMapping, SeverityStyles: o.SeverityStyles, CriticalIsUrgent: o.CriticalIsUrgent,
			InstanceLabels: o.InstanceLabels, Retention: o.Retention, TOTPRequired: organization.TOTPLocalUsers,
			OIDCTokenGraceSeconds: int64(o.OIDCTokenGrace / time.Second)}
		if _, err := svc.Update(ctx, audit.System, audit.TransportUI, netip.Addr{}, nil, in); !errors.As(err,
			new(*organization.UnsupportedError)) {
			t.Fatalf("a changed name: %v", err)
		}
		in.Name = o.Name
		if _, err := svc.Update(ctx, audit.System, audit.TransportUI, netip.Addr{}, &o.Version, in); err != nil {
			t.Fatal(err)
		}
		if got := next(t, events); got != "event: hint\nid: 1\ndata: {\"type\":\"organization\",\"id\":null}\n\n" {
			t.Errorf("the hint of the update = %q", got)
		}

		notices := live.NewNotices(func(ctx context.Context, now time.Time) ([]organization.Notice, error) {
			return leader.Notices(ctx, leader.NewStore(d.Pool), now)
		}, business, hub, log)
		if _, err := d.Pool.Exec(ctx, `INSERT INTO runtime_state (alive_at, updated_at) VALUES ($1, $1)
			ON CONFLICT (singleton) DO UPDATE SET alive_at = $1`,
			t0); err != nil {
			t.Fatal(err)
		}
		notices.Check(ctx)
		if _, err := d.Pool.Exec(ctx, `UPDATE runtime_state SET recovery_until = $1`, t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		notices.Check(ctx)
		if got := next(t, events); got != "event: hint\nid: 2\ndata: {\"type\":\"system-notices\",\"id\":null}\n\n" {
			t.Errorf("the hint of the recovery window = %q", got)
		}
		business.Advance(time.Minute)
		if _, err := d.Pool.Exec(ctx, `UPDATE runtime_state SET alive_at = $1`, business.Now()); err != nil {
			t.Fatal(err)
		}
		notices.Check(ctx)
		if got := next(t, events); got != "event: hint\nid: 3\ndata: {\"type\":\"system-notices\",\"id\":null}\n\n" {
			t.Errorf("the hint of the end of the window = %q", got)
		}
		select {
		case got := <-events:
			t.Errorf("an extra event %q: the refused update sent a hint", got)
		case <-time.After(200 * time.Millisecond):
		}
	})
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

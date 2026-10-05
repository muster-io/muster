// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package leader

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/keyring"
	kdb "github.com/muster-io/muster/internal/keyring/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/partitions"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

func migrated(t *testing.T, s dbtest.Server) (*db.DB, string) {
	t.Helper()
	u := s.NewDatabase(t)
	conn := config.Database{URL: logging.Secret(u), SSLMode: "disable"}
	d, err := db.Open(t.Context(), conn, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	if err := d.Migrate(t.Context(), logging.New(&bytes.Buffer{}, logging.LevelInfo)); err != nil {
		t.Fatal(err)
	}
	return d, u
}

// blackhole forwards TCP connections to a target until Cut: then it forwards nothing more in either direction and
// accepts no new connection, while every connection stays open, like a network that drops every packet.
type blackhole struct {
	ln     net.Listener
	target string
	cut    atomic.Bool
	mu     sync.Mutex
	conns  []net.Conn
}

func newBlackhole(t *testing.T, target string) *blackhole {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &blackhole{ln: ln, target: target}
	t.Cleanup(b.close)
	go b.accept()
	return b
}

func (b *blackhole) accept() {
	for {
		c, err := b.ln.Accept()
		if err != nil {
			return
		}
		b.track(c)
		if b.cut.Load() {
			continue
		}
		var d net.Dialer
		up, err := d.Dial("tcp", b.target)
		if err != nil {
			_ = c.Close()
			continue
		}
		b.track(up)
		go b.pipe(up, c)
		go b.pipe(c, up)
	}
}

func (b *blackhole) track(c net.Conn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.conns = append(b.conns, c)
}

// pipe copies src to dst until src fails; after the cut the bytes are dropped.
func (b *blackhole) pipe(dst, src net.Conn) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if err != nil {
			if !b.cut.Load() {
				_ = dst.Close()
			}
			return
		}
		if !b.cut.Load() {
			if _, err := dst.Write(buf[:n]); err != nil {
				return
			}
		}
	}
}

func (b *blackhole) Cut() { b.cut.Store(true) }

func (b *blackhole) close() {
	_ = b.ln.Close()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range b.conns {
		_ = c.Close()
	}
}

// through is the database URL u reached through the blackhole.
func (b *blackhole) through(t *testing.T, u string) string {
	t.Helper()
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Host = b.ln.Addr().String()
	return parsed.String()
}

// testKeeper is a Keeper on the real clock with short intervals and a task that counts the leaderships it runs in.
type testKeeper struct {
	*Keeper
	log     *syncBuffer
	running atomic.Int64
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

const (
	testPing       = 100 * time.Millisecond
	testFencing    = time.Second
	testServerSide = 2 * time.Second
)

func startKeeper(t *testing.T, id, dbURL string) *testKeeper {
	t.Helper()
	k := &testKeeper{log: &syncBuffer{}}
	connect := Dial(func(ctx context.Context) (*pgx.Conn, error) { return pgx.Connect(ctx, dbURL) }, testServerSide)
	tasks := func() []Task {
		return []Task{{Name: "lead", Every: time.Hour, Run: func(ctx context.Context) error {
			k.running.Add(1)
			<-ctx.Done()
			k.running.Add(-1)
			return nil
		}}}
	}
	k.Keeper = NewKeeper(connect, clock.Real{}, logging.New(k.log, logging.LevelInfo), id, tasks)
	k.pingInterval, k.fencingTimeout = testPing, testFencing
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	ticker := time.NewTicker(testPing)
	go func() {
		defer close(done)
		defer ticker.Stop()
		k.Run(ctx, ticker.C)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return k
}

func waitFor(t *testing.T, within time.Duration, what string, cond func() bool) time.Duration {
	t.Helper()
	begin := time.Now()
	for !cond() {
		if time.Since(begin) > within {
			t.Fatalf("%s did not happen within %v", what, within)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return time.Since(begin)
}

// TestIntegrationLeaderLease: one of two replicas takes the lock; when the Leader's network goes silent, it stops its
// tasks within the fencing timeout, PostgreSQL ends its silent session within the server-side bound, and the other
// replica takes over.
func TestIntegrationLeaderLease(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		_, u := migrated(t, s)
		hole := newBlackhole(t, func() string { p, _ := url.Parse(u); return p.Host }())
		a := startKeeper(t, "a", hole.through(t, u))
		waitFor(t, 5*time.Second, "a taking the lock", a.Leading)
		waitFor(t, time.Second, "a starting its tasks", func() bool { return a.running.Load() == 1 })
		b := startKeeper(t, "b", u)
		time.Sleep(5 * testPing)
		if b.Leading() {
			t.Fatal("b took the lock that a holds")
		}

		hole.Cut()
		fenced := waitFor(t, 3*testFencing, "a fencing", func() bool { return !a.Leading() })
		if fenced > testFencing+2*testPing {
			t.Errorf("a fenced %v after the cut, want within the fencing timeout %v", fenced, testFencing)
		}
		if a.running.Load() != 0 {
			t.Error("a's tasks still run after fencing")
		}
		if !strings.Contains(a.log.String(), `"level":"WARN","event":"leadership_lost","replica":"a"`) {
			t.Errorf("log of a: %s", a.log.String())
		}
		took := fenced + waitFor(t, 3*testServerSide, "b taking the lock", b.Leading)
		if took > testServerSide+testPing+time.Second {
			t.Errorf("b took the lock %v after the cut, want within the server-side bound %v", took, testServerSide)
		}
		t.Logf("after the cut: a fenced in %v (fencing timeout %v), b led after %v (server-side bound %v)", fenced,
			testFencing, took, testServerSide)
		if a.Leading() {
			t.Error("a leads again through the cut network")
		}
		if !strings.Contains(b.log.String(), `"event":"leadership_acquired","replica":"b"`) {
			t.Errorf("log of b: %s", b.log.String())
		}
	})
}

// TestIntegrationOverlappingLeaders runs the Leader tasks of two replicas at once after an outage, as a frozen old
// Leader and its successor would: one downtime period, one runtime state, every partition once, and no error.
func TestIntegrationOverlappingLeaders(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		d, _ := migrated(t, s)
		if _, err := d.Pool.Exec(t.Context(), `INSERT INTO organizations (public_id, name, time_zone, severity_label,
			severity_mapping, severity_styles, critical_is_urgent, instance_labels, retention_stored_snapshots_days,
			retention_alert_details_days, retention_alert_group_summaries_days, retention_audit_log_days,
			totp_required, oidc_token_grace_seconds, created_at, updated_at)
			VALUES ('RG0000000000AA', 'Muster', 'UTC', 'severity', '[]', '[]', true, '{}', 14, 90, 730, 365, 'nobody',
			604800, '2026-10-05', '2026-10-05')`); err != nil {
			t.Fatal(err)
		}
		realClock := clock.NewManual(time.Now())
		business := clock.NewManual(realClock.Now())
		clocks := clock.Clocks{Business: business, Real: realClock}
		var log syncBuffer
		logger := logging.New(&log, logging.LevelInfo)
		maintainer := partitions.New(func(ctx context.Context) (partitions.Session, error) {
			return d.ConnectSession(ctx)
		}, business, logger)
		work := func(id string) []Task {
			return Tasks(Work{
				Alive:              NewAlive(NewStore(d.Pool), clocks, logger, id),
				MaintainPartitions: maintainer.Maintain,
				PruneReplicas: func(ctx context.Context) error {
					return keyring.PruneReplicas(ctx, kdb.New(d.Pool), logger, realClock.Now())
				},
			})()
		}
		// The first Leader marks alive, then everything stops for ten minutes.
		first := work("first")
		if err := first[1].Run(t.Context()); err != nil {
			t.Fatal(err)
		}
		realClock.Advance(10 * time.Minute)
		business.Advance(10 * time.Minute)
		if _, err := d.Pool.Exec(t.Context(), `INSERT INTO replicas (replica_id, version, key_ids, started_at, refreshed_at)
			VALUES ('gone', 'test', '{}', $1, $1)`, realClock.Now().Add(-2*time.Hour)); err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		var mu sync.Mutex
		var errs []error
		for _, tasks := range [][]Task{work("b"), work("c")} {
			for _, task := range tasks {
				wg.Go(func() {
					// Each task runs twice, as the next interval would run it.
					for range 2 {
						if err := task.Run(t.Context()); err != nil {
							mu.Lock()
							errs = append(errs, err)
							mu.Unlock()
						}
					}
				})
			}
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(log.String(), "partition_maintenance_failed") {
			t.Errorf("partition maintenance failed: %s", log.String())
		}
		count := func(sql string) int {
			var n int
			if err := d.Pool.QueryRow(t.Context(), sql).Scan(&n); err != nil {
				t.Fatalf("%s: %v", sql, err)
			}
			return n
		}
		for sql, want := range map[string]int{
			"SELECT count(*) FROM downtime_periods":                                                     1,
			"SELECT count(*) FROM runtime_state":                                                        1,
			"SELECT count(*) FROM replicas":                                                             0,
			"SELECT count(*) FROM pg_inherits WHERE inhparent = 'stored_snapshots'::regclass":           8,
			"SELECT count(*) FROM pg_inherits WHERE inhparent = 'snapshot_bodies'::regclass":            8,
			"SELECT count(*) FROM pg_inherits WHERE inhparent = 'audit_log'::regclass":                  3,
			"SELECT count(*) FROM pg_inherits WHERE inhparent = 'timeline_entries'::regclass":           3,
			"SELECT count(*) FROM pg_inherits WHERE inhparent = 'delivery_events'::regclass":            3,
			"SELECT count(*) FROM runtime_state WHERE recovery_until - updated_at <= interval '15 min'": 1,
		} {
			if got := count(sql); got != want {
				t.Errorf("%s = %d, want %d", sql, got, want)
			}
		}
		if n := strings.Count(log.String(), `"event":"downtime_recorded"`); n != 1 {
			t.Errorf("downtime_recorded logged %d times: %s", n, log.String())
		}
		var duration float64
		if err := d.Pool.QueryRow(t.Context(),
			"SELECT extract(epoch FROM ended_at - started_at)::float8 FROM downtime_periods").Scan(&duration); err != nil ||
			duration < 600 || duration > 601 {
			t.Errorf("the downtime lasted %v s (%v), want about 600", duration, err)
		}
	})
}

// lostStore is a replica's view of the database: while down is set, every call fails as without the database.
type lostStore struct {
	keyring.Store
	down *atomic.Bool
}

var errNoDatabase = errors.New("connect to the database: connection refused")

func (s lostStore) RecordReplica(ctx context.Context, arg kdb.RecordReplicaParams) error {
	if s.down.Load() {
		return errNoDatabase
	}
	return s.Store.RecordReplica(ctx, arg)
}

func (s lostStore) GetActiveKeyID(ctx context.Context) (string, error) {
	if s.down.Load() {
		return "", errNoDatabase
	}
	return s.Store.GetActiveKeyID(ctx)
}

// TestIntegrationDatabaseOutageIsDowntime: every replica stays up while the database is gone for ten minutes. When it
// comes back, replica b refreshes its record before a takes over again; b re-registers, so it does not count as having
// run across the outage, and a records the downtime. A replica that kept the database throughout still hides the gap.
func TestIntegrationDatabaseOutageIsDowntime(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		for _, tt := range []struct {
			name         string
			bLosesTheDB  bool
			wantDowntime int
		}{
			{name: "every replica loses the database", bLosesTheDB: true, wantDowntime: 1},
			{name: "replica b keeps the database", bLosesTheDB: false, wantDowntime: 0},
		} {
			t.Run(tt.name, func(t *testing.T) {
				d, _ := migrated(t, s)
				realClock := clock.NewManual(time.Now())
				business := clock.NewManual(realClock.Now())
				clocks := clock.Clocks{Business: business, Real: realClock}
				var log syncBuffer
				logger := logging.New(&log, logging.LevelInfo)
				key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'k'}, keyring.KeySize))
				k, err := keyring.Load(t.Context(), keyring.Env{Keys: logging.Secret(key), Source: keyring.SecretKeysVar},
					false)
				if err != nil {
					t.Fatal(err)
				}
				st, err := k.Establish(t.Context(), keyring.NewStore(d.Pool), business.Now())
				if err != nil {
					t.Fatal(err)
				}
				if err := k.Open(t.Context(), logger, st); err != nil {
					t.Fatal(err)
				}
				var aDown, bDown atomic.Bool
				recorder := func(id string, down *atomic.Bool) *keyring.Recorder {
					return keyring.NewRecorder(k, lostStore{Store: keyring.NewStore(d.Pool), down: down}, realClock,
						logger, id, id, "test")
				}
				a, b := recorder("a", &aDown), recorder("b", &bDown)
				for _, r := range []*keyring.Recorder{a, b} {
					if err := r.Start(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
				alive := NewAlive(NewStore(d.Pool), clocks, logger, "a")
				if err := alive.TakeOver(t.Context()); err != nil {
					t.Fatal(err)
				}

				// Ten minutes without the database: the refreshes fail, and no Leader marks alive.
				aDown.Store(true)
				bDown.Store(tt.bLosesTheDB)
				for range int(10 * time.Minute / keyring.KeyRecordRefresh) {
					realClock.Advance(keyring.KeyRecordRefresh)
					business.Advance(keyring.KeyRecordRefresh)
					_ = a.Refresh(t.Context())
					_ = b.Refresh(t.Context())
				}

				// The database is back: b refreshes first, then a takes over again.
				aDown.Store(false)
				bDown.Store(false)
				if err := b.Refresh(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err := a.Refresh(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err := alive.TakeOver(t.Context()); err != nil {
					t.Fatal(err)
				}
				var periods int
				var duration float64
				if err := d.Pool.QueryRow(t.Context(), `SELECT count(*),
					coalesce(max(extract(epoch FROM ended_at - started_at)), 0)::float8 FROM downtime_periods`,
				).Scan(&periods, &duration); err != nil {
					t.Fatal(err)
				}
				if periods != tt.wantDowntime {
					t.Fatalf("%d downtime periods, want %d; log:\n%s", periods, tt.wantDowntime, log.String())
				}
				if tt.wantDowntime == 1 && (duration < 600 || duration > 601) {
					t.Errorf("the downtime lasted %v s, want about 600", duration)
				}
				if tt.wantDowntime == 1 && !strings.Contains(log.String(), `"event":"downtime_recorded"`) {
					t.Errorf("downtime_recorded was not logged: %s", log.String())
				}
			})
		}
	})
}

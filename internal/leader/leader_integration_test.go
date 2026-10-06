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

	"github.com/muster-io/muster/internal/auth"
	authdb "github.com/muster-io/muster/internal/auth/dbgen"
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

// testKeeper is a Keeper whose lease runs on a manual clock and whose steps the test takes itself, with a task that
// counts the leaderships it runs in.
type testKeeper struct {
	*Keeper
	clock   *clock.Manual
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

// testServerBound is leader.server_bound for the lock sessions of the test. It is PostgreSQL's own timer, which no
// clock of Muster moves, so the test keeps it short.
const testServerBound = 2 * time.Second

func newTestKeeper(t *testing.T, id, dbURL string) *testKeeper {
	t.Helper()
	k := &testKeeper{clock: clock.NewManual(time.Now()), log: &syncBuffer{}}
	connect := Dial(func(ctx context.Context) (*pgx.Conn, error) { return pgx.Connect(ctx, dbURL) }, testServerBound)
	tasks := func() []Task {
		return []Task{{Name: "lead", Every: time.Hour, Run: func(ctx context.Context) error {
			k.running.Add(1)
			<-ctx.Done()
			k.running.Add(-1)
			return nil
		}}}
	}
	k.Keeper = NewKeeper(connect, k.clock, logging.New(k.log, logging.LevelInfo), id, tasks)
	t.Cleanup(func() { k.release(context.WithoutCancel(t.Context())) })
	return k
}

// eventually polls cond until it holds and returns how long that took. The deadline only stops a test that hangs; it
// is no bound of the behaviour under test, so a slow, busy machine does not fail it.
func eventually(t *testing.T, what string, cond func() bool) time.Duration {
	t.Helper()
	const deadline = 30 * time.Second
	begin := time.Now()
	for !cond() {
		if time.Since(begin) > deadline {
			t.Fatalf("%s did not happen within %v", what, deadline)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return time.Since(begin)
}

// TestIntegrationLeaderLease: one of two replicas takes the lock and pings it through the network. When that network
// goes silent, the Leader's next ping waits no longer than what is left of leader.fencing_timeout since the last ping
// that succeeded, counted on the lease's clock, and the Leader stops its tasks. PostgreSQL then ends the silent lock
// session, which nothing but the session's server-side bound does here, as the blackhole keeps the connection open,
// and the other replica takes the lock. The test takes every step of both replicas itself and moves the lease's clock,
// so no assertion depends on how fast the machine is; TestLeaderFailover in the e2e suite measures the bounds of
// C-02.AC-3 on running replicas.
func TestIntegrationLeaderLease(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		_, u := migrated(t, s)
		parsed, err := url.Parse(u)
		if err != nil {
			t.Fatal(err)
		}
		hole := newBlackhole(t, parsed.Host)
		a := newTestKeeper(t, "a", hole.through(t, u))
		b := newTestKeeper(t, "b", u)
		ctx := t.Context()

		a.step(ctx)
		if !a.Leading() {
			t.Fatalf("a did not take the free lock; log of a: %s", a.log.String())
		}
		eventually(t, "a starting its tasks", func() bool { return a.running.Load() == 1 })
		if !strings.Contains(a.log.String(), `"event":"leadership_acquired","replica":"a"`) {
			t.Errorf("log of a: %s", a.log.String())
		}
		b.step(ctx)
		if b.Leading() {
			t.Fatal("b took the lock that a holds")
		}
		a.clock.Advance(PingInterval)
		a.step(ctx)
		if !a.Leading() {
			t.Fatalf("a stopped leading on a ping through the network; log of a: %s", a.log.String())
		}

		// The network goes silent shortly before the fencing deadline: the ping waits for the rest, then a fences.
		hole.Cut()
		const rest = 200 * time.Millisecond
		a.clock.Advance(FencingTimeout - rest)
		a.step(ctx)
		if a.Leading() || a.running.Load() != 0 {
			t.Fatalf("after the fencing deadline a leads %v with %d tasks running", a.Leading(), a.running.Load())
		}
		if !strings.Contains(a.log.String(),
			`"level":"WARN","event":"leadership_lost","replica":"a","error":"no ping succeeded for 15s: `) {
			t.Errorf("log of a: %s", a.log.String())
		}

		took := eventually(t, "b taking the lock", func() bool {
			b.step(ctx)
			return b.Leading()
		})
		t.Logf("b took the lock %v after a fenced (server-side bound %v)", took, testServerBound)
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
				Organizations: NewStore(d.Pool).ListOrganizationIDs,
				Business:      business,
				Log:           logger,
				PruneAuth: []PruneTable{
					{Name: "sign_in_throttles", Delete: auth.NewPruner(authdb.New(d.Pool)).SignInThrottles},
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
		if _, err := d.Pool.Exec(t.Context(), `INSERT INTO sign_in_throttles (org_id, subject_kind, subject,
			consecutive_failures, last_failure_at) SELECT id, 'address', '192.0.2.1', 3, $1 FROM organizations`,
			business.Now().Add(-48*time.Hour)); err != nil {
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
		// The overlapping runs of partition maintenance take turns on the maintenance lock. A run that waits for it
		// longer than lock_timeout, behind a run that is still creating the partitions on a busy machine, gives up and
		// is retried at the next run, as designed; any other failure is an error. One run created every partition.
		for line := range strings.Lines(log.String()) {
			if strings.Contains(line, `"event":"partition_maintenance_failed"`) && !strings.Contains(line,
				`"error":"take the partition maintenance lock: ERROR: canceling statement due to lock timeout (SQLSTATE 55P03)"`) {
				t.Errorf("partition maintenance failed: %s", line)
			}
		}
		if n := strings.Count(log.String(), `"event":"partitions_maintained"`); n != 1 {
			t.Errorf("partitions_maintained logged %d times: %s", n, log.String())
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
			"SELECT count(*) FROM sign_in_throttles":                                                    0,
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
		if n := strings.Count(log.String(), `"event":"short_lived_pruned"`); n != 1 {
			t.Errorf("short_lived_pruned logged %d times: %s", n, log.String())
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

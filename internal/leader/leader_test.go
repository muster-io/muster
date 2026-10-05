// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package leader

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

// lockServer is the advisory lock of a database: one session holds it at a time.
type lockServer struct {
	mu     sync.Mutex
	holder *fakeSession
}

type fakeSession struct {
	server *lockServer
	// pingErr fails the pings; pingBlock makes them wait until their context ends.
	pingErr   error
	pingBlock bool
	lockErr   error
	pings     int
	closed    bool
}

func (s *fakeSession) TryLock(context.Context) (bool, error) {
	if s.lockErr != nil {
		return false, s.lockErr
	}
	s.server.mu.Lock()
	defer s.server.mu.Unlock()
	if s.server.holder == nil || s.server.holder == s {
		s.server.holder = s
		return true, nil
	}
	return false, nil
}

func (s *fakeSession) Ping(ctx context.Context) error {
	s.pings++
	if s.pingBlock {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.pingErr
}

func (s *fakeSession) Close(context.Context) error {
	s.closed = true
	s.server.mu.Lock()
	defer s.server.mu.Unlock()
	if s.server.holder == s {
		s.server.holder = nil
	}
	return nil
}

// replica is a Keeper over the lock server with a manual real clock and tasks that count their runs.
type replica struct {
	*Keeper
	sessions []*fakeSession
	clock    *clock.Manual
	log      *bytes.Buffer
	runs     atomic.Int64
	stopped  atomic.Int64
	ticks    chan time.Time
	// connectErr fails the next connects; lockErr fails the lock attempts of the next sessions.
	connectErr, lockErr error
}

func newReplica(server *lockServer, id string) *replica {
	r := &replica{clock: clock.NewManual(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)), log: &bytes.Buffer{},
		ticks: make(chan time.Time)}
	connect := func(context.Context) (Session, error) {
		if r.connectErr != nil {
			return nil, r.connectErr
		}
		s := &fakeSession{server: server, lockErr: r.lockErr}
		r.sessions = append(r.sessions, s)
		return s, nil
	}
	tasks := func() []Task {
		return []Task{{Name: "count", Every: time.Hour, Run: func(ctx context.Context) error {
			r.runs.Add(1)
			<-ctx.Done()
			r.stopped.Add(1)
			return nil
		}}}
	}
	r.Keeper = NewKeeper(connect, r.clock, logging.New(r.log, logging.LevelInfo), id, tasks)
	r.every = func(time.Duration) (<-chan time.Time, func()) { return r.ticks, func() {} }
	return r
}

func (r *replica) session() *fakeSession { return r.sessions[len(r.sessions)-1] }

// waitRuns waits until the tasks have started n times in all.
func (r *replica) waitRuns(t *testing.T, n int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for r.runs.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("the tasks started %d times, want %d", r.runs.Load(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAcquisitionAndFencing(t *testing.T) {
	server := &lockServer{}
	a, b := newReplica(server, "a"), newReplica(server, "b")
	ctx := t.Context()

	a.step(ctx)
	if !a.Leading() {
		t.Fatal("a did not take the free lock")
	}
	a.waitRuns(t, 1)
	if !strings.Contains(a.log.String(), `"event":"leadership_acquired","replica":"a"`) {
		t.Errorf("log of a: %s", a.log.String())
	}
	b.step(ctx)
	if b.Leading() {
		t.Fatal("b took a lock that a holds")
	}

	// The pings succeed: a keeps leading, however long it leads.
	for range 5 {
		a.clock.Advance(PingInterval)
		a.step(ctx)
	}
	if !a.Leading() || a.session().pings != 5 {
		t.Fatalf("a leads %v after %d pings", a.Leading(), a.session().pings)
	}

	// The pings stop answering: at the fencing deadline a stops its tasks and closes the session.
	a.session().pingErr = nil
	a.clock.Advance(FencingTimeout - 10*time.Millisecond)
	a.session().pingBlock = true
	a.step(ctx) // the ping may take the 10 ms left until the deadline, then a fences
	if a.Leading() || a.stopped.Load() != 1 || !a.sessions[0].closed {
		t.Fatalf("a leads %v, tasks stopped %d, session closed %v", a.Leading(), a.stopped.Load(),
			a.sessions[0].closed)
	}
	if !strings.Contains(a.log.String(), `"level":"WARN","event":"leadership_lost","replica":"a","error":"no ping `+
		`succeeded for 15s: context deadline exceeded"`) {
		t.Errorf("log of a: %s", a.log.String())
	}

	// The lock is free again: b takes it, and a competes without taking it.
	b.step(ctx)
	a.step(ctx)
	if !b.Leading() || a.Leading() {
		t.Fatalf("after fencing: a leads %v, b leads %v", a.Leading(), b.Leading())
	}
	b.waitRuns(t, 1)
}

func TestFencingWithoutAPing(t *testing.T) {
	r := newReplica(&lockServer{}, "a")
	r.step(t.Context())
	r.waitRuns(t, 1)
	// The process was frozen past the deadline: it fences before it pings.
	r.clock.Advance(FencingTimeout)
	r.step(t.Context())
	if r.Leading() || r.session().pings != 0 {
		t.Fatalf("leads %v after %d pings", r.Leading(), r.session().pings)
	}
	if !strings.Contains(r.log.String(), `"error":"no ping succeeded for 15s"`) {
		t.Errorf("log: %s", r.log.String())
	}
}

func TestFencingOnAFailedPing(t *testing.T) {
	r := newReplica(&lockServer{}, "a")
	r.step(t.Context())
	r.waitRuns(t, 1)
	r.session().pingErr = errors.New("ping the Leader lock session: conn closed")
	r.clock.Advance(PingInterval)
	r.step(t.Context())
	if r.Leading() || r.stopped.Load() != 1 {
		t.Fatalf("leads %v, tasks stopped %d", r.Leading(), r.stopped.Load())
	}
	if !strings.Contains(r.log.String(), `"error":"ping the Leader lock session: conn closed"`) {
		t.Errorf("log: %s", r.log.String())
	}
	// The next leadership starts the tasks again.
	r.step(t.Context())
	if !r.Leading() || len(r.sessions) != 2 {
		t.Fatalf("leads %v with %d sessions", r.Leading(), len(r.sessions))
	}
	r.waitRuns(t, 2)
}

func TestCompetingFailures(t *testing.T) {
	r := newReplica(&lockServer{}, "a")
	r.connectErr = errors.New("connection refused")
	r.step(t.Context())
	if r.Leading() || len(r.sessions) != 0 {
		t.Fatal("led without a session")
	}
	r.connectErr, r.lockErr = nil, errors.New("conn closed")
	r.step(t.Context())
	if r.Leading() || !r.sessions[0].closed || r.Keeper.session != nil {
		t.Fatal("a session whose lock attempt failed was kept")
	}
	if r.log.Len() != 0 {
		t.Errorf("log: %s", r.log.String())
	}
}

func TestRunReleasesTheLockAtTheEnd(t *testing.T) {
	server := &lockServer{}
	r := newReplica(server, "a")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		r.Run(ctx, r.ticks)
		close(done)
	}()
	r.waitRuns(t, 1)
	r.ticks <- time.Time{} // a ping
	r.ticks <- time.Time{} // the ping has run
	cancel()
	<-done
	if r.Leading() || server.holder != nil || r.stopped.Load() != 1 || !r.sessions[0].closed {
		t.Fatalf("after the end: leads %v, holder %v, stopped %d", r.Leading(), server.holder, r.stopped.Load())
	}
	if strings.Contains(r.log.String(), "leadership_lost") {
		t.Errorf("a shutdown logged leadership_lost: %s", r.log.String())
	}
}

func TestTaskFailuresAreLoggedAndRetried(t *testing.T) {
	var log bytes.Buffer
	ticks := make(chan time.Time)
	var runs atomic.Int64
	k := NewKeeper(nil, clock.Real{}, logging.New(&log, logging.LevelInfo), "a", nil)
	k.every = func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		k.runTask(ctx, Task{Name: "flaky", Run: func(context.Context) error {
			runs.Add(1)
			return errors.New("the database is unavailable")
		}})
		close(done)
	}()
	ticks <- time.Time{}
	ticks <- time.Time{}
	cancel()
	<-done
	if runs.Load() != 3 {
		t.Errorf("runs %d, want 3", runs.Load())
	}
	if !strings.Contains(log.String(), `"level":"WARN","event":"leader_task_failed","task":"flaky",`+
		`"error":"the database is unavailable"`) {
		t.Errorf("log: %s", log.String())
	}
}

func TestLeaderGauge(t *testing.T) {
	r := newReplica(&lockServer{}, "a")
	Register(r.Keeper)
	scrape := func() string {
		rec := httptest.NewRecorder()
		metrics.Handler(nil).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "GET", "/metrics", nil))
		return rec.Body.String()
	}
	if !strings.Contains(scrape(), "\nmuster_leader 0\n") {
		t.Error("muster_leader is not 0 before the lock is taken")
	}
	r.step(t.Context())
	if !strings.Contains(scrape(), "\nmuster_leader 1\n") {
		t.Error("muster_leader is not 1 on the Leader")
	}
	r.release(t.Context())
}

func TestTasksAreTheClosedList(t *testing.T) {
	var calls []string
	record := func(name string) func(context.Context) error {
		return func(context.Context) error {
			calls = append(calls, name)
			return nil
		}
	}
	tasks := Tasks(Work{
		Alive:              nil,
		MaintainPartitions: func(context.Context) error { calls = append(calls, "partitions"); return errors.New("x") },
		PruneReplicas:      record("prune"),
	})()
	var names []string
	for _, task := range tasks {
		names = append(names, task.Name)
	}
	if want := []string{"partition_maintenance", "alive_mark", "replica_pruning"}; !slices.Equal(names, want) {
		t.Fatalf("Leader tasks %v, want %v", names, want)
	}
	if tasks[0].Every != time.Hour || tasks[1].Every != AliveMarkInterval || tasks[2].Every != time.Hour {
		t.Errorf("intervals %v %v %v", tasks[0].Every, tasks[1].Every, tasks[2].Every)
	}
	// Partition maintenance logs its own failures; the runner gets none to log twice.
	if err := tasks[0].Run(t.Context()); err != nil {
		t.Errorf("partition maintenance returned %v", err)
	}
	if err := tasks[2].Run(t.Context()); err != nil || !slices.Equal(calls, []string{"partitions", "prune"}) {
		t.Errorf("calls %v, err %v", calls, err)
	}
}

// fakeConn is the connection under a lock session.
type fakeConn struct {
	execs    []string
	execErr  error
	locked   bool
	queryErr error
	closed   bool
}

func (c *fakeConn) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	c.execs = append(c.execs, sql)
	return pgconn.CommandTag{}, c.execErr
}

func (c *fakeConn) QueryRow(context.Context, string, ...any) pgx.Row {
	return boolRow{v: c.locked, err: c.queryErr}
}

func (c *fakeConn) Close(context.Context) error {
	c.closed = true
	return nil
}

type boolRow struct {
	v   bool
	err error
}

func (r boolRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*dest[0].(*bool) = r.v
	return nil
}

func TestLockSession(t *testing.T) {
	c := &fakeConn{locked: true}
	connect := dial(func(context.Context) (sessionConn, error) { return c, nil }, ServerBound)
	s, err := connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if want := "SET idle_session_timeout = 30000; SET tcp_keepalives_idle = 5; SET tcp_keepalives_interval = 5; " +
		"SET tcp_keepalives_count = 3; SET tcp_user_timeout = 30000"; len(c.execs) != 1 || c.execs[0] != want {
		t.Errorf("session settings %v, want %s", c.execs, want)
	}
	if ok, err := s.TryLock(t.Context()); !ok || err != nil {
		t.Errorf("TryLock = %v, %v", ok, err)
	}
	if err := s.Ping(t.Context()); err != nil || c.execs[1] != "SELECT 1" {
		t.Errorf("Ping = %v, %v", err, c.execs)
	}
	c.queryErr, c.execErr = errors.New("conn closed"), errors.New("conn closed")
	if _, err := s.TryLock(t.Context()); err == nil || !strings.Contains(err.Error(), "try the Leader lock") {
		t.Errorf("TryLock = %v", err)
	}
	if err := s.Ping(t.Context()); err == nil || !strings.Contains(err.Error(), "ping the Leader lock session") {
		t.Errorf("Ping = %v", err)
	}
	if err := s.Close(t.Context()); err != nil || !c.closed {
		t.Errorf("Close = %v", err)
	}

	// A session that cannot be bounded is closed and refused.
	c = &fakeConn{execErr: errors.New("permission denied")}
	if _, err := dial(func(context.Context) (sessionConn, error) { return c, nil }, ServerBound)(t.Context()); err == nil ||
		!c.closed {
		t.Errorf("an unbounded session: %v, closed %v", err, c.closed)
	}
	if _, err := Dial(func(context.Context) (*pgx.Conn, error) { return nil, errors.New("refused") },
		ServerBound)(t.Context()); err == nil {
		t.Error("Dial without a connection succeeded")
	}
}

// TestReregisterAfterIsTheAbsenceNotice: a replica re-registers after losing the database for longer than
// leader.absence_notice, the gap that the downtime rule counts from.
func TestReregisterAfterIsTheAbsenceNotice(t *testing.T) {
	if keyring.ReregisterAfter != AbsenceNotice {
		t.Errorf("keyring.ReregisterAfter = %v, want leader.absence_notice %v", keyring.ReregisterAfter, AbsenceNotice)
	}
}

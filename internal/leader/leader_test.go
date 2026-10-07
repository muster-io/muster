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
		Organizations:      func(context.Context) ([]int64, error) { return []int64{1}, nil },
		Business:           clock.NewManual(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)),
		Log:                logging.New(&bytes.Buffer{}, logging.LevelInfo),
		PruneAuth: []PruneTable{{Name: "sessions", Delete: func(context.Context, int64, time.Time, int32) (int64,
			error,
		) {
			calls = append(calls, "short-lived")
			return 0, nil
		}}},
	})()
	var names []string
	for _, task := range tasks {
		names = append(names, task.Name)
	}
	want := []string{"partition_maintenance", "alive_mark", "replica_pruning", "short_lived_pruning",
		"ingest_backlog", "alert_retention", "heartbeat_check", "stale_scan", "alert_group_gauges"}
	if !slices.Equal(names, want) {
		t.Fatalf("Leader tasks %v, want %v", names, want)
	}
	if tasks[0].Every != time.Hour || tasks[1].Every != AliveMarkInterval || tasks[2].Every != time.Hour ||
		tasks[3].Every != MaintenanceInterval || tasks[4].Every != BacklogInterval ||
		tasks[5].Every != MaintenanceInterval || tasks[6].Every != 10*time.Second || tasks[7].Every != 30*time.Second {
		t.Errorf("intervals %v %v %v %v", tasks[0].Every, tasks[1].Every, tasks[2].Every, tasks[3].Every)
	}
	if tasks[6].Wake != nil || tasks[7].Wake != nil {
		t.Error("tasks are woken outside development mode")
	}
	// Partition maintenance logs its own failures; the runner gets none to log twice.
	if err := tasks[0].Run(t.Context()); err != nil {
		t.Errorf("partition maintenance returned %v", err)
	}
	if err := tasks[2].Run(t.Context()); err != nil || !slices.Equal(calls, []string{"partitions", "prune"}) {
		t.Errorf("calls %v, err %v", calls, err)
	}
	if err := tasks[3].Run(t.Context()); err != nil ||
		!slices.Equal(calls, []string{"partitions", "prune", "short-lived"}) {
		t.Errorf("calls %v, err %v", calls, err)
	}
	// Without a backlog counter the task does nothing.
	if err := tasks[4].Run(t.Context()); err != nil {
		t.Errorf("backlog without a counter: %v", err)
	}
	// Without a retention the task does nothing.
	if err := tasks[5].Run(t.Context()); err != nil {
		t.Errorf("alert retention without a retention: %v", err)
	}
	// Without a counter of Alert Groups the task does nothing.
	if tasks[8].Every != AlertGroupGaugeInterval || tasks[8].Run(t.Context()) != nil {
		t.Errorf("alert group gauges without a counter")
	}
}

// TestAlertGroupGauges: the Leader counts the open Alert Groups of every Organization and goes on past one that
// fails.
func TestAlertGroupGauges(t *testing.T) {
	var counted []int64
	failed := errors.New("locked")
	work := Work{
		Organizations: func(context.Context) ([]int64, error) { return []int64{1, 2}, nil },
		AlertGroupGauges: func(_ context.Context, org int64) error {
			counted = append(counted, org)
			if org == 1 {
				return failed
			}
			return nil
		},
	}
	gauges := Tasks(work)()[8]
	if err := gauges.Run(t.Context()); !errors.Is(err, failed) || !slices.Equal(counted, []int64{1, 2}) {
		t.Errorf("counted %v, %v", counted, err)
	}
	orgsErr := errors.New("down")
	work.Organizations = func(context.Context) ([]int64, error) { return nil, orgsErr }
	if err := Tasks(work)()[8].Run(t.Context()); !errors.Is(err, orgsErr) {
		t.Errorf("gauges = %v", err)
	}
}

// TestHeartbeatAndStaleTasks covers C-07.FR-8 and C-02.FR-10: the Heartbeat check runs over every Organization only
// once this leadership's takeover ran, so that it measures from the end of a downtime the takeover records; the Stale
// scan runs per Organization and goes on past one that fails; in development mode a move of the development clock
// wakes both.
func TestHeartbeatAndStaleTasks(t *testing.T) {
	w := newWorld(0)
	var checked [][]int64
	var scanned []int64
	failed := errors.New("locked")
	moved := &Wakes{}
	work := Work{
		Alive:         w.alive("a"),
		Organizations: func(context.Context) ([]int64, error) { return []int64{1, 2}, nil },
		HeartbeatCheck: func(_ context.Context, orgs []int64) error {
			checked = append(checked, orgs)
			return nil
		},
		StaleScan: func(_ context.Context, org int64) error {
			scanned = append(scanned, org)
			if org == 1 {
				return failed
			}
			return nil
		},
		ClockMoved: moved,
	}
	tasks := Tasks(work)()
	check, scan := tasks[6], tasks[7]
	if err := check.Run(t.Context()); err != nil || len(checked) != 0 {
		t.Fatalf("checked before the takeover: %v, %v", checked, err)
	}
	if err := tasks[1].Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := check.Run(t.Context()); err != nil || len(checked) != 1 || !slices.Equal(checked[0], []int64{1, 2}) {
		t.Errorf("checked %v, %v", checked, err)
	}
	if err := scan.Run(t.Context()); !errors.Is(err, failed) || !slices.Equal(scanned, []int64{1, 2}) {
		t.Errorf("scanned %v, %v", scanned, err)
	}
	moved.Wake()
	moved.Wake()
	for _, task := range []Task{check, scan} {
		select {
		case <-task.Wake:
		default:
			t.Errorf("%s was not woken", task.Name)
		}
		select {
		case <-task.Wake:
			t.Errorf("%s was woken twice", task.Name)
		default:
		}
	}
	// A new leadership takes over again before it checks.
	if err := Tasks(work)()[6].Run(t.Context()); err != nil || len(checked) != 1 {
		t.Errorf("a new leadership checked before its takeover: %v", checked)
	}
	orgsErr := errors.New("down")
	work.Organizations = func(context.Context) ([]int64, error) { return nil, orgsErr }
	tasks = Tasks(work)()
	if err := tasks[7].Run(t.Context()); !errors.Is(err, orgsErr) {
		t.Errorf("scan = %v", err)
	}
	if err := tasks[1].Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := tasks[6].Run(t.Context()); !errors.Is(err, orgsErr) {
		t.Errorf("check = %v", err)
	}
	// Without a check or a scan the tasks do nothing.
	empty := Tasks(Work{Alive: w.alive("b")})()
	if err := empty[7].Run(t.Context()); err != nil {
		t.Errorf("scan without a scan: %v", err)
	}
	if err := empty[1].Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := empty[6].Run(t.Context()); err != nil {
		t.Errorf("check without a check: %v", err)
	}
	var none *Wakes
	none.Wake()
}

// TestWokenTask: a task runs again at once when it is woken, between its ticks.
func TestWokenTask(t *testing.T) {
	var log bytes.Buffer
	ticks := make(chan time.Time)
	wake := make(chan struct{})
	runs := make(chan struct{}, 4)
	k := NewKeeper(nil, clock.Real{}, logging.New(&log, logging.LevelInfo), "a", nil)
	k.every = func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		k.runTask(ctx, Task{Name: "woken", Wake: wake, Run: func(context.Context) error {
			runs <- struct{}{}
			return nil
		}})
		close(done)
	}()
	<-runs
	wake <- struct{}{}
	<-runs
	cancel()
	<-done
}

// TestAlertRetentionTask: the retention of the Alerts view runs in every Organization at the same now on the business
// clock, goes on past an Organization that fails and reports the failure.
func TestAlertRetentionTask(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	type call struct {
		org int64
		now time.Time
	}
	var calls []call
	failed := errors.New("locked")
	w := Work{
		Organizations: func(context.Context) ([]int64, error) { return []int64{1, 2}, nil },
		Business:      clock.NewManual(now),
		AlertRetention: func(_ context.Context, org int64, at time.Time) (int64, error) {
			calls = append(calls, call{org, at})
			if org == 1 {
				return 0, failed
			}
			return 3, nil
		},
	}
	task := Tasks(w)()[5]
	if err := task.Run(t.Context()); !errors.Is(err, failed) ||
		!slices.Equal(calls, []call{{1, now}, {2, now}}) {
		t.Errorf("calls %v, err %v", calls, err)
	}
	orgsErr := errors.New("down")
	w.Organizations = func(context.Context) ([]int64, error) { return nil, orgsErr }
	if err := Tasks(w)()[5].Run(t.Context()); !errors.Is(err, orgsErr) {
		t.Errorf("= %v", err)
	}
}

// TestIngestBacklogTask: the backlog task counts the pending Stored Snapshots of every Organization.
func TestIngestBacklogTask(t *testing.T) {
	var counted [][]int64
	orgsErr := errors.New("down")
	w := Work{
		Organizations: func(context.Context) ([]int64, error) { return []int64{1, 2}, nil },
		IngestBacklog: func(_ context.Context, orgs []int64) error {
			counted = append(counted, orgs)
			return nil
		},
	}
	backlog := Tasks(w)()[4]
	if err := backlog.Run(t.Context()); err != nil || len(counted) != 1 || !slices.Equal(counted[0], []int64{1, 2}) {
		t.Errorf("counted %v, %v", counted, err)
	}
	w.Organizations = func(context.Context) ([]int64, error) { return nil, orgsErr }
	if err := Tasks(w)()[4].Run(t.Context()); !errors.Is(err, orgsErr) {
		t.Errorf("= %v", err)
	}
}

// pruneCall is one call of a fake short-lived delete.
type pruneCall struct {
	table string
	org   int64
	now   time.Time
	limit int32
}

// fakeTable deletes from a backlog of rows per Organization, at most limit at a time, and fails once failAfter
// calls are made when failAfter is set.
func fakeTable(name string, backlog map[int64]int64, calls *[]pruneCall, failAfter int) PruneTable {
	return PruneTable{Name: name, Delete: func(_ context.Context, org int64, now time.Time, limit int32) (int64,
		error,
	) {
		*calls = append(*calls, pruneCall{name, org, now, limit})
		if failAfter > 0 && len(*calls) >= failAfter {
			return 0, errors.New("connection reset")
		}
		n := min(backlog[org], int64(limit))
		backlog[org] -= n
		return n, nil
	}}
}

func TestShortLivedPruning(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	var log bytes.Buffer
	var calls []pruneCall
	backlog := map[int64]int64{1: 2*PruneBatch + 3, 2: 5}
	idle := map[int64]int64{}
	before := metrics.ShortLivedRowsPruned.With("sessions").Get()
	idleBefore := metrics.ShortLivedRowsPruned.With("sign_in_throttles").Get()
	w := Work{
		Organizations: func(context.Context) ([]int64, error) { return []int64{1, 2}, nil },
		Business:      clock.NewManual(now),
		Log:           logging.New(&log, logging.LevelInfo),
		PruneAuth: []PruneTable{
			fakeTable("sessions", backlog, &calls, 0),
			fakeTable("sign_in_throttles", idle, &calls, 0),
		},
	}
	if err := w.pruneShortLived(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Organization 1 takes three batches, the last one short; Organization 2 one; the empty table one per
	// Organization. Every call has the same now and the batch size.
	want := []pruneCall{
		{"sessions", 1, now, PruneBatch}, {"sessions", 1, now, PruneBatch}, {"sessions", 1, now, PruneBatch},
		{"sessions", 2, now, PruneBatch}, {"sign_in_throttles", 1, now, PruneBatch},
		{"sign_in_throttles", 2, now, PruneBatch},
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("calls\n %v\nwant %v", calls, want)
	}
	if backlog[1] != 0 || backlog[2] != 0 {
		t.Errorf("backlog left %v", backlog)
	}
	if got := metrics.ShortLivedRowsPruned.With("sessions").Get() - before; got != 2*PruneBatch+8 {
		t.Errorf("sessions counted %d", got)
	}
	if got := metrics.ShortLivedRowsPruned.With("sign_in_throttles").Get() - idleBefore; got != 0 {
		t.Errorf("sign_in_throttles counted %d", got)
	}
	if !strings.Contains(log.String(), `"event":"short_lived_pruned","table":"sessions","rows":2008`) ||
		strings.Count(log.String(), "short_lived_pruned") != 1 {
		t.Errorf("log: %s", log.String())
	}

	// A second run finds nothing: it deletes, logs and counts nothing more.
	log.Reset()
	if err := w.pruneShortLived(t.Context()); err != nil || log.Len() != 0 {
		t.Errorf("second run: err %v, log %s", err, log.String())
	}
	if got := metrics.ShortLivedRowsPruned.With("sessions").Get() - before; got != 2*PruneBatch+8 {
		t.Errorf("sessions counted %d after the second run", got)
	}
}

func TestShortLivedPruningFailures(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	var log bytes.Buffer
	var calls []pruneCall
	w := Work{
		Organizations: func(context.Context) ([]int64, error) { return []int64{1, 2}, nil },
		Business:      clock.NewManual(now),
		Log:           logging.New(&log, logging.LevelInfo),
	}
	// A failed table stops at its failure, keeps what it deleted before it, and the next table still runs.
	w.PruneAuth = []PruneTable{
		fakeTable("sessions", map[int64]int64{1: PruneBatch + 1, 2: 1}, &calls, 2),
		fakeTable("sign_in_throttles", map[int64]int64{1: 4, 2: 0}, &calls, 0),
	}
	err := w.pruneShortLived(t.Context())
	if err == nil || err.Error() != "prune sessions: connection reset" {
		t.Fatalf("err %v", err)
	}
	if len(calls) != 4 || calls[2].table != "sign_in_throttles" {
		t.Errorf("calls %v", calls)
	}
	for _, want := range []string{`"table":"sessions","rows":1000`, `"table":"sign_in_throttles","rows":4`} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log misses %s: %s", want, log.String())
		}
	}

	// Without the Organizations nothing is deleted.
	calls = nil
	w.Organizations = func(context.Context) ([]int64, error) { return nil, errors.New("database unavailable") }
	if err := w.pruneShortLived(t.Context()); err == nil ||
		err.Error() != "list the organizations to prune: database unavailable" || len(calls) != 0 {
		t.Errorf("err %v, calls %v", err, calls)
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

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package leader elects the Leader (C-02.FR-10, ADR-0007): one replica holds a session advisory lock with a constant
// key on a dedicated connection and runs the closed list of Leader tasks in tasks.go. The lock has a bounded lease:
// the Leader pings over the lock's own session and stops every Leader task as soon as a ping fails or none has
// succeeded for FencingTimeout, while PostgreSQL ends a silent lock session, and so releases the lock, within
// ServerBound. The lease runs on the real clock, so the development clock never fences a Leader.
package leader

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

const (
	// PingInterval is leader.ping_interval: how often the Leader pings over its lock session and another replica
	// tries to take the lock.
	PingInterval = 5 * time.Second
	// FencingTimeout is leader.fencing_timeout: the Leader stops every Leader task when no ping succeeded for this
	// long. It is shorter than ServerBound, so the old Leader normally stops before another one can take the lock.
	FencingTimeout = 15 * time.Second
	// ServerBound is leader.server_bound: PostgreSQL ends a silent lock session, and releases the lock, within it.
	ServerBound = 30 * time.Second

	// LockKey is the session advisory lock of the Leader. It follows the keys of internal/db.
	LockKey int64 = 0x6d75_7374_6572_0003

	closeTimeout = time.Second
)

// Session is the dedicated session connection that holds the Leader lock.
type Session interface {
	// TryLock tries to take the Leader lock without waiting.
	TryLock(ctx context.Context) (bool, error)
	// Ping proves that the session, and with it the lock, is still there.
	Ping(ctx context.Context) error
	// Close ends the session, which releases the lock.
	Close(ctx context.Context) error
}

// Dial returns the connector of lock sessions over connect, which opens a session connection to PostgreSQL. Each
// session sets idle_session_timeout and TCP keepalive parameters, so that PostgreSQL ends it once it has been silent
// for serverBound and releases the lock.
func Dial(connect func(context.Context) (*pgx.Conn, error), serverBound time.Duration) func(context.Context) (Session,
	error,
) {
	return dial(func(ctx context.Context) (sessionConn, error) { return connect(ctx) }, serverBound)
}

func dial(connect func(context.Context) (sessionConn, error), serverBound time.Duration) func(context.Context) (Session,
	error,
) {
	return func(ctx context.Context) (Session, error) {
		c, err := connect(ctx)
		if err != nil {
			return nil, err
		}
		s := &pgSession{conn: c}
		if err := s.bound(ctx, serverBound); err != nil {
			_ = s.Close(ctx)
			return nil, err
		}
		return s, nil
	}
}

// BoundSession bounds a session connection like a lock session: PostgreSQL ends it once it has been silent for d.
// Partition maintenance bounds its session with it, so that the maintenance lock of a frozen process goes too.
func BoundSession(ctx context.Context, c *pgx.Conn, d time.Duration) error {
	return (&pgSession{conn: c}).bound(ctx, d)
}

// sessionConn is what a lock session needs of *pgx.Conn.
type sessionConn interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Close(ctx context.Context) error
}

type pgSession struct {
	conn sessionConn
}

// bound makes PostgreSQL end the session once it has been silent for d: idle_session_timeout for a session that
// waits for its client, TCP keepalives and tcp_user_timeout for a peer that is gone.
func (s *pgSession) bound(ctx context.Context, d time.Duration) error {
	keepalive := max(d/6, time.Second)
	sql := fmt.Sprintf("SET idle_session_timeout = %d; SET tcp_keepalives_idle = %d; "+
		"SET tcp_keepalives_interval = %d; SET tcp_keepalives_count = 3; SET tcp_user_timeout = %d",
		d.Milliseconds(), int(keepalive.Seconds()), int(keepalive.Seconds()), d.Milliseconds())
	if _, err := s.conn.Exec(ctx, sql); err != nil {
		return fmt.Errorf("bound the session connection: %w", err)
	}
	return nil
}

func (s *pgSession) TryLock(ctx context.Context) (bool, error) {
	var ok bool
	if err := s.conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", LockKey).Scan(&ok); err != nil {
		return false, fmt.Errorf("try the Leader lock: %w", err)
	}
	return ok, nil
}

func (s *pgSession) Ping(ctx context.Context) error {
	if _, err := s.conn.Exec(ctx, "SELECT 1"); err != nil {
		return fmt.Errorf("ping the Leader lock session: %w", err)
	}
	return nil
}

func (s *pgSession) Close(ctx context.Context) error {
	return s.conn.Close(ctx)
}

// Keeper competes for the Leader lock and, while it holds it, runs the Leader tasks.
type Keeper struct {
	connect func(context.Context) (Session, error)
	real    clock.Clock
	log     *logging.Logger
	replica string
	tasks   func() []Task

	pingInterval, fencingTimeout time.Duration
	// every makes the ticks of a task; tests replace it.
	every func(time.Duration) (<-chan time.Time, func())

	leading atomic.Bool

	// The state of Run.
	session  Session
	lastPing time.Time
	stop     context.CancelFunc
	running  sync.WaitGroup
}

// NewKeeper returns the Keeper of the replica id. connect opens lock sessions (Dial); the lease runs on the real
// clock; tasks returns the Leader tasks at every leadership (Tasks).
func NewKeeper(connect func(context.Context) (Session, error), realClock clock.Clock, log *logging.Logger,
	replica string, tasks func() []Task,
) *Keeper {
	return &Keeper{connect: connect, real: realClock, log: log, replica: replica, tasks: tasks,
		pingInterval: PingInterval, fencingTimeout: FencingTimeout, every: ticker}
}

func ticker(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

var (
	gaugeOnce sync.Once
	// current is the Keeper that muster_leader reports: the one most recently given to Register.
	current atomic.Pointer[Keeper]
)

// Register makes muster_leader report k.
func Register(k *Keeper) {
	current.Store(k)
	gaugeOnce.Do(func() {
		metrics.Leader.Func(func() float64 {
			if k := current.Load(); k != nil && k.Leading() {
				return 1
			}
			return 0
		})
	})
}

// Leading reports whether this replica holds the Leader lock and runs the Leader tasks.
func (k *Keeper) Leading() bool { return k.leading.Load() }

// Run competes for the lock and keeps it until ctx ends: once at the start and at every tick it tries to take the
// lock or, while leading, pings. At the end it stops the Leader tasks and closes the lock session.
func (k *Keeper) Run(ctx context.Context, ticks <-chan time.Time) {
	defer k.release(ctx)
	for {
		k.step(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
	}
}

func (k *Keeper) step(ctx context.Context) {
	if k.Leading() {
		k.ping(ctx)
		return
	}
	k.tryLock(ctx)
}

func (k *Keeper) tryLock(ctx context.Context) {
	attempt, cancel := context.WithTimeout(ctx, k.pingInterval)
	defer cancel()
	if k.session == nil {
		s, err := k.connect(attempt)
		if err != nil {
			// The database is unavailable: the readiness check reports it, and the next tick tries again.
			return
		}
		k.session = s
	}
	ok, err := k.session.TryLock(attempt)
	if err != nil {
		k.closeSession(ctx)
		return
	}
	if !ok {
		return
	}
	k.lastPing = k.real.Now()
	k.start(ctx)
	k.log.Log(ctx, logging.LeadershipAcquired, logging.F("replica", k.replica))
}

// ping confirms the lock. A ping may take until the fencing deadline; one that fails or reaches the deadline fences.
func (k *Keeper) ping(ctx context.Context) {
	remaining := k.fencingTimeout - k.real.Now().Sub(k.lastPing)
	if remaining <= 0 {
		k.fence(ctx, fmt.Errorf("no ping succeeded for %v", k.fencingTimeout))
		return
	}
	attempt, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	err := k.session.Ping(attempt)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || attempt.Err() != nil {
			err = fmt.Errorf("no ping succeeded for %v: %w", k.fencingTimeout, err)
		}
		k.fence(ctx, err)
		return
	}
	k.lastPing = k.real.Now()
}

// fence stops every Leader task at once, closes the lock session and logs leadership_lost; the next tick competes
// for the lock again.
func (k *Keeper) fence(ctx context.Context, cause error) {
	k.stopTasks()
	k.closeSession(ctx)
	k.log.Log(ctx, logging.LeadershipLost, logging.F("replica", k.replica), logging.F("error", cause.Error()))
}

// start marks the replica as leading and starts every Leader task.
func (k *Keeper) start(ctx context.Context) {
	taskCtx, stop := context.WithCancel(ctx)
	k.stop = stop
	k.leading.Store(true)
	for _, t := range k.tasks() {
		k.running.Go(func() { k.runTask(taskCtx, t) })
	}
}

// stopTasks cancels the Leader tasks and waits until each has returned.
func (k *Keeper) stopTasks() {
	if k.stop == nil {
		return
	}
	k.leading.Store(false)
	k.stop()
	k.running.Wait()
	k.stop = nil
}

func (k *Keeper) closeSession(ctx context.Context) {
	if k.session == nil {
		return
	}
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
	defer cancel()
	_ = k.session.Close(closeCtx)
	k.session = nil
}

// release ends leadership at shutdown: the tasks stop, and closing the session releases the lock at once.
func (k *Keeper) release(ctx context.Context) {
	k.stopTasks()
	k.closeSession(ctx)
}

// runTask runs t at once, then at every interval and whenever it is woken, until ctx ends.
func (k *Keeper) runTask(ctx context.Context, t Task) {
	ticks, stop := k.every(t.Every)
	defer stop()
	for {
		if err := t.Run(ctx); err != nil && ctx.Err() == nil {
			k.log.Log(ctx, logging.LeaderTaskFailed, logging.F("task", t.Name), logging.F("error", err.Error()))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		case <-t.Wake:
		}
	}
}

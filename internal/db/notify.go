// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/logging"
)

// HintChannel is the LISTEN/NOTIFY channel of the live-update hints (ADR-0009): writers send a Hint in the
// transaction of their change, and every replica's Listener receives it once the transaction commits.
const HintChannel = "muster_hints"

// The waits of a Listener: how long it waits for a notification before it pings its connection, how long a ping or a
// LISTEN may take, and the delays between reconnections, doubling up to the last.
const (
	listenPingInterval = 30 * time.Second
	listenPingTimeout  = 5 * time.Second
	listenRetryFirst   = time.Second
	listenRetryMax     = 30 * time.Second
)

// Hint is the payload of a notification on HintChannel: something of the Organization changed. Type is the type of a
// live-update hint (HintEvent in the API specification) and ID the public_id it names, or empty for a whole
// collection.
type Hint struct {
	OrgID int64  `json:"org_id"`
	Type  string `json:"type"`
	ID    string `json:"id,omitempty"`
}

// Execer runs a statement: a pool, a connection or the transaction of the change a hint announces.
type Execer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// NotifyHint sends h on HintChannel through e. Sent inside a transaction, it reaches the listeners only when the
// transaction commits, and not at all when it rolls back.
func NotifyHint(ctx context.Context, e Execer, h Hint) error {
	payload, err := json.Marshal(h)
	if err != nil {
		return fmt.Errorf("encode the %s hint: %w", h.Type, err)
	}
	if _, err := e.Exec(ctx, "SELECT pg_notify($1, $2)", HintChannel, string(payload)); err != nil {
		return fmt.Errorf("notify the %s hint: %w", h.Type, err)
	}
	return nil
}

// ListenConn is the session connection a Listener holds; *pgx.Conn implements it.
type ListenConn interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	WaitForNotification(ctx context.Context) (*pgconn.Notification, error)
	Close(ctx context.Context) error
}

// Listener keeps a LISTEN on HintChannel over a session connection of its own — a pooler in transaction mode would
// lose it — and passes every hint it receives on. It pings the connection when no notification came for a while and
// connects again after a loss, with a growing delay.
type Listener struct {
	connect func(context.Context) (ListenConn, error)
	log     *logging.Logger
	// wait is how long the Listener sleeps between reconnections; tests replace it.
	wait func(ctx context.Context, d time.Duration) bool
}

// NewListener returns the Listener over connect, which opens a session connection.
func NewListener(connect func(context.Context) (ListenConn, error), log *logging.Logger) *Listener {
	return &Listener{connect: connect, log: log, wait: sleep}
}

// SessionListenConn opens a session connection for a Listener.
func (d *DB) SessionListenConn(ctx context.Context) (ListenConn, error) {
	c, err := d.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Run listens until ctx ends. Each hint goes to hint, and listening calls listening each time the LISTEN is in place;
// after the first time, notifications sent while the connection was lost are gone, so the caller tells its clients to
// read everything again. A payload that is not a Hint is skipped. A loss is logged as live_updates_listen_failed once
// until the LISTEN is back, which is logged as live_updates_listen_restored.
func (l *Listener) Run(ctx context.Context, hint func(Hint), listening func(restored bool)) {
	delay := listenRetryFirst
	failed, connected := false, false
	for ctx.Err() == nil {
		c, err := l.listen(ctx)
		if err == nil {
			if failed {
				l.log.Log(ctx, logging.LiveUpdatesListenRestored)
			}
			listening(connected)
			connected, failed, delay = true, false, listenRetryFirst
			err = l.receive(ctx, c, hint)
			closeListen(ctx, c)
		}
		if ctx.Err() != nil {
			return
		}
		if !failed {
			l.log.Log(ctx, logging.LiveUpdatesListenFailed, logging.F("error", err.Error()))
			failed = true
		}
		if !l.wait(ctx, delay) {
			return
		}
		delay = min(delay*2, listenRetryMax)
	}
}

// listen opens a session connection and starts listening on it.
func (l *Listener) listen(ctx context.Context) (ListenConn, error) {
	c, err := l.connect(ctx)
	if err != nil {
		return nil, err
	}
	lctx, cancel := context.WithTimeout(ctx, listenPingTimeout)
	defer cancel()
	if _, err := c.Exec(lctx, "LISTEN "+pgx.Identifier{HintChannel}.Sanitize()); err != nil {
		closeListen(ctx, c)
		return nil, fmt.Errorf("listen on %s: %w", HintChannel, err)
	}
	return c, nil
}

// receive passes on the hints that arrive on c until ctx ends or the connection fails; a wait without a notification
// ends with a ping that proves the connection is still there.
func (l *Listener) receive(ctx context.Context, c ListenConn, hint func(Hint)) error {
	for {
		wctx, cancel := context.WithTimeout(ctx, listenPingInterval)
		n, err := c.WaitForNotification(wctx)
		cancel()
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case err == nil:
			var h Hint
			if json.Unmarshal([]byte(n.Payload), &h) == nil && h.Type != "" {
				hint(h)
			}
		case errors.Is(err, context.DeadlineExceeded) || pgconn.Timeout(err):
			pctx, cancel := context.WithTimeout(ctx, listenPingTimeout)
			_, err := c.Exec(pctx, "SELECT 1")
			cancel()
			if err != nil {
				return fmt.Errorf("ping the listening connection: %w", err)
			}
		default:
			return fmt.Errorf("wait for notifications: %w", err)
		}
	}
}

func closeListen(ctx context.Context, c ListenConn) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), listenPingTimeout)
	defer cancel()
	_ = c.Close(ctx)
}

// sleep waits for d unless ctx ends first; it reports whether the whole wait passed.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

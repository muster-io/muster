// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"context"
	"fmt"
	"time"

	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery/dbgen"
)

// NewTestStore is the Store over the queries q, which the tests keep in memory, beginning its transactions with b.
func NewTestStore(b db.Beginner, q any) *Store {
	return &Store{begin: b, queries: func(dbgen.DBTX) queries { return q.(queries) }}
}

// WaitReal is the wait of a worker without its own.
func WaitReal(ctx context.Context, d time.Duration, wake <-chan struct{}) { waitReal(ctx, d, wake) }

// SleepReal is the wait of the interactive path without its own.
func SleepReal(ctx context.Context, d time.Duration) bool { return sleepReal(ctx, d) }

// WakeChannel is the channel that Wake delivers to.
func (w *Worker) WakeChannel() <-chan struct{} {
	w.init()
	return w.wake
}

// Defaults are the wait and the batch a Worker uses.
func (w *Worker) Defaults() (time.Duration, int32) { return w.maxWait(), w.batch() }

// The events a round hook sees (WithRoundHook).
const (
	RoundWaits    = roundWaits
	RoundPrepared = roundPrepared
	RoundRuns     = roundRuns
)

// WithRoundHook is ctx with a hook that sees, on the round's goroutine, each claimed row that waits for the attempt of
// its key in flight before its preparation, and each row that was prepared and whose attempt started; and, on the
// goroutine of each such attempt, the start of its rest, before its call waits for its turn. It is called from several
// goroutines at once.
func WithRoundHook(ctx context.Context, f func(event string, id int64)) context.Context {
	return withHook(ctx, f)
}

// AttemptKeys claims the due deliveries that are neither Urgent nor configuration edits, and the due Thread replies, as
// a round claims its ranks 3 and 4, and returns the key that each one's attempt waits on before its preparation, by id.
func (w *Worker) AttemptKeys(ctx context.Context, org int64) (deliveries, replies map[int64]string, err error) {
	ctx = context.WithValue(ctx, refusedKey{}, newRefused())
	keys := func(claim claimFunc) (map[int64]string, error) {
		rows, err := claim(ctx, org)
		out := map[int64]string{}
		for _, r := range rows {
			out[r.id] = fmt.Sprintf("%+v", r.key)
		}
		return out, err
	}
	if deliveries, err = keys(w.claimDeliveries(false, false, time.Time{}, time.Time{})); err != nil {
		return nil, nil, err
	}
	replies, err = keys(w.claimReplies(time.Time{}))
	return deliveries, replies, err
}

// Backoff is the wait after the n-th Transient attempt.
func (w *Worker) Backoff(n int64) time.Duration { return w.backoff(n) }

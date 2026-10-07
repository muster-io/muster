// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"context"
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

// Backoff is the wait after the n-th Transient attempt.
func (w *Worker) Backoff(n int64) time.Duration { return w.backoff(n) }

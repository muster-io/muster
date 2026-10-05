// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package runtime

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

const (
	// ClockSkewWarning is process.clock_skew_warning: a larger skew against the database clock is logged as
	// clock_skew.
	ClockSkewWarning = 2 * time.Second
	// SkewInterval is how often each replica measures its clock skew.
	SkewInterval = time.Minute

	skewTimeout = 10 * time.Second
)

// rowQuerier is what the skew measurement needs of the main pool or a connection.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// MeasureSkew compares the real clock with the database's clock_timestamp(), read halfway through the round trip:
// the result is positive when the replica's clock is ahead. It takes two samples and keeps the one with the shorter
// round trip, so that opening a pool connection for the first does not skew the result.
func MeasureSkew(ctx context.Context, q rowQuerier, realClock clock.Clock) (time.Duration, error) {
	var skew, rtt time.Duration
	for i := range 2 {
		sent := realClock.Now()
		var db time.Time
		if err := q.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&db); err != nil {
			return 0, fmt.Errorf("read the database clock: %w", err)
		}
		received := realClock.Now()
		if sample := received.Sub(sent); i == 0 || sample < rtt {
			rtt, skew = sample, sent.Add(sample/2).Sub(db)
		}
	}
	return skew, nil
}

// skewCheck measures the clock skew of this replica every SkewInterval, exports it as muster_clock_skew_seconds and
// logs clock_skew when it exceeds ClockSkewWarning.
type skewCheck struct {
	q    rowQuerier
	real clock.Clock
	log  *logging.Logger
}

func (s skewCheck) check(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, skewTimeout)
	defer cancel()
	skew, err := MeasureSkew(ctx, s.q, s.real)
	if err != nil {
		// The database is unavailable: readiness reports it, and the gauge keeps the last measurement.
		return
	}
	metrics.ClockSkew.With().Set(skew.Seconds())
	if skew.Abs() > ClockSkewWarning {
		s.log.Log(ctx, logging.ClockSkew, logging.F("skew_seconds", skew.Seconds()),
			logging.F("warning_seconds", ClockSkewWarning.Seconds()))
	}
}

// run checks at once and then at every tick until ctx ends.
func (s skewCheck) run(ctx context.Context, ticks <-chan time.Time) {
	for {
		s.check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
	}
}

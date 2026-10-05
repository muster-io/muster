// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package runtime

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

// dbClock is a database whose clock reads at, or, with rc set, runs offset from the replica's clock rc; its round
// trips move rc by rtt, and the first one takes slow longer, as opening a connection would.
type dbClock struct {
	at     time.Time
	offset time.Duration
	rtt    time.Duration
	slow   time.Duration
	rc     *clock.Manual
	err    error
	n      *int
}

func (d dbClock) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	if sql != "SELECT clock_timestamp()" {
		panic(sql)
	}
	if d.n != nil {
		if *d.n == 0 {
			d.rc.Advance(d.slow)
		}
		*d.n++
		d.rc.Advance(d.rtt / 2)
		// The database reads its clock halfway through the round trip that the network sees.
		d.at = d.rc.Now().Add(d.offset)
		d.rc.Advance(d.rtt / 2)
	}
	return d
}

func (d dbClock) Scan(dest ...any) error {
	if d.err != nil {
		return d.err
	}
	*dest[0].(*time.Time) = d.at
	return nil
}

func TestMeasureSkew(t *testing.T) {
	sent := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	rc := clock.NewManual(sent)
	// The database clock runs 2.5 s behind the replica's; the first round trip spends 300 ms opening a connection
	// before the query's 200 ms.
	var n int
	skew, err := MeasureSkew(t.Context(), dbClock{offset: -2500 * time.Millisecond, rtt: 200 * time.Millisecond,
		slow: 300 * time.Millisecond, rc: rc, n: &n}, rc)
	if err != nil || skew != 2500*time.Millisecond || n != 2 {
		t.Errorf("MeasureSkew = %v, %v after %d samples; want 2.5s after 2", skew, err, n)
	}
	if _, err := MeasureSkew(t.Context(), dbClock{rc: rc, err: errors.New("conn closed")}, rc); err == nil ||
		!strings.Contains(err.Error(), "read the database clock: conn closed") {
		t.Errorf("MeasureSkew = %v", err)
	}
}

func scrape(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	metrics.Handler(nil).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "GET", "/metrics", nil))
	return rec.Body.String()
}

func TestSkewCheck(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name    string
		skew    time.Duration
		warn    bool
		metric  string
		failing bool
	}{
		{name: "in sync", skew: 30 * time.Millisecond, metric: "muster_clock_skew_seconds 0.03\n"},
		{name: "replica behind", skew: -3 * time.Second, warn: true, metric: "muster_clock_skew_seconds -3\n"},
		{name: "replica ahead", skew: 2500 * time.Millisecond, warn: true, metric: "muster_clock_skew_seconds 2.5\n"},
		{name: "database unavailable", failing: true, metric: "muster_clock_skew_seconds 2.5\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var log bytes.Buffer
			rc := clock.NewManual(now)
			db := dbClock{at: now.Add(-tt.skew), rc: rc}
			if tt.failing {
				db.err = errors.New("conn closed")
			}
			skewCheck{q: db, real: rc, log: logging.New(&log, logging.LevelInfo)}.check(t.Context())
			if !strings.Contains(scrape(t), "\n"+tt.metric) {
				t.Errorf("metrics lack %q", tt.metric)
			}
			warned := strings.Contains(log.String(), `"level":"WARN","event":"clock_skew","skew_seconds":`)
			if warned != tt.warn || (tt.warn && !strings.Contains(log.String(), `"warning_seconds":2`)) {
				t.Errorf("log %s", log.String())
			}
		})
	}
}

func TestSkewCheckRunsEveryTick(t *testing.T) {
	rc := clock.NewManual(time.Now())
	var log bytes.Buffer
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		skewCheck{q: dbClock{at: rc.Now().Add(-5 * time.Second), rc: rc}, real: rc,
			log: logging.New(&log, logging.LevelInfo)}.run(ctx, ticks)
		close(done)
	}()
	ticks <- time.Time{}
	ticks <- time.Time{}
	cancel()
	<-done
	if n := strings.Count(log.String(), `"event":"clock_skew"`); n != 3 {
		t.Errorf("%d clock_skew lines after the first check and two ticks, want 3", n)
	}
}

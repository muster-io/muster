// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package clock_test

import (
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
)

// within reports whether got is within a second of want.
func within(got, want time.Time) bool {
	d := got.Sub(want)
	return d >= -time.Second && d <= time.Second
}

func TestBusinessFollowsOffsetRealDoesNot(t *testing.T) {
	clocks, business := clock.System()
	if now := time.Now(); !within(clocks.Business.Now(), now) || !within(clocks.Real.Now(), now) {
		t.Fatalf("without an offset both clocks read the system time: business %v, real %v, system %v",
			clocks.Business.Now(), clocks.Real.Now(), now)
	}

	business.SetOffset(10 * time.Minute)
	if business.Offset() != 10*time.Minute {
		t.Errorf("Offset = %v, want 10m", business.Offset())
	}
	now := time.Now()
	if got := clocks.Business.Now(); !within(got, now.Add(10*time.Minute)) {
		t.Errorf("business clock %v, want about %v (system time plus 10m)", got, now.Add(10*time.Minute))
	}
	if got := clocks.Real.Now(); !within(got, now) {
		t.Errorf("real clock %v moved with the offset, want about %v", got, now)
	}
}

func TestZeroBusiness(t *testing.T) {
	var b clock.Business
	if got := b.Now(); !within(got, time.Now()) {
		t.Errorf("zero business clock %v, want the system time", got)
	}
}

func TestManual(t *testing.T) {
	start := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	m := clock.NewManual(start)
	var c clock.Clock = m
	if !c.Now().Equal(start) {
		t.Fatalf("Now = %v, want %v", c.Now(), start)
	}
	m.Advance(90 * time.Second)
	if want := start.Add(90 * time.Second); !m.Now().Equal(want) {
		t.Errorf("after Advance: %v, want %v", m.Now(), want)
	}
	later := start.Add(24 * time.Hour)
	m.Set(later)
	if !m.Now().Equal(later) {
		t.Errorf("after Set: %v, want %v", m.Now(), later)
	}
}

func TestManualConcurrent(t *testing.T) {
	m := clock.NewManual(time.Unix(0, 0))
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			m.Advance(time.Second)
			_ = m.Now()
		})
	}
	wg.Wait()
	if got := m.Now(); !got.Equal(time.Unix(10, 0)) {
		t.Errorf("after 10 concurrent advances: %v", got)
	}
}

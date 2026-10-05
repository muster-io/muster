// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package clock holds the two clocks of Muster, which code takes by injection instead of calling time.Now() or SQL
// now() (ADR-0006). The business clock is the time of the domain: domain timestamps, timers, windows, retention,
// sessions, the alive mark and downtime; in development mode it runs ahead of the system time by the offset of the
// development clock. The real clock is always the system time, for what must agree with the world outside the database
// or with other processes: TOTP steps, ID-token times, webhook signatures, row and Leader leases, the clock skew check
// and replica records. Each has a manual implementation that drives tests.
package clock

import (
	"sync"
	"sync/atomic"
	"time"
)

// Clock tells the time.
type Clock interface {
	Now() time.Time
}

// Real is the system time.
type Real struct{}

func (Real) Now() time.Time { return time.Now() }

// Business is the system time plus an offset that only the development clock moves; its zero value has no offset.
type Business struct {
	offset atomic.Int64
}

func (b *Business) Now() time.Time { return time.Now().Add(b.Offset()) }

// Offset is how far the business clock runs ahead of the system time.
func (b *Business) Offset() time.Duration { return time.Duration(b.offset.Load()) }

// SetOffset sets how far the business clock runs ahead of the system time.
func (b *Business) SetOffset(d time.Duration) { b.offset.Store(int64(d)) }

// Manual is a clock that moves only when told to, for tests.
type Manual struct {
	mu  sync.Mutex
	now time.Time
}

// NewManual returns a manual clock that reads t.
func NewManual(t time.Time) *Manual {
	return &Manual{now: t}
}

func (m *Manual) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now
}

// Set moves the clock to t.
func (m *Manual) Set(t time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = t
}

// Advance moves the clock forward by d.
func (m *Manual) Advance(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = m.now.Add(d)
}

// Clocks are the two clocks of a process.
type Clocks struct {
	Business Clock
	Real     Clock
}

// System returns the clocks of a running process: the real clock and a business clock without an offset, which the
// development clock can move.
func System() (Clocks, *Business) {
	b := &Business{}
	return Clocks{Business: b, Real: Real{}}, b
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/auth/dbgen"
)

type fakePruneQueries struct {
	sessions  dbgen.PruneSessionsParams
	throttles dbgen.PruneSignInThrottlesParams
	err       error
}

func (q *fakePruneQueries) PruneSessions(_ context.Context, arg dbgen.PruneSessionsParams) (int64, error) {
	q.sessions = arg
	return 7, q.err
}

func (q *fakePruneQueries) PruneSignInThrottles(_ context.Context, arg dbgen.PruneSignInThrottlesParams) (int64,
	error,
) {
	q.throttles = arg
	return 3, q.err
}

func TestPrunerCutoffs(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	q := &fakePruneQueries{}
	p := NewPruner(q)
	if n, err := p.Sessions(t.Context(), 4, now, 1000); n != 7 || err != nil {
		t.Fatalf("Sessions = %d, %v", n, err)
	}
	want := dbgen.PruneSessionsParams{OrgID: 4, Before: now.Add(-7 * 24 * time.Hour), BatchSize: 1000}
	if q.sessions != want {
		t.Errorf("sessions %+v, want %+v", q.sessions, want)
	}
	if n, err := p.SignInThrottles(t.Context(), 4, now, 1000); n != 3 || err != nil {
		t.Fatalf("SignInThrottles = %d, %v", n, err)
	}
	wantThrottles := dbgen.PruneSignInThrottlesParams{OrgID: 4, Before: now.Add(-24 * time.Hour), BatchSize: 1000}
	if q.throttles != wantThrottles {
		t.Errorf("throttles %+v, want %+v", q.throttles, wantThrottles)
	}
}

func TestPrunerErrors(t *testing.T) {
	p := NewPruner(&fakePruneQueries{err: errors.New("conn closed")})
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	if n, err := p.Sessions(t.Context(), 1, now, 10); n != 0 || err == nil ||
		err.Error() != "delete ended sessions: conn closed" {
		t.Errorf("Sessions = %d, %v", n, err)
	}
	if n, err := p.SignInThrottles(t.Context(), 1, now, 10); n != 0 || err == nil ||
		err.Error() != "delete stale sign-in throttles: conn closed" {
		t.Errorf("SignInThrottles = %d, %v", n, err)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/muster-io/muster/internal/auth/dbgen"
)

const (
	// SessionPruneAfter is auth.session_prune_after: a session is deleted this long after it ended, by sign-out, by an
	// administrative action or at its idle timeout or lifetime. Until then a browser that comes back with its cookie
	// still learns why it has to sign in again (session_expired) instead of a bare unauthenticated, for a week that
	// covers a weekend or a short holiday, the same span as auth.session_lifetime.
	SessionPruneAfter = 7 * 24 * time.Hour
	// ThrottlePruneAfter is auth.signin_throttle_prune_after: a sign-in throttle row is deleted once no failure was
	// counted for this long and its block has passed. Forgetting the count then only restarts the auth.signin_throttle
	// sequence, which reaches its 60 s maximum again within minutes of new failures.
	ThrottlePruneAfter = 24 * time.Hour
)

// PruneQueries are the deletes of short-lived pruning; *dbgen.Queries implements them.
type PruneQueries interface {
	PruneSessions(ctx context.Context, arg dbgen.PruneSessionsParams) (int64, error)
	PruneSignInThrottles(ctx context.Context, arg dbgen.PruneSignInThrottlesParams) (int64, error)
}

// Pruner deletes the sessions and sign-in throttles that can no longer be used, one batch at a time, for the
// short_lived_pruning Leader task. Its methods are PruneTable deletes.
type Pruner struct {
	q PruneQueries
}

// NewPruner returns the Pruner over q.
func NewPruner(q PruneQueries) Pruner {
	return Pruner{q: q}
}

// Sessions deletes at most limit sessions of the Organization that ended or expired SessionPruneAfter before now.
func (p Pruner) Sessions(ctx context.Context, orgID int64, now time.Time, limit int32) (int64, error) {
	n, err := p.q.PruneSessions(ctx, dbgen.PruneSessionsParams{
		OrgID: orgID, Before: now.Add(-SessionPruneAfter), BatchSize: limit,
	})
	if err != nil {
		return 0, fmt.Errorf("delete ended sessions: %w", err)
	}
	return n, nil
}

// SignInThrottles deletes at most limit sign-in throttle rows of the Organization whose last failure and block are
// ThrottlePruneAfter before now.
func (p Pruner) SignInThrottles(ctx context.Context, orgID int64, now time.Time, limit int32) (int64, error) {
	n, err := p.q.PruneSignInThrottles(ctx, dbgen.PruneSignInThrottlesParams{
		OrgID: orgID, Before: now.Add(-ThrottlePruneAfter), BatchSize: limit,
	})
	if err != nil {
		return 0, fmt.Errorf("delete stale sign-in throttles: %w", err)
	}
	return n, nil
}

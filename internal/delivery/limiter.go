// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"context"
	"fmt"
	"time"

	"github.com/muster-io/muster/internal/delivery/dbgen"
)

// The limiters (C-11.FR-3, ADR-0005): a token bucket per Destination and per Connection in rate_limit_buckets, shared by
// every replica, with the capacity limit and a refill of limit / per_seconds on the business clock, created full on
// first use. A send and an edit take one token alike; a delivery takes a token from both buckets of its Destination or
// from neither, and an outgoing webhook has only its Destination's bucket. A RetryAfter holds the bucket of its scope
// until its end.

// taken is the result of taking tokens: whether they were taken, and otherwise when every empty bucket holds one.
type taken struct {
	ok  bool
	due time.Time
}

// subject names the buckets of a call: a Destination with its Connection, or a Connection alone.
type subject struct {
	destination *int64
	connection  *int64
}

// subjectOf is the subject of a call to d.
func subjectOf(d Destination) subject {
	id := d.ID
	return subject{destination: &id, connection: d.Connection}
}

// takeTokens takes one token from each bucket of s at now, creating the buckets on first use, in one statement that
// takes from all of them or from none.
func takeTokens(ctx context.Context, q queries, orgID int64, s subject, now time.Time) (taken, error) {
	if err := q.EnsureBuckets(ctx, dbgen.EnsureBucketsParams{OrgID: orgID, Now: now,
		DestinationID: nullInt(s.destination), ConnectionID: nullInt(s.connection)}); err != nil {
		return taken{}, fmt.Errorf("create the limiter buckets: %w", err)
	}
	r, err := q.TakeTokens(ctx, dbgen.TakeTokensParams{OrgID: orgID, Now: now, DestinationID: nullInt(s.destination),
		ConnectionID: nullInt(s.connection)})
	if err != nil {
		return taken{}, fmt.Errorf("take the limiter tokens: %w", err)
	}
	return taken{ok: r.Taken, due: r.Due.UTC()}, nil
}

// holdBucket holds the bucket that a RetryAfter of d names — d's own or its Connection's — until until: no token
// before then, one at that time, so that the next call to any Destination it covers waits exactly as asked.
func holdBucket(ctx context.Context, q queries, orgID int64, d Destination, o Outcome, until time.Time) error {
	kind, id := string(ScopeDestination), d.ID
	if o.Scope == ScopeConnection && d.Connection != nil {
		kind, id = string(ScopeConnection), *d.Connection
	}
	if err := q.HoldBucket(ctx, dbgen.HoldBucketParams{OrgID: orgID, SubjectKind: kind, SubjectID: id,
		Until: until}); err != nil {
		return fmt.Errorf("hold the %s limiter: %w", kind, err)
	}
	return nil
}

// ConnectionToken takes, through db — the pool or the caller's transaction — one token of the limiter of the
// Connection connection at now on the business clock, creating its bucket on first use (C-11.FR-3): the gateway sender
// of C-30.FR-4 waits for it before every attempt of a call, as a delivery does for its Connection. ok is false when no
// token is free; due is then when the bucket will hold one.
func ConnectionToken(ctx context.Context, db dbgen.DBTX, orgID, connection int64, now time.Time) (ok bool,
	due time.Time, err error) {
	q := pgQueries{Queries: dbgen.New(db), exec: db}
	t, err := takeTokens(ctx, q, orgID, subject{connection: &connection}, now)
	return t.ok, t.due, err
}

// HoldConnection holds the bucket of the Connection connection, through db, until until on the business clock: a
// gateway's RetryAfter, which holds the whole gateway whatever its scope, as a call has no Destination.
func HoldConnection(ctx context.Context, db dbgen.DBTX, orgID, connection int64, until time.Time) error {
	q := pgQueries{Queries: dbgen.New(db), exec: db}
	return holdBucket(ctx, q, orgID, Destination{Connection: &connection}, Outcome{Scope: ScopeConnection}, until)
}

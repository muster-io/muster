// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package deliverytest

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
)

// Unlimited is an interactive path whose limiters always have a token, without a database, for the tests of its
// callers: every statement succeeds and every take of tokens is granted at once.
func Unlimited(orgID int64, clocks clock.Clocks) *delivery.Interactive {
	var d unlimitedDB
	return &delivery.Interactive{OrgID: orgID, Store: delivery.NewStore(d, d), Clocks: clocks}
}

// unlimitedDB answers the limiter's statements: the buckets exist, a take is granted, a hold is held.
type unlimitedDB struct{}

func (unlimitedDB) Begin(context.Context) (pgx.Tx, error) { return unlimitedTx{}, nil }

func (unlimitedDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (unlimitedDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, pgx.ErrNoRows
}

func (unlimitedDB) QueryRow(context.Context, string, ...any) pgx.Row { return grantedRow{} }

// unlimitedTx is a transaction of unlimitedDB; only what the limiter calls is implemented.
type unlimitedTx struct {
	pgx.Tx
}

func (unlimitedTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return unlimitedDB{}.Exec(ctx, sql, args...)
}

func (unlimitedTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return unlimitedDB{}.Query(ctx, sql, args...)
}

func (unlimitedTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return unlimitedDB{}.QueryRow(ctx, sql, args...)
}

func (unlimitedTx) Begin(context.Context) (pgx.Tx, error) { return unlimitedTx{}, nil }

func (unlimitedTx) Commit(context.Context) error { return nil }

func (unlimitedTx) Rollback(context.Context) error { return nil }

// grantedRow is the answer of a take of tokens: taken, due now.
type grantedRow struct{}

func (grantedRow) Scan(dest ...any) error {
	for _, d := range dest {
		switch v := d.(type) {
		case *bool:
			*v = true
		case *time.Time:
			*v = time.Time{}
		}
	}
	return nil
}

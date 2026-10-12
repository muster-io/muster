// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package oidc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/internalalerts"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/oidc/dbgen"
)

// ExpiryQueries are what the OIDC client secret expiry check reads and writes in its transaction: the OIDC settings
// row, locked, and the Internal alerts of the built-in Integration.
type ExpiryQueries interface {
	internalalerts.Store
	LockSettings(ctx context.Context, orgID int64) (int64, error)
	GetSettings(ctx context.Context, orgID int64) (dbgen.OidcSetting, error)
}

// ExpiryStore runs the check in one transaction.
type ExpiryStore interface {
	InTx(ctx context.Context, f func(ExpiryQueries) error) error
}

// NewExpiryStore is the ExpiryStore over the main pool.
func NewExpiryStore(pool *pgxpool.Pool) ExpiryStore {
	return expiryStore{pool: pool}
}

type expiryStore struct{ pool *pgxpool.Pool }

type expiryQueries struct {
	*dbgen.Queries
	internalalerts.Store
}

func (s expiryStore) InTx(ctx context.Context, f func(ExpiryQueries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return f(expiryQueries{Queries: dbgen.New(tx), Store: internalalerts.NewStore(tx)})
	})
}

// ExpiryCheck is the Leader task that raises MusterOIDCSecretExpiring while the OIDC client secret expires within
// oidc.secret_expiry_lead and resolves it when the date moves further out or is removed (C-19.FR-5). It raises and
// resolves only on transitions, which it reads from the database: the open Internal alert and its pending raise or
// resolve. So running it twice, on two Leaders at once or after a handover, writes nothing more; the lock of the
// settings row orders two runs.
type ExpiryCheck struct {
	Store ExpiryStore
	// Business is the business clock: the expiry date is a domain date, compared as the settings warning compares it.
	Business clock.Clock
	Log      *logging.Logger
	// RunbookBase is MUSTER_RUNBOOK_BASE_URL.
	RunbookBase string
}

// Check runs the check for the Organization orgID.
func (c ExpiryCheck) Check(ctx context.Context, orgID int64) error {
	raiser := internalalerts.NewRaiser(orgID, c.RunbookBase)
	var raised *time.Time
	err := c.Store.InTx(ctx, func(q ExpiryQueries) error {
		raised = nil
		expiresOn, err := expiryDate(ctx, q, orgID)
		if err != nil {
			return err
		}
		now := c.Business.Now()
		want := secretExpiring(expiresOn, now)
		firing, err := raiser.Firing(ctx, q, internalalerts.OIDCSecretExpiring)
		if err != nil {
			return err
		}
		switch {
		case want && !firing:
			if err := raiser.Raise(ctx, q, now, internalalerts.OIDCSecretExpiring, internalalerts.Entity{},
				nil); err != nil {
				return fmt.Errorf("raise %s: %w", internalalerts.OIDCSecretExpiring.Name, err)
			}
			raised = expiresOn
		case !want && firing:
			if err := raiser.Resolve(ctx, q, now, internalalerts.OIDCSecretExpiring, ""); err != nil {
				return fmt.Errorf("resolve %s: %w", internalalerts.OIDCSecretExpiring.Name, err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("check the expiry of the oidc client secret: %w", err)
	}
	if raised != nil {
		c.Log.Log(ctx, logging.OIDCSecretExpiring, logging.F("expires_on", raised.Format(time.DateOnly)))
	}
	return nil
}

// expiryDate locks the OIDC settings of the Organization and reads the expiry date of the client secret; nil when
// there are no settings or no date.
func expiryDate(ctx context.Context, q ExpiryQueries, orgID int64) (*time.Time, error) {
	if _, err := q.LockSettings(ctx, orgID); errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("lock the oidc settings: %w", err)
	}
	row, err := q.GetSettings(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("read the oidc settings: %w", err)
	}
	if !row.ClientSecretExpiresOn.Valid {
		return nil, nil
	}
	d := row.ClientSecretExpiresOn.Time.UTC()
	return &d, nil
}

// secretExpiring reports whether a client secret expiring on expiresOn expires within oidc.secret_expiry_lead of now.
func secretExpiring(expiresOn *time.Time, now time.Time) bool {
	return expiresOn != nil && expiresOn.Sub(now) < SecretExpiryLead
}

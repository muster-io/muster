// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package gateways holds the Notification gateways of C-30: the Bridge Connections whose stored info declares the
// role gateway, and the contacts of people at them (C-30.FR-3). A person adds a contact from their own web session and
// confirms it with a code that the gateway delivers through sendNotification on the interactive path; the code is
// hashed at rest, compared in constant time and only in the session that asked for it (ADR-0013), within the limits
// of gateway.contact_code and gateway.contact_code_requests on the business clock. An Admin sees and removes the
// contacts of any User, never sets one. A contact is personal data, not a Secret: it is stored in plain text, and
// masked wherever it would be logged. Deleting a User erases their contacts and codes (C-03.FR-13).
package gateways

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/gateways/dbgen"
	"github.com/muster-io/muster/internal/logging"
	usersdb "github.com/muster-io/muster/internal/users/dbgen"
)

// DBTX is the pool or transaction the queries run in.
type DBTX = dbgen.DBTX

// Pool is the main pool: the queries, and the transactions of a change.
type Pool interface {
	DBTX
	db.Beginner
}

// Queries are the queries of the package, with the insert of the Audit log.
type Queries interface {
	ListGateways(ctx context.Context, orgID int64) ([]dbgen.ListGatewaysRow, error)
	GetGateway(ctx context.Context, arg dbgen.GetGatewayParams) (dbgen.GetGatewayRow, error)
	ConfirmedContact(ctx context.Context, arg dbgen.ConfirmedContactParams) (string, error)
	LockContactUser(ctx context.Context, arg dbgen.LockContactUserParams) (dbgen.LockContactUserRow, error)
	GetUserByPublicID(ctx context.Context, arg dbgen.GetUserByPublicIDParams) (dbgen.GetUserByPublicIDRow, error)
	LockContactText(ctx context.Context, arg dbgen.LockContactTextParams) error
	CountUserCodes(ctx context.Context, arg dbgen.CountUserCodesParams) (dbgen.CountUserCodesRow, error)
	CountContactCodes(ctx context.Context, arg dbgen.CountContactCodesParams) (dbgen.CountContactCodesRow, error)
	ListContactsOfUser(ctx context.Context, arg dbgen.ListContactsOfUserParams) ([]dbgen.ListContactsOfUserRow, error)
	LockContactOfUser(ctx context.Context, arg dbgen.LockContactOfUserParams) (dbgen.LockContactOfUserRow, error)
	InsertContact(ctx context.Context, arg dbgen.InsertContactParams) (int64, error)
	DeleteContact(ctx context.Context, arg dbgen.DeleteContactParams) (int64, error)
	DeleteUnconfirmedContacts(ctx context.Context, arg dbgen.DeleteUnconfirmedContactsParams) (
		[]dbgen.DeleteUnconfirmedContactsRow, error)
	DeleteReplacedContacts(ctx context.Context, arg dbgen.DeleteReplacedContactsParams) (
		[]dbgen.DeleteReplacedContactsRow, error)
	ConfirmContact(ctx context.Context, arg dbgen.ConfirmContactParams) error
	DeleteUserContacts(ctx context.Context, arg dbgen.DeleteUserContactsParams) ([]dbgen.DeleteUserContactsRow, error)
	DeleteUserCodes(ctx context.Context, arg dbgen.DeleteUserCodesParams) (int64, error)
	MarkCodeSent(ctx context.Context, arg dbgen.MarkCodeSentParams) error
	VoidEarlierCodes(ctx context.Context, arg dbgen.VoidEarlierCodesParams) (int64, error)
	InsertCode(ctx context.Context, arg dbgen.InsertCodeParams) (int64, error)
	VoidCode(ctx context.Context, arg dbgen.VoidCodeParams) error
	DeleteCode(ctx context.Context, arg dbgen.DeleteCodeParams) error
	LockNewestCode(ctx context.Context, arg dbgen.LockNewestCodeParams) (dbgen.LockNewestCodeRow, error)
	RecordWrongCode(ctx context.Context, arg dbgen.RecordWrongCodeParams) (int64, error)
	UseCode(ctx context.Context, arg dbgen.UseCodeParams) (int64, error)
	audit.Store
}

// Store runs the queries alone or in one transaction.
type Store interface {
	Queries
	InTx(ctx context.Context, f func(Queries) error) error
}

// NewStore is the Store over the main pool.
func NewStore(p Pool) Store {
	return pgStore{pgQueries: queriesOver(p), pool: p}
}

type pgQueries struct {
	*dbgen.Queries
	audit.Store
}

func queriesOver(d DBTX) pgQueries {
	return pgQueries{Queries: dbgen.New(d), Store: audit.NewStore(d)}
}

type pgStore struct {
	pgQueries
	pool Pool
}

func (s pgStore) InTx(ctx context.Context, f func(Queries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return f(queriesOver(tx)) })
}

// Path is the interactive path of delivery (C-11.FR-2); *delivery.Interactive implements it.
type Path interface {
	Do(ctx context.Context, s delivery.Subject, op delivery.Op) (delivery.Outcome, error)
}

// Config is what the Service needs.
type Config struct {
	OrgID int64
	Store Store
	Audit *audit.Writer
	// Business is the business clock: the times of contacts and codes, the expiry of a code and the request limits.
	Business clock.Clock
	Log      *logging.Logger
	// Gateway sends the notification with a code, through Path, the interactive path.
	Gateway delivery.Gateway
	Path    Path
	// Random is where codes come from; nil is crypto/rand.
	Random io.Reader
}

// Service holds the Notification gateways of an Organization and the contacts of people at them.
type Service struct {
	cfg Config
}

// New returns the Service of cfg.
func New(cfg Config) *Service {
	if cfg.Random == nil {
		cfg.Random = randReader
	}
	return &Service{cfg: cfg}
}

// Gateway is a Notification gateway: a Bridge Connection, not deleted, whose stored info declares the role gateway.
type Gateway struct {
	ID       int64
	PublicID string
	Name     string
}

// Gateways lists the Notification gateways of the Organization by name (C-30.FR-1, FR-9).
func (s *Service) Gateways(ctx context.Context) ([]Gateway, error) {
	rows, err := s.cfg.Store.ListGateways(ctx, s.cfg.OrgID)
	if err != nil {
		return nil, fmt.Errorf("list the notification gateways: %w", err)
	}
	out := make([]Gateway, len(rows))
	for i, r := range rows {
		out[i] = Gateway{ID: r.ID, PublicID: r.PublicID, Name: r.Name}
	}
	return out, nil
}

// ConfirmedContact is the confirmed contact of the User userID at the Notification gateway gatewayID, the one a call
// goes to; ok is false without one, or when the gateway is deleted or no longer a Notification gateway. An unconfirmed
// contact is never called (C-30.FR-3).
func (s *Service) ConfirmedContact(ctx context.Context, userID, gatewayID int64) (contact string, ok bool, err error) {
	c, err := s.cfg.Store.ConfirmedContact(ctx, dbgen.ConfirmedContactParams{OrgID: s.cfg.OrgID, UserID: userID,
		ConnectionID: gatewayID})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read the confirmed contact: %w", err)
	}
	return c, true, nil
}

// RemoveUserContacts deletes the contacts and codes of the User userID in the transaction tx of their deletion
// (C-03.FR-13 revised for L2), each contact recorded as gateway_contact.deleted with the reason user_deleted by
// actor. It returns nothing to run after the commit: nothing is logged.
func (s *Service) RemoveUserContacts(ctx context.Context, tx usersdb.DBTX, userID int64, actor audit.Actor,
	t audit.Transport, addr netip.Addr) (func(context.Context), error) {
	return nil, s.removeUserContacts(ctx, queriesOver(tx), userID, actor, t, addr)
}

func (s *Service) removeUserContacts(ctx context.Context, q Queries, userID int64, actor audit.Actor,
	t audit.Transport, addr netip.Addr) error {
	u, err := q.LockContactUser(ctx, dbgen.LockContactUserParams{OrgID: s.cfg.OrgID, ID: userID})
	if err != nil {
		return fmt.Errorf("read the user %d: %w", userID, err)
	}
	if _, err := q.DeleteUserCodes(ctx, dbgen.DeleteUserCodesParams{OrgID: s.cfg.OrgID, UserID: userID}); err != nil {
		return fmt.Errorf("delete the contact codes of %s: %w", u.PublicID, err)
	}
	rows, err := q.DeleteUserContacts(ctx, dbgen.DeleteUserContactsParams{OrgID: s.cfg.OrgID, UserID: userID})
	if err != nil {
		return fmt.Errorf("delete the gateway contacts of %s: %w", u.PublicID, err)
	}
	for _, c := range rows {
		ref := gatewayRef{publicID: c.ConnectionPublicID, name: c.ConnectionName}
		if err := s.record(ctx, q, actor, t, addr, ActionDeleted, ref, c.PublicID, removed(ref, c.Contact),
			details(ref, u.PublicID, ReasonUserDeleted, "")); err != nil {
			return err
		}
	}
	return nil
}

// Pruner deletes the codes that expired CodePruneAfter ago, for the short_lived_pruning Leader task.
type Pruner struct {
	q interface {
		PruneCodes(ctx context.Context, arg dbgen.PruneCodesParams) (int64, error)
	}
}

// NewPruner returns the Pruner over d.
func NewPruner(d DBTX) Pruner {
	return Pruner{q: dbgen.New(d)}
}

// Codes deletes at most limit codes of the Organization that expired CodePruneAfter before now, used and void ones
// included.
func (p Pruner) Codes(ctx context.Context, orgID int64, now time.Time, limit int32) (int64, error) {
	n, err := p.q.PruneCodes(ctx, dbgen.PruneCodesParams{OrgID: orgID, Before: now.Add(-CodePruneAfter),
		BatchSize: limit})
	if err != nil {
		return 0, fmt.Errorf("delete expired gateway contact codes: %w", err)
	}
	return n, nil
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package destinations

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/destinations/dbgen"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/templates"
)

// The Audit log action and resource type, and the live hint, of Destinations (C-03.FR-14).
const (
	ActionDeleted       = "destination.deleted"
	ResourceDestination = "destination"
	Hint                = "destination"
)

// ErrVersionMismatch is an If-Match that names another version of the Destination.
var ErrVersionMismatch = errors.New("the destination changed since it was read")

// Requester is who asks for a change and how: the actor and the Transport of the Audit log entry, and the client
// address.
type Requester struct {
	Actor     audit.Actor
	Transport audit.Transport
	Address   netip.Addr
}

// Retire is the hook that delivery runs in the transaction tx that deleted the Destination destinationID: the final
// edits of its open Root messages, and the wipe of its secrets once none is pending (C-11.FR-14).
type Retire func(ctx context.Context, tx dbgen.DBTX, destinationID int64) error

// Renamed is the hook that delivery runs in the transaction tx that renamed the Destination publicID to name: every
// Internal alert about it takes the new name, as the same firing.
type Renamed func(ctx context.Context, tx dbgen.DBTX, publicID, name string) error

// Routes is the hook that routing runs in the transaction tx that deletes the Destination destinationID, before it
// leaves its Routes: each of them gets a new version and its hint, and its membership lock, so that no Alert Group of
// it is rendered for the Destination meanwhile.
type Routes func(ctx context.Context, tx dbgen.DBTX, destinationID int64) error

// TxQueries are the queries of a change of a Destination, with the insert of the Audit log and the live hint, in one
// transaction.
type TxQueries interface {
	LockDestination(ctx context.Context, arg dbgen.LockDestinationParams) (dbgen.LockDestinationRow, error)
	MarkDestinationDeleted(ctx context.Context, arg dbgen.MarkDestinationDeletedParams) error
	DeleteDestinationRoutes(ctx context.Context, arg dbgen.DeleteDestinationRoutesParams) error
	writeQueries
	audit.Store
	Notify(ctx context.Context, h db.Hint) error
	// DB is the transaction the queries run in, which the Retire hook writes through.
	DB() dbgen.DBTX
}

// Writer runs the queries of a change in one transaction.
type Writer interface {
	InTx(ctx context.Context, f func(TxQueries) error) error
}

// NewWriter is the Writer over the main pool.
func NewWriter(pool *pgxpool.Pool) Writer { return pgWriter{pool: pool} }

type pgWriter struct {
	pool *pgxpool.Pool
}

func (w pgWriter) InTx(ctx context.Context, f func(TxQueries) error) error {
	return pgx.BeginFunc(ctx, w.pool, func(tx pgx.Tx) error {
		return f(txQueries{Queries: dbgen.New(tx), Store: audit.NewStore(tx), tx: tx})
	})
}

type txQueries struct {
	*dbgen.Queries
	audit.Store
	tx pgx.Tx
}

func (q txQueries) Notify(ctx context.Context, h db.Hint) error { return db.NotifyHint(ctx, q.tx, h) }

func (q txQueries) DB() dbgen.DBTX { return q.tx }

// WriterConfig is what the changes of Destinations need: the Writer, the Audit log, the business clock that dates the
// changes, routing's Routes hook and delivery's Retire and Renamed hooks; and for the saves, the validation of Mention settings, the
// Destination checks of the Mattermost and Telegram types and delivery's end of a Broken state, and for outgoing
// webhooks the Keyring that encrypts their secrets and the template sandbox that checks their request.
type WriterConfig struct {
	Writer     Writer
	Audit      *audit.Writer
	Business   clock.Clock
	Routes     Routes
	Retire     Retire
	Renamed    Renamed
	Mentions   MentionValidator
	Mattermost MattermostChecker
	Telegram   TelegramChecker
	Healthy    Healthy
	Keyring    *keyring.Keyring
	Templates  *templates.Sandbox
}

// SetWriter fills what the changes of Destinations need, before any change.
func (s *Service) SetWriter(c WriterConfig) {
	s.writer = c
}

// Delete deletes the Destination publicID (C-11.FR-14); a non-nil version must be its current one. In one
// transaction it is marked deleted — it leaves every list and reads as missing, the row stays for history — it leaves
// every Route, each of which gets a new version and its hint through the Routes hook, delivery gives its open Root
// messages their final edit through the Retire hook, which also ends the waiting events of an outgoing webhook as Not
// delivered, and the Audit log entry destination.deleted is written. Its secrets are wiped once the final edits are done
// and no call of it is in flight. A deleted or unknown Destination is ErrNotFound.
func (s *Service) Delete(ctx context.Context, r Requester, publicID string, version *int64) error {
	id, err := publicid.Parse(publicid.Destination, publicID)
	if err != nil {
		return ErrNotFound
	}
	err = s.writer.Writer.InTx(ctx, func(q TxQueries) error {
		d, err := q.LockDestination(ctx, dbgen.LockDestinationParams{OrgID: s.orgID, PublicID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock the destination %s: %w", id, err)
		}
		if version != nil && *version != d.Version {
			return ErrVersionMismatch
		}
		now := s.writer.Business.Now().UTC()
		if err := q.MarkDestinationDeleted(ctx, dbgen.MarkDestinationDeletedParams{OrgID: s.orgID, ID: d.ID,
			Now: pgtype.Timestamptz{Time: now, Valid: true}}); err != nil {
			return fmt.Errorf("delete the destination %s: %w", id, err)
		}
		if s.writer.Routes != nil {
			if err := s.writer.Routes(ctx, q.DB(), d.ID); err != nil {
				return fmt.Errorf("change the routes of the destination %s: %w", id, err)
			}
		}
		if err := q.DeleteDestinationRoutes(ctx, dbgen.DeleteDestinationRoutesParams{OrgID: s.orgID,
			DestinationID: d.ID}); err != nil {
			return fmt.Errorf("remove the destination %s from its routes: %w", id, err)
		}
		if s.writer.Retire != nil {
			if err := s.writer.Retire(ctx, q.DB(), d.ID); err != nil {
				return fmt.Errorf("retire the deliveries of the destination %s: %w", id, err)
			}
		}
		if err := s.writer.Audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport, Action: ActionDeleted,
			Resource: audit.Resource{Type: ResourceDestination, PublicID: d.PublicID, Name: d.Name},
			Diff:     []audit.Change{{Pointer: "/deleted_at", After: now}}, SourceAddress: r.Address,
		}); err != nil {
			return err
		}
		return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint, ID: d.PublicID})
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.info[id]; ok {
		metrics.DestinationInfo.Delete(id, old)
		delete(s.info, id)
	}
	return nil
}

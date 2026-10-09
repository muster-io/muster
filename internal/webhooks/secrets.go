// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package webhooks

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/webhooks/dbgen"
)

// TypeWebhook is the Destination type of an outgoing webhook.
const TypeWebhook = "webhook"

// The Audit log action, resource type and live hint of a change of the Secrets of a Destination.
const (
	ActionUpdated       = "destination.updated"
	ResourceDestination = "destination"
	HintDestination     = "destination"
)

var (
	// ErrNotFound is a Destination that does not exist in the Organization or is deleted.
	ErrNotFound = errors.New("no such destination")
	// ErrNotWebhook is a Destination that is not an outgoing webhook (not_webhook_destination).
	ErrNotWebhook = errors.New("the destination is not an outgoing webhook")
	// ErrNoSecret is a Secret the Destination does not have.
	ErrNoSecret = errors.New("no such secret")
	// ErrVersionMismatch is an If-Match that names another version of the Destination's Secrets.
	ErrVersionMismatch = errors.New("the secrets changed since they were read")
)

// Requester is who asks for a change and how: the actor and the Transport of the Audit log entry, and the client
// address.
type Requester struct {
	Actor     audit.Actor
	Transport audit.Transport
	Address   netip.Addr
}

// Store are the queries the package runs outside a change.
type Store interface {
	GetWebhookDestination(ctx context.Context, arg dbgen.GetWebhookDestinationParams) (
		dbgen.GetWebhookDestinationRow, error)
	ListSecrets(ctx context.Context, arg dbgen.ListSecretsParams) ([]dbgen.ListSecretsRow, error)
	GetTarget(ctx context.Context, arg dbgen.GetTargetParams) (dbgen.GetTargetRow, error)
	ListSecretValues(ctx context.Context, arg dbgen.ListSecretValuesParams) ([]dbgen.ListSecretValuesRow, error)
}

// NewStore is the Store over d, the main pool.
func NewStore(d dbgen.DBTX) Store { return dbgen.New(d) }

// TxQueries are the queries of a change of the secrets of a Destination, with the insert of the Audit log and the
// live hint, in one transaction.
type TxQueries interface {
	LockWebhookDestination(ctx context.Context, arg dbgen.LockWebhookDestinationParams) (
		dbgen.LockWebhookDestinationRow, error)
	BumpDestinationVersion(ctx context.Context, arg dbgen.BumpDestinationVersionParams) (int64, error)
	UpsertSecret(ctx context.Context, arg dbgen.UpsertSecretParams) error
	DeleteSecret(ctx context.Context, arg dbgen.DeleteSecretParams) (int64, error)
	RotateSigningSecret(ctx context.Context, arg dbgen.RotateSigningSecretParams) (int64, error)
	RetirePreviousSigningSecret(ctx context.Context, arg dbgen.RetirePreviousSigningSecretParams) (int64, error)
	audit.Store
	Notify(ctx context.Context, h db.Hint) error
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

// Config is what a Service needs: its queries, the Keyring that encrypts and opens the secrets, the Audit log, the
// business clock that dates the changes, and for the adapter and the bodies of events, MUSTER_PUBLIC_URL and the
// network of its outbound clients.
type Config struct {
	Store     Store
	Writer    Writer
	Keyring   *keyring.Keyring
	Audit     *audit.Writer
	Business  clock.Clock
	PublicURL string
}

// Service holds the secrets of the outgoing webhooks of an Organization and reads what their events need.
type Service struct {
	orgID int64
	cfg   Config
}

// New returns the Service of the Organization orgID.
func New(orgID int64, cfg Config) *Service {
	return &Service{orgID: orgID, cfg: cfg}
}

// Secret is the status of a named Secret: its name and the time of its last change; the value is never read back.
type Secret struct {
	Name      string
	UpdatedAt time.Time
}

// Secrets are the Secrets of a Destination by name, with the version of the Destination, their ETag.
type Secrets struct {
	Items   []Secret
	Version int64
}

// ListSecrets lists the Secrets of the outgoing webhook publicID (C-15.FR-10); another type is ErrNotWebhook.
func (s *Service) ListSecrets(ctx context.Context, publicID string) (Secrets, error) {
	d, err := s.destination(ctx, publicID)
	if err != nil {
		return Secrets{}, err
	}
	rows, err := s.cfg.Store.ListSecrets(ctx, dbgen.ListSecretsParams{OrgID: s.orgID, DestinationID: d.ID})
	if err != nil {
		return Secrets{}, fmt.Errorf("list the secrets of %s: %w", d.PublicID, err)
	}
	out := Secrets{Items: make([]Secret, 0, len(rows)), Version: d.Version}
	for _, r := range rows {
		out.Items = append(out.Items, Secret{Name: r.Name, UpdatedAt: r.ValueUpdatedAt.UTC()})
	}
	return out, nil
}

// SetSecret creates or replaces the Secret name of the outgoing webhook publicID with value (C-15.FR-10); a non-nil
// version must be the current version of its Secrets. It is recorded as destination.updated with the Secret marked
// changed, never its value, and returns the Secret's status and the new version.
func (s *Service) SetSecret(ctx context.Context, r Requester, publicID string, version *int64, name string,
	value logging.Secret) (Secret, int64, error) {
	if err := checkSecret(name, value); err != nil {
		return Secret{}, 0, err
	}
	var out Secret
	var next int64
	err := s.inTx(ctx, publicID, func(q TxQueries, d dbgen.LockWebhookDestinationRow, now time.Time) error {
		if version != nil && *version != d.Version {
			return ErrVersionMismatch
		}
		stored, _, err := s.cfg.Keyring.ApplySecret(FieldSecret, keyring.StoredSecret{}, keyring.Replace(value), now)
		if err != nil {
			return err
		}
		if err := q.UpsertSecret(ctx, dbgen.UpsertSecretParams{DestinationID: d.ID, OrgID: s.orgID, Name: name,
			ValueCiphertext: stored.Ciphertext, ValueKeyID: stored.KeyID, Now: now}); err != nil {
			return fmt.Errorf("store the secret %s of %s: %w", name, d.PublicID, err)
		}
		if next, err = q.BumpDestinationVersion(ctx, dbgen.BumpDestinationVersionParams{Now: now, OrgID: s.orgID,
			ID: d.ID}); err != nil {
			return fmt.Errorf("change the version of %s: %w", d.PublicID, err)
		}
		out = Secret{Name: name, UpdatedAt: now}
		return s.record(ctx, q, r, ActionUpdated, d.PublicID, d.Name,
			[]audit.Change{{Pointer: "/secrets/" + name, SecretChanged: true}})
	})
	if err != nil {
		return Secret{}, 0, err
	}
	return out, next, nil
}

// DeleteSecret deletes the Secret name of the outgoing webhook publicID; a non-nil version must be the current
// version of its Secrets. A Secret it does not have is ErrNoSecret. It is recorded as destination.updated.
func (s *Service) DeleteSecret(ctx context.Context, r Requester, publicID string, version *int64, name string) error {
	return s.inTx(ctx, publicID, func(q TxQueries, d dbgen.LockWebhookDestinationRow, now time.Time) error {
		if version != nil && *version != d.Version {
			return ErrVersionMismatch
		}
		n, err := q.DeleteSecret(ctx, dbgen.DeleteSecretParams{OrgID: s.orgID, DestinationID: d.ID, Name: name})
		if err != nil {
			return fmt.Errorf("delete the secret %s of %s: %w", name, d.PublicID, err)
		}
		if n == 0 {
			return ErrNoSecret
		}
		if _, err := q.BumpDestinationVersion(ctx, dbgen.BumpDestinationVersionParams{Now: now, OrgID: s.orgID,
			ID: d.ID}); err != nil {
			return fmt.Errorf("change the version of %s: %w", d.PublicID, err)
		}
		return s.record(ctx, q, r, ActionUpdated, d.PublicID, d.Name,
			[]audit.Change{{Pointer: "/secrets/" + name, SecretChanged: true}})
	})
}

// checkSecret refuses a name that is not a template identifier and an empty or too long value.
func checkSecret(name string, value logging.Secret) error {
	switch {
	case !ValidSecretName(name):
		return &FieldError{Pointer: "/path/secret_name", Code: CodeInvalidFormat,
			Detail: "A Secret name is a letter or an underscore followed by letters, digits and underscores."}
	case value == "":
		return &FieldError{Pointer: "/value", Code: CodeRequired, Detail: "The value is empty."}
	case len(value) > maxSecretLength:
		return &FieldError{Pointer: "/value", Code: CodeTooLong,
			Detail: fmt.Sprintf("The value is longer than %d bytes.", maxSecretLength)}
	}
	return nil
}

// inTx locks the outgoing webhook publicID in one transaction and runs f with it and the time of the change; a
// Destination of another type is ErrNotWebhook.
func (s *Service) inTx(ctx context.Context, publicID string,
	f func(q TxQueries, d dbgen.LockWebhookDestinationRow, now time.Time) error) error {
	id, err := publicid.Parse(publicid.Destination, publicID)
	if err != nil {
		return ErrNotFound
	}
	return s.cfg.Writer.InTx(ctx, func(q TxQueries) error {
		d, err := q.LockWebhookDestination(ctx, dbgen.LockWebhookDestinationParams{OrgID: s.orgID, PublicID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock the destination %s: %w", id, err)
		}
		if d.Type != TypeWebhook {
			return ErrNotWebhook
		}
		return f(q, d, s.cfg.Business.Now().UTC())
	})
}

// record writes the Audit log entry of a change of the Destination publicID and its live hint.
func (s *Service) record(ctx context.Context, q TxQueries, r Requester, action, publicID, name string,
	diff []audit.Change) error {
	if err := s.cfg.Audit.Record(ctx, q, audit.Entry{OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport,
		Action: action, Resource: audit.Resource{Type: ResourceDestination, PublicID: publicID, Name: name},
		Diff: diff, SourceAddress: r.Address}); err != nil {
		return err
	}
	return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: HintDestination, ID: publicID})
}

func timeOf(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	u := t.Time.UTC()
	return &u
}

func text(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

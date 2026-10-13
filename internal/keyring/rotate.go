// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package keyring

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/keyring/dbgen"
	"github.com/muster-io/muster/internal/logging"
)

// Channel is the LISTEN/NOTIFY channel of key activations: every replica switches to the active key when it hears
// it, and the Leader re-renders the open Root messages whose buttons another key signed.
const Channel = "muster_keyring"

// ActionKeyActivated is the Audit log action of `muster secrets rotate-key`, and ResourceKeyring its resource type.
const (
	ActionKeyActivated = "keyring.key_activated"
	ResourceKeyring    = "keyring"
)

// The live-update hints an activation sends: the organization hint, which the Keyring page follows, and the
// system-status hint. They are the HintOrganization and HintSystemStatus of internal/live, which imports this
// package through internal/organization; a test checks that they agree.
const (
	hintOrganization = "organization"
	hintSystemStatus = "system-status"
)

// ReplicasLackKeyError refuses an activation while live replicas do not hold the key (C-20.FR-7, ADR-0011).
type ReplicasLackKeyError struct {
	KeyID    string
	Replicas []string
}

func (e *ReplicasLackKeyError) Error() string {
	return fmt.Sprintf("Replicas %s do not hold key %s yet.", strings.Join(e.Replicas, ", "), e.KeyID)
}

// RowKey names a row of a table with an encrypted field: its id, the state hash of an OIDC redirect, or the
// Destination and name of a Destination Secret.
type RowKey struct {
	ID   int64
	Hash []byte
	Name string
}

// String is the row_key of the encrypted_values view.
func (r RowKey) String() string {
	switch {
	case r.Hash != nil:
		return hex.EncodeToString(r.Hash)
	case r.Name != "":
		return strconv.FormatInt(r.ID, 10) + "/" + r.Name
	}
	return strconv.FormatInt(r.ID, 10)
}

// EncryptedValue is one stored value of an encrypted field, with the key that encrypted it.
type EncryptedValue struct {
	Row        RowKey
	Ciphertext []byte
	KeyID      string
}

// RotationQueries are the queries of an activation, in its transaction, with the insert of the Audit log.
type RotationQueries interface {
	LockKeyringState(ctx context.Context) (string, error)
	ActivateKey(ctx context.Context, arg dbgen.ActivateKeyParams) (int64, error)
	ListLiveReplicas(ctx context.Context, liveSince time.Time) ([]dbgen.Replica, error)
	ListOrganizationIDs(ctx context.Context) ([]int64, error)
	CountOpenDeliveriesSignedByOtherKey(ctx context.Context, arg dbgen.CountOpenDeliveriesSignedByOtherKeyParams) (
		int64, error)
	NotifyKeyring(ctx context.Context, arg dbgen.NotifyKeyringParams) error
	// Notify sends a live-update hint, delivered when the transaction commits.
	Notify(ctx context.Context, h db.Hint) error
	// EncryptedValues lists, locked, the values of field in the Organization that a key other than keyID encrypted;
	// Reencrypt replaces one of them with ciphertext under keyID while it is still under previous, keeping the time
	// the field last changed, and returns the rows it changed.
	EncryptedValues(ctx context.Context, field string, orgID int64, keyID string) ([]EncryptedValue, error)
	Reencrypt(ctx context.Context, field string, orgID int64, row RowKey, ciphertext []byte, keyID, previous string) (
		int64, error)
	audit.Store
}

// RotationStore runs an activation in one transaction.
type RotationStore interface {
	InTx(ctx context.Context, f func(RotationQueries) error) error
}

// NewRotationStore is the RotationStore over the main pool.
func NewRotationStore(pool *pgxpool.Pool) RotationStore {
	return pgRotationStore{pool: pool}
}

type pgRotationStore struct {
	pool *pgxpool.Pool
}

func (s pgRotationStore) InTx(ctx context.Context, f func(RotationQueries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return f(pgRotationQueries{Queries: dbgen.New(tx), Store: audit.NewStore(tx), tx: tx})
	})
}

type pgRotationQueries struct {
	*dbgen.Queries
	audit.Store
	tx pgx.Tx
}

func (q pgRotationQueries) Notify(ctx context.Context, h db.Hint) error {
	return db.NotifyHint(ctx, q.tx, h)
}

func (q pgRotationQueries) EncryptedValues(ctx context.Context, field string, orgID int64, keyID string) (
	[]EncryptedValue, error) {
	f, ok := encryptedFields[field]
	if !ok {
		return nil, fmt.Errorf("%s is not an encrypted field", field)
	}
	return f.list(ctx, q.Queries, orgID, keyID)
}

func (q pgRotationQueries) Reencrypt(ctx context.Context, field string, orgID int64, row RowKey, ciphertext []byte,
	keyID, previous string) (int64, error) {
	f, ok := encryptedFields[field]
	if !ok {
		return 0, fmt.Errorf("%s is not an encrypted field", field)
	}
	return f.update(ctx, q.Queries, orgID, update{row: row, ciphertext: ciphertext, keyID: keyID, previous: previous})
}

// update is the re-encryption of one value.
type update struct {
	row        RowKey
	ciphertext []byte
	keyID      string
	previous   string
}

// encryptedField reads and re-encrypts one field of the encrypted_values view.
type encryptedField struct {
	list   func(ctx context.Context, q *dbgen.Queries, orgID int64, keyID string) ([]EncryptedValue, error)
	update func(ctx context.Context, q *dbgen.Queries, orgID int64, u update) (int64, error)
}

// values maps the rows of a list query to EncryptedValues.
func values[T any](rows []T, err error, f func(T) EncryptedValue) ([]EncryptedValue, error) {
	if err != nil {
		return nil, err
	}
	out := make([]EncryptedValue, len(rows))
	for i, r := range rows {
		out[i] = f(r)
	}
	return out, nil
}

// encryptedFields is the closed registry of the fields of the encrypted_values view, by the field name that is also
// the associated data of their ciphertexts; a test compares it with the view. Rotation walks it in rotationOrder.
var encryptedFields = map[string]encryptedField{
	"organizations.outgoing_heartbeat_url": {
		list: func(ctx context.Context, q *dbgen.Queries, org int64, key string) ([]EncryptedValue, error) {
			rows, err := q.ListRotateHeartbeatURL(ctx, dbgen.ListRotateHeartbeatURLParams{OrgID: org, KeyID: key})
			return values(rows, err, func(r dbgen.ListRotateHeartbeatURLRow) EncryptedValue {
				return EncryptedValue{Row: RowKey{ID: r.ID}, Ciphertext: r.Ciphertext, KeyID: r.KeyID}
			})
		},
		update: func(ctx context.Context, q *dbgen.Queries, org int64, u update) (int64, error) {
			return q.ReencryptHeartbeatURL(ctx, dbgen.ReencryptHeartbeatURLParams{OrgID: org,
				Ciphertext: u.ciphertext, KeyID: u.keyID, PreviousKeyID: u.previous})
		},
	},
	"organizations.outgoing_heartbeat_proxy_password": {
		list: func(ctx context.Context, q *dbgen.Queries, org int64, key string) ([]EncryptedValue, error) {
			rows, err := q.ListRotateHeartbeatProxyPassword(ctx, dbgen.ListRotateHeartbeatProxyPasswordParams{
				OrgID: org, KeyID: key})
			return values(rows, err, func(r dbgen.ListRotateHeartbeatProxyPasswordRow) EncryptedValue {
				return EncryptedValue{Row: RowKey{ID: r.ID}, Ciphertext: r.Ciphertext, KeyID: r.KeyID}
			})
		},
		update: func(ctx context.Context, q *dbgen.Queries, org int64, u update) (int64, error) {
			return q.ReencryptHeartbeatProxyPassword(ctx, dbgen.ReencryptHeartbeatProxyPasswordParams{OrgID: org,
				Ciphertext: u.ciphertext, KeyID: u.keyID, PreviousKeyID: u.previous})
		},
	},
	"oidc_settings.client_secret": {
		list: func(ctx context.Context, q *dbgen.Queries, org int64, key string) ([]EncryptedValue, error) {
			rows, err := q.ListRotateOIDCClientSecret(ctx, dbgen.ListRotateOIDCClientSecretParams{OrgID: org,
				KeyID: key})
			return values(rows, err, func(r dbgen.ListRotateOIDCClientSecretRow) EncryptedValue {
				return EncryptedValue{Row: RowKey{ID: r.OrgID}, Ciphertext: r.Ciphertext, KeyID: r.KeyID}
			})
		},
		update: func(ctx context.Context, q *dbgen.Queries, org int64, u update) (int64, error) {
			return q.ReencryptOIDCClientSecret(ctx, dbgen.ReencryptOIDCClientSecretParams{OrgID: org,
				Ciphertext: u.ciphertext, KeyID: u.keyID, PreviousKeyID: u.previous})
		},
	},
	"oidc_settings.proxy_password": {
		list: func(ctx context.Context, q *dbgen.Queries, org int64, key string) ([]EncryptedValue, error) {
			rows, err := q.ListRotateOIDCProxyPassword(ctx, dbgen.ListRotateOIDCProxyPasswordParams{OrgID: org,
				KeyID: key})
			return values(rows, err, func(r dbgen.ListRotateOIDCProxyPasswordRow) EncryptedValue {
				return EncryptedValue{Row: RowKey{ID: r.OrgID}, Ciphertext: r.Ciphertext, KeyID: r.KeyID}
			})
		},
		update: func(ctx context.Context, q *dbgen.Queries, org int64, u update) (int64, error) {
			return q.ReencryptOIDCProxyPassword(ctx, dbgen.ReencryptOIDCProxyPasswordParams{OrgID: org,
				Ciphertext: u.ciphertext, KeyID: u.keyID, PreviousKeyID: u.previous})
		},
	},
	"oidc_auth_requests.code_verifier": {
		list: func(ctx context.Context, q *dbgen.Queries, org int64, key string) ([]EncryptedValue, error) {
			rows, err := q.ListRotateOIDCCodeVerifier(ctx, dbgen.ListRotateOIDCCodeVerifierParams{OrgID: org,
				KeyID: key})
			return values(rows, err, func(r dbgen.ListRotateOIDCCodeVerifierRow) EncryptedValue {
				return EncryptedValue{Row: RowKey{Hash: r.StateHash}, Ciphertext: r.Ciphertext, KeyID: r.KeyID}
			})
		},
		update: func(ctx context.Context, q *dbgen.Queries, org int64, u update) (int64, error) {
			return q.ReencryptOIDCCodeVerifier(ctx, dbgen.ReencryptOIDCCodeVerifierParams{OrgID: org,
				StateHash: u.row.Hash, Ciphertext: u.ciphertext, KeyID: u.keyID, PreviousKeyID: u.previous})
		},
	},
	"users.oidc_offline_token": {
		list: func(ctx context.Context, q *dbgen.Queries, org int64, key string) ([]EncryptedValue, error) {
			rows, err := q.ListRotateOfflineToken(ctx, dbgen.ListRotateOfflineTokenParams{OrgID: org, KeyID: key})
			return values(rows, err, func(r dbgen.ListRotateOfflineTokenRow) EncryptedValue {
				return EncryptedValue{Row: RowKey{ID: r.ID}, Ciphertext: r.Ciphertext, KeyID: r.KeyID}
			})
		},
		update: func(ctx context.Context, q *dbgen.Queries, org int64, u update) (int64, error) {
			return q.ReencryptOfflineToken(ctx, dbgen.ReencryptOfflineTokenParams{OrgID: org, ID: u.row.ID,
				Ciphertext: u.ciphertext, KeyID: u.keyID, PreviousKeyID: u.previous})
		},
	},
	"user_totp.seed": {
		list: func(ctx context.Context, q *dbgen.Queries, org int64, key string) ([]EncryptedValue, error) {
			rows, err := q.ListRotateTOTPSeed(ctx, dbgen.ListRotateTOTPSeedParams{OrgID: org, KeyID: key})
			return values(rows, err, func(r dbgen.ListRotateTOTPSeedRow) EncryptedValue {
				return EncryptedValue{Row: RowKey{ID: r.UserID}, Ciphertext: r.Ciphertext, KeyID: r.KeyID}
			})
		},
		update: func(ctx context.Context, q *dbgen.Queries, org int64, u update) (int64, error) {
			return q.ReencryptTOTPSeed(ctx, dbgen.ReencryptTOTPSeedParams{OrgID: org, UserID: u.row.ID,
				Ciphertext: u.ciphertext, KeyID: u.keyID, PreviousKeyID: u.previous})
		},
	},
	"user_totp.pending_seed": {
		list: func(ctx context.Context, q *dbgen.Queries, org int64, key string) ([]EncryptedValue, error) {
			rows, err := q.ListRotateTOTPPendingSeed(ctx, dbgen.ListRotateTOTPPendingSeedParams{OrgID: org,
				KeyID: key})
			return values(rows, err, func(r dbgen.ListRotateTOTPPendingSeedRow) EncryptedValue {
				return EncryptedValue{Row: RowKey{ID: r.UserID}, Ciphertext: r.Ciphertext, KeyID: r.KeyID}
			})
		},
		update: func(ctx context.Context, q *dbgen.Queries, org int64, u update) (int64, error) {
			return q.ReencryptTOTPPendingSeed(ctx, dbgen.ReencryptTOTPPendingSeedParams{OrgID: org, UserID: u.row.ID,
				Ciphertext: u.ciphertext, KeyID: u.keyID, PreviousKeyID: u.previous})
		},
	},
	"connections.bot_token": {
		list: func(ctx context.Context, q *dbgen.Queries, org int64, key string) ([]EncryptedValue, error) {
			rows, err := q.ListRotateBotToken(ctx, dbgen.ListRotateBotTokenParams{OrgID: org, KeyID: key})
			return values(rows, err, func(r dbgen.ListRotateBotTokenRow) EncryptedValue {
				return EncryptedValue{Row: RowKey{ID: r.ID}, Ciphertext: r.Ciphertext, KeyID: r.KeyID}
			})
		},
		update: func(ctx context.Context, q *dbgen.Queries, org int64, u update) (int64, error) {
			return q.ReencryptBotToken(ctx, dbgen.ReencryptBotTokenParams{OrgID: org, ID: u.row.ID,
				Ciphertext: u.ciphertext, KeyID: u.keyID, PreviousKeyID: u.previous})
		},
	},
	"connections.proxy_password": {
		list: func(ctx context.Context, q *dbgen.Queries, org int64, key string) ([]EncryptedValue, error) {
			rows, err := q.ListRotateConnectionProxyPassword(ctx, dbgen.ListRotateConnectionProxyPasswordParams{
				OrgID: org, KeyID: key})
			return values(rows, err, func(r dbgen.ListRotateConnectionProxyPasswordRow) EncryptedValue {
				return EncryptedValue{Row: RowKey{ID: r.ID}, Ciphertext: r.Ciphertext, KeyID: r.KeyID}
			})
		},
		update: func(ctx context.Context, q *dbgen.Queries, org int64, u update) (int64, error) {
			return q.ReencryptConnectionProxyPassword(ctx, dbgen.ReencryptConnectionProxyPasswordParams{OrgID: org,
				ID: u.row.ID, Ciphertext: u.ciphertext, KeyID: u.keyID, PreviousKeyID: u.previous})
		},
	},
	"connections.telegram_webhook_secret": {
		list: func(ctx context.Context, q *dbgen.Queries, org int64, key string) ([]EncryptedValue, error) {
			rows, err := q.ListRotateTelegramWebhookSecret(ctx, dbgen.ListRotateTelegramWebhookSecretParams{
				OrgID: org, KeyID: key})
			return values(rows, err, func(r dbgen.ListRotateTelegramWebhookSecretRow) EncryptedValue {
				return EncryptedValue{Row: RowKey{ID: r.ID}, Ciphertext: r.Ciphertext, KeyID: r.KeyID}
			})
		},
		update: func(ctx context.Context, q *dbgen.Queries, org int64, u update) (int64, error) {
			return q.ReencryptTelegramWebhookSecret(ctx, dbgen.ReencryptTelegramWebhookSecretParams{OrgID: org,
				ID: u.row.ID, Ciphertext: u.ciphertext, KeyID: u.keyID, PreviousKeyID: u.previous})
		},
	},
	"destinations.proxy_password": {
		list: func(ctx context.Context, q *dbgen.Queries, org int64, key string) ([]EncryptedValue, error) {
			rows, err := q.ListRotateDestinationProxyPassword(ctx, dbgen.ListRotateDestinationProxyPasswordParams{
				OrgID: org, KeyID: key})
			return values(rows, err, func(r dbgen.ListRotateDestinationProxyPasswordRow) EncryptedValue {
				return EncryptedValue{Row: RowKey{ID: r.ID}, Ciphertext: r.Ciphertext, KeyID: r.KeyID}
			})
		},
		update: func(ctx context.Context, q *dbgen.Queries, org int64, u update) (int64, error) {
			return q.ReencryptDestinationProxyPassword(ctx, dbgen.ReencryptDestinationProxyPasswordParams{
				OrgID: org, ID: u.row.ID, Ciphertext: u.ciphertext, KeyID: u.keyID, PreviousKeyID: u.previous})
		},
	},
	"destinations.signing_secret": {
		list: func(ctx context.Context, q *dbgen.Queries, org int64, key string) ([]EncryptedValue, error) {
			rows, err := q.ListRotateSigningSecret(ctx, dbgen.ListRotateSigningSecretParams{OrgID: org, KeyID: key})
			return values(rows, err, func(r dbgen.ListRotateSigningSecretRow) EncryptedValue {
				return EncryptedValue{Row: RowKey{ID: r.ID}, Ciphertext: r.Ciphertext, KeyID: r.KeyID}
			})
		},
		update: func(ctx context.Context, q *dbgen.Queries, org int64, u update) (int64, error) {
			return q.ReencryptSigningSecret(ctx, dbgen.ReencryptSigningSecretParams{OrgID: org, ID: u.row.ID,
				Ciphertext: u.ciphertext, KeyID: u.keyID, PreviousKeyID: u.previous})
		},
	},
	"destinations.previous_signing_secret": {
		list: func(ctx context.Context, q *dbgen.Queries, org int64, key string) ([]EncryptedValue, error) {
			rows, err := q.ListRotatePreviousSigningSecret(ctx, dbgen.ListRotatePreviousSigningSecretParams{
				OrgID: org, KeyID: key})
			return values(rows, err, func(r dbgen.ListRotatePreviousSigningSecretRow) EncryptedValue {
				return EncryptedValue{Row: RowKey{ID: r.ID}, Ciphertext: r.Ciphertext, KeyID: r.KeyID}
			})
		},
		update: func(ctx context.Context, q *dbgen.Queries, org int64, u update) (int64, error) {
			return q.ReencryptPreviousSigningSecret(ctx, dbgen.ReencryptPreviousSigningSecretParams{OrgID: org,
				ID: u.row.ID, Ciphertext: u.ciphertext, KeyID: u.keyID, PreviousKeyID: u.previous})
		},
	},
	"destination_secrets.value": {
		list: func(ctx context.Context, q *dbgen.Queries, org int64, key string) ([]EncryptedValue, error) {
			rows, err := q.ListRotateDestinationSecret(ctx, dbgen.ListRotateDestinationSecretParams{OrgID: org,
				KeyID: key})
			return values(rows, err, func(r dbgen.ListRotateDestinationSecretRow) EncryptedValue {
				return EncryptedValue{Row: RowKey{ID: r.DestinationID, Name: r.Name}, Ciphertext: r.Ciphertext,
					KeyID: r.KeyID}
			})
		},
		update: func(ctx context.Context, q *dbgen.Queries, org int64, u update) (int64, error) {
			return q.ReencryptDestinationSecret(ctx, dbgen.ReencryptDestinationSecretParams{OrgID: org,
				DestinationID: u.row.ID, Name: u.row.Name, Ciphertext: u.ciphertext, KeyID: u.keyID,
				PreviousKeyID: u.previous})
		},
	},
}

// rotationOrder is the order rotation re-encrypts the fields in: that of the encrypted_values view, so that two
// rotations lock the rows of the tables in the same order.
var rotationOrder = []string{
	"organizations.outgoing_heartbeat_url", "organizations.outgoing_heartbeat_proxy_password",
	"oidc_settings.client_secret", "oidc_settings.proxy_password", "oidc_auth_requests.code_verifier",
	"users.oidc_offline_token", "user_totp.seed", "user_totp.pending_seed", "connections.bot_token",
	"connections.proxy_password", "connections.telegram_webhook_secret", "destinations.proxy_password",
	"destinations.signing_secret", "destinations.previous_signing_secret", "destination_secrets.value",
}

// EncryptedFields are the fields rotation re-encrypts, in its order: every field of the encrypted_values view.
func EncryptedFields() []string {
	return slices.Clone(rotationOrder)
}

// Rotation is `muster secrets rotate-key` (C-20.FR-7, ADR-0011): the activation of a key every live replica holds.
type Rotation struct {
	Store RotationStore
	// Keys is this process's Keyring: it decrypts with the key each value names and encrypts with the new key.
	Keys  *Keyring
	Audit *audit.Writer
	Log   *logging.Logger
	// Business dates the activation and its Audit entries; Real decides which replica records are live.
	Business clock.Clock
	Real     clock.Clock
}

// Rotated is what an activation did: the key, the key that was active before it, whether it was active already,
// how many encrypted values it re-encrypted and how many deliveries of open Alert Groups the Leader re-renders.
type Rotated struct {
	KeyID         string
	Previous      string
	AlreadyActive bool
	Reencrypted   int64
	ToResign      int64
}

// Activate makes the key keyID active. The caller holds the migration lock, so that a replica that starts meanwhile
// checks the canary after it. Refused, with ErrKeyNotHeld, when this process does not hold the key, and with a
// *ReplicasLackKeyError while a live replica lacks it. Otherwise, in one transaction: the active key and its time
// change and the key canary is encrypted with it; every value of every encrypted field that another key encrypted is
// decrypted with that key and encrypted with this one, keeping the time it last changed; keyring.key_activated is
// recorded per Organization with the actor cli, the name, the diff of /active_key_id and the counts; and the
// organization and system-status hints and the keyring notification are sent, delivered when it commits. A value that
// does not decrypt rolls everything back.
//
// A key that is active already changes nothing but values another key still encrypted, which a replica that had not
// switched yet may have written: they are re-encrypted, without an Audit entry.
func (r Rotation) Activate(ctx context.Context, keyID, actor string) (Rotated, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return Rotated{}, errors.New("the actor is required")
	}
	kk, ok := r.Keys.byID[keyID]
	if !ok {
		return Rotated{}, fmt.Errorf("%w: %s", ErrKeyNotHeld, keyID)
	}
	now := r.Business.Now().UTC()
	var out Rotated
	err := r.Store.InTx(ctx, func(q RotationQueries) error {
		out = Rotated{KeyID: keyID}
		previous, err := q.LockKeyringState(ctx)
		if err != nil {
			return fmt.Errorf("read the active key: %w", err)
		}
		out.Previous, out.AlreadyActive = previous, previous == keyID
		if !out.AlreadyActive {
			if err := r.activate(ctx, q, kk, previous, now); err != nil {
				return err
			}
		}
		orgs, err := q.ListOrganizationIDs(ctx)
		if err != nil {
			return fmt.Errorf("list the organizations: %w", err)
		}
		for _, org := range orgs {
			if err := r.organization(ctx, q, org, kk, actor, &out); err != nil {
				return err
			}
		}
		if err := q.NotifyKeyring(ctx, dbgen.NotifyKeyringParams{Channel: Channel, Payload: keyID}); err != nil {
			return fmt.Errorf("notify the replicas: %w", err)
		}
		return nil
	})
	if err != nil {
		return Rotated{}, err
	}
	if !out.AlreadyActive {
		r.Log.Log(ctx, logging.KeyActivated, logging.F("from", out.Previous), logging.F("to", keyID),
			logging.F("actor", actor))
	}
	return out, nil
}

// activate refuses while a live replica lacks the key, then makes it active with the canary encrypted with it.
func (r Rotation) activate(ctx context.Context, q RotationQueries, kk *key, previous string, now time.Time) error {
	replicas, err := q.ListLiveReplicas(ctx, r.Real.Now().Add(-LiveExpiry))
	if err != nil {
		return fmt.Errorf("list the live replicas: %w", err)
	}
	var lacking []string
	for _, rp := range replicas {
		if !slices.Contains(rp.KeyIds, kk.id) {
			lacking = append(lacking, rp.ReplicaID)
		}
	}
	if len(lacking) > 0 {
		slices.Sort(lacking)
		return &ReplicasLackKeyError{KeyID: kk.id, Replicas: lacking}
	}
	n, err := q.ActivateKey(ctx, dbgen.ActivateKeyParams{KeyID: kk.id, ActivatedAt: now,
		CanaryCiphertext: kk.seal(canaryField, canaryValue), PreviousKeyID: previous})
	if err != nil {
		return fmt.Errorf("activate key %s: %w", kk.id, err)
	}
	if n != 1 {
		return fmt.Errorf("activate key %s: the active key is no longer %s", kk.id, previous)
	}
	return nil
}

// organization re-encrypts the values of the Organization org, counts its deliveries to re-render, records the
// activation in its Audit log and sends its hints.
func (r Rotation) organization(ctx context.Context, q RotationQueries, org int64, kk *key, actor string,
	out *Rotated) error {
	var reencrypted int64
	for _, field := range rotationOrder {
		n, err := r.reencrypt(ctx, q, org, field, kk)
		if err != nil {
			return err
		}
		reencrypted += n
	}
	resign, err := q.CountOpenDeliveriesSignedByOtherKey(ctx, dbgen.CountOpenDeliveriesSignedByOtherKeyParams{
		OrgID: org, KeyID: kk.id})
	if err != nil {
		return fmt.Errorf("count the deliveries to re-render: %w", err)
	}
	out.Reencrypted += reencrypted
	out.ToResign += resign
	if out.AlreadyActive && reencrypted == 0 {
		return nil
	}
	if !out.AlreadyActive {
		if err := r.Audit.Record(ctx, q, audit.Entry{OrgID: org, Actor: audit.CLI(actor),
			Transport: audit.TransportCLI, Action: ActionKeyActivated,
			Resource: audit.Resource{Type: ResourceKeyring, Name: kk.id},
			Diff:     audit.Changed("/active_key_id", out.Previous, kk.id),
			Details:  map[string]any{"reencrypted": reencrypted, "open_deliveries_to_resign": resign}}); err != nil {
			return err
		}
	}
	for _, hint := range []string{hintOrganization, hintSystemStatus} {
		if err := q.Notify(ctx, db.Hint{OrgID: org, Type: hint}); err != nil {
			return err
		}
	}
	return nil
}

// reencrypt re-encrypts the values of one field of the Organization org that another key encrypted, with kk; an error
// names the field and the row, never a value.
func (r Rotation) reencrypt(ctx context.Context, q RotationQueries, org int64, field string, kk *key) (int64, error) {
	vs, err := q.EncryptedValues(ctx, field, org, kk.id)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", field, err)
	}
	var n int64
	for _, v := range vs {
		plain, err := r.Keys.Decrypt(field, v.KeyID, v.Ciphertext)
		if err != nil {
			return 0, fmt.Errorf("re-encrypt %s of row %s: %w", field, v.Row, err)
		}
		ct := kk.seal(field, plain)
		clear(plain)
		changed, err := q.Reencrypt(ctx, field, org, v.Row, ct, kk.id, v.KeyID)
		if err != nil {
			return 0, fmt.Errorf("re-encrypt %s of row %s: %w", field, v.Row, err)
		}
		n += changed
	}
	return n, nil
}

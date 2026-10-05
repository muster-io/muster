// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package keyring

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/keyring/dbgen"
	"github.com/muster-io/muster/internal/logging"
)

// Store is the keyring's part of the database: keyring_state and replicas. *dbgen.Queries implements it.
type Store interface {
	GetKeyringState(ctx context.Context) (dbgen.GetKeyringStateRow, error)
	CreateKeyringState(ctx context.Context, arg dbgen.CreateKeyringStateParams) (int64, error)
	GetActiveKeyID(ctx context.Context) (string, error)
	RecordReplica(ctx context.Context, arg dbgen.RecordReplicaParams) error
	DeleteReplica(ctx context.Context, replicaID string) error
	ListLiveReplicas(ctx context.Context, liveSince time.Time) ([]dbgen.Replica, error)
}

// NewStore is the Store over a pool, a connection or a transaction.
func NewStore(db dbgen.DBTX) Store {
	return dbgen.New(db)
}

// ErrKeyMismatch is the canary error: the Keyring cannot decrypt the key canary, or a running replica sees an active
// key it does not hold. The replica stops.
var ErrKeyMismatch = errors.New("master key does not match the database")

// The canary is a known value encrypted with the active key, bound to its own field name.
const canaryField = "keyring_state.canary"

var canaryValue = []byte("muster key canary")

// State is the row of keyring_state.
type State struct {
	ActiveKeyID string
	CanaryKeyID string
	Canary      []byte
}

// Establish returns the keyring state, writing it first on a new database: the first key of the Keyring becomes
// active and the canary is encrypted with it. The caller holds the migration lock, so replicas starting together
// write one canary. now is the business time of the activation.
func (k *Keyring) Establish(ctx context.Context, s Store, now time.Time) (State, error) {
	row, err := s.GetKeyringState(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		first := k.keys[0]
		if _, err := s.CreateKeyringState(ctx, dbgen.CreateKeyringStateParams{
			ActiveKeyID: first.id, ActivatedAt: now, CanaryCiphertext: first.seal(canaryField, canaryValue),
		}); err != nil {
			return State{}, fmt.Errorf("write the key canary: %w", err)
		}
		row, err = s.GetKeyringState(ctx)
	}
	if err != nil {
		return State{}, fmt.Errorf("read the key canary: %w", err)
	}
	return State{ActiveKeyID: row.ActiveKeyID, CanaryKeyID: row.CanaryKeyID, Canary: row.CanaryCiphertext}, nil
}

// Open decrypts the canary of st. On success the active key of st becomes the Keyring's and keyring_loaded is
// logged; otherwise key_canary_failed is logged and ErrKeyMismatch returned.
func (k *Keyring) Open(ctx context.Context, log *logging.Logger, st State) error {
	ok := st.CanaryKeyID == st.ActiveKeyID && k.Holds(st.ActiveKeyID)
	if ok {
		plain, err := k.Decrypt(canaryField, st.CanaryKeyID, st.Canary)
		ok = err == nil && string(plain) == string(canaryValue)
	}
	if !ok {
		log.Log(ctx, logging.KeyCanaryFailed, logging.F("active_key_id", st.ActiveKeyID),
			logging.F("key_ids", k.KeyIDs()), logging.F("error", ErrKeyMismatch.Error()))
		return ErrKeyMismatch
	}
	if err := k.setActive(st.ActiveKeyID); err != nil {
		return err
	}
	log.Log(ctx, logging.KeyringLoaded, logging.F("key_ids", k.KeyIDs()), logging.F("active_key_id", st.ActiveKeyID))
	return nil
}

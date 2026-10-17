// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package keyring

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/keyring/dbgen"
)

// KeyState is what the Keyring status says of a key (C-20.FR-6).
type KeyState string

// The states of a key, in the order they are decided: the active key; else a key a Secret or an open Root message
// still depends on; else a key that was active before and can be removed; else a key never active, such as one added
// for a rotation that has not run yet, which is activated, never removed.
const (
	KeyActive       KeyState = "active"
	KeyNeeded       KeyState = "needed"
	KeyRemovable    KeyState = "removable"
	KeyNotActiveYet KeyState = "not_active_yet"
)

// KeyStatus is one key of the Keyring status: the live replicas that hold it and those that lack it, by replica id,
// what still depends on it and its state.
type KeyStatus struct {
	ID        string
	Active    bool
	HeldBy    []string
	MissingOn []string
	Needed    bool
	Removable bool
	State     KeyState
	// EncryptedValues are the rows of encrypted_values the key encrypted, and OpenMessages the Root messages of open
	// Alert Groups, not deleted in the messenger, retired or withheld, whose buttons it signed, the older ones a
	// republication past a bridge's edit window replaced included.
	EncryptedValues int64
	OpenMessages    int64
}

// Status is the Keyring status: the active key and every key held by this process or a live replica, the active key
// first, then by id.
type Status struct {
	ActiveKeyID string
	Keys        []KeyStatus
}

// Key is the status of the key id, false when the status does not list it.
func (s Status) Key(id string) (KeyStatus, bool) {
	i := slices.IndexFunc(s.Keys, func(k KeyStatus) bool { return k.ID == id })
	if i < 0 {
		return KeyStatus{}, false
	}
	return s.Keys[i], true
}

// StatusQueries are the reads of the Keyring status; *dbgen.Queries implements them.
type StatusQueries interface {
	GetActiveKeyID(ctx context.Context) (string, error)
	ListLiveReplicas(ctx context.Context, liveSince time.Time) ([]dbgen.Replica, error)
	ListOrganizationIDs(ctx context.Context) ([]int64, error)
	CountEncryptedValuesByKey(ctx context.Context, orgID int64) ([]dbgen.CountEncryptedValuesByKeyRow, error)
	CountOpenButtonKeys(ctx context.Context, orgID int64) ([]dbgen.CountOpenButtonKeysRow, error)
	ListActivatedKeys(ctx context.Context, orgID int64) ([]dbgen.ListActivatedKeysRow, error)
}

// NewStatusQueries are the StatusQueries over a pool, a connection or a transaction.
func NewStatusQueries(db dbgen.DBTX) StatusQueries {
	return dbgen.New(db)
}

// ReadStatus reads the Keyring status (C-20.FR-6) with held, the key ids of this process, at now on the real clock,
// against which replica records are live. Keys are installation-wide, so what depends on them is counted over every
// Organization.
//
// A key is needed when a row of encrypted_values carries its id or a delivery of an open Alert Group that is not
// deleted in the messenger, retired or withheld has it as actual_button_key_id or among superseded_button_key_ids, the
// keys of the older Root messages a republication past a bridge's edit window replaced, whose buttons still work
// (C-22.FR-9). The re-render after a rotation never re-signs a delivery deleted in the messenger, retired or withheld,
// so such a delivery must not keep a key needed; nor does it re-sign an older Root message, whose key stays needed
// until its Alert Group resolves. It was active before when it is the active key, when it is the before or after value
// of /active_key_id in a keyring.key_activated Audit entry, or when it is needed: only the active key encrypts or
// signs. It is removable when it was active before and is neither active nor needed.
func ReadStatus(ctx context.Context, q StatusQueries, held []string, now time.Time) (Status, error) {
	active, err := q.GetActiveKeyID(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("read the active key: %w", err)
	}
	replicas, err := q.ListLiveReplicas(ctx, now.Add(-LiveExpiry))
	if err != nil {
		return Status{}, fmt.Errorf("list the live replicas: %w", err)
	}
	orgs, err := q.ListOrganizationIDs(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("list the organizations: %w", err)
	}
	in := statusInput{active: active, held: held, replicas: replicas, encrypted: map[string]int64{},
		messages: map[string]int64{}, activated: map[string]bool{}}
	for _, org := range orgs {
		values, err := q.CountEncryptedValuesByKey(ctx, org)
		if err != nil {
			return Status{}, fmt.Errorf("count the encrypted values: %w", err)
		}
		for _, v := range values {
			if v.KeyID.Valid {
				in.encrypted[v.KeyID.String] += v.Encrypted
			}
		}
		messages, err := q.CountOpenButtonKeys(ctx, org)
		if err != nil {
			return Status{}, fmt.Errorf("count the open root messages by key: %w", err)
		}
		for _, m := range messages {
			in.messages[m.KeyID] += m.Messages
		}
		activations, err := q.ListActivatedKeys(ctx, org)
		if err != nil {
			return Status{}, fmt.Errorf("read the key activations: %w", err)
		}
		for _, a := range activations {
			in.activated[a.BeforeKeyID] = true
			in.activated[a.AfterKeyID] = true
		}
	}
	return in.derive(), nil
}

// statusInput is what the status is derived from.
type statusInput struct {
	active    string
	held      []string
	replicas  []dbgen.Replica
	encrypted map[string]int64
	messages  map[string]int64
	activated map[string]bool
}

// derive applies the rule of ReadStatus. The keys are those held by this process or a live replica, the active key,
// and a key something still needs although nobody holds it, which no replica can use: it is listed needed, held by
// none.
func (in statusInput) derive() Status {
	ids := slices.Concat(in.held, []string{in.active})
	for _, r := range in.replicas {
		ids = append(ids, r.KeyIds...)
	}
	for id, n := range in.encrypted {
		if n > 0 {
			ids = append(ids, id)
		}
	}
	for id, n := range in.messages {
		if n > 0 {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	ids = slices.DeleteFunc(ids, func(id string) bool { return id == "" })
	out := Status{ActiveKeyID: in.active, Keys: make([]KeyStatus, 0, len(ids))}
	for _, id := range ids {
		k := KeyStatus{ID: id, Active: id == in.active, HeldBy: []string{}, MissingOn: []string{},
			EncryptedValues: in.encrypted[id], OpenMessages: in.messages[id]}
		for _, r := range in.replicas {
			if slices.Contains(r.KeyIds, id) {
				k.HeldBy = append(k.HeldBy, r.ReplicaID)
			} else {
				k.MissingOn = append(k.MissingOn, r.ReplicaID)
			}
		}
		k.Needed = k.EncryptedValues > 0 || k.OpenMessages > 0
		activeBefore := k.Active || in.activated[id] || k.Needed
		k.Removable = activeBefore && !k.Active && !k.Needed
		switch {
		case k.Active:
			k.State = KeyActive
		case k.Needed:
			k.State = KeyNeeded
		case k.Removable:
			k.State = KeyRemovable
		default:
			k.State = KeyNotActiveYet
		}
		out.Keys = append(out.Keys, k)
	}
	slices.SortStableFunc(out.Keys, func(a, b KeyStatus) int {
		if a.Active != b.Active {
			if a.Active {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return out
}

// StatusReader reads the Keyring status of this process for the API.
type StatusReader struct {
	q    StatusQueries
	keys *Keyring
	real clock.Clock
}

// NewStatusReader is the StatusReader over q with the keys of k, at the time of the real clock.
func NewStatusReader(q StatusQueries, k *Keyring, realClock clock.Clock) *StatusReader {
	return &StatusReader{q: q, keys: k, real: realClock}
}

// Status reads the Keyring status.
func (r *StatusReader) Status(ctx context.Context) (Status, error) {
	return ReadStatus(ctx, r.q, r.keys.KeyIDs(), r.real.Now())
}

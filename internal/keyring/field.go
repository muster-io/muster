// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package keyring

import (
	"errors"
	"fmt"
	"time"

	"github.com/muster-io/muster/internal/logging"
)

// SecretInput is a Secret field of an update (C-03.FR-21): an omitted value keeps the stored one, null clears it, and
// a value replaces it.
type SecretInput struct {
	Given bool
	Null  bool
	Value logging.Secret
}

// Keep is the input of an omitted Secret field.
var Keep = SecretInput{}

// Clear is the input of a Secret field set to null.
var Clear = SecretInput{Given: true, Null: true}

// Replace is the input of a Secret field set to value.
func Replace(value logging.Secret) SecretInput {
	return SecretInput{Given: true, Value: value}
}

// StoredSecret is a Secret field as a row stores it: the ciphertext, the id of the key that wrote it and when it last
// changed. An empty ciphertext is a field that is not set.
type StoredSecret struct {
	Ciphertext []byte
	KeyID      string
	UpdatedAt  *time.Time
}

// Set reports whether the field holds a value.
func (s StoredSecret) Set() bool {
	return len(s.Ciphertext) > 0
}

// SecretStatus is what a read shows instead of a Secret: whether it is set and when it last changed.
type SecretStatus struct {
	Set       bool
	UpdatedAt *time.Time
}

// Status is the read of the field; the value never leaves the server.
func (s StoredSecret) Status() SecretStatus {
	if !s.Set() {
		return SecretStatus{UpdatedAt: s.UpdatedAt}
	}
	return SecretStatus{Set: true, UpdatedAt: s.UpdatedAt}
}

// ErrEmptySecret refuses an empty value: a Secret is cleared with null, never with "".
var ErrEmptySecret = errors.New("a secret value is empty; send null to clear it")

// ApplySecret applies in to the field as stored, encrypting a new value for field with the active key at now. It
// returns the field to store and whether it changed: keeping never changes it, clearing changes a field that is set,
// and a replacement always does, even with the same value, because only the ciphertext is known.
func (k *Keyring) ApplySecret(field string, stored StoredSecret, in SecretInput, now time.Time) (StoredSecret, bool,
	error) {
	switch {
	case !in.Given:
		return stored, false, nil
	case in.Null:
		if !stored.Set() {
			return stored, false, nil
		}
		t := now.UTC()
		return StoredSecret{UpdatedAt: &t}, true, nil
	case in.Value == "":
		return stored, false, ErrEmptySecret
	}
	ct, id, err := k.Encrypt(field, []byte(in.Value))
	if err != nil {
		return stored, false, fmt.Errorf("encrypt %s: %w", field, err)
	}
	t := now.UTC()
	return StoredSecret{Ciphertext: ct, KeyID: id, UpdatedAt: &t}, true, nil
}

// OpenSecret decrypts the field; a field that is not set is the empty Secret.
func (k *Keyring) OpenSecret(field string, s StoredSecret) (logging.Secret, error) {
	if !s.Set() {
		return "", nil
	}
	plain, err := k.Decrypt(field, s.KeyID, s.Ciphertext)
	if err != nil {
		return "", err
	}
	return logging.Secret(plain), nil
}

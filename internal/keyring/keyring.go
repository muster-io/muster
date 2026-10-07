// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package keyring holds the master keys of ADR-0011: the Keyring loaded through a key provider, each key's id derived
// from its material, the sub-keys derived per purpose, AES-256-GCM encryption of secret fields, the active key and the
// key canary in keyring_state, and the replica key records in replicas. Key material never leaves this package: it is
// not stored, logged or returned, and no error carries it; the Keyring keeps only the derived sub-keys, and the
// process memory stays as sensitive as the keys (ADR-0011).
package keyring

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
)

// KeySize is the size of a master key and of each sub-key, in bytes.
const KeySize = 32

// Purpose names a sub-key; material derived for one purpose never serves another.
type Purpose string

const (
	PurposeEncryption      Purpose = "encryption"
	PurposeButtonSignature Purpose = "button-signature"
	PurposeCSRF            Purpose = "csrf"
)

var purposes = []Purpose{PurposeEncryption, PurposeButtonSignature, PurposeCSRF}

const (
	keyIDPrefix = "k-"
	// keyIDBytes of the SHA-256 of the material make the id: 64 bits, in hex.
	keyIDBytes = 8
	// hkdfInfo is prefixed to the purpose in the HKDF info, which keeps Muster's sub-keys apart from any other use.
	hkdfInfo = "muster keyring v1 "
)

var (
	// ErrNoActiveKey is returned by Encrypt and Sign before the canary check has set the active key.
	ErrNoActiveKey = errors.New("the keyring has no active key yet")
	// ErrKeyNotHeld is wrapped when a ciphertext or a signature names a key that is not in the Keyring.
	ErrKeyNotHeld = errors.New("the key is not in the keyring")
	// ErrDecrypt is wrapped when a ciphertext does not open: another field, another key or altered bytes.
	ErrDecrypt = errors.New("cannot decrypt")
)

// Keyring is the master keys of the process, in the order of the provider, and the active key.
type Keyring struct {
	keys   []*key
	byID   map[string]*key
	active atomic.Pointer[key]
}

// key is one master key: its id and its sub-keys. The material itself is not kept.
type key struct {
	id   string
	aead cipher.AEAD
	macs map[Purpose][]byte
}

// New makes a Keyring of keys, each KeySize bytes; it derives the ids and sub-keys and clears the material. A key
// given twice is refused. With allowDevelopmentKey false, the published development key is refused.
func New(keys [][]byte, allowDevelopmentKey bool) (*Keyring, error) {
	defer func() {
		for _, m := range keys {
			clear(m)
		}
	}()
	if len(keys) == 0 {
		return nil, errEmpty
	}
	dev := developmentMaterial()
	defer clear(dev)
	k := &Keyring{byID: make(map[string]*key, len(keys))}
	for i, m := range keys {
		if len(m) != KeySize {
			return nil, fmt.Errorf("%s: key %d is not %d bytes; %s", SecretKeysVar, i+1, KeySize, generateHint)
		}
		if !allowDevelopmentKey && hmac.Equal(m, dev) {
			return nil, errDevelopmentKey
		}
		kk, err := derive(m)
		if err != nil {
			return nil, err
		}
		if j := slices.IndexFunc(k.keys, func(o *key) bool { return o.id == kk.id }); j >= 0 {
			return nil, fmt.Errorf("%s: key %d repeats key %d", SecretKeysVar, i+1, j+1)
		}
		k.keys = append(k.keys, kk)
		k.byID[kk.id] = kk
	}
	return k, nil
}

// KeyID is the id of a key: "k-" and the first 64 bits of the SHA-256 of its material, in hex. It is the same on
// every replica and after every restart, and does not reveal the material.
func KeyID(material []byte) string {
	sum := sha256.Sum256(material)
	return keyIDPrefix + hex.EncodeToString(sum[:keyIDBytes])
}

func derive(material []byte) (*key, error) {
	k := &key{id: KeyID(material), macs: map[Purpose][]byte{}}
	for _, p := range purposes {
		sub, err := hkdf.Key(sha256.New, material, nil, hkdfInfo+string(p), KeySize)
		if err != nil {
			return nil, fmt.Errorf("derive the %s sub-key of key %s: %w", p, k.id, err)
		}
		if p != PurposeEncryption {
			k.macs[p] = sub
			continue
		}
		block, err := aes.NewCipher(sub)
		clear(sub)
		if err != nil {
			return nil, fmt.Errorf("prepare the encryption of key %s: %w", k.id, err)
		}
		if k.aead, err = cipher.NewGCM(block); err != nil {
			return nil, fmt.Errorf("prepare the encryption of key %s: %w", k.id, err)
		}
	}
	return k, nil
}

// KeyIDs are the ids of the keys, in the order of the provider.
func (k *Keyring) KeyIDs() []string {
	ids := make([]string, len(k.keys))
	for i, kk := range k.keys {
		ids[i] = kk.id
	}
	return ids
}

// Holds reports whether the key with id is in the Keyring.
func (k *Keyring) Holds(id string) bool {
	_, ok := k.byID[id]
	return ok
}

// ActiveKeyID is the id of the active key, empty before the canary check.
func (k *Keyring) ActiveKeyID() string {
	if a := k.active.Load(); a != nil {
		return a.id
	}
	return ""
}

// setActive makes the key with id active; it must be held.
func (k *Keyring) setActive(id string) error {
	kk, ok := k.byID[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrKeyNotHeld, id)
	}
	k.active.Store(kk)
	return nil
}

// Encrypt encrypts a secret field with the active key: a random nonce and AES-256-GCM, with the field name bound as
// associated data, so that the ciphertext opens only for that field. It returns the ciphertext and the key id to
// store next to it.
func (k *Keyring) Encrypt(field string, plaintext []byte) (ciphertext []byte, keyID string, err error) {
	a := k.active.Load()
	if a == nil {
		return nil, "", ErrNoActiveKey
	}
	return a.seal(field, plaintext), a.id, nil
}

func (kk *key) seal(field string, plaintext []byte) []byte {
	nonce := make([]byte, kk.aead.NonceSize(), kk.aead.NonceSize()+len(plaintext)+kk.aead.Overhead())
	_, _ = rand.Read(nonce) // crypto/rand.Read never fails
	return kk.aead.Seal(nonce, nonce, plaintext, []byte(field))
}

// Decrypt opens a ciphertext of field written with the key keyID, which may be any key of the Keyring. The error
// names the field and the key id, never the plaintext.
func (k *Keyring) Decrypt(field, keyID string, ciphertext []byte) ([]byte, error) {
	kk, ok := k.byID[keyID]
	if !ok {
		return nil, fmt.Errorf("decrypt %s: %w: %s", field, ErrKeyNotHeld, keyID)
	}
	return kk.open(field, ciphertext)
}

func (kk *key) open(field string, ciphertext []byte) ([]byte, error) {
	n := kk.aead.NonceSize()
	if len(ciphertext) < n+kk.aead.Overhead() {
		return nil, fmt.Errorf("%w %s with key %s: the ciphertext is too short", ErrDecrypt, field, kk.id)
	}
	plaintext, err := kk.aead.Open(nil, ciphertext[:n], ciphertext[n:], []byte(field))
	if err != nil {
		return nil, fmt.Errorf("%w %s with key %s", ErrDecrypt, field, kk.id)
	}
	return plaintext, nil
}

// Sign returns an HMAC-SHA256 of msg with the active key's sub-key for p, which is a signature purpose, and the id of
// that key.
func (k *Keyring) Sign(p Purpose, msg []byte) (mac []byte, keyID string, err error) {
	a := k.active.Load()
	if a == nil {
		return nil, "", ErrNoActiveKey
	}
	mac, err = a.mac(p, msg)
	return mac, a.id, err
}

// Verify checks a signature made by Sign with the key keyID, which may be any key of the Keyring.
func (k *Keyring) Verify(p Purpose, keyID string, msg, mac []byte) (bool, error) {
	kk, ok := k.byID[keyID]
	if !ok {
		return false, fmt.Errorf("verify a %s signature: %w: %s", p, ErrKeyNotHeld, keyID)
	}
	want, err := kk.mac(p, msg)
	if err != nil {
		return false, err
	}
	return hmac.Equal(mac, want), nil
}

// MinSignaturePrefix is the shortest signature VerifyPrefix accepts: 128 bits.
const MinSignaturePrefix = 16

// VerifyPrefix checks a signature made by Sign with the key keyID, which may be any key of the Keyring, and cut to its
// first bytes, at least MinSignaturePrefix of them, as a button action id carries it.
func (k *Keyring) VerifyPrefix(p Purpose, keyID string, msg, prefix []byte) (bool, error) {
	if len(prefix) < MinSignaturePrefix {
		return false, fmt.Errorf("verify a %s signature: shorter than %d bytes", p, MinSignaturePrefix)
	}
	kk, ok := k.byID[keyID]
	if !ok {
		return false, fmt.Errorf("verify a %s signature: %w: %s", p, ErrKeyNotHeld, keyID)
	}
	want, err := kk.mac(p, msg)
	if err != nil {
		return false, err
	}
	return len(prefix) <= len(want) && hmac.Equal(prefix, want[:len(prefix)]), nil
}

func (kk *key) mac(p Purpose, msg []byte) ([]byte, error) {
	sub, ok := kk.macs[p]
	if !ok {
		return nil, fmt.Errorf("%q is not a signature purpose", p)
	}
	h := hmac.New(sha256.New, sub)
	h.Write(msg)
	return h.Sum(nil), nil
}

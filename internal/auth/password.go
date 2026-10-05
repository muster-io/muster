// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// PasswordMinLength is auth.password_min_length, in characters.
const PasswordMinLength = 12

// The argon2id parameters of new hashes: the OWASP minimum of 19 MiB, two passes and one lane. A stored hash carries
// its own parameters, so verifying an older hash works after they change.
const (
	argonMemoryKiB = 19 * 1024
	argonTime      = 2
	argonThreads   = 1
	argonSaltLen   = 16
	argonKeyLen    = 32
	// argonConcurrency bounds the hashes computed at once, so that a burst of sign-ins cannot take more than
	// argonConcurrency × 19 MiB of memory.
	argonConcurrency = 2
	// maxArgonMemoryKiB and maxArgonTime bound the parameters accepted from a stored hash.
	maxArgonMemoryKiB = 256 * 1024
	maxArgonTime      = 16
)

var (
	// ErrPasswordTooShort is a password shorter than PasswordMinLength characters.
	ErrPasswordTooShort = fmt.Errorf("the password is shorter than %d characters", PasswordMinLength)
	errMalformedHash    = errors.New("the password hash is not an argon2id PHC string")

	hashSlots = make(chan struct{}, argonConcurrency)

	dummyOnce sync.Once
	dummyHash string
)

// CheckPasswordLength refuses a password shorter than PasswordMinLength characters.
func CheckPasswordLength(password string) error {
	if utf8.RuneCountInString(password) < PasswordMinLength {
		return ErrPasswordTooShort
	}
	return nil
}

// HashPassword returns the argon2id PHC string of password with a random salt:
// $argon2id$v=19$m=<KiB>,t=<passes>,p=<lanes>$<salt>$<key>, salt and key in unpadded standard base64.
func HashPassword(ctx context.Context, password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("read a salt: %w", err)
	}
	key, err := derive(ctx, password, salt, argonMemoryKiB, argonTime, argonThreads, argonKeyLen)
	if err != nil {
		return "", err
	}
	return encodeHash(salt, key), nil
}

func encodeHash(salt, key []byte) string {
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemoryKiB, argonTime,
		argonThreads, enc.EncodeToString(salt), enc.EncodeToString(key))
}

// VerifyPassword reports whether password matches the PHC string encoded, in constant time for the key; a string
// that is not an argon2id hash is an error, and so is ctx ending while the check waits for its turn.
func VerifyPassword(ctx context.Context, encoded, password string) (bool, error) {
	p, err := parseHash(encoded)
	if err != nil {
		return false, err
	}
	key, err := derive(ctx, password, p.salt, p.memory, p.time, p.threads, uint32(len(p.key))) //nolint:gosec // G115: bounded by parseHash
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(key, p.key) == 1, nil
}

// prepareDummy computes the hash that verifyDummy checks against, once per process, so that even the first unknown
// login takes no longer than a wrong password.
func prepareDummy() {
	dummyOnce.Do(func() {
		salt := []byte(rand.Text())[:argonSaltLen]
		dummyHash = encodeHash(salt, argon2.IDKey([]byte(rand.Text()), salt, argonTime, argonMemoryKiB, argonThreads,
			argonKeyLen))
	})
}

// verifyDummy spends the time of a verification on a hash no password matches, so that the answer for an unknown or
// unusable account takes as long as for a wrong password.
func verifyDummy(ctx context.Context, password string) error {
	prepareDummy()
	_, err := VerifyPassword(ctx, dummyHash, password)
	return err
}

// derive computes an argon2id key once a hashing slot is free; it gives up when ctx ends first, so that requests whose
// clients went away do not queue up behind a burst of sign-ins.
func derive(ctx context.Context, password string, salt []byte, memory, passes uint32, threads uint8,
	keyLen uint32) ([]byte, error) {
	select {
	case hashSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-hashSlots }()
	return argon2.IDKey([]byte(password), salt, passes, memory, threads, keyLen), nil
}

type phc struct {
	memory, time uint32
	threads      uint8
	salt, key    []byte
}

func parseHash(encoded string) (phc, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return phc{}, errMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return phc{}, errMalformedHash
	}
	var p phc
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); err != nil {
		return phc{}, errMalformedHash
	}
	if p.memory == 0 || p.memory > maxArgonMemoryKiB || p.time == 0 || p.time > maxArgonTime || p.threads == 0 {
		return phc{}, errMalformedHash
	}
	var err error
	if p.salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil || len(p.salt) < 8 {
		return phc{}, errMalformedHash
	}
	if p.key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil || len(p.key) < 16 || len(p.key) > 64 {
		return phc{}, errMalformedHash
	}
	return p, nil
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package keyring

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/muster-io/muster/internal/logging"
)

// Provider returns the master keys, KeySize bytes each, in their order: on a new database the first one becomes
// active. Env reads the environment; an external key service would be another Provider (ADR-0011).
type Provider interface {
	MasterKeys(ctx context.Context) ([][]byte, error)
}

// The variables of the master keys.
const (
	SecretKeysVar     = "MUSTER_SECRET_KEYS"
	SecretKeysFileVar = "MUSTER_SECRET_KEYS_FILE"
)

// DevelopmentKey is the published master key of `muster dev`, the base64 of "muster-dev-only-key-not-a-secret".
// Development mode accepts it; the server command refuses it outside development mode.
const DevelopmentKey = "bXVzdGVyLWRldi1vbmx5LWtleS1ub3QtYS1zZWNyZXQ="

// Placeholder is the value of MUSTER_SECRET_KEYS in the compose example, refused until it is replaced.
const Placeholder = "REPLACE-ME-with-openssl-rand-base64-32"

const generateHint = "generate a key with `openssl rand -base64 32`"

var (
	errEmpty          = errors.New(SecretKeysVar + " holds no key: " + generateHint)
	errDevelopmentKey = errors.New(SecretKeysVar + " holds the published development key, which only muster dev " +
		"accepts: " + generateHint)
)

func developmentMaterial() []byte {
	m, _ := base64.StdEncoding.DecodeString(DevelopmentKey) // a constant that decodes
	return m
}

// Env is the key provider of the environment: MUSTER_SECRET_KEYS holds base64 keys separated by commas, and the file
// that MUSTER_SECRET_KEYS_FILE names holds one key per line. Errors name the variable and the position of a key in
// the list, never a value.
type Env struct {
	// Keys is the value of the variable, or the content of the file.
	Keys logging.Secret
	// Source is the variable that was set: SecretKeysVar or SecretKeysFileVar; empty when neither was.
	Source string
}

func (e Env) MasterKeys(context.Context) ([][]byte, error) {
	var entries []string
	switch e.Source {
	case "":
		return nil, errors.New(SecretKeysVar + " is not set: " + generateHint)
	case SecretKeysFileVar:
		for line := range strings.Lines(string(e.Keys)) {
			if line = strings.TrimSpace(line); line != "" {
				entries = append(entries, line)
			}
		}
		if len(entries) == 0 {
			return nil, errors.New(SecretKeysFileVar + " names a file without a key: " + generateHint)
		}
	default:
		if strings.TrimSpace(string(e.Keys)) == "" {
			return nil, errors.New(SecretKeysVar + " is empty: " + generateHint)
		}
		entries = strings.Split(string(e.Keys), ",")
	}
	holder := e.Source
	if e.Source == SecretKeysFileVar {
		holder = "the file named by " + SecretKeysFileVar
	}
	keys := make([][]byte, 0, len(entries))
	for i, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == Placeholder {
			return nil, fmt.Errorf("%s still holds the placeholder of the compose example: %s", holder, generateHint)
		}
		m, err := base64.StdEncoding.DecodeString(entry)
		if err != nil || len(m) != KeySize {
			clear(m)
			return nil, fmt.Errorf("%s: key %d is not a base64-encoded key of %d bytes: %s", e.Source, i+1, KeySize,
				generateHint)
		}
		keys = append(keys, m)
	}
	return keys, nil
}

// Load makes the Keyring from the keys of p; see New for allowDevelopmentKey.
func Load(ctx context.Context, p Provider, allowDevelopmentKey bool) (*Keyring, error) {
	keys, err := p.MasterKeys(ctx)
	if err != nil {
		return nil, err
	}
	return New(keys, allowDevelopmentKey)
}

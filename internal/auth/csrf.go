// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package auth

import (
	"encoding/base64"
	"strings"

	"github.com/muster-io/muster/internal/keyring"
)

// CSRFHeader carries the CSRF token on every mutating request made with the session cookie.
const CSRFHeader = "X-CSRF-Token"

// csrfToken derives the CSRF token of a session from its cookie value: the id of the active key, a dot and the
// HMAC-SHA256 of the value with that key's csrf sub-key (ADR-0011), in unpadded base64url. Nothing is stored: the
// token is derived again on each request, and the key id lets a token made before a key rotation still verify.
func csrfToken(k *keyring.Keyring, sessionToken []byte) (string, error) {
	mac, keyID, err := k.Sign(keyring.PurposeCSRF, sessionToken)
	if err != nil {
		return "", err
	}
	return keyID + "." + base64.RawURLEncoding.EncodeToString(mac), nil
}

// checkCSRF reports whether header is the CSRF token of the session with the cookie value sessionToken.
func checkCSRF(k *keyring.Keyring, sessionToken []byte, header string) bool {
	keyID, enc, ok := strings.Cut(header, ".")
	if !ok || !k.Holds(keyID) {
		return false
	}
	mac, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return false
	}
	valid, err := k.Verify(keyring.PurposeCSRF, keyID, sessionToken, mac)
	return err == nil && valid
}

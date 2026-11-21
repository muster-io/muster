// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package mattermost

import (
	"crypto/sha256"
	"encoding/base64"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/keyring"
)

// The callback secret of a button authenticates a press as one the Mattermost server made (C-13.FR-4). Mattermost does
// not sign its requests to the integration URL, and the signed action id alone does not tell them apart from a
// forgery: the same action id is in the Telegram, Slack and bridge messages of the Alert Group. The secret is an
// HMAC-SHA256 with the mattermost-callback sub-key of the key that signed the action id, over the Connection's
// public_id and the action id, so that it is valid for that button of that Connection only. It is derived, never
// stored: the adapter puts it in the button's context when it lays out a post, the Mattermost server strips that
// context from the posts it sends to clients and returns it with a press, and the callback checks it, in constant
// time, before anything acts on the press. A rotation re-signs the buttons of open Root messages and so changes their
// secrets; a key that left the Keyring verifies neither its action ids nor their secrets.

// callbackDomain separates the message of a callback secret from any other HMAC of the same sub-key.
const callbackDomain = "muster mattermost callback v1\x00"

// SecretKeys derive and verify the callback secrets of buttons: the Keyring.
type SecretKeys interface {
	SignWith(p keyring.Purpose, keyID string, msg []byte) ([]byte, error)
	Verify(p keyring.Purpose, keyID string, msg, mac []byte) (bool, error)
}

// Keys verify the signed action ids of presses and their callback secrets: the Keyring.
type Keys interface {
	buttons.Keys
	SecretKeys
}

var _ Keys = (*keyring.Keyring)(nil)

func callbackMessage(connection, action string) []byte {
	return []byte(callbackDomain + connection + "\x00" + action)
}

// CallbackSecret is the callback secret of the button whose action id the key keyID signed in a post of the Connection
// whose public_id is connection, in base64url, as the adapter lays it out.
func CallbackSecret(k SecretKeys, keyID, connection, action string) (string, error) {
	mac, err := k.SignWith(keyring.PurposeMattermostCallback, keyID, callbackMessage(connection, action))
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(mac), nil
}

// verifySecret reports whether secret is the callback secret of the button action signed by the key keyID in a post of
// the Connection connection, compared in constant time; a key that is not in the Keyring verifies nothing.
func verifySecret(k SecretKeys, keyID, connection, action, secret string) bool {
	mac, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil || len(mac) != sha256.Size || keyID == "" || action == "" {
		return false
	}
	ok, err := k.Verify(keyring.PurposeMattermostCallback, keyID, callbackMessage(connection, action), mac)
	return err == nil && ok
}

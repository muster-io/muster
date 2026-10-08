// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package buttons holds the Command buttons of messages (C-12.FR-1, reference.md "Buttons and links by status") and
// their signed action ids (ADR-0011). An action id is opaque: a version, the kind of its subject, the subject's
// public_id, the Command and its argument, the first bytes of the id of the key that signed it, and an HMAC-SHA256 with
// the button-signature sub-key of that key truncated to 128 bits — at most 64 bytes in its compact base64url form, so
// that Telegram's callback_data holds it. Verification finds the key in the Keyring by its id, which may be any key
// of the Keyring, and compares the HMAC in constant time.
package buttons

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/muster-io/muster/internal/keyring"
)

// The Commands a button carries.
const (
	CommandAcknowledge   = "acknowledge"
	CommandUnacknowledge = "unacknowledge"
	CommandResolve       = "resolve"
	CommandSnooze        = "snooze"
	CommandUnsnooze      = "unsnooze"
	CommandStillOnIt     = "still_on_it"
)

// commandCodes are the one-byte codes of the Commands in an action id.
var commandCodes = map[string]byte{CommandAcknowledge: 'a', CommandUnacknowledge: 'u', CommandResolve: 'r',
	CommandSnooze: 's', CommandUnsnooze: 'n', CommandStillOnIt: 'k'}

// Subject is what a button is about: the Root message of an Alert Group, a Thread reply of one, or the test message
// of a Destination.
type Subject string

// The subjects of action ids.
const (
	SubjectRoot  Subject = "root"
	SubjectReply Subject = "reply"
	SubjectTest  Subject = "test"
)

var subjectCodes = map[Subject]byte{SubjectRoot: 'r', SubjectReply: 'p', SubjectTest: 't'}

// Button is a button before it is signed: its Command and the argument, the index of a Snooze duration.
type Button struct {
	Command  string
	Argument int
}

// ForStatus are the buttons of a Root message in a status (reference.md): firing — Ack, Resolve, Snooze;
// acknowledged — Unack, Resolve, Snooze; snoozed — Ack, Unsnooze, Resolve; resolved — none. Snooze is one button per
// duration of route.snooze_durations.
func ForStatus(status string, snoozeDurations int) []Button {
	snooze := make([]Button, 0, snoozeDurations)
	for i := range min(snoozeDurations, maxArgument+1) {
		snooze = append(snooze, Button{Command: CommandSnooze, Argument: i})
	}
	switch status {
	case "firing":
		return append([]Button{{Command: CommandAcknowledge}, {Command: CommandResolve}}, snooze...)
	case "acknowledged":
		return append([]Button{{Command: CommandUnacknowledge}, {Command: CommandResolve}}, snooze...)
	case "snoozed":
		return []Button{{Command: CommandAcknowledge}, {Command: CommandUnsnooze}, {Command: CommandResolve}}
	}
	return []Button{}
}

// Action is what an action id says.
type Action struct {
	Subject  Subject
	PublicID string
	Command  string
	Argument int
}

// The layout of an action id: version, subject, public_id, Command, argument, key id prefix, MAC.
const (
	version       = 1
	publicIDLen   = 14
	keyPrefixLen  = 4
	macLen        = keyring.MinSignaturePrefix
	signedLen     = 1 + 1 + publicIDLen + 1 + 1 + keyPrefixLen
	actionLen     = signedLen + macLen
	maxArgument   = 255
	keyIDHexStart = len("k-")
)

// MaxLen is the longest action id in its compact form: Telegram's callback_data takes 64 bytes.
const MaxLen = 64

// Keys are the keys that sign and verify action ids: the Keyring.
type Keys interface {
	Sign(p keyring.Purpose, msg []byte) (mac []byte, keyID string, err error)
	VerifyPrefix(p keyring.Purpose, keyID string, msg, prefix []byte) (bool, error)
	KeyIDs() []string
}

var _ Keys = (*keyring.Keyring)(nil)

// ErrInvalid is an action id that is malformed, names no key of the Keyring or whose signature does not match.
var ErrInvalid = errors.New("the action id is not valid")

// Sign is the action id of a, signed with the active key, and the full id of that key, which Mattermost receives as
// context.key_id and deliveries.desired_button_key_id records.
func Sign(k Keys, a Action) (id, keyID string, err error) {
	msg, err := encode(a)
	if err != nil {
		return "", "", err
	}
	// The key id prefix is part of what is signed; the active key is known only once Sign returns, so the message is
	// signed after its prefix is filled from the key Sign reports, and signed again if the active key changed.
	for range 3 {
		mac, kid, err := k.Sign(keyring.PurposeButtonSignature, msg)
		if err != nil {
			return "", "", fmt.Errorf("sign the action id: %w", err)
		}
		prefix, err := keyPrefix(kid)
		if err != nil {
			return "", "", err
		}
		if string(msg[signedLen-keyPrefixLen:]) == string(prefix) {
			return base64.RawURLEncoding.EncodeToString(append(msg, mac[:macLen]...)), kid, nil
		}
		copy(msg[signedLen-keyPrefixLen:], prefix)
	}
	return "", "", errors.New("sign the action id: the active key changed while signing")
}

// encode is the signed part of an action id, with an empty key id prefix.
func encode(a Action) ([]byte, error) {
	subject, ok := subjectCodes[a.Subject]
	if !ok {
		return nil, fmt.Errorf("an action id about %q", a.Subject)
	}
	command, ok := commandCodes[a.Command]
	if !ok {
		return nil, fmt.Errorf("an action id of the command %q", a.Command)
	}
	if len(a.PublicID) != publicIDLen {
		return nil, fmt.Errorf("an action id about %q, which is not a public_id", a.PublicID)
	}
	if a.Argument < 0 || a.Argument > maxArgument {
		return nil, fmt.Errorf("an action id with the argument %d", a.Argument)
	}
	msg := make([]byte, 0, actionLen)
	msg = append(msg, version, subject)
	msg = append(msg, a.PublicID...)
	msg = append(msg, command, byte(a.Argument))
	return append(msg, make([]byte, keyPrefixLen)...), nil
}

// keyPrefix is the first bytes of a key id: its hex digits after "k-", decoded.
func keyPrefix(keyID string) ([]byte, error) {
	if len(keyID) < keyIDHexStart+2*keyPrefixLen {
		return nil, fmt.Errorf("the key id %q is too short", keyID)
	}
	b, err := hex.DecodeString(keyID[keyIDHexStart : keyIDHexStart+2*keyPrefixLen])
	if err != nil {
		return nil, fmt.Errorf("the key id %q is not hex: %w", keyID, err)
	}
	return b, nil
}

// Verify reads an action id and checks its signature with the key of the Keyring its prefix names; keyID, when
// given, is the full key id Mattermost returns, which must agree with the prefix. Anything else is ErrInvalid.
func Verify(k Keys, id, keyID string) (Action, error) {
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil || len(raw) != actionLen || raw[0] != version {
		return Action{}, ErrInvalid
	}
	msg, mac := raw[:signedLen], raw[signedLen:]
	a := Action{PublicID: string(msg[2 : 2+publicIDLen]), Argument: int(msg[signedLen-keyPrefixLen-1])}
	for s, c := range subjectCodes {
		if c == msg[1] {
			a.Subject = s
		}
	}
	for cmd, c := range commandCodes {
		if c == msg[2+publicIDLen] {
			a.Command = cmd
		}
	}
	if a.Subject == "" || a.Command == "" {
		return Action{}, ErrInvalid
	}
	prefix := hex.EncodeToString(msg[signedLen-keyPrefixLen:])
	candidates := slices.DeleteFunc(k.KeyIDs(), func(kid string) bool {
		return !strings.HasPrefix(kid[min(keyIDHexStart, len(kid)):], prefix) || (keyID != "" && kid != keyID)
	})
	for _, kid := range candidates {
		if ok, err := k.VerifyPrefix(keyring.PurposeButtonSignature, kid, msg, mac); err == nil && ok {
			return a, nil
		}
	}
	return Action{}, ErrInvalid
}

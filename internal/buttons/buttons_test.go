// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package buttons_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/keyring/dbgen"
	"github.com/muster-io/muster/internal/logging"
)

// stateStore keeps keyring_state in memory.
type stateStore struct {
	keyring.Store
	row *dbgen.GetKeyringStateRow
}

func (s *stateStore) GetKeyringState(context.Context) (dbgen.GetKeyringStateRow, error) {
	if s.row == nil {
		return dbgen.GetKeyringStateRow{}, pgx.ErrNoRows
	}
	return *s.row, nil
}

func (s *stateStore) CreateKeyringState(_ context.Context, p dbgen.CreateKeyringStateParams) (int64, error) {
	s.row = &dbgen.GetKeyringStateRow{ActiveKeyID: p.ActiveKeyID, CanaryKeyID: p.ActiveKeyID,
		CanaryCiphertext: p.CanaryCiphertext}
	return 1, nil
}

// active is a Keyring of the materials whose first key is active, as after the canary check.
func active(t *testing.T, materials ...byte) *keyring.Keyring {
	t.Helper()
	var keys [][]byte
	for _, m := range materials {
		keys = append(keys, bytes.Repeat([]byte{m}, keyring.KeySize))
	}
	k, err := keyring.New(keys, false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := k.Establish(t.Context(), &stateStore{}, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Open(t.Context(), logging.New(io.Discard, logging.LevelInfo), st); err != nil {
		t.Fatal(err)
	}
	return k
}

// TestActionIDs: an action id names its subject by public_id, its Command and argument, fits in 64 bytes, verifies
// with any key of the Keyring and refuses any changed byte, an unknown key and a wrong full key id.
func TestActionIDs(t *testing.T) {
	k := active(t, 'a', 'b')
	a := buttons.Action{Subject: buttons.SubjectRoot, PublicID: "AGK7M3QX9P2RTA", Command: buttons.CommandSnooze,
		Argument: 2}
	id, keyID, err := buttons.Sign(k, a)
	if err != nil {
		t.Fatal(err)
	}
	if len(id) > buttons.MaxLen || keyID != k.ActiveKeyID() {
		t.Errorf("id %q (%d bytes), key %s", id, len(id), keyID)
	}
	if got, err := buttons.Verify(k, id, ""); err != nil || got != a {
		t.Errorf("Verify = %+v, %v", got, err)
	}
	if got, err := buttons.Verify(k, id, keyID); err != nil || got != a {
		t.Errorf("Verify with the key id = %+v, %v", got, err)
	}
	if _, err := buttons.Verify(k, id, k.KeyIDs()[1]); !errors.Is(err, buttons.ErrInvalid) {
		t.Errorf("Verify with another key id = %v", err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(id)
	for i := range raw {
		changed := bytes.Clone(raw)
		changed[i] ^= 0x01
		if _, err := buttons.Verify(k, base64.RawURLEncoding.EncodeToString(changed), ""); !errors.Is(err,
			buttons.ErrInvalid) {
			t.Errorf("byte %d changed: %v", i, err)
		}
	}
	for _, bad := range []string{"", "!!", base64.RawURLEncoding.EncodeToString(raw[:10]), id + "AA"} {
		if _, err := buttons.Verify(k, bad, ""); !errors.Is(err, buttons.ErrInvalid) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// An id signed by a key the Keyring no longer holds does not verify; one signed by a key it still holds does.
	if _, err := buttons.Verify(active(t, 'c'), id, ""); !errors.Is(err, buttons.ErrInvalid) {
		t.Errorf("unknown key: %v", err)
	}
	if _, err := buttons.Verify(active(t, 'b', 'a'), id, ""); err != nil {
		t.Errorf("an older key of the Keyring: %v", err)
	}
	for _, s := range []buttons.Subject{buttons.SubjectReply, buttons.SubjectTest} {
		for _, c := range []string{buttons.CommandAcknowledge, buttons.CommandUnacknowledge, buttons.CommandResolve,
			buttons.CommandUnsnooze, buttons.CommandStillOnIt} {
			b := buttons.Action{Subject: s, PublicID: "DS4M1T6RW9KXPA", Command: c}
			id, _, err := buttons.Sign(k, b)
			if got, verr := buttons.Verify(k, id, ""); err != nil || verr != nil || got != b {
				t.Errorf("%s %s: %v %v", s, c, err, verr)
			}
		}
	}
	for _, bad := range []buttons.Action{
		{Subject: "other", PublicID: a.PublicID, Command: a.Command},
		{Subject: buttons.SubjectRoot, PublicID: "AG1", Command: a.Command},
		{Subject: buttons.SubjectRoot, PublicID: a.PublicID, Command: "delete"},
		{Subject: buttons.SubjectRoot, PublicID: a.PublicID, Command: a.Command, Argument: 256},
	} {
		if _, _, err := buttons.Sign(k, bad); err == nil {
			t.Errorf("%+v signed", bad)
		}
	}
	unopened, _ := keyring.New([][]byte{bytes.Repeat([]byte{'a'}, keyring.KeySize)}, false)
	if _, _, err := buttons.Sign(unopened, a); !errors.Is(err, keyring.ErrNoActiveKey) {
		t.Errorf("no active key: %v", err)
	}
	if _, _, err := buttons.Sign(badKeys{"x"}, a); err == nil {
		t.Error("a short key id")
	}
	if _, _, err := buttons.Sign(badKeys{"k-zzzzzzzzzz"}, a); err == nil {
		t.Error("a key id that is not hex")
	}
	if _, _, err := buttons.Sign(&rotating{}, a); err == nil {
		t.Error("an active key that keeps changing")
	}
}

// badKeys sign with a malformed key id.
type badKeys struct{ id string }

func (b badKeys) Sign(keyring.Purpose, []byte) ([]byte, string, error) {
	return make([]byte, 32), b.id, nil
}
func (badKeys) VerifyPrefix(keyring.Purpose, string, []byte, []byte) (bool, error) {
	return false, nil
}
func (b badKeys) KeyIDs() []string { return []string{b.id} }

// rotating has a new active key at every signature.
type rotating struct{ n int }

func (r *rotating) Sign(keyring.Purpose, []byte) ([]byte, string, error) {
	r.n++
	return make([]byte, 32), fmt.Sprintf("k-%08d00000000", r.n), nil
}
func (*rotating) VerifyPrefix(keyring.Purpose, string, []byte, []byte) (bool, error) {
	return false, nil
}
func (*rotating) KeyIDs() []string { return nil }

// TestForStatus: the buttons of each status of reference.md, Snooze once per duration.
func TestForStatus(t *testing.T) {
	cmds := func(bs []buttons.Button) []string {
		out := []string{}
		for _, b := range bs {
			out = append(out, b.Command)
		}
		return out
	}
	for status, want := range map[string][]string{
		"firing":       {"acknowledge", "resolve", "snooze", "snooze", "snooze"},
		"acknowledged": {"unacknowledge", "resolve", "snooze", "snooze", "snooze"},
		"snoozed":      {"acknowledge", "unsnooze", "resolve"},
		"resolved":     {},
	} {
		if got := cmds(buttons.ForStatus(status, 3)); !slices.Equal(got, want) {
			t.Errorf("%s: %v", status, got)
		}
	}
	if bs := buttons.ForStatus("firing", 3); bs[4].Argument != 2 {
		t.Errorf("the third Snooze has argument %d", bs[4].Argument)
	}
}

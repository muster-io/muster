// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package keyring

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/keyring/dbgen"
)

// fakeStatus answers the reads of the Keyring status per Organization.
type fakeStatus struct {
	active    string
	replicas  []dbgen.Replica
	since     time.Time
	orgs      []int64
	encrypted map[int64][]dbgen.CountEncryptedValuesByKeyRow
	messages  map[int64][]dbgen.CountOpenButtonKeysRow
	activated map[int64][]dbgen.ListActivatedKeysRow
	fail      string
}

var errStatus = errors.New("boom")

func (f *fakeStatus) err(q string) error {
	if f.fail == q {
		return errStatus
	}
	return nil
}

func (f *fakeStatus) GetActiveKeyID(context.Context) (string, error) {
	return f.active, f.err("GetActiveKeyID")
}

func (f *fakeStatus) ListLiveReplicas(_ context.Context, since time.Time) ([]dbgen.Replica, error) {
	f.since = since
	return f.replicas, f.err("ListLiveReplicas")
}

func (f *fakeStatus) ListOrganizationIDs(context.Context) ([]int64, error) {
	return f.orgs, f.err("ListOrganizationIDs")
}

func (f *fakeStatus) CountEncryptedValuesByKey(_ context.Context, org int64) ([]dbgen.CountEncryptedValuesByKeyRow,
	error) {
	return f.encrypted[org], f.err("CountEncryptedValuesByKey")
}

func (f *fakeStatus) CountOpenButtonKeys(_ context.Context, org int64) ([]dbgen.CountOpenButtonKeysRow, error) {
	return f.messages[org], f.err("CountOpenButtonKeys")
}

func (f *fakeStatus) ListActivatedKeys(_ context.Context, org int64) ([]dbgen.ListActivatedKeysRow, error) {
	return f.activated[org], f.err("ListActivatedKeys")
}

func valuesOf(id string, n int64) dbgen.CountEncryptedValuesByKeyRow {
	return dbgen.CountEncryptedValuesByKeyRow{KeyID: pgtype.Text{String: id, Valid: true}, Encrypted: n}
}

// TestKeyringStatus is C-20.FR-6: the keys are this process's, the live replicas' and the active one, each with the
// live replicas that hold and lack it; a key a Secret or an open Root message needs is needed, counted over every
// Organization; a key that was active before — named by a keyring.key_activated entry, or needed — and nothing needs
// is removable; a key never active is not active yet, never removable; the active key comes first.
func TestKeyringStatus(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	f := &fakeStatus{active: "k-active", orgs: []int64{1, 2},
		replicas: []dbgen.Replica{{ReplicaID: "r-a", KeyIds: []string{"k-active", "k-old", "k-new"}},
			{ReplicaID: "r-b", KeyIds: []string{"k-active", "k-old", "k-secret", "k-message"}}},
		encrypted: map[int64][]dbgen.CountEncryptedValuesByKeyRow{
			1: {valuesOf("k-active", 4), {Encrypted: 9}},
			2: {valuesOf("k-secret", 1), valuesOf("k-gone", 2)},
		},
		messages: map[int64][]dbgen.CountOpenButtonKeysRow{2: {{KeyID: "k-message", Messages: 3}}},
		activated: map[int64][]dbgen.ListActivatedKeysRow{
			1: {{BeforeKeyID: "k-old", AfterKeyID: "k-active"}},
			2: {{BeforeKeyID: "", AfterKeyID: ""}},
		},
	}
	st, err := ReadStatus(t.Context(), f, []string{"k-active", "k-mine"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !f.since.Equal(now.Add(-LiveExpiry)) || st.ActiveKeyID != "k-active" {
		t.Errorf("live since %v, active %s", f.since, st.ActiveKeyID)
	}
	type want struct {
		state             KeyState
		needed, removable bool
		held, missing     []string
		values, messages  int64
	}
	all := []string{"r-a", "r-b"}
	wants := map[string]want{
		"k-active":  {KeyActive, true, false, all, []string{}, 4, 0},
		"k-gone":    {KeyNeeded, true, false, []string{}, all, 2, 0},
		"k-message": {KeyNeeded, true, false, []string{"r-b"}, []string{"r-a"}, 0, 3},
		"k-mine":    {KeyNotActiveYet, false, false, []string{}, all, 0, 0},
		"k-new":     {KeyNotActiveYet, false, false, []string{"r-a"}, []string{"r-b"}, 0, 0},
		"k-old":     {KeyRemovable, false, true, all, []string{}, 0, 0},
		"k-secret":  {KeyNeeded, true, false, []string{"r-b"}, []string{"r-a"}, 1, 0},
	}
	var ids []string
	for _, k := range st.Keys {
		ids = append(ids, k.ID)
		w, ok := wants[k.ID]
		if !ok || k.State != w.state || k.Needed != w.needed || k.Removable != w.removable ||
			k.Active != (w.state == KeyActive) || !slices.Equal(k.HeldBy, w.held) ||
			!slices.Equal(k.MissingOn, w.missing) || k.EncryptedValues != w.values || k.OpenMessages != w.messages {
			t.Errorf("%s = %+v, want %+v", k.ID, k, w)
		}
	}
	if !slices.Equal(ids, []string{"k-active", "k-gone", "k-message", "k-mine", "k-new", "k-old", "k-secret"}) {
		t.Errorf("keys %v", ids)
	}
	if k, ok := st.Key("k-old"); !ok || k.State != KeyRemovable {
		t.Errorf("Key = %+v, %v", k, ok)
	}
	if _, ok := st.Key("k-nope"); ok {
		t.Error("an unknown key is listed")
	}

	// An open Root message still signed by the old key holds it back; once edited, the key is removable again.
	f.messages[1] = []dbgen.CountOpenButtonKeysRow{{KeyID: "k-old", Messages: 1}}
	st, _ = ReadStatus(t.Context(), f, nil, now)
	if k, _ := st.Key("k-old"); k.State != KeyNeeded || k.Removable {
		t.Errorf("k-old with an open root message = %+v", k)
	}
	// A key that was never activated stays not active yet, with nothing depending on it.
	f.activated = nil
	f.messages[1] = nil
	st, _ = ReadStatus(t.Context(), f, nil, now)
	if k, _ := st.Key("k-old"); k.State != KeyNotActiveYet || k.Removable || k.Needed {
		t.Errorf("k-old without an activation = %+v", k)
	}
}

// TestKeyringStatusErrors: a failed read is the status's error.
func TestKeyringStatusErrors(t *testing.T) {
	for _, q := range []string{"GetActiveKeyID", "ListLiveReplicas", "ListOrganizationIDs", "CountEncryptedValuesByKey",
		"CountOpenButtonKeys", "ListActivatedKeys"} {
		f := &fakeStatus{active: "k-a", orgs: []int64{1}, fail: q}
		if _, err := ReadStatus(t.Context(), f, nil, time.Now()); !errors.Is(err, errStatus) {
			t.Errorf("%s: %v", q, err)
		}
	}
}

// TestStatusReader reads the status with this process's keys at the real time.
func TestStatusReader(t *testing.T) {
	k := newKeyring(t, 'a', 'b')
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	f := &fakeStatus{active: KeyID(material('a'))}
	st, err := NewStatusReader(f, k, clock.NewManual(now)).Status(t.Context())
	if err != nil || len(st.Keys) != 2 || st.Keys[0].State != KeyActive || st.Keys[1].State != KeyNotActiveYet ||
		!f.since.Equal(now.Add(-LiveExpiry)) {
		t.Errorf("status %+v, %v", st, err)
	}
	if NewStatusQueries(nil) == nil {
		t.Error("NewStatusQueries returned nil")
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package keyring_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/audit"
	adb "github.com/muster-io/muster/internal/audit/dbgen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/keyring/dbgen"
	"github.com/muster-io/muster/internal/live"
	"github.com/muster-io/muster/internal/logging"
)

// memStore is keyring_state in memory, for the canary of the rotation tests.
type memStore struct {
	state *dbgen.GetKeyringStateRow
}

func (s *memStore) GetKeyringState(context.Context) (dbgen.GetKeyringStateRow, error) {
	if s.state == nil {
		return dbgen.GetKeyringStateRow{}, pgx.ErrNoRows
	}
	return *s.state, nil
}

func (s *memStore) CreateKeyringState(_ context.Context, arg dbgen.CreateKeyringStateParams) (int64, error) {
	s.state = &dbgen.GetKeyringStateRow{ActiveKeyID: arg.ActiveKeyID, ActivatedAt: arg.ActivatedAt,
		CanaryCiphertext: arg.CanaryCiphertext, CanaryKeyID: arg.ActiveKeyID}
	return 1, nil
}

func (s *memStore) GetActiveKeyID(context.Context) (string, error) { return s.state.ActiveKeyID, nil }

func (s *memStore) RecordReplica(context.Context, dbgen.RecordReplicaParams) error { return nil }

func (s *memStore) DeleteReplica(context.Context, string) error { return nil }

func (s *memStore) ListLiveReplicas(context.Context, time.Time) ([]dbgen.Replica, error) {
	return nil, nil
}

// rotationDB is the database of an activation in memory: one transaction that commits or rolls back as a whole.
type rotationDB struct {
	active   string
	canary   []byte
	replicas []dbgen.Replica
	since    time.Time
	orgs     []int64
	// values are the encrypted values by Organization and field.
	values map[int64]map[string][]keyring.EncryptedValue
	resign map[int64]int64
	// audit, hints and notified are what committed.
	audit    []adb.InsertAuditEntryParams
	hints    []db.Hint
	notified []dbgen.NotifyKeyringParams
	fail     string
}

// tx is one transaction of rotationDB: its writes apply when it commits.
type tx struct {
	d        *rotationDB
	active   string
	canary   []byte
	values   map[int64]map[string][]keyring.EncryptedValue
	audit    []adb.InsertAuditEntryParams
	hints    []db.Hint
	notified []dbgen.NotifyKeyringParams
}

var errRotation = errors.New("boom")

func (d *rotationDB) InTx(_ context.Context, f func(keyring.RotationQueries) error) error {
	t := &tx{d: d, active: d.active, canary: d.canary, values: map[int64]map[string][]keyring.EncryptedValue{}}
	for org, fields := range d.values {
		t.values[org] = map[string][]keyring.EncryptedValue{}
		for field, vs := range fields {
			t.values[org][field] = slices.Clone(vs)
		}
	}
	if err := f(t); err != nil {
		return err
	}
	d.active, d.canary, d.values = t.active, t.canary, t.values
	d.audit, d.hints, d.notified = append(d.audit, t.audit...), append(d.hints, t.hints...),
		append(d.notified, t.notified...)
	return nil
}

func (t *tx) err(q string) error {
	if t.d.fail == q {
		return errRotation
	}
	return nil
}

func (t *tx) LockKeyringState(context.Context) (string, error) {
	return t.active, t.err("LockKeyringState")
}

func (t *tx) ActivateKey(_ context.Context, arg dbgen.ActivateKeyParams) (int64, error) {
	if err := t.err("ActivateKey"); err != nil || t.d.fail == "ActivateKey0" || arg.PreviousKeyID != t.active {
		return 0, err
	}
	t.active, t.canary = arg.KeyID, arg.CanaryCiphertext
	return 1, nil
}

func (t *tx) ListLiveReplicas(_ context.Context, since time.Time) ([]dbgen.Replica, error) {
	t.d.since = since
	return t.d.replicas, t.err("ListLiveReplicas")
}

func (t *tx) ListOrganizationIDs(context.Context) ([]int64, error) {
	return t.d.orgs, t.err("ListOrganizationIDs")
}

func (t *tx) CountOpenDeliveriesSignedByOtherKey(_ context.Context,
	arg dbgen.CountOpenDeliveriesSignedByOtherKeyParams) (int64, error) {
	return t.d.resign[arg.OrgID], t.err("CountOpenDeliveriesSignedByOtherKey")
}

func (t *tx) NotifyKeyring(_ context.Context, arg dbgen.NotifyKeyringParams) error {
	t.notified = append(t.notified, arg)
	return t.err("NotifyKeyring")
}

func (t *tx) Notify(_ context.Context, h db.Hint) error {
	t.hints = append(t.hints, h)
	return t.err("Notify")
}

func (t *tx) InsertAuditEntry(_ context.Context, arg adb.InsertAuditEntryParams) error {
	t.audit = append(t.audit, arg)
	return t.err("InsertAuditEntry")
}

func (t *tx) EncryptedValues(_ context.Context, field string, org int64, keyID string) ([]keyring.EncryptedValue,
	error) {
	var out []keyring.EncryptedValue
	for _, v := range t.values[org][field] {
		if v.KeyID != keyID {
			out = append(out, v)
		}
	}
	return out, t.err("EncryptedValues")
}

func (t *tx) Reencrypt(_ context.Context, field string, org int64, row keyring.RowKey, ct []byte, keyID,
	previous string) (int64, error) {
	if err := t.err("Reencrypt"); err != nil {
		return 0, err
	}
	vs := t.values[org][field]
	for i, v := range vs {
		if v.Row.String() == row.String() && v.KeyID == previous {
			vs[i].Ciphertext, vs[i].KeyID = ct, keyID
			return 1, nil
		}
	}
	return 0, nil
}

// rotationEnv is a process holding k-a, active, and k-b, with Secrets encrypted by k-a in two Organizations.
type rotationEnv struct {
	keys     *keyring.Keyring
	a, b     string
	db       *rotationDB
	log      bytes.Buffer
	rotation keyring.Rotation
	business time.Time
}

func material(c byte) []byte { return bytes.Repeat([]byte{c}, keyring.KeySize) }

func newRotationEnv(t *testing.T) *rotationEnv {
	t.Helper()
	k, err := keyring.New([][]byte{material('a'), material('b')}, false)
	if err != nil {
		t.Fatal(err)
	}
	store := &memStore{}
	st, err := k.Establish(t.Context(), store, time.Now())
	if err != nil || k.Open(t.Context(), logging.New(&bytes.Buffer{}, logging.LevelInfo), st) != nil {
		t.Fatal(err)
	}
	e := &rotationEnv{keys: k, a: keyring.KeyID(material('a')), b: keyring.KeyID(material('b')),
		business: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	encrypt := func(field, value string) keyring.EncryptedValue {
		ct, id, err := k.Encrypt(field, []byte(value))
		if err != nil {
			t.Fatal(err)
		}
		return keyring.EncryptedValue{Ciphertext: ct, KeyID: id}
	}
	token := encrypt("connections.bot_token", "bot-token")
	token.Row = keyring.RowKey{ID: 7}
	secret := encrypt("destination_secrets.value", "webhook-secret")
	secret.Row = keyring.RowKey{ID: 9, Name: "API_KEY"}
	verifier := encrypt("oidc_auth_requests.code_verifier", "verifier")
	verifier.Row = keyring.RowKey{Hash: []byte{0xab, 0xcd}}
	e.db = &rotationDB{active: e.a, canary: st.Canary, orgs: []int64{1, 2},
		replicas: []dbgen.Replica{{ReplicaID: "r-1", KeyIds: []string{e.a, e.b}}},
		values: map[int64]map[string][]keyring.EncryptedValue{
			1: {"connections.bot_token": {token}, "destination_secrets.value": {secret}},
			2: {"oidc_auth_requests.code_verifier": {verifier}},
		},
		resign: map[int64]int64{1: 3}}
	e.rotation = keyring.Rotation{Store: e.db, Keys: k, Audit: audit.NewWriter(logging.New(&e.log,
		logging.LevelInfo), clock.NewManual(e.business)), Log: logging.New(&e.log, logging.LevelInfo),
		Business: clock.NewManual(e.business), Real: clock.NewManual(e.business.Add(time.Hour))}
	return e
}

// opens checks that every value of field in the Organization org is encrypted with key and opens to want.
func (e *rotationEnv) opens(t *testing.T, org int64, field, key, want string) {
	t.Helper()
	for _, v := range e.db.values[org][field] {
		plain, err := e.keys.Decrypt(field, v.KeyID, v.Ciphertext)
		if v.KeyID != key || err != nil || string(plain) != want {
			t.Errorf("%s of row %s: key %s, %q, %v", field, v.Row, v.KeyID, plain, err)
		}
	}
}

// TestActivate is C-20.FR-7 and ADR-0011: once every live replica holds the key, it becomes active, the canary opens
// with it alone, every Secret is re-encrypted with it, keyring.key_activated is recorded per Organization with the
// actor cli, the diff of /active_key_id and the counts, the organization and system-status hints of every
// Organization and the keyring notification are sent in the transaction, and key_activated is logged after it.
func TestActivate(t *testing.T) {
	e := newRotationEnv(t)
	out, err := e.rotation.Activate(t.Context(), e.b, " ops ")
	if err != nil {
		t.Fatal(err)
	}
	if out != (keyring.Rotated{KeyID: e.b, Previous: e.a, Reencrypted: 3, ToResign: 3}) || e.db.active != e.b ||
		!e.db.since.Equal(e.business.Add(time.Hour-keyring.LiveExpiry)) {
		t.Errorf("rotated %+v, active %s, live since %v", out, e.db.active, e.db.since)
	}
	only, err := keyring.New([][]byte{material('b')}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := only.Open(t.Context(), logging.New(&bytes.Buffer{}, logging.LevelInfo), keyring.State{
		ActiveKeyID: e.b, CanaryKeyID: e.b, Canary: e.db.canary}); err != nil {
		t.Errorf("the canary does not open with the new key alone: %v", err)
	}
	e.opens(t, 1, "connections.bot_token", e.b, "bot-token")
	e.opens(t, 1, "destination_secrets.value", e.b, "webhook-secret")
	e.opens(t, 2, "oidc_auth_requests.code_verifier", e.b, "verifier")

	if len(e.db.audit) != 2 {
		t.Fatalf("audit %+v", e.db.audit)
	}
	for i, a := range e.db.audit {
		var diff []map[string]any
		var details map[string]any
		_ = json.Unmarshal(a.Diff, &diff)
		_ = json.Unmarshal(a.Details, &details)
		wantDetails := map[string]any{"reencrypted": float64(2 - i), "open_deliveries_to_resign": float64(3 * (1 - i))}
		if a.OrgID != int64(i+1) || a.Action != keyring.ActionKeyActivated || a.ActorKind != "cli" ||
			a.ActorName.String != "ops" || a.Transport != "cli" || a.ResourceType.String != keyring.ResourceKeyring ||
			a.ResourceName.String != e.b || !a.At.Equal(e.business) || len(diff) != 1 ||
			diff[0]["pointer"] != "/active_key_id" || diff[0]["before"] != e.a || diff[0]["after"] != e.b ||
			details["reencrypted"] != wantDetails["reencrypted"] ||
			details["open_deliveries_to_resign"] != wantDetails["open_deliveries_to_resign"] {
			t.Errorf("audit %d: %+v, diff %v, details %v", i, a, diff, details)
		}
	}
	wantHints := []db.Hint{{OrgID: 1, Type: live.HintOrganization}, {OrgID: 1, Type: live.HintSystemStatus},
		{OrgID: 2, Type: live.HintOrganization}, {OrgID: 2, Type: live.HintSystemStatus}}
	if !slices.Equal(e.db.hints, wantHints) || !slices.Equal(e.db.notified, []dbgen.NotifyKeyringParams{
		{Channel: keyring.Channel, Payload: e.b}}) {
		t.Errorf("hints %+v, notified %+v", e.db.hints, e.db.notified)
	}
	var activated map[string]any
	for line := range strings.Lines(e.log.String()) {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["event"] == "key_activated" {
			activated = m
		}
	}
	if activated["level"] != "WARN" || activated["from"] != e.a || activated["to"] != e.b || activated["actor"] != "ops" {
		t.Errorf("key_activated %v", activated)
	}
}

// TestActivateAlreadyActive: a key already active records nothing and logs no activation; values another key still
// encrypted are re-encrypted with it and announced by the hints; with none, only the keyring notification is sent.
func TestActivateAlreadyActive(t *testing.T) {
	e := newRotationEnv(t)
	out, err := e.rotation.Activate(t.Context(), e.a, "ops")
	if err != nil || !out.AlreadyActive || out.Reencrypted != 0 || len(e.db.audit)+len(e.db.hints) != 0 ||
		len(e.db.notified) != 1 || strings.Contains(e.log.String(), "key_activated") {
		t.Errorf("rotated %+v, %v, audit %d, hints %d", out, err, len(e.db.audit), len(e.db.hints))
	}
	if _, err := e.rotation.Activate(t.Context(), e.b, "ops"); err != nil {
		t.Fatal(err)
	}
	// A replica that had not switched yet wrote a Secret with the old key.
	ct, _, _ := e.keys.Encrypt("connections.bot_token", []byte("late"))
	e.db.values[2]["connections.bot_token"] = []keyring.EncryptedValue{{Row: keyring.RowKey{ID: 8},
		Ciphertext: ct, KeyID: e.a}}
	e.db.audit, e.db.hints = nil, nil
	out, err = e.rotation.Activate(t.Context(), e.b, "ops")
	if err != nil || !out.AlreadyActive || out.Reencrypted != 1 || len(e.db.audit) != 0 ||
		!slices.Equal(e.db.hints, []db.Hint{{OrgID: 2, Type: live.HintOrganization},
			{OrgID: 2, Type: live.HintSystemStatus}}) {
		t.Errorf("rotated %+v, %v, audit %d, hints %+v", out, err, len(e.db.audit), e.db.hints)
	}
	e.opens(t, 2, "connections.bot_token", e.b, "late")
}

// TestActivateRefusals: a key this process does not hold, a live replica that lacks the key — named, sorted — and a
// missing actor are refused before anything changes, as is a value that does not decrypt, whose error names the field
// and the row, never the value; every failed step rolls back.
func TestActivateRefusals(t *testing.T) {
	e := newRotationEnv(t)
	other := keyring.KeyID(material('c'))
	if _, err := e.rotation.Activate(t.Context(), other, "ops"); !errors.Is(err, keyring.ErrKeyNotHeld) ||
		!strings.Contains(err.Error(), other) {
		t.Errorf("a key not held = %v", err)
	}
	if _, err := e.rotation.Activate(t.Context(), e.b, " "); err == nil {
		t.Error("no actor was accepted")
	}
	e.db.replicas = append(e.db.replicas, dbgen.Replica{ReplicaID: "ghost-2", KeyIds: []string{e.a}},
		dbgen.Replica{ReplicaID: "ghost-1", KeyIds: []string{e.a}})
	_, err := e.rotation.Activate(t.Context(), e.b, "ops")
	lacking, ok := errors.AsType[*keyring.ReplicasLackKeyError](err)
	if !ok || !slices.Equal(lacking.Replicas, []string{"ghost-1", "ghost-2"}) ||
		err.Error() != "Replicas ghost-1, ghost-2 do not hold key "+e.b+" yet." {
		t.Errorf("refusal = %v", err)
	}
	if e.db.active != e.a || len(e.db.audit)+len(e.db.hints)+len(e.db.notified) != 0 {
		t.Errorf("the refusal changed the database: active %s", e.db.active)
	}
	e.db.replicas = e.db.replicas[:1]

	altered := slices.Clone(e.db.values[1]["connections.bot_token"][0].Ciphertext)
	altered[len(altered)-1] ^= 1
	e.db.values[1]["connections.bot_token"][0].Ciphertext = altered
	_, err = e.rotation.Activate(t.Context(), e.b, "ops")
	if !errors.Is(err, keyring.ErrDecrypt) || !strings.Contains(err.Error(), "connections.bot_token of row 7") ||
		strings.Contains(err.Error(), "bot-token") || e.db.active != e.a {
		t.Errorf("a value that does not decrypt = %v, active %s", err, e.db.active)
	}
}

// TestActivateErrors: every failed query is the activation's error and changes nothing.
func TestActivateErrors(t *testing.T) {
	for _, q := range []string{"LockKeyringState", "ListLiveReplicas", "ActivateKey", "ActivateKey0",
		"ListOrganizationIDs", "EncryptedValues", "Reencrypt", "CountOpenDeliveriesSignedByOtherKey",
		"InsertAuditEntry", "Notify", "NotifyKeyring"} {
		t.Run(q, func(t *testing.T) {
			e := newRotationEnv(t)
			e.db.fail = q
			if _, err := e.rotation.Activate(t.Context(), e.b, "ops"); err == nil {
				t.Error("no error")
			}
			if e.db.active != e.a || len(e.db.audit)+len(e.db.hints)+len(e.db.notified) != 0 ||
				strings.Contains(e.log.String(), `"event":"key_activated"`) {
				t.Errorf("a failed activation changed the database: active %s", e.db.active)
			}
		})
	}
}

// TestEncryptedFields: the registry of rotation lists every field of the encrypted_values view once, in its order;
// the integration test compares it with the view itself.
func TestEncryptedFields(t *testing.T) {
	fields := keyring.EncryptedFields()
	sorted := slices.Clone(fields)
	slices.Sort(sorted)
	if len(fields) != 15 || len(slices.Compact(sorted)) != 15 || fields[0] != "organizations.outgoing_heartbeat_url" ||
		fields[14] != "destination_secrets.value" {
		t.Errorf("fields %v", fields)
	}
	for _, tc := range []struct {
		row  keyring.RowKey
		want string
	}{{keyring.RowKey{ID: 7}, "7"}, {keyring.RowKey{ID: 9, Name: "API_KEY"}, "9/API_KEY"},
		{keyring.RowKey{Hash: []byte{0xab}}, "ab"}} {
		if tc.row.String() != tc.want {
			t.Errorf("%+v = %s", tc.row, tc.row.String())
		}
	}
	if keyring.NewRotationStore(nil) == nil {
		t.Error("NewRotationStore returned nil")
	}
}

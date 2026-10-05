// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package keyring

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/keyring/dbgen"
	"github.com/muster-io/muster/internal/logging"
)

func material(c byte) []byte { return bytes.Repeat([]byte{c}, KeySize) }

func b64(m []byte) string { return base64.StdEncoding.EncodeToString(m) }

func newKeyring(t *testing.T, cs ...byte) *Keyring {
	t.Helper()
	var keys [][]byte
	for _, c := range cs {
		keys = append(keys, material(c))
	}
	k, err := New(keys, false)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// active makes the first key active, as the canary check does.
func active(t *testing.T, k *Keyring, id string) *Keyring {
	t.Helper()
	if err := k.setActive(id); err != nil {
		t.Fatal(err)
	}
	return k
}

func TestKeyIDIsStableAndHidesTheMaterial(t *testing.T) {
	a, b := newKeyring(t, 'a', 'b'), newKeyring(t, 'a', 'b')
	if !slices.Equal(a.KeyIDs(), b.KeyIDs()) {
		t.Errorf("the same keys have ids %v and %v", a.KeyIDs(), b.KeyIDs())
	}
	ids := a.KeyIDs()
	if ids[0] == ids[1] || ids[0] != KeyID(material('a')) {
		t.Errorf("ids %v", ids)
	}
	for _, id := range ids {
		if !strings.HasPrefix(id, "k-") || len(id) != 18 {
			t.Errorf("id %q is not k- and 16 hex digits", id)
		}
		if strings.Contains(id, b64(material('a'))[:8]) || strings.Contains(id, "6161616161") {
			t.Errorf("id %q shows the material", id)
		}
	}
	// The pinned id of a known key: a change would orphan every stored ciphertext.
	if got := KeyID(material('k')); got != "k-5e318f8cf9cbe249" {
		t.Errorf("KeyID = %s", got)
	}
}

func TestNewClearsTheMaterial(t *testing.T) {
	m := material('a')
	if _, err := New([][]byte{m}, false); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(m, make([]byte, KeySize)) {
		t.Error("New kept the key material in the caller's slice")
	}
}

func TestSubKeysAreSeparate(t *testing.T) {
	k := newKeyring(t, 'a')
	kk := k.keys[0]
	button, csrf := kk.macs[PurposeButtonSignature], kk.macs[PurposeCSRF]
	if bytes.Equal(button, csrf) || bytes.Equal(button, material('a')) || bytes.Equal(csrf, material('a')) {
		t.Fatal("the signature sub-keys are not separate")
	}
	active(t, k, kk.id)
	msg := []byte("acknowledge AG0000000000AB")
	b, id, err := k.Sign(PurposeButtonSignature, msg)
	if err != nil || id != kk.id {
		t.Fatalf("Sign = %v, %s", err, id)
	}
	c, _, _ := k.Sign(PurposeCSRF, msg)
	if bytes.Equal(b, c) {
		t.Error("a button signature equals a CSRF signature of the same message")
	}
	if ok, err := k.Verify(PurposeButtonSignature, id, msg, b); !ok || err != nil {
		t.Errorf("Verify = %v, %v", ok, err)
	}
	if ok, _ := k.Verify(PurposeCSRF, id, msg, b); ok {
		t.Error("a button signature verifies as a CSRF signature")
	}
	if _, _, err := k.Sign(PurposeEncryption, msg); err == nil {
		t.Error("the encryption sub-key signs")
	}
	if _, err := k.Verify(PurposeEncryption, id, msg, b); err == nil {
		t.Error("the encryption sub-key verifies")
	}
	if _, err := k.Verify(PurposeCSRF, "k-unknown", msg, b); !errors.Is(err, ErrKeyNotHeld) {
		t.Errorf("Verify with an unknown key = %v", err)
	}
	// The encryption sub-key differs from the material and from both signature sub-keys.
	enc, err := hkdf.Key(sha256.New, material('a'), nil, hkdfInfo+string(PurposeEncryption), KeySize)
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range [][]byte{material('a'), button, csrf} {
		if bytes.Equal(enc, other) {
			t.Error("the encryption sub-key equals another key")
		}
	}
	block, _ := aes.NewCipher(enc)
	gcm, _ := cipher.NewGCM(block)
	ct, _, _ := k.Encrypt("f", []byte("v"))
	if plain, err := gcm.Open(nil, ct[:12], ct[12:], []byte("f")); err != nil || string(plain) != "v" {
		t.Errorf("the ciphertext does not open with the encryption sub-key: %v", err)
	}
}

func TestEncryptionBindsTheField(t *testing.T) {
	k := active(t, newKeyring(t, 'a'), KeyID(material('a')))
	ct, id, err := k.Encrypt("destinations.bot_token", []byte("secret-bot-token"))
	if err != nil || id != KeyID(material('a')) {
		t.Fatalf("Encrypt = %v, %s", err, id)
	}
	if bytes.Contains(ct, []byte("secret-bot-token")) {
		t.Error("the ciphertext holds the plaintext")
	}
	ct2, _, _ := k.Encrypt("destinations.bot_token", []byte("secret-bot-token"))
	if bytes.Equal(ct, ct2) {
		t.Error("two encryptions of one value are equal: the nonce is not random")
	}
	got, err := k.Decrypt("destinations.bot_token", id, ct)
	if err != nil || string(got) != "secret-bot-token" {
		t.Fatalf("Decrypt = %q, %v", got, err)
	}
	_, err = k.Decrypt("oidc_settings.client_secret", id, ct)
	if !errors.Is(err, ErrDecrypt) || strings.Contains(err.Error(), "secret-bot-token") {
		t.Errorf("Decrypt for another field = %v", err)
	}
	altered := bytes.Clone(ct)
	altered[len(altered)-1] ^= 1
	if _, err := k.Decrypt("destinations.bot_token", id, altered); !errors.Is(err, ErrDecrypt) {
		t.Errorf("Decrypt of altered bytes = %v", err)
	}
	if _, err := k.Decrypt("destinations.bot_token", id, ct[:5]); !errors.Is(err, ErrDecrypt) {
		t.Errorf("Decrypt of a short ciphertext = %v", err)
	}
	if _, err := k.Decrypt("destinations.bot_token", "k-unknown", ct); !errors.Is(err, ErrKeyNotHeld) {
		t.Errorf("Decrypt with an unknown key = %v", err)
	}
}

func TestEveryKeyDecryptsWhatItEncrypted(t *testing.T) {
	old := active(t, newKeyring(t, 'a'), KeyID(material('a')))
	ct, id, _ := old.Encrypt("f", []byte("v"))
	both := active(t, newKeyring(t, 'b', 'a'), KeyID(material('b')))
	got, err := both.Decrypt("f", id, ct)
	if err != nil || string(got) != "v" {
		t.Errorf("a non-active key of the Keyring: %q, %v", got, err)
	}
	ct2, id2, _ := both.Encrypt("f", []byte("w"))
	if id2 != KeyID(material('b')) {
		t.Errorf("encrypted with %s, want the active key", id2)
	}
	if _, err := old.Decrypt("f", id2, ct2); !errors.Is(err, ErrKeyNotHeld) {
		t.Errorf("a Keyring without the key = %v", err)
	}
}

func TestNoActiveKey(t *testing.T) {
	k := newKeyring(t, 'a')
	if _, _, err := k.Encrypt("f", nil); !errors.Is(err, ErrNoActiveKey) {
		t.Errorf("Encrypt = %v", err)
	}
	if _, _, err := k.Sign(PurposeCSRF, nil); !errors.Is(err, ErrNoActiveKey) {
		t.Errorf("Sign = %v", err)
	}
	if k.ActiveKeyID() != "" {
		t.Errorf("ActiveKeyID = %q", k.ActiveKeyID())
	}
	if err := k.setActive("k-unknown"); !errors.Is(err, ErrKeyNotHeld) {
		t.Errorf("setActive = %v", err)
	}
}

func TestNewRefuses(t *testing.T) {
	dev := developmentMaterial()
	tests := []struct {
		name  string
		keys  [][]byte
		allow bool
		want  string
	}{
		{"empty", nil, false, "MUSTER_SECRET_KEYS holds no key: generate a key with `openssl rand -base64 32`"},
		{"short", [][]byte{material('a'), []byte("short")}, false, "MUSTER_SECRET_KEYS: key 2 is not 32 bytes"},
		{"repeated", [][]byte{material('a'), material('b'), material('a')}, false,
			"MUSTER_SECRET_KEYS: key 3 repeats key 1"},
		{"development key", [][]byte{material('a'), bytes.Clone(dev)}, false,
			"MUSTER_SECRET_KEYS holds the published development key, which only muster dev accepts: generate a key " +
				"with `openssl rand -base64 32`"},
	}
	for _, tt := range tests {
		_, err := New(tt.keys, tt.allow)
		if err == nil || err.Error() != tt.want && !strings.HasPrefix(err.Error(), tt.want) {
			t.Errorf("%s: New = %v, want %q", tt.name, err, tt.want)
		}
	}
	if _, err := New([][]byte{bytes.Clone(dev)}, true); err != nil {
		t.Errorf("development mode refused the development key: %v", err)
	}
}

func TestEnvProvider(t *testing.T) {
	a, b := b64(material('a')), b64(material('b'))
	ctx := t.Context()
	tests := []struct {
		name string
		env  Env
		want []byte // the first byte of each key
		err  string
	}{
		{"one key", Env{Keys: logging.Secret(a), Source: SecretKeysVar}, []byte("a"), ""},
		{"two keys", Env{Keys: logging.Secret(" " + a + " , " + b), Source: SecretKeysVar}, []byte("ab"), ""},
		{"file", Env{Keys: logging.Secret(b + "\r\n\n" + a + "\n"), Source: SecretKeysFileVar}, []byte("ba"), ""},
		{"not set", Env{}, nil, "MUSTER_SECRET_KEYS is not set: generate a key with `openssl rand -base64 32`"},
		{"empty", Env{Keys: " ", Source: SecretKeysVar}, nil,
			"MUSTER_SECRET_KEYS is empty: generate a key with `openssl rand -base64 32`"},
		{"empty file", Env{Keys: "\n\n", Source: SecretKeysFileVar}, nil,
			"MUSTER_SECRET_KEYS_FILE names a file without a key: generate a key with `openssl rand -base64 32`"},
		{"placeholder", Env{Keys: Placeholder, Source: SecretKeysVar}, nil,
			"MUSTER_SECRET_KEYS still holds the placeholder of the compose example: generate a key with " +
				"`openssl rand -base64 32`"},
		{"placeholder in a file", Env{Keys: logging.Secret(a + "\n" + Placeholder), Source: SecretKeysFileVar}, nil,
			"the file named by MUSTER_SECRET_KEYS_FILE still holds the placeholder of the compose example"},
		{"not base64", Env{Keys: logging.Secret(a + ",not-a-key!"), Source: SecretKeysVar}, nil,
			"MUSTER_SECRET_KEYS: key 2 is not a base64-encoded key of 32 bytes: generate a key with " +
				"`openssl rand -base64 32`"},
		{"too short", Env{Keys: "a2V5", Source: SecretKeysVar}, nil,
			"MUSTER_SECRET_KEYS: key 1 is not a base64-encoded key of 32 bytes"},
		{"trailing comma", Env{Keys: logging.Secret(a + ","), Source: SecretKeysVar}, nil,
			"MUSTER_SECRET_KEYS: key 2 is not a base64-encoded key"},
	}
	for _, tt := range tests {
		keys, err := tt.env.MasterKeys(ctx)
		if tt.err != "" {
			if err == nil || !strings.HasPrefix(err.Error(), tt.err) {
				t.Errorf("%s: err = %v, want %q", tt.name, err, tt.err)
			}
			if err != nil && strings.Contains(err.Error(), "not-a-key") {
				t.Errorf("%s: the error repeats the value: %v", tt.name, err)
			}
			continue
		}
		if err != nil || len(keys) != len(tt.want) {
			t.Errorf("%s: %d keys, %v", tt.name, len(keys), err)
			continue
		}
		for i, k := range keys {
			if !bytes.Equal(k, material(tt.want[i])) {
				t.Errorf("%s: key %d is wrong", tt.name, i+1)
			}
		}
	}
}

func TestLoad(t *testing.T) {
	k, err := Load(t.Context(), Env{Keys: logging.Secret(b64(material('a'))), Source: SecretKeysVar}, false)
	if err != nil || !k.Holds(KeyID(material('a'))) {
		t.Fatalf("Load = %v", err)
	}
	if _, err := Load(t.Context(), Env{Keys: DevelopmentKey, Source: SecretKeysVar}, false); !errors.Is(err,
		errDevelopmentKey) {
		t.Errorf("Load of the development key = %v", err)
	}
	if _, err := Load(t.Context(), Env{}, false); err == nil {
		t.Error("Load without keys succeeded")
	}
}

// fakeStore keeps keyring_state and replicas in memory, with the real clock semantics of the queries.
type fakeStore struct {
	mu       sync.Mutex
	state    *dbgen.GetKeyringStateRow
	replicas map[string]dbgen.Replica
	creates  int
	err      error // fails every call
	readErr  error // fails GetKeyringState and GetActiveKeyID
}

func (s *fakeStore) GetKeyringState(context.Context) (dbgen.GetKeyringStateRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.err != nil:
		return dbgen.GetKeyringStateRow{}, s.err
	case s.readErr != nil:
		return dbgen.GetKeyringStateRow{}, s.readErr
	case s.state == nil:
		return dbgen.GetKeyringStateRow{}, pgx.ErrNoRows
	}
	return *s.state, nil
}

func (s *fakeStore) CreateKeyringState(_ context.Context, arg dbgen.CreateKeyringStateParams) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, s.err
	}
	s.creates++
	if s.state != nil {
		return 0, nil
	}
	s.state = &dbgen.GetKeyringStateRow{ActiveKeyID: arg.ActiveKeyID, ActivatedAt: arg.ActivatedAt,
		CanaryCiphertext: arg.CanaryCiphertext, CanaryKeyID: arg.ActiveKeyID}
	return 1, nil
}

func (s *fakeStore) GetActiveKeyID(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readErr != nil {
		return "", s.readErr
	}
	return s.state.ActiveKeyID, nil
}

func (s *fakeStore) RecordReplica(_ context.Context, arg dbgen.RecordReplicaParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if s.replicas == nil {
		s.replicas = map[string]dbgen.Replica{}
	}
	r, ok := s.replicas[arg.ReplicaID]
	if !ok {
		r = dbgen.Replica{ReplicaID: arg.ReplicaID, Hostname: arg.Hostname, Version: arg.Version,
			StartedAt: arg.StartedAt}
	}
	r.KeyIds, r.RefreshedAt = arg.KeyIds, arg.RefreshedAt
	s.replicas[arg.ReplicaID] = r
	return nil
}

func (s *fakeStore) DeleteReplica(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	delete(s.replicas, id)
	return nil
}

func (s *fakeStore) ListLiveReplicas(_ context.Context, since time.Time) ([]dbgen.Replica, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	var out []dbgen.Replica
	for _, r := range s.replicas {
		if r.RefreshedAt.After(since) {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b dbgen.Replica) int { return strings.Compare(a.ReplicaID, b.ReplicaID) })
	return out, nil
}

func lines(t *testing.T, b *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.Lines(b.String()) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func TestEstablishAndOpen(t *testing.T) {
	ctx := t.Context()
	s := &fakeStore{}
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	k := newKeyring(t, 'a', 'b')
	st, err := k.Establish(ctx, s, now)
	if err != nil {
		t.Fatal(err)
	}
	first := KeyID(material('a'))
	if st.ActiveKeyID != first || st.CanaryKeyID != first || !s.state.ActivatedAt.Equal(now) || s.creates != 1 {
		t.Fatalf("state %+v, stored %+v", st, s.state)
	}
	var log bytes.Buffer
	if err := k.Open(ctx, logging.New(&log, logging.LevelInfo), st); err != nil {
		t.Fatal(err)
	}
	if k.ActiveKeyID() != first {
		t.Errorf("active %s", k.ActiveKeyID())
	}
	l := lines(t, &log)
	if len(l) != 1 || l[0]["event"] != "keyring_loaded" || l[0]["active_key_id"] != first ||
		len(l[0]["key_ids"].([]any)) != 2 {
		t.Errorf("log %v", l)
	}

	// A later start, with the keys in another order, keeps the active key and the canary.
	again := newKeyring(t, 'b', 'a')
	st2, err := again.Establish(ctx, s, now.Add(time.Hour))
	if err != nil || s.creates != 1 || st2.ActiveKeyID != first {
		t.Fatalf("Establish again = %+v, %v (creates %d)", st2, err, s.creates)
	}
	if err := again.Open(ctx, logging.New(&bytes.Buffer{}, logging.LevelInfo), st2); err != nil ||
		again.ActiveKeyID() != first {
		t.Errorf("Open = %v, active %s", err, again.ActiveKeyID())
	}
}

func TestOpenRefusesAWrongKeyring(t *testing.T) {
	ctx := t.Context()
	s := &fakeStore{}
	st, err := newKeyring(t, 'a').Establish(ctx, s, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	altered := st
	altered.Canary = bytes.Clone(st.Canary)
	altered.Canary[len(altered.Canary)-1] ^= 1
	mismatched := st
	mismatched.CanaryKeyID = KeyID(material('b'))
	for name, tc := range map[string]struct {
		k  *Keyring
		st State
	}{
		"another key":           {newKeyring(t, 'b'), st},
		"an altered canary":     {newKeyring(t, 'a'), altered},
		"canary of another key": {newKeyring(t, 'a', 'b'), mismatched},
	} {
		var log bytes.Buffer
		err := tc.k.Open(ctx, logging.New(&log, logging.LevelInfo), tc.st)
		if !errors.Is(err, ErrKeyMismatch) || err.Error() != "master key does not match the database" {
			t.Errorf("%s: Open = %v", name, err)
		}
		l := lines(t, &log)
		if len(l) != 1 || l[0]["event"] != "key_canary_failed" || l[0]["level"] != "ERROR" ||
			l[0]["error"] != "master key does not match the database" {
			t.Errorf("%s: log %v", name, l)
		}
		if tc.k.ActiveKeyID() != "" {
			t.Errorf("%s: a key became active", name)
		}
	}
}

func TestEstablishErrors(t *testing.T) {
	boom := errors.New("boom")
	if _, err := newKeyring(t, 'a').Establish(t.Context(), &fakeStore{readErr: boom}, time.Now()); !errors.Is(err,
		boom) {
		t.Errorf("read error = %v", err)
	}
	s := &fakeStore{}
	s.err = nil
	failing := &failingCreate{fakeStore: s}
	if _, err := newKeyring(t, 'a').Establish(t.Context(), failing, time.Now()); err == nil ||
		!strings.HasPrefix(err.Error(), "write the key canary") {
		t.Errorf("write error = %v", err)
	}
}

type failingCreate struct{ *fakeStore }

func (failingCreate) CreateKeyringState(context.Context, dbgen.CreateKeyringStateParams) (int64, error) {
	return 0, errors.New("boom")
}

func TestNewReplicaID(t *testing.T) {
	a, b := NewReplicaID("muster-0"), NewReplicaID("muster-0")
	if a == b || !strings.HasPrefix(a, "muster-0-") || len(a) != len("muster-0-")+8 {
		t.Errorf("ids %q and %q", a, b)
	}
	if id := NewReplicaID(""); !strings.HasPrefix(id, "replica-") {
		t.Errorf("id without a host name %q", id)
	}
}

func TestRecorder(t *testing.T) {
	ctx := t.Context()
	s := &fakeStore{}
	k := newKeyring(t, 'a', 'b')
	st, _ := k.Establish(ctx, s, time.Now())
	if err := k.Open(ctx, logging.New(&bytes.Buffer{}, logging.LevelInfo), st); err != nil {
		t.Fatal(err)
	}
	realClock := clock.NewManual(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	var log bytes.Buffer
	r := NewRecorder(k, s, realClock, logging.New(&log, logging.LevelInfo), "muster-0-abcdefgh", "muster-0", "1.2.3")
	if r.ID() != "muster-0-abcdefgh" {
		t.Errorf("ID = %s", r.ID())
	}
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	live, err := LiveReplicas(ctx, s, realClock.Now())
	if err != nil || len(live) != 1 {
		t.Fatalf("LiveReplicas = %v, %v", live, err)
	}
	want := Replica{ID: "muster-0-abcdefgh", Hostname: "muster-0", Version: "1.2.3", KeyIDs: k.KeyIDs(),
		StartedAt: realClock.Now(), RefreshedAt: realClock.Now()}
	if got := live[0]; got.ID != want.ID || got.Hostname != want.Hostname || got.Version != want.Version ||
		!slices.Equal(got.KeyIDs, want.KeyIDs) || !got.StartedAt.Equal(want.StartedAt) {
		t.Errorf("replica %+v, want %+v", got, want)
	}

	// The record ages out of the live window unless it is refreshed.
	realClock.Advance(LiveExpiry)
	if live, _ := LiveReplicas(ctx, s, realClock.Now()); len(live) != 0 {
		t.Errorf("a record of LiveExpiry ago is live: %v", live)
	}
	if err := r.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if live, _ := LiveReplicas(ctx, s, realClock.Now()); len(live) != 1 || !live[0].StartedAt.Equal(want.StartedAt) ||
		!live[0].RefreshedAt.Equal(realClock.Now()) {
		t.Errorf("after a refresh: %v", live)
	}

	// Another held key that became active is taken over.
	s.state.ActiveKeyID = KeyID(material('b'))
	if err := r.Refresh(ctx); err != nil || k.ActiveKeyID() != KeyID(material('b')) {
		t.Errorf("Refresh = %v, active %s", err, k.ActiveKeyID())
	}

	// An active key that is not held stops the replica.
	s.state.ActiveKeyID = "k-unknown"
	if err := r.Refresh(ctx); !errors.Is(err, ErrKeyMismatch) {
		t.Errorf("Refresh = %v", err)
	}
	l := lines(t, &log)
	if last := l[len(l)-1]; last["event"] != "active_key_not_held" || last["level"] != "ERROR" ||
		last["active_key_id"] != "k-unknown" || last["error"] != "master key does not match the database" {
		t.Errorf("log %v", l)
	}

	if err := r.Stop(ctx); err != nil || len(s.replicas) != 0 {
		t.Errorf("Stop = %v, records %d", err, len(s.replicas))
	}
}

func TestRecorderRun(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s := &fakeStore{}
	k := newKeyring(t, 'a')
	st, _ := k.Establish(ctx, s, time.Now())
	_ = k.Open(ctx, logging.New(&bytes.Buffer{}, logging.LevelInfo), st)
	var log bytes.Buffer
	var mu sync.Mutex
	r := NewRecorder(k, s, clock.Real{}, logging.New(&lockedWriter{w: &log, mu: &mu}, logging.LevelInfo), "r1", "",
		"dev")
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	ticks := make(chan time.Time)
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx, ticks) }()

	// A failed refresh is logged and retried.
	s.mu.Lock()
	s.readErr = errors.New("connection refused")
	s.mu.Unlock()
	ticks <- time.Now()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		mu.Lock()
		logged := strings.Contains(log.String(), "replica_record_failed")
		mu.Unlock()
		if logged {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the failed refresh was not logged")
		}
	}
	s.mu.Lock()
	s.readErr = nil
	s.state.ActiveKeyID = "k-unknown"
	s.mu.Unlock()
	ticks <- time.Now()
	if err := <-done; !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("Run = %v", err)
	}
	mu.Lock()
	l := lines(t, &log)
	mu.Unlock()
	if len(l) != 2 || l[0]["event"] != "replica_record_failed" || l[0]["replica"] != "r1" ||
		l[1]["event"] != "active_key_not_held" {
		t.Errorf("log %v", l)
	}

	// Run ends without an error when its context ends.
	go func() { done <- r.Run(ctx, ticks) }()
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run after the context ended = %v", err)
	}
}

type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func TestRecorderStoreErrors(t *testing.T) {
	boom := errors.New("boom")
	k := newKeyring(t, 'a')
	r := NewRecorder(k, &fakeStore{err: boom}, clock.Real{}, logging.New(&bytes.Buffer{}, logging.LevelInfo), "r1",
		"h", "v")
	if err := r.Start(t.Context()); !errors.Is(err, boom) {
		t.Errorf("Start = %v", err)
	}
	if err := r.Stop(t.Context()); !errors.Is(err, boom) {
		t.Errorf("Stop = %v", err)
	}
	if _, err := LiveReplicas(t.Context(), &fakeStore{err: boom}, time.Now()); !errors.Is(err, boom) {
		t.Errorf("LiveReplicas = %v", err)
	}
	if NewStore(nil) == nil {
		t.Error("NewStore returned nil")
	}
}

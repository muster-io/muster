// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package webhooks

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
)

// TestSignatureVector checks the signature against the example of the Standard Webhooks specification, computed
// independently of this code.
func TestSignatureVector(t *testing.T) {
	secret := logging.Secret("whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw")
	body := []byte(`{"test": 2432232314}`)
	got, err := Sign([]logging.Secret{secret}, "msg_p5jXN8AQM9LWM0D4loKWxJek", time.Unix(1614265330, 0), body)
	if err != nil || got != "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE=" {
		t.Fatalf("signature %q (%v)", got, err)
	}
	if !Verify(secret, "msg_p5jXN8AQM9LWM0D4loKWxJek", "1614265330", body, "v1,bogus "+got) {
		t.Error("the signature does not verify")
	}
}

// TestSigning is C-15.FR-5: a new Signing secret is whsec_ and 32 random bytes; each secret that still signs adds its
// v1 signature, separated by a space, each of which verifies with its own secret only; a changed id, time or body, and
// another version, verify with none.
func TestSigning(t *testing.T) {
	a, b := NewSigningSecret(), NewSigningSecret()
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(string(a), SigningSecretPrefix))
	if !strings.HasPrefix(string(a), SigningSecretPrefix) || err != nil || len(key) != 32 || a == b {
		t.Fatalf("secret %d bytes (%v)", len(key), err)
	}
	at := time.Unix(1760000000, 0)
	body := []byte(`{"event":"created"}`)
	header, err := Sign([]logging.Secret{a, b}, "msg_1", at, body)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(header, " ")
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "v1,") || !strings.HasPrefix(parts[1], "v1,") {
		t.Fatalf("header %q", header)
	}
	for _, s := range []logging.Secret{a, b} {
		if !Verify(s, "msg_1", "1760000000", body, header) {
			t.Error("a signature does not verify")
		}
	}
	if Verify(a, "msg_1", "1760000000", body, parts[1]) || Verify(a, "msg_2", "1760000000", body, header) ||
		Verify(a, "msg_1", "1760000001", body, header) || Verify(a, "msg_1", "1760000000", []byte("{}"), header) ||
		Verify(a, "msg_1", "1760000000", body, "v2,"+strings.TrimPrefix(parts[0], "v1,")) ||
		Verify("nope", "msg_1", "1760000000", body, header) {
		t.Error("a wrong signature verifies")
	}
	for _, bad := range [][]logging.Secret{nil, {"nope"}, {"whsec_!!"}, {"whsec_"}} {
		if _, err := Sign(bad, "msg_1", at, body); err == nil {
			t.Errorf("%q signed", bad)
		}
	}
}

// TestSigningSecretRotation is C-15.FR-5 and AC-2: generating moves the current Signing secret to the previous one,
// which still signs since then, and returns the new one once; retiring wipes it; both are recorded without the values;
// master key rotation re-encrypts the Signing secrets but never changes them.
func TestSigningSecretRotation(t *testing.T) {
	s, f := newService(t)
	ctx := t.Context()
	first, st, err := s.GenerateSigningSecret(ctx, requester, "DSAAAAAAAAAAA1")
	if err != nil || !st.Set || st.PreviousActiveSince != nil || f.dests[0].previous.Set() {
		t.Fatalf("first %+v (%v)", st, err)
	}
	second, st, err := s.GenerateSigningSecret(ctx, requester, "DSAAAAAAAAAAA1")
	if err != nil || st.PreviousActiveSince == nil || !st.PreviousActiveSince.Equal(t0) || first == second {
		t.Fatalf("second %+v (%v)", st, err)
	}
	d := f.dests[0]
	prev, err := s.cfg.Keyring.OpenSecret(FieldPreviousSigningSecret, d.previous)
	if err != nil || prev != first {
		t.Fatalf("previous %v", err)
	}
	if status, err := s.SigningStatus(ctx, "DSAAAAAAAAAAA1"); err != nil || !status.Set ||
		status.PreviousActiveSince == nil || !status.UpdatedAt.Equal(t0) {
		t.Fatalf("status %+v (%v)", status, err)
	}
	tgt, err := s.target(ctx, 1)
	if err != nil || len(tgt.signing) != 2 || tgt.signing[0] != second || tgt.signing[1] != first {
		t.Fatalf("target signs with %d (%v)", len(tgt.signing), err)
	}
	for _, e := range f.audit {
		for _, v := range []logging.Secret{first, second} {
			if strings.Contains(string(e.Diff), string(v)) {
				t.Fatal("the audit log carries a signing secret")
			}
		}
	}
	if f.audit[0].Action != ActionSigningSecretGenerated {
		t.Errorf("action %s", f.audit[0].Action)
	}
	if err := s.RetirePreviousSigningSecret(ctx, requester, "DSAAAAAAAAAAA1"); err != nil || d.previous.Set() ||
		f.audit[len(f.audit)-1].Action != ActionSigningSecretRetired {
		t.Fatalf("retire %v", err)
	}
	n := len(f.audit)
	if err := s.RetirePreviousSigningSecret(ctx, requester, "DSAAAAAAAAAAA1"); err != nil || len(f.audit) != n {
		t.Fatalf("retire again %v", err)
	}

	// A new master key re-encrypts the stored value; the Signing secret it opens to, and its signature, stay.
	rotated := activeKeyring(t, 'n', 'k')
	plain, err := s.cfg.Keyring.OpenSecret(FieldSigningSecret, d.signing)
	if err != nil {
		t.Fatal(err)
	}
	again, _, err := rotated.ApplySecret(FieldSigningSecret, keyring.StoredSecret{}, keyring.Replace(plain), t0)
	if err != nil || again.KeyID == d.signing.KeyID {
		t.Fatalf("re-encrypt %v", err)
	}
	reopened, err := rotated.OpenSecret(FieldSigningSecret, again)
	sig1, _ := Sign([]logging.Secret{second}, "msg_1", t0, nil)
	sig2, _ := Sign([]logging.Secret{reopened}, "msg_1", t0, nil)
	if err != nil || reopened != second || sig1 != sig2 {
		t.Fatal("key rotation changed the signing secret")
	}

	for id, want := range map[string]error{"DSAAAAAAAAAAA2": ErrNotWebhook, "DSAAAAAAAAAAA9": ErrNotFound,
		"x": ErrNotFound} {
		if _, err := s.SigningStatus(ctx, id); !errors.Is(err, want) {
			t.Errorf("status %s = %v", id, err)
		}
		if _, _, err := s.GenerateSigningSecret(ctx, requester, id); !errors.Is(err, want) {
			t.Errorf("generate %s = %v", id, err)
		}
		if err := s.RetirePreviousSigningSecret(ctx, requester, id); !errors.Is(err, want) {
			t.Errorf("retire %s = %v", id, err)
		}
	}
	for _, name := range []string{"RotateSigningSecret", "RetirePreviousSigningSecret", "LockWebhookDestination"} {
		f.fail[name] = errBoom
		_, _, err1 := s.GenerateSigningSecret(ctx, requester, "DSAAAAAAAAAAA1")
		d.previous = d.signing
		err2 := s.RetirePreviousSigningSecret(ctx, requester, "DSAAAAAAAAAAA1")
		if !errors.Is(err1, errBoom) && !errors.Is(err2, errBoom) {
			t.Errorf("%s did not fail", name)
		}
		delete(f.fail, name)
	}
	d.signing.Ciphertext = []byte("garbage-garbage-garbage-garbage")
	if _, _, err := s.GenerateSigningSecret(ctx, requester, "DSAAAAAAAAAAA1"); err == nil {
		t.Error("generated over a signing secret that does not open")
	}
}

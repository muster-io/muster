// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package keyring

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestApplySecretKeepsClearsAndReplaces(t *testing.T) {
	k := newKeyring(t, 'a')
	k = active(t, k, k.KeyIDs()[0])
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.FixedZone("x", 3600))
	const field = "oidc_settings.client_secret"

	empty := StoredSecret{}
	if got, changed, err := k.ApplySecret(field, empty, Keep, now); err != nil || changed || got.Set() {
		t.Fatalf("keep of an empty field = %+v, %v, %v", got, changed, err)
	}
	if got, changed, err := k.ApplySecret(field, empty, Clear, now); err != nil || changed || got.Set() {
		t.Fatalf("clear of an empty field = %+v, %v, %v", got, changed, err)
	}

	stored, changed, err := k.ApplySecret(field, empty, Replace("s3cr3t"), now)
	if err != nil || !changed || !stored.Set() {
		t.Fatalf("replace = %+v, %v, %v", stored, changed, err)
	}
	if bytes.Contains(stored.Ciphertext, []byte("s3cr3t")) || stored.KeyID != k.KeyIDs()[0] {
		t.Fatalf("the stored field is not encrypted with the active key: %+v", stored)
	}
	if !stored.UpdatedAt.Equal(now) || stored.UpdatedAt.Location() != time.UTC {
		t.Fatalf("updated_at = %v, want %v in UTC", stored.UpdatedAt, now)
	}
	if v, err := k.OpenSecret(field, stored); err != nil || v != "s3cr3t" {
		t.Fatalf("open = %q, %v", v, err)
	}
	if _, err := k.OpenSecret("oidc_settings.proxy_password", stored); err == nil {
		t.Fatal("the ciphertext opened for another field")
	}
	st := stored.Status()
	if !st.Set || st.UpdatedAt == nil || !st.UpdatedAt.Equal(now) {
		t.Fatalf("status = %+v", st)
	}

	kept, changed, err := k.ApplySecret(field, stored, Keep, now.Add(time.Hour))
	if err != nil || changed || !bytes.Equal(kept.Ciphertext, stored.Ciphertext) || !kept.UpdatedAt.Equal(now) {
		t.Fatalf("keep = %+v, %v, %v", kept, changed, err)
	}
	again, changed, err := k.ApplySecret(field, stored, Replace("s3cr3t"), now.Add(time.Hour))
	if err != nil || !changed || bytes.Equal(again.Ciphertext, stored.Ciphertext) {
		t.Fatalf("a replacement with the same value = %+v, %v, %v; want a new ciphertext", again, changed, err)
	}
	cleared, changed, err := k.ApplySecret(field, stored, Clear, now.Add(2*time.Hour))
	if err != nil || !changed || cleared.Set() || !cleared.UpdatedAt.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("clear = %+v, %v, %v", cleared, changed, err)
	}
	if st := cleared.Status(); st.Set || st.UpdatedAt == nil {
		t.Fatalf("status after clear = %+v", st)
	}
	if v, err := k.OpenSecret(field, cleared); err != nil || v != "" {
		t.Fatalf("open of a cleared field = %q, %v", v, err)
	}
	if _, _, err := k.ApplySecret(field, stored, Replace(""), now); !errors.Is(err, ErrEmptySecret) {
		t.Fatalf("an empty value = %v, want ErrEmptySecret", err)
	}
}

func TestApplySecretNeedsAnActiveKey(t *testing.T) {
	k := newKeyring(t, 'a')
	if _, _, err := k.ApplySecret("f", StoredSecret{}, Replace("v"), time.Now()); !errors.Is(err, ErrNoActiveKey) {
		t.Fatalf("err = %v, want ErrNoActiveKey", err)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package webhooks

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/webhooks/dbgen"
)

// The Signing secret (C-15.FR-5) in the style of the Standard Webhooks specification: whsec_ and 32 random bytes in
// base64; the HMAC-SHA256 key is the decoded bytes. Each request carries webhook-id, webhook-timestamp (Unix seconds of
// the real clock) and webhook-signature, `v1,` and the base64 HMAC of "{id}.{timestamp}.{body}", one per secret that
// still signs, separated by a space.
const (
	SigningSecretPrefix = "whsec_"
	signingKeyBytes     = 32
	signatureVersion    = "v1"
)

// The headers of a signed request.
const (
	HeaderID        = "webhook-id"
	HeaderTimestamp = "webhook-timestamp"
	HeaderSignature = "webhook-signature"
)

// The keyring fields of the secrets of an outgoing webhook (encrypted_values).
const (
	FieldSigningSecret         = "destinations.signing_secret"
	FieldPreviousSigningSecret = "destinations.previous_signing_secret"
	FieldSecret                = "destination_secrets.value"
	FieldProxyPassword         = "destinations.proxy_password"
)

// The Audit log actions of the Signing secret.
const (
	ActionSigningSecretGenerated = "destination.signing_secret_generated"
	ActionSigningSecretRetired   = "destination.signing_secret_retired"
)

// NewSigningSecret is a new random Signing secret.
func NewSigningSecret() logging.Secret {
	b := make([]byte, signingKeyBytes)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	return logging.Secret(SigningSecretPrefix + base64.StdEncoding.EncodeToString(b))
}

// signingKey is the HMAC key of a Signing secret: the base64 after its prefix.
func signingKey(secret logging.Secret) ([]byte, error) {
	s, ok := strings.CutPrefix(string(secret), SigningSecretPrefix)
	if !ok {
		return nil, errors.New("the signing secret does not start with " + SigningSecretPrefix)
	}
	key, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(key) == 0 {
		return nil, errors.New("the signing secret is not base64")
	}
	return key, nil
}

// signature is the base64 HMAC-SHA256 of "{id}.{timestamp}.{body}" with key.
func signature(key []byte, id, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + timestamp + "."))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// Sign is the value of webhook-signature for the request id sent at the Unix time at with body: a v1 signature with
// each secret, in order, separated by a space.
func Sign(secrets []logging.Secret, id string, at time.Time, body []byte) (string, error) {
	timestamp := strconv.FormatInt(at.Unix(), 10)
	parts := make([]string, 0, len(secrets))
	for _, s := range secrets {
		key, err := signingKey(s)
		if err != nil {
			return "", err
		}
		parts = append(parts, signatureVersion+","+signature(key, id, timestamp, body))
	}
	if len(parts) == 0 {
		return "", errors.New("the destination has no signing secret")
	}
	return strings.Join(parts, " "), nil
}

// Verify reports whether one of the signatures of header, the value of webhook-signature, is the v1 signature of the
// request id sent at timestamp with body under secret; the comparison takes constant time. A receiving endpoint also refuses a
// timestamp far from their clock, which this does not check.
func Verify(secret logging.Secret, id, timestamp string, body []byte, header string) bool {
	key, err := signingKey(secret)
	if err != nil {
		return false
	}
	want := []byte(signature(key, id, timestamp, body))
	for part := range strings.FieldsSeq(header) {
		version, sig, ok := strings.Cut(part, ",")
		if ok && version == signatureVersion && hmac.Equal([]byte(sig), want) {
			return true
		}
	}
	return false
}

// SigningStatus is the status of the Signing secrets of a Destination (SigningSecretStatus).
type SigningStatus struct {
	Set                 bool
	UpdatedAt           *time.Time
	PreviousActiveSince *time.Time
}

// SigningStatus reads the status of the Signing secrets of the outgoing webhook publicID; another type is
// ErrNotWebhook.
func (s *Service) SigningStatus(ctx context.Context, publicID string) (SigningStatus, error) {
	d, err := s.destination(ctx, publicID)
	if err != nil {
		return SigningStatus{}, err
	}
	return SigningStatus{Set: d.SigningSecretSet, UpdatedAt: timeOf(d.SigningSecretUpdatedAt),
		PreviousActiveSince: timeOf(d.PreviousSigningSecretSince)}, nil
}

// GenerateSigningSecret makes a new Signing secret for the outgoing webhook publicID (C-15.FR-5): the current one
// becomes the previous one, which still signs until it is retired, and the new one is returned once. It is recorded as
// destination.signing_secret_generated.
func (s *Service) GenerateSigningSecret(ctx context.Context, r Requester, publicID string) (logging.Secret,
	SigningStatus, error) {
	secret := NewSigningSecret()
	var status SigningStatus
	err := s.inTx(ctx, publicID, func(q TxQueries, d dbgen.LockWebhookDestinationRow, now time.Time) error {
		current := keyring.StoredSecret{Ciphertext: d.SigningSecretCiphertext, KeyID: d.SigningSecretKeyID.String}
		var previous keyring.StoredSecret
		if current.Set() {
			plain, err := s.cfg.Keyring.OpenSecret(FieldSigningSecret, current)
			if err != nil {
				return fmt.Errorf("open the signing secret of %s: %w", d.PublicID, err)
			}
			if previous, _, err = s.cfg.Keyring.ApplySecret(FieldPreviousSigningSecret, previous,
				keyring.Replace(plain), now); err != nil {
				return err
			}
		}
		next, _, err := s.cfg.Keyring.ApplySecret(FieldSigningSecret, keyring.StoredSecret{}, keyring.Replace(secret),
			now)
		if err != nil {
			return err
		}
		p := dbgen.RotateSigningSecretParams{OrgID: s.orgID, ID: d.ID, Ciphertext: next.Ciphertext,
			KeyID: text(next.KeyID), Now: now}
		if previous.Set() {
			p.PreviousCiphertext, p.PreviousKeyID = previous.Ciphertext, text(previous.KeyID)
		}
		if _, err := q.RotateSigningSecret(ctx, p); err != nil {
			return fmt.Errorf("store the signing secret of %s: %w", d.PublicID, err)
		}
		status = SigningStatus{Set: true, UpdatedAt: &now}
		if previous.Set() {
			status.PreviousActiveSince = &now
		}
		return s.record(ctx, q, r, ActionSigningSecretGenerated, d.PublicID, d.Name,
			[]audit.Change{{Pointer: "/signing_secret", SecretChanged: true}})
	})
	if err != nil {
		return "", SigningStatus{}, err
	}
	return secret, status, nil
}

// RetirePreviousSigningSecret wipes the previous Signing secret of the outgoing webhook publicID, which signs no more
// (C-15.FR-5); it is recorded as destination.signing_secret_retired. Without a previous one it changes nothing.
func (s *Service) RetirePreviousSigningSecret(ctx context.Context, r Requester, publicID string) error {
	return s.inTx(ctx, publicID, func(q TxQueries, d dbgen.LockWebhookDestinationRow, now time.Time) error {
		_, err := q.RetirePreviousSigningSecret(ctx, dbgen.RetirePreviousSigningSecretParams{OrgID: s.orgID, ID: d.ID,
			Now: now})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("retire the previous signing secret of %s: %w", d.PublicID, err)
		}
		return s.record(ctx, q, r, ActionSigningSecretRetired, d.PublicID, d.Name,
			[]audit.Change{{Pointer: "/previous_signing_secret", SecretChanged: true}})
	})
}

// destination reads the outgoing webhook publicID that is not deleted.
func (s *Service) destination(ctx context.Context, publicID string) (dbgen.GetWebhookDestinationRow, error) {
	id, err := publicid.Parse(publicid.Destination, publicID)
	if err != nil {
		return dbgen.GetWebhookDestinationRow{}, ErrNotFound
	}
	d, err := s.cfg.Store.GetWebhookDestination(ctx, dbgen.GetWebhookDestinationParams{OrgID: s.orgID, PublicID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	if err != nil {
		return d, fmt.Errorf("read the destination %s: %w", id, err)
	}
	if d.Type != TypeWebhook {
		return d, ErrNotWebhook
	}
	return d, nil
}

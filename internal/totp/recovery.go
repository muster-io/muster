// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package totp

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"time"

	"github.com/muster-io/muster/internal/totp/dbgen"
)

// RecoveryCodes is auth.totp_recovery_codes (P-08): how many single-use recovery codes an enrolment issues.
const RecoveryCodes = 10

// A recovery code is ten characters of the alphabet below, about 50 bits, shown in two groups of five
// ("7k2mq-x9c4d"). The alphabet has no i, l, o or u, so that a code read aloud or typed from paper is not confused.
const (
	recoveryAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"
	recoveryLength   = 10
	recoveryGroup    = 5
)

// newRecoveryCodes returns RecoveryCodes new codes and their argon2id hashes.
func (s *Service) newRecoveryCodes(ctx context.Context) ([]string, []string, error) {
	codes := make([]string, RecoveryCodes)
	hashes := make([]string, RecoveryCodes)
	for i := range codes {
		raw := make([]byte, recoveryLength)
		_, _ = rand.Read(raw) // crypto/rand.Read never fails
		var b strings.Builder
		for j, c := range raw {
			if j == recoveryGroup {
				b.WriteByte('-')
			}
			b.WriteByte(recoveryAlphabet[int(c)%len(recoveryAlphabet)])
		}
		codes[i] = b.String()
		h, err := s.hash(ctx, normalizeRecovery(codes[i]))
		if err != nil {
			return nil, nil, fmt.Errorf("hash a recovery code: %w", err)
		}
		hashes[i] = h
	}
	return codes, hashes, nil
}

// normalizeRecovery is a recovery code as it is hashed: lowercase, without spaces and dashes.
func normalizeRecovery(code string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '-' || r == ' ' || r == '\t':
			return -1
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		}
		return r
	}, code)
}

// useRecoveryCode checks code against the unused recovery codes of the user and uses up the one it matches; another
// request that uses the same code first wins. A text of another length costs no hashing.
func (s *Service) useRecoveryCode(ctx context.Context, userID int64, code string) (bool, error) {
	code = normalizeRecovery(code)
	if len(code) != recoveryLength {
		return false, nil
	}
	rows, err := s.store.ListUnusedRecoveryCodes(ctx, dbgen.ListUnusedRecoveryCodesParams{OrgID: s.orgID, UserID: userID})
	if err != nil {
		return false, fmt.Errorf("read the recovery codes: %w", err)
	}
	for _, r := range rows {
		ok, err := s.verifyHash(ctx, r.CodeHash, code)
		if err != nil {
			return false, fmt.Errorf("check a recovery code: %w", err)
		}
		if !ok {
			continue
		}
		n, err := s.store.UseRecoveryCode(ctx, dbgen.UseRecoveryCodeParams{
			Now: s.business.Now().UTC(), OrgID: s.orgID, ID: r.ID,
		})
		if err != nil {
			return false, fmt.Errorf("use a recovery code: %w", err)
		}
		return n == 1, nil
	}
	return false, nil
}

// replaceRecoveryCodes deletes the unused recovery codes of a user and stores the new hashes.
func (s *Service) replaceRecoveryCodes(ctx context.Context, q Queries, userID int64, hashes []string,
	now time.Time) error {
	if _, err := q.DeleteUnusedRecoveryCodes(ctx, dbgen.DeleteUnusedRecoveryCodesParams{
		OrgID: s.orgID, UserID: userID,
	}); err != nil {
		return fmt.Errorf("remove the unused recovery codes: %w", err)
	}
	for _, h := range hashes {
		if err := q.InsertRecoveryCode(ctx, dbgen.InsertRecoveryCodeParams{
			OrgID: s.orgID, UserID: userID, CodeHash: h, Now: now,
		}); err != nil {
			return fmt.Errorf("store a recovery code: %w", err)
		}
	}
	return nil
}

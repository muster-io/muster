// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/auth/dbgen"
)

// auth.signin_throttle: after ThrottleAfter consecutive failures of an account or a source address, each attempt
// waits twice as long as the previous one, from ThrottleFirstDelay up to ThrottleMaxDelay.
const (
	ThrottleAfter      = 3
	ThrottleFirstDelay = time.Second
	ThrottleMaxDelay   = 60 * time.Second
)

// The subjects that the throttle counts separately.
const (
	subjectAccount = "account"
	subjectAddress = "address"
)

// ThrottleDelay is how long the next attempt waits after failures consecutive failures: nothing before
// ThrottleAfter, then 1 s, 2 s, 4 s and so on up to 60 s.
func ThrottleDelay(failures int64) time.Duration {
	if failures < ThrottleAfter {
		return 0
	}
	d := ThrottleFirstDelay
	for range failures - ThrottleAfter {
		d *= 2
		if d >= ThrottleMaxDelay {
			return ThrottleMaxDelay
		}
	}
	return d
}

// accountSubject is the subject of a login: the SHA-256 of the lowercased login in hex, so that the key has a fixed
// size however long the login that was typed.
func accountSubject(login string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(login)))
	return hex.EncodeToString(sum[:])
}

// addressSubject is the subject of a source address: an IPv4 address, or the /64 network of an IPv6 address, which
// one client usually holds whole. An unknown address is not throttled on its own.
func addressSubject(addr netip.Addr) string {
	switch {
	case !addr.IsValid():
		return ""
	case addr.Is4():
		return addr.String()
	}
	return netip.PrefixFrom(addr, 64).Masked().String()
}

// throttled returns how long the account or the address must still wait before an attempt is evaluated.
func (s *Service) throttled(ctx context.Context, account, address string) (time.Duration, error) {
	rows, err := s.store.GetThrottles(ctx, dbgen.GetThrottlesParams{OrgID: s.orgID, Account: account, Address: address})
	if err != nil {
		return 0, fmt.Errorf("read the sign-in throttle: %w", err)
	}
	now := s.clock.Now()
	var wait time.Duration
	for _, r := range rows {
		if r.BlockedUntil.Valid {
			wait = max(wait, r.BlockedUntil.Time.Sub(now))
		}
	}
	return wait, nil
}

// recordFailure counts an evaluated failure for the account and the address and blocks each as its count says.
func (s *Service) recordFailure(ctx context.Context, account, address string) error {
	now := s.clock.Now().UTC()
	return s.store.InTx(ctx, func(q Queries) error {
		for _, subj := range []struct{ kind, value string }{{subjectAccount, account}, {subjectAddress, address}} {
			if subj.value == "" {
				continue
			}
			n, err := q.RecordSignInFailure(ctx, dbgen.RecordSignInFailureParams{
				OrgID: s.orgID, SubjectKind: subj.kind, Subject: subj.value, Now: now,
			})
			if err != nil {
				return fmt.Errorf("count the failed sign-in: %w", err)
			}
			d := ThrottleDelay(n)
			if d == 0 {
				continue
			}
			until := now.Add(d)
			if err := q.BlockSignIn(ctx, dbgen.BlockSignInParams{
				OrgID: s.orgID, SubjectKind: subj.kind, Subject: subj.value, BlockedUntil: timestamptz(until),
			}); err != nil {
				return fmt.Errorf("throttle the sign-in: %w", err)
			}
		}
		return nil
	})
}

func timestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

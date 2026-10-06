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

// attemptLockClass is the first key of the attempt locks of accounts (TryLockSignInSubject).
const attemptLockClass = 0x6d75_0001

// attempt evaluates one attempt of an account and a source address — a password, a TOTP code or a recovery code —
// under the throttle. It runs in a transaction that holds the account's attempt lock, so that concurrent attempts of
// an account are never evaluated at once: each one sees the failures of the ones before, and a burst cannot slip past
// a block. An attempt that finds the lock taken, or comes before the throttle allows one, is a *ThrottledError and
// check does not run. A failed check is counted for the account and the address before the lock goes; attempt then
// returns false.
func (s *Service) attempt(ctx context.Context, account, address string,
	check func(context.Context) (bool, error)) (bool, error) {
	ok := false
	err := s.store.InTx(ctx, func(q Queries) error {
		locked, err := q.TryLockSignInSubject(ctx, dbgen.TryLockSignInSubjectParams{
			LockClass: attemptLockClass, Subject: account,
		})
		if err != nil {
			return fmt.Errorf("lock the sign-in attempts: %w", err)
		}
		if !locked {
			return &ThrottledError{RetryAfter: ThrottleFirstDelay}
		}
		if wait, err := s.throttled(ctx, q, account, address); err != nil {
			return err
		} else if wait > 0 {
			return &ThrottledError{RetryAfter: wait}
		}
		if ok, err = check(ctx); err != nil || ok {
			return err
		}
		return s.recordFailure(ctx, q, account, address)
	})
	return ok, err
}

// throttled returns how long the account or the address must still wait before an attempt is evaluated.
func (s *Service) throttled(ctx context.Context, q Queries, account, address string) (time.Duration, error) {
	rows, err := q.GetThrottles(ctx, dbgen.GetThrottlesParams{OrgID: s.orgID, Account: account, Address: address})
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
func (s *Service) recordFailure(ctx context.Context, q Queries, account, address string) error {
	now := s.clock.Now().UTC()
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
}

func timestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

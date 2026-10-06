// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package tokens

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/tokens/dbgen"
)

// Authenticate returns the identity of a bearer token of the API (C-04.FR-4): a Personal access token acts as its
// owner with the token's Permissions intersected with the owner's current Role, a Service account token with its
// account's Role. The prefix gives the kind before the lookup by hash. A malformed, unknown, revoked or expired token,
// an Integration token, and a token whose owner is disabled or deleted are ErrInvalidToken; a Personal access token
// of an OIDC account that must sign in again is ErrOIDCRecheckRequired (C-04.FR-8); a token over api.rate_limit is a
// RateLimitedError. A request that passes records the use of the token and the client addr, at most once a minute.
func (s *Service) Authenticate(ctx context.Context, value string, addr netip.Addr) (*auth.Identity, error) {
	kind, ok := kindOf(value)
	if !ok {
		return nil, ErrInvalidToken
	}
	hash := Hash(value)
	row, err := s.store.GetTokenByHash(ctx, dbgen.GetTokenByHashParams{OrgID: s.orgID, Kind: kind, TokenHash: hash})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidToken
	}
	if err != nil {
		return nil, fmt.Errorf("find the token: %w", err)
	}
	if subtle.ConstantTimeCompare(row.TokenHash, hash) != 1 {
		return nil, ErrInvalidToken
	}
	now := s.clock.Now().UTC()
	if row.RevokedAt.Valid || (row.ExpiresAt.Valid && !now.Before(row.ExpiresAt.Time)) {
		return nil, ErrInvalidToken
	}
	id := &auth.Identity{Transport: audit.TransportAPI,
		Token: &auth.Token{ID: row.ID, PublicID: row.PublicID, Name: row.Name}}
	switch kind {
	case KindPersonal:
		if err := personalUsable(row, now); err != nil {
			return nil, err
		}
		id.Session.User = auth.Principal{ID: row.UserID.Int64, PublicID: row.UserPublicID.String,
			Name: row.UserName.String, Role: row.UserRole.String}
		id.Permissions = s.narrowed(row.UserRole.String, row.Permissions)
	default:
		if !row.ServiceAccountID.Valid || row.ServiceAccountStatus.String != StatusActive {
			return nil, ErrInvalidToken
		}
		id.Token.ServiceAccount = &auth.Principal{ID: row.ServiceAccountID.Int64,
			PublicID: row.ServiceAccountPublicID.String, Name: row.ServiceAccountName.String,
			Role: row.ServiceAccountRole.String}
		id.Permissions = s.roles.Permissions(row.ServiceAccountRole.String)
	}
	if s.limiter != nil {
		if err := s.limiter.Allow(row.ID); err != nil {
			return nil, err
		}
	}
	if !row.LastUsedAt.Valid || now.Sub(row.LastUsedAt.Time) >= touchInterval {
		if err := s.store.TouchToken(ctx, dbgen.TouchTokenParams{
			OrgID: s.orgID, ID: row.ID, Now: now, Address: address(addr), StaleBefore: now.Add(-touchInterval),
		}); err != nil {
			return nil, fmt.Errorf("record the use of the token %s: %w", row.PublicID, err)
		}
	}
	return id, nil
}

// personalUsable decides whether a Personal access token of an active token row may act for its owner now: the owner
// is active, and an owner who signs in through OIDC was not refused by the identity provider and, without an offline
// token, signed in through OIDC within auth.oidc_token_grace (C-04.FR-8, C-03.FR-30). The token is never revoked for
// it: the owner's next OIDC sign-in makes it work again.
func personalUsable(row dbgen.GetTokenByHashRow, now time.Time) error {
	if !row.UserID.Valid || row.UserStatus.String != StatusActive {
		return ErrInvalidToken
	}
	if !row.UserOidc {
		return nil
	}
	if row.UserOidcRefusedAt.Valid {
		return ErrOIDCRecheckRequired
	}
	if row.UserOfflineToken {
		return nil
	}
	grace := time.Duration(row.OidcTokenGraceSeconds) * time.Second
	if !row.UserOidcLastContactAt.Valid || now.Sub(row.UserOidcLastContactAt.Time) > grace {
		return ErrOIDCRecheckRequired
	}
	return nil
}

// narrowed are the effective Permissions of a Personal access token: those it was narrowed to that its owner's Role
// holds now, so that a lower Role shrinks the token with it (C-04.FR-1).
func (s *Service) narrowed(role string, perms []string) []auth.Permission {
	out := []auth.Permission{}
	for _, p := range perms {
		if s.roles.Has(role, auth.Permission(p)) {
			out = append(out, auth.Permission(p))
		}
	}
	slices.Sort(out)
	return out
}

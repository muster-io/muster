// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package tokens

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/tokens/dbgen"
)

// Token is a Personal access token or a Service account token as the API lists it; its value is never kept.
type Token struct {
	ID              int64
	PublicID        string
	Name            string
	ExpiresAt       *time.Time
	CreatedAt       time.Time
	LastUsedAt      *time.Time
	LastUsedAddress netip.Addr
	// Permissions are those a Personal access token is narrowed to; a Service account token has its account's Role.
	Permissions []auth.Permission
}

// Created is a token that was just issued, with its value, which is shown this once.
type Created struct {
	Token Token
	Value string
}

// Owner is the user a Personal access token belongs to.
type Owner struct {
	ID       int64
	PublicID string
}

// NewPersonal is a Personal access token to issue: its name, the Permissions it is narrowed to and its optional expiry
// (token.expiry).
type NewPersonal struct {
	Name        string
	Permissions []auth.Permission
	ExpiresAt   *time.Time
}

// ListPersonal lists the Personal access tokens of the user ownerID that are not revoked, expired ones included, the
// newest first.
func (s *Service) ListPersonal(ctx context.Context, ownerID int64) ([]Token, error) {
	rows, err := s.store.ListUserTokens(ctx, dbgen.ListUserTokensParams{OrgID: s.orgID, UserID: pgtype.Int8{
		Int64: ownerID, Valid: true}})
	if err != nil {
		return nil, fmt.Errorf("list the personal access tokens: %w", err)
	}
	out := make([]Token, 0, len(rows))
	for _, r := range rows {
		perms := make([]auth.Permission, len(r.Permissions))
		for i, p := range r.Permissions {
			perms[i] = auth.Permission(p)
		}
		out = append(out, Token{ID: r.ID, PublicID: r.PublicID, Name: r.Name, ExpiresAt: timeOf(r.ExpiresAt),
			CreatedAt: r.CreatedAt.UTC(), LastUsedAt: timeOf(r.LastUsedAt), LastUsedAddress: addressOf(r.LastUsedAddress),
			Permissions: perms})
	}
	return out, nil
}

// CreatePersonal issues a Personal access token of owner, narrowed to n.Permissions, each of which the owner must
// hold now: held are the owner's Permissions at this request (C-04.FR-7). A Permission not held is a FieldError
// permission_not_held at /permissions/<i>. The value is returned once and only its hash is stored.
func (s *Service) CreatePersonal(ctx context.Context, r Requester, owner Owner, held []auth.Permission,
	n NewPersonal) (Created, error) {
	if err := checkName("/name", n.Name); err != nil {
		return Created{}, err
	}
	var perms []auth.Permission
	for i, p := range n.Permissions {
		if !slices.Contains(held, p) {
			return Created{}, &FieldError{Pointer: "/permissions/" + strconv.Itoa(i), Code: CodePermissionNotHeld,
				Detail: "You do not hold the Permission " + string(p) + "."}
		}
		if !slices.Contains(perms, p) {
			perms = append(perms, p)
		}
	}
	if len(perms) == 0 {
		return Created{}, &FieldError{Pointer: "/permissions", Code: CodeInvalidFormat,
			Detail: "A token needs at least one Permission."}
	}
	slices.Sort(perms)
	now := s.clock.Now().UTC()
	if err := checkExpiry(n.ExpiresAt, now); err != nil {
		return Created{}, err
	}
	value, hash, err := Generate(PrefixPersonal)
	if err != nil {
		return Created{}, err
	}
	t := Token{PublicID: publicid.New(publicid.PersonalToken), Name: n.Name, ExpiresAt: utc(n.ExpiresAt),
		CreatedAt: now, Permissions: perms}
	names := make([]string, len(perms))
	for i, p := range perms {
		names[i] = string(p)
	}
	err = s.store.InTx(ctx, func(q Queries) error {
		var err error
		t.ID, err = q.InsertToken(ctx, dbgen.InsertTokenParams{
			OrgID: s.orgID, PublicID: t.PublicID, Kind: KindPersonal, UserID: pgtype.Int8{Int64: owner.ID, Valid: true},
			Name: t.Name, TokenHash: hash, ExpiresAt: timestamptz(t.ExpiresAt), CreatedAt: now,
		})
		if err != nil {
			return fmt.Errorf("store the personal access token: %w", err)
		}
		if err := q.InsertTokenPermissions(ctx, dbgen.InsertTokenPermissionsParams{
			ApiTokenID: t.ID, OrgID: s.orgID, Permissions: names,
		}); err != nil {
			return fmt.Errorf("store the permissions of the personal access token: %w", err)
		}
		return s.audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport, Action: audit.ActionAPITokenCreated,
			Resource: audit.Resource{Type: audit.ResourceAPIToken, PublicID: t.PublicID, Name: t.Name},
			Details: map[string]any{"kind": KindPersonal, "owner": owner.PublicID, "permissions": names,
				"expires_at": expiryDetail(t.ExpiresAt)},
			SourceAddress: r.Address,
		})
	})
	if err != nil {
		return Created{}, err
	}
	return Created{Token: t, Value: value}, nil
}

// RevokePersonal revokes the Personal access token publicID of the user owner at once; a token of another user, an
// unknown or an already revoked one is ErrNotFound.
func (s *Service) RevokePersonal(ctx context.Context, r Requester, owner Owner, publicID string) error {
	id, err := publicid.Parse(publicid.PersonalToken, publicID)
	if err != nil {
		return ErrNotFound
	}
	return s.store.InTx(ctx, func(q Queries) error {
		row, err := q.RevokeUserToken(ctx, dbgen.RevokeUserTokenParams{
			OrgID: s.orgID, UserID: pgtype.Int8{Int64: owner.ID, Valid: true}, PublicID: id, Now: s.clock.Now().UTC(),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("revoke the personal access token %s: %w", id, err)
		}
		return s.audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport, Action: audit.ActionAPITokenRevoked,
			Resource:      audit.Resource{Type: audit.ResourceAPIToken, PublicID: id, Name: row.Name},
			Details:       map[string]any{"kind": KindPersonal, "owner": owner.PublicID, "reason": "revoked"},
			SourceAddress: r.Address,
		})
	})
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := t.UTC()
	return &v
}

// expiryDetail is the expiry as the Audit log details show it; null when the token never expires.
func expiryDetail(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Format(time.RFC3339)
}

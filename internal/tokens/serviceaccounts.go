// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package tokens

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/tokens/dbgen"
)

// The statuses of a Service account; a deleted one is never shown.
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
	StatusDeleted  = "deleted"
)

// ServiceAccount is a non-person identity with a Role and tokens (C-04.FR-2). It never signs in to the UI, has no
// profile and no Account links, and never becomes the Owner of an Alert Group.
type ServiceAccount struct {
	ID         int64
	PublicID   string
	Name       string
	Role       string
	Status     string
	TokenCount int64
	CreatedAt  time.Time
	Version    int64
}

// ServiceAccountInput is the name and the Role of a Service account.
type ServiceAccountInput struct {
	Name string
	Role string
}

// ListFilter selects a page of Service accounts, in the order they were created, after the id After when it is set.
type ListFilter struct {
	After *int64
	Limit int
}

// Page is a page of Service accounts; Next, the id to continue after, is nil on the last page.
type Page struct {
	ServiceAccounts []ServiceAccount
	Next            *int64
}

// NewToken is a Service account token to issue, with its optional expiry (token.expiry).
type NewToken struct {
	Name      string
	ExpiresAt *time.Time
}

// ListServiceAccounts lists the Service accounts that are not deleted.
func (s *Service) ListServiceAccounts(ctx context.Context, f ListFilter) (Page, error) {
	limit := max(f.Limit, 1)
	p := dbgen.ListServiceAccountsParams{OrgID: s.orgID, PageSize: int32(min(limit, 1000)) + 1}
	if f.After != nil {
		p.AfterID = pgtype.Int8{Int64: *f.After, Valid: true}
	}
	rows, err := s.store.ListServiceAccounts(ctx, p)
	if err != nil {
		return Page{}, fmt.Errorf("list the service accounts: %w", err)
	}
	var page Page
	for i, r := range rows {
		if i == limit {
			last := page.ServiceAccounts[limit-1].ID
			page.Next = &last
			break
		}
		page.ServiceAccounts = append(page.ServiceAccounts, ServiceAccount{ID: r.ID, PublicID: r.PublicID,
			Name: r.Name, Role: r.Role, Status: r.Status, TokenCount: r.TokenCount, CreatedAt: r.CreatedAt.UTC(),
			Version: r.Version})
	}
	return page, nil
}

// GetServiceAccount reads the Service account publicID; a deleted or unknown one is ErrNotFound.
func (s *Service) GetServiceAccount(ctx context.Context, publicID string) (ServiceAccount, error) {
	return s.get(ctx, s.store, publicID)
}

func (s *Service) get(ctx context.Context, q Queries, publicID string) (ServiceAccount, error) {
	id, err := publicid.Parse(publicid.ServiceAccount, publicID)
	if err != nil {
		return ServiceAccount{}, ErrNotFound
	}
	r, err := q.GetServiceAccount(ctx, dbgen.GetServiceAccountParams{OrgID: s.orgID, PublicID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return ServiceAccount{}, ErrNotFound
	}
	if err != nil {
		return ServiceAccount{}, fmt.Errorf("read the service account %s: %w", id, err)
	}
	return ServiceAccount{ID: r.ID, PublicID: r.PublicID, Name: r.Name, Role: r.Role, Status: r.Status,
		TokenCount: r.TokenCount, CreatedAt: r.CreatedAt.UTC(), Version: r.Version}, nil
}

// lock locks the Service account publicID for a change and reads it.
func (s *Service) lock(ctx context.Context, q Queries, publicID string) (ServiceAccount, error) {
	id, err := publicid.Parse(publicid.ServiceAccount, publicID)
	if err != nil {
		return ServiceAccount{}, ErrNotFound
	}
	if _, err := q.LockServiceAccount(ctx, dbgen.LockServiceAccountParams{OrgID: s.orgID, PublicID: id}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ServiceAccount{}, ErrNotFound
		}
		return ServiceAccount{}, fmt.Errorf("lock the service account %s: %w", id, err)
	}
	return s.get(ctx, q, id)
}

func (s *Service) checkInput(in ServiceAccountInput) error {
	if err := checkName("/name", in.Name); err != nil {
		return err
	}
	if !slices.Contains(auth.RoleNames, in.Role) || len(s.roles.Permissions(in.Role)) == 0 {
		return &FieldError{Pointer: "/role", Code: CodeInvalidFormat, Detail: "No such Role."}
	}
	return nil
}

// nameTaken maps the refusal of the unique index of the names to ErrNameTaken.
func nameTaken(err error) error {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == uniqueViolation &&
		pgErr.ConstraintName == nameIndex {
		return ErrNameTaken
	}
	return err
}

// resourceOf is a Service account as the Audit log names it.
func resourceOf(sa ServiceAccount) audit.Resource {
	return audit.Resource{Type: audit.ResourceServiceAccount, PublicID: sa.PublicID, Name: sa.Name}
}

// CreateServiceAccount creates an active Service account; a name another one has is ErrNameTaken.
func (s *Service) CreateServiceAccount(ctx context.Context, r Requester, in ServiceAccountInput) (ServiceAccount,
	error) {
	if err := s.checkInput(in); err != nil {
		return ServiceAccount{}, err
	}
	var created ServiceAccount
	err := s.store.InTx(ctx, func(q Queries) error {
		id := publicid.New(publicid.ServiceAccount)
		if _, err := q.InsertServiceAccount(ctx, dbgen.InsertServiceAccountParams{
			OrgID: s.orgID, PublicID: id, Name: in.Name, Role: in.Role, Now: s.clock.Now().UTC(),
		}); err != nil {
			return fmt.Errorf("create the service account: %w", nameTaken(err))
		}
		var err error
		if created, err = s.get(ctx, q, id); err != nil {
			return err
		}
		return s.audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport, Action: audit.ActionServiceAccountCreated,
			Resource: resourceOf(created), Diff: audit.Created(viewOf(created)),
			SourceAddress: r.Address,
		})
	})
	if err != nil {
		return ServiceAccount{}, err
	}
	return created, nil
}

// UpdateServiceAccount changes the name and the Role of the Service account publicID; a non-nil version must be its
// current one (If-Match).
func (s *Service) UpdateServiceAccount(ctx context.Context, r Requester, publicID string, version *int64,
	in ServiceAccountInput) (ServiceAccount, error) {
	if err := s.checkInput(in); err != nil {
		return ServiceAccount{}, err
	}
	var updated ServiceAccount
	err := s.store.InTx(ctx, func(q Queries) error {
		before, err := s.lock(ctx, q, publicID)
		if err != nil {
			return err
		}
		if version != nil && *version != before.Version {
			return ErrVersionMismatch
		}
		if before.Name == in.Name && before.Role == in.Role {
			updated = before
			return nil
		}
		if err := q.UpdateServiceAccount(ctx, dbgen.UpdateServiceAccountParams{
			OrgID: s.orgID, ID: before.ID, Name: in.Name, Role: in.Role, Now: s.clock.Now().UTC(),
		}); err != nil {
			return fmt.Errorf("update the service account %s: %w", before.PublicID, nameTaken(err))
		}
		if updated, err = s.get(ctx, q, before.PublicID); err != nil {
			return err
		}
		return s.audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport, Action: audit.ActionServiceAccountUpdated,
			Resource: resourceOf(updated), Diff: audit.Diff(viewOf(before), viewOf(updated)), SourceAddress: r.Address,
		})
	})
	if err != nil {
		return ServiceAccount{}, err
	}
	return updated, nil
}

// DisableServiceAccount disables the Service account publicID: its tokens answer 401 until it is enabled again. A
// disabled one is returned unchanged.
func (s *Service) DisableServiceAccount(ctx context.Context, r Requester, publicID string) (ServiceAccount, error) {
	return s.setStatus(ctx, r, publicID, StatusDisabled)
}

// EnableServiceAccount enables the Service account publicID again, and its tokens work again. An active one is
// returned unchanged.
func (s *Service) EnableServiceAccount(ctx context.Context, r Requester, publicID string) (ServiceAccount, error) {
	return s.setStatus(ctx, r, publicID, StatusActive)
}

func (s *Service) setStatus(ctx context.Context, r Requester, publicID, status string) (ServiceAccount, error) {
	var changed ServiceAccount
	err := s.store.InTx(ctx, func(q Queries) error {
		before, err := s.lock(ctx, q, publicID)
		if err != nil {
			return err
		}
		if before.Status == status {
			changed = before
			return nil
		}
		if err := q.SetServiceAccountStatus(ctx, dbgen.SetServiceAccountStatusParams{
			OrgID: s.orgID, ID: before.ID, Status: status, Now: s.clock.Now().UTC(),
		}); err != nil {
			return fmt.Errorf("set the status of the service account %s: %w", before.PublicID, err)
		}
		if changed, err = s.get(ctx, q, before.PublicID); err != nil {
			return err
		}
		action := audit.ActionServiceAccountEnabled
		if status == StatusDisabled {
			action = audit.ActionServiceAccountDisabled
		}
		return s.audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport, Action: action, Resource: resourceOf(changed),
			Diff: audit.Diff(viewOf(before), viewOf(changed)), SourceAddress: r.Address,
		})
	})
	if err != nil {
		return ServiceAccount{}, err
	}
	return changed, nil
}

// DeleteServiceAccount deletes the Service account publicID and revokes its tokens with the reason owner_deleted; a
// non-nil version must be its current one. The row stays, so that the Audit log still names it.
func (s *Service) DeleteServiceAccount(ctx context.Context, r Requester, publicID string, version *int64) error {
	return s.store.InTx(ctx, func(q Queries) error {
		before, err := s.lock(ctx, q, publicID)
		if err != nil {
			return err
		}
		if version != nil && *version != before.Version {
			return ErrVersionMismatch
		}
		now := s.clock.Now().UTC()
		if err := q.DeleteServiceAccount(ctx, dbgen.DeleteServiceAccountParams{OrgID: s.orgID, ID: before.ID,
			Now: now}); err != nil {
			return fmt.Errorf("delete the service account %s: %w", before.PublicID, err)
		}
		revoked, err := q.RevokeServiceAccountTokens(ctx, dbgen.RevokeServiceAccountTokensParams{
			OrgID: s.orgID, ServiceAccountID: pgtype.Int8{Int64: before.ID, Valid: true}, Now: now,
		})
		if err != nil {
			return fmt.Errorf("revoke the tokens of the service account %s: %w", before.PublicID, err)
		}
		return s.audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport, Action: audit.ActionServiceAccountDeleted,
			Resource: resourceOf(before),
			Diff:     []audit.Change{{Pointer: "/status", Before: before.Status, After: StatusDeleted}},
			Details:  map[string]any{"tokens_revoked": revoked}, SourceAddress: r.Address,
		})
	})
}

// ListServiceAccountTokens lists the tokens of the Service account publicID that are not revoked, the newest first.
func (s *Service) ListServiceAccountTokens(ctx context.Context, publicID string) ([]Token, error) {
	sa, err := s.get(ctx, s.store, publicID)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListServiceAccountTokens(ctx, dbgen.ListServiceAccountTokensParams{
		OrgID: s.orgID, ServiceAccountID: pgtype.Int8{Int64: sa.ID, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("list the tokens of the service account %s: %w", sa.PublicID, err)
	}
	out := make([]Token, 0, len(rows))
	for _, r := range rows {
		out = append(out, Token{ID: r.ID, PublicID: r.PublicID, Name: r.Name, ExpiresAt: timeOf(r.ExpiresAt),
			CreatedAt: r.CreatedAt.UTC(), LastUsedAt: timeOf(r.LastUsedAt), LastUsedAddress: addressOf(r.LastUsedAddress)})
	}
	return out, nil
}

// CreateServiceAccountToken issues a token of the Service account publicID; the value is returned once and only its
// hash is stored. A disabled account gets tokens too; they work once it is enabled.
func (s *Service) CreateServiceAccountToken(ctx context.Context, r Requester, publicID string, n NewToken) (Created,
	error) {
	if err := checkName("/name", n.Name); err != nil {
		return Created{}, err
	}
	now := s.clock.Now().UTC()
	if err := checkExpiry(n.ExpiresAt, now); err != nil {
		return Created{}, err
	}
	value, hash, err := Generate(PrefixServiceAccount)
	if err != nil {
		return Created{}, err
	}
	t := Token{PublicID: publicid.New(publicid.ServiceToken), Name: n.Name, ExpiresAt: utc(n.ExpiresAt), CreatedAt: now}
	err = s.store.InTx(ctx, func(q Queries) error {
		sa, err := s.lock(ctx, q, publicID)
		if err != nil {
			return err
		}
		if t.ID, err = q.InsertToken(ctx, dbgen.InsertTokenParams{
			OrgID: s.orgID, PublicID: t.PublicID, Kind: KindServiceAccount,
			ServiceAccountID: pgtype.Int8{Int64: sa.ID, Valid: true}, Name: t.Name, TokenHash: hash,
			ExpiresAt: timestamptz(t.ExpiresAt), CreatedAt: now,
		}); err != nil {
			return fmt.Errorf("store the token of the service account %s: %w", sa.PublicID, err)
		}
		return s.audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport, Action: audit.ActionAPITokenCreated,
			Resource: audit.Resource{Type: audit.ResourceAPIToken, PublicID: t.PublicID, Name: t.Name},
			Details: map[string]any{"kind": KindServiceAccount, "service_account": sa.PublicID,
				"expires_at": expiryDetail(t.ExpiresAt)},
			SourceAddress: r.Address,
		})
	})
	if err != nil {
		return Created{}, err
	}
	return Created{Token: t, Value: value}, nil
}

// RevokeServiceAccountToken revokes the token tokenID of the Service account publicID at once; a token of another
// account, an unknown or an already revoked one is ErrNotFound.
func (s *Service) RevokeServiceAccountToken(ctx context.Context, r Requester, publicID, tokenID string) error {
	tid, err := publicid.Parse(publicid.ServiceToken, tokenID)
	if err != nil {
		return ErrNotFound
	}
	return s.store.InTx(ctx, func(q Queries) error {
		sa, err := s.lock(ctx, q, publicID)
		if err != nil {
			return err
		}
		row, err := q.RevokeServiceAccountToken(ctx, dbgen.RevokeServiceAccountTokenParams{
			OrgID: s.orgID, ServiceAccountID: pgtype.Int8{Int64: sa.ID, Valid: true}, PublicID: tid,
			Now: s.clock.Now().UTC(),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("revoke the token %s: %w", tid, err)
		}
		return s.audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport, Action: audit.ActionAPITokenRevoked,
			Resource: audit.Resource{Type: audit.ResourceAPIToken, PublicID: tid, Name: row.Name},
			Details: map[string]any{"kind": KindServiceAccount, "service_account": sa.PublicID,
				"reason": "revoked"},
			SourceAddress: r.Address,
		})
	})
}

// view is a Service account as its Audit log diff shows it.
type view struct {
	Name   string `json:"name"`
	Role   string `json:"role"`
	Status string `json:"status"`
}

func viewOf(sa ServiceAccount) view {
	return view{Name: sa.Name, Role: sa.Role, Status: sa.Status}
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package users

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/users/dbgen"
)

const (
	// PasswordSetupLinkTTL is auth.password_setup_link_ttl (P-05): a link sets a password once, within 24 hours.
	PasswordSetupLinkTTL = 24 * time.Hour
	// PasswordSetupPruneAfter is auth.password_setup_prune_after: a link is deleted this long after it expired, used
	// and superseded links included, so that a late click still answers link_used or link_expired instead of an
	// unknown token for a week.
	PasswordSetupPruneAfter = 7 * 24 * time.Hour
	// setupTokenBytes is the length of a setup token: 32 random bytes, stored only as their SHA-256.
	setupTokenBytes = 32
	// SetupPath is the page of the SPA that completes a link; the token follows in the URL fragment.
	SetupPath = "password-setup"
)

var (
	// ErrLinkNotFound is a token that names no password setup link.
	ErrLinkNotFound = errors.New("no such password setup link")
	// ErrLinkExpired is a link past auth.password_setup_link_ttl.
	ErrLinkExpired = errors.New("the password setup link expired")
	// ErrLinkUsed is a link that set a password already or that a newer link replaced.
	ErrLinkUsed = errors.New("the password setup link was used or replaced")
)

// SetupLink is a single-use password setup link: MUSTER_PUBLIC_URL/password-setup#token=<token>.
type SetupLink struct {
	URL       string
	ExpiresAt time.Time
}

// CreateSetupLink issues a new password setup link for the user id; it supersedes the user's older links. An account
// that signs in through OIDC has no password to set (auth.ErrNotLocal).
func (a *Admin) CreateSetupLink(ctx context.Context, r Requester, id string) (SetupLink, error) {
	var link SetupLink
	err := a.store.InTx(ctx, func(q AdminQueries) error {
		_, u, err := a.lock(ctx, q, id, false)
		if err != nil {
			return err
		}
		if u.HasOIDCIdentity {
			return auth.ErrNotLocal
		}
		if link, err = a.issueLink(ctx, q, r, u); err != nil {
			return err
		}
		return a.audit.Record(ctx, q, audit.Entry{
			OrgID: a.orgID, Actor: r.Actor, Transport: r.Transport, Action: audit.ActionPasswordSetupLink,
			Resource: resourceOf(u), Details: map[string]any{"expires_at": link.ExpiresAt.Format(time.RFC3339)},
			SourceAddress: r.Address,
		})
	})
	if err != nil {
		return SetupLink{}, err
	}
	return link, nil
}

// issueLink supersedes the open links of u and stores a new one; only the hash of its token is kept.
func (a *Admin) issueLink(ctx context.Context, q AdminQueries, r Requester, u User) (SetupLink, error) {
	token := make([]byte, setupTokenBytes)
	if _, err := rand.Read(token); err != nil {
		return SetupLink{}, fmt.Errorf("make a password setup token: %w", err)
	}
	now := a.clock.Now().UTC()
	if _, err := q.SupersedePasswordSetups(ctx, dbgen.SupersedePasswordSetupsParams{
		OrgID: a.orgID, UserID: u.ID, Now: now,
	}); err != nil {
		return SetupLink{}, fmt.Errorf("supersede the password setup links of %s: %w", u.PublicID, err)
	}
	hash := sha256.Sum256(token)
	p := dbgen.InsertPasswordSetupParams{
		OrgID: a.orgID, UserID: u.ID, TokenHash: hash[:], CreatedAt: now, ExpiresAt: now.Add(PasswordSetupLinkTTL),
	}
	if r.Actor.Kind == audit.ActorUser {
		p.CreatedByUserID = pgtype.Int8{Int64: r.Actor.ID, Valid: true}
	}
	if err := q.InsertPasswordSetup(ctx, p); err != nil {
		return SetupLink{}, fmt.Errorf("store the password setup link of %s: %w", u.PublicID, err)
	}
	u2 := a.publicURL.JoinPath(SetupPath)
	u2.RawQuery = ""
	u2.Fragment = "token=" + base64.RawURLEncoding.EncodeToString(token)
	return SetupLink{URL: u2.String(), ExpiresAt: p.ExpiresAt}, nil
}

// CompleteSetup sets a password from the token of a setup link (C-03.FR-26): an unknown token is ErrLinkNotFound, a
// used or superseded link ErrLinkUsed and an expired one ErrLinkExpired, checked in that order on the business clock;
// a password shorter than auth.password_min_length is a FieldError. None of them changes anything. The link is used
// once, the user's sessions end, and the Audit log records user.password_set by the system, since the person
// holding the link is not signed in. The password is hashed before the transaction, which locks the user and then the
// link, in the order of every other change of a user and its links, and checks the link again.
func (a *Admin) CompleteSetup(ctx context.Context, token, password string, addr netip.Addr) error {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != setupTokenBytes {
		return ErrLinkNotFound
	}
	hash := sha256.Sum256(raw)
	link, err := a.usableLink(ctx, a.store, hash[:])
	if err != nil {
		return err
	}
	if errors.Is(auth.CheckPasswordLength(password), auth.ErrPasswordTooShort) {
		return &FieldError{Pointer: "/password", Code: "too_short",
			Detail: "The password is shorter than " + strconv.Itoa(auth.PasswordMinLength) + " characters."}
	}
	passwordHash, err := auth.HashPassword(ctx, password)
	if err != nil {
		return err
	}
	return a.store.InTx(ctx, func(q AdminQueries) error {
		if _, err := q.LockUser(ctx, dbgen.LockUserParams{OrgID: a.orgID, PublicID: link.UserPublicID}); err != nil {
			return fmt.Errorf("lock the user of the password setup link: %w", err)
		}
		if link, err = a.usableLink(ctx, q, hash[:]); err != nil {
			return err
		}
		now := a.clock.Now().UTC()
		if _, err := q.MarkPasswordSetupUsed(ctx, dbgen.MarkPasswordSetupUsedParams{
			OrgID: a.orgID, ID: link.ID, Now: now,
		}); err != nil {
			return fmt.Errorf("use the password setup link: %w", err)
		}
		n, err := q.SetUserPassword(ctx, dbgen.SetUserPasswordParams{
			OrgID: a.orgID, ID: link.UserID, PasswordHash: pgtype.Text{String: passwordHash, Valid: true}, Now: now,
		})
		if err != nil {
			return fmt.Errorf("set the password of %s: %w", link.UserPublicID, err)
		}
		if n == 0 { // deleted, or the account signs in through OIDC since the link was issued
			return ErrLinkUsed
		}
		u := User{ID: link.UserID, PublicID: link.UserPublicID, Name: link.UserName}
		ended, err := a.endSessions(ctx, q, u, auth.EndPasswordChanged, now)
		if err != nil {
			return err
		}
		return a.audit.Record(ctx, q, audit.Entry{
			OrgID: a.orgID, Actor: audit.System, Transport: audit.TransportUI, Action: audit.ActionPasswordSet,
			Resource: resourceOf(u), Diff: []audit.Change{{Pointer: "/password", SecretChanged: true}},
			Details: map[string]any{"sessions_ended": ended}, SourceAddress: addr,
		})
	})
}

// usableLink reads the link of a token hash and refuses it when it is unknown, used, superseded or expired; inside a
// transaction the read locks the link.
func (a *Admin) usableLink(ctx context.Context, q AdminQueries, hash []byte) (dbgen.GetPasswordSetupRow, error) {
	link, err := q.GetPasswordSetup(ctx, dbgen.GetPasswordSetupParams{OrgID: a.orgID, TokenHash: hash})
	if errors.Is(err, pgx.ErrNoRows) {
		return link, ErrLinkNotFound
	}
	if err != nil {
		return link, fmt.Errorf("find the password setup link: %w", err)
	}
	switch {
	case link.UsedAt.Valid || link.SupersededAt.Valid:
		return link, ErrLinkUsed
	case !a.clock.Now().Before(link.ExpiresAt):
		return link, ErrLinkExpired
	}
	return link, nil
}

// ResetPassword is `muster admin reset-password` (C-03.FR-11): it sets password on the account whose login is login,
// compared lowercased — an account that signs in through OIDC included, whose identity and offline token it removes in
// the same update, with its re-check, so that the account never holds both — ends its sessions, supersedes its
// password setup links and records user.password_reset by the CLI actor named actor, with whether an identity was
// removed. It returns the public_id of the account. A deleted or unknown account is ErrNotFound.
func (a *Admin) ResetPassword(ctx context.Context, actor, login, password string) (string, error) {
	if actor == "" {
		return "", errors.New("the reset needs the --actor name")
	}
	if err := auth.CheckPasswordLength(password); err != nil {
		return "", err
	}
	row, err := a.store.GetUserByLogin(ctx, dbgen.GetUserByLoginParams{OrgID: a.orgID, Login: login})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && row.Status == StatusDeleted) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("find the account: %w", err)
	}
	passwordHash, err := auth.HashPassword(ctx, password)
	if err != nil {
		return "", err
	}
	return row.PublicID, a.store.InTx(ctx, func(q AdminQueries) error {
		_, before, err := a.lock(ctx, q, row.PublicID, false)
		if err != nil {
			return err
		}
		now := a.clock.Now().UTC()
		n, err := q.ResetUserPassword(ctx, dbgen.ResetUserPasswordParams{
			OrgID: a.orgID, ID: before.ID, PasswordHash: pgtype.Text{String: passwordHash, Valid: true}, Now: now,
		})
		if err != nil {
			return fmt.Errorf("set the password of %s: %w", before.PublicID, err)
		}
		if n == 0 {
			return ErrNotFound
		}
		if before.HasOIDCIdentity {
			if err := a.dropCheck(ctx, q, before); err != nil {
				return err
			}
		}
		ended, err := a.endSessions(ctx, q, before, auth.EndPasswordChanged, now)
		if err != nil {
			return err
		}
		if _, err := q.SupersedePasswordSetups(ctx, dbgen.SupersedePasswordSetupsParams{
			OrgID: a.orgID, UserID: before.ID, Now: now,
		}); err != nil {
			return fmt.Errorf("supersede the password setup links of %s: %w", before.PublicID, err)
		}
		diff := []audit.Change{{Pointer: "/password", SecretChanged: true}}
		if before.HasOIDCIdentity {
			diff = append(diff, audit.Change{Pointer: "/sign_in_method", Before: auth.MethodOIDC,
				After: auth.MethodLocal})
		}
		return a.audit.Record(ctx, q, audit.Entry{
			OrgID: a.orgID, Actor: audit.CLI(actor), Transport: audit.TransportCLI, Action: audit.ActionPasswordReset,
			Resource: resourceOf(before), Diff: diff,
			Details: map[string]any{"sessions_ended": ended, "oidc_identity_removed": before.HasOIDCIdentity},
		})
	})
}

// PruneQueries are the deletes of short-lived pruning; *dbgen.Queries implements them.
type PruneQueries interface {
	PrunePasswordSetups(ctx context.Context, arg dbgen.PrunePasswordSetupsParams) (int64, error)
}

// Pruner deletes the password setup links that expired PasswordSetupPruneAfter ago, for the short_lived_pruning
// Leader task.
type Pruner struct {
	q PruneQueries
}

// NewPruner returns the Pruner over q.
func NewPruner(q PruneQueries) Pruner {
	return Pruner{q: q}
}

// PasswordSetups deletes at most limit links of the Organization that expired PasswordSetupPruneAfter before now.
func (p Pruner) PasswordSetups(ctx context.Context, orgID int64, now time.Time, limit int32) (int64, error) {
	n, err := p.q.PrunePasswordSetups(ctx, dbgen.PrunePasswordSetupsParams{
		OrgID: orgID, Before: now.Add(-PasswordSetupPruneAfter), BatchSize: limit,
	})
	if err != nil {
		return 0, fmt.Errorf("delete expired password setup links: %w", err)
	}
	return n, nil
}

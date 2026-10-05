// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package users

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/users/dbgen"
)

// The bootstrap variables (C-02.FR-1).
const (
	BootstrapEmailVar        = "MUSTER_BOOTSTRAP_ADMIN_EMAIL"
	BootstrapPasswordVar     = "MUSTER_BOOTSTRAP_ADMIN_PASSWORD"
	BootstrapPasswordFileVar = "MUSTER_BOOTSTRAP_ADMIN_PASSWORD_FILE"
)

// Bootstrap is the bootstrap Admin the environment asks for; an empty Email asks for none.
type Bootstrap struct {
	Email    string
	Password logging.Secret
}

// EnsureBootstrapAdmin is the start-up step of C-03.FR-22. When the Organization has no Admin and Email is set, it
// creates a local Admin whose login and email are Email, whose name is the part of Email before @ and whose password
// is Password, which must be at least auth.password_min_length long, and records user.created by the actor bootstrap.
// When an Admin exists the variables are ignored with bootstrap_admin_ignored; with neither an Admin nor the
// variables it logs bootstrap_admin_missing. now is the business time.
func EnsureBootstrapAdmin(ctx context.Context, s Store, w *audit.Writer, log *logging.Logger, orgID int64,
	b Bootstrap, now time.Time) error {
	admins, err := s.CountAdmins(ctx, orgID)
	if err != nil {
		return fmt.Errorf("count the admins: %w", err)
	}
	if admins > 0 {
		if set := b.variables(); len(set) > 0 {
			log.Log(ctx, logging.BootstrapAdminIgnored, logging.F("variables", set))
		}
		return nil
	}
	if b.Email == "" {
		log.Log(ctx, logging.BootstrapAdminMissing)
		return nil
	}
	if b.Password == "" {
		return fmt.Errorf("%s is set but %s (or %s) is not: the bootstrap Admin needs a password",
			BootstrapEmailVar, BootstrapPasswordVar, BootstrapPasswordFileVar)
	}
	if err := auth.CheckPasswordLength(string(b.Password)); err != nil {
		return fmt.Errorf("%s (or %s): the bootstrap Admin's password is shorter than %d characters",
			BootstrapPasswordVar, BootstrapPasswordFileVar, auth.PasswordMinLength)
	}
	hash, err := auth.HashPassword(ctx, string(b.Password))
	if err != nil {
		return err
	}
	name, _, _ := strings.Cut(b.Email, "@")
	id := publicid.New(publicid.User)
	return s.InTx(ctx, func(q Queries) error {
		_, err := q.CreateUser(ctx, dbgen.CreateUserParams{
			OrgID: orgID, PublicID: id, Login: b.Email, Name: name, Email: pgtype.Text{String: b.Email, Valid: true},
			Role: auth.RoleAdmin, Source: "bootstrap", PasswordHash: pgtype.Text{String: hash, Valid: true},
			PasswordChangedAt: pgtype.Timestamptz{Time: now, Valid: true}, CreatedAt: now,
		})
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23505" {
			return fmt.Errorf("%s: a user with the login %s exists but is not an Admin", BootstrapEmailVar, b.Email)
		}
		if err != nil {
			return fmt.Errorf("create the bootstrap Admin: %w", err)
		}
		return w.Record(ctx, q, audit.Entry{
			OrgID: orgID, Actor: audit.Bootstrap, Transport: audit.TransportSystem, Action: audit.ActionUserCreated,
			Resource: audit.Resource{Type: audit.ResourceUser, PublicID: id, Name: name},
			Diff: []audit.Change{
				{Pointer: "/login", After: b.Email}, {Pointer: "/email", After: b.Email}, {Pointer: "/name", After: name},
				{Pointer: "/role", After: auth.RoleAdmin}, {Pointer: "/password", SecretChanged: true},
			},
			Details: map[string]any{"source": "bootstrap"},
		})
	})
}

// variables are the bootstrap variables that are set.
func (b Bootstrap) variables() []string {
	var set []string
	if b.Email != "" {
		set = append(set, BootstrapEmailVar)
	}
	if b.Password != "" {
		set = append(set, BootstrapPasswordVar+" (or "+BootstrapPasswordFileVar+")")
	}
	return set
}

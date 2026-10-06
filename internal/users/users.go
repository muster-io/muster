// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package users holds the Users of the Organization (C-03): reading a user, the profile a user edits — display name,
// time zone and language — the start-up step that creates the bootstrap Admin, the administration of users by Admins
// with the rule that the Organization keeps an active Admin, the single-use password setup links and the emergency
// password reset of the CLI.
package users

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	_ "time/tzdata" // IANA time zones in any image, not only where the host ships them

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/users/dbgen"
)

// The languages of the UI.
var Languages = []string{"en", "ru"}

// maxNameLength bounds a display name, in characters.
const maxNameLength = 200

// ErrNotFound is a user that does not exist in the Organization.
var ErrNotFound = errors.New("no such user")

// FieldError is a profile field that is not valid, at a JSON pointer of the request body, with a stable code of
// the validation-failed problem.
type FieldError struct {
	Pointer string
	Code    string
	Detail  string
}

func (e *FieldError) Error() string {
	return e.Pointer + ": " + e.Detail
}

// User is a user as the API shows it.
type User struct {
	ID              int64
	PublicID        string
	Login           string
	Name            string
	Email           string
	Role            string
	Source          string
	Status          string
	HasPassword     bool
	HasOIDCIdentity bool
	OfflineAccess   bool
	TOTPEnabled     bool
	TimeZone        *string
	Language        *string
	LastSignInAt    *time.Time
	CreatedAt       time.Time
	Version         int64
}

// SignInMethod is how the user signs in now: oidc with an identity, local otherwise.
func (u User) SignInMethod() string {
	if u.HasOIDCIdentity {
		return "oidc"
	}
	return "local"
}

// ETag is the entity tag of the user's version.
func (u User) ETag() string {
	return fmt.Sprintf(`"%d"`, u.Version)
}

// Queries are the queries of the package, with the insert of the Audit log.
type Queries interface {
	GetUser(ctx context.Context, arg dbgen.GetUserParams) (dbgen.GetUserRow, error)
	UpdateProfile(ctx context.Context, arg dbgen.UpdateProfileParams) (int64, error)
	CountAdmins(ctx context.Context, orgID int64) (int64, error)
	CreateUser(ctx context.Context, arg dbgen.CreateUserParams) (int64, error)
	audit.Store
}

// Store runs the queries alone or in one transaction.
type Store interface {
	Queries
	InTx(ctx context.Context, f func(Queries) error) error
}

// NewStore is the Store over the main pool.
func NewStore(pool *pgxpool.Pool) Store {
	return pgStore{pgQueries: pgQueries{Queries: dbgen.New(pool), Store: audit.NewStore(pool)}, pool: pool}
}

type pgQueries struct {
	*dbgen.Queries
	audit.Store
}

type pgStore struct {
	pgQueries
	pool *pgxpool.Pool
}

func (s pgStore) InTx(ctx context.Context, f func(Queries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return f(pgQueries{Queries: dbgen.New(tx), Store: audit.NewStore(tx)})
	})
}

// Service reads users and changes profiles in the Organization.
type Service struct {
	orgID int64
	store Store
	audit *audit.Writer
	clock clock.Clock
}

// NewService returns the Service of the Organization orgID; clock is the business clock.
func NewService(orgID int64, s Store, w *audit.Writer, business clock.Clock) *Service {
	return &Service{orgID: orgID, store: s, audit: w, clock: business}
}

// Get returns the user with the internal id.
func (s *Service) Get(ctx context.Context, id int64) (User, error) {
	return get(ctx, s.store, s.orgID, id)
}

// userReader reads a user by its internal id.
type userReader interface {
	GetUser(ctx context.Context, arg dbgen.GetUserParams) (dbgen.GetUserRow, error)
}

func get(ctx context.Context, q userReader, orgID, id int64) (User, error) {
	row, err := q.GetUser(ctx, dbgen.GetUserParams{OrgID: orgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("read the user: %w", err)
	}
	return userOf(row), nil
}

// userOf is the user of a row of the users table.
func userOf(row dbgen.GetUserRow) User {
	u := User{
		ID: row.ID, PublicID: row.PublicID, Login: row.Login, Name: row.Name, Email: row.Email.String, Role: row.Role,
		Source: row.Source, Status: row.Status, HasPassword: row.HasPassword, HasOIDCIdentity: row.HasOidcIdentity,
		OfflineAccess: row.HasOfflineToken, TOTPEnabled: row.TotpEnabled, TimeZone: textPtr(row.TimeZone),
		Language: textPtr(row.Language), CreatedAt: row.CreatedAt, Version: row.Version,
	}
	if row.LastSignInAt.Valid {
		t := row.LastSignInAt.Time
		u.LastSignInAt = &t
	}
	return u
}

// Profile is what a user edits about themselves; a nil time zone or language follows the browser.
type Profile struct {
	Name     string
	TimeZone *string
	Language *string
}

// Validate checks the profile: a name of 1 to 200 characters, an IANA time zone and a language of the UI.
func (p Profile) Validate() error {
	switch n := utf8.RuneCountInString(strings.TrimSpace(p.Name)); {
	case n == 0:
		return &FieldError{Pointer: "/name", Code: "required", Detail: "The name is empty."}
	case utf8.RuneCountInString(p.Name) > maxNameLength:
		return &FieldError{Pointer: "/name", Code: "too_long",
			Detail: fmt.Sprintf("The name is longer than %d characters.", maxNameLength)}
	}
	if p.TimeZone != nil && !validTimeZone(*p.TimeZone) {
		return &FieldError{Pointer: "/time_zone", Code: "invalid_format", Detail: "The time zone is not an IANA name."}
	}
	if p.Language != nil && !slices.Contains(Languages, *p.Language) {
		return &FieldError{Pointer: "/language", Code: "invalid_format", Detail: "The language is not en or ru."}
	}
	return nil
}

func validTimeZone(name string) bool {
	if name == "" || name == "Local" || len(name) > 64 {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

// UpdateProfile changes the profile of the user id, which is the actor, and records user.profile_updated with the
// diff of what changed.
func (s *Service) UpdateProfile(ctx context.Context, id int64, actor audit.Actor, p Profile,
	addr netip.Addr) (User, error) {
	if err := p.Validate(); err != nil {
		return User{}, err
	}
	var updated User
	err := s.store.InTx(ctx, func(q Queries) error {
		before, err := get(ctx, q, s.orgID, id)
		if err != nil {
			return err
		}
		n, err := q.UpdateProfile(ctx, dbgen.UpdateProfileParams{
			OrgID: s.orgID, ID: id, Name: strings.TrimSpace(p.Name), TimeZone: optText(p.TimeZone), Language: optText(p.Language),
			UpdatedAt: s.clock.Now().UTC(),
		})
		if err != nil {
			return fmt.Errorf("update the profile of %s: %w", before.PublicID, err)
		}
		if n == 0 {
			return ErrNotFound
		}
		if updated, err = get(ctx, q, s.orgID, id); err != nil {
			return err
		}
		var diff []audit.Change
		diff = append(diff, audit.Changed("/name", before.Name, updated.Name)...)
		diff = append(diff, audit.Changed("/time_zone", deref(before.TimeZone), deref(updated.TimeZone))...)
		diff = append(diff, audit.Changed("/language", deref(before.Language), deref(updated.Language))...)
		return s.audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: actor, Transport: audit.TransportUI, Action: audit.ActionProfileUpdated,
			Resource: audit.Resource{Type: audit.ResourceUser, PublicID: updated.PublicID, Name: updated.Name},
			Diff:     diff, SourceAddress: addr,
		})
	})
	if err != nil {
		return User{}, err
	}
	return updated, nil
}

func textPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func optText(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *s, Valid: true}
}

// deref is the value of s, or nil for JSON when s is nil.
func deref(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

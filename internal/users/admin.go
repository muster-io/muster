// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package users

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/users/dbgen"
)

// The statuses of a user.
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
	StatusDeleted  = "deleted"
)

// ActionConvertedToLocal is the Audit log action of an OIDC account an Admin converted back to local.
const ActionConvertedToLocal = "user.converted_to_local"

// SourceLocal is a user an Admin created.
const SourceLocal = "local"

// DeletedPrefix starts the name and the login of a deleted user, deleted-user-<public_id>; no login may start with it.
const DeletedPrefix = "deleted-user-"

const (
	maxLoginLength = 200
	maxEmailLength = 254
	// uniqueViolation is the SQLSTATE of a unique index that refused a row, and loginIndex the index of the logins.
	uniqueViolation = "23505"
	loginIndex      = "users_login_key"
)

var (
	// ErrNameTaken is a login that another user has, compared lowercased.
	ErrNameTaken = errors.New("another user has this login")
	// ErrLastAdmin is a change that would leave the Organization without an active Admin (C-03.FR-31).
	ErrLastAdmin = errors.New("the last active Admin cannot be disabled, deleted or given a lower Role")
	// ErrVersionMismatch is an If-Match that names another version of the user.
	ErrVersionMismatch = errors.New("the user changed since it was read")
	// ErrRoleLocked is a Role change of an account that signs in through OIDC while OIDC and oidc.sync_role are both
	// on: the identity provider decides its Role (C-03.FR-29).
	ErrRoleLocked = errors.New("the identity provider decides the Role of this account")
	// ErrNotLinked is a conversion to local of an account that does not sign in through OIDC.
	ErrNotLinked = errors.New("the account does not sign in through OIDC")
)

// AdminQueries are the queries of user administration, with the insert of the Audit log.
type AdminQueries interface {
	GetUser(ctx context.Context, arg dbgen.GetUserParams) (dbgen.GetUserRow, error)
	GetUserByPublicID(ctx context.Context, arg dbgen.GetUserByPublicIDParams) (dbgen.GetUserByPublicIDRow, error)
	GetUserByLogin(ctx context.Context, arg dbgen.GetUserByLoginParams) (dbgen.GetUserByLoginRow, error)
	ListUsers(ctx context.Context, arg dbgen.ListUsersParams) ([]dbgen.ListUsersRow, error)
	LockActiveAdmins(ctx context.Context, orgID int64) ([]int64, error)
	LockUser(ctx context.Context, arg dbgen.LockUserParams) (int64, error)
	CreateUser(ctx context.Context, arg dbgen.CreateUserParams) (int64, error)
	UpdateUser(ctx context.Context, arg dbgen.UpdateUserParams) (int64, error)
	SetUserStatus(ctx context.Context, arg dbgen.SetUserStatusParams) (int64, error)
	PseudonymizeUser(ctx context.Context, arg dbgen.PseudonymizeUserParams) (int64, error)
	SetUserPassword(ctx context.Context, arg dbgen.SetUserPasswordParams) (int64, error)
	EndSessionsOfUser(ctx context.Context, arg dbgen.EndSessionsOfUserParams) (int64, error)
	RevokeTokensOfUser(ctx context.Context, arg dbgen.RevokeTokensOfUserParams) (int64, error)
	RoleSyncOn(ctx context.Context, orgID int64) (bool, error)
	ConvertToLocal(ctx context.Context, arg dbgen.ConvertToLocalParams) (int64, error)
	ResetUserPassword(ctx context.Context, arg dbgen.ResetUserPasswordParams) (int64, error)
	DeleteOIDCCheck(ctx context.Context, arg dbgen.DeleteOIDCCheckParams) error
	InsertPasswordSetup(ctx context.Context, arg dbgen.InsertPasswordSetupParams) error
	SupersedePasswordSetups(ctx context.Context, arg dbgen.SupersedePasswordSetupsParams) (int64, error)
	GetPasswordSetup(ctx context.Context, arg dbgen.GetPasswordSetupParams) (dbgen.GetPasswordSetupRow, error)
	MarkPasswordSetupUsed(ctx context.Context, arg dbgen.MarkPasswordSetupUsedParams) (int64, error)
	ListUserDirectory(ctx context.Context, arg dbgen.ListUserDirectoryParams) ([]dbgen.ListUserDirectoryRow, error)
	// Tx is the connection the queries run on: inside InTx, its transaction, which the OwnerReleaser joins.
	Tx() dbgen.DBTX
	audit.Store
}

// AdminStore runs the queries of user administration alone or in one transaction.
type AdminStore interface {
	AdminQueries
	InTx(ctx context.Context, f func(AdminQueries) error) error
}

// NewAdminStore is the AdminStore over the main pool.
func NewAdminStore(pool *pgxpool.Pool) AdminStore {
	return pgAdminStore{pgAdminQueries: adminQueriesOver(pool), pool: pool}
}

// pgAdminQueries are the queries over a pool or a transaction, which Tx returns.
type pgAdminQueries struct {
	pgQueries
	tx dbgen.DBTX
}

func adminQueriesOver(d dbgen.DBTX) pgAdminQueries {
	return pgAdminQueries{pgQueries: pgQueries{Queries: dbgen.New(d), Store: audit.NewStore(d)}, tx: d}
}

func (q pgAdminQueries) Tx() dbgen.DBTX { return q.tx }

type pgAdminStore struct {
	pgAdminQueries
	pool *pgxpool.Pool
}

func (s pgAdminStore) InTx(ctx context.Context, f func(AdminQueries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return f(adminQueriesOver(tx)) })
}

// The reasons of the release of an Owner, as OwnerReleaser takes them (C-03.FR-13).
const (
	ReleaseDisabled = "owner_disabled"
	ReleaseDeleted  = "owner_deleted"
)

// OwnerReleaser releases the acknowledgements of a disabled or deleted user (C-03.FR-13): each acknowledged Alert Group
// the user owns becomes firing without an Owner. Admin calls it inside the transaction tx of the disable or the
// delete, after the status change and before the Audit log entry, with the reason ReleaseDisabled or ReleaseDeleted;
// an error rolls the change back. It returns what to run once tx committed, or nil. The Alert Group lifecycle
// implements it (ADR-0016: the interface is declared by its consumer, so users never imports groups).
type OwnerReleaser interface {
	ReleaseOwner(ctx context.Context, tx dbgen.DBTX, userID int64, reason string) (func(context.Context), error)
}

// Requester is who asks for an administrative change and how: the actor and the Transport of the Audit log entry, and
// the client address.
type Requester struct {
	Actor     audit.Actor
	Transport audit.Transport
	Address   netip.Addr
}

// Admin administers the users of the Organization (C-03.FR-3, FR-13, FR-31): every change is recorded in the Audit
// log with a before/after diff, in the transaction of the change.
type Admin struct {
	orgID     int64
	store     AdminStore
	audit     *audit.Writer
	clock     clock.Clock
	publicURL *url.URL
	releaser  OwnerReleaser
}

// NewAdmin returns the Admin of the Organization orgID; clock is the business clock and publicURL is
// MUSTER_PUBLIC_URL, the base of the password setup links.
func NewAdmin(orgID int64, s AdminStore, w *audit.Writer, business clock.Clock, publicURL *url.URL) *Admin {
	return &Admin{orgID: orgID, store: s, audit: w, clock: business, publicURL: publicURL}
}

// SetOwnerReleaser sets what releases the acknowledgements of the users that Disable and Delete disable or delete;
// the runtime sets it on the Admin the API uses. Only an Admin that never disables or deletes — the one of muster
// admin reset-password — goes without; one without releases nothing.
func (a *Admin) SetOwnerReleaser(r OwnerReleaser) { a.releaser = r }

// release runs the OwnerReleaser for the user u in the transaction of q and adds what it returns to committed.
func (a *Admin) release(ctx context.Context, q AdminQueries, u User, reason string,
	committed *[]func(context.Context)) error {
	if a.releaser == nil {
		return nil
	}
	after, err := a.releaser.ReleaseOwner(ctx, q.Tx(), u.ID, reason)
	if err != nil {
		return fmt.Errorf("release the alert groups of %s: %w", u.PublicID, err)
	}
	if after != nil {
		*committed = append(*committed, after)
	}
	return nil
}

// ListFilter selects users; empty fields do not filter. Q matches the name, the login and the email.
type ListFilter struct {
	Q      string
	Role   string
	Status string
	Source string
	After  *Cursor
	Limit  int
}

// Cursor is the sort key of the last user of a page: the lowercased name and the id.
type Cursor struct {
	Name string
	ID   int64
}

// Page is a page of users; Next is nil on the last page.
type Page struct {
	Users []User
	Next  *Cursor
}

// List returns a page of at most f.Limit users in the order of their name, deleted users included.
func (a *Admin) List(ctx context.Context, f ListFilter) (Page, error) {
	p := dbgen.ListUsersParams{
		OrgID: a.orgID, Q: optional(f.Q), Role: optional(f.Role), Status: optional(f.Status), Source: optional(f.Source),
		PageSize: int32(f.Limit + 1), //nolint:gosec // G115: the limit is at most 500
	}
	if f.After != nil {
		p.AfterName = pgtype.Text{String: f.After.Name, Valid: true}
		p.AfterID = pgtype.Int8{Int64: f.After.ID, Valid: true}
	}
	rows, err := a.store.ListUsers(ctx, p)
	if err != nil {
		return Page{}, fmt.Errorf("list the users: %w", err)
	}
	page := Page{Users: make([]User, 0, min(len(rows), f.Limit))}
	for i, row := range rows {
		if i == f.Limit {
			page.Next = &Cursor{Name: rows[i-1].SortName, ID: rows[i-1].ID}
			break
		}
		page.Users = append(page.Users, userOf(dbgen.GetUserRow{
			ID: row.ID, PublicID: row.PublicID, Login: row.Login, Name: row.Name, Email: row.Email, Role: row.Role,
			Source: row.Source, Status: row.Status, HasPassword: row.HasPassword, HasOidcIdentity: row.HasOidcIdentity,
			HasOfflineToken: row.HasOfflineToken, TimeZone: row.TimeZone, Language: row.Language,
			LastSignInAt: row.LastSignInAt, CreatedAt: row.CreatedAt, Version: row.Version, TotpEnabled: row.TotpEnabled,
			RoleLocked: row.RoleLocked,
		}))
	}
	return page, nil
}

// Get returns the user with the public_id id, deleted users included.
func (a *Admin) Get(ctx context.Context, id string) (User, error) {
	return byPublicID(ctx, a.store, a.orgID, id)
}

func byPublicID(ctx context.Context, q AdminQueries, orgID int64, id string) (User, error) {
	norm, err := publicid.Parse(publicid.User, id)
	if err != nil {
		return User{}, ErrNotFound
	}
	row, err := q.GetUserByPublicID(ctx, dbgen.GetUserByPublicIDParams{OrgID: orgID, PublicID: norm})
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("read the user: %w", err)
	}
	return userOf(dbgen.GetUserRow(row)), nil
}

// NewUser is a local user an Admin creates; a nil Email is none.
type NewUser struct {
	Name  string
	Login string
	Email *string
	Role  string
}

// Create creates a local user without a password and issues its first password setup link (C-03.FR-3). A login that
// another user has in any case is ErrNameTaken.
func (a *Admin) Create(ctx context.Context, r Requester, n NewUser) (User, SetupLink, error) {
	if err := validateFields(n.Name, n.Login, n.Email); err != nil {
		return User{}, SetupLink{}, err
	}
	email := normalizeEmail(n.Email)
	var (
		created User
		link    SetupLink
	)
	err := a.store.InTx(ctx, func(q AdminQueries) error {
		now := a.clock.Now().UTC()
		pid := publicid.New(publicid.User)
		id, err := q.CreateUser(ctx, dbgen.CreateUserParams{
			OrgID: a.orgID, PublicID: pid, Login: n.Login, Name: strings.TrimSpace(n.Name), Email: optText(email),
			Role: n.Role, Source: SourceLocal, CreatedAt: now,
		})
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == uniqueViolation &&
			pgErr.ConstraintName == loginIndex {
			return ErrNameTaken
		}
		if err != nil {
			return fmt.Errorf("create the user: %w", err)
		}
		if created, err = get(ctx, q, a.orgID, id); err != nil {
			return err
		}
		if link, err = a.issueLink(ctx, q, r, created); err != nil {
			return err
		}
		return a.audit.Record(ctx, q, audit.Entry{
			OrgID: a.orgID, Actor: r.Actor, Transport: r.Transport, Action: audit.ActionUserCreated,
			Resource: resourceOf(created), Diff: audit.Created(viewOf(created)),
			Details: map[string]any{"source": SourceLocal}, SourceAddress: r.Address,
		})
	})
	if err != nil {
		return User{}, SetupLink{}, err
	}
	return created, link, nil
}

// Changes are what an Admin edits on a user; EmailSet false keeps the email, and a nil Email with EmailSet clears it.
type Changes struct {
	Name     string
	Email    *string
	EmailSet bool
	Role     string
}

// Update changes the name, the email and the Role of the user id at version, the version its If-Match names, or at
// any version when version is nil. A Role change ends the user's sessions; giving the last active Admin a lower Role
// is ErrLastAdmin, and changing the Role of an account that signs in through OIDC while OIDC and oidc.sync_role are
// both on is ErrRoleLocked. Nothing changes, and nothing is recorded, when the values are the ones the user has.
func (a *Admin) Update(ctx context.Context, r Requester, id string, version *int64, c Changes) (User, error) {
	var updated User
	err := a.store.InTx(ctx, func(q AdminQueries) error {
		admins, before, err := a.lock(ctx, q, id, true)
		if err != nil {
			return err
		}
		if version != nil && before.Version != *version {
			return ErrVersionMismatch
		}
		email := optionalPtr(before.Email)
		if c.EmailSet {
			email = c.Email
		}
		if err := validateNameAndEmail(c.Name, email); err != nil {
			return err
		}
		email = normalizeEmail(email)
		after := before
		after.Name, after.Email, after.Role = strings.TrimSpace(c.Name), derefString(email), c.Role
		diff := audit.Diff(viewOf(before), viewOf(after))
		if len(diff) == 0 {
			updated = before
			return nil
		}
		if after.Role != before.Role && before.HasOIDCIdentity {
			locked, err := q.RoleSyncOn(ctx, a.orgID)
			if err != nil {
				return fmt.Errorf("read whether the identity provider decides the Role: %w", err)
			}
			if locked {
				return ErrRoleLocked
			}
		}
		if lowersAdmin(before, after.Role, after.Status) && len(admins) <= 1 {
			return ErrLastAdmin
		}
		now := a.clock.Now().UTC()
		n, err := q.UpdateUser(ctx, dbgen.UpdateUserParams{
			OrgID: a.orgID, ID: before.ID, Version: before.Version, Name: after.Name, Email: optText(email), Role: after.Role,
			Now: now,
		})
		if err != nil {
			return fmt.Errorf("update the user %s: %w", before.PublicID, err)
		}
		if n == 0 {
			return ErrVersionMismatch
		}
		action, details := audit.ActionUserUpdated, map[string]any{}
		if after.Role != before.Role {
			action = audit.ActionUserRoleChanged
			if details["sessions_ended"], err = a.endSessions(ctx, q, before, auth.EndRoleChanged, now); err != nil {
				return err
			}
		}
		if updated, err = get(ctx, q, a.orgID, before.ID); err != nil {
			return err
		}
		return a.audit.Record(ctx, q, audit.Entry{
			OrgID: a.orgID, Actor: r.Actor, Transport: r.Transport, Action: action, Resource: resourceOf(updated),
			Diff: diff, Details: details, SourceAddress: r.Address,
		})
	})
	if err != nil {
		return User{}, err
	}
	return updated, nil
}

// Disable disables the user id and ends their sessions; until Enable they cannot sign in. Their acknowledged Alert
// Groups are released in the same transaction (C-03.FR-13). Disabling the last active Admin is ErrLastAdmin. A
// disabled user is returned unchanged.
func (a *Admin) Disable(ctx context.Context, r Requester, id string) (User, error) {
	return a.setStatus(ctx, r, id, StatusDisabled)
}

// Enable enables the user id again; no acknowledgement comes back. An active user is returned unchanged.
func (a *Admin) Enable(ctx context.Context, r Requester, id string) (User, error) {
	return a.setStatus(ctx, r, id, StatusActive)
}

func (a *Admin) setStatus(ctx context.Context, r Requester, id, status string) (User, error) {
	var (
		changed   User
		committed []func(context.Context)
	)
	err := a.store.InTx(ctx, func(q AdminQueries) error {
		committed = nil
		admins, before, err := a.lock(ctx, q, id, status == StatusDisabled)
		if err != nil {
			return err
		}
		if before.Status == status {
			changed = before
			return nil
		}
		if lowersAdmin(before, before.Role, status) && len(admins) <= 1 {
			return ErrLastAdmin
		}
		now := a.clock.Now().UTC()
		if _, err := q.SetUserStatus(ctx, dbgen.SetUserStatusParams{
			OrgID: a.orgID, ID: before.ID, Status: status, Now: now,
		}); err != nil {
			return fmt.Errorf("set the status of %s: %w", before.PublicID, err)
		}
		action, details := audit.ActionUserEnabled, map[string]any{}
		if status == StatusDisabled {
			action = audit.ActionUserDisabled
			if details["sessions_ended"], err = a.endSessions(ctx, q, before, auth.EndUserDisabled, now); err != nil {
				return err
			}
			// Disabling wiped the offline token, so the user is no longer re-checked.
			if err := a.dropCheck(ctx, q, before); err != nil {
				return err
			}
			if err := a.release(ctx, q, before, ReleaseDisabled, &committed); err != nil {
				return err
			}
		}
		if changed, err = get(ctx, q, a.orgID, before.ID); err != nil {
			return err
		}
		return a.audit.Record(ctx, q, audit.Entry{
			OrgID: a.orgID, Actor: r.Actor, Transport: r.Transport, Action: action, Resource: resourceOf(changed),
			Diff: audit.Diff(viewOf(before), viewOf(changed)), Details: details, SourceAddress: r.Address,
		})
	})
	if err != nil {
		return User{}, err
	}
	for _, f := range committed {
		f(ctx)
	}
	return changed, nil
}

// Delete deletes the user id (C-03.FR-13): the row stays with the status deleted, the name and the login become
// deleted-user-<public_id>, the email and the password are erased, the sessions end, the Personal access tokens are
// revoked with the reason owner_deleted (C-04.FR-1), open password setup links are superseded and their acknowledged
// Alert Groups are released (C-03.FR-13). The Audit log keeps the user's earlier entries, which show the new name. A
// non-nil version must be the user's; deleting the last active Admin is ErrLastAdmin.
func (a *Admin) Delete(ctx context.Context, r Requester, id string, version *int64) error {
	var committed []func(context.Context)
	err := a.store.InTx(ctx, func(q AdminQueries) error {
		committed = nil
		admins, before, err := a.lock(ctx, q, id, true)
		if err != nil {
			return err
		}
		if version != nil && *version != before.Version {
			return ErrVersionMismatch
		}
		if lowersAdmin(before, before.Role, StatusDeleted) && len(admins) <= 1 {
			return ErrLastAdmin
		}
		now := a.clock.Now().UTC()
		pseudonym := DeletedPrefix + before.PublicID
		if _, err := q.PseudonymizeUser(ctx, dbgen.PseudonymizeUserParams{
			OrgID: a.orgID, ID: before.ID, Pseudonym: pseudonym, Now: now,
		}); err != nil {
			return fmt.Errorf("delete the user %s: %w", before.PublicID, err)
		}
		if err := a.release(ctx, q, before, ReleaseDeleted, &committed); err != nil {
			return err
		}
		ended, err := a.endSessions(ctx, q, before, auth.EndUserDeleted, now)
		if err != nil {
			return err
		}
		if err := a.dropCheck(ctx, q, before); err != nil {
			return err
		}
		revoked, err := q.RevokeTokensOfUser(ctx, dbgen.RevokeTokensOfUserParams{
			OrgID: a.orgID, UserID: pgtype.Int8{Int64: before.ID, Valid: true}, Now: now,
		})
		if err != nil {
			return fmt.Errorf("revoke the personal access tokens of %s: %w", before.PublicID, err)
		}
		if _, err := q.SupersedePasswordSetups(ctx, dbgen.SupersedePasswordSetupsParams{
			OrgID: a.orgID, UserID: before.ID, Now: now,
		}); err != nil {
			return fmt.Errorf("supersede the password setup links of %s: %w", before.PublicID, err)
		}
		return a.audit.Record(ctx, q, audit.Entry{
			OrgID: a.orgID, Actor: r.Actor, Transport: r.Transport, Action: audit.ActionUserDeleted,
			Resource: audit.Resource{Type: audit.ResourceUser, PublicID: before.PublicID, Name: pseudonym},
			Diff: []audit.Change{
				{Pointer: "/status", Before: before.Status, After: StatusDeleted},
				{Pointer: "/name", After: pseudonym}, {Pointer: "/login", After: pseudonym},
			},
			Details:       map[string]any{"sessions_ended": ended, "tokens_revoked": revoked, "email_erased": before.Email != ""},
			SourceAddress: r.Address,
		})
	})
	if err != nil {
		return err
	}
	for _, f := range committed {
		f(ctx)
	}
	return nil
}

// lock locks the user id for the change, after the active Admins when the change may remove one, and reads it; a
// deleted user is ErrNotFound. admins are the ids of the active Admins, locked.
func (a *Admin) lock(ctx context.Context, q AdminQueries, id string, admins bool) ([]int64, User, error) {
	var ids []int64
	if admins {
		var err error
		if ids, err = q.LockActiveAdmins(ctx, a.orgID); err != nil {
			return nil, User{}, fmt.Errorf("lock the active admins: %w", err)
		}
	}
	norm, err := publicid.Parse(publicid.User, id)
	if err != nil {
		return nil, User{}, ErrNotFound
	}
	uid, err := q.LockUser(ctx, dbgen.LockUserParams{OrgID: a.orgID, PublicID: norm})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, User{}, ErrNotFound
	}
	if err != nil {
		return nil, User{}, fmt.Errorf("lock the user: %w", err)
	}
	u, err := get(ctx, q, a.orgID, uid)
	if err != nil {
		return nil, User{}, err
	}
	if u.Status == StatusDeleted {
		return nil, User{}, ErrNotFound
	}
	return ids, u, nil
}

// lowersAdmin reports whether giving u the Role role and the status status removes an active Admin.
func lowersAdmin(u User, role, status string) bool {
	return u.Role == auth.RoleAdmin && u.Status == StatusActive && (role != auth.RoleAdmin || status != StatusActive)
}

// endSessions ends every session of u with reason and returns how many ended.
func (a *Admin) endSessions(ctx context.Context, q AdminQueries, u User, reason string, now time.Time) (int64, error) {
	n, err := q.EndSessionsOfUser(ctx, dbgen.EndSessionsOfUserParams{
		OrgID: a.orgID, UserID: u.ID, Now: now, EndReason: pgtype.Text{String: reason, Valid: true},
	})
	if err != nil {
		return 0, fmt.Errorf("end the sessions of %s: %w", u.PublicID, err)
	}
	return n, nil
}

// dropCheck removes the background re-check of u, whose offline token was wiped.
func (a *Admin) dropCheck(ctx context.Context, q AdminQueries, u User) error {
	if err := q.DeleteOIDCCheck(ctx, dbgen.DeleteOIDCCheckParams{OrgID: a.orgID, UserID: u.ID}); err != nil {
		return fmt.Errorf("remove the OIDC re-check of %s: %w", u.PublicID, err)
	}
	return nil
}

// ConvertToLocal converts the account id, which signs in through OIDC, back to local (C-03.FR-29): the identity and
// the offline token are removed with its re-check, the sessions end (converted_to_local), and the returned password
// setup link sets its first password, as at creation. The TOTP enrolment stays. An account without an identity is
// ErrNotLinked.
func (a *Admin) ConvertToLocal(ctx context.Context, r Requester, id string) (User, SetupLink, error) {
	var (
		converted User
		link      SetupLink
	)
	err := a.store.InTx(ctx, func(q AdminQueries) error {
		_, before, err := a.lock(ctx, q, id, false)
		if err != nil {
			return err
		}
		if !before.HasOIDCIdentity {
			return ErrNotLinked
		}
		now := a.clock.Now().UTC()
		n, err := q.ConvertToLocal(ctx, dbgen.ConvertToLocalParams{OrgID: a.orgID, ID: before.ID, Now: now})
		if err != nil {
			return fmt.Errorf("convert %s to local: %w", before.PublicID, err)
		}
		if n == 0 {
			return ErrNotLinked
		}
		if err := a.dropCheck(ctx, q, before); err != nil {
			return err
		}
		ended, err := a.endSessions(ctx, q, before, auth.EndConvertedToLocal, now)
		if err != nil {
			return err
		}
		if converted, err = get(ctx, q, a.orgID, before.ID); err != nil {
			return err
		}
		if link, err = a.issueLink(ctx, q, r, converted); err != nil {
			return err
		}
		return a.audit.Record(ctx, q, audit.Entry{
			OrgID: a.orgID, Actor: r.Actor, Transport: r.Transport, Action: ActionConvertedToLocal,
			Resource: resourceOf(converted),
			Diff:     []audit.Change{{Pointer: "/sign_in_method", Before: auth.MethodOIDC, After: auth.MethodLocal}},
			Details: map[string]any{"sessions_ended": ended, "offline_token_wiped": before.OfflineAccess,
				"expires_at": link.ExpiresAt.Format(time.RFC3339)},
			SourceAddress: r.Address,
		})
	})
	if err != nil {
		return User{}, SetupLink{}, err
	}
	return converted, link, nil
}

// view is what the Audit log diff of a user shows.
type view struct {
	Name   string  `json:"name,omitempty"`
	Login  string  `json:"login,omitempty"`
	Email  *string `json:"email,omitempty"`
	Role   string  `json:"role,omitempty"`
	Status string  `json:"status,omitempty"`
}

func viewOf(u User) view {
	return view{Name: u.Name, Login: u.Login, Email: optionalPtr(u.Email), Role: u.Role, Status: u.Status}
}

func resourceOf(u User) audit.Resource {
	return audit.Resource{Type: audit.ResourceUser, PublicID: u.PublicID, Name: u.Name}
}

// validateFields checks a name of 1 to 200 characters, a login of 1 to 200 characters without spaces or control
// characters that does not start with deleted-user-, and an email address of at most 254 characters.
func validateFields(name, login string, email *string) error {
	if err := validateNameAndEmail(name, email); err != nil {
		return err
	}
	switch {
	case login == "":
		return &FieldError{Pointer: "/login", Code: "required", Detail: "The login is empty."}
	case utf8.RuneCountInString(login) > maxLoginLength:
		return &FieldError{Pointer: "/login", Code: "too_long",
			Detail: fmt.Sprintf("The login is longer than %d characters.", maxLoginLength)}
	case strings.IndexFunc(login, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0:
		return &FieldError{Pointer: "/login", Code: "invalid_format", Detail: "The login contains a space."}
	case strings.HasPrefix(strings.ToLower(login), DeletedPrefix):
		return &FieldError{Pointer: "/login", Code: "invalid_format",
			Detail: "Logins starting with " + DeletedPrefix + " are reserved for deleted users."}
	}
	return nil
}

func validateNameAndEmail(name string, email *string) error {
	if err := (Profile{Name: name}).Validate(); err != nil {
		return err
	}
	if e := normalizeEmail(email); e != nil {
		addr, err := mail.ParseAddress(*e)
		if len(*e) > maxEmailLength || err != nil || addr.Address != *e {
			return &FieldError{Pointer: "/email", Code: "invalid_format", Detail: "The email is not an address."}
		}
	}
	return nil
}

// normalizeEmail trims the email; an empty one is none.
func normalizeEmail(email *string) *string {
	if email == nil {
		return nil
	}
	e := strings.TrimSpace(*email)
	if e == "" {
		return nil
	}
	return &e
}

func optional(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}

func optionalPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

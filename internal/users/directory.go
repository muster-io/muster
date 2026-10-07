// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package users

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/users/dbgen"
)

// DirectoryQueries are the queries of the user directory.
type DirectoryQueries interface {
	ListUserDirectory(ctx context.Context, arg dbgen.ListUserDirectoryParams) ([]dbgen.ListUserDirectoryRow, error)
}

// Directory is the user directory of the Organization (C-10.FR-13): every user, deleted ones included, with only what
// the Owner filter, Mention settings and other pickers show, for everyone who reads Alert Groups — unlike List of
// Admin, which shows the email, the Role and the sign-in details to Admins.
type Directory struct {
	orgID int64
	q     DirectoryQueries
}

// NewDirectory returns the user directory of the Organization orgID.
func NewDirectory(orgID int64, q DirectoryQueries) *Directory {
	return &Directory{orgID: orgID, q: q}
}

// DirectoryEntry is a user in the directory (UserRef): Deactivated when the user was deleted, whose name and login
// are then the pseudonym deleted-user-<public_id>.
type DirectoryEntry struct {
	PublicID    string
	Name        string
	Login       string
	Deactivated bool
}

// DirectoryPage is a page of the directory; Next is nil on the last page.
type DirectoryPage struct {
	Entries []DirectoryEntry
	Next    *Cursor
}

// List returns a page of at most limit users in the order of their name, after the cursor when given; q, when not
// empty, matches the name and the login case-insensitively.
func (d *Directory) List(ctx context.Context, q string, after *Cursor, limit int) (DirectoryPage, error) {
	p := dbgen.ListUserDirectoryParams{OrgID: d.orgID, Q: optional(q),
		PageSize: int32(limit + 1)} //nolint:gosec // G115: the limit is at most the page size
	if after != nil {
		p.AfterName = pgtype.Text{String: after.Name, Valid: true}
		p.AfterID = pgtype.Int8{Int64: after.ID, Valid: true}
	}
	rows, err := d.q.ListUserDirectory(ctx, p)
	if err != nil {
		return DirectoryPage{}, fmt.Errorf("list the user directory: %w", err)
	}
	page := DirectoryPage{Entries: make([]DirectoryEntry, 0, min(len(rows), limit))}
	for i, row := range rows {
		if i == limit {
			page.Next = &Cursor{Name: rows[i-1].SortName, ID: rows[i-1].ID}
			break
		}
		page.Entries = append(page.Entries, DirectoryEntry{PublicID: row.PublicID, Name: row.Name, Login: row.Login,
			Deactivated: row.Status == StatusDeleted})
	}
	return page, nil
}

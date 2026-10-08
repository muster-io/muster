// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package accountlinks holds the Account links of C-18: which messenger account, in which identity space, acts as
// which User. A Telegram account lives in the one identity space telegram, a Mattermost account in the space of its
// Connection. This package only looks links up, for the button presses of the messengers; S-051 adds linking.
package accountlinks

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/accountlinks/dbgen"
)

// DBTX is the pool or transaction the queries run in.
type DBTX = dbgen.DBTX

// The statuses of a linked User; a deleted User has no links.
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
)

// SpaceTelegram is the identity space of every Telegram account.
const SpaceTelegram = "telegram"

// SpaceMattermost is the identity space of the accounts of the Mattermost Connection connectionID.
func SpaceMattermost(connectionID int64) string {
	return "mattermost:" + strconv.FormatInt(connectionID, 10)
}

// User is the User an account is linked to: its id and public_id, login, display name, Role and status.
type User struct {
	ID       int64
	PublicID string
	Login    string
	Name     string
	Role     string
	Status   string
}

// ErrNotLinked is an account without an Account link, or linked to a deleted User.
var ErrNotLinked = errors.New("the messenger account is not linked to a user")

// Queries are the queries of the package.
type Queries interface {
	LookupAccountLink(ctx context.Context, arg dbgen.LookupAccountLinkParams) (dbgen.LookupAccountLinkRow, error)
}

// Service reads the Account links of an Organization.
type Service struct {
	orgID int64
	q     Queries
}

// New returns the Service of the Organization orgID over db.
func New(orgID int64, db DBTX) *Service {
	return &Service{orgID: orgID, q: dbgen.New(db)}
}

// Lookup is the User the account externalID of identitySpace is linked to, or ErrNotLinked.
func (s *Service) Lookup(ctx context.Context, identitySpace, externalID string) (User, error) {
	r, err := s.q.LookupAccountLink(ctx, dbgen.LookupAccountLinkParams{OrgID: s.orgID, IdentitySpace: identitySpace,
		ExternalID: externalID})
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotLinked
	}
	if err != nil {
		return User{}, fmt.Errorf("read the account link in %s: %w", identitySpace, err)
	}
	return User{ID: r.ID, PublicID: r.PublicID, Login: r.Login, Name: r.Name, Role: r.Role, Status: r.Status}, nil
}

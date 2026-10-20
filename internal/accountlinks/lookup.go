// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package accountlinks

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/accountlinks/dbgen"
)

// The statuses of a linked User; a deleted User has no links.
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
	statusDeleted  = "deleted"
)

// SpaceTelegram is the identity space of every Telegram account.
const SpaceTelegram = "telegram"

// SpaceMattermost is the identity space of the accounts of the Mattermost Connection connectionID.
func SpaceMattermost(connectionID int64) string {
	return "mattermost:" + strconv.FormatInt(connectionID, 10)
}

// SpaceBridge is the identity space of the accounts of the Bridge Connection connectionID (C-22.FR-15).
func SpaceBridge(connectionID int64) string {
	return "bridge:" + strconv.FormatInt(connectionID, 10)
}

// SpaceSlack is the identity space of the accounts of the Slack Connection connectionID (C-24.FR-12).
func SpaceSlack(connectionID int64) string {
	return "slack:" + strconv.FormatInt(connectionID, 10)
}

// User is the User an account is linked to: its id and public_id, login, display name, Role and status, and the
// language of its profile, empty when it has none.
type User struct {
	ID       int64
	PublicID string
	Login    string
	Name     string
	Role     string
	Status   string
	Language string
}

// ErrNotLinked is an account without an Account link, or linked to a deleted User.
var ErrNotLinked = errors.New("the messenger account is not linked to a user")

// Lookup is the User the account externalID of identitySpace is linked to, or ErrNotLinked (C-18.FR-8). A press
// that finds the link sets its last use, at most once per TouchInterval.
func (s *Service) Lookup(ctx context.Context, identitySpace, externalID string) (User, error) {
	r, err := s.cfg.Store.LookupAccountLink(ctx, dbgen.LookupAccountLinkParams{OrgID: s.cfg.OrgID,
		IdentitySpace: identitySpace, ExternalID: externalID})
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotLinked
	}
	if err != nil {
		return User{}, fmt.Errorf("read the account link in %s: %w", identitySpace, err)
	}
	now := s.now()
	if stale := now.Add(-TouchInterval); !r.LastUsedAt.Valid || r.LastUsedAt.Time.Before(stale) {
		if err := s.cfg.Store.TouchAccountLink(ctx, dbgen.TouchAccountLinkParams{Now: now, OrgID: s.cfg.OrgID,
			ID: r.LinkID, StaleBefore: stale}); err != nil {
			return User{}, fmt.Errorf("record the use of the account link in %s: %w", identitySpace, err)
		}
	}
	return User{ID: r.ID, PublicID: r.PublicID, Login: r.Login, Name: r.Name, Role: r.Role, Status: r.Status,
		Language: r.Language.String}, nil
}

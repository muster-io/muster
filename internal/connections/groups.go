// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package connections

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/muster-io/muster/internal/connections/dbgen"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/mattermost"
	"github.com/muster-io/muster/internal/slack"
)

// ErrGroupsNotListed is the group list of a Connection that lists no groups (groups_not_listed): a Telegram one, whose
// messenger has no group mentions, a Bridge, whose groups the protocol does not list, and a Slack one whose last
// check found no usergroups:read.
var ErrGroupsNotListed = errors.New("the connection lists no groups")

// Group is a group of a Connection for a Team mention or a group entry (C-25.FR-8, C-28.FR-2): its id, its name —
// what a mention names: a Mattermost group's name, a Slack user group's handle —, its display name, and how many people
// it has, nil when the messenger did not say.
type Group struct {
	ID          string
	Name        string
	DisplayName string
	MemberCount *int64
}

// GroupList is the group list of a Connection, by name, and whether groups were left after what it read.
type GroupList struct {
	Groups    []Group
	Truncated bool
}

// Groups lists the groups of the Connection publicID whose name or display name contains q, regardless of case, by
// name, on the interactive path, limited by the Connection: the Mattermost groups that may be mentioned, one page of
// mattermost.GroupsPageSize, Truncated when it is full (F-122); and the Slack user groups that are not disabled, from
// usergroups.list, once the Connection's last check found usergroups:read (F-125). A Slack 429, which holds the
// Connection's limiter, is a *delivery.LimitedError with its Retry-After, as no token within the budget is; a read that
// failed otherwise is a *MessengerError that names the messenger. A Telegram or Bridge Connection, or a Slack one
// without the scope, is ErrGroupsNotListed; a deleted or unknown one ErrNotFound.
func (s *Service) Groups(ctx context.Context, publicID, q string) (GroupList, error) {
	row, err := s.row(ctx, s.cfg.Store, publicID)
	if err != nil {
		return GroupList{}, err
	}
	id := row.ID
	subject := delivery.Subject{Connection: &id}
	q = strings.ToLower(strings.TrimSpace(q))
	var out GroupList
	switch {
	case row.Type == TypeMattermost:
		if out, err = s.mattermostGroups(ctx, row, subject); err != nil {
			return GroupList{}, err
		}
	case row.Type == TypeSlack && row.SlackUsergroupsRead.Valid && row.SlackUsergroupsRead.Bool:
		c, err := s.slackClient(row)
		if err != nil {
			return GroupList{}, err
		}
		list, err := slack.UserGroups(ctx, c, slack.Interactive(s.cfg.Interactive, subject))
		if rl, ok := errors.AsType[*slack.RateLimitedError](err); ok {
			return GroupList{}, &delivery.LimitedError{RetryAfter: rl.RetryAfter}
		}
		if lf, ok := errors.AsType[*slack.ListFailedError](err); ok {
			return GroupList{}, &MessengerError{Messenger: "Slack", Text: string(lf.Text)}
		}
		if err != nil {
			return GroupList{}, err
		}
		for _, g := range list {
			n := g.UserCount
			out.Groups = append(out.Groups, Group{ID: g.ID, Name: g.Handle, DisplayName: g.Name, MemberCount: &n})
		}
	default:
		return GroupList{}, ErrGroupsNotListed
	}
	kept := []Group{}
	for _, g := range out.Groups {
		if q == "" || strings.Contains(strings.ToLower(g.Name), q) || strings.Contains(strings.ToLower(g.DisplayName), q) {
			kept = append(kept, g)
		}
	}
	slices.SortStableFunc(kept, func(a, b Group) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID))
	})
	out.Groups = kept
	return out, nil
}

// mattermostGroups reads one page of the groups of the Mattermost Connection of row that may be mentioned, through the
// interactive path limited by subject; a refused read is a *MessengerError.
func (s *Service) mattermostGroups(ctx context.Context, row dbgen.GetConnectionRow, subject delivery.Subject) (
	GroupList, error) {
	c, err := s.client(row)
	if err != nil {
		return GroupList{}, err
	}
	var groups []mattermost.Group
	if err := read(ctx, mattermost.Interactive(s.cfg.Interactive, subject),
		func(ctx context.Context, call delivery.Call) mattermost.Result {
			var r mattermost.Result
			groups, r = c.Groups(ctx, call.Class)
			return r
		}); err != nil {
		return GroupList{}, err
	}
	out := GroupList{Truncated: len(groups) >= mattermost.GroupsPageSize}
	for _, g := range groups {
		if g.DeleteAt != 0 || g.Name == "" {
			continue
		}
		out.Groups = append(out.Groups, Group{ID: g.ID, Name: g.Name, DisplayName: g.DisplayName,
			MemberCount: g.MemberCount})
	}
	return out, nil
}

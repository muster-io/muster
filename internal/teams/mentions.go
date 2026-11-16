// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package teams

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/teams/dbgen"
)

// The Connection types, as internal/connections names them.
const (
	connectionMattermost = "mattermost"
	connectionTelegram   = "telegram"
	connectionBridge     = "bridge"
	connectionSlack      = "slack"
)

// The reasons a Connection cannot mention a group (MentionUnavailableReason, C-25.FR-8, C-28.FR-2): Telegram has no
// group mentions; the last check of a Slack Connection found no usergroups:read; a bridge declares no group mentions in
// its info, or is a Notification gateway only.
const (
	ReasonTelegramNoGroups      = "telegram_no_groups"
	ReasonUsergroupsReadMissing = "usergroups_read_missing"
	ReasonBridgeNoGroupMentions = "bridge_no_group_mentions"
)

// CodeGroupMentionsUnsupported is the FieldError code of a Team mention for a Connection that cannot mention a group
// (C-25.FR-8), and CodeUnsupported that of a Slack user group's id given for another type.
const (
	CodeGroupMentionsUnsupported = "group_mentions_unsupported"
	CodeUnsupported              = "unsupported"
)

// MaxTeamMentions is the most Team mentions a Team has, one per Connection.
const MaxTeamMentions = 100

var (
	// mentionGroup is the form of the group of a Team mention, the form of the group names of Mention settings: 1 to
	// 64 letters, digits, ".", "_" and "-", starting with a letter or a digit.
	mentionGroup = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	// slackUserGroupID is the form of a Slack user group's id (F-125).
	slackUserGroupID = regexp.MustCompile(`^S[0-9A-Z]+$`)
)

// MentionInput is a Team mention as a save gives it and as the Audit log diff of its Team shows it: the Connection by
// public_id, the group that mentions the Team through it and, for a Slack Connection, the user group's id.
type MentionInput struct {
	ConnectionID string `json:"connection_id"`
	Group        string `json:"group"`
	GroupID      string `json:"group_id,omitempty"`
}

// MentionConnectionRef is a Connection as a Team mention names it: its public_id, name and type.
type MentionConnectionRef struct {
	PublicID string
	Name     string
	Type     string
}

// Mention is a Team mention as a read gives it: its input, its Connection, and whether the Connection can carry it
// now, with the reason when it cannot. A Team mention whose Connection lost the ability stays and mentions nobody.
type Mention struct {
	MentionInput
	Connection        MentionConnectionRef
	Available         bool
	UnavailableReason string
}

// MentionConnection is a Connection that may have a Destination, for the field of each Connection on the Team page
// (C-25.FR-8): whether it can mention a group now, the reason when it cannot, and the Team's Team mention for it, nil
// without one.
type MentionConnection struct {
	Connection        MentionConnectionRef
	Available         bool
	UnavailableReason string
	Mention           *Mention
}

// ability is whether a Connection of the type can mention a group now, and the reason when it cannot: a Mattermost
// Connection always (F-122), a Slack one once its last check found usergroups:read, a Bridge whose stored info declares
// group mentions and the role messenger, a Telegram one never.
func ability(typ string, usergroupsRead pgtype.Bool, groupMentions, messenger bool) (bool, string) {
	switch typ {
	case connectionMattermost:
		return true, ""
	case connectionSlack:
		if usergroupsRead.Valid && usergroupsRead.Bool {
			return true, ""
		}
		return false, ReasonUsergroupsReadMissing
	case connectionBridge:
		if groupMentions && messenger {
			return true, ""
		}
		return false, ReasonBridgeNoGroupMentions
	}
	return false, ReasonTelegramNoGroups
}

// withMentions sets the Team mentions of each Team and the Connections that may have a Destination with the Team's
// Team mention for each: one read of the Connections of the Organization and one of the Team mentions of the Teams.
func (s *Service) withMentions(ctx context.Context, q Queries, list []Team) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]int64, len(list))
	index := make(map[int64]int, len(list))
	for i, t := range list {
		ids[i], index[t.ID] = t.ID, i
		list[i].Mentions = []Mention{}
	}
	rows, err := q.ListTeamMentions(ctx, dbgen.ListTeamMentionsParams{OrgID: s.orgID, TeamIds: ids})
	if err != nil {
		return fmt.Errorf("list the team mentions of the teams: %w", err)
	}
	byTeam := make(map[int64]map[int64]Mention, len(list))
	for _, r := range rows {
		i, ok := index[r.TeamID]
		if !ok {
			continue
		}
		available, reason := ability(r.Type, r.SlackUsergroupsRead, r.GroupMentions, r.Messenger)
		m := Mention{MentionInput: MentionInput{ConnectionID: r.PublicID, Group: r.GroupName,
			GroupID: r.SlackUsergroupID.String}, Connection: MentionConnectionRef{PublicID: r.PublicID, Name: r.Name,
			Type: r.Type}, Available: available, UnavailableReason: reason}
		list[i].Mentions = append(list[i].Mentions, m)
		if byTeam[r.TeamID] == nil {
			byTeam[r.TeamID] = map[int64]Mention{}
		}
		byTeam[r.TeamID][r.ConnectionID] = m
	}
	conns, err := q.ListMentionConnections(ctx, s.orgID)
	if err != nil {
		return fmt.Errorf("list the connections for the team mentions: %w", err)
	}
	for i := range list {
		t := &list[i]
		t.MentionConnections = make([]MentionConnection, 0, len(conns))
		for _, c := range conns {
			available, reason := ability(c.Type, c.SlackUsergroupsRead, c.GroupMentions, true)
			mc := MentionConnection{Connection: MentionConnectionRef{PublicID: c.PublicID, Name: c.Name, Type: c.Type},
				Available: available, UnavailableReason: reason}
			if m, ok := byTeam[t.ID][c.ID]; ok {
				mc.Mention = &m
			}
			t.MentionConnections = append(t.MentionConnections, mc)
		}
	}
	return nil
}

// teamMention is a Team mention of a save, resolved: the Connection's id, the group and the user group's id.
type teamMention struct {
	connection int64
	group      string
	groupID    string
}

// resolveMentions checks the Team mentions of a save (C-25.FR-8) against what each Connection can mention now and
// returns them with their Connections' ids, and their inputs in the order of the Connections' public_ids: a Connection
// that is unknown, deleted or named twice is an unknown_id or duplicate FieldError at its connection_id, a group of
// another form an invalid_format one at its group; a Connection that cannot mention a group — Telegram, a Slack
// Connection whose last check found no usergroups:read, a Bridge whose stored info declares no group mentions or that
// is a Notification gateway only — group_mentions_unsupported at its connection_id; a Slack Team mention without the
// user group's id is required, one of another form invalid_format, and the id given for another type unsupported, at
// its group_id. A Team mention identical to one of stored, the Team's Team mentions before the save, is kept whatever
// its Connection can mention now: one whose Connection lost the ability stays, unavailable (C-25.FR-8). It calls no
// messenger.
func (s *Service) resolveMentions(ctx context.Context, q Queries, in, stored []MentionInput) ([]teamMention,
	[]MentionInput, error) {
	if len(in) == 0 {
		return nil, []MentionInput{}, nil
	}
	pointer := func(i int, field string) string { return "/team_mentions/" + strconv.Itoa(i) + "/" + field }
	if len(in) > MaxTeamMentions {
		return nil, nil, &FieldError{Pointer: "/team_mentions", Code: CodeOutOfRange,
			Detail: fmt.Sprintf("A Team has at most %d Team mentions.", MaxTeamMentions)}
	}
	canonical := make([]string, len(in))
	seen := make(map[string]bool, len(in))
	for i, m := range in {
		id, err := publicid.Parse(publicid.Connection, m.ConnectionID)
		if err != nil {
			return nil, nil, &FieldError{Pointer: pointer(i, "connection_id"), Code: CodeUnknownID,
				Detail: "No such Connection."}
		}
		if seen[id] {
			return nil, nil, &FieldError{Pointer: pointer(i, "connection_id"), Code: CodeDuplicate,
				Detail: "A Team has one Team mention per Connection."}
		}
		if !mentionGroup.MatchString(m.Group) {
			return nil, nil, &FieldError{Pointer: pointer(i, "group"), Code: CodeInvalidFormat,
				Detail: "A group is 1 to 64 letters, digits, \".\", \"_\" and \"-\", starting with a letter or a digit."}
		}
		seen[id], canonical[i] = true, id
	}
	rows, err := q.ResolveMentionConnections(ctx, dbgen.ResolveMentionConnectionsParams{OrgID: s.orgID,
		PublicIds: canonical})
	if err != nil {
		return nil, nil, fmt.Errorf("read the connections of the team mentions: %w", err)
	}
	found := make(map[string]dbgen.ResolveMentionConnectionsRow, len(rows))
	for _, r := range rows {
		found[r.PublicID] = r
	}
	out := make([]teamMention, len(in))
	inputs := make([]MentionInput, len(in))
	for i, id := range canonical {
		c, ok := found[id]
		if !ok {
			return nil, nil, &FieldError{Pointer: pointer(i, "connection_id"), Code: CodeUnknownID,
				Detail: "No such Connection."}
		}
		kept := slices.Contains(stored, MentionInput{ConnectionID: id, Group: in[i].Group, GroupID: in[i].GroupID})
		if available, reason := ability(c.Type, c.SlackUsergroupsRead, c.GroupMentions, c.Messenger); !available &&
			!kept {
			return nil, nil, &FieldError{Pointer: pointer(i, "connection_id"), Code: CodeGroupMentionsUnsupported,
				Detail: unsupportedDetail(reason)}
		}
		groupID := in[i].GroupID
		switch {
		case c.Type == connectionSlack && groupID == "":
			return nil, nil, &FieldError{Pointer: pointer(i, "group_id"), Code: CodeRequired,
				Detail: "A Slack Team mention names the user group by its id, as listConnectionGroups gives it."}
		case c.Type == connectionSlack && !slackUserGroupID.MatchString(groupID):
			return nil, nil, &FieldError{Pointer: pointer(i, "group_id"), Code: CodeInvalidFormat,
				Detail: "A Slack user group's id starts with S, followed by digits and capital letters."}
		case c.Type != connectionSlack && groupID != "":
			return nil, nil, &FieldError{Pointer: pointer(i, "group_id"), Code: CodeUnsupported,
				Detail: "Only a Slack Team mention has a user group's id."}
		}
		out[i] = teamMention{connection: c.ID, group: in[i].Group, groupID: groupID}
		inputs[i] = MentionInput{ConnectionID: id, Group: in[i].Group, GroupID: groupID}
	}
	return out, sortedMentions(inputs), nil
}

// unsupportedDetail is the detail of group_mentions_unsupported for the reason of the Connection.
func unsupportedDetail(reason string) string {
	switch reason {
	case ReasonUsergroupsReadMissing:
		return "This Slack Connection cannot mention user groups: its last check found no usergroups:read. Add the " +
			"scope to the Slack app, reinstall it and check the Connection again."
	case ReasonBridgeNoGroupMentions:
		return "This bridge does not declare group mentions in its info."
	}
	return "Telegram has no group mentions."
}

// sortedMentions are the Team mentions in the order of their Connections' public_ids, as the Audit log diff shows them.
func sortedMentions(in []MentionInput) []MentionInput {
	out := slices.Clone(in)
	if out == nil {
		out = []MentionInput{}
	}
	slices.SortFunc(out, func(a, b MentionInput) int { return cmp.Compare(a.ConnectionID, b.ConnectionID) })
	return out
}

// mentionInputs are the inputs of the Team mentions of a read, in the order of their Connections' public_ids.
func mentionInputs(ms []Mention) []MentionInput {
	out := make([]MentionInput, len(ms))
	for i, m := range ms {
		out[i] = m.MentionInput
	}
	return sortedMentions(out)
}

// writeMentions replaces the Team mentions of the Team teamID with ms, created now.
func (s *Service) writeMentions(ctx context.Context, q Queries, teamID int64, ms []teamMention, now time.Time) error {
	if err := q.DeleteTeamMentions(ctx, dbgen.DeleteTeamMentionsParams{OrgID: s.orgID, TeamID: teamID}); err != nil {
		return fmt.Errorf("remove the team mentions: %w", err)
	}
	if len(ms) == 0 {
		return nil
	}
	p := dbgen.InsertTeamMentionsParams{OrgID: s.orgID, TeamID: teamID, Now: now}
	for _, m := range ms {
		p.ConnectionIds = append(p.ConnectionIds, m.connection)
		p.GroupNames = append(p.GroupNames, m.group)
		p.UsergroupIds = append(p.UsergroupIds, m.groupID)
	}
	if err := q.InsertTeamMentions(ctx, p); err != nil {
		return fmt.Errorf("write the team mentions: %w", err)
	}
	return nil
}

// TeamMention is the Team mention that mentions a Team through a Connection, as delivery renders it: the group and, for
// a Slack Connection, the user group's id; the type internal/mentions declares for its interface TeamMentions.
type TeamMention = mentions.TeamMention

// ForConnection is the Team mention of the Team teamID for the Connection connectionID, by their ids, and whether there
// is one that the Connection can carry now: none for a deleted Team or Connection, without a Team mention, or when the
// Connection lost the ability, so that the Team is mentioned as nobody and never by its On-call (C-25.FR-8,
// C-28.FR-3). It reads through db, the transaction of the resolution, or the main pool for a nil one, so that a caller
// that holds a connection of the pool takes no second one. It satisfies the interface TeamMentions that
// internal/mentions declares.
func (s *Service) ForConnection(ctx context.Context, db mentions.DBTX, teamID, connectionID int64) (TeamMention, bool,
	error) {
	q := Queries(s.store)
	if db != nil {
		q = s.store.On(db)
	}
	r, err := q.GetTeamMention(ctx, dbgen.GetTeamMentionParams{OrgID: s.orgID, TeamID: teamID,
		ConnectionID: connectionID})
	if errors.Is(err, pgx.ErrNoRows) {
		return TeamMention{}, false, nil
	}
	if err != nil {
		return TeamMention{}, false, fmt.Errorf("read the team mention of the team %d for the connection %d: %w",
			teamID, connectionID, err)
	}
	if available, _ := ability(r.Type, r.SlackUsergroupsRead, r.GroupMentions, r.Messenger); !available {
		return TeamMention{}, false, nil
	}
	return TeamMention{Group: r.GroupName, GroupID: r.SlackUsergroupID.String}, true, nil
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package mentions holds whom a Destination mentions. From 0.2.0 that is its Mention entries (C-28.FR-1): an ordered
// list, each entry one target — everyone in the chat with a word, a person, a messenger group, a Team, the Teams of the
// Alert Group, or the On-call of a Team or of each Team of the Alert Group — and the Loud events it applies to, limited
// to what the Destination's Connection can mention (C-28.FR-2), with the L1 Mention settings of C-12.FR-8 derived from
// them and converted into them until 0.3.0 (C-28.FR-5). It resolves the symbolic Mentions of a Loud message into
// targets (C-28.FR-3, FR-4): the entries for its event — a person by their Account link in the Destination's identity
// space and left out without one, a Team by its Team mention for the Connection and nobody without one, the On-call by
// the Team's main Schedule at the moment of the event — and the Owner the lifecycle event recorded or its previous
// Owner, named as L1 named them. A User is named by the username of their Account link in the Destination's identity
// space — through a Bridge by the link's messenger user id and display name (C-22.FR-16), and in Slack by the link's
// user id as <@id>, which its adapter renders (C-24.FR-6) — otherwise by the Muster display name, which mentions
// nobody; the footer of a message names its user the same way (C-12.FR-12). A group — a group entry or a Team
// mention — is named by its name and, in Slack, the user group's id, which the Slack adapter writes as <!subteam^id>
// (C-24.FR-6); everyone in the chat by its symbolic name; a Bridge receives both as entries of their own kind
// (C-22.FR-16). It imports neither internal/schedules nor internal/teams: the On-call and the Team mentions come
// through the interfaces it declares, which internal/runtime wires (ADR-0016). Storing a Destination is
// internal/destinations', and rendering a target in a messenger's syntax is its adapter's.
package mentions

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/mentions/dbgen"
	"github.com/muster-io/muster/internal/publicid"
)

// The kinds of Loud events a Destination sets Mentions for, as the lifecycle event tables name them (C-09.FR-22).
const (
	KindNewAlertGroup = "new_alert_group"
	KindNewAlerts     = "new_alerts"
	KindReopen        = "reopen"
	KindAckTimeout    = "ack_timeout"
	KindSnoozeEnded   = "snooze_ended"
	KindRiseToUrgent  = "rise_to_urgent"
)

// Kinds are the kinds of MentionSettings, in the order of the API.
var Kinds = []string{KindNewAlertGroup, KindNewAlerts, KindReopen, KindAckTimeout, KindSnoozeEnded, KindRiseToUrgent}

// The symbolic Mentions that name a person of the Alert Group rather than a setting: the Owner, and the previous Owner
// of a Takeover.
const (
	Owner         = "owner"
	PreviousOwner = "previous_owner"
)

// The choices of everyone in the chat: nobody, or the word of the messenger.
const (
	EveryoneNone    = "none"
	EveryoneChannel = "channel"
	EveryoneAll     = "all"
	EveryoneHere    = "here"
)

// The Destination types.
const (
	TypeMattermost = "mattermost"
	TypeTelegram   = "telegram"
	TypeWebhook    = "webhook"
	TypeBridge     = "bridge"
	TypeSlack      = "slack"
)

// The kinds of a Target.
const (
	TargetEveryone = "everyone"
	TargetGroup    = "group"
	TargetUser     = "user"
)

// The codes of FieldError, as the API names them.
const (
	CodeRequired      = "required"
	CodeInvalidFormat = "invalid_format"
	CodeUnsupported   = "unsupported"
	CodeUnknownID     = "unknown_id"
	CodeDuplicate     = "duplicate"
)

// groupName is the form of a Mattermost group name.
var groupName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Setting is whom to mention for one kind of Loud event: everyone in the chat, chosen Users by public_id and
// messenger groups. The zero Setting mentions nobody (destination.mentions).
type Setting struct {
	Everyone string   `json:"everyone"`
	UserIDs  []string `json:"user_ids"`
	Groups   []string `json:"groups"`
}

// Settings are the Mention settings of a Destination by kind (MentionSettings).
type Settings map[string]Setting

// FieldError is a field of the Mention settings that is not valid, at a JSON pointer under /mentions, with a stable
// code of the validation-failed problem.
type FieldError struct {
	Pointer string
	Code    string
	Detail  string
	// extra marks a field a target of its kind does not have there, which yields to the refusal of the target as a
	// whole when the Connection cannot mention it.
	extra bool
}

func (e *FieldError) Error() string {
	return e.Pointer + ": " + e.Detail
}

// DBTX is the pool, connection or transaction the package reads through.
type DBTX = dbgen.DBTX

// Queries are the queries of the package.
type Queries interface {
	ListMentionEntries(ctx context.Context, arg dbgen.ListMentionEntriesParams) ([]dbgen.ListMentionEntriesRow, error)
	DeleteMentionEntries(ctx context.Context, arg dbgen.DeleteMentionEntriesParams) error
	InsertMentionEntries(ctx context.Context, arg dbgen.InsertMentionEntriesParams) error
	ResolveEntryUsers(ctx context.Context, arg dbgen.ResolveEntryUsersParams) ([]dbgen.ResolveEntryUsersRow, error)
	ResolveEntryTeams(ctx context.Context, arg dbgen.ResolveEntryTeamsParams) ([]dbgen.ResolveEntryTeamsRow, error)
	ListLiveTeams(ctx context.Context, orgID int64) ([]dbgen.ListLiveTeamsRow, error)
	GetMentionConnection(ctx context.Context, arg dbgen.GetMentionConnectionParams) (dbgen.GetMentionConnectionRow,
		error)
	ListAlertGroupTeams(ctx context.Context, arg dbgen.ListAlertGroupTeamsParams) ([]dbgen.ListAlertGroupTeamsRow,
		error)
	ListStormTeams(ctx context.Context, arg dbgen.ListStormTeamsParams) ([]dbgen.ListStormTeamsRow, error)
	LockEntryDestination(ctx context.Context, arg dbgen.LockEntryDestinationParams) (dbgen.LockEntryDestinationRow,
		error)
	BumpEntryDestination(ctx context.Context, arg dbgen.BumpEntryDestinationParams) error
	GetMentionEventAt(ctx context.Context, arg dbgen.GetMentionEventAtParams) (time.Time, error)
	GetPublicationEventAt(ctx context.Context, arg dbgen.GetPublicationEventAtParams) (time.Time, error)
	GetStormStartedAt(ctx context.Context, arg dbgen.GetStormStartedAtParams) (time.Time, error)
	GetEventOwners(ctx context.Context, arg dbgen.GetEventOwnersParams) (dbgen.GetEventOwnersRow, error)
	ListMentionUsers(ctx context.Context, arg dbgen.ListMentionUsersParams) ([]dbgen.ListMentionUsersRow, error)
	ListLiveUserPublicIDs(ctx context.Context, arg dbgen.ListLiveUserPublicIDsParams) ([]string, error)
	ListFooterUsers(ctx context.Context, arg dbgen.ListFooterUsersParams) ([]dbgen.ListFooterUsersRow, error)
	ListAssigneeUsers(ctx context.Context, arg dbgen.ListAssigneeUsersParams) ([]dbgen.ListAssigneeUsersRow, error)
}

// OnCalls is the On-call of a Team (C-27.FR-4), declared by its consumer; *schedules.Service implements it, and
// internal/runtime wires it. TeamOnCallAt is the User on Shift in the main Schedule of the Team teamID at t, by id, and
// false for a Team without a main Schedule or in a gap of it, read through db — the transaction of the resolution, so
// that it takes no second connection of the pool while the caller holds one — or the main pool for a nil one.
type OnCalls interface {
	TeamOnCallAt(ctx context.Context, db DBTX, teamID int64, t time.Time) (int64, bool, error)
}

// TeamMention is the Team mention that mentions a Team through a Connection (C-25.FR-8): the group and, for a Slack
// Connection, the user group's id.
type TeamMention struct {
	Group   string
	GroupID string
}

// TeamMentions are the Team mentions of the Teams (C-25.FR-8), declared by their consumer; *teams.Service implements
// them, and internal/runtime wires them. ForConnection is the Team mention of the Team teamID for the Connection
// connectionID, false when there is none that the Connection can carry now, read through db as OnCalls reads.
type TeamMentions interface {
	ForConnection(ctx context.Context, db DBTX, teamID, connectionID int64) (TeamMention, bool, error)
}

// Service validates Mention entries and settings, resolves Mentions and names users for the Organization.
type Service struct {
	orgID int64
	db    DBTX
	// queries are the queries over a pool or a transaction; tests replace them.
	queries      func(DBTX) Queries
	onCalls      OnCalls
	teamMentions TeamMentions
	business     clock.Clock
}

// Wire gives the Service the On-call of the Teams, their Team mentions and the business clock, on which a read says
// what an entry mentions now and a request without a time takes the On-call; without them a Team and an On-call
// mention nobody.
func (s *Service) Wire(onCalls OnCalls, teamMentions TeamMentions, business clock.Clock) {
	s.onCalls, s.teamMentions, s.business = onCalls, teamMentions, business
}

// New is the Service of the Organization orgID over the main pool db.
func New(orgID int64, db DBTX) *Service {
	return &Service{orgID: orgID, db: db, queries: func(d DBTX) Queries { return dbgen.New(d) }}
}

// q are the queries over db, the main pool for a nil one.
func (s *Service) q(db DBTX) Queries {
	if db == nil {
		db = s.db
	}
	return s.queries(db)
}

// everyoneOffered reports whether a Destination type takes a choice of everyone: Mattermost and outgoing webhooks take
// every one, Slack every one but all, which it has no Mention for (C-24.FR-6), Telegram and Bridges only nobody.
func everyoneOffered(destinationType, everyone string) bool {
	switch {
	case everyone == EveryoneNone:
		return true
	case usersOnly(destinationType):
		return false
	case destinationType == TypeSlack:
		return everyone != EveryoneAll
	}
	return true
}

// groupsOffered reports whether a Destination type takes messenger groups: Mattermost and outgoing webhooks do;
// Telegram and Bridges have none, and Slack's user groups are not named by the bot's scopes (C-24.FR-6).
func groupsOffered(destinationType string) bool {
	return !usersOnly(destinationType) && destinationType != TypeSlack
}

// usersOnly reports whether a Destination type takes nobody and chosen Users only: Telegram, and a Bridge (C-22.FR-3),
// whose protocol offers no Mention of everyone in the chat and no messenger groups.
func usersOnly(destinationType string) bool {
	return destinationType == TypeTelegram || destinationType == TypeBridge
}

// refusal is the detail of a choice that the Destination type refuses: everyone in the chat, or groups.
func refusal(destinationType string, groups bool) string {
	switch {
	case destinationType == TypeSlack && groups:
		return "Slack has no messenger groups that Muster can name."
	case destinationType == TypeSlack:
		return "Slack has no Mention of all; choose channel or here."
	case destinationType == TypeBridge && groups:
		return "A Bridge has no messenger groups."
	case destinationType == TypeBridge:
		return "A Bridge has no Mention of everyone in the chat."
	case groups:
		return "Telegram has no groups."
	}
	return "Telegram has no Mention of everyone in the chat."
}

// Validate refuses Mention settings that a Destination of the type cannot take (C-12.FR-8, C-22.FR-3, C-24.FR-6): every
// kind is present; everyone is none, channel, all or here, only none for Telegram and Bridges, and not all for Slack;
// user_ids name Users that exist and are not deleted, or that an entry of stored, the Destination's entries before the
// save, names, so that a read sent back with a User deleted since is not refused for it (C-28.FR-5, settled 2); groups
// are Mattermost group names, and none for Telegram, Bridges and Slack. Outgoing webhooks take every choice, as data.
func (s *Service) Validate(ctx context.Context, destinationType string, set Settings, stored []EntryInput) error {
	for name := range set {
		if !slices.Contains(Kinds, name) {
			return &FieldError{Pointer: "/mentions/" + name, Code: CodeInvalidFormat,
				Detail: "This is not a kind of Loud event with Mention settings."}
		}
	}
	type ref struct {
		pointer, id string
	}
	var refs []ref
	for _, kind := range Kinds {
		base := "/mentions/" + kind
		m, ok := set[kind]
		if !ok {
			return &FieldError{Pointer: base, Code: CodeRequired, Detail: "The setting of this kind is missing."}
		}
		switch m.Everyone {
		case EveryoneNone, EveryoneChannel, EveryoneAll, EveryoneHere:
		default:
			return &FieldError{Pointer: base + "/everyone", Code: CodeInvalidFormat,
				Detail: "Everyone is none, channel, all or here."}
		}
		if !everyoneOffered(destinationType, m.Everyone) {
			return &FieldError{Pointer: base + "/everyone", Code: CodeUnsupported,
				Detail: refusal(destinationType, false)}
		}
		seen := map[string]bool{}
		for i, id := range m.UserIDs {
			pointer := base + "/user_ids/" + strconv.Itoa(i)
			n, err := publicid.Parse(publicid.User, id)
			if err != nil {
				return &FieldError{Pointer: pointer, Code: CodeUnknownID, Detail: "No such user."}
			}
			if seen[n] {
				return &FieldError{Pointer: pointer, Code: CodeDuplicate, Detail: "The user is already listed."}
			}
			seen[n] = true
			refs = append(refs, ref{pointer: pointer, id: n})
		}
		if !groupsOffered(destinationType) && len(m.Groups) > 0 {
			return &FieldError{Pointer: base + "/groups", Code: CodeUnsupported, Detail: refusal(destinationType, true)}
		}
		for i, g := range m.Groups {
			if (destinationType == TypeMattermost && !groupName.MatchString(g)) || g == "" {
				return &FieldError{Pointer: base + "/groups/" + strconv.Itoa(i), Code: CodeInvalidFormat,
					Detail: "A group name has letters, digits, \".\", \"_\" and \"-\"."}
			}
		}
	}
	if len(refs) == 0 {
		return nil
	}
	ids := make([]string, len(refs))
	for i, r := range refs {
		ids[i] = r.id
	}
	live, err := s.q(nil).ListLiveUserPublicIDs(ctx, dbgen.ListLiveUserPublicIDsParams{OrgID: s.orgID,
		PublicIds: ids})
	if err != nil {
		return fmt.Errorf("read the users of the mention settings: %w", err)
	}
	for _, r := range refs {
		if !slices.Contains(live, r.id) && !kept(stored, EntryTarget{Kind: TargetUser, UserID: r.id}) {
			return &FieldError{Pointer: r.pointer, Code: CodeUnknownID, Detail: "No such user."}
		}
	}
	return nil
}

// Request is the Mentions of one Loud message to resolve: its Destination — id, type and Connection — its Alert
// Group, or for the first publication of a Storm summary its Storm, the lifecycle event it carries, 0 for none, the
// business time of the event, at which the On-call is taken — zero to have Resolve read it (eventAt) — and the symbolic
// Mentions.
type Request struct {
	DestinationID   int64
	DestinationType string
	ConnectionID    *int64
	AlertGroupID    int64
	StormID         int64
	Seq             int64
	At              time.Time
	Mentions        []string
}

// Target is one Mention for an adapter to render: everyone in the chat with the word — the symbolic name a Bridge
// receives — a messenger group — a Team mention among them — by its name with a Slack user group's id, which the Slack
// adapter writes as <!subteam^id> and a Bridge names by the group's name, a User, or for an outgoing webhook a Team as
// data (C-15.FR-11, C-22.FR-16, C-24.FR-6).
type Target struct {
	Kind     string      `json:"kind"`
	Everyone string      `json:"everyone,omitempty"`
	Group    string      `json:"group,omitempty"`
	GroupID  string      `json:"group_id,omitempty"`
	User     *User       `json:"user,omitempty"`
	Team     *TeamTarget `json:"team,omitempty"`
}

// TeamTarget is a Team as an outgoing webhook receives it: its slug and name.
type TeamTarget struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// User is a User as a Mention names them: public_id, display name, login, and the username, the messenger's user id
// and the display name of their Account link in the Destination's identity space, empty without one; Telegram mentions
// a user by that id (C-12.FR-8), a Bridge by that id and the link's display name (C-22.FR-16), and Slack by that id
// (C-24.FR-6).
type User struct {
	PublicID    string `json:"id"`
	Name        string `json:"name"`
	Login       string `json:"login"`
	Username    string `json:"username,omitempty"`
	ExternalID  string `json:"external_id,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

// Display is how the User is shown: the messenger username of their Account link, otherwise the display name as
// plain text, which notifies nobody (C-12.FR-12).
func (u User) Display() string {
	if u.Username != "" {
		return u.Username
	}
	return u.Name
}

// SlackUserGroupID reports whether id has the form of a Slack user group's id (F-125), the only group a Slack message
// can mention (C-24.FR-6).
func SlackUserGroupID(id string) bool {
	return slackUserGroupID.MatchString(id)
}

// IdentitySpace is the identity space of a Destination's messenger: every Telegram link, or the Mattermost, Bridge or
// Slack links of its Connection (C-22.FR-15, C-24.FR-6); an outgoing webhook has none, so that a User is named by their
// display name.
func IdentitySpace(destinationType string, connection *int64) string {
	switch {
	case destinationType == TypeTelegram:
		return "telegram"
	case destinationType == TypeMattermost && connection != nil:
		return "mattermost:" + strconv.FormatInt(*connection, 10)
	case destinationType == TypeBridge && connection != nil:
		return "bridge:" + strconv.FormatInt(*connection, 10)
	case destinationType == TypeSlack && connection != nil:
		return "slack:" + strconv.FormatInt(*connection, 10)
	}
	return ""
}

// part is one thing an entry mentions: the word of everyone, a group with a Slack user group's id, a Team as an
// outgoing webhook receives it, or a User by id.
type part struct {
	everyone string
	group    string
	groupID  string
	team     *TeamRef
	user     int64
}

// partGroup is what an entry mentions for one Team, nil for the targets that name none.
type partGroup struct {
	team  *TeamRef
	parts []part
}

// onCall is the On-call of a Team at the resolver's time: the User's id, or none.
type onCall struct {
	user int64
	ok   bool
}

// teamMention is a Team mention for a Connection, or none.
type teamMention struct {
	mention TeamMention
	ok      bool
}

// resolver expands entries at one time through one set of queries, reading each Connection, On-call, Team mention and
// the Teams that are not deleted once.
type resolver struct {
	s        *Service
	ctx      context.Context
	db       DBTX
	q        Queries
	at       time.Time
	conns    map[string]Connection
	onCalls  map[int64]onCall
	mentions map[[2]int64]teamMention
	live     []TeamRef
	liveRead bool
}

func (s *Service) newResolver(ctx context.Context, db DBTX, q Queries, at time.Time) *resolver {
	return &resolver{s: s, ctx: ctx, db: db, q: q, at: at, conns: map[string]Connection{}, onCalls: map[int64]onCall{},
		mentions: map[[2]int64]teamMention{}}
}

// connection is what the Connection of a Destination of the type, by id or public_id, can mention; none for an
// outgoing webhook.
func (r *resolver) connection(destinationType string, id *int64, publicID string) (Connection, error) {
	if destinationType == TypeWebhook {
		return Connection{}, nil
	}
	key := "p" + publicID
	if id != nil {
		key = "i" + strconv.FormatInt(*id, 10)
	}
	if c, ok := r.conns[key]; ok {
		return c, nil
	}
	c, err := r.s.connection(r.ctx, r.q, id, publicID)
	if err != nil {
		return Connection{}, err
	}
	r.conns[key] = c
	return c, nil
}

// liveTeams are the Teams that are not deleted, by name, for a read of the targets from the label.
func (r *resolver) liveTeams() ([]TeamRef, error) {
	if r.liveRead {
		return r.live, nil
	}
	rows, err := r.q.ListLiveTeams(r.ctx, r.s.orgID)
	if err != nil {
		return nil, fmt.Errorf("list the teams for the mention entries: %w", err)
	}
	for _, t := range rows {
		r.live = append(r.live, TeamRef{ID: t.ID, PublicID: t.PublicID, Name: t.Name, Slug: t.Slug})
	}
	r.liveRead = true
	return r.live, nil
}

// teamParts is what a Team mentions in a Destination of the type on the Connection c: through a messenger its Team
// mention for the Connection, nobody without one — never its On-call (C-28.FR-3) — and for an outgoing webhook the Team
// itself, as data (C-15.FR-11).
func (r *resolver) teamParts(destinationType string, c Connection, t TeamRef) ([]part, error) {
	if destinationType == TypeWebhook {
		team := t
		return []part{{team: &team}}, nil
	}
	if r.s.teamMentions == nil || c.ID == 0 {
		return nil, nil
	}
	key := [2]int64{t.ID, c.ID}
	m, ok := r.mentions[key]
	if !ok {
		tm, found, err := r.s.teamMentions.ForConnection(r.ctx, r.db, t.ID, c.ID)
		if err != nil {
			return nil, fmt.Errorf("read the team mention of %s: %w", t.PublicID, err)
		}
		m = teamMention{mention: tm, ok: found}
		r.mentions[key] = m
	}
	if !m.ok {
		return nil, nil
	}
	return []part{{group: m.mention.Group, groupID: m.mention.GroupID}}, nil
}

// onCallParts is the On-call of the Team's main Schedule at the resolver's time, nobody without one or in a gap
// (C-27.FR-4, C-28.FR-3).
func (r *resolver) onCallParts(t TeamRef) ([]part, error) {
	if r.s.onCalls == nil || r.at.IsZero() {
		return nil, nil
	}
	o, ok := r.onCalls[t.ID]
	if !ok {
		user, found, err := r.s.onCalls.TeamOnCallAt(r.ctx, r.db, t.ID, r.at)
		if err != nil {
			return nil, fmt.Errorf("read the on-call of %s: %w", t.PublicID, err)
		}
		o = onCall{user: user, ok: found}
		r.onCalls[t.ID] = o
	}
	if !o.ok {
		return nil, nil
	}
	return []part{{user: o.user}}, nil
}

// expand is what the entry e of a Destination of the type on the Connection c mentions, for each Team: the targets from
// the label for each of labelTeams. An entry the Connection cannot mention now, or one that names a deleted User or
// Team, mentions nobody.
func (r *resolver) expand(destinationType string, c Connection, e Entry, labelTeams []TeamRef) ([]partGroup, error) {
	available, _ := Can(destinationType, c, e.Target)
	var out []partGroup
	perTeam := func(t TeamRef, onCall bool) error {
		g := partGroup{team: &t}
		var err error
		switch {
		case !available || t.Deleted:
		case onCall:
			g.parts, err = r.onCallParts(t)
		default:
			g.parts, err = r.teamParts(destinationType, c, t)
		}
		out = append(out, g)
		return err
	}
	switch e.Target.Kind {
	case TargetEveryone:
		g := partGroup{}
		if available {
			g.parts = []part{{everyone: e.Target.Everyone}}
		}
		out = append(out, g)
	case TargetUser:
		g := partGroup{}
		if e.User != nil && !e.User.Deleted {
			g.parts = []part{{user: e.User.ID}}
		}
		out = append(out, g)
	case TargetGroup:
		g := partGroup{}
		if available {
			g.parts = []part{{group: e.Target.Group, groupID: e.Target.GroupID}}
		}
		out = append(out, g)
	case TargetTeam, TargetOnCallOfTeam:
		if e.Team == nil {
			out = append(out, partGroup{})
			break
		}
		if err := perTeam(*e.Team, e.Target.Kind == TargetOnCallOfTeam); err != nil {
			return nil, err
		}
	case TargetTeamsFromLabel, TargetOnCallOfTeamsFromLabel:
		for _, t := range labelTeams {
			if err := perTeam(t, e.Target.Kind == TargetOnCallOfTeamsFromLabel); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// users reads the Users that are not deleted among ids with their Account link in the identity space, by id.
func (r *resolver) users(space string, ids []int64) (map[int64]dbgen.ListMentionUsersRow, error) {
	out := map[int64]dbgen.ListMentionUsersRow{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.q.ListMentionUsers(r.ctx, dbgen.ListMentionUsersParams{OrgID: r.s.orgID, IdentitySpace: space,
		Ids: ids, PublicIds: []string{}})
	if err != nil {
		return nil, fmt.Errorf("read the users of the mentions: %w", err)
	}
	for _, u := range rows {
		out[u.ID] = u
	}
	return out, nil
}

// spaceOf is the identity space of a Destination of the type on the Connection c.
func spaceOf(destinationType string, c Connection) string {
	if c.ID == 0 {
		return IdentitySpace(destinationType, nil)
	}
	return IdentitySpace(destinationType, &c.ID)
}

// describe sets on each entry of a Destination of the type on the Connection c whether c can mention it now and what
// it mentions now (MentionEntry.resolves_to): a person by the username of their Account link, else their login, with
// whether they have one — always on an outgoing webhook, which mentions people as data — a group or a Team mention by
// its name, a Team of an outgoing webhook by its slug, and the targets from the label for each Team that is not
// deleted.
func (r *resolver) describe(destinationType string, c Connection, list []Entry) error {
	groups := make([][]partGroup, len(list))
	var ids []int64
	for i := range list {
		e := &list[i]
		e.Available, e.UnavailableReason = Can(destinationType, c, e.Target)
		var label []TeamRef
		if e.Target.Kind == TargetTeamsFromLabel || e.Target.Kind == TargetOnCallOfTeamsFromLabel {
			var err error
			if label, err = r.liveTeams(); err != nil {
				return err
			}
		}
		g, err := r.expand(destinationType, c, *e, label)
		if err != nil {
			return err
		}
		groups[i] = g
		for _, pg := range g {
			for _, p := range pg.parts {
				if p.user != 0 {
					ids = append(ids, p.user)
				}
			}
		}
	}
	users, err := r.users(spaceOf(destinationType, c), ids)
	if err != nil {
		return err
	}
	for i := range list {
		list[i].ResolvesTo = make([]Resolution, 0, len(groups[i]))
		for _, pg := range groups[i] {
			res := Resolution{Team: pg.team, Mentions: []Resolved{}}
			for _, p := range pg.parts {
				switch {
				case p.everyone != "":
					res.Mentions = append(res.Mentions, Resolved{Kind: TargetEveryone, Name: p.everyone, Linked: true})
				case p.group != "":
					res.Mentions = append(res.Mentions, Resolved{Kind: TargetGroup, Name: p.group, Linked: true})
				case p.team != nil:
					res.Mentions = append(res.Mentions, Resolved{Kind: TargetGroup, Name: p.team.Slug, Linked: true})
				case p.user != 0:
					u, ok := users[p.user]
					if !ok {
						continue
					}
					linked := u.Linked || destinationType == TypeWebhook
					name := u.Login
					if u.Linked && u.Username != "" {
						name = u.Username
					}
					res.Mentions = append(res.Mentions, Resolved{Kind: TargetUser, Name: name, Linked: linked})
				}
			}
			list[i].ResolvesTo = append(list[i].ResolvesTo, res)
		}
	}
	return nil
}

// labelTeams are the Teams that "from the label" names for the request: those of the Alert Group (C-25.FR-6), or for
// the first publication of a Storm summary those of the Alert Groups the Storm counts (C-28.FR-7).
func (s *Service) labelTeams(ctx context.Context, q Queries, org int64, r Request) ([]TeamRef, error) {
	var out []TeamRef
	switch {
	case r.StormID != 0:
		rows, err := q.ListStormTeams(ctx, dbgen.ListStormTeamsParams{OrgID: org, StormID: r.StormID})
		if err != nil {
			return nil, fmt.Errorf("read the teams of storm %d: %w", r.StormID, err)
		}
		for _, t := range rows {
			out = append(out, TeamRef{ID: t.ID, PublicID: t.PublicID, Name: t.Name, Slug: t.Slug})
		}
	case r.AlertGroupID != 0:
		rows, err := q.ListAlertGroupTeams(ctx, dbgen.ListAlertGroupTeamsParams{OrgID: org,
			AlertGroupID: r.AlertGroupID})
		if err != nil {
			return nil, fmt.Errorf("read the teams of alert group %d: %w", r.AlertGroupID, err)
		}
		for _, t := range rows {
			out = append(out, TeamRef{ID: t.ID, PublicID: t.PublicID, Name: t.Name, Slug: t.Slug})
		}
	}
	return out, nil
}

// The reasons a person whom a Mention entry names is not mentioned as their messenger mentions them
// (MentionNotDeliveredReason, C-28.FR-3): no Account link in the Destination's identity space, which leaves them out of
// the message, a bridge without native_mentions, or an Account link whose marker the bridge's message cannot build,
// which names them by the plain name (C-22.FR-16), or a bridge message whose Mention line left their Mention out for
// its length (C-12.FR-11) — also the reason of a group or everyone in the chat left out so.
const (
	ReasonNoAccountLink             = "no_account_link"
	ReasonBridgeCannotMentionPeople = "bridge_cannot_mention_people"
	ReasonMessageTooLong            = "message_too_long"
)

// EntryRef is the Mention entry that named a person: its id, its position and its target, which is the entry's
// identity across saves of its Destination — a save writes every entry again, and each target appears once.
type EntryRef struct {
	ID       int64
	Position int
	Target   EntryTarget
}

// Mention is a Mention that names no one person: a group — a group entry or a Team mention — by its name, or everyone
// in the chat by its word; its Kind is TargetGroup or TargetEveryone. The zero Mention stands for a person.
type Mention struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// Undelivered is a person — or, with Mention set, a group or everyone in the chat — whom an entry named for the Loud
// event Event and whom the message does not mention as their messenger mentions them, with the reason (C-28.FR-3). A
// template's token that a bridge's message left out has a Mention and neither an entry nor an event (C-12.FR-11).
type Undelivered struct {
	UserID  int64
	Mention Mention
	Entry   EntryRef
	Event   string
	Reason  string
}

// Mentioned is a person — or, with Mention set, a group or everyone in the chat through a bridge — whom an entry named
// for the Loud event Event and whom the message mentions as their messenger mentions them; PublicID is the person's,
// as their target names them.
type Mentioned struct {
	UserID   int64
	PublicID string
	Mention  Mention
	Entry    EntryRef
	Event    string
}

// Result is the resolution of a Loud message's Mentions: its targets, in order, and for each person an entry named,
// once per person and entry, either Undelivered or Mentioned, and through a bridge the same for each group and
// everyone in the chat an entry named. An outgoing webhook, which takes every person as data, has neither.
type Result struct {
	Targets     []Target
	Undelivered []Undelivered
	Mentioned   []Mentioned
}

// Resolve turns the symbolic Mentions of a Loud message into its targets in the Organization org, reading through
// db (C-28.FR-3, FR-4, FR-7): the Loud events among them into the Destination's entries that apply to any of them, in
// the entries' order — everyone with its word, a group by its name, a person by their Account link in the
// Destination's identity space and left out without one, a Team by its Team mention for the Connection and nobody
// without one, "Team from the label" by the Team mention of each Team of the Alert Group, the On-call of a Team by its
// main Schedule at the time of the request, nobody without one or in a gap, and the same for each Team of the Alert
// Group; for the first publication of a Storm summary the Teams of the Alert Groups the Storm counts. An entry that the
// Connection cannot mention now mentions nobody. An outgoing webhook takes every target as data, a person without an
// Account link and a Team as itself included (C-15.FR-11). The Owner turns into the Owner the lifecycle event recorded,
// or the Owner it released, and the previous Owner into the one a Takeover replaced, named as L1 named them, without a
// link by their display name. Each person and group is mentioned once. Each person an entry named that is not deleted
// is, once per entry, Undelivered — left out without an Account link, or named by the plain name through a bridge
// whose stored info does not declare native_mentions, or has none — or Mentioned, with the first of the message's Loud
// events the entry applies to. Through a bridge each group and everyone in the chat an entry named is Mentioned too,
// once per entry, so that a message whose Mention line leaves it out records it (LeftOutMentions).
func (s *Service) Resolve(ctx context.Context, db DBTX, org int64, r Request) (Result, error) {
	if len(r.Mentions) == 0 {
		return Result{}, nil
	}
	q := s.q(db)
	owner, previous, err := s.owners(ctx, q, org, r)
	if err != nil {
		return Result{}, err
	}
	var events []string
	for _, m := range r.Mentions {
		if slices.Contains(Events, m) && !slices.Contains(events, m) {
			events = append(events, m)
		}
	}
	at := r.At
	if at.IsZero() && len(events) > 0 {
		if at, err = s.eventAt(ctx, q, org, r); err != nil {
			return Result{}, err
		}
	}
	rs := s.newResolver(ctx, db, q, at)
	var parts []entryPart
	var conn Connection
	if len(events) > 0 {
		if parts, conn, err = s.entryParts(ctx, rs, org, r, events); err != nil {
			return Result{}, err
		}
	}
	// The Owner's and the previous Owner's places among the parts: at their places in the symbolic Mentions, the
	// entries at the place of the first Loud event.
	var ordered []entryPart
	placed := false
	for _, m := range r.Mentions {
		switch {
		case m == Owner && owner != nil:
			ordered = append(ordered, entryPart{part: part{user: *owner}})
		case m == PreviousOwner && previous != nil:
			ordered = append(ordered, entryPart{part: part{user: *previous}})
		case slices.Contains(events, m) && !placed:
			ordered = append(ordered, parts...)
			placed = true
		}
	}
	var ids []int64
	for _, p := range ordered {
		if p.user != 0 {
			ids = append(ids, p.user)
		}
	}
	if conn.ID == 0 && r.ConnectionID != nil {
		conn.ID = *r.ConnectionID
	}
	users, err := rs.users(spaceOf(r.DestinationType, conn), ids)
	if err != nil {
		return Result{}, err
	}
	var out Result
	add := func(t Target) {
		if !slices.ContainsFunc(out.Targets, func(o Target) bool { return same(o, t) }) {
			out.Targets = append(out.Targets, t)
		}
	}
	for _, p := range ordered {
		switch {
		case p.everyone != "":
			add(Target{Kind: TargetEveryone, Everyone: p.everyone})
			if r.DestinationType == TypeBridge {
				out.named(p, Mention{Kind: TargetEveryone, Name: p.everyone})
			}
		case p.group != "":
			add(Target{Kind: TargetGroup, Group: p.group, GroupID: p.groupID})
			if r.DestinationType == TypeBridge {
				out.named(p, Mention{Kind: TargetGroup, Name: p.group})
			}
		case p.team != nil:
			add(Target{Kind: TargetTeam, Team: &TeamTarget{Slug: p.team.Slug, Name: p.team.Name}})
		case p.user != 0:
			u, ok := users[p.user]
			if !ok {
				continue
			}
			if p.entry != nil && r.DestinationType != TypeWebhook {
				out.record(p, u.PublicID, u.Linked, r.DestinationType == TypeBridge && !conn.NativeMentions)
				if !u.Linked {
					continue
				}
			}
			add(Target{Kind: TargetUser, User: &User{PublicID: u.PublicID, Name: u.Name, Login: u.Login,
				Username: u.Username, ExternalID: u.ExternalID, DisplayName: u.DisplayName}})
		}
	}
	return out, nil
}

// LeftOut is r once the delivered message left out the Mentions of the people whose public ids are ids for its length
// (C-12.FR-11): each Mentioned of theirs is Undelivered with message_too_long, after the Undelivered of r. r is left
// as it is.
func (r Result) LeftOut(ids []string) Result {
	return r.undelivered(ids, ReasonMessageTooLong)
}

// Plain is r once the delivered message named the people whose public ids are ids by the plain name, without a
// marker, as a bridge does for an Account link whose marker it cannot build (C-22.FR-16): each Mentioned of theirs is
// Undelivered with bridge_cannot_mention_people, after the Undelivered of r. r is left as it is.
func (r Result) Plain(ids []string) Result {
	return r.undelivered(ids, ReasonBridgeCannotMentionPeople)
}

// LeftOutMentions is r once the delivered message left out for its length (C-12.FR-11) the Mentions of the groups and
// everyone in the chat among targets, a template's tokens included: each Mentioned of theirs is Undelivered with
// message_too_long, after the Undelivered of r, and then each of them that no entry named — a token's — once, without
// an entry. r is left as it is.
func (r Result) LeftOutMentions(targets []Target) Result {
	var left []Mention
	for _, t := range targets {
		var m Mention
		switch t.Kind {
		case TargetGroup:
			m = Mention{Kind: TargetGroup, Name: t.Group}
		case TargetEveryone:
			m = Mention{Kind: TargetEveryone, Name: t.Everyone}
		default:
			continue
		}
		if !slices.Contains(left, m) {
			left = append(left, m)
		}
	}
	if len(left) == 0 {
		return r
	}
	out := Result{Targets: r.Targets, Undelivered: slices.Clone(r.Undelivered)}
	var named []Mention
	for _, m := range r.Mentioned {
		if m.Mention.Kind == "" || !slices.Contains(left, m.Mention) {
			out.Mentioned = append(out.Mentioned, m)
			continue
		}
		out.Undelivered = append(out.Undelivered, Undelivered{Mention: m.Mention, Entry: m.Entry, Event: m.Event,
			Reason: ReasonMessageTooLong})
		named = append(named, m.Mention)
	}
	for _, m := range left {
		if !slices.Contains(named, m) {
			out.Undelivered = append(out.Undelivered, Undelivered{Mention: m, Reason: ReasonMessageTooLong})
		}
	}
	return out
}

// undelivered is r with each Mentioned of the people whose public ids are ids Undelivered with the reason, after the
// Undelivered of r; r itself when ids is empty.
func (r Result) undelivered(ids []string, reason string) Result {
	if len(ids) == 0 {
		return r
	}
	out := Result{Targets: r.Targets, Undelivered: slices.Clone(r.Undelivered)}
	for _, m := range r.Mentioned {
		if slices.Contains(ids, m.PublicID) {
			out.Undelivered = append(out.Undelivered, Undelivered{UserID: m.UserID, Entry: m.Entry, Event: m.Event,
				Reason: reason})
		} else {
			out.Mentioned = append(out.Mentioned, m)
		}
	}
	return out
}

// record adds the person of the part p, whose public id is id and whom an entry named, as Undelivered or Mentioned,
// once per person and entry: Undelivered without an Account link (linked false) and through a bridge that names people
// by the plain name (plain), Mentioned otherwise.
func (r *Result) record(p entryPart, id string, linked, plain bool) {
	seen := func(user, entry int64) bool {
		return slices.ContainsFunc(r.Undelivered, func(u Undelivered) bool {
			return u.UserID == user && u.Entry.ID == entry
		}) || slices.ContainsFunc(r.Mentioned, func(m Mentioned) bool { return m.UserID == user && m.Entry.ID == entry })
	}
	if seen(p.user, p.entry.ID) {
		return
	}
	switch {
	case !linked:
		r.Undelivered = append(r.Undelivered, Undelivered{UserID: p.user, Entry: *p.entry, Event: p.event,
			Reason: ReasonNoAccountLink})
	case plain:
		r.Undelivered = append(r.Undelivered, Undelivered{UserID: p.user, Entry: *p.entry, Event: p.event,
			Reason: ReasonBridgeCannotMentionPeople})
	default:
		r.Mentioned = append(r.Mentioned, Mentioned{UserID: p.user, PublicID: id, Entry: *p.entry, Event: p.event})
	}
}

// named adds the group or everyone in the chat m of the part p, when an entry named it, as Mentioned once per Mention
// and entry: only a bridge's message leaves such a Mention out, for its length (LeftOutMentions).
func (r *Result) named(p entryPart, m Mention) {
	if p.entry == nil || slices.ContainsFunc(r.Mentioned, func(o Mentioned) bool {
		return o.Mention == m && o.Entry.ID == p.entry.ID
	}) {
		return
	}
	r.Mentioned = append(r.Mentioned, Mentioned{Mention: m, Entry: *p.entry, Event: p.event})
}

// entryPart is a part of the Mentions of a message with the entry that named it and the Loud event it was named for,
// nil for the Owner and the previous Owner.
type entryPart struct {
	part
	entry *EntryRef
	event string
}

// entryParts are what the Destination's entries that apply to any of the events mention, in their order, each with its
// entry and the first of the events it applies to, and the Destination's Connection.
func (s *Service) entryParts(ctx context.Context, rs *resolver, org int64, r Request, events []string) ([]entryPart,
	Connection, error) {
	all, err := s.listEntriesOf(ctx, rs.q, org, r.DestinationID)
	if err != nil {
		return nil, Connection{}, err
	}
	var list []Entry
	label := false
	for _, e := range all {
		if slices.ContainsFunc(e.Events, func(ev string) bool { return slices.Contains(events, ev) }) {
			list = append(list, e)
			label = label || e.Target.Kind == TargetTeamsFromLabel || e.Target.Kind == TargetOnCallOfTeamsFromLabel
		}
	}
	if len(list) == 0 {
		return nil, Connection{}, nil
	}
	conn, err := rs.connection(r.DestinationType, r.ConnectionID, "")
	if err != nil {
		return nil, Connection{}, err
	}
	var teams []TeamRef
	if label {
		if teams, err = s.labelTeams(ctx, rs.q, org, r); err != nil {
			return nil, Connection{}, err
		}
	}
	var parts []entryPart
	for _, e := range list {
		groups, err := rs.expand(r.DestinationType, conn, e, teams)
		if err != nil {
			return nil, Connection{}, err
		}
		ref := &EntryRef{ID: e.ID, Position: e.Position, Target: e.Target}
		event := events[slices.IndexFunc(events, func(ev string) bool { return slices.Contains(e.Events, ev) })]
		for _, g := range groups {
			for _, p := range g.parts {
				parts = append(parts, entryPart{part: p, entry: ref, event: event})
			}
		}
	}
	return parts, conn, nil
}

// listEntriesOf reads the entries of one Destination of the Organization org through q.
func (s *Service) listEntriesOf(ctx context.Context, q Queries, org, destinationID int64) ([]Entry, error) {
	rows, err := q.ListMentionEntries(ctx, dbgen.ListMentionEntriesParams{OrgID: org,
		DestinationIds: []int64{destinationID}})
	if err != nil {
		return nil, fmt.Errorf("read the mention entries of destination %d: %w", destinationID, err)
	}
	out := make([]Entry, len(rows))
	for i, row := range rows {
		out[i] = entryOf(row)
	}
	return out, nil
}

// eventAt is the business time of the event the request carries, at which its On-call is taken (C-28.FR-3), read
// through q so that a retry or a send the limiter delayed takes the same: the Timeline entry of its lifecycle event;
// for a first Publication, the latest of the Alert Group's creation and its destination_joined in the Destination;
// for the first publication of a Storm summary, the start of the Storm; the business clock's now when none is found.
func (s *Service) eventAt(ctx context.Context, q Queries, org int64, r Request) (time.Time, error) {
	var at time.Time
	var err error
	switch {
	case r.StormID != 0:
		at, err = q.GetStormStartedAt(ctx, dbgen.GetStormStartedAtParams{OrgID: org, ID: r.StormID})
	case r.AlertGroupID != 0 && r.Seq != 0:
		at, err = q.GetMentionEventAt(ctx, dbgen.GetMentionEventAtParams{OrgID: org, AlertGroupID: r.AlertGroupID,
			EventSeq: r.Seq})
	case r.AlertGroupID != 0:
		at, err = q.GetPublicationEventAt(ctx, dbgen.GetPublicationEventAtParams{OrgID: org,
			AlertGroupID: r.AlertGroupID, DestinationID: r.DestinationID})
	default:
		return s.now(), nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return s.now(), nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("read the time of the event of the mentions: %w", err)
	}
	return at.UTC(), nil
}

func same(a, b Target) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case TargetUser:
		return a.User.PublicID == b.User.PublicID
	case TargetTeam:
		return a.Team.Slug == b.Team.Slug
	case TargetGroup:
		// A Slack user group is the same whatever handle names it: a Team mention and a group entry of one id.
		if a.GroupID != "" || b.GroupID != "" {
			return a.GroupID == b.GroupID
		}
		return a.Group == b.Group
	}
	return a.Everyone == b.Everyone
}

// owners reads the Owner and the previous Owner the lifecycle event recorded, when a Mention names one. The Owner is
// the one after the event, or the one it released — a rise to Urgent or an auto-unacknowledge removes the Owner it
// mentions.
func (s *Service) owners(ctx context.Context, q Queries, org int64, r Request) (*int64, *int64, error) {
	if (!slices.Contains(r.Mentions, Owner) && !slices.Contains(r.Mentions, PreviousOwner)) || r.AlertGroupID == 0 ||
		r.Seq == 0 {
		return nil, nil, nil
	}
	row, err := q.GetEventOwners(ctx, dbgen.GetEventOwnersParams{OrgID: org, AlertGroupID: r.AlertGroupID,
		EventSeq: r.Seq})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read the owners of the lifecycle event %d: %w", r.Seq, err)
	}
	var owner, previous *int64
	if row.PreviousOwnerUserID.Valid {
		previous = &row.PreviousOwnerUserID.Int64
		owner = previous
	}
	if row.OwnerUserID.Valid {
		owner = &row.OwnerUserID.Int64
	}
	return owner, previous, nil
}

// FooterUsers are the user the footer of the Alert Group groupID names — its Owner, who snoozed it or who resolved
// it — by the identity spaces where they have an Account link their messenger can mention them by, read through db,
// or the main pool for a nil one (C-12.FR-12): a Mattermost link with a username, a Telegram, Bridge or Slack link
// with the messenger's user id, which every link has. Each carries only what its adapter writes the user by, so that
// a Desired state changes only with what a messenger shows: the Muster display name, and a Mattermost link's
// username, a Telegram or Slack link's user id, a Bridge link's user id, display name and username, on one line. The
// adapter writes the user as its messenger's Mention (C-22.FR-16, C-24.FR-6); in any other identity space the footer
// names the User by their Muster display name.
func (s *Service) FooterUsers(ctx context.Context, db DBTX, groupID int64) (map[string]User, error) {
	rows, err := s.q(db).ListFooterUsers(ctx, dbgen.ListFooterUsersParams{OrgID: s.orgID, ID: groupID})
	if err != nil {
		return nil, fmt.Errorf("read the account links of the footer of alert group %d: %w", groupID, err)
	}
	var out map[string]User
	for _, r := range rows {
		u, ok := footerUser(r.Name, r.Messenger, r.Username, r.ExternalID, r.DisplayName)
		if !ok {
			continue
		}
		if out == nil {
			out = make(map[string]User, len(rows))
		}
		out[r.IdentitySpace] = u
	}
	return out, nil
}

// footerUser is a user of a footer by their Account link of the messenger: what its adapter writes them by, and
// whether the link lets the messenger mention them.
func footerUser(name, messenger, username, externalID, displayName string) (User, bool) {
	u := User{Name: name}
	switch messenger {
	case TypeMattermost:
		u.Username = oneLine(username)
	case TypeBridge:
		u.Username, u.ExternalID, u.DisplayName = oneLine(username), externalID, oneLine(displayName)
	default:
		u.ExternalID = externalID
	}
	return u, u.Username != "" || u.ExternalID != ""
}

// AssigneeUsers are the Assignees of the Alert Group groupID by id, each by the identity spaces where they have an
// Account link their messenger can mention them by, read through db, as FooterUsers reads the footer's user: item 7
// of the Root message names them as Mentions there (C-29.FR-1, C-32.FR-12). An Assignee without such a link is not in
// it.
func (s *Service) AssigneeUsers(ctx context.Context, db DBTX, groupID int64) (map[int64]map[string]User, error) {
	rows, err := s.q(db).ListAssigneeUsers(ctx, dbgen.ListAssigneeUsersParams{OrgID: s.orgID, AlertGroupID: groupID})
	if err != nil {
		return nil, fmt.Errorf("read the account links of the assignees of alert group %d: %w", groupID, err)
	}
	var out map[int64]map[string]User
	for _, r := range rows {
		u, ok := footerUser(r.Name, r.Messenger, r.Username, r.ExternalID, r.DisplayName)
		if !ok {
			continue
		}
		if out == nil {
			out = map[int64]map[string]User{}
		}
		if out[r.UserID] == nil {
			out[r.UserID] = map[string]User{}
		}
		out[r.UserID][r.IdentitySpace] = u
	}
	return out, nil
}

// oneLine is s on one line: every run of white space and control characters a single space, trimmed, as a Bridge
// envelope writes the names of its Mentions.
func oneLine(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }),
		" ")
}

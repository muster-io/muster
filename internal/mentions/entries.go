// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package mentions

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/mentions/dbgen"
	"github.com/muster-io/muster/internal/publicid"
)

// EventStormStarted is the Loud event of the first publication of a Storm summary (C-28.FR-7), which only Mention
// entries name; the six kinds of L1 are the others.
const EventStormStarted = "storm_started"

// Events are the Loud events a Mention entry applies to, in the order of the API (MentionEvent, C-28.FR-1).
var Events = []string{KindNewAlertGroup, KindNewAlerts, KindReopen, KindAckTimeout, KindSnoozeEnded,
	KindRiseToUrgent, EventStormStarted}

// The kinds of an entry's target beside TargetEveryone, TargetUser and TargetGroup (MentionTargetKind): a Team, the
// Teams of the Alert Group, and the On-call of a Team's main Schedule or of each Team of the Alert Group. TargetTeam is
// also the kind of a resolved Target of an outgoing webhook, which carries a Team as data (C-15.FR-11).
const (
	TargetTeam                   = "team"
	TargetTeamsFromLabel         = "teams_from_label"
	TargetOnCallOfTeam           = "on_call_of_team"
	TargetOnCallOfTeamsFromLabel = "on_call_of_teams_from_label"
)

// TargetKinds are the kinds of an entry's target, in the order of the API.
var TargetKinds = []string{TargetEveryone, TargetUser, TargetGroup, TargetTeam, TargetTeamsFromLabel,
	TargetOnCallOfTeam, TargetOnCallOfTeamsFromLabel}

// EveryoneEveryone is the mention of everyone that only an entry names: Mattermost and Slack have no such token, a
// bridge may declare it, and an outgoing webhook takes it as data.
const EveryoneEveryone = "everyone"

// CodeMentionUnsupported is the code of FieldError of a target the Destination's Connection cannot mention.
const CodeMentionUnsupported = "mention_unsupported"

// MaxEntries is the most Mention entries a Destination has (the maxItems of mention_entries), which the request
// validation of the API holds; the entry a Team adds is not added to a Destination that has them, so that a read sent
// back stays within it.
const MaxEntries = 100

// slackUserGroupID is the form of a Slack user group's id (F-125), as internal/teams checks it.
var slackUserGroupID = regexp.MustCompile(`^S[0-9A-Z]+$`)

// The reasons a Connection cannot mention a target (MentionUnavailableReason, C-28.FR-2), as internal/teams names
// those of the Team mentions.
const (
	ReasonTelegramNoGroups      = "telegram_no_groups"
	ReasonUsergroupsReadMissing = "usergroups_read_missing"
	ReasonBridgeNoGroupMentions = "bridge_no_group_mentions"
	ReasonBridgeNotDeclared     = "bridge_not_declared"
	ReasonMessengerHasNoToken   = "messenger_has_no_token"
)

// EntryTarget is the target of a Mention entry as a save writes it and the Audit log diff of its Destination shows it,
// with the fields of its kind: the word of everyone, a User by public_id, a group by its name with a Slack user group's
// id, or a Team by public_id.
type EntryTarget struct {
	Kind     string `json:"kind"`
	Everyone string `json:"everyone,omitempty"`
	UserID   string `json:"user_id,omitempty"`
	Group    string `json:"group,omitempty"`
	GroupID  string `json:"group_id,omitempty"`
	TeamID   string `json:"team_id,omitempty"`
}

// EntryInput is a Mention entry as a save writes it (MentionEntryInput): its target and the Loud events it applies to,
// all of them for an entry that names none.
type EntryInput struct {
	Target EntryTarget `json:"target"`
	Events []string    `json:"events"`
}

// UserRef is the User an entry names, by id and public_id, marked deleted once deleted.
type UserRef struct {
	ID       int64
	PublicID string
	Name     string
	Login    string
	Deleted  bool
}

// TeamRef is a Team an entry names or resolves for, marked deleted once deleted.
type TeamRef struct {
	ID       int64
	PublicID string
	Name     string
	Slug     string
	Deleted  bool
}

// Entry is a Mention entry as a read gives it (MentionEntry): its id and position, its input, the User or the Team it
// names, whether the Destination's Connection can mention its target now, with the reason when it cannot, and what it
// mentions now.
type Entry struct {
	ID       int64
	Position int
	EntryInput
	User              *UserRef
	Team              *TeamRef
	Available         bool
	UnavailableReason string
	ResolvesTo        []Resolution
}

// Resolution is what an entry mentions now for one Team, nil for the targets that name none.
type Resolution struct {
	Team     *TeamRef
	Mentions []Resolved
}

// Resolved is one Mention an entry makes now: a user by the username of their Account link, else their login, with
// whether they have one; a group, a Team mention or a Team of an outgoing webhook by name; or the word of everyone.
type Resolved struct {
	Kind   string
	Name   string
	Linked bool
}

// Connection is what a Destination's Connection can mention (C-28.FR-2): its type, whether the last check of a Slack
// Connection found usergroups:read, and from a Bridge's stored info whether it declares group mentions and native
// mentions — neither while no info was read — the role messenger and which mentions of everyone.
type Connection struct {
	ID             int64
	Type           string
	UsergroupsRead bool
	GroupMentions  bool
	NativeMentions bool
	Messenger      bool
	Everyone       []string
}

// Can reports whether a Destination of the type on the Connection c can mention the target now, and the reason when it
// cannot (C-28.FR-2): people and the On-call on every type; everyone with channel, here and all in Mattermost, with
// channel and here in Slack, with the words a bridge declares in everyone_mentions, never in Telegram; groups and Teams
// in Mattermost, in Slack while its last check found usergroups:read, through a bridge that declares group mentions,
// never in Telegram. A bridge that is a Notification gateway only mentions neither. An outgoing webhook takes every
// target, as data.
func Can(destinationType string, c Connection, t EntryTarget) (bool, string) {
	switch t.Kind {
	case TargetUser, TargetOnCallOfTeam, TargetOnCallOfTeamsFromLabel:
		return true, ""
	}
	if destinationType == TypeWebhook {
		return true, ""
	}
	if t.Kind == TargetEveryone {
		switch destinationType {
		case TypeMattermost:
			if t.Everyone == EveryoneEveryone {
				return false, ReasonMessengerHasNoToken
			}
			return true, ""
		case TypeSlack:
			if t.Everyone == EveryoneChannel || t.Everyone == EveryoneHere {
				return true, ""
			}
			return false, ReasonMessengerHasNoToken
		case TypeBridge:
			if c.Messenger && slices.Contains(c.Everyone, t.Everyone) {
				return true, ""
			}
			return false, ReasonBridgeNotDeclared
		}
		return false, ReasonTelegramNoGroups
	}
	switch destinationType {
	case TypeMattermost:
		return true, ""
	case TypeSlack:
		if c.UsergroupsRead {
			return true, ""
		}
		return false, ReasonUsergroupsReadMissing
	case TypeBridge:
		if c.GroupMentions && c.Messenger {
			return true, ""
		}
		return false, ReasonBridgeNoGroupMentions
	}
	return false, ReasonTelegramNoGroups
}

// unsupportedDetail is the detail of mention_unsupported for the reason.
func unsupportedDetail(reason string) string {
	switch reason {
	case ReasonUsergroupsReadMissing:
		return "This Slack Connection cannot mention user groups or Teams: its last check found no usergroups:read."
	case ReasonBridgeNoGroupMentions:
		return "This bridge does not declare group mentions in its info."
	case ReasonBridgeNotDeclared:
		return "This bridge does not declare this mention of everyone in its info."
	case ReasonMessengerHasNoToken:
		return "The messenger has no such mention of everyone."
	}
	return "Telegram cannot mention everyone or groups."
}

// connectionOf is what a Connection row can mention.
func connectionOf(r dbgen.GetMentionConnectionRow) Connection {
	return Connection{ID: r.ID, Type: r.Type, UsergroupsRead: r.UsergroupsRead, GroupMentions: r.GroupMentions,
		NativeMentions: r.NativeMentions, Messenger: r.Messenger, Everyone: r.EveryoneMentions}
}

// connection reads what the Connection by id or public_id can mention, through q; none for an outgoing webhook, a
// Destination without one or a Connection that is deleted.
func (s *Service) connection(ctx context.Context, q Queries, id *int64, publicID string) (Connection, error) {
	p := dbgen.GetMentionConnectionParams{OrgID: s.orgID}
	switch {
	case id != nil:
		p.ID = pgtype.Int8{Int64: *id, Valid: true}
	case publicID != "":
		p.PublicID = pgtype.Text{String: publicID, Valid: true}
	default:
		return Connection{}, nil
	}
	r, err := q.GetMentionConnection(ctx, p)
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{}, nil
	}
	if err != nil {
		return Connection{}, fmt.Errorf("read what the connection can mention: %w", err)
	}
	return connectionOf(r), nil
}

// sameTarget reports whether two targets name the same thing: a User or a Team by its canonical public_id, a Slack user
// group by its id whatever handle names it.
func sameTarget(a, b EntryTarget) bool {
	if a.Kind == TargetGroup && b.Kind == TargetGroup && (a.GroupID != "" || b.GroupID != "") {
		return a.GroupID == b.GroupID
	}
	return a == b
}

// Check is the Mention entries of a save to validate: the Destination's type and Connection (public_id, empty for an
// outgoing webhook), the entries, and the Destination's entries before the save, nil on a creation.
type Check struct {
	DestinationType string
	Connection      string
	Entries         []EntryInput
	Stored          []EntryInput
}

// ValidateEntries refuses Mention entries that the Destination cannot take and returns them in their canonical form,
// every event when an entry names none (C-28.FR-1, FR-2): each target of a kind with exactly its
// fields — the word of everyone, a User, a group's name with a Slack user group's id, required on Slack and refused on
// the others, or a Team — each target once; each event once; a target the Connection cannot mention now refused with
// mention_unsupported at /mention_entries/{i}/target, and a User or a Team that does not exist or is deleted with
// unknown_id at its field. An entry whose target equals a stored one is kept whatever its Connection can mention now
// and whether its User or Team was deleted since (C-28.FR-2, settled 2).
func (s *Service) ValidateEntries(ctx context.Context, c Check) ([]EntryInput, error) {
	q := s.q(nil)
	var conn *Connection
	connection := func() (Connection, error) {
		if conn == nil {
			got, err := s.connection(ctx, q, nil, c.Connection)
			if err != nil {
				return Connection{}, err
			}
			conn = &got
		}
		return *conn, nil
	}
	out := make([]EntryInput, len(c.Entries))
	for i, e := range c.Entries {
		base := "/mention_entries/" + strconv.Itoa(i)
		t, err := canonicalTarget(c.DestinationType, base+"/target", e.Target)
		if err != nil {
			// A target of a kind the Connection cannot mention is refused as a whole rather than for a field its kind
			// does not have there: a Telegram group with a group_id is mention_unsupported, not an unsupported
			// group_id. A missing or malformed field of the kind keeps its own refusal.
			if fe, ok := errors.AsType[*FieldError](err); ok && fe.extra {
				cn, cerr := connection()
				if cerr != nil {
					return nil, cerr
				}
				if ok, reason := Can(c.DestinationType, cn, e.Target); !ok {
					return nil, &FieldError{Pointer: base + "/target", Code: CodeMentionUnsupported,
						Detail: unsupportedDetail(reason)}
				}
			}
			return nil, err
		}
		for j := range i {
			if sameTarget(out[j].Target, t) {
				return nil, &FieldError{Pointer: base + "/target", Code: CodeDuplicate,
					Detail: "Another entry has this target; tick its events instead."}
			}
		}
		events, err := canonicalEvents(base+"/events", e.Events)
		if err != nil {
			return nil, err
		}
		out[i] = EntryInput{Target: t, Events: events}
	}
	cn, err := connection()
	if err != nil {
		return nil, err
	}
	var users, teams []string
	for i, e := range out {
		if kept(c.Stored, e.Target) {
			continue
		}
		if ok, reason := Can(c.DestinationType, cn, e.Target); !ok {
			return nil, &FieldError{Pointer: "/mention_entries/" + strconv.Itoa(i) + "/target",
				Code: CodeMentionUnsupported, Detail: unsupportedDetail(reason)}
		}
		if e.Target.UserID != "" {
			users = append(users, e.Target.UserID)
		}
		if e.Target.TeamID != "" {
			teams = append(teams, e.Target.TeamID)
		}
	}
	live, err := s.liveRefs(ctx, q, users, teams)
	if err != nil {
		return nil, err
	}
	for i, e := range out {
		pointer := "/mention_entries/" + strconv.Itoa(i) + "/target/"
		switch {
		case kept(c.Stored, e.Target):
		case e.Target.UserID != "" && !live[e.Target.UserID]:
			return nil, &FieldError{Pointer: pointer + "user_id", Code: CodeUnknownID, Detail: "No such user."}
		case e.Target.TeamID != "" && !live[e.Target.TeamID]:
			return nil, &FieldError{Pointer: pointer + "team_id", Code: CodeUnknownID, Detail: "No such Team."}
		}
	}
	return out, nil
}

// kept reports whether the target is one of the stored entries.
func kept(stored []EntryInput, t EntryTarget) bool {
	return slices.ContainsFunc(stored, func(e EntryInput) bool { return sameTarget(e.Target, t) })
}

// liveRefs reads which of the Users and Teams, by public_id, exist and are not deleted.
func (s *Service) liveRefs(ctx context.Context, q Queries, users, teams []string) (map[string]bool, error) {
	live := map[string]bool{}
	if len(users) > 0 {
		rows, err := q.ResolveEntryUsers(ctx, dbgen.ResolveEntryUsersParams{OrgID: s.orgID, PublicIds: users})
		if err != nil {
			return nil, fmt.Errorf("read the users of the mention entries: %w", err)
		}
		for _, r := range rows {
			live[r.PublicID] = !r.Deleted
		}
	}
	if len(teams) > 0 {
		rows, err := q.ResolveEntryTeams(ctx, dbgen.ResolveEntryTeamsParams{OrgID: s.orgID, PublicIds: teams})
		if err != nil {
			return nil, fmt.Errorf("read the teams of the mention entries: %w", err)
		}
		for _, r := range rows {
			live[r.PublicID] = !r.Deleted
		}
	}
	return live, nil
}

// canonicalTarget checks the fields of a target at pointer and returns it with its public_ids in canonical form.
func canonicalTarget(destinationType, pointer string, t EntryTarget) (EntryTarget, error) {
	if !slices.Contains(TargetKinds, t.Kind) {
		return EntryTarget{}, &FieldError{Pointer: pointer + "/kind", Code: CodeInvalidFormat,
			Detail: "This is not a kind of Mention target."}
	}
	allowed := map[string][]string{
		TargetEveryone:               {"everyone"},
		TargetUser:                   {"user_id"},
		TargetGroup:                  {"group", "group_id"},
		TargetTeam:                   {"team_id"},
		TargetOnCallOfTeam:           {"team_id"},
		TargetTeamsFromLabel:         nil,
		TargetOnCallOfTeamsFromLabel: nil,
	}[t.Kind]
	for field, value := range map[string]string{"everyone": t.Everyone, "user_id": t.UserID, "group": t.Group,
		"group_id": t.GroupID, "team_id": t.TeamID} {
		if value != "" && !slices.Contains(allowed, field) {
			return EntryTarget{}, &FieldError{Pointer: pointer + "/" + field, Code: CodeInvalidFormat,
				Detail: "A target of this kind has no " + field + ".", extra: true}
		}
	}
	switch t.Kind {
	case TargetEveryone:
		switch t.Everyone {
		case "":
			return EntryTarget{}, &FieldError{Pointer: pointer + "/everyone", Code: CodeRequired,
				Detail: "Choose channel, here, all or everyone."}
		case EveryoneChannel, EveryoneHere, EveryoneAll, EveryoneEveryone:
		default:
			return EntryTarget{}, &FieldError{Pointer: pointer + "/everyone", Code: CodeInvalidFormat,
				Detail: "Everyone is channel, here, all or everyone."}
		}
	case TargetUser:
		if t.UserID == "" {
			return EntryTarget{}, &FieldError{Pointer: pointer + "/user_id", Code: CodeRequired,
				Detail: "Choose a user."}
		}
		id, err := publicid.Parse(publicid.User, t.UserID)
		if err != nil {
			return EntryTarget{}, &FieldError{Pointer: pointer + "/user_id", Code: CodeUnknownID, Detail: "No such user."}
		}
		t.UserID = id
	case TargetGroup:
		switch {
		case t.Group == "":
			return EntryTarget{}, &FieldError{Pointer: pointer + "/group", Code: CodeRequired,
				Detail: "The group's name is empty."}
		case destinationType != TypeWebhook && !groupName.MatchString(t.Group):
			return EntryTarget{}, &FieldError{Pointer: pointer + "/group", Code: CodeInvalidFormat,
				Detail: "A group name has 1 to 64 letters, digits, \".\", \"_\" and \"-\", starting with a letter or a digit."}
		case destinationType == TypeSlack && t.GroupID == "":
			return EntryTarget{}, &FieldError{Pointer: pointer + "/group_id", Code: CodeRequired,
				Detail: "A Slack user group is named by its id, as listConnectionGroups gives it."}
		case destinationType == TypeSlack && !slackUserGroupID.MatchString(t.GroupID):
			return EntryTarget{}, &FieldError{Pointer: pointer + "/group_id", Code: CodeInvalidFormat,
				Detail: "A Slack user group's id starts with S, followed by digits and capital letters."}
		case destinationType != TypeSlack && t.GroupID != "":
			return EntryTarget{}, &FieldError{Pointer: pointer + "/group_id", Code: CodeUnsupported,
				Detail: "Only a Slack user group has an id.", extra: true}
		}
	case TargetTeam, TargetOnCallOfTeam:
		if t.TeamID == "" {
			return EntryTarget{}, &FieldError{Pointer: pointer + "/team_id", Code: CodeRequired,
				Detail: "Choose a Team."}
		}
		id, err := publicid.Parse(publicid.Team, t.TeamID)
		if err != nil {
			return EntryTarget{}, &FieldError{Pointer: pointer + "/team_id", Code: CodeUnknownID, Detail: "No such Team."}
		}
		t.TeamID = id
	}
	return t, nil
}

// canonicalEvents checks the events of an entry at pointer: each a Loud event, each once; every one when none is
// given, nil.
func canonicalEvents(pointer string, events []string) ([]string, error) {
	if events == nil {
		return slices.Clone(Events), nil
	}
	if len(events) == 0 {
		return nil, &FieldError{Pointer: pointer, Code: CodeRequired,
			Detail: "An entry applies to at least one Loud event."}
	}
	for j, e := range events {
		switch {
		case !slices.Contains(Events, e):
			return nil, &FieldError{Pointer: pointer + "/" + strconv.Itoa(j), Code: CodeInvalidFormat,
				Detail: "This is not a Loud event an entry applies to."}
		case slices.Contains(events[:j], e):
			return nil, &FieldError{Pointer: pointer + "/" + strconv.Itoa(j), Code: CodeDuplicate,
				Detail: "The event is already listed."}
		}
	}
	return ordered(events), nil
}

// ordered are the events in the order of Events.
func ordered(events []string) []string {
	out := make([]string, 0, len(events))
	for _, e := range Events {
		if slices.Contains(events, e) {
			out = append(out, e)
		}
	}
	return out
}

// Inputs are the inputs of the entries of a read.
func Inputs(entries []Entry) []EntryInput {
	out := make([]EntryInput, len(entries))
	for i, e := range entries {
		out[i] = e.EntryInput
	}
	return out
}

// l1Expressible reports whether L1 took the target on the Destination type (C-12.FR-8, C-22.FR-3, C-24.FR-6): the
// everyone choices it offered there — channel, all and here in Mattermost and outgoing webhooks, channel and here in
// Slack — Users on every type, and groups by name in Mattermost and outgoing webhooks.
func l1Expressible(destinationType string, t EntryTarget) bool {
	switch t.Kind {
	case TargetEveryone:
		return t.Everyone != EveryoneEveryone && everyoneOffered(destinationType, t.Everyone)
	case TargetUser:
		return true
	case TargetGroup:
		return groupsOffered(destinationType) && t.GroupID == ""
	}
	return false
}

// expressible reports whether L1 can express the entry on the Destination type: L1 took its target there and it
// applies to at least one of the six kinds of L1 (C-28.FR-5).
func expressible(destinationType string, e EntryInput) bool {
	return l1Expressible(destinationType, e.Target) &&
		slices.ContainsFunc(e.Events, func(ev string) bool { return slices.Contains(Kinds, ev) })
}

// Nobody is the Mention settings of L1 that mention nobody (destination.mentions), which a Destination created from
// 0.2.0 on stores in its L1 column, so that a replica of 0.1 mentions nobody there (C-28.FR-5).
func Nobody() Settings {
	set := make(Settings, len(Kinds))
	for _, k := range Kinds {
		set[k] = Setting{Everyone: EveryoneNone, UserIDs: []string{}, Groups: []string{}}
	}
	return set
}

// L1Form is the Mention settings of L1 computed from the entries that L1 can express on the Destination type
// (C-28.FR-5): for each of its six kinds, the first everyone choice whose entry applies to it, the chosen Users and the
// groups, in the order of the entries. The other entries are not seen in it.
func L1Form(destinationType string, entries []EntryInput) Settings {
	set := Nobody()
	for _, e := range entries {
		if !expressible(destinationType, e) {
			continue
		}
		for _, k := range e.Events {
			st, ok := set[k]
			if !ok {
				continue
			}
			switch e.Target.Kind {
			case TargetEveryone:
				if st.Everyone == EveryoneNone {
					st.Everyone = e.Target.Everyone
				}
			case TargetUser:
				st.UserIDs = append(st.UserIDs, e.Target.UserID)
			case TargetGroup:
				st.Groups = append(st.Groups, e.Target.Group)
			}
			set[k] = st
		}
	}
	return set
}

// FromSettings turns the Mention settings of L1 into entries with the same effect, as the 0.2.0 migration does
// (C-28.FR-5, migration 0043): each target chosen — the everyone choice other than none, each chosen User, each group,
// among those L1 took on the Destination type — becomes one entry for the kinds it was chosen for, with storm_started
// where new_alert_group is among them, since an L1 Storm summary mentioned the targets of new_alert_group; the entries
// are everyone, then the Users, then the groups, each in the order L1 listed them, kind by kind. Settings that mention
// nobody give no entry.
func FromSettings(destinationType string, set Settings) []EntryInput {
	type found struct {
		target EntryTarget
		kinds  []string
	}
	var classes [3][]found
	add := func(class int, t EntryTarget, kind string) {
		for i := range classes[class] {
			if sameTarget(classes[class][i].target, t) {
				if !slices.Contains(classes[class][i].kinds, kind) {
					classes[class][i].kinds = append(classes[class][i].kinds, kind)
				}
				return
			}
		}
		classes[class] = append(classes[class], found{target: t, kinds: []string{kind}})
	}
	for _, k := range Kinds {
		st, ok := set[k]
		if !ok {
			continue
		}
		if t := (EntryTarget{Kind: TargetEveryone, Everyone: st.Everyone}); st.Everyone != EveryoneNone &&
			st.Everyone != "" && l1Expressible(destinationType, t) {
			add(0, t, k)
		}
		for _, u := range st.UserIDs {
			if id, err := publicid.Parse(publicid.User, u); err == nil {
				add(1, EntryTarget{Kind: TargetUser, UserID: id}, k)
			}
		}
		if groupsOffered(destinationType) {
			for _, g := range st.Groups {
				if g != "" {
					add(2, EntryTarget{Kind: TargetGroup, Group: g}, k)
				}
			}
		}
	}
	var out []EntryInput
	for _, class := range classes {
		for _, f := range class {
			events := ordered(f.kinds)
			if slices.Contains(events, KindNewAlertGroup) {
				events = append(events, EventStormStarted)
			}
			out = append(out, EntryInput{Target: f.target, Events: events})
		}
	}
	return out
}

// normalized is the settings with every kind present, each User's public_id in canonical form and empty lists, as
// L1Form gives them, for the comparison of a request's settings with the stored form.
func normalized(set Settings) Settings {
	out := Nobody()
	for _, k := range Kinds {
		st, ok := set[k]
		if !ok {
			continue
		}
		n := Setting{Everyone: st.Everyone, UserIDs: []string{}, Groups: []string{}}
		if n.Everyone == "" {
			n.Everyone = EveryoneNone
		}
		for _, u := range st.UserIDs {
			if id, err := publicid.Parse(publicid.User, u); err == nil {
				u = id
			}
			n.UserIDs = append(n.UserIDs, u)
		}
		n.Groups = append(n.Groups, st.Groups...)
		out[k] = n
	}
	return out
}

// SameForm reports whether the Mention settings of a request equal the L1 form of the stored entries of a Destination of
// the type, as a client that sends back what it read sends them: such a request changes nothing and is not validated
// again, so that a User deleted since, whom the form shows, does not refuse it (C-28.FR-5).
func SameForm(destinationType string, stored []EntryInput, set Settings) bool {
	return reflect.DeepEqual(unordered(L1Form(destinationType, stored)), unordered(normalized(set)))
}

// unordered is the settings with the Users and the groups of each kind sorted, so that the order a client lists them
// in within a kind does not count: whom a kind mentions does not depend on it.
func unordered(set Settings) Settings {
	out := make(Settings, len(set))
	for k, st := range set {
		st.UserIDs, st.Groups = slices.Sorted(slices.Values(st.UserIDs)), slices.Sorted(slices.Values(st.Groups))
		out[k] = st
	}
	return out
}

// MergeSettings is the entries of a request that sends the Mention settings of L1 without entries, on an update of a
// Destination of the type whose entries are stored (C-28.FR-5): nothing changes when the settings equal the L1 form of
// the stored entries, so that a client that sends back what it read loses nothing; otherwise the conversion of
// FromSettings replaces exactly the stored entries that L1 can express, every other entry staying after them in its
// order — a Slack user group, a mention of everyone that L1 had not, an entry for storm_started alone, Teams and
// On-calls — and a target present before and after keeps its stored storm_started rather than the conversion's rule;
// such a target stored in an entry that L1 cannot express merges into the converted one. A kind of a stored entry that
// the form does not show — a second mention of everyone for a kind, of which the form shows the first — stays on it
// unless the request changed that kind's everyone choice, so that a save from the L1 editor that did not touch the kind
// loses no Mention.
func MergeSettings(destinationType string, stored []EntryInput, set Settings) []EntryInput {
	if SameForm(destinationType, stored, set) {
		return stored
	}
	converted := keepHidden(destinationType, stored, set, FromSettings(destinationType, set))
	used := make([]bool, len(stored))
	for i, c := range converted {
		j := slices.IndexFunc(stored, func(e EntryInput) bool { return sameTarget(e.Target, c.Target) })
		if j < 0 {
			continue
		}
		used[j] = true
		events := slices.DeleteFunc(slices.Clone(c.Events), func(e string) bool { return e == EventStormStarted })
		if slices.Contains(stored[j].Events, EventStormStarted) {
			events = append(events, EventStormStarted)
		}
		converted[i].Events = events
	}
	out := converted
	for j, e := range stored {
		if !used[j] && !expressible(destinationType, e) {
			out = append(out, e)
		}
	}
	return out
}

// keepHidden adds to converted, the conversion of a request's settings, the kinds of the stored entries of everyone
// that the L1 form of the stored entries hides — where it shows another word for the kind — and whose everyone choice
// the request kept: such a kind stays on the entry of that word, which is added after the other entries of everyone
// when the conversion has none. Users and groups are never hidden: the form lists every one for each kind.
func keepHidden(destinationType string, stored []EntryInput, set Settings, converted []EntryInput) []EntryInput {
	form, asked := L1Form(destinationType, stored), normalized(set)
	for _, e := range stored {
		if e.Target.Kind != TargetEveryone || !l1Expressible(destinationType, e.Target) {
			continue
		}
		var hidden []string
		for _, k := range e.Events {
			if f, ok := form[k]; ok && f.Everyone != e.Target.Everyone && asked[k].Everyone == f.Everyone {
				hidden = append(hidden, k)
			}
		}
		if len(hidden) == 0 {
			continue
		}
		i := slices.IndexFunc(converted, func(c EntryInput) bool { return sameTarget(c.Target, e.Target) })
		if i >= 0 {
			converted[i].Events = ordered(append(converted[i].Events, hidden...))
			continue
		}
		at := 0
		for at < len(converted) && converted[at].Target.Kind == TargetEveryone {
			at++
		}
		// MergeSettings gives it the stored storm_started, as to every target present before and after.
		converted = slices.Insert(converted, at, EntryInput{Target: e.Target, Events: ordered(hidden)})
	}
	return converted
}

// WriteEntries makes entries, validated, the Mention entries of the Destination destinationID, created now, through
// db — the transaction of the save — in their order; Users and Teams by public_id.
func (s *Service) WriteEntries(ctx context.Context, db DBTX, destinationID int64, entries []EntryInput,
	now time.Time) error {
	q := s.q(db)
	if err := q.DeleteMentionEntries(ctx, dbgen.DeleteMentionEntriesParams{OrgID: s.orgID,
		DestinationID: destinationID}); err != nil {
		return fmt.Errorf("remove the mention entries: %w", err)
	}
	return s.insertEntries(ctx, q, destinationID, 0, entries, now)
}

// insertEntries adds entries to the Destination destinationID from the position from on.
func (s *Service) insertEntries(ctx context.Context, q Queries, destinationID int64, from int, entries []EntryInput,
	now time.Time) error {
	if len(entries) == 0 {
		return nil
	}
	var users, teams []string
	for _, e := range entries {
		if e.Target.UserID != "" {
			users = append(users, e.Target.UserID)
		}
		if e.Target.TeamID != "" {
			teams = append(teams, e.Target.TeamID)
		}
	}
	ids := map[string]int64{}
	if len(users) > 0 {
		rows, err := q.ResolveEntryUsers(ctx, dbgen.ResolveEntryUsersParams{OrgID: s.orgID, PublicIds: users})
		if err != nil {
			return fmt.Errorf("read the users of the mention entries: %w", err)
		}
		for _, r := range rows {
			ids[r.PublicID] = r.ID
		}
	}
	if len(teams) > 0 {
		rows, err := q.ResolveEntryTeams(ctx, dbgen.ResolveEntryTeamsParams{OrgID: s.orgID, PublicIds: teams})
		if err != nil {
			return fmt.Errorf("read the teams of the mention entries: %w", err)
		}
		for _, r := range rows {
			ids[r.PublicID] = r.ID
		}
	}
	p := dbgen.InsertMentionEntriesParams{OrgID: s.orgID, DestinationID: destinationID, Now: now}
	for i, e := range entries {
		var user, team int64
		if e.Target.UserID != "" {
			if user = ids[e.Target.UserID]; user == 0 {
				return fmt.Errorf("write the mention entries: no user %s", e.Target.UserID)
			}
		}
		if e.Target.TeamID != "" {
			if team = ids[e.Target.TeamID]; team == 0 {
				return fmt.Errorf("write the mention entries: no team %s", e.Target.TeamID)
			}
		}
		p.Positions = append(p.Positions, int32(from+i)) //nolint:gosec // at most MaxEntries
		p.Kinds = append(p.Kinds, e.Target.Kind)
		p.Everyones = append(p.Everyones, e.Target.Everyone)
		p.UserIds = append(p.UserIds, user)
		p.GroupNames = append(p.GroupNames, e.Target.Group)
		p.UsergroupIds = append(p.UsergroupIds, e.Target.GroupID)
		p.TeamIds = append(p.TeamIds, team)
		p.Events = append(p.Events, strings.Join(e.Events, ","))
	}
	if err := q.InsertMentionEntries(ctx, p); err != nil {
		return fmt.Errorf("write the mention entries: %w", err)
	}
	return nil
}

// TeamEntry is the entry "Team {Team}" that a Team added to one of its Destinations (C-28.FR-6): the Destination by
// public_id, name and new version, and its entries before and after, for the Audit log diff of destination.updated.
type TeamEntry struct {
	PublicID string
	Name     string
	Before   []EntryInput
	After    []EntryInput
}

// AddTeamEntry adds the entry "Team {Team}" for every Loud event to the Destination destinationID that the Team teamID
// (public_id teamPublicID) of the Organization org names as a Destination of the Team, through db — the transaction of
// the Team's save — after its other entries, created now, and gives the Destination a new version (C-28.FR-6,
// C-26.FR-1). It adds nothing and returns false to a Destination that already has an entry of the kind team with that
// Team, to a Telegram Destination, which cannot mention Teams (settled 1), to a deleted one, and to one that has
// MaxEntries entries. The Destination's row
// is locked after the Team's, the order of a save of the Destination with an owning Team.
func AddTeamEntry(ctx context.Context, db DBTX, org, destinationID, teamID int64, teamPublicID string,
	now time.Time) (TeamEntry, bool, error) {
	return addTeamEntry(ctx, dbgen.New(db), org, destinationID, teamID, teamPublicID, now)
}

func addTeamEntry(ctx context.Context, q Queries, org, destinationID, teamID int64, teamPublicID string,
	now time.Time) (TeamEntry, bool, error) {
	d, err := q.LockEntryDestination(ctx, dbgen.LockEntryDestinationParams{OrgID: org, ID: destinationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return TeamEntry{}, false, nil
	}
	if err != nil {
		return TeamEntry{}, false, fmt.Errorf("lock the destination %d for its team entry: %w", destinationID, err)
	}
	if d.Type == TypeTelegram {
		return TeamEntry{}, false, nil
	}
	rows, err := q.ListMentionEntries(ctx, dbgen.ListMentionEntriesParams{OrgID: org,
		DestinationIds: []int64{destinationID}})
	if err != nil {
		return TeamEntry{}, false, fmt.Errorf("read the mention entries of the destination %s: %w", d.PublicID, err)
	}
	if len(rows) >= MaxEntries {
		return TeamEntry{}, false, nil
	}
	before := make([]EntryInput, len(rows))
	next := 0
	for i, r := range rows {
		if r.TargetKind == TargetTeam && r.TeamID.Valid && r.TeamID.Int64 == teamID {
			return TeamEntry{}, false, nil
		}
		before[i] = entryOf(r).EntryInput
		next = max(next, int(r.Position)+1)
	}
	entry := EntryInput{Target: EntryTarget{Kind: TargetTeam, TeamID: teamPublicID}, Events: slices.Clone(Events)}
	s := &Service{orgID: org}
	if err := s.insertEntries(ctx, q, destinationID, next, []EntryInput{entry}, now); err != nil {
		return TeamEntry{}, false, err
	}
	if err := q.BumpEntryDestination(ctx, dbgen.BumpEntryDestinationParams{OrgID: org, ID: destinationID,
		Now: now}); err != nil {
		return TeamEntry{}, false, fmt.Errorf("give the destination %s a new version: %w", d.PublicID, err)
	}
	return TeamEntry{PublicID: d.PublicID, Name: d.Name, Before: before, After: append(slices.Clone(before), entry)},
		true, nil
}

// entryOf is the entry of a row, with its User and Team; neither availability nor resolution.
func entryOf(r dbgen.ListMentionEntriesRow) Entry {
	e := Entry{ID: r.ID, Position: int(r.Position), EntryInput: EntryInput{Target: EntryTarget{Kind: r.TargetKind, Everyone: r.Everyone, Group: r.GroupName,
		GroupID: r.SlackUsergroupID, UserID: r.UserPublicID, TeamID: r.TeamPublicID}, Events: ordered(r.Events)}}
	if r.UserID.Valid {
		e.User = &UserRef{ID: r.UserID.Int64, PublicID: r.UserPublicID, Name: r.UserName, Login: r.UserLogin, Deleted: r.UserDeleted}
	}
	if r.TeamID.Valid {
		e.Team = &TeamRef{ID: r.TeamID.Int64, PublicID: r.TeamPublicID, Name: r.TeamName, Slug: r.TeamSlug,
			Deleted: r.TeamDeleted}
	}
	return e
}

// listEntries reads the entries of the Destinations ids through q, by Destination, with their Users and Teams.
func (s *Service) listEntries(ctx context.Context, q Queries, ids []int64) (map[int64][]Entry, error) {
	rows, err := q.ListMentionEntries(ctx, dbgen.ListMentionEntriesParams{OrgID: s.orgID, DestinationIds: ids})
	if err != nil {
		return nil, fmt.Errorf("read the mention entries: %w", err)
	}
	out := make(map[int64][]Entry, len(ids))
	for _, r := range rows {
		out[r.DestinationID] = append(out[r.DestinationID], entryOf(r))
	}
	return out, nil
}

// EntryDestination is a Destination whose entries a read gives: its id, its type and its Connection by public_id,
// empty for an outgoing webhook.
type EntryDestination struct {
	ID         int64
	Type       string
	Connection string
}

// Entries reads the Mention entries of the Destinations through db, or the main pool for a nil one, by Destination id,
// each with whether its Connection can mention it now and what it mentions now on the business clock (C-28.FR-1 to
// FR-3): the targets from the label for each Team that is not deleted. A Destination without entries has none.
func (s *Service) Entries(ctx context.Context, db DBTX, ds []EntryDestination) (map[int64][]Entry, error) {
	if len(ds) == 0 {
		return map[int64][]Entry{}, nil
	}
	q := s.q(db)
	ids := make([]int64, len(ds))
	for i, d := range ds {
		ids[i] = d.ID
	}
	out, err := s.listEntries(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	r := s.newResolver(ctx, db, q, s.now())
	for _, d := range ds {
		list := out[d.ID]
		if len(list) == 0 {
			continue
		}
		conn, err := r.connection(d.Type, nil, d.Connection)
		if err != nil {
			return nil, err
		}
		if err := r.describe(d.Type, conn, list); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// now is the business clock's now, the zero time without one.
func (s *Service) now() time.Time {
	if s.business == nil {
		return time.Time{}
	}
	return s.business.Now().UTC()
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package mentions holds the Mention settings of C-12.FR-8 and FR-12: whom a Destination mentions for each kind of
// Loud event — nobody, everyone in the chat with the word its messenger offers, chosen Users or messenger groups —
// validated per Destination type, and the resolution of a Loud message's symbolic Mentions into targets: the
// Destination's setting for a kind, the Owner the lifecycle event recorded, or its previous Owner. A User is named by
// the username of their Account link in the Destination's identity space, otherwise by the Muster display name,
// which mentions nobody; the footer of a message names users the same way. Storing the settings is the Destination's,
// and rendering a target in a messenger's syntax is its adapter's.
package mentions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5"

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
}

func (e *FieldError) Error() string {
	return e.Pointer + ": " + e.Detail
}

// DBTX is the pool, connection or transaction the package reads through.
type DBTX = dbgen.DBTX

// Queries are the queries of the package.
type Queries interface {
	GetDestinationMentions(ctx context.Context, arg dbgen.GetDestinationMentionsParams) ([]byte, error)
	GetEventOwners(ctx context.Context, arg dbgen.GetEventOwnersParams) (dbgen.GetEventOwnersRow, error)
	ListMentionUsers(ctx context.Context, arg dbgen.ListMentionUsersParams) ([]dbgen.ListMentionUsersRow, error)
	ListLiveUserPublicIDs(ctx context.Context, arg dbgen.ListLiveUserPublicIDsParams) ([]string, error)
	ListFooterUsernames(ctx context.Context, arg dbgen.ListFooterUsernamesParams) ([]dbgen.ListFooterUsernamesRow,
		error)
}

// Service validates Mention settings, resolves Mentions and names users for the Organization.
type Service struct {
	orgID int64
	db    DBTX
	// queries are the queries over a pool or a transaction; tests replace them.
	queries func(DBTX) Queries
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
// every one, Telegram only nobody.
func everyoneOffered(destinationType, everyone string) bool {
	return everyone == EveryoneNone || destinationType != TypeTelegram
}

// Validate refuses Mention settings that a Destination of the type cannot take (C-12.FR-8): every kind is present;
// everyone is none, channel, all or here, and only none for Telegram; user_ids name Users that exist and are not
// deleted; groups are Mattermost group names, and none for Telegram. Outgoing webhooks take every choice, as data.
func (s *Service) Validate(ctx context.Context, destinationType string, set Settings) error {
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
				Detail: "Telegram has no Mention of everyone in the chat."}
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
		if destinationType == TypeTelegram && len(m.Groups) > 0 {
			return &FieldError{Pointer: base + "/groups", Code: CodeUnsupported, Detail: "Telegram has no groups."}
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
		if !slices.Contains(live, r.id) {
			return &FieldError{Pointer: r.pointer, Code: CodeUnknownID, Detail: "No such user."}
		}
	}
	return nil
}

// Request is the Mentions of one Loud message to resolve: its Destination — id, type and Connection — its Alert
// Group and the lifecycle event it carries, 0 for none, and the symbolic Mentions.
type Request struct {
	DestinationID   int64
	DestinationType string
	ConnectionID    *int64
	AlertGroupID    int64
	Seq             int64
	Mentions        []string
}

// Target is one Mention for an adapter to render: everyone in the chat with the word, a messenger group, or a User.
type Target struct {
	Kind     string `json:"kind"`
	Everyone string `json:"everyone,omitempty"`
	Group    string `json:"group,omitempty"`
	User     *User  `json:"user,omitempty"`
}

// User is a User as a Mention names them: public_id, display name, login, and the username and the messenger's user
// id of their Account link in the Destination's identity space, empty without one; Telegram mentions a user by that
// id (C-12.FR-8).
type User struct {
	PublicID   string `json:"id"`
	Name       string `json:"name"`
	Login      string `json:"login"`
	Username   string `json:"username,omitempty"`
	ExternalID string `json:"external_id,omitempty"`
}

// Display is how the User is shown: the messenger username of their Account link, otherwise the display name as
// plain text, which notifies nobody (C-12.FR-12).
func (u User) Display() string {
	if u.Username != "" {
		return u.Username
	}
	return u.Name
}

// IdentitySpace is the identity space of a Destination's messenger: every Telegram link, or the Mattermost links of
// its Connection; an outgoing webhook has none.
func IdentitySpace(destinationType string, connection *int64) string {
	switch {
	case destinationType == TypeTelegram:
		return "telegram"
	case destinationType == TypeMattermost && connection != nil:
		return "mattermost:" + strconv.FormatInt(*connection, 10)
	}
	return ""
}

// Resolve turns the symbolic Mentions of a Loud message into its targets in the Organization org, reading through
// db (C-12.FR-8): a kind of Loud event into the Destination's setting for it — everyone, its groups, its Users — the
// Owner into the Owner the lifecycle event recorded, or the Owner it released, and the previous Owner into the one a
// Takeover replaced. A Reopen into acknowledged carries only the Owner, by the lifecycle event table. Duplicates are
// left out, and so are the choices the Destination's type does not offer.
func (s *Service) Resolve(ctx context.Context, db DBTX, org int64, r Request) ([]Target, error) {
	if len(r.Mentions) == 0 {
		return nil, nil
	}
	q := s.q(db)
	set, err := s.settings(ctx, q, org, r)
	if err != nil {
		return nil, err
	}
	owner, previous, err := s.owners(ctx, q, org, r)
	if err != nil {
		return nil, err
	}
	var ids []int64
	var publicIDs []string
	for _, m := range r.Mentions {
		switch m {
		case Owner:
			ids = appendID(ids, owner)
		case PreviousOwner:
			ids = appendID(ids, previous)
		default:
			for _, id := range set[m].UserIDs {
				if n, err := publicid.Parse(publicid.User, id); err == nil {
					publicIDs = append(publicIDs, n)
				}
			}
		}
	}
	users := map[string]User{}
	byID := map[int64]string{}
	if len(ids) > 0 || len(publicIDs) > 0 {
		rows, err := q.ListMentionUsers(ctx, dbgen.ListMentionUsersParams{OrgID: org,
			IdentitySpace: IdentitySpace(r.DestinationType, r.ConnectionID), Ids: orEmpty(ids),
			PublicIds: orEmpty(publicIDs)})
		if err != nil {
			return nil, fmt.Errorf("read the users of the mentions: %w", err)
		}
		for _, u := range rows {
			users[u.PublicID] = User{PublicID: u.PublicID, Name: u.Name, Login: u.Login, Username: u.Username,
				ExternalID: u.ExternalID}
			byID[u.ID] = u.PublicID
		}
	}
	var out []Target
	add := func(t Target) {
		if !slices.ContainsFunc(out, func(o Target) bool { return same(o, t) }) {
			out = append(out, t)
		}
	}
	addUser := func(publicID string) {
		if u, ok := users[publicID]; ok {
			add(Target{Kind: TargetUser, User: &u})
		}
	}
	for _, m := range r.Mentions {
		switch m {
		case Owner, PreviousOwner:
			id := owner
			if m == PreviousOwner {
				id = previous
			}
			if id != nil {
				addUser(byID[*id])
			}
			continue
		}
		st := set[m]
		if st.Everyone != "" && st.Everyone != EveryoneNone && everyoneOffered(r.DestinationType, st.Everyone) {
			add(Target{Kind: TargetEveryone, Everyone: st.Everyone})
		}
		if r.DestinationType != TypeTelegram {
			for _, g := range st.Groups {
				add(Target{Kind: TargetGroup, Group: g})
			}
		}
		for _, id := range st.UserIDs {
			if n, err := publicid.Parse(publicid.User, id); err == nil {
				addUser(n)
			}
		}
	}
	return out, nil
}

func same(a, b Target) bool {
	if a.Kind != b.Kind {
		return false
	}
	if a.Kind == TargetUser {
		return a.User.PublicID == b.User.PublicID
	}
	return a.Everyone == b.Everyone && a.Group == b.Group
}

func appendID(ids []int64, id *int64) []int64 {
	if id == nil {
		return ids
	}
	return append(ids, *id)
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// settings reads the Destination's Mention settings when a Mention names a kind; a kind it does not set mentions
// nobody.
func (s *Service) settings(ctx context.Context, q Queries, org int64, r Request) (Settings, error) {
	if !slices.ContainsFunc(r.Mentions, func(m string) bool { return slices.Contains(Kinds, m) }) {
		return Settings{}, nil
	}
	b, err := q.GetDestinationMentions(ctx, dbgen.GetDestinationMentionsParams{OrgID: org, ID: r.DestinationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the mention settings of destination %d: %w", r.DestinationID, err)
	}
	set := Settings{}
	if err := json.Unmarshal(b, &set); err != nil {
		return nil, fmt.Errorf("read the mention settings of destination %d: %w", r.DestinationID, err)
	}
	return set, nil
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

// FooterNames are the usernames, by identity space, of the user the footer of the Alert Group groupID names — its
// Owner, who snoozed it or who resolved it — read through db, or the main pool for a nil one (C-12.FR-12).
func (s *Service) FooterNames(ctx context.Context, db DBTX, groupID int64) (map[string]string, error) {
	rows, err := s.q(db).ListFooterUsernames(ctx, dbgen.ListFooterUsernamesParams{OrgID: s.orgID, ID: groupID})
	if err != nil {
		return nil, fmt.Errorf("read the usernames of the footer of alert group %d: %w", groupID, err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.IdentitySpace] = r.Username
	}
	return out, nil
}

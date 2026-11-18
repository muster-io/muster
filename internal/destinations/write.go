// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package destinations

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/bridge"
	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/destinations/dbgen"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/mattermost"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/proxyconf"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/slack"
	"github.com/muster-io/muster/internal/telegram"
	"github.com/muster-io/muster/internal/webhooks"
)

// The Destination types.
const (
	TypeMattermost = "mattermost"
	TypeTelegram   = "telegram"
	TypeWebhook    = "webhook"
	TypeBridge     = "bridge"
	TypeSlack      = "slack"
)

// maxBridgeChannelLength is the longest channel id of a Bridge Destination (api/bridge.yaml ChannelId).
const maxBridgeChannelLength = 512

// KindDestinationButtons is the kind of the open_group_updates row that a changed button set of a Destination writes
// (C-28.FR-9), KindDestinationConnection that of a changed Connection (C-12.FR-12), and OpenGroupUpdatesChannel the
// LISTEN/NOTIFY channel that wakes the workers of internal/groups, which claim them.
const (
	KindDestinationButtons    = "destination_buttons"
	KindDestinationConnection = "destination_connection"
	OpenGroupUpdatesChannel   = "muster_open_group_updates"
)

// The Audit log actions of saved Destinations (C-03.FR-14).
const (
	ActionCreated = "destination.created"
	ActionUpdated = "destination.updated"
)

// The codes of FieldError; gateway_only refuses a Bridge Connection whose stored info declares only the role gateway
// (C-30.FR-1).
const (
	CodeRequired           = "required"
	CodeInvalidFormat      = "invalid_format"
	CodeTooLong            = "too_long"
	CodeUnknownID          = "unknown_id"
	CodeGatewayOnly        = "gateway_only"
	CodeUnsupported        = "unsupported"
	CodeCheckNotSupported  = "check_not_supported"
	CodeCheckFailed        = "destination_check_failed"
	maxNameLength          = 200
	uniqueViolation        = "23505"
	nameIndex              = "destinations_name_key"
	healthBroken           = "broken"
	pointerConnection      = "/connection_id"
	pointerChannel         = "/channel_id"
	pointerCheckNotAllowed = "/path/destination_id"
)

var (
	// ErrNameTaken is a name another Destination that is not deleted has.
	ErrNameTaken = errors.New("another destination has this name")
	// ErrUnknownConnection is a connection_id that names no Connection of the type: missing, deleted or of the other
	// messenger.
	ErrUnknownConnection = errors.New("no such connection of this type")
)

// FieldError is a field of a request that is not valid, at a JSON pointer of the request body, with a stable code of
// the validation-failed problem and, for a request template, the 1-based line and column of its error when known.
type FieldError struct {
	Pointer string
	Code    string
	Detail  string
	Line    int
	Column  int
}

func (e *FieldError) Error() string { return e.Pointer + ": " + e.Detail }

// CheckItem is one step of a Destination check with its result: when it failed, its message, in English, its code,
// which clients translate (DestinationCheckItem.code), with missing_scope the scopes the bot token lacks, which clients
// name in their own words (D396), and, on a save, the field it concerns.
type CheckItem struct {
	Name          string
	OK            bool
	Message       string
	Code          string
	MissingScopes []string
	Pointer       string
	// AbsentUsers are, with the step discussion_group_members of a Telegram Destination, the people it saw outside the
	// discussion group (C-27.FR-8).
	AbsentUsers []UserRef
}

// UserRef names a User: deactivated when disabled.
type UserRef struct {
	PublicID    string
	Name        string
	Login       string
	Deactivated bool
}

// CheckFailedError is a save that its Destination check refused: nothing was saved. Items are the failing checks.
type CheckFailedError struct {
	Items []CheckItem
}

func (e *CheckFailedError) Error() string {
	names := make([]string, len(e.Items))
	for i, it := range e.Items {
		names[i] = it.Name
	}
	return "the destination check failed: " + strings.Join(names, ", ")
}

// Limiter is a rate limit of Limit calls per PerSeconds.
type Limiter struct {
	Limit      int64 `json:"limit"`
	PerSeconds int64 `json:"per_seconds"`
}

// MattermostInput are the fields of a Mattermost Destination: its Connection by public_id, its team and its channel.
type MattermostInput struct {
	Connection string
	TeamID     string
	ChannelID  string
}

// TelegramInput are the fields of a Telegram Destination (C-14.FR-2): its Connection by public_id and its channel, a
// chat id or an @username, as entered; its discussion group is found by its Destination check.
type TelegramInput struct {
	Connection string
	ChannelID  string
}

// BridgeInput are the fields of a Bridge Destination (C-22.FR-3): its Connection by public_id and its channel id as its
// messenger names it.
type BridgeInput struct {
	Connection string
	ChannelID  string
}

// SlackInput are the fields of a Slack Destination (C-24.FR-3): its Connection by public_id and its channel id, C… or
// G…, as entered.
type SlackInput struct {
	Connection string
	ChannelID  string
}

// WebhookInput are the fields of an outgoing webhook Destination (C-15.FR-1): its mode, the request of the events
// mode, the requests of the template mode and its proxy. Only the requests of the mode are stored: the events request
// in the modes events and both, the request templates in the modes template and both.
type WebhookInput struct {
	Mode     string
	Events   *webhooks.EventsConfig
	Template *webhooks.TemplateConfig
	Proxy    proxyconf.Input
}

// ButtonSetInput is the button set of a messenger Destination as a request writes it (DestinationButtonSet): its own,
// with the items, which Items nil leaves missing, or "As in the Organization".
type ButtonSetInput struct {
	Own   bool
	Items []buttons.ItemInput
}

// Input is what createDestination and updateDestination write: the common fields and those of its type. Buttons is the
// button set of a messenger Destination, nil when the request omits it: "As in the Organization" on a creation, the
// stored set on an update; an outgoing webhook has none.
type Input struct {
	Type string
	Name string
	// MentionEntries are the Mention entries (C-28.FR-1), nil when the request omits them: none on a creation, the
	// stored ones on an update. Mentions are the L1 Mention settings, nil when the request omits them, which a request
	// without entries converts into entries until 0.3.0 (C-28.FR-5).
	MentionEntries *[]mentions.EntryInput
	Mentions       mentions.Settings
	Buttons        *ButtonSetInput
	Limiter        Limiter
	Mattermost     *MattermostInput
	Telegram       *TelegramInput
	Webhook        *WebhookInput
	Bridge         *BridgeInput
	Slack          *SlackInput
	// OwningTeamID is the public_id of the Team that owns the Destination (C-25.FR-2): nil keeps the stored one on an
	// update and is none on a creation, and an empty string clears it.
	OwningTeamID *string
	// entries are the Mention entries the save writes, validated, which validate sets.
	entries []mentions.EntryInput
}

// entriesOf are the Mention entries a validated save of in writes, as the Destination saved reads them until its
// next read says what they mention now.
func entriesOf(in Input) []mentions.Entry {
	out := make([]mentions.Entry, len(in.entries))
	for i, e := range in.entries {
		out[i] = mentions.Entry{EntryInput: e}
	}
	return out
}

// nobodyJSON is the L1 Mention settings that mention nobody, which a creation stores in the L1 column for the replicas
// of 0.1 of a rolling update (C-28.FR-5).
func nobodyJSON() json.RawMessage {
	b, _ := json.Marshal(mentions.Nobody()) // strings and lists only
	return b
}

// connectionOf is the Connection of in by public_id, empty for an outgoing webhook.
func connectionOf(in Input) string {
	switch {
	case in.Mattermost != nil:
		return in.Mattermost.Connection
	case in.Telegram != nil:
		return in.Telegram.Connection
	case in.Bridge != nil:
		return in.Bridge.Connection
	case in.Slack != nil:
		return in.Slack.Connection
	}
	return ""
}

// planMentions sets the Mention entries the save of in writes (C-28.FR-1, FR-5); stored is nil on a creation: the
// entries of the request, validated against what its Connection can mention; without them, the L1 settings of the
// request, validated as L1 validated them and converted — on a creation by mentions.FromSettings, on an update by
// mentions.MergeSettings, which keeps the entries L1 cannot express; settings that equal the L1 form of the stored
// entries change nothing and are not validated again, and the validation takes the Users the stored entries name, so
// that a read sent back with a User deleted since is not refused (C-28.FR-5, settled 2); without either, none on a
// creation and the stored ones on an update.
func (s *Service) planMentions(ctx context.Context, in *Input, stored *Destination) error {
	var before []mentions.EntryInput
	if stored != nil {
		before = mentions.Inputs(stored.MentionEntries)
	}
	switch {
	case in.MentionEntries != nil:
		out, err := s.writer.Mentions.ValidateEntries(ctx, mentions.Check{DestinationType: in.Type,
			Connection: connectionOf(*in), Entries: *in.MentionEntries, Stored: before})
		if err != nil {
			return err
		}
		in.entries = out
	case in.Mentions != nil && stored != nil && mentions.SameForm(in.Type, before, in.Mentions):
		in.entries = before
	case in.Mentions != nil:
		if err := s.writer.Mentions.Validate(ctx, in.Type, in.Mentions, before); err != nil {
			return err
		}
		if stored == nil {
			in.entries = mentions.FromSettings(in.Type, in.Mentions)
		} else {
			in.entries = mentions.MergeSettings(in.Type, before, in.Mentions)
		}
	default:
		in.entries = before
	}
	if in.entries == nil {
		in.entries = []mentions.EntryInput{}
	}
	return nil
}

// writeMentionEntries writes the Mention entries of d, saved with the id id, in the transaction of the save, when they
// differ from those of before, the zero Destination on a creation.
func (s *Service) writeMentionEntries(ctx context.Context, q TxQueries, id int64, before, d Destination) error {
	a, b := mentions.Inputs(d.MentionEntries), mentions.Inputs(before.MentionEntries)
	if slices.EqualFunc(a, b, func(x, y mentions.EntryInput) bool { return reflect.DeepEqual(x, y) }) {
		return nil
	}
	if err := s.writer.Mentions.WriteEntries(ctx, q.DB(), id, a, s.writer.Business.Now().UTC()); err != nil {
		return fmt.Errorf("write the mention entries of the destination %s: %w", d.PublicID, err)
	}
	return nil
}

// checkOwner refuses an owning Team that cannot be a Team's public_id, and writes the canonical form of one that can.
func checkOwner(in *Input) error {
	if in.OwningTeamID == nil || *in.OwningTeamID == "" {
		return nil
	}
	id, err := publicid.Parse(publicid.Team, *in.OwningTeamID)
	if err != nil {
		return unknownTeam()
	}
	in.OwningTeamID = &id
	return nil
}

func unknownTeam() *FieldError {
	return &FieldError{Pointer: "/owning_team_id", Code: CodeUnknownID, Detail: "No such Team."}
}

// planOwner sets on d the owning Team that in asks for, by its public_id only: nil keeps d's, an empty one clears it.
func planOwner(in Input, d *Destination) {
	switch {
	case in.OwningTeamID == nil:
	case *in.OwningTeamID == "":
		d.OwningTeam = nil
	default:
		d.OwningTeam = &TeamRef{PublicID: *in.OwningTeamID}
	}
}

// lockOwner reads the owning Team that in gives d, in share mode, in the transaction of the save and before the
// Destination is locked, as a Team's deletion locks the Team, then its Destinations; a Team that does not exist or is
// deleted is an unknown_id FieldError at /owning_team_id.
func (s *Service) lockOwner(ctx context.Context, q TxQueries, in Input, d *Destination) error {
	if in.OwningTeamID == nil || d.OwningTeam == nil {
		return nil
	}
	t, err := q.LockOwningTeam(ctx, dbgen.LockOwningTeamParams{OrgID: s.orgID, PublicID: d.OwningTeam.PublicID})
	if errors.Is(err, pgx.ErrNoRows) {
		return unknownTeam()
	}
	if err != nil {
		return fmt.Errorf("read the owning team %s: %w", d.OwningTeam.PublicID, err)
	}
	d.OwningTeam = &TeamRef{ID: t.ID, PublicID: t.PublicID, Name: t.Name, Slug: t.Slug}
	return nil
}

// writeOwner writes the owning Team of d, saved in the same transaction with the id id.
func (s *Service) writeOwner(ctx context.Context, q TxQueries, id int64, d Destination) error {
	p := dbgen.SetDestinationOwningTeamParams{OrgID: s.orgID, ID: id}
	if d.OwningTeam != nil {
		p.OwningTeamID = pgtype.Int8{Int64: d.OwningTeam.ID, Valid: true}
	}
	if err := q.SetDestinationOwningTeam(ctx, p); err != nil {
		return fmt.Errorf("write the owning team of the destination %s: %w", d.PublicID, err)
	}
	return nil
}

// ChannelCheck is the Destination check of a Mattermost channel through the Connection Connection (public_id), limited
// by that Connection alone, whether or not the Destination exists: its requests post nothing, and the Destination's
// limiter counts sends and edits (C-13.FR-2).
type ChannelCheck struct {
	Connection string
	TeamID     string
	ChannelID  string
}

// ChannelChecked is the result of a ChannelCheck: the id of the Connection and the check.
type ChannelChecked struct {
	ConnectionID int64
	Check        mattermost.Check
}

// MattermostChecker runs the Destination check of a Mattermost channel on the interactive path (C-13.FR-2, FR-10),
// declared by its consumer; *connections.Service implements it. A Connection that is not a Mattermost one that is not
// deleted is ErrUnknownConnection; no limiter token within the budget is a *delivery.LimitedError.
type MattermostChecker interface {
	CheckChannel(ctx context.Context, c ChannelCheck) (ChannelChecked, error)
}

// TelegramChannelCheck is the Destination check of a Telegram channel through the Connection Connection (public_id),
// limited by that Connection alone, whether or not the Destination exists: its requests post nothing, and the
// Destination's limiter counts sends and edits (C-14.FR-2). People are those whose membership of the discussion group
// its step discussion_group_members reads (C-27.FR-8), none before the Destination exists, since no Team or Route
// names it yet.
type TelegramChannelCheck struct {
	Connection string
	ChannelID  string
	People     []telegram.GroupPerson
}

// TelegramChannelChecked is the result of a TelegramChannelCheck: the id of the Connection and the check.
type TelegramChannelChecked struct {
	ConnectionID int64
	Check        telegram.DestinationCheck
}

// TelegramChecker runs the Destination check of a Telegram channel on the interactive path (C-14.FR-2, FR-14), declared
// by its consumer next to MattermostChecker; *connections.Service implements it. A Connection that is not a Telegram
// one that is not deleted is ErrUnknownConnection; no limiter token within the budget is a *delivery.LimitedError.
type TelegramChecker interface {
	CheckTelegramChannel(ctx context.Context, c TelegramChannelCheck) (TelegramChannelChecked, error)
}

// BridgeChannelCheck is the Destination check of a Bridge channel through the Connection Connection (public_id),
// limited by that Connection alone, whether or not the Destination exists: its request posts nothing, and the
// Destination's limiter counts sends and edits (C-22.FR-3).
type BridgeChannelCheck struct {
	Connection string
	ChannelID  string
}

// BridgeChannelChecked is the result of a BridgeChannelCheck: the id of the Connection and the check.
type BridgeChannelChecked struct {
	ConnectionID int64
	Check        bridge.ChannelCheck
}

// BridgeChecker runs the Destination check of a Bridge channel on the interactive path (C-22.FR-3), declared by its
// consumer next to TelegramChecker; *connections.Service implements it. A Connection that is not a Bridge Connection
// that is not deleted is ErrUnknownConnection; no limiter token within the budget is a *delivery.LimitedError.
type BridgeChecker interface {
	CheckBridgeChannel(ctx context.Context, c BridgeChannelCheck) (BridgeChannelChecked, error)
	// GatewayOnly reports whether the Connection connection (public_id) is a Bridge Connection whose stored info
	// declares only the role gateway, a Notification gateway (C-30.FR-1); any other Connection, unknown ones included,
	// is not.
	GatewayOnly(ctx context.Context, connection string) (bool, error)
}

// SlackChannelCheck is the Destination check of a Slack channel through the Connection Connection (public_id), limited
// by that Connection alone, whether or not the Destination exists: its request posts nothing, and the Destination's
// limiter counts sends and edits (C-24.FR-3).
type SlackChannelCheck struct {
	Connection string
	ChannelID  string
}

// SlackChannelChecked is the result of a SlackChannelCheck: the id of the Connection and the check.
type SlackChannelChecked struct {
	ConnectionID int64
	Check        slack.ChannelCheck
}

// SlackChecker runs the Destination check of a Slack channel on the interactive path (C-24.FR-3), declared by its
// consumer next to BridgeChecker; *connections.Service implements it. A Connection that is not a Slack Connection that
// is not deleted is ErrUnknownConnection; no limiter token within the budget is a *delivery.LimitedError.
type SlackChecker interface {
	CheckSlackChannel(ctx context.Context, c SlackChannelCheck) (SlackChannelChecked, error)
}

// MentionValidator validates the Mention settings of a Destination type (C-12.FR-8) and its Mention entries
// (C-28.FR-1, FR-2), reads the entries of Destinations with what they mention now and writes them, declared by its
// consumer; *mentions.Service implements it.
type MentionValidator interface {
	Validate(ctx context.Context, destinationType string, set mentions.Settings, stored []mentions.EntryInput) error
	ValidateEntries(ctx context.Context, c mentions.Check) ([]mentions.EntryInput, error)
	Entries(ctx context.Context, db mentions.DBTX, ds []mentions.EntryDestination) (map[int64][]mentions.Entry, error)
	WriteEntries(ctx context.Context, db mentions.DBTX, destinationID int64, entries []mentions.EntryInput,
		now time.Time) error
}

// Healthy ends the Broken state of the Destination destinationID after a successful Destination check started by a
// person (C-13.FR-10): delivery's MarkHealthy.
type Healthy func(ctx context.Context, destinationID int64) error

// Probe makes the probe of the Destination destinationID due at once when it is Broken, after a person saved it:
// delivery's ProbeNow.
type Probe func(ctx context.Context, destinationID int64) error

// writeQueries are the queries of a save of a Destination.
type writeQueries interface {
	InsertWebhookDestination(ctx context.Context, arg dbgen.InsertWebhookDestinationParams) (int64, error)
	UpdateWebhookDestination(ctx context.Context, arg dbgen.UpdateWebhookDestinationParams) error
	LockMattermostConnection(ctx context.Context, arg dbgen.LockMattermostConnectionParams) (int64, error)
	InsertMattermostDestination(ctx context.Context, arg dbgen.InsertMattermostDestinationParams) (int64, error)
	UpdateMattermostDestination(ctx context.Context, arg dbgen.UpdateMattermostDestinationParams) error
	LockTelegramConnection(ctx context.Context, arg dbgen.LockTelegramConnectionParams) (int64, error)
	InsertTelegramDestination(ctx context.Context, arg dbgen.InsertTelegramDestinationParams) (int64, error)
	UpdateTelegramDestination(ctx context.Context, arg dbgen.UpdateTelegramDestinationParams) error
	LockBridgeConnection(ctx context.Context, arg dbgen.LockBridgeConnectionParams) (int64, error)
	InsertBridgeDestination(ctx context.Context, arg dbgen.InsertBridgeDestinationParams) (int64, error)
	UpdateBridgeDestination(ctx context.Context, arg dbgen.UpdateBridgeDestinationParams) error
	LockSlackConnection(ctx context.Context, arg dbgen.LockSlackConnectionParams) (int64, error)
	InsertSlackDestination(ctx context.Context, arg dbgen.InsertSlackDestinationParams) (int64, error)
	UpdateSlackDestination(ctx context.Context, arg dbgen.UpdateSlackDestinationParams) error
	LockOwningTeam(ctx context.Context, arg dbgen.LockOwningTeamParams) (dbgen.LockOwningTeamRow, error)
	SetDestinationOwningTeam(ctx context.Context, arg dbgen.SetDestinationOwningTeamParams) error
	InsertDestinationUpdate(ctx context.Context, arg dbgen.InsertDestinationUpdateParams) error
	NotifyOpenGroupUpdates(ctx context.Context, channel string) error
	ListTelegramGroupPeople(ctx context.Context, arg dbgen.ListTelegramGroupPeopleParams) (
		[]dbgen.ListTelegramGroupPeopleRow, error)
	ListChainPagedTelegramPeople(ctx context.Context, arg dbgen.ListChainPagedTelegramPeopleParams) (
		[]dbgen.ListChainPagedTelegramPeopleRow, error)
	SetTelegramAbsences(ctx context.Context, arg dbgen.SetTelegramAbsencesParams) error
}

// view is what the Audit log diff of a saved Destination shows: never a secret, whose change is marked apart.
type view struct {
	Name       string                   `json:"name"`
	Connection *string                  `json:"connection_id,omitempty"`
	TeamID     *string                  `json:"team_id,omitempty"`
	ChannelID  *string                  `json:"channel_id,omitempty"`
	Mode       *string                  `json:"mode,omitempty"`
	Events     *webhooks.EventsConfig   `json:"events,omitempty"`
	Template   *webhooks.TemplateConfig `json:"template,omitempty"`
	Proxy      *proxyconf.Config        `json:"proxy,omitempty"`
	// MentionEntries are the Mention entries; the L1 settings are computed from them and not shown.
	MentionEntries []mentions.EntryInput `json:"mention_entries"`
	Buttons        json.RawMessage       `json:"buttons,omitempty"`
	Limiter        Limiter               `json:"limiter"`
	// OwningTeam is the public_id of the Team that owns the Destination, null for none.
	OwningTeam *string `json:"owning_team_id"`
}

func viewOf(d Destination) (view, error) {
	v := view{Name: d.Name, Connection: d.Connection, TeamID: d.MattermostTeamID, ChannelID: d.MattermostChannelID,
		Mode: d.WebhookMode, Limiter: Limiter{Limit: d.LimiterLimit, PerSeconds: d.LimiterPerSeconds}}
	if d.OwningTeam != nil {
		v.OwningTeam = &d.OwningTeam.PublicID
	}
	switch d.Type {
	case TypeTelegram:
		v.ChannelID = d.TelegramChannelID
	case TypeBridge:
		v.ChannelID = d.BridgeChannelID
	case TypeSlack:
		v.ChannelID = d.SlackChannelID
	}
	if d.Type != TypeWebhook {
		v.Buttons = buttonsView(d.Buttons)
	}
	v.MentionEntries = mentions.Inputs(d.MentionEntries)
	if len(d.EventsConfig) > 0 {
		c, err := webhooks.ParseEventsConfig(d.EventsConfig)
		if err != nil {
			return view{}, err
		}
		v.Events = &c
	}
	if len(d.TemplateConfig) > 0 {
		c, err := webhooks.ParseTemplateConfig(d.TemplateConfig)
		if err != nil {
			return view{}, err
		}
		v.Template = &c
	}
	if d.Type == TypeWebhook {
		p := proxyOf(d.Proxy)
		v.Proxy = &p
	}
	return v, nil
}

// buttonsView is the button set of a Destination as the diff shows it, one value at /buttons: {"source":
// "organization"} or {"source": "own", "items": [...]}, as the API writes it.
func buttonsView(o buttons.Own) json.RawMessage {
	v := struct {
		Source string         `json:"source"`
		Items  []buttons.Item `json:"items,omitempty"`
	}{Source: "organization"}
	if o.Set {
		v.Source, v.Items = "own", o.Items
		if v.Items == nil {
			v.Items = []buttons.Item{}
		}
	}
	b, _ := json.Marshal(v) // strings and numbers only
	return b
}

// buttonsOf validates the button set of a request into in.Buttons' stored form (C-28.FR-8): own without items is
// required at /buttons/items, an invalid item is refused at /buttons/items/{i}. ok is false when the request omits it.
func buttonsOf(in *ButtonSetInput) (own buttons.Own, ok bool, err error) {
	switch {
	case in == nil:
		return buttons.Own{}, false, nil
	case !in.Own:
		return buttons.Own{}, true, nil
	case in.Items == nil:
		return buttons.Own{}, false, &FieldError{Pointer: "/buttons/items", Code: CodeRequired,
			Detail: "An own button set lists its items."}
	}
	items, ie := buttons.Validate(in.Items)
	if ie != nil {
		return buttons.Own{}, false, &FieldError{Pointer: ie.Pointer("/buttons"), Code: ie.Code, Detail: ie.Detail}
	}
	return buttons.Own{Set: true, Items: items}, true, nil
}

// withButtons sets the button set of d from in, already validated: the set in sends, or d's when it sends none.
func withButtons(d *Destination, in Input) {
	if own, ok, _ := buttonsOf(in.Buttons); ok {
		d.Buttons = own
	}
}

// buttonsChanged, in the transaction of the save of d, asks the workers of internal/groups to re-render the open Root
// messages of d when its button set changed (C-28.FR-9): one open_group_updates row destination_buttons, requested at
// the business time of the save, and a notification that wakes them once the transaction commits.
func (s *Service) buttonsChanged(ctx context.Context, q TxQueries, before, d Destination) error {
	if bytes.Equal(before.Buttons.JSON(), d.Buttons.JSON()) {
		return nil
	}
	return s.rerenderRequested(ctx, q, KindDestinationButtons, d)
}

// connectionChanged, in the transaction of the save of d, asks the workers of internal/groups to re-render the open
// Root messages of d when its Connection changed (C-12.FR-12): the Connection decides the identity space whose footer
// a Root message shows, which may name its user by another Mention or by their name alone, so each open Root message
// whose footer reads otherwise in the new space is edited once, Quietly. One open_group_updates row
// destination_connection, as buttonsChanged writes. A save that keeps the Connection does nothing, and so does one
// whose identity space cannot change — every Telegram Connection shares one — or whose button set changed too, whose
// row destination_buttons walks the same Alert Groups.
func (s *Service) connectionChanged(ctx context.Context, q TxQueries, before, d Destination) error {
	if deref(before.Connection) == deref(d.Connection) || !spacePerConnection(d.Type) ||
		!bytes.Equal(before.Buttons.JSON(), d.Buttons.JSON()) {
		return nil
	}
	return s.rerenderRequested(ctx, q, KindDestinationConnection, d)
}

// spacePerConnection reports whether the identity space of a Destination type is that of its Connection
// (mentions.IdentitySpace): of every messenger but Telegram, whose Account links form one space whatever the
// Connection.
func spacePerConnection(destinationType string) bool {
	one, two := int64(1), int64(2)
	return mentions.IdentitySpace(destinationType, &one) != mentions.IdentitySpace(destinationType, &two)
}

// rerenderRequested inserts the open_group_updates row of the kind given for d, requested at the business time of the
// save, and a notification that wakes the workers once the transaction commits.
func (s *Service) rerenderRequested(ctx context.Context, q TxQueries, kind string, d Destination) error {
	if err := q.InsertDestinationUpdate(ctx, dbgen.InsertDestinationUpdateParams{OrgID: s.orgID, Kind: kind,
		DestinationID: pgtype.Int8{Int64: d.ID, Valid: true},
		RequestedAt:   s.writer.Business.Now().UTC()}); err != nil {
		return fmt.Errorf("re-render the root messages of the destination %s: %w", d.PublicID, err)
	}
	if err := q.NotifyOpenGroupUpdates(ctx, OpenGroupUpdatesChannel); err != nil {
		return fmt.Errorf("wake the open alert group update workers: %w", err)
	}
	return nil
}

// proxyOf is the stored proxy object of a Destination as read.
func proxyOf(p Proxy) proxyconf.Config {
	c := proxyconf.Config{Enabled: p.Enabled, Username: p.Username}
	if p.Type != nil {
		c.Type = *p.Type
	}
	if p.Address != nil {
		c.Address = *p.Address
	}
	return c
}

// validate checks the common fields of in, the fields of its type, its Mention entries or settings and the button set
// of a messenger Destination, before the Destination check; stored is nil on a creation.
func (s *Service) validate(ctx context.Context, in *Input, stored *Destination) error {
	if err := s.validateFields(ctx, in, stored); err != nil {
		return err
	}
	if err := s.planMentions(ctx, in, stored); err != nil {
		return err
	}
	if in.Type == TypeWebhook {
		in.Buttons = nil
		return nil
	}
	_, _, err := buttonsOf(in.Buttons)
	return err
}

// validateFields checks the common fields of in and the fields of its type.
func (s *Service) validateFields(ctx context.Context, in *Input, stored *Destination) error {
	switch {
	case stored != nil && stored.Type != in.Type:
		return &FieldError{Pointer: "/type", Code: CodeInvalidFormat, Detail: "The type of a Destination cannot change."}
	case in.Type == TypeTelegram && in.Telegram != nil:
		return s.validateTelegram(in)
	case in.Type == TypeTelegram:
		return &FieldError{Pointer: "/type", Code: CodeInvalidFormat, Detail: "The fields of the type are missing."}
	case in.Type == TypeBridge && in.Bridge != nil:
		return s.validateBridge(ctx, in, stored)
	case in.Type == TypeBridge:
		return &FieldError{Pointer: "/type", Code: CodeInvalidFormat, Detail: "The fields of the type are missing."}
	case in.Type == TypeSlack && in.Slack != nil:
		return s.validateSlack(in)
	case in.Type == TypeSlack:
		return &FieldError{Pointer: "/type", Code: CodeInvalidFormat, Detail: "The fields of the type are missing."}
	case in.Type == TypeWebhook && in.Webhook != nil:
		if err := validateCommon(in); err != nil {
			return err
		}
		if err := s.validateWebhook(in.Webhook); err != nil {
			return err
		}
		return nil
	case in.Type != TypeMattermost || in.Mattermost == nil:
		return &FieldError{Pointer: "/type", Code: CodeInvalidFormat, Detail: "The type is not mattermost."}
	}
	if err := validateCommon(in); err != nil {
		return err
	}
	m := in.Mattermost
	m.TeamID, m.ChannelID = strings.TrimSpace(m.TeamID), strings.TrimSpace(m.ChannelID)
	switch {
	case m.TeamID == "":
		return &FieldError{Pointer: "/team_id", Code: CodeRequired, Detail: "The team is empty."}
	case m.ChannelID == "":
		return &FieldError{Pointer: pointerChannel, Code: CodeRequired, Detail: "The channel is empty."}
	}
	conn, err := publicid.Parse(publicid.Connection, m.Connection)
	if err != nil {
		return unknownConnection()
	}
	m.Connection = conn
	return nil
}

// validateCommon checks the name and the limiter of in, trimming the name.
func validateCommon(in *Input) error {
	in.Name = strings.TrimSpace(in.Name)
	switch {
	case in.Name == "":
		return &FieldError{Pointer: "/name", Code: CodeRequired, Detail: "The name is empty."}
	case utf8.RuneCountInString(in.Name) > maxNameLength:
		return &FieldError{Pointer: "/name", Code: CodeTooLong,
			Detail: fmt.Sprintf("The name is longer than %d characters.", maxNameLength)}
	case in.Limiter.Limit < 1 || in.Limiter.PerSeconds < 1:
		return &FieldError{Pointer: "/limiter", Code: CodeInvalidFormat,
			Detail: "The limiter needs a limit and a period of at least 1."}
	}
	return nil
}

// validateWebhook checks the fields of an outgoing webhook (C-15.FR-1, FR-3): its mode, and the requests the mode
// sends — the events request in the modes events and both, the request templates in the modes template and both —
// parsed and run on a dry run in the template sandbox. The requests of the other mode are dropped.
func (s *Service) validateWebhook(w *WebhookInput) error {
	events := w.Mode == webhooks.ModeEvents || w.Mode == webhooks.ModeBoth
	template := w.Mode == webhooks.ModeTemplate || w.Mode == webhooks.ModeBoth
	switch {
	case !events && !template:
		return &FieldError{Pointer: "/mode", Code: CodeInvalidFormat, Detail: "The mode is events, template or both."}
	case events && w.Events == nil:
		return &FieldError{Pointer: "/events", Code: CodeRequired, Detail: "The mode needs the events request."}
	case template && w.Template == nil:
		return &FieldError{Pointer: "/template", Code: CodeRequired, Detail: "The mode needs the request templates."}
	}
	if !events {
		w.Events = nil
	}
	if !template {
		w.Template = nil
	}
	var err error
	if events {
		err = webhooks.Validate(s.writer.Templates, "/events", w.Events)
	}
	if err == nil && template {
		err = webhooks.ValidateTemplate(s.writer.Templates, "/template", w.Template,
			s.writer.Business.Now().UTC())
	}
	if fe, ok := errors.AsType[*webhooks.FieldError](err); ok {
		return &FieldError{Pointer: fe.Pointer, Code: fe.Code, Detail: fe.Detail, Line: fe.Line, Column: fe.Column}
	}
	return err
}

// webhookConfigs are the stored requests of w: the events request and the request templates, nil for those its mode
// does not send.
func webhookConfigs(w *WebhookInput) (events, template json.RawMessage) {
	if w.Events != nil {
		events = w.Events.JSON()
	}
	if w.Template != nil {
		template = w.Template.JSON()
	}
	return events, template
}

// webhookSecrets are what a save of an outgoing webhook stores beside its fields: the proxy object after the input,
// the proxy password and whether the input gave one, and the Audit log changes of the secrets.
type webhookSecrets struct {
	proxy    proxyconf.Config
	password keyring.StoredSecret
	given    bool
	diff     []audit.Change
}

// applyWebhook applies the proxy of w to the stored one, passwordSet telling whether a password is stored.
func (s *Service) applyWebhook(w *WebhookInput, stored proxyconf.Config, passwordSet bool, now time.Time) (
	webhookSecrets, error) {
	p, err := w.Proxy.Apply(stored)
	if fe, ok := errors.AsType[*proxyconf.FieldError](err); ok {
		return webhookSecrets{}, &FieldError{Pointer: "/proxy" + fe.Pointer, Code: fe.Code, Detail: fe.Detail}
	}
	if err != nil {
		return webhookSecrets{}, err
	}
	var current keyring.StoredSecret
	if passwordSet {
		current.Ciphertext = []byte{1} // only whether it is set matters: a kept password is not written
	}
	password, changed, err := s.writer.Keyring.ApplySecret(webhooks.FieldProxyPassword, current, w.Proxy.Password,
		now)
	if errors.Is(err, keyring.ErrEmptySecret) {
		return webhookSecrets{}, &FieldError{Pointer: "/proxy/password", Code: CodeRequired,
			Detail: "The proxy password is empty; send null to clear it."}
	}
	if err != nil {
		return webhookSecrets{}, err
	}
	out := webhookSecrets{proxy: p, password: password, given: w.Proxy.Password.Given}
	if changed {
		out.diff = append(out.diff, audit.Change{Pointer: "/proxy/password", SecretChanged: true})
	}
	return out, nil
}

// createWebhook creates an outgoing webhook Destination (C-15.FR-1, FR-5) with its first Signing secret, which the
// Destination returned carries once. It is recorded as destination.created.
func (s *Service) createWebhook(ctx context.Context, r Requester, in Input) (Destination, error) {
	now := s.writer.Business.Now().UTC()
	sec, err := s.applyWebhook(in.Webhook, proxyconf.Config{}, false, now)
	if err != nil {
		return Destination{}, err
	}
	secret := webhooks.NewSigningSecret()
	signing, _, err := s.writer.Keyring.ApplySecret(webhooks.FieldSigningSecret, keyring.StoredSecret{},
		keyring.Replace(secret), now)
	if err != nil {
		return Destination{}, err
	}
	mode := in.Webhook.Mode
	events, template := webhookConfigs(in.Webhook)
	d := Destination{PublicID: publicid.New(publicid.Destination), Type: in.Type, Name: in.Name, WebhookMode: &mode,
		EventsConfig: events, TemplateConfig: template, Proxy: proxyRead(sec.proxy, sec.password),
		SigningSecret: SigningSecret{Set: true, UpdatedAt: &now}, SigningSecretOnce: secret, MentionEntries: entriesOf(in),
		LimiterLimit: in.Limiter.Limit, LimiterPerSeconds: in.Limiter.PerSeconds, Health: Health{State: "healthy"},
		Routes: []RouteRef{}, CreatedAt: now, Version: 1}
	planOwner(in, &d)
	after, err := viewOf(d)
	if err != nil {
		return Destination{}, err
	}
	err = s.writer.Writer.InTx(ctx, func(q TxQueries) error {
		if err := s.lockOwner(ctx, q, in, &d); err != nil {
			return err
		}
		p := dbgen.InsertWebhookDestinationParams{OrgID: s.orgID, PublicID: d.PublicID, Name: d.Name,
			WebhookMode: text(d.WebhookMode), WebhookEventsConfig: d.EventsConfig,
			WebhookTemplateConfig: d.TemplateConfig, Proxy: sec.proxy.JSON(), SigningSecretCiphertext: signing.Ciphertext, SigningSecretKeyID: nonEmpty(signing.KeyID), Now: now,
			Mentions: nobodyJSON(), LimiterLimit: d.LimiterLimit, LimiterPerSeconds: d.LimiterPerSeconds}
		if sec.password.Set() {
			p.ProxyPasswordCiphertext, p.ProxyPasswordKeyID = sec.password.Ciphertext, nonEmpty(sec.password.KeyID)
			p.ProxyPasswordUpdatedAt = pgtype.Timestamptz{Time: now, Valid: true}
		}
		id, err := q.InsertWebhookDestination(ctx, p)
		if err != nil {
			return fmt.Errorf("create the destination: %w", nameTaken(err))
		}
		d.ID = id
		if err := s.writeOwner(ctx, q, id, d); err != nil {
			return err
		}
		if err := s.writeMentionEntries(ctx, q, id, Destination{}, d); err != nil {
			return err
		}
		return s.record(ctx, q, r, ActionCreated, d, append(audit.Created(after), sec.diff...))
	})
	if err != nil {
		return Destination{}, err
	}
	return d, nil
}

// updateWebhook replaces the configured fields of the outgoing webhook before, already validated; its Signing secrets
// and Secrets change through their own operations. An update that changes nothing writes nothing. Saving a Broken one
// makes its probe due at once, changed or not.
func (s *Service) updateWebhook(ctx context.Context, r Requester, before Destination, in Input) (Destination,
	error) {
	now := s.writer.Business.Now().UTC()
	sec, err := s.applyWebhook(in.Webhook, proxyOf(before.Proxy), before.Proxy.PasswordSet, now)
	if err != nil {
		return Destination{}, err
	}
	d := before
	mode := in.Webhook.Mode
	d.Name, d.WebhookMode, d.MentionEntries = in.Name, &mode, entriesOf(in)
	d.EventsConfig, d.TemplateConfig = webhookConfigs(in.Webhook)
	d.LimiterLimit, d.LimiterPerSeconds = in.Limiter.Limit, in.Limiter.PerSeconds
	d.Proxy = proxyRead(sec.proxy, sec.password)
	if !sec.given {
		d.Proxy.PasswordSet, d.Proxy.PasswordUpdatedAt = before.Proxy.PasswordSet, before.Proxy.PasswordUpdatedAt
	}
	old, err := viewOf(before)
	if err != nil {
		return Destination{}, err
	}
	planOwner(in, &d)
	after, err := viewOf(d)
	if err != nil {
		return Destination{}, err
	}
	diff := append(audit.Diff(old, after), sec.diff...)
	if len(diff) == 0 && !sec.given {
		return d, s.probe(ctx, before)
	}
	err = s.writer.Writer.InTx(ctx, func(q TxQueries) error {
		if err := s.lockOwner(ctx, q, in, &d); err != nil {
			return err
		}
		lock, err := q.LockDestination(ctx, dbgen.LockDestinationParams{OrgID: s.orgID, PublicID: before.PublicID})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock the destination %s: %w", before.PublicID, err)
		}
		if lock.Version != before.Version {
			return ErrVersionMismatch
		}
		p := dbgen.UpdateWebhookDestinationParams{Name: d.Name, WebhookMode: text(d.WebhookMode),
			WebhookEventsConfig: d.EventsConfig, WebhookTemplateConfig: d.TemplateConfig, Proxy: sec.proxy.JSON(),
			PasswordGiven: sec.given, Now: now,
			LimiterLimit: d.LimiterLimit, LimiterPerSeconds: d.LimiterPerSeconds, OrgID: s.orgID,
			ID: before.ID}
		if sec.password.Set() {
			p.ProxyPasswordCiphertext, p.ProxyPasswordKeyID = sec.password.Ciphertext, nonEmpty(sec.password.KeyID)
		}
		if err := q.UpdateWebhookDestination(ctx, p); err != nil {
			return fmt.Errorf("update the destination %s: %w", before.PublicID, nameTaken(err))
		}
		d.Version++
		if err := s.writeOwner(ctx, q, before.ID, d); err != nil {
			return err
		}
		if err := s.writeMentionEntries(ctx, q, before.ID, before, d); err != nil {
			return err
		}
		if err := s.renamed(ctx, q, before, d); err != nil {
			return err
		}
		if len(diff) == 0 {
			return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint, ID: d.PublicID})
		}
		return s.record(ctx, q, r, ActionUpdated, d, diff)
	})
	if err != nil {
		return Destination{}, err
	}
	return d, s.probe(ctx, before)
}

// probe makes the probe of the outgoing webhook before due at once when it was Broken: an outgoing webhook has no
// Destination check that a save could run, and the save may have fixed it, such as its URL.
func (s *Service) probe(ctx context.Context, before Destination) error {
	if before.Health.State != healthBroken || s.writer.Probe == nil {
		return nil
	}
	if err := s.writer.Probe(ctx, before.ID); err != nil {
		return fmt.Errorf("probe destination %s: %w", before.PublicID, err)
	}
	return nil
}

// renamed gives the Internal alerts about the Destination its new name, in the transaction of the rename; a save
// that keeps the name does nothing.
func (s *Service) renamed(ctx context.Context, q TxQueries, before, after Destination) error {
	if before.Name == after.Name || s.writer.Renamed == nil {
		return nil
	}
	if err := s.writer.Renamed(ctx, q.DB(), after.PublicID, after.Name); err != nil {
		return fmt.Errorf("rename the internal alerts of %s: %w", after.PublicID, err)
	}
	return nil
}

// proxyRead is the proxy of an outgoing webhook as read after a save.
func proxyRead(c proxyconf.Config, password keyring.StoredSecret) Proxy {
	p := Proxy{Enabled: c.Enabled, Username: c.Username, PasswordSet: password.Set(), PasswordUpdatedAt: password.UpdatedAt}
	if c.Type != "" {
		t := c.Type
		p.Type = &t
	}
	if c.Address != "" {
		a := c.Address
		p.Address = &a
	}
	return p
}

func nonEmpty(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

func unknownConnection() *FieldError {
	return &FieldError{Pointer: pointerConnection, Code: CodeUnknownID, Detail: "No such Mattermost Connection."}
}

// idOf is the id p points to, 0 for nil.
func idOf(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func unknownTelegramConnection() *FieldError {
	return &FieldError{Pointer: pointerConnection, Code: CodeUnknownID, Detail: "No such Telegram Connection."}
}

// check runs the Destination check of in for the Destination destinationID, nil before it exists, and refuses a failing
// one with a *CheckFailedError: one item per failing check at the field it concerns. Its requests are recorded as
// destination_check under a check run of their own (C-23.FR-2).
func (s *Service) check(ctx context.Context, in Input, destinationID *int64) (ChannelChecked, error) {
	ctx, _ = outbound.StartCheck(ctx, outbound.CheckDestination, idOf(destinationID))
	m := in.Mattermost
	res, err := s.writer.Mattermost.CheckChannel(ctx, ChannelCheck{Connection: m.Connection, TeamID: m.TeamID,
		ChannelID: m.ChannelID})
	if errors.Is(err, ErrUnknownConnection) {
		return ChannelChecked{}, unknownConnection()
	}
	if err != nil {
		return ChannelChecked{}, err
	}
	if res.Check.OK() {
		return res, nil
	}
	failed := &CheckFailedError{}
	for _, st := range res.Check.Steps {
		if st.OK {
			continue
		}
		pointer := pointerChannel
		if st.Name == mattermost.StepToken {
			pointer = pointerConnection
		}
		failed.Items = append(failed.Items, CheckItem{Name: st.Name, Message: st.Message, Code: st.Code,
			Pointer: pointer})
	}
	return ChannelChecked{}, failed
}

// Create creates a Mattermost Destination (C-13.FR-2, FR-3, C-11.FR-18) once its Destination check passed on the
// interactive path; the check reads the names of its team and channel, which the Destination keeps. A Telegram
// Destination is created the same way from its channel alone, keeping the discussion group its check found
// (C-14.FR-2), and a Bridge or Slack Destination from its channel id, keeping the channel's name its check read
// (C-22.FR-3, C-24.FR-3). An outgoing webhook in the events mode has no check: it is created with its first Signing
// secret (C-15.FR-1, FR-5). It is recorded as destination.created.
func (s *Service) Create(ctx context.Context, r Requester, in Input) (Destination, error) {
	d, err := s.create(ctx, r, in)
	return s.described(ctx, d, err)
}

// described is the Destination d of a save that succeeded, with what its Mention entries mention now.
func (s *Service) described(ctx context.Context, d Destination, err error) (Destination, error) {
	if err != nil {
		return Destination{}, err
	}
	list := []Destination{d}
	if err := s.withEntries(ctx, list); err != nil {
		return Destination{}, err
	}
	return list[0], nil
}

func (s *Service) create(ctx context.Context, r Requester, in Input) (Destination, error) {
	if err := s.validate(ctx, &in, nil); err != nil {
		return Destination{}, err
	}
	if err := checkOwner(&in); err != nil {
		return Destination{}, err
	}
	switch in.Type {
	case TypeWebhook:
		return created(s.createWebhook(ctx, r, in))
	case TypeTelegram:
		return created(s.createTelegram(ctx, r, in))
	case TypeBridge:
		return created(s.createBridge(ctx, r, in))
	case TypeSlack:
		return created(s.createSlack(ctx, r, in))
	}
	checked, err := s.check(ctx, in, nil)
	if err != nil {
		return Destination{}, err
	}
	now := s.writer.Business.Now().UTC()
	conn := in.Mattermost.Connection
	d := Destination{PublicID: publicid.New(publicid.Destination), Type: in.Type, Name: in.Name, Connection: &conn,
		MattermostTeamID: &in.Mattermost.TeamID, MattermostChannelID: &in.Mattermost.ChannelID,
		MattermostTeamName: &checked.Check.TeamName, MattermostChannelName: &checked.Check.ChannelName, MentionEntries: entriesOf(in),
		LimiterLimit: in.Limiter.Limit, LimiterPerSeconds: in.Limiter.PerSeconds, Health: Health{State: "healthy"},
		Routes: []RouteRef{}, CreatedAt: now, Version: 1}
	planOwner(in, &d)
	withButtons(&d, in)
	after, err := viewOf(d)
	if err != nil {
		return Destination{}, err
	}
	err = s.writer.Writer.InTx(ctx, func(q TxQueries) error {
		if err := s.lockOwner(ctx, q, in, &d); err != nil {
			return err
		}
		if err := lockConnection(ctx, q, s.orgID, checked.ConnectionID); err != nil {
			return err
		}
		id, err := q.InsertMattermostDestination(ctx, dbgen.InsertMattermostDestinationParams{OrgID: s.orgID,
			PublicID: d.PublicID, Name: d.Name, ConnectionID: pgtype.Int8{Int64: checked.ConnectionID, Valid: true},
			TeamID: text(d.MattermostTeamID), ChannelID: text(d.MattermostChannelID),
			TeamName: text(d.MattermostTeamName), ChannelName: text(d.MattermostChannelName), Mentions: nobodyJSON(),
			Buttons:      d.Buttons.JSON(),
			LimiterLimit: d.LimiterLimit, LimiterPerSeconds: d.LimiterPerSeconds, Now: now})
		if err != nil {
			return fmt.Errorf("create the destination: %w", nameTaken(err))
		}
		d.ID = id
		if err := s.writeOwner(ctx, q, id, d); err != nil {
			return err
		}
		if err := s.writeMentionEntries(ctx, q, id, Destination{}, d); err != nil {
			return err
		}
		return s.record(ctx, q, r, ActionCreated, d, audit.Created(after))
	})
	return created(d, err)
}

// created creates the counter series of the Destination d that was just created, at once on this replica; the
// others create them when the hint of the creation refreshes their info.
func created(d Destination, err error) (Destination, error) {
	if err != nil {
		return Destination{}, err
	}
	exportCounters(d.PublicID, d.Type)
	return d, nil
}

// Update replaces the configured fields of the Destination publicID (C-13.FR-2, FR-3, C-14.FR-2, C-22.FR-3,
// C-24.FR-3); a non-nil version must be its current one (If-Match). Saving runs the Destination check as Create does,
// limited by the Destination's Connection alone, since a check posts nothing; a passing check of a Broken Destination
// ends its Broken state (C-13.FR-10, C-14.FR-14). An update that changes nothing writes nothing.
func (s *Service) Update(ctx context.Context, r Requester, publicID string, version *int64, in Input) (Destination,
	error) {
	d, err := s.update(ctx, r, publicID, version, in)
	return s.described(ctx, d, err)
}

func (s *Service) update(ctx context.Context, r Requester, publicID string, version *int64, in Input) (Destination,
	error) {
	before, err := s.Get(ctx, publicID)
	if err != nil {
		return Destination{}, err
	}
	if version != nil && *version != before.Version {
		return Destination{}, ErrVersionMismatch
	}
	if err := s.validate(ctx, &in, &before); err != nil {
		return Destination{}, err
	}
	if err := checkOwner(&in); err != nil {
		return Destination{}, err
	}
	switch in.Type {
	case TypeWebhook:
		return s.updateWebhook(ctx, r, before, in)
	case TypeTelegram:
		return s.updateTelegram(ctx, r, before, in)
	case TypeBridge:
		return s.updateBridge(ctx, r, before, in)
	case TypeSlack:
		return s.updateSlack(ctx, r, before, in)
	}
	checked, err := s.check(ctx, in, &before.ID)
	if err != nil {
		return Destination{}, err
	}
	d := before
	conn := in.Mattermost.Connection
	d.Name, d.Connection, d.MattermostTeamID, d.MattermostChannelID = in.Name, &conn, &in.Mattermost.TeamID,
		&in.Mattermost.ChannelID
	d.MattermostTeamName, d.MattermostChannelName = &checked.Check.TeamName, &checked.Check.ChannelName
	d.MentionEntries, d.LimiterLimit, d.LimiterPerSeconds = entriesOf(in), in.Limiter.Limit, in.Limiter.PerSeconds
	withButtons(&d, in)
	old, err := viewOf(before)
	if err != nil {
		return Destination{}, err
	}
	planOwner(in, &d)
	after, err := viewOf(d)
	if err != nil {
		return Destination{}, err
	}
	diff := audit.Diff(old, after)
	namesChanged := deref(before.MattermostTeamName) != checked.Check.TeamName ||
		deref(before.MattermostChannelName) != checked.Check.ChannelName
	if len(diff) > 0 || namesChanged {
		err = s.writer.Writer.InTx(ctx, func(q TxQueries) error {
			if err := s.lockOwner(ctx, q, in, &d); err != nil {
				return err
			}
			lock, err := q.LockDestination(ctx, dbgen.LockDestinationParams{OrgID: s.orgID, PublicID: before.PublicID})
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return fmt.Errorf("lock the destination %s: %w", before.PublicID, err)
			}
			if lock.Version != before.Version {
				return ErrVersionMismatch
			}
			if err := lockConnection(ctx, q, s.orgID, checked.ConnectionID); err != nil {
				return err
			}
			now := s.writer.Business.Now().UTC()
			if err := q.UpdateMattermostDestination(ctx, dbgen.UpdateMattermostDestinationParams{OrgID: s.orgID,
				ID: before.ID, Name: d.Name, ConnectionID: pgtype.Int8{Int64: checked.ConnectionID, Valid: true},
				TeamID: text(d.MattermostTeamID), ChannelID: text(d.MattermostChannelID),
				TeamName: text(d.MattermostTeamName), ChannelName: text(d.MattermostChannelName),
				Buttons:      d.Buttons.JSON(),
				LimiterLimit: d.LimiterLimit, LimiterPerSeconds: d.LimiterPerSeconds, Now: now}); err != nil {
				return fmt.Errorf("update the destination %s: %w", before.PublicID, nameTaken(err))
			}
			d.Version++
			if err := s.writeOwner(ctx, q, before.ID, d); err != nil {
				return err
			}
			if err := s.writeMentionEntries(ctx, q, before.ID, before, d); err != nil {
				return err
			}
			if err := s.buttonsChanged(ctx, q, before, d); err != nil {
				return err
			}
			if err := s.connectionChanged(ctx, q, before, d); err != nil {
				return err
			}
			if err := s.renamed(ctx, q, before, d); err != nil {
				return err
			}
			if len(diff) == 0 {
				return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint, ID: d.PublicID})
			}
			return s.record(ctx, q, r, ActionUpdated, d, diff)
		})
		if err != nil {
			return Destination{}, err
		}
	}
	if before.Health.State == healthBroken {
		if err := s.healthy(ctx, before.ID); err != nil {
			return Destination{}, err
		}
		return s.Get(ctx, publicID)
	}
	return d, nil
}

// CheckResult is the result of checkDestination: whether every check passed, each check, the health after it, and the
// check run its requests share in the Request log (C-23.FR-5).
type CheckResult struct {
	OK     bool
	Items  []CheckItem
	Health Health
	Run    string
}

// Check runs the Destination check of the Destination publicID from checkDestination (C-13.FR-10, C-14.FR-14,
// C-22.FR-3, C-24.FR-3) on the interactive path, limited by its Connection alone, since a check posts nothing: each
// check with its result. A passing check of a Broken Destination ends its Broken state. A Bridge or Slack
// Destination's check keeps no channel name it reads: a save does. A Telegram Destination's check that passes keeps
// what its step discussion_group_members saw of the people of its Teams and of those its chains Page (C-27.FR-8), with
// them in the item's AbsentUsers.
// A type without a Destination check here is a *FieldError check_not_supported. Its requests are recorded as the kind
// destination_check with the Destination under one check run, which the result names (C-23.FR-2, FR-5).
func (s *Service) Check(ctx context.Context, publicID string) (CheckResult, error) {
	d, err := s.Get(ctx, publicID)
	if err != nil {
		return CheckResult{}, err
	}
	ctx, run := outbound.StartCheck(ctx, outbound.CheckDestination, d.ID)
	var out CheckResult
	switch {
	case d.Type == TypeMattermost && d.Connection != nil:
		res, err := s.writer.Mattermost.CheckChannel(ctx, ChannelCheck{Connection: *d.Connection,
			TeamID: deref(d.MattermostTeamID), ChannelID: deref(d.MattermostChannelID)})
		if err != nil {
			return CheckResult{}, err
		}
		out = CheckResult{OK: res.Check.OK(), Health: d.Health}
		for _, st := range res.Check.Steps {
			out.Items = append(out.Items, CheckItem{Name: st.Name, OK: st.OK, Message: st.Message, Code: st.Code})
		}
	case d.Type == TypeTelegram && d.Connection != nil:
		members, refs, err := s.telegramGroupPeople(ctx, &d.ID)
		if err != nil {
			return CheckResult{}, err
		}
		res, err := s.writer.Telegram.CheckTelegramChannel(ctx, TelegramChannelCheck{Connection: *d.Connection,
			ChannelID: deref(d.TelegramChannelID), People: members})
		if err != nil {
			return CheckResult{}, err
		}
		out = CheckResult{OK: res.Check.OK(), Health: d.Health}
		for _, st := range res.Check.Steps {
			item := CheckItem{Name: st.Name, OK: st.OK, Message: st.Message, Code: st.Code}
			if st.Name == telegram.StepDiscussionGroupMembers {
				item.AbsentUsers = absentUsers(st.Absent, refs)
			}
			out.Items = append(out.Items, item)
		}
		// A group other than the stored one fails the check until the Destination is saved again, since its Thread
		// replies go to the stored group.
		if group := strconv.FormatInt(res.Check.GroupID, 10); out.OK && d.TelegramDiscussionGroupID != nil &&
			*d.TelegramDiscussionGroupID != group {
			out.OK = false
			for i := range out.Items {
				if out.Items[i].Name == telegram.StepDiscussionGroup {
					out.Items[i].OK, out.Items[i].Message, out.Items[i].Code = false, telegram.MessageMoved,
						telegram.CodeMoved
				}
			}
		}
		// What the check saw of another group than the stored one is kept only once a save stores that group, which
		// the write checks against the stored group.
		if err := s.recordGroupPeople(ctx, d.ID, res.Check); err != nil {
			return CheckResult{}, err
		}
	case d.Type == TypeBridge && d.Connection != nil:
		res, err := s.writer.Bridge.CheckBridgeChannel(ctx, BridgeChannelCheck{Connection: *d.Connection,
			ChannelID: deref(d.BridgeChannelID)})
		if err != nil {
			return CheckResult{}, err
		}
		c := res.Check
		out = CheckResult{OK: c.OK(), Health: d.Health, Items: []CheckItem{{Name: bridge.ItemChannel, OK: c.OK(),
			Message: c.Message, Code: c.Code}}}
	case d.Type == TypeSlack && d.Connection != nil:
		res, err := s.writer.Slack.CheckSlackChannel(ctx, SlackChannelCheck{Connection: *d.Connection,
			ChannelID: deref(d.SlackChannelID)})
		if err != nil {
			return CheckResult{}, err
		}
		c := res.Check
		out = CheckResult{OK: c.OK(), Health: d.Health, Items: []CheckItem{{Name: slack.ItemChannel, OK: c.OK(),
			Message: c.Message, Code: c.Code, MissingScopes: c.MissingScopes}}}
	default:
		return CheckResult{}, &FieldError{Pointer: pointerCheckNotAllowed, Code: CodeCheckNotSupported,
			Detail: "This type of Destination has no Destination check."}
	}
	out.Run = run
	if out.OK && d.Health.State == healthBroken {
		if err := s.healthy(ctx, d.ID); err != nil {
			return CheckResult{}, err
		}
		if d, err = s.Get(ctx, publicID); err != nil {
			return CheckResult{}, err
		}
		out.Health = d.Health
	}
	return out, nil
}

// healthy ends the Broken state of the Destination id through delivery.
func (s *Service) healthy(ctx context.Context, id int64) error {
	if s.writer.Healthy == nil {
		return nil
	}
	if err := s.writer.Healthy(ctx, id); err != nil {
		return fmt.Errorf("end the broken state of destination %d: %w", id, err)
	}
	return nil
}

// record writes the Audit log entry of a save of d and its live hint.
func (s *Service) record(ctx context.Context, q TxQueries, r Requester, action string, d Destination,
	diff []audit.Change) error {
	if err := s.writer.Audit.Record(ctx, q, audit.Entry{OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport,
		Action: action, Resource: audit.Resource{Type: ResourceDestination, PublicID: d.PublicID, Name: d.Name},
		Diff: diff, SourceAddress: r.Address}); err != nil {
		return err
	}
	return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint, ID: d.PublicID})
}

// lockConnection takes the Mattermost Connection id in share mode; a Connection deleted since the check is
// ErrUnknownConnection's field error.
func lockConnection(ctx context.Context, q TxQueries, orgID, id int64) error {
	_, err := q.LockMattermostConnection(ctx, dbgen.LockMattermostConnectionParams{OrgID: orgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return unknownConnection()
	}
	if err != nil {
		return fmt.Errorf("lock the connection %d: %w", id, err)
	}
	return nil
}

// nameTaken turns the refusal of the unique index of the names into ErrNameTaken.
func nameTaken(err error) error {
	if pe, ok := errors.AsType[*pgconn.PgError](err); ok && pe.Code == uniqueViolation && pe.ConstraintName == nameIndex {
		return ErrNameTaken
	}
	return err
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// telegramChannel is a Telegram chat id or the @username of a public channel.
var telegramChannel = regexp.MustCompile(`^(-?[1-9][0-9]{0,19}|@[A-Za-z][A-Za-z0-9_]{3,31})$`)

// validateTelegram checks the common fields of in and its channel, a chat id or an @username, trimmed, and its
// Connection.
func (s *Service) validateTelegram(in *Input) error {
	if err := validateCommon(in); err != nil {
		return err
	}
	t := in.Telegram
	t.ChannelID = strings.TrimSpace(t.ChannelID)
	switch {
	case t.ChannelID == "":
		return &FieldError{Pointer: pointerChannel, Code: CodeRequired, Detail: "The channel is empty."}
	case !telegramChannel.MatchString(t.ChannelID):
		return &FieldError{Pointer: pointerChannel, Code: CodeInvalidFormat,
			Detail: "The channel is a chat id, such as -1001234567890, or an @username."}
	}
	conn, err := publicid.Parse(publicid.Connection, t.Connection)
	if err != nil {
		return unknownTelegramConnection()
	}
	t.Connection = conn
	return nil
}

// checkTelegram runs the Destination check of the Telegram channel of in for the Destination destinationID, nil before
// it exists, and refuses a failing one with a *CheckFailedError: the failing step at /channel_id, or at
// /connection_id when the bot token was refused. Its requests are recorded as destination_check under a check run of
// their own (C-23.FR-2).
func (s *Service) checkTelegram(ctx context.Context, in Input, destinationID *int64) (TelegramChannelChecked, error) {
	members, _, err := s.telegramGroupPeople(ctx, destinationID)
	if err != nil {
		return TelegramChannelChecked{}, err
	}
	ctx, _ = outbound.StartCheck(ctx, outbound.CheckDestination, idOf(destinationID))
	t := in.Telegram
	res, err := s.writer.Telegram.CheckTelegramChannel(ctx, TelegramChannelCheck{Connection: t.Connection,
		ChannelID: t.ChannelID, People: members})
	if errors.Is(err, ErrUnknownConnection) {
		return TelegramChannelChecked{}, unknownTelegramConnection()
	}
	if err != nil {
		return TelegramChannelChecked{}, err
	}
	if res.Check.OK() {
		return res, nil
	}
	failed := &CheckFailedError{}
	for _, st := range res.Check.Steps {
		if st.OK {
			continue
		}
		pointer := pointerChannel
		if st.Code == telegram.CodeTokenInvalid {
			pointer = pointerConnection
		}
		failed.Items = append(failed.Items, CheckItem{Name: st.Name, Message: st.Message, Code: st.Code,
			Pointer: pointer})
	}
	return TelegramChannelChecked{}, failed
}

// telegramGroupPeople reads the people with a Telegram Account link whose membership of the discussion group of the
// Destination id its check reads (C-27.FR-8), with their references by User id: those of the Teams that name it, by
// name, then those whom a chain Paging in it can Page through a Schedule (C-32.FR-8), by name; none for a Destination
// not created yet. A person of both is in both lists, and the check asks about them once, at the first
// (telegram.CheckGroupPeople). A link whose Telegram user id is not a number names nobody Telegram knows, and is left
// out.
func (s *Service) telegramGroupPeople(ctx context.Context, id *int64) ([]telegram.GroupPerson, map[int64]UserRef,
	error) {
	refs := map[int64]UserRef{}
	if id == nil {
		return nil, refs, nil
	}
	var rows []dbgen.ListTelegramGroupPeopleRow
	var paged []dbgen.ListChainPagedTelegramPeopleRow
	if err := s.writer.Writer.InTx(ctx, func(q TxQueries) error {
		var err error
		if rows, err = q.ListTelegramGroupPeople(ctx, dbgen.ListTelegramGroupPeopleParams{OrgID: s.orgID,
			DestinationID: *id}); err != nil {
			return err
		}
		paged, err = q.ListChainPagedTelegramPeople(ctx, dbgen.ListChainPagedTelegramPeopleParams{OrgID: s.orgID,
			DestinationID: *id, Now: s.writer.Business.Now().UTC()})
		return err
	}); err != nil {
		return nil, nil, fmt.Errorf("list the people of the teams and the chains of destination %d: %w", *id, err)
	}
	for _, r := range paged {
		rows = append(rows, dbgen.ListTelegramGroupPeopleRow(r))
	}
	var out []telegram.GroupPerson
	for _, r := range rows {
		tg, err := strconv.ParseInt(r.ExternalID, 10, 64)
		if err != nil {
			continue
		}
		out = append(out, telegram.GroupPerson{UserID: r.ID, TelegramID: tg})
		refs[r.ID] = UserRef{PublicID: r.PublicID, Name: r.Name, Login: r.Login}
	}
	return out, refs, nil
}

// absentUsers are the references of the Users ids by name, ignoring case, then by id: the check asks the people of
// the Teams first and those of the chains after them, each by name.
func absentUsers(ids []int64, refs map[int64]UserRef) []UserRef {
	type absent struct {
		id  int64
		ref UserRef
	}
	list := make([]absent, 0, len(ids))
	for _, id := range ids {
		if r, ok := refs[id]; ok {
			list = append(list, absent{id, r})
		}
	}
	slices.SortFunc(list, func(a, b absent) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.ref.Name), strings.ToLower(b.ref.Name)), cmp.Compare(a.id, b.id))
	})
	out := make([]UserRef, len(list))
	for i, a := range list {
		out[i] = a.ref
	}
	return out
}

// recordGroupPeople stores in a transaction of its own what the check c of the Telegram Destination id saw of the
// people of its discussion group, as setGroupPeople does.
func (s *Service) recordGroupPeople(ctx context.Context, id int64, c telegram.DestinationCheck) error {
	if _, _, ok := c.GroupPeople(); !ok {
		return nil
	}
	return s.writer.Writer.InTx(ctx, func(q TxQueries) error { return s.setGroupPeople(ctx, q, id, c) })
}

// setGroupPeople stores through q what the step discussion_group_members of the check c of the Telegram Destination id
// saw: the Users outside the discussion group, with those stored as outside it whom the check could not tell, and the
// time of the check on the business clock, only while the Destination's stored group is the one the check read, so
// that a slow check never overwrites a save that moved the Destination to another group. A check without the step
// stores nothing. It is not a change of the configuration: no version, no Audit log entry, no hint.
func (s *Service) setGroupPeople(ctx context.Context, q TxQueries, id int64, c telegram.DestinationCheck) error {
	absent, unknown, ok := c.GroupPeople()
	if !ok {
		return nil
	}
	if err := q.SetTelegramAbsences(ctx, dbgen.SetTelegramAbsencesParams{OrgID: s.orgID, ID: id,
		AbsentUserIds: orEmpty(absent), UnknownUserIds: orEmpty(unknown), CheckedAt: s.writer.Business.Now().UTC(),
		GroupID: pgtype.Int8{Int64: c.GroupID, Valid: true}}); err != nil {
		return fmt.Errorf("record the members of the discussion group of destination %d: %w", id, err)
	}
	return nil
}

// orEmpty is ids, empty for nil, which PostgreSQL would read as a null array.
func orEmpty(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}

// telegramFound sets on d what the check of its channel found: the ids and titles of the channel and of its
// discussion group.
func telegramFound(d *Destination, c telegram.DestinationCheck) {
	channel, group := strconv.FormatInt(c.ChannelID, 10), strconv.FormatInt(c.GroupID, 10)
	d.TelegramChannelChatID, d.TelegramDiscussionGroupID, d.TelegramChannelTitle = &channel, &group, &c.ChannelTitle
	d.TelegramDiscussionGroupTitle = &c.GroupTitle
}

// createTelegram creates a Telegram Destination from its channel once its Destination check passed (C-14.FR-2,
// FR-14), keeping the discussion group the check found and what it saw of its members (C-27.FR-8). It is recorded as
// destination.created.
func (s *Service) createTelegram(ctx context.Context, r Requester, in Input) (Destination, error) {
	checked, err := s.checkTelegram(ctx, in, nil)
	if err != nil {
		return Destination{}, err
	}
	now := s.writer.Business.Now().UTC()
	conn, channel := in.Telegram.Connection, in.Telegram.ChannelID
	d := Destination{PublicID: publicid.New(publicid.Destination), Type: in.Type, Name: in.Name, Connection: &conn,
		TelegramChannelID: &channel, MentionEntries: entriesOf(in), LimiterLimit: in.Limiter.Limit,
		LimiterPerSeconds: in.Limiter.PerSeconds, Health: Health{State: "healthy"}, Routes: []RouteRef{},
		CreatedAt: now, Version: 1}
	withButtons(&d, in)
	telegramFound(&d, checked.Check)
	planOwner(in, &d)
	after, err := viewOf(d)
	if err != nil {
		return Destination{}, err
	}
	err = s.writer.Writer.InTx(ctx, func(q TxQueries) error {
		if err := s.lockOwner(ctx, q, in, &d); err != nil {
			return err
		}
		if err := lockTelegramConnection(ctx, q, s.orgID, checked.ConnectionID); err != nil {
			return err
		}
		c := checked.Check
		id, err := q.InsertTelegramDestination(ctx, dbgen.InsertTelegramDestinationParams{OrgID: s.orgID,
			PublicID: d.PublicID, Name: d.Name, ConnectionID: pgtype.Int8{Int64: checked.ConnectionID, Valid: true},
			ChannelID: text(d.TelegramChannelID), ChannelChatID: pgtype.Int8{Int64: c.ChannelID, Valid: true},
			DiscussionChatID: pgtype.Int8{Int64: c.GroupID, Valid: true}, ChannelTitle: text(d.TelegramChannelTitle),
			DiscussionGroupTitle: text(d.TelegramDiscussionGroupTitle), Mentions: nobodyJSON(),
			Buttons: d.Buttons.JSON(), LimiterLimit: d.LimiterLimit,
			LimiterPerSeconds: d.LimiterPerSeconds, Now: now})
		if err != nil {
			return fmt.Errorf("create the destination: %w", nameTaken(err))
		}
		d.ID = id
		if err := s.setGroupPeople(ctx, q, id, c); err != nil {
			return err
		}
		if err := s.writeOwner(ctx, q, id, d); err != nil {
			return err
		}
		if err := s.writeMentionEntries(ctx, q, id, Destination{}, d); err != nil {
			return err
		}
		return s.record(ctx, q, r, ActionCreated, d, audit.Created(after))
	})
	if err != nil {
		return Destination{}, err
	}
	return d, nil
}

// updateTelegram replaces the configured fields of the Telegram Destination before, already validated, once its
// Destination check passed, through its Connection, and keeps what the check found; a passing check of a Broken
// Destination ends its Broken state. An update that changes nothing, and whose check found the same, writes only what
// the check saw of the members of the discussion group (C-27.FR-8).
func (s *Service) updateTelegram(ctx context.Context, r Requester, before Destination, in Input) (Destination,
	error) {
	checked, err := s.checkTelegram(ctx, in, &before.ID)
	if err != nil {
		return Destination{}, err
	}
	d := before
	conn, channel := in.Telegram.Connection, in.Telegram.ChannelID
	d.Name, d.Connection, d.TelegramChannelID = in.Name, &conn, &channel
	d.MentionEntries, d.LimiterLimit, d.LimiterPerSeconds = entriesOf(in), in.Limiter.Limit, in.Limiter.PerSeconds
	withButtons(&d, in)
	telegramFound(&d, checked.Check)
	old, err := viewOf(before)
	if err != nil {
		return Destination{}, err
	}
	planOwner(in, &d)
	after, err := viewOf(d)
	if err != nil {
		return Destination{}, err
	}
	diff := audit.Diff(old, after)
	found := deref(before.TelegramChannelChatID) != deref(d.TelegramChannelChatID) ||
		deref(before.TelegramDiscussionGroupID) != deref(d.TelegramDiscussionGroupID) ||
		deref(before.TelegramChannelTitle) != deref(d.TelegramChannelTitle) ||
		deref(before.TelegramDiscussionGroupTitle) != deref(d.TelegramDiscussionGroupTitle)
	if len(diff) > 0 || found {
		err = s.writer.Writer.InTx(ctx, func(q TxQueries) error {
			if err := s.lockOwner(ctx, q, in, &d); err != nil {
				return err
			}
			lock, err := q.LockDestination(ctx, dbgen.LockDestinationParams{OrgID: s.orgID, PublicID: before.PublicID})
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return fmt.Errorf("lock the destination %s: %w", before.PublicID, err)
			}
			if lock.Version != before.Version {
				return ErrVersionMismatch
			}
			if err := lockTelegramConnection(ctx, q, s.orgID, checked.ConnectionID); err != nil {
				return err
			}
			c := checked.Check
			if err := q.UpdateTelegramDestination(ctx, dbgen.UpdateTelegramDestinationParams{OrgID: s.orgID,
				ID: before.ID, Name: d.Name, ConnectionID: pgtype.Int8{Int64: checked.ConnectionID, Valid: true},
				ChannelID: text(d.TelegramChannelID), ChannelChatID: pgtype.Int8{Int64: c.ChannelID, Valid: true},
				DiscussionChatID: pgtype.Int8{Int64: c.GroupID, Valid: true}, ChannelTitle: text(d.TelegramChannelTitle),
				DiscussionGroupTitle: text(d.TelegramDiscussionGroupTitle), Buttons: d.Buttons.JSON(),
				LimiterLimit: d.LimiterLimit, LimiterPerSeconds: d.LimiterPerSeconds,
				Now: s.writer.Business.Now().UTC()}); err != nil {
				return fmt.Errorf("update the destination %s: %w", before.PublicID, nameTaken(err))
			}
			d.Version++
			if err := s.setGroupPeople(ctx, q, before.ID, c); err != nil {
				return err
			}
			if err := s.writeOwner(ctx, q, before.ID, d); err != nil {
				return err
			}
			if err := s.writeMentionEntries(ctx, q, before.ID, before, d); err != nil {
				return err
			}
			if err := s.buttonsChanged(ctx, q, before, d); err != nil {
				return err
			}
			if err := s.connectionChanged(ctx, q, before, d); err != nil {
				return err
			}
			if err := s.renamed(ctx, q, before, d); err != nil {
				return err
			}
			if len(diff) == 0 {
				return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint, ID: d.PublicID})
			}
			return s.record(ctx, q, r, ActionUpdated, d, diff)
		})
		if err != nil {
			return Destination{}, err
		}
	} else if err := s.recordGroupPeople(ctx, before.ID, checked.Check); err != nil {
		// Nothing else changed, so nothing was saved before this write.
		return Destination{}, err
	}
	if before.Health.State == healthBroken {
		if err := s.healthy(ctx, before.ID); err != nil {
			return Destination{}, err
		}
		return s.Get(ctx, before.PublicID)
	}
	return d, nil
}

// lockTelegramConnection takes the Telegram Connection id in share mode; a Connection deleted since the check is
// ErrUnknownConnection's field error.
func lockTelegramConnection(ctx context.Context, q TxQueries, orgID, id int64) error {
	_, err := q.LockTelegramConnection(ctx, dbgen.LockTelegramConnectionParams{OrgID: orgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return unknownTelegramConnection()
	}
	if err != nil {
		return fmt.Errorf("lock the connection %d: %w", id, err)
	}
	return nil
}

func unknownBridgeConnection() *FieldError {
	return &FieldError{Pointer: pointerConnection, Code: CodeUnknownID, Detail: "No such Bridge Connection."}
}

// validateBridge checks the common fields of in, its channel id, trimmed, of 1 to 512 characters, and its Connection
// (C-22.FR-3) — stored is nil on a creation — which a creation, or an update that moves the Destination to another
// Connection, may not give as a Notification gateway alone: a Bridge Connection whose stored info declares only the
// role gateway is refused with gateway_only before the Mention settings are validated, so that it never takes a
// Mention entry either (C-30.FR-1). A Destination saved before its bridge became one keeps its delivery as the bridge
// answers it; an update that keeps that Connection is not refused here, but its Destination check fails as the bridge
// answers checkChannel, so that the Destination can only be deleted or moved to another Connection.
func (s *Service) validateBridge(ctx context.Context, in *Input, stored *Destination) error {
	if err := validateCommon(in); err != nil {
		return err
	}
	b := in.Bridge
	b.ChannelID = strings.TrimSpace(b.ChannelID)
	switch {
	case b.ChannelID == "":
		return &FieldError{Pointer: pointerChannel, Code: CodeRequired, Detail: "The channel is empty."}
	case utf8.RuneCountInString(b.ChannelID) > maxBridgeChannelLength:
		return &FieldError{Pointer: pointerChannel, Code: CodeTooLong,
			Detail: fmt.Sprintf("The channel id is longer than %d characters.", maxBridgeChannelLength)}
	}
	conn, err := publicid.Parse(publicid.Connection, b.Connection)
	if err != nil {
		return unknownBridgeConnection()
	}
	b.Connection = conn
	if stored != nil && stored.Connection != nil && *stored.Connection == conn {
		return nil
	}
	only, err := s.writer.Bridge.GatewayOnly(ctx, conn)
	if err != nil {
		return err
	}
	if only {
		return &FieldError{Pointer: pointerConnection, Code: CodeGatewayOnly,
			Detail: "The Bridge Connection is a Notification gateway, which cannot be a Destination."}
	}
	return nil
}

// checkBridge runs the Destination check of the Bridge channel of in for the Destination destinationID, nil before it
// exists, and refuses a failing one with a *CheckFailedError: its item channel at /channel_id, or at /connection_id
// when the bridge refused the token. Its requests are recorded as destination_check under a check run of their own
// (C-23.FR-2).
func (s *Service) checkBridge(ctx context.Context, in Input, destinationID *int64) (BridgeChannelChecked, error) {
	ctx, _ = outbound.StartCheck(ctx, outbound.CheckDestination, idOf(destinationID))
	b := in.Bridge
	res, err := s.writer.Bridge.CheckBridgeChannel(ctx, BridgeChannelCheck{Connection: b.Connection,
		ChannelID: b.ChannelID})
	if errors.Is(err, ErrUnknownConnection) {
		return BridgeChannelChecked{}, unknownBridgeConnection()
	}
	if err != nil {
		return BridgeChannelChecked{}, err
	}
	if res.Check.OK() {
		return res, nil
	}
	pointer := pointerChannel
	if res.Check.Code == bridge.CodeTokenInvalid {
		pointer = pointerConnection
	}
	return BridgeChannelChecked{}, &CheckFailedError{Items: []CheckItem{{Name: bridge.ItemChannel,
		Message: res.Check.Message, Code: res.Check.Code, Pointer: pointer}}}
}

// createBridge creates a Bridge Destination from its channel id once its Destination check passed (C-22.FR-3),
// keeping the channel's name the check read. It is recorded as destination.created.
func (s *Service) createBridge(ctx context.Context, r Requester, in Input) (Destination, error) {
	checked, err := s.checkBridge(ctx, in, nil)
	if err != nil {
		return Destination{}, err
	}
	now := s.writer.Business.Now().UTC()
	conn, channel, name := in.Bridge.Connection, in.Bridge.ChannelID, checked.Check.Name
	d := Destination{PublicID: publicid.New(publicid.Destination), Type: in.Type, Name: in.Name, Connection: &conn,
		BridgeChannelID: &channel, BridgeChannelName: &name, MentionEntries: entriesOf(in), LimiterLimit: in.Limiter.Limit,
		LimiterPerSeconds: in.Limiter.PerSeconds, Health: Health{State: "healthy"}, Routes: []RouteRef{},
		CreatedAt: now, Version: 1}
	planOwner(in, &d)
	withButtons(&d, in)
	after, err := viewOf(d)
	if err != nil {
		return Destination{}, err
	}
	err = s.writer.Writer.InTx(ctx, func(q TxQueries) error {
		if err := s.lockOwner(ctx, q, in, &d); err != nil {
			return err
		}
		if err := lockBridgeConnection(ctx, q, s.orgID, checked.ConnectionID); err != nil {
			return err
		}
		id, err := q.InsertBridgeDestination(ctx, dbgen.InsertBridgeDestinationParams{OrgID: s.orgID,
			PublicID: d.PublicID, Name: d.Name, ConnectionID: pgtype.Int8{Int64: checked.ConnectionID, Valid: true},
			ChannelID: text(d.BridgeChannelID), ChannelName: text(d.BridgeChannelName), Mentions: nobodyJSON(),
			Buttons:      d.Buttons.JSON(),
			LimiterLimit: d.LimiterLimit, LimiterPerSeconds: d.LimiterPerSeconds, Now: now})
		if err != nil {
			return fmt.Errorf("create the destination: %w", nameTaken(err))
		}
		d.ID = id
		if err := s.writeOwner(ctx, q, id, d); err != nil {
			return err
		}
		if err := s.writeMentionEntries(ctx, q, id, Destination{}, d); err != nil {
			return err
		}
		return s.record(ctx, q, r, ActionCreated, d, audit.Created(after))
	})
	if err != nil {
		return Destination{}, err
	}
	return d, nil
}

// updateBridge replaces the configured fields of the Bridge Destination before, already validated, once its
// Destination check passed, through its Connection, and keeps the channel's name the check read; a passing check of a
// Broken Destination ends its Broken state. An update that changes nothing, and whose check read the same name, writes
// nothing.
func (s *Service) updateBridge(ctx context.Context, r Requester, before Destination, in Input) (Destination, error) {
	checked, err := s.checkBridge(ctx, in, &before.ID)
	if err != nil {
		return Destination{}, err
	}
	d := before
	conn, channel, name := in.Bridge.Connection, in.Bridge.ChannelID, checked.Check.Name
	d.Name, d.Connection, d.BridgeChannelID, d.BridgeChannelName = in.Name, &conn, &channel, &name
	d.MentionEntries, d.LimiterLimit, d.LimiterPerSeconds = entriesOf(in), in.Limiter.Limit, in.Limiter.PerSeconds
	withButtons(&d, in)
	old, err := viewOf(before)
	if err != nil {
		return Destination{}, err
	}
	planOwner(in, &d)
	after, err := viewOf(d)
	if err != nil {
		return Destination{}, err
	}
	diff := audit.Diff(old, after)
	if len(diff) > 0 || deref(before.BridgeChannelName) != name {
		err = s.writer.Writer.InTx(ctx, func(q TxQueries) error {
			if err := s.lockOwner(ctx, q, in, &d); err != nil {
				return err
			}
			lock, err := q.LockDestination(ctx, dbgen.LockDestinationParams{OrgID: s.orgID, PublicID: before.PublicID})
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return fmt.Errorf("lock the destination %s: %w", before.PublicID, err)
			}
			if lock.Version != before.Version {
				return ErrVersionMismatch
			}
			if err := lockBridgeConnection(ctx, q, s.orgID, checked.ConnectionID); err != nil {
				return err
			}
			if err := q.UpdateBridgeDestination(ctx, dbgen.UpdateBridgeDestinationParams{OrgID: s.orgID,
				ID: before.ID, Name: d.Name, ConnectionID: pgtype.Int8{Int64: checked.ConnectionID, Valid: true},
				ChannelID: text(d.BridgeChannelID), ChannelName: text(d.BridgeChannelName),
				Buttons:      d.Buttons.JSON(),
				LimiterLimit: d.LimiterLimit, LimiterPerSeconds: d.LimiterPerSeconds,
				Now: s.writer.Business.Now().UTC()}); err != nil {
				return fmt.Errorf("update the destination %s: %w", before.PublicID, nameTaken(err))
			}
			d.Version++
			if err := s.writeOwner(ctx, q, before.ID, d); err != nil {
				return err
			}
			if err := s.writeMentionEntries(ctx, q, before.ID, before, d); err != nil {
				return err
			}
			if err := s.buttonsChanged(ctx, q, before, d); err != nil {
				return err
			}
			if err := s.connectionChanged(ctx, q, before, d); err != nil {
				return err
			}
			if err := s.renamed(ctx, q, before, d); err != nil {
				return err
			}
			if len(diff) == 0 {
				return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint, ID: d.PublicID})
			}
			return s.record(ctx, q, r, ActionUpdated, d, diff)
		})
		if err != nil {
			return Destination{}, err
		}
	}
	if before.Health.State == healthBroken {
		if err := s.healthy(ctx, before.ID); err != nil {
			return Destination{}, err
		}
		return s.Get(ctx, before.PublicID)
	}
	return d, nil
}

// lockBridgeConnection takes the Bridge Connection id in share mode; a Connection deleted since the check is
// ErrUnknownConnection's field error.
func lockBridgeConnection(ctx context.Context, q TxQueries, orgID, id int64) error {
	_, err := q.LockBridgeConnection(ctx, dbgen.LockBridgeConnectionParams{OrgID: orgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return unknownBridgeConnection()
	}
	if err != nil {
		return fmt.Errorf("lock the connection %d: %w", id, err)
	}
	return nil
}

func unknownSlackConnection() *FieldError {
	return &FieldError{Pointer: pointerConnection, Code: CodeUnknownID, Detail: "No such Slack Connection."}
}

// validateSlack checks, before any request, the common fields of in, its channel id, trimmed, which names a channel —
// C… or G… in upper-case letters and digits, as entered (C-24.FR-3) — its Connection and its Mention settings.
func (s *Service) validateSlack(in *Input) error {
	if err := validateCommon(in); err != nil {
		return err
	}
	sl := in.Slack
	sl.ChannelID = strings.TrimSpace(sl.ChannelID)
	switch {
	case sl.ChannelID == "":
		return &FieldError{Pointer: pointerChannel, Code: CodeRequired, Detail: "The channel is empty."}
	case !slack.ValidChannelID(sl.ChannelID):
		return &FieldError{Pointer: pointerChannel, Code: CodeInvalidFormat,
			Detail: "A Slack channel id starts with C or G and has upper-case letters and digits, such as C0123456789."}
	}
	conn, err := publicid.Parse(publicid.Connection, sl.Connection)
	if err != nil {
		return unknownSlackConnection()
	}
	sl.Connection = conn
	return nil
}

// checkSlack runs the Destination check of the Slack channel of in for the Destination destinationID, nil before it
// exists, and refuses a failing one with a *CheckFailedError: its item channel at /channel_id, or at /connection_id
// when Slack refused the bot token or the token lacks a scope. Its requests are recorded as destination_check under a
// check run of their own (C-23.FR-2).
func (s *Service) checkSlack(ctx context.Context, in Input, destinationID *int64) (SlackChannelChecked, error) {
	ctx, _ = outbound.StartCheck(ctx, outbound.CheckDestination, idOf(destinationID))
	sl := in.Slack
	res, err := s.writer.Slack.CheckSlackChannel(ctx, SlackChannelCheck{Connection: sl.Connection,
		ChannelID: sl.ChannelID})
	if errors.Is(err, ErrUnknownConnection) {
		return SlackChannelChecked{}, unknownSlackConnection()
	}
	if err != nil {
		return SlackChannelChecked{}, err
	}
	c := res.Check
	if c.OK() {
		return res, nil
	}
	pointer := pointerChannel
	if c.Code == slack.CodeTokenInvalid || c.Code == slack.CodeMissingScope {
		pointer = pointerConnection
	}
	return SlackChannelChecked{}, &CheckFailedError{Items: []CheckItem{{Name: slack.ItemChannel, Message: c.Message,
		Code: c.Code, MissingScopes: c.MissingScopes, Pointer: pointer}}}
}

// createSlack creates a Slack Destination from its channel id once its Destination check passed (C-24.FR-3), keeping
// the channel's name the check read. It is recorded as destination.created.
func (s *Service) createSlack(ctx context.Context, r Requester, in Input) (Destination, error) {
	checked, err := s.checkSlack(ctx, in, nil)
	if err != nil {
		return Destination{}, err
	}
	now := s.writer.Business.Now().UTC()
	conn, channel, name := in.Slack.Connection, in.Slack.ChannelID, checked.Check.Name
	d := Destination{PublicID: publicid.New(publicid.Destination), Type: in.Type, Name: in.Name, Connection: &conn,
		SlackChannelID: &channel, SlackChannelName: &name, MentionEntries: entriesOf(in), LimiterLimit: in.Limiter.Limit,
		LimiterPerSeconds: in.Limiter.PerSeconds, Health: Health{State: "healthy"}, Routes: []RouteRef{},
		CreatedAt: now, Version: 1}
	planOwner(in, &d)
	withButtons(&d, in)
	after, err := viewOf(d)
	if err != nil {
		return Destination{}, err
	}
	err = s.writer.Writer.InTx(ctx, func(q TxQueries) error {
		if err := s.lockOwner(ctx, q, in, &d); err != nil {
			return err
		}
		if err := lockSlackConnection(ctx, q, s.orgID, checked.ConnectionID); err != nil {
			return err
		}
		id, err := q.InsertSlackDestination(ctx, dbgen.InsertSlackDestinationParams{OrgID: s.orgID,
			PublicID: d.PublicID, Name: d.Name, ConnectionID: pgtype.Int8{Int64: checked.ConnectionID, Valid: true},
			ChannelID: text(d.SlackChannelID), ChannelName: text(d.SlackChannelName), Mentions: nobodyJSON(),
			Buttons:      d.Buttons.JSON(),
			LimiterLimit: d.LimiterLimit, LimiterPerSeconds: d.LimiterPerSeconds, Now: now})
		if err != nil {
			return fmt.Errorf("create the destination: %w", nameTaken(err))
		}
		d.ID = id
		if err := s.writeOwner(ctx, q, id, d); err != nil {
			return err
		}
		if err := s.writeMentionEntries(ctx, q, id, Destination{}, d); err != nil {
			return err
		}
		return s.record(ctx, q, r, ActionCreated, d, audit.Created(after))
	})
	if err != nil {
		return Destination{}, err
	}
	return d, nil
}

// updateSlack replaces the configured fields of the Slack Destination before, already validated, once its Destination
// check passed, through its Connection, and keeps the channel's name the check read; a passing check of a Broken
// Destination ends its Broken state. An update that changes nothing, and whose check read the same name, writes
// nothing.
func (s *Service) updateSlack(ctx context.Context, r Requester, before Destination, in Input) (Destination, error) {
	checked, err := s.checkSlack(ctx, in, &before.ID)
	if err != nil {
		return Destination{}, err
	}
	d := before
	conn, channel, name := in.Slack.Connection, in.Slack.ChannelID, checked.Check.Name
	d.Name, d.Connection, d.SlackChannelID, d.SlackChannelName = in.Name, &conn, &channel, &name
	d.MentionEntries, d.LimiterLimit, d.LimiterPerSeconds = entriesOf(in), in.Limiter.Limit, in.Limiter.PerSeconds
	withButtons(&d, in)
	old, err := viewOf(before)
	if err != nil {
		return Destination{}, err
	}
	planOwner(in, &d)
	after, err := viewOf(d)
	if err != nil {
		return Destination{}, err
	}
	diff := audit.Diff(old, after)
	if len(diff) > 0 || deref(before.SlackChannelName) != name {
		err = s.writer.Writer.InTx(ctx, func(q TxQueries) error {
			if err := s.lockOwner(ctx, q, in, &d); err != nil {
				return err
			}
			lock, err := q.LockDestination(ctx, dbgen.LockDestinationParams{OrgID: s.orgID, PublicID: before.PublicID})
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return fmt.Errorf("lock the destination %s: %w", before.PublicID, err)
			}
			if lock.Version != before.Version {
				return ErrVersionMismatch
			}
			if err := lockSlackConnection(ctx, q, s.orgID, checked.ConnectionID); err != nil {
				return err
			}
			if err := q.UpdateSlackDestination(ctx, dbgen.UpdateSlackDestinationParams{OrgID: s.orgID,
				ID: before.ID, Name: d.Name, ConnectionID: pgtype.Int8{Int64: checked.ConnectionID, Valid: true},
				ChannelID: text(d.SlackChannelID), ChannelName: text(d.SlackChannelName),
				Buttons:      d.Buttons.JSON(),
				LimiterLimit: d.LimiterLimit, LimiterPerSeconds: d.LimiterPerSeconds,
				Now: s.writer.Business.Now().UTC()}); err != nil {
				return fmt.Errorf("update the destination %s: %w", before.PublicID, nameTaken(err))
			}
			d.Version++
			if err := s.writeOwner(ctx, q, before.ID, d); err != nil {
				return err
			}
			if err := s.writeMentionEntries(ctx, q, before.ID, before, d); err != nil {
				return err
			}
			if err := s.buttonsChanged(ctx, q, before, d); err != nil {
				return err
			}
			if err := s.connectionChanged(ctx, q, before, d); err != nil {
				return err
			}
			if err := s.renamed(ctx, q, before, d); err != nil {
				return err
			}
			if len(diff) == 0 {
				return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint, ID: d.PublicID})
			}
			return s.record(ctx, q, r, ActionUpdated, d, diff)
		})
		if err != nil {
			return Destination{}, err
		}
	}
	if before.Health.State == healthBroken {
		if err := s.healthy(ctx, before.ID); err != nil {
			return Destination{}, err
		}
		return s.Get(ctx, before.PublicID)
	}
	return d, nil
}

// lockSlackConnection takes the Slack Connection id in share mode; a Connection deleted since the check is
// ErrUnknownConnection's field error.
func lockSlackConnection(ctx context.Context, q TxQueries, orgID, id int64) error {
	_, err := q.LockSlackConnection(ctx, dbgen.LockSlackConnectionParams{OrgID: orgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return unknownSlackConnection()
	}
	if err != nil {
		return fmt.Errorf("lock the connection %d: %w", id, err)
	}
	return nil
}

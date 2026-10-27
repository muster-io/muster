// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package destinations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/bridge"
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

// The Audit log actions of saved Destinations (C-03.FR-14).
const (
	ActionCreated = "destination.created"
	ActionUpdated = "destination.updated"
)

// The codes of FieldError.
const (
	CodeRequired           = "required"
	CodeInvalidFormat      = "invalid_format"
	CodeTooLong            = "too_long"
	CodeUnknownID          = "unknown_id"
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

// Input is what createDestination and updateDestination write: the common fields and those of its type.
type Input struct {
	Type       string
	Name       string
	Mentions   mentions.Settings
	Limiter    Limiter
	Mattermost *MattermostInput
	Telegram   *TelegramInput
	Webhook    *WebhookInput
	Bridge     *BridgeInput
	Slack      *SlackInput
	// OwningTeamID is the public_id of the Team that owns the Destination (C-25.FR-2): nil keeps the stored one on an
	// update and is none on a creation, and an empty string clears it.
	OwningTeamID *string
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

// ChannelCheck is the Destination check of a Mattermost channel through the Connection Connection (public_id): of the
// saved Destination Destination (its id) when it exists, so that its limiter applies, else of the Connection alone.
type ChannelCheck struct {
	Connection  string
	Destination *int64
	TeamID      string
	ChannelID   string
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

// TelegramChannelCheck is the Destination check of a Telegram channel through the Connection Connection (public_id):
// of the saved Destination Destination (its id) when it exists, so that its limiter applies, else of the Connection
// alone.
type TelegramChannelCheck struct {
	Connection  string
	Destination *int64
	ChannelID   string
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

// BridgeChannelCheck is the Destination check of a Bridge channel through the Connection Connection (public_id): of the
// saved Destination Destination (its id) when it exists, so that its limiter applies, else of the Connection alone.
type BridgeChannelCheck struct {
	Connection  string
	Destination *int64
	ChannelID   string
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
}

// SlackChannelCheck is the Destination check of a Slack channel through the Connection Connection (public_id): of the
// saved Destination Destination (its id) when it exists, so that its limiter applies, else of the Connection alone.
type SlackChannelCheck struct {
	Connection  string
	Destination *int64
	ChannelID   string
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

// MentionValidator validates the Mention settings of a Destination type (C-12.FR-8), declared by its consumer;
// *mentions.Service implements it.
type MentionValidator interface {
	Validate(ctx context.Context, destinationType string, set mentions.Settings) error
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
	Mentions   mentions.Settings        `json:"mentions"`
	Limiter    Limiter                  `json:"limiter"`
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
	if len(d.Mentions) > 0 {
		if err := json.Unmarshal(d.Mentions, &v.Mentions); err != nil {
			return view{}, fmt.Errorf("read the mention settings of %s: %w", d.PublicID, err)
		}
	}
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

// validate checks the common fields of in and the fields of its type, before the Destination check; stored is nil on a
// creation.
func (s *Service) validate(ctx context.Context, in *Input, stored *Destination) error {
	switch {
	case stored != nil && stored.Type != in.Type:
		return &FieldError{Pointer: "/type", Code: CodeInvalidFormat, Detail: "The type of a Destination cannot change."}
	case in.Type == TypeTelegram && in.Telegram != nil:
		return s.validateTelegram(ctx, in)
	case in.Type == TypeTelegram:
		return &FieldError{Pointer: "/type", Code: CodeInvalidFormat, Detail: "The fields of the type are missing."}
	case in.Type == TypeBridge && in.Bridge != nil:
		return s.validateBridge(ctx, in)
	case in.Type == TypeBridge:
		return &FieldError{Pointer: "/type", Code: CodeInvalidFormat, Detail: "The fields of the type are missing."}
	case in.Type == TypeSlack && in.Slack != nil:
		return s.validateSlack(ctx, in)
	case in.Type == TypeSlack:
		return &FieldError{Pointer: "/type", Code: CodeInvalidFormat, Detail: "The fields of the type are missing."}
	case in.Type == TypeWebhook && in.Webhook != nil:
		if err := validateCommon(in); err != nil {
			return err
		}
		if err := s.validateWebhook(in.Webhook); err != nil {
			return err
		}
		return s.writer.Mentions.Validate(ctx, in.Type, in.Mentions)
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
	return s.writer.Mentions.Validate(ctx, in.Type, in.Mentions)
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
	set, err := json.Marshal(in.Mentions)
	if err != nil {
		return Destination{}, fmt.Errorf("encode the mention settings: %w", err)
	}
	mode := in.Webhook.Mode
	events, template := webhookConfigs(in.Webhook)
	d := Destination{PublicID: publicid.New(publicid.Destination), Type: in.Type, Name: in.Name, WebhookMode: &mode,
		EventsConfig: events, TemplateConfig: template, Proxy: proxyRead(sec.proxy, sec.password),
		SigningSecret: SigningSecret{Set: true, UpdatedAt: &now}, SigningSecretOnce: secret, Mentions: set,
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
			Mentions: set, LimiterLimit: d.LimiterLimit, LimiterPerSeconds: d.LimiterPerSeconds}
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
	set, err := json.Marshal(in.Mentions)
	if err != nil {
		return Destination{}, fmt.Errorf("encode the mention settings: %w", err)
	}
	d := before
	mode := in.Webhook.Mode
	d.Name, d.WebhookMode, d.Mentions = in.Name, &mode, set
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
			Mentions: set, LimiterLimit: d.LimiterLimit, LimiterPerSeconds: d.LimiterPerSeconds, OrgID: s.orgID,
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
	res, err := s.writer.Mattermost.CheckChannel(ctx, ChannelCheck{Connection: m.Connection, Destination: destinationID,
		TeamID: m.TeamID, ChannelID: m.ChannelID})
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
	set, err := json.Marshal(in.Mentions)
	if err != nil {
		return Destination{}, fmt.Errorf("encode the mention settings: %w", err)
	}
	now := s.writer.Business.Now().UTC()
	conn := in.Mattermost.Connection
	d := Destination{PublicID: publicid.New(publicid.Destination), Type: in.Type, Name: in.Name, Connection: &conn,
		MattermostTeamID: &in.Mattermost.TeamID, MattermostChannelID: &in.Mattermost.ChannelID,
		MattermostTeamName: &checked.Check.TeamName, MattermostChannelName: &checked.Check.ChannelName, Mentions: set,
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
		if err := lockConnection(ctx, q, s.orgID, checked.ConnectionID); err != nil {
			return err
		}
		id, err := q.InsertMattermostDestination(ctx, dbgen.InsertMattermostDestinationParams{OrgID: s.orgID,
			PublicID: d.PublicID, Name: d.Name, ConnectionID: pgtype.Int8{Int64: checked.ConnectionID, Valid: true},
			TeamID: text(d.MattermostTeamID), ChannelID: text(d.MattermostChannelID),
			TeamName: text(d.MattermostTeamName), ChannelName: text(d.MattermostChannelName), Mentions: set,
			LimiterLimit: d.LimiterLimit, LimiterPerSeconds: d.LimiterPerSeconds, Now: now})
		if err != nil {
			return fmt.Errorf("create the destination: %w", nameTaken(err))
		}
		d.ID = id
		if err := s.writeOwner(ctx, q, id, d); err != nil {
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
// limited by the Destination; a passing check of a Broken Destination ends its Broken state (C-13.FR-10, C-14.FR-14).
// An update that changes nothing writes nothing.
func (s *Service) Update(ctx context.Context, r Requester, publicID string, version *int64, in Input) (Destination,
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
	set, err := json.Marshal(in.Mentions)
	if err != nil {
		return Destination{}, fmt.Errorf("encode the mention settings: %w", err)
	}
	d := before
	conn := in.Mattermost.Connection
	d.Name, d.Connection, d.MattermostTeamID, d.MattermostChannelID = in.Name, &conn, &in.Mattermost.TeamID,
		&in.Mattermost.ChannelID
	d.MattermostTeamName, d.MattermostChannelName = &checked.Check.TeamName, &checked.Check.ChannelName
	d.Mentions, d.LimiterLimit, d.LimiterPerSeconds = set, in.Limiter.Limit, in.Limiter.PerSeconds
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
				TeamName: text(d.MattermostTeamName), ChannelName: text(d.MattermostChannelName), Mentions: set,
				LimiterLimit: d.LimiterLimit, LimiterPerSeconds: d.LimiterPerSeconds, Now: now}); err != nil {
				return fmt.Errorf("update the destination %s: %w", before.PublicID, nameTaken(err))
			}
			d.Version++
			if err := s.writeOwner(ctx, q, before.ID, d); err != nil {
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
// C-22.FR-3, C-24.FR-3) on the interactive path, limited by the Destination: each check with its result. A passing
// check of a Broken Destination ends its Broken state. A Bridge or Slack Destination's check keeps no channel name it
// reads: a save does.
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
		res, err := s.writer.Mattermost.CheckChannel(ctx, ChannelCheck{Connection: *d.Connection, Destination: &d.ID,
			TeamID: deref(d.MattermostTeamID), ChannelID: deref(d.MattermostChannelID)})
		if err != nil {
			return CheckResult{}, err
		}
		out = CheckResult{OK: res.Check.OK(), Health: d.Health}
		for _, st := range res.Check.Steps {
			out.Items = append(out.Items, CheckItem{Name: st.Name, OK: st.OK, Message: st.Message, Code: st.Code})
		}
	case d.Type == TypeTelegram && d.Connection != nil:
		res, err := s.writer.Telegram.CheckTelegramChannel(ctx, TelegramChannelCheck{Connection: *d.Connection,
			Destination: &d.ID, ChannelID: deref(d.TelegramChannelID)})
		if err != nil {
			return CheckResult{}, err
		}
		out = CheckResult{OK: res.Check.OK(), Health: d.Health}
		for _, st := range res.Check.Steps {
			out.Items = append(out.Items, CheckItem{Name: st.Name, OK: st.OK, Message: st.Message, Code: st.Code})
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
	case d.Type == TypeBridge && d.Connection != nil:
		res, err := s.writer.Bridge.CheckBridgeChannel(ctx, BridgeChannelCheck{Connection: *d.Connection,
			Destination: &d.ID, ChannelID: deref(d.BridgeChannelID)})
		if err != nil {
			return CheckResult{}, err
		}
		c := res.Check
		out = CheckResult{OK: c.OK(), Health: d.Health, Items: []CheckItem{{Name: bridge.ItemChannel, OK: c.OK(),
			Message: c.Message, Code: c.Code}}}
	case d.Type == TypeSlack && d.Connection != nil:
		res, err := s.writer.Slack.CheckSlackChannel(ctx, SlackChannelCheck{Connection: *d.Connection,
			Destination: &d.ID, ChannelID: deref(d.SlackChannelID)})
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
func (s *Service) validateTelegram(ctx context.Context, in *Input) error {
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
	return s.writer.Mentions.Validate(ctx, in.Type, in.Mentions)
}

// checkTelegram runs the Destination check of the Telegram channel of in for the Destination destinationID, nil before
// it exists, and refuses a failing one with a *CheckFailedError: the failing step at /channel_id, or at
// /connection_id when the bot token was refused. Its requests are recorded as destination_check under a check run of
// their own (C-23.FR-2).
func (s *Service) checkTelegram(ctx context.Context, in Input, destinationID *int64) (TelegramChannelChecked, error) {
	ctx, _ = outbound.StartCheck(ctx, outbound.CheckDestination, idOf(destinationID))
	t := in.Telegram
	res, err := s.writer.Telegram.CheckTelegramChannel(ctx, TelegramChannelCheck{Connection: t.Connection,
		Destination: destinationID, ChannelID: t.ChannelID})
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

// telegramFound sets on d what the check of its channel found: the ids and titles of the channel and of its
// discussion group.
func telegramFound(d *Destination, c telegram.DestinationCheck) {
	channel, group := strconv.FormatInt(c.ChannelID, 10), strconv.FormatInt(c.GroupID, 10)
	d.TelegramChannelChatID, d.TelegramDiscussionGroupID, d.TelegramChannelTitle = &channel, &group, &c.ChannelTitle
	d.TelegramDiscussionGroupTitle = &c.GroupTitle
}

// createTelegram creates a Telegram Destination from its channel once its Destination check passed (C-14.FR-2,
// FR-14), keeping the discussion group the check found. It is recorded as destination.created.
func (s *Service) createTelegram(ctx context.Context, r Requester, in Input) (Destination, error) {
	checked, err := s.checkTelegram(ctx, in, nil)
	if err != nil {
		return Destination{}, err
	}
	set, err := json.Marshal(in.Mentions)
	if err != nil {
		return Destination{}, fmt.Errorf("encode the mention settings: %w", err)
	}
	now := s.writer.Business.Now().UTC()
	conn, channel := in.Telegram.Connection, in.Telegram.ChannelID
	d := Destination{PublicID: publicid.New(publicid.Destination), Type: in.Type, Name: in.Name, Connection: &conn,
		TelegramChannelID: &channel, Mentions: set, LimiterLimit: in.Limiter.Limit,
		LimiterPerSeconds: in.Limiter.PerSeconds, Health: Health{State: "healthy"}, Routes: []RouteRef{},
		CreatedAt: now, Version: 1}
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
			DiscussionGroupTitle: text(d.TelegramDiscussionGroupTitle), Mentions: set, LimiterLimit: d.LimiterLimit,
			LimiterPerSeconds: d.LimiterPerSeconds, Now: now})
		if err != nil {
			return fmt.Errorf("create the destination: %w", nameTaken(err))
		}
		d.ID = id
		if err := s.writeOwner(ctx, q, id, d); err != nil {
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
// Destination check passed, limited by the Destination, and keeps what the check found; a passing check of a Broken
// Destination ends its Broken state. An update that changes nothing, and whose check found the same, writes nothing.
func (s *Service) updateTelegram(ctx context.Context, r Requester, before Destination, in Input) (Destination,
	error) {
	checked, err := s.checkTelegram(ctx, in, &before.ID)
	if err != nil {
		return Destination{}, err
	}
	set, err := json.Marshal(in.Mentions)
	if err != nil {
		return Destination{}, fmt.Errorf("encode the mention settings: %w", err)
	}
	d := before
	conn, channel := in.Telegram.Connection, in.Telegram.ChannelID
	d.Name, d.Connection, d.TelegramChannelID = in.Name, &conn, &channel
	d.Mentions, d.LimiterLimit, d.LimiterPerSeconds = set, in.Limiter.Limit, in.Limiter.PerSeconds
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
				DiscussionGroupTitle: text(d.TelegramDiscussionGroupTitle), Mentions: set,
				LimiterLimit: d.LimiterLimit, LimiterPerSeconds: d.LimiterPerSeconds,
				Now: s.writer.Business.Now().UTC()}); err != nil {
				return fmt.Errorf("update the destination %s: %w", before.PublicID, nameTaken(err))
			}
			d.Version++
			if err := s.writeOwner(ctx, q, before.ID, d); err != nil {
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
// (C-22.FR-3).
func (s *Service) validateBridge(ctx context.Context, in *Input) error {
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
	return s.writer.Mentions.Validate(ctx, in.Type, in.Mentions)
}

// checkBridge runs the Destination check of the Bridge channel of in for the Destination destinationID, nil before it
// exists, and refuses a failing one with a *CheckFailedError: its item channel at /channel_id, or at /connection_id
// when the bridge refused the token. Its requests are recorded as destination_check under a check run of their own
// (C-23.FR-2).
func (s *Service) checkBridge(ctx context.Context, in Input, destinationID *int64) (BridgeChannelChecked, error) {
	ctx, _ = outbound.StartCheck(ctx, outbound.CheckDestination, idOf(destinationID))
	b := in.Bridge
	res, err := s.writer.Bridge.CheckBridgeChannel(ctx, BridgeChannelCheck{Connection: b.Connection,
		Destination: destinationID, ChannelID: b.ChannelID})
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
	set, err := json.Marshal(in.Mentions)
	if err != nil {
		return Destination{}, fmt.Errorf("encode the mention settings: %w", err)
	}
	now := s.writer.Business.Now().UTC()
	conn, channel, name := in.Bridge.Connection, in.Bridge.ChannelID, checked.Check.Name
	d := Destination{PublicID: publicid.New(publicid.Destination), Type: in.Type, Name: in.Name, Connection: &conn,
		BridgeChannelID: &channel, BridgeChannelName: &name, Mentions: set, LimiterLimit: in.Limiter.Limit,
		LimiterPerSeconds: in.Limiter.PerSeconds, Health: Health{State: "healthy"}, Routes: []RouteRef{},
		CreatedAt: now, Version: 1}
	planOwner(in, &d)
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
			ChannelID: text(d.BridgeChannelID), ChannelName: text(d.BridgeChannelName), Mentions: set,
			LimiterLimit: d.LimiterLimit, LimiterPerSeconds: d.LimiterPerSeconds, Now: now})
		if err != nil {
			return fmt.Errorf("create the destination: %w", nameTaken(err))
		}
		d.ID = id
		if err := s.writeOwner(ctx, q, id, d); err != nil {
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
// Destination check passed, limited by the Destination, and keeps the channel's name the check read; a passing check
// of a Broken Destination ends its Broken state. An update that changes nothing, and whose check read the same name,
// writes nothing.
func (s *Service) updateBridge(ctx context.Context, r Requester, before Destination, in Input) (Destination, error) {
	checked, err := s.checkBridge(ctx, in, &before.ID)
	if err != nil {
		return Destination{}, err
	}
	set, err := json.Marshal(in.Mentions)
	if err != nil {
		return Destination{}, fmt.Errorf("encode the mention settings: %w", err)
	}
	d := before
	conn, channel, name := in.Bridge.Connection, in.Bridge.ChannelID, checked.Check.Name
	d.Name, d.Connection, d.BridgeChannelID, d.BridgeChannelName = in.Name, &conn, &channel, &name
	d.Mentions, d.LimiterLimit, d.LimiterPerSeconds = set, in.Limiter.Limit, in.Limiter.PerSeconds
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
				ChannelID: text(d.BridgeChannelID), ChannelName: text(d.BridgeChannelName), Mentions: set,
				LimiterLimit: d.LimiterLimit, LimiterPerSeconds: d.LimiterPerSeconds,
				Now: s.writer.Business.Now().UTC()}); err != nil {
				return fmt.Errorf("update the destination %s: %w", before.PublicID, nameTaken(err))
			}
			d.Version++
			if err := s.writeOwner(ctx, q, before.ID, d); err != nil {
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
func (s *Service) validateSlack(ctx context.Context, in *Input) error {
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
	return s.writer.Mentions.Validate(ctx, in.Type, in.Mentions)
}

// checkSlack runs the Destination check of the Slack channel of in for the Destination destinationID, nil before it
// exists, and refuses a failing one with a *CheckFailedError: its item channel at /channel_id, or at /connection_id
// when Slack refused the bot token or the token lacks a scope. Its requests are recorded as destination_check under a
// check run of their own (C-23.FR-2).
func (s *Service) checkSlack(ctx context.Context, in Input, destinationID *int64) (SlackChannelChecked, error) {
	ctx, _ = outbound.StartCheck(ctx, outbound.CheckDestination, idOf(destinationID))
	sl := in.Slack
	res, err := s.writer.Slack.CheckSlackChannel(ctx, SlackChannelCheck{Connection: sl.Connection,
		Destination: destinationID, ChannelID: sl.ChannelID})
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
	set, err := json.Marshal(in.Mentions)
	if err != nil {
		return Destination{}, fmt.Errorf("encode the mention settings: %w", err)
	}
	now := s.writer.Business.Now().UTC()
	conn, channel, name := in.Slack.Connection, in.Slack.ChannelID, checked.Check.Name
	d := Destination{PublicID: publicid.New(publicid.Destination), Type: in.Type, Name: in.Name, Connection: &conn,
		SlackChannelID: &channel, SlackChannelName: &name, Mentions: set, LimiterLimit: in.Limiter.Limit,
		LimiterPerSeconds: in.Limiter.PerSeconds, Health: Health{State: "healthy"}, Routes: []RouteRef{},
		CreatedAt: now, Version: 1}
	planOwner(in, &d)
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
			ChannelID: text(d.SlackChannelID), ChannelName: text(d.SlackChannelName), Mentions: set,
			LimiterLimit: d.LimiterLimit, LimiterPerSeconds: d.LimiterPerSeconds, Now: now})
		if err != nil {
			return fmt.Errorf("create the destination: %w", nameTaken(err))
		}
		d.ID = id
		if err := s.writeOwner(ctx, q, id, d); err != nil {
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
// check passed, limited by the Destination, and keeps the channel's name the check read; a passing check of a Broken
// Destination ends its Broken state. An update that changes nothing, and whose check read the same name, writes
// nothing.
func (s *Service) updateSlack(ctx context.Context, r Requester, before Destination, in Input) (Destination, error) {
	checked, err := s.checkSlack(ctx, in, &before.ID)
	if err != nil {
		return Destination{}, err
	}
	set, err := json.Marshal(in.Mentions)
	if err != nil {
		return Destination{}, fmt.Errorf("encode the mention settings: %w", err)
	}
	d := before
	conn, channel, name := in.Slack.Connection, in.Slack.ChannelID, checked.Check.Name
	d.Name, d.Connection, d.SlackChannelID, d.SlackChannelName = in.Name, &conn, &channel, &name
	d.Mentions, d.LimiterLimit, d.LimiterPerSeconds = set, in.Limiter.Limit, in.Limiter.PerSeconds
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
				ChannelID: text(d.SlackChannelID), ChannelName: text(d.SlackChannelName), Mentions: set,
				LimiterLimit: d.LimiterLimit, LimiterPerSeconds: d.LimiterPerSeconds,
				Now: s.writer.Business.Now().UTC()}); err != nil {
				return fmt.Errorf("update the destination %s: %w", before.PublicID, nameTaken(err))
			}
			d.Version++
			if err := s.writeOwner(ctx, q, before.ID, d); err != nil {
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

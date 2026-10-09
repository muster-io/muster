// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package destinations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/destinations/dbgen"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/mattermost"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/proxyconf"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/webhooks"
)

// The Destination types.
const (
	TypeMattermost = "mattermost"
	TypeTelegram   = "telegram"
	TypeWebhook    = "webhook"
)

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

// CheckItem is one step of a Destination check with its result: when it failed, its message and, on a save, the field
// it concerns.
type CheckItem struct {
	Name    string
	OK      bool
	Message string
	Pointer string
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

// WebhookInput are the fields of an outgoing webhook Destination (C-15.FR-1): its mode, the request of the events
// mode and its proxy. Only the mode events is written yet; template and both answer unsupported at /mode until S-045.
type WebhookInput struct {
	Mode   string
	Events *webhooks.EventsConfig
	Proxy  proxyconf.Input
}

// Input is what createDestination and updateDestination write: the common fields and those of its type. Telegram
// answers unsupported at /type until S-042.
type Input struct {
	Type       string
	Name       string
	Mentions   mentions.Settings
	Limiter    Limiter
	Mattermost *MattermostInput
	Webhook    *WebhookInput
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

// MentionValidator validates the Mention settings of a Destination type (C-12.FR-8), declared by its consumer;
// *mentions.Service implements it.
type MentionValidator interface {
	Validate(ctx context.Context, destinationType string, set mentions.Settings) error
}

// Healthy ends the Broken state of the Destination destinationID after a successful Destination check started by a
// person (C-13.FR-10): delivery's MarkHealthy.
type Healthy func(ctx context.Context, destinationID int64) error

// writeQueries are the queries of a save of a Destination.
type writeQueries interface {
	InsertWebhookDestination(ctx context.Context, arg dbgen.InsertWebhookDestinationParams) (int64, error)
	UpdateWebhookDestination(ctx context.Context, arg dbgen.UpdateWebhookDestinationParams) error
	LockMattermostConnection(ctx context.Context, arg dbgen.LockMattermostConnectionParams) (int64, error)
	InsertMattermostDestination(ctx context.Context, arg dbgen.InsertMattermostDestinationParams) (int64, error)
	UpdateMattermostDestination(ctx context.Context, arg dbgen.UpdateMattermostDestinationParams) error
}

// view is what the Audit log diff of a saved Destination shows: never a secret, whose change is marked apart.
type view struct {
	Name       string                 `json:"name"`
	Connection *string                `json:"connection_id,omitempty"`
	TeamID     *string                `json:"team_id,omitempty"`
	ChannelID  *string                `json:"channel_id,omitempty"`
	Mode       *string                `json:"mode,omitempty"`
	Events     *webhooks.EventsConfig `json:"events,omitempty"`
	Proxy      *proxyconf.Config      `json:"proxy,omitempty"`
	Mentions   mentions.Settings      `json:"mentions"`
	Limiter    Limiter                `json:"limiter"`
}

func viewOf(d Destination) (view, error) {
	v := view{Name: d.Name, Connection: d.Connection, TeamID: d.MattermostTeamID, ChannelID: d.MattermostChannelID,
		Mode: d.WebhookMode, Limiter: Limiter{Limit: d.LimiterLimit, PerSeconds: d.LimiterPerSeconds}}
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
	case in.Type == TypeTelegram:
		return &FieldError{Pointer: "/type", Code: CodeUnsupported,
			Detail: "Saving this type of Destination is not supported yet."}
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

// validateWebhook checks the fields of an outgoing webhook (C-15.FR-1): the mode events with its request, parsed and
// run on a dry run in the template sandbox; the other modes are not supported until S-045.
func (s *Service) validateWebhook(w *WebhookInput) error {
	switch {
	case w.Mode == webhooks.ModeTemplate || w.Mode == webhooks.ModeBoth:
		return &FieldError{Pointer: "/mode", Code: CodeUnsupported,
			Detail: "The modes template and both are not supported yet."}
	case w.Mode != webhooks.ModeEvents:
		return &FieldError{Pointer: "/mode", Code: CodeInvalidFormat, Detail: "The mode is not events."}
	case w.Events == nil:
		return &FieldError{Pointer: "/events", Code: CodeRequired, Detail: "The mode events needs its request."}
	}
	if err := webhooks.Validate(s.writer.Templates, "/events", w.Events); err != nil {
		if fe, ok := errors.AsType[*webhooks.FieldError](err); ok {
			return &FieldError{Pointer: fe.Pointer, Code: fe.Code, Detail: fe.Detail, Line: fe.Line, Column: fe.Column}
		}
		return err
	}
	return nil
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
	d := Destination{PublicID: publicid.New(publicid.Destination), Type: in.Type, Name: in.Name, WebhookMode: &mode,
		EventsConfig: in.Webhook.Events.JSON(), Proxy: proxyRead(sec.proxy, sec.password),
		SigningSecret: SigningSecret{Set: true, UpdatedAt: &now}, SigningSecretOnce: secret, Mentions: set,
		LimiterLimit: in.Limiter.Limit, LimiterPerSeconds: in.Limiter.PerSeconds, Health: Health{State: "healthy"},
		Routes: []RouteRef{}, CreatedAt: now, Version: 1}
	after, err := viewOf(d)
	if err != nil {
		return Destination{}, err
	}
	err = s.writer.Writer.InTx(ctx, func(q TxQueries) error {
		p := dbgen.InsertWebhookDestinationParams{OrgID: s.orgID, PublicID: d.PublicID, Name: d.Name,
			WebhookMode: text(d.WebhookMode), WebhookEventsConfig: d.EventsConfig, Proxy: sec.proxy.JSON(),
			SigningSecretCiphertext: signing.Ciphertext, SigningSecretKeyID: nonEmpty(signing.KeyID), Now: now,
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
		return s.record(ctx, q, r, ActionCreated, d, append(audit.Created(after), sec.diff...))
	})
	if err != nil {
		return Destination{}, err
	}
	return d, nil
}

// updateWebhook replaces the configured fields of the outgoing webhook before, already validated; its Signing secrets
// and Secrets change through their own operations. An update that changes nothing writes nothing.
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
	d.Name, d.WebhookMode, d.EventsConfig, d.Mentions = in.Name, &mode, in.Webhook.Events.JSON(), set
	d.LimiterLimit, d.LimiterPerSeconds = in.Limiter.Limit, in.Limiter.PerSeconds
	d.Proxy = proxyRead(sec.proxy, sec.password)
	if !sec.given {
		d.Proxy.PasswordSet, d.Proxy.PasswordUpdatedAt = before.Proxy.PasswordSet, before.Proxy.PasswordUpdatedAt
	}
	old, err := viewOf(before)
	if err != nil {
		return Destination{}, err
	}
	after, err := viewOf(d)
	if err != nil {
		return Destination{}, err
	}
	diff := append(audit.Diff(old, after), sec.diff...)
	if len(diff) == 0 && !sec.given {
		return d, nil
	}
	err = s.writer.Writer.InTx(ctx, func(q TxQueries) error {
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
			WebhookEventsConfig: d.EventsConfig, Proxy: sec.proxy.JSON(), PasswordGiven: sec.given, Now: now,
			Mentions: set, LimiterLimit: d.LimiterLimit, LimiterPerSeconds: d.LimiterPerSeconds, OrgID: s.orgID,
			ID: before.ID}
		if sec.password.Set() {
			p.ProxyPasswordCiphertext, p.ProxyPasswordKeyID = sec.password.Ciphertext, nonEmpty(sec.password.KeyID)
		}
		if err := q.UpdateWebhookDestination(ctx, p); err != nil {
			return fmt.Errorf("update the destination %s: %w", before.PublicID, nameTaken(err))
		}
		d.Version++
		if len(diff) == 0 {
			return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint, ID: d.PublicID})
		}
		return s.record(ctx, q, r, ActionUpdated, d, diff)
	})
	if err != nil {
		return Destination{}, err
	}
	return d, nil
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

// check runs the Destination check of in for the Destination destinationID, nil before it exists, and refuses a failing
// one with a *CheckFailedError: one item per failing check at the field it concerns.
func (s *Service) check(ctx context.Context, in Input, destinationID *int64) (ChannelChecked, error) {
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
		failed.Items = append(failed.Items, CheckItem{Name: st.Name, Message: st.Message, Pointer: pointer})
	}
	return ChannelChecked{}, failed
}

// Create creates a Mattermost Destination (C-13.FR-2, FR-3, C-11.FR-18) once its Destination check passed on the
// interactive path; the check reads the names of its team and channel, which the Destination keeps. An outgoing webhook
// in the events mode has no check: it is created with its first Signing secret (C-15.FR-1, FR-5). Telegram is refused
// as unsupported. It is recorded as destination.created.
func (s *Service) Create(ctx context.Context, r Requester, in Input) (Destination, error) {
	if err := s.validate(ctx, &in, nil); err != nil {
		return Destination{}, err
	}
	if in.Type == TypeWebhook {
		return s.createWebhook(ctx, r, in)
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
	after, err := viewOf(d)
	if err != nil {
		return Destination{}, err
	}
	err = s.writer.Writer.InTx(ctx, func(q TxQueries) error {
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
		return s.record(ctx, q, r, ActionCreated, d, audit.Created(after))
	})
	if err != nil {
		return Destination{}, err
	}
	return d, nil
}

// Update replaces the configured fields of the Destination publicID (C-13.FR-2, FR-3); a non-nil version must be its
// current one (If-Match). Saving runs the Destination check as Create does, limited by the Destination; a passing
// check of a Broken Destination ends its Broken state (C-13.FR-10). An update that changes nothing writes nothing.
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
	if in.Type == TypeWebhook {
		return s.updateWebhook(ctx, r, before, in)
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
	after, err := viewOf(d)
	if err != nil {
		return Destination{}, err
	}
	diff := audit.Diff(old, after)
	namesChanged := deref(before.MattermostTeamName) != checked.Check.TeamName ||
		deref(before.MattermostChannelName) != checked.Check.ChannelName
	if len(diff) > 0 || namesChanged {
		err = s.writer.Writer.InTx(ctx, func(q TxQueries) error {
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

// CheckResult is the result of checkDestination: whether every check passed, each check, and the health after it.
type CheckResult struct {
	OK     bool
	Items  []CheckItem
	Health Health
}

// Check runs the Destination check of the Destination publicID from checkDestination (C-13.FR-10) on the interactive
// path, limited by the Destination: each check with its result. A passing check of a Broken Destination ends its
// Broken state. A type without a Destination check here is a *FieldError check_not_supported.
func (s *Service) Check(ctx context.Context, publicID string) (CheckResult, error) {
	d, err := s.Get(ctx, publicID)
	if err != nil {
		return CheckResult{}, err
	}
	if d.Type != TypeMattermost || d.Connection == nil {
		return CheckResult{}, &FieldError{Pointer: pointerCheckNotAllowed, Code: CodeCheckNotSupported,
			Detail: "This type of Destination has no Destination check."}
	}
	res, err := s.writer.Mattermost.CheckChannel(ctx, ChannelCheck{Connection: *d.Connection, Destination: &d.ID,
		TeamID: deref(d.MattermostTeamID), ChannelID: deref(d.MattermostChannelID)})
	if err != nil {
		return CheckResult{}, err
	}
	out := CheckResult{OK: res.Check.OK(), Health: d.Health}
	for _, st := range res.Check.Steps {
		out.Items = append(out.Items, CheckItem{Name: st.Name, OK: st.OK, Message: st.Message})
	}
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

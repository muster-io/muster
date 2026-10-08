// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package destinations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/destinations/dbgen"
	"github.com/muster-io/muster/internal/mattermost"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/publicid"
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
// the validation-failed problem.
type FieldError struct {
	Pointer string
	Code    string
	Detail  string
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

// Input is what createDestination and updateDestination write: the common fields and those of its type. Only the
// Mattermost type has its write path yet; Telegram and outgoing webhook answer unsupported at /type until S-042 and
// S-044.
type Input struct {
	Type       string
	Name       string
	Mentions   mentions.Settings
	Limiter    Limiter
	Mattermost *MattermostInput
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
	LockMattermostConnection(ctx context.Context, arg dbgen.LockMattermostConnectionParams) (int64, error)
	InsertMattermostDestination(ctx context.Context, arg dbgen.InsertMattermostDestinationParams) (int64, error)
	UpdateMattermostDestination(ctx context.Context, arg dbgen.UpdateMattermostDestinationParams) error
}

// view is what the Audit log diff of a saved Destination shows.
type view struct {
	Name       string            `json:"name"`
	Connection *string           `json:"connection_id,omitempty"`
	TeamID     *string           `json:"team_id,omitempty"`
	ChannelID  *string           `json:"channel_id,omitempty"`
	Mentions   mentions.Settings `json:"mentions"`
	Limiter    Limiter           `json:"limiter"`
}

func viewOf(d Destination) (view, error) {
	v := view{Name: d.Name, Connection: d.Connection, TeamID: d.MattermostTeamID, ChannelID: d.MattermostChannelID,
		Limiter: Limiter{Limit: d.LimiterLimit, PerSeconds: d.LimiterPerSeconds}}
	if len(d.Mentions) > 0 {
		if err := json.Unmarshal(d.Mentions, &v.Mentions); err != nil {
			return view{}, fmt.Errorf("read the mention settings of %s: %w", d.PublicID, err)
		}
	}
	return v, nil
}

// validate checks the common fields of in and the fields of its type, before the Destination check; stored is nil on a
// creation.
func (s *Service) validate(ctx context.Context, in *Input, stored *Destination) error {
	switch {
	case stored != nil && stored.Type != in.Type:
		return &FieldError{Pointer: "/type", Code: CodeInvalidFormat, Detail: "The type of a Destination cannot change."}
	case in.Type == TypeTelegram || in.Type == TypeWebhook:
		return &FieldError{Pointer: "/type", Code: CodeUnsupported,
			Detail: "Saving this type of Destination is not supported yet."}
	case in.Type != TypeMattermost || in.Mattermost == nil:
		return &FieldError{Pointer: "/type", Code: CodeInvalidFormat, Detail: "The type is not mattermost."}
	}
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
// interactive path; the check reads the names of its team and channel, which the Destination keeps. Other types are
// refused as unsupported. It is recorded as destination.created.
func (s *Service) Create(ctx context.Context, r Requester, in Input) (Destination, error) {
	if err := s.validate(ctx, &in, nil); err != nil {
		return Destination{}, err
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

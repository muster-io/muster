// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package organization

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization/dbgen"
	"github.com/muster-io/muster/internal/proxyconf"
)

// The Audit log action and resource type of the organization resource.
const (
	ActionUpdated    = "organization.updated"
	ResourceType     = "organization"
	HintOrganization = "organization"
	// HintSystemStatus is live.HintSystemStatus, which this package cannot import: a new or cleared URL of the
	// outgoing heartbeat clears its last result, which the System status shows.
	HintSystemStatus = "system-status"
)

// The Secret fields of the organization resource, as the encrypted_values view names them: the key of the encryption
// binds each ciphertext to its field.
const (
	FieldHeartbeatURL           = "organizations.outgoing_heartbeat_url"
	FieldHeartbeatProxyPassword = "organizations.outgoing_heartbeat_proxy_password"
)

// maxHeartbeatURL bounds the URL of the outgoing heartbeat.
const maxHeartbeatURL = 2000

// ErrVersionMismatch is an If-Match that names another version of the organization resource.
var ErrVersionMismatch = errors.New("the organization changed since it was read")

// errNoKeyring is an update of a Secret by a Service without the Keyring that encrypts it.
var errNoKeyring = errors.New("the organization service has no keyring to encrypt a secret with")

// Covers reports whether the policy demands TOTP of an account; local is an account that has a password (C-03.FR-10).
func (p TOTPPolicy) Covers(local bool) bool {
	return p == TOTPEveryone || (p == TOTPLocalUsers && local)
}

// Proxy is a per-client proxy as stored (C-02.FR-22), without its password: the shared proxy settings object.
type Proxy = proxyconf.Config

// SecretState is what a read shows of a Secret: whether it is set and when it last changed.
type SecretState struct {
	Set       bool
	UpdatedAt *time.Time
}

// OutgoingHeartbeat is the outgoing heartbeat of the Organization as read: its URL and proxy password are Secrets.
type OutgoingHeartbeat struct {
	URL           SecretState
	Proxy         Proxy
	ProxyPassword SecretState
}

// Organization is the organization resource (C-03.FR-20).
type Organization struct {
	PublicID string
	Name     string
	TimeZone string
	// WorkdayStart and WeekStart are organization.workday_start and organization.week_start (C-20.FR-9).
	WorkdayStart      string
	WeekStart         string
	SeverityLabel     string
	SeverityMapping   []SeverityMapping
	SeverityStyles    []SeverityStyle
	CriticalIsUrgent  bool
	InstanceLabels    []string
	Retention         Retention
	TOTPRequired      TOTPPolicy
	OIDCTokenGrace    time.Duration
	OutgoingHeartbeat OutgoingHeartbeat
	Version           int64
}

// SecretInput is a Secret field of an update: omitted keeps the stored value, null clears it, a value replaces it.
type SecretInput = keyring.SecretInput

// ProxyInput is a per-client proxy as written: an omitted type, address or username keeps the stored one and a null
// username clears it.
type ProxyInput = proxyconf.Input

// Input is an update of the organization resource.
type Input struct {
	Name     string
	TimeZone string
	// WorkdayStart and WeekStart are organization.workday_start and organization.week_start; nil keeps the stored one.
	WorkdayStart          *string
	WeekStart             *string
	SeverityLabel         string
	SeverityMapping       []SeverityMapping
	SeverityStyles        []SeverityStyle
	CriticalIsUrgent      bool
	InstanceLabels        []string
	Retention             Retention
	TOTPRequired          TOTPPolicy
	OIDCTokenGraceSeconds int64
	HeartbeatURL          SecretInput
	HeartbeatProxy        ProxyInput
}

// FieldError is a field of an update that is not valid, at a JSON pointer of the request body.
type FieldError struct {
	Pointer string
	Code    string
	Detail  string
}

func (e *FieldError) Error() string { return e.Pointer + ": " + e.Detail }

// SettingsQueries are the queries of the organization resource, with the insert of the Audit log and the hint.
type SettingsQueries interface {
	GetSettings(ctx context.Context, orgID int64) (dbgen.GetSettingsRow, error)
	LockSettings(ctx context.Context, orgID int64) (int64, error)
	GetOutgoingHeartbeatSecrets(ctx context.Context, orgID int64) (dbgen.GetOutgoingHeartbeatSecretsRow, error)
	UpdateSettings(ctx context.Context, arg dbgen.UpdateSettingsParams) (int64, error)
	GetOutboundPolicySettings(ctx context.Context, orgID int64) (dbgen.GetOutboundPolicySettingsRow, error)
	LockOutboundPolicy(ctx context.Context, orgID int64) (int64, error)
	UpdateOutboundPolicy(ctx context.Context, arg dbgen.UpdateOutboundPolicyParams) (int64, error)
	ListSeverityValues(ctx context.Context, arg dbgen.ListSeverityValuesParams) ([]dbgen.ListSeverityValuesRow, error)
	audit.Store
	// Notify sends a live-update hint, in the transaction when the queries run in one.
	Notify(ctx context.Context, h db.Hint) error
	// NotifyOutboundPolicy sends NOTIFY on OutboundPolicyChannel, in the transaction when the queries run in one.
	NotifyOutboundPolicy(ctx context.Context, orgID int64) error
}

// SettingsStore runs the queries alone or in one transaction.
type SettingsStore interface {
	SettingsQueries
	InTx(ctx context.Context, f func(SettingsQueries) error) error
}

// NewSettingsStore is the SettingsStore over the main pool.
func NewSettingsStore(pool *pgxpool.Pool) SettingsStore {
	return pgSettings{pgSettingsQueries: newSettingsQueries(pool), pool: pool}
}

type pgSettingsQueries struct {
	*dbgen.Queries
	audit.Store
	exec db.Execer
}

func newSettingsQueries(d dbgen.DBTX) pgSettingsQueries {
	return pgSettingsQueries{Queries: dbgen.New(d), Store: audit.NewStore(d), exec: d}
}

func (q pgSettingsQueries) Notify(ctx context.Context, h db.Hint) error {
	return db.NotifyHint(ctx, q.exec, h)
}

func (q pgSettingsQueries) NotifyOutboundPolicy(ctx context.Context, orgID int64) error {
	if _, err := q.exec.Exec(ctx, "SELECT pg_notify($1, $2)", OutboundPolicyChannel,
		strconv.FormatInt(orgID, 10)); err != nil {
		return fmt.Errorf("notify %s: %w", OutboundPolicyChannel, err)
	}
	return nil
}

type pgSettings struct {
	pgSettingsQueries
	pool *pgxpool.Pool
}

func (s pgSettings) InTx(ctx context.Context, f func(SettingsQueries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return f(newSettingsQueries(tx)) })
}

// Service reads and changes the organization resource and the outbound address policy of the Organization.
type Service struct {
	orgID   int64
	store   SettingsStore
	audit   *audit.Writer
	clock   clock.Clock
	keyring *keyring.Keyring
	log     *logging.Logger
	// policyChanged runs after a committed change of the outbound address policy.
	policyChanged func()
}

// WithLog gives the Service the logger of organization_updated and outbound_policy_updated; without one, it logs
// nothing.
func (s *Service) WithLog(l *logging.Logger) *Service {
	s.log = l
	return s
}

// WithOutboundPolicyChanged runs f after each committed change of the outbound address policy, on the replica that
// made it: the replica applies the change at once, without waiting for its own notification.
func (s *Service) WithOutboundPolicyChanged(f func()) *Service {
	s.policyChanged = f
	return s
}

// WithKeyring gives the Service the Keyring that encrypts the Secrets of the outgoing heartbeat; without one, an
// update that sets one fails.
func (s *Service) WithKeyring(k *keyring.Keyring) *Service {
	s.keyring = k
	return s
}

// NewService returns the Service of the Organization orgID; clock is the business clock.
func NewService(orgID int64, s SettingsStore, w *audit.Writer, business clock.Clock) *Service {
	return &Service{orgID: orgID, store: s, audit: w, clock: business}
}

// Get reads the organization resource.
func (s *Service) Get(ctx context.Context) (Organization, error) {
	return s.get(ctx, s.store)
}

func (s *Service) get(ctx context.Context, q SettingsQueries) (Organization, error) {
	row, err := q.GetSettings(ctx, s.orgID)
	if err != nil {
		return Organization{}, fmt.Errorf("read the organization: %w", err)
	}
	o := Organization{
		PublicID: row.PublicID, Name: row.Name, TimeZone: row.TimeZone, WorkdayStart: row.WorkdayStart,
		WeekStart: row.WeekStart, SeverityLabel: row.SeverityLabel, CriticalIsUrgent: row.CriticalIsUrgent,
		InstanceLabels: row.InstanceLabels,
		Retention: Retention{
			StoredSnapshotsDays: row.RetentionStoredSnapshotsDays, AlertDetailsDays: row.RetentionAlertDetailsDays,
			AlertGroupSummariesDays: row.RetentionAlertGroupSummariesDays, AuditLogDays: row.RetentionAuditLogDays,
		},
		TOTPRequired:   TOTPPolicy(row.TotpRequired),
		OIDCTokenGrace: time.Duration(row.OidcTokenGraceSeconds) * time.Second,
		OutgoingHeartbeat: OutgoingHeartbeat{
			URL:           SecretState{Set: row.OutgoingHeartbeatUrlSet, UpdatedAt: timePtr(row.OutgoingHeartbeatUrlUpdatedAt)},
			ProxyPassword: SecretState{Set: row.OutgoingHeartbeatProxyPasswordSet, UpdatedAt: timePtr(row.OutgoingHeartbeatProxyPasswordUpdatedAt)},
		},
		Version: row.Version,
	}
	if o.InstanceLabels == nil {
		o.InstanceLabels = []string{}
	}
	for _, f := range []struct {
		name string
		raw  []byte
		into any
	}{
		{"severity mapping", row.SeverityMapping, &o.SeverityMapping},
		{"severity styles", row.SeverityStyles, &o.SeverityStyles},
		{"outgoing heartbeat proxy", row.OutgoingHeartbeatProxy, &o.OutgoingHeartbeat.Proxy},
	} {
		if err := json.Unmarshal(f.raw, f.into); err != nil {
			return Organization{}, fmt.Errorf("read the %s: %w", f.name, err)
		}
	}
	return o, nil
}

// Update applies an update of every field of the organization resource (C-20.FR-1, FR-8, FR-9, C-04.FR-8); an omitted
// start of the working day or first day of the week keeps the stored one. version is the version of If-Match, nil for
// any; another one is ErrVersionMismatch. An invalid field is a *FieldError at the full JSON pointer of the field, and
// changes nothing. A change is recorded in the Audit log as organization.updated with its before/after diff, in which
// the Secrets show only that they changed, and announced to the live-updates streams with an organization hint, in
// the transaction of the change. An update that changes nothing writes nothing. A new or cleared URL clears the result
// of the last outgoing heartbeat and also sends a system-status hint. Severity levels and Instance labels apply from
// the next evaluation: their readers read them in their own transactions (C-20.FR-2).
func (s *Service) Update(ctx context.Context, r audit.Actor, t audit.Transport, addr netip.Addr, version *int64,
	in Input) (Organization, error) {
	var out Organization
	var changed []string
	err := s.store.InTx(ctx, func(q SettingsQueries) error {
		current, err := q.LockSettings(ctx, s.orgID)
		if err != nil {
			return fmt.Errorf("lock the organization: %w", err)
		}
		if version != nil && *version != current {
			return ErrVersionMismatch
		}
		before, err := s.get(ctx, q)
		if err != nil {
			return err
		}
		after, err := applyInput(before, in)
		if err != nil {
			return err
		}
		stored, err := q.GetOutgoingHeartbeatSecrets(ctx, s.orgID)
		if err != nil {
			return fmt.Errorf("read the secrets of the outgoing heartbeat: %w", err)
		}
		now := s.clock.Now().UTC()
		hbURL, urlChanged, err := s.applySecret(FieldHeartbeatURL, "/outgoing_heartbeat/url",
			secretOf(stored.OutgoingHeartbeatUrlCiphertext, stored.OutgoingHeartbeatUrlKeyID,
				stored.OutgoingHeartbeatUrlUpdatedAt), in.HeartbeatURL, now)
		if err != nil {
			return err
		}
		password, passwordChanged, err := s.applySecret(FieldHeartbeatProxyPassword,
			"/outgoing_heartbeat/proxy/password", secretOf(stored.OutgoingHeartbeatProxyPasswordCiphertext,
				stored.OutgoingHeartbeatProxyPasswordKeyID, stored.OutgoingHeartbeatProxyPasswordUpdatedAt),
			in.HeartbeatProxy.Password, now)
		if err != nil {
			return err
		}
		diff := audit.Diff(viewOf(before), viewOf(after))
		if urlChanged {
			diff = append(diff, audit.Change{Pointer: "/outgoing_heartbeat/url", SecretChanged: true})
		}
		if passwordChanged {
			diff = append(diff, audit.Change{Pointer: "/outgoing_heartbeat/proxy/password", SecretChanged: true})
		}
		if len(diff) == 0 {
			out = before
			return nil
		}
		mapping, err := json.Marshal(after.SeverityMapping)
		if err != nil {
			return err
		}
		styles, err := json.Marshal(after.SeverityStyles)
		if err != nil {
			return err
		}
		if _, err := q.UpdateSettings(ctx, dbgen.UpdateSettingsParams{
			OrgID:                            s.orgID,
			Name:                             after.Name,
			TimeZone:                         after.TimeZone,
			WorkdayStart:                     after.WorkdayStart,
			WeekStart:                        after.WeekStart,
			SeverityLabel:                    after.SeverityLabel,
			SeverityMapping:                  mapping,
			SeverityStyles:                   styles,
			CriticalIsUrgent:                 after.CriticalIsUrgent,
			InstanceLabels:                   after.InstanceLabels,
			RetentionStoredSnapshotsDays:     after.Retention.StoredSnapshotsDays,
			RetentionAlertDetailsDays:        after.Retention.AlertDetailsDays,
			RetentionAlertGroupSummariesDays: after.Retention.AlertGroupSummariesDays,
			RetentionAuditLogDays:            after.Retention.AuditLogDays,
			OidcTokenGraceSeconds:            int64(after.OIDCTokenGrace / time.Second),
			TotpRequired:                     string(after.TOTPRequired),
			UrlCiphertext:                    hbURL.Ciphertext,
			UrlKeyID:                         optText(hbURL.KeyID),
			UrlUpdatedAt:                     optTime(hbURL.UpdatedAt),
			Proxy:                            after.OutgoingHeartbeat.Proxy.JSON(),
			ProxyPasswordCiphertext:          password.Ciphertext,
			ProxyPasswordKeyID:               optText(password.KeyID),
			ProxyPasswordUpdatedAt:           optTime(password.UpdatedAt),
			UrlChanged:                       urlChanged,
			Now:                              now,
		}); err != nil {
			return fmt.Errorf("change the organization: %w", err)
		}
		if out, err = s.get(ctx, q); err != nil {
			return err
		}
		if err := s.audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: r, Transport: t, Action: ActionUpdated,
			Resource: audit.Resource{Type: ResourceType, PublicID: out.PublicID, Name: out.Name},
			Diff:     diff, SourceAddress: addr,
		}); err != nil {
			return err
		}
		if err := q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: HintOrganization}); err != nil {
			return err
		}
		for _, c := range diff {
			changed = append(changed, c.Pointer)
		}
		if urlChanged {
			return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: HintSystemStatus})
		}
		return nil
	})
	if err != nil {
		return Organization{}, err
	}
	if len(changed) > 0 && s.log != nil {
		s.log.Log(ctx, logging.OrganizationUpdated, logging.F("fields", strings.Join(changed, ",")))
	}
	return out, nil
}

// applyInput validates every field of in and returns the resource after them.
func applyInput(before Organization, in Input) (Organization, error) {
	if fe := validate(in); fe != nil {
		return Organization{}, fe
	}
	after := before
	after.Name, after.TimeZone = strings.TrimSpace(in.Name), in.TimeZone
	if in.WorkdayStart != nil {
		after.WorkdayStart = *in.WorkdayStart
	}
	if in.WeekStart != nil {
		after.WeekStart = *in.WeekStart
	}
	after.SeverityLabel, after.CriticalIsUrgent = in.SeverityLabel, in.CriticalIsUrgent
	after.SeverityMapping = append([]SeverityMapping{}, in.SeverityMapping...)
	after.SeverityStyles = append([]SeverityStyle{}, in.SeverityStyles...)
	after.InstanceLabels = append([]string{}, in.InstanceLabels...)
	after.Retention = in.Retention
	after.OIDCTokenGrace = time.Duration(in.OIDCTokenGraceSeconds) * time.Second
	after.TOTPRequired = in.TOTPRequired
	proxy, err := in.HeartbeatProxy.Apply(before.OutgoingHeartbeat.Proxy)
	if fe, ok := errors.AsType[*proxyconf.FieldError](err); ok {
		return Organization{}, &FieldError{Pointer: "/outgoing_heartbeat/proxy" + fe.Pointer, Code: fe.Code,
			Detail: fe.Detail}
	}
	if err != nil {
		return Organization{}, err
	}
	after.OutgoingHeartbeat.Proxy = proxy
	if u := in.HeartbeatURL; u.Given && !u.Null && u.Value != "" && !validHeartbeatURL(string(u.Value)) {
		return Organization{}, &FieldError{Pointer: "/outgoing_heartbeat/url", Code: "invalid_format",
			Detail: fmt.Sprintf("The URL is not an absolute http or https URL with a host and without a fragment, "+
				"of at most %d characters.", maxHeartbeatURL)}
	}
	return after, nil
}

// validHeartbeatURL reports whether raw is an absolute http or https URL with a host, without a fragment.
func validHeartbeatURL(raw string) bool {
	if len(raw) > maxHeartbeatURL || strings.TrimSpace(raw) != raw {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.Hostname() != "" &&
		u.Fragment == "" && !strings.Contains(raw, "#")
}

// applySecret applies in to the Secret field as stored; an empty value is a *FieldError at pointer, with or without
// a Keyring.
func (s *Service) applySecret(field, pointer string, stored keyring.StoredSecret, in SecretInput,
	now time.Time) (keyring.StoredSecret, bool, error) {
	if in.Given && !in.Null && in.Value == "" {
		return stored, false, emptySecret(pointer)
	}
	if s.keyring == nil && in.Given && !in.Null {
		return stored, false, errNoKeyring
	}
	out, changed, err := s.keyring.ApplySecret(field, stored, in, now)
	if errors.Is(err, keyring.ErrEmptySecret) {
		return stored, false, emptySecret(pointer)
	}
	return out, changed, err
}

func emptySecret(pointer string) *FieldError {
	return &FieldError{Pointer: pointer, Code: "too_short",
		Detail: "The value is empty; send null to clear it or omit it to keep it."}
}

func secretOf(ciphertext []byte, keyID pgtype.Text, updatedAt pgtype.Timestamptz) keyring.StoredSecret {
	return keyring.StoredSecret{Ciphertext: ciphertext, KeyID: keyID.String, UpdatedAt: timePtr(updatedAt)}
}

func optText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}

func optTime(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t.UTC(), Valid: true}
}

// view is what the Audit log diff of the organization resource shows.
type view struct {
	Name                  string            `json:"name"`
	TimeZone              string            `json:"time_zone"`
	WorkdayStart          string            `json:"workday_start"`
	WeekStart             string            `json:"week_start"`
	SeverityLabel         string            `json:"severity_label"`
	SeverityMapping       []SeverityMapping `json:"severity_mapping"`
	SeverityStyles        []SeverityStyle   `json:"severity_styles"`
	CriticalIsUrgent      bool              `json:"critical_is_urgent"`
	InstanceLabels        []string          `json:"instance_labels"`
	Retention             retentionView     `json:"retention"`
	TOTPRequired          string            `json:"totp_required"`
	OIDCTokenGraceSeconds int64             `json:"oidc_token_grace_seconds"`
	OutgoingHeartbeat     heartbeatView     `json:"outgoing_heartbeat"`
}

// retentionView is what the diff shows of the retention periods.
type retentionView struct {
	StoredSnapshotsDays     int64 `json:"stored_snapshots_days"`
	AlertDetailsDays        int64 `json:"alert_details_days"`
	AlertGroupSummariesDays int64 `json:"alert_group_summaries_days"`
	AuditLogDays            int64 `json:"audit_log_days"`
}

// heartbeatView is what the diff shows of the outgoing heartbeat besides its Secrets, which Update adds as changed.
type heartbeatView struct {
	Proxy proxyconf.Config `json:"proxy"`
}

func viewOf(o Organization) view {
	return view{
		Name: o.Name, TimeZone: o.TimeZone, WorkdayStart: o.WorkdayStart, WeekStart: o.WeekStart,
		SeverityLabel: o.SeverityLabel, SeverityMapping: o.SeverityMapping, SeverityStyles: o.SeverityStyles,
		CriticalIsUrgent: o.CriticalIsUrgent, InstanceLabels: o.InstanceLabels,
		Retention: retentionView{
			StoredSnapshotsDays: o.Retention.StoredSnapshotsDays, AlertDetailsDays: o.Retention.AlertDetailsDays,
			AlertGroupSummariesDays: o.Retention.AlertGroupSummariesDays, AuditLogDays: o.Retention.AuditLogDays,
		},
		TOTPRequired: string(o.TOTPRequired), OIDCTokenGraceSeconds: int64(o.OIDCTokenGrace / time.Second),
		OutgoingHeartbeat: heartbeatView{Proxy: o.OutgoingHeartbeat.Proxy},
	}
}

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

// The codes of field errors, from the catalogue of the API (x-problem-codes).
const (
	CodeRequired       = "required"
	CodeInvalidFormat  = "invalid_format"
	CodeTooShort       = "too_short"
	CodeTooLong        = "too_long"
	CodeOutOfRange     = "out_of_range"
	CodeDuplicate      = "duplicate"
	CodeRetentionOrder = "retention_order"
)

const (
	// maxNameLength bounds the name of the Organization, in characters.
	maxNameLength = 200
	// maxTimeZoneLength bounds the name of a time zone.
	maxTimeZoneLength = 64
	// maxEmojiBytes bounds the emoji of a Severity level, which may be a sequence of several code points.
	maxEmojiBytes = 64
	// MaxRetentionDays bounds every retention period: 100 years, far below the durations that would overflow.
	MaxRetentionDays = 36500
	// MaxOIDCTokenGraceSeconds bounds auth.oidc_token_grace, as MaxRetentionDays the retention periods.
	MaxOIDCTokenGraceSeconds = MaxRetentionDays * 24 * 3600
)

// labelName is the form of a Prometheus label name: the severity label and the Instance labels.
var labelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// hexColour is a CSS colour as #rrggbb.
var hexColour = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// timeOfDay is organization.workday_start: HH:MM on the 24-hour clock, the pattern of the API and of the CHECK.
var timeOfDay = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// weekdays are the values of organization.week_start, as the API and the CHECK name them.
var weekdays = []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}

// severityLevels are the Severity levels in the order the API shows them.
var severityLevels = []SeverityLevel{SeverityCritical, SeverityWarning, SeverityInfo}

// ValidTimeZone reports whether name is an IANA time zone that the Organization or a User profile may take: a name
// time.LoadLocation knows, never Local, the zone of the server.
func ValidTimeZone(name string) bool {
	if name == "" || name == "Local" || len(name) > maxTimeZoneLength {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

// validate returns the first field of in that is not valid, in the order of the request body, with the full JSON
// pointer of the field. The outgoing heartbeat is checked by applyInput.
func validate(in Input) *FieldError {
	name := strings.TrimSpace(in.Name)
	switch {
	case name == "":
		return &FieldError{Pointer: "/name", Code: CodeTooShort, Detail: "The name is empty."}
	case utf8.RuneCountInString(name) > maxNameLength:
		return &FieldError{Pointer: "/name", Code: CodeTooLong,
			Detail: fmt.Sprintf("The name is longer than %d characters.", maxNameLength)}
	case strings.IndexFunc(name, unicode.IsControl) >= 0:
		return &FieldError{Pointer: "/name", Code: CodeInvalidFormat, Detail: "The name has a control character."}
	case !ValidTimeZone(in.TimeZone):
		return &FieldError{Pointer: "/time_zone", Code: CodeInvalidFormat,
			Detail: "The time zone is not an IANA time zone such as Europe/Berlin."}
	case in.WorkdayStart != nil && !timeOfDay.MatchString(*in.WorkdayStart):
		return &FieldError{Pointer: "/workday_start", Code: CodeInvalidFormat,
			Detail: "The start of the working day is not a time HH:MM on the 24-hour clock, such as 09:00."}
	case in.WeekStart != nil && !slices.Contains(weekdays, *in.WeekStart):
		return &FieldError{Pointer: "/week_start", Code: CodeInvalidFormat,
			Detail: "The first day of the week is not a day from monday to sunday."}
	case !labelName.MatchString(in.SeverityLabel):
		return &FieldError{Pointer: "/severity_label", Code: CodeInvalidFormat,
			Detail: "The severity label is not a label name."}
	}
	if fe := validateMapping(in.SeverityMapping); fe != nil {
		return fe
	}
	if fe := validateStyles(in.SeverityStyles); fe != nil {
		return fe
	}
	seen := map[string]bool{}
	for i, l := range in.InstanceLabels {
		pointer := fmt.Sprintf("/instance_labels/%d", i)
		if !labelName.MatchString(l) {
			return &FieldError{Pointer: pointer, Code: CodeInvalidFormat, Detail: "The Instance label is not a label name."}
		}
		if seen[l] {
			return &FieldError{Pointer: pointer, Code: CodeDuplicate, Detail: "The Instance label appears twice."}
		}
		seen[l] = true
	}
	if fe := validateRetention(in.Retention); fe != nil {
		return fe
	}
	switch in.TOTPRequired {
	case TOTPNobody, TOTPLocalUsers, TOTPEveryone:
	default:
		return &FieldError{Pointer: "/totp_required", Code: CodeInvalidFormat,
			Detail: "The TOTP policy is not nobody, local_users or everyone."}
	}
	if in.OIDCTokenGraceSeconds < 1 || in.OIDCTokenGraceSeconds > MaxOIDCTokenGraceSeconds {
		return &FieldError{Pointer: "/oidc_token_grace_seconds", Code: CodeOutOfRange,
			Detail: fmt.Sprintf("The grace period is not from 1 to %d seconds.", MaxOIDCTokenGraceSeconds)}
	}
	return nil
}

func validLevel(l SeverityLevel) bool {
	return slices.Contains(severityLevels, l)
}

// validateMapping refuses an empty value, a value mapped twice and a level that is not one: routing matches the
// values exactly, so a second entry for a value would never apply.
func validateMapping(mapping []SeverityMapping) *FieldError {
	seen := map[string]bool{}
	for i, m := range mapping {
		pointer := fmt.Sprintf("/severity_mapping/%d", i)
		switch {
		case m.Value == "":
			return &FieldError{Pointer: pointer + "/value", Code: CodeTooShort, Detail: "The value is empty."}
		case seen[m.Value]:
			return &FieldError{Pointer: pointer + "/value", Code: CodeDuplicate,
				Detail: "The value is mapped twice."}
		case !validLevel(m.Level):
			return &FieldError{Pointer: pointer + "/level", Code: CodeInvalidFormat,
				Detail: "The level is not critical, warning or info."}
		}
		seen[m.Value] = true
	}
	return nil
}

// validateStyles demands exactly one style for each Severity level, with an emoji and a #rrggbb colour.
func validateStyles(styles []SeverityStyle) *FieldError {
	seen := map[SeverityLevel]bool{}
	for i, st := range styles {
		pointer := fmt.Sprintf("/severity_styles/%d", i)
		switch {
		case !validLevel(st.Level):
			return &FieldError{Pointer: pointer + "/level", Code: CodeInvalidFormat,
				Detail: "The level is not critical, warning or info."}
		case seen[st.Level]:
			return &FieldError{Pointer: pointer + "/level", Code: CodeDuplicate,
				Detail: "The level has a second style."}
		case strings.TrimSpace(st.Emoji) == "":
			return &FieldError{Pointer: pointer + "/emoji", Code: CodeTooShort, Detail: "The emoji is empty."}
		case len(st.Emoji) > maxEmojiBytes:
			return &FieldError{Pointer: pointer + "/emoji", Code: CodeTooLong,
				Detail: fmt.Sprintf("The emoji is longer than %d bytes.", maxEmojiBytes)}
		case !hexColour.MatchString(st.Color):
			return &FieldError{Pointer: pointer + "/color", Code: CodeInvalidFormat,
				Detail: "The colour is not a CSS hex colour such as #d32f2f."}
		}
		seen[st.Level] = true
	}
	for _, l := range severityLevels {
		if !seen[l] {
			return &FieldError{Pointer: "/severity_styles", Code: CodeRequired,
				Detail: fmt.Sprintf("The level %s has no style.", l)}
		}
	}
	return nil
}

// validateRetention bounds each period and keeps the summary rows at least as long as the details they belong to
// (C-20.AC-6), before the CHECK of the database refuses it.
func validateRetention(r Retention) *FieldError {
	for _, p := range []struct {
		pointer string
		days    int64
	}{
		{"/retention/stored_snapshots_days", r.StoredSnapshotsDays},
		{"/retention/alert_details_days", r.AlertDetailsDays},
		{"/retention/alert_group_summaries_days", r.AlertGroupSummariesDays},
		{"/retention/audit_log_days", r.AuditLogDays},
	} {
		if p.days < 1 || p.days > MaxRetentionDays {
			return &FieldError{Pointer: p.pointer, Code: CodeOutOfRange,
				Detail: fmt.Sprintf("The period is not from 1 to %d days.", MaxRetentionDays)}
		}
	}
	if r.AlertGroupSummariesDays < r.AlertDetailsDays {
		return &FieldError{Pointer: "/retention/alert_group_summaries_days", Code: CodeRetentionOrder,
			Detail: fmt.Sprintf("Keep Alert Group summaries at least as long as alert details (%d days).",
				r.AlertDetailsDays)}
	}
	return nil
}

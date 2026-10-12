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
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/keyring"
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
	PublicID          string
	Name              string
	TimeZone          string
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
	Name                  string
	TimeZone              string
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

// UnsupportedError refuses an update that changes fields that are not editable yet (C-03.FR-20): each pointer names
// one of them in the request body.
type UnsupportedError struct {
	Pointers []string
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("the fields %v cannot be changed yet", e.Pointers)
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
	audit.Store
	// Notify sends a live-update hint, in the transaction when the queries run in one.
	Notify(ctx context.Context, h db.Hint) error
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

type pgSettings struct {
	pgSettingsQueries
	pool *pgxpool.Pool
}

func (s pgSettings) InTx(ctx context.Context, f func(SettingsQueries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return f(newSettingsQueries(tx)) })
}

// Service reads and changes the organization resource of the Organization.
type Service struct {
	orgID   int64
	store   SettingsStore
	audit   *audit.Writer
	clock   clock.Clock
	keyring *keyring.Keyring
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
		PublicID: row.PublicID, Name: row.Name, TimeZone: row.TimeZone, SeverityLabel: row.SeverityLabel,
		CriticalIsUrgent: row.CriticalIsUrgent, InstanceLabels: row.InstanceLabels,
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

// Update applies an update of the organization resource. In this release the TOTP policy and the outgoing heartbeat,
// its URL and its proxy, are editable; an update that changes any other field is an *UnsupportedError naming each
// such field, and changes nothing. version is the version of If-Match, nil for any; another one is
// ErrVersionMismatch. An invalid field is a *FieldError. A change is recorded in the Audit log as organization.updated
// with its before/after diff, in which the Secrets show only that they changed, and announced to the live-updates
// streams with an organization hint, in the transaction of the change. An update that changes nothing writes nothing.
// A new or cleared URL clears the result of the last outgoing heartbeat and also sends a system-status hint.
func (s *Service) Update(ctx context.Context, r audit.Actor, t audit.Transport, addr netip.Addr, version *int64,
	in Input) (Organization, error) {
	var out Organization
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
		if p := unsupported(before, in); len(p) > 0 {
			return &UnsupportedError{Pointers: p}
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
		if _, err := q.UpdateSettings(ctx, dbgen.UpdateSettingsParams{
			OrgID: s.orgID, TotpRequired: string(after.TOTPRequired),
			UrlCiphertext: hbURL.Ciphertext, UrlKeyID: optText(hbURL.KeyID), UrlUpdatedAt: optTime(hbURL.UpdatedAt),
			Proxy:                   after.OutgoingHeartbeat.Proxy.JSON(),
			ProxyPasswordCiphertext: password.Ciphertext, ProxyPasswordKeyID: optText(password.KeyID),
			ProxyPasswordUpdatedAt: optTime(password.UpdatedAt), UrlChanged: urlChanged, Now: now,
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
		if urlChanged {
			return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: HintSystemStatus})
		}
		return nil
	})
	if err != nil {
		return Organization{}, err
	}
	return out, nil
}

// applyInput validates the editable fields of in and returns the resource after them.
func applyInput(before Organization, in Input) (Organization, error) {
	after := before
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

// unsupported returns the pointers of the fields that in changes and that are not editable yet.
func unsupported(o Organization, in Input) []string {
	var out []string
	add := func(changed bool, pointer string) {
		if changed {
			out = append(out, pointer)
		}
	}
	add(in.Name != o.Name, "/name")
	add(in.TimeZone != o.TimeZone, "/time_zone")
	add(in.SeverityLabel != o.SeverityLabel, "/severity_label")
	add(!slices.Equal(in.SeverityMapping, o.SeverityMapping), "/severity_mapping")
	add(!slices.Equal(in.SeverityStyles, o.SeverityStyles), "/severity_styles")
	add(in.CriticalIsUrgent != o.CriticalIsUrgent, "/critical_is_urgent")
	add(!slices.Equal(in.InstanceLabels, o.InstanceLabels), "/instance_labels")
	add(in.Retention.StoredSnapshotsDays != o.Retention.StoredSnapshotsDays, "/retention/stored_snapshots_days")
	add(in.Retention.AlertDetailsDays != o.Retention.AlertDetailsDays, "/retention/alert_details_days")
	add(in.Retention.AlertGroupSummariesDays != o.Retention.AlertGroupSummariesDays,
		"/retention/alert_group_summaries_days")
	add(in.Retention.AuditLogDays != o.Retention.AuditLogDays, "/retention/audit_log_days")
	add(time.Duration(in.OIDCTokenGraceSeconds)*time.Second != o.OIDCTokenGrace, "/oidc_token_grace_seconds")
	return out
}

// view is what the Audit log diff of the organization resource shows.
type view struct {
	Name                  string            `json:"name"`
	TimeZone              string            `json:"time_zone"`
	SeverityLabel         string            `json:"severity_label"`
	SeverityMapping       []SeverityMapping `json:"severity_mapping"`
	SeverityStyles        []SeverityStyle   `json:"severity_styles"`
	CriticalIsUrgent      bool              `json:"critical_is_urgent"`
	InstanceLabels        []string          `json:"instance_labels"`
	TOTPRequired          string            `json:"totp_required"`
	OIDCTokenGraceSeconds int64             `json:"oidc_token_grace_seconds"`
	OutgoingHeartbeat     heartbeatView     `json:"outgoing_heartbeat"`
}

// heartbeatView is what the diff shows of the outgoing heartbeat besides its Secrets, which Update adds as changed.
type heartbeatView struct {
	Proxy proxyconf.Config `json:"proxy"`
}

func viewOf(o Organization) view {
	return view{
		Name: o.Name, TimeZone: o.TimeZone, SeverityLabel: o.SeverityLabel, SeverityMapping: o.SeverityMapping,
		SeverityStyles: o.SeverityStyles, CriticalIsUrgent: o.CriticalIsUrgent, InstanceLabels: o.InstanceLabels,
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

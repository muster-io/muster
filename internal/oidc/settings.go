// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package oidc is sign-in through an OpenID Connect identity provider (C-03): the OIDC settings with their write-only
// client secret, proxy, warnings and connection check, the back channel to the identity provider through the outbound
// HTTP package, and the redirect flow with PKCE that creates users on their first sign-in, maps their groups to a Role
// and opens their sessions, linking an identity to an account from its web session, and the offline token with the
// background re-checks of OIDC users at the identity provider.
package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/oidc/dbgen"
	"github.com/muster-io/muster/internal/proxyconf"
)

const (
	// SecretExpiryLead is oidc.secret_expiry_lead: the settings warn this long before the client secret expires.
	SecretExpiryLead = 14 * 24 * time.Hour
	// DefaultGroupsClaim and DefaultScopes are what a read shows before an Admin saves the settings.
	DefaultGroupsClaim = "groups"

	// The roles for users whose groups map to none (oidc.unmatched_role).
	UnmatchedNone      = "none"
	UnmatchedViewer    = auth.RoleViewer
	UnmatchedResponder = auth.RoleResponder

	// The Secret fields, as their ciphertexts are bound to them.
	fieldClientSecret  = "oidc_settings.client_secret"  //nolint:gosec // G101: the name of a field, not a credential
	fieldProxyPassword = "oidc_settings.proxy_password" //nolint:gosec // G101: the name of a field, not a credential

	maxDisplayName = 100
	maxURL         = 2000
	maxClaimName   = 200
	maxGroups      = 500
)

// DefaultScopes are the configured scopes before an Admin saves the settings; openid and offline_access are always
// added.
var DefaultScopes = []string{"profile", "email"}

// The Audit log actions and resource type of OIDC.
const (
	ActionSettingsUpdated   = "oidc_settings.updated"
	ActionSignInRefused     = "session.oidc_refused"
	ActionRoleSyncKeptAdmin = "user.role_sync_kept_admin"
	ResourceSettings        = "oidc_settings"
)

// The warning kinds of the settings (C-03.FR-5, FR-6, FR-32).
const (
	WarningNobodyCanSignIn    = "nobody_can_sign_in"
	WarningGroupsClaimMissing = "groups_claim_missing"
	WarningSecretExpiring     = "secret_expiring"
	WarningLastAdminKept      = "last_admin_kept"
)

var (
	// ErrVersionMismatch is an If-Match that names another version of the settings.
	ErrVersionMismatch = errors.New("the OIDC settings changed since they were read")
	// ErrNotEnabled is a sign-in or link while OIDC is off.
	ErrNotEnabled = errors.New("OIDC sign-in is switched off")
	// ErrTooManyRequests is a start while MaxPendingRequests redirects of the Organization are in flight.
	ErrTooManyRequests = errors.New("too many OIDC sign-ins are in flight")
	// ErrAlreadyLinked is a link started by an account that signs in through OIDC already, or has no password.
	ErrAlreadyLinked = errors.New("the account already signs in through OIDC")
)

// FieldError is a field of the settings that is not valid, at a JSON pointer of the request body.
type FieldError struct {
	Pointer string
	Code    string
	Detail  string
}

func (e *FieldError) Error() string { return e.Pointer + ": " + e.Detail }

// GroupMapping maps an IdP group to a Role.
type GroupMapping struct {
	Group string `json:"group"`
	Role  string `json:"role"`
}

// UserRef names a user in a warning.
type UserRef struct {
	PublicID string
	Name     string
	Login    string
}

// Warning is a warning of the settings; ExpiresOn goes with secret_expiring, User and Role with last_admin_kept.
type Warning struct {
	Kind      string
	ExpiresOn *time.Time
	User      *UserRef
	Role      string
}

// Settings are the OIDC settings as read: Secrets only as their status.
type Settings struct {
	// Configured is false until an Admin saves the settings; the other fields are then the defaults.
	Configured            bool
	Enabled               bool
	DisplayName           string
	IssuerURL             string
	ClientID              string
	ClientSecret          keyring.SecretStatus
	ClientSecretExpiresOn *time.Time
	Scopes                []string
	GroupsClaim           string
	GroupMappings         []GroupMapping
	UnmatchedRole         string
	SyncRole              bool
	SkipTOTPWithIDPMFA    bool
	Proxy                 proxyconf.Config
	ProxyPassword         keyring.SecretStatus
	// GroupsClaimMissing says that the last check or sign-in found no groups claim.
	GroupsClaimMissing bool
	Warnings           []Warning
	Version            int64
	UpdatedAt          *time.Time
}

// ButtonName is the provider name of the sign-in button: the display name, or the host of the issuer URL.
func (s Settings) ButtonName() string {
	if s.DisplayName != "" {
		return s.DisplayName
	}
	if u, err := url.Parse(s.IssuerURL); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return "OIDC"
}

// Input is an update of the settings. A nil DisplayName, Scopes or ExpiresOn with ExpiresOnSet false keeps the stored
// value; an empty display name returns to the default.
type Input struct {
	Enabled               bool
	DisplayName           *string
	IssuerURL             string
	ClientID              string
	ClientSecret          keyring.SecretInput
	ClientSecretExpiresOn *time.Time
	ExpiresOnSet          bool
	Scopes                *[]string
	GroupsClaim           string
	GroupMappings         []GroupMapping
	UnmatchedRole         string
	SyncRole              bool
	SkipTOTPWithIDPMFA    bool
	Proxy                 proxyconf.Input
}

// Requester is who asks for a change and how.
type Requester struct {
	Actor     audit.Actor
	Transport audit.Transport
	Address   netip.Addr
}

// Queries are the queries of the package, with the insert of the Audit log.
type Queries interface {
	GetSettings(ctx context.Context, orgID int64) (dbgen.OidcSetting, error)
	LockSettings(ctx context.Context, orgID int64) (int64, error)
	InsertSettings(ctx context.Context, arg dbgen.InsertSettingsParams) (int64, error)
	UpdateSettings(ctx context.Context, arg dbgen.UpdateSettingsParams) (int64, error)
	SetGroupsClaimMissing(ctx context.Context, arg dbgen.SetGroupsClaimMissingParams) error
	LatestKeptAdmin(ctx context.Context, orgID int64) (dbgen.LatestKeptAdminRow, error)
	InsertAuthRequest(ctx context.Context, arg dbgen.InsertAuthRequestParams) (int64, error)
	TakeAuthRequest(ctx context.Context, arg dbgen.TakeAuthRequestParams) (dbgen.TakeAuthRequestRow, error)
	GetIdentityUser(ctx context.Context, arg dbgen.GetIdentityUserParams) (dbgen.GetIdentityUserRow, error)
	LoginTaken(ctx context.Context, arg dbgen.LoginTakenParams) (bool, error)
	CreateIdentityUser(ctx context.Context, arg dbgen.CreateIdentityUserParams) (int64, error)
	LockActiveAdmins(ctx context.Context, orgID int64) ([]int64, error)
	LockUser(ctx context.Context, arg dbgen.LockUserParams) (dbgen.LockUserRow, error)
	SetRole(ctx context.Context, arg dbgen.SetRoleParams) (int64, error)
	EndUserSessions(ctx context.Context, arg dbgen.EndUserSessionsParams) (int64, error)
	RecordContact(ctx context.Context, arg dbgen.RecordContactParams) error
	TakeLinkRequest(ctx context.Context, arg dbgen.TakeLinkRequestParams) (dbgen.TakeLinkRequestRow, error)
	LockLinkUser(ctx context.Context, arg dbgen.LockLinkUserParams) (dbgen.LockLinkUserRow, error)
	LinkIdentity(ctx context.Context, arg dbgen.LinkIdentityParams) (int64, error)
	EndOtherUserSessions(ctx context.Context, arg dbgen.EndOtherUserSessionsParams) (int64, error)
	ContinueAsOIDCSession(ctx context.Context, arg dbgen.ContinueAsOIDCSessionParams) (int64, error)
	StoreOfflineToken(ctx context.Context, arg dbgen.StoreOfflineTokenParams) error
	WipeOfflineToken(ctx context.Context, arg dbgen.WipeOfflineTokenParams) error
	ScheduleCheck(ctx context.Context, arg dbgen.ScheduleCheckParams) error
	DeleteCheck(ctx context.Context, arg dbgen.DeleteCheckParams) error
	GetCheckUser(ctx context.Context, arg dbgen.GetCheckUserParams) (dbgen.GetCheckUserRow, error)
	LockCheckUser(ctx context.Context, arg dbgen.LockCheckUserParams) (dbgen.LockCheckUserRow, error)
	FinishCheck(ctx context.Context, arg dbgen.FinishCheckParams) (int64, error)
	DropCheck(ctx context.Context, arg dbgen.DropCheckParams) (int64, error)
	RefuseUser(ctx context.Context, arg dbgen.RefuseUserParams) error
	CapOIDCSessions(ctx context.Context, arg dbgen.CapOIDCSessionsParams) (int64, error)
	InsertDemoSettings(ctx context.Context, arg dbgen.InsertDemoSettingsParams) (int64, error)
	AllowNetwork(ctx context.Context, arg dbgen.AllowNetworkParams) (int64, error)
	audit.Store
}

// Store runs the queries alone or in one transaction.
type Store interface {
	Queries
	InTx(ctx context.Context, f func(Queries) error) error
}

// NewStore is the Store over the main pool.
func NewStore(pool *pgxpool.Pool) Store {
	return pgStore{pgQueries: pgQueries{Queries: dbgen.New(pool), Store: audit.NewStore(pool)}, pool: pool}
}

type pgQueries struct {
	*dbgen.Queries
	audit.Store
}

type pgStore struct {
	pgQueries
	pool *pgxpool.Pool
}

func (s pgStore) InTx(ctx context.Context, f func(Queries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return f(pgQueries{Queries: dbgen.New(tx), Store: audit.NewStore(tx)})
	})
}

// Sessions opens the sessions of OIDC sign-ins; internal/auth implements it.
type Sessions interface {
	OpenOIDCSession(ctx context.Context, in auth.OIDCSignIn) (auth.Session, error)
}

// Config is what the Service works with.
type Config struct {
	OrgID   int64
	Store   Store
	Keyring *keyring.Keyring
	Audit   *audit.Writer
	// Clocks: the business clock for domain times, the real clock for ID-token times.
	Clocks   clock.Clocks
	Network  Network
	Log      *logging.Logger
	Sessions Sessions
	// PublicURL is MUSTER_PUBLIC_URL, the base of the redirect URIs.
	PublicURL *url.URL
	// RecheckBudget bounds the refresh of a background re-check; zero is RecheckBudget, tests make it shorter.
	RecheckBudget time.Duration
}

// Service holds the OIDC settings and the sign-in flow of the Organization.
type Service struct {
	cfg Config

	mu       sync.Mutex
	provider *Provider
	// providerVersion is the version of the settings the provider was built for.
	providerVersion int64
}

// NewService returns the Service.
func NewService(cfg Config) *Service {
	return &Service{cfg: cfg}
}

// Get reads the settings with their warnings; before an Admin saves them it returns the defaults with OIDC off and
// the version 0.
func (s *Service) Get(ctx context.Context) (Settings, error) {
	st, _, err := s.read(ctx, s.cfg.Store)
	if err != nil {
		return Settings{}, err
	}
	return st, s.warn(ctx, s.cfg.Store, &st)
}

// read reads the settings and the stored row; ok is false before an Admin saved them.
func (s *Service) read(ctx context.Context, q Queries) (Settings, dbgen.OidcSetting, error) {
	row, err := q.GetSettings(ctx, s.cfg.OrgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return defaults(), dbgen.OidcSetting{}, nil
	}
	if err != nil {
		return Settings{}, row, fmt.Errorf("read the OIDC settings: %w", err)
	}
	st, err := settingsOf(row)
	return st, row, err
}

func defaults() Settings {
	return Settings{Scopes: slices.Clone(DefaultScopes), GroupsClaim: DefaultGroupsClaim, GroupMappings: []GroupMapping{},
		UnmatchedRole: UnmatchedNone, SyncRole: true}
}

func settingsOf(row dbgen.OidcSetting) (Settings, error) {
	st := Settings{
		Configured: true, Enabled: row.Enabled, DisplayName: row.DisplayName.String, IssuerURL: row.IssuerUrl,
		ClientID: row.ClientID, ClientSecret: secretOf(row.ClientSecretCiphertext, row.ClientSecretKeyID,
			row.ClientSecretUpdatedAt).Status(),
		Scopes: row.Scopes, GroupsClaim: row.GroupsClaim, UnmatchedRole: row.UnmatchedRole, SyncRole: row.SyncRole,
		SkipTOTPWithIDPMFA: row.SkipTotpWithIdpMfa, ProxyPassword: secretOf(row.ProxyPasswordCiphertext,
			row.ProxyPasswordKeyID, row.ProxyPasswordUpdatedAt).Status(),
		GroupsClaimMissing: row.GroupsClaimMissingSince.Valid, Version: row.Version,
	}
	if row.ClientSecretExpiresOn.Valid {
		d := row.ClientSecretExpiresOn.Time.UTC()
		st.ClientSecretExpiresOn = &d
	}
	t := row.UpdatedAt.UTC()
	st.UpdatedAt = &t
	if st.Scopes == nil {
		st.Scopes = []string{}
	}
	if err := json.Unmarshal(row.GroupMappings, &st.GroupMappings); err != nil {
		return Settings{}, fmt.Errorf("read the group mappings: %w", err)
	}
	if st.GroupMappings == nil {
		st.GroupMappings = []GroupMapping{}
	}
	var err error
	if st.Proxy, err = proxyconf.Parse(row.Proxy); err != nil {
		return Settings{}, err
	}
	return st, nil
}

func secretOf(ct []byte, keyID pgtype.Text, at pgtype.Timestamptz) keyring.StoredSecret {
	s := keyring.StoredSecret{Ciphertext: ct, KeyID: keyID.String}
	if at.Valid {
		t := at.Time.UTC()
		s.UpdatedAt = &t
	}
	return s
}

// warn adds the warnings to st, on the business clock.
func (s *Service) warn(ctx context.Context, q Queries, st *Settings) error {
	st.Warnings = []Warning{}
	if len(st.GroupMappings) == 0 && st.UnmatchedRole == UnmatchedNone {
		st.Warnings = append(st.Warnings, Warning{Kind: WarningNobodyCanSignIn})
	}
	if st.GroupsClaimMissing {
		st.Warnings = append(st.Warnings, Warning{Kind: WarningGroupsClaimMissing})
	}
	if d := st.ClientSecretExpiresOn; d != nil && d.Sub(s.cfg.Clocks.Business.Now()) < SecretExpiryLead {
		st.Warnings = append(st.Warnings, Warning{Kind: WarningSecretExpiring, ExpiresOn: d})
	}
	if !st.Configured {
		return nil
	}
	kept, err := q.LatestKeptAdmin(ctx, s.cfg.OrgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the latest Role sync that kept an Admin: %w", err)
	}
	// The warning lasts while the user is the last active Admin and no later contact with the IdP happened, at which
	// sync applied or the IdP mapped them to Admin again.
	if kept.Role == auth.RoleAdmin && kept.Status == "active" && kept.ActiveAdmins <= 1 &&
		(!kept.OidcLastContactAt.Valid || !kept.OidcLastContactAt.Time.After(kept.At)) {
		st.Warnings = append(st.Warnings, Warning{Kind: WarningLastAdminKept, Role: kept.MappedRole,
			User: &UserRef{PublicID: kept.PublicID, Name: kept.Name, Login: kept.Login}})
	}
	return nil
}

// view is what the Audit log diff of the settings shows; the Secrets only as changed or not.
type view struct {
	Enabled               bool             `json:"enabled"`
	DisplayName           string           `json:"display_name"`
	IssuerURL             string           `json:"issuer_url"`
	ClientID              string           `json:"client_id"`
	ClientSecretExpiresOn *string          `json:"client_secret_expires_on"`
	Scopes                []string         `json:"scopes"`
	GroupsClaim           string           `json:"groups_claim"`
	GroupMappings         []GroupMapping   `json:"group_mappings"`
	UnmatchedRole         string           `json:"unmatched_role"`
	SyncRole              bool             `json:"sync_role"`
	SkipTOTPWithIDPMFA    bool             `json:"skip_totp_with_idp_mfa"`
	Proxy                 proxyconf.Config `json:"proxy"`
}

func viewOf(st Settings) view {
	v := view{Enabled: st.Enabled, DisplayName: st.DisplayName, IssuerURL: st.IssuerURL, ClientID: st.ClientID,
		Scopes: st.Scopes, GroupsClaim: st.GroupsClaim, GroupMappings: st.GroupMappings, UnmatchedRole: st.UnmatchedRole,
		SyncRole: st.SyncRole, SkipTOTPWithIDPMFA: st.SkipTOTPWithIDPMFA, Proxy: st.Proxy}
	if st.ClientSecretExpiresOn != nil {
		d := st.ClientSecretExpiresOn.Format(time.DateOnly)
		v.ClientSecretExpiresOn = &d
	}
	return v
}

// Update replaces the settings at version, the version If-Match names, or at any version when version is nil; the
// first save names version 0. The client secret and the proxy password follow the rule of every Secret field. A
// change is recorded as oidc_settings.updated with its diff, in which the Secrets show only that they changed; an
// update that changes nothing writes nothing.
func (s *Service) Update(ctx context.Context, r Requester, version *int64, in Input) (Settings, error) {
	var out Settings
	err := s.cfg.Store.InTx(ctx, func(q Queries) error {
		current, err := q.LockSettings(ctx, s.cfg.OrgID)
		if errors.Is(err, pgx.ErrNoRows) {
			current = 0
		} else if err != nil {
			return fmt.Errorf("lock the OIDC settings: %w", err)
		}
		if version != nil && *version != current {
			return ErrVersionMismatch
		}
		before, row, err := s.read(ctx, q)
		if err != nil {
			return err
		}
		after, err := apply(before, in)
		if err != nil {
			return err
		}
		now := s.cfg.Clocks.Business.Now()
		secret, secretChanged, err := s.cfg.Keyring.ApplySecret(fieldClientSecret,
			secretOf(row.ClientSecretCiphertext, row.ClientSecretKeyID, row.ClientSecretUpdatedAt), in.ClientSecret, now)
		if err != nil {
			return secretError("/client_secret", err)
		}
		password, passwordChanged, err := s.cfg.Keyring.ApplySecret(fieldProxyPassword,
			secretOf(row.ProxyPasswordCiphertext, row.ProxyPasswordKeyID, row.ProxyPasswordUpdatedAt),
			in.Proxy.Password, now)
		if err != nil {
			return secretError("/proxy/password", err)
		}
		diff := audit.Diff(viewOf(before), viewOf(after))
		if secretChanged {
			diff = append(diff, audit.Change{Pointer: "/client_secret", SecretChanged: true})
		}
		if passwordChanged {
			diff = append(diff, audit.Change{Pointer: "/proxy/password", SecretChanged: true})
		}
		if len(diff) == 0 && before.Configured {
			out = before
			return s.warn(ctx, q, &out)
		}
		if err := s.store(ctx, q, before.Configured, current, after, secret, password, now); err != nil {
			return err
		}
		if out, _, err = s.read(ctx, q); err != nil {
			return err
		}
		if err := s.warn(ctx, q, &out); err != nil {
			return err
		}
		return s.cfg.Audit.Record(ctx, q, audit.Entry{
			OrgID: s.cfg.OrgID, Actor: r.Actor, Transport: r.Transport, Action: ActionSettingsUpdated,
			Resource: audit.Resource{Type: ResourceSettings, Name: out.ButtonName()}, Diff: diff,
			SourceAddress: r.Address,
		})
	})
	if err != nil {
		return Settings{}, err
	}
	return out, nil
}

func secretError(pointer string, err error) error {
	if errors.Is(err, keyring.ErrEmptySecret) {
		return &FieldError{Pointer: pointer, Code: "too_short", Detail: "The secret is empty; omit it to keep it."}
	}
	return err
}

func (s *Service) store(ctx context.Context, q Queries, exists bool, version int64, st Settings, secret,
	password keyring.StoredSecret, now time.Time) error {
	mappings, err := json.Marshal(st.GroupMappings)
	if err != nil {
		return fmt.Errorf("encode the group mappings: %w", err)
	}
	var expires pgtype.Date
	if st.ClientSecretExpiresOn != nil {
		expires = pgtype.Date{Time: *st.ClientSecretExpiresOn, Valid: true}
	}
	display := pgtype.Text{String: st.DisplayName, Valid: st.DisplayName != ""}
	if !exists {
		n, err := q.InsertSettings(ctx, dbgen.InsertSettingsParams{
			OrgID: s.cfg.OrgID, Enabled: st.Enabled, DisplayName: display, IssuerUrl: st.IssuerURL, ClientID: st.ClientID,
			ClientSecretCiphertext: secret.Ciphertext, ClientSecretKeyID: optText(secret.KeyID),
			ClientSecretUpdatedAt: optTime(secret.UpdatedAt), ClientSecretExpiresOn: expires, Scopes: st.Scopes,
			GroupsClaim: st.GroupsClaim, GroupMappings: mappings, UnmatchedRole: st.UnmatchedRole, SyncRole: st.SyncRole,
			SkipTotpWithIdpMfa: st.SkipTOTPWithIDPMFA, Proxy: st.Proxy.JSON(),
			ProxyPasswordCiphertext: password.Ciphertext, ProxyPasswordKeyID: optText(password.KeyID),
			ProxyPasswordUpdatedAt: optTime(password.UpdatedAt), UpdatedAt: now.UTC(),
		})
		if err != nil {
			return fmt.Errorf("store the OIDC settings: %w", err)
		}
		if n == 0 {
			return ErrVersionMismatch
		}
		return nil
	}
	n, err := q.UpdateSettings(ctx, dbgen.UpdateSettingsParams{
		OrgID: s.cfg.OrgID, Version: version, Enabled: st.Enabled, DisplayName: display, IssuerUrl: st.IssuerURL,
		ClientID: st.ClientID, ClientSecretCiphertext: secret.Ciphertext, ClientSecretKeyID: optText(secret.KeyID),
		ClientSecretUpdatedAt: optTime(secret.UpdatedAt), ClientSecretExpiresOn: expires, Scopes: st.Scopes,
		GroupsClaim: st.GroupsClaim, GroupMappings: mappings, UnmatchedRole: st.UnmatchedRole, SyncRole: st.SyncRole,
		SkipTotpWithIdpMfa: st.SkipTOTPWithIDPMFA, Proxy: st.Proxy.JSON(), ProxyPasswordCiphertext: password.Ciphertext,
		ProxyPasswordKeyID: optText(password.KeyID), ProxyPasswordUpdatedAt: optTime(password.UpdatedAt),
		UpdatedAt: now.UTC(),
	})
	if err != nil {
		return fmt.Errorf("update the OIDC settings: %w", err)
	}
	if n == 0 {
		return ErrVersionMismatch
	}
	return nil
}

// apply validates in and returns the settings after it.
func apply(before Settings, in Input) (Settings, error) {
	after := before
	after.Enabled, after.SyncRole, after.SkipTOTPWithIDPMFA = in.Enabled, in.SyncRole, in.SkipTOTPWithIDPMFA
	if in.DisplayName != nil {
		after.DisplayName = strings.TrimSpace(*in.DisplayName)
		if utf8.RuneCountInString(after.DisplayName) > maxDisplayName {
			return Settings{}, &FieldError{Pointer: "/display_name", Code: "too_long",
				Detail: fmt.Sprintf("The display name is longer than %d characters.", maxDisplayName)}
		}
	}
	after.IssuerURL = strings.TrimSpace(in.IssuerURL)
	if u, err := url.Parse(after.IssuerURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") ||
		u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || len(after.IssuerURL) > maxURL {
		return Settings{}, &FieldError{Pointer: "/issuer_url", Code: "invalid_format",
			Detail: "The issuer URL is not an absolute http or https URL without a query."}
	}
	after.ClientID = strings.TrimSpace(in.ClientID)
	if after.ClientID == "" || len(after.ClientID) > maxClaimName {
		return Settings{}, &FieldError{Pointer: "/client_id", Code: "required", Detail: "The client id is empty."}
	}
	if in.ExpiresOnSet {
		after.ClientSecretExpiresOn = nil
		if in.ClientSecretExpiresOn != nil {
			d := time.Date(in.ClientSecretExpiresOn.Year(), in.ClientSecretExpiresOn.Month(),
				in.ClientSecretExpiresOn.Day(), 0, 0, 0, 0, time.UTC)
			after.ClientSecretExpiresOn = &d
		}
	}
	if in.Scopes != nil {
		after.Scopes = []string{}
		for _, sc := range *in.Scopes {
			sc = strings.TrimSpace(sc)
			if sc == "" || strings.IndexFunc(sc, func(r rune) bool { return unicode.IsSpace(r) || r == '"' || r == '\\' }) >= 0 {
				return Settings{}, &FieldError{Pointer: "/scopes", Code: "invalid_format",
					Detail: "A scope is empty or contains a space."}
			}
			if !slices.Contains(after.Scopes, sc) {
				after.Scopes = append(after.Scopes, sc)
			}
		}
	}
	after.GroupsClaim = strings.TrimSpace(in.GroupsClaim)
	if after.GroupsClaim == "" || len(after.GroupsClaim) > maxClaimName {
		return Settings{}, &FieldError{Pointer: "/groups_claim", Code: "required", Detail: "The groups claim is empty."}
	}
	if len(in.GroupMappings) > maxGroups {
		return Settings{}, &FieldError{Pointer: "/group_mappings", Code: "too_long",
			Detail: fmt.Sprintf("There are more than %d group mappings.", maxGroups)}
	}
	after.GroupMappings = make([]GroupMapping, 0, len(in.GroupMappings))
	for i, m := range in.GroupMappings {
		m.Group = strings.TrimSpace(m.Group)
		pointer := fmt.Sprintf("/group_mappings/%d", i)
		switch {
		case m.Group == "" || len(m.Group) > maxClaimName:
			return Settings{}, &FieldError{Pointer: pointer + "/group", Code: "required", Detail: "The group is empty."}
		case !slices.Contains(auth.RoleNames, m.Role):
			return Settings{}, &FieldError{Pointer: pointer + "/role", Code: "invalid_format",
				Detail: "The Role is not admin, responder or viewer."}
		}
		after.GroupMappings = append(after.GroupMappings, m)
	}
	switch in.UnmatchedRole {
	case UnmatchedNone, UnmatchedViewer, UnmatchedResponder:
		after.UnmatchedRole = in.UnmatchedRole
	default:
		return Settings{}, &FieldError{Pointer: "/unmatched_role", Code: "invalid_format",
			Detail: "The Role for unmatched users is not none, viewer or responder."}
	}
	proxy, err := in.Proxy.Apply(before.Proxy)
	if fe, ok := errors.AsType[*proxyconf.FieldError](err); ok {
		return Settings{}, &FieldError{Pointer: "/proxy" + fe.Pointer, Code: fe.Code, Detail: fe.Detail}
	}
	if err != nil {
		return Settings{}, err
	}
	after.Proxy = proxy
	return after, nil
}

// SignInOptions says whether the sign-in page shows the OIDC button, and its provider name.
func (s *Service) SignInOptions(ctx context.Context) (bool, string, error) {
	st, _, err := s.read(ctx, s.cfg.Store)
	if err != nil {
		return false, "", err
	}
	if !st.Configured || !st.Enabled {
		return false, "", nil
	}
	return true, st.ButtonName(), nil
}

// CheckResult is the result of the connection check (C-03.FR-8).
type CheckResult struct {
	OK        bool
	ViaProxy  bool
	Latency   time.Duration
	Discovery Discovery
	// Error is what failed, masked; it names the blocking rule when the outbound address policy refused the call.
	Error    string
	Warnings []Warning
}

// Check fetches discovery with the saved settings through the interactive class and the OIDC proxy, and records
// whether discovery advertises the groups claim.
func (s *Service) Check(ctx context.Context) (CheckResult, error) {
	st, row, err := s.read(ctx, s.cfg.Store)
	if err != nil {
		return CheckResult{}, err
	}
	res := CheckResult{ViaProxy: st.Proxy.Enabled}
	if !st.Configured {
		res.Error = "OIDC is not configured: save the settings first."
		res.Warnings = []Warning{}
		return res, nil
	}
	p, err := s.providerFor(row, st)
	if err != nil {
		res.Error = masked(err)
		return res, s.finishCheck(ctx, &res)
	}
	start := s.cfg.Clocks.Real.Now()
	d, err := FetchDiscovery(ctx, p.Interactive(), st.IssuerURL)
	res.Latency = s.cfg.Clocks.Real.Now().Sub(start)
	if err != nil {
		res.Error = masked(err)
		return res, s.finishCheck(ctx, &res)
	}
	res.OK, res.Discovery = true, d
	if err := s.setGroupsClaimMissing(ctx, !d.AdvertisesClaim(st.GroupsClaim)); err != nil {
		return CheckResult{}, err
	}
	return res, s.finishCheck(ctx, &res)
}

func (s *Service) finishCheck(ctx context.Context, res *CheckResult) error {
	st, err := s.Get(ctx)
	if err != nil {
		return err
	}
	res.Warnings = st.Warnings
	return nil
}

// masked is the text of an error of the back channel for a person: the outbound package already replaced the
// registered secrets, and the step of a back-channel error is left out.
func masked(err error) string {
	if be, ok := errors.AsType[*BackChannelError](err); ok {
		return be.msg
	}
	return err.Error()
}

// setGroupsClaimMissing records that the groups claim was found missing, or clears it.
func (s *Service) setGroupsClaimMissing(ctx context.Context, missing bool) error {
	p := dbgen.SetGroupsClaimMissingParams{OrgID: s.cfg.OrgID}
	if missing {
		p.Since = pgtype.Timestamptz{Time: s.cfg.Clocks.Business.Now().UTC(), Valid: true}
	}
	if err := s.cfg.Store.SetGroupsClaimMissing(ctx, p); err != nil {
		return fmt.Errorf("record the groups claim: %w", err)
	}
	return nil
}

// providerFor returns the back channel of the settings' version, built once per version: its clients carry the
// decrypted client secret and proxy password.
func (s *Service) providerFor(row dbgen.OidcSetting, st Settings) (*Provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.provider != nil && s.providerVersion == st.Version {
		return s.provider, nil
	}
	// A request that read an older version than the cached provider's builds its own and leaves the cache alone.
	keep := s.provider == nil || st.Version > s.providerVersion
	secret, err := s.cfg.Keyring.OpenSecret(fieldClientSecret,
		secretOf(row.ClientSecretCiphertext, row.ClientSecretKeyID, row.ClientSecretUpdatedAt))
	if err != nil {
		return nil, err
	}
	password, err := s.cfg.Keyring.OpenSecret(fieldProxyPassword,
		secretOf(row.ProxyPasswordCiphertext, row.ProxyPasswordKeyID, row.ProxyPasswordUpdatedAt))
	if err != nil {
		return nil, err
	}
	p, err := NewProvider(s.cfg.Network, st.IssuerURL, st.ClientID, secret, st.Proxy.Outbound(password))
	if err != nil {
		return nil, err
	}
	if keep {
		s.provider, s.providerVersion = p, st.Version
	}
	return p, nil
}

// Demo is the demo OIDC configuration of `muster dev`.
type Demo struct {
	IssuerURL    string
	ClientID     string
	ClientSecret logging.Secret
	DisplayName  string
	Mappings     []GroupMapping
	// AllowNetwork is added to the Organization's allowed outbound networks, so that Muster may call the fakes.
	AllowNetwork string
}

// EnsureDemo is the start-up step of `muster dev`: when the Organization has no OIDC settings it stores the demo
// configuration, enabled, and records it in the Audit log as done by the system; it then allows the demo's network.
// It never runs outside development mode.
func (s *Service) EnsureDemo(ctx context.Context, d Demo) error {
	now := s.cfg.Clocks.Business.Now().UTC()
	ct, keyID, err := s.cfg.Keyring.Encrypt(fieldClientSecret, []byte(d.ClientSecret))
	if err != nil {
		return fmt.Errorf("encrypt the demo client secret: %w", err)
	}
	mappings, err := json.Marshal(d.Mappings)
	if err != nil {
		return fmt.Errorf("encode the demo mappings: %w", err)
	}
	return s.cfg.Store.InTx(ctx, func(q Queries) error {
		n, err := q.InsertDemoSettings(ctx, dbgen.InsertDemoSettingsParams{
			OrgID: s.cfg.OrgID, DisplayName: optText(d.DisplayName), IssuerUrl: d.IssuerURL, ClientID: d.ClientID,
			ClientSecretCiphertext: ct, ClientSecretKeyID: optText(keyID), UpdatedAt: optTime(&now),
			Scopes: slices.Clone(DefaultScopes), GroupsClaim: DefaultGroupsClaim, GroupMappings: mappings,
		})
		if err != nil {
			return fmt.Errorf("store the demo OIDC settings: %w", err)
		}
		if _, err := q.AllowNetwork(ctx, dbgen.AllowNetworkParams{OrgID: s.cfg.OrgID, Network: d.AllowNetwork,
			UpdatedAt: now}); err != nil {
			return fmt.Errorf("allow %s for the demo: %w", d.AllowNetwork, err)
		}
		if n == 0 {
			return nil
		}
		return s.cfg.Audit.Record(ctx, q, audit.Entry{
			OrgID: s.cfg.OrgID, Actor: audit.System, Transport: audit.TransportSystem, Action: ActionSettingsUpdated,
			Resource: audit.Resource{Type: ResourceSettings, Name: d.DisplayName},
			Diff:     []audit.Change{{Pointer: "/client_secret", SecretChanged: true}},
			Details:  map[string]any{"development_demo": true, "issuer_url": d.IssuerURL},
		})
	})
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

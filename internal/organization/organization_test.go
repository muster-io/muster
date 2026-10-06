// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package organization

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	auditdb "github.com/muster-io/muster/internal/audit/dbgen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization/dbgen"
	"github.com/muster-io/muster/internal/publicid"
)

// fakeStore keeps the rows in memory; each err field fails its call.
type fakeStore struct {
	org                          *dbgen.CreateOrganizationParams
	policy                       *dbgen.CreateOutboundPolicyParams
	creates, policyCreates       int
	getErr, createErr, policyErr error
	getPolicyErr                 error
	mappingJSON, stylesJSON      []byte
}

func (s *fakeStore) GetOrganization(context.Context) (dbgen.GetOrganizationRow, error) {
	if s.getErr != nil {
		return dbgen.GetOrganizationRow{}, s.getErr
	}
	if s.org == nil {
		return dbgen.GetOrganizationRow{}, pgx.ErrNoRows
	}
	o := s.org
	row := dbgen.GetOrganizationRow{
		ID: 7, PublicID: o.PublicID, Name: o.Name, TimeZone: o.TimeZone, SeverityLabel: o.SeverityLabel,
		SeverityMapping: o.SeverityMapping, SeverityStyles: o.SeverityStyles, CriticalIsUrgent: o.CriticalIsUrgent,
		InstanceLabels: o.InstanceLabels, RetentionStoredSnapshotsDays: o.RetentionStoredSnapshotsDays,
		RetentionAlertDetailsDays: o.RetentionAlertDetailsDays, RetentionAuditLogDays: o.RetentionAuditLogDays,
		RetentionAlertGroupSummariesDays: o.RetentionAlertGroupSummariesDays, TotpRequired: o.TotpRequired,
		OidcTokenGraceSeconds: o.OidcTokenGraceSeconds,
	}
	if s.mappingJSON != nil {
		row.SeverityMapping = s.mappingJSON
	}
	if s.stylesJSON != nil {
		row.SeverityStyles = s.stylesJSON
	}
	return row, nil
}

func (s *fakeStore) CreateOrganization(_ context.Context, arg dbgen.CreateOrganizationParams) (int64, error) {
	if s.createErr != nil {
		return 0, s.createErr
	}
	s.creates++
	s.org = &arg
	return 7, nil
}

func (s *fakeStore) CreateOutboundPolicy(_ context.Context, arg dbgen.CreateOutboundPolicyParams) (int64, error) {
	if s.policyErr != nil {
		return 0, s.policyErr
	}
	if s.policy != nil {
		return 0, nil
	}
	s.policyCreates++
	s.policy = &arg
	return 1, nil
}

func (s *fakeStore) GetOutboundPolicy(_ context.Context, orgID int64) (dbgen.GetOutboundPolicyRow, error) {
	if s.getPolicyErr != nil {
		return dbgen.GetOutboundPolicyRow{}, s.getPolicyErr
	}
	if s.policy == nil || s.policy.OrgID != orgID {
		return dbgen.GetOutboundPolicyRow{}, pgx.ErrNoRows
	}
	return dbgen.GetOutboundPolicyRow{Policy: s.policy.Policy, Allowed: s.policy.Allowed, Denied: s.policy.Denied}, nil
}

var now = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// TestEnsureCreatesTheDefaults: the values of defaults.md, as stored and as read back.
func TestEnsureCreatesTheDefaults(t *testing.T) {
	s := &fakeStore{}
	var log bytes.Buffer
	if err := Ensure(t.Context(), s, logging.New(&log, logging.LevelInfo), now); err != nil {
		t.Fatal(err)
	}
	o := s.org
	if _, err := publicid.Parse(publicid.Organization, o.PublicID); err != nil {
		t.Errorf("public id %q: %v", o.PublicID, err)
	}
	if o.Name != "Muster" || o.TimeZone != "UTC" || o.SeverityLabel != "severity" || !o.CriticalIsUrgent ||
		strings.Join(o.InstanceLabels, ",") != "pod,instance,container,endpoint" ||
		o.RetentionStoredSnapshotsDays != 14 || o.RetentionAlertDetailsDays != 90 ||
		o.RetentionAlertGroupSummariesDays != 730 || o.RetentionAuditLogDays != 365 || o.TotpRequired != "nobody" ||
		o.OidcTokenGraceSeconds != 604800 || !o.CreatedAt.Equal(now) {
		t.Errorf("organization %+v", o)
	}
	if string(o.SeverityMapping) != `[{"value":"critical","level":"critical"},{"value":"warning","level":"warning"},`+
		`{"value":"info","level":"info"},{"value":"none","level":"info"}]` {
		t.Errorf("severity mapping %s", o.SeverityMapping)
	}
	if string(o.SeverityStyles) != `[{"level":"critical","emoji":"🟥","color":"#d32f2f"},`+
		`{"level":"warning","emoji":"🟧","color":"#f57c00"},{"level":"info","emoji":"🟦","color":"#1976d2"}]` {
		t.Errorf("severity styles %s", o.SeverityStyles)
	}
	p := s.policy
	if p.OrgID != 7 || p.Policy != "standard" || p.Allowed == nil || len(p.Allowed) != 0 || p.Denied == nil ||
		len(p.Denied) != 0 || !p.UpdatedAt.Equal(now) {
		t.Errorf("outbound policy %+v", p)
	}
	var line map[string]any
	if err := json.Unmarshal(log.Bytes(), &line); err != nil || line["event"] != "organization_created" ||
		line["organization"] != o.PublicID {
		t.Errorf("log %s", log.String())
	}

	got, err := Current(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	want := Defaults()
	want.ID, want.PublicID = 7, o.PublicID
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Current = %+v\nwant %+v", got, want)
	}
}

func TestEnsureChangesNothingThatExists(t *testing.T) {
	s := &fakeStore{}
	log := logging.New(&bytes.Buffer{}, logging.LevelInfo)
	if err := Ensure(t.Context(), s, log, now); err != nil {
		t.Fatal(err)
	}
	id := s.org.PublicID
	var out bytes.Buffer
	if err := Ensure(t.Context(), s, logging.New(&out, logging.LevelInfo), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s.creates != 1 || s.policyCreates != 1 || s.org.PublicID != id || !s.policy.UpdatedAt.Equal(now) {
		t.Errorf("a second start changed the rows: creates %d, policies %d", s.creates, s.policyCreates)
	}
	if out.Len() != 0 {
		t.Errorf("a second start logged %s", out.String())
	}

	// An Organization whose policy is missing, as after an interrupted first start, gains it.
	s.policy = nil
	if err := Ensure(t.Context(), s, log, now); err != nil || s.policy == nil || s.creates != 1 {
		t.Errorf("Ensure = %v, policy %v, creates %d", err, s.policy, s.creates)
	}
}

func TestEnsureErrors(t *testing.T) {
	boom := errors.New("boom")
	log := logging.New(&bytes.Buffer{}, logging.LevelInfo)
	for want, s := range map[string]*fakeStore{
		"read the organization: boom":              {getErr: boom},
		"create the organization: boom":            {createErr: boom},
		"create the outbound address policy: boom": {policyErr: boom},
	} {
		if err := Ensure(t.Context(), s, log, now); err == nil || err.Error() != want {
			t.Errorf("Ensure = %v, want %q", err, want)
		}
	}
}

func TestCurrentErrors(t *testing.T) {
	boom := errors.New("boom")
	log := logging.New(&bytes.Buffer{}, logging.LevelInfo)
	created := func(f func(*fakeStore)) *fakeStore {
		s := &fakeStore{}
		if err := Ensure(t.Context(), s, log, now); err != nil {
			t.Fatal(err)
		}
		f(s)
		return s
	}
	for want, s := range map[string]*fakeStore{
		"read the organization: boom":            {getErr: boom},
		"read the outbound address policy: boom": created(func(s *fakeStore) { s.getPolicyErr = boom }),
		"read the severity mapping":              created(func(s *fakeStore) { s.mappingJSON = []byte("{") }),
		"read the severity styles":               created(func(s *fakeStore) { s.stylesJSON = []byte("{") }),
	} {
		if _, err := Current(t.Context(), s); err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("Current = %v, want %q", err, want)
		}
	}
	if NewStore(nil) == nil {
		t.Error("NewStore returned nil")
	}
}

// fakeSettings is the organization resource in memory; fail makes the named method fail.
type fakeSettings struct {
	row   dbgen.GetSettingsRow
	audit []auditdb.InsertAuditEntryParams
	hints []db.Hint
	fail  map[string]error
}

func newFakeSettings(t *testing.T) *fakeSettings {
	t.Helper()
	d := Defaults()
	mapping, _ := json.Marshal(d.SeverityMapping)
	styles, _ := json.Marshal(d.SeverityStyles)
	return &fakeSettings{row: dbgen.GetSettingsRow{
		ID: 7, PublicID: "RGAAAAAAAAAAAA", Name: d.Name, TimeZone: d.TimeZone, SeverityLabel: d.SeverityLabel,
		SeverityMapping: mapping, SeverityStyles: styles, CriticalIsUrgent: true, InstanceLabels: d.InstanceLabels,
		RetentionStoredSnapshotsDays: 14, RetentionAlertDetailsDays: 90, RetentionAlertGroupSummariesDays: 730,
		RetentionAuditLogDays: 365, TotpRequired: "nobody", OidcTokenGraceSeconds: 7 * 24 * 3600,
		OutgoingHeartbeatProxy: []byte(`{"enabled": false}`), Version: 1,
	}, fail: map[string]error{}}
}

func (s *fakeSettings) InTx(_ context.Context, f func(SettingsQueries) error) error {
	if err := s.fail["InTx"]; err != nil {
		return err
	}
	before := *s
	err := f(s)
	if err != nil {
		*s = before
	}
	return err
}

func (s *fakeSettings) GetSettings(_ context.Context, orgID int64) (dbgen.GetSettingsRow, error) {
	if err := s.fail["GetSettings"]; err != nil || orgID != 7 {
		return dbgen.GetSettingsRow{}, errors.Join(err, pgx.ErrNoRows)
	}
	return s.row, nil
}

func (s *fakeSettings) LockSettings(context.Context, int64) (int64, error) {
	return s.row.Version, s.fail["LockSettings"]
}

func (s *fakeSettings) SetTOTPPolicy(_ context.Context, arg dbgen.SetTOTPPolicyParams) (int64, error) {
	if err := s.fail["SetTOTPPolicy"]; err != nil {
		return 0, err
	}
	s.row.TotpRequired, s.row.Version = arg.TotpRequired, s.row.Version+1
	return 1, nil
}

func (s *fakeSettings) InsertAuditEntry(_ context.Context, arg auditdb.InsertAuditEntryParams) error {
	if err := s.fail["InsertAuditEntry"]; err != nil {
		return err
	}
	s.audit = append(s.audit, arg)
	return nil
}

func (s *fakeSettings) Notify(_ context.Context, h db.Hint) error {
	if err := s.fail["Notify"]; err != nil {
		return err
	}
	s.hints = append(s.hints, h)
	return nil
}

// inputOf is the update that changes nothing of o.
func inputOf(o Organization) Input {
	return Input{
		Name: o.Name, TimeZone: o.TimeZone, SeverityLabel: o.SeverityLabel, SeverityMapping: o.SeverityMapping,
		SeverityStyles: o.SeverityStyles, CriticalIsUrgent: o.CriticalIsUrgent, InstanceLabels: o.InstanceLabels,
		Retention: o.Retention, TOTPRequired: o.TOTPRequired,
		OIDCTokenGraceSeconds: int64(o.OIDCTokenGrace / time.Second),
		HeartbeatProxy:        ProxyInput{Enabled: o.OutgoingHeartbeat.Proxy.Enabled},
	}
}

var (
	actor = audit.User(1, "SRAAAAAAAAAAAA")
	t0    = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
)

// TestTOTPPolicyCovers is C-03.FR-10: local_users covers accounts with a password, everyone covers all.
func TestTOTPPolicyCovers(t *testing.T) {
	for _, c := range []struct {
		p           TOTPPolicy
		local, want bool
	}{
		{TOTPNobody, true, false}, {TOTPLocalUsers, true, true}, {TOTPLocalUsers, false, false},
		{TOTPEveryone, false, true}, {TOTPEveryone, true, true},
	} {
		if got := c.p.Covers(c.local); got != c.want {
			t.Errorf("%s covers local=%v: %v", c.p, c.local, got)
		}
	}
}

// TestOrganizationGet: the resource with its Secrets only as set or not.
func TestOrganizationGet(t *testing.T) {
	s := newFakeSettings(t)
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	s.row.OutgoingHeartbeatUrlSet = true
	s.row.OutgoingHeartbeatUrlUpdatedAt = pgtype.Timestamptz{Time: at, Valid: true}
	s.row.OutgoingHeartbeatProxy = []byte(`{"enabled": true, "type": "http", "address": "proxy:3128", "username": "u"}`)
	o, err := NewService(7, s, nil, nil).Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	hb := o.OutgoingHeartbeat
	if o.PublicID != "RGAAAAAAAAAAAA" || o.TOTPRequired != TOTPNobody || o.OIDCTokenGrace != 7*24*time.Hour ||
		!hb.URL.Set || !hb.URL.UpdatedAt.Equal(at) || hb.ProxyPassword.Set || hb.ProxyPassword.UpdatedAt != nil ||
		!hb.Proxy.Enabled || hb.Proxy.Type != "http" || *hb.Proxy.Username != "u" || len(o.SeverityMapping) != 4 ||
		o.Version != 1 {
		t.Errorf("Get = %+v", o)
	}
	for field, bad := range map[string]func(*fakeSettings){
		"mapping": func(s *fakeSettings) { s.row.SeverityMapping = []byte("{") },
		"styles":  func(s *fakeSettings) { s.row.SeverityStyles = []byte("{") },
		"proxy":   func(s *fakeSettings) { s.row.OutgoingHeartbeatProxy = []byte("[") },
		"query":   func(s *fakeSettings) { s.fail["GetSettings"] = errors.New("db down") },
	} {
		s := newFakeSettings(t)
		bad(s)
		if _, err := NewService(7, s, nil, nil).Get(t.Context()); err == nil {
			t.Errorf("%s: no error", field)
		}
	}
}

// TestOrganizationUpdate is C-03.FR-20: an Admin changes the TOTP policy with If-Match, recorded as
// organization.updated with its before/after diff and announced with an organization hint; a stale version is
// ErrVersionMismatch; a changed value of any other field is an UnsupportedError naming each such field, and changes
// nothing; an update that changes nothing writes nothing.
func TestOrganizationUpdate(t *testing.T) {
	s := newFakeSettings(t)
	var log bytes.Buffer
	svc := NewService(7, s, audit.NewWriter(logging.New(&log, logging.LevelInfo), clock.NewManual(t0)),
		clock.NewManual(t0))
	o, _ := svc.Get(t.Context())
	in := inputOf(o)
	in.TOTPRequired = TOTPEveryone
	stale := int64(9)
	if _, err := svc.Update(t.Context(), actor, audit.TransportUI, netip.Addr{}, &stale, in); !errors.Is(err,
		ErrVersionMismatch) {
		t.Errorf("a stale version: %v", err)
	}
	v := int64(1)
	got, err := svc.Update(t.Context(), actor, audit.TransportUI, netip.Addr{}, &v, in)
	if err != nil || got.TOTPRequired != TOTPEveryone || got.Version != 2 {
		t.Fatalf("Update = %+v, %v", got, err)
	}
	if len(s.audit) != 1 || s.audit[0].Action != ActionUpdated || s.audit[0].ResourceType.String != ResourceType ||
		string(s.audit[0].Diff) != `[{"pointer":"/totp_required","before":"nobody","after":"everyone"}]` {
		t.Errorf("audit = %+v", s.audit)
	}
	if len(s.hints) != 1 || s.hints[0] != (db.Hint{OrgID: 7, Type: HintOrganization}) {
		t.Errorf("hints = %+v", s.hints)
	}
	if got, err := svc.Update(t.Context(), actor, audit.TransportUI, netip.Addr{}, nil, in); err != nil ||
		got.Version != 2 || len(s.audit) != 1 || len(s.hints) != 1 {
		t.Errorf("an update without a change = %+v, %v, %d entries", got, err, len(s.audit))
	}
	user := "someone"
	tz := "http"
	proxyAddr := "proxy:3128"
	changes := map[string]func(*Input){
		"/name":                                 func(in *Input) { in.Name = "Other" },
		"/time_zone":                            func(in *Input) { in.TimeZone = "Europe/Berlin" },
		"/severity_label":                       func(in *Input) { in.SeverityLabel = "level" },
		"/severity_mapping":                     func(in *Input) { in.SeverityMapping = in.SeverityMapping[:1] },
		"/severity_styles":                      func(in *Input) { in.SeverityStyles = nil },
		"/critical_is_urgent":                   func(in *Input) { in.CriticalIsUrgent = false },
		"/instance_labels":                      func(in *Input) { in.InstanceLabels = []string{"pod"} },
		"/retention/stored_snapshots_days":      func(in *Input) { in.Retention.StoredSnapshotsDays = 1 },
		"/retention/alert_details_days":         func(in *Input) { in.Retention.AlertDetailsDays = 1 },
		"/retention/alert_group_summaries_days": func(in *Input) { in.Retention.AlertGroupSummariesDays = 1 },
		"/retention/audit_log_days":             func(in *Input) { in.Retention.AuditLogDays = 1 },
		"/oidc_token_grace_seconds":             func(in *Input) { in.OIDCTokenGraceSeconds = 60 },
		"/outgoing_heartbeat/url":               func(in *Input) { in.HeartbeatURL = SecretInput{Given: true, Value: "x"} },
		"/outgoing_heartbeat/proxy/enabled":     func(in *Input) { in.HeartbeatProxy.Enabled = true },
		"/outgoing_heartbeat/proxy/type":        func(in *Input) { in.HeartbeatProxy.Type = &tz },
		"/outgoing_heartbeat/proxy/address":     func(in *Input) { in.HeartbeatProxy.Address = &proxyAddr },
		"/outgoing_heartbeat/proxy/username": func(in *Input) {
			in.HeartbeatProxy.UsernameSet, in.HeartbeatProxy.Username = true, &user
		},
		"/outgoing_heartbeat/proxy/password": func(in *Input) {
			in.HeartbeatProxy.Password = SecretInput{Given: true, Value: "pw"}
		},
	}
	for pointer, change := range changes {
		in := inputOf(got)
		in.TOTPRequired = TOTPLocalUsers
		change(&in)
		_, err := svc.Update(t.Context(), actor, audit.TransportUI, netip.Addr{}, nil, in)
		u, ok := errors.AsType[*UnsupportedError](err)
		if !ok || len(u.Pointers) != 1 || u.Pointers[0] != pointer || !strings.Contains(u.Error(), pointer) {
			t.Errorf("%s: %v", pointer, err)
		}
	}
	if s.row.TotpRequired != "everyone" || len(s.audit) != 1 {
		t.Error("a refused update changed the policy or wrote an entry")
	}
	// Clearing Secrets that are not set and keeping the omitted ones change nothing.
	in = inputOf(got)
	in.HeartbeatURL = SecretInput{Given: true, Null: true}
	in.HeartbeatProxy.Password = SecretInput{Given: true, Null: true}
	in.HeartbeatProxy.UsernameSet = true
	if _, err := svc.Update(t.Context(), actor, audit.TransportUI, netip.Addr{}, nil, in); err != nil {
		t.Errorf("clearing unset Secrets: %v", err)
	}
	for _, method := range []string{"InTx", "LockSettings", "GetSettings", "SetTOTPPolicy", "InsertAuditEntry",
		"Notify"} {
		s := newFakeSettings(t)
		s.fail[method] = errors.New("db down")
		svc := NewService(7, s, audit.NewWriter(logging.New(&log, logging.LevelInfo), clock.NewManual(t0)),
			clock.NewManual(t0))
		in := inputOf(Organization{Name: "Muster", TimeZone: "UTC", SeverityLabel: "severity",
			SeverityMapping: Defaults().SeverityMapping, SeverityStyles: Defaults().SeverityStyles,
			CriticalIsUrgent: true, InstanceLabels: Defaults().InstanceLabels,
			Retention: Retention{14, 90, 730, 365}, OIDCTokenGrace: 7 * 24 * time.Hour})
		in.TOTPRequired = TOTPEveryone
		if _, err := svc.Update(t.Context(), actor, audit.TransportUI, netip.Addr{}, nil, in); err == nil {
			t.Errorf("%s: no error", method)
		}
	}
}

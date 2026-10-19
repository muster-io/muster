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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	auditdb "github.com/muster-io/muster/internal/audit/dbgen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/keyring"
	keyringdb "github.com/muster-io/muster/internal/keyring/dbgen"
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
		ID: 7, PublicID: o.PublicID, Name: o.Name, TimeZone: o.TimeZone, WorkdayStart: o.WorkdayStart,
		WeekStart: o.WeekStart, SeverityLabel: o.SeverityLabel, SeverityMapping: o.SeverityMapping,
		SeverityStyles: o.SeverityStyles, CriticalIsUrgent: o.CriticalIsUrgent,
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
	if o.Name != "Muster" || o.TimeZone != "UTC" || o.WorkdayStart != "09:00" || o.WeekStart != "monday" ||
		o.SeverityLabel != "severity" || !o.CriticalIsUrgent ||
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

// fakeSettings is the organization resource and the outbound address policy in memory; fail makes the named method
// fail.
type fakeSettings struct {
	row       dbgen.GetSettingsRow
	secrets   dbgen.GetOutgoingHeartbeatSecretsRow
	updates   []dbgen.UpdateSettingsParams
	audit     []auditdb.InsertAuditEntryParams
	hints     []db.Hint
	policy    dbgen.GetOutboundPolicySettingsRow
	policyUps []dbgen.UpdateOutboundPolicyParams
	notified  []int64
	values    []dbgen.ListSeverityValuesRow
	valueArgs []dbgen.ListSeverityValuesParams
	fail      map[string]error
}

func newFakeSettings(t *testing.T) *fakeSettings {
	t.Helper()
	d := Defaults()
	mapping, _ := json.Marshal(d.SeverityMapping)
	styles, _ := json.Marshal(d.SeverityStyles)
	return &fakeSettings{row: dbgen.GetSettingsRow{
		ID: 7, PublicID: "RGAAAAAAAAAAAA", Name: d.Name, TimeZone: d.TimeZone, WorkdayStart: d.WorkdayStart,
		WeekStart: d.WeekStart, SeverityLabel: d.SeverityLabel, SeverityMapping: mapping, SeverityStyles: styles,
		CriticalIsUrgent: true, InstanceLabels: d.InstanceLabels,
		RetentionStoredSnapshotsDays: 14, RetentionAlertDetailsDays: 90, RetentionAlertGroupSummariesDays: 730,
		RetentionAuditLogDays: 365, TotpRequired: "nobody", OidcTokenGraceSeconds: 7 * 24 * 3600,
		OutgoingHeartbeatProxy: []byte(`{"enabled": false}`), Version: 1,
	}, policy: dbgen.GetOutboundPolicySettingsRow{Policy: "standard", Allowed: []string{"127.0.0.0/8"},
		Denied: []string{}, Version: 1}, fail: map[string]error{}}
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

func (s *fakeSettings) GetOutgoingHeartbeatSecrets(context.Context, int64) (dbgen.GetOutgoingHeartbeatSecretsRow,
	error) {
	return s.secrets, s.fail["GetOutgoingHeartbeatSecrets"]
}

func (s *fakeSettings) UpdateSettings(_ context.Context, arg dbgen.UpdateSettingsParams) (int64, error) {
	if err := s.fail["UpdateSettings"]; err != nil {
		return 0, err
	}
	s.updates = append(s.updates, arg)
	s.row.Name, s.row.TimeZone, s.row.SeverityLabel = arg.Name, arg.TimeZone, arg.SeverityLabel
	s.row.WorkdayStart, s.row.WeekStart = arg.WorkdayStart, arg.WeekStart
	s.row.SeverityMapping, s.row.SeverityStyles = arg.SeverityMapping, arg.SeverityStyles
	s.row.CriticalIsUrgent, s.row.InstanceLabels = arg.CriticalIsUrgent, arg.InstanceLabels
	s.row.RetentionStoredSnapshotsDays = arg.RetentionStoredSnapshotsDays
	s.row.RetentionAlertDetailsDays = arg.RetentionAlertDetailsDays
	s.row.RetentionAlertGroupSummariesDays = arg.RetentionAlertGroupSummariesDays
	s.row.RetentionAuditLogDays = arg.RetentionAuditLogDays
	s.row.OidcTokenGraceSeconds = arg.OidcTokenGraceSeconds
	s.row.TotpRequired, s.row.Version = arg.TotpRequired, s.row.Version+1
	s.row.OutgoingHeartbeatProxy = arg.Proxy
	s.row.OutgoingHeartbeatUrlSet, s.row.OutgoingHeartbeatUrlUpdatedAt = arg.UrlCiphertext != nil, arg.UrlUpdatedAt
	s.row.OutgoingHeartbeatProxyPasswordSet = arg.ProxyPasswordCiphertext != nil
	s.row.OutgoingHeartbeatProxyPasswordUpdatedAt = arg.ProxyPasswordUpdatedAt
	s.secrets = dbgen.GetOutgoingHeartbeatSecretsRow{
		OutgoingHeartbeatUrlCiphertext: arg.UrlCiphertext, OutgoingHeartbeatUrlKeyID: arg.UrlKeyID,
		OutgoingHeartbeatUrlUpdatedAt:            arg.UrlUpdatedAt,
		OutgoingHeartbeatProxyPasswordCiphertext: arg.ProxyPasswordCiphertext,
		OutgoingHeartbeatProxyPasswordKeyID:      arg.ProxyPasswordKeyID,
		OutgoingHeartbeatProxyPasswordUpdatedAt:  arg.ProxyPasswordUpdatedAt,
	}
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

func (s *fakeSettings) GetOutboundPolicySettings(_ context.Context, orgID int64) (dbgen.GetOutboundPolicySettingsRow,
	error) {
	if err := s.fail["GetOutboundPolicySettings"]; err != nil || orgID != 7 {
		return dbgen.GetOutboundPolicySettingsRow{}, errors.Join(err, pgx.ErrNoRows)
	}
	return s.policy, nil
}

func (s *fakeSettings) LockOutboundPolicy(context.Context, int64) (int64, error) {
	return s.policy.Version, s.fail["LockOutboundPolicy"]
}

func (s *fakeSettings) UpdateOutboundPolicy(_ context.Context, arg dbgen.UpdateOutboundPolicyParams) (int64, error) {
	if err := s.fail["UpdateOutboundPolicy"]; err != nil {
		return 0, err
	}
	s.policyUps = append(s.policyUps, arg)
	s.policy = dbgen.GetOutboundPolicySettingsRow{Policy: arg.Policy, Allowed: arg.Allowed, Denied: arg.Denied,
		Version: s.policy.Version + 1}
	return 1, nil
}

func (s *fakeSettings) ListSeverityValues(_ context.Context, arg dbgen.ListSeverityValuesParams) (
	[]dbgen.ListSeverityValuesRow, error) {
	s.valueArgs = append(s.valueArgs, arg)
	return s.values, s.fail["ListSeverityValues"]
}

func (s *fakeSettings) NotifyOutboundPolicy(_ context.Context, orgID int64) error {
	if err := s.fail["NotifyOutboundPolicy"]; err != nil {
		return err
	}
	s.notified = append(s.notified, orgID)
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
	if o.PublicID != "RGAAAAAAAAAAAA" || o.WorkdayStart != "09:00" || o.WeekStart != "monday" ||
		o.TOTPRequired != TOTPNobody || o.OIDCTokenGrace != 7*24*time.Hour ||
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

// TestOrganizationUpdate is C-20.FR-1, FR-8 and C-04.FR-8: an Admin changes each setting of the Organization with
// If-Match; the change is written, moves the version, is recorded as organization.updated with the diff at the
// field's pointer, announced with one organization hint and logged as organization_updated. A stale version is
// ErrVersionMismatch and changes nothing; an update that changes nothing writes nothing.
func TestOrganizationUpdate(t *testing.T) {
	s := newFakeSettings(t)
	var log bytes.Buffer
	logger := logging.New(&log, logging.LevelInfo)
	svc := NewService(7, s, audit.NewWriter(logging.New(&bytes.Buffer{}, logging.LevelInfo), clock.NewManual(t0)),
		clock.NewManual(t0)).WithLog(logger)
	o, _ := svc.Get(t.Context())
	in := inputOf(o)
	in.TOTPRequired = TOTPEveryone
	stale := int64(9)
	if _, err := svc.Update(t.Context(), actor, audit.TransportUI, netip.Addr{}, &stale, in); !errors.Is(err,
		ErrVersionMismatch) || len(s.updates) != 0 {
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
	if !strings.Contains(log.String(), `"event":"organization_updated"`) ||
		!strings.Contains(log.String(), `"fields":"/totp_required"`) {
		t.Errorf("log %s", log.String())
	}
	log.Reset()
	if got, err := svc.Update(t.Context(), actor, audit.TransportUI, netip.Addr{}, nil, in); err != nil ||
		got.Version != 2 || len(s.audit) != 1 || len(s.hints) != 1 || log.Len() != 0 {
		t.Errorf("an update without a change = %+v, %v, %d entries, log %s", got, err, len(s.audit), log.String())
	}
	changes := []struct {
		pointer string
		change  func(*Input)
		check   func(Organization) bool
	}{
		{"/name", func(in *Input) { in.Name = " Ops " }, func(o Organization) bool { return o.Name == "Ops" }},
		{"/time_zone", func(in *Input) { in.TimeZone = "Europe/Berlin" },
			func(o Organization) bool { return o.TimeZone == "Europe/Berlin" }},
		{"/workday_start", func(in *Input) { in.WorkdayStart = new("23:59") },
			func(o Organization) bool { return o.WorkdayStart == "23:59" }},
		{"/week_start", func(in *Input) { in.WeekStart = new("saturday") },
			func(o Organization) bool { return o.WeekStart == "saturday" }},
		{"/severity_label", func(in *Input) { in.SeverityLabel = "level" },
			func(o Organization) bool { return o.SeverityLabel == "level" }},
		{"/severity_mapping", func(in *Input) {
			in.SeverityMapping = append(slices.Clone(in.SeverityMapping), SeverityMapping{"P1", SeverityCritical})
		}, func(o Organization) bool { return len(o.SeverityMapping) == 5 && o.SeverityMapping[4].Value == "P1" }},
		{"/severity_styles", func(in *Input) {
			in.SeverityStyles = slices.Clone(in.SeverityStyles)
			in.SeverityStyles[1].Color = "#000000"
		}, func(o Organization) bool { return o.SeverityStyles[1].Color == "#000000" }},
		{"/critical_is_urgent", func(in *Input) { in.CriticalIsUrgent = false },
			func(o Organization) bool { return !o.CriticalIsUrgent }},
		{"/instance_labels", func(in *Input) { in.InstanceLabels = []string{"node"} },
			func(o Organization) bool { return slices.Equal(o.InstanceLabels, []string{"node"}) }},
		{"/retention/stored_snapshots_days", func(in *Input) { in.Retention.StoredSnapshotsDays = 1 },
			func(o Organization) bool { return o.Retention.StoredSnapshotsDays == 1 }},
		{"/retention/alert_details_days", func(in *Input) { in.Retention.AlertDetailsDays = 30 },
			func(o Organization) bool { return o.Retention.AlertDetailsDays == 30 }},
		{"/retention/alert_group_summaries_days", func(in *Input) { in.Retention.AlertGroupSummariesDays = 400 },
			func(o Organization) bool { return o.Retention.AlertGroupSummariesDays == 400 }},
		{"/retention/audit_log_days", func(in *Input) { in.Retention.AuditLogDays = 36500 },
			func(o Organization) bool { return o.Retention.AuditLogDays == 36500 }},
		{"/oidc_token_grace_seconds", func(in *Input) { in.OIDCTokenGraceSeconds = 3600 },
			func(o Organization) bool { return o.OIDCTokenGrace == time.Hour }},
		{"/totp_required", func(in *Input) { in.TOTPRequired = TOTPLocalUsers },
			func(o Organization) bool { return o.TOTPRequired == TOTPLocalUsers }},
	}
	for _, c := range changes {
		entries, hints, version := len(s.audit), len(s.hints), got.Version
		in := inputOf(got)
		c.change(&in)
		got, err = svc.Update(t.Context(), actor, audit.TransportUI, netip.Addr{}, &version, in)
		if err != nil || !c.check(got) || got.Version != version+1 {
			t.Fatalf("%s: %+v, %v", c.pointer, got, err)
		}
		var diff []audit.Change
		if err := json.Unmarshal(s.audit[len(s.audit)-1].Diff, &diff); err != nil || len(s.audit) != entries+1 ||
			len(diff) != 1 || diff[0].Pointer != c.pointer || len(s.hints) != hints+1 {
			t.Errorf("%s: diff %s, %d entries, %d hints", c.pointer, s.audit[len(s.audit)-1].Diff, len(s.audit),
				len(s.hints))
		}
		// The change is stored: a new read shows it.
		if read, err := svc.Get(t.Context()); err != nil || !c.check(read) {
			t.Errorf("%s: read back %+v, %v", c.pointer, read, err)
		}
	}
	// Clearing Secrets that are not set and keeping the omitted ones change nothing.
	in = inputOf(got)
	in.HeartbeatURL = SecretInput{Given: true, Null: true}
	in.HeartbeatProxy.Password = SecretInput{Given: true, Null: true}
	in.HeartbeatProxy.UsernameSet = true
	if _, err := svc.Update(t.Context(), actor, audit.TransportUI, netip.Addr{}, nil, in); err != nil {
		t.Errorf("clearing unset Secrets: %v", err)
	}
	for _, method := range []string{"InTx", "LockSettings", "GetSettings", "GetOutgoingHeartbeatSecrets",
		"UpdateSettings", "InsertAuditEntry", "Notify"} {
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

// TestOrganizationUpdateValidation: each field that is not valid is refused at its full JSON pointer with its code,
// and nothing is written, recorded or announced.
func TestOrganizationUpdateValidation(t *testing.T) {
	long := strings.Repeat("x", 201)
	for _, c := range []struct {
		pointer, code string
		change        func(*Input)
	}{
		{"/name", CodeTooShort, func(in *Input) { in.Name = "  " }},
		{"/name", CodeTooLong, func(in *Input) { in.Name = long }},
		{"/name", CodeInvalidFormat, func(in *Input) { in.Name = "a\nb" }},
		{"/time_zone", CodeInvalidFormat, func(in *Input) { in.TimeZone = "Mars/Olympus" }},
		{"/time_zone", CodeInvalidFormat, func(in *Input) { in.TimeZone = "Local" }},
		{"/workday_start", CodeInvalidFormat, func(in *Input) { in.WorkdayStart = new("24:00") }},
		{"/workday_start", CodeInvalidFormat, func(in *Input) { in.WorkdayStart = new("9:00") }},
		{"/workday_start", CodeInvalidFormat, func(in *Input) { in.WorkdayStart = new("09:60") }},
		{"/workday_start", CodeInvalidFormat, func(in *Input) { in.WorkdayStart = new("") }},
		{"/week_start", CodeInvalidFormat, func(in *Input) { in.WeekStart = new("weekend") }},
		{"/week_start", CodeInvalidFormat, func(in *Input) { in.WeekStart = new("Monday") }},
		{"/severity_label", CodeInvalidFormat, func(in *Input) { in.SeverityLabel = "2severity" }},
		{"/severity_mapping/4/value", CodeTooShort, func(in *Input) {
			in.SeverityMapping = append(in.SeverityMapping, SeverityMapping{"", SeverityInfo})
		}},
		{"/severity_mapping/4/value", CodeDuplicate, func(in *Input) {
			in.SeverityMapping = append(in.SeverityMapping, SeverityMapping{"warning", SeverityCritical})
		}},
		{"/severity_mapping/4/level", CodeInvalidFormat, func(in *Input) {
			in.SeverityMapping = append(in.SeverityMapping, SeverityMapping{"P1", "urgent"})
		}},
		{"/severity_styles/2/level", CodeInvalidFormat, func(in *Input) { in.SeverityStyles[2].Level = "debug" }},
		{"/severity_styles/2/level", CodeDuplicate, func(in *Input) { in.SeverityStyles[2].Level = SeverityWarning }},
		{"/severity_styles/1/emoji", CodeTooShort, func(in *Input) { in.SeverityStyles[1].Emoji = " " }},
		{"/severity_styles/1/emoji", CodeTooLong, func(in *Input) { in.SeverityStyles[1].Emoji = long }},
		{"/severity_styles/1/color", CodeInvalidFormat, func(in *Input) { in.SeverityStyles[1].Color = "red" }},
		{"/severity_styles", CodeRequired, func(in *Input) { in.SeverityStyles = in.SeverityStyles[:2] }},
		{"/instance_labels/1", CodeInvalidFormat, func(in *Input) { in.InstanceLabels = []string{"pod", "a-b"} }},
		{"/instance_labels/1", CodeDuplicate, func(in *Input) { in.InstanceLabels = []string{"pod", "pod"} }},
		{"/retention/stored_snapshots_days", CodeOutOfRange, func(in *Input) { in.Retention.StoredSnapshotsDays = 0 }},
		{"/retention/alert_details_days", CodeOutOfRange, func(in *Input) { in.Retention.AlertDetailsDays = -1 }},
		{"/retention/alert_group_summaries_days", CodeOutOfRange,
			func(in *Input) { in.Retention.AlertGroupSummariesDays = MaxRetentionDays + 1 }},
		{"/retention/audit_log_days", CodeOutOfRange, func(in *Input) { in.Retention.AuditLogDays = 9999999 }},
		{"/totp_required", CodeInvalidFormat, func(in *Input) { in.TOTPRequired = "sometimes" }},
		{"/oidc_token_grace_seconds", CodeOutOfRange, func(in *Input) { in.OIDCTokenGraceSeconds = 0 }},
		{"/oidc_token_grace_seconds", CodeOutOfRange,
			func(in *Input) { in.OIDCTokenGraceSeconds = MaxOIDCTokenGraceSeconds + 1 }},
	} {
		s := newFakeSettings(t)
		svc := NewService(7, s, audit.NewWriter(logging.New(&bytes.Buffer{}, logging.LevelInfo),
			clock.NewManual(t0)), clock.NewManual(t0))
		o, _ := svc.Get(t.Context())
		in := inputOf(o)
		in.SeverityMapping, in.SeverityStyles = slices.Clone(in.SeverityMapping), slices.Clone(in.SeverityStyles)
		c.change(&in)
		_, err := svc.Update(t.Context(), actor, audit.TransportUI, netip.Addr{}, nil, in)
		if f, ok := errors.AsType[*FieldError](err); !ok || f.Pointer != c.pointer || f.Code != c.code ||
			f.Detail == "" {
			t.Errorf("%s %s: %v", c.pointer, c.code, err)
		}
		if len(s.updates) != 0 || len(s.audit) != 0 || len(s.hints) != 0 {
			t.Errorf("%s: a refused update was written", c.pointer)
		}
	}
	// The limits themselves are accepted, and an empty mapping counts every value as warning.
	s := newFakeSettings(t)
	svc := NewService(7, s, audit.NewWriter(logging.New(&bytes.Buffer{}, logging.LevelInfo), clock.NewManual(t0)),
		clock.NewManual(t0))
	o, _ := svc.Get(t.Context())
	in := inputOf(o)
	in.Name, in.SeverityMapping, in.InstanceLabels = strings.Repeat("я", 200), nil, nil
	in.Retention = Retention{MaxRetentionDays, 1, 1, 1}
	in.OIDCTokenGraceSeconds = MaxOIDCTokenGraceSeconds
	got, err := svc.Update(t.Context(), actor, audit.TransportUI, netip.Addr{}, nil, in)
	if err != nil || got.SeverityMapping == nil || len(got.SeverityMapping) != 0 || got.InstanceLabels == nil ||
		string(s.updates[0].SeverityMapping) != "[]" {
		t.Errorf("the limits: %+v, %v", got, err)
	}
}

// TestRetentionOrder is C-20.AC-6: summaries kept shorter than details are refused with retention_order at
// /retention/alert_group_summaries_days and change nothing; equal periods are accepted.
func TestRetentionOrder(t *testing.T) {
	s := newFakeSettings(t)
	svc := NewService(7, s, audit.NewWriter(logging.New(&bytes.Buffer{}, logging.LevelInfo), clock.NewManual(t0)),
		clock.NewManual(t0))
	o, _ := svc.Get(t.Context())
	in := inputOf(o)
	in.Retention.AlertGroupSummariesDays, in.Retention.AlertDetailsDays = 30, 90
	_, err := svc.Update(t.Context(), actor, audit.TransportUI, netip.Addr{}, nil, in)
	if f, ok := errors.AsType[*FieldError](err); !ok || f.Pointer != "/retention/alert_group_summaries_days" ||
		f.Code != CodeRetentionOrder || !strings.Contains(f.Detail, "(90 days)") {
		t.Errorf("summaries below details: %v", err)
	}
	if len(s.updates) != 0 || s.row.RetentionAlertGroupSummariesDays != 730 {
		t.Error("a refused order changed the row")
	}
	in.Retention.AlertGroupSummariesDays = 90
	got, err := svc.Update(t.Context(), actor, audit.TransportUI, netip.Addr{}, nil, in)
	if err != nil || got.Retention.AlertGroupSummariesDays != 90 || got.Retention.AlertDetailsDays != 90 {
		t.Fatalf("equal periods: %+v, %v", got.Retention, err)
	}
	if string(s.audit[0].Diff) != `[{"pointer":"/retention/alert_group_summaries_days","before":730,"after":90}]` {
		t.Errorf("diff %s", s.audit[0].Diff)
	}
}

// TestSnoozePresetSettings is C-20.FR-9 and C-20.AC-7 on the service: the read carries the start of the working day
// and the first day of the week; 24:00, 9:00 and weekend are refused with invalid_format at the field and change
// nothing; 08:30 with sunday is stored, recorded as organization.updated with both fields in its diff and announced;
// an update that omits both keeps them, also when it changes another field.
func TestSnoozePresetSettings(t *testing.T) {
	s := newFakeSettings(t)
	svc := NewService(7, s, audit.NewWriter(logging.New(&bytes.Buffer{}, logging.LevelInfo), clock.NewManual(t0)),
		clock.NewManual(t0))
	o, err := svc.Get(t.Context())
	if err != nil || o.WorkdayStart != "09:00" || o.WeekStart != "monday" {
		t.Fatalf("Get = %+v, %v", o, err)
	}
	for _, c := range []struct {
		pointer string
		change  func(*Input)
	}{
		{"/workday_start", func(in *Input) { in.WorkdayStart = new("24:00") }},
		{"/workday_start", func(in *Input) { in.WorkdayStart = new("9:00") }},
		{"/week_start", func(in *Input) { in.WeekStart = new("weekend") }},
	} {
		in := inputOf(o)
		in.WorkdayStart, in.WeekStart = new("08:30"), new("sunday")
		c.change(&in)
		_, err := svc.Update(t.Context(), actor, audit.TransportAPI, netip.Addr{}, nil, in)
		if f, ok := errors.AsType[*FieldError](err); !ok || f.Pointer != c.pointer || f.Code != CodeInvalidFormat {
			t.Errorf("%s: %v", c.pointer, err)
		}
		if len(s.updates) != 0 || len(s.audit) != 0 || len(s.hints) != 0 || s.row.WorkdayStart != "09:00" ||
			s.row.WeekStart != "monday" {
			t.Errorf("%s: a refused update changed the organization", c.pointer)
		}
	}

	in := inputOf(o)
	in.WorkdayStart, in.WeekStart = new("08:30"), new("sunday")
	got, err := svc.Update(t.Context(), actor, audit.TransportAPI, netip.Addr{}, nil, in)
	if err != nil || got.WorkdayStart != "08:30" || got.WeekStart != "sunday" || len(s.updates) != 1 ||
		s.updates[0].WorkdayStart != "08:30" || s.updates[0].WeekStart != "sunday" {
		t.Fatalf("08:30 with sunday = %+v, %v", got, err)
	}
	if len(s.audit) != 1 || s.audit[0].Action != ActionUpdated || string(s.audit[0].Diff) !=
		`[{"pointer":"/workday_start","before":"09:00","after":"08:30"},`+
			`{"pointer":"/week_start","before":"monday","after":"sunday"}]` {
		t.Errorf("audit = %+v", s.audit)
	}
	if len(s.hints) != 1 || s.hints[0] != (db.Hint{OrgID: 7, Type: HintOrganization}) {
		t.Errorf("hints = %+v", s.hints)
	}

	in = inputOf(got)
	if in.WorkdayStart != nil || in.WeekStart != nil {
		t.Fatalf("inputOf sets the settings: %+v", in)
	}
	if kept, err := svc.Update(t.Context(), actor, audit.TransportAPI, netip.Addr{}, nil, in); err != nil ||
		kept.WorkdayStart != "08:30" || kept.WeekStart != "sunday" || len(s.updates) != 1 || len(s.audit) != 1 {
		t.Errorf("an update that omits both = %+v, %v, %d writes", kept, err, len(s.updates))
	}
	in.Name = "Ops"
	kept, err := svc.Update(t.Context(), actor, audit.TransportAPI, netip.Addr{}, nil, in)
	if err != nil || kept.WorkdayStart != "08:30" || kept.WeekStart != "sunday" || len(s.updates) != 2 ||
		s.updates[1].WorkdayStart != "08:30" || s.updates[1].WeekStart != "sunday" ||
		string(s.audit[1].Diff) != `[{"pointer":"/name","before":"Muster","after":"Ops"}]` {
		t.Errorf("another change that omits both = %+v, %v, diff %s", kept, err, s.audit[len(s.audit)-1].Diff)
	}
	if read, err := svc.Get(t.Context()); err != nil || read.WorkdayStart != "08:30" || read.WeekStart != "sunday" {
		t.Errorf("read back %+v, %v", read, err)
	}
}

// TestValidTimeZone: the rule the Organization and User profiles share.
func TestValidTimeZone(t *testing.T) {
	for name, want := range map[string]bool{
		"UTC": true, "Europe/Berlin": true, "America/Argentina/Buenos_Aires": true, "": false, "Local": false,
		"Mars/Olympus": false, "../../etc/passwd": false, strings.Repeat("A", 65): false,
	} {
		if got := ValidTimeZone(name); got != want {
			t.Errorf("ValidTimeZone(%q) = %v", name, got)
		}
	}
}

// TestSeverityValues is C-20.AC-4 on the service: the values of the current severity label within the 14 days of the
// business clock, each with its count and the level of the current mapping; an unmapped value is warning, not mapped.
func TestSeverityValues(t *testing.T) {
	s := newFakeSettings(t)
	s.values = []dbgen.ListSeverityValuesRow{{Value: "P5", Alerts: 2}, {Value: "critical", Alerts: 1}}
	svc := NewService(7, s, nil, clock.NewManual(t0))
	got, err := svc.SeverityValues(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := SeverityValues{WindowDays: 14, Items: []SeverityValue{
		{Value: "P5", Count: 2, Level: SeverityWarning}, {Value: "critical", Count: 1, Level: SeverityCritical,
			Mapped: true},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SeverityValues = %+v", got)
	}
	if a := s.valueArgs[0]; a.OrgID != 7 || a.Label != "severity" || !a.Since.Equal(t0.Add(-14*24*time.Hour)) ||
		a.MaxValues != maxSeverityValues {
		t.Errorf("query %+v", a)
	}
	s.values = nil
	if got, err := svc.SeverityValues(t.Context()); err != nil || got.Items == nil || len(got.Items) != 0 {
		t.Errorf("no values = %+v, %v", got, err)
	}
	s.fail["ListSeverityValues"] = errors.New("db down")
	if _, err := svc.SeverityValues(t.Context()); err == nil {
		t.Error("a failed query: no error")
	}
	s.fail["GetSettings"] = errors.New("db down")
	if _, err := svc.SeverityValues(t.Context()); err == nil {
		t.Error("a failed read: no error")
	}
}

type keyState struct {
	keyring.Store
	row *keyringdb.GetKeyringStateRow
}

func (s *keyState) GetKeyringState(context.Context) (keyringdb.GetKeyringStateRow, error) {
	if s.row == nil {
		return keyringdb.GetKeyringStateRow{}, pgx.ErrNoRows
	}
	return *s.row, nil
}

func (s *keyState) CreateKeyringState(_ context.Context, p keyringdb.CreateKeyringStateParams) (int64, error) {
	s.row = &keyringdb.GetKeyringStateRow{ActiveKeyID: p.ActiveKeyID, CanaryKeyID: p.ActiveKeyID,
		CanaryCiphertext: p.CanaryCiphertext}
	return 1, nil
}

// activeKeyring is a Keyring of one key, active.
func activeKeyring(t *testing.T) *keyring.Keyring {
	t.Helper()
	k, err := keyring.New([][]byte{bytes.Repeat([]byte{'o'}, keyring.KeySize)}, false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := k.Establish(t.Context(), &keyState{}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Open(t.Context(), logging.New(&bytes.Buffer{}, logging.LevelInfo), st); err != nil {
		t.Fatal(err)
	}
	return k
}

func heartbeatService(t *testing.T) (*Service, *fakeSettings, *keyring.Keyring) {
	t.Helper()
	s := newFakeSettings(t)
	k := activeKeyring(t)
	svc := NewService(7, s, audit.NewWriter(logging.New(&bytes.Buffer{}, logging.LevelInfo), clock.NewManual(t0)),
		clock.NewManual(t0)).WithKeyring(k)
	return svc, s, k
}

const hookURL = "https://hc.example.org/ping/0f6c2a1e-secret-token?k=v"

// TestUpdateHeartbeatURLEncrypted covers C-19.FR-7 on the setting: the URL is stored encrypted for its field, read
// back only as set, and the Audit log diff says only that it changed; an equal value again still changes it.
func TestUpdateHeartbeatURLEncrypted(t *testing.T) {
	svc, s, k := heartbeatService(t)
	o, _ := svc.Get(t.Context())
	in := inputOf(o)
	in.HeartbeatURL = keyring.Replace(hookURL)
	got, err := svc.Update(t.Context(), actor, audit.TransportAPI, netip.Addr{}, nil, in)
	if err != nil {
		t.Fatal(err)
	}
	if !got.OutgoingHeartbeat.URL.Set || !got.OutgoingHeartbeat.URL.UpdatedAt.Equal(t0) || got.Version != 2 {
		t.Errorf("Update = %+v", got.OutgoingHeartbeat)
	}
	u := s.updates[0]
	if !u.UrlChanged || bytes.Contains(u.UrlCiphertext, []byte("secret-token")) || !u.UrlKeyID.Valid {
		t.Errorf("update %+v", u)
	}
	plain, err := k.OpenSecret(FieldHeartbeatURL, secretOf(u.UrlCiphertext, u.UrlKeyID, u.UrlUpdatedAt))
	if err != nil || string(plain) != hookURL {
		t.Errorf("OpenSecret = %v", err)
	}
	if _, err := k.OpenSecret(FieldHeartbeatProxyPassword, secretOf(u.UrlCiphertext, u.UrlKeyID,
		u.UrlUpdatedAt)); err == nil {
		t.Error("the ciphertext opens for another field")
	}
	if len(s.audit) != 1 || string(s.audit[0].Diff) != `[{"pointer":"/outgoing_heartbeat/url","secret_changed":true}]` {
		t.Errorf("audit %s", s.audit[0].Diff)
	}
	if !slices.Equal(s.hints, []db.Hint{{OrgID: 7, Type: HintOrganization}, {OrgID: 7, Type: HintSystemStatus}}) {
		t.Errorf("hints %v", s.hints)
	}
	in = inputOf(got)
	in.HeartbeatURL = keyring.Replace(hookURL)
	if _, err := svc.Update(t.Context(), actor, audit.TransportAPI, netip.Addr{}, nil, in); err != nil ||
		len(s.audit) != 2 {
		t.Errorf("the same value again: %v, %d entries", err, len(s.audit))
	}
}

// TestUpdateHeartbeatURLClearResetsResult: clearing the URL clears it and its last result, is recorded and sends the
// system-status hint; a change that keeps the URL sends none.
func TestUpdateHeartbeatURLClearResetsResult(t *testing.T) {
	svc, s, _ := heartbeatService(t)
	o, _ := svc.Get(t.Context())
	in := inputOf(o)
	in.HeartbeatURL = keyring.Replace(hookURL)
	if _, err := svc.Update(t.Context(), actor, audit.TransportAPI, netip.Addr{}, nil, in); err != nil {
		t.Fatal(err)
	}
	in.HeartbeatURL = keyring.Clear
	got, err := svc.Update(t.Context(), actor, audit.TransportAPI, netip.Addr{}, nil, in)
	if err != nil || got.OutgoingHeartbeat.URL.Set {
		t.Fatalf("Update = %+v, %v", got.OutgoingHeartbeat, err)
	}
	if u := s.updates[1]; !u.UrlChanged || u.UrlCiphertext != nil || u.UrlKeyID.Valid || !u.UrlUpdatedAt.Valid {
		t.Errorf("update %+v", u)
	}
	if got := s.hints[len(s.hints)-2:]; !slices.Equal(got, []db.Hint{{OrgID: 7, Type: HintOrganization},
		{OrgID: 7, Type: HintSystemStatus}}) {
		t.Errorf("hints of the clear %v", got)
	}
	// The TOTP policy alone keeps the URL and its result.
	hints := len(s.hints)
	in.TOTPRequired = TOTPEveryone
	if _, err := svc.Update(t.Context(), actor, audit.TransportAPI, netip.Addr{}, nil, in); err != nil {
		t.Fatal(err)
	}
	if u := s.updates[2]; u.UrlChanged {
		t.Errorf("a TOTP change reset the result: %+v", u)
	}
	if got := s.hints[hints:]; !slices.Equal(got, []db.Hint{{OrgID: 7, Type: HintOrganization}}) {
		t.Errorf("hints of a TOTP change %v", got)
	}
}

// TestUpdateHeartbeatProxy: the proxy of the outgoing heartbeat is the shared proxy object, its password a Secret; an
// invalid field names its pointer.
func TestUpdateHeartbeatProxy(t *testing.T) {
	svc, s, k := heartbeatService(t)
	o, _ := svc.Get(t.Context())
	in := inputOf(o)
	typ, addr, user := "http", "proxy.example.org:3128", "hb"
	in.HeartbeatProxy = ProxyInput{Enabled: true, Type: &typ, Address: &addr, UsernameSet: true, Username: &user,
		Password: keyring.Replace("proxy-pw")}
	got, err := svc.Update(t.Context(), actor, audit.TransportAPI, netip.Addr{}, nil, in)
	if err != nil {
		t.Fatal(err)
	}
	p := got.OutgoingHeartbeat.Proxy
	if !p.Enabled || p.Type != "http" || p.Address != addr || *p.Username != "hb" || !got.OutgoingHeartbeat.ProxyPassword.Set {
		t.Errorf("proxy %+v", got.OutgoingHeartbeat)
	}
	u := s.updates[0]
	if pw, err := k.OpenSecret(FieldHeartbeatProxyPassword, secretOf(u.ProxyPasswordCiphertext,
		u.ProxyPasswordKeyID, u.ProxyPasswordUpdatedAt)); err != nil || pw != "proxy-pw" || u.UrlChanged {
		t.Errorf("password %v, update %+v", err, u)
	}
	diff := string(s.audit[0].Diff)
	for _, want := range []string{`"pointer":"/outgoing_heartbeat/proxy/enabled"`,
		`"pointer":"/outgoing_heartbeat/proxy/address","before":"","after":"proxy.example.org:3128"`,
		`{"pointer":"/outgoing_heartbeat/proxy/password","secret_changed":true}`} {
		if !strings.Contains(diff, want) {
			t.Errorf("diff %s lacks %s", diff, want)
		}
	}
	if strings.Contains(diff, "proxy-pw") {
		t.Errorf("the diff has the password: %s", diff)
	}
	bad := "nohost"
	in = inputOf(got)
	in.HeartbeatProxy = ProxyInput{Enabled: true, Address: &bad}
	_, err = svc.Update(t.Context(), actor, audit.TransportAPI, netip.Addr{}, nil, in)
	if f, ok := errors.AsType[*FieldError](err); !ok || f.Pointer != "/outgoing_heartbeat/proxy/address" ||
		f.Code != "invalid_format" || !strings.Contains(f.Error(), f.Pointer) {
		t.Errorf("a bad address: %v", err)
	}
	in.HeartbeatProxy = ProxyInput{Enabled: true, Password: keyring.Replace("")}
	if _, err := svc.Update(t.Context(), actor, audit.TransportAPI, netip.Addr{}, nil, in); err == nil {
		t.Error("an empty password was accepted")
	} else if f, ok := errors.AsType[*FieldError](err); !ok || f.Pointer != "/outgoing_heartbeat/proxy/password" ||
		f.Code != "too_short" {
		t.Errorf("an empty password: %v", err)
	}
}

// TestUpdateHeartbeatURLInvalid: a URL that is not an absolute http or https URL with a host, has a fragment or is
// too long is refused at its pointer, without the URL in the error; an empty one is too short.
func TestUpdateHeartbeatURLInvalid(t *testing.T) {
	svc, s, _ := heartbeatService(t)
	o, _ := svc.Get(t.Context())
	for _, raw := range []string{"ftp://hc.example.org/x", "/relative", "https://", "https://hc.example.org/#frag",
		"https://hc.example.org/" + strings.Repeat("a", maxHeartbeatURL), " https://hc.example.org/", "http://:80/"} {
		in := inputOf(o)
		in.HeartbeatURL = keyring.Replace(logging.Secret(raw))
		_, err := svc.Update(t.Context(), actor, audit.TransportAPI, netip.Addr{}, nil, in)
		if f, ok := errors.AsType[*FieldError](err); !ok || f.Pointer != "/outgoing_heartbeat/url" ||
			f.Code != "invalid_format" || strings.Contains(f.Detail, "hc.example.org") {
			t.Errorf("%q: %v", raw, err)
		}
	}
	in := inputOf(o)
	in.HeartbeatURL = keyring.Replace("")
	if _, err := svc.Update(t.Context(), actor, audit.TransportAPI, netip.Addr{}, nil, in); err == nil {
		t.Error("an empty URL was accepted")
	} else if f, ok := errors.AsType[*FieldError](err); !ok || f.Code != "too_short" {
		t.Errorf("an empty URL: %v", err)
	}
	if len(s.updates) != 0 {
		t.Errorf("a refused update wrote %d", len(s.updates))
	}
}

// TestUpdateWithoutKeyring: a Service without a Keyring refuses to set a Secret instead of panicking, still refuses an
// empty one as too short, and still clears one.
func TestUpdateWithoutKeyring(t *testing.T) {
	s := newFakeSettings(t)
	svc := NewService(7, s, audit.NewWriter(logging.New(&bytes.Buffer{}, logging.LevelInfo), clock.NewManual(t0)),
		clock.NewManual(t0))
	o, _ := svc.Get(t.Context())
	in := inputOf(o)
	in.HeartbeatURL = keyring.Replace(hookURL)
	if _, err := svc.Update(t.Context(), actor, audit.TransportAPI, netip.Addr{}, nil, in); !errors.Is(err,
		errNoKeyring) {
		t.Errorf("Update = %v", err)
	}
	in.HeartbeatURL = keyring.Replace("")
	_, err := svc.Update(t.Context(), actor, audit.TransportAPI, netip.Addr{}, nil, in)
	if f, ok := errors.AsType[*FieldError](err); !ok || f.Pointer != "/outgoing_heartbeat/url" ||
		f.Code != "too_short" {
		t.Errorf("an empty URL without a keyring: %v", err)
	}
	in.HeartbeatURL = keyring.Clear
	if _, err := svc.Update(t.Context(), actor, audit.TransportAPI, netip.Addr{}, nil, in); err != nil {
		t.Errorf("clearing: %v", err)
	}
}

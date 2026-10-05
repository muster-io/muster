// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package organization

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

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

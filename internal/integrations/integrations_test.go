// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	auditdb "github.com/muster-io/muster/internal/audit/dbgen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/integrations/dbgen"
	idb "github.com/muster-io/muster/internal/internalalerts/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/tokens"
)

const orgID = 7

var t0 = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

var by = Requester{Actor: audit.User(1, "SRAAAAAAAAAAAA"), Transport: audit.TransportUI}

type row struct {
	dbgen.GetIntegrationRow
	deleted bool
}

type tokenRow struct {
	id, integrationID int64
	publicID, name    string
	hash              []byte
	created           time.Time
	used, revoked     *time.Time
}

// fakeStore is the database of the package in memory.
type fakeStore struct {
	// mu guards what the goroutines of RunInfo and RunTouches read and write.
	mu      sync.Mutex
	rows    []*row
	tokens  []*tokenRow
	last    map[int64]time.Time
	audit   []auditdb.InsertAuditEntryParams
	hints   []db.Hint
	touched []int64
	fail    map[string]error
	nextID  int64
	// truncated counts the truncated groupKeys by Integration, routes are the learned Alertmanager routes, open the
	// firing Internal alerts, and snapshots the synthetic Stored Snapshots written with their bodies.
	truncated map[int64]int64
	routes    []dbgen.ListLongRepeatRoutesRow
	open      []idb.ListOpenInternalAlertsRow
	snapshots []internalSnapshot
	body      []byte
}

// internalSnapshot is a synthetic Stored Snapshot the fake stored.
type internalSnapshot struct {
	integrationID int64
	receivedAt    time.Time
	body          []byte
}

func newStore() *fakeStore {
	return &fakeStore{last: map[int64]time.Time{}, fail: map[string]error{}, truncated: map[int64]int64{}}
}

func (s *fakeStore) EnsureBuiltin(_ context.Context, arg dbgen.EnsureBuiltinParams) (string, error) {
	if err := s.fail["EnsureBuiltin"]; err != nil {
		return "", err
	}
	if slices.ContainsFunc(s.rows, func(r *row) bool { return r.Builtin }) {
		return "", pgx.ErrNoRows
	}
	if s.nameTaken(arg.Name, 0) {
		return "", errUnique
	}
	s.nextID++
	s.rows = append(s.rows, &row{GetIntegrationRow: dbgen.GetIntegrationRow{
		ID: s.nextID, PublicID: arg.PublicID, Name: arg.Name, Description: arg.Description, Builtin: true,
		ConnectionMode: ConnectionWebhookOnly, StaticLabels: []byte(`{}`),
		DuplicateWindowSeconds: arg.DuplicateWindowSeconds, HeartbeatTimeoutSeconds: arg.HeartbeatTimeoutSeconds,
		HeartbeatState: HeartbeatNotConfigured, CreatedAt: arg.Now, Version: 1,
	}})
	return arg.PublicID, nil
}

func (s *fakeStore) CountTruncatedGroupsOf(_ context.Context, arg dbgen.CountTruncatedGroupsOfParams) (
	[]dbgen.CountTruncatedGroupsOfRow, error) {
	if err := s.fail["CountTruncatedGroupsOf"]; err != nil {
		return nil, err
	}
	var out []dbgen.CountTruncatedGroupsOfRow
	for _, id := range arg.IntegrationIds {
		if n := s.truncated[id]; n > 0 && arg.OrgID == orgID {
			out = append(out, dbgen.CountTruncatedGroupsOfRow{IntegrationID: id, TruncatedGroupCount: n})
		}
	}
	return out, nil
}

func (s *fakeStore) ListLongRepeatRoutes(_ context.Context, arg dbgen.ListLongRepeatRoutesParams) (
	[]dbgen.ListLongRepeatRoutesRow, error) {
	if err := s.fail["ListLongRepeatRoutes"]; err != nil {
		return nil, err
	}
	var out []dbgen.ListLongRepeatRoutesRow
	for _, r := range s.routes {
		if slices.Contains(arg.IntegrationIds, r.IntegrationID) && r.LearnedRepeatIntervalMs > arg.ThresholdMs {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *fakeStore) FindBuiltinIntegration(_ context.Context, org int64) (int64, error) {
	for _, r := range s.rows {
		if r.Builtin && org == orgID {
			return r.ID, nil
		}
	}
	return 0, pgx.ErrNoRows
}

func (s *fakeStore) InsertInternalBody(_ context.Context, arg idb.InsertInternalBodyParams) error {
	s.body = arg.Body
	return s.fail["InsertInternalBody"]
}

func (s *fakeStore) InsertInternalSnapshot(_ context.Context, arg idb.InsertInternalSnapshotParams) error {
	if err := s.fail["InsertInternalSnapshot"]; err != nil {
		return err
	}
	s.snapshots = append(s.snapshots, internalSnapshot{integrationID: arg.IntegrationID, receivedAt: arg.ReceivedAt,
		body: s.body})
	return nil
}

func (s *fakeStore) ListPendingInternalRaises(context.Context, int64) ([][]byte, error) {
	return nil, s.fail["ListPendingInternalRaises"]
}

func (s *fakeStore) NotifyInternalSnapshot(context.Context, idb.NotifyInternalSnapshotParams) error {
	return nil
}

func (s *fakeStore) ListOpenInternalAlerts(_ context.Context, arg idb.ListOpenInternalAlertsParams) (
	[]idb.ListOpenInternalAlertsRow, error) {
	if err := s.fail["ListOpenInternalAlerts"]; err != nil {
		return nil, err
	}
	var want map[string]string
	if err := json.Unmarshal(arg.Contains, &want); err != nil {
		return nil, err
	}
	var out []idb.ListOpenInternalAlertsRow
	for _, a := range s.open {
		var labels map[string]string
		if err := json.Unmarshal(a.Labels, &labels); err != nil {
			return nil, err
		}
		match := true
		for k, v := range want {
			match = match && labels[k] == v
		}
		if match {
			out = append(out, a)
		}
	}
	return out, nil
}

func (s *fakeStore) InTx(_ context.Context, f func(Queries) error) error { return f(s) }

func (s *fakeStore) find(publicID string) *row {
	for _, r := range s.rows {
		if r.PublicID == publicID && !r.deleted {
			return r
		}
	}
	return nil
}

func (s *fakeStore) ListIntegrations(_ context.Context, arg dbgen.ListIntegrationsParams) (
	[]dbgen.ListIntegrationsRow, error) {
	if err := s.fail["ListIntegrations"]; err != nil {
		return nil, err
	}
	var out []dbgen.ListIntegrationsRow
	for _, r := range s.rows {
		if r.deleted || arg.OrgID != orgID || (arg.AfterID.Valid && r.ID <= arg.AfterID.Int64) {
			continue
		}
		if len(out) == int(arg.PageSize) {
			break
		}
		out = append(out, dbgen.ListIntegrationsRow(r.GetIntegrationRow))
	}
	return out, nil
}

func (s *fakeStore) GetIntegration(_ context.Context, arg dbgen.GetIntegrationParams) (dbgen.GetIntegrationRow, error) {
	if err := s.fail["GetIntegration"]; err != nil {
		return dbgen.GetIntegrationRow{}, err
	}
	if r := s.find(arg.PublicID); r != nil && arg.OrgID == orgID {
		return r.GetIntegrationRow, nil
	}
	return dbgen.GetIntegrationRow{}, pgx.ErrNoRows
}

func (s *fakeStore) LastSnapshotTimes(_ context.Context, arg dbgen.LastSnapshotTimesParams) (
	[]dbgen.LastSnapshotTimesRow, error) {
	if err := s.fail["LastSnapshotTimes"]; err != nil {
		return nil, err
	}
	var out []dbgen.LastSnapshotTimesRow
	for _, id := range arg.IntegrationIds {
		if at, ok := s.last[id]; ok {
			out = append(out, dbgen.LastSnapshotTimesRow{IntegrationID: id, ReceivedAt: at})
		}
	}
	return out, nil
}

func (s *fakeStore) LockIntegration(_ context.Context, arg dbgen.LockIntegrationParams) (int64, error) {
	if err := s.fail["LockIntegration"]; err != nil {
		return 0, err
	}
	if r := s.find(arg.PublicID); r != nil {
		return r.ID, nil
	}
	return 0, pgx.ErrNoRows
}

func (s *fakeStore) FindIntegrationByName(_ context.Context, arg dbgen.FindIntegrationByNameParams) (string, error) {
	if err := s.fail["FindIntegrationByName"]; err != nil {
		return "", err
	}
	for _, r := range s.rows {
		if r.Name == arg.Name && !r.deleted {
			return r.PublicID, nil
		}
	}
	return "", pgx.ErrNoRows
}

func (s *fakeStore) nameTaken(name string, except int64) bool {
	return slices.ContainsFunc(s.rows, func(r *row) bool { return r.Name == name && !r.deleted && r.ID != except })
}

var errUnique = &pgconn.PgError{Code: "23505", ConstraintName: "integrations_name_key"}

func (s *fakeStore) InsertIntegration(_ context.Context, arg dbgen.InsertIntegrationParams) (int64, error) {
	if err := s.fail["InsertIntegration"]; err != nil {
		return 0, err
	}
	if s.nameTaken(arg.Name, 0) {
		return 0, errUnique
	}
	s.nextID++
	s.rows = append(s.rows, &row{GetIntegrationRow: dbgen.GetIntegrationRow{
		ID: s.nextID, PublicID: arg.PublicID, Name: arg.Name, Description: arg.Description,
		ConnectionMode: arg.ConnectionMode, StaticLabels: arg.StaticLabels,
		DuplicateWindowSeconds: arg.DuplicateWindowSeconds, HeartbeatEnabled: arg.HeartbeatEnabled,
		HeartbeatTimeoutSeconds: arg.HeartbeatTimeoutSeconds, HeartbeatState: HeartbeatNotConfigured, CreatedAt: arg.Now,
		Version: 1,
	}})
	if arg.HeartbeatEnabled {
		s.rows[len(s.rows)-1].HeartbeatState = HeartbeatWaiting
	}
	return s.nextID, nil
}

func (s *fakeStore) byID(id int64) *row {
	for _, r := range s.rows {
		if r.ID == id {
			return r
		}
	}
	return nil
}

func (s *fakeStore) UpdateIntegration(_ context.Context, arg dbgen.UpdateIntegrationParams) error {
	if err := s.fail["UpdateIntegration"]; err != nil {
		return err
	}
	if s.nameTaken(arg.Name, arg.ID) {
		return errUnique
	}
	r := s.byID(arg.ID)
	r.Name, r.Description, r.StaticLabels = arg.Name, arg.Description, arg.StaticLabels
	r.DuplicateWindowSeconds, r.HeartbeatTimeoutSeconds = arg.DuplicateWindowSeconds, arg.HeartbeatTimeoutSeconds
	r.HeartbeatEnabled = arg.HeartbeatEnabled
	switch {
	case !arg.HeartbeatEnabled:
		r.HeartbeatState, r.HeartbeatLostSince = HeartbeatNotConfigured, pgtype.Timestamptz{}
	case r.HeartbeatState == HeartbeatNotConfigured:
		r.HeartbeatState = HeartbeatWaiting
	}
	r.Version++
	return nil
}

func (s *fakeStore) DeleteIntegration(_ context.Context, arg dbgen.DeleteIntegrationParams) error {
	if err := s.fail["DeleteIntegration"]; err != nil {
		return err
	}
	r := s.byID(arg.ID)
	r.deleted = true
	r.Version++
	return nil
}

func (s *fakeStore) ListIntegrationInfo(context.Context, int64) ([]dbgen.ListIntegrationInfoRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail["ListIntegrationInfo"]; err != nil {
		return nil, err
	}
	var out []dbgen.ListIntegrationInfoRow
	for _, r := range s.rows {
		if !r.deleted {
			out = append(out, dbgen.ListIntegrationInfoRow{PublicID: r.PublicID, Name: r.Name})
		}
	}
	return out, nil
}

func (s *fakeStore) InsertIntegrationToken(_ context.Context, arg dbgen.InsertIntegrationTokenParams) (int64, error) {
	if err := s.fail["InsertIntegrationToken"]; err != nil {
		return 0, err
	}
	s.nextID++
	s.tokens = append(s.tokens, &tokenRow{id: s.nextID, integrationID: arg.IntegrationID, publicID: arg.PublicID,
		name: arg.Name.String, hash: arg.TokenHash, created: arg.Now})
	return s.nextID, nil
}

func (s *fakeStore) EnsureIntegrationToken(_ context.Context, arg dbgen.EnsureIntegrationTokenParams) (string,
	error) {
	if err := s.fail["EnsureIntegrationToken"]; err != nil {
		return "", err
	}
	for _, t := range s.tokens {
		if bytes.Equal(t.hash, arg.TokenHash) {
			if t.integrationID == arg.IntegrationID && t.revoked == nil {
				return "", pgx.ErrNoRows
			}
			t.integrationID, t.revoked = arg.IntegrationID, nil
			return t.publicID, nil
		}
	}
	_, err := s.InsertIntegrationToken(context.Background(), dbgen.InsertIntegrationTokenParams(arg))
	return arg.PublicID, err
}

func (s *fakeStore) LockDemo(context.Context, int64) error { return s.fail["LockDemo"] }

func (s *fakeStore) ListIntegrationTokens(_ context.Context, arg dbgen.ListIntegrationTokensParams) (
	[]dbgen.ListIntegrationTokensRow, error) {
	if err := s.fail["ListIntegrationTokens"]; err != nil {
		return nil, err
	}
	var out []dbgen.ListIntegrationTokensRow
	for _, t := range slices.Backward(s.tokens) {
		if t.integrationID == arg.IntegrationID && t.revoked == nil {
			out = append(out, dbgen.ListIntegrationTokensRow{ID: t.id, PublicID: t.publicID,
				Name: pgtype.Text{String: t.name, Valid: t.name != ""}, CreatedAt: t.created, LastUsedAt: ts(t.used)})
		}
	}
	return out, nil
}

func (s *fakeStore) RevokeIntegrationToken(_ context.Context, arg dbgen.RevokeIntegrationTokenParams) (
	dbgen.RevokeIntegrationTokenRow, error) {
	if err := s.fail["RevokeIntegrationToken"]; err != nil {
		return dbgen.RevokeIntegrationTokenRow{}, err
	}
	for _, t := range s.tokens {
		if t.publicID == arg.PublicID && t.integrationID == arg.IntegrationID && t.revoked == nil {
			now := arg.Now
			t.revoked = &now
			return dbgen.RevokeIntegrationTokenRow{ID: t.id, Name: pgtype.Text{String: t.name, Valid: t.name != ""}}, nil
		}
	}
	return dbgen.RevokeIntegrationTokenRow{}, pgx.ErrNoRows
}

func (s *fakeStore) FindIngestToken(_ context.Context, arg dbgen.FindIngestTokenParams) (dbgen.FindIngestTokenRow,
	error) {
	if err := s.fail["FindIngestToken"]; err != nil {
		return dbgen.FindIngestTokenRow{}, err
	}
	for _, t := range s.tokens {
		if bytes.Equal(t.hash, arg.TokenHash) {
			r := s.byID(t.integrationID)
			out := dbgen.FindIngestTokenRow{ID: t.id, TokenHash: t.hash, RevokedAt: ts(t.revoked),
				LastUsedAt: ts(t.used), IntegrationID: r.ID, IntegrationPublicID: r.PublicID}
			if r.deleted {
				out.IntegrationDeletedAt = pgtype.Timestamptz{Time: t0, Valid: true}
			}
			return out, nil
		}
	}
	return dbgen.FindIngestTokenRow{}, pgx.ErrNoRows
}

// rename renames the Integration id as another replica would.
func (s *fakeStore) rename(id int64, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID(id).Name = name
}

// usedAt is the last use of the first token, and how many uses were written.
func (s *fakeStore) usedAt() (*time.Time, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens[0].used, len(s.touched)
}

func (s *fakeStore) TouchIntegrationToken(_ context.Context, arg dbgen.TouchIntegrationTokenParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.touched = append(s.touched, arg.ID)
	for _, t := range s.tokens {
		if t.id == arg.ID {
			now := arg.Now
			t.used = &now
		}
	}
	return s.fail["TouchIntegrationToken"]
}

func (s *fakeStore) InsertAuditEntry(_ context.Context, arg auditdb.InsertAuditEntryParams) error {
	if err := s.fail["InsertAuditEntry"]; err != nil {
		return err
	}
	s.audit = append(s.audit, arg)
	return nil
}

func (s *fakeStore) Notify(_ context.Context, h db.Hint) error {
	if err := s.fail["Notify"]; err != nil {
		return err
	}
	s.hints = append(s.hints, h)
	return nil
}

func (s *fakeStore) lastAudit(t *testing.T) (auditdb.InsertAuditEntryParams, []audit.Change) {
	t.Helper()
	if len(s.audit) == 0 {
		t.Fatal("no audit entry")
	}
	e := s.audit[len(s.audit)-1]
	var diff []audit.Change
	if err := json.Unmarshal(e.Diff, &diff); err != nil {
		t.Fatal(err)
	}
	return e, diff
}

func ts(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

func newService(t *testing.T) (*Service, *fakeStore, *clock.Manual, *bytes.Buffer) {
	t.Helper()
	store := newStore()
	c := clock.NewManual(t0)
	var log bytes.Buffer
	u, _ := url.Parse("https://ingest.example.org/")
	svc := New(Config{OrgID: orgID, Store: store, Audit: audit.NewWriter(logging.New(&log, logging.LevelInfo), c),
		Business: c, IngestURL: u})
	return svc, store, c, &log
}

func input(name string) Input {
	return Input{Name: name, ConnectionMode: ConnectionWebhookOnly, StaticLabels: map[string]string{"cluster": "eu"},
		DuplicateWindowSeconds: 45}
}

func ptr[T any](v T) *T { return &v }

func fieldErr(t *testing.T, err error) *FieldError {
	t.Helper()
	f, ok := errors.AsType[*FieldError](err)
	if !ok {
		t.Fatalf("err = %v, want a FieldError", err)
	}
	return f
}

// infoSeries is the muster_integration_info series of the integration id, as /metrics shows it.
func infoSeries(t *testing.T, id string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	metrics.Handler(nil).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	var out []string
	for line := range strings.Lines(rec.Body.String()) {
		if strings.HasPrefix(line, `muster_integration_info{integration="`+id+`"`) {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return strings.Join(out, "\n")
}

// TestCreate is C-05.FR-1 and FR-6: an Integration is stored with its name, description, Connection mode, Static
// labels and duplicate window, its Heartbeat off with the default timeout; the Audit log has the creation with its
// values and the live hint names it.
func TestCreate(t *testing.T) {
	svc, store, _, _ := newService(t)
	in := input("prod-eu")
	in.Description = ptr("EU cluster")
	created, err := svc.Create(t.Context(), by, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.PublicID, "NT") || created.Name != "prod-eu" || created.Description != "EU cluster" ||
		created.ConnectionMode != "webhook_only" || created.StaticLabels["cluster"] != "eu" ||
		created.DuplicateWindowSeconds != 45 || created.Heartbeat.Enabled || created.Heartbeat.TimeoutSeconds != 300 ||
		created.Heartbeat.State != "not_configured" || created.LastSnapshotAt != nil || created.Version != 1 {
		t.Errorf("created = %+v", created)
	}
	e, diff := store.lastAudit(t)
	if e.Action != "integration.created" || e.ResourceType.String != "integration" ||
		e.ResourcePublicID.String != created.PublicID || len(diff) != 6 {
		t.Errorf("audit %s %s %s diff %+v", e.Action, e.ResourceType.String, e.ResourcePublicID.String, diff)
	}
	if len(store.hints) != 1 || store.hints[0] != (db.Hint{OrgID: orgID, Type: "integration", ID: created.PublicID}) {
		t.Errorf("hints = %+v", store.hints)
	}
	if got := infoSeries(t, created.PublicID); got != `muster_integration_info{integration="`+created.PublicID+
		`",name="prod-eu"} 1` {
		t.Errorf("info = %q", got)
	}

	// Without labels or a description, with an explicit Heartbeat timeout.
	other := Input{Name: "other", ConnectionMode: ConnectionWebhookOnly, DuplicateWindowSeconds: 1,
		Heartbeat: HeartbeatInput{TimeoutSeconds: ptr(int64(60))}}
	o, err := svc.Create(t.Context(), by, other)
	if err != nil || len(o.StaticLabels) != 0 || o.StaticLabels == nil || o.Heartbeat.TimeoutSeconds != 60 {
		t.Errorf("other = %+v, %v", o, err)
	}
}

// TestCreateRefusals: a taken name is ErrNameTaken; invalid fields are FieldErrors at their JSON pointer; a Heartbeat
// that is switched on is unsupported until the Heartbeat endpoint exists.
func TestCreateRefusals(t *testing.T) {
	svc, store, _, _ := newService(t)
	if _, err := svc.Create(t.Context(), by, input("prod-eu")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(t.Context(), by, input("prod-eu")); !errors.Is(err, ErrNameTaken) {
		t.Errorf("same name = %v, want ErrNameTaken", err)
	}
	tests := []struct {
		name          string
		change        func(*Input)
		pointer, code string
	}{
		{"empty name", func(in *Input) { in.Name = "  " }, "/name", CodeInvalidFormat},
		{"long name", func(in *Input) { in.Name = strings.Repeat("n", 201) }, "/name", CodeTooLong},
		{"other mode", func(in *Input) { in.ConnectionMode = "api_polling" }, "/connection_mode", CodeInvalidFormat},
		{"label starting with a digit", func(in *Input) { in.StaticLabels = map[string]string{"ok": "", "1x": "v"} },
			"/static_labels/1x", CodeInvalidFormat},
		{"label with a slash", func(in *Input) { in.StaticLabels = map[string]string{"a/b": "v"} },
			"/static_labels/a~1b", CodeInvalidFormat},
		{"label with a dash", func(in *Input) { in.StaticLabels = map[string]string{"team-name": "v"} },
			"/static_labels/team-name", CodeInvalidFormat},
		{"zero window", func(in *Input) { in.DuplicateWindowSeconds = 0 }, "/duplicate_window_seconds", CodeOutOfRange},
		{"zero timeout", func(in *Input) { in.Heartbeat.TimeoutSeconds = ptr(int64(0)) }, "/heartbeat/timeout_seconds",
			CodeOutOfRange},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := input("x")
			tt.change(&in)
			_, err := svc.Create(t.Context(), by, in)
			if f := fieldErr(t, err); f.Pointer != tt.pointer || f.Code != tt.code || f.Error() == "" {
				t.Errorf("err = %+v, want %s at %s", f, tt.code, tt.pointer)
			}
		})
	}
	// Names are stored trimmed, so a trailing space does not make another name.
	if _, err := svc.Create(t.Context(), by, input("prod-eu ")); !errors.Is(err, ErrNameTaken) {
		t.Errorf("a name with a trailing space = %v, want ErrNameTaken", err)
	}
	// Labels of the Prometheus form pass.
	in := input("labels")
	in.StaticLabels = map[string]string{"_x": "1", "Team_2": "a b", "le": ""}
	if _, err := svc.Create(t.Context(), by, in); err != nil {
		t.Errorf("valid labels: %v", err)
	}
	store.fail["InsertIntegration"] = errors.New("down")
	if _, err := svc.Create(t.Context(), by, input("y")); err == nil || errors.Is(err, ErrNameTaken) {
		t.Errorf("failed insert = %v", err)
	}
}

// TestUpdate: the configured fields change with If-Match, the diff names what changed, and the runtime state stays.
func TestUpdate(t *testing.T) {
	svc, store, _, _ := newService(t)
	created, err := svc.Create(t.Context(), by, input("prod-eu"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(t.Context(), by, created.PublicID, ptr(int64(2)), input("prod-eu")); !errors.Is(err,
		ErrVersionMismatch) {
		t.Errorf("stale version = %v", err)
	}
	// Nothing changes: no write, no entry.
	audits := len(store.audit)
	same, err := svc.Update(t.Context(), by, created.PublicID, ptr(int64(1)), input("prod-eu"))
	if err != nil || same.Version != 1 || len(store.audit) != audits {
		t.Errorf("unchanged = %+v, %v; audits %d", same, err, len(store.audit)-audits)
	}
	in := input("prod-eu-1")
	in.StaticLabels = map[string]string{"cluster": "eu-1"}
	in.DuplicateWindowSeconds = 60
	in.Heartbeat.TimeoutSeconds = ptr(int64(120))
	updated, err := svc.Update(t.Context(), by, strings.ToLower(created.PublicID), ptr(int64(1)), in)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "prod-eu-1" || updated.Version != 2 || updated.DuplicateWindowSeconds != 60 ||
		updated.Heartbeat.TimeoutSeconds != 120 || updated.Description != "" {
		t.Errorf("updated = %+v", updated)
	}
	e, diff := store.lastAudit(t)
	var pointers []string
	for _, c := range diff {
		pointers = append(pointers, c.Pointer)
	}
	if e.Action != "integration.updated" || !slices.Equal(pointers, []string{"/name", "/static_labels",
		"/duplicate_window_seconds", "/heartbeat/timeout_seconds"}) {
		t.Errorf("audit %s %v", e.Action, pointers)
	}
	if infoSeries(t, created.PublicID) != `muster_integration_info{integration="`+created.PublicID+
		`",name="prod-eu-1"} 1` {
		t.Errorf("info after rename = %q", infoSeries(t, created.PublicID))
	}
	// An omitted description keeps the stored one; a given one replaces it.
	in.Description = ptr("described")
	if u, err := svc.Update(t.Context(), by, created.PublicID, nil, in); err != nil || u.Description != "described" {
		t.Errorf("description = %+v, %v", u, err)
	}
	in.Description = nil
	if u, err := svc.Update(t.Context(), by, created.PublicID, nil, in); err != nil || u.Description != "described" {
		t.Errorf("kept description = %+v, %v", u, err)
	}
	other, _ := svc.Create(t.Context(), by, input("other"))
	if _, err := svc.Update(t.Context(), by, other.PublicID, nil, in); !errors.Is(err, ErrNameTaken) {
		t.Errorf("rename onto a taken name = %v", err)
	}
	if _, err := svc.Update(t.Context(), by, "NT0000000000ZZ", nil, in); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown = %v", err)
	}
	if _, err := svc.Update(t.Context(), by, "not-an-id", nil, in); !errors.Is(err, ErrNotFound) {
		t.Errorf("malformed id = %v", err)
	}
}

// TestDelete is C-05.FR-8: a deleted Integration leaves the list and the reads, its info series goes, and its name is
// free again.
func TestDelete(t *testing.T) {
	svc, store, _, _ := newService(t)
	created, err := svc.Create(t.Context(), by, input("prod-eu"))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(t.Context(), by, created.PublicID, ptr(int64(9))); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("stale delete = %v", err)
	}
	if err := svc.Delete(t.Context(), by, created.PublicID, ptr(int64(1))); err != nil {
		t.Fatal(err)
	}
	e, diff := store.lastAudit(t)
	if e.Action != "integration.deleted" || len(diff) != 1 || diff[0].Pointer != "/deleted_at" {
		t.Errorf("audit %s %+v", e.Action, diff)
	}
	if _, err := svc.Get(t.Context(), created.PublicID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete = %v", err)
	}
	if err := svc.Delete(t.Context(), by, created.PublicID, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete = %v", err)
	}
	page, err := svc.List(t.Context(), ListFilter{Limit: 10})
	if err != nil || len(page.Integrations) != 0 {
		t.Errorf("list = %+v, %v", page, err)
	}
	if got := infoSeries(t, created.PublicID); got != "" {
		t.Errorf("info after delete = %q", got)
	}
	if _, err := svc.Create(t.Context(), by, input("prod-eu")); err != nil {
		t.Errorf("reuse of the name: %v", err)
	}
	if len(store.hints) != 3 {
		t.Errorf("hints = %+v", store.hints)
	}
}

// TestListAndGet: pages in the order of creation with the time of the newest Stored Snapshot.
func TestListAndGet(t *testing.T) {
	svc, store, _, _ := newService(t)
	var ids []string
	for _, name := range []string{"a", "b", "c"} {
		in, err := svc.Create(t.Context(), by, input(name))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, in.PublicID)
	}
	store.last[2] = t0.Add(time.Minute)
	page, err := svc.List(t.Context(), ListFilter{Limit: 2})
	if err != nil || len(page.Integrations) != 2 || page.Next == nil || *page.Next != 2 {
		t.Fatalf("page 1 = %+v, %v", page, err)
	}
	if page.Integrations[0].LastSnapshotAt != nil || page.Integrations[1].LastSnapshotAt == nil ||
		!page.Integrations[1].LastSnapshotAt.Equal(t0.Add(time.Minute)) {
		t.Errorf("last snapshot times %v %v", page.Integrations[0].LastSnapshotAt, page.Integrations[1].LastSnapshotAt)
	}
	page, err = svc.List(t.Context(), ListFilter{After: page.Next, Limit: 2})
	if err != nil || len(page.Integrations) != 1 || page.Integrations[0].PublicID != ids[2] || page.Next != nil {
		t.Errorf("page 2 = %+v, %v", page, err)
	}
	got, err := svc.Get(t.Context(), ids[1])
	if err != nil || got.LastSnapshotAt == nil {
		t.Errorf("get = %+v, %v", got, err)
	}
	for _, name := range []string{"ListIntegrations", "LastSnapshotTimes"} {
		store.fail[name] = errors.New("down")
		if _, err := svc.List(t.Context(), ListFilter{Limit: 2}); err == nil {
			t.Errorf("%s failing: no error", name)
		}
		delete(store.fail, name)
	}
	store.rows[0].StaticLabels = []byte("[")
	if _, err := svc.List(t.Context(), ListFilter{Limit: 2}); err == nil {
		t.Error("broken labels: no error")
	}
	store.fail["GetIntegration"] = errors.New("down")
	if _, err := svc.Get(t.Context(), ids[1]); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("get failing = %v", err)
	}
}

// TestWritesFail: a failure of any step of a change fails the change.
func TestWritesFail(t *testing.T) {
	for step, ops := range map[string][]string{
		"InsertAuditEntry": {"create", "update", "delete"}, "Notify": {"create", "update", "delete"},
		"LockIntegration": {"update", "delete"}, "UpdateIntegration": {"update"}, "DeleteIntegration": {"delete"},
	} {
		t.Run(step, func(t *testing.T) {
			svc, store, _, _ := newService(t)
			created, err := svc.Create(t.Context(), by, input("a"))
			if err != nil {
				t.Fatal(err)
			}
			store.fail[step] = errors.New("down")
			errs := map[string]error{}
			_, errs["create"] = svc.Create(t.Context(), by, input("b"))
			_, errs["update"] = svc.Update(t.Context(), by, created.PublicID, nil, input("c"))
			errs["delete"] = svc.Delete(t.Context(), by, created.PublicID, nil)
			for _, op := range ops {
				if errs[op] == nil {
					t.Errorf("%s did not fail", op)
				}
			}
		})
	}
}

// TestURLs: the ingestion and Heartbeat URLs are under MUSTER_INGEST_URL.
func TestURLs(t *testing.T) {
	svc, _, _, _ := newService(t)
	if svc.IngestURL() != "https://ingest.example.org/api/v1/ingest" ||
		svc.HeartbeatURL() != "https://ingest.example.org/api/v1/heartbeat" {
		t.Errorf("urls %s %s", svc.IngestURL(), svc.HeartbeatURL())
	}
	if s := New(Config{}); s.IngestURL() != "/api/v1/ingest" {
		t.Errorf("without a base: %s", s.IngestURL())
	}
}

// TestRefreshInfo: muster_integration_info lists exactly the Integrations that are not deleted in the database, also
// after changes made elsewhere.
func TestRefreshInfo(t *testing.T) {
	svc, store, _, _ := newService(t)
	a, _ := svc.Create(t.Context(), by, input("a"))
	b, _ := svc.Create(t.Context(), by, input("b"))
	// Another replica renamed a and deleted b.
	store.byID(a.ID).Name = "a2"
	store.byID(b.ID).deleted = true
	if err := svc.RefreshInfo(t.Context()); err != nil {
		t.Fatal(err)
	}
	if infoSeries(t, a.PublicID) != `muster_integration_info{integration="`+a.PublicID+`",name="a2"} 1` ||
		infoSeries(t, b.PublicID) != "" {
		t.Errorf("info a %q b %q", infoSeries(t, a.PublicID), infoSeries(t, b.PublicID))
	}
	store.fail["ListIntegrationInfo"] = errors.New("down")
	if err := svc.RefreshInfo(t.Context()); err == nil {
		t.Error("failing refresh: no error")
	}
	delete(store.fail, "ListIntegrationInfo")

	// RunInfo refreshes at once, after InfoChanged and at every tick, until its context ends.
	store.rename(a.ID, "a3")
	ctx, cancel := context.WithCancel(t.Context())
	ticks := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		svc.RunInfo(ctx, ticks)
		close(done)
	}()
	waitFor(t, func() bool { return strings.Contains(infoSeries(t, a.PublicID), `name="a3"`) })
	store.rename(a.ID, "a4")
	svc.InfoChanged()
	svc.InfoChanged() // never blocks
	waitFor(t, func() bool { return strings.Contains(infoSeries(t, a.PublicID), `name="a4"`) })
	store.rename(a.ID, "a5")
	ticks <- t0
	waitFor(t, func() bool { return strings.Contains(infoSeries(t, a.PublicID), `name="a5"`) })
	cancel()
	<-done
	svc.unexportInfo(a.PublicID)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for range 2500 {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("the condition never held")
}

// TestEnsureDemo: the demo Integration is created once with its token; a later start finds both; a revoked token is
// given back; Muster is the actor.
func TestEnsureDemo(t *testing.T) {
	svc, store, _, _ := newService(t)
	d := Demo{Name: "dev-alertmanager", StaticLabels: map[string]string{"cluster": "dev"},
		Token: "mstr_int_" + strings.Repeat("dev", 17) + "d", TokenName: "dev"}
	if err := svc.EnsureDemo(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnsureDemo(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	if len(store.rows) != 1 || len(store.tokens) != 1 || len(store.audit) != 2 {
		t.Fatalf("rows %d tokens %d audit %d", len(store.rows), len(store.tokens), len(store.audit))
	}
	if store.audit[0].ActorKind != "system" || store.audit[0].Action != "integration.created" ||
		store.audit[1].Action != "integration_token.created" ||
		store.audit[1].ResourcePublicID.String != store.tokens[0].publicID {
		t.Errorf("audit %+v", store.audit)
	}
	caller, err := svc.Authenticate(t.Context(), d.Token)
	if err != nil || caller.Integration != store.rows[0].PublicID {
		t.Errorf("demo token = %+v, %v", caller, err)
	}
	now := t0
	store.tokens[0].revoked = &now
	if err := svc.EnsureDemo(t.Context(), d); err != nil || store.tokens[0].revoked != nil || len(store.audit) != 3 {
		t.Errorf("revoked demo token: %v, %+v", err, store.tokens[0])
	}
	if err := svc.EnsureDemo(t.Context(), Demo{Name: "x", Token: "mstr_pat_x"}); err == nil {
		t.Error("a demo token of another kind was accepted")
	}
	for _, step := range []string{"LockDemo", "FindIntegrationByName", "EnsureIntegrationToken", "GetIntegration"} {
		store.fail[step] = errors.New("down")
		if err := svc.EnsureDemo(t.Context(), d); err == nil {
			t.Errorf("%s failing: no error", step)
		}
		delete(store.fail, step)
	}
	store.rows[0].deleted = true
	store.fail["InsertAuditEntry"] = errors.New("down")
	if err := svc.EnsureDemo(t.Context(), d); err == nil {
		t.Error("audit failing: no error")
	}
}

// TestTokens is C-05.FR-2 and C-05.AC-6: a token is mstr_int_ and 52 characters, returned once with its snippet; only
// its SHA-256 is stored; the list never has a value; a revoked token leaves the list.
func TestTokens(t *testing.T) {
	svc, store, _, log := newService(t)
	in, _ := svc.Create(t.Context(), by, input("prod-eu"))
	created, err := svc.CreateToken(t.Context(), by, in.PublicID, " rotation-1 ")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Value, "mstr_int_") || len(created.Value) != 61 || !wellFormed(created.Value) ||
		!strings.HasPrefix(created.Token.PublicID, "NK") || created.Token.Name != "rotation-1" {
		t.Errorf("created = %+v", created.Token)
	}
	if !bytes.Equal(store.tokens[0].hash, tokens.Hash(created.Value)) || len(store.tokens[0].hash) != 32 {
		t.Error("the stored hash is not the SHA-256 of the value")
	}
	if !strings.Contains(created.Snippet, "credentials: "+created.Value) ||
		!strings.Contains(created.Snippet, "url: https://ingest.example.org/api/v1/ingest") {
		t.Errorf("snippet:\n%s", created.Snippet)
	}
	e, _ := store.lastAudit(t)
	if e.Action != "integration_token.created" || e.ResourcePublicID.String != created.Token.PublicID ||
		strings.Contains(string(e.Details)+string(e.Diff), created.Value) {
		t.Errorf("audit %+v", e)
	}
	if strings.Contains(log.String(), created.Value) {
		t.Error("the token value reached the log")
	}
	unnamed, err := svc.CreateToken(t.Context(), by, in.PublicID, "")
	if err != nil || unnamed.Token.Name != "" {
		t.Errorf("unnamed = %+v, %v", unnamed.Token, err)
	}
	if _, err := svc.CreateToken(t.Context(), by, in.PublicID, strings.Repeat("n", 201)); fieldErr(t,
		err).Code != CodeTooLong {
		t.Errorf("long name = %v", err)
	}
	list, err := svc.ListTokens(t.Context(), in.PublicID)
	if err != nil || len(list) != 2 || list[0].PublicID != unnamed.Token.PublicID || list[1].Name != "rotation-1" {
		t.Errorf("list = %+v, %v", list, err)
	}
	if err := svc.RevokeToken(t.Context(), by, in.PublicID, created.Token.PublicID); err != nil {
		t.Fatal(err)
	}
	if e, _ := store.lastAudit(t); e.Action != "integration_token.revoked" || e.ResourceName.String != "rotation-1" {
		t.Errorf("revoke audit %+v", e)
	}
	if err := svc.RevokeToken(t.Context(), by, in.PublicID, created.Token.PublicID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second revoke = %v", err)
	}
	if err := svc.RevokeToken(t.Context(), by, in.PublicID, "bad"); !errors.Is(err, ErrNotFound) {
		t.Errorf("malformed token id = %v", err)
	}
	if list, _ := svc.ListTokens(t.Context(), in.PublicID); len(list) != 1 {
		t.Errorf("list after revoke = %+v", list)
	}
	if _, err := svc.ListTokens(t.Context(), "NT0000000000ZZ"); !errors.Is(err, ErrNotFound) {
		t.Errorf("tokens of an unknown integration = %v", err)
	}
	if _, err := svc.CreateToken(t.Context(), by, "NT0000000000ZZ", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("token of an unknown integration = %v", err)
	}
	for _, step := range []string{"InsertIntegrationToken", "InsertAuditEntry", "Notify", "ListIntegrationTokens",
		"RevokeIntegrationToken"} {
		store.fail[step] = errors.New("down")
		_, errC := svc.CreateToken(t.Context(), by, in.PublicID, "")
		errR := svc.RevokeToken(t.Context(), by, in.PublicID, unnamed.Token.PublicID)
		_, errL := svc.ListTokens(t.Context(), in.PublicID)
		if errC == nil && errR == nil && errL == nil {
			t.Errorf("%s failing: no error", step)
		}
		delete(store.fail, step)
	}
}

// TestAuthenticate is C-05.FR-3, FR-4 and FR-8 for the token: a valid token names its Integration; a revoked one is
// refused with its Integration's id (P-10); a token of a deleted Integration, an unknown, a malformed one and an API
// token are refused as unknown.
func TestAuthenticate(t *testing.T) {
	svc, store, c, _ := newService(t)
	in, _ := svc.Create(t.Context(), by, input("prod-eu"))
	gone, _ := svc.Create(t.Context(), by, input("gone"))
	valid, _ := svc.CreateToken(t.Context(), by, in.PublicID, "")
	revoked, _ := svc.CreateToken(t.Context(), by, in.PublicID, "")
	ofDeleted, _ := svc.CreateToken(t.Context(), by, gone.PublicID, "")
	_ = svc.RevokeToken(t.Context(), by, in.PublicID, revoked.Token.PublicID)
	_ = svc.Delete(t.Context(), by, gone.PublicID, nil)

	caller, err := svc.Authenticate(t.Context(), valid.Value)
	if err != nil || caller.Integration != in.PublicID || caller.IntegrationID != in.ID || caller.TokenID == 0 {
		t.Errorf("valid = %+v, %v", caller, err)
	}
	pat, _, _ := tokens.Generate(tokens.PrefixPersonal)
	for name, tt := range map[string]struct{ value, label string }{
		"revoked":           {revoked.Value, in.PublicID},
		"deleted":           {ofDeleted.Value, Unknown},
		"unknown":           {"mstr_int_" + strings.Repeat("a", 52), Unknown},
		"empty":             {"", Unknown},
		"wrong length":      {valid.Value + "a", Unknown},
		"wrong alphabet":    {"mstr_int_" + strings.Repeat("1", 52), Unknown},
		"personal token":    {pat, Unknown},
		"in the wrong case": {strings.ToUpper(valid.Value), Unknown},
	} {
		caller, err := svc.Authenticate(t.Context(), tt.value)
		if !errors.Is(err, ErrInvalidToken) || caller.Integration != tt.label {
			t.Errorf("%s = %+v, %v; want ErrInvalidToken labelled %s", name, caller, err, tt.label)
		}
	}
	store.fail["FindIngestToken"] = errors.New("down")
	if caller, err := svc.Authenticate(t.Context(), valid.Value); err == nil || errors.Is(err, ErrInvalidToken) ||
		caller.Integration != Unknown {
		t.Errorf("failing lookup = %+v, %v", caller, err)
	}
	delete(store.fail, "FindIngestToken")

	// The use is written off the request path, at most once a minute.
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		svc.RunTouches(ctx)
		close(done)
	}()
	waitFor(t, func() bool {
		used, _ := store.usedAt()
		return len(svc.touches.queue) == 0 && used != nil
	})
	cancel()
	<-done
	for range 3 {
		if _, err := svc.Authenticate(t.Context(), valid.Value); err != nil {
			t.Fatal(err)
		}
	}
	if _, touched := store.usedAt(); len(svc.touches.queue) != 0 || touched != 1 {
		t.Errorf("queued %d, touched %d; want no write within the minute", len(svc.touches.queue), touched)
	}
	c.Advance(time.Minute)
	_, _ = svc.Authenticate(t.Context(), valid.Value)
	_, _ = svc.Authenticate(t.Context(), valid.Value)
	if len(svc.touches.queue) != 1 {
		t.Errorf("queued %d after a minute, want 1", len(svc.touches.queue))
	}
	// A full queue drops a use, which a later use queues again.
	full := newTouches()
	for i := range touchQueue + 1 {
		full.use(int64(i), t0)
	}
	if len(full.queue) != touchQueue || len(full.sent) != touchQueue {
		t.Errorf("full queue %d sent %d", len(full.queue), len(full.sent))
	}
}

func TestDecodeLabels(t *testing.T) {
	if _, err := decodeLabels([]byte(`{"a":1}`)); err == nil {
		t.Error("a number label value decoded")
	}
	if b, err := encodeLabels(nil); err != nil || string(b) != "{}" {
		t.Errorf("nil labels = %s, %v", b, err)
	}
	if escapePointer("a~b/c") != "a~0b~1c" {
		t.Error(escapePointer("a~b/c"))
	}
}

// TestBuiltin is C-06.FR-14: the ensure step creates the built-in Integration once, it is listed marked builtin, and
// changing, deleting it or giving it a token is ErrBuiltinImmutable.
func TestBuiltin(t *testing.T) {
	svc, store, _, _ := newService(t)
	ctx := t.Context()
	if err := EnsureBuiltin(ctx, store, orgID, t0); err != nil {
		t.Fatal(err)
	}
	if err := EnsureBuiltin(ctx, store, orgID, t0.Add(time.Hour)); err != nil || len(store.rows) != 1 {
		t.Fatalf("a second start: %v, %d rows", err, len(store.rows))
	}
	page, err := svc.List(ctx, ListFilter{Limit: 10})
	if err != nil || len(page.Integrations) != 1 {
		t.Fatalf("list %+v, %v", page, err)
	}
	b := page.Integrations[0]
	if !b.Builtin || b.Name != BuiltinName || b.Heartbeat.Enabled || len(b.StaticLabels) != 0 ||
		b.DuplicateWindowSeconds != 45 || len(b.Warnings) != 0 {
		t.Errorf("built-in %+v", b)
	}
	if _, err := svc.Update(ctx, by, b.PublicID, nil, input("other")); !errors.Is(err, ErrBuiltinImmutable) {
		t.Errorf("update = %v", err)
	}
	if err := svc.Delete(ctx, by, b.PublicID, nil); !errors.Is(err, ErrBuiltinImmutable) {
		t.Errorf("delete = %v", err)
	}
	if _, err := svc.CreateToken(ctx, by, b.PublicID, ""); !errors.Is(err, ErrBuiltinImmutable) {
		t.Errorf("token = %v", err)
	}
	if len(store.audit) != 0 || len(store.tokens) != 0 || len(store.hints) != 0 {
		t.Errorf("a refusal wrote %+v %+v %+v", store.audit, store.tokens, store.hints)
	}

	// An Integration that already has the name stops the start; another failure is reported.
	other := newStore()
	if _, err := other.InsertIntegration(ctx, dbgen.InsertIntegrationParams{Name: BuiltinName,
		PublicID: "NTAAAAAAAAAAAA"}); err != nil {
		t.Fatal(err)
	}
	if err := EnsureBuiltin(ctx, other, orgID, t0); !errors.Is(err, ErrNameTaken) {
		t.Errorf("name taken = %v", err)
	}
	other = newStore()
	other.fail["EnsureBuiltin"] = errors.New("down")
	if err := EnsureBuiltin(ctx, other, orgID, t0); err == nil {
		t.Error("a failure was ignored")
	}
}

// TestWarnings is C-06.FR-18: every read of an Integration carries snapshot_truncated while a groupKey is truncated
// and one long_repeat_interval per route above processing.long_repeat_warning.
func TestWarnings(t *testing.T) {
	svc, store, _, _ := newService(t)
	ctx := t.Context()
	a, err := svc.Create(ctx, by, input("a"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.Create(ctx, by, input("b"))
	if err != nil {
		t.Fatal(err)
	}
	store.truncated[a.ID] = 2
	store.routes = []dbgen.ListLongRepeatRoutesRow{
		{IntegrationID: a.ID, RoutePath: `{}/{kind="info"}`, LearnedRepeatIntervalMs: 7_200_400},
		{IntegrationID: b.ID, RoutePath: "{}", LearnedRepeatIntervalMs: 3_600_000},
		{IntegrationID: b.ID, RoutePath: `{}/{x="y"}`, LearnedRepeatIntervalMs: 86_400_000},
	}
	got, err := svc.Get(ctx, a.PublicID)
	want := []Warning{{Kind: WarningHeartbeatNotConfigured}, {Kind: WarningSnapshotTruncated, TruncatedGroupCount: 2},
		{Kind: WarningLongRepeatInterval, RoutePath: `{}/{kind="info"}`, RepeatIntervalSeconds: 7200}}
	if err != nil || !slices.Equal(got.Warnings, want) {
		t.Errorf("warnings of a = %+v, %v", got.Warnings, err)
	}
	page, err := svc.List(ctx, ListFilter{Limit: 10})
	if err != nil || len(page.Integrations[1].Warnings) != 2 || page.Integrations[1].Warnings[1].RoutePath != `{}/{x="y"}` {
		t.Errorf("list %+v, %v", page, err)
	}
	for _, name := range []string{"CountTruncatedGroupsOf", "ListLongRepeatRoutes"} {
		store.fail = map[string]error{name: errors.New("down")}
		if _, err := svc.Get(ctx, a.PublicID); err == nil {
			t.Errorf("%s failing: no error", name)
		}
	}
}

// TestRenameRaisesInternalAlerts is C-06.AC-11: a rename raises every open Internal alert about the Integration again
// with the new name, in the same transaction; a change that keeps the name raises nothing.
func TestRenameRaisesInternalAlerts(t *testing.T) {
	svc, store, c, _ := newService(t)
	ctx := t.Context()
	if err := EnsureBuiltin(ctx, store, orgID, t0); err != nil {
		t.Fatal(err)
	}
	in, err := svc.Create(ctx, by, input("lab"))
	if err != nil {
		t.Fatal(err)
	}
	started := t0.Add(-time.Hour)
	store.open = []idb.ListOpenInternalAlertsRow{{Fingerprint: "f", StartsAt: started,
		Labels: []byte(`{"alertname":"MusterSnapshotTruncated","severity":"warning","integration":"` + in.PublicID +
			`","integration_name":"lab"}`)}}
	c.Advance(time.Minute)
	keep := input("lab")
	keep.DuplicateWindowSeconds = 60
	if _, err := svc.Update(ctx, by, in.PublicID, nil, keep); err != nil || len(store.snapshots) != 0 {
		t.Fatalf("an update that keeps the name: %v, %d", err, len(store.snapshots))
	}
	if _, err := svc.Update(ctx, by, in.PublicID, nil, input("lab-eu")); err != nil {
		t.Fatal(err)
	}
	if len(store.snapshots) != 1 || store.snapshots[0].integrationID != store.rows[0].ID ||
		!store.snapshots[0].receivedAt.Equal(c.Now()) {
		t.Fatalf("snapshots %+v", store.snapshots)
	}
	var w struct {
		Alerts []struct {
			Labels   map[string]string `json:"labels"`
			StartsAt time.Time         `json:"startsAt"`
		} `json:"alerts"`
	}
	if err := json.Unmarshal(store.snapshots[0].body, &w); err != nil || w.Alerts[0].Labels["integration_name"] !=
		"lab-eu" || !w.Alerts[0].StartsAt.Equal(started) {
		t.Errorf("raise %s, %v", store.snapshots[0].body, err)
	}
	store.fail["ListOpenInternalAlerts"] = errors.New("down")
	if _, err := svc.Update(ctx, by, in.PublicID, nil, input("lab-us")); err == nil {
		t.Error("a failed raise was ignored")
	}
}

// TestDeleteMarksDeletion is C-06.FR-16: the deletion writes its marker into the Integration's own queue in the same
// transaction.
func TestDeleteMarksDeletion(t *testing.T) {
	svc, store, c, _ := newService(t)
	ctx := t.Context()
	in, err := svc.Create(ctx, by, input("lab"))
	if err != nil {
		t.Fatal(err)
	}
	c.Advance(time.Minute)
	if err := svc.Delete(ctx, by, in.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	if len(store.snapshots) != 1 || store.snapshots[0].integrationID != in.ID ||
		!store.snapshots[0].receivedAt.Equal(c.Now()) ||
		!strings.Contains(string(store.snapshots[0].body), `"summary":"Integration lab deleted"`) {
		t.Errorf("snapshots %+v", store.snapshots)
	}
	other, err := svc.Create(ctx, by, input("other"))
	if err != nil {
		t.Fatal(err)
	}
	store.fail["InsertInternalSnapshot"] = errors.New("down")
	if err := svc.Delete(ctx, by, other.PublicID, nil); err == nil {
		t.Error("a failed marker was ignored")
	}
}

// TestHeartbeatSettings covers C-07.FR-2, C-07.FR-3 and C-07.FR-5 on the settings: the Heartbeat is off by default
// with the warning heartbeat_not_configured; turned on it waits for its first signal with
// integration.heartbeat_timeout and the warning heartbeat_waiting; lost it warns since its last signal; turned off it
// is not configured again, no longer lost, and its MusterHeartbeatLost resolves; a token issued while it is on says so
// for the Heartbeat snippet. The built-in Integration has no Heartbeat warning.
func TestHeartbeatSettings(t *testing.T) {
	svc, store, c, _ := newService(t)
	ctx := t.Context()
	if err := EnsureBuiltin(ctx, store, orgID, t0); err != nil {
		t.Fatal(err)
	}
	in, err := svc.Create(ctx, by, input("hb"))
	if err != nil {
		t.Fatal(err)
	}
	if in.Heartbeat.Enabled || in.Heartbeat.State != HeartbeatNotConfigured ||
		!slices.Equal(in.Warnings, []Warning{{Kind: WarningHeartbeatNotConfigured}}) {
		t.Fatalf("created %+v", in)
	}
	created, err := svc.CreateToken(ctx, by, in.PublicID, "")
	if err != nil || created.Heartbeat || created.Integration != "hb" {
		t.Fatalf("token without a heartbeat %+v, %v", created, err)
	}

	on := input("hb")
	on.Heartbeat.Enabled = true
	got, err := svc.Update(ctx, by, in.PublicID, nil, on)
	if err != nil || !got.Heartbeat.Enabled || got.Heartbeat.State != HeartbeatWaiting ||
		got.Heartbeat.TimeoutSeconds != 300 || !slices.Equal(got.Warnings, []Warning{{Kind: WarningHeartbeatWaiting}}) ||
		got.Version != in.Version+1 {
		t.Fatalf("turned on %+v, %v", got, err)
	}
	if e, diff := store.lastAudit(t); e.Action != "integration.updated" || len(diff) != 1 ||
		diff[0].Pointer != "/heartbeat/enabled" {
		t.Errorf("audit %s %+v", e.Action, diff)
	}
	if created, err := svc.CreateToken(ctx, by, in.PublicID, ""); err != nil || !created.Heartbeat {
		t.Errorf("token with the heartbeat on %+v, %v", created, err)
	}

	r := store.byID(in.ID)
	r.HeartbeatState = HeartbeatLost
	r.HeartbeatLastSignalAt = pgtype.Timestamptz{Time: t0, Valid: true}
	r.HeartbeatLostSince = r.HeartbeatLastSignalAt
	lost, err := svc.Get(ctx, in.PublicID)
	if err != nil || len(lost.Warnings) != 1 || lost.Warnings[0].Kind != WarningHeartbeatLost ||
		!lost.Warnings[0].Since.Equal(t0) {
		t.Fatalf("lost %+v, %v", lost, err)
	}
	c.Advance(time.Minute)
	off, err := svc.Update(ctx, by, in.PublicID, nil, input("hb"))
	if err != nil || off.Heartbeat.Enabled || off.Heartbeat.State != HeartbeatNotConfigured ||
		off.Heartbeat.LostSince != nil {
		t.Fatalf("turned off %+v, %v", off, err)
	}
	if len(store.snapshots) != 1 || !strings.Contains(string(store.snapshots[0].body),
		`"alertname":"MusterHeartbeatLost"`) || !strings.Contains(string(store.snapshots[0].body), `"status":"resolved"`) {
		t.Errorf("snapshots %+v", store.snapshots)
	}
	// Turning a Heartbeat off that was not lost resolves nothing.
	if _, err := svc.Update(ctx, by, in.PublicID, nil, on); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(ctx, by, in.PublicID, nil, input("hb")); err != nil || len(store.snapshots) != 1 {
		t.Errorf("turned off while waiting: %v, %d snapshots", err, len(store.snapshots))
	}
	created2, err := svc.Create(ctx, by, on)
	if err == nil || !errors.Is(err, ErrNameTaken) {
		t.Errorf("a second hb = %+v, %v", created2, err)
	}
	on.Name = "hb-2"
	if created2, err = svc.Create(ctx, by, on); err != nil || created2.Heartbeat.State != HeartbeatWaiting {
		t.Errorf("created with the heartbeat on %+v, %v", created2, err)
	}
	page, err := svc.List(ctx, ListFilter{Limit: 10})
	if err != nil || page.Integrations[0].Name != BuiltinName || len(page.Integrations[0].Warnings) != 0 {
		t.Errorf("the built-in integration %+v, %v", page.Integrations[0], err)
	}
	r = store.byID(in.ID)
	r.HeartbeatEnabled, r.HeartbeatState = true, HeartbeatLost
	r.HeartbeatLostSince = pgtype.Timestamptz{Time: t0, Valid: true}
	store.fail["InsertInternalSnapshot"] = errors.New("down")
	if _, err := svc.Update(ctx, by, in.PublicID, nil, input("hb")); err == nil {
		t.Error("a failed resolve was ignored")
	}
}

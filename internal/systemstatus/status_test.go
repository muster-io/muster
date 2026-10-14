// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package systemstatus

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/systemstatus/dbgen"
)

var now = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

func at(d time.Duration) pgtype.Timestamptz { return pgtype.Timestamptz{Time: now.Add(d), Valid: true} }

// fakeQueries answers every read from its fields; fail makes the named read fail.
type fakeQueries struct {
	runtime   *dbgen.GetRuntimeStateRow
	downtime  *dbgen.LatestDowntimeRow
	queues    []dbgen.ListDeliveryQueuesRow
	queueArg  dbgen.ListDeliveryQueuesParams
	broken    []dbgen.ListBrokenDestinationsRow
	attention []dbgen.ListIntegrationsAttentionRow
	templates []dbgen.ListTemplateErrorsRow
	storms    []dbgen.ListActiveStormsRow
	heartbeat dbgen.GetOutgoingHeartbeatResultRow
	orgs      []int64
	fail      map[string]error
}

func (f *fakeQueries) GetRuntimeState(context.Context) (dbgen.GetRuntimeStateRow, error) {
	if err := f.fail["GetRuntimeState"]; err != nil {
		return dbgen.GetRuntimeStateRow{}, err
	}
	if f.runtime == nil {
		return dbgen.GetRuntimeStateRow{}, pgx.ErrNoRows
	}
	return *f.runtime, nil
}

func (f *fakeQueries) LatestDowntime(context.Context) (dbgen.LatestDowntimeRow, error) {
	if err := f.fail["LatestDowntime"]; err != nil {
		return dbgen.LatestDowntimeRow{}, err
	}
	if f.downtime == nil {
		return dbgen.LatestDowntimeRow{}, pgx.ErrNoRows
	}
	return *f.downtime, nil
}

func (f *fakeQueries) ListDeliveryQueues(_ context.Context, arg dbgen.ListDeliveryQueuesParams) (
	[]dbgen.ListDeliveryQueuesRow, error) {
	f.queueArg = arg
	return f.queues, f.fail["ListDeliveryQueues"]
}

func (f *fakeQueries) ListBrokenDestinations(_ context.Context, org int64) ([]dbgen.ListBrokenDestinationsRow, error) {
	f.orgs = append(f.orgs, org)
	return f.broken, f.fail["ListBrokenDestinations"]
}

func (f *fakeQueries) ListIntegrationsAttention(_ context.Context, org int64) (
	[]dbgen.ListIntegrationsAttentionRow, error) {
	f.orgs = append(f.orgs, org)
	return f.attention, f.fail["ListIntegrationsAttention"]
}

func (f *fakeQueries) ListTemplateErrors(_ context.Context, org int64) ([]dbgen.ListTemplateErrorsRow, error) {
	f.orgs = append(f.orgs, org)
	return f.templates, f.fail["ListTemplateErrors"]
}

func (f *fakeQueries) ListActiveStorms(_ context.Context, org int64) ([]dbgen.ListActiveStormsRow, error) {
	f.orgs = append(f.orgs, org)
	return f.storms, f.fail["ListActiveStorms"]
}

func (f *fakeQueries) GetOutgoingHeartbeatResult(_ context.Context, org int64) (dbgen.GetOutgoingHeartbeatResultRow,
	error) {
	f.orgs = append(f.orgs, org)
	return f.heartbeat, f.fail["GetOutgoingHeartbeatResult"]
}

var liveReplicas = []keyring.Replica{
	{ID: "muster-a", KeyIDs: []string{"k-1", "k-2"}, RefreshedAt: now.Add(-10 * time.Second)},
	{ID: "muster-b", RefreshedAt: now.Add(-20 * time.Second)},
}

func service(f *fakeQueries, replicasErr error) *Service {
	return New(Config{OrgID: 7, Queries: f, Business: clock.NewManual(now), LeaderAbsence: 2 * time.Minute,
		Version: "1.4.0", Commit: "abc1234",
		Replicas: func(context.Context) ([]keyring.Replica, error) { return liveReplicas, replicasErr }})
}

func filled() *fakeQueries {
	return &fakeQueries{
		runtime: &dbgen.GetRuntimeStateRow{LeaderReplicaID: pgtype.Text{String: "muster-a", Valid: true},
			AliveAt: at(-30 * time.Second), RecoveryUntil: at(10 * time.Minute)},
		downtime: &dbgen.LatestDowntimeRow{StartedAt: now.Add(-time.Hour), EndedAt: now.Add(-50 * time.Minute)},
		queues:   []dbgen.ListDeliveryQueuesRow{{PublicID: "DSAAAAAAAAAAAA", Name: "alerts", Queued: 3}},
		broken: []dbgen.ListBrokenDestinationsRow{{PublicID: "DSAAAAAAAAAAAA", Name: "alerts",
			BrokenSince: now.Add(-time.Minute), Reason: "answered 403"}},
		attention: []dbgen.ListIntegrationsAttentionRow{
			{PublicID: "NTAAAAAAAAAAAA", Name: "lab", Issue: IssueHeartbeatLost, Since: at(-5 * time.Minute)},
			{PublicID: "NTAAAAAAAAAAAB", Name: "eu", Issue: IssueSnapshotTruncated},
		},
		templates: []dbgen.ListTemplateErrorsRow{{Kind: SubjectRoute, PublicID: "RTAAAAAAAAAAAA", Name: "t",
			Template: "root_message", Since: now.Add(-time.Minute), Error: "line 1: boom"}},
		storms: []dbgen.ListActiveStormsRow{{PublicID: "RTAAAAAAAAAAAA", Name: "t", StartedAt: now.Add(-time.Minute),
			AlertGroupCount: 12}},
		heartbeat: dbgen.GetOutgoingHeartbeatResultRow{Configured: true, OutgoingHeartbeatLastAttemptAt: at(-time.Minute),
			OutgoingHeartbeatLastOk:    pgtype.Bool{Bool: false, Valid: true},
			OutgoingHeartbeatLastError: pgtype.Text{String: "answered 503 (transient)", Valid: true}},
		fail: map[string]error{},
	}
}

// TestGetFillsEverySection covers C-19.FR-10: every section of the System status from its reads, per Organization.
func TestGetFillsEverySection(t *testing.T) {
	f := filled()
	st, err := service(f, nil).Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != "1.4.0" || st.Commit != "abc1234" || len(st.Replicas) != 2 || !st.Replicas[0].Leader ||
		st.Replicas[1].Leader || len(st.Replicas[0].KeyIDs) != 2 || st.Replicas[1].KeyIDs == nil ||
		!st.Replicas[0].RefreshedAt.Equal(now.Add(-10*time.Second)) {
		t.Errorf("replicas %+v", st.Replicas)
	}
	r := st.Recovery
	if !r.Active || !r.Until.Equal(now.Add(10*time.Minute)) || !r.PeriodFrom.Equal(now.Add(-time.Hour)) ||
		!r.PeriodTo.Equal(now.Add(-50*time.Minute)) {
		t.Errorf("recovery %+v", r)
	}
	if len(st.DeliveryQueues) != 1 || st.DeliveryQueues[0] != (Queue{Entity{"DSAAAAAAAAAAAA", "alerts"}, 3}) ||
		f.queueArg != (dbgen.ListDeliveryQueuesParams{OrgID: 7, Now: now}) {
		t.Errorf("queues %+v, arg %+v", st.DeliveryQueues, f.queueArg)
	}
	if b := st.BrokenDestinations; len(b) != 1 || b[0].Destination.Name != "alerts" || b[0].Reason != "answered 403" ||
		!b[0].Since.Equal(now.Add(-time.Minute)) {
		t.Errorf("broken %+v", b)
	}
	if a := st.IntegrationsAttention; len(a) != 2 || a[0].Issue != IssueHeartbeatLost ||
		!a[0].Since.Equal(now.Add(-5*time.Minute)) || a[1].Since != nil || a[1].Integration.ID != "NTAAAAAAAAAAAB" {
		t.Errorf("attention %+v", a)
	}
	if e := st.TemplateErrors; len(e) != 1 || e[0].SubjectKind != SubjectRoute || e[0].Template != "root_message" ||
		e[0].Error != "line 1: boom" || e[0].Subject.Name != "t" {
		t.Errorf("template errors %+v", e)
	}
	if s := st.ActiveStorms; len(s) != 1 || s[0].AlertGroupCount != 12 || s[0].Route.ID != "RTAAAAAAAAAAAA" {
		t.Errorf("storms %+v", s)
	}
	h := st.OutgoingHeartbeat
	if !h.Configured || *h.LastOK || *h.LastError != "answered 503 (transient)" || h.LastErrorClass != "transient" ||
		h.LastStatus != 503 || !h.LastAttemptAt.Equal(now.Add(-time.Minute)) {
		t.Errorf("outgoing heartbeat %+v", h)
	}
	for _, org := range f.orgs {
		if org != 7 {
			t.Errorf("a read of organization %d", org)
		}
	}
}

// TestLeaderFlagNeedsFreshAliveMark: the Leader of runtime_state is flagged only while its alive mark is younger than
// leader.absence_notice; without a Leader or a runtime row, none is.
func TestLeaderFlagNeedsFreshAliveMark(t *testing.T) {
	f := filled()
	f.runtime.AliveAt = at(-2*time.Minute - time.Second)
	st, err := service(f, nil).Get(t.Context())
	if err != nil || st.Replicas[0].Leader {
		t.Errorf("a stale alive mark: %+v, %v", st.Replicas, err)
	}
	f.runtime = nil
	st, err = service(f, nil).Get(t.Context())
	if err != nil || st.Replicas[0].Leader || st.Recovery.Active {
		t.Errorf("without a runtime row: %+v, %v", st, err)
	}
}

// TestRecoveryActiveAndOver: past recovery_until the recovery is not active and has no end, the last downtime still
// shown; without a downtime the period is null.
func TestRecoveryActiveAndOver(t *testing.T) {
	f := filled()
	f.runtime.RecoveryUntil = at(-time.Second)
	st, err := service(f, nil).Get(t.Context())
	if err != nil || st.Recovery.Active || st.Recovery.Until != nil || st.Recovery.PeriodFrom == nil {
		t.Errorf("over: %+v, %v", st.Recovery, err)
	}
	f.downtime = nil
	st, err = service(f, nil).Get(t.Context())
	if err != nil || st.Recovery.PeriodFrom != nil || st.Recovery.PeriodTo != nil {
		t.Errorf("no downtime: %+v, %v", st.Recovery, err)
	}
}

// TestHeartbeatNotConfigured: without a URL the result is configured false and nothing else, even when an old result
// is stored; with a URL that was never sent, every field is null.
func TestHeartbeatNotConfigured(t *testing.T) {
	f := filled()
	f.heartbeat.Configured = false
	st, err := service(f, nil).Get(t.Context())
	if h := st.OutgoingHeartbeat; err != nil || h.Configured || h.LastAttemptAt != nil || h.LastOK != nil ||
		h.LastError != nil || h.LastErrorClass != "" || h.LastStatus != 0 {
		t.Errorf("not configured: %+v, %v", h, err)
	}
	f.heartbeat = dbgen.GetOutgoingHeartbeatResultRow{Configured: true}
	st, err = service(f, nil).Get(t.Context())
	if h := st.OutgoingHeartbeat; err != nil || !h.Configured || h.LastAttemptAt != nil || h.LastOK != nil {
		t.Errorf("never sent: %+v, %v", h, err)
	}
}

func TestErrorsPropagate(t *testing.T) {
	down := errors.New("down")
	for _, name := range []string{"GetRuntimeState", "LatestDowntime", "ListDeliveryQueues", "ListBrokenDestinations",
		"ListIntegrationsAttention", "ListTemplateErrors", "ListActiveStorms", "GetOutgoingHeartbeatResult"} {
		f := filled()
		f.fail[name] = down
		if _, err := service(f, nil).Get(t.Context()); !errors.Is(err, down) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := service(filled(), down).Get(t.Context()); !errors.Is(err, down) {
		t.Errorf("replicas: %v", err)
	}
	if NewQueries(nil) == nil {
		t.Error("NewQueries returned nil")
	}
}

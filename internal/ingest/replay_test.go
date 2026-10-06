// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/audit"
	auditdb "github.com/muster-io/muster/internal/audit/dbgen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/ingest/dbgen"
	"github.com/muster-io/muster/internal/logging"
)

// fakeReplay is the database of a replay in memory.
type fakeReplay struct {
	integrations map[string]dbgen.FindReplayIntegrationRow
	replayed     []int64
	params       []dbgen.ReplaySnapshotsParams
	notified     []string
	audited      []auditdb.InsertAuditEntryParams
	fail         map[string]error
}

func (f *fakeReplay) InTx(_ context.Context, fn func(ReplayQueries) error) error { return fn(f) }

func (f *fakeReplay) GetRetention(context.Context, int64) (int64, error) {
	return 14, f.fail["GetRetention"]
}

func (f *fakeReplay) FindReplayIntegration(_ context.Context, arg dbgen.FindReplayIntegrationParams) (
	dbgen.FindReplayIntegrationRow, error) {
	if err := f.fail["FindReplayIntegration"]; err != nil {
		return dbgen.FindReplayIntegrationRow{}, err
	}
	in, ok := f.integrations[arg.Name]
	if !ok || arg.OrgID != 1 {
		return in, pgx.ErrNoRows
	}
	return in, nil
}

func (f *fakeReplay) ReplaySnapshots(_ context.Context, arg dbgen.ReplaySnapshotsParams) ([]int64, error) {
	f.params = append(f.params, arg)
	return f.replayed, f.fail["ReplaySnapshots"]
}

func (f *fakeReplay) NotifySnapshot(_ context.Context, arg dbgen.NotifySnapshotParams) error {
	f.notified = append(f.notified, arg.Channel+" "+arg.Payload)
	return f.fail["NotifySnapshot"]
}

func (f *fakeReplay) InsertAuditEntry(_ context.Context, arg auditdb.InsertAuditEntryParams) error {
	f.audited = append(f.audited, arg)
	return f.fail["InsertAuditEntry"]
}

func newReplayer(f *fakeReplay, log *bytes.Buffer, now time.Time) *Replayer {
	c := clock.NewManual(now)
	l := logging.New(log, logging.LevelInfo)
	return &Replayer{OrgID: 1, Store: f, Audit: audit.NewWriter(l, c), Log: l, Business: c}
}

// TestReplay is C-06.FR-17 and C-02.FR-15: the Stored Snapshots of the period, limited to retention.stored_snapshots
// and to one Integration when named, go back to pending in one transaction; each Integration's workers are woken
// once; ingest.replayed is recorded with the actor, the Transport cli and the details; ingest_replayed is logged.
func TestReplay(t *testing.T) {
	now := t0.Add(time.Hour)
	f := &fakeReplay{integrations: map[string]dbgen.FindReplayIntegrationRow{
		"lab-eu": {ID: 5, PublicID: "NTAAAAAAAAAAAA", Name: "lab-eu"}}, replayed: []int64{5, 5, 5}, fail: map[string]error{}}
	var log bytes.Buffer
	r := newReplayer(f, &log, now)
	out, err := r.Replay(t.Context(), Replay{Since: time.Hour, SinceText: "1h", Integration: "lab-eu",
		Actor: " ops-alice "})
	if err != nil || out.Count != 3 || out.Integration == nil || *out.Integration != (Ref{PublicID: "NTAAAAAAAAAAAA",
		Name: "lab-eu"}) {
		t.Fatalf("replay = %+v, %v", out, err)
	}
	p := f.params[0]
	if !p.Since.Equal(t0) || !p.ReplayedAt.Equal(now) || !p.IntegrationID.Valid || p.IntegrationID.Int64 != 5 ||
		p.OrgID != 1 {
		t.Errorf("params %+v", p)
	}
	if !slices.Equal(f.notified, []string{`muster_snapshots {"org_id":1,"integration_id":5}`}) {
		t.Errorf("notified %v", f.notified)
	}
	a := f.audited[0]
	var details map[string]any
	if err := json.Unmarshal(a.Details, &details); err != nil {
		t.Fatal(err)
	}
	if a.Action != ActionReplayed || a.ActorKind != "cli" || a.ActorName.String != "ops-alice" || a.Transport != "cli" ||
		a.ResourceType.String != "integration" || a.ResourcePublicID.String != "NTAAAAAAAAAAAA" ||
		a.ResourceName.String != "lab-eu" || details["since"] != "1h" || details["integration"] != "NTAAAAAAAAAAAA" ||
		details["count"] != 3.0 {
		t.Errorf("audit %+v, %v", a, details)
	}
	if !strings.Contains(log.String(), `"event":"ingest_replayed","actor":"ops-alice","since":"1h",`+
		`"integration":"NTAAAAAAAAAAAA","count":3`) {
		t.Errorf("log %s", log.String())
	}

	// Every Integration, a period longer than the retention of Stored Snapshots.
	f.replayed, f.audited, f.notified = []int64{6, 5, 6}, nil, nil
	log.Reset()
	out, err = r.Replay(t.Context(), Replay{Since: 30 * 24 * time.Hour, SinceText: "720h", Actor: "ops-alice"})
	if err != nil || out.Count != 3 || out.Integration != nil {
		t.Fatalf("replay all = %+v, %v", out, err)
	}
	if p := f.params[1]; !p.Since.Equal(now.Add(-14*24*time.Hour)) || p.IntegrationID.Valid {
		t.Errorf("params %+v", p)
	}
	if len(f.notified) != 2 {
		t.Errorf("notified %v", f.notified)
	}
	if err := json.Unmarshal(f.audited[0].Details, &details); err != nil || details["integration"] != nil ||
		f.audited[0].ResourceType.Valid {
		t.Errorf("audit %+v, %v", f.audited[0], details)
	}
	if !strings.Contains(log.String(), `"integration":"","count":3`) {
		t.Errorf("log %s", log.String())
	}
}

func TestReplayRefusals(t *testing.T) {
	f := &fakeReplay{integrations: map[string]dbgen.FindReplayIntegrationRow{}, fail: map[string]error{}}
	var log bytes.Buffer
	r := newReplayer(f, &log, t0)
	for _, tt := range []struct {
		name string
		req  Replay
		want string
	}{
		{"no actor", Replay{Since: time.Hour, Actor: " "}, "the actor is required"},
		{"no period", Replay{Since: 0, Actor: "ops-alice"}, "the period is not positive"},
		{"unknown integration", Replay{Since: time.Hour, Integration: "nope", Actor: "ops-alice"},
			"no integration has this name: nope"},
	} {
		if _, err := r.Replay(t.Context(), tt.req); err == nil || err.Error() != tt.want {
			t.Errorf("%s: %v", tt.name, err)
		}
	}
	if len(f.params) != 0 || len(f.audited) != 0 {
		t.Errorf("a refused replay changed something: %+v %+v", f.params, f.audited)
	}
	if _, err := r.Replay(t.Context(), Replay{Since: time.Hour, Integration: "nope", Actor: "x"}); !errors.Is(err,
		ErrUnknownIntegration) {
		t.Errorf("unknown = %v", err)
	}
	for _, name := range []string{"FindReplayIntegration", "GetRetention", "ReplaySnapshots", "NotifySnapshot",
		"InsertAuditEntry"} {
		f := &fakeReplay{integrations: map[string]dbgen.FindReplayIntegrationRow{"a": {ID: 1}}, replayed: []int64{1},
			fail: map[string]error{name: errors.New("down")}}
		var log bytes.Buffer
		if _, err := newReplayer(f, &log, t0).Replay(t.Context(), Replay{Since: time.Hour, Integration: "a",
			Actor: "x"}); err == nil || strings.Contains(log.String(), "ingest_replayed") {
			t.Errorf("%s failing: %v, %s", name, err, log.String())
		}
	}
}

// TestReplayCountsOnce is decision D257: a replayed Stored Snapshot is not counted on its Integration again.
func TestReplayCountsOnce(t *testing.T) {
	store := newFakeProcess()
	var log bytes.Buffer
	p := newTestProcessor(store, clock.NewManual(t0), &log, nil)
	store.add(1, t0, body(wireAlertOf("db-a", "firing")))
	if _, err := p.ProcessPending(t.Context(), 5); err != nil || store.counted != 1 {
		t.Fatalf("first processing counted %d, %v", store.counted, err)
	}
	store.replayed = true
	store.add(1, t0, body(wireAlertOf("db-a", "firing")))
	store.add(2, t0.Add(time.Second), "not json")
	if _, err := p.ProcessPending(t.Context(), 5); err != nil || store.counted != 1 || len(store.finished) != 3 {
		t.Errorf("replayed processing counted %d, finished %d, %v", store.counted, len(store.finished), err)
	}
}

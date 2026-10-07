// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/ingest/dbgen"
	idb "github.com/muster-io/muster/internal/internalalerts/dbgen"
	"github.com/muster-io/muster/internal/metrics"
)

// TestEffectiveClock covers the liveness clock of schema.md 4.7: it runs only while the Heartbeat is live, and only
// for a gap since the last signal of at most the timeout.
func TestEffectiveClock(t *testing.T) {
	last, timeout := t0, 5*time.Minute
	for name, tt := range map[string]struct {
		state string
		last  *time.Time
		at    time.Time
		want  int64
	}{
		"live, a minute after the signal":   {state: "live", last: &last, at: t0.Add(time.Minute), want: 1000 + 60_000},
		"live, at the timeout":              {state: "live", last: &last, at: t0.Add(timeout), want: 1000 + 300_000},
		"live, past the timeout":            {state: "live", last: &last, at: t0.Add(timeout + time.Millisecond), want: 1000},
		"live, before the last signal":      {state: "live", last: &last, at: t0.Add(-time.Second), want: 1000},
		"lost":                              {state: "lost", last: &last, at: t0.Add(time.Minute), want: 1000},
		"waiting":                           {state: "waiting", at: t0.Add(time.Minute), want: 1000},
		"not configured":                    {state: "not_configured", at: t0, want: 1000},
		"live without a signal, impossible": {state: "live", at: t0, want: 1000},
	} {
		if got := EffectiveClock(1000, tt.state, tt.last, timeout, tt.at); got != tt.want {
			t.Errorf("%s: EffectiveClock = %d, want %d", name, got, tt.want)
		}
	}
}

// fakeStale is the database of the Stale scan in memory: the live Integrations, their claim and lease, the Stale
// presences and the Alerts they resolve.
type fakeStale struct {
	ProcessQueries
	live      []int64
	claimable []int64
	info      dbgen.RenewIngestLeaseRow
	lost      bool
	stale     []dbgen.ListStalePresencesRow
	resolved  []dbgen.ResolveStaleAlertsRow
	expired   int64
	truncated int64
	pending   bool
	fail      map[string]error

	ensured  []int64
	released []int64
	params   []dbgen.ListStalePresencesParams
	marked   []dbgen.MarkPresencesStaleParams
	resolves []dbgen.ResolveStaleAlertsParams
	expires  []dbgen.ExpireTruncationParams
	internal int
}

func newFakeStale() *fakeStale {
	last := t0.Add(-time.Minute)
	return &fakeStale{live: []int64{5}, claimable: []int64{5}, fail: map[string]error{},
		info: dbgen.RenewIngestLeaseRow{PublicID: "NTAAAAAAAAAAAA", Name: "hb", LivenessClockMs: 1_000_000,
			HeartbeatState: "live", HeartbeatLastSignalAt: pgtype.Timestamptz{Time: last, Valid: true},
			HeartbeatTimeoutSeconds: 300}}
}

func (f *fakeStale) InTx(_ context.Context, fn func(ProcessQueries, dbgen.DBTX) error) error {
	return fn(f, nil)
}

func (f *fakeStale) ListLiveIntegrations(_ context.Context, orgID int64) ([]int64, error) {
	if orgID != 1 {
		return nil, errors.New("another organization")
	}
	return f.live, f.fail["ListLiveIntegrations"]
}

func (f *fakeStale) GetRetention(context.Context, int64) (int64, error) {
	return 14, f.fail["GetRetention"]
}

func (f *fakeStale) HasPendingSnapshots(_ context.Context, arg dbgen.HasPendingSnapshotsParams) (bool, error) {
	if !arg.Horizon.Equal(t0.Add(-14 * 24 * time.Hour)) {
		return false, errors.New("another horizon")
	}
	return f.pending, f.fail["HasPendingSnapshots"]
}

func (f *fakeStale) EnsureIngestClaims(_ context.Context, arg dbgen.EnsureIngestClaimsParams) error {
	f.ensured = append(f.ensured, arg.IntegrationIds...)
	return f.fail["EnsureIngestClaims"]
}

func (f *fakeStale) ClaimIntegrations(_ context.Context, l db.Lease, _ int64, ids []int64, _ int32) ([]Claimed,
	error) {
	if err := f.fail["ClaimIntegrations"]; err != nil {
		return nil, err
	}
	var out []Claimed
	for _, id := range ids {
		if slices.Contains(f.claimable, id) && l.Owner == "replica-a/stale-scan" {
			out = append(out, Claimed{ID: id, PublicID: fmt.Sprintf("NT%012d", id)})
		}
	}
	return out, nil
}

func (f *fakeStale) RenewIngestLease(context.Context, dbgen.RenewIngestLeaseParams) (dbgen.RenewIngestLeaseRow,
	error) {
	if f.lost {
		return dbgen.RenewIngestLeaseRow{}, pgx.ErrNoRows
	}
	return f.info, f.fail["RenewIngestLease"]
}

func (f *fakeStale) ReleaseIngestClaim(_ context.Context, arg dbgen.ReleaseIngestClaimParams) error {
	f.released = append(f.released, arg.IntegrationID)
	return f.fail["ReleaseIngestClaim"]
}

func (f *fakeStale) ListStalePresences(_ context.Context, arg dbgen.ListStalePresencesParams) (
	[]dbgen.ListStalePresencesRow, error) {
	f.params = append(f.params, arg)
	return f.stale, f.fail["ListStalePresences"]
}

func (f *fakeStale) MarkPresencesStale(_ context.Context, arg dbgen.MarkPresencesStaleParams) error {
	f.marked = append(f.marked, arg)
	return f.fail["MarkPresencesStale"]
}

func (f *fakeStale) ResolveStaleAlerts(_ context.Context, arg dbgen.ResolveStaleAlertsParams) (
	[]dbgen.ResolveStaleAlertsRow, error) {
	f.resolves = append(f.resolves, arg)
	return f.resolved, f.fail["ResolveStaleAlerts"]
}

func (f *fakeStale) ExpireTruncation(_ context.Context, arg dbgen.ExpireTruncationParams) (int64, error) {
	f.expires = append(f.expires, arg)
	return f.expired, f.fail["ExpireTruncation"]
}

func (f *fakeStale) CountTruncatedGroups(context.Context, dbgen.CountTruncatedGroupsParams) (int64, error) {
	return f.truncated, f.fail["CountTruncatedGroups"]
}

func (f *fakeStale) FindBuiltinIntegration(context.Context, int64) (int64, error) {
	return 100, f.fail["FindBuiltinIntegration"]
}

func (f *fakeStale) InsertInternalBody(context.Context, idb.InsertInternalBodyParams) error {
	f.internal++
	return nil
}

func (f *fakeStale) InsertInternalSnapshot(context.Context, idb.InsertInternalSnapshotParams) error {
	return nil
}

func (f *fakeStale) NotifyInternalSnapshot(context.Context, idb.NotifyInternalSnapshotParams) error {
	return nil
}

func newScanner(store ProcessStore, log *bytes.Buffer, sink Sink) *Processor {
	clocks := clock.Clocks{Business: clock.NewManual(t0), Real: clock.Real{}}
	p := newTestProcessor(store, clocks.Business, log, sink)
	p.lease = db.Lease{Owner: "replica-a/stale-scan", Duration: Lease, Clocks: clocks}
	return p
}

// TestStaleScan: the scan of a live Integration under its claim makes the Stale presences Stale at the effective
// clock, resolves the Alerts left without an active presence with the reason stale, hands their changes to the
// Sink, counts and logs them, ends the expired truncation and resolves MusterSnapshotTruncated with the last one, and
// releases the claim.
func TestStaleScan(t *testing.T) {
	store := newFakeStale()
	store.stale = []dbgen.ListStalePresencesRow{{AlertID: 1, AlertmanagerGroupID: 10},
		{AlertID: 2, AlertmanagerGroupID: 10}, {AlertID: 2, AlertmanagerGroupID: 11}}
	store.resolved = []dbgen.ResolveStaleAlertsRow{{ID: 1, Fingerprint: "a", Episode: 2}}
	store.expired = 1
	sink := &recordingSink{}
	var log bytes.Buffer
	before := metrics.AlertsResolved.With("NT000000000005", ResolveStale).Get()
	if err := newScanner(store, &log, sink).StaleScan(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(store.params) != 1 || store.params[0].ClockMs != 1_060_000 || store.params[0].Factor != 3 ||
		store.params[0].UnlearnedMs != (25*time.Hour).Milliseconds() || store.params[0].IntegrationID != 5 {
		t.Errorf("stale presences asked with %+v", store.params)
	}
	if len(store.marked) != 1 || !slices.Equal(store.marked[0].AlertIds, []int64{1, 2, 2}) ||
		!slices.Equal(store.marked[0].AlertmanagerGroupIds, []int64{10, 10, 11}) {
		t.Errorf("marked %+v", store.marked)
	}
	if len(store.resolves) != 1 || store.resolves[0].ReasonText != GoneReasonText ||
		!store.resolves[0].ResolvedAt.Equal(t0) {
		t.Errorf("resolves %+v", store.resolves)
	}
	if len(sink.changes) != 1 || sink.changes[0] != (AlertChange{Kind: ChangeResolved, AlertID: 1, Fingerprint: "a",
		Episode: 2, Reason: ResolveStale, ReasonText: GoneReasonText}) {
		t.Errorf("changes %+v", sink.changes)
	}
	if got := metrics.AlertsResolved.With("NT000000000005", ResolveStale).Get() - before; got != 1 {
		t.Errorf("muster_alerts_resolved_total{reason=stale} grew by %d", got)
	}
	l := events(t, &log)
	if len(l) != 1 || l[0]["event"] != "alerts_stale" || l[0]["integration"] != "NT000000000005" || l[0]["count"] != 1.0 {
		t.Errorf("log %v", l)
	}
	if len(store.expires) != 1 || store.expires[0].ClockMs != 1_060_000 || store.internal != 1 {
		t.Errorf("truncation: %+v, %d internal snapshots", store.expires, store.internal)
	}
	if !slices.Equal(store.released, []int64{5}) || !slices.Equal(store.ensured, []int64{5}) {
		t.Errorf("claims: ensured %v released %v", store.ensured, store.released)
	}

	// A second run finds nothing Stale and changes nothing.
	store.stale, store.resolved, store.expired = nil, nil, 0
	log.Reset()
	if err := newScanner(store, &log, sink).StaleScan(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(store.marked) != 1 || len(store.resolves) != 1 || len(sink.changes) != 1 || store.internal != 1 ||
		log.Len() != 0 {
		t.Errorf("the second run changed something: %+v %s", store.marked, log.String())
	}
}

// TestStaleScanSkips: nothing is scanned without a live Integration, for an Integration claimed by its processing,
// one whose Heartbeat is no longer live under the claim, one deleted, or one whose lease went elsewhere; a truncation
// that ends while another groupKey stays truncated keeps MusterSnapshotTruncated.
func TestStaleScanSkips(t *testing.T) {
	for name, change := range map[string]func(f *fakeStale){
		"no live integration":   func(f *fakeStale) { f.live = nil },
		"claimed by processing": func(f *fakeStale) { f.claimable = nil },
		"lost under the claim":  func(f *fakeStale) { f.info.HeartbeatState = "lost" },
		"snapshots pending":     func(f *fakeStale) { f.pending = true },
		"deleted": func(f *fakeStale) {
			f.info.DeletedAt = pgtype.Timestamptz{Time: t0, Valid: true}
		},
		"the built-in integration": func(f *fakeStale) { f.info.Builtin = true },
		"lease lost":               func(f *fakeStale) { f.lost = true },
	} {
		t.Run(name, func(t *testing.T) {
			store := newFakeStale()
			change(store)
			var log bytes.Buffer
			if err := newScanner(store, &log, nil).StaleScan(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(store.params) != 0 || len(store.expires) != 0 {
				t.Errorf("scanned: %+v %+v", store.params, store.expires)
			}
		})
	}
	store := newFakeStale()
	store.expired, store.truncated = 1, 1
	var log bytes.Buffer
	if err := newScanner(store, &log, nil).StaleScan(t.Context()); err != nil || store.internal != 0 {
		t.Errorf("a truncation left: %v, %d internal snapshots", err, store.internal)
	}
}

// TestStaleScanErrors: each failure is returned and the claim is still released.
func TestStaleScanErrors(t *testing.T) {
	boom := errors.New("boom")
	for _, name := range []string{"ListLiveIntegrations", "EnsureIngestClaims", "GetRetention", "ClaimIntegrations",
		"RenewIngestLease", "HasPendingSnapshots",
		"ListStalePresences", "MarkPresencesStale", "ResolveStaleAlerts", "ExpireTruncation", "CountTruncatedGroups",
		"FindBuiltinIntegration", "ReleaseIngestClaim", "Sink"} {
		t.Run(name, func(t *testing.T) {
			store := newFakeStale()
			store.stale = []dbgen.ListStalePresencesRow{{AlertID: 1, AlertmanagerGroupID: 10}}
			store.resolved = []dbgen.ResolveStaleAlertsRow{{ID: 1, Fingerprint: "a", Episode: 1}}
			store.expired = 1
			store.fail[name] = boom
			sink := &recordingSink{}
			if name == "Sink" {
				sink.err = boom
			}
			var log bytes.Buffer
			if err := newScanner(store, &log, sink).StaleScan(t.Context()); !errors.Is(err, boom) {
				t.Errorf("StaleScan = %v", err)
			}
		})
	}
}

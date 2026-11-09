// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package gateways

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/bridge"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/gateways/dbgen"
	"github.com/muster-io/muster/internal/groups"
	idb "github.com/muster-io/muster/internal/internalalerts/dbgen"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/outbound"
)

// fakeBroken is the Broken state of a gateway in the fake: its columns of connections.
type fakeBroken struct {
	since, nextProbe time.Time
	reason           string
	owner            string
	leaseUntil       time.Time
}

// brokenFake is the BrokenStore of the tests over the gateways of a fakeCalls, with the semantics of query.sql; a
// transaction that fails is rolled back. deleted marks Connections deleted, notGateway those whose info no longer
// declares the role gateway; internal records the synthetic Stored Snapshots of the Internal alerts, hints the hints.
type brokenFake struct {
	f          *fakeCalls
	mu         sync.Mutex
	deleted    map[int64]bool
	internal   []raised
	bodies     [][]byte
	hints      []db.Hint
	fail       map[string]error
	txs        int
	onEndQuery func()
}

// raised is a raise or a resolve of an Internal alert.
type raised struct {
	status string
	labels map[string]string
}

func newBrokenFake(f *fakeCalls) *brokenFake {
	f.mu.Lock()
	f.broken, f.recovered = map[int64]*fakeBroken{}, map[int64]time.Time{}
	f.mu.Unlock()
	return &brokenFake{f: f, deleted: map[int64]bool{}, fail: map[string]error{}}
}

func (b *brokenFake) err(name string) error { return b.fail[name] }

func (b *brokenFake) InBrokenTx(_ context.Context, fn func(BrokenQueries) error) error {
	b.f.mu.Lock()
	saved := map[int64]*fakeBroken{}
	for id, st := range b.f.broken {
		c := *st
		saved[id] = &c
	}
	b.f.mu.Unlock()
	b.mu.Lock()
	internal, bodies, hints := slices.Clone(b.internal), slices.Clone(b.bodies), slices.Clone(b.hints)
	b.txs++
	b.mu.Unlock()
	if err := fn(b); err != nil {
		b.f.mu.Lock()
		b.f.broken = saved
		b.f.mu.Unlock()
		b.mu.Lock()
		b.internal, b.bodies, b.hints = internal, bodies, hints
		b.mu.Unlock()
		return err
	}
	return nil
}

func (b *brokenFake) Over(DBTX) BrokenQueries { return b }

func (b *brokenFake) BreakGateway(_ context.Context, a dbgen.BreakGatewayParams) (dbgen.BreakGatewayRow, error) {
	if err := b.err("BreakGateway"); err != nil {
		return dbgen.BreakGatewayRow{}, err
	}
	b.f.mu.Lock()
	defer b.f.mu.Unlock()
	gw, ok := b.f.gateways[a.ID]
	if a.OrgID != 1 || !ok || b.deleted[a.ID] || b.f.broken[a.ID] != nil {
		return dbgen.BreakGatewayRow{}, pgx.ErrNoRows
	}
	b.f.broken[a.ID] = &fakeBroken{since: a.Now, nextProbe: a.NextProbe, reason: a.Reason}
	return dbgen.BreakGatewayRow{PublicID: gw.publicID, Name: gw.name}, nil
}

func (b *brokenFake) UpdateGatewayBrokenReason(_ context.Context, a dbgen.UpdateGatewayBrokenReasonParams) (int64,
	error) {
	if err := b.err("UpdateGatewayBrokenReason"); err != nil {
		return 0, err
	}
	b.f.mu.Lock()
	defer b.f.mu.Unlock()
	if st := b.f.broken[a.ID]; st != nil && a.OrgID == 1 {
		st.reason = a.Reason
		return 1, nil
	}
	return 0, nil
}

func (b *brokenFake) EndGatewayBroken(_ context.Context, a dbgen.EndGatewayBrokenParams) (dbgen.EndGatewayBrokenRow,
	error) {
	if b.onEndQuery != nil {
		b.onEndQuery()
	}
	if err := b.err("EndGatewayBroken"); err != nil {
		return dbgen.EndGatewayBrokenRow{}, err
	}
	b.f.mu.Lock()
	defer b.f.mu.Unlock()
	st := b.f.broken[a.ID]
	if a.OrgID != 1 || st == nil || (a.Owner.Valid && (st.owner != a.Owner.String || !st.leaseUntil.After(a.Now))) {
		return dbgen.EndGatewayBrokenRow{}, pgx.ErrNoRows
	}
	delete(b.f.broken, a.ID)
	b.f.recovered[a.ID] = a.RecoveredAt
	gw := b.f.gateways[a.ID]
	return dbgen.EndGatewayBrokenRow{PublicID: gw.publicID, Name: gw.name, WasBrokenSince: st.since,
		Deleted: b.deleted[a.ID]}, nil
}

func (b *brokenFake) ClaimGatewayProbes(_ context.Context, a dbgen.ClaimGatewayProbesParams) (
	[]dbgen.ClaimGatewayProbesRow, error) {
	if err := b.err("ClaimGatewayProbes"); err != nil {
		return nil, err
	}
	b.f.mu.Lock()
	defer b.f.mu.Unlock()
	var due []int64
	for id, st := range b.f.broken {
		if a.OrgID == 1 && !b.deleted[id] && !st.nextProbe.After(a.Due) &&
			(st.owner == "" || !st.leaseUntil.After(a.Now)) {
			due = append(due, id)
		}
	}
	slices.SortFunc(due, func(x, y int64) int {
		if c := b.f.broken[x].nextProbe.Compare(b.f.broken[y].nextProbe); c != 0 {
			return c
		}
		return cmp.Compare(x, y)
	})
	if len(due) > int(a.Lim) {
		due = due[:a.Lim]
	}
	out := []dbgen.ClaimGatewayProbesRow{}
	for _, id := range due {
		st := b.f.broken[id]
		st.owner, st.leaseUntil, st.nextProbe = a.Owner, a.LeaseUntil, a.NextProbe
		gw := b.f.gateways[id]
		out = append(out, dbgen.ClaimGatewayProbesRow{ID: id, PublicID: gw.publicID, Name: gw.name})
	}
	return out, nil
}

func (b *brokenFake) FailGatewayProbe(_ context.Context, a dbgen.FailGatewayProbeParams) (int64, error) {
	if err := b.err("FailGatewayProbe"); err != nil {
		return 0, err
	}
	b.f.mu.Lock()
	defer b.f.mu.Unlock()
	st := b.f.broken[a.ID]
	if a.OrgID != 1 || st == nil || st.owner != a.Owner || !st.leaseUntil.After(a.Now) {
		return 0, nil
	}
	st.reason, st.owner, st.leaseUntil = a.Reason, "", time.Time{}
	return 1, nil
}

func (b *brokenFake) NextGatewayProbe(_ context.Context, a dbgen.NextGatewayProbeParams) (dbgen.NextGatewayProbeRow,
	error) {
	if err := b.err("NextGatewayProbe"); err != nil {
		return dbgen.NextGatewayProbeRow{}, err
	}
	b.f.mu.Lock()
	defer b.f.mu.Unlock()
	var out dbgen.NextGatewayProbeRow
	for id, st := range b.f.broken {
		if b.deleted[id] {
			continue
		}
		if st.owner == "" || !st.leaseUntil.After(a.Now) {
			if out.FreeAt.IsZero() || st.nextProbe.Before(out.FreeAt) {
				out.FreeAt = st.nextProbe
			}
		} else if out.LeaseEnd.IsZero() || st.leaseUntil.Before(out.LeaseEnd) {
			out.LeaseEnd = st.leaseUntil
		}
	}
	return out, nil
}

func (b *brokenFake) ListGatewayHealth(_ context.Context, orgID int64) ([]dbgen.ListGatewayHealthRow, error) {
	if err := b.err("ListGatewayHealth"); err != nil {
		return nil, err
	}
	b.f.mu.Lock()
	defer b.f.mu.Unlock()
	out := []dbgen.ListGatewayHealthRow{}
	for _, id := range slices.Sorted(maps.Keys(b.f.gateways)) {
		gw := b.f.gateways[id]
		if orgID == 1 && !b.deleted[id] && (gw.gateway || b.f.broken[id] != nil) {
			out = append(out, dbgen.ListGatewayHealthRow{PublicID: gw.publicID, Broken: b.f.broken[id] != nil})
		}
	}
	return out, nil
}

func (b *brokenFake) Notify(_ context.Context, h db.Hint) error {
	if err := b.err("Notify"); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.hints = append(b.hints, h)
	return nil
}

func (b *brokenFake) FindBuiltinIntegration(context.Context, int64) (int64, error) {
	return 100, b.err("FindBuiltinIntegration")
}

func (b *brokenFake) InsertInternalBody(_ context.Context, a idb.InsertInternalBodyParams) error {
	if err := b.err("InsertInternalBody"); err != nil {
		return err
	}
	var w struct {
		Status string `json:"status"`
		Alerts []struct {
			Labels map[string]string `json:"labels"`
		} `json:"alerts"`
	}
	if err := json.Unmarshal(a.Body, &w); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.internal = append(b.internal, raised{status: w.Status, labels: w.Alerts[0].Labels})
	b.bodies = append(b.bodies, a.Body)
	return nil
}

func (b *brokenFake) InsertInternalSnapshot(context.Context, idb.InsertInternalSnapshotParams) error {
	return nil
}

func (b *brokenFake) NotifyInternalSnapshot(context.Context, idb.NotifyInternalSnapshotParams) error {
	return nil
}

func (b *brokenFake) ListOpenInternalAlerts(context.Context, idb.ListOpenInternalAlertsParams) (
	[]idb.ListOpenInternalAlertsRow, error) {
	return nil, nil
}

// ListPendingInternalRaises lists every synthetic Snapshot written, none processed yet.
func (b *brokenFake) ListPendingInternalRaises(context.Context, int64) ([][]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.bodies), nil
}

// alerts are the raises and resolves written, as status/connection/connection_name.
func (b *brokenFake) alerts() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, r := range b.internal {
		out = append(out, r.status+"/"+r.labels["alertname"]+"/"+r.labels["connection"]+"/"+
			r.labels["connection_name"])
	}
	return out
}

// state is the Broken state of the gateway id, nil while it is in order.
func (b *brokenFake) state(id int64) *fakeBroken {
	b.f.mu.Lock()
	defer b.f.mu.Unlock()
	if st := b.f.broken[id]; st != nil {
		c := *st
		return &c
	}
	return nil
}

// prober is the Prober of the tests: it answers with the next scripted outcome, ok when none is left, and runs during
// each probe when set.
type prober struct {
	mu       sync.Mutex
	probed   []int64
	outcomes []delivery.Outcome
	during   func()
	checks   []string
}

func (p *prober) ProbeGateway(ctx context.Context, id int64) delivery.Outcome {
	p.mu.Lock()
	p.probed = append(p.probed, id)
	p.checks = append(p.checks, outbound.LinksOf(ctx).Check)
	var o delivery.Outcome
	if len(p.outcomes) > 0 {
		o, p.outcomes = p.outcomes[0], p.outcomes[1:]
	} else {
		o = delivery.Outcome{Kind: delivery.OutcomeOK}
	}
	during := p.during
	p.mu.Unlock()
	if during != nil {
		during()
	}
	return o
}

// brokenEnv is a sender with the Broken state of its gateways.
type brokenEnv struct {
	*senderEnv
	b      *brokenFake
	prober *prober
}

func newBrokenSender(t *testing.T) *brokenEnv {
	t.Helper()
	e := newSender(t)
	b := newBrokenFake(e.f)
	p := &prober{}
	e.s.Broken = &Broken{OrgID: 1, Store: b, Lease: e.s.Lease, Prober: p, Log: e.s.Log}
	return &brokenEnv{senderEnv: e, b: b, prober: p}
}

// scrape is what /metrics serves, the Leader's metrics included.
func scrape(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	metrics.Handler(func() bool { return true }).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(),
		http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

// gauge is the scraped muster_notification_gateway_broken of the Connection id, "" without a series.
func gauge(t *testing.T, id string) string {
	t.Helper()
	prefix := `muster_notification_gateway_broken{connection="` + id + `"} `
	for line := range strings.SplitSeq(scrape(t), "\n") {
		if v, ok := strings.CutPrefix(line, prefix); ok {
			return v
		}
	}
	return ""
}

// forbidden is the answer of a gateway that refuses the bridge's token.
var forbidden = delivery.Outcome{Kind: delivery.OutcomeFatal, Status: 401, Error: "POST notifications: 401 Unauthorized"}

// TestBrokenAfterFatal is C-30.FR-8 and AC-5 on a manual clock: a call the gateway answers 401 — forbidden, a Fatal
// answer — ends Not delivered with call_refused and makes the gateway Broken with the masked error as its reason, its
// first probe delivery.broken_probe_interval later; MusterNotificationGatewayBroken fires about the Connection with
// its name, the Connection's hint is sent and gateway_broken is logged with the cause fatal; the Leader's gauge is 1
// for it and 0 for the gateway in order, and running it twice sets the same values.
func TestBrokenAfterFatal(t *testing.T) {
	e := newBrokenSender(t)
	e.gw.script(forbidden)
	e.page(t, 1, 10, 1)
	e.round(t)
	call := e.f.calls[0]
	if call.state != CallNotDelivered || call.reason.String != groups.PageCallRefused || len(e.pages.pages) != 1 {
		t.Fatalf("the refused call %+v", call)
	}
	st := e.b.state(10)
	if st == nil || !st.since.Equal(c0) || st.reason != "POST notifications: 401 Unauthorized" ||
		!st.nextProbe.Equal(c0.Add(BrokenProbeInterval)) || e.b.state(11) != nil {
		t.Fatalf("the state %+v", st)
	}
	if got := e.b.alerts(); !slices.Equal(got, []string{
		"firing/MusterNotificationGatewayBroken/CN00000000GW10/Gateway"}) {
		t.Errorf("internal alerts %v", got)
	}
	if len(e.b.hints) != 1 || e.b.hints[0] != (db.Hint{OrgID: 1, Type: "connection", ID: "CN00000000GW10"}) {
		t.Errorf("hints %v", e.b.hints)
	}
	if !strings.Contains(e.log.String(), `"event":"gateway_broken","connection":"CN00000000GW10","cause":"fatal",`+
		`"reason":"POST notifications: 401 Unauthorized"`) {
		t.Errorf("the log:\n%s", e.log)
	}
	for range 2 {
		if err := e.s.Broken.Export(t.Context()); err != nil {
			t.Fatal(err)
		}
		if gauge(t, "CN00000000GW10") != "1" || gauge(t, "CN00000000GW11") != "0" || gauge(t, "CN00000000BR12") != "" {
			t.Errorf("the gauge %q %q %q", gauge(t, "CN00000000GW10"), gauge(t, "CN00000000GW11"),
				gauge(t, "CN00000000BR12"))
		}
	}
	// A deleted gateway's series goes.
	e.b.deleted[11] = true
	if err := e.s.Broken.Export(t.Context()); err != nil || gauge(t, "CN00000000GW11") != "" {
		t.Errorf("the gauge of a deleted gateway %q, %v", gauge(t, "CN00000000GW11"), err)
	}
	delete(e.b.deleted, 11)
	e.b.fail["ListGatewayHealth"] = errors.New("down")
	if err := e.s.Broken.Export(t.Context()); err == nil {
		t.Error("a failed read was ignored")
	}
}

// TestBrokenCallsNotSentAndProbe is C-30.FR-8, AC-5 and P-68 on a manual clock: while the gateway is Broken, every
// call that comes due — waiting or being retried — ends Not delivered with gateway_broken, unsent and without a
// limiter token, and records page_not_delivered naming the person and the gateway; the probe through info runs every
// delivery.broken_probe_interval in a check run of the kind probe, a failed one keeps the state with its error as the
// reason; a successful one ends it, resolves the alert, logs gateway_recovered and sets the gauge to 0 at the next run;
// no call is sent after the recovery.
func TestBrokenCallsNotSentAndProbe(t *testing.T) {
	e := newBrokenSender(t)
	// A call of another person being retried when the gateway becomes Broken.
	e.gw.script(delivery.Outcome{Kind: delivery.OutcomeTransient, Error: "busy"}, forbidden)
	e.page(t, 2, 10, 2)
	e.round(t)
	e.page(t, 1, 10, 1)
	e.round(t)
	if e.b.state(10) == nil || e.gw.count() != 2 {
		t.Fatalf("not Broken after %d calls", e.gw.count())
	}
	if next := e.round(t); next != delivery.TransientFirstStep {
		t.Errorf("the next round in %v", next)
	}
	e.advance(61 * time.Second)
	e.page(t, 1, 10, 3)
	e.lim.deny = true
	e.round(t)
	if e.gw.count() != 2 {
		t.Fatalf("a call went through a Broken gateway: %d", e.gw.count())
	}
	for _, c := range e.f.calls {
		if c.state != CallNotDelivered {
			t.Errorf("the call %+v", c)
		}
	}
	retried, waited := e.f.calls[0], e.f.calls[2]
	if retried.reason.String != groups.PageGatewayBroken || retried.attempts != 1 ||
		waited.reason.String != groups.PageGatewayBroken || waited.attempts != 0 || waited.text.Valid {
		t.Errorf("the calls due while Broken %+v %+v", retried, waited)
	}
	last := e.pages.pages[len(e.pages.pages)-1]
	if last.page.Reason != groups.PageGatewayBroken || last.page.Gateway.PublicID != "CN00000000GW10" ||
		last.page.Users[0].PublicID != "SR0000000000P1" || !slices.Equal(last.ids, []int64{3}) {
		t.Errorf("page_not_delivered %+v", last)
	}
	if !strings.Contains(e.log.String(), `"event":"gateway_call_not_delivered"`) ||
		!strings.Contains(e.log.String(), `"reason":"gateway_broken"`) {
		t.Errorf("the log:\n%s", e.log)
	}
	e.lim.deny = false

	// The probe fails: still Broken, with the probe's error as the reason, probed again an interval later.
	e.prober.outcomes = []delivery.Outcome{{Kind: delivery.OutcomeTransient, Status: 503, Error: "GET info: 503"}}
	e.s.MaxWait = time.Hour
	if next := e.round(t); next != BrokenProbeInterval-61*time.Second {
		t.Errorf("the next round in %v, not at the probe", next)
	}
	e.advance(BrokenProbeInterval - 61*time.Second)
	e.round(t)
	st := e.b.state(10)
	if len(e.prober.probed) != 1 || st == nil || st.reason != "GET info: 503" || st.owner != "" ||
		!st.nextProbe.Equal(c0.Add(2*BrokenProbeInterval)) || e.prober.checks[0] != outbound.CheckProbe {
		t.Fatalf("after a failed probe %+v, probed %v", st, e.prober.probed)
	}
	e.round(t)
	if len(e.prober.probed) != 1 {
		t.Errorf("probed again before the interval: %v", e.prober.probed)
	}

	// The probe succeeds: the state ends, nothing is sent.
	e.advance(BrokenProbeInterval)
	e.round(t)
	if e.b.state(10) != nil || len(e.prober.probed) != 2 || e.gw.count() != 2 {
		t.Fatalf("after a successful probe %+v, %d calls", e.b.state(10), e.gw.count())
	}
	if got := e.b.alerts(); !slices.Equal(got, []string{
		"firing/MusterNotificationGatewayBroken/CN00000000GW10/Gateway",
		"resolved/MusterNotificationGatewayBroken/CN00000000GW10/"}) {
		t.Errorf("internal alerts %v", got)
	}
	if !strings.Contains(e.log.String(), `"event":"gateway_recovered","connection":"CN00000000GW10","by":"probe",`+
		`"broken_for_s":600`) {
		t.Errorf("the log:\n%s", e.log)
	}
	if err := e.s.Broken.Export(t.Context()); err != nil || gauge(t, "CN00000000GW10") != "0" {
		t.Errorf("the gauge after the recovery %q, %v", gauge(t, "CN00000000GW10"), err)
	}
	e.advance(time.Hour)
	e.round(t)
	if e.gw.count() != 2 {
		t.Errorf("a call due while Broken was sent after the recovery")
	}
	// The next call goes.
	e.page(t, 1, 10, 4)
	e.round(t)
	if e.gw.count() != 3 || e.f.calls[len(e.f.calls)-1].state != CallAccepted {
		t.Errorf("the call after the recovery %+v", e.f.calls[len(e.f.calls)-1])
	}
}

// TestBrokenAfterBudget is C-30.FR-8: a call that used up delivery.transient_budget, by failed attempts or by the
// time of the waits the gateway asked for, ends Not delivered with call_budget_used and makes the gateway Broken with
// the cause unavailable and the reason "unavailable after repeated failures: …"; an invalid answer, which is about the
// call, ends it call_refused and leaves the gateway in order.
func TestBrokenAfterBudget(t *testing.T) {
	e := newBrokenSender(t)
	for range delivery.TransientBudgetAttempts {
		e.gw.script(delivery.Outcome{Kind: delivery.OutcomeTransient, Error: "bridge down"})
	}
	e.page(t, 1, 10, 1)
	for e.f.calls[0].state != CallNotDelivered {
		e.round(t)
		e.advance(delivery.TransientBackoffMax)
	}
	call := e.f.calls[0]
	st := e.b.state(10)
	if call.reason.String != groups.PageCallBudgetUsed || st == nil ||
		st.reason != "unavailable after repeated failures: bridge down" {
		t.Fatalf("the call %+v, the state %+v", call, st)
	}
	if !strings.Contains(e.log.String(), `"event":"gateway_broken","connection":"CN00000000GW10","cause":"unavailable"`) {
		t.Errorf("the log:\n%s", e.log)
	}

	e = newBrokenSender(t)
	for range 3 {
		e.gw.script(delivery.Outcome{Kind: delivery.OutcomeRetryAfter, RetryAfter: 20 * time.Minute, Error: "wait"})
	}
	e.page(t, 1, 10, 1)
	e.round(t)
	e.advance(21 * time.Minute)
	e.round(t)
	e.advance(21 * time.Minute)
	e.round(t)
	if call, st := e.f.calls[0], e.b.state(11); call.reason.String != groups.PageCallBudgetUsed || st != nil ||
		e.b.state(10) == nil || e.b.state(10).reason != "unavailable after repeated failures: wait" {
		t.Errorf("after the waits: the call %+v, the state %+v", call, e.b.state(10))
	}

	e = newBrokenSender(t)
	e.gw.script(delivery.Outcome{Kind: delivery.OutcomeUnknown, Status: 400, Error: "invalid contact"})
	e.page(t, 1, 10, 1)
	e.round(t)
	if call := e.f.calls[0]; call.reason.String != groups.PageCallRefused || e.b.state(10) != nil ||
		len(e.b.alerts()) != 0 {
		t.Errorf("an invalid answer: the call %+v, the state %+v", call, e.b.state(10))
	}
}

// TestBrokenAgain: a Fatal answer of a gateway that is Broken already keeps its state, since and alert with the newer
// reason, and logs no second gateway_broken; a failure of the state's transaction rolls back its alert and is the
// attempt's failure, logged as gateway_call_failed.
func TestBrokenAgain(t *testing.T) {
	e := newBrokenSender(t)
	e.gw.script(forbidden, delivery.Outcome{Kind: delivery.OutcomeFatal, Status: 403, Error: "403 Forbidden"})
	e.page(t, 1, 10, 1)
	e.page(t, 2, 10, 2)
	if err := e.s.Broken.Break(t.Context(), brokenGateway{id: 10, publicID: "CN00000000GW10"}, causeFatal,
		"first"); err != nil {
		t.Fatal(err)
	}
	e.b.f.mu.Lock()
	e.b.f.broken[10].since = c0.Add(-time.Hour)
	e.b.f.mu.Unlock()
	if err := e.s.Broken.Break(t.Context(), brokenGateway{id: 10, publicID: "CN00000000GW10"}, causeFatal,
		"403 Forbidden"); err != nil {
		t.Fatal(err)
	}
	if st := e.b.state(10); st == nil || st.reason != "403 Forbidden" || !st.since.Equal(c0.Add(-time.Hour)) ||
		len(e.b.alerts()) != 1 || strings.Count(e.log.String(), `"event":"gateway_broken"`) != 1 {
		t.Errorf("Broken again %+v, alerts %v", st, e.b.alerts())
	}
	// A deleted Connection is never made Broken.
	e.b.deleted[11] = true
	if err := e.s.Broken.Break(t.Context(), brokenGateway{id: 11, publicID: "CN00000000GW11"}, causeFatal,
		"x"); err != nil || e.b.state(11) != nil {
		t.Errorf("a deleted gateway: %+v, %v", e.b.state(11), err)
	}
	delete(e.b.deleted, 11)
	for _, q := range []string{"BreakGateway", "InsertInternalBody", "Notify"} {
		e.b.fail[q] = errors.New("down")
		if err := e.s.Broken.Break(t.Context(), brokenGateway{id: 11, publicID: "CN00000000GW11"}, causeFatal,
			"x"); err == nil || e.b.state(11) != nil || len(e.b.alerts()) != 1 {
			t.Errorf("%s failed: %v, %+v, %v", q, err, e.b.state(11), e.b.alerts())
		}
		delete(e.b.fail, q)
	}
	e.b.fail["UpdateGatewayBrokenReason"] = errors.New("down")
	if err := e.s.Broken.Break(t.Context(), brokenGateway{id: 10, publicID: "CN00000000GW10"}, causeFatal,
		"x"); err == nil {
		t.Error("a failed update of the reason was ignored")
	}
	delete(e.b.fail, "UpdateGatewayBrokenReason")
	// The call's Break fails: the call ended Not delivered, and the failure is logged.
	e.b.fail["BreakGateway"] = errors.New("down")
	e.s.Gateway = &notifier{outcomes: []delivery.Outcome{forbidden}}
	e.page(t, 1, 11, 1)
	e.round(t)
	if !strings.Contains(e.log.String(), `"event":"gateway_call_failed"`) {
		t.Errorf("the failed Break was not logged:\n%s", e.log)
	}
}

// TestEndBroken is C-30.FR-8 on the other ends of the state: a successful Connection check ends it, resolves the
// alert and logs gateway_recovered by check; a gateway in order changes nothing; the deletion of the Connection ends
// it and resolves the alert without a log line or a hint; a rename raises the open alert again with the new name.
func TestEndBroken(t *testing.T) {
	e := newBrokenSender(t)
	gw := brokenGateway{id: 10, publicID: "CN00000000GW10"}
	if err := e.s.Broken.Break(t.Context(), gw, causeFatal, "401"); err != nil {
		t.Fatal(err)
	}
	e.advance(90 * time.Second)
	if err := e.s.Broken.Renamed(t.Context(), nil, "CN00000000GW10", "Voice"); err != nil {
		t.Fatal(err)
	}
	if got := e.b.alerts(); !slices.Equal(got, []string{"firing/MusterNotificationGatewayBroken/CN00000000GW10/Gateway",
		"firing/MusterNotificationGatewayBroken/CN00000000GW10/Voice"}) {
		t.Errorf("renamed %v", got)
	}
	if err := e.s.Broken.EndBroken(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	if e.b.state(10) != nil || !strings.Contains(e.log.String(),
		`"event":"gateway_recovered","connection":"CN00000000GW10","by":"check","broken_for_s":90`) {
		t.Errorf("after the check %+v:\n%s", e.b.state(10), e.log)
	}
	n := len(e.b.alerts())
	if err := e.s.Broken.EndBroken(t.Context(), 10); err != nil || len(e.b.alerts()) != n {
		t.Errorf("a gateway in order: %v, %v", err, e.b.alerts())
	}

	if err := e.s.Broken.Break(t.Context(), gw, causeFatal, "401"); err != nil {
		t.Fatal(err)
	}
	e.b.deleted[10] = true
	hints, lines := len(e.b.hints), strings.Count(e.log.String(), `"event":"gateway_recovered"`)
	if err := e.s.Broken.Deleted(t.Context(), nil, 10); err != nil {
		t.Fatal(err)
	}
	if got := e.b.alerts(); e.b.state(10) != nil || !strings.HasPrefix(got[len(got)-1], "resolved/") ||
		len(e.b.hints) != hints || strings.Count(e.log.String(), `"event":"gateway_recovered"`) != lines {
		t.Errorf("after the deletion %+v %v", e.b.state(10), got)
	}

	delete(e.b.deleted, 10)
	if err := e.s.Broken.Break(t.Context(), gw, causeFatal, "401"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"EndGatewayBroken", "InsertInternalBody", "Notify"} {
		e.b.fail[q] = errors.New("down")
		if err := e.s.Broken.EndBroken(t.Context(), 10); err == nil || e.b.state(10) == nil {
			t.Errorf("%s failed: %v", q, err)
		}
		delete(e.b.fail, q)
	}
	e.b.fail["InsertInternalBody"] = errors.New("down")
	if err := e.s.Broken.Renamed(t.Context(), nil, "CN00000000GW10", "Calls"); err == nil {
		t.Error("a failed rename was ignored")
	}
}

// TestProbeLease is C-30.FR-8 and NFR-4 on the probe's lease: two replicas probe a Broken gateway once per interval
// between them; a replica whose lease went to another while it probed changes nothing with its outcome; a failed claim
// or record fails the round.
func TestProbeLease(t *testing.T) {
	e := newBrokenSender(t)
	if err := e.s.Broken.Break(t.Context(), brokenGateway{id: 10, publicID: "CN00000000GW10"}, causeFatal,
		"401"); err != nil {
		t.Fatal(err)
	}
	other := *e.s.Broken
	other.Lease.Owner = "r2"
	e.advance(BrokenProbeInterval)
	// While r1 probes, r2 claims nothing: the lease is live and the next probe moved on.
	e.prober.during = func() {
		if err := other.Probe(t.Context()); err != nil {
			t.Error(err)
		}
	}
	e.prober.outcomes = []delivery.Outcome{{Kind: delivery.OutcomeTransient, Error: "down"}}
	e.round(t)
	if len(e.prober.probed) != 1 || e.b.state(10).reason != "down" {
		t.Fatalf("probed %v, the state %+v", e.prober.probed, e.b.state(10))
	}
	// r2 takes the lease over while r1 probes: r1's success changes nothing.
	e.advance(BrokenProbeInterval)
	e.prober.during = func() {
		e.b.f.mu.Lock()
		e.b.f.broken[10].owner = "r2"
		e.b.f.mu.Unlock()
	}
	e.round(t)
	if e.b.state(10) == nil || len(e.b.alerts()) != 1 {
		t.Errorf("the outcome of a lost lease changed the state: %+v", e.b.state(10))
	}
	e.prober.during = nil
	e.b.f.mu.Lock()
	e.b.f.broken[10].owner, e.b.f.broken[10].leaseUntil = "r2", e.real.Now().Add(time.Minute)
	e.b.f.mu.Unlock()
	if d, ok, err := e.s.Broken.Next(t.Context()); err != nil || !ok || d != time.Minute {
		t.Errorf("next with a live lease %v %v %v", d, ok, err)
	}
	e.b.f.mu.Lock()
	e.b.f.broken[10].owner, e.b.f.broken[10].leaseUntil = "", time.Time{}
	e.b.f.mu.Unlock()
	e.advance(BrokenProbeInterval)
	e.prober.outcomes = []delivery.Outcome{{Kind: delivery.OutcomeFatal, Status: 401, Error: "401"}}
	e.b.fail["FailGatewayProbe"] = errors.New("down")
	if _, err := e.s.Round(t.Context()); err == nil {
		t.Error("a failed record of the probe was ignored")
	}
	delete(e.b.fail, "FailGatewayProbe")
	for _, q := range []string{"ClaimGatewayProbes", "NextGatewayProbe"} {
		e.b.fail[q] = errors.New("down")
		if _, err := e.s.Round(t.Context()); err == nil {
			t.Errorf("a failed %s was ignored", q)
		}
		delete(e.b.fail, q)
	}
	if d, ok, err := (&Broken{OrgID: 1, Store: newBrokenFake(newFakeCalls()), Lease: e.s.Lease}).Next(
		t.Context()); err != nil || ok || d != 0 {
		t.Errorf("next without a Broken gateway %v %v %v", d, ok, err)
	}
	// A Batch of one claims them all, one at a time.
	if err := e.s.Broken.Break(t.Context(), brokenGateway{id: 11, publicID: "CN00000000GW11"}, causeFatal,
		"x"); err != nil {
		t.Fatal(err)
	}
	e.advance(time.Hour)
	e.s.Broken.Batch = 1
	e.prober.outcomes = nil
	e.round(t)
	if e.b.state(10) != nil || e.b.state(11) != nil {
		t.Errorf("not every gateway was probed: %+v %+v", e.b.state(10), e.b.state(11))
	}
	if reasonOf(delivery.Outcome{Kind: delivery.OutcomeTransient}) != "transient" {
		t.Error("the reason of an answer without an error")
	}
	if metrics.NotificationGatewayBroken.With("CN00000000GW99").Get() != 0 {
		t.Error("a new series is not 0")
	}
	metrics.NotificationGatewayBroken.Delete("CN00000000GW99")
}

// TestBrokenOnlyByTheGateway is C-30.FR-8 on what breaks a gateway: Muster's own refusals that never reach the
// gateway — its Connection is gone, its info no longer declares the role gateway — end the call Not delivered with
// call_refused and leave the gateway in order, without MusterNotificationGatewayBroken; an address the outbound policy
// blocks, a lasting failure without an answer, makes it Broken, as a Fatal answer of the gateway does.
func TestBrokenOnlyByTheGateway(t *testing.T) {
	e := newBrokenSender(t)
	for i, out := range []delivery.Outcome{
		{Kind: delivery.OutcomeFatal, Error: outbound.Untrusted(bridge.ErrNoTarget.Error())},
		{Kind: delivery.OutcomeFatal, Error: bridge.MessageNotGateway},
	} {
		e.gw.script(out)
		e.page(t, 1, 10, 1)
		e.round(t)
		call := e.f.calls[len(e.f.calls)-1]
		if call.state != CallNotDelivered || call.reason.String != groups.PageCallRefused || e.b.state(10) != nil ||
			len(e.b.alerts()) != 0 || len(e.b.hints) != 0 {
			t.Fatalf("%d %q: the call %+v, the state %+v, alerts %v", i, out.Error, call, e.b.state(10),
				e.b.alerts())
		}
		e.advance(2 * CombineWindow)
	}
	if strings.Contains(e.log.String(), `"event":"gateway_broken"`) {
		t.Errorf("logged gateway_broken:\n%s", e.log)
	}
	e.gw.script(delivery.Outcome{Kind: delivery.OutcomeFatal, Error: "blocked by the outbound address policy"})
	e.page(t, 1, 10, 1)
	e.round(t)
	if st := e.b.state(10); st == nil || st.reason != "blocked by the outbound address policy" ||
		len(e.b.alerts()) != 1 {
		t.Fatalf("a blocked address: %+v, %v", st, e.b.alerts())
	}
	if !strings.Contains(e.log.String(), `"event":"gateway_broken","connection":"CN00000000GW10","cause":"fatal"`) {
		t.Errorf("the log:\n%s", e.log)
	}
	e = newBrokenSender(t)
	e.gw.script(delivery.Outcome{Kind: delivery.OutcomeFatal, Status: 403, Error: "403 Forbidden"})
	e.page(t, 1, 10, 1)
	e.round(t)
	if st := e.b.state(10); st == nil || st.reason != "403 Forbidden" || len(e.b.alerts()) != 1 {
		t.Errorf("a Fatal answer of the gateway: %+v, %v", st, e.b.alerts())
	}
}

// TestBrokenReasonHint: a newer reason of a gateway that is Broken already sends the Connection's hint, so that its
// page shows the reason; a gateway in order gets no reason and no hint.
func TestBrokenReasonHint(t *testing.T) {
	e := newBrokenSender(t)
	gw := brokenGateway{id: 10, publicID: "CN00000000GW10"}
	if err := e.s.Broken.Break(t.Context(), gw, causeFatal, "401"); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Broken.Break(t.Context(), gw, causeFatal, "403"); err != nil {
		t.Fatal(err)
	}
	if len(e.b.hints) != 2 || e.b.hints[1].ID != "CN00000000GW10" || e.b.state(10).reason != "403" {
		t.Errorf("hints %v, the state %+v", e.b.hints, e.b.state(10))
	}
	e.b.deleted[11] = true
	if err := e.s.Broken.Break(t.Context(), brokenGateway{id: 11, publicID: "CN00000000GW11"}, causeFatal,
		"x"); err != nil || len(e.b.hints) != 2 {
		t.Errorf("a deleted gateway: hints %v, %v", e.b.hints, err)
	}
	e.b.fail["Notify"] = errors.New("down")
	if err := e.s.Broken.Break(t.Context(), gw, causeFatal, "x"); err == nil || e.b.state(10).reason != "403" {
		t.Errorf("a failed hint: %v, %+v", err, e.b.state(10))
	}
}

// TestBrokenEndedWhilePrepared is P-68 across replicas: a replica claims a call that came due while the gateway was
// Broken, another replica's probe ends the state before the first prepares it, and the call still ends Not delivered
// with gateway_broken, unsent; a call that comes due after the end goes.
func TestBrokenEndedWhilePrepared(t *testing.T) {
	e := newBrokenSender(t)
	if err := e.s.Broken.Break(t.Context(), brokenGateway{id: 10, publicID: "CN00000000GW10"}, causeFatal,
		"401"); err != nil {
		t.Fatal(err)
	}
	e.page(t, 1, 10, 1)
	rows, err := e.f.ClaimDueCalls(t.Context(), claimParams(e.s))
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	e.advance(time.Second)
	if err := e.s.Broken.EndBroken(t.Context(), 10); err != nil || e.b.state(10) != nil {
		t.Fatalf("the end %+v, %v", e.b.state(10), err)
	}
	e.s.attempt(t.Context(), rows[0])
	if call := e.f.calls[0]; e.gw.count() != 0 || call.state != CallNotDelivered ||
		call.reason.String != groups.PageGatewayBroken {
		t.Fatalf("the call claimed while Broken %+v, %d sent", call, e.gw.count())
	}
	e.advance(2 * CombineWindow)
	e.page(t, 1, 10, 2)
	e.round(t)
	if e.gw.count() != 1 || e.f.calls[1].state != CallAccepted {
		t.Errorf("the call due after the end %+v", e.f.calls[1])
	}
}

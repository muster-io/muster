// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package gateways

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/bridge"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/gateways/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/internalalerts"
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
	f           *fakeCalls
	mu          sync.Mutex
	deleted     map[int64]bool
	internal    []raised
	bodies      [][]byte
	hints       []db.Hint
	fail        map[string]error
	beforeBreak func()
	locks       []int64
	txs         int
	onEndQuery  func()
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
	if f := b.beforeBreak; f != nil {
		b.beforeBreak = nil
		f()
	}
	if err := b.err("BreakGateway"); err != nil {
		return dbgen.BreakGatewayRow{}, err
	}
	b.f.mu.Lock()
	defer b.f.mu.Unlock()
	gw, ok := b.f.gateways[a.ID]
	if a.OrgID != 1 || !ok || b.deleted[a.ID] || b.f.broken[a.ID] != nil {
		return dbgen.BreakGatewayRow{}, pgx.ErrNoRows
	}
	if r, ok := b.f.recovered[a.ID]; ok && a.FailedAt.Valid && !r.Before(a.FailedAt.Time) {
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

func (b *brokenFake) ListUnmarkedFailures(_ context.Context, a dbgen.ListUnmarkedFailuresParams) (
	[]dbgen.ListUnmarkedFailuresRow, error) {
	if err := b.err("ListUnmarkedFailures"); err != nil {
		return nil, err
	}
	b.f.mu.Lock()
	defer b.f.mu.Unlock()
	out := []dbgen.ListUnmarkedFailuresRow{}
	for _, id := range slices.Sorted(maps.Keys(b.f.gateways)) {
		if a.OrgID != 1 || b.deleted[id] || b.f.broken[id] != nil {
			continue
		}
		var last *fakeCall
		for i := range b.f.calls {
			c := &b.f.calls[i]
			r, recovered := b.f.recovered[id]
			breaks := (c.reason.String == groups.PageCallRefused && c.errorClass.String == "fatal") ||
				c.reason.String == groups.PageCallBudgetUsed
			if c.conn != id || c.state != CallNotDelivered || !c.ended.Valid || !c.ended.Time.After(a.Since) ||
				(recovered && !c.ended.Time.After(r)) || !breaks {
				continue
			}
			if last == nil || !c.ended.Time.Before(last.ended.Time) {
				last = c
			}
		}
		if last != nil {
			out = append(out, dbgen.ListUnmarkedFailuresRow{ID: id, PublicID: b.f.gateways[id].publicID,
				Reason: last.reason.String, ErrorClass: last.errorClass.String, Error: last.errorText.String,
				EndedAt: last.ended.Time})
		}
	}
	return out, nil
}

func (b *brokenFake) LockGatewayState(_ context.Context, a dbgen.LockGatewayStateParams) error {
	if err := b.err("LockGatewayState"); err != nil {
		return err
	}
	if a.LockClass != db.GatewayStateLockClass {
		return errors.New("the wrong lock class")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.locks = append(b.locks, a.ID)
	return nil
}

func (b *brokenFake) GetGatewayState(_ context.Context, a dbgen.GetGatewayStateParams) (dbgen.GetGatewayStateRow,
	error) {
	if err := b.err("GetGatewayState"); err != nil {
		return dbgen.GetGatewayStateRow{}, err
	}
	b.f.mu.Lock()
	defer b.f.mu.Unlock()
	for id, gw := range b.f.gateways {
		if a.OrgID == 1 && gw.publicID == a.PublicID {
			return dbgen.GetGatewayStateRow{ID: id, Broken: b.f.broken[id] != nil}, nil
		}
	}
	return dbgen.GetGatewayStateRow{}, pgx.ErrNoRows
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
	// hold makes the probe of a gateway wait until its channel is closed, as an info that hangs.
	hold map[int64]chan struct{}
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
	during, hold := p.during, p.hold[id]
	p.mu.Unlock()
	if during != nil {
		during()
	}
	if hold != nil {
		<-hold
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

// round runs one round of the sender and waits for the probes it started.
func (e *brokenEnv) round(t *testing.T) time.Duration {
	t.Helper()
	next := e.senderEnv.round(t)
	e.s.Broken.Wait()
	return next
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
	lease := e.s.Lease
	lease.Owner = "r2"
	other := &Broken{OrgID: 1, Store: e.b, Lease: lease, Prober: e.prober, Log: e.s.Log}
	e.advance(BrokenProbeInterval)
	// While r1 probes, r2 claims nothing: the lease is live and the next probe moved on.
	e.prober.during = func() {
		if err := other.Probe(t.Context()); err != nil {
			t.Error(err)
		}
		other.Wait()
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
	e.round(t)
	if !strings.Contains(e.log.String(), `"event":"gateway_probe_failed","connection":"CN00000000GW10"`) {
		t.Errorf("a failed record of the probe was not logged:\n%s", e.log)
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
	// Each probe is claimed on its own, and they all run in one round.
	if err := e.s.Broken.Break(t.Context(), brokenGateway{id: 11, publicID: "CN00000000GW11"}, causeFatal,
		"x"); err != nil {
		t.Fatal(err)
	}
	e.advance(time.Hour)
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
// gateway — NotSent: its Connection is gone, its info no longer declares the role gateway — end the call Not delivered
// with gateway_deleted and leave the gateway in order, without MusterNotificationGatewayBroken, also after the
// Leader's reconciliation; an address the outbound policy blocks, a lasting failure without an answer, makes it Broken,
// as a Fatal answer of the gateway does.
func TestBrokenOnlyByTheGateway(t *testing.T) {
	e := newBrokenSender(t)
	for i, out := range []delivery.Outcome{
		{Kind: delivery.OutcomeFatal, Error: outbound.Untrusted(bridge.ErrNoTarget.Error()), NotSent: true},
		{Kind: delivery.OutcomeFatal, Error: bridge.MessageNotGateway, NotSent: true},
	} {
		e.gw.script(out)
		e.page(t, 1, 10, 1)
		e.round(t)
		if err := e.s.Broken.Reconcile(t.Context()); err != nil {
			t.Fatal(err)
		}
		call := e.f.calls[len(e.f.calls)-1]
		if call.state != CallNotDelivered || call.reason.String != groups.PageGatewayDeleted || e.b.state(10) != nil ||
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

// TestProbeHangs is C-30.FR-8 and NFR-2 on the probes: a probe whose info hangs holds one slot of its own, so that the
// calls through a gateway in order go in the same round and the probe of another Broken gateway ends its state; once
// the hanging info answers, its gateway recovers too.
func TestProbeHangs(t *testing.T) {
	e := newBrokenSender(t)
	for _, g := range []brokenGateway{{id: 10, publicID: "CN00000000GW10"}, {id: 12, publicID: "CN00000000BR12"}} {
		if err := e.s.Broken.Break(t.Context(), g, causeFatal, "401"); err != nil {
			t.Fatal(err)
		}
	}
	release := make(chan struct{})
	e.prober.hold = map[int64]chan struct{}{10: release}
	e.advance(BrokenProbeInterval)
	e.page(t, 1, 11, 1)
	e.senderEnv.round(t)
	if e.gw.count() != 1 || e.f.calls[0].state != CallAccepted {
		t.Fatalf("the call through the gateway in order waited for the probe: %+v", e.f.calls[0])
	}
	deadline := time.Now().Add(5 * time.Second)
	for e.b.state(12) != nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if e.b.state(12) != nil || e.b.state(10) == nil {
		t.Fatalf("while one probe hangs: %+v %+v", e.b.state(10), e.b.state(12))
	}
	close(release)
	e.s.Broken.Wait()
	if e.b.state(10) != nil {
		t.Errorf("the hanging probe lost its result: %+v", e.b.state(10))
	}
	// More due probes than slots: the rest wait for a later round.
	e.prober.hold = nil
	block := make(chan struct{})
	e.prober.during = func() { <-block }
	for id := range int64(ProbeConcurrency + 1) {
		e.f.mu.Lock()
		e.f.gateways[100+id] = fakeGateway{publicID: fmt.Sprintf("CN0000000G%04d", id), name: "g", gateway: true}
		e.f.mu.Unlock()
		if err := e.s.Broken.Break(t.Context(), brokenGateway{id: 100 + id}, causeFatal, "x"); err != nil {
			t.Fatal(err)
		}
	}
	e.advance(BrokenProbeInterval)
	if err := e.s.Broken.Probe(t.Context()); err != nil {
		t.Fatal(err)
	}
	waiting := 0
	for id := range int64(ProbeConcurrency + 1) {
		if st := e.b.state(100 + id); st != nil && st.owner == "" {
			waiting++
		}
	}
	close(block)
	e.s.Broken.Wait()
	if waiting != 1 {
		t.Errorf("%d probes wait for a slot, want 1", waiting)
	}
	if err := e.s.Broken.Probe(t.Context()); err != nil {
		t.Fatal(err)
	}
	e.s.Broken.Wait()
	for id := range int64(ProbeConcurrency + 1) {
		if e.b.state(100+id) != nil {
			t.Errorf("gateway %d is still Broken", 100+id)
		}
	}
}

// TestReconcile is C-30.FR-8 and ADR-0007 on the Leader's reconciliation: a call that ended Not delivered by a Fatal
// answer, or by a used budget, without making its gateway Broken — the replica stopped in between — makes it Broken at
// the next run, with the call's error as the reason; running it twice changes nothing more; a failure that the end of
// the gateway's last Broken state followed, one older than ReconcileWindow, an invalid answer and a refusal of Muster
// that never reached the gateway break nothing; an open MusterNotificationGatewayBroken of a gateway in order, as a
// down migration leaves it, and of a deleted or unknown Connection, is resolved, and one of a Broken gateway stays.
func TestReconcile(t *testing.T) {
	e := newBrokenSender(t)
	e.s.Broken = nil // the replica stops between the end of the call and the Broken state
	e.gw.script(forbidden)
	e.page(t, 1, 10, 1)
	e.senderEnv.round(t)
	broken := &Broken{OrgID: 1, Store: e.b, Lease: e.s.Lease, Prober: e.prober, Log: e.s.Log}
	if e.b.state(10) != nil {
		t.Fatal("Broken without the reconciliation")
	}
	for range 2 {
		if err := broken.Reconcile(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if st := e.b.state(10); st == nil || st.reason != "POST notifications: 401 Unauthorized" ||
		len(e.b.alerts()) != 1 || strings.Count(e.log.String(), `"event":"gateway_broken"`) != 1 {
		t.Fatalf("after the reconciliation %+v, alerts %v", st, e.b.alerts())
	}
	// The end of the state follows the failure: it is not made Broken again for it.
	e.advance(time.Second)
	if err := broken.EndBroken(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	if err := broken.Reconcile(t.Context()); err != nil || e.b.state(10) != nil {
		t.Fatalf("broken again for an old failure: %+v, %v", e.b.state(10), err)
	}

	// A used budget, through gateway 11; then failures that break nothing.
	e.advance(2 * CombineWindow)
	e.f.mu.Lock()
	e.f.calls = append(e.f.calls,
		fakeCall{id: 900, publicID: "CA000000000900", conn: 11, user: 2, state: CallNotDelivered,
			reason:     pgtype.Text{String: groups.PageCallBudgetUsed, Valid: true},
			errorClass: pgtype.Text{String: "transient", Valid: true}, errorText: pgtype.Text{String: "busy", Valid: true},
			ended: pgtype.Timestamptz{Time: e.business.Now(), Valid: true}},
		fakeCall{id: 901, publicID: "CA000000000901", conn: 10, user: 2, state: CallNotDelivered,
			reason:     pgtype.Text{String: groups.PageCallRefused, Valid: true},
			errorClass: pgtype.Text{String: "unknown", Valid: true},
			ended:      pgtype.Timestamptz{Time: e.business.Now(), Valid: true}},
		fakeCall{id: 902, publicID: "CA000000000902", conn: 10, user: 2, state: CallNotDelivered,
			reason:     pgtype.Text{String: groups.PageGatewayDeleted, Valid: true},
			errorClass: pgtype.Text{String: "fatal", Valid: true},
			ended:      pgtype.Timestamptz{Time: e.business.Now(), Valid: true}},
		fakeCall{id: 903, publicID: "CA000000000903", conn: 12, user: 2, state: CallNotDelivered,
			reason:     pgtype.Text{String: groups.PageCallRefused, Valid: true},
			errorClass: pgtype.Text{String: "fatal", Valid: true},
			ended:      pgtype.Timestamptz{Time: e.business.Now().Add(-ReconcileWindow), Valid: true}})
	e.f.mu.Unlock()
	if err := broken.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if st := e.b.state(11); st == nil || st.reason != "unavailable after repeated failures: busy" ||
		e.b.state(10) != nil || e.b.state(12) != nil {
		t.Fatalf("after the second reconciliation %+v %+v %+v", e.b.state(10), st, e.b.state(12))
	}

	// Orphan alerts: gateway 11 is Broken and keeps its alert; 10's, a deleted one's and an unknown one's resolve.
	raiser := internalalerts.NewRaiser(1, "")
	for _, id := range []string{"CN00000000GW10", "CN00000000BR12", "CN0000000000XX"} {
		if err := raiser.Raise(t.Context(), e.b, e.business.Now(), internalalerts.NotificationGatewayBroken,
			internalalerts.Entity{ID: id, Name: "x"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	e.b.deleted[12] = true
	for range 2 {
		if err := broken.Reconcile(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	resolved := map[string]int{}
	for _, a := range e.b.alerts() {
		if strings.HasPrefix(a, "resolved/") {
			resolved[strings.Split(a, "/")[2]]++
		}
	}
	if len(resolved) != 3 || resolved["CN00000000GW10"] != 2 || resolved["CN00000000BR12"] != 1 ||
		resolved["CN0000000000XX"] != 1 || resolved["CN00000000GW11"] != 0 {
		t.Errorf("resolved %v", resolved)
	}
	for _, q := range []string{"ListUnmarkedFailures", "GetGatewayState", "LockGatewayState", "InsertInternalBody"} {
		if err := raiser.Raise(t.Context(), e.b, e.business.Now(), internalalerts.NotificationGatewayBroken,
			internalalerts.Entity{ID: "CN00000000GW10", Name: "x"}, nil); err != nil {
			t.Fatal(err)
		}
		e.b.fail[q] = errors.New("down")
		if err := broken.Reconcile(t.Context()); err == nil {
			t.Errorf("a failed %s was ignored", q)
		}
		delete(e.b.fail, q)
		if err := broken.Reconcile(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}

// TestBrokenLocksFirst is the lock order of broken.go: every transaction of the Broken state — a Break, an end by a
// check or a probe, a failed probe, the resolve of an orphan alert — takes the advisory lock of the gateway's state;
// the deletion of the Connection, whose transaction locked its row first, does not.
func TestBrokenLocksFirst(t *testing.T) {
	e := newBrokenSender(t)
	gw := brokenGateway{id: 10, publicID: "CN00000000GW10"}
	if err := e.s.Broken.Break(t.Context(), gw, causeFatal, "401"); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Broken.EndBroken(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Broken.Break(t.Context(), gw, causeFatal, "401"); err != nil {
		t.Fatal(err)
	}
	e.prober.outcomes = []delivery.Outcome{{Kind: delivery.OutcomeTransient, Error: "down"}, {Kind: delivery.OutcomeOK}}
	for range 2 {
		e.advance(BrokenProbeInterval)
		e.round(t)
	}
	if !slices.Equal(e.b.locks, []int64{10, 10, 10, 10, 10}) {
		t.Fatalf("locks %v", e.b.locks)
	}
	if err := e.s.Broken.Break(t.Context(), gw, causeFatal, "401"); err != nil {
		t.Fatal(err)
	}
	e.b.deleted[10] = true
	if err := e.s.Broken.Deleted(t.Context(), nil, 10); err != nil {
		t.Fatal(err)
	}
	if len(e.b.locks) != 6 {
		t.Errorf("the deletion took the lock: %v", e.b.locks)
	}
	if err := internalalerts.NewRaiser(1, "").Raise(t.Context(), e.b, e.business.Now(),
		internalalerts.NotificationGatewayBroken, internalalerts.Entity{ID: "CN00000000GW11", Name: "x"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Broken.Reconcile(t.Context()); err != nil || e.b.locks[len(e.b.locks)-1] != 11 {
		t.Errorf("the resolve of an orphan alert: locks %v, %v", e.b.locks, err)
	}
	e.b.fail["LockGatewayState"] = errors.New("down")
	if err := e.s.Broken.Break(t.Context(), brokenGateway{id: 11, publicID: "CN00000000GW11"}, causeFatal,
		"x"); err == nil || e.b.state(11) != nil {
		t.Errorf("a failed lock: %v", err)
	}
}

// TestBreakSupersededByEnd is C-30.FR-8 on the live path, as on the reconciliation: when an end of the Broken state
// by a probe or a check lands between the end of a call that the gateway refused and that call's Break, which waited
// for the gateway's lock, the failure is superseded and the gateway stays in order; a call refused after the end
// breaks it.
func TestBreakSupersededByEnd(t *testing.T) {
	e := newBrokenSender(t)
	gw := brokenGateway{id: 10, publicID: "CN00000000GW10"}
	e.b.beforeBreak = func() {
		// Another call broke the gateway, and a probe ended the state a second later.
		if err := e.s.Broken.Break(t.Context(), gw, causeFatal, "earlier"); err != nil {
			t.Error(err)
		}
		e.advance(time.Second)
		if err := e.s.Broken.EndBroken(t.Context(), 10); err != nil {
			t.Error(err)
		}
	}
	e.gw.script(forbidden)
	e.page(t, 1, 10, 1)
	e.round(t)
	if call := e.f.calls[0]; call.reason.String != groups.PageCallRefused || e.b.state(10) != nil ||
		len(e.b.alerts()) != 2 {
		t.Fatalf("a failure the end superseded: the call %+v, the state %+v, alerts %v", call, e.b.state(10),
			e.b.alerts())
	}
	e.advance(2 * CombineWindow)
	e.gw.script(forbidden)
	e.page(t, 1, 10, 2)
	e.round(t)
	if st := e.b.state(10); st == nil || st.reason != "POST notifications: 401 Unauthorized" {
		t.Errorf("a failure after the end: %+v", st)
	}
}

// TestProbeSlotsFull is C-30.FR-8 on the sender's waits: while every probe slot holds a probe and more probes are due,
// a round waits for a lease to end or for a slot, not SenderMinWait, so the sender does not poll; a probe that frees
// its slot wakes the sender, whose next round claims the probe that waited.
func TestProbeSlotsFull(t *testing.T) {
	e := newBrokenSender(t)
	for id := range int64(ProbeConcurrency + 1) {
		e.f.mu.Lock()
		e.f.gateways[100+id] = fakeGateway{publicID: fmt.Sprintf("CN0000000G%04d", id), name: "g", gateway: true}
		e.f.mu.Unlock()
		if err := e.s.Broken.Break(t.Context(), brokenGateway{id: 100 + id}, causeFatal, "x"); err != nil {
			t.Fatal(err)
		}
	}
	block := make(chan struct{})
	e.prober.during = func() { <-block }
	e.s.MaxWait = time.Hour
	e.advance(BrokenProbeInterval)
	if next := e.senderEnv.round(t); next <= SenderMinWait || next != SenderLease {
		t.Errorf("with every slot busy the next round is in %v, want the end of a lease, %v", next, SenderLease)
	}
	select {
	case <-e.s.wake:
		t.Fatal("woken while every slot probes")
	default:
	}
	close(block)
	e.s.Broken.Wait()
	select {
	case <-e.s.wake:
	default:
		t.Fatal("a freed slot did not wake the sender")
	}
	e.prober.during = nil
	e.round(t)
	for id := range int64(ProbeConcurrency + 1) {
		if e.b.state(100+id) != nil {
			t.Errorf("gateway %d is still Broken", 100+id)
		}
	}
	if next := e.round(t); next != time.Hour {
		t.Errorf("without a due probe the next round is in %v", next)
	}
}

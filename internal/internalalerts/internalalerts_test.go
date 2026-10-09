// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package internalalerts

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/internalalerts/dbgen"
)

var t0 = time.Date(2026, 10, 6, 12, 0, 0, 123456789, time.UTC)

// fakeStore is the database of the package in memory: the built-in Integration, the open Internal alerts and the
// synthetic Stored Snapshots written.
type fakeStore struct {
	builtin   int64
	open      []dbgen.ListOpenInternalAlertsRow
	bodies    []dbgen.InsertInternalBodyParams
	snapshots []dbgen.InsertInternalSnapshotParams
	notified  []dbgen.NotifyInternalSnapshotParams
	contains  []string
	pending   [][]byte
	fail      map[string]error
}

func (s *fakeStore) ListPendingInternalRaises(_ context.Context, orgID int64) ([][]byte, error) {
	if orgID != 1 {
		return nil, errors.New("another organization")
	}
	return s.pending, s.fail["ListPendingInternalRaises"]
}

func (s *fakeStore) FindBuiltinIntegration(_ context.Context, orgID int64) (int64, error) {
	if err := s.fail["FindBuiltinIntegration"]; err != nil {
		return 0, err
	}
	if s.builtin == 0 || orgID != 1 {
		return 0, pgx.ErrNoRows
	}
	return s.builtin, nil
}

func (s *fakeStore) InsertInternalBody(_ context.Context, arg dbgen.InsertInternalBodyParams) error {
	s.bodies = append(s.bodies, arg)
	return s.fail["InsertInternalBody"]
}

func (s *fakeStore) InsertInternalSnapshot(_ context.Context, arg dbgen.InsertInternalSnapshotParams) error {
	s.snapshots = append(s.snapshots, arg)
	return s.fail["InsertInternalSnapshot"]
}

func (s *fakeStore) NotifyInternalSnapshot(_ context.Context, arg dbgen.NotifyInternalSnapshotParams) error {
	s.notified = append(s.notified, arg)
	return s.fail["NotifyInternalSnapshot"]
}

func (s *fakeStore) ListOpenInternalAlerts(_ context.Context, arg dbgen.ListOpenInternalAlertsParams) (
	[]dbgen.ListOpenInternalAlertsRow, error) {
	s.contains = append(s.contains, string(arg.Contains))
	return s.open, s.fail["ListOpenInternalAlerts"]
}

func newFake() *fakeStore {
	return &fakeStore{builtin: 7, fail: map[string]error{}}
}

// read decodes the i-th synthetic Snapshot as written.
func (s *fakeStore) read(t *testing.T, i int) webhook {
	t.Helper()
	var w webhook
	if err := json.Unmarshal(s.bodies[i].Body, &w); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestRegistry(t *testing.T) {
	defs, err := Definitions()
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 4 || defs[0] != DestinationBroken || defs[1] != HeartbeatLost || defs[2] != SnapshotTruncated ||
		defs[3] != TemplateError || Lookup("MusterSnapshotTruncated") != SnapshotTruncated ||
		Lookup("MusterHeartbeatLost") != HeartbeatLost || Lookup("MusterDestinationBroken") != DestinationBroken ||
		Lookup("MusterTemplateError") != TemplateError || Lookup("MusterUnknown") != nil {
		t.Errorf("registry %v", defs)
	}
	if d := TemplateError; d.Severity != SeverityWarning || d.Capability != "C-12" ||
		!slices.Equal(d.Labels(), []string{"route", "route_name", "destination", "destination_name", "template"}) {
		t.Errorf("MusterTemplateError = %+v", d)
	}
	if d := DestinationBroken; d.Severity != SeverityCritical || d.StaticLabels || d.Capability != "C-11" ||
		!slices.Equal(d.Labels(), []string{"destination", "destination_name"}) {
		t.Errorf("MusterDestinationBroken = %+v", d)
	}
	if h := HeartbeatLost; h.Severity != SeverityCritical || !h.StaticLabels || h.Capability != "C-07" ||
		!slices.Equal(h.Labels(), []string{"integration", "integration_name"}) {
		t.Errorf("MusterHeartbeatLost = %+v", h)
	}
	d := SnapshotTruncated
	if d.Severity != SeverityWarning || !slices.Equal(d.Labels(), []string{"integration", "integration_name"}) ||
		d.Runbook() != "operations/runbooks/MusterSnapshotTruncated/" || d.Capability != "C-06" {
		t.Errorf("MusterSnapshotTruncated = %+v", d)
	}
	if got := (&Definition{Name: "MusterAlone", Extra: []string{"template"}}).Labels(); !slices.Equal(got,
		[]string{"template"}) {
		t.Errorf("labels without an entity = %v", got)
	}
}

func TestRegistryChecks(t *testing.T) {
	saved := registry
	t.Cleanup(func() { registry = saved })
	text := func(d *Definition) *Definition {
		d.Condition, d.Summary, d.Description, d.Capability = "c", "s", "d", "C-06"
		return d
	}
	registry = []*Definition{
		text(&Definition{Name: "Bad", Severity: SeverityWarning}),
		text(&Definition{Name: "MusterTwice", Severity: SeverityWarning}),
		text(&Definition{Name: "MusterTwice", Severity: "page"}),
		text(&Definition{Name: "MusterNameless", Severity: SeverityWarning, NameLabel: "x_name"}),
		text(&Definition{Name: "MusterRoute", Severity: SeverityCritical, Entity: EntityRoute, NameLabel: "name"}),
		text(&Definition{Name: "MusterThing", Severity: SeverityCritical, Entity: "thing", NameLabel: "thing_name"}),
		text(&Definition{Name: "MusterLabels", Severity: SeverityCritical, Extra: []string{"severity", "a", "a"}}),
		{Name: "MusterQuiet", Severity: SeverityCritical},
		text(&Definition{Name: "MusterGood", Severity: SeverityCritical, Entity: EntityDestination,
			NameLabel: "destination_name", Extra: []string{"template"}}),
		text(&Definition{Name: "MusterStatic", Severity: SeverityCritical, Entity: EntityRoute,
			NameLabel: "route_name", StaticLabels: true}),
	}
	defs, err := Definitions()
	for _, want := range []string{`"Bad": the name is not`, `"MusterTwice" is registered twice`,
		`"MusterTwice": unknown severity "page"`, `"MusterNameless": a name label without an entity`,
		`"MusterRoute": the name label of route is route_name`, `"MusterThing": unknown entity "thing"`,
		`"MusterLabels": invalid label "severity"`, `"MusterLabels": a label is listed twice`,
		`"MusterQuiet": condition, summary`, `"MusterStatic": Static labels need the integration entity`} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error %v lacks %q", err, want)
		}
	}
	if err != nil && strings.Contains(err.Error(), "MusterGood") {
		t.Errorf("a valid definition was refused: %v", err)
	}
	if len(defs) != len(registry) || defs[0].Name != "Bad" {
		t.Errorf("sorted %v", defs)
	}
}

// TestRaiseWithStaticLabels covers C-07.FR-4 on the raise: MusterHeartbeatLost carries the Integration's Static
// labels, its own labels winning over a Static label of the same name, and its fingerprint is that of alertname and
// the Integration's id alone.
func TestRaiseWithStaticLabels(t *testing.T) {
	q := newFake()
	r := NewRaiser(1, "")
	static := map[string]string{"env": "prod", "severity": "low", "integration": "other", "integration_name": "x",
		"alertname": "Other"}
	if err := r.Raise(t.Context(), q, t0, HeartbeatLost, Entity{ID: "NTAAAAAAAAAAAA", Name: "hb"},
		static); err != nil {
		t.Fatal(err)
	}
	a := q.read(t, 0).Alerts[0]
	want := map[string]string{"alertname": "MusterHeartbeatLost", "severity": "critical",
		"integration": "NTAAAAAAAAAAAA", "integration_name": "hb", "env": "prod"}
	if !mapsEqual(a.Labels, want) || a.Fingerprint != Fingerprint(HeartbeatLost, map[string]string{
		"integration": "NTAAAAAAAAAAAA"}) || a.Annotations["summary"] != "No Heartbeat from the Alertmanager of "+
		"Integration hb" || a.Annotations["runbook_url"] != DefaultRunbookBase+"/operations/runbooks/MusterHeartbeatLost/" {
		t.Errorf("alert %+v", a)
	}
}

// TestRaise: a raise is a synthetic Stored Snapshot of the built-in Integration in Alertmanager format, with the
// groupKey of its Internal alert, the labels, the annotations with the runbook, startsAt the time of the raise and the
// fingerprint of alertname and the id label; the workers are woken.
func TestRaise(t *testing.T) {
	q := newFake()
	r := NewRaiser(1, "https://docs.example.org/muster/")
	if err := r.Raise(t.Context(), q, t0, SnapshotTruncated, Entity{ID: "NTAAAAAAAAAAAA", Name: "lab"},
		nil); err != nil {
		t.Fatal(err)
	}
	if len(q.snapshots) != 1 || len(q.notified) != 1 {
		t.Fatalf("snapshots %v, notified %v", q.snapshots, q.notified)
	}
	sn, body := q.snapshots[0], q.bodies[0]
	at := t0.Truncate(time.Microsecond)
	if sn.IntegrationID != 7 || sn.OrgID != 1 || !sn.ReceivedAt.Equal(at) || sn.SizeBytes != int64(len(body.Body)) ||
		!strings.HasPrefix(sn.PublicID, "SS") || string(sn.BodySha256) != string(body.BodySha256) ||
		sn.BodyDay.Time.Format(time.DateOnly) != "2026-10-06" || body.BodyDay != sn.BodyDay {
		t.Errorf("snapshot %+v, body %+v", sn, body)
	}
	if q.notified[0].Channel != SnapshotChannel || q.notified[0].Payload != `{"org_id":1,"integration_id":7}` {
		t.Errorf("notified %+v", q.notified[0])
	}
	w := q.read(t, 0)
	labels := map[string]string{"alertname": "MusterSnapshotTruncated", "severity": "warning",
		"integration": "NTAAAAAAAAAAAA", "integration_name": "lab"}
	a := w.Alerts[0]
	if w.Version != "4" || w.GroupKey != `{}/{muster="internal"}:{alertname="MusterSnapshotTruncated"}` ||
		w.Status != StatusFiring || w.Receiver != "muster" || len(w.Alerts) != 1 || a.Status != StatusFiring ||
		!mapsEqual(a.Labels, labels) || a.StartsAt != at.Format(time.RFC3339Nano) || a.EndsAt != zeroTime ||
		a.Fingerprint != Fingerprint(SnapshotTruncated, labels) {
		t.Errorf("webhook %+v", w)
	}
	if a.Annotations["summary"] != "Alertmanager truncates the Snapshots of Integration lab" ||
		!strings.Contains(a.Annotations["description"], "for Integration lab:") ||
		a.Annotations["runbook_url"] != "https://docs.example.org/muster/operations/runbooks/MusterSnapshotTruncated/" {
		t.Errorf("annotations %v", a.Annotations)
	}
	if strings.Contains(string(body.Body), `<`) || strings.HasSuffix(string(body.Body), "\n") {
		t.Errorf("body %s", body.Body)
	}
	// Extra labels of a definition are carried; a definition without an entity carries none.
	d := &Definition{Name: "MusterOther", Severity: SeverityCritical, Extra: []string{"template"},
		Summary: "s {name}", Description: "d"}
	if err := r.Raise(t.Context(), q, t0, d, Entity{}, map[string]string{"template": "line"}); err != nil {
		t.Fatal(err)
	}
	if got := q.read(t, 1).Alerts[0]; !mapsEqual(got.Labels, map[string]string{"alertname": "MusterOther",
		"severity": "critical", "template": "line"}) || got.Annotations["summary"] != "s " {
		t.Errorf("alert %+v", got)
	}
}

func TestFingerprint(t *testing.T) {
	a := map[string]string{"alertname": "MusterSnapshotTruncated", "integration": "NTAAAAAAAAAAAA",
		"integration_name": "lab", "severity": "warning"}
	b := map[string]string{"integration": "NTAAAAAAAAAAAA", "integration_name": "lab-eu"}
	c := map[string]string{"integration": "NTBBBBBBBBBBBB", "integration_name": "lab"}
	fa, fb, fc := Fingerprint(SnapshotTruncated, a), Fingerprint(SnapshotTruncated, b), Fingerprint(SnapshotTruncated, c)
	if fa != fb || fa == fc || len(fa) != 16 {
		t.Errorf("fingerprints %s %s %s", fa, fb, fc)
	}
	// Pinned: a change would start new Internal alerts for the open ones after an upgrade.
	if fa != "3807b3b3ec75888c" {
		t.Errorf("fingerprint %s", fa)
	}
}

func TestResolve(t *testing.T) {
	q := newFake()
	r := NewRaiser(1, "")
	if err := r.Resolve(t.Context(), q, t0, SnapshotTruncated, "NTAAAAAAAAAAAA"); err != nil {
		t.Fatal(err)
	}
	w := q.read(t, 0)
	a := w.Alerts[0]
	if w.Status != StatusResolved || a.Status != StatusResolved || a.Labels["integration"] != "NTAAAAAAAAAAAA" ||
		a.Labels["integration_name"] != "" || len(a.Annotations) != 0 ||
		a.Fingerprint != Fingerprint(SnapshotTruncated, a.Labels) ||
		a.StartsAt != t0.Truncate(time.Microsecond).Format(time.RFC3339Nano) {
		t.Errorf("webhook %+v", w)
	}
	if r.base != DefaultRunbookBase {
		t.Errorf("base %s", r.base)
	}
}

// TestRenamed: every open Internal alert about the entity is raised again with the new name and its startsAt; one
// with the name already, of another entity kind, or of an unknown alertname is left alone.
func TestRenamed(t *testing.T) {
	q := newFake()
	started := t0.Add(-time.Hour)
	row := func(labels string) dbgen.ListOpenInternalAlertsRow {
		return dbgen.ListOpenInternalAlertsRow{Fingerprint: labels, Labels: []byte(labels), StartsAt: started}
	}
	q.open = []dbgen.ListOpenInternalAlertsRow{
		row(`{"alertname":"MusterSnapshotTruncated","severity":"warning","integration":"NTA","integration_name":"lab"}`),
		row(`{"alertname":"MusterSnapshotTruncated","integration":"NTA","integration_name":"lab-eu"}`),
		row(`{"alertname":"MusterUnknown","integration":"NTA"}`),
	}
	r := NewRaiser(1, "")
	if err := r.Renamed(t.Context(), q, t0, EntityIntegration, "NTA", "lab-eu"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(q.contains, []string{`{"integration":"NTA"}`}) || len(q.snapshots) != 1 {
		t.Fatalf("contains %v, snapshots %v", q.contains, q.snapshots)
	}
	a := q.read(t, 0).Alerts[0]
	if a.Labels["integration_name"] != "lab-eu" || a.Labels["integration"] != "NTA" ||
		a.StartsAt != started.Truncate(time.Microsecond).Format(time.RFC3339Nano) ||
		a.Annotations["summary"] != "Alertmanager truncates the Snapshots of Integration lab-eu" ||
		!q.snapshots[0].ReceivedAt.Equal(t0.Truncate(time.Microsecond)) {
		t.Errorf("alert %+v", a)
	}
	if err := r.Renamed(t.Context(), q, t0, EntityRoute, "NTA", "x"); err != nil || len(q.snapshots) != 1 {
		t.Errorf("another entity kind: %v, %d", err, len(q.snapshots))
	}
	q.open = []dbgen.ListOpenInternalAlertsRow{row(`not json`)}
	if err := r.Renamed(t.Context(), q, t0, EntityIntegration, "NTA", "x"); err == nil {
		t.Error("labels that are not JSON were accepted")
	}
	q.fail["ListOpenInternalAlerts"] = errors.New("down")
	if err := r.Renamed(t.Context(), q, t0, EntityIntegration, "NTA", "x"); err == nil {
		t.Error("a failed read was ignored")
	}
}

// TestTemplateErrorOfDestination: MusterTemplateError about an outgoing webhook carries the Destination's labels and
// texts and no Route label, its fingerprint is not that of a Route with the same id, and it follows a rename and the
// deletion of the Destination; the other entity of another alert falls back to its own.
func TestTemplateErrorOfDestination(t *testing.T) {
	q := newFake()
	r := NewRaiser(1, "https://docs.example.org")
	ctx := t.Context()
	e := Entity{ID: "DSA", Name: "chat"}
	if err := r.RaiseFor(ctx, q, t0, TemplateError, EntityDestination, e,
		map[string]string{"template": "webhook_request"}); err != nil {
		t.Fatal(err)
	}
	a := q.read(t, 0).Alerts[0]
	if a.Labels["destination"] != "DSA" || a.Labels["destination_name"] != "chat" || a.Labels["template"] !=
		"webhook_request" || a.Labels["route"] != "" || a.Labels["route_name"] != "" ||
		a.Annotations["summary"] != "A request template of Destination chat keeps failing" ||
		a.Fingerprint == Fingerprint(TemplateError, map[string]string{"route": "DSA"}) {
		t.Errorf("raise %+v", a)
	}
	if err := r.ResolveFor(ctx, q, t0, TemplateError, EntityDestination, "DSA"); err != nil {
		t.Fatal(err)
	}
	if res := q.read(t, 1).Alerts[0]; res.Fingerprint != a.Fingerprint || res.Status != StatusResolved {
		t.Errorf("resolve %+v", res)
	}
	if err := r.RaiseFor(ctx, q, t0, TemplateError, EntityRoute, Entity{ID: "RTA", Name: "r"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.ResolveFor(ctx, q, t0, TemplateError, EntityRoute, "RTA"); err != nil {
		t.Fatal(err)
	}
	if a, res := q.read(t, 2).Alerts[0], q.read(t, 3).Alerts[0]; a.Labels["route"] != "RTA" ||
		a.Labels["destination"] != "" || res.Fingerprint != a.Fingerprint ||
		a.Fingerprint != Fingerprint(TemplateError, map[string]string{"route": "RTA"}) {
		t.Errorf("route raise %+v, resolve %+v", a, res)
	}
	q.open = []dbgen.ListOpenInternalAlertsRow{{Fingerprint: "f", StartsAt: t0,
		Labels: []byte(`{"alertname":"MusterTemplateError","destination":"DSA","destination_name":"chat"}`)}}
	if err := r.Renamed(ctx, q, t0, EntityDestination, "DSA", "chat-2"); err != nil {
		t.Fatal(err)
	}
	if a := q.read(t, 4).Alerts[0]; a.Labels["destination_name"] != "chat-2" ||
		a.Annotations["summary"] != "A request template of Destination chat-2 keeps failing" {
		t.Errorf("renamed %+v", a)
	}
	if err := r.ResolveAbout(ctx, q, t0, EntityDestination, "DSA"); err != nil {
		t.Fatal(err)
	}
	if res := q.read(t, 5).Alerts[0]; res.Status != StatusResolved || res.Labels["destination"] != "DSA" {
		t.Errorf("deleted %+v", res)
	}
	if err := r.ResolveAbout(ctx, q, t0, EntityIntegration, "DSA"); err != nil || len(q.bodies) != 6 {
		t.Errorf("another entity: %v, %d", err, len(q.bodies))
	}
	if err := r.RaiseFor(ctx, q, t0, DestinationBroken, EntityRoute, e, nil); err != nil {
		t.Fatal(err)
	}
	if a := q.read(t, 6).Alerts[0]; a.Labels["destination"] != "DSA" {
		t.Errorf("an alert without another entity %+v", a)
	}
}

// TestRenamedPending: a raise that still waits for processing is raised again with the new name after it; one whose
// resolve waits behind it is not; markers and bodies that processing will fail are skipped.
func TestRenamedPending(t *testing.T) {
	q := newFake()
	r := NewRaiser(1, "")
	ctx := t.Context()
	raised := t0.Add(-time.Minute)
	if err := r.Raise(ctx, q, raised, SnapshotTruncated, Entity{ID: "NTA", Name: "lab"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.Raise(ctx, q, raised, SnapshotTruncated, Entity{ID: "NTB", Name: "other"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.Raise(ctx, q, raised, SnapshotTruncated, Entity{ID: "NTC", Name: "gone"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.Resolve(ctx, q, raised, SnapshotTruncated, "NTC"); err != nil {
		t.Fatal(err)
	}
	if err := r.MarkDeleted(ctx, q, raised, 3, "NTA", "lab"); err != nil {
		t.Fatal(err)
	}
	for _, b := range q.bodies {
		q.pending = append(q.pending, b.Body)
	}
	q.pending = append(q.pending, []byte("not json"))
	q.bodies, q.snapshots = nil, nil
	for _, id := range []string{"NTA", "NTC"} {
		if err := r.Renamed(ctx, q, t0, EntityIntegration, id, "renamed"); err != nil {
			t.Fatal(err)
		}
	}
	if len(q.snapshots) != 1 {
		t.Fatalf("raises %d", len(q.snapshots))
	}
	a := q.read(t, 0).Alerts[0]
	if a.Labels["integration"] != "NTA" || a.Labels["integration_name"] != "renamed" ||
		a.StartsAt != raised.Truncate(time.Microsecond).Format(time.RFC3339Nano) {
		t.Errorf("raise %+v", a)
	}
	q.fail["ListPendingInternalRaises"] = errors.New("down")
	if err := r.Renamed(ctx, q, t0, EntityIntegration, "NTA", "x"); err == nil {
		t.Error("a failed read was ignored")
	}
}

func TestMarkDeleted(t *testing.T) {
	q := newFake()
	r := NewRaiser(1, "")
	if err := r.MarkDeleted(t.Context(), q, t0, 42, "NTA", "lab-eu"); err != nil {
		t.Fatal(err)
	}
	if len(q.snapshots) != 1 || q.snapshots[0].IntegrationID != 42 || q.notified[0].Payload !=
		`{"org_id":1,"integration_id":42}` {
		t.Fatalf("snapshots %+v", q.snapshots)
	}
	d, ok := DeletionOf(q.bodies[0].Body)
	if !ok || d != (Deletion{IntegrationID: "NTA", Name: "lab-eu"}) {
		t.Errorf("deletion %+v, %v", d, ok)
	}
	w := q.read(t, 0)
	if w.GroupKey != DeletionGroupKey || w.Status != StatusResolved || len(w.Alerts) != 0 ||
		w.CommonAnnotations["summary"] != "Integration lab-eu deleted" {
		t.Errorf("marker %+v", w)
	}
	for _, body := range []string{`not json`, `{"groupKey":"{}:{}"}`} {
		if _, ok := DeletionOf([]byte(body)); ok {
			t.Errorf("%s read as a deletion", body)
		}
	}
}

func TestRaiseErrors(t *testing.T) {
	r := NewRaiser(1, "")
	raise := func(q *fakeStore) error {
		return r.Raise(t.Context(), q, t0, SnapshotTruncated, Entity{ID: "NTA", Name: "lab"}, nil)
	}
	q := newFake()
	q.builtin = 0
	if err := raise(q); !errors.Is(err, ErrNoBuiltin) {
		t.Errorf("without the built-in integration = %v", err)
	}
	if err := r.Resolve(t.Context(), q, t0, SnapshotTruncated, "NTA"); !errors.Is(err, ErrNoBuiltin) {
		t.Errorf("resolve without the built-in integration = %v", err)
	}
	for _, name := range []string{"FindBuiltinIntegration", "InsertInternalBody", "InsertInternalSnapshot",
		"NotifyInternalSnapshot"} {
		q := newFake()
		q.fail[name] = errors.New("down")
		if err := raise(q); err == nil || !strings.Contains(err.Error(), "down") {
			t.Errorf("%s failing: %v", name, err)
		}
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

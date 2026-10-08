// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package messages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	idb "github.com/muster-io/muster/internal/internalalerts/dbgen"
	"github.com/muster-io/muster/internal/links"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/messages/dbgen"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/templates"
)

var errBoom = errors.New("boom")

// fakeQueries keep one Alert Group, its Route, the Route's Stored Snapshots and its template error in memory, and
// record the synthetic Stored Snapshots of Internal alerts.
type fakeQueries struct {
	group     *Source
	snapshots [][]byte
	entry     dbgen.GetReplyEntryRow
	routeErr  string
	locked    bool
	internal  []string
	fail      map[string]error
	retention int64
}

func (f *fakeQueries) err(name string) error { return f.fail[name] }

func (f *fakeQueries) GetRenderGroup(_ context.Context, arg dbgen.GetRenderGroupParams) (dbgen.GetRenderGroupRow,
	error) {
	if err := f.err("GetRenderGroup"); err != nil {
		return dbgen.GetRenderGroupRow{}, err
	}
	s := f.group
	if s == nil || arg.ID != s.ID {
		return dbgen.GetRenderGroupRow{}, pgx.ErrNoRows
	}
	js := func(m map[string]string) []byte { b, _ := json.Marshal(m); return b }
	text := func(p *string) pgtype.Text {
		if p == nil {
			return pgtype.Text{}
		}
		return pgtype.Text{String: *p, Valid: true}
	}
	row := dbgen.GetRenderGroupRow{ID: s.ID, PublicID: s.PublicID, Number: s.Number, Title: s.Title,
		Summary: pgtype.Text{String: s.Summary, Valid: s.Summary != ""}, Status: s.Status,
		SeverityLevel: s.SeverityLevel, GroupKeyLabels: s.KeyLabels, GroupKeyValues: js(s.KeyValues),
		CommonLabels: js(s.CommonLabels), CommonAnnotations: js(s.CommonAnnotations), FiringAlertCount: s.FiringCount,
		ReopenCount: s.ReopenCount, ResolvedByKind: pgtype.Text{String: s.ResolvedByKind, Valid: s.ResolvedByKind != ""},
		ResolveReason: pgtype.Text{String: s.ResolveReason, Valid: s.ResolveReason != ""}, CreatedAt: s.StartedAt,
		RouteID: s.Route.ID, RoutePublicID: s.Route.PublicID, RouteName: s.Route.Name, Language: s.Route.Language,
		SnoozeDurationsSeconds: s.Route.SnoozeDurations, TemplateRootMessage: text(s.Route.RootMessage),
		TemplateLine: text(s.Route.Line), TemplateErrorTemplate: pgtype.Text{String: f.routeErr,
			Valid: f.routeErr != ""}, TimeZone: s.TimeZone, OwnerName: s.Owner}
	if s.SnoozeUntil != nil {
		row.SnoozeUntil = pgtype.Timestamptz{Time: *s.SnoozeUntil, Valid: true}
	}
	return row, nil
}

func (f *fakeQueries) GetGroupIDByPublicID(_ context.Context, arg dbgen.GetGroupIDByPublicIDParams) (int64, error) {
	if f.group == nil || arg.PublicID != f.group.PublicID {
		return 0, pgx.ErrNoRows
	}
	return f.group.ID, f.err("GetGroupIDByPublicID")
}

func (f *fakeQueries) ListRenderAlerts(context.Context, dbgen.ListRenderAlertsParams) ([]dbgen.ListRenderAlertsRow,
	error) {
	var out []dbgen.ListRenderAlertsRow
	for _, a := range f.group.Alerts {
		labels, _ := json.Marshal(a.Labels)
		annotations, _ := json.Marshal(a.Annotations)
		row := dbgen.ListRenderAlertsRow{Fingerprint: a.Fingerprint, Labels: labels, Annotations: annotations,
			StartsAt: a.StartsAt, State: "resolved", Total: int64(len(f.group.Alerts)),
			GeneratorUrl: pgtype.Text{String: a.GeneratorURL, Valid: true}}
		if a.Firing {
			row.State = "firing"
		} else {
			row.EndedAt = pgtype.Timestamptz{Time: a.StartsAt.Add(time.Minute), Valid: true}
		}
		out = append(out, row)
	}
	return out, f.err("ListRenderAlerts")
}

func (f *fakeQueries) ListAlertsByFingerprint(_ context.Context, arg dbgen.ListAlertsByFingerprintParams) (
	[]dbgen.ListAlertsByFingerprintRow, error) {
	var out []dbgen.ListAlertsByFingerprintRow
	for _, a := range f.group.Alerts {
		for _, fp := range arg.Fingerprints {
			if fp == a.Fingerprint {
				labels, _ := json.Marshal(a.Labels)
				out = append(out, dbgen.ListAlertsByFingerprintRow{Fingerprint: fp, Labels: labels})
			}
		}
	}
	return out, f.err("ListAlertsByFingerprint")
}

func (f *fakeQueries) GetReplyEntry(context.Context, dbgen.GetReplyEntryParams) (dbgen.GetReplyEntryRow, error) {
	return f.entry, f.err("GetReplyEntry")
}

func (f *fakeQueries) GetPreviewRoute(_ context.Context, arg dbgen.GetPreviewRouteParams) (dbgen.GetPreviewRouteRow,
	error) {
	if f.group == nil || arg.PublicID != f.group.Route.PublicID {
		return dbgen.GetPreviewRouteRow{}, pgx.ErrNoRows
	}
	r := f.group.Route
	return dbgen.GetPreviewRouteRow{ID: r.ID, PublicID: r.PublicID, Name: r.Name, Language: r.Language,
		SnoozeDurationsSeconds: r.SnoozeDurations}, f.err("GetPreviewRoute")
}

func (f *fakeQueries) GetRenderSettings(context.Context, int64) (dbgen.GetRenderSettingsRow, error) {
	return dbgen.GetRenderSettingsRow{TimeZone: "UTC", Name: "Default", Language: "en",
		SnoozeDurationsSeconds: []int64{3600}}, f.err("GetRenderSettings")
}

func (f *fakeQueries) ListRouteSnapshots(_ context.Context, arg dbgen.ListRouteSnapshotsParams) (
	[]dbgen.ListRouteSnapshotsRow, error) {
	var out []dbgen.ListRouteSnapshotsRow
	for i, b := range f.snapshots {
		if int32(i) < arg.Lim {
			out = append(out, dbgen.ListRouteSnapshotsRow{PublicID: fmt.Sprintf("SS%012d", i), Body: b})
		}
	}
	return out, f.err("ListRouteSnapshots")
}

func (f *fakeQueries) GetSnapshotBody(_ context.Context, arg dbgen.GetSnapshotBodyParams) ([]byte, error) {
	if len(f.snapshots) == 0 || arg.PublicID != "SS000000000000" {
		return nil, pgx.ErrNoRows
	}
	return f.snapshots[0], f.err("GetSnapshotBody")
}

func (f *fakeQueries) GetSnapshotRetention(context.Context, int64) (int64, error) {
	return f.retention, f.err("GetSnapshotRetention")
}

func (f *fakeQueries) SetTemplateError(_ context.Context, arg dbgen.SetTemplateErrorParams) ([]string, error) {
	if err := f.err("SetTemplateError"); err != nil {
		return nil, err
	}
	if f.routeErr != "" || f.locked {
		return []string{}, nil
	}
	f.routeErr = arg.Template
	return []string{f.group.Route.Name}, nil
}

func (f *fakeQueries) ClearTemplateError(_ context.Context, arg dbgen.ClearTemplateErrorParams) ([]string, error) {
	if err := f.err("ClearTemplateError"); err != nil {
		return nil, err
	}
	if f.routeErr != arg.Template || f.locked {
		return []string{}, nil
	}
	f.routeErr = ""
	return []string{f.group.Route.PublicID}, nil
}

func (f *fakeQueries) FindBuiltinIntegration(context.Context, int64) (int64, error) {
	return 1, f.err("FindBuiltinIntegration")
}

func (f *fakeQueries) InsertInternalBody(_ context.Context, arg idb.InsertInternalBodyParams) error {
	f.internal = append(f.internal, string(arg.Body))
	return nil
}

func (*fakeQueries) InsertInternalSnapshot(context.Context, idb.InsertInternalSnapshotParams) error {
	return nil
}
func (*fakeQueries) NotifyInternalSnapshot(context.Context, idb.NotifyInternalSnapshotParams) error {
	return nil
}
func (*fakeQueries) ListOpenInternalAlerts(context.Context, idb.ListOpenInternalAlertsParams) (
	[]idb.ListOpenInternalAlertsRow, error) {
	return nil, nil
}
func (*fakeQueries) ListPendingInternalRaises(context.Context, int64) ([][]byte, error) {
	return nil, nil
}

// withFake is a Renderer over the fake queries.
func withFake(t *testing.T, f *fakeQueries) *Renderer {
	t.Helper()
	r := newRenderer(t)
	r.queries = func(DBTX) queries { return f }
	if f.fail == nil {
		f.fail = map[string]error{}
	}
	if f.retention == 0 {
		f.retention = 14
	}
	return r
}

func ptr(s string) *string { return &s }

// TestRouteTemplates is C-12.FR-2: a Route's root_message template replaces only the body — the heading, links,
// notices, footer and buttons stay Muster's — and its line template replaces the default line of each Alert, with
// alert data escaped for the markup; muster_template_render_duration_seconds is observed for every render.
func TestRouteTemplates(t *testing.T) {
	r := newRenderer(t)
	before := histogramCount(t, "root_message") + histogramCount(t, "line")
	src := source("acknowledged", 3)
	src.Owner = "Alice"
	src.Route.RootMessage = ptr(`{{ .AlertGroup.Title }} has {{ len .Alerts.Firing }} firing; ` +
		`owner {{ .CommonLabels.owner }} {{ .CommonLabels.team }}`)
	src.CommonLabels["team"] = "web_ops"
	out := r.Root(src, MarkupMarkdown)
	m := out.Message
	if out.Failure != nil || m.Body == nil || m.Body.Text != "PodDown has 2 firing; owner @\u200bchannel web\\_ops" ||
		m.Environment != "" || m.GroupLabels != nil || m.Alerts != nil || m.Footer != "Acknowledged by Alice" ||
		len(m.Buttons) != 5 || len(m.Links) != 1 || m.Heading.Title != "PodDown" || len(out.Rendered) != 1 {
		t.Errorf("root template %+v", out)
	}
	src = source("firing", 3)
	src.Route.Line = ptr(`{{ .Labels.pod }} on {{ .Labels.cluster }} of #{{ .AlertGroup.Number }}`)
	out = r.Root(src, MarkupHTML)
	lines := out.Message.Alerts.Lines
	if out.Failure != nil || len(lines) != 3 || lines[0].Text != "p1 on prod of #12" || lines[0].Markup != MarkupHTML ||
		out.Message.Environment == "" || len(out.Rendered) != 1 || out.Rendered[0] != "line" {
		t.Errorf("line template %+v", out)
	}
	if !strings.Contains(Layout(out.Message, MarkupHTML), "• <s>p2 on prod of #12</s>") {
		t.Errorf("layout %s", Layout(out.Message, MarkupHTML))
	}
	// Above message.alerts_listed the distinct values stand in: the line template does not render.
	src = source("firing", 11)
	src.Route.Line = ptr(`{{ .Labels.pod }}`)
	if out := r.Root(src, MarkupMarkdown); len(out.Rendered) != 0 || out.Message.Alerts.FullList == nil {
		t.Errorf("many alerts %+v", out)
	}
	if after := histogramCount(t, "root_message") + histogramCount(t, "line"); after < before+3+3 {
		t.Errorf("render durations observed %d times", after-before)
	}
}

// histogramCount is the number of observations of muster_template_render_duration_seconds for a kind.
func histogramCount(t *testing.T, kind string) uint64 {
	t.Helper()
	rec := httptest.NewRecorder()
	metrics.Handler(func() bool { return true }).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(),
		http.MethodGet, "/metrics", nil))
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if strings.HasPrefix(line, `muster_template_render_duration_seconds_count{template="`+kind+`"}`) {
			var n uint64
			_, _ = fmt.Sscanf(line[strings.LastIndex(line, " ")+1:], "%d", &n)
			return n
		}
	}
	return 0
}

// TestFallbackTemplate is C-12.FR-6: a Route template that fails produces the Fallback template — every label of the
// Alert Group and of each Alert under Muster's heading, footer and buttons — and the failure with its position; the
// Fallback template never fails, whatever the labels.
func TestFallbackTemplate(t *testing.T) {
	r := newRenderer(t)
	src := source("firing", 2)
	src.Route.RootMessage = ptr("{{ index .Alerts 5 }}")
	out := r.Root(src, MarkupMarkdown)
	if out.Failure == nil || out.Failure.Template != "root_message" || out.Failure.Error.Line != 1 ||
		!strings.HasPrefix(out.Failure.Detail(), "root_message template failed: line 1, column") {
		t.Fatalf("failure %+v", out.Failure)
	}
	text := Layout(out.Message, MarkupMarkdown)
	for _, want := range []string{"🔴 [#12 PodDown]", "The message template of this Route failed",
		"prometheus: mon/k8s", "severity: critical", `- alertname=PodDown, cluster=prod, namespace=shop, owner=@`,
		"pod=p1", "~~alertname=PodDown", "[Open in Muster]", "[Ack] [Resolve] [Snooze 1 h]"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	src = source("firing", 2)
	src.Route.Line = ptr(`{{ .Labels.pod | nosuch }}`)
	if out := r.Root(src, MarkupHTML); out.Failure == nil || out.Failure.Template != "line" ||
		out.Failure.Error.Code != templates.CodeUnknownFunction || out.Message.Notices[0] == "" {
		t.Errorf("line failure %+v", out)
	}
	// Fuzzed labels: every label shows, the layout never fails, in every markup.
	rnd := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // a fixed seed makes the fuzzed labels repeatable
	chars := []rune("aZ09_-@<>&*[]`~|\\\"'é😀\u200b\n ")
	word := func() string {
		w := make([]rune, rnd.IntN(12))
		for i := range w {
			w[i] = chars[rnd.IntN(len(chars))]
		}
		return string(w)
	}
	for range 200 {
		src := source("firing", 0)
		src.Route.RootMessage = ptr("{{ fail }}")
		src.CommonLabels, src.KeyValues = map[string]string{word(): word()}, map[string]string{word(): word()}
		for range rnd.IntN(15) {
			labels := map[string]string{}
			for range rnd.IntN(5) {
				labels[word()] = word()
			}
			src.Alerts = append(src.Alerts, SourceAlert{Labels: labels, Firing: rnd.IntN(2) == 0})
		}
		src.TotalAlerts = int64(len(src.Alerts)) + int64(rnd.IntN(3))
		for _, mk := range []Markup{MarkupMarkdown, MarkupHTML, MarkupPlain} {
			out := r.Root(src, mk)
			if out.Failure == nil || out.Message.Heading == nil || len(out.Message.Buttons) == 0 {
				t.Fatalf("fallback %+v", out)
			}
			_ = Layout(out.Message, mk)
		}
	}
}

// TestSettle is the template error state of C-12.FR-6 and C-12.AC-3: a failure records the Route's template error
// once, raises MusterTemplateError and, once committed, counts muster_template_errors_total and logs
// fallback_template_used; the next successful render of that template clears the error, resolves the Internal alert
// and logs template_recovered; saving a new template clears it too.
func TestSettle(t *testing.T) {
	f := &fakeQueries{group: source("firing", 2)}
	r := withFake(t, f)
	var log strings.Builder
	r.log = logging.New(&log, logging.LevelInfo)
	before := counter(t)
	fail := Failure{Template: "root_message", Error: &templates.Error{Code: "template_syntax", Line: 3, Detail: "x"}}
	after, err := r.Settle(t.Context(), nil, f.group, []Failure{fail}, nil)
	if err != nil || f.routeErr != "root_message" || len(f.internal) != 1 ||
		!strings.Contains(f.internal[0], `"status":"firing"`) || !strings.Contains(f.internal[0], "MusterTemplateError") ||
		!strings.Contains(f.internal[0], `"template":"root_message"`) {
		t.Fatalf("settle = %v, error %q, internal %v", err, f.routeErr, f.internal)
	}
	if counter(t) != before || strings.Contains(log.String(), "fallback_template_used") {
		t.Error("counted or logged before the commit")
	}
	after(t.Context())
	if counter(t) != before+1 || !strings.Contains(log.String(), `"event":"fallback_template_used","route":"RTAAAAAAAAAAA1",`+
		`"group":"AGK7M3QX9P2RTA","template":"root_message","error":"line 3: x"`) {
		t.Errorf("after commit: %v, log %s", counter(t)-before, log.String())
	}
	// A second failure while the error is set raises nothing more.
	if _, err := r.Settle(t.Context(), nil, f.group, []Failure{fail}, nil); err != nil || len(f.internal) != 1 {
		t.Errorf("second failure: %v, %d", err, len(f.internal))
	}
	// Another template rendering does not clear it; the failing one does.
	f.group.Route.ErrorTemplate = "root_message"
	if _, err := r.Settle(t.Context(), nil, f.group, nil, []string{"line"}); err != nil || f.routeErr == "" {
		t.Errorf("another template cleared it: %v", err)
	}
	f.locked = true
	if _, err := r.Settle(t.Context(), nil, f.group, nil, []string{"root_message"}); err != nil || f.routeErr == "" {
		t.Errorf("a locked route was cleared: %v", err)
	}
	f.locked = false
	after, err = r.Settle(t.Context(), nil, f.group, nil, []string{"root_message"})
	if err != nil || f.routeErr != "" || len(f.internal) != 2 || !strings.Contains(f.internal[1], `"status":"resolved"`) {
		t.Fatalf("recovery = %v, %q, %v", err, f.routeErr, f.internal)
	}
	after(t.Context())
	if !strings.Contains(log.String(), `"event":"template_recovered","route":"RTAAAAAAAAAAA1","template":"root_message"`) {
		t.Errorf("log %s", log.String())
	}
	// A rendered root_message template ends an error of the line template, which it puts out of use.
	f.routeErr, f.group.Route.ErrorTemplate = "line", "line"
	if _, err := r.Settle(t.Context(), nil, f.group, nil, []string{"root_message"}); err != nil || f.routeErr != "" ||
		len(f.internal) != 3 {
		t.Errorf("stale line error = %v, %q", err, f.routeErr)
	}
	// Saving a new template clears the error and resolves the Internal alert.
	f.routeErr = "line"
	if err := r.TemplateSaved(t.Context(), nil, 3, "RTAAAAAAAAAAA1", []string{"root_message", "line"}); err != nil ||
		f.routeErr != "" || len(f.internal) != 4 {
		t.Errorf("saved = %v, %q, %d", err, f.routeErr, len(f.internal))
	}
	if err := r.TemplateSaved(t.Context(), nil, 3, "RTAAAAAAAAAAA1", []string{"line"}); err != nil ||
		len(f.internal) != 4 {
		t.Errorf("saved without an error = %v", err)
	}
	for _, q := range []string{"SetTemplateError", "ClearTemplateError", "FindBuiltinIntegration"} {
		f.fail = map[string]error{q: errBoom}
		f.routeErr, f.group.Route.ErrorTemplate = "", "root_message"
		_, err1 := r.Settle(t.Context(), nil, f.group, []Failure{fail}, nil)
		f.routeErr = "root_message"
		_, err2 := r.Settle(t.Context(), nil, f.group, nil, []string{"root_message"})
		err3 := r.TemplateSaved(t.Context(), nil, 3, "RTAAAAAAAAAAA1", []string{"root_message"})
		if !errors.Is(errors.Join(err1, err2, err3), errBoom) {
			t.Errorf("%s: %v %v %v", q, err1, err2, err3)
		}
	}
}

// counter is muster_template_errors_total of the Route of the fixtures.
func counter(t *testing.T) uint64 {
	t.Helper()
	return metrics.TemplateErrors.With("RTAAAAAAAAAAA1", "", "root_message").Get()
}

// TestLoadAndReplies reads an Alert Group through the queries and renders its Thread replies in its language (C-12.FR-3,
// C-12.AC-5): new Alerts with the labels the common labels do not already say and "…and K more"; Takeover,
// Replacement, the release of a disabled or deleted Owner and the others.
func TestLoadAndReplies(t *testing.T) {
	f := &fakeQueries{group: source("acknowledged", 3)}
	f.group.Owner = "Alice"
	f.group.Alerts[0].GeneratorURL = "http://prometheus/graph"
	r := withFake(t, f)
	src, err := r.Load(t.Context(), nil, 9)
	if err != nil || len(src.Alerts) != 3 || src.TotalAlerts != 3 || src.Alerts[1].EndsAt == nil ||
		src.CommonLabels["team"] != "web" || src.Route.SnoozeDurations[2] != 86400 || src.Owner != "Alice" {
		t.Fatalf("load %+v %v", src, err)
	}
	if _, err := r.Load(t.Context(), nil, 99); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown group: %v", err)
	}
	reply := func(lang, event string, fps ...string) string {
		t.Helper()
		f.group.Route.Language = lang
		m, err := r.Reply(t.Context(), nil, 9, ReplyInput{Event: event, Seq: 4, Fingerprints: fps, Listed: 2})
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(m.Lines, "\n")
	}
	if got := reply("ru", EventAlertsAdded, "fp1", "fp2", "fp3"); got !=
		"Новые алерты (3):\n• pod: p1\n• pod: p2\n…и ещё 1 — смотрите в Muster" {
		t.Errorf("ru alerts %q", got)
	}
	if got := reply("en", EventAlertsAdded, "fpX"); got != "New alerts (1):\n• fpX" {
		t.Errorf("unknown alert %q", got)
	}
	f.entry = dbgen.GetReplyEntryRow{ReplacedLabel: "pod"}
	for event, want := range map[string]string{
		EventTakeover:           "Alice took over this Alert Group.",
		EventAlertReplaced:      "Alerts were replaced because pod changed.",
		EventUnacknowledged:     "The owner was disabled — this Alert Group has no owner now.",
		EventReopened:           "Reopened: the alerts fire again.",
		EventSnoozeEnded:        "Snooze ended while alerts still fire.",
		EventUrgencyRaised:      "This Alert Group is Urgent now.",
		EventResolved:           "Resolved automatically: all alerts resolved",
		EventAckTimeout:         "Nobody has acknowledged this Alert Group yet.",
		EventUnclaimed:          "⚠ Nobody has taken this",
		EventReminder:           "Reminder: Alice, are you still on it?",
		EventAutoUnacknowledged: "The acknowledgement was released: nobody answered the Reminders.",
		EventNoticesMissed:      "1 notice was missed while Muster was unavailable.",
		"something_else":        "#12 something_else",
	} {
		if got := reply("en", event); got != want {
			t.Errorf("%s = %q", event, got)
		}
	}
	f.entry = dbgen.GetReplyEntryRow{Reason: "owner_deleted", MissedCount: 5}
	if got := reply("ru", EventUnacknowledged); got != "Владелец удалён — у этой группы алертов больше нет владельца." {
		t.Errorf("deleted %q", got)
	}
	if got := reply("ru", EventNoticesMissed); got != "Пока Muster был недоступен, пропущено 5 уведомлений." {
		t.Errorf("missed %q", got)
	}
	if got := reply("ru", EventAlertReplaced); got != "Алерты заменились, потому что изменилась метка." {
		t.Errorf("replaced %q", got)
	}
	for _, q := range []string{"GetRenderGroup", "ListRenderAlerts", "ListAlertsByFingerprint", "GetReplyEntry"} {
		f.fail = map[string]error{q: errBoom}
		_, err1 := r.Load(t.Context(), nil, 9)
		_, err2 := r.Reply(t.Context(), nil, 9, ReplyInput{Event: EventAlertsAdded, Fingerprints: []string{"fp1"}})
		_, err3 := r.Reply(t.Context(), nil, 9, ReplyInput{Event: EventAlertReplaced})
		_, err4 := r.Reply(t.Context(), nil, 9, ReplyInput{Event: EventUnacknowledged})
		_, err5 := r.Reply(t.Context(), nil, 9, ReplyInput{Event: EventNoticesMissed})
		if !errors.Is(errors.Join(err1, err2, err3, err4, err5), errBoom) {
			t.Errorf("%s: no error", q)
		}
	}
	f.fail = map[string]error{"GetReplyEntry": pgx.ErrNoRows}
	if got := reply("en", EventAlertReplaced); got != "Alerts were replaced because a label changed." {
		t.Errorf("no entry %q", got)
	}
}

// TestStormAndNotes is C-12.FR-10 and the notes of S-035 in the language and time zone of the message.
func TestStormAndNotes(t *testing.T) {
	r := newRenderer(t)
	m := r.Storm("RTAAAAAAAAAAA1", "db", "en", 21, 1)
	if m.Lines[0] != "⛈ Storm on db: 21 Alert Groups, 1 urgent — open in Muster" ||
		m.Links[0].URL != "https://muster.example.org/alert-groups?route=RTAAAAAAAAAAA1" {
		t.Errorf("storm %+v", m)
	}
	// A value that holds a placeholder is never filled in turn.
	for range 20 {
		if m := r.Storm("RT", "{urgent}", "en", 2, 1); m.Lines[0] !=
			"⛈ Storm on {urgent}: 2 Alert Groups, 1 urgent — open in Muster" {
			t.Fatalf("placeholder in a value %q", m.Lines[0])
		}
	}
	if m := r.StormOver("RTAAAAAAAAAAA1", "en", 4); m.Lines[0] != "Storm over: 4 Alert Groups still open" {
		t.Errorf("over %+v", m)
	}
	if m := r.Storm("RT", "db", "ru", 2, 5); m.Lines[0] != "⛈ Шторм в маршруте db: 2 группы алертов, 5 срочных — смотрите в Muster" {
		t.Errorf("ru storm %q", m.Lines[0])
	}
	if m := r.StormOver("RT", "ru", 1); m.Lines[0] != "Шторм закончился: открытой осталась 1 группа алертов" {
		t.Errorf("ru over %q", m.Lines[0])
	}
	base := Message{Language: "ru", TimeZone: "Europe/Moscow", Notices: []string{"n"}, Buttons: []Button{{Label: "x"}}}
	late := LateNote(base, t0, t0.Add(time.Hour))
	if late.Notices[1] != "Доставлено с опозданием: группа алертов началась в 17:05 и закрылась в 18:05, пока доставка сюда не работала." {
		t.Errorf("late %q", late.Notices)
	}
	if d := DeletedNote(Message{}, t0); d.Notices[0] != "The previous message was deleted at 14:05." {
		t.Errorf("deleted %q", d.Notices)
	}
	final := FinalEdit(base, "https://m/ag")
	if final.Notices[1] != "Здесь больше не обновляется; текущее состояние — в Muster: https://m/ag" ||
		len(final.Buttons) != 0 || len(base.Notices) != 1 || len(base.Buttons) != 1 {
		t.Errorf("final %+v, base %+v", final, base)
	}
}

// webhook is the body of a Stored Snapshot with Alerts of PodDown, one per pod.
func webhook(pods ...string) []byte {
	var alerts []string
	for _, p := range pods {
		alerts = append(alerts, fmt.Sprintf(`{"status":"firing","labels":{"alertname":"PodDown","pod":%q,`+
			`"cluster":"prod"},"annotations":{"summary":"down"},"startsAt":"2026-10-04T13:00:00Z","fingerprint":"f%s"}`,
			p, p))
	}
	return []byte(`{"receiver":"muster","status":"firing","groupLabels":{"alertname":"PodDown"},` +
		`"alerts":[` + strings.Join(alerts, ",") + `]}`)
}

// TestPreview is previewTemplate (C-12.FR-5, C-12.AC-2): an unregistered function is valid false with
// unknown_function and its position; an empty template previews the built-in one — the whole default Root message,
// shortened to a length limit — and returns its source in the requested language; html lays the output out as
// Telegram HTML; the sample is the Alert Group, a Stored Snapshot, the Route's recent Stored Snapshots or the
// example.
func TestPreview(t *testing.T) {
	f := &fakeQueries{group: source("firing", 25)}
	f.group.CommonAnnotations["details"] = strings.Repeat("d", 4000)
	r := withFake(t, f)
	ctx := t.Context()
	res, err := r.Preview(ctx, PreviewRequest{Kind: "root_message", Template: `{{ env "HOME" }}`})
	if err != nil || res.Valid || len(res.Errors) != 1 || res.Errors[0].Code != "unknown_function" ||
		res.Errors[0].Line != 1 || res.Errors[0].Column != 4 || res.Output != "" || res.Source != `{{ env "HOME" }}` {
		t.Errorf("env = %+v, %v", res, err)
	}
	res, err = r.Preview(ctx, PreviewRequest{Kind: "root_message", AlertGroupID: "AGK7M3QX9P2RTA", LengthLimit: 4096})
	if err != nil || !res.Valid || !res.Truncated || len([]rune(res.Output)) > 4096 || res.Sample != "alert_group" ||
		res.Format != MarkupMarkdown || res.Source != BuiltinSource("root_message", "en") {
		t.Fatalf("group = %+v, %v", res, err)
	}
	mustKeep(t, res.Output, "🔴 [#12 PodDown](", "pod: p1, p2, p3, p4, p5, p6, p7, p8, p9, p10 +15 more",
		"Full list in Muster", "Open in Muster", "[Ack] [Resolve] [Snooze 1 h]", "owner: @\u200bchannel")
	f.group.Status, f.group.ResolvedByKind, f.group.ResolveReason = "resolved", "system", "resolved"
	res, err = r.Preview(ctx, PreviewRequest{Kind: "root_message", AlertGroupID: "AGK7M3QX9P2RTA", Language: "ru",
		Format: MarkupHTML})
	if err != nil || !strings.Contains(res.Output, "\nЗакрыта автоматически: все алерты закрыты") ||
		!strings.Contains(res.Output, `<a href="https://muster.example.org/alert-groups/AGK7M3QX9P2RTA">`) ||
		res.Source != BuiltinSource("root_message", "ru") || res.Format != MarkupHTML {
		t.Errorf("ru html = %+v, %v", res, err)
	}
	// A template is laid out with alert data escaped for the markup.
	f.group.CommonLabels["team"] = "<b>web</b>"
	res, _ = r.Preview(ctx, PreviewRequest{Kind: "root_message", Template: "team {{ .CommonLabels.team }}",
		AlertGroupID: "AGK7M3QX9P2RTA", Format: MarkupHTML})
	if !res.Valid || !strings.Contains(res.Output, "\nteam &lt;b&gt;web&lt;/b&gt;\n") {
		t.Errorf("escaped %+v", res)
	}
	// The Route's recent Stored Snapshots, all of them rendered; the output is that of the most recent.
	f.snapshots = [][]byte{webhook("a", "b"), []byte("not json"), webhook("c")}
	res, err = r.Preview(ctx, PreviewRequest{Kind: "line", Template: "{{ .Labels.pod }} on {{ .Labels.cluster }}",
		RouteID: "RTAAAAAAAAAAA1"})
	if err != nil || !res.Valid || res.Sample != "stored_snapshot" || res.Output != "- a on prod\n- b on prod" {
		t.Errorf("route snapshots = %+v, %v", res, err)
	}
	res, _ = r.Preview(ctx, PreviewRequest{Kind: "line", Template: `{{ if eq .Labels.pod "c" }}{{ fail }}{{ end }}`,
		RouteID: "RTAAAAAAAAAAA1"})
	if res.Valid || res.Errors[0].Code != "unknown_function" {
		t.Errorf("a template failing on an older snapshot %+v", res)
	}
	res, _ = r.Preview(ctx, PreviewRequest{Kind: "line", Template: `{{ if eq .Labels.pod "c" }}{{ index . 1 }}{{ end }}`,
		RouteID: "RTAAAAAAAAAAA1"})
	if res.Valid || res.Errors[0].Code != "template_syntax" || res.Sample != "stored_snapshot" {
		t.Errorf("a template failing on an older snapshot %+v", res)
	}
	res, err = r.Preview(ctx, PreviewRequest{Kind: "ack_timeout_notice", Template: "", SnapshotID: "SS000000000000"})
	if err != nil || !res.Valid || res.Sample != "stored_snapshot" || res.Output != "Nobody has acknowledged #1 PodDown yet." {
		t.Errorf("snapshot = %+v, %v", res, err)
	}
	// Without Stored Snapshots, the example.
	f.snapshots = nil
	res, err = r.Preview(ctx, PreviewRequest{Kind: "line", RouteID: "RTAAAAAAAAAAA1", Language: "ru"})
	if err != nil || !res.Valid || res.Sample != "example" || res.Output != "- pod: checkout-1\n- pod: checkout-2" ||
		res.Source != BuiltinSource("line", "ru") {
		t.Errorf("example = %+v, %v", res, err)
	}
	res, err = r.Preview(ctx, PreviewRequest{Kind: "root_message", Template: "{{ .AlertGroup.Title }}"})
	if err != nil || !res.Valid || res.Sample != "example" || !strings.Contains(res.Output, "\nHighErrorRate\n") {
		t.Errorf("no route = %+v, %v", res, err)
	}
	f.snapshots = [][]byte{[]byte(`{"status":"firing","alerts":[]}`)}
	if res, err := r.Preview(ctx, PreviewRequest{Kind: "line", SnapshotID: "SS000000000000"}); err != nil ||
		!res.Valid || res.Output != "" {
		t.Errorf("a snapshot without alerts = %+v, %v", res, err)
	}
	f.snapshots = [][]byte{[]byte("nope")}
	if res, err := r.Preview(ctx, PreviewRequest{Kind: "line", SnapshotID: "SS000000000000"}); err != nil || res.Valid ||
		res.Errors[0].Code != "invalid_format" {
		t.Errorf("not a webhook = %+v, %v", res, err)
	}
	for _, tc := range []struct {
		req  PreviewRequest
		want error
	}{
		{PreviewRequest{Kind: "link_rule"}, ErrUnsupportedKind},
		{PreviewRequest{Kind: "line", RouteID: "RTZZZZZZZZZZZZ"}, ErrRouteNotFound},
		{PreviewRequest{Kind: "line", RouteID: "bad"}, ErrRouteNotFound},
		{PreviewRequest{Kind: "line", AlertGroupID: "AGZZZZZZZZZZZZ"}, ErrNotFound},
		{PreviewRequest{Kind: "line", AlertGroupID: "bad"}, ErrNotFound},
		{PreviewRequest{Kind: "line", SnapshotID: "SSZZZZZZZZZZZZ"}, ErrSnapshotNotFound},
		{PreviewRequest{Kind: "line", SnapshotID: "bad"}, ErrSnapshotNotFound},
	} {
		if _, err := r.Preview(ctx, tc.req); !errors.Is(err, tc.want) {
			t.Errorf("%+v = %v", tc.req, err)
		}
	}
	for q, req := range map[string]PreviewRequest{
		"GetRenderSettings":    {Kind: "line"},
		"GetPreviewRoute":      {Kind: "line", RouteID: "RTAAAAAAAAAAA1"},
		"GetGroupIDByPublicID": {Kind: "line", AlertGroupID: "AGK7M3QX9P2RTA"},
		"GetSnapshotRetention": {Kind: "line", RouteID: "RTAAAAAAAAAAA1"},
		"ListRouteSnapshots":   {Kind: "line", RouteID: "RTAAAAAAAAAAA1"},
		"GetSnapshotBody":      {Kind: "line", SnapshotID: "SS000000000000"},
	} {
		f.fail = map[string]error{q: errBoom}
		if _, err := r.Preview(ctx, req); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", q, err)
		}
		f.fail = map[string]error{}
	}
	f.fail = map[string]error{"GetSnapshotRetention": errBoom}
	if _, err := r.Preview(ctx, PreviewRequest{Kind: "line", SnapshotID: "SS000000000000"}); !errors.Is(err, errBoom) {
		t.Errorf("retention: %v", err)
	}
}

// TestCheckTemplate is the dry run on save (C-12.FR-5): a template is parsed, then rendered against the Route's most
// recent Stored Snapshots, or the example for a new Route or one without any; the first failure is returned with its
// code and position.
func TestCheckTemplate(t *testing.T) {
	f := &fakeQueries{group: source("firing", 1)}
	r := withFake(t, f)
	ctx := t.Context()
	var te *templates.Error
	if err := r.CheckTemplate(ctx, 0, "root_message", `{{ env "HOME" }}`, "en"); !errors.As(err, &te) ||
		te.Code != "unknown_function" || te.Line != 1 || te.Column != 4 {
		t.Errorf("env = %v", err)
	}
	if err := r.CheckTemplate(ctx, 0, "line", "{{ .Labels.pod }} on {{ .Labels.cluster }}", "en"); err != nil {
		t.Errorf("valid on the example = %v", err)
	}
	f.snapshots = [][]byte{webhook("a"), webhook("b")}
	if err := r.CheckTemplate(ctx, 3, "line", `{{ if eq .Labels.pod "b" }}{{ index . 1 }}{{ end }}`, "en"); !errors.As(err,
		&te) || te.Code != "template_syntax" {
		t.Errorf("fails on an older snapshot = %v", err)
	}
	if err := r.CheckTemplate(ctx, 3, "ack_timeout_notice", "#{{ .AlertGroup.Number }}", "ru"); err != nil {
		t.Errorf("valid on the snapshots = %v", err)
	}
	for _, q := range []string{"GetRenderSettings", "ListRouteSnapshots"} {
		f.fail = map[string]error{q: errBoom}
		if err := r.CheckTemplate(ctx, 3, "line", "x", "en"); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", q, err)
		}
	}
	// The cache of parsed templates is emptied when it is full.
	f.fail = map[string]error{}
	for i := range cacheSize + 1 {
		if _, err := r.compile("line", fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.cache) >= cacheSize {
		t.Errorf("cache of %d", len(r.cache))
	}
}

// fakeLinks give every Alert Group a Link rule's link, a runbook and a source, and render a URL template by its
// source; fail makes them fail.
type fakeLinks struct {
	inputs []links.Input
	fail   error
}

func (f *fakeLinks) Evaluate(_ context.Context, _ links.DBTX, in links.Input) ([]links.Link, error) {
	f.inputs = append(f.inputs, in)
	return []links.Link{{Kind: links.KindRule, Name: "Logs: @here", URL: "https://logs.example.org/x(1)"},
		{Kind: links.KindRunbook, Name: links.NameRunbook, URL: "https://runbooks.example.org/a"},
		{Kind: links.KindSource, Name: links.NameSource, URL: "https://prometheus.example.org/g"}}, f.fail
}

func (f *fakeLinks) RenderURL(_ context.Context, _ links.DBTX, source string, in links.Input) (string, error) {
	f.inputs = append(f.inputs, in)
	if strings.Contains(source, "fail") {
		return "", &templates.Error{Code: templates.CodeSyntax, Line: 1, Column: 3, Detail: "failed"}
	}
	return source + "|" + in.Data.CommonLabels["cluster"], f.fail
}

// fakeNames name the footer's user by a username in one identity space.
type fakeNames struct{ fail error }

func (f fakeNames) FooterNames(context.Context, mentions.DBTX, int64) (map[string]string, error) {
	return map[string]string{"mattermost:3": "alice.mm", "telegram": "Alice"}, f.fail
}

// TestLinksMentionsAndFooters (C-12.FR-1 item 9, C-12.FR-7, C-12.FR-8, C-12.FR-12): the links follow "Open in
// Muster" on one line, a rule's by its neutral name and the others in the message's language; a literal @all of a
// template is neutralized while `mention` leaves a token the layout shows as @all; the footer names the user by the
// username of each identity space, the display name elsewhere; a link_rule preview renders the URL template.
func TestLinksMentionsAndFooters(t *testing.T) {
	ctx := t.Context()
	f := &fakeQueries{group: source("acknowledged", 2)}
	f.group.Owner = "Alice Smith"
	f.group.Route.RootMessage = ptr(`{{ mention "all" }} and @all {{ mention "group" "db-oncall" }} {{ mention "owner" }}`)
	r := withFake(t, f)
	fl := &fakeLinks{}
	r.links, r.names = fl, fakeNames{}
	src, err := r.Load(ctx, nil, 9)
	if err != nil {
		t.Fatal(err)
	}
	if len(fl.inputs) != 1 || fl.inputs[0].Group != "AGK7M3QX9P2RTA" || fl.inputs[0].Route != "RTAAAAAAAAAAA1" ||
		fl.inputs[0].Data.CommonLabels["owner"] != "@channel" {
		t.Fatalf("link input %+v", fl.inputs)
	}
	rd := r.Root(src, MarkupMarkdown)
	m := rd.Message
	if len(m.Links) != 4 || m.Links[1].Text != "Logs: @\u200bhere" || m.Links[2].Text != "Runbook" ||
		m.Links[3].Text != "Source" {
		t.Fatalf("links %+v", m.Links)
	}
	if m.Footer != "Acknowledged by Alice Smith" || m.FooterIn("mattermost:3") != "Acknowledged by alice.mm" ||
		m.FooterIn("telegram") != "Acknowledged by Alice" || m.FooterIn("mattermost:4") != m.Footer ||
		len(m.Footers) != 2 {
		t.Errorf("footers %q %v", m.Footer, m.Footers)
	}
	if tokens := templates.Tokens(m.Body.Text); len(tokens) != 3 || tokens[0].Name != "all" ||
		tokens[1].Group != "db-oncall" || tokens[2].Name != "owner" {
		t.Errorf("tokens %v in %q", tokens, m.Body.Text)
	}
	out := Layout(m, MarkupMarkdown)
	mustKeep(t, out, "@all and @\u200ball @db-oncall @owner",
		"[Open in Muster](https://muster.example.org/alert-groups/AGK7M3QX9P2RTA) · [Logs: @\u200bhere](https://logs.example.org/x%281%29) · [Runbook](https://runbooks.example.org/a) · [Source](https://prometheus.example.org/g)")
	if strings.ContainsAny(out, "") || strings.ContainsAny(m.Text(), "") {
		t.Error("token characters reach the layout")
	}
	mustKeep(t, Layout(m, MarkupHTML), `<a href="https://logs.example.org/x(1)">Logs: @`+"\u200b"+`here</a>`)
	ru := *src
	ru.Route.Language = "ru"
	mustKeep(t, Layout(r.Root(&ru, MarkupPlain).Message, MarkupPlain), "Ранбук https://runbooks.example.org/a",
		"Источник https://prometheus.example.org/g")
	// Alert data cannot forge a token: its token characters are dropped.
	f.group.CommonLabels["team"] = "all"
	f.group.Route.RootMessage = ptr("{{ .CommonLabels.team }}")
	src, _ = r.Load(ctx, nil, 9)
	if body := r.Root(src, MarkupMarkdown).Message.Body.Text; body != "all" {
		t.Errorf("forged token %q", body)
	}
	// Nor can a label name or a fingerprint, in the built-in templates or in the default layout.
	forged := "team\ue000channel\ue002"
	f.group.CommonLabels = map[string]string{forged: "x", "cluster": "prod"}
	f.group.KeyLabels = append(f.group.KeyLabels, forged)
	f.group.KeyValues[forged] = "y"
	f.group.Alerts[0].Fingerprint = "\ue000here\ue002"
	f.group.Alerts[0].Labels[forged] = "z"
	f.group.Route.RootMessage = ptr(BuiltinSource(TemplateRootMessage, "en") + "{{ range .Alerts }}{{ .Fingerprint }}{{ end }}")
	src, _ = r.Load(ctx, nil, 9)
	for _, m := range []Message{r.Root(src, MarkupMarkdown).Message, func() Message {
		def := *src
		def.Route.RootMessage = nil
		return r.Root(&def, MarkupMarkdown).Message
	}()} {
		out := Layout(m, MarkupMarkdown)
		body := ""
		if m.Body != nil {
			body = m.Body.Text
		}
		if tk := templates.Tokens(body); len(tk) != 0 || strings.Contains(out, "@channel") ||
			strings.Contains(out, "@here") || strings.ContainsAny(out, "\ue000\ue001\ue002") {
			t.Errorf("forged tokens %v in %q", tk, out)
		}
	}
	// A line template is neutralized the same way.
	f.group.Route.RootMessage, f.group.Route.Line = nil, ptr("@here {{ .Labels.pod }}")
	src, _ = r.Load(ctx, nil, 9)
	if line := r.Root(src, MarkupMarkdown).Message.Alerts.Lines[0].Text; line != "@\u200bhere p1" {
		t.Errorf("line %q", line)
	}
	// Failing links or names fail the load.
	fl.fail = errBoom
	if _, err := r.Load(ctx, nil, 9); !errors.Is(err, errBoom) {
		t.Errorf("failing links: %v", err)
	}
	fl.fail, r.names = nil, fakeNames{fail: errBoom}
	if _, err := r.Load(ctx, nil, 9); !errors.Is(err, errBoom) {
		t.Errorf("failing names: %v", err)
	}
	r.names = nil
	// The preview of a URL template, against the example and against a Stored Snapshot.
	res, err := r.Preview(ctx, PreviewRequest{Kind: TemplateLinkRule, Template: "https://x/{{ .Labels.cluster }}"})
	if err != nil || !res.Valid || res.Output != "https://x/{{ .Labels.cluster }}|prod" || res.Format != "" ||
		res.Sample != SampleExample {
		t.Errorf("link preview %+v %v", res, err)
	}
	f.snapshots = [][]byte{webhook("a")}
	res, _ = r.Preview(ctx, PreviewRequest{Kind: TemplateLinkRule, Template: "fail", SnapshotID: "SS000000000000"})
	if res.Valid || len(res.Errors) != 1 || res.Errors[0].Line != 1 || res.Sample != SampleStoredSnapshot {
		t.Errorf("failing link preview %+v", res)
	}
	// A root_message preview of a sample shows the sample's links.
	fl.inputs = nil
	res, err = r.Preview(ctx, PreviewRequest{Kind: TemplateRootMessage, SnapshotID: "SS000000000000"})
	if err != nil || !strings.Contains(res.Output, "[Runbook](https://runbooks.example.org/a)") || len(fl.inputs) != 1 {
		t.Errorf("root preview %+v %v", res, err)
	}
	fl.fail = errBoom
	if _, err := r.Preview(ctx, PreviewRequest{Kind: TemplateRootMessage, SnapshotID: "SS000000000000"}); err == nil {
		t.Error("failing links fail the preview")
	}
	r.links = nil
	if _, err := r.Preview(ctx, PreviewRequest{Kind: TemplateLinkRule, Template: "x"}); !errors.Is(err,
		ErrUnsupportedKind) {
		t.Errorf("no links: %v", err)
	}
	if Neutralize(Neutralize("a@b @@")) != "a@\u200bb @\u200b@\u200b" {
		t.Error("neutralizing twice changes nothing")
	}
}

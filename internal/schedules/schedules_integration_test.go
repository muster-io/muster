// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package schedules_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/schedules"
	"github.com/muster-io/muster/internal/teams"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

// t0 is Wednesday 28 October 2026, noon UTC.
var t0 = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

var (
	admin     = schedules.Requester{Actor: audit.System, Transport: audit.TransportSystem, Admin: true}
	teamAdmin = teams.Requester{Actor: audit.System, Transport: audit.TransportSystem}
)

type env struct {
	d      *db.DB
	orgID  int64
	clock  *clock.Manual
	svc    *schedules.Service
	teams  *teams.Service
	team   teams.Team
	people map[string]string
}

// setup has the Team payments with petr, sergey and olga, and the Viewer vic in it.
func setup(t *testing.T, s dbtest.Server) *env {
	t.Helper()
	ctx := t.Context()
	conn := config.Database{URL: logging.Secret(s.NewDatabase(t)), SSLMode: "disable"}
	d, err := db.Open(ctx, conn, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	logger := logging.New(&bytes.Buffer{}, logging.LevelInfo)
	if err := d.Migrate(ctx, logger); err != nil {
		t.Fatal(err)
	}
	if err := organization.Ensure(ctx, organization.NewStore(d.Pool), logger, t0); err != nil {
		t.Fatal(err)
	}
	for _, month := range []string{"2026-10", "2026-11", "2026-12"} {
		next := map[string]string{"2026-10": "2026-11", "2026-11": "2026-12", "2026-12": "2027-01"}[month]
		if _, err := d.Pool.Exec(ctx, `CREATE TABLE audit_log_p`+strings.ReplaceAll(month, "-", "")+
			` PARTITION OF audit_log FOR VALUES FROM ('`+month+`-01 00:00:00+00') TO ('`+next+
			`-01 00:00:00+00')`); err != nil {
			t.Fatal(err)
		}
	}
	org, err := organization.NewStore(d.Pool).GetOrganization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := clock.NewManual(t0)
	w := audit.NewWriter(logger, c)
	e := &env{d: d, orgID: org.ID, clock: c, people: map[string]string{},
		svc:   schedules.New(schedules.Config{OrgID: org.ID, Store: schedules.NewStore(d.Pool), Audit: w, Business: c}),
		teams: teams.New(teams.Config{OrgID: org.ID, Store: teams.NewStore(d.Pool), Audit: w, Business: c})}
	if e.team, err = e.teams.Create(ctx, teamAdmin, teams.Input{Name: "Payments", Slug: "payments"}); err != nil {
		t.Fatal(err)
	}
	for i, p := range []struct{ login, role string }{{"petr", "responder"}, {"sergey", "responder"},
		{"olga", "responder"}, {"vic", "viewer"}} {
		id := "SR00000000000" + string(rune('A'+i))
		if _, err := d.Pool.Exec(ctx, `INSERT INTO users (org_id, public_id, login, name, role, source, status,
			created_at, updated_at) VALUES ($1, $2, $3, $3, $4, 'local', 'active', $5, $5)`, org.ID, id, p.login,
			p.role, t0); err != nil {
			t.Fatal(err)
		}
		if _, _, err := e.teams.AddUser(ctx, teamAdmin, e.team.PublicID, id); err != nil {
			t.Fatal(err)
		}
		e.people[p.login] = id
	}
	return e
}

func (e *env) layers() []schedules.LayerInput {
	start := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	return []schedules.LayerInput{
		{Name: "Around the clock", UserIDs: []string{e.people["petr"], e.people["sergey"], e.people["olga"]},
			ShiftLength: schedules.ShiftWeek, HandoverTime: "10:00", HandoverDay: "monday", StartDate: start},
		{Name: "Working hours 11-19", UserIDs: []string{e.people["olga"], e.people["petr"]},
			ShiftLength: schedules.ShiftWeek, HandoverTime: "11:00", HandoverDay: "monday", StartDate: start,
			Limits: &schedules.Limits{Days: []string{"monday", "tuesday", "wednesday", "thursday", "friday"},
				Start: "11:00", End: "19:00"}},
	}
}

func ptr[T any](v T) *T { return &v }

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.d.Pool.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// berlin is the instant of a wall-clock time in Berlin.
func berlin(t *testing.T, s string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	at, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

// TestIntegrationSchedules is C-27.FR-1, FR-2, FR-12, FR-13 and C-27.AC-1, AC-8 on PostgreSQL: a Schedule with its
// Rotation layers in the Organization's time zone when none is given, main as the first of its Team; a second one made
// main in one transaction; the On-call of scenario 1 and of the new main Schedule; name_taken; a Viewer refused in a
// Rotation layer; the Audit log; muster_schedule_info refreshed from the database; deleting the main Schedule leaves
// the Team without one; Referrers names nothing until the Escalation steps.
func TestIntegrationSchedules(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		main, err := e.svc.Create(ctx, admin, schedules.Input{TeamID: e.team.PublicID, Name: "Main",
			TimeZone: ptr("Europe/Berlin"), Layers: e.layers()})
		if err != nil {
			t.Fatal(err)
		}
		if !main.Main || len(main.Layers) != 2 || main.Layers[1].Limits == nil || main.Layers[0].Users[2].Login != "olga" {
			t.Fatalf("main = %+v", main)
		}
		who := func(publicID, at string) string {
			t.Helper()
			seg, err := e.svc.OnCallAt(ctx, publicID, berlin(t, at))
			if err != nil {
				t.Fatal(err)
			}
			if seg.User == nil {
				return "gap"
			}
			return seg.User.Login
		}
		for at, want := range map[string]string{"2026-11-04 15:00": "olga", "2026-11-04 21:00": "petr",
			"2026-11-07 15:00": "petr"} {
			if got := who(main.PublicID, at); got != want {
				t.Errorf("On-call at %s = %s, want %s", at, got, want)
			}
		}
		if _, err := e.svc.Create(ctx, admin, schedules.Input{TeamID: e.team.PublicID, Name: "MAIN"}); !errors.Is(err,
			schedules.ErrNameTaken) {
			t.Errorf("a name taken = %v", err)
		}
		bad := e.layers()[:1]
		bad[0].UserIDs = []string{e.people["vic"]}
		if _, err := e.svc.Create(ctx, admin, schedules.Input{TeamID: e.team.PublicID, Name: "Bad",
			Layers: bad}); err == nil || !strings.Contains(err.Error(), "/rotation_layers/0/user_ids/0") {
			t.Errorf("a viewer = %v", err)
		}
		second, err := e.svc.Create(ctx, admin, schedules.Input{TeamID: e.team.PublicID, Name: "Second line",
			Layers: []schedules.LayerInput{{Name: "Days", UserIDs: []string{e.people["sergey"]},
				ShiftLength: schedules.ShiftDay, HandoverTime: "09:00",
				StartDate: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)}}})
		if err != nil || second.Main || second.TimeZone != "UTC" {
			t.Fatalf("second = %+v, %v", second, err)
		}
		in := schedules.Input{TeamID: e.team.PublicID, Name: second.Name, Main: ptr(true),
			Layers: []schedules.LayerInput{{ID: second.Layers[0].PublicID, Name: "Days",
				UserIDs: []string{e.people["sergey"]}, ShiftLength: schedules.ShiftDay, HandoverTime: "09:00",
				StartDate: second.Layers[0].StartDate}}}
		promoted, err := e.svc.Update(ctx, admin, second.PublicID, ptr(int64(1)), in)
		if err != nil || !promoted.Main || promoted.Layers[0].PublicID != second.Layers[0].PublicID {
			t.Fatalf("promoted = %+v, %v", promoted, err)
		}
		if n := e.count(t, `SELECT count(*) FROM schedules WHERE team_id = $1 AND main`, e.team.ID); n != 1 {
			t.Errorf("%d main schedules", n)
		}
		if got := who(promoted.PublicID, "2026-11-04 15:00"); got != "sergey" {
			t.Errorf("the On-call of the new main schedule = %s", got)
		}
		if n := e.count(t, `SELECT count(*) FROM audit_log WHERE action = 'schedule.updated'
			AND resource_public_id = $1 AND diff::text LIKE '%"/main"%'`, main.PublicID); n != 1 {
			t.Errorf("%d entries of the main schedule unmarked", n)
		}
		if refs, err := e.svc.Referrers(ctx, nil, main); err != nil || len(refs) != 0 {
			t.Errorf("referrers = %v, %v", refs, err)
		}

		if err := e.svc.RefreshInfo(ctx); err != nil {
			t.Fatal(err)
		}
		for _, sc := range []schedules.Schedule{main, promoted} {
			if got := infoLines(t, sc.PublicID); got != 1 {
				t.Errorf("%d muster_schedule_info series of %s, want 1", got, sc.Name)
			}
		}
		if err := e.svc.Delete(ctx, admin, promoted.PublicID, nil); err != nil {
			t.Fatal(err)
		}
		if got := infoLines(t, promoted.PublicID); got != 0 {
			t.Errorf("%d muster_schedule_info series of the deleted schedule", got)
		}
		if n := e.count(t, `SELECT count(*) FROM schedules WHERE team_id = $1 AND main`, e.team.ID); n != 0 {
			t.Errorf("after deleting the main schedule %d are main", n)
		}
		if n := e.count(t, `SELECT count(*) FROM rotation_layers WHERE schedule_id = $1`, promoted.ID); n != 0 {
			t.Errorf("%d layers of the deleted schedule", n)
		}
		for _, action := range []string{"schedule.created", "schedule.updated", "schedule.deleted"} {
			if n := e.count(t, `SELECT count(*) FROM audit_log WHERE action = $1`, action); n == 0 {
				t.Errorf("no %s", action)
			}
		}
	})
}

// infoLines counts the muster_schedule_info series of the Schedule id; the registry is the process's, which other
// tests of the package fill too.
func infoLines(t *testing.T, id string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	metrics.Handler(nil).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	return strings.Count(rec.Body.String(), "\nmuster_schedule_info{schedule=\""+id+"\",")
}

// TestIntegrationOverridesAndTeams is C-27.FR-3, FR-6, C-27.AC-2, AC-3 and C-25.FR-10 on PostgreSQL: an Override by a
// person in the Team, with the person it replaces, makes petr On-call over both layers and is recorded; one by a
// Responder outside the Team is refused; once it has ended it cannot be deleted; a person taken out of the Team is
// skipped with a warning; deleting the Team is refused with team_in_use naming its Schedules until they are deleted.
func TestIntegrationOverridesAndTeams(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		sc, err := e.svc.Create(ctx, admin, schedules.Input{TeamID: e.team.PublicID, Name: "Main",
			TimeZone: ptr("Europe/Berlin"), Layers: e.layers()})
		if err != nil {
			t.Fatal(err)
		}
		var olgaID int64
		if err := e.d.Pool.QueryRow(ctx, `SELECT id FROM users WHERE login = 'olga'`).Scan(&olgaID); err != nil {
			t.Fatal(err)
		}
		asOlga := schedules.Requester{Actor: audit.User(olgaID, e.people["olga"]), Transport: audit.TransportAPI}
		o, err := e.svc.CreateOverride(ctx, asOlga, sc.PublicID, schedules.OverrideInput{UserID: e.people["petr"],
			ReplacesUserID: e.people["olga"], Start: berlin(t, "2026-11-18 11:00"), End: berlin(t, "2026-11-18 21:00")})
		if err != nil || o.Replaces == nil || o.CreatedBy.Login != "olga" {
			t.Fatalf("override = %+v, %v", o, err)
		}
		from, to := berlin(t, "2026-11-18 00:00"), berlin(t, "2026-11-19 00:00")
		res, err := e.svc.OnCall(ctx, sc.PublicID, &from, &to)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, seg := range res.Segments {
			got = append(got, seg.Start.In(from.Location()).Format("15:04")+" "+seg.User.Login)
		}
		if strings.Join(got, ",") != "00:00 olga,11:00 petr,21:00 olga" || len(res.Overrides) != 1 {
			t.Errorf("segments %v", got)
		}
		if n := e.count(t, `SELECT count(*) FROM audit_log WHERE action = 'schedule_override.created'
			AND actor_user_id = $1`, olgaID); n != 1 {
			t.Errorf("%d override entries", n)
		}
		outsider := schedules.Requester{Actor: audit.User(9999, "SR0000000000ZZ"), Transport: audit.TransportAPI}
		if _, err := e.svc.CreateOverride(ctx, outsider, sc.PublicID, schedules.OverrideInput{UserID: e.people["petr"],
			Start: from, End: to}); !errors.Is(err, schedules.ErrNotInTeam) {
			t.Errorf("an outsider = %v", err)
		}
		if err := e.teams.RemoveUser(ctx, teamAdmin, e.team.PublicID, e.people["petr"]); err != nil {
			t.Fatal(err)
		}
		if res, err = e.svc.OnCall(ctx, sc.PublicID, &from, &to); err != nil {
			t.Fatal(err)
		}
		if len(res.Segments) != 3 || res.Segments[1].User.Login != "olga" || len(res.Warnings) != 1 ||
			res.Warnings[0].Kind != schedules.ReasonNotInTeam {
			t.Errorf("petr out of the team: %+v %+v", res.Segments, res.Warnings)
		}
		e.clock.Set(berlin(t, "2026-11-18 22:00"))
		if err := e.svc.DeleteOverride(ctx, admin, sc.PublicID, o.PublicID, nil); !errors.Is(err,
			schedules.ErrOverrideEnded) {
			t.Errorf("an ended override deleted = %v", err)
		}

		err = e.teams.Delete(ctx, teamAdmin, e.team.PublicID, nil)
		use, ok := errors.AsType[*teams.InUseError](err)
		if !ok || len(use.Holders) != 1 || use.Holders[0].Name != "Main" || use.Holders[0].Type != "schedule" {
			t.Fatalf("deleting the team = %v", err)
		}
		if err := e.svc.Delete(ctx, admin, sc.PublicID, nil); err != nil {
			t.Fatal(err)
		}
		if err := e.teams.Delete(ctx, teamAdmin, e.team.PublicID, nil); err != nil {
			t.Errorf("the team without schedules = %v", err)
		}
	})
}

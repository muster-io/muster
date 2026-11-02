// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/fakes/fakemattermost"
)

// evaluationEntry is a Timeline entry with what the evaluation on save records of it.
type evaluationEntry struct {
	Kind     string `json:"kind"`
	Event    string `json:"event"`
	Loudness string `json:"loudness"`
	Actor    struct {
		Transport string `json:"transport"`
	} `json:"actor"`
	Team *struct {
		Slug string `json:"slug"`
	} `json:"team"`
	TeamRule *string `json:"team_rule"`
}

// evaluated are the entries of the Timeline of the Alert Group g that are not delivery events, oldest first, each as
// event/loudness/transport and, for team_joined, /slug/rule.
func (e *mm) evaluated(g string) []string {
	e.t.Helper()
	var page struct {
		Items []evaluationEntry `json:"items"`
	}
	decode(e.t, e.api.do(http.MethodGet, "/api/v1/alert-groups/"+g+"/timeline?limit=200&order=asc", ""), &page)
	out := []string{}
	for _, x := range page.Items {
		if x.Kind == "delivery" || x.Event == "" {
			continue
		}
		s := x.Event + "/" + x.Loudness + "/" + x.Actor.Transport
		if x.Team != nil && x.TeamRule != nil {
			s += "/" + x.Team.Slug + "/" + *x.TeamRule
		}
		out = append(out, s)
	}
	return out
}

// groupTeams are the Teams of the Alert Group g as slug/rule.
func (e *mm) groupTeams(g string) []string {
	e.t.Helper()
	var v struct {
		Teams []teamOf `json:"teams"`
	}
	decode(e.t, e.api.do(http.MethodGet, "/api/v1/alert-groups/"+g, ""), &v)
	out := []string{}
	for _, t := range v.Teams {
		out = append(out, t.Team.Slug+"/"+t.Rule)
	}
	return out
}

// labelValues are the values of the Team label without a Team, sorted, each as value/valid_slug, and the label.
func (e *mm) labelValues() (string, int, []string) {
	e.t.Helper()
	var v struct {
		Label      string `json:"label"`
		WindowDays int    `json:"window_days"`
		Items      []struct {
			Value     string `json:"value"`
			ValidSlug bool   `json:"valid_slug"`
		} `json:"items"`
	}
	decode(e.t, e.api.do(http.MethodGet, "/api/v1/team-label-values", ""), &v)
	out := []string{}
	for _, i := range v.Items {
		s := i.Value + "/invalid"
		if i.ValidSlug {
			s = i.Value + "/valid"
		}
		out = append(out, s)
	}
	slices.Sort(out)
	return v.Label, v.WindowDays, out
}

// evaluationsDone counts the lines open_group_update_done of the kind team_evaluation that the replicas logged.
func evaluationsDone(h *Harness) int {
	n := 0
	for _, r := range h.Replicas {
		n += strings.Count(r.allOutput(), `"event":"open_group_update_done","kind":"team_evaluation"`)
	}
	return n
}

// TestTeamEvaluation is S-163 against `muster dev`, as its Verification: the values of the Team label without a Team
// (C-25.AC-1, C-25.FR-7); creating the Team "checkout" brings the open Alert Group of team="checkout" — whose Alert
// routing gave no Team — to it with one Quiet team_joined by the system, re-renders it without a new message or a
// notification and records no other event, and the value leaves the list (C-25.AC-13, C-25.FR-13); a Team matcher
// cluster="x" brings the second Alert Group, and a matcher that no longer picks it leaves the Team in it and on its
// Alert; the Team label service_team brings the Alert Group whose service_team names the Team, and changing it back
// adds nothing; a new slug that no longer picks the Alert leaves the Team where it is; a save of the name alone
// requests no evaluation, only the re-render of its name. Every save that writes a row runs it once.
func TestTeamEvaluation(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newMM(t, h, r)
	api := e.api
	conn := e.connection(50)
	dest := e.destination("alerts", conn, fakemattermost.ChannelAlerts, nil, 50)
	var profiles struct {
		Items []struct {
			ID     string         `json:"id"`
			Policy map[string]any `json:"policy"`
		} `json:"items"`
	}
	decode(t, api.do(http.MethodGet, "/api/v1/route-profiles", ""), &profiles)
	var policy map[string]any
	for _, p := range profiles.Items {
		if p.ID == "paging" {
			policy = p.Policy
		}
	}
	route, _ := json.Marshal(map[string]any{"name": "latency", "urgent": false,
		"group_key":       []string{"alertname", "cluster"},
		"matchers":        []map[string]string{{"label": "alertname", "op": "=", "value": "Latency"}},
		"destination_ids": []string{dest}, "policy": policy})
	api.json(http.MethodPost, "/api/v1/routes", string(route), http.StatusCreated)

	e.group("u1", "Latency")
	e.alert("u1", "a", `{"team":"checkout","severity":"warning","cluster":"c1"}`, "")
	e.alert("u1", "b", `{"team":"Payments","severity":"warning","cluster":"x"}`, "")
	e.alert("u1", "s", `{"service_team":"checkout","severity":"warning","cluster":"s1"}`, "")
	e.notify("u1", "first notification")
	g, gb, gs := e.groupOf("c1"), e.groupOf("x"), e.groupOf("s1")

	// C-25.AC-1: the values without a Team.
	if label, days, got := e.labelValues(); label != "team" || days != 14 ||
		!slices.Equal(got, []string{"Payments/invalid", "checkout/valid"}) {
		t.Errorf("values %s %d %v", label, days, got)
	}

	// C-25.FR-13, C-25.AC-13: creating "checkout" brings its open Alert Group, Quietly, as the system.
	for _, id := range []string{g, gb, gs} {
		e.root(e.number(id))
	}
	if got := e.groupTeams(g); len(got) != 0 {
		t.Fatalf("teams before the save %v", got)
	}
	before := e.evaluated(g)
	posts, notes := len(e.posts()), len(e.notifications())
	root := e.root(e.number(g))
	tc := api.json(http.MethodPost, "/api/v1/teams", `{"name":"checkout","slug":"checkout","own_working_hours":false}`,
		http.StatusCreated)["id"].(string)
	eventually(t, "the team to join its alert group", func() bool {
		return slices.Equal(e.groupTeams(g), []string{"checkout/label"})
	})
	eventually(t, "the row to be done", func() bool {
		return h.count(t, `SELECT count(*) FROM open_group_updates`) == 0 && evaluationsDone(h) == 1
	})
	if got := e.evaluated(g); !slices.Equal(got, append(slices.Clone(before), "team_joined/quiet/system/checkout/label")) {
		t.Errorf("timeline %v, before %v", got, before)
	}
	if n := h.count(t, `SELECT count(*) FROM alert_teams x JOIN teams t ON t.id = x.team_id
		WHERE t.public_id = '`+tc+`' AND x.rule = 'label'`); n != 1 {
		t.Errorf("%d alerts of checkout", n)
	}
	if p, n := len(e.posts()), len(e.notifications()); p != posts || n != notes {
		t.Errorf("the evaluation posted %d messages and %d notifications", p-posts, n-notes)
	}
	// The re-render of team_joined is Quiet: the Root message stays the same live post, not posted again or deleted.
	// It shows no Teams before the field "Teams" of C-29.FR-1 (S-125), so its Desired state, and the post, are the same.
	if live := e.root(e.number(g)); live.ID != root.ID || e.post(root.ID).DeleteAt != 0 {
		t.Errorf("the root message %s is now %s", root.ID, live.ID)
	}
	if _, _, got := e.labelValues(); !slices.Equal(got, []string{"Payments/invalid"}) {
		t.Errorf("values after the team %v", got)
	}

	// The matcher cluster="x" brings the second Alert Group; one that no longer picks it leaves the Team there.
	put := func(body string) {
		t.Helper()
		etag := api.do(http.MethodGet, "/api/v1/teams/"+tc, "").header.Get("ETag")
		if a := api.do(http.MethodPut, "/api/v1/teams/"+tc, body, "If-Match", etag); a.status != http.StatusOK {
			t.Fatalf("update the team = %d %s", a.status, a.body)
		}
	}
	put(`{"name":"checkout","slug":"checkout","own_working_hours":false,` +
		`"matchers":[{"label":"cluster","op":"=","value":"x"}]}`)
	eventually(t, "the matcher to bring the second alert group", func() bool {
		return slices.Equal(e.groupTeams(gb), []string{"checkout/matcher"}) && evaluationsDone(h) == 2
	})
	put(`{"name":"checkout","slug":"checkout","own_working_hours":false,` +
		`"matchers":[{"label":"cluster","op":"=","value":"y"}]}`)
	eventually(t, "the run of the changed matcher", func() bool { return evaluationsDone(h) == 3 })
	if got := e.groupTeams(gb); !slices.Equal(got, []string{"checkout/matcher"}) {
		t.Errorf("teams of the second alert group after the matcher changed %v", got)
	}
	if got := slugs(func() []teamOf {
		var page struct {
			Items []struct {
				Teams []teamOf `json:"teams"`
			} `json:"items"`
		}
		decode(t, api.do(http.MethodGet, "/api/v1/alert-groups/"+gb+"/alerts", ""), &page)
		if len(page.Items) != 1 {
			t.Fatalf("alerts of the second alert group %+v", page)
		}
		return page.Items[0].Teams
	}()); !slices.Equal(got, []string{`checkout/matcher/cluster="x"`}) {
		t.Errorf("teams of the alert of cluster x %v", got)
	}

	// A save of the name alone requests no evaluation: only the re-render of the Root messages that show the name
	// (C-29.FR-1).
	put(`{"name":"Checkout team","slug":"checkout","own_working_hours":false}`)
	eventually(t, "the run of the new name", func() bool {
		return h.count(t, `SELECT count(*) FROM open_group_updates`) == 0 &&
			messageSettingsDone(h, "team_name") == 1
	})
	if evaluationsDone(h) != 3 {
		t.Errorf("a new name ran %d evaluations", evaluationsDone(h)-3)
	}

	// The Team label service_team brings the third Alert Group; back to team adds nothing.
	if a := putOrganization(api, func(m map[string]any) { m["team_label"] = "service_team" }); a.status !=
		http.StatusOK {
		t.Fatalf("the team label service_team = %d %s", a.status, a.body)
	}
	eventually(t, "the team label to bring the third alert group", func() bool {
		return slices.Equal(e.groupTeams(gs), []string{"checkout/label"}) && evaluationsDone(h) == 4
	})
	entries := h.count(t, `SELECT count(*) FROM timeline_entries WHERE event = 'team_joined'`)
	if a := putOrganization(api, func(m map[string]any) { m["team_label"] = "team" }); a.status != http.StatusOK {
		t.Fatalf("the team label team = %d %s", a.status, a.body)
	}
	eventually(t, "the run of the team label back", func() bool { return evaluationsDone(h) == 5 })
	if n := h.count(t, `SELECT count(*) FROM timeline_entries WHERE event = 'team_joined'`); n != entries {
		t.Errorf("the team label back recorded %d team_joined", n-entries)
	}

	// A new slug that no longer picks the Alert leaves the Team where it is.
	put(`{"name":"Checkout team","slug":"checkout-x","own_working_hours":false}`)
	eventually(t, "the run of the new slug", func() bool { return evaluationsDone(h) == 6 })
	if got := e.groupTeams(g); !slices.Equal(got, []string{"checkout-x/label"}) {
		t.Errorf("teams after the new slug %v", got)
	}
	if n := h.count(t, `SELECT count(*) FROM timeline_entries WHERE event = 'team_joined'`); n != entries ||
		h.count(t, `SELECT count(*) FROM timeline_entries WHERE event = 'unknown_team_value' AND at >
			(SELECT created_at FROM teams WHERE public_id = '`+tc+`')`) != 0 {
		t.Errorf("the new slug recorded %d team_joined or an unknown_team_value", n-entries)
	}
}

// TestTeamEvaluationAfterACrash is C-25.FR-13 and ADR-0007 across replicas: replica A claims the row of a new Team and
// is killed while its batch waits for the Alert Group; the row stays, leased; once the lease ends, replica B, started
// afterwards, runs it, and the Team joins the Alert Group once.
func TestTeamEvaluationAfterACrash(t *testing.T) {
	h := Start(t, FakesInProcess)
	a := h.StartReplica(t, ReplicaOptions{})
	e := newMM(t, h, a)
	e.group("k1", "Latency")
	e.alert("k1", "a", `{"team":"checkout","severity":"warning","cluster":"k1"}`, "")
	e.notify("k1", "first notification")
	g := e.groupOf("k1")

	// The test holds the Alert Group, so that A's batch waits for it.
	conn, err := pgx.Connect(t.Context(), h.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(t.Context())
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `SELECT 1 FROM alert_groups WHERE public_id = $1 FOR UPDATE`, g); err != nil {
		t.Fatal(err)
	}
	var holder int64
	if err := tx.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&holder); err != nil {
		t.Fatal(err)
	}
	tc := e.api.json(http.MethodPost, "/api/v1/teams", `{"name":"checkout","slug":"checkout",`+
		`"own_working_hours":false}`, http.StatusCreated)["id"].(string)
	eventually(t, "replica A to claim the row and wait for the alert group", func() bool {
		return h.count(t, `SELECT count(*) FROM open_group_updates WHERE leased_by IS NOT NULL`) == 1 &&
			h.count(t, fmt.Sprintf(`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()
				AND wait_event_type = 'Lock' AND %d = ANY(pg_blocking_pids(pid))`, holder)) > 0
	})
	_ = a.cmd.Process.Kill()
	<-a.exited
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := h.count(t, `SELECT count(*) FROM open_group_updates WHERE kind = 'team_evaluation'`); n != 1 ||
		h.count(t, `SELECT count(*) FROM alert_group_teams`) != 0 {
		t.Fatalf("after the crash: %d rows", n)
	}

	// The lease runs on the real clock, which the development clock does not move; ending it stands in for the wait.
	h.exec(t, `UPDATE open_group_updates SET lease_until = now()`)
	b := h.StartReplica(t, ReplicaOptions{ListenApp: ":8080", ListenIngest: ":8081", ListenInternal: ":8082"})
	eventually(t, "replica B to run the row", func() bool {
		return h.count(t, `SELECT count(*) FROM open_group_updates`) == 0
	})
	onB := patClient{t: t, base: b.App, token: e.api.token}
	var v struct {
		Teams []teamOf `json:"teams"`
	}
	decode(t, onB.do(http.MethodGet, "/api/v1/alert-groups/"+g, ""), &v)
	if got := slugs(v.Teams); !slices.Equal(got, []string{"checkout/label"}) ||
		h.count(t, `SELECT count(*) FROM timeline_entries WHERE event = 'team_joined'`) != 1 ||
		!strings.Contains(b.Output(), `"event":"open_group_update_done","kind":"team_evaluation","team":"`+tc+
			`","alert_groups":1`) {
		t.Errorf("after B: teams %v, output %s", got, b.Output())
	}
}

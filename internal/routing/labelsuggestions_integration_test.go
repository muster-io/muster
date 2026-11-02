// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package routing_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/routing"
	"github.com/muster-io/muster/internal/routing/dbgen"
)

const (
	sgGroups  = 250 // Alert Groups of the Route within the period; the 200 most recent are read
	sgMembers = 30  // memberships of each, 31 in the most recent one, whose first Alert fired again
	sgOld     = 5   // Alert Groups of the Route created before the period
)

// sgSeed is one Alert and its memberships: the Alert Group (an index into the Alert Groups seeded), its labels and
// the Episodes it joined in, in the order the memberships are inserted.
type sgSeed struct {
	group    int
	labels   map[string]string
	episodes int
}

// TestIntegrationLabelSuggestionsBounded is C-29.FR-2 on PostgreSQL with the read bounded: of the Route's 250 Alert
// Groups within routing.group_key_preview_period only the 200 most recent are taken, and of their memberships, one
// Alert Group after another, the most recent first and the newest membership first, only 5,000 — the 167th Alert Group
// is cut after its 19 newest memberships; Alert Groups older than the period and those of another Organization on the
// same Route are not read; an Alert that fired again counts once in its Alert Group; at most 50 labels. The executed
// plan (auto_explain with ANALYZE, in JSON) shows that no more than 5,000 memberships reach the label expansion and
// that alert_group_alerts is read through an index, at most 5,000 rows and the rest of the last Alert Groups read.
func TestIntegrationLabelSuggestionsBounded(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		var routeID int64
		var routePublicID string
		if err := e.d.Pool.QueryRow(ctx, `SELECT id, public_id FROM routes WHERE org_id = $1 AND is_default`,
			e.orgID).Scan(&routeID, &routePublicID); err != nil {
			t.Fatal(err)
		}
		var otherOrg int64
		if err := e.d.Pool.QueryRow(ctx, `INSERT INTO organizations (public_id, name, time_zone, severity_label,
			severity_mapping, severity_styles, critical_is_urgent, instance_labels, retention_stored_snapshots_days,
			retention_alert_details_days, retention_alert_group_summaries_days, retention_audit_log_days,
			totp_required, oidc_token_grace_seconds, created_at, updated_at)
			SELECT 'RG0000000000ZZ', 'Other', time_zone, severity_label, severity_mapping, severity_styles,
				critical_is_urgent, instance_labels, retention_stored_snapshots_days, retention_alert_details_days,
				retention_alert_group_summaries_days, retention_audit_log_days, totp_required,
				oidc_token_grace_seconds, created_at, updated_at
			FROM organizations WHERE id = $1 RETURNING id`, e.orgID).Scan(&otherOrg); err != nil {
			t.Fatal(err)
		}

		// Alert Groups 1..250 within the period, 1 the most recent; 251..255 before it; 256 of the other
		// Organization on the same Route, the most recent of all.
		type groupRow struct {
			org     int64
			created time.Time
		}
		var groupRows []groupRow
		for i := 1; i <= sgGroups; i++ {
			groupRows = append(groupRows, groupRow{e.orgID, t0.Add(-time.Duration(i) * time.Minute)})
		}
		for i := 1; i <= sgOld; i++ {
			groupRows = append(groupRows, groupRow{e.orgID, t0.Add(-routing.PreviewPeriod - time.Duration(i)*time.Hour)})
		}
		groupRows = append(groupRows, groupRow{otherOrg, t0.Add(-30 * time.Second)})
		groupIDs := make([]int64, len(groupRows))
		for i, g := range groupRows {
			if err := e.d.Pool.QueryRow(ctx, `INSERT INTO alert_groups (org_id, public_id, number, route_id,
				group_key_labels, group_key_values, group_key_sha256, title, common_labels, common_annotations,
				integration_ids, status, severity_level, urgent, created_at, last_changed_at)
				VALUES ($1, $2, $3, $4, '{alertname}', '{}', sha256(convert_to($2::text, 'UTF8')), 't', '{}', '{}',
					'{}', 'firing', 'warning', false, $5, $5) RETURNING id`, g.org, fmt.Sprintf("AG%012d", i+1), i+1,
				routeID, g.created).Scan(&groupIDs[i]); err != nil {
				t.Fatal(err)
			}
		}

		var seeds []sgSeed
		for g := 1; g <= sgGroups; g++ {
			for j := range sgMembers {
				l := map[string]string{"alertname": "Alpha", "pod": fmt.Sprintf("pod-%03d-%02d", g, j),
					"severity": "warning"}
				if j%2 == 1 {
					l["alertname"] = "Zeta"
				}
				if j < 3 {
					l["severity"] = "critical"
				}
				if j == sgMembers-1 {
					for n := range 60 {
						if g <= 100+n {
							l[fmt.Sprintf("l%02d", n)] = fmt.Sprintf("v-%02d", n)
						}
					}
				}
				switch {
				case g == 1 && j == 0:
					l["flap"] = "b"
				case g == 1 && j == 1:
					l["flap"] = "a"
				case g == 167 && j <= 10:
					l["g167_old"] = "x"
				case g == 167:
					l["g167_new"] = "x"
				case g > 167 && g <= 200:
					l["cap_cut"] = "x"
				case g > 200:
					l["group_cut"] = "x"
				}
				episodes := 1
				if g == 1 && j == 0 {
					episodes = 2
				}
				seeds = append(seeds, sgSeed{group: g - 1, labels: l, episodes: episodes})
			}
		}
		for g := sgGroups; g < sgGroups+sgOld; g++ {
			for range sgMembers {
				seeds = append(seeds, sgSeed{group: g, labels: map[string]string{"alertname": "Alpha", "old_only": "x"},
					episodes: 1})
			}
		}
		for range sgMembers {
			seeds = append(seeds, sgSeed{group: len(groupRows) - 1,
				labels: map[string]string{"alertname": "Alpha", "other_org": "x"}, episodes: 1})
		}

		var orgs, groups, episodes []int64
		var fingerprints, labels []string
		for i, sd := range seeds {
			b, err := json.Marshal(sd.labels)
			if err != nil {
				t.Fatal(err)
			}
			orgs = append(orgs, groupRows[sd.group].org)
			groups = append(groups, groupIDs[sd.group])
			episodes = append(episodes, int64(sd.episodes))
			fingerprints = append(fingerprints, fmt.Sprintf("sg%06d", i))
			labels = append(labels, string(b))
		}
		// One Alert per seed, then its memberships in the order of the seeds, so that the ids of the memberships
		// grow with j; an Alert that fired again has a resolved membership for its first Episode.
		if _, err := e.d.Pool.Exec(ctx, `WITH input AS (
				SELECT * FROM unnest($1::bigint[], $2::bigint[], $3::text[], $4::text[], $5::bigint[])
					WITH ORDINALITY AS u(org_id, alert_group_id, fingerprint, labels, episodes, ord)
			), alerts_in AS (
				INSERT INTO alerts (org_id, integration_id, fingerprint, labels, annotations, status, starts_at,
					fired_at, first_seen_at, last_seen_at, updated_at)
				SELECT i.org_id, $6, i.fingerprint, i.labels::jsonb, '{}', 'firing', $7, $7, $7, $7, $7 FROM input i
				ORDER BY i.ord
				RETURNING id, fingerprint
			)
			INSERT INTO alert_group_alerts (org_id, alert_group_id, alert_id, episode, state, joined_at, starts_at,
				annotations, ended_at, resolve_reason)
			SELECT i.org_id, i.alert_group_id, a.id, ep.n,
				CASE WHEN ep.n < i.episodes THEN 'resolved' ELSE 'firing' END, $7, $7, '{}',
				CASE WHEN ep.n < i.episodes THEN $7::timestamptz END,
				CASE WHEN ep.n < i.episodes THEN 'resolved' END
			FROM input i
			JOIN alerts_in a ON a.fingerprint = i.fingerprint
			CROSS JOIN LATERAL generate_series(1, i.episodes) AS ep(n)
			ORDER BY i.ord, ep.n`, orgs, groups, fingerprints, labels, episodes, e.intID, t0); err != nil {
			t.Fatal(err)
		}
		var episodeTwo, total int64
		if err := e.d.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE episode = 2), count(*) FROM alert_group_alerts`).
			Scan(&episodeTwo, &total); err != nil || episodeTwo != 1 || total != int64(sgGroups*sgMembers+1+(sgOld+1)*sgMembers) {
			t.Fatalf("seeded %d memberships, %d of a second Episode: %v", total, episodeTwo, err)
		}
		if _, err := e.d.Pool.Exec(ctx, `ANALYZE alert_groups, alert_group_alerts, alerts`); err != nil {
			t.Fatal(err)
		}

		// Every label read, unlimited: the 5,000 memberships end inside Alert Group 167, after its 19 newest.
		type sug struct {
			label, example string
			count          int64
		}
		want := []sug{{"alertname", "Zeta", 167}, {"pod", "pod-001-00", 167}, {"severity", "warning", 167}}
		for n := 59; n >= 0; n-- {
			want = append(want, sug{fmt.Sprintf("l%02d", n), fmt.Sprintf("v-%02d", n), int64(100 + n)})
		}
		want = append(want, sug{"flap", "a", 1}, sug{"g167_new", "x", 1})
		rows, err := routing.NewStore(e.d.Pool).ListRouteLabelSuggestions(ctx, dbgen.ListRouteLabelSuggestionsParams{
			OrgID: e.orgID, RouteID: routeID, Since: t0.Add(-routing.PreviewPeriod), GroupLim: 200, AlertLim: 5000,
			Lim: 1000})
		if err != nil {
			t.Fatal(err)
		}
		var got []sug
		for _, r := range rows {
			got = append(got, sug{r.Label, r.ExampleValue, r.AlertGroupCount})
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("every label:\n got %v\nwant %v", got, want)
		}

		// Through the service, at most 50, with the executed plan.
		pool, plans := explainedPool(t, e.d.Pool.Config().ConnString())
		c := clock.NewManual(t0)
		svc := routing.New(routing.Config{OrgID: e.orgID, Store: routing.NewStore(pool),
			Audit: audit.NewWriter(logging.New(&bytes.Buffer{}, logging.LevelInfo), c), Business: c})
		list, err := svc.LabelSuggestions(ctx, routePublicID)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != routing.MaxLabelSuggestions {
			t.Fatalf("%d suggestions, want %d", len(list), routing.MaxLabelSuggestions)
		}
		for i, l := range list {
			if w := want[i]; l.Label != w.label || l.ExampleValue != w.example || l.AlertGroupCount != w.count {
				t.Fatalf("suggestion %d: %+v, want %+v", i, l, w)
			}
		}

		plan := plans.find(t, "jsonb_each_text")
		var expanded, members float64
		plan.walk(func(n planNode) {
			switch {
			case n.Type == "Function Scan" && n.Function == "jsonb_each_text":
				expanded += n.Loops
			case n.Relation == "alert_group_alerts":
				if n.Type == "Seq Scan" {
					t.Errorf("alert_group_alerts is read by a Seq Scan")
				}
				members += (n.Rows + n.RemovedByFilter) * n.Loops
				t.Logf("alert_group_alerts: %s on %s, %.0f rows × %.0f loops, %.0f removed by filter", n.Type,
					n.Index, n.Rows, n.Loops, n.RemovedByFilter)
			}
		})
		t.Logf("PostgreSQL %s: %.0f Alerts expanded by jsonb_each_text, %.0f memberships read", s.Version, expanded,
			members)
		if expanded == 0 || expanded > 5000 {
			t.Errorf("%.0f Alerts reach the label expansion, want 1 to 5,000", expanded)
		}
		// The last Alert Groups read are read whole before the LIMIT cuts them: two of at most 31 memberships.
		if members == 0 || members > 5000+2*(sgMembers+1) {
			t.Errorf("%.0f memberships read, want at most 5,000 and the rest of the last Alert Groups", members)
		}
		if cte := plan.cte("members"); cte == nil {
			t.Error("no CTE members in the plan")
		} else {
			cte.walk(func(n planNode) {
				if n.Rows*n.Loops > 5000+2*(sgMembers+1) {
					t.Errorf("%s under the CTE members yields %.0f × %.0f rows", n.Type, n.Rows, n.Loops)
				}
			})
		}
	})
}

// planNode is a node of an executed plan in JSON.
type planNode struct {
	Type            string     `json:"Node Type"`
	Relation        string     `json:"Relation Name"`
	Index           string     `json:"Index Name"`
	Function        string     `json:"Function Name"`
	SubplanName     string     `json:"Subplan Name"`
	Rows            float64    `json:"Actual Rows"`
	Loops           float64    `json:"Actual Loops"`
	RemovedByFilter float64    `json:"Rows Removed by Filter"`
	Plans           []planNode `json:"Plans"`
}

func (n planNode) walk(f func(planNode)) {
	f(n)
	for _, c := range n.Plans {
		c.walk(f)
	}
}

// cte is the subtree of the CTE named name, or nil.
func (n planNode) cte(name string) *planNode {
	var out *planNode
	n.walk(func(m planNode) {
		if out == nil && m.SubplanName == "CTE "+name {
			out = &m
		}
	})
	return out
}

type planLog struct {
	mu    sync.Mutex
	plans []string
}

// find is the plan of the statement that mentions marker.
func (l *planLog) find(t *testing.T, marker string) planNode {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, p := range l.plans {
		if !strings.Contains(p, marker) {
			continue
		}
		var doc struct {
			Plan planNode `json:"Plan"`
		}
		if err := json.Unmarshal([]byte(p), &doc); err != nil {
			t.Fatalf("plan %s: %v", p, err)
		}
		return doc.Plan
	}
	t.Fatalf("no plan mentions %s among %d", marker, len(l.plans))
	return planNode{}
}

// explainedPool is a one-connection pool on the database at connString that sends the executed plan of every
// statement, with ANALYZE, in JSON, back as a notice (auto_explain), which the returned log collects.
func explainedPool(t *testing.T, connString string) (*pgxpool.Pool, *planLog) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		t.Fatal(err)
	}
	log := &planLog{}
	cfg.MaxConns = 1
	cfg.ConnConfig.OnNotice = func(_ *pgconn.PgConn, n *pgconn.Notice) {
		if i := strings.IndexByte(n.Message, '{'); i >= 0 && strings.Contains(n.Message, "plan:") {
			log.mu.Lock()
			log.plans = append(log.plans, n.Message[i:])
			log.mu.Unlock()
		}
	}
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		for _, stmt := range []string{"LOAD 'auto_explain'", "SET auto_explain.log_min_duration = 0",
			"SET auto_explain.log_analyze = on", "SET auto_explain.log_format = 'json'",
			"SET auto_explain.log_level = 'notice'", "SET client_min_messages = 'notice'"} {
			if _, err := conn.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("%s: %w", stmt, err)
			}
		}
		return nil
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, log
}

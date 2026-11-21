// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package delivery_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/links"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/routing"
)

// TestIntegrationRouteSettingsAndLinksRerender is C-12.FR-2, FR-3, FR-9 and C-10.FR-6 on PostgreSQL, with the
// renderer, the Link rules and delivery of the runtime: two open Alert Groups and a resolved one of the Route db are
// published in two Mattermost Destinations. A change of the Route's language, of its Snooze durations and of its
// root_message template, and the creation of a Link rule that matches them, each write one open_group_updates row in
// the transaction of the save, whose run edits each of the four open Root messages once, Quietly, with no Mention and
// no new message, so that they show the Russian texts, the new Snooze buttons, the template's body and the link; the
// resolved Alert Group's Root messages are not edited. A second save of the same values writes no row, and a second
// run of each row, inserted by hand, edits nothing. A new value of a cell of a Lookup table that a rule reads edits
// each open Root message once with the new link, and the same rows again write no row; a Lookup table that no rule
// reads writes a row whose run edits nothing.
func TestIntegrationRouteSettingsAndLinksRerender(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		ctx := t.Context()
		l.fresh(t, 1000, 1)
		l.attach(t, l.dests...)
		// The renderer of the runtime computes the links of each Alert Group from the Link rules.
		lsvc := links.New(links.Config{OrgID: l.orgID, Store: links.NewStore(l.d.Pool),
			Audit: audit.NewWriter(l.logger, l.business), Business: l.business, Real: l.real,
			PublicURL: "http://localhost:8080", Log: l.logger})
		l.renderer = messages.New(messages.Config{OrgID: l.orgID, DB: l.d.Pool, PublicURL: "http://localhost:8080",
			Business: l.business, Real: l.real, Keys: l.keys, Log: l.logger, Names: l.mentions, Links: lsvc})
		l.svc = delivery.New(delivery.Config{OrgID: l.orgID, Store: delivery.NewStore(l.d.Pool, l.d.Pool),
			Business: l.business, Log: l.logger, Renderer: delivery.MessageRenderer{Renderer: l.renderer}})
		l.groups.SetRerender(l.svc.Enqueue)
		w := l.worker("rs")
		updates := l.updates()

		l.fire(t, "rs", "RsA/a", "RsB/a", "RsC/a")
		l.resolve(t, l.group(t, "RsC"))
		l.round(t, w)
		if n := l.count(t, `SELECT count(*) FROM deliveries WHERE state = 'delivered'`); n != 6 {
			t.Fatalf("%d of 6 delivered\n%s", n, l.log)
		}
		var routeID string
		if err := l.d.Pool.QueryRow(ctx, `SELECT public_id FROM routes WHERE id = $1`, l.routeID).Scan(
			&routeID); err != nil {
			t.Fatal(err)
		}
		by := routing.Requester{Actor: audit.System, Transport: audit.TransportAPI}
		save := func(t *testing.T, change func(p *routing.Policy)) {
			t.Helper()
			r, err := l.routes.Get(ctx, routeID)
			if err != nil {
				t.Fatal(err)
			}
			in := routing.Input{Name: r.Name, Matchers: r.Matchers, GroupKey: r.GroupKey, Policy: r.Policy}
			change(&in.Policy)
			if _, err := l.routes.Update(ctx, by, routeID, nil, in); err != nil {
				t.Fatal(err)
			}
		}
		rows := func(t *testing.T, kind string) int64 {
			t.Helper()
			return l.count(t, `SELECT count(*) FROM open_group_updates WHERE org_id = $1 AND kind = $2`, l.orgID, kind)
		}
		// run runs the open Alert Group updates and delivers, and returns the calls to the messengers.
		run := func(t *testing.T) []deliverytest.Call {
			t.Helper()
			l.rec.Reset()
			if _, err := updates.Round(ctx); err != nil {
				t.Fatal(err)
			}
			l.round(t, w)
			if n := l.count(t, `SELECT count(*) FROM open_group_updates`); n != 0 {
				t.Fatalf("%d rows left\n%s", n, l.log)
			}
			return l.rec.Calls()
		}
		resolved := l.group(t, "RsC")
		entries := l.count(t, `SELECT count(*) FROM timeline_entries`)
		edited := func(t *testing.T, what string, calls []deliverytest.Call, shows func(m messages.Message) bool) {
			t.Helper()
			if len(calls) != 4 {
				t.Fatalf("%s: %d calls %s, want 4 edits", what, len(calls), methods(calls))
			}
			for _, c := range calls {
				if c.Method != deliverytest.MethodUpdate || c.Loudness != groups.Quiet || len(c.Mentions) != 0 ||
					!shows(c.Message) || strings.Contains(head(c.Message), "RsC") {
					t.Errorf("%s: %s %s, %s, mentions %v", what, c.Method, head(c.Message), c.Loudness, c.Mentions)
				}
			}
			if n := l.count(t, `SELECT count(*) FROM deliveries d JOIN alert_groups g ON g.id = d.alert_group_id
				WHERE g.public_id = $1 AND d.configuration_only`, resolved); n != 0 {
				t.Errorf("%s: the resolved alert group has %d configuration edits", what, n)
			}
		}
		again := func(t *testing.T, what, kind string) {
			t.Helper()
			l.exec(t, `INSERT INTO open_group_updates (org_id, kind, route_id, requested_at)
				VALUES ($1, $2, CASE WHEN $2 = 'route_message_settings' THEN $3::bigint END, $4)`, l.orgID, kind,
				l.routeID, l.business.Now())
			if calls := run(t); len(calls) != 0 {
				t.Errorf("%s: a second run made %s", what, methods(calls))
			}
		}

		for _, c := range []struct {
			name   string
			change func(p *routing.Policy)
			shows  func(m messages.Message) bool
		}{
			{"language", func(p *routing.Policy) { p.Language = routing.LanguageRussian },
				func(m messages.Message) bool { return m.Language == routing.LanguageRussian }},
			{"snooze durations", func(p *routing.Policy) { p.SnoozeDurationsSeconds = []int64{1800, 5400} },
				func(m messages.Message) bool {
					return slices.ContainsFunc(m.Buttons, func(b messages.Button) bool {
						return b.Command == buttons.CommandSnooze &&
							strings.Contains(b.Label, messages.Duration(m.Language, 1800))
					})
				}},
			{"root_message", func(p *routing.Policy) {
				p.Templates.RootMessage = new("custom body {{ .AlertGroup.Title }}")
			}, func(m messages.Message) bool { return strings.Contains(m.Text(), "custom body") }},
		} {
			l.business.Advance(time.Second)
			save(t, c.change)
			if n := rows(t, groups.KindRouteMessageSettings); n != 1 {
				t.Fatalf("%s: %d rows of route_message_settings", c.name, n)
			}
			edited(t, c.name, run(t), c.shows)
			save(t, c.change)
			if n := rows(t, groups.KindRouteMessageSettings); n != 0 {
				t.Errorf("%s: the same values again wrote %d rows", c.name, n)
			}
			again(t, c.name, groups.KindRouteMessageSettings)
		}

		lby := links.Requester{Actor: audit.System, Transport: audit.TransportAPI}
		rule := links.RuleInput{Name: "Disk", Matchers: []links.Matcher{{Label: "team", Op: "=", Value: "db"}},
			Scope: links.Scope{Type: links.ScopeAlertGroup}, URLTemplate: "https://dash.example.org/{{ .Labels.disk }}"}
		created, err := lsvc.CreateRule(ctx, lby, rule)
		if err != nil {
			t.Fatal(err)
		}
		// A second change before a worker claims the row writes no second one: the run renders the rule as it is then.
		rule.URLTemplate = "https://dash.example.org/d/{{ .Labels.disk }}"
		if _, err := lsvc.UpdateRule(ctx, lby, created.PublicID, nil, rule); err != nil {
			t.Fatal(err)
		}
		if n := rows(t, groups.KindOrganizationLinks); n != 1 {
			t.Fatalf("a new link rule changed again: %d rows of organization_links", n)
		}
		edited(t, "a link rule", run(t), func(m messages.Message) bool {
			return slices.ContainsFunc(m.Links, func(k messages.Link) bool {
				return k.Text == "Disk" && strings.HasPrefix(k.URL, "https://dash.example.org/d/Rs")
			})
		})
		again(t, "a link rule", groups.KindOrganizationLinks)

		// A rule that reads a Lookup table: a new value of the cell it reads edits each open Root message once, with
		// the new link; the same rows again change nothing and write no row, and a second run edits nothing.
		dash := links.TableInput{Name: "dash", Columns: []string{"base"},
			Entries: []links.Entry{{Key: "db", Values: map[string]string{"base": "https://old.example.org"}}}}
		table, err := lsvc.CreateTable(ctx, lby, dash)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := lsvc.CreateRule(ctx, lby, links.RuleInput{Name: "Team board",
			Matchers: []links.Matcher{{Label: "team", Op: "=", Value: "db"}}, Scope: links.Scope{Type: links.ScopeAlertGroup},
			URLTemplate: `{{ lookup "dash" .Labels.team "base" }}/board`}); err != nil {
			t.Fatal(err)
		}
		board := func(base string) func(m messages.Message) bool {
			return func(m messages.Message) bool {
				return slices.ContainsFunc(m.Links, func(k messages.Link) bool {
					return k.Text == "Team board" && k.URL == base+"/board"
				})
			}
		}
		edited(t, "a rule that reads a lookup table", run(t), board("https://old.example.org"))
		dash.Entries[0].Values["base"] = "https://new.example.org"
		if _, err := lsvc.UpdateTable(ctx, lby, table.PublicID, nil, dash); err != nil {
			t.Fatal(err)
		}
		if n := rows(t, groups.KindOrganizationLinks); n != 1 {
			t.Fatalf("a new cell: %d rows of organization_links", n)
		}
		edited(t, "a new cell", run(t), board("https://new.example.org"))
		if _, err := lsvc.UpdateTable(ctx, lby, table.PublicID, nil, dash); err != nil {
			t.Fatal(err)
		}
		if n := rows(t, groups.KindOrganizationLinks); n != 0 {
			t.Errorf("the same rows again wrote %d rows", n)
		}
		again(t, "a new cell", groups.KindOrganizationLinks)
		if _, err := lsvc.CreateTable(ctx, lby, links.TableInput{Name: "unused", Columns: []string{"a"},
			Entries: []links.Entry{{Key: "k", Values: map[string]string{"a": "1"}}}}); err != nil {
			t.Fatal(err)
		}
		// No rule reads the table "unused" by its name, and none computes a name, so no walk is requested.
		if n := rows(t, groups.KindOrganizationLinks); n != 0 {
			t.Fatalf("a new lookup table no rule reads: %d rows of organization_links", n)
		}
		if calls := run(t); len(calls) != 0 {
			t.Errorf("a lookup table no rule reads made %s", methods(calls))
		}
		// A re-render has no lifecycle event, so it records nothing in the Timeline.
		if n := l.count(t, `SELECT count(*) FROM timeline_entries`); n != entries {
			t.Errorf("the re-renders recorded %d timeline entries", n-entries)
		}
		l.noDeadlock(t)
	})
}

// saveRoute saves the Route publicID with the change applied to what it stores.
func (l *live) saveRoute(t *testing.T, publicID string, change func(in *routing.Input)) {
	t.Helper()
	rt, err := l.routes.Get(t.Context(), publicID)
	if err != nil {
		t.Fatal(err)
	}
	in := routing.Input{Name: rt.Name, Description: &rt.Description, Matchers: rt.Matchers, Urgent: rt.Urgent,
		GroupKey: rt.GroupKey, Policy: rt.Policy}
	change(&in)
	if _, err := l.routes.Update(t.Context(), system, publicID, nil, in); err != nil {
		t.Fatal(err)
	}
}

// TestIntegrationRouteRerenderOneRow is C-26.FR-9 and C-29.FR-2 on PostgreSQL (#542): the saves of a Route before a
// worker claims its row reuse it. A change of the language, of the Snooze durations and a rename write one row of the
// Route's re-render between them, since its run renders the Route as it is then; two changes of the Minimum severity
// write one row route_minimum_severity; a save beside a row a worker has claimed writes a row of its own. The run edits
// each open Root message once, with the last values.
func TestIntegrationRouteRerenderOneRow(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		ctx := t.Context()
		l.fresh(t, 1000, 1)
		w := l.worker("ro")
		updates := l.updates()
		l.fire(t, "ro", "RoA/a", "RoB/a")
		l.round(t, w)
		var routeID string
		if err := l.d.Pool.QueryRow(ctx, `SELECT public_id FROM routes WHERE id = $1`, l.routeID).Scan(
			&routeID); err != nil {
			t.Fatal(err)
		}
		rows := func(t *testing.T, kinds ...string) int64 {
			t.Helper()
			return l.count(t, `SELECT count(*) FROM open_group_updates WHERE org_id = $1 AND route_id = $2
				AND kind = ANY($3::text[])`, l.orgID, l.routeID, kinds)
		}
		rerender := []string{groups.KindRouteMessageSettings, groups.KindRouteMessageLabels}
		for i, change := range []func(in *routing.Input){
			func(in *routing.Input) { in.Policy.Language = routing.LanguageRussian },
			func(in *routing.Input) { in.Policy.SnoozeDurationsSeconds = []int64{1800, 5400} },
			func(in *routing.Input) { in.Name = "db-renamed" },
		} {
			l.business.Advance(time.Second)
			l.saveRoute(t, routeID, change)
			if n := rows(t, rerender...); n != 1 {
				t.Fatalf("after save %d: %d rows of the route's re-render, want 1", i+1, n)
			}
		}
		for _, m := range []string{routing.MinimumWarning, routing.MinimumNone} {
			l.saveRoute(t, routeID, func(in *routing.Input) { in.MinimumSeverity = &m })
		}
		if n := rows(t, groups.KindRouteMinimumSeverity); n != 1 {
			t.Fatalf("two changes of the minimum severity: %d rows, want 1", n)
		}
		// A row a worker has claimed may have walked past some Alert Groups: a save beside it writes a row of its own.
		l.exec(t, `UPDATE open_group_updates SET leased_by = 'r1', lease_until = $3
			WHERE org_id = $1 AND route_id = $2 AND kind = ANY($4::text[])`, l.orgID, l.routeID,
			l.real.Now().Add(time.Minute), rerender)
		l.saveRoute(t, routeID, func(in *routing.Input) { in.Policy.SnoozeDurationsSeconds = []int64{1800} })
		if n, free := rows(t, rerender...), l.count(t, `SELECT count(*) FROM open_group_updates WHERE org_id = $1
			AND route_id = $2 AND kind = ANY($3::text[]) AND leased_by IS NULL`, l.orgID, l.routeID,
			rerender); n != 2 || free != 1 {
			t.Fatalf("a save beside a claimed row: %d rows, %d unclaimed, want 2 and 1", n, free)
		}
		l.exec(t, `UPDATE open_group_updates SET leased_by = NULL, lease_until = NULL WHERE org_id = $1`, l.orgID)
		l.rec.Reset()
		if _, err := updates.Round(ctx); err != nil {
			t.Fatal(err)
		}
		l.round(t, w)
		if n := l.count(t, `SELECT count(*) FROM open_group_updates`); n != 0 {
			t.Fatalf("%d rows left\n%s", n, l.log)
		}
		calls := l.rec.Calls()
		if len(calls) != 2 {
			t.Fatalf("%d calls %s, want 2 edits", len(calls), methods(calls))
		}
		for _, c := range calls {
			if c.Method != deliverytest.MethodUpdate || c.Loudness != groups.Quiet ||
				c.Message.Language != routing.LanguageRussian {
				t.Errorf("%s %s, %s, %s", c.Method, head(c.Message), c.Loudness, c.Message.Language)
			}
		}
		l.noDeadlock(t)
	})
}

// TestIntegrationStormSummaryFollowsRoute is C-11.FR-6 and C-12.FR-10 on PostgreSQL (#542): during a Storm, a rename
// of its Route and a change of its language each edit the Storm summary Quietly, with no Mention, so that it names the
// Route as it is now in its language, with no new Alert Group held; a change the summary does not show edits nothing.
func TestIntegrationStormSummaryFollowsRoute(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		l.fresh(t, 1000, 1)
		updates := l.updates()
		_, route := l.newRoute(t, "sr", 1, "DSAAAAAAAAAAA1")
		l.fireAlerts(t, "sr1", alert{name: "SrA/a", team: "sr"}, alert{name: "SrB/a", team: "sr"},
			alert{name: "SrC/a", team: "sr"})
		l.round(t, l.a)
		summaries := func(calls []deliverytest.Call) []deliverytest.Call {
			return slices.DeleteFunc(slices.Clone(calls), func(c deliverytest.Call) bool {
				return c.Message.Kind != messages.KindStorm
			})
		}
		if got := summaries(l.rec.Calls()); len(got) != 1 || got[0].Method != deliverytest.MethodPublish ||
			!strings.HasPrefix(got[0].Message.Lines[0], "⛈ Storm on sr: ") {
			t.Fatalf("the storm summary: %+v", got)
		}
		held := l.count(t, `SELECT alert_group_count FROM storms WHERE ended_at IS NULL`)
		run := func(t *testing.T) []deliverytest.Call {
			t.Helper()
			l.rec.Reset()
			if _, err := updates.Round(t.Context()); err != nil {
				t.Fatal(err)
			}
			l.round(t, l.a)
			return summaries(l.rec.Calls())
		}
		for _, c := range []struct {
			name   string
			change func(in *routing.Input)
			want   string
		}{
			{"rename", func(in *routing.Input) { in.Name = "sr-renamed" }, "⛈ Storm on sr-renamed: "},
			{"language", func(in *routing.Input) { in.Policy.Language = routing.LanguageRussian },
				"⛈ Шторм в маршруте sr-renamed: "},
		} {
			l.business.Advance(time.Second)
			l.saveRoute(t, route, c.change)
			got := run(t)
			if len(got) != 1 || got[0].Method != deliverytest.MethodUpdate || got[0].Loudness != groups.Quiet ||
				len(got[0].Mentions) != 0 || !strings.HasPrefix(got[0].Message.Lines[0], c.want) {
				t.Fatalf("%s: summary calls %s %+v, want an edit to %q", c.name, methods(got), got, c.want)
			}
			t.Logf("%s: %q", c.name, got[0].Message.Lines[0])
		}
		l.saveRoute(t, route, func(in *routing.Input) { in.Policy.SnoozeDurationsSeconds = []int64{1800} })
		if got := run(t); len(got) != 0 {
			t.Errorf("a change of the snooze durations edited the summary: %s", methods(got))
		}
		if n := l.count(t, `SELECT alert_group_count FROM storms WHERE ended_at IS NULL`); n != held {
			t.Errorf("the storm counts %d alert groups, %d before", n, held)
		}
		l.noDeadlock(t)
	})
}

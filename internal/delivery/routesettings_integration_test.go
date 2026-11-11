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

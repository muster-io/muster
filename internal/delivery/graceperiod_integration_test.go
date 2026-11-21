// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package delivery_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/routing"
	"github.com/muster-io/muster/internal/timers"
	timersdb "github.com/muster-io/muster/internal/timers/dbgen"
)

// TestIntegrationGracePeriodEndWithLineTemplate is C-09.FR-5 with C-29.FR-3 on PostgreSQL, on a Route with a line
// template whose Destinations are a messenger and outgoing webhooks in the template and the events mode: a person
// resolves an Alert Group through the API, with a Note, while its two Alerts still fire; when the timer
// grace_period_end fires, the Alerts move to a new Alert Group marked "firing again after a manual resolve of #N", and
// the resolved one, with no Alert left to list, is rendered again through the dispatcher's re-render step, delivery's
// Enqueue and messages.Renderer without its line template, without a Fallback template and without a panic; the next
// round edits its Root message and publishes the new one with a line per Alert.
func TestIntegrationGracePeriodEndWithLineTemplate(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		ctx := t.Context()
		l.fresh(t, 1000, 1)
		const cfg = `{"create":{"method":"POST","url":"http://127.0.0.1/m","headers":[],` +
			`"body":"{{ .AlertGroup.Number }}","extract":[]},"update":{"method":"PUT",` +
			`"url":"http://127.0.0.1/m/{{ .AlertGroup.Status }}","headers":[],"body":null,"extract":[]},` +
			`"open_thread":null,"reply_in_thread":null}`
		var hook int64
		if err := l.d.Pool.QueryRow(ctx, `INSERT INTO destinations (org_id, public_id, type, name, webhook_mode,
			webhook_template_config, signing_secret_ciphertext, signing_secret_key_id, signing_secret_updated_at,
			mentions, limiter_limit, limiter_per_seconds, health, created_at, updated_at)
			VALUES ($1, 'DSAAAAAAAAAAGT', 'webhook', 'chat', 'template', $3, '\x01', 'k1', $2, '{}', 1000, 1,
			'healthy', $2, $2) RETURNING id`, l.orgID, l.business.Now(), cfg).Scan(&hook); err != nil {
			t.Fatal(err)
		}
		events := l.eventsHook(t, "DSAAAAAAAAAAGE", "events", false)
		bodies := &stubBodies{}
		svc := delivery.New(delivery.Config{OrgID: l.orgID, Store: delivery.NewStore(l.d.Pool, l.d.Pool),
			Business: l.business, Real: l.real, Log: l.logger, Renderer: delivery.MessageRenderer{Renderer: l.renderer},
			Bodies: bodies})
		l.groups.SetRerender(svc.Enqueue)
		defer l.groups.SetRerender(l.svc.Enqueue)
		w := l.worker("r1")
		w.Adapters[delivery.TypeWebhook] = l.rec
		w.Events = &fakeSender{}
		policy := routing.Profiles()[0].Policy
		policy.StormThreshold = 1_000_000
		policy.Templates.Line = ptrTo(`{{ .Labels.instance }} {{ .Annotations.summary }}`)
		rt, err := l.routes.Create(ctx, system, routing.Input{Name: "grace", GroupKey: []string{"alertname"},
			Policy: policy, Matchers: []routing.Matcher{{Label: "team", Op: "=", Value: "grace"}},
			DestinationIDs: []string{"DSAAAAAAAAAAA1", "DSAAAAAAAAAAGT", "DSAAAAAAAAAAGE"}})
		if err != nil {
			t.Fatal(err)
		}
		body := `{"groupKey":"grace","status":"firing","alerts":[` +
			`{"status":"firing","labels":{"alertname":"GraceDisk","team":"grace","instance":"db1"},` +
			`"annotations":{"summary":"Disk full"},"startsAt":"2026-10-07T11:00:00Z"},` +
			`{"status":"firing","labels":{"alertname":"GraceDisk","team":"grace","instance":"db2"},` +
			`"annotations":{"summary":"Disk full"},"startsAt":"2026-10-07T11:00:00Z"}]}`
		if _, err := l.snaps.Store(ctx, ingest.Received{IntegrationID: l.intID, Body: []byte(body)}); err != nil {
			t.Fatal(err)
		}
		l.process(t)
		resolved := l.group(t, "GraceDisk")
		l.round(t, w)
		pubs := only(to(l.rec.Calls(), "DSAAAAAAAAAAA1"), deliverytest.MethodPublish)
		if len(pubs) != 1 || !listed(pubs[0].Message, "db1 Disk full", "db2 Disk full") ||
			len(only(to(l.rec.Calls(), "DSAAAAAAAAAAGT"), deliverytest.MethodPublish)) != 1 {
			t.Fatalf("the publication with the line template: %s %+v", methods(l.rec.Calls()), pubs)
		}
		number := pubs[0].Message.Heading.Number

		// A person resolves it through the API with a Note while both Alerts still fire: the Grace period starts.
		note := "Rebooted both hosts"
		caller := groups.Caller{Actor: l.alice.Actor, Transport: audit.TransportAPI,
			Permissions: []auth.Permission{groups.PermissionResolve, groups.PermissionNote}}
		if _, err := l.groups.Resolve(ctx, caller, resolved, &note); err != nil {
			t.Fatal(err)
		}
		gid := l.groupID(t, resolved)
		v, err := l.groups.Get(ctx, resolved)
		if err != nil || v.Status != groups.StatusResolved || len(v.Notices) != 1 ||
			v.Notices[0].Kind != groups.NoticeAlertsStillFiring || *v.Notices[0].Count != 2 {
			t.Fatalf("resolved %+v, %v", v, err)
		}
		if n := l.count(t, `SELECT count(*) FROM timers WHERE kind = 'grace_period_end' AND alert_group_id = $1`,
			gid); n != 1 {
			t.Fatalf("%d grace_period_end timers", n)
		}
		if n := l.count(t, `SELECT count(*) FROM timeline_entries WHERE alert_group_id = $1 AND event = 'resolved'
			AND transport = 'api'`, gid) + l.count(t, `SELECT count(*) FROM notes WHERE alert_group_id = $1
			AND body = $2`, gid, note); n != 2 {
			t.Fatalf("the resolve and its note: %d", n)
		}
		l.round(t, w)
		l.rec.Reset()

		// The Grace period ends: the timer fires as the runtime's handler fires it.
		grace := &timers.Worker{Store: timers.NewStore(l.d.Pool), Lease: db.Lease{Owner: "grace",
			Duration: timers.Lease, Clocks: clock.Clocks{Business: l.business, Real: l.real}},
			Organizations: func(context.Context) ([]int64, error) { return []int64{l.orgID}, nil }, Log: l.logger,
			Handlers: map[string]timers.Handler{groups.TimerGracePeriodEnd: func(ctx context.Context,
				tx timersdb.DBTX, _ int64, tm timers.Timer) (func(context.Context), error) {
				if tm.AlertGroupID == nil {
					return nil, nil
				}
				r, err := l.groups.EndGracePeriod(ctx, tx, *tm.AlertGroupID)
				return r.Committed, err
			}}}
		l.business.Advance(time.Duration(rt.Policy.GracePeriodSeconds)*time.Second + time.Second)
		if _, err := grace.Round(ctx); err != nil {
			t.Fatal(err)
		}
		if n := l.count(t, `SELECT count(*) FROM timers WHERE kind = 'grace_period_end' AND alert_group_id = $1`,
			gid); n != 0 || strings.Contains(l.log.String(), `"event":"timer_failed"`) {
			t.Fatalf("the timer did not fire: %d left, log %s", n, l.log)
		}
		if n := l.count(t, `SELECT count(*) FROM alert_groups WHERE id = $1 AND status = 'resolved'
			AND grace_deadline IS NULL AND firing_alert_count = 0`, gid); n != 1 {
			t.Errorf("the resolved alert group after its grace period: %d", n)
		}
		if n := l.count(t, `SELECT count(*) FROM alert_group_alerts WHERE alert_group_id = $1 AND state = 'moved'`,
			gid); n != 2 {
			t.Errorf("%d alerts moved", n)
		}
		again := l.group(t, "GraceDisk")
		v, err = l.groups.Get(ctx, again)
		if err != nil || again == resolved || v.Status != groups.StatusFiring || len(v.Notices) != 1 ||
			v.Notices[0].Kind != groups.NoticeFiringAgainAfterManualResolve || *v.Notices[0].ResolvedNumber != number {
			t.Errorf("firing again %+v, %v", v, err)
		}
		if n := l.count(t, `SELECT count(*) FROM timeline_entries WHERE alert_group_id IN ($1, $2)
			AND system_event = 'fallback_template_used'`, gid, l.groupID(t, again)) +
			l.count(t, `SELECT count(*) FROM routes WHERE id = $1 AND template_error IS NOT NULL`, rt.ID); n != 0 {
			t.Errorf("the line template failed: %d", n)
		}
		// The renderer delivery uses renders the resolved Alert Group with no Alert left in every markup.
		markups := []messages.Markup{messages.MarkupMarkdown, messages.MarkupHTML, messages.MarkupPlain,
			messages.MarkupCommonMark, messages.MarkupMrkdwn}
		roots, err := delivery.MessageRenderer{Renderer: l.renderer}.Roots(ctx, l.d.Pool,
			delivery.GroupView{ID: gid, PublicID: resolved, Number: number}, markups)
		if err != nil || len(roots.Failures) != 0 || len(roots.Messages) != len(markups) {
			t.Fatalf("roots %+v, %v", roots, err)
		}
		for mk, rd := range roots.Messages {
			if rd.Failure != nil || slices.Contains(rd.Rendered, messages.TemplateLine) || rd.Message.AlertsLine != nil ||
				rd.Message.Heading == nil || rd.Message.Heading.Number != number {
				t.Errorf("%s: %+v", mk, rd)
			}
		}

		// The next round edits the resolved Root message, now without Alerts, and publishes the new Alert Group.
		l.round(t, w)
		var edited, published []deliverytest.Call
		for _, c := range to(l.rec.Calls(), "DSAAAAAAAAAAA1") {
			switch {
			case c.Method == deliverytest.MethodUpdate && c.Message.Heading != nil && c.Message.Heading.Number == number:
				edited = append(edited, c)
			case c.Method == deliverytest.MethodPublish:
				published = append(published, c)
			}
		}
		if len(edited) != 1 || edited[0].Message.AlertsLine != nil || edited[0].Message.Colour != messages.ColourResolved ||
			len(published) != 1 || published[0].Message.Heading.Number == number ||
			!listed(published[0].Message, "db1 Disk full", "db2 Disk full") {
			t.Errorf("after the grace period: %s, edited %+v, published %+v", methods(l.rec.Calls()), edited, published)
		}
		if n := l.count(t, `SELECT count(*) FROM webhook_events WHERE destination_id = $1 AND alert_group_id = $2`,
			events, l.groupID(t, again)); n == 0 {
			t.Errorf("no webhook event of the new alert group")
		}
		t.Logf("#%d resolved through the API with a note while 2 Alerts fired; grace_period_end fired: 2 Alerts "+
			"moved to %s (firing again after #%d); the resolved Root message re-rendered without Alerts in %d "+
			"markups and edited, calls %s", number, again, number, len(roots.Messages), methods(l.rec.Calls()))
	})
}

// listed reports whether the Alerts line of a Root message has exactly the lines of the line template given.
func listed(m messages.Message, lines ...string) bool {
	if m.AlertsLine == nil || len(m.AlertsLine.Lines) != len(lines) {
		return false
	}
	for i, l := range m.AlertsLine.Lines {
		if l.Text != lines[i] {
			return false
		}
	}
	return true
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package notificationrules_test

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/notificationrules"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/users"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

var t0 = time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)

const gw = "CN0000000000G1"

type env struct {
	d     *db.DB
	orgID int64
	clock *clock.Manual
	log   *bytes.Buffer
	svc   *notificationrules.Service
	org   *organization.Service
	admin *users.Admin
	ids   map[string]int64
	by    notificationrules.Requester
}

// setup is a migrated database with the Organization, the Audit log partition of November 2026, the Notification
// gateway CN0000000000G1, the Admin ops and the Responders petr, with a confirmed contact at the gateway, and ivan,
// without one.
func setup(t *testing.T, s dbtest.Server) *env {
	t.Helper()
	ctx := t.Context()
	conn := config.Database{URL: logging.Secret(s.NewDatabase(t)), SSLMode: "disable"}
	d, err := db.Open(ctx, conn, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	var out bytes.Buffer
	logger := logging.New(&out, logging.LevelInfo)
	if err := d.Migrate(ctx, logger); err != nil {
		t.Fatal(err)
	}
	if err := organization.Ensure(ctx, organization.NewStore(d.Pool), logger, t0); err != nil {
		t.Fatal(err)
	}
	org, err := organization.NewStore(d.Pool).GetOrganization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := clock.NewManual(t0)
	w := audit.NewWriter(logger, c)
	base, _ := url.Parse("http://localhost:8080")
	e := &env{d: d, orgID: org.ID, clock: c, log: &out, ids: map[string]int64{},
		svc: notificationrules.New(notificationrules.Config{OrgID: org.ID, Store: notificationrules.NewStore(d.Pool),
			Audit: w, Business: c}),
		org:   organization.NewService(org.ID, organization.NewSettingsStore(d.Pool), w, c),
		admin: users.NewAdmin(org.ID, users.NewAdminStore(d.Pool), w, c, base),
	}
	e.admin.SetRuleRemover(e.svc)
	e.exec(t, `CREATE TABLE audit_log_p202611 PARTITION OF audit_log
		FOR VALUES FROM ('2026-11-01 00:00:00+00') TO ('2026-12-01 00:00:00+00')`)
	e.exec(t, `INSERT INTO connections (org_id, public_id, type, name, bridge_base_url, bridge_event_delivery,
		bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds, bridge_info,
		created_at, updated_at)
		VALUES ($1, $2, 'bridge', 'Example Gateway', 'http://gw', 'poll', '\x00', 'k1', $3, 1, 1,
		'{"roles": ["gateway"]}', $3, $3)`, org.ID, gw, t0)
	for i, login := range []string{"ops", "petr", "ivan"} {
		role := "responder"
		if login == "ops" {
			role = "admin"
		}
		var id int64
		if err := d.Pool.QueryRow(ctx, `INSERT INTO users (org_id, public_id, login, name, role, source, status,
			created_at, updated_at) VALUES ($1, $2, $3, $3, $4, 'local', 'active', $5, $5) RETURNING id`, org.ID,
			"SR00000000000"+string(rune('A'+i)), login, role, t0).Scan(&id); err != nil {
			t.Fatal(err)
		}
		e.ids[login] = id
	}
	e.exec(t, `INSERT INTO gateway_contacts (org_id, public_id, user_id, connection_id, contact, confirmed_at,
		created_at) SELECT $1, 'GC0000000000P1', $2, id, '+10000000002', $3, $3 FROM connections`, org.ID,
		e.ids["petr"], t0)
	e.by = notificationrules.Requester{Actor: audit.User(e.ids["ops"], "SR00000000000A"),
		Transport: audit.TransportAPI, Address: netip.MustParseAddr("192.0.2.1")}
	return e
}

func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.d.Pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.d.Pool.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// important is the important rule of the User login as its kinds, waits by their seconds: "call,mention,300,mention".
func (e *env) important(t *testing.T, login string) string {
	t.Helper()
	r, err := e.svc.Get(t.Context(), e.ids[login])
	if err != nil {
		t.Fatal(err)
	}
	return kinds(r.Rules.Important) + " " + r.State
}

func kinds(r []notificationrules.ActionView) string {
	parts := make([]string, len(r))
	for i, a := range r {
		switch {
		case a.Kind == notificationrules.KindWait:
			parts[i] = strconv.Itoa(a.WaitSeconds)
		case !a.Available:
			parts[i] = a.Kind + " " + a.Reason
		default:
			parts[i] = a.Kind
		}
	}
	return strings.Join(parts, ",")
}

// setOrganization replaces the Organization's important rule with a Mention, a wait of seconds and a Mention.
func (e *env) setOrganization(t *testing.T, seconds int) {
	t.Helper()
	if err := e.organizationRules(t.Context(), seconds); err != nil {
		t.Fatal(err)
	}
}

func (e *env) organizationRules(ctx context.Context, seconds int) error {
	o, err := e.org.Get(ctx)
	if err != nil {
		return err
	}
	in := organization.Input{Name: o.Name, TimeZone: o.TimeZone, SeverityLabel: o.SeverityLabel,
		SeverityMapping: o.SeverityMapping, SeverityStyles: o.SeverityStyles, InstanceLabels: o.InstanceLabels,
		Retention: o.Retention, TOTPRequired: o.TOTPRequired, OIDCTokenGraceSeconds: int64(o.OIDCTokenGrace.Seconds()),
		HeartbeatProxy: organization.ProxyInput{Enabled: o.OutgoingHeartbeat.Proxy.Enabled},
		NotificationRules: &notificationrules.SetInput{
			Normal: &[]notificationrules.ActionInput{{Kind: "mention"}},
			Important: &[]notificationrules.ActionInput{{Kind: "mention"}, {Kind: "wait", WaitSeconds: &seconds},
				{Kind: "mention"}},
		}}
	_, err = e.org.Update(ctx, audit.User(e.ids["ops"], "SR00000000000A"), audit.TransportAPI, netip.Addr{}, nil, in)
	return err
}

// TestIntegrationFollowTheOrganization is C-31.FR-2, C-31.AC-4 and journal D569 against PostgreSQL: an existing
// Organization reads the default rules; a User in the state organization follows a change of
// organization.notification_rules at once, while the rules of Users in call, no_call and custom stay as they were;
// resetMyNotificationRules returns a User to organization, who then follows the Organization again; the version moves
// with every write, also after a reset; the changes are in the Audit log with their diffs; a removed contact and a
// deleted gateway leave the call in the rule, unavailable.
func TestIntegrationFollowTheOrganization(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		if got := e.important(t, "ivan"); got != "mention,300,mention organization" {
			t.Fatalf("ivan at first = %s", got)
		}
		petr, err := e.svc.Update(ctx, e.by, e.ids["petr"], nil, notificationrules.Update{
			State: notificationrules.StateCall})
		if err != nil || petr.Gateway == nil || petr.Gateway.PublicID != gw || petr.Version == 0 {
			t.Fatalf("petr call = %+v, %v", petr, err)
		}
		_, err = e.svc.Update(ctx, e.by, e.ids["ivan"], nil, notificationrules.Update{
			State: notificationrules.StateCall})
		if fe, ok := errors.AsType[*notificationrules.FieldError](err); !ok || fe.Code != "contact_required" ||
			fe.Pointer != "/gateway_id" {
			t.Errorf("ivan call without a contact: %v", err)
		}
		ops := e.ids["ops"] // the Admin's own rules stand for a third User: custom
		if _, err := e.svc.Update(ctx, e.by, ops, nil, notificationrules.Update{State: notificationrules.StateCustom,
			Rules: notificationrules.SetInput{
				Normal:    &[]notificationrules.ActionInput{{Kind: "mention"}, {Kind: "mention"}, {Kind: "mention"}},
				Important: &[]notificationrules.ActionInput{{Kind: "mention"}}}}); err != nil {
			t.Fatal(err)
		}

		e.setOrganization(t, 600)
		if got := e.important(t, "ivan"); got != "mention,600,mention organization" {
			t.Errorf("ivan after the change = %s", got)
		}
		if got := e.important(t, "petr"); got != "call,mention,300,mention call" {
			t.Errorf("petr after the change = %s", got)
		}
		if got := e.important(t, "ops"); got != "mention custom" {
			t.Errorf("the custom rules after the change = %s", got)
		}
		if got := e.count(t, `SELECT count(*) FROM audit_log WHERE action = 'organization.updated'
			AND diff @> '[{"pointer": "/notification_rules/important"}]'`); got != 1 {
			t.Errorf("%d organization.updated with the rules", got)
		}

		// A removed contact, then a deleted gateway: the call stays, unavailable.
		e.exec(t, `DELETE FROM gateway_contacts`)
		if got := e.important(t, "petr"); got != "call no_confirmed_contact,mention,300,mention call" {
			t.Errorf("without the contact = %s", got)
		}
		e.exec(t, `UPDATE connections SET deleted_at = $1`, t0)
		if got := e.important(t, "petr"); got != "call gateway_deleted,mention,300,mention call" {
			t.Errorf("the gateway deleted = %s", got)
		}

		// no_call keeps the Organization's rules of the moment; reset follows again.
		before, _ := e.svc.Get(ctx, e.ids["petr"])
		noCall, err := e.svc.Update(ctx, e.by, e.ids["petr"], &before.Version, notificationrules.Update{
			State: notificationrules.StateNoCall})
		if err != nil || kinds(noCall.Rules.Important) != "mention,600,mention" || noCall.Version <= before.Version {
			t.Fatalf("no_call = %+v, %v", noCall, err)
		}
		if _, err := e.svc.Update(ctx, e.by, e.ids["petr"], &before.Version, notificationrules.Update{
			State: notificationrules.StateNoCall}); !errors.Is(err, notificationrules.ErrVersionMismatch) {
			t.Errorf("a stale version: %v", err)
		}
		e.setOrganization(t, 1200)
		if got := e.important(t, "petr"); got != "mention,600,mention no_call" {
			t.Errorf("no_call after the change = %s", got)
		}
		reset, err := e.svc.Reset(ctx, e.by, e.ids["petr"])
		if err != nil || reset.State != notificationrules.StateOrganization || reset.Version%2 != 1 ||
			kinds(reset.Rules.Important) != "mention,1200,mention" {
			t.Fatalf("reset = %+v, %v", reset, err)
		}
		e.setOrganization(t, 60)
		if got := e.important(t, "petr"); got != "mention,60,mention organization" {
			t.Errorf("petr after the reset and a change = %s", got)
		}
		// The ETag of the state organization follows the Organization: the one read before its change is stale.
		if _, err := e.svc.Update(ctx, e.by, e.ids["petr"], &reset.Version, notificationrules.Update{
			State: notificationrules.StateNoCall}); !errors.Is(err, notificationrules.ErrVersionMismatch) {
			t.Errorf("an If-Match from before the Organization's change: %v", err)
		}
		current, err := e.svc.Get(ctx, e.ids["petr"])
		if err != nil || current.Version == reset.Version || current.Version%2 != 1 {
			t.Fatalf("the ETag after the Organization's change = %d, before %d, %v", current.Version, reset.Version, err)
		}
		again, err := e.svc.Update(ctx, e.by, e.ids["petr"], &current.Version, notificationrules.Update{
			State: notificationrules.StateNoCall})
		if err != nil || again.Version <= noCall.Version {
			t.Errorf("a write after the reset: version %d after %d, %v", again.Version, noCall.Version, err)
		}
		entries := e.count(t, `SELECT count(*) FROM audit_log WHERE action = 'notification_rules.updated'
			AND resource_type = 'notification_rules' AND resource_public_id = 'SR00000000000B'`)
		if entries != 4 {
			t.Errorf("%d entries of petr's rules", entries)
		}
		if got := e.count(t, `SELECT count(*) FROM audit_log WHERE action = 'notification_rules.updated'
			AND resource_public_id = 'SR00000000000B' AND diff @> '[{"pointer": "/state", "before": "no_call",
			"after": "organization"}]'`); got != 1 {
			t.Errorf("the reset's diff: %d", got)
		}
	})
}

// TestIntegrationDeletedUser is C-31.FR-5 and C-03.FR-13 against PostgreSQL: deleting a User deletes their rules in
// the transaction of the deletion; their rules then answer not found and cannot be set again.
func TestIntegrationDeletedUser(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		if _, err := e.svc.Update(ctx, e.by, e.ids["petr"], nil, notificationrules.Update{
			State: notificationrules.StateCall}); err != nil {
			t.Fatal(err)
		}
		if got := e.count(t, `SELECT count(*) FROM notification_rules WHERE user_id = $1`, e.ids["petr"]); got != 1 {
			t.Fatalf("%d rows before", got)
		}
		if err := e.admin.Delete(ctx, users.Requester{Actor: e.by.Actor, Transport: audit.TransportAPI},
			"SR00000000000B", nil); err != nil {
			t.Fatal(err)
		}
		if got := e.count(t, `SELECT count(*) FROM notification_rules WHERE user_id = $1`, e.ids["petr"]); got != 0 {
			t.Errorf("%d rows after the deletion", got)
		}
		if _, err := e.svc.GetOfUser(ctx, "SR00000000000B"); !errors.Is(err, notificationrules.ErrUserNotFound) {
			t.Errorf("read after the deletion: %v", err)
		}
		if _, err := e.svc.Update(ctx, e.by, e.ids["petr"], nil, notificationrules.Update{
			State: notificationrules.StateNoCall}); !errors.Is(err, notificationrules.ErrUserNotFound) {
			t.Errorf("set after the deletion: %v", err)
		}
	})
}

// TestIntegrationRulesRaces: concurrent changes of one User's rules, by the User and by an Admin, the deletion of the
// User and changes of the Organization's rules never deadlock — a change of rules locks the User's row first, then the
// rules' row, as the User's deletion does, and reads the Organization's row without a lock — and with If-Match exactly
// one of two changes from the same version wins; once the User is deleted, no rules of theirs are left.
func TestIntegrationRulesRaces(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		for round := range 8 {
			var wg sync.WaitGroup
			errs := make([]error, 6)
			states := []string{notificationrules.StateCall, notificationrules.StateNoCall,
				notificationrules.StateOrganization, notificationrules.StateNoCall}
			for i, st := range states {
				wg.Go(func() {
					_, errs[i] = e.svc.Update(ctx, e.by, e.ids["petr"], nil, notificationrules.Update{State: st})
				})
			}
			wg.Go(func() {
				_, errs[4] = e.svc.UpdateOfUser(ctx, e.by, "SR00000000000C", nil, notificationrules.Update{
					State: notificationrules.StateNoCall})
			})
			wg.Go(func() { errs[5] = e.organizationRules(ctx, 60*(round+1)) })
			wg.Wait()
			for i, err := range errs {
				if err != nil {
					t.Errorf("round %d, change %d: %v", round, i, err)
				}
			}
		}

		// Two changes from the same version: one wins, the other is stale.
		r, err := e.svc.Reset(ctx, e.by, e.ids["petr"])
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		results := make([]error, 2)
		for i, st := range []string{notificationrules.StateCall, notificationrules.StateCustom} {
			wg.Go(func() {
				_, results[i] = e.svc.Update(ctx, e.by, e.ids["petr"], &r.Version, notificationrules.Update{
					State: st, Rules: notificationrules.SetInput{
						Normal:    &[]notificationrules.ActionInput{{Kind: "mention"}, {Kind: "mention"}},
						Important: &[]notificationrules.ActionInput{{Kind: "mention"}}}})
			})
		}
		wg.Wait()
		if (results[0] == nil) == (results[1] == nil) || !errors.Is(errors.Join(results...),
			notificationrules.ErrVersionMismatch) {
			t.Errorf("two changes from version %d: %v", r.Version, results)
		}

		// Changes while the User is deleted.
		var deleteErr error
		changes := make([]error, 4)
		wg.Go(func() {
			deleteErr = e.admin.Delete(ctx, users.Requester{Actor: e.by.Actor, Transport: audit.TransportAPI},
				"SR00000000000C", nil)
		})
		for i := range changes {
			wg.Go(func() {
				if i%2 == 1 {
					_, changes[i] = e.svc.Reset(ctx, e.by, e.ids["ivan"])
					return
				}
				_, changes[i] = e.svc.Update(ctx, e.by, e.ids["ivan"], nil, notificationrules.Update{
					State: notificationrules.StateNoCall})
			})
		}
		wg.Wait()
		if deleteErr != nil {
			t.Errorf("the deletion: %v", deleteErr)
		}
		for i, err := range changes {
			var pe *pgconn.PgError
			if errors.As(err, &pe) {
				t.Errorf("change %d: %s %s", i, pe.Code, pe.Message)
			} else if err != nil && !errors.Is(err, notificationrules.ErrUserNotFound) {
				t.Errorf("change %d: %v", i, err)
			}
		}
		if got := e.count(t, `SELECT count(*) FROM notification_rules WHERE user_id = $1`, e.ids["ivan"]); got != 0 {
			t.Errorf("%d rows of the deleted User", got)
		}
		if strings.Contains(e.log.String(), "40P01") || strings.Contains(e.log.String(), "deadlock") {
			t.Errorf("a deadlock was logged")
		}
	})
}

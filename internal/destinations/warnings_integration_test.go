// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package destinations_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/destinations"
	"github.com/muster-io/muster/internal/destinations/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/organization"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

// countingStore counts the warning queries of a read.
type countingStore struct {
	destinations.Store
	warningReads int
}

// ListMentionWarnings counts the call and runs the query.
func (c *countingStore) ListMentionWarnings(ctx context.Context, arg dbgen.ListMentionWarningsParams) (
	[]dbgen.ListMentionWarningsRow, error) {
	c.warningReads++
	return c.Store.ListMentionWarnings(ctx, arg)
}

// TestIntegrationMentionWarnings is C-28.FR-3, AC-4 and C-11.FR-21 against PostgreSQL, on the delivery events as
// delivery writes them: a page of three Destinations reads its warnings in one query; a person and an entry whose
// latest event is mention_not_delivered warn, since the first such event after their last mention_delivered and with
// the latest reason; a later mention_delivered clears the warning; an event of another Organization, of a deleted User
// or of an entry removed since warns of nothing, nor do events past retention.alert_details, which also clear a
// warning once the business clock passes them. A group or everyone that a bridge's line of Mentions left out warns the
// same way by its Mention, with no person, and a template's token, which names no entry, warns of nothing (#626).
func TestIntegrationMentionWarnings(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
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
		t0 := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
		if err := organization.Ensure(ctx, organization.NewStore(d.Pool), logger, t0); err != nil {
			t.Fatal(err)
		}
		org, err := organization.NewStore(d.Pool).GetOrganization(ctx)
		if err != nil {
			t.Fatal(err)
		}
		exec := func(sql string, args ...any) {
			t.Helper()
			if _, err := d.Pool.Exec(ctx, sql, args...); err != nil {
				t.Fatalf("%s: %v", sql, err)
			}
		}
		id := func(sql string, args ...any) int64 {
			t.Helper()
			var n int64
			if err := d.Pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
				t.Fatalf("%s: %v", sql, err)
			}
			return n
		}
		for _, m := range []string{"202608", "202609", "202610", "202611", "202612"} {
			from, _ := time.Parse("200601", m)
			exec(fmt.Sprintf(`CREATE TABLE delivery_events_p%s PARTITION OF delivery_events
				FOR VALUES FROM ('%s') TO ('%s')`, m, from.Format(time.RFC3339), from.AddDate(0, 1, 0).Format(time.RFC3339)))
		}
		other := id(`INSERT INTO organizations (public_id, name, time_zone, severity_label, severity_mapping,
			severity_styles, critical_is_urgent, instance_labels, retention_stored_snapshots_days,
			retention_alert_details_days, retention_alert_group_summaries_days, retention_audit_log_days, totp_required,
			oidc_token_grace_seconds, created_at, updated_at)
			VALUES ('RG0000000000BB', 'Other', 'UTC', 'severity', '[]', '[]', true, '{}', 14, 90, 730, 365, 'nobody',
			604800, $1, $1) RETURNING id`, t0)
		user := func(org int64, publicID, name, status string) int64 {
			return id(`INSERT INTO users (org_id, public_id, login, name, role, source, status, deleted_at, created_at,
				updated_at) VALUES ($1, $2, $3, $3, 'responder', 'local', $4,
				CASE WHEN $4 = 'deleted' THEN $5::timestamptz END, $5, $5) RETURNING id`, org, publicID, name, status, t0)
		}
		bob := user(org.ID, "SR0000000000BB", "bob", "active")
		carol := user(org.ID, "SR0000000000CC", "carol", "active")
		gone := user(org.ID, "SR0000000000GN", "gone", "deleted")
		connID := id(`INSERT INTO connections (org_id, public_id, type, name, mattermost_server_url,
			bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds,
			created_at, updated_at) VALUES ($1, 'CN0000000000MM', 'mattermost', 'mm', 'http://mm', '\x01', 'k1', $2, 1,
			1, $2, $2) RETURNING id`, org.ID, t0)
		dest := func(publicID string) int64 {
			return id(`INSERT INTO destinations (org_id, public_id, type, name, connection_id, mattermost_team_id,
				mattermost_channel_id, mentions, limiter_limit, limiter_per_seconds, health, created_at, updated_at)
				VALUES ($1, $2, 'mattermost', $2, $3, 'team', 'chan', '{}', 1, 1, 'healthy', $4, $4) RETURNING id`,
				org.ID, publicID, connID, t0)
		}
		d1, d2, d3 := dest("DS0000000000D1"), dest("DS0000000000D2"), dest("DS0000000000D3")
		ms := mentions.New(org.ID, d.Pool)
		onCall := mentions.EntryInput{Target: mentions.EntryTarget{Kind: mentions.TargetOnCallOfTeamsFromLabel},
			Events: mentions.Events}
		carolEntry := mentions.EntryInput{Target: mentions.EntryTarget{Kind: mentions.TargetUser,
			UserID: "SR0000000000CC"}, Events: mentions.Events}
		here := mentions.EntryInput{Target: mentions.EntryTarget{Kind: mentions.TargetEveryone,
			Everyone: mentions.EveryoneHere}, Events: mentions.Events}
		for dst, entries := range map[int64][]mentions.EntryInput{d1: {here, onCall}, d2: {carolEntry},
			d3: {onCall}} {
			if err := ms.WriteEntries(ctx, nil, dst, entries, t0); err != nil {
				t.Fatal(err)
			}
		}
		n := 0
		event := func(org, dst, user int64, kind delivery.EventKind, target mentions.EntryTarget, reason string,
			at time.Time) {
			t.Helper()
			n++
			detail, err := json.Marshal(delivery.MentionDetail{UserID: user, EntryID: 1, Position: 0, Target: target,
				Event: mentions.KindNewAlertGroup, Reason: reason})
			if err != nil {
				t.Fatal(err)
			}
			exec(`INSERT INTO delivery_events (org_id, public_id, occurred_at, destination_id, kind, loudness, detail)
				VALUES ($1, $2, $3, $4, $5, 'quiet', $6)`, org, fmt.Sprintf("DE%012d", n), at, dst, string(kind),
				detail)
		}
		nd, dl := delivery.EventMentionNotDelivered, delivery.EventMentionDelivered
		no, plain := mentions.ReasonNoAccountLink, mentions.ReasonBridgeCannotMentionPeople
		h := func(n int) time.Time { return t0.Add(-time.Duration(n) * time.Hour) }
		// d1: bob through the On-call entry twice; carol through a person entry d1 no longer has; gone, deleted;
		// carol through the On-call entry past retention; a row of another Organization with this Organization's bob,
		// d1 and the entry, newer and with another reason, which only the org_id filter of the query leaves out.
		event(org.ID, d1, bob, nd, onCall.Target, no, h(3))
		event(org.ID, d1, bob, nd, onCall.Target, plain, h(2))
		event(org.ID, d1, carol, nd, carolEntry.Target, no, h(2))
		event(org.ID, d1, gone, nd, onCall.Target, no, h(2))
		event(org.ID, d1, carol, nd, onCall.Target, no, t0.AddDate(0, 0, -91))
		event(other, d1, bob, nd, onCall.Target, no, h(1))
		// d2: carol, delivered between two undelivered.
		event(org.ID, d2, carol, nd, carolEntry.Target, no, h(5))
		event(org.ID, d2, carol, dl, carolEntry.Target, "", h(4))
		event(org.ID, d2, carol, nd, carolEntry.Target, plain, h(1))
		// d3: bob, delivered last.
		event(org.ID, d3, bob, nd, onCall.Target, no, h(2))
		event(org.ID, d3, bob, dl, onCall.Target, "", h(1))
		// #626: d1's here, which a bridge's line of Mentions left out for length, twice, keyed by its Mention with no
		// person; a template's token without an entry, which warns of nothing.
		named := func(dst int64, kind delivery.EventKind, target *mentions.EntryTarget, at time.Time) {
			t.Helper()
			n++
			detail := map[string]any{"mention": mentions.Mention{Kind: mentions.TargetEveryone, Name: "here"}}
			if kind == nd {
				detail["reason"] = mentions.ReasonMessageTooLong
			}
			if target != nil {
				detail["entry_id"], detail["position"], detail["target"], detail["event"] = 1, 0, *target,
					mentions.KindNewAlertGroup
			}
			b, err := json.Marshal(detail)
			if err != nil {
				t.Fatal(err)
			}
			exec(`INSERT INTO delivery_events (org_id, public_id, occurred_at, destination_id, kind, loudness, detail)
				VALUES ($1, $2, $3, $4, $5, 'quiet', $6)`, org.ID, fmt.Sprintf("DE%012d", n), at, dst, string(kind), b)
		}
		named(d1, nd, &here.Target, h(4))
		named(d1, nd, &here.Target, h(2))
		named(d3, nd, nil, h(1))

		store := &countingStore{Store: destinations.NewStore(d.Pool)}
		svc := destinations.New(org.ID, store)
		business := clock.NewManual(t0)
		svc.SetWriter(destinations.WriterConfig{Business: business, Mentions: ms})
		describe := func(ws []destinations.MentionWarning) string {
			var out []string
			for _, w := range ws {
				who := w.User.Login
				if w.Mention.Kind != "" {
					who = w.Mention.Kind + ":" + w.Mention.Name
				}
				out = append(out, fmt.Sprintf("%s/%d/%s/%s/%s/%s", who, w.Entry.Position, w.Entry.Target.Kind,
					w.Messenger, w.Reason, w.Since.UTC().Format("15:04")))
			}
			return strings.Join(out, ",")
		}
		page, err := svc.List(ctx, destinations.ListFilter{Limit: 10})
		if err != nil || len(page.Destinations) != 3 {
			t.Fatalf("list = %d, %v", len(page.Destinations), err)
		}
		want := map[string]string{
			"DS0000000000D1": "everyone:here/0/everyone/mattermost/message_too_long/06:00," +
				"bob/1/on_call_of_teams_from_label/mattermost/bridge_cannot_mention_people/07:00",
			"DS0000000000D2": "carol/0/user/mattermost/bridge_cannot_mention_people/09:00",
			"DS0000000000D3": "",
		}
		for _, dd := range page.Destinations {
			if got := describe(dd.MentionWarnings); got != want[dd.PublicID] {
				t.Errorf("%s: %q, want %q", dd.PublicID, got, want[dd.PublicID])
			}
		}
		if store.warningReads != 1 {
			t.Errorf("%d warning queries for a page", store.warningReads)
		}

		// A delivered Mention of bob through the entry clears his warning, and one of here clears its own.
		event(org.ID, d1, bob, dl, onCall.Target, "", t0)
		if got, err := svc.Get(ctx, "DS0000000000D1"); err != nil ||
			describe(got.MentionWarnings) != "everyone:here/0/everyone/mattermost/message_too_long/06:00" {
			t.Errorf("after bob's mention_delivered: %q, %v", describe(got.MentionWarnings), err)
		}
		named(d1, dl, &here.Target, t0)
		if got, err := svc.Get(ctx, "DS0000000000D1"); err != nil || len(got.MentionWarnings) != 0 {
			t.Errorf("after mention_delivered: %q, %v", describe(got.MentionWarnings), err)
		}
		// Once retention.alert_details passes carol's events, d2 warns no more.
		business.Set(h(1).AddDate(0, 0, 90).Add(time.Minute))
		if got, err := svc.Get(ctx, "DS0000000000D2"); err != nil || len(got.MentionWarnings) != 0 {
			t.Errorf("past retention: %q, %v", describe(got.MentionWarnings), err)
		}
		business.Set(h(1).AddDate(0, 0, 90).Add(-time.Minute))
		if got, err := svc.Get(ctx, "DS0000000000D2"); err != nil || describe(got.MentionWarnings) !=
			"carol/0/user/mattermost/bridge_cannot_mention_people/09:00" {
			t.Errorf("within retention: %q, %v", describe(got.MentionWarnings), err)
		}
	})
}

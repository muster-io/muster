// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package delivery_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/delivery"
	deliverydb "github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/organization"
)

// TestIntegrationListOpenMentions is C-28.FR-3 and C-11.FR-21 for the query that decides a mention_delivered, on
// PostgreSQL: of the people asked for, ListOpenMentions returns each person and entry target whose latest mention event
// of the Destination is mention_not_delivered — not one a later mention_delivered closed, nor one past
// retention.alert_details (@since), nor one of another Destination, of another Organization or of a person not asked
// for — whatever the order the rows were written in.
func TestIntegrationListOpenMentions(t *testing.T) {
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
		now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
		if err := organization.Ensure(ctx, organization.NewStore(d.Pool), logger, now); err != nil {
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
		for _, m := range []string{"202608", "202609", "202610", "202611"} {
			from, _ := time.Parse("200601", m)
			exec(fmt.Sprintf(`CREATE TABLE delivery_events_p%s PARTITION OF delivery_events FOR VALUES FROM ('%s')
				TO ('%s')`, m, from.Format(time.RFC3339), from.AddDate(0, 1, 0).Format(time.RFC3339)))
		}
		var other int64
		if err := d.Pool.QueryRow(ctx, `INSERT INTO organizations (public_id, name, time_zone, severity_label,
			severity_mapping, severity_styles, critical_is_urgent, instance_labels, retention_stored_snapshots_days,
			retention_alert_details_days, retention_alert_group_summaries_days, retention_audit_log_days,
			totp_required, oidc_token_grace_seconds, created_at, updated_at)
			VALUES ('RG0000000000BB', 'Other', 'UTC', 'severity', '[]', '[]', true, '{}', 14, 90, 730, 365, 'nobody',
			604800, $1, $1) RETURNING id`, now).Scan(&other); err != nil {
			t.Fatal(err)
		}
		onCall := mentions.EntryTarget{Kind: mentions.TargetOnCallOfTeamsFromLabel}
		person := mentions.EntryTarget{Kind: mentions.TargetUser, UserID: "SR0000000000BB"}
		const dest, otherDest = 41, 42
		n := 0
		event := func(org, destination, user int64, kind delivery.EventKind, target mentions.EntryTarget,
			at time.Time) {
			t.Helper()
			n++
			reason := ""
			if kind == delivery.EventMentionNotDelivered {
				reason = mentions.ReasonNoAccountLink
			}
			detail, err := json.Marshal(delivery.MentionDetail{UserID: user, EntryID: 9, Target: target,
				Event: mentions.KindNewAlertGroup, Reason: reason})
			if err != nil {
				t.Fatal(err)
			}
			exec(`INSERT INTO delivery_events (org_id, public_id, occurred_at, destination_id, kind, loudness, detail)
				VALUES ($1, $2, $3, $4, $5, 'quiet', $6)`, org, fmt.Sprintf("DE%012d", n), at, destination,
				string(kind), detail)
		}
		h := func(n int) time.Time { return now.Add(-time.Duration(n) * time.Hour) }
		nd, dl := delivery.EventMentionNotDelivered, delivery.EventMentionDelivered
		// Written out of time order on purpose: the latest by time decides.
		event(org.ID, dest, 2, nd, onCall, h(3))                        // open
		event(org.ID, dest, 2, dl, person, h(2))                        // closes the next one
		event(org.ID, dest, 2, nd, person, h(3))                        //
		event(org.ID, dest, 3, nd, onCall, h(1))                        // open again after a delivered one
		event(org.ID, dest, 3, dl, onCall, h(3))                        //
		event(org.ID, dest, 4, nd, onCall, now.AddDate(0, 0, -91))      // past retention
		event(other, dest, 5, nd, onCall, h(1))                         // another Organization
		event(org.ID, otherDest, 6, nd, onCall, h(1))                   // another Destination
		event(org.ID, dest, 7, nd, onCall, h(1))                        // not asked for
		event(org.ID, dest, 8, delivery.EventPublication, onCall, h(1)) // another kind
		ids := []string{"2", "3", "4", "5", "6", "8"}
		rows, err := deliverydb.New(d.Pool).ListOpenMentions(ctx, deliverydb.ListOpenMentionsParams{OrgID: org.ID,
			DestinationID: dest, Since: now.AddDate(0, 0, -90), UserIds: ids})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range rows {
			var target mentions.EntryTarget
			if err := json.Unmarshal(r.Target, &target); err != nil {
				t.Fatal(err)
			}
			got = append(got, strconv.FormatInt(r.UserID, 10)+"/"+target.Kind+"/"+target.UserID)
		}
		slices.Sort(got)
		if want := []string{"2/on_call_of_teams_from_label/", "3/on_call_of_teams_from_label/"}; !slices.Equal(got,
			want) {
			t.Errorf("open = %q, want %q", got, want)
		}
		// With the retention reaching back further, the old one is open too.
		rows, err = deliverydb.New(d.Pool).ListOpenMentions(ctx, deliverydb.ListOpenMentionsParams{OrgID: org.ID,
			DestinationID: dest, Since: now.AddDate(0, 0, -100), UserIds: []string{"4"}})
		if err != nil || len(rows) != 1 || rows[0].UserID != 4 {
			t.Errorf("a longer retention = %+v, %v", rows, err)
		}
	})
}

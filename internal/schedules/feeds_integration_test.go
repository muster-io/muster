// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package schedules_test

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/schedules"
	"github.com/muster-io/muster/internal/tokens"
)

// feedSpans are the DTSTART/DTEND pairs of the events of an iCal document whose summary is summary, read back with
// its folded lines joined.
func feedSpans(t *testing.T, doc []byte, summary string) []string {
	t.Helper()
	text := strings.ReplaceAll(string(doc), "\r\n ", "")
	var out []string
	for block := range strings.SplitSeq(text, "BEGIN:VEVENT\r\n") {
		props := map[string]string{}
		for line := range strings.SplitSeq(block, "\r\n") {
			if name, value, ok := strings.Cut(line, ":"); ok {
				props[name] = value
			}
		}
		if props["SUMMARY"] == summary {
			out = append(out, props["DTSTART"]+"/"+props["DTEND"])
		}
	}
	return out
}

// utcOf is a wall-clock time in Berlin as an iCal UTC time.
func utcOf(t *testing.T, s string) string {
	t.Helper()
	return berlin(t, s).UTC().Format("20060102T150405Z")
}

// TestIntegrationFeeds is C-27.FR-10 and C-27.AC-7 on PostgreSQL: the database holds only the 32-byte SHA-256 of a
// feed's token; issuing again replaces the row, so that the old token is not found and the new one is; with the two
// Rotation layers of scenario 1 petr's feed shows week 1 as its nights and weekend, an Override of petr as an event of
// its own span, and a Rotation layer limited to Friday 19:00–09:00 as one event from Friday 19:00 to Saturday 09:00; a
// Viewer's feed is empty; revoking makes the token unknown, and so does disabling the User, who is named.
func TestIntegrationFeeds(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		e := setup(t, s)
		w := audit.NewWriter(logging.New(&bytes.Buffer{}, logging.LevelInfo), e.clock)
		feeds := schedules.NewFeeds(schedules.FeedsConfig{OrgID: e.orgID, Store: schedules.NewFeedStore(e.d.Pool),
			Audit: w, Business: e.clock, Schedules: e.svc, PublicURL: "http://localhost:8080"})
		var petrID int64
		if err := e.d.Pool.QueryRow(ctx, `SELECT id FROM users WHERE public_id = $1`, e.people["petr"]).
			Scan(&petrID); err != nil {
			t.Fatal(err)
		}
		petr := schedules.FeedUser{ID: petrID, PublicID: e.people["petr"]}
		by := schedules.Requester{Actor: audit.User(petrID, petr.PublicID), Transport: audit.TransportUI}
		token := func(f schedules.IssuedFeed) string {
			t.Helper()
			v, ok := strings.CutPrefix(f.URL, "http://localhost:8080/ical/")
			if !ok || !strings.HasSuffix(v, ".ics") {
				t.Fatalf("url %q", f.URL)
			}
			return strings.TrimSuffix(v, ".ics")
		}

		first, err := feeds.Issue(ctx, by, petr)
		if err != nil {
			t.Fatal(err)
		}
		old := token(first)
		if n := e.count(t, `SELECT count(*) FROM ical_feeds WHERE user_id = $1 AND token_hash = sha256($2::bytea)
			AND octet_length(token_hash) = 32 AND position('mstr_cal_'::bytea in token_hash) = 0`, petrID,
			[]byte(old)); n != 1 || !tokens.WellFormed(tokens.PrefixIcal, old) {
			t.Errorf("%d rows hold the hash of %d characters", n, len(old))
		}
		second, err := feeds.Issue(ctx, by, petr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := feeds.Lookup(ctx, old); !errors.Is(err, schedules.ErrFeedNotFound) {
			t.Errorf("the old token: %v", err)
		}
		if u, err := feeds.Lookup(ctx, token(second)); err != nil || u != petr ||
			e.count(t, `SELECT count(*) FROM ical_feeds`) != 1 {
			t.Errorf("the new token: %+v, %v", u, err)
		}
		if f, err := feeds.Get(ctx, petrID); err != nil || f.IssuedAt == nil || !f.IssuedAt.Equal(t0) {
			t.Errorf("Get = %+v, %v", f, err)
		}
		if n := e.count(t, `SELECT count(*) FROM audit_log WHERE action = 'ical_feed.issued'
			AND resource_type = 'user' AND resource_public_id = $1 AND details::text NOT LIKE '%mstr_cal_%'`,
			petr.PublicID); n != 2 {
			t.Errorf("%d ical_feed.issued entries", n)
		}

		sc, err := e.svc.Create(ctx, admin, schedules.Input{TeamID: e.team.PublicID, Name: "Main",
			TimeZone: ptr("Europe/Berlin"), RotationLayers: e.layers()})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.Create(ctx, admin, schedules.Input{TeamID: e.team.PublicID, Name: "Nights",
			TimeZone: ptr("Europe/Berlin"), RotationLayers: []schedules.RotationLayerInput{{Name: "Friday night",
				UserIDs: []string{petr.PublicID}, ShiftLength: schedules.ShiftWeek, HandoverTime: "10:00",
				HandoverDay: "monday", StartDate: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
				Limits: &schedules.Limits{Days: []string{"friday"}, Start: "19:00", End: "09:00"}}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.CreateOverride(ctx, by, sc.PublicID, schedules.OverrideInput{UserID: petr.PublicID,
			Start: berlin(t, "2026-11-04 11:00"), End: berlin(t, "2026-11-04 19:00")}); err != nil {
			t.Fatal(err)
		}
		doc, err := feeds.Calendar(ctx, petr)
		if err != nil {
			t.Fatal(err)
		}
		var week1 []string
		for _, span := range feedSpans(t, doc, "Payments · Main") {
			if span >= utcOf(t, "2026-11-02 00:00") && span < utcOf(t, "2026-11-09 00:00") {
				week1 = append(week1, span)
			}
		}
		pair := func(a, b string) string { return utcOf(t, a) + "/" + utcOf(t, b) }
		want := []string{pair("2026-11-02 10:00", "2026-11-02 11:00"), pair("2026-11-02 19:00", "2026-11-03 11:00"),
			pair("2026-11-03 19:00", "2026-11-04 11:00"), pair("2026-11-04 11:00", "2026-11-04 19:00"),
			pair("2026-11-04 19:00", "2026-11-05 11:00"), pair("2026-11-05 19:00", "2026-11-06 11:00"),
			pair("2026-11-06 19:00", "2026-11-09 10:00")}
		if !slices.Equal(week1, want) {
			t.Errorf("week 1 = %v\nwant %v", week1, want)
		}
		if nights := feedSpans(t, doc, "Payments · Nights"); len(nights) == 0 ||
			nights[0] != pair("2026-11-06 19:00", "2026-11-07 09:00") {
			t.Errorf("the Friday nights = %v", nights)
		}

		var vicID int64
		if err := e.d.Pool.QueryRow(ctx, `SELECT id FROM users WHERE public_id = $1`, e.people["vic"]).
			Scan(&vicID); err != nil {
			t.Fatal(err)
		}
		if doc, err := feeds.Calendar(ctx, schedules.FeedUser{ID: vicID, PublicID: e.people["vic"]}); err != nil ||
			strings.Contains(string(doc), "BEGIN:VEVENT") {
			t.Errorf("a Viewer's feed = %s, %v", doc, err)
		}

		if _, err := e.d.Pool.Exec(ctx, `UPDATE users SET status = 'disabled' WHERE id = $1`, petrID); err != nil {
			t.Fatal(err)
		}
		if u, err := feeds.Lookup(ctx, token(second)); !errors.Is(err, schedules.ErrFeedNotFound) || u != petr {
			t.Errorf("a disabled User's token: %+v, %v", u, err)
		}
		if _, err := e.d.Pool.Exec(ctx, `UPDATE users SET status = 'active' WHERE id = $1`, petrID); err != nil {
			t.Fatal(err)
		}
		if err := feeds.Revoke(ctx, by, petr); err != nil {
			t.Fatal(err)
		}
		if _, err := feeds.Lookup(ctx, token(second)); !errors.Is(err, schedules.ErrFeedNotFound) {
			t.Errorf("a revoked token: %v", err)
		}
		if f, err := feeds.Get(ctx, petrID); err != nil || f.IssuedAt != nil ||
			e.count(t, `SELECT count(*) FROM audit_log WHERE action = 'ical_feed.revoked'`) != 1 {
			t.Errorf("after revoking: %+v, %v", f, err)
		}
	})
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package schedules

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/schedules/dbgen"
	"github.com/muster-io/muster/internal/tokens"
)

// fakeFeedStore is ical_feeds in memory over the Schedules of fakeStore, whose Audit log it shares.
type fakeFeedStore struct {
	*fakeStore
	feeds map[int64]fakeFeed
}

type fakeFeed struct {
	hash   []byte
	issued time.Time
}

func (s *fakeFeedStore) InTx(_ context.Context, f func(FeedQueries) error) error {
	feeds, audit := maps(s.feeds), len(s.audit)
	if err := f(s); err != nil {
		s.feeds, s.audit = feeds, s.audit[:audit]
		return err
	}
	return nil
}

func maps(m map[int64]fakeFeed) map[int64]fakeFeed {
	out := make(map[int64]fakeFeed, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (s *fakeFeedStore) On(dbgen.DBTX) FeedQueries { return s }

func (s *fakeFeedStore) ListUserSchedules(_ context.Context, arg dbgen.ListUserSchedulesParams) (
	[]dbgen.ListUserSchedulesRow, error) {
	if err := s.err("ListUserSchedules"); err != nil {
		return nil, err
	}
	var out []dbgen.ListUserSchedulesRow
	for _, sc := range s.schedules {
		if arg.OrgID != orgID || !slices.Contains(s.inTeam[sc.team], arg.UserID) {
			continue
		}
		t := s.team(sc.team)
		out = append(out, dbgen.ListUserSchedulesRow{ID: sc.id, PublicID: sc.publicID, Name: sc.name, TimeZone: sc.tz,
			Main: sc.main, Version: sc.version, CreatedAt: sc.created, TeamID: t.id, TeamPublicID: t.publicID,
			TeamName: t.name, TeamSlug: t.slug})
	}
	return out, nil
}

func (s *fakeFeedStore) ReplaceIcalFeed(_ context.Context, arg dbgen.ReplaceIcalFeedParams) (bool, error) {
	if err := s.err("ReplaceIcalFeed"); err != nil {
		return false, err
	}
	if s.user(arg.UserID).Status != "active" || arg.OrgID != orgID {
		return false, pgx.ErrNoRows
	}
	_, replaced := s.feeds[arg.UserID]
	s.feeds[arg.UserID] = fakeFeed{hash: arg.TokenHash, issued: arg.Now}
	return replaced, nil
}

func (s *fakeFeedStore) GetIcalFeed(_ context.Context, arg dbgen.GetIcalFeedParams) (time.Time, error) {
	if err := s.err("GetIcalFeed"); err != nil {
		return time.Time{}, err
	}
	f, ok := s.feeds[arg.UserID]
	if !ok || arg.OrgID != orgID {
		return time.Time{}, pgx.ErrNoRows
	}
	return f.issued, nil
}

func (s *fakeFeedStore) DeleteIcalFeed(_ context.Context, arg dbgen.DeleteIcalFeedParams) (int64, error) {
	if err := s.err("DeleteIcalFeed"); err != nil {
		return 0, err
	}
	if _, ok := s.feeds[arg.UserID]; !ok || arg.OrgID != orgID {
		return 0, nil
	}
	delete(s.feeds, arg.UserID)
	return 1, nil
}

func (s *fakeFeedStore) FindIcalFeed(_ context.Context, hash []byte) (dbgen.FindIcalFeedRow, error) {
	if err := s.err("FindIcalFeed"); err != nil {
		return dbgen.FindIcalFeedRow{}, err
	}
	for id, f := range s.feeds {
		if bytes.Equal(f.hash, hash) {
			u := s.user(id)
			org := int64(orgID)
			if id == bob.ID {
				org = orgID + 1 // bob's feed belongs to another Organization in these tests
			}
			return dbgen.FindIcalFeedRow{OrgID: org, UserID: id, UserPublicID: u.PublicID, UserStatus: u.Status}, nil
		}
	}
	return dbgen.FindIcalFeedRow{}, pgx.ErrNoRows
}

// newFeeds are the Feeds over the Schedules of newService.
func newFeeds(t *testing.T) (*Feeds, *Service, *fakeFeedStore, *clock.Manual) {
	t.Helper()
	svc, store, c := newService(t)
	fs := &fakeFeedStore{fakeStore: store, feeds: map[int64]fakeFeed{}}
	return NewFeeds(FeedsConfig{OrgID: orgID, Store: fs, Audit: audit.NewWriter(logging.New(nilWriter{},
		logging.LevelInfo), c), Business: c, Schedules: svc, PublicURL: "https://muster.example.org/"}), svc, fs, c
}

var feedOfPetr = FeedUser{ID: petr.ID, PublicID: petr.PublicID}

// tokenOf is the token in the link of an issued feed.
func tokenOf(t *testing.T, issued IssuedFeed) string {
	t.Helper()
	rest, ok := strings.CutPrefix(issued.URL, "https://muster.example.org/ical/")
	value, ok2 := strings.CutSuffix(rest, ".ics")
	if !ok || !ok2 {
		t.Fatalf("url = %q", issued.URL)
	}
	return value
}

// TestFeedLifecycle is C-27.FR-10 and C-27.AC-7: issuing gives, once, the link MUSTER_PUBLIC_URL/ical/{token}.ics
// whose token is mstr_cal_ and 32 random bytes, of which only the SHA-256 is stored; Get says when it was issued and
// never the token; issuing again replaces it, so that the old token is not found and the new one is; revoking makes
// the new one unknown too, and revoking without a feed does nothing. Each issue and revocation is an Audit log entry
// of the User without the token: ical_feed.issued, with whether it replaced a feed, and ical_feed.revoked.
func TestFeedLifecycle(t *testing.T) {
	ctx := t.Context()
	feeds, _, store, c := newFeeds(t)
	if f, err := feeds.Get(ctx, petr.ID); err != nil || f.IssuedAt != nil {
		t.Fatalf("before issuing: %+v, %v", f, err)
	}
	first, err := feeds.Issue(ctx, asPetr, feedOfPetr)
	if err != nil {
		t.Fatal(err)
	}
	old := tokenOf(t, first)
	if !strings.HasPrefix(old, tokens.PrefixIcal) || len(old) != len(tokens.PrefixIcal)+52 ||
		!tokens.WellFormed(tokens.PrefixIcal, old) || !first.IssuedAt.Equal(t0) {
		t.Errorf("issued %q at %v", old, first.IssuedAt)
	}
	stored := store.feeds[petr.ID]
	if !bytes.Equal(stored.hash, tokens.Hash(old)) || len(stored.hash) != 32 || bytes.Contains(stored.hash, []byte("mstr_cal_")) {
		t.Errorf("stored %x", stored.hash)
	}
	if f, err := feeds.Get(ctx, petr.ID); err != nil || f.IssuedAt == nil || !f.IssuedAt.Equal(t0) {
		t.Errorf("Get = %+v, %v", f, err)
	}
	if u, err := feeds.Lookup(ctx, old); err != nil || u != feedOfPetr {
		t.Errorf("Lookup = %+v, %v", u, err)
	}

	c.Advance(time.Hour)
	second, err := feeds.Issue(ctx, asPetr, feedOfPetr)
	if err != nil {
		t.Fatal(err)
	}
	fresh := tokenOf(t, second)
	if fresh == old || !second.IssuedAt.Equal(t0.Add(time.Hour)) || len(store.feeds) != 1 {
		t.Errorf("issued again %q at %v, %d feeds", fresh, second.IssuedAt, len(store.feeds))
	}
	if _, err := feeds.Lookup(ctx, old); !errors.Is(err, ErrFeedNotFound) {
		t.Errorf("the old token: %v", err)
	}
	if u, err := feeds.Lookup(ctx, fresh); err != nil || u != feedOfPetr {
		t.Errorf("the new token: %+v, %v", u, err)
	}
	if err := feeds.Revoke(ctx, asPetr, feedOfPetr); err != nil {
		t.Fatal(err)
	}
	if _, err := feeds.Lookup(ctx, fresh); !errors.Is(err, ErrFeedNotFound) {
		t.Errorf("a revoked token: %v", err)
	}
	if f, err := feeds.Get(ctx, petr.ID); err != nil || f.IssuedAt != nil {
		t.Errorf("after revoking: %+v, %v", f, err)
	}
	if err := feeds.Revoke(ctx, asPetr, feedOfPetr); err != nil {
		t.Fatal(err)
	}

	var actions []string
	for _, e := range store.audit {
		actions = append(actions, e.Action)
		var details map[string]any
		_ = json.Unmarshal(e.Details, &details)
		if e.ResourceType.String != audit.ResourceUser || e.ResourcePublicID.String != petr.PublicID ||
			e.ActorUserID.Int64 != petr.ID || bytes.Contains(e.Details, []byte(tokens.PrefixIcal)) ||
			bytes.Contains(e.Diff, []byte(tokens.PrefixIcal)) {
			t.Errorf("entry %+v", e)
		}
		switch e.Action {
		case ActionFeedIssued:
			if _, ok := details["replaced"].(bool); !ok {
				t.Errorf("issued without replaced: %s", e.Details)
			}
		case ActionFeedRevoked:
			if details["reason"] != "revoked" {
				t.Errorf("revoked: %s", e.Details)
			}
		}
	}
	if !slices.Equal(actions, []string{ActionFeedIssued, ActionFeedIssued, ActionFeedRevoked}) {
		t.Errorf("actions = %v", actions)
	}
	var replaced []string
	for _, e := range store.audit[:2] {
		replaced = append(replaced, string(e.Details))
	}
	if replaced[0] != `{"replaced":false}` || replaced[1] != `{"replaced":true}` {
		t.Errorf("replaced = %v", replaced)
	}
}

// TestFeedLookup is C-27.FR-10: a malformed token, one of another Organization, and the token of a disabled or
// deleted User are not found, the last two naming the User, and a failure of the lookup is an error.
func TestFeedLookup(t *testing.T) {
	ctx := t.Context()
	feeds, _, store, _ := newFeeds(t)
	for _, v := range []string{"", "mstr_cal_", "mstr_pat_" + strings.Repeat("a", 52), "mstr_cal_" + strings.Repeat("a", 51),
		"mstr_cal_" + strings.Repeat("1", 52), "garbage"} {
		if _, err := feeds.Lookup(ctx, v); !errors.Is(err, ErrFeedNotFound) {
			t.Errorf("Lookup(%q) = %v", v, err)
		}
	}
	if _, err := feeds.Lookup(ctx, "mstr_cal_"+strings.Repeat("a", 52)); !errors.Is(err, ErrFeedNotFound) {
		t.Errorf("an unknown token: %v", err)
	}
	// dan is disabled: no feed is issued for him, and the feed he had answers as not found, naming him.
	if _, err := feeds.Issue(ctx, admin, FeedUser{ID: dan.ID, PublicID: dan.PublicID}); !errors.Is(err,
		ErrFeedUserInactive) || len(store.feeds) != 0 || len(store.audit) != 0 {
		t.Errorf("issuing for a disabled user: %v, %d feeds, %d entries", err, len(store.feeds), len(store.audit))
	}
	value, hash, err := tokens.Generate(tokens.PrefixIcal)
	if err != nil {
		t.Fatal(err)
	}
	store.feeds[dan.ID] = fakeFeed{hash: hash, issued: t0}
	if u, err := feeds.Lookup(ctx, value); !errors.Is(err, ErrFeedNotFound) || u.PublicID != dan.PublicID {
		t.Errorf("the feed of a disabled user = %+v, %v", u, err)
	}
	bobs, err := feeds.Issue(ctx, admin, FeedUser{ID: bob.ID, PublicID: bob.PublicID})
	if err != nil {
		t.Fatal(err)
	}
	if u, err := feeds.Lookup(ctx, tokenOf(t, bobs)); !errors.Is(err, ErrFeedNotFound) || u.PublicID != "" {
		t.Errorf("the feed of another Organization = %+v, %v", u, err)
	}
	petrs, err := feeds.Issue(ctx, asPetr, feedOfPetr)
	if err != nil {
		t.Fatal(err)
	}
	for i := range store.users {
		if store.users[i].ID == petr.ID {
			store.users[i].Status = "deleted"
		}
	}
	if u, err := feeds.Lookup(ctx, tokenOf(t, petrs)); !errors.Is(err, ErrFeedNotFound) || u.PublicID != petr.PublicID {
		t.Errorf("the feed of a deleted user = %+v, %v", u, err)
	}
	store.fail["FindIcalFeed"] = errBoom
	if _, err := feeds.Lookup(ctx, tokenOf(t, petrs)); !errors.Is(err, errBoom) ||
		strings.Contains(err.Error(), tokens.PrefixIcal) {
		t.Errorf("a failed lookup: %v", err)
	}
}

// TestFeedFailures: a failure of the store is an error that leaves no feed and no Audit log entry behind.
func TestFeedFailures(t *testing.T) {
	ctx := t.Context()
	for _, name := range []string{"ReplaceIcalFeed", "InsertAuditEntry"} {
		feeds, _, store, _ := newFeeds(t)
		store.fail[name] = errBoom
		if _, err := feeds.Issue(ctx, asPetr, feedOfPetr); !errors.Is(err, errBoom) || len(store.feeds) != 0 ||
			len(store.audit) != 0 {
			t.Errorf("%s: %v, %d feeds, %d entries", name, err, len(store.feeds), len(store.audit))
		}
	}
	feeds, _, store, _ := newFeeds(t)
	if _, err := feeds.Issue(ctx, asPetr, feedOfPetr); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"DeleteIcalFeed", "InsertAuditEntry"} {
		store.fail = map[string]error{name: errBoom}
		if err := feeds.Revoke(ctx, asPetr, feedOfPetr); !errors.Is(err, errBoom) || len(store.feeds) != 1 {
			t.Errorf("%s: %v, %d feeds", name, err, len(store.feeds))
		}
	}
	store.fail = map[string]error{"GetIcalFeed": errBoom}
	if _, err := feeds.Get(ctx, petr.ID); !errors.Is(err, errBoom) {
		t.Errorf("Get: %v", err)
	}
	for _, name := range []string{"ListUserSchedules", "ListRotationLayers", "ListPeriodOverrides"} {
		feeds, svc, store, _ := newFeeds(t)
		if _, err := svc.Create(ctx, admin, mainInput()); err != nil {
			t.Fatal(err)
		}
		store.fail[name] = errBoom
		if _, err := feeds.Calendar(ctx, feedOfPetr); !errors.Is(err, errBoom) {
			t.Errorf("Calendar with %s failing: %v", name, err)
		}
	}
}

// TestRemoveUserFeed is C-27.FR-10: the deletion of a User deletes their feed in its transaction, recorded as
// ical_feed.revoked by the actor of the deletion with the reason user_deleted; a User without one records nothing.
func TestRemoveUserFeed(t *testing.T) {
	ctx := t.Context()
	feeds, _, store, _ := newFeeds(t)
	if _, err := feeds.Issue(ctx, asPetr, feedOfPetr); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		after, err := feeds.RemoveUserFeed(ctx, nil, petr.ID, petr.PublicID, admin.Actor, audit.TransportUI,
			admin.Address)
		if err != nil || after != nil {
			t.Fatalf("RemoveUserFeed = %v, %v", after != nil, err)
		}
	}
	if len(store.feeds) != 0 || len(store.audit) != 2 || store.audit[1].Action != ActionFeedRevoked ||
		string(store.audit[1].Details) != `{"reason":"user_deleted"}` || store.audit[1].ActorUserID.Int64 != ann.ID {
		t.Errorf("feeds %v, audit %+v", store.feeds, store.audit)
	}
}

// berlinTime is the instant of a wall-clock time in Europe/Berlin, as YYYYMMDDTHHMMSSZ in UTC.
func berlinTime(t *testing.T, y int, m time.Month, d, hh int) string {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	return time.Date(y, m, d, hh, 0, 0, 0, loc).UTC().Format(icalTime)
}

// feedEvents is the feed of petr read back, and the events in [from, to).
func feedEvents(t *testing.T, feeds *Feeds, from, to string) []event {
	t.Helper()
	doc, err := feeds.Calendar(t.Context(), feedOfPetr)
	if err != nil {
		t.Fatal(err)
	}
	events, _ := parseEvents(t, doc)
	var out []event
	for _, e := range events {
		if e["DTSTART"] >= from && e["DTSTART"] < to {
			out = append(out, e)
		}
	}
	return out
}

// TestFeedScenario1 is C-27.AC-7 with the two Rotation layers of scenario 1: in week 1, when petr has the lower
// Rotation layer and olga the upper one, petr's feed shows the week as its nights and its weekend only — Monday
// 10:00–11:00, the four nights from 19:00 to 11:00 and Friday 19:00 to Monday 10:00 — adjacent segments joined. An
// Override of petr inside olga's Wednesday is an event of its own span between his nights, not joined with them, and
// an Override of olga inside petr's Thursday night takes that time out of his feed. In week 2 petr has the upper
// Rotation layer: his five spans 11:00–19:00. Every event is named "{Team} · {Schedule}".
func TestFeedScenario1(t *testing.T) {
	ctx := t.Context()
	feeds, svc, _, _ := newFeeds(t)
	sc, err := svc.Create(ctx, admin, mainInput())
	if err != nil {
		t.Fatal(err)
	}
	at := func(d, hh int) string { return berlinTime(t, 2026, time.November, d, hh) }
	week1 := spans(feedEvents(t, feeds, at(2, 0), at(9, 0)))
	want := []string{at(2, 10) + "/" + at(2, 11), at(2, 19) + "/" + at(3, 11), at(3, 19) + "/" + at(4, 11),
		at(4, 19) + "/" + at(5, 11), at(5, 19) + "/" + at(6, 11), at(6, 19) + "/" + at(9, 10)}
	if !slices.Equal(week1, want) {
		t.Errorf("week 1 = %v\nwant %v", week1, want)
	}
	week2 := spans(feedEvents(t, feeds, at(9, 0), at(16, 0)))
	want2 := []string{at(9, 11) + "/" + at(9, 19), at(10, 11) + "/" + at(10, 19), at(11, 11) + "/" + at(11, 19),
		at(12, 11) + "/" + at(12, 19), at(13, 11) + "/" + at(13, 19)}
	if !slices.Equal(week2, want2) {
		t.Errorf("week 2 = %v\nwant %v", week2, want2)
	}

	berlin, _ := time.LoadLocation("Europe/Berlin")
	wall := func(d, hh int) time.Time { return time.Date(2026, time.November, d, hh, 0, 0, 0, berlin) }
	if _, err := svc.CreateOverride(ctx, admin, sc.PublicID, OverrideInput{UserID: petr.PublicID,
		ReplacesUserID: olga.PublicID, Start: wall(4, 11), End: wall(4, 19)}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateOverride(ctx, admin, sc.PublicID, OverrideInput{UserID: olga.PublicID,
		Start: wall(5, 19), End: wall(5, 23)}); err != nil {
		t.Fatal(err)
	}
	events := feedEvents(t, feeds, at(2, 0), at(9, 0))
	want = []string{at(2, 10) + "/" + at(2, 11), at(2, 19) + "/" + at(3, 11), at(3, 19) + "/" + at(4, 11),
		at(4, 11) + "/" + at(4, 19), at(4, 19) + "/" + at(5, 11), at(5, 23) + "/" + at(6, 11),
		at(6, 19) + "/" + at(9, 10)}
	if got := spans(events); !slices.Equal(got, want) {
		t.Errorf("week 1 with the Overrides = %v\nwant %v", got, want)
	}
	for _, e := range events {
		if e["SUMMARY"] != "Payments · Main" || e["UID"] != sc.PublicID+"-"+petr.PublicID+"-"+e["DTSTART"]+"@muster.example.org" ||
			e["DTSTAMP"] != t0.Format(icalTime) {
			t.Errorf("event %v", e)
		}
	}
}

// TestFeedFridayNight is C-27.FR-2 and FR-10: a Rotation layer limited to Friday 19:00–09:00 is one event from Friday
// 19:00 to Saturday 09:00 each week, and the feed reaches from ical.window back to its end ahead.
func TestFeedFridayNight(t *testing.T) {
	ctx := t.Context()
	feeds, svc, _, c := newFeeds(t)
	if _, err := svc.Create(ctx, admin, Input{TeamID: teamPay, Name: "Nights", TimeZone: ptr("Europe/Berlin"),
		RotationLayers: []RotationLayerInput{{Name: "Friday night", UserIDs: []string{petr.PublicID},
			ShiftLength: ShiftWeek, HandoverTime: "10:00", HandoverDay: "monday", StartDate: date("2026-10-05"),
			Limits: &Limits{Days: []string{"friday"}, Start: "19:00", End: "09:00"}}}}); err != nil {
		t.Fatal(err)
	}
	c.Set(time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC))
	events := feedEvents(t, feeds, "", "~")
	var got []string
	for _, e := range events {
		got = append(got, e["DTSTART"]+"/"+e["DTEND"])
	}
	// From 28 October 00:00 UTC, 7 days before the day of now, to 4 January 00:00 UTC, the end of the day 60 days
	// ahead: the Fridays from 30 October to 1 January.
	if len(got) != 10 || got[0] != berlinTime(t, 2026, time.October, 30, 19)+"/"+
		berlinTime(t, 2026, time.October, 31, 9) || got[1] != berlinTime(t, 2026, time.November, 6, 19)+"/"+
		berlinTime(t, 2026, time.November, 7, 9) || got[9] != berlinTime(t, 2027, time.January, 1, 19)+"/"+
		berlinTime(t, 2027, time.January, 2, 9) {
		t.Errorf("events = %v", got)
	}
	if got[1] != "20261106T180000Z/20261107T080000Z" {
		t.Errorf("Friday 6 November = %s", got[1])
	}
}

// TestFeedChangeOfClocks is C-27.FR-2 and FR-10 across the changes of the clocks in Europe/Berlin: the night from
// Saturday 19:00 to Sunday 09:00 is 15 hours on 24–25 October 2026, when the clocks go back, and 13 hours on 27–28
// March 2027, when they go forward; 02:30–04:00 on the Sunday of each change starts at the first occurrence of 02:30
// in October, 00:30 UTC, and at the instant the clocks move in March, 01:00 UTC, when 02:30 does not happen. Every
// time in the feed is UTC.
func TestFeedChangeOfClocks(t *testing.T) {
	ctx := t.Context()
	feeds, svc, _, c := newFeeds(t)
	c.Set(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))
	for name, lm := range map[string]Limits{
		"Nights": {Days: []string{"saturday"}, Start: "19:00", End: "09:00"},
		"Early":  {Days: []string{"sunday"}, Start: "02:30", End: "04:00"},
	} {
		if _, err := svc.Create(ctx, admin, Input{TeamID: teamPay, Name: name, TimeZone: ptr("Europe/Berlin"),
			RotationLayers: []RotationLayerInput{{Name: name, UserIDs: []string{petr.PublicID}, ShiftLength: ShiftDay,
				HandoverTime: "10:00", StartDate: date("2026-10-01"), Limits: &lm}}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, c2 := range []struct {
		now         time.Time
		from, to    string
		nights      string
		early       string
		nightsHours float64
	}{
		{time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC), "20261024T000000Z", "20261026T000000Z",
			"20261024T170000Z/20261025T080000Z", "20261025T003000Z/20261025T030000Z", 15},
		{time.Date(2027, 3, 20, 12, 0, 0, 0, time.UTC), "20270327T000000Z", "20270329T000000Z",
			"20270327T180000Z/20270328T070000Z", "20270328T010000Z/20270328T020000Z", 13},
	} {
		c.Set(c2.now)
		events := feedEvents(t, feeds, c2.from, c2.to)
		got := map[string]string{}
		for _, e := range events {
			got[strings.TrimPrefix(e["SUMMARY"], "Payments · ")] = e["DTSTART"] + "/" + e["DTEND"]
			if !strings.HasSuffix(e["DTSTART"], "Z") || !strings.HasSuffix(e["DTEND"], "Z") {
				t.Errorf("not UTC: %v", e)
			}
		}
		if len(events) != 2 || got["Nights"] != c2.nights || got["Early"] != c2.early {
			t.Errorf("%v: events %v, want Nights %s and Early %s", c2.now, got, c2.nights, c2.early)
		}
		start, _ := time.Parse(icalTime, strings.Split(got["Nights"], "/")[0])
		end, _ := time.Parse(icalTime, strings.Split(got["Nights"], "/")[1])
		if h := end.Sub(start).Hours(); h != c2.nightsHours {
			t.Errorf("%v: the night lasts %v hours", c2.now, h)
		}
	}
}

// TestEventsOf: the feed keeps only the segments of its User, joins adjacent segments of Rotation layers whatever the
// layer, keeps an Override's segments together and apart from the others, and leaves out gaps and other people.
func TestEventsOf(t *testing.T) {
	h := func(n int) time.Time { return t0.Add(time.Duration(n) * time.Hour) }
	p, o := petr, olga
	lower := &Source{Kind: SourceRotationLayer, RotationLayer: RotationLayerRef{PublicID: "RY000000000001"}}
	upper := &Source{Kind: SourceRotationLayer, RotationLayer: RotationLayerRef{PublicID: "RY000000000002"}}
	over := func(id string) *Source { return &Source{Kind: SourceOverride, Override: id} }
	res := Result{Segments: []Segment{
		{Start: h(0), End: h(1), User: &p, Source: lower},
		{Start: h(1), End: h(2), User: &p, Source: upper},
		{Start: h(2), End: h(3), User: &p, Source: over("VR000000000001")},
		{Start: h(3), End: h(4), User: &p, Source: over("VR000000000001")},
		{Start: h(4), End: h(5), User: &p, Source: over("VR000000000002")},
		{Start: h(5), End: h(6), User: &p, Source: lower},
		{Start: h(6), End: h(7), User: &o, Source: lower},
		{Start: h(7), End: h(8)},
		{Start: h(8), End: h(9), User: &p, Source: lower},
	}}
	sc := Schedule{PublicID: "SC0000000000AA", Name: "Main", Team: TeamRef{Name: "Payments"}}
	var got []string
	for _, e := range eventsOf(sc, res, petr.ID) {
		got = append(got, e.Start.Sub(t0).String()+"-"+e.End.Sub(t0).String())
		if e.Schedule != sc.PublicID || e.Team != "Payments" || e.Name != "Main" {
			t.Errorf("event %+v", e)
		}
	}
	want := []string{"0s-2h0m0s", "2h0m0s-4h0m0s", "4h0m0s-5h0m0s", "5h0m0s-6h0m0s", "8h0m0s-9h0m0s"}
	if !slices.Equal(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
}

// TestFeedWindow is ical.window (P-81): the feed reaches from midnight UTC 7 days before the business clock's day to
// midnight UTC at the end of the day 60 days ahead, so that an event it cuts at an end keeps its start and its UID
// through the day, and the feed fetched again later that day is the same but for DTSTAMP.
func TestFeedWindow(t *testing.T) {
	ctx := t.Context()
	feeds, svc, _, c := newFeeds(t)
	if _, err := svc.Create(ctx, admin, Input{TeamID: teamPay, Name: "Always", TimeZone: ptr("Europe/Berlin"),
		RotationLayers: []RotationLayerInput{{Name: "Petr", UserIDs: []string{petr.PublicID}, ShiftLength: ShiftDay,
			HandoverTime: "10:00", StartDate: date("2026-10-01")}}}); err != nil {
		t.Fatal(err)
	}
	read := func() (string, []event) {
		t.Helper()
		doc, err := feeds.Calendar(ctx, feedOfPetr)
		if err != nil {
			t.Fatal(err)
		}
		events, _ := parseEvents(t, doc)
		if len(events) == 0 {
			t.Fatalf("no events in %s", doc)
		}
		return strings.ReplaceAll(string(doc), events[0]["DTSTAMP"], "STAMP"), events
	}
	c.Set(time.Date(2026, 10, 28, 0, 0, 1, 0, time.UTC))
	morning, events := read()
	if len(events) != 1 || events[0]["DTSTART"] != "20261021T000000Z" || events[0]["DTEND"] != "20261228T000000Z" ||
		!strings.HasSuffix(events[0]["UID"], "-"+petr.PublicID+"-20261021T000000Z@muster.example.org") {
		t.Errorf("events = %v", events)
	}
	c.Set(time.Date(2026, 10, 28, 23, 59, 59, 0, time.UTC))
	if evening, _ := read(); evening != morning {
		t.Errorf("the feed changed within the day:\n%s\n%s", morning, evening)
	}
	c.Set(time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC))
	if _, next := read(); next[0]["DTSTART"] != "20261022T000000Z" || next[0]["DTEND"] != "20261229T000000Z" {
		t.Errorf("the next day = %v", next)
	}
}

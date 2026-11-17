// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package destinations

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/destinations/dbgen"
	"github.com/muster-io/muster/internal/mentions"
)

// TestMentionWarnings is C-28.FR-3 and AC-4 for the reads (plan review, L9): a page of Destinations reads its
// warnings in one query, for every Destination of it but the outgoing webhook, at the business clock's now; each
// warning names its entry as it is on the Destination now — its position there and its target — with the messenger,
// the reason and since when, by entry and then by name; a warning whose entry was removed since is dropped; a read of
// one Destination, a save and a Destination without warnings carry them too, empty when there are none; a failed
// query fails the read.
func TestMentionWarnings(t *testing.T) {
	s, store, _, _, _ := newSaver(t)
	fm := s.writer.Mentions.(*fakeMentions)
	onCall := mentions.EntryInput{Target: mentions.EntryTarget{Kind: mentions.TargetOnCallOfTeamsFromLabel},
		Events: mentions.Events}
	carolEntry := mentions.EntryInput{Target: mentions.EntryTarget{Kind: mentions.TargetUser,
		UserID: "SRAAAAAAAAAAC1"}, Events: mentions.Events}
	here := mentions.EntryInput{Target: mentions.EntryTarget{Kind: mentions.TargetEveryone,
		Everyone: mentions.EveryoneHere}, Events: mentions.Events}
	fm.entries[1] = []mentions.EntryInput{here, carolEntry, onCall}
	fm.entries[2] = []mentions.EntryInput{onCall}
	since := t0.Add(-2 * time.Hour)
	row := func(dest, user int64, name, target, reason string) dbgen.ListMentionWarningsRow {
		return dbgen.ListMentionWarningsRow{DestinationID: dest, UserID: user, UserPublicID: "SRAAAAAAAAAA" +
			strings.ToUpper(name[:1]) + "1", UserName: name, UserLogin: name, Target: []byte(target), Since: since, Reason: reason}
	}
	store.warnings = []dbgen.ListMentionWarningsRow{
		row(1, 3, "carol", `{"kind":"on_call_of_teams_from_label"}`, mentions.ReasonNoAccountLink),
		row(1, 2, "Bob", `{"kind":"on_call_of_teams_from_label"}`, mentions.ReasonNoAccountLink),
		row(1, 3, "carol", `{"kind":"user","user_id":"SRAAAAAAAAAAC1"}`, mentions.ReasonNoAccountLink),
		// An entry removed since.
		row(1, 4, "dave", `{"kind":"user","user_id":"SRAAAAAAAAAAD1"}`, mentions.ReasonNoAccountLink),
		row(2, 2, "Bob", `{"kind":"on_call_of_teams_from_label"}`, mentions.ReasonBridgeCannotMentionPeople),
	}
	page, err := s.List(t.Context(), ListFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.warningReads) != 1 || !slices.Equal(store.warningReads[0].DestinationIds, []int64{1, 2}) ||
		!store.warningReads[0].Now.Equal(t0) || store.warningReads[0].OrgID != 1 {
		t.Errorf("reads %+v, want one for the page", store.warningReads)
	}
	type got struct {
		user      string
		position  int
		target    string
		messenger string
		reason    string
	}
	describe := func(ws []MentionWarning) []got {
		out := []got{}
		for _, w := range ws {
			if !w.Since.Equal(since) {
				t.Errorf("since %v", w.Since)
			}
			out = append(out, got{w.User.Login, w.Entry.Position, w.Entry.Target.Kind, w.Messenger, w.Reason})
		}
		return out
	}
	want := map[string][]got{
		"DSAAAAAAAAAAA1": {{"carol", 1, mentions.TargetUser, TypeMattermost, mentions.ReasonNoAccountLink},
			{"Bob", 2, mentions.TargetOnCallOfTeamsFromLabel, TypeMattermost, mentions.ReasonNoAccountLink},
			{"carol", 2, mentions.TargetOnCallOfTeamsFromLabel, TypeMattermost, mentions.ReasonNoAccountLink}},
		"DSAAAAAAAAAAA2": {{"Bob", 0, mentions.TargetOnCallOfTeamsFromLabel, TypeTelegram,
			mentions.ReasonBridgeCannotMentionPeople}},
		"DSAAAAAAAAAAA3": {},
	}
	for _, d := range page.Destinations {
		if g := describe(d.MentionWarnings); !slices.Equal(g, want[d.PublicID]) {
			t.Errorf("%s: %+v, want %+v", d.PublicID, g, want[d.PublicID])
		}
	}
	if w := page.Destinations[0].MentionWarnings[0]; w.User.PublicID != "SRAAAAAAAAAAC1" || w.User.ID != 3 ||
		w.Entry.Target.UserID != "SRAAAAAAAAAAC1" {
		t.Errorf("warning %+v", w)
	}

	d, err := s.Get(t.Context(), "DSAAAAAAAAAAA2")
	if err != nil || len(d.MentionWarnings) != 1 || len(store.warningReads) != 2 ||
		!slices.Equal(store.warningReads[1].DestinationIds, []int64{2}) {
		t.Errorf("get = %+v, %v, reads %+v", d.MentionWarnings, err, store.warningReads)
	}
	if d, err := s.Get(t.Context(), "DSAAAAAAAAAAA3"); err != nil || d.MentionWarnings == nil ||
		len(store.warningReads) != 2 {
		t.Errorf("an outgoing webhook = %+v, %v, %d reads", d.MentionWarnings, err, len(store.warningReads))
	}
	store.warnings = nil
	if d, err := s.Get(t.Context(), "DSAAAAAAAAAAA1"); err != nil || d.MentionWarnings == nil ||
		len(d.MentionWarnings) != 0 {
		t.Errorf("without warnings = %+v, %v", d.MentionWarnings, err)
	}

	store.warnings = []dbgen.ListMentionWarningsRow{row(1, 2, "Bob", `{"kind":`, mentions.ReasonNoAccountLink)}
	if _, err := s.Get(t.Context(), "DSAAAAAAAAAAA1"); err == nil {
		t.Error("a target that is not JSON was read")
	}
	store.fail["ListMentionWarnings"] = errBoom
	if _, err := s.List(t.Context(), ListFilter{Limit: 10}); !errors.Is(err, errBoom) {
		t.Errorf("a failed query = %v", err)
	}

	// Without a business clock, as a reader without the changes has, no warning is read.
	s.writer.Business = nil
	delete(store.fail, "ListMentionWarnings")
	if d, err := s.Get(t.Context(), "DSAAAAAAAAAAA1"); err != nil || d.MentionWarnings == nil ||
		len(store.warningReads) != 5 {
		t.Errorf("without a clock = %+v, %v, %d reads", d.MentionWarnings, err, len(store.warningReads))
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package destinations

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/destinations/dbgen"
	"github.com/muster-io/muster/internal/managedby"
)

// markOnlyKind is a type of Destination with a Destination check for TestMarkOnlySave: the row of a Broken
// Destination of it whose check fails, the input that saves it as stored, with the name given, and the count of its
// checks.
type markOnlyKind struct {
	typ    string
	row    func(store *fakeStore) *dbgen.GetDestinationRow
	input  func(name string) Input
	checks func(s *Service) int
}

func markOnlyKinds() []markOnlyKind {
	since := pgtype.Timestamptz{Time: t0.Add(-time.Hour), Valid: true}
	return []markOnlyKind{
		{TypeMattermost, func(store *fakeStore) *dbgen.GetDestinationRow {
			r := &store.rows[0]
			r.MattermostChannelID = txt("ch-nobot")
			r.Health, r.BrokenSince, r.BrokenReason = healthBroken, since, txt("not a member")
			return r
		}, func(name string) Input {
			return Input{Type: TypeMattermost, Name: name, Limiter: Limiter{Limit: 30, PerSeconds: 60},
				Mattermost: &MattermostInput{Connection: "cnaaaaaaaaaaa1", TeamID: "team", ChannelID: "ch-nobot"}}
		}, func(s *Service) int { return len(s.writer.Mattermost.(*fakeChecker).calls) }},
		{TypeTelegram, func(store *fakeStore) *dbgen.GetDestinationRow {
			r := &store.rows[1]
			r.TelegramChannelID = txt("@revoked")
			return r
		}, func(name string) Input {
			return Input{Type: TypeTelegram, Name: name, Limiter: Limiter{Limit: 10, PerSeconds: 60},
				Telegram: &TelegramInput{Connection: "cnaaaaaaaaaaa2", ChannelID: "@revoked"}}
		}, func(s *Service) int { return len(s.writer.Telegram.(*fakeTelegram).calls) }},
		{TypeBridge, func(store *fakeStore) *dbgen.GetDestinationRow {
			store.rows = append(store.rows, bridgeRow())
			r := &store.rows[len(store.rows)-1]
			r.BridgeChannelID = txt("revoked")
			return r
		}, func(name string) Input {
			return Input{Type: TypeBridge, Name: name, Limiter: Limiter{Limit: 30, PerSeconds: 60},
				Bridge: &BridgeInput{Connection: "cnaaaaaaaaaaa3", ChannelID: "revoked"}}
		}, func(s *Service) int { return len(s.writer.Bridge.(*fakeBridge).calls) }},
		{TypeSlack, func(store *fakeStore) *dbgen.GetDestinationRow {
			store.rows = append(store.rows, slackRow())
			r := &store.rows[len(store.rows)-1]
			r.SlackChannelID = txt("CREVOKED")
			return r
		}, func(name string) Input {
			return Input{Type: TypeSlack, Name: name, Limiter: Limiter{Limit: 30, PerSeconds: 60},
				Slack: &SlackInput{Connection: "cnaaaaaaaaaaa8", ChannelID: "CREVOKED"}}
		}, func(s *Service) int { return len(s.writer.Slack.(*fakeSlack).calls) }},
	}
}

// configWrites counts the writes of the configured fields of a Mattermost, Telegram, Bridge or Slack Destination.
func configWrites(w *saveWriter) int {
	return len(w.updated) + len(w.tgSets) + len(w.brSets) + len(w.slSets)
}

// TestMarkOnlySave is C-33.FR-11 on the Destinations that have a Destination check: a save of a Mattermost, Telegram,
// Bridge or Slack Destination that changes its mark managed_by and nothing else runs no Destination check, so it is
// saved while the check fails; it writes the mark alone, moves the version, records /managed_by alone and requests no
// re-render and no write of the Mention entries, the owning Team or what a check found; a Broken Destination stays
// Broken, since nothing was checked. The same save with another field changed runs the check and is refused while it
// fails, and a save of the mark alone over a stale version is refused.
func TestMarkOnlySave(t *testing.T) {
	for _, k := range markOnlyKinds() {
		t.Run(k.typ, func(t *testing.T) {
			s, store, w, _, healed := newSaver(t)
			ctx := t.Context()
			row := k.row(store)
			w.versions[row.PublicID] = row.Version
			mentionWrites := len(s.writer.Mentions.(*fakeMentions).writes)
			checks, writes, entries, reRenders := k.checks(s), configWrites(w), len(w.audit), len(w.updates)
			seen, hints := len(w.tgSeen), len(w.hints)

			in := k.input(row.Name)
			in.ManagedBy = managedby.Set("terraform")
			d, err := s.Update(ctx, saver, row.PublicID, new(row.Version), in)
			if err != nil {
				t.Fatalf("a save of the mark alone while the check fails = %v", err)
			}
			if d.ManagedBy == nil || *d.ManagedBy != "terraform" || d.Version != row.Version+1 {
				t.Errorf("the saved Destination: mark %v, version %d, want terraform and %d", d.ManagedBy, d.Version,
					row.Version+1)
			}
			if got := k.checks(s); got != checks {
				t.Errorf("a save of the mark alone ran the Destination check %d times", got-checks)
			}
			if got := configWrites(w); got != writes {
				t.Errorf("a save of the mark alone wrote the configured fields %d times", got-writes)
			}
			assertMarkWritten(t, w, row.ID, "terraform")
			if len(w.audit) != entries+1 {
				t.Fatalf("%d Audit log entries, want 1", len(w.audit)-entries)
			}
			var diff []audit.Change
			if err := json.Unmarshal(w.audit[len(w.audit)-1].Diff, &diff); err != nil {
				t.Fatal(err)
			}
			if len(diff) != 1 || diff[0].Pointer != managedby.Pointer || diff[0].Before != nil ||
				diff[0].After != "terraform" || w.audit[len(w.audit)-1].Action != ActionUpdated {
				t.Errorf("the Audit log of the mark alone: %s %s", w.audit[len(w.audit)-1].Action,
					w.audit[len(w.audit)-1].Diff)
			}
			if len(w.updates) != reRenders || len(w.owners) != 0 || len(w.tgSeen) != seen ||
				len(s.writer.Mentions.(*fakeMentions).writes) != mentionWrites {
				t.Errorf("a save of the mark alone requested %d re-renders, wrote %d owners, %d records of the "+
					"discussion group and %d Mention entries", len(w.updates)-reRenders, len(w.owners),
					len(w.tgSeen)-seen, len(s.writer.Mentions.(*fakeMentions).writes)-mentionWrites)
			}
			if len(*healed) != 0 || d.Health.State != healthBroken {
				t.Errorf("a save of the mark alone ended the Broken state: healed %v, health %s", *healed,
					d.Health.State)
			}
			if len(w.hints) != hints+1 || w.hints[len(w.hints)-1].ID != row.PublicID {
				t.Errorf("the hints of the mark alone: %+v", w.hints[hints:])
			}

			// The mark together with a new name runs the check, which fails, and writes nothing.
			entries = len(w.audit)
			in = k.input(row.Name + "-2")
			in.ManagedBy = managedby.Set("opentofu")
			if _, err := s.Update(ctx, saver, row.PublicID, nil, in); !errors.As(err, new(*CheckFailedError)) {
				t.Errorf("the mark and a new name while the check fails = %v, want a *CheckFailedError", err)
			}
			if got := k.checks(s); got != checks+1 {
				t.Errorf("the mark and a new name ran the Destination check %d times, want once", got-checks)
			}
			if len(w.audit) != entries || configWrites(w) != writes {
				t.Error("a save refused by its check wrote")
			}

			// A save of the mark alone over a version that changed since it was read is refused.
			w.versions[row.PublicID] = row.Version + 1
			in = k.input(row.Name)
			in.ManagedBy = managedby.Set("opentofu")
			if _, err := s.Update(ctx, saver, row.PublicID, nil, in); !errors.Is(err, ErrVersionMismatch) {
				t.Errorf("the mark alone over a changed version = %v, want ErrVersionMismatch", err)
			}
			if _, err := s.Update(ctx, saver, row.PublicID, new(row.Version-1), in); !errors.Is(err,
				ErrVersionMismatch) {
				t.Errorf("the mark alone with a stale If-Match = %v, want ErrVersionMismatch", err)
			}
			if len(w.audit) != entries || k.checks(s) != checks+1 {
				t.Error("a refused save of the mark alone wrote or checked")
			}
		})
	}
}

// assertMarkWritten checks that w wrote the mark alone once, of the Destination id, at the business time of the save.
func assertMarkWritten(t *testing.T, w *saveWriter, id int64, mark string) {
	t.Helper()
	if len(w.marks) != 1 || w.marks[0].ID != id || w.marks[0].OrgID != 1 || w.marks[0].ManagedBy.String != mark ||
		!w.marks[0].Now.Equal(t0) {
		t.Errorf("the writes of the mark alone: %+v, want one of %s on %d", w.marks, mark, id)
	}
}

// TestMarkOnlySaveFailures: a save of the mark alone of a Destination that is gone since it was read is ErrNotFound, and
// one whose lock, write or Audit log entry fails is that error; none of them runs the Destination check.
func TestMarkOnlySaveFailures(t *testing.T) {
	k := markOnlyKinds()[0]
	for _, tc := range []struct {
		name string
		set  func(w *saveWriter, id string)
		want func(err error) bool
	}{
		{"gone", func(w *saveWriter, id string) { delete(w.versions, id) },
			func(err error) bool { return errors.Is(err, ErrNotFound) }},
		{"lock", func(w *saveWriter, _ string) { w.fail["LockDestination"] = errBoom },
			func(err error) bool { return errors.Is(err, errBoom) }},
		{"write", func(w *saveWriter, _ string) { w.fail["SetDestinationMark"] = errBoom },
			func(err error) bool { return errors.Is(err, errBoom) }},
		{"audit", func(w *saveWriter, _ string) { w.auditErr = errBoom },
			func(err error) bool { return errors.Is(err, errBoom) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, store, w, _, _ := newSaver(t)
			row := k.row(store)
			w.versions[row.PublicID] = row.Version
			tc.set(w, row.PublicID)
			in := k.input(row.Name)
			in.ManagedBy = managedby.Set("terraform")
			if _, err := s.Update(t.Context(), saver, row.PublicID, nil, in); !tc.want(err) {
				t.Errorf("%s: %v", tc.name, err)
			}
			if k.checks(s) != 0 {
				t.Errorf("%s: the Destination check ran", tc.name)
			}
		})
	}
}

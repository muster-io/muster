// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package leader

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/leader/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
)

// fakeStore is runtime_state, downtime_periods, the replica records and the learned repeat intervals in memory. A
// transaction holds the store's lock, as the row lock of LockRuntimeState does.
type fakeStore struct {
	tx       sync.Mutex
	state    *dbgen.GetRuntimeStateRow
	downtime []dbgen.RecordDowntimeParams
	// replicas are the replica records, on the real clock.
	replicas map[string]replicaRecord
	// learned are the longest learned repeat intervals per Organization, in milliseconds.
	learned map[int64]int64
	// fail fails the query of that name.
	fail string
}

func (s *fakeStore) err(name string) error {
	if s.fail == name {
		return errors.New(name + " failed")
	}
	return nil
}

func (*fakeStore) DBTX() dbgen.DBTX { return nil }

func (s *fakeStore) InTx(_ context.Context, f func(Queries) error) error {
	s.tx.Lock()
	defer s.tx.Unlock()
	saved, savedDowntime := s.state, slices.Clone(s.downtime)
	if s.state != nil {
		st := *s.state
		s.state = &st
	}
	if err := f(s); err != nil {
		s.state, s.downtime = saved, savedDowntime // rolled back
		return err
	}
	return nil
}

func (s *fakeStore) EnsureRuntimeState(context.Context, time.Time) error {
	if s.state == nil {
		s.state = &dbgen.GetRuntimeStateRow{}
	}
	return s.err("EnsureRuntimeState")
}

func (s *fakeStore) LockRuntimeState(context.Context) (dbgen.LockRuntimeStateRow, error) {
	if s.state == nil {
		return dbgen.LockRuntimeStateRow{}, pgx.ErrNoRows
	}
	return dbgen.LockRuntimeStateRow(*s.state), s.err("LockRuntimeState")
}

func (s *fakeStore) GetRuntimeState(context.Context) (dbgen.GetRuntimeStateRow, error) {
	if err := s.err("GetRuntimeState"); err != nil {
		return dbgen.GetRuntimeStateRow{}, err
	}
	if s.state == nil {
		return dbgen.GetRuntimeStateRow{}, pgx.ErrNoRows
	}
	return *s.state, nil
}

type replicaRecord struct {
	started, refreshed time.Time
}

func (s *fakeStore) LatestOtherReplicaRefresh(_ context.Context, arg dbgen.LatestOtherReplicaRefreshParams,
) (time.Time, error) {
	if err := s.err("LatestOtherReplicaRefresh"); err != nil {
		return time.Time{}, err
	}
	var latest time.Time
	for other, r := range s.replicas {
		if other != arg.ReplicaID && !r.started.After(arg.StartedBefore) && r.refreshed.After(latest) {
			latest = r.refreshed
		}
	}
	if latest.IsZero() {
		return time.Time{}, pgx.ErrNoRows
	}
	return latest, nil
}

func (s *fakeStore) RecordDowntime(_ context.Context, arg dbgen.RecordDowntimeParams) error {
	s.downtime = append(s.downtime, arg)
	return s.err("RecordDowntime")
}

func (s *fakeStore) LatestDowntimeEnd(context.Context) (time.Time, error) {
	if err := s.err("LatestDowntimeEnd"); err != nil {
		return time.Time{}, err
	}
	if len(s.downtime) == 0 {
		return time.Time{}, pgx.ErrNoRows
	}
	return s.downtime[len(s.downtime)-1].EndedAt, nil
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func (s *fakeStore) TakeOver(_ context.Context, arg dbgen.TakeOverParams) error {
	s.state.LeaderReplicaID = pgtype.Text{String: arg.LeaderReplicaID, Valid: true}
	s.state.LeaderSince = ts(arg.Now)
	if !s.state.AliveAt.Valid || arg.Now.After(s.state.AliveAt.Time) {
		s.state.AliveAt = ts(arg.Now)
	}
	if arg.RecoveryUntil.Valid {
		s.state.RecoveryUntil = arg.RecoveryUntil
	}
	return s.err("TakeOver")
}

func (s *fakeStore) MarkAlive(_ context.Context, arg dbgen.MarkAliveParams) (int64, error) {
	if err := s.err("MarkAlive"); err != nil {
		return 0, err
	}
	if s.state == nil {
		return 0, nil
	}
	if s.state.LeaderReplicaID.String != arg.LeaderReplicaID {
		s.state.LeaderSince = ts(arg.Now)
	}
	s.state.LeaderReplicaID = pgtype.Text{String: arg.LeaderReplicaID, Valid: true}
	if arg.Now.After(s.state.AliveAt.Time) {
		s.state.AliveAt = ts(arg.Now)
	}
	return 1, nil
}

func (s *fakeStore) ListOrganizationIDs(context.Context) ([]int64, error) {
	ids := []int64{1}
	for id := range s.learned {
		if id != 1 {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids, s.err("ListOrganizationIDs")
}

func (s *fakeStore) LongestLearnedRepeatInterval(_ context.Context, org int64) (int64, error) {
	return s.learned[org], s.err("LongestLearnedRepeatInterval")
}

var start = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// world is one installation: the store and the clocks of its replicas.
type world struct {
	store *fakeStore
	real  *clock.Manual
	// business runs ahead of real by offset, as the development clock does.
	business *clock.Manual
	log      *bytes.Buffer
}

func newWorld(offset time.Duration) *world {
	return &world{store: &fakeStore{replicas: map[string]replicaRecord{}, learned: map[int64]int64{}},
		real: clock.NewManual(start), business: clock.NewManual(start.Add(offset)), log: &bytes.Buffer{}}
}

func (w *world) advance(d time.Duration) {
	w.real.Advance(d)
	w.business.Advance(d)
}

func (w *world) alive(id string) *Alive {
	return NewAlive(w.store, clock.Clocks{Business: w.business, Real: w.real}, logging.New(w.log, logging.LevelInfo), id)
}

func (w *world) notices(t *testing.T) []organization.Notice {
	t.Helper()
	n, err := Notices(t.Context(), w.store, w.business.Now())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func kinds(notices []organization.Notice) []organization.NoticeKind {
	var out []organization.NoticeKind
	for _, n := range notices {
		out = append(out, n.Kind)
	}
	return out
}

func TestFirstStartAndNormalHandover(t *testing.T) {
	w := newWorld(0)
	if got := kinds(w.notices(t)); !slices.Equal(got, []organization.NoticeKind{organization.NoticeNoReplicaLeading}) {
		t.Errorf("notices before any Leader: %v", got)
	}
	a := w.alive("a")
	if err := a.TakeOver(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(w.store.downtime) != 0 || w.store.state.LeaderReplicaID.String != "a" || !w.store.state.AliveAt.Time.Equal(start) {
		t.Fatalf("first start: downtime %v, state %+v", w.store.downtime, w.store.state)
	}
	for range 4 {
		w.advance(AliveMarkInterval)
		if err := a.Mark(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if got := w.store.state.AliveAt.Time; !got.Equal(start.Add(4 * AliveMarkInterval)) {
		t.Errorf("alive mark %v", got)
	}
	if got := w.notices(t); len(got) != 0 {
		t.Errorf("notices while a Leader marks: %v", got)
	}

	// a stops; b takes over within leader.absence_notice: a handover, not downtime.
	w.advance(AbsenceNotice)
	b := w.alive("b")
	if err := b.TakeOver(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(w.store.downtime) != 0 || w.store.state.LeaderReplicaID.String != "b" ||
		!w.store.state.LeaderSince.Time.Equal(w.business.Now()) {
		t.Fatalf("handover: downtime %v, state %+v", w.store.downtime, w.store.state)
	}
	if w.log.Len() != 0 {
		t.Errorf("log: %s", w.log.String())
	}
}

func TestTenMinuteOutage(t *testing.T) {
	w := newWorld(0)
	if err := w.alive("a").TakeOver(t.Context()); err != nil {
		t.Fatal(err)
	}
	lastMark := w.business.Now()
	w.advance(10 * time.Minute)

	// The no-Leader notice is active while no replica runs.
	if got := w.notices(t); len(got) != 1 || got[0].Kind != organization.NoticeNoReplicaLeading ||
		!got[0].Since.Equal(lastMark) {
		t.Errorf("notices during the outage: %+v", got)
	}

	// The takeover records the downtime on the Alert Groups of every Organization in its transaction.
	var recorded []Downtime
	b := w.alive("b").OnDowntime(func(_ context.Context, _ dbgen.DBTX, org int64, d Downtime) error {
		if org != 1 {
			t.Errorf("downtime recorded in organization %d", org)
		}
		recorded = append(recorded, d)
		return nil
	})
	if err := b.TakeOver(t.Context()); err != nil {
		t.Fatal(err)
	}
	now := w.business.Now()
	want := []dbgen.RecordDowntimeParams{{StartedAt: lastMark, EndedAt: now, RecordedAt: now}}
	if !slices.Equal(w.store.downtime, want) {
		t.Fatalf("downtime %+v, want %+v", w.store.downtime, want)
	}
	if len(recorded) != 1 || !recorded[0].Start.Equal(lastMark) || !recorded[0].End.Equal(now) {
		t.Errorf("recorded on the alert groups: %+v", recorded)
	}
	if got := w.store.state.RecoveryUntil.Time; !got.Equal(now.Add(RecoveryUnlearned)) {
		t.Errorf("recovery until %v, want %v", got, now.Add(RecoveryUnlearned))
	}
	if !strings.Contains(w.log.String(), `"level":"WARN","event":"downtime_recorded","started_at":"2026-10-05T12:00:00Z",`+
		`"ended_at":"2026-10-05T12:10:00Z","duration_seconds":600}`) {
		t.Errorf("log: %s", w.log.String())
	}

	// The recovery notice is active until recovery_until and no longer.
	notices := w.notices(t)
	if len(notices) != 1 || notices[0] != (organization.Notice{Kind: organization.NoticeRecoveringAfterDowntime,
		Audience: organization.AudienceAll, Since: now, Until: now.Add(RecoveryUnlearned)}) {
		t.Errorf("notices after the outage: %+v", notices)
	}
	for range int(RecoveryUnlearned / AliveMarkInterval) {
		w.advance(AliveMarkInterval)
		if err := b.Mark(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if got := w.notices(t); len(got) != 0 {
		t.Errorf("notices after the recovery window: %+v", got)
	}

	// An overlapping Leader that takes over too records nothing more.
	if err := w.alive("c").TakeOver(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(w.store.downtime) != 1 {
		t.Errorf("downtime %+v", w.store.downtime)
	}
}

func TestOverlappingTakeovers(t *testing.T) {
	w := newWorld(0)
	if err := w.alive("a").TakeOver(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.advance(time.Hour)
	var wg sync.WaitGroup
	for _, id := range []string{"b", "c", "d"} {
		wg.Go(func() {
			if err := w.alive(id).TakeOver(t.Context()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if len(w.store.downtime) != 1 {
		t.Errorf("three overlapping Leaders recorded %d downtime periods", len(w.store.downtime))
	}
}

func TestLiveReplicasAreNotDowntime(t *testing.T) {
	w := newWorld(5 * time.Minute) // the development clock runs 5 minutes ahead
	if err := w.alive("a").TakeOver(t.Context()); err != nil {
		t.Fatal(err)
	}
	lastMark := w.business.Now()

	// No replica leads for 10 minutes, but replica r, which started a minute after the last mark, keeps running:
	// ingestion and delivery never stopped.
	rStarted := w.real.Now().Add(time.Minute)
	w.advance(10 * time.Minute)
	w.store.replicas["r"] = replicaRecord{started: rStarted, refreshed: w.real.Now().Add(-30 * time.Second)}
	// The new Leader's own record does not count.
	w.store.replicas["b"] = replicaRecord{started: start, refreshed: w.real.Now()}
	b := w.alive("b")
	if err := b.TakeOver(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(w.store.downtime) != 0 {
		t.Fatalf("downtime with a live replica: %+v", w.store.downtime)
	}

	// r stopped six minutes ago: the downtime starts then, in business time.
	w.advance(10 * time.Minute)
	w.store.replicas["r"] = replicaRecord{started: rStarted, refreshed: w.real.Now().Add(-6 * time.Minute)}
	w.store.state.AliveAt = ts(lastMark)
	if err := b.TakeOver(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(w.store.downtime) != 1 || !w.store.downtime[0].StartedAt.Equal(w.business.Now().Add(-6*time.Minute)) {
		t.Fatalf("downtime %+v, want it to start at %v", w.store.downtime, w.business.Now().Add(-6*time.Minute))
	}
}

// TestReplicasStartingAfterAnOutageDoNotHideIt: replicas that start together after a full outage refresh their records
// before the new Leader takes over; they did not run across the gap, so the gap is downtime.
func TestReplicasStartingAfterAnOutageDoNotHideIt(t *testing.T) {
	w := newWorld(0)
	if err := w.alive("a").TakeOver(t.Context()); err != nil {
		t.Fatal(err)
	}
	lastMark := w.business.Now()
	w.advance(10 * time.Minute)
	w.store.replicas["c"] = replicaRecord{started: w.real.Now().Add(-time.Second), refreshed: w.real.Now()}
	if err := w.alive("b").TakeOver(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(w.store.downtime) != 1 || !w.store.downtime[0].StartedAt.Equal(lastMark) {
		t.Fatalf("downtime %+v, want it to start at the last mark %v", w.store.downtime, lastMark)
	}
}

func TestRecoveryWindowFollowsLearnedRepeatIntervals(t *testing.T) {
	for _, tt := range []struct {
		name    string
		learned map[int64]int64
		want    time.Duration
	}{
		{name: "nothing learned", want: RecoveryUnlearned},
		{name: "the longest learned interval", learned: map[int64]int64{1: (4 * time.Minute).Milliseconds(),
			2: (40 * time.Minute).Milliseconds()}, want: 40 * time.Minute},
		{name: "at most an hour", learned: map[int64]int64{1: (4 * time.Hour).Milliseconds()}, want: RecoveryMax},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld(0)
			w.store.learned = tt.learned
			if err := w.alive("a").TakeOver(t.Context()); err != nil {
				t.Fatal(err)
			}
			w.advance(time.Hour)
			if err := w.alive("b").TakeOver(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := w.store.state.RecoveryUntil.Time.Sub(w.business.Now()); got != tt.want {
				t.Errorf("recovery window %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMarkWithoutARowTakesOver(t *testing.T) {
	w := newWorld(0)
	if err := w.alive("a").Mark(t.Context()); err != nil {
		t.Fatal(err)
	}
	if w.store.state == nil || w.store.state.LeaderReplicaID.String != "a" {
		t.Fatalf("state %+v", w.store.state)
	}
	// Another Leader's id in the row: the mark writes this Leader's own.
	w.advance(AliveMarkInterval)
	if err := w.alive("b").Mark(t.Context()); err != nil {
		t.Fatal(err)
	}
	if st := w.store.state; st.LeaderReplicaID.String != "b" || !st.LeaderSince.Time.Equal(w.business.Now()) {
		t.Errorf("state %+v", st)
	}
}

func TestAliveFailures(t *testing.T) {
	for _, name := range []string{"EnsureRuntimeState", "LockRuntimeState", "LatestOtherReplicaRefresh",
		"ListOrganizationIDs", "LongestLearnedRepeatInterval", "RecordDowntime", "TakeOver"} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(0)
			if err := w.alive("a").TakeOver(t.Context()); err != nil {
				t.Fatal(err)
			}
			w.advance(time.Hour)
			w.store.fail = name
			if err := w.alive("b").TakeOver(t.Context()); err == nil {
				t.Fatal("the takeover succeeded")
			}
			if len(w.store.downtime) != 0 || w.log.Len() != 0 {
				t.Errorf("a failed takeover left downtime %v, log %s", w.store.downtime, w.log.String())
			}
		})
	}
	// The Alert Groups of the downtime are recorded in the takeover's transaction: a failure rolls it back.
	w := newWorld(0)
	if err := w.alive("a").TakeOver(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.advance(time.Hour)
	failing := w.alive("b").OnDowntime(func(context.Context, dbgen.DBTX, int64, Downtime) error {
		return errors.New("no partition")
	})
	if err := failing.TakeOver(t.Context()); err == nil || len(w.store.downtime) != 0 {
		t.Errorf("a failed record on the alert groups = %v, downtime %v", err, w.store.downtime)
	}
	w.store.fail = "ListOrganizationIDs"
	w.store.downtime = nil
	if err := failing.TakeOver(t.Context()); err == nil || len(w.store.downtime) != 0 {
		t.Errorf("the organizations failed = %v, downtime %v", err, w.store.downtime)
	}
	w = newWorld(0)
	w.store.fail = "MarkAlive"
	if err := w.alive("a").Mark(t.Context()); err == nil {
		t.Error("the mark succeeded")
	}
	for _, name := range []string{"GetRuntimeState", "LatestDowntimeEnd"} {
		w.store.fail = name
		if _, err := Notices(t.Context(), w.store, start); err == nil {
			t.Errorf("Notices succeeded when %s failed", name)
		}
	}
}

func TestAliveMarkTask(t *testing.T) {
	w := newWorld(0)
	newTasks := Tasks(Work{Alive: w.alive("a")})
	mark := newTasks()[1]
	if err := mark.Run(t.Context()); err != nil { // the takeover
		t.Fatal(err)
	}
	w.advance(AliveMarkInterval)
	if err := mark.Run(t.Context()); err != nil { // a mark
		t.Fatal(err)
	}
	if !w.store.state.LeaderSince.Time.Equal(start) || !w.store.state.AliveAt.Time.Equal(start.Add(AliveMarkInterval)) {
		t.Fatalf("state %+v", w.store.state)
	}
	// A new leadership takes over again.
	w.advance(time.Hour)
	if err := newTasks()[1].Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(w.store.downtime) != 1 {
		t.Errorf("a new leadership did not take over: downtime %+v", w.store.downtime)
	}
	// A failed takeover is retried at the next run.
	w.advance(time.Hour)
	again := newTasks()[1]
	w.store.fail = "TakeOver"
	if err := again.Run(t.Context()); err == nil {
		t.Fatal("the takeover succeeded")
	}
	w.store.fail = ""
	if err := again.Run(t.Context()); err != nil || len(w.store.downtime) != 2 {
		t.Errorf("retried takeover: %v, downtime %+v", err, w.store.downtime)
	}
}

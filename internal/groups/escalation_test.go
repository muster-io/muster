// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/organization"
)

// chainCall is one call of the runs of the chains: Start or Stop, with the start of the chains it ran in.
type chainCall struct {
	op    string
	group int64
	gen   int64
	start ChainStart
	stop  ChainStop
}

// fakeChains are the runs of the chains in memory: each Start starts n runs, none when n is 0, in the current start of
// its Alert Group, which Started then reports; RanOut answers ranOut.
type fakeChains struct {
	calls   []chainCall
	n       int
	started map[[2]int64]bool
	ranOut  bool
	err     error
}

func (f *fakeChains) Start(_ context.Context, _ DBTX, g *Group, s ChainStart) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.calls = append(f.calls, chainCall{op: "start", group: g.ID, gen: g.ChainGeneration, start: s})
	if f.n > 0 {
		f.started[[2]int64{g.ID, g.ChainGeneration}] = true
	}
	if s.After != nil {
		s.After(func(context.Context) {})
	}
	return f.n, nil
}

func (f *fakeChains) Stop(_ context.Context, _ DBTX, g *Group, s ChainStop) error {
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, chainCall{op: "stop", group: g.ID, gen: g.ChainGeneration, stop: s})
	return nil
}

func (f *fakeChains) Started(_ context.Context, _ DBTX, g *Group) (bool, error) {
	return f.started[[2]int64{g.ID, g.ChainGeneration}], f.err
}

func (f *fakeChains) RanOut(context.Context, DBTX, *Group) (bool, error) {
	return f.ranOut, f.err
}

// withChains gives the harness the runs of the chains, each start starting one run.
func (h *harness) withChains() *fakeChains {
	f := &fakeChains{n: 1, started: map[[2]int64]bool{}}
	h.svc.SetChainRuns(f)
	return f
}

// since are the calls made from index from on, as "op gen" strings with what they started or stopped.
func (f *fakeChains) since(from int) []string {
	var out []string
	for _, c := range f.calls[from:] {
		switch {
		case c.op == "stop":
			out = append(out, "stop "+c.stop.Command)
		case c.start.All:
			out = append(out, "start all")
		default:
			s := "start"
			if c.start.Rise {
				s += " rise from " + string(c.start.Before)
			}
			if len(c.start.Teams) > 0 {
				s += " teams"
			}
			out = append(out, s)
		}
	}
	return out
}

// TestChainPoints is C-32.FR-5, C-32.AC-3, C-09.FR-9, C-11.FR-6 and C-10.FR-1: every point of the table of C-32.FR-5
// starts, starts over or stops the chains as its row says, opening a new start of the chains (chain_generation) only
// where they start or start over.
func TestChainPoints(t *testing.T) {
	published := func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
		g := h.firing(t, "x")
		h.publish(t, g, t0)
		return g
	}
	cases := []struct {
		name string
		// run makes the change after the setup and returns the Alert Group; the calls it expects follow.
		run  func(t *testing.T, h *harness) *dbgen.LockGroupsRow
		want []string
		gen  int64
	}{
		{"the first publication starts them", func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
			return published(t, h)
		}, []string{"start all"}, 1},
		{"a quiet publication by a change of configuration starts none",
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				g := h.firing(t, "x")
				if err := h.svc.MarkPublished(t.Context(), g.ID, t0, false); err != nil {
					t.Fatal(err)
				}
				return g
			}, nil, 0},
		{"a creation that no destination reaches starts them at once",
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				h.svc.SetRerender(func(_ context.Context, _ DBTX, r Rendering) error {
					if r.Unreached != nil {
						r.Unreached()
					}
					return nil
				})
				g := h.firing(t, "x")
				// Its first Publication, when a Destination recovers, starts nothing more.
				h.publish(t, g, t0.Add(time.Minute))
				return g
			}, []string{"start all"}, 1},
		{"a creation that a destination reaches waits for its publication",
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				h.svc.SetRerender(func(context.Context, DBTX, Rendering) error { return nil })
				return h.firing(t, "x")
			}, nil, 0},
		{"unacknowledge starts them over", func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
			g := published(t, h)
			h.people()
			if _, err := h.do(alice, CommandAcknowledge, g); err != nil {
				t.Fatal(err)
			}
			if _, err := h.do(alice, CommandUnacknowledge, g); err != nil {
				t.Fatal(err)
			}
			return g
		}, []string{"start all", "stop acknowledge", "start all"}, 2},
		{"snooze stops them and unsnooze starts them over",
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				g := published(t, h)
				h.people()
				if _, err := h.do(alice, CommandSnooze, g); err != nil {
					t.Fatal(err)
				}
				if _, err := h.do(alice, CommandUnsnooze, g); err != nil {
					t.Fatal(err)
				}
				return g
			}, []string{"start all", "stop snooze", "start all"}, 2},
		{"the end of a snooze starts them over", func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
			g := published(t, h)
			h.people()
			if _, err := h.do(alice, CommandSnooze, g); err != nil {
				t.Fatal(err)
			}
			h.clock.Advance(time.Hour)
			h.fire(t, g, TimerSnoozeEnd)
			return g
		}, []string{"start all", "stop snooze", "start all"}, 2},
		{"resolve stops them and unresolve starts them over",
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				g := published(t, h)
				h.people()
				if _, err := h.do(alice, CommandResolve, g); err != nil {
					t.Fatal(err)
				}
				if _, err := h.do(alice, CommandUnresolve, g); err != nil {
					t.Fatal(err)
				}
				return g
			}, []string{"start all", "stop resolve", "start all"}, 2},
		{"a reopen into firing starts them over", func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
			return h.reopen(t, func(g *dbgen.LockGroupsRow) { h.publish(t, g, t0) })
		}, []string{"start all", "stop resolve", "start all"}, 2},
		{"a rise to critical that ends an acknowledgement starts them over",
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				return h.rise(t, func(g *dbgen.LockGroupsRow) {
					h.publish(t, g, t0)
					h.people()
					if _, err := h.do(alice, CommandAcknowledge, g); err != nil {
						t.Fatal(err)
					}
				})
			}, []string{"start all", "stop acknowledge", "start all"}, 2},
		{"a rise of a firing alert group starts only the newly met",
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				return h.rise(t, func(g *dbgen.LockGroupsRow) { h.publish(t, g, t0) })
			}, []string{"start all", "start rise from warning"}, 1},
		{"the release of a disabled owner starts them over",
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				g := published(t, h)
				h.people()
				if _, err := h.do(alice, CommandAcknowledge, g); err != nil {
					t.Fatal(err)
				}
				after, err := h.svc.ReleaseOwner(t.Context(), nil, owner, ReasonOwnerDisabled)
				if err != nil {
					t.Fatal(err)
				}
				after(t.Context())
				return g
			}, []string{"start all", "stop acknowledge", "start all"}, 2},
		{"the own publication of an alert group a storm summary stood for starts them",
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				// A Storm holds the creation: delivery reports a Destination, so nothing starts until the end of the
				// Storm publishes the Alert Group, which delivery marks as starting the chains (TestStorm).
				h.svc.SetRerender(func(context.Context, DBTX, Rendering) error { return nil })
				g := h.firing(t, "x")
				h.clock.Advance(30 * time.Minute)
				h.publish(t, g, h.clock.Now())
				return g
			}, []string{"start all"}, 1},
		{"a delivered created event in events mode starts them",
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				// The worker marks the delivered created event as a first Publication that starts the chains
				// (TestWebhookEventsMarkPublished).
				g := h.firing(t, "x")
				if err := h.svc.MarkPublished(t.Context(), g.ID, t0.Add(time.Second), true); err != nil {
					t.Fatal(err)
				}
				return g
			}, []string{"start all"}, 1},
		{"an auto-unacknowledge starts them over", func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
			h.people()
			h.paging()
			h.db.routes[2].timers.AutoUnacknowledge = true
			var asked []int64
			h.svc.SetReminderReached(reachedAll(&asked))
			g := h.acknowledgedBy(t, bob, "x")
			h.runUntil(t, g, t0.Add(28*time.Hour))
			if g.Status != "firing" || h.events(g)[len(h.events(g))-1] != string(EventAutoUnacknowledged) {
				t.Fatalf("not auto-unacknowledged: %s %v", g.Status, h.events(g))
			}
			return g
		}, []string{"start all", "stop acknowledge", "start all"}, 2},
		{"the release of a deleted owner starts them over",
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				g := published(t, h)
				h.people()
				if _, err := h.do(alice, CommandAcknowledge, g); err != nil {
					t.Fatal(err)
				}
				after, err := h.svc.ReleaseOwner(t.Context(), nil, owner, ReasonOwnerDeleted)
				if err != nil {
					t.Fatal(err)
				}
				after(t.Context())
				return g
			}, []string{"start all", "stop acknowledge", "start all"}, 2},
		{"a rise to critical that ends a snooze starts them over",
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				g := h.rise(t, func(g *dbgen.LockGroupsRow) {
					h.publish(t, g, t0)
					h.people()
					if _, err := h.do(alice, CommandSnooze, g); err != nil {
						t.Fatal(err)
					}
				})
				if g.Status != "firing" {
					t.Fatalf("still %s", g.Status)
				}
				return g
			}, []string{"start all", "stop snooze", "start all"}, 2},
		{"a rise after a quiet publication opens the first start with the newly met chains",
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				return h.rise(t, func(g *dbgen.LockGroupsRow) {
					if err := h.svc.MarkPublished(t.Context(), g.ID, t0, false); err != nil {
						t.Fatal(err)
					}
				})
			}, []string{"start rise from warning"}, 1},
		{"a team joining after a quiet publication opens the first start with its chains",
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				h.withTeams()
				a := h.teamAlert("x", "", byLabel(payments))
				h.changes(t, ingest.ChangeFired, a)
				g := h.groupOf(t, a)
				if err := h.svc.MarkPublished(t.Context(), g.ID, t0, false); err != nil {
					t.Fatal(err)
				}
				h.clock.Advance(time.Minute)
				h.changes(t, ingest.ChangeFired, h.teamAlert("x", "", byLabel(search)))
				return g
			}, []string{"start teams"}, 1},
		{"a resolve by the alerts stops them", func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
			g := published(t, h)
			h.resolve(t, 1)
			return g
		}, []string{"start all", "stop resolve"}, 1},
		{"a team joining starts its chains", func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
			h.withTeams()
			a := h.teamAlert("x", "", byLabel(payments))
			h.changes(t, ingest.ChangeFired, a)
			g := h.groupOf(t, a)
			h.publish(t, g, t0)
			h.clock.Advance(time.Minute)
			h.changes(t, ingest.ChangeFired, h.teamAlert("x", "", byLabel(search)))
			return g
		}, []string{"start all", "start teams"}, 1},
		{"a team joining before the publication starts nothing",
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				h.withTeams()
				a := h.teamAlert("x", "", byLabel(payments))
				h.changes(t, ingest.ChangeFired, a)
				h.clock.Advance(time.Minute)
				h.changes(t, ingest.ChangeFired, h.teamAlert("x", "", byLabel(search)))
				return h.groupOf(t, a)
			}, nil, 0},
		{"new alerts without a rise or a team start nothing",
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				return h.join(t, func(g *dbgen.LockGroupsRow) { h.publish(t, g, t0) })
			}, []string{"start all"}, 1},
		{"a fall of the severity level stops none", func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
			g := h.rise(t, func(g *dbgen.LockGroupsRow) { h.publish(t, g, t0) })
			h.clock.Advance(time.Minute)
			h.resolve(t, 2)
			return g
		}, []string{"start all", "start rise from warning"}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			f := h.withChains()
			g := c.run(t, h)
			if got := f.since(0); !slices.Equal(got, c.want) {
				t.Errorf("calls %q, want %q", got, c.want)
			}
			if g.ChainGeneration != c.gen {
				t.Errorf("chain_generation %d, want %d", g.ChainGeneration, c.gen)
			}
			// Every start runs in a start of the chains, never in none (escalation_runs_generation_check).
			for _, call := range f.calls {
				if call.op == "start" && call.gen < 1 {
					t.Errorf("a start in generation %d", call.gen)
				}
			}
		})
	}
}

// TestMarkPublishedRendering: the mark of the first Publication renders as before the chains, without the mark of a
// change of configuration, whether or not it starts the chains: only the sync of the chains reads whether a change of
// configuration released the Publication (C-32.FR-5, ADR-0018).
func TestMarkPublishedRendering(t *testing.T) {
	for _, starts := range []bool{true, false} {
		h := newHarness(t)
		f := h.withChains()
		var rendered []Rendering
		h.svc.SetRerender(func(_ context.Context, _ DBTX, r Rendering) error {
			rendered = append(rendered, r)
			return nil
		})
		g := h.firing(t, "x")
		rendered = nil
		if err := h.svc.MarkPublished(t.Context(), g.ID, t0, starts); err != nil {
			t.Fatal(err)
		}
		if len(rendered) != 1 || rendered[0].Configuration || rendered[0].Grows != nil || len(rendered[0].Events) != 0 {
			t.Errorf("starts %v: rendered %+v", starts, rendered)
		}
		if got := f.since(0); (len(got) == 1) != starts {
			t.Errorf("starts %v: calls %q", starts, got)
		}
	}
}

// TestChainStopNamesTheCommand: the runs are stopped with the lifecycle event of the change that stopped them, which
// "How it went" reads the actor of, and at its time.
func TestChainStopNamesTheCommand(t *testing.T) {
	h := newHarness(t)
	f := h.withChains()
	g := h.firing(t, "x")
	h.publish(t, g, t0)
	h.people()
	h.clock.Advance(time.Minute)
	if _, err := h.do(alice, CommandAcknowledge, g); err != nil {
		t.Fatal(err)
	}
	stop := f.calls[len(f.calls)-1]
	if stop.op != "stop" || stop.stop.Command != StopAcknowledge || stop.stop.EventSeq != g.EventSeq ||
		!stop.stop.At.Equal(h.clock.Now()) || stop.stop.After == nil {
		t.Errorf("stop %+v, alert group at %d", stop, g.EventSeq)
	}
	if e := h.last(t, g); e.Event.String != "acknowledged" || e.EventSeq.Int64 != stop.stop.EventSeq {
		t.Errorf("last entry %+v", e.InsertTimelineEntryParams)
	}
}

// TestChainsAndConfiguration is C-32.FR-5 and C-32.AC-18: on a firing Alert Group that nobody owns, a change of
// configuration — the Minimum severity of its Route raised to critical, the evaluation of the Teams on a save that
// brings it a Team — starts no chain; an Acknowledge and an Unacknowledge then start the chains that apply.
func TestChainsAndConfiguration(t *testing.T) {
	t.Run("the minimum severity", func(t *testing.T) {
		u := newUpdates(t)
		f := u.withChains()
		u.publish(t, u.firing, t0)
		from := len(f.calls)
		u.clock.Advance(time.Minute)
		u.request("critical")
		u.round(t, u.worker)
		if u.firing.SeverityLevel != "critical" || len(f.calls) != from {
			t.Errorf("level %s, calls %q", u.firing.SeverityLevel, f.since(from))
		}
		u.people()
		for _, cmd := range []Command{CommandAcknowledge, CommandUnacknowledge} {
			if _, err := u.do(alice, cmd, u.firing); err != nil {
				t.Fatal(err)
			}
		}
		if got := f.since(from); !slices.Equal(got, []string{"stop acknowledge", "start all"}) {
			t.Errorf("after an acknowledge and an unacknowledge %q", got)
		}
	})
	t.Run("the evaluation of the teams on a save", func(t *testing.T) {
		e := newEvaluation(t)
		f := e.withChains()
		_, g := e.fire(t, 2, map[string]string{"alertname": "Latency", "cluster": "a", "team": "checkout"})
		e.publish(t, g, t0)
		from := len(f.calls)
		e.db.teamsOf().refs[checkoutTeam] = dbgen.ListTeamRefsRow{ID: checkoutTeam, PublicID: "TMCHECKOUTBBBB",
			Name: "checkout", Slug: "checkout"}
		e.clock.Advance(time.Minute)
		e.save(checkoutTeam)
		e.round(t, e.worker)
		if got := e.teamEntries(g); len(got) != 1 || len(f.calls) != from {
			t.Errorf("team entries %v, calls %q", got, f.since(from))
		}
	})
}

// TestChainsRanOut is C-32.FR-7, C-32.AC-5, C-17.FR-3 and C-17.FR-11: a step after which every run of the current
// start ran out makes the firing Alert Group Unclaimed, recording unclaimed once; a second such step records nothing;
// a chain that starts later, for a rise, ends Unclaimed while it runs, and it is Unclaimed again, with unclaimed, once
// that chain ran out too; Acknowledge ends it.
func TestChainsRanOut(t *testing.T) {
	h := newHarness(t)
	f := h.withChains()
	g := h.firing(t, "x")
	h.publish(t, g, t0)
	step := func(p ChainProgress) {
		t.Helper()
		after, err := h.svc.ChainStep(t.Context(), nil, g.ID, func(context.Context, *Group) (ChainProgress, error) {
			return p, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if after != nil {
			after(t.Context())
		}
	}
	unclaimed := func() int {
		n := 0
		for _, e := range h.entriesOf(g) {
			if e.Event.String == string(EventUnclaimed) {
				n++
			}
		}
		return n
	}
	h.clock.Advance(time.Minute)
	f.ranOut = false
	step(ChainProgress{RanOut: true, Changed: true})
	if g.Unclaimed || unclaimed() != 0 {
		t.Errorf("a chain still runs: unclaimed %v, %d entries", g.Unclaimed, unclaimed())
	}
	f.ranOut = true
	hints := h.hintsOf("alert-groups", g)
	step(ChainProgress{RanOut: true, Changed: true})
	if !g.Unclaimed || unclaimed() != 1 || h.hintsOf("alert-groups", g) != hints+1 {
		t.Errorf("every chain ran out: unclaimed %v, %d entries", g.Unclaimed, unclaimed())
	}
	if e := h.last(t, g); e.Kind != "timers" || e.Loudness.String != "quiet" || e.ActorKind != "system" {
		t.Errorf("unclaimed %+v", e.InsertTimelineEntryParams)
	}
	step(ChainProgress{RanOut: true})
	if unclaimed() != 1 {
		t.Errorf("a second run out recorded %d", unclaimed())
	}
	// A rise starts the chains for critical: no longer Unclaimed while they run.
	h.clock.Advance(time.Minute)
	h.changes(t, ingest.ChangeFired, h.alert(2, "critical", map[string]string{"alertname": "A", "cluster": "x",
		"n": "2"}))
	if g.Unclaimed {
		t.Error("still Unclaimed after a chain started")
	}
	step(ChainProgress{RanOut: true})
	if !g.Unclaimed || unclaimed() != 2 {
		t.Errorf("after the later chain ran out: unclaimed %v, %d entries", g.Unclaimed, unclaimed())
	}
	h.people()
	if _, err := h.do(alice, CommandAcknowledge, g); err != nil {
		t.Fatal(err)
	}
	if g.Unclaimed {
		t.Error("Unclaimed after an acknowledge")
	}
	// A step of a run on an Alert Group that is no longer firing records nothing.
	entries := len(h.entriesOf(g))
	step(ChainProgress{RanOut: true})
	if len(h.entriesOf(g)) != entries {
		t.Errorf("%d entries recorded on an acknowledged alert group", len(h.entriesOf(g))-entries)
	}
}

// TestEscalationStepSkipped is C-32.FR-14 and C-09.FR-22: each skip of a step records the Quiet
// escalation_step_skipped of the kind escalation, without Mentions, as Muster, with the run, the chain, the step and
// the reason, and the person of a skip of a person; the Timeline reads them with the chain, its name and the step, and
// a skip with an unknown reason is refused.
func TestEscalationStepSkipped(t *testing.T) {
	h := newHarness(t)
	h.people()
	h.withChains()
	g := h.firing(t, "x")
	h.publish(t, g, t0)
	h.clock.Advance(time.Minute)
	skips := []StepSkip{
		{ChainEntry: ChainEntry{Run: runID, Chain: 5, ChainPublicID: "EC0000000000PA", ChainName: "payments", Step: 3},
			Reason: SkipSeverity},
		{ChainEntry: ChainEntry{Run: deletedRunID, Step: 2}, Reason: SkipViewer,
			Person: &PagedUser{ID: bobID, PublicID: "SRAAAAAAAAAAB0", Name: "Bob", Login: "bob"}},
	}
	after, err := h.svc.ChainStep(t.Context(), nil, g.ID, func(_ context.Context, locked *Group) (ChainProgress,
		error) {
		if locked.ID != g.ID || locked.Status != StatusFiring || locked.ChainGeneration != 1 {
			t.Errorf("locked %+v", locked)
		}
		return ChainProgress{Skips: skips, Changed: true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	after(t.Context())
	var got []*fakeEntry
	for _, e := range h.entriesOf(g) {
		if e.Event.String == string(EventEscalationStepSkipped) {
			got = append(got, e)
		}
	}
	if len(got) != 2 || got[0].Kind != "escalation" || got[0].Loudness.String != "quiet" || len(got[0].Mentions) != 0 ||
		got[0].ActorKind != "system" || got[0].Reason.String != SkipSeverity || got[0].EscalationRunID.Int64 != runID ||
		got[0].EscalationChainID.Int64 != 5 || got[0].StepNumber.Int32 != 3 || got[1].EscalationChainID.Valid ||
		!slices.Equal(got[1].PagedUserIds, []int64{bobID}) || got[1].Reason.String != SkipViewer {
		t.Fatalf("entries %+v", got)
	}
	page, err := h.svc.Timeline(t.Context(), g.PublicID, TimelineFilter{Kinds: []Kind{KindEscalation}, Limit: 10,
		Ascending: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 2 || page.Entries[0].Chain == nil || page.Entries[0].Chain.PublicID != "EC0000000000PA" ||
		page.Entries[0].Chain.OwningTeam == nil || *page.Entries[0].Step != 3 || page.Entries[0].ChainName != "payments" ||
		page.Entries[1].Chain != nil || page.Entries[1].ChainName != "gone" || len(page.Entries[1].PagedUsers) != 1 ||
		page.Entries[1].PagedUsers[0].PublicID != "SRAAAAAAAAAAB0" {
		t.Errorf("timeline %+v", page.Entries)
	}
	if h.hintsOf("alert-group", g) == 0 {
		t.Error("no alert-group hint")
	}
	_, err = h.svc.ChainStep(t.Context(), nil, g.ID, func(context.Context, *Group) (ChainProgress, error) {
		return ChainProgress{Skips: []StepSkip{{Reason: "bored"}}}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "bored") {
		t.Errorf("an unknown reason: %v", err)
	}
}

// TestTimelinePageFields is C-32.FR-19: the Timeline reads, with the escalation entries of Pages, the Team of a
// page_team step's paged, the word for everyone in the chat, and the importance of a Page of people on its paged and
// its page_not_delivered; an entry that has none of them gives none.
func TestTimelinePageFields(t *testing.T) {
	h, g, targets, _ := pageHarness(t)
	h.db.teamsOf().refs[3] = dbgen.ListTeamRefsRow{ID: 3, PublicID: "TM0000000000DE", Name: "devops", Slug: "devops"}
	targets.targets = []PageTarget{{DestinationID: 11, Source: PageSourceRoute, Linked: []int64{owner}}}
	if err := h.step(t, g, ChainProgress{Pages: []StepPage{
		{ChainEntry: paymentsStep, People: []int64{owner, bobID}, Importance: "important"},
		{ChainEntry: paymentsStep, Team: 3, Seq: 1},
		{ChainEntry: paymentsStep, Everyone: "here", Seq: 2},
	}}); err != nil {
		t.Fatal(err)
	}
	page, err := h.svc.Timeline(t.Context(), g.PublicID, TimelineFilter{Kinds: []Kind{KindEscalation}, Limit: 10,
		Ascending: true})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range page.Entries {
		s := e.Event
		if e.Team != nil {
			s += "/team:" + e.Team.Slug
		}
		if e.Everyone != nil {
			s += "/everyone:" + *e.Everyone
		}
		if e.Importance != nil {
			s += "/" + *e.Importance
		}
		got = append(got, s)
	}
	want := []string{"paged/important", "page_not_delivered/important", "paged/team:devops", "paged/everyone:here"}
	if !slices.Equal(got, want) {
		t.Errorf("entries %v, want %v", got, want)
	}
}

// TestChainStepFailures: a step whose function fails, or whose Alert Group is gone, changes nothing; a failure of the
// runs fails the change that asked them.
func TestChainStepFailures(t *testing.T) {
	h := newHarness(t)
	f := h.withChains()
	g := h.firing(t, "x")
	errBoom := errors.New("boom")
	if _, err := h.svc.ChainStep(t.Context(), nil, g.ID, func(context.Context, *Group) (ChainProgress, error) {
		return ChainProgress{}, errBoom
	}); !errors.Is(err, errBoom) {
		t.Errorf("a failing step: %v", err)
	}
	called := false
	after, err := h.svc.ChainStep(t.Context(), nil, 9999, func(context.Context, *Group) (ChainProgress, error) {
		called = true
		return ChainProgress{}, nil
	})
	if err != nil || after != nil || called {
		t.Errorf("a missing alert group: %v %v", err, called)
	}
	f.err = errBoom
	if err := h.svc.MarkPublished(t.Context(), g.ID, t0, true); !errors.Is(err, errBoom) {
		t.Errorf("a failing start: %v", err)
	}
	f.err = nil
	h.publish(t, g, t0)
	f.err, f.ranOut = errBoom, true
	if _, err := h.svc.ChainStep(t.Context(), nil, g.ID, func(context.Context, *Group) (ChainProgress, error) {
		return ChainProgress{RanOut: true}, nil
	}); !errors.Is(err, errBoom) {
		t.Errorf("a failing ran out: %v", err)
	}
	h.people()
	if _, err := h.do(alice, CommandAcknowledge, g); !errors.Is(err, errBoom) {
		t.Errorf("a failing stop: %v", err)
	}
}

// TestChainStateOf: the chains run only while the Alert Group is firing, once published or started without a
// Destination.
func TestChainStateOf(t *testing.T) {
	for _, c := range []struct {
		s    timerState
		want bool
	}{
		{timerState{status: StatusFiring}, false},
		{timerState{status: StatusFiring, published: true}, true},
		{timerState{status: StatusFiring, generation: 1}, true},
		{timerState{status: StatusAcknowledged, published: true, generation: 1}, false},
		{timerState{status: StatusResolved, published: true, severity: organization.SeverityCritical}, false},
	} {
		if got := c.s.chainsRun(); got != c.want {
			t.Errorf("%+v: %v", c.s, got)
		}
	}
}

// unclaimedUnreached creates an Alert Group that no Destination reaches, whose chains start at once and run out, so
// that it is Unclaimed without ever being published; it returns its Alert.
func (h *harness) unclaimedUnreached(t *testing.T) int64 {
	t.Helper()
	f := h.withChains()
	h.svc.SetRerender(func(_ context.Context, _ DBTX, r Rendering) error {
		if r.Unreached != nil {
			r.Unreached()
		}
		return nil
	})
	a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"})
	h.changes(t, ingest.ChangeFired, a)
	g := h.groupOf(t, a)
	f.ranOut = true
	after, err := h.svc.ChainStep(t.Context(), nil, g.ID, func(context.Context, *Group) (ChainProgress, error) {
		return ChainProgress{RanOut: true, Changed: true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	after(t.Context())
	if !g.Unclaimed || g.FirstPublishedAt.Valid {
		t.Fatalf("setup: unclaimed %v, published %v", g.Unclaimed, g.FirstPublishedAt.Valid)
	}
	return a
}

// TestUnclaimedUnpublishedLeavesFiring is C-17.FR-3 and C-32.FR-7: an Alert Group that no Destination reaches,
// Unclaimed once its chains ran out without a Publication, stops being Unclaimed when it stops being firing — by
// Acknowledge, Snooze, Resolve or a resolve by its Alerts — and the change is saved (alert_groups_unclaimed_check).
func TestUnclaimedUnpublishedLeavesFiring(t *testing.T) {
	for _, c := range []struct {
		name   string
		status string
		run    func(t *testing.T, h *harness, a int64) error
	}{
		{"acknowledge", "acknowledged", func(t *testing.T, h *harness, a int64) error {
			_, err := h.do(alice, CommandAcknowledge, h.groupOf(t, a))
			return err
		}},
		{"snooze", "snoozed", func(t *testing.T, h *harness, a int64) error {
			_, err := h.do(alice, CommandSnooze, h.groupOf(t, a))
			return err
		}},
		{"resolve", "resolved", func(t *testing.T, h *harness, a int64) error {
			_, err := h.do(alice, CommandResolve, h.groupOf(t, a))
			return err
		}},
		{"a resolve by the alerts", "resolved", func(_ *testing.T, h *harness, a int64) error {
			h.db.alerts[a].Status = "resolved"
			_, err := h.apply(ingest.ChangeResolved, a)
			return err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.people()
			a := h.unclaimedUnreached(t)
			if err := c.run(t, h, a); err != nil {
				t.Fatal(err)
			}
			if g := h.groupOf(t, a); g.Status != c.status || g.Unclaimed {
				t.Errorf("status %s, unclaimed %v", g.Status, g.Unclaimed)
			}
		})
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package escalation

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/escalation/dbgen"
	"github.com/muster-io/muster/internal/groups"
)

// GetStepDestination reads the Destinations of the fake store, deleted ones included.
func (f *fakeRuns) GetStepDestination(_ context.Context, arg dbgen.GetStepDestinationParams) (
	dbgen.GetStepDestinationRow, error) {
	f.order = append(f.order, "GetStepDestination:"+strconv.FormatInt(arg.ID, 10))
	if err := f.fail["GetStepDestination"]; err != nil {
		return dbgen.GetStepDestinationRow{}, err
	}
	for _, d := range f.store.destinations {
		if d.ID == arg.ID && arg.OrgID == orgID {
			//nolint:staticcheck // S1016: field by field; the two generated rows only happen to have the same fields
			return dbgen.GetStepDestinationRow{ID: d.ID, PublicID: d.PublicID, Name: d.Name, Type: d.Type,
				WebhookMode: d.WebhookMode, Deleted: d.Deleted}, nil
		}
	}
	return dbgen.GetStepDestinationRow{}, pgx.ErrNoRows
}

func webhookStep(destination int64) dbgen.SnapshotStepsRow {
	return dbgen.SnapshotStepsRow{Kind: KindWebhook, DestinationID: pgtype.Int8{Int64: destination, Valid: true}}
}

// webhooks are the escalation_webhook the steps asked for, as "step destination after".
func (e *runsEnv) webhooks() []string {
	var out []string
	for _, p := range e.groups.progress {
		for _, w := range p.Webhooks {
			out = append(out, strconv.Itoa(w.Step)+" "+w.Destination.PublicID+" "+strconv.Itoa(w.After))
		}
	}
	return out
}

// TestWebhookStep is C-32.FR-13, C-32.AC-9 and C-32.FR-14: a webhook step whose Destination is an outgoing webhook in
// the mode events or both is made, recording escalation_webhook with the run, the chain, the step and the Destination;
// one whose Destination was deleted since the step was saved, or no longer exists, is skipped with
// destination_deleted, and one whose Destination is no longer an outgoing webhook in events mode or both with
// webhook_events_required; the chain goes on with its next step at once. The Severity condition and the Working hours
// skip it as any step that sends, before its Destination is read.
func TestWebhookStep(t *testing.T) {
	cases := []struct {
		name     string
		step     dbgen.SnapshotStepsRow
		made     []string
		skips    []string
		webhooks []string
		read     bool
	}{
		{"events mode", webhookStep(41), []string{"1 made", "2 made"}, nil, []string{"1 " + destHook + " 0"}, true},
		{"both modes", webhookStep(46), []string{"1 made", "2 made"}, nil, []string{"1 DS00000000004G 0"}, true},
		{"deleted since it was saved", webhookStep(45),
			[]string{"1 skipped destination_deleted", "2 made"}, []string{"1 destination_deleted"}, nil, true},
		{"gone", webhookStep(99), []string{"1 skipped destination_deleted", "2 made"},
			[]string{"1 destination_deleted"}, nil, true},
		{"switched to the template mode", webhookStep(42),
			[]string{"1 skipped webhook_events_required", "2 made"}, []string{"1 webhook_events_required"}, nil, true},
		{"not an outgoing webhook", webhookStep(43),
			[]string{"1 skipped webhook_events_required", "2 made"}, []string{"1 webhook_events_required"}, nil, true},
		{"a severity condition not met", critical(webhookStep(41)), []string{"1 skipped severity", "2 made"},
			[]string{"1 severity"}, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newRunsEnv(t)
			e.store.destinations = append(e.store.destinations, dbgen.ResolveDestinationsRow{ID: 46,
				PublicID: "DS00000000004G", Name: "both", Type: "webhook",
				WebhookMode: pgtype.Text{String: "both", Valid: true}})
			e.chain(1, "devops", false, c.step, page(schedMain))
			e.source(1, 0, ConditionAny)
			e.start(t, groups.ChainStart{All: true})
			run := e.q.runs[0]
			e.fire(t, run.id)
			if got := e.made(); !slices.Equal(got, c.made) {
				t.Errorf("steps %v, want %v", got, c.made)
			}
			if got := e.skips(); !slices.Equal(got, c.skips) {
				t.Errorf("skips %v, want %v", got, c.skips)
			}
			if got := e.webhooks(); !slices.Equal(got, c.webhooks) {
				t.Errorf("webhooks %v, want %v", got, c.webhooks)
			}
			if read := slices.ContainsFunc(e.q.order, func(s string) bool {
				return s == "GetStepDestination:"+strconv.FormatInt(c.step.DestinationID.Int64, 10)
			}); read != c.read {
				t.Errorf("destination read %v, want %v: %v", read, c.read, e.q.order)
			}
			for _, p := range e.groups.progress {
				for _, w := range p.Webhooks {
					if w.Run != run.id || w.Chain != 1 || w.ChainPublicID != e.q.chains[1].PublicID ||
						w.ChainName != "devops" || w.Destination.ID != c.step.DestinationID.Int64 ||
						w.Destination.Type != "webhook" || w.Destination.Name == "" {
						t.Errorf("webhook %+v", w)
					}
				}
			}
			if run.state != RunRanOut {
				t.Errorf("run %s", run.state)
			}
		})
	}
}

// TestWebhookStepOrder: the escalation_webhook of a step keeps its place among the skips of one firing, so that the
// dispatcher records the events in the order of the steps, repeats included.
func TestWebhookStepOrder(t *testing.T) {
	e := newRunsEnv(t)
	e.chain(1, "devops", false, critical(team()), webhookStep(41), people(23), webhookStep(42), webhookStep(41),
		repeat(2, 1))
	e.source(1, 0, ConditionAny)
	e.start(t, groups.ChainStart{All: true})
	e.fire(t, e.q.runs[0].id)
	if got, want := e.skips(), []string{"1 severity", "3 viewer vic", "4 webhook_events_required",
		"3 viewer vic", "4 webhook_events_required"}; !slices.Equal(got, want) {
		t.Errorf("skips %v, want %v", got, want)
	}
	// The first webhook comes after the skip of step 1, the second after step 4's, and the repeat's after those of
	// steps 3 and 4 again.
	if got, want := e.webhooks(), []string{"2 " + destHook + " 1", "5 " + destHook + " 3", "2 " + destHook + " 3",
		"5 " + destHook + " 5"}; !slices.Equal(got, want) {
		t.Errorf("webhooks %v, want %v", got, want)
	}
}

// TestWebhookStepFailure: a failure to read the step's Destination fails the step, which the timer worker retries;
// nothing of the step is recorded.
func TestWebhookStepFailure(t *testing.T) {
	e := newRunsEnv(t)
	e.chain(1, "devops", false, webhookStep(41))
	e.source(1, 0, ConditionAny)
	e.start(t, groups.ChainStart{All: true})
	e.q.fail["GetStepDestination"] = errors.New("boom")
	run := e.q.runs[0]
	if _, err := e.runs.FireStep(t.Context(), nil, run.id, e.q.timers[run.id]); err == nil {
		t.Fatal("no error")
	}
	if len(e.q.recorded) != 0 || len(e.groups.progress) != 0 {
		t.Errorf("steps %v, progress %v", e.made(), e.groups.progress)
	}
}

// TestWebhookStepWorkingHours: a webhook step limited to Working hours is skipped outside them without its
// Destination being read.
func TestWebhookStepWorkingHours(t *testing.T) {
	e := newRunsEnv(t)
	// 20:00 UTC on a Monday, outside the Working hours of the Organization.
	e.clock.Set(time.Date(2026, 11, 16, 20, 0, 0, 0, time.UTC))
	e.chain(1, "devops", false, workingHours(webhookStep(41)))
	e.source(1, 0, ConditionAny)
	e.start(t, groups.ChainStart{All: true})
	e.fire(t, e.q.runs[0].id)
	if got := e.skips(); !slices.Equal(got, []string{"1 outside_working_hours"}) || len(e.webhooks()) != 0 {
		t.Errorf("skips %v, webhooks %v", got, e.webhooks())
	}
	if slices.ContainsFunc(e.q.order, func(s string) bool { return s == "GetStepDestination:41" }) {
		t.Errorf("the destination was read: %v", e.q.order)
	}
}

// TestWebhookStepLimitOfARun: a made webhook step counts toward the step cap of its run like every step, so repeats
// nested around it stop at MaxStepsPerRun, each webhook step made recorded and handed over as one escalation_webhook.
func TestWebhookStepLimitOfARun(t *testing.T) {
	e := newRunsEnv(t)
	e.chain(1, "devops", false, webhookStep(41), repeat(1, 10), repeat(1, 10), repeat(1, 10), repeat(1, 10))
	e.source(1, 0, ConditionAny)
	e.start(t, groups.ChainStart{All: true})
	run := e.q.runs[0]
	for i := 0; i < 10 && run.state == RunRunning; i++ {
		e.fire(t, run.id)
	}
	hooks, made := 0, 0
	for _, p := range e.groups.progress {
		hooks += len(p.Webhooks)
	}
	for _, s := range e.q.recorded {
		if s.Kind == KindWebhook && s.Outcome == OutcomeMade {
			made++
		}
	}
	if run.state != RunRanOut || len(e.q.recorded) != MaxStepsPerRun || hooks == 0 || hooks != made ||
		!strings.Contains(e.log.String(), `"event":"escalation_run_step_limit"`) {
		t.Errorf("%s after %d steps, %d webhooks for %d made webhook steps", run.state, len(e.q.recorded), hooks,
			made)
	}
}

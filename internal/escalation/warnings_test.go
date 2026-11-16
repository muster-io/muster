// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package escalation

import (
	"fmt"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/escalation/dbgen"
	"github.com/muster-io/muster/internal/mentions"
)

// TestWarnings is C-32.FR-17 and C-30.FR-9: a chain written in a Mattermost and a Telegram Destination warns
// everyone_unsupported for its page_team steps at the Telegram one, with the reason of mentions.Can, and for @everyone
// at the Mattermost one; a call step whose gateway is no Notification gateway any more warns gateway_deleted, and a
// webhook step whose Destination was deleted destination_deleted; the Destinations are read with their health.
func TestWarnings(t *testing.T) {
	svc, store := newService(t)
	ctx := t.Context()
	store.gateways = []dbgen.ListGatewaysRow{{ID: 31, PublicID: gatewayOne, Name: "gw", Live: true}}
	steps := append(paymentsSteps(), StepInput{Kind: KindPageTeam, Everyone: ptr("everyone")},
		StepInput{Kind: KindCall, CallTarget: ptr(CallPeople), UserIDs: &[]string{userPetr}},
		StepInput{Kind: KindWebhook, DestinationID: ptr(destHook)})
	c, err := svc.Create(ctx, by, Input{Name: "payments", OwningTeamID: ptr(teamPay), Steps: steps})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Warnings) != 0 {
		t.Fatalf("warnings without destinations %+v", c.Warnings)
	}
	store.chainDests[c.ID] = []dbgen.ListChainDestinationsRow{
		{ID: 43, PublicID: destMM, Name: "payments-critical", Type: "mattermost", Health: "broken",
			BrokenSince: pgtype.Timestamptz{Time: t0, Valid: true}, BrokenReason: pgtype.Text{String: "gone", Valid: true}},
		{ID: 44, PublicID: destTG, Name: "payments-tg", Type: "telegram", Health: "healthy"},
	}
	// The gateway stops being one and the Destination of the webhook step is deleted.
	store.gateways[0].Live = false
	store.destinations[0].Deleted = true
	got, err := svc.Get(ctx, c.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Destinations) != 2 || got.Destinations[0].BrokenSince == nil || *got.Destinations[0].BrokenReason != "gone" {
		t.Errorf("destinations %+v", got.Destinations)
	}
	want := []Warning{
		{Kind: WarningEveryoneUnsupported, Step: 6, Destination: &NamedRef{PublicID: destTG, Name: "payments-tg"},
			Reason: mentions.ReasonTelegramNoGroups},
		{Kind: WarningEveryoneUnsupported, Step: 8, Destination: &NamedRef{PublicID: destMM, Name: "payments-critical"},
			Reason: mentions.ReasonMessengerHasNoToken},
		{Kind: WarningEveryoneUnsupported, Step: 8, Destination: &NamedRef{PublicID: destTG, Name: "payments-tg"},
			Reason: mentions.ReasonTelegramNoGroups},
		{Kind: WarningGatewayDeleted, Step: 9},
		{Kind: WarningDestinationDeleted, Step: 10, Destination: &NamedRef{PublicID: destHook, Name: "hook"}},
	}
	if len(got.Warnings) != len(want) {
		t.Fatalf("warnings %+v", got.Warnings)
	}
	for i, w := range want {
		g := got.Warnings[i]
		if g.Kind != w.Kind || g.Step != w.Step || g.Reason != w.Reason || (g.Destination == nil) != (w.Destination == nil) ||
			g.Destination != nil && *g.Destination != *w.Destination {
			t.Errorf("warning %d = %+v, want %+v", i, g, w)
		}
	}
	// A rename keeps the deleted gateway and Destination: what the stored steps name passes.
	in := Input{Name: "payments-renamed", Steps: steps}
	in.Steps[8].GatewayID = ptr(gatewayOne)
	if _, err := svc.Update(ctx, by, c.PublicID, nil, in); err != nil {
		t.Errorf("a rename with a deleted gateway and destination: %v", err)
	}
}

// TestChainWarnings is the warnings of C-32.FR-17 revised for L2 (#578): pages_nobody, without a step, for an empty
// chain and for a chain of waits, repeats and webhooks only, and none for a chain with a step that Pages anyone, of
// each kind; team_deleted on a page_team step whose Team was deleted — the one it names, else the chain's owning Team —
// and none for everyone in the chat or a Team that is not deleted.
func TestChainWarnings(t *testing.T) {
	kinds := func(ws []Warning) []string {
		out := make([]string, len(ws))
		for i, w := range ws {
			out[i] = fmt.Sprintf("%s:%d", w.Kind, w.Step)
		}
		return out
	}
	wait := Step{Number: 1, Kind: KindWait, WaitSeconds: 60}
	gone := &TeamRef{PublicID: teamGone, Deleted: true}
	live := &TeamRef{PublicID: teamPay}
	telegram := []DestinationRef{{PublicID: destTG, Name: "payments-tg", Type: "telegram"}}
	for name, c := range map[string]struct {
		chain Chain
		want  []string
	}{
		"empty":      {Chain{}, []string{"pages_nobody:0"}},
		"waits only": {Chain{Steps: []Step{wait, {Number: 2, Kind: KindWait, WaitSeconds: 120}}}, []string{"pages_nobody:0"}},
		"waits and a repeat": {Chain{Steps: []Step{wait, {Number: 2, Kind: KindRepeat, RepeatFrom: 1, RepeatTimes: 1}}},
			[]string{"pages_nobody:0"}},
		"webhook only": {Chain{Steps: []Step{{Number: 1, Kind: KindWebhook,
			Destination: &DestinationTarget{PublicID: destHook}}}}, []string{"pages_nobody:0"}},
		"on-call": {Chain{Steps: []Step{wait, {Number: 2, Kind: KindPageOnCall}}}, []string{}},
		"people":  {Chain{Steps: []Step{{Number: 1, Kind: KindPagePeople}}}, []string{}},
		"call":    {Chain{Steps: []Step{{Number: 1, Kind: KindCall, Gateway: &GatewayRef{Live: true}}}}, []string{}},
		"team of the step deleted": {Chain{OwningTeam: live, Steps: []Step{wait, {Number: 2, Kind: KindPageTeam,
			Team: gone}}}, []string{"team_deleted:2"}},
		"owning team deleted": {Chain{OwningTeam: gone, Steps: []Step{{Number: 1, Kind: KindPageTeam}}},
			[]string{"team_deleted:1"}},
		"named team kept": {Chain{OwningTeam: gone, Steps: []Step{{Number: 1, Kind: KindPageTeam, Team: live}}},
			[]string{}},
		"everyone": {Chain{OwningTeam: gone, Steps: []Step{{Number: 1, Kind: KindPageTeam, Everyone: "here"}}},
			[]string{}},
		"deleted team and telegram": {Chain{Destinations: telegram, Steps: []Step{{Number: 1, Kind: KindPageTeam,
			Team: gone}}}, []string{"team_deleted:1"}},
		"live team and telegram": {Chain{Destinations: telegram, Steps: []Step{{Number: 1, Kind: KindPageTeam,
			Team: live}}}, []string{"everyone_unsupported:1"}},
	} {
		if got := kinds(warningsOf(c.chain)); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

// TestTeamDeletedWarning reads team_deleted through the Service: a page_team step names devops, which is deleted after
// the save; the chain reads the warning on that step, and a chain of waits reads pages_nobody.
func TestTeamDeletedWarning(t *testing.T) {
	svc, store := newService(t)
	ctx := t.Context()
	c, err := svc.Create(ctx, by, Input{Name: "devops", Steps: []StepInput{{Kind: KindWait, WaitSeconds: ptr(60)},
		{Kind: KindPageTeam, TeamID: ptr(teamDev)}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Warnings) != 0 {
		t.Fatalf("warnings before the deletion %+v", c.Warnings)
	}
	store.teams[1].deleted = true
	got, err := svc.Get(ctx, c.PublicID)
	if err != nil || len(got.Warnings) != 1 || got.Warnings[0] != (Warning{Kind: WarningTeamDeleted, Step: 2}) {
		t.Errorf("after the deletion %+v, %v", got.Warnings, err)
	}
	waits, err := svc.Create(ctx, by, Input{Name: "waits", Steps: []StepInput{{Kind: KindWait, WaitSeconds: ptr(60)}}})
	if err != nil || len(waits.Warnings) != 1 || waits.Warnings[0] != (Warning{Kind: WarningPagesNobody}) {
		t.Errorf("a chain of waits %+v, %v", waits.Warnings, err)
	}
}

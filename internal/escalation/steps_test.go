// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package escalation

import (
	"errors"
	"slices"
	"testing"

	"github.com/muster-io/muster/internal/escalation/dbgen"
)

var errBoom = errors.New("boom")

func paymentsSteps() []StepInput {
	return []StepInput{
		{Kind: KindPageOnCall, ScheduleID: ptr(schedMain), Importance: ptr(ImportanceNormal)},
		{Kind: KindWait, WaitSeconds: ptr(600)},
		{Kind: KindPageOnCall, ScheduleID: ptr(schedMain), Importance: ptr(ImportanceImportant),
			Condition: ptr(ConditionCritical)},
		{Kind: KindWait, WaitSeconds: ptr(900)},
		{Kind: KindPageOnCall, ScheduleID: ptr(schedSecond), Importance: ptr(ImportanceImportant),
			Condition: ptr(ConditionCritical)},
		{Kind: KindPageTeam, WorkingHoursOnly: ptr(true)},
		{Kind: KindRepeat, RepeatFrom: ptr(3), RepeatTimes: ptr(2)},
	}
}

// TestShape is C-32.FR-3 for each kind on its own: the fields of its kind only, those it needs, the defaults, the
// limits of escalation.limits (P-84) and the target of a page_team step.
func TestShape(t *testing.T) {
	ids := ptr([]string{userPetr})
	for name, c := range map[string]struct {
		in       StepInput
		hasOwner bool
		pointer  string
		code     string
	}{
		"unknown kind":            {StepInput{Kind: "sms"}, true, "/steps/0/kind", CodeUnsupported},
		"wait with a condition":   {StepInput{Kind: KindWait, WaitSeconds: ptr(60), Condition: ptr("any")}, true, "/steps/0/severity_condition", CodeUnsupported},
		"wait with working hours": {StepInput{Kind: KindWait, WaitSeconds: ptr(60), WorkingHoursOnly: ptr(false)}, true, "/steps/0/working_hours_only", CodeUnsupported},
		"on-call with people":     {StepInput{Kind: KindPageOnCall, ScheduleID: ptr(schedMain), UserIDs: ids}, true, "/steps/0/user_ids", CodeUnsupported},
		"people with a gateway":   {StepInput{Kind: KindPagePeople, UserIDs: ids, GatewayID: ptr(gatewayOne)}, true, "/steps/0/gateway_id", CodeUnsupported},
		"team with importance":    {StepInput{Kind: KindPageTeam, Importance: ptr(ImportanceNormal)}, true, "/steps/0/importance", CodeUnsupported},
		"repeat with a wait":      {StepInput{Kind: KindRepeat, RepeatFrom: ptr(1), RepeatTimes: ptr(1), WaitSeconds: ptr(60)}, true, "/steps/0/wait_seconds", CodeUnsupported},
		"webhook with a team":     {StepInput{Kind: KindWebhook, DestinationID: ptr(destHook), TeamID: ptr(teamPay)}, true, "/steps/0/team_id", CodeUnsupported},
		"bad condition":           {StepInput{Kind: KindPageTeam, Condition: ptr("warning")}, true, "/steps/0/severity_condition", CodeUnsupported},
		"bad importance":          {StepInput{Kind: KindPagePeople, UserIDs: ids, Importance: ptr("urgent")}, true, "/steps/0/importance", CodeUnsupported},
		"on-call without":         {StepInput{Kind: KindPageOnCall}, true, "/steps/0/schedule_id", CodeRequired},
		"people without":          {StepInput{Kind: KindPagePeople, UserIDs: &[]string{}}, true, "/steps/0/user_ids", CodeRequired},
		"team and everyone":       {StepInput{Kind: KindPageTeam, TeamID: ptr(teamPay), Everyone: ptr("here")}, true, "/steps/0/everyone", CodeUnsupported},
		"bad everyone":            {StepInput{Kind: KindPageTeam, Everyone: ptr("room")}, true, "/steps/0/everyone", CodeUnsupported},
		"team without an owner":   {StepInput{Kind: KindPageTeam}, false, "/steps/0/team_id", CodeRequired},
		"call without a target":   {StepInput{Kind: KindCall}, true, "/steps/0/call_target", CodeRequired},
		"call on-call with users": {StepInput{Kind: KindCall, CallTarget: ptr(CallOnCall), UserIDs: ids}, true, "/steps/0/user_ids", CodeUnsupported},
		"call on-call without":    {StepInput{Kind: KindCall, CallTarget: ptr(CallOnCall)}, true, "/steps/0/schedule_id", CodeRequired},
		"call people with sched":  {StepInput{Kind: KindCall, CallTarget: ptr(CallPeople), ScheduleID: ptr(schedMain)}, true, "/steps/0/schedule_id", CodeUnsupported},
		"call people without":     {StepInput{Kind: KindCall, CallTarget: ptr(CallPeople)}, true, "/steps/0/user_ids", CodeRequired},
		"call bad target":         {StepInput{Kind: KindCall, CallTarget: ptr("team")}, true, "/steps/0/call_target", CodeUnsupported},
		"wait without":            {StepInput{Kind: KindWait}, true, "/steps/0/wait_seconds", CodeRequired},
		"wait of 90 s":            {StepInput{Kind: KindWait, WaitSeconds: ptr(90)}, true, "/steps/0/wait_seconds", CodeOutOfRange},
		"wait of 0":               {StepInput{Kind: KindWait, WaitSeconds: ptr(0)}, true, "/steps/0/wait_seconds", CodeOutOfRange},
		"wait of 1441 min":        {StepInput{Kind: KindWait, WaitSeconds: ptr(86460)}, true, "/steps/0/wait_seconds", CodeOutOfRange},
		"webhook without":         {StepInput{Kind: KindWebhook}, true, "/steps/0/destination_id", CodeRequired},
		"repeat without from":     {StepInput{Kind: KindRepeat, RepeatTimes: ptr(1)}, true, "/steps/0/repeat_from", CodeRequired},
		"repeat without times":    {StepInput{Kind: KindRepeat, RepeatFrom: ptr(1)}, true, "/steps/0/repeat_times", CodeRequired},
		"repeat from itself":      {StepInput{Kind: KindRepeat, RepeatFrom: ptr(1), RepeatTimes: ptr(1)}, true, "/steps/0/repeat_from", CodeOutOfRange},
	} {
		if _, err := shape(0, c.in, c.hasOwner); err == nil {
			t.Errorf("%s: accepted", name)
		} else if f := fieldError(t, err); f.Pointer != c.pointer || f.Code != c.code {
			t.Errorf("%s: %+v", name, f)
		}
	}
	for name, c := range map[string]struct {
		i    int
		in   StepInput
		want Step
	}{
		"on-call defaults": {0, StepInput{Kind: KindPageOnCall, ScheduleID: ptr(schedMain)},
			Step{Number: 1, Kind: KindPageOnCall, Importance: ImportanceNormal, Condition: ConditionAny}},
		"team of the owner": {1, StepInput{Kind: KindPageTeam, WorkingHoursOnly: ptr(true)},
			Step{Number: 2, Kind: KindPageTeam, WorkingHoursOnly: true, Condition: ConditionAny}},
		"everyone": {0, StepInput{Kind: KindPageTeam, Everyone: ptr("all"), Condition: ptr(ConditionBelowCritical)},
			Step{Number: 1, Kind: KindPageTeam, Everyone: "all", Condition: ConditionBelowCritical}},
		"call of people": {0, StepInput{Kind: KindCall, CallTarget: ptr(CallPeople), UserIDs: ids},
			Step{Number: 1, Kind: KindCall, CallTarget: CallPeople, Condition: ConditionAny}},
		"a day":       {0, StepInput{Kind: KindWait, WaitSeconds: ptr(86400)}, Step{Number: 1, Kind: KindWait, WaitSeconds: 86400}},
		"ten repeats": {4, StepInput{Kind: KindRepeat, RepeatFrom: ptr(4), RepeatTimes: ptr(10)}, Step{Number: 5, Kind: KindRepeat, RepeatFrom: 4, RepeatTimes: 10}},
		"one repeat":  {1, StepInput{Kind: KindRepeat, RepeatFrom: ptr(1), RepeatTimes: ptr(1)}, Step{Number: 2, Kind: KindRepeat, RepeatFrom: 1, RepeatTimes: 1}},
		"webhook":     {0, StepInput{Kind: KindWebhook, DestinationID: ptr(destHook)}, Step{Number: 1, Kind: KindWebhook, Condition: ConditionAny}},
	} {
		got, err := shape(c.i, c.in, false)
		if c.in.Kind == KindPageTeam && c.in.Everyone == nil {
			got, err = shape(c.i, c.in, true)
		}
		if err != nil || got.Number != c.want.Number || got.Kind != c.want.Kind || got.Importance != c.want.Importance ||
			got.Condition != c.want.Condition || got.WorkingHoursOnly != c.want.WorkingHoursOnly ||
			got.Everyone != c.want.Everyone || got.WaitSeconds != c.want.WaitSeconds || got.CallTarget != c.want.CallTarget ||
			got.RepeatFrom != c.want.RepeatFrom || got.RepeatTimes != c.want.RepeatTimes {
			t.Errorf("%s: %+v, %v", name, got, err)
		}
	}
	if _, err := shape(1, StepInput{Kind: KindRepeat, RepeatFrom: ptr(1), RepeatTimes: ptr(11)}, true); err == nil ||
		fieldError(t, err).Pointer != "/steps/1/repeat_times" {
		t.Errorf("eleven repeats: %v", err)
	}
	for name, c := range map[string]struct {
		in      StepInput
		pointer string
		code    string
	}{
		"no repeat":         {StepInput{Kind: KindRepeat, RepeatFrom: ptr(1), RepeatTimes: ptr(0)}, "/steps/1/repeat_times", CodeOutOfRange},
		"repeat from 0":     {StepInput{Kind: KindRepeat, RepeatFrom: ptr(0), RepeatTimes: ptr(1)}, "/steps/1/repeat_from", CodeOutOfRange},
		"call importance":   {StepInput{Kind: KindCall, CallTarget: ptr(CallOnCall), ScheduleID: ptr(schedMain), Importance: ptr(ImportanceNormal)}, "/steps/1/importance", CodeUnsupported},
		"wait of 1441 mins": {StepInput{Kind: KindWait, WaitSeconds: ptr(86460)}, "/steps/1/wait_seconds", CodeOutOfRange},
	} {
		_, err := shape(1, c.in, true)
		if err == nil {
			t.Errorf("%s at step 2: accepted", name)
			continue
		}
		if f := fieldError(t, err); f.Pointer != c.pointer || f.Code != c.code {
			t.Errorf("%s at step 2: %+v", name, f)
		}
	}
}

// TestResolveSteps is C-32.FR-3, C-32.AC-15 and C-30.FR-9 for what steps name: the limit of 20 steps; Schedules,
// Teams, people, gateways and Destinations known; a Viewer or a disabled person refused with not_pageable and the
// reason where the save adds them, and kept where the stored steps name them; a call step without a gateway, with
// one, with two; a webhook step whose Destination is no outgoing webhook in events mode or both; a failing read.
func TestResolveSteps(t *testing.T) {
	svc, store := newService(t)
	ctx := t.Context()
	owner := &TeamRef{ID: 1, PublicID: teamPay, Name: "payments", Slug: "payments"}
	resolve := func(in []StepInput, stored []Step) ([]stepRow, error) {
		return svc.resolveSteps(ctx, store, in, owner, stored)
	}
	refused := func(name string, in []StepInput, stored []Step, pointer, code, reason string) {
		t.Helper()
		_, err := resolve(in, stored)
		if err == nil {
			t.Errorf("%s: accepted", name)
			return
		}
		if f := fieldError(t, err); f.Pointer != pointer || f.Code != code || f.Reason != reason {
			t.Errorf("%s: %+v", name, f)
		}
	}
	rows, err := resolve(paymentsSteps(), nil)
	if err != nil || len(rows) != 7 || rows[0].scheduleID != 11 || rows[4].Schedule.Name != "Second line" ||
		rows[4].Schedule.Team.Slug != "payments" || rows[5].teamID != 0 || rows[6].RepeatFrom != 3 {
		t.Fatalf("the payments chain: %+v, %v", rows, err)
	}
	many := make([]StepInput, 21) // escalation.limits: at most 20 steps (P-84)
	for i := range many {
		many[i] = StepInput{Kind: KindWait, WaitSeconds: ptr(60)}
	}
	refused("21 steps", many, nil, "/steps", CodeOutOfRange, "")
	if _, err := resolve(many[:20], nil); err != nil {
		t.Errorf("20 steps: %v", err)
	}

	people := func(ids ...string) []StepInput {
		return []StepInput{{Kind: KindPagePeople, UserIDs: &ids}}
	}
	refused("viewer", people(userPetr, userVic), nil, "/steps/0/user_ids/1", CodeNotPageable, ReasonViewer)
	refused("disabled", people(userDora), nil, "/steps/0/user_ids/0", CodeNotPageable, ReasonDisabled)
	refused("deleted", people(userGone), nil, "/steps/0/user_ids/0", CodeUnknownID, "")
	refused("unknown", people("SR0000000000XX"), nil, "/steps/0/user_ids/0", CodeUnknownID, "")
	refused("twice", people(userPetr, "sr0000000000pe"), nil, "/steps/0/user_ids/1", CodeDuplicate, "")
	refused("call of a viewer", []StepInput{{Kind: KindCall, CallTarget: ptr(CallPeople), UserIDs: &[]string{userVic},
		GatewayID: ptr(gatewayOne)}}, nil, "/steps/0/user_ids/0", CodeNotPageable, ReasonViewer)
	stored := []Step{{Kind: KindPagePeople, Users: []UserRef{{PublicID: userVic}, {PublicID: userDora},
		{PublicID: userGone}}}}
	if rows, err := resolve(people(userVic, userDora, userGone, userPetr), stored); err != nil ||
		!slices.Equal(rows[0].userIDs, []int64{23, 24, 25, 21}) || rows[0].Users[2].Status != "deleted" {
		t.Errorf("the stored people kept: %+v, %v", rows, err)
	}

	refused("unknown schedule", []StepInput{{Kind: KindPageOnCall, ScheduleID: ptr("SC0000000000XX")}}, nil,
		"/steps/0/schedule_id", CodeUnknownID, "")
	refused("deleted team", []StepInput{{Kind: KindPageTeam, TeamID: ptr(teamGone)}}, nil, "/steps/0/team_id",
		CodeUnknownID, "")
	refused("unknown team", []StepInput{{Kind: KindPageTeam, TeamID: ptr("TM0000000000XX")}}, nil, "/steps/0/team_id",
		CodeUnknownID, "")
	if rows, err := resolve([]StepInput{{Kind: KindPageTeam, TeamID: ptr(teamGone)}},
		[]Step{{Kind: KindPageTeam, Team: &TeamRef{PublicID: teamGone}}}); err != nil || rows[0].teamID != 3 ||
		!rows[0].Team.Deleted {
		t.Errorf("a stored deleted team kept: %+v, %v", rows, err)
	}
	if rows, err := resolve([]StepInput{{Kind: KindPageTeam, TeamID: ptr(teamDev)}}, nil); err != nil ||
		rows[0].teamID != 2 {
		t.Errorf("another team: %+v, %v", rows, err)
	}

	call := func(gateway *string) []StepInput {
		return []StepInput{{Kind: KindCall, CallTarget: ptr(CallOnCall), ScheduleID: ptr(schedMain),
			GatewayID: gateway}}
	}
	refused("no gateway", call(nil), nil, "/steps/0", CodeGatewayRequired, "")
	refused("no gateway, one named", call(ptr(gatewayOne)), nil, "/steps/0", CodeGatewayRequired, "")
	store.gateways = []dbgen.ListGatewaysRow{{ID: 31, PublicID: gatewayOne, Name: "gw", Live: true},
		{ID: 33, PublicID: "CN0000000000F0", Name: "old", Live: false}}
	if rows, err := resolve(call(nil), nil); err != nil || rows[0].gatewayID != 31 || rows[0].Gateway.Name != "gw" {
		t.Errorf("the only gateway: %+v, %v", rows, err)
	}
	refused("not a gateway", call(ptr("CN0000000000F0")), nil, "/steps/0/gateway_id", CodeUnknownID, "")
	store.gateways = append(store.gateways, dbgen.ListGatewaysRow{ID: 32, PublicID: gatewayTwo, Name: "gw2",
		Live: true})
	refused("two gateways", call(nil), nil, "/steps/0/gateway_id", CodeRequired, "")
	if rows, err := resolve(call(ptr(gatewayTwo)), nil); err != nil || rows[0].gatewayID != 32 {
		t.Errorf("the second gateway: %+v, %v", rows, err)
	}
	store.gateways = []dbgen.ListGatewaysRow{{ID: 33, PublicID: "CN0000000000F0", Name: "old", Live: false}}
	if rows, err := resolve(call(ptr("CN0000000000F0")), []Step{{Kind: KindCall,
		Gateway: &GatewayRef{PublicID: "CN0000000000F0"}}}); err != nil || rows[0].gatewayID != 33 || rows[0].Gateway.Live {
		t.Errorf("a stored deleted gateway kept: %+v, %v", rows, err)
	}

	hook := func(id string) []StepInput { return []StepInput{{Kind: KindWebhook, DestinationID: &id}} }
	if rows, err := resolve(hook(destHook), nil); err != nil || rows[0].destinationID != 41 {
		t.Errorf("a webhook in events mode: %+v, %v", rows, err)
	}
	refused("template mode", hook(destTemplate), nil, "/steps/0/destination_id", CodeWebhookEventsRequired, "")
	refused("mattermost", hook(destMM), nil, "/steps/0/destination_id", CodeWebhookEventsRequired, "")
	refused("deleted", hook(destGone), nil, "/steps/0/destination_id", CodeUnknownID, "")
	refused("unknown", hook("DS0000000000XX"), nil, "/steps/0/destination_id", CodeUnknownID, "")
	if rows, err := resolve(hook(destGone), []Step{{Kind: KindWebhook,
		Destination: &DestinationTarget{PublicID: destGone}}}); err != nil || !rows[0].Destination.Deleted {
		t.Errorf("a stored deleted destination kept: %+v, %v", rows, err)
	}
	// A stored webhook Destination whose mode has since become template is kept: the run skips the step (S-156).
	if rows, err := resolve(hook(destTemplate), []Step{{Kind: KindWebhook,
		Destination: &DestinationTarget{PublicID: destTemplate}}}); err != nil || rows[0].destinationID != 42 {
		t.Errorf("a stored destination now in template mode: %+v, %v", rows, err)
	}
	// A disabled Viewer is refused as disabled, the order of S-131's skipReason.
	store.users = append(store.users, dbgen.ResolveUsersRow{ID: 26, PublicID: "SR0000000000DV", Name: "Dave",
		Login: "dave", Role: "viewer", Status: "disabled"})
	refused("a disabled viewer", people("SR0000000000DV"), nil, "/steps/0/user_ids/0", CodeNotPageable, ReasonDisabled)

	for _, query := range []string{"ResolveSchedules", "ResolveUsers", "ResolveTeams", "ResolveDestinations",
		"ListGateways"} {
		store.fail = map[string]error{query: errBoom}
		in := append(paymentsSteps(), people(userPetr)[0], StepInput{Kind: KindPageTeam, TeamID: ptr(teamDev)},
			hook(destHook)[0], call(nil)[0])
		if _, err := resolve(in, nil); !errors.Is(err, errBoom) {
			t.Errorf("%s fails: %v", query, err)
		}
	}
}

// TestWorkingHoursOf is C-25.FR-9 for steps: a chain uses the Working hours of its owning Team, its own when it has
// them, else the Organization's, and the Organization's without an owning Team.
func TestWorkingHoursOf(t *testing.T) {
	svc, store := newService(t)
	ctx := t.Context()
	for name, c := range map[string]struct {
		owner *TeamRef
		zone  string
	}{
		"own hours":    {&TeamRef{ID: 1, PublicID: teamPay}, "Europe/Berlin"},
		"organization": {&TeamRef{ID: 2, PublicID: teamDev}, "UTC"},
		"no owner":     {nil, "UTC"},
		"gone":         {&TeamRef{ID: 99, PublicID: "TM0000000000XX"}, "UTC"},
	} {
		w, err := svc.WorkingHoursOf(ctx, Chain{OwningTeam: c.owner})
		if err != nil || w.TimeZone != c.zone {
			t.Errorf("%s: %+v, %v", name, w, err)
		}
	}
	for _, query := range []string{"GetOrganizationWorkingHours", "GetTeamWorkingHours"} {
		store.fail = map[string]error{query: errBoom}
		if _, err := svc.WorkingHoursOf(ctx, Chain{OwningTeam: &TeamRef{ID: 1}}); !errors.Is(err, errBoom) {
			t.Errorf("%s fails: %v", query, err)
		}
	}
}

// TestStepView: the Audit log shows each step with the fields of its kind only.
func TestStepView(t *testing.T) {
	wait := Step{Kind: KindWait, WaitSeconds: 60}.view()
	if wait.WorkingHoursOnly != nil || wait.Condition != "" || wait.WaitSeconds != 60 {
		t.Errorf("wait %+v", wait)
	}
	call := Step{Kind: KindCall, CallTarget: CallPeople, Users: []UserRef{{PublicID: userPetr}},
		Gateway: &GatewayRef{PublicID: gatewayOne}, Condition: ConditionAny}.view()
	if call.WorkingHoursOnly == nil || *call.WorkingHoursOnly || call.GatewayID != gatewayOne ||
		!slices.Equal(call.UserIDs, []string{userPetr}) {
		t.Errorf("call %+v", call)
	}
}

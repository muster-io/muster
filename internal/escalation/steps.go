// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package escalation

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/escalation/dbgen"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/teams"
)

// The kinds of an Escalation step (C-32.FR-3).
const (
	KindPageOnCall = "page_on_call"
	KindPagePeople = "page_people"
	KindPageTeam   = "page_team"
	KindCall       = "call"
	KindWait       = "wait"
	KindWebhook    = "webhook"
	KindRepeat     = "repeat"
)

// The importance of a Page, and the targets of a call.
const (
	ImportanceNormal    = "normal"
	ImportanceImportant = "important"
	CallOnCall          = "on_call"
	CallPeople          = "people"
)

// The Severity conditions of a step (C-26.FR-1).
const (
	ConditionAny           = "any"
	ConditionCritical      = "critical"
	ConditionBelowCritical = "below_critical"
)

// The limits of a chain, escalation.limits (P-84, confirmed with the story of the Escalation chains): at most
// MaxSteps steps, each wait a whole number of minutes from MinWaitSeconds to MaxWaitSeconds, and a repeat at most
// MaxRepeatTimes times. Migration 0046 pins the same values in the CHECKs of escalation_steps.
const (
	MaxSteps       = 20
	MinWaitSeconds = 60
	MaxWaitSeconds = 86400
	MaxRepeatTimes = 10
)

// The statuses and the Role that keep a person from being Paged.
const (
	statusDisabled = "disabled"
	statusDeleted  = "deleted"
	roleViewer     = "viewer"
)

// The modes of an outgoing webhook Destination that receive events, and the type of such a Destination.
const (
	typeWebhook  = "webhook"
	modeTemplate = "template"
)

// The fields of a step input by their names in the API.
const (
	fieldImportance   = "importance"
	fieldSchedule     = "schedule_id"
	fieldUsers        = "user_ids"
	fieldTeam         = "team_id"
	fieldEveryone     = "everyone"
	fieldCallTarget   = "call_target"
	fieldGateway      = "gateway_id"
	fieldWait         = "wait_seconds"
	fieldDestination  = "destination_id"
	fieldRepeatFrom   = "repeat_from"
	fieldRepeatTimes  = "repeat_times"
	fieldWorkingHours = "working_hours_only"
	fieldCondition    = "severity_condition"
)

// paging are the fields of every step that Pages or sends.
var paging = []string{fieldWorkingHours, fieldCondition}

// kindFields are the fields each kind takes.
var kindFields = map[string][]string{
	KindPageOnCall: append([]string{fieldImportance, fieldSchedule}, paging...),
	KindPagePeople: append([]string{fieldImportance, fieldUsers}, paging...),
	KindPageTeam:   append([]string{fieldTeam, fieldEveryone}, paging...),
	KindCall:       append([]string{fieldCallTarget, fieldSchedule, fieldUsers, fieldGateway}, paging...),
	KindWait:       {fieldWait},
	KindWebhook:    append([]string{fieldDestination}, paging...),
	KindRepeat:     {fieldRepeatFrom, fieldRepeatTimes},
}

// StepInput is a step of a request body; a nil field was not given.
type StepInput struct {
	Kind             string
	Importance       *string
	ScheduleID       *string
	UserIDs          *[]string
	TeamID           *string
	Everyone         *string
	CallTarget       *string
	GatewayID        *string
	WaitSeconds      *int
	DestinationID    *string
	RepeatFrom       *int
	RepeatTimes      *int
	WorkingHoursOnly *bool
	Condition        *string
}

// given are the names of the fields the input gives, in the order of the API.
func (in StepInput) given() []string {
	var out []string
	add := func(name string, set bool) {
		if set {
			out = append(out, name)
		}
	}
	add(fieldImportance, in.Importance != nil)
	add(fieldSchedule, in.ScheduleID != nil)
	add(fieldUsers, in.UserIDs != nil)
	add(fieldTeam, in.TeamID != nil)
	add(fieldEveryone, in.Everyone != nil)
	add(fieldCallTarget, in.CallTarget != nil)
	add(fieldGateway, in.GatewayID != nil)
	add(fieldWait, in.WaitSeconds != nil)
	add(fieldDestination, in.DestinationID != nil)
	add(fieldRepeatFrom, in.RepeatFrom != nil)
	add(fieldRepeatTimes, in.RepeatTimes != nil)
	add(fieldWorkingHours, in.WorkingHoursOnly != nil)
	add(fieldCondition, in.Condition != nil)
	return out
}

// Step is a step of a chain as a read gives it, with its number from 1 and the objects it names: Team is the Team a
// page_team step names itself, nil when it Pages the chain's owning Team or everyone.
type Step struct {
	Number           int
	Kind             string
	Importance       string
	Schedule         *ScheduleRef
	Users            []UserRef
	Team             *TeamRef
	Everyone         string
	CallTarget       string
	Gateway          *GatewayRef
	WaitSeconds      int
	Destination      *DestinationTarget
	RepeatFrom       int
	RepeatTimes      int
	WorkingHoursOnly bool
	Condition        string
}

// Pages reports whether the step Pages or sends, and so takes the limit to Working hours and a Severity condition.
func (st Step) Pages() bool {
	return st.Kind != KindWait && st.Kind != KindRepeat
}

// ScheduleRef is the Schedule a step names, with its Team.
type ScheduleRef struct {
	PublicID string
	Name     string
	Team     TeamRef
}

// UserRef is a person a step names, with their Role and status.
type UserRef struct {
	PublicID string
	Name     string
	Login    string
	Role     string
	Status   string
}

// GatewayRef is the Notification gateway of a call step, Live while it is one: not deleted, its info declaring the role
// gateway.
type GatewayRef struct {
	PublicID string
	Name     string
	Live     bool
}

// DestinationTarget is the Destination of a webhook step, with its type and mode, marked deleted once deleted.
type DestinationTarget struct {
	PublicID string
	Name     string
	Type     string
	Mode     string
	Deleted  bool
}

// stepView is a step as the Audit log diff of its chain shows it: the fields of its kind, by public_id.
type stepView struct {
	Kind             string   `json:"kind"`
	Importance       string   `json:"importance,omitempty"`
	ScheduleID       string   `json:"schedule_id,omitempty"`
	UserIDs          []string `json:"user_ids,omitempty"`
	TeamID           string   `json:"team_id,omitempty"`
	Everyone         string   `json:"everyone,omitempty"`
	CallTarget       string   `json:"call_target,omitempty"`
	GatewayID        string   `json:"gateway_id,omitempty"`
	WaitSeconds      int      `json:"wait_seconds,omitempty"`
	DestinationID    string   `json:"destination_id,omitempty"`
	RepeatFrom       int      `json:"repeat_from,omitempty"`
	RepeatTimes      int      `json:"repeat_times,omitempty"`
	WorkingHoursOnly *bool    `json:"working_hours_only,omitempty"`
	Condition        string   `json:"severity_condition,omitempty"`
}

func (st Step) view() stepView {
	v := stepView{Kind: st.Kind, Importance: st.Importance, Everyone: st.Everyone, CallTarget: st.CallTarget,
		WaitSeconds: st.WaitSeconds, RepeatFrom: st.RepeatFrom, RepeatTimes: st.RepeatTimes}
	if st.Schedule != nil {
		v.ScheduleID = st.Schedule.PublicID
	}
	for _, u := range st.Users {
		v.UserIDs = append(v.UserIDs, u.PublicID)
	}
	if st.Team != nil {
		v.TeamID = st.Team.PublicID
	}
	if st.Gateway != nil {
		v.GatewayID = st.Gateway.PublicID
	}
	if st.Destination != nil {
		v.DestinationID = st.Destination.PublicID
	}
	if st.Pages() {
		wh := st.WorkingHoursOnly
		v.WorkingHoursOnly, v.Condition = &wh, st.Condition
	}
	return v
}

// stepRow is a step as a save writes it: the step and the internal ids of what it names.
type stepRow struct {
	Step
	scheduleID    int64
	teamID        int64
	gatewayID     int64
	destinationID int64
	userIDs       []int64
}

// stepsOf are the steps of the rows of a save.
func stepsOf(rows []stepRow) []Step {
	out := make([]Step, len(rows))
	for i, r := range rows {
		out[i] = r.Step
	}
	return out
}

func pointerOf(i int, field string) string {
	p := "/steps/" + strconv.Itoa(i)
	if field != "" {
		p += "/" + field
	}
	return p
}

func required(i int, field, detail string) *FieldError {
	return &FieldError{Pointer: pointerOf(i, field), Code: CodeRequired, Detail: detail}
}

// shape checks the step at index i on its own, before what it names is read: the fields of its kind only, those it
// needs, its wait and its repeat within escalation.limits, and a page_team step that names neither a Team nor everyone
// only in a chain with an owning Team (hasOwner). It returns the step with its defaults: importance normal, the
// condition any.
func shape(i int, in StepInput, hasOwner bool) (Step, error) {
	fields, ok := kindFields[in.Kind]
	if !ok {
		return Step{}, &FieldError{Pointer: pointerOf(i, "kind"), Code: CodeUnsupported,
			Detail: "A step is page_on_call, page_people, page_team, call, wait, webhook or repeat."}
	}
	for _, f := range in.given() {
		if !slices.Contains(fields, f) {
			return Step{}, &FieldError{Pointer: pointerOf(i, f), Code: CodeUnsupported,
				Detail: fmt.Sprintf("A %s step does not take %s.", in.Kind, f)}
		}
	}
	st := Step{Number: i + 1, Kind: in.Kind}
	if st.Pages() {
		st.WorkingHoursOnly = in.WorkingHoursOnly != nil && *in.WorkingHoursOnly
		st.Condition = ConditionAny
		if in.Condition != nil {
			switch *in.Condition {
			case ConditionAny, ConditionCritical, ConditionBelowCritical:
				st.Condition = *in.Condition
			default:
				return Step{}, &FieldError{Pointer: pointerOf(i, fieldCondition), Code: CodeUnsupported,
					Detail: "Choose any, critical or below_critical."}
			}
		}
	}
	switch in.Kind {
	case KindPageOnCall, KindPagePeople:
		st.Importance = ImportanceNormal
		if in.Importance != nil {
			if *in.Importance != ImportanceNormal && *in.Importance != ImportanceImportant {
				return Step{}, &FieldError{Pointer: pointerOf(i, fieldImportance), Code: CodeUnsupported,
					Detail: "A Page is normal or important."}
			}
			st.Importance = *in.Importance
		}
		if in.Kind == KindPageOnCall && in.ScheduleID == nil {
			return Step{}, required(i, fieldSchedule, "A step that Pages the On-call names its Schedule.")
		}
		if in.Kind == KindPagePeople && (in.UserIDs == nil || len(*in.UserIDs) == 0) {
			return Step{}, required(i, fieldUsers, "A step that Pages people names at least one.")
		}
	case KindPageTeam:
		switch {
		case in.TeamID != nil && in.Everyone != nil:
			return Step{}, &FieldError{Pointer: pointerOf(i, fieldEveryone), Code: CodeUnsupported,
				Detail: "A step Pages a Team or everyone in the chat, not both."}
		case in.Everyone != nil:
			switch *in.Everyone {
			case "channel", "here", "all", "everyone":
				st.Everyone = *in.Everyone
			default:
				return Step{}, &FieldError{Pointer: pointerOf(i, fieldEveryone), Code: CodeUnsupported,
					Detail: "Everyone in the chat is channel, here, all or everyone."}
			}
		case in.TeamID == nil && !hasOwner:
			return Step{}, required(i, fieldTeam,
				"The chain has no owning Team: name the Team the step Pages, or everyone in the chat.")
		}
	case KindCall:
		switch {
		case in.CallTarget == nil:
			return Step{}, required(i, fieldCallTarget, "A call names its target: on_call or people.")
		case *in.CallTarget == CallOnCall:
			if in.UserIDs != nil {
				return Step{}, &FieldError{Pointer: pointerOf(i, fieldUsers), Code: CodeUnsupported,
					Detail: "A call of the On-call names a Schedule, not people."}
			}
			if in.ScheduleID == nil {
				return Step{}, required(i, fieldSchedule, "A call of the On-call names its Schedule.")
			}
		case *in.CallTarget == CallPeople:
			if in.ScheduleID != nil {
				return Step{}, &FieldError{Pointer: pointerOf(i, fieldSchedule), Code: CodeUnsupported,
					Detail: "A call of people names people, not a Schedule."}
			}
			if in.UserIDs == nil || len(*in.UserIDs) == 0 {
				return Step{}, required(i, fieldUsers, "A call of people names at least one.")
			}
		default:
			return Step{}, &FieldError{Pointer: pointerOf(i, fieldCallTarget), Code: CodeUnsupported,
				Detail: "A call's target is on_call or people."}
		}
		st.CallTarget = *in.CallTarget
	case KindWait:
		if in.WaitSeconds == nil {
			return Step{}, required(i, fieldWait, "A wait needs wait_seconds.")
		}
		if w := *in.WaitSeconds; w < MinWaitSeconds || w > MaxWaitSeconds || w%60 != 0 {
			return Step{}, &FieldError{Pointer: pointerOf(i, fieldWait), Code: CodeOutOfRange,
				Detail: "A wait is a whole number of minutes from 1 to 1,440: a multiple of 60 from 60 to 86400 seconds."}
		}
		st.WaitSeconds = *in.WaitSeconds
	case KindWebhook:
		if in.DestinationID == nil {
			return Step{}, required(i, fieldDestination, "A webhook step names its outgoing webhook Destination.")
		}
	case KindRepeat:
		switch {
		case in.RepeatFrom == nil:
			return Step{}, required(i, fieldRepeatFrom, "A repeat names the step it goes back to.")
		case in.RepeatTimes == nil:
			return Step{}, required(i, fieldRepeatTimes, "A repeat says how many times it goes back.")
		case *in.RepeatFrom < 1 || *in.RepeatFrom > i:
			return Step{}, &FieldError{Pointer: pointerOf(i, fieldRepeatFrom), Code: CodeOutOfRange,
				Detail: "A repeat goes back to an earlier step, by its number from 1."}
		case *in.RepeatTimes < 1 || *in.RepeatTimes > MaxRepeatTimes:
			return Step{}, &FieldError{Pointer: pointerOf(i, fieldRepeatTimes), Code: CodeOutOfRange,
				Detail: fmt.Sprintf("A repeat goes back from 1 to %d times.", MaxRepeatTimes)}
		}
		st.RepeatFrom, st.RepeatTimes = *in.RepeatFrom, *in.RepeatTimes
	}
	return st, nil
}

// kept are the objects the stored steps of a chain name, by public_id, which a save keeps whatever became of them.
type kept struct {
	users, teams, gateways, destinations map[string]bool
}

func keptOf(stored []Step) kept {
	k := kept{users: map[string]bool{}, teams: map[string]bool{}, gateways: map[string]bool{},
		destinations: map[string]bool{}}
	for _, st := range stored {
		for _, u := range st.Users {
			k.users[u.PublicID] = true
		}
		if st.Team != nil {
			k.teams[st.Team.PublicID] = true
		}
		if st.Gateway != nil {
			k.gateways[st.Gateway.PublicID] = true
		}
		if st.Destination != nil {
			k.destinations[st.Destination.PublicID] = true
		}
	}
	return k
}

// canonical normalizes the public_id raw of prefix, empty when it is none.
func canonical(prefix publicid.Prefix, raw string) string {
	id, err := publicid.Parse(prefix, raw)
	if err != nil {
		return ""
	}
	return id
}

// references are the public_ids the steps of a save name, for one read of each kind.
type references struct {
	schedules, users, teams, destinations []string
	calls                                 bool
}

func referencesOf(in []StepInput) references {
	var r references
	for _, st := range in {
		if st.ScheduleID != nil {
			r.schedules = append(r.schedules, canonical(publicid.Schedule, *st.ScheduleID))
		}
		if st.UserIDs != nil {
			for _, u := range *st.UserIDs {
				r.users = append(r.users, canonical(publicid.User, u))
			}
		}
		if st.TeamID != nil {
			r.teams = append(r.teams, canonical(publicid.Team, *st.TeamID))
		}
		if st.DestinationID != nil {
			r.destinations = append(r.destinations, canonical(publicid.Destination, *st.DestinationID))
		}
		if st.Kind == KindCall {
			r.calls = true
		}
	}
	return r
}

// found are the objects the steps of a save name, as read, by public_id.
type found struct {
	schedules    map[string]dbgen.ResolveSchedulesRow
	users        map[string]dbgen.ResolveUsersRow
	teams        map[string]dbgen.ResolveTeamsRow
	gateways     []dbgen.ListGatewaysRow
	destinations map[string]dbgen.ResolveDestinationsRow
}

// read reads what the steps of a save name: the Schedules FOR SHARE, after the chain's row (Lock order), the people,
// the Teams and the Destinations, deleted ones included, and, when a step calls, every Bridge Connection.
func (s *Service) read(ctx context.Context, q Queries, r references) (found, error) {
	f := found{schedules: map[string]dbgen.ResolveSchedulesRow{}, users: map[string]dbgen.ResolveUsersRow{},
		teams: map[string]dbgen.ResolveTeamsRow{}, destinations: map[string]dbgen.ResolveDestinationsRow{}}
	if len(r.schedules) > 0 {
		rows, err := q.ResolveSchedules(ctx, dbgen.ResolveSchedulesParams{OrgID: s.orgID, PublicIds: r.schedules})
		if err != nil {
			return f, fmt.Errorf("read the schedules of the steps: %w", err)
		}
		for _, x := range rows {
			f.schedules[x.PublicID] = x
		}
	}
	if len(r.users) > 0 {
		rows, err := q.ResolveUsers(ctx, dbgen.ResolveUsersParams{OrgID: s.orgID, PublicIds: r.users})
		if err != nil {
			return f, fmt.Errorf("read the people of the steps: %w", err)
		}
		for _, x := range rows {
			f.users[x.PublicID] = x
		}
	}
	if len(r.teams) > 0 {
		rows, err := q.ResolveTeams(ctx, dbgen.ResolveTeamsParams{OrgID: s.orgID, PublicIds: r.teams})
		if err != nil {
			return f, fmt.Errorf("read the teams of the steps: %w", err)
		}
		for _, x := range rows {
			f.teams[x.PublicID] = x
		}
	}
	if len(r.destinations) > 0 {
		rows, err := q.ResolveDestinations(ctx, dbgen.ResolveDestinationsParams{OrgID: s.orgID,
			PublicIds: r.destinations})
		if err != nil {
			return f, fmt.Errorf("read the destinations of the steps: %w", err)
		}
		for _, x := range rows {
			f.destinations[x.PublicID] = x
		}
	}
	if r.calls {
		rows, err := q.ListGateways(ctx, s.orgID)
		if err != nil {
			return f, fmt.Errorf("list the notification gateways: %w", err)
		}
		f.gateways = rows
	}
	return f, nil
}

// resolveSteps validates the steps of a save of a chain with the owning Team owner, whose stored steps are stored (nil
// on a creation), and returns them as written: at most MaxSteps, each of its shape (shape), and what each names known.
// A person a step adds — one no stored step names — who is a Viewer or disabled is not_pageable at their pointer
// (C-32.FR-3, as S-131 does for Rotation layers); a call step needs a Notification gateway (C-30.FR-9); a webhook step
// an outgoing webhook Destination in events mode or both. The people, Teams, gateways and Destinations the stored
// steps name pass whatever became of them, so that a chain can be saved again after a person was disabled or a gateway
// or a Destination deleted; the run skips such a step (C-32.FR-14).
func (s *Service) resolveSteps(ctx context.Context, q Queries, in []StepInput, owner *TeamRef, stored []Step) (
	[]stepRow, error) {
	if len(in) > MaxSteps {
		return nil, &FieldError{Pointer: "/steps", Code: CodeOutOfRange,
			Detail: fmt.Sprintf("A chain has at most %d steps.", MaxSteps)}
	}
	rows := make([]stepRow, len(in))
	for i, st := range in {
		shaped, err := shape(i, st, owner != nil)
		if err != nil {
			return nil, err
		}
		rows[i] = stepRow{Step: shaped}
	}
	f, err := s.read(ctx, q, referencesOf(in))
	if err != nil {
		return nil, err
	}
	k := keptOf(stored)
	for i, st := range in {
		if err := assign(i, st, &rows[i], f, k); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

// assign sets on row what the step input at index i names, as f read it, refusing what it may not name.
func assign(i int, in StepInput, row *stepRow, f found, k kept) error {
	if in.ScheduleID != nil {
		sc, ok := f.schedules[canonical(publicid.Schedule, *in.ScheduleID)]
		if !ok {
			return &FieldError{Pointer: pointerOf(i, fieldSchedule), Code: CodeUnknownID, Detail: "No such Schedule."}
		}
		row.scheduleID = sc.ID
		row.Schedule = &ScheduleRef{PublicID: sc.PublicID, Name: sc.Name, Team: TeamRef{PublicID: sc.TeamPublicID,
			Name: sc.TeamName, Slug: sc.TeamSlug, Deleted: sc.TeamDeleted}}
	}
	if in.UserIDs != nil {
		if err := assignUsers(i, *in.UserIDs, row, f, k); err != nil {
			return err
		}
	}
	if in.TeamID != nil {
		id := canonical(publicid.Team, *in.TeamID)
		t, ok := f.teams[id]
		if !ok || t.Deleted && !k.teams[id] {
			return &FieldError{Pointer: pointerOf(i, fieldTeam), Code: CodeUnknownID, Detail: "No such Team."}
		}
		row.teamID = t.ID
		row.Team = &TeamRef{ID: t.ID, PublicID: t.PublicID, Name: t.Name, Slug: t.Slug, Deleted: t.Deleted}
	}
	if in.Kind == KindCall {
		gw, err := callGateway(i, in.GatewayID, f.gateways, k)
		if err != nil {
			return err
		}
		row.gatewayID = gw.ID
		row.Gateway = &GatewayRef{PublicID: gw.PublicID, Name: gw.Name, Live: gw.Live}
	}
	if in.DestinationID != nil {
		id := canonical(publicid.Destination, *in.DestinationID)
		d, ok := f.destinations[id]
		switch {
		case !ok, d.Deleted && !k.destinations[id]:
			return &FieldError{Pointer: pointerOf(i, fieldDestination), Code: CodeUnknownID,
				Detail: "No such Destination."}
		case !k.destinations[id] && (d.Type != typeWebhook || d.WebhookMode.String == modeTemplate):
			return &FieldError{Pointer: pointerOf(i, fieldDestination), Code: CodeWebhookEventsRequired,
				Detail: "A webhook step sends to an outgoing webhook Destination in the mode events or both."}
		}
		row.destinationID = d.ID
		row.Destination = &DestinationTarget{PublicID: d.PublicID, Name: d.Name, Type: d.Type,
			Mode: d.WebhookMode.String, Deleted: d.Deleted}
	}
	return nil
}

// assignUsers sets the people of a page_people or call step: each once; one the stored steps name as stored, deleted
// ones included; any other a User who is not deleted (unknown_id) and may be Paged — neither disabled nor a Viewer
// (not_pageable with the reason).
func assignUsers(i int, ids []string, row *stepRow, f found, k kept) error {
	seen := map[string]bool{}
	for j, raw := range ids {
		pointer := pointerOf(i, fieldUsers) + "/" + strconv.Itoa(j)
		id := canonical(publicid.User, raw)
		u, ok := f.users[id]
		if !ok || u.Status == statusDeleted && !k.users[id] {
			return &FieldError{Pointer: pointer, Code: CodeUnknownID, Detail: "No such User."}
		}
		if seen[id] {
			return &FieldError{Pointer: pointer, Code: CodeDuplicate, Detail: "The person is named twice."}
		}
		seen[id] = true
		if !k.users[id] {
			switch {
			case u.Status == statusDisabled:
				return &FieldError{Pointer: pointer, Code: CodeNotPageable, Reason: ReasonDisabled,
					Detail: "The person is disabled and may not be Paged."}
			case u.Role == roleViewer:
				return &FieldError{Pointer: pointer, Code: CodeNotPageable, Reason: ReasonViewer,
					Detail: "The person is a Viewer and may not be Paged."}
			}
		}
		row.userIDs = append(row.userIDs, u.ID)
		row.Users = append(row.Users, UserRef{PublicID: u.PublicID, Name: u.Name, Login: u.Login, Role: u.Role,
			Status: u.Status})
	}
	return nil
}

// callGateway is the gateway of the call step at index i: without gateway_id the only Notification gateway, required
// while several exist; with it a Notification gateway, or the gateway a stored step names whatever became of it. A
// call while no Notification gateway exists is gateway_required at the step (C-30.FR-9).
func callGateway(i int, raw *string, all []dbgen.ListGatewaysRow, k kept) (dbgen.ListGatewaysRow, error) {
	var live []dbgen.ListGatewaysRow
	for _, g := range all {
		if g.Live {
			live = append(live, g)
		}
	}
	none := &FieldError{Pointer: pointerOf(i, ""), Code: CodeGatewayRequired,
		Detail: "A call needs a Notification gateway, a Bridge Connection in the role gateway; none exists."}
	if raw == nil {
		switch len(live) {
		case 0:
			return dbgen.ListGatewaysRow{}, none
		case 1:
			return live[0], nil
		}
		return dbgen.ListGatewaysRow{}, required(i, fieldGateway,
			"Several Notification gateways exist; name the one the call goes through.")
	}
	id := canonical(publicid.Connection, *raw)
	for _, g := range all {
		if g.PublicID == id && (g.Live || k.gateways[id]) {
			return g, nil
		}
	}
	if len(live) == 0 {
		return dbgen.ListGatewaysRow{}, none
	}
	return dbgen.ListGatewaysRow{}, &FieldError{Pointer: pointerOf(i, fieldGateway), Code: CodeUnknownID,
		Detail: "No Notification gateway has this id."}
}

func int8Of(id int64) pgtype.Int8 {
	return pgtype.Int8{Int64: id, Valid: id != 0}
}

func int4Of(n int) pgtype.Int4 {
	return pgtype.Int4{Int32: int32(n), Valid: n != 0} //nolint:gosec // G115: steps.go bounds every value
}

func textOf(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}

// writeSteps writes the steps of the chain chainID in their order, after deleting the stored ones on a replace.
func (s *Service) writeSteps(ctx context.Context, q Queries, chainID int64, rows []stepRow, replace bool) error {
	if replace {
		if err := q.DeleteSteps(ctx, dbgen.DeleteStepsParams{OrgID: s.orgID, ChainID: chainID}); err != nil {
			return fmt.Errorf("replace the steps of the escalation chain: %w", err)
		}
	}
	for _, r := range rows {
		id, err := q.InsertStep(ctx, dbgen.InsertStepParams{OrgID: s.orgID, ChainID: chainID,
			Position: int32(r.Number), Kind: r.Kind, Importance: textOf(r.Importance), //nolint:gosec // G115: at most MaxSteps
			ScheduleID: int8Of(r.scheduleID), TeamID: int8Of(r.teamID), Everyone: textOf(r.Everyone),
			CallTarget: textOf(r.CallTarget), GatewayConnectionID: int8Of(r.gatewayID),
			WaitSeconds: int4Of(r.WaitSeconds), DestinationID: int8Of(r.destinationID),
			RepeatFrom: int4Of(r.RepeatFrom), RepeatTimes: int4Of(r.RepeatTimes),
			WorkingHoursOnly: r.WorkingHoursOnly, SeverityCondition: cmp.Or(r.Condition, ConditionAny)})
		if err != nil {
			return fmt.Errorf("write step %d of the escalation chain: %w", r.Number, err)
		}
		if len(r.userIDs) == 0 {
			continue
		}
		if err := q.InsertStepUsers(ctx, dbgen.InsertStepUsersParams{OrgID: s.orgID, StepID: id,
			UserIds: r.userIDs}); err != nil {
			return fmt.Errorf("write the people of step %d of the escalation chain: %w", r.Number, err)
		}
	}
	return nil
}

// withSteps sets the steps of each chain in their order, with what they name.
func (s *Service) withSteps(ctx context.Context, q Queries, list []Chain) error {
	ids, index := chainIndex(list)
	rows, err := q.ListSteps(ctx, dbgen.ListStepsParams{OrgID: s.orgID, ChainIds: ids})
	if err != nil {
		return fmt.Errorf("list the steps of the escalation chains: %w", err)
	}
	stepIDs := make([]int64, 0, len(rows))
	where := make(map[int64][2]int, len(rows))
	for _, r := range rows {
		i, ok := index[r.ChainID]
		if !ok {
			continue
		}
		list[i].Steps = append(list[i].Steps, stepOf(r))
		stepIDs = append(stepIDs, r.ID)
		where[r.ID] = [2]int{i, len(list[i].Steps) - 1}
	}
	if len(stepIDs) == 0 {
		return nil
	}
	users, err := q.ListStepUsers(ctx, dbgen.ListStepUsersParams{OrgID: s.orgID, StepIds: stepIDs})
	if err != nil {
		return fmt.Errorf("list the people of the escalation steps: %w", err)
	}
	for _, u := range users {
		if at, ok := where[u.StepID]; ok {
			st := &list[at[0]].Steps[at[1]]
			st.Users = append(st.Users, UserRef{PublicID: u.PublicID, Name: u.Name, Login: u.Login, Role: u.Role,
				Status: u.Status})
		}
	}
	return nil
}

// stepOf is a step as a row of ListSteps gives it.
func stepOf(r dbgen.ListStepsRow) Step {
	st := Step{Number: int(r.Position), Kind: r.Kind, Importance: r.Importance.String, Everyone: r.Everyone.String,
		CallTarget: r.CallTarget.String, WaitSeconds: int(r.WaitSeconds.Int32), RepeatFrom: int(r.RepeatFrom.Int32),
		RepeatTimes: int(r.RepeatTimes.Int32), WorkingHoursOnly: r.WorkingHoursOnly, Condition: r.SeverityCondition}
	if r.SchedulePublicID.Valid {
		st.Schedule = &ScheduleRef{PublicID: r.SchedulePublicID.String, Name: r.ScheduleName.String,
			Team: TeamRef{PublicID: r.ScheduleTeamPublicID.String, Name: r.ScheduleTeamName.String,
				Slug: r.ScheduleTeamSlug.String, Deleted: r.ScheduleTeamDeleted}}
	}
	if r.TeamPublicID.Valid {
		st.Team = &TeamRef{PublicID: r.TeamPublicID.String, Name: r.TeamName.String, Slug: r.TeamSlug.String,
			Deleted: r.TeamDeleted}
	}
	if r.GatewayPublicID.Valid {
		st.Gateway = &GatewayRef{PublicID: r.GatewayPublicID.String, Name: r.GatewayName.String, Live: r.GatewayLive}
	}
	if r.DestinationPublicID.Valid {
		st.Destination = &DestinationTarget{PublicID: r.DestinationPublicID.String, Name: r.DestinationName.String,
			Type: r.DestinationType.String, Mode: r.DestinationMode.String, Deleted: r.DestinationDeleted}
	}
	return st
}

// WorkingHoursOf are the Working hours a step of the chain limited to Working hours uses (C-25.FR-9, C-32.FR-3): those
// of its owning Team — its own, else the Organization's, as teams.Effective says — and the Organization's for a chain
// without one.
func (s *Service) WorkingHoursOf(ctx context.Context, c Chain) (teams.WorkingHours, error) {
	o, err := s.store.GetOrganizationWorkingHours(ctx, s.orgID)
	if err != nil {
		return teams.WorkingHours{}, fmt.Errorf("read the working hours of the organization: %w", err)
	}
	org := teams.WorkingHours{Days: o.WorkingDays, Start: o.WorkdayStart, End: o.WorkdayEnd, TimeZone: o.TimeZone}
	if c.OwningTeam == nil {
		return org, nil
	}
	t, err := s.store.GetTeamWorkingHours(ctx, dbgen.GetTeamWorkingHoursParams{OrgID: s.orgID, ID: c.OwningTeam.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return org, nil
	}
	if err != nil {
		return teams.WorkingHours{}, fmt.Errorf("read the working hours of the team %s: %w", c.OwningTeam.PublicID, err)
	}
	var team teams.Team
	if t.OwnWorkingHours {
		team.Own = &teams.WorkingHours{Days: t.WorkingDays, Start: t.WorkingStart.String, End: t.WorkingEnd.String,
			TimeZone: t.WorkingTimeZone.String}
	}
	return teams.Effective(team, org), nil
}

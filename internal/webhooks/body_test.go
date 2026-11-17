// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package webhooks

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/jackc/pgx/v5/pgtype"

	spec "github.com/muster-io/muster/api"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/webhooks/dbgen"
)

// eventSchema is OutgoingWebhookEvent of the API specification.
func eventSchema(t *testing.T) *openapi3.Schema {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromData(spec.Spec())
	if err != nil {
		t.Fatal(err)
	}
	ref := doc.Components.Schemas["OutgoingWebhookEvent"]
	if ref == nil || ref.Value == nil {
		t.Fatal("no OutgoingWebhookEvent in the specification")
	}
	return ref.Value
}

// bodyFixture is an acknowledged, snoozed-before Alert Group with an Owner and two Alerts, one resolved.
func bodyFixture(f *fakeDB) delivery.EventSource {
	owner := int64(7)
	summary := "Error rate above 5%"
	resolved := t0.Add(-time.Minute)
	snooze := t0.Add(time.Hour)
	f.users = map[int64]dbgen.GetBodyUserRow{7: {PublicID: "SRAAAAAAAAAAA7", Name: "Alice Smith", Login: "alice"},
		3: {PublicID: "SRAAAAAAAAAAA3", Name: "Bob", Login: "bob"}}
	f.sas = map[int64]dbgen.GetBodyServiceAccountRow{4: {PublicID: "SAAAAAAAAAAAA4", Name: "ci"}}
	f.rows = []dbgen.ListBodyAlertsRow{
		{Fingerprint: "a1", Labels: []byte(`{"alertname":"HighErrorRate","job":"api"}`),
			Annotations: []byte(`{"summary":"x"}`), StartsAt: t0.Add(-time.Hour), State: "firing",
			GeneratorUrl: pgtype.Text{String: "http://prometheus/graph", Valid: true}},
		{Fingerprint: "b2", Labels: []byte(`{"alertname":"HighErrorRate"}`), Annotations: []byte(`{}`),
			StartsAt: t0.Add(-2 * time.Hour), EndedAt: pgtype.Timestamptz{Time: resolved, Valid: true},
			ResolveReason: pgtype.Text{String: "resolved", Valid: true}, State: "resolved"},
	}
	return delivery.EventSource{Group: &groups.Group{ID: 21, PublicID: "AGAAAAAAAAAA21", Number: 412,
		Title: "HighErrorRate api", Summary: &summary, Status: groups.StatusAcknowledged,
		Severity: organization.SeverityCritical, Urgent: true, OwnerUserID: &owner, SnoozeUntil: &snooze,
		ReopenCount: 1, CreatedAt: t0.Add(-time.Hour)}, RoutePublicID: "RTAAAAAAAAAAA1", RouteName: "payments",
		Actor: groups.Actor{Kind: audit.ActorUser, Transport: audit.TransportUI, Person: audit.User(3, "SRAAAAAAAAAAA3")}}
}

// TestBodyEveryRow is C-15.FR-2 and AC-11: the body of every row of the lifecycle event tables, as each actor makes it,
// is valid against OutgoingWebhookEvent: version 1, the event's name and loudness, its sequence, the actor, the Alert
// Group with its Owner and Route, and its Alerts.
func TestBodyEveryRow(t *testing.T) {
	schema := eventSchema(t)
	s, f := newService(t)
	in := bodyFixture(f)
	actors := map[string]groups.Actor{
		"user": in.Actor,
		"service_account": {Kind: audit.ActorServiceAccount, Transport: audit.TransportAPI,
			Person: audit.ServiceAccount(4, "SAAAAAAAAAAAA4").Via(9, "deploy")},
		"system": groups.System,
	}
	for name, actor := range actors {
		in.Actor = actor
		render, err := s.eventBodies(t.Context(), f, in)
		if err != nil {
			t.Fatal(err)
		}
		for i, row := range delivery.Table {
			loud := row.Loudness == groups.Loud
			var targets []mentions.Target
			if loud {
				targets = []mentions.Target{{Kind: mentions.TargetEveryone, Everyone: "all"},
					{Kind: mentions.TargetGroup, Group: "sre"},
					{Kind: mentions.TargetUser, User: &mentions.User{PublicID: "SRAAAAAAAAAAA7", Name: "Alice Smith",
						Login: "alice", Username: "alice.mm"}},
					{Kind: mentions.TargetUser}}
			}
			raw, err := render(delivery.EventBody{Event: groups.Recorded{Seq: int64(i + 1), Event: row.Event,
				Reason: "timer"}, Notify: loud, Mentions: targets, OccurredAt: t0})
			if err != nil {
				t.Fatal(err)
			}
			var v any
			if err := json.Unmarshal(raw, &v); err != nil {
				t.Fatal(err)
			}
			if err := schema.VisitJSON(v); err != nil {
				t.Fatalf("%s/%s: %v\n%s", name, row.Event, err, raw)
			}
			var b Body
			_ = json.Unmarshal(raw, &b)
			if b.Version != 1 || b.Event != string(row.Event) || b.Notify != loud || b.Sequence != int64(i+1) ||
				(loud && len(b.Mentions) != 3) || (!loud && len(b.Mentions) != 0) {
				t.Fatalf("%s/%s: %s", name, row.Event, raw)
			}
		}
	}
}

// TestBodyFields is C-15.FR-2 and FR-11: the body carries the Alert Group's state and links to its page, the actor
// with its login or token, and Mentions as data: everyone, a group by name, a User by id, name and login.
func TestBodyFields(t *testing.T) {
	s, f := newService(t)
	in := bodyFixture(f)
	render, err := s.eventBodies(t.Context(), f, in)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := render(delivery.EventBody{Event: groups.Recorded{Seq: 5, Event: groups.EventCreated}, Notify: true,
		Mentions: []mentions.Target{{Kind: mentions.TargetUser, User: &mentions.User{PublicID: "SRAAAAAAAAAAA7",
			Name: "Alice Smith", Login: "alice"}}}, OccurredAt: t0})
	if err != nil {
		t.Fatal(err)
	}
	var b Body
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	ag := b.AlertGroup
	if ag.Number != 412 || ag.ID != "AGAAAAAAAAAA21" || ag.Status != "acknowledged" || ag.Owner == nil ||
		ag.Owner.Login != "alice" || ag.Route.Name != "payments" || ag.SeverityLevel != "critical" || !ag.Urgent ||
		ag.URL != "https://muster.example.org/alert-groups/AGAAAAAAAAAA21" || ag.ReopenCount != 1 {
		t.Errorf("alert group %+v", ag)
	}
	if len(b.Alerts) != 2 || b.Alerts[1].ResolveReason == nil || b.Alerts[0].GeneratorURL == nil ||
		b.Alerts[0].Labels["job"] != "api" {
		t.Errorf("alerts %+v", b.Alerts)
	}
	if b.Actor.Kind != "user" || *b.Actor.Login != "bob" || b.Actor.Transport != "ui" || b.Actor.Reason != nil {
		t.Errorf("actor %+v", b.Actor)
	}
	if len(b.Mentions) != 1 || b.Mentions[0].Type != "user" || *b.Mentions[0].Login != "alice" {
		t.Errorf("mentions %s", raw)
	}

	in.Actor = groups.Actor{Kind: audit.ActorServiceAccount, Person: audit.ServiceAccount(4, "SA").Via(9, "deploy")}
	in.Group.OwnerUserID = nil
	render, _ = s.eventBodies(t.Context(), f, in)
	raw, _ = render(delivery.EventBody{Event: groups.Recorded{Seq: 6, Event: groups.EventResolved}, OccurredAt: t0})
	b = Body{}
	_ = json.Unmarshal(raw, &b)
	if b.Actor.Kind != "service_account" || *b.Actor.Name != "ci" || *b.Actor.TokenName != "deploy" ||
		b.Actor.Transport != "system" || b.AlertGroup.Owner != nil {
		t.Errorf("service account %s", raw)
	}
	in.Actor = groups.Actor{Kind: audit.ActorCLI}
	render, _ = s.eventBodies(t.Context(), f, in)
	raw, _ = render(delivery.EventBody{Event: groups.Recorded{Seq: 7, Event: groups.EventResolved,
		Reason: "owner_deleted"}, OccurredAt: t0})
	_ = json.Unmarshal(raw, &b)
	if b.Actor.Kind != "system" || b.Actor.Reason == nil || *b.Actor.Reason != "owner_deleted" {
		t.Errorf("system %s", raw)
	}

	for name, set := range map[string]func(){
		"owner":           func() { in.Group.OwnerUserID = new(int64(99)) },
		"alerts":          func() { f.fail["ListBodyAlerts"] = errBoom },
		"teams":           func() { f.fail["ListBodyTeams"] = errBoom },
		"user":            func() { in.Actor = groups.Actor{Kind: audit.ActorUser, Person: audit.User(99, "x")} },
		"service account": func() { in.Actor = groups.Actor{Kind: audit.ActorServiceAccount, Person: audit.User(99, "x")} },
	} {
		in = bodyFixture(f)
		f.fail = map[string]error{}
		set()
		if _, err := s.eventBodies(t.Context(), f, in); err == nil {
			t.Errorf("%s did not fail", name)
		}
	}
	if got := MentionTargets(nil); got == nil || len(got) != 0 {
		t.Errorf("no mentions %v", got)
	}
}

// TestMentionTargetTeams is C-15.FR-11 revised for L2: in outgoing webhook data a Team is a target of the type team
// with its slug and name — "Team from the label" one such target per Team of the Alert Group, as Resolve gives them —
// and the On-call of a Team a user target; two mentions of everyone are one target, which has only its type.
func TestMentionTargetTeams(t *testing.T) {
	got := MentionTargets([]mentions.Target{
		{Kind: mentions.TargetTeam, Team: &mentions.TeamTarget{Slug: "payments", Name: "Payments"}},
		{Kind: mentions.TargetTeam, Team: &mentions.TeamTarget{Slug: "search", Name: "Search"}},
		{Kind: mentions.TargetTeam},
		{Kind: mentions.TargetUser, User: &mentions.User{PublicID: "SRAAAAAAAAAAA7", Name: "Alice Smith",
			Login: "alice"}},
		{Kind: mentions.TargetEveryone, Everyone: "here"}, {Kind: mentions.TargetEveryone, Everyone: "channel"}})
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"type":"team","name":"Payments","slug":"payments"},{"type":"team","name":"Search","slug":"search"},` +
		`{"type":"user","id":"SRAAAAAAAAAAA7","name":"Alice Smith","login":"alice"},{"type":"everyone"}]`
	if string(b) != want {
		t.Errorf("targets %s, want %s", b, want)
	}
}

// bodyTeams are the Teams of the Alert Groups of the tests by id: payments and the deleted search for #412.
var bodyTeams = map[int64][]dbgen.ListBodyTeamsRow{21: {
	{PublicID: "TM0000000000PA", Name: "Payments", Slug: "payments"},
	{PublicID: "TM0000000000SE", Name: "Search", Slug: "search"},
}}

func (f *fakeDB) ListBodyTeams(_ context.Context, arg dbgen.ListBodyTeamsParams) ([]dbgen.ListBodyTeamsRow, error) {
	return bodyTeams[arg.AlertGroupID], f.fail["ListBodyTeams"]
}

// TestBodyTeams is C-15.FR-2 revised for L2 and C-09.FR-22: team_joined and unknown_team_value reach events mode one to
// one, Quiet, and the alert_group of every event carries teams — the id, name and slug of each Team in the order they
// joined, a deleted one by its name — and an empty list for an Alert Group without Teams.
func TestBodyTeams(t *testing.T) {
	schema := eventSchema(t)
	s, f := newService(t)
	in := bodyFixture(f)
	for _, c := range []struct {
		group int64
		want  string
	}{
		{21, `[{"id":"TM0000000000PA","name":"Payments","slug":"payments"},` +
			`{"id":"TM0000000000SE","name":"Search","slug":"search"}]`},
		{22, `[]`},
	} {
		in.Group.ID = c.group
		render, err := s.eventBodies(t.Context(), f, in)
		if err != nil {
			t.Fatal(err)
		}
		for i, e := range []groups.Event{groups.EventTeamJoined, groups.EventUnknownTeamValue} {
			raw, err := render(delivery.EventBody{Event: groups.Recorded{Seq: int64(i + 1), Event: e},
				OccurredAt: t0})
			if err != nil {
				t.Fatal(err)
			}
			var v any
			_ = json.Unmarshal(raw, &v)
			if err := schema.VisitJSON(v); err != nil {
				t.Fatalf("%s: %v\n%s", e, err, raw)
			}
			var b struct {
				Event      string `json:"event"`
				Notify     bool   `json:"notify"`
				AlertGroup struct {
					Teams json.RawMessage `json:"teams"`
				} `json:"alert_group"`
			}
			_ = json.Unmarshal(raw, &b)
			if b.Event != string(e) || b.Notify || string(b.AlertGroup.Teams) != c.want {
				t.Errorf("#%d %s: %s", c.group, e, raw)
			}
		}
	}
}

// TestBodyDestinationJoined is C-15.FR-2 revised by C-26.FR-5: destination_joined carries the Destination the Alert
// Group joined in its destination {id, name, type}, valid against OutgoingWebhookEvent, Loud with its Mentions while
// it fires; no other event carries a destination, even one recorded with a Destination by mistake.
func TestBodyDestinationJoined(t *testing.T) {
	schema := eventSchema(t)
	s, f := newService(t)
	render, err := s.eventBodies(t.Context(), f, bodyFixture(f))
	if err != nil {
		t.Fatal(err)
	}
	dest := &groups.JoinedDestination{ID: 9, PublicID: "DSAAAAAAAAAAA9", Name: "search-notice", Type: "mattermost"}
	for _, c := range []struct {
		event groups.Event
		want  string
	}{
		{groups.EventDestinationJoined, `{"id":"DSAAAAAAAAAAA9","name":"search-notice","type":"mattermost"}`},
		{groups.EventAlertsAdded, ``},
	} {
		raw, err := render(delivery.EventBody{Event: groups.Recorded{Seq: 5, Event: c.event, Destination: dest},
			Notify: true, Mentions: []mentions.Target{{Kind: mentions.TargetEveryone, Everyone: "all"}},
			OccurredAt: t0})
		if err != nil {
			t.Fatal(err)
		}
		var v any
		_ = json.Unmarshal(raw, &v)
		if err := schema.VisitJSON(v); err != nil {
			t.Fatalf("%s: %v\n%s", c.event, err, raw)
		}
		var b struct {
			Event       string          `json:"event"`
			Notify      bool            `json:"notify"`
			Destination json.RawMessage `json:"destination"`
		}
		_ = json.Unmarshal(raw, &b)
		if b.Event != string(c.event) || !b.Notify || string(b.Destination) != c.want {
			t.Errorf("%s: %s", c.event, raw)
		}
	}
}

// TestBodyEscalationStepSkipped is C-15.FR-2 revised by C-32.FR-14: escalation_step_skipped carries the chain, the
// step and the reason in its escalation_step {chain, step, reason, users}, valid against OutgoingWebhookEvent, Quiet
// without Mentions, with the person of a skip of a person and a null chain once the chain was deleted; no other event
// carries it.
func TestBodyEscalationStepSkipped(t *testing.T) {
	schema := eventSchema(t)
	s, f := newService(t)
	render, err := s.eventBodies(t.Context(), f, bodyFixture(f))
	if err != nil {
		t.Fatal(err)
	}
	chain := &groups.ChainEntry{Run: 3, Chain: 5, ChainPublicID: "ECAAAAAAAAAAP5", ChainName: "payments", Step: 3}
	viewer := &groups.Page{Users: []groups.PagedUser{{ID: 4, PublicID: "SRAAAAAAAAAAP4", Name: "Vic", Login: "vic"}},
		Reason: groups.SkipViewer}
	for _, c := range []struct {
		event  groups.Event
		chain  *groups.ChainEntry
		reason string
		page   *groups.Page
		want   string
	}{
		{groups.EventEscalationStepSkipped, chain, groups.SkipSeverity, nil,
			`{"chain":{"id":"ECAAAAAAAAAAP5","name":"payments"},"step":3,"reason":"severity","users":[]}`},
		{groups.EventEscalationStepSkipped, &groups.ChainEntry{Run: 3, Step: 2}, groups.SkipViewer, viewer,
			`{"chain":null,"step":2,"reason":"viewer","users":[{"id":"SRAAAAAAAAAAP4","name":"Vic","login":"vic"}]}`},
		{groups.EventAlertsAdded, chain, "", nil, ``},
	} {
		raw, err := render(delivery.EventBody{Event: groups.Recorded{Seq: 6, Event: c.event, Loudness: groups.Quiet,
			Chain: c.chain, Reason: c.reason, Page: c.page}, OccurredAt: t0})
		if err != nil {
			t.Fatal(err)
		}
		var v any
		_ = json.Unmarshal(raw, &v)
		if err := schema.VisitJSON(v); err != nil {
			t.Fatalf("%s: %v\n%s", c.event, err, raw)
		}
		var b struct {
			Event      string          `json:"event"`
			Notify     bool            `json:"notify"`
			Mentions   []any           `json:"mentions"`
			Escalation json.RawMessage `json:"escalation_step"`
			Page       json.RawMessage `json:"page"`
		}
		_ = json.Unmarshal(raw, &b)
		if b.Event != string(c.event) || b.Notify || len(b.Mentions) != 0 || string(b.Escalation) != c.want ||
			len(b.Page) != 0 {
			t.Errorf("%s: %s", c.event, raw)
		}
	}
}

// TestBodyEscalationWebhook is C-32.FR-13, C-32.AC-9 and C-15.FR-2 revised: escalation_webhook carries the Alert Group
// as every event, the chain and the step in escalation_step with a null reason and no users, and the step's
// Destination in destination, valid against OutgoingWebhookEvent, Quiet without Mentions and with no reason of the
// actor; a deleted chain is null. No other event but destination_joined carries a destination.
func TestBodyEscalationWebhook(t *testing.T) {
	schema := eventSchema(t)
	s, f := newService(t)
	render, err := s.eventBodies(t.Context(), f, bodyFixture(f))
	if err != nil {
		t.Fatal(err)
	}
	ops := &groups.JoinedDestination{ID: 77, PublicID: "DSAAAAAAAAAAOB", Name: "ops-bot", Type: "webhook"}
	for _, c := range []struct {
		event       groups.Event
		chain       *groups.ChainEntry
		dest        *groups.JoinedDestination
		escalation  string
		destination string
	}{
		{groups.EventEscalationWebhook,
			&groups.ChainEntry{Run: 3, Chain: 5, ChainPublicID: "ECAAAAAAAAAADO", ChainName: "devops", Step: 1}, ops,
			`{"chain":{"id":"ECAAAAAAAAAADO","name":"devops"},"step":1,"reason":null,"users":[]}`,
			`{"id":"DSAAAAAAAAAAOB","name":"ops-bot","type":"webhook"}`},
		{groups.EventEscalationWebhook, &groups.ChainEntry{Run: 3, Step: 4}, ops,
			`{"chain":null,"step":4,"reason":null,"users":[]}`,
			`{"id":"DSAAAAAAAAAAOB","name":"ops-bot","type":"webhook"}`},
		{groups.EventEscalationStepSkipped, &groups.ChainEntry{Run: 3, Step: 2}, ops,
			`{"chain":null,"step":2,"reason":null,"users":[]}`, ``},
	} {
		raw, err := render(delivery.EventBody{Event: groups.Recorded{Seq: 7, Event: c.event, Loudness: groups.Quiet,
			Chain: c.chain, Destination: c.dest}, OccurredAt: t0})
		if err != nil {
			t.Fatal(err)
		}
		var v any
		_ = json.Unmarshal(raw, &v)
		if err := schema.VisitJSON(v); err != nil {
			t.Fatalf("%s: %v\n%s", c.event, err, raw)
		}
		var b struct {
			Event       string          `json:"event"`
			Notify      bool            `json:"notify"`
			Mentions    []any           `json:"mentions"`
			Sequence    int64           `json:"sequence"`
			Actor       map[string]any  `json:"actor"`
			AlertGroup  map[string]any  `json:"alert_group"`
			Alerts      []any           `json:"alerts"`
			Escalation  json.RawMessage `json:"escalation_step"`
			Destination json.RawMessage `json:"destination"`
			Page        json.RawMessage `json:"page"`
		}
		_ = json.Unmarshal(raw, &b)
		if b.Event != string(c.event) || b.Notify || len(b.Mentions) != 0 || b.Sequence != 7 ||
			string(b.Escalation) != c.escalation || string(b.Destination) != c.destination || len(b.Page) != 0 ||
			b.Actor["reason"] != nil || b.AlertGroup["id"] == nil || b.Alerts == nil {
			t.Errorf("%s: %s", c.event, raw)
		}
	}
}

// TestBodyPageNotDelivered is C-15.FR-2 revised by C-30.FR-7: page_not_delivered carries the Page in its page {users,
// gateway, reason}, valid against OutgoingWebhookEvent, Quiet without Mentions; no other event carries a page, even
// one recorded with a Page by mistake, and a Page by Mention has a null gateway.
func TestBodyPageNotDelivered(t *testing.T) {
	schema := eventSchema(t)
	s, f := newService(t)
	render, err := s.eventBodies(t.Context(), f, bodyFixture(f))
	if err != nil {
		t.Fatal(err)
	}
	call := &groups.Page{Users: []groups.PagedUser{{ID: 4, PublicID: "SRAAAAAAAAAAP4", Name: "Petr", Login: "petr"}},
		Gateway: &groups.PageGateway{ID: 7, PublicID: "CNAAAAAAAAAAG7", Name: "Example Gateway"},
		Reason:  groups.PageCallRefused}
	mention := &groups.Page{Users: call.Users, Reason: groups.PageCallUnknown}
	for _, c := range []struct {
		event groups.Event
		page  *groups.Page
		want  string
	}{
		{groups.EventPageNotDelivered, call, `{"users":[{"id":"SRAAAAAAAAAAP4","name":"Petr","login":"petr"}],` +
			`"gateway":{"id":"CNAAAAAAAAAAG7","name":"Example Gateway"},"reason":"call_refused"}`},
		{groups.EventPageNotDelivered, mention, `{"users":[{"id":"SRAAAAAAAAAAP4","name":"Petr","login":"petr"}],` +
			`"gateway":null,"reason":"call_unknown"}`},
		{groups.EventAlertsAdded, call, ``},
	} {
		raw, err := render(delivery.EventBody{Event: groups.Recorded{Seq: 6, Event: c.event, Loudness: groups.Quiet,
			Page: c.page}, OccurredAt: t0})
		if err != nil {
			t.Fatal(err)
		}
		var v any
		_ = json.Unmarshal(raw, &v)
		if err := schema.VisitJSON(v); err != nil {
			t.Fatalf("%s: %v\n%s", c.event, err, raw)
		}
		var b struct {
			Event    string          `json:"event"`
			Notify   bool            `json:"notify"`
			Mentions []any           `json:"mentions"`
			Page     json.RawMessage `json:"page"`
		}
		_ = json.Unmarshal(raw, &b)
		if b.Event != string(c.event) || b.Notify || len(b.Mentions) != 0 || string(b.Page) != c.want {
			t.Errorf("%s: %s", c.event, raw)
		}
	}
}

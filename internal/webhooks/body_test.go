// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package webhooks

import (
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

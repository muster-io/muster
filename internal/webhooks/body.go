// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package webhooks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/webhooks/dbgen"
)

// BodyVersion is the version of the body of events-mode requests (C-15.FR-2): new event names and new fields are added
// within it; removing a field or changing its meaning needs a new version.
const BodyVersion = 1

// Body is the body of an events-mode request (OutgoingWebhookEvent), rendered when the event is queued, so that a
// retry hours later still carries the state the event described.
type Body struct {
	Version    int             `json:"version"`
	Event      string          `json:"event"`
	Notify     bool            `json:"notify"`
	Mentions   []MentionTarget `json:"mentions"`
	Sequence   int64           `json:"sequence"`
	OccurredAt time.Time       `json:"occurred_at"`
	Actor      Actor           `json:"actor"`
	AlertGroup AlertGroup      `json:"alert_group"`
	Alerts     []Alert         `json:"alerts"`
	// Test is set only on the test event of a Destination test (C-16.FR-1), which belongs to no Alert Group's order.
	Test bool `json:"test,omitempty"`
}

// Actor is who made the change of an event (WebhookActor): a User or a Service account with the token and the
// Transport, or Muster itself (`system`) with the reason.
type Actor struct {
	Kind      string  `json:"kind"`
	ID        *string `json:"id,omitempty"`
	Name      *string `json:"name,omitempty"`
	Login     *string `json:"login,omitempty"`
	TokenName *string `json:"token_name,omitempty"`
	Transport string  `json:"transport"`
	Reason    *string `json:"reason,omitempty"`
}

// User is a User as the body names them (WebhookUser).
type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Login string `json:"login"`
}

// RouteRef names the Route of an Alert Group (EntityRef).
type RouteRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AlertGroup is the state of the Alert Group when the event was recorded (WebhookAlertGroup).
type AlertGroup struct {
	Number        int64      `json:"number"`
	ID            string     `json:"id"`
	Title         string     `json:"title"`
	Summary       *string    `json:"summary"`
	Status        string     `json:"status"`
	Owner         *User      `json:"owner,omitempty"`
	Route         RouteRef   `json:"route"`
	Urgent        bool       `json:"urgent"`
	SeverityLevel string     `json:"severity_level"`
	StartedAt     time.Time  `json:"started_at"`
	ResolvedAt    *time.Time `json:"resolved_at"`
	SnoozeUntil   *time.Time `json:"snooze_until"`
	ReopenCount   int64      `json:"reopen_count"`
	URL           string     `json:"url"`
}

// Alert is one Alert of the Alert Group, by fingerprint (WebhookAlert).
type Alert struct {
	Fingerprint   string            `json:"fingerprint"`
	Status        string            `json:"status"`
	Labels        map[string]string `json:"labels"`
	Annotations   map[string]string `json:"annotations"`
	StartsAt      time.Time         `json:"starts_at"`
	ResolvedAt    *time.Time        `json:"resolved_at"`
	ResolveReason *string           `json:"resolve_reason"`
	GeneratorURL  *string           `json:"generator_url"`
}

// bodyQueries are the queries the bodies read through the transaction of the change.
type bodyQueries interface {
	GetBodyUser(ctx context.Context, arg dbgen.GetBodyUserParams) (dbgen.GetBodyUserRow, error)
	GetBodyServiceAccount(ctx context.Context, arg dbgen.GetBodyServiceAccountParams) (
		dbgen.GetBodyServiceAccountRow, error)
	ListBodyAlerts(ctx context.Context, arg dbgen.ListBodyAlertsParams) ([]dbgen.ListBodyAlertsRow, error)
}

var _ delivery.EventBodies = (*Service)(nil)

// EventBodies reads, through the transaction tx of a change, the Alert Group of in as its events carry it — its state
// after the change, its Owner, its Route and its Alerts — and the actor of the change, and returns the function that
// renders the body of each of its events (C-15.FR-2).
func (s *Service) EventBodies(ctx context.Context, tx groups.DBTX, in delivery.EventSource) (delivery.BodyFunc,
	error) {
	return s.eventBodies(ctx, dbgen.New(tx), in)
}

func (s *Service) eventBodies(ctx context.Context, q bodyQueries, in delivery.EventSource) (delivery.BodyFunc,
	error) {
	g := in.Group
	ag := AlertGroup{Number: g.Number, ID: g.PublicID, Title: g.Title, Summary: g.Summary, Status: string(g.Status),
		Route: RouteRef{ID: in.RoutePublicID, Name: in.RouteName}, Urgent: g.Urgent,
		SeverityLevel: string(g.Severity), StartedAt: g.CreatedAt.UTC(), ResolvedAt: utc(g.ResolvedAt),
		SnoozeUntil: utc(g.SnoozeUntil), ReopenCount: g.ReopenCount,
		URL: strings.TrimSuffix(s.cfg.PublicURL, "/") + "/alert-groups/" + g.PublicID}
	if g.OwnerUserID != nil {
		u, err := q.GetBodyUser(ctx, dbgen.GetBodyUserParams{OrgID: s.orgID, ID: *g.OwnerUserID})
		if err != nil {
			return nil, fmt.Errorf("read the owner of alert group #%d: %w", g.Number, err)
		}
		ag.Owner = &User{ID: u.PublicID, Name: u.Name, Login: u.Login}
	}
	rows, err := q.ListBodyAlerts(ctx, dbgen.ListBodyAlertsParams{OrgID: s.orgID, AlertGroupID: g.ID})
	if err != nil {
		return nil, fmt.Errorf("read the alerts of alert group #%d: %w", g.Number, err)
	}
	alerts := make([]Alert, 0, len(rows))
	for _, r := range rows {
		a := Alert{Fingerprint: r.Fingerprint, Status: r.State, Labels: kv(r.Labels), Annotations: kv(r.Annotations),
			StartsAt: r.StartsAt.UTC()}
		if r.EndedAt.Valid {
			a.ResolvedAt = utc(&r.EndedAt.Time)
		}
		if r.ResolveReason.Valid {
			a.ResolveReason = &r.ResolveReason.String
		}
		if r.GeneratorUrl.Valid && r.GeneratorUrl.String != "" {
			a.GeneratorURL = &r.GeneratorUrl.String
		}
		alerts = append(alerts, a)
	}
	actor, err := s.actor(ctx, q, in.Actor)
	if err != nil {
		return nil, err
	}
	return func(e delivery.EventBody) ([]byte, error) {
		a := actor
		if a.Kind == string(audit.ActorSystem) && e.Event.Reason != "" {
			reason := e.Event.Reason
			a.Reason = &reason
		}
		b := Body{Version: BodyVersion, Event: string(e.Event.Event), Notify: e.Notify,
			Mentions: MentionTargets(e.Mentions), Sequence: e.Event.Seq, OccurredAt: e.OccurredAt.UTC(), Actor: a,
			AlertGroup: ag, Alerts: alerts}
		if !e.Notify {
			b.Mentions = []MentionTarget{}
		}
		return json.Marshal(b)
	}, nil
}

// actor is the actor of a change as the body names it: a User with their login, a Service account with its token, or
// Muster itself.
func (s *Service) actor(ctx context.Context, q bodyQueries, a groups.Actor) (Actor, error) {
	out := Actor{Kind: string(a.Kind), Transport: string(a.Transport)}
	if out.Transport == "" {
		out.Transport = string(audit.TransportSystem)
	}
	if a.Person.TokenName != "" {
		name := a.Person.TokenName
		out.TokenName = &name
	}
	switch a.Kind {
	case audit.ActorUser:
		u, err := q.GetBodyUser(ctx, dbgen.GetBodyUserParams{OrgID: s.orgID, ID: a.Person.ID})
		if err != nil {
			return Actor{}, fmt.Errorf("read the user of the change: %w", err)
		}
		out.ID, out.Name, out.Login = &u.PublicID, &u.Name, &u.Login
	case audit.ActorServiceAccount:
		sa, err := q.GetBodyServiceAccount(ctx, dbgen.GetBodyServiceAccountParams{OrgID: s.orgID, ID: a.Person.ID})
		if err != nil {
			return Actor{}, fmt.Errorf("read the service account of the change: %w", err)
		}
		out.ID, out.Name = &sa.PublicID, &sa.Name
	default:
		out.Kind, out.TokenName = string(audit.ActorSystem), nil
	}
	return out, nil
}

// kv reads a JSON object of strings; anything else is empty.
func kv(raw []byte) map[string]string {
	out := map[string]string{}
	_ = json.Unmarshal(raw, &out) // the columns hold JSON objects of strings
	return out
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

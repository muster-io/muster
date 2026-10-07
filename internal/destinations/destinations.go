// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package destinations holds the Destinations (C-11.FR-18): Mattermost channels and Telegram channels reached through
// a Connection, and outgoing webhooks. This package reads every type with its health and Routes and exports
// muster_destination_info; the type capabilities add creating and editing them (C-13 to C-15) and S-035 their health
// changes. Secrets are read only as their status.
package destinations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/destinations/dbgen"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/publicid"
)

// ErrNotFound is a Destination that does not exist in the Organization or is deleted.
var ErrNotFound = errors.New("no such destination")

// Health is the health of a Destination: healthy, or broken since a time with a reason.
type Health struct {
	State  string
	Since  *time.Time
	Reason *string
}

// Ref names a Destination with its type and health (DestinationRef).
type Ref struct {
	PublicID string
	Name     string
	Type     string
	Health   Health
}

// RouteRef names a Route of a Destination.
type RouteRef struct {
	PublicID string
	Name     string
}

// Proxy is the proxy of an outgoing webhook as stored, without its password; PasswordSet and PasswordUpdatedAt are
// the password's status.
type Proxy struct {
	Enabled           bool       `json:"enabled"`
	Type              *string    `json:"type,omitempty"`
	Address           *string    `json:"address,omitempty"`
	Username          *string    `json:"username,omitempty"`
	PasswordSet       bool       `json:"-"`
	PasswordUpdatedAt *time.Time `json:"-"`
}

// SigningSecret is the status of the Signing secret of an outgoing webhook.
type SigningSecret struct {
	Set                 bool
	UpdatedAt           *time.Time
	PreviousActiveSince *time.Time
}

// TemplateError is the template error state of an outgoing webhook request template.
type TemplateError struct {
	Since time.Time
	Error string
}

// Destination is a Destination of any type as read. The fields of the other types are empty: Mattermost has its
// team and channel, Telegram its channel and the discussion group a Destination check found, an outgoing webhook its
// mode, request templates, proxy and Signing secret. Mentions, EventsConfig and TemplateConfig are the stored JSON.
type Destination struct {
	ID         int64
	PublicID   string
	Type       string
	Name       string
	Connection *string

	MattermostTeamID      *string
	MattermostChannelID   *string
	MattermostTeamName    *string
	MattermostChannelName *string

	TelegramChannelID            *string
	TelegramDiscussionGroupID    *string
	TelegramChannelTitle         *string
	TelegramDiscussionGroupTitle *string

	WebhookMode    *string
	EventsConfig   json.RawMessage
	TemplateConfig json.RawMessage
	Proxy          Proxy
	SigningSecret  SigningSecret

	Mentions          json.RawMessage
	LimiterLimit      int64
	LimiterPerSeconds int64
	Health            Health
	Routes            []RouteRef
	TemplateError     *TemplateError
	CreatedAt         time.Time
	Version           int64
}

// Page is a page of Destinations; Next is the cursor after its last one, nil on the last page.
type Page struct {
	Destinations []Destination
	Next         *int64
}

// ListFilter selects the Destinations of a page: of a type, a health and a Route (its public_id) when set, after the
// id After, at most Limit.
type ListFilter struct {
	Type   *string
	Health *string
	Route  *string
	After  *int64
	Limit  int
}

// Store are the queries of the package.
type Store interface {
	ListDestinations(ctx context.Context, arg dbgen.ListDestinationsParams) ([]dbgen.ListDestinationsRow, error)
	GetDestination(ctx context.Context, arg dbgen.GetDestinationParams) (dbgen.GetDestinationRow, error)
	ListDestinationRoutes(ctx context.Context, arg dbgen.ListDestinationRoutesParams) (
		[]dbgen.ListDestinationRoutesRow, error)
	ListRouteDestinationRefs(ctx context.Context, arg dbgen.ListRouteDestinationRefsParams) (
		[]dbgen.ListRouteDestinationRefsRow, error)
	ListDestinationInfo(ctx context.Context, orgID int64) ([]dbgen.ListDestinationInfoRow, error)
}

// NewStore is the Store over the main pool.
func NewStore(pool *pgxpool.Pool) Store { return dbgen.New(pool) }

// Service reads the Destinations of an Organization.
type Service struct {
	orgID int64
	store Store

	mu   sync.Mutex
	info map[string]string
}

// New returns the Service of the Organization orgID.
func New(orgID int64, store Store) *Service {
	return &Service{orgID: orgID, store: store, info: map[string]string{}}
}

// List reads a page of the Destinations that are not deleted, in id order, with their Routes.
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error) {
	limit := min(max(f.Limit, 1), 1000)
	p := dbgen.ListDestinationsParams{OrgID: s.orgID, PageSize: int32(limit) + 1,
		Type: text(f.Type), Health: text(f.Health), AfterID: nullInt(f.After)}
	if f.Route != nil {
		// A Route that cannot exist has no Destinations.
		id, ok := routeID(*f.Route)
		if !ok {
			return Page{Destinations: []Destination{}}, nil
		}
		p.Route = pgtype.Text{String: id, Valid: true}
	}
	rows, err := s.store.ListDestinations(ctx, p)
	if err != nil {
		return Page{}, fmt.Errorf("list the destinations: %w", err)
	}
	page := Page{Destinations: make([]Destination, 0, min(len(rows), limit))}
	for i, r := range rows {
		if i == limit {
			last := page.Destinations[limit-1].ID
			page.Next = &last
			break
		}
		page.Destinations = append(page.Destinations, destinationOf(dbgen.GetDestinationRow(r)))
	}
	if err := s.withRoutes(ctx, page.Destinations); err != nil {
		return Page{}, err
	}
	return page, nil
}

// Get reads the Destination publicID; a deleted or unknown one is ErrNotFound.
func (s *Service) Get(ctx context.Context, publicID string) (Destination, error) {
	id, err := publicid.Parse(publicid.Destination, publicID)
	if err != nil {
		return Destination{}, ErrNotFound
	}
	r, err := s.store.GetDestination(ctx, dbgen.GetDestinationParams{OrgID: s.orgID, PublicID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Destination{}, ErrNotFound
	}
	if err != nil {
		return Destination{}, fmt.Errorf("read the destination %s: %w", id, err)
	}
	list := []Destination{destinationOf(r)}
	if err := s.withRoutes(ctx, list); err != nil {
		return Destination{}, err
	}
	return list[0], nil
}

// withRoutes sets the Routes of each Destination.
func (s *Service) withRoutes(ctx context.Context, list []Destination) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]int64, len(list))
	for i, d := range list {
		ids[i] = d.ID
	}
	rows, err := s.store.ListDestinationRoutes(ctx, dbgen.ListDestinationRoutesParams{OrgID: s.orgID,
		DestinationIds: ids})
	if err != nil {
		return fmt.Errorf("list the routes of the destinations: %w", err)
	}
	by := map[int64][]RouteRef{}
	for _, r := range rows {
		by[r.DestinationID] = append(by[r.DestinationID], RouteRef{PublicID: r.PublicID, Name: r.Name})
	}
	for i := range list {
		list[i].Routes = by[list[i].ID]
		if list[i].Routes == nil {
			list[i].Routes = []RouteRef{}
		}
	}
	return nil
}

// RouteRefs are the Destinations that are not deleted of each of the Routes routeIDs, by name, with their health
// (C-08.FR-1).
func (s *Service) RouteRefs(ctx context.Context, routeIDs []int64) (map[int64][]Ref, error) {
	out := map[int64][]Ref{}
	if len(routeIDs) == 0 {
		return out, nil
	}
	rows, err := s.store.ListRouteDestinationRefs(ctx, dbgen.ListRouteDestinationRefsParams{OrgID: s.orgID,
		RouteIds: routeIDs})
	if err != nil {
		return nil, fmt.Errorf("list the destinations of the routes: %w", err)
	}
	for _, r := range rows {
		out[r.RouteID] = append(out[r.RouteID], Ref{PublicID: r.PublicID, Name: r.Name, Type: r.Type,
			Health: Health{State: r.Health, Since: timeOf(r.BrokenSince), Reason: textOf(r.BrokenReason)}})
	}
	return out, nil
}

// RefreshInfo sets muster_destination_info to the Destinations that are not deleted: a series per Destination with
// its current name; the series of a renamed or deleted one is removed.
func (s *Service) RefreshInfo(ctx context.Context) error {
	rows, err := s.store.ListDestinationInfo(ctx, s.orgID)
	if err != nil {
		return fmt.Errorf("list the destinations for their info metric: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	want := make(map[string]string, len(rows))
	for _, r := range rows {
		want[r.PublicID] = r.Name
	}
	for id, name := range s.info {
		if want[id] != name {
			metrics.DestinationInfo.Delete(id, name)
			delete(s.info, id)
		}
	}
	for id, name := range want {
		s.info[id] = name
		metrics.DestinationInfo.With(id, name).Set(1)
	}
	return nil
}

// RunInfo keeps muster_destination_info current until ctx ends: it refreshes it at once and at every tick, which
// also repairs a refresh that failed.
func (s *Service) RunInfo(ctx context.Context, ticks <-chan time.Time) {
	for {
		_ = s.RefreshInfo(ctx) // a failed refresh is repeated at the next tick
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
	}
}

// destinationOf is the Destination of a row.
func destinationOf(r dbgen.GetDestinationRow) Destination {
	d := Destination{ID: r.ID, PublicID: r.PublicID, Type: r.Type, Name: r.Name, Connection: textOf(r.ConnectionPublicID),
		MattermostTeamID: textOf(r.MattermostTeamID), MattermostChannelID: textOf(r.MattermostChannelID),
		MattermostTeamName: textOf(r.MattermostTeamName), MattermostChannelName: textOf(r.MattermostChannelName),
		TelegramChannelID: textOf(r.TelegramChannelID), TelegramChannelTitle: textOf(r.TelegramChannelTitle),
		TelegramDiscussionGroupTitle: textOf(r.TelegramDiscussionGroupTitle), WebhookMode: textOf(r.WebhookMode),
		EventsConfig: r.WebhookEventsConfig, TemplateConfig: r.WebhookTemplateConfig,
		SigningSecret: SigningSecret{Set: r.SigningSecretSet, UpdatedAt: timeOf(r.SigningSecretUpdatedAt),
			PreviousActiveSince: timeOf(r.PreviousSigningSecretSince)},
		Mentions: r.Mentions, LimiterLimit: r.LimiterLimit, LimiterPerSeconds: r.LimiterPerSeconds,
		Health:    Health{State: r.Health, Since: timeOf(r.BrokenSince), Reason: textOf(r.BrokenReason)},
		CreatedAt: r.CreatedAt.UTC(), Version: r.Version}
	if r.TelegramDiscussionChatID.Valid {
		id := fmt.Sprint(r.TelegramDiscussionChatID.Int64)
		d.TelegramDiscussionGroupID = &id
	}
	_ = json.Unmarshal(r.Proxy, &d.Proxy) // the CHECK keeps it an object; an unknown shape reads as disabled
	d.Proxy.PasswordSet, d.Proxy.PasswordUpdatedAt = r.ProxyPasswordSet, timeOf(r.ProxyPasswordUpdatedAt)
	if r.TemplateErrorSince.Valid {
		d.TemplateError = &TemplateError{Since: r.TemplateErrorSince.Time.UTC(), Error: r.TemplateError.String}
	}
	return d
}

// routeID is the canonical public_id of a Route, false when s cannot name one.
func routeID(s string) (string, bool) {
	id, err := publicid.Parse(publicid.Route, s)
	return id, err == nil
}

func timeOf(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	u := t.Time.UTC()
	return &u
}

func textOf(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func text(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *s, Valid: true}
}

func nullInt(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
}

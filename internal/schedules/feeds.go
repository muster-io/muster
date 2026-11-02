// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package schedules

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/schedules/dbgen"
	"github.com/muster-io/muster/internal/tokens"
	usersdb "github.com/muster-io/muster/internal/users/dbgen"
)

// The Audit log actions of the iCal feeds (C-27.FR-10), whose resource is their User.
const (
	ActionFeedIssued  = "ical_feed.issued"
	ActionFeedRevoked = "ical_feed.revoked"
)

// FeedBasePath is where the iCal feeds are served on the app listener: FeedBasePath, the token, then FeedSuffix.
const (
	FeedBasePath = "/ical/"
	FeedSuffix   = ".ics"
)

// ICalSpan is the span the feed reads: ical.window from midnight UTC of the day ICalWindowBack before the business
// clock's day to midnight UTC at the end of the day ICalWindowAhead after it, so a day more than MaxPeriod.
const ICalSpan = ICalWindowBack + ICalWindowAhead + 24*time.Hour

// ErrFeedNotFound is a token that names no iCal feed of an active User: malformed, unknown, revoked, or of a disabled
// or deleted User. It never says which.
var ErrFeedNotFound = errors.New("no such ical feed")

// ErrFeedUserInactive is an issue of a feed for a User who is not active, disabled or deleted meanwhile.
var ErrFeedUserInactive = errors.New("the user of the ical feed is not active")

// FeedQueries are the queries of the iCal feeds, with the insert of the Audit log.
type FeedQueries interface {
	ListUserSchedules(ctx context.Context, arg dbgen.ListUserSchedulesParams) ([]dbgen.ListUserSchedulesRow, error)
	ReplaceIcalFeed(ctx context.Context, arg dbgen.ReplaceIcalFeedParams) (bool, error)
	GetIcalFeed(ctx context.Context, arg dbgen.GetIcalFeedParams) (time.Time, error)
	DeleteIcalFeed(ctx context.Context, arg dbgen.DeleteIcalFeedParams) (int64, error)
	FindIcalFeed(ctx context.Context, tokenHash []byte) (dbgen.FindIcalFeedRow, error)
	audit.Store
}

// FeedStore runs the queries of the iCal feeds alone, in one transaction, or in the transaction of another package.
type FeedStore interface {
	FeedQueries
	InTx(ctx context.Context, f func(FeedQueries) error) error
	On(tx dbgen.DBTX) FeedQueries
}

// NewFeedStore is the FeedStore over the main pool.
func NewFeedStore(pool *pgxpool.Pool) FeedStore {
	return feedStore{pgQueries: newQueries(pool), pool: pool}
}

type feedStore struct {
	pgQueries
	pool *pgxpool.Pool
}

func (s feedStore) InTx(ctx context.Context, f func(FeedQueries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return f(newQueries(tx))
	})
}

func (s feedStore) On(tx dbgen.DBTX) FeedQueries { return newQueries(tx) }

// FeedsConfig is what Feeds need: the Organization, the Store, the Audit log writer, the business clock, which dates
// the feeds and places ical.window, the Schedules, whose Result the feed lists, and MUSTER_PUBLIC_URL, the base of the
// links.
type FeedsConfig struct {
	OrgID     int64
	Store     FeedStore
	Audit     *audit.Writer
	Business  clock.Clock
	Schedules *Service
	PublicURL string
}

// Feeds holds the personal iCal feeds of the Users of the Organization (C-27.FR-10): one per User, issued from the web
// session and shown once, its token (ical.token) stored only as its SHA-256 (ADR-0011).
type Feeds struct {
	orgID     int64
	store     FeedStore
	audit     *audit.Writer
	clock     clock.Clock
	schedules *Service
	publicURL string
	// host is the host of MUSTER_PUBLIC_URL, which ends the UIDs of the events.
	host string
}

// NewFeeds returns the Feeds of the Organization in cfg.
func NewFeeds(cfg FeedsConfig) *Feeds {
	host := "muster"
	if u, err := url.Parse(cfg.PublicURL); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	return &Feeds{orgID: cfg.OrgID, store: cfg.Store, audit: cfg.Audit, clock: cfg.Business, schedules: cfg.Schedules,
		publicURL: strings.TrimSuffix(cfg.PublicURL, "/"), host: host}
}

// FeedUser is the User of an iCal feed.
type FeedUser struct {
	ID       int64
	PublicID string
}

// Feed is whether a User has an iCal feed: IssuedAt is when it was issued, nil without one.
type Feed struct {
	IssuedAt *time.Time
}

// IssuedFeed is an iCal feed just issued: its link, which carries the token and is shown once, and when.
type IssuedFeed struct {
	URL      string
	IssuedAt time.Time
}

// Get reads whether the User userID has an iCal feed.
func (f *Feeds) Get(ctx context.Context, userID int64) (Feed, error) {
	at, err := f.store.GetIcalFeed(ctx, dbgen.GetIcalFeedParams{OrgID: f.orgID, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Feed{}, nil
	}
	if err != nil {
		return Feed{}, fmt.Errorf("read the ical feed: %w", err)
	}
	at = at.UTC()
	return Feed{IssuedAt: &at}, nil
}

// Issue issues the iCal feed of the User u, replacing the one they had in the same statement, so that the previous
// link stops working at once, and records ical_feed.issued without the token. A User who is not active, disabled or
// deleted meanwhile, gets ErrFeedUserInactive and no feed.
func (f *Feeds) Issue(ctx context.Context, r Requester, u FeedUser) (IssuedFeed, error) {
	value, hash, err := tokens.Generate(tokens.PrefixIcal)
	if err != nil {
		return IssuedFeed{}, err
	}
	now := f.clock.Now().UTC()
	err = f.store.InTx(ctx, func(q FeedQueries) error {
		replaced, err := q.ReplaceIcalFeed(ctx, dbgen.ReplaceIcalFeedParams{OrgID: f.orgID, UserID: u.ID,
			TokenHash: hash, Now: now})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrFeedUserInactive
		}
		if err != nil {
			return fmt.Errorf("issue the ical feed: %w", err)
		}
		return f.record(ctx, q, r.Actor, r.Transport, r.Address, ActionFeedIssued, u,
			map[string]any{"replaced": replaced})
	})
	if err != nil {
		return IssuedFeed{}, err
	}
	return IssuedFeed{URL: f.publicURL + FeedBasePath + value + FeedSuffix, IssuedAt: now}, nil
}

// Revoke deletes the iCal feed of the User u, whose link stops working at once, and records ical_feed.revoked; without
// a feed it does nothing.
func (f *Feeds) Revoke(ctx context.Context, r Requester, u FeedUser) error {
	return f.store.InTx(ctx, func(q FeedQueries) error {
		return f.revoke(ctx, q, r.Actor, r.Transport, r.Address, u, "revoked")
	})
}

// RemoveUserFeed deletes the iCal feed of a deleted User in the transaction tx of the deletion, recorded as
// ical_feed.revoked by the actor of the deletion with the reason user_deleted. It is the users package's FeedRemover;
// it has nothing to run once tx committed.
func (f *Feeds) RemoveUserFeed(ctx context.Context, tx usersdb.DBTX, userID int64, userPublicID string,
	actor audit.Actor, t audit.Transport, addr netip.Addr) (func(context.Context), error) {
	return nil, f.revoke(ctx, f.store.On(tx), actor, t, addr, FeedUser{ID: userID, PublicID: userPublicID},
		"user_deleted")
}

func (f *Feeds) revoke(ctx context.Context, q FeedQueries, actor audit.Actor, t audit.Transport, addr netip.Addr,
	u FeedUser, reason string) error {
	n, err := q.DeleteIcalFeed(ctx, dbgen.DeleteIcalFeedParams{OrgID: f.orgID, UserID: u.ID})
	if err != nil {
		return fmt.Errorf("revoke the ical feed of %s: %w", u.PublicID, err)
	}
	if n == 0 {
		return nil
	}
	return f.record(ctx, q, actor, t, addr, ActionFeedRevoked, u, map[string]any{"reason": reason})
}

func (f *Feeds) record(ctx context.Context, q FeedQueries, actor audit.Actor, t audit.Transport, addr netip.Addr,
	action string, u FeedUser, details map[string]any) error {
	return f.audit.Record(ctx, q, audit.Entry{OrgID: f.orgID, Actor: actor, Transport: t, Action: action,
		Resource: audit.Resource{Type: audit.ResourceUser, PublicID: u.PublicID}, Details: details,
		SourceAddress: addr})
}

// Lookup finds the User of the iCal feed whose token is value: an active User of the Organization, else
// ErrFeedNotFound, with the User when the feed exists but its User is disabled or deleted.
func (f *Feeds) Lookup(ctx context.Context, value string) (FeedUser, error) {
	if !tokens.WellFormed(tokens.PrefixIcal, value) {
		return FeedUser{}, ErrFeedNotFound
	}
	row, err := f.store.FindIcalFeed(ctx, tokens.Hash(value))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return FeedUser{}, ErrFeedNotFound
	case err != nil:
		return FeedUser{}, fmt.Errorf("find the ical feed: %w", err)
	case row.OrgID != f.orgID:
		return FeedUser{}, ErrFeedNotFound
	}
	u := FeedUser{ID: row.UserID, PublicID: row.UserPublicID}
	if row.UserStatus != "active" {
		return u, ErrFeedNotFound
	}
	return u, nil
}

// Calendar is the iCal document of the feed of the User u (C-27.FR-10): one event per span of ical.window — from
// ICalWindowBack before the start of the business clock's day in UTC to the end of the day ICalWindowAhead after it —
// in which u is On-call in a Schedule of one of their Teams, the only Schedules in which they can be, by its Result.
func (f *Feeds) Calendar(ctx context.Context, u FeedUser) ([]byte, error) {
	// The window starts and ends at midnight UTC, so that an event it cuts keeps its start, and its UID, all day.
	now := f.clock.Now().UTC().Truncate(time.Second)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	from, to := day.Add(-ICalWindowBack), day.Add(-ICalWindowBack+ICalSpan)
	rows, err := f.store.ListUserSchedules(ctx, dbgen.ListUserSchedulesParams{OrgID: f.orgID, UserID: u.ID})
	if err != nil {
		return nil, fmt.Errorf("list the schedules of %s: %w", u.PublicID, err)
	}
	list := make([]Schedule, 0, len(rows))
	for _, r := range rows {
		list = append(list, Schedule{ID: r.ID, PublicID: r.PublicID, Name: r.Name, TimeZone: r.TimeZone, Main: r.Main,
			CreatedAt: r.CreatedAt.UTC(), Version: r.Version, Team: TeamRef{ID: r.TeamID, PublicID: r.TeamPublicID,
				Name: r.TeamName, Slug: r.TeamSlug, Deleted: r.TeamDeleted}})
	}
	s := f.schedules
	if err := s.withLayers(ctx, s.store, list); err != nil {
		return nil, err
	}
	var events []Event
	for _, sc := range list {
		res, err := s.result(ctx, s.store, sc, from, to)
		if err != nil {
			return nil, err
		}
		events = append(events, eventsOf(sc, res, u.ID)...)
	}
	slices.SortStableFunc(events, func(a, b Event) int { return a.Start.Compare(b.Start) })
	return Calendar(events, u.PublicID, f.host, now), nil
}

// eventsOf are the events of the User userID in the Result of the Schedule sc: each segment in which they are
// On-call, adjacent segments joined into one event, except that an Override of theirs is an event of its own span,
// joined only with the other segments of the same Override. Time in which someone else is On-call — an Override of
// another person, a higher Rotation layer — is not theirs and leaves a hole.
func eventsOf(sc Schedule, res Result, userID int64) []Event {
	var events []Event
	override := ""
	for _, seg := range res.Segments {
		if seg.User == nil || seg.User.ID != userID {
			continue
		}
		id := ""
		if seg.Source != nil && seg.Source.Kind == SourceOverride {
			id = seg.Source.Override
		}
		if n := len(events); n > 0 && events[n-1].End.Equal(seg.Start) && override == id {
			events[n-1].End = seg.End
			continue
		}
		override = id
		events = append(events, Event{Schedule: sc.PublicID, Team: sc.Team.Name, Name: sc.Name, Start: seg.Start,
			End: seg.End})
	}
	return events
}

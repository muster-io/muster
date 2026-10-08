// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/links"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/publicid"
)

// readQueries are the queries of the reads and of muster_alert_groups.
type readQueries interface {
	GetGroup(ctx context.Context, arg dbgen.GetGroupParams) (dbgen.GetGroupRow, error)
	GetGroupID(ctx context.Context, arg dbgen.GetGroupIDParams) (dbgen.GetGroupIDRow, error)
	ListIntegrationRefs(ctx context.Context, arg dbgen.ListIntegrationRefsParams) ([]dbgen.ListIntegrationRefsRow,
		error)
	ListUserRefs(ctx context.Context, arg dbgen.ListUserRefsParams) ([]dbgen.ListUserRefsRow, error)
	ListServiceAccountRefs(ctx context.Context, arg dbgen.ListServiceAccountRefsParams) (
		[]dbgen.ListServiceAccountRefsRow, error)
	ListGroupAlerts(ctx context.Context, arg dbgen.ListGroupAlertsParams) ([]dbgen.ListGroupAlertsRow, error)
	ListAlertGroupKeys(ctx context.Context, arg dbgen.ListAlertGroupKeysParams) ([]dbgen.ListAlertGroupKeysRow, error)
	ListTimelineDesc(ctx context.Context, arg dbgen.ListTimelineDescParams) ([]dbgen.ListTimelineDescRow, error)
	ListTimelineAsc(ctx context.Context, arg dbgen.ListTimelineAscParams) ([]dbgen.ListTimelineAscRow, error)
	ListTimelineNotes(ctx context.Context, arg dbgen.ListTimelineNotesParams) ([]dbgen.ListTimelineNotesRow, error)
	ListTimelineDeliveryEvents(ctx context.Context, arg dbgen.ListTimelineDeliveryEventsParams) (
		[]dbgen.ListTimelineDeliveryEventsRow, error)
	CountOpenGroupsByRoute(ctx context.Context, orgID int64) ([]dbgen.CountOpenGroupsByRouteRow, error)
	ListRoutePublicIDs(ctx context.Context, orgID int64) ([]string, error)
}

// The notices of the Alert Group page (AlertGroupNotice).
const (
	NoticeAlertsStillFiring             = "alerts_still_firing"
	NoticeReplacement                   = "replacement"
	NoticeFiringAgainAfterManualResolve = "firing_again_after_manual_resolve"
	NoticeDetailsRemoved                = "details_removed"
	NoticeNewerAlertGroupExists         = "newer_alert_group_exists"
)

// Ref names an entity by its public_id and name.
type Ref struct {
	PublicID string
	Name     string
}

// ActorRef is a User or a Service account as the API shows it; Deactivated when it was deleted.
type ActorRef struct {
	Kind        string
	PublicID    string
	Name        string
	Login       string
	Deactivated bool
	// id is the row id of a reference that actors has yet to name.
	id int64
}

// Resolution is who resolved an Alert Group: a person, or the system with the reason of its last Alert.
type Resolution struct {
	By         string
	Actor      *ActorRef
	Reason     *string
	ReasonCode *string
}

// Notice is a banner of the Alert Group page.
type Notice struct {
	Kind           string
	Count          *int64
	Label          *string
	ResolvedNumber *int64
	RetentionDays  *int64
	// Related is the newer open Alert Group of newer_alert_group_exists.
	Related *GroupRef
}

// View is an Alert Group as getAlertGroup reads it.
type View struct {
	PublicID          string
	Number            int64
	Title             string
	Summary           *string
	Status            Status
	Severity          organization.SeverityLevel
	Urgent            bool
	Route             Ref
	Integrations      []Ref
	StartedAt         time.Time
	LastChangedAt     time.Time
	ResolvedAt        *time.Time
	ReopenCount       int64
	FiringCount       int64
	ResolvedCount     int64
	Resolution        *Resolution
	GroupLabels       map[string]string
	CommonLabels      map[string]string
	CommonAnnotations map[string]string
	Notices           []Notice
	// DetailsRemoved is set once retention.alert_details passed since the resolution: the Alerts and the Timeline
	// are gone, the summary and the Notes stay (C-09.FR-16).
	DetailsRemoved bool
	// LabelValues are the values of the labels the list asked for that the Alerts share.
	LabelValues map[string]string
	// Owner is the Owner while acknowledged; SnoozeUntil, nil for no end, and SnoozedBy the Snooze while snoozed.
	Owner       *ActorRef
	SnoozeUntil *time.Time
	SnoozedBy   *ActorRef
	// Newer is, for an Alert Group a person resolved, the open Alert Group of its Route and key that takes part in
	// grouping (C-10.FR-7).
	Newer *GroupRef
	// Links are its links, computed on read (C-09.FR-14, C-12.FR-9).
	Links []Link
	// DeliveryProblem is set while a delivery of it is Not delivered or deleted in the messenger, waits for a Broken
	// Destination, or has a Thread not attached (C-13.FR-12).
	DeliveryProblem bool
	// ownerID is the Owner's id, stillFiring the Alerts still firing in it and routeDeleted whether its Route was
	// deleted, which allowed_commands needs.
	ownerID      int64
	stillFiring  int64
	routeDeleted bool
}

// Link is a link of an Alert Group: its kind, its name and its http(s) URL.
type Link = links.Link

// Linker computes the links of the Alert Group id on read (C-12.FR-9): its Link rules, runbook_url, dashboard_url and
// generatorURL; (*links.Service).ForGroup is the one of the runtime.
type Linker func(ctx context.Context, id int64) ([]Link, error)

// SetLinks fills the links of Get.
func (s *Service) SetLinks(l Linker) {
	s.links = l
}

// owned sets the Owner, the Snooze and the newer open Alert Group of a View from its row and the Users and Service
// accounts that refs read.
func (v *View) owned(r refs, owner, snoozedByUser, snoozedByAccount pgtype.Int8, snoozeUntil pgtype.Timestamptz,
	newerID string, newerNumber int64) {
	v.Owner = r.actor(owner, pgtype.Int8{})
	v.ownerID = owner.Int64
	if v.Status == StatusSnoozed {
		v.SnoozeUntil = timeOf(snoozeUntil)
		v.SnoozedBy = r.actor(snoozedByUser, snoozedByAccount)
	}
	if newerID != "" {
		v.Newer = &GroupRef{PublicID: newerID, Number: newerNumber}
	}
}

// detailsRemoved reports whether retention.alert_details, in days, passed at now since an Alert Group was resolved:
// the reads hide its details from then on, whether or not the Leader deleted them yet (design/db/schema.md §6).
func detailsRemoved(resolvedAt pgtype.Timestamptz, days int64, now time.Time) bool {
	return resolvedAt.Valid && resolvedAt.Time.Before(now.Add(-time.Duration(days)*24*time.Hour))
}

// groupID reads the id of the Alert Group publicID and whether its details are removed; an unknown or malformed one
// is ErrNotFound.
func (s *Service) groupID(ctx context.Context, publicID string) (int64, bool, error) {
	id, err := publicid.Parse(publicid.AlertGroup, publicID)
	if err != nil {
		return 0, false, ErrNotFound
	}
	r, err := s.store.GetGroupID(ctx, dbgen.GetGroupIDParams{OrgID: s.orgID, PublicID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, ErrNotFound
	}
	if err != nil {
		return 0, false, fmt.Errorf("read alert group %s: %w", id, err)
	}
	return r.ID, detailsRemoved(r.ResolvedAt, r.RetentionAlertDetailsDays, s.clock.Now()), nil
}

// Get reads the Alert Group publicID (C-09.FR-1, FR-10, FR-14): its status, Route, Integrations, title and summary,
// Severity level, urgency as its Route and organization.critical_is_urgent give it now, counts, the resolution, its
// Owner and Snooze, its labels, its links and the notices of its page — newer_alert_group_exists for a person-resolved one whose
// key another open Alert Group took (C-10.FR-7); once its details are removed, the notice details_removed with the
// period (C-09.FR-16).
func (s *Service) Get(ctx context.Context, publicID string) (View, error) {
	id, err := publicid.Parse(publicid.AlertGroup, publicID)
	if err != nil {
		return View{}, ErrNotFound
	}
	r, err := s.store.GetGroup(ctx, dbgen.GetGroupParams{OrgID: s.orgID, PublicID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return View{}, ErrNotFound
	}
	if err != nil {
		return View{}, fmt.Errorf("read alert group %s: %w", id, err)
	}
	v := View{PublicID: r.PublicID, Number: r.Number, Title: r.Title, Summary: textOf(r.Summary),
		Status: Status(r.Status), Severity: organization.SeverityLevel(r.SeverityLevel), Urgent: r.Urgent,
		Route: Ref{PublicID: r.RoutePublicID, Name: r.RouteName}, StartedAt: r.CreatedAt.UTC(),
		LastChangedAt: r.LastChangedAt.UTC(), ResolvedAt: timeOf(r.ResolvedAt), ReopenCount: r.ReopenCount,
		FiringCount: r.FiringAlertCount, ResolvedCount: r.ResolvedAlertCount, Integrations: []Ref{},
		Notices: []Notice{}, Links: []Link{}, DeliveryProblem: r.DeliveryProblem}
	for _, f := range []struct {
		raw  []byte
		into *map[string]string
	}{{r.GroupKeyValues, &v.GroupLabels}, {r.CommonLabels, &v.CommonLabels},
		{r.CommonAnnotations, &v.CommonAnnotations}} {
		if err := json.Unmarshal(f.raw, f.into); err != nil {
			return View{}, fmt.Errorf("read alert group %s: %w", id, err)
		}
	}
	if len(r.IntegrationIds) > 0 {
		refs, err := s.store.ListIntegrationRefs(ctx, dbgen.ListIntegrationRefsParams{OrgID: s.orgID,
			Ids: r.IntegrationIds})
		if err != nil {
			return View{}, fmt.Errorf("read the integrations of alert group %s: %w", id, err)
		}
		for _, ref := range refs {
			v.Integrations = append(v.Integrations, Ref{PublicID: ref.PublicID, Name: ref.Name})
		}
	}
	refs, err := s.refs(ctx, []int64{r.ResolvedByUserID.Int64, r.OwnerUserID.Int64, r.SnoozedByUserID.Int64},
		[]int64{r.ResolvedByServiceAccountID.Int64, r.SnoozedByServiceAccountID.Int64})
	if err != nil {
		return View{}, err
	}
	if r.ResolvedByKind.Valid {
		v.Resolution = &Resolution{By: r.ResolvedByKind.String, Reason: textOf(r.ResolveReasonText),
			ReasonCode: textOf(r.ResolveReason), Actor: refs.actor(r.ResolvedByUserID, r.ResolvedByServiceAccountID)}
	}
	v.owned(refs, r.OwnerUserID, r.SnoozedByUserID, r.SnoozedByServiceAccountID, r.SnoozeUntil, r.NewerPublicID,
		r.NewerNumber)
	v.stillFiring, v.routeDeleted = r.FiringAlertCount, r.RouteDeleted
	if r.ResolvedByKind.String == ResolvedByUser && r.StillFiring > 0 {
		v.Notices = append(v.Notices, Notice{Kind: NoticeAlertsStillFiring, Count: &r.StillFiring})
	}
	if r.ReplacedLabel != "" {
		v.Notices = append(v.Notices, Notice{Kind: NoticeReplacement, Label: &r.ReplacedLabel})
	}
	if r.FiringAgainAfterNumber > 0 {
		v.Notices = append(v.Notices, Notice{Kind: NoticeFiringAgainAfterManualResolve,
			ResolvedNumber: &r.FiringAgainAfterNumber})
	}
	if v.Newer != nil {
		v.Notices = append(v.Notices, Notice{Kind: NoticeNewerAlertGroupExists, Related: v.Newer})
	}
	if detailsRemoved(r.ResolvedAt, r.RetentionAlertDetailsDays, s.clock.Now()) {
		v.DetailsRemoved = true
		v.Notices = append(v.Notices, Notice{Kind: NoticeDetailsRemoved, RetentionDays: &r.RetentionAlertDetailsDays})
	}
	if s.links != nil {
		links, err := s.links(ctx, r.ID)
		if err != nil {
			return View{}, fmt.Errorf("compute the links of alert group %s: %w", id, err)
		}
		v.Links = append(v.Links, links...)
	}
	return v, nil
}

// refs are Users and Service accounts by id.
type refs struct {
	users    map[int64]ActorRef
	accounts map[int64]ActorRef
}

// refs reads the Users and Service accounts of the ids; zero ids are skipped.
func (s *Service) refs(ctx context.Context, userIDs, accountIDs []int64) (refs, error) {
	out := refs{users: map[int64]ActorRef{}, accounts: map[int64]ActorRef{}}
	userIDs = slices.DeleteFunc(slices.Clone(userIDs), func(id int64) bool { return id == 0 })
	accountIDs = slices.DeleteFunc(slices.Clone(accountIDs), func(id int64) bool { return id == 0 })
	if len(userIDs) > 0 {
		rows, err := s.store.ListUserRefs(ctx, dbgen.ListUserRefsParams{OrgID: s.orgID, Ids: userIDs})
		if err != nil {
			return out, fmt.Errorf("read users: %w", err)
		}
		for _, u := range rows {
			out.users[u.ID] = ActorRef{Kind: "user", PublicID: u.PublicID, Name: u.Name, Login: u.Login,
				Deactivated: u.Status == "deleted"}
		}
	}
	if len(accountIDs) > 0 {
		rows, err := s.store.ListServiceAccountRefs(ctx, dbgen.ListServiceAccountRefsParams{OrgID: s.orgID,
			Ids: accountIDs})
		if err != nil {
			return out, fmt.Errorf("read service accounts: %w", err)
		}
		for _, a := range rows {
			out.accounts[a.ID] = ActorRef{Kind: "service_account", PublicID: a.PublicID, Name: a.Name,
				Deactivated: a.Status == "deleted"}
		}
	}
	return out, nil
}

// actor is the User or Service account of one of the ids, nil when neither is known.
func (r refs) actor(user, account pgtype.Int8) *ActorRef {
	if a, ok := r.users[user.Int64]; ok && user.Valid {
		return &a
	}
	if a, ok := r.accounts[account.Int64]; ok && account.Valid {
		return &a
	}
	return nil
}

// AlertPosition is the place of an Alert in the list of an Alert Group: firing ones (rank 0) before the others.
type AlertPosition struct {
	Rank int
	ID   int64
}

// AlertFilter selects a page of the Alerts of an Alert Group: firing or resolved when State is set, after the
// position After when it is set.
type AlertFilter struct {
	State string
	After *AlertPosition
	Limit int
}

// GroupAlert is an Alert inside an Alert Group (AlertGroupAlert); an Alert that left a person-resolved Alert Group
// for another one shows as resolved there, without a reason.
type GroupAlert struct {
	Fingerprint string
	Integration Ref
	Labels      map[string]string
	Annotations map[string]string
	Firing      bool
	Reason      *string
	ReasonText  *string
	StartsAt    time.Time
	ResolvedAt  *time.Time
	LastSeenAt  time.Time
	GroupKeys   []string
	SourceURL   *string
}

// AlertPage is a page of the Alerts of an Alert Group; Next is nil on the last page.
type AlertPage struct {
	Alerts []GroupAlert
	Next   *AlertPosition
}

// Alerts lists the Alerts of the Alert Group publicID (C-09.FR-14, C-06.FR-19): firing first, then resolved with
// their reason, each with its Integration, labels, the annotations as last seen in it, the Alertmanager groups that
// listed it and its source link; none once its details are removed (C-09.FR-16).
func (s *Service) Alerts(ctx context.Context, publicID string, f AlertFilter) (AlertPage, error) {
	gid, removed, err := s.groupID(ctx, publicID)
	if err != nil {
		return AlertPage{}, err
	}
	if removed {
		return AlertPage{Alerts: []GroupAlert{}}, nil
	}
	p := dbgen.ListGroupAlertsParams{OrgID: s.orgID, AlertGroupID: gid, AllStates: f.State == "",
		Firing: f.State == string(StatusFiring), AfterRank: -1, Lim: int32(f.Limit + 1)} //nolint:gosec // G115: limit is at most the page size
	if f.After != nil {
		p.AfterRank, p.AfterID = int32(f.After.Rank), f.After.ID //nolint:gosec // G115: rank is 0 or 1
	}
	rows, err := s.store.ListGroupAlerts(ctx, p)
	if err != nil {
		return AlertPage{}, fmt.Errorf("list the alerts of alert group %s: %w", publicID, err)
	}
	out := AlertPage{Alerts: make([]GroupAlert, 0, min(len(rows), f.Limit))}
	if len(rows) > f.Limit {
		rows = rows[:f.Limit]
		last := rows[len(rows)-1]
		out.Next = &AlertPosition{Rank: rankOf(last.Ended), ID: last.ID}
	}
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.AlertID
	}
	keys := map[int64][]string{}
	if len(ids) > 0 {
		gk, err := s.store.ListAlertGroupKeys(ctx, dbgen.ListAlertGroupKeysParams{OrgID: s.orgID, AlertIds: ids})
		if err != nil {
			return AlertPage{}, fmt.Errorf("read the alertmanager groups of the alerts: %w", err)
		}
		for _, k := range gk {
			keys[k.AlertID] = append(keys[k.AlertID], k.GroupKey)
		}
	}
	for _, r := range rows {
		a := GroupAlert{Fingerprint: r.Fingerprint, Integration: Ref{PublicID: r.IntegrationPublicID,
			Name: r.IntegrationName}, Firing: !r.Ended, Reason: textOf(r.ResolveReason),
			ReasonText: textOf(r.ResolveReasonText), StartsAt: r.StartsAt.UTC(), ResolvedAt: timeOf(r.EndedAt),
			LastSeenAt: r.LastSeenAt.UTC(), GroupKeys: orEmpty(keys[r.AlertID]),
			SourceURL: textOf(r.GeneratorUrl)}
		if err := json.Unmarshal(r.Labels, &a.Labels); err != nil {
			return AlertPage{}, fmt.Errorf("read the labels of alert %s: %w", r.Fingerprint, err)
		}
		if err := json.Unmarshal(r.Annotations, &a.Annotations); err != nil {
			return AlertPage{}, fmt.Errorf("read the annotations of alert %s: %w", r.Fingerprint, err)
		}
		out.Alerts = append(out.Alerts, a)
	}
	return out, nil
}

func rankOf(ended bool) int {
	if ended {
		return 1
	}
	return 0
}

// The sources the Timeline merges, in the order they take at the same time.
const (
	sourceEntries  = 0
	sourceNotes    = 1
	sourceDelivery = 2
)

// TimelinePosition is the place of an entry in the Timeline: its time, its source and its id there.
type TimelinePosition struct {
	At     time.Time
	Source int
	ID     int64
}

func (p TimelinePosition) compare(o TimelinePosition) int {
	return cmp.Or(p.At.Compare(o.At), cmp.Compare(p.Source, o.Source), cmp.Compare(p.ID, o.ID))
}

// TimelineFilter selects a page of a Timeline: only the Kinds when set, oldest first when Ascending, after the
// position After when it is set.
type TimelineFilter struct {
	Kinds     []Kind
	Ascending bool
	After     *TimelinePosition
	Limit     int
}

// TimelineActor is who caused an entry and how.
type TimelineActor struct {
	Kind      string
	Ref       *ActorRef
	TokenName *string
	Transport string
	Reason    *string
}

// NoteView is a Note as its Timeline entry shows it.
type NoteView struct {
	PublicID  string
	Body      string
	Author    *ActorRef
	Transport string
	CreatedAt time.Time
}

// DeliveryView is a delivery event as the Timeline shows it.
type DeliveryView struct {
	Event       string
	Destination Ref
	Error       *string
}

// TimelineEntry is one entry of the Timeline (TimelineEntry): a lifecycle event or a system entry, a Note or a
// delivery event. Event is empty for a system entry without a lifecycle event.
type TimelineEntry struct {
	PublicID       string
	At             time.Time
	Kind           Kind
	Event          string
	Loudness       Loudness
	Mentions       []string
	System         string
	Actor          TimelineActor
	Reason         *string
	From, To       Status
	Owner          *ActorRef
	PreviousOwner  *ActorRef
	SnoozeUntil    *time.Time
	Fingerprints   []string
	ReplacedLabel  *string
	LabelConflicts []string
	NoticeNumber   *int64
	MissedCount    *int64
	PeriodFrom     *time.Time
	PeriodTo       *time.Time
	Detail         *string
	Note           *NoteView
	Delivery       *DeliveryView
	position       TimelinePosition
}

// TimelinePage is a page of a Timeline; Next is nil on the last page.
type TimelinePage struct {
	Entries []TimelineEntry
	Next    *TimelinePosition
}

// Timeline reads the Timeline of the Alert Group publicID (C-09.FR-11, FR-14): its entries with the Notes and the
// delivery events merged by time, newest first unless Ascending, filtered by kind, each with its actor and
// Transport and, for a lifecycle event, its event, loudness and Mentions. Once its details are removed only the Notes
// remain (C-09.FR-16).
func (s *Service) Timeline(ctx context.Context, publicID string, f TimelineFilter) (TimelinePage, error) {
	gid, removed, err := s.groupID(ctx, publicID)
	if err != nil {
		return TimelinePage{}, err
	}
	if removed {
		if len(f.Kinds) > 0 && !slices.Contains(f.Kinds, KindNotes) {
			return TimelinePage{Entries: []TimelineEntry{}}, nil
		}
		f.Kinds = []Kind{KindNotes}
	}
	want := func(k Kind) bool { return len(f.Kinds) == 0 || slices.Contains(f.Kinds, k) }
	var stored []string
	for _, k := range f.Kinds {
		if k != KindNotes && k != KindDelivery {
			stored = append(stored, string(k))
		}
	}
	lim := int32(f.Limit + 1) //nolint:gosec // G115: limit is at most the page size
	at, source, after := pgtype.Timestamptz{}, int32(0), int64(0)
	if f.After != nil {
		at = pgtype.Timestamptz{Time: f.After.At, Valid: true}
		source, after = int32(f.After.Source), f.After.ID //nolint:gosec // G115: a source is 0, 1 or 2
	}
	var all []TimelineEntry
	if len(f.Kinds) == 0 || len(stored) > 0 {
		entries, err := s.entries(ctx, gid, f.Ascending, stored, at, source, after, lim)
		if err != nil {
			return TimelinePage{}, err
		}
		all = append(all, entries...)
	}
	if want(KindNotes) {
		notes, err := s.notes(ctx, gid, !f.Ascending, at, source, after, lim)
		if err != nil {
			return TimelinePage{}, err
		}
		all = append(all, notes...)
	}
	if want(KindDelivery) {
		events, err := s.deliveryEvents(ctx, gid, !f.Ascending, at, source, after, lim)
		if err != nil {
			return TimelinePage{}, err
		}
		all = append(all, events...)
	}
	slices.SortFunc(all, func(a, b TimelineEntry) int {
		if f.Ascending {
			return a.position.compare(b.position)
		}
		return b.position.compare(a.position)
	})
	out := TimelinePage{Entries: all}
	if len(all) > f.Limit {
		out.Entries = all[:f.Limit]
		next := out.Entries[f.Limit-1].position
		out.Next = &next
	}
	if err := s.actors(ctx, out.Entries); err != nil {
		return TimelinePage{}, err
	}
	return out, nil
}

// entryRow is a row of either order of the Timeline entries.
type entryRow = dbgen.ListTimelineDescRow

// entries reads the Timeline entries of an Alert Group in one direction.
func (s *Service) entries(ctx context.Context, gid int64, asc bool, kinds []string, at pgtype.Timestamptz,
	source int32, after int64, lim int32) ([]TimelineEntry, error) {
	if kinds == nil {
		kinds = []string{}
	}
	var rows []entryRow
	if asc {
		asc, err := s.store.ListTimelineAsc(ctx, dbgen.ListTimelineAscParams{OrgID: s.orgID, AlertGroupID: gid,
			Kinds: kinds, At: at, Source: source, ID: after, Lim: lim})
		if err != nil {
			return nil, fmt.Errorf("read the timeline: %w", err)
		}
		for _, r := range asc {
			rows = append(rows, entryRow(r))
		}
	} else {
		var err error
		if rows, err = s.store.ListTimelineDesc(ctx, dbgen.ListTimelineDescParams{OrgID: s.orgID,
			AlertGroupID: gid, Kinds: kinds, At: at, Source: source, ID: after, Lim: lim}); err != nil {
			return nil, fmt.Errorf("read the timeline: %w", err)
		}
	}
	out := make([]TimelineEntry, 0, len(rows))
	for _, r := range rows {
		e := TimelineEntry{PublicID: r.PublicID, At: r.At.UTC(), Kind: Kind(r.Kind), Event: r.Event.String,
			Loudness: Loudness(r.Loudness.String), System: r.SystemEvent.String,
			Actor: TimelineActor{Kind: r.ActorKind, TokenName: textOf(r.TokenName), Transport: r.Transport,
				Reason: textOf(r.Reason)},
			Reason: textOf(r.Reason), From: Status(r.FromStatus.String), To: Status(r.ToStatus.String),
			SnoozeUntil: timeOf(r.SnoozeUntil), Fingerprints: r.Fingerprints, ReplacedLabel: textOf(r.ReplacedLabel),
			LabelConflicts: r.LabelConflicts, NoticeNumber: int8Of(r.NoticeNumber),
			MissedCount: int8Of(r.MissedCount), PeriodFrom: timeOf(r.PeriodFrom), PeriodTo: timeOf(r.PeriodTo),
			Detail: textOf(r.Detail), position: TimelinePosition{At: r.At.UTC(), Source: sourceEntries, ID: r.ID}}
		if r.Event.Valid {
			e.Mentions = orEmpty(r.Mentions)
		}
		e.Owner, e.PreviousOwner = userRef(r.OwnerUserID), userRef(r.PreviousOwnerUserID)
		e.Actor.Ref = pendingRef(r.ActorUserID, r.ActorServiceAccountID)
		out = append(out, e)
	}
	return out, nil
}

// notes reads the Notes of an Alert Group for the Timeline.
func (s *Service) notes(ctx context.Context, gid int64, desc bool, at pgtype.Timestamptz, source int32, after int64,
	lim int32) ([]TimelineEntry, error) {
	rows, err := s.store.ListTimelineNotes(ctx, dbgen.ListTimelineNotesParams{OrgID: s.orgID, AlertGroupID: gid,
		At: at, Descending: desc, Source: source, ID: after, Lim: lim})
	if err != nil {
		return nil, fmt.Errorf("read the notes: %w", err)
	}
	out := make([]TimelineEntry, 0, len(rows))
	for _, r := range rows {
		author := pendingRef(r.ActorUserID, r.ActorServiceAccountID)
		out = append(out, TimelineEntry{PublicID: r.PublicID, At: r.CreatedAt.UTC(), Kind: KindNotes,
			Event: "note_added", Loudness: Quiet, Mentions: []string{},
			Actor: TimelineActor{Kind: r.ActorKind, Ref: author, TokenName: textOf(r.TokenName),
				Transport: r.Transport},
			Note: &NoteView{PublicID: r.PublicID, Body: r.Body, Author: author, Transport: r.Transport,
				CreatedAt: r.CreatedAt.UTC()},
			position: TimelinePosition{At: r.CreatedAt.UTC(), Source: sourceNotes, ID: r.ID}})
	}
	return out, nil
}

// deliveryEvents reads the delivery events of an Alert Group for the Timeline.
func (s *Service) deliveryEvents(ctx context.Context, gid int64, desc bool, at pgtype.Timestamptz, source int32,
	after int64, lim int32) ([]TimelineEntry, error) {
	rows, err := s.store.ListTimelineDeliveryEvents(ctx, dbgen.ListTimelineDeliveryEventsParams{OrgID: s.orgID,
		AlertGroupID: gid, At: at, Descending: desc, Source: source, ID: after, Lim: lim})
	if err != nil {
		return nil, fmt.Errorf("read the delivery events: %w", err)
	}
	out := make([]TimelineEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, TimelineEntry{PublicID: r.PublicID, At: r.OccurredAt.UTC(), Kind: KindDelivery,
			Loudness: Loudness(r.Loudness), Mentions: orEmpty(r.Mentions),
			Actor: TimelineActor{Kind: "system", Transport: "system"},
			Delivery: &DeliveryView{Event: r.Kind, Destination: Ref{PublicID: r.DestinationPublicID,
				Name: r.DestinationName}, Error: textOf(r.Error)},
			position: TimelinePosition{At: r.OccurredAt.UTC(), Source: sourceDelivery, ID: r.ID}})
	}
	return out, nil
}

// pendingRef is a reference to a User or a Service account by id, which actors replaces with its name.
func pendingRef(user, account pgtype.Int8) *ActorRef {
	switch {
	case user.Valid:
		return &ActorRef{Kind: "user", id: user.Int64}
	case account.Valid:
		return &ActorRef{Kind: "service_account", id: account.Int64}
	}
	return nil
}

func userRef(id pgtype.Int8) *ActorRef {
	if !id.Valid {
		return nil
	}
	return &ActorRef{Kind: "user", id: id.Int64}
}

// actors replaces the pending references of the entries with the Users and Service accounts they name.
func (s *Service) actors(ctx context.Context, entries []TimelineEntry) error {
	var users, accounts []int64
	each := func(f func(r *ActorRef)) {
		for i := range entries {
			e := &entries[i]
			for _, r := range []*ActorRef{e.Actor.Ref, e.Owner, e.PreviousOwner} {
				if r != nil {
					f(r)
				}
			}
			if e.Note != nil && e.Note.Author != nil && e.Note.Author != e.Actor.Ref {
				f(e.Note.Author)
			}
		}
	}
	each(func(r *ActorRef) {
		if r.Kind == "user" {
			users = append(users, r.id)
		} else {
			accounts = append(accounts, r.id)
		}
	})
	if len(users) == 0 && len(accounts) == 0 {
		return nil
	}
	found, err := s.refs(ctx, users, accounts)
	if err != nil {
		return err
	}
	each(func(r *ActorRef) {
		src := found.users
		if r.Kind != "user" {
			src = found.accounts
		}
		if ref, ok := src[r.id]; ok {
			*r = ref
		} else {
			*r = ActorRef{Kind: r.Kind, Deactivated: true}
		}
	})
	return nil
}

// gaugeRoutes are the Routes whose muster_alert_groups series this replica exports.
var (
	gaugeMu     sync.Mutex
	gaugeRoutes = map[string]bool{}
)

// openStatuses are the status label values of muster_alert_groups.
var openStatuses = []Status{StatusFiring, StatusAcknowledged, StatusSnoozed}

// ExportGauges sets muster_alert_groups to the open Alert Groups per Route and status, as a Leader task: a series
// for every status of every Route that is not deleted, and of any Route that still has open ones; the series of a
// Route that has neither are removed. Running it twice sets the same values.
func (s *Service) ExportGauges(ctx context.Context) error {
	routes, err := s.store.ListRoutePublicIDs(ctx, s.orgID)
	if err != nil {
		return fmt.Errorf("list the routes for muster_alert_groups: %w", err)
	}
	counts, err := s.store.CountOpenGroupsByRoute(ctx, s.orgID)
	if err != nil {
		return fmt.Errorf("count the open alert groups: %w", err)
	}
	values := map[string]map[Status]int64{}
	for _, r := range routes {
		values[r] = map[Status]int64{}
	}
	for _, c := range counts {
		if values[c.PublicID] == nil {
			values[c.PublicID] = map[Status]int64{}
		}
		values[c.PublicID][Status(c.Status)] = c.Count
	}
	gaugeMu.Lock()
	defer gaugeMu.Unlock()
	for r := range gaugeRoutes {
		if values[r] == nil {
			for _, st := range openStatuses {
				metrics.AlertGroups.Delete(r, string(st))
			}
			delete(gaugeRoutes, r)
		}
	}
	for r, byStatus := range values {
		for _, st := range openStatuses {
			metrics.AlertGroups.With(r, string(st)).Set(float64(byStatus[st]))
		}
		gaugeRoutes[r] = true
	}
	return nil
}

// orEmpty is s, or an empty list for nil.
func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

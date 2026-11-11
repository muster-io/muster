// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package routing holds the Routes of C-08 and routes Alerts with them. A Route has a name, Matchers, the Minimum
// severity, a Group key and every policy field, each in its typed column; the Default route exists from the first start, has no
// Matchers and is always last. Routes are evaluated `ORDER BY is_default, position, id` and the first one whose
// Matchers all match takes a newly firing Alert (Router). Reordering replaces the positions under the list ETag,
// organizations.route_order_version, which creating, deleting and reordering bump; deletion is soft. Every change is
// recorded in the Audit log and announced with the live hint route; the package also exports muster_route_info,
// serves the two Route profiles, Paging and Informational, from the built-in defaults, previews a Group key over the
// Stored Snapshots of a period with the Router's evaluation order, and computes the Route suggestions on each read.
package routing

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/matchers"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/routing/dbgen"
	"github.com/muster-io/muster/internal/templates"
)

// The languages of a Route (route.language).
const (
	LanguageEnglish = "en"
	LanguageRussian = "ru"
)

// DefaultName is the name the Default route is created with.
const DefaultName = "Default"

// The Minimum severities of a Route (route.minimum_severity, C-26.FR-8): none, or the lowest Severity level of its
// Alert Groups.
const (
	MinimumNone     = "none"
	MinimumWarning  = "warning"
	MinimumCritical = "critical"
)

// KindMinimumSeverity is the kind of the open_group_updates row that a changed Minimum severity writes (C-26.FR-9),
// KindMessageLabels that of a changed "Labels in the message" or name (C-29.FR-1, FR-2), KindMessageSettings that of a
// changed language, Snooze durations, root_message or line template (C-12.FR-2, FR-3, C-10.FR-6), and
// OpenGroupUpdatesChannel the LISTEN/NOTIFY channel that wakes the workers of internal/groups, which claim them.
const (
	KindMinimumSeverity     = "route_minimum_severity"
	KindMessageLabels       = "route_message_labels"
	KindMessageSettings     = "route_message_settings"
	OpenGroupUpdatesChannel = "muster_open_group_updates"
)

// The computed "Labels in the message" (route.message_labels, C-29.FR-2): the Group key without alertname and the
// Organization's severity label, then the Team label and namespace; and the most labels a Route's own list holds.
const (
	labelAlertname     = "alertname"
	labelNamespace     = "namespace"
	maxMessageLabels   = 50
	messageLabelsField = "/message_labels"
)

// MessageLabels is the effective "Labels in the message" of a Route (route.message_labels, C-29.FR-2): its own list
// own when it has one, otherwise the list computed from its Group key key and the Organization's severity and Team
// labels — the labels of the Group key except alertname and the severity label, then the Team label and namespace,
// each once, in that order.
func MessageLabels(own []string, key []string, severityLabel, teamLabel string) []string {
	if own != nil {
		return slices.Clone(own)
	}
	out := make([]string, 0, len(key)+2)
	add := func(name string) {
		if name != "" && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	for _, name := range key {
		if name != labelAlertname && name != severityLabel {
			add(name)
		}
	}
	add(teamLabel)
	add(labelNamespace)
	return out
}

// The Audit log actions and resource type, and the live hint, of Routes (C-03.FR-14).
const (
	ActionCreated   = "route.created"
	ActionUpdated   = "route.updated"
	ActionDeleted   = "route.deleted"
	ActionReordered = "route.reordered"
	ResourceRoute   = "route"
	Hint            = "route"
)

const (
	// maxNameLength bounds the name of a Route, in characters.
	maxNameLength = 200
	// uniqueViolation is the SQLSTATE of a unique index that refused a row, and nameIndex the index of the names of
	// the Routes that are not deleted.
	uniqueViolation = "23505"
	nameIndex       = "routes_name_key"
)

var (
	// ErrNotFound is a Route that does not exist in the Organization or is deleted.
	ErrNotFound = errors.New("no such route")
	// ErrNameTaken is a name another Route that is not deleted has.
	ErrNameTaken = errors.New("another route has this name")
	// ErrVersionMismatch is an If-Match that names another version of the Route, or of the Route list.
	ErrVersionMismatch = errors.New("the route or the route order changed since it was read")
	// ErrDefaultImmutable is a deletion of the Default route, or a reorder that lists it.
	ErrDefaultImmutable = errors.New("the default route cannot be deleted or moved")
)

// FieldError is a field of a request that is not valid, at a JSON pointer of the request body, with a stable code of
// the validation-failed problem.
type FieldError struct {
	Pointer string
	Code    string
	Detail  string
	// Line and Column are the 1-based position of a template error, 0 when unknown.
	Line   int
	Column int
}

func (e *FieldError) Error() string {
	return e.Pointer + ": " + e.Detail
}

// The codes of FieldError.
const (
	CodeInvalidFormat    = "invalid_format"
	CodeTooLong          = "too_long"
	CodeOutOfRange       = "out_of_range"
	CodeUnsupported      = "unsupported"
	CodeDuplicate        = "duplicate"
	CodeUnknownID        = "unknown_id"
	CodeInvalidRegex     = "invalid_regex"
	CodeRouteSetMismatch = "route_set_mismatch"
	CodeOneOfRequired    = "one_of_required"
	CodeRequired         = "required"
)

// Matcher is a Matcher of a Route as the API and the Audit log show it: a label, an operator and a value.
type Matcher struct {
	Label string `json:"label"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

// Policy is the policy fields of a Route; each capability brings the behaviour of its fields (C-08.FR-1).
type Policy struct {
	ReopenWindowSeconds         int64      `json:"reopen_window_seconds"`
	GracePeriodSeconds          int64      `json:"grace_period_seconds"`
	UrgentRiseRemovesAck        bool       `json:"urgent_rise_removes_ack"`
	SnoozeDurationsSeconds      []int64    `json:"snooze_durations_seconds"`
	ThreadBatchingWindowSeconds int64      `json:"thread_batching_window_seconds"`
	StormThreshold              int64      `json:"storm_threshold"`
	Language                    string     `json:"language"`
	Templates                   Templates  `json:"templates"`
	AckTimeout                  AckTimeout `json:"ack_timeout"`
	Reminders                   Reminders  `json:"reminders"`
	AutoUnacknowledge           bool       `json:"auto_unacknowledge"`
}

// Templates are the message templates of a Route; nil uses the built-in template.
type Templates struct {
	RootMessage      *string `json:"root_message"`
	Line             *string `json:"line"`
	AckTimeoutNotice *string `json:"ack_timeout_notice"`
}

// The kinds of Route templates, as the API and the template error state name them.
const (
	TemplateRootMessage      = "root_message"
	TemplateLine             = "line"
	TemplateAckTimeoutNotice = "ack_timeout_notice"
)

// KeepTemplates says, for updateRoute, which templates the request left out: those keep the stored template, while a
// template given as null resets it to the built-in one (C-08.FR-1). A creation ignores it: a template left out is the
// built-in one.
type KeepTemplates struct {
	RootMessage      bool
	Line             bool
	AckTimeoutNotice bool
}

// TemplateError is the template error state of a Route (C-12.FR-6): since when a template of it keeps failing and what
// failed.
type TemplateError struct {
	Since time.Time
	Error string
}

// TemplateHooks are the template checks of messages, wired in the runtime: Check dry-runs a template of the Route
// routeID, 0 for a new Route, and returns a *templates.Error for a template that fails; Saved clears the Route's
// template error, in the transaction tx that saved new templates of the kinds given.
type TemplateHooks struct {
	Check func(ctx context.Context, routeID int64, kind, source, language string) error
	Saved func(ctx context.Context, tx dbgen.DBTX, routeID int64, publicID string, kinds []string) error
}

// named are the templates of t by kind.
func (t Templates) named() []namedTemplate {
	return []namedTemplate{{TemplateRootMessage, t.RootMessage}, {TemplateLine, t.Line},
		{TemplateAckTimeoutNotice, t.AckTimeoutNotice}}
}

type namedTemplate struct {
	kind  string
	value *string
}

// keep sets in t the templates k keeps from stored.
func (k KeepTemplates) keep(t *Templates, stored Templates) {
	if k.RootMessage {
		t.RootMessage = stored.RootMessage
	}
	if k.Line {
		t.Line = stored.Line
	}
	if k.AckTimeoutNotice {
		t.AckTimeoutNotice = stored.AckTimeoutNotice
	}
}

// AckTimeout is route.ack_timeout.
type AckTimeout struct {
	Enabled              bool  `json:"enabled"`
	FirstIntervalSeconds int64 `json:"first_interval_seconds"`
}

// Reminders is route.reminders.
type Reminders struct {
	Enabled              bool  `json:"enabled"`
	FirstIntervalSeconds int64 `json:"first_interval_seconds"`
	CapSeconds           int64 `json:"cap_seconds"`
}

// Route is a Route as the API reads it; Position is its zero-based place in evaluation order, DestinationEntries its
// Destinations that are not deleted with their Severity conditions, by public_id, DestinationIDs their public_ids, which
// the API still derives from them until 0.3.0, and Storm its active Storm, nil when there is none. DeliverToTeams is
// "Also to the Teams from the label" and RunTeamChains "Run the Teams' Escalation chains" (C-26.FR-2).
type Route struct {
	ID          int64
	PublicID    string
	Name        string
	Description string
	Position    int
	IsDefault   bool
	// MinimumSeverity is route.minimum_severity; Urgent, the deprecated urgent mark, is true exactly when it is
	// critical (C-26.FR-10).
	MinimumSeverity string
	Urgent          bool
	Matchers        []Matcher
	GroupKey        []string
	Policy          Policy
	DestinationIDs  []string
	// DestinationEntries are the Destinations with their Severity conditions (C-26.FR-1).
	DestinationEntries []DestinationEntry
	DeliverToTeams     bool
	RunTeamChains      bool
	Storm              *Storm
	TemplateError      *TemplateError
	CreatedAt          time.Time
	Version            int64
	// OpenAlertGroupCount is the number of its open Alert Groups, which block its deletion (C-09.FR-19).
	OpenAlertGroupCount int64
	// OwningTeam is the Team that owns the Route (C-25.FR-2), nil when none does.
	OwningTeam *TeamRef
	// OwnMessageLabels says whether the Route has a list of "Labels in the message" of its own, and MessageLabels is
	// the effective list (C-29.FR-2): its own, or the one computed from its Group key and labels while it has none.
	OwnMessageLabels bool
	MessageLabels    []string
	// labels are the Organization's labels the computed list is made of.
	labels labelSettings
}

// labelSettings are the Organization's severity and Team labels, which the computed "Labels in the message" leave out
// and add (C-29.FR-2).
type labelSettings struct {
	severity string
	team     string
}

// ownLabels is the Route's own list of "Labels in the message", nil while it has none.
func (r Route) ownLabels() []string {
	if !r.OwnMessageLabels {
		return nil
	}
	if r.MessageLabels == nil {
		return []string{}
	}
	return r.MessageLabels
}

// TeamRef names the Team that owns a Route: its internal id, public_id, name and slug, and whether it was deleted.
type TeamRef struct {
	ID       int64
	PublicID string
	Name     string
	Slug     string
	Deleted  bool
}

// OwnerFilter selects the Routes a Team owns (C-25.FR-2): the Team by its public_id, or with UserID the Teams the
// User is in, "My Teams".
type OwnerFilter struct {
	Team   string
	UserID int64
}

// Storm is the active Storm of a Route (C-11.FR-6): when it started and the new Alert Groups it counted, those of its
// Storm summary.
type Storm struct {
	Since           time.Time
	AlertGroupCount int64
}

// DestinationEntry is a Destination of a Route with its Severity condition, by the Destination's public_id
// (C-26.FR-1).
type DestinationEntry struct {
	DestinationID string            `json:"destination_id"`
	Condition     SeverityCondition `json:"severity_condition"`
}

// MembershipChange is a save of the Route RouteID that changed its Destination entries or its option "Also to the
// Teams from the label" (C-26.FR-6, C-11.FR-14): the entries before and after the save, by internal Destination id in
// id order, and the option before and after.
type MembershipChange struct {
	RouteID       int64
	Before, After []Entry
	ToTeamsBefore bool
	ToTeamsAfter  bool
}

// Changed reports whether the save changed the entries or the option.
func (c MembershipChange) Changed() bool {
	return c.ToTeamsBefore != c.ToTeamsAfter || !slices.Equal(c.Before, c.After)
}

// Removed are the Destinations the save took off the Route, in id order.
func (c MembershipChange) Removed() []int64 {
	var out []int64
	for _, e := range c.Before {
		if !slices.ContainsFunc(c.After, func(a Entry) bool { return a.DestinationID == e.DestinationID }) {
			out = append(out, e.DestinationID)
		}
	}
	return out
}

// Added are the Destinations the save put on the Route, in id order.
func (c MembershipChange) Added() []int64 {
	return MembershipChange{Before: c.After, After: c.Before}.Removed()
}

// Membership is the hook that delivery runs, in the transaction tx of a Route's save, when the save changed the
// Route's Destination entries or its option "Also to the Teams from the label" (C-26.FR-6, C-11.FR-14), under the
// Route's membership lock, which the save holds exclusively.
type Membership func(ctx context.Context, tx dbgen.DBTX, c MembershipChange) error

// OpenAlertGroupsError is the refusal to delete a Route that still has open Alert Groups (C-09.FR-19); Count says
// how many.
type OpenAlertGroupsError struct {
	Count int64
}

func (e *OpenAlertGroupsError) Error() string {
	return fmt.Sprintf("the route has %d open alert groups", e.Count)
}

// Input is what createRoute and updateRoute write. A nil Description or MinimumSeverity keeps the stored value on an
// update, and is empty or none on a creation; Matchers replace the stored ones as a whole. Urgent is the deprecated
// urgent mark of a 0.1 client (C-26.FR-10): true without a MinimumSeverity is read as the Minimum severity critical;
// false, or beside a MinimumSeverity, changes nothing.
type Input struct {
	Name            string
	Description     *string
	Matchers        []Matcher
	MinimumSeverity *string
	Urgent          bool
	GroupKey        []string
	// DestinationEntries are the Route's Destinations with their Severity conditions, replacing the stored ones as a
	// whole; nil, when the request left them out, takes DestinationIDs. DestinationIDs, the deprecated list of a 0.1
	// client, is read only without DestinationEntries: it keeps the stored condition of each Destination it keeps and
	// gives a new one any. Both nil keep the stored entries on an update and are none on a creation.
	DestinationEntries *[]DestinationEntry
	DestinationIDs     []string
	// DeliverToTeams and RunTeamChains are the options of C-26.FR-2: nil keeps the stored one on an update and is
	// false, or true for RunTeamChains, on a creation.
	DeliverToTeams *bool
	RunTeamChains  *bool
	Policy         Policy
	// Keep are the templates an update leaves as they are stored.
	Keep KeepTemplates
	// OwningTeamID is the public_id of the Team that owns the Route: nil keeps the stored one on an update and is none
	// on a creation, and an empty string clears it.
	OwningTeamID *string
	// OwnMessageLabels says whether the Route has a list of "Labels in the message" of its own: nil keeps the stored
	// choice on an update and is false on a creation. MessageLabels is that list, read only when the Route has one, nil
	// when the request left it out — which keeps a stored list of its own — and a slice, empty included, when it gave one.
	OwnMessageLabels *bool
	MessageLabels    []string
}

// List is every Route that is not deleted in evaluation order, the Default route last, with the version of the list
// that its ETag carries.
type List struct {
	Routes  []Route
	Version int64
}

// Requester is who asks for a change and how: the actor and the Transport of the Audit log entry, and the client
// address.
type Requester struct {
	Actor     audit.Actor
	Transport audit.Transport
	Address   netip.Addr
}

// Queries are the queries of the package, with the insert of the Audit log and the live hint.
type Queries interface {
	evalQueries
	ListRoutes(ctx context.Context, orgID int64) ([]dbgen.ListRoutesRow, error)
	GetRoute(ctx context.Context, arg dbgen.GetRouteParams) (dbgen.GetRouteRow, error)
	LockRoute(ctx context.Context, arg dbgen.LockRouteParams) (int64, error)
	ListRouteMatchers(ctx context.Context, arg dbgen.ListRouteMatchersParams) ([]dbgen.ListRouteMatchersRow, error)
	BumpRouteOrder(ctx context.Context, arg dbgen.BumpRouteOrderParams) (int64, error)
	GetRouteOrderVersion(ctx context.Context, orgID int64) (int64, error)
	InsertRoute(ctx context.Context, arg dbgen.InsertRouteParams) (int64, error)
	EnsureDefaultRoute(ctx context.Context, arg dbgen.EnsureDefaultRouteParams) (string, error)
	UpdateRoute(ctx context.Context, arg dbgen.UpdateRouteParams) error
	DeleteRouteMatchers(ctx context.Context, arg dbgen.DeleteRouteMatchersParams) error
	InsertRouteMatcher(ctx context.Context, arg dbgen.InsertRouteMatcherParams) error
	DeleteRoute(ctx context.Context, arg dbgen.DeleteRouteParams) error
	SetRoutePositions(ctx context.Context, arg dbgen.SetRoutePositionsParams) error
	ListRouteInfo(ctx context.Context, orgID int64) ([]dbgen.ListRouteInfoRow, error)
	ListHeartbeatIntegrations(ctx context.Context, orgID int64) ([]dbgen.ListHeartbeatIntegrationsRow, error)
	ListAlertingIntegrations(ctx context.Context, orgID int64) ([]dbgen.ListAlertingIntegrationsRow, error)
	ListAlertingDestinations(ctx context.Context, orgID int64) ([]dbgen.ListAlertingDestinationsRow, error)
	ListAlertingSchedules(ctx context.Context, orgID int64) ([]dbgen.ListAlertingSchedulesRow, error)
	ListAlertingGateways(ctx context.Context, orgID int64) ([]dbgen.ListAlertingGatewaysRow, error)
	HasOpenAlertGroups(ctx context.Context, arg dbgen.HasOpenAlertGroupsParams) (bool, error)
	InsertOpenGroupUpdate(ctx context.Context, arg dbgen.InsertOpenGroupUpdateParams) error
	NotifyOpenGroupUpdates(ctx context.Context, channel string) error
	ListRouteLabelSuggestions(ctx context.Context, arg dbgen.ListRouteLabelSuggestionsParams) (
		[]dbgen.ListRouteLabelSuggestionsRow, error)
	ListRouteSuggestionDismissals(ctx context.Context, arg dbgen.ListRouteSuggestionDismissalsParams) ([]string,
		error)
	DismissRouteSuggestion(ctx context.Context, arg dbgen.DismissRouteSuggestionParams) error
	CountOpenAlertGroups(ctx context.Context, arg dbgen.CountOpenAlertGroupsParams) (int64, error)
	ListOpenAlertGroupCounts(ctx context.Context, orgID int64) ([]dbgen.ListOpenAlertGroupCountsRow, error)
	ResolveDestinations(ctx context.Context, arg dbgen.ResolveDestinationsParams) ([]dbgen.ResolveDestinationsRow,
		error)
	ListRouteDestinationIDs(ctx context.Context, arg dbgen.ListRouteDestinationIDsParams) (
		[]dbgen.ListRouteDestinationIDsRow, error)
	InsertRouteDestinations(ctx context.Context, arg dbgen.InsertRouteDestinationsParams) error
	SetRouteDestinationConditions(ctx context.Context, arg dbgen.SetRouteDestinationConditionsParams) error
	DeleteRouteDestinations(ctx context.Context, arg dbgen.DeleteRouteDestinationsParams) error
	DropDeletedRouteDestinations(ctx context.Context, arg dbgen.DropDeletedRouteDestinationsParams) error
	LockRouteMembership(ctx context.Context, arg dbgen.LockRouteMembershipParams) error
	ListTeamsOfDestination(ctx context.Context, arg dbgen.ListTeamsOfDestinationParams) ([]int64, error)
	LockTeamMembership(ctx context.Context, arg dbgen.LockTeamMembershipParams) error
	DeleteDestinationTeams(ctx context.Context, arg dbgen.DeleteDestinationTeamsParams) error
	BumpRoutesOfDestination(ctx context.Context, arg dbgen.BumpRoutesOfDestinationParams) (
		[]dbgen.BumpRoutesOfDestinationRow, error)
	LockOwningTeam(ctx context.Context, arg dbgen.LockOwningTeamParams) (dbgen.LockOwningTeamRow, error)
	ListUserTeams(ctx context.Context, arg dbgen.ListUserTeamsParams) ([]string, error)
	ListPreviewTeams(ctx context.Context, arg dbgen.ListPreviewTeamsParams) ([]dbgen.ListPreviewTeamsRow, error)
	ListPreviewTeamEntries(ctx context.Context, arg dbgen.ListPreviewTeamEntriesParams) (
		[]dbgen.ListPreviewTeamEntriesRow, error)
	ListPreviewDestinations(ctx context.Context, arg dbgen.ListPreviewDestinationsParams) (
		[]dbgen.ListPreviewDestinationsRow, error)
	audit.Store
	Notify(ctx context.Context, h db.Hint) error
	// DB is the pool or the transaction the queries run on, which the membership hook writes through.
	DB() dbgen.DBTX
}

// Store runs the queries alone, in one transaction, or in a caller's transaction tx (On).
type Store interface {
	Queries
	InTx(ctx context.Context, f func(Queries) error) error
	On(tx dbgen.DBTX) Queries
}

// NewStore is the Store over the main pool.
func NewStore(pool *pgxpool.Pool) Store {
	return pgStore{pgQueries: newQueries(pool), pool: pool}
}

type pgQueries struct {
	*dbgen.Queries
	audit.Store
	exec dbgen.DBTX
}

func newQueries(d dbgen.DBTX) pgQueries {
	return pgQueries{Queries: dbgen.New(d), Store: audit.NewStore(d), exec: d}
}

func (q pgQueries) Notify(ctx context.Context, h db.Hint) error {
	return db.NotifyHint(ctx, q.exec, h)
}

func (q pgQueries) DB() dbgen.DBTX { return q.exec }

type pgStore struct {
	pgQueries
	pool *pgxpool.Pool
}

func (pgStore) On(tx dbgen.DBTX) Queries { return newQueries(tx) }

func (s pgStore) InTx(ctx context.Context, f func(Queries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return f(newQueries(tx))
	})
}

// Config is what a Service needs.
type Config struct {
	OrgID int64
	Store Store
	Audit *audit.Writer
	// Business is the business clock, which dates the rows and the period of a Group key preview; Real measures how
	// long a preview took.
	Business clock.Clock
	Real     clock.Clock
	// Router is the Router of the Organization, whose evaluation order the Group key preview and the suggestions
	// use; a new one when nil.
	Router *Router
	// Snapshots reads the Stored Snapshots for the Group key preview.
	Snapshots Snapshots
	Log       *logging.Logger
}

// Service holds the Routes of the Organization.
type Service struct {
	orgID     int64
	store     Store
	audit     *audit.Writer
	clock     clock.Clock
	real      clock.Clock
	router    *Router
	snapshots Snapshots
	log       *logging.Logger

	// membership is the hook of delivery for Destinations added to or removed from a Route; nil changes nothing.
	membership Membership
	// templates are the template checks of messages; without them templates are saved without a dry run.
	templates TemplateHooks

	// info holds the muster_route_info series this replica exports, by public_id to name.
	infoMu sync.Mutex
	info   map[string]string
	// infoChanged wakes RunInfo.
	infoChanged chan struct{}
}

// New returns the Service of the Organization in cfg.
func New(cfg Config) *Service {
	router := cfg.Router
	if router == nil {
		router = NewRouter(cfg.OrgID)
	}
	return &Service{orgID: cfg.OrgID, store: cfg.Store, audit: cfg.Audit, clock: cfg.Business, real: cfg.Real,
		router: router, snapshots: cfg.Snapshots, log: cfg.Log, info: map[string]string{},
		infoChanged: make(chan struct{}, 1)}
}

// DestinationDeleted is the hook of the Destinations (C-11.FR-14), in the transaction tx that deletes the Destination
// destinationID, before it leaves its Routes: each Route that is not deleted and has it gets a new version — the
// Destination leaves its destination_ids — and its hint, and its membership lock is taken, so that no Enqueue on it
// runs until tx ends. The Routes are locked in id order before their membership locks; a transaction that renders
// Alert Groups of several Routes at once and holds the membership lock of one of them may still deadlock with it,
// which PostgreSQL detects and ends. Then, after the Routes' membership locks and in id order, it takes the membership
// lock of each Team whose Destination entries name the Destination exclusively and removes it from those Teams, so
// that no Enqueue reads such an entry and delivers to the Destination while the deletion commits (C-26.FR-6).
func (s *Service) DestinationDeleted(ctx context.Context, tx dbgen.DBTX, destinationID int64) error {
	q := s.store.On(tx)
	routes, err := q.BumpRoutesOfDestination(ctx, dbgen.BumpRoutesOfDestinationParams{OrgID: s.orgID,
		DestinationID: destinationID, Now: s.clock.Now().UTC()})
	if err != nil {
		return fmt.Errorf("change the routes of destination %d: %w", destinationID, err)
	}
	slices.SortFunc(routes, func(a, b dbgen.BumpRoutesOfDestinationRow) int { return cmp.Compare(a.ID, b.ID) })
	for _, r := range routes {
		if err := q.LockRouteMembership(ctx, dbgen.LockRouteMembershipParams{
			LockClass: db.RouteMembershipLockClass, RouteID: r.ID}); err != nil {
			return fmt.Errorf("lock the destinations of route %s: %w", r.PublicID, err)
		}
		if err := q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint, ID: r.PublicID}); err != nil {
			return err
		}
	}
	teams, err := q.ListTeamsOfDestination(ctx, dbgen.ListTeamsOfDestinationParams{OrgID: s.orgID,
		DestinationID: destinationID})
	if err != nil {
		return fmt.Errorf("list the teams of destination %d: %w", destinationID, err)
	}
	if len(teams) == 0 {
		return nil
	}
	for _, id := range teams {
		if err := q.LockTeamMembership(ctx, dbgen.LockTeamMembershipParams{LockClass: db.TeamMembershipLockClass,
			TeamID: id}); err != nil {
			return fmt.Errorf("lock the destinations of team %d: %w", id, err)
		}
	}
	if err := q.DeleteDestinationTeams(ctx, dbgen.DeleteDestinationTeamsParams{OrgID: s.orgID,
		DestinationID: destinationID}); err != nil {
		return fmt.Errorf("remove destination %d from its teams: %w", destinationID, err)
	}
	return nil
}

// SetMembership fills the hook of delivery for Destinations added to or removed from a Route, before any Route
// changes.
func (s *Service) SetMembership(m Membership) {
	s.membership = m
}

// SetTemplates fills the template checks of messages, before any Route is created or updated.
func (s *Service) SetTemplates(h TemplateHooks) {
	s.templates = h
}

// List lists the Routes that are not deleted in evaluation order with the version of the list. The version is read
// first, so that the list is never older than its ETag.
func (s *Service) List(ctx context.Context) (List, error) {
	return s.list(ctx, s.store)
}

func (s *Service) list(ctx context.Context, q Queries) (List, error) {
	version, err := q.GetRouteOrderVersion(ctx, s.orgID)
	if err != nil {
		return List{}, fmt.Errorf("read the version of the route order: %w", err)
	}
	rows, err := q.ListRoutes(ctx, s.orgID)
	if err != nil {
		return List{}, fmt.Errorf("list the routes: %w", err)
	}
	out := List{Version: version, Routes: make([]Route, len(rows))}
	for i, r := range rows {
		out.Routes[i] = routeOf(dbgen.GetRouteRow{ID: r.ID, PublicID: r.PublicID, Name: r.Name,
			Description: r.Description, Position: r.Position, IsDefault: r.IsDefault,
			MinimumSeverity: r.MinimumSeverity,
			GroupKey:        r.GroupKey, ReopenWindowSeconds: r.ReopenWindowSeconds, GracePeriodSeconds: r.GracePeriodSeconds,
			UrgentRiseRemovesAck: r.UrgentRiseRemovesAck, SnoozeDurationsSeconds: r.SnoozeDurationsSeconds,
			ThreadBatchingWindowSeconds: r.ThreadBatchingWindowSeconds, StormThreshold: r.StormThreshold,
			Language: r.Language, TemplateRootMessage: r.TemplateRootMessage, TemplateLine: r.TemplateLine,
			TemplateAckTimeoutNotice: r.TemplateAckTimeoutNotice, AckTimeoutEnabled: r.AckTimeoutEnabled,
			AckTimeoutFirstIntervalSeconds: r.AckTimeoutFirstIntervalSeconds, RemindersEnabled: r.RemindersEnabled,
			RemindersFirstIntervalSeconds: r.RemindersFirstIntervalSeconds, RemindersCapSeconds: r.RemindersCapSeconds,
			AutoUnacknowledge: r.AutoUnacknowledge, CreatedAt: r.CreatedAt, Version: r.Version, Place: int64(i),
			TemplateErrorSince: r.TemplateErrorSince, TemplateError: r.TemplateError,
			StormSince: r.StormSince, StormAlertGroupCount: r.StormAlertGroupCount, OwningTeamID: r.OwningTeamID,
			OwningTeamPublicID: r.OwningTeamPublicID, OwningTeamName: r.OwningTeamName,
			OwningTeamSlug: r.OwningTeamSlug, OwningTeamDeleted: r.OwningTeamDeleted, MessageLabels: r.MessageLabels,
			SeverityLabel: r.SeverityLabel, TeamLabel: r.TeamLabel, DeliverToTeams: r.DeliverToTeams,
			RunTeamChains: r.RunTeamChains})
	}
	if err := s.withMatchers(ctx, q, out.Routes); err != nil {
		return List{}, err
	}
	if err := s.withDestinations(ctx, q, out.Routes); err != nil {
		return List{}, err
	}
	if err := s.withCounts(ctx, q, out.Routes); err != nil {
		return List{}, err
	}
	return out, nil
}

// ListOwned lists the Routes that are not deleted and that the Team of f owns, or the Teams of its User, in
// evaluation order with the version of the whole list. A Team that cannot exist owns nothing.
func (s *Service) ListOwned(ctx context.Context, f OwnerFilter) (List, error) {
	teams := map[string]bool{}
	if f.UserID != 0 {
		ids, err := s.store.ListUserTeams(ctx, dbgen.ListUserTeamsParams{OrgID: s.orgID, UserID: f.UserID})
		if err != nil {
			return List{}, fmt.Errorf("list the teams of the user: %w", err)
		}
		for _, id := range ids {
			teams[id] = true
		}
	} else if id, err := publicid.Parse(publicid.Team, f.Team); err == nil {
		teams[id] = true
	}
	list, err := s.List(ctx)
	if err != nil {
		return List{}, err
	}
	owned := make([]Route, 0, len(list.Routes))
	for _, rt := range list.Routes {
		if rt.OwningTeam != nil && teams[rt.OwningTeam.PublicID] {
			owned = append(owned, rt)
		}
	}
	list.Routes = owned
	return list, nil
}

// resolveOwner reads the Team raw names as the owner of a Route (C-25.FR-2) in share mode: nil keeps stored, an empty
// string clears it, and a Team that does not exist or is deleted is an unknown_id FieldError.
func (s *Service) resolveOwner(ctx context.Context, q Queries, raw *string, stored *TeamRef) (*TeamRef, error) {
	if raw == nil {
		return stored, nil
	}
	if *raw == "" {
		return nil, nil
	}
	unknown := &FieldError{Pointer: "/owning_team_id", Code: CodeUnknownID, Detail: "No such Team."}
	id, err := publicid.Parse(publicid.Team, *raw)
	if err != nil {
		return nil, unknown
	}
	t, err := q.LockOwningTeam(ctx, dbgen.LockOwningTeamParams{OrgID: s.orgID, PublicID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, unknown
	}
	if err != nil {
		return nil, fmt.Errorf("read the owning team %s: %w", id, err)
	}
	return &TeamRef{ID: t.ID, PublicID: t.PublicID, Name: t.Name, Slug: t.Slug}, nil
}

// ownerID is the owning_team_id of a write.
func ownerID(t *TeamRef) pgtype.Int8 {
	if t == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: t.ID, Valid: true}
}

// Get reads the Route publicID; a deleted or unknown one is ErrNotFound.
func (s *Service) Get(ctx context.Context, publicID string) (Route, error) {
	return s.get(ctx, s.store, publicID)
}

func (s *Service) get(ctx context.Context, q Queries, publicID string) (Route, error) {
	id, err := publicid.Parse(publicid.Route, publicID)
	if err != nil {
		return Route{}, ErrNotFound
	}
	r, err := q.GetRoute(ctx, dbgen.GetRouteParams{OrgID: s.orgID, PublicID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Route{}, ErrNotFound
	}
	if err != nil {
		return Route{}, fmt.Errorf("read the route %s: %w", id, err)
	}
	list := []Route{routeOf(r)}
	if err := s.withMatchers(ctx, q, list); err != nil {
		return Route{}, err
	}
	if err := s.withDestinations(ctx, q, list); err != nil {
		return Route{}, err
	}
	if err := s.withCounts(ctx, q, list); err != nil {
		return Route{}, err
	}
	return list[0], nil
}

// withCounts sets the number of open Alert Groups of each Route.
func (s *Service) withCounts(ctx context.Context, q Queries, list []Route) error {
	counts, err := q.ListOpenAlertGroupCounts(ctx, s.orgID)
	if err != nil {
		return fmt.Errorf("count the open alert groups of the routes: %w", err)
	}
	by := make(map[int64]int64, len(counts))
	for _, c := range counts {
		by[c.RouteID] = c.Count
	}
	for i := range list {
		list[i].OpenAlertGroupCount = by[list[i].ID]
	}
	return nil
}

// withMatchers sets the Matchers of each Route.
func (s *Service) withMatchers(ctx context.Context, q Queries, list []Route) error {
	ids := make([]int64, len(list))
	index := make(map[int64]int, len(list))
	for i, r := range list {
		ids[i], index[r.ID] = r.ID, i
		list[i].Matchers = []Matcher{}
	}
	rows, err := q.ListRouteMatchers(ctx, dbgen.ListRouteMatchersParams{OrgID: s.orgID, RouteIds: ids})
	if err != nil {
		return fmt.Errorf("list the matchers of the routes: %w", err)
	}
	for _, m := range rows {
		if i, ok := index[m.RouteID]; ok {
			list[i].Matchers = append(list[i].Matchers, Matcher{Label: m.Label, Op: m.Op, Value: m.Value})
		}
	}
	return nil
}

// withDestinations sets the Destinations of each Route that are not deleted with their Severity conditions, and their
// public_ids, in order.
func (s *Service) withDestinations(ctx context.Context, q Queries, list []Route) error {
	ids := make([]int64, len(list))
	index := make(map[int64]int, len(list))
	for i, r := range list {
		ids[i], index[r.ID] = r.ID, i
		list[i].DestinationIDs, list[i].DestinationEntries = []string{}, []DestinationEntry{}
	}
	rows, err := q.ListRouteDestinationIDs(ctx, dbgen.ListRouteDestinationIDsParams{OrgID: s.orgID, RouteIds: ids})
	if err != nil {
		return fmt.Errorf("list the destinations of the routes: %w", err)
	}
	for _, d := range rows {
		if i, ok := index[d.RouteID]; ok {
			list[i].DestinationIDs = append(list[i].DestinationIDs, d.PublicID)
			list[i].DestinationEntries = append(list[i].DestinationEntries, DestinationEntry{DestinationID: d.PublicID,
				Condition: SeverityCondition(d.SeverityCondition)})
		}
	}
	return nil
}

// wanted is a Destination of a Route as an edit gives it: its internal id and its Severity condition.
type wanted struct {
	id        int64
	condition SeverityCondition
}

// destinationSet is the Destinations of a Route as an edit gives them, by public_id.
type destinationSet map[string]wanted

// publicIDs are the public_ids of the set, in order.
func (d destinationSet) publicIDs() []string {
	return slices.Sorted(maps.Keys(d))
}

// entries are the entries of the set, in the order of the public_ids.
func (d destinationSet) entries() []DestinationEntry {
	out := make([]DestinationEntry, 0, len(d))
	for _, id := range d.publicIDs() {
		out = append(out, DestinationEntry{DestinationID: id, Condition: d[id].condition})
	}
	return out
}

// requested is one Destination entry of a request with the pointer of its Destination.
type requested struct {
	DestinationEntry
	pointer string
}

// requestedDestinations are the Destination entries an Input gives (C-26.FR-1, C-08.FR-1): its DestinationEntries,
// any by default; without them its deprecated DestinationIDs, each with the stored condition of a Destination the Route
// keeps and any for a new one; without either the stored entries, none on a creation.
func requestedDestinations(in Input, stored []DestinationEntry) []requested {
	switch {
	case in.DestinationEntries != nil:
		out := make([]requested, len(*in.DestinationEntries))
		for i, e := range *in.DestinationEntries {
			if e.Condition == "" {
				e.Condition = ConditionAny
			}
			out[i] = requested{DestinationEntry: e, pointer: "/destination_entries/" + strconv.Itoa(i) + "/destination_id"}
		}
		return out
	case in.DestinationIDs != nil:
		conditions := make(map[string]SeverityCondition, len(stored))
		for _, e := range stored {
			conditions[e.DestinationID] = e.Condition
		}
		out := make([]requested, len(in.DestinationIDs))
		for i, raw := range in.DestinationIDs {
			c := ConditionAny
			if id, err := publicid.Parse(publicid.Destination, raw); err == nil && conditions[id] != "" {
				c = conditions[id]
			}
			out[i] = requested{DestinationEntry: DestinationEntry{DestinationID: raw, Condition: c},
				pointer: "/destination_ids/" + strconv.Itoa(i)}
		}
		return out
	}
	out := make([]requested, len(stored))
	for i, e := range stored {
		out[i] = requested{DestinationEntry: e, pointer: "/destination_entries/" + strconv.Itoa(i) + "/destination_id"}
	}
	return out
}

// resolveDestinations reads the Destinations of the requested entries (C-08.FR-1, C-26.FR-1): an id that names no
// Destination, or a deleted one, is an unknown_id FieldError at its pointer, and a condition outside the three an
// invalid_format one. The deprecated list may name a Destination twice, which counts once; the entries may not, which is
// a duplicate FieldError.
func (s *Service) resolveDestinations(ctx context.Context, q Queries, req []requested) (destinationSet, error) {
	out := destinationSet{}
	if len(req) == 0 {
		return out, nil
	}
	canonical := make([]string, len(req))
	seen := make(map[string]bool, len(req))
	for i, r := range req {
		id, err := publicid.Parse(publicid.Destination, r.DestinationID)
		if err != nil {
			return nil, unknownDestination(r.pointer)
		}
		if !slices.Contains(Conditions, r.Condition) {
			return nil, &FieldError{Pointer: strings.TrimSuffix(r.pointer, "/destination_id") + "/severity_condition",
				Code: CodeInvalidFormat, Detail: "Choose any, critical or below_critical."}
		}
		if seen[id] && strings.HasPrefix(r.pointer, "/destination_entries/") {
			return nil, &FieldError{Pointer: r.pointer, Code: CodeDuplicate,
				Detail: "The Destination is named twice."}
		}
		seen[id], canonical[i] = true, id
	}
	rows, err := q.ResolveDestinations(ctx, dbgen.ResolveDestinationsParams{OrgID: s.orgID, PublicIds: canonical})
	if err != nil {
		return nil, fmt.Errorf("read the destinations of the route: %w", err)
	}
	found := make(map[string]int64, len(rows))
	for _, r := range rows {
		found[r.PublicID] = r.ID
	}
	for i, id := range canonical {
		internal, ok := found[id]
		if !ok {
			return nil, unknownDestination(req[i].pointer)
		}
		out[id] = wanted{id: internal, condition: req[i].Condition}
	}
	return out, nil
}

// unknownDestination is the refusal of a Destination of a Route at its pointer.
func unknownDestination(pointer string) error {
	return &FieldError{Pointer: pointer, Code: CodeUnknownID, Detail: "No such Destination."}
}

// writeDestinations makes want the Destinations of the Route routeID in route_destinations — those kept keep when
// they were added, and take the condition of want — and returns the entries before and after, by Destination in id
// order. It takes the Route's membership lock first, after the Route's row, so that an Enqueue on the Route waits for
// the change or the change for it. Entries of a deleted Destination, which a save that raced its deletion may have
// left and no read returns, are dropped.
func (s *Service) writeDestinations(ctx context.Context, q Queries, routeID int64, want destinationSet) (before,
	after []Entry, err error) {
	if err := q.LockRouteMembership(ctx, dbgen.LockRouteMembershipParams{LockClass: db.RouteMembershipLockClass,
		RouteID: routeID}); err != nil {
		return nil, nil, fmt.Errorf("lock the destinations of the route: %w", err)
	}
	if err := q.DropDeletedRouteDestinations(ctx, dbgen.DropDeletedRouteDestinationsParams{OrgID: s.orgID,
		RouteID: routeID}); err != nil {
		return nil, nil, fmt.Errorf("drop the entries of deleted destinations from the route: %w", err)
	}
	rows, err := q.ListRouteDestinationIDs(ctx, dbgen.ListRouteDestinationIDsParams{OrgID: s.orgID,
		RouteIds: []int64{routeID}})
	if err != nil {
		return nil, nil, fmt.Errorf("list the destinations of the route: %w", err)
	}
	current := make(map[int64]bool, len(rows))
	var removed []int64
	var changed dbgen.SetRouteDestinationConditionsParams
	for _, r := range rows {
		current[r.DestinationID] = true
		before = append(before, Entry{DestinationID: r.DestinationID, Condition: SeverityCondition(r.SeverityCondition)})
		w, ok := want[r.PublicID]
		switch {
		case !ok:
			removed = append(removed, r.DestinationID)
		case string(w.condition) != r.SeverityCondition:
			changed.DestinationIds = append(changed.DestinationIds, r.DestinationID)
			changed.Conditions = append(changed.Conditions, string(w.condition))
		}
	}
	var added dbgen.InsertRouteDestinationsParams
	for _, id := range want.publicIDs() {
		w := want[id]
		after = append(after, Entry{DestinationID: w.id, Condition: w.condition})
		if !current[w.id] {
			added.DestinationIds = append(added.DestinationIds, w.id)
			added.Conditions = append(added.Conditions, string(w.condition))
		}
	}
	byDestination := func(a, b Entry) int { return cmp.Compare(a.DestinationID, b.DestinationID) }
	slices.SortFunc(before, byDestination)
	slices.SortFunc(after, byDestination)
	if len(changed.DestinationIds) > 0 {
		changed.OrgID, changed.RouteID = s.orgID, routeID
		if err := q.SetRouteDestinationConditions(ctx, changed); err != nil {
			return nil, nil, fmt.Errorf("change the severity conditions of the destinations of the route: %w", err)
		}
	}
	if len(removed) > 0 {
		if err := q.DeleteRouteDestinations(ctx, dbgen.DeleteRouteDestinationsParams{OrgID: s.orgID,
			RouteID: routeID, DestinationIds: removed}); err != nil {
			return nil, nil, fmt.Errorf("remove destinations from the route: %w", err)
		}
	}
	if len(added.DestinationIds) > 0 {
		added.OrgID, added.RouteID, added.Now = s.orgID, routeID, s.clock.Now().UTC()
		if err := q.InsertRouteDestinations(ctx, added); err != nil {
			return nil, nil, fmt.Errorf("add destinations to the route: %w", err)
		}
	}
	return before, after, nil
}

// changeMembership writes the Destinations want of the Route routeID (writeDestinations) after a save that changed its
// Destination entries or its option "Also to the Teams from the label", from toTeamsBefore to toTeamsAfter, and runs
// the membership hook with the entries before and after and the option before and after (C-26.FR-6, C-11.FR-14), in
// the save's transaction and under the Route's membership lock: delivery publishes the open Alert Groups Quietly where
// their reach grew and gives the final edit where no entry names a Destination any more.
func (s *Service) changeMembership(ctx context.Context, q Queries, routeID int64, want destinationSet,
	toTeamsBefore, toTeamsAfter bool) error {
	before, after, err := s.writeDestinations(ctx, q, routeID, want)
	if err != nil {
		return err
	}
	c := MembershipChange{RouteID: routeID, Before: before, After: after, ToTeamsBefore: toTeamsBefore,
		ToTeamsAfter: toTeamsAfter}
	if s.membership == nil || !c.Changed() {
		return nil
	}
	if err := s.membership(ctx, q.DB(), c); err != nil {
		return fmt.Errorf("deliver to the changed destinations of the route: %w", err)
	}
	return nil
}

// lock locks the Route publicID for a change and reads it.
func (s *Service) lock(ctx context.Context, q Queries, publicID string) (Route, error) {
	id, err := publicid.Parse(publicid.Route, publicID)
	if err != nil {
		return Route{}, ErrNotFound
	}
	if _, err := q.LockRoute(ctx, dbgen.LockRouteParams{OrgID: s.orgID, PublicID: id}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Route{}, ErrNotFound
		}
		return Route{}, fmt.Errorf("lock the route %s: %w", id, err)
	}
	return s.get(ctx, q, id)
}

// Create creates a Route at the last position before the Default route; a name another Route has is ErrNameTaken.
// Its templates are dry-run against the built-in example first (C-12.FR-5).
func (s *Service) Create(ctx context.Context, r Requester, in Input) (Route, error) {
	return s.create(ctx, r, in, creation{})
}

// creation is how a Route is created: at the top of the list instead of before the Default route, after a
// precondition checked under the lock of the list, and with details for its Audit log entry. The precondition may
// return a Route, by id, to place the new one directly below instead; 0 leaves it where it was inserted.
type creation struct {
	first        bool
	precondition func(Queries) (int64, error)
	details      map[string]any
}

func (s *Service) create(ctx context.Context, r Requester, in Input, how creation) (Route, error) {
	in.Name = strings.TrimSpace(in.Name)
	if err := check(in); err != nil {
		return Route{}, err
	}
	if err := s.checkTemplates(ctx, 0, in.Policy, Templates{}, map[string]string{}); err != nil {
		return Route{}, err
	}
	var created Route
	err := s.store.InTx(ctx, func(q Queries) error {
		// The lock of the list serializes the changes of the order, so that a new Route takes the next position.
		if _, err := q.BumpRouteOrder(ctx, dbgen.BumpRouteOrderParams{OrgID: s.orgID}); err != nil {
			return fmt.Errorf("bump the route order: %w", err)
		}
		below := int64(0)
		if how.precondition != nil {
			var err error
			if below, err = how.precondition(q); err != nil {
				return err
			}
		}
		dests, err := s.resolveDestinations(ctx, q, requestedDestinations(in, nil))
		if err != nil {
			return err
		}
		owner, err := s.resolveOwner(ctx, q, in.OwningTeamID, nil)
		if err != nil {
			return err
		}
		description := ""
		if in.Description != nil {
			description = *in.Description
		}
		own, err := messageLabelsOf(in, nil)
		if err != nil {
			return err
		}
		id := publicid.New(publicid.Route)
		p := in.Policy
		routeID, err := q.InsertRoute(ctx, dbgen.InsertRouteParams{
			OrgID: s.orgID, PublicID: id, Name: in.Name, Description: description, First: how.first,
			MinimumSeverity: minimumOf(in),
			GroupKey:        in.GroupKey, ReopenWindowSeconds: p.ReopenWindowSeconds, GracePeriodSeconds: p.GracePeriodSeconds,
			UrgentRiseRemovesAck: p.UrgentRiseRemovesAck, SnoozeDurationsSeconds: p.SnoozeDurationsSeconds,
			ThreadBatchingWindowSeconds: p.ThreadBatchingWindowSeconds, StormThreshold: p.StormThreshold,
			Language: p.Language, TemplateRootMessage: text(p.Templates.RootMessage),
			TemplateLine: text(p.Templates.Line), TemplateAckTimeoutNotice: text(p.Templates.AckTimeoutNotice),
			AckTimeoutEnabled: p.AckTimeout.Enabled, AckTimeoutFirstIntervalSeconds: p.AckTimeout.FirstIntervalSeconds,
			RemindersEnabled: p.Reminders.Enabled, RemindersFirstIntervalSeconds: p.Reminders.FirstIntervalSeconds,
			RemindersCapSeconds: p.Reminders.CapSeconds, AutoUnacknowledge: p.AutoUnacknowledge,
			OwningTeamID: ownerID(owner), MessageLabels: own,
			DeliverToTeams: in.DeliverToTeams != nil && *in.DeliverToTeams,
			RunTeamChains:  in.RunTeamChains == nil || *in.RunTeamChains, Now: s.clock.Now().UTC(),
		})
		if err != nil {
			return fmt.Errorf("create the route: %w", nameTaken(err))
		}
		if below != 0 {
			if err := s.placeBelow(ctx, q, routeID, below); err != nil {
				return err
			}
		}
		if err := s.writeMatchers(ctx, q, routeID, in.Matchers); err != nil {
			return err
		}
		// A new Route has no open Alert Group: its Destinations reach none.
		if _, _, err := s.writeDestinations(ctx, q, routeID, dests); err != nil {
			return err
		}
		if created, err = s.get(ctx, q, id); err != nil {
			return err
		}
		if err := s.audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport, Action: ActionCreated,
			Resource: resourceOf(created), Diff: audit.Created(viewOf(created)), Details: how.details,
			SourceAddress: r.Address,
		}); err != nil {
			return err
		}
		return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint, ID: created.PublicID})
	})
	if err != nil {
		return Route{}, err
	}
	s.exportInfo(created.PublicID, created.Name)
	return created, nil
}

// placeBelow moves the Route routeID directly below the Route below in the evaluation order, renumbering the Routes
// other than the Default route.
func (s *Service) placeBelow(ctx context.Context, q Queries, routeID, below int64) error {
	current, err := q.ListRoutes(ctx, s.orgID)
	if err != nil {
		return fmt.Errorf("list the routes: %w", err)
	}
	order := make([]int64, 0, len(current))
	for _, r := range current {
		if r.IsDefault || r.ID == routeID {
			continue
		}
		order = append(order, r.ID)
		if r.ID == below {
			order = append(order, routeID)
		}
	}
	positions := make([]int64, len(order))
	for i := range order {
		positions[i] = int64(i)
	}
	if err := q.SetRoutePositions(ctx, dbgen.SetRoutePositionsParams{OrgID: s.orgID, Ids: order,
		Positions: positions}); err != nil {
		return fmt.Errorf("write the route order: %w", err)
	}
	return nil
}

// writeMatchers writes the Matchers of a Route in their order.
func (s *Service) writeMatchers(ctx context.Context, q Queries, routeID int64, ms []Matcher) error {
	for i, m := range ms {
		if err := q.InsertRouteMatcher(ctx, dbgen.InsertRouteMatcherParams{RouteID: routeID, OrgID: s.orgID,
			Position: int64(i), Label: m.Label, Op: m.Op, Value: m.Value}); err != nil {
			return fmt.Errorf("write the matchers of the route: %w", err)
		}
	}
	return nil
}

// Update replaces the configured fields of the Route publicID; a non-nil version must be its current one (If-Match).
// The change applies at once to the next Alerts routed; an Alert already routed keeps its Route (C-08.FR-8). The
// Default route takes no Matchers. A template in.Keep names stays as stored; a new template is dry-run first, and
// saving one clears the Route's template error about it (C-12.FR-5, FR-6).
func (s *Service) Update(ctx context.Context, r Requester, publicID string, version *int64, in Input) (Route, error) {
	in.Name = strings.TrimSpace(in.Name)
	if err := check(in); err != nil {
		return Route{}, err
	}
	// The dry run reads Stored Snapshots and renders them, so it runs before the Route is locked; a template that
	// changed meanwhile is dry-run again under the lock.
	checked := map[string]string{}
	if s.templates.Check != nil {
		current, err := s.Get(ctx, publicID)
		if err != nil {
			return Route{}, err
		}
		proposed := in.Policy
		in.Keep.keep(&proposed.Templates, current.Policy.Templates)
		if err := s.checkTemplates(ctx, current.ID, proposed, current.Policy.Templates, checked); err != nil {
			return Route{}, err
		}
	}
	var before, updated Route
	err := s.store.InTx(ctx, func(q Queries) error {
		// A new owner is read before the Route is locked: a Team's deletion locks the Team, then its Routes.
		owner, err := s.resolveOwner(ctx, q, in.OwningTeamID, nil)
		if err != nil {
			return err
		}
		if before, err = s.lock(ctx, q, publicID); err != nil {
			return err
		}
		if in.OwningTeamID == nil {
			owner = before.OwningTeam
		}
		if version != nil && *version != before.Version {
			return ErrVersionMismatch
		}
		if before.IsDefault && len(in.Matchers) > 0 {
			return &FieldError{Pointer: "/matchers", Code: CodeUnsupported,
				Detail: "The Default route takes every Alert no other Route took; it has no Matchers."}
		}
		in.Keep.keep(&in.Policy.Templates, before.Policy.Templates)
		if err := s.checkTemplates(ctx, before.ID, in.Policy, before.Policy.Templates, checked); err != nil {
			return err
		}
		dests, err := s.resolveDestinations(ctx, q, requestedDestinations(in, before.DestinationEntries))
		if err != nil {
			return err
		}
		next := before
		next.Name, next.Matchers, next.GroupKey, next.Policy = in.Name, in.Matchers, in.GroupKey, in.Policy
		if in.MinimumSeverity != nil {
			next.MinimumSeverity = *in.MinimumSeverity
		} else if in.Urgent {
			next.MinimumSeverity = MinimumCritical
		}
		next.Urgent = next.MinimumSeverity == MinimumCritical
		next.DestinationIDs, next.DestinationEntries, next.OwningTeam = dests.publicIDs(), dests.entries(), owner
		if in.DeliverToTeams != nil {
			next.DeliverToTeams = *in.DeliverToTeams
		}
		if in.RunTeamChains != nil {
			next.RunTeamChains = *in.RunTeamChains
		}
		if in.Description != nil {
			next.Description = *in.Description
		}
		own, err := messageLabelsOf(in, &before)
		if err != nil {
			return err
		}
		next.OwnMessageLabels = own != nil
		next.MessageLabels = MessageLabels(own, next.GroupKey, before.labels.severity, before.labels.team)
		changes := audit.Diff(viewOf(before), viewOf(next))
		if len(changes) == 0 {
			updated = before
			return nil
		}
		p := next.Policy
		if err := q.UpdateRoute(ctx, dbgen.UpdateRouteParams{
			OrgID: s.orgID, ID: before.ID, Name: next.Name, Description: next.Description,
			MinimumSeverity: next.MinimumSeverity,
			GroupKey:        next.GroupKey, ReopenWindowSeconds: p.ReopenWindowSeconds, GracePeriodSeconds: p.GracePeriodSeconds,
			UrgentRiseRemovesAck: p.UrgentRiseRemovesAck, SnoozeDurationsSeconds: p.SnoozeDurationsSeconds,
			ThreadBatchingWindowSeconds: p.ThreadBatchingWindowSeconds, StormThreshold: p.StormThreshold,
			Language: p.Language, TemplateRootMessage: text(p.Templates.RootMessage),
			TemplateLine: text(p.Templates.Line), TemplateAckTimeoutNotice: text(p.Templates.AckTimeoutNotice),
			AckTimeoutEnabled: p.AckTimeout.Enabled, AckTimeoutFirstIntervalSeconds: p.AckTimeout.FirstIntervalSeconds,
			RemindersEnabled: p.Reminders.Enabled, RemindersFirstIntervalSeconds: p.Reminders.FirstIntervalSeconds,
			RemindersCapSeconds: p.Reminders.CapSeconds, AutoUnacknowledge: p.AutoUnacknowledge,
			OwningTeamID: ownerID(next.OwningTeam), MessageLabels: own, DeliverToTeams: next.DeliverToTeams,
			RunTeamChains: next.RunTeamChains, Now: s.clock.Now().UTC(),
		}); err != nil {
			return fmt.Errorf("update the route %s: %w", before.PublicID, nameTaken(err))
		}
		if kinds := changedTemplates(before.Policy.Templates, p.Templates); len(kinds) > 0 &&
			s.templates.Saved != nil {
			if err := s.templates.Saved(ctx, q.DB(), before.ID, before.PublicID, kinds); err != nil {
				return err
			}
		}
		if !slices.Equal(before.Matchers, next.Matchers) {
			if err := q.DeleteRouteMatchers(ctx, dbgen.DeleteRouteMatchersParams{OrgID: s.orgID,
				RouteID: before.ID}); err != nil {
				return fmt.Errorf("replace the matchers of the route %s: %w", before.PublicID, err)
			}
			if err := s.writeMatchers(ctx, q, before.ID, next.Matchers); err != nil {
				return err
			}
		}
		if !slices.Equal(before.DestinationEntries, next.DestinationEntries) ||
			before.DeliverToTeams != next.DeliverToTeams {
			if err := s.changeMembership(ctx, q, before.ID, dests, before.DeliverToTeams,
				next.DeliverToTeams); err != nil {
				return err
			}
		}
		if before.MinimumSeverity != next.MinimumSeverity {
			if err := s.requestOpenGroupUpdate(ctx, q, before, KindMinimumSeverity); err != nil {
				return err
			}
		}
		// The field Route of every open Root message names the Route, so a rename re-renders them too (C-29.FR-1). Both
		// kinds re-render with the Route as it is when the row runs, so a save that changes the labels and a setting
		// the Root messages show writes one row.
		switch {
		case !slices.Equal(before.MessageLabels, next.MessageLabels) || before.Name != next.Name:
			if err := s.requestOpenGroupUpdate(ctx, q, before, KindMessageLabels); err != nil {
				return err
			}
		case messageSettingsChanged(before.Policy, p):
			if err := s.requestOpenGroupUpdate(ctx, q, before, KindMessageSettings); err != nil {
				return err
			}
		}
		if updated, err = s.get(ctx, q, before.PublicID); err != nil {
			return err
		}
		if err := s.audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport, Action: ActionUpdated,
			Resource: resourceOf(updated), Diff: changes, SourceAddress: r.Address,
		}); err != nil {
			return err
		}
		return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint, ID: updated.PublicID})
	})
	if err != nil {
		return Route{}, err
	}
	if updated.Name != before.Name {
		s.exportInfo(updated.PublicID, updated.Name)
	}
	return updated, nil
}

// requestOpenGroupUpdate, in the transaction of the change of the Route r's Minimum severity, of its effective
// "Labels in the message" or of another setting its Root messages show, asks the workers of internal/groups to apply it
// to the open Alert Groups of r (C-26.FR-9, C-29.FR-2): one open_group_updates row of the kind given, when r has any,
// and a notification that wakes them once the transaction commits. The row is requested at the business time of the
// change.
func (s *Service) requestOpenGroupUpdate(ctx context.Context, q Queries, r Route, kind string) error {
	open, err := q.HasOpenAlertGroups(ctx, dbgen.HasOpenAlertGroupsParams{OrgID: s.orgID, RouteID: r.ID})
	if err != nil {
		return fmt.Errorf("look for the open alert groups of the route %s: %w", r.PublicID, err)
	}
	if !open {
		return nil
	}
	if err := q.InsertOpenGroupUpdate(ctx, dbgen.InsertOpenGroupUpdateParams{OrgID: s.orgID,
		Kind: kind, RouteID: pgtype.Int8{Int64: r.ID, Valid: true},
		RequestedAt: s.clock.Now().UTC()}); err != nil {
		return fmt.Errorf("apply the change of the route %s to its open alert groups: %w", r.PublicID, err)
	}
	if err := q.NotifyOpenGroupUpdates(ctx, OpenGroupUpdatesChannel); err != nil {
		return fmt.Errorf("wake the open alert group update workers: %w", err)
	}
	return nil
}

// Delete deletes the Route publicID; a non-nil version must be its current one. It leaves every list and the
// evaluation order at once; the row stays for the history that points at it. The Default route is
// ErrDefaultImmutable.
func (s *Service) Delete(ctx context.Context, r Requester, publicID string, version *int64) error {
	var before Route
	err := s.store.InTx(ctx, func(q Queries) error {
		// The list is locked before the Route, in the order a reorder takes them.
		if _, err := q.BumpRouteOrder(ctx, dbgen.BumpRouteOrderParams{OrgID: s.orgID}); err != nil {
			return fmt.Errorf("bump the route order: %w", err)
		}
		var err error
		if before, err = s.lock(ctx, q, publicID); err != nil {
			return err
		}
		if before.IsDefault {
			return ErrDefaultImmutable
		}
		if version != nil && *version != before.Version {
			return ErrVersionMismatch
		}
		// Counted after the lock: a Snapshot that groups on the Route holds it FOR SHARE, so its Alert Group is
		// either committed and counted here, or the Snapshot waits and then finds the Route deleted.
		open, err := q.CountOpenAlertGroups(ctx, dbgen.CountOpenAlertGroupsParams{OrgID: s.orgID,
			RouteID: before.ID})
		if err != nil {
			return fmt.Errorf("count the open alert groups of the route %s: %w", before.PublicID, err)
		}
		if open > 0 {
			return &OpenAlertGroupsError{Count: open}
		}
		now := s.clock.Now().UTC()
		if err := q.DeleteRoute(ctx, dbgen.DeleteRouteParams{OrgID: s.orgID, ID: before.ID,
			Now: pgtype.Timestamptz{Time: now, Valid: true}}); err != nil {
			return fmt.Errorf("delete the route %s: %w", before.PublicID, err)
		}
		if err := s.audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport, Action: ActionDeleted,
			Resource: resourceOf(before), Diff: []audit.Change{{Pointer: "/deleted_at", After: now}},
			SourceAddress: r.Address,
		}); err != nil {
			return err
		}
		return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint, ID: before.PublicID})
	})
	if err != nil {
		return err
	}
	s.unexportInfo(before.PublicID)
	unexportCounters(before.PublicID)
	return nil
}

// Reorder replaces the order of every Route except the Default route with ids, in one transaction; a non-nil version
// must be the current version of the list (the list ETag). A list that names the Default route is ErrDefaultImmutable;
// one whose Routes differ from the current set is a route_set_mismatch FieldError.
func (s *Service) Reorder(ctx context.Context, r Requester, version *int64, ids []string) (List, error) {
	var out List
	err := s.store.InTx(ctx, func(q Queries) error {
		expected := pgtype.Int8{}
		if version != nil {
			expected = pgtype.Int8{Int64: *version, Valid: true}
		}
		if _, err := q.BumpRouteOrder(ctx, dbgen.BumpRouteOrderParams{OrgID: s.orgID,
			Expected: expected}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrVersionMismatch
			}
			return fmt.Errorf("bump the route order: %w", err)
		}
		current, err := q.ListRoutes(ctx, s.orgID)
		if err != nil {
			return fmt.Errorf("list the routes: %w", err)
		}
		before, order, err := newOrder(current, ids)
		if err != nil {
			return err
		}
		positions := make([]int64, len(order))
		for i := range order {
			positions[i] = int64(i)
		}
		if err := q.SetRoutePositions(ctx, dbgen.SetRoutePositionsParams{OrgID: s.orgID, Ids: order,
			Positions: positions}); err != nil {
			return fmt.Errorf("write the route order: %w", err)
		}
		if out, err = s.list(ctx, q); err != nil {
			return err
		}
		after := make([]string, 0, len(out.Routes))
		for _, rt := range out.Routes {
			if !rt.IsDefault {
				after = append(after, rt.PublicID)
			}
		}
		var diff []audit.Change
		if !slices.Equal(before, after) {
			diff = []audit.Change{{Pointer: "/route_ids", Before: before, After: after}}
		}
		if err := s.audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport, Action: ActionReordered,
			Resource: audit.Resource{Type: ResourceRoute}, Diff: diff, SourceAddress: r.Address,
		}); err != nil {
			return err
		}
		return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint})
	})
	if err != nil {
		return List{}, err
	}
	return out, nil
}

// newOrder checks a reorder against the current Routes in evaluation order: it returns the public_ids of the Routes
// other than the Default route in their current order and the internal ids in the new one. The ids must name exactly
// those Routes, each once.
func newOrder(current []dbgen.ListRoutesRow, ids []string) ([]string, []int64, error) {
	byPublicID := make(map[string]dbgen.ListRoutesRow, len(current))
	var before []string
	for _, r := range current {
		byPublicID[r.PublicID] = r
		if !r.IsDefault {
			before = append(before, r.PublicID)
		}
	}
	mismatch := &FieldError{Pointer: "/route_ids", Code: CodeRouteSetMismatch,
		Detail: "The Routes differ from the current set: list every Route except the Default route, each once."}
	order := make([]int64, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, raw := range ids {
		id, err := publicid.Parse(publicid.Route, raw)
		r, ok := byPublicID[id]
		if ok && r.IsDefault {
			return nil, nil, ErrDefaultImmutable
		}
		if err != nil || !ok || seen[id] {
			continue
		}
		seen[id] = true
		order = append(order, r.ID)
	}
	if len(order) != len(ids) || len(order) != len(before) {
		return nil, nil, mismatch
	}
	return before, order, nil
}

// EnsureDefault creates the Default route of the Organization once (C-08.FR-3), as a start-up ensure step: "Default",
// without Matchers, with the values of the Paging profile, which sets no Minimum severity. It changes nothing when the Default route exists; a Route
// that is not deleted and already has its name stops the start with ErrNameTaken.
func EnsureDefault(ctx context.Context, q Queries, orgID int64, now time.Time) error {
	p := paging()
	_, err := q.EnsureDefaultRoute(ctx, dbgen.EnsureDefaultRouteParams{
		OrgID: orgID, PublicID: publicid.New(publicid.Route), Name: DefaultName,
		Description: "Takes every Alert no other Route took.", MinimumSeverity: p.MinimumSeverity, GroupKey: p.GroupKey,
		ReopenWindowSeconds: p.Policy.ReopenWindowSeconds, GracePeriodSeconds: p.Policy.GracePeriodSeconds,
		UrgentRiseRemovesAck: p.Policy.UrgentRiseRemovesAck, SnoozeDurationsSeconds: p.Policy.SnoozeDurationsSeconds,
		ThreadBatchingWindowSeconds: p.Policy.ThreadBatchingWindowSeconds, StormThreshold: p.Policy.StormThreshold,
		Language: p.Policy.Language, AckTimeoutEnabled: p.Policy.AckTimeout.Enabled,
		AckTimeoutFirstIntervalSeconds: p.Policy.AckTimeout.FirstIntervalSeconds,
		RemindersEnabled:               p.Policy.Reminders.Enabled,
		RemindersFirstIntervalSeconds:  p.Policy.Reminders.FirstIntervalSeconds,
		RemindersCapSeconds:            p.Policy.Reminders.CapSeconds, AutoUnacknowledge: p.Policy.AutoUnacknowledge,
		Now: now.UTC(),
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("create the default route: %w", nameTaken(err))
	}
	return nil
}

// check refuses an Input that is not valid: the fields the database checks are refused here first, with the pointer
// of the field. The Destinations are checked in the transaction that writes them.
func check(in Input) error {
	if strings.TrimSpace(in.Name) == "" {
		return &FieldError{Pointer: "/name", Code: CodeInvalidFormat, Detail: "The name is empty."}
	}
	if utf8.RuneCountInString(in.Name) > maxNameLength {
		return &FieldError{Pointer: "/name", Code: CodeTooLong,
			Detail: fmt.Sprintf("The name is longer than %d characters.", maxNameLength)}
	}
	for i, m := range in.Matchers {
		if err := checkMatcher(i, m); err != nil {
			return err
		}
	}
	if m := in.MinimumSeverity; m != nil && !slices.Contains([]string{MinimumNone, MinimumWarning, MinimumCritical}, *m) {
		return &FieldError{Pointer: "/minimum_severity", Code: CodeInvalidFormat,
			Detail: "The Minimum severity is none, warning or critical."}
	}
	if err := checkGroupKey("/group_key", in.GroupKey); err != nil {
		return err
	}
	return checkPolicy(in.Policy)
}

// checkMessageLabels refuses a list of "Labels in the message" of a Route's own longer than maxMessageLabels, or with
// an empty or a repeated name, as checkGroupKey refuses a Group key: any label name a Group key takes, such as
// `app-name`, is taken here too.
func checkMessageLabels(names []string) error {
	if len(names) > maxMessageLabels {
		return &FieldError{Pointer: messageLabelsField, Code: CodeTooLong,
			Detail: fmt.Sprintf("At most %d labels are shown in the message.", maxMessageLabels)}
	}
	seen := make(map[string]bool, len(names))
	for i, name := range names {
		pointer := messageLabelsField + "/" + strconv.Itoa(i)
		if name == "" {
			return &FieldError{Pointer: pointer, Code: CodeInvalidFormat, Detail: "A label name is empty."}
		}
		if seen[name] {
			return &FieldError{Pointer: pointer, Code: CodeDuplicate, Detail: "The label is already in the list."}
		}
		seen[name] = true
	}
	return nil
}

// messageLabelsOf is the own list of "Labels in the message" that a write of in stores, nil for the computed one: on a
// creation, before is nil and the list is computed unless in asks for one of its own; on an update, an omitted choice
// keeps the stored one and an omitted list the stored list. A list of its own without a list to keep is required, and
// only a list of its own is checked: the computed list a read gives, sent back unchanged, is ignored.
func messageLabelsOf(in Input, before *Route) ([]string, error) {
	own := before != nil && before.OwnMessageLabels
	if in.OwnMessageLabels != nil {
		own = *in.OwnMessageLabels
	}
	switch {
	case !own:
		return nil, nil
	case in.MessageLabels != nil:
		if err := checkMessageLabels(in.MessageLabels); err != nil {
			return nil, err
		}
		return slices.Clone(in.MessageLabels), nil
	case before != nil && before.OwnMessageLabels:
		return before.ownLabels(), nil
	}
	return nil, &FieldError{Pointer: messageLabelsField, Code: CodeRequired,
		Detail: "A Route with labels of its own lists them."}
}

// checkGroupKey refuses a Group key, at the pointer base, with an empty or a repeated label name.
func checkGroupKey(base string, key []string) error {
	seen := make(map[string]bool, len(key))
	for i, name := range key {
		pointer := base + "/" + strconv.Itoa(i)
		if name == "" {
			return &FieldError{Pointer: pointer, Code: CodeInvalidFormat, Detail: "A label name is empty."}
		}
		if seen[name] {
			return &FieldError{Pointer: pointer, Code: CodeDuplicate, Detail: "The label is already in the Group key."}
		}
		seen[name] = true
	}
	return nil
}

// checkMatcher refuses a Matcher at index i whose label is empty, whose operator is not one of Alertmanager's, or
// whose regular expression does not compile; the expression is compiled as routing compiles it.
func checkMatcher(i int, m Matcher) error {
	pointer := "/matchers/" + strconv.Itoa(i)
	if m.Label == "" {
		return &FieldError{Pointer: pointer + "/label", Code: CodeInvalidFormat, Detail: "The label name is empty."}
	}
	if _, err := matchers.New(m.Label, matchers.Op(m.Op), m.Value); err != nil {
		if re, ok := errors.AsType[*matchers.RegexpError](err); ok {
			return &FieldError{Pointer: pointer + "/value", Code: CodeInvalidRegex, Detail: re.Error()}
		}
		return &FieldError{Pointer: pointer + "/op", Code: CodeInvalidFormat,
			Detail: "The operator is one of =, !=, =~ and !~."}
	}
	return nil
}

// checkPolicy refuses policy values outside the CHECKs of the routes table; the templates are dry-run by
// checkTemplates.
func checkPolicy(p Policy) error {
	nonNegative := []struct {
		pointer string
		value   int64
	}{
		{"/policy/reopen_window_seconds", p.ReopenWindowSeconds},
		{"/policy/grace_period_seconds", p.GracePeriodSeconds},
		{"/policy/thread_batching_window_seconds", p.ThreadBatchingWindowSeconds},
	}
	for _, f := range nonNegative {
		if f.value < 0 {
			return &FieldError{Pointer: f.pointer, Code: CodeOutOfRange, Detail: "The value is at least 0."}
		}
	}
	positive := []struct {
		pointer string
		value   int64
	}{
		{"/policy/storm_threshold", p.StormThreshold},
		{"/policy/ack_timeout/first_interval_seconds", p.AckTimeout.FirstIntervalSeconds},
		{"/policy/reminders/first_interval_seconds", p.Reminders.FirstIntervalSeconds},
	}
	for _, f := range positive {
		if f.value < 1 {
			return &FieldError{Pointer: f.pointer, Code: CodeOutOfRange, Detail: "The value is at least 1."}
		}
	}
	for i, d := range p.SnoozeDurationsSeconds {
		if d < 1 {
			return &FieldError{Pointer: "/policy/snooze_durations_seconds/" + strconv.Itoa(i), Code: CodeOutOfRange,
				Detail: "A Snooze duration is at least 1 second."}
		}
	}
	if p.Reminders.CapSeconds < p.Reminders.FirstIntervalSeconds {
		return &FieldError{Pointer: "/policy/reminders/cap_seconds", Code: CodeOutOfRange,
			Detail: "The cap of the Reminders is at least their first interval."}
	}
	if p.Language != LanguageEnglish && p.Language != LanguageRussian {
		return &FieldError{Pointer: "/policy/language", Code: CodeInvalidFormat, Detail: "The language is en or ru."}
	}
	return nil
}

// checkTemplates dry-runs the templates of p that are set, differ from those of before and are not in checked, the
// texts already dry-run by kind (C-12.FR-5): a template that fails is refused at /policy/templates/<kind> with the
// code and the position of the sandbox. It adds the texts it dry-ran to checked.
func (s *Service) checkTemplates(ctx context.Context, routeID int64, p Policy, before Templates,
	checked map[string]string) error {
	if s.templates.Check == nil {
		return nil
	}
	was := before.named()
	for i, t := range p.Templates.named() {
		if t.value == nil || (was[i].value != nil && *was[i].value == *t.value) {
			continue
		}
		if done, ok := checked[t.kind]; ok && done == *t.value {
			continue
		}
		err := s.templates.Check(ctx, routeID, t.kind, *t.value, p.Language)
		if e, ok := errors.AsType[*templates.Error](err); ok {
			return &FieldError{Pointer: "/policy/templates/" + t.kind, Code: e.Code, Detail: e.Detail, Line: e.Line,
				Column: e.Column}
		}
		if err != nil {
			return fmt.Errorf("check the %s template: %w", t.kind, err)
		}
		checked[t.kind] = *t.value
	}
	return nil
}

// messageSettingsChanged reports whether an update changes a setting of the Route that its Root messages show besides
// its name and "Labels in the message": the language of the built-in texts (C-12.FR-3), the Snooze durations, which
// its Snooze buttons name (C-10.FR-6), and the root_message and line templates (C-12.FR-2). The ack_timeout_notice
// template renders only a Thread reply.
func messageSettingsChanged(before, after Policy) bool {
	if before.Language != after.Language || !slices.Equal(before.SnoozeDurationsSeconds, after.SnoozeDurationsSeconds) {
		return true
	}
	kinds := changedTemplates(before.Templates, after.Templates)
	return slices.Contains(kinds, TemplateRootMessage) || slices.Contains(kinds, TemplateLine)
}

// changedTemplates are the kinds of templates whose text changes from before to after, a reset included.
func changedTemplates(before, after Templates) []string {
	var out []string
	was := before.named()
	for i, t := range after.named() {
		if (t.value == nil) != (was[i].value == nil) || (t.value != nil && *t.value != *was[i].value) {
			out = append(out, t.kind)
		}
	}
	return out
}

// nameTaken maps the refusal of the unique index of the names to ErrNameTaken.
func nameTaken(err error) error {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == uniqueViolation &&
		pgErr.ConstraintName == nameIndex {
		return ErrNameTaken
	}
	return err
}

// resourceOf is a Route as the Audit log names it.
func resourceOf(r Route) audit.Resource {
	return audit.Resource{Type: ResourceRoute, PublicID: r.PublicID, Name: r.Name}
}

// view is a Route as its Audit log diff shows it.
type view struct {
	Name            string    `json:"name"`
	Description     string    `json:"description"`
	Matchers        []Matcher `json:"matchers"`
	MinimumSeverity string    `json:"minimum_severity"`
	Urgent          bool      `json:"urgent"`
	GroupKey        []string  `json:"group_key"`
	DestinationIDs  []string  `json:"destination_ids"`
	// DestinationEntries are the Destinations with their Severity conditions, of which DestinationIDs are the ids.
	DestinationEntries []DestinationEntry `json:"destination_entries"`
	DeliverToTeams     bool               `json:"deliver_to_teams"`
	RunTeamChains      bool               `json:"run_team_chains"`
	OwningTeamID       *string            `json:"owning_team_id"`
	// MessageLabels is the Route's own list of "Labels in the message", null while it is computed.
	MessageLabels []string `json:"message_labels"`
	Policy        Policy   `json:"policy"`
}

func viewOf(r Route) view {
	ms, key := r.Matchers, r.GroupKey
	if ms == nil {
		ms = []Matcher{}
	}
	if key == nil {
		key = []string{}
	}
	dests, entries := r.DestinationIDs, r.DestinationEntries
	if dests == nil {
		dests = []string{}
	}
	if entries == nil {
		entries = []DestinationEntry{}
	}
	p := r.Policy
	if p.SnoozeDurationsSeconds == nil {
		p.SnoozeDurationsSeconds = []int64{}
	}
	v := view{Name: r.Name, Description: r.Description, Matchers: ms, MinimumSeverity: r.MinimumSeverity,
		Urgent: r.Urgent, GroupKey: key, DestinationIDs: dests, DestinationEntries: entries,
		DeliverToTeams: r.DeliverToTeams, RunTeamChains: r.RunTeamChains, MessageLabels: r.ownLabels(), Policy: p}
	if r.OwningTeam != nil {
		v.OwningTeamID = &r.OwningTeam.PublicID
	}
	return v
}

func routeOf(r dbgen.GetRouteRow) Route {
	var st *Storm
	if r.StormSince.Valid {
		st = &Storm{Since: r.StormSince.Time.UTC(), AlertGroupCount: r.StormAlertGroupCount.Int64}
	}
	var te *TemplateError
	if r.TemplateErrorSince.Valid {
		te = &TemplateError{Since: r.TemplateErrorSince.Time.UTC(), Error: r.TemplateError.String}
	}
	var owner *TeamRef
	if r.OwningTeamID.Valid && r.OwningTeamPublicID.Valid {
		owner = &TeamRef{ID: r.OwningTeamID.Int64, PublicID: r.OwningTeamPublicID.String,
			Name: r.OwningTeamName.String, Slug: r.OwningTeamSlug.String, Deleted: r.OwningTeamDeleted}
	}
	labels := labelSettings{severity: r.SeverityLabel, team: r.TeamLabel}
	return Route{Storm: st, TemplateError: te, OwningTeam: owner, labels: labels,
		OwnMessageLabels: r.MessageLabels != nil,
		MessageLabels:    MessageLabels(r.MessageLabels, r.GroupKey, labels.severity, labels.team),
		ID:               r.ID, PublicID: r.PublicID, Name: r.Name, Description: r.Description, Position: int(r.Place),
		IsDefault: r.IsDefault, MinimumSeverity: r.MinimumSeverity, Urgent: r.MinimumSeverity == MinimumCritical,
		GroupKey: r.GroupKey, CreatedAt: r.CreatedAt.UTC(), DeliverToTeams: r.DeliverToTeams,
		RunTeamChains: r.RunTeamChains,
		Version:       r.Version,
		Policy: Policy{
			ReopenWindowSeconds: r.ReopenWindowSeconds, GracePeriodSeconds: r.GracePeriodSeconds,
			UrgentRiseRemovesAck: r.UrgentRiseRemovesAck, SnoozeDurationsSeconds: r.SnoozeDurationsSeconds,
			ThreadBatchingWindowSeconds: r.ThreadBatchingWindowSeconds, StormThreshold: r.StormThreshold,
			Language: r.Language,
			Templates: Templates{RootMessage: textOf(r.TemplateRootMessage), Line: textOf(r.TemplateLine),
				AckTimeoutNotice: textOf(r.TemplateAckTimeoutNotice)},
			AckTimeout: AckTimeout{Enabled: r.AckTimeoutEnabled, FirstIntervalSeconds: r.AckTimeoutFirstIntervalSeconds},
			Reminders: Reminders{Enabled: r.RemindersEnabled, FirstIntervalSeconds: r.RemindersFirstIntervalSeconds,
				CapSeconds: r.RemindersCapSeconds},
			AutoUnacknowledge: r.AutoUnacknowledge,
		},
	}
}

// exportInfo sets the muster_route_info series of a Route and creates its counter series.
func (s *Service) exportInfo(publicID, name string) {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	if old, ok := s.info[publicID]; ok && old != name {
		metrics.RouteInfo.Delete(publicID, old)
	}
	s.info[publicID] = name
	metrics.RouteInfo.With(publicID, name).Set(1)
	exportCounters(publicID)
}

// exportCounters creates the series of muster_template_errors_total of a Route at zero, one per template it owns:
// increase() never counts the first sample of a series, so MusterTemplateError would otherwise miss the first error
// of each of its templates on each replica.
func exportCounters(publicID string) {
	for _, template := range metrics.RouteTemplateKinds {
		metrics.TemplateErrors.With(publicID, "", template)
	}
}

// unexportCounters removes the series of muster_template_errors_total of a deleted Route: it has no open Alert
// Groups (C-09.FR-19), so its templates no longer render.
func unexportCounters(publicID string) {
	for _, template := range metrics.RouteTemplateKinds {
		metrics.TemplateErrors.Delete(publicID, "", template)
	}
}

// unexportInfo removes the muster_route_info series of a Route.
func (s *Service) unexportInfo(publicID string) {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	if old, ok := s.info[publicID]; ok {
		metrics.RouteInfo.Delete(publicID, old)
		delete(s.info, publicID)
	}
}

// RefreshInfo makes muster_route_info list exactly the Routes that are not deleted, the Default route included, as
// the database has them, whichever replica changed them (C-08.FR-12), and gives each its series of
// muster_template_errors_total; a deleted Route loses them.
func (s *Service) RefreshInfo(ctx context.Context) error {
	// The read happens under the lock, so that a local change exported meanwhile is never undone by an older read.
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	rows, err := s.store.ListRouteInfo(ctx, s.orgID)
	if err != nil {
		return fmt.Errorf("list the routes for their info metric: %w", err)
	}
	want := make(map[string]string, len(rows))
	for _, r := range rows {
		want[r.PublicID] = r.Name
		exportCounters(r.PublicID)
	}
	for id, name := range s.info {
		if _, ok := want[id]; !ok {
			unexportCounters(id)
		}
		if want[id] != name {
			metrics.RouteInfo.Delete(id, name)
			delete(s.info, id)
		}
	}
	for id, name := range want {
		s.info[id] = name
		metrics.RouteInfo.With(id, name).Set(1)
	}
	return nil
}

// InfoChanged asks RunInfo to refresh muster_route_info: a hint said a Route changed, maybe on another replica. It
// never blocks.
func (s *Service) InfoChanged() {
	select {
	case s.infoChanged <- struct{}{}:
	default:
	}
}

// RunInfo keeps muster_route_info current until ctx ends: it refreshes it at once, after InfoChanged and at every
// tick, which also repairs a refresh that failed.
func (s *Service) RunInfo(ctx context.Context, ticks <-chan time.Time) {
	for {
		_ = s.RefreshInfo(ctx) // a failed refresh is repeated at the next tick
		select {
		case <-ctx.Done():
			return
		case <-s.infoChanged:
		case <-ticks:
		}
	}
}

// minimumOf is the Minimum severity a new Route takes: the one written; critical for the urgent mark of a 0.1 client
// without one (C-26.FR-10); none otherwise.
func minimumOf(in Input) string {
	switch {
	case in.MinimumSeverity != nil:
		return *in.MinimumSeverity
	case in.Urgent:
		return MinimumCritical
	default:
		return MinimumNone
	}
}

func text(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *s, Valid: true}
}

func textOf(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

// RestampAlerts records routeID as the Route of the current firing of the Alerts, in the Snapshot's transaction tx:
// grouping puts Alerts whose Route was deleted while their Snapshot waited for it on the Default route.
func RestampAlerts(ctx context.Context, tx dbgen.DBTX, orgID int64, alertIDs []int64, routeID int64) error {
	if err := dbgen.New(tx).RestampAlertRoutes(ctx, dbgen.RestampAlertRoutesParams{OrgID: orgID, Ids: alertIDs,
		RouteID: pgtype.Int8{Int64: routeID, Valid: true}}); err != nil {
		return fmt.Errorf("record the route of the alerts: %w", err)
	}
	return nil
}

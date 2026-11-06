// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package routing

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/matchers"
	"github.com/muster-io/muster/internal/routing/dbgen"
	"github.com/muster-io/muster/internal/teams"
)

// The values of the Group key preview (C-08.FR-5, defaults.md).
const (
	// PreviewPeriod is routing.group_key_preview_period, the period a request without one previews (P-12).
	PreviewPeriod = 24 * time.Hour
	// PreviewExamples is routing.group_key_preview_examples: how many of the largest Alert Groups each side shows.
	PreviewExamples = 5
	// PreviewMaxAlerts is routing.group_key_preview_max_alerts: the most Alerts a preview covers, the newest first.
	PreviewMaxAlerts = 10000
)

// Snapshots reads the Stored Snapshots for the Group key preview (internal/ingest).
type Snapshots interface {
	Retention(ctx context.Context) (time.Duration, error)
	Firing(ctx context.Context, since time.Time, visit func([]ingest.FiringAlert) bool) (ingest.FiringRead, error)
}

// PreviewRequest asks for a Group key preview: of the saved Route RouteID with its own Matchers, or with Matchers when
// they are not nil, or of Matchers alone for a Route that is not saved yet; with the proposed Group key, over the
// period PeriodSeconds or, when it is nil, routing.group_key_preview_period. MinimumSeverity, DestinationEntries and
// DeliverToTeams are the unsaved settings of the Route for the delivery preview (C-26.FR-12): nil takes the saved
// Route's when RouteID is given, else none, no entries and false.
type PreviewRequest struct {
	RouteID            string
	Matchers           []Matcher
	ProposedGroupKey   []string
	PeriodSeconds      *int64
	MinimumSeverity    *string
	DestinationEntries *[]DestinationEntry
	DeliverToTeams     *bool
}

// Preview is a Group key preview: how the Alerts the Route would take in the period group with its current Group key,
// absent for a Route that is not saved, and with the proposed one. Truncated says that the preview stopped at
// routing.group_key_preview_max_alerts with Stored Snapshots of the period left unread.
type Preview struct {
	PeriodSeconds int64
	Truncated     bool
	Current       *PreviewSide
	Proposed      PreviewSide
	// Delivery is the preview "Where alerts go", nil when the request gave neither entries nor deliver_to_teams and
	// no saved Route.
	Delivery *DeliveryPreview
}

// PreviewSide is how the Alerts group by one Group key: the count of distinct key values, which is the count of Alert
// Groups when Reopen windows and Grace periods are left aside, and the largest groups.
type PreviewSide struct {
	AlertGroupCount int
	Examples        []PreviewExample
}

// PreviewExample is one group of a PreviewSide: its Group key values, a missing label being empty, and its Alerts.
type PreviewExample struct {
	GroupKeyValues map[string]string
	AlertCount     int
}

// alertKey is an Alert: its Integration and its fingerprint.
type alertKey struct {
	integrationID int64
	fingerprint   string
}

// Preview previews a Group key over the Stored Snapshots of the period (C-08.FR-5). Each distinct Alert reported
// firing, with its labels from the newest Snapshot listing it and its Integration's current Static labels, counts
// when the Route takes it in the current evaluation order as routing evaluates it: a saved Route at its place, with
// its own Matchers or with the given ones, and Matchers alone as a new Route just before the Default route, where
// createRoute puts it. It reads at most PreviewMaxAlerts such Alerts, from the newest Snapshot back.
func (s *Service) Preview(ctx context.Context, req PreviewRequest) (Preview, error) {
	started := s.real.Now()
	if req.RouteID == "" && req.Matchers == nil {
		return Preview{}, &FieldError{Pointer: "", Code: CodeOneOfRequired,
			Detail: "Give route_id for a saved Route, matchers for a Route that is not saved yet, or both."}
	}
	period, err := s.previewPeriod(ctx, req.PeriodSeconds)
	if err != nil {
		return Preview{}, err
	}
	if err := checkGroupKey("/proposed_group_key", req.ProposedGroupKey); err != nil {
		return Preview{}, err
	}
	for i, m := range req.Matchers {
		if err := checkMatcher(i, m); err != nil {
			return Preview{}, err
		}
	}
	o, at, saved, st, err := s.previewOrder(ctx, req)
	if err != nil {
		return Preview{}, err
	}
	var settings *deliverySettings
	if req.RouteID != "" || req.DestinationEntries != nil || req.DeliverToTeams != nil {
		if settings, err = s.deliverySettings(ctx, req, saved); err != nil {
			return Preview{}, err
		}
	}
	alerts, read, truncated, err := s.previewAlerts(ctx, o, at, s.clock.Now().Add(-period))
	if err != nil {
		return Preview{}, err
	}
	out := Preview{PeriodSeconds: int64(period / time.Second), Truncated: truncated,
		Proposed: sideOf(alerts, req.ProposedGroupKey)}
	if settings != nil {
		d, err := s.deliveryOf(ctx, st, groupAlerts(alerts, req.ProposedGroupKey), req.ProposedGroupKey, *settings)
		if err != nil {
			return Preview{}, err
		}
		out.Delivery = &d
	}
	routeID := ""
	if saved != nil {
		current := sideOf(alerts, saved.GroupKey)
		out.Current, routeID = &current, saved.PublicID
	}
	s.log.Log(ctx, logging.GroupKeyPreviewed, logging.F("route", routeID),
		logging.F("period_seconds", out.PeriodSeconds), logging.F("snapshots_read", read.Bodies),
		logging.F("truncated", truncated), logging.F("duration_ms", s.real.Now().Sub(started).Milliseconds()))
	return out, nil
}

// previewPeriod is the period of a preview: the one asked for, at most retention.stored_snapshots, or
// routing.group_key_preview_period.
func (s *Service) previewPeriod(ctx context.Context, seconds *int64) (time.Duration, error) {
	retention, err := s.snapshots.Retention(ctx)
	if err != nil {
		return 0, err
	}
	if seconds == nil {
		return min(PreviewPeriod, retention), nil
	}
	if *seconds < 1 || *seconds > int64(retention/time.Second) {
		return 0, &FieldError{Pointer: "/period_seconds", Code: CodeOutOfRange,
			Detail: "The period is at least 1 second and at most the retention of Stored Snapshots, " +
				strconv.FormatInt(int64(retention/time.Second), 10) + " seconds."}
	}
	return time.Duration(*seconds) * time.Second, nil
}

// previewOrder is the evaluation order the preview evaluates with, the place of the previewed Route in it, and the
// saved Route when there is one. It is a copy of the Router's order, which it never changes: the saved Route with
// the given Matchers in place of its own, or a new Route with them just before the Default route.
func (s *Service) previewOrder(ctx context.Context, req PreviewRequest) (*order, int, *Route, dbgen.GetRoutingStampRow, error) {
	var saved *Route
	if req.RouteID != "" {
		rt, err := s.get(ctx, s.store, req.RouteID)
		if err != nil {
			return nil, 0, nil, dbgen.GetRoutingStampRow{}, err
		}
		saved = &rt
	}
	st, err := s.store.GetRoutingStamp(ctx, s.orgID)
	if err != nil {
		return nil, 0, nil, dbgen.GetRoutingStampRow{}, fmt.Errorf("read the routing stamp: %w", err)
	}
	current, err := s.router.order(ctx, s.store, stamp{order: st.RouteOrderVersion, versions: st.RouteVersions})
	if err != nil {
		return nil, 0, nil, dbgen.GetRoutingStampRow{}, err
	}
	o := &order{stamp: current.stamp, routes: slices.Clone(current.routes)}
	var given []matchers.Matcher
	if req.Matchers != nil {
		given = make([]matchers.Matcher, len(req.Matchers))
		for i, m := range req.Matchers {
			// The Matchers were checked, so they compile.
			given[i], _ = matchers.New(m.Label, matchers.Op(m.Op), m.Value)
		}
	}
	if saved == nil {
		at := max(len(o.routes)-1, 0)
		o.routes = slices.Insert(o.routes, at, compiled{matchers: given})
		return o, at, nil, st, nil
	}
	at := slices.IndexFunc(o.routes, func(c compiled) bool { return c.id == saved.ID })
	if at < 0 {
		// Deleted since it was read.
		return nil, 0, nil, dbgen.GetRoutingStampRow{}, ErrNotFound
	}
	if given != nil {
		o.routes[at].matchers = given
	}
	return o, at, saved, st, nil
}

// previewAlerts reads the distinct Alerts reported firing since since, newest first, and keeps the labels of those
// the Route at place at in o takes, at most PreviewMaxAlerts of them. truncated says that the limit stopped it while
// an Alert the Route takes, or a Stored Snapshot, was left unread.
func (s *Service) previewAlerts(ctx context.Context, o *order, at int, since time.Time) ([]map[string]string,
	ingest.FiringRead, bool, error) {
	seen := map[alertKey]bool{}
	var kept []map[string]string
	more := false
	takes := func(a ingest.FiringAlert) bool {
		k := alertKey{integrationID: a.IntegrationID, fingerprint: a.Fingerprint}
		if seen[k] {
			return false
		}
		seen[k] = true
		return o.first(a.Labels) == at
	}
	read, err := s.snapshots.Firing(ctx, since, func(batch []ingest.FiringAlert) bool {
		for i, a := range batch {
			if !takes(a) {
				continue
			}
			kept = append(kept, a.Labels)
			if len(kept) == PreviewMaxAlerts {
				more = slices.ContainsFunc(batch[i+1:], takes)
				return false
			}
		}
		return true
	})
	if err != nil {
		return nil, ingest.FiringRead{}, false, err
	}
	return kept, read, more || read.Unread, nil
}

// keyGroup is the Alerts of one Group key value.
type keyGroup struct {
	values []string
	count  int
	alerts []map[string]string
}

// sideOf groups the Alerts by the Group key, a missing label counting as the empty value: the count of distinct
// values and the PreviewExamples largest groups, the larger first and, at the same size, by their values.
func sideOf(alerts []map[string]string, key []string) PreviewSide {
	groups := groupAlerts(alerts, key)
	out := PreviewSide{AlertGroupCount: len(groups), Examples: make([]PreviewExample, 0, PreviewExamples)}
	for _, g := range groups[:min(len(groups), PreviewExamples)] {
		out.Examples = append(out.Examples, PreviewExample{GroupKeyValues: valuesOf(key, g.values),
			AlertCount: g.count})
	}
	return out
}

// valuesOf names the values of a Group key.
func valuesOf(key, values []string) map[string]string {
	out := make(map[string]string, len(key))
	for i, name := range key {
		out[name] = values[i]
	}
	return out
}

// groupAlerts groups the Alerts by the Group key, a missing label counting as the empty value, the larger group first
// and, at the same size, by their values.
func groupAlerts(alerts []map[string]string, key []string) []*keyGroup {
	byValue := map[string]*keyGroup{}
	var groups []*keyGroup
	for _, labels := range alerts {
		values := make([]string, len(key))
		quoted := make([]string, len(key))
		for i, name := range key {
			values[i] = labels[name]
			quoted[i] = strconv.Quote(values[i])
		}
		id := strings.Join(quoted, ",")
		g, ok := byValue[id]
		if !ok {
			g = &keyGroup{values: values}
			byValue[id] = g
			groups = append(groups, g)
		}
		g.count++
		g.alerts = append(g.alerts, labels)
	}
	slices.SortFunc(groups, func(a, b *keyGroup) int {
		return cmp.Or(cmp.Compare(b.count, a.count), slices.Compare(a.values, b.values))
	})
	return groups
}

// DeliveryPreview is the preview "Where alerts go" (C-26.FR-12): the PreviewExamples largest Alert Groups that the
// Alerts would form under the proposed Group key with their Teams and the Destinations each reaches, for every
// Destination that any such Alert Group reaches how many of all of them do, and whether Alerts of different Teams
// would share an Alert Group.
type DeliveryPreview struct {
	AlertGroups       []DeliveryGroup
	DestinationCounts []DestinationCount
	SharedKeyWarning  bool
}

// DeliveryGroup is one Alert Group of the preview: its Group key values, its Alerts, its Severity level raised to the
// Minimum severity, its Teams in the order of their ids and the Destinations it reaches in the order of their ids.
type DeliveryGroup struct {
	GroupKeyValues map[string]string
	AlertCount     int
	SeverityLevel  string
	Teams          []PreviewTeam
	Destinations   []ReachedDestination
}

// PreviewTeam is a Team that is not deleted, as the preview names it.
type PreviewTeam struct {
	ID       int64
	PublicID string
	Name     string
	Slug     string
}

// PreviewDestination is a Destination that is not deleted, as the preview names it.
type PreviewDestination struct {
	ID           int64
	PublicID     string
	Name         string
	Type         string
	Health       string
	BrokenSince  *time.Time
	BrokenReason *string
}

// ReachedDestination is a Destination an Alert Group of the preview reaches with the entries that reach it.
type ReachedDestination struct {
	Destination PreviewDestination
	Entries     []ReachedEntry
}

// ReachedEntry is an entry that reaches a Destination: the Route's, with a nil Team, or a Team's.
type ReachedEntry struct {
	Kind      string
	Team      *PreviewTeam
	Condition SeverityCondition
}

// DestinationCount is how many of all the Alert Groups of the preview reach a Destination.
type DestinationCount struct {
	Destination     PreviewDestination
	AlertGroupCount int
}

// deliverySettings are the settings of the Route that the delivery preview applies: the Minimum severity, the entries
// resolved to the Destinations' internal ids, and "Also to the Teams from the label".
type deliverySettings struct {
	minimum string
	entries []Entry
	toTeams bool
}

// deliverySettings takes the settings the request gives and, for those it omits, the saved Route's, else none, no
// entries and false; the entries are checked as a save checks them.
func (s *Service) deliverySettings(ctx context.Context, req PreviewRequest, saved *Route) (*deliverySettings, error) {
	out := deliverySettings{minimum: MinimumNone}
	var stored []DestinationEntry
	if saved != nil {
		out.minimum, out.toTeams, stored = saved.MinimumSeverity, saved.DeliverToTeams, saved.DestinationEntries
	}
	if m := req.MinimumSeverity; m != nil {
		if !slices.Contains([]string{MinimumNone, MinimumWarning, MinimumCritical}, *m) {
			return nil, &FieldError{Pointer: "/minimum_severity", Code: CodeInvalidFormat,
				Detail: "Choose none, warning or critical."}
		}
		out.minimum = *m
	}
	if req.DeliverToTeams != nil {
		out.toTeams = *req.DeliverToTeams
	}
	set, err := s.resolveDestinations(ctx, s.store,
		requestedDestinations(Input{DestinationEntries: req.DestinationEntries}, stored))
	if err != nil {
		return nil, err
	}
	for _, id := range set.publicIDs() {
		out.entries = append(out.entries, Entry{DestinationID: set[id].id, Condition: set[id].condition})
	}
	return &out, nil
}

// alertGroupFacts are what the Reach of an Alert Group of the preview depends on: its Severity level raised to the
// Minimum severity and its Teams by id.
type alertGroupFacts struct {
	group *keyGroup
	level string
	teams []int64
}

// levelRank orders the Severity levels.
var levelRank = map[string]int{"info": 1, "warning": 2, "critical": 3}

// factsOf is, for each group of Alerts, the highest Severity level of its Alerts by the Organization's mapping,
// raised to the Minimum severity (it never lowers one), and the Teams of its Alerts.
func factsOf(groups []*keyGroup, severities Severities, classes *teams.Classes, minimum string) []alertGroupFacts {
	floor := levelRank[minimum]
	out := make([]alertGroupFacts, len(groups))
	for i, g := range groups {
		level := ""
		members := map[int64]bool{}
		for _, labels := range g.alerts {
			if l := string(severities.Of(labels).Level); levelRank[l] > levelRank[level] {
				level = l
			}
			for _, m := range classes.Classify(labels).Teams {
				members[m.TeamID] = true
			}
		}
		if levelRank[level] < floor {
			level = minimum
		}
		out[i] = alertGroupFacts{group: g, level: level, teams: slices.Sorted(maps.Keys(members))}
	}
	return out
}

// reachesOf is the Reach (C-26.FR-2, FR-3) of each group under the settings.
func reachesOf(facts []alertGroupFacts, set deliverySettings, teamEntries map[int64][]Entry) [][]Reached {
	out := make([][]Reached, len(facts))
	for i, f := range facts {
		var ts []TeamEntries
		for _, id := range f.teams {
			if es := teamEntries[id]; len(es) > 0 {
				ts = append(ts, TeamEntries{TeamID: id, Entries: es})
			}
		}
		out[i] = Reach(f.level, set.entries, set.toTeams, ts)
	}
	return out
}

// deliveryOf computes the preview "Where alerts go" for the groups of Alerts under the proposed Group key and the
// settings, with the Teams as saved. It only reads.
func (s *Service) deliveryOf(ctx context.Context, st dbgen.GetRoutingStampRow, groups []*keyGroup, key []string,
	set deliverySettings) (DeliveryPreview, error) {
	severities, err := severitiesOf(st.SeverityLabel, st.SeverityMapping)
	if err != nil {
		return DeliveryPreview{}, err
	}
	classes, err := s.router.teams.At(ctx, s.router.classify(s.store.DB()),
		teams.Stamp{Label: st.TeamLabel, Version: st.TeamsVersion})
	if err != nil {
		return DeliveryPreview{}, err
	}
	facts := factsOf(groups, severities, classes, set.minimum)
	teamEntries, err := s.previewTeamEntries(ctx, facts, set.toTeams)
	if err != nil {
		return DeliveryPreview{}, err
	}
	reaches := reachesOf(facts, set, teamEntries)
	counts := ReachCounts(reaches)
	refs, err := s.previewTeams(ctx, facts)
	if err != nil {
		return DeliveryPreview{}, err
	}
	dests, err := s.previewDestinations(ctx, slices.Sorted(maps.Keys(counts)))
	if err != nil {
		return DeliveryPreview{}, err
	}
	out := DeliveryPreview{AlertGroups: make([]DeliveryGroup, 0, PreviewExamples),
		DestinationCounts: make([]DestinationCount, 0, len(counts)),
		SharedKeyWarning:  set.toTeams && !slices.Contains(key, st.TeamLabel)}
	for i, f := range facts[:min(len(facts), PreviewExamples)] {
		g := DeliveryGroup{GroupKeyValues: valuesOf(key, f.group.values), AlertCount: f.group.count,
			SeverityLevel: f.level, Teams: make([]PreviewTeam, 0, len(f.teams)),
			Destinations: make([]ReachedDestination, 0, len(reaches[i]))}
		for _, id := range f.teams {
			if t, ok := refs[id]; ok {
				g.Teams = append(g.Teams, t)
			}
		}
		for _, r := range reaches[i] {
			dest, ok := dests[r.DestinationID]
			if !ok {
				continue
			}
			d := ReachedDestination{Destination: dest, Entries: make([]ReachedEntry, 0, len(r.Entries))}
			for _, e := range r.Entries {
				entry := ReachedEntry{Kind: e.Kind, Condition: e.Condition}
				if e.Kind == EntryTeam {
					t, ok := refs[e.TeamID]
					if !ok {
						continue
					}
					entry.Team = &t
				}
				d.Entries = append(d.Entries, entry)
			}
			if len(d.Entries) > 0 {
				g.Destinations = append(g.Destinations, d)
			}
		}
		out.AlertGroups = append(out.AlertGroups, g)
	}
	for _, id := range slices.Sorted(maps.Keys(counts)) {
		if dest, ok := dests[id]; ok {
			out.DestinationCounts = append(out.DestinationCounts, DestinationCount{Destination: dest,
				AlertGroupCount: counts[id]})
		}
	}
	return out, nil
}

// previewTeamEntries reads the Destination entries of the Teams of the groups, by Team, none unless the Route delivers
// to the Teams.
func (s *Service) previewTeamEntries(ctx context.Context, facts []alertGroupFacts, toTeams bool) (
	map[int64][]Entry, error) {
	members := map[int64]bool{}
	for _, f := range facts {
		for _, id := range f.teams {
			members[id] = true
		}
	}
	if !toTeams || len(members) == 0 {
		return nil, nil
	}
	rows, err := s.store.ListPreviewTeamEntries(ctx, dbgen.ListPreviewTeamEntriesParams{OrgID: s.orgID,
		TeamIds: slices.Sorted(maps.Keys(members))})
	if err != nil {
		return nil, fmt.Errorf("list the destinations of the teams of the alerts: %w", err)
	}
	out := map[int64][]Entry{}
	for _, r := range rows {
		out[r.TeamID] = append(out[r.TeamID], Entry{DestinationID: r.DestinationID,
			Condition: SeverityCondition(r.SeverityCondition)})
	}
	return out, nil
}

// previewTeams names the Teams of the groups.
func (s *Service) previewTeams(ctx context.Context, facts []alertGroupFacts) (map[int64]PreviewTeam, error) {
	out := map[int64]PreviewTeam{}
	if !slices.ContainsFunc(facts, func(f alertGroupFacts) bool { return len(f.teams) > 0 }) {
		return out, nil
	}
	members := map[int64]bool{}
	for _, f := range facts {
		for _, id := range f.teams {
			members[id] = true
		}
	}
	rows, err := s.store.ListPreviewTeams(ctx, dbgen.ListPreviewTeamsParams{OrgID: s.orgID,
		TeamIds: slices.Sorted(maps.Keys(members))})
	if err != nil {
		return nil, fmt.Errorf("list the teams: %w", err)
	}
	for _, r := range rows {
		out[r.ID] = PreviewTeam{ID: r.ID, PublicID: r.PublicID, Name: r.Name, Slug: r.Slug}
	}
	return out, nil
}

// previewDestinations names the Destinations by id; one deleted since is left out.
func (s *Service) previewDestinations(ctx context.Context, ids []int64) (map[int64]PreviewDestination, error) {
	out := make(map[int64]PreviewDestination, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.store.ListPreviewDestinations(ctx, dbgen.ListPreviewDestinationsParams{OrgID: s.orgID, Ids: ids})
	if err != nil {
		return nil, fmt.Errorf("list the destinations of the preview: %w", err)
	}
	for _, r := range rows {
		d := PreviewDestination{ID: r.ID, PublicID: r.PublicID, Name: r.Name, Type: r.Type, Health: r.Health,
			BrokenReason: textOf(r.BrokenReason)}
		if r.BrokenSince.Valid {
			since := r.BrokenSince.Time.UTC()
			d.BrokenSince = &since
		}
		out[r.ID] = d
	}
	return out, nil
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package routing

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/matchers"
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
// period PeriodSeconds or, when it is nil, routing.group_key_preview_period.
type PreviewRequest struct {
	RouteID          string
	Matchers         []Matcher
	ProposedGroupKey []string
	PeriodSeconds    *int64
}

// Preview is a Group key preview: how the Alerts the Route would take in the period group with its current Group key,
// absent for a Route that is not saved, and with the proposed one. Truncated says that the preview stopped at
// routing.group_key_preview_max_alerts with Stored Snapshots of the period left unread.
type Preview struct {
	PeriodSeconds int64
	Truncated     bool
	Current       *PreviewSide
	Proposed      PreviewSide
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
	o, at, saved, err := s.previewOrder(ctx, req)
	if err != nil {
		return Preview{}, err
	}
	alerts, read, truncated, err := s.previewAlerts(ctx, o, at, s.clock.Now().Add(-period))
	if err != nil {
		return Preview{}, err
	}
	out := Preview{PeriodSeconds: int64(period / time.Second), Truncated: truncated,
		Proposed: sideOf(alerts, req.ProposedGroupKey)}
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
func (s *Service) previewOrder(ctx context.Context, req PreviewRequest) (*order, int, *Route, error) {
	var saved *Route
	if req.RouteID != "" {
		rt, err := s.get(ctx, s.store, req.RouteID)
		if err != nil {
			return nil, 0, nil, err
		}
		saved = &rt
	}
	st, err := s.store.GetRoutingStamp(ctx, s.orgID)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("read the routing stamp: %w", err)
	}
	current, err := s.router.order(ctx, s.store, stamp{order: st.RouteOrderVersion, versions: st.RouteVersions})
	if err != nil {
		return nil, 0, nil, err
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
		return o, at, nil, nil
	}
	at := slices.IndexFunc(o.routes, func(c compiled) bool { return c.id == saved.ID })
	if at < 0 {
		// Deleted since it was read.
		return nil, 0, nil, ErrNotFound
	}
	if given != nil {
		o.routes[at].matchers = given
	}
	return o, at, saved, nil
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
}

// sideOf groups the Alerts by the Group key, a missing label counting as the empty value: the count of distinct
// values and the PreviewExamples largest groups, the larger first and, at the same size, by their values.
func sideOf(alerts []map[string]string, key []string) PreviewSide {
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
	}
	slices.SortFunc(groups, func(a, b *keyGroup) int {
		return cmp.Or(cmp.Compare(b.count, a.count), slices.Compare(a.values, b.values))
	})
	out := PreviewSide{AlertGroupCount: len(groups), Examples: make([]PreviewExample, 0, PreviewExamples)}
	for _, g := range groups[:min(len(groups), PreviewExamples)] {
		values := make(map[string]string, len(key))
		for i, name := range key {
			values[name] = g.values[i]
		}
		out.Examples = append(out.Examples, PreviewExample{GroupKeyValues: values, AlertCount: g.count})
	}
	return out
}

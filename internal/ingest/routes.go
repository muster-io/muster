// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/ingest/dbgen"
	"github.com/muster-io/muster/internal/integrations"
	"github.com/muster-io/muster/internal/publicid"
)

// routeQueries are the queries of the learned Alertmanager routes.
type routeQueries interface {
	FindRoutesIntegration(ctx context.Context, arg dbgen.FindRoutesIntegrationParams) (
		dbgen.FindRoutesIntegrationRow, error)
	ListAlertmanagerRoutes(ctx context.Context, arg dbgen.ListAlertmanagerRoutesParams) (
		[]dbgen.ListAlertmanagerRoutesRow, error)
}

// AlertmanagerRoute is an Alertmanager route Muster learned from the groupKeys of an Integration (C-06.FR-18).
type AlertmanagerRoute struct {
	RoutePath string
	// LearnedRepeatInterval is nil before an interval is learned; ResolveByAbsenceAfter is stale_after.
	LearnedRepeatInterval *time.Duration
	ResolveByAbsenceAfter time.Duration
	TruncatedGroupCount   int64
	// LongIntervalWarning is a learned interval above processing.long_repeat_warning; RecommendedSnippet, set with it,
	// is the route with snippet.repeat_interval.
	LongIntervalWarning bool
	RecommendedSnippet  string
}

// Routes lists the Alertmanager routes of the Integration integration, which is not deleted, in the order of their
// paths; an unknown Integration is integrations.ErrNotFound. The built-in Integration has none: its synthetic
// Snapshots are never resolved by absence and teach nothing.
func (v *AlertsView) Routes(ctx context.Context, integration string) ([]AlertmanagerRoute, error) {
	pid, err := publicid.Parse(publicid.Integration, integration)
	if err != nil {
		return nil, integrations.ErrNotFound
	}
	in, err := v.routes.FindRoutesIntegration(ctx, dbgen.FindRoutesIntegrationParams{OrgID: v.orgID, PublicID: pid})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, integrations.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find the integration %s: %w", pid, err)
	}
	if in.Builtin {
		return []AlertmanagerRoute{}, nil
	}
	rows, err := v.routes.ListAlertmanagerRoutes(ctx, dbgen.ListAlertmanagerRoutesParams{OrgID: v.orgID,
		IntegrationID: in.ID})
	if err != nil {
		return nil, fmt.Errorf("list the alertmanager routes of %s: %w", pid, err)
	}
	out := make([]AlertmanagerRoute, len(rows))
	for i, r := range rows {
		out[i] = routeOf(r.RoutePath, r.LearnedRepeatIntervalMs.Int64, r.LearnedRepeatIntervalMs.Valid,
			r.TruncatedGroupCount)
	}
	return out, nil
}

func routeOf(path string, learnedMs int64, learned bool, truncated int64) AlertmanagerRoute {
	r := AlertmanagerRoute{RoutePath: path, TruncatedGroupCount: truncated, ResolveByAbsenceAfter: StaleAfter(nil)}
	if learned {
		d := time.Duration(learnedMs) * time.Millisecond
		r.LearnedRepeatInterval, r.ResolveByAbsenceAfter = &d, StaleAfter(&d)
		if d > integrations.LongRepeatWarning {
			r.LongIntervalWarning, r.RecommendedSnippet = true, RouteSnippet(path, d)
		}
	}
	return r
}

// RouteSnippet is the Alertmanager route fragment that sets snippet.repeat_interval on the route of routePath, with
// the matchers of each level below the top-level route, for a route whose learned repeat interval is learned. The
// route path comes from a received groupKey, so it and every matcher are written as double-quoted YAML scalars in
// ASCII with escapes: no character of the groupKey can end a comment or a scalar.
func RouteSnippet(routePath string, learned time.Duration) string {
	interval := amDuration(integrations.RepeatInterval)
	var b strings.Builder
	b.WriteString("# Muster learned a repeat interval of " + amDuration(learned.Round(time.Minute)) +
		" for the Alertmanager route\n")
	b.WriteString("# " + strconv.QuoteToASCII(routePath) + ", so it takes long to resolve its Alerts by absence. Set\n")
	b.WriteString("# the repeat interval of that route between 5 and 15 minutes; it is shown below the top-level route\n")
	b.WriteString("# with its matchers.\n")
	levels := routeLevels(routePath)
	if len(levels) == 0 {
		b.WriteString("route:\n  repeat_interval: " + interval + "\n")
		return b.String()
	}
	b.WriteString("route:\n  routes:\n")
	indent := "    "
	for i, matchers := range levels {
		if len(matchers) == 0 {
			b.WriteString(indent + "- matchers: []\n")
		} else {
			b.WriteString(indent + "- matchers:\n")
			for _, m := range matchers {
				b.WriteString(indent + "    - " + strconv.QuoteToASCII(m) + "\n")
			}
		}
		if i == len(levels)-1 {
			b.WriteString(indent + "  repeat_interval: " + interval + "\n")
			break
		}
		b.WriteString(indent + "  routes:\n")
		indent += "    "
	}
	return b.String()
}

// routeLevels splits a route path such as {}/{team="web"}/{a="b",c=~"d"} into the matchers of each level below the
// top-level route; a slash or comma inside a quoted value does not split.
func routeLevels(path string) [][]string {
	var levels [][]string
	for i, level := range splitOutside(path, '/') {
		inner := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(level), "{"), "}")
		if i == 0 {
			continue
		}
		matchers := []string{}
		for _, m := range splitOutside(inner, ',') {
			if m = strings.TrimSpace(m); m != "" {
				matchers = append(matchers, m)
			}
		}
		levels = append(levels, matchers)
	}
	return levels
}

// splitOutside splits s at sep where it is neither inside a quoted value nor inside braces nested below the top.
func splitOutside(s string, sep byte) []string {
	var parts []string
	start, depth, quoted := 0, 0, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quoted && c == '\\':
			i++
		case c == '"':
			quoted = !quoted
		case quoted:
		case c == '{':
			depth++
		case c == '}':
			depth = max(depth-1, 0)
		case c == sep && depth == 0:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

// amDuration writes d in the duration syntax of Alertmanager, such as 10m or 2h.
func amDuration(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	case d >= time.Minute && d%time.Minute == 0:
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	default:
		return strconv.FormatInt(int64(d/time.Second), 10) + "s"
	}
}

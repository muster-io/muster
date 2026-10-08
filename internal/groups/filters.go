// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/matchers"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/publicid"
)

// ListRange is alert_group.list_range: the time range of the list, the counts and the statistics when none is given.
const ListRange = 7 * 24 * time.Hour

// The field codes of FieldError.
const (
	CodeUnknownID   = "unknown_id"
	CodeOutOfRange  = "out_of_range"
	CodeInvalid     = "invalid_format"
	CodeUnsupported = "unsupported"
)

// FieldError is a query parameter of a read that is valid but cannot be used: an unknown id, a range that ends before
// it starts, an unknown time zone; the API answers it as 422 at Pointer.
type FieldError struct {
	Pointer string
	Code    string
	Detail  string
}

func (e *FieldError) Error() string { return e.Pointer + ": " + e.Code }

// Filter is the filters of the Alert Group list and its counts (C-09.FR-13). The zero Filter selects the open Alert
// Groups whose lifetime overlaps the last ListRange.
type Filter struct {
	// Statuses are the statuses to list; none means open (firing, acknowledged and snoozed), or every status for a
	// number. The counts ignore them.
	Statuses []Status
	// Routes and Integrations are public_ids; an Alert Group matches one of each kind given.
	Routes       []string
	Integrations []string
	Severities   []organization.SeverityLevel
	Urgent       *bool
	// ResolvedBy is ResolvedByUser or ResolvedBySystem, ResolveReason the reason of a system resolve.
	ResolvedBy    *string
	ResolveReason *string
	// Reopened selects a Reopen count above zero when true, of zero when false.
	Reopened *bool
	// Matchers are matched against the common labels: a label the Alerts do not share counts as absent.
	Matchers []matchers.Matcher
	// From and To are the time range; the default is the last ListRange. A number ignores it.
	From, To *time.Time
	// Number is the #N to find; a Query of the form #N is the same.
	Number *int64
	// Query is searched in the title and summary, case-insensitively and inside words.
	Query string
	// Owner is a User's public_id, OwnerMe for the User Me, or OwnerNone for the Alert Groups nobody owns
	// (C-10.FR-13); empty does not filter. Me is the caller, whose own Alert Groups OwnerMe selects.
	Owner string
	Me    audit.Actor
	// SnoozedNoEnd selects the Alert Groups snoozed with no end when true, and the others when false.
	SnoozedNoEnd *bool
	// DeliveryProblem selects the Alert Groups with a Delivery problem when true, and the others when false.
	DeliveryProblem *bool
}

// The values of Filter.Owner that name no User.
const (
	OwnerMe   = "me"
	OwnerNone = "none"
)

// query is a Filter resolved to the parameters of the list queries.
type query struct {
	statuses     []string
	number       pgtype.Int8
	from, to     time.Time
	routes       []int64
	integrations []int64
	severities   []string
	urgent       pgtype.Bool
	resolvedBy   pgtype.Text
	reason       pgtype.Text
	reopened     pgtype.Bool
	contains     []byte
	pattern      pgtype.Text
	ownerSet     bool
	owner        pgtype.Int8
	snoozedNoEnd pgtype.Bool
	problem      pgtype.Bool
	// rowwise are the Matchers the database does not apply.
	rowwise []matchers.Matcher
}

// allStatuses are the statuses a number finds by default.
var allStatuses = []Status{StatusFiring, StatusAcknowledged, StatusSnoozed, StatusResolved}

// resolve turns f into the parameters of the list queries at now: the default range, the number of a #N query, the
// pattern of a text query, the ids of the Routes and Integrations, the = Matchers as one containment and the rest for
// Go.
func (s *Service) resolve(ctx context.Context, f Filter, now time.Time) (query, error) {
	var q query
	number := f.Number
	search := strings.TrimSpace(f.Query)
	if n, ok := numberQuery(search); ok {
		number, search = &n, ""
	}
	if number != nil {
		q.number = pgtype.Int8{Int64: *number, Valid: true}
	}
	statuses := f.Statuses
	if len(statuses) == 0 {
		statuses = openStatuses
		if number != nil {
			statuses = allStatuses
		}
	}
	for _, st := range statuses {
		q.statuses = append(q.statuses, string(st))
	}
	from, to, err := Range(f.From, f.To, now)
	if err != nil {
		return q, err
	}
	q.from, q.to = from, to
	if q.routes, err = s.routeIDs(ctx, f.Routes, "/query/route"); err != nil {
		return q, err
	}
	if q.integrations, err = s.integrationIDs(ctx, f.Integrations, "/query/integration"); err != nil {
		return q, err
	}
	q.routes, q.integrations = orNone(q.routes), orNone(q.integrations)
	q.severities = []string{}
	for _, l := range f.Severities {
		q.severities = append(q.severities, string(l))
	}
	if f.Urgent != nil {
		q.urgent = pgtype.Bool{Bool: *f.Urgent, Valid: true}
	}
	q.resolvedBy, q.reason = text(f.ResolvedBy), text(f.ResolveReason)
	if f.Reopened != nil {
		q.reopened = pgtype.Bool{Bool: *f.Reopened, Valid: true}
	}
	if q.ownerSet, q.owner, err = s.owner(ctx, f.Owner, f.Me); err != nil {
		return q, err
	}
	if f.SnoozedNoEnd != nil {
		q.snoozedNoEnd = pgtype.Bool{Bool: *f.SnoozedNoEnd, Valid: true}
	}
	if f.DeliveryProblem != nil {
		q.problem = pgtype.Bool{Bool: *f.DeliveryProblem, Valid: true}
	}
	contains := map[string]string{}
	for _, m := range f.Matchers {
		if _, taken := contains[m.Name]; m.Op == matchers.Equal && m.Value != "" && !taken {
			contains[m.Name] = m.Value
			continue
		}
		q.rowwise = append(q.rowwise, m)
	}
	if len(contains) > 0 {
		q.contains, _ = json.Marshal(contains) // a map of strings always encodes
	}
	if search != "" {
		q.pattern = pgtype.Text{String: "%" + escapeLike(search) + "%", Valid: true}
	}
	return q, nil
}

// owner resolves the Owner filter: whether it is set, and the User it selects, none for OwnerNone. OwnerMe is the
// calling User; a Service account, which never owns an Alert Group, is a FieldError, as is an unknown User.
func (s *Service) owner(ctx context.Context, owner string, me audit.Actor) (bool, pgtype.Int8, error) {
	switch owner {
	case "":
		return false, pgtype.Int8{}, nil
	case OwnerNone:
		return true, pgtype.Int8{}, nil
	case OwnerMe:
		if me.Kind != audit.ActorUser {
			return false, pgtype.Int8{}, &FieldError{Pointer: "/query/owner", Code: CodeUnsupported,
				Detail: "A Service account owns no Alert Groups; name a user instead of me."}
		}
		return true, pgtype.Int8{Int64: me.ID, Valid: true}, nil
	}
	refs, err := lookup(publicid.User, "user", []string{owner}, "/query/owner", func(ids []string) ([]idRef, error) {
		rows, err := s.store.ListUsersByPublicID(ctx, dbgen.ListUsersByPublicIDParams{OrgID: s.orgID, PublicIds: ids})
		out := make([]idRef, len(rows))
		for i, r := range rows {
			out[i] = idRef(r)
		}
		return out, err
	})
	if err != nil {
		return false, pgtype.Int8{}, err
	}
	return true, pgtype.Int8{Int64: refs[0].ID, Valid: true}, nil
}

// orNone is ids, or an empty list for nil, which the queries read as no filter.
func orNone(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}

// Range is the time range of a read at now: [from, to), where to defaults to the end of now — the database's
// microsecond after it, so that an Alert Group that started at this very time is in — and from to ListRange before
// to. A range that does not start before it ends is a FieldError.
func Range(from, to *time.Time, now time.Time) (time.Time, time.Time, error) {
	end := now.UTC().Truncate(time.Microsecond).Add(time.Microsecond)
	if to != nil {
		end = to.UTC()
	}
	start := end.Add(-ListRange)
	if from != nil {
		start = from.UTC()
	}
	if !start.Before(end) {
		return start, end, &FieldError{Pointer: "/query/from", Code: CodeOutOfRange,
			Detail: "The time range must start before it ends."}
	}
	return start, end, nil
}

// numberQuery reads a search of the form #N.
func numberQuery(q string) (int64, bool) {
	rest, ok := strings.CutPrefix(q, "#")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	return n, err == nil && n > 0
}

// escapeLike escapes the wildcards of ILIKE, so that the text is searched as it is.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// routeIDs reads the ids of Routes by public_id, deleted ones included; one that does not exist is a FieldError at
// pointer.
func (s *Service) routeIDs(ctx context.Context, publicIDs []string, pointer string) ([]int64, error) {
	refs, err := lookup(publicid.Route, "route", publicIDs, pointer, func(ids []string) ([]idRef, error) {
		rows, err := s.store.ListRoutesByPublicID(ctx, dbgen.ListRoutesByPublicIDParams{OrgID: s.orgID,
			PublicIds: ids})
		out := make([]idRef, len(rows))
		for i, r := range rows {
			out[i] = idRef(r)
		}
		return out, err
	})
	return idsOf(refs), err
}

// integrationIDs reads the ids of Integrations by public_id, as routeIDs does.
func (s *Service) integrationIDs(ctx context.Context, publicIDs []string, pointer string) ([]int64, error) {
	refs, err := lookup(publicid.Integration, "integration", publicIDs, pointer, func(ids []string) ([]idRef, error) {
		rows, err := s.store.ListIntegrationsByPublicID(ctx, dbgen.ListIntegrationsByPublicIDParams{OrgID: s.orgID,
			PublicIds: ids})
		out := make([]idRef, len(rows))
		for i, r := range rows {
			out[i] = idRef(r)
		}
		return out, err
	})
	return idsOf(refs), err
}

// idRef is a Route or an Integration by id, public_id and name.
type idRef struct {
	ID       int64
	PublicID string
	Name     string
}

// lookup reads the entities of a kind by public_id with read; a malformed or unknown one is a FieldError at pointer.
func lookup(kind publicid.Prefix, name string, publicIDs []string, pointer string,
	read func([]string) ([]idRef, error)) ([]idRef, error) {
	if len(publicIDs) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(publicIDs))
	for _, raw := range publicIDs {
		id, err := publicid.Parse(kind, raw)
		if err != nil {
			return nil, &FieldError{Pointer: pointer, Code: CodeUnknownID, Detail: "No such " + name + "."}
		}
		ids = append(ids, id)
	}
	refs, err := read(ids)
	if err != nil {
		return nil, fmt.Errorf("read the %ss of the filter: %w", name, err)
	}
	found := make(map[string]bool, len(refs))
	for _, r := range refs {
		found[r.PublicID] = true
	}
	for _, id := range ids {
		if !found[id] {
			return nil, &FieldError{Pointer: pointer, Code: CodeUnknownID, Detail: "No such " + name + "."}
		}
	}
	return refs, nil
}

func idsOf(refs []idRef) []int64 {
	if refs == nil {
		return nil
	}
	out := make([]int64, len(refs))
	for i, r := range refs {
		out[i] = r.ID
	}
	return out
}

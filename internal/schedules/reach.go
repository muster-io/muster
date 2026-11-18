// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package schedules

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/schedules/dbgen"
)

// WarningUnreachable is the kind of the warning of a segment in which a Mention of its On-call cannot reach a
// Destination of their Team, or one in whose Threads a chain that Pages the Schedule writes (C-27.FR-8).
const WarningUnreachable = "unreachable"

// The reasons a Mention of an On-call cannot reach a Destination (ReachReason, C-27.FR-8).
const (
	ReachNoAccountLink          = "no_account_link"
	ReachBridgeNoNativeMentions = "bridge_no_native_mentions"
	ReachWebhookNoThreadReply   = "webhook_no_thread_reply"
	ReachNotInDiscussionGroup   = "not_in_discussion_group"
)

// webhookEvents is the mode of an outgoing webhook that sends events only, which mentions nobody and so warns of
// nothing; template and both send its templates.
const webhookEvents = "events"

// DestinationRef names a Destination with its type and health.
type DestinationRef struct {
	ID           int64
	PublicID     string
	Name         string
	Type         string
	Health       string
	BrokenSince  *time.Time
	BrokenReason *string
}

// Unreachable is a Destination that a Mention of a person cannot reach, with the messenger — the Destination's type:
// its Connection's, or webhook — and why (OnCallReach).
type Unreachable struct {
	Destination DestinationRef
	Messenger   string
	Reason      string
	// GatewayReaches is whether the person has a confirmed contact at a Notification gateway, so that a call through
	// it reaches them (C-27.FR-8); whether that gateway is Broken is not considered.
	GatewayReaches bool
}

// teamDestination is a Destination of the reach of a Schedule — of its Team, or of a Route or a Team whose chain
// Pages the Schedule — with what decides whether a Mention of a person reaches it: its Connection, the mode of an
// outgoing webhook and whether its template replies in the Thread, whether the info a Bridge's Connection last read
// declares native_mentions, and the Users the last check of a Telegram Destination saw outside its discussion group.
type teamDestination struct {
	DestinationRef
	Connection     *int64
	WebhookMode    string
	ReplyInThread  bool
	NativeMentions bool
	Absent         []int64
}

// reachReason is why a Mention of the person user, with Account links in the Identity spaces spaces, cannot reach the
// Destination d, empty when it can (C-27.FR-8): an outgoing webhook that sends its templates without a "reply in
// thread" request; a bridge whose info does not declare native_mentions — or was never read, for which delivery
// renders plain names too; no Account link in the Identity space of the Destination's Connection; or a Telegram
// Destination whose last check saw the person outside its discussion group. An outgoing webhook in events mode
// mentions nobody and warns of nothing, nor does a messenger Destination without a Connection.
func reachReason(d teamDestination, user int64, spaces map[string]bool) string {
	if d.Type == mentions.TypeWebhook {
		if d.WebhookMode != webhookEvents && !d.ReplyInThread {
			return ReachWebhookNoThreadReply
		}
		return ""
	}
	if d.Connection == nil {
		return ""
	}
	if d.Type == mentions.TypeBridge && !d.NativeMentions {
		return ReachBridgeNoNativeMentions
	}
	space := mentions.IdentitySpace(d.Type, d.Connection)
	switch {
	case space == "":
		return ""
	case !spaces[space]:
		return ReachNoAccountLink
	case d.Type == mentions.TypeTelegram && slices.Contains(d.Absent, user):
		return ReachNotInDiscussionGroup
	}
	return ""
}

// reach is what the reach of the people of some Schedules reads: the Destinations of the reach of each Schedule, by
// Schedule id, the Identity spaces in which each person has an Account link and who has a confirmed contact at a
// Notification gateway, by User id.
type reach struct {
	schedules map[int64][]teamDestination
	spaces    map[int64]map[string]bool
	gateway   map[int64]bool
}

// unreachable are the Destinations of the reach of the Schedule schedule that a Mention of the person user cannot
// reach, by name; none for a Schedule without Destinations.
func (r reach) unreachable(schedule, user int64) []Unreachable {
	out := []Unreachable{}
	for _, d := range r.schedules[schedule] {
		if reason := reachReason(d, user, r.spaces[user]); reason != "" {
			out = append(out, Unreachable{Destination: d.DestinationRef, Messenger: d.Type, Reason: reason,
				GatewayReaches: r.gateway[user]})
		}
	}
	return out
}

// reachDestination is the teamDestination of a row of ListTeamReach, or of ListChainReach as one.
func reachDestination(r dbgen.ListTeamReachRow) teamDestination {
	d := teamDestination{DestinationRef: DestinationRef{ID: r.ID, PublicID: r.PublicID, Name: r.Name, Type: r.Type,
		Health: r.Health}, WebhookMode: r.WebhookMode, ReplyInThread: r.ReplyInThread,
		NativeMentions: r.NativeMentions, Absent: r.TelegramAbsentUserIds}
	if r.BrokenSince.Valid {
		at := r.BrokenSince.Time
		d.BrokenSince = &at
	}
	if r.BrokenReason.Valid {
		reason := r.BrokenReason.String
		d.BrokenReason = &reason
	}
	if r.ConnectionID.Valid {
		id := r.ConnectionID.Int64
		d.Connection = &id
	}
	return d
}

// byName orders Destinations by name, ignoring case, then by id.
func byName(a, b teamDestination) int {
	return cmp.Or(cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.ID, b.ID))
}

// readReach reads the reach of the people users in the Schedules schedules, each with its Team by Schedule id
// (C-27.FR-8): the Destinations of the reach of each Schedule — those of its Team, which are the Team's part of the
// reach rule of C-26.FR-1 whatever their conditions, since an On-call is warned about every Destination of their Team,
// and those in whose Threads the chains that Page the Schedule write, of the Routes and the Teams that name them
// (C-32.FR-8) —, each once and by name; the person's Account links and who has a confirmed contact at a Notification
// gateway. It only reads.
func (s *Service) readReach(ctx context.Context, q Queries, schedules map[int64]int64, users map[int64]bool) (reach,
	error) {
	out := reach{schedules: map[int64][]teamDestination{}, spaces: map[int64]map[string]bool{},
		gateway: map[int64]bool{}}
	if len(schedules) == 0 || len(users) == 0 {
		return out, nil
	}
	teams := map[int64]bool{}
	for _, team := range schedules {
		teams[team] = true
	}
	rows, err := q.ListTeamReach(ctx, dbgen.ListTeamReachParams{OrgID: s.orgID, TeamIds: sortedKeys(teams)})
	if err != nil {
		return reach{}, fmt.Errorf("read the destinations of the teams: %w", err)
	}
	byTeam := map[int64][]teamDestination{}
	for _, r := range rows {
		byTeam[r.TeamID] = append(byTeam[r.TeamID], reachDestination(r))
	}
	ids := make(map[int64]bool, len(schedules))
	for id := range schedules {
		ids[id] = true
	}
	chained, err := q.ListChainReach(ctx, dbgen.ListChainReachParams{OrgID: s.orgID, ScheduleIds: sortedKeys(ids)})
	if err != nil {
		return reach{}, fmt.Errorf("read the destinations of the chains that page the schedules: %w", err)
	}
	byChain := map[int64][]teamDestination{}
	for _, r := range chained {
		byChain[r.ScheduleID] = append(byChain[r.ScheduleID], reachDestination(dbgen.ListTeamReachRow{ID: r.ID,
			PublicID: r.PublicID, Name: r.Name, Type: r.Type, Health: r.Health, BrokenSince: r.BrokenSince,
			BrokenReason: r.BrokenReason, ConnectionID: r.ConnectionID, WebhookMode: r.WebhookMode,
			ReplyInThread: r.ReplyInThread, NativeMentions: r.NativeMentions,
			TelegramAbsentUserIds: r.TelegramAbsentUserIds}))
	}
	for id, team := range schedules {
		ds := slices.Clone(byTeam[team])
		for _, d := range byChain[id] {
			if !slices.ContainsFunc(ds, func(x teamDestination) bool { return x.ID == d.ID }) {
				ds = append(ds, d)
			}
		}
		slices.SortStableFunc(ds, byName)
		out.schedules[id] = ds
	}
	links, err := q.ListAccountLinkSpaces(ctx, dbgen.ListAccountLinkSpacesParams{OrgID: s.orgID,
		UserIds: sortedKeys(users)})
	if err != nil {
		return reach{}, fmt.Errorf("read the account links of the on-calls: %w", err)
	}
	for _, l := range links {
		if out.spaces[l.UserID] == nil {
			out.spaces[l.UserID] = map[string]bool{}
		}
		out.spaces[l.UserID][l.IdentitySpace] = true
	}
	reached, err := q.ListGatewayReachUsers(ctx, dbgen.ListGatewayReachUsersParams{OrgID: s.orgID,
		UserIds: sortedKeys(users)})
	if err != nil {
		return reach{}, fmt.Errorf("read the contacts of the on-calls at notification gateways: %w", err)
	}
	for _, u := range reached {
		out.gateway[u] = true
	}
	return out, nil
}

// onCalls are the people On-call in some segment of the Results, by User id.
func onCalls(results ...Result) map[int64]bool {
	out := map[int64]bool{}
	for _, r := range results {
		for _, seg := range r.Segments {
			if seg.User != nil {
				out[seg.User.ID] = true
			}
		}
	}
	return out
}

// withReach adds to the Result res of the Schedule sc its warnings unreachable (C-27.FR-8).
func (s *Service) withReach(ctx context.Context, q Queries, sc Schedule, res *Result) error {
	r, err := s.readReach(ctx, q, map[int64]int64{sc.ID: sc.Team.ID}, onCalls(*res))
	if err != nil {
		return err
	}
	res.Warnings = append(res.Warnings, unreachableWarnings(*res, sc.ID, r)...)
	return nil
}

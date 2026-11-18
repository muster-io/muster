// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/routing"
)

// Where chains Page (C-32.FR-8): a chain Pages only in the Threads of its own Destinations. Its Route's chain in those
// of the Route's Destinations that have the Alert Group's Root message; a Team's chain in those of the Team's
// Destinations that have it and, where none has, in those of the Route's; a chain that the Route and Teams both name in
// the union (P-70). A source owns a Destination with the Root message when an entry of its own reached it and it still
// names the Destination. The entries recorded on the delivery are every Route or Team whose entry ever reached it, as
// routing.Reach — the one rule of who an Alert Group reaches, never a copy of it — found them at a level the Alert
// Group had: EnsureDelivery appends them and never removes one. So a source keeps the Destinations its entries
// reached before the Alert Group's level fell, as their Root messages stay (C-26.FR-4), and never owns one whose Root
// message only another source's entry brought, such as a shared channel that another Team names for any level and it
// only for critical, while the level is below critical. A Page by Mention replies in Mattermost, Slack, Telegram — in
// the discussion group, as every Thread reply (C-14.FR-3) — and through Bridges, and an outgoing webhook takes it
// through its "reply in thread" request (C-32.FR-11). One without that request takes no Page, but still counts as
// having the Root message when the targets are chosen: a Team whose Destinations with it are all of those does not
// fall back to the Route's, so a step never rings another team's chat. An outgoing webhook in the events mode has no
// Root message: it takes paged as an event (webhookevents.go).

// pageTypes are the Destination types a Page by Mention replies in; an outgoing webhook only with a "reply in thread"
// request.
var pageTypes = []string{TypeMattermost, TypeSlack, TypeTelegram, TypeBridge, TypeWebhook}

// pageQueries are the queries of the Pages by Mention.
type pageQueries interface {
	ListGroupTeams(ctx context.Context, arg dbgen.ListGroupTeamsParams) ([]int64, error)
	ListPageRoots(ctx context.Context, arg dbgen.ListPageRootsParams) ([]dbgen.ListPageRootsRow, error)
	ListPageLinks(ctx context.Context, arg dbgen.ListPageLinksParams) ([]dbgen.ListPageLinksRow, error)
	GetPageReply(ctx context.Context, arg dbgen.GetPageReplyParams) (dbgen.GetPageReplyRow, error)
	ListPageReplyUsers(ctx context.Context, arg dbgen.ListPageReplyUsersParams) ([]dbgen.ListPageReplyUsersRow, error)
	ListPageReplies(ctx context.Context, arg dbgen.ListPageRepliesParams) ([]dbgen.ListPageRepliesRow, error)
	ListEndedPages(ctx context.Context, arg dbgen.ListEndedPagesParams) ([]dbgen.ListEndedPagesRow, error)
}

// pageRoot is a Destination where the Alert Group has its Root message: its type, its Connection and what that can
// mention (C-28.FR-2), whether it is Broken and, for an outgoing webhook, whether it has a "reply in thread" request.
type pageRoot struct {
	id         int64
	typ        string
	connection *int64
	conn       mentions.Connection
	broken     bool
	replies    bool
}

// pageRootOf is the pageRoot of a row of ListPageRoots.
func pageRootOf(x dbgen.ListPageRootsRow) pageRoot {
	r := pageRoot{id: x.ID, typ: x.Type, connection: int8Of(x.ConnectionID), broken: x.Health == healthBroken,
		replies: x.WebhookReplies, conn: mentions.Connection{Type: x.Type, UsergroupsRead: x.UsergroupsRead,
			GroupMentions: x.GroupMentions, NativeMentions: x.NativeMentions, Messenger: x.Messenger,
			Everyone: x.EveryoneMentions}}
	if r.connection != nil {
		r.conn.ID = *r.connection
	}
	return r
}

// pages reports whether a Page by Mention replies in the Destination: one of pageTypes, an outgoing webhook only with
// a "reply in thread" request.
func (r pageRoot) pages() bool {
	return slices.Contains(pageTypes, r.typ) && (r.typ != TypeWebhook || r.replies)
}

// PageTargets is groups.PageTargets: the Destinations of a run that can carry a Page of the Alert Group g, read in
// the dispatcher's transaction tx after the lock of g (C-32.FR-8, FR-9). The Destinations where g has its Root
// message, of any type, each with the entries that own it (owners): those that name it now, of the Route and, where it
// delivers to the Teams, of the Teams of g, whose source reached its delivery; of those, the Destinations chosen by
// chooseTargets; then of those the Destinations that reply with Pages (pageRoot.pages), and for a page_team step only
// those whose Connection can mention the Team — a Team mention for it — or the token for everyone in the chat, as
// mentions.Can says with what the Connection can mention (C-28.FR-2): never Telegram, a bridge only with the group
// mentions or the token it declares, an outgoing webhook always, as data. Each target lists the people of the Page
// it reaches and those it names by the plain name (linkPeople), and whether it is Broken, which withholds its reply
// (C-11.FR-19). Reads only: the Enqueue of the same change locks the deliveries.
func (s *Service) PageTargets(ctx context.Context, tx groups.DBTX, g *groups.Group, r groups.PageRequest) (
	[]groups.PageTarget, error) {
	q := s.store.queries(tx)
	route, err := q.GetRouteDelivery(ctx, dbgen.GetRouteDeliveryParams{OrgID: s.orgID, ID: g.RouteID})
	if err != nil {
		return nil, fmt.Errorf("read the route of alert group #%d: %w", g.Number, err)
	}
	var teams []int64
	if route.DeliverToTeams {
		if teams, err = q.ListGroupTeams(ctx, dbgen.ListGroupTeamsParams{OrgID: s.orgID,
			AlertGroupID: g.ID}); err != nil {
			return nil, fmt.Errorf("list the teams of alert group #%d: %w", g.Number, err)
		}
	}
	n, err := s.namingOf(ctx, q, g.RouteID, route.DeliverToTeams, teams)
	if err != nil {
		return nil, fmt.Errorf("read the destinations of alert group #%d: %w", g.Number, err)
	}
	roots, err := q.ListPageRoots(ctx, dbgen.ListPageRootsParams{OrgID: s.orgID, AlertGroupID: g.ID})
	if err != nil {
		return nil, fmt.Errorf("list the root messages of alert group #%d: %w", g.Number, err)
	}
	byID := map[int64]pageRoot{}
	owned := make([]routing.Reached, 0, len(roots))
	for _, x := range roots {
		byID[x.ID] = pageRootOf(x)
		var d entryDelivery
		if err := json.Unmarshal(x.Entries, &d.reaching); err != nil {
			return nil, fmt.Errorf("read the entries of alert group #%d in destination %d: %w", g.Number, x.ID, err)
		}
		if es := owners(d, n.names(x.ID, teams)); len(es) > 0 {
			owned = append(owned, routing.Reached{DestinationID: x.ID, Entries: es})
		}
	}
	chosen := chooseTargets(r.Sources, owned, func(id int64) bool { _, ok := byID[id]; return ok })
	out := make([]groups.PageTarget, 0, len(chosen))
	for _, c := range chosen {
		root := byID[c.DestinationID]
		if !root.pages() {
			continue
		}
		if r.Team != 0 || r.Everyone != "" {
			ok, err := s.canMention(ctx, tx, root, r)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
		}
		c.Withheld = root.broken
		out = append(out, c)
	}
	return out, s.linkPeople(ctx, q, out, byID, r.People)
}

// owners are the entries of names — those that name a Destination now, whatever their Severity condition — whose
// source reached the delivery d there: the Route, or the same Team (entryDelivery.holds). The entries of d are every
// Route or Team whose entry routing.Reach found at a level the Alert Group had, so a source owns the Destination even
// after the level fell below its entry's condition, and never by the entry of another source that brought the Root
// message.
func owners(d entryDelivery, names []routing.Reaching) []routing.Reaching {
	var out []routing.Reaching
	for _, e := range names {
		if d.holds(e.Kind, e.TeamID) {
			out = append(out, e)
		}
	}
	return out
}

// chooseTargets are the Destinations of the reach where the chain of the sources Pages, in id order, among those
// rooted says have the Root message (C-32.FR-8): for the Route those an entry of the Route reaches; for a Team those
// an entry of that Team reaches and, where none of them is rooted, those of the Route; each once, of the source
// PageSourceRoute when the Route is a source and one of its entries reaches it, else PageSourceTeam.
func chooseTargets(sources []groups.PageSource, reached []routing.Reached, rooted func(int64) bool) []groups.PageTarget {
	of := func(kind string, team int64) []int64 {
		var out []int64
		for _, r := range reached {
			if rooted(r.DestinationID) && slices.ContainsFunc(r.Entries, func(e routing.Reaching) bool {
				return e.Kind == kind && (kind == routing.EntryRoute || e.TeamID == team)
			}) {
				out = append(out, r.DestinationID)
			}
		}
		return out
	}
	routeDests := of(routing.EntryRoute, 0)
	source := map[int64]string{}
	for _, s := range sources {
		switch s.Kind {
		case groups.PageSourceRoute:
			for _, id := range routeDests {
				source[id] = groups.PageSourceRoute
			}
		case groups.PageSourceTeam:
			ids := of(routing.EntryTeam, s.TeamID)
			if len(ids) == 0 {
				ids = routeDests
			}
			for _, id := range ids {
				if _, ok := source[id]; !ok {
					source[id] = groups.PageSourceTeam
				}
			}
		}
	}
	out := make([]groups.PageTarget, 0, len(source))
	for _, id := range slices.Sorted(maps.Keys(source)) {
		out = append(out, groups.PageTarget{DestinationID: id, Source: source[id]})
	}
	return out
}

// canMention reports whether the Destination's Connection can mention what a page_team step Pages (C-28.FR-2), as
// mentions.Can says with what the Connection can mention: the token for everyone in the chat, or the Team — by a Team
// mention for the Connection that it can carry now, and in an outgoing webhook as data.
func (s *Service) canMention(ctx context.Context, tx groups.DBTX, root pageRoot, r groups.PageRequest) (bool, error) {
	if r.Everyone != "" {
		ok, _ := mentions.Can(root.typ, root.conn,
			mentions.EntryTarget{Kind: mentions.TargetEveryone, Everyone: r.Everyone})
		return ok, nil
	}
	if ok, _ := mentions.Can(root.typ, root.conn, mentions.EntryTarget{Kind: mentions.TargetTeam}); !ok {
		return false, nil
	}
	if root.typ == TypeWebhook {
		return true, nil
	}
	if s.teamMentions == nil || root.connection == nil {
		return false, nil
	}
	_, ok, err := s.teamMentions.ForConnection(ctx, tx, r.Team, *root.connection)
	if err != nil {
		return false, fmt.Errorf("read the team mention of team %d: %w", r.Team, err)
	}
	return ok, nil
}

// linkPeople sets on each target the people its reply reaches (C-32.FR-11, C-18.FR-4): those with an Account link in
// its Identity space (mentions.IdentitySpace), whom it mentions, and every person in an outgoing webhook, which takes
// them as data. Through a bridge whose stored info does not declare native_mentions, or has none, the people with an
// Account link are Named instead: the bridge shows the plain name, which reaches nobody (C-22.FR-16).
func (s *Service) linkPeople(ctx context.Context, q queries, targets []groups.PageTarget, roots map[int64]pageRoot,
	people []int64) error {
	if len(people) == 0 || len(targets) == 0 {
		return nil
	}
	var spaces []string
	for _, t := range targets {
		r := roots[t.DestinationID]
		if sp := mentions.IdentitySpace(r.typ, r.connection); sp != "" && !slices.Contains(spaces, sp) {
			spaces = append(spaces, sp)
		}
	}
	var links []dbgen.ListPageLinksRow
	if len(spaces) > 0 {
		var err error
		if links, err = q.ListPageLinks(ctx, dbgen.ListPageLinksParams{OrgID: s.orgID, UserIds: people,
			Spaces: spaces}); err != nil {
			return fmt.Errorf("read the account links of the people of a page: %w", err)
		}
	}
	for i := range targets {
		r := roots[targets[i].DestinationID]
		if r.typ == TypeWebhook {
			targets[i].Linked = slices.Clone(people)
			continue
		}
		space := mentions.IdentitySpace(r.typ, r.connection)
		for _, id := range people {
			if !slices.ContainsFunc(links, func(l dbgen.ListPageLinksRow) bool {
				return l.UserID == id && l.IdentitySpace == space
			}) {
				continue
			}
			if r.typ == TypeBridge && !r.conn.NativeMentions {
				targets[i].Named = append(targets[i].Named, id)
			} else {
				targets[i].Linked = append(targets[i].Linked, id)
			}
		}
	}
	return nil
}

// pageOf is the Page target of the reply of e in the Destination id, false when the Page does not reply there.
func pageOf(e groups.Recorded, id int64) (*groups.PageTarget, bool) {
	if e.Page == nil {
		return nil, false
	}
	for i := range e.Page.Targets {
		if e.Page.Targets[i].DestinationID == id {
			return &e.Page.Targets[i], true
		}
	}
	return nil, false
}

// pageParams set on the Thread reply of a paged in a Destination its Page, the people it mentions and its source.
func pageParams(p dbgen.InsertThreadReplyParams, e groups.Recorded, t *groups.PageTarget) dbgen.InsertThreadReplyParams {
	p.PageID = pgtype.Int8{Int64: e.Page.ID, Valid: true}
	p.PagedUserIds = t.Linked
	if p.PagedUserIds == nil {
		p.PagedUserIds = []int64{}
	}
	p.PageSource = pgtype.Text{String: t.Source, Valid: true}
	return p
}

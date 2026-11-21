// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/routing"
)

// The people of the Pages of the fixtures: ivan, linked in the Identity space of the Mattermost Connection, and petr
// and carol, linked nowhere.
const (
	ivanID  = 41
	petrID  = 42
	carolID = 43
	pageID  = 900
)

// fakePageDB are the Pages of the fake database: what each reply says, its people, the Account links and the state of
// each Page, which the settler ends, how many times the Pages to settle were listed, what the Connection of each
// Destination can mention, as ListPageRoots reads it, and the reason each person's Page recorded when it was queued.
type fakePageDB struct {
	pages   map[int64]dbgen.GetPageReplyRow
	people  map[int64][]int64
	links   []dbgen.ListPageLinksRow
	state   map[int64]string
	group   map[int64]int64
	lists   int
	caps    map[int64]dbgen.ListPageRootsRow
	reasons map[int64]string
}

func (p *fakePageDB) init() {
	if p.pages == nil {
		p.pages, p.people, p.state, p.group = map[int64]dbgen.GetPageReplyRow{}, map[int64][]int64{},
			map[int64]string{}, map[int64]int64{}
	}
}

// zeroWidth is the zero-width space that neutralizes an `@` of a text (messages.Neutralize).
var zeroWidth = string(rune(0x200b))

// pagePeople names the people of the fixtures.
var pagePeople = map[int64]dbgen.ListPageReplyUsersRow{
	ivanID:  {ID: ivanID, PublicID: "SRAAAAAAAAAAIV", Name: "Ivan", Login: "ivan"},
	petrID:  {ID: petrID, PublicID: "SRAAAAAAAAAAPE", Name: "Petr", Login: "petr"},
	carolID: {ID: carolID, PublicID: "SRAAAAAAAAAACA", Name: "Carol", Login: "carol"},
}

func (f *fakeDB) ListPageRoots(_ context.Context, arg dbgen.ListPageRootsParams) ([]dbgen.ListPageRootsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListPageRoots"); err != nil {
		return nil, err
	}
	var out []dbgen.ListPageRootsRow
	for _, d := range f.deliveries {
		ds := f.dests[d.dest]
		coming := d.state == "pending" && d.publishedAt == nil
		if d.group != arg.AlertGroupID || (d.messageID == nil && !coming) || d.heldBy != 0 || ds == nil ||
			ds.deleted || d.desiredRetire || slices.Contains([]string{"withheld", "deleted_in_messenger", "retired"},
			d.state) {
			continue
		}
		// deliveries.entries is never null: '[]' by default, as for a delivery made before the entries were recorded.
		entries := d.entries
		if entries == nil {
			entries = []byte("[]")
		}
		c := f.pageDB.caps[ds.id]
		out = append(out, dbgen.ListPageRootsRow{ID: ds.id, Type: ds.typ, ConnectionID: nullInt(ds.connection),
			Health: ds.health, Entries: entries, WebhookReplies: ds.typ == delivery.TypeWebhook && !ds.noReplies,
			UsergroupsRead: c.UsergroupsRead, GroupMentions: c.GroupMentions, NativeMentions: c.NativeMentions,
			Messenger: c.Messenger || ds.typ != delivery.TypeBridge, EveryoneMentions: c.EveryoneMentions})
	}
	slices.SortFunc(out, func(a, b dbgen.ListPageRootsRow) int { return int(a.ID - b.ID) })
	return out, nil
}

func (f *fakeDB) ListPageLinks(_ context.Context, arg dbgen.ListPageLinksParams) ([]dbgen.ListPageLinksRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListPageLinks"); err != nil {
		return nil, err
	}
	var out []dbgen.ListPageLinksRow
	for _, l := range f.pageDB.links {
		if slices.Contains(arg.UserIds, l.UserID) && slices.Contains(arg.Spaces, l.IdentitySpace) {
			out = append(out, l)
		}
	}
	return out, nil
}

func (f *fakeDB) GetPageReply(_ context.Context, arg dbgen.GetPageReplyParams) (dbgen.GetPageReplyRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetPageReply"); err != nil {
		return dbgen.GetPageReplyRow{}, err
	}
	p, ok := f.pageDB.pages[arg.ID]
	if !ok {
		return p, pgx.ErrNoRows
	}
	return p, nil
}

func (f *fakeDB) ListPageReplyUsers(_ context.Context, arg dbgen.ListPageReplyUsersParams) (
	[]dbgen.ListPageReplyUsersRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListPageReplyUsers"); err != nil {
		return nil, err
	}
	var out []dbgen.ListPageReplyUsersRow
	for _, id := range f.pageDB.people[arg.PageID] {
		u := pagePeople[id]
		u.Reason = f.pageDB.reasons[id]
		for _, l := range f.pageDB.links {
			if l.UserID == id && l.IdentitySpace == arg.IdentitySpace {
				u.Username, u.ExternalID = u.Login, "u-"+u.Login
			}
		}
		out = append(out, u)
	}
	return out, nil
}

func (f *fakeDB) ListPageReplies(_ context.Context, arg dbgen.ListPageRepliesParams) ([]dbgen.ListPageRepliesRow,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListPageReplies"); err != nil {
		return nil, err
	}
	var out []dbgen.ListPageRepliesRow
	for _, r := range f.replies {
		if r.page == arg.PageID {
			out = append(out, dbgen.ListPageRepliesRow{State: r.state, PagedUserIds: r.pagedUsers})
		}
	}
	return out, nil
}

func (f *fakeDB) ListEndedPages(_ context.Context, arg dbgen.ListEndedPagesParams) ([]dbgen.ListEndedPagesRow,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListEndedPages"); err != nil {
		return nil, err
	}
	var out []dbgen.ListEndedPagesRow
	for _, id := range slices.Sorted(func(yield func(int64) bool) {
		for id := range f.pageDB.state {
			if !yield(id) {
				return
			}
		}
	}) {
		if id <= arg.After || f.pageDB.state[id] != "pending" || slices.ContainsFunc(f.replies,
			func(r *fakeReply) bool {
				return r.page == id && (r.state == "pending" || r.state == "collecting")
			}) {
			continue
		}
		if len(out) == int(arg.Lim) {
			break
		}
		out = append(out, dbgen.ListEndedPagesRow{ID: id, AlertGroupID: f.pageDB.group[id]})
	}
	f.pageDB.lists++
	return out, nil
}

func (f *fakeDB) DropBrokenPageReplies(_ context.Context, arg dbgen.DropBrokenPageRepliesParams) (
	[]dbgen.DropBrokenPageRepliesRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("DropBrokenPageReplies"); err != nil {
		return nil, err
	}
	var out []dbgen.DropBrokenPageRepliesRow
	for _, r := range slices.SortedFunc(slices.Values(f.replies), func(a, b *fakeReply) int {
		return cmp.Compare(a.id, b.id)
	}) {
		if len(out) == int(arg.Lim) {
			break
		}
		if d := f.dests[r.dest]; r.page == 0 || (r.state != "pending" && r.state != "collecting") || d == nil ||
			d.health != "broken" || (r.owner != "" && r.until.After(arg.Now)) {
			continue
		}
		r.state, r.owner, r.until = "dropped", "", time.Time{}
		out = append(out, dbgen.DropBrokenPageRepliesRow{PageID: r.page, AlertGroupID: r.group})
	}
	return out, nil
}

// fakeSettler records the outcomes of Pages and ends a Page once its replies ended; err fails every settle, and
// failPages fails the settles of those Pages only.
type fakeSettler struct {
	db        *fakeDB
	outcomes  []groups.PageOutcome
	err       error
	failPages []int64
}

func (s *fakeSettler) SettlePage(_ context.Context, _ groups.DBTX, groupID int64, o groups.PageOutcome) (
	func(context.Context), error) {
	if s.err != nil {
		return nil, s.err
	}
	if slices.Contains(s.failPages, o.Page) {
		return nil, fmt.Errorf("settle page %d: boom", o.Page)
	}
	s.outcomes = append(s.outcomes, o)
	if o.Ended && s.db.pageDB.group[o.Page] == groupID {
		s.db.pageDB.state[o.Page] = "ended"
	}
	return func(context.Context) {}, nil
}

// fakeTeamMentions are the Team mentions of Team 3 for the Mattermost Connection of the fixtures.
type fakeTeamMentions struct{ err error }

func (m fakeTeamMentions) ForConnection(_ context.Context, _ mentions.DBTX, team, conn int64) (mentions.TeamMention,
	bool, error) {
	if m.err != nil {
		return mentions.TeamMention{}, false, m.err
	}
	return mentions.TeamMention{Group: "payments"}, team == 3 && conn == connID, nil
}

// pageEnv is a published env whose worker settles the Pages and reads the Team mentions, with ivan linked in the
// Identity space of the Mattermost Connection.
func pageEnv(t *testing.T) (*env, *fakeSettler) {
	t.Helper()
	e := published(t)
	e.db.pageDB.init()
	e.db.pageDB.links = []dbgen.ListPageLinksRow{{UserID: ivanID, IdentitySpace: "mattermost:" + strconv.Itoa(connID)}}
	s := &fakeSettler{db: e.db}
	e.w.Pages, e.w.TeamMentions = s, fakeTeamMentions{}
	e.svc = delivery.New(delivery.Config{OrgID: orgID, Store: e.store, Business: e.business,
		Renderer: stubRenderer{}, TeamMentions: fakeTeamMentions{}})
	return e, s
}

// addPage adds a Page of people of the fixtures.
func (e *env) addPage(id int64, row dbgen.GetPageReplyRow, people ...int64) {
	e.db.pageDB.pages[id], e.db.pageDB.people[id], e.db.pageDB.state[id], e.db.pageDB.group[id] = row, people,
		"pending", groupID
}

// rounds runs rounds of the worker until its Thread replies are sent, one reply of a delivery each round.
func (e *env) rounds(t *testing.T) {
	t.Helper()
	for range 4 {
		e.round(t)
	}
}

// paged is the paged event of the Page id into the targets.
func pagedEvent(seq, id int64, targets ...groups.PageTarget) groups.Recorded {
	return groups.Recorded{Seq: seq, Event: groups.EventPaged, Variant: groups.VariantAny, Loudness: groups.Loud,
		Mentions: []groups.Mention{groups.MentionPaged}, Page: &groups.Page{ID: id, Targets: targets}}
}

// TestPageReplies is C-32.FR-9, C-11.FR-2 and C-18.FR-4: paged makes one Loud Thread reply in each Destination of its
// Page only, due at once without the Thread batching window, never batched with another reply; it mentions the people
// with an Account link in the Destination's Identity space by it, in the Lead "Step {n}: …, you are needed", names
// the others in "Page not delivered", and once delivered reports the people it reached and that the replies ended.
func TestPageReplies(t *testing.T) {
	e, s := pageEnv(t)
	e.addPage(pageID, dbgen.GetPageReplyRow{StepNumber: 1, Importance: "important", AgainSeconds: 300, OnCall: true,
		ScheduleName: "Main", ScheduleTeamName: "devops", ChainTeamName: "payments"}, ivanID, petrID)
	g := e.group(groups.StatusFiring, "a")
	e.enqueue(t, g, groups.System, alertsAdded(2, groups.StatusFiring, "fp00"),
		pagedEvent(3, pageID, groups.PageTarget{DestinationID: destMM, Source: groups.PageSourceRoute,
			Linked: []int64{ivanID}}, groups.PageTarget{DestinationID: 99, Source: groups.PageSourceTeam}))
	var page *fakeReply
	for _, r := range e.db.replies {
		if r.event == "paged" {
			if page != nil {
				t.Fatal("two replies of the page")
			}
			page = r
		}
	}
	if page == nil || page.page != pageID || !slices.Equal(page.pagedUsers, []int64{ivanID}) ||
		page.pageSource != groups.PageSourceRoute || page.state != "pending" || !page.next.Equal(e.business.Now()) ||
		page.loudness != "loud" || !slices.Equal(page.mentions, []string{"paged"}) {
		t.Fatalf("page reply %+v", page)
	}
	e.rounds(t)
	var call *messages.Message
	var targets []mentions.Target
	for _, c := range e.replies() {
		if c.Message.Lead != nil {
			call, targets = &c.Message, c.Targets
		}
	}
	if call == nil || call.Lead.Before != "Step 1: " || call.Lead.After != ", you are needed" || len(targets) != 1 ||
		targets[0].User == nil || targets[0].User.Username != "ivan" {
		t.Fatalf("page call %+v %+v", call, targets)
	}
	text := strings.ReplaceAll(call.Text(), zeroWidth, "")
	if text != "Page not delivered: @petr has no linked Mattermost account\n"+
		"Chain of the Route payments · On-call of devops Main · important, again in 5 min" {
		t.Errorf("text %q", text)
	}
	if len(s.outcomes) != 1 || s.outcomes[0].Page != pageID || !s.outcomes[0].Ended || !s.outcomes[0].Delivered ||
		!slices.Equal(s.outcomes[0].Reached, []int64{ivanID}) {
		t.Errorf("outcomes %+v", s.outcomes)
	}
	e.round(t)
	if len(s.outcomes) != 1 {
		t.Errorf("settled again %+v", s.outcomes)
	}
}

// TestPageReplyRetried: a reply attempted again after a transient failure does not settle its Page, which would lock
// its Alert Group and the Page for nothing; once sent it does, itself, before any round settles the Pages.
func TestPageReplyRetried(t *testing.T) {
	e, s := pageEnv(t)
	e.addPage(pageID, dbgen.GetPageReplyRow{StepNumber: 1, Importance: "normal"}, ivanID)
	g := e.group(groups.StatusFiring, "a")
	e.enqueue(t, g, groups.System, pagedEvent(2, pageID, groups.PageTarget{DestinationID: destMM,
		Source: groups.PageSourceRoute, Linked: []int64{ivanID}}))
	e.rec.Script(deliverytest.MethodReply, deliverytest.Failure(delivery.OutcomeTransient, "HTTP 503"))
	e.round(t)
	if len(s.outcomes) != 0 {
		t.Errorf("settled after a transient failure %+v", s.outcomes)
	}
	e.business.Advance(time.Minute)
	// The rounds list no Pages to settle, so only the reply that ended can settle its Page.
	e.db.fail["ListEndedPages"] = errors.New("boom")
	for range 4 {
		_, _ = e.w.Round(t.Context())
	}
	delete(e.db.fail, "ListEndedPages")
	if len(s.outcomes) != 1 || !s.outcomes[0].Ended || !s.outcomes[0].Delivered ||
		!slices.Equal(s.outcomes[0].Reached, []int64{ivanID}) {
		t.Errorf("outcomes %+v", s.outcomes)
	}
}

// TestPageRepliesOfTeams: the reply of a page_team step mentions the Team mention of the Connection or the token for
// everyone, says which Team's chain it comes from, and a later Mention of a rule says "again"; a reply that is
// dropped, as while its Destination is Broken, settles its Page as ended without reaching anyone (C-11.FR-19).
func TestPageRepliesOfTeams(t *testing.T) {
	e, s := pageEnv(t)
	e.addPage(901, dbgen.GetPageReplyRow{StepNumber: 6, TeamID: pgtype.Int8{Int64: 3, Valid: true},
		TeamName: "payments", ChainTeamName: "payments"})
	e.addPage(902, dbgen.GetPageReplyRow{StepNumber: 2, Everyone: "here"})
	e.addPage(903, dbgen.GetPageReplyRow{StepNumber: 3, Importance: "important", Again: true}, ivanID)
	g := e.group(groups.StatusFiring, "a")
	target := groups.PageTarget{DestinationID: destMM, Source: groups.PageSourceTeam}
	e.enqueue(t, g, groups.System, pagedEvent(2, 901, target), pagedEvent(3, 902, target),
		pagedEvent(4, 903, groups.PageTarget{DestinationID: destMM, Source: groups.PageSourceRoute,
			Linked: []int64{ivanID}}))
	e.rounds(t)
	var got []string
	for _, c := range e.replies() {
		var names []string
		for _, tg := range c.Targets {
			names = append(names, tg.Kind+":"+tg.Group+tg.Everyone)
		}
		got = append(got, strings.Join(names, ",")+"|"+strings.ReplaceAll(c.Message.Text(), zeroWidth, ""))
	}
	want := []string{"group:payments|Chain of the Team payments · Team payments",
		"everyone:here|Chain of the Route payments · everyone in the chat",
		"user:|Chain of the Route payments · @ivan · important, again"}
	if !slices.Equal(got, want) {
		t.Errorf("replies\n%q\nwant\n%q", got, want)
	}
	if len(s.outcomes) != 3 {
		t.Errorf("outcomes %+v", s.outcomes)
	}
	e.addPage(904, dbgen.GetPageReplyRow{StepNumber: 1, Importance: "normal"}, ivanID)
	e.breakDest(destMM, "down")
	e.enqueue(t, g, groups.System, pagedEvent(5, 904, groups.PageTarget{DestinationID: destMM,
		Linked: []int64{ivanID}, Withheld: true}))
	e.round(t)
	last := s.outcomes[len(s.outcomes)-1]
	if last.Page != 904 || !last.Ended || last.Delivered || len(last.Reached) != 0 {
		t.Errorf("dropped %+v", s.outcomes)
	}
}

// leavingOutMentions is the adapter of a bridge whose Mention line leaves out every group and mention of everyone of
// its replies for length (C-12.FR-11), which it reports in Outcome.LeftOutMentions, and the people whose public ids
// are ids, which it reports in Outcome.LeftOut.
type leavingOutMentions struct {
	*deliverytest.Recorder
	ids []string
}

func (a leavingOutMentions) Reply(ctx context.Context, c delivery.Call, root delivery.Root,
	m delivery.Message) delivery.Outcome {
	out := a.Recorder.Reply(ctx, c, root, m)
	if out.Kind == delivery.OutcomeOK {
		for _, tg := range c.Targets {
			if tg.Kind != mentions.TargetUser {
				out.LeftOutMentions = append(out.LeftOutMentions, tg)
			}
		}
		out.LeftOut = a.ids
	}
	return out
}

// pageMentionDetails are the details of the mention events of e, each decoded as a map, so that a field the event
// should not carry shows.
func pageMentionDetails(t *testing.T, e *env) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ev := range e.mentionEvents() {
		var d map[string]any
		if err := json.Unmarshal(ev.Detail, &d); err != nil {
			t.Fatalf("detail %s: %v", ev.Detail, err)
		}
		if ev.Kind != string(delivery.EventMentionNotDelivered) || ev.Loudness != "quiet" || len(ev.Mentions) != 0 ||
			ev.AlertGroupID.Int64 != groupID || ev.DestinationID != destMM {
			t.Errorf("event %s %s %v group %d destination %d", ev.Kind, ev.Loudness, ev.Mentions,
				ev.AlertGroupID.Int64, ev.DestinationID)
		}
		out = append(out, d)
	}
	return out
}

// TestPageReplyLeftOutMentions is C-28.FR-3, C-12.FR-11 and C-32.FR-9 (#690): a Page reply whose line leaves out the
// Team mention or the mention of everyone that its Page names, for length, records "Mention not delivered" with the
// reason message_too_long, Quiet and without Mentions, naming the Mention and — in place of an entry, which a Page
// does not have — the Page, by its step and the name of its chain; with no entry, event or target it shows in the
// Timeline and warns on no Destination. A person the line leaves out is not reached, as before (#645), and records no
// event; a Page of people only, and a reply that keeps its Mentions, record nothing.
func TestPageReplyLeftOutMentions(t *testing.T) {
	e, s := pageEnv(t)
	e.w.Adapters[delivery.TypeMattermost] = leavingOutMentions{Recorder: e.rec,
		ids: []string{pagePeople[ivanID].PublicID}}
	e.addPage(901, dbgen.GetPageReplyRow{StepNumber: 6, TeamID: pgtype.Int8{Int64: 3, Valid: true},
		TeamName: "payments", ChainTeamName: "payments", ChainName: "Payments primary"})
	e.addPage(902, dbgen.GetPageReplyRow{StepNumber: 2, Everyone: "here", ChainName: "Night"})
	e.addPage(903, dbgen.GetPageReplyRow{StepNumber: 1, Importance: "normal", ChainName: "Night"}, ivanID)
	target := groups.PageTarget{DestinationID: destMM, Source: groups.PageSourceTeam}
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, pagedEvent(2, 901, target),
		pagedEvent(3, 902, target), pagedEvent(4, 903, groups.PageTarget{DestinationID: destMM,
			Source: groups.PageSourceRoute, Linked: []int64{ivanID}}))
	e.rounds(t)
	if r := e.replies(); len(r) != 3 {
		t.Fatalf("replies %+v", r)
	}
	want := []map[string]any{
		{"mention": map[string]any{"kind": "group", "name": "payments"},
			"page": map[string]any{"step": float64(6), "chain_name": "Payments primary"}, "reason": "message_too_long"},
		{"mention": map[string]any{"kind": "everyone", "name": "here"},
			"page": map[string]any{"step": float64(2), "chain_name": "Night"}, "reason": "message_too_long"}}
	got := pageMentionDetails(t, e)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("mention events\n%v\nwant\n%v", got, want)
	}
	for _, r := range e.db.replies {
		if r.page == 903 && (r.state != "sent" || len(r.pagedUsers) != 0) {
			t.Errorf("the reply of the Page of ivan, left out: %s, mentions %v", r.state, r.pagedUsers)
		}
	}
	for _, o := range s.outcomes {
		if o.Page == 903 && o.Ended && (!o.Delivered || len(o.Reached) != 0) {
			t.Errorf("outcome of the Page of ivan %+v", o)
		}
	}

	// A reply whose lease went to another replica during the call records nothing; the replica that sends it again
	// records the Mention once.
	e.addPage(904, dbgen.GetPageReplyRow{StepNumber: 3, Everyone: "channel", ChainName: "Night"})
	e.business.Advance(time.Minute)
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, pagedEvent(5, 904, target))
	var reply *fakeReply
	e.rec.Script(deliverytest.MethodReply, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK},
		Then: func() {
			for _, r := range e.db.replies {
				if r.page == 904 {
					reply, r.owner = r, "r2"
				}
			}
		}})
	e.round(t)
	if reply == nil || len(e.mentionEvents()) != 2 {
		t.Fatalf("recorded without the lease: %v", e.eventKinds())
	}
	reply.until = e.real.Now()
	e.real.Advance(delivery.Lease)
	e.round(t)
	got = pageMentionDetails(t, e)
	if reply.state != "sent" || len(got) != 3 || fmt.Sprint(got[2]) != fmt.Sprint(map[string]any{
		"mention": map[string]any{"kind": "everyone", "name": "channel"},
		"page":    map[string]any{"step": float64(3), "chain_name": "Night"}, "reason": "message_too_long"}) {
		t.Errorf("after the reply sent again: %s %v", reply.state, got)
	}

	// A reply that keeps them records nothing.
	kept, _ := pageEnv(t)
	kept.addPage(901, dbgen.GetPageReplyRow{StepNumber: 6, TeamID: pgtype.Int8{Int64: 3, Valid: true},
		TeamName: "payments", ChainName: "Payments primary"})
	kept.addPage(902, dbgen.GetPageReplyRow{StepNumber: 2, Everyone: "here", ChainName: "Night"})
	kept.enqueue(t, kept.group(groups.StatusFiring, "a"), groups.System, pagedEvent(2, 901, target),
		pagedEvent(3, 902, target))
	kept.rounds(t)
	if r := kept.replies(); len(r) != 2 || len(kept.mentionEvents()) != 0 {
		t.Errorf("kept: %d replies, events %v", len(r), kept.eventKinds())
	}
}

// TestPageReplyFailures: a reply whose Page cannot be read, whose people or Team mention cannot be read is retried;
// a settle that fails is logged without failing the round, which settles the Page later; a list that fails fails it.
func TestPageReplyFailures(t *testing.T) {
	e, s := pageEnv(t)
	g := e.group(groups.StatusFiring, "a")
	e.addPage(905, dbgen.GetPageReplyRow{StepNumber: 6, TeamID: pgtype.Int8{Int64: 3, Valid: true}})
	errBoom := errors.New("boom")
	for _, name := range []string{"GetPageReply", "ListPageReplyUsers"} {
		e.db.fail[name] = errBoom
		e.enqueue(t, g, groups.System, pagedEvent(2, 905, groups.PageTarget{DestinationID: destMM}))
		e.round(t)
		if len(e.replies()) != 0 {
			t.Errorf("%s: sent", name)
		}
		delete(e.db.fail, name)
		e.db.replies = nil
	}
	e.w.TeamMentions = fakeTeamMentions{err: errBoom}
	e.enqueue(t, g, groups.System, pagedEvent(2, 905, groups.PageTarget{DestinationID: destMM}))
	e.round(t)
	if len(e.replies()) != 0 {
		t.Error("team mention: sent")
	}
	e.w.TeamMentions = fakeTeamMentions{}
	e.db.replies = nil
	e.db.pageDB.state[905] = "pending"
	s.err = errBoom
	e.enqueue(t, g, groups.System, pagedEvent(2, 905, groups.PageTarget{DestinationID: destMM}))
	if _, err := e.w.Round(t.Context()); err != nil {
		t.Errorf("a failing settle failed the round: %v", err)
	}
	if !strings.Contains(e.log.String(), `"work":"page"`) {
		t.Errorf("no log line of the settle: %s", e.log.String())
	}
	s.err = nil
	e.db.fail["ListEndedPages"] = errBoom
	if _, err := e.w.Round(t.Context()); !errors.Is(err, errBoom) {
		t.Errorf("a failing list: %v", err)
	}
	delete(e.db.fail, "ListEndedPages")
	e.db.fail["ListPageReplies"] = errBoom
	if _, err := e.w.Round(t.Context()); err != nil {
		t.Errorf("a failing read of the replies failed the round: %v", err)
	}
	if e.db.pageDB.state[905] != "pending" {
		t.Errorf("page %s after a failing read of the replies", e.db.pageDB.state[905])
	}
	delete(e.db.fail, "ListPageReplies")
	e.round(t)
	if e.db.pageDB.state[905] != "ended" {
		t.Errorf("page %s", e.db.pageDB.state[905])
	}
	e.w.Pages = nil
	e.round(t)
}

// TestPageSettleFailureSkipped: a Page that fails to settle, first in the round's batch, is logged and does not hold
// back the Pages after it, which the round settles; it stays pending and is settled once it no longer fails.
func TestPageSettleFailureSkipped(t *testing.T) {
	e, s := pageEnv(t)
	e.addPage(906, dbgen.GetPageReplyRow{StepNumber: 1, Importance: "normal"}, ivanID)
	e.addPage(907, dbgen.GetPageReplyRow{StepNumber: 2, Importance: "normal"}, ivanID)
	s.failPages = []int64{906}
	if _, err := e.w.Round(t.Context()); err != nil {
		t.Errorf("round: %v", err)
	}
	if got := []string{e.db.pageDB.state[906], e.db.pageDB.state[907]}; !slices.Equal(got,
		[]string{"pending", "ended"}) {
		t.Errorf("pages %q, want the first pending and the second ended", got)
	}
	if !strings.Contains(e.log.String(), `"work":"page"`) || !strings.Contains(e.log.String(), "settle page 906") {
		t.Errorf("no log line of the failing settle: %s", e.log.String())
	}
	s.failPages = nil
	e.round(t)
	if e.db.pageDB.state[906] != "ended" {
		t.Errorf("page 906 %s once its settle no longer fails", e.db.pageDB.state[906])
	}
}

// TestPageSettleFailuresOfABatch: a whole batch of Pages that fail to settle does not hold back the Pages after it,
// which the round settles in its next batch, after the last one listed; the round ends at the first short batch, and
// the next round starts again from the oldest, settling the failing Pages once they no longer fail.
func TestPageSettleFailuresOfABatch(t *testing.T) {
	e, s := pageEnv(t)
	e.w.Batch = 2
	for _, id := range []int64{906, 907, 908} {
		e.addPage(id, dbgen.GetPageReplyRow{StepNumber: 1, Importance: "normal"}, ivanID)
	}
	s.failPages = []int64{906, 907}
	lists := e.db.pageDB.lists
	if _, err := e.w.Round(t.Context()); err != nil {
		t.Errorf("round: %v", err)
	}
	if got := pageStates(e, 906, 907, 908); !slices.Equal(got, []string{"pending", "pending", "ended"}) {
		t.Errorf("pages %q, want the failing batch pending and the Page after it ended", got)
	}
	if n := e.db.pageDB.lists - lists; n != 2 {
		t.Errorf("listed %d times in the round, want 2", n)
	}
	s.failPages = nil
	e.round(t)
	if got := pageStates(e, 906, 907); !slices.Equal(got, []string{"ended", "ended"}) {
		t.Errorf("pages %q once they no longer fail, want both ended from the oldest again", got)
	}
}

// TestPageSettleBacklog: a round settles at most PageSettleBatches batches of Pages and then waits at most MinWait,
// and the next round goes on after the last Page it listed, so that more failing Pages than a round's batches still do
// not hold back the Pages after them; after the short batch that reached the newest, the round waits as long as the
// due work allows, and the next round starts again from the oldest.
func TestPageSettleBacklog(t *testing.T) {
	e, s := pageEnv(t)
	e.w.Batch = 2
	perRound := delivery.PageSettleBatches * 2
	var failing, later []int64
	for i := range perRound + 3 {
		id := int64(1000 + i)
		e.addPage(id, dbgen.GetPageReplyRow{StepNumber: 1, Importance: "normal"}, ivanID)
		if i < perRound {
			failing = append(failing, id)
		} else {
			later = append(later, id)
		}
	}
	all := func(ids []int64, state string) bool {
		return !slices.ContainsFunc(pageStates(e, ids...), func(s string) bool { return s != state })
	}
	s.failPages = failing
	lists := e.db.pageDB.lists
	if wait := e.round(t); wait > delivery.MinWait {
		t.Errorf("the round that stopped at its batches waits %v, want at most MinWait", wait)
	}
	if n := e.db.pageDB.lists - lists; n != delivery.PageSettleBatches {
		t.Errorf("listed %d times in the first round, want %d", n, delivery.PageSettleBatches)
	}
	if !all(later, "pending") {
		t.Errorf("the first round settled past its batches: %q", pageStates(e, later...))
	}
	lists = e.db.pageDB.lists
	if wait := e.round(t); wait <= delivery.MinWait {
		t.Errorf("the round that reached the newest Page waits %v, want more than MinWait with nothing due", wait)
	}
	if !all(later, "ended") {
		t.Errorf("the second round did not go on after the failing Pages: %q", pageStates(e, later...))
	}
	if n := e.db.pageDB.lists - lists; n != 2 {
		t.Errorf("listed %d times in the second round, want 2 up to its short batch", n)
	}
	s.failPages = nil
	e.round(t)
	if !all(failing, "ended") {
		t.Errorf("the third round did not start again from the oldest: %q", pageStates(e, failing...))
	}
}

// pageStates are the states of the Pages ids.
func pageStates(e *env, ids ...int64) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = e.db.pageDB.state[id]
	}
	return out
}

// TestPageStaysOpenUntilEveryReplyEnded is C-32.FR-10 for a Page with replies in two Destinations: the reply sent in
// one does not end the Page while the other is still to be sent, neither when it settles the Page nor in the round;
// the Page ends once the other reply ends.
func TestPageStaysOpenUntilEveryReplyEnded(t *testing.T) {
	e, s := pageEnv(t)
	e.addPage(pageID, dbgen.GetPageReplyRow{StepNumber: 1, Importance: "normal"}, ivanID)
	g := e.group(groups.StatusFiring, "a")
	e.enqueue(t, g, groups.System, pagedEvent(2, pageID, groups.PageTarget{DestinationID: destMM,
		Source: groups.PageSourceRoute, Linked: []int64{ivanID}}))
	if len(e.db.replies) != 1 {
		t.Fatalf("%d replies", len(e.db.replies))
	}
	// The reply in the other Destination, not due for a long while.
	sent := e.db.replies[0]
	other := &fakeReply{id: sent.id + 1000, delivery: sent.delivery, group: groupID, dest: destMM, event: "paged",
		state: "pending", next: business0.AddDate(1, 0, 0), page: pageID}
	e.db.replies = append(e.db.replies, other)
	e.rounds(t)
	if len(e.replies()) != 1 {
		t.Fatalf("%d replies sent", len(e.replies()))
	}
	if e.db.pageDB.state[pageID] != "pending" || slices.ContainsFunc(s.outcomes, func(o groups.PageOutcome) bool {
		return o.Ended
	}) {
		t.Errorf("ended with a reply still to be sent: %s %+v", e.db.pageDB.state[pageID], s.outcomes)
	}
	other.state = "sent"
	e.round(t)
	if e.db.pageDB.state[pageID] != "ended" {
		t.Errorf("page %s once every reply ended", e.db.pageDB.state[pageID])
	}
}

// TestPageReplyOfABrokenDestination is C-32.FR-10 and C-11.FR-19 for a Page reply queued before its Destination
// became Broken (#627): its own attempt is Fatal, which makes the Destination Broken and leaves the reply to be sent;
// the same round ends it, and settles its Page as ended and reaching nobody, without waiting for the recovery. A
// reply that is not a Page keeps waiting for the recovery, and a later round settles nothing again.
func TestPageReplyOfABrokenDestination(t *testing.T) {
	e, s := pageEnv(t)
	e.addPage(pageID, dbgen.GetPageReplyRow{StepNumber: 1, Importance: "normal"}, ivanID)
	g := e.group(groups.StatusFiring, "a")
	e.enqueue(t, g, groups.System, pagedEvent(2, pageID, groups.PageTarget{DestinationID: destMM,
		Source: groups.PageSourceRoute, Linked: []int64{ivanID}}))
	if len(e.db.replies) != 1 {
		t.Fatalf("%d replies", len(e.db.replies))
	}
	page := e.db.replies[0]
	other := &fakeReply{id: page.id + 1000, delivery: page.delivery, group: groupID, dest: destMM, event: "reminder",
		state: "pending", next: e.business.Now().Add(time.Minute), loudness: "quiet"}
	e.db.replies = append(e.db.replies, other)
	e.rec.Script(deliverytest.MethodReply, deliverytest.Failure(delivery.OutcomeFatal, "HTTP 403"))
	e.round(t)
	if h := e.db.dests[destMM].health; h != "broken" {
		t.Fatalf("destination %s after the fatal reply", h)
	}
	if len(s.outcomes) != 1 || s.outcomes[0].Page != pageID || !s.outcomes[0].Ended || s.outcomes[0].Delivered ||
		len(s.outcomes[0].Reached) != 0 {
		t.Errorf("outcomes %+v, want the page ended without reaching anyone", s.outcomes)
	}
	if page.state != "dropped" || page.owner != "" || e.db.pageDB.state[pageID] != "ended" {
		t.Errorf("page reply %s owned by %q, page %s", page.state, page.owner, e.db.pageDB.state[pageID])
	}
	// The other reply comes due while the Destination is Broken, before its probe: it waits for the recovery.
	e.business.Advance(2 * time.Minute)
	e.round(t)
	if other.state != "pending" || e.rec.Count(deliverytest.MethodReply) != 1 || len(s.outcomes) != 1 {
		t.Errorf("other reply %s after %d replies, outcomes %+v", other.state, e.rec.Count(deliverytest.MethodReply),
			s.outcomes)
	}
}

// TestBrokenPageRepliesInBatches is #627 for the drop of the Page replies of a Broken Destination: a batch at a time
// until a short one, each Page settled once though two of its replies end in the same batch; a Page that fails to
// settle is logged without failing the round and is settled by a later round, its replies all ended; a reply whose
// lease another replica holds is left to it; a drop that fails fails the round.
func TestBrokenPageRepliesInBatches(t *testing.T) {
	e, s := pageEnv(t)
	e.w.Batch = 2
	del := e.db.deliveries[0].id
	add := func(id, page int64) *fakeReply {
		r := &fakeReply{id: id, delivery: del, group: groupID, dest: destMM, event: "paged", state: "pending",
			next: e.business.Now(), page: page, pagedUsers: []int64{ivanID}}
		e.db.replies = append(e.db.replies, r)
		return r
	}
	for _, p := range []int64{1001, 1002, 1003, 1004, 1005} {
		e.addPage(p, dbgen.GetPageReplyRow{StepNumber: 1, Importance: "normal"}, ivanID)
	}
	add(1, 1001)
	add(2, 1001)
	add(3, 1002)
	add(4, 1003)
	add(5, 1004)
	leased := add(6, 1005)
	leased.owner, leased.until = "r2", e.real.Now().Add(time.Hour)
	e.breakDest(destMM, "down")
	s.failPages = []int64{1002}
	e.round(t)
	settled := map[int64]int{}
	for _, o := range s.outcomes {
		if o.Ended && !o.Delivered {
			settled[o.Page]++
		}
	}
	if !maps.Equal(settled, map[int64]int{1001: 1, 1003: 1, 1004: 1}) {
		t.Errorf("settled %v, want 1001, 1003 and 1004 once each", settled)
	}
	if got := pageStates(e, 1001, 1002, 1003, 1004, 1005); !slices.Equal(got,
		[]string{"ended", "pending", "ended", "ended", "pending"}) {
		t.Errorf("pages %q", got)
	}
	if !strings.Contains(e.log.String(), "settle page 1002") {
		t.Errorf("no log line of the failing settle: %s", e.log.String())
	}
	if leased.state != "pending" || leased.owner != "r2" {
		t.Errorf("the leased reply %s owned by %q", leased.state, leased.owner)
	}
	s.failPages = nil
	e.round(t)
	if got := pageStates(e, 1002, 1005); !slices.Equal(got, []string{"ended", "pending"}) {
		t.Errorf("pages %q after the settle no longer fails", got)
	}
	errBoom := errors.New("boom")
	e.db.fail["DropBrokenPageReplies"] = errBoom
	if _, err := e.w.Round(t.Context()); !errors.Is(err, errBoom) {
		t.Errorf("a failing drop: %v", err)
	}
}

// TestBrokenPageSettledBehindABacklog is #627 for a Page whose reply its Destination drops while the Pages to settle
// have a backlog: the round that drops the reply settles the Page itself, though settlePages goes on after the last
// Page of the previous round, past it, and does not reach it in that round.
func TestBrokenPageSettledBehindABacklog(t *testing.T) {
	e, _ := pageEnv(t)
	e.w.Batch = 2
	e.addPage(500, dbgen.GetPageReplyRow{StepNumber: 1, Importance: "normal"}, ivanID)
	e.db.replies = append(e.db.replies, &fakeReply{id: 1, delivery: e.db.deliveries[0].id, group: groupID,
		dest: destMM, event: "paged", state: "pending", next: e.business.Now().Add(time.Hour), page: 500,
		pagedUsers: []int64{ivanID}})
	backlog := delivery.PageSettleBatches*2 + 3
	for i := range backlog {
		e.addPage(int64(1000+i), dbgen.GetPageReplyRow{StepNumber: 1, Importance: "normal"}, ivanID)
	}
	if wait := e.round(t); wait > delivery.MinWait {
		t.Fatalf("the first round waits %v, want it stopped at its batches", wait)
	}
	e.breakDest(destMM, "down")
	e.round(t)
	if got := pageStates(e, 500, int64(1000+backlog-1)); !slices.Equal(got, []string{"ended", "ended"}) {
		t.Errorf("pages %q, want the page of the dropped reply and the newest of the backlog ended", got)
	}
}

// TestPageReplyOfABrokenDestinationBesideASentOne is C-32.FR-10 and FR-12 for a Page with replies in two Destinations
// (#627): the reply in the outgoing webhook is sent and reaches ivan and petr, the one in Mattermost is Fatal and its
// Destination Broken; the Page ends delivered to the people the sent reply mentioned.
func TestPageReplyOfABrokenDestinationBesideASentOne(t *testing.T) {
	e, s := pageEnvOfAll(t)
	e.addPage(pageID, dbgen.GetPageReplyRow{StepNumber: 1, Importance: "normal"}, ivanID, petrID)
	g := e.group(groups.StatusFiring, "a")
	e.enqueue(t, g, groups.System, pagedEvent(2, pageID,
		groups.PageTarget{DestinationID: destWH, Source: groups.PageSourceRoute, Linked: []int64{ivanID, petrID}},
		groups.PageTarget{DestinationID: destMM, Source: groups.PageSourceRoute, Linked: []int64{ivanID}}))
	answers := map[int64]deliverytest.Answer{destWH: deliverytest.OK(),
		destMM: deliverytest.Failure(delivery.OutcomeFatal, "HTTP 403")}
	var order []int64
	for _, r := range slices.SortedFunc(slices.Values(e.db.replies), func(a, b *fakeReply) int {
		return cmp.Compare(a.id, b.id)
	}) {
		if r.page == pageID {
			order = append(order, r.dest)
			e.rec.Script(deliverytest.MethodReply, answers[r.dest])
		}
	}
	if len(order) != 2 {
		t.Fatalf("page replies in %v", order)
	}
	e.rounds(t)
	if h := e.db.dests[destMM].health; h != "broken" {
		t.Fatalf("mattermost %s after the fatal reply", h)
	}
	var ended []groups.PageOutcome
	for _, o := range s.outcomes {
		if o.Ended {
			ended = append(ended, o)
		}
	}
	if len(ended) != 1 || !ended[0].Delivered || !slices.Equal(slices.Sorted(slices.Values(ended[0].Reached)),
		[]int64{ivanID, petrID}) || e.db.pageDB.state[pageID] != "ended" {
		t.Errorf("outcomes %+v, want the page ended delivered to ivan and petr", s.outcomes)
	}
}

// TestPageRootNotDelivered is C-32.FR-10 for a Page whose reply waits for its Root message (#603): the Root message
// is deleted, its republication ends Not delivered, and the Page reply that waited for it is never sent; it ends, and
// the Page is settled ended and not delivered in the round that recorded the republication, while the other replies
// of the delivery keep waiting for a later Publication (C-11.FR-10).
func TestPageRootNotDelivered(t *testing.T) {
	e, s := pageEnv(t)
	e.addPage(pageID, dbgen.GetPageReplyRow{StepNumber: 1, Importance: "normal"}, ivanID)
	e.enqueue(t, e.group(groups.StatusAcknowledged, "a"), groups.System,
		groups.Recorded{Seq: 2, Event: groups.EventAcknowledged, Loudness: groups.Quiet},
		pagedEvent(3, pageID, groups.PageTarget{DestinationID: destMM, Source: groups.PageSourceRoute,
			Linked: []int64{ivanID}}))
	d := e.only(t)
	other := &fakeReply{id: 5000, delivery: d.id, group: groupID, dest: destMM, event: "reminder", state: "pending",
		next: e.business.Now(), loudness: "quiet"}
	e.db.replies = append(e.db.replies, other)
	e.rec.Script(deliverytest.MethodUpdate, deliverytest.Failure(delivery.OutcomeGone, "post not found"))
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeGone, "channel not found"))
	e.round(t)
	if d.state != "pending" || !d.republished || d.publishedAt != nil {
		t.Fatalf("not reset for the republication: %+v", d)
	}
	if len(s.outcomes) != 0 {
		t.Fatalf("settled before the republication ended: %+v", s.outcomes)
	}
	// The round lists no Pages to settle, so only the republication that ended can settle its Page.
	e.db.fail["ListEndedPages"] = errors.New("boom")
	_, _ = e.w.Round(t.Context())
	delete(e.db.fail, "ListEndedPages")
	if d.state != "not_delivered" || e.rec.Count(deliverytest.MethodPublish) != 1 {
		t.Fatalf("republication %+v, %s", d, callMethods(e.rec.Calls()))
	}
	if len(s.outcomes) != 1 || s.outcomes[0].Page != pageID || !s.outcomes[0].Ended || s.outcomes[0].Delivered ||
		len(s.outcomes[0].Reached) != 0 || e.db.pageDB.state[pageID] != "ended" {
		t.Errorf("page outcomes %+v, state %s", s.outcomes, e.db.pageDB.state[pageID])
	}
	for _, r := range e.db.replies {
		if r.page == pageID && (r.state == "pending" || r.state == "collecting") {
			t.Errorf("page reply still waits: %+v", r)
		}
	}
	if other.state != "pending" {
		t.Errorf("the other reply of the delivery %s, want it waiting for a later publication", other.state)
	}
	e.business.Advance(time.Hour)
	e.rounds(t)
	if e.rec.Count(deliverytest.MethodReply) != 0 || len(s.outcomes) != 1 {
		t.Errorf("after the rounds: %s, outcomes %+v", callMethods(e.rec.Calls()), s.outcomes)
	}
}

// pendingPageEnv is pageEnv whose Alert Group's first Publication is still to be made.
func pendingPageEnv(t *testing.T) (*env, *fakeSettler, *fakeDelivery) {
	t.Helper()
	e, d := pending(t)
	e.db.pageDB.init()
	e.db.pageDB.links = []dbgen.ListPageLinksRow{{UserID: ivanID, IdentitySpace: "mattermost:" + strconv.Itoa(connID)}}
	s := &fakeSettler{db: e.db}
	e.w.Pages, e.w.TeamMentions = s, fakeTeamMentions{}
	e.svc = delivery.New(delivery.Config{OrgID: orgID, Store: e.store, Business: e.business,
		Renderer: stubRenderer{}, TeamMentions: fakeTeamMentions{}})
	return e, s, d
}

// TestPageFirstPublicationNotDelivered is C-32.FR-10 for a Page queued while the first Publication of its Alert Group
// was still to be made: the Publication ends Not delivered, the Page reply is never sent, and the Page is settled
// ended and not delivered in that round.
func TestPageFirstPublicationNotDelivered(t *testing.T) {
	e, s, d := pendingPageEnv(t)
	e.addPage(pageID, dbgen.GetPageReplyRow{StepNumber: 1, Importance: "normal"}, ivanID)
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, pagedEvent(2, pageID,
		groups.PageTarget{DestinationID: destMM, Source: groups.PageSourceRoute, Linked: []int64{ivanID}}))
	if len(e.db.replies) != 1 || e.db.replies[0].state != "pending" {
		t.Fatalf("page reply %+v", e.db.replies)
	}
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeGone, "channel not found"))
	e.round(t)
	if d.state != "not_delivered" || e.db.replies[0].state != "dropped" || e.rec.Count(deliverytest.MethodReply) != 0 {
		t.Fatalf("delivery %s, reply %s, calls %s", d.state, e.db.replies[0].state, callMethods(e.rec.Calls()))
	}
	if len(s.outcomes) != 1 || !s.outcomes[0].Ended || s.outcomes[0].Delivered || len(s.outcomes[0].Reached) != 0 {
		t.Errorf("outcomes %+v", s.outcomes)
	}
}

// TestPageEditNotDelivered is C-32.FR-10 and C-11.FR-10: an edit of a published Root message that ends Not delivered
// leaves the Root message in place, so the Page reply waiting under it is still sent and its Page delivered.
func TestPageEditNotDelivered(t *testing.T) {
	e, s := pageEnv(t)
	e.addPage(pageID, dbgen.GetPageReplyRow{StepNumber: 1, Importance: "normal"}, ivanID)
	e.enqueue(t, e.group(groups.StatusAcknowledged, "a"), groups.System,
		groups.Recorded{Seq: 2, Event: groups.EventAcknowledged, Loudness: groups.Quiet},
		pagedEvent(3, pageID, groups.PageTarget{DestinationID: destMM, Source: groups.PageSourceRoute,
			Linked: []int64{ivanID}}))
	e.rec.Script(deliverytest.MethodUpdate, deliverytest.Failure(delivery.OutcomeUnknown, "HTTP 400"))
	e.round(t)
	if d := e.only(t); d.state != "not_delivered" || d.publishedAt == nil {
		t.Fatalf("edit %+v", d)
	}
	e.rounds(t)
	if e.rec.Count(deliverytest.MethodReply) != 1 || len(s.outcomes) != 1 || !s.outcomes[0].Delivered ||
		!slices.Equal(s.outcomes[0].Reached, []int64{ivanID}) {
		t.Errorf("calls %s, outcomes %+v", callMethods(e.rec.Calls()), s.outcomes)
	}
}

// TestPageQueuedAfterRootNotDelivered is C-32.FR-10 for a Page whose Destination had a pending Publication when its
// targets were chosen, which ended Not delivered before the reply was queued (#603): the reply is queued dropped, and
// the round settles the Page as not delivered.
func TestPageQueuedAfterRootNotDelivered(t *testing.T) {
	e, s, d := pendingPageEnv(t)
	e.addPage(pageID, dbgen.GetPageReplyRow{StepNumber: 1, Importance: "normal"}, ivanID)
	// The worker records the Publication Not delivered between ListPageRoots and EnsureDelivery.
	e.db.before["EnsureDelivery"] = func() { d.state = "not_delivered" }
	paged := pagedEvent(2, pageID, groups.PageTarget{DestinationID: destMM, Source: groups.PageSourceRoute,
		Linked: []int64{ivanID}})
	e.db.fail["InsertThreadReply"] = errors.New("boom")
	if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: e.group(groups.StatusFiring, "a"),
		Actor: groups.System, Events: []groups.Recorded{paged}}); err == nil || len(e.db.replies) != 0 {
		t.Fatalf("a failing drop of the page reply: %v, %d replies", err, len(e.db.replies))
	}
	delete(e.db.fail, "InsertThreadReply")
	// The same rendering carries a reply of no Page, which waits for a later Publication (C-11.FR-10).
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, paged, reminder(3))
	if d.state != "not_delivered" {
		t.Fatalf("the rendering restarted the delivery: %s", d.state)
	}
	var page, other *fakeReply
	for _, r := range e.db.replies {
		if r.page == pageID {
			page = r
		} else {
			other = r
		}
	}
	if len(e.db.replies) != 2 || page == nil || other == nil || page.state != "dropped" || other.state != "pending" {
		t.Fatalf("%d replies, the page reply %+v, the other %+v, want the page reply dropped and the other pending",
			len(e.db.replies), page, other)
	}
	e.round(t)
	if len(s.outcomes) != 1 || !s.outcomes[0].Ended || s.outcomes[0].Delivered ||
		e.db.pageDB.state[pageID] != "ended" {
		t.Errorf("outcomes %+v, page %s", s.outcomes, e.db.pageDB.state[pageID])
	}
}

// TestPageQueuedAfterEditNotDelivered is C-32.FR-10 and C-11.FR-10: a Page queued while the last edit of a published
// Root message ended Not delivered replies under that Root message, which is still there: the reply is queued pending,
// sent, and the Page delivered.
func TestPageQueuedAfterEditNotDelivered(t *testing.T) {
	e, s := pageEnv(t)
	e.enqueue(t, e.group(groups.StatusAcknowledged, "a"), groups.System,
		groups.Recorded{Seq: 2, Event: groups.EventAcknowledged, Loudness: groups.Quiet})
	e.rec.Script(deliverytest.MethodUpdate, deliverytest.Failure(delivery.OutcomeUnknown, "HTTP 400"))
	e.round(t)
	d := e.only(t)
	if d.state != "not_delivered" || d.publishedAt == nil {
		t.Fatalf("edit %+v", d)
	}
	// The Page is recorded once the edit ended, as the dispatcher records it with its reply.
	e.addPage(pageID, dbgen.GetPageReplyRow{StepNumber: 1, Importance: "normal"}, ivanID)
	e.enqueue(t, e.group(groups.StatusAcknowledged, "a"), groups.System, pagedEvent(3, pageID,
		groups.PageTarget{DestinationID: destMM, Source: groups.PageSourceRoute, Linked: []int64{ivanID}}))
	if d.state != "not_delivered" {
		t.Fatalf("the paged rendering restarted the delivery: %s", d.state)
	}
	var page *fakeReply
	for _, r := range e.db.replies {
		if r.page == pageID {
			page = r
		}
	}
	if page == nil || page.state != "pending" {
		t.Fatalf("page reply %+v, want it pending", page)
	}
	e.rounds(t)
	if page.state != "sent" || len(s.outcomes) != 1 || !s.outcomes[0].Delivered ||
		!slices.Equal(s.outcomes[0].Reached, []int64{ivanID}) {
		t.Errorf("page reply %s, outcomes %+v", page.state, s.outcomes)
	}
}

// TestPageRootNotDeliveredSettleFails: a Page whose settle fails once its Root message ended Not delivered is logged,
// and the next round settles it, as its reply ended.
func TestPageRootNotDeliveredSettleFails(t *testing.T) {
	e, s := pageEnv(t)
	e.addPage(pageID, dbgen.GetPageReplyRow{StepNumber: 1, Importance: "normal"}, ivanID)
	e.enqueue(t, e.group(groups.StatusAcknowledged, "a"), groups.System,
		groups.Recorded{Seq: 2, Event: groups.EventAcknowledged, Loudness: groups.Quiet},
		pagedEvent(3, pageID, groups.PageTarget{DestinationID: destMM, Source: groups.PageSourceRoute,
			Linked: []int64{ivanID}}))
	e.rec.Script(deliverytest.MethodUpdate, deliverytest.Failure(delivery.OutcomeGone, "post not found"))
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeGone, "channel not found"))
	e.round(t)
	s.failPages = []int64{pageID}
	e.round(t)
	if d := e.only(t); d.state != "not_delivered" || e.db.pageDB.state[pageID] != "pending" ||
		!strings.Contains(e.log.String(), "settle page 900") {
		t.Fatalf("delivery %s, page %s, log %s", d.state, e.db.pageDB.state[pageID], e.log.String())
	}
	s.failPages = nil
	e.round(t)
	if e.db.pageDB.state[pageID] != "ended" || len(s.outcomes) != 1 || s.outcomes[0].Delivered {
		t.Errorf("page %s, outcomes %+v", e.db.pageDB.state[pageID], s.outcomes)
	}
}

// TestPageTargets is C-32.FR-8, C-32.AC-1 and AC-16 through the queries: a Route's chain Pages in the Route's
// Destinations with the Root message, a Team's chain in the Team's and, without one of them with a Root message, in
// the Route's; a Team whose only Destination with the Root message is a Telegram channel Pages there; a chain of both
// in the union; a page_team step only where the Connection can mention the Team or the token, never in Telegram nor in
// Slack without usergroups:read; each target with the people linked in its Identity space and whether it is Broken.
func TestPageTargets(t *testing.T) {
	e, _ := pageEnv(t)
	e.db.routes[routeID] = fakeRoute{language: "en", toTeams: true}
	e.db.groups[groupID].teams = []int64{3, 4, 5}
	slackConn := int64(6)
	e.db.dests[13] = &fakeDest{id: 13, publicID: "DSAAAAAAAAAA13", name: "payments-notice", typ: delivery.TypeSlack,
		connection: &slackConn, health: "broken"}
	e.db.dests[14] = &fakeDest{id: 14, publicID: "DSAAAAAAAAAA14", name: "tg", typ: delivery.TypeTelegram,
		health: "healthy"}
	e.db.dests[15] = &fakeDest{id: 15, publicID: "DSAAAAAAAAAA15", name: "search-notice", typ: delivery.TypeMattermost,
		connection: &slackConn, health: "healthy"}
	e.db.teamDests = map[int64][]routing.Entry{3: {{DestinationID: 13}, {DestinationID: 14}}, 4: {{DestinationID: 14}},
		5: {{DestinationID: 15}}}
	id := "m"
	e.db.deliveries = append(e.db.deliveries, &fakeDelivery{id: 70, dest: 13, group: groupID, state: "delivered",
		messageID: &id, entries: reachedBy(teamEntry(3))}, &fakeDelivery{id: 71, dest: 14, group: groupID,
		state: "delivered", messageID: &id, entries: reachedBy(teamEntry(3), teamEntry(4))})
	e.db.pageDB.links = append(e.db.pageDB.links, dbgen.ListPageLinksRow{UserID: petrID, IdentitySpace: "slack:6"})
	g := e.group(groups.StatusFiring, "a")
	targets := func(r groups.PageRequest) []string {
		t.Helper()
		got, err := e.svc.PageTargets(t.Context(), nil, g, r)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, x := range got {
			s := strconv.FormatInt(x.DestinationID, 10) + "/" + x.Source
			for _, u := range x.Linked {
				s += "/" + strconv.FormatInt(u, 10)
			}
			if x.Withheld {
				s += "/withheld"
			}
			out = append(out, s)
		}
		return out
	}
	people := []int64{ivanID, petrID}
	route := []groups.PageSource{{Kind: groups.PageSourceRoute}}
	for _, c := range []struct {
		name string
		r    groups.PageRequest
		want []string
	}{
		{"the Route's chain", groups.PageRequest{Sources: route, People: people}, []string{"11/route/41"}},
		{"a Team's chain", groups.PageRequest{Sources: []groups.PageSource{{Kind: groups.PageSourceTeam, TeamID: 3}},
			People: people}, []string{"13/team/42/withheld", "14/team"}},
		{"a Team whose only Destination with the Root message is a Telegram channel",
			groups.PageRequest{Sources: []groups.PageSource{{Kind: groups.PageSourceTeam, TeamID: 4}}, People: people},
			[]string{"14/team"}},
		{"a Team without a Destination of its own with the Root message",
			groups.PageRequest{Sources: []groups.PageSource{{Kind: groups.PageSourceTeam, TeamID: 5}}, People: people},
			[]string{"11/team/41"}},
		{"both", groups.PageRequest{Sources: []groups.PageSource{{Kind: groups.PageSourceRoute},
			{Kind: groups.PageSourceTeam, TeamID: 3}}, People: people},
			[]string{"11/route/41", "13/team/42/withheld", "14/team"}},
		{"the Team mention", groups.PageRequest{Sources: []groups.PageSource{{Kind: groups.PageSourceRoute},
			{Kind: groups.PageSourceTeam, TeamID: 3}}, Team: 3}, []string{"11/route"}},
		{"another Team", groups.PageRequest{Sources: route, Team: 4}, nil},
		{"everyone", groups.PageRequest{Sources: []groups.PageSource{{Kind: groups.PageSourceRoute},
			{Kind: groups.PageSourceTeam, TeamID: 3}}, Everyone: "all"}, []string{"11/route"}},
	} {
		if got := targets(c.r); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	// A first Publication on its way counts, as the chains start with the first Publication elsewhere; one a Storm
	// holds does not.
	team3 := groups.PageRequest{Sources: []groups.PageSource{{Kind: groups.PageSourceTeam, TeamID: 3}}, People: people}
	e.db.deliveries[len(e.db.deliveries)-2].messageID, e.db.deliveries[len(e.db.deliveries)-2].state = nil, "pending"
	if got := targets(team3); !slices.Equal(got, []string{"13/team/42/withheld", "14/team"}) {
		t.Errorf("on its way: %v", got)
	}
	e.db.deliveries[len(e.db.deliveries)-2].heldBy = 9
	if got := targets(team3); !slices.Equal(got, []string{"14/team"}) {
		t.Errorf("held, the Telegram channel still with its Root message: %v", got)
	}
	e.db.deliveries[len(e.db.deliveries)-1].heldBy = 9
	if got := targets(team3); !slices.Equal(got, []string{"11/team/41"}) {
		t.Errorf("held: %v", got)
	}
	e.db.deliveries[len(e.db.deliveries)-2].heldBy, e.db.deliveries[len(e.db.deliveries)-1].heldBy = 0, 0
	errBoom := errors.New("boom")
	for _, name := range []string{"GetRouteDelivery", "ListRouteDestinations", "ListGroupTeams", "ListTeamDestinations",
		"ListPageRoots", "ListPageLinks"} {
		e.db.fail[name] = errBoom
		if _, err := e.svc.PageTargets(t.Context(), nil, g, groups.PageRequest{Sources: route, People: people}); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", name, err)
		}
		delete(e.db.fail, name)
	}
	e.db.deliveries[len(e.db.deliveries)-2].entries = []byte("{")
	if _, err := e.svc.PageTargets(t.Context(), nil, g, groups.PageRequest{Sources: route}); err == nil {
		t.Error("entries that do not decode")
	}
	e.db.deliveries[len(e.db.deliveries)-2].entries = reachedBy(teamEntry(3))
	e.svc = delivery.New(delivery.Config{OrgID: orgID, Store: e.store, Business: e.business,
		Renderer: stubRenderer{}, TeamMentions: fakeTeamMentions{err: errBoom}})
	if _, err := e.svc.PageTargets(t.Context(), nil, g, groups.PageRequest{Sources: route, Team: 3}); !errors.Is(err, errBoom) {
		t.Errorf("team mention: %v", err)
	}
	e.svc = delivery.New(delivery.Config{OrgID: orgID, Store: e.store, Business: e.business, Renderer: stubRenderer{}})
	if got := targets(groups.PageRequest{Sources: route, Team: 3}); len(got) != 0 {
		t.Errorf("without team mentions %v", got)
	}
	t.Run("the level fell", testPageTargetsAfterTheLevelFell)
	t.Run("a shared channel", testPageTargetsInASharedChannel)
}

// reachedBy is deliveries.entries of a delivery that the entries es reached, as EnsureDelivery records them.
func reachedBy(es ...routing.Reaching) []byte {
	b, err := json.Marshal(es)
	if err != nil {
		panic(err)
	}
	return b
}

// teamEntry is the entry of the Team id for any level.
func teamEntry(id int64) routing.Reaching {
	return routing.Reaching{Kind: routing.EntryTeam, TeamID: id, Condition: routing.ConditionAny}
}

// testPageTargetsInASharedChannel is C-32.FR-8 for a Destination that several sources name: a source owns it only
// when its own entry reached it, not when it names it under a condition the Alert Group never met. A warning Alert
// Group that was never critical: the Route names ops for any level and the shared channel for critical only, Team
// payments its own channel for any level and the shared one for critical only, and Team devops the shared one for any
// level, so only devops' entry brought its Root message there. A payments @here rings payments' channel only, the
// Route's chain Pages in ops only and devops' chain in the shared channel.
func testPageTargetsInASharedChannel(t *testing.T) {
	const payments, devops, own, shared = 3, 4, 22, 30
	e, _ := pageEnv(t)
	e.db.routes[routeID] = fakeRoute{language: "en", toTeams: true}
	e.db.groups[groupID].teams = []int64{payments, devops}
	conn := int64(connID)
	e.db.dests[own] = &fakeDest{id: own, publicID: "DSAAAAAAAAAA22", name: "payments", typ: delivery.TypeMattermost,
		connection: &conn, health: "healthy"}
	e.db.dests[shared] = &fakeDest{id: shared, publicID: "DSAAAAAAAAAA30", name: "shared",
		typ: delivery.TypeMattermost, connection: &conn, health: "healthy"}
	e.db.routeDests[routeID] = []int64{destMM, shared}
	e.db.conditions = map[int64]map[int64]string{routeID: {shared: "critical"}}
	e.db.teamDests = map[int64][]routing.Entry{
		payments: {{DestinationID: own}, {DestinationID: shared, Condition: routing.ConditionCritical}},
		devops:   {{DestinationID: shared}},
	}
	id := "m"
	e.db.deliveries = append(e.db.deliveries,
		&fakeDelivery{id: 73, dest: own, group: groupID, state: "delivered", messageID: &id,
			entries: reachedBy(teamEntry(payments))},
		&fakeDelivery{id: 74, dest: shared, group: groupID, state: "delivered", messageID: &id,
			entries: reachedBy(teamEntry(devops))})
	g := e.group(groups.StatusFiring, "a")
	g.Severity = organization.SeverityWarning
	targets := func(sources ...groups.PageSource) []int64 {
		t.Helper()
		got, err := e.svc.PageTargets(t.Context(), nil, g, groups.PageRequest{Sources: sources, Everyone: "here"})
		if err != nil {
			t.Fatal(err)
		}
		return targetIDs(got)
	}
	route := groups.PageSource{Kind: groups.PageSourceRoute}
	for _, c := range []struct {
		name    string
		sources []groups.PageSource
		want    []int64
	}{
		{"the chain of payments", []groups.PageSource{{Kind: groups.PageSourceTeam, TeamID: payments}}, []int64{own}},
		{"the Route's chain", []groups.PageSource{route}, []int64{destMM}},
		{"the chain of devops", []groups.PageSource{{Kind: groups.PageSourceTeam, TeamID: devops}}, []int64{shared}},
		{"a chain of the Route and payments", []groups.PageSource{route,
			{Kind: groups.PageSourceTeam, TeamID: payments}}, []int64{destMM, own}},
	} {
		if got := targets(c.sources...); !slices.Equal(got, c.want) {
			t.Errorf("%s Pages in %v, want %v", c.name, got, c.want)
		}
	}
	// Once the shared channel's Root message came from the Route's entry too, at a critical level the Alert Group had,
	// the Route owns it as well.
	e.db.deliveries[len(e.db.deliveries)-1].entries = reachedBy(teamEntry(devops),
		routing.Reaching{Kind: routing.EntryRoute, Condition: routing.ConditionCritical})
	if got := targets(route); !slices.Equal(got, []int64{destMM, shared}) {
		t.Errorf("the Route's chain Pages in %v once its entry reached the shared channel", got)
	}
}

// targetIDs are the Destinations of the targets.
func targetIDs(ts []groups.PageTarget) []int64 {
	var out []int64
	for _, x := range ts {
		out = append(out, x.DestinationID)
	}
	return out
}

// testPageTargetsAfterTheLevelFell is C-32.FR-8 with C-26.FR-4: once the Severity level of an Alert Group fell below
// critical, a source still owns the Destinations with the Root message that its critical entries reached. A Team
// whose critical Destination has it Pages there, not in the Route's (A); a Route whose critical Destination has it
// still Pages there (B).
func testPageTargetsAfterTheLevelFell(t *testing.T) {
	const critical, notice = 16, 17
	e, _ := pageEnv(t)
	e.db.routes[routeID] = fakeRoute{language: "en", toTeams: true}
	e.db.groups[groupID].teams = []int64{3}
	conn := int64(connID)
	e.db.dests[critical] = &fakeDest{id: critical, publicID: "DSAAAAAAAAAA16", name: "payments-critical",
		typ: delivery.TypeMattermost, connection: &conn, health: "healthy"}
	e.db.dests[notice] = &fakeDest{id: notice, publicID: "DSAAAAAAAAAA17", name: "payments-notice",
		typ: delivery.TypeMattermost, connection: &conn, health: "healthy"}
	id := "m"
	// Team 3's critical entry reached its critical channel while the Alert Group was critical.
	e.db.deliveries = append(e.db.deliveries, &fakeDelivery{id: 72, dest: critical, group: groupID,
		state: "delivered", messageID: &id, entries: reachedBy(routing.Reaching{Kind: routing.EntryTeam, TeamID: 3,
			Condition: routing.ConditionCritical})})
	g := e.group(groups.StatusFiring, "a")
	g.Severity = organization.SeverityWarning
	targets := func(r groups.PageRequest) []int64 {
		t.Helper()
		got, err := e.svc.PageTargets(t.Context(), nil, g, r)
		if err != nil {
			t.Fatal(err)
		}
		var out []int64
		for _, x := range got {
			out = append(out, x.DestinationID)
		}
		return out
	}
	// A: the Route has the ops channel for any level; Team 3 its critical and its notice channel.
	e.db.teamDests = map[int64][]routing.Entry{3: {{DestinationID: critical, Condition: routing.ConditionCritical},
		{DestinationID: notice, Condition: routing.ConditionBelowCritical}}}
	team := groups.PageRequest{Sources: []groups.PageSource{{Kind: groups.PageSourceTeam, TeamID: 3}}, Everyone: "here"}
	if got := targets(team); !slices.Equal(got, []int64{critical}) {
		t.Errorf("A: the chain of Team 3 Pages in %v, want its critical channel %d", got, critical)
	}
	// B: the Route has the ops channel for critical and the notice channel below it, the Team nothing.
	e.db.teamDests = nil
	e.db.routeDests[routeID] = []int64{destMM, notice}
	e.db.conditions = map[int64]map[int64]string{routeID: {destMM: "critical", notice: "below_critical"}}
	if got := targets(groups.PageRequest{Sources: []groups.PageSource{{Kind: groups.PageSourceRoute}},
		People: []int64{ivanID}}); !slices.Equal(got, []int64{destMM}) {
		t.Errorf("B: the Route's chain Pages in %v, want its critical channel %d", got, destMM)
	}
}

// The Destinations of the Pages beyond Mattermost and Slack: a Telegram channel, a Bridge whose bridge declares
// native_mentions, @here and group mentions, one whose bridge declares none of them, and an outgoing webhook in the
// template mode without "reply in thread"; destWH has it.
const (
	destPageTG       = 14
	destPageBridge   = 16
	destPagePlain    = 17
	destPageNoReply  = 18
	connPageBridge   = 7
	connPagePlain    = 8
	spacePageBridge  = "bridge:7"
	spacePagePlain   = "bridge:8"
	spacePageTG      = "telegram"
	publicPagePlain  = "DSAAAAAAAAAA17"
	publicPageBridge = "DSAAAAAAAAAA16"
)

// pageEnvOfAll is pageEnv with the Route's Destinations Mattermost, the webhook, Telegram, both Bridges and the
// webhook without "reply in thread", each with the Alert Group's Root message, and ivan linked in every Identity
// space; petr is linked through the Bridge without native_mentions only.
func pageEnvOfAll(t *testing.T) (*env, *fakeSettler) {
	t.Helper()
	e, s := pageEnv(t)
	bridge, plain := int64(connPageBridge), int64(connPagePlain)
	e.db.dests[destPageTG] = &fakeDest{id: destPageTG, publicID: "DSAAAAAAAAAA14", name: "tg",
		typ: delivery.TypeTelegram, health: "healthy", limit: 600, per: 60}
	e.db.dests[destPageBridge] = &fakeDest{id: destPageBridge, publicID: publicPageBridge, name: "bridge",
		typ: delivery.TypeBridge, connection: &bridge, health: "healthy", limit: 600, per: 60}
	e.db.dests[destPagePlain] = &fakeDest{id: destPagePlain, publicID: publicPagePlain, name: "plain",
		typ: delivery.TypeBridge, connection: &plain, health: "healthy", limit: 600, per: 60}
	e.db.dests[destPageNoReply] = &fakeDest{id: destPageNoReply, publicID: "DSAAAAAAAAAA18", name: "no-reply",
		typ: delivery.TypeWebhook, health: "healthy", limit: 600, per: 60, noReplies: true}
	e.db.pageDB.caps = map[int64]dbgen.ListPageRootsRow{
		destPageBridge: {GroupMentions: true, NativeMentions: true, Messenger: true, EveryoneMentions: []string{"here"}},
		destPagePlain:  {Messenger: true},
	}
	e.db.pageDB.links = append(e.db.pageDB.links, dbgen.ListPageLinksRow{UserID: ivanID, IdentitySpace: spacePageTG},
		dbgen.ListPageLinksRow{UserID: ivanID, IdentitySpace: spacePageBridge},
		dbgen.ListPageLinksRow{UserID: ivanID, IdentitySpace: spacePagePlain},
		dbgen.ListPageLinksRow{UserID: petrID, IdentitySpace: spacePagePlain})
	e.db.routeDests[routeID] = []int64{destMM, destWH, destPageTG, destPageBridge, destPagePlain, destPageNoReply}
	id := "m"
	for i, d := range []int64{destWH, destPageTG, destPageBridge, destPagePlain, destPageNoReply} {
		e.db.deliveries = append(e.db.deliveries, &fakeDelivery{id: int64(80 + i), dest: d, group: groupID,
			state: "delivered", messageID: &id})
	}
	return e, s
}

// pageTargetsOf are the targets of r in the env as destination/source/L linked/N named.
func pageTargetsOf(t *testing.T, e *env, r groups.PageRequest) []string {
	t.Helper()
	got, err := e.svc.PageTargets(t.Context(), nil, e.group(groups.StatusFiring, "a"), r)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, x := range got {
		s := strconv.FormatInt(x.DestinationID, 10) + "/" + x.Source
		for _, u := range x.Linked {
			s += "/L" + strconv.FormatInt(u, 10)
		}
		for _, u := range x.Named {
			s += "/N" + strconv.FormatInt(u, 10)
		}
		out = append(out, s)
	}
	return out
}

// TestPageTargetsBeyondMattermost is C-32.FR-8, FR-9, FR-11 and C-18.FR-4 for Telegram, Bridges and outgoing
// webhooks: a chain Pages in the Threads of its Telegram and Bridge Destinations as in Mattermost, each with the people
// linked in its Identity space; through a bridge without native_mentions a linked person is Named, not reached; an
// outgoing webhook with "reply in thread" takes every person as data, one without it takes no Page; a page_team step
// Pages Telegram never, a Bridge only with what its bridge declares, and an outgoing webhook always.
func TestPageTargetsBeyondMattermost(t *testing.T) {
	e, _ := pageEnvOfAll(t)
	route := []groups.PageSource{{Kind: groups.PageSourceRoute}}
	for _, c := range []struct {
		name string
		r    groups.PageRequest
		want []string
	}{
		{"people", groups.PageRequest{Sources: route, People: []int64{ivanID, petrID}},
			[]string{"11/route/L41", "12/route/L41/L42", "14/route/L41", "16/route/L41", "17/route/N41/N42"}},
		{"the whole Team", groups.PageRequest{Sources: route, Team: 3}, []string{"11/route", "12/route"}},
		{"@here", groups.PageRequest{Sources: route, Everyone: "here"}, []string{"11/route", "12/route", "16/route"}},
		{"@all", groups.PageRequest{Sources: route, Everyone: "all"}, []string{"11/route", "12/route"}},
	} {
		if got := pageTargetsOf(t, e, c.r); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	// A bridge whose info was never read declares nothing (C-22.FR-16, as mentions.Resolve and the reach of S-171
	// read it): its linked people are Named, decided now for the reply to render.
	delete(e.db.pageDB.caps, destPagePlain)
	if got := pageTargetsOf(t, e, groups.PageRequest{Sources: route, People: []int64{ivanID}}); !slices.Contains(got,
		"17/route/N41") {
		t.Errorf("a bridge whose info was never read: %v", got)
	}
	// Without any Identity space among them, only the outgoing webhook has its people, and no link is read.
	e.db.routeDests[routeID] = []int64{destWH}
	e.db.fail["ListPageLinks"] = errors.New("boom")
	if got := pageTargetsOf(t, e, groups.PageRequest{Sources: route, People: []int64{petrID}}); !slices.Equal(got,
		[]string{"12/route/L42"}) {
		t.Errorf("the webhook alone: %v", got)
	}
}

// TestPageRepliesBeyondMattermost is C-32.FR-9 and FR-11 for Bridges and outgoing webhooks: through a bridge without
// native_mentions the reply still names a linked person by their marker, which the bridge shows as the plain name,
// inside the Lead, while it reaches nobody — its Page reports no one reached — and names a person without a link in
// "Page not delivered"; an outgoing webhook's "reply in thread" carries paged with every person and the Team of a
// page_team step as data. The reply renders what the Page decided when it was queued: a bridge whose info declares
// native_mentions only once the Page was queued, as when it was first read by the Publication there, still names the
// people it Named by their marker and reaches none of them; and a person the Page recorded as linked nowhere, who
// linked their account through a bridge with native_mentions since, gets no Mention there.
func TestPageRepliesBeyondMattermost(t *testing.T) {
	e, s := pageEnvOfAll(t)
	e.w.Adapters[delivery.TypeBridge] = e.rec
	e.addPage(pageID, dbgen.GetPageReplyRow{StepNumber: 1, Importance: "normal"}, ivanID, petrID, carolID)
	e.addPage(901, dbgen.GetPageReplyRow{StepNumber: 2, TeamID: pgtype.Int8{Int64: 3, Valid: true},
		TeamName: "Payments", TeamSlug: "payments"})
	e.db.pageDB.reasons = map[int64]string{carolID: groups.PageNoAccountLink}
	g := e.group(groups.StatusFiring, "a")
	e.enqueue(t, g, groups.System,
		pagedEvent(2, pageID, groups.PageTarget{DestinationID: destPagePlain, Source: groups.PageSourceRoute,
			Named: []int64{ivanID, petrID}}, groups.PageTarget{DestinationID: destWH, Source: groups.PageSourceRoute,
			Linked: []int64{ivanID, petrID}}, groups.PageTarget{DestinationID: destPageBridge,
			Source: groups.PageSourceRoute, Linked: []int64{ivanID}}),
		pagedEvent(3, 901, groups.PageTarget{DestinationID: destWH, Source: groups.PageSourceRoute}))
	// Since the Page was queued, the plain bridge's info was read and declares native_mentions, and carol linked her
	// account through the bridge with native_mentions.
	e.db.pageDB.caps[destPagePlain] = dbgen.ListPageRootsRow{NativeMentions: true, Messenger: true}
	e.db.pageDB.links = append(e.db.pageDB.links, dbgen.ListPageLinksRow{UserID: carolID,
		IdentitySpace: spacePageBridge})
	e.rounds(t)
	var got []string
	for _, c := range e.replies() {
		var names []string
		for _, tg := range c.Targets {
			switch {
			case tg.User != nil:
				names = append(names, tg.Kind+":"+tg.User.Login+"/"+tg.User.ExternalID)
			case tg.Team != nil:
				names = append(names, tg.Kind+":"+tg.Team.Slug+"/"+tg.Team.Name)
			}
		}
		lead, event := "", ""
		if c.Message.Lead != nil {
			lead = c.Message.Lead.Before + "…" + c.Message.Lead.After
		}
		if c.Webhook != nil {
			event = c.Webhook.Event
		}
		got = append(got, c.Destination.PublicID+"|"+event+"|"+strings.Join(names, ",")+"|"+lead+"|"+
			strings.ReplaceAll(c.Message.Text(), zeroWidth, ""))
	}
	slices.Sort(got)
	carol := "Page not delivered: @carol has no linked Bridge account\n"
	want := []string{
		"DSAAAAAAAAAA12|paged|team:payments/Payments||",
		"DSAAAAAAAAAA12|paged|user:ivan/,user:petr/||",
		publicPageBridge + "||user:ivan/u-ivan|Step 1: …, you are needed|" +
			"Page not delivered: @petr has no linked Bridge account\n" + carol +
			"Chain of the Route payments · @ivan, @petr, @carol · normal",
		publicPagePlain + "||user:ivan/u-ivan,user:petr/u-petr|Step 1: …, you are needed|" + carol +
			"Chain of the Route payments · @ivan, @petr, @carol · normal",
	}
	if !slices.Equal(got, want) {
		t.Errorf("replies\n%q\nwant\n%q", got, want)
	}
	for _, o := range s.outcomes {
		if o.Page == pageID && o.Ended && !slices.Equal(slices.Sorted(slices.Values(o.Reached)),
			[]int64{ivanID, petrID}) {
			t.Errorf("the replies reach %+v", o)
		}
	}
	for _, r := range e.db.replies {
		if r.page == pageID && r.dest == destPagePlain && len(r.pagedUsers) != 0 {
			t.Errorf("the reply through the plain bridge reaches %v", r.pagedUsers)
		}
	}
}

// leavingOut is the adapter of a bridge whose Mention line leaves out the people whose public ids are ids for its
// length (C-12.FR-11), or, when plain, names them by the plain name for want of a marker (C-22.FR-16): its replies
// are sent and report them in Outcome.LeftOut, or in Outcome.Plain.
type leavingOut struct {
	*deliverytest.Recorder
	ids   []string
	plain bool
}

func (a leavingOut) Reply(ctx context.Context, c delivery.Call, root delivery.Root,
	m delivery.Message) delivery.Outcome {
	out := a.Recorder.Reply(ctx, c, root, m)
	switch {
	case out.Kind != delivery.OutcomeOK:
	case a.plain:
		out.Plain = a.ids
	default:
		out.LeftOut = a.ids
	}
	return out
}

// TestPageReplyLeftOut is C-32.FR-10, FR-12, C-12.FR-11 and C-22.FR-16: a sent Page reply through a bridge whose
// Mention line left out a person for its length, or that named them by the plain name because no marker could be
// built for their Account link (#652), does not reach them — the reply mentions only the others from then on, and
// the Page reports the others reached once its replies ended, so that the dispatcher records the person not
// delivered and makes them no Assignee — unless another reply of the Page mentioned them. Left out, someone who is not
// among the people the reply mentions changes nothing; and carol, a person of the Page linked only in Mattermost, whom
// the bridge reply never mentioned, is not added to it.
func TestPageReplyLeftOut(t *testing.T) {
	for _, tc := range []struct {
		name    string
		leftOut int64
		plain   bool
		other   bool
		want    []int64
		mention []int64
	}{
		{"left out", petrID, false, false, []int64{ivanID}, []int64{ivanID}},
		{"mentioned by another reply", petrID, false, true, []int64{ivanID, petrID}, []int64{ivanID}},
		{"left out someone else", carolID, false, false, []int64{ivanID, petrID}, []int64{ivanID, petrID}},
		{"plain", petrID, true, false, []int64{ivanID}, []int64{ivanID}},
		{"plain, mentioned by another reply", petrID, true, true, []int64{ivanID, petrID}, []int64{ivanID}},
		{"plain someone else", carolID, true, false, []int64{ivanID, petrID}, []int64{ivanID, petrID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, s := pageEnvOfAll(t)
			e.w.Adapters[delivery.TypeBridge] = leavingOut{Recorder: e.rec,
				ids: []string{pagePeople[tc.leftOut].PublicID}, plain: tc.plain}
			e.db.pageDB.links = append(e.db.pageDB.links,
				dbgen.ListPageLinksRow{UserID: petrID, IdentitySpace: spacePageBridge},
				dbgen.ListPageLinksRow{UserID: petrID, IdentitySpace: "mattermost:" + strconv.Itoa(connID)},
				dbgen.ListPageLinksRow{UserID: carolID, IdentitySpace: "mattermost:" + strconv.Itoa(connID)})
			e.addPage(pageID, dbgen.GetPageReplyRow{StepNumber: 1, Importance: "normal"}, ivanID, petrID, carolID)
			targets := []groups.PageTarget{{DestinationID: destPageBridge, Source: groups.PageSourceRoute,
				Linked: []int64{ivanID, petrID}}}
			if tc.other {
				targets = append(targets, groups.PageTarget{DestinationID: destMM, Source: groups.PageSourceRoute,
					Linked: []int64{petrID}})
			}
			e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, pagedEvent(2, pageID, targets...))
			e.rounds(t)
			var sent []string
			for _, c := range e.replies() {
				if c.Destination.ID != destPageBridge {
					continue
				}
				for _, tg := range c.Targets {
					if tg.User != nil {
						sent = append(sent, tg.User.Login)
					}
				}
			}
			if !slices.Equal(sent, []string{"ivan", "petr"}) {
				t.Fatalf("the bridge reply mentions %v", sent)
			}
			var ended []groups.PageOutcome
			for _, o := range s.outcomes {
				if o.Page == pageID && o.Ended {
					ended = append(ended, o)
				}
			}
			if len(ended) != 1 || !ended[0].Delivered ||
				!slices.Equal(slices.Sorted(slices.Values(ended[0].Reached)), tc.want) {
				t.Errorf("outcomes %+v, want %v reached", s.outcomes, tc.want)
			}
			for _, r := range e.db.replies {
				if r.page == pageID && r.dest == destPageBridge && !slices.Equal(r.pagedUsers, tc.mention) {
					t.Errorf("the sent bridge reply mentions %v", r.pagedUsers)
				}
			}
		})
	}
}

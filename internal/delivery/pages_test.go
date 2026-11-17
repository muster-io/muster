// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// The people of the Pages of the fixtures: ivan, linked in the Identity space of the Mattermost Connection, and petr,
// linked nowhere.
const (
	ivanID = 41
	petrID = 42
	pageID = 900
)

// fakePageDB are the Pages of the fake database: what each reply says, its people, the Account links and the state of
// each Page, which the settler ends, and how many times the Pages to settle were listed.
type fakePageDB struct {
	pages  map[int64]dbgen.GetPageReplyRow
	people map[int64][]int64
	links  []dbgen.ListPageLinksRow
	state  map[int64]string
	group  map[int64]int64
	lists  int
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
	ivanID: {ID: ivanID, PublicID: "SRAAAAAAAAAAIV", Name: "Ivan", Login: "ivan"},
	petrID: {ID: petrID, PublicID: "SRAAAAAAAAAAPE", Name: "Petr", Login: "petr"},
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
		out = append(out, dbgen.ListPageRootsRow{ID: ds.id, Type: ds.typ, ConnectionID: nullInt(ds.connection),
			Health: ds.health, Entries: entries})
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

// TestPageTargets is C-32.FR-8, C-32.AC-1 and AC-16 through the queries: a Route's chain Pages in the Route's
// Destinations with the Root message, a Team's chain in the Team's and, without one of them with a Root message, in
// the Route's; a Team whose Destinations with the Root message all take no Page yet (Telegram) makes no reply and does
// not fall back to the Route's; a chain of both in the union; a page_team step only where the Connection can mention
// the Team or the token; each target with the people linked in its Identity space and whether it is Broken.
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
			People: people}, []string{"13/team/42/withheld"}},
		{"a Team whose only Destination with the Root message is a Telegram channel",
			groups.PageRequest{Sources: []groups.PageSource{{Kind: groups.PageSourceTeam, TeamID: 4}}, People: people},
			nil},
		{"a Team without a Destination of its own with the Root message",
			groups.PageRequest{Sources: []groups.PageSource{{Kind: groups.PageSourceTeam, TeamID: 5}}, People: people},
			[]string{"11/team/41"}},
		{"both", groups.PageRequest{Sources: []groups.PageSource{{Kind: groups.PageSourceRoute},
			{Kind: groups.PageSourceTeam, TeamID: 3}}, People: people}, []string{"11/route/41", "13/team/42/withheld"}},
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
	if got := targets(team3); !slices.Equal(got, []string{"13/team/42/withheld"}) {
		t.Errorf("on its way: %v", got)
	}
	e.db.deliveries[len(e.db.deliveries)-2].heldBy = 9
	if got := targets(team3); got != nil {
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

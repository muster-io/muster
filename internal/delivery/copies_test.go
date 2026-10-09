// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
)

// The copy buffer of the fake database: telegram_post_copies by Connection, channel and post.
type (
	copyKey  struct{ conn, channel, post int64 }
	fakeCopy struct {
		org, discussion, copyID int64
		learnedFrom             string
		received                time.Time
	}
)

func (f *fakeDB) LockPostCopy(_ context.Context, arg dbgen.LockPostCopyParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("LockPostCopy"); err != nil {
		return err
	}
	if arg.LockClass != 0x6d75_0003 {
		return errors.New("wrong lock class")
	}
	f.postLocks = append(f.postLocks, copyKey{arg.ConnectionID, arg.ChannelChatID, arg.ChannelMessageID})
	return nil
}

func (f *fakeDB) InsertPostCopy(_ context.Context, arg dbgen.InsertPostCopyParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("InsertPostCopy"); err != nil {
		return err
	}
	k := copyKey{arg.ConnectionID, arg.ChannelChatID, arg.ChannelMessageID}
	if f.copies[k] == nil {
		f.copies[k] = &fakeCopy{org: arg.OrgID, discussion: arg.DiscussionChatID, copyID: arg.CopyMessageID,
			learnedFrom: arg.LearnedFrom, received: arg.Now}
	}
	return nil
}

func (f *fakeDB) GetPostCopy(_ context.Context, arg dbgen.GetPostCopyParams) (dbgen.GetPostCopyRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetPostCopy"); err != nil {
		return dbgen.GetPostCopyRow{}, err
	}
	c := f.copies[copyKey{arg.ConnectionID, arg.ChannelChatID, arg.ChannelMessageID}]
	if c == nil || c.org != arg.OrgID {
		return dbgen.GetPostCopyRow{}, pgx.ErrNoRows
	}
	return dbgen.GetPostCopyRow{DiscussionChatID: c.discussion, CopyMessageID: c.copyID}, nil
}

// telegramPostOf reports whether the delivery d is to a Telegram Destination of the Connection with the channel and
// whose Root message is the post; f.mu is held.
func (f *fakeDB) telegramPostOf(d *fakeDelivery, conn, channel int64, post string) (*fakeDest, bool) {
	ds := f.dests[d.dest]
	return ds, ds.typ == delivery.TypeTelegram && ds.connection != nil && *ds.connection == conn &&
		ds.tgChannel != nil && *ds.tgChannel == channel && d.messageID != nil && *d.messageID == post
}

func (f *fakeDB) AttachCopy(_ context.Context, arg dbgen.AttachCopyParams) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("AttachCopy"); err != nil {
		return nil, err
	}
	var out []string
	for _, d := range f.deliveries {
		ds, ok := f.telegramPostOf(d, arg.ConnectionID, arg.ChannelChatID, arg.MessageID)
		if !ok || ds.tgGroup == nil || *ds.tgGroup != arg.DiscussionChatID || d.group == 0 ||
			d.threadState == "attached" || (d.anchorID != nil && *d.anchorID == arg.CopyMessageID) {
			continue
		}
		d.threadState, d.anchorID, d.chainLastID, d.updated = "attached", at2(arg.CopyMessageID), nil, arg.Now
		out = append(out, f.groups[d.group].publicID)
	}
	return out, nil
}

func (f *fakeDB) WakeCopyReplies(_ context.Context, arg dbgen.WakeCopyRepliesParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("WakeCopyReplies"); err != nil {
		return 0, err
	}
	var n int64
	for _, r := range f.replies {
		d := f.delivery(r.delivery)
		if _, ok := f.telegramPostOf(d, arg.ConnectionID, arg.ChannelChatID, arg.MessageID); !ok ||
			d.threadState != "attached" || r.state != "pending" || !r.next.After(arg.Now) || r.errorClass != nil {
			continue
		}
		r.next = arg.Now
		n++
	}
	return n, nil
}

func (f *fakeDB) LockDeliveryThread(_ context.Context, arg dbgen.LockDeliveryThreadParams) (
	dbgen.LockDeliveryThreadRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("LockDeliveryThread"); err != nil {
		return dbgen.LockDeliveryThreadRow{}, err
	}
	d := f.delivery(arg.ID)
	if d == nil {
		return dbgen.LockDeliveryThreadRow{}, pgx.ErrNoRows
	}
	return dbgen.LockDeliveryThreadRow{ThreadState: d.threadState, ThreadAnchorID: txt(d.anchorID),
		ThreadChainLastID: txt(d.chainLastID), MessageID: txt(d.messageID)}, nil
}

func (f *fakeDB) SetThread(_ context.Context, arg dbgen.SetThreadParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("SetThread"); err != nil {
		return err
	}
	if d := f.delivery(arg.ID); d != nil {
		d.threadState, d.anchorID, d.chainLastID, d.updated = arg.ThreadState, strOf(arg.AnchorID),
			strOf(arg.ChainLastID), arg.Now
	}
	return nil
}

func (f *fakeDB) PrunePostCopies(_ context.Context, arg dbgen.PrunePostCopiesParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("PrunePostCopies"); err != nil {
		return 0, err
	}
	var n int64
	for k, c := range f.copies {
		if n < int64(arg.BatchSize) && c.org == arg.OrgID && c.received.Before(arg.Before) {
			delete(f.copies, k)
			n++
		}
	}
	return n, nil
}

// The Telegram fixtures: Destination 13 through Connection 5, its channel and discussion group, the post that its
// Publication creates and the post's automatic copy.
const (
	destTG    = 13
	tgChannel = int64(-1001000000001)
	tgGroup   = int64(-1001000000002)
	tgPost    = "501"
	tgCopy    = int64(9001)
)

// telegramEnv is an env whose Route delivers to the Telegram Destination only, whose Publications answer the post
// tgPost.
func telegramEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	conn, channel, group := int64(connID), tgChannel, tgGroup
	e.db.dests[destTG] = &fakeDest{id: destTG, publicID: "DSAAAAAAAAAA13", name: "alerts", typ: delivery.TypeTelegram,
		connection: &conn, health: "healthy", limit: 600, per: 60, tgChannel: &channel, tgGroup: &group}
	e.db.routeDests[routeID] = []int64{destTG}
	e.w.Adapters[delivery.TypeTelegram] = e.rec
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK,
		MessageID: tgPost}})
	return e
}

// publish publishes the Alert Group in the Telegram Destination and forgets the calls.
func (e *env) publish(t *testing.T) *fakeDelivery {
	t.Helper()
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	e.round(t)
	d := e.only(t)
	if d.messageID == nil || *d.messageID != tgPost {
		t.Fatalf("publication %+v", d)
	}
	e.rec.Reset()
	return d
}

// learn is LearnCopy of the copy of tgPost.
func (e *env) learn(t *testing.T, copyID int64, from string) {
	t.Helper()
	if err := e.svc.LearnCopy(t.Context(), delivery.Copy{ConnectionID: connID, ChannelChatID: tgChannel,
		PostID: 501, DiscussionChatID: tgGroup, CopyID: copyID, LearnedFrom: from}); err != nil {
		t.Fatal(err)
	}
}

// newAlerts queues the Thread reply of new Alerts in the firing Alert Group.
func (e *env) newAlerts(t *testing.T, seq int64, fp string) {
	t.Helper()
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, alertsAdded(seq, groups.StatusFiring, fp))
}

func (e *env) threadEvents() int {
	n := 0
	for _, ev := range e.db.events {
		if ev.Kind == string(delivery.EventThreadNotAttached) {
			n++
		}
	}
	return n
}

func str(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// TestCopyBeforePublication is C-14.FR-3: a copy learned before the Publication completes attaches the Thread when
// the Publication is recorded, under the lock of the post taken before the delivery is recorded; the reply goes to the
// copy.
func TestCopyBeforePublication(t *testing.T) {
	e := telegramEnv(t)
	e.learn(t, tgCopy, delivery.LearnedFromAutomaticForward)
	if c := e.db.copies[copyKey{connID, tgChannel, 501}]; c == nil || c.copyID != tgCopy ||
		c.learnedFrom != "automatic_forward" || c.discussion != tgGroup || !c.received.Equal(business0) {
		t.Fatalf("buffered %+v", c)
	}
	d := e.publish(t)
	if d.threadState != "attached" || str(d.anchorID) != "9001" || len(e.db.postLocks) != 2 {
		t.Fatalf("thread %s %s, locks %v", d.threadState, str(d.anchorID), e.db.postLocks)
	}
	e.newAlerts(t, 2, "fp2")
	e.round(t)
	r := e.replies()
	if len(r) != 1 || r[0].Root != (delivery.Root{MessageID: tgPost, ThreadAnchorID: "9001"}) {
		t.Fatalf("replies %+v", r)
	}
	if e.threadEvents() != 0 {
		t.Errorf("a thread_not_attached event")
	}
}

// TestCopyAfterPublication is C-14.FR-3: a Publication without a known copy waits for it; the reply that comes due
// waits up to telegram.copy_wait, and the copy learned meanwhile attaches the Thread, makes the reply due at once and
// wakes the workers; the reply goes to the copy.
func TestCopyAfterPublication(t *testing.T) {
	e := telegramEnv(t)
	d := e.publish(t)
	if d.threadState != "waiting_for_copy" || d.anchorID != nil {
		t.Fatalf("thread %s", d.threadState)
	}
	e.business.Set(business0.Add(5 * time.Second))
	e.newAlerts(t, 2, "fp2")
	e.round(t)
	r := e.db.replies[0]
	if len(e.replies()) != 0 || !r.next.Equal(business0.Add(delivery.CopyWait)) || r.owner != "" {
		t.Fatalf("the reply did not wait: %+v %+v", e.replies(), r)
	}
	notified, hints := e.db.notified, len(e.db.hints)
	e.learn(t, tgCopy, delivery.LearnedFromAutomaticForward)
	if d.threadState != "attached" || str(d.anchorID) != "9001" || !r.next.Equal(business0.Add(5*time.Second)) ||
		e.db.notified != notified+1 || len(e.db.hints) != hints+1 || e.db.hints[hints].ID != "AGAAAAAAAAAA21" {
		t.Fatalf("after the copy: %s %s %v notified %d hints %v", d.threadState, str(d.anchorID), r.next,
			e.db.notified, e.db.hints)
	}
	e.round(t)
	if got := e.replies(); len(got) != 1 || got[0].Root.ThreadAnchorID != "9001" {
		t.Fatalf("replies %+v", got)
	}
	// Learning it again changes nothing and wakes nobody.
	e.learn(t, tgCopy, delivery.LearnedFromComment)
	if e.db.notified != notified+1 {
		t.Errorf("a second wake")
	}
}

// TestCopyLookedUpByReply is C-14.FR-3: a copy buffered whose attachment the Publication missed is found by the due
// Thread reply, under the lock of the post, which attaches the Thread before it replies to the copy.
func TestCopyLookedUpByReply(t *testing.T) {
	e := telegramEnv(t)
	d := e.publish(t)
	e.db.copies[copyKey{connID, tgChannel, 501}] = &fakeCopy{org: orgID, discussion: tgGroup, copyID: tgCopy}
	e.newAlerts(t, 2, "fp2")
	e.round(t)
	if r := e.replies(); len(r) != 1 || r[0].Root.ThreadAnchorID != "9001" || d.threadState != "attached" {
		t.Fatalf("replies %+v, thread %s", r, d.threadState)
	}
	// A copy in another discussion group is not this Thread's.
	e2 := telegramEnv(t)
	d2 := e2.publish(t)
	e2.db.copies[copyKey{connID, tgChannel, 501}] = &fakeCopy{org: orgID, discussion: -42, copyID: tgCopy}
	e2.business.Set(business0.Add(delivery.CopyWait))
	e2.newAlerts(t, 2, "fp2")
	e2.round(t)
	if r := e2.replies(); len(r) != 1 || r[0].Root != (delivery.Root{MessageID: tgPost}) ||
		d2.threadState != "unattached" {
		t.Fatalf("replies %+v, thread %s", r, d2.threadState)
	}
}

// TestLearnCopyRules: a copy that names no post is refused; the first copy learned wins; a copy in another discussion
// group, or of a Destination whose channel is not known, attaches nothing; and a Mattermost reply is untouched.
func TestLearnCopyRules(t *testing.T) {
	e := telegramEnv(t)
	for _, c := range []delivery.Copy{
		{ChannelChatID: tgChannel, PostID: 1, DiscussionChatID: tgGroup, CopyID: 2, LearnedFrom: "comment"},
		{ConnectionID: connID, PostID: 1, DiscussionChatID: tgGroup, CopyID: 2, LearnedFrom: "comment"},
		{ConnectionID: connID, ChannelChatID: tgChannel, DiscussionChatID: tgGroup, CopyID: 2, LearnedFrom: "comment"},
		{ConnectionID: connID, ChannelChatID: tgChannel, PostID: 1, CopyID: 2, LearnedFrom: "comment"},
		{ConnectionID: connID, ChannelChatID: tgChannel, PostID: 1, DiscussionChatID: tgGroup, LearnedFrom: "comment"},
		{ConnectionID: connID, ChannelChatID: tgChannel, PostID: 1, DiscussionChatID: tgGroup, CopyID: 2},
	} {
		if err := e.svc.LearnCopy(t.Context(), c); err == nil {
			t.Errorf("%+v was learned", c)
		}
	}
	d := e.publish(t)
	e.learn(t, 77, delivery.LearnedFromComment)
	e.learn(t, 78, delivery.LearnedFromAutomaticForward)
	if c := e.db.copies[copyKey{connID, tgChannel, 501}]; c.copyID != 77 || str(d.anchorID) != "77" {
		t.Fatalf("copy %+v anchor %s", c, str(d.anchorID))
	}
	other := telegramEnv(t)
	od := other.publish(t)
	if err := other.svc.LearnCopy(t.Context(), delivery.Copy{ConnectionID: connID, ChannelChatID: tgChannel,
		PostID: 501, DiscussionChatID: -42, CopyID: 5, LearnedFrom: "comment"}); err != nil || od.threadState != "waiting_for_copy" {
		t.Fatalf("another group attached: %v %s", err, od.threadState)
	}
	// A Destination whose channel is not known yet publishes without a Thread state, and its replies go unattached
	// once telegram.copy_wait is over.
	unknown := telegramEnv(t)
	unknown.db.dests[destTG].tgChannel = nil
	ud := unknown.publish(t)
	if ud.threadState != "none" {
		t.Fatalf("thread %s", ud.threadState)
	}
	unknown.business.Set(business0.Add(delivery.CopyWait))
	unknown.newAlerts(t, 2, "fp2")
	unknown.round(t)
	if r := unknown.replies(); len(r) != 1 || r[0].Root != (delivery.Root{MessageID: tgPost}) ||
		ud.threadState != "unattached" {
		t.Fatalf("replies %+v thread %s", r, ud.threadState)
	}
	// Mattermost replies keep their Root message only and never touch the Thread state.
	mm := published(t)
	mm.enqueue(t, mm.group(groups.StatusFiring, "a"), groups.System, alertsAdded(2, groups.StatusFiring, "fp"))
	mm.round(t)
	if r := mm.replies(); len(r) != 1 || r[0].Root != (delivery.Root{MessageID: "m1"}) ||
		mm.only(t).threadState != "none" || mm.db.calls["LockDeliveryThread"] != 0 {
		t.Fatalf("mattermost %+v", r)
	}
}

// TestPrunePostCopies: copies received more than a day ago are deleted in batches; running it twice deletes nothing
// more.
func TestPrunePostCopies(t *testing.T) {
	e := telegramEnv(t)
	for i, age := range []time.Duration{25 * time.Hour, 24*time.Hour + time.Second, 23 * time.Hour} {
		e.db.copies[copyKey{connID, tgChannel, int64(i + 1)}] = &fakeCopy{org: orgID, received: business0.Add(-age)}
	}
	e.db.copies[copyKey{connID, tgChannel, 9}] = &fakeCopy{org: orgID + 1, received: business0.Add(-48 * time.Hour)}
	if n, err := e.svc.PrunePostCopies(t.Context(), business0, 1); err != nil || n != 1 {
		t.Fatalf("first batch %d %v", n, err)
	}
	if n, err := e.svc.PrunePostCopies(t.Context(), business0, 10); err != nil || n != 1 || len(e.db.copies) != 2 {
		t.Fatalf("second batch %d %v %d", n, err, len(e.db.copies))
	}
	if n, err := e.svc.PrunePostCopies(t.Context(), business0, 10); err != nil || n != 0 {
		t.Fatalf("again %d %v", n, err)
	}
	e.db.fail["PrunePostCopies"] = errBoom
	if _, err := e.svc.PrunePostCopies(t.Context(), business0, 10); !errors.Is(err, errBoom) {
		t.Fatalf("failure %v", err)
	}
}

// TestCopyFailures: a failed query of the copy buffer or of a Thread fails the learning, the Publication's record,
// the preparation or the record of a reply, which are retried.
func TestCopyFailures(t *testing.T) {
	for _, name := range []string{"LockPostCopy", "InsertPostCopy", "GetPostCopy", "AttachCopy", "Notify",
		"WakeCopyReplies", "NotifyDelivery"} {
		e := telegramEnv(t)
		d := e.publish(t)
		e.business.Set(business0.Add(time.Second))
		e.newAlerts(t, 2, "fp2")
		e.round(t)
		e.db.fail[name] = errBoom
		if err := e.svc.LearnCopy(t.Context(), delivery.Copy{ConnectionID: connID, ChannelChatID: tgChannel,
			PostID: 501, DiscussionChatID: tgGroup, CopyID: tgCopy,
			LearnedFrom: delivery.LearnedFromComment}); !errors.Is(err, errBoom) {
			t.Errorf("%s: LearnCopy = %v (%s)", name, err, d.threadState)
		}
	}
	for _, name := range []string{"LockPostCopy", "GetPostCopy", "SetThread"} {
		e := telegramEnv(t)
		e.db.fail[name] = errBoom
		e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
		e.round(t)
		if !strings.Contains(e.log.String(), "boom") {
			t.Errorf("%s: publication %s\n%s", name, e.only(t).state, e.log)
		}
	}
	for _, name := range []string{"LockPostCopy", "LockDeliveryThread", "GetPostCopy", "SetThread"} {
		e := telegramEnv(t)
		e.publish(t)
		e.db.copies[copyKey{connID, tgChannel, 501}] = &fakeCopy{org: orgID, discussion: tgGroup, copyID: tgCopy}
		e.newAlerts(t, 2, "fp2")
		e.db.fail[name] = errBoom
		e.round(t)
		if len(e.replies()) != 0 || !strings.Contains(e.log.String(), "boom") {
			t.Errorf("%s: replies %+v", name, e.replies())
		}
	}
	for _, name := range []string{"LockDeliveryThread", "SetThread", "InsertDeliveryEvent", "Notify"} {
		e := telegramEnv(t)
		d := e.publish(t)
		e.business.Set(business0.Add(delivery.CopyWait))
		e.newAlerts(t, 2, "fp2")
		e.db.before["RenewReplyLease"] = func() { e.db.fail[name] = errBoom }
		e.round(t)
		if d.threadState == "unattached" && name != "InsertDeliveryEvent" && name != "Notify" ||
			!strings.Contains(e.log.String(), "boom") {
			t.Errorf("%s: thread %s\n%s", name, d.threadState, e.log)
		}
	}
}

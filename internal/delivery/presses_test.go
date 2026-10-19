// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/outbound"
)

func (f *fakeDB) GetPressBinding(_ context.Context, arg dbgen.GetPressBindingParams) (dbgen.GetPressBindingRow,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetPressBinding"); err != nil {
		return dbgen.GetPressBindingRow{}, err
	}
	for _, d := range f.deliveries {
		ds, g := f.dests[d.dest], f.groups[d.group]
		if g == nil || ds == nil || d.messageID == nil || *d.messageID != arg.MessageID ||
			g.publicID != arg.GroupPublicID || ds.typ != delivery.TypeMattermost || ds.deleted ||
			ds.connection == nil || *ds.connection != arg.ConnectionID.Int64 {
			continue
		}
		r := f.routes[g.route]
		return dbgen.GetPressBindingRow{DestinationID: ds.id, DestinationPublicID: ds.publicID,
			DestinationName: ds.name, ChannelID: ds.channel, Language: r.language,
			SnoozeDurationsSeconds: slices.Clone(r.snooze)}, nil
	}
	return dbgen.GetPressBindingRow{}, pgx.ErrNoRows
}

func (f *fakeDB) GetTelegramPressBinding(_ context.Context, arg dbgen.GetTelegramPressBindingParams) (
	dbgen.GetTelegramPressBindingRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetTelegramPressBinding"); err != nil {
		return dbgen.GetTelegramPressBindingRow{}, err
	}
	for _, d := range f.deliveries {
		ds, g := f.dests[d.dest], f.groups[d.group]
		if g == nil || ds == nil || d.messageID == nil || *d.messageID != arg.MessageID ||
			g.publicID != arg.GroupPublicID || ds.typ != delivery.TypeTelegram || ds.deleted ||
			ds.connection == nil || *ds.connection != arg.ConnectionID.Int64 || ds.tgChannel == nil ||
			*ds.tgChannel != arg.ChatID {
			continue
		}
		r := f.routes[g.route]
		return dbgen.GetTelegramPressBindingRow{DestinationID: ds.id, DestinationPublicID: ds.publicID,
			DestinationName: ds.name, Language: r.language, SnoozeDurationsSeconds: slices.Clone(r.snooze)}, nil
	}
	return dbgen.GetTelegramPressBindingRow{}, pgx.ErrNoRows
}

// reminderReply is the delivery, Destination and Alert Group of the sent Reminder whose message id is messageID, for
// the reply bindings.
func (f *fakeDB) reminderReply(messageID, group string, conn int64, typ string) (*fakeDelivery, *fakeDest,
	*fakeGroup) {
	for _, r := range f.replies {
		var d *fakeDelivery
		for _, x := range f.deliveries {
			if x.id == r.delivery {
				d = x
			}
		}
		if d == nil {
			continue
		}
		ds, g := f.dests[d.dest], f.groups[d.group]
		if g == nil || ds == nil || r.event != "reminder" || r.state != "sent" || r.messageID == nil ||
			*r.messageID != messageID || g.publicID != group || ds.typ != typ || ds.deleted || ds.connection == nil ||
			*ds.connection != conn {
			continue
		}
		return d, ds, g
	}
	return nil, nil, nil
}

func (f *fakeDB) GetReplyPressBinding(_ context.Context, arg dbgen.GetReplyPressBindingParams) (
	dbgen.GetReplyPressBindingRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetReplyPressBinding"); err != nil {
		return dbgen.GetReplyPressBindingRow{}, err
	}
	d, ds, g := f.reminderReply(arg.MessageID, arg.GroupPublicID, arg.ConnectionID.Int64, delivery.TypeMattermost)
	if ds == nil {
		return dbgen.GetReplyPressBindingRow{}, pgx.ErrNoRows
	}
	root := ""
	if d.messageID != nil {
		root = *d.messageID
	}
	r := f.routes[g.route]
	return dbgen.GetReplyPressBindingRow{DestinationID: ds.id, DestinationPublicID: ds.publicID,
		DestinationName: ds.name, ChannelID: ds.channel, RootMessageID: root, Language: r.language,
		SnoozeDurationsSeconds: slices.Clone(r.snooze)}, nil
}

func (f *fakeDB) GetTelegramReplyPressBinding(_ context.Context, arg dbgen.GetTelegramReplyPressBindingParams) (
	dbgen.GetTelegramReplyPressBindingRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetTelegramReplyPressBinding"); err != nil {
		return dbgen.GetTelegramReplyPressBindingRow{}, err
	}
	_, ds, g := f.reminderReply(arg.MessageID, arg.GroupPublicID, arg.ConnectionID.Int64, delivery.TypeTelegram)
	if ds == nil || ds.tgGroup == nil || *ds.tgGroup != arg.ChatID {
		return dbgen.GetTelegramReplyPressBindingRow{}, pgx.ErrNoRows
	}
	r := f.routes[g.route]
	return dbgen.GetTelegramReplyPressBindingRow{DestinationID: ds.id, DestinationPublicID: ds.publicID,
		DestinationName: ds.name, Language: r.language, SnoozeDurationsSeconds: slices.Clone(r.snooze)}, nil
}

func (f *fakeDB) GetPostDestination(_ context.Context, arg dbgen.GetPostDestinationParams) (
	dbgen.GetPostDestinationRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetPostDestination"); err != nil {
		return dbgen.GetPostDestinationRow{}, err
	}
	for _, id := range slices.Sorted(maps.Keys(f.dests)) {
		ds := f.dests[id]
		if ds.typ != delivery.TypeMattermost || ds.deleted || ds.connection == nil ||
			*ds.connection != arg.ConnectionID.Int64 || ds.channel != arg.ChannelID {
			continue
		}
		for _, d := range f.deliveries {
			if d.dest == id && d.messageID != nil && *d.messageID == arg.MessageID {
				return dbgen.GetPostDestinationRow{ID: ds.id, PublicID: ds.publicID, Name: ds.name}, nil
			}
		}
	}
	return dbgen.GetPostDestinationRow{}, pgx.ErrNoRows
}

// TestPressBinding is the binding of C-13.FR-4 and AC-11: a press is bound to the Alert Group's delivery to a
// Destination of the Connection whose Root message is the pressed post, with the Destination's channel and the Route's
// language and Snooze durations; another post, another Connection or a deleted Destination is not bound.
func TestPressBinding(t *testing.T) {
	e := newEnv(t)
	e.db.dests[destMM].channel = "ch-alerts"
	r := e.db.routes[routeID]
	r.language, r.snooze = "ru", []int64{3600, 14400}
	e.db.routes[routeID] = r
	post := "post-1"
	e.db.deliveries = append(e.db.deliveries, &fakeDelivery{id: 1, dest: destMM, group: groupID, state: "delivered",
		messageID: &post, publishedAt: at(business0)})
	b, err := e.svc.PressBinding(t.Context(), connID, "AGAAAAAAAAAA21", "post-1")
	if err != nil || b.Destination.ID != destMM || b.Destination.PublicID != "DSAAAAAAAAAA11" ||
		b.Destination.Name != "ops" || b.Destination.Type != delivery.TypeMattermost || b.Destination.Connection == nil ||
		*b.Destination.Connection != connID || b.ChannelID != "ch-alerts" || b.Language != "ru" ||
		!slices.Equal(b.SnoozeSeconds, []int64{3600, 14400}) {
		t.Fatalf("PressBinding = %+v, %v", b, err)
	}
	for _, c := range []struct {
		conn        int64
		group, post string
	}{{connID, "AGAAAAAAAAAA21", "post-2"}, {connID + 1, "AGAAAAAAAAAA21", "post-1"},
		{connID, "AGAAAAAAAAAA99", "post-1"}} {
		if _, err := e.svc.PressBinding(t.Context(), c.conn, c.group, c.post); !errors.Is(err, delivery.ErrNotBound) {
			t.Errorf("PressBinding(%d, %s, %s) = %v, want ErrNotBound", c.conn, c.group, c.post, err)
		}
	}
	e.db.dests[destMM].deleted = true
	if _, err := e.svc.PressBinding(t.Context(), connID, "AGAAAAAAAAAA21", "post-1"); !errors.Is(err,
		delivery.ErrNotBound) {
		t.Errorf("a deleted Destination = %v", err)
	}
	e.db.fail["GetPressBinding"] = errBoom
	if _, err := e.svc.PressBinding(t.Context(), connID, "AGAAAAAAAAAA21", "post-1"); !errors.Is(err, errBoom) {
		t.Errorf("a failed read = %v", err)
	}
}

// TestPostDestination: a post is answered in the channel of a Mattermost Destination of the Connection that is not
// deleted, only when a delivery to it posted the post.
func TestPostDestination(t *testing.T) {
	e := newEnv(t)
	e.db.dests[destMM].channel = "ch-alerts"
	post := "post-1"
	e.db.deliveries = append(e.db.deliveries, &fakeDelivery{id: 1, dest: destMM, group: groupID, state: "delivered",
		messageID: &post, publishedAt: at(business0)}, &fakeDelivery{id: 2, dest: destMM, group: groupID, state: "pending"})
	d, ok, err := e.svc.PostDestination(t.Context(), connID, "post-1", "ch-alerts")
	if err != nil || !ok || d.ID != destMM || d.PublicID != "DSAAAAAAAAAA11" || d.Type != delivery.TypeMattermost ||
		d.Connection == nil || *d.Connection != connID {
		t.Fatalf("PostDestination = %+v, %v, %v", d, ok, err)
	}
	for _, c := range []struct {
		conn          int64
		post, channel string
	}{{connID, "post-1", "ch-other"}, {connID + 1, "post-1", "ch-alerts"}, {connID, "garbage", "ch-alerts"}} {
		if _, ok, err := e.svc.PostDestination(t.Context(), c.conn, c.post, c.channel); ok || err != nil {
			t.Errorf("PostDestination(%d, %s, %s) = %v, %v", c.conn, c.post, c.channel, ok, err)
		}
	}
	e.db.dests[destMM].deleted = true
	if _, ok, err := e.svc.PostDestination(t.Context(), connID, "post-1", "ch-alerts"); ok || err != nil {
		t.Errorf("a deleted Destination = %v, %v", ok, err)
	}
	e.db.fail["GetPostDestination"] = errBoom
	if _, _, err := e.svc.PostDestination(t.Context(), connID, "post-1", "ch-alerts"); !errors.Is(err, errBoom) {
		t.Errorf("a failed read = %v", err)
	}
}

// TestAnswerOp: a private answer takes a limiter token of its Destination and makes the adapter's call in the
// interactive client class, with its outcome.
func TestAnswerOp(t *testing.T) {
	e := newEnv(t)
	conn := int64(connID)
	dest := delivery.Destination{ID: destMM, PublicID: "DSAAAAAAAAAA11", Type: delivery.TypeMattermost,
		Connection: &conn}
	var got delivery.Call
	out, err := e.interactive().Do(t.Context(), delivery.Subject{Destination: &dest}, delivery.AnswerOp(
		func(_ context.Context, c delivery.Call) delivery.Outcome {
			got = c
			return delivery.Outcome{Kind: delivery.OutcomeFatal, Error: "refused"}
		}))
	if err != nil || out.Kind != delivery.OutcomeFatal || out.Error != "refused" ||
		got.Class != outbound.ClassInteractive || got.Destination.PublicID != dest.PublicID {
		t.Fatalf("Do = %+v, %v; call %+v", out, err, got)
	}
	if len(e.rec.Calls()) != 0 {
		t.Errorf("the answer called the adapter: %+v", e.rec.Calls())
	}
}

// TestDirectOp: a bot's message to one person takes a limiter token of its Connection and makes the client's call in
// the interactive client class, with its outcome, without calling the adapter.
func TestDirectOp(t *testing.T) {
	e := newEnv(t)
	conn := int64(connID)
	var got delivery.Call
	out, err := e.interactive().Do(t.Context(), delivery.Subject{Connection: &conn}, delivery.DirectOp(
		func(_ context.Context, c delivery.Call) delivery.Outcome {
			got = c
			return delivery.Outcome{Kind: delivery.OutcomeOK}
		}))
	if err != nil || out.Kind != delivery.OutcomeOK || got.Class != outbound.ClassInteractive {
		t.Fatalf("Do = %+v, %v; call %+v", out, err, got)
	}
	if len(e.rec.Calls()) != 0 {
		t.Errorf("the direct message called the adapter: %+v", e.rec.Calls())
	}
}

// TestTelegramPressBinding is the binding of C-14.FR-4: a press is bound to the Alert Group's delivery to a Telegram
// Destination of the Connection whose channel is the chat pressed and whose Root message is the post pressed, with the
// Route's language and Snooze durations; another chat, another post, another Connection, another Alert Group or a
// deleted Destination is not bound.
func TestTelegramPressBinding(t *testing.T) {
	e := telegramEnv(t)
	r := e.db.routes[routeID]
	r.language, r.snooze = "ru", []int64{3600}
	e.db.routes[routeID] = r
	post := tgPost
	e.db.deliveries = append(e.db.deliveries, &fakeDelivery{id: 1, dest: destTG, group: groupID, state: "delivered",
		messageID: &post, publishedAt: at(business0)})
	postID, _ := strconv.ParseInt(tgPost, 10, 64)
	b, err := e.svc.TelegramPressBinding(t.Context(), connID, "AGAAAAAAAAAA21", tgChannel, postID)
	if err != nil || b.Destination.ID != destTG || b.Destination.PublicID != "DSAAAAAAAAAA13" ||
		b.Destination.Type != delivery.TypeTelegram || b.Destination.Connection == nil ||
		*b.Destination.Connection != connID || b.ChannelID != "-1001000000001" || b.Language != "ru" ||
		!slices.Equal(b.SnoozeSeconds, []int64{3600}) {
		t.Fatalf("TelegramPressBinding = %+v, %v", b, err)
	}
	for _, c := range []struct {
		conn, chat, post int64
		group            string
	}{{connID, tgGroup, postID, "AGAAAAAAAAAA21"}, {connID, tgChannel, postID + 1, "AGAAAAAAAAAA21"},
		{connID + 1, tgChannel, postID, "AGAAAAAAAAAA21"}, {connID, tgChannel, postID, "AGAAAAAAAAAA99"}} {
		if _, err := e.svc.TelegramPressBinding(t.Context(), c.conn, c.group, c.chat, c.post); !errors.Is(err,
			delivery.ErrNotBound) {
			t.Errorf("TelegramPressBinding(%+v) = %v, want ErrNotBound", c, err)
		}
	}
	e.db.dests[destTG].deleted = true
	if _, err := e.svc.TelegramPressBinding(t.Context(), connID, "AGAAAAAAAAAA21", tgChannel, postID); !errors.Is(err,
		delivery.ErrNotBound) {
		t.Errorf("a deleted Destination = %v", err)
	}
	e.db.fail["GetTelegramPressBinding"] = errBoom
	if _, err := e.svc.TelegramPressBinding(t.Context(), connID, "AGAAAAAAAAAA21", tgChannel, postID); !errors.Is(err,
		errBoom) {
		t.Errorf("a failed read = %v", err)
	}
}

// TestReplyBinding is the binding of a press on a Reminder (C-13.FR-4): it is bound to a sent Reminder of the Alert
// Group whose message id is the pressed post, delivered to a Mattermost Destination of the Connection, with the
// Destination's channel, the Root message it replies to and the Route's language; another post, a reply that is not a
// Reminder or not sent, another Alert Group, another Connection, a Telegram Destination or a deleted one is not bound.
func TestReplyBinding(t *testing.T) {
	e := newEnv(t)
	e.db.dests[destMM].channel = "ch-alerts"
	r := e.db.routes[routeID]
	r.language = "ru"
	e.db.routes[routeID] = r
	root, rp, other := "root-1", "reply-1", "reply-2"
	e.db.deliveries = append(e.db.deliveries, &fakeDelivery{id: 1, dest: destMM, group: groupID, state: "delivered",
		messageID: &root, publishedAt: at(business0)})
	e.db.replies = append(e.db.replies,
		&fakeReply{id: 1, delivery: 1, group: groupID, dest: destMM, event: "reminder", state: "sent", messageID: &rp},
		&fakeReply{id: 2, delivery: 1, group: groupID, dest: destMM, event: "alerts_added", state: "sent",
			messageID: &other})
	b, err := e.svc.ReplyBinding(t.Context(), connID, "AGAAAAAAAAAA21", rp)
	if err != nil || b.Destination.ID != destMM || b.Destination.PublicID != "DSAAAAAAAAAA11" ||
		b.Destination.Type != delivery.TypeMattermost || b.Destination.Connection == nil ||
		*b.Destination.Connection != connID || b.ChannelID != "ch-alerts" || b.RootMessageID != root ||
		b.Language != "ru" {
		t.Fatalf("ReplyBinding = %+v, %v", b, err)
	}
	for _, c := range []struct {
		conn        int64
		group, post string
	}{{connID, "AGAAAAAAAAAA21", root}, {connID, "AGAAAAAAAAAA21", other}, {connID, "AGAAAAAAAAAA99", rp},
		{connID + 1, "AGAAAAAAAAAA21", rp}} {
		if _, err := e.svc.ReplyBinding(t.Context(), c.conn, c.group, c.post); !errors.Is(err, delivery.ErrNotBound) {
			t.Errorf("ReplyBinding(%d, %s, %s) = %v, want ErrNotBound", c.conn, c.group, c.post, err)
		}
	}
	e.db.replies[0].state = "pending"
	if _, err := e.svc.ReplyBinding(t.Context(), connID, "AGAAAAAAAAAA21", rp); !errors.Is(err, delivery.ErrNotBound) {
		t.Errorf("an unsent Reminder = %v", err)
	}
	e.db.replies[0].state = "sent"
	e.db.dests[destMM].deleted = true
	if _, err := e.svc.ReplyBinding(t.Context(), connID, "AGAAAAAAAAAA21", rp); !errors.Is(err, delivery.ErrNotBound) {
		t.Errorf("a deleted Destination = %v", err)
	}
	e.db.fail["GetReplyPressBinding"] = errBoom
	if _, err := e.svc.ReplyBinding(t.Context(), connID, "AGAAAAAAAAAA21", rp); !errors.Is(err, errBoom) {
		t.Errorf("a failed read = %v", err)
	}
}

// TestTelegramReplyBinding is the binding of a press on a Reminder in Telegram (C-14.FR-4): it is bound to a sent
// Reminder of the Alert Group whose message is the one pressed in the discussion group of a Telegram Destination of the
// Connection; the channel, another message, another Alert Group, another Connection or a deleted Destination is not.
func TestTelegramReplyBinding(t *testing.T) {
	e := telegramEnv(t)
	r := e.db.routes[routeID]
	r.language = "ru"
	e.db.routes[routeID] = r
	post, rp := tgPost, "812"
	e.db.deliveries = append(e.db.deliveries, &fakeDelivery{id: 1, dest: destTG, group: groupID, state: "delivered",
		messageID: &post, publishedAt: at(business0)})
	e.db.replies = append(e.db.replies,
		&fakeReply{id: 1, delivery: 1, group: groupID, dest: destTG, event: "reminder", state: "sent", messageID: &rp})
	b, err := e.svc.TelegramReplyBinding(t.Context(), connID, "AGAAAAAAAAAA21", tgGroup, 812)
	if err != nil || b.Destination.ID != destTG || b.Destination.Type != delivery.TypeTelegram ||
		b.ChannelID != "-1001000000002" || b.RootMessageID != "" || b.Language != "ru" {
		t.Fatalf("TelegramReplyBinding = %+v, %v", b, err)
	}
	for _, c := range []struct {
		conn, chat, post int64
		group            string
	}{{connID, tgChannel, 812, "AGAAAAAAAAAA21"}, {connID, tgGroup, 813, "AGAAAAAAAAAA21"},
		{connID + 1, tgGroup, 812, "AGAAAAAAAAAA21"}, {connID, tgGroup, 812, "AGAAAAAAAAAA99"}} {
		if _, err := e.svc.TelegramReplyBinding(t.Context(), c.conn, c.group, c.chat, c.post); !errors.Is(err,
			delivery.ErrNotBound) {
			t.Errorf("TelegramReplyBinding(%+v) = %v, want ErrNotBound", c, err)
		}
	}
	e.db.dests[destTG].deleted = true
	if _, err := e.svc.TelegramReplyBinding(t.Context(), connID, "AGAAAAAAAAAA21", tgGroup, 812); !errors.Is(err,
		delivery.ErrNotBound) {
		t.Errorf("a deleted Destination = %v", err)
	}
	e.db.fail["GetTelegramReplyPressBinding"] = errBoom
	if _, err := e.svc.TelegramReplyBinding(t.Context(), connID, "AGAAAAAAAAAA21", tgGroup, 812); !errors.Is(err,
		errBoom) {
		t.Errorf("a failed read = %v", err)
	}
}

// bridgeDest is the Destination of a delivery when it is a Bridge Destination of the Connection conn, not deleted.
func (f *fakeDB) bridgeDest(id, conn int64) *fakeDest {
	ds := f.dests[id]
	if ds == nil || ds.typ != delivery.TypeBridge || ds.deleted || ds.connection == nil || *ds.connection != conn {
		return nil
	}
	return ds
}

// bridgeWeekStart is organization.week_start as the reads of a bridge binding give it.
const bridgeWeekStart = "sunday"

// bridgeRoot is the binding row of the delivery to a Bridge Destination of the Connection conn whose current Root
// message, or older one when older is set, is messageID, the lowest delivery id first.
func (f *fakeDB) bridgeRoot(conn int64, messageID string, older bool) (dbgen.GetBridgeRootBindingRow, bool) {
	for _, d := range slices.SortedFunc(slices.Values(f.deliveries), func(a, b *fakeDelivery) int {
		return int(a.id - b.id)
	}) {
		ds, g := f.bridgeDest(d.dest, conn), f.groups[d.group]
		if ds == nil || g == nil {
			continue
		}
		if (!older && (d.messageID == nil || *d.messageID != messageID)) ||
			(older && !slices.Contains(d.superseded, messageID)) {
			continue
		}
		return dbgen.GetBridgeRootBindingRow{GroupPublicID: g.publicID, Language: f.routes[g.route].language,
			SnoozeDurationsSeconds: slices.Clone(f.routes[g.route].snooze), WeekStart: bridgeWeekStart}, true
	}
	return dbgen.GetBridgeRootBindingRow{}, false
}

func (f *fakeDB) GetBridgeRootBinding(_ context.Context, arg dbgen.GetBridgeRootBindingParams) (
	dbgen.GetBridgeRootBindingRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetBridgeRootBinding"); err != nil {
		return dbgen.GetBridgeRootBindingRow{}, err
	}
	if r, ok := f.bridgeRoot(arg.ConnectionID.Int64, arg.MessageID, false); ok {
		return r, nil
	}
	return dbgen.GetBridgeRootBindingRow{}, pgx.ErrNoRows
}

func (f *fakeDB) GetBridgeSupersededBinding(_ context.Context, arg dbgen.GetBridgeSupersededBindingParams) (
	dbgen.GetBridgeSupersededBindingRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetBridgeSupersededBinding"); err != nil {
		return dbgen.GetBridgeSupersededBindingRow{}, err
	}
	if r, ok := f.bridgeRoot(arg.ConnectionID.Int64, arg.MessageID, true); ok {
		return dbgen.GetBridgeSupersededBindingRow(r), nil
	}
	return dbgen.GetBridgeSupersededBindingRow{}, pgx.ErrNoRows
}

func (f *fakeDB) GetBridgeReminderBinding(_ context.Context, arg dbgen.GetBridgeReminderBindingParams) (
	dbgen.GetBridgeReminderBindingRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetBridgeReminderBinding"); err != nil {
		return dbgen.GetBridgeReminderBindingRow{}, err
	}
	for _, r := range f.replies {
		ds, g := f.bridgeDest(r.dest, arg.ConnectionID.Int64), f.groups[r.group]
		if ds == nil || g == nil || r.event != "reminder" || r.state != "sent" || r.messageID == nil ||
			*r.messageID != arg.MessageID {
			continue
		}
		return dbgen.GetBridgeReminderBindingRow{GroupPublicID: g.publicID, Language: f.routes[g.route].language,
			SnoozeDurationsSeconds: slices.Clone(f.routes[g.route].snooze), WeekStart: bridgeWeekStart}, nil
	}
	return dbgen.GetBridgeReminderBindingRow{}, pgx.ErrNoRows
}

// TestBridgePressBinding is the binding of C-22.FR-12: a press on a message of a Bridge Connection is bound to the
// delivery of an Alert Group to a Bridge Destination of the Connection whose current Root message it is, or whose older
// Root message a republication past the edit window replaced, or to a sent Reminder there, with the Alert Group, the
// Route's language and Snooze durations and the Organization's first day of the week; another Connection, another
// type, a deleted Destination, a Thread reply that is not a sent Reminder, a Storm summary or an unknown message is not
// bound, and a failed read is an error.
func TestBridgePressBinding(t *testing.T) {
	e, _ := bridged(t)
	e.db.dests[destMM].name = "alerts"
	r := e.db.routes[routeID]
	r.language, r.snooze = "ru", []int64{3600, 14400, 86400}
	e.db.routes[routeID] = r
	current, reminder, other, summary := "m3", "r-1", "r-2", "s-1"
	e.db.deliveries = append(e.db.deliveries,
		&fakeDelivery{id: 1, dest: destMM, group: groupID, state: "delivered", messageID: &current,
			superseded: []string{"m1", "m2"}, publishedAt: at(business0)},
		&fakeDelivery{id: 2, dest: destMM, storm: 9, state: "delivered", messageID: &summary,
			publishedAt: at(business0)})
	e.db.replies = append(e.db.replies,
		&fakeReply{id: 1, delivery: 1, group: groupID, dest: destMM, event: "reminder", state: "sent",
			messageID: &reminder},
		&fakeReply{id: 2, delivery: 1, group: groupID, dest: destMM, event: "alerts_added", state: "sent",
			messageID: &other})
	for _, tc := range []struct {
		message string
		kind    buttons.Subject
		// reads are the reads of the binding, the current Root message's first.
		reads []string
	}{
		{"m3", buttons.SubjectRoot, []string{"GetBridgeRootBinding"}},
		{"m1", buttons.SubjectRoot, []string{"GetBridgeRootBinding", "GetBridgeSupersededBinding"}},
		{"m2", buttons.SubjectRoot, []string{"GetBridgeRootBinding", "GetBridgeSupersededBinding"}},
		{"r-1", buttons.SubjectReply, []string{"GetBridgeRootBinding", "GetBridgeSupersededBinding",
			"GetBridgeReminderBinding"}},
	} {
		calls := maps.Clone(e.db.calls)
		b, err := e.svc.BridgePressBinding(t.Context(), connID, tc.message)
		if err != nil || b.Kind != tc.kind || b.GroupPublicID != "AGAAAAAAAAAA21" || b.Language != "ru" ||
			!slices.Equal(b.SnoozeSeconds, []int64{3600, 14400, 86400}) || b.WeekStart != bridgeWeekStart {
			t.Errorf("BridgePressBinding(%s) = %+v, %v", tc.message, b, err)
		}
		if got := readsSince(calls, e.db.calls); !slices.Equal(got, tc.reads) {
			t.Errorf("BridgePressBinding(%s) read %v", tc.message, got)
		}
	}
	for _, c := range []struct {
		conn    int64
		message string
	}{{connID, "r-2"}, {connID, "s-1"}, {connID, "m9"}, {connID, ""}, {connID + 1, "m3"}, {connID + 1, "m1"},
		{connID + 1, "r-1"}} {
		if _, err := e.svc.BridgePressBinding(t.Context(), c.conn, c.message); !errors.Is(err, delivery.ErrNotBound) {
			t.Errorf("BridgePressBinding(%d, %q) = %v, want ErrNotBound", c.conn, c.message, err)
		}
	}
	e.db.replies[0].state = "pending"
	if _, err := e.svc.BridgePressBinding(t.Context(), connID, "r-1"); !errors.Is(err, delivery.ErrNotBound) {
		t.Errorf("an unsent Reminder = %v", err)
	}
	e.db.replies[0].state = "sent"
	e.db.dests[destMM].typ = delivery.TypeMattermost
	if _, err := e.svc.BridgePressBinding(t.Context(), connID, "m3"); !errors.Is(err, delivery.ErrNotBound) {
		t.Errorf("a Mattermost Destination = %v", err)
	}
	e.db.dests[destMM].typ = delivery.TypeBridge
	e.db.dests[destMM].deleted = true
	for _, m := range []string{"m3", "m1", "r-1"} {
		if _, err := e.svc.BridgePressBinding(t.Context(), connID, m); !errors.Is(err, delivery.ErrNotBound) {
			t.Errorf("%s of a deleted Destination = %v", m, err)
		}
	}
	e.db.dests[destMM].deleted = false
	reads := []string{"GetBridgeRootBinding", "GetBridgeSupersededBinding", "GetBridgeReminderBinding"}
	for i, q := range reads {
		e.db.fail[q] = errBoom
		calls := maps.Clone(e.db.calls)
		if _, err := e.svc.BridgePressBinding(t.Context(), connID, "r-1"); !errors.Is(err, errBoom) {
			t.Errorf("a failed %s = %v", q, err)
		}
		for _, later := range reads[i+1:] {
			if e.db.calls[later] != calls[later] {
				t.Errorf("after a failed %s, %s was read", q, later)
			}
		}
		delete(e.db.fail, q)
	}
}

// TestBridgePressBindingPrefersTheCurrentRootMessage: should a message id be both the current Root message of one
// delivery and an older one of another, the current one is bound.
func TestBridgePressBindingPrefersTheCurrentRootMessage(t *testing.T) {
	e, _ := bridged(t)
	m := "m1"
	e.db.groups[groupID+1] = &fakeGroup{id: groupID + 1, publicID: "AGAAAAAAAAAA22", number: 8, status: "firing",
		route: routeID, created: business0}
	e.db.deliveries = append(e.db.deliveries,
		&fakeDelivery{id: 1, dest: destMM, group: groupID, state: "delivered", superseded: []string{m}},
		&fakeDelivery{id: 2, dest: destMM, group: groupID + 1, state: "delivered", messageID: &m})
	b, err := e.svc.BridgePressBinding(t.Context(), connID, m)
	if err != nil || b.GroupPublicID != "AGAAAAAAAAAA22" || e.db.calls["GetBridgeSupersededBinding"] != 0 {
		t.Errorf("BridgePressBinding = %+v, %v", b, err)
	}
}

// readsSince are the reads of a Bridge binding made since before, in the order the bindings make them.
func readsSince(before, now map[string]int) []string {
	var out []string
	for _, q := range []string{"GetBridgeRootBinding", "GetBridgeSupersededBinding", "GetBridgeReminderBinding"} {
		if now[q] != before[q] {
			out = append(out, q)
		}
	}
	return out
}

// TestPressOnTheOlderRootMessage is C-22.FR-9 and FR-12 on the delivery side: a press on the older Root message of a
// delivery republished past its bridge's edit window is bound to the Alert Group, and the re-render of the Command it
// runs never edits that message — the worker reconciles only the current Root message, within its window or by
// another republication whose pointer goes under the current one.
func TestPressOnTheOlderRootMessage(t *testing.T) {
	e, d, _ := publishedWindowed(t)
	republished := business0.Add(11 * time.Minute)
	e.business.Set(republished)
	e.rec.Script(deliverytest.MethodPublish, publishedAs("m2", "t-2"))
	e.acknowledged(t, 2)
	e.round(t)
	if *d.messageID != "m2" || !slices.Equal(d.superseded, []string{"m1"}) {
		t.Fatalf("not republished: %+v", d)
	}
	e.rec.Reset()
	calls := maps.Clone(e.db.calls)
	b, err := e.svc.BridgePressBinding(t.Context(), connID, "m1")
	if err != nil || b.Kind != buttons.SubjectRoot || b.GroupPublicID != "AGAAAAAAAAAA21" ||
		!slices.Equal(readsSince(calls, e.db.calls), []string{"GetBridgeRootBinding", "GetBridgeSupersededBinding"}) {
		t.Fatalf("BridgePressBinding(m1) = %+v, %v", b, err)
	}
	untouched := func(when string) {
		t.Helper()
		for _, c := range e.rec.Calls() {
			if c.MessageID == "m1" || c.Root.MessageID == "m1" {
				t.Errorf("%s: a call on the older Root message: %+v", when, c)
			}
		}
	}
	// The Command the press runs — Unacknowledge — re-renders within the current message's window: an edit of m2.
	e.business.Set(republished.Add(time.Minute))
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, groups.Recorded{Seq: 3,
		Event: groups.EventUnacknowledged, Variant: groups.VariantCommand, Loudness: groups.Quiet})
	e.round(t)
	if calls := e.rec.Calls(); len(calls) == 0 || calls[0].Method != deliverytest.MethodUpdate ||
		calls[0].MessageID != "m2" {
		t.Errorf("within the window: %s %+v", callMethods(calls), calls)
	}
	untouched("within the window")
	// Past the current message's window as well, the next Command republishes again, pointing from m2, not m1.
	e.rec.Reset()
	e.business.Set(republished.Add(11 * time.Minute))
	e.rec.Script(deliverytest.MethodPublish, publishedAs("m3", "t-3"))
	e.acknowledged(t, 4)
	e.round(t)
	sent := e.rec.Calls()
	if callMethods(sent) != "publish,reply" || sent[1].Root.MessageID != "m2" ||
		!slices.Equal(d.superseded, []string{"m1", "m2"}) || *d.messageID != "m3" {
		t.Errorf("past the window: %s %+v, delivery %+v", callMethods(sent), sent, d)
	}
	untouched("past the window")
	for _, m := range []string{"m1", "m2"} {
		calls := maps.Clone(e.db.calls)
		if b, err := e.svc.BridgePressBinding(t.Context(), connID, m); err != nil ||
			b.GroupPublicID != "AGAAAAAAAAAA21" || !slices.Equal(readsSince(calls, e.db.calls),
			[]string{"GetBridgeRootBinding", "GetBridgeSupersededBinding"}) {
			t.Errorf("BridgePressBinding(%s) after two republications = %+v, %v", m, b, err)
		}
	}
}

func (f *fakeDB) LockBridgeDeletedRoot(_ context.Context, arg dbgen.LockBridgeDeletedRootParams) (
	dbgen.LockBridgeDeletedRootRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("LockBridgeDeletedRoot"); err != nil {
		return dbgen.LockBridgeDeletedRootRow{}, err
	}
	for _, d := range slices.SortedFunc(slices.Values(f.deliveries), func(a, b *fakeDelivery) int {
		return int(a.id - b.id)
	}) {
		if f.bridgeDest(d.dest, arg.ConnectionID.Int64) == nil || d.messageID == nil || *d.messageID != arg.MessageID ||
			d.desiredRetire || endedRow(d) {
			continue
		}
		r := dbgen.LockBridgeDeletedRootRow{ID: d.id, DestinationID: d.dest, RepublishedAfterDelete: d.republished}
		if g := f.groups[d.group]; g != nil {
			r.AlertGroupID = pgtype.Int8{Int64: g.id, Valid: true}
			r.GroupPublicID, r.Resolved = g.publicID, g.status == string(groups.StatusResolved)
		}
		if d.storm != 0 {
			r.StormID = pgtype.Int8{Int64: d.storm, Valid: true}
		}
		return r, nil
	}
	return dbgen.LockBridgeDeletedRootRow{}, pgx.ErrNoRows
}

// TestBridgeThreadBinding is the binding of C-22.FR-14: a Thread message is bound to the delivery of an Alert Group to
// a Bridge Destination of the Connection whose current Root message, or older Root message a republication past the
// edit window replaced, is the message whose Thread it is in, with the Alert Group and the Route's Snooze durations;
// a Reminder, another Thread reply, a Storm summary, another Connection, another type, a deleted Destination or an
// unknown message is not bound, and a failed read is an error after which nothing more is read.
func TestBridgeThreadBinding(t *testing.T) {
	e, _ := bridged(t)
	r := e.db.routes[routeID]
	r.snooze = []int64{3600, 14400, 86400}
	e.db.routes[routeID] = r
	current, reminder, summary := "m3", "r-1", "s-1"
	e.db.deliveries = append(e.db.deliveries,
		&fakeDelivery{id: 1, dest: destMM, group: groupID, state: "delivered", messageID: &current,
			superseded: []string{"m1"}, publishedAt: at(business0)},
		&fakeDelivery{id: 2, dest: destMM, storm: 9, state: "delivered", messageID: &summary,
			publishedAt: at(business0)})
	e.db.replies = append(e.db.replies, &fakeReply{id: 1, delivery: 1, group: groupID, dest: destMM,
		event: "reminder", state: "sent", messageID: &reminder})
	for _, tc := range []struct {
		message string
		reads   []string
	}{
		{"m3", []string{"GetBridgeRootBinding"}},
		{"m1", []string{"GetBridgeRootBinding", "GetBridgeSupersededBinding"}},
	} {
		calls := maps.Clone(e.db.calls)
		b, err := e.svc.BridgeThreadBinding(t.Context(), connID, tc.message)
		if err != nil || b.Kind != buttons.SubjectRoot || b.GroupPublicID != "AGAAAAAAAAAA21" ||
			!slices.Equal(b.SnoozeSeconds, []int64{3600, 14400, 86400}) ||
			!slices.Equal(readsSince(calls, e.db.calls), tc.reads) {
			t.Errorf("BridgeThreadBinding(%s) = %+v, %v", tc.message, b, err)
		}
	}
	for _, c := range []struct {
		conn    int64
		message string
	}{{connID, "r-1"}, {connID, "s-1"}, {connID, "m9"}, {connID + 1, "m3"}, {connID + 1, "m1"}} {
		if _, err := e.svc.BridgeThreadBinding(t.Context(), c.conn, c.message); !errors.Is(err, delivery.ErrNotBound) {
			t.Errorf("BridgeThreadBinding(%d, %q) = %v, want ErrNotBound", c.conn, c.message, err)
		}
	}
	if e.db.calls["GetBridgeReminderBinding"] != 0 {
		t.Error("a Thread message was looked up among the Reminders")
	}
	for _, change := range []func(ds *fakeDest){
		func(ds *fakeDest) { ds.typ = delivery.TypeMattermost },
		func(ds *fakeDest) { ds.deleted = true },
	} {
		ds := *e.db.dests[destMM]
		change(e.db.dests[destMM])
		for _, m := range []string{"m3", "m1"} {
			if _, err := e.svc.BridgeThreadBinding(t.Context(), connID, m); !errors.Is(err, delivery.ErrNotBound) {
				t.Errorf("%s of %+v = %v", m, e.db.dests[destMM], err)
			}
		}
		*e.db.dests[destMM] = ds
	}
	reads := []string{"GetBridgeRootBinding", "GetBridgeSupersededBinding"}
	for i, q := range reads {
		e.db.fail[q] = errBoom
		calls := maps.Clone(e.db.calls)
		if _, err := e.svc.BridgeThreadBinding(t.Context(), connID, "m9"); !errors.Is(err, errBoom) {
			t.Errorf("a failed %s = %v", q, err)
		}
		for _, later := range reads[i+1:] {
			if e.db.calls[later] != calls[later] {
				t.Errorf("after a failed %s, %s was read", q, later)
			}
		}
		delete(e.db.fail, q)
	}
}

// TestRootDeleted is C-22.FR-10 and AC-17 on the delivery side, with C-11.FR-13: a message_deleted event for the
// current Root message of an open Alert Group in a Bridge Destination publishes it again at once — the delivery due
// now, the workers woken — Quietly, with the deletion note and a new Thread; for the republished message it marks the
// pair deleted in the messenger, drops its pending Thread replies and leaves it alone; once ended, the same message
// changes nothing.
func TestRootDeleted(t *testing.T) {
	e, d, _ := publishedBridge(t, publishedAs("m1", "t-1"))
	d.threadState, d.anchorID = "attached", at2("t-1")
	e.db.events = nil
	deleted := time.Date(2026, 10, 7, 12, 42, 0, 0, time.UTC)
	e.business.Set(deleted)
	woken, hints := e.db.notified, len(e.db.hints)
	if err := e.svc.RootDeleted(t.Context(), connID, "m1"); err != nil {
		t.Fatalf("RootDeleted(m1) = %v", err)
	}
	if d.state != "pending" || !d.republished || d.messageID != nil || d.threadState != "none" || d.anchorID != nil ||
		!d.next.Equal(deleted) || e.db.notified != woken+1 || len(e.db.hints) != hints+1 {
		t.Fatalf("not reset for a republication: %+v, %d wakes, %d hints", d, e.db.notified, len(e.db.hints))
	}
	if !slices.Equal(e.eventKinds(), []string{"republished"}) || e.db.events[0].Loudness != "quiet" ||
		!e.db.events[0].OccurredAt.Equal(deleted) {
		t.Errorf("events %+v", e.db.events)
	}
	e.rec.Script(deliverytest.MethodPublish, publishedAs("m2", "t-2"))
	e.round(t)
	pubs := e.publications()
	if len(pubs) != 1 || pubs[0].Loudness != groups.Quiet || len(pubs[0].Mentions) != 0 ||
		pubs[0].Message.Notices[len(pubs[0].Message.Notices)-1] != "The previous message was deleted at 12:42." {
		t.Fatalf("republication %+v", pubs)
	}
	if d.state != "delivered" || *d.messageID != "m2" || d.publications != 2 {
		t.Fatalf("not republished %+v", d)
	}
	// The republished Root message is deleted too: the pair is deleted in the messenger, its pending replies dropped.
	pending := "pending"
	e.db.replies = append(e.db.replies, &fakeReply{id: 1, delivery: d.id, group: groupID, dest: destMM,
		event: "alerts_added", state: pending})
	e.rec.Reset()
	if err := e.svc.RootDeleted(t.Context(), connID, "m2"); err != nil {
		t.Fatalf("RootDeleted(m2) = %v", err)
	}
	if d.state != "deleted_in_messenger" || e.db.replies[0].state != "dropped" ||
		e.eventKinds()[len(e.db.events)-1] != "deleted_in_messenger" {
		t.Errorf("the pair %+v, reply %s, events %v", d, e.db.replies[0].state, e.eventKinds())
	}
	events, lockedRows := len(e.db.events), e.db.calls["LockBridgeDeletedRoot"]
	for _, m := range []string{"m2", "m1"} {
		if err := e.svc.RootDeleted(t.Context(), connID, m); err != nil || len(e.db.events) != events {
			t.Errorf("RootDeleted(%s) after the pair = %v, events %v", m, err, e.eventKinds())
		}
	}
	if e.db.calls["LockBridgeDeletedRoot"] != lockedRows+2 {
		t.Errorf("%d locks after the pair", e.db.calls["LockBridgeDeletedRoot"]-lockedRows)
	}
	e.business.Advance(time.Hour)
	e.round(t)
	if len(e.rec.Calls()) != 0 || len(e.db.events) != events || d.state != "deleted_in_messenger" {
		t.Errorf("after the pair: %s, events %v, %+v", callMethods(e.rec.Calls()), e.eventKinds(), d)
	}
}

// TestRootDeletedResolved is C-11.FR-13 through a message_deleted event: the Root message of a resolved Alert Group is
// marked deleted in the messenger at once, never published again.
func TestRootDeletedResolved(t *testing.T) {
	e, d, _ := publishedBridge(t, publishedAs("m1", "t-1"))
	e.db.groups[groupID].status = string(groups.StatusResolved)
	e.db.events = nil
	if err := e.svc.RootDeleted(t.Context(), connID, "m1"); err != nil {
		t.Fatalf("RootDeleted = %v", err)
	}
	if d.state != "deleted_in_messenger" || d.republished || !slices.Equal(e.eventKinds(), []string{
		"deleted_in_messenger"}) {
		t.Errorf("%+v, events %v", d, e.eventKinds())
	}
}

// TestRootDeletedStormSummary: a deleted Storm summary of a Bridge Destination is published again, as after a gone
// answer to its edit.
func TestRootDeletedStormSummary(t *testing.T) {
	e, _ := bridged(t)
	summary := "s-1"
	d := &fakeDelivery{id: 1, dest: destMM, storm: 9, state: "delivered", messageID: &summary,
		publishedAt: at(business0)}
	e.db.deliveries = append(e.db.deliveries, d)
	if err := e.svc.RootDeleted(t.Context(), connID, "s-1"); err != nil {
		t.Fatalf("RootDeleted = %v", err)
	}
	if d.state != "pending" || !d.republished || d.messageID != nil || len(e.db.events) != 1 ||
		e.db.events[0].StormID.Int64 != 9 || e.db.events[0].AlertGroupID.Valid {
		t.Errorf("%+v, events %+v", d, e.db.events)
	}
}

// TestRootDeletedOtherMessages is C-22.FR-10: a message_deleted event for any other message of the bot changes
// nothing — an older Root message a republication past the edit window replaced, a Thread reply, an unknown message,
// a Root message of another Connection, of a Mattermost Destination, of a deleted Destination or of a delivery that
// retires — and a failed lock or flow is an error.
func TestRootDeletedOtherMessages(t *testing.T) {
	e, d, _ := publishedBridge(t, publishedAs("m1", "t-1"))
	d.superseded = []string{"m0"}
	reply := "r-1"
	e.db.replies = append(e.db.replies, &fakeReply{id: 1, delivery: d.id, group: groupID, dest: destMM,
		event: "reminder", state: "sent", messageID: &reply})
	e.db.events = nil
	before := *d
	unchanged := func(what string) {
		t.Helper()
		if d.state != before.state || d.republished || d.messageID == nil || *d.messageID != "m1" ||
			len(e.db.events) != 0 {
			t.Errorf("%s changed the delivery: %+v, events %v", what, d, e.eventKinds())
		}
	}
	for _, c := range []struct {
		conn    int64
		message string
	}{{connID, "m0"}, {connID, "r-1"}, {connID, "m9"}, {connID, ""}, {connID + 1, "m1"}} {
		if err := e.svc.RootDeleted(t.Context(), c.conn, c.message); err != nil {
			t.Errorf("RootDeleted(%d, %q) = %v", c.conn, c.message, err)
		}
		unchanged(c.message)
	}
	for name, change := range map[string]func(){
		"a Mattermost Destination": func() { e.db.dests[destMM].typ = delivery.TypeMattermost },
		"a deleted Destination":    func() { e.db.dests[destMM].deleted = true },
		"a retiring delivery":      func() { d.desiredRetire = true },
	} {
		ds, row := *e.db.dests[destMM], *d
		change()
		if err := e.svc.RootDeleted(t.Context(), connID, "m1"); err != nil {
			t.Errorf("%s: RootDeleted = %v", name, err)
		}
		*e.db.dests[destMM], *d = ds, row
		unchanged(name)
	}
	for _, q := range []string{"LockBridgeDeletedRoot", "ResetForRepublish", "InsertDeliveryEvent", "NotifyDelivery"} {
		e.db.fail[q] = errBoom
		if err := e.svc.RootDeleted(t.Context(), connID, "m1"); !errors.Is(err, errBoom) {
			t.Errorf("a failed %s = %v", q, err)
		}
		delete(e.db.fail, q)
		*d = before
		e.db.events = nil
	}
}

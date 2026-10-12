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

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/dbgen"
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

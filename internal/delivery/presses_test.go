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
		messageID: &post})
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
		messageID: &post}, &fakeDelivery{id: 2, dest: destMM, group: groupID, state: "pending"})
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
		messageID: &post})
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

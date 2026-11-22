// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package connections_test

import (
	"errors"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/connections"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/managedby"
)

// onlyMark reports whether the newest Audit log entry of e is connection.updated with /managed_by alone, from before
// to after.
func onlyMark(t *testing.T, e *env, before, after any) bool {
	t.Helper()
	a := e.store.audit[len(e.store.audit)-1]
	d := diffOf(t, a)
	return a.Action == connections.ActionUpdated && len(d) == 1 && d[0].Pointer == managedby.Pointer &&
		d[0].Before == before && d[0].After == after
}

// TestMarkOnlySaveOfASocket is C-33.FR-11 on a Slack Connection in socket mode: a save that changes the mark
// managed_by and nothing else keeps the version that the Leader's socket, the cached client and the version-guarded
// writes follow, and forgets nothing: the socket is listed again with the same version and client, so it does not
// reconnect, what its last hello counted stays, and a hello of the socket read before the save is still recorded. The
// save writes the mark alone, calls nothing and is recorded with /managed_by alone. A save of the mark together with
// the name moves the version and forgets the hello, as any other save.
func TestMarkOnlySaveOfASocket(t *testing.T) {
	e, f := newSlackEnv(t)
	ctx := t.Context()
	sl := e.createSlack(t, slackInput("sl", connections.EventModeSocket))
	list, err := e.svc.SocketConnections(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("socket connections = %+v, %v", list, err)
	}
	sock := list[0]
	e.business.Advance(time.Minute)
	if err := e.svc.RecordSocketHello(ctx, sock, 1); err != nil {
		t.Fatal(err)
	}
	before, err := e.svc.Get(ctx, sl.PublicID)
	if err != nil || before.SlackSocket == nil {
		t.Fatalf("before the save: %+v, %v", before.SlackSocket, err)
	}
	updates, calls := len(e.store.updates), len(f.Requests())

	in := slackInput("sl", connections.EventModeSocket)
	in.BotToken, in.AppToken, in.ManagedBy = keyring.Keep, keyring.Keep, managedby.Set("terraform")
	u, err := e.svc.Update(ctx, by, sl.PublicID, new(before.Version), in)
	if err != nil || markOf(u) != "terraform" {
		t.Fatalf("a save of the mark alone = %s, %v", markOf(u), err)
	}
	if row := e.store.rows[sl.ID].row; u.Version != before.Version || row.Version != before.Version {
		t.Errorf("a save of the mark alone moved the version from %d to %d (stored %d)", before.Version, u.Version,
			row.Version)
	}
	if u.SlackSocket == nil || *u.SlackSocket != *before.SlackSocket {
		t.Errorf("a save of the mark alone forgot the hello: %+v, before %+v", u.SlackSocket, before.SlackSocket)
	}
	if u.ManagedByVersion != before.ManagedByVersion+1 || u.Tag() != before.Tag()+1 {
		t.Errorf("a save of the mark alone: tag %d (%d marks), before %d", u.Tag(), u.ManagedByVersion, before.Tag())
	}
	if got, err := e.svc.Get(ctx, sl.PublicID); err != nil || got.Tag() != u.Tag() || markOf(got) != "terraform" {
		t.Errorf("the read after the save: tag %d, mark %s, %v; want %d", got.Tag(), markOf(got), err, u.Tag())
	}
	if len(e.store.updates) != updates || len(f.Requests()) != calls {
		t.Errorf("a save of the mark alone rewrote the configuration %d times and called Slack %d times",
			len(e.store.updates)-updates, len(f.Requests())-calls)
	}
	if !onlyMark(t, e, nil, "terraform") {
		t.Errorf("the Audit log of the mark alone: %s", e.store.audit[len(e.store.audit)-1].Diff)
	}
	again, err := e.svc.SocketConnections(ctx)
	if err != nil || len(again) != 1 || again[0].Version != sock.Version || again[0].Client != sock.Client {
		t.Errorf("after the save the socket is listed with version %d and a new client %t, before %d",
			again[0].Version, again[0].Client != sock.Client, sock.Version)
	}
	e.business.Advance(time.Minute)
	if err := e.svc.RecordSocketHello(ctx, sock, 2); err != nil {
		t.Fatal(err)
	}
	if row := e.store.rows[sl.ID].row; row.SlackSocketOtherConnections.Int32 != 2 ||
		!row.SlackSocketHelloAt.Time.Equal(t0.Add(2*time.Minute)) {
		t.Errorf("the hello of the socket read before the save was not recorded: %+v", row.SlackSocketHelloAt)
	}

	// The mark with a new name starts the socket again: a new version, and the hello forgotten.
	in.Name, in.ManagedBy = "sl-2", managedby.Set("opentofu")
	u, err = e.svc.Update(ctx, by, sl.PublicID, nil, in)
	if err != nil || u.Version != before.Version+1 || u.SlackSocket != nil || len(e.store.updates) != updates+1 {
		t.Errorf("the mark and a new name = version %d, socket %+v, %v", u.Version, u.SlackSocket, err)
	}
}

// TestMarkOnlySaveIfMatch is NFR-13 after a save of the mark alone: the ETag names the Tag, version plus the saves of
// the mark alone, so the Tag read before the save and the version alone are stale on an update and a deletion, and the
// Tag read after it is current; a save of the mark with the name moves the version and keeps the count; the list
// carries the count; a failed write of the mark writes nothing.
func TestMarkOnlySaveIfMatch(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	c := e.create(t, "mm")
	in := input("mm", e.fake.URL())
	in.BotToken, in.ManagedBy = keyring.Keep, managedby.Set("terraform")

	errBoom := errors.New("boom")
	e.store.fail["SetConnectionMark"] = errBoom
	if _, err := e.svc.Update(ctx, by, c.PublicID, nil, in); !errors.Is(err, errBoom) {
		t.Errorf("a failed write of the mark = %v", err)
	}
	delete(e.store.fail, "SetConnectionMark")
	if got, _ := e.svc.Get(ctx, c.PublicID); got.Tag() != c.Tag() || got.ManagedBy != nil || len(e.store.audit) != 1 {
		t.Errorf("a failed write of the mark wrote: tag %d, mark %s, %d entries", got.Tag(), markOf(got),
			len(e.store.audit))
	}

	marked, err := e.svc.Update(ctx, by, c.PublicID, new(c.Tag()), in)
	if err != nil || marked.Version != c.Version || marked.Tag() != c.Tag()+1 {
		t.Fatalf("the mark alone = version %d, tag %d, %v", marked.Version, marked.Tag(), err)
	}
	in.Name = "mm-2"
	for _, stale := range []int64{c.Tag(), marked.Version} {
		if _, err := e.svc.Update(ctx, by, c.PublicID, new(stale), in); !errors.Is(err, connections.ErrVersionMismatch) {
			t.Errorf("an update with If-Match %d = %v, want ErrVersionMismatch", stale, err)
		}
		if err := e.svc.Delete(ctx, by, c.PublicID, new(stale)); !errors.Is(err, connections.ErrVersionMismatch) {
			t.Errorf("a deletion with If-Match %d = %v, want ErrVersionMismatch", stale, err)
		}
	}
	renamed, err := e.svc.Update(ctx, by, c.PublicID, new(marked.Tag()), in)
	if err != nil || renamed.Version != marked.Version+1 || renamed.ManagedByVersion != marked.ManagedByVersion ||
		renamed.Tag() != marked.Tag()+1 {
		t.Fatalf("a rename after the mark = version %d, %d marks, %v", renamed.Version, renamed.ManagedByVersion, err)
	}
	page, err := e.svc.List(ctx, connections.ListFilter{Limit: 10})
	if err != nil || len(page.Connections) != 1 || page.Connections[0].Tag() != renamed.Tag() {
		t.Errorf("the list = %+v, %v", page.Connections, err)
	}
	if err := e.svc.Delete(ctx, by, c.PublicID, new(marked.Tag())); !errors.Is(err, connections.ErrVersionMismatch) {
		t.Errorf("a deletion with the Tag before the rename = %v", err)
	}
	if err := e.svc.Delete(ctx, by, c.PublicID, new(renamed.Tag())); err != nil {
		t.Errorf("a deletion with the current Tag = %v", err)
	}
}

// TestMarkOnlySaveOfAPoller is C-33.FR-11 on a Bridge Connection in poll mode: a save that changes the mark managed_by
// and nothing else keeps the version that the Leader's poller and the cached client follow, so the poller is listed
// with the same version, client and cursor and goes on, reads no info and stores the cursor of a batch it polled
// before the save; the save is recorded with /managed_by alone.
func TestMarkOnlySaveOfAPoller(t *testing.T) {
	e, b := newBridgeEnv(t)
	ctx := t.Context()
	br := e.createBridge(t, "br", b.srv.URL, connections.EventDeliveryPoll)
	list, err := e.svc.PollingConnections(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("polling connections = %+v, %v", list, err)
	}
	poll := list[0]
	cursor := "7"
	if err := e.svc.StoreEventsCursor(ctx, poll.Conn, &cursor); err != nil {
		t.Fatal(err)
	}
	updates, requests := len(e.store.updates), len(b.seen())

	in := bridgeInput("br", b.srv.URL, connections.EventDeliveryPoll)
	in.BotToken, in.ManagedBy = keyring.Keep, managedby.Set("terraform")
	u, err := e.svc.Update(ctx, by, br.PublicID, nil, in)
	if err != nil || markOf(u) != "terraform" {
		t.Fatalf("a save of the mark alone = %s, %v", markOf(u), err)
	}
	if u.Version != br.Version || len(e.store.updates) != updates || len(b.seen()) != requests ||
		u.BridgeInfo == nil {
		t.Errorf("a save of the mark alone: version %d, before %d, %d rewrites, %d requests, info %s", u.Version,
			br.Version, len(e.store.updates)-updates, len(b.seen())-requests, u.BridgeInfo)
	}
	if !onlyMark(t, e, nil, "terraform") {
		t.Errorf("the Audit log of the mark alone: %s", e.store.audit[len(e.store.audit)-1].Diff)
	}
	again, err := e.svc.PollingConnections(ctx)
	if err != nil || len(again) != 1 || again[0].Conn.Version != poll.Conn.Version ||
		again[0].Conn.Client != poll.Conn.Client || again[0].Cursor == nil || *again[0].Cursor != cursor {
		t.Errorf("after the save the poller is listed with %+v, before %+v", again, list)
	}
	next := "8"
	if err := e.svc.StoreEventsCursor(ctx, poll.Conn, &next); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.PollingConnections(ctx); len(got) != 1 || got[0].Cursor == nil || *got[0].Cursor != next {
		t.Errorf("the cursor of a batch polled before the save was not stored: %+v", got)
	}
}

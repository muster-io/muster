// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package gateways

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/gateways/dbgen"
	"github.com/muster-io/muster/internal/logging"
)

// fakeCalls is the calls of one Organization in memory, with the semantics of query.sql: the Users 1 (petr, English)
// and 2 (olga, Russian), the Notification gateways 10 and 11, the Bridge Connection 12 for messages only, and the Alert
// Groups added with group. A transaction that fails is rolled back. fail makes the named query fail.
type fakeCalls struct {
	mu       sync.Mutex
	calls    []fakeCall
	items    []fakeItem
	groups   map[int64]fakeGroup
	users    map[int64]fakeCallUser
	gateways map[int64]fakeGateway
	// contacts are the confirmed contacts by User and gateway.
	contacts map[[2]int64]dbgen.CallContactRow
	fail     map[string]error
	locks    []string
	notified int
	nextID   int64
}

type fakeCall struct {
	id                                  int64
	publicID                            string
	conn, user                          int64
	contactID                           pgtype.Int8
	state                               string
	critical                            bool
	language, text                      pgtype.Text
	notBefore, nextAttempt              pgtype.Timestamptz
	owner                               string
	leaseUntil                          pgtype.Timestamptz
	attempts                            int64
	firstAttempt, firstFailed, ended    pgtype.Timestamptz
	errorClass, errorText, notification pgtype.Text
	reason                              pgtype.Text
	created                             time.Time
}

type fakeItem struct {
	call, group int64
	queued      time.Time
	dropped     pgtype.Timestamptz
}

type fakeGroup struct {
	publicID string
	number   int64
	title    string
	severity string
	status   string
}

type fakeCallUser struct {
	publicID, name, login string
	language              pgtype.Text
	status                string
}

func newFakeCalls() *fakeCalls {
	return &fakeCalls{
		groups: map[int64]fakeGroup{},
		users: map[int64]fakeCallUser{
			1: {publicID: "SR0000000000P1", name: "Petr", login: "petr", status: "active"},
			2: {publicID: "SR0000000000P2", name: "Olga", login: "olga", status: "active",
				language: pgtype.Text{String: "ru", Valid: true}},
			3: {publicID: "SR0000000000P3", name: "Gone", login: "gone", status: "deleted"},
		},
		gateways: map[int64]fakeGateway{10: {publicID: "CN00000000GW10", name: "Gateway", gateway: true},
			11: {publicID: "CN00000000GW11", name: "Second", gateway: true},
			12: {publicID: "CN00000000BR12", name: "Bridge"}},
		contacts: map[[2]int64]dbgen.CallContactRow{{1, 10}: {ID: 501, Contact: "+10000000001"},
			{2, 10}: {ID: 502, Contact: "+10000000002"}, {1, 11}: {ID: 503, Contact: "+10000000003"}},
		fail: map[string]error{}, nextID: 1,
	}
}

// group adds the firing Alert Group id, #id, of severity.
func (f *fakeCalls) group(id int64, title, severity string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.groups[id] = fakeGroup{publicID: fmt.Sprintf("AG00000000%04d", id),
		number: id, title: title, severity: severity, status: "firing"}
}

// setStatus sets the status of the Alert Group id.
func (f *fakeCalls) setStatus(id int64, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := f.groups[id]
	g.status = status
	f.groups[id] = g
}

func (f *fakeCalls) err(name string) error { return f.fail[name] }

// InCallTx runs fn on the fake, restoring its calls and items when fn fails.
func (f *fakeCalls) InCallTx(_ context.Context, fn func(DBTX, CallQueries) error) error {
	f.mu.Lock()
	calls, items := slices.Clone(f.calls), slices.Clone(f.items)
	f.mu.Unlock()
	if err := fn(nil, f); err != nil {
		f.mu.Lock()
		f.calls, f.items = calls, items
		f.mu.Unlock()
		return err
	}
	return nil
}

func (f *fakeCalls) call(id int64) *fakeCall {
	for i := range f.calls {
		if f.calls[i].id == id {
			return &f.calls[i]
		}
	}
	return nil
}

func (f *fakeCalls) GetPage(_ context.Context, a dbgen.GetPageParams) (dbgen.GetPageRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err("GetPage"); err != nil {
		return dbgen.GetPageRow{}, err
	}
	g, ok := f.groups[a.AlertGroupID]
	gw, gok := f.gateways[a.ConnectionID]
	u, uok := f.users[a.UserID]
	if !ok || !gok || !gw.gateway || !uok || u.status == "deleted" || a.OrgID != 1 {
		return dbgen.GetPageRow{}, pgx.ErrNoRows
	}
	return dbgen.GetPageRow{Number: g.number, Critical: g.severity == "critical", ConnectionPublicID: gw.publicID,
		UserPublicID: u.publicID}, nil
}

func (f *fakeCalls) GetPageGroupID(_ context.Context, a dbgen.GetPageGroupIDParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, g := range f.groups {
		if g.publicID == a.PublicID {
			return id, f.err("GetPageGroupID")
		}
	}
	return 0, cmp.Or(f.err("GetPageGroupID"), pgx.ErrNoRows)
}

func (f *fakeCalls) GetGateway(_ context.Context, a dbgen.GetGatewayParams) (dbgen.GetGatewayRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, g := range f.gateways {
		if g.publicID == a.PublicID && g.gateway {
			return dbgen.GetGatewayRow{ID: id, PublicID: g.publicID, Name: g.name}, nil
		}
	}
	return dbgen.GetGatewayRow{}, pgx.ErrNoRows
}

func (f *fakeCalls) GetUserByPublicID(_ context.Context, a dbgen.GetUserByPublicIDParams) (
	dbgen.GetUserByPublicIDRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, u := range f.users {
		if u.publicID == a.PublicID {
			return dbgen.GetUserByPublicIDRow{ID: id, PublicID: u.publicID, Status: u.status}, f.err("GetUser")
		}
	}
	return dbgen.GetUserByPublicIDRow{}, cmp.Or(f.err("GetUser"), pgx.ErrNoRows)
}

func (f *fakeCalls) LockCallPerson(_ context.Context, a dbgen.LockCallPersonParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a.LockClass != db.GatewayCallLockClass {
		return errors.New("the wrong lock class")
	}
	f.locks = append(f.locks, fmt.Sprintf("%d/%d", a.ConnectionID, a.UserID))
	return f.err("LockCallPerson")
}

func (f *fakeCalls) PreviousCall(_ context.Context, a dbgen.PreviousCallParams) (dbgen.PreviousCallRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err("PreviousCall"); err != nil {
		return dbgen.PreviousCallRow{}, err
	}
	var out *fakeCall
	for i := range f.calls {
		c := &f.calls[i]
		if c.conn == a.ConnectionID && c.user == a.UserID && c.firstAttempt.Valid && (out == nil || c.id > out.id) {
			out = c
		}
	}
	if out == nil {
		return dbgen.PreviousCallRow{}, pgx.ErrNoRows
	}
	return dbgen.PreviousCallRow{State: out.state, EndedAt: out.ended}, nil
}

func (f *fakeCalls) LockWaitingCall(_ context.Context, a dbgen.LockWaitingCallParams) (dbgen.LockWaitingCallRow,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err("LockWaitingCall"); err != nil {
		return dbgen.LockWaitingCallRow{}, err
	}
	for _, c := range f.calls {
		if c.conn == a.ConnectionID && c.user == a.UserID && c.state == CallWaiting {
			return dbgen.LockWaitingCallRow{ID: c.id, PublicID: c.publicID}, nil
		}
	}
	return dbgen.LockWaitingCallRow{}, pgx.ErrNoRows
}

func (f *fakeCalls) InsertWaitingCall(_ context.Context, a dbgen.InsertWaitingCallParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err("InsertWaitingCall"); err != nil {
		return 0, err
	}
	for _, c := range f.calls {
		if c.conn == a.ConnectionID && c.user == a.UserID && c.state == CallWaiting {
			return 0, errors.New("duplicate key value violates unique constraint \"gateway_calls_waiting_key\"")
		}
	}
	id := f.nextID
	f.nextID++
	f.calls = append(f.calls, fakeCall{id: id, publicID: a.PublicID, conn: a.ConnectionID, user: a.UserID,
		state: CallWaiting, critical: a.Critical, notBefore: a.NotBefore, nextAttempt: a.NotBefore, created: a.Now})
	return id, nil
}

func (f *fakeCalls) MarkCallCritical(_ context.Context, a dbgen.MarkCallCriticalParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.call(a.ID); c != nil && c.state == CallWaiting {
		c.critical = true
	}
	return f.err("MarkCallCritical")
}

func (f *fakeCalls) AddCallItem(_ context.Context, a dbgen.AddCallItemParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err("AddCallItem"); err != nil {
		return err
	}
	i := slices.IndexFunc(f.items, func(i fakeItem) bool { return i.call == a.CallID && i.group == a.AlertGroupID })
	switch {
	case i < 0:
		f.items = append(f.items, fakeItem{call: a.CallID, group: a.AlertGroupID, queued: a.Now})
	case f.items[i].dropped.Valid:
		f.items[i].dropped, f.items[i].queued = pgtype.Timestamptz{}, a.Now
	}
	return nil
}

func (f *fakeCalls) NotifyCalls(_ context.Context, channel string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if channel != CallsChannel {
		return errors.New("the wrong channel")
	}
	f.notified++
	return f.err("NotifyCalls")
}

func (f *fakeCalls) ClaimDueCalls(_ context.Context, a dbgen.ClaimDueCallsParams) ([]dbgen.ClaimDueCallsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err("ClaimDueCalls"); err != nil {
		return nil, err
	}
	var due []*fakeCall
	for i := range f.calls {
		c := &f.calls[i]
		if (c.state == CallWaiting || c.state == CallSending) && c.nextAttempt.Valid &&
			!c.nextAttempt.Time.After(a.Due) && (!c.leaseUntil.Valid || !c.leaseUntil.Time.After(a.Now)) {
			due = append(due, c)
		}
	}
	slices.SortFunc(due, func(x, y *fakeCall) int {
		if x.critical != y.critical {
			if x.critical {
				return -1
			}
			return 1
		}
		return cmp.Or(x.nextAttempt.Time.Compare(y.nextAttempt.Time), cmp.Compare(x.id, y.id))
	})
	var out []dbgen.ClaimDueCallsRow
	for _, c := range due[:min(len(due), int(a.Lim))] {
		c.owner, c.leaseUntil = a.Owner, pgtype.Timestamptz{Time: a.LeaseUntil, Valid: true}
		out = append(out, dbgen.ClaimDueCallsRow{ID: c.id, PublicID: c.publicID, ConnectionID: c.conn, UserID: c.user,
			Critical: c.critical, NextAttemptAt: c.nextAttempt.Time})
	}
	return out, nil
}

func (f *fakeCalls) GetLeasedCall(_ context.Context, a dbgen.GetLeasedCallParams) (dbgen.GetLeasedCallRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err("GetLeasedCall"); err != nil {
		return dbgen.GetLeasedCallRow{}, err
	}
	c := f.call(a.ID)
	if c == nil || (c.state != CallWaiting && c.state != CallSending) || c.owner != a.Owner ||
		!c.leaseUntil.Time.After(a.Now) {
		return dbgen.GetLeasedCallRow{}, pgx.ErrNoRows
	}
	gw, u := f.gateways[c.conn], f.users[c.user]
	row := dbgen.GetLeasedCallRow{ID: c.id, PublicID: c.publicID, ConnectionID: c.conn, UserID: c.user,
		State: c.state, Critical: c.critical, Language: c.language, Text: c.text, Attempts: c.attempts,
		FirstFailedAt: c.firstFailed, ConnectionPublicID: gw.publicID, ConnectionName: gw.name,
		UserPublicID: u.publicID, UserName: u.name, UserLogin: u.login, UserLanguage: u.language}
	for _, k := range f.contacts {
		if c.contactID.Valid && k.ID == c.contactID.Int64 {
			row.Contact = pgtype.Text{String: k.Contact, Valid: true}
		}
	}
	return row, nil
}

func (f *fakeCalls) ListCallGroups(_ context.Context, a dbgen.ListCallGroupsParams) ([]dbgen.ListCallGroupsRow,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err("ListCallGroups"); err != nil {
		return nil, err
	}
	out := []dbgen.ListCallGroupsRow{}
	for _, i := range f.items {
		if g, ok := f.groups[i.group]; ok && i.call == a.CallID && !i.dropped.Valid {
			out = append(out, dbgen.ListCallGroupsRow{ID: i.group, PublicID: g.publicID, Number: g.number,
				Title: g.title, SeverityLevel: g.severity, Status: g.status})
		}
	}
	slices.SortFunc(out, func(x, y dbgen.ListCallGroupsRow) int { return cmp.Compare(x.Number, y.Number) })
	return out, nil
}

func (f *fakeCalls) DropCallItems(_ context.Context, a dbgen.DropCallItemsParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for n := range f.items {
		i := &f.items[n]
		if i.call == a.CallID && slices.Contains(a.AlertGroupIds, i.group) && !i.dropped.Valid {
			i.dropped = pgtype.Timestamptz{Time: a.Now, Valid: true}
		}
	}
	return f.err("DropCallItems")
}

func (f *fakeCalls) CallContact(_ context.Context, a dbgen.CallContactParams) (dbgen.CallContactRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err("CallContact"); err != nil {
		return dbgen.CallContactRow{}, err
	}
	k, ok := f.contacts[[2]int64{a.UserID, a.ConnectionID}]
	if !ok {
		return dbgen.CallContactRow{}, pgx.ErrNoRows
	}
	return k, nil
}

func (f *fakeCalls) StartCall(_ context.Context, a dbgen.StartCallParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err("StartCall"); err != nil {
		return 0, err
	}
	c := f.call(a.ID)
	if c == nil || c.state != CallWaiting || c.owner != a.Owner {
		return 0, nil
	}
	at := pgtype.Timestamptz{Time: a.Now, Valid: true}
	c.state, c.contactID, c.language, c.text = CallSending, a.ContactID, pgtype.Text{String: a.Language, Valid: true},
		pgtype.Text{String: a.Text, Valid: true}
	c.firstAttempt, c.nextAttempt, c.leaseUntil = at, at, pgtype.Timestamptz{Time: a.LeaseUntil, Valid: true}
	if !c.notBefore.Valid {
		c.notBefore = at
	}
	return 1, nil
}

func (f *fakeCalls) RenewCallLease(_ context.Context, a dbgen.RenewCallLeaseParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.call(a.ID)
	if c == nil || c.state != CallSending || c.owner != a.Owner {
		return 0, f.err("RenewCallLease")
	}
	c.leaseUntil = pgtype.Timestamptz{Time: a.LeaseUntil, Valid: true}
	return 1, f.err("RenewCallLease")
}

func (f *fakeCalls) release(c *fakeCall) {
	c.owner, c.leaseUntil = "", pgtype.Timestamptz{}
}

func (f *fakeCalls) RescheduleCall(_ context.Context, a dbgen.RescheduleCallParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err("RescheduleCall"); err != nil {
		return 0, err
	}
	c := f.call(a.ID)
	if c == nil || (c.state != CallWaiting && c.state != CallSending) || c.owner != a.Owner {
		return 0, nil
	}
	c.nextAttempt = pgtype.Timestamptz{Time: a.At, Valid: true}
	f.release(c)
	if a.ErrorClass.Valid {
		c.errorClass = a.ErrorClass
	}
	if a.Error.Valid {
		c.errorText = a.Error
	}
	return 1, nil
}

func (f *fakeCalls) RecordCallRetry(_ context.Context, a dbgen.RecordCallRetryParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err("RecordCallRetry"); err != nil {
		return 0, err
	}
	c := f.call(a.ID)
	if c == nil || c.state != CallSending || c.owner != a.Owner {
		return 0, nil
	}
	c.attempts++
	if !c.firstFailed.Valid {
		c.firstFailed = pgtype.Timestamptz{Time: a.Now, Valid: true}
	}
	c.nextAttempt = pgtype.Timestamptz{Time: a.At, Valid: true}
	c.errorClass, c.errorText = pgtype.Text{String: a.ErrorClass, Valid: true}, a.Error
	f.release(c)
	return 1, nil
}

func (f *fakeCalls) EndCall(_ context.Context, a dbgen.EndCallParams) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err("EndCall"); err != nil {
		return false, err
	}
	c := f.call(a.ID)
	if c == nil || (c.state != CallWaiting && c.state != CallSending) || c.owner != a.Owner {
		return false, pgx.ErrNoRows
	}
	c.state, c.reason, c.notification = a.State, a.Reason, a.NotificationID
	c.attempts += a.Attempted
	c.ended, c.nextAttempt = pgtype.Timestamptz{Time: a.Now, Valid: true}, pgtype.Timestamptz{}
	if a.ErrorClass.Valid {
		c.errorClass, c.errorText = a.ErrorClass, a.Error
		if !c.firstFailed.Valid {
			c.firstFailed = c.ended
		}
	}
	f.release(c)
	return c.firstAttempt.Valid, nil
}

func (f *fakeCalls) ReleaseWaitingCall(_ context.Context, a dbgen.ReleaseWaitingCallParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.calls {
		c := &f.calls[i]
		if c.conn == a.ConnectionID && c.user == a.UserID && c.state == CallWaiting && !c.notBefore.Valid {
			c.notBefore = pgtype.Timestamptz{Time: a.At, Valid: true}
			c.nextAttempt = c.notBefore
		}
	}
	return f.err("ReleaseWaitingCall")
}

func (f *fakeCalls) NextCallWork(_ context.Context, a dbgen.NextCallWorkParams) (dbgen.NextCallWorkRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out dbgen.NextCallWorkRow
	for _, c := range f.calls {
		if (c.state != CallWaiting && c.state != CallSending) || !c.nextAttempt.Valid {
			continue
		}
		if !c.leaseUntil.Valid || !c.leaseUntil.Time.After(a.Now) {
			if out.FreeAt.IsZero() || c.nextAttempt.Time.Before(out.FreeAt) {
				out.FreeAt = c.nextAttempt.Time
			}
		} else if out.LeaseEnd.IsZero() || c.leaseUntil.Time.Before(out.LeaseEnd) {
			out.LeaseEnd = c.leaseUntil.Time
		}
	}
	return out, f.err("NextCallWork")
}

// itemsOf are the Alert Groups of the call id, "+" before the kept ones and "-" before the dropped ones.
func (f *fakeCalls) itemsOf(id int64) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, i := range f.items {
		if i.call == id {
			mark := "+"
			if i.dropped.Valid {
				mark = "-"
			}
			out = append(out, mark+f.groups[i.group].title)
		}
	}
	slices.Sort(out)
	return out
}

var c0 = time.Date(2026, 11, 7, 3, 25, 0, 0, time.UTC)

// queue is the queue of calls of Organization 1 over a fake, on a manual business clock at c0.
func queue() (*Calls, *fakeCalls, *clock.Manual) {
	f := newFakeCalls()
	for id, title := range map[int64]string{1: "Disk1", 2: "Disk2", 3: "Disk3"} {
		f.group(id, title, "warning")
	}
	f.group(4, "Down", "critical")
	business := clock.NewManual(c0)
	c := NewCalls(1, business, logging.New(io.Discard, logging.LevelInfo))
	c.queries = func(DBTX) CallQueries { return f }
	return c, f, business
}

// page queues the Page of the Alert Group group to the User user through the gateway gateway.
func page(t *testing.T, c *Calls, user, gateway, group int64) {
	t.Helper()
	log, err := c.Page(t.Context(), nil, PageCall{UserID: user, GatewayID: gateway, AlertGroupID: group,
		Stop: StopNotFiring})
	if err != nil {
		t.Fatal(err)
	}
	log(t.Context())
}

// TestPageQueue covers the queue of C-30.FR-4 and AC-3 with a manual clock: the first call to a person through a
// gateway is due at once; later Pages join the one waiting call, which has no due time while the previous call is
// being sent or retried and is due gateway.combine_window after it ended, accepted or Not delivered, and at once when
// it ended longer ago; a critical Alert Group makes the call critical; a call is per person and gateway, so a Page of
// another person or through another gateway never joins it — Mentions are Thread replies of delivery and never reach
// this queue at all; a Page takes the person's lock first and wakes the senders.
func TestPageQueue(t *testing.T) {
	c, f, business := queue()
	page(t, c, 1, 10, 1)
	first := f.calls[0]
	if first.state != CallWaiting || !first.notBefore.Time.Equal(c0) || !first.nextAttempt.Time.Equal(c0) ||
		first.critical || !strings.HasPrefix(first.publicID, "CA") || len(first.publicID) != 14 {
		t.Fatalf("the first call %+v", first)
	}
	if !slices.Equal(f.locks, []string{"10/1"}) || f.notified != 1 {
		t.Errorf("locks %v, notified %d", f.locks, f.notified)
	}
	// The first call goes: it is being sent.
	f.calls[0].state, f.calls[0].firstAttempt = CallSending, pgtype.Timestamptz{Time: c0, Valid: true}
	business.Advance(10 * time.Second)
	page(t, c, 1, 10, 2)
	page(t, c, 1, 10, 3)
	page(t, c, 1, 10, 3)
	if len(f.calls) != 2 {
		t.Fatalf("%d calls", len(f.calls))
	}
	second := f.calls[1]
	if second.state != CallWaiting || second.notBefore.Valid || second.nextAttempt.Valid ||
		!slices.Equal(f.itemsOf(second.id), []string{"+Disk2", "+Disk3"}) {
		t.Errorf("the waiting call %+v, %v", second, f.itemsOf(second.id))
	}
	// A critical Alert Group makes it critical.
	page(t, c, 1, 10, 4)
	if !f.calls[1].critical {
		t.Error("the call with a critical Alert Group is not critical")
	}
	// Another person and another gateway have calls of their own, due at once.
	page(t, c, 2, 10, 2)
	page(t, c, 1, 11, 2)
	if len(f.calls) != 4 || !f.calls[2].nextAttempt.Valid || !f.calls[3].nextAttempt.Valid ||
		f.calls[2].user != 2 || f.calls[3].conn != 11 {
		t.Errorf("calls %+v", f.calls)
	}

	// The window after a call that ended: accepted 30 s ago, the next is due 30 s from now.
	t.Run("after an ended call", func(t *testing.T) {
		for _, state := range []string{CallAccepted, CallNotDelivered} {
			c, f, business := queue()
			page(t, c, 1, 10, 1)
			f.calls[0].state, f.calls[0].firstAttempt = state, pgtype.Timestamptz{Time: c0, Valid: true}
			f.calls[0].ended = pgtype.Timestamptz{Time: c0, Valid: true}
			business.Advance(30 * time.Second)
			page(t, c, 1, 10, 2)
			if got := f.calls[1].notBefore; !got.Time.Equal(c0.Add(CombineWindow)) ||
				!f.calls[1].nextAttempt.Time.Equal(got.Time) {
				t.Errorf("%s: due at %v", state, got)
			}
			// Ended longer ago than the window: at once.
			f.calls = f.calls[:1]
			business.Advance(CombineWindow)
			page(t, c, 1, 10, 3)
			if got := f.calls[1].notBefore; !got.Time.Equal(business.Now()) {
				t.Errorf("%s, long ago: due at %v", state, got)
			}
		}
	})
	// A call dropped before its first attempt never went: it starts no window.
	t.Run("after a dropped call", func(t *testing.T) {
		c, f, _ := queue()
		page(t, c, 1, 10, 1)
		f.calls[0].state, f.calls[0].ended = CallDropped, pgtype.Timestamptz{Time: c0, Valid: true}
		page(t, c, 1, 10, 2)
		if !f.calls[1].notBefore.Time.Equal(c0) {
			t.Errorf("due at %v", f.calls[1].notBefore)
		}
	})
}

// TestPageRefused: a Page with an unknown stop check, of an Alert Group, a User or a gateway that is not there — a
// Bridge Connection for messages only, a deleted User — queues nothing and is ErrNoPage; a failed query fails it.
func TestPageRefused(t *testing.T) {
	c, f, _ := queue()
	if _, err := c.Page(t.Context(), nil, PageCall{UserID: 1, GatewayID: 10, AlertGroupID: 1,
		Stop: "chain_started_over"}); !errors.Is(err, ErrUnknownStop) {
		t.Errorf("an unknown stop: %v", err)
	}
	for _, p := range []PageCall{{UserID: 1, GatewayID: 10, AlertGroupID: 99}, {UserID: 1, GatewayID: 12,
		AlertGroupID: 1}, {UserID: 3, GatewayID: 10, AlertGroupID: 1}} {
		p.Stop = StopNotFiring
		if _, err := c.Page(t.Context(), nil, p); !errors.Is(err, ErrNoPage) {
			t.Errorf("%+v: %v", p, err)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("queued %+v", f.calls)
	}
	boom := errors.New("boom")
	for _, q := range []string{"GetPage", "LockCallPerson", "LockWaitingCall", "PreviousCall", "InsertWaitingCall",
		"AddCallItem", "NotifyCalls"} {
		c, f, _ := queue()
		f.fail[q] = boom
		if _, err := c.Page(t.Context(), nil, PageCall{UserID: 1, GatewayID: 10, AlertGroupID: 1,
			Stop: StopNotFiring}); !errors.Is(err, boom) {
			t.Errorf("%s: %v", q, err)
		}
	}
	c, f, _ = queue()
	page(t, c, 1, 10, 1)
	f.fail["MarkCallCritical"] = boom
	if _, err := c.Page(t.Context(), nil, PageCall{UserID: 1, GatewayID: 10, AlertGroupID: 4,
		Stop: StopNotFiring}); !errors.Is(err, boom) {
		t.Errorf("MarkCallCritical: %v", err)
	}
}

// TestDevPage is the hook of muster dev: the Pages of several Alert Groups by public_id to one User through one
// gateway in one transaction, one call; an id that names nothing is ErrNoPage and queues nothing.
func TestDevPage(t *testing.T) {
	c, f, _ := queue()
	g1, g2 := f.groups[1].publicID, f.groups[2].publicID
	if err := c.DevPage(t.Context(), f, "SR0000000000P1", "CN00000000GW10", []string{g1, g2, g2}); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || !slices.Equal(f.itemsOf(f.calls[0].id), []string{"+Disk1", "+Disk2"}) {
		t.Errorf("calls %+v %v", f.calls, f.itemsOf(1))
	}
	for _, args := range [][]string{{"SR0000000000XX", "CN00000000GW10", g1}, {"SR0000000000P3", "CN00000000GW10", g1},
		{"SR0000000000P1", "CN00000000BR12", g1}, {"SR0000000000P1", "CN00000000GW10", "AG00000000000X"},
		{"SR0000000000P1", "CN00000000GW10"}} {
		c, f, _ := queue()
		if err := c.DevPage(t.Context(), f, args[0], args[1], args[2:]); !errors.Is(err, ErrNoPage) {
			t.Errorf("%v: %v", args, err)
		}
		if len(f.calls) != 0 {
			t.Errorf("%v queued %+v", args, f.calls)
		}
	}
	boom := errors.New("boom")
	c, f, _ = queue()
	f.fail["GetUser"] = boom
	if err := c.DevPage(t.Context(), f, "SR0000000000P1", "CN00000000GW10", []string{g1}); !errors.Is(err, boom) ||
		errors.Is(err, ErrNoPage) {
		t.Errorf("a failed read: %v", err)
	}
	c, f, _ = queue()
	f.fail["AddCallItem"] = boom
	if err := c.DevPage(t.Context(), f, "SR0000000000P1", "CN00000000GW10", []string{g1}); !errors.Is(err, boom) {
		t.Errorf("a failed page: %v", err)
	}
}

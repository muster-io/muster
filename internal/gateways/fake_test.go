// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package gateways

import (
	"bytes"
	"context"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	adb "github.com/muster-io/muster/internal/audit/dbgen"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/gateways/dbgen"
)

// fakeStore is the contacts and codes of one Organization in memory, with the semantics of query.sql; a transaction
// that fails is rolled back. fail makes the named query fail.
type fakeStore struct {
	mu       sync.Mutex
	users    map[int64]fakeUser
	gateways map[int64]fakeGateway
	contacts []fakeContact
	codes    []fakeCode
	ended    map[int64]bool
	audits   []adb.InsertAuditEntryParams
	fail     map[string]error
	nextID   int64
	locks    int
}

type fakeUser struct {
	publicID string
	language pgtype.Text
	status   string
}

type fakeGateway struct {
	publicID string
	name     string
	gateway  bool
}

type fakeContact struct {
	id, userID, connID int64
	publicID, contact  string
	confirmedAt        pgtype.Timestamptz
	createdAt          time.Time
}

type fakeCode struct {
	id, userID, sessionID int64
	contactID             pgtype.Int8
	contactHash, codeHash []byte
	attempts              int64
	createdAt, expiresAt  time.Time
	sentAt, usedAt        pgtype.Timestamptz
	voidedAt              pgtype.Timestamptz
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		users: map[int64]fakeUser{1: {publicID: "SR0000000000P1", status: "active"},
			2: {publicID: "SR0000000000P2", language: pgtype.Text{String: "ru", Valid: true}, status: "active"}},
		gateways: map[int64]fakeGateway{10: {publicID: "CN00000000GW10", name: "Gateway", gateway: true},
			11: {publicID: "CN00000000BR11", name: "Bridge"}},
		ended: map[int64]bool{}, fail: map[string]error{}, nextID: 100,
	}
}

func (f *fakeStore) err(name string) error {
	return f.fail[name]
}

func (f *fakeStore) InTx(_ context.Context, fn func(Queries) error) error {
	f.mu.Lock()
	contacts, codes, audits := slices.Clone(f.contacts), slices.Clone(f.codes), slices.Clone(f.audits)
	f.mu.Unlock()
	if err := fn(f); err != nil {
		f.mu.Lock()
		f.contacts, f.codes, f.audits = contacts, codes, audits
		f.mu.Unlock()
		return err
	}
	return nil
}

func (f *fakeStore) id() int64 {
	f.nextID++
	return f.nextID
}

func (f *fakeStore) ListGateways(context.Context, int64) ([]dbgen.ListGatewaysRow, error) {
	if err := f.err("ListGateways"); err != nil {
		return nil, err
	}
	var out []dbgen.ListGatewaysRow
	for _, id := range []int64{10, 11} {
		if g := f.gateways[id]; g.gateway {
			out = append(out, dbgen.ListGatewaysRow{ID: id, PublicID: g.publicID, Name: g.name})
		}
	}
	return out, nil
}

func (f *fakeStore) GetGateway(_ context.Context, a dbgen.GetGatewayParams) (dbgen.GetGatewayRow, error) {
	if err := f.err("GetGateway"); err != nil {
		return dbgen.GetGatewayRow{}, err
	}
	for id, g := range f.gateways {
		if g.publicID == a.PublicID && g.gateway {
			return dbgen.GetGatewayRow{ID: id, PublicID: g.publicID, Name: g.name}, nil
		}
	}
	return dbgen.GetGatewayRow{}, pgx.ErrNoRows
}

func (f *fakeStore) ConfirmedContact(_ context.Context, a dbgen.ConfirmedContactParams) (string, error) {
	if err := f.err("ConfirmedContact"); err != nil {
		return "", err
	}
	for _, c := range f.contacts {
		if c.userID == a.UserID && c.connID == a.ConnectionID && c.confirmedAt.Valid && f.gateways[c.connID].gateway {
			return c.contact, nil
		}
	}
	return "", pgx.ErrNoRows
}

func (f *fakeStore) LockContactUser(_ context.Context, a dbgen.LockContactUserParams) (dbgen.LockContactUserRow,
	error) {
	if err := f.err("LockContactUser"); err != nil {
		return dbgen.LockContactUserRow{}, err
	}
	u, ok := f.users[a.ID]
	if !ok {
		return dbgen.LockContactUserRow{}, pgx.ErrNoRows
	}
	return dbgen.LockContactUserRow{ID: a.ID, PublicID: u.publicID, Language: u.language, Status: u.status}, nil
}

func (f *fakeStore) GetUserByPublicID(_ context.Context, a dbgen.GetUserByPublicIDParams) (
	dbgen.GetUserByPublicIDRow, error) {
	if err := f.err("GetUserByPublicID"); err != nil {
		return dbgen.GetUserByPublicIDRow{}, err
	}
	for id, u := range f.users {
		if u.publicID == a.PublicID {
			return dbgen.GetUserByPublicIDRow{ID: id, PublicID: u.publicID, Status: u.status}, nil
		}
	}
	return dbgen.GetUserByPublicIDRow{}, pgx.ErrNoRows
}

func (f *fakeStore) LockContactText(context.Context, dbgen.LockContactTextParams) error {
	if err := f.err("LockContactText"); err != nil {
		return err
	}
	f.locks++
	return nil
}

func (f *fakeStore) count(match func(fakeCode) bool, since time.Time) (int64, time.Time) {
	var n int64
	oldest := since
	for _, k := range f.codes {
		if match(k) && k.createdAt.After(since) {
			if n == 0 || k.createdAt.Before(oldest) {
				oldest = k.createdAt
			}
			n++
		}
	}
	return n, oldest
}

func (f *fakeStore) CountUserCodes(_ context.Context, a dbgen.CountUserCodesParams) (dbgen.CountUserCodesRow, error) {
	if err := f.err("CountUserCodes"); err != nil {
		return dbgen.CountUserCodesRow{}, err
	}
	n, oldest := f.count(func(k fakeCode) bool { return k.userID == a.UserID }, a.Since)
	return dbgen.CountUserCodesRow{Requests: n, Oldest: oldest}, nil
}

func (f *fakeStore) CountContactCodes(_ context.Context, a dbgen.CountContactCodesParams) (
	dbgen.CountContactCodesRow, error) {
	if err := f.err("CountContactCodes"); err != nil {
		return dbgen.CountContactCodesRow{}, err
	}
	n, oldest := f.count(func(k fakeCode) bool { return bytes.Equal(k.contactHash, a.ContactHash) }, a.Since)
	return dbgen.CountContactCodesRow{Requests: n, Oldest: oldest}, nil
}

// replaces is the confirmed contact c replaces once confirmed.
func (f *fakeStore) replaces(c fakeContact) pgtype.Text {
	if c.confirmedAt.Valid {
		return pgtype.Text{}
	}
	for _, r := range f.contacts {
		if r.userID == c.userID && r.connID == c.connID && r.confirmedAt.Valid {
			return pgtype.Text{String: r.publicID, Valid: true}
		}
	}
	return pgtype.Text{}
}

func (f *fakeStore) ListContactsOfUser(_ context.Context, a dbgen.ListContactsOfUserParams) (
	[]dbgen.ListContactsOfUserRow, error) {
	if err := f.err("ListContactsOfUser"); err != nil {
		return nil, err
	}
	out := []dbgen.ListContactsOfUserRow{}
	for _, c := range f.contacts {
		if c.userID == a.UserID {
			g := f.gateways[c.connID]
			out = append(out, dbgen.ListContactsOfUserRow{ID: c.id, PublicID: c.publicID, Contact: c.contact,
				ConfirmedAt: c.confirmedAt, CreatedAt: c.createdAt, ConnectionPublicID: g.publicID,
				ConnectionName: g.name, Replaces: f.replaces(c)})
		}
	}
	return out, nil
}

func (f *fakeStore) LockContactOfUser(_ context.Context, a dbgen.LockContactOfUserParams) (
	dbgen.LockContactOfUserRow, error) {
	if err := f.err("LockContactOfUser"); err != nil {
		return dbgen.LockContactOfUserRow{}, err
	}
	for _, c := range f.contacts {
		if c.userID == a.UserID && c.publicID == a.PublicID {
			g := f.gateways[c.connID]
			return dbgen.LockContactOfUserRow{ID: c.id, PublicID: c.publicID, ConnectionID: c.connID,
				Contact: c.contact, ConfirmedAt: c.confirmedAt, CreatedAt: c.createdAt,
				ConnectionPublicID: g.publicID, ConnectionName: g.name, Gateway: g.gateway, Replaces: f.replaces(c)}, nil
		}
	}
	return dbgen.LockContactOfUserRow{}, pgx.ErrNoRows
}

func (f *fakeStore) InsertContact(_ context.Context, a dbgen.InsertContactParams) (int64, error) {
	if err := f.err("InsertContact"); err != nil {
		return 0, err
	}
	id := f.id()
	f.contacts = append(f.contacts, fakeContact{id: id, userID: a.UserID, connID: a.ConnectionID,
		publicID: a.PublicID, contact: a.Contact, createdAt: a.CreatedAt})
	return id, nil
}

// remove deletes the contacts that match, setting the contact of their codes to null, and returns them.
func (f *fakeStore) remove(match func(fakeContact) bool) []fakeContact {
	var gone []fakeContact
	f.contacts = slices.DeleteFunc(f.contacts, func(c fakeContact) bool {
		if match(c) {
			gone = append(gone, c)
			return true
		}
		return false
	})
	for _, c := range gone {
		for i := range f.codes {
			if f.codes[i].contactID.Int64 == c.id {
				f.codes[i].contactID = pgtype.Int8{}
			}
		}
	}
	return gone
}

func (f *fakeStore) DeleteContact(_ context.Context, a dbgen.DeleteContactParams) (int64, error) {
	if err := f.err("DeleteContact"); err != nil {
		return 0, err
	}
	return int64(len(f.remove(func(c fakeContact) bool { return c.id == a.ID }))), nil
}

func (f *fakeStore) DeleteUnconfirmedContacts(_ context.Context, a dbgen.DeleteUnconfirmedContactsParams) (
	[]dbgen.DeleteUnconfirmedContactsRow, error) {
	if err := f.err("DeleteUnconfirmedContacts"); err != nil {
		return nil, err
	}
	var out []dbgen.DeleteUnconfirmedContactsRow
	for _, c := range f.remove(func(c fakeContact) bool {
		return c.userID == a.UserID && c.connID == a.ConnectionID && !c.confirmedAt.Valid
	}) {
		out = append(out, dbgen.DeleteUnconfirmedContactsRow{PublicID: c.publicID, Contact: c.contact})
	}
	return out, nil
}

func (f *fakeStore) DeleteReplacedContacts(_ context.Context, a dbgen.DeleteReplacedContactsParams) (
	[]dbgen.DeleteReplacedContactsRow, error) {
	if err := f.err("DeleteReplacedContacts"); err != nil {
		return nil, err
	}
	var out []dbgen.DeleteReplacedContactsRow
	for _, c := range f.remove(func(c fakeContact) bool {
		return c.userID == a.UserID && c.connID == a.ConnectionID && c.confirmedAt.Valid && c.id != a.Keep
	}) {
		out = append(out, dbgen.DeleteReplacedContactsRow{PublicID: c.publicID, Contact: c.contact})
	}
	return out, nil
}

func (f *fakeStore) ConfirmContact(_ context.Context, a dbgen.ConfirmContactParams) error {
	if err := f.err("ConfirmContact"); err != nil {
		return err
	}
	for i := range f.contacts {
		if f.contacts[i].id == a.ID {
			f.contacts[i].confirmedAt = pgtype.Timestamptz{Time: a.Now, Valid: true}
		}
	}
	return nil
}

func (f *fakeStore) DeleteUserContacts(_ context.Context, a dbgen.DeleteUserContactsParams) (
	[]dbgen.DeleteUserContactsRow, error) {
	if err := f.err("DeleteUserContacts"); err != nil {
		return nil, err
	}
	var out []dbgen.DeleteUserContactsRow
	for _, c := range f.remove(func(c fakeContact) bool { return c.userID == a.UserID }) {
		g := f.gateways[c.connID]
		out = append(out, dbgen.DeleteUserContactsRow{PublicID: c.publicID, Contact: c.contact,
			Confirmed: c.confirmedAt.Valid, ConnectionPublicID: g.publicID, ConnectionName: g.name})
	}
	return out, nil
}

func (f *fakeStore) DeleteUserCodes(_ context.Context, a dbgen.DeleteUserCodesParams) (int64, error) {
	if err := f.err("DeleteUserCodes"); err != nil {
		return 0, err
	}
	n := len(f.codes)
	f.codes = slices.DeleteFunc(f.codes, func(k fakeCode) bool { return k.userID == a.UserID })
	return int64(n - len(f.codes)), nil
}

func (f *fakeStore) MarkCodeSent(_ context.Context, a dbgen.MarkCodeSentParams) error {
	if err := f.err("MarkCodeSent"); err != nil {
		return err
	}
	f.code(a.ID).sentAt = pgtype.Timestamptz{Time: a.Now, Valid: true}
	return nil
}

func (f *fakeStore) VoidEarlierCodes(_ context.Context, a dbgen.VoidEarlierCodesParams) (int64, error) {
	if err := f.err("VoidEarlierCodes"); err != nil {
		return 0, err
	}
	var n int64
	for i := range f.codes {
		k := &f.codes[i]
		if k.contactID == a.ContactID && k.id < a.Keep && !k.usedAt.Valid && !k.voidedAt.Valid {
			k.voidedAt = pgtype.Timestamptz{Time: a.Now, Valid: true}
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) InsertCode(_ context.Context, a dbgen.InsertCodeParams) (int64, error) {
	if err := f.err("InsertCode"); err != nil {
		return 0, err
	}
	id := f.id()
	f.codes = append(f.codes, fakeCode{id: id, userID: a.UserID, sessionID: a.SessionID, contactID: a.ContactID,
		contactHash: a.ContactHash, codeHash: a.CodeHash, createdAt: a.CreatedAt, expiresAt: a.ExpiresAt})
	return id, nil
}

func (f *fakeStore) code(id int64) *fakeCode {
	for i := range f.codes {
		if f.codes[i].id == id {
			return &f.codes[i]
		}
	}
	return &fakeCode{}
}

func (f *fakeStore) VoidCode(_ context.Context, a dbgen.VoidCodeParams) error {
	if err := f.err("VoidCode"); err != nil {
		return err
	}
	if k := f.code(a.ID); !k.usedAt.Valid && !k.voidedAt.Valid {
		k.voidedAt = pgtype.Timestamptz{Time: a.Now, Valid: true}
	}
	return nil
}

func (f *fakeStore) DeleteCode(_ context.Context, a dbgen.DeleteCodeParams) error {
	if err := f.err("DeleteCode"); err != nil {
		return err
	}
	f.codes = slices.DeleteFunc(f.codes, func(k fakeCode) bool { return k.id == a.ID })
	return nil
}

func (f *fakeStore) LockNewestCode(_ context.Context, a dbgen.LockNewestCodeParams) (dbgen.LockNewestCodeRow,
	error) {
	if err := f.err("LockNewestCode"); err != nil {
		return dbgen.LockNewestCodeRow{}, err
	}
	var newest *fakeCode
	for i := range f.codes {
		k := &f.codes[i]
		if k.contactID == a.ContactID && k.sentAt.Valid && (newest == nil || !k.createdAt.Before(newest.createdAt)) {
			newest = k
		}
	}
	if newest == nil {
		return dbgen.LockNewestCodeRow{}, pgx.ErrNoRows
	}
	return dbgen.LockNewestCodeRow{ID: newest.id, SessionID: newest.sessionID, CodeHash: newest.codeHash,
		Attempts: newest.attempts, ExpiresAt: newest.expiresAt, UsedAt: newest.usedAt, VoidedAt: newest.voidedAt,
		SessionEnded: f.ended[newest.sessionID]}, nil
}

func (f *fakeStore) RecordWrongCode(_ context.Context, a dbgen.RecordWrongCodeParams) (int64, error) {
	if err := f.err("RecordWrongCode"); err != nil {
		return 0, err
	}
	k := f.code(a.ID)
	k.attempts++
	if k.attempts >= a.MaxAttempts {
		k.voidedAt = pgtype.Timestamptz{Time: a.Now, Valid: true}
	}
	return k.attempts, nil
}

func (f *fakeStore) UseCode(_ context.Context, a dbgen.UseCodeParams) (int64, error) {
	if err := f.err("UseCode"); err != nil {
		return 0, err
	}
	k := f.code(a.ID)
	if k.usedAt.Valid || k.voidedAt.Valid {
		return 0, nil
	}
	k.usedAt = pgtype.Timestamptz{Time: a.Now, Valid: true}
	return 1, nil
}

func (f *fakeStore) InsertAuditEntry(_ context.Context, a adb.InsertAuditEntryParams) error {
	if err := f.err("InsertAuditEntry"); err != nil {
		return err
	}
	f.audits = append(f.audits, a)
	return nil
}

// fakeGatewaySender is the Notification gateway: it keeps what it was sent and answers outcome.
type fakeGatewaySender struct {
	mu      sync.Mutex
	sent    []delivery.Notification
	keys    []string
	outcome delivery.Outcome
	// during runs while the notification is being sent.
	during func()
}

func (g *fakeGatewaySender) Notify(_ context.Context, c delivery.Call, n delivery.Notification) delivery.Outcome {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sent = append(g.sent, n)
	g.keys = append(g.keys, c.Key)
	if g.during != nil {
		g.during()
	}
	if g.outcome.Kind == "" {
		return delivery.Outcome{Kind: delivery.OutcomeOK, MessageID: "n1"}
	}
	return g.outcome
}

func (g *fakeGatewaySender) last() delivery.Notification {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.sent) == 0 {
		return delivery.Notification{}
	}
	return g.sent[len(g.sent)-1]
}

// fakePath is an interactive path that fails with err without calling anything.
type fakePath struct{ err error }

func (p fakePath) Do(context.Context, delivery.Subject, delivery.Op) (delivery.Outcome, error) {
	return delivery.Outcome{}, p.err
}

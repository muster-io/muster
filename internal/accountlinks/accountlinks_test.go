// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package accountlinks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/accountlinks/dbgen"
	"github.com/muster-io/muster/internal/audit"
	adb "github.com/muster-io/muster/internal/audit/dbgen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/logging"
)

const org = 3

var (
	t0      = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	errBoom = errors.New("boom")
)

// The fixtures: Users, a Telegram and a Mattermost Connection, a deleted one and a second Telegram one.
const (
	aliceID, bobID, carolID, daveID     = 1, 2, 3, 4
	alicePID, bobPID, carolPID, davePID = "SRAAAAAAAAAAA1", "SRAAAAAAAAAAA2", "SRAAAAAAAAAAA3", "SRAAAAAAAAAAA4"
	tgID, mmID, goneID, tg2ID           = 11, 12, 13, 14
	tgPID, mmPID, gonePID, tg2PID       = "CNAAAAAAAAAAA1", "CNAAAAAAAAAAA2", "CNAAAAAAAAAAA3", "CNAAAAAAAAAAA4"
)

type fUser struct {
	id                                  int64
	publicID, login, name, role, status string
	language                            string
}

type fConn struct {
	id                     int64
	publicID, typ, name    string
	botUserID, botUsername string
	deleted                bool
}

type fLink struct {
	id        int64
	publicID  string
	userID    int64
	messenger string
	connID    int64
	external  string
	username  *string
	createdAt time.Time
	lastUsed  *time.Time
}

func (l fLink) space() string {
	if l.messenger == MessengerTelegram {
		return SpaceTelegram
	}
	return SpaceMattermost(l.connID)
}

type fReq struct {
	id                     int64
	publicID               string
	userID                 int64
	messenger              string
	connID                 int64
	hash                   []byte
	target, targetUsername pgtype.Text
	attempts               int64
	created, expires       time.Time
	consumed, voided       *time.Time
}

// fakeStore is the database of the package in memory; InTx rolls back what f changed when it fails, and fail makes
// the named query fail.
type fakeStore struct {
	users []fUser
	conns []fConn
	links []fLink
	reqs  []fReq
	audit []adb.InsertAuditEntryParams
	hints []db.Hint
	// counted are the request counts and the locks of code targets, in order, each marked whether it ran in a
	// transaction.
	counted    []string
	fail       map[string]error
	next       int64
	inTx       bool
	savepoints int
}

func newStore() *fakeStore {
	s := &fakeStore{fail: map[string]error{}, next: 100}
	s.users = []fUser{
		{aliceID, alicePID, "alice", "Alice Smith", "responder", StatusActive, ""},
		{bobID, bobPID, "bob", "Bob", "responder", StatusActive, "ru"},
		{carolID, carolPID, "carol", "Carol", "viewer", StatusDisabled, ""},
		{daveID, davePID, "deleted-user-" + davePID, "deleted-user-" + davePID, "responder", statusDeleted, ""},
	}
	s.conns = []fConn{
		{tgID, tgPID, MessengerTelegram, "Dev Telegram", "", "muster_dev_bot", false},
		{mmID, mmPID, MessengerMattermost, "mm", "bot-id", "", false},
		{goneID, gonePID, MessengerTelegram, "gone", "", "gone_bot", true},
		{tg2ID, tg2PID, MessengerTelegram, "Second", "", "", false},
	}
	return s
}

func (s *fakeStore) err(name string) error { return s.fail[name] }

func (s *fakeStore) id() int64 { s.next++; return s.next }

func (s *fakeStore) InTx(_ context.Context, f func(Queries) error) error {
	s.inTx = true
	err := s.undoOnError(f)
	s.inTx = false
	return err
}

// Savepoint rolls back what f changed when it fails, as a savepoint does; savepoints counts the calls.
func (s *fakeStore) Savepoint(_ context.Context, f func(Queries) error) error {
	s.savepoints++
	return s.undoOnError(f)
}

func (s *fakeStore) undoOnError(f func(Queries) error) error {
	saved := *s
	saved.links, saved.reqs = slices.Clone(s.links), slices.Clone(s.reqs)
	saved.audit, saved.hints = slices.Clone(s.audit), slices.Clone(s.hints)
	err := f(s)
	if err != nil {
		fail, next, inTx, savepoints := s.fail, s.next, s.inTx, s.savepoints
		*s = saved
		s.fail, s.next, s.inTx, s.savepoints = fail, next, inTx, savepoints
	}
	return err
}

func (s *fakeStore) user(id int64) (fUser, bool) {
	i := slices.IndexFunc(s.users, func(u fUser) bool { return u.id == id })
	if i < 0 {
		return fUser{}, false
	}
	return s.users[i], true
}

func (s *fakeStore) conn(id int64) fConn {
	return s.conns[slices.IndexFunc(s.conns, func(c fConn) bool { return c.id == id })]
}

func (s *fakeStore) LookupAccountLink(_ context.Context, a dbgen.LookupAccountLinkParams) (
	dbgen.LookupAccountLinkRow, error) {
	if err := s.err("LookupAccountLink"); err != nil {
		return dbgen.LookupAccountLinkRow{}, err
	}
	for _, l := range s.links {
		u, _ := s.user(l.userID)
		if a.OrgID == org && l.space() == a.IdentitySpace && l.external == a.ExternalID && u.status != statusDeleted {
			r := dbgen.LookupAccountLinkRow{ID: u.id, PublicID: u.publicID, Login: u.login, Name: u.name, Role: u.role,
				Status: u.status, LinkID: l.id}
			if l.lastUsed != nil {
				r.LastUsedAt = pgtype.Timestamptz{Time: *l.lastUsed, Valid: true}
			}
			return r, nil
		}
	}
	return dbgen.LookupAccountLinkRow{}, pgx.ErrNoRows
}

func (s *fakeStore) TouchAccountLink(_ context.Context, a dbgen.TouchAccountLinkParams) error {
	if err := s.err("TouchAccountLink"); err != nil {
		return err
	}
	for i, l := range s.links {
		if a.OrgID == org && l.id == a.ID && (l.lastUsed == nil || l.lastUsed.Before(a.StaleBefore)) {
			now := a.Now
			s.links[i].lastUsed = &now
		}
	}
	return nil
}

func (s *fakeStore) GetLinkConnection(_ context.Context, a dbgen.GetLinkConnectionParams) (
	dbgen.GetLinkConnectionRow, error) {
	if err := s.err("GetLinkConnection"); err != nil {
		return dbgen.GetLinkConnectionRow{}, err
	}
	for _, c := range s.conns {
		if a.OrgID == org && c.publicID == a.PublicID && !c.deleted {
			return dbgen.GetLinkConnectionRow{ID: c.id, PublicID: c.publicID, Type: c.typ, Name: c.name,
				BotUserID: text(c.botUserID), BotUsername: text(c.botUsername)}, nil
		}
	}
	return dbgen.GetLinkConnectionRow{}, pgx.ErrNoRows
}

func (s *fakeStore) ListLinkableConnections(_ context.Context, orgID int64) ([]dbgen.ListLinkableConnectionsRow,
	error) {
	if err := s.err("ListLinkableConnections"); err != nil {
		return nil, err
	}
	out := []dbgen.ListLinkableConnectionsRow{}
	for _, c := range s.conns {
		if orgID == org && !c.deleted {
			out = append(out, dbgen.ListLinkableConnectionsRow{PublicID: c.publicID, Name: c.name, Type: c.typ})
		}
	}
	slices.SortFunc(out, func(a, b dbgen.ListLinkableConnectionsRow) int {
		return strings.Compare(a.Type+strings.ToLower(a.Name), b.Type+strings.ToLower(b.Name))
	})
	return out, nil
}

func (s *fakeStore) LockLinkUser(_ context.Context, a dbgen.LockLinkUserParams) (dbgen.LockLinkUserRow, error) {
	if err := s.err("LockLinkUser"); err != nil {
		return dbgen.LockLinkUserRow{}, err
	}
	u, ok := s.user(a.ID)
	if !ok || a.OrgID != org {
		return dbgen.LockLinkUserRow{}, pgx.ErrNoRows
	}
	return dbgen.LockLinkUserRow{ID: u.id, PublicID: u.publicID, Login: u.login, Name: u.name,
		Language: text(u.language), Status: u.status}, nil
}

func (s *fakeStore) GetUserByPublicID(_ context.Context, a dbgen.GetUserByPublicIDParams) (
	dbgen.GetUserByPublicIDRow, error) {
	if err := s.err("GetUserByPublicID"); err != nil {
		return dbgen.GetUserByPublicIDRow{}, err
	}
	for _, u := range s.users {
		if a.OrgID == org && u.publicID == a.PublicID {
			return dbgen.GetUserByPublicIDRow{ID: u.id, PublicID: u.publicID, Status: u.status}, nil
		}
	}
	return dbgen.GetUserByPublicIDRow{}, pgx.ErrNoRows
}

func (s *fakeStore) ListAccountLinksOfUser(_ context.Context, a dbgen.ListAccountLinksOfUserParams) (
	[]dbgen.ListAccountLinksOfUserRow, error) {
	if err := s.err("ListAccountLinksOfUser"); err != nil {
		return nil, err
	}
	out := []dbgen.ListAccountLinksOfUserRow{}
	for _, l := range s.links {
		if a.OrgID == org && l.userID == a.UserID {
			c := s.conn(l.connID)
			r := dbgen.ListAccountLinksOfUserRow{PublicID: l.publicID, Messenger: l.messenger, ExternalID: l.external,
				Username: ptrText(l.username), CreatedAt: l.createdAt, ConnectionPublicID: c.publicID,
				ConnectionName: c.name}
			if l.lastUsed != nil {
				r.LastUsedAt = pgtype.Timestamptz{Time: *l.lastUsed, Valid: true}
			}
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *fakeStore) GetAccountLinkOfUser(_ context.Context, a dbgen.GetAccountLinkOfUserParams) (
	dbgen.GetAccountLinkOfUserRow, error) {
	if err := s.err("GetAccountLinkOfUser"); err != nil {
		return dbgen.GetAccountLinkOfUserRow{}, err
	}
	for _, l := range s.links {
		if a.OrgID == org && l.userID == a.UserID && l.publicID == a.PublicID {
			c := s.conn(l.connID)
			return dbgen.GetAccountLinkOfUserRow{ID: l.id, PublicID: l.publicID, Messenger: l.messenger,
				ExternalID: l.external, Username: ptrText(l.username), ConnectionPublicID: c.publicID,
				ConnectionName: c.name}, nil
		}
	}
	return dbgen.GetAccountLinkOfUserRow{}, pgx.ErrNoRows
}

func (s *fakeStore) GetAccountLinkBySpace(_ context.Context, a dbgen.GetAccountLinkBySpaceParams) (
	dbgen.GetAccountLinkBySpaceRow, error) {
	if err := s.err("GetAccountLinkBySpace"); err != nil {
		return dbgen.GetAccountLinkBySpaceRow{}, err
	}
	for _, l := range s.links {
		if a.OrgID == org && l.space() == a.IdentitySpace && l.external == a.ExternalID {
			return dbgen.GetAccountLinkBySpaceRow{ID: l.id, PublicID: l.publicID, UserID: l.userID}, nil
		}
	}
	return dbgen.GetAccountLinkBySpaceRow{}, pgx.ErrNoRows
}

func (s *fakeStore) GetUserLinkInSpace(_ context.Context, a dbgen.GetUserLinkInSpaceParams) (
	dbgen.GetUserLinkInSpaceRow, error) {
	if err := s.err("GetUserLinkInSpace"); err != nil {
		return dbgen.GetUserLinkInSpaceRow{}, err
	}
	for _, l := range s.links {
		if a.OrgID == org && l.userID == a.UserID && l.space() == a.IdentitySpace {
			c := s.conn(l.connID)
			return dbgen.GetUserLinkInSpaceRow{ID: l.id, PublicID: l.publicID, Messenger: l.messenger,
				ExternalID: l.external, Username: ptrText(l.username), CreatedAt: l.createdAt,
				LastUsedAt: ts(l.lastUsed), ConnectionPublicID: c.publicID, ConnectionName: c.name}, nil
		}
	}
	return dbgen.GetUserLinkInSpaceRow{}, pgx.ErrNoRows
}

func (s *fakeStore) DeleteAccountLink(_ context.Context, a dbgen.DeleteAccountLinkParams) (int64, error) {
	if err := s.err("DeleteAccountLink"); err != nil {
		return 0, err
	}
	before := len(s.links)
	s.links = slices.DeleteFunc(s.links, func(l fLink) bool { return a.OrgID == org && l.id == a.ID })
	return int64(before - len(s.links)), nil
}

func (s *fakeStore) InsertAccountLink(_ context.Context, a dbgen.InsertAccountLinkParams) (int64, error) {
	if err := s.err("InsertAccountLink"); err != nil {
		return 0, err
	}
	l := fLink{id: s.id(), publicID: a.PublicID, userID: a.UserID, messenger: a.Messenger, connID: a.ConnectionID,
		external: a.ExternalID, username: textPtr(a.Username), createdAt: a.CreatedAt}
	if a.OrgID != org {
		return 0, errors.New("another organization")
	}
	for _, o := range s.links {
		switch {
		case o.space() == l.space() && o.external == l.external:
			return 0, &pgconn.PgError{Code: uniqueViolation, ConstraintName: accountKey}
		case o.userID == l.userID && o.space() == l.space():
			return 0, &pgconn.PgError{Code: uniqueViolation, ConstraintName: "account_links_user_space_key"}
		}
	}
	s.links = append(s.links, l)
	return l.id, nil
}

func (s *fakeStore) UpdateAccountLink(_ context.Context, a dbgen.UpdateAccountLinkParams) error {
	if err := s.err("UpdateAccountLink"); err != nil {
		return err
	}
	for i, l := range s.links {
		if a.OrgID == org && l.id == a.ID {
			s.links[i].username, s.links[i].connID = textPtr(a.Username), a.ConnectionID
		}
	}
	return nil
}

func (s *fakeStore) InsertLinkRequest(_ context.Context, a dbgen.InsertLinkRequestParams) (int64, error) {
	if err := s.err("InsertLinkRequest"); err != nil {
		return 0, err
	}
	r := fReq{id: s.id(), publicID: a.PublicID, userID: a.UserID, messenger: a.Messenger, connID: a.ConnectionID,
		hash: a.SecretHash, target: a.TargetExternalID, targetUsername: a.TargetUsername, created: a.CreatedAt,
		expires: a.ExpiresAt}
	s.reqs = append(s.reqs, r)
	return r.id, nil
}

func (s *fakeStore) req(id int64) *fReq {
	i := slices.IndexFunc(s.reqs, func(r fReq) bool { return r.id == id })
	if i < 0 {
		return nil
	}
	return &s.reqs[i]
}

func (s *fakeStore) GetTelegramRequestByHash(_ context.Context, a dbgen.GetTelegramRequestByHashParams) (
	dbgen.GetTelegramRequestByHashRow, error) {
	if err := s.err("GetTelegramRequestByHash"); err != nil {
		return dbgen.GetTelegramRequestByHashRow{}, err
	}
	for _, r := range s.reqs {
		if a.OrgID == org && r.messenger == MessengerTelegram && bytes.Equal(r.hash, a.SecretHash) {
			u, _ := s.user(r.userID)
			return dbgen.GetTelegramRequestByHashRow{ID: r.id, UserID: r.userID, ConnectionID: r.connID,
				ExpiresAt: r.expires, ConsumedAt: ts(r.consumed), VoidedAt: ts(r.voided), UserPublicID: u.publicID,
				ConnectionPublicID: s.conn(r.connID).publicID}, nil
		}
	}
	return dbgen.GetTelegramRequestByHashRow{}, pgx.ErrNoRows
}

func (s *fakeStore) GetLinkRequest(_ context.Context, a dbgen.GetLinkRequestParams) (dbgen.GetLinkRequestRow, error) {
	if err := s.err("GetLinkRequest"); err != nil {
		return dbgen.GetLinkRequestRow{}, err
	}
	for _, r := range s.reqs {
		if a.OrgID == org && r.publicID == a.PublicID {
			c := s.conn(r.connID)
			return dbgen.GetLinkRequestRow{ID: r.id, UserID: r.userID, Messenger: r.messenger, ConnectionID: r.connID,
				TargetExternalID: r.target, TargetUsername: r.targetUsername, ConnectionPublicID: c.publicID,
				ConnectionName: c.name}, nil
		}
	}
	return dbgen.GetLinkRequestRow{}, pgx.ErrNoRows
}

func (s *fakeStore) LockLinkRequest(_ context.Context, a dbgen.LockLinkRequestParams) (dbgen.LockLinkRequestRow,
	error) {
	if err := s.err("LockLinkRequest"); err != nil {
		return dbgen.LockLinkRequestRow{}, err
	}
	r := s.req(a.ID)
	if r == nil || a.OrgID != org {
		return dbgen.LockLinkRequestRow{}, pgx.ErrNoRows
	}
	return dbgen.LockLinkRequestRow{ID: r.id, SecretHash: r.hash, Attempts: r.attempts, ExpiresAt: r.expires,
		ConsumedAt: ts(r.consumed), VoidedAt: ts(r.voided)}, nil
}

func (s *fakeStore) ConsumeLinkRequest(_ context.Context, a dbgen.ConsumeLinkRequestParams) (int64, error) {
	if err := s.err("ConsumeLinkRequest"); err != nil {
		return 0, err
	}
	r := s.req(a.ID)
	if r == nil || a.OrgID != org || r.consumed != nil || r.voided != nil {
		return 0, nil
	}
	now := a.Now
	r.consumed = &now
	return 1, nil
}

func (s *fakeStore) RecordWrongCode(_ context.Context, a dbgen.RecordWrongCodeParams) (int64, error) {
	if err := s.err("RecordWrongCode"); err != nil {
		return 0, err
	}
	r := s.req(a.ID)
	if r == nil || a.OrgID != org {
		return 0, pgx.ErrNoRows
	}
	r.attempts++
	if r.attempts >= a.MaxAttempts {
		now := a.Now
		r.voided = &now
	}
	return r.attempts, nil
}

func (s *fakeStore) VoidPendingMattermostRequests(_ context.Context, a dbgen.VoidPendingMattermostRequestsParams) (
	int64, error) {
	if err := s.err("VoidPendingMattermostRequests"); err != nil {
		return 0, err
	}
	var n int64
	for i := range s.reqs {
		r := &s.reqs[i]
		if a.OrgID == org && r.userID == a.UserID && r.connID == a.ConnectionID && r.messenger == MessengerMattermost &&
			r.consumed == nil && r.voided == nil {
			now := a.Now
			r.voided = &now
			n++
		}
	}
	return n, nil
}

func (s *fakeStore) VoidLinkRequest(_ context.Context, a dbgen.VoidLinkRequestParams) error {
	if err := s.err("VoidLinkRequest"); err != nil {
		return err
	}
	if r := s.req(a.ID); r != nil && a.OrgID == org && r.consumed == nil && r.voided == nil {
		now := a.Now
		r.voided = &now
	}
	return nil
}

func (s *fakeStore) count(match func(fReq) bool, since time.Time) (int64, time.Time) {
	var n int64
	oldest := since
	for _, r := range s.reqs {
		if r.messenger == MessengerMattermost && r.created.After(since) && match(r) {
			if n == 0 || r.created.Before(oldest) {
				oldest = r.created
			}
			n++
		}
	}
	return n, oldest
}

// mark records a count or a lock, with whether it ran in a transaction.
func (s *fakeStore) mark(name string) {
	if s.inTx {
		name += " in tx"
	}
	s.counted = append(s.counted, name)
}

func (s *fakeStore) LockCodeTarget(_ context.Context, a dbgen.LockCodeTargetParams) error {
	if err := s.err("LockCodeTarget"); err != nil {
		return err
	}
	if a.LockClass != db.AccountLinkCodeTargetLockClass || a.OrgID != org {
		return errors.New("the wrong lock")
	}
	s.mark("lock " + strconv.FormatInt(a.ConnectionID, 10) + "/" + a.TargetExternalID)
	return nil
}

func (s *fakeStore) CountUserCodeRequests(_ context.Context, a dbgen.CountUserCodeRequestsParams) (
	dbgen.CountUserCodeRequestsRow, error) {
	if err := s.err("CountUserCodeRequests"); err != nil {
		return dbgen.CountUserCodeRequestsRow{}, err
	}
	s.mark("user")
	n, oldest := s.count(func(r fReq) bool { return a.OrgID == org && r.userID == a.UserID }, a.Since)
	return dbgen.CountUserCodeRequestsRow{Requests: n, Oldest: oldest}, nil
}

func (s *fakeStore) CountTargetCodeRequests(_ context.Context, a dbgen.CountTargetCodeRequestsParams) (
	dbgen.CountTargetCodeRequestsRow, error) {
	if err := s.err("CountTargetCodeRequests"); err != nil {
		return dbgen.CountTargetCodeRequestsRow{}, err
	}
	s.mark("target")
	n, oldest := s.count(func(r fReq) bool {
		return a.OrgID == org && r.connID == a.ConnectionID && r.target == a.TargetExternalID
	}, a.Since)
	return dbgen.CountTargetCodeRequestsRow{Requests: n, Oldest: oldest}, nil
}

func (s *fakeStore) DeleteUserAccountLinks(_ context.Context, a dbgen.DeleteUserAccountLinksParams) (
	[]dbgen.DeleteUserAccountLinksRow, error) {
	if err := s.err("DeleteUserAccountLinks"); err != nil {
		return nil, err
	}
	out := []dbgen.DeleteUserAccountLinksRow{}
	for _, l := range s.links {
		if a.OrgID == org && l.userID == a.UserID {
			c := s.conn(l.connID)
			out = append(out, dbgen.DeleteUserAccountLinksRow{PublicID: l.publicID, Messenger: l.messenger,
				ExternalID: l.external, Username: ptrText(l.username), ConnectionPublicID: c.publicID,
				ConnectionName: c.name})
		}
	}
	s.links = slices.DeleteFunc(s.links, func(l fLink) bool { return a.OrgID == org && l.userID == a.UserID })
	return out, nil
}

func (s *fakeStore) VoidUserLinkRequests(_ context.Context, a dbgen.VoidUserLinkRequestsParams) (int64, error) {
	if err := s.err("VoidUserLinkRequests"); err != nil {
		return 0, err
	}
	var n int64
	for i := range s.reqs {
		r := &s.reqs[i]
		if a.OrgID == org && r.userID == a.UserID && r.consumed == nil && r.voided == nil {
			now := a.Now
			r.voided = &now
			n++
		}
	}
	return n, nil
}

func (s *fakeStore) InsertAuditEntry(_ context.Context, a adb.InsertAuditEntryParams) error {
	if err := s.err("InsertAuditEntry"); err != nil {
		return err
	}
	s.audit = append(s.audit, a)
	return nil
}

func (s *fakeStore) Notify(_ context.Context, h db.Hint) error {
	if err := s.err("Notify"); err != nil {
		return err
	}
	if !s.inTx {
		return errors.New("a hint outside a transaction")
	}
	s.hints = append(s.hints, h)
	return nil
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

func ptrText(s *string) pgtype.Text { return optText(s) }

func ts(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

func ptr[T any](v T) *T { return &v }

// entry is an Audit log entry as the tests read it.
type entry struct {
	Action, Actor, Transport, ResourceType, Resource, Address string
	Details                                                   map[string]any
	Diff                                                      []audit.Change
}

func entries(s *fakeStore) []entry {
	out := make([]entry, len(s.audit))
	for i, a := range s.audit {
		e := entry{Action: a.Action, Actor: a.ActorKind, Transport: a.Transport, ResourceType: a.ResourceType.String,
			Resource: a.ResourcePublicID.String}
		if a.SourceAddress != nil {
			e.Address = a.SourceAddress.String()
		}
		if a.ActorUserID.Valid {
			e.Actor += ":" + strconv.FormatInt(a.ActorUserID.Int64, 10)
		}
		_ = json.Unmarshal(a.Details, &e.Details)
		_ = json.Unmarshal(a.Diff, &e.Diff)
		out[i] = e
	}
	return out
}

// env is a Service over a fakeStore with a manual business clock, fake bots and a log.
type env struct {
	s     *Service
	store *fakeStore
	clock *clock.Manual
	log   *bytes.Buffer
	tg    *fakeTG
	mm    *fakeMM
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{store: newStore(), clock: clock.NewManual(t0), log: &bytes.Buffer{}, tg: &fakeTG{username: "fallback_bot"},
		mm: newFakeMM()}
	logger := logging.New(e.log, logging.LevelInfo)
	e.s = New(Config{OrgID: org, Store: e.store, Audit: audit.NewWriter(logger, e.clock), Business: e.clock,
		Log: logger, Telegram: e.tg, Mattermost: e.mm, Random: &counter{}})
	return e
}

// counter is a random source that never repeats: each read is the next run of bytes.
type counter struct{ n byte }

func (c *counter) Read(p []byte) (int, error) {
	for i := range p {
		c.n++
		p[i] = c.n
	}
	return len(p), nil
}

var (
	alice = Requester{UserID: aliceID, UserPublicID: alicePID, Actor: audit.User(aliceID, alicePID),
		Transport: audit.TransportUI, Address: netip.MustParseAddr("192.0.2.1")}
	bob = Requester{UserID: bobID, UserPublicID: bobPID, Actor: audit.User(bobID, bobPID),
		Transport: audit.TransportUI, Address: netip.MustParseAddr("192.0.2.2")}
	admin = Requester{UserID: carolID, UserPublicID: carolPID, Actor: audit.User(carolID, carolPID).Via(9, "cli"),
		Transport: audit.TransportAPI, Address: netip.MustParseAddr("192.0.2.3")}
)

func (e *env) addLink(userID int64, messenger string, connID int64, external string, username *string) fLink {
	l := fLink{id: e.store.id(), publicID: "AKAAAAAAAAA" + strconv.FormatInt(e.store.next, 10), userID: userID,
		messenger: messenger, connID: connID, external: external, username: username, createdAt: t0}
	e.store.links = append(e.store.links, l)
	return l
}

// TestLookup is C-18.FR-8 and C-10.FR-11: an account maps to the User of its link with their status, a deleted User
// and an unknown account map to none, and a lookup records the last use at most once a minute.
func TestLookup(t *testing.T) {
	e := newEnv(t)
	l := e.addLink(bobID, MessengerMattermost, mmID, "u-bob", ptr("bob"))
	e.addLink(carolID, MessengerTelegram, tgID, "7003", nil)
	e.addLink(daveID, MessengerTelegram, tg2ID, "7004", nil)
	u, err := e.s.Lookup(t.Context(), SpaceMattermost(mmID), "u-bob")
	if err != nil || u != (User{ID: bobID, PublicID: bobPID, Login: "bob", Name: "Bob", Role: "responder",
		Status: StatusActive}) {
		t.Fatalf("Lookup = %+v, %v", u, err)
	}
	if got := e.store.links[0].lastUsed; got == nil || !got.Equal(t0) {
		t.Fatalf("last use %v", got)
	}
	e.clock.Advance(30 * time.Second)
	e.store.fail["TouchAccountLink"] = errBoom
	if _, err := e.s.Lookup(t.Context(), SpaceMattermost(mmID), "u-bob"); err != nil {
		t.Fatalf("a lookup within the minute wrote the last use: %v", err)
	}
	delete(e.store.fail, "TouchAccountLink")
	e.clock.Advance(31 * time.Second)
	if _, err := e.s.Lookup(t.Context(), SpaceMattermost(mmID), "u-bob"); err != nil ||
		!e.store.links[0].lastUsed.Equal(t0.Add(61*time.Second)) {
		t.Fatalf("a lookup after a minute = %v, last use %v", err, e.store.links[0].lastUsed)
	}
	if u, err := e.s.Lookup(t.Context(), SpaceTelegram, "7003"); err != nil || u.Status != StatusDisabled {
		t.Errorf("a disabled User = %+v, %v", u, err)
	}
	for _, c := range [][2]string{{SpaceTelegram, "7004"}, {SpaceTelegram, "u-bob"}, {SpaceMattermost(mmID + 1),
		"u-bob"}, {SpaceMattermost(mmID), "u-alice"}} {
		if _, err := e.s.Lookup(t.Context(), c[0], c[1]); !errors.Is(err, ErrNotLinked) {
			t.Errorf("Lookup(%s, %s) = %v", c[0], c[1], err)
		}
	}
	e.clock.Advance(time.Hour)
	e.store.fail["TouchAccountLink"] = errBoom
	if _, err := e.s.Lookup(t.Context(), SpaceMattermost(mmID), "u-bob"); !errors.Is(err, errBoom) {
		t.Errorf("a failed write of the last use = %v", err)
	}
	e.store.fail["LookupAccountLink"] = errBoom
	if _, err := e.s.Lookup(t.Context(), SpaceMattermost(mmID), "u-bob"); !errors.Is(err, errBoom) ||
		errors.Is(err, ErrNotLinked) {
		t.Errorf("a failed read = %v", err)
	}
	if SpaceMattermost(12) != "mattermost:12" || l.space() != "mattermost:12" {
		t.Error("the identity space of a Connection")
	}
}

// TestLists is C-18.FR-6 and FR-10: a User's links with their Connection, username and last use; an Admin's list of
// another User, unknown and deleted Users included; the Connections to link through, by messenger and name.
func TestLists(t *testing.T) {
	e := newEnv(t)
	used := t0.Add(time.Minute)
	l := e.addLink(bobID, MessengerMattermost, mmID, "u-bob", ptr("bob"))
	e.store.links[0].lastUsed = &used
	e.addLink(bobID, MessengerTelegram, tgID, "7002", nil)
	links, err := e.s.List(t.Context(), bobID)
	if err != nil || len(links) != 2 || links[0].PublicID != l.publicID || links[0].ConnectionName != "mm" ||
		links[0].ConnectionPublicID != mmPID || *links[0].Username != "bob" || !links[0].LastUsedAt.Equal(used) ||
		links[1].Username != nil || links[1].LastUsedAt != nil || links[1].Messenger != MessengerTelegram {
		t.Fatalf("List = %+v, %v", links, err)
	}
	if got, err := e.s.ListOfUser(t.Context(), strings.ToLower(bobPID)); err != nil || len(got) != 2 {
		t.Errorf("ListOfUser = %+v, %v", got, err)
	}
	if got, err := e.s.ListOfUser(t.Context(), davePID); err != nil || len(got) != 0 {
		t.Errorf("a deleted User = %+v, %v", got, err)
	}
	for _, id := range []string{"SRAAAAAAAAAAA9", "nope"} {
		if _, err := e.s.ListOfUser(t.Context(), id); !errors.Is(err, ErrUserNotFound) {
			t.Errorf("ListOfUser(%s) = %v", id, err)
		}
	}
	conns, err := e.s.Connections(t.Context())
	if err != nil || !slices.Equal(conns, []LinkableConnection{{mmPID, "mm", MessengerMattermost},
		{tgPID, "Dev Telegram", MessengerTelegram}, {tg2PID, "Second", MessengerTelegram}}) {
		t.Errorf("Connections = %+v, %v", conns, err)
	}
	for name, call := range map[string]func() error{
		"ListAccountLinksOfUser":  func() error { _, err := e.s.List(t.Context(), bobID); return err },
		"GetUserByPublicID":       func() error { _, err := e.s.ListOfUser(t.Context(), bobPID); return err },
		"ListLinkableConnections": func() error { _, err := e.s.Connections(t.Context()); return err },
	} {
		e.store.fail[name] = errBoom
		if err := call(); !errors.Is(err, errBoom) {
			t.Errorf("a failed %s = %v", name, err)
		}
		delete(e.store.fail, name)
	}
}

// TestUnlink is C-18.FR-6 and FR-9: a User unlinks their own link from the web session, an Admin any User's link;
// each is recorded as account_link.unlink with the actor, the Transport and the client address, and the hint that
// names the link goes to the link's User. A link of another User, or an unknown one, is ErrNotFound.
func TestUnlink(t *testing.T) {
	e := newEnv(t)
	tl := e.addLink(aliceID, MessengerTelegram, tgID, "7001", ptr("alice_t"))
	ml := e.addLink(bobID, MessengerMattermost, mmID, "u-bob", ptr("bob"))
	for _, id := range []string{ml.publicID, "AKAAAAAAAAAAZZ", "bad"} {
		if err := e.s.Unlink(t.Context(), alice, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Unlink(%s) = %v", id, err)
		}
	}
	if err := e.s.Unlink(t.Context(), alice, tl.publicID); err != nil {
		t.Fatal(err)
	}
	got := entries(e.store)
	if len(e.store.links) != 1 || len(got) != 1 || got[0].Action != ActionUnlink ||
		got[0].Actor != "user:"+strconv.Itoa(aliceID) || got[0].Transport != "ui" || got[0].Address != "192.0.2.1" ||
		got[0].ResourceType != ResourceLink || got[0].Resource != tl.publicID || got[0].Details["reason"] != nil ||
		got[0].Details["external_id"] != "7001" || got[0].Details["username"] != "alice_t" ||
		got[0].Details["connection"] != tgPID || got[0].Details["messenger"] != MessengerTelegram ||
		got[0].Details["user"] != alicePID || len(got[0].Diff) != 4 || got[0].Diff[0].After != nil {
		t.Fatalf("unlink: links %+v, audit %+v", e.store.links, got)
	}
	if !slices.Equal(e.store.hints, []db.Hint{{OrgID: org, Type: HintType, ID: tl.publicID, User: alicePID}}) {
		t.Errorf("hints %+v", e.store.hints)
	}
	if !strings.Contains(e.log.String(), `"event":"account_link_unlinked","messenger":"telegram","connection":"`+
		tgPID+`","user":"`+alicePID+`","reason":"removed"`) {
		t.Errorf("log %s", e.log)
	}
	if err := e.s.UnlinkOfUser(t.Context(), admin, bobPID, tl.publicID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another User's link = %v", err)
	}
	if err := e.s.UnlinkOfUser(t.Context(), admin, "SRAAAAAAAAAAA9", ml.publicID); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("an unknown User = %v", err)
	}
	e.store.fail["InsertAuditEntry"] = errBoom
	if err := e.s.UnlinkOfUser(t.Context(), admin, bobPID, ml.publicID); !errors.Is(err, errBoom) ||
		len(e.store.links) != 1 {
		t.Fatalf("a failed Audit log entry = %v, links %+v", err, e.store.links)
	}
	delete(e.store.fail, "InsertAuditEntry")
	if err := e.s.UnlinkOfUser(t.Context(), admin, bobPID, ml.publicID); err != nil {
		t.Fatal(err)
	}
	got = entries(e.store)
	if last := got[len(got)-1]; len(e.store.links) != 0 || last.Actor != "user:"+strconv.Itoa(carolID) ||
		last.Transport != "api" || last.Address != "192.0.2.3" || last.Details["user"] != bobPID ||
		e.store.hints[1].User != bobPID || e.store.hints[1].ID != ml.publicID {
		t.Errorf("an Admin's unlink: %+v, hints %+v", last, e.store.hints)
	}
	for _, name := range []string{"GetAccountLinkOfUser", "DeleteAccountLink"} {
		l := e.addLink(aliceID, MessengerTelegram, tgID, "7001", nil)
		e.store.fail[name] = errBoom
		if err := e.s.Unlink(t.Context(), alice, l.publicID); !errors.Is(err, errBoom) {
			t.Errorf("a failed %s = %v", name, err)
		}
		delete(e.store.fail, name)
		e.store.links = nil
	}
}

// TestRemoveUserLinks is C-03.FR-13 and C-18.FR-8: deleting a User removes their links, each recorded as
// account_link.unlink with the reason user_deleted by the Admin, voids their pending requests and sends one hint that
// names no link; the log lines come once the deletion committed.
func TestRemoveUserLinks(t *testing.T) {
	e := newEnv(t)
	e.addLink(bobID, MessengerTelegram, tgID, "7002", nil)
	e.addLink(bobID, MessengerMattermost, mmID, "u-bob", ptr("bob"))
	e.addLink(aliceID, MessengerTelegram, tgID, "7001", nil)
	e.store.reqs = []fReq{{id: 1, userID: bobID, messenger: MessengerMattermost, connID: mmID},
		{id: 2, userID: bobID, messenger: MessengerTelegram, connID: tgID, consumed: &t0},
		{id: 3, userID: aliceID, messenger: MessengerTelegram, connID: tgID}}
	var after func(context.Context)
	err := e.store.InTx(t.Context(), func(q Queries) error {
		var err error
		after, err = e.s.removeUserLinks(t.Context(), q, bobID, admin.Actor, admin.Transport, admin.Address)
		return err
	})
	if err != nil || after == nil {
		t.Fatal(err)
	}
	got := entries(e.store)
	if len(e.store.links) != 1 || len(got) != 2 || got[0].Details["reason"] != ReasonUserDeleted ||
		got[1].Details["reason"] != ReasonUserDeleted || got[0].Actor != "user:"+strconv.Itoa(carolID) ||
		got[0].Transport != "api" || got[0].Address != "192.0.2.3" {
		t.Fatalf("links %+v, audit %+v", e.store.links, got)
	}
	if e.store.reqs[0].voided == nil || e.store.reqs[1].voided != nil || e.store.reqs[2].voided != nil ||
		!slices.Equal(e.store.hints, []db.Hint{{OrgID: org, Type: HintType, User: bobPID}}) {
		t.Errorf("requests %+v, hints %+v", e.store.reqs, e.store.hints)
	}
	if strings.Contains(e.log.String(), "account_link_unlinked") {
		t.Fatal("logged before the commit")
	}
	after(t.Context())
	if strings.Count(e.log.String(), `"event":"account_link_unlinked"`) != 2 {
		t.Errorf("log %s", e.log)
	}
	if after, err := e.s.RemoveUserLinks(t.Context(), failDB{}, carolID, admin.Actor, admin.Transport,
		admin.Address); !errors.Is(err, errBoom) || after != nil {
		t.Errorf("RemoveUserLinks over a failing transaction = %v", err)
	}
	for _, name := range []string{"LockLinkUser", "DeleteUserAccountLinks", "InsertAuditEntry", "VoidUserLinkRequests",
		"Notify"} {
		e.addLink(aliceID, MessengerMattermost, mmID, "u-alice", nil)
		e.store.fail[name] = errBoom
		_, err := e.s.removeUserLinks(t.Context(), e.store, aliceID, admin.Actor, admin.Transport, admin.Address)
		if !errors.Is(err, errBoom) {
			t.Errorf("a failed %s = %v", name, err)
		}
		delete(e.store.fail, name)
		e.store.links = nil
	}
	if after, err := e.s.removeUserLinks(t.Context(), e.store, aliceID, admin.Actor, admin.Transport,
		admin.Address); err != nil || after != nil {
		t.Errorf("a User without links = %v", err)
	}
}

// failDB is a transaction whose every statement fails.
type failDB struct{}

func (failDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errBoom
}

func (failDB) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, errBoom }

func (failDB) QueryRow(context.Context, string, ...any) pgx.Row { return failRow{} }

type failRow struct{}

func (failRow) Scan(...any) error { return errBoom }

type fakePrune struct {
	got dbgen.PruneLinkRequestsParams
	err error
}

func (f *fakePrune) PruneLinkRequests(_ context.Context, a dbgen.PruneLinkRequestsParams) (int64, error) {
	f.got = a
	return 7, f.err
}

// TestPruner: link requests are deleted account_link.request_prune_after past their expiry, in batches.
func TestPruner(t *testing.T) {
	f := &fakePrune{}
	p := Pruner{q: f}
	n, err := p.Requests(t.Context(), org, t0, 50)
	if err != nil || n != 7 || f.got != (dbgen.PruneLinkRequestsParams{OrgID: org, Before: t0.Add(-24 * time.Hour),
		BatchSize: 50}) {
		t.Fatalf("Requests = %d, %v with %+v", n, err, f.got)
	}
	f.err = errBoom
	if _, err := p.Requests(t.Context(), org, t0, 50); !errors.Is(err, errBoom) {
		t.Errorf("a failed delete = %v", err)
	}
	if NewPruner(nil).q == nil || NewStore(nil) == nil {
		t.Error("the stores over the pool")
	}
}

// TestErrors: the errors say what failed without a token or a code.
func TestErrors(t *testing.T) {
	r := &RateLimitedError{RetryAfter: 1500 * time.Millisecond}
	if r.Seconds() != 2 || (&RateLimitedError{}).Seconds() != 1 || !strings.Contains(r.Error(), "1.5s") {
		t.Errorf("RateLimitedError %d %s", r.Seconds(), r)
	}
	m := &MessengerError{Call: "the direct message", Outcome: "fatal", Status: 403}
	if m.Error() != "the messenger refused the direct message (fatal, status 403)" {
		t.Error(m)
	}
	if f := (&FieldError{Pointer: "/code", Detail: "wrong"}); f.Error() != "/code: wrong" {
		t.Error(f)
	}
	if language(text("ru")) != "ru" || language(text("de")) != "en" || language(pgtype.Text{}) != "en" {
		t.Error("the language of the bot's messages")
	}
}

// beginFail is a transaction whose savepoints cannot start.
type beginFail struct{ failDB }

func (beginFail) Begin(context.Context) (pgx.Tx, error) { return nil, errBoom }

// TestSavepoint: the queries over a transaction run f in a savepoint, and over anything that cannot start one run f
// as they are.
func TestSavepoint(t *testing.T) {
	ran := false
	if err := queriesOver(failDB{}).Savepoint(t.Context(), func(Queries) error { ran = true; return nil }); err != nil ||
		!ran {
		t.Errorf("without savepoints = %v, ran %v", err, ran)
	}
	ran = false
	if err := queriesOver(beginFail{}).Savepoint(t.Context(), func(Queries) error { ran = true; return nil }); !errors.Is(
		err, errBoom) || ran {
		t.Errorf("a savepoint that fails to start = %v, ran %v", err, ran)
	}
}

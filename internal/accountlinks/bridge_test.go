// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package accountlinks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/accountlinks/dbgen"
)

func (s *fakeStore) GetBridgeTokenRequestByHash(_ context.Context, a dbgen.GetBridgeTokenRequestByHashParams) (
	dbgen.GetBridgeTokenRequestByHashRow, error) {
	if err := s.err("GetBridgeTokenRequestByHash"); err != nil {
		return dbgen.GetBridgeTokenRequestByHashRow{}, err
	}
	for _, r := range s.reqs {
		if a.OrgID == org && r.messenger == MessengerBridge && r.method.String == MethodToken &&
			bytes.Equal(r.hash, a.SecretHash) {
			u, _ := s.user(r.userID)
			return dbgen.GetBridgeTokenRequestByHashRow{ID: r.id, UserID: r.userID, ConnectionID: r.connID,
				ExpiresAt: r.expires, ConsumedAt: ts(r.consumed), VoidedAt: ts(r.voided), UserPublicID: u.publicID,
				ConnectionPublicID: s.conn(r.connID).publicID}, nil
		}
	}
	return dbgen.GetBridgeTokenRequestByHashRow{}, pgx.ErrNoRows
}

func (s *fakeStore) VoidPendingBridgeCodeRequests(_ context.Context, a dbgen.VoidPendingBridgeCodeRequestsParams) (
	int64, error) {
	if err := s.err("VoidPendingBridgeCodeRequests"); err != nil {
		return 0, err
	}
	var n int64
	for i := range s.reqs {
		r := &s.reqs[i]
		if a.OrgID == org && r.userID == a.UserID && r.connID == a.ConnectionID && r.messenger == MessengerBridge &&
			r.method.String == MethodCode && r.consumed == nil && r.voided == nil {
			now := a.Now
			r.voided = &now
			n++
		}
	}
	return n, nil
}

func (s *fakeStore) CountUserBridgeCodeRequests(_ context.Context, a dbgen.CountUserBridgeCodeRequestsParams) (
	dbgen.CountUserBridgeCodeRequestsRow, error) {
	if err := s.err("CountUserBridgeCodeRequests"); err != nil {
		return dbgen.CountUserBridgeCodeRequestsRow{}, err
	}
	s.mark("bridge user")
	n, oldest := s.count(MessengerBridge, func(r fReq) bool { return a.OrgID == org && r.userID == a.UserID }, a.Since)
	return dbgen.CountUserBridgeCodeRequestsRow{Requests: n, Oldest: oldest}, nil
}

func (s *fakeStore) CountTargetBridgeCodeRequests(_ context.Context, a dbgen.CountTargetBridgeCodeRequestsParams) (
	dbgen.CountTargetBridgeCodeRequestsRow, error) {
	if err := s.err("CountTargetBridgeCodeRequests"); err != nil {
		return dbgen.CountTargetBridgeCodeRequestsRow{}, err
	}
	s.mark("bridge target")
	n, oldest := s.count(MessengerBridge, func(r fReq) bool {
		return a.OrgID == org && r.connID == a.ConnectionID && r.target == a.TargetExternalID
	}, a.Since)
	return dbgen.CountTargetBridgeCodeRequestsRow{Requests: n, Oldest: oldest}, nil
}

// brDM is a direct message the fake bridge bot sent.
type brDM struct{ key, user, text string }

// fakeBR is the bot of a Bridge Connection: its offer, the accounts it finds by handle, and the direct messages it
// sent; each error makes its call fail.
type fakeBR struct {
	offer      BridgeOffer
	users      map[string]BridgeAccount
	sent       []brDM
	sessions   []string
	sessionErr error
	findErr    error
	sendErr    error
	onFind     func()
}

func newFakeBR() *fakeBR {
	return &fakeBR{offer: BridgeOffer{Read: true, UserLookup: LookupEmail, BotCanStart: true},
		users: map[string]BridgeAccount{
			"bob@example.org":   {ExternalID: "u-bob", DisplayName: "Bob Brown", Handle: "bob@example.org"},
			"alice@example.org": {ExternalID: "u-alice", DisplayName: "Alice Smith", Handle: "alice@example.org"},
			"dana@example.org":  {ExternalID: "u-dana", DisplayName: "Dana"},
		}}
}

func (f *fakeBR) Session(ctx context.Context, id string, fn func(context.Context, BridgeBot) error) error {
	if f.sessionErr != nil {
		return f.sessionErr
	}
	f.sessions = append(f.sessions, id)
	return fn(ctx, f)
}

func (f *fakeBR) Offer() BridgeOffer { return f.offer }

func (f *fakeBR) FindUser(_ context.Context, handle string) (BridgeAccount, error) {
	if f.onFind != nil {
		f.onFind()
	}
	if f.findErr != nil {
		return BridgeAccount{}, f.findErr
	}
	a, ok := f.users[handle]
	if !ok {
		return BridgeAccount{}, ErrMessengerUserNotFound
	}
	return a, nil
}

func (f *fakeBR) SendDirectMessage(_ context.Context, key, user, text string) error {
	f.sent = append(f.sent, brDM{key, user, text})
	return f.sendErr
}

func (f *fakeBR) code(t *testing.T) string {
	t.Helper()
	if len(f.sent) == 0 {
		t.Fatal("no direct message")
	}
	c := codeRe.FindString(f.sent[len(f.sent)-1].text)
	if c == "" {
		t.Fatalf("no code in %q", f.sent[len(f.sent)-1].text)
	}
	return c
}

// startBR is a started Bridge link of r through the Bridge Connection connPID by token, and its token.
func (e *env) startBR(t *testing.T, r Requester, connPID string) string {
	t.Helper()
	st, err := e.s.StartBridge(t.Context(), r, connPID)
	if err != nil {
		t.Fatal(err)
	}
	return st.Token
}

// sendBR is an account_link event with token from the account from to the bot of the Bridge Connection connID: its
// answer, once Log ran with answerErr.
func (e *env) sendBR(t *testing.T, connID int64, connPID, token string, from BridgeAccount, answerErr error) string {
	t.Helper()
	out, err := e.s.LinkBridge(t.Context(), BridgeConnection{ID: connID, PublicID: connPID}, token, from)
	if err != nil {
		t.Fatal(err)
	}
	out.Log(t.Context(), answerErr)
	return out.Answer
}

func (e *env) startCode(t *testing.T, r Requester, handle string) BridgeCodeStarted {
	t.Helper()
	st, err := e.s.StartBridgeCode(t.Context(), r, brPID, handle)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

var aliceBR = BridgeAccount{ExternalID: "u-alice", DisplayName: "Alice Smith", Handle: "alice@example.org"}

// TestStartBridge is C-22.FR-15 for the token: 32 random bytes in base64url, of which only the hash is stored with the
// User, the Connection, the method token and an expiry of account_link.bridge_token_ttl; the link is the stored info's
// link_url_template with the token, and none without a template, or for one that is not an http or https link. A
// Connection that is not a Bridge one is unknown.
func TestStartBridge(t *testing.T) {
	e := newEnv(t)
	st, err := e.s.StartBridge(t.Context(), alice, strings.ToLower(brPID))
	if err != nil {
		t.Fatal(err)
	}
	raw, derr := base64.RawURLEncoding.DecodeString(st.Token)
	sum := sha256.Sum256([]byte(st.Token))
	r := e.store.reqs[0]
	if len(st.Token) != 43 || derr != nil || len(raw) != 32 || !st.ExpiresAt.Equal(t0.Add(10*time.Minute)) ||
		st.LinkURL == nil || *st.LinkURL != "https://chat.example.org/bots/muster?start="+st.Token ||
		!bytes.Equal(r.hash, sum[:]) || r.messenger != MessengerBridge || r.method.String != MethodToken ||
		r.connID != brID || r.userID != aliceID || r.target.Valid || !strings.HasPrefix(r.publicID, "AR") {
		t.Fatalf("StartBridge = %+v, stored %+v", st, r)
	}
	if len(e.br.sessions) != 0 || len(e.store.audit) != 0 || strings.Contains(e.log.String(), st.Token) {
		t.Errorf("a token start called the bridge or recorded: %v, %d entries", e.br.sessions, len(e.store.audit))
	}
	if st, err := e.s.StartBridge(t.Context(), alice, br2PID); err != nil || st.LinkURL != nil {
		t.Errorf("a Connection whose info was never read = %+v, %v", st, err)
	}
	for _, tmpl := range []string{"ftp://chat.example.org/bot?start={token}", "https://chat.example.org/bot",
		"https://chat.example.org/bot?start={token} x"} {
		e.store.infos[brID] = fInfo{template: tmpl}
		if st, err := e.s.StartBridge(t.Context(), alice, brPID); err != nil || st.LinkURL != nil {
			t.Errorf("template %q = %+v, %v", tmpl, st, err)
		}
	}
	var fe *FieldError
	for _, id := range []string{tgPID, mmPID, brGonePID, "CNAAAAAAAAAAZZ", "bad"} {
		if _, err := e.s.StartBridge(t.Context(), alice, id); !errors.As(err, &fe) || fe.Code != CodeUnknownID ||
			fe.Pointer != "/connection_id" || fe.Detail != "No Bridge Connection has this id." {
			t.Errorf("StartBridge(%s) = %v", id, err)
		}
	}
	for _, name := range []string{"GetLinkConnection", "InsertLinkRequest"} {
		e.store.fail = map[string]error{name: errBoom}
		if _, err := e.s.StartBridge(t.Context(), alice, brPID); !errors.Is(err, errBoom) {
			t.Errorf("a failed %s = %v", name, err)
		}
	}
	e.store.fail = map[string]error{}
	e.s.cfg.Random = bytes.NewReader(nil)
	if _, err := e.s.StartBridge(t.Context(), alice, brPID); err == nil {
		t.Error("a token without randomness")
	}
}

// TestLinkBridge is C-22.FR-15, FR-13 and AC-18: a token sent to the bot links the sender's account — its external id,
// handle and display name — to the User, recorded with the Transport bridge, answered "Linked to Muster user {name}
// ({login})" and logged once the caller logs; the same token again, an unknown one, an expired one and one of another
// Connection link nothing, are answered that the link is invalid or expired — in English for an unknown token, in the
// bound User's language otherwise — and are recorded as link_rejected. A new link replaces the User's earlier one for
// the same Connection; a second Bridge Connection is a second identity space.
func TestLinkBridge(t *testing.T) {
	e := newEnv(t)
	tok := e.startBR(t, alice, brPID)
	out, err := e.s.LinkBridge(t.Context(), BridgeConnection{ID: brID, PublicID: brPID}, tok, aliceBR)
	if err != nil || out.Answer != "Linked to Muster user Alice Smith (alice)" || len(e.store.links) != 1 {
		t.Fatalf("LinkBridge = %+v, %v", out, err)
	}
	l := e.store.links[0]
	if l.external != "u-alice" || *l.username != "alice@example.org" || *l.displayName != "Alice Smith" ||
		l.userID != aliceID || l.messenger != MessengerBridge || l.space() != "bridge:15" ||
		e.store.reqs[0].consumed == nil {
		t.Fatalf("links %+v, requests %+v", e.store.links, e.store.reqs)
	}
	if got := e.lastEntry(); got.Action != ActionLink || got.Actor != "user:1" || got.Transport != "bridge" ||
		got.Address != "" || got.Details["external_id"] != "u-alice" || got.Details["display_name"] != "Alice Smith" ||
		got.Details["username"] != "alice@example.org" || got.Details["messenger"] != MessengerBridge ||
		got.Details["connection"] != brPID || len(got.Diff) != 5 {
		t.Fatalf("entry %+v", got)
	}
	if len(e.store.hints) != 1 || e.store.hints[0].User != alicePID || e.store.hints[0].ID != l.publicID {
		t.Errorf("hints %+v", e.store.hints)
	}
	if strings.Contains(e.log.String(), "account_link_linked") {
		t.Fatal("logged before the answer")
	}
	out.Log(t.Context(), nil)
	if !strings.Contains(e.log.String(), `"event":"account_link_linked","messenger":"bridge","connection":"`+brPID+
		`","user":"`+alicePID+`"}`) || strings.Contains(e.log.String(), tok) {
		t.Errorf("log %s", e.log)
	}
	// The token works once.
	answer := e.sendBR(t, brID, brPID, tok, BridgeAccount{ExternalID: "u-mallory", DisplayName: "Mallory"}, nil)
	got := e.lastEntry()
	if answer != invalidText || len(e.store.links) != 1 || got.Action != ActionLinkRejected ||
		got.Details["reason"] != ReasonTokenUsed || got.Details["external_id"] != "u-mallory" ||
		got.Details["user"] != alicePID || got.Actor != "system" || got.Transport != "bridge" ||
		got.ResourceType != "connection" || got.Resource != brPID {
		t.Fatalf("a used token: %q, entry %+v", answer, got)
	}
	if !strings.Contains(e.log.String(), `"event":"account_link_rejected","messenger":"bridge","connection":"`+brPID+
		`","reason":"token_used"`) {
		t.Errorf("log %s", e.log)
	}
	// An unknown token, a Telegram token sent to a bridge's bot included, in English whoever sends it.
	tgTok := e.start(t, bob)
	for _, tk := range []string{strings.Repeat("A", 43), tgTok} {
		answer = e.sendBR(t, brID, brPID, tk, BridgeAccount{ExternalID: "u-bob", DisplayName: "Bob Brown"}, nil)
		if got := e.lastEntry(); answer != invalidText || got.Details["reason"] != ReasonTokenUnknown ||
			got.Details["user"] != nil || got.Details["username"] != nil || got.Details["display_name"] != "Bob Brown" {
			t.Errorf("an unknown token: %q, %+v", answer, got)
		}
	}
	// An expired token, answered in the bound User's language.
	tok = e.startBR(t, bob, brPID)
	e.clock.Advance(10*time.Minute + time.Second)
	answer = e.sendBR(t, brID, brPID, tok, BridgeAccount{ExternalID: "u-bob", DisplayName: "Bob Brown"}, nil)
	if got := e.lastEntry(); answer != "Эта ссылка недействительна или устарела. Начните заново в своём профиле Muster." ||
		got.Details["reason"] != ReasonTokenExpired {
		t.Errorf("an expired token: %q, %+v", answer, got)
	}
	// A token of another Connection is not consumed: it still works with its own bot.
	tok = e.startBR(t, alice, brPID)
	answer = e.sendBR(t, br2ID, br2PID, tok, aliceBR, nil)
	if got := e.lastEntry(); answer != invalidText || got.Details["reason"] != ReasonWrongConnection ||
		got.Resource != br2PID || e.store.reqs[len(e.store.reqs)-1].consumed != nil {
		t.Errorf("another Connection: %q, %+v", answer, got)
	}
	// A new link of the User through the same Connection replaces the earlier one.
	before := len(e.store.audit)
	answer = e.sendBR(t, brID, brPID, tok, BridgeAccount{ExternalID: "u-alice2", DisplayName: "Alice 2"}, nil)
	all := entries(e.store)[before:]
	if answer != "Linked to Muster user Alice Smith (alice)" || len(e.store.links) != 1 ||
		e.store.links[0].external != "u-alice2" || e.store.links[0].username != nil || len(all) != 2 ||
		all[0].Action != ActionUnlink || all[0].Details["reason"] != ReasonReplaced ||
		all[0].Details["display_name"] != "Alice Smith" || all[1].Details["replaced"] != all[0].Resource {
		t.Fatalf("a replacement: links %+v, entries %+v", e.store.links, all)
	}
	if !strings.Contains(e.log.String(), `"event":"account_link_unlinked","messenger":"bridge","connection":"`+brPID+
		`","user":"`+alicePID+`","reason":"replaced"`) {
		t.Errorf("log %s", e.log)
	}
	// Two Connections to one bridge are two identity spaces: the same account is linked through both.
	tok = e.startBR(t, alice, br2PID)
	if answer := e.sendBR(t, br2ID, br2PID, tok, BridgeAccount{ExternalID: "u-alice2", DisplayName: "Alice 2"},
		nil); answer != "Linked to Muster user Alice Smith (alice)" || len(e.store.links) != 2 ||
		e.store.links[1].space() != "bridge:16" {
		t.Errorf("a second Connection: %q, links %+v", answer, e.store.links)
	}
}

// TestLinkBridgeSameAccount is C-18.FR-4 for a Bridge account linked again by its own User: the link is kept, its
// handle and display name become the new ones, and the Audit log shows what changed.
func TestLinkBridgeSameAccount(t *testing.T) {
	e := newEnv(t)
	l := e.addLink(aliceID, MessengerBridge, brID, "u-alice", ptr("alice"))
	e.store.links[0].displayName = ptr("Alice")
	tok := e.startBR(t, alice, brPID)
	if answer := e.sendBR(t, brID, brPID, tok, BridgeAccount{ExternalID: "u-alice", DisplayName: "Alice Smith"},
		nil); answer != "Linked to Muster user Alice Smith (alice)" {
		t.Fatalf("answer %q", answer)
	}
	got := entries(e.store)
	if len(e.store.links) != 1 || e.store.links[0].publicID != l.publicID || e.store.links[0].username != nil ||
		*e.store.links[0].displayName != "Alice Smith" || len(got) != 1 || len(got[0].Diff) != 2 {
		t.Errorf("links %+v, entries %+v", e.store.links, got)
	}
}

// TestLinkBridgeConflict is C-18.FR-5 through a Bridge: an account linked to another User is refused with the answer
// in the User's language and recorded as link_rejected with conflict; the request stays usable. An answer that failed
// is logged with its error.
func TestLinkBridgeConflict(t *testing.T) {
	e := newEnv(t)
	e.addLink(aliceID, MessengerBridge, brID, "u-alice", nil)
	tok := e.startBR(t, bob, brPID)
	answer := e.sendBR(t, brID, brPID, tok, aliceBR, errors.New("the event was not answered: forbidden"))
	if got := e.lastEntry(); answer != "Эта учётная запись связана с другим пользователем. Отвязать её может "+
		"владелец или администратор." || got.Details["reason"] != ReasonConflict || got.Details["user"] != bobPID ||
		e.store.reqs[0].consumed != nil || len(e.store.links) != 1 {
		t.Fatalf("a conflict: %q, %+v, links %+v", answer, got, e.store.links)
	}
	if !strings.Contains(e.log.String(), `"reason":"conflict","error":"the event was not answered: forbidden"`) {
		t.Errorf("log %s", e.log)
	}
	if answer := e.sendBR(t, brID, brPID, tok, BridgeAccount{ExternalID: "u-bob", DisplayName: "Bob Brown"},
		errors.New("lost")); answer != "Учётная запись связана с пользователем Muster Bob (bob)" ||
		len(e.store.links) != 2 || !strings.Contains(e.log.String(), `"user":"`+bobPID+`","error":"lost"`) {
		t.Errorf("the kept request: %q, links %+v, log %s", answer, e.store.links, e.log)
	}
	var zero BridgeLinked
	zero.Log(t.Context(), nil)
}

// TestLinkBridgeFailures: a database failure links nothing and leaves the event to come again.
func TestLinkBridgeFailures(t *testing.T) {
	for _, name := range []string{"GetLinkConnection", "GetBridgeTokenRequestByHash", "LockLinkUser", "InsertAuditEntry",
		"GetAccountLinkBySpace", "GetUserLinkInSpace", "DeleteAccountLink", "InsertAccountLink", "ConsumeLinkRequest",
		"Notify"} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.addLink(aliceID, MessengerBridge, brID, "u-old", nil)
			tok := e.startBR(t, alice, brPID)
			e.store.fail[name] = errBoom
			_, err := e.s.LinkBridge(t.Context(), BridgeConnection{ID: brID, PublicID: brPID}, tok, aliceBR)
			if !errors.Is(err, errBoom) || len(e.store.links) != 1 || e.store.links[0].external != "u-old" ||
				len(e.store.audit) != 0 {
				t.Errorf("= %v, links %+v", err, e.store.links)
			}
		})
	}
}

// TestBridgeCode is C-22.FR-15 and AC-19 for the code: the account found by its handle gets a code with the warning in
// a direct message, key dm-<request public_id>, in the requesting User's language; the request stores the account's
// external id, handle and display name; the right code links it with them, without a confirming message, and each
// wrong code says how many attempts are left; after five the sixth attempt is refused even with the right code.
func TestBridgeCode(t *testing.T) {
	e := newEnv(t)
	st := e.startCode(t, bob, " bob@example.org ")
	if !strings.HasPrefix(st.RequestPublicID, "AR") || !st.ExpiresAt.Equal(t0.Add(10*time.Minute)) ||
		st.AttemptsRemaining != 5 || len(e.br.sent) != 1 || e.br.sent[0].user != "u-bob" ||
		e.br.sent[0].key != "dm-"+st.RequestPublicID || !slices.Equal(e.br.sessions, []string{brPID}) ||
		!strings.HasPrefix(e.br.sent[0].text, "Вы запросили связывание учётной записи с Muster. Никому не сообщайте этот код.") {
		t.Fatalf("started %+v, sent %+v", st, e.br.sent)
	}
	code := e.br.code(t)
	sum := sha256.Sum256([]byte(code))
	r := e.store.reqs[0]
	if !bytes.Equal(r.hash, sum[:]) || r.target.String != "u-bob" || r.targetUsername.String != "bob@example.org" ||
		r.targetDisplayName.String != "Bob Brown" || r.messenger != MessengerBridge || r.method.String != MethodCode ||
		strings.Contains(e.log.String(), code) {
		t.Fatalf("stored %+v", r)
	}
	if _, err := e.s.ConfirmBridge(t.Context(), bob, st.RequestPublicID, "22222222"); !linkCodeInvalid(err) ||
		attemptsLeft(err) != 4 || e.lastEntry().Details["reason"] != ReasonWrongCode {
		t.Fatalf("a wrong code = %v", err)
	}
	l, err := e.s.ConfirmBridge(t.Context(), bob, st.RequestPublicID, strings.ToLower(code))
	if err != nil || l.Messenger != MessengerBridge || l.ExternalID != "u-bob" || *l.Username != "bob@example.org" ||
		*l.DisplayName != "Bob Brown" || l.ConnectionPublicID != brPID || l.ConnectionName != "Dev Bridge" ||
		!strings.HasPrefix(l.PublicID, "AK") || len(e.br.sent) != 1 || len(e.br.sessions) != 1 {
		t.Fatalf("Confirm = %+v, %v", l, err)
	}
	if got := e.lastEntry(); got.Action != ActionLink || got.Transport != "ui" || got.Address != "192.0.2.2" ||
		got.Details["display_name"] != "Bob Brown" || e.store.reqs[0].consumed == nil || len(e.store.hints) != 1 {
		t.Errorf("entry %+v", got)
	}
	if !strings.Contains(e.log.String(), `"event":"account_link_linked","messenger":"bridge","connection":"`+brPID+
		`","user":"`+bobPID+`"}`) {
		t.Errorf("log %s", e.log)
	}
	if _, err := e.s.ConfirmBridge(t.Context(), bob, st.RequestPublicID, code); !linkCodeInvalid(err) ||
		attemptsLeft(err) != 0 || e.lastEntry().Details["reason"] != ReasonTokenUsed {
		t.Errorf("a used request = %v", err)
	}
	// C-22.AC-19: five wrong codes void the request; an account whose handle the bridge does not report keeps the one
	// it was found by.
	st = e.startCode(t, alice, "dana@example.org")
	code = e.br.code(t)
	for i := range 5 {
		_, err := e.s.ConfirmBridge(t.Context(), alice, st.RequestPublicID, "22222222")
		if !linkCodeInvalid(err) || attemptsLeft(err) != 4-i || e.lastEntry().Details["reason"] != ReasonWrongCode {
			t.Fatalf("wrong code %d = %v, %d left", i+1, err, attemptsLeft(err))
		}
	}
	if req := e.store.reqs[len(e.store.reqs)-1]; req.attempts != 5 || req.voided == nil ||
		req.targetUsername.String != "dana@example.org" {
		t.Fatalf("after five wrong codes %+v", req)
	}
	if _, err := e.s.ConfirmBridge(t.Context(), alice, st.RequestPublicID, code); !linkCodeInvalid(err) ||
		attemptsLeft(err) != 0 || len(e.store.links) != 1 || e.lastEntry().Details["reason"] != ReasonWrongCode {
		t.Errorf("the sixth attempt with the right code = %v, links %+v", err, e.store.links)
	}
	// An expired code.
	st = e.startCode(t, alice, "dana@example.org")
	code = e.br.code(t)
	e.clock.Advance(10 * time.Minute)
	if _, err := e.s.ConfirmBridge(t.Context(), alice, st.RequestPublicID, code); !linkCodeInvalid(err) ||
		attemptsLeft(err) != 0 || e.lastEntry().Details["reason"] != ReasonTokenExpired {
		t.Errorf("an expired code = %v", err)
	}
}

// TestBridgeCodeRequestLimits is C-22.FR-15 with account_link.bridge_code_requests on a manual clock: the sixth request
// of one User within an hour and the fourth for one target account are refused until the oldest counted request is an
// hour old, counted before the bridge is called and again in the transaction; a new request voids the User's earlier
// pending one, and a request whose direct message failed still counts.
func TestBridgeCodeRequestLimits(t *testing.T) {
	e := newEnv(t)
	e.startCode(t, alice, "dana@example.org")
	if want := []string{"bridge user", "bridge user in tx", "lock 15/u-dana in tx", "bridge target in tx"}; !slices.Equal(
		e.store.counted, want) {
		t.Fatalf("counted %q, want %q", e.store.counted, want)
	}
	e.clock.Advance(time.Minute)
	e.br.sendErr = ErrDirectMessageRefused
	if _, err := e.s.StartBridgeCode(t.Context(), alice, brPID, "bob@example.org"); err == nil {
		t.Fatal("a refused direct message")
	}
	e.br.sendErr = nil
	for i := range 3 {
		e.clock.Advance(time.Minute)
		e.startCode(t, alice, []string{"bob@example.org", "alice@example.org", "dana@example.org"}[i])
	}
	if n := len(e.store.reqs); e.store.reqs[0].voided == nil || e.store.reqs[n-1].voided != nil {
		t.Fatal("a new request did not void the earlier one")
	}
	var rl *RateLimitedError
	if _, err := e.s.StartBridgeCode(t.Context(), alice, brPID, "bob@example.org"); !errors.As(err, &rl) ||
		rl.RetryAfter != 56*time.Minute || len(e.store.reqs) != 5 || len(e.br.sessions) != 5 {
		t.Fatalf("the sixth request = %v, %d sessions", err, len(e.br.sessions))
	}
	// Mattermost requests are counted apart.
	if st := e.startMM(t, alice, "bob"); st.RequestPublicID == "" {
		t.Error("a Mattermost request is limited by Bridge requests")
	}
	f := newEnv(t)
	for range 3 {
		f.startCode(t, bob, "dana@example.org")
	}
	if _, err := f.s.StartBridgeCode(t.Context(), alice, brPID, "dana@example.org"); !errors.As(err, &rl) ||
		rl.Seconds() != 3600 {
		t.Fatalf("the fourth request for one account = %v", err)
	}
	if st := f.startCode(t, alice, "bob@example.org"); st.RequestPublicID == "" {
		t.Error("another account is not limited")
	}
	// Requests stored meanwhile by concurrent ones refuse the request in its transaction.
	g := newEnv(t)
	g.br.onFind = func() {
		for range 5 {
			g.store.reqs = append(g.store.reqs, fReq{id: g.store.id(), userID: aliceID, messenger: MessengerBridge,
				method: text(MethodCode), connID: brID, target: text("u-x"), created: g.clock.Now()})
		}
	}
	if _, err := g.s.StartBridgeCode(t.Context(), alice, brPID, "bob@example.org"); !errors.As(err, &rl) ||
		len(g.br.sent) != 0 {
		t.Errorf("a request past the User's limit reached meanwhile = %v", err)
	}
}

// TestBridgeCodeRefusals: a Connection whose info offers no code is ErrCodeNotOffered; an unknown handle, an empty one
// and a Connection that is not a Bridge one are field errors; an account linked to another User is ErrLinkedElsewhere,
// recorded as link_rejected with conflict; a direct message the bridge refuses as forbidden is direct_message_refused
// and any other failure is returned, both voiding the request; no limiter token stores nothing.
func TestBridgeCodeRefusals(t *testing.T) {
	e := newEnv(t)
	for _, o := range []BridgeOffer{{}, {Read: true, UserLookup: LookupNone, BotCanStart: true},
		{Read: true, UserLookup: LookupEmail}, {UserLookup: LookupEmail, BotCanStart: true}} {
		e.br.offer = o
		if _, err := e.s.StartBridgeCode(t.Context(), alice, brPID, "bob@example.org"); !errors.Is(err,
			ErrCodeNotOffered) || len(e.store.reqs) != 0 || len(e.br.sent) != 0 {
			t.Errorf("offer %+v = %v", o, err)
		}
	}
	e.br.offer = BridgeOffer{Read: true, UserLookup: LookupUserID, BotCanStart: true}
	var fe *FieldError
	if _, err := e.s.StartBridgeCode(t.Context(), alice, brPID, "nobody"); !errors.As(err, &fe) ||
		fe.Pointer != "/handle" || fe.Code != CodeHandleNotFound || fe.Detail != "No account with this user id was found." {
		t.Errorf("an unknown handle = %v", err)
	}
	e.br.offer = newFakeBR().offer
	for _, c := range []struct{ conn, handle, pointer, code string }{
		{brPID, "nobody@example.org", "/handle", CodeHandleNotFound},
		{brPID, "  ", "/handle", CodeRequired},
		{mmPID, "bob@example.org", "/connection_id", CodeUnknownID},
		{brGonePID, "bob@example.org", "/connection_id", CodeUnknownID},
	} {
		if _, err := e.s.StartBridgeCode(t.Context(), alice, c.conn, c.handle); !errors.As(err, &fe) ||
			fe.Pointer != c.pointer || fe.Code != c.code {
			t.Errorf("StartBridgeCode(%s, %q) = %v", c.conn, c.handle, err)
		}
	}
	e.addLink(bobID, MessengerBridge, brID, "u-bob", nil)
	if _, err := e.s.StartBridgeCode(t.Context(), alice, brPID, "bob@example.org"); !errors.Is(err,
		ErrLinkedElsewhere) || len(e.store.reqs) != 0 {
		t.Fatalf("a conflict = %v", err)
	}
	if got := e.lastEntry(); got.Action != ActionLinkRejected || got.Details["reason"] != ReasonConflict ||
		got.Details["external_id"] != "u-bob" || got.Details["user"] != alicePID || got.Actor != "user:1" ||
		got.Transport != "ui" || got.Address != "192.0.2.1" || got.Details["display_name"] != "Bob Brown" {
		t.Errorf("entry %+v", got)
	}
	if !strings.Contains(e.log.String(), `"event":"account_link_rejected","messenger":"bridge","connection":"`+brPID+
		`","reason":"conflict"`) {
		t.Errorf("log %s", e.log)
	}
	limited := errors.New("no limiter token")
	e.br.sessionErr = limited
	if _, err := e.s.StartBridgeCode(t.Context(), alice, brPID, "dana@example.org"); !errors.Is(err, limited) ||
		len(e.store.reqs) != 0 {
		t.Errorf("no token = %v", err)
	}
	e.br.sessionErr = ErrNoConnection
	if _, err := e.s.StartBridgeCode(t.Context(), alice, brPID, "dana@example.org"); !errors.As(err, &fe) ||
		fe.Code != CodeUnknownID || fe.Detail != "No Bridge Connection has this id." {
		t.Errorf("a Connection that went away = %v", err)
	}
	e.br.sessionErr = nil
	e.br.findErr = &MessengerError{Call: "findUsers", Outcome: "invalid", Status: 400}
	if _, err := e.s.StartBridgeCode(t.Context(), alice, brPID, "dana@example.org"); !errors.As(err,
		new(*MessengerError)) {
		t.Errorf("a refused lookup = %v", err)
	}
	e.br.findErr = nil
	e.br.sendErr = ErrDirectMessageRefused
	_, err := e.s.StartBridgeCode(t.Context(), alice, brPID, "dana@example.org")
	if !errors.As(err, &fe) || fe.Pointer != "/handle" || fe.Code != CodeDirectMessageRefused ||
		fe.Detail != "The bot cannot send you a direct message. Send it the token instead." || len(e.store.reqs) != 1 ||
		e.store.reqs[0].voided == nil || strings.Contains(err.Error(), e.br.code(t)) {
		t.Errorf("a refused direct message = %v, %+v", err, e.store.reqs)
	}
	e.br.sendErr = &MessengerError{Call: "sendDirectMessage", Outcome: "transient", Status: 502}
	if _, err := e.s.StartBridgeCode(t.Context(), alice, brPID, "dana@example.org"); !errors.As(err,
		new(*MessengerError)) || e.store.reqs[1].voided == nil {
		t.Errorf("a failed direct message = %v", err)
	}
	e.store.fail["VoidLinkRequest"] = errBoom
	if _, err := e.s.StartBridgeCode(t.Context(), alice, brPID, "dana@example.org"); !errors.Is(err, errBoom) ||
		!errors.As(err, new(*MessengerError)) {
		t.Errorf("a failed void = %v", err)
	}
	e.br.sendErr = nil
	for _, name := range []string{"CountUserBridgeCodeRequests", "CountTargetBridgeCodeRequests", "LockCodeTarget",
		"LockLinkUser", "GetAccountLinkBySpace", "VoidPendingBridgeCodeRequests", "InsertLinkRequest",
		"InsertAuditEntry", "GetLinkConnection"} {
		f := newEnv(t)
		f.addLink(bobID, MessengerBridge, brID, "u-bob", nil)
		f.store.fail = map[string]error{name: errBoom}
		handle := "dana@example.org"
		if name == "InsertAuditEntry" {
			handle = "bob@example.org"
		}
		if _, err := f.s.StartBridgeCode(t.Context(), alice, brPID, handle); !errors.Is(err, errBoom) {
			t.Errorf("a failed %s = %v", name, err)
		}
	}
	e.store.fail = map[string]error{}
	e.s.cfg.Random = bytes.NewReader(nil)
	if _, err := e.s.StartBridgeCode(t.Context(), alice, brPID, "dana@example.org"); err == nil {
		t.Error("a code without randomness")
	}
}

// TestConfirmBridgeRefusals: a request of another User, a Mattermost or a Bridge token request and an unknown one are
// ErrNotFound; a request whose Connection was deleted takes no code and records nothing; an account linked meanwhile
// to another User is ErrLinkedElsewhere; a database failure links nothing.
func TestConfirmBridgeRefusals(t *testing.T) {
	e := newEnv(t)
	st := e.startCode(t, alice, "dana@example.org")
	code := e.br.code(t)
	mm := e.startMM(t, alice, "dana")
	e.startBR(t, alice, brPID)
	tokReq := e.store.reqs[len(e.store.reqs)-1].publicID
	for _, c := range []struct {
		r  Requester
		id string
	}{{bob, st.RequestPublicID}, {alice, mm.RequestPublicID}, {alice, tokReq}, {alice, "ARAAAAAAAAAAZZ"},
		{alice, "bad"}} {
		if _, err := e.s.ConfirmBridge(t.Context(), c.r, c.id, code); !errors.Is(err, ErrNotFound) {
			t.Errorf("Confirm(%s) = %v", c.id, err)
		}
	}
	if _, err := e.s.ConfirmMattermost(t.Context(), alice, st.RequestPublicID, code); !errors.Is(err, ErrNotFound) {
		t.Errorf("a Bridge request confirmed as a Mattermost one = %v", err)
	}
	e.addLink(bobID, MessengerBridge, brID, "u-dana", nil)
	if _, err := e.s.ConfirmBridge(t.Context(), alice, st.RequestPublicID, code); !errors.Is(err,
		ErrLinkedElsewhere) || e.lastEntry().Details["reason"] != ReasonConflict || e.store.reqs[0].consumed != nil {
		t.Fatalf("a conflict = %v", err)
	}
	e.store.links = nil
	for _, name := range []string{"LockLinkRequest", "LockLinkUser", "RecordWrongCode", "InsertAuditEntry",
		"ConsumeLinkRequest", "GetLinkRequest"} {
		e.store.fail = map[string]error{name: errBoom}
		c := code
		if name == "RecordWrongCode" {
			c = "22222222"
		}
		if _, err := e.s.ConfirmBridge(t.Context(), alice, st.RequestPublicID, c); !errors.Is(err, errBoom) ||
			len(e.store.links) != 0 {
			t.Errorf("a failed %s = %v", name, err)
		}
	}
	e.store.fail = map[string]error{}
	before := len(e.store.audit)
	e.store.conns[slices.IndexFunc(e.store.conns, func(c fConn) bool { return c.id == brID })].deleted = true
	if _, err := e.s.ConfirmBridge(t.Context(), alice, st.RequestPublicID, code); !linkCodeInvalid(err) ||
		attemptsLeft(err) != 0 || len(e.store.audit) != before || len(e.store.links) != 0 {
		t.Errorf("a deleted Connection = %v", err)
	}
}

// TestBridgeConfirmConcurrentConflict is C-18.FR-5 for a Bridge account that another User linked after the check: the
// confirmation is ErrLinkedElsewhere, the request kept, so that its code still links.
func TestBridgeConfirmConcurrentConflict(t *testing.T) {
	e := newEnv(t)
	st := e.startCode(t, alice, "dana@example.org")
	code := e.br.code(t)
	e.store.fail["InsertAccountLink"] = fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: uniqueViolation,
		ConstraintName: accountKey})
	if _, err := e.s.ConfirmBridge(t.Context(), alice, st.RequestPublicID, code); !errors.Is(err,
		ErrLinkedElsewhere) || e.store.reqs[0].consumed != nil || e.store.reqs[0].attempts != 0 {
		t.Fatalf("a concurrent conflict = %v", err)
	}
	delete(e.store.fail, "InsertAccountLink")
	if l, err := e.s.ConfirmBridge(t.Context(), alice, st.RequestPublicID, code); err != nil || l.ExternalID != "u-dana" ||
		*l.DisplayName != "Dana" {
		t.Errorf("the kept request = %+v, %v", l, err)
	}
}

// TestBridgeOffer is C-22.FR-15: the token is always offered, the code only with an info that was read, declares a
// user lookup other than none and a bot that can start a direct chat; the kind of handle goes with the code.
func TestBridgeOffer(t *testing.T) {
	for _, c := range []struct {
		o       BridgeOffer
		methods []string
		kind    string
	}{
		{BridgeOffer{}, []string{MethodToken}, ""},
		{BridgeOffer{Read: true, UserLookup: LookupNone, BotCanStart: true}, []string{MethodToken}, ""},
		{BridgeOffer{Read: true, UserLookup: LookupLogin}, []string{MethodToken}, ""},
		{BridgeOffer{UserLookup: LookupLogin, BotCanStart: true}, []string{MethodToken}, ""},
		{BridgeOffer{Read: true, UserLookup: "phone", BotCanStart: true}, []string{MethodToken}, ""},
		{BridgeOffer{Read: true, UserLookup: LookupUsername, BotCanStart: true}, []string{MethodToken, MethodCode},
			LookupUsername},
	} {
		if !slices.Equal(c.o.Methods(), c.methods) || c.o.HandleKind() != c.kind || c.o.Code() != (c.kind != "") {
			t.Errorf("%+v: %v %q", c.o, c.o.Methods(), c.o.HandleKind())
		}
	}
}

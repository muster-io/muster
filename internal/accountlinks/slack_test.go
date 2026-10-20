// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package accountlinks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/accountlinks/dbgen"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/logging"
)

func (s *fakeStore) VoidPendingSlackRequests(_ context.Context, a dbgen.VoidPendingSlackRequestsParams) (int64,
	error) {
	if err := s.err("VoidPendingSlackRequests"); err != nil {
		return 0, err
	}
	s.locks = append(s.locks, "requests")
	var n int64
	for i := range s.reqs {
		r := &s.reqs[i]
		if a.OrgID == org && r.userID == a.UserID && r.connID == a.ConnectionID && r.messenger == MessengerSlack &&
			r.consumed == nil && r.voided == nil {
			now := a.Now
			r.voided = &now
			n++
		}
	}
	return n, nil
}

func (s *fakeStore) CountUserSlackCodeRequests(_ context.Context, a dbgen.CountUserSlackCodeRequestsParams) (
	dbgen.CountUserSlackCodeRequestsRow, error) {
	if err := s.err("CountUserSlackCodeRequests"); err != nil {
		return dbgen.CountUserSlackCodeRequestsRow{}, err
	}
	s.mark("slack user")
	n, oldest := s.count(MessengerSlack, func(r fReq) bool { return a.OrgID == org && r.userID == a.UserID }, a.Since)
	return dbgen.CountUserSlackCodeRequestsRow{Requests: n, Oldest: oldest}, nil
}

func (s *fakeStore) CountTargetSlackCodeRequests(_ context.Context, a dbgen.CountTargetSlackCodeRequestsParams) (
	dbgen.CountTargetSlackCodeRequestsRow, error) {
	if err := s.err("CountTargetSlackCodeRequests"); err != nil {
		return dbgen.CountTargetSlackCodeRequestsRow{}, err
	}
	s.mark("slack target")
	n, oldest := s.count(MessengerSlack, func(r fReq) bool {
		return a.OrgID == org && r.connID == a.ConnectionID && r.target == a.TargetExternalID
	}, a.Since)
	return dbgen.CountTargetSlackCodeRequestsRow{Requests: n, Oldest: oldest}, nil
}

// slDM is a direct message the fake Slack bot sent, with the code it was given as a secret, if any.
type slDM struct {
	user, text string
	code       logging.Secret
}

// fakeSL is the bot of a Slack Connection: the accounts it finds by lower-cased email or name, the handles it was
// asked for, and the direct messages it sent; each error makes its call fail.
type fakeSL struct {
	users      map[string]SlackAccount
	finds      []string
	sent       []slDM
	sessions   []string
	sessionErr error
	findErr    error
	sendErr    error
	linkedErr  error
	onFind     func()
}

func newFakeSL() *fakeSL {
	return &fakeSL{users: map[string]SlackAccount{
		"alice@example.org": {ID: "U0ALICE", Name: "alice", DisplayName: "Alice Smith"},
		"bob":               {ID: "U0BOB", Name: "bob", DisplayName: "Bob Jones"},
		"dana":              {ID: "U0DANA", Name: "dana"},
	}}
}

func (f *fakeSL) Session(ctx context.Context, id string, fn func(context.Context, SlackBot) error) error {
	if f.sessionErr != nil {
		return f.sessionErr
	}
	f.sessions = append(f.sessions, id)
	return fn(ctx, f)
}

func (f *fakeSL) FindUser(_ context.Context, handle string) (SlackAccount, error) {
	f.finds = append(f.finds, handle)
	if f.onFind != nil {
		f.onFind()
	}
	if f.findErr != nil {
		return SlackAccount{}, f.findErr
	}
	if strings.EqualFold(handle, "sam") {
		return SlackAccount{}, ErrHandleAmbiguous
	}
	a, ok := f.users[strings.ToLower(strings.TrimPrefix(handle, "@"))]
	if !ok {
		return SlackAccount{}, ErrMessengerUserNotFound
	}
	return a, nil
}

func (f *fakeSL) SendCode(_ context.Context, user string, code logging.Secret, text string) error {
	f.sent = append(f.sent, slDM{user: user, text: text, code: code})
	return f.sendErr
}

func (f *fakeSL) SendLinked(_ context.Context, user, text string) error {
	f.sent = append(f.sent, slDM{user: user, text: text})
	return f.linkedErr
}

// code is the code of the last direct message, which must be the secret it was sent with.
func (f *fakeSL) code(t *testing.T) string {
	t.Helper()
	if len(f.sent) == 0 {
		t.Fatal("no direct message")
	}
	last := f.sent[len(f.sent)-1]
	c := codeRe.FindString(last.text)
	if c == "" || string(last.code) != c {
		t.Fatalf("no code in %q, or not as its secret %q", last.text, last.code)
	}
	return c
}

// startSL is a started Slack link of r through the Slack Connection sl to the account with handle.
func (e *env) startSL(t *testing.T, r Requester, handle string) SlackStarted {
	t.Helper()
	st, err := e.s.StartSlack(t.Context(), r, slPID, handle)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// TestSlackLink is C-24.FR-12 and AC-14: the account found by an email in other letter case gets a code with the
// warning by direct message, in the requesting User's language, with the code as the secret of the call; the request
// stores the account's user id, name and display name, and none of a method; the right code, in lower case, links it
// with them, recorded with the messenger slack, and the bot confirms "Linked to Muster user {name}" in the User's
// language; an account without a display name is linked without one.
func TestSlackLink(t *testing.T) {
	e := newEnv(t)
	st := e.startSL(t, alice, " Alice@Example.ORG ")
	if !strings.HasPrefix(st.RequestPublicID, "AR") || !st.ExpiresAt.Equal(t0.Add(10*time.Minute)) ||
		st.AttemptsRemaining != 5 || len(e.sl.sent) != 1 || e.sl.sent[0].user != "U0ALICE" ||
		!slices.Equal(e.sl.sessions, []string{slPID}) || !slices.Equal(e.sl.finds, []string{"Alice@Example.ORG"}) ||
		!strings.HasPrefix(e.sl.sent[0].text, "You requested a link to Muster. Do not share this code.\n\n") {
		t.Fatalf("started %+v, sent %+v, finds %q", st, e.sl.sent, e.sl.finds)
	}
	if !slices.Equal(e.store.locks, []string{"user", "requests"}) {
		t.Errorf("locked %q", e.store.locks)
	}
	code := e.sl.code(t)
	sum := sha256.Sum256([]byte(code))
	r := e.store.reqs[0]
	if !bytes.Equal(r.hash, sum[:]) || r.target.String != "U0ALICE" || r.targetUsername.String != "alice" ||
		r.targetDisplayName.String != "Alice Smith" || r.messenger != MessengerSlack || r.method.Valid ||
		r.connID != slID || !r.expires.Equal(t0.Add(SlackCodeTTL)) || strings.Contains(e.log.String(), code) {
		t.Fatalf("stored %+v", r)
	}
	e.store.locks = nil
	l, err := e.s.ConfirmSlack(t.Context(), alice, st.RequestPublicID, strings.ToLower(code))
	if !slices.Equal(e.store.locks, []string{"user", "request"}) {
		t.Errorf("locked %q: the User first, as a new request does", e.store.locks)
	}
	if err != nil || l.Messenger != MessengerSlack || l.ExternalID != "U0ALICE" || *l.Username != "alice" ||
		*l.DisplayName != "Alice Smith" || l.ConnectionPublicID != slPID || l.ConnectionName != "sl" ||
		!strings.HasPrefix(l.PublicID, "AK") || len(e.sl.sessions) != 2 {
		t.Fatalf("Confirm = %+v, %v", l, err)
	}
	if got := e.sl.sent[1]; got.user != "U0ALICE" || got.text != "Linked to Muster user Alice Smith" || got.code != "" {
		t.Errorf("the confirmation %+v", got)
	}
	if got := e.lastEntry(); got.Action != ActionLink || got.Transport != "ui" || got.Address != "192.0.2.1" ||
		got.Details["messenger"] != MessengerSlack || got.Details["display_name"] != "Alice Smith" ||
		got.Details["username"] != "alice" || got.Details["external_id"] != "U0ALICE" ||
		e.store.reqs[0].consumed == nil || len(e.store.hints) != 1 || e.store.links[0].space() != "slack:18" {
		t.Errorf("entry %+v", got)
	}
	if !strings.Contains(e.log.String(), `"event":"account_link_linked","messenger":"slack","connection":"`+slPID+
		`","user":"`+alicePID+`"}`) || strings.Contains(e.log.String(), code) {
		t.Errorf("log %s", e.log)
	}
	// Bob's profile is in Russian; Dana's account has no display name.
	st = e.startSL(t, bob, "@BOB")
	if !strings.HasPrefix(e.sl.sent[2].text, "Вы запросили связывание учётной записи с Muster.") {
		t.Errorf("the code in Russian %q", e.sl.sent[2].text)
	}
	if _, err := e.s.ConfirmSlack(t.Context(), bob, st.RequestPublicID, e.sl.code(t)); err != nil ||
		e.sl.sent[3].text != "Учётная запись связана с пользователем Muster Bob" {
		t.Errorf("Bob's link = %v, %+v", err, e.sl.sent)
	}
	st = e.startSL(t, alice, "dana")
	if r := e.store.reqs[len(e.store.reqs)-1]; r.targetDisplayName.Valid || r.targetUsername.String != "dana" {
		t.Errorf("a request without a display name %+v", r)
	}
	l, err = e.s.ConfirmSlack(t.Context(), alice, st.RequestPublicID, e.sl.code(t))
	if err != nil || l.DisplayName != nil || *l.Username != "dana" {
		t.Errorf("a link without a display name = %+v, %v", l, err)
	}
}

// TestSlackLinkAttempts is C-24.AC-14 and account_link.slack_code: each wrong code says how many attempts are left and
// is recorded with wrong_code; after five the sixth attempt is refused even with the right code, with none left; an
// expired and a used request have none either.
func TestSlackLinkAttempts(t *testing.T) {
	e := newEnv(t)
	st := e.startSL(t, alice, "bob")
	code := e.sl.code(t)
	for i := range 5 {
		_, err := e.s.ConfirmSlack(t.Context(), alice, st.RequestPublicID, "22222222")
		if !linkCodeInvalid(err) || attemptsLeft(err) != 4-i || e.lastEntry().Details["reason"] != ReasonWrongCode ||
			e.lastEntry().Details["messenger"] != MessengerSlack {
			t.Fatalf("wrong code %d = %v, %d left", i+1, err, attemptsLeft(err))
		}
	}
	if req := e.store.reqs[0]; req.attempts != SlackMaxAttempts || req.voided == nil {
		t.Fatalf("after five wrong codes %+v", req)
	}
	if _, err := e.s.ConfirmSlack(t.Context(), alice, st.RequestPublicID, code); !linkCodeInvalid(err) ||
		attemptsLeft(err) != 0 || len(e.store.links) != 0 || e.lastEntry().Details["reason"] != ReasonWrongCode {
		t.Errorf("the sixth attempt with the right code = %v, links %+v", err, e.store.links)
	}
	if !strings.Contains(e.log.String(), `"event":"account_link_rejected","messenger":"slack","connection":"`+slPID+
		`","reason":"wrong_code"`) || strings.Contains(e.log.String(), code) {
		t.Errorf("log %s", e.log)
	}
	st = e.startSL(t, alice, "bob")
	code = e.sl.code(t)
	e.clock.Advance(SlackCodeTTL)
	if _, err := e.s.ConfirmSlack(t.Context(), alice, st.RequestPublicID, code); !linkCodeInvalid(err) ||
		attemptsLeft(err) != 0 || e.lastEntry().Details["reason"] != ReasonTokenExpired {
		t.Errorf("an expired code = %v", err)
	}
	st = e.startSL(t, alice, "bob")
	code = e.sl.code(t)
	if _, err := e.s.ConfirmSlack(t.Context(), alice, st.RequestPublicID, code); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.ConfirmSlack(t.Context(), alice, st.RequestPublicID, code); !linkCodeInvalid(err) ||
		attemptsLeft(err) != 0 || e.lastEntry().Details["reason"] != ReasonTokenUsed {
		t.Errorf("a used request = %v", err)
	}
}

// TestSlackRequestLimits is C-24.FR-12 with account_link.slack_code_requests on a manual clock: the sixth request of
// one User within an hour and the fourth for one target account are refused until the oldest counted request is an
// hour old, counted before the account is looked up and again in the transaction under the lock of the account; a new
// request voids the User's earlier pending one through the Connection; a request whose direct message failed still
// counts; requests of other messengers are counted apart.
func TestSlackRequestLimits(t *testing.T) {
	e := newEnv(t)
	e.startSL(t, alice, "dana")
	if want := []string{"slack user", "slack user in tx", "lock 18/U0DANA in tx", "slack target in tx"}; !slices.Equal(
		e.store.counted, want) {
		t.Fatalf("counted %q, want %q", e.store.counted, want)
	}
	e.clock.Advance(time.Minute)
	e.sl.sendErr = ErrDirectMessageRefused
	if _, err := e.s.StartSlack(t.Context(), alice, slPID, "bob"); err == nil {
		t.Fatal("a refused direct message")
	}
	e.sl.sendErr = nil
	for i := range 3 {
		e.clock.Advance(time.Minute)
		e.startSL(t, alice, []string{"bob", "alice@example.org", "dana"}[i])
	}
	if n := len(e.store.reqs); e.store.reqs[0].voided == nil || e.store.reqs[n-1].voided != nil {
		t.Fatal("a new request did not void the earlier one")
	}
	var rl *RateLimitedError
	e.sl.onFind = func() { t.Error("an account looked up past the limit") }
	if _, err := e.s.StartSlack(t.Context(), alice, slPID, "bob"); !errors.As(err, &rl) ||
		rl.RetryAfter != 56*time.Minute || len(e.store.reqs) != 5 || len(e.sl.sessions) != 5 {
		t.Fatalf("the sixth request = %v, %d sessions", err, len(e.sl.sessions))
	}
	e.sl.onFind = nil
	if st := e.startMM(t, alice, "bob"); st.RequestPublicID == "" {
		t.Error("a Mattermost request is limited by Slack requests")
	}
	f := newEnv(t)
	for range 3 {
		f.startSL(t, bob, "dana")
	}
	if _, err := f.s.StartSlack(t.Context(), alice, slPID, "dana"); !errors.As(err, &rl) || rl.Seconds() != 3600 {
		t.Fatalf("the fourth request for one account = %v", err)
	}
	if st := f.startSL(t, alice, "bob"); st.RequestPublicID == "" {
		t.Error("another account is not limited")
	}
	if _, err := f.s.StartSlack(t.Context(), alice, sl2PID, "dana"); err != nil {
		t.Errorf("the account of another Connection is limited = %v", err)
	}
	// Requests stored meanwhile by concurrent ones refuse the request in its transaction.
	g := newEnv(t)
	g.sl.onFind = func() {
		for range 5 {
			g.store.reqs = append(g.store.reqs, fReq{id: g.store.id(), userID: aliceID, messenger: MessengerSlack,
				connID: slID, target: text("U0X"), created: g.clock.Now()})
		}
	}
	if _, err := g.s.StartSlack(t.Context(), alice, slPID, "bob"); !errors.As(err, &rl) || len(g.sl.sent) != 0 {
		t.Errorf("a request past the User's limit reached meanwhile = %v", err)
	}
}

// TestSlackStartRefusals is C-24.FR-12: an empty handle, a Connection that is not a Slack one or is deleted, no
// account and several accounts with the name are field errors with their texts; an account linked to another User is
// ErrLinkedElsewhere, recorded as link_rejected with conflict; a direct message Slack refuses for the account is
// direct_message_refused and any other failure is returned, both voiding the request; no limiter token stores nothing,
// and a Connection that went away is unknown_id.
func TestSlackStartRefusals(t *testing.T) {
	e := newEnv(t)
	var fe *FieldError
	for _, c := range []struct{ conn, handle, pointer, code, detail string }{
		{slPID, "  ", "/handle", CodeRequired, "The email or name is empty."},
		{slPID, " @ ", "/handle", CodeRequired, "The email or name is empty."},
		{slPID, "nobody", "/handle", CodeHandleNotFound, "No Slack account with this email or name was found."},
		{slPID, "Sam", "/handle", CodeHandleAmbiguous, "Several Slack accounts have this name. Enter the email instead."},
		{mmPID, "bob", "/connection_id", CodeUnknownID, "No Slack Connection has this id."},
		{slGonePID, "bob", "/connection_id", CodeUnknownID, "No Slack Connection has this id."},
		{"bad", "bob", "/connection_id", CodeUnknownID, "No Slack Connection has this id."},
	} {
		if _, err := e.s.StartSlack(t.Context(), alice, c.conn, c.handle); !errors.As(err, &fe) ||
			fe.Pointer != c.pointer || fe.Code != c.code || fe.Detail != c.detail || len(e.store.reqs) != 0 {
			t.Errorf("StartSlack(%s, %q) = %v", c.conn, c.handle, err)
		}
	}
	e.addLink(bobID, MessengerSlack, slID, "U0BOB", ptr("bob"))
	if _, err := e.s.StartSlack(t.Context(), alice, slPID, "bob"); !errors.Is(err, ErrLinkedElsewhere) ||
		len(e.store.reqs) != 0 || len(e.sl.sent) != 0 {
		t.Fatalf("a conflict = %v", err)
	}
	if got := e.lastEntry(); got.Action != ActionLinkRejected || got.Details["reason"] != ReasonConflict ||
		got.Details["external_id"] != "U0BOB" || got.Details["messenger"] != MessengerSlack ||
		got.Details["user"] != alicePID || got.Transport != "ui" || got.Details["display_name"] != "Bob Jones" {
		t.Errorf("entry %+v", got)
	}
	if !strings.Contains(e.log.String(), `"event":"account_link_rejected","messenger":"slack","connection":"`+slPID+
		`","reason":"conflict"`) {
		t.Errorf("log %s", e.log)
	}
	limited := errors.New("no limiter token")
	e.sl.sessionErr = limited
	if _, err := e.s.StartSlack(t.Context(), alice, slPID, "dana"); !errors.Is(err, limited) || len(e.store.reqs) != 0 {
		t.Errorf("no token = %v", err)
	}
	e.sl.sessionErr = ErrNoConnection
	if _, err := e.s.StartSlack(t.Context(), alice, slPID, "dana"); !errors.As(err, &fe) ||
		fe.Code != CodeUnknownID || fe.Detail != "No Slack Connection has this id." {
		t.Errorf("a Connection that went away = %v", err)
	}
	e.sl.sessionErr = nil
	e.sl.findErr = &MessengerError{Call: "users.list", Outcome: "fatal", Status: 200}
	if _, err := e.s.StartSlack(t.Context(), alice, slPID, "dana"); !errors.As(err, new(*MessengerError)) {
		t.Errorf("a refused lookup = %v", err)
	}
	e.sl.findErr = nil
	e.sl.sendErr = ErrDirectMessageRefused
	_, err := e.s.StartSlack(t.Context(), alice, slPID, "dana")
	if !errors.As(err, &fe) || fe.Pointer != "/handle" || fe.Code != CodeDirectMessageRefused ||
		fe.Detail != "The bot cannot send a direct message to this Slack account." || len(e.store.reqs) != 1 ||
		e.store.reqs[0].voided == nil || strings.Contains(err.Error(), e.sl.code(t)) {
		t.Errorf("a refused direct message = %v, %+v", err, e.store.reqs)
	}
	e.sl.sendErr = &MessengerError{Call: "the direct message", Outcome: "transient", Status: 502}
	if _, err := e.s.StartSlack(t.Context(), alice, slPID, "dana"); !errors.As(err, new(*MessengerError)) ||
		e.store.reqs[1].voided == nil {
		t.Errorf("a failed direct message = %v", err)
	}
	e.store.fail["VoidLinkRequest"] = errBoom
	if _, err := e.s.StartSlack(t.Context(), alice, slPID, "dana"); !errors.Is(err, errBoom) ||
		!errors.As(err, new(*MessengerError)) {
		t.Errorf("a failed void = %v", err)
	}
	e.sl.sendErr = nil
	for _, name := range []string{"CountUserSlackCodeRequests", "CountTargetSlackCodeRequests", "LockCodeTarget",
		"LockLinkUser", "GetAccountLinkBySpace", "VoidPendingSlackRequests", "InsertLinkRequest", "InsertAuditEntry",
		"GetLinkConnection"} {
		f := newEnv(t)
		f.addLink(bobID, MessengerSlack, slID, "U0BOB", nil)
		f.store.fail = map[string]error{name: errBoom}
		handle := "dana"
		if name == "InsertAuditEntry" {
			handle = "bob"
		}
		if _, err := f.s.StartSlack(t.Context(), alice, slPID, handle); !errors.Is(err, errBoom) {
			t.Errorf("a failed %s = %v", name, err)
		}
	}
	e.store.fail = map[string]error{}
	e.s.cfg.Random = bytes.NewReader(nil)
	if _, err := e.s.StartSlack(t.Context(), alice, slPID, "dana"); err == nil {
		t.Error("a code without randomness")
	}
}

// TestCodeAttempts is account_link.bridge_code and account_link.slack_code: a code takes five attempts through a
// Bridge and through Slack, and the code of a messenger without attempts is an error before anything is read or
// written — even the right code links nothing, takes no attempt and records nothing.
func TestCodeAttempts(t *testing.T) {
	if len(codeAttempts) != 2 || codeAttempts[MessengerBridge] != 5 || codeAttempts[MessengerSlack] != 5 {
		t.Errorf("code attempts %v", codeAttempts)
	}
	e := newEnv(t)
	e.startSL(t, alice, "bob")
	code := e.sl.code(t)
	req, audits, locks := e.store.reqs[0], len(e.store.audit), len(e.store.locks)
	for _, m := range []string{MessengerMattermost, MessengerTelegram, ""} {
		a := account{messenger: m, conn: connRef{id: slID, publicID: slPID}, space: SpaceSlack(slID),
			external: req.target.String}
		out, err := e.s.checkCode(t.Context(), alice, req.id, a, code)
		if err == nil || !strings.Contains(err.Error(), "no code attempts") || out != (codeOutcome{}) {
			t.Errorf("a %q code = %+v, %v", m, out, err)
		}
	}
	if got := e.store.reqs[0]; len(e.store.links) != 0 || got.attempts != 0 || got.consumed != nil ||
		got.voided != nil || len(e.store.audit) != audits || len(e.store.locks) != locks {
		t.Errorf("a code without attempts changed %+v, links %+v", got, e.store.links)
	}
}

// TestSlackLookupLimited is C-24.FR-12 and F-099: a lookup Slack limits, its *delivery.LimitedError, is returned as
// it is, for the API to answer 503 with its Retry-After; it stores no request, so it counts toward no limit of
// account_link.slack_code_requests, and sends nothing.
func TestSlackLookupLimited(t *testing.T) {
	e := newEnv(t)
	e.sl.findErr = &delivery.LimitedError{RetryAfter: 30 * time.Second}
	var le *delivery.LimitedError
	if _, err := e.s.StartSlack(t.Context(), alice, slPID, "dana"); !errors.As(err, &le) ||
		le.RetryAfter != 30*time.Second || len(e.store.reqs) != 0 || len(e.sl.sent) != 0 {
		t.Errorf("a lookup Slack limits = %v, %+v", err, e.store.reqs)
	}
	e.sl.findErr = nil
	if st := e.startSL(t, alice, "dana"); st.AttemptsRemaining != SlackMaxAttempts || len(e.store.reqs) != 1 {
		t.Errorf("the request after it %+v", st)
	}
}

// TestConfirmSlackRefusals: a request of another User, a Mattermost or a Bridge code request and an unknown one are
// ErrNotFound, and a Slack request is no Mattermost or Bridge one; an account linked meanwhile to another User is
// ErrLinkedElsewhere and keeps the request; no limiter token is returned; a request whose Connection was deleted or
// went away takes no code and records nothing; a confirmation the bot fails to send leaves the link and is logged; a
// database failure links nothing.
func TestConfirmSlackRefusals(t *testing.T) {
	e := newEnv(t)
	st := e.startSL(t, alice, "dana")
	code := e.sl.code(t)
	mm := e.startMM(t, alice, "dana")
	br := e.startCode(t, alice, "dana@example.org")
	for _, c := range []struct {
		r  Requester
		id string
	}{{bob, st.RequestPublicID}, {alice, mm.RequestPublicID}, {alice, br.RequestPublicID}, {alice, "ARAAAAAAAAAAZZ"},
		{alice, "bad"}} {
		if _, err := e.s.ConfirmSlack(t.Context(), c.r, c.id, code); !errors.Is(err, ErrNotFound) {
			t.Errorf("ConfirmSlack(%s) = %v", c.id, err)
		}
	}
	if _, err := e.s.ConfirmMattermost(t.Context(), alice, st.RequestPublicID, code); !errors.Is(err, ErrNotFound) {
		t.Errorf("a Slack request confirmed as a Mattermost one = %v", err)
	}
	if _, err := e.s.ConfirmBridge(t.Context(), alice, st.RequestPublicID, code); !errors.Is(err, ErrNotFound) {
		t.Errorf("a Slack request confirmed as a Bridge one = %v", err)
	}
	e.addLink(bobID, MessengerSlack, slID, "U0DANA", nil)
	if _, err := e.s.ConfirmSlack(t.Context(), alice, st.RequestPublicID, code); !errors.Is(err,
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
		if _, err := e.s.ConfirmSlack(t.Context(), alice, st.RequestPublicID, c); !errors.Is(err, errBoom) ||
			len(e.store.links) != 0 {
			t.Errorf("a failed %s = %v", name, err)
		}
	}
	e.store.fail = map[string]error{}
	limited := errors.New("no limiter token")
	e.sl.sessionErr = limited
	if _, err := e.s.ConfirmSlack(t.Context(), alice, st.RequestPublicID, code); !errors.Is(err, limited) ||
		len(e.store.links) != 0 {
		t.Errorf("no token = %v", err)
	}
	before := len(e.store.audit)
	e.sl.sessionErr = ErrNoConnection
	if _, err := e.s.ConfirmSlack(t.Context(), alice, st.RequestPublicID, code); !linkCodeInvalid(err) ||
		attemptsLeft(err) != 0 || len(e.store.audit) != before {
		t.Errorf("a Connection that went away = %v", err)
	}
	e.sl.sessionErr = nil
	e.sl.linkedErr = &MessengerError{Call: "the confirmation", Outcome: "transient", Status: 502}
	if l, err := e.s.ConfirmSlack(t.Context(), alice, st.RequestPublicID, code); err != nil || l.ExternalID != "U0DANA" {
		t.Fatalf("a link whose confirmation failed = %+v, %v", l, err)
	}
	if !strings.Contains(e.log.String(), `"event":"account_link_linked","messenger":"slack","connection":"`+slPID+
		`","user":"`+alicePID+`","error":"the messenger refused the confirmation (transient, status 502)"}`) {
		t.Errorf("log %s", e.log)
	}
	f := newEnv(t)
	st = f.startSL(t, alice, "dana")
	f.store.conns[slices.IndexFunc(f.store.conns, func(c fConn) bool { return c.id == slID })].deleted = true
	if _, err := f.s.ConfirmSlack(t.Context(), alice, st.RequestPublicID, f.sl.code(t)); !linkCodeInvalid(err) ||
		attemptsLeft(err) != 0 || len(f.store.audit) != 0 || len(f.sl.sessions) != 1 {
		t.Errorf("a deleted Connection = %v", err)
	}
}

// TestSlackLinkReplaces is C-18.FR-4 for Slack: a second link of a User through the same Connection replaces the
// first, recorded as account_link.unlink with replaced and logged; a link through another Slack Connection is of
// another identity space and stays.
func TestSlackLinkReplaces(t *testing.T) {
	e := newEnv(t)
	st := e.startSL(t, alice, "alice@example.org")
	first, err := e.s.ConfirmSlack(t.Context(), alice, st.RequestPublicID, e.sl.code(t))
	if err != nil {
		t.Fatal(err)
	}
	st2, err := e.s.StartSlack(t.Context(), alice, sl2PID, "alice@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.ConfirmSlack(t.Context(), alice, st2.RequestPublicID, e.sl.code(t)); err != nil ||
		len(e.store.links) != 2 {
		t.Fatalf("a link through another Slack Connection = %v, links %+v", err, e.store.links)
	}
	st = e.startSL(t, alice, "dana")
	l, err := e.s.ConfirmSlack(t.Context(), alice, st.RequestPublicID, e.sl.code(t))
	if err != nil || l.ExternalID != "U0DANA" || len(e.store.links) != 2 {
		t.Fatalf("the replacing link = %+v, %v, links %+v", l, err, e.store.links)
	}
	got := entries(e.store)
	unlink := got[len(got)-2]
	if unlink.Action != ActionUnlink || unlink.Details["reason"] != ReasonReplaced || unlink.Resource != first.PublicID ||
		got[len(got)-1].Details["replaced"] != first.PublicID {
		t.Errorf("entries %+v", got[len(got)-2:])
	}
	if !strings.Contains(e.log.String(), `"event":"account_link_unlinked","messenger":"slack","connection":"`+slPID+
		`","user":"`+alicePID+`","reason":"replaced"`) {
		t.Errorf("log %s", e.log)
	}
	u, err := e.s.Lookup(t.Context(), SpaceSlack(slID), "U0DANA")
	if err != nil || u.ID != aliceID {
		t.Errorf("Lookup = %+v, %v", u, err)
	}
	if _, err := e.s.Lookup(t.Context(), SpaceSlack(slID), "U0ALICE"); !errors.Is(err, ErrNotLinked) {
		t.Errorf("the replaced account = %v", err)
	}
	if _, err := e.s.Lookup(t.Context(), SpaceSlack(sl2ID), "U0ALICE"); err != nil {
		t.Errorf("the link through Dev Slack = %v", err)
	}
}

// TestSlackConfirmConcurrentConflict is C-18.FR-5 for a Slack account that another User linked after the check: the
// confirmation is ErrLinkedElsewhere, the request kept, so that its code still links, and no confirmation is sent.
func TestSlackConfirmConcurrentConflict(t *testing.T) {
	e := newEnv(t)
	st := e.startSL(t, alice, "dana")
	code := e.sl.code(t)
	e.store.fail["InsertAccountLink"] = fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: uniqueViolation,
		ConstraintName: accountKey})
	if _, err := e.s.ConfirmSlack(t.Context(), alice, st.RequestPublicID, code); !errors.Is(err,
		ErrLinkedElsewhere) || e.store.reqs[0].consumed != nil || e.store.reqs[0].attempts != 0 || len(e.sl.sent) != 1 {
		t.Fatalf("a concurrent conflict = %v", err)
	}
	delete(e.store.fail, "InsertAccountLink")
	if l, err := e.s.ConfirmSlack(t.Context(), alice, st.RequestPublicID, code); err != nil || l.ExternalID != "U0DANA" {
		t.Errorf("the kept request = %+v, %v", l, err)
	}
}

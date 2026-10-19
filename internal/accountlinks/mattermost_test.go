// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package accountlinks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// dm is a direct message the bot sent.
type dm struct{ user, text string }

// fakeMM is the bot of a Mattermost Connection: its users by username, the direct messages it sent, and the failures
// to answer with.
type fakeMM struct {
	users      map[string]MattermostAccount
	sent       []dm
	sessions   []string
	sessionErr error
	findErr    error
	sendErr    error
	// onFind runs before a lookup, as a concurrent request would.
	onFind func()
}

func newFakeMM() *fakeMM {
	return &fakeMM{users: map[string]MattermostAccount{"bob": {"u-bob", "bob"}, "dana": {"u-dana", "dana"},
		"alice": {"u-alice", "alice"}}}
}

func (f *fakeMM) Session(ctx context.Context, id string, fn func(context.Context, MattermostBot) error) error {
	if f.sessionErr != nil {
		return f.sessionErr
	}
	f.sessions = append(f.sessions, id)
	return fn(ctx, f)
}

func (f *fakeMM) FindUser(_ context.Context, username string) (MattermostAccount, error) {
	if f.onFind != nil {
		f.onFind()
	}
	if f.findErr != nil {
		return MattermostAccount{}, f.findErr
	}
	a, ok := f.users[username]
	if !ok {
		return MattermostAccount{}, ErrMessengerUserNotFound
	}
	return a, nil
}

func (f *fakeMM) SendDirect(_ context.Context, user, text string) error {
	f.sent = append(f.sent, dm{user, text})
	return f.sendErr
}

var codeRe = regexp.MustCompile(`[2-9A-HJKMNP-Z]{8}`)

// code is the code of the last direct message.
func (f *fakeMM) code(t *testing.T) string {
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

func (e *env) startMM(t *testing.T, r Requester, username string) MattermostStarted {
	t.Helper()
	st, err := e.s.StartMattermost(t.Context(), r, mmPID, username)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func linkCodeInvalid(err error) bool {
	var fe *FieldError
	return errors.As(err, &fe) && fe.Pointer == "/code" && fe.Code == CodeLinkCodeInvalid
}

// attemptsLeft is the attempts a link_code_invalid refusal says the request has left, -1 without the number.
func attemptsLeft(err error) int {
	var fe *FieldError
	if !errors.As(err, &fe) || fe.AttemptsRemaining == nil {
		return -1
	}
	return *fe.AttemptsRemaining
}

// TestMattermostLink is C-18.FR-3, FR-11 and AC-2: the bot sends an 8-character code with the warning to the account
// found by its username; the right code links it and the bot confirms; each wrong code says how many attempts are
// left, after five the sixth attempt is refused even with the right code with none left, the request is void, and each
// wrong code is recorded with wrong_code. A used or expired request has no attempts left.
func TestMattermostLink(t *testing.T) {
	e := newEnv(t)
	st := e.startMM(t, bob, " @Bob ")
	if !slices.Equal(e.store.locks, []string{"user", "requests"}) {
		t.Errorf("a new request locked %q: the User before the pending requests it voids", e.store.locks)
	}
	if !strings.HasPrefix(st.RequestPublicID, "AR") || !st.ExpiresAt.Equal(t0.Add(10*time.Minute)) ||
		st.AttemptsRemaining != 5 || len(e.mm.sent) != 1 || e.mm.sent[0].user != "u-bob" ||
		!strings.HasPrefix(e.mm.sent[0].text, "Вы запросили связывание учётной записи с Muster. Никому не сообщайте этот код.") {
		t.Fatalf("started %+v, sent %+v", st, e.mm.sent)
	}
	code := e.mm.code(t)
	sum := sha256.Sum256([]byte(code))
	r := e.store.reqs[0]
	if len(code) != 8 || !bytes.Equal(r.hash, sum[:]) || r.target.String != "u-bob" || r.targetUsername.String != "bob" ||
		r.messenger != MessengerMattermost || strings.Contains(e.log.String(), code) {
		t.Fatalf("stored %+v", r)
	}
	e.store.locks = nil
	l, err := e.s.ConfirmMattermost(t.Context(), bob, st.RequestPublicID, strings.ToLower(code))
	if !slices.Equal(e.store.locks, []string{"user", "request"}) {
		t.Errorf("locked %q: the User first, as a new request does", e.store.locks)
	}
	if err != nil || l.Messenger != MessengerMattermost || l.ExternalID != "u-bob" || *l.Username != "bob" ||
		l.ConnectionPublicID != mmPID || l.ConnectionName != "mm" || !strings.HasPrefix(l.PublicID, "AK") {
		t.Fatalf("Confirm = %+v, %v", l, err)
	}
	if last := e.mm.sent[len(e.mm.sent)-1]; last.user != "u-bob" || last.text != "Учётная запись связана с пользователем Muster Bob" {
		t.Errorf("confirmation %+v", last)
	}
	if got := e.lastEntry(); got.Action != ActionLink || got.Transport != "ui" || got.Address != "192.0.2.2" ||
		got.Details["external_id"] != "u-bob" || e.store.reqs[0].consumed == nil || len(e.store.hints) != 1 ||
		len(e.mm.sessions) != 2 {
		t.Errorf("entry %+v, request %+v", got, e.store.reqs[0])
	}
	if _, err := e.s.ConfirmMattermost(t.Context(), bob, st.RequestPublicID, code); !linkCodeInvalid(err) ||
		attemptsLeft(err) != 0 || e.lastEntry().Details["reason"] != ReasonTokenUsed {
		t.Errorf("a used request = %v, %+v", err, e.lastEntry())
	}
	// C-18.AC-2: five wrong codes void the request.
	st = e.startMM(t, alice, "dana")
	code = e.mm.code(t)
	for i := range 5 {
		_, err := e.s.ConfirmMattermost(t.Context(), alice, st.RequestPublicID, "22222222")
		if !linkCodeInvalid(err) || attemptsLeft(err) != 4-i || e.lastEntry().Details["reason"] != ReasonWrongCode {
			t.Fatalf("wrong code %d = %v, %d left", i+1, err, attemptsLeft(err))
		}
	}
	req := e.store.reqs[len(e.store.reqs)-1]
	if req.attempts != 5 || req.voided == nil {
		t.Fatalf("after five wrong codes %+v", req)
	}
	if _, err := e.s.ConfirmMattermost(t.Context(), alice, st.RequestPublicID, code); !linkCodeInvalid(err) ||
		attemptsLeft(err) != 0 || len(e.store.links) != 1 || e.lastEntry().Details["reason"] != ReasonWrongCode {
		t.Errorf("the sixth attempt with the right code = %v, links %+v", err, e.store.links)
	}
	if strings.Count(e.log.String(), `"event":"account_link_rejected","messenger":"mattermost","connection":"`+mmPID+
		`","reason":"wrong_code"`) != 6 {
		t.Errorf("log %s", e.log)
	}
	// An expired code.
	st = e.startMM(t, alice, "dana")
	code = e.mm.code(t)
	e.clock.Advance(10 * time.Minute)
	if _, err := e.s.ConfirmMattermost(t.Context(), alice, st.RequestPublicID, code); !linkCodeInvalid(err) ||
		attemptsLeft(err) != 0 || e.lastEntry().Details["reason"] != ReasonTokenExpired {
		t.Errorf("an expired code = %v", err)
	}
}

// TestMattermostRequestLimits is C-18.FR-3 with account_link.mattermost_code_requests on a manual clock: the sixth
// request of one User within an hour and the fourth for one target account are refused until the oldest counted
// request is an hour old; a new request voids the User's earlier pending one.
func TestMattermostRequestLimits(t *testing.T) {
	e := newEnv(t)
	for i := range 5 {
		e.startMM(t, alice, []string{"bob", "dana", "alice", "bob", "dana"}[i])
		e.clock.Advance(time.Minute)
	}
	if n := len(e.store.reqs); e.store.reqs[0].voided == nil || e.store.reqs[n-1].voided != nil {
		t.Fatal("a new request did not void the earlier one")
	}
	var rl *RateLimitedError
	if _, err := e.s.StartMattermost(t.Context(), alice, mmPID, "bob"); !errors.As(err, &rl) ||
		rl.RetryAfter != 55*time.Minute || rl.Seconds() != 3300 || len(e.store.reqs) != 5 {
		t.Fatalf("the sixth request = %v", err)
	}
	e.clock.Advance(55 * time.Minute)
	if _, err := e.s.StartMattermost(t.Context(), alice, mmPID, "carol-unknown"); err == nil || errors.As(err, &rl) {
		t.Errorf("a request once the oldest is an hour old = %v", err)
	}
	// Three requests for dana by others; the fourth is refused for the hour.
	f := newEnv(t)
	for range 3 {
		f.startMM(t, bob, "dana")
	}
	if _, err := f.s.StartMattermost(t.Context(), alice, mmPID, "dana"); !errors.As(err, &rl) ||
		rl.Seconds() != 3600 {
		t.Fatalf("the fourth request for one account = %v", err)
	}
	if st := f.startMM(t, alice, "bob"); st.RequestPublicID == "" {
		t.Error("another account is not limited")
	}
}

// TestMattermostRequestLimitsInTransaction is C-18.FR-3 under concurrency: both limits of
// account_link.mattermost_code_requests are counted again in the transaction that stores the request, the User's
// under the lock of their row and the target account's under its advisory lock, so that requests stored meanwhile by
// concurrent ones refuse it and nothing is stored or sent.
func TestMattermostRequestLimitsInTransaction(t *testing.T) {
	e := newEnv(t)
	e.startMM(t, alice, "dana")
	if want := []string{"user", "user in tx", "lock 12/u-dana in tx", "target in tx"}; !slices.Equal(e.store.counted,
		want) {
		t.Fatalf("counted %q, want %q", e.store.counted, want)
	}
	stored := func(e *env, userID int64, target string, n int) func() {
		return func() {
			for range n {
				e.store.reqs = append(e.store.reqs, fReq{id: e.store.id(), userID: userID,
					messenger: MessengerMattermost, connID: mmID, target: text(target), created: e.clock.Now()})
			}
		}
	}
	var rl *RateLimitedError
	e.clock.Advance(time.Minute)
	e.mm.onFind = stored(e, aliceID, "u-alice", 4)
	before, sent := len(e.store.reqs), len(e.mm.sent)
	if _, err := e.s.StartMattermost(t.Context(), alice, mmPID, "bob"); !errors.As(err, &rl) ||
		rl.RetryAfter != 59*time.Minute || len(e.store.reqs) != before+4 || len(e.mm.sent) != sent {
		t.Fatalf("a request past the User's limit reached meanwhile = %v", err)
	}
	f := newEnv(t)
	f.startMM(t, bob, "dana")
	f.mm.onFind = stored(f, carolID, "u-dana", 2)
	before, sent = len(f.store.reqs), len(f.mm.sent)
	if _, err := f.s.StartMattermost(t.Context(), alice, mmPID, "dana"); !errors.As(err, &rl) ||
		rl.Seconds() != 3600 || len(f.store.reqs) != before+2 || len(f.mm.sent) != sent {
		t.Fatalf("a request past the account's limit reached meanwhile = %v", err)
	}
	f.mm.onFind = nil
	f.store.fail = map[string]error{"LockCodeTarget": errBoom}
	if _, err := f.s.StartMattermost(t.Context(), alice, mmPID, "bob"); !errors.Is(err, errBoom) {
		t.Errorf("a failed lock = %v", err)
	}
}

// TestMattermostStartRefusals: an unknown username, an empty one and a Connection that is not a Mattermost one are
// field errors; an account linked to another User is ErrLinkedElsewhere, recorded as link_rejected with conflict; no
// limiter token stores nothing; a direct message that fails voids the request and its error carries no code.
func TestMattermostStartRefusals(t *testing.T) {
	e := newEnv(t)
	var fe *FieldError
	for _, c := range []struct{ conn, user, pointer, code string }{
		{mmPID, "nobody", "/username", CodeMessengerUserNotFound},
		{mmPID, " @ ", "/username", CodeRequired},
		{tgPID, "bob", "/connection_id", CodeUnknownID},
		{"bad", "bob", "/connection_id", CodeUnknownID},
	} {
		if _, err := e.s.StartMattermost(t.Context(), alice, c.conn, c.user); !errors.As(err, &fe) ||
			fe.Pointer != c.pointer || fe.Code != c.code {
			t.Errorf("StartMattermost(%s, %q) = %v", c.conn, c.user, err)
		}
	}
	e.addLink(bobID, MessengerMattermost, mmID, "u-bob", ptr("bob"))
	if _, err := e.s.StartMattermost(t.Context(), alice, mmPID, "bob"); !errors.Is(err, ErrLinkedElsewhere) ||
		len(e.store.reqs) != 0 {
		t.Fatalf("a conflict = %v", err)
	}
	if got := e.lastEntry(); got.Action != ActionLinkRejected || got.Details["reason"] != ReasonConflict ||
		got.Details["external_id"] != "u-bob" || got.Details["user"] != alicePID || got.Actor != "user:1" ||
		got.Transport != "ui" || got.Address != "192.0.2.1" {
		t.Errorf("entry %+v", got)
	}
	limited := errors.New("no limiter token")
	e.mm.sessionErr = limited
	if _, err := e.s.StartMattermost(t.Context(), alice, mmPID, "dana"); !errors.Is(err, limited) ||
		len(e.store.reqs) != 0 {
		t.Errorf("no token = %v", err)
	}
	e.mm.sessionErr = ErrNoConnection
	if _, err := e.s.StartMattermost(t.Context(), alice, mmPID, "dana"); !errors.As(err, &fe) ||
		fe.Code != CodeUnknownID {
		t.Errorf("a Connection that went away = %v", err)
	}
	e.mm.sessionErr = nil
	e.mm.findErr = &MessengerError{Call: "the user lookup", Outcome: "fatal", Status: 403}
	if _, err := e.s.StartMattermost(t.Context(), alice, mmPID, "dana"); !errors.As(err, new(*MessengerError)) {
		t.Errorf("a refused lookup = %v", err)
	}
	e.mm.findErr = nil
	e.mm.sendErr = &MessengerError{Call: "the direct message", Outcome: "fatal", Status: 403}
	_, err := e.s.StartMattermost(t.Context(), alice, mmPID, "dana")
	if !errors.As(err, new(*MessengerError)) || len(e.store.reqs) != 1 || e.store.reqs[0].voided == nil ||
		strings.Contains(err.Error(), e.mm.code(t)) {
		t.Errorf("a failed direct message = %v, %+v", err, e.store.reqs)
	}
	e.store.fail["VoidLinkRequest"] = errBoom
	if _, err := e.s.StartMattermost(t.Context(), alice, mmPID, "dana"); !errors.Is(err, errBoom) ||
		!errors.As(err, new(*MessengerError)) {
		t.Errorf("a failed void = %v", err)
	}
	e.mm.sendErr = nil
	for _, name := range []string{"CountUserCodeRequests", "CountTargetCodeRequests", "LockLinkUser",
		"GetAccountLinkBySpace", "VoidPendingMattermostRequests", "InsertLinkRequest", "GetLinkConnection"} {
		e.store.fail = map[string]error{name: errBoom}
		if _, err := e.s.StartMattermost(t.Context(), alice, mmPID, "dana"); !errors.Is(err, errBoom) {
			t.Errorf("a failed %s = %v", name, err)
		}
	}
	e.store.fail = map[string]error{}
	e.s.cfg.Random = bytes.NewReader(nil)
	if _, err := e.s.StartMattermost(t.Context(), alice, mmPID, "dana"); err == nil {
		t.Error("a code without randomness")
	}
}

// TestMattermostConfirmRefusals: a request of another User, a Telegram one and an unknown one are ErrNotFound; an
// account linked meanwhile to another User is ErrLinkedElsewhere; no limiter token counts no attempt; a confirmation
// the bot fails to send is only logged.
func TestMattermostConfirmRefusals(t *testing.T) {
	e := newEnv(t)
	st := e.startMM(t, alice, "dana")
	code := e.mm.code(t)
	e.start(t, alice)
	tgReq := e.store.reqs[1].publicID
	for _, c := range []struct {
		r  Requester
		id string
	}{{bob, st.RequestPublicID}, {alice, tgReq}, {alice, "ARAAAAAAAAAAZZ"}, {alice, "bad"}} {
		if _, err := e.s.ConfirmMattermost(t.Context(), c.r, c.id, code); !errors.Is(err, ErrNotFound) {
			t.Errorf("Confirm(%s) = %v", c.id, err)
		}
	}
	limited := errors.New("no limiter token")
	e.mm.sessionErr = limited
	if _, err := e.s.ConfirmMattermost(t.Context(), alice, st.RequestPublicID, "22222222"); !errors.Is(err, limited) ||
		e.store.reqs[0].attempts != 0 {
		t.Errorf("no token = %v, %d attempts", err, e.store.reqs[0].attempts)
	}
	e.mm.sessionErr = ErrNoConnection
	if _, err := e.s.ConfirmMattermost(t.Context(), alice, st.RequestPublicID, code); !linkCodeInvalid(err) {
		t.Errorf("a Connection that went away = %v", err)
	}
	e.mm.sessionErr = nil
	e.addLink(bobID, MessengerMattermost, mmID, "u-dana", ptr("dana"))
	if _, err := e.s.ConfirmMattermost(t.Context(), alice, st.RequestPublicID, code); !errors.Is(err,
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
		if _, err := e.s.ConfirmMattermost(t.Context(), alice, st.RequestPublicID, c); !errors.Is(err, errBoom) ||
			len(e.store.links) != 0 {
			t.Errorf("a failed %s = %v", name, err)
		}
	}
	e.store.fail = map[string]error{}
	e.mm.sendErr = &MessengerError{Call: "the direct message", Outcome: "transient", Status: 502}
	if l, err := e.s.ConfirmMattermost(t.Context(), alice, st.RequestPublicID, code); err != nil || l.ExternalID != "u-dana" {
		t.Fatalf("a link whose confirmation failed = %+v, %v", l, err)
	}
	if !strings.Contains(e.log.String(), `"event":"account_link_linked","messenger":"mattermost","connection":"`+mmPID+
		`","user":"`+alicePID+`","error":"the messenger refused the direct message (transient, status 502)"`) {
		t.Errorf("log %s", e.log)
	}
}

// TestNewCode: codes are drawn without bias from the alphabet without look-alikes.
func TestNewCode(t *testing.T) {
	c, err := newCode(bytes.NewReader(append(bytes.Repeat([]byte{255}, 16), 0, 30, 31, 61, 247, 248, 1, 2, 3, 4, 5,
		6, 7, 8, 9, 10)))
	if err != nil || c != "2Z2ZZ345" {
		t.Errorf("newCode = %q, %v", c, err)
	}
	if normalizeCode(" ab2c3d4e ") != "AB2C3D4E" {
		t.Error("normalizeCode")
	}
}

// TestMattermostConfirmConcurrentConflict is C-18.FR-5 for an account that another User linked after the check: the
// insert fails on account_links_account_key and the confirmation is ErrLinkedElsewhere, recorded as link_rejected with
// conflict, the User's previous link kept and the request neither consumed nor void, so that its code still links.
func TestMattermostConfirmConcurrentConflict(t *testing.T) {
	e := newEnv(t)
	prev := e.addLink(aliceID, MessengerMattermost, mmID, "u-erin", ptr("erin"))
	st := e.startMM(t, alice, "dana")
	code := e.mm.code(t)
	sent := len(e.mm.sent)
	e.store.fail["InsertAccountLink"] = &pgconn.PgError{Code: uniqueViolation, ConstraintName: accountKey}
	_, err := e.s.ConfirmMattermost(t.Context(), alice, st.RequestPublicID, code)
	got := e.lastEntry()
	if !errors.Is(err, ErrLinkedElsewhere) || got.Action != ActionLinkRejected ||
		got.Details["reason"] != ReasonConflict || got.Details["user"] != alicePID ||
		got.Details["external_id"] != "u-dana" || e.store.reqs[0].consumed != nil || e.store.reqs[0].voided != nil ||
		e.store.reqs[0].attempts != 0 || len(e.store.links) != 1 || e.store.links[0] != prev ||
		len(e.mm.sent) != sent || len(e.store.hints) != 0 {
		t.Fatalf("a concurrent conflict = %v, entry %+v, links %+v", err, got, e.store.links)
	}
	if !strings.Contains(e.log.String(), `"event":"account_link_rejected","messenger":"mattermost","connection":"`+
		mmPID+`","reason":"conflict"`) {
		t.Errorf("log %s", e.log)
	}
	delete(e.store.fail, "InsertAccountLink")
	if l, err := e.s.ConfirmMattermost(t.Context(), alice, st.RequestPublicID, code); err != nil ||
		l.ExternalID != "u-dana" || len(e.store.links) != 1 || e.store.reqs[0].consumed == nil {
		t.Errorf("the kept request = %+v, %v", l, err)
	}
}

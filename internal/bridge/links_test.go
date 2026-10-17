// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/accountlinks"
	"github.com/muster-io/muster/internal/bridge/gen"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/outbound"
)

// linkCall is an account_link event as the Linker received it.
type linkCall struct {
	conn  accountlinks.BridgeConnection
	token string
	from  accountlinks.BridgeAccount
}

// fakeLinker is the Linker of the event tests: it records the events it links, answers them with answer and records
// the answer errors its Log gets; err makes it fail.
type fakeLinker struct {
	mu     sync.Mutex
	calls  []linkCall
	logged []error
	answer string
	err    error
}

func (f *fakeLinker) LinkBridge(_ context.Context, conn accountlinks.BridgeConnection, token string,
	from accountlinks.BridgeAccount) (accountlinks.BridgeLinked, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, linkCall{conn: conn, token: token, from: from})
	if f.err != nil {
		return accountlinks.BridgeLinked{}, f.err
	}
	return accountlinks.BridgeLinked{Answer: f.answer, Log: func(_ context.Context, answerErr error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.logged = append(f.logged, answerErr)
	}}, nil
}

func (f *fakeLinker) linked() []linkCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]linkCall(nil), f.calls...)
}

func (f *fakeLinker) logs() []error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]error(nil), f.logged...)
}

const linkToken = "dGhlLWFjY291bnQtbGluay10b2tlbi1vZi1tdXN0ZXI"

// TestAccountLinkEvent covers C-22.FR-15 and FR-13: an account_link event reaches the Linker inside its claim with the
// Connection, the token and the sender — external id, display name and handle — and is answered with the Linker's
// text, with the Idempotency-Key answer-<event id>, once the claim committed; then the Linker logs, with the answer's
// error stripped of the token that a bridge may echo. An event the Connection handled before reaches the Linker no
// more; an event without an answer is logged with none; a failure of the Linker fails the event, which is answered
// nothing and comes again.
func TestAccountLinkEvent(t *testing.T) {
	e := newPressEnv(t)
	e.linker.answer = "Linked to Muster user Bob Brown (bob)"
	ev := e.event(t, TypeAccountLink, member("id", "ev-link"), member("token", linkToken))
	for range 2 {
		if err := e.events.Handle(t.Context(), e.conn, ev); err != nil {
			t.Fatal(err)
		}
	}
	calls := e.linker.linked()
	if len(calls) != 1 || calls[0].conn != (accountlinks.BridgeConnection{ID: e.conn.ID, PublicID: e.conn.PublicID}) ||
		calls[0].token != linkToken || calls[0].from != (accountlinks.BridgeAccount{ExternalID: "u-bob",
		DisplayName: "Bob Brown", Handle: "bob"}) {
		t.Fatalf("linked %+v", calls)
	}
	a := e.answers(t)
	if len(a) != 1 || a[0].text != "Linked to Muster user Bob Brown (bob)" || a[0].key != "answer-ev-link" ||
		a[0].eventID != "ev-link" {
		t.Fatalf("answers %+v", a)
	}
	if logs := e.linker.logs(); len(logs) != 1 || logs[0] != nil {
		t.Errorf("logged %v", logs)
	}
	// An answer the bridge refuses, echoing the token.
	e.answerAll(answerJSON(http.StatusBadRequest, "application/problem+json",
		`{"title":"Bad","detail":"the account sent `+linkToken+` before","class":"invalid"}`))
	ev = e.event(t, TypeAccountLink, member("id", "ev-link-2"), member("token", linkToken))
	if err := e.events.Handle(t.Context(), e.conn, ev); err != nil {
		t.Fatal(err)
	}
	logs := e.linker.logs()
	if len(logs) != 2 || logs[1] == nil || strings.Contains(logs[1].Error(), linkToken) ||
		!strings.Contains(logs[1].Error(), "the account sent [redacted] before") {
		t.Errorf("the refused answer logged %v", logs)
	}
	// No answer.
	e.linker.answer = ""
	ev = e.event(t, TypeAccountLink, member("id", "ev-link-3"))
	if err := e.events.Handle(t.Context(), e.conn, ev); err != nil || len(e.answers(t)) != 2 ||
		len(e.linker.logs()) != 3 || e.linker.logs()[2] != nil {
		t.Errorf("an event without an answer = %v, %d answers", err, len(e.answers(t)))
	}
	// A failed Linker.
	e.linker.err = errors.New("the database went away")
	ev = e.event(t, TypeAccountLink, member("id", "ev-link-4"))
	if err := e.events.Handle(t.Context(), e.conn, ev); err == nil || e.store.recorded(e.conn.ID, ev.ID) ||
		len(e.answers(t)) != 2 || !strings.Contains(e.log.String(), `"event":"bridge_event_failed"`) {
		t.Errorf("a failed link = %v", err)
	}
	// A Linker without a log.
	l := &bareLinker{}
	e.events.Linker = l
	ev = e.event(t, TypeAccountLink, member("id", "ev-link-5"))
	if err := e.events.Handle(t.Context(), e.conn, ev); err != nil || len(e.answers(t)) != 3 {
		t.Errorf("a link without a log = %v", err)
	}
}

// bareLinker answers without a log.
type bareLinker struct{}

func (bareLinker) LinkBridge(context.Context, accountlinks.BridgeConnection, string, accountlinks.BridgeAccount) (
	accountlinks.BridgeLinked, error) {
	return accountlinks.BridgeLinked{Answer: "x"}, nil
}

// TestWithoutSecret: a secret leaves the text of an error; an error without it, or nil, is kept as it is.
func TestWithoutSecret(t *testing.T) {
	err := errors.New("echo " + linkToken + " and " + linkToken)
	if got := withoutSecret(err, linkToken); got.Error() != "echo [redacted] and [redacted]" {
		t.Errorf("= %v", got)
	}
	other := errors.New("other")
	if !errors.Is(withoutSecret(other, linkToken), other) || withoutSecret(nil, linkToken) != nil ||
		!errors.Is(withoutSecret(err, ""), err) {
		t.Error("an error without the secret changed")
	}
}

// TestFindUsers covers findUsers: the handle travels in the query, never in the text of a failure; an answer with one
// person is that person, an empty list none, and anything else, or a person out of the protocol's bounds, is invalid.
func TestFindUsers(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		found      bool
		class      Class
	}{
		{"one", `{"items":[{"external_id":"u-bob","display_name":"Bob Brown","handle":"bob@example.org"}]}`, 200, true,
			""},
		{"none", `{"items":[]}`, 200, false, ""},
		{"two", `{"items":[{"external_id":"a","display_name":"A"},{"external_id":"b","display_name":"B"}]}`, 200,
			false, ClassInvalid},
		{"no items", `{}`, 200, false, ClassInvalid},
		{"null items", `{"items":null}`, 200, false, ClassInvalid},
		{"no name", `{"items":[{"external_id":"u-bob","display_name":""}]}`, 200, false, ClassInvalid},
		{"not a person", `{"items":[7]}`, 200, false, ClassInvalid},
		{"long handle", `{"items":[{"external_id":"u","display_name":"U","handle":"` + strings.Repeat("h", 321) +
			`"}]}`, 200, false, ClassInvalid},
		{"refused", `{"title":"no","detail":"cannot search for bob@example.org","class":"invalid"}`, 400, false,
			ClassInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ct := "application/json"
			if c.status != 200 {
				ct = "application/problem+json"
			}
			s := startStandIn(t, answerJSON(c.status, ct, c.body))
			cl := newClient(t, network(t, nil), Settings{BaseURL: s.srv.URL, Token: testToken})
			p, found, r := cl.FindUsers(t.Context(), outbound.ClassInteractive, "bob+x@example.org")
			if found != c.found || r.Classification.Class != c.class {
				t.Fatalf("FindUsers = %+v, %v, %+v", p, found, r)
			}
			req := s.seen()[0]
			if req.Method != http.MethodGet || req.Path != "/v1/users" ||
				req.Header.Get("Authorization") != "Bearer "+testToken {
				t.Errorf("request %+v", req)
			}
			if c.found && (p.ExternalId != "u-bob" || p.DisplayName != "Bob Brown" ||
				p.Handle.MustGet() != "bob@example.org") {
				t.Errorf("person %+v", p)
			}
			if c.status == 400 && strings.Contains(string(r.Error), "bob+x") {
				t.Errorf("the handle in %q", r.Error)
			}
		})
	}
	var q url.Values
	s := startStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query()
		answerJSON(200, "application/json", `{"items":[]}`)(w, r)
	})
	cl := newClient(t, network(t, nil), Settings{BaseURL: s.srv.URL, Token: testToken})
	if _, _, r := cl.FindUsers(t.Context(), outbound.ClassInteractive, "bob+x@example.org"); !r.OK() ||
		q.Get("handle") != "bob+x@example.org" {
		t.Errorf("the query %v, %+v", q, r)
	}
}

// TestSendDirectMessage covers sendDirectMessage: the body and the account as JSON with the Idempotency-Key, the body
// cut to 2000 characters; an answer without a message_id is invalid, and a refusal keeps its class.
func TestSendDirectMessage(t *testing.T) {
	s := startStandIn(t, answerJSON(http.StatusCreated, "application/json", `{"message_id":"dm1","replayed":false}`))
	cl := newClient(t, network(t, nil), Settings{BaseURL: s.srv.URL, Token: testToken})
	out, r := cl.SendDirectMessage(t.Context(), outbound.ClassInteractive, "dm-AR0000000000A1", "u-bob",
		strings.Repeat("é", 2100))
	req := s.seen()[0]
	var in gen.DirectMessageInput
	if !r.OK() || out.MessageId != "dm1" || req.Method != http.MethodPost || req.Path != "/v1/direct-messages" ||
		req.Header.Get("Idempotency-Key") != "dm-AR0000000000A1" || json.Unmarshal([]byte(req.Body), &in) != nil ||
		in.UserExternalId != "u-bob" || in.Body != strings.Repeat("é", 2000) {
		t.Fatalf("SendDirectMessage = %+v, %+v; request %+v", out, r, req)
	}
	for _, c := range []struct {
		status int
		ct     string
		body   string
		class  Class
	}{
		{http.StatusCreated, "application/json", `{"replayed":false}`, ClassInvalid},
		{http.StatusForbidden, "application/problem+json", `{"title":"no","class":"forbidden"}`, ClassForbidden},
	} {
		s := startStandIn(t, answerJSON(c.status, c.ct, c.body))
		cl := newClient(t, network(t, nil), Settings{BaseURL: s.srv.URL, Token: testToken})
		if _, r := cl.SendDirectMessage(t.Context(), outbound.ClassInteractive, "k", "u", "b"); r.Classification.Class !=
			c.class {
			t.Errorf("%d %s = %+v", c.status, c.body, r)
		}
	}
}

// linkConns are the LinkConnections of the LinkBots tests: the Connection and its info, with what each call got.
type linkConns struct {
	conn       Conn
	err        error
	info       Info
	infoResult Result
	classes    []outbound.Class
}

func (f *linkConns) BridgeConn(_ context.Context, publicID string) (Conn, error) {
	if f.err != nil {
		return Conn{}, f.err
	}
	if publicID != f.conn.PublicID {
		return Conn{}, ErrNoConnection
	}
	return f.conn, nil
}

func (f *linkConns) BridgeInfo(_ context.Context, _ Conn, class outbound.Class) (Info, Result) {
	f.classes = append(f.classes, class)
	return f.info, f.infoResult
}

// codeInfo is an info that offers a code by email.
func codeInfo() Info {
	var i Info
	i.ProtocolVersion, i.UserLookup = ProtocolVersion, gen.Email
	i.Capabilities.DirectMessages = &struct {
		BotCanStart bool `json:"bot_can_start"`
	}{BotCanStart: true}
	return i
}

// linkBotsEnv is LinkBots over linkConns and a path that takes every token at once, with a stand-in bridge that
// answers findUsers and sendDirectMessage with the handlers set.
type linkBotsEnv struct {
	bots  LinkBots
	conns *linkConns
	path  *answerPath
	srv   *standIn

	mu        sync.Mutex
	findUsers http.HandlerFunc
	sendDM    http.HandlerFunc
}

func newLinkBotsEnv(t *testing.T) *linkBotsEnv {
	t.Helper()
	e := &linkBotsEnv{path: newTestPath(eventTime),
		findUsers: answerJSON(200, "application/json",
			`{"items":[{"external_id":"u-bob","display_name":"Bob Brown","handle":"bob@example.org"}]}`),
		sendDM: answerJSON(201, "application/json", `{"message_id":"dm1","replayed":false}`)}
	e.srv = startStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		h := e.sendDM
		if r.URL.Path == "/v1/users" {
			h = e.findUsers
		}
		e.mu.Unlock()
		h(w, r)
	})
	e.conns = &linkConns{conn: Conn{ID: 7, PublicID: "CN0000000000B7", Version: 1,
		Client: newClient(t, network(t, nil), Settings{BaseURL: e.srv.srv.URL, Token: testToken})},
		info: codeInfo()}
	e.bots = LinkBots{Conns: e.conns, Path: e.path}
	return e
}

func (e *linkBotsEnv) answer(find, send http.HandlerFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if find != nil {
		e.findUsers = find
	}
	if send != nil {
		e.sendDM = send
	}
}

// TestLinkBots covers C-22.FR-15 and C-18.FR-11: a session takes one token of the Connection on the interactive path
// as a direct message, reads the info in the interactive class and offers what it declares; the bot finds the account
// by its handle and sends it the code with its key. An unknown Connection is accountlinks.ErrNoConnection, and what f
// returns is the session's.
func TestLinkBots(t *testing.T) {
	e := newLinkBotsEnv(t)
	var (
		offer accountlinks.BridgeOffer
		acct  accountlinks.BridgeAccount
	)
	err := e.bots.Session(t.Context(), "CN0000000000B7", func(ctx context.Context, b accountlinks.BridgeBot) error {
		offer = b.Offer()
		var err error
		if acct, err = b.FindUser(ctx, "bob@example.org"); err != nil {
			return err
		}
		return b.SendDirectMessage(ctx, "dm-AR0000000000A1", acct.ExternalID, "the code")
	})
	if err != nil || offer != (accountlinks.BridgeOffer{Read: true, UserLookup: "email", BotCanStart: true}) ||
		acct != (accountlinks.BridgeAccount{ExternalID: "u-bob", DisplayName: "Bob Brown", Handle: "bob@example.org"}) {
		t.Fatalf("Session = %v, offer %+v, account %+v", err, offer, acct)
	}
	if e.path.calls() != 1 || *e.path.subjects[0].Connection != 7 || e.path.ops[0] != "delivery.directOp" ||
		len(e.conns.classes) != 1 || e.conns.classes[0] != outbound.ClassInteractive ||
		e.path.outcomes[0].Kind != delivery.OutcomeOK {
		t.Errorf("path %+v %v, info read in %v", e.path.subjects, e.path.ops, e.conns.classes)
	}
	reqs := e.srv.seen()
	if len(reqs) != 2 || reqs[1].Header.Get("Idempotency-Key") != "dm-AR0000000000A1" ||
		!strings.Contains(reqs[1].Body, `"user_external_id":"u-bob"`) {
		t.Errorf("requests %+v", reqs)
	}
	want := errors.New("from f")
	if err := e.bots.Session(t.Context(), "CN0000000000B7", func(context.Context, accountlinks.BridgeBot) error {
		return want
	}); !errors.Is(err, want) {
		t.Errorf("f's error = %v", err)
	}
	if err := e.bots.Session(t.Context(), "CN0000000000B9", nil); !errors.Is(err, accountlinks.ErrNoConnection) {
		t.Errorf("an unknown Connection = %v", err)
	}
	e.conns.err = errors.New("the database went away")
	if err := e.bots.Session(t.Context(), "CN0000000000B7", nil); !errors.Is(err, e.conns.err) {
		t.Errorf("a failed read = %v", err)
	}
	e.conns.err = nil
	limited := &delivery.LimitedError{RetryAfter: time.Second}
	e.path.err = limited
	if err := e.bots.Session(t.Context(), "CN0000000000B7", nil); !errors.Is(err, limited) {
		t.Errorf("no token = %v", err)
	}
}

// TestLinkBotsOffer covers C-22.FR-2 for a code: the offer of a session is the info's, the stale copy when its read
// fails, and nothing read without a copy; a read that waits holds the Connection.
func TestLinkBotsOffer(t *testing.T) {
	e := newLinkBotsEnv(t)
	offerNow := func() accountlinks.BridgeOffer {
		t.Helper()
		var o accountlinks.BridgeOffer
		if err := e.bots.Session(t.Context(), "CN0000000000B7", func(_ context.Context,
			b accountlinks.BridgeBot) error {
			o = b.Offer()
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return o
	}
	e.conns.infoResult = Result{Status: 500, Classification: Classification{Class: ClassTransient}}
	if o := offerNow(); !o.Code() {
		t.Errorf("the stale copy = %+v", o)
	}
	e.conns.info = Info{}
	if o := offerNow(); o.Read || o.Code() {
		t.Errorf("no copy = %+v", o)
	}
	e.conns.info = codeInfo()
	e.conns.info.Capabilities.DirectMessages = nil
	e.conns.infoResult = Result{Status: 429, Classification: Classification{Class: ClassRateLimited,
		RetryAfter: time.Minute, HasRetryAfter: true}}
	if o := offerNow(); !o.Read || o.BotCanStart || o.Code() {
		t.Errorf("without direct messages = %+v", o)
	}
	if last := e.path.outcomes[len(e.path.outcomes)-1]; last.Kind != delivery.OutcomeRetryAfter ||
		last.Scope != delivery.ScopeConnection || last.RetryAfter != time.Minute {
		t.Errorf("a read that waits = %+v", last)
	}
}

// TestLinkBotCalls covers the failures of the bot's calls: no account, and a handle the bridge cannot search for, are
// accountlinks.ErrMessengerUserNotFound; a direct message refused as forbidden is accountlinks.ErrDirectMessageRefused
// and leaves the Connection as it is; a RetryAfter is a *delivery.LimitedError that holds the Connection; any other
// failure is an *accountlinks.MessengerError without the bridge's text, which may repeat the code.
func TestLinkBotCalls(t *testing.T) {
	problem := func(status int, class, detail string, header ...string) http.HandlerFunc {
		return answerJSON(status, "application/problem+json", `{"title":"t","detail":"`+detail+`","class":"`+class+`"}`,
			header...)
	}
	run := func(t *testing.T, e *linkBotsEnv, f func(ctx context.Context, b accountlinks.BridgeBot) error) (
		delivery.Outcome, error) {
		t.Helper()
		err := e.bots.Session(t.Context(), "CN0000000000B7", f)
		return e.path.outcomes[len(e.path.outcomes)-1], err
	}
	find := func(ctx context.Context, b accountlinks.BridgeBot) error {
		_, err := b.FindUser(ctx, "bob@example.org")
		return err
	}
	send := func(ctx context.Context, b accountlinks.BridgeBot) error {
		return b.SendDirectMessage(ctx, "k", "u-bob", "code ABCDEFGH")
	}
	for _, c := range []struct {
		name    string
		find    http.HandlerFunc
		send    http.HandlerFunc
		f       func(context.Context, accountlinks.BridgeBot) error
		want    error
		kind    delivery.OutcomeKind
		message string
	}{
		{name: "none", find: answerJSON(200, "application/json", `{"items":[]}`), f: find,
			want: accountlinks.ErrMessengerUserNotFound, kind: delivery.OutcomeOK},
		{name: "cannot search", find: problem(400, "invalid", "not an email"), f: find,
			want: accountlinks.ErrMessengerUserNotFound, kind: delivery.OutcomeOK},
		{name: "lookup refused", find: problem(403, "forbidden", "no"), f: find, kind: delivery.OutcomeFatal,
			message: "the messenger refused findUsers (forbidden, status 403)"},
		{name: "lookup not found", find: answerJSON(404, "text/plain", "nope"), f: find, kind: delivery.OutcomeUnknown,
			message: "the messenger refused findUsers (invalid, status 404)"},
		{name: "refused", send: problem(403, "forbidden", "code ABCDEFGH"), f: send,
			want: accountlinks.ErrDirectMessageRefused, kind: delivery.OutcomeOK},
		{name: "token refused", send: answerJSON(401, "text/plain", "no"), f: send, kind: delivery.OutcomeFatal,
			message: "the messenger refused sendDirectMessage (forbidden, status 401)"},
		{name: "transient", send: problem(502, "transient", "code ABCDEFGH"), f: send, kind: delivery.OutcomeTransient,
			message: "the messenger refused sendDirectMessage (transient, status 502)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newLinkBotsEnv(t)
			e.answer(c.find, c.send)
			last, err := run(t, e, c.f)
			switch {
			case c.want != nil && !errors.Is(err, c.want):
				t.Errorf("= %v, want %v", err, c.want)
			case c.message != "" && (err == nil || err.Error() != c.message):
				t.Errorf("= %v, want %q", err, c.message)
			}
			if last.Kind != c.kind {
				t.Errorf("outcome %+v", last)
			}
			if err != nil && strings.Contains(err.Error(), "ABCDEFGH") {
				t.Errorf("the code in %v", err)
			}
		})
	}
	e := newLinkBotsEnv(t)
	e.answer(nil, problem(429, "rate_limited", "slow", "Retry-After", "30"))
	last, err := run(t, e, send)
	var le *delivery.LimitedError
	if !errors.As(err, &le) || le.RetryAfter != 30*time.Second || last.Kind != delivery.OutcomeRetryAfter ||
		last.Scope != delivery.ScopeConnection {
		t.Errorf("a RetryAfter = %v, %+v", err, last)
	}
}

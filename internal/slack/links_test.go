// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package slack

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/accountlinks"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/fakes/fakeslack"
	"github.com/muster-io/muster/internal/outbound"
)

// linkConnID is the Connection of the link tests.
const linkConnID = "CN0000000000SL"

// linkConns find the Connection of the link tests, or answer err.
type linkConns struct {
	conn  Conn
	err   error
	asked []string
}

func (c *linkConns) SlackLinkConnection(_ context.Context, publicID string) (Conn, error) {
	c.asked = append(c.asked, publicID)
	if c.err != nil {
		return Conn{}, c.err
	}
	return c.conn, nil
}

// linkPath is the interactive path of the link tests: it records the subject and the outcome of each call and makes
// it as deliverytest.Unlimited does; err fails every call before it is made.
type linkPath struct {
	path *delivery.Interactive

	mu       sync.Mutex
	subjects []delivery.Subject
	outcomes []delivery.Outcome
	err      error
}

func (p *linkPath) Do(ctx context.Context, s delivery.Subject, op delivery.Op) (delivery.Outcome, error) {
	p.mu.Lock()
	p.subjects = append(p.subjects, s)
	err := p.err
	p.mu.Unlock()
	if err != nil {
		return delivery.Outcome{}, err
	}
	out, err := p.path.Do(ctx, s, op)
	p.mu.Lock()
	p.outcomes = append(p.outcomes, out)
	p.mu.Unlock()
	return out, err
}

// last is the outcome of the last call the path made.
func (p *linkPath) last() delivery.Outcome {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.outcomes[len(p.outcomes)-1]
}

// linkEnv is LinkBots over the fake's app b, whose requests the Request log keeps.
type linkEnv struct {
	f     *fakeslack.Fake
	rec   *records
	conns *linkConns
	path  *linkPath
	bots  LinkBots
}

func newLinkEnv(t *testing.T) *linkEnv {
	t.Helper()
	f := startFake(t)
	return newLinkEnvAt(t, f, devRoute(f))
}

// newLinkEnvAt is the linkEnv of a client whose requests route sends where it says; f, nil for none, is the fake.
func newLinkEnvAt(t *testing.T, f *fakeslack.Fake, route Route) *linkEnv {
	t.Helper()
	rec := &records{}
	n := network(t, nil)
	n.Recorder = rec
	c := newClient(t, n, Settings{BotToken: fakeslack.SecondBotToken, AppToken: fakeslack.SecondAppToken,
		Owner: outbound.Owner{Connection: 7, PublicID: linkConnID}, Route: route})
	clk := clock.NewManual(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))
	e := &linkEnv{f: f, rec: rec, conns: &linkConns{conn: Conn{ID: 7, PublicID: linkConnID, Mode: EventModeSocket,
		Client: c}}, path: &linkPath{path: deliverytest.Unlimited(1, clock.Clocks{Business: clk, Real: clk})}}
	e.bots = LinkBots{Conns: e.conns, Path: e.path}
	return e
}

// run runs fn with the bot of a Session, failing the test when the Session fails for any other reason than fn's.
func (e *linkEnv) run(t *testing.T, fn func(ctx context.Context, b accountlinks.SlackBot) error) error {
	t.Helper()
	var ran bool
	err := e.bots.Session(t.Context(), linkConnID, func(ctx context.Context, b accountlinks.SlackBot) error {
		ran = true
		return fn(ctx, b)
	})
	if !ran {
		t.Fatalf("the Session did not run: %v", err)
	}
	return err
}

// find is FindUser of handle in a Session of its own.
func (e *linkEnv) find(t *testing.T, handle string) (accountlinks.SlackAccount, error) {
	t.Helper()
	var a accountlinks.SlackAccount
	err := e.run(t, func(ctx context.Context, b accountlinks.SlackBot) error {
		var err error
		a, err = b.FindUser(ctx, handle)
		return err
	})
	return a, err
}

// requests are the requests the fake received for method, in order.
func (e *linkEnv) requests(method string) []fakeserver.Request {
	var out []fakeserver.Request
	for _, r := range e.f.Requests() {
		if r.Path == "/api/"+method {
			out = append(out, r)
		}
	}
	return out
}

// TestEmailHandle is C-24.FR-12: an email has an @ after at least one character and a dot after that @; anything else
// is a name, a name written with a leading @ included.
func TestEmailHandle(t *testing.T) {
	for h, want := range map[string]bool{"a@b.c": true, "Alice@Example.ORG": true, "x@y@z.org": true,
		"@alice.smith": false, "alice": false, "a@b": false, "@": false, "alice.smith@": false, "": false} {
		if emailHandle(h) != want {
			t.Errorf("emailHandle(%q) = %v", h, !want)
		}
	}
}

// TestFindUserByEmail is C-24.FR-12, AC-14 and F-095: an email in any letter case finds its account with
// users.lookupByEmail, form-encoded, in the interactive class of the Connection's limiter: its user id, name and
// display name, the real name when the profile has no display name; an unknown address, a deleted account and an id
// of another shape are no account; a token without users:read.email and an answer without a user are refusals that
// name the call and no text of Slack's.
func TestFindUserByEmail(t *testing.T) {
	e := newLinkEnv(t)
	for h, want := range map[string]accountlinks.SlackAccount{
		"Alice@Example.ORG":   {ID: fakeslack.UserAlice, Name: "alice", DisplayName: "Alice Smith"},
		" bob@example.org ":   {ID: fakeslack.UserBob, Name: "bob", DisplayName: "Bob Jones"},
		"CAROL@example.org\t": {ID: fakeslack.UserCarol, Name: "carol", DisplayName: "Carol"},
	} {
		if got, err := e.find(t, h); err != nil || got != want {
			t.Errorf("FindUser(%q) = %+v, %v", h, got, err)
		}
	}
	reqs := e.requests("users.lookupByEmail")
	if len(reqs) != 3 || len(e.requests("users.list")) != 0 {
		t.Fatalf("requests %+v", e.f.Requests())
	}
	for _, r := range reqs {
		form, err := url.ParseQuery(r.Body)
		email := form.Get("email")
		if err != nil || !strings.Contains(email, "@") || strings.TrimSpace(email) != email ||
			r.Headers["Content-Type"][0] != "application/x-www-form-urlencoded" {
			t.Errorf("request %+v", r)
		}
	}
	if s := e.path.subjects[0]; s.Connection == nil || *s.Connection != 7 {
		t.Errorf("subject %+v", s)
	}
	for _, h := range []string{"nobody@example.org", "bob.old@example.org"} {
		if _, err := e.find(t, h); !errors.Is(err, accountlinks.ErrMessengerUserNotFound) {
			t.Errorf("FindUser(%q) = %v", h, err)
		}
	}
	if err := e.f.SetFault(fakeserver.Fault{Path: "/api/users.lookupByEmail", Status: http.StatusOK,
		Body: `{"ok":true,"user":{"id":"U0>X","name":"x"}}`, Times: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.find(t, "x@example.org"); !errors.Is(err, accountlinks.ErrMessengerUserNotFound) {
		t.Errorf("an id of another shape = %v", err)
	}
	if err := e.f.SetFault(fakeserver.Fault{Path: "/api/users.lookupByEmail", Status: http.StatusOK,
		Body: `{"ok":true}`, Times: 1}); err != nil {
		t.Fatal(err)
	}
	var me *accountlinks.MessengerError
	if _, err := e.find(t, "x@example.org"); !errors.As(err, &me) || me.Call != "users.lookupByEmail" ||
		me.Outcome != string(ClassUnknown) {
		t.Errorf("an answer without a user = %v", err)
	}
	if err := e.f.SetConfig(fakeslack.Config{Scopes: map[string]string{fakeslack.AppB: "chat:write,users:read"}}); err != nil {
		t.Fatal(err)
	}
	_, err := e.find(t, "alice@example.org")
	if !errors.As(err, &me) || me.Call != "users.lookupByEmail" || me.Outcome != string(ClassFatal) ||
		strings.Contains(err.Error(), "missing_scope") {
		t.Errorf("a token without users:read.email = %v", err)
	}
	if o := e.path.last(); o.Kind != delivery.OutcomeFatal || o.Error != "" {
		t.Errorf("the outcome %+v", o)
	}
}

// TestFindUserByName is C-24.FR-12, AC-14 and F-095: a name, a leading @ dropped, is compared with each account's name
// and display name regardless of case over the pages of users.list, 200 asked for a page, the second and third with
// the cursor the page before named; Slackbot, bots, the app's bot user and deleted accounts are no match; a name two
// accounts share is ambiguous once the second is found; a cursor that names itself ends the pages; a 429 with
// Retry-After is a *delivery.LimitedError that holds the Connection, and any other failure a refusal without Slack's
// text.
func TestFindUserByName(t *testing.T) {
	e := newLinkEnv(t)
	b := fakeslack.Apps()[1]
	got, err := e.find(t, "@BOB")
	if err != nil || got != (accountlinks.SlackAccount{ID: fakeslack.UserBob, Name: "bob", DisplayName: "Bob Jones"}) {
		t.Fatalf("FindUser(@BOB) = %+v, %v", got, err)
	}
	reqs := e.requests("users.list")
	if len(reqs) != 3 {
		t.Fatalf("%d pages: %+v", len(reqs), reqs)
	}
	for i, r := range reqs {
		form, _ := url.ParseQuery(r.Body)
		if form.Get("limit") != strconv.Itoa(userPageSize) || (form.Get("cursor") == "") != (i == 0) {
			t.Errorf("page %d: %s", i+1, r.Body)
		}
	}
	for h, want := range map[string]accountlinks.SlackAccount{
		"alice smith": {ID: fakeslack.UserAlice, Name: "alice", DisplayName: "Alice Smith"},
		" @carol ":    {ID: fakeslack.UserCarol, Name: "carol", DisplayName: "Carol"},
		"Sam.Two":     {ID: fakeslack.UserSam2, Name: "sam.two", DisplayName: "sam"},
	} {
		if got, err := e.find(t, h); err != nil || got != want {
			t.Errorf("FindUser(%q) = %+v, %v", h, got, err)
		}
	}
	e.f.ResetRequests()
	if _, err := e.find(t, "Sam"); !errors.Is(err, accountlinks.ErrHandleAmbiguous) || len(e.requests("users.list")) != 3 {
		t.Errorf("Sam = %v after %d pages", err, len(e.requests("users.list")))
	}
	for _, h := range []string{"helper", "bob.old", "slackbot", "Slackbot", b.BotName, "nobody"} {
		if _, err := e.find(t, h); !errors.Is(err, accountlinks.ErrMessengerUserNotFound) {
			t.Errorf("FindUser(%q) = %v", h, err)
		}
	}
	e.f.ResetRequests()
	if err := e.f.SetFault(fakeserver.Fault{Path: "/api/users.list", Status: http.StatusOK,
		Body: `{"ok":true,"members":[],"response_metadata":{"next_cursor":"same"}}`, Times: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.find(t, "bob"); !errors.Is(err, accountlinks.ErrMessengerUserNotFound) ||
		len(e.requests("users.list")) != 2 {
		t.Errorf("a cursor that names itself = %v after %d pages", err, len(e.requests("users.list")))
	}
	if err := e.f.SetFault(fakeserver.Fault{Path: "/api/users.list", Status: http.StatusTooManyRequests,
		RetryAfterSeconds: 30, Body: `{"ok":false,"error":"ratelimited"}`, Times: 1}); err != nil {
		t.Fatal(err)
	}
	var le *delivery.LimitedError
	if _, err := e.find(t, "bob"); !errors.As(err, &le) || le.RetryAfter != 30*time.Second {
		t.Errorf("a 429 = %v", err)
	}
	if o := e.path.last(); o.Kind != delivery.OutcomeRetryAfter || o.Scope != delivery.ScopeConnection ||
		o.RetryAfter != 30*time.Second || o.Error != "" {
		t.Errorf("the outcome of a 429 %+v", o)
	}
	if err := e.f.SetFault(fakeserver.Fault{Path: "/api/users.list", Status: http.StatusBadGateway,
		Body: "bad gateway", Times: 1}); err != nil {
		t.Fatal(err)
	}
	var me *accountlinks.MessengerError
	if _, err := e.find(t, "bob"); !errors.As(err, &me) || me.Call != "users.list" ||
		me.Outcome != string(ClassTransient) || me.Status != http.StatusBadGateway {
		t.Errorf("a 502 = %v", err)
	}
	if err := e.f.SetFault(fakeserver.Fault{Path: "/api/users.list", Status: http.StatusOK, Body: `{"ok":true,` +
		`"members":{}}`, Times: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.find(t, "bob"); !errors.As(err, &me) || me.Outcome != string(ClassUnknown) {
		t.Errorf("an answer that is not a page = %v", err)
	}
}

// TestFindUserPages: a cursor that never ends stops after maxUserPages pages, refused as unknown.
func TestFindUserPages(t *testing.T) {
	var (
		mu    sync.Mutex
		pages int
	)
	stand := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		pages++
		n := pages
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"ok":true,"members":[{"id":"U0SOMEONE%d","name":"x%d"}],`+
			`"response_metadata":{"next_cursor":"c%d"}}`, n, n, n)
	}))
	t.Cleanup(stand.Close)
	e := newLinkEnvAt(t, nil, to(stand.URL+"/api/"))
	var me *accountlinks.MessengerError
	if _, err := e.find(t, "bob"); !errors.As(err, &me) || me.Call != "users.list" || pages != maxUserPages {
		t.Errorf("an endless cursor = %v after %d pages", err, pages)
	}
}

// TestSendCode is C-24.FR-12, F-096 and C-23.FR-3: the code goes by chat.postMessage to the person's user id, which
// lands in the bot's direct chat with them, as text escaped for mrkdwn and without unfurling; its record in the
// Request log has the code masked in the request and in Slack's answer, which repeats the message.
func TestSendCode(t *testing.T) {
	e := newLinkEnv(t)
	const code = "ABCD2345"
	text := "You requested a link to Muster. Do not share this code.\n\n" + code
	if err := e.run(t, func(ctx context.Context, b accountlinks.SlackBot) error {
		return b.SendCode(ctx, fakeslack.UserAlice, code, text)
	}); err != nil {
		t.Fatal(err)
	}
	dms := e.f.DirectMessages(fakeslack.AppB, fakeslack.UserAlice)
	if len(dms) != 1 || dms[0].Text != text || dms[0].User != fakeslack.Apps()[1].BotUserID ||
		dms[0].Channel != fakeslack.DMChannel(fakeslack.UserAlice) || dms[0].UnfurlLinks == nil || *dms[0].UnfurlLinks ||
		len(e.f.DirectMessages(fakeslack.AppA, fakeslack.UserAlice)) != 0 {
		t.Fatalf("direct messages %+v", dms)
	}
	recs := e.rec.got()
	if len(recs) != 1 || recs[0].Operation != "chat.postMessage" ||
		!strings.Contains(string(recs[0].RequestBody), `"channel":"`+fakeslack.UserAlice+`"`) {
		t.Fatalf("records %+v", recs)
	}
	for name, body := range map[string][]byte{"request": recs[0].RequestBody, "answer": recs[0].ResponseBody} {
		if strings.Contains(string(body), code) || !strings.Contains(string(body), "[redacted]") {
			t.Errorf("the %s body %s", name, body)
		}
	}
	if err := e.run(t, func(ctx context.Context, b accountlinks.SlackBot) error {
		return b.SendLinked(ctx, fakeslack.UserAlice, "Linked to Muster user <!channel> & *x*")
	}); err != nil {
		t.Fatal(err)
	}
	if dms := e.f.DirectMessages(fakeslack.AppB, fakeslack.UserAlice); len(dms) != 2 ||
		dms[1].Text != "Linked to Muster user &lt;!channel&gt; &amp; *x*" {
		t.Errorf("the confirmation %+v", dms)
	}
}

// TestSendCodeRefused is C-24.FR-12 and F-096: Slack refusing the direct message for the person — a bot, the app's
// own bot user, a deleted account and an id Slack does not know — is accountlinks.ErrDirectMessageRefused, which
// leaves the Connection as it is; any other failure is a refusal that names the call and carries no text of Slack's,
// and its record has the code masked.
func TestSendCodeRefused(t *testing.T) {
	e := newLinkEnv(t)
	const code = "ABCD2345"
	send := func(user string) error {
		return e.run(t, func(ctx context.Context, b accountlinks.SlackBot) error {
			return b.SendCode(ctx, user, code, "code "+code)
		})
	}
	for _, user := range []string{fakeslack.UserHelper, fakeslack.Apps()[1].BotUserID, fakeslack.UserBobOld,
		"U0NOBODY"} {
		if err := send(user); !errors.Is(err, accountlinks.ErrDirectMessageRefused) {
			t.Errorf("SendCode(%s) = %v", user, err)
		}
		if o := e.path.last(); o.Kind != delivery.OutcomeOK {
			t.Errorf("a refusal for %s changed the outcome %+v", user, o)
		}
	}
	if err := e.f.SetFault(fakeserver.Fault{Path: "/api/chat.postMessage", Status: http.StatusOK,
		Body: `{"ok":false,"error":"internal_error","warning":"code ` + code + `"}`, Times: 1}); err != nil {
		t.Fatal(err)
	}
	err := send(fakeslack.UserAlice)
	var me *accountlinks.MessengerError
	if !errors.As(err, &me) || me.Call != "the direct message" || me.Outcome != string(ClassTransient) ||
		strings.Contains(err.Error(), code) {
		t.Errorf("a failed direct message = %v", err)
	}
	if o := e.path.last(); o.Kind != delivery.OutcomeTransient || o.Error != "" {
		t.Errorf("the outcome %+v", o)
	}
	for _, r := range e.rec.got() {
		if strings.Contains(string(r.RequestBody)+string(r.ResponseBody)+r.Error, code) {
			t.Errorf("a record with the code %+v", r)
		}
	}
	if err := e.run(t, func(ctx context.Context, b accountlinks.SlackBot) error {
		return b.SendLinked(ctx, fakeslack.UserHelper, "Linked")
	}); !errors.Is(err, accountlinks.ErrDirectMessageRefused) {
		t.Errorf("a refused confirmation = %v", err)
	}
}

// TestLinkSession: a Session takes a limiter token of its Connection on the interactive path; a Connection that is
// unknown, deleted or not a Slack one is accountlinks.ErrNoConnection, any other failure to find it is returned, and
// no token within the budget runs nothing.
func TestLinkSession(t *testing.T) {
	e := newLinkEnv(t)
	ran := false
	f := func(context.Context, accountlinks.SlackBot) error { ran = true; return nil }
	e.conns.err = ErrNoConnection
	if err := e.bots.Session(t.Context(), linkConnID, f); !errors.Is(err, accountlinks.ErrNoConnection) || ran {
		t.Errorf("an unknown Connection = %v", err)
	}
	boom := errors.New("boom")
	e.conns.err = boom
	if err := e.bots.Session(t.Context(), linkConnID, f); !errors.Is(err, boom) || ran {
		t.Errorf("a failed lookup = %v", err)
	}
	e.conns.err = nil
	e.path.err = &delivery.LimitedError{RetryAfter: time.Second}
	var le *delivery.LimitedError
	if err := e.bots.Session(t.Context(), linkConnID, f); !errors.As(err, &le) || ran {
		t.Errorf("no token = %v", err)
	}
	e.path.err = nil
	if err := e.bots.Session(t.Context(), linkConnID, f); err != nil || !ran ||
		e.conns.asked[len(e.conns.asked)-1] != linkConnID {
		t.Errorf("a Session = %v, ran %v", err, ran)
	}
	if err := e.bots.Session(t.Context(), linkConnID, func(context.Context, accountlinks.SlackBot) error {
		return boom
	}); !errors.Is(err, boom) {
		t.Errorf("the error of f = %v", err)
	}
}

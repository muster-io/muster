// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/fakes/fakeproxy"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/outbound"
)

// destPublicID is the public_id of the Destination of the tests.
const destPublicID = "DS0123456789AB"

// The answers of the stand-in bridge by default.
const (
	postedAnswer  = `{"message_id":"m1","url":"https://chat.example.org/m/m1","truncated":false,"replayed":false}`
	editedAnswer  = `{"truncated":false}`
	repliedAnswer = `{"message_id":"r1","thread_ref":"t-m1","truncated":false,"replayed":false}`
	checkedAnswer = `{"channel_id":"ch-alerts","name":"alerts"}`
)

// opOf is the protocol operation of a request to the stand-in bridge.
func opOf(method, path string) string {
	p := path[strings.Index(path, "/v1/")+len("/v1/"):]
	switch {
	case p == "info":
		return "info"
	case p == "messages":
		return "post"
	case strings.HasSuffix(p, "/thread-replies"):
		return "reply"
	case strings.HasPrefix(p, "channels/"):
		return "check"
	case method == http.MethodPatch:
		return "edit"
	}
	return "other"
}

// messageBridge is a stand-in bridge that answers info, postMessage, editMessage, replyInThread and checkChannel,
// each as set.
type messageBridge struct {
	*standIn
	mu      sync.Mutex
	answers map[string]http.HandlerFunc
}

func startMessageBridge(t *testing.T, info []byte) *messageBridge {
	t.Helper()
	mb := &messageBridge{answers: map[string]http.HandlerFunc{
		"info":  answerJSON(http.StatusOK, "application/json", string(info)),
		"post":  answerJSON(http.StatusCreated, "application/json", postedAnswer),
		"edit":  answerJSON(http.StatusOK, "application/json", editedAnswer),
		"reply": answerJSON(http.StatusCreated, "application/json", repliedAnswer),
		"check": answerJSON(http.StatusOK, "application/json", checkedAnswer),
	}}
	mb.standIn = startStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		mb.mu.Lock()
		answer, ok := mb.answers[opOf(r.Method, r.URL.Path)]
		mb.mu.Unlock()
		if !ok {
			answer = answerJSON(http.StatusNotFound, "text/plain", "no such path")
		}
		answer(w, r)
	})
	return mb
}

// answer sets the answer of the operation op.
func (mb *messageBridge) answer(op string, h http.HandlerFunc) {
	mb.mu.Lock()
	mb.answers[op] = h
	mb.mu.Unlock()
}

// requests are the requests of the operation op.
func (mb *messageBridge) requests(op string) []seen {
	var out []seen
	for _, s := range mb.seen() {
		if opOf(s.Method, s.Path) == op {
			out = append(out, s)
		}
	}
	return out
}

// targets are the Targets of the tests: Destinations by id, and the info of their Connections through an InfoCache,
// or info and failed reads as set.
type targets struct {
	mu     sync.Mutex
	byID   map[int64]Target
	err    error
	cache  *InfoCache
	fixed  *Info
	failed *Result
}

func (ts *targets) BridgeTarget(_ context.Context, id int64) (Target, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.err != nil {
		return Target{}, ts.err
	}
	t, ok := ts.byID[id]
	if !ok {
		return Target{}, ErrNoTarget
	}
	return t, nil
}

func (ts *targets) BridgeInfo(ctx context.Context, conn Conn, class outbound.Class) (Info, Result) {
	ts.mu.Lock()
	fixed, failed := ts.fixed, ts.failed
	ts.mu.Unlock()
	switch {
	case failed != nil:
		return Info{}, *failed
	case fixed != nil:
		return *fixed, Result{}
	}
	return ts.cache.Info(ctx, conn, class)
}

// adapterEnv is a Bridge delivery whose Destination 1 posts to ch-alerts through the stand-in bridge.
type adapterEnv struct {
	bridge  *messageBridge
	client  *Client
	targets *targets
	adapter *Adapter
	clock   *clock.Manual
	store   *memStore
	log     *bytes.Buffer
}

func newAdapterEnv(t *testing.T, mutate ...func(map[string]any)) *adapterEnv {
	t.Helper()
	e := &adapterEnv{clock: clock.NewManual(time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)), store: &memStore{},
		log: &bytes.Buffer{}}
	e.bridge = startMessageBridge(t, validInfo(t, func(m map[string]any) {
		for _, f := range mutate {
			f(m)
		}
	}))
	e.client = newClient(t, network(t, e.log), Settings{BaseURL: e.bridge.srv.URL, Token: testToken})
	e.targets = &targets{byID: map[int64]Target{1: {Conn: Conn{ID: 1, PublicID: "CB0123456789AB", Version: 1,
		Client: e.client}, ChannelID: "ch-alerts"}},
		cache: NewInfoCache(e.store, e.clock, 0, logging.New(e.log, logging.LevelInfo))}
	e.adapter = &Adapter{Targets: e.targets}
	return e
}

// call is a call of the delivery worker for Destination 1.
func call(loud groups.Loudness, key string) delivery.Call {
	conn := int64(1)
	return delivery.Call{Class: outbound.ClassDelivery, Loudness: loud, Key: key, Destination: delivery.Destination{
		ID: 1, PublicID: destPublicID, Name: "alerts", Type: delivery.TypeBridge, Connection: &conn}}
}

// decode reads the JSON body of a request.
func decode(t *testing.T, s seen) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s.Body), &m); err != nil {
		t.Fatalf("%s %s: %v", s.Method, s.Path, err)
	}
	return m
}

// buttonsSent are the buttons of a decoded envelope as label:style.
func buttonsSent(t *testing.T, env map[string]any) []string {
	t.Helper()
	var out []string
	for _, b := range env["buttons"].([]any) {
		b := b.(map[string]any)
		if d := b["data"].(string); len(d) > 64 || d == "" {
			t.Errorf("data %q", d)
		}
		out = append(out, b["label"].(string)+":"+b["style"].(string))
	}
	return out
}

// TestPublishEnvelope covers C-22.FR-4, FR-5, FR-6 and AC-4: a new Alert Group is one postMessage of the kind root
// with its key, the status, colour and emoji, the title and link, the CommonMark body after the Mentions of a Loud
// call, notify true and the buttons with their style hints and signed data; its outcome names the message, its link
// and its Thread. A Quiet Storm summary is of the kind storm_summary, without Mentions.
func TestPublishEnvelope(t *testing.T) {
	e := newAdapterEnv(t)
	c := call(groups.Loud, "pub-"+destPublicID+"-17-1")
	c.Targets = []mentions.Target{{Kind: mentions.TargetUser, User: &mentions.User{Name: "Alice Smith"}}}
	m := rootMessage(t, messages.ColourFiring)
	e.bridge.answer("post", answerJSON(http.StatusCreated, "application/json",
		`{"message_id":"m1","url":"https://chat.example.org/m/m1","thread_ref":"t-0","truncated":true,"replayed":false}`))
	o := e.adapter.Publish(t.Context(), c, m)
	if o.Kind != delivery.OutcomeOK || o.MessageID != "m1" || o.MessageURL != "https://chat.example.org/m/m1" ||
		o.ThreadRef != "t-0" || o.Replayed || o.Unanswered || o.Status != http.StatusCreated {
		t.Fatalf("outcome %+v", o)
	}
	posts := e.bridge.requests("post")
	if len(posts) != 1 || posts[0].Method != http.MethodPost || posts[0].Path != "/v1/messages" ||
		posts[0].Header.Get("Idempotency-Key") != "pub-"+destPublicID+"-17-1" ||
		posts[0].Header.Get("Authorization") != "Bearer "+testToken {
		t.Fatalf("requests %+v", posts)
	}
	env := decode(t, posts[0])
	link, _ := env["link"].(map[string]any)
	if env["kind"] != "root" || env["channel_id"] != "ch-alerts" || env["status"] != "firing" ||
		env["color"] != "#d32f2f" || env["emoji"] != "🔴" || env["title"] != "#42 DiskFull" || env["notify"] != true ||
		link["url"] != groupPage || link["label"] != "Open in Muster" || fmt.Sprint(env["mentions"]) != "[]" {
		t.Fatalf("envelope %v", env)
	}
	if got := fmt.Sprint(buttonsSent(t, env)); got != "[Ack:primary Resolve:success Snooze 1 h:neutral "+
		"Snooze 4 h:neutral Snooze 24 h:neutral Until tomorrow 09:00:neutral Until Monday 09:00:neutral]" {
		t.Fatalf("buttons %s", got)
	}
	body := env["body"].(string)
	if !strings.HasPrefix(body, "Alice Smith\n\nprod · started") || !strings.Contains(body, "> **alertname** DiskFull") ||
		!strings.Contains(body, "- ~~pod: i2~~") || !strings.Contains(body, "[Open in Muster]("+groupPage+")") ||
		strings.Contains(body, "#42") || strings.Contains(body, "[Ack]") {
		t.Fatalf("body %q", body)
	}
	storm := e.adapter.Publish(t.Context(), call(groups.Quiet, "pub-"+destPublicID+"-18-1"), stormMessage())
	env = decode(t, e.bridge.requests("post")[1])
	if storm.Kind != delivery.OutcomeOK || env["kind"] != "storm_summary" || env["status"] != "storm" ||
		env["notify"] != false || env["body"] != "[Open in Muster]("+routePage+")" {
		t.Fatalf("storm summary %+v %v", storm, env)
	}
}

// TestUpdateEnvelope covers C-22.FR-6 and AC-4: Acknowledge is one editMessage of the message with the whole envelope
// of the acknowledged state — its buttons, no Mentions, no notify — without an idempotency key.
func TestUpdateEnvelope(t *testing.T) {
	e := newAdapterEnv(t)
	o := e.adapter.Update(t.Context(), call(groups.Quiet, ""), "m/1", rootMessage(t, messages.ColourAcknowledged))
	if o.Kind != delivery.OutcomeOK || o.MessageID != "m/1" || o.Status != http.StatusOK {
		t.Fatalf("outcome %+v", o)
	}
	edits := e.bridge.requests("edit")
	if len(edits) != 1 || edits[0].RawPath != "/v1/messages/m%2F1" || edits[0].Header.Get("Idempotency-Key") != "" {
		t.Fatalf("requests %+v", edits)
	}
	env := decode(t, edits[0])
	for _, absent := range []string{"mentions", "notify", "kind", "channel_id"} {
		if _, ok := env[absent]; ok {
			t.Errorf("an edit has %s", absent)
		}
	}
	if env["status"] != "acknowledged" || env["color"] != "#f57c00" || env["emoji"] != "🟠" ||
		env["title"] != "#42 DiskFull" || !strings.HasSuffix(env["body"].(string), "Acknowledged by Bob Smith") {
		t.Fatalf("envelope %v", env)
	}
	if got := fmt.Sprint(buttonsSent(t, env)); got != "[Unack:neutral Resolve:success Snooze 1 h:neutral "+
		"Snooze 4 h:neutral Snooze 24 h:neutral Until tomorrow 09:00:neutral Until Monday 09:00:neutral]" {
		t.Fatalf("buttons %s", got)
	}
	e.bridge.answer("edit", answerJSON(http.StatusOK, "application/json", `{"truncated":true}`))
	if o := e.adapter.Update(t.Context(), call(groups.Quiet, ""), "m1", rootMessage(t, messages.ColourResolved)); o.Kind !=
		delivery.OutcomeOK {
		t.Fatalf("a truncated edit = %+v", o)
	}
	if env := decode(t, e.bridge.requests("edit")[1]); fmt.Sprint(env["buttons"]) != "[]" {
		t.Fatalf("a resolved message has buttons: %v", env["buttons"])
	}
}

// TestReplyThreadRef covers C-22.FR-6 and AC-4: a Thread reply goes under the Root message with its own key and the
// thread_ref of the Root message — null without one — and its outcome returns the thread_ref the bridge answered; a
// Loud reply notifies, with its Mentions first, and a Reminder carries its buttons.
func TestReplyThreadRef(t *testing.T) {
	e := newAdapterEnv(t)
	o := e.adapter.Reply(t.Context(), call(groups.Quiet, "reply-"+destPublicID+"-5"), delivery.Root{MessageID: "m1"},
		replyMessage("New Alert: pod i3"))
	if o.Kind != delivery.OutcomeOK || o.MessageID != "r1" || o.ThreadRef != "t-m1" || o.Replayed {
		t.Fatalf("outcome %+v", o)
	}
	e.bridge.answer("reply", answerJSON(http.StatusCreated, "application/json",
		`{"message_id":"r2","thread_ref":"t-m1b","truncated":false,"replayed":true}`))
	c := call(groups.Loud, "reply-"+destPublicID+"-6")
	c.Targets = []mentions.Target{{Kind: mentions.TargetUser, User: &mentions.User{Name: "Alice Smith"}}}
	reminder := replyMessage("Still unacknowledged")
	reminder.Buttons = signedButtons(t, messages.ColourAcknowledged, "en", nil)
	o = e.adapter.Reply(t.Context(), c, delivery.Root{MessageID: "m1", ThreadAnchorID: o.ThreadRef}, reminder)
	if o.Kind != delivery.OutcomeOK || o.MessageID != "r2" || o.ThreadRef != "t-m1b" || !o.Replayed {
		t.Fatalf("second outcome %+v", o)
	}
	replies := e.bridge.requests("reply")
	if len(replies) != 2 || replies[0].Path != "/v1/messages/m1/thread-replies" ||
		replies[0].Header.Get("Idempotency-Key") != "reply-"+destPublicID+"-5" ||
		replies[1].Header.Get("Idempotency-Key") != "reply-"+destPublicID+"-6" {
		t.Fatalf("requests %+v", replies)
	}
	first, second := decode(t, replies[0]), decode(t, replies[1])
	if v, ok := first["thread_ref"]; !ok || v != nil || first["notify"] != false || first["body"] != "New Alert: pod i3" ||
		fmt.Sprint(first["buttons"]) != "[]" || fmt.Sprint(first["mentions"]) != "[]" {
		t.Fatalf("first reply %v", first)
	}
	if second["thread_ref"] != "t-m1" || second["notify"] != true ||
		second["body"] != "Alice Smith\n\nStill unacknowledged" ||
		fmt.Sprint(buttonsSent(t, second)) != "[Unack:neutral Resolve:success]" {
		t.Fatalf("second reply %v", second)
	}
}

// TestKeys covers C-22.FR-6 and FR-9: a Publication's key is pub-<Destination public_id>-<delivery id>-<publication>,
// a Thread reply's reply-<Destination public_id>-<thread reply id>, the pointer reply's
// pointer-<Destination public_id>-<delivery id>-<publication> and that of a final edit sent as a Thread reply
// final-<Destination public_id>-<delivery id>, all within the protocol's pattern.
func TestKeys(t *testing.T) {
	a := &Adapter{}
	d := delivery.Destination{ID: 4, PublicID: destPublicID}
	pattern := regexp.MustCompile(`^[!-~]{1,255}$`)
	pub, reply := a.PublicationKey(d, 17, 3), a.ReplyKey(d, 99)
	if pub != "pub-DS0123456789AB-17-3" || reply != "reply-DS0123456789AB-99" || !pattern.MatchString(pub) ||
		!pattern.MatchString(reply) || a.PublicationKey(d, 17, 3) != pub {
		t.Fatalf("keys %q %q", pub, reply)
	}
	pointer, final := a.PointerKey(d, 17, 3), a.FinalKey(d, 17)
	if pointer != "pointer-DS0123456789AB-17-3" || final != "final-DS0123456789AB-17" ||
		!pattern.MatchString(pointer) || !pattern.MatchString(final) {
		t.Fatalf("keys %q %q", pointer, final)
	}
	if a.LengthLimit() != MinBodyLength {
		t.Fatalf("length limit %d", a.LengthLimit())
	}
	e := newAdapterEnv(t)
	if o := e.adapter.Publish(t.Context(), call(groups.Quiet, ""), rootMessage(t, messages.ColourFiring)); o.Kind !=
		delivery.OutcomeOK || e.bridge.requests("post")[0].Header.Values("Idempotency-Key") != nil {
		t.Fatalf("a call without a key = %+v", o)
	}
}

// TestUnsafeURL covers C-22.FR-6: the answer's url is the message's link only when messages.SafeURL accepts it.
func TestUnsafeURL(t *testing.T) {
	e := newAdapterEnv(t)
	for url, want := range map[string]string{
		`"javascript:alert(1)"`:           "",
		`"https://chat.example.org/m/m1"`: "https://chat.example.org/m/m1",
		`null`:                            "",
		`"/m/m1"`:                         "",
	} {
		e.bridge.answer("post", answerJSON(http.StatusCreated, "application/json",
			`{"message_id":"m1","url":`+url+`,"truncated":false,"replayed":false}`))
		if o := e.adapter.Publish(t.Context(), call(groups.Quiet, "k"), rootMessage(t, messages.ColourFiring)); o.Kind !=
			delivery.OutcomeOK || o.MessageURL != want {
			t.Errorf("url %s: %+v", url, o)
		}
	}
}

// fired is a context whose deadline passes when fire is called, so that a call times out after its request was
// sent without waiting for real time.
type fired struct {
	context.Context
	done chan struct{}
	once sync.Once
}

func newFired(parent context.Context) *fired {
	return &fired{Context: parent, done: make(chan struct{})}
}

func (f *fired) Done() <-chan struct{} { return f.done }

func (f *fired) Err() error {
	select {
	case <-f.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func (f *fired) fire() { f.once.Do(func() { close(f.done) }) }

// TestErrorMapping covers C-22.FR-8 and AC-7 on each call: transient is Transient; rate_limited waits exactly its
// Retry-After for the Destination, or with the scope bot for the Connection, and is Transient without one or with one
// longer than bridge.retry_after_max; gone is Gone on an edit and on a reply, and on an edit with the reason
// edit_window_closed OutcomeEditWindowClosed (C-22.AC-9); forbidden is Fatal with the bridge's
// detail, masked; invalid, an answer that is not the protocol's and a redirect are unknown; a call without an answer is
// Transient, Unanswered when its request was written and may have reached the bridge — never when it was refused or
// timed out while connecting, in a TLS handshake or a proxy's CONNECT — and an address the policy refuses is Fatal.
func TestErrorMapping(t *testing.T) {
	problem := func(status int, body string, header ...string) http.HandlerFunc {
		return answerJSON(status, problemJSON, body, header...)
	}
	for _, tc := range []struct {
		name    string
		op      string
		answer  http.HandlerFunc
		kind    delivery.OutcomeKind
		retry   time.Duration
		scope   delivery.Scope
		message string
	}{
		{"transient", "post", problem(500, `{"class":"transient","detail":"The messenger is down."}`),
			delivery.OutcomeTransient, 0, "", "The messenger is down."},
		{"rate limited", "post", problem(429, `{"class":"rate_limited"}`, "Retry-After", "7"),
			delivery.OutcomeRetryAfter, 7 * time.Second, delivery.ScopeDestination, ""},
		{"rate limited for the bot", "edit", problem(429, `{"class":"rate_limited","scope":"bot"}`, "Retry-After", "7"),
			delivery.OutcomeRetryAfter, 7 * time.Second, delivery.ScopeConnection, ""},
		{"rate limited too long", "reply", problem(429, `{"class":"rate_limited"}`, "Retry-After", "901"),
			delivery.OutcomeTransient, 0, "", ""},
		{"429 without Retry-After", "post", answerJSON(429, "text/plain", "slow down"), delivery.OutcomeTransient, 0,
			"", ""},
		{"gone on an edit", "edit", problem(404, `{"class":"gone","detail":"The message no longer exists."}`),
			delivery.OutcomeGone, 0, "", "The message no longer exists."},
		{"gone with edit_window_closed", "edit", problem(404, `{"class":"gone","reason":"edit_window_closed"}`),
			delivery.OutcomeEditWindowClosed, 0, "", ""},
		{"gone on a reply", "reply", problem(404, `{"class":"gone"}`), delivery.OutcomeGone, 0, "", ""},
		{"gone with edit_window_closed on a reply", "reply",
			problem(404, `{"class":"gone","reason":"edit_window_closed"}`), delivery.OutcomeGone, 0, "", ""},
		{"forbidden", "post", problem(403, `{"class":"forbidden","detail":"The bot was removed from this channel."}`),
			delivery.OutcomeFatal, 0, "", "The bot was removed from this channel."},
		{"forbidden echoing the token", "edit", problem(403, `{"class":"forbidden","detail":"bad token `+testToken+`"}`),
			delivery.OutcomeFatal, 0, "", "bad token [redacted]"},
		{"401", "post", answerJSON(401, "text/plain", "no"), delivery.OutcomeFatal, 0, "", ""},
		{"invalid", "post", problem(400, `{"class":"invalid","detail":"The body is too long."}`),
			delivery.OutcomeUnknown, 0, "", "The body is too long."},
		{"404 is not gone", "edit", answerJSON(404, "text/plain", "not found"), delivery.OutcomeUnknown, 0, "", ""},
		{"not the expected JSON", "post", answerJSON(201, "text/html", "<p>ok</p>"), delivery.OutcomeUnknown, 0, "",
			"POST messages: the answer is not the expected JSON"},
		{"no message_id", "post", answerJSON(201, "application/json", `{"message_id":"","replayed":false}`),
			delivery.OutcomeUnknown, 0, "", "POST messages: the answer is not the expected JSON"},
		{"a thread_ref too long", "post", answerJSON(201, "application/json",
			`{"message_id":"m1","thread_ref":"`+strings.Repeat("t", 513)+`"}`), delivery.OutcomeUnknown, 0, "", ""},
		{"a reply without thread_ref", "reply", answerJSON(201, "application/json", `{"message_id":"r1"}`),
			delivery.OutcomeUnknown, 0, "", "POST messages/{message_id}/thread-replies: the answer is not the " +
				"expected JSON"},
		{"redirect", "edit", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://127.0.0.1:1/x", http.StatusFound)
		}, delivery.OutcomeUnknown, 0, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newAdapterEnv(t)
			e.bridge.answer(tc.op, tc.answer)
			o := callOp(t, e.adapter, tc.op, t.Context())
			if o.Kind != tc.kind || o.RetryAfter != tc.retry || o.Scope != tc.scope || o.Unanswered ||
				(tc.message != "" && string(o.Error) != tc.message) || strings.Contains(string(o.Error), testToken) ||
				o.MessageID != "" {
				t.Fatalf("outcome %+v", o)
			}
			if strings.Contains(e.log.String(), testToken) {
				t.Fatalf("the token leaked into the log: %s", e.log.String())
			}
		})
	}
	t.Run("without an answer", func(t *testing.T) {
		ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		refused := "http://" + ln.Addr().String()
		_ = ln.Close()
		strict, err := outbound.ParsePolicy("standard", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		blocked := network(t, nil)
		blocked.Policy = outbound.StaticPolicy(strict)
		for _, op := range []string{"post", "edit", "reply"} {
			for _, tc := range []struct {
				name       string
				answer     func(f *fired) http.HandlerFunc
				n          Network
				base       string
				proxy      string
				silent     string
				kind       delivery.OutcomeKind
				unanswered bool
			}{
				{name: "connection lost after the request", answer: func(*fired) http.HandlerFunc {
					return func(w http.ResponseWriter, _ *http.Request) {
						conn, _, err := http.NewResponseController(w).Hijack()
						if err == nil {
							_ = conn.Close()
						}
					}
				}, kind: delivery.OutcomeTransient, unanswered: true},
				{name: "timeout after the request", answer: func(f *fired) http.HandlerFunc {
					return func(_ http.ResponseWriter, r *http.Request) {
						f.fire()
						<-r.Context().Done()
					}
				}, kind: delivery.OutcomeTransient, unanswered: true},
				{name: "refused", base: refused, kind: delivery.OutcomeTransient},
				{name: "proxy down", proxy: strings.TrimPrefix(refused, "http://"), kind: delivery.OutcomeTransient},
				{name: "timeout in the TLS handshake", silent: "bridge", kind: delivery.OutcomeTransient},
				{name: "timeout in the proxy's CONNECT", silent: "proxy", kind: delivery.OutcomeTransient},
				{name: "blocked", n: blocked, kind: delivery.OutcomeFatal},
			} {
				t.Run(op+" "+tc.name, func(t *testing.T) {
					e := newAdapterEnv(t)
					info := infoOf(t)
					e.targets.fixed = &info
					ctx := newFired(t.Context())
					if tc.answer != nil {
						e.bridge.answer(op, tc.answer(ctx))
					}
					n, base := network(t, e.log), e.bridge.srv.URL
					if tc.n.Policy != nil {
						n = tc.n
					}
					if tc.base != "" {
						base = tc.base
					}
					proxy := tc.proxy
					switch tc.silent {
					case "bridge":
						base = "https://" + silent(t, ctx)
					case "proxy":
						base, proxy = "https://127.0.0.1:1", silent(t, ctx)
					}
					settings := Settings{BaseURL: base, Token: testToken}
					if proxy != "" {
						settings.Proxy = &outbound.Proxy{Type: outbound.ProxyHTTP, Address: proxy}
					}
					e.targets.byID[1] = Target{Conn: Conn{ID: 1, Version: 1, Client: newClient(t, n, settings)},
						ChannelID: "ch-alerts"}
					o := callOp(t, e.adapter, op, ctx)
					if o.Kind != tc.kind || o.Unanswered != tc.unanswered || o.Status != 0 ||
						strings.Contains(string(o.Error), testToken) {
						t.Fatalf("outcome %+v", o)
					}
				})
			}
		}
	})
}

// silent listens on loopback and accepts connections that never answer, a TLS handshake or a proxy's CONNECT that
// hangs: the first connection fires f, as a timeout while connecting. It returns the address.
func silent(t *testing.T, f *fired) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			f.fire()
		}
	}()
	return ln.Addr().String()
}

// callOp makes the call of the delivery worker for op — post, edit or reply — of a firing Root message.
func callOp(t *testing.T, a *Adapter, op string, ctx context.Context) delivery.Outcome { //nolint:revive // ctx after t, as the tests read
	t.Helper()
	m := rootMessage(t, messages.ColourFiring)
	switch op {
	case "post":
		return a.Publish(ctx, call(groups.Loud, "pub-"+destPublicID+"-1-1"), m)
	case "edit":
		return a.Update(ctx, call(groups.Quiet, ""), "m1", m)
	}
	return a.Reply(ctx, call(groups.Quiet, "reply-"+destPublicID+"-1"), delivery.Root{MessageID: "m1"},
		replyMessage("New Alert: pod i3"))
}

// TestProxyRefused covers D338 (C-22.FR-2, FR-3, FR-8): a 407 of the Connection's proxy, which refuses Muster's
// credentials, is no answer of the bridge — proxy_refused with Telegram's message in the Connection check and in the
// Destination check, and Transient, never Unanswered, in delivery — and the password never shows; a 407 that a bridge
// reached without a proxy answers is the bridge's own, unknown.
func TestProxyRefused(t *testing.T) {
	const password = "proxy-pw-0hX9"
	e := newAdapterEnv(t)
	proxy, err := fakeproxy.Start(t.Context(), fakeproxy.HTTP, "127.0.0.1:0",
		fakeproxy.Options{Username: "muster", Password: "the-right-one"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })
	c := newClient(t, network(t, e.log), Settings{BaseURL: e.bridge.srv.URL, Token: testToken,
		Proxy: &outbound.Proxy{Type: outbound.ProxyHTTP, Address: proxy.Addr(), Username: "muster",
			Password: password}})
	_, r := c.GetInfo(t.Context(), outbound.ClassInteractive)
	if r.OK() || r.CheckCode() != delivery.CodeProxyRefused || r.Error != MessageProxyRefused || r.Status != 0 ||
		r.Network != outbound.NetworkProxyAuth || r.Unanswered || r.Outcome().Kind != delivery.OutcomeTransient {
		t.Fatalf("the Connection check's info = %+v", r)
	}
	check, err := CheckDestination(t.Context(), c, direct, "ch-alerts")
	if err != nil || check.OK() || check.Code != delivery.CodeProxyRefused || check.Message != MessageProxyRefused ||
		check.Outcome.Kind != delivery.OutcomeTransient {
		t.Fatalf("the Destination check = %+v, %v", check, err)
	}
	info := infoOf(t)
	e.targets.fixed = &info
	e.targets.byID[1] = Target{Conn: Conn{ID: 1, Version: 1, Client: c}, ChannelID: "ch-alerts"}
	for _, op := range []string{"post", "edit", "reply"} {
		if o := callOp(t, e.adapter, op, t.Context()); o.Kind != delivery.OutcomeTransient || o.Unanswered ||
			o.Error != MessageProxyRefused || o.Status != 0 {
			t.Fatalf("%s = %+v", op, o)
		}
	}
	if o := e.adapter.Check(t.Context(), call(groups.Quiet, "")); o.Kind != delivery.OutcomeTransient ||
		o.Error != MessageProxyRefused {
		t.Fatalf("the probe = %+v", o)
	}
	if len(e.bridge.seen()) != 0 || proxy.Refused() != 6 || strings.Contains(e.log.String(), password) {
		t.Fatalf("bridge %d, refused %d, log %s", len(e.bridge.seen()), proxy.Refused(), e.log.String())
	}
	own := newAdapterEnv(t)
	own.bridge.answer("post", answerJSON(http.StatusProxyAuthRequired, "text/plain", "who?"))
	if o := callOp(t, own.adapter, "post", t.Context()); o.Kind != delivery.OutcomeUnknown ||
		o.Status != http.StatusProxyAuthRequired {
		t.Fatalf("a bridge's own 407 = %+v", o)
	}
}

// longMessage is an acknowledged Root message with 40 Alerts.
func longMessage(t *testing.T) messages.Message {
	t.Helper()
	m := rootMessage(t, messages.ColourAcknowledged)
	m.Alerts.Lines = nil
	for n := 1; n <= 40; n++ {
		m.Alerts.Lines = append(m.Alerts.Lines, messages.AlertLine{Text: fmt.Sprintf("pod: m%d · instance: "+
			"host-%02d.example.org:9100", n, n)})
	}
	m.Alerts.Distinct = []messages.Distinct{{Label: "pod", Values: []string{"m1", "m2", "m3"}, More: 37}}
	return m
}

// TestBodyFitsMaxBodyLength covers C-22.FR-7 and AC-10: with max_body_length 1,000, an Alert Group with 40 Alerts
// posts a body of at most 1,000 code points that keeps its link line, notices and footer, with the title, link and
// buttons in the envelope; truncated from the bridge is delivered.
func TestBodyFitsMaxBodyLength(t *testing.T) {
	e := newAdapterEnv(t, set("limits", "max_body_length", 1000))
	e.bridge.answer("post", answerJSON(http.StatusCreated, "application/json",
		`{"message_id":"m1","truncated":true,"replayed":false}`))
	if o := e.adapter.Publish(t.Context(), call(groups.Quiet, "k"), longMessage(t)); o.Kind != delivery.OutcomeOK ||
		o.MessageID != "m1" {
		t.Fatalf("outcome %+v", o)
	}
	env := decode(t, e.bridge.requests("post")[0])
	body := env["body"].(string)
	if n := utf8.RuneCountInString(body); n > 1000 || !strings.Contains(body, "[Open in Muster]("+groupPage+") · "+
		"[Runbook]("+runbook+")") || !strings.Contains(body, "🔁 Reopened ×2") ||
		!strings.HasSuffix(body, "Acknowledged by Bob Smith") || strings.Contains(body, "m40") {
		t.Fatalf("%d code points: %q", n, body)
	}
	if env["title"] != "#42 DiskFull" || env["link"].(map[string]any)["label"] != "Open in Muster" ||
		len(env["buttons"].([]any)) != 7 {
		t.Fatalf("envelope %v", env)
	}
	e.bridge.answer("edit", answerJSON(http.StatusOK, "application/json", `{"truncated":true}`))
	if o := e.adapter.Update(t.Context(), call(groups.Quiet, ""), "m1", longMessage(t)); o.Kind != delivery.OutcomeOK ||
		utf8.RuneCountInString(decode(t, e.bridge.requests("edit")[0])["body"].(string)) > 1000 {
		t.Fatalf("a truncated edit = %+v", o)
	}
}

// TestLimitPerConnection covers C-22.FR-7 and AC-10: the limit is that of the Destination's Connection, read through
// the InfoCache — two Connections with different limits shorten the same message differently — and read again past
// bridge.info_max_age, so a new limit applies.
func TestLimitPerConnection(t *testing.T) {
	e := newAdapterEnv(t, set("limits", "max_body_length", 1000))
	other := startMessageBridge(t, validInfo(t, set("limits", "max_body_length", 4000)))
	e.targets.byID[2] = Target{Conn: Conn{ID: 2, PublicID: "CB9999999999AB", Version: 1,
		Client: newClient(t, network(t, e.log), Settings{BaseURL: other.srv.URL, Token: testToken})},
		ChannelID: "ch-other"}
	second := call(groups.Quiet, "k2")
	second.Destination.ID = 2
	m := longMessage(t)
	for _, c := range []delivery.Call{call(groups.Quiet, "k1"), second} {
		if o := e.adapter.Publish(t.Context(), c, m); o.Kind != delivery.OutcomeOK {
			t.Fatalf("outcome %+v", o)
		}
	}
	short := decode(t, e.bridge.requests("post")[0])["body"].(string)
	long := decode(t, other.requests("post")[0])["body"].(string)
	if utf8.RuneCountInString(short) > 1000 || strings.Contains(short, "- pod: m1 ") ||
		utf8.RuneCountInString(long) > 4000 || !strings.Contains(long, "- pod: m40 · instance") {
		t.Fatalf("1000: %d %q\n4000: %d %q", utf8.RuneCountInString(short), short, utf8.RuneCountInString(long), long)
	}
	e.bridge.answer("info", answerJSON(http.StatusOK, "application/json",
		string(validInfo(t, set("limits", "max_body_length", 4000)))))
	_ = e.adapter.Publish(t.Context(), call(groups.Quiet, "k3"), m)
	if got := decode(t, e.bridge.requests("post")[1])["body"].(string); got != short ||
		len(e.bridge.requests("info")) != 1 {
		t.Fatalf("info read again before its maximum age: %d reads", len(e.bridge.requests("info")))
	}
	e.clock.Advance(DefaultInfoMaxAge)
	_ = e.adapter.Publish(t.Context(), call(groups.Quiet, "k4"), m)
	if got := decode(t, e.bridge.requests("post")[2])["body"].(string); got != long ||
		len(e.bridge.requests("info")) != 2 {
		t.Fatalf("after bridge.info_max_age: %d reads, %q", len(e.bridge.requests("info")), got)
	}
}

// TestInfoReadFailure covers C-22.FR-7 and contract gap K4: a call whose Connection's info cannot be read, without
// an earlier copy, ends as the read classifies it, without posting and never Unanswered; with a stale copy, the call
// goes ahead with it.
func TestInfoReadFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer http.HandlerFunc
		kind   delivery.OutcomeKind
		error  string
	}{
		{"down", answerJSON(500, problemJSON, `{"class":"transient","detail":"down"}`), delivery.OutcomeTransient,
			"down"},
		{"token refused", answerJSON(401, "text/plain", "no"), delivery.OutcomeFatal, MessageTokenInvalid},
		{"another protocol", answerJSON(200, "application/json", `{"protocol_version":"2"}`),
			delivery.OutcomeUnknown, "The bridge speaks protocol version 2; this Muster speaks 1."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newAdapterEnv(t)
			e.bridge.answer("info", tc.answer)
			for _, op := range []string{"post", "edit", "reply"} {
				if o := callOp(t, e.adapter, op, t.Context()); o.Kind != tc.kind || string(o.Error) != tc.error {
					t.Fatalf("%s = %+v", op, o)
				}
			}
			if n := len(e.bridge.seen()) - len(e.bridge.requests("info")); n != 0 {
				t.Fatalf("%d messages posted", n)
			}
		})
	}
	e := newAdapterEnv(t)
	e.targets.failed = &Result{Classification: Classification{Class: ClassTransient}, Unanswered: true,
		Error: "timeout"}
	if o := callOp(t, e.adapter, "post", t.Context()); o.Kind != delivery.OutcomeTransient || o.Unanswered {
		t.Fatalf("a read that timed out = %+v", o)
	}
	e.targets.failed = nil
	if o := callOp(t, e.adapter, "post", t.Context()); o.Kind != delivery.OutcomeOK {
		t.Fatalf("first read = %+v", o)
	}
	e.clock.Advance(DefaultInfoMaxAge)
	e.bridge.answer("info", answerJSON(500, problemJSON, `{"class":"transient"}`))
	if o := callOp(t, e.adapter, "edit", t.Context()); o.Kind != delivery.OutcomeOK || len(e.bridge.requests("edit")) != 1 {
		t.Fatalf("with a stale copy = %+v", o)
	}
}

// TestEditWindow covers C-22.FR-9: the edit window is limits.edit_max_age_seconds of the Connection's info, read
// through the InfoCache; none when the info declares null, when it cannot be read, or without a Target.
func TestEditWindow(t *testing.T) {
	e := newAdapterEnv(t, set("limits", "edit_max_age_seconds", 600))
	if d, ok := e.adapter.EditWindow(t.Context(), call(groups.Quiet, "")); !ok || d != 600*time.Second {
		t.Errorf("600 = %v, %v", d, ok)
	}
	if n := len(e.bridge.requests("info")); n != 1 || len(e.bridge.seen()) != 1 {
		t.Errorf("%d info reads, %d requests", n, len(e.bridge.seen()))
	}
	e = newAdapterEnv(t, set("limits", "edit_max_age_seconds", nil))
	if d, ok := e.adapter.EditWindow(t.Context(), call(groups.Quiet, "")); ok || d != 0 {
		t.Errorf("null = %v, %v", d, ok)
	}
	e = newAdapterEnv(t)
	e.bridge.answer("info", answerJSON(500, problemJSON, `{"class":"transient","detail":"down"}`))
	if d, ok := e.adapter.EditWindow(t.Context(), call(groups.Quiet, "")); ok || d != 0 {
		t.Errorf("unreadable info = %v, %v", d, ok)
	}
	other := call(groups.Quiet, "")
	other.Destination.ID = 9
	if d, ok := e.adapter.EditWindow(t.Context(), other); ok || d != 0 {
		t.Errorf("without a target = %v, %v", d, ok)
	}
}

// TestNoTarget: a Destination without a Bridge Connection is Fatal on every call, and one whose Connection could not
// be read is Transient, both without a request.
func TestNoTarget(t *testing.T) {
	e := newAdapterEnv(t)
	other := call(groups.Quiet, "")
	other.Destination.ID = 9
	for name, f := range map[string]func(context.Context) delivery.Outcome{
		"publish": func(ctx context.Context) delivery.Outcome { return e.adapter.Publish(ctx, other, stormMessage()) },
		"update": func(ctx context.Context) delivery.Outcome {
			return e.adapter.Update(ctx, other, "m1", stormMessage())
		},
		"reply": func(ctx context.Context) delivery.Outcome {
			return e.adapter.Reply(ctx, other, delivery.Root{MessageID: "m1"}, replyMessage("x"))
		},
		"check": func(ctx context.Context) delivery.Outcome { return e.adapter.Check(ctx, other) },
	} {
		if o := f(t.Context()); o.Kind != delivery.OutcomeFatal || o.Error != outbound.Untrusted(ErrNoTarget.Error()) {
			t.Errorf("%s without a target = %+v", name, o)
		}
		e.targets.err = errors.New("the database is down")
		if o := f(t.Context()); o.Kind != delivery.OutcomeTransient ||
			o.Error != "the connection of the destination could not be read" {
			t.Errorf("%s with a failed read = %+v", name, o)
		}
		e.targets.err = nil
	}
	if len(e.bridge.seen()) != 0 {
		t.Fatalf("%d requests", len(e.bridge.seen()))
	}
}

// TestCheckIsTheChannelCheck covers C-22.FR-3 and FR-19: the adapter's check, which the Broken probe runs, is one
// checkChannel in the client class of its call — the delivery class for the probe — which posts nothing; a forbidden
// answer is Fatal with the bridge's detail.
func TestCheckIsTheChannelCheck(t *testing.T) {
	e := newAdapterEnv(t)
	counter := metrics.ClientRequests.With(string(outbound.ClassDelivery), string(outbound.OutcomeOK))
	before := counter.Get()
	if o := e.adapter.Check(t.Context(), call(groups.Quiet, "")); o.Kind != delivery.OutcomeOK {
		t.Fatalf("check = %+v", o)
	}
	checks := e.bridge.requests("check")
	if len(checks) != 1 || len(e.bridge.seen()) != 1 || checks[0].Method != http.MethodPost ||
		checks[0].Path != "/v1/channels/ch-alerts/check" || counter.Get() != before+1 {
		t.Fatalf("requests %+v, delivery requests %d → %d", e.bridge.seen(), before, counter.Get())
	}
	e.bridge.answer("check", answerJSON(403, problemJSON, `{"class":"forbidden","detail":"The bot was removed."}`))
	if o := e.adapter.Check(t.Context(), call(groups.Quiet, "")); o.Kind != delivery.OutcomeFatal ||
		o.Error != "The bot was removed." {
		t.Fatalf("forbidden = %+v", o)
	}
	c := call(groups.Quiet, "")
	c.Class = outbound.ClassHeartbeat
	if o := e.adapter.Check(t.Context(), c); o.Kind != delivery.OutcomeUnknown {
		t.Fatalf("a class the client has not = %+v", o)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/bridge/gen"
	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/fakes/fakeproxy"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/outbound"
)

// testKeyPattern is the idempotency key of a test message: test-<Destination public_id>-<16 bytes in base64url>.
var testKeyPattern = regexp.MustCompile(`^test-` + destPublicID + `-[A-Za-z0-9_-]{22}$`)

// testerEnv is a Tester over the stand-in bridge with Destination 1 on ch-alerts.
type testerEnv struct {
	*adapterEnv
	tester *Tester
	src    *messages.Source
}

func newTesterEnv(t *testing.T, mutate ...func(map[string]any)) *testerEnv {
	t.Helper()
	e := newAdapterEnv(t, mutate...)
	r := messages.New(messages.Config{OrgID: 1, PublicURL: "http://localhost:8080", Business: e.clock,
		Real: clock.Real{}, Keys: testKeys{}, Log: logging.New(&bytes.Buffer{}, logging.LevelInfo)})
	path := deliverytest.Unlimited(1, clock.Clocks{Business: e.clock, Real: e.clock})
	return &testerEnv{adapterEnv: e,
		tester: &Tester{Adapter: e.adapter, Path: path, Renderer: r, Budget: 2 * time.Second, Real: e.clock},
		src: &messages.Source{Number: 7, Title: "CertExpiry", Status: messages.ColourFiring, SeverityLevel: "warning",
			TimeZone: "UTC", StartedAt: e.clock.Now(), Route: messages.RouteRef{Name: "db", Language: "en",
				SnoozeDurations: []int64{3600}}, TotalAlerts: 1,
			Alerts: []messages.SourceAlert{{Fingerprint: "f1", Labels: map[string]string{"alertname": "CertExpiry"},
				StartsAt: e.clock.Now(), Firing: true}}}}
}

// testDest is Destination 1 as the API hands it to a test: without its Connection.
var testDest = delivery.Destination{ID: 1, PublicID: destPublicID, Name: "alerts", Type: delivery.TypeBridge}

func (e *testerEnv) run(t *testing.T) []delivery.TestStep {
	t.Helper()
	steps, err := e.tester.Test(t.Context(), delivery.TestInput{Destination: testDest, Source: e.src})
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].Name != StepMessage {
		t.Fatalf("steps %+v", steps)
	}
	return steps
}

// header is the value of the header name of a request a step shows.
func header(r *delivery.TestRequest, name string) string {
	if i := slices.IndexFunc(r.Headers, func(h [2]string) bool { return h[0] == name }); i >= 0 {
		return r.Headers[i][1]
	}
	return ""
}

// TestTestMessage covers C-22.FR-17 and AC-12: testDestination is one postMessage of the kind test, Quiet, without
// Mentions, marked "🧪 Test message" before its body, with the buttons of its status whose data name the Destination
// as a test, under a key new for each test; the step shows the request with the token masked and the answer.
func TestTestMessage(t *testing.T) {
	e := newTesterEnv(t)
	st := e.run(t)[0]
	if !st.OK() || st.ErrorClass != delivery.ClassNone || st.Error != "" {
		t.Fatalf("step %+v", st)
	}
	posts := e.bridge.requests("post")
	if len(posts) != 1 || posts[0].Path != "/v1/messages" || posts[0].Header.Get("Authorization") != "Bearer "+testToken {
		t.Fatalf("requests %+v", posts)
	}
	key := posts[0].Header.Get("Idempotency-Key")
	if !testKeyPattern.MatchString(key) {
		t.Fatalf("key %q", key)
	}
	var in gen.MessageInput
	if err := json.Unmarshal([]byte(posts[0].Body), &in); err != nil {
		t.Fatal(err)
	}
	if in.Kind != gen.Test || in.Notify || in.ChannelId != "ch-alerts" || in.Status != gen.Firing ||
		in.Title != "#7 CertExpiry" || len(in.Mentions) != 0 || !strings.HasPrefix(in.Body, "🧪 Test message\n\n") {
		t.Fatalf("envelope %+v", in)
	}
	if len(in.Buttons) == 0 {
		t.Fatal("no buttons")
	}
	for _, b := range in.Buttons {
		a, err := buttons.Verify(testKeys{}, b.Data, "")
		if err != nil || a.Subject != buttons.SubjectTest || a.PublicID != destPublicID {
			t.Fatalf("button %+v: %+v %v", b, a, err)
		}
	}
	r := st.Request
	if r == nil || r.Method != http.MethodPost || r.URL != e.bridge.srv.URL+"/v1/messages" ||
		header(r, "Authorization") != "Bearer [redacted]" || header(r, "Content-Type") != "application/json" ||
		header(r, "Idempotency-Key") != key || r.Body == nil || *r.Body != posts[0].Body {
		t.Fatalf("request %+v", r)
	}
	if st.ResponseStatus != http.StatusCreated || st.ResponseBody == nil || *st.ResponseBody != postedAnswer {
		t.Fatalf("response %d %v", st.ResponseStatus, st.ResponseBody)
	}
	if b, _ := json.Marshal(st); strings.Contains(string(b), testToken) {
		t.Fatalf("the token shows: %s", b)
	}
	e.run(t)
	if again := e.bridge.requests("post")[1].Header.Get("Idempotency-Key"); again == key ||
		!testKeyPattern.MatchString(again) {
		t.Fatalf("a second test's key %q after %q", again, key)
	}
	thread := newTesterEnv(t, threadOnly("all"))
	thread.run(t)
	in = gen.MessageInput{}
	if err := json.Unmarshal([]byte(thread.bridge.requests("post")[0].Body), &in); err != nil {
		t.Fatal(err)
	}
	if len(in.Buttons) != 0 || !strings.HasSuffix(in.Body, "Reply in the Thread: ack · resolve · snooze 1h") {
		t.Fatalf("a bridge without buttons: %+v", in)
	}
}

// TestTestMessageFailures: a refusal is the step's class with the bridge's text masked; a request the outbound address
// policy refused is blocked and reaches nothing; a 407 of the Connection's proxy is transient (D338); an info that
// cannot be read fails the step without posting; a step that found no token sends nothing; a Destination without a
// Connection fails; a failed path is an error.
func TestTestMessageFailures(t *testing.T) {
	e := newTesterEnv(t)
	e.bridge.answer("post", answerJSON(http.StatusForbidden, problemJSON,
		`{"class":"forbidden","detail":"The bot `+testToken+` was removed."}`))
	st := e.run(t)[0]
	if st.ErrorClass != string(delivery.OutcomeFatal) || st.Error != "The bot [redacted] was removed." ||
		st.ResponseStatus != http.StatusForbidden || st.ResponseBody == nil ||
		strings.Contains(*st.ResponseBody, testToken) || st.Request == nil {
		t.Fatalf("forbidden %+v %v", st, st.ResponseBody)
	}
	e.bridge.answer("post", answerJSON(http.StatusInternalServerError, problemJSON, `{"class":"transient"}`))
	if st := e.run(t)[0]; st.ErrorClass != string(delivery.OutcomeTransient) || st.ResponseStatus != 500 {
		t.Fatalf("transient %+v", st)
	}

	blocked := newTesterEnv(t)
	strict, err := outbound.ParsePolicy("standard", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	n := network(t, nil)
	n.Policy = outbound.StaticPolicy(strict)
	blocked.targets.byID[1] = Target{Conn: Conn{ID: 1, Version: 1, Client: newClient(t, n,
		Settings{BaseURL: blocked.bridge.srv.URL, Token: testToken})}, ChannelID: "ch-alerts"}
	if st := blocked.run(t)[0]; st.ErrorClass != delivery.ClassBlocked || st.Request != nil ||
		len(blocked.bridge.seen()) != 0 {
		t.Fatalf("blocked info read %+v", st)
	}
	info := infoOf(t)
	blocked.targets.fixed = &info
	if st := blocked.run(t)[0]; st.ErrorClass != delivery.ClassBlocked || st.Request == nil ||
		st.ResponseStatus != 0 || len(blocked.bridge.seen()) != 0 {
		t.Fatalf("blocked post %+v", st)
	}

	const password = "proxy-pw-0hX9"
	proxy, err := fakeproxy.Start(t.Context(), fakeproxy.HTTP, "127.0.0.1:0",
		fakeproxy.Options{Username: "muster", Password: "the-right-one"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })
	refused := newTesterEnv(t)
	refused.targets.fixed = &info
	refused.targets.byID[1] = Target{Conn: Conn{ID: 1, Version: 1, Client: newClient(t, network(t, refused.log),
		Settings{BaseURL: refused.bridge.srv.URL, Token: testToken, Proxy: &outbound.Proxy{Type: outbound.ProxyHTTP,
			Address: proxy.Addr(), Username: "muster", Password: password}})}, ChannelID: "ch-alerts"}
	if st := refused.run(t)[0]; st.ErrorClass != string(delivery.OutcomeTransient) ||
		st.Error != MessageProxyRefused || len(refused.bridge.seen()) != 0 ||
		strings.Contains(refused.log.String(), password) {
		t.Fatalf("proxy refused %+v", st)
	}

	unread := newTesterEnv(t)
	unread.bridge.answer("info", answerJSON(500, problemJSON, `{"class":"transient","detail":"down"}`))
	if st := unread.run(t)[0]; st.ErrorClass != string(delivery.OutcomeTransient) || st.Error != "down" ||
		st.Request != nil || len(unread.bridge.requests("post")) != 0 {
		t.Fatalf("info unread %+v", st)
	}

	e.tester.Path = &path{err: &delivery.LimitedError{RetryAfter: time.Second}}
	before := len(e.bridge.requests("post"))
	if st := e.run(t)[0]; st.ErrorClass != delivery.ClassLimited || st.Request != nil ||
		len(e.bridge.requests("post")) != before {
		t.Fatalf("limited %+v", st)
	}
	e.tester.Path = &path{err: errors.New("the database is down")}
	if _, err := e.tester.Test(t.Context(), delivery.TestInput{Destination: testDest, Source: e.src}); err == nil {
		t.Fatal("a failed path is no error")
	}
	other := testDest
	other.ID = 9
	if steps, err := e.tester.Test(t.Context(), delivery.TestInput{Destination: other, Source: e.src}); err != nil ||
		len(steps) != 1 || steps[0].ErrorClass != string(delivery.OutcomeFatal) {
		t.Fatalf("no target %+v %v", steps, err)
	}
	e.targets.err = errors.New("the database is down")
	if st := e.run(t)[0]; st.ErrorClass != string(delivery.OutcomeTransient) {
		t.Fatalf("a target that cannot be read %+v", st)
	}
}

// preview is the envelope of the one item of a preview.
func preview(t *testing.T, items []delivery.PreviewItem) gen.MessageInput {
	t.Helper()
	if len(items) != 1 || items[0].Name != StepMessage || items[0].Format != FormatJSON || items[0].Text == nil ||
		items[0].Request != nil {
		t.Fatalf("items %+v", items)
	}
	var in gen.MessageInput
	if err := json.Unmarshal([]byte(*items[0].Text), &in); err != nil {
		t.Fatal(err)
	}
	return in
}

// TestPreview covers C-22.FR-17 and AC-12: previewDestination is the envelope the test would post, as JSON in the item
// message, and posts nothing; without the Connection's info its body has the protocol's floor and no buttons.
func TestPreview(t *testing.T) {
	e := newTesterEnv(t)
	items, err := e.tester.Preview(t.Context(), delivery.TestInput{Destination: testDest, Source: e.src})
	if err != nil {
		t.Fatal(err)
	}
	in := preview(t, items)
	if in.Kind != gen.Test || in.Notify || in.ChannelId != "ch-alerts" || in.Title != "#7 CertExpiry" ||
		!strings.HasPrefix(in.Body, "🧪 Test message\n\n") || len(in.Buttons) == 0 {
		t.Fatalf("envelope %+v", in)
	}
	if !strings.Contains(*items[0].Text, "\n  \"kind\": \"test\"") {
		t.Fatalf("not indented: %s", *items[0].Text)
	}
	if n := len(e.bridge.seen()) - len(e.bridge.requests("info")); n != 0 {
		t.Fatalf("%d requests besides info", n)
	}
	unread := newTesterEnv(t)
	unread.bridge.answer("info", answerJSON(500, problemJSON, `{"class":"transient"}`))
	unread.src.Alerts = nil
	for n := range 60 {
		unread.src.Alerts = append(unread.src.Alerts, messages.SourceAlert{Fingerprint: fmt.Sprintf("f%d", n),
			Labels: map[string]string{"alertname": "CertExpiry", "pod": fmt.Sprintf("pod-%02d-%s", n,
				strings.Repeat("p", 40))}, StartsAt: unread.clock.Now(), Firing: true})
	}
	unread.src.TotalAlerts = int64(len(unread.src.Alerts))
	items, err = unread.tester.Preview(t.Context(), delivery.TestInput{Destination: testDest, Source: unread.src})
	if err != nil {
		t.Fatal(err)
	}
	if in := preview(t, items); len(in.Buttons) != 0 || len([]rune(in.Body)) > MinBodyLength {
		t.Fatalf("without info: %d buttons, %d characters", len(in.Buttons), len([]rune(in.Body)))
	}
	unread.tester.Path = &path{err: &delivery.LimitedError{RetryAfter: time.Second}}
	unread.bridge.answer("info", answerJSON(http.StatusOK, "application/json", string(validInfo(t, nil))))
	items, err = unread.tester.Preview(t.Context(), delivery.TestInput{Destination: testDest, Source: e.src})
	if err != nil || len(preview(t, items).Buttons) != 0 {
		t.Fatalf("limited: %+v %v", items, err)
	}
	other := testDest
	other.ID = 9
	if _, err := e.tester.Preview(t.Context(), delivery.TestInput{Destination: other, Source: e.src}); err == nil {
		t.Fatal("a Destination without a Connection previews")
	}
}

// TestTestPublisher: a test message is never edited and has no Thread; a Tester without a budget or a clock takes
// delivery.interactive_budget and the real clock.
func TestTestPublisher(t *testing.T) {
	p := &testPublisher{}
	if o := p.Update(t.Context(), delivery.Call{}, "m1", messages.Message{}); o.Kind != delivery.OutcomeFatal {
		t.Errorf("update = %+v", o)
	}
	if o := p.Reply(t.Context(), delivery.Call{}, delivery.Root{}, messages.Message{}); o.Kind != delivery.OutcomeFatal {
		t.Errorf("reply = %+v", o)
	}
	if p.LengthLimit() != MinBodyLength {
		t.Errorf("length limit %d", p.LengthLimit())
	}
	if tr := (&Tester{}); tr.budget() != delivery.InteractiveBudget || tr.real() != (clock.Real{}) {
		t.Errorf("defaults %v %v", tr.budget(), tr.real())
	}
}

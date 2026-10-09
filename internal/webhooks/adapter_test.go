// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package webhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/templates"
)

func sandbox() *templates.Sandbox { return templates.New(clock.NewManual(t0), clock.Real{}) }

// endpoint is the receiving endpoint: it records each request and answers with the next scripted status, headers
// and body, then 200.
type endpoint struct {
	mu      sync.Mutex
	reqs    []*http.Request
	bodies  [][]byte
	answers []answer
}

type answer struct {
	status int
	header map[string]string
	body   string
}

func (e *endpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	e.mu.Lock()
	e.reqs, e.bodies = append(e.reqs, r), append(e.bodies, body)
	a := answer{status: http.StatusOK}
	if len(e.answers) > 0 {
		a, e.answers = e.answers[0], e.answers[1:]
	}
	e.mu.Unlock()
	for k, v := range a.header {
		w.Header().Set(k, v)
	}
	w.WriteHeader(a.status)
	_, _ = io.WriteString(w, a.body)
}

// adapterEnv is the Service with the outgoing webhook 1 pointed at a local endpoint, its first Signing secret and the
// Secret token, and its adapter under a policy that allows the loopback address.
type adapterEnv struct {
	s       *Service
	f       *fakeDB
	a       *Adapter
	ep      *endpoint
	url     string
	signing logging.Secret
	log     *bytes.Buffer
}

func newAdapterEnv(t *testing.T) *adapterEnv {
	t.Helper()
	s, f := newService(t)
	ep := &endpoint{}
	srv := httptest.NewServer(ep)
	t.Cleanup(srv.Close)
	secret, _, err := s.GenerateSigningSecret(t.Context(), requester, "DSAAAAAAAAAAA1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SetSecret(t.Context(), requester, "DSAAAAAAAAAAA1", nil, "token", "s3cr3t-token-value"); err != nil {
		t.Fatal(err)
	}
	f.dests[0].events = `{"url":"` + srv.URL + `/hook/auto?k={{ .Secrets.token }}","headers":[{"name":"Authorization",` +
		`"value":"Bearer {{ .Secrets.token }}"}]}`
	policy, err := outbound.ParsePolicy("standard", []string{"127.0.0.1/32"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	log := &bytes.Buffer{}
	a := &Adapter{Service: s, Sandbox: sandbox(), Network: Network{Policy: outbound.StaticPolicy(policy),
		Log: logging.New(log, logging.LevelInfo), Real: clock.NewManual(t0)}}
	return &adapterEnv{s: s, f: f, a: a, ep: ep, url: srv.URL, signing: secret, log: log}
}

func (e *adapterEnv) send(t *testing.T) delivery.Outcome {
	t.Helper()
	return e.a.SendEvent(t.Context(), delivery.EventCall{Class: outbound.ClassDelivery,
		Destination: delivery.Destination{ID: 1, PublicID: "DSAAAAAAAAAAA1", Type: TypeWebhook},
		WebhookID:   "msg_test", Body: []byte(`{"version":1,"event":"created"}`)})
}

// TestSendEvent is C-15.FR-2, FR-5 and AC-1, FR-10 and AC-8: one POST with the body as JSON, the headers rendered with
// the Destination's Secrets and webhook-id, webhook-timestamp from the real clock and webhook-signature, which verifies
// with the Signing secret; after a regeneration it carries both signatures.
func TestSendEvent(t *testing.T) {
	e := newAdapterEnv(t)
	if out := e.send(t); out.Kind != delivery.OutcomeOK || out.Status != http.StatusOK {
		t.Fatalf("outcome %+v", out)
	}
	r := e.ep.reqs[0]
	if r.Method != http.MethodPost || r.URL.Path != "/hook/auto" || r.URL.Query().Get("k") != "s3cr3t-token-value" ||
		r.Header.Get("Authorization") != "Bearer s3cr3t-token-value" ||
		r.Header.Get("Content-Type") != "application/json" || r.Header.Get(HeaderID) != "msg_test" ||
		r.Header.Get(HeaderTimestamp) != strconv.FormatInt(t0.Unix(), 10) ||
		string(e.ep.bodies[0]) != `{"version":1,"event":"created"}` {
		t.Fatalf("request %s %v %s", r.URL, r.Header, e.ep.bodies[0])
	}
	sig := r.Header.Get(HeaderSignature)
	if strings.Count(sig, "v1,") != 1 || !Verify(e.signing, "msg_test", r.Header.Get(HeaderTimestamp), e.ep.bodies[0],
		sig) {
		t.Fatalf("signature %q does not verify", sig)
	}
	next, _, err := e.s.GenerateSigningSecret(t.Context(), requester, "DSAAAAAAAAAAA1")
	if err != nil {
		t.Fatal(err)
	}
	e.send(t)
	r = e.ep.reqs[1]
	sig = r.Header.Get(HeaderSignature)
	if strings.Count(sig, " ") != 1 || !Verify(next, "msg_test", r.Header.Get(HeaderTimestamp), e.ep.bodies[1], sig) ||
		!Verify(e.signing, "msg_test", r.Header.Get(HeaderTimestamp), e.ep.bodies[1], sig) {
		t.Fatalf("signatures %q", sig)
	}
	if err := e.s.RetirePreviousSigningSecret(t.Context(), requester, "DSAAAAAAAAAAA1"); err != nil {
		t.Fatal(err)
	}
	e.send(t)
	if sig = e.ep.reqs[2].Header.Get(HeaderSignature); strings.Contains(sig, " ") {
		t.Fatalf("a retired secret still signs: %q", sig)
	}
	if strings.Contains(e.log.String(), "s3cr3t") {
		t.Error("the secret reached a log line")
	}
}

// TestResponseMapping is C-15.FR-6 and AC-5, AC-7: 2xx is delivered; 429 or 503 with Retry-After is a RetryAfter;
// 408, other 5xx are Transient; 401, 403, 404, 410 are Fatal; 400, 413, 422, a redirect, which is never followed and
// whose error names its target, and any other answer are unknown; no error carries a secret.
func TestResponseMapping(t *testing.T) {
	cases := []struct {
		a      answer
		kind   delivery.OutcomeKind
		errSub string
	}{
		{answer{status: 204}, delivery.OutcomeOK, ""},
		{answer{status: 200, body: strings.Repeat("x", maxAnswerBytes+1)}, delivery.OutcomeOK, ""},
		{answer{status: 503, header: map[string]string{"Retry-After": "2"}}, delivery.OutcomeRetryAfter, "answered 503"},
		{answer{status: 429, header: map[string]string{"Retry-After": "3"}}, delivery.OutcomeRetryAfter, "answered 429"},
		{answer{status: 429}, delivery.OutcomeUnknown, "answered 429"},
		{answer{status: 503}, delivery.OutcomeTransient, "answered 503"},
		{answer{status: 408}, delivery.OutcomeTransient, "answered 408"},
		{answer{status: 502, body: "bad gateway"}, delivery.OutcomeTransient, "answered 502: bad gateway"},
		{answer{status: 401}, delivery.OutcomeFatal, "answered 401"},
		{answer{status: 403}, delivery.OutcomeFatal, "answered 403"},
		{answer{status: 404}, delivery.OutcomeFatal, "answered 404"},
		{answer{status: 410}, delivery.OutcomeFatal, "answered 410"},
		{answer{status: 400, body: "token s3cr3t-token-value is wrong"}, delivery.OutcomeUnknown,
			"answered 400: token [redacted] is wrong"},
		{answer{status: 413}, delivery.OutcomeUnknown, "answered 413"},
		{answer{status: 422}, delivery.OutcomeUnknown, "answered 422"},
		{answer{status: 302, header: map[string]string{"Location": "http://elsewhere.example.org/x"}},
			delivery.OutcomeUnknown, "redirect to http://elsewhere.example.org/x refused"},
		{answer{status: 418}, delivery.OutcomeUnknown, "answered 418"},
	}
	e := newAdapterEnv(t)
	for _, c := range cases {
		e.ep.answers = []answer{c.a}
		out := e.send(t)
		if out.Kind != c.kind || out.Status != c.a.status || !strings.Contains(string(out.Error), c.errSub) ||
			strings.Contains(string(out.Error), "s3cr3t") {
			t.Errorf("%d: %+v", c.a.status, out)
		}
		if c.kind == delivery.OutcomeRetryAfter && (out.RetryAfter < 2*time.Second || out.Scope != delivery.ScopeDestination) {
			t.Errorf("retry after %+v", out)
		}
	}
	if len(e.ep.reqs) != len(cases) {
		t.Errorf("the redirect was followed: %d requests", len(e.ep.reqs))
	}
}

// TestAddressPolicy is C-15.FR-8 and AC-4: a URL that resolves to the metadata address is refused under the standard
// and the strict policy, with an error naming the link-local rule, and nothing is sent; a refused connection is
// Transient.
func TestAddressPolicy(t *testing.T) {
	for _, mode := range []string{"standard", "strict"} {
		e := newAdapterEnv(t)
		policy, _ := outbound.ParsePolicy(mode, nil, nil)
		e.a.Network.Policy = outbound.StaticPolicy(policy)
		e.f.dests[0].events = `{"url":"http://169.254.169.254/latest","headers":[]}`
		out := e.send(t)
		if out.Kind != delivery.OutcomeFatal || string(out.Error) !=
			"blocked by the outbound address policy: 169.254.169.254 is link-local (always blocked)" ||
			len(e.ep.reqs) != 0 {
			t.Errorf("%s: %+v", mode, out)
		}
	}
	e := newAdapterEnv(t)
	e.f.dests[0].events = `{"url":"http://127.0.0.1:1/x?k={{ .Secrets.token }}","headers":[]}`
	if out := e.send(t); out.Kind != delivery.OutcomeTransient || strings.Contains(string(out.Error), "s3cr3t") {
		t.Errorf("refused connection %+v", out)
	}
}

// TestSendEventTemplates is C-15.FR-7 and FR-10: a template that fails, reads a Secret the Destination does not have,
// or renders no http URL sends nothing and is a template error whose text carries no secret.
func TestSendEventTemplates(t *testing.T) {
	for name, c := range map[string]struct{ url, header string }{
		"missing secret": {"/{{ .Secrets.missing }}", "x"},
		"syntax":         {"/{{ ", "x"},
		"runtime":        {"/{{ printf \"%d\" .Secrets.token }}{{ len 3 }}", "x"},
		"header":         {"/ok", "{{ .Secrets.other }}"},
		"header runtime": {"/ok", "{{ len 3 }}"},
		"header syntax":  {"/ok", "{{ end }}"},
		"line break":     {"/ok", "{{ .Secrets.token }}{{ printf \"%c\" 10 }}x"},
		"index missing":  {"/{{ index .Secrets \"missing\" }}", "x"},
		"not http":       {"", ""},
	} {
		e := newAdapterEnv(t)
		url := e.url + c.url
		if name == "not http" {
			url = "ftp://example.org/{{ .Secrets.token }}"
		}
		e.f.dests[0].events = `{"url":"` + strings.ReplaceAll(url, `"`, `\"`) + `","headers":[{"name":"X","value":"` +
			strings.ReplaceAll(c.header, `"`, `\"`) + `"}]}`
		out := e.send(t)
		if out.Kind != delivery.OutcomeTemplateError || len(e.ep.reqs) != 0 ||
			strings.Contains(string(out.Error), "s3cr3t") || !strings.HasPrefix(string(out.Error),
			"the request template failed: ") {
			t.Errorf("%s: %+v", name, out)
		}
	}
}

// TestSendEventTargets covers a Destination that is gone or of another type (Fatal), one in the template mode only
// (unknown), settings that cannot be read (Transient), secrets that do not open, a proxy that cannot be built, and the
// client kept while its settings stay and built again when they change.
func TestSendEventTargets(t *testing.T) {
	e := newAdapterEnv(t)
	call := func(id int64) delivery.Outcome {
		return e.a.SendEvent(t.Context(), delivery.EventCall{Class: outbound.ClassDelivery,
			Destination: delivery.Destination{ID: id}, WebhookID: "msg_1", Body: []byte("{}")})
	}
	if out := call(2); out.Kind != delivery.OutcomeFatal {
		t.Errorf("not a webhook %+v", out)
	}
	if out := call(9); out.Kind != delivery.OutcomeFatal {
		t.Errorf("gone %+v", out)
	}
	e.f.dests[0].mode = ModeTemplate
	if out := call(1); out.Kind != delivery.OutcomeUnknown {
		t.Errorf("template mode %+v", out)
	}
	e.f.dests[0].mode = ModeEvents
	for _, name := range []string{"GetTarget", "ListSecretValues"} {
		e.f.fail[name] = errBoom
		if out := call(1); out.Kind != delivery.OutcomeTransient {
			t.Errorf("%s %+v", name, out)
		}
		delete(e.f.fail, name)
	}
	d := e.f.dests[0]
	for name, set := range map[string]func(){
		"events":   func() { d.events = "[" },
		"proxy":    func() { d.proxy = "[" },
		"password": func() { d.password = keyring.StoredSecret{Ciphertext: []byte("x"), KeyID: "k-x"} },
		"signing":  func() { d.signing.KeyID = "k-x" },
		"secret": func() {
			d.secrets["token"] = keyring.StoredSecret{Ciphertext: []byte("x"), KeyID: "k-x", UpdatedAt: &t0}
		},
	} {
		saved := *d
		saved.secrets = map[string]keyring.StoredSecret{"token": d.secrets["token"]}
		set()
		if out := call(1); out.Kind != delivery.OutcomeTransient {
			t.Errorf("%s %+v", name, out)
		}
		*d = saved
	}
	if out := call(1); out.Kind != delivery.OutcomeOK {
		t.Fatalf("restored %+v", out)
	}
	first := e.a.clients[clientKey{destination: 1, class: outbound.ClassDelivery}].client
	call(1)
	if e.a.clients[clientKey{destination: 1, class: outbound.ClassDelivery}].client != first {
		t.Error("the client was built again with the same settings")
	}
	if _, _, err := e.s.SetSecret(t.Context(), requester, "DSAAAAAAAAAAA1", nil, "token", "rotated"); err != nil {
		t.Fatal(err)
	}
	call(1)
	if e.a.clients[clientKey{destination: 1, class: outbound.ClassDelivery}].client == first {
		t.Error("the client kept a changed secret")
	}
	d.proxy = `{"enabled":true,"type":"gopher","address":"p:1"}`
	if out := call(1); out.Kind != delivery.OutcomeFatal {
		t.Errorf("bad proxy %+v", out)
	}
	d.proxy = `{"enabled":false}`
	d.signing, d.previous = keyring.StoredSecret{}, keyring.StoredSecret{}
	if out := call(1); out.Kind != delivery.OutcomeFatal {
		t.Errorf("no signing secret %+v", out)
	}
}

// TestMapping is the table of C-15.FR-6.
func TestMapping(t *testing.T) {
	for status, want := range map[int]outbound.Outcome{200: outbound.OutcomeOK, 299: outbound.OutcomeOK,
		408: outbound.OutcomeTransient, 500: outbound.OutcomeTransient, 599: outbound.OutcomeTransient,
		401: outbound.OutcomeFatal, 403: outbound.OutcomeFatal, 404: outbound.OutcomeFatal, 410: outbound.OutcomeFatal,
		400: outbound.OutcomeUnknown, 413: outbound.OutcomeUnknown, 422: outbound.OutcomeUnknown,
		429: outbound.OutcomeUnknown, 100: outbound.OutcomeUnknown} {
		if got := Mapping(status, 0, false); got != want {
			t.Errorf("%d = %s", status, got)
		}
	}
	if Mapping(503, time.Second, true) != outbound.OutcomeRetryAfter || Mapping(429, 0, true) != outbound.OutcomeRetryAfter {
		t.Error("retry after")
	}
	if out := outcomeOf(outbound.Result{}, errBoom, nil); out.Kind != delivery.OutcomeTransient {
		t.Errorf("other error %+v", out)
	}
}

// templateCall is a call of the template mode to the outgoing webhook 1 for the Alert Group #7 Disk in the status,
// with the values extracted so far.
func templateCall(status string, response map[string]string, loud bool) delivery.Call {
	st, _ := json.Marshal(delivery.RequestState{Language: "en", Group: &templates.Data{Status: status,
		AlertGroup: templates.AlertGroup{Number: 7, Title: "Disk", Status: status}}})
	c := delivery.Call{Class: outbound.ClassDelivery, Loudness: groups.Quiet,
		Destination: delivery.Destination{ID: 1, PublicID: "DSAAAAAAAAAAA1", Type: TypeWebhook},
		Webhook:     &delivery.WebhookCall{State: st, Response: response}}
	if loud {
		c.Loudness = groups.Loud
		c.Targets = []mentions.Target{{Kind: mentions.TargetUser, User: &mentions.User{PublicID: "SRA", Name: "Alice",
			Login: "alice"}}}
	}
	return c
}

// templateMode puts the outgoing webhook 1 in the mode both with the request templates of a chat with two-step
// threads at the endpoint.
func (e *adapterEnv) templateMode(c TemplateConfig) {
	_ = ValidateTemplate(sandbox(), "/template", &c, t0) // normalizes, as a save does
	e.f.dests[0].mode, e.f.dests[0].template = ModeBoth, string(c.JSON())
}

// TestTemplateRequests is C-15.FR-3, AC-3 and FR-11 against a local endpoint: "create" posts the body rendered with the
// Alert Group, the Secret in its header and the Mention as plain text, signed, and extracts $.data.id as the message
// id; "update" uses it in its URL; a reply runs "open thread" once, adds its value and posts the event and its
// loudness; once the thread is open, only the reply goes; the final edit reads .Final.
func TestTemplateRequests(t *testing.T) {
	e := newAdapterEnv(t)
	e.templateMode(chatConfig(e.url))
	e.ep.answers = []answer{{status: 200, body: `{"data":{"id":"m1"}}`}}
	out := e.a.Publish(t.Context(), templateCall("firing", nil, true), delivery.Message{})
	if out.Kind != delivery.OutcomeOK || out.MessageID != "m1" || out.Values["id"] != "m1" || len(out.Missing) != 0 ||
		!out.Rendered || out.Request != RequestCreate {
		t.Fatalf("create %+v", out)
	}
	r := e.ep.reqs[0]
	if r.Method != http.MethodPost || r.URL.Path != "/chat/ops/messages" ||
		r.Header.Get("Authorization") != "Bearer s3cr3t-token-value" ||
		r.Header.Get("Content-Type") != "application/json" || !strings.HasPrefix(r.Header.Get(HeaderID), "msg_") ||
		string(e.ep.bodies[0]) != `{"text":"#7 Disk","who":"@alice"}` ||
		!Verify(e.signing, r.Header.Get(HeaderID), r.Header.Get(HeaderTimestamp), e.ep.bodies[0],
			r.Header.Get(HeaderSignature)) {
		t.Fatalf("create request %s %v %s", r.URL, r.Header, e.ep.bodies[0])
	}
	up := templateCall("acknowledged", map[string]string{"id": "m1"}, false)
	if out := e.a.Update(t.Context(), up, "m1", delivery.Message{}); out.Kind != delivery.OutcomeOK ||
		e.ep.reqs[1].Method != http.MethodPut || e.ep.reqs[1].URL.Path != "/chat/ops/messages/m1" ||
		string(e.ep.bodies[1]) != `{"text":"acknowledged","final":""}` {
		t.Fatalf("update %+v %s %s", out, e.ep.reqs[1].URL, e.ep.bodies[1])
	}
	up.Webhook.Final = "No longer updated here"
	e.a.Update(t.Context(), up, "m1", delivery.Message{})
	if string(e.ep.bodies[2]) != `{"text":"acknowledged","final":"No longer updated here"}` {
		t.Errorf("final edit %s", e.ep.bodies[2])
	}
	reply := templateCall("firing", map[string]string{"id": "m1"}, true)
	reply.Webhook.Event = "alerts_added"
	e.ep.answers = []answer{{status: 200, body: `{"thread":{"id":"t1"}}`}}
	out = e.a.Reply(t.Context(), reply, delivery.Root{}, delivery.Message{})
	if out.Kind != delivery.OutcomeOK || !out.ThreadOpened || out.Values["thread"] != "t1" ||
		e.ep.reqs[3].URL.Path != "/chat/ops/threads" || string(e.ep.bodies[3]) != `{"root":"m1"}` ||
		e.ep.reqs[4].URL.Path != "/chat/ops/threads/t1/messages" ||
		string(e.ep.bodies[4]) != `{"text":"alerts_added true"}` {
		t.Fatalf("first reply %+v", out)
	}
	reply.Webhook.Response["thread"], reply.Webhook.ThreadOpened = "t1", true
	if out := e.a.Reply(t.Context(), reply, delivery.Root{}, delivery.Message{}); out.Kind != delivery.OutcomeOK ||
		out.ThreadOpened || len(e.ep.reqs) != 6 || e.ep.reqs[5].URL.Path != "/chat/ops/threads/t1/messages" {
		t.Fatalf("second reply %+v, %d requests", out, len(e.ep.reqs))
	}
	if strings.Contains(e.log.String(), "s3cr3t") {
		t.Error("the secret reached a log line")
	}
}

// TestTemplateFailures is C-15.FR-4 and FR-7: a rule that finds nothing leaves "create" delivered with the rule
// missing and no message id; a later request that reads the value fails as a template error before anything is sent,
// with no Secret in its error; "open thread" that fails sends no reply; a Destination without "reply in thread" sends
// nothing; one without request templates, gone or unreadable cannot be called.
func TestTemplateFailures(t *testing.T) {
	e := newAdapterEnv(t)
	c := chatConfig(e.url)
	c.Create.Extract[0].Path = "$.nothing.here"
	e.templateMode(c)
	out := e.a.Publish(t.Context(), templateCall("firing", nil, false), delivery.Message{})
	if out.Kind != delivery.OutcomeOK || out.MessageID != "" || !slices.Equal(out.Missing, []string{"id"}) {
		t.Fatalf("create without the value %+v", out)
	}
	out = e.a.Update(t.Context(), templateCall("acknowledged", map[string]string{}, false), "", delivery.Message{})
	if out.Kind != delivery.OutcomeTemplateError || out.Rendered || out.Request != RequestUpdate ||
		!strings.Contains(string(out.Error), "the value id is missing") || len(e.ep.reqs) != 1 {
		t.Fatalf("update without the value %+v", out)
	}
	c = chatConfig(e.url)
	c.Create.Headers[0].Value = "{{ .Secrets.token }} {{ .Secrets.gone }}"
	e.templateMode(c)
	out = e.a.Publish(t.Context(), templateCall("firing", nil, false), delivery.Message{})
	if out.Kind != delivery.OutcomeTemplateError || strings.Contains(string(out.Error), "s3cr3t") ||
		!strings.Contains(string(out.Error), "the Secret gone is not set") {
		t.Errorf("missing secret %+v", out)
	}
	e.templateMode(chatConfig(e.url))
	e.ep.answers = []answer{{status: 404}}
	reply := templateCall("firing", map[string]string{"id": "m1"}, false)
	if out := e.a.Reply(t.Context(), reply, delivery.Root{}, delivery.Message{}); out.Kind != delivery.OutcomeFatal ||
		out.ThreadOpened || out.Request != RequestOpenThread || len(e.ep.reqs) != 2 {
		t.Errorf("open thread failed %+v", out)
	}
	c = chatConfig(e.url)
	c.ReplyInThread = nil
	e.templateMode(c)
	if out := e.a.Reply(t.Context(), reply, delivery.Root{}, delivery.Message{}); out.Kind != delivery.OutcomeOK ||
		len(e.ep.reqs) != 2 {
		t.Errorf("no reply template %+v", out)
	}
	e.f.dests[0].mode = ModeEvents
	if out := e.a.Publish(t.Context(), reply, delivery.Message{}); out.Kind != delivery.OutcomeUnknown {
		t.Errorf("events mode %+v", out)
	}
	e.f.dests[0].mode, e.f.dests[0].template = ModeTemplate, "["
	if out := e.a.Publish(t.Context(), reply, delivery.Message{}); out.Kind != delivery.OutcomeTransient {
		t.Errorf("broken templates %+v", out)
	}
	reply.Destination.ID = 9
	if out := e.a.Update(t.Context(), reply, "", delivery.Message{}); out.Kind != delivery.OutcomeFatal {
		t.Errorf("gone %+v", out)
	}
	if e.a.LengthLimit() != templates.OutputCap {
		t.Error("length limit")
	}
	e.templateMode(chatConfig(e.url))
	reply.Destination.ID = 1
	e.f.dests[0].signing, e.f.dests[0].previous = keyring.StoredSecret{}, keyring.StoredSecret{}
	if out := e.a.Publish(t.Context(), reply, delivery.Message{}); out.Kind != delivery.OutcomeFatal {
		t.Errorf("no signing secret %+v", out)
	}
}

// TestTemplateStorm is C-15.FR-3 and C-11.FR-6: a Storm summary renders .Storm and no Alert Group; a template that
// reads the Alert Group of a summary is a template error.
func TestTemplateStorm(t *testing.T) {
	e := newAdapterEnv(t)
	c := chatConfig(e.url)
	c.Create.Body = str(`{"text":{{ if .Storm }}{{ printf "Storm on %s: %d (%d Urgent) %s %v" .Storm.Route ` +
		`.Storm.AlertGroupCount .Storm.UrgentCount .Storm.URL .Storm.Final | toJson }}{{ else }}"#{{ .AlertGroup.Number }}"` +
		`{{ end }}}`)
	e.templateMode(c)
	st, _ := json.Marshal(delivery.RequestState{Storm: &delivery.StormState{Route: "db", AlertGroupCount: 30,
		UrgentCount: 2, URL: "https://muster.example.org/alert-groups?route=RT1"}})
	call := templateCall("firing", nil, true)
	call.Webhook.State = st
	if out := e.a.Publish(t.Context(), call, delivery.Message{}); out.Kind != delivery.OutcomeOK ||
		string(e.ep.bodies[0]) != `{"text":"Storm on db: 30 (2 Urgent) https://muster.example.org/alert-groups?route=RT1 false"}` {
		t.Fatalf("summary %+v %s", out, e.ep.bodies)
	}
	if out := e.a.Update(t.Context(), call, "m1", delivery.Message{}); out.Kind != delivery.OutcomeTemplateError {
		t.Errorf("an update that reads the Alert Group of a summary %+v", out)
	}
}

// configDB serves GetTemplateConfig with raw, or fails with err.
type configDB struct {
	raw []byte
	err error
}

func (configDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (configDB) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, errBoom }

func (d configDB) QueryRow(context.Context, string, ...any) pgx.Row { return configRow(d) }

type configRow configDB

func (r configRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*[]byte)) = r.raw
	return nil
}

// TestDesired is the Desired state of the template mode (C-15.FR-3, ADR-0005): the hash of the rendered "update"
// request, the same whatever the Secrets and the values extracted, another for a change it shows and none for a change
// it does not; one that fails hashes the state; without request templates, the state.
func TestDesired(t *testing.T) {
	e := newAdapterEnv(t)
	c := chatConfig(e.url)
	raw := c.JSON()
	state := func(status, summary string) delivery.RequestState {
		return delivery.RequestState{Group: &templates.Data{Status: status, CommonAnnotations: templates.KV{"s": summary},
			AlertGroup: templates.AlertGroup{Number: 7, Status: status}}}
	}
	hash := func(db configDB, st delivery.RequestState) []byte {
		t.Helper()
		h, err := e.a.Desired(t.Context(), db, 1, st)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	firing := hash(configDB{raw: raw}, state("firing", "a"))
	if !bytes.Equal(firing, hash(configDB{raw: raw}, state("firing", "b"))) {
		t.Error("a change the update does not show changed the hash")
	}
	if bytes.Equal(firing, hash(configDB{raw: raw}, state("acknowledged", "a"))) {
		t.Error("a change the update shows kept the hash")
	}
	c.Update.Body = str("{{ len 3 }}")
	broken := hash(configDB{raw: c.JSON()}, state("firing", "a"))
	if bytes.Equal(broken, hash(configDB{raw: c.JSON()}, state("firing", "b"))) || bytes.Equal(broken, firing) {
		t.Error("a failing update hashes its state")
	}
	if bytes.Equal(hash(configDB{}, state("firing", "a")), hash(configDB{}, state("firing", "b"))) {
		t.Error("without templates the state is hashed")
	}
	if _, err := e.a.Desired(t.Context(), configDB{err: errBoom}, 1, state("firing", "a")); err == nil {
		t.Error("a failed read was ignored")
	}
	if _, err := e.a.Desired(t.Context(), configDB{raw: []byte("[")}, 1, state("firing", "a")); err == nil {
		t.Error("broken templates were ignored")
	}
	if h, err := e.a.Desired(t.Context(), configDB{err: pgx.ErrNoRows}, 1, state("firing", "a")); err != nil ||
		len(h) == 0 {
		t.Errorf("a destination without a row %v", err)
	}
}

// TestPreviewRequest is C-12.FR-4: a request template renders with the sample, its Secrets as [redacted], the values
// it reads as example-<name> and the Mentions of the example; a template that fails, or reads .Secrets other than by
// name, is an error with its position.
func TestPreviewRequest(t *testing.T) {
	e := newAdapterEnv(t)
	sample := templates.Data{AlertGroup: templates.AlertGroup{Number: 4, StartedAt: t0}}
	out, err := e.a.PreviewRequest(`Bearer {{ .Secrets.token }} for #{{ .AlertGroup.Number }} {{ .Response.id }} `+
		`{{ range .Mentions }}{{ mention . }} {{ end }}{{ .Event }}`, sample)
	if err != nil || out != "Bearer [redacted] for #4 example-id @all oncall @alice alerts_added" {
		t.Errorf("preview %q %v", out, err)
	}
	for src, code := range map[string]string{"{{ .Secrets }}": templates.CodeSyntax, "{{ nope }}": templates.CodeUnknownFunction,
		"{{ len 3 }}": templates.CodeSyntax} {
		_, err := e.a.PreviewRequest(src, sample)
		var te *templates.Error
		if !errors.As(err, &te) || te.Code != code || te.Line != 1 {
			t.Errorf("%s: %+v", src, err)
		}
	}
	if _, err := e.a.PreviewRequest("{{ .Response.x", sample); err == nil {
		t.Error("a broken template rendered")
	}
}

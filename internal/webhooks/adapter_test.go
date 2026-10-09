// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package webhooks

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
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

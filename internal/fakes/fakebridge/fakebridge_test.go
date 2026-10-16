// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package fakebridge_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	"github.com/muster-io/muster/internal/bridge"
	"github.com/muster-io/muster/internal/bridge/gen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/outbound"
)

// allFeatures are the features of the default info, as bridge.Info names them.
var allFeatures = []string{"buttons", "delete", "deletion_events", "direct_messages", "native_mentions",
	"private_answers", "quiet_messages", "status_color", "thread_messages"}

// spec checks the requests to the fake and its answers against api/bridge.yaml.
type spec struct {
	fake   *fakebridge.Fake
	router routers.Router
}

func start(t *testing.T) *spec {
	t.Helper()
	f := fakebridge.New()
	if err := f.Start(t.Context(), "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close(t.Context()) })
	doc, err := openapi3.NewLoader().LoadFromFile("../../../api/bridge.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc.Servers = openapi3.Servers{{URL: f.URL()}}
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	return &spec{fake: f, router: router}
}

// answer is an answer of the fake, and what the validator found wrong with it, nil when it matches the protocol.
type answer struct {
	status  int
	header  http.Header
	body    []byte
	invalid error
}

// getInfo sends getInfo with the token, none when it is empty, after checking that the request matches
// api/bridge.yaml, and checks the answer: its status must be declared and its body must match the schema. The
// operations that later stories add to the fake are checked the same way.
func (s *spec) getInfo(t *testing.T, token string) answer {
	t.Helper()
	return s.call(t, http.MethodGet, "/v1/info", token)
}

// checkChannel sends checkChannel for the channel id, which is one path segment, checked as getInfo is.
func (s *spec) checkChannel(t *testing.T, token, id string) answer {
	t.Helper()
	return s.call(t, http.MethodPost, "/v1/channels/"+bridge.Segment(id)+"/check", token)
}

// call sends the request of an operation without a body, checked as getInfo is.
func (s *spec) call(t *testing.T, method, path, token string) answer {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, s.fake.URL()+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	route, params, err := s.router.FindRoute(req)
	if err != nil {
		t.Fatalf("%s %s is no operation of api/bridge.yaml: %v", method, path, err)
	}
	in := &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route,
		Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc}}
	if err := openapi3filter.ValidateRequest(t.Context(), in); err != nil {
		t.Fatalf("the request %s %s does not match api/bridge.yaml: %v", method, path, err)
	}
	a := send(t, req)
	a.invalid = openapi3filter.ValidateResponse(t.Context(), &openapi3filter.ResponseValidationInput{
		RequestValidationInput: in, Status: a.status, Header: a.header, Body: io.NopCloser(bytes.NewReader(a.body)),
		Options: &openapi3filter.Options{IncludeResponseStatus: true, MultiError: true}})
	return a
}

func send(t *testing.T, req *http.Request) answer {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return answer{status: resp.StatusCode, header: resp.Header, body: body}
}

// valid is the info of an answer that matches the protocol, as Muster parses it.
func (a answer) valid(t *testing.T) bridge.Info {
	t.Helper()
	if a.invalid != nil {
		t.Fatalf("the answer %d does not match api/bridge.yaml: %v\n%s", a.status, a.invalid, a.body)
	}
	if a.status != http.StatusOK {
		t.Fatalf("status %d: %s", a.status, a.body)
	}
	i, err := bridge.ParseInfo(a.body)
	if err != nil {
		t.Fatalf("Muster refuses the info: %v", err)
	}
	return i
}

func (a answer) problem(t *testing.T, status int, class gen.ErrorClass) {
	t.Helper()
	var p gen.Problem
	if err := json.Unmarshal(a.body, &p); err != nil || a.status != status || p.Class == nil || *p.Class != class ||
		p.Status == nil || *p.Status != status || a.header.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("answer %d %s %q, want a problem %d of the class %s", a.status, a.header.Get("Content-Type"), a.body,
			status, class)
	}
}

// control calls a control endpoint and returns its status and body.
func (s *spec) control(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, s.fake.URL()+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	a := send(t, req)
	return a.status, string(a.body)
}

func (s *spec) configure(t *testing.T, patch string) {
	t.Helper()
	if status, body := s.control(t, http.MethodPut, "/_fake/config", patch); status != http.StatusOK {
		t.Fatalf("PUT /_fake/config %s = %d %s", patch, status, body)
	}
}

// TestInfo is C-01.FR-13: both bots get the default info, which matches api/bridge.yaml and Muster accepts: the
// messenger "Fake chat", protocol version 1, every feature and both event delivery modes.
func TestInfo(t *testing.T) {
	s := start(t)
	for _, token := range []string{fakebridge.Token, fakebridge.SecondToken} {
		i := s.getInfo(t, token).valid(t)
		if i.Messenger.Name != "Fake chat" || i.ProtocolVersion != "1" || i.Bridge.Name != fakebridge.BridgeName ||
			i.Bridge.Version != fakebridge.BridgeVersion || i.UserLookup != gen.Login ||
			!slices.Equal(i.EventDelivery, []gen.InfoEventDelivery{gen.Poll, gen.Push}) ||
			!slices.Equal(i.Features(), allFeatures) {
			t.Errorf("info of %s: %s", token, i.Raw)
		}
		if i.Limits.MaxBodyLength != 4000 || !i.Limits.EditMaxAgeSeconds.IsNull() ||
			i.Capabilities.Buttons.MaxPerMessage != 10 || len(i.Capabilities.Buttons.Styles) != 4 {
			t.Errorf("limits and buttons of %s: %s", token, i.Raw)
		}
		if strings.Contains(string(i.Raw), "padding") || strings.Contains(string(i.Raw), "link_url_template") {
			t.Errorf("info of %s has a padding or a link: %s", token, i.Raw)
		}
	}
	if fakebridge.Token != "muster-dev-bridge-token" || fakebridge.SecondToken != "muster-dev-bridge-token-b" {
		t.Errorf("tokens %q, %q", fakebridge.Token, fakebridge.SecondToken)
	}
}

// TestUnauthorized: a missing, unknown, malformed or revoked token gets 401 as api/bridge.yaml specifies, on every
// path; revoking one bot leaves the other, and an empty list restores it.
func TestUnauthorized(t *testing.T) {
	s := start(t)
	for _, token := range []string{"", "nope", fakebridge.Token + "x"} {
		a := s.getInfo(t, token)
		if a.invalid != nil {
			t.Errorf("401 for %q does not match api/bridge.yaml: %v", token, a.invalid)
		}
		a.problem(t, http.StatusUnauthorized, gen.ErrorClassForbidden)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.fake.URL()+"/v1/info", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Basic "+fakebridge.Token)
	send(t, req).problem(t, http.StatusUnauthorized, gen.ErrorClassForbidden)
	req.Header.Set("Authorization", "bearer "+fakebridge.Token)
	if a := send(t, req); a.status != http.StatusOK {
		t.Errorf("a lower-case scheme = %d", a.status)
	}

	s.configure(t, `{"revoked":["`+fakebridge.SecondToken+`"]}`)
	a := s.getInfo(t, fakebridge.SecondToken)
	if a.invalid != nil {
		t.Errorf("401 of a revoked token does not match api/bridge.yaml: %v", a.invalid)
	}
	a.problem(t, http.StatusUnauthorized, gen.ErrorClassForbidden)
	s.getInfo(t, fakebridge.Token).valid(t)
	s.configure(t, `{"revoked":[]}`)
	s.getInfo(t, fakebridge.SecondToken).valid(t)
}

// TestBrokenOnPurpose (C-22.AC-1): the fake serves a newer protocol version when told to, which api/bridge.yaml and
// Muster refuse, and a padding that makes info larger than 64 KiB, which the protocol allows and Muster refuses.
func TestBrokenOnPurpose(t *testing.T) {
	s := start(t)
	s.configure(t, `{"protocol_version":"2"}`)
	a := s.getInfo(t, fakebridge.Token)
	if a.status != http.StatusOK || a.invalid == nil {
		t.Errorf("protocol version 2 = %d, validator %v; want an answer the validator refuses", a.status, a.invalid)
	}
	var ie *bridge.InfoError
	if _, err := bridge.ParseInfo(a.body); !errors.As(err, &ie) || ie.Code != bridge.CodeProtocolVersionUnknown {
		t.Errorf("Muster parses version 2 as %v", err)
	}

	s.configure(t, `{"protocol_version":"1","info_padding_bytes":70000}`)
	a = s.getInfo(t, fakebridge.Token)
	if a.invalid != nil || a.status != http.StatusOK || len(a.body) <= bridge.InfoMaxBytes {
		t.Fatalf("padded info = %d, %d bytes, validator %v", a.status, len(a.body), a.invalid)
	}
	if _, err := bridge.ParseInfo(a.body); !errors.As(err, &ie) || ie.Code != bridge.CodeProtocolMismatch ||
		!strings.HasSuffix(ie.Message, "info is larger than 64 KiB") {
		t.Errorf("Muster parses the padded info as %v", err)
	}
	s.configure(t, `{"info_padding_bytes":0}`)
	s.getInfo(t, fakebridge.Token).valid(t)
}

// TestChannels is checkChannel of the fake (C-22.FR-3): for both bots, the default channels ch-alerts and
// ch-alerts-prod answer their names, and ch-nobot and unknown ids forbidden with "The bot is not a member of this
// channel.", as api/bridge.yaml specifies and Muster's check reads them; a revoked token gets 401; nothing is posted.
func TestChannels(t *testing.T) {
	s := start(t)
	for _, token := range []string{fakebridge.Token, fakebridge.SecondToken} {
		for id, name := range map[string]string{fakebridge.ChannelAlerts: "alerts",
			fakebridge.ChannelAlertsProd: "alerts-prod"} {
			a := s.checkChannel(t, token, id)
			var c gen.ChannelCheck
			if err := json.Unmarshal(a.body, &c); err != nil || a.invalid != nil || a.status != http.StatusOK ||
				c.ChannelId != id || c.Name != name {
				t.Errorf("%s by %s = %d %s %v", id, token, a.status, a.body, a.invalid)
			}
		}
		for _, id := range []string{fakebridge.ChannelNoBot, "ch-unknown", "a/b .."} {
			a := s.checkChannel(t, token, id)
			if a.invalid != nil {
				t.Errorf("%s does not match api/bridge.yaml: %v", id, a.invalid)
			}
			a.problem(t, http.StatusForbidden, gen.ErrorClassForbidden)
			var p gen.Problem
			if err := json.Unmarshal(a.body, &p); err != nil || p.Detail == nil || *p.Detail != fakebridge.MessageNotMember {
				t.Errorf("%s = %s", id, a.body)
			}
		}
	}
	reqs := s.fake.Requests()
	if len(reqs) != 10 || slices.ContainsFunc(reqs, func(r fakeserver.Request) bool {
		return r.Method != http.MethodPost || !strings.HasPrefix(r.Path, "/v1/channels/")
	}) {
		t.Errorf("requests %+v", reqs)
	}

	n := network(t)
	c, err := bridge.NewClient(n, bridge.Settings{BaseURL: s.fake.URL(), Token: fakebridge.Token})
	if err != nil {
		t.Fatal(err)
	}
	direct := bridge.Direct(delivery.Call{Class: outbound.ClassInteractive})
	res, err := bridge.CheckDestination(t.Context(), c, direct, fakebridge.ChannelNoBot)
	if err != nil || res.Code != bridge.CodeChannelForbidden || res.Message != fakebridge.MessageNotMember {
		t.Errorf("Muster's check of ch-nobot = %+v, %v", res, err)
	}
	if res, err := bridge.CheckDestination(t.Context(), c, direct, fakebridge.ChannelAlerts); err != nil || !res.OK() ||
		res.Name != "alerts" {
		t.Errorf("Muster's check of ch-alerts = %+v, %v", res, err)
	}
	s.configure(t, `{"revoked":["`+fakebridge.Token+`"]}`)
	a := s.checkChannel(t, fakebridge.Token, fakebridge.ChannelAlerts)
	if a.invalid != nil {
		t.Errorf("401 does not match api/bridge.yaml: %v", a.invalid)
	}
	a.problem(t, http.StatusUnauthorized, gen.ErrorClassForbidden)
	if res, err := bridge.CheckDestination(t.Context(), c, direct, fakebridge.ChannelAlerts); err != nil ||
		res.Code != bridge.CodeTokenInvalid {
		t.Errorf("Muster's check with a revoked token = %+v, %v", res, err)
	}
}

// TestChannelsConfig: a PUT of channels adds or replaces each channel it names — a name adds or renames one, null
// forbids it — and keeps the others; GET shows them; SetConfig without channels leaves none.
func TestChannelsConfig(t *testing.T) {
	s := start(t)
	s.configure(t, `{"channels":{"ch-alerts":null,"ch-new":"new","ch-alerts-prod":"prod"}}`)
	s.checkChannel(t, fakebridge.Token, fakebridge.ChannelAlerts).problem(t, http.StatusForbidden,
		gen.ErrorClassForbidden)
	for id, name := range map[string]string{"ch-new": "new", fakebridge.ChannelAlertsProd: "prod"} {
		var c gen.ChannelCheck
		if a := s.checkChannel(t, fakebridge.Token, id); json.Unmarshal(a.body, &c) != nil || c.Name != name {
			t.Errorf("%s = %d %s", id, a.status, a.body)
		}
	}
	s.configure(t, `{"channels":{"ch-alerts":"alerts"}}`)
	if a := s.checkChannel(t, fakebridge.Token, fakebridge.ChannelAlerts); a.status != http.StatusOK {
		t.Errorf("ch-alerts again = %d %s", a.status, a.body)
	}
	s.configure(t, `{"channels":null}`)
	status, body := s.control(t, http.MethodGet, "/_fake/config", "")
	var c fakebridge.Config
	if err := json.Unmarshal([]byte(body), &c); err != nil || status != http.StatusOK || len(c.Channels) != 4 ||
		*c.Channels["ch-new"] != "new" || c.Channels[fakebridge.ChannelNoBot] != nil {
		t.Errorf("GET /_fake/config = %d %s", status, body)
	}
	for _, bad := range []string{`{"channels":{"x":1}}`, `{"channels":["x"]}`} {
		if status, body := s.control(t, http.MethodPut, "/_fake/config", bad); status != http.StatusBadRequest {
			t.Errorf("PUT %s = %d %s, want 400", bad, status, body)
		}
	}
	cfg := fakebridge.DefaultConfig()
	cfg.Channels = nil
	if err := s.fake.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if got := s.fake.Config(); got.Channels == nil || len(got.Channels) != 0 {
		t.Errorf("channels after SetConfig without them: %+v", got.Channels)
	}
	s.checkChannel(t, fakebridge.Token, fakebridge.ChannelAlerts).problem(t, http.StatusForbidden,
		gen.ErrorClassForbidden)
	s.configure(t, `{"channels":{"ch-alerts":"alerts"}}`)
	if a := s.checkChannel(t, fakebridge.Token, fakebridge.ChannelAlerts); a.status != http.StatusOK {
		t.Errorf("a channel added to none = %d %s", a.status, a.body)
	}
}

// network is a bridge.Network that lets the client reach the fake on the loopback address.
func network(t *testing.T) bridge.Network {
	t.Helper()
	p, err := outbound.ParsePolicy("standard", []string{"127.0.0.0/8"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return bridge.Network{Policy: outbound.StaticPolicy(p), Log: logging.New(io.Discard, logging.LevelInfo),
		Real: clock.Real{}}
}

// TestConfig: a PUT replaces the fields it gives, each as a whole, keeps the others, and answers the configuration;
// limits without edit_max_age_seconds serve it as null.
func TestConfig(t *testing.T) {
	s := start(t)
	s.configure(t, `{"event_delivery":["poll"],"capabilities":{"thread_messages":"all"},"user_lookup":"email",
		"limits":{"max_body_length":1000}}`)
	i := s.getInfo(t, fakebridge.Token).valid(t)
	if !slices.Equal(i.EventDelivery, []gen.InfoEventDelivery{gen.Poll}) ||
		!slices.Equal(i.Features(), []string{"thread_messages"}) || i.UserLookup != gen.Email ||
		i.Limits.MaxBodyLength != 1000 || !i.Limits.EditMaxAgeSeconds.IsNull() || i.ProtocolVersion != "1" {
		t.Errorf("info after the patch: %s", i.Raw)
	}
	s.configure(t, `{"capabilities":{"delete":{}},"limits":{"max_body_length":4000,"edit_max_age_seconds":3600}}`)
	i = s.getInfo(t, fakebridge.Token).valid(t)
	if !slices.Equal(i.Features(), []string{"delete"}) || !i.Capabilities.Delete.MaxAgeSeconds.IsNull() ||
		i.Limits.EditMaxAgeSeconds.MustGet() != 3600 || !slices.Equal(i.EventDelivery, []gen.InfoEventDelivery{gen.Poll}) {
		t.Errorf("info after the second patch: %s", i.Raw)
	}

	status, body := s.control(t, http.MethodGet, "/_fake/config", "")
	var c fakebridge.Config
	if err := json.Unmarshal([]byte(body), &c); err != nil || status != http.StatusOK || c.UserLookup != gen.Email ||
		c.InfoPaddingBytes != 0 || len(c.Revoked) != 0 {
		t.Errorf("GET /_fake/config = %d %s", status, body)
	}
	if got := s.fake.Config(); got.Limits.MaxBodyLength != 4000 || got.Capabilities.Delete == nil {
		t.Errorf("Config() = %+v", got)
	}

	for _, bad := range []string{`{`, `{"unknown":1}`, `{"info_padding_bytes":-1}`, `{"limits":{"x":1}}`,
		`{"info_padding_bytes":8388609}`, `{"event_delivery":"poll"}`, `{} {}`} {
		if status, body := s.control(t, http.MethodPut, "/_fake/config", bad); status != http.StatusBadRequest {
			t.Errorf("PUT %s = %d %s, want 400", bad, status, body)
		}
	}
	if got := s.fake.Config(); got.UserLookup != gen.Email {
		t.Errorf("a refused PUT changed the configuration: %+v", got)
	}
}

// TestSetConfig: the Go accessors replace and return copies of the configuration, and refuse a padding out of range.
func TestSetConfig(t *testing.T) {
	s := start(t)
	c := fakebridge.DefaultConfig()
	c.EventDelivery = []gen.InfoEventDelivery{gen.Push}
	c.Limits.EditMaxAgeSeconds = nil
	if err := s.fake.SetConfig(c); err != nil {
		t.Fatal(err)
	}
	c.EventDelivery[0] = gen.Poll
	got := s.fake.Config()
	got.Capabilities.Buttons.MaxPerMessage = 2
	i := s.getInfo(t, fakebridge.Token).valid(t)
	if !slices.Equal(i.EventDelivery, []gen.InfoEventDelivery{gen.Push}) || i.Capabilities.Buttons.MaxPerMessage != 10 ||
		!i.Limits.EditMaxAgeSeconds.IsNull() {
		t.Errorf("info after SetConfig: %s", i.Raw)
	}
	for _, n := range []int{-1, fakebridge.MaxPadding + 1} {
		c.InfoPaddingBytes = n
		if err := s.fake.SetConfig(c); err == nil {
			t.Errorf("SetConfig with a padding of %d", n)
		}
	}
}

// TestHarness: the fake records the requests of the protocol with their Authorization header, answers the faults of
// the harness, and answers 501 to an operation it does not implement yet.
func TestHarness(t *testing.T) {
	s := start(t)
	s.getInfo(t, fakebridge.Token).valid(t)
	status, body := s.control(t, http.MethodGet, "/_fake/requests", "")
	var reqs []fakeserver.Request
	if err := json.Unmarshal([]byte(body), &reqs); err != nil || status != http.StatusOK || len(reqs) != 1 ||
		reqs[0].Path != "/v1/info" || reqs[0].Status != http.StatusOK ||
		!slices.Equal(reqs[0].Headers["Authorization"], []string{"Bearer " + fakebridge.Token}) {
		t.Fatalf("GET /_fake/requests = %d %s", status, body)
	}

	if status, body := s.control(t, http.MethodPost, "/_fake/faults", `{"path":"/v1/info","status":500,
		"content_type":"application/problem+json","body":"{\"title\":\"down\",\"status\":500,\"class\":\"transient\"}",
		"times":1}`); status != http.StatusNoContent {
		t.Fatalf("POST /_fake/faults = %d %s", status, body)
	}
	a := s.getInfo(t, fakebridge.Token)
	if a.invalid != nil {
		t.Errorf("the fault does not match api/bridge.yaml: %v", a.invalid)
	}
	a.problem(t, http.StatusInternalServerError, gen.ErrorClassTransient)
	s.getInfo(t, fakebridge.Token).valid(t)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.fake.URL()+"/v1/info", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+fakebridge.Token)
	send(t, req).problem(t, http.StatusNotImplemented, gen.ErrorClassInvalid)
	req.Header.Set("Authorization", "Bearer nope")
	send(t, req).problem(t, http.StatusUnauthorized, gen.ErrorClassForbidden)
	if n := len(s.fake.Requests()); n != 5 {
		t.Errorf("%d requests recorded, want 5", n)
	}
}

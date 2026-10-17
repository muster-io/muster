// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package fakebridge_test

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

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
	"github.com/oapi-codegen/nullable"

	"github.com/muster-io/muster/internal/bridge"
	"github.com/muster-io/muster/internal/bridge/gen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/webhooks"
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
	return s.exchange(t, request{method: method, path: path, token: token})
}

// request is a request of the protocol: the method and path of its operation, the token and the Idempotency-Key, none
// when empty, and the body, sent as JSON unless it is nil and as it is when it is a string. broken marks a request
// that breaks api/bridge.yaml on purpose: the validator must refuse it, and the answer is still checked.
type request struct {
	method, path, token, key string
	body                     any
	broken                   bool
}

// exchange sends rq after checking it against api/bridge.yaml and checks the answer, as getInfo does.
func (s *spec) exchange(t *testing.T, rq request) answer {
	t.Helper()
	var body io.Reader
	switch b := rq.body.(type) {
	case nil:
	case string:
		body = strings.NewReader(b)
	default:
		data, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(t.Context(), rq.method, s.fake.URL()+rq.path, body)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if rq.token != "" {
		req.Header.Set("Authorization", "Bearer "+rq.token)
	}
	if rq.key != "" {
		req.Header.Set("Idempotency-Key", rq.key)
	}
	route, params, err := s.router.FindRoute(req)
	if err != nil {
		t.Fatalf("%s %s is no operation of api/bridge.yaml: %v", rq.method, rq.path, err)
	}
	in := &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route,
		Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc}}
	switch err := openapi3filter.ValidateRequest(t.Context(), in); {
	case err != nil && !rq.broken:
		t.Fatalf("the request %s %s does not match api/bridge.yaml: %v", rq.method, rq.path, err)
	case err == nil && rq.broken:
		t.Fatalf("the request %s %s %v matches api/bridge.yaml", rq.method, rq.path, rq.body)
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
		c.InfoPaddingBytes != 0 || len(c.Revoked) != 0 || !c.Replays {
		t.Errorf("GET /_fake/config = %d %s", status, body)
	}
	if got := s.fake.Config(); got.Limits.MaxBodyLength != 4000 || got.Capabilities.Delete == nil {
		t.Errorf("Config() = %+v", got)
	}
	s.configure(t, `{"replays":false}`)
	if got := s.fake.Config(); got.Replays || got.UserLookup != gen.Email {
		t.Errorf("Config() after replays off = %+v", got)
	}

	for _, bad := range []string{`{`, `{"unknown":1}`, `{"info_padding_bytes":-1}`, `{"limits":{"x":1}}`,
		`{"info_padding_bytes":8388609}`, `{"event_delivery":"poll"}`, `{} {}`, `{"replays":"no"}`} {
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

// envelope is a Root message to channel as Muster posts it.
func envelope(channel string) gen.MessageInput {
	return gen.MessageInput{ChannelId: channel, Kind: gen.Root, Status: gen.Firing, Color: "#d32f2f", Emoji: "🔴",
		Title: "#1 DiskFull",
		Link:  gen.Link{Url: "http://localhost:8080/alert-groups/AG00000000000A", Label: "Open in Muster"},
		Body:  "> **pod** i1", Buttons: []gen.Button{{Label: "Ack", Style: gen.Primary, Data: "a1.k1.c2ln"}},
		Mentions: []gen.Mention{}, Notify: true}
}

// content is the envelope of an edit to status, as Muster sends it.
func content(status gen.Status) gen.MessageContent {
	e := envelope(fakebridge.ChannelAlerts)
	return gen.MessageContent{Status: status, Color: "#f57c00", Emoji: "🟠", Title: e.Title, Link: e.Link, Body: e.Body,
		Buttons: []gen.Button{}}
}

// replyOf is a Thread reply under the thread_ref ref, null when it is nil.
func replyOf(ref *string) gen.ThreadReplyInput {
	in := gen.ThreadReplyInput{ThreadRef: nullable.NewNullNullable[string](), Body: "New Alerts: 1",
		Buttons: []gen.Button{}, Mentions: []gen.Mention{}}
	if ref != nil {
		in.ThreadRef = nullable.NewNullableWithValue(*ref)
	}
	return in
}

func (s *spec) post(t *testing.T, token, key string, in gen.MessageInput) answer {
	t.Helper()
	return s.exchange(t, request{method: http.MethodPost, path: "/v1/messages", token: token, key: key, body: in})
}

func (s *spec) edit(t *testing.T, token, id string, in gen.MessageContent) answer {
	t.Helper()
	return s.exchange(t, request{method: http.MethodPatch, path: "/v1/messages/" + bridge.Segment(id), token: token,
		body: in})
}

func (s *spec) reply(t *testing.T, token, id, key string, in gen.ThreadReplyInput) answer {
	t.Helper()
	return s.exchange(t, request{method: http.MethodPost, path: "/v1/messages/" + bridge.Segment(id) + "/thread-replies",
		token: token, key: key, body: in})
}

// ok decodes an answer of status that matches the protocol into v.
func (a answer) ok(t *testing.T, status int, v any) {
	t.Helper()
	if a.invalid != nil {
		t.Fatalf("the answer %d does not match api/bridge.yaml: %v\n%s", a.status, a.invalid, a.body)
	}
	if a.status != status {
		t.Fatalf("status %d, want %d: %s", a.status, status, a.body)
	}
	if err := json.Unmarshal(a.body, v); err != nil {
		t.Fatalf("answer %s: %v", a.body, err)
	}
}

func (a answer) posted(t *testing.T) gen.MessagePosted {
	t.Helper()
	var p gen.MessagePosted
	a.ok(t, http.StatusCreated, &p)
	return p
}

func (a answer) replied(t *testing.T) gen.ThreadReplyPosted {
	t.Helper()
	var p gen.ThreadReplyPosted
	a.ok(t, http.StatusCreated, &p)
	return p
}

// refused checks that the answer is a problem of the class that matches the protocol.
func (a answer) refused(t *testing.T, status int, class gen.ErrorClass) gen.Problem {
	t.Helper()
	if a.invalid != nil {
		t.Errorf("the problem %d does not match api/bridge.yaml: %v\n%s", a.status, a.invalid, a.body)
	}
	a.problem(t, status, class)
	var p gen.Problem
	_ = json.Unmarshal(a.body, &p)
	return p
}

// client is Muster's client of the bot of token at the fake.
func (s *spec) client(t *testing.T, token string) *bridge.Client {
	t.Helper()
	c, err := bridge.NewClient(network(t), bridge.Settings{BaseURL: s.fake.URL(), Token: logging.Secret(token)})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// listing is GET /_fake/messages, decoded, and as it was sent.
func (s *spec) listing(t *testing.T) ([]fakebridge.Message, string) {
	t.Helper()
	status, body := s.control(t, http.MethodGet, "/_fake/messages", "")
	var ms []fakebridge.Message
	if err := json.Unmarshal([]byte(body), &ms); err != nil || status != http.StatusOK {
		t.Fatalf("GET /_fake/messages = %d %s", status, body)
	}
	return ms, body
}

// TestMessages is postMessage, editMessage and replyInThread of the fake (C-22.FR-4 to FR-6), checked against
// api/bridge.yaml: a post answers m<N> with its link and no thread_ref, an edit keeps the whole envelope, a reply
// answers r<N> with the thread_ref t-<message id> whatever thread_ref it sent; /_fake/messages and Messages list them
// with the bot's number, never its token, and edits without Mentions. Muster's client reads the answers.
func TestMessages(t *testing.T) {
	s := start(t)
	p := s.post(t, fakebridge.Token, "pub-DS1-1-1", envelope(fakebridge.ChannelAlerts)).posted(t)
	if p.MessageId != "m1" || p.Url.MustGet() != s.fake.URL()+"/m/m1" || p.ThreadRef.IsSpecified() || p.Replayed ||
		p.Truncated {
		t.Errorf("posted %+v", p)
	}
	var e gen.MessageEdited
	s.edit(t, fakebridge.Token, "m1", content(gen.Acknowledged)).ok(t, http.StatusOK, &e)
	if e.Truncated {
		t.Errorf("edited %+v", e)
	}
	ref := "t-m1"
	for i, in := range []gen.ThreadReplyInput{replyOf(nil), replyOf(&ref)} {
		r := s.reply(t, fakebridge.Token, "m1", "reply-DS1-"+strconv.Itoa(i), in).replied(t)
		if r.MessageId != "r"+strconv.Itoa(i+1) || r.ThreadRef != "t-m1" || r.Replayed || r.Truncated {
			t.Errorf("reply %d = %+v", i, r)
		}
	}

	c := s.client(t, fakebridge.SecondToken)
	ctx := t.Context()
	out, r := c.PostMessage(ctx, outbound.ClassDelivery, "pub-DS2-1-1", envelope(fakebridge.ChannelAlertsProd))
	if !r.OK() || out.MessageId != "m2" || out.Url.MustGet() != s.fake.URL()+"/m/m2" || out.ThreadRef.IsSpecified() {
		t.Fatalf("Muster's post = %+v, %+v", out, r)
	}
	if _, r := c.EditMessage(ctx, outbound.ClassDelivery, "m2", content(gen.Resolved)); !r.OK() {
		t.Errorf("Muster's edit = %+v", r)
	}
	rep, r := c.ReplyInThread(ctx, outbound.ClassDelivery, "m2", "reply-DS2-1", replyOf(nil))
	if !r.OK() || rep.MessageId != "r3" || rep.ThreadRef != "t-m2" || rep.Replayed {
		t.Errorf("Muster's reply = %+v, %+v", rep, r)
	}

	ms, body := s.listing(t)
	for _, token := range []string{fakebridge.Token, fakebridge.SecondToken} {
		if strings.Contains(body, token) {
			t.Errorf("/_fake/messages shows the token %s: %s", token, body)
		}
	}
	if len(ms) != 2 {
		t.Fatalf("messages %s", body)
	}
	m := ms[0]
	if m.ID != "m1" || m.Bot != 1 || m.ChannelID != fakebridge.ChannelAlerts || m.URL != s.fake.URL()+"/m/m1" ||
		m.Deleted || m.Truncated || m.Envelope.Title != "#1 DiskFull" || !m.Envelope.Notify ||
		len(m.Envelope.Buttons) != 1 || len(m.Edits) != 1 || m.Edits[0].Status != gen.Acknowledged ||
		len(m.Replies) != 2 {
		t.Errorf("m1 = %+v", m)
	}
	if r0, r1 := m.Replies[0], m.Replies[1]; r0.ID != "r1" || r0.SentThreadRef != nil || r0.ThreadRef != "t-m1" ||
		!r0.Input.ThreadRef.IsNull() || r1.ID != "r2" || r1.SentThreadRef == nil || *r1.SentThreadRef != "t-m1" ||
		r1.Input.Body != "New Alerts: 1" {
		t.Errorf("replies of m1 = %+v", m.Replies)
	}
	if m := ms[1]; m.ID != "m2" || m.Bot != 2 || m.ChannelID != fakebridge.ChannelAlertsProd ||
		len(m.Edits) != 1 || m.Edits[0].Status != gen.Resolved || len(m.Replies) != 1 || m.Replies[0].ID != "r3" {
		t.Errorf("m2 = %+v", m)
	}
	var raw []struct {
		Envelope map[string]json.RawMessage   `json:"envelope"`
		Edits    []map[string]json.RawMessage `json:"edits"`
	}
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw[0].Envelope["mentions"]; !ok {
		t.Errorf("the envelope has no mentions: %s", body)
	}
	for _, member := range []string{"mentions", "notify", "channel_id", "kind"} {
		if _, ok := raw[0].Edits[0][member]; ok {
			t.Errorf("an edit has %s: %s", member, body)
		}
	}
	if got := s.fake.Messages(); len(got) != 2 || got[0].ID != "m1" || len(got[0].Replies) != 2 {
		t.Errorf("Messages() = %+v", got)
	}
}

// TestMessageRefusals: the fake answers what api/bridge.yaml specifies for a request that cannot succeed — forbidden
// for a channel the bot cannot use, invalid for a missing or malformed Idempotency-Key, a body that breaks the protocol
// or has a member it does not define, and an id the bot never posted — and 401 without a valid token; nothing is
// posted.
func TestMessageRefusals(t *testing.T) {
	s := start(t)
	s.post(t, fakebridge.Token, "pub-1", envelope(fakebridge.ChannelAlerts)).posted(t)
	for _, ch := range []string{fakebridge.ChannelNoBot, "ch-unknown"} {
		p := s.post(t, fakebridge.Token, "pub-"+ch, envelope(ch)).refused(t, http.StatusForbidden,
			gen.ErrorClassForbidden)
		if p.Detail == nil || *p.Detail != fakebridge.MessageNotMember {
			t.Errorf("%s = %+v", ch, p)
		}
	}
	noButtons := envelope(fakebridge.ChannelAlerts)
	noButtons.Buttons = nil
	noMentions := envelope(fakebridge.ChannelAlerts)
	noMentions.Mentions = nil
	noReplyMentions := replyOf(nil)
	noReplyMentions.Mentions = nil
	noReplyButtons := replyOf(nil)
	noReplyButtons.Buttons = nil
	editButtons := content(gen.Firing)
	editButtons.Buttons = nil
	post, postJSON := envelope(fakebridge.ChannelAlerts), mustJSON(t, envelope(fakebridge.ChannelAlerts))
	editJSON, replyJSON := mustJSON(t, content(gen.Firing)), mustJSON(t, replyOf(nil))
	const messages, edit, replies = "/v1/messages", "/v1/messages/m1", "/v1/messages/m1/thread-replies"
	const postM, patchM = http.MethodPost, http.MethodPatch
	for name, rq := range map[string]request{
		"no key":             {method: postM, path: messages, body: post, broken: true},
		"a key with a space": {method: postM, path: messages, key: "a b", body: post, broken: true},
		"not JSON":           {method: postM, path: messages, key: "k", body: `{"channel_id":`, broken: true},
		"buttons null":       {method: postM, path: messages, key: "k", body: noButtons, broken: true},
		"mentions null":      {method: postM, path: messages, key: "k", body: noMentions, broken: true},
		"an unknown kind": {method: postM, path: messages, key: "k",
			body: strings.Replace(postJSON, `"root"`, `"poll"`, 1), broken: true},
		"no channel": {method: postM, path: messages, key: "k",
			body: strings.Replace(postJSON, `"ch-alerts"`, `""`, 1), broken: true},
		"an unknown member": {method: postM, path: messages, key: "k",
			body: strings.Replace(postJSON, `{`, `{"extra":1,`, 1)},
		"an edit with mentions": {method: patchM, path: edit,
			body: strings.Replace(editJSON, `{`, `{"mentions":[],`, 1)},
		"an edit of buttons null": {method: patchM, path: edit, body: editButtons, broken: true},
		"an edit of a bad status": {method: patchM, path: edit,
			body: strings.Replace(editJSON, `"firing"`, `"gone"`, 1), broken: true},
		"an edit that is not JSON": {method: patchM, path: edit, body: `[`, broken: true},
		"a reply without a key":    {method: postM, path: replies, body: replyOf(nil), broken: true},
		"a reply without thread_ref": {method: postM, path: replies, key: "k",
			body: strings.Replace(replyJSON, `,"thread_ref":null`, ``, 1), broken: true},
		"a reply of buttons null":  {method: postM, path: replies, key: "k", body: noReplyButtons, broken: true},
		"a reply of mentions null": {method: postM, path: replies, key: "k", body: noReplyMentions, broken: true},
		"a reply that is not JSON": {method: postM, path: replies, key: "k", body: `{`, broken: true},
		"an edit of an unknown id": {method: patchM, path: "/v1/messages/m9", body: content(gen.Firing)},
		"a reply to an unknown id": {method: postM, path: "/v1/messages/m9/thread-replies", key: "k",
			body: replyOf(nil)},
	} {
		rq.token = fakebridge.Token
		if a := s.exchange(t, rq); a.status != http.StatusBadRequest {
			t.Errorf("%s = %d %s", name, a.status, a.body)
		} else {
			a.refused(t, http.StatusBadRequest, gen.ErrorClassInvalid)
		}
	}
	s.edit(t, fakebridge.SecondToken, "m1", content(gen.Firing)).refused(t, http.StatusBadRequest,
		gen.ErrorClassInvalid)
	s.reply(t, fakebridge.SecondToken, "m1", "k", replyOf(nil)).refused(t, http.StatusBadRequest,
		gen.ErrorClassInvalid)
	s.post(t, "nope", "k", envelope(fakebridge.ChannelAlerts)).refused(t, http.StatusUnauthorized,
		gen.ErrorClassForbidden)
	if ms := s.fake.Messages(); len(ms) != 1 || len(ms[0].Edits) != 0 || len(ms[0].Replies) != 0 {
		t.Errorf("messages %+v", ms)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestIdempotency is C-22.FR-6 at the fake: a repeated Idempotency-Key answers its first result with replayed true
// and posts nothing, whatever the body of the repeat, for posts and Thread replies; keys belong to the bot, so the
// other bot's same key posts; a failed request stores nothing; with replays off every repeat posts again, as for a
// bridge that lost its keys, and the keys stored before still replay once they are on again.
func TestIdempotency(t *testing.T) {
	s := start(t)
	first := s.post(t, fakebridge.Token, "k1", envelope(fakebridge.ChannelAlerts)).posted(t)
	other := envelope(fakebridge.ChannelAlertsProd)
	other.Title = "#2 Other"
	again := s.post(t, fakebridge.Token, "k1", other).posted(t)
	if !again.Replayed || again.MessageId != first.MessageId || again.Url.MustGet() != first.Url.MustGet() {
		t.Errorf("repeat = %+v, first %+v", again, first)
	}
	if p := s.post(t, fakebridge.SecondToken, "k1", other).posted(t); p.Replayed || p.MessageId != "m2" {
		t.Errorf("the same key of the other bot = %+v", p)
	}
	s.post(t, fakebridge.Token, "k2", envelope(fakebridge.ChannelNoBot)).refused(t, http.StatusForbidden,
		gen.ErrorClassForbidden)
	if p := s.post(t, fakebridge.Token, "k2", envelope(fakebridge.ChannelAlerts)).posted(t); p.Replayed ||
		p.MessageId != "m3" {
		t.Errorf("the retry of a failed post = %+v", p)
	}
	r := s.reply(t, fakebridge.Token, "m1", "k3", replyOf(nil)).replied(t)
	if again := s.reply(t, fakebridge.Token, "m1", "k3", replyOf(&r.ThreadRef)).replied(t); !again.Replayed ||
		again.MessageId != r.MessageId || again.ThreadRef != r.ThreadRef {
		t.Errorf("a repeated reply = %+v, first %+v", again, r)
	}
	if ms := s.fake.Messages(); len(ms) != 3 || ms[0].Envelope.Title != "#1 DiskFull" || len(ms[0].Replies) != 1 {
		t.Fatalf("messages %+v", ms)
	}

	s.configure(t, `{"replays":false}`)
	for _, key := range []string{"k4", "k4", "k1"} {
		if p := s.post(t, fakebridge.Token, key, envelope(fakebridge.ChannelAlerts)).posted(t); p.Replayed {
			t.Errorf("%s with replays off = %+v", key, p)
		}
	}
	if r := s.reply(t, fakebridge.Token, "m1", "k3", replyOf(nil)).replied(t); r.Replayed || r.MessageId != "r2" {
		t.Errorf("a repeated reply with replays off = %+v", r)
	}
	if ms := s.fake.Messages(); len(ms) != 6 || len(ms[0].Replies) != 2 {
		t.Fatalf("%d messages with replays off: %+v", len(ms), ms)
	}
	s.configure(t, `{"replays":true}`)
	if p := s.post(t, fakebridge.Token, "k1", other).posted(t); !p.Replayed || p.MessageId != "m1" {
		t.Errorf("k1 with replays on again = %+v", p)
	}
	if p := s.post(t, fakebridge.Token, "k4", other).posted(t); p.Replayed || p.MessageId != "m7" {
		t.Errorf("k4, sent with replays off, = %+v", p)
	}
}

// TestLostAnswerIsStored is C-22.AC-6 at the fake: a fault with a delay_ms and no status posts the message and holds
// the answer, so Muster's client, which stops waiting, reports the call as Unanswered; the retry with the same key
// gets the first answer, replayed, and the fake holds one message.
func TestLostAnswerIsStored(t *testing.T) {
	s := start(t)
	if err := s.fake.SetFault(fakeserver.Fault{Path: "/v1/messages", DelayMs: 60_000, Times: 1}); err != nil {
		t.Fatal(err)
	}
	c := s.client(t, fakebridge.Token)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, r := c.PostMessage(ctx, outbound.ClassDelivery, "pub-lost", envelope(fakebridge.ChannelAlerts)); r.OK() ||
		!r.Unanswered {
		t.Fatalf("the held post = %+v", r)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(s.fake.Messages()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	p := s.post(t, fakebridge.Token, "pub-lost", envelope(fakebridge.ChannelAlerts)).posted(t)
	if !p.Replayed || p.MessageId != "m1" {
		t.Errorf("the retry = %+v", p)
	}
	out, r := c.PostMessage(t.Context(), outbound.ClassDelivery, "pub-lost", envelope(fakebridge.ChannelAlerts))
	if !r.OK() || !out.Replayed || out.MessageId != "m1" {
		t.Errorf("Muster's retry = %+v, %+v", out, r)
	}
	if ms := s.fake.Messages(); len(ms) != 1 {
		t.Errorf("messages %+v", ms)
	}
	reqs := s.fake.Requests()
	if len(reqs) != 3 || reqs[0].Status != 0 || reqs[1].Status != http.StatusCreated ||
		reqs[0].Headers["Idempotency-Key"][0] != "pub-lost" || reqs[2].Headers["Idempotency-Key"][0] != "pub-lost" {
		t.Errorf("requests %+v", reqs)
	}
}

// TestDeletedMessage: DELETE /_fake/messages/{id} deletes a message as a person would: its later edits and Thread
// replies answer gone, as api/bridge.yaml specifies and Muster's client maps it, the message stays listed as deleted, a
// reply's key from before still replays, and an unknown id is 404.
func TestDeletedMessage(t *testing.T) {
	s := start(t)
	s.post(t, fakebridge.Token, "k1", envelope(fakebridge.ChannelAlerts)).posted(t)
	first := s.reply(t, fakebridge.Token, "m1", "k2", replyOf(nil)).replied(t)
	if status, body := s.control(t, http.MethodDelete, "/_fake/messages/m1", ""); status != http.StatusNoContent {
		t.Fatalf("DELETE /_fake/messages/m1 = %d %s", status, body)
	}
	for _, a := range []answer{s.edit(t, fakebridge.Token, "m1", content(gen.Acknowledged)),
		s.reply(t, fakebridge.Token, "m1", "k3", replyOf(&first.ThreadRef))} {
		if p := a.refused(t, http.StatusNotFound, gen.ErrorClassGone); p.Title == nil ||
			*p.Title != fakebridge.MessageGone || p.Reason != nil {
			t.Errorf("after the deletion = %+v", p)
		}
	}
	if again := s.reply(t, fakebridge.Token, "m1", "k2", replyOf(nil)).replied(t); !again.Replayed {
		t.Errorf("a key from before the deletion = %+v", again)
	}
	c := s.client(t, fakebridge.Token)
	if _, r := c.EditMessage(t.Context(), outbound.ClassDelivery, "m1", content(gen.Resolved)); r.Outcome().Kind !=
		delivery.OutcomeGone {
		t.Errorf("Muster's edit = %+v", r)
	}
	if _, r := c.ReplyInThread(t.Context(), outbound.ClassDelivery, "m1", "k4", replyOf(nil)); r.Outcome().Kind !=
		delivery.OutcomeGone {
		t.Errorf("Muster's reply = %+v", r)
	}
	if ms := s.fake.Messages(); len(ms) != 1 || !ms[0].Deleted || len(ms[0].Edits) != 0 || len(ms[0].Replies) != 1 {
		t.Errorf("messages %+v", ms)
	}
	if status, body := s.control(t, http.MethodDelete, "/_fake/messages/m9", ""); status != http.StatusNotFound {
		t.Errorf("DELETE of an unknown message = %d %s", status, body)
	}
}

// TestMessageFaults: the faults of the harness answer the message operations with the problems a test scripts —
// rate_limited with Retry-After and the scope bot on an edit, as api/bridge.yaml specifies and Muster's client maps
// it — and change nothing; `*` matches one path segment, so an edit's fault leaves the Thread replies alone.
func TestMessageFaults(t *testing.T) {
	s := start(t)
	s.post(t, fakebridge.Token, "k1", envelope(fakebridge.ChannelAlerts)).posted(t)
	if status, body := s.control(t, http.MethodPost, "/_fake/faults", `{"path":"/v1/messages/*","status":429,
		"retry_after_seconds":7,"content_type":"application/problem+json",
		"body":"{\"title\":\"Slow down\",\"status\":429,\"class\":\"rate_limited\",\"scope\":\"bot\"}","times":2}`); status !=
		http.StatusNoContent {
		t.Fatalf("POST /_fake/faults = %d %s", status, body)
	}
	a := s.edit(t, fakebridge.Token, "m1", content(gen.Acknowledged))
	if p := a.refused(t, http.StatusTooManyRequests, gen.ErrorClassRateLimited); p.Scope == nil ||
		*p.Scope != gen.Bot || a.header.Get("Retry-After") != "7" {
		t.Errorf("the scripted problem = %+v, Retry-After %q", p, a.header.Get("Retry-After"))
	}
	s.reply(t, fakebridge.Token, "m1", "k2", replyOf(nil)).replied(t)
	_, r := s.client(t, fakebridge.Token).EditMessage(t.Context(), outbound.ClassDelivery, "m1",
		content(gen.Acknowledged))
	if o := r.Outcome(); o.Kind != delivery.OutcomeRetryAfter || o.RetryAfter != 7*time.Second ||
		o.Scope != delivery.ScopeConnection {
		t.Errorf("Muster's edit = %+v", o)
	}
	var e gen.MessageEdited
	s.edit(t, fakebridge.Token, "m1", content(gen.Acknowledged)).ok(t, http.StatusOK, &e)
	if ms := s.fake.Messages(); len(ms) != 1 || len(ms[0].Edits) != 1 || len(ms[0].Replies) != 1 {
		t.Errorf("messages %+v", ms)
	}
}

// TestEditWindowClosedFault is C-22.AC-9 at the fake: limits.edit_max_age_seconds of /_fake/config is served in info,
// and the fake never enforces it — Muster's window runs on the development clock — so a harness fault answering gone
// with the reason edit_window_closed is what refuses an edit earlier than declared, as api/bridge.yaml specifies and
// Muster's client classifies it; the fault matches the edit's path only, so Thread replies are untouched, and the
// message is neither edited nor deleted.
func TestEditWindowClosedFault(t *testing.T) {
	s := start(t)
	s.configure(t, `{"limits":{"max_body_length":4000,"edit_max_age_seconds":600}}`)
	if i := s.getInfo(t, fakebridge.Token).valid(t); i.Limits.EditMaxAgeSeconds.MustGet() != 600 {
		t.Fatalf("info %s", i.Raw)
	}
	s.post(t, fakebridge.Token, "k1", envelope(fakebridge.ChannelAlerts)).posted(t)
	if status, body := s.control(t, http.MethodPost, "/_fake/faults", `{"path":"/v1/messages/*","status":404,
		"content_type":"application/problem+json",
		"body":"{\"title\":\"Too old to edit\",\"status\":404,\"class\":\"gone\",\"reason\":\"edit_window_closed\"}",
		"times":2}`); status != http.StatusNoContent {
		t.Fatalf("POST /_fake/faults = %d %s", status, body)
	}
	if r := s.reply(t, fakebridge.Token, "m1", "k2", replyOf(nil)).replied(t); r.ThreadRef != "t-m1" {
		t.Errorf("a Thread reply under the fault = %+v", r)
	}
	if p := s.edit(t, fakebridge.Token, "m1", content(gen.Acknowledged)).refused(t, http.StatusNotFound,
		gen.ErrorClassGone); p.Reason == nil || *p.Reason != gen.EditWindowClosed {
		t.Errorf("the refused edit = %+v", p)
	}
	_, r := s.client(t, fakebridge.Token).EditMessage(t.Context(), outbound.ClassDelivery, "m1",
		content(gen.Acknowledged))
	if r.Classification.Class != bridge.ClassGone || r.Classification.Reason != bridge.ReasonEditWindowClosed ||
		r.Outcome().Kind != delivery.OutcomeGone {
		t.Errorf("Muster's edit = %+v", r)
	}
	if ms := s.fake.Messages(); len(ms) != 1 || ms[0].Deleted || len(ms[0].Edits) != 0 || len(ms[0].Replies) != 1 {
		t.Errorf("messages %+v", ms)
	}
	var e gen.MessageEdited
	s.edit(t, fakebridge.Token, "m1", content(gen.Acknowledged)).ok(t, http.StatusOK, &e)
	if ms := s.fake.Messages(); len(ms[0].Edits) != 1 {
		t.Errorf("an edit once the fault is spent: %+v", ms)
	}
}

// TestTruncated is C-22.FR-7 at the fake: a body longer than limits.max_body_length code points is answered truncated
// by a post, an edit and a reply, and listed so, with the body kept as Muster sent it; a body of exactly the limit is
// not truncated.
func TestTruncated(t *testing.T) {
	s := start(t)
	s.configure(t, `{"limits":{"max_body_length":10}}`)
	in := envelope(fakebridge.ChannelAlerts)
	in.Body = strings.Repeat("é", 10)
	if p := s.post(t, fakebridge.Token, "k1", in).posted(t); p.Truncated {
		t.Errorf("a body of the limit = %+v", p)
	}
	in.Body = strings.Repeat("é", 11)
	if p := s.post(t, fakebridge.Token, "k2", in).posted(t); !p.Truncated {
		t.Errorf("a body over the limit = %+v", p)
	}
	edit := content(gen.Acknowledged)
	edit.Body = in.Body
	var e gen.MessageEdited
	if s.edit(t, fakebridge.Token, "m2", edit).ok(t, http.StatusOK, &e); !e.Truncated {
		t.Errorf("an edit over the limit = %+v", e)
	}
	reply := replyOf(nil)
	reply.Body = in.Body
	if r := s.reply(t, fakebridge.Token, "m2", "k3", reply).replied(t); !r.Truncated || r.ThreadRef != "t-m2" {
		t.Errorf("a reply over the limit = %+v", r)
	}
	ms := s.fake.Messages()
	if len(ms) != 2 || ms[0].Truncated || !ms[1].Truncated || ms[1].Envelope.Body != in.Body ||
		!ms[1].Replies[0].Truncated || ms[1].Replies[0].Input.Body != in.Body {
		t.Errorf("messages %+v", ms)
	}
}

// poll sends pollEvents with the query, checked as getInfo is.
func (s *spec) poll(t *testing.T, token, query string) answer {
	t.Helper()
	return s.exchange(t, request{method: http.MethodGet, path: "/v1/events?" + query, token: token})
}

// pollBroken sends pollEvents with a query that breaks api/bridge.yaml on purpose.
func (s *spec) pollBroken(t *testing.T, token, query string) answer {
	t.Helper()
	return s.exchange(t, request{method: http.MethodGet, path: "/v1/events?" + query, token: token, broken: true})
}

// batch is a pollEvents answer that matches the protocol: its events, each of which Muster decodes, and next_cursor.
func (a answer) batch(t *testing.T) ([]bridge.Event, string) {
	t.Helper()
	var b struct {
		Events     []json.RawMessage `json:"events"`
		NextCursor string            `json:"next_cursor"`
	}
	a.ok(t, http.StatusOK, &b)
	if b.Events == nil {
		t.Fatalf("events is not a list: %s", a.body)
	}
	out := make([]bridge.Event, 0, len(b.Events))
	for _, raw := range b.Events {
		ev, err := bridge.DecodeEvent(raw)
		if err != nil {
			t.Fatalf("Muster refuses the event %s: %v", raw, err)
		}
		out = append(out, ev)
	}
	return out, b.NextCursor
}

// ids are the ids of events.
func ids(events []bridge.Event) []string {
	out := []string{}
	for _, ev := range events {
		out = append(out, ev.ID)
	}
	return out
}

// press sends POST /_fake/press with body and returns the event it queued.
func (s *spec) press(t *testing.T, body string) fakebridge.Event {
	t.Helper()
	status, out := s.control(t, http.MethodPost, "/_fake/press", body)
	var ev fakebridge.Event
	if err := json.Unmarshal([]byte(out), &ev); err != nil || status != http.StatusOK {
		t.Fatalf("POST /_fake/press %s = %d %s", body, status, out)
	}
	return ev
}

// said sends POST /_fake/thread-message with body and returns the event it queued.
func (s *spec) said(t *testing.T, body string) fakebridge.Event {
	t.Helper()
	status, out := s.control(t, http.MethodPost, "/_fake/thread-message", body)
	var ev fakebridge.Event
	if err := json.Unmarshal([]byte(out), &ev); err != nil || status != http.StatusOK {
		t.Fatalf("POST /_fake/thread-message %s = %d %s", body, status, out)
	}
	return ev
}

// decoded is the queued event as Muster decodes it.
func decoded(t *testing.T, ev fakebridge.Event) bridge.Event {
	t.Helper()
	out, err := bridge.DecodeEvent(ev.Event)
	if err != nil {
		t.Fatalf("Muster refuses the event %s: %v", ev.Event, err)
	}
	return out
}

// waitHeld waits until n requests to path are being answered, held by the fake.
func (s *spec) waitHeld(t *testing.T, path string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		held := 0
		for _, r := range s.fake.Requests() {
			if r.Path == path && r.Status == 0 {
				held++
			}
		}
		if held >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no %d held requests to %s", n, path)
}

// TestPollEvents is pollEvents of the fake (C-22.FR-11), checked against api/bridge.yaml and decoded by Muster: the
// events of the token's bot after the cursor, the index of the next event, all of them without a cursor, and the
// same events again for an older cursor; an empty batch at once with wait 0; a long poll held while there is nothing
// and answered as soon as the bot has an event, and left alone by the other bot's events; a held poll whose client
// gives up ends without an answer; a cursor the fake never gave and a wait outside 0…60 are invalid; Muster's client
// reads the batches.
func TestPollEvents(t *testing.T) {
	s := start(t)
	if evs, next := s.poll(t, fakebridge.Token, "wait=0").batch(t); len(evs) != 0 || next != "0" {
		t.Fatalf("an empty queue = %v, %q", ids(evs), next)
	}
	s.post(t, fakebridge.Token, "k1", envelope(fakebridge.ChannelAlerts)).posted(t)
	s.post(t, fakebridge.SecondToken, "k2", envelope(fakebridge.ChannelAlerts)).posted(t)
	s.press(t, `{"message_id":"m1","user_id":"u-bob","button":"Ack"}`)
	s.press(t, `{"message_id":"m2","user_id":"u-bob","button":"Ack"}`)
	s.said(t, `{"message_id":"m1","user_id":"u-alice","text":"ack"}`)

	evs, next := s.poll(t, fakebridge.Token, "wait=0").batch(t)
	if !slices.Equal(ids(evs), []string{"e1", "e3"}) || next != "2" || evs[0].Type != bridge.TypePress ||
		evs[1].Type != bridge.TypeThreadMessage {
		t.Fatalf("all events of bot 1 = %+v, %q", evs, next)
	}
	if evs, next := s.poll(t, fakebridge.Token, "cursor=1&wait=0").batch(t); !slices.Equal(ids(evs),
		[]string{"e3"}) || next != "2" {
		t.Errorf("after the cursor 1 = %v, %q", ids(evs), next)
	}
	if evs, next := s.poll(t, fakebridge.Token, "cursor=2&wait=0").batch(t); len(evs) != 0 || next != "2" {
		t.Errorf("after the last event = %v, %q", ids(evs), next)
	}
	if evs, next := s.poll(t, fakebridge.SecondToken, "wait=0").batch(t); !slices.Equal(ids(evs),
		[]string{"e2"}) || next != "1" {
		t.Errorf("the events of bot 2 = %v, %q", ids(evs), next)
	}

	for _, bad := range []string{"cursor=3", "cursor=x", "cursor=-1", "cursor=%2B1", "cursor="} {
		s.poll(t, fakebridge.Token, bad+"&wait=0").refused(t, http.StatusBadRequest, gen.ErrorClassInvalid)
	}
	for _, bad := range []string{"wait=61", "wait=-1", "wait=x"} {
		s.pollBroken(t, fakebridge.Token, bad).problem(t, http.StatusBadRequest, gen.ErrorClassInvalid)
	}

	type polled struct {
		evs  []bridge.Event
		next string
	}
	held := make(chan polled, 1)
	go func() {
		a := s.poll(t, fakebridge.Token, "cursor=2&wait=60")
		var b struct {
			Events     []json.RawMessage `json:"events"`
			NextCursor string            `json:"next_cursor"`
		}
		_ = json.Unmarshal(a.body, &b)
		var p polled
		for _, raw := range b.Events {
			ev, _ := bridge.DecodeEvent(raw)
			p.evs = append(p.evs, ev)
		}
		p.next = b.NextCursor
		held <- p
	}()
	s.waitHeld(t, "/v1/events", 1)
	s.press(t, `{"message_id":"m2","user_id":"u-vera","button":"Ack"}`)
	select {
	case p := <-held:
		t.Fatalf("an event of the other bot ended the long poll: %v", ids(p.evs))
	default:
	}
	s.said(t, `{"message_id":"m1","user_id":"u-vera","text":"resolve"}`)
	select {
	case p := <-held:
		if !slices.Equal(ids(p.evs), []string{"e5"}) || p.next != "3" || p.evs[0].User.ExternalId != fakebridge.VeraID {
			t.Errorf("the long poll = %+v, %q", p.evs, p.next)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the long poll did not end with the event")
	}

	c := s.client(t, fakebridge.Token)
	cursor := "3"
	ctx, cancel := context.WithCancel(t.Context())
	ended := make(chan bridge.Result, 1)
	go func() {
		_, r := c.PollEvents(ctx, &cursor, 60*time.Second)
		ended <- r
	}()
	s.waitHeld(t, "/v1/events", 1)
	cancel()
	select {
	case r := <-ended:
		if r.OK() {
			t.Errorf("a cancelled poll = %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled poll did not end")
	}
	b, r := c.PollEvents(t.Context(), nil, 0)
	if !r.OK() || len(b.Events) != 3 || b.NextCursor != "3" {
		t.Errorf("Muster's poll = %+v, %+v", b, r)
	}
	start := time.Now()
	if evs, next := s.poll(t, fakebridge.SecondToken, "cursor=2&wait=1").batch(t); len(evs) != 0 || next != "2" ||
		time.Since(start) < 900*time.Millisecond {
		t.Errorf("a wait of 1 s = %v, %q after %v", ids(evs), next, time.Since(start))
	}
}

// TestPress is POST /_fake/press: a press of the button of a label in the latest content of a Root message — its last
// edit, or what was posted — or of a Thread reply, with the data unchanged, by one of the fake's people, queued as
// e<N> for the message's bot with occurred_at now or as given; data given is sent as it is for any message id, to bot 1
// unless the message or bot names another; GET /_fake/events lists the events with their bots; Muster decodes them.
func TestPress(t *testing.T) {
	s := start(t)
	s.post(t, fakebridge.Token, "k1", envelope(fakebridge.ChannelAlerts)).posted(t)
	before := time.Now().UTC()
	ev := s.press(t, `{"message_id":"m1","user_id":"u-bob","button":"Ack"}`)
	if got := decoded(t, ev); ev.Bot != 1 || got.ID != "e1" || got.Type != bridge.TypePress || got.MessageID != "m1" ||
		got.Data != "a1.k1.c2ln" || got.User.ExternalId != fakebridge.BobID || got.User.DisplayName != "Bob Brown" ||
		got.User.Handle.MustGet() != "bob" || got.OccurredAt.Before(before.Add(-time.Second)) ||
		got.OccurredAt.After(time.Now().Add(time.Second)) {
		t.Errorf("the press = %+v", got)
	}

	edit := content(gen.Acknowledged)
	edit.Buttons = []gen.Button{{Label: "Resolve", Style: gen.Danger, Data: "a1.k1.cmVz"}}
	var e gen.MessageEdited
	s.edit(t, fakebridge.Token, "m1", edit).ok(t, http.StatusOK, &e)
	reply := replyOf(nil)
	reply.Buttons = []gen.Button{{Label: "Still on it", Style: gen.Primary, Data: "r1.k1.c3RpbGw"}}
	s.reply(t, fakebridge.Token, "m1", "k2", reply).replied(t)
	s.post(t, fakebridge.SecondToken, "k3", envelope(fakebridge.ChannelAlerts)).posted(t)

	at := "2026-10-14T12:00:00Z"
	for _, c := range []struct {
		body, id, message, data, user string
		bot                           int
	}{
		{`{"message_id":"m1","user_id":"u-alice","button":"Resolve","occurred_at":"` + at + `"}`, "e2", "m1",
			"a1.k1.cmVz", fakebridge.AliceID, 1},
		{`{"message_id":"r1","user_id":"u-vera","button":"Still on it"}`, "e3", "r1", "r1.k1.c3RpbGw",
			fakebridge.VeraID, 1},
		{`{"message_id":"m2","user_id":"u-bob","button":"Ack"}`, "e4", "m2", "a1.k1.c2ln", fakebridge.BobID, 2},
		{`{"message_id":"m1","user_id":"u-bob","button":"Resolve","data":"a9.k1.AAAAAAAAAAAA"}`, "e5", "m1",
			"a9.k1.AAAAAAAAAAAA", fakebridge.BobID, 1},
		{`{"message_id":"nope","user_id":"u-bob","data":"x"}`, "e6", "nope", "x", fakebridge.BobID, 1},
		{`{"message_id":"nope","user_id":"u-bob","data":"x","bot":2}`, "e7", "nope", "x", fakebridge.BobID, 2},
	} {
		ev := s.press(t, c.body)
		if got := decoded(t, ev); ev.Bot != c.bot || got.ID != c.id || got.MessageID != c.message ||
			got.Data != c.data || got.User.ExternalId != c.user {
			t.Errorf("%s = bot %d %+v", c.body, ev.Bot, got)
		}
	}
	if got := decoded(t, s.fake.Events()[1]); !got.OccurredAt.Equal(time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("occurred_at %v", got.OccurredAt)
	}

	if status, body := s.control(t, http.MethodDelete, "/_fake/messages/m2", ""); status != http.StatusNoContent {
		t.Fatalf("DELETE m2 = %d %s", status, body)
	}
	for _, bad := range []string{`{"message_id":"m1","user_id":"u-carol","button":"Resolve"}`,
		`{"user_id":"u-bob","button":"Resolve"}`, `{"message_id":"m9","user_id":"u-bob","button":"Ack"}`,
		`{"message_id":"m1","user_id":"u-bob","button":"Ack"}`, `{"message_id":"m2","user_id":"u-bob","button":"Ack"}`,
		`{"message_id":"m1","user_id":"u-bob","data":"x","bot":3}`, `{"message_id":"m1","user_id":"u-bob","x":1}`,
		`{"message_id":"m1","user_id":"u-bob","button":"Resolve","occurred_at":"yesterday"}`} {
		if status, body := s.control(t, http.MethodPost, "/_fake/press", bad); status != http.StatusBadRequest {
			t.Errorf("POST /_fake/press %s = %d %s", bad, status, body)
		}
	}
	if _, body := s.control(t, http.MethodPost, "/_fake/press", `{"message_id":"m1","user_id":"u-bob",
		"button":"Ack"}`); !strings.Contains(body, `[\"Resolve\"]`) {
		t.Errorf("an unknown button names %s", body)
	}

	status, body := s.control(t, http.MethodGet, "/_fake/events", "")
	var listed []fakebridge.Event
	if err := json.Unmarshal([]byte(body), &listed); err != nil || status != http.StatusOK || len(listed) != 8 ||
		listed[3].Bot != 2 || decoded(t, listed[3]).ID != "e4" || decoded(t, listed[7]).Type != bridge.TypeMessageDeleted {
		t.Errorf("GET /_fake/events = %d %s", status, body)
	}
	if evs, _ := s.poll(t, fakebridge.SecondToken, "wait=0").batch(t); !slices.Equal(ids(evs),
		[]string{"e4", "e7", "e8"}) {
		t.Errorf("the events of bot 2 = %v", ids(evs))
	}
}

// TestThreadMessage is POST /_fake/thread-message: a person's message in the Thread of a message of either bot,
// queued for that bot with the message's id, the text as written and the thread_ref t-<message id> once the message
// has a Thread reply, null before; a reply id, an unknown or deleted message, an unknown person and a text longer than
// 4000 code points are refused; Muster decodes the events.
func TestThreadMessage(t *testing.T) {
	s := start(t)
	s.post(t, fakebridge.Token, "k1", envelope(fakebridge.ChannelAlerts)).posted(t)
	s.post(t, fakebridge.SecondToken, "k2", envelope(fakebridge.ChannelAlerts)).posted(t)
	ev := s.said(t, `{"message_id":"m1","user_id":"u-bob","text":"/Snooze 4h"}`)
	if got := decoded(t, ev); ev.Bot != 1 || got.ID != "e1" || got.Type != bridge.TypeThreadMessage ||
		got.MessageID != "m1" || got.ThreadRef != nil || got.Text != "/Snooze 4h" ||
		got.User.ExternalId != fakebridge.BobID || !strings.Contains(string(ev.Event), `"thread_ref":null`) {
		t.Errorf("the thread message = %+v %s", got, ev.Event)
	}
	s.reply(t, fakebridge.Token, "m1", "k3", replyOf(nil)).replied(t)
	long := strings.Repeat("ё", fakebridge.MaxText)
	ev = s.said(t, `{"message_id":"m1","user_id":"u-vera","text":"`+long+`","occurred_at":"2026-10-14T12:00:00Z"}`)
	if got := decoded(t, ev); got.ThreadRef == nil || *got.ThreadRef != "t-m1" || got.Text != long ||
		!got.OccurredAt.Equal(time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("the thread message after a reply = %+v", got)
	}
	if ev := s.said(t, `{"message_id":"m2","user_id":"u-alice","text":"решено"}`); ev.Bot != 2 ||
		decoded(t, ev).Text != "решено" {
		t.Errorf("a thread message of bot 2 = %+v", ev)
	}
	if status, body := s.control(t, http.MethodDelete, "/_fake/messages/m2", ""); status != http.StatusNoContent {
		t.Fatalf("DELETE m2 = %d %s", status, body)
	}
	for _, bad := range []string{`{"message_id":"r1","user_id":"u-bob","text":"ack"}`,
		`{"message_id":"m9","user_id":"u-bob","text":"ack"}`, `{"message_id":"m2","user_id":"u-bob","text":"ack"}`,
		`{"message_id":"m1","user_id":"u-carol","text":"ack"}`,
		`{"message_id":"m1","user_id":"u-bob","text":"` + long + `!"}`} {
		if status, body := s.control(t, http.MethodPost, "/_fake/thread-message", bad); status != http.StatusBadRequest {
			t.Errorf("POST /_fake/thread-message %.80s = %d %s", bad, status, body)
		}
	}
	if evs, next := s.poll(t, fakebridge.Token, "wait=0").batch(t); !slices.Equal(ids(evs), []string{"e1", "e2"}) ||
		next != "2" {
		t.Errorf("the events of bot 1 = %v, %q", ids(evs), next)
	}
}

// TestDeletionEvent is C-22.FR-10 at the fake: the first DELETE /_fake/messages/{id} of a message queues
// message_deleted for its bot while capabilities.deletion_events is on, a second one queues nothing, and with
// deletion_events off or absent a deletion queues nothing; Muster decodes the events.
func TestDeletionEvent(t *testing.T) {
	s := start(t)
	for i, token := range []string{fakebridge.Token, fakebridge.Token, fakebridge.SecondToken, fakebridge.Token} {
		s.post(t, token, "k"+strconv.Itoa(i), envelope(fakebridge.ChannelAlerts)).posted(t)
	}
	del := func(id string) {
		t.Helper()
		if status, body := s.control(t, http.MethodDelete, "/_fake/messages/"+id, ""); status != http.StatusNoContent {
			t.Fatalf("DELETE %s = %d %s", id, status, body)
		}
	}
	del("m1")
	del("m1")
	del("m3")
	evs := s.fake.Events()
	if len(evs) != 2 || evs[0].Bot != 1 || evs[1].Bot != 2 {
		t.Fatalf("events %+v", evs)
	}
	if got := decoded(t, evs[0]); got.ID != "e1" || got.Type != bridge.TypeMessageDeleted || got.MessageID != "m1" ||
		time.Since(got.OccurredAt) > time.Minute {
		t.Errorf("the deletion = %+v", got)
	}
	if got := decoded(t, evs[1]); got.Type != bridge.TypeMessageDeleted || got.MessageID != "m3" {
		t.Errorf("the deletion of bot 2 = %+v", got)
	}
	s.configure(t, `{"capabilities":{"buttons":{"max_per_message":10,"max_per_row":5,"data_max_bytes":64},
		"deletion_events":false}}`)
	del("m2")
	s.configure(t, `{"capabilities":{"delete":{}}}`)
	del("m4")
	if evs := s.fake.Events(); len(evs) != 2 {
		t.Errorf("deletions without deletion events queued %+v", evs)
	}
	if ms := s.fake.Messages(); !ms[0].Deleted || !ms[1].Deleted || !ms[2].Deleted || !ms[3].Deleted {
		t.Errorf("messages %+v", ms)
	}
}

// answerOf sends answerEvent with the token and the key, checked as getInfo is.
func (s *spec) answerOf(t *testing.T, token, key string, in any, broken bool) answer {
	t.Helper()
	return s.exchange(t, request{method: http.MethodPost, path: "/v1/answers", token: token, key: key, body: in,
		broken: broken})
}

func (a answer) shown(t *testing.T) gen.AnswerShown {
	t.Helper()
	var out gen.AnswerShown
	a.ok(t, http.StatusOK, &out)
	return out
}

// TestAnswers is answerEvent of the fake (C-22.FR-13): an answer to a press is shown privately, as ephemeral while
// private_answers has it, else as popup, else in the Thread; an answer to a Thread message in the Thread; a repeated
// Idempotency-Key answers its first result, replayed, and records nothing; an event the bot does not know, another
// bot's and a message_deleted event are gone; a request that breaks the protocol is invalid; GET /_fake/answers lists
// each answer with its event, its message, its person and where it was shown; Muster's client reads the answers.
func TestAnswers(t *testing.T) {
	s := start(t)
	s.post(t, fakebridge.Token, "k1", envelope(fakebridge.ChannelAlerts)).posted(t)
	s.press(t, `{"message_id":"m1","user_id":"u-alice","button":"Ack"}`)
	s.said(t, `{"message_id":"m1","user_id":"u-bob","text":"snooze 2h"}`)
	if status, body := s.control(t, http.MethodDelete, "/_fake/messages/m1", ""); status != http.StatusNoContent {
		t.Fatalf("DELETE m1 = %d %s", status, body)
	}

	if a := s.answerOf(t, fakebridge.Token, "answer-e1", gen.AnswerInput{EventId: "e1", Text: "Not linked"},
		false).shown(t); a.ShownAs != gen.AnswerShownShownAsEphemeral || a.Replayed {
		t.Errorf("the answer to the press = %+v", a)
	}
	if a := s.answerOf(t, fakebridge.Token, "answer-e1", gen.AnswerInput{EventId: "e1", Text: "Other"},
		false).shown(t); a.ShownAs != gen.AnswerShownShownAsEphemeral || !a.Replayed {
		t.Errorf("the repeated answer = %+v", a)
	}
	if a := s.answerOf(t, fakebridge.Token, "answer-e2", gen.AnswerInput{EventId: "e2",
		Text: "Snooze for one of: 1h, 4h, 24h"}, false).shown(t); a.ShownAs != gen.AnswerShownShownAsThread {
		t.Errorf("the answer to the thread message = %+v", a)
	}
	s.answerOf(t, fakebridge.Token, "answer-e3", gen.AnswerInput{EventId: "e3", Text: "x"}, false).refused(t,
		http.StatusNotFound, gen.ErrorClassGone)
	s.answerOf(t, fakebridge.Token, "answer-e9", gen.AnswerInput{EventId: "e9", Text: "x"}, false).refused(t,
		http.StatusNotFound, gen.ErrorClassGone)
	s.answerOf(t, fakebridge.SecondToken, "answer-e1", gen.AnswerInput{EventId: "e1", Text: "x"}, false).refused(t,
		http.StatusNotFound, gen.ErrorClassGone)
	for _, bad := range []any{gen.AnswerInput{EventId: "e1", Text: ""},
		gen.AnswerInput{EventId: "e1", Text: strings.Repeat("я", fakebridge.MaxAnswer+1)},
		gen.AnswerInput{EventId: "e 1", Text: "x"}, map[string]any{"event_id": "e1"}} {
		s.answerOf(t, fakebridge.Token, "answer-bad", bad, true).refused(t, http.StatusBadRequest,
			gen.ErrorClassInvalid)
	}
	s.exchange(t, request{method: http.MethodPost, path: "/v1/answers", token: fakebridge.Token,
		body: gen.AnswerInput{EventId: "e1", Text: "x"}, broken: true}).refused(t, http.StatusBadRequest,
		gen.ErrorClassInvalid)

	s.post(t, fakebridge.Token, "k2", envelope(fakebridge.ChannelAlerts)).posted(t)
	c := s.client(t, fakebridge.Token)
	for _, private := range []string{`["popup"]`, `[]`} {
		s.configure(t, `{"capabilities":{"private_answers":`+private+`}}`)
		ev := decoded(t, s.press(t, `{"message_id":"m2","user_id":"u-vera","button":"Ack"}`))
		out, r := c.AnswerEvent(t.Context(), outbound.ClassInteractive, ev.ID, "You are not permitted to do this")
		want := gen.AnswerShownShownAsPopup
		if private == `[]` {
			want = gen.AnswerShownShownAsThread
		}
		if !r.OK() || out.ShownAs != want || out.Replayed {
			t.Errorf("Muster's answer with private_answers %s = %+v, %+v", private, out, r)
		}
	}

	status, body := s.control(t, http.MethodGet, "/_fake/answers", "")
	var listed []fakebridge.Answer
	if err := json.Unmarshal([]byte(body), &listed); err != nil || status != http.StatusOK {
		t.Fatalf("GET /_fake/answers = %d %s", status, body)
	}
	want := []fakebridge.Answer{
		{Bot: 1, EventID: "e1", EventType: "press", MessageID: "m1", UserID: fakebridge.AliceID, Text: "Not linked",
			ShownAs: "ephemeral"},
		{Bot: 1, EventID: "e2", EventType: "thread_message", MessageID: "m1", UserID: fakebridge.BobID,
			Text: "Snooze for one of: 1h, 4h, 24h", ShownAs: "thread"},
		{Bot: 1, EventID: "e4", EventType: "press", MessageID: "m2", UserID: fakebridge.VeraID,
			Text: "You are not permitted to do this", ShownAs: "popup"},
		{Bot: 1, EventID: "e5", EventType: "press", MessageID: "m2", UserID: fakebridge.VeraID,
			Text: "You are not permitted to do this", ShownAs: "thread"},
	}
	if !slices.Equal(listed, want) || !slices.Equal(s.fake.Answers(), want) {
		t.Errorf("answers %+v", listed)
	}
}

// receiver is a push target that records the events it receives and answers each with the next status of its
// script, the last one again once the script ends.
type receiver struct {
	mu       sync.Mutex
	statuses []int
	got      []fakeserver.Request
	arrived  chan struct{}
}

func newReceiver(t *testing.T, statuses ...int) (*receiver, string) {
	t.Helper()
	rc := &receiver{statuses: statuses, arrived: make(chan struct{}, 100)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rc.mu.Lock()
		rc.got = append(rc.got, fakeserver.Request{Method: r.Method, Path: r.URL.Path, Headers: r.Header.Clone(),
			Body: string(body)})
		status := rc.statuses[min(len(rc.got), len(rc.statuses))-1]
		rc.mu.Unlock()
		w.WriteHeader(status)
		rc.arrived <- struct{}{}
	}))
	t.Cleanup(srv.Close)
	return rc, srv.URL
}

// wait waits for n pushes in all.
func (rc *receiver) wait(t *testing.T, n int) []fakeserver.Request {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		rc.mu.Lock()
		got := slices.Clone(rc.got)
		rc.mu.Unlock()
		if len(got) >= n {
			return got
		}
		select {
		case <-rc.arrived:
		case <-timeout:
			t.Fatalf("%d pushes, want %d", len(got), n)
		}
	}
}

// pushSecret is the push secret of the tests, in the Standard Webhooks form.
const pushSecret = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"

// signedBy checks that the push r carries the event ev, signed with secret as Standard Webhooks does, at about now.
func signedBy(t *testing.T, r fakeserver.Request, ev fakebridge.Event, secret string) {
	t.Helper()
	h := http.Header(r.Headers)
	id, ts, sig := h.Get("Webhook-Id"), h.Get("Webhook-Timestamp"), h.Get("Webhook-Signature")
	at, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || r.Method != http.MethodPost || r.Path != "/api/v1/callbacks/bridge/CN0000000000B3" ||
		id != decoded(t, ev).ID || r.Body != string(ev.Event) || h.Get("Content-Type") != "application/json" ||
		time.Since(time.Unix(at, 0)).Abs() > time.Minute ||
		!webhooks.Verify(logging.Secret(secret), id, ts, []byte(r.Body), sig) {
		t.Errorf("the push %+v of %s", r, ev.Event)
	}
}

// TestPushSignsAndRetriesUntil204 is push mode of the fake (C-22.FR-11): with push set in /_fake/config, every later
// event of either bot is posted to the one target in order, signed with the push secret as Standard Webhooks does —
// which Muster's verifier accepts — and retried with the same webhook-id after a 500 and a 401 until it is answered
// 204; the events stay in the poll queues; events from before the target was set are not pushed; push null stops
// pushing; a target the fake cannot push to is refused.
func TestPushSignsAndRetriesUntil204(t *testing.T) {
	s := start(t)
	rc, url := newReceiver(t, http.StatusInternalServerError, http.StatusUnauthorized, http.StatusNoContent)
	target := url + "/api/v1/callbacks/bridge/CN0000000000B3"
	s.post(t, fakebridge.Token, "k1", envelope(fakebridge.ChannelAlerts)).posted(t)
	s.post(t, fakebridge.SecondToken, "k2", envelope(fakebridge.ChannelAlerts)).posted(t)
	s.press(t, `{"message_id":"m1","user_id":"u-bob","button":"Ack"}`)

	for _, bad := range []string{`{"push":{"url":"ftp://x/y","secret":"` + pushSecret + `"}}`,
		`{"push":{"url":"/relative","secret":"` + pushSecret + `"}}`, `{"push":{"url":"http://x","secret":"nope"}}`,
		`{"push":{"url":"http://x","secret":"whsec_!!"}}`, `{"push":{"url":"http://x"}}`, `{"push":"http://x"}`} {
		if status, body := s.control(t, http.MethodPut, "/_fake/config", bad); status != http.StatusBadRequest {
			t.Errorf("PUT %s = %d %s", bad, status, body)
		}
	}
	c := fakebridge.DefaultConfig()
	c.Push = &fakebridge.PushTarget{URL: target, Secret: "whsec_"}
	if err := s.fake.SetConfig(c); err == nil {
		t.Error("SetConfig with a push secret without a key")
	}
	s.configure(t, `{"push":{"url":"`+target+`","secret":"`+pushSecret+`"}}`)
	if got := s.fake.Config().Push; got == nil || got.URL != target || got.Secret != pushSecret {
		t.Fatalf("push %+v", got)
	}
	first := s.press(t, `{"message_id":"m1","user_id":"u-alice","button":"Ack"}`)
	second := s.said(t, `{"message_id":"m2","user_id":"u-bob","text":"ack"}`)
	got := rc.wait(t, 4)
	if len(got) != 4 {
		t.Fatalf("pushes %+v", got)
	}
	for i, ev := range []fakebridge.Event{first, first, first, second} {
		signedBy(t, got[i], ev, pushSecret)
	}
	if evs, _ := s.poll(t, fakebridge.Token, "wait=0").batch(t); !slices.Equal(ids(evs), []string{"e1", "e2"}) {
		t.Errorf("the poll queue of bot 1 = %v", ids(evs))
	}
	if evs, _ := s.poll(t, fakebridge.SecondToken, "wait=0").batch(t); !slices.Equal(ids(evs), []string{"e3"}) {
		t.Errorf("the poll queue of bot 2 = %v", ids(evs))
	}

	s.configure(t, `{"push":null}`)
	s.press(t, `{"message_id":"m1","user_id":"u-vera","button":"Ack"}`)
	other := fakebridge.DefaultConfig()
	other.Push = &fakebridge.PushTarget{URL: target, Secret: "whsec_c2Vjb25k"}
	if err := s.fake.SetConfig(other); err != nil {
		t.Fatal(err)
	}
	third := s.press(t, `{"message_id":"m1","user_id":"u-vera","button":"Ack"}`)
	got = rc.wait(t, 5)
	if len(got) != 5 {
		t.Fatalf("pushes %+v", got)
	}
	signedBy(t, got[4], third, "whsec_c2Vjb25k")
	if s.fake.Config().Push == nil {
		t.Error("SetConfig dropped the push target")
	}
}

// TestPushDropsOn400 is the 400 answer of the push (C-22.FR-11): an event answered 400 can never be handled and is
// dropped after one attempt, and the next event is pushed.
func TestPushDropsOn400(t *testing.T) {
	s := start(t)
	rc, url := newReceiver(t, http.StatusBadRequest, http.StatusNoContent)
	s.post(t, fakebridge.Token, "k1", envelope(fakebridge.ChannelAlerts)).posted(t)
	s.configure(t, `{"push":{"url":"`+url+`/api/v1/callbacks/bridge/CN0000000000B3","secret":"`+pushSecret+`"}}`)
	first := s.press(t, `{"message_id":"m1","user_id":"u-bob","button":"Ack"}`)
	second := s.said(t, `{"message_id":"m1","user_id":"u-bob","text":"unack"}`)
	got := rc.wait(t, 2)
	signedBy(t, got[0], first, pushSecret)
	signedBy(t, got[1], second, pushSecret)
	third := s.said(t, `{"message_id":"m1","user_id":"u-bob","text":"ack"}`)
	if got = rc.wait(t, 3); len(got) != 3 {
		t.Fatalf("pushes %+v", got)
	}
	signedBy(t, got[2], third, pushSecret)
}

// held is a push target that holds every push until the request ends, and reports each push and each end; it reads
// the body first, so that the server notices the client leaving.
func held(t *testing.T) (url string, arrived, ended chan struct{}) {
	t.Helper()
	arrived, ended = make(chan struct{}, 10), make(chan struct{}, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		arrived <- struct{}{}
		<-r.Context().Done()
		ended <- struct{}{}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, arrived, ended
}

func within(t *testing.T, what string, ch chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("no %s", what)
	}
}

// TestCloseStopsThePusher: Close ends a push in flight and the pusher before it returns, and a later event is never
// pushed; closing the fake's server alone, as `muster dev` does, ends the push in flight too.
func TestCloseStopsThePusher(t *testing.T) {
	s := start(t)
	url, arrived, ended := held(t)
	s.post(t, fakebridge.Token, "k1", envelope(fakebridge.ChannelAlerts)).posted(t)
	s.configure(t, `{"push":{"url":"`+url+`/api/v1/callbacks/bridge/CN0000000000B3","secret":"`+pushSecret+`"}}`)
	s.press(t, `{"message_id":"m1","user_id":"u-bob","button":"Ack"}`)
	within(t, "push", arrived)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := s.fake.Close(ctx); err != nil {
		t.Fatal(err)
	}
	within(t, "end of the push in flight", ended)
	if _, err := s.fake.Press(t.Context(), fakebridge.Press{MessageID: "m1", UserID: fakebridge.BobID,
		Button: "Ack"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-arrived:
		t.Error("an event was pushed after Close")
	case <-time.After(200 * time.Millisecond):
	}

	s2 := start(t)
	url2, arrived2, ended2 := held(t)
	s2.post(t, fakebridge.Token, "k1", envelope(fakebridge.ChannelAlerts)).posted(t)
	s2.configure(t, `{"push":{"url":"`+url2+`/api/v1/callbacks/bridge/CN0000000000B3","secret":"`+pushSecret+`"}}`)
	s2.press(t, `{"message_id":"m1","user_id":"u-bob","button":"Ack"}`)
	within(t, "push", arrived2)
	if err := s2.fake.Server.Close(ctx); err != nil {
		t.Fatal(err)
	}
	within(t, "end of the push in flight after the server closed", ended2)
}

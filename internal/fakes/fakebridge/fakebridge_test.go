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
	"slices"
	"strconv"
	"strings"
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

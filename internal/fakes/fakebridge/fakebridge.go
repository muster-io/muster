// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package fakebridge is the fake bridge of the Bridge protocol (api/bridge.yaml) for tests and `muster dev`. It serves
// two bots, one per published token, each with its own state, as the protocol's "one token, one bot" asks: Token,
// which the demo Connection of `muster dev` uses, and SecondToken. getInfo answers the configured info — the
// messenger "Fake chat", every feature on and both event delivery modes by default — checkChannel answers the
// configured channels (channels.go), and every request with any other token, none or a revoked one gets 401.
// Operations that no story has added yet answer 501.
//
// The control endpoints under /_fake/ come from the fake-server harness (requests, faults) and add GET and PUT
// /_fake/config: the protocol version, the capabilities, the limits, the user lookup, the event delivery modes, the
// revoked tokens, a padding that makes info larger and the channels, which both bots share. The fake answers what it is configured to, also when that
// breaks the protocol, so that the checks of Muster can be tried against a broken bridge.
package fakebridge

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/oapi-codegen/nullable"

	"github.com/muster-io/muster/internal/bridge/gen"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

// The published tokens of the two bots.
const (
	Token       = "muster-dev-bridge-token"   //nolint:gosec // G101: the published development token of the fake
	SecondToken = "muster-dev-bridge-token-b" //nolint:gosec // G101: the published development token of the fake
)

// What getInfo names.
const (
	MessengerName = "Fake chat"
	BridgeName    = "Muster fake bridge"
	BridgeVersion = "1.0.0"
)

// MaxPadding bounds info_padding_bytes: enough for an info larger than the 4 MiB the outbound client reads.
const MaxPadding = 8 << 20

// defaultConfig is the configuration of a new fake, in the form GET /_fake/config shows it.
const defaultConfig = `{
  "protocol_version": "1",
  "capabilities": {
    "buttons": {"max_per_message": 10, "max_per_row": 5, "data_max_bytes": 64,
      "styles": ["primary", "danger", "neutral", "success"]},
    "thread_messages": "all",
    "direct_messages": {"bot_can_start": true},
    "private_answers": ["ephemeral", "popup"],
    "native_mentions": true,
    "quiet_messages": true,
    "status_color": true,
    "delete": {"max_age_seconds": null},
    "deletion_events": true
  },
  "limits": {"max_body_length": 4000, "edit_max_age_seconds": null},
  "user_lookup": "login",
  "event_delivery": ["poll", "push"],
  "revoked": [],
  "info_padding_bytes": 0,
  "channels": {"ch-alerts": "alerts", "ch-alerts-prod": "alerts-prod", "ch-nobot": null}
}`

// Config is the configuration of the fake, as GET and PUT /_fake/config show it. ProtocolVersion, Capabilities,
// Limits, UserLookup and EventDelivery are served in info as they are; Revoked tokens get 401; InfoPaddingBytes adds a
// member "padding" of that many bytes to info, which the protocol allows and Muster ignores, to make it larger.
// Channels are the channels checkChannel knows, by id: the name of one the bot can use, or null for one it cannot;
// any other id is one it cannot use either.
type Config struct {
	ProtocolVersion  string                  `json:"protocol_version"`
	Capabilities     gen.Capabilities        `json:"capabilities"`
	Limits           gen.Limits              `json:"limits"`
	UserLookup       gen.InfoUserLookup      `json:"user_lookup"`
	EventDelivery    []gen.InfoEventDelivery `json:"event_delivery"`
	Revoked          []string                `json:"revoked"`
	InfoPaddingBytes int                     `json:"info_padding_bytes"`
	Channels         map[string]*string      `json:"channels"`
}

// configPatch is a PUT of /_fake/config: each field given replaces the whole value, the others are kept, except
// channels, whose entries are added or replaced one by one: a name adds or renames a channel, null forbids it.
type configPatch struct {
	ProtocolVersion  *string                  `json:"protocol_version"`
	Capabilities     *gen.Capabilities        `json:"capabilities"`
	Limits           *gen.Limits              `json:"limits"`
	UserLookup       *gen.InfoUserLookup      `json:"user_lookup"`
	EventDelivery    *[]gen.InfoEventDelivery `json:"event_delivery"`
	Revoked          *[]string                `json:"revoked"`
	InfoPaddingBytes *int                     `json:"info_padding_bytes"`
	Channels         map[string]*string       `json:"channels"`
}

// DefaultConfig is the configuration of a new fake: protocol version 1, every feature on, a body of up to 4000 code
// points, edits that never expire, user lookup by login, both event delivery modes, and the channels ChannelAlerts and
// ChannelAlertsProd that the bot can use and ChannelNoBot that it cannot.
func DefaultConfig() Config {
	var c Config
	if err := json.Unmarshal([]byte(defaultConfig), &c); err != nil {
		panic(fmt.Sprintf("fakebridge: the default configuration: %v", err))
	}
	return c
}

// bot is the state of one bot, named by its token; the messages and the events of the later stories are kept here,
// apart from the other bot's.
type bot struct {
	token string
}

// Fake is the fake bridge.
type Fake struct {
	*fakeserver.Server

	api *http.ServeMux

	mu     sync.Mutex
	config Config
	bots   map[string]*bot
}

// New returns the fake with DefaultConfig.
func New() *Fake {
	f := &Fake{api: http.NewServeMux(), config: DefaultConfig(),
		bots: map[string]*bot{Token: {token: Token}, SecondToken: {token: SecondToken}}}
	f.api.HandleFunc("GET /v1/info", f.getInfo)
	f.api.HandleFunc("POST /v1/channels/{channel_id}/check", f.checkChannel)
	f.api.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, http.StatusNotImplemented, gen.ErrorClassInvalid,
			r.Method+" "+r.URL.Path+" is not implemented by the fake bridge")
	})
	f.Server = fakeserver.New("bridge", http.HandlerFunc(f.serve))
	f.HandleControl("GET /_fake/config", func(w http.ResponseWriter, _ *http.Request) {
		fakeserver.WriteJSON(w, http.StatusOK, f.Config())
	})
	f.HandleControl("PUT /_fake/config", f.putConfig)
	return f
}

// Config is the configuration in force.
func (f *Fake) Config() Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	return clone(f.config)
}

// SetConfig replaces the whole configuration; it fails on a padding outside 0…MaxPadding.
func (f *Fake) SetConfig(c Config) error {
	if err := checkPadding(c.InfoPaddingBytes); err != nil {
		return err
	}
	c = clone(normalize(c))
	f.mu.Lock()
	defer f.mu.Unlock()
	f.config = c
	return nil
}

func checkPadding(n int) error {
	if n < 0 || n > MaxPadding {
		return fmt.Errorf("info_padding_bytes is from 0 to %d", MaxPadding)
	}
	return nil
}

// normalize returns c with the nullable members it leaves out set to null, as the protocol requires them, where JSON
// would show them as 0, and with no channels rather than none at all.
func normalize(c Config) Config {
	if c.Channels == nil {
		c.Channels = map[string]*string{}
	}
	if !c.Limits.EditMaxAgeSeconds.IsSpecified() {
		c.Limits.EditMaxAgeSeconds = nullable.NewNullNullable[int]()
	}
	if d := c.Capabilities.Delete; d != nil && !d.MaxAgeSeconds.IsSpecified() {
		nd := *d
		nd.MaxAgeSeconds = nullable.NewNullNullable[int]()
		c.Capabilities.Delete = &nd
	}
	return c
}

// clone copies c deeply: the capabilities and limits hold pointers and maps.
func clone(c Config) Config {
	b, err := json.Marshal(c)
	if err == nil {
		var out Config
		if err = json.Unmarshal(b, &out); err == nil {
			return out
		}
	}
	panic(fmt.Sprintf("fakebridge: copy the configuration: %v", err))
}

func (f *Fake) putConfig(w http.ResponseWriter, r *http.Request) {
	var p configPatch
	if err := fakeserver.DecodeJSON(w, r, &p); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if p.InfoPaddingBytes != nil {
		if err := checkPadding(*p.InfoPaddingBytes); err != nil {
			fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	f.mu.Lock()
	c := f.config
	if p.ProtocolVersion != nil {
		c.ProtocolVersion = *p.ProtocolVersion
	}
	if p.Capabilities != nil {
		c.Capabilities = *p.Capabilities
	}
	if p.Limits != nil {
		c.Limits = *p.Limits
	}
	if p.UserLookup != nil {
		c.UserLookup = *p.UserLookup
	}
	if p.EventDelivery != nil {
		c.EventDelivery = *p.EventDelivery
	}
	if p.Revoked != nil {
		c.Revoked = *p.Revoked
	}
	if p.InfoPaddingBytes != nil {
		c.InfoPaddingBytes = *p.InfoPaddingBytes
	}
	if len(p.Channels) > 0 {
		channels := maps.Clone(c.Channels)
		if channels == nil {
			channels = map[string]*string{}
		}
		maps.Copy(channels, p.Channels)
		c.Channels = channels
	}
	f.config = normalize(c)
	f.mu.Unlock()
	fakeserver.WriteJSON(w, http.StatusOK, f.Config())
}

// serve answers the requests of the protocol: 401 unless the bearer token is one of a bot and not revoked.
func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	if _, ok := f.botOf(r); !ok {
		writeProblem(w, http.StatusUnauthorized, gen.ErrorClassForbidden, "Unauthorized")
		return
	}
	f.api.ServeHTTP(w, r)
}

// botOf is the bot of the request's bearer token; false for no token, another one or a revoked one.
func (f *Fake) botOf(r *http.Request) (*bot, bool) {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return nil, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.bots[token]
	if !ok || slices.Contains(f.config.Revoked, token) {
		return nil, false
	}
	return b, true
}

// info is the answer of getInfo: Info, and the padding when one is configured.
type info struct {
	gen.Info
	Padding string `json:"padding,omitempty"`
}

func (f *Fake) getInfo(w http.ResponseWriter, _ *http.Request) {
	c := f.Config()
	i := info{Info: gen.Info{ProtocolVersion: gen.InfoProtocolVersion(c.ProtocolVersion), Capabilities: c.Capabilities,
		Limits: c.Limits, UserLookup: c.UserLookup, EventDelivery: c.EventDelivery},
		Padding: strings.Repeat("x", c.InfoPaddingBytes)}
	i.Bridge.Name, i.Bridge.Version, i.Messenger.Name = BridgeName, BridgeVersion, MessengerName
	fakeserver.WriteJSON(w, http.StatusOK, i)
}

// writeProblem answers a problem of the protocol (RFC 9457) with its class.
func writeProblem(w http.ResponseWriter, status int, class gen.ErrorClass, title string) {
	writeProblemDetail(w, status, class, title, "")
}

// writeProblemDetail is writeProblem with a detail, none when it is empty.
func writeProblemDetail(w http.ResponseWriter, status int, class gen.ErrorClass, title, detail string) {
	p := gen.Problem{Title: &title, Status: &status, Class: &class}
	if detail != "" {
		p.Detail = &detail
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(p)
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package mattermost is the REST client of Mattermost Connections (C-13.FR-1): the calls of the Mattermost REST API v4
// with the bot token as a bearer token, through internal/outbound with the Connection's proxy, in the client class of
// the caller (ADR-0015); and the Destination check of a Mattermost Destination (C-13.FR-10). Muster uses only the REST
// API with the bot account. The adapter of S-061 builds on this client.
package mattermost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/outbound"
)

// The timeouts of a call and the largest answer read.
const (
	connectTimeout = 5 * time.Second
	callTimeout    = 10 * time.Second
	maxAnswerBytes = 4 << 20
)

// defaultRetryAfter is the wait of a 429 without a usable Retry-After; Mattermost sends Retry-After: 1 (F-031).
const defaultRetryAfter = time.Second

// The paths of a Connection's routes for the way its requests travel (ConnectionPath).
const (
	ViaDirect = "direct"
	ViaProxy  = "proxy"
)

// Network is what a Client needs besides its Connection: the outbound address policy, the logger, the real clock of
// the outbound metrics and a resolver (nil: the system resolver).
type Network struct {
	Policy   outbound.PolicySource
	Log      *logging.Logger
	Real     clock.Clock
	Resolver outbound.Resolver
}

// Settings are a Connection as its client needs it: the server URL, the bot token and the proxy, nil for none.
type Settings struct {
	ServerURL string
	Token     logging.Secret
	Proxy     *outbound.Proxy
}

// Client calls the REST API of one Mattermost Connection, in the interactive and the delivery client classes; the
// outbound client of a class is built on its first call.
type Client struct {
	network  Network
	settings Settings
	via      string

	mu      sync.Mutex
	clients map[outbound.Class]*outbound.Client
}

// NewClient builds the client of the Connection s, checking its settings with the interactive class. The bot token
// and the proxy password are registered for redaction.
func NewClient(n Network, s Settings) (*Client, error) {
	c := &Client{network: n, settings: s, via: ViaDirect, clients: map[outbound.Class]*outbound.Client{}}
	if s.Proxy != nil {
		c.via = ViaProxy
	}
	if _, err := c.client(outbound.ClassInteractive); err != nil {
		return nil, err
	}
	return c, nil
}

// client is the outbound client of class, built once.
func (c *Client) client(class outbound.Class) (*outbound.Client, error) {
	if class != outbound.ClassInteractive && class != outbound.ClassDelivery {
		return nil, fmt.Errorf("the Mattermost client has no %s class", class)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if oc, ok := c.clients[class]; ok {
		return oc, nil
	}
	s, n := c.settings, c.network
	oc, err := outbound.New(outbound.Config{Class: class, ConnectTimeout: connectTimeout, Timeout: callTimeout,
		Proxy: s.Proxy, Secrets: []logging.Secret{s.Token}, BaseURL: s.ServerURL, Policy: n.Policy, Logger: n.Log,
		Clock: n.Real, Resolver: n.Resolver, MaxBodyBytes: maxAnswerBytes})
	if err != nil {
		return nil, err
	}
	c.clients[class] = oc
	return oc, nil
}

// Via is how the requests of the client travel: ViaDirect or ViaProxy.
func (c *Client) Via() string { return c.via }

// Result is how a call ended: its outcome for delivery, which classifies the answer as C-13.FR-5 says, the status of
// the answer, 0 without one, and the error id Mattermost answered with, such as api.context.permissions.app_error.
type Result struct {
	Outcome delivery.Outcome
	Status  int
	ErrorID string
}

// OK reports whether the call succeeded.
func (r Result) OK() bool { return r.Outcome.Kind == delivery.OutcomeOK }

// User is a Mattermost user as the API answers it.
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	IsBot    bool   `json:"is_bot"`
}

// Team is a Mattermost team: Name is its URL name, the team_domain of a press.
type Team struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

// The types of Mattermost channels.
const (
	ChannelOpen    = "O"
	ChannelPrivate = "P"
	ChannelDirect  = "D"
	ChannelGroup   = "G"
)

// Channel is a Mattermost channel; a DeleteAt other than 0 is an archived one.
type Channel struct {
	ID          string `json:"id"`
	TeamID      string `json:"team_id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
	DeleteAt    int64  `json:"delete_at"`
}

// Me reads the bot's own user: the check of its token.
func (c *Client) Me(ctx context.Context, class outbound.Class) (User, Result) {
	var u User
	return u, c.get(ctx, class, "/api/v4/users/me", &u)
}

// Teams reads the teams the bot belongs to.
func (c *Client) Teams(ctx context.Context, class outbound.Class) ([]Team, Result) {
	var ts []Team
	return ts, c.get(ctx, class, "/api/v4/users/me/teams", &ts)
}

// Channels reads the channels of the team teamID that the bot is a member of.
func (c *Client) Channels(ctx context.Context, class outbound.Class, teamID string) ([]Channel, Result) {
	var cs []Channel
	return cs, c.get(ctx, class, "/api/v4/users/me/teams/"+url.PathEscape(teamID)+"/channels", &cs)
}

// Team reads the team teamID.
func (c *Client) Team(ctx context.Context, class outbound.Class, teamID string) (Team, Result) {
	var t Team
	return t, c.get(ctx, class, "/api/v4/teams/"+url.PathEscape(teamID), &t)
}

// Channel reads the channel channelID.
func (c *Client) Channel(ctx context.Context, class outbound.Class, channelID string) (Channel, Result) {
	var ch Channel
	return ch, c.get(ctx, class, "/api/v4/channels/"+url.PathEscape(channelID), &ch)
}

// Member reads the bot's membership of the channel channelID; a channel without the bot answers 404 or 403.
func (c *Client) Member(ctx context.Context, class outbound.Class, channelID string) Result {
	var m struct {
		ChannelID string `json:"channel_id"`
	}
	return c.get(ctx, class, "/api/v4/channels/"+url.PathEscape(channelID)+"/members/me", &m)
}

// get reads path into out in the client class.
func (c *Client) get(ctx context.Context, class outbound.Class, path string, out any) Result {
	oc, err := c.client(class)
	if err != nil {
		return Result{Outcome: delivery.Outcome{Kind: delivery.OutcomeFatal, Error: outbound.Untrusted(err.Error())}}
	}
	res, err := oc.Do(ctx, outbound.Request{Method: http.MethodGet, URL: path, Mapping: mapping,
		Header: http.Header{"Authorization": {"Bearer " + string(c.settings.Token)}, "Accept": {"application/json"}}})
	r := resultOf(res, err)
	if !r.OK() {
		return r
	}
	if err := json.Unmarshal(res.Body, out); err != nil {
		return Result{Status: res.Status, Outcome: delivery.Outcome{Kind: delivery.OutcomeUnknown,
			Error: outbound.Untrusted("GET " + path + ": the answer is not the expected JSON")}}
	}
	return r
}

// mapping classifies the status of an answer to the reads of this client (C-13.FR-5): 429 waits for the whole
// Connection, 5xx and 408 are Transient, 401, 403 and 404 Fatal, anything else unknown. The 429 body is plain text, so
// the status and headers decide alone (F-031). A 404 is Fatal here because these reads name the bot, its teams and
// channels; the edits and post reads of S-061 tell a deleted post from a missing channel with their own mapping.
func mapping(status int, _ time.Duration, _ bool) outbound.Outcome {
	switch {
	case status >= 200 && status < 300:
		return outbound.OutcomeOK
	case status == http.StatusTooManyRequests:
		return outbound.OutcomeRetryAfter
	case status >= 500 || status == http.StatusRequestTimeout:
		return outbound.OutcomeTransient
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return outbound.OutcomeFatal
	}
	return outbound.OutcomeUnknown
}

// appError is the body of a Mattermost error answer.
type appError struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}

// resultOf is the Result of an outbound call: a blocked address and a refused redirect are configuration errors and
// Fatal; a timeout or a failed connection is Transient; a RetryAfter holds the whole Connection, because Mattermost
// counts requests per client address, not per channel (F-030).
func resultOf(res outbound.Result, err error) Result {
	if err == nil {
		return Result{Status: res.Status, Outcome: delivery.Outcome{Kind: delivery.OutcomeOK}}
	}
	r := Result{Status: res.Status, Outcome: delivery.Outcome{Error: outbound.Untrusted(err.Error())}}
	// The provider's text is masked of the Connection's secrets; the error id and message are read from it.
	var e appError
	if res.Status != 0 && json.Unmarshal([]byte(res.ProviderError), &e) == nil && e.ID != "" {
		r.ErrorID = e.ID
		r.Outcome.Error = outbound.Untrusted(fmt.Sprintf("Mattermost answered %d: %s (%s)", res.Status, e.Message,
			e.ID))
	}
	switch res.Outcome {
	case outbound.OutcomeRetryAfter:
		r.Outcome.Kind, r.Outcome.Scope = delivery.OutcomeRetryAfter, delivery.ScopeConnection
		r.Outcome.RetryAfter = res.RetryAfter
		if r.Outcome.RetryAfter <= 0 {
			r.Outcome.RetryAfter = defaultRetryAfter
		}
	case outbound.OutcomeTransient:
		r.Outcome.Kind = delivery.OutcomeTransient
	case outbound.OutcomeFatal, outbound.OutcomeBlocked, outbound.OutcomeRedirect:
		r.Outcome.Kind = delivery.OutcomeFatal
	default:
		r.Outcome.Kind = delivery.OutcomeUnknown
	}
	return r
}

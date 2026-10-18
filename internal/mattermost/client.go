// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package mattermost is the REST client of Mattermost Connections (C-13.FR-1): the calls of the Mattermost REST API v4
// with the bot token as a bearer token, through internal/outbound with the Connection's proxy, in the client class of
// the caller (ADR-0015); the dry probe of a check without a token (C-13.FR-2); and the Destination check of a
// Mattermost Destination (C-13.FR-10). Muster uses only the REST API with the bot account. The adapter of S-061 builds
// on this client.
package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/muster-io/muster/internal/accountlinks"
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

// DryProbePath is the path of the dry probe: the system ping, which a Mattermost server answers without a token
// (F-069).
const DryProbePath = "/api/v4/system/ping"

// errNoToken refuses a call with the token on a client that has none: the client of a draft whose stored token may not
// go to its server URL, which makes only the dry probe (C-13.FR-14).
var errNoToken = errors.New("this client has no bot token: only the dry probe runs without one")

// Network is what a Client needs besides its Connection: the outbound address policy, the logger, the real clock of
// the outbound metrics, a resolver (nil: the system resolver) and the Request log, nil for none (C-23.FR-1).
type Network struct {
	Policy   outbound.PolicySource
	Log      *logging.Logger
	Real     clock.Clock
	Resolver outbound.Resolver
	Recorder outbound.Recorder
}

// Settings are a Connection as its client needs it: the server URL, the bot token and the proxy, nil for none; and the
// Connection whose Request log keeps its requests, none for a client that records nothing.
type Settings struct {
	ServerURL string
	Token     logging.Secret
	Proxy     *outbound.Proxy
	Owner     outbound.Owner
}

// Client calls the REST API of one Mattermost Connection, in the interactive and the delivery client classes and, for
// muster doctor, the background class; the outbound client of a class is built on its first call.
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
	if class != outbound.ClassInteractive && class != outbound.ClassDelivery && class != outbound.ClassBackground {
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
		Clock: n.Real, Resolver: n.Resolver, MaxBodyBytes: maxAnswerBytes, Recorder: n.Recorder, Owner: s.Owner})
	if err != nil {
		return nil, err
	}
	c.clients[class] = oc
	return oc, nil
}

// Via is how the requests of the client travel: ViaDirect or ViaProxy.
func (c *Client) Via() string { return c.via }

// Result is how a call ended: its outcome for delivery, which classifies the answer as C-13.FR-5 says, the status of
// the answer, 0 without one, the error id Mattermost answered with, such as api.context.permissions.app_error, and the
// body of the answer as received, which only a Destination test shows, masked (C-16.FR-2); when the call got no answer,
// what failed (outbound.Network*), and whether the outbound address policy refused the address.
type Result struct {
	Outcome delivery.Outcome
	Status  int
	ErrorID string
	Body    []byte
	Network string
	Blocked bool
}

// OK reports whether the call succeeded.
func (r Result) OK() bool { return r.Outcome.Kind == delivery.OutcomeOK }

// FailureCode is the check code of a call that failed for a reason its check does not name (delivery.FailureCode).
func (r Result) FailureCode() string { return delivery.FailureCode(r.Blocked, r.Network) }

// User is a Mattermost user as the API answers it; a DeleteAt other than 0 is a deactivated account.
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	IsBot    bool   `json:"is_bot"`
	// Roles are the user's system roles, separated by spaces, such as "system_user".
	Roles    string `json:"roles"`
	DeleteAt int64  `json:"delete_at"`
}

// Role is a Mattermost role with its permissions; a DeleteAt other than 0 grants nothing.
type Role struct {
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
	DeleteAt    int64    `json:"delete_at"`
}

// PermissionEphemeralPosts is the permission of POST /api/v4/posts/ephemeral, which only the system admin role holds by
// default (F-063).
const PermissionEphemeralPosts = "create_post_ephemeral"

// HintPressAnswersInThread is the hint of a Connection whose bot lacks PermissionEphemeralPosts (D284).
const HintPressAnswersInThread = "the bot may not make ephemeral messages (create_post_ephemeral), so answers to " +
	"button presses show in the Thread of the Root message; give it that permission, for example the system admin " +
	"role, to show them in the channel"

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
	return u, c.get(ctx, class, "getMe", "/api/v4/users/me", &u)
}

// UserByUsername reads the user username, which Mattermost matches without case; an unknown one answers 404.
func (c *Client) UserByUsername(ctx context.Context, class outbound.Class, username string) (User, Result) {
	var u User
	return u, c.get(ctx, class, "getUserByUsername", "/api/v4/users/username/"+url.PathEscape(username), &u)
}

// Grants reports whether the system roles of u grant permission, as the server decides it for u's requests (F-064):
// one read of the roles, POST /api/v4/roles/names, of which any role that is not deleted holds permission. A user
// without roles is granted nothing and needs no read.
func (c *Client) Grants(ctx context.Context, class outbound.Class, u User, permission string) (bool, Result) {
	names := strings.Fields(u.Roles)
	if len(names) == 0 {
		return false, Result{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK}}
	}
	var roles []Role
	r := c.send(ctx, class, "getRolesByNames", http.MethodPost, "/api/v4/roles/names", names, &roles)
	return slices.ContainsFunc(roles, func(role Role) bool {
		return role.DeleteAt == 0 && slices.Contains(role.Permissions, permission)
	}), r
}

// Teams reads the teams the bot belongs to.
func (c *Client) Teams(ctx context.Context, class outbound.Class) ([]Team, Result) {
	var ts []Team
	return ts, c.get(ctx, class, "getTeamsForUser", "/api/v4/users/me/teams", &ts)
}

// Channels reads the channels of the team teamID that the bot is a member of.
func (c *Client) Channels(ctx context.Context, class outbound.Class, teamID string) ([]Channel, Result) {
	var cs []Channel
	return cs, c.get(ctx, class, "getChannelsForTeamForUser", "/api/v4/users/me/teams/"+url.PathEscape(teamID)+
		"/channels", &cs)
}

// Team reads the team teamID.
func (c *Client) Team(ctx context.Context, class outbound.Class, teamID string) (Team, Result) {
	var t Team
	return t, c.get(ctx, class, "getTeam", "/api/v4/teams/"+url.PathEscape(teamID), &t)
}

// Channel reads the channel channelID.
func (c *Client) Channel(ctx context.Context, class outbound.Class, channelID string) (Channel, Result) {
	var ch Channel
	return ch, c.get(ctx, class, "getChannel", "/api/v4/channels/"+url.PathEscape(channelID), &ch)
}

// Member reads the bot's membership of the channel channelID; a channel without the bot answers 404 or 403.
func (c *Client) Member(ctx context.Context, class outbound.Class, channelID string) Result {
	var m struct {
		ChannelID string `json:"channel_id"`
	}
	return c.get(ctx, class, "getChannelMember", "/api/v4/channels/"+url.PathEscape(channelID)+"/members/me", &m)
}

// get reads path into out in the client class, as the operation op.
func (c *Client) get(ctx context.Context, class outbound.Class, op, path string, out any) Result {
	return c.send(ctx, class, op, http.MethodGet, path, nil, out)
}

// DryProbe is `GET <server>/api/v4/system/ping` without the token (C-13.FR-2), which a Mattermost server answers to
// anyone. The Result is the raw answer with its body, which the check names, with the media type of the answer, empty
// without one; a 429 is Transient, never a RetryAfter.
func (c *Client) DryProbe(ctx context.Context, class outbound.Class) (Result, string) {
	oc, err := c.client(class)
	if err != nil {
		return Result{Outcome: delivery.Outcome{Kind: delivery.OutcomeFatal,
			Error: outbound.Untrusted(err.Error())}}, ""
	}
	res, err := oc.Do(ctx, outbound.Request{Method: http.MethodGet, URL: DryProbePath,
		Header: http.Header{"Accept": {"application/json"}}, Mapping: mapping, Operation: "getPing"})
	r := resultOf(res, err)
	r.Body = res.Body
	// A 429 to a request without a token says nothing of the bot's budget: it must not hold the Connection's limiter,
	// least of all when an unsaved server URL answered it.
	if r.Outcome.Kind == delivery.OutcomeRetryAfter {
		r.Outcome.Kind, r.Outcome.Scope, r.Outcome.RetryAfter = delivery.OutcomeTransient, "", 0
	}
	mt, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil {
		return r, ""
	}
	if c.settings.Token != "" {
		mt = strings.ReplaceAll(mt, string(c.settings.Token), redacted)
	}
	return r, mt
}

// send makes one call of method on path in the client class, the operation op of the REST API, with body as JSON
// unless it is nil, and reads a successful answer into out unless it is nil. A client without a token makes no call.
func (c *Client) send(ctx context.Context, class outbound.Class, op, method, path string, body, out any) Result {
	if c.settings.Token == "" {
		return Result{Outcome: delivery.Outcome{Kind: delivery.OutcomeFatal,
			Error: outbound.Untrusted(errNoToken.Error())}}
	}
	oc, err := c.client(class)
	if err != nil {
		return Result{Outcome: delivery.Outcome{Kind: delivery.OutcomeFatal, Error: outbound.Untrusted(err.Error())}}
	}
	header := http.Header{"Authorization": {"Bearer " + string(c.settings.Token)}, "Accept": {"application/json"}}
	var data []byte
	if body != nil {
		if data, err = json.Marshal(body); err != nil {
			return Result{Outcome: delivery.Outcome{Kind: delivery.OutcomeUnknown,
				Error: outbound.Untrusted(method + " " + path + ": the request is not valid JSON")}}
		}
		header.Set("Content-Type", "application/json")
	}
	res, err := oc.Do(ctx, outbound.Request{Method: method, URL: path, Header: header, Body: data, Mapping: mapping,
		Operation: op})
	r := resultOf(res, err)
	r.Body = res.Body
	if !r.OK() || out == nil {
		return r
	}
	if err := json.Unmarshal(res.Body, out); err != nil {
		return Result{Status: res.Status, Outcome: delivery.Outcome{Kind: delivery.OutcomeUnknown,
			Error: outbound.Untrusted(method + " " + path + ": the answer is not the expected JSON")}}
	}
	return r
}

// post is a post as Muster creates, edits and reads it; Props holds the attachment of a Root message.
type post struct {
	ID        string `json:"id,omitempty"`
	ChannelID string `json:"channel_id,omitempty"`
	RootID    string `json:"root_id,omitempty"`
	Message   string `json:"message"`
	Props     *props `json:"props,omitempty"`
}

// props are the properties of a post that Muster sets: its attachments, one for a Root message.
type props struct {
	Attachments []attachment `json:"attachments"`
}

// attachment is a message attachment: its colour, title and link, text, footer and buttons.
type attachment struct {
	Color      string   `json:"color,omitempty"`
	Title      string   `json:"title,omitempty"`
	TitleLink  string   `json:"title_link,omitempty"`
	Text       string   `json:"text,omitempty"`
	Footer     string   `json:"footer,omitempty"`
	FooterIcon string   `json:"footer_icon,omitempty"`
	Actions    []action `json:"actions,omitempty"`
}

// action is a button of an attachment: Mattermost calls integration.url with integration.context when it is pressed.
// A style tints it (F-068); without one it stays plain.
type action struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Type        string      `json:"type"`
	Style       string      `json:"style,omitempty"`
	Integration integration `json:"integration"`
}

type integration struct {
	URL     string        `json:"url"`
	Context actionContext `json:"context"`
}

// actionContext is what a button sends back: its signed action id and the id of the key that signed it, and on a
// test message the nonce of its test, which the bot's own press of it reports (C-16.FR-3).
type actionContext struct {
	Action string `json:"action"`
	KeyID  string `json:"key_id"`
	Test   string `json:"test,omitempty"`
}

// patch is an edit of a post: its message and its props, both replaced.
type patch struct {
	Message string `json:"message"`
	Props   *props `json:"props"`
}

// createPost creates the post p: a Root message, or a Thread reply when p has a root_id. Only the adapter calls it,
// from the delivery worker and the interactive path (lint 3).
func (c *Client) createPost(ctx context.Context, class outbound.Class, p post) (post, Result) {
	var out post
	return out, c.send(ctx, class, "createPost", http.MethodPost, "/api/v4/posts", p, &out)
}

// patchPost edits the post id to p, which never notifies anyone (F-029).
func (c *Client) patchPost(ctx context.Context, class outbound.Class, id string, p patch) (post, Result) {
	var out post
	return out, c.send(ctx, class, "patchPost", http.MethodPut, "/api/v4/posts/"+url.PathEscape(id)+"/patch", p, &out)
}

// getPost reads the post id with the plain read, which answers 404 for a deleted post; Muster never reads with
// include_deleted, which may need the system admin role (F-058).
func (c *Client) getPost(ctx context.Context, class outbound.Class, id string) Result {
	var out post
	return c.get(ctx, class, "getPost", "/api/v4/posts/"+url.PathEscape(id), &out)
}

// ephemeralPost shows message to the user userID alone in the channel channelID: in the channel view without a
// rootID, in the Thread of rootID with one (F-025, F-026).
func (c *Client) ephemeralPost(ctx context.Context, class outbound.Class, userID, channelID, rootID,
	message string) Result {
	body := struct {
		UserID string `json:"user_id"`
		Post   post   `json:"post"`
	}{UserID: userID, Post: post{ChannelID: channelID, RootID: rootID, Message: message}}
	return c.send(ctx, class, "createPostEphemeral", http.MethodPost, "/api/v4/posts/ephemeral", body, nil)
}

// directPost posts message to the user userID in the direct channel of the bot botUserID and the user, which the bot
// opens with POST /api/v4/channels/direct knowing only the user's id (F-028). Only the Account link bots call it,
// inside a DirectOp of the interactive path.
func (c *Client) directPost(ctx context.Context, class outbound.Class, botUserID, userID, message string) Result {
	var ch Channel
	if r := c.send(ctx, class, "createDirectChannel", http.MethodPost, "/api/v4/channels/direct",
		[]string{botUserID, userID}, &ch); !r.OK() {
		return r
	}
	_, r := c.createPost(ctx, class, post{ChannelID: ch.ID, Message: message})
	return r
}

// doAction presses the button actionID of the post postID as the bot (F-054): the server calls the button's
// integration URL as for any press, and answers 400 with "Action integration error" when it could not (F-022).
func (c *Client) doAction(ctx context.Context, class outbound.Class, postID, actionID string) Result {
	return c.send(ctx, class, "doPostAction", http.MethodPost, "/api/v4/posts/"+url.PathEscape(postID)+"/actions/"+
		url.PathEscape(actionID), struct{}{}, nil)
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
	r := Result{Status: res.Status, Outcome: delivery.Outcome{Error: outbound.Untrusted(err.Error())},
		Blocked: res.Outcome == outbound.OutcomeBlocked}
	if oe, ok := errors.AsType[*outbound.Error](err); ok {
		r.Network = oe.Network
	}
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

// LinkBots run the calls of an Account link through the bot of a Mattermost Connection (C-18.FR-3, FR-11): the
// lookup of the account by its username and the bot's direct messages, on the interactive path, never as a delivery.
// They implement accountlinks.MattermostBots.
type LinkBots struct {
	Conns Connections
	Path  Path
}

// Session takes one limiter token of the Connection publicID on the interactive path and runs f with its bot; a 429
// that a call of f gets holds the Connection. A Connection that is unknown, deleted or not a Mattermost one is
// accountlinks.ErrNoConnection; no token within the budget is a *delivery.LimitedError and runs nothing.
func (l LinkBots) Session(ctx context.Context, publicID string,
	f func(ctx context.Context, b accountlinks.MattermostBot) error) error {
	conn, err := l.Conns.Connection(ctx, publicID)
	if errors.Is(err, ErrNoConnection) {
		return accountlinks.ErrNoConnection
	}
	if err != nil {
		return err
	}
	var ferr error
	id := conn.ID
	if _, err := l.Path.Do(ctx, delivery.Subject{Connection: &id}, delivery.DirectOp(
		func(ctx context.Context, call delivery.Call) delivery.Outcome {
			b := &linkBot{conn: conn, class: call.Class, last: delivery.Outcome{Kind: delivery.OutcomeOK}}
			ferr = f(ctx, b)
			return b.last
		})); err != nil {
		return err
	}
	return ferr
}

// linkBot is the bot of a Connection within a Session.
type linkBot struct {
	conn  Connection
	class outbound.Class
	// last is the outcome of the last call that failed, which the Session hands to the interactive path.
	last delivery.Outcome
}

// FindUser finds the user username; an unknown one, a bot and a deactivated account are
// accountlinks.ErrMessengerUserNotFound.
func (b *linkBot) FindUser(ctx context.Context, username string) (accountlinks.MattermostAccount, error) {
	u, r := b.conn.Client.UserByUsername(ctx, b.class, username)
	if r.Status == http.StatusNotFound || (r.OK() && (u.IsBot || u.DeleteAt != 0 || u.ID == "")) {
		return accountlinks.MattermostAccount{}, accountlinks.ErrMessengerUserNotFound
	}
	if err := b.failed("the user lookup", r); err != nil {
		return accountlinks.MattermostAccount{}, err
	}
	return accountlinks.MattermostAccount{ID: u.ID, Username: u.Username}, nil
}

// SendDirect sends text to the user userID in the direct channel of the bot and the user: the bot's user id is the one
// the Connection check learned, or GET /api/v4/users/me when none is stored.
func (b *linkBot) SendDirect(ctx context.Context, userID, text string) error {
	bot := b.conn.BotUserID
	if bot == "" {
		me, r := b.conn.Client.Me(ctx, b.class)
		if err := b.failed("the bot's user", r); err != nil {
			return err
		}
		bot = me.ID
	}
	return b.failed("the direct message", b.conn.Client.directPost(ctx, b.class, bot, userID, text))
}

// failed is the error of a call that did not succeed: a 429 is a *delivery.LimitedError, anything else an
// *accountlinks.MessengerError that names the call and how it ended, never Mattermost's text, which may repeat the
// message.
func (b *linkBot) failed(call string, r Result) error {
	if r.OK() {
		return nil
	}
	b.last = r.Outcome
	if r.Outcome.Kind == delivery.OutcomeRetryAfter {
		return &delivery.LimitedError{RetryAfter: r.Outcome.RetryAfter}
	}
	return &accountlinks.MessengerError{Call: call, Outcome: string(r.Outcome.Kind), Status: r.Status}
}

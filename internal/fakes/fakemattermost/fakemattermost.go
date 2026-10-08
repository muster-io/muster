// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package fakemattermost is the fake Mattermost server. It answers the REST API v4 calls Muster makes for its bot user
// as Mattermost 11.2.2 does in the verified facts (F-022 to F-032, F-054 to F-058, F-062 to F-064): a team with three
// channels and two people, posts, Thread replies, edits, ephemeral posts — refused with 403 unless the bot is a system
// admin — the system roles with their permissions, direct messages, notifications, button presses sent to the
// integration URL with the ephemeral_text of their answers, and the rate limit. Every other call is recorded and
// answered 501. The control endpoints under /_fake/ list what happened and change the fixtures: posts, notifications,
// ephemeral posts, presses, the server log, channel members, archived channels and the server configuration.
package fakemattermost

import (
	"crypto/rand"
	"encoding/json"
	"math"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

// The fixtures: the bot, the people, the team and its channels.
const (
	BotUserID   = "musterdevbotuserfake000000"
	BotUsername = "muster-dev-bot"

	AliceUserID   = "u-alice"
	AliceUsername = "alice"
	BobUserID     = "u-bob"
	BobUsername   = "bob"

	TeamID   = "team-dev"
	TeamName = "dev"

	ChannelAlerts     = "ch-alerts"
	ChannelAlertsProd = "ch-alerts-prod"
	// ChannelNoBot is a channel the bot is not a member of.
	ChannelNoBot = "ch-nobot"
)

const (
	// Version is the Mattermost version the facts were verified on.
	Version = "11.2.2"
	// MaxPostSize is the longest post message in characters (F-057).
	MaxPostSize = 16_383
	// DefaultPressTimeout is how long the server waits for the answer to a press (F-032).
	DefaultPressTimeout = 30 * time.Second

	// The rate limit once it is on (F-030): 10 requests per second per client address, with a burst of 100 on top of
	// the request itself, which Mattermost reports as X-Ratelimit-Limit 101 (F-031).
	RateLimitPerSecond = 10
	RateLimitBurst     = 100

	// ActionIntegrationError is what a person sees when a press fails (F-022).
	ActionIntegrationError = "Action integration error"

	maxBody = 4 << 20
)

// Fake is the fake Mattermost server. Set PressTimeout and Now before it serves.
type Fake struct {
	*fakeserver.Server
	// PressTimeout bounds the wait for an integration's answer to a press; DefaultPressTimeout unless changed.
	PressTimeout time.Duration
	// Now is the fake's time, of posts, records and the rate limit; nil is the system time.
	Now func() time.Time

	api    *http.ServeMux
	client *http.Client

	mu sync.Mutex
	st state
}

// state is everything the fake changes; reset puts the fixtures back.
type state struct {
	config        Config
	channels      map[string]*Channel
	members       map[string]map[string]bool
	posts         map[string]*post
	order         []string
	ephemeral     []Ephemeral
	notifications []Notification
	presses       []Press
	log           []LogEntry
	buckets       map[string]*bucket
}

// Config is the part of the server configuration the fake models, as GET and PUT /_fake/config show it.
type Config struct {
	// AllowedUntrustedInternalConnections lists host names, IP addresses and CIDR ranges, separated by spaces or
	// commas, that presses may call although they resolve to a reserved address (F-022).
	AllowedUntrustedInternalConnections string          `json:"allowed_untrusted_internal_connections"`
	RateLimit                           RateLimitConfig `json:"rate_limit"`
	// RevokedTokens are refused with 401; every other non-empty token is the bot's.
	RevokedTokens []string `json:"revoked_tokens"`
	// BotSystemAdmin gives the bot the system admin role. Off by default, as for a bot with the role Member, which
	// gets 403 from POST /api/v4/posts/ephemeral (F-063).
	BotSystemAdmin bool `json:"bot_system_admin"`
}

type RateLimitConfig struct {
	Enabled bool `json:"enabled"`
}

type configPatch struct {
	AllowedUntrustedInternalConnections *string `json:"allowed_untrusted_internal_connections"`
	RateLimit                           *struct {
		Enabled *bool `json:"enabled"`
	} `json:"rate_limit"`
	RevokedTokens  *[]string `json:"revoked_tokens"`
	BotSystemAdmin *bool     `json:"bot_system_admin"`
}

// User is a Mattermost user as the API shows it.
type User struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Roles     string `json:"roles"`
}

type Team struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
}

// Channel is a channel as the API shows it; DeleteAt is set once it is archived.
type Channel struct {
	ID          string `json:"id"`
	TeamID      string `json:"team_id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
	DeleteAt    int64  `json:"delete_at"`
}

// LogEntry is a line of the server log, as GET /_fake/server-log lists it.
type LogEntry struct {
	AtMs    int64  `json:"at_ms"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

var (
	users = []User{
		{ID: BotUserID, Username: BotUsername, IsBot: true, Roles: "system_user"},
		{ID: AliceUserID, Username: AliceUsername, FirstName: "Alice", Roles: "system_user"},
		{ID: BobUserID, Username: BobUsername, FirstName: "Bob", Roles: "system_user"},
	}
	devTeam = Team{ID: TeamID, Name: TeamName, DisplayName: "Dev", Type: "O"}
)

func userByID(id string) (User, bool) {
	i := slices.IndexFunc(users, func(u User) bool { return u.ID == id })
	if i < 0 {
		return User{}, false
	}
	return users[i], true
}

func userByName(name string) (User, bool) {
	i := slices.IndexFunc(users, func(u User) bool { return strings.EqualFold(u.Username, name) })
	if i < 0 {
		return User{}, false
	}
	return users[i], true
}

func fixtures() state {
	channels := map[string]*Channel{}
	for _, c := range []Channel{
		{ID: ChannelAlerts, Name: "alerts", DisplayName: "Alerts"},
		{ID: ChannelAlertsProd, Name: "alerts-prod", DisplayName: "Alerts prod"},
		{ID: ChannelNoBot, Name: "no-bot", DisplayName: "No bot"},
	} {
		c.TeamID, c.Type = TeamID, "O"
		channels[c.ID] = &c
	}
	everyone := func(withBot bool) map[string]bool {
		m := map[string]bool{AliceUserID: true, BobUserID: true}
		if withBot {
			m[BotUserID] = true
		}
		return m
	}
	return state{
		config:   Config{RevokedTokens: []string{}},
		channels: channels,
		members: map[string]map[string]bool{
			ChannelAlerts:     everyone(true),
			ChannelAlertsProd: everyone(true),
			ChannelNoBot:      everyone(false),
		},
		posts:   map[string]*post{},
		buckets: map[string]*bucket{},
	}
}

func New() *Fake {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	f := &Fake{
		PressTimeout: DefaultPressTimeout,
		api:          http.NewServeMux(),
		client: &http.Client{
			Transport: t,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		st: fixtures(),
	}
	f.Server = fakeserver.New("Mattermost", http.HandlerFunc(f.serve))

	f.api.HandleFunc("GET /api/v4/users/me", f.handleMe)
	f.api.HandleFunc("GET /api/v4/users/me/teams", f.handleMyTeams)
	f.api.HandleFunc("GET /api/v4/users/me/teams/{team_id}/channels", f.handleMyChannels)
	f.api.HandleFunc("GET /api/v4/teams/{team_id}", f.handleTeam)
	f.api.HandleFunc("GET /api/v4/channels/{channel_id}", f.handleChannel)
	f.api.HandleFunc("GET /api/v4/channels/{channel_id}/members/me", f.handleMyMembership)
	f.api.HandleFunc("POST /api/v4/channels/direct", f.handleDirect)
	f.api.HandleFunc("GET /api/v4/config/client", f.handleClientConfig)
	f.api.HandleFunc("POST /api/v4/roles/names", f.handleRolesByNames)
	f.api.HandleFunc("POST /api/v4/posts", f.handleCreatePost)
	f.api.HandleFunc("POST /api/v4/posts/ephemeral", f.handleEphemeral)
	f.api.HandleFunc("GET /api/v4/posts/{post_id}", f.handleGetPost)
	f.api.HandleFunc("PUT /api/v4/posts/{post_id}/patch", f.handlePatchPost)
	f.api.HandleFunc("POST /api/v4/posts/{post_id}/actions/{action_id}", f.handleAction)
	f.api.HandleFunc("/", notImplemented)

	f.HandleControl("GET /_fake/config", f.handleGetConfig)
	f.HandleControl("PUT /_fake/config", f.handlePutConfig)
	f.HandleControl("POST /_fake/reset", func(w http.ResponseWriter, _ *http.Request) {
		f.Reset()
		w.WriteHeader(http.StatusNoContent)
	})
	f.HandleControl("GET /_fake/server-log", func(w http.ResponseWriter, _ *http.Request) {
		fakeserver.WriteJSON(w, http.StatusOK, f.ServerLog())
	})
	f.HandleControl("DELETE /_fake/server-log", f.resetter(func(s *state) { s.log = nil }))
	f.HandleControl("PUT /_fake/channels/{id}/members/{user_id}", f.handleMember(true))
	f.HandleControl("DELETE /_fake/channels/{id}/members/{user_id}", f.handleMember(false))
	f.HandleControl("POST /_fake/channels/{id}/archive", f.handleArchive)
	f.HandleControl("GET /_fake/posts", func(w http.ResponseWriter, _ *http.Request) {
		fakeserver.WriteJSON(w, http.StatusOK, f.Posts())
	})
	f.HandleControl("DELETE /_fake/posts", f.resetter(func(s *state) { s.posts, s.order = map[string]*post{}, nil }))
	f.HandleControl("DELETE /_fake/posts/{id}", f.handleDeletePost)
	f.HandleControl("GET /_fake/notifications", func(w http.ResponseWriter, _ *http.Request) {
		fakeserver.WriteJSON(w, http.StatusOK, f.Notifications())
	})
	f.HandleControl("DELETE /_fake/notifications", f.resetter(func(s *state) { s.notifications = nil }))
	f.HandleControl("GET /_fake/ephemeral", func(w http.ResponseWriter, _ *http.Request) {
		fakeserver.WriteJSON(w, http.StatusOK, f.Ephemeral())
	})
	f.HandleControl("DELETE /_fake/ephemeral", f.resetter(func(s *state) { s.ephemeral = nil }))
	f.HandleControl("POST /_fake/press", f.handlePress)
	f.HandleControl("GET /_fake/presses", func(w http.ResponseWriter, _ *http.Request) {
		fakeserver.WriteJSON(w, http.StatusOK, f.Presses())
	})
	f.HandleControl("DELETE /_fake/presses", f.resetter(func(s *state) { s.presses = nil }))
	return f
}

func (f *Fake) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func (f *Fake) nowMs() int64 { return f.now().UnixMilli() }

// Reset puts the fixtures back: posts, records, members, archived channels and the configuration. The harness's
// recorded requests and faults stay.
func (f *Fake) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.st = fixtures()
}

// Config returns the current configuration.
func (f *Fake) Config() Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.st.config
	c.RevokedTokens = slices.Clone(c.RevokedTokens)
	return c
}

// SetAllowedUntrustedInternalConnections sets the addresses presses may call although they are reserved (F-022), such
// as "localhost 127.0.0.1".
func (f *Fake) SetAllowedUntrustedInternalConnections(v string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.st.config.AllowedUntrustedInternalConnections = v
}

// SetBotSystemAdmin gives the bot the system admin role, with which it may make ephemeral posts (F-063), or takes it.
func (f *Fake) SetBotSystemAdmin(admin bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.st.config.BotSystemAdmin = admin
}

// ServerLog returns the server log lines in order.
func (f *Fake) ServerLog() []LogEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]LogEntry{}, f.st.log...)
}

// logLocked adds a server log line; f.mu is held.
func (f *Fake) logLocked(level, msg string) {
	f.st.log = append(f.st.log, LogEntry{AtMs: f.nowMs(), Level: level, Message: msg})
}

func (f *Fake) resetter(reset func(*state)) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		reset(&f.st)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}
}

func (f *Fake) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	fakeserver.WriteJSON(w, http.StatusOK, f.Config())
}

func (f *Fake) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	var p configPatch
	if err := fakeserver.DecodeJSON(w, r, &p); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	f.mu.Lock()
	c := &f.st.config
	if p.AllowedUntrustedInternalConnections != nil {
		c.AllowedUntrustedInternalConnections = *p.AllowedUntrustedInternalConnections
	}
	if p.RateLimit != nil && p.RateLimit.Enabled != nil && *p.RateLimit.Enabled != c.RateLimit.Enabled {
		c.RateLimit.Enabled = *p.RateLimit.Enabled
		clear(f.st.buckets)
	}
	if p.RevokedTokens != nil {
		c.RevokedTokens = append([]string{}, *p.RevokedTokens...)
	}
	if p.BotSystemAdmin != nil {
		c.BotSystemAdmin = *p.BotSystemAdmin
	}
	f.mu.Unlock()
	fakeserver.WriteJSON(w, http.StatusOK, f.Config())
}

func (f *Fake) handleMember(add bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, userID := r.PathValue("id"), r.PathValue("user_id")
		if _, ok := userByID(userID); !ok {
			fakeserver.WriteError(w, http.StatusNotFound, "unknown user "+userID)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, ok := f.st.channels[id]; !ok {
			fakeserver.WriteError(w, http.StatusNotFound, "unknown channel "+id)
			return
		}
		if add {
			f.st.members[id][userID] = true
		} else {
			delete(f.st.members[id], userID)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (f *Fake) handleArchive(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.st.channels[id]
	if !ok {
		fakeserver.WriteError(w, http.StatusNotFound, "unknown channel "+id)
		return
	}
	if c.DeleteAt == 0 {
		c.DeleteAt = f.nowMs()
	}
	w.WriteHeader(http.StatusNoContent)
}

// serve answers the API: the rate limit first, then the token, then the call.
func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/v4/") {
		if !f.rateLimit(w, r) {
			return
		}
		if !f.authorized(r) {
			writeAppError(w, http.StatusUnauthorized, "api.context.session_expired.app_error",
				"Invalid or expired session, please login again.")
			return
		}
	}
	f.api.ServeHTTP(w, r)
}

func (f *Fake) authorized(r *http.Request) bool {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	token = strings.TrimSpace(token)
	if !ok || !strings.EqualFold(scheme, "bearer") || token == "" {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return !slices.Contains(f.st.config.RevokedTokens, token)
}

// bucket is a token bucket of one client address; it holds up to RateLimitBurst+1 requests.
type bucket struct {
	tokens float64
	at     time.Time
}

// rateLimit applies the rate limit when it is on and reports whether the request may go on (F-030, F-031).
func (f *Fake) rateLimit(w http.ResponseWriter, r *http.Request) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.st.config.RateLimit.Enabled {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	const capacity = RateLimitBurst + 1
	now := f.now()
	b, ok := f.st.buckets[host]
	if !ok {
		b = &bucket{tokens: capacity, at: now}
		f.st.buckets[host] = b
	}
	if elapsed := now.Sub(b.at).Seconds(); elapsed > 0 {
		b.tokens = min(capacity, b.tokens+elapsed*RateLimitPerSecond)
	}
	b.at = now
	allowed := b.tokens >= 1
	if allowed {
		b.tokens--
	}
	h := w.Header()
	h.Set("X-Ratelimit-Limit", strconv.Itoa(capacity))
	h.Set("X-Ratelimit-Remaining", strconv.Itoa(int(math.Floor(b.tokens))))
	h.Set("X-Ratelimit-Reset", strconv.Itoa(int(math.Ceil((capacity-b.tokens)/RateLimitPerSecond))))
	if allowed {
		return true
	}
	h.Set("X-Ratelimit-Remaining", "0")
	h.Set("Retry-After", "1")
	h.Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write([]byte("limit exceeded"))
	return false
}

// appError is the shape of Mattermost's error answers.
type appError struct {
	ID            string `json:"id"`
	Message       string `json:"message"`
	DetailedError string `json:"detailed_error"`
	RequestID     string `json:"request_id"`
	StatusCode    int    `json:"status_code"`
}

func writeAppError(w http.ResponseWriter, status int, id, msg string) {
	fakeserver.WriteJSON(w, status, appError{ID: id, Message: msg, RequestID: newID(), StatusCode: status})
}

func notImplemented(w http.ResponseWriter, _ *http.Request) {
	writeAppError(w, http.StatusNotImplemented, "fake.not_implemented", "not implemented by the fake Mattermost server")
}

func forbidden(w http.ResponseWriter) {
	writeAppError(w, http.StatusForbidden, "api.context.permissions.app_error",
		"You do not have the appropriate permissions.")
}

func unknownChannel(w http.ResponseWriter, id string) {
	writeAppError(w, http.StatusNotFound, "app.channel.get.existing.app_error",
		"Unable to find the existing channel "+id+".")
}

func invalidBody(w http.ResponseWriter, what string) {
	writeAppError(w, http.StatusBadRequest, "api.context.invalid_body_param.app_error",
		"Invalid or missing "+what+" in request body.")
}

// newID returns a Mattermost-style id: 26 lowercase letters and digits.
func newID() string {
	return strings.ToLower(rand.Text())
}

// decodeBody reads an API request body leniently, as Mattermost does: unknown fields are ignored.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(v)
}

func (f *Fake) handleMe(w http.ResponseWriter, _ *http.Request) {
	bot, _ := userByID(BotUserID)
	if f.Config().BotSystemAdmin {
		bot.Roles = "system_admin system_user"
	}
	fakeserver.WriteJSON(w, http.StatusOK, bot)
}

// Role is a Mattermost role as POST /api/v4/roles/names answers it.
type Role struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
	DeleteAt    int64    `json:"delete_at"`
}

// roles are the system roles the fake knows, with a few of their default permissions: only system_admin has
// create_post_ephemeral (F-063).
var roles = []Role{
	{ID: "role-system-user", Name: "system_user", Permissions: []string{"create_direct_channel", "create_team",
		"list_public_teams", "view_members"}},
	{ID: "role-system-admin", Name: "system_admin", Permissions: []string{"create_direct_channel", "create_post",
		"create_post_ephemeral", "manage_system", "read_deleted_posts"}},
}

// handleRolesByNames reads the roles named in the body, as any user with a session may (F-064); unknown names are left
// out.
func (f *Fake) handleRolesByNames(w http.ResponseWriter, r *http.Request) {
	var names []string
	if err := decodeBody(w, r, &names); err != nil || len(names) == 0 {
		invalidBody(w, "rolenames")
		return
	}
	out := []Role{}
	for _, role := range roles {
		if slices.Contains(names, role.Name) {
			out = append(out, role)
		}
	}
	fakeserver.WriteJSON(w, http.StatusOK, out)
}

func (f *Fake) handleMyTeams(w http.ResponseWriter, _ *http.Request) {
	fakeserver.WriteJSON(w, http.StatusOK, []Team{devTeam})
}

func (f *Fake) handleTeam(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("team_id") != TeamID {
		writeAppError(w, http.StatusNotFound, "app.team.get.find.app_error", "Unable to find the existing team.")
		return
	}
	fakeserver.WriteJSON(w, http.StatusOK, devTeam)
}

// handleMyChannels lists the live channels of the team that the bot is a member of; everyone is in team dev.
func (f *Fake) handleMyChannels(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("team_id") != TeamID {
		writeAppError(w, http.StatusNotFound, "app.team.get.find.app_error", "Unable to find the existing team.")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []Channel{}
	for _, c := range f.st.channels {
		if c.TeamID == TeamID && c.DeleteAt == 0 && f.st.members[c.ID][BotUserID] {
			out = append(out, *c)
		}
	}
	slices.SortFunc(out, func(a, b Channel) int { return strings.Compare(a.Name, b.Name) })
	fakeserver.WriteJSON(w, http.StatusOK, out)
}

// handleChannel answers any channel of team dev, which is public, and a direct channel only to its members.
func (f *Fake) handleChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("channel_id")
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.st.channels[id]
	switch {
	case !ok:
		unknownChannel(w, id)
	case c.TeamID == "" && !f.st.members[id][BotUserID]:
		forbidden(w)
	default:
		fakeserver.WriteJSON(w, http.StatusOK, *c)
	}
}

func (f *Fake) handleMyMembership(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("channel_id")
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.st.channels[id]; !ok {
		unknownChannel(w, id)
		return
	}
	if !f.st.members[id][BotUserID] {
		writeAppError(w, http.StatusNotFound, "app.channel.get_member.missing.app_error",
			"No channel member found for that user ID and channel ID.")
		return
	}
	fakeserver.WriteJSON(w, http.StatusOK, map[string]string{
		"channel_id": id,
		"user_id":    BotUserID,
		"roles":      "channel_user",
	})
}

// handleDirect returns the direct channel of two users, creating it on the first call (F-028).
func (f *Fake) handleDirect(w http.ResponseWriter, r *http.Request) {
	var ids []string
	if err := decodeBody(w, r, &ids); err != nil || len(ids) != 2 {
		invalidBody(w, "user_ids")
		return
	}
	for _, id := range ids {
		if _, ok := userByID(id); !ok {
			invalidBody(w, "user_ids")
			return
		}
	}
	slices.Sort(ids)
	id := ids[0] + "__" + ids[1]
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.st.channels[id]
	if !ok {
		c = &Channel{ID: id, Name: id, Type: "D"}
		f.st.channels[id] = c
		f.st.members[id] = map[string]bool{ids[0]: true, ids[1]: true}
	}
	fakeserver.WriteJSON(w, http.StatusCreated, *c)
}

// handleClientConfig answers the old format of the client configuration, which reports MaxPostSize (F-057).
func (f *Fake) handleClientConfig(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("format") != "old" {
		notImplemented(w, r)
		return
	}
	fakeserver.WriteJSON(w, http.StatusOK, map[string]string{
		"Version":     Version,
		"BuildNumber": Version,
		"SiteName":    "Mattermost",
		"MaxPostSize": strconv.Itoa(MaxPostSize),
	})
}

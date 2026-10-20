// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package connections

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/connections/dbgen"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/destinations"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/proxyconf"
	"github.com/muster-io/muster/internal/slack"
)

// TypeSlack is the type of a Slack Connection (C-24.FR-1): the bot of a Slack app in one workspace, at Slack's fixed
// address.
const TypeSlack = "slack"

// The event modes of a Slack Connection (C-24.FR-1); connection.slack.event_mode is socket.
const (
	EventModeSocket = slack.EventModeSocket
	EventModeHTTP   = slack.EventModeHTTP
)

// The default limiter of a Slack Connection (connection.slack.limiter, P-60): 50 Web API calls a minute.
const (
	SlackDefaultLimit      = 50
	SlackDefaultPerSeconds = 60
)

// The steps of the check of a Slack Connection (C-24.FR-2) besides StepToken.
const (
	StepScopes   = slack.StepScopes
	StepAppToken = slack.StepAppToken
)

const (
	fieldSlackAppToken      = "connections.slack_app_token"
	fieldSlackSigningSecret = "connections.slack_signing_secret"
	// The prefixes of a Slack bot token and app-level token (C-24.FR-1).
	botTokenPrefix = "xoxb-"
	appTokenPrefix = "xapp-"
)

// WarningAnotherClientConnected is the warning of a check of a Slack Connection in socket mode whose last hello
// counted another connection of its Slack app than the Leader's: another program, usually a second installation, takes
// some of its presses (C-24.FR-2, F-102).
const WarningAnotherClientConnected = "another_client_connected"

var (
	_ slack.Targets      = (*Service)(nil)
	_ slack.SocketSource = (*Service)(nil)
)

// slackSocketQueries are the queries of the Socket Mode connections of Slack Connections, part of Queries: the
// Connections in socket mode and what a hello counted.
type slackSocketQueries interface {
	ListSocketConnections(ctx context.Context, orgID int64) ([]dbgen.ListSocketConnectionsRow, error)
	RecordSocketHello(ctx context.Context, arg dbgen.RecordSocketHelloParams) (int64, error)
}

// SlackSocket is what the last hello of a Slack Connection's Socket Mode connection counted (C-24.FR-2, FR-9): when
// it came, on the business clock, and the connections of the app beyond those the Leader holds.
type SlackSocket struct {
	HelloAt          time.Time
	OtherConnections int
}

// cachedSlack is the Slack client of a version of a Connection.
type cachedSlack struct {
	version int64
	client  *slack.Client
}

// SlackIdentity is what a check of a Slack Connection found: the workspace and the bot's user id from the step token,
// and the app's id from the step app_token, empty without it.
type SlackIdentity struct {
	TeamID    string
	TeamName  string
	BotUserID string
	AppID     string
}

// SlackCallbackURL is where Slack posts the button presses of the Slack Connection publicID in http mode:
// MUSTER_INGEST_URL followed by /api/v1/callbacks/slack/<public_id> (C-24.FR-1).
func (s *Service) SlackCallbackURL(publicID string) string {
	base := ""
	if s.cfg.IngestURL != nil {
		base = strings.TrimSuffix(s.cfg.IngestURL.String(), "/")
	}
	return base + slack.CallbackPath + publicID
}

// checkSlackAddress validates the event mode of a Slack Connection and clears the address fields of the other types:
// Slack's address is fixed, so the Connection has none.
func checkSlackAddress(in *Input) error {
	in.ServerURL, in.BotAPIBaseURL, in.UpdateMode, in.BridgeBaseURL, in.EventDelivery = "", "", "", "", ""
	if in.EventMode != EventModeSocket && in.EventMode != EventModeHTTP {
		return &FieldError{Pointer: "/event_mode", Code: CodeInvalidFormat,
			Detail: "The event mode is not socket or http."}
	}
	return nil
}

// checkSlackSecrets validates the secrets of the Slack Connection in, saved over stored (nil on a creation)
// (C-24.FR-1): the bot token starts with xoxb- and the app-level token with xapp-; socket mode needs an app-level token
// and http mode a Slack signing secret, each sent now or stored. A change of the event mode keeps the other mode's
// stored secret. The detail never shows a value; an empty value is left to the save, which refuses it as too short.
func checkSlackSecrets(in Input, stored *Connection) error {
	bot, app, signing := in.BotToken, in.AppToken, in.SlackSigningSecret
	switch {
	case bot.Given && bot.Value != "" && !strings.HasPrefix(string(bot.Value), botTokenPrefix):
		return &FieldError{Pointer: "/bot_token", Code: CodeInvalidFormat,
			Detail: "The bot token does not start with " + botTokenPrefix + "."}
	case app.Given && !app.Null && app.Value != "" && !strings.HasPrefix(string(app.Value), appTokenPrefix):
		return &FieldError{Pointer: "/app_token", Code: CodeInvalidFormat,
			Detail: "The app-level token does not start with " + appTokenPrefix + "."}
	case in.EventMode == EventModeSocket && missing(app, stored != nil && stored.AppToken.Set):
		return &FieldError{Pointer: "/app_token", Code: CodeRequired, Detail: "Socket Mode needs an app-level token."}
	case in.EventMode == EventModeHTTP && missing(signing, stored != nil && stored.SlackSigningSecret.Set):
		return &FieldError{Pointer: "/slack_signing_secret", Code: CodeRequired,
			Detail: "HTTP mode needs the Slack signing secret."}
	}
	return nil
}

// missing reports whether a secret that a mode needs is cleared, or neither sent nor stored.
func missing(in keyring.SecretInput, stored bool) bool {
	return in.Null || !in.Given && !stored
}

// slackSecrets applies the app-level token and the Slack signing secret of in to their stored values at now: those of
// a Slack Connection as sent, kept when omitted in either mode; none for the other types. It returns the fields to
// store and the Audit log changes of the secrets.
func (s *Service) slackSecrets(in Input, app, signing keyring.StoredSecret, now time.Time) (keyring.StoredSecret,
	keyring.StoredSecret, []audit.Change, error) {
	appIn, signingIn := in.AppToken, in.SlackSigningSecret
	if in.Type != TypeSlack {
		appIn, signingIn = keyring.Clear, keyring.Clear
	}
	var diff []audit.Change
	app, changed, err := s.cfg.Keyring.ApplySecret(fieldSlackAppToken, app, appIn, now)
	if errors.Is(err, keyring.ErrEmptySecret) {
		return app, signing, nil, &FieldError{Pointer: "/app_token", Code: CodeTooShort,
			Detail: "The app-level token is empty."}
	}
	if err != nil {
		return app, signing, nil, err
	}
	if changed {
		diff = append(diff, audit.Change{Pointer: "/app_token", SecretChanged: true})
	}
	signing, changed, err = s.cfg.Keyring.ApplySecret(fieldSlackSigningSecret, signing, signingIn, now)
	if errors.Is(err, keyring.ErrEmptySecret) {
		return app, signing, nil, &FieldError{Pointer: "/slack_signing_secret", Code: CodeTooShort,
			Detail: "The Slack signing secret is empty."}
	}
	if err != nil {
		return app, signing, nil, err
	}
	if changed {
		diff = append(diff, audit.Change{Pointer: "/slack_signing_secret", SecretChanged: true})
	}
	return app, signing, diff, nil
}

func storedSlackAppToken(r dbgen.GetConnectionRow) keyring.StoredSecret {
	s := keyring.StoredSecret{Ciphertext: r.SlackAppTokenCiphertext, KeyID: r.SlackAppTokenKeyID.String}
	if r.SlackAppTokenUpdatedAt.Valid {
		at := r.SlackAppTokenUpdatedAt.Time.UTC()
		s.UpdatedAt = &at
	}
	return s
}

func storedSlackSigningSecret(r dbgen.GetConnectionRow) keyring.StoredSecret {
	s := keyring.StoredSecret{Ciphertext: r.SlackSigningSecretCiphertext, KeyID: r.SlackSigningSecretKeyID.String}
	if r.SlackSigningSecretUpdatedAt.Valid {
		at := r.SlackSigningSecretUpdatedAt.Time.UTC()
		s.UpdatedAt = &at
	}
	return s
}

// slackClient builds the Web API client of the stored Slack Connection row, with its decrypted bot token, app-level
// token, Slack signing secret and proxy password, which it masks; its requests go to the Request log of the Connection,
// and under muster dev to where the route of the Service's Config sends its bot token.
func (s *Service) slackClient(row dbgen.GetConnectionRow) (*slack.Client, error) {
	token, err := s.cfg.Keyring.OpenSecret(fieldBotToken, storedToken(row))
	if err != nil {
		return nil, fmt.Errorf("open the bot token of the connection %s: %w", row.PublicID, err)
	}
	app, err := s.cfg.Keyring.OpenSecret(fieldSlackAppToken, storedSlackAppToken(row))
	if err != nil {
		return nil, fmt.Errorf("open the app-level token of the connection %s: %w", row.PublicID, err)
	}
	signing, err := s.cfg.Keyring.OpenSecret(fieldSlackSigningSecret, storedSlackSigningSecret(row))
	if err != nil {
		return nil, fmt.Errorf("open the slack signing secret of the connection %s: %w", row.PublicID, err)
	}
	password, err := s.cfg.Keyring.OpenSecret(fieldProxyPassword, storedPassword(row))
	if err != nil {
		return nil, fmt.Errorf("open the proxy password of the connection %s: %w", row.PublicID, err)
	}
	p, err := proxyconf.Parse(row.Proxy)
	if err != nil {
		return nil, err
	}
	return slack.NewClient(slack.Network(s.cfg.Network), slack.Settings{BotToken: token, AppToken: app,
		SigningSecret: signing, Proxy: p.Outbound(password),
		Owner: outbound.Owner{Connection: row.ID, PublicID: row.PublicID}, Route: s.cfg.SlackRoute})
}

// cachedSlackClient is the Slack client of the Connection id at version for delivery, built again when the version
// changed.
func (s *Service) cachedSlackClient(id, version int64, build func() (*slack.Client, error)) (*slack.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cc, ok := s.slackClients[id]; ok && cc.version == version {
		return cc.client, nil
	}
	c, err := build()
	if err != nil {
		return nil, err
	}
	s.slackClients[id] = cachedSlack{version: version, client: c}
	return c, nil
}

// SlackPressConnection is the Slack Connection publicID as the callback of its presses needs it (slack.Connections):
// its event mode, its client, kept until the Connection changes, and its Slack signing secret. A Slack Connection in
// socket mode, or in http mode without a stored secret, comes without its client and with an empty secret, which the
// callback refuses as it refuses a wrong signature, and records (C-23.FR-1); one that is unknown, deleted or not a
// Slack Connection is slack.ErrNoConnection.
func (s *Service) SlackPressConnection(ctx context.Context, publicID string) (slack.Conn, logging.Secret, error) {
	row, err := s.row(ctx, s.cfg.Store, publicID)
	if errors.Is(err, ErrNotFound) || err == nil && row.Type != TypeSlack {
		return slack.Conn{}, "", slack.ErrNoConnection
	}
	if err != nil {
		return slack.Conn{}, "", err
	}
	conn := slack.Conn{ID: row.ID, PublicID: row.PublicID, Mode: row.SlackEventMode.String}
	stored := storedSlackSigningSecret(row)
	if conn.Mode != EventModeHTTP || !stored.Set() {
		return conn, "", nil
	}
	secret, err := s.cfg.Keyring.OpenSecret(fieldSlackSigningSecret, stored)
	if err != nil {
		return slack.Conn{}, "", fmt.Errorf("open the slack signing secret of the connection %s: %w", row.PublicID, err)
	}
	if conn.Client, err = s.cachedSlackClient(row.ID, row.Version, func() (*slack.Client, error) {
		return s.slackClient(row)
	}); err != nil {
		return slack.Conn{}, "", err
	}
	return conn, secret, nil
}

// SocketConnections lists the Slack Connections in socket mode that are not deleted, for the Leader's sockets
// (slack.SocketSource): each with its client, kept until the Connection changes, whose requests — apps.connections.open
// among them — go to the Request log of the Connection, and its version. A Connection whose client cannot be built has
// the error instead of a client, and the sockets log it.
func (s *Service) SocketConnections(ctx context.Context) ([]slack.SocketConn, error) {
	rows, err := s.cfg.Store.ListSocketConnections(ctx, s.cfg.OrgID)
	if err != nil {
		return nil, fmt.Errorf("list the slack connections in socket mode: %w", err)
	}
	out := make([]slack.SocketConn, 0, len(rows))
	for _, r := range rows {
		row := dbgen.GetConnectionRow{ID: r.ID, PublicID: r.PublicID, Type: TypeSlack,
			BotTokenCiphertext: r.BotTokenCiphertext, BotTokenKeyID: r.BotTokenKeyID,
			BotTokenUpdatedAt: r.BotTokenUpdatedAt, SlackAppTokenCiphertext: r.SlackAppTokenCiphertext,
			SlackAppTokenKeyID: r.SlackAppTokenKeyID, SlackAppTokenUpdatedAt: r.SlackAppTokenUpdatedAt,
			SlackSigningSecretCiphertext: r.SlackSigningSecretCiphertext, Proxy: r.Proxy,
			SlackSigningSecretKeyID: r.SlackSigningSecretKeyID, Version: r.Version,
			SlackSigningSecretUpdatedAt: r.SlackSigningSecretUpdatedAt, ProxyPasswordKeyID: r.ProxyPasswordKeyID,
			ProxyPasswordCiphertext: r.ProxyPasswordCiphertext, ProxyPasswordUpdatedAt: r.ProxyPasswordUpdatedAt}
		c := slack.SocketConn{Conn: slack.Conn{ID: r.ID, PublicID: r.PublicID, Mode: EventModeSocket},
			Version: r.Version}
		c.Client, c.Err = s.cachedSlackClient(r.ID, r.Version, func() (*slack.Client, error) {
			return s.slackClient(row)
		})
		out = append(out, c)
	}
	return out, nil
}

// RecordSocketHello records what a hello of the Socket Mode connection of the Slack Connection c counted
// (slack.SocketSource): when it came, at the business clock's now, and other, the connections of its app beyond those
// the Leader holds — unless the Connection changed since c was read, left socket mode or was deleted. When it recorded
// them it sends the live hint, so that the Connection's page shows them. It is not a change of the configuration: the
// version, the ETag and the Audit log stay as they were.
func (s *Service) RecordSocketHello(ctx context.Context, c slack.SocketConn, other int) error {
	return s.cfg.Store.InTx(ctx, func(q Queries) error {
		n, err := q.RecordSocketHello(ctx, dbgen.RecordSocketHelloParams{OrgID: s.cfg.OrgID, ID: c.ID,
			Version: c.Version, HelloAt: s.cfg.Clocks.Business.Now(),
			OtherConnections: int32(min(max(other, 0), math.MaxInt32))})
		if err != nil {
			return fmt.Errorf("record the hello of the connection %s: %w", c.PublicID, err)
		}
		if n == 0 {
			return nil
		}
		return q.Notify(ctx, db.Hint{OrgID: s.cfg.OrgID, Type: Hint, ID: c.PublicID})
	})
}

// slackSocketOf is what the last hello of the Socket Mode connection of the Slack Connection row counted, nil for
// another type, in http mode and before the first hello after a save.
func slackSocketOf(r dbgen.GetConnectionRow) *SlackSocket {
	if r.Type != TypeSlack || r.SlackEventMode.String != EventModeSocket || !r.SlackSocketHelloAt.Valid {
		return nil
	}
	return &SlackSocket{HelloAt: r.SlackSocketHelloAt.Time.UTC(),
		OtherConnections: int(r.SlackSocketOtherConnections.Int32)}
}

// sharedApp reports whether the last hello of the Socket Mode connection of the Slack Connection row counted another
// connection of its Slack app than the Leader's.
func sharedApp(r dbgen.GetConnectionRow) bool {
	sock := slackSocketOf(r)
	return sock != nil && sock.OtherConnections > 0
}

// SlackTarget is where the Slack Destination destinationID posts, for the Slack delivery (slack.Targets): its channel
// id and its Connection with the Web API client, kept until the Connection changes, and the workspace address and the
// bot's name the Connection's last passing check stored, read every time. A deleted Destination still has one, for
// its final edit. A Destination that is unknown or not a Slack one, or whose Connection is deleted or not a Slack
// Connection, is slack.ErrNoTarget.
func (s *Service) SlackTarget(ctx context.Context, destinationID int64) (slack.Target, error) {
	r, err := s.cfg.Store.GetSlackDestinationTarget(ctx, dbgen.GetSlackDestinationTargetParams{OrgID: s.cfg.OrgID,
		DestinationID: destinationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return slack.Target{}, slack.ErrNoTarget
	}
	if err != nil {
		return slack.Target{}, fmt.Errorf("read the connection of the destination %d: %w", destinationID, err)
	}
	if r.Type != TypeSlack {
		return slack.Target{}, slack.ErrNoTarget
	}
	row := dbgen.GetConnectionRow{ID: r.ID, PublicID: r.PublicID, Type: r.Type, Name: r.Name,
		BotTokenCiphertext: r.BotTokenCiphertext, BotTokenKeyID: r.BotTokenKeyID, BotTokenUpdatedAt: r.BotTokenUpdatedAt,
		SlackAppTokenCiphertext: r.SlackAppTokenCiphertext, SlackAppTokenKeyID: r.SlackAppTokenKeyID,
		SlackAppTokenUpdatedAt: r.SlackAppTokenUpdatedAt, SlackSigningSecretCiphertext: r.SlackSigningSecretCiphertext,
		SlackSigningSecretKeyID: r.SlackSigningSecretKeyID, SlackSigningSecretUpdatedAt: r.SlackSigningSecretUpdatedAt,
		Proxy: r.Proxy, ProxyPasswordCiphertext: r.ProxyPasswordCiphertext, ProxyPasswordKeyID: r.ProxyPasswordKeyID,
		ProxyPasswordUpdatedAt: r.ProxyPasswordUpdatedAt, Version: r.Version}
	c, err := s.cachedSlackClient(r.ID, r.Version, func() (*slack.Client, error) { return s.slackClient(row) })
	if err != nil {
		return slack.Target{}, err
	}
	return slack.Target{Client: c, ConnectionID: r.ID, ConnectionPublicID: r.PublicID,
		ChannelID: r.SlackChannelID.String, TeamURL: r.SlackTeamUrl.String, BotName: r.BotUsername.String}, nil
}

// checkSlack runs the check of the Slack Connection row (C-24.FR-2) on the interactive path, limited by the Connection,
// with slack.RunCheck: token, scopes and, in socket mode, app_token. A passing check records what it found — the
// workspace, the bot and the app — unless the Connection changed since it was read, and announces it with the live
// hint; in socket mode it passes with WarningAnotherClientConnected while the last hello counted another connection of
// the app than the Leader's. It takes no baseURL.
func (s *Service) checkSlack(ctx context.Context, row dbgen.GetConnectionRow, baseURL *string) (CheckResult, error) {
	if baseURL != nil {
		return CheckResult{}, &FieldError{Pointer: "/base_url", Code: CodeUnsupported,
			Detail: "Only a Telegram Connection takes an unsaved base URL."}
	}
	c, err := s.slackClient(row)
	if err != nil {
		return CheckResult{}, err
	}
	id := row.ID
	res, err := slack.RunCheck(ctx, c, slack.Interactive(s.cfg.Interactive, delivery.Subject{Connection: &id}),
		s.cfg.Clocks.Real, row.SlackEventMode.String)
	if err != nil {
		return CheckResult{}, err
	}
	if res.OK() && res.Identity != nil {
		if err := s.storeSlackIdentity(ctx, row, *res.Identity); err != nil {
			return CheckResult{}, err
		}
	}
	out := slackResultOf(res)
	if out.OK && sharedApp(row) {
		out.Warnings = []string{WarningAnotherClientConnected}
	}
	return out, nil
}

// storeSlackIdentity records what a passing check of the Slack Connection row found, unless the Connection changed
// since the check read it, and sends its live hint when it did.
func (s *Service) storeSlackIdentity(ctx context.Context, row dbgen.GetConnectionRow, id slack.Identity) error {
	return s.cfg.Store.InTx(ctx, func(q Queries) error {
		n, err := q.SetSlackIdentity(ctx, dbgen.SetSlackIdentityParams{OrgID: s.cfg.OrgID, ID: row.ID,
			Version: row.Version, TeamID: nonEmpty(id.TeamID), TeamName: nonEmpty(id.TeamName),
			TeamUrl: nonEmpty(id.TeamURL), BotID: nonEmpty(id.BotID), BotUserID: nonEmpty(id.BotUserID),
			BotUsername: nonEmpty(id.BotName), AppID: nonEmpty(id.AppID)})
		if err != nil {
			return fmt.Errorf("record the workspace and the bot of the connection %s: %w", row.PublicID, err)
		}
		if n == 0 {
			return nil
		}
		return q.Notify(ctx, db.Hint{OrgID: s.cfg.OrgID, Type: Hint, ID: row.PublicID})
	})
}

// slackResultOf is the CheckResult of a check of a Slack Connection: its steps and, once the step token passed, the
// bot's name and the workspace, the bot's user id and the app the check found.
func slackResultOf(res slack.Check) CheckResult {
	out := CheckResult{OK: res.OK(), Steps: make([]Step, 0, len(res.Steps))}
	for _, st := range res.Steps {
		out.Steps = append(out.Steps, Step{Name: st.Name, OK: st.OK, Skipped: st.Skipped, Latency: st.Latency,
			Via: st.Via, Message: st.Message, Code: st.Code, MissingScopes: st.MissingScopes})
	}
	if id := res.Identity; id != nil {
		out.BotName = id.BotName
		out.Slack = &SlackIdentity{TeamID: id.TeamID, TeamName: id.TeamName, BotUserID: id.BotUserID, AppID: id.AppID}
	}
	return out
}

// slackSecretsCreated are the Audit log changes of the app-level token and the Slack signing secret of the new
// Connection c, for those it has.
func slackSecretsCreated(c Connection) []audit.Change {
	var diff []audit.Change
	if c.AppToken.Set {
		diff = append(diff, audit.Change{Pointer: "/app_token", SecretChanged: true})
	}
	if c.SlackSigningSecret.Set {
		diff = append(diff, audit.Change{Pointer: "/slack_signing_secret", SecretChanged: true})
	}
	return diff
}

// CheckSlackChannel runs the Destination check of a Slack Destination being saved or checked, for the Destination
// write path (C-24.FR-3): conversations.info through the Slack Connection that the check names, on the interactive
// path, limited by the Destination when it exists and by the Connection otherwise, with a client built for the check.
// The text of bot_not_in_channel names the bot by the name the Connection's last passing check stored, and by none
// before one. A Connection that is missing, deleted or not a Slack Connection is destinations.ErrUnknownConnection.
func (s *Service) CheckSlackChannel(ctx context.Context, in destinations.SlackChannelCheck) (
	destinations.SlackChannelChecked, error) {
	row, err := s.row(ctx, s.cfg.Store, in.Connection)
	if errors.Is(err, ErrNotFound) || (err == nil && row.Type != TypeSlack) {
		return destinations.SlackChannelChecked{}, destinations.ErrUnknownConnection
	}
	if err != nil {
		return destinations.SlackChannelChecked{}, err
	}
	c, err := s.slackClient(row)
	if err != nil {
		return destinations.SlackChannelChecked{}, err
	}
	conn := row.ID
	subject := delivery.Subject{Connection: &conn}
	if in.Destination != nil {
		subject = delivery.Subject{Destination: &delivery.Destination{ID: *in.Destination, Type: TypeSlack,
			Connection: &conn}}
	}
	res, err := slack.CheckChannel(ctx, c, slack.Interactive(s.cfg.Interactive, subject), in.ChannelID,
		row.BotUsername.String)
	if err != nil {
		return destinations.SlackChannelChecked{}, err
	}
	return destinations.SlackChannelChecked{ConnectionID: row.ID, Check: res}, nil
}

// SlackChannels lists the channels of the Slack Connection publicID for the channel picker (C-24.FR-3): those the bot
// is a member of, public and private, not archived, whose name contains q, read with conversations.list on the
// interactive path, limited by the Connection, at most five pages. A Slack 429 on a page, which holds the Connection's
// limiter, is a *delivery.LimitedError with its Retry-After, as no token within the budget is; a page that failed
// otherwise is a *MessengerError that names Slack. A deleted or unknown Connection is ErrNotFound; one of another type
// is errNotSlack.
func (s *Service) SlackChannels(ctx context.Context, publicID, q string) (slack.ChannelList, error) {
	row, err := s.row(ctx, s.cfg.Store, publicID)
	if err != nil {
		return slack.ChannelList{}, err
	}
	if row.Type != TypeSlack {
		return slack.ChannelList{}, errNotSlack
	}
	c, err := s.slackClient(row)
	if err != nil {
		return slack.ChannelList{}, err
	}
	id := row.ID
	list, err := slack.Channels(ctx, c, slack.Interactive(s.cfg.Interactive, delivery.Subject{Connection: &id}), q)
	if rl, ok := errors.AsType[*slack.RateLimitedError](err); ok {
		return slack.ChannelList{}, &delivery.LimitedError{RetryAfter: rl.RetryAfter}
	}
	if lf, ok := errors.AsType[*slack.ListFailedError](err); ok {
		return slack.ChannelList{}, &MessengerError{Messenger: "Slack", Text: string(lf.Text)}
	}
	if err != nil {
		return slack.ChannelList{}, err
	}
	return list, nil
}

// doctorSlack runs the steps of the check of the Slack Connection row in the background class, bounded by each, at
// once, waiting for no limiter token: token, scopes and, in socket mode, app_token. It returns the client and the bot's
// name the step token found, with an empty message when every step passed, otherwise the failing step with its message,
// and, when it passed in socket mode while the last hello counted another connection of the app than the Leader's, the
// hint slack.HintAnotherClientConnected. It stores nothing it finds.
func (s *Service) doctorSlack(ctx context.Context, row dbgen.GetConnectionRow, each time.Duration) (*slack.Client,
	string, string, string) {
	if s.cfg.Keyring == nil {
		return nil, "", errNoKeyring.Error(), ""
	}
	c, err := s.slackClient(row)
	if err != nil {
		return nil, "", err.Error(), ""
	}
	ctx, cancel := context.WithTimeout(ctx, each)
	defer cancel()
	// The direct runner has no error of its own.
	res, _ := slack.RunCheck(ctx, c, slack.Direct(delivery.Call{Class: outbound.ClassBackground}), s.cfg.Clocks.Real,
		row.SlackEventMode.String)
	if st, failed := res.Failed(); failed {
		return nil, "", st.Name + ": " + cmp.Or(st.Message, checkFailed), ""
	}
	bot, hint := "", ""
	if res.Identity != nil {
		bot = res.Identity.BotName
	}
	if sharedApp(row) {
		hint = slack.HintAnotherClientConnected
	}
	return c, bot, "", hint
}

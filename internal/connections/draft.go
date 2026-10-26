// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package connections

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/muster-io/muster/internal/bridge"
	"github.com/muster-io/muster/internal/connections/dbgen"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mattermost"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/proxyconf"
	"github.com/muster-io/muster/internal/slack"
	"github.com/muster-io/muster/internal/telegram"
)

// Draft is a Connection as its form holds it before it is saved (C-13.FR-14): its type and address — the server URL
// of a Mattermost one, the Bot API base URL and update mode of a Telegram one, the base URL and event delivery mode of
// a Bridge, the event mode of a Slack one, whose address is Slack's —, the bot token typed in the form, Keep when none
// is, and for Slack the app-level token, its proxy, nil for no proxy on a new Connection and for the saved one on a
// saved Connection, and ConnectionID, the public_id of the saved Connection the form edits, nil for a new one.
type Draft struct {
	Type          string
	ServerURL     string
	BotAPIBaseURL string
	UpdateMode    string
	BridgeBaseURL string
	EventDelivery string
	EventMode     string
	BotToken      keyring.SecretInput
	AppToken      keyring.SecretInput
	Proxy         *proxyconf.Input
	ConnectionID  *string
}

// draftRoute is where and how the check of a draft calls: its address as a save normalizes it, its proxy and the
// proxy's password, the bot token, empty when only the dry probe may run, what limits its requests and whose Request
// log they go to; for Slack the app-level token, empty when the step app_token is skipped, and slackBase, where muster
// dev sends the requests instead of Slack, empty for Slack; for Mattermost, Telegram and Bridge, routeToken, the token
// whose route of muster dev the requests take (devRouteToken).
type draftRoute struct {
	in         Input
	proxy      proxyconf.Config
	password   logging.Secret
	token      logging.Secret
	appToken   logging.Secret
	slackBase  string
	routeToken logging.Secret
	subject    delivery.Subject
	owner      outbound.Owner
}

// CheckDraft runs the check of a draft Connection d on the interactive path without saving it (C-13.FR-14), with the
// steps and the codes of Check. It validates d as a save would — the address of its type, the connection_id, the proxy
// — and then decides the token: a bot token typed in d goes to the address of d; without one, the stored token of the
// Connection d edits is used only when d's address, normalized as a save normalizes it, and d's proxy route are the
// saved ones; otherwise only the dry probe of the type runs and the other steps are skipped. The address of a Slack
// draft is Slack's, so only its proxy can differ; its app-level token follows slackDraftRoute. A stored proxy password
// is used only with the saved route and no password in d. The clients are built for the check and never kept; their
// requests, the dry probe's included, are recorded as draft_check under the Connection the draft names, or without an
// owner, under one check run that the result names (C-23.FR-1, FR-5). A draft of a saved Connection takes its limiter
// tokens from that Connection, one of a new Connection takes none; no limiter token within the budget is a
// *delivery.LimitedError. It stores nothing: no bot, no info, no Audit log entry, no hint and no log event.
func (s *Service) CheckDraft(ctx context.Context, d Draft) (CheckResult, error) {
	r, err := s.draftRoute(ctx, d)
	if err != nil {
		return CheckResult{}, err
	}
	ctx, run := outbound.StartCheck(ctx, outbound.CheckDraft, 0)
	var res CheckResult
	switch r.in.Type {
	case TypeTelegram:
		res, err = s.checkTelegramDraft(ctx, r)
	case TypeBridge:
		res, err = s.checkBridgeDraft(ctx, r)
	case TypeSlack:
		res, err = s.checkSlackDraft(ctx, r)
	default:
		res, err = s.checkMattermostDraft(ctx, r)
	}
	if err != nil {
		return CheckResult{}, err
	}
	res.Run = run
	return res, nil
}

// draftRoute validates d in the order of a save — the type and the address, the bot token and for Slack the app-level
// token, the connection_id, the proxy and its password — and applies the token rule of C-13.FR-14; a secret is opened
// only when the rule uses it.
func (s *Service) draftRoute(ctx context.Context, d Draft) (draftRoute, error) {
	in := Input{Type: d.Type, ServerURL: d.ServerURL, BotAPIBaseURL: d.BotAPIBaseURL, UpdateMode: d.UpdateMode,
		BridgeBaseURL: d.BridgeBaseURL, EventDelivery: d.EventDelivery, EventMode: d.EventMode}
	if in.Type != TypeMattermost && in.Type != TypeTelegram && in.Type != TypeBridge && in.Type != TypeSlack {
		return draftRoute{}, &FieldError{Pointer: "/type", Code: CodeInvalidFormat,
			Detail: "The type is not mattermost, telegram, bridge or slack."}
	}
	if err := checkAddress(&in); err != nil {
		return draftRoute{}, err
	}
	switch {
	case d.BotToken.Null:
		return draftRoute{}, &FieldError{Pointer: "/bot_token", Code: CodeRequired,
			Detail: "The bot token cannot be null; omit it to use the stored one."}
	case d.BotToken.Given && d.BotToken.Value == "":
		return draftRoute{}, &FieldError{Pointer: "/bot_token", Code: CodeTooShort, Detail: "The bot token is empty."}
	}
	if in.Type == TypeSlack {
		if err := checkSlackDraftTokens(d); err != nil {
			return draftRoute{}, err
		}
	}
	var row *dbgen.GetConnectionRow
	var saved proxyconf.Config
	if d.ConnectionID != nil {
		r, err := s.row(ctx, s.cfg.Store, *d.ConnectionID)
		if errors.Is(err, ErrNotFound) || err == nil && r.Type != in.Type {
			return draftRoute{}, &FieldError{Pointer: "/connection_id", Code: CodeUnknownID,
				Detail: "No Connection of this type has this id."}
		}
		if err != nil {
			return draftRoute{}, err
		}
		if saved, err = proxyconf.Parse(r.Proxy); err != nil {
			return draftRoute{}, err
		}
		row = &r
	}
	out := draftRoute{in: in, proxy: saved, subject: delivery.Subject{NoLimiter: true},
		owner: outbound.Owner{Draft: true}}
	password := keyring.Keep
	if d.Proxy != nil {
		p, err := proxy(*d.Proxy, saved)
		if err != nil {
			return draftRoute{}, err
		}
		out.proxy, password = p, d.Proxy.Password
	}
	same := row != nil && sameRoute(out.proxy, saved)
	var err error
	switch {
	case password.Null:
	case password.Given && password.Value == "":
		return draftRoute{}, &FieldError{Pointer: "/proxy/password", Code: CodeTooShort,
			Detail: "The proxy password is empty; send null to clear it."}
	case password.Given:
		out.password = password.Value
	case same && out.proxy.Enabled:
		if out.password, err = s.cfg.Keyring.OpenSecret(fieldProxyPassword, storedPassword(*row)); err != nil {
			return draftRoute{}, fmt.Errorf("open the proxy password of the connection %s: %w", row.PublicID, err)
		}
	}
	switch {
	case d.BotToken.Given:
		out.token = d.BotToken.Value
	case same && addressOf(in) == savedAddress(*row):
		if out.token, err = s.cfg.Keyring.OpenSecret(fieldBotToken, storedToken(*row)); err != nil {
			return draftRoute{}, fmt.Errorf("open the bot token of the connection %s: %w", row.PublicID, err)
		}
	}
	if in.Type == TypeSlack {
		if err := s.slackDraftRoute(d, row, same, &out); err != nil {
			return draftRoute{}, err
		}
	}
	out.routeToken = s.devRouteToken(in.Type, row, out.token)
	if row != nil {
		id := row.ID
		out.subject = delivery.Subject{Connection: &id}
		out.owner = outbound.Owner{Connection: row.ID, PublicID: row.PublicID}
	}
	return out, nil
}

// checkSlackDraftTokens validates the tokens typed in the Slack draft d as a save does (C-24.FR-1): the bot token
// starts with xoxb- and the app-level token, which is not null and not empty, with xapp-. A draft may leave out either
// token, so the mode's secret is not required.
func checkSlackDraftTokens(d Draft) error {
	switch {
	case d.AppToken.Null:
		return &FieldError{Pointer: "/app_token", Code: CodeRequired,
			Detail: "The app-level token cannot be null; omit it to use the stored one."}
	case d.AppToken.Given && d.AppToken.Value == "":
		return &FieldError{Pointer: "/app_token", Code: CodeTooShort, Detail: "The app-level token is empty."}
	}
	// Without an event mode checkSlackSecrets checks the prefixes alone.
	return checkSlackSecrets(Input{BotToken: d.BotToken, AppToken: d.AppToken}, nil)
}

// slackDraftRoute decides the app-level token of the Slack draft d and where muster dev sends its requests, once the
// bot token is decided (C-13.FR-14, C-24.FR-2). The bot token decides: without one only the dry probe runs, and no
// app-level token goes anywhere. With one, an app-level token typed in d is used; without one, in socket mode, the
// stored app-level token of the Connection row is used only on the saved proxy route (same) and, under muster dev,
// only when the bot token is routed where the stored one is; otherwise the step app_token is skipped. Under muster dev
// the route of the bot token that is sent decides slackBase, and for the dry probe of a saved Connection the route of
// its stored bot token, which is opened for that alone and never sent; the dry probe of a new Connection goes to Slack.
func (s *Service) slackDraftRoute(d Draft, row *dbgen.GetConnectionRow, same bool, out *draftRoute) error {
	savedBase := func() (string, error) {
		if row == nil || s.cfg.SlackRoute == nil {
			return "", nil
		}
		stored, err := s.cfg.Keyring.OpenSecret(fieldBotToken, storedToken(*row))
		if err != nil {
			return "", fmt.Errorf("open the bot token of the connection %s: %w", row.PublicID, err)
		}
		return s.cfg.SlackRoute(stored), nil
	}
	if out.token == "" {
		base, err := savedBase()
		out.slackBase = base
		return err
	}
	if s.cfg.SlackRoute != nil {
		out.slackBase = s.cfg.SlackRoute(out.token)
	}
	switch {
	case d.AppToken.Given:
		out.appToken = d.AppToken.Value
	case same && out.in.EventMode == EventModeSocket && storedSlackAppToken(*row).Set():
		// Without a typed bot token the stored one is sent, which is routed where it is stored.
		if d.BotToken.Given {
			base, err := savedBase()
			if err != nil || base != out.slackBase {
				return err
			}
		}
		app, err := s.cfg.Keyring.OpenSecret(fieldSlackAppToken, storedSlackAppToken(*row))
		if err != nil {
			return fmt.Errorf("open the app-level token of the connection %s: %w", row.PublicID, err)
		}
		out.appToken = app
	}
	return nil
}

// devRouteToken is the token by which muster dev routes the check of a Mattermost, Telegram or Bridge draft, by the
// same care as slackDraftRoute: the token that is sent, so that a token routes only where it goes; without one, the
// stored token of the saved Connection row, opened for the route of its dry probe alone and never sent, and none when it
// cannot be opened, as the dry probe needs no token; none for a new Connection, whose dry probe goes to the address in
// the form. Outside muster dev it is empty.
func (s *Service) devRouteToken(typ string, row *dbgen.GetConnectionRow, token logging.Secret) logging.Secret {
	routed := typ == TypeMattermost && s.cfg.MattermostRoute != nil || typ == TypeTelegram && s.cfg.TelegramRoute != nil ||
		typ == TypeBridge && s.cfg.BridgeRoute != nil
	switch {
	case !routed:
		return ""
	case token != "" || row == nil:
		return token
	}
	stored, err := s.cfg.Keyring.OpenSecret(fieldBotToken, storedToken(*row))
	if err != nil {
		return ""
	}
	return stored
}

// sameRoute reports whether the proxies a and b send a request the same way: both disabled, whatever type and address
// they remember, or both enabled with the same type and address.
func sameRoute(a, b proxyconf.Config) bool {
	if !a.Enabled || !b.Enabled {
		return a.Enabled == b.Enabled
	}
	return a.Type == b.Type && a.Address == b.Address
}

// addressOf is where the Connection of in calls: its server URL, Bot API base URL or base URL; Slack's, which is
// fixed, for a Slack Connection.
func addressOf(in Input) string {
	switch in.Type {
	case TypeTelegram:
		return in.BotAPIBaseURL
	case TypeBridge:
		return in.BridgeBaseURL
	case TypeSlack:
		return slack.BaseURL
	}
	return in.ServerURL
}

// savedAddress is where the stored Connection r calls, as its save normalized it.
func savedAddress(r dbgen.GetConnectionRow) string {
	switch r.Type {
	case TypeTelegram:
		return r.TelegramBotApiBaseUrl.String
	case TypeBridge:
		return r.BridgeBaseUrl.String
	case TypeSlack:
		return slack.BaseURL
	}
	return r.MattermostServerUrl.String
}

// draftRunner makes each request of the check of a draft on the interactive path, limited by subject. An answered
// RetryAfter reaches the path as Transient, so that a draft never holds a limiter: an address that is not saved must
// not pause the deliveries of the Connection the draft takes its tokens from. The step keeps its failure.
func draftRunner(in Interactive, subject delivery.Subject) func(ctx context.Context,
	f func(ctx context.Context, c delivery.Call) delivery.Outcome) (delivery.Outcome, error) {
	return func(ctx context.Context, f func(ctx context.Context, c delivery.Call) delivery.Outcome) (delivery.Outcome,
		error) {
		return in.Do(ctx, subject, delivery.ReadOp(func(ctx context.Context, c delivery.Call) delivery.Outcome {
			o := f(ctx, c)
			if o.Kind == delivery.OutcomeRetryAfter {
				o.Kind, o.Scope, o.RetryAfter = delivery.OutcomeTransient, "", 0
			}
			return o
		}))
	}
}

// checkMattermostDraft checks a Mattermost draft (C-13.FR-2): with a token the step token and the read of the bot's
// roles, as Check makes them; without one the dry probe, and the step token skipped.
func (s *Service) checkMattermostDraft(ctx context.Context, r draftRoute) (CheckResult, error) {
	var route mattermost.Route
	if s.cfg.MattermostRoute != nil {
		route = func(serverURL string, _ logging.Secret) string { return s.cfg.MattermostRoute(serverURL, r.routeToken) }
	}
	c, err := mattermost.NewClient(s.cfg.Network, mattermost.Settings{ServerURL: r.in.ServerURL, Token: r.token,
		Proxy: r.proxy.Outbound(r.password), Owner: r.owner, Route: route})
	if err != nil {
		return CheckResult{}, err
	}
	run := draftRunner(s.cfg.Interactive, r.subject)
	if r.token == "" {
		p, err := mattermost.RunDryProbe(ctx, c, run, s.cfg.Clocks.Real)
		if err != nil {
			return CheckResult{}, err
		}
		return CheckResult{OK: p.OK, Steps: []Step{{Name: mattermost.StepDryProbe, OK: p.OK, Latency: p.Latency,
			Via: c.Via(), Message: p.Message, Code: p.Code}, {Name: StepToken, Skipped: true, Via: c.Via()}}}, nil
	}
	step, user, err := s.mattermostToken(ctx, c, run)
	if err != nil {
		return CheckResult{}, err
	}
	if !step.OK {
		return CheckResult{Steps: []Step{step}}, nil
	}
	warnings, roles := pressWarnings(ctx, c, run, user)
	return CheckResult{OK: true, Steps: []Step{step}, BotName: user.Username, Warnings: warnings, BotRoles: roles}, nil
}

// checkTelegramDraft checks a Telegram draft (C-14.FR-11) with telegram.RunCheck: the dry probe, getMe and
// getWebhookInfo with a token, the dry probe alone without one.
func (s *Service) checkTelegramDraft(ctx context.Context, r draftRoute) (CheckResult, error) {
	var route telegram.Route
	if s.cfg.TelegramRoute != nil {
		route = func(baseURL string, _ logging.Secret) string { return s.cfg.TelegramRoute(baseURL, r.routeToken) }
	}
	c, err := telegram.NewClient(telegram.Network(s.cfg.Network), telegram.Settings{BaseURL: r.in.BotAPIBaseURL,
		Token: r.token, Proxy: r.proxy.Outbound(r.password), Owner: r.owner, Route: route})
	if err != nil {
		return CheckResult{}, err
	}
	saved, probe := c, (*telegram.Client)(nil)
	if r.token == "" {
		saved, probe = nil, c
	}
	res, err := telegram.RunCheck(ctx, saved, probe, draftRunner(s.cfg.Interactive, r.subject), s.cfg.Clocks.Real)
	if err != nil {
		return CheckResult{}, err
	}
	return checkResultOf(res), nil
}

// checkBridgeDraft checks a Bridge draft (C-22.FR-2): with a token the steps info, whose info is returned and neither
// stored nor kept, and event_delivery; without one the dry probe, and both steps skipped.
func (s *Service) checkBridgeDraft(ctx context.Context, r draftRoute) (CheckResult, error) {
	var route bridge.Route
	if s.cfg.BridgeRoute != nil {
		route = func(baseURL string, _ logging.Secret) string { return s.cfg.BridgeRoute(baseURL, r.routeToken) }
	}
	c, err := bridge.NewClient(bridge.Network(s.cfg.Network), bridge.Settings{BaseURL: r.in.BridgeBaseURL,
		Token: r.token, Proxy: r.proxy.Outbound(r.password), Owner: r.owner, Route: route})
	if err != nil {
		return CheckResult{}, err
	}
	run := draftRunner(s.cfg.Interactive, r.subject)
	if r.token == "" {
		p, err := bridge.RunDryProbe(ctx, c, run, s.cfg.Clocks.Real)
		if err != nil {
			return CheckResult{}, err
		}
		return CheckResult{OK: p.OK, Bridge: true, Steps: []Step{{Name: bridge.StepDryProbe, OK: p.OK,
			Latency: p.Latency, Via: c.Via(), Message: p.Message, Code: p.Code},
			{Name: StepInfo, Skipped: true, Via: c.Via()}, {Name: StepEventDelivery, Skipped: true, Via: c.Via()}}}, nil
	}
	var info bridge.Info
	var res bridge.Result
	var latency time.Duration
	if _, err := run(ctx, func(ctx context.Context, call delivery.Call) delivery.Outcome {
		start := s.cfg.Clocks.Real.Now()
		info, res = c.GetInfo(ctx, call.Class)
		latency = s.cfg.Clocks.Real.Now().Sub(start)
		return res.Outcome()
	}); err != nil {
		return CheckResult{}, err
	}
	return bridgeResult(info, res, latency, c.Via(), r.in.EventDelivery), nil
}

// checkSlackDraft checks a Slack draft (C-24.FR-2) with a client built for the check, whose requests go to Slack, or
// where muster dev routes r.slackBase: with a bot token the steps of slack.RunCheck in the draft's event mode — token,
// scopes and, in socket mode, app_token, skipped without an app-level token —, whose findings are returned and never
// stored; without one the dry probe, and the other steps skipped.
func (s *Service) checkSlackDraft(ctx context.Context, r draftRoute) (CheckResult, error) {
	base := r.slackBase
	c, err := slack.NewClient(slack.Network(s.cfg.Network), slack.Settings{BotToken: r.token, AppToken: r.appToken,
		Proxy: r.proxy.Outbound(r.password), Owner: r.owner, Route: func(logging.Secret) string { return base }})
	if err != nil {
		return CheckResult{}, err
	}
	run := draftRunner(s.cfg.Interactive, r.subject)
	socket := r.in.EventMode == EventModeSocket
	if r.token == "" {
		p, err := slack.RunDryProbe(ctx, c, run, s.cfg.Clocks.Real)
		if err != nil {
			return CheckResult{}, err
		}
		steps := []Step{{Name: slack.StepDryProbe, OK: p.OK, Latency: p.Latency, Via: c.Via(), Message: p.Message,
			Code: p.Code}, {Name: StepToken, Skipped: true, Via: c.Via()}, {Name: StepScopes, Skipped: true,
			Via: c.Via()}}
		if socket {
			steps = append(steps, Step{Name: StepAppToken, Skipped: true, Via: c.Via()})
		}
		return CheckResult{OK: p.OK, Steps: steps}, nil
	}
	mode := r.in.EventMode
	if socket && r.appToken == "" {
		mode = EventModeHTTP
	}
	res, err := slack.RunCheck(ctx, c, run, s.cfg.Clocks.Real, mode)
	if err != nil {
		return CheckResult{}, err
	}
	if socket && r.appToken == "" {
		res.Steps = append(res.Steps, slack.Step{Name: StepAppToken, Skipped: true, Via: c.Via()})
	}
	return slackResultOf(res), nil
}

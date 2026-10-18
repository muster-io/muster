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
	"github.com/muster-io/muster/internal/proxyconf"
	"github.com/muster-io/muster/internal/telegram"
)

// CodeUnknownID is a connection_id of a draft that names no Connection of the draft's type.
const CodeUnknownID = "unknown_id"

// Draft is a Connection as its form holds it before it is saved (C-13.FR-14): its type and address — the server URL
// of a Mattermost one, the Bot API base URL and update mode of a Telegram one, the base URL and event delivery mode of
// a Bridge —, the bot token typed in the form, Keep when none is, its proxy, nil for no proxy on a new Connection and
// for the saved one on a saved Connection, and ConnectionID, the public_id of the saved Connection the form edits, nil
// for a new one.
type Draft struct {
	Type          string
	ServerURL     string
	BotAPIBaseURL string
	UpdateMode    string
	BridgeBaseURL string
	EventDelivery string
	BotToken      keyring.SecretInput
	Proxy         *proxyconf.Input
	ConnectionID  *string
}

// draftRoute is where and how the check of a draft calls: its address as a save normalizes it, its proxy and the
// proxy's password, the bot token, empty when only the dry probe may run, and what limits its requests.
type draftRoute struct {
	in       Input
	proxy    proxyconf.Config
	password logging.Secret
	token    logging.Secret
	subject  delivery.Subject
}

// CheckDraft runs the check of a draft Connection d on the interactive path without saving it (C-13.FR-14), with the
// steps and the codes of Check. It validates d as a save would — the address of its type, the connection_id, the
// proxy — and then decides the token: a bot token typed in d goes to the address of d; without one, the stored token
// of the Connection d edits is used only when d's address, normalized as a save normalizes it, and d's proxy route
// are the saved ones; otherwise only the dry probe of the type runs and the other steps are skipped. A stored proxy
// password is used only with the saved route and no password in d. The clients are built for the check and never
// kept. A draft of a saved Connection takes its limiter tokens from that Connection, one of a new Connection takes
// none; no limiter token within the budget is a *delivery.LimitedError. It stores nothing: no bot, no info, no Audit
// log entry, no hint and no log event.
func (s *Service) CheckDraft(ctx context.Context, d Draft) (CheckResult, error) {
	r, err := s.draftRoute(ctx, d)
	if err != nil {
		return CheckResult{}, err
	}
	switch r.in.Type {
	case TypeTelegram:
		return s.checkTelegramDraft(ctx, r)
	case TypeBridge:
		return s.checkBridgeDraft(ctx, r)
	}
	return s.checkMattermostDraft(ctx, r)
}

// draftRoute validates d in the order of a save — the type and the address, the bot token, the connection_id, the
// proxy and its password — and applies the token rule of C-13.FR-14; a secret is opened only when the rule uses it.
func (s *Service) draftRoute(ctx context.Context, d Draft) (draftRoute, error) {
	in := Input{Type: d.Type, ServerURL: d.ServerURL, BotAPIBaseURL: d.BotAPIBaseURL, UpdateMode: d.UpdateMode,
		BridgeBaseURL: d.BridgeBaseURL, EventDelivery: d.EventDelivery}
	if in.Type != TypeMattermost && in.Type != TypeTelegram && in.Type != TypeBridge {
		return draftRoute{}, &FieldError{Pointer: "/type", Code: CodeInvalidFormat,
			Detail: "The type is not mattermost, telegram or bridge."}
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
	out := draftRoute{in: in, proxy: saved, subject: delivery.Subject{NoLimiter: true}}
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
	if row != nil {
		id := row.ID
		out.subject = delivery.Subject{Connection: &id}
	}
	return out, nil
}

// sameRoute reports whether the proxies a and b send a request the same way: both disabled, whatever type and address
// they remember, or both enabled with the same type and address.
func sameRoute(a, b proxyconf.Config) bool {
	if !a.Enabled || !b.Enabled {
		return a.Enabled == b.Enabled
	}
	return a.Type == b.Type && a.Address == b.Address
}

// addressOf is where the Connection of in calls: its server URL, Bot API base URL or base URL.
func addressOf(in Input) string {
	switch in.Type {
	case TypeTelegram:
		return in.BotAPIBaseURL
	case TypeBridge:
		return in.BridgeBaseURL
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
	c, err := mattermost.NewClient(s.cfg.Network, mattermost.Settings{ServerURL: r.in.ServerURL, Token: r.token,
		Proxy: r.proxy.Outbound(r.password)})
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
	return CheckResult{OK: true, Steps: []Step{step}, BotName: user.Username,
		Warnings: pressWarnings(ctx, c, run, user)}, nil
}

// checkTelegramDraft checks a Telegram draft (C-14.FR-11) with telegram.RunCheck: the dry probe, getMe and
// getWebhookInfo with a token, the dry probe alone without one.
func (s *Service) checkTelegramDraft(ctx context.Context, r draftRoute) (CheckResult, error) {
	c, err := telegram.NewClient(telegram.Network(s.cfg.Network), telegram.Settings{BaseURL: r.in.BotAPIBaseURL,
		Token: r.token, Proxy: r.proxy.Outbound(r.password)})
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
	c, err := bridge.NewClient(bridge.Network(s.cfg.Network), bridge.Settings{BaseURL: r.in.BridgeBaseURL,
		Token: r.token, Proxy: r.proxy.Outbound(r.password)})
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

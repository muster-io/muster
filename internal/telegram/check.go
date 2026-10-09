// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package telegram

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/outbound"
)

// The steps of the connection check of a Telegram Connection (C-14.FR-11).
const (
	StepDryProbe       = "dry_probe"
	StepGetMe          = "get_me"
	StepGetWebhookInfo = "get_webhook_info"
)

// The messages of the steps that failed: what answered the dry probe, and a token getMe refused.
const (
	MessageNotBotAPI      = "This is not a Bot API."
	MessageWrongPrefix    = "Wrong path prefix: the server answered 404."
	MessageProxyRefused   = "The proxy refused the connection (407)"
	MessageTokenInvalid   = "The bot token is not valid." //nolint:gosec // G101: a message, not a credential
	messageDNS            = "DNS lookup failed: %s"
	messageTLS            = "TLS handshake failed: %s"
	messageTimeout        = "No answer within %d s"
	messageOtherStatus    = "The server answered %d instead of 401 with JSON."
	messageWebhookSet     = "A webhook is set at %s."
	messageWebhookUnknown = "A webhook is set."
)

// Step is one step of a check: whether it passed or was skipped, its latency and path, and what answered when it
// failed, or the host of a webhook that is set.
type Step struct {
	Name    string
	OK      bool
	Skipped bool
	Latency time.Duration
	Via     string
	Message string
}

// Check is the result of a connection check: its three steps, the bot once getMe passed and the webhook once
// getWebhookInfo passed.
type Check struct {
	Steps   []Step
	Bot     *User
	Webhook *WebhookInfo
}

// OK reports whether no step failed; skipped steps do not fail a check.
func (c Check) OK() bool {
	for _, s := range c.Steps {
		if !s.OK && !s.Skipped {
			return false
		}
	}
	return len(c.Steps) > 0
}

// Failed is the first step that failed, if any.
func (c Check) Failed() (Step, bool) {
	for _, s := range c.Steps {
		if !s.OK && !s.Skipped {
			return s, true
		}
	}
	return Step{}, false
}

// Runner makes one request of a check with the Call it is made in: on the interactive path when a person waits for it,
// at once for muster doctor. Its error is the runner's own — no limiter token within the budget — and ends the check.
type Runner func(ctx context.Context, f func(ctx context.Context, c delivery.Call) delivery.Outcome) (delivery.Outcome,
	error)

// Direct is the Runner that calls at once with c: muster doctor, which waits for no token.
func Direct(c delivery.Call) Runner {
	return func(ctx context.Context, f func(ctx context.Context, c delivery.Call) delivery.Outcome) (delivery.Outcome,
		error) {
		return f(ctx, c), nil
	}
}

// Path is the interactive path, declared by its consumer; *delivery.Interactive implements it.
type Path interface {
	Do(ctx context.Context, s delivery.Subject, op delivery.Op) (delivery.Outcome, error)
}

// Interactive is the Runner of a check a person waits for: each request through the interactive path, limited by s.
func Interactive(in Path, s delivery.Subject) Runner {
	return func(ctx context.Context, f func(ctx context.Context, c delivery.Call) delivery.Outcome) (delivery.Outcome,
		error) {
		return in.Do(ctx, s, delivery.ReadOp(f))
	}
}

// RunCheck runs the connection check (C-14.FR-11) through run, timing each step on the real clock:
//
//  1. dry_probe — `GET <base>/bot0:x/getMe` without the token, which passes on a 401 answer with a JSON body and
//     otherwise names what answered;
//  2. get_me — with the token: the bot;
//  3. get_webhook_info — whether a webhook is set, with its host in the message, and the pending updates.
//
// With probe set — the client of an unsaved base URL, which has no token — only the dry probe runs, against it, and
// steps 2 and 3 are skipped: the token never reaches another address than the saved base URL. A dry probe that fails
// skips steps 2 and 3 too, so that the token never goes to a server that is not a Bot API; a getMe that fails skips
// step 3.
func RunCheck(ctx context.Context, saved, probe *Client, run Runner, realClock clock.Clock) (Check, error) {
	target := saved
	if probe != nil {
		target = probe
	}
	var out Check
	skipRest := func(names ...string) {
		for _, n := range names {
			out.Steps = append(out.Steps, Step{Name: n, Skipped: true, Via: saved.Via()})
		}
	}
	var r Result
	latency, err := timed(ctx, run, realClock, func(ctx context.Context, c delivery.Call) Result {
		r = target.DryProbe(ctx, c.Class)
		return r
	})
	if err != nil {
		return Check{}, err
	}
	dry := Step{Name: StepDryProbe, Latency: latency, Via: target.Via()}
	dry.OK, dry.Message = probeVerdict(r, target.settings.BaseURL)
	out.Steps = append(out.Steps, dry)
	if probe != nil || !dry.OK {
		skipRest(StepGetMe, StepGetWebhookInfo)
		return out, nil
	}
	var u User
	if latency, err = timed(ctx, run, realClock, func(ctx context.Context, c delivery.Call) Result {
		u, r = saved.GetMe(ctx, c.Class)
		return r
	}); err != nil {
		return Check{}, err
	}
	me := Step{Name: StepGetMe, OK: r.OK(), Latency: latency, Via: saved.Via()}
	if !me.OK {
		me.Message = string(r.Outcome.Error)
		if r.Status == http.StatusUnauthorized || r.Code == http.StatusUnauthorized {
			me.Message = MessageTokenInvalid
		}
		out.Steps = append(out.Steps, me)
		skipRest(StepGetWebhookInfo)
		return out, nil
	}
	out.Steps, out.Bot = append(out.Steps, me), &u
	var info WebhookInfo
	if latency, err = timed(ctx, run, realClock, func(ctx context.Context, c delivery.Call) Result {
		info, r = saved.GetWebhookInfo(ctx, c.Class)
		return r
	}); err != nil {
		return Check{}, err
	}
	hook := Step{Name: StepGetWebhookInfo, OK: r.OK(), Latency: latency, Via: saved.Via()}
	switch {
	case !hook.OK:
		hook.Message = string(r.Outcome.Error)
	default:
		out.Webhook = &info
		if info.URL != "" {
			hook.Message = messageWebhookUnknown
			if u, err := url.Parse(info.URL); err == nil && u.Host != "" {
				hook.Message = fmt.Sprintf(messageWebhookSet, u.Host)
			}
		}
	}
	out.Steps = append(out.Steps, hook)
	return out, nil
}

// timed makes one request of a check through run and measures it on the real clock.
func timed(ctx context.Context, run Runner, realClock clock.Clock, f func(ctx context.Context, c delivery.Call) Result) (
	time.Duration, error) {
	var latency time.Duration
	_, err := run(ctx, func(ctx context.Context, c delivery.Call) delivery.Outcome {
		start := realClock.Now()
		r := f(ctx, c)
		latency = realClock.Now().Sub(start)
		return r.Outcome
	})
	return latency, err
}

// probeVerdict says whether the answer to the dry probe is a Bot API's — 401 with a JSON body — and otherwise names
// what answered: the name, the TLS handshake, the time, the proxy, a 404 of a wrong path prefix, an answer that is not
// JSON, or another status.
func probeVerdict(r Result, base string) (bool, string) {
	if r.Status == 0 {
		switch r.Network {
		case outbound.NetworkDNS:
			host := base
			if u, err := url.Parse(base); err == nil {
				host = u.Hostname()
			}
			return false, fmt.Sprintf(messageDNS, host)
		case outbound.NetworkTLS:
			return false, fmt.Sprintf(messageTLS, r.Detail)
		case outbound.NetworkTimeout:
			return false, fmt.Sprintf(messageTimeout, int(CallTimeout/time.Second))
		case outbound.NetworkProxyAuth:
			return false, MessageProxyRefused
		}
		return false, string(r.Outcome.Error)
	}
	switch {
	case r.Status == http.StatusProxyAuthRequired:
		return false, MessageProxyRefused
	case r.Status == http.StatusNotFound:
		return false, MessageWrongPrefix
	case r.NotJSON:
		return false, MessageNotBotAPI
	case r.Status == http.StatusUnauthorized:
		return true, ""
	}
	return false, fmt.Sprintf(messageOtherStatus, r.Status)
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package connections_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/bridge"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/connections"
	"github.com/muster-io/muster/internal/connections/dbgen"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/fakeproxy"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/fakes/fakeslack"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mattermost"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/proxyconf"
	"github.com/muster-io/muster/internal/slack"
)

// recorder is a fake server that records the requests it gets.
type recorder interface {
	URL() string
	Requests() []fakeserver.Request
	ResetRequests()
}

// tokenNames names the tokens of the tests in what a failure prints, the longer of two that share a prefix first, so
// that no failure message carries a token.
var tokenNames = strings.NewReplacer(fakemattermost.SecondBotToken, "<mm-second>", botToken, "<mm-stored>",
	otherToken, "<mm-typed>", faketelegram.SecondBotToken, "<tg-second>", tgToken, "<tg-stored>", tgOtherToken,
	"<tg-typed>", fakebridge.SecondToken, "<br-second>", fakebridge.Token, "<br-stored>", fakeslack.SecondBotToken,
	"<sl-bot-b>", fakeslack.SecondAppToken, "<sl-app-b>", fakeslack.BotToken, "<sl-bot>", fakeslack.AppToken,
	"<sl-app>", unpublishedBot, "<sl-bot-x>", unpublishedApp, "<sl-app-x>")

// calls is what r was asked, one "METHOD path Authorization" per request, its tokens named.
func calls(r recorder) []string {
	out := []string{}
	for _, q := range r.Requests() {
		c := q.Method + " " + q.Path
		if a := q.Headers["Authorization"]; len(a) > 0 {
			c += " " + strings.Join(a, ",")
		}
		out = append(out, tokenNames.Replace(c))
	}
	return out
}

// stepsOf is the steps of res, each as name:ok, name:skip or name:code.
func stepsOf(res connections.CheckResult) string {
	out := make([]string, 0, len(res.Steps))
	for _, s := range res.Steps {
		switch {
		case s.Skipped:
			out = append(out, s.Name+":skip")
		case s.OK:
			out = append(out, s.Name+":ok")
		default:
			out = append(out, s.Name+":"+s.Code)
		}
	}
	return strings.Join(out, " ")
}

// vias is the path of every step of res.
func vias(res connections.CheckResult) []string {
	out := make([]string, 0, len(res.Steps))
	for _, s := range res.Steps {
		out = append(out, s.Via)
	}
	return out
}

func startMattermost(t *testing.T) *fakemattermost.Fake {
	t.Helper()
	f := fakemattermost.New()
	if err := f.Start(t.Context(), "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close(context.WithoutCancel(t.Context())) })
	return f
}

func startTelegram(t *testing.T) *faketelegram.Fake {
	t.Helper()
	f := faketelegram.New()
	if err := f.Start(t.Context(), "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close(context.WithoutCancel(t.Context())) })
	return f
}

func startProxy(t *testing.T, opts fakeproxy.Options) *fakeproxy.Server {
	t.Helper()
	p, err := fakeproxy.Start(t.Context(), fakeproxy.HTTP, "127.0.0.1:0", opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// hostOf is the host:port of the address u.
func hostOf(t *testing.T, u string) string {
	t.Helper()
	p, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	return p.Host
}

// draftEnv is the Telegram env with a saved Connection of each type at the first fake server of its type — the
// Mattermost one with botToken, the Telegram one with tgToken, the Bridge with fakebridge.Token in poll mode — and a
// second fake server of each type, the other address of a draft.
type draftEnv struct {
	*tgEnv
	mm2, tg2 recorder
	br, br2  *fakebridge.Fake

	savedMM, savedTG, savedBR connections.Connection
}

func newDraftEnv(t *testing.T) *draftEnv {
	t.Helper()
	e := &draftEnv{tgEnv: newTGEnv(t), mm2: startMattermost(t), tg2: startTelegram(t), br: startFakeBridge(t),
		br2: startFakeBridge(t)}
	e.savedMM = e.create(t, "mm")
	tg, err := e.svc.Create(t.Context(), by, e.input("tg", connections.ModeLongPolling))
	if err != nil {
		t.Fatal(err)
	}
	e.savedTG = tg
	e.savedBR = e.createFakeBridge(t, e.br, "br", fakebridge.Token, connections.EventDeliveryPoll)
	e.reset()
	return e
}

// reset forgets the requests every fake recorded and the subjects of the interactive path.
func (e *draftEnv) reset() {
	for _, r := range []recorder{e.fake, e.mm2, e.tg, e.tg2, e.br, e.br2} {
		r.ResetRequests()
	}
	e.path.subjects = nil
}

func mmDraft(addr string) connections.Draft {
	return connections.Draft{Type: connections.TypeMattermost, ServerURL: addr}
}

func tgDraft(addr string) connections.Draft {
	return connections.Draft{Type: connections.TypeTelegram, BotAPIBaseURL: addr,
		UpdateMode: connections.ModeLongPolling}
}

func brDraft(addr string) connections.Draft {
	return connections.Draft{Type: connections.TypeBridge, BridgeBaseURL: addr,
		EventDelivery: connections.EventDeliveryPoll}
}

// of is d with the bot token typed, when token is set, and the Connection it edits, when id is set.
func of(d connections.Draft, id string, token logging.Secret) connections.Draft {
	if id != "" {
		d.ConnectionID = &id
	}
	if token != "" {
		d.BotToken = keyring.Replace(token)
	}
	return d
}

// TestCheckDraftTokenRule is the token rule of C-13.FR-14, C-14.FR-11 and C-22.FR-1, one case per rule and per type,
// told apart by what each fake server recorded: a token typed in the draft goes to the draft's address only; without
// one, the stored token goes to the saved address, as a save normalizes it, through the saved proxy route — two
// disabled proxies are the same route — and otherwise only the dry probe runs, without any token, and the other steps
// are skipped; a dry probe that fails skips them too.
func TestCheckDraftTokenRule(t *testing.T) {
	e := newDraftEnv(t)
	p := startProxy(t, fakeproxy.Options{})
	viaProxy := &proxyconf.Input{Enabled: true, Type: new("http"), Address: new(p.Addr())}
	offOther := &proxyconf.Input{Enabled: false, Type: new("socks5"), Address: new("127.0.0.1:1")}
	mm, tg, br := e.savedMM.PublicID, e.savedTG.PublicID, e.savedBR.PublicID
	const (
		mmFull    = "token:ok"
		mmProbe   = "dry_probe:ok token:skip"
		tgFull    = "dry_probe:ok get_me:ok get_webhook_info:ok"
		tgProbe   = "dry_probe:ok get_me:skip get_webhook_info:skip"
		brFull    = "info:ok event_delivery:ok"
		brProbe   = "dry_probe:ok info:skip event_delivery:skip"
		ping      = "GET /api/v4/system/ping"
		tgDry     = "GET /bot0:x/getMe"
		brDry     = "GET /v1/info"
		mmMe      = "GET /api/v4/users/me Bearer "
		mmRoles   = "POST /api/v4/roles/names Bearer "
		brInfo    = "GET /v1/info Bearer "
		nowhere   = "http://127.0.0.1:1"
		unreached = "connect_failed"
	)
	tgCalls := func(name string) []string {
		return []string{tgDry, "GET /bot" + name + "/getMe", "GET /bot" + name + "/getWebhookInfo"}
	}
	for _, tc := range []struct {
		name          string
		draft         connections.Draft
		steps         string
		saved, other  recorder
		atSaved, away []string
		proxied       bool
	}{
		{name: "mattermost typed token at another address", draft: of(mmDraft(e.mm2.URL()), mm, otherToken),
			steps: mmFull, saved: e.fake, other: e.mm2, atSaved: []string{},
			away: []string{mmMe + "<mm-typed>", mmRoles + "<mm-typed>"}},
		{name: "mattermost stored token at the saved address", draft: of(mmDraft(" "+e.fake.URL()+" "), mm, ""),
			steps: mmFull, saved: e.fake, other: e.mm2, atSaved: []string{mmMe + "<mm-stored>", mmRoles + "<mm-stored>"},
			away: []string{}},
		{name: "mattermost another address without a token", draft: of(mmDraft(e.mm2.URL()), mm, ""),
			steps: mmProbe, saved: e.fake, other: e.mm2, atSaved: []string{}, away: []string{ping}},
		{name: "mattermost another proxy without a token",
			draft: func() connections.Draft { d := of(mmDraft(e.fake.URL()), mm, ""); d.Proxy = viaProxy; return d }(),
			steps: mmProbe, saved: e.fake, other: e.mm2, atSaved: []string{ping}, away: []string{}, proxied: true},
		{name: "mattermost two disabled proxies are one route",
			draft: func() connections.Draft { d := of(mmDraft(e.fake.URL()), mm, ""); d.Proxy = offOther; return d }(),
			steps: mmFull, saved: e.fake, other: e.mm2, atSaved: []string{mmMe + "<mm-stored>", mmRoles + "<mm-stored>"},
			away: []string{}},
		{name: "mattermost a failed dry probe", draft: of(mmDraft(nowhere), mm, ""),
			steps: "dry_probe:" + unreached + " token:skip", saved: e.fake, other: e.mm2, atSaved: []string{},
			away: []string{}},

		{name: "telegram typed token at another address", draft: of(tgDraft(e.tg2.URL()), tg, tgOtherToken),
			steps: tgFull, saved: e.tg, other: e.tg2, atSaved: []string{}, away: tgCalls("<tg-typed>")},
		{name: "telegram stored token at the saved address", draft: of(tgDraft(" "+e.tg.URL()+"/ "), tg, ""),
			steps: tgFull, saved: e.tg, other: e.tg2, atSaved: tgCalls("<tg-stored>"), away: []string{}},
		{name: "telegram another address without a token", draft: of(tgDraft(e.tg2.URL()), tg, ""),
			steps: tgProbe, saved: e.tg, other: e.tg2, atSaved: []string{}, away: []string{tgDry}},
		{name: "telegram another proxy without a token",
			draft: func() connections.Draft { d := of(tgDraft(e.tg.URL()), tg, ""); d.Proxy = viaProxy; return d }(),
			steps: tgProbe, saved: e.tg, other: e.tg2, atSaved: []string{tgDry}, away: []string{}, proxied: true},
		{name: "telegram two disabled proxies are one route",
			draft: func() connections.Draft { d := of(tgDraft(e.tg.URL()), tg, ""); d.Proxy = offOther; return d }(),
			steps: tgFull, saved: e.tg, other: e.tg2, atSaved: tgCalls("<tg-stored>"), away: []string{}},
		{name: "telegram a failed dry probe", draft: of(tgDraft(e.tg.URL()+"/other"), tg, ""),
			steps: "dry_probe:wrong_path_prefix get_me:skip get_webhook_info:skip", saved: e.tg, other: e.tg2,
			atSaved: []string{"GET /other/bot0:x/getMe"}, away: []string{}},

		{name: "bridge typed token at another address", draft: of(brDraft(e.br2.URL()), br, fakebridge.SecondToken),
			steps: brFull, saved: e.br, other: e.br2, atSaved: []string{}, away: []string{brInfo + "<br-second>"}},
		{name: "bridge stored token at the saved address", draft: of(brDraft(e.br.URL()+"/"), br, ""),
			steps: brFull, saved: e.br, other: e.br2, atSaved: []string{brInfo + "<br-stored>"}, away: []string{}},
		{name: "bridge another address without a token", draft: of(brDraft(e.br2.URL()), br, ""),
			steps: brProbe, saved: e.br, other: e.br2, atSaved: []string{}, away: []string{brDry}},
		{name: "bridge another proxy without a token",
			draft: func() connections.Draft { d := of(brDraft(e.br.URL()), br, ""); d.Proxy = viaProxy; return d }(),
			steps: brProbe, saved: e.br, other: e.br2, atSaved: []string{brDry}, away: []string{}, proxied: true},
		{name: "bridge two disabled proxies are one route",
			draft: func() connections.Draft { d := of(brDraft(e.br.URL()), br, ""); d.Proxy = offOther; return d }(),
			steps: brFull, saved: e.br, other: e.br2, atSaved: []string{brInfo + "<br-stored>"}, away: []string{}},
		{name: "bridge a failed dry probe", draft: of(brDraft(nowhere), br, ""),
			steps: "dry_probe:" + unreached + " info:skip event_delivery:skip", saved: e.br, other: e.br2,
			atSaved: []string{}, away: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e.reset()
			targets := len(p.Targets())
			res, err := e.svc.CheckDraft(t.Context(), tc.draft)
			if err != nil || stepsOf(res) != tc.steps {
				t.Fatalf("steps %q, %v; want %q", stepsOf(res), err, tc.steps)
			}
			if got := calls(tc.saved); !slices.Equal(got, tc.atSaved) {
				t.Errorf("at the saved address %q, want %q", got, tc.atSaved)
			}
			if got := calls(tc.other); !slices.Equal(got, tc.away) {
				t.Errorf("at the other address %q, want %q", got, tc.away)
			}
			via := "direct"
			if tc.proxied {
				via = "proxy"
				if got := p.Targets()[targets:]; len(got) != 1 || got[0] != hostOf(t, tc.saved.URL()) {
					t.Errorf("through the proxy to %v", got)
				}
			} else if len(p.Targets()) != targets {
				t.Errorf("a request went through the proxy: %v", p.Targets()[targets:])
			}
			for _, v := range vias(res) {
				if v != via {
					t.Errorf("paths %v, want %s", vias(res), via)
				}
			}
			if failed := slices.ContainsFunc(strings.Fields(tc.steps), func(s string) bool {
				return !strings.HasSuffix(s, ":ok") && !strings.HasSuffix(s, ":skip")
			}); res.OK == failed {
				t.Errorf("ok %v for %q", res.OK, tc.steps)
			}
		})
	}
	for _, r := range []recorder{e.mm2, e.tg2, e.br2} {
		r.ResetRequests()
	}
	for _, d := range []connections.Draft{of(mmDraft(e.mm2.URL()), mm, ""), of(tgDraft(e.tg2.URL()), tg, ""),
		of(brDraft(e.br2.URL()), br, "")} {
		if _, err := e.svc.CheckDraft(t.Context(), d); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []recorder{e.mm2, e.tg2, e.br2} {
		for _, q := range r.Requests() {
			raw := q.Path + " " + strings.Join(q.Headers["Authorization"], ",")
			for _, s := range []string{botToken, tgToken, fakebridge.Token} {
				if strings.Contains(raw, s) {
					t.Errorf("a stored token reached another address: %s", tokenNames.Replace(raw))
				}
			}
		}
	}
}

// TestCheckDraftProxyPassword is the proxy password of the rule of C-13.FR-14: the stored password goes only with the
// saved proxy type and address and no password in the draft; null sends none, and a typed one is sent as typed.
func TestCheckDraftProxyPassword(t *testing.T) {
	e := newDraftEnv(t)
	creds := fakeproxy.Options{Username: "muster", Password: "proxy-pass"}
	saved, other := startProxy(t, creds), startProxy(t, creds)
	in := input("mm-proxy", e.fake.URL())
	in.Proxy = proxyconf.Input{Enabled: true, Type: new("http"), Address: new(saved.Addr()), UsernameSet: true,
		Username: new("muster"), Password: keyring.Replace("proxy-pass")}
	c, err := e.svc.Create(t.Context(), by, in)
	if err != nil {
		t.Fatal(err)
	}
	proxied := func(addr string, password keyring.SecretInput) connections.Draft {
		d := of(mmDraft(e.fake.URL()), c.PublicID, "")
		d.Proxy = &proxyconf.Input{Enabled: true, Type: new("http"), Address: new(addr), Password: password}
		return d
	}
	for _, tc := range []struct {
		name           string
		draft          connections.Draft
		steps          string
		proxy          *fakeproxy.Server
		refused, calls int
	}{
		{name: "the saved proxy", draft: of(mmDraft(e.fake.URL()), c.PublicID, ""), steps: "token:ok", proxy: saved,
			calls: 2},
		{name: "the saved proxy without its password", draft: proxied(saved.Addr(), keyring.Clear),
			steps: "token:request_failed", proxy: saved, refused: 1},
		{name: "another proxy without a password", draft: proxied(other.Addr(), keyring.Keep),
			steps: "dry_probe:proxy_refused token:skip", proxy: other, refused: 1},
		{name: "another proxy with a typed password", draft: proxied(other.Addr(), keyring.Replace("proxy-pass")),
			steps: "dry_probe:ok token:skip", proxy: other, calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refused, targets := tc.proxy.Refused(), len(tc.proxy.Targets())
			res, err := e.svc.CheckDraft(t.Context(), tc.draft)
			if err != nil || stepsOf(res) != tc.steps {
				t.Fatalf("steps %q, %v; want %q", stepsOf(res), err, tc.steps)
			}
			if tc.proxy.Refused()-refused != tc.refused || len(tc.proxy.Targets())-targets != tc.calls {
				t.Errorf("refused %d, connected %d", tc.proxy.Refused()-refused, len(tc.proxy.Targets())-targets)
			}
		})
	}
	if strings.Contains(e.log.String(), "proxy-pass") {
		t.Error("the proxy password reached the log")
	}
}

// TestCheckDraftProxyTurnedOff is the proxy route of the rule of C-13.FR-14 for a saved Connection of each type behind
// a proxy: a draft of the saved values checks the stored token through that proxy, and a draft at the saved address
// with the proxy turned off and no token typed runs only the dry probe, directly, so that the stored token never
// leaves the saved proxy out.
func TestCheckDraftProxyTurnedOff(t *testing.T) {
	e := newDraftEnv(t)
	p := startProxy(t, fakeproxy.Options{})
	br := bridgeInput("br-proxied", e.br.URL(), connections.EventDeliveryPoll)
	br.BotToken = keyring.Replace(logging.Secret(fakebridge.Token))
	for _, tc := range []struct {
		in          connections.Input
		draft       connections.Draft
		saved       recorder
		full, probe string
		dry         string
	}{
		{in: input("mm-proxied", e.fake.URL()), draft: mmDraft(e.fake.URL()), saved: e.fake, full: "token:ok",
			probe: "dry_probe:ok token:skip", dry: "GET /api/v4/system/ping"},
		{in: e.input("tg-proxied", connections.ModeLongPolling), draft: tgDraft(e.tg.URL()), saved: e.tg,
			full: "dry_probe:ok get_me:ok get_webhook_info:ok", probe: "dry_probe:ok get_me:skip get_webhook_info:skip",
			dry: "GET /bot0:x/getMe"},
		{in: br, draft: brDraft(e.br.URL()), saved: e.br, full: "info:ok event_delivery:ok",
			probe: "dry_probe:ok info:skip event_delivery:skip", dry: "GET /v1/info"},
	} {
		t.Run(tc.in.Type, func(t *testing.T) {
			tc.in.Proxy = proxyconf.Input{Enabled: true, Type: new("http"), Address: new(p.Addr())}
			c, err := e.svc.Create(t.Context(), by, tc.in)
			if err != nil {
				t.Fatal(err)
			}
			e.reset()
			targets := len(p.Targets())
			res, err := e.svc.CheckDraft(t.Context(), of(tc.draft, c.PublicID, ""))
			if err != nil || stepsOf(res) != tc.full || slices.ContainsFunc(vias(res), func(v string) bool {
				return v != "proxy"
			}) {
				t.Fatalf("the saved proxy: steps %q through %v, %v; want %q", stepsOf(res), vias(res), err, tc.full)
			}
			if got := p.Targets()[targets:]; len(got) == 0 || slices.ContainsFunc(got, func(s string) bool {
				return s != hostOf(t, tc.saved.URL())
			}) {
				t.Errorf("the saved proxy connected to %v", got)
			}

			e.reset()
			targets = len(p.Targets())
			off := of(tc.draft, c.PublicID, "")
			off.Proxy = &proxyconf.Input{Enabled: false}
			res, err = e.svc.CheckDraft(t.Context(), off)
			if err != nil || stepsOf(res) != tc.probe || slices.ContainsFunc(vias(res), func(v string) bool {
				return v != "direct"
			}) {
				t.Fatalf("the proxy turned off: steps %q through %v, %v; want %q", stepsOf(res), vias(res), err,
					tc.probe)
			}
			if got := p.Targets()[targets:]; len(got) != 0 {
				t.Errorf("with the proxy turned off a request went through it to %v", got)
			}
			if got := calls(tc.saved); !slices.Equal(got, []string{tc.dry}) {
				t.Errorf("with the proxy turned off the saved address got %q, want only %q", got, tc.dry)
			}
		})
	}
}

// TestCheckDraftStoresNothing is C-13.FR-14: drafts of new Connections of each type pass with their typed tokens and
// drafts of the saved ones with the stored tokens, and nothing is written — no Connection, no bot, no info, no hint,
// no Audit log entry, no log line — and the saved Connections keep their bot, info and version.
func TestCheckDraftStoresNothing(t *testing.T) {
	e := newDraftEnv(t)
	if _, err := e.svc.Check(t.Context(), e.savedBR.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	e.reset()
	rows := map[int64]dbgen.GetConnectionRow{}
	for id, r := range e.store.rows {
		rows[id] = r.row
	}
	audits, hints, bots, infos, updates, logged := len(e.store.audit), len(e.store.hints), len(e.store.bots),
		len(e.store.infos), len(e.store.updates), e.log.Len()

	res, err := e.svc.CheckDraft(t.Context(), of(mmDraft(e.mm2.URL()), "", fakemattermost.SecondBotToken))
	if err != nil || !res.OK || stepsOf(res) != "token:ok" || res.BotName != fakemattermost.SecondBotUsername ||
		!slices.Equal(res.Warnings, []string{connections.WarningPressAnswersInThread}) || res.Bridge {
		t.Errorf("new mattermost = %q %+v, %v", stepsOf(res), res.BotName, err)
	}
	res, err = e.svc.CheckDraft(t.Context(), of(tgDraft(e.tg.URL()), "", faketelegram.SecondBotToken))
	if err != nil || !res.OK || stepsOf(res) != "dry_probe:ok get_me:ok get_webhook_info:ok" ||
		res.BotName != faketelegram.SecondBotUsername || res.WebhookSet == nil || *res.WebhookSet {
		t.Errorf("new telegram = %q %s, %v", stepsOf(res), res.BotName, err)
	}
	res, err = e.svc.CheckDraft(t.Context(), of(brDraft(e.br.URL()), "", fakebridge.SecondToken))
	if err != nil || !res.OK || stepsOf(res) != "info:ok event_delivery:ok" || !res.Bridge || len(res.Info) == 0 {
		t.Errorf("new bridge = %q, %v", stepsOf(res), err)
	}
	if i, err := bridge.DecodeInfo(res.Info); err != nil || i.Messenger.Name != fakebridge.MessengerName {
		t.Errorf("the info returned = %v", err)
	}
	for _, s := range e.path.subjects {
		if !s.NoLimiter || s.Connection != nil || s.Destination != nil {
			t.Errorf("a draft of a new Connection is limited by %+v", s)
		}
	}
	for _, d := range []connections.Draft{of(mmDraft(e.fake.URL()), e.savedMM.PublicID, ""),
		of(tgDraft(e.tg.URL()), e.savedTG.PublicID, ""), of(brDraft(e.br.URL()), e.savedBR.PublicID, "")} {
		if res, err := e.svc.CheckDraft(t.Context(), d); err != nil || !res.OK || res.Steps[0].Name == "dry_probe" &&
			d.Type != connections.TypeTelegram {
			t.Errorf("saved %s = %q, %v", d.Type, stepsOf(res), err)
		}
	}

	if len(e.store.rows) != len(rows) || len(e.store.audit) != audits || len(e.store.hints) != hints ||
		len(e.store.bots) != bots || len(e.store.infos) != infos || len(e.store.updates) != updates {
		t.Errorf("a draft wrote: %d rows, %d audit, %d hints, %d bots, %d infos, %d updates", len(e.store.rows)-len(rows),
			len(e.store.audit)-audits, len(e.store.hints)-hints, len(e.store.bots)-bots, len(e.store.infos)-infos,
			len(e.store.updates)-updates)
	}
	for id, r := range rows {
		if !reflect.DeepEqual(e.store.rows[id].row, r) {
			t.Errorf("the saved Connection %s changed", r.PublicID)
		}
	}
	if e.log.Len() != logged {
		t.Errorf("a draft logged: %s", tokenNames.Replace(e.log.String()[logged:]))
	}
}

// TestCheckDraftLimiter is the interactive path of C-13.FR-14: a draft of a saved Connection takes every limiter token
// from that Connection, whatever its address and token, and a draft of a new one takes none; no token within the
// budget is a *delivery.LimitedError at every step of every type; a RetryAfter answered to a draft reaches the path as
// Transient, so that it never holds the saved Connection's limiter.
func TestCheckDraftLimiter(t *testing.T) {
	e := newDraftEnv(t)
	saved := []connections.Draft{of(mmDraft(e.fake.URL()), e.savedMM.PublicID, ""),
		of(mmDraft(e.mm2.URL()), e.savedMM.PublicID, ""), of(tgDraft(e.tg2.URL()), e.savedTG.PublicID, tgOtherToken),
		of(brDraft(e.br2.URL()), e.savedBR.PublicID, "")}
	ids := map[string]int64{connections.TypeMattermost: e.savedMM.ID, connections.TypeTelegram: e.savedTG.ID,
		connections.TypeBridge: e.savedBR.ID}
	for _, d := range saved {
		e.path.subjects = nil
		if _, err := e.svc.CheckDraft(t.Context(), d); err != nil {
			t.Fatal(err)
		}
		for _, s := range e.path.subjects {
			if s.NoLimiter || s.Destination != nil || s.Connection == nil || *s.Connection != ids[d.Type] {
				t.Errorf("a %s draft of a saved Connection is limited by %+v", d.Type, s)
			}
		}
		if len(e.path.subjects) == 0 {
			t.Errorf("a %s draft made no request", d.Type)
		}
	}
	e.path.subjects = nil
	if _, err := e.svc.CheckDraft(t.Context(), of(tgDraft(e.tg2.URL()), "", tgOtherToken)); err != nil ||
		len(e.path.subjects) != 3 || slices.ContainsFunc(e.path.subjects, func(s delivery.Subject) bool {
		return !s.NoLimiter
	}) {
		t.Errorf("a new draft = %v, limited by %+v", err, e.path.subjects)
	}

	e.path.limited = true
	for _, d := range []connections.Draft{mmDraft(e.mm2.URL()), of(mmDraft(e.mm2.URL()), "", otherToken),
		tgDraft(e.tg2.URL()), of(tgDraft(e.tg2.URL()), "", tgOtherToken), brDraft(e.br2.URL()),
		of(brDraft(e.br2.URL()), "", fakebridge.SecondToken), saved[0]} {
		if _, err := e.svc.CheckDraft(t.Context(), d); !isLimited(err) {
			t.Errorf("a limited %s draft = %v", d.Type, err)
		}
	}
	e.path.limited = false

	var outcomes []delivery.Outcome
	unlimited := deliverytest.Unlimited(1, clock.Clocks{Business: e.business, Real: clock.Real{}})
	e.path.next = interactiveFunc(func(ctx context.Context, s delivery.Subject, op delivery.Op) (delivery.Outcome,
		error) {
		out, err := unlimited.Do(ctx, s, op)
		outcomes = append(outcomes, out)
		return out, err
	})
	if err := e.mm2.(*fakemattermost.Fake).SetFault(fakeserver.Fault{Path: "/api/v4/users/me", Status: 429,
		RetryAfterSeconds: 30, Times: 1}); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.CheckDraft(t.Context(), of(mmDraft(e.mm2.URL()), e.savedMM.PublicID, otherToken))
	if err != nil || res.OK || len(outcomes) != 1 || outcomes[0].Kind != delivery.OutcomeTransient ||
		outcomes[0].RetryAfter != 0 {
		t.Errorf("a RetryAfter = %q, %v, outcomes %+v", stepsOf(res), err, outcomes)
	}
}

// TestCheckDraftRefusals is what a save would refuse, refused before any request: an unknown, malformed, deleted or
// other type's connection_id at /connection_id with unknown_id, an address in the wrong format at its pointer, an
// update or event delivery mode, a proxy and the secrets that are empty; and the failures of the store, of a stored
// secret or proxy that cannot be read and of a proxy the client refuses, as errors that carry no token.
func TestCheckDraftRefusals(t *testing.T) {
	e := newDraftEnv(t)
	gone, err := e.svc.Create(t.Context(), by, input("gone", e.fake.URL()))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Delete(t.Context(), by, gone.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	e.reset()
	withProxy := func(d connections.Draft, p proxyconf.Input) connections.Draft {
		d.Proxy = &p
		return d
	}
	for _, tc := range []struct {
		name          string
		draft         connections.Draft
		pointer, code string
	}{
		{"unknown id", of(mmDraft(e.fake.URL()), "CN000000000000", ""), "/connection_id", connections.CodeUnknownID},
		{"malformed id", of(mmDraft(e.fake.URL()), "bad", ""), "/connection_id", connections.CodeUnknownID},
		{"deleted", of(mmDraft(e.fake.URL()), gone.PublicID, ""), "/connection_id", connections.CodeUnknownID},
		{"other type", of(mmDraft(e.fake.URL()), e.savedTG.PublicID, ""), "/connection_id", connections.CodeUnknownID},
		{"bridge of a telegram id", of(brDraft(e.br.URL()), e.savedTG.PublicID, ""), "/connection_id",
			connections.CodeUnknownID},
		{"server url", of(mmDraft("ftp://x"), e.savedMM.PublicID, ""), "/server_url", connections.CodeInvalidFormat},
		{"server url with user", mmDraft("http://u:p@x/"), "/server_url", connections.CodeInvalidFormat},
		{"bot api base url", tgDraft("ftp://x"), "/bot_api_base_url", connections.CodeInvalidFormat},
		{"base url with query", brDraft("http://x/?q"), "/base_url", connections.CodeInvalidFormat},
		{"update mode", connections.Draft{Type: connections.TypeTelegram, BotAPIBaseURL: e.tg.URL(),
			UpdateMode: "smoke"}, "/update_mode", connections.CodeInvalidFormat},
		{"event delivery", connections.Draft{Type: connections.TypeBridge, BridgeBaseURL: e.br.URL(),
			EventDelivery: "smoke"}, "/event_delivery", connections.CodeInvalidFormat},
		{"type", connections.Draft{Type: "smoke"}, "/type", connections.CodeInvalidFormat},
		{"empty token", connections.Draft{Type: connections.TypeMattermost, ServerURL: e.fake.URL(),
			BotToken: keyring.Replace("")}, "/bot_token", connections.CodeTooShort},
		{"null token", connections.Draft{Type: connections.TypeMattermost, ServerURL: e.fake.URL(),
			BotToken: keyring.Clear}, "/bot_token", connections.CodeRequired},
		{"proxy type", withProxy(mmDraft(e.fake.URL()), proxyconf.Input{Enabled: true, Address: new("127.0.0.1:1")}),
			"/proxy/type", connections.CodeRequired},
		{"proxy address", withProxy(tgDraft(e.tg.URL()), proxyconf.Input{Enabled: true, Type: new("http")}),
			"/proxy/address", connections.CodeRequired},
		{"empty proxy password", withProxy(brDraft(e.br.URL()), proxyconf.Input{Enabled: true, Type: new("http"),
			Address: new("127.0.0.1:1"), Password: keyring.Replace("")}), "/proxy/password", connections.CodeTooShort},
	} {
		_, err := e.svc.CheckDraft(t.Context(), tc.draft)
		if fe := fieldError(err); fe == nil || fe.Pointer != tc.pointer || fe.Code != tc.code {
			t.Errorf("%s = %v", tc.name, err)
		}
	}
	for _, r := range []recorder{e.fake, e.mm2, e.tg, e.tg2, e.br, e.br2} {
		if n := len(r.Requests()); n != 0 {
			t.Errorf("a refused draft made %d requests", n)
		}
	}

	e.store.fail["GetConnection"] = errors.New("down")
	if _, err := e.svc.CheckDraft(t.Context(), of(mmDraft(e.fake.URL()), e.savedMM.PublicID, "")); err == nil ||
		fieldError(err) != nil {
		t.Errorf("a failed read = %v", err)
	}
	delete(e.store.fail, "GetConnection")
	for _, c := range []connections.Connection{e.savedMM, e.savedTG, e.savedBR} {
		row := &e.store.rows[c.ID].row
		keep := *row
		addr := map[string]string{connections.TypeMattermost: e.fake.URL(), connections.TypeTelegram: e.tg.URL(),
			connections.TypeBridge: e.br.URL()}[c.Type]
		d := of(connections.Draft{Type: c.Type, ServerURL: addr, BotAPIBaseURL: addr, BridgeBaseURL: addr,
			UpdateMode: connections.ModeLongPolling, EventDelivery: connections.EventDeliveryPoll}, c.PublicID, "")
		if c.Type != connections.TypeMattermost {
			d.ServerURL = ""
		}
		row.BotTokenCiphertext = []byte("garbage")
		if _, err := e.svc.CheckDraft(t.Context(), d); err == nil || strings.Contains(err.Error(), botToken) ||
			strings.Contains(err.Error(), tgToken) || strings.Contains(err.Error(), fakebridge.Token) {
			t.Errorf("%s with an unreadable token = %s", c.Type, tokenNames.Replace(fmt.Sprint(err)))
		}
		other := d
		other.ServerURL, other.BotAPIBaseURL, other.BridgeBaseURL = e.mm2.URL(), e.tg2.URL(), e.br2.URL()
		if c.Type != connections.TypeMattermost {
			other.ServerURL = ""
		}
		if res, err := e.svc.CheckDraft(t.Context(), other); err != nil || !res.OK {
			t.Errorf("%s at another address opened the unreadable token: %q, %v", c.Type, stepsOf(res), err)
		}
		*row = keep
		row.Proxy = []byte(`{"enabled":true,"type":"http","address":"127.0.0.1:1"}`)
		row.ProxyPasswordCiphertext, row.ProxyPasswordKeyID = []byte("garbage"), pgtype.Text{String: "k", Valid: true}
		if _, err := e.svc.CheckDraft(t.Context(), d); err == nil {
			t.Errorf("%s with an unreadable proxy password", c.Type)
		}
		row.ProxyPasswordCiphertext, row.ProxyPasswordKeyID = keep.ProxyPasswordCiphertext, keep.ProxyPasswordKeyID
		row.Proxy = []byte(`{"enabled":true,"type":"ftp","address":"127.0.0.1:1"}`)
		if _, err := e.svc.CheckDraft(t.Context(), d); err == nil {
			t.Errorf("%s with a proxy the client refuses", c.Type)
		}
		row.Proxy = []byte("{")
		if _, err := e.svc.CheckDraft(t.Context(), d); err == nil {
			t.Errorf("%s with an unreadable proxy", c.Type)
		}
		*row = keep
	}
}

// TestCheckDraftNotBridge: the dry probe of a Bridge draft at an address that answers, but not with a problem of the
// protocol, fails with not_bridge naming the media type, and skips the steps with the token (C-22.FR-2).
func TestCheckDraftNotBridge(t *testing.T) {
	e := newDraftEnv(t)
	b := startStandIn(t)
	res, err := e.svc.CheckDraft(t.Context(), of(brDraft(b.srv.URL), e.savedBR.PublicID, ""))
	if err != nil || res.OK || stepsOf(res) != "dry_probe:not_bridge info:skip event_delivery:skip" ||
		!strings.Contains(res.Steps[0].Message, "application/json") || res.Info != nil {
		t.Fatalf("not a bridge = %q %q, %v", stepsOf(res), res.Steps[0].Message, err)
	}
	if reqs := b.seen(); len(reqs) != 1 || reqs[0].Header.Get("Authorization") != "" {
		t.Errorf("%d requests, the first with a token %v", len(reqs), len(reqs) > 0 &&
			reqs[0].Header.Get("Authorization") != "")
	}
}

// capture is an outbound.Recorder that keeps every record it gets.
type capture struct {
	mu      sync.Mutex
	records []outbound.Record
}

func (c *capture) Record(_ context.Context, r outbound.Record) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, r)
}

// take is the records kept since the last take.
func (c *capture) take() []outbound.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.records
	c.records = nil
	return out
}

// recordingService is a Service over the store, keyring and interactive path of e whose clients record into a
// capture.
func recordingService(t *testing.T, e *draftEnv) (*connections.Service, *capture) {
	t.Helper()
	logger := logging.New(e.log, logging.LevelInfo)
	policy, err := outbound.ParsePolicy("standard", []string{"127.0.0.0/8"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := &capture{}
	clocks := clock.Clocks{Business: e.business, Real: &ticking{at: t0, step: 7 * time.Millisecond}}
	return connections.New(connections.Config{OrgID: 1, Store: e.store, Keyring: e.keys,
		Audit: audit.NewWriter(logger, clocks.Business), Clocks: clocks,
		Network: mattermost.Network{Policy: outbound.StaticPolicy(policy), Log: logger, Real: clock.Real{},
			Recorder: rec},
		Interactive: e.path, IngestURL: &url.URL{Scheme: "http", Host: "localhost:8081", Path: "/"}, Log: logger}), rec
}

// TestCheckRuns covers the kinds and runs of the Connection checks (C-23.FR-1, FR-2, FR-5): a check of a saved
// Connection records connection_check under the Connection and a run the result names; a check before saving records
// draft_check, with no owner for a new Connection and under the Connection a draft names otherwise, its dry probe
// included; each check has a run of its own, and a draft refused by its validation records nothing.
func TestCheckRuns(t *testing.T) {
	e := newDraftEnv(t)
	svc, rec := recordingService(t, e)
	runRe := regexp.MustCompile(`^CR[0-9A-HJKMNP-TV-Z]{12}$`)
	type want struct {
		conn  int64
		draft bool
		kind  string
		ops   []string
	}
	runs := map[string]bool{}
	check := func(name string, res connections.CheckResult, err error, w want) {
		t.Helper()
		got := rec.take()
		if err != nil || !runRe.MatchString(res.Run) || runs[res.Run] {
			t.Fatalf("%s: run %q, %v", name, res.Run, err)
		}
		runs[res.Run] = true
		ops := []string{}
		for _, r := range got {
			ops = append(ops, r.Operation)
			if r.Owner.Connection != w.conn || r.Owner.Destination != 0 || r.Owner.Draft != w.draft ||
				r.Links != (outbound.Links{Check: w.kind, Run: res.Run}) {
				t.Errorf("%s: a record of %+v, links %+v", name, r.Owner, r.Links)
			}
		}
		if !slices.Equal(ops, w.ops) {
			t.Errorf("%s: operations %v, want %v", name, ops, w.ops)
		}
	}

	res, err := svc.Check(t.Context(), e.savedBR.PublicID, nil)
	check("the check of a saved Bridge", res, err, want{conn: e.savedBR.ID, kind: "connection_check",
		ops: []string{"getInfo"}})
	res, err = svc.Check(t.Context(), e.savedMM.PublicID, nil)
	check("the check of a saved Mattermost Connection", res, err, want{conn: e.savedMM.ID, kind: "connection_check",
		ops: []string{"getMe", "getRolesByNames"}})
	res, err = svc.CheckDraft(t.Context(), of(brDraft(e.br2.URL()), "", fakebridge.SecondToken))
	check("a new Bridge", res, err, want{draft: true, kind: "draft_check", ops: []string{"getInfo"}})
	res, err = svc.CheckDraft(t.Context(), of(brDraft(e.br2.URL()), e.savedBR.PublicID, fakebridge.SecondToken))
	check("a draft of a saved Bridge", res, err, want{conn: e.savedBR.ID, kind: "draft_check",
		ops: []string{"getInfo"}})
	res, err = svc.CheckDraft(t.Context(), tgDraft(e.tg2.URL()))
	check("a new Telegram Connection without a token", res, err, want{draft: true, kind: "draft_check",
		ops: []string{"getMe"}})
	res, err = svc.CheckDraft(t.Context(), of(mmDraft(e.mm2.URL()), "", otherToken))
	check("a new Mattermost Connection", res, err, want{draft: true, kind: "draft_check",
		ops: []string{"getMe", "getRolesByNames"}})
	res, err = svc.CheckDraft(t.Context(), of(mmDraft(e.mm2.URL()), e.savedMM.PublicID, ""))
	check("a draft of a saved Mattermost Connection at another address", res, err, want{conn: e.savedMM.ID,
		kind: "draft_check", ops: []string{"getPing"}})

	if _, err := svc.CheckDraft(t.Context(), brDraft("http://x/?q")); fieldError(err) == nil {
		t.Errorf("a refused draft = %v", err)
	}
	if got := rec.take(); len(got) != 0 {
		t.Errorf("a refused draft recorded %+v", got)
	}
}

// The tokens of a Slack Connection that muster dev does not route to the fake Slack server: its requests go to Slack.
const (
	unpublishedBot = "xoxb-draft-unpublished-bot"
	unpublishedApp = "xapp-draft-unpublished-app"
)

// noDNS resolves no name and records the names it is asked for, so that no request reaches Slack.
type noDNS struct {
	mu    sync.Mutex
	hosts []string
}

func (r *noDNS) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hosts = append(r.hosts, host)
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func (r *noDNS) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.hosts
	r.hosts = nil
	return out
}

// devRoute is the route of muster dev: the fake Slack server f for its published bot tokens, Slack for any other.
func devRoute(f *fakeslack.Fake) slack.Route {
	return func(token logging.Secret) string {
		if fakeslack.PublishedBotToken(string(token)) {
			return f.URL() + "/api/"
		}
		return ""
	}
}

// slackDraftService is a Service over the store, keyring and interactive path of e with the route of muster dev, or
// route when it is given, a resolver that resolves no name and a Request log that records into a capture.
func slackDraftService(t *testing.T, e *env, f *fakeslack.Fake, route ...slack.Route) (*connections.Service, *capture,
	*noDNS) {
	t.Helper()
	logger := logging.New(e.log, logging.LevelInfo)
	policy, err := outbound.ParsePolicy("standard", []string{"127.0.0.0/8"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec, dns := &capture{}, &noDNS{}
	clocks := clock.Clocks{Business: e.business, Real: &ticking{at: t0, step: 7 * time.Millisecond}}
	return connections.New(connections.Config{OrgID: 1, Store: e.store, Keyring: e.keys,
		Audit: audit.NewWriter(logger, clocks.Business), Clocks: clocks,
		Network: mattermost.Network{Policy: outbound.StaticPolicy(policy), Log: logger, Real: clock.Real{},
			Resolver: dns, Recorder: rec},
		Interactive: e.path, Log: logger, SlackRoute: append(route, devRoute(f))[0]}), rec, dns
}

// slDraft is a Slack draft in mode for the Connection id, empty for a new one, with the tokens typed when set and the
// proxy p, nil for the saved one.
func slDraft(mode, id string, bot, app logging.Secret, p *proxyconf.Input) connections.Draft {
	d := of(connections.Draft{Type: connections.TypeSlack, EventMode: mode, Proxy: p}, id, bot)
	if app != "" {
		d.AppToken = keyring.Replace(app)
	}
	return d
}

// TestCheckSlackDraft is the rule of C-13.FR-14 for Slack (C-24.FR-2), whose address is fixed, one case per rule,
// told apart by what the fake Slack server and the proxy recorded: typed tokens go to Slack through the draft's proxy
// with the steps of the saved check; without a typed bot token the stored one goes only through the saved proxy
// route, and otherwise only the dry probe runs, without any token, through the draft's proxy, the other steps skipped;
// the app-level token typed is used, the stored one only on the saved route with a bot token muster dev routes where
// it routes the stored one, and otherwise the step app_token is skipped. Under muster dev the bot token that is sent
// decides the address, and the stored one that of the dry probe of a saved Connection; the dry probe of a new one
// goes to Slack. Each request is recorded as draft_check, under the Connection a draft names.
func TestCheckSlackDraft(t *testing.T) {
	e, f := newSlackEnv(t)
	svc, rec, dns := slackDraftService(t, e, f)
	p := startProxy(t, fakeproxy.Options{})
	viaProxy := &proxyconf.Input{Enabled: true, Type: new("http"), Address: new(p.Addr())}
	off := &proxyconf.Input{Enabled: false}
	sl := e.createSlack(t, slackInput("sl", connections.EventModeSocket))
	httpIn := slackInput("sl-http", connections.EventModeHTTP)
	httpIn.AppToken = keyring.Replace(fakeslack.AppToken)
	slHTTP := e.createSlack(t, httpIn)
	proxiedIn := slackInput("sl-proxied", connections.EventModeSocket)
	proxiedIn.Proxy = *viaProxy
	slProxied := e.createSlack(t, proxiedIn)
	realIn := slackInput("sl-real", connections.EventModeSocket)
	realIn.BotToken, realIn.AppToken = keyring.Replace(unpublishedBot), keyring.Replace(unpublishedApp)
	slReal := e.createSlack(t, realIn)
	realProxiedIn := realIn
	realProxiedIn.Name, realProxiedIn.Proxy = "sl-real-proxied", *viaProxy
	slRealProxied := e.createSlack(t, realProxiedIn)
	const (
		full      = "token:ok scopes:ok app_token:ok"
		probe     = "dry_probe:ok token:skip scopes:skip app_token:skip"
		authTest  = "POST /api/auth.test"
		open      = "POST /api/apps.connections.open"
		botsInfo  = "POST /api/bots.info"
		bearer    = " Bearer "
		dnsFailed = "dns_failed"
	)
	fullCalls := func(bot, app string) []string {
		return []string{authTest + bearer + bot, open + bearer + app, authTest + bearer + app, botsInfo + bearer + bot}
	}
	for _, tc := range []struct {
		name    string
		draft   connections.Draft
		steps   string
		calls   []string
		via     string
		slack   bool
		resolve []string
		owner   int64
	}{
		{name: "new, typed tokens", draft: slDraft("socket", "", fakeslack.SecondBotToken, fakeslack.SecondAppToken,
			nil), steps: full, calls: fullCalls("<sl-bot-b>", "<sl-app-b>"), via: "direct", slack: true},
		{name: "new, typed tokens through a proxy", draft: slDraft("socket", "", fakeslack.SecondBotToken,
			fakeslack.SecondAppToken, viaProxy), steps: full, calls: fullCalls("<sl-bot-b>", "<sl-app-b>"),
			via: "proxy", slack: true},
		{name: "new, http mode", draft: slDraft("http", "", fakeslack.SecondBotToken, "", nil),
			steps: "token:ok scopes:ok", calls: []string{authTest + bearer + "<sl-bot-b>"}, via: "direct", slack: true},
		{name: "new, no app-level token", draft: slDraft("socket", "", fakeslack.SecondBotToken, "", nil),
			steps: "token:ok scopes:ok app_token:skip", calls: []string{authTest + bearer + "<sl-bot-b>"},
			via: "direct", slack: true},
		{name: "new, no token: the dry probe at Slack", draft: slDraft("socket", "", "", fakeslack.SecondAppToken, nil),
			steps: "dry_probe:" + dnsFailed + " token:skip scopes:skip app_token:skip", calls: []string{},
			via: "direct", resolve: []string{"slack.com"}},
		{name: "new, a token muster dev does not route goes to Slack", draft: slDraft("socket", "", unpublishedBot,
			unpublishedApp, nil), steps: "token:" + dnsFailed + " scopes:skip app_token:skip", calls: []string{},
			via: "direct", resolve: []string{"slack.com"}},
		{name: "saved, stored tokens on the saved route", draft: slDraft("socket", sl.PublicID, "", "", nil),
			steps: full, calls: fullCalls("<sl-bot>", "<sl-app>"), via: "direct", slack: true, owner: sl.ID},
		{name: "saved, two disabled proxies are one route", draft: slDraft("socket", sl.PublicID, "", "",
			&proxyconf.Input{Enabled: false, Type: new("socks5"), Address: new("127.0.0.1:1")}), steps: full,
			calls: fullCalls("<sl-bot>", "<sl-app>"), via: "direct", slack: true, owner: sl.ID},
		{name: "saved, only the proxy changed: the dry probe", draft: slDraft("socket", sl.PublicID, "", "", viaProxy),
			steps: probe, calls: []string{authTest}, via: "proxy", owner: sl.ID},
		{name: "saved, only the proxy changed, http mode", draft: slDraft("http", sl.PublicID, "", "", viaProxy),
			steps: "dry_probe:ok token:skip scopes:skip", calls: []string{authTest}, via: "proxy", owner: sl.ID},
		{name: "saved, only the proxy changed, an app-level token typed: the dry probe",
			draft: slDraft("socket", sl.PublicID, "", fakeslack.SecondAppToken, viaProxy), steps: probe,
			calls: []string{authTest}, via: "proxy", owner: sl.ID},
		{name: "saved, the tokens typed again through the new proxy", draft: slDraft("socket", sl.PublicID,
			fakeslack.SecondBotToken, fakeslack.SecondAppToken, viaProxy), steps: full,
			calls: fullCalls("<sl-bot-b>", "<sl-app-b>"), via: "proxy", slack: true, owner: sl.ID},
		{name: "saved, the bot token typed through the new proxy: no stored app-level token",
			draft: slDraft("socket", sl.PublicID, fakeslack.SecondBotToken, "", viaProxy),
			steps: "token:ok scopes:ok app_token:skip", calls: []string{authTest + bearer + "<sl-bot-b>"}, via: "proxy",
			slack: true, owner: sl.ID},
		{name: "saved, the bot token typed on the saved route: the stored app-level token",
			draft: slDraft("socket", sl.PublicID, fakeslack.SecondBotToken, "", nil),
			steps: "token:ok scopes:ok app_token:app_mismatch", calls: []string{authTest + bearer + "<sl-bot-b>",
				open + bearer + "<sl-app>", authTest + bearer + "<sl-app>", botsInfo + bearer + "<sl-bot-b>"},
			via: "direct", slack: true, owner: sl.ID},
		{name: "saved, the app-level token typed on the saved route: the stored bot token",
			draft: slDraft("socket", sl.PublicID, "", fakeslack.SecondAppToken, nil),
			steps: "token:ok scopes:ok app_token:app_mismatch", calls: []string{authTest + bearer + "<sl-bot>",
				open + bearer + "<sl-app-b>", authTest + bearer + "<sl-app-b>", botsInfo + bearer + "<sl-bot>"},
			via: "direct", slack: true, owner: sl.ID},
		{name: "saved in http mode, a socket draft: the stored app-level token",
			draft: slDraft("socket", slHTTP.PublicID, "", "", nil), steps: full,
			calls: fullCalls("<sl-bot>", "<sl-app>"), via: "direct", slack: true, owner: slHTTP.ID},
		{name: "saved behind a proxy, the proxy turned off: the dry probe directly",
			draft: slDraft("socket", slProxied.PublicID, "", "", off), steps: probe, calls: []string{authTest},
			via: "direct", owner: slProxied.ID},
		{name: "saved behind a proxy, the stored tokens through it", draft: slDraft("socket", slProxied.PublicID, "",
			"", nil), steps: full, calls: fullCalls("<sl-bot>", "<sl-app>"), via: "proxy", slack: true,
			owner: slProxied.ID},
		{name: "saved at Slack, the proxy turned off: the dry probe at Slack",
			draft: slDraft("socket", slRealProxied.PublicID, "", "", off),
			steps: "dry_probe:" + dnsFailed + " token:skip scopes:skip app_token:skip", calls: []string{},
			via: "direct", resolve: []string{"slack.com"}, owner: slRealProxied.ID},
		{name: "saved at Slack, a bot token routed to the fake: no stored app-level token",
			draft: slDraft("socket", slReal.PublicID, fakeslack.SecondBotToken, "", nil),
			steps: "token:ok scopes:ok app_token:skip", calls: []string{authTest + bearer + "<sl-bot-b>"},
			via: "direct", slack: true, owner: slReal.ID},
		{name: "saved at the fake, a bot token muster dev does not route goes to Slack without the stored app-level token",
			draft: slDraft("socket", sl.PublicID, unpublishedBot, "", nil),
			steps: "token:" + dnsFailed + " scopes:skip app_token:skip", calls: []string{}, via: "direct",
			resolve: []string{"slack.com"}, owner: sl.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.ResetRequests()
			rec.take()
			dns.take()
			targets := len(p.Targets())
			res, err := svc.CheckDraft(t.Context(), tc.draft)
			if err != nil || stepsOf(res) != tc.steps {
				t.Fatalf("steps %q, %v; want %q", stepsOf(res), err, tc.steps)
			}
			if got := calls(f); !slices.Equal(got, tc.calls) {
				t.Errorf("the fake got %q, want %q", got, tc.calls)
			}
			if got := dns.take(); !slices.Equal(got, tc.resolve) {
				t.Errorf("resolved %q, want %q", got, tc.resolve)
			}
			for _, v := range vias(res) {
				if v != tc.via {
					t.Errorf("paths %v, want %s", vias(res), tc.via)
				}
			}
			if got := p.Targets()[targets:]; tc.via == "proxy" && (len(got) == 0 || slices.ContainsFunc(got,
				func(s string) bool { return "http://"+s != f.URL() })) || tc.via == "direct" && len(got) != 0 {
				t.Errorf("the proxy connected to %v", got)
			}
			failed := slices.ContainsFunc(strings.Fields(tc.steps), func(s string) bool {
				return !strings.HasSuffix(s, ":ok") && !strings.HasSuffix(s, ":skip")
			})
			if res.OK == failed || (res.Slack != nil) != tc.slack || tc.slack && (res.BotName == "" ||
				res.Slack.TeamID == "" || res.Slack.BotUserID == "") {
				t.Errorf("ok %v, slack %+v, bot %q", res.OK, res.Slack, res.BotName)
			}
			for _, r := range rec.take() {
				if r.Links != (outbound.Links{Check: "draft_check", Run: res.Run}) || r.Owner.Connection != tc.owner ||
					r.Owner.Draft != (tc.owner == 0) {
					t.Errorf("a record of %+v, links %+v", r.Owner, r.Links)
				}
			}
		})
	}
	res, err := svc.CheckDraft(t.Context(), slDraft("socket", "", fakeslack.SecondBotToken,
		fakeslack.SecondAppToken, nil))
	b := fakeslack.Apps()[1]
	if err != nil || res.BotName != b.BotName || *res.Slack != (connections.SlackIdentity{TeamID: b.TeamID,
		TeamName: "Fake workspace B", BotUserID: b.BotUserID, AppID: b.AppID}) || res.Warnings != nil {
		t.Errorf("what a draft found = %+v %+v, %v", res.BotName, res.Slack, err)
	}
	for _, r := range f.Requests() {
		if r.Path == "/link/" {
			t.Errorf("a draft opened a socket")
		}
	}
}

// TestCheckSlackDraftOtherRoute: under muster dev the stored app-level token goes only with a bot token routed where
// the stored bot token is; a typed bot token routed to another fake Slack server checks there alone, with the step
// app_token skipped, and nothing reaches the fake of the saved Connection.
func TestCheckSlackDraftOtherRoute(t *testing.T) {
	e, f := newSlackEnv(t)
	other := fakeslack.New()
	if err := other.Start(t.Context(), "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close(context.WithoutCancel(t.Context())) })
	svc, _, dns := slackDraftService(t, e, f, func(token logging.Secret) string {
		switch string(token) {
		case fakeslack.BotToken:
			return f.URL() + "/api/"
		case fakeslack.SecondBotToken:
			return other.URL() + "/api/"
		}
		return ""
	})
	sl := e.createSlack(t, slackInput("sl", connections.EventModeSocket))
	f.ResetRequests()
	res, err := svc.CheckDraft(t.Context(), slDraft("socket", sl.PublicID, fakeslack.SecondBotToken, "", nil))
	if err != nil || stepsOf(res) != "token:ok scopes:ok app_token:skip" {
		t.Fatalf("steps %q, %v", stepsOf(res), err)
	}
	if got := calls(f); len(got) != 0 {
		t.Errorf("the fake of the saved Connection got %q", got)
	}
	if got := calls(other); !slices.Equal(got, []string{"POST /api/auth.test Bearer <sl-bot-b>"}) {
		t.Errorf("the other fake got %q", got)
	}
	if got := dns.take(); len(got) != 0 {
		t.Errorf("resolved %q", got)
	}
	res, err = svc.CheckDraft(t.Context(), slDraft("socket", sl.PublicID, fakeslack.BotToken, "", nil))
	if err != nil || stepsOf(res) != "token:ok scopes:ok app_token:ok" {
		t.Errorf("a bot token routed where the stored one is: %q, %v", stepsOf(res), err)
	}
}

// TestCheckSlackDraftStoresNothing is C-13.FR-14 and C-24.FR-2 for Slack: drafts of a new Slack Connection and of a
// saved one pass and write nothing — no Connection, no bot, workspace or app, no hint, no Audit log entry, no log line
// —; the saved Connection keeps its row; a draft of a saved Connection takes its limiter tokens from it, one of a new
// Connection none.
func TestCheckSlackDraftStoresNothing(t *testing.T) {
	e, f := newSlackEnv(t)
	svc, _, _ := slackDraftService(t, e, f)
	sl := e.createSlack(t, slackInput("sl", connections.EventModeSocket))
	rows := map[int64]dbgen.GetConnectionRow{}
	for id, r := range e.store.rows {
		rows[id] = r.row
	}
	e.path.subjects = nil
	audits, hints, ids, updates, logged := len(e.store.audit), len(e.store.hints), len(e.store.slackIDs),
		len(e.store.updates), e.log.Len()
	for _, d := range []connections.Draft{slDraft("socket", "", fakeslack.SecondBotToken, fakeslack.SecondAppToken,
		nil), slDraft("http", "", fakeslack.SecondBotToken, "", nil)} {
		if res, err := svc.CheckDraft(t.Context(), d); err != nil || !res.OK || res.Slack == nil {
			t.Fatalf("new %s = %q, %v", d.EventMode, stepsOf(res), err)
		}
	}
	for _, s := range e.path.subjects {
		if !s.NoLimiter || s.Connection != nil || s.Destination != nil {
			t.Errorf("a draft of a new Connection is limited by %+v", s)
		}
	}
	e.path.subjects = nil
	if res, err := svc.CheckDraft(t.Context(), slDraft("socket", sl.PublicID, "", "", nil)); err != nil || !res.OK {
		t.Fatalf("saved = %q, %v", stepsOf(res), err)
	}
	if len(e.path.subjects) != 4 || slices.ContainsFunc(e.path.subjects, func(s delivery.Subject) bool {
		return s.NoLimiter || s.Connection == nil || *s.Connection != sl.ID
	}) {
		t.Errorf("a draft of a saved Connection is limited by %+v", e.path.subjects)
	}
	if len(e.store.rows) != len(rows) || len(e.store.audit) != audits || len(e.store.hints) != hints ||
		len(e.store.slackIDs) != ids || len(e.store.updates) != updates {
		t.Errorf("a draft wrote: %d rows, %d audit, %d hints, %d identities, %d updates", len(e.store.rows)-len(rows),
			len(e.store.audit)-audits, len(e.store.hints)-hints, len(e.store.slackIDs)-ids,
			len(e.store.updates)-updates)
	}
	for id, r := range rows {
		if !reflect.DeepEqual(e.store.rows[id].row, r) {
			t.Errorf("the saved Connection %s changed", r.PublicID)
		}
	}
	if e.log.Len() != logged {
		t.Errorf("a draft logged: %s", tokenNames.Replace(e.log.String()[logged:]))
	}
}

// TestCheckSlackDraftRefusals is what a save of a Slack Connection would refuse, refused before any request: an event
// mode that is not socket or http, a bot token without xoxb-, an app-level token without xapp-, empty or null, and a
// connection_id of another type; and the failures of a stored secret that cannot be read, as errors that carry no
// token. A Slack draft needs no token, so the secret of its mode is not required.
func TestCheckSlackDraftRefusals(t *testing.T) {
	e, f := newSlackEnv(t)
	svc, _, _ := slackDraftService(t, e, f)
	sl := e.createSlack(t, slackInput("sl", connections.EventModeSocket))
	mm := e.create(t, "mm")
	f.ResetRequests()
	for _, tc := range []struct {
		name          string
		draft         connections.Draft
		pointer, code string
	}{
		{"event mode", slDraft("smoke", "", fakeslack.BotToken, "", nil), "/event_mode", connections.CodeInvalidFormat},
		{"no event mode", slDraft("", sl.PublicID, "", "", nil), "/event_mode", connections.CodeInvalidFormat},
		{"bot token", slDraft("socket", "", fakeslack.AppToken, "", nil), "/bot_token", connections.CodeInvalidFormat},
		{"app-level token", slDraft("socket", "", fakeslack.BotToken, fakeslack.SecondBotToken, nil), "/app_token",
			connections.CodeInvalidFormat},
		{"empty bot token", connections.Draft{Type: connections.TypeSlack, EventMode: "socket",
			BotToken: keyring.Replace("")}, "/bot_token", connections.CodeTooShort},
		{"empty app-level token", connections.Draft{Type: connections.TypeSlack, EventMode: "socket",
			AppToken: keyring.Replace("")}, "/app_token", connections.CodeTooShort},
		{"null app-level token", connections.Draft{Type: connections.TypeSlack, EventMode: "socket",
			AppToken: keyring.Clear}, "/app_token", connections.CodeRequired},
		{"a mattermost id", slDraft("socket", mm.PublicID, "", "", nil), "/connection_id", connections.CodeUnknownID},
		{"a slack id as mattermost", of(mmDraft(e.fake.URL()), sl.PublicID, ""), "/connection_id",
			connections.CodeUnknownID},
		{"proxy", slDraft("socket", sl.PublicID, "", "", &proxyconf.Input{Enabled: true, Type: new("http")}),
			"/proxy/address", connections.CodeRequired},
	} {
		_, err := svc.CheckDraft(t.Context(), tc.draft)
		if fe := fieldError(err); fe == nil || fe.Pointer != tc.pointer || fe.Code != tc.code {
			t.Errorf("%s = %v", tc.name, err)
		}
	}
	if n := len(f.Requests()); n != 0 {
		t.Errorf("a refused draft made %d requests", n)
	}
	row := &e.store.rows[sl.ID].row
	keep := *row
	row.SlackAppTokenCiphertext = []byte("garbage")
	if _, err := svc.CheckDraft(t.Context(), slDraft("socket", sl.PublicID, "", "", nil)); err == nil ||
		strings.Contains(err.Error(), fakeslack.AppToken) {
		t.Errorf("an unreadable app-level token = %s", tokenNames.Replace(fmt.Sprint(err)))
	}
	if res, err := svc.CheckDraft(t.Context(), slDraft("http", sl.PublicID, "", "", nil)); err != nil || !res.OK {
		t.Errorf("http mode opened the unreadable app-level token: %q, %v", stepsOf(res), err)
	}
	*row = keep
	row.BotTokenCiphertext = []byte("garbage")
	for _, d := range []connections.Draft{slDraft("socket", sl.PublicID, "", "", nil), slDraft("socket", sl.PublicID,
		"", "", &proxyconf.Input{Enabled: true, Type: new("http"), Address: new("127.0.0.1:1")}),
		slDraft("socket", sl.PublicID, fakeslack.SecondBotToken, "", nil)} {
		if _, err := svc.CheckDraft(t.Context(), d); err == nil || strings.Contains(err.Error(), fakeslack.BotToken) {
			t.Errorf("an unreadable bot token under muster dev = %s", tokenNames.Replace(fmt.Sprint(err)))
		}
	}
	if res, err := svc.CheckDraft(t.Context(), slDraft("socket", sl.PublicID, fakeslack.SecondBotToken,
		fakeslack.SecondAppToken, &proxyconf.Input{Enabled: true, Type: new("http"), Address: new("127.0.0.1:1")})); err != nil || res.OK {
		t.Errorf("typed tokens opened the unreadable one: %q, %v", stepsOf(res), err)
	}
	*row = keep
	row.Proxy = []byte(`{"enabled":true,"type":"ftp","address":"127.0.0.1:1"}`)
	if _, err := svc.CheckDraft(t.Context(), slDraft("socket", sl.PublicID, "", "", nil)); err == nil {
		t.Error("a proxy the client refuses")
	}
	*row = keep
	e.path.limited = true
	for _, d := range []connections.Draft{slDraft("socket", sl.PublicID, "", "", &proxyconf.Input{Enabled: true,
		Type: new("http"), Address: new("127.0.0.1:1")}), slDraft("socket", "", fakeslack.SecondBotToken, "", nil)} {
		if _, err := svc.CheckDraft(t.Context(), d); !isLimited(err) {
			t.Errorf("a limited draft = %v", err)
		}
	}
	e.path.limited = false
}

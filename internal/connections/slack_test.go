// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package connections_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/connections"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/fakes/fakeproxy"
	"github.com/muster-io/muster/internal/fakes/fakeslack"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/proxyconf"
	"github.com/muster-io/muster/internal/slack"
)

// slackSecrets are the values no log line, error or Audit log entry of a Slack test may show.
var slackSecrets = []string{fakeslack.BotToken, fakeslack.AppToken, fakeslack.SigningSecret, fakeslack.SecondBotToken,
	fakeslack.SecondAppToken, fakeslack.SecondSigningSecret}

// newSlackEnv is newEnv with a fake Slack server that the Slack Connections' requests go to instead of Slack.
func newSlackEnv(t *testing.T) (*env, *fakeslack.Fake) {
	t.Helper()
	e := newEnv(t)
	f := fakeslack.New()
	if err := f.Start(t.Context(), "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close(context.WithoutCancel(t.Context())) })
	e.slackBase = f.URL() + "/api/"
	t.Cleanup(func() {
		for _, s := range slackSecrets {
			if strings.Contains(e.log.String(), s) {
				t.Errorf("a Slack secret reached the log: %s", e.log.String())
			}
		}
		for _, a := range e.store.audit {
			for _, s := range slackSecrets {
				if strings.Contains(string(a.Diff), s) || strings.Contains(string(a.Details), s) {
					t.Errorf("a Slack secret reached the Audit log: %s", a.Diff)
				}
			}
		}
	})
	return e, f
}

// slackInput is a Slack Connection in mode with the first app's bot token, and its app-level token in socket mode or
// its Slack signing secret in http mode.
func slackInput(name, mode string) connections.Input {
	in := connections.Input{Type: connections.TypeSlack, Name: name, EventMode: mode,
		BotToken: keyring.Replace(fakeslack.BotToken),
		Limiter:  connections.Limiter{Limit: connections.SlackDefaultLimit, PerSeconds: connections.SlackDefaultPerSeconds}}
	if mode == connections.EventModeSocket {
		in.AppToken = keyring.Replace(fakeslack.AppToken)
	} else {
		in.SlackSigningSecret = keyring.Replace(fakeslack.SigningSecret)
	}
	return in
}

func (e *env) createSlack(t *testing.T, in connections.Input) connections.Connection {
	t.Helper()
	c, err := e.svc.Create(t.Context(), by, in)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestSlackCreate is C-24.FR-1: a Slack Connection with its name, write-only bot token, event mode, app-level token
// and Slack signing secret, proxy and limiter; each secret is encrypted with the associated data of its own field and
// shown only as its status; the read-only callback address is MUSTER_INGEST_URL/api/v1/callbacks/slack/<public_id>;
// nothing is learned before a check, and the Audit log shows the secrets only as secret_changed.
func TestSlackCreate(t *testing.T) {
	e, _ := newSlackEnv(t)
	in := slackInput("sl", connections.EventModeSocket)
	in.SlackSigningSecret = keyring.Replace(fakeslack.SigningSecret)
	c := e.createSlack(t, in)
	if !strings.HasPrefix(c.PublicID, "CN") || c.Type != connections.TypeSlack || c.Name != "sl" ||
		c.EventMode != connections.EventModeSocket || !c.BotToken.Set || !c.AppToken.Set || !c.SlackSigningSecret.Set ||
		c.ServerURL != "" || c.BotAPIBaseURL != "" || c.BridgeBaseURL != "" || c.EventDelivery != "" ||
		c.BotUsername != nil || c.BotUserID != nil || c.SlackTeamID != nil || c.SlackTeamName != nil ||
		c.SlackAppID != nil || c.Limiter.Limit != 50 || c.Limiter.PerSeconds != 60 || c.Version != 1 {
		t.Errorf("created %+v", c)
	}
	if got := e.svc.SlackCallbackURL(c.PublicID); got != "http://localhost:8081/api/v1/callbacks/slack/"+c.PublicID {
		t.Errorf("callback = %q", got)
	}
	row := e.store.rows[c.ID].row
	for _, tc := range []struct {
		field, keyID string
		ct           []byte
		want         string
	}{
		{"connections.slack_app_token", row.SlackAppTokenKeyID.String, row.SlackAppTokenCiphertext, fakeslack.AppToken},
		{"connections.slack_signing_secret", row.SlackSigningSecretKeyID.String, row.SlackSigningSecretCiphertext,
			fakeslack.SigningSecret},
		{"connections.bot_token", row.BotTokenKeyID.String, row.BotTokenCiphertext, fakeslack.BotToken},
	} {
		if plain, err := e.keys.Decrypt(tc.field, tc.keyID, tc.ct); err != nil || string(plain) != tc.want {
			t.Errorf("%s = %v", tc.field, err)
		}
		if _, err := e.keys.Decrypt("connections.proxy_password", tc.keyID, tc.ct); err == nil {
			t.Errorf("%s decrypts with the associated data of another field", tc.field)
		}
	}
	if row.SlackEventMode.String != "socket" || !row.SlackAppTokenUpdatedAt.Valid ||
		!row.SlackSigningSecretUpdatedAt.Valid {
		t.Errorf("row %+v", row)
	}
	a := e.store.audit[0]
	d := diffOf(t, a)
	if a.Action != connections.ActionCreated || !secretChanged(d, "/bot_token") || !secretChanged(d, "/app_token") ||
		!secretChanged(d, "/slack_signing_secret") || !strings.Contains(string(a.Diff), `"/event_mode","after":"socket"`) {
		t.Errorf("audit %s %s", a.Action, a.Diff)
	}

	h := e.createSlack(t, slackInput("sl-http", connections.EventModeHTTP))
	if h.EventMode != connections.EventModeHTTP || h.AppToken.Set || !h.SlackSigningSecret.Set {
		t.Errorf("http = %+v", h)
	}
	if d := diffOf(t, e.store.audit[1]); secretChanged(d, "/app_token") || !secretChanged(d, "/slack_signing_secret") {
		t.Errorf("http audit %s", e.store.audit[1].Diff)
	}
	got, err := e.svc.Get(t.Context(), h.PublicID)
	if err != nil || got.EventMode != connections.EventModeHTTP || got.AppToken.Set || !got.SlackSigningSecret.Set {
		t.Errorf("get = %+v, %v", got, err)
	}
	page, err := e.svc.List(t.Context(), connections.ListFilter{Type: new(connections.TypeSlack), Limit: 10})
	if err != nil || len(page.Connections) != 2 {
		t.Errorf("list = %+v, %v", page, err)
	}
}

// TestSlackValidation is C-24.FR-1: the prefixes of the tokens, the secret each event mode needs and the event mode,
// refused at the field's pointer with a detail that never shows a value; nothing is stored.
func TestSlackValidation(t *testing.T) {
	e, _ := newSlackEnv(t)
	cases := []struct {
		name    string
		mode    string
		change  func(in *connections.Input)
		pointer string
		code    string
	}{
		{"no bot token", connections.EventModeSocket, func(in *connections.Input) { in.BotToken = keyring.Keep },
			"/bot_token", connections.CodeRequired},
		{"a user token", connections.EventModeSocket,
			func(in *connections.Input) { in.BotToken = keyring.Replace("xoxp-muster-user-token") }, "/bot_token",
			connections.CodeInvalidFormat},
		{"an app-level token as the bot token", connections.EventModeHTTP,
			func(in *connections.Input) { in.BotToken = keyring.Replace(fakeslack.AppToken) }, "/bot_token",
			connections.CodeInvalidFormat},
		{"empty bot token", connections.EventModeSocket, func(in *connections.Input) { in.BotToken = keyring.Replace("") },
			"/bot_token", connections.CodeTooShort},
		{"a bot token as the app-level token", connections.EventModeSocket,
			func(in *connections.Input) { in.AppToken = keyring.Replace(fakeslack.BotToken) }, "/app_token",
			connections.CodeInvalidFormat},
		{"a wrong app-level token in http mode", connections.EventModeHTTP,
			func(in *connections.Input) { in.AppToken = keyring.Replace("bad-muster-app-token") }, "/app_token",
			connections.CodeInvalidFormat},
		{"socket without app-level token", connections.EventModeSocket,
			func(in *connections.Input) { in.AppToken = keyring.Keep }, "/app_token", connections.CodeRequired},
		{"socket with a cleared app-level token", connections.EventModeSocket,
			func(in *connections.Input) { in.AppToken = keyring.Clear }, "/app_token", connections.CodeRequired},
		{"empty app-level token", connections.EventModeSocket,
			func(in *connections.Input) { in.AppToken = keyring.Replace("") }, "/app_token", connections.CodeTooShort},
		{"http without Slack signing secret", connections.EventModeHTTP,
			func(in *connections.Input) { in.SlackSigningSecret = keyring.Keep }, "/slack_signing_secret",
			connections.CodeRequired},
		{"http with a cleared Slack signing secret", connections.EventModeHTTP,
			func(in *connections.Input) { in.SlackSigningSecret = keyring.Clear }, "/slack_signing_secret",
			connections.CodeRequired},
		{"empty Slack signing secret", connections.EventModeHTTP,
			func(in *connections.Input) { in.SlackSigningSecret = keyring.Replace("") }, "/slack_signing_secret",
			connections.CodeTooShort},
		{"empty Slack signing secret in socket mode", connections.EventModeSocket,
			func(in *connections.Input) { in.SlackSigningSecret = keyring.Replace("") }, "/slack_signing_secret",
			connections.CodeTooShort},
		{"unknown event mode", connections.EventModeSocket, func(in *connections.Input) { in.EventMode = "poll" },
			"/event_mode", connections.CodeInvalidFormat},
		{"no event mode", connections.EventModeSocket, func(in *connections.Input) { in.EventMode = "" },
			"/event_mode", connections.CodeInvalidFormat},
	}
	for _, tc := range cases {
		in := slackInput("sl", tc.mode)
		tc.change(&in)
		_, err := e.svc.Create(t.Context(), by, in)
		fe := fieldError(err)
		if fe == nil || fe.Pointer != tc.pointer || fe.Code != tc.code || fe.Detail == "" {
			t.Errorf("%s = %v", tc.name, err)
			continue
		}
		for _, s := range append(slices.Clone(slackSecrets), "xoxp-muster-user-token", "bad-muster-app-token") {
			if strings.Contains(fe.Error(), s) {
				t.Errorf("%s shows a secret: %v", tc.name, fe)
			}
		}
	}
	if len(e.store.rows) != 0 || len(e.store.audit) != 0 {
		t.Errorf("an invalid Slack Connection was written: %d rows", len(e.store.rows))
	}
	in := slackInput("sl", connections.EventModeSocket)
	in.ServerURL, in.BotAPIBaseURL, in.BridgeBaseURL = "http://mm.example.org", "http://tg.example.org", "http://b"
	c := e.createSlack(t, in)
	if c.ServerURL != "" || c.BotAPIBaseURL != "" || c.BridgeBaseURL != "" {
		t.Errorf("a Slack Connection kept another type's address: %+v", c)
	}
	m := e.create(t, "mm")
	if m.EventMode != "" || m.AppToken.Set || m.SlackSigningSecret.Set {
		t.Errorf("a Mattermost Connection has Slack fields: %+v", m)
	}
}

// TestSlackUpdate is C-24.FR-1: a change of the event mode keeps the other mode's stored secret, and needs the secret
// of the new mode sent or stored; each replaced secret is secret_changed in the Audit log; a new bot token forgets what
// a check learned — the bot, the workspace and the app; an update that changes nothing writes nothing; the type cannot
// change.
func TestSlackUpdate(t *testing.T) {
	e, _ := newSlackEnv(t)
	ctx := t.Context()
	c := e.createSlack(t, slackInput("sl", connections.EventModeSocket))
	if res, err := e.svc.Check(ctx, c.PublicID, nil); err != nil || !res.OK {
		t.Fatalf("check = %+v, %v", res, err)
	}

	in := slackInput("sl", connections.EventModeHTTP)
	in.BotToken, in.SlackSigningSecret = keyring.Keep, keyring.Keep
	if fe := fieldError(e.update(t, c.PublicID, in)); fe == nil || fe.Pointer != "/slack_signing_secret" ||
		fe.Code != connections.CodeRequired {
		t.Errorf("http without a Slack signing secret = %v", fe)
	}
	in.SlackSigningSecret = keyring.Replace(fakeslack.SigningSecret)
	nAudit := len(e.store.audit)
	if err := e.update(t, c.PublicID, in); err != nil {
		t.Fatal(err)
	}
	got, _ := e.svc.Get(ctx, c.PublicID)
	if got.EventMode != connections.EventModeHTTP || !got.AppToken.Set || !got.SlackSigningSecret.Set ||
		got.SlackTeamID == nil || got.SlackAppID == nil || got.BotUsername == nil {
		t.Errorf("socket to http = %+v", got)
	}
	d := diffOf(t, e.store.audit[nAudit])
	if !secretChanged(d, "/slack_signing_secret") || secretChanged(d, "/app_token") || secretChanged(d, "/bot_token") ||
		!slices.ContainsFunc(d, func(c audit.Change) bool { return c.Pointer == "/event_mode" }) {
		t.Errorf("socket to http diff %s", e.store.audit[nAudit].Diff)
	}

	in = slackInput("sl", connections.EventModeSocket)
	in.BotToken, in.AppToken = keyring.Keep, keyring.Keep
	if err := e.update(t, c.PublicID, in); err != nil {
		t.Fatal(err)
	}
	got, _ = e.svc.Get(ctx, c.PublicID)
	if got.EventMode != connections.EventModeSocket || !got.AppToken.Set || !got.SlackSigningSecret.Set {
		t.Errorf("http to socket = %+v", got)
	}
	nUpdates := len(e.store.updates)
	if err := e.update(t, c.PublicID, in); err != nil || len(e.store.updates) != nUpdates {
		t.Errorf("an update that changes nothing wrote: %v, %d updates", err, len(e.store.updates)-nUpdates)
	}

	in.AppToken = keyring.Replace(fakeslack.AppToken)
	nAudit = len(e.store.audit)
	if err := e.update(t, c.PublicID, in); err != nil {
		t.Fatal(err)
	}
	if d := diffOf(t, e.store.audit[nAudit]); !secretChanged(d, "/app_token") || secretChanged(d, "/bot_token") {
		t.Errorf("a new app-level token diff %s", e.store.audit[nAudit].Diff)
	}
	got, _ = e.svc.Get(ctx, c.PublicID)
	if got.SlackTeamID == nil || got.SlackAppID == nil {
		t.Errorf("a new app-level token forgot the workspace: %+v", got)
	}

	in.AppToken, in.BotToken = keyring.Keep, keyring.Replace(fakeslack.SecondBotToken)
	if err := e.update(t, c.PublicID, in); err != nil {
		t.Fatal(err)
	}
	got, _ = e.svc.Get(ctx, c.PublicID)
	if got.BotUsername != nil || got.BotUserID != nil || got.SlackTeamID != nil || got.SlackTeamName != nil ||
		got.SlackAppID != nil || e.store.rows[c.ID].row.SlackTeamUrl.Valid || e.store.rows[c.ID].row.SlackBotID.Valid {
		t.Errorf("a new bot token kept what a check learned: %+v", got)
	}

	in.BotToken = keyring.Replace("xoxp-muster-user-token")
	if fe := fieldError(e.update(t, c.PublicID, in)); fe == nil || fe.Pointer != "/bot_token" ||
		fe.Code != connections.CodeInvalidFormat {
		t.Errorf("a user token = %v", fe)
	}
	in.BotToken, in.SlackSigningSecret = keyring.Keep, keyring.Replace("")
	if fe := fieldError(e.update(t, c.PublicID, in)); fe == nil || fe.Pointer != "/slack_signing_secret" ||
		fe.Code != connections.CodeTooShort {
		t.Errorf("an empty Slack signing secret = %v", fe)
	}
	in.SlackSigningSecret, in.AppToken = keyring.Keep, keyring.Replace("")
	if fe := fieldError(e.update(t, c.PublicID, in)); fe == nil || fe.Pointer != "/app_token" ||
		fe.Code != connections.CodeTooShort {
		t.Errorf("an empty app-level token = %v", fe)
	}
	mm := input("sl", "http://mm.example.org")
	if fe := fieldError(e.update(t, c.PublicID, mm)); fe == nil || fe.Pointer != "/type" {
		t.Errorf("a change of type = %v", fe)
	}
}

// TestSlackCheck is C-24.FR-2 and C-24.AC-1 against the fake Slack server: the steps token, scopes, which makes no
// request, and in socket mode app_token, each through the interactive path limited by the Connection; a passing check
// stores the workspace, the bot and the app for the version it read; a revoked token, a person's token, a missing scope
// and an app-level token of another app fail their step with their code and text and store nothing; http mode has no
// step app_token and keeps the stored app.
func TestSlackCheck(t *testing.T) {
	e, f := newSlackEnv(t)
	ctx := t.Context()
	a := fakeslack.Apps()[0]
	c := e.createSlack(t, slackInput("sl", connections.EventModeSocket))
	nHints := len(e.store.hints)
	res, err := e.svc.Check(ctx, c.PublicID, nil)
	if err != nil || !res.OK || len(res.Steps) != 3 || res.BotName != a.BotName || res.Bridge || res.Info != nil ||
		res.Slack == nil || *res.Slack != (connections.SlackIdentity{TeamID: a.TeamID, TeamName: a.TeamName,
		BotUserID: a.BotUserID, AppID: a.AppID}) || res.Run == "" {
		t.Fatalf("check = %+v, %v", res, err)
	}
	for i, name := range []string{connections.StepToken, connections.StepScopes, connections.StepAppToken} {
		st := res.Steps[i]
		if st.Name != name || !st.OK || st.Skipped || st.Via != slack.ViaDirect || st.Message != "" || st.Code != "" ||
			st.Called() != (name != connections.StepScopes) {
			t.Errorf("step %d = %+v", i, st)
		}
	}
	if res.Steps[0].Latency != 7*time.Millisecond || res.Steps[2].Latency != 14*time.Millisecond {
		t.Errorf("latencies %v %v", res.Steps[0].Latency, res.Steps[2].Latency)
	}
	reqs := f.Requests()
	if len(reqs) != 3 || reqs[0].Path != "/api/auth.test" ||
		reqs[0].Headers["Authorization"][0] != "Bearer "+fakeslack.BotToken || reqs[1].Path != "/api/auth.test" ||
		reqs[1].Headers["Authorization"][0] != "Bearer "+fakeslack.AppToken || reqs[2].Path != "/api/bots.info" ||
		reqs[2].Headers["Authorization"][0] != "Bearer "+fakeslack.BotToken {
		t.Errorf("requests %+v", reqs)
	}
	if len(e.path.subjects) != 3 || slices.ContainsFunc(e.path.subjects, func(s delivery.Subject) bool {
		return s.Connection == nil || *s.Connection != c.ID || s.Destination != nil
	}) {
		t.Errorf("subjects %+v", e.path.subjects)
	}
	if len(e.store.slackIDs) != 1 || e.store.slackIDs[0].Version != 1 || e.store.slackIDs[0].TeamUrl.String != a.TeamURL ||
		e.store.slackIDs[0].BotID.String != a.BotID || len(e.store.hints) != nHints+1 {
		t.Errorf("stored %+v, %d hints", e.store.slackIDs, len(e.store.hints)-nHints)
	}
	got, _ := e.svc.Get(ctx, c.PublicID)
	if got.BotUsername == nil || *got.BotUsername != a.BotName || got.BotUserID == nil || *got.BotUserID != a.BotUserID ||
		got.SlackTeamID == nil || *got.SlackTeamID != a.TeamID || got.SlackTeamName == nil ||
		*got.SlackTeamName != a.TeamName || got.SlackAppID == nil || *got.SlackAppID != a.AppID || got.Version != 1 {
		t.Errorf("learned %+v", got)
	}

	failing := func(name string, cfg fakeslack.Config, step int, code, message string) {
		t.Helper()
		if err := f.SetConfig(cfg); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.SetConfig(fakeslack.DefaultConfig()) }()
		n := len(e.store.slackIDs)
		res, err := e.svc.Check(ctx, c.PublicID, nil)
		if err != nil || res.OK || len(res.Steps) != 3 || res.Steps[step].OK || res.Steps[step].Code != code ||
			res.Steps[step].Message != message || len(e.store.slackIDs) != n {
			t.Errorf("%s = %+v, %v", name, res, err)
			return
		}
		for _, st := range res.Steps[step+1:] {
			if !st.Skipped || st.Called() {
				t.Errorf("%s: a step after the failure ran: %+v", name, st)
			}
		}
		if (step == 0) != (res.Slack == nil) {
			t.Errorf("%s: slack = %+v", name, res.Slack)
		}
	}
	cfg := fakeslack.DefaultConfig()
	cfg.Revoked = []string{fakeslack.BotToken}
	failing("revoked", cfg, 0, slack.CodeTokenInvalid, "Slack refused the token.")
	cfg = fakeslack.DefaultConfig()
	cfg.UserTokens = []string{fakeslack.BotToken}
	failing("a person's token", cfg, 0, slack.CodeWrongTokenType, "This is not a bot token.")
	cfg = fakeslack.DefaultConfig()
	cfg.Scopes[fakeslack.AppA] = "chat:write,channels:read,groups:read,im:write,users:read"
	failing("a missing scope", cfg, 1, slack.CodeMissingScope, "The bot token lacks the scopes users:read.email.")
	cfg = fakeslack.DefaultConfig()
	cfg.AppOf = map[string]string{fakeslack.AppToken: fakeslack.AppB}
	failing("another app", cfg, 2, slack.CodeAppMismatch,
		"The app-level token belongs to another Slack app than the bot token.")
	cfg = fakeslack.DefaultConfig()
	cfg.Revoked = []string{fakeslack.AppToken}
	failing("a revoked app-level token", cfg, 2, slack.CodeTokenInvalid, "Slack refused the token.")

	in := slackInput("sl", connections.EventModeHTTP)
	in.BotToken = keyring.Keep
	if err := e.update(t, c.PublicID, in); err != nil {
		t.Fatal(err)
	}
	res, err = e.svc.Check(ctx, c.PublicID, nil)
	if err != nil || !res.OK || len(res.Steps) != 2 || res.Steps[1].Name != connections.StepScopes ||
		res.Slack == nil || res.Slack.AppID != "" || res.Slack.TeamID != a.TeamID {
		t.Fatalf("http = %+v, %v", res, err)
	}
	last := e.store.slackIDs[len(e.store.slackIDs)-1]
	got, _ = e.svc.Get(ctx, c.PublicID)
	if last.AppID.Valid || last.Version != 2 || got.SlackAppID == nil || *got.SlackAppID != a.AppID {
		t.Errorf("http kept the app: %+v, %+v", last, got)
	}
}

// TestSlackCheckRefused: a check stores nothing for a Connection that changed since it was read; no limiter token in
// time is a *delivery.LimitedError; a baseURL is refused; a failed store, an unreadable secret or proxy fail the check
// without showing a secret.
func TestSlackCheckRefused(t *testing.T) {
	e, _ := newSlackEnv(t)
	ctx := t.Context()
	c := e.createSlack(t, slackInput("sl", connections.EventModeSocket))
	next := e.path.next
	e.path.next = interactiveFunc(func(ctx context.Context, s delivery.Subject, op delivery.Op) (delivery.Outcome,
		error) {
		e.store.rows[c.ID].row.Version = 5
		return next.Do(ctx, s, op)
	})
	nHints := len(e.store.hints)
	res, err := e.svc.Check(ctx, c.PublicID, nil)
	got, _ := e.svc.Get(ctx, c.PublicID)
	if err != nil || !res.OK || res.Slack == nil || len(e.store.slackIDs) != 1 || e.store.slackIDs[0].Version != 1 ||
		got.SlackTeamID != nil || got.BotUsername != nil || len(e.store.hints) != nHints {
		t.Errorf("a changed Connection = %+v, %v, %+v", res, err, got)
	}
	e.path.next = next

	base := "http://127.0.0.1:1"
	if fe := fieldError(func() error { _, err := e.svc.Check(ctx, c.PublicID, &base); return err }()); fe == nil ||
		fe.Pointer != "/base_url" || fe.Code != connections.CodeUnsupported {
		t.Errorf("a base URL = %v", fe)
	}
	e.path.limited = true
	if _, err := e.svc.Check(ctx, c.PublicID, nil); !isLimited(err) {
		t.Errorf("limited = %v", err)
	}
	e.path.limited = false
	e.store.fail["SetSlackIdentity"] = errors.New("down")
	if _, err := e.svc.Check(ctx, c.PublicID, nil); err == nil {
		t.Error("a failed record of the workspace")
	}
	delete(e.store.fail, "SetSlackIdentity")

	row := &e.store.rows[c.ID].row
	for name, spoil := range map[string]func() func(){
		"bot token": func() func() {
			v := row.BotTokenCiphertext
			row.BotTokenCiphertext = []byte("garbage")
			return func() { row.BotTokenCiphertext = v }
		},
		"app-level token": func() func() {
			v := row.SlackAppTokenCiphertext
			row.SlackAppTokenCiphertext = []byte("garbage")
			return func() { row.SlackAppTokenCiphertext = v }
		},
		"slack signing secret": func() func() {
			v, k := row.SlackSigningSecretCiphertext, row.SlackSigningSecretKeyID
			row.SlackSigningSecretCiphertext, row.SlackSigningSecretKeyID = []byte("garbage"),
				pgtype.Text{String: row.BotTokenKeyID.String, Valid: true}
			return func() { row.SlackSigningSecretCiphertext, row.SlackSigningSecretKeyID = v, k }
		},
		"proxy password": func() func() {
			v, k := row.ProxyPasswordCiphertext, row.ProxyPasswordKeyID
			row.ProxyPasswordCiphertext, row.ProxyPasswordKeyID = []byte("garbage"),
				pgtype.Text{String: row.BotTokenKeyID.String, Valid: true}
			return func() { row.ProxyPasswordCiphertext, row.ProxyPasswordKeyID = v, k }
		},
		"proxy": func() func() {
			v := row.Proxy
			row.Proxy = []byte("{")
			return func() { row.Proxy = v }
		},
	} {
		restore := spoil()
		_, err := e.svc.Check(ctx, c.PublicID, nil)
		restore()
		if err == nil || slices.ContainsFunc(slackSecrets, func(s string) bool { return strings.Contains(err.Error(), s) }) {
			t.Errorf("an unreadable %s = %v", name, err)
		}
	}
	if res, err := e.svc.Check(ctx, c.PublicID, nil); err != nil || !res.OK {
		t.Errorf("after restoring = %+v, %v", res, err)
	}
}

// TestSlackCheckThroughProxy is C-24.AC-3: with an HTTP proxy on the Slack Connection, the check reports the path proxy
// and every request of the check reaches the fake Slack server only through the proxy.
func TestSlackCheckThroughProxy(t *testing.T) {
	e, f := newSlackEnv(t)
	p, err := fakeproxy.Start(t.Context(), fakeproxy.HTTP, "127.0.0.1:0", fakeproxy.Options{Username: "muster",
		Password: "proxy-pass"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	in := slackInput("sl", connections.EventModeSocket)
	in.Proxy = proxyconf.Input{Enabled: true, Type: new("http"), Address: new(p.Addr()), UsernameSet: true,
		Username: new("muster"), Password: keyring.Replace("proxy-pass")}
	c := e.createSlack(t, in)
	res, err := e.svc.Check(t.Context(), c.PublicID, nil)
	if err != nil || !res.OK || slices.ContainsFunc(res.Steps, func(s connections.Step) bool {
		return s.Via != slack.ViaProxy
	}) {
		t.Fatalf("check = %+v, %v", res, err)
	}
	reqs, targets := f.Requests(), p.Targets()
	if len(reqs) != 3 || len(targets) != len(reqs) || p.Refused() != 0 {
		t.Errorf("%d requests reached the fake, %d through the proxy, %d refused", len(reqs), len(targets),
			p.Refused())
	}
	for _, tg := range targets {
		if "http://"+tg != f.URL() {
			t.Errorf("the proxy connected to %s", tg)
		}
	}
}

// TestSlackChannelsDoctorAndDelete: a Slack Connection has no channel list yet, muster doctor skips it, and its
// deletion follows C-13.FR-6: it is marked deleted with its three secrets wiped and the Audit log entry
// connection.deleted.
func TestSlackChannelsDoctorAndDelete(t *testing.T) {
	e, _ := newSlackEnv(t)
	ctx := t.Context()
	in := slackInput("sl", connections.EventModeSocket)
	in.SlackSigningSecret = keyring.Replace(fakeslack.SigningSecret)
	c := e.createSlack(t, in)
	if _, err := e.svc.Channels(ctx, c.PublicID, "", ""); !errors.Is(err, connections.ErrNotMattermost) {
		t.Errorf("channels = %v", err)
	}
	e.create(t, "mm")
	found, err := e.svc.Doctor(ctx, e.store, time.Second)
	if err != nil || len(found) != 1 || found[0].Name != "mm" {
		t.Errorf("doctor = %+v, %v", found, err)
	}
	if err := e.svc.Delete(ctx, by, c.PublicID, new(int64(1))); err != nil {
		t.Fatal(err)
	}
	r := e.store.rows[c.ID]
	if !r.deleted || r.row.BotTokenCiphertext != nil || r.row.SlackAppTokenCiphertext != nil ||
		r.row.SlackAppTokenKeyID.Valid || r.row.SlackSigningSecretCiphertext != nil || r.row.SlackSigningSecretKeyID.Valid ||
		len(e.abandon) != 1 || e.abandon[0] != c.ID {
		t.Errorf("deleted %+v, abandon %v", r, e.abandon)
	}
	if a := e.store.audit[len(e.store.audit)-1]; a.Action != connections.ActionDeleted {
		t.Errorf("audit %s", a.Action)
	}
	if _, err := e.svc.Get(ctx, c.PublicID); !errors.Is(err, connections.ErrNotFound) {
		t.Errorf("get after delete = %v", err)
	}
}

// TestSlackDemo is C-01.FR-13: the demo Slack Connection of muster dev in socket mode with the fake Slack server's
// first app, its limiter at connection.slack.limiter, created once, with both tokens secret_changed in its Audit log
// entry.
func TestSlackDemo(t *testing.T) {
	e, _ := newSlackEnv(t)
	d := connections.Demo{Type: connections.TypeSlack, Name: "Dev Slack", BotToken: logging.Secret(fakeslack.BotToken),
		AppToken: logging.Secret(fakeslack.AppToken)}
	for range 2 {
		if err := e.svc.EnsureDemo(t.Context(), d); err != nil {
			t.Fatal(err)
		}
	}
	page, err := e.svc.List(t.Context(), connections.ListFilter{Limit: 10})
	if err != nil || len(page.Connections) != 1 {
		t.Fatalf("list = %+v, %v", page, err)
	}
	c := page.Connections[0]
	if c.Type != connections.TypeSlack || c.Name != "Dev Slack" || c.EventMode != connections.EventModeSocket ||
		!c.BotToken.Set || !c.AppToken.Set || c.SlackSigningSecret.Set || c.Limiter.Limit != 50 ||
		c.Limiter.PerSeconds != 60 {
		t.Errorf("demo %+v", c)
	}
	if len(e.store.audit) != 1 {
		t.Fatalf("%d audit entries", len(e.store.audit))
	}
	a := e.store.audit[0]
	diff := diffOf(t, a)
	if !secretChanged(diff, "/bot_token") || !secretChanged(diff, "/app_token") ||
		secretChanged(diff, "/slack_signing_secret") || !strings.Contains(string(a.Details), "development_demo") {
		t.Errorf("demo audit %s %s", a.Diff, a.Details)
	}
	if res, err := e.svc.Check(t.Context(), c.PublicID, nil); err != nil || !res.OK {
		t.Errorf("demo check = %+v, %v", res, err)
	}
}

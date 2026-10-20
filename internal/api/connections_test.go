// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/connections"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/mattermost"
	"github.com/muster-io/muster/internal/proxyconf"
)

const (
	connectionsWriter = "mstr_pat_connections_write"
	connectionsReader = "mstr_pat_connections_read"
	connectionID      = "CNAAAAAAAAAAA1"
	// botTokenValue is the bot token of the create and update bodies, which no answer may carry.
	botTokenValue = "mm-api-bot-token-secret-42"
)

const connectionBody = `{"type":"mattermost","name":"mm","server_url":"http://127.0.0.1:18065",` +
	`"bot_token":"` + botTokenValue + `","proxy":{"enabled":false},"limiter":{"limit":5,"per_seconds":1}}`

// fakeConnections stands for internal/connections: the Connections in id order, the inputs of the writes with who
// asked and the version they named, the drafts checked, and the answers of the checks and the channel list.
type fakeConnections struct {
	list     []connections.Connection
	filters  []connections.ListFilter
	inputs   []connections.Input
	versions []*int64
	by       []connections.Requester
	deleted  []string
	check    connections.CheckResult
	channels []connections.Channel
	chanArgs [][2]string
	baseURLs []*string
	drafts   []connections.Draft
	err      error
}

func (f *fakeConnections) find(id string) int {
	return slices.IndexFunc(f.list, func(c connections.Connection) bool { return c.PublicID == id })
}

func (f *fakeConnections) List(_ context.Context, lf connections.ListFilter) (connections.Page, error) {
	f.filters = append(f.filters, lf)
	var page connections.Page
	for _, c := range f.list {
		if lf.After != nil && c.ID <= *lf.After || lf.Type != nil && c.Type != *lf.Type {
			continue
		}
		if len(page.Connections) == lf.Limit {
			last := page.Connections[lf.Limit-1].ID
			page.Next = &last
			break
		}
		page.Connections = append(page.Connections, c)
	}
	return page, f.err
}

func (f *fakeConnections) Get(_ context.Context, id string) (connections.Connection, error) {
	i := f.find(id)
	if i < 0 {
		return connections.Connection{}, connections.ErrNotFound
	}
	return f.list[i], f.err
}

func (f *fakeConnections) Create(_ context.Context, r connections.Requester, in connections.Input) (
	connections.Connection, error) {
	f.inputs, f.by = append(f.inputs, in), append(f.by, r)
	if f.err != nil {
		return connections.Connection{}, f.err
	}
	c := connections.Connection{ID: int64(len(f.list) + 1), PublicID: "CNAAAAAAAAAAA9", Type: in.Type, Name: in.Name,
		ServerURL: in.ServerURL, BotAPIBaseURL: in.BotAPIBaseURL, UpdateMode: in.UpdateMode,
		BridgeBaseURL: in.BridgeBaseURL, EventDelivery: in.EventDelivery, EventMode: in.EventMode,
		BotToken: keyring.SecretStatus{Set: true, UpdatedAt: &t0}, Limiter: in.Limiter, CreatedAt: t0, Version: 1}
	if in.PushSecret.Given {
		c.PushSecret = keyring.SecretStatus{Set: true, UpdatedAt: &t0}
	}
	if in.AppToken.Given {
		c.AppToken = keyring.SecretStatus{Set: true, UpdatedAt: &t0}
	}
	if in.SlackSigningSecret.Given {
		c.SlackSigningSecret = keyring.SecretStatus{Set: true, UpdatedAt: &t0}
	}
	if in.Type == connections.TypeTelegram {
		c.Warnings = []string{"base_url_uses_http"}
	}
	f.list = append(f.list, c)
	return c, nil
}

func (f *fakeConnections) Update(_ context.Context, r connections.Requester, id string, version *int64,
	in connections.Input) (connections.Connection, error) {
	f.inputs, f.versions, f.by = append(f.inputs, in), append(f.versions, version), append(f.by, r)
	i := f.find(id)
	if i < 0 {
		return connections.Connection{}, connections.ErrNotFound
	}
	if version != nil && *version != f.list[i].Version {
		return connections.Connection{}, connections.ErrVersionMismatch
	}
	if f.err != nil {
		return connections.Connection{}, f.err
	}
	c := &f.list[i]
	c.Name, c.ServerURL, c.Limiter = in.Name, in.ServerURL, in.Limiter
	c.Version++
	return *c, nil
}

func (f *fakeConnections) Delete(_ context.Context, r connections.Requester, id string, version *int64) error {
	f.versions, f.by = append(f.versions, version), append(f.by, r)
	i := f.find(id)
	if i < 0 {
		return connections.ErrNotFound
	}
	if version != nil && *version != f.list[i].Version {
		return connections.ErrVersionMismatch
	}
	if f.list[i].DestinationCount > 0 {
		return connections.ErrInUse
	}
	if f.err != nil {
		return f.err
	}
	f.deleted = append(f.deleted, id)
	f.list = slices.Delete(f.list, i, i+1)
	return nil
}

// checkRun is the check run of a fake's result that names none, as every check and test names one.
const checkRun = "CRAAAAAAAAAAA1"

func (f *fakeConnections) Check(_ context.Context, id string, baseURL *string) (connections.CheckResult, error) {
	f.baseURLs = append(f.baseURLs, baseURL)
	if f.find(id) < 0 {
		return connections.CheckResult{}, connections.ErrNotFound
	}
	return f.checked(), f.err
}

func (f *fakeConnections) CheckDraft(_ context.Context, d connections.Draft) (connections.CheckResult, error) {
	f.drafts = append(f.drafts, d)
	return f.checked(), f.err
}

func (f *fakeConnections) checked() connections.CheckResult {
	res := f.check
	res.Run = cmp.Or(res.Run, checkRun)
	return res
}

func (f *fakeConnections) Channels(_ context.Context, id, team, q string) ([]connections.Channel, error) {
	f.chanArgs = append(f.chanArgs, [2]string{team, q})
	if f.find(id) < 0 {
		return nil, connections.ErrNotFound
	}
	return f.channels, f.err
}

func (f *fakeConnections) CallbackURL(id string) string {
	return "http://localhost:8081" + connections.CallbackPath + id
}

func (f *fakeConnections) BridgeCallbackURL(id string) string {
	return "http://localhost:8081/api/v1/callbacks/bridge/" + id
}

func (f *fakeConnections) SlackCallbackURL(id string) string {
	return "http://localhost:8081/api/v1/callbacks/slack/" + id
}

func newConnectionsAPI(t *testing.T) (*testAPI, *fakeConnections) {
	t.Helper()
	x, _, _ := newAlertGroupsAPI(t)
	ft := x.srv.tokens.(*fakeTokens)
	owner := ft.idents[fullToken].Session
	ft.idents[connectionsWriter] = &auth.Identity{Session: owner, Permissions: []auth.Permission{"connections:read",
		"connections:write"}, Transport: audit.TransportAPI, Token: &auth.Token{ID: 61, Name: "connections"}}
	ft.idents[connectionsReader] = &auth.Identity{Session: owner, Permissions: []auth.Permission{"connections:read"},
		Transport: audit.TransportAPI, Token: &auth.Token{ID: 62, Name: "connections-read"}}
	user := "muster-dev-bot"
	fc := &fakeConnections{list: []connections.Connection{
		{ID: 1, PublicID: connectionID, Type: connections.TypeMattermost, Name: "mm",
			ServerURL: "http://127.0.0.1:18065", BotToken: keyring.SecretStatus{Set: true, UpdatedAt: &t0},
			BotUsername: &user, Limiter: connections.Limiter{Limit: 5, PerSeconds: 1}, DestinationCount: 1,
			CreatedAt: t0, Version: 3},
		{ID: 2, PublicID: "CNAAAAAAAAAAA2", Type: connections.TypeMattermost, Name: "mm-proxy",
			ServerURL: "https://mm.example.org", BotToken: keyring.SecretStatus{Set: true, UpdatedAt: &t0},
			Proxy:         proxyconf.Config{Enabled: true, Type: "http", Address: "proxy:3128", Username: ptr("muster")},
			ProxyPassword: keyring.SecretStatus{Set: true, UpdatedAt: &t0},
			Limiter:       connections.Limiter{Limit: 5, PerSeconds: 1}, CreatedAt: t0, Version: 1},
		{ID: 3, PublicID: "CNAAAAAAAAAAA3", Type: connections.TypeMattermost, Name: "mm-3",
			ServerURL: "https://mm3.example.org", BotToken: keyring.SecretStatus{Set: true, UpdatedAt: &t0},
			Limiter: connections.Limiter{Limit: 5, PerSeconds: 1}, CreatedAt: t0, Version: 1},
	}}
	x.srv.connections = fc
	return x, fc
}

// noToken fails when an answer carries the bot token or a bot_token field.
func noToken(t *testing.T, a answer) {
	t.Helper()
	if strings.Contains(string(a.body), botTokenValue) || strings.Contains(string(a.body), `"bot_token"`) {
		t.Errorf("the bot token is in the answer: %s", a.body)
	}
}

// TestListConnectionsAPI is listConnections (C-13.FR-1, FR-13): the Connections with their bot_token_status, proxy,
// destination_count and callback_url, paged by cursor and filtered by type; never the bot token.
func TestListConnectionsAPI(t *testing.T) {
	x, fc := newConnectionsAPI(t)
	a := x.as(t, connectionsReader, http.MethodGet, "/api/v1/connections?limit=2", "")
	var list gen.ConnectionList
	decodeInto(t, a, &list)
	noToken(t, a)
	if a.status != http.StatusOK || len(list.Items) != 2 || list.NextCursor.IsNull() {
		t.Fatalf("first page = %d %s", a.status, a.body)
	}
	first, err := list.Items[0].AsMattermostConnection()
	if err != nil || first.Id != connectionID || first.BotUsername.MustGet() != "muster-dev-bot" ||
		!first.BotTokenStatus.Set || first.DestinationCount != 1 || *first.Etag != `"3"` ||
		*first.CallbackUrl != "http://localhost:8081/api/v1/callbacks/mattermost/"+connectionID {
		t.Errorf("first = %+v, %v", first, err)
	}
	second, _ := list.Items[1].AsMattermostConnection()
	if !second.Proxy.Enabled || !second.Proxy.PasswordStatus.Set || !second.BotUsername.IsNull() {
		t.Errorf("second %s", a.body)
	}
	a = x.as(t, connectionsReader, http.MethodGet, "/api/v1/connections?type=mattermost&cursor="+
		list.NextCursor.MustGet(), "")
	decodeInto(t, a, &list)
	last := fc.filters[len(fc.filters)-1]
	if a.status != http.StatusOK || len(list.Items) != 1 || !list.NextCursor.IsNull() || *last.Type != "mattermost" ||
		*last.After != 2 {
		t.Errorf("second page = %d %s, %+v", a.status, a.body, last)
	}
	if a := x.as(t, connectionsReader, http.MethodGet, "/api/v1/connections?cursor=bad", ""); a.status !=
		http.StatusBadRequest {
		t.Errorf("bad cursor = %d", a.status)
	}
	if a := x.as(t, groupsReader, http.MethodGet, "/api/v1/connections", ""); a.status != http.StatusForbidden {
		t.Errorf("without connections:read = %d", a.status)
	}
	fc.err = errors.New("down")
	if a := x.as(t, connectionsReader, http.MethodGet, "/api/v1/connections", ""); a.status !=
		http.StatusInternalServerError {
		t.Errorf("failed = %d", a.status)
	}
}

// TestCreateConnectionAPI is createConnection (C-13.FR-1, C-14.FR-1): 201 with ETag and Location, the bot token handed
// to the service and never answered, for a Mattermost and a Telegram Connection.
func TestCreateConnectionAPI(t *testing.T) {
	x, fc := newConnectionsAPI(t)
	a := x.as(t, connectionsWriter, http.MethodPost, "/api/v1/connections", connectionBody)
	noToken(t, a)
	if a.status != http.StatusCreated || a.header.Get("ETag") != `"1"` ||
		a.header.Get("Location") != "/api/v1/connections/CNAAAAAAAAAAA9" || a.json(t)["callback_url"] !=
		"http://localhost:8081/api/v1/callbacks/mattermost/CNAAAAAAAAAAA9" {
		t.Fatalf("create = %d %v %s", a.status, a.header, a.body)
	}
	in := fc.inputs[0]
	if in.Type != connections.TypeMattermost || in.Name != "mm" || in.ServerURL != "http://127.0.0.1:18065" ||
		!in.BotToken.Given || string(in.BotToken.Value) != botTokenValue || in.Proxy.Enabled || in.Limiter.Limit != 5 ||
		fc.by[0].Actor.TokenName != "connections" || fc.by[0].Transport != audit.TransportAPI {
		t.Errorf("input %+v, by %+v", in, fc.by[0])
	}
	telegram := `{"type":"telegram","name":"tg","bot_api_base_url":"http://127.0.0.1:18081/",` +
		`"update_mode":"webhook","bot_token":"` + botTokenValue + `","proxy":{"enabled":true,"type":"socks5",` +
		`"address":"127.0.0.1:18092"},"limiter":{"limit":15,"per_seconds":1}}`
	a = x.as(t, connectionsWriter, http.MethodPost, "/api/v1/connections", telegram)
	noToken(t, a)
	m := a.json(t)
	if a.status != http.StatusCreated || m["type"] != "telegram" || m["bot_api_base_url"] != "http://127.0.0.1:18081/" ||
		m["update_mode"] != "webhook" || m["callback_url"] != nil || m["bot_username"] != nil ||
		m["warnings"].([]any)[0] != "base_url_uses_http" {
		t.Errorf("telegram = %d %s", a.status, a.body)
	}
	if in := fc.inputs[1]; in.Type != connections.TypeTelegram || in.BotAPIBaseURL != "http://127.0.0.1:18081/" ||
		in.UpdateMode != "webhook" || string(in.BotToken.Value) != botTokenValue || !in.Proxy.Enabled ||
		in.ServerURL != "" || in.Limiter.Limit != 15 {
		t.Errorf("telegram input %+v", in)
	}
	fc.err = &connections.FieldError{Pointer: "/update_mode", Code: connections.CodeWebhookCallFailed,
		Detail: "setWebhook failed: Telegram answered 400: Bad Request"}
	if errs := problemErrors(t, x.as(t, connectionsWriter, http.MethodPost, "/api/v1/connections",
		telegram)); len(errs) != 1 || errs[0]["pointer"] != "/update_mode" || errs[0]["code"] != "webhook_call_failed" {
		t.Errorf("refused setWebhook = %v", errs)
	}
	fc.err = nil
	withoutToken := strings.Replace(telegram, `"bot_token":"`+botTokenValue+`",`, "", 1)
	if a := x.as(t, connectionsWriter, http.MethodPost, "/api/v1/connections", withoutToken); a.status !=
		http.StatusCreated || fc.inputs[len(fc.inputs)-1].BotToken.Given {
		t.Errorf("telegram without a token = %d %s", a.status, a.body)
	}
	fc.err = connections.ErrNameTaken
	if a := x.as(t, connectionsWriter, http.MethodPost, "/api/v1/connections", connectionBody); a.status !=
		http.StatusConflict || a.code(t) != "name_taken" {
		t.Errorf("taken = %d %s", a.status, a.body)
	}
	fc.err = &connections.FieldError{Pointer: "/server_url", Code: connections.CodeInvalidFormat, Detail: "bad"}
	if errs := problemErrors(t, x.as(t, connectionsWriter, http.MethodPost, "/api/v1/connections",
		connectionBody)); len(errs) != 1 || errs[0]["pointer"] != "/server_url" {
		t.Errorf("invalid = %v", errs)
	}
	if a := x.as(t, connectionsReader, http.MethodPost, "/api/v1/connections", connectionBody); a.status !=
		http.StatusForbidden {
		t.Errorf("by a reader = %d", a.status)
	}
}

func problemErrors(t *testing.T, a answer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, e := range a.json(t)["errors"].([]any) {
		out = append(out, e.(map[string]any))
	}
	return out
}

// TestGetConnectionAPI is getConnection: the Connection with its ETag and read-only callback_url (C-13.FR-13); an
// unknown or deleted Connection is 404.
func TestGetConnectionAPI(t *testing.T) {
	x, _ := newConnectionsAPI(t)
	a := x.as(t, connectionsReader, http.MethodGet, "/api/v1/connections/"+connectionID, "")
	m := a.json(t)
	noToken(t, a)
	if a.status != http.StatusOK || a.header.Get("ETag") != `"3"` || m["type"] != "mattermost" ||
		m["bot_token_status"].(map[string]any)["set"] != true || m["bot_username"] != "muster-dev-bot" ||
		m["callback_url"] != "http://localhost:8081/api/v1/callbacks/mattermost/"+connectionID {
		t.Errorf("get = %d %s", a.status, a.body)
	}
	a = x.as(t, connectionsReader, http.MethodGet, "/api/v1/connections/CN000000000000", "")
	if a.status != http.StatusNotFound || a.json(t)["detail"] != "No such Connection." {
		t.Errorf("unknown = %d %s", a.status, a.body)
	}
}

// TestUpdateConnectionAPI is updateConnection: If-Match is required (428) and must be current (412); an omitted bot
// token keeps the stored one.
func TestUpdateConnectionAPI(t *testing.T) {
	x, fc := newConnectionsAPI(t)
	path := "/api/v1/connections/" + connectionID
	body := `{"type":"mattermost","name":"mm-2","server_url":"http://127.0.0.1:18065","proxy":{"enabled":false},` +
		`"limiter":{"limit":10,"per_seconds":1}}`
	if a := x.as(t, connectionsWriter, http.MethodPut, path, body); a.status != http.StatusPreconditionRequired {
		t.Errorf("without If-Match = %d %s", a.status, a.body)
	}
	if a := x.as(t, connectionsWriter, http.MethodPut, path, body, "If-Match", `"2"`); a.status !=
		http.StatusPreconditionFailed {
		t.Errorf("stale = %d %s", a.status, a.body)
	}
	a := x.as(t, connectionsWriter, http.MethodPut, path, body, "If-Match", `"3"`)
	in := fc.inputs[len(fc.inputs)-1]
	if a.status != http.StatusOK || a.header.Get("ETag") != `"4"` || a.json(t)["name"] != "mm-2" ||
		in.BotToken.Given || *fc.versions[len(fc.versions)-1] != 3 || in.Limiter.Limit != 10 {
		t.Errorf("update = %d %s, %+v", a.status, a.body, in)
	}
	a = x.as(t, connectionsWriter, http.MethodPut, path, connectionBody, "If-Match", `"4"`)
	noToken(t, a)
	if in := fc.inputs[len(fc.inputs)-1]; a.status != http.StatusOK || string(in.BotToken.Value) != botTokenValue {
		t.Errorf("replace the token = %d %s", a.status, a.body)
	}
	if a := x.as(t, connectionsWriter, http.MethodPut, "/api/v1/connections/CN000000000000", body, "If-Match",
		`"1"`); a.status != http.StatusNotFound {
		t.Errorf("unknown = %d", a.status)
	}
}

// TestDeleteConnectionAPI is deleteConnection (C-13.FR-6): 204, 409 in_use while a Destination uses the Connection,
// 412 for a stale If-Match and 404 once it is deleted.
func TestDeleteConnectionAPI(t *testing.T) {
	x, fc := newConnectionsAPI(t)
	a := x.as(t, connectionsWriter, http.MethodDelete, "/api/v1/connections/"+connectionID, "")
	if a.status != http.StatusConflict || a.code(t) != "in_use" {
		t.Errorf("in use = %d %s", a.status, a.body)
	}
	path := "/api/v1/connections/CNAAAAAAAAAAA2"
	if a := x.as(t, connectionsWriter, http.MethodDelete, path, "", "If-Match", `"9"`); a.status !=
		http.StatusPreconditionFailed {
		t.Errorf("stale = %d", a.status)
	}
	if a := x.as(t, connectionsWriter, http.MethodDelete, path, "", "If-Match", `"1"`); a.status !=
		http.StatusNoContent || !slices.Equal(fc.deleted, []string{"CNAAAAAAAAAAA2"}) {
		t.Errorf("delete = %d %s", a.status, a.body)
	}
	if a := x.as(t, connectionsWriter, http.MethodDelete, path, ""); a.status != http.StatusNotFound {
		t.Errorf("again = %d", a.status)
	}
	if a := x.as(t, connectionsWriter, http.MethodDelete, "/api/v1/connections/CNAAAAAAAAAAA3", ""); a.status !=
		http.StatusNoContent || fc.versions[len(fc.versions)-1] != nil {
		t.Errorf("without If-Match = %d", a.status)
	}
	if a := x.as(t, connectionsReader, http.MethodDelete, "/api/v1/connections/"+connectionID, ""); a.status !=
		http.StatusForbidden {
		t.Errorf("by a reader = %d", a.status)
	}
}

// TestCheckConnectionAPI is checkConnection (C-13.FR-2, C-11.FR-2): the token step with its latency and path, the
// message of a failure and the bot's name; 503 with Retry-After when no limiter token is free in time.
func TestCheckConnectionAPI(t *testing.T) {
	x, fc := newConnectionsAPI(t)
	path := "/api/v1/connections/" + connectionID + "/checks"
	fc.check = connections.CheckResult{OK: true, BotName: "muster-dev-bot", Steps: []connections.Step{{
		Name: connections.StepToken, OK: true, Latency: 7 * time.Millisecond, Via: mattermost.ViaDirect}},
		Warnings: []string{connections.WarningPressAnswersInThread}}
	a := x.as(t, connectionsWriter, http.MethodPost, path, "")
	var res gen.ConnectionCheckResult
	decodeInto(t, a, &res)
	if a.status != http.StatusOK || !res.Ok || res.BotName.MustGet() != "muster-dev-bot" || len(res.Steps) != 1 ||
		res.Steps[0].Name != "token" || res.Steps[0].LatencyMs.MustGet() != 7 || *res.Steps[0].Via != "direct" ||
		!res.Steps[0].Message.IsNull() || !res.Steps[0].Code.IsNull() || len(res.Warnings) != 1 ||
		res.Warnings[0] != gen.PressAnswersInThread {
		t.Errorf("check = %d %s", a.status, a.body)
	}
	fc.check = connections.CheckResult{Steps: []connections.Step{{Name: connections.StepToken,
		Latency: 3 * time.Millisecond, Via: mattermost.ViaProxy, Message: mattermost.MessageTokenInvalid,
		Code: mattermost.CodeTokenInvalid}}}
	a = x.as(t, connectionsWriter, http.MethodPost, path, "")
	decodeInto(t, a, &res)
	if a.status != http.StatusOK || res.Ok || !res.BotName.IsNull() || *res.Steps[0].Via != "proxy" ||
		res.Steps[0].Message.MustGet() != "The bot token is not valid." ||
		res.Steps[0].Code.MustGet() != gen.NullableConnectionCheckCodeTokenInvalid || !strings.Contains(string(a.body),
		`"warnings":[]`) {
		t.Errorf("revoked = %d %s", a.status, a.body)
	}
	fc.err = &delivery.LimitedError{RetryAfter: 1500 * time.Millisecond}
	if a := x.as(t, connectionsWriter, http.MethodPost, path, ""); a.status != http.StatusServiceUnavailable ||
		a.header.Get("Retry-After") != "2" {
		t.Errorf("limited = %d %v", a.status, a.header)
	}
	if a := x.as(t, connectionsReader, http.MethodPost, path, ""); a.status != http.StatusForbidden {
		t.Errorf("by a reader = %d", a.status)
	}
	if a := x.as(t, connectionsWriter, http.MethodPost, "/api/v1/connections/CN000000000000/checks", ""); a.status !=
		http.StatusNotFound {
		t.Errorf("unknown = %d", a.status)
	}
}

// TestCheckConnectionDraftAPI is checkConnectionDraft (C-13.FR-14): a draft of each type reaches the service with the
// token typed in the form, its proxy and the Connection it edits, and its result has the steps and codes of
// checkConnection, skipped steps without latency and a Bridge's info; nothing is written. The refusals of the service
// are 422 at their pointers, no limiter token 503 with Retry-After; a reader is 403, and a body the specification
// refuses is 400.
func TestCheckConnectionDraftAPI(t *testing.T) {
	x, fc := newConnectionsAPI(t)
	const path = "/api/v1/connections/checks"
	fc.check = connections.CheckResult{Steps: []connections.Step{
		{Name: mattermost.StepDryProbe, Latency: 4 * time.Millisecond, Via: mattermost.ViaProxy,
			Message: "The address answered 200 with text/html, not as a Mattermost server.",
			Code:    mattermost.CodeNotMattermost},
		{Name: connections.StepToken, Skipped: true, Via: mattermost.ViaProxy}}}
	body := `{"type":"mattermost","server_url":"http://127.0.0.1:18066","bot_token":"` + botTokenValue +
		`","proxy":{"enabled":true,"type":"http","address":"127.0.0.1:18091","password":"proxy-pass"},` +
		`"connection_id":"` + connectionID + `"}`
	a := x.as(t, connectionsWriter, http.MethodPost, path, body)
	noToken(t, a)
	var res gen.ConnectionCheckResult
	decodeInto(t, a, &res)
	if a.status != http.StatusOK || res.Ok || len(res.Steps) != 2 || res.Steps[0].Name != "dry_probe" ||
		res.Steps[0].Code.MustGet() != gen.NullableConnectionCheckCodeNotMattermost ||
		res.Steps[0].LatencyMs.MustGet() != 4 || *res.Steps[0].Via != "proxy" || res.Steps[1].Name != "token" ||
		!*res.Steps[1].Skipped || !res.Steps[1].LatencyMs.IsNull() || !res.Steps[1].Code.IsNull() ||
		!res.BotName.IsNull() || strings.Contains(string(a.body), `"info"`) ||
		strings.Contains(string(a.body), "proxy-pass") {
		t.Fatalf("mattermost = %d %s", a.status, a.body)
	}
	d := fc.drafts[0]
	if d.Type != connections.TypeMattermost || d.ServerURL != "http://127.0.0.1:18066" || !d.BotToken.Given ||
		string(d.BotToken.Value) != botTokenValue || d.Proxy == nil || !d.Proxy.Enabled ||
		*d.Proxy.Address != "127.0.0.1:18091" || string(d.Proxy.Password.Value) != "proxy-pass" ||
		d.ConnectionID == nil || *d.ConnectionID != connectionID {
		t.Errorf("mattermost draft %+v", d)
	}
	fc.check = connections.CheckResult{OK: true, BotName: "muster-dev-bot-b", Steps: []connections.Step{{
		Name: connections.StepToken, OK: true, Latency: 7 * time.Millisecond, Via: mattermost.ViaDirect}},
		Warnings: []string{connections.WarningPressAnswersInThread}}
	a = x.as(t, connectionsWriter, http.MethodPost, path, `{"type":"mattermost","server_url":"http://127.0.0.1:18066"}`)
	decodeInto(t, a, &res)
	if a.status != http.StatusOK || !res.Ok || res.BotName.MustGet() != "muster-dev-bot-b" ||
		res.Warnings[0] != gen.PressAnswersInThread || len(fc.drafts) != 2 || fc.drafts[1].BotToken.Given ||
		fc.drafts[1].Proxy != nil || fc.drafts[1].ConnectionID != nil {
		t.Fatalf("a new draft without a token = %d %s, %+v", a.status, a.body, fc.drafts)
	}

	webhook, pending := false, int64(0)
	fc.check = connections.CheckResult{OK: true, BotName: "muster_dev_bot", WebhookSet: &webhook,
		PendingUpdates: &pending, Steps: []connections.Step{
			{Name: "dry_probe", OK: true, Latency: time.Millisecond, Via: "direct"},
			{Name: "get_me", OK: true, Latency: time.Millisecond, Via: "direct"},
			{Name: "get_webhook_info", OK: true, Latency: time.Millisecond, Via: "direct"}}}
	a = x.as(t, connectionsWriter, http.MethodPost, path,
		`{"type":"telegram","bot_api_base_url":"http://127.0.0.1:18081/","update_mode":"long_polling"}`)
	decodeInto(t, a, &res)
	if d := fc.drafts[2]; a.status != http.StatusOK || !res.Ok || len(res.Steps) != 3 ||
		res.WebhookSet.MustGet() || res.PendingUpdates.MustGet() != 0 || d.Type != connections.TypeTelegram ||
		d.BotAPIBaseURL != "http://127.0.0.1:18081/" || d.UpdateMode != connections.ModeLongPolling ||
		d.BotToken.Given || d.Proxy != nil || d.ServerURL != "" {
		t.Errorf("telegram = %d %s, %+v", a.status, a.body, d)
	}

	fc.check = connections.CheckResult{OK: true, Bridge: true, Info: []byte(bridgeInfoJSON), Steps: []connections.Step{
		{Name: connections.StepInfo, OK: true, Latency: 9 * time.Millisecond, Via: "direct"},
		{Name: connections.StepEventDelivery, OK: true, Via: "direct"}}}
	bridgeBody := `{"type":"bridge","base_url":"http://127.0.0.1:18094","event_delivery":"push","bot_token":"` +
		botTokenValue + `","connection_id":"CNAAAAAAAAAAB1"}`
	a = x.as(t, connectionsWriter, http.MethodPost, path, bridgeBody)
	noToken(t, a)
	decodeInto(t, a, &res)
	if info, err := res.Info.Get(); a.status != http.StatusOK || err != nil || info.Messenger.Name != "Fake chat" {
		t.Errorf("bridge = %d %s", a.status, a.body)
	}
	if d := fc.drafts[3]; d.Type != connections.TypeBridge || d.BridgeBaseURL != "http://127.0.0.1:18094" ||
		d.EventDelivery != connections.EventDeliveryPush || string(d.BotToken.Value) != botTokenValue ||
		*d.ConnectionID != "CNAAAAAAAAAAB1" {
		t.Errorf("bridge draft %+v", d)
	}
	fc.check = connections.CheckResult{Bridge: true, Steps: []connections.Step{
		{Name: "dry_probe", Latency: time.Millisecond, Via: "direct", Message: "not a bridge", Code: "not_bridge"},
		{Name: connections.StepInfo, Skipped: true, Via: "direct"},
		{Name: connections.StepEventDelivery, Skipped: true, Via: "direct"}}}
	a = x.as(t, connectionsWriter, http.MethodPost, path,
		`{"type":"bridge","base_url":"http://127.0.0.1:18065","event_delivery":"poll"}`)
	decodeInto(t, a, &res)
	if a.status != http.StatusOK || res.Ok || !res.Info.IsNull() ||
		res.Steps[0].Code.MustGet() != gen.NullableConnectionCheckCodeNotBridge || !*res.Steps[2].Skipped {
		t.Errorf("bridge without a token = %d %s", a.status, a.body)
	}

	for _, fe := range []*connections.FieldError{
		{Pointer: "/connection_id", Code: connections.CodeUnknownID, Detail: "No Connection of this type has this id."},
		{Pointer: "/server_url", Code: connections.CodeInvalidFormat, Detail: "bad"},
		{Pointer: "/proxy/address", Code: connections.CodeRequired, Detail: "An enabled proxy needs its address."},
	} {
		fc.err = fe
		a := x.as(t, connectionsWriter, http.MethodPost, path, body)
		if errs := problemErrors(t, a); a.status != http.StatusUnprocessableEntity || len(errs) != 1 ||
			errs[0]["pointer"] != fe.Pointer || errs[0]["code"] != fe.Code {
			t.Errorf("%s = %d %s", fe.Pointer, a.status, a.body)
		}
	}
	fc.err = &delivery.LimitedError{RetryAfter: 1500 * time.Millisecond}
	if a := x.as(t, connectionsWriter, http.MethodPost, path, body); a.status != http.StatusServiceUnavailable ||
		a.header.Get("Retry-After") != "2" {
		t.Errorf("limited = %d %v", a.status, a.header)
	}
	fc.err = nil
	n := len(fc.drafts)
	for name, tc := range map[string]struct {
		token, method, body string
		status              int
	}{
		"reader":          {connectionsReader, http.MethodPost, body, http.StatusForbidden},
		"no server_url":   {connectionsWriter, http.MethodPost, `{"type":"mattermost"}`, http.StatusBadRequest},
		"unknown type":    {connectionsWriter, http.MethodPost, `{"type":"slack","server_url":"x"}`, 400},
		"malformed id":    {connectionsWriter, http.MethodPost, strings.Replace(body, connectionID, "checks", 1), 400},
		"bad update mode": {connectionsWriter, http.MethodPost, `{"type":"telegram","bot_api_base_url":"x"}`, 400},
		"no body":         {connectionsWriter, http.MethodPost, "", http.StatusBadRequest},
		"read":            {connectionsWriter, http.MethodGet, "", http.StatusMethodNotAllowed},
	} {
		if a := x.as(t, tc.token, tc.method, path, tc.body); a.status != tc.status {
			t.Errorf("%s = %d %s", name, a.status, a.body)
		}
	}
	if a := x.as(t, connectionsWriter, http.MethodPost, path, strings.Replace(body, connectionID, "checks", 1)); len(
		problemErrors(t, a)) == 0 || problemErrors(t, a)[0]["pointer"] != "/connection_id" {
		t.Errorf("a malformed connection_id = %s", a.body)
	}
	if len(fc.drafts) != n || len(fc.inputs) != 0 || len(fc.deleted) != 0 || len(fc.list) != 3 {
		t.Errorf("refused drafts reached the service or a draft wrote: %d drafts, %d inputs, %d deleted, %d listed",
			len(fc.drafts)-n, len(fc.inputs), len(fc.deleted), len(fc.list))
	}
}

// TestTelegramConnectionAPI covers C-14.FR-1, FR-10 and FR-11 on the API: a Telegram Connection reads with its base
// URL, update mode and warnings; its check has the three steps, skipped ones without latency, the webhook and the
// pending updates; an unsaved base_url reaches the service, a null one does not.
func TestTelegramConnectionAPI(t *testing.T) {
	x, fc := newConnectionsAPI(t)
	bot := "muster_dev_bot"
	fc.list = append(fc.list, connections.Connection{ID: 4, PublicID: "CNAAAAAAAAAAT1", Type: connections.TypeTelegram,
		Name: "tg", BotAPIBaseURL: "http://127.0.0.1:18081", UpdateMode: "long_polling",
		Warnings: []string{"base_url_uses_http"}, BotToken: keyring.SecretStatus{Set: true, UpdatedAt: &t0},
		BotUsername: &bot, Limiter: connections.Limiter{Limit: 15, PerSeconds: 1}, CreatedAt: t0, Version: 2})
	a := x.as(t, connectionsReader, http.MethodGet, "/api/v1/connections/CNAAAAAAAAAAT1", "")
	var c gen.TelegramConnection
	decodeInto(t, a, &c)
	if a.status != http.StatusOK || c.BotApiBaseUrl != "http://127.0.0.1:18081" || c.UpdateMode != "long_polling" ||
		len(c.Warnings) != 1 || c.Warnings[0] != "base_url_uses_http" || c.BotUsername.MustGet() != bot ||
		*c.Etag != `"2"` || !c.BotTokenStatus.Set {
		t.Fatalf("get = %d %s", a.status, a.body)
	}
	a = x.as(t, connectionsReader, http.MethodGet, "/api/v1/connections?type=telegram", "")
	var list gen.ConnectionList
	decodeInto(t, a, &list)
	if kind, _ := list.Items[0].Discriminator(); a.status != http.StatusOK || len(list.Items) != 1 || kind != "telegram" {
		t.Fatalf("list = %d %s", a.status, a.body)
	}
	set, pending := true, int64(4)
	fc.check = connections.CheckResult{OK: true, BotName: bot, WebhookSet: &set, PendingUpdates: &pending,
		Steps: []connections.Step{
			{Name: "dry_probe", OK: true, Latency: 4 * time.Millisecond, Via: "proxy"},
			{Name: "get_me", OK: true, Latency: 5 * time.Millisecond, Via: "proxy"},
			{Name: "get_webhook_info", OK: true, Latency: 6 * time.Millisecond, Via: "proxy",
				Message: "A webhook is set at in.example.org.", Code: "webhook_set"}}}
	path := "/api/v1/connections/CNAAAAAAAAAAT1/checks"
	a = x.as(t, connectionsWriter, http.MethodPost, path, "{}")
	var res gen.ConnectionCheckResult
	decodeInto(t, a, &res)
	if a.status != http.StatusOK || !res.WebhookSet.MustGet() || res.PendingUpdates.MustGet() != 4 ||
		len(res.Steps) != 3 || *res.Steps[2].Skipped || res.Steps[2].Message.MustGet() != "A webhook is set at in.example.org." ||
		res.Steps[2].Code.MustGet() != gen.NullableConnectionCheckCodeWebhookSet || fc.baseURLs[len(fc.baseURLs)-1] != nil {
		t.Fatalf("check = %d %s", a.status, a.body)
	}
	fc.check = connections.CheckResult{Steps: []connections.Step{
		{Name: "dry_probe", Latency: time.Millisecond, Via: "direct", Message: "This is not a Bot API.",
			Code: "not_bot_api"},
		{Name: "get_me", Skipped: true, Via: "direct"}, {Name: "get_webhook_info", Skipped: true, Via: "direct"}}}
	a = x.as(t, connectionsWriter, http.MethodPost, path, `{"base_url":"http://127.0.0.1:18081/other/"}`)
	decodeInto(t, a, &res)
	if got := fc.baseURLs[len(fc.baseURLs)-1]; a.status != http.StatusOK || got == nil ||
		*got != "http://127.0.0.1:18081/other/" || !*res.Steps[1].Skipped || !res.Steps[1].LatencyMs.IsNull() ||
		res.Steps[0].Message.MustGet() != "This is not a Bot API." ||
		res.Steps[0].Code.MustGet() != gen.NullableConnectionCheckCodeNotBotApi || !res.Steps[1].Code.IsNull() ||
		!res.WebhookSet.IsNull() ||
		!res.PendingUpdates.IsNull() {
		t.Fatalf("unsaved base URL = %d %s", a.status, a.body)
	}
	if a := x.as(t, connectionsWriter, http.MethodPost, path, `{"base_url":null}`); a.status != http.StatusOK ||
		fc.baseURLs[len(fc.baseURLs)-1] != nil {
		t.Fatalf("null base URL = %d", a.status)
	}
	body := `{"type":"telegram","name":"tg-2","bot_api_base_url":"http://127.0.0.1:18081","update_mode":"long_polling",` +
		`"proxy":{"enabled":false},"limiter":{"limit":15,"per_seconds":1}}`
	a = x.as(t, connectionsWriter, http.MethodPut, "/api/v1/connections/CNAAAAAAAAAAT1", body, "If-Match", `"2"`)
	if in := fc.inputs[len(fc.inputs)-1]; a.status != http.StatusOK || in.BotToken.Given || in.Type != "telegram" ||
		in.UpdateMode != "long_polling" {
		t.Fatalf("update = %d %s, %+v", a.status, a.body, in)
	}
}

// TestListConnectionChannelsAPI is listConnectionChannels: the channels with their team, type and archived flag,
// filtered by team_id and q; 409 for a Telegram Connection and for a call Mattermost refused; 503 when limited.
func TestListConnectionChannelsAPI(t *testing.T) {
	x, fc := newConnectionsAPI(t)
	ch := func(id, kind string, deleteAt int64) connections.Channel {
		return connections.Channel{Channel: mattermost.Channel{ID: id, TeamID: "team-dev", Name: id,
			DisplayName: strings.ToUpper(id), Type: kind, DeleteAt: deleteAt}, TeamName: "dev"}
	}
	fc.channels = []connections.Channel{ch("open", mattermost.ChannelOpen, 0), ch("private", mattermost.ChannelPrivate,
		0), ch("direct", mattermost.ChannelDirect, 0), ch("group", mattermost.ChannelGroup, 5)}
	path := "/api/v1/connections/" + connectionID + "/channels"
	a := x.as(t, connectionsWriter, http.MethodGet, path+"?team_id=team-dev&q=al", "")
	var list gen.MattermostChannelList
	decodeInto(t, a, &list)
	if a.status != http.StatusOK || len(list.Items) != 4 || fc.chanArgs[0] != [2]string{"team-dev", "al"} {
		t.Fatalf("channels = %d %s", a.status, a.body)
	}
	for i, want := range []string{"open", "private", "direct", "group"} {
		it := list.Items[i]
		if string(it.Type) != want || it.TeamName != "dev" || it.TeamId != "team-dev" || *it.Archived != (i == 3) ||
			it.DisplayName != strings.ToUpper(want) {
			t.Errorf("item %d %+v", i, it)
		}
	}
	if a := x.as(t, connectionsWriter, http.MethodGet, path, ""); a.status != http.StatusOK ||
		fc.chanArgs[1] != [2]string{} {
		t.Errorf("without filters = %d %v", a.status, fc.chanArgs)
	}
	fc.err = connections.ErrNotMattermost
	if a := x.as(t, connectionsWriter, http.MethodGet, path, ""); a.status != http.StatusConflict {
		t.Errorf("telegram = %d %s", a.status, a.body)
	}
	fc.err = &connections.MessengerError{Text: "answered 401"}
	if a := x.as(t, connectionsWriter, http.MethodGet, path, ""); a.status != http.StatusConflict ||
		!strings.Contains(a.json(t)["detail"].(string), "answered 401") {
		t.Errorf("refused = %d %s", a.status, a.body)
	}
	fc.err = &delivery.LimitedError{RetryAfter: time.Second}
	if a := x.as(t, connectionsWriter, http.MethodGet, path, ""); a.status != http.StatusServiceUnavailable ||
		a.header.Get("Retry-After") != "1" {
		t.Errorf("limited = %d", a.status)
	}
	if a := x.as(t, connectionsReader, http.MethodGet, path, ""); a.status != http.StatusForbidden {
		t.Errorf("by a reader = %d", a.status)
	}
}

// pushSecretValue is the push secret of the Bridge bodies, which no answer may carry.
const pushSecretValue = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"

// bridgeInfoJSON is an info as a bridge sends it, with a member Muster does not know.
const bridgeInfoJSON = `{"protocol_version":"1","bridge":{"name":"Stand-in","version":"0.1.0"},` +
	`"messenger":{"name":"Fake chat"},"capabilities":{"buttons":{"max_per_message":8,"max_per_row":4,` +
	`"data_max_bytes":256,"styles":["primary","danger"]},"native_mentions":true,` +
	`"delete":{"max_age_seconds":null}},"limits":{"max_body_length":4000,"edit_max_age_seconds":null},` +
	`"user_lookup":"email","event_delivery":["poll","push"],"future_field":{"kept":true}}`

// TestBridgeConnectionAPI covers C-22.FR-1 and FR-2 on the API: a Bridge Connection is created with its base URL,
// event delivery mode, token and push secret, which reach the service and no answer; it reads with both secrets'
// status, its callback_url and its info, null before the first check and for a copy that does not decode; the type
// filter selects it; its check answers the steps info and event_delivery with their codes and the info read, null
// when info failed, and no info for the other types; an unknown event delivery mode is refused.
func TestBridgeConnectionAPI(t *testing.T) {
	x, fc := newConnectionsAPI(t)
	body := `{"type":"bridge","name":"br","base_url":"http://127.0.0.1:18094/","bot_token":"` + botTokenValue +
		`","event_delivery":"push","push_secret":"` + pushSecretValue + `","proxy":{"enabled":false},` +
		`"limiter":{"limit":10,"per_seconds":1}}`
	a := x.as(t, connectionsWriter, http.MethodPost, "/api/v1/connections", body)
	m := a.json(t)
	if a.status != http.StatusCreated || m["type"] != "bridge" || m["base_url"] != "http://127.0.0.1:18094/" ||
		m["event_delivery"] != "push" || m["bot_token_status"].(map[string]any)["set"] != true ||
		m["push_secret_status"].(map[string]any)["set"] != true ||
		m["callback_url"] != "http://localhost:8081/api/v1/callbacks/bridge/CNAAAAAAAAAAA9" ||
		!strings.Contains(string(a.body), `"info":null`) || !strings.Contains(string(a.body), `"info_read_at":null`) ||
		strings.Contains(string(a.body), pushSecretValue) || strings.Contains(string(a.body), `"push_secret"`) {
		t.Fatalf("create = %d %s", a.status, a.body)
	}
	noToken(t, a)
	in := fc.inputs[0]
	if in.Type != connections.TypeBridge || in.Name != "br" || in.BridgeBaseURL != "http://127.0.0.1:18094/" ||
		in.EventDelivery != connections.EventDeliveryPush || string(in.BotToken.Value) != botTokenValue ||
		string(in.PushSecret.Value) != pushSecretValue || in.Limiter.Limit != 10 || in.ServerURL != "" {
		t.Errorf("input %+v", in)
	}
	withoutSecrets := `{"type":"bridge","name":"br","base_url":"http://127.0.0.1:18094","event_delivery":"poll",` +
		`"proxy":{"enabled":false},"limiter":{"limit":10,"per_seconds":1}}`
	a = x.as(t, connectionsWriter, http.MethodPut, "/api/v1/connections/CNAAAAAAAAAAA9", withoutSecrets, "If-Match",
		`"1"`)
	if in := fc.inputs[len(fc.inputs)-1]; a.status != http.StatusOK || in.BotToken.Given || in.PushSecret.Given ||
		in.EventDelivery != connections.EventDeliveryPoll {
		t.Errorf("update without secrets = %d %s, %+v", a.status, a.body, in)
	}
	smoke := strings.Replace(withoutSecrets, `"poll"`, `"smoke"`, 1)
	if a := x.as(t, connectionsWriter, http.MethodPost, "/api/v1/connections", smoke); a.status !=
		http.StatusBadRequest {
		t.Errorf("an unknown event delivery mode = %d %s", a.status, a.body)
	}

	read := t0.Add(time.Minute)
	fc.list = append(fc.list, connections.Connection{ID: 5, PublicID: "CNAAAAAAAAAAB1", Type: connections.TypeBridge,
		Name: "checked", BridgeBaseURL: "https://bridge.example.org/prefix", EventDelivery: connections.EventDeliveryPoll,
		BotToken: keyring.SecretStatus{Set: true, UpdatedAt: &t0}, PushSecret: keyring.SecretStatus{UpdatedAt: &t0},
		BridgeInfo: []byte(bridgeInfoJSON), BridgeInfoReadAt: &read,
		Limiter: connections.Limiter{Limit: 10, PerSeconds: 1}, CreatedAt: t0, Version: 4})
	a = x.as(t, connectionsReader, http.MethodGet, "/api/v1/connections/CNAAAAAAAAAAB1", "")
	var b gen.BridgeConnection
	decodeInto(t, a, &b)
	info, err := b.Info.Get()
	if a.status != http.StatusOK || err != nil || info.ProtocolVersion != "1" || info.Messenger.Name != "Fake chat" ||
		!b.InfoReadAt.MustGet().Equal(read) || b.PushSecretStatus.Set || *b.Etag != `"4"` ||
		b.BaseUrl != "https://bridge.example.org/prefix" {
		t.Fatalf("get = %d %s", a.status, a.body)
	}
	a = x.as(t, connectionsReader, http.MethodGet, "/api/v1/connections?type=bridge", "")
	var list gen.ConnectionList
	decodeInto(t, a, &list)
	if a.status != http.StatusOK || len(list.Items) != 2 || *fc.filters[len(fc.filters)-1].Type != "bridge" {
		t.Fatalf("list = %d %s", a.status, a.body)
	}
	for _, it := range list.Items {
		if kind, _ := it.Discriminator(); kind != "bridge" {
			t.Errorf("listed %s", kind)
		}
	}

	path := "/api/v1/connections/CNAAAAAAAAAAB1/checks"
	fc.check = connections.CheckResult{OK: true, Bridge: true, Info: []byte(bridgeInfoJSON), Steps: []connections.Step{
		{Name: connections.StepInfo, OK: true, Latency: 9 * time.Millisecond, Via: "proxy"},
		{Name: connections.StepEventDelivery, OK: true, Via: "proxy"}}}
	a = x.as(t, connectionsWriter, http.MethodPost, path, "")
	var res gen.ConnectionCheckResult
	decodeInto(t, a, &res)
	if info, err := res.Info.Get(); a.status != http.StatusOK || !res.Ok || err != nil ||
		info.Messenger.Name != "Fake chat" || len(res.Steps) != 2 || res.Steps[0].Name != "info" ||
		res.Steps[0].LatencyMs.MustGet() != 9 || *res.Steps[0].Via != "proxy" || res.Steps[1].Name != "event_delivery" ||
		!res.Steps[1].Code.IsNull() || !res.Steps[1].LatencyMs.IsNull() || *res.Steps[1].Skipped {
		t.Fatalf("check = %d %s", a.status, a.body)
	}
	for _, tc := range []struct {
		steps []connections.Step
		info  []byte
		code  gen.NullableConnectionCheckCode
	}{
		{steps: []connections.Step{{Name: connections.StepInfo, Via: "direct",
			Message: "The bridge speaks protocol version 2; this Muster speaks 1.", Code: "protocol_version_unknown"},
			{Name: connections.StepEventDelivery, Skipped: true, Via: "direct"}},
			code: gen.NullableConnectionCheckCodeProtocolVersionUnknown},
		{steps: []connections.Step{{Name: connections.StepInfo, Via: "direct",
			Message: "The bridge's answer does not match the protocol: info is larger than 64 KiB",
			Code:    "protocol_mismatch"}, {Name: connections.StepEventDelivery, Skipped: true, Via: "direct"}},
			code: gen.NullableConnectionCheckCodeProtocolMismatch},
		{steps: []connections.Step{{Name: connections.StepInfo, OK: true, Via: "direct"},
			{Name: connections.StepEventDelivery, Via: "direct", Message: "The bridge does not offer push event delivery.",
				Code: "event_delivery_not_offered"}}, info: []byte(bridgeInfoJSON),
			code: gen.NullableConnectionCheckCodeEventDeliveryNotOffered},
	} {
		fc.check = connections.CheckResult{Bridge: true, Info: tc.info, Steps: tc.steps}
		a = x.as(t, connectionsWriter, http.MethodPost, path, "")
		decodeInto(t, a, &res)
		failed := res.Steps[0]
		if !failed.Ok {
			if !res.Info.IsNull() || !res.Steps[1].LatencyMs.IsNull() || !*res.Steps[1].Skipped {
				t.Errorf("a failed info: %s", a.body)
			}
		} else {
			failed = res.Steps[1]
			if !failed.LatencyMs.IsNull() {
				t.Errorf("event_delivery has a latency: %s", a.body)
			}
		}
		if a.status != http.StatusOK || res.Ok || failed.Code.MustGet() != tc.code ||
			failed.Message.MustGet() == "" {
			t.Errorf("%s = %d %s", tc.code, a.status, a.body)
		}
	}
	fc.check = connections.CheckResult{OK: true, Steps: []connections.Step{{Name: connections.StepToken, OK: true,
		Via: "direct"}}}
	if a := x.as(t, connectionsWriter, http.MethodPost, "/api/v1/connections/"+connectionID+"/checks", ""); a.status !=
		http.StatusOK || strings.Contains(string(a.body), `"info"`) {
		t.Errorf("a Mattermost check has info: %s", a.body)
	}
	fc.check = connections.CheckResult{Bridge: true, Info: []byte("not json")}
	if a := x.as(t, connectionsWriter, http.MethodPost, path, ""); a.status != http.StatusOK ||
		!strings.Contains(string(a.body), `"info":null`) {
		t.Errorf("an unreadable info = %d %s", a.status, a.body)
	}
	// A stored copy that does not decode is none, as the domain's StoredInfo reads it, and fails no list.
	fc.list[len(fc.list)-1].BridgeInfo = []byte(`{"bridge":5}`)
	a = x.as(t, connectionsReader, http.MethodGet, "/api/v1/connections/CNAAAAAAAAAAB1", "")
	var unread gen.BridgeConnection
	decodeInto(t, a, &unread)
	if a.status != http.StatusOK || !unread.Info.IsNull() || !unread.InfoReadAt.IsNull() || unread.Name != "checked" {
		t.Errorf("an unreadable stored info = %d %s", a.status, a.body)
	}
	a = x.as(t, connectionsReader, http.MethodGet, "/api/v1/connections?type=bridge", "")
	if a.status != http.StatusOK || !strings.Contains(string(a.body), `"name":"checked"`) {
		t.Errorf("a list with an unreadable stored info = %d %s", a.status, a.body)
	}
	fc.err = connections.ErrNotMattermost
	if a := x.as(t, connectionsWriter, http.MethodGet, "/api/v1/connections/CNAAAAAAAAAAB1/channels", ""); a.status !=
		http.StatusConflict {
		t.Errorf("the channels of a Bridge = %d %s", a.status, a.body)
	}
}

// The secrets of the Slack bodies, which no answer may carry.
const (
	slackBotToken      = "xoxb-muster-dev-slack-token-b"
	slackAppToken      = "xapp-muster-dev-slack-token-b"
	slackSigningSecret = "muster-dev-slack-signing-secret-b"
)

// TestSlackConnectionAPI covers C-24.FR-1 and FR-2 on the API: a Slack Connection is created with its event mode and
// its three write-only secrets, which reach the service and no answer; it reads with the three secrets' status, its
// callback_url and what its last check learned, null before one; the type filter selects it; its check answers the
// steps token, scopes without latency, and app_token, with the new codes, and slack with the workspace, the bot and
// the app, null without the step app_token, and null for the other types; a refusal of the service is 422 at its
// pointer; its channel list is 409; a Slack draft matches no member of ConnectionDraft and is 400.
func TestSlackConnectionAPI(t *testing.T) {
	x, fc := newConnectionsAPI(t)
	body := `{"type":"slack","name":"sl","event_mode":"socket","bot_token":"` + slackBotToken + `","app_token":"` +
		slackAppToken + `","slack_signing_secret":"` + slackSigningSecret + `","proxy":{"enabled":false},` +
		`"limiter":{"limit":50,"per_seconds":60}}`
	secretFree := func(t *testing.T, a answer) {
		t.Helper()
		for _, v := range []string{slackBotToken, slackAppToken, slackSigningSecret, `"bot_token"`, `"app_token"`,
			`"slack_signing_secret"`} {
			if strings.Contains(string(a.body), v) {
				t.Errorf("the answer has %s: %s", v, a.body)
			}
		}
	}
	a := x.as(t, connectionsWriter, http.MethodPost, "/api/v1/connections", body)
	m := a.json(t)
	if a.status != http.StatusCreated || m["type"] != "slack" || m["event_mode"] != "socket" ||
		m["bot_token_status"].(map[string]any)["set"] != true || m["app_token_status"].(map[string]any)["set"] != true ||
		m["slack_signing_secret_status"].(map[string]any)["set"] != true ||
		m["callback_url"] != "http://localhost:8081/api/v1/callbacks/slack/CNAAAAAAAAAAA9" {
		t.Fatalf("create = %d %s", a.status, a.body)
	}
	for _, k := range []string{"bot_name", "bot_user_id", "team_id", "team_name", "app_id"} {
		if v, ok := m[k]; !ok || v != nil {
			t.Errorf("%s = %v, present %v", k, v, ok)
		}
	}
	for _, k := range []string{"server_url", "base_url", "bot_api_base_url"} {
		if _, ok := m[k]; ok {
			t.Errorf("a Slack Connection has %s: %s", k, a.body)
		}
	}
	secretFree(t, a)
	in := fc.inputs[0]
	if in.Type != connections.TypeSlack || in.Name != "sl" || in.EventMode != connections.EventModeSocket ||
		in.BotToken != keyring.Replace(slackBotToken) || in.AppToken != keyring.Replace(slackAppToken) ||
		in.SlackSigningSecret != keyring.Replace(slackSigningSecret) || in.Limiter.Limit != 50 ||
		in.Limiter.PerSeconds != 60 || in.ServerURL != "" || in.Proxy.Enabled {
		t.Errorf("input %+v", in)
	}
	withoutSecrets := `{"type":"slack","name":"sl","event_mode":"http","proxy":{"enabled":false},` +
		`"limiter":{"limit":50,"per_seconds":60}}`
	a = x.as(t, connectionsWriter, http.MethodPut, "/api/v1/connections/CNAAAAAAAAAAA9", withoutSecrets, "If-Match",
		`"1"`)
	if in := fc.inputs[len(fc.inputs)-1]; a.status != http.StatusOK || in.BotToken.Given || in.AppToken.Given ||
		in.SlackSigningSecret.Given || in.EventMode != connections.EventModeHTTP {
		t.Errorf("update without secrets = %d %s, %+v", a.status, a.body, in)
	}
	for name, bad := range map[string]string{
		"an unknown event mode": strings.Replace(withoutSecrets, `"http"`, `"poll"`, 1),
		"no event mode":         strings.Replace(withoutSecrets, `"event_mode":"http",`, ``, 1),
	} {
		if a := x.as(t, connectionsWriter, http.MethodPost, "/api/v1/connections", bad); a.status !=
			http.StatusBadRequest {
			t.Errorf("%s = %d %s", name, a.status, a.body)
		}
	}
	fc.err = &connections.FieldError{Pointer: "/slack_signing_secret", Code: connections.CodeRequired,
		Detail: "HTTP mode needs the Slack signing secret."}
	a = x.as(t, connectionsWriter, http.MethodPost, "/api/v1/connections", withoutSecrets)
	if errs := problemErrors(t, a); a.status != http.StatusUnprocessableEntity || len(errs) != 1 ||
		errs[0]["pointer"] != "/slack_signing_secret" || errs[0]["code"] != "required" {
		t.Errorf("a refusal = %d %s", a.status, a.body)
	}
	fc.err = nil

	team, teamName, bot, botUser, app := "T0MUSTERDEVB", "Fake workspace B", "muster-dev-bot-b", "U0MUSTERBOTB",
		"A0MUSTERDEVB"
	fc.list = append(fc.list, connections.Connection{ID: 6, PublicID: "CNAAAAAAAAAAS1", Type: connections.TypeSlack,
		Name: "checked", EventMode: connections.EventModeHTTP, BotToken: keyring.SecretStatus{Set: true, UpdatedAt: &t0},
		AppToken: keyring.SecretStatus{Set: true, UpdatedAt: &t0}, SlackSigningSecret: keyring.SecretStatus{Set: true,
			UpdatedAt: &t0}, BotUsername: &bot, BotUserID: &botUser, SlackTeamID: &team, SlackTeamName: &teamName,
		SlackAppID: &app, Limiter: connections.Limiter{Limit: 50, PerSeconds: 60}, CreatedAt: t0, Version: 4})
	a = x.as(t, connectionsReader, http.MethodGet, "/api/v1/connections/CNAAAAAAAAAAS1", "")
	var sl gen.SlackConnection
	decodeInto(t, a, &sl)
	if a.status != http.StatusOK || sl.EventMode != gen.SlackEventModeHttp || sl.BotName.MustGet() != bot ||
		sl.BotUserId.MustGet() != botUser || sl.TeamId.MustGet() != team || sl.TeamName.MustGet() != teamName ||
		sl.AppId.MustGet() != app || !sl.AppTokenStatus.Set || !sl.SlackSigningSecretStatus.Set || *sl.Etag != `"4"` ||
		*sl.CallbackUrl != "http://localhost:8081/api/v1/callbacks/slack/CNAAAAAAAAAAS1" {
		t.Fatalf("get = %d %s", a.status, a.body)
	}
	a = x.as(t, connectionsReader, http.MethodGet, "/api/v1/connections?type=slack", "")
	var list gen.ConnectionList
	decodeInto(t, a, &list)
	if a.status != http.StatusOK || len(list.Items) != 2 || *fc.filters[len(fc.filters)-1].Type != "slack" {
		t.Fatalf("list = %d %s", a.status, a.body)
	}
	for _, it := range list.Items {
		if kind, _ := it.Discriminator(); kind != "slack" {
			t.Errorf("listed %s", kind)
		}
	}

	path := "/api/v1/connections/CNAAAAAAAAAAS1/checks"
	fc.check = connections.CheckResult{OK: true, BotName: bot, Steps: []connections.Step{
		{Name: connections.StepToken, OK: true, Latency: 9 * time.Millisecond, Via: "direct"},
		{Name: connections.StepScopes, OK: true, Via: "direct"},
		{Name: connections.StepAppToken, OK: true, Latency: 12 * time.Millisecond, Via: "direct"}},
		Slack: &connections.SlackIdentity{TeamID: team, TeamName: teamName, BotUserID: botUser, AppID: app}}
	a = x.as(t, connectionsWriter, http.MethodPost, path, "")
	var res gen.ConnectionCheckResult
	decodeInto(t, a, &res)
	info, err := res.Slack.Get()
	if a.status != http.StatusOK || !res.Ok || err != nil || info.TeamId != team || info.TeamName != teamName ||
		info.BotUserId != botUser || info.AppId.MustGet() != app || res.BotName.MustGet() != bot ||
		len(res.Steps) != 3 || res.Steps[0].Name != gen.ConnectionCheckStepNameToken ||
		res.Steps[0].LatencyMs.MustGet() != 9 || res.Steps[1].Name != gen.ConnectionCheckStepNameScopes ||
		!res.Steps[1].LatencyMs.IsNull() || res.Steps[2].Name != gen.ConnectionCheckStepNameAppToken ||
		res.Steps[2].LatencyMs.MustGet() != 12 || strings.Contains(string(a.body), `"info"`) {
		t.Fatalf("check = %d %s", a.status, a.body)
	}
	fc.check = connections.CheckResult{BotName: bot, Steps: []connections.Step{
		{Name: connections.StepToken, OK: true, Via: "direct"},
		{Name: connections.StepScopes, Via: "direct", Message: "The bot token lacks the scopes users:read.email.",
			Code: "missing_scope"}},
		Slack: &connections.SlackIdentity{TeamID: team, TeamName: teamName, BotUserID: botUser}}
	a = x.as(t, connectionsWriter, http.MethodPost, path, "")
	decodeInto(t, a, &res)
	if info, err := res.Slack.Get(); a.status != http.StatusOK || res.Ok || err != nil || !info.AppId.IsNull() ||
		res.Steps[1].Code.MustGet() != gen.NullableConnectionCheckCodeMissingScope ||
		res.Steps[1].Message.MustGet() != "The bot token lacks the scopes users:read.email." {
		t.Errorf("missing scope = %d %s", a.status, a.body)
	}
	for _, tc := range []struct {
		step connections.Step
		code gen.NullableConnectionCheckCode
	}{
		{connections.Step{Name: connections.StepToken, Via: "direct", Message: "Slack refused the token.",
			Code: "token_invalid"}, gen.NullableConnectionCheckCodeTokenInvalid},
		{connections.Step{Name: connections.StepToken, Via: "direct", Message: "This is not a bot token.",
			Code: "wrong_token_type"}, gen.NullableConnectionCheckCodeWrongTokenType},
		{connections.Step{Name: connections.StepAppToken, Via: "proxy",
			Message: "The app-level token belongs to another Slack app than the bot token.", Code: "app_mismatch"},
			gen.NullableConnectionCheckCodeAppMismatch},
	} {
		fc.check = connections.CheckResult{Steps: []connections.Step{tc.step}}
		a = x.as(t, connectionsWriter, http.MethodPost, path, "")
		decodeInto(t, a, &res)
		if a.status != http.StatusOK || res.Ok || res.Steps[0].Code.MustGet() != tc.code ||
			res.Steps[0].Message.MustGet() != tc.step.Message || !res.Slack.IsNull() {
			t.Errorf("%s = %d %s", tc.code, a.status, a.body)
		}
	}
	fc.check = connections.CheckResult{OK: true, Steps: []connections.Step{{Name: connections.StepToken, OK: true,
		Via: "direct"}}}
	if a := x.as(t, connectionsWriter, http.MethodPost, "/api/v1/connections/"+connectionID+"/checks", ""); a.status !=
		http.StatusOK || !strings.Contains(string(a.body), `"slack":null`) {
		t.Errorf("a Mattermost check = %s", a.body)
	}

	fc.err = connections.ErrNotMattermost
	if a := x.as(t, connectionsWriter, http.MethodGet, "/api/v1/connections/CNAAAAAAAAAAS1/channels", ""); a.status !=
		http.StatusConflict {
		t.Errorf("the channels of a Slack Connection = %d %s", a.status, a.body)
	}
	fc.err = nil
	n := len(fc.drafts)
	draft := `{"type":"slack","event_mode":"socket","bot_token":"` + slackBotToken + `","proxy":{"enabled":false}}`
	if a := x.as(t, connectionsWriter, http.MethodPost, "/api/v1/connections/checks", draft); a.status !=
		http.StatusBadRequest || len(fc.drafts) != n {
		t.Errorf("a Slack draft = %d %s", a.status, a.body)
	}
}

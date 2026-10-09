// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
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
// asked and the version they named, and the answers of the check and the channel list.
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
		BotToken: keyring.SecretStatus{Set: true, UpdatedAt: &t0}, Limiter: in.Limiter, CreatedAt: t0, Version: 1}
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

func (f *fakeConnections) Check(_ context.Context, id string, baseURL *string) (connections.CheckResult, error) {
	f.baseURLs = append(f.baseURLs, baseURL)
	if f.find(id) < 0 {
		return connections.CheckResult{}, connections.ErrNotFound
	}
	return f.check, f.err
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
		!res.Steps[0].Message.IsNull() || len(res.Warnings) != 1 || res.Warnings[0] != gen.PressAnswersInThread {
		t.Errorf("check = %d %s", a.status, a.body)
	}
	fc.check = connections.CheckResult{Steps: []connections.Step{{Name: connections.StepToken,
		Latency: 3 * time.Millisecond, Via: mattermost.ViaProxy, Message: mattermost.MessageTokenInvalid}}}
	a = x.as(t, connectionsWriter, http.MethodPost, path, "")
	decodeInto(t, a, &res)
	if a.status != http.StatusOK || res.Ok || !res.BotName.IsNull() || *res.Steps[0].Via != "proxy" ||
		res.Steps[0].Message.MustGet() != "The bot token is not valid." || !strings.Contains(string(a.body),
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
				Message: "A webhook is set at in.example.org."}}}
	path := "/api/v1/connections/CNAAAAAAAAAAT1/checks"
	a = x.as(t, connectionsWriter, http.MethodPost, path, "{}")
	var res gen.ConnectionCheckResult
	decodeInto(t, a, &res)
	if a.status != http.StatusOK || !res.WebhookSet.MustGet() || res.PendingUpdates.MustGet() != 4 ||
		len(res.Steps) != 3 || *res.Steps[2].Skipped || res.Steps[2].Message.MustGet() != "A webhook is set at in.example.org." ||
		fc.baseURLs[len(fc.baseURLs)-1] != nil {
		t.Fatalf("check = %d %s", a.status, a.body)
	}
	fc.check = connections.CheckResult{Steps: []connections.Step{
		{Name: "dry_probe", Latency: time.Millisecond, Via: "direct", Message: "This is not a Bot API."},
		{Name: "get_me", Skipped: true, Via: "direct"}, {Name: "get_webhook_info", Skipped: true, Via: "direct"}}}
	a = x.as(t, connectionsWriter, http.MethodPost, path, `{"base_url":"http://127.0.0.1:18081/other/"}`)
	decodeInto(t, a, &res)
	if got := fc.baseURLs[len(fc.baseURLs)-1]; a.status != http.StatusOK || got == nil ||
		*got != "http://127.0.0.1:18081/other/" || !*res.Steps[1].Skipped || !res.Steps[1].LatencyMs.IsNull() ||
		res.Steps[0].Message.MustGet() != "This is not a Bot API." || !res.WebhookSet.IsNull() ||
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

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/devmode"
)

// postIngest sends body to an ingestion URL with the bearer token, none when it is empty, and returns the status.
func postIngest(t *testing.T, url, token, body string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// fakeSend asks the fake Alertmanager to send and returns the status Muster answered.
func fakeSend(t *testing.T, h *Harness, body string) int {
	t.Helper()
	a := call(t, http.MethodPost, h.Fakes.Alertmanager+"/_fake/send", body)
	var out struct {
		Status int `json:"status"`
	}
	decode(t, a, &out)
	if a.status != http.StatusOK {
		t.Fatalf("send %s = %d %s", body, a.status, a.body)
	}
	return out.Status
}

// TestIngestion is S-018 against `muster dev`: an Integration and its token with the snippet (C-05.AC-6), webhooks from
// the fake Alertmanager stored as received (C-05.AC-1, AC-8), the refusals and their counters (C-05.AC-2, AC-7,
// C-04.FR-4), no token from a path in the logs (C-05.AC-5), the Stored Snapshots for Admins only, the deletion
// (C-05.AC-3) and the demo Integration (C-01.FR-13).
func TestIngestion(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	admin := newAgent(t, r.App)
	if a := admin.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		t.Fatalf("sign in = %d %s", a.status, a.body)
	}
	in := admin.json(http.MethodPost, "/api/v1/integrations", `{"name":"prod-eu","connection_mode":"webhook_only",
		"static_labels":{"cluster":"prod-eu"},"duplicate_window_seconds":45,"heartbeat":{"enabled":false}}`,
		http.StatusCreated)
	id := in["id"].(string)
	created := admin.json(http.MethodPost, "/api/v1/integrations/"+id+"/tokens", `{"name":"rotation-1"}`,
		http.StatusCreated)
	token, snippet := created["value"].(string), created["alertmanager_snippet"].(string)
	for _, want := range []string{"url: http://localhost:8081/api/v1/ingest", "send_resolved: true", "max_alerts: 0",
		"credentials: " + token, "continue: true", "repeat_interval: 10m"} {
		if !strings.Contains(snippet, want) {
			t.Errorf("the snippet lacks %q:\n%s", want, snippet)
		}
	}
	if !strings.HasPrefix(token, "mstr_int_") {
		t.Fatalf("token %q", token[:9])
	}
	tokens := admin.do(http.MethodGet, "/api/v1/integrations/"+id+"/tokens", "")
	if tokens.status != http.StatusOK || strings.Contains(string(tokens.body), `"value"`) ||
		strings.Contains(string(tokens.body), token) {
		t.Errorf("token list = %d %s", tokens.status, tokens.body)
	}
	if n := h.count(t, `SELECT count(*) FROM integration_tokens WHERE name = 'rotation-1'
		AND octet_length(token_hash) = 32`); n != 1 {
		t.Errorf("hashed tokens = %d", n)
	}

	fam := h.Fakes.Alertmanager + "/_fake"
	if a := call(t, http.MethodPost, fam+"/receivers", `{"name":"prod-eu","url":"`+r.Ingest+`/api/v1/ingest",
		"token":"`+token+`"}`); a.status != http.StatusNoContent {
		t.Fatalf("register = %d %s", a.status, a.body)
	}
	payload := `{"version":"4","groupKey":"{}:{alertname=\"T\"}","status":"firing","alerts":[]}`
	if s := fakeSend(t, h, `{"receiver":"prod-eu","payload":`+payload+`}`); s != http.StatusAccepted {
		t.Errorf("webhook = %d", s)
	}
	list := admin.json(http.MethodGet, "/api/v1/stored-snapshots?integration="+id, "", http.StatusOK)
	items := list["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["state"] != "pending" {
		t.Errorf("stored snapshots = %v", list)
	}
	first := admin.json(http.MethodGet, "/api/v1/stored-snapshots/"+items[0].(map[string]any)["id"].(string), "",
		http.StatusOK)
	if first["body"] != payload || first["content_type"] != "application/json" {
		t.Errorf("first snapshot = %v", first)
	}
	if s := fakeSend(t, h, `{"receiver":"prod-eu","raw":"{}","no_token":true}`); s != http.StatusUnauthorized {
		t.Errorf("without a token = %d", s)
	}
	if s := postIngest(t, r.Ingest+"/api/v1/ingest/"+token, "", "{}"); s != http.StatusAccepted {
		t.Errorf("token in the path = %d", s)
	}
	if s := postIngest(t, r.Ingest+"/api/v1/ingest", "mstr_int_nope", "{}"); s != http.StatusUnauthorized {
		t.Errorf("unknown token = %d", s)
	}
	if v := r.Metric(t, `muster_ingest_requests_total{integration="unknown",outcome="unauthorized"}`); v != "2" {
		t.Errorf("unknown unauthorized = %q, want 2", v)
	}

	// C-05.AC-2: the token before the size.
	if s := fakeSend(t, h, `{"receiver":"prod-eu","size_bytes":17000000}`); s != http.StatusRequestEntityTooLarge {
		t.Errorf("17 MB = %d", s)
	}
	if s := fakeSend(t, h, `{"receiver":"prod-eu","size_bytes":17000000,"no_token":true}`); s !=
		http.StatusUnauthorized {
		t.Errorf("17 MB without a token = %d", s)
	}
	if v := r.Metric(t, `muster_ingest_requests_total{integration="`+id+`",outcome="too_large"}`); v != "1" {
		t.Errorf("too_large = %q, want 1", v)
	}

	// C-05.AC-8: any body is stored as received.
	if s := fakeSend(t, h, `{"receiver":"prod-eu","raw":"not json","content_type":"text/plain"}`); s !=
		http.StatusAccepted {
		t.Errorf("not json = %d", s)
	}
	newest := func() map[string]any {
		l := admin.json(http.MethodGet, "/api/v1/stored-snapshots?integration="+id+"&limit=1", "", http.StatusOK)
		ss := l["items"].([]any)[0].(map[string]any)["id"].(string)
		return admin.json(http.MethodGet, "/api/v1/stored-snapshots/"+ss, "", http.StatusOK)
	}
	if sn := newest(); sn["body"] != "not json" || sn["body_encoding"] != "utf8" || sn["content_type"] != "text/plain" ||
		sn["state"] != "pending" {
		t.Errorf("not json snapshot = %v", sn)
	}
	if s := postIngest(t, r.Ingest+"/api/v1/ingest", token, "\xff\xfe"); s != http.StatusAccepted {
		t.Errorf("binary body = %d", s)
	}
	if sn := newest(); sn["body_encoding"] != "base64" || sn["body"] != base64.StdEncoding.EncodeToString([]byte("\xff\xfe")) {
		t.Errorf("binary snapshot = %v", sn)
	}

	// C-04.FR-4: API tokens on ingestion, and the Integration token on the API.
	pat := admin.json(http.MethodPost, "/api/v1/me/personal-access-tokens",
		`{"name":"p","permissions":["integrations:read"]}`, http.StatusCreated)["value"].(string)
	if s := postIngest(t, r.Ingest+"/api/v1/ingest", pat, "{}"); s != http.StatusUnauthorized {
		t.Errorf("a Personal access token on ingestion = %d", s)
	}
	if a := bearerGet(t, r.App+"/api/v1/integrations", token); a.status != http.StatusUnauthorized {
		t.Errorf("an Integration token on the API = %d", a.status)
	}

	// C-05.AC-5: no token from a path reaches the log.
	if strings.Contains(r.Output(), token) {
		t.Error("the Integration token reached the log")
	}
	found := false
	for line := range strings.Lines(r.Output()) {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["event"] == "snapshot_accepted" &&
			m["route_pattern"] == "/api/v1/ingest/{ingest_token}" {
			found = true
		}
	}
	if !found {
		t.Error("no snapshot_accepted line names the route pattern of the path token")
	}

	// Stored Snapshots are for Admins.
	localUser(t, admin, "resp@example.org", "responder-password-1")
	resp := newAgent(t, r.App)
	if a := resp.signIn("resp@example.org", "responder-password-1"); a.status != http.StatusCreated {
		t.Fatalf("responder sign in = %d %s", a.status, a.body)
	}
	if a := resp.do(http.MethodGet, "/api/v1/stored-snapshots?integration="+id, ""); a.status != http.StatusForbidden {
		t.Errorf("stored snapshots for a Responder = %d", a.status)
	}

	// C-05.AC-3: deletion.
	if a := admin.do(http.MethodDelete, "/api/v1/integrations/"+id, ""); a.status != http.StatusNoContent {
		t.Fatalf("delete = %d %s", a.status, a.body)
	}
	if s := fakeSend(t, h, `{"receiver":"prod-eu","raw":"{}"}`); s != http.StatusUnauthorized {
		t.Errorf("after the deletion = %d", s)
	}
	all := admin.json(http.MethodGet, "/api/v1/integrations", "", http.StatusOK)
	for _, it := range all["items"].([]any) {
		if it.(map[string]any)["id"] == id {
			t.Error("the deleted Integration is listed")
		}
	}
	left := admin.json(http.MethodGet, "/api/v1/stored-snapshots?integration="+id, "", http.StatusOK)
	if n := len(left["items"].([]any)); n != 4 {
		t.Errorf("stored snapshots after the deletion = %d, want 4", n)
	}
	if v := r.Metric(t, `muster_integration_info{integration="`+id+`",name="prod-eu"}`); v != "" {
		t.Errorf("muster_integration_info of the deleted Integration = %q", v)
	}

	// C-01.FR-13: the demo Integration and the receiver muster.
	if s := fakeSend(t, h, `{"receiver":"muster"}`); s != http.StatusAccepted {
		t.Errorf("the demo receiver = %d", s)
	}
	if n := h.count(t, `SELECT count(*) FROM integrations WHERE name = 'dev-alertmanager' AND deleted_at IS NULL
		AND static_labels = '{"cluster":"dev"}'`); n != 1 {
		t.Errorf("demo integrations = %d", n)
	}
}

// TestIngestionOnTheAppPort is C-02.AC-8: with MUSTER_LISTEN_INGEST equal to MUSTER_LISTEN_APP, a webhook to the shared
// port gets 202 and the API answers on the same port.
func TestIngestionOnTheAppPort(t *testing.T) {
	h := Start(t, FakesInProcess)
	r := h.StartReplica(t, ReplicaOptions{ListenApp: ":9080", ListenIngest: ":9080"})
	admin := newAgent(t, r.App)
	if a := admin.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		t.Fatalf("sign in = %d %s", a.status, a.body)
	}
	in := admin.json(http.MethodPost, "/api/v1/integrations", `{"name":"shared","connection_mode":"webhook_only",
		"static_labels":{},"duplicate_window_seconds":45,"heartbeat":{"enabled":false}}`, http.StatusCreated)
	token := admin.json(http.MethodPost, "/api/v1/integrations/"+in["id"].(string)+"/tokens", "",
		http.StatusCreated)["value"].(string)
	if s := postIngest(t, r.App+"/api/v1/ingest", token, "{}"); s != http.StatusAccepted {
		t.Errorf("ingestion on the app port = %d", s)
	}
	if a := admin.do(http.MethodGet, "/api/v1/integrations", ""); a.status != http.StatusOK {
		t.Errorf("the API on the shared port = %d", a.status)
	}
}

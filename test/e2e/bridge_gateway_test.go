// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
)

// gatewayContact is the contact the checks of the role gateway call.
const gatewayContact = "+10000000001"

// rolesOf is the roles of the Connection id as its read gives them, with the name of its messenger.
func (e *brd) rolesOf(id string) ([]string, string) {
	e.t.Helper()
	c := e.api.json(http.MethodGet, "/api/v1/connections/"+id, "", http.StatusOK)
	var roles []string
	for _, r := range c["roles"].([]any) {
		roles = append(roles, r.(string))
	}
	messenger := ""
	if info, ok := c["info"].(map[string]any); ok {
		messenger = fmt.Sprint(info["messenger"].(map[string]any)["name"])
	}
	return roles, messenger
}

// listedAs are the names of the Connections the role filter lists.
func (e *brd) listedAs(role string) []string {
	e.t.Helper()
	var names []string
	for _, item := range e.api.json(http.MethodGet, "/api/v1/connections?role="+role, "",
		http.StatusOK)["items"].([]any) {
		names = append(names, item.(map[string]any)["name"].(string))
	}
	return names
}

// gatewayToken writes the token of the fake bridge's gateway bot to a file of the test.
func gatewayToken(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(devmode.GatewayConnectionBotToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestBridgeGateway is C-30.AC-1, C-30.FR-1, FR-2 and FR-10, C-22.FR-2, FR-3, FR-11 and FR-21 as revised and C-18.FR-10
// against `muster dev` and its fake bridge: the demo Connection "Example Gateway", checked at start, reads the role
// gateway alone and the provider "Example Gateway", refuses a Destination with gateway_only, is listed by the role
// filter gateway and not messenger, and is not offered for Account links; "Example Bridge", whose info declares no
// roles, reads messenger; a Connection to a bot whose info declares both roles takes a Destination; a
// notification_acknowledged event polled from the gateway bot is recorded and logged as unhandled; the Request log
// has the requests to the gateway; `muster bridge check` passes the role gateway interactively, reports a replay that
// is none and a fault scripted on sendNotification, and skips the gateway steps for a messenger.
func TestBridgeGateway(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newBRD(t, h, r)
	t.Cleanup(func() {
		restore(t, http.MethodPut, e.fb+"/config", `{"token":"`+fakebridge.SecondToken+`","roles":null}`)
	})
	gw := e.connectionNamed(devmode.GatewayConnectionName)

	// C-30.AC-1: the demo gateway, checked at start, reads the role gateway alone.
	if roles, messenger := e.rolesOf(gw); !slices.Equal(roles, []string{"gateway"}) ||
		messenger != fakebridge.GatewayName {
		t.Fatalf("Example Gateway: roles %v, messenger %q", roles, messenger)
	}
	if roles, _ := e.rolesOf(e.conn); !slices.Equal(roles, []string{"messenger"}) {
		t.Errorf("Example Bridge, whose info declares no roles: roles %v", roles)
	}
	a := e.api.do(http.MethodPost, "/api/v1/destinations", fmt.Sprintf(`{"type":"bridge","name":"gw",
		"connection_id":%q,"channel_id":%q,"mentions":%s,"limiter":{"limit":30,"per_seconds":60}}`, gw,
		fakebridge.ChannelAlerts, mentionSettings(nil)))
	var refused struct {
		Status int `json:"status"`
		Errors []struct {
			Pointer string `json:"pointer"`
			Code    string `json:"code"`
		} `json:"errors"`
	}
	decode(t, a, &refused)
	if a.status != http.StatusUnprocessableEntity || len(refused.Errors) != 1 ||
		refused.Errors[0].Pointer != "/connection_id" || refused.Errors[0].Code != "gateway_only" {
		t.Errorf("a Destination of the gateway = %d %s", a.status, a.body)
	}

	// Both roles: a Connection to the second bot, whose info declares both, takes a Destination.
	e.configure(`{"token":"` + fakebridge.SecondToken + `","roles":["messenger","gateway"]}`)
	both := e.api.json(http.MethodPost, "/api/v1/connections", `{"type":"bridge","name":"both","base_url":"`+
		h.Fakes.Bridge+`","event_delivery":"poll","bot_token":"`+fakebridge.SecondToken+`","proxy":{"enabled":false},`+
		`"limiter":{"limit":10,"per_seconds":1}}`, http.StatusCreated)["id"].(string)
	if res := e.api.json(http.MethodPost, "/api/v1/connections/"+both+"/checks", "", http.StatusOK); res["ok"] != true {
		t.Fatalf("the check of both = %v", res)
	}
	if roles, _ := e.rolesOf(both); !slices.Equal(roles, []string{"messenger", "gateway"}) {
		t.Errorf("both: roles %v", roles)
	}
	e.api.json(http.MethodPost, "/api/v1/destinations", fmt.Sprintf(`{"type":"bridge","name":"both-alerts",
		"connection_id":%q,"channel_id":%q,"mentions":%s,"limiter":{"limit":30,"per_seconds":60}}`, both,
		fakebridge.ChannelAlerts, mentionSettings(nil)), http.StatusCreated)

	// The role filter.
	messengers, gateways := e.listedAs("messenger"), e.listedAs("gateway")
	if slices.Contains(messengers, devmode.GatewayConnectionName) || !slices.Contains(messengers, "both") ||
		!slices.Contains(messengers, devmode.BridgeConnectionName) ||
		!slices.Contains(messengers, devmode.ConnectionName) ||
		!slices.Contains(messengers, devmode.SlackConnectionName) {
		t.Errorf("messengers %v", messengers)
	}
	if !slices.Equal(gateways, []string{devmode.GatewayConnectionName, "both"}) {
		t.Errorf("gateways %v", gateways)
	}

	// C-18.FR-10: the gateway alone is not offered for Account links; both roles are.
	var linkable []string
	for _, x := range e.api.json(http.MethodGet, "/api/v1/me/account-links/connections", "",
		http.StatusOK)["items"].([]any) {
		linkable = append(linkable, x.(map[string]any)["name"].(string))
	}
	if slices.Contains(linkable, devmode.GatewayConnectionName) || !slices.Contains(linkable, "both") {
		t.Errorf("linkable %v", linkable)
	}

	// C-22.FR-11: a notification_acknowledged of the gateway bot, polled by the Leader, is handled once; its data is
	// not Muster's, so it is refused and answered so (C-30.FR-6).
	sent := call(t, http.MethodPost, h.Fakes.Bridge+"/v1/notifications", `{"contact":"`+gatewayContact+`",`+
		`"text":"A test","language":"en","action":{"data":"e2e-data","label":"Press 1"}}`,
		"Authorization", "Bearer "+devmode.GatewayConnectionBotToken, "Idempotency-Key", "e2e-1",
		"Content-Type", "application/json")
	var n struct {
		ID       string `json:"notification_id"`
		Replayed bool   `json:"replayed"`
	}
	decode(t, sent, &n)
	if sent.status != http.StatusCreated || n.ID == "" || n.Replayed {
		t.Fatalf("sendNotification = %d %s", sent.status, sent.body)
	}
	var queued fakebridge.Event
	decode(t, call(t, http.MethodPost, e.fb+"/acknowledge", `{"notification_id":"`+n.ID+`"}`), &queued)
	var ev struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(queued.Event, &ev); err != nil || queued.Bot != 3 {
		t.Fatalf("acknowledge = %+v", queued)
	}
	e.waitLine("gateway_acknowledge_refused", map[string]any{"connection": gw, "call": "",
		"reason": "not_verified"})
	if n := h.count(t, `SELECT count(*) FROM bridge_events_seen WHERE event_id = '`+ev.ID+`'`); n != 1 {
		t.Errorf("%d records of the event", n)
	}

	// C-30.FR-10: the Request log of the gateway has its info and the poll that brought the event.
	records := e.requestLog("connection=" + gw)
	if len(ofOperation(records, "getInfo")) == 0 || len(ofOperation(records, "pollEvents")) == 0 {
		t.Errorf("the Request log of the gateway %v", records)
	}
	// The path prefix of the base URL may carry a secret, so a record shows it masked.
	for _, rec := range records {
		if !strings.HasPrefix(fmt.Sprint(rec["url"]), h.Fakes.Bridge+"/[redacted]/v1/") {
			t.Errorf("a record of the gateway at %v", rec["url"])
		}
	}

	// C-22.FR-21: muster bridge check of the role gateway, interactive, acknowledged through the fake.
	token := gatewayToken(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	//nolint:gosec // G204: the binary under test, from MUSTER_E2E_BINARY
	cmd := exec.CommandContext(ctx, h.binary, "bridge", "check", h.Fakes.Bridge, "--token-file", token, "--contact",
		gatewayContact)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for sc := bufio.NewScanner(out); sc.Scan(); {
		line := sc.Text()
		lines = append(lines, line)
		if _, id, ok := strings.Cut(line, ", notification_id "); ok && strings.HasPrefix(line, "WAIT ") {
			e.fake(http.MethodPost, e.fb+"/acknowledge", `{"notification_id":"`+id+`"}`)
		}
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("exit %v, stdout:\n%s\nstderr:\n%s", err, strings.Join(lines, "\n"), stderr.String())
	}
	if lines[1] != "ROLES gateway" || !slices.Contains(lines, "PASS notification") ||
		lines[len(lines)-1] != "PASS notification_acknowledged" ||
		!slices.Contains(lines, "SKIP post: the bridge is not in the role messenger") ||
		strings.Contains(strings.Join(lines, "\n")+stderr.String(), devmode.GatewayConnectionBotToken) {
		t.Fatalf("stdout:\n%s\nstderr:\n%s", strings.Join(lines, "\n"), stderr.String())
	}
	var notifications []fakebridge.Notification
	decode(t, call(t, http.MethodGet, e.fb+"/notifications", ""), &notifications)
	last := notifications[len(notifications)-1]
	if last.Contact != gatewayContact || last.Action == nil || len(last.Action.Data) != 64 || last.Bot != 3 {
		t.Errorf("the check's notification %+v", last)
	}
	var answered []fakebridge.Answer
	decode(t, call(t, http.MethodGet, e.fb+"/answers", ""), &answered)
	if !slices.ContainsFunc(answered, func(a fakebridge.Answer) bool {
		return a.MessageID == last.ID && a.ShownAs == "notification" &&
			a.EventType == fakebridge.TypeNotificationAcknowledged
	}) {
		t.Errorf("answers %+v", answered)
	}

	// A replayed key: the second request with the key answers the first notification, replayed.
	again := call(t, http.MethodPost, h.Fakes.Bridge+"/v1/notifications", `{"contact":"+1","text":"other",`+
		`"language":"en"}`, "Authorization", "Bearer "+devmode.GatewayConnectionBotToken, "Idempotency-Key", "e2e-1",
		"Content-Type", "application/json")
	var replayed struct {
		ID       string `json:"notification_id"`
		Replayed bool   `json:"replayed"`
	}
	decode(t, again, &replayed)
	if again.status != http.StatusCreated || replayed.ID != n.ID || !replayed.Replayed {
		t.Errorf("the replayed key = %d %s", again.status, again.body)
	}

	// A fault scripted on sendNotification fails the step notification, and the check exits 1.
	e.fake(http.MethodPost, e.fb+"/faults", `{"path":"/v1/notifications","status":403,`+
		`"content_type":"application/problem+json","body":"{\"title\":\"Forbidden\",\"status\":403,`+
		`\"class\":\"forbidden\",\"detail\":\"The provider refused the credentials.\"}","times":1}`)
	code, stdout, stderrText := h.runCLIEnv(t, nil, "bridge", "check", h.Fakes.Bridge, "--token-file", token,
		"--contact", gatewayContact, "--no-interactive")
	if code != 1 || !strings.Contains(stdout, "\nFAIL notification: answered 403 forbidden: The provider refused the "+
		"credentials.\n") || !strings.HasSuffix(stdout, "SKIP notification_acknowledged: --no-interactive\n") {
		t.Fatalf("the fault: exit %d, stdout:\n%s\nstderr:\n%s", code, stdout, stderrText)
	}

	// A messenger's check skips the gateway steps.
	messengerToken := filepath.Join(t.TempDir(), "messenger")
	if err := os.WriteFile(messengerToken, []byte(devmode.BridgeConnectionBotToken), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderrText = h.runCLIEnv(t, nil, "bridge", "check", h.Fakes.Bridge, "--token-file", messengerToken,
		"--channel", fakebridge.ChannelAlerts, "--contact", gatewayContact, "--no-interactive")
	if code != 0 || !strings.HasPrefix(stdout, "PASS info\nROLES messenger\n") || !strings.HasSuffix(stdout,
		"SKIP notification: the bridge is not in the role gateway\n"+
			"SKIP notification_acknowledged: the bridge is not in the role gateway\n") {
		t.Fatalf("a messenger: exit %d, stdout:\n%s\nstderr:\n%s", code, stdout, stderrText)
	}

	// The contact of the notifications never reaches Muster's log.
	if strings.Contains(r.Output(), gatewayContact) {
		t.Error("the contact reached the log of muster dev")
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/devmode"
)

// TestNotesAndOwnerRelease is S-063 against `muster dev`, with E2E_REPLICAS=2 on two replicas, the reads on the
// second one: deleting a user who owns an acknowledged Alert Group releases it with an unacknowledged by the system
// with the reason owner_deleted, the deleted user deactivated as the previous Owner and no Audit log entry of its own,
// and a system-resolved Alert Group that user owned reopens into firing (C-03.FR-13, C-03.AC-27, C-09.FR-22); a Note
// on a resolved Alert Group is listed with its author after retention.alert_details on the development clock and is
// gone with the summary row after retention.alert_group_summaries (C-10.AC-20, C-09.FR-16).
func TestNotesAndOwnerRelease(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	reader := h.Replicas[len(h.Replicas)-1]
	admin := newAgent(t, r.App)
	if a := admin.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		t.Fatalf("sign in = %d %s", a.status, a.body)
	}
	perms, _ := json.Marshal(admin.json(http.MethodGet, "/api/v1/me", "", http.StatusOK)["permissions"])
	token := admin.json(http.MethodPost, "/api/v1/me/personal-access-tokens",
		`{"name":"notes","permissions":`+string(perms)+`}`, http.StatusCreated)["value"].(string)
	pat := patClient{t: t, base: r.App, token: token}
	read := func(path string) map[string]any {
		t.Helper()
		return (patClient{t: t, base: reader.App, token: token}).json(http.MethodGet, path, "", http.StatusOK)
	}
	created := admin.json(http.MethodPost, "/api/v1/users", `{"name":"bob","login":"bob","role":"responder"}`,
		http.StatusCreated)
	bobID := created["user"].(map[string]any)["id"].(string)
	link, _ := url.Parse(created["password_setup_link"].(map[string]any)["url"].(string))
	setup, _ := strings.CutPrefix(link.Fragment, "token=")
	if a := admin.do(http.MethodPost, "/api/v1/password-setups", `{"token":"`+setup+`","password":"bob-password-1"}`); a.status !=
		http.StatusNoContent {
		t.Fatalf("password setup = %d %s", a.status, a.body)
	}
	bob := newAgent(t, r.App)
	if a := bob.signIn("bob", "bob-password-1"); a.status != http.StatusCreated {
		t.Fatalf("bob = %d %s", a.status, a.body)
	}

	in := admin.json(http.MethodPost, "/api/v1/integrations", `{"name":"nts","connection_mode":"webhook_only",
		"static_labels":{},"duplicate_window_seconds":45,"heartbeat":{"enabled":false}}`, http.StatusCreated)
	intID := in["id"].(string)
	intToken := admin.json(http.MethodPost, "/api/v1/integrations/"+intID+"/tokens", `{"name":"nts"}`,
		http.StatusCreated)["value"].(string)
	fam := h.Fakes.Alertmanager + "/_fake"
	if a := call(t, http.MethodPost, fam+"/receivers", `{"name":"nts","url":"`+r.Ingest+`/api/v1/ingest","token":"`+
		intToken+`"}`); a.status != http.StatusNoContent {
		t.Fatalf("register = %d %s", a.status, a.body)
	}
	var profiles struct {
		Items []struct {
			Policy json.RawMessage `json:"policy"`
		} `json:"items"`
	}
	decode(t, admin.do(http.MethodGet, "/api/v1/route-profiles", ""), &profiles)
	admin.json(http.MethodPost, "/api/v1/routes", `{"name":"ops","matchers":[{"label":"team","op":"=","value":"ops"}],`+
		`"urgent":false,"group_key":["alertname","cluster"],"destination_ids":[],"policy":`+
		string(profiles.Items[0].Policy)+`}`, http.StatusCreated)
	put := func(path, body string) {
		t.Helper()
		if a := call(t, http.MethodPut, fam+path, body); a.status/100 != 2 {
			t.Fatalf("PUT %s = %d %s", path, a.status, a.body)
		}
	}
	processed := func() int64 {
		return h.count(t, `SELECT count(*) FROM stored_snapshots WHERE state <> 'pending'`)
	}
	notify := func(reason string) {
		t.Helper()
		before := processed()
		if a := call(t, http.MethodPost, fam+"/groups/o1/notify", `{"reason":"`+reason+`"}`); a.status != http.StatusOK {
			t.Fatalf("notify = %d %s", a.status, a.body)
		}
		eventually(t, "the snapshot to be processed", func() bool { return processed() > before })
	}
	alert := func(name, cluster, extra string) {
		put("/groups/o1/alerts/"+name, `{"labels":{"team":"ops","cluster":"`+cluster+`","severity":"warning",`+
			`"n":"`+name+`"}`+extra+`}`)
	}
	groupOf := func(cluster string) string {
		t.Helper()
		items := read("/api/v1/integrations/" + intID + "/alerts?" + url.Values{"label": {`cluster="` + cluster +
			`"`}}.Encode())["items"].([]any)
		if len(items) == 0 {
			t.Fatalf("no alert of cluster %s", cluster)
		}
		return items[0].(map[string]any)["alert_group"].(map[string]any)["id"].(string)
	}
	top := func(id string) map[string]any {
		t.Helper()
		items := read("/api/v1/alert-groups/" + id + "/timeline?limit=1")["items"].([]any)
		if len(items) == 0 {
			t.Fatalf("no timeline of %s", id)
		}
		return items[0].(map[string]any)
	}

	put("/groups/o1", `{"receiver":"nts","route":"{}","labels":{"alertname":"DiskSlow"}}`)
	for _, c := range []string{"a", "b", "c"} {
		alert(c, c, "")
	}
	notify("first notification")
	ga, gb, gc := groupOf("a"), groupOf("b"), groupOf("c")
	for _, id := range []string{ga, gb} {
		if a := bob.do(http.MethodPost, "/api/v1/alert-groups/"+id+"/acknowledge", ""); a.status != http.StatusOK {
			t.Fatalf("bob acknowledges = %d %s", a.status, a.body)
		}
	}
	// gb resolves by the system while Bob owns it.
	alert("b", "b", `,"status":"resolved"`)
	notify("some alerts resolved")
	if v := read("/api/v1/alert-groups/" + gb); v["status"] != "resolved" {
		t.Fatalf("gb = %v", v)
	}

	// C-03.FR-13: deleting Bob releases ga in the delete's transaction.
	audits := h.count(t, `SELECT count(*) FROM audit_log WHERE action LIKE 'alert_group.%'`)
	if a := pat.do(http.MethodDelete, "/api/v1/users/"+bobID, ""); a.status != http.StatusNoContent {
		t.Fatalf("delete bob = %d %s", a.status, a.body)
	}
	if v := read("/api/v1/alert-groups/" + ga); v["status"] != "firing" || v["owner"] != nil {
		t.Errorf("ga after the delete = %v", v)
	}
	e := top(ga)
	previous, _ := e["previous_owner"].(map[string]any)
	if e["event"] != "unacknowledged" || e["actor"].(map[string]any)["kind"] != "system" ||
		e["reason"] != "owner_deleted" || previous == nil || previous["id"] != bobID || previous["deactivated"] != true ||
		e["loudness"] != "loud" || len(e["mentions"].([]any)) != 0 {
		t.Errorf("the release = %v", e)
	}
	if n := h.count(t, `SELECT count(*) FROM audit_log WHERE action LIKE 'alert_group.%'`); n != audits {
		t.Errorf("%d audit entries of alert groups by the release", n-audits)
	}
	if items := read("/api/v1/audit-log?resource_id=" + bobID)["items"].([]any); len(items) == 0 ||
		items[0].(map[string]any)["action"] != "user.deleted" {
		t.Errorf("audit log of bob = %v", items)
	}
	t.Logf("release: event=%v actor=%v reason=%v previous_owner=%v loudness=%v mentions=%v", e["event"],
		e["actor"].(map[string]any)["kind"], e["reason"], previous, e["loudness"], e["mentions"])

	// The system-resolved gb that Bob owned reopens into firing, with the Loud reopened of a Reopen into firing.
	alert("b2", "b", "")
	notify("new alerts added")
	v := read("/api/v1/alert-groups/" + gb)
	if e := top(gb); v["status"] != "firing" || v["owner"] != nil || v["reopen_count"] != 1.0 ||
		e["event"] != "reopened" || e["loudness"] != "loud" || len(e["mentions"].([]any)) != 1 {
		t.Errorf("gb reopened = %v, %v", v["status"], e)
	}
	t.Logf("reopen: status=%v owner=%v event=%v loudness=%v mentions=%v", v["status"], v["owner"], top(gb)["event"],
		top(gb)["loudness"], top(gb)["mentions"])

	// C-10.AC-20: a Note on the resolved gc outlives the details and goes with the summary row.
	alert("c", "c", `,"status":"resolved"`)
	notify("some alerts resolved")
	n := pat.json(http.MethodPost, "/api/v1/alert-groups/"+gc+"/notes", `{"body":"Post-mortem filed."}`,
		http.StatusCreated)
	if n["author"].(map[string]any)["kind"] != "user" || n["transport"] != "api" {
		t.Errorf("note = %v", n)
	}
	advance(t, r, 91*24*3600)
	eventually(t, "the leader to purge the details", func() bool {
		return h.count(t, `SELECT count(*) FROM alert_group_alerts m JOIN alert_groups g ON g.id = m.alert_group_id
			WHERE g.public_id = '`+gc+`'`) == 0
	})
	notes := read("/api/v1/alert-groups/" + gc + "/notes")["items"].([]any)
	if len(notes) != 1 || notes[0].(map[string]any)["body"] != "Post-mortem filed." ||
		notes[0].(map[string]any)["author"].(map[string]any)["kind"] != "user" {
		t.Errorf("notes after the details went = %v", notes)
	}
	tl := read("/api/v1/alert-groups/" + gc + "/timeline")["items"].([]any)
	if len(tl) != 1 || tl[0].(map[string]any)["event"] != "note_added" {
		t.Errorf("timeline after the details went = %v", tl)
	}
	t.Logf("after retention.alert_details: notes=%d timeline=%d", len(notes), len(tl))
	advance(t, r, 640*24*3600)
	eventually(t, "the leader to purge the summary row", func() bool {
		return h.count(t, `SELECT count(*) FROM alert_groups WHERE public_id = '`+gc+`'`) == 0
	})
	if a := (patClient{t: t, base: reader.App, token: token}).do(http.MethodGet, "/api/v1/alert-groups/"+gc+"/notes",
		""); a.status != http.StatusNotFound {
		t.Errorf("notes after the summary row went = %d %s", a.status, a.body)
	}
	if n := h.count(t, `SELECT count(*) FROM notes`); n != 0 {
		t.Errorf("%d notes left", n)
	}
	t.Log("after retention.alert_group_summaries: the alert group and its notes are gone")
}

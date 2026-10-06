// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/devmode"
)

// eventually waits until cond holds, polling, and fails after a while.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(pollInterval)
	}
}

// devClock is the answer of the development clock of a replica.
type devClock struct {
	Now           time.Time `json:"now"`
	OffsetSeconds int64     `json:"offset_seconds"`
}

func readClock(t *testing.T, r *Replica) devClock {
	t.Helper()
	a := call(t, http.MethodGet, r.Internal+devmode.ClockPath, "")
	var c devClock
	decode(t, a, &c)
	return c
}

func advance(t *testing.T, r *Replica, seconds int) devClock {
	t.Helper()
	a := call(t, http.MethodPost, r.Internal+devmode.ClockPath, fmt.Sprintf(`{"advance_seconds":%d}`, seconds))
	if a.status != http.StatusOK {
		t.Fatalf("advance = %d %s", a.status, a.body)
	}
	var c devClock
	decode(t, a, &c)
	return c
}

// TestProcessing is S-020 against `muster dev`, with E2E_REPLICAS=2 on two replicas: webhooks from the fake
// Alertmanager's group model become Alerts with Static labels and warnings (C-06.AC-8), an HA copy changes nothing,
// a near-duplicate proves nothing and Gone needs a second window and the minimum absence on the development clock
// (C-06.AC-2, AC-1), a resolved Alertmanager group resolves all its Alerts (C-06.AC-13), a body that is not JSON fails
// alone (C-06.AC-12), one webhook writes one line (C-06.AC-9), and the Integration counts its Snapshots.
func TestProcessing(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	admin := newAgent(t, r.App)
	if a := admin.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		t.Fatalf("sign in = %d %s", a.status, a.body)
	}
	in := admin.json(http.MethodPost, "/api/v1/integrations", `{"name":"lab","connection_mode":"webhook_only",
		"static_labels":{"cluster":"b"},"duplicate_window_seconds":45,"heartbeat":{"enabled":false}}`,
		http.StatusCreated)
	id := in["id"].(string)
	token := admin.json(http.MethodPost, "/api/v1/integrations/"+id+"/tokens", `{"name":"lab"}`,
		http.StatusCreated)["value"].(string)
	fam := h.Fakes.Alertmanager + "/_fake"
	if a := call(t, http.MethodPost, fam+"/receivers", `{"name":"lab","url":"`+r.Ingest+`/api/v1/ingest","token":"`+
		token+`"}`); a.status != http.StatusNoContent {
		t.Fatalf("register = %d %s", a.status, a.body)
	}
	put := func(path, body string) {
		t.Helper()
		if a := call(t, http.MethodPut, fam+path, body); a.status/100 != 2 {
			t.Fatalf("PUT %s = %d %s", path, a.status, a.body)
		}
	}
	processed := func() int {
		return int(h.count(t, `SELECT count(*) FROM stored_snapshots WHERE state <> 'pending'`))
	}
	notify := func(group, body string) {
		t.Helper()
		before := processed()
		a := call(t, http.MethodPost, fam+"/groups/"+group+"/notify", body)
		var res struct {
			Sent []struct {
				Status int `json:"status"`
			} `json:"sent"`
		}
		decode(t, a, &res)
		if a.status != http.StatusOK || len(res.Sent) == 0 || res.Sent[0].Status != http.StatusAccepted {
			t.Fatalf("notify %s = %d %s", group, a.status, a.body)
		}
		eventually(t, "the snapshots to be processed", func() bool { return processed() >= before+len(res.Sent) })
	}
	type alert struct {
		Labels        map[string]string `json:"labels"`
		State         string            `json:"state"`
		ResolveReason *string           `json:"resolve_reason"`
		Warnings      []string          `json:"static_label_warnings"`
		Groups        []string          `json:"alertmanager_groups"`
	}
	view := func(query string) []alert {
		t.Helper()
		var page struct {
			Items []alert `json:"items"`
		}
		a := admin.do(http.MethodGet, "/api/v1/integrations/"+id+"/alerts?"+query, "")
		if a.status != http.StatusOK {
			t.Fatalf("view %s = %d %s", query, a.status, a.body)
		}
		decode(t, a, &page)
		return page.Items
	}
	label := func(m string) string { return "label=" + url.QueryEscape(m) }

	put("/groups/g1", `{"receiver":"lab","route":"{}/{team=\"db\"}","labels":{"alertname":"DiskFull"}}`)
	put("/groups/g1/alerts/a", `{"labels":{"instance":"db-a","cluster":"a"}}`)
	put("/groups/g1/alerts/b", `{"labels":{"instance":"db-b"}}`)
	put("/groups/g1/alerts/c", `{"labels":{"instance":"db-c"}}`)
	notify("g1", `{"reason":"first notification","copies":2}`)
	firing := view("state=firing")
	if len(firing) != 3 {
		t.Fatalf("firing %+v", firing)
	}
	for _, a := range firing {
		wantCluster, wantWarnings := "b", "[]"
		if a.Labels["instance"] == "db-a" {
			wantCluster, wantWarnings = "a", `["cluster"]`
		}
		w, _ := json.Marshal(a.Warnings)
		if a.Labels["cluster"] != wantCluster || string(w) != wantWarnings ||
			strings.Join(a.Groups, ",") != `{}/{team="db"}:{alertname="DiskFull"}` {
			t.Errorf("alert %+v", a)
		}
	}
	if n := h.count(t, `SELECT max(episode) FROM alerts`); n != 1 {
		t.Errorf("the HA copy made episode %d", n)
	}

	// C-06.AC-2 and C-06.AC-1 on the development clock.
	advance(t, r, 20)
	notify("g1", `{"reason":"repeat interval elapsed","list":["a","b"]}`)
	advance(t, r, 60)
	notify("g1", `{"reason":"repeat interval elapsed","list":["a","b"]}`)
	if got := view("state=firing&" + label(`instance="db-c"`)); len(got) != 1 {
		t.Errorf("db-c after the first miss: %+v", got)
	}
	advance(t, r, 300)
	notify("g1", `{"reason":"repeat interval elapsed","list":["a","b"]}`)
	got := view("state=resolved&" + label(`instance="db-c"`))
	if len(got) != 1 || got[0].ResolveReason == nil || *got[0].ResolveReason != "gone" {
		t.Errorf("db-c after the second miss: %+v", got)
	}

	// C-06.AC-13: a resolved Alertmanager group resolves all its Alerts, listed or not.
	resolvedMetric := `muster_alerts_resolved_total{integration="` + id + `",reason="resolved"}`
	put("/groups/g3", `{"receiver":"lab","route":"{}","labels":{"alertname":"Batch"}}`)
	put("/groups/g4", `{"receiver":"lab","route":"{}","labels":{"alertname":"Other"}}`)
	for i := range 20 {
		put(fmt.Sprintf("/groups/g3/alerts/y%d", i), fmt.Sprintf(`{"labels":{"pod":"q%d"}}`, i))
	}
	put("/groups/g4/alerts/z", `{"labels":{"pod":"z"}}`)
	notify("g3", `{"reason":"first notification"}`)
	notify("g4", `{"reason":"first notification"}`)
	before := metricValue(t, r, resolvedMetric)
	for i := range 20 {
		put(fmt.Sprintf("/groups/g3/alerts/y%d", i), fmt.Sprintf(`{"labels":{"pod":"q%d"},"status":"resolved"}`, i))
	}
	notify("g3", `{"reason":"all alerts resolved","list":["y0","y1","y2","y3","y4","y5"]}`)
	if n := len(view("state=firing&" + label(`alertname="Batch"`))); n != 0 {
		t.Errorf("%d Batch alerts still fire", n)
	}
	if n := len(view("state=firing&" + label(`alertname="Other"`))); n != 1 {
		t.Errorf("%d Other alerts fire", n)
	}
	eventually(t, "20 resolutions on the metrics of a replica", func() bool {
		total := 0
		for _, rr := range h.Replicas {
			total += metricValue(t, rr, resolvedMetric)
		}
		return total-before >= 20
	})

	// C-06.AC-12: a body that is not JSON fails alone; C-06.AC-9: one webhook writes one line.
	failedBefore := processed()
	if a := call(t, http.MethodPost, fam+"/send", `{"receiver":"lab","raw":"not json","content_type":"text/plain"}`); a.status != http.StatusOK {
		t.Fatalf("send = %d %s", a.status, a.body)
	}
	eventually(t, "the body to fail", func() bool { return processed() > failedBefore })
	failed := admin.json(http.MethodGet, "/api/v1/stored-snapshots?integration="+id+"&state=failed", "", http.StatusOK)
	items := failed["items"].([]any)
	if len(items) != 1 || !strings.HasPrefix(items[0].(map[string]any)["processing_error"].(string),
		"the body is not valid JSON: ") {
		t.Errorf("failed snapshots %v", failed)
	}
	put("/groups/g5", `{"receiver":"lab","route":"{}","labels":{"alertname":"Wide"}}`)
	for i := 1; i <= 200; i++ {
		put(fmt.Sprintf("/groups/g5/alerts/w%d", i), fmt.Sprintf(`{"labels":{"n":"%d"}}`, i))
	}
	notify("g5", `{"reason":"first notification"}`)
	lines := 0
	for _, rr := range h.Replicas {
		for line := range strings.Lines(rr.Output()) {
			if strings.Contains(line, `"event":"snapshot_processed"`) && strings.Contains(line, `"alerts":200`) {
				lines++
			}
		}
	}
	if lines != 1 {
		t.Errorf("%d snapshot_processed lines for the webhook with 200 alerts", lines)
	}
	if n := len(view("state=firing&" + label(`alertname="Wide"`) + "&limit=500")); n != 200 {
		t.Errorf("%d Wide alerts", n)
	}
	count := admin.json(http.MethodGet, "/api/v1/integrations/"+id, "", http.StatusOK)["snapshot_count"].(float64)
	if int(count) != int(h.count(t, `SELECT count(*) FROM stored_snapshots s JOIN integrations i ON
		i.id = s.integration_id WHERE i.public_id = '`+id+`'`)) {
		t.Errorf("snapshot_count %v", count)
	}
	if n := h.count(t, `SELECT count(*) FROM stored_snapshots WHERE state = 'pending'`); n != 0 {
		t.Errorf("%d snapshots pending", n)
	}
}

func metricValue(t *testing.T, r *Replica, series string) int {
	t.Helper()
	v, _ := strconv.Atoi(r.Metric(t, series))
	return v
}

// TestDevClock is the development clock of C-01.FR-13 on two replicas of one database: an advance through one moves
// the clock of the other within a second, and a replica started afterwards starts at the advanced time.
func TestDevClock(t *testing.T) {
	h := Start(t, FakesInProcess)
	a := h.StartReplica(t, ReplicaOptions{})
	if c := readClock(t, a); c.OffsetSeconds != 0 {
		t.Fatalf("a fresh database runs ahead by %d s", c.OffsetSeconds)
	}
	if c := advance(t, a, 3600); c.OffsetSeconds != 3600 || time.Until(c.Now) < 59*time.Minute {
		t.Fatalf("advance = %+v", c)
	}
	b := h.StartReplica(t, ReplicaOptions{ListenApp: "127.0.0.1:9180", ListenIngest: "127.0.0.1:9181",
		ListenInternal: "127.0.0.1:9182"})
	if c := readClock(t, b); c.OffsetSeconds != 3600 {
		t.Errorf("a replica started afterwards runs ahead by %d s", c.OffsetSeconds)
	}
	start := time.Now()
	advance(t, b, 600)
	eventually(t, "the other replica to follow", func() bool { return readClock(t, a).OffsetSeconds == 4200 })
	if took := time.Since(start); took > time.Second {
		t.Errorf("the other replica followed after %v", took)
	}
	if code := call(t, http.MethodPost, a.Internal+devmode.ClockPath, `{"advance_seconds":-5}`).status; code !=
		http.StatusBadRequest {
		t.Errorf("going back = %d", code)
	}
}

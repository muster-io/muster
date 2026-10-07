// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/devmode"
)

// stream is an open live-updates stream of an agent's session, read into lines in the background.
type stream struct {
	status int
	cancel context.CancelFunc
	mu     sync.Mutex
	lines  []string
	closed bool
}

// openStream opens GET /api/v1/live-updates with the agent's session cookies.
func openStream(t *testing.T, a *agent) *stream {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.base+"/api/v1/live-updates", nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range a.cookies {
		req.AddCookie(&http.Cookie{Name: name, Value: value}) //nolint:gosec // G124: a request cookie carries no attributes
	}
	// A client without a timeout, as the stream outlives any.
	resp, err := (&http.Client{}).Do(req) //nolint:bodyclose // the reader goroutine closes it
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	s := &stream{status: resp.StatusCode, cancel: cancel}
	go func() {
		defer resp.Body.Close()
		r := bufio.NewScanner(resp.Body)
		for r.Scan() {
			s.mu.Lock()
			s.lines = append(s.lines, r.Text())
			s.mu.Unlock()
		}
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
	}()
	t.Cleanup(cancel)
	return s
}

// hints are the data lines of the stream so far.
func (s *stream) hints() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, l := range s.lines {
		if data, ok := strings.CutPrefix(l, "data: "); ok {
			out = append(out, data)
		}
	}
	return out
}

func (s *stream) first() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.lines) == 0 {
		return ""
	}
	return s.lines[0]
}

func (s *stream) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// TestAlertGroupList is S-029 against `muster dev`, with E2E_REPLICAS=2 on two replicas: the live-update hints of a
// new Alert Group and of each change (C-09.AC-13), the list's filters on the common labels (C-09.AC-15, AC-25),
// search inside words and the counts (C-09.AC-20), the unsupported filters, the open count of an Integration
// (C-09.FR-21), statistics per Route and per Integration (C-09.AC-17), related Alert Groups (C-09.AC-16), an old open
// Alert Group in the default view (C-09.AC-5), #N 60 days after its resolution and one statistics item per Route
// (C-09.AC-23), the removal of details after retention.alert_details on the development clock (C-09.AC-18), and the
// end of the stream with its session (C-09.AC-24). With two replicas the reads go to the second one.
func TestAlertGroupList(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	reader := h.Replicas[len(h.Replicas)-1]
	admin := newAgent(t, r.App)
	if a := admin.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		t.Fatalf("sign in = %d %s", a.status, a.body)
	}
	perms, _ := json.Marshal(admin.json(http.MethodGet, "/api/v1/me", "", http.StatusOK)["permissions"])
	token := admin.json(http.MethodPost, "/api/v1/me/personal-access-tokens",
		`{"name":"list","permissions":`+string(perms)+`}`, http.StatusCreated)["value"].(string)
	pat := patClient{t: t, base: reader.App, token: token}
	read := func(path string) map[string]any {
		t.Helper()
		return pat.json(http.MethodGet, path, "", http.StatusOK)
	}
	list := func(q url.Values) []map[string]any {
		t.Helper()
		var out []map[string]any
		for _, it := range read("/api/v1/alert-groups?" + q.Encode())["items"].([]any) {
			out = append(out, it.(map[string]any))
		}
		return out
	}
	routeNames := func(items []map[string]any) []string {
		out := []string{}
		for _, it := range items {
			out = append(out, it["route"].(map[string]any)["name"].(string))
		}
		return out
	}
	in := admin.json(http.MethodPost, "/api/v1/integrations", `{"name":"lst","connection_mode":"webhook_only",
		"static_labels":{},"duplicate_window_seconds":45,"heartbeat":{"enabled":false}}`, http.StatusCreated)
	intID := in["id"].(string)
	intToken := admin.json(http.MethodPost, "/api/v1/integrations/"+intID+"/tokens", `{"name":"lst"}`,
		http.StatusCreated)["value"].(string)
	fam := h.Fakes.Alertmanager + "/_fake"
	if a := call(t, http.MethodPost, fam+"/receivers", `{"name":"lst","url":"`+r.Ingest+`/api/v1/ingest","token":"`+
		intToken+`"}`); a.status != http.StatusNoContent {
		t.Fatalf("register = %d %s", a.status, a.body)
	}
	var profiles struct {
		Items []struct {
			Policy json.RawMessage `json:"policy"`
		} `json:"items"`
	}
	decode(t, admin.do(http.MethodGet, "/api/v1/route-profiles", ""), &profiles)
	policy := string(profiles.Items[0].Policy)
	route := func(name, key string) string {
		return admin.json(http.MethodPost, "/api/v1/routes", `{"name":"`+name+`","matchers":[{"label":"team","op":"=",`+
			`"value":"`+name+`"}],"urgent":false,"group_key":`+key+`,"destination_ids":[],"policy":`+policy+`}`,
			http.StatusCreated)["id"].(string)
	}
	rk, rm, rs := route("k8s", `["alertname","namespace"]`), route("mix", `["alertname"]`), route("st", `["alertname","n"]`)
	put := func(path, body string) {
		t.Helper()
		if a := call(t, http.MethodPut, fam+path, body); a.status/100 != 2 {
			t.Fatalf("PUT %s = %d %s", path, a.status, a.body)
		}
	}
	processed := func() int64 {
		return h.count(t, `SELECT count(*) FROM stored_snapshots WHERE state <> 'pending'`)
	}
	notify := func(group, reason string) {
		t.Helper()
		before := processed()
		if a := call(t, http.MethodPost, fam+"/groups/"+group+"/notify", `{"reason":"`+reason+`"}`); a.status !=
			http.StatusOK {
			t.Fatalf("notify %s = %d %s", group, a.status, a.body)
		}
		eventually(t, "the snapshot to be processed", func() bool { return processed() > before })
	}
	// pod is an Alert of the team in the namespace, open for its annotations or status.
	pod := func(team, namespace, name, extra string) string {
		return `{"labels":{"team":"` + team + `","namespace":"` + namespace + `","pod":"` + name + `"` + extra + `}`
	}
	const critical = `,"severity":"critical"`

	// C-09.AC-13: the stream follows the web session; it is opened before the clock jumps.
	sse := openStream(t, admin)
	if sse.status != http.StatusOK {
		t.Fatalf("live-updates = %d", sse.status)
	}
	put("/groups/c1", `{"receiver":"lst","route":"{}","labels":{"alertname":"KubePodCrashLooping"}}`)
	put("/groups/c1/alerts/p0", pod("k8s", "payments", "postgres-0", critical)+
		`,"annotations":{"summary":"Pod postgres-0 is crash looping"}}`)
	put("/groups/c1/alerts/p1", pod("k8s", "payments", "postgres-1", critical)+"}")
	put("/groups/c1/alerts/m0", pod("mix", "payments", "a", "")+"}")
	put("/groups/c1/alerts/m1", pod("mix", "billing", "b", "")+"}")
	notify("c1", "first notification")
	eventually(t, "the alert-groups and alert-group hints", func() bool {
		var groups, group int
		for _, d := range sse.hints() {
			switch {
			case strings.Contains(d, `"type":"alert-groups"`):
				groups++
			case strings.Contains(d, `"type":"alert-group"`):
				group++
			}
		}
		return groups >= 1 && group >= 2
	})
	sse.cancel()

	// C-09.AC-15 and C-09.AC-25: the common labels decide.
	from := readClock(t, r).Now.Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	payments := `namespace="payments"`
	if got := routeNames(list(url.Values{"label": {payments}, "urgent": {"true"}, "from": {from}})); !slices.Equal(got,
		[]string{"k8s"}) {
		t.Errorf("payments and urgent = %v", got)
	}
	if got := routeNames(list(url.Values{"label": {payments}})); !slices.Equal(got, []string{"k8s"}) {
		t.Errorf("payments = %v", got)
	}
	if items := list(url.Values{"label_columns": {"namespace"}, "route": {rm}}); len(items) != 1 ||
		len(items[0]["label_values"].(map[string]any)) != 0 {
		t.Errorf("label values of mix = %v", items)
	}

	// C-09.AC-20: search inside words, either case.
	found := list(url.Values{"q": {"postgres"}})
	if len(found) != 1 || len(list(url.Values{"q": {"POSTGRES"}})) != 1 {
		t.Fatalf("search = %v", found)
	}
	pay, npay := found[0]["id"].(string), found[0]["number"].(float64)
	if c := read("/api/v1/alert-group-counts?" + url.Values{"label": {payments}}.Encode()); c["firing"] != 1.0 ||
		c["all"] != 1.0 || c["resolved"] != 0.0 {
		t.Errorf("counts = %v", c)
	}
	if a := pat.do(http.MethodGet, "/api/v1/alert-groups?owner=me", ""); a.status != http.StatusUnprocessableEntity ||
		!strings.Contains(string(a.body), `"code":"unsupported"`) {
		t.Errorf("owner = %d %s", a.status, a.body)
	}
	if n := read("/api/v1/integrations/" + intID)["open_alert_group_count"]; n != 2.0 {
		t.Errorf("open_alert_group_count = %v", n)
	}

	// C-09.AC-17: three Alert Groups resolved 10, 20 and 30 minutes after they started.
	put("/groups/s1", `{"receiver":"lst","route":"{}","labels":{"alertname":"Stat"}}`)
	stat := func(n, status string) string {
		return `{"labels":{"team":"st","n":"` + n + `"}` + status + `}`
	}
	for _, n := range []string{"1", "2", "3"} {
		put("/groups/s1/alerts/x"+n, stat(n, ""))
	}
	// The business clock follows the real one between the moves, so each duration is the moves plus the real time
	// that processing took, at most elapsed.
	began := time.Now()
	notify("s1", "first notification")
	for _, n := range []string{"1", "2", "3"} {
		advance(t, r, 600)
		put("/groups/s1/alerts/x"+n, stat(n, `,"status":"resolved"`))
		notify("s1", "some alerts resolved")
	}
	elapsed := math.Ceil(time.Since(began).Seconds())
	st := read("/api/v1/alert-group-statistics?group_by=route&route=" + rs + "&time_zone=Europe/Berlin")
	item := st["items"].([]any)[0].(map[string]any)
	var perDay float64
	for _, d := range item["per_day"].([]any) {
		perDay += d.(map[string]any)["alert_group_count"].(float64)
	}
	median, _ := item["time_to_resolve"].(map[string]any)["median_seconds"].(float64)
	if item["alert_group_count"] != 3.0 || median < 1200 || median > 1200+elapsed || perDay != 3 {
		t.Errorf("statistics = %v", item)
	}
	if n := read("/api/v1/alert-group-statistics?group_by=integration&integration=" + intID)["items"].([]any)[0].(map[string]any)["alert_group_count"]; n != 5.0 {
		t.Errorf("per integration = %v", n)
	}

	// C-09.AC-16: the earlier Alert Group with the same key.
	put("/groups/c1/alerts/p0", pod("k8s", "payments", "postgres-0", critical)+`,"status":"resolved"}`)
	put("/groups/c1/alerts/p1", pod("k8s", "payments", "postgres-1", critical)+`,"status":"resolved"}`)
	notify("c1", "some alerts resolved")
	advance(t, r, 1200)
	put("/groups/c1/alerts/p2", pod("k8s", "payments", "postgres-2", critical)+"}")
	notify("c1", "new alerts added")
	newID := list(url.Values{"route": {rk}})[0]["id"].(string)
	related := read("/api/v1/alert-groups/" + newID + "/related")["items"].([]any)
	if len(related) != 1 || related[0].(map[string]any)["number"] != npay ||
		related[0].(map[string]any)["status"] != "resolved" ||
		related[0].(map[string]any)["resolution"].(map[string]any)["by"] != "system" {
		t.Errorf("related = %v", related)
	}

	// C-09.AC-5: an open Alert Group that started 10 days ago is in the default view.
	advance(t, r, 10*24*3600)
	ids := func(items []map[string]any) []string {
		var out []string
		for _, it := range items {
			out = append(out, it["id"].(string))
		}
		return out
	}
	if got := ids(list(url.Values{})); !slices.Contains(got, newID) {
		t.Errorf("the default view %v lacks %s", got, newID)
	}

	// C-09.AC-23: 60 days after its resolution, the number still finds it; one statistics item per Route.
	advance(t, r, 50*24*3600)
	if got := ids(list(url.Values{"number": {strconv.Itoa(int(npay))}})); !slices.Equal(got, []string{pay}) {
		t.Errorf("number = %v", got)
	}
	routes := read("/api/v1/routes")["items"].([]any)
	if items := read("/api/v1/alert-group-statistics?group_by=route")["items"].([]any); len(items) != len(routes) {
		t.Errorf("%d statistics items for %d routes", len(items), len(routes))
	}

	// C-09.AC-18 and C-09.AC-25: the details go after 90 days, the summary and the filters stay.
	advance(t, r, 31*24*3600)
	eventually(t, "the leader to purge the details", func() bool {
		return h.count(t, `SELECT count(*) FROM alert_group_alerts m JOIN alert_groups g ON g.id = m.alert_group_id
			WHERE g.public_id = '`+pay+`'`) == 0
	})
	v := read("/api/v1/alert-groups/" + pay)
	notices, _ := v["notices"].([]any)
	if v["details_removed"] != true || len(notices) != 1 || notices[0].(map[string]any)["kind"] != "details_removed" ||
		notices[0].(map[string]any)["retention_days"] != 90.0 {
		t.Errorf("after the details went = %v", v)
	}
	if items := read("/api/v1/alert-groups/" + pay + "/alerts")["items"].([]any); len(items) != 0 {
		t.Errorf("alerts = %v", items)
	}
	now := readClock(t, r).Now
	q := url.Values{"q": {"KubePodCrashLooping"}, "status": {"resolved"},
		"from": {now.Add(-200 * 24 * time.Hour).Format(time.RFC3339)}, "to": {"2100-01-01T00:00:00Z"}}
	if got := ids(list(q)); !slices.Contains(got, pay) {
		t.Errorf("search after the details went = %v", got)
	}
	q.Set("q", "")
	q.Set("label", payments)
	if got := ids(list(q)); !slices.Contains(got, pay) {
		t.Errorf("the label filter after the details went = %v", got)
	}
	logged := false
	for _, x := range h.Replicas {
		if _, _, ok := x.LogLine("alert_groups_purged"); ok {
			logged = true
		}
	}
	if !logged {
		t.Error("no alert_groups_purged line")
	}

	// C-09.AC-24: a new session of the Admin; ending it closes the stream and the reconnect gets 401.
	again := newAgent(t, r.App)
	if a := again.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		t.Fatalf("sign in again = %d %s", a.status, a.body)
	}
	s2 := openStream(t, again)
	eventually(t, "the first line", func() bool { return s2.first() != "" })
	if s2.status != http.StatusOK || s2.first() != "retry: 3000" {
		t.Errorf("stream = %d %q", s2.status, s2.first())
	}
	if a := again.do(http.MethodDelete, "/api/v1/sessions/current", ""); a.status != http.StatusNoContent {
		t.Fatalf("sign out = %d %s", a.status, a.body)
	}
	eventually(t, "the stream to close", s2.isClosed)
	if s3 := openStream(t, again); s3.status != http.StatusUnauthorized {
		t.Errorf("reconnect = %d", s3.status)
	}
}

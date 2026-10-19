// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package ingest_test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/integrations"
)

// grafanaBody is one line of the fixtures in testdata/grafana: a body as Grafana 13.2.4 sent it on the development
// stand, with its time of arrival and the receiver's path.
type grafanaBody struct {
	Recv time.Time       `json:"recv"`
	Path string          `json:"path"`
	Body json.RawMessage `json:"body"`
}

// grafanaListing is what the test reads of a body: its groupKey and, per alert, the fingerprint, the status and the
// labels that name it.
type grafanaListing struct {
	GroupKey string `json:"groupKey"`
	Alerts   []struct {
		Fingerprint string            `json:"fingerprint"`
		Status      string            `json:"status"`
		Labels      map[string]string `json:"labels"`
	} `json:"alerts"`
}

func readGrafana(t *testing.T, name string) []grafanaBody {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "grafana", name))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []grafanaBody
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var b grafanaBody
		if err := json.Unmarshal(sc.Bytes(), &b); err != nil {
			t.Fatalf("%s line %d: %v", name, len(out)+1, err)
		}
		out = append(out, b)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func listingOf(t *testing.T, body []byte) grafanaListing {
	t.Helper()
	var l grafanaListing
	if err := json.Unmarshal(body, &l); err != nil {
		t.Fatal(err)
	}
	return l
}

// grafanaWorld replays captured bodies into Integrations of one database on the manual business clock.
type grafanaWorld struct {
	e    *env
	p    *ingest.Processor
	sink *changeSink
}

func newGrafanaWorld(t *testing.T, e *env) *grafanaWorld {
	t.Helper()
	// The bodies arrived on 2026-10-19, after the partitions of setup.
	day, next := "20261019", "20261020"
	for _, stmt := range []string{
		`CREATE TABLE stored_snapshots_p` + day + ` PARTITION OF stored_snapshots FOR VALUES FROM ('2026-10-19T00:00:00Z')
			TO ('2026-10-20T00:00:00Z')`,
		`CREATE TABLE snapshot_bodies_p` + day + ` PARTITION OF snapshot_bodies FOR VALUES FROM ('2026-10-19')
			TO ('2026-10-20')`,
		`CREATE TABLE stored_snapshots_p` + next + ` PARTITION OF stored_snapshots FOR VALUES FROM ('2026-10-20T00:00:00Z')
			TO ('2026-10-21T00:00:00Z')`,
		`CREATE TABLE snapshot_bodies_p` + next + ` PARTITION OF snapshot_bodies FOR VALUES FROM ('2026-10-20')
			TO ('2026-10-21')`,
	} {
		if _, err := e.d.Pool.Exec(t.Context(), stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	sink := &changeSink{}
	return &grafanaWorld{e: e, p: e.processor("replica-a", sink), sink: sink}
}

// integration creates an Integration with the default duplicate window of 45 s.
func (w *grafanaWorld) integration(t *testing.T, name string) integrations.Integration {
	t.Helper()
	in, err := w.e.ints.Create(t.Context(), by, integrations.Input{Name: name,
		ConnectionMode: integrations.ConnectionWebhookOnly, DuplicateWindowSeconds: 45})
	if err != nil {
		t.Fatal(err)
	}
	return in
}

// send stores body as a Stored Snapshot of in received at recv and processes it.
func (w *grafanaWorld) send(t *testing.T, in integrations.Integration, recv time.Time, body []byte) {
	t.Helper()
	w.e.clock.Set(recv)
	if _, err := w.e.snapshots.Store(t.Context(), ingest.Received{IntegrationID: in.ID, Body: body,
		ContentType: "application/json"}); err != nil {
		t.Fatal(err)
	}
	drain(t, w.p)
}

type grafanaAlert struct {
	Status, Reason string
	Episode        int64
	StartsAt       time.Time
}

func (w *grafanaWorld) alert(t *testing.T, in integrations.Integration, fingerprint string) grafanaAlert {
	t.Helper()
	var a grafanaAlert
	if err := w.e.d.Pool.QueryRow(t.Context(), `SELECT status, coalesce(resolve_reason, ''), episode, starts_at
		FROM alerts WHERE integration_id = $1 AND fingerprint = $2`, in.ID, fingerprint).Scan(&a.Status, &a.Reason,
		&a.Episode, &a.StartsAt); err != nil {
		t.Fatalf("alert %s: %v", fingerprint, err)
	}
	return a
}

// notProcessed counts the Stored Snapshots of in that failed or are still pending.
func (w *grafanaWorld) notProcessed(t *testing.T, in integrations.Integration) int64 {
	return w.e.count(t, `SELECT count(*) FROM stored_snapshots WHERE integration_id = $1 AND state <> 'processed'`,
		in.ID)
}

func (w *grafanaWorld) truncated(t *testing.T, in integrations.Integration, groupKey string) bool {
	t.Helper()
	var truncated bool
	if err := w.e.d.Pool.QueryRow(t.Context(), `SELECT truncated FROM alertmanager_groups WHERE integration_id = $1
		AND group_key = $2`, in.ID, groupKey).Scan(&truncated); err != nil {
		t.Fatal(err)
	}
	return truncated
}

// expected follows the bodies: each fingerprint is in the state of the latest body that lists it.
type expected struct {
	status map[string]string
	name   map[string]string
	// diverged names each fingerprint once, at the first body after which it is not in the expected state.
	diverged map[string]bool
}

func (x *expected) add(l grafanaListing) {
	for _, a := range l.Alerts {
		x.status[a.Fingerprint] = a.Status
		name := a.Labels["host"]
		if name == "" {
			name = a.Labels["rulename"]
		}
		x.name[a.Fingerprint] = a.Labels["alertname"] + "/" + name
	}
}

func (w *grafanaWorld) check(t *testing.T, in integrations.Integration, x *expected, after string) {
	t.Helper()
	for fp, want := range x.status {
		if x.diverged[fp] {
			continue
		}
		a := w.alert(t, in, fp)
		if a.Status != want || (want == ingest.StatusResolved && a.Reason != ingest.ResolveResolved) {
			x.diverged[fp] = true
			t.Errorf("after the body of %s: %s is %s/%s, want %s", after, x.name[fp], a.Status, a.Reason, want)
		}
	}
}

// replay sends the captured bodies of name in their order, each at its time of arrival, to a new Integration, and
// checks after each one that every Alert is in the state of the latest body that lists it; at is called after each
// body.
func (w *grafanaWorld) replay(t *testing.T, name string, at func(in integrations.Integration, b grafanaBody)) (
	integrations.Integration, *expected) {
	t.Helper()
	bodies := readGrafana(t, name)
	in := w.integration(t, strings.TrimSuffix(name, ".jsonl"))
	x := &expected{status: map[string]string{}, name: map[string]string{}, diverged: map[string]bool{}}
	for _, b := range bodies {
		w.send(t, in, b.Recv, b.Body)
		x.add(listingOf(t, b.Body))
		w.check(t, in, x, b.Recv.Format("15:04:05.000"))
		if at != nil {
			at(in, b)
		}
	}
	if n := w.notProcessed(t, in); n != 0 {
		t.Errorf("%d stored snapshots of %s were not processed", n, name)
	}
	return in, x
}

// TestIntegrationGrafana is C-06.AC-16, C-06.AC-17 and C-06.AC-18 on PostgreSQL with bodies Grafana 13.2.4 sent: the
// 84 bodies of an HA pair leave every Alert in the state of the latest body that lists it — the paused rule's alert and
// the missing series resolved by the resolve of the replica whose copy Muster did not keep, the series' return a new
// firing — and the route learns its repeat interval without notification_reason; the 6 truncated bodies of a contact
// point with Max alerts 5 fail nothing and raise MusterSnapshotTruncated; a truncated status: resolved from Grafana
// resolves only the Alerts it lists, and the same body without orgId resolves its whole Alertmanager group.
func TestIntegrationGrafana(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		w := newGrafanaWorld(t, setup(t, s))

		t.Run("HA pair", func(t *testing.T) {
			const noData = "fd9e6a4b5371d337" // SpikeNoData on host n
			resolvedAt := time.Date(2026, 10, 19, 7, 39, 27, 947e6, time.UTC)
			firedAgainAt := time.Date(2026, 10, 19, 7, 57, 17, 513e6, time.UTC)
			in, x := w.replay(t, "ha-main.jsonl", func(in integrations.Integration, b grafanaBody) {
				lists := false
				for _, a := range listingOf(t, b.Body).Alerts {
					lists = lists || a.Fingerprint == noData
				}
				switch {
				case !lists:
				case b.Recv.Equal(resolvedAt):
					if a := w.alert(t, in, noData); a.Status != ingest.StatusResolved || a.Reason != ingest.ResolveResolved {
						t.Errorf("after the resolve of 07:39:27: SpikeNoData/n is %s/%s, want resolved/resolved",
							a.Status, a.Reason)
					}
				case b.Recv.Equal(firedAgainAt):
					a := w.alert(t, in, noData)
					if a.Status != ingest.StatusFiring || a.Episode != 2 ||
						!a.StartsAt.Equal(time.Date(2026, 10, 19, 7, 57, 0, 0, time.UTC)) {
						t.Errorf("after the firing of 07:57: SpikeNoData/n is %s, episode %d, starts at %v; want firing, "+
							"episode 2, 07:57:00", a.Status, a.Episode, a.StartsAt)
					}
				}
			})
			for fp, want := range map[string]string{"8b3b2b4c51b37a81": ingest.StatusResolved, noData: ingest.StatusFiring} {
				if x.status[fp] != want {
					t.Errorf("the bodies leave %s %s, want %s", x.name[fp], x.status[fp], want)
				}
			}
			for _, c := range w.sink.take() {
				if c.Fingerprint == noData && c.Kind == ingest.ChangeContinued {
					t.Errorf("SpikeNoData/n recorded a Continuation (episode %d)", c.Episode)
				}
			}
			var ms int64
			if err := w.e.d.Pool.QueryRow(t.Context(), `SELECT learned_repeat_interval_ms FROM alertmanager_routes
				WHERE integration_id = $1 AND route_path = '{}/{}'`, in.ID).Scan(&ms); err != nil {
				t.Fatalf("route {}/{}: %v", err)
			}
			if learned := time.Duration(ms) * time.Millisecond; learned < 120*time.Second || learned > 155*time.Second {
				t.Errorf("route {}/{} learned %v, want 120s to 155s", learned)
			}
		})

		t.Run("Max alerts 5", func(t *testing.T) {
			in, _ := w.replay(t, "trunc.jsonl", nil)
			raised := false
			for _, a := range w.e.builtinAlerts(t) {
				raised = raised || (a.Labels["alertname"] == "MusterSnapshotTruncated" &&
					a.Labels["integration"] == in.PublicID && a.Status == ingest.StatusFiring)
			}
			if !raised {
				t.Errorf("MusterSnapshotTruncated does not fire for %s", in.PublicID)
			}
		})

		// The truncated status: resolved of 07:53:10, after an untruncated firing body of its groupKey with its five
		// alerts and five more.
		for _, tt := range []struct {
			name, integration string
			orgID             func(map[string]any)
			firing            int
		}{
			{"truncated resolved with orgId", "trunc-with-orgid", func(map[string]any) {}, 5},
			{"truncated resolved without orgId", "trunc-without-orgid", func(b map[string]any) { delete(b, "orgId") }, 0},
			{"truncated resolved with orgId null", "trunc-orgid-null", func(b map[string]any) { b["orgId"] = nil }, 0},
		} {
			t.Run(tt.name, func(t *testing.T) {
				var res grafanaBody
				for _, b := range readGrafana(t, "trunc.jsonl") {
					if res.Body == nil && listingOf(t, b.Body).Alerts[0].Status == ingest.StatusResolved {
						res = b
					}
				}
				if !res.Recv.Equal(time.Date(2026, 10, 19, 7, 53, 10, 30e6, time.UTC)) {
					t.Fatalf("the first resolved body arrived at %v", res.Recv)
				}
				resolved, fire := decode(t, res.Body), decode(t, res.Body)
				fire["status"], fire["truncatedAlerts"] = ingest.StatusFiring, 0
				var more []any
				for _, raw := range fire["alerts"].([]any) {
					a := raw.(map[string]any)
					a["status"], a["endsAt"] = ingest.StatusFiring, "0001-01-01T00:00:00Z"
					other := decode(t, encode(t, a))
					other["labels"].(map[string]any)["host"] = "x" + a["labels"].(map[string]any)["host"].(string)
					other["fingerprint"] = "x" + a["fingerprint"].(string)
					more = append(more, other)
				}
				fire["alerts"] = append(fire["alerts"].([]any), more...)
				tt.orgID(fire)
				tt.orgID(resolved)
				in := w.integration(t, tt.integration)
				w.send(t, in, time.Date(2026, 10, 19, 7, 48, 10, 24e6, time.UTC), encode(t, fire))
				w.send(t, in, res.Recv, encode(t, resolved))
				if n := w.notProcessed(t, in); n != 0 {
					t.Errorf("%d stored snapshots were not processed", n)
				}
				firing := 0
				for _, a := range listingOf(t, encode(t, fire)).Alerts {
					got := w.alert(t, in, a.Fingerprint)
					listed := a.Fingerprint[0] != 'x'
					switch {
					case got.Status == ingest.StatusFiring && !listed:
						firing++
					case got.Status == ingest.StatusResolved && got.Reason == ingest.ResolveResolved:
					default:
						t.Errorf("%s/%s is %s/%s", a.Labels["alertname"], a.Labels["host"], got.Status, got.Reason)
					}
				}
				if firing != tt.firing {
					t.Errorf("%d alerts fire, want %d", firing, tt.firing)
				}
				if !w.truncated(t, in, listingOf(t, res.Body).GroupKey) {
					t.Error("the groupKey is not truncated")
				}
			})
		}
	})
}

func decode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func encode(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

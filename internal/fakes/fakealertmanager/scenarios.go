// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package fakealertmanager

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"
)

// The scenario library: one timed request sequence per verified Alertmanager fact that processing relies on — F-033
// to F-053 of design/facts.md — with what processing must show after it. F-045 and F-047 run with a live Heartbeat,
// whose signals are steps of their own, because only the Stale scan resolves what a muted group never sends.
// internal/ingest/facts_test.go runs every scenario through processing and the Stale scan with a manual clock.

// ScenarioReceiver is the receiver every scenario's groups send to.
const ScenarioReceiver = "scenario"

// Scenario replays one fact; Heartbeat says that its Integration has its Heartbeat on.
type Scenario struct {
	Fact        string
	Requirement string
	Title       string
	Heartbeat   bool
	Steps       []Step
}

// Step is one request of a scenario, At after its start: a group defined, an Alert set or removed, a notification
// sent — its copies CopyDelayMs apart — a Heartbeat signal, or an expectation checked after the Stale scan. StartsAt
// and EndsAt, offsets from the start, set the times of an Alert that the step sets.
type Step struct {
	At       time.Duration
	Group    string
	PutGroup *GroupSpec
	Alert    string
	PutAlert *AlertSpec
	StartsAt *time.Duration
	EndsAt   *time.Duration
	Remove   bool
	Notify   *NotifyOptions
	Signal   bool
	Expect   *Expect
}

// Expect is what processing shows after a step. Alerts are named "group/alert".
type Expect struct {
	// Firing are Alerts that fire; Resolved maps Alerts that resolved to their reason.
	Firing   []string
	Resolved map[string]string
	// Episodes are the firings an Alert recorded; StartsAt is its startsAt as an offset from the start.
	Episodes map[string]int64
	StartsAt map[string]time.Duration
	// Truncated says whether the groupKey of a group is truncated.
	Truncated map[string]bool
	// Learned bounds the learned repeat interval of an Alertmanager route path, both ends included.
	Learned map[string][2]time.Duration
	// Dropped is how many resolves processing dropped since the start, when set; Alerts how many Alerts it holds.
	Dropped *int
	Alerts  *int
}

func at(d time.Duration) *time.Duration { return &d }

func count(n int) *int { return &n }

func s(n int) time.Duration { return time.Duration(n) * time.Second }

func defineGroup(name, route string, labels map[string]string) Step {
	return Step{Group: name, PutGroup: &GroupSpec{Receiver: ScenarioReceiver, Route: route, Labels: labels}}
}

func fire(when time.Duration, group, alert string, labels map[string]string) Step {
	return Step{At: when, Group: group, Alert: alert, PutAlert: &AlertSpec{Labels: labels}}
}

// resolve resolves the Alert named "group/alert".
func resolve(when time.Duration, name string, labels map[string]string) Step {
	group, alert, _ := strings.Cut(name, "/")
	return Step{At: when, Group: group, Alert: alert, PutAlert: &AlertSpec{Labels: labels, Status: "resolved"}}
}

func notify(when time.Duration, group, reason string, o NotifyOptions) Step {
	o.Reason = reason
	return Step{At: when, Group: group, Notify: &o}
}

func expect(when time.Duration, e Expect) Step {
	return Step{At: when, Expect: &e}
}

func names(group, prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s/%s%02d", group, prefix, i)
	}
	return out
}

// numbered sets n firing Alerts named prefix00 to prefix(n-1) with the label pod.
func numbered(when time.Duration, group, prefix string, n int, status string) []Step {
	out := make([]Step, n)
	for i := range out {
		name := fmt.Sprintf("%s%02d", prefix, i)
		out[i] = Step{At: when, Group: group, Alert: name,
			PutAlert: &AlertSpec{Labels: map[string]string{"pod": name}, Status: status}}
	}
	return out
}

func list(prefix string, idx ...int) []string {
	out := make([]string, len(idx))
	for i, n := range idx {
		out[i] = fmt.Sprintf("%s%02d", prefix, n)
	}
	return out
}

func steps(parts ...any) []Step {
	var out []Step
	for _, p := range parts {
		switch v := p.(type) {
		case Step:
			out = append(out, v)
		case []Step:
			out = append(out, v...)
		}
	}
	return out
}

// signals are Heartbeat signals every minute from from to to, both included.
func signals(from, to time.Duration) []Step {
	var out []Step
	for t := from; t <= to; t += time.Minute {
		out = append(out, Step{At: t, Signal: true})
	}
	return out
}

// timeline is steps in the order of their times; steps of the same time keep the order they are given in, so that a
// Heartbeat signal given first comes before a notification of the same time.
func timeline(parts ...any) []Step {
	out := steps(parts...)
	slices.SortStableFunc(out, func(a, b Step) int { return cmp.Compare(a.At, b.At) })
	return out
}

func resolvedAll(alerts []string, reason string) map[string]string {
	out := map[string]string{}
	for _, a := range alerts {
		out[a] = reason
	}
	return out
}

var labelsA = map[string]string{"alertname": "A"}

// Scenarios are the scenarios of the verified facts, in the order of their ids.
func Scenarios() []Scenario {
	ten := names("g", "x", 10)
	// nine are the Alerts of ten but x04, which a restarted Alertmanager missed (F-042).
	nine := list("x", 0, 1, 2, 3, 5, 6, 7, 8, 9)
	return []Scenario{
		{Fact: "F-033", Requirement: "C-06.FR-8", Title: "a repeat comes on the first tick after repeat_interval",
			Steps: steps(defineGroup("rep", `{}/{team="ops"}`, map[string]string{"alertname": "Repeat"}),
				fire(0, "rep", "a", map[string]string{"instance": "a"}),
				notify(0, "rep", "first notification", NotifyOptions{}),
				notify(s(360), "rep", "repeat interval elapsed", NotifyOptions{}),
				notify(s(720), "rep", "repeat interval elapsed", NotifyOptions{}),
				notify(s(1080), "rep", "repeat interval elapsed", NotifyOptions{}),
				expect(s(1080), Expect{Firing: []string{"rep/a"},
					Learned: map[string][2]time.Duration{`{}/{team="ops"}`: {359980 * time.Millisecond,
						360100 * time.Millisecond}}}))},
		{Fact: "F-034", Requirement: "C-06.FR-8", Title: "in an HA pair, repeat gaps vary",
			Steps: steps(defineGroup("rep", "{}", map[string]string{"alertname": "Repeat"}),
				fire(0, "rep", "a", map[string]string{"instance": "a"}),
				notify(0, "rep", "first notification", NotifyOptions{}),
				notify(s(345), "rep", "repeat interval elapsed", NotifyOptions{}),
				notify(s(660), "rep", "repeat interval elapsed", NotifyOptions{}),
				notify(s(1005), "rep", "repeat interval elapsed", NotifyOptions{}),
				notify(s(1320), "rep", "repeat interval elapsed", NotifyOptions{}),
				expect(s(1320), Expect{Learned: map[string][2]time.Duration{"{}": {s(300) + time.Millisecond, s(360)}}}))},
		{Fact: "F-035", Requirement: "C-06.FR-8", Title: "a reload can stretch a gap",
			Steps: steps(defineGroup("rep", "{}", map[string]string{"alertname": "Repeat"}),
				fire(0, "rep", "a", map[string]string{"instance": "a"}),
				notify(0, "rep", "first notification", NotifyOptions{}),
				notify(s(360), "rep", "repeat interval elapsed", NotifyOptions{}),
				notify(s(720), "rep", "repeat interval elapsed", NotifyOptions{}),
				notify(s(1084), "rep", "repeat interval elapsed", NotifyOptions{}),
				notify(s(1444), "rep", "repeat interval elapsed", NotifyOptions{}),
				expect(s(1444), Expect{Learned: map[string][2]time.Duration{"{}": {s(360), s(360)}}}))},
		{Fact: "F-036", Requirement: "C-06.FR-7", Title: "new route matchers mean a new groupKey",
			Steps: steps(defineGroup("old", `{}/{team="a"}`, map[string]string{"alertname": "Disk"}),
				defineGroup("new", `{}/{team=~"a|b"}`, map[string]string{"alertname": "Disk"}),
				fire(0, "old", "x", map[string]string{"instance": "x"}),
				fire(0, "old", "y", map[string]string{"instance": "y"}),
				notify(0, "old", "first notification", NotifyOptions{}),
				fire(s(60), "new", "x", map[string]string{"instance": "x"}),
				fire(s(60), "new", "y", map[string]string{"instance": "y"}),
				notify(s(60), "new", "first notification", NotifyOptions{}),
				notify(s(420), "new", "repeat interval elapsed", NotifyOptions{}),
				Step{At: s(480), Group: "new", Alert: "y", Remove: true},
				notify(s(780), "new", "repeat interval elapsed", NotifyOptions{}),
				notify(s(1140), "new", "repeat interval elapsed", NotifyOptions{}),
				expect(s(1140), Expect{Firing: []string{"new/x", "new/y", "old/y"}, Alerts: count(2)}))},
		{Fact: "F-037", Requirement: "C-06.FR-2", Title: "the copies of an HA pair are identical",
			Steps: steps(defineGroup("g", "{}", labelsA),
				fire(0, "g", "a", map[string]string{"instance": "a"}),
				fire(0, "g", "b", map[string]string{"instance": "b"}),
				fire(0, "g", "c", map[string]string{"instance": "c"}),
				notify(0, "g", "first notification", NotifyOptions{Copies: 2}),
				expect(0, Expect{Firing: []string{"g/a", "g/b", "g/c"}, Alerts: count(3),
					Episodes: map[string]int64{"g/a": 1, "g/b": 1, "g/c": 1}}),
				notify(s(15), "g", "first notification", NotifyOptions{}),
				expect(s(15), Expect{Alerts: count(3), Episodes: map[string]int64{"g/a": 1, "g/b": 1, "g/c": 1}}))},
		{Fact: "F-038", Requirement: "C-06.FR-2", Title: "a slow answer brings a copy one peer timeout later",
			Steps: steps(defineGroup("g", "{}", labelsA),
				fire(0, "g", "a", map[string]string{"instance": "a"}),
				fire(0, "g", "b", map[string]string{"instance": "b"}),
				notify(0, "g", "first notification", NotifyOptions{Copies: 2, CopyDelayMs: 15000}),
				fire(s(60), "g", "c", map[string]string{"instance": "c"}),
				notify(s(60), "g", "new alerts added", NotifyOptions{Copies: 2, CopyDelayMs: 15000}),
				resolve(s(120), "g/b", map[string]string{"instance": "b"}),
				notify(s(120), "g", "some alerts resolved", NotifyOptions{Copies: 2, CopyDelayMs: 15000}),
				expect(s(140), Expect{Firing: []string{"g/a", "g/c"}, Resolved: map[string]string{"g/b": "resolved"},
					Dropped: count(0), Episodes: map[string]int64{"g/a": 1, "g/b": 1, "g/c": 1}}))},
		{Fact: "F-039", Requirement: "C-06.FR-2", Title: "a failure longer than the peer timeout is delivered twice",
			Steps: steps(defineGroup("g", "{}", labelsA),
				fire(0, "g", "a", map[string]string{"instance": "a"}),
				notify(0, "g", "first notification", NotifyOptions{}),
				fire(s(60), "g", "b", map[string]string{"instance": "b"}),
				notify(s(105), "g", "new alerts added", NotifyOptions{Copies: 2, CopyDelayMs: 900}),
				expect(s(110), Expect{Firing: []string{"g/a", "g/b"}, Episodes: map[string]int64{"g/a": 1, "g/b": 1},
					Alerts: count(2)}))},
		{Fact: "F-040", Requirement: "C-06.FR-8", Title: "a split cluster sends everything twice",
			Steps: steps(defineGroup("g", "{}", labelsA),
				fire(0, "g", "a", map[string]string{"instance": "a"}),
				notify(0, "g", "first notification", NotifyOptions{Copies: 2, CopyDelayMs: 1}),
				notify(s(300), "g", "repeat interval elapsed", NotifyOptions{Copies: 2, CopyDelayMs: 1}),
				notify(s(600), "g", "repeat interval elapsed", NotifyOptions{Copies: 2}),
				notify(s(900), "g", "repeat interval elapsed", NotifyOptions{Copies: 2, CopyDelayMs: 1}),
				expect(s(901), Expect{Firing: []string{"g/a"}, Episodes: map[string]int64{"g/a": 1},
					Learned: map[string][2]time.Duration{"{}": {s(300), s(300)}}}))},
		{Fact: "F-041", Requirement: "C-06.FR-5", Title: "a restarted HA instance can send a partial Snapshot",
			Steps: steps(defineGroup("g", "{}", labelsA), numbered(0, "g", "x", 10, ""),
				notify(0, "g", "first notification", NotifyOptions{}),
				notify(s(360), "g", "repeat interval elapsed", NotifyOptions{}),
				notify(s(600), "g", "repeat interval elapsed", NotifyOptions{List: list("x", 0)}),
				notify(s(615), "g", "repeat interval elapsed", NotifyOptions{}),
				notify(s(960), "g", "repeat interval elapsed", NotifyOptions{}),
				notify(s(1320), "g", "repeat interval elapsed", NotifyOptions{}),
				expect(s(1320), Expect{Firing: ten}))},
		{Fact: "F-042", Requirement: "C-06.FR-5", Title: "a single instance without a volume starts with partial Snapshots",
			Steps: steps(defineGroup("g", "{}", labelsA), numbered(0, "g", "x", 10, ""),
				notify(0, "g", "first notification", NotifyOptions{}),
				notify(s(600), "g", "first notification", NotifyOptions{List: list("x", 0, 1, 2)}),
				notify(s(660), "g", "new alerts added", NotifyOptions{List: nine}),
				notify(s(720), "g", "new alerts added", NotifyOptions{}),
				expect(s(720), Expect{Firing: ten}),
				notify(s(1320), "g", "repeat interval elapsed", NotifyOptions{List: nine}),
				notify(s(1380), "g", "repeat interval elapsed", NotifyOptions{List: nine}),
				expect(s(1380), Expect{Firing: []string{"g/x04"}}),
				notify(s(1680), "g", "repeat interval elapsed", NotifyOptions{List: nine}),
				expect(s(1680), Expect{Resolved: map[string]string{"g/x04": "gone"}}))},
		{Fact: "F-043", Requirement: "C-06.FR-5", Title: "Prometheus re-sends firing alerts every 2 minutes",
			Steps: steps(defineGroup("g", "{}", labelsA), numbered(0, "g", "x", 10, ""),
				notify(0, "g", "first notification", NotifyOptions{}),
				notify(s(600), "g", "first notification", NotifyOptions{List: list("x", 0, 1, 2, 3, 4)}),
				notify(s(720), "g", "new alerts added", NotifyOptions{}),
				notify(s(1080), "g", "repeat interval elapsed", NotifyOptions{}),
				expect(s(1080), Expect{Firing: ten}))},
		{Fact: "F-044", Requirement: "C-06.FR-21", Title: "a full HA restart can leave resolves unsent",
			Steps: steps(defineGroup("g20", "{}", map[string]string{"alertname": "Twenty"}),
				defineGroup("g10", "{}", map[string]string{"alertname": "Ten"}),
				numbered(0, "g20", "y", 20, ""), numbered(0, "g10", "z", 10, ""),
				notify(0, "g20", "first notification", NotifyOptions{}),
				notify(0, "g10", "first notification", NotifyOptions{}),
				numbered(s(300), "g20", "y", 20, "resolved"), numbered(s(300), "g10", "z", 10, "resolved"),
				notify(s(300), "g20", "all alerts resolved", NotifyOptions{List: list("y", 0, 1, 2, 3, 4, 5)}),
				notify(s(300), "g10", "all alerts resolved", NotifyOptions{List: list("z", 0)}),
				expect(s(300), Expect{Resolved: resolvedAll(append(names("g20", "y", 20), names("g10", "z", 10)...),
					"resolved"), Dropped: count(0)}))},
		{Fact: "F-045", Requirement: "C-06.FR-8", Title: "a wholly muted group sends nothing", Heartbeat: true,
			Steps: timeline(signals(0, s(2400)),
				defineGroup("g", `{}/{team="ops"}`, map[string]string{"alertname": "Muted"}),
				fire(0, "g", "a", map[string]string{"instance": "a"}),
				notify(0, "g", "first notification", NotifyOptions{}),
				notify(s(300), "g", "repeat interval elapsed", NotifyOptions{}),
				notify(s(600), "g", "repeat interval elapsed", NotifyOptions{}),
				notify(s(900), "g", "repeat interval elapsed", NotifyOptions{}),
				// The whole group is silenced from here: nothing is sent, repeats included.
				expect(s(1800), Expect{Firing: []string{"g/a"},
					Learned: map[string][2]time.Duration{`{}/{team="ops"}`: {s(300), s(300)}}}),
				expect(s(1860), Expect{Resolved: map[string]string{"g/a": "stale"}}),
				// After a long mute the group returns as a first notification: a new firing.
				notify(s(2400), "g", "first notification", NotifyOptions{}),
				expect(s(2400), Expect{Firing: []string{"g/a"}, Episodes: map[string]int64{"g/a": 2}}))},
		{Fact: "F-046", Requirement: "C-06.FR-4", Title: "resolves come back for about 15 minutes",
			Steps: steps(defineGroup("g", "{}", labelsA),
				fire(0, "g", "a", map[string]string{"instance": "a"}),
				fire(0, "g", "b", map[string]string{"instance": "b"}),
				notify(0, "g", "first notification", NotifyOptions{}),
				resolve(s(120), "g/a", map[string]string{"instance": "a"}),
				notify(s(120), "g", "some alerts resolved", NotifyOptions{}),
				notify(s(240), "g", "some alerts resolved", NotifyOptions{List: []string{"a"}}),
				notify(s(360), "g", "repeat interval elapsed", NotifyOptions{}),
				notify(s(480), "g", "some alerts resolved", NotifyOptions{}),
				notify(s(720), "g", "repeat interval elapsed", NotifyOptions{}),
				expect(s(720), Expect{Resolved: map[string]string{"g/a": "resolved"}, Firing: []string{"g/b"},
					Episodes: map[string]int64{"g/a": 1}, Dropped: count(0)}),
				fire(s(840), "g", "a", map[string]string{"instance": "a"}),
				notify(s(840), "g", "new alerts added", NotifyOptions{}),
				Step{At: s(900), Group: "g", Alert: "a", StartsAt: at(0),
					PutAlert: &AlertSpec{Labels: map[string]string{"instance": "a"}, Status: "resolved"}},
				notify(s(900), "g", "some alerts resolved", NotifyOptions{}),
				expect(s(900), Expect{Firing: []string{"g/a", "g/b"}, Episodes: map[string]int64{"g/a": 2},
					StartsAt: map[string]time.Duration{"g/a": s(840)}, Dropped: count(0)}))},
		{Fact: "F-047", Requirement: "C-06.FR-8", Title: "a resolve during a mute arrives if the mute ends soon",
			Heartbeat: true,
			Steps: timeline(signals(0, s(1860)),
				defineGroup("short", `{}/{mute="short"}`, map[string]string{"alertname": "Short"}),
				defineGroup("long", `{}/{mute="long"}`, map[string]string{"alertname": "Long"}),
				fire(0, "short", "a", map[string]string{"instance": "a"}),
				fire(0, "long", "b", map[string]string{"instance": "b"}),
				notify(0, "short", "first notification", NotifyOptions{}),
				notify(0, "long", "first notification", NotifyOptions{}),
				notify(s(300), "short", "repeat interval elapsed", NotifyOptions{}),
				notify(s(300), "long", "repeat interval elapsed", NotifyOptions{}),
				notify(s(600), "short", "repeat interval elapsed", NotifyOptions{}),
				notify(s(600), "long", "repeat interval elapsed", NotifyOptions{}),
				notify(s(900), "short", "repeat interval elapsed", NotifyOptions{}),
				notify(s(900), "long", "repeat interval elapsed", NotifyOptions{}),
				// Both groups are muted from here, and both alerts resolve during the mute.
				resolve(s(1020), "short/a", map[string]string{"instance": "a"}),
				resolve(s(1020), "long/b", map[string]string{"instance": "b"}),
				// The short mute ends after 10 minutes, and its resolve arrives then.
				notify(s(1500), "short", "all alerts resolved", NotifyOptions{}),
				expect(s(1500), Expect{Resolved: map[string]string{"short/a": "resolved"}, Firing: []string{"long/b"}}),
				// The long mute never sends the resolve: the Alert goes Stale.
				expect(s(1860), Expect{Resolved: map[string]string{"short/a": "resolved", "long/b": "stale"}}))},
		{Fact: "F-048", Requirement: "C-06.FR-6", Title: "max_alerts keeps the first alerts",
			Steps: steps(defineGroup("g", "{}", labelsA),
				fire(0, "g", "i3", map[string]string{"instance": "i3"}),
				fire(0, "g", "i4", map[string]string{"instance": "i4"}),
				notify(0, "g", "first notification", NotifyOptions{MaxAlerts: 2}),
				expect(0, Expect{Truncated: map[string]bool{"g": false}}),
				fire(s(120), "g", "i1", map[string]string{"instance": "i1"}),
				fire(s(120), "g", "i2", map[string]string{"instance": "i2"}),
				notify(s(120), "g", "new alerts added", NotifyOptions{MaxAlerts: 2}),
				notify(s(480), "g", "repeat interval elapsed", NotifyOptions{MaxAlerts: 2}),
				notify(s(840), "g", "repeat interval elapsed", NotifyOptions{MaxAlerts: 2}),
				notify(s(1200), "g", "repeat interval elapsed", NotifyOptions{MaxAlerts: 2}),
				expect(s(1200), Expect{Firing: []string{"g/i1", "g/i2", "g/i3", "g/i4"},
					Truncated: map[string]bool{"g": true}}),
				resolve(s(1300), "g/i4", map[string]string{"instance": "i4"}),
				notify(s(1300), "g", "some alerts resolved", NotifyOptions{MaxAlerts: 2}),
				expect(s(1300), Expect{Firing: []string{"g/i4"}}),
				resolve(s(1400), "g/i1", map[string]string{"instance": "i1"}),
				resolve(s(1400), "g/i2", map[string]string{"instance": "i2"}),
				resolve(s(1400), "g/i3", map[string]string{"instance": "i3"}),
				notify(s(1400), "g", "all alerts resolved", NotifyOptions{MaxAlerts: 2}),
				expect(s(1400), Expect{Resolved: resolvedAll([]string{"g/i1", "g/i2", "g/i3", "g/i4"}, "resolved"),
					Dropped: count(0)}))},
		{Fact: "F-049", Requirement: "C-06.FR-7", Title: "sibling routes with the same matchers share a groupKey",
			Steps: steps(defineGroup("g", `{}/{team="db"}`, labelsA),
				fire(0, "g", "a", map[string]string{"instance": "a"}),
				notify(0, "g", "first notification", NotifyOptions{Copies: 2, CopyDelayMs: 23}),
				notify(s(300), "g", "repeat interval elapsed", NotifyOptions{}),
				notify(s(600), "g", "repeat interval elapsed", NotifyOptions{}),
				resolve(s(700), "g/a", map[string]string{"instance": "a"}),
				notify(s(700), "g", "all alerts resolved", NotifyOptions{Copies: 2, CopyDelayMs: 3}),
				expect(s(701), Expect{Resolved: map[string]string{"g/a": "resolved"}, Dropped: count(0), Alerts: count(1),
					Learned: map[string][2]time.Duration{`{}/{team="db"}`: {s(300), s(300)}}}))},
		{Fact: "F-050", Requirement: "C-06.FR-8", Title: "routeLabels and notification_reason",
			Steps: steps(defineGroup("g", `{}/{team="g"}`, labelsA),
				defineGroup("h", `{}/{team="h"}`, map[string]string{"alertname": "H"}),
				fire(0, "g", "a", map[string]string{"instance": "a"}),
				fire(0, "h", "b", map[string]string{"instance": "b"}),
				notify(0, "g", "first notification", NotifyOptions{}),
				notify(0, "h", "first notification", NotifyOptions{OmitReason: true}),
				notify(s(300), "g", "repeat interval elapsed", NotifyOptions{}),
				notify(s(300), "h", "repeat interval elapsed", NotifyOptions{OmitReason: true}),
				notify(s(600), "g", "repeat interval elapsed", NotifyOptions{}),
				expect(s(600), Expect{Learned: map[string][2]time.Duration{`{}/{team="g"}`: {s(300), s(300)},
					`{}/{team="h"}`: {s(300), s(300)}}}),
				resolve(s(700), "g/a", map[string]string{"instance": "a"}),
				notify(s(700), "g", "all alerts resolved", NotifyOptions{}),
				fire(s(1000), "g", "a", map[string]string{"instance": "a"}),
				notify(s(1000), "g", "first notification", NotifyOptions{}),
				expect(s(1000), Expect{Firing: []string{"g/a"}, Episodes: map[string]int64{"g/a": 2}}))},
		{Fact: "F-051", Requirement: "C-06.FR-11", Title: "endsAt is the last send plus 4 minutes",
			Steps: steps(defineGroup("g", "{}", labelsA),
				Step{At: 0, Group: "g", Alert: "a", EndsAt: at(s(240)),
					PutAlert: &AlertSpec{Labels: map[string]string{"instance": "a"}}},
				notify(0, "g", "first notification", NotifyOptions{}),
				notify(s(360), "g", "repeat interval elapsed", NotifyOptions{}),
				notify(s(720), "g", "repeat interval elapsed", NotifyOptions{}),
				expect(s(720), Expect{Firing: []string{"g/a"}, Episodes: map[string]int64{"g/a": 1}}))},
		{Fact: "F-052", Requirement: "C-06.FR-11", Title: "a Prometheus restart can produce a Continuation",
			Steps: steps(defineGroup("g", "{}", labelsA),
				fire(0, "g", "a", map[string]string{"instance": "a"}),
				notify(0, "g", "first notification", NotifyOptions{}),
				notify(s(360), "g", "repeat interval elapsed", NotifyOptions{}),
				Step{At: s(600), Group: "g", Alert: "a", StartsAt: at(s(600)),
					PutAlert: &AlertSpec{Labels: map[string]string{"instance": "a"}}},
				notify(s(720), "g", "repeat interval elapsed", NotifyOptions{}),
				expect(s(720), Expect{Firing: []string{"g/a"}, Episodes: map[string]int64{"g/a": 1},
					StartsAt: map[string]time.Duration{"g/a": s(600)}}),
				resolve(s(900), "g/a", map[string]string{"instance": "a"}),
				notify(s(900), "g", "all alerts resolved", NotifyOptions{}),
				fire(s(1000), "g", "a", map[string]string{"instance": "a"}),
				notify(s(1000), "g", "first notification", NotifyOptions{}),
				expect(s(1000), Expect{Firing: []string{"g/a"}, Episodes: map[string]int64{"g/a": 2},
					StartsAt: map[string]time.Duration{"g/a": s(1000)}}))},
		{Fact: "F-053", Requirement: "C-06.FR-11", Title: "vmalert gives no Continuation",
			Steps: steps(defineGroup("g", "{}", labelsA),
				fire(0, "g", "a", map[string]string{"instance": "a"}),
				notify(0, "g", "first notification", NotifyOptions{}),
				notify(s(360), "g", "repeat interval elapsed", NotifyOptions{}),
				notify(s(720), "g", "repeat interval elapsed", NotifyOptions{}),
				expect(s(720), Expect{Firing: []string{"g/a"}, Episodes: map[string]int64{"g/a": 1},
					StartsAt: map[string]time.Duration{"g/a": 0}}),
				resolve(s(1200), "g/a", map[string]string{"instance": "a"}),
				notify(s(1200), "g", "all alerts resolved", NotifyOptions{}),
				fire(s(1320), "g", "a", map[string]string{"instance": "a"}),
				notify(s(1320), "g", "first notification", NotifyOptions{}),
				expect(s(1320), Expect{Firing: []string{"g/a"}, Episodes: map[string]int64{"g/a": 2}}))},
	}
}

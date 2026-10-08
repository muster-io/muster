// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package fakealertmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

// The group model: Alertmanager groups of a receiver, their Alerts, and the Snapshots Alertmanager would send for
// them, as the verified facts F-033 to F-053 describe (design/facts.md).

// zeroTime is what Alertmanager sends as the endsAt of an Alert without an end.
const zeroTime = "0001-01-01T00:00:00Z"

// maxCopies bounds the identical copies of one notification.
const maxCopies = 10

// GroupSpec defines an Alertmanager group: the receiver it sends to, the route part of its groupKey, `{}` when empty,
// and its group labels.
type GroupSpec struct {
	Receiver string            `json:"receiver"`
	Route    string            `json:"route"`
	Labels   map[string]string `json:"labels"`
}

// AlertSpec sets an Alert of a group. The group labels are merged into its labels. StartsAt and EndsAt are RFC 3339
// times or "now"; StartsAt defaults to now at the Alert's first firing, and a resolve sets EndsAt to now unless
// given. Status is firing or resolved, firing by default; Fingerprint replaces the one computed from the labels;
// GeneratorURL is sent as the Alert's generatorURL, empty by default.
type AlertSpec struct {
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	Status       string            `json:"status"`
	StartsAt     string            `json:"starts_at"`
	EndsAt       string            `json:"ends_at"`
	Fingerprint  string            `json:"fingerprint"`
	GeneratorURL string            `json:"generator_url"`
}

// NotifyOptions shape the Snapshot of a group: its notification_reason (left out when empty or with OmitReason), the
// Alerts it lists — all that are not removed, in Alertmanager's order, or only those named in List — cut to
// MaxAlerts with truncatedAlerts counting what was left out, its status — firing while any Alert of the group fires
// unless given — and Copies identical bodies CopyDelayMs apart.
type NotifyOptions struct {
	Reason      string   `json:"reason"`
	List        []string `json:"list"`
	MaxAlerts   int      `json:"max_alerts"`
	Status      string   `json:"status"`
	Copies      int      `json:"copies"`
	CopyDelayMs int      `json:"copy_delay_ms"`
	OmitReason  bool     `json:"omit_reason"`
}

// Sent is one copy of a notification: Muster's answer and how many Alerts it listed and left out.
type Sent struct {
	Status    int `json:"status"`
	Listed    int `json:"listed"`
	Truncated int `json:"truncated"`
}

// NotifyResult is what a notification sent.
type NotifyResult struct {
	GroupKey string `json:"group_key"`
	Sent     []Sent `json:"sent"`
}

type fakeGroup struct {
	spec   GroupSpec
	alerts map[string]*fakeAlert
}

type fakeAlert struct {
	labels, annotations map[string]string
	status              string
	startsAt, endsAt    time.Time
	fingerprint         string
	generatorURL        string
	removed             bool
}

// Snapshot is one webhook body built from a group, with what it listed and left out.
type Snapshot struct {
	Body      []byte
	Listed    int
	Truncated int
}

func (f *Fake) now() time.Time {
	f.mu.Lock()
	now := f.clock
	f.mu.Unlock()
	if now == nil {
		return time.Now().UTC()
	}
	return now().UTC()
}

// SetClock makes the group model read the time from now, such as a manual clock in a test; nil is the system time.
func (f *Fake) SetClock(now func() time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clock = now
}

// groupKey renders the groupKey as Alertmanager does: the route, a colon and the group labels sorted by name, each
// name="value" with Go quoting, separated by a comma and a space.
func groupKey(s GroupSpec) string {
	pairs := make([]string, 0, len(s.Labels))
	for _, name := range slices.Sorted(maps.Keys(s.Labels)) {
		pairs = append(pairs, name+"="+strconv.Quote(s.Labels[name]))
	}
	return s.Route + ":{" + strings.Join(pairs, ", ") + "}"
}

// PutGroup defines the group name, or replaces its definition and keeps its Alerts, and returns its groupKey.
func (f *Fake) PutGroup(name string, s GroupSpec) (string, error) {
	if name == "" || s.Receiver == "" {
		return "", errors.New("a group needs a name and a receiver")
	}
	if s.Route == "" {
		s.Route = "{}"
	}
	s.Labels = maps.Clone(s.Labels)
	if s.Labels == nil {
		s.Labels = map[string]string{}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if g, ok := f.groups[name]; ok {
		g.spec = s
	} else {
		f.groups[name] = &fakeGroup{spec: s, alerts: map[string]*fakeAlert{}}
	}
	return groupKey(s), nil
}

// parseTime reads an RFC 3339 time or "now".
func parseTime(s string, now time.Time) (time.Time, error) {
	if s == "now" {
		return now, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is neither an RFC 3339 time nor now", s)
	}
	return t.UTC(), nil
}

// PutAlert sets the Alert name of a group and lists it again if it was removed.
func (f *Fake) PutAlert(group, name string, s AlertSpec) error {
	now := f.now()
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.groups[group]
	if !ok {
		return fmt.Errorf("no group %q is defined", group)
	}
	if s.Status == "" {
		s.Status = "firing"
	}
	if s.Status != "firing" && s.Status != "resolved" {
		return errors.New(`status must be "firing" or "resolved"`)
	}
	a, existed := g.alerts[name]
	if !existed {
		a = &fakeAlert{}
	}
	firstFiring := !existed || a.status != "firing"
	labels := maps.Clone(s.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	maps.Copy(labels, g.spec.Labels)
	annotations := maps.Clone(s.Annotations)
	if annotations == nil {
		annotations = map[string]string{}
	}
	next := fakeAlert{labels: labels, annotations: annotations, status: s.Status, startsAt: a.startsAt,
		fingerprint: s.Fingerprint, generatorURL: s.GeneratorURL}
	if next.fingerprint == "" {
		next.fingerprint = fingerprint(labels)
	}
	switch {
	case s.StartsAt != "":
		t, err := parseTime(s.StartsAt, now)
		if err != nil {
			return fmt.Errorf("starts_at: %w", err)
		}
		next.startsAt = t
	case firstFiring && s.Status == "firing", next.startsAt.IsZero():
		next.startsAt = now
	}
	switch {
	case s.EndsAt != "":
		t, err := parseTime(s.EndsAt, now)
		if err != nil {
			return fmt.Errorf("ends_at: %w", err)
		}
		next.endsAt = t
	case s.Status == "resolved" && a.status == "resolved" && !a.endsAt.IsZero():
		next.endsAt = a.endsAt
	case s.Status == "resolved":
		next.endsAt = now
	}
	*a = next
	g.alerts[name] = a
	return nil
}

// RemoveAlert stops listing the Alert name of a group without a resolve, as a silence or an inhibition does.
func (f *Fake) RemoveAlert(group, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.groups[group]
	if !ok {
		return fmt.Errorf("no group %q is defined", group)
	}
	a, ok := g.alerts[name]
	if !ok {
		return fmt.Errorf("group %q has no alert %q", group, name)
	}
	a.removed = true
	return nil
}

// Fingerprint is the fingerprint of the Alert name of a group, or empty.
func (f *Fake) Fingerprint(group, name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if g, ok := f.groups[group]; ok {
		if a, ok := g.alerts[name]; ok {
			return a.fingerprint
		}
	}
	return ""
}

// before is Alertmanager's order of the Alerts of a group: by the label job, then instance — an Alert with the label
// before one without — then by the label sets, a smaller set first and equal sizes by name and value in name order.
func before(a, b map[string]string) bool {
	for _, name := range []string{"job", "instance"} {
		av, aok := a[name]
		bv, bok := b[name]
		switch {
		case !aok && !bok:
			continue
		case !aok:
			return false
		case !bok:
			return true
		case av != bv:
			return av < bv
		}
	}
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	names := slices.Sorted(maps.Keys(a))
	for name := range b {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		av, aok := a[name]
		if !aok {
			return true
		}
		bv, bok := b[name]
		if !bok {
			return false
		}
		if av != bv {
			return av < bv
		}
	}
	return false
}

// Snapshots builds the bodies a notification of the group sends, without sending them: the copies are identical.
func (f *Fake) Snapshots(group string, o NotifyOptions) (string, Receiver, []Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.groups[group]
	if !ok {
		return "", Receiver{}, nil, fmt.Errorf("no group %q is defined", group)
	}
	rc, ok := f.receivers[g.spec.Receiver]
	if !ok {
		return "", Receiver{}, nil, fmt.Errorf("no receiver %q is registered", g.spec.Receiver)
	}
	var listed []*fakeAlert
	if o.List != nil {
		for _, name := range o.List {
			a, ok := g.alerts[name]
			if !ok {
				return "", Receiver{}, nil, fmt.Errorf("group %q has no alert %q", group, name)
			}
			listed = append(listed, a)
		}
	} else {
		for _, a := range g.alerts {
			if !a.removed {
				listed = append(listed, a)
			}
		}
	}
	slices.SortStableFunc(listed, func(a, b *fakeAlert) int {
		switch {
		case before(a.labels, b.labels):
			return -1
		case before(b.labels, a.labels):
			return 1
		}
		return 0
	})
	truncated := 0
	if o.MaxAlerts > 0 && len(listed) > o.MaxAlerts {
		truncated = len(listed) - o.MaxAlerts
		listed = listed[:o.MaxAlerts]
	}
	status := o.Status
	if status == "" {
		status = "resolved"
		for _, a := range g.alerts {
			if !a.removed && a.status == "firing" {
				status = "firing"
			}
		}
	}
	body, err := json.Marshal(f.groupWebhook(g, o, status, listed, truncated))
	if err != nil {
		return "", Receiver{}, nil, err
	}
	copies := min(max(o.Copies, 1), maxCopies)
	out := make([]Snapshot, copies)
	for i := range out {
		out[i] = Snapshot{Body: body, Listed: len(listed), Truncated: truncated}
	}
	return groupKey(g.spec), rc, out, nil
}

// groupBody is a version 4 webhook of the group model, with routeLabels and notification_reason (F-050).
type groupBody struct {
	Version           string            `json:"version"`
	GroupKey          string            `json:"groupKey"`
	TruncatedAlerts   int               `json:"truncatedAlerts"`
	Status            string            `json:"status"`
	Receiver          string            `json:"receiver"`
	GroupLabels       map[string]string `json:"groupLabels"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
	ExternalURL       string            `json:"externalURL"`
	RouteLabels       map[string]string `json:"routeLabels"`
	Reason            *string           `json:"notification_reason,omitempty"`
	Alerts            []alert           `json:"alerts"`
}

func (f *Fake) groupWebhook(g *fakeGroup, o NotifyOptions, status string, listed []*fakeAlert,
	truncated int) groupBody {
	externalURL := "http://localhost:9093"
	if f.Server != nil && f.URL() != "" {
		externalURL = f.URL()
	}
	b := groupBody{Version: "4", GroupKey: groupKey(g.spec), TruncatedAlerts: truncated, Status: status,
		Receiver: g.spec.Receiver, GroupLabels: g.spec.Labels, CommonLabels: map[string]string{},
		CommonAnnotations: map[string]string{}, ExternalURL: externalURL, RouteLabels: map[string]string{},
		Alerts: make([]alert, 0, len(listed))}
	if o.Reason != "" && !o.OmitReason {
		b.Reason = &o.Reason
	}
	for i, a := range listed {
		if i == 0 {
			b.CommonLabels, b.CommonAnnotations = maps.Clone(a.labels), maps.Clone(a.annotations)
		}
		maps.DeleteFunc(b.CommonLabels, func(k, v string) bool { return a.labels[k] != v })
		maps.DeleteFunc(b.CommonAnnotations, func(k, v string) bool { return a.annotations[k] != v })
		ends := zeroTime
		if !a.endsAt.IsZero() {
			ends = a.endsAt.Format(time.RFC3339Nano)
		}
		b.Alerts = append(b.Alerts, alert{Status: a.status, Labels: a.labels, Annotations: a.annotations,
			StartsAt: a.startsAt.Format(time.RFC3339Nano), EndsAt: ends, GeneratorURL: a.generatorURL,
			Fingerprint: a.fingerprint})
	}
	return b
}

// Notify sends the Snapshot of the group to its receiver, each copy CopyDelayMs after the one before.
func (f *Fake) Notify(ctx context.Context, group string, o NotifyOptions) (NotifyResult, error) {
	key, rc, snaps, err := f.Snapshots(group, o)
	if err != nil {
		return NotifyResult{}, err
	}
	res := NotifyResult{GroupKey: key, Sent: make([]Sent, 0, len(snaps))}
	for i, s := range snaps {
		if i > 0 && o.CopyDelayMs > 0 {
			t := time.NewTimer(time.Duration(o.CopyDelayMs) * time.Millisecond)
			select {
			case <-ctx.Done():
				t.Stop()
				return res, ctx.Err()
			case <-t.C:
			}
		}
		status, err := f.Send(ctx, rc.Endpoint(), s.Body)
		if err != nil {
			return res, err
		}
		res.Sent = append(res.Sent, Sent{Status: status, Listed: s.Listed, Truncated: s.Truncated})
	}
	return res, nil
}

func (f *Fake) handlePutGroup(w http.ResponseWriter, r *http.Request) {
	var in GroupSpec
	if err := fakeserver.DecodeJSON(w, r, &in); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	key, err := f.PutGroup(r.PathValue("group"), in)
	if err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	fakeserver.WriteJSON(w, http.StatusOK, map[string]string{"group_key": key})
}

func (f *Fake) handlePutAlert(w http.ResponseWriter, r *http.Request) {
	var in AlertSpec
	if err := fakeserver.DecodeJSON(w, r, &in); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := f.PutAlert(r.PathValue("group"), r.PathValue("alert"), in); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (f *Fake) handleDeleteAlert(w http.ResponseWriter, r *http.Request) {
	if err := f.RemoveAlert(r.PathValue("group"), r.PathValue("alert")); err != nil {
		fakeserver.WriteError(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (f *Fake) handleNotify(w http.ResponseWriter, r *http.Request) {
	var in NotifyOptions
	if err := fakeserver.DecodeJSON(w, r, &in); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, _, _, err := f.Snapshots(r.PathValue("group"), in); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := f.Notify(r.Context(), r.PathValue("group"), in)
	if err != nil {
		fakeserver.WriteError(w, http.StatusBadGateway, err.Error())
		return
	}
	fakeserver.WriteJSON(w, http.StatusOK, res)
}

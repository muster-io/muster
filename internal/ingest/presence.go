// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"cmp"
	"slices"
	"time"
)

// GoneMinAbsence is processing.gone_min_absence: an Alert missing from consecutive Snapshots of its groupKey is Gone
// there once a Snapshot arrives this long after the first that missed it.
const GoneMinAbsence = 5 * time.Minute

// GoneReasonText is the reason of an Alert resolved as Gone or Stale (C-06.FR-10).
const GoneReasonText = "Alertmanager no longer reports this alert — it resolved without notice, was silenced or " +
	"inhibited in Alertmanager, or the Alertmanager routing changed"

// The states of an Alert's presence in a groupKey.
const (
	PresenceListed = "listed"
	PresenceMissed = "missed"
	PresenceGone   = "gone"
	PresenceStale  = "stale"
)

// group is an alertmanager_groups row as processing reads and writes it.
type group struct {
	ID              int64
	WindowSeq       int64
	WindowStartedAt *time.Time
	WindowTruncated bool
	Truncated       bool
	TruncatedSince  *time.Time
	LastRepeatAt    *time.Time
	LastContent     []byte
	LastContentAt   *time.Time
	LastSnapshotAt  time.Time
	LastClockMs     int64
}

// presence is an active presence (listed or missed) of an Alert in the Snapshot's groupKey.
type presence struct {
	AlertID          int64
	State            string
	LastListedWindow int64
	MissedSince      *time.Time
	// ActiveElsewhere is an active presence of the same Alert in another groupKey.
	ActiveElsewhere bool
}

// timing places a Snapshot received before the current window of its groupKey started. One received more than the
// duplicate window before is late, as in a replay: it applies only explicit resolves and changes no presence, window
// or learned interval. One received less than that before is early: a request that committed after a later one of
// the same window (design/db/schema.md §5), whose Alerts count as listed in the current window; it opens no window,
// learns nothing and decides no absence.
func (e *engine) timing() (late, early bool) {
	start := e.group.WindowStartedAt
	if start == nil || !e.in.ReceivedAt.Before(*start) {
		return false, false
	}
	if start.Sub(e.in.ReceivedAt) < e.in.DuplicateWindow {
		return false, true
	}
	return true, false
}

// window puts the Snapshot into the duplicate window of its groupKey (C-06.FR-5): it opens a new window unless it
// arrived within the Integration's duplicate window of the current one's start. It returns whether it opened one, and
// when the previous one started.
func (e *engine) window() (bool, *time.Time) {
	g, t := e.group, e.in.ReceivedAt
	truncated := e.in.Payload.TruncatedAlerts > 0
	g.LastSnapshotAt, g.LastClockMs = t, e.in.ClockMs
	if g.WindowStartedAt != nil && t.Sub(*g.WindowStartedAt) < e.in.DuplicateWindow {
		g.WindowTruncated = g.WindowTruncated || truncated
		return false, nil
	}
	previous := g.WindowStartedAt
	g.WindowSeq++
	g.WindowStartedAt, g.WindowTruncated = &t, truncated
	return true, previous
}

// markListed makes the presence of a firing Alert listed in the current window; Alerts created by this Snapshot get
// theirs once they have an id.
func (e *engine) markListed(a *alert) {
	if e.late || e.listedSet[a.Fingerprint] {
		return
	}
	e.listedSet[a.Fingerprint] = true
	e.listedAlerts = append(e.listedAlerts, a)
	if a.ID == 0 {
		return
	}
	p := e.presences[a.ID]
	if p == nil {
		p = &presence{AlertID: a.ID}
		e.presences[a.ID] = p
	}
	p.State, p.LastListedWindow, p.MissedSince = PresenceListed, e.group.WindowSeq, nil
}

// absence decides the absence of the firing Alerts the current window has not listed (C-06.FR-5, FR-7): a listed one
// becomes missed since the window's start, and one missed since an earlier window becomes Gone once the Snapshot
// arrived processing.gone_min_absence after it was first missed. An Alert Gone here and active in no other groupKey
// resolves as Gone. A truncated Snapshot or window proves no absence (C-06.FR-6).
func (e *engine) absence() {
	g, t := e.group, e.in.ReceivedAt
	if e.in.Payload.TruncatedAlerts > 0 || g.WindowTruncated {
		return
	}
	start := *g.WindowStartedAt
	for _, p := range e.sortedPresences() {
		a := e.byID[p.AlertID]
		if p.LastListedWindow >= g.WindowSeq || a == nil || a.Status != StatusFiring {
			continue
		}
		switch {
		case p.State == PresenceListed:
			p.State, p.MissedSince = PresenceMissed, &start
			e.missed = append(e.missed, p.AlertID)
		case p.State == PresenceMissed && p.MissedSince.Before(start) && t.Sub(*p.MissedSince) >= GoneMinAbsence:
			p.State, p.MissedSince = PresenceGone, nil
			e.gone = append(e.gone, p.AlertID)
			if !p.ActiveElsewhere {
				e.resolve(a, ResolveGone, GoneReasonText, nil)
			}
		}
	}
}

// sortedPresences are the presences in the order of their Alerts' ids, so that processing is deterministic.
func (e *engine) sortedPresences() []*presence {
	out := make([]*presence, 0, len(e.presences))
	for _, p := range e.presences {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b *presence) int { return cmp.Compare(a.AlertID, b.AlertID) })
	return out
}

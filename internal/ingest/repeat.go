// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"slices"
	"time"
)

// The repeat settings of defaults.md.
const (
	// RepeatSamples is processing.repeat_samples: the learned repeat interval is the median of the last 9 gaps.
	RepeatSamples = 9
	// StaleAfterFactor is processing.stale_after_factor and StaleAfterUnlearned processing.stale_after_unlearned.
	StaleAfterFactor    = 3
	StaleAfterUnlearned = 25 * time.Hour
)

// route is an alertmanager_routes row as processing reads and writes it; observed are the gaps a Snapshot learned,
// which the end of its transaction adds to the ring as the row is then.
type route struct {
	ID           int64
	Gaps         []int64
	Learned      *int64
	Observations int64
	changed      bool
	observed     []int64
}

// AlertmanagerRoutePath is the Alertmanager route of a groupKey: the part before the group labels, `{}/{team="db"}` of
// `{}/{team="db"}:{alertname="DiskFull"}`. A colon inside a quoted value or inside braces does not count; a groupKey
// without a separating colon is its own route path.
func AlertmanagerRoutePath(groupKey string) string {
	last, depth, quoted := -1, 0, false
	for i := 0; i < len(groupKey); i++ {
		c := groupKey[i]
		switch {
		case quoted && c == '\\':
			i++
		case c == '"':
			quoted = !quoted
		case quoted:
		case c == '{':
			depth++
		case c == '}':
			depth = max(depth-1, 0)
		case c == ':' && depth == 0:
			last = i
		}
	}
	if last < 0 {
		return groupKey
	}
	return groupKey[:last]
}

// contentSum hashes the fingerprints and statuses of a Snapshot, in order of fingerprint, for the fallback of
// identical Snapshots.
func contentSum(alerts []PayloadAlert) []byte {
	lines := make([]string, len(alerts))
	for i, a := range alerts {
		lines[i] = a.Fingerprint + "\x00" + a.Status + "\n"
	}
	slices.Sort(lines)
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
	}
	return h.Sum(nil)
}

// learn records a repeat when the Snapshot opened a window after an earlier one (C-06.FR-8): when its
// notification_reason is repeat interval elapsed, or, without the field, when its content is that of the previous
// window. The gap since the previous window enters the route's ring. Every window opener's content is kept for the
// next comparison.
func (e *engine) learn(opened bool, previous *time.Time) {
	if !opened {
		return
	}
	g, p, t := e.group, e.in.Payload, e.in.ReceivedAt
	sum := contentSum(p.Alerts)
	if previous != nil {
		repeat := p.HasReason && p.Reason == ReasonRepeat
		if !p.HasReason {
			repeat = bytes.Equal(sum, g.LastContent)
		}
		if gap := t.Sub(*previous).Milliseconds(); repeat && gap > 0 {
			e.route.add(gap)
			g.LastRepeatAt = &t
		}
	}
	g.LastContent, g.LastContentAt = sum, &t
}

// add puts a gap into the ring of the last RepeatSamples gaps and learns their median.
func (r *route) add(gapMs int64) {
	r.observed = append(r.observed, gapMs)
	r.Gaps = append(r.Gaps, gapMs)
	if len(r.Gaps) > RepeatSamples {
		r.Gaps = slices.Clone(r.Gaps[len(r.Gaps)-RepeatSamples:])
	}
	r.Observations++
	m := median(r.Gaps)
	r.Learned = &m
	r.changed = true
}

// median is the middle of the values, or the mean of the two middle ones.
func median(values []int64) int64 {
	s := slices.Clone(values)
	slices.SortFunc(s, cmp.Compare)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// StaleAfter is how long an Alert of an Alertmanager route may go unseen before it is Stale (C-06.FR-8):
// StaleAfterFactor times the learned repeat interval, or StaleAfterUnlearned before one is learned.
func StaleAfter(learned *time.Duration) time.Duration {
	if learned == nil {
		return StaleAfterUnlearned
	}
	return StaleAfterFactor * *learned
}

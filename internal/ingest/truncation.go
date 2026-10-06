// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

// truncation marks the groupKey truncated from a Snapshot with truncatedAlerts > 0 until its next untruncated
// Snapshot (C-06.FR-6); the window of a truncated Snapshot is marked by the window itself. The end of truncation after
// stale_after without Snapshots is the Stale scan's (S-023), which reads last_snapshot_clock_ms.
func (e *engine) truncation() {
	g, t := e.group, e.in.ReceivedAt
	if e.in.Payload.TruncatedAlerts > 0 {
		if !g.Truncated {
			g.Truncated, g.TruncatedSince = true, &t
		}
		return
	}
	g.Truncated, g.TruncatedSince = false, nil
}

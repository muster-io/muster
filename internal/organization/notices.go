// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package organization

import "time"

// NoticeKind is the kind of an Organization-wide notice, a SystemNotice kind of the API (C-02.FR-24).
type NoticeKind string

const (
	// NoticeRecoveringAfterDowntime is active during the recovery window after downtime (C-02.FR-12).
	NoticeRecoveringAfterDowntime NoticeKind = "recovering_after_downtime"
	// NoticeNoReplicaLeading is active while the Leader's alive mark is older than leader.absence_notice.
	NoticeNoReplicaLeading NoticeKind = "no_replica_leading"
)

// NoticeAudience says who sees a notice.
type NoticeAudience string

const (
	AudienceAll    NoticeAudience = "all"
	AudienceAdmins NoticeAudience = "admins"
)

// Notice is an active Organization-wide notice. A zero Since or Until is unknown or open.
type Notice struct {
	Kind     NoticeKind
	Audience NoticeAudience
	Since    time.Time
	Until    time.Time
}

// RuntimeState is what the notices are derived from: the Leader's alive mark, the end of the recovery window and the
// end of the latest downtime; a zero time is absent.
type RuntimeState struct {
	AliveAt       time.Time
	RecoveryUntil time.Time
	DowntimeEnd   time.Time
}

// ActiveNotices returns the notices active at now, a business time: "recovering after downtime" for everyone, from
// the end of the downtime until the end of the recovery window, and "no replica is leading" for Admins, since the
// last alive mark, while that mark is older than absence (leader.absence_notice) or missing.
func ActiveNotices(st RuntimeState, now time.Time, absence time.Duration) []Notice {
	notices := []Notice{}
	if now.Before(st.RecoveryUntil) {
		notices = append(notices, Notice{Kind: NoticeRecoveringAfterDowntime, Audience: AudienceAll,
			Since: st.DowntimeEnd, Until: st.RecoveryUntil})
	}
	if st.AliveAt.IsZero() || now.Sub(st.AliveAt) > absence {
		notices = append(notices, Notice{Kind: NoticeNoReplicaLeading, Audience: AudienceAdmins, Since: st.AliveAt})
	}
	return notices
}

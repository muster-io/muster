// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package organization

import (
	"slices"
	"testing"
	"time"
)

func TestActiveNotices(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	absence := 2 * time.Minute
	recovering := Notice{Kind: NoticeRecoveringAfterDowntime, Audience: AudienceAll,
		Since: now.Add(-5 * time.Minute), Until: now.Add(10 * time.Minute)}
	for _, tt := range []struct {
		name string
		st   RuntimeState
		want []Notice
	}{
		{name: "a Leader marks alive", st: RuntimeState{AliveAt: now.Add(-30 * time.Second)}, want: []Notice{}},
		{name: "the mark is exactly as old as the absence notice",
			st: RuntimeState{AliveAt: now.Add(-absence)}, want: []Notice{}},
		{name: "no replica is leading", st: RuntimeState{AliveAt: now.Add(-3 * time.Minute)},
			want: []Notice{{Kind: NoticeNoReplicaLeading, Audience: AudienceAdmins, Since: now.Add(-3 * time.Minute)}}},
		{name: "never led", st: RuntimeState{},
			want: []Notice{{Kind: NoticeNoReplicaLeading, Audience: AudienceAdmins}}},
		{name: "recovering after downtime", st: RuntimeState{AliveAt: now, RecoveryUntil: recovering.Until,
			DowntimeEnd: recovering.Since}, want: []Notice{recovering}},
		{name: "the recovery window ended", st: RuntimeState{AliveAt: now, RecoveryUntil: now,
			DowntimeEnd: now.Add(-15 * time.Minute)}, want: []Notice{}},
		{name: "both", st: RuntimeState{AliveAt: now.Add(-10 * time.Minute), RecoveryUntil: recovering.Until,
			DowntimeEnd: recovering.Since}, want: []Notice{recovering,
			{Kind: NoticeNoReplicaLeading, Audience: AudienceAdmins, Since: now.Add(-10 * time.Minute)}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ActiveNotices(tt.st, now, absence); !slices.Equal(got, tt.want) {
				t.Errorf("ActiveNotices = %+v, want %+v", got, tt.want)
			}
		})
	}
}

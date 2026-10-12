// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package timers

import (
	"math"
	"time"
)

// AckTimeoutNotices is the most notices one ack timeout sends; the last makes the Alert Group Unclaimed (C-17.FR-2,
// FR-3).
const AckTimeoutNotices = 3

// AutoUnacknowledgeAfter is timers.auto_unacknowledge_after: the unanswered Reminders in a row after which the next
// one auto-unacknowledges instead, where the Route has auto-unacknowledge on (C-17.FR-5).
const AutoUnacknowledgeAfter = 2

// AckNoticeDue is when notice n (1 to AckTimeoutNotices) of an ack timeout started at start is due, with the first
// interval first: start + (2ⁿ − 1)·first, that is first, 3·first and 7·first after the start.
func AckNoticeDue(start time.Time, first time.Duration, n int64) time.Time {
	return start.Add(time.Duration(1<<n-1) * first)
}

// AckNoticeGap is the interval before notice n: 2ⁿ⁻¹·first, that is first, 2·first and 4·first.
func AckNoticeGap(first time.Duration, n int64) time.Duration {
	return time.Duration(1<<(max(n, 1)-1)) * first
}

// DueAckNotices is how many notices of an ack timeout started at start, after the sent ones, are due at now: those
// whose time came, at most the notices left.
func DueAckNotices(start time.Time, first time.Duration, sent int64, now time.Time) int64 {
	var due int64
	for n := sent + 1; n <= AckTimeoutNotices; n++ {
		if AckNoticeDue(start, first, n).After(now) {
			break
		}
		due++
	}
	return due
}

// ReminderInterval is the interval before Reminder k (1, 2, …) after the one before it, or after the acknowledgement
// for the first: min(first·2ᵏ⁻¹, limit), limit being the cap of route.reminders. It stops doubling once the cap is
// reached, so a large k never overflows; a k below 1 is the first.
func ReminderInterval(k int64, first, limit time.Duration) time.Duration {
	d := first
	for i := int64(1); i < k && d < limit; i++ {
		if d > math.MaxInt64/2 {
			return limit
		}
		d *= 2
	}
	return min(d, limit)
}

// DueReminders counts the Reminders due at now when Reminder next is due at deadline: deadline itself, then each one
// interval of the following step after the one before, as long as it is not after now. It returns the count, zero
// when deadline is after now, and when the last of them was due. Once the interval reaches the cap the rest are
// counted at once, so that days of downtime are not walked one step at a time.
func DueReminders(deadline time.Time, next int64, first, limit time.Duration, now time.Time) (int64, time.Time) {
	if deadline.After(now) {
		return 0, time.Time{}
	}
	if limit = max(limit, first); limit <= 0 {
		return 1, deadline
	}
	count, last := int64(1), deadline
	for k := next + 1; ; k++ {
		gap := ReminderInterval(k, first, limit)
		if gap >= limit {
			n := int64(now.Sub(last) / limit)
			return count + n, last.Add(time.Duration(n) * limit)
		}
		if last.Add(gap).After(now) {
			return count, last
		}
		count, last = count+1, last.Add(gap)
	}
}

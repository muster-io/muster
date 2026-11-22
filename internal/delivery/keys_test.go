// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"testing"
)

// TestRepliesWaitForTheirRootMessage: the attempt of a Thread reply waits on the key of its delivery's Root message,
// the key of the delivery's own attempts, so that a round prepares a reply only once an edit or a republication of its
// Root message in flight ended — the reply goes under the Root message that exists then, and its preparation never
// meets that Root message's record (the post's lock, DropPendingReplies) inside one replica — and the next reply of a
// delivery only once the one before it recorded its `thread_ref`; the Root messages of other deliveries do not wait for
// each other. Keyed by its own id, a reply would be prepared beside them.
func TestRepliesWaitForTheirRootMessage(t *testing.T) {
	e := newEnv(t)
	at, root := e.business.Now(), "m1"
	for _, id := range []int64{501, 502} {
		e.db.deliveries = append(e.db.deliveries, &fakeDelivery{id: id, dest: destMM, group: groupID, state: "pending",
			next: at, publishedAt: &at, messageID: &root})
	}
	e.db.replies = append(e.db.replies,
		&fakeReply{id: 601, delivery: 501, group: groupID, dest: destMM, state: "pending", next: at},
		&fakeReply{id: 602, delivery: 502, group: groupID, dest: destMM, state: "collecting", next: at})
	deliveries, replies, err := e.w.AttemptKeys(t.Context(), orgID)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 2 || len(replies) != 2 || replies[601] != deliveries[501] || replies[602] != deliveries[502] ||
		deliveries[501] == deliveries[502] {
		t.Fatalf("deliveries %v, replies %v", deliveries, replies)
	}
}

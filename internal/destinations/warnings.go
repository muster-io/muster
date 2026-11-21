// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package destinations

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/muster-io/muster/internal/destinations/dbgen"
	"github.com/muster-io/muster/internal/mentions"
)

// MentionWarning is a warning of a Destination about a person and a Mention entry whose latest "Mention not
// delivered" is not followed by a delivered Mention of that person through that entry (C-28.FR-3): the person, the
// entry as it is on the Destination now, the messenger — the Destination's type — the reason of the latest event and
// the time of the first one since the person's last delivered Mention through the entry. Through a bridge whose
// Mention line left out a group, a Team mention or everyone in the chat for length (C-12.FR-11), the warning is about
// that Mention and has no person.
type MentionWarning struct {
	User      mentions.UserRef
	Mention   mentions.Mention
	Entry     mentions.Entry
	Messenger string
	Reason    string
	Since     time.Time
}

// withWarnings sets the Mention warnings of each Destination, computed from the delivery events still kept, never
// stored, in one query for them all. A warning names the entry by its target, which identifies an entry across saves:
// one whose entry was removed since is dropped, and one of a deleted User is not read. An outgoing webhook, which
// takes every person as data, has none. Without a business clock, as a reader without the changes has, none.
func (s *Service) withWarnings(ctx context.Context, list []Destination) error {
	ids := make([]int64, 0, len(list))
	for i := range list {
		list[i].MentionWarnings = []MentionWarning{}
		if list[i].Type != TypeWebhook {
			ids = append(ids, list[i].ID)
		}
	}
	if len(ids) == 0 || s.writer.Business == nil {
		return nil
	}
	rows, err := s.store.ListMentionWarnings(ctx, dbgen.ListMentionWarningsParams{OrgID: s.orgID,
		Now: s.writer.Business.Now().UTC(), DestinationIds: ids})
	if err != nil {
		return fmt.Errorf("read the mention warnings of the destinations: %w", err)
	}
	by := map[int64][]dbgen.ListMentionWarningsRow{}
	for _, r := range rows {
		by[r.DestinationID] = append(by[r.DestinationID], r)
	}
	for i := range list {
		d := &list[i]
		for _, r := range by[d.ID] {
			var target mentions.EntryTarget
			if err := json.Unmarshal(r.Target, &target); err != nil {
				return fmt.Errorf("read the target of a mention warning of %s: %w", d.PublicID, err)
			}
			at := slices.IndexFunc(d.MentionEntries, func(e mentions.Entry) bool { return e.Target == target })
			if at < 0 {
				continue
			}
			var mention mentions.Mention
			if len(r.Mention) > 0 {
				if err := json.Unmarshal(r.Mention, &mention); err != nil {
					return fmt.Errorf("read the mention of a mention warning of %s: %w", d.PublicID, err)
				}
			}
			entry := d.MentionEntries[at]
			entry.Position = at
			d.MentionWarnings = append(d.MentionWarnings, MentionWarning{
				User:    mentions.UserRef{ID: r.UserID, PublicID: r.UserPublicID, Name: r.UserName, Login: r.UserLogin},
				Mention: mention, Entry: entry, Messenger: d.Type, Reason: r.Reason, Since: r.Since.UTC()})
		}
		name := func(w MentionWarning) string { return strings.ToLower(cmp.Or(w.User.Name, w.Mention.Name)) }
		slices.SortStableFunc(d.MentionWarnings, func(a, b MentionWarning) int {
			return cmp.Or(cmp.Compare(a.Entry.Position, b.Entry.Position), strings.Compare(name(a), name(b)),
				cmp.Compare(a.User.ID, b.User.ID))
		})
	}
	return nil
}

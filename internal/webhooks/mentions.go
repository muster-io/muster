// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package webhooks

import "github.com/muster-io/muster/internal/mentions"

// MentionTarget is a resolved Mention as data (WebhookMentionTarget, C-15.FR-11): Muster does not know the markup of
// the receiving endpoint, so everyone has only its type, a group its name, and a User their public_id, display name
// and login.
type MentionTarget struct {
	Type  string  `json:"type"`
	ID    *string `json:"id,omitempty"`
	Name  *string `json:"name,omitempty"`
	Login *string `json:"login,omitempty"`
}

// MentionTargets are the targets of a Loud event as the body carries them, in the order Resolve gave them; none is
// the empty list, never null.
func MentionTargets(ts []mentions.Target) []MentionTarget {
	out := make([]MentionTarget, 0, len(ts))
	for _, t := range ts {
		switch t.Kind {
		case mentions.TargetEveryone:
			out = append(out, MentionTarget{Type: mentions.TargetEveryone})
		case mentions.TargetGroup:
			name := t.Group
			out = append(out, MentionTarget{Type: mentions.TargetGroup, Name: &name})
		case mentions.TargetUser:
			if t.User == nil {
				continue
			}
			id, name, login := t.User.PublicID, t.User.Name, t.User.Login
			out = append(out, MentionTarget{Type: mentions.TargetUser, ID: &id, Name: &name, Login: &login})
		}
	}
	return out
}

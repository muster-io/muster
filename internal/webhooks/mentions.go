// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package webhooks

import "github.com/muster-io/muster/internal/mentions"

// MentionTarget is a resolved Mention as data (WebhookMentionTarget, C-15.FR-11, revised for L2): Muster does not know
// the markup of the receiving endpoint, so everyone has only its type, a group its name, a User — the On-call of a Team
// among them — their public_id, display name and login, and a Team its slug and name.
type MentionTarget struct {
	Type  string  `json:"type"`
	ID    *string `json:"id,omitempty"`
	Name  *string `json:"name,omitempty"`
	Login *string `json:"login,omitempty"`
	Slug  *string `json:"slug,omitempty"`
}

// MentionTargets are the targets of a Loud event as the body carries them, in the order Resolve gave them, each once
// — two mentions of everyone are one target of the type everyone; none is the empty list, never null.
func MentionTargets(ts []mentions.Target) []MentionTarget {
	out := make([]MentionTarget, 0, len(ts))
	everyone := false
	for _, t := range ts {
		if t.Kind == mentions.TargetEveryone {
			if everyone {
				continue
			}
			everyone = true
		}
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
		case mentions.TargetTeam:
			if t.Team == nil {
				continue
			}
			slug, name := t.Team.Slug, t.Team.Name
			out = append(out, MentionTarget{Type: mentions.TargetTeam, Name: &name, Slug: &slug})
		}
	}
	return out
}

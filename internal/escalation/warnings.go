// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package escalation

import (
	"cmp"
	"slices"

	"github.com/muster-io/muster/internal/mentions"
)

// The kinds of a chain's warning (C-32.FR-17): a page_team step whose Team mention or word for everyone the
// Connection of one of the chain's Destinations cannot mention, a call step whose gateway is no Notification gateway
// any more, a webhook step whose Destination was deleted, a page_team step whose Team was deleted, and a chain none of
// whose steps Pages anyone — no page_on_call, page_people, page_team or call step — which is the only warning of the
// chain as a whole, without a step.
const (
	WarningEveryoneUnsupported = "everyone_unsupported"
	WarningGatewayDeleted      = "gateway_deleted"
	WarningDestinationDeleted  = "destination_deleted"
	WarningTeamDeleted         = "team_deleted"
	WarningPagesNobody         = "pages_nobody"
)

// Warning is what a step of a chain cannot do: its kind, the step's number — 0 for pages_nobody, which concerns the
// chain as a whole — the Destination it concerns — the one that cannot mention, or the deleted one — and, for
// everyone_unsupported, the reason of mentions.Can (MentionUnavailableReason).
type Warning struct {
	Kind        string
	Step        int
	Destination *NamedRef
	Reason      string
}

// NamedRef is an object by public_id and name.
type NamedRef struct {
	PublicID string
	Name     string
}

// connectionAbilities are what a Destination's Connection can mention, as internal/mentions reads them (C-28.FR-2).
type connectionAbilities struct {
	UsergroupsRead bool
	GroupMentions  bool
	Messenger      bool
	Everyone       []string
}

// warningsOf are the warnings of the chain c: pages_nobody first when no step Pages anyone, then by step: for each
// page_team step, its Team when it was deleted — the one it names, else the chain's owning Team — and otherwise each
// Destination of the chain whose Connection cannot mention its Team mention or word for everyone now; a call step whose gateway is
// not live; a webhook step whose Destination is deleted.
func warningsOf(c Chain) []Warning {
	out := []Warning{}
	if !slices.ContainsFunc(c.Steps, func(st Step) bool {
		return st.Kind == KindPageOnCall || st.Kind == KindPagePeople || st.Kind == KindPageTeam || st.Kind == KindCall
	}) {
		out = append(out, Warning{Kind: WarningPagesNobody})
	}
	for _, st := range c.Steps {
		switch st.Kind {
		case KindPageTeam:
			// A step whose Team was deleted Pages nobody, whatever its Destinations can mention.
			if team := cmp.Or(st.Team, c.OwningTeam); st.Everyone == "" && team != nil && team.Deleted {
				out = append(out, Warning{Kind: WarningTeamDeleted, Step: st.Number})
				continue
			}
			target := mentions.EntryTarget{Kind: mentions.TargetTeam}
			if st.Everyone != "" {
				target = mentions.EntryTarget{Kind: mentions.TargetEveryone, Everyone: st.Everyone}
			}
			for _, d := range c.Destinations {
				conn := mentions.Connection{Type: d.Type, UsergroupsRead: d.connection.UsergroupsRead,
					GroupMentions: d.connection.GroupMentions, Messenger: d.connection.Messenger,
					Everyone: d.connection.Everyone}
				if ok, reason := mentions.Can(d.Type, conn, target); !ok {
					out = append(out, Warning{Kind: WarningEveryoneUnsupported, Step: st.Number,
						Destination: &NamedRef{PublicID: d.PublicID, Name: d.Name}, Reason: reason})
				}
			}
		case KindCall:
			if st.Gateway != nil && !st.Gateway.Live {
				out = append(out, Warning{Kind: WarningGatewayDeleted, Step: st.Number})
			}
		case KindWebhook:
			if d := st.Destination; d != nil && d.Deleted {
				out = append(out, Warning{Kind: WarningDestinationDeleted, Step: st.Number,
					Destination: &NamedRef{PublicID: d.PublicID, Name: d.Name}})
			}
		}
	}
	return out
}

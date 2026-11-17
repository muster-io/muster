// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package messages

import (
	"context"
	"strconv"
	"strings"
)

// The text of a Page by Mention (C-32.FR-9, C-12.FR-3 revised for L2), in the language of the Route: "Step {n}:
// {Mentions}, you are needed" as the Mention line when the reply mentions anyone, a line "Page not delivered: @{login}
// has no linked {messenger} account" for each person without an Account link in the Identity space of the
// Destination's Connection, and "Chain of the Route {Route}" or "Chain of the Team {Team}", then what the step Pages —
// "On-call of {Team} {Schedule}", the people, the Team or everyone in the chat — and "normal" or "important", with
// "again in {N} min" when the person's rule repeats and "again" for a later Mention of the rule.

// EventPaged is the lifecycle event of a Page by Mention.
const EventPaged = "paged"

// The sources of the chain of a Page in a Destination: its Route's, or a Team's.
const (
	PageSourceRoute = "route"
	PageSourceTeam  = "team"
)

// PageInput is a Thread reply of a Page to render: the step, whether the reply mentions anyone, the logins of the
// people without an Account link in the Destination's Identity space and the messenger's name, the source of the
// chain in this Destination and the Team of "Chain of the Team", what the step Pages — the On-call of a Schedule of a
// Team, people by login, a Team by name or everyone in the chat — its importance, the wait until the rule's next
// Mention, 0 for none, and whether it is a later Mention.
type PageInput struct {
	Step         int
	Mentioned    bool
	Unlinked     []string
	Messenger    string
	Source       string
	ChainTeam    string
	OnCallTeam   string
	Schedule     string
	People       []string
	Team         string
	Everyone     bool
	Importance   string
	AgainSeconds int
	Again        bool
}

// messengerNames are the messengers as the line of an unlinked person names them.
var messengerNames = map[string]string{"mattermost": "Mattermost", "slack": "Slack", "telegram": "Telegram",
	"bridge": "Bridge"}

// Page renders the Thread reply of a Page of the Alert Group groupID, reading through db.
func (r *Renderer) Page(ctx context.Context, db DBTX, groupID int64, in PageInput) (Message, error) {
	src, err := r.loadGroup(ctx, db, groupID)
	if err != nil {
		return Message{}, err
	}
	return PageMessage(src.Route.Language, src.Route.Name, src.TimeZone, src.Status, in), nil
}

// PageMessage is the Thread reply of a Page in lang, for the Route route, of an Alert Group of the colour.
func PageMessage(lang, route, timeZone, colour string, in PageInput) Message {
	lang = language(lang)
	m := Message{Kind: KindReply, Language: lang, TimeZone: timeZone, Colour: colour, Buttons: []Button{}}
	if in.Mentioned {
		m.Lead = &Lead{Before: T(lang, "page.leadBefore", Args{"step": strconv.Itoa(in.Step)}),
			After: T(lang, "page.leadAfter", nil)}
	}
	messenger := messengerNames[in.Messenger]
	if messenger == "" {
		messenger = in.Messenger
	}
	for _, login := range in.Unlinked {
		m.Lines = append(m.Lines, T(lang, "page.notDelivered", Args{"login": Value("@" + login), "messenger": messenger}))
	}
	parts := []string{T(lang, "page.chainOfRoute", Args{"route": Value(route)})}
	if in.Source == PageSourceTeam && in.ChainTeam != "" {
		parts[0] = T(lang, "page.chainOfTeam", Args{"team": Value(in.ChainTeam)})
	}
	switch {
	case in.Schedule != "":
		parts = append(parts, T(lang, "page.onCall", Args{"team": Value(in.OnCallTeam), "schedule": Value(in.Schedule)}))
	case in.Team != "":
		parts = append(parts, T(lang, "page.team", Args{"team": Value(in.Team)}))
	case in.Everyone:
		parts = append(parts, T(lang, "page.everyone", nil))
	case len(in.People) > 0:
		logins := make([]string, len(in.People))
		for i, p := range in.People {
			logins[i] = Value("@" + p)
		}
		parts = append(parts, strings.Join(logins, ", "))
	}
	if in.Importance != "" {
		imp := T(lang, "page."+in.Importance, nil)
		switch {
		case in.Again:
			imp = T(lang, "page.again", Args{"importance": imp})
		case in.AgainSeconds > 0:
			imp = T(lang, "page.againIn", Args{"importance": imp,
				"minutes": strconv.Itoa(max(1, (in.AgainSeconds+59)/60))})
		}
		parts = append(parts, imp)
	}
	m.Lines = append(m.Lines, strings.Join(parts, " · "))
	return m
}

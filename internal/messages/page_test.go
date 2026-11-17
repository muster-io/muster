// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package messages

import (
	"strings"
	"testing"
)

// TestPageMessage is C-32.FR-9 and C-12.FR-3 (revised for L2): the reply of a Page leads with "Step {n}: {Mentions},
// you are needed" when it mentions anyone, names each person without an Account link in the Destination's Identity
// space, and says which chain it comes from, what the step Pages and how important it is, with "again in {N} min"
// when the rule repeats and "again" for a later Mention, in English and in Russian.
func TestPageMessage(t *testing.T) {
	onCall := PageInput{Step: 1, Mentioned: true, Messenger: "mattermost", Source: PageSourceRoute,
		OnCallTeam: "devops", Schedule: "Main", Importance: "normal"}
	unlinked := PageInput{Step: 1, Unlinked: []string{"petr"}, Messenger: "mattermost", Source: PageSourceTeam,
		ChainTeam: "payments", OnCallTeam: "payments", Schedule: "Main", Importance: "normal"}
	important := onCall
	important.Importance, important.AgainSeconds = "important", 300
	again := onCall
	again.Importance, again.Again = "important", true
	for _, c := range []struct {
		name, lang string
		in         PageInput
		lead       string
		text       string
	}{
		{"on-call", "en", onCall, "Step 1: |, you are needed",
			"Chain of the Route Infrastructure · On-call of devops Main · normal"},
		{"unlinked", "en", unlinked, "",
			"Page not delivered: @petr has no linked Mattermost account\n" +
				"Chain of the Team payments · On-call of payments Main · normal"},
		{"repeats", "en", important, "Step 1: |, you are needed",
			"Chain of the Route Infrastructure · On-call of devops Main · important, again in 5 min"},
		{"later", "en", again, "Step 1: |, you are needed",
			"Chain of the Route Infrastructure · On-call of devops Main · important, again"},
		{"people", "en", PageInput{Step: 2, Mentioned: true, Messenger: "slack", People: []string{"ivan", "petr"},
			Importance: "important", AgainSeconds: 61}, "Step 2: |, you are needed",
			"Chain of the Route Infrastructure · @ivan, @petr · important, again in 2 min"},
		{"team", "en", PageInput{Step: 6, Mentioned: true, Team: "payments", Source: PageSourceTeam},
			"Step 6: |, you are needed", "Chain of the Route Infrastructure · Team payments"},
		{"everyone", "en", PageInput{Step: 6, Mentioned: true, Everyone: true, Messenger: "telegram"},
			"Step 6: |, you are needed", "Chain of the Route Infrastructure · everyone in the chat"},
		{"a messenger without a name", "en", PageInput{Step: 1, Unlinked: []string{"petr"}, Messenger: "other"}, "",
			"Page not delivered: @petr has no linked other account\nChain of the Route Infrastructure"},
		{"russian", "ru", important, "Шаг 1: |, вы нужны",
			"Цепочка маршрута Infrastructure · Дежурный devops Main · важно, снова через 5 мин"},
		{"russian unlinked", "ru", unlinked, "",
			"Призыв не доставлен: у @petr нет связанного аккаунта Mattermost\n" +
				"Цепочка команды payments · Дежурный payments Main · обычно"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := PageMessage(c.lang, "Infrastructure", "UTC", ColourFiring, c.in)
			lead := ""
			if m.Lead != nil {
				lead = m.Lead.Before + "|" + m.Lead.After
			}
			if text := strings.ReplaceAll(m.Text(), zeroWidthSpace, ""); lead != c.lead || text != c.text ||
				m.Kind != KindReply || len(m.Buttons) != 0 {
				t.Errorf("lead %q text %q", lead, text)
			}
		})
	}
}

// TestMentionLine: the Mentions of a message stand alone on their line, or in its Lead, escaped; without Mentions
// there is no line.
func TestMentionLine(t *testing.T) {
	m := Message{}
	if got := m.MentionLine([]string{"@a", "@b"}, " ", nil); got != "@a @b" {
		t.Errorf("alone %q", got)
	}
	m.Lead = &Lead{Before: "Step 1: ", After: ", you are *needed*"}
	if got := m.MentionLine([]string{"@a"}, " ", nil); got != "Step 1: @a, you are *needed*" {
		t.Errorf("lead %q", got)
	}
	if got := m.MentionLine([]string{"@a"}, " ", EscapeMarkdown); !strings.HasSuffix(got, `\*needed\*`) {
		t.Errorf("escaped %q", got)
	}
	if got := m.MentionLine(nil, " ", nil); got != "" {
		t.Errorf("no mentions %q", got)
	}
}

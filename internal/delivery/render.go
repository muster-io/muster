// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/muster-io/muster/internal/groups"
)

// Message is a rendered message, neutral of any markup: its sections of text, its buttons and its colour. Each adapter
// turns it into its messenger's markup and escapes it (ADR-0005, output safety).
type Message struct {
	Sections []string `json:"sections"`
	Buttons  []Button `json:"buttons"`
	Colour   string   `json:"colour"`
}

// Button is a Command button of a Root message or a Thread reply: the Command it runs and its label.
type Button struct {
	Command string `json:"command"`
	Label   string `json:"label"`
}

// Text is the plain text of the message, its sections one per line.
func (m Message) Text() string {
	return strings.Join(m.Sections, "\n")
}

// GroupView is an Alert Group as the renderer shows it.
type GroupView struct {
	PublicID string
	Number   int64
	Title    string
	Status   groups.Status
	Urgent   bool
}

// viewOf is the view of an Alert Group the dispatcher changed.
func viewOf(g *groups.Group) GroupView {
	return GroupView{PublicID: g.PublicID, Number: g.Number, Title: g.Title, Status: g.Status, Urgent: g.Urgent}
}

// ReplyView is a Thread reply as the renderer shows it: its lifecycle event, the fingerprints of the new Alerts it
// lists, the language of the Route and how many Alerts it lists at most.
type ReplyView struct {
	Event        groups.Event
	Fingerprints []string
	Language     string
	Listed       int
}

// Renderer renders Root messages and Thread replies (C-12). The minimal renderer stands in until S-036 brings the
// default layout, the built-in texts, templates and buttons.
type Renderer interface {
	Render(g GroupView, d Destination, language string) Message
	RenderReply(r ReplyView, g GroupView, d Destination) Message
}

// MinimalRenderer shows the status, #N and title of an Alert Group with the buttons of its status (reference.md,
// buttons and links by status), and for a Thread reply its event and the fingerprints of its new Alerts, at most
// Listed of them and then "…and K more — open in Muster".
type MinimalRenderer struct{}

// The labels of the minimal renderer per language; English for any other.
var minimalTexts = map[string]map[string]string{
	"en": {
		"firing": "Firing", "acknowledged": "Acknowledged", "snoozed": "Snoozed", "resolved": "Resolved",
		"urgent": "Urgent", "acknowledge": "Ack", "unacknowledge": "Unack", "resolve": "Resolve", "snooze": "Snooze",
		"unsnooze": "Unsnooze", "open": "Open in Muster", "more": "…and %d more — open in Muster",
	},
	"ru": {
		"firing": "Горит", "acknowledged": "Подтверждена", "snoozed": "Отложена", "resolved": "Закрыта",
		"urgent": "Срочная", "acknowledge": "Подтвердить", "unacknowledge": "Снять подтверждение",
		"resolve": "Закрыть", "snooze": "Отложить", "unsnooze": "Отменить откладывание", "open": "Открыть в Muster",
		"more": "…и ещё %d — откройте в Muster",
	},
}

// The colours of the statuses.
var statusColours = map[groups.Status]string{
	groups.StatusFiring: "#d93025", groups.StatusAcknowledged: "#f29900", groups.StatusSnoozed: "#80868b",
	groups.StatusResolved: "#1e8e3e",
}

// The Commands of the buttons of each status; a resolved Root message has none.
var statusButtons = map[groups.Status][]string{
	groups.StatusFiring:       {"acknowledge", "resolve", "snooze"},
	groups.StatusAcknowledged: {"unacknowledge", "resolve", "snooze"},
	groups.StatusSnoozed:      {"acknowledge", "unsnooze", "resolve"},
}

func textsOf(language string) map[string]string {
	if t, ok := minimalTexts[language]; ok {
		return t
	}
	return minimalTexts["en"]
}

// Render is the Root message of g.
func (MinimalRenderer) Render(g GroupView, _ Destination, language string) Message {
	t := textsOf(language)
	status := t[string(g.Status)]
	if g.Urgent {
		status += " · " + t["urgent"]
	}
	m := Message{Sections: []string{fmt.Sprintf("#%d %s", g.Number, g.Title), status},
		Buttons: []Button{}, Colour: statusColours[g.Status]}
	for _, c := range statusButtons[g.Status] {
		m.Buttons = append(m.Buttons, Button{Command: c, Label: t[c]})
	}
	if len(m.Buttons) == 0 {
		m.Sections = append(m.Sections, t["open"])
	}
	return m
}

// RenderReply is the Thread reply r about g.
func (MinimalRenderer) RenderReply(r ReplyView, g GroupView, _ Destination) Message {
	t := textsOf(r.Language)
	m := Message{Sections: []string{fmt.Sprintf("#%d %s", g.Number, r.Event)}, Buttons: []Button{},
		Colour: statusColours[g.Status]}
	listed := r.Fingerprints
	if r.Listed >= 0 && len(listed) > r.Listed {
		listed = listed[:r.Listed]
	}
	m.Sections = append(m.Sections, listed...)
	if more := len(r.Fingerprints) - len(listed); more > 0 {
		m.Sections = append(m.Sections, fmt.Sprintf(t["more"], more))
	}
	return m
}

// encoded is a rendered message as the Desired state stores it: its text, the payload that restores it and the hash of
// what it shows — text, buttons and colour, never Mentions.
type encoded struct {
	text    string
	payload []byte
	hash    []byte
}

func encode(m Message) encoded {
	payload, _ := json.Marshal(m) // a Message is plain strings
	sum := sha256.Sum256(payload)
	return encoded{text: m.Text(), payload: payload, hash: sum[:]}
}

// decode restores the message of a Desired state.
func decode(payload []byte) (Message, error) {
	var m Message
	if err := json.Unmarshal(payload, &m); err != nil {
		return Message{}, fmt.Errorf("read the desired message: %w", err)
	}
	return m, nil
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/messages"
)

// Message is a rendered message, neutral of any markup (C-12.FR-1): each adapter lays it out for its messenger and
// escapes its neutral text (ADR-0005, output safety).
type Message = messages.Message

// Button is a Command button of a Root message or a Thread reply.
type Button = messages.Button

// GroupView is an Alert Group as delivery renders it.
type GroupView struct {
	ID       int64
	PublicID string
	Number   int64
	Title    string
	Status   groups.Status
	Urgent   bool
}

// viewOf is the view of an Alert Group the dispatcher changed.
func viewOf(g *groups.Group) GroupView {
	return GroupView{ID: g.ID, PublicID: g.PublicID, Number: g.Number, Title: g.Title, Status: g.Status,
		Urgent: g.Urgent}
}

// ReplyView is a Thread reply as the renderer shows it: its lifecycle event, the number of the first event it stands
// for, the fingerprints of the new Alerts it lists, the language of the Route and how many Alerts it lists at most.
type ReplyView struct {
	Event        groups.Event
	Seq          int64
	Fingerprints []string
	Language     string
	Listed       int
}

// Roots are the Root messages of one Alert Group, one per markup of its Destinations, as one render produced them:
// the Route templates that failed, whose messages are the Fallback template, and what to run once the transaction
// committed.
type Roots struct {
	Messages map[messages.Markup]messages.Rendered
	Failures []messages.Failure
	After    func(ctx context.Context)
}

// Renderer renders what delivery stores and sends (C-12), declared here by its consumer: Root messages, read through
// the transaction of the change, Thread replies when the worker sends them, and the Storm summary. MessageRenderer is
// the one of the runtime.
type Renderer interface {
	Roots(ctx context.Context, db messages.DBTX, g GroupView, markups []messages.Markup) (Roots, error)
	Reply(ctx context.Context, db messages.DBTX, r ReplyView, g GroupView) (Message, error)
	Storm(routePublicID, routeName, language string, count, urgent int64) Message
	StormOver(routePublicID, language string, open int64) Message
}

// MessageRenderer is the Renderer of messages.Renderer: a Root message is rendered once per markup and the template
// error state of its Route settled in the same transaction (C-12.FR-6).
type MessageRenderer struct {
	*messages.Renderer
}

// Roots renders the Root message of g in each markup.
func (r MessageRenderer) Roots(ctx context.Context, db messages.DBTX, g GroupView, markups []messages.Markup) (Roots,
	error) {
	src, err := r.Load(ctx, db, g.ID)
	if err != nil {
		return Roots{}, err
	}
	out := Roots{Messages: make(map[messages.Markup]messages.Rendered, len(markups))}
	var rendered []string
	failed := map[string]bool{}
	for _, mk := range markups {
		if _, ok := out.Messages[mk]; ok {
			continue
		}
		rd := r.Root(src, mk)
		out.Messages[mk] = rd
		if f := rd.Failure; f != nil && !failed[f.Template] {
			failed[f.Template] = true
			out.Failures = append(out.Failures, *f)
		}
		rendered = append(rendered, rd.Rendered...)
	}
	if out.After, err = r.Settle(ctx, db, src, out.Failures, rendered); err != nil {
		return Roots{}, err
	}
	return out, nil
}

// Reply renders a Thread reply of g.
func (r MessageRenderer) Reply(ctx context.Context, db messages.DBTX, rv ReplyView, g GroupView) (Message, error) {
	return r.Renderer.Reply(ctx, db, g.ID, messages.ReplyInput{Event: string(rv.Event), Seq: rv.Seq,
		Fingerprints: rv.Fingerprints, Listed: rv.Listed})
}

// markupOf is the markup a Destination type writes: Markdown in Mattermost, HTML in Telegram, plain text for an
// outgoing webhook.
func markupOf(destinationType string) messages.Markup {
	switch destinationType {
	case TypeMattermost:
		return messages.MarkupMarkdown
	case TypeTelegram:
		return messages.MarkupHTML
	}
	return messages.MarkupPlain
}

// markupsOf are the markups of Destinations of the types given.
func markupsOf(types []string) []messages.Markup {
	out := make([]messages.Markup, 0, len(types))
	for _, t := range types {
		out = append(out, markupOf(t))
	}
	return out
}

// encoded is a rendered message as the Desired state stores it: its text, the payload that restores it, the hash of
// what it shows — text, buttons and colour, never Mentions — and the key that signed its buttons.
type encoded struct {
	text    string
	payload []byte
	hash    []byte
	keyID   string
}

func encode(m Message) encoded {
	payload, _ := json.Marshal(m) // a Message is plain strings and numbers
	sum := sha256.Sum256(payload)
	return encoded{text: m.Text(), payload: payload, hash: sum[:]}
}

// encodeRoot is a Root message with the key that signed its buttons.
func encodeRoot(rd messages.Rendered) encoded {
	e := encode(rd.Message)
	e.keyID = rd.KeyID
	return e
}

// decode restores the message of a Desired state.
func decode(payload []byte) (Message, error) {
	var m Message
	if err := json.Unmarshal(payload, &m); err != nil {
		return Message{}, fmt.Errorf("read the desired message: %w", err)
	}
	return m, nil
}

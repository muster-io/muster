// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/templates"
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
// committed. Data is the Alert Group as the request templates of outgoing webhooks read it, with alert data as
// received, and Language the language of its Route.
type Roots struct {
	Messages map[messages.Markup]messages.Rendered
	Failures []messages.Failure
	After    func(ctx context.Context)
	Data     *templates.Data
	Language string
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
	data := r.Data(src)
	out.Data, out.Language = &data, src.Route.Language
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

// RequestState is the Desired state of an outgoing webhook in the template mode (C-15.FR-3): what its request templates
// read of an Alert Group (Group, alert data as received) or of a Storm summary (Storm, and no Alert Group), and the
// language of its final edit. Its hash is that of the rendered "update" request (Requests).
type RequestState struct {
	Language string          `json:"language"`
	Group    *templates.Data `json:"group,omitempty"`
	Storm    *StormState     `json:"storm,omitempty"`
}

// StormState is a Storm summary as the request templates of an outgoing webhook read it (`.Storm`): the name of the
// Route, the counts of its new Alert Groups and of the Urgent ones, whether it is the final state after the Storm
// ended, and the link to the Route's Alert Groups in Muster.
type StormState struct {
	Route           string
	AlertGroupCount int64
	UrgentCount     int64
	Final           bool
	URL             string
}

// sendsRequests reports whether a Destination type renders requests rather than messages: an outgoing webhook, whose
// deliveries exist only in the template mode.
func sendsRequests(destinationType string) bool { return destinationType == TypeWebhook }

// encodeRequest is the Desired state st of the outgoing webhook destinationID: the state as its payload and the hash
// of its rendered "update" request; its text is empty.
func (s *Service) encodeRequest(ctx context.Context, db dbgen.DBTX, destinationID int64, st RequestState) (encoded,
	error) {
	payload, err := json.Marshal(st)
	if err != nil {
		return encoded{}, fmt.Errorf("encode the request state: %w", err)
	}
	if s.requests == nil {
		sum := sha256.Sum256(payload)
		return encoded{payload: payload, hash: sum[:]}, nil
	}
	hash, err := s.requests.Desired(ctx, db, destinationID, st)
	if err != nil {
		return encoded{}, err
	}
	return encoded{payload: payload, hash: hash}, nil
}

// desiredOf is the Desired state of an Alert Group rendered as roots in the Destination d, through db: its Root
// message in the markup of d, or for an outgoing webhook its request state.
func (s *Service) desiredOf(ctx context.Context, db dbgen.DBTX, d Destination, roots Roots) (encoded, error) {
	if !sendsRequests(d.Type) {
		return encodeRoot(roots.Messages[markupOf(d.Type)]), nil
	}
	return s.encodeRequest(ctx, db, d.ID, RequestState{Language: roots.Language, Group: roots.Data})
}

// summaryOf is the Desired state of a Storm summary in the Destination destinationID of the type given, through db:
// the message m, or for an outgoing webhook the request state of the Storm st, with the link of m.
func (s *Service) summaryOf(ctx context.Context, db dbgen.DBTX, destinationID int64, destinationType, language string,
	m Message, st StormState) (encoded, error) {
	if !sendsRequests(destinationType) {
		return encode(m), nil
	}
	if len(m.Links) > 0 {
		st.URL = m.Links[0].URL
	}
	return s.encodeRequest(ctx, db, destinationID, RequestState{Language: language, Storm: &st})
}

// responseValues reads the values extracted from the responses of an outgoing webhook, by rule name.
func responseValues(raw []byte) map[string]string {
	out := map[string]string{}
	_ = json.Unmarshal(raw, &out) // deliveries.response_values is a JSON object of strings
	return out
}

// valuesJSON is the JSON object of extracted values, nil for none.
func valuesJSON(values map[string]string) []byte {
	if len(values) == 0 {
		return nil
	}
	b, _ := json.Marshal(values) // strings only
	return b
}

// requestState reads the Desired state of a template-mode request from a delivery's payload.
func requestState(payload []byte) RequestState {
	var st RequestState
	_ = json.Unmarshal(payload, &st) // the payload of a webhook delivery is a RequestState, else empty
	return st
}

// decode restores the message of a Desired state.
func decode(payload []byte) (Message, error) {
	var m Message
	if err := json.Unmarshal(payload, &m); err != nil {
		return Message{}, fmt.Errorf("read the desired message: %w", err)
	}
	return m, nil
}

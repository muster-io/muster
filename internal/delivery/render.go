// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/mentions"
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
// for, the fingerprints of the new Alerts it lists, the language of the Route and how many Alerts it lists at most,
// with the markup of its Destination. A Route template that fails for it is reported to Fallback, and what to run
// once the transaction of the render committed is handed to After.
type ReplyView struct {
	Event        groups.Event
	Seq          int64
	Fingerprints []string
	Language     string
	Listed       int
	Markup       messages.Markup
	After        func(f func(context.Context))
	Fallback     func(detail string)
	// Page is what the reply of a Page by Mention says (C-32.FR-9), nil for another reply.
	Page *messages.PageInput
}

// Roots are the Root messages of one Alert Group, one per markup of its Destinations, as one render produced them:
// the Route templates that failed, whose messages are the Fallback template, and what to run once the transaction
// committed. Data is the Alert Group as the request templates of outgoing webhooks read it, with alert data as
// received, Language the language of its Route and SnoozeSeconds its Route's Snooze durations, which the button set of
// each Destination names (C-28.FR-8).
type Roots struct {
	Messages      map[messages.Markup]messages.Rendered
	Failures      []messages.Failure
	After         func(ctx context.Context)
	Data          *templates.Data
	Language      string
	SnoozeSeconds []int64
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
	out.Data, out.Language, out.SnoozeSeconds = &data, src.Route.Language, src.Route.SnoozeDurations
	return out, nil
}

// Reply renders a Thread reply of g, the reply of a Page from its PageInput.
func (r MessageRenderer) Reply(ctx context.Context, db messages.DBTX, rv ReplyView, g GroupView) (Message, error) {
	if rv.Page != nil {
		return r.Page(ctx, db, g.ID, *rv.Page)
	}
	return r.Renderer.Reply(ctx, db, g.ID, messages.ReplyInput{Event: string(rv.Event), Seq: rv.Seq,
		Fingerprints: rv.Fingerprints, Listed: rv.Listed, Markup: rv.Markup, After: rv.After, Fallback: rv.Fallback})
}

// markupOf is the markup a Destination type writes: Markdown in Mattermost, HTML in Telegram, CommonMark in a
// bridge's envelope body (C-22.FR-4), Slack's mrkdwn (C-24.FR-7), plain text for an outgoing webhook.
func markupOf(destinationType string) messages.Markup {
	switch destinationType {
	case TypeMattermost:
		return messages.MarkupMarkdown
	case TypeTelegram:
		return messages.MarkupHTML
	case TypeBridge:
		return messages.MarkupCommonMark
	case TypeSlack:
		return messages.MarkupMrkdwn
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
// what it shows — text, buttons and colour, never Mentions — and the key that signed its buttons; footer says whether
// a Root message has a footer, without which its stored message, which has the same hash, has none either.
type encoded struct {
	text    string
	payload []byte
	hash    []byte
	keyID   string
	footer  bool
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

// buttonKeyOf is the id of the key that signed the buttons of a Thread reply — a Reminder's — or empty for a reply
// without signed buttons. Thread replies are never edited, so their buttons are never signed again (C-17.FR-6).
func buttonKeyOf(m Message) string {
	for _, b := range m.Buttons {
		if b.KeyID != "" {
			return b.KeyID
		}
	}
	return ""
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
// message in the markup of d with only the buttons of d's button set, or for an outgoing webhook its request state. The
// buttons the set does not show are dropped before the hash, so that a set that changes nothing changes no hash. The
// stored message keeps the footer of every identity space, and the adapter writes the one of the Destination's
// Connection when it sends, so that an edit queued before a change of the Connection, or retried after it, names the
// user as the new Connection's messenger mentions them; the hash covers only the footer of d's identity space as it is
// now, so that an Account link in another space changes no hash, and the first rendering after a change of the
// Connection edits the Root message into the new space's footer when it reads otherwise (C-12.FR-12). A rendering whose
// hash is the stored one may still find the stored message older in the other spaces' footers, or a send since a
// change of the Connection that showed one of them: storedNeeds tells which, and repairHash is the hash of a state due
// again for that reason.
func (s *Service) desiredOf(ctx context.Context, db dbgen.DBTX, d Destination, roots Roots) (encoded, error) {
	if !sendsRequests(d.Type) {
		rd := roots.Messages[markupOf(d.Type)]
		e := encodeShown(rd, d.Buttons, roots.SnoozeSeconds)
		own := rd
		own.Message = rd.Message.OnlyFooterIn(mentions.IdentitySpace(d.Type, d.Connection))
		e.hash = encodeShown(own, d.Buttons, roots.SnoozeSeconds).hash
		e.footer = rd.Message.Footer != "" || len(rd.Message.Footers) > 0
		return e, nil
	}
	return s.encodeRequest(ctx, db, d.ID, RequestState{Language: roots.Language, Group: roots.Data})
}

// storedNeeds is what the stored Desired state of a Root message, stored, needs from a rendering msg whose hash is the
// stored one or its repair hash (sameHash, C-12.FR-12). The hash covers the footer of the identity space the
// Destination had when the state was stored, while the adapter writes the footer of its space at the time of the send,
// so after a change of the Connection a send may have shown the stored footer of the new space, which a link changed or
// removed since may have made wrong. Both messages are compared in the identity space of the Destination as stored read
// it, after the delivery's lock, since a change of the Connection may have committed after the rendering read the
// Destination. When the stored message of a Root message that may have been shown reads otherwise there than msg, the
// delivery is due again (due), with the repair hash of msg's hash for its next version, so that the Root message is
// edited into msg; when only what it keeps for other spaces differs, or the Root message was never shown, msg replaces
// it without a send (refresh), so that a later send writes the footer of its space as it is now; when they are the
// same, nothing. A stored message that may have been shown and cannot be read is due again.
func storedNeeds(stored dbgen.GetDesiredMessageRow, msg encoded) (due, refresh bool) {
	m, err := decode(stored.DesiredPayload)
	switch {
	case err != nil:
		return stored.Shown, !stored.Shown
	case bytes.Equal(encode(m).payload, msg.payload):
		return false, false
	case !stored.Shown:
		return false, true
	}
	fresh, err := decode(msg.payload)
	if err != nil {
		return true, false
	}
	space := mentions.IdentitySpace(stored.DestinationType, int8Of(stored.ConnectionID))
	differs := !bytes.Equal(encode(m.OnlyFooterIn(space)).payload, encode(fresh.OnlyFooterIn(space)).payload)
	return differs, !differs
}

// repairHash is the repair hash of h for the version version: the hash a Desired state stores when it is due again,
// at that version, although its rendering's hash h is the stored one (storedNeeds). It is a hash of h salted with the
// version, which no rendering yields and which differs from every hash the Root message was sent with — h and the
// repair hashes of earlier versions — so that the worker, which takes a delivery whose actual hash is the desired one
// as delivered, edits it, even when a call in flight records the hash it was sent with after the change or two repairs
// come before the send.
func repairHash(h []byte, version int64) []byte {
	b := append([]byte("redone\x00"), h...)
	b = binary.BigEndian.AppendUint64(b, uint64(version)) //nolint:gosec // G115: a version is never negative
	sum := sha256.Sum256(b)
	return sum[:]
}

// sameHash reports whether the stored hash of the Desired state of a Root message of an Alert Group, of version
// version, is the hash h of a rendering or its repair hash for that version: either way the rendering reads as the
// stored state, whose Root message is due or was sent with it. Every comparison of the stored hash of such a Root
// message with a rendering's goes through it; Storm summaries and outgoing webhooks are never repaired.
func sameHash(stored, h []byte, version int64) bool {
	return bytes.Equal(stored, h) || bytes.Equal(stored, repairHash(h, version))
}

// encodeShown is the Root message rd with only the buttons that set shows on a Route with the Snooze durations given
// (C-28.FR-8), with the key that signed the buttons it keeps; a set of every button, or nil, keeps rd as it is.
func encodeShown(rd messages.Rendered, set *buttons.Set, durations []int64) encoded {
	if set == nil || set.All {
		return encodeRoot(rd)
	}
	m := messages.ShownButtons(rd.Message, set, durations)
	e := encode(m)
	e.keyID = buttonKeyOf(m)
	return e
}

// buttonSetOf is the button set of a Destination from what destinations.buttons and organizations.buttons store: its
// own, else the Organization's (C-28.FR-8); nil, every button, when neither is a set, as for a row of an outgoing
// webhook or a row read without them.
func buttonSetOf(own, organization []byte) *buttons.Set {
	o, err := buttons.ParseOwn(own)
	if err != nil {
		return nil
	}
	org, err := buttons.ParseSet(organization)
	if err != nil {
		return nil
	}
	set := o.Effective(org)
	return &set
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

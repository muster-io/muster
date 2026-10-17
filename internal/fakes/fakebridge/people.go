// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package fakebridge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/oapi-codegen/nullable"

	"github.com/muster-io/muster/internal/bridge/gen"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

// The Account links of the fake messenger (C-22.FR-15): findUsers finds a person by the handle of the kind that
// /_fake/config sets as user_lookup — the email address (alice@example.org, bob@example.org, vera@example.org, in any
// case), the login or the username (alice, bob, vera) or the user id (u-alice, …) — and answers them with that handle;
// with user_lookup none it finds nobody and answers invalid. sendDirectMessage writes to a person in the direct chat
// with the bot while capabilities.direct_messages is on, answers forbidden for the people /_fake/config lists under
// dm_blocked, and keeps each message, which GET /_fake/direct-messages lists. POST /_fake/link {user_id, token[, bot]
// [, occurred_at]} is a person sending an Account link token to the bot bot, 1 by default, which queues an
// account_link event with the person and the handle of the configured kind; its answer is shown in the direct chat.

// TypeAccountLink is the type of the event of a person who sent the bot an Account link token.
const TypeAccountLink = "account_link"

// The bounds of findUsers and sendDirectMessage (api/bridge.yaml).
const (
	// MaxHandle is the longest handle findUsers searches for, in characters.
	MaxHandle = 320
	// MaxDirectMessage is the longest body of a direct message, in characters.
	MaxDirectMessage = 2000
	// maxUserID is the longest user id of a direct message, in characters.
	maxUserID = 512
)

// DirectMessageRefused is the detail of the forbidden answer to a direct message to a person in dm_blocked.
const DirectMessageRefused = "The bot cannot write to this person."

// emails are the email addresses of the people, by their external ids.
var emails = map[string]string{AliceID: "alice@example.org", BobID: "bob@example.org", VeraID: "vera@example.org"}

// Email is the email address of the person externalID, empty for one the fake does not know.
func Email(externalID string) string { return emails[externalID] }

// DirectMessage is a direct message a bot sent, as GET /_fake/direct-messages lists it: the bot, 1 for Token and 2 for
// SecondToken, its id dm<N>, the person it was sent to, its body and its Idempotency-Key.
type DirectMessage struct {
	Bot            int    `json:"bot"`
	ID             string `json:"id"`
	UserExternalID string `json:"user_external_id"`
	Body           string `json:"body"`
	Key            string `json:"key"`
}

// Link is a person sending an Account link token to a bot, as POST /_fake/link takes it: the person, the token, the
// bot, 1 for Token — the default — or 2 for SecondToken, and when, now by default.
type Link struct {
	UserID     string     `json:"user_id"`
	Token      string     `json:"token"`
	Bot        int        `json:"bot"`
	OccurredAt *time.Time `json:"occurred_at"`
}

type accountLinkEvent struct {
	ID         string     `json:"id"`
	Type       string     `json:"type"`
	OccurredAt time.Time  `json:"occurred_at"`
	Token      string     `json:"token"`
	User       gen.Person `json:"user"`
}

// DirectMessages returns the direct messages of both bots in the order they were sent.
func (f *Fake) DirectMessages() []DirectMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.directMessages)
}

// withHandle is p with its handle of the kind: the email address, the login for a login or a username, the user id,
// and null for none.
func withHandle(p gen.Person, kind gen.InfoUserLookup) gen.Person {
	switch kind {
	case gen.Email:
		p.Handle = nullable.NewNullableWithValue(emails[p.ExternalId])
	case gen.UserId:
		p.Handle = nullable.NewNullableWithValue(p.ExternalId)
	case gen.Login, gen.Username:
	default:
		p.Handle = nullable.NewNullNullable[string]()
	}
	return p
}

// found reports whether handle names p for the kind: an email address in any case, any other handle exactly.
func found(p gen.Person, kind gen.InfoUserLookup, handle string) bool {
	h := withHandle(p, kind).Handle.GetOrEmpty()
	if kind == gen.Email {
		return strings.EqualFold(h, handle)
	}
	return h != "" && h == handle
}

// findUsers answers findUsers: the person with the handle of the configured kind, with that handle, or nobody; with
// user_lookup none, and for a handle that is missing or too long, invalid.
func (f *Fake) findUsers(w http.ResponseWriter, r *http.Request) {
	handle := r.URL.Query().Get("handle")
	kind := f.Config().UserLookup
	switch n := utf8.RuneCountInString(handle); {
	case kind == gen.None:
		invalid(w, errors.New("the bridge looks nobody up"))
		return
	case n == 0 || n > MaxHandle:
		invalid(w, fmt.Errorf("handle is 1 to %d characters", MaxHandle))
		return
	}
	out := gen.PersonList{Items: []gen.Person{}}
	if i := slices.IndexFunc(people, func(p gen.Person) bool { return found(p, kind, handle) }); i >= 0 {
		out.Items = append(out.Items, withHandle(people[i], kind))
	}
	fakeserver.WriteJSON(w, http.StatusOK, out)
}

// sendDirectMessage answers sendDirectMessage: a repeated Idempotency-Key gets its first answer, replayed; otherwise
// the message is kept as dm<N> for a person the fake knows; without capabilities.direct_messages, and for an unknown
// person, it is invalid, and for a person in dm_blocked forbidden.
func (f *Fake) sendDirectMessage(w http.ResponseWriter, r *http.Request) {
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	var in gen.DirectMessageInput
	err := fakeserver.DecodeJSON(w, r, &in)
	if err == nil {
		err = checkDirectMessage(in)
	}
	b := botFrom(r)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.replay(w, b, key) {
		return
	}
	_, known := person(in.UserExternalId)
	switch {
	case err != nil:
		invalid(w, err)
		return
	case f.config.Capabilities.DirectMessages == nil:
		invalid(w, errors.New("the bot sends no direct messages"))
		return
	case !known:
		invalid(w, fmt.Errorf("unknown user %q", in.UserExternalId))
		return
	case slices.Contains(f.config.DMBlocked, in.UserExternalId):
		writeProblemDetail(w, http.StatusForbidden, gen.ErrorClassForbidden, "Forbidden", DirectMessageRefused)
		return
	}
	m := DirectMessage{Bot: b.n, ID: "dm" + strconv.Itoa(len(f.directMessages)+1), UserExternalID: in.UserExternalId,
		Body: in.Body, Key: key}
	f.directMessages = append(f.directMessages, m)
	f.answer(w, b, key, http.StatusCreated, gen.DirectMessageSent{MessageId: m.ID})
}

func checkDirectMessage(in gen.DirectMessageInput) error {
	switch n := utf8.RuneCountInString(in.Body); {
	case in.UserExternalId == "" || utf8.RuneCountInString(in.UserExternalId) > maxUserID:
		return fmt.Errorf("user_external_id is 1 to %d characters", maxUserID)
	case n == 0 || n > MaxDirectMessage:
		return fmt.Errorf("body is 1 to %d characters", MaxDirectMessage)
	}
	return nil
}

func (f *Fake) postLink(w http.ResponseWriter, r *http.Request) {
	var l Link
	if err := fakeserver.DecodeJSON(w, r, &l); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	ev, err := f.SendLink(r.Context(), l)
	if err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	fakeserver.WriteJSON(w, http.StatusOK, ev)
}

// SendLink queues l as an account_link event of its bot and returns it, with the person and the handle of the
// configured kind; ctx is what caused it. It fails for an unknown person, no token or a bot other than 1 and 2. The
// token is sent as it is, also one that breaks the protocol, so that Muster's refusal can be tried.
func (f *Fake) SendLink(ctx context.Context, l Link) (Event, error) {
	user, ok := person(l.UserID)
	switch {
	case !ok:
		return Event{}, fmt.Errorf("unknown user %q: the fake knows %s, %s and %s", l.UserID, AliceID, BobID, VeraID)
	case l.Token == "":
		return Event{}, errors.New("token is required")
	case l.Bot < 0 || l.Bot > 2:
		return Event{}, errors.New("bot is 1 or 2")
	}
	n := max(l.Bot, 1)
	at := occurred(l.OccurredAt)
	f.mu.Lock()
	defer f.mu.Unlock()
	user = withHandle(user, f.config.UserLookup)
	return f.queue(ctx, f.botByNumber(n), TypeAccountLink, "", user.ExternalId, func(id string) any {
		return accountLinkEvent{ID: id, Type: TypeAccountLink, OccurredAt: at, Token: l.Token, User: user}
	}), nil
}

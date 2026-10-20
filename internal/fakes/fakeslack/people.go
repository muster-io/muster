// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package fakeslack

import (
	"encoding/base64"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

// The people of the fake's workspaces as users.list pages them (F-095), beside UserAlice, UserBob, UserCarol and
// UserDora (presses.go): two accounts whose display name is "Sam", a bot whose display name is "Bob", and a deleted
// account whose display name is "bob".
const (
	UserSam1   = "U0FAKESAM1"
	UserSam2   = "U0FAKESAM2"
	UserHelper = "U0FAKEHELPER"
	UserBobOld = "U0FAKEBOBOLD"
)

// UsersPageSize is the most accounts a page of users.list holds, whatever its limit asks for, so that the directory
// spans three pages.
const UsersPageSize = 4

// usersCursorPrefix starts the decoded cursor of a page of users.list, which the offset of the page follows.
const usersCursorPrefix = "users:"

// The scopes of the methods that read people (F-095) and of conversations.open (F-096).
const (
	usersScope = "users:read"
	emailScope = "users:read.email"
	imScope    = "im:write"
	openScopes = "channels:manage,groups:write,im:write,mpim:write"
)

// Person is an account of a workspace as users.list, users.info and users.lookupByEmail answer it (F-095): its user
// id, name, real name and profile display name, email, time zone, and whether it is deleted or a bot.
type Person struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	RealName    string `json:"real_name"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	TZ          string `json:"tz"`
	Deleted     bool   `json:"deleted"`
	IsBot       bool   `json:"is_bot"`
}

// People are the accounts of the app's workspace in the order users.list pages them: Slackbot, which Slack does not
// count as a bot; the people, two of them named "Sam", Bob without a display name of his own; a bot named "Bob"; the
// app's bot user; and a deleted account named "bob". The person of a person's token (UserID) is not among them.
func People(app App) []Person {
	const tz = "Europe/Berlin"
	return []Person{
		{ID: slackbot, Name: "slackbot", RealName: "Slackbot", DisplayName: "Slackbot", TZ: "America/Los_Angeles"},
		{ID: UserSam1, Name: "sam.one", RealName: "Sam One", DisplayName: "Sam", Email: "sam.one@example.org", TZ: tz},
		{ID: UserCarol, Name: "carol", RealName: "Carol Jones", DisplayName: "Carol", Email: "carol@example.org", TZ: tz},
		{ID: UserHelper, Name: "helper", RealName: "Helper", DisplayName: "Bob", TZ: tz, IsBot: true},
		{ID: UserAlice, Name: "alice", RealName: "Alice Smith", DisplayName: "Alice Smith", Email: "alice@example.org",
			TZ: tz},
		{ID: UserBob, Name: "bob", RealName: "Bob Jones", Email: "bob@example.org", TZ: tz},
		{ID: UserDora, Name: "dora", RealName: "Dora Lee", DisplayName: "Dora", Email: "dora@example.org", TZ: tz},
		{ID: app.BotUserID, Name: app.BotName, DisplayName: app.BotName, TZ: tz, IsBot: true},
		{ID: UserBobOld, Name: "bob.old", RealName: "Bob Old", DisplayName: "bob", Email: "bob.old@example.org",
			TZ: tz, Deleted: true},
		{ID: UserSam2, Name: "sam.two", RealName: "Sam Two", DisplayName: "sam", Email: "sam.two@example.org", TZ: tz},
	}
}

// personOf is the person id of the app's workspace, false for an id it does not have.
func personOf(app App, id string) (Person, bool) {
	all := People(app)
	i := slices.IndexFunc(all, func(p Person) bool { return p.ID == id })
	if i < 0 {
		return Person{}, false
	}
	return all[i], true
}

// memberOf is p as users.list, users.info and users.lookupByEmail answer it, with its email in the profile when
// withEmail, as Slack shows it only to a token with users:read.email (F-095).
func memberOf(app App, p Person, withEmail bool) map[string]any {
	profile := map[string]any{"display_name": p.DisplayName, "real_name": p.RealName}
	if withEmail && p.Email != "" {
		profile["email"] = p.Email
	}
	return map[string]any{"id": p.ID, "team_id": app.TeamID, "name": p.Name, "real_name": p.RealName,
		"deleted": p.Deleted, "is_bot": p.IsBot, "tz": p.TZ, "profile": profile}
}

// peopleCaller answers the token of a method that reads people when it cannot use it: an app-level token is the wrong
// kind, and a token without scope lacks it; false when it answered.
func peopleCaller(w http.ResponseWriter, c caller, scopes, scope string) bool {
	switch {
	case c.kind == kindApp:
		writeError(w, "not_allowed_token_type")
		return false
	case !slices.Contains(splitScopes(scopes), scope):
		missingScope(w, scope, scopes)
		return false
	}
	return true
}

// usersList answers users.list (F-095) page by page: a page holds limit accounts, UsersPageSize at most and by
// default, and next_cursor names the rest, empty on the last page; a cursor the fake did not give is invalid_cursor.
func usersList(w http.ResponseWriter, c caller, scopes string, args map[string]string) {
	if !peopleCaller(w, c, scopes, usersScope) {
		return
	}
	all := People(c.app)
	start := 0
	if cursor := args["cursor"]; cursor != "" {
		raw, err := base64.StdEncoding.DecodeString(cursor)
		from, ok := strings.CutPrefix(string(raw), usersCursorPrefix)
		n, nerr := strconv.Atoi(from)
		if err != nil || !ok || nerr != nil || n <= 0 || n >= len(all) {
			writeError(w, "invalid_cursor")
			return
		}
		start = n
	}
	limit := UsersPageSize
	if n, err := strconv.Atoi(args["limit"]); err == nil && n > 0 {
		limit = min(n, UsersPageSize)
	}
	end := min(start+limit, len(all))
	withEmail := slices.Contains(splitScopes(scopes), emailScope)
	members := make([]map[string]any, 0, end-start)
	for _, p := range all[start:end] {
		members = append(members, memberOf(c.app, p, withEmail))
	}
	next := ""
	if end < len(all) {
		next = base64.StdEncoding.EncodeToString([]byte(usersCursorPrefix + strconv.Itoa(end)))
	}
	writeOK(w, map[string]any{"members": members, "cache_ts": 1760000000,
		"response_metadata": map[string]any{"next_cursor": next}})
}

// usersLookupByEmail answers users.lookupByEmail (F-095): the account whose email is the address, whatever its letter
// case, deleted ones included, as Slack answers them with deleted true; any other address is users_not_found.
func usersLookupByEmail(w http.ResponseWriter, c caller, scopes, email string) {
	if !peopleCaller(w, c, scopes, emailScope) {
		return
	}
	for _, p := range People(c.app) {
		if p.Email != "" && strings.EqualFold(p.Email, email) {
			writeOK(w, map[string]any{"user": memberOf(c.app, p, true)})
			return
		}
	}
	writeError(w, "users_not_found")
}

// usersInfo answers users.info (F-095): the account of the user id; any other id is user_not_found.
func usersInfo(w http.ResponseWriter, c caller, scopes, user string) {
	if !peopleCaller(w, c, scopes, usersScope) {
		return
	}
	p, ok := personOf(c.app, user)
	if !ok {
		writeError(w, "user_not_found")
		return
	}
	writeOK(w, map[string]any{"user": memberOf(c.app, p, slices.Contains(splitScopes(scopes), emailScope))})
}

// DMChannel is the direct chat of the bot and the person user: D followed by the user id without its first letter.
func DMChannel(user string) string {
	if user == "" {
		return "D"
	}
	return "D" + user[1:]
}

// conversationsOpen answers conversations.open (F-096) for the person users: the direct chat with the bot, already
// open; a bot, the app's own bot user included, is cannot_dm_bot, and an unknown or deleted account user_not_found.
func conversationsOpen(w http.ResponseWriter, c caller, scopes, users string) {
	if !peopleCaller(w, c, scopes, imScope) {
		return
	}
	p, ok := personOf(c.app, users)
	switch {
	case ok && p.IsBot:
		writeError(w, "cannot_dm_bot")
	case !ok || p.Deleted:
		writeError(w, "user_not_found")
	default:
		writeOK(w, map[string]any{"no_op": true, "already_open": true, "channel": map[string]any{"id": DMChannel(p.ID)}})
	}
}

// directTarget reports whether the channel of chat.postMessage is a user id, which posts into the bot's direct chat
// with that person (F-096).
func directTarget(channel string) bool {
	return strings.HasPrefix(channel, "U") || strings.HasPrefix(channel, "W")
}

// postDirect answers chat.postMessage to the user id of one of the app's people (F-096): the message lands in the
// bot's direct chat with the person, DMChannel, and the answer names that channel. A bot, the app's own bot user
// included, is cannot_dm_bot and a deleted account user_not_found, as conversations.open answers them; an id that is
// not one of the people stays channel_not_found, and a person of an app in messages_tab_off is messages_tab_disabled.
// The model of the direct chat is the fake's own: F-096 verified the delivery to a user id and the errors of
// conversations.open only.
func (f *Fake) postDirect(w http.ResponseWriter, c caller, scopes string, a chatArgs) {
	if !chatCaller(w, c, scopes) {
		return
	}
	id, _ := a.text("channel")
	p, ok := personOf(c.app, id)
	switch {
	case !ok:
		writeError(w, "channel_not_found")
		return
	case p.IsBot:
		writeError(w, "cannot_dm_bot")
		return
	case p.Deleted:
		writeError(w, "user_not_found")
		return
	case f.messagesTabOff(c.app.Key):
		writeError(w, "messages_tab_disabled")
		return
	}
	con, code, errs := contentOf(a)
	switch {
	case code != "":
		writeInvalid(w, code, errs)
		return
	case (con.text == nil || *con.text == "") && empty(con.blocks) && empty(con.attachments):
		writeError(w, "no_text")
		return
	}
	channel := DMChannel(p.ID)
	m := &stored{user: userOf(c), text: deref(con.text), blocks: orEmpty(con.blocks),
		attachments: orEmpty(con.attachments), unfurlLinks: a.flag("unfurl_links"),
		unfurlMedia: a.flag("unfurl_media"), richOnly: richOnly(con.blocks)}
	f.mu.Lock()
	defer f.mu.Unlock()
	m.seq, m.ts = f.nextTS()
	k := chatKey{c.app.Key, channel}
	f.chats[k] = append(f.chats[k], m)
	writeOK(w, map[string]any{"channel": channel, "ts": m.ts, "message": answerOf(c, m, nil)})
}

// messagesTabOff reports whether the Messages tab of the app key is off.
func (f *Fake) messagesTabOff(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.config.MessagesTabOff, key)
}

// DirectMessages are the messages the bot of the app sent to the person user by direct message, oldest first; of both
// apps when app is empty.
func (f *Fake) DirectMessages(app, user string) []Message {
	return f.Messages(app, DMChannel(user))
}

// listDMs answers GET /_fake/dms?user=U…[&app=a|b] with DirectMessages; the user is required.
func (f *Fake) listDMs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	user := q.Get("user")
	if user == "" {
		fakeserver.WriteError(w, http.StatusBadRequest, "user is required")
		return
	}
	fakeserver.WriteJSON(w, http.StatusOK, f.DirectMessages(q.Get("app"), user))
}

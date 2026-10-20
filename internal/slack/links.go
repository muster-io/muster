// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package slack

import (
	"cmp"
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"

	"github.com/muster-io/muster/internal/accountlinks"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/outbound"
)

// The paging of users.list (C-24.FR-12, F-095): 200 accounts a page, as Slack advises, and a bound on the pages that
// only a cursor that never ends could reach.
const (
	userPageSize = 200
	maxUserPages = 500
)

// slackbot is the user id of Slackbot, which users.list answers among the people and no link takes (F-095).
const slackbot = "USLACKBOT"

// slackUserID is the shape of a Slack user id, U or W and capitals or digits: a link stores only such an id, and a
// Mention renders only such an id as <@…> (layout.go).
var slackUserID = regexp.MustCompile(`^[UW][A-Z0-9]{2,20}$`)

// refusedDirect are the errors of chat.postMessage to a user id that refuse the person rather than the Connection: a
// bot, an unknown or deleted account (F-096), and a user id Slack does not know as a channel.
var refusedDirect = []string{"cannot_dm_bot", "user_not_found", "channel_not_found"}

// LinkConnections find the Slack Connection of an Account link, declared by their consumer; *connections.Service
// implements them.
type LinkConnections interface {
	// SlackLinkConnection is the Slack Connection publicID with its client, in either event mode; one that is unknown,
	// deleted or not a Slack Connection is ErrNoConnection.
	SlackLinkConnection(ctx context.Context, publicID string) (Conn, error)
}

// LinkBots run the calls of an Account link through the bot of a Slack Connection (C-24.FR-12, C-18.FR-11): the
// lookup of the account by email or name and the bot's direct messages, on the interactive path, never as a delivery.
// They implement accountlinks.SlackBots.
type LinkBots struct {
	Conns LinkConnections
	Path  Path
}

// Session takes one limiter token of the Connection publicID on the interactive path and runs f with its bot; a 429
// with Retry-After that a call of f gets holds the Connection, since Slack limits each method per app and workspace
// (F-099). A Connection that is unknown, deleted or not a Slack one is accountlinks.ErrNoConnection; no token within
// the budget is a *delivery.LimitedError and runs nothing.
func (l LinkBots) Session(ctx context.Context, publicID string,
	f func(ctx context.Context, b accountlinks.SlackBot) error) error {
	conn, err := l.Conns.SlackLinkConnection(ctx, publicID)
	if errors.Is(err, ErrNoConnection) {
		return accountlinks.ErrNoConnection
	}
	if err != nil {
		return err
	}
	var ferr error
	id := conn.ID
	if _, err := l.Path.Do(ctx, delivery.Subject{Connection: &id}, delivery.DirectOp(
		func(ctx context.Context, call delivery.Call) delivery.Outcome {
			b := &linkBot{conn: conn, class: call.Class, last: delivery.Outcome{Kind: delivery.OutcomeOK}}
			ferr = f(ctx, b)
			return b.last
		})); err != nil {
		return err
	}
	return ferr
}

// linkBot is the bot of a Slack Connection within a Session.
type linkBot struct {
	conn  Conn
	class outbound.Class
	// last is the outcome of the last call that failed, without its text, which the Session hands to the interactive
	// path.
	last delivery.Outcome
}

// member is an account as users.list and users.lookupByEmail answer it (F-095).
type member struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Deleted bool   `json:"deleted"`
	IsBot   bool   `json:"is_bot"`
	Profile struct {
		DisplayName string `json:"display_name"`
		RealName    string `json:"real_name"`
	} `json:"profile"`
}

// linkable reports whether the account is a person a link may take: not deleted, not a bot, not Slackbot, with a user
// id of the shape of slackUserID.
func (m member) linkable() bool {
	return !m.Deleted && !m.IsBot && m.ID != slackbot && slackUserID.MatchString(m.ID)
}

// account is m as a link stores it, masked: its user id, its name and its display name — the profile's display_name,
// else its real_name.
func (c *Client) account(m member) accountlinks.SlackAccount {
	return accountlinks.SlackAccount{ID: m.ID, Name: c.mask(m.Name),
		DisplayName: c.mask(cmp.Or(m.Profile.DisplayName, m.Profile.RealName))}
}

// emailHandle reports whether handle is an email: an @ after at least one character, and a dot after that @.
func emailHandle(handle string) bool {
	i := strings.LastIndexByte(handle, '@')
	return i > 0 && strings.Contains(handle[i+1:], ".")
}

// FindUser finds the account with handle (C-24.FR-12): an email with users.lookupByEmail, which ignores its letter
// case; anything else, a leading @ dropped, a name compared with each account's name and profile display_name
// regardless of case over the pages of users.list, until two match or the last page is read. A deleted account, a bot,
// Slackbot and an id of another shape are no match; none is accountlinks.ErrMessengerUserNotFound, and two by name
// accountlinks.ErrHandleAmbiguous.
func (b *linkBot) FindUser(ctx context.Context, handle string) (accountlinks.SlackAccount, error) {
	h := strings.TrimSpace(handle)
	if emailHandle(h) {
		return b.byEmail(ctx, h)
	}
	return b.byName(ctx, strings.TrimSpace(strings.TrimPrefix(h, "@")))
}

// byEmail finds the account of email with users.lookupByEmail; users_not_found is no account.
func (b *linkBot) byEmail(ctx context.Context, email string) (accountlinks.SlackAccount, error) {
	const method = "users.lookupByEmail"
	r := b.conn.Client.Call(ctx, b.class, method, Args{"email": email})
	if r.ErrorCode == "users_not_found" {
		return accountlinks.SlackAccount{}, accountlinks.ErrMessengerUserNotFound
	}
	if err := b.failed(method, r); err != nil {
		return accountlinks.SlackAccount{}, err
	}
	var out struct {
		User *member `json:"user"`
	}
	if r.Decode(&out) != nil || out.User == nil {
		return accountlinks.SlackAccount{}, b.failed(method, notExpected(r, method))
	}
	if !out.User.linkable() {
		return accountlinks.SlackAccount{}, accountlinks.ErrMessengerUserNotFound
	}
	return b.conn.Client.account(*out.User), nil
}

// byName finds the account named name with users.list, userPageSize accounts a page, following next_cursor until two
// accounts match or a page names no further cursor, or the cursor it was given.
func (b *linkBot) byName(ctx context.Context, name string) (accountlinks.SlackAccount, error) {
	const method = "users.list"
	var (
		found  []member
		cursor string
	)
	for range maxUserPages {
		args := Args{"limit": userPageSize}
		if cursor != "" {
			args["cursor"] = cursor
		}
		r := b.conn.Client.Call(ctx, b.class, method, args)
		if err := b.failed(method, r); err != nil {
			return accountlinks.SlackAccount{}, err
		}
		var page struct {
			Members []member `json:"members"`
		}
		if r.Decode(&page) != nil {
			return accountlinks.SlackAccount{}, b.failed(method, notExpected(r, method))
		}
		for _, m := range page.Members {
			if !m.linkable() || (!strings.EqualFold(m.Name, name) && !strings.EqualFold(m.Profile.DisplayName, name)) ||
				slices.ContainsFunc(found, func(f member) bool { return f.ID == m.ID }) {
				continue
			}
			if found = append(found, m); len(found) > 1 {
				return accountlinks.SlackAccount{}, accountlinks.ErrHandleAmbiguous
			}
		}
		if r.NextCursor == "" || r.NextCursor == cursor {
			if len(found) == 0 {
				return accountlinks.SlackAccount{}, accountlinks.ErrMessengerUserNotFound
			}
			return b.conn.Client.account(found[0]), nil
		}
		cursor = r.NextCursor
	}
	return accountlinks.SlackAccount{}, &accountlinks.MessengerError{Call: method, Outcome: string(ClassUnknown)}
}

// SendCode sends text, which holds code, by direct message to the account userID, with code registered as a secret of
// the request, so that its record in the Request log has it masked in the request and in Slack's answer, which repeats
// the message.
func (b *linkBot) SendCode(ctx context.Context, userID string, code logging.Secret, text string) error {
	return b.direct(outbound.WithSecrets(ctx, code), "the direct message", userID, text)
}

// SendLinked sends text by direct message to the account userID.
func (b *linkBot) SendLinked(ctx context.Context, userID, text string) error {
	return b.direct(ctx, "the confirmation", userID, text)
}

// direct posts text, escaped for mrkdwn, to the account userID with chat.postMessage, whose channel is the user id
// (F-096): the bot's direct chat with the person, without unfurling. An error of refusedDirect is
// accountlinks.ErrDirectMessageRefused, which leaves the Connection as it is.
func (b *linkBot) direct(ctx context.Context, call, userID, text string) error {
	r := b.conn.Client.Call(ctx, b.class, "chat.postMessage", Args{"channel": userID,
		"text": answerText.Replace(text), "unfurl_links": false, "unfurl_media": false})
	if slices.Contains(refusedDirect, r.ErrorCode) {
		return accountlinks.ErrDirectMessageRefused
	}
	return b.failed(call, r)
}

// failed is the error of a call that did not succeed: a RetryAfter is a *delivery.LimitedError and holds the
// Connection, anything else an *accountlinks.MessengerError that names the call and how it ended, never Slack's text,
// which may repeat the direct message and so the code; the Session's outcome keeps no text either.
func (b *linkBot) failed(call string, r Result) error {
	if r.OK() {
		return nil
	}
	o := r.Outcome()
	b.last = delivery.Outcome{Kind: o.Kind, RetryAfter: o.RetryAfter, Scope: o.Scope, Status: o.Status}
	if o.Kind == delivery.OutcomeRetryAfter {
		return &delivery.LimitedError{RetryAfter: o.RetryAfter}
	}
	return &accountlinks.MessengerError{Call: call, Outcome: string(r.Classification.Class), Status: r.Status}
}

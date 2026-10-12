// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package telegram

import (
	"context"
	"strings"

	"github.com/muster-io/muster/internal/accountlinks"
	"github.com/muster-io/muster/internal/delivery"
)

// startCommand is the command of a deep link: Telegram sends `/start <token>` when the person presses Start.
const startCommand = "/start"

// Linker links the Telegram account that sent `/start <token>`; *accountlinks.Service implements it.
type Linker interface {
	LinkTelegram(ctx context.Context, conn accountlinks.TelegramConnection, token string,
		from accountlinks.TelegramAccount, reply func(ctx context.Context, text string) error) error
}

// Start is the handler of the private messages of the router (C-14.FR-8, C-18.FR-2): `/start <token>`, which Telegram
// sends when a person follows the deep link of their profile and presses Start, links that person's account. Any
// other private message, an edit, and a message of a bot are ignored without an answer.
type Start struct {
	Links Linker
	// Path is the interactive path the bot's replies go through, on the Connection's limiter.
	Path Path
}

// Handle hands `/start <token>`, or `/start@<bot> <token>`, to the Linker with the account that sent it and a reply to
// its private chat; like every update it runs under the Connection's update lock. An error of the Linker — a database
// failure — leaves the update unconfirmed, so that it comes again.
func (s *Start) Handle(ctx context.Context, c Conn, u Update) error {
	m := u.Message
	if m == nil || m.From == nil || m.From.IsBot || m.Chat.Type != "private" {
		return nil
	}
	f := strings.Fields(m.Text)
	if len(f) != 2 || (f[0] != startCommand && !strings.HasPrefix(f[0], startCommand+"@")) {
		return nil
	}
	chat := m.Chat.ID
	return s.Links.LinkTelegram(ctx, accountlinks.TelegramConnection{ID: c.ID, PublicID: c.PublicID}, f[1],
		accountlinks.TelegramAccount{ID: m.From.ID, Username: m.From.Username},
		func(ctx context.Context, text string) error { return s.reply(ctx, c, chat, text) })
}

// reply sends text to the private chat through the interactive path, limited by the Connection only: a private
// message spends no token of a Destination, and a RetryAfter it gets holds the Connection. A 429 is a
// *delivery.LimitedError; another failure is an *accountlinks.MessengerError that names how the call ended, never
// Telegram's text, which may repeat the message.
func (s *Start) reply(ctx context.Context, c Conn, chat int64, text string) error {
	id := c.ID
	var status int
	out, err := s.Path.Do(ctx, delivery.Subject{Connection: &id}, delivery.DirectOp(
		func(ctx context.Context, call delivery.Call) delivery.Outcome {
			r := c.Client.sendPrivate(ctx, call.Class, chat, text)
			status = r.Status
			o := r.Outcome
			if o.Kind == delivery.OutcomeRetryAfter {
				o.Scope = delivery.ScopeConnection
			}
			return o
		}))
	switch {
	case err != nil:
		return err
	case out.Kind == delivery.OutcomeRetryAfter:
		return &delivery.LimitedError{RetryAfter: out.RetryAfter}
	case out.Kind != delivery.OutcomeOK:
		return &accountlinks.MessengerError{Call: "the reply", Outcome: string(out.Kind), Status: status}
	}
	return nil
}

// ConnLookup finds a Telegram Connection that is not deleted with its client; *connections.Service implements it.
type ConnLookup interface {
	TelegramConn(ctx context.Context, publicID string) (Conn, error)
}

// Bots read the bots of Telegram Connections for starting an Account link (C-18.FR-2); they implement
// accountlinks.TelegramBots.
type Bots struct {
	Conns ConnLookup
	Path  Path
}

// Username reads the username of the bot of the Connection publicID with getMe on the interactive path, for a
// Connection whose check has not stored it yet. No limiter token within the budget, and a 429, are a
// *delivery.LimitedError; another failure is an *accountlinks.MessengerError without Telegram's text.
func (b Bots) Username(ctx context.Context, publicID string) (string, error) {
	c, err := b.Conns.TelegramConn(ctx, publicID)
	if err != nil {
		return "", err
	}
	var (
		me User
		r  Result
	)
	id := c.ID
	if _, err := b.Path.Do(ctx, delivery.Subject{Connection: &id}, delivery.ReadOp(
		func(ctx context.Context, call delivery.Call) delivery.Outcome {
			me, r = c.Client.GetMe(ctx, call.Class)
			return r.Outcome
		})); err != nil {
		return "", err
	}
	switch {
	case r.Outcome.Kind == delivery.OutcomeRetryAfter:
		return "", &delivery.LimitedError{RetryAfter: r.Outcome.RetryAfter}
	case !r.OK() || me.Username == "":
		return "", &accountlinks.MessengerError{Call: "getMe", Outcome: string(r.Outcome.Kind), Status: r.Status}
	}
	return me.Username, nil
}

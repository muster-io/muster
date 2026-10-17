// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package accountlinks

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/accountlinks/dbgen"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/publicid"
)

// TelegramBots read the bots of Telegram Connections on the interactive path; *telegram.Bots implements it.
type TelegramBots interface {
	// Username is the username of the bot of the Telegram Connection publicID, read with getMe. No limiter token
	// within the interactive budget is a *delivery.LimitedError, a refused call a *MessengerError.
	Username(ctx context.Context, connectionPublicID string) (string, error)
}

// TelegramStart is a started Telegram link: the deep link to open and when its token expires.
type TelegramStart struct {
	DeepLink  string
	ExpiresAt time.Time
}

// StartTelegram starts a Telegram link of the requesting User through the Telegram Connection connectionPublicID
// (C-18.FR-2): a token of 32 random bytes in base64url, of which only the SHA-256 is stored with the User, the
// Connection and the expiry account_link.telegram_token_ttl from now; the answer is t.me/<bot>?start=<token>. The bot's
// username comes from the last Connection check, or from getMe on the interactive path; a getMe that gets no token or
// is refused stores nothing. A Connection that is missing, deleted or not a Telegram one is a FieldError unknown_id.
func (s *Service) StartTelegram(ctx context.Context, r Requester, connectionPublicID string) (TelegramStart, error) {
	conn, err := s.connection(ctx, connectionPublicID, MessengerTelegram)
	if err != nil {
		return TelegramStart{}, err
	}
	username := conn.BotUsername.String
	if username == "" {
		if username, err = s.cfg.Telegram.Username(ctx, conn.PublicID); err != nil {
			return TelegramStart{}, err
		}
	}
	raw := make([]byte, tokenBytes)
	if _, err := io.ReadFull(s.cfg.Random, raw); err != nil {
		return TelegramStart{}, fmt.Errorf("generate the link token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := s.now()
	expires := now.Add(TelegramTokenTTL)
	if _, err := s.cfg.Store.InsertLinkRequest(ctx, dbgen.InsertLinkRequestParams{OrgID: s.cfg.OrgID,
		PublicID: publicid.New(publicid.AccountLinkRequest), UserID: r.UserID, Messenger: MessengerTelegram,
		ConnectionID: conn.ID, SecretHash: hash(token), CreatedAt: now, ExpiresAt: expires}); err != nil {
		return TelegramStart{}, fmt.Errorf("store the link request: %w", err)
	}
	return TelegramStart{DeepLink: "https://t.me/" + username + "?start=" + token, ExpiresAt: expires}, nil
}

// connection is the Connection publicID of the messenger, not deleted, for starting a link; anything else is a
// FieldError unknown_id at /connection_id.
func (s *Service) connection(ctx context.Context, publicID, messenger string) (dbgen.GetLinkConnectionRow, error) {
	unknown := &FieldError{Pointer: pointerConnection, Code: CodeUnknownID,
		Detail: "No " + messengerName(messenger) + " Connection has this id."}
	norm, err := publicid.Parse(publicid.Connection, publicID)
	if err != nil {
		return dbgen.GetLinkConnectionRow{}, unknown
	}
	c, err := s.cfg.Store.GetLinkConnection(ctx, dbgen.GetLinkConnectionParams{OrgID: s.cfg.OrgID, PublicID: norm})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && c.Type != messenger) {
		return dbgen.GetLinkConnectionRow{}, unknown
	}
	if err != nil {
		return dbgen.GetLinkConnectionRow{}, fmt.Errorf("read the connection: %w", err)
	}
	return c, nil
}

func messengerName(messenger string) string {
	switch messenger {
	case MessengerTelegram:
		return "Telegram"
	case MessengerBridge:
		return "Bridge"
	}
	return "Mattermost"
}

// TelegramAccount is the Telegram account that sent `/start`: its user id and username, empty without one.
type TelegramAccount struct {
	ID       int64
	Username string
}

// TelegramConnection is the Connection whose bot received `/start`.
type TelegramConnection struct {
	ID       int64
	PublicID string
}

// telegramOutcome is how a `/start` ended: the link, or the reason it was rejected, and the bot's reply.
type telegramOutcome struct {
	link   Link
	old    *view
	user   string
	reason string
	reply  string
}

// LinkTelegram handles `/start <token>` that the account from sent to the bot of conn (C-18.FR-2, FR-4, FR-5). The
// token is looked up by its hash; it must be unconsumed, not void, not expired and bound to conn. Otherwise nothing is
// linked, account_link.link_rejected is recorded with the reason token_unknown, token_used, token_expired or
// wrong_connection, and the bot replies "This link is invalid or expired. Start again from your Muster profile." An
// account linked to another User is rejected with conflict and the reply that it is linked to another user, the
// request kept. Otherwise, in one transaction, the User's previous Telegram link is replaced, the account is linked,
// the request consumed, the link recorded and the hint sent; then the bot replies "Linked to Muster user {name}
// ({login})". reply sends the bot's answer through the interactive path; its failure is only logged. An error is a
// database failure, which leaves the update unconfirmed.
func (s *Service) LinkTelegram(ctx context.Context, conn TelegramConnection, token string, from TelegramAccount,
	reply func(ctx context.Context, text string) error) error {
	a := account{messenger: MessengerTelegram, conn: connRef{id: conn.ID, publicID: conn.PublicID}, space: SpaceTelegram,
		external: strconv.FormatInt(from.ID, 10)}
	if from.Username != "" {
		u := from.Username
		a.username = &u
	}
	var out telegramOutcome
	err := s.cfg.Store.InTx(ctx, func(q Queries) error {
		out = telegramOutcome{}
		c, err := q.GetLinkConnection(ctx, dbgen.GetLinkConnectionParams{OrgID: s.cfg.OrgID, PublicID: conn.PublicID})
		if err != nil {
			return fmt.Errorf("read the connection %s: %w", conn.PublicID, err)
		}
		a.conn.name = c.Name
		return s.linkTelegram(ctx, q, a, conn, token, &out)
	})
	if err != nil {
		return err
	}
	replyErr := reply(ctx, out.reply)
	if out.reason != "" {
		s.logRejected(ctx, MessengerTelegram, conn.PublicID, out.reason, replyErr)
		return nil
	}
	s.logLinked(ctx, out.link, out.old, out.user, replyErr)
	return nil
}

// linkTelegram is the transaction of LinkTelegram; out says how it ended.
func (s *Service) linkTelegram(ctx context.Context, q Queries, a account, conn TelegramConnection, token string,
	out *telegramOutcome) error {
	system := audit.System
	invalid := func(lang string) string { return messages.T(lang, "accountLink.invalid", nil) }
	r, err := q.GetTelegramRequestByHash(ctx, dbgen.GetTelegramRequestByHashParams{OrgID: s.cfg.OrgID,
		SecretHash: hash(token)})
	if errors.Is(err, pgx.ErrNoRows) {
		out.reason, out.reply = ReasonTokenUnknown, invalid(messages.LanguageEnglish)
		return s.reject(ctx, q, system, audit.TransportTelegram, noAddress, a, out.reason, "")
	}
	if err != nil {
		return fmt.Errorf("read the link request: %w", err)
	}
	u, err := q.LockLinkUser(ctx, dbgen.LockLinkUserParams{OrgID: s.cfg.OrgID, ID: r.UserID})
	if err != nil {
		return fmt.Errorf("lock the user %s: %w", r.UserPublicID, err)
	}
	lang := language(u.Language)
	switch {
	case r.ConsumedAt.Valid || r.VoidedAt.Valid || u.Status == statusDeleted:
		out.reason = ReasonTokenUsed
	case !r.ExpiresAt.After(s.now()):
		out.reason = ReasonTokenExpired
	case r.ConnectionID != conn.ID:
		out.reason = ReasonWrongConnection
	}
	if out.reason != "" {
		out.reply = invalid(lang)
		return s.reject(ctx, q, system, audit.TransportTelegram, noAddress, a, out.reason, u.PublicID)
	}
	l, old, err := s.link(ctx, q, u, a, audit.User(u.ID, u.PublicID), audit.TransportTelegram, noAddress)
	if errors.Is(err, ErrLinkedElsewhere) {
		out.reason, out.reply = ReasonConflict, messages.T(lang, "accountLink.conflict", nil)
		return s.reject(ctx, q, system, audit.TransportTelegram, noAddress, a, out.reason, u.PublicID)
	}
	if err != nil {
		return err
	}
	if _, err := q.ConsumeLinkRequest(ctx, dbgen.ConsumeLinkRequestParams{Now: s.now(), OrgID: s.cfg.OrgID,
		ID: r.ID}); err != nil {
		return fmt.Errorf("consume the link request: %w", err)
	}
	out.link, out.old, out.user = l, old, u.PublicID
	out.reply = messages.T(lang, "accountLink.linkedTelegram", messages.Args{"name": u.Name, "login": u.Login})
	return nil
}

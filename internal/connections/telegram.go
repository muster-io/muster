// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package connections

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/connections/dbgen"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/destinations"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/proxyconf"
	"github.com/muster-io/muster/internal/telegram"
)

// cachedTelegram is the Telegram client of a version of a Connection.
type cachedTelegram struct {
	version int64
	client  *telegram.Client
}

// WebhookURL is where Telegram posts the updates of the Connection publicID in the webhook mode: MUSTER_INGEST_URL
// followed by /api/v1/callbacks/telegram/<public_id> (C-14.FR-1).
func (s *Service) WebhookURL(publicID string) string {
	base := ""
	if s.cfg.IngestURL != nil {
		base = strings.TrimSuffix(s.cfg.IngestURL.String(), "/")
	}
	return base + telegram.WebhookPath + publicID
}

// telegramSettings are the settings of the client of a stored Telegram Connection, its secrets decrypted.
func (s *Service) telegramSettings(id string, base string, token, password keyring.StoredSecret, proxy []byte) (
	telegram.Settings, error) {
	t, err := s.cfg.Keyring.OpenSecret(fieldBotToken, token)
	if err != nil {
		return telegram.Settings{}, fmt.Errorf("open the bot token of the connection %s: %w", id, err)
	}
	pw, err := s.cfg.Keyring.OpenSecret(fieldProxyPassword, password)
	if err != nil {
		return telegram.Settings{}, fmt.Errorf("open the proxy password of the connection %s: %w", id, err)
	}
	p, err := proxyconf.Parse(proxy)
	if err != nil {
		return telegram.Settings{}, err
	}
	return telegram.Settings{BaseURL: base, Token: t, Proxy: p.Outbound(pw)}, nil
}

// telegramClient builds the Telegram client of the stored Connection row.
func (s *Service) telegramClient(row dbgen.GetConnectionRow) (*telegram.Client, error) {
	if row.Type != TypeTelegram {
		return nil, errNotTelegram
	}
	st, err := s.telegramSettings(row.PublicID, row.TelegramBotApiBaseUrl.String, storedToken(row),
		storedPassword(row), row.Proxy)
	if err != nil {
		return nil, err
	}
	return telegram.NewClient(telegram.Network(s.cfg.Network), st)
}

// cachedTelegramClient is the Telegram client of the Connection id at version, built again when the version changed.
func (s *Service) cachedTelegramClient(id, version int64, build func() (*telegram.Client, error)) (*telegram.Client,
	error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cc, ok := s.tgClients[id]; ok && cc.version == version {
		return cc.client, nil
	}
	c, err := build()
	if err != nil {
		return nil, err
	}
	s.tgClients[id] = cachedTelegram{version: version, client: c}
	return c, nil
}

// webhookSecret is the webhook secret token to store for in, saved over before (nil on a creation): a new one, which
// setWebhook must register, when a Telegram Connection enters the webhook mode or changes its base URL or bot token in
// it, or has none; none when it leaves the webhook mode; the stored one otherwise.
func (s *Service) webhookSecret(in Input, before *Connection, stored keyring.StoredSecret, now time.Time) (
	keyring.StoredSecret, logging.Secret, error) {
	if in.Type != TypeTelegram || in.UpdateMode != ModeWebhook {
		hook, _, err := s.cfg.Keyring.ApplySecret(fieldWebhookSecret, stored, keyring.Clear, now)
		return hook, "", err
	}
	if before != nil && before.UpdateMode == ModeWebhook && before.BotAPIBaseURL == in.BotAPIBaseURL &&
		!in.BotToken.Given && stored.Set() {
		return stored, "", nil
	}
	secret, err := telegram.NewSecret()
	if err != nil {
		return stored, "", fmt.Errorf("generate the webhook secret token: %w", err)
	}
	hook, _, err := s.cfg.Keyring.ApplySecret(fieldWebhookSecret, stored, keyring.Replace(secret), now)
	return hook, secret, err
}

// setWebhook registers the webhook of the Telegram Connection publicID, as saved in q, with secret, in the interactive
// client class while the person waits, and returns the host of the webhook for webhookSet; a failure is a FieldError
// at /update_mode with the step's message.
func (s *Service) setWebhook(ctx context.Context, q Queries, publicID string, secret logging.Secret) (string, error) {
	row, err := s.row(ctx, q, publicID)
	if err != nil {
		return "", err
	}
	c, err := s.telegramClient(row)
	if err != nil {
		return "", err
	}
	hook := s.WebhookURL(publicID)
	if r := c.SetWebhook(ctx, outbound.ClassInteractive, hook, secret); !r.OK() {
		return "", &FieldError{Pointer: "/update_mode", Code: CodeWebhookCallFailed,
			Detail: "setWebhook failed: " + string(r.Outcome.Error)}
	}
	host := "?"
	if u, err := url.Parse(hook); err == nil && u.Host != "" {
		host = u.Host
	}
	return host, nil
}

// webhookSet logs telegram_webhook_set once the save that set the webhook at host committed; an empty host logs
// nothing.
func (s *Service) webhookSet(ctx context.Context, publicID, host string) {
	if host != "" {
		s.cfg.Log.Log(ctx, logging.TelegramWebhookSet, logging.F("connection", publicID), logging.F("host", host))
	}
}

// deleteWebhook removes the webhook of the Telegram Connection row as it is saved before the update that leaves the
// webhook mode — its bot and base URL — so that long polling works again; a failure is a FieldError at /update_mode
// with the step's message.
func (s *Service) deleteWebhook(ctx context.Context, row dbgen.GetConnectionRow) error {
	c, err := s.telegramClient(row)
	if err != nil {
		return err
	}
	if r := c.DeleteWebhook(ctx, outbound.ClassInteractive); !r.OK() {
		return &FieldError{Pointer: "/update_mode", Code: CodeWebhookCallFailed,
			Detail: "deleteWebhook failed: " + string(r.Outcome.Error)}
	}
	return nil
}

// checkTelegram runs the step-by-step check of the Telegram Connection row (C-14.FR-11) on the interactive path,
// limited by the Connection. With baseURL set only the dry probe runs, against that unsaved address and without the
// token. A getMe that passed records the bot's username and user id on the Connection.
func (s *Service) checkTelegram(ctx context.Context, row dbgen.GetConnectionRow, baseURL *string) (CheckResult,
	error) {
	c, err := s.telegramClient(row)
	if err != nil {
		return CheckResult{}, err
	}
	var probe *telegram.Client
	if baseURL != nil {
		base, err := telegram.ParseBaseURL(*baseURL)
		if err != nil {
			return CheckResult{}, &FieldError{Pointer: "/base_url", Code: CodeInvalidFormat,
				Detail: "The Bot API base URL is not an absolute http or https URL without user information, query or " +
					"fragment."}
		}
		st, err := s.telegramSettings(row.PublicID, base, keyring.StoredSecret{}, storedPassword(row), row.Proxy)
		if err != nil {
			return CheckResult{}, err
		}
		if probe, err = telegram.NewClient(telegram.Network(s.cfg.Network), st); err != nil {
			return CheckResult{}, err
		}
	}
	id := row.ID
	res, err := telegram.RunCheck(ctx, c, probe, telegram.Interactive(s.cfg.Interactive,
		delivery.Subject{Connection: &id}), s.cfg.Clocks.Real)
	if err != nil {
		return CheckResult{}, err
	}
	out := checkResultOf(res)
	if res.Bot == nil {
		return out, nil
	}
	if err := s.cfg.Store.InTx(ctx, func(q Queries) error {
		if err := q.SetBotIdentity(ctx, dbgen.SetBotIdentityParams{OrgID: s.cfg.OrgID, ID: row.ID,
			BotUserID: nonEmpty(strconv.FormatInt(res.Bot.ID, 10)), BotUsername: nonEmpty(res.Bot.Username)}); err != nil {
			return fmt.Errorf("record the bot of the connection %s: %w", row.PublicID, err)
		}
		return q.Notify(ctx, db.Hint{OrgID: s.cfg.OrgID, Type: Hint, ID: row.PublicID})
	}); err != nil {
		return CheckResult{}, err
	}
	return out, nil
}

// checkResultOf is the CheckResult of a Telegram check.
func checkResultOf(res telegram.Check) CheckResult {
	out := CheckResult{OK: res.OK(), Steps: make([]Step, 0, len(res.Steps))}
	for _, st := range res.Steps {
		out.Steps = append(out.Steps, Step{Name: st.Name, OK: st.OK, Skipped: st.Skipped, Latency: st.Latency,
			Via: st.Via, Message: st.Message})
	}
	if res.Bot != nil {
		out.BotName = res.Bot.Username
	}
	if res.Webhook != nil {
		set, pending := res.Webhook.URL != "", res.Webhook.PendingUpdateCount
		out.WebhookSet, out.PendingUpdates = &set, &pending
	}
	return out
}

// Polling lists the Telegram Connections in the long-polling mode for the Leader's poller (telegram.Source), each with
// its client, kept until the Connection changes, and its stored offset. A Connection whose secrets cannot be opened is
// left out, with telegram_poll_failed.
func (s *Service) Polling(ctx context.Context) ([]telegram.Polled, error) {
	rows, err := s.cfg.Store.ListPollingConnections(ctx, s.cfg.OrgID)
	if err != nil {
		return nil, fmt.Errorf("list the telegram connections to poll: %w", err)
	}
	out := make([]telegram.Polled, 0, len(rows))
	for _, r := range rows {
		c, err := s.cachedTelegramClient(r.ID, r.Version, func() (*telegram.Client, error) {
			st, err := s.telegramSettings(r.PublicID, r.TelegramBotApiBaseUrl.String,
				storedToken(dbgen.GetConnectionRow{BotTokenCiphertext: r.BotTokenCiphertext, BotTokenKeyID: r.BotTokenKeyID,
					BotTokenUpdatedAt: r.BotTokenUpdatedAt}),
				storedPassword(dbgen.GetConnectionRow{ProxyPasswordCiphertext: r.ProxyPasswordCiphertext,
					ProxyPasswordKeyID: r.ProxyPasswordKeyID, ProxyPasswordUpdatedAt: r.ProxyPasswordUpdatedAt}), r.Proxy)
			if err != nil {
				return nil, err
			}
			return telegram.NewClient(telegram.Network(s.cfg.Network), st)
		})
		if err != nil {
			s.cfg.Log.Log(ctx, logging.TelegramPollFailed, logging.F("connection", r.PublicID),
				logging.F("error", err.Error()))
			continue
		}
		out = append(out, telegram.Polled{Conn: telegram.Conn{ID: r.ID, PublicID: r.PublicID, Client: c},
			Version: r.Version, Offset: int8Of(r.TelegramUpdateOffset)})
	}
	return out, nil
}

// Hooked is the Telegram Connection publicID of a webhook request with its secret token (telegram.Hooks); a Connection
// that is unknown, deleted, not a Telegram one, not in the webhook mode or without a secret token is
// telegram.ErrNoWebhook.
func (s *Service) Hooked(ctx context.Context, publicID string) (telegram.Conn, logging.Secret, error) {
	row, err := s.row(ctx, s.cfg.Store, publicID)
	if errors.Is(err, ErrNotFound) {
		return telegram.Conn{}, "", telegram.ErrNoWebhook
	}
	if err != nil {
		return telegram.Conn{}, "", err
	}
	if row.Type != TypeTelegram || row.TelegramUpdateMode.String != ModeWebhook || !storedHook(row).Set() {
		return telegram.Conn{}, "", telegram.ErrNoWebhook
	}
	secret, err := s.cfg.Keyring.OpenSecret(fieldWebhookSecret, storedHook(row))
	if err != nil {
		return telegram.Conn{}, "", fmt.Errorf("open the webhook secret token of the connection %s: %w", row.PublicID,
			err)
	}
	c, err := s.cachedTelegramClient(row.ID, row.Version, func() (*telegram.Client, error) {
		return s.telegramClient(row)
	})
	if err != nil {
		return telegram.Conn{}, "", err
	}
	return telegram.Conn{ID: row.ID, PublicID: row.PublicID, Client: c}, secret, nil
}

// HandleOnce runs f for the update updateID of the Telegram Connection id unless its stored offset is past it, and then
// raises the offset to updateID+1, all in one transaction under the Connection's update lock (telegram.Offsets): a
// transaction advisory lock of the class db.TelegramUpdateLockClass keyed by the Connection, so that a second poller of
// a frozen old Leader, or a webhook request with the same update, waits for it and then skips the update. The offset
// is read without a row lock, and the final GREATEST update is the only statement that touches the connections row: a
// handler runs while a save of the Connection holds the row during setWebhook, and only that final write waits for the
// save; the delivery worker reschedules an edit of a Root message of the Connection while the lock is held. A save
// that sets a new bot
// token while a handler runs forgets the offset, and the final write leaves it forgotten. A deleted Connection runs
// nothing. An error of f stores nothing, so that the update comes again.
func (s *Service) HandleOnce(ctx context.Context, id, updateID int64, f func(context.Context) error) (bool, error) {
	ran := false
	err := s.cfg.Store.InTx(ctx, func(q Queries) error {
		if err := q.LockUpdates(ctx, dbgen.LockUpdatesParams{LockClass: db.TelegramUpdateLockClass,
			ID: id}); err != nil {
			return fmt.Errorf("lock the updates: %w", err)
		}
		o, err := q.GetUpdateOffset(ctx, dbgen.GetUpdateOffsetParams{OrgID: s.cfg.OrgID, ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read the update offset: %w", err)
		}
		if o.TelegramUpdateOffset.Valid && updateID < o.TelegramUpdateOffset.Int64 {
			return nil
		}
		if err := f(ctx); err != nil {
			return err
		}
		ran = true
		if err := q.StoreUpdateOffset(ctx, dbgen.StoreUpdateOffsetParams{OrgID: s.cfg.OrgID, ID: id,
			Next: updateID + 1, TokenUpdatedAt: o.BotTokenUpdatedAt}); err != nil {
			return fmt.Errorf("store the update offset: %w", err)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return ran, nil
}

// Outage reads when Muster was last known not to run, for the age of presses (telegram.Outages, C-14.FR-4): the
// Leader's alive mark and the latest downtime period it recorded.
func (s *Service) Outage(ctx context.Context) (telegram.Outage, error) {
	r, err := s.cfg.Store.GetOutage(ctx)
	if err != nil {
		return telegram.Outage{}, fmt.Errorf("read the latest downtime: %w", err)
	}
	return telegram.Outage{AliveAt: zeroless(r.AliveAt), DowntimeStart: zeroless(r.DowntimeStartedAt),
		DowntimeEnd: zeroless(r.DowntimeEndedAt)}, nil
}

// zeroless is t, or the zero time.Time for the zero time a query answers for none.
func zeroless(t time.Time) time.Time {
	if t.Year() <= 1 {
		return time.Time{}
	}
	return t
}

func int8Of(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

// unhook removes the webhook of the deleted Telegram Connection row, as it was saved before its deletion — its bot and
// base URL — when it was in the webhook update mode, so that Telegram stops sending its updates to Muster. It is a
// best effort after the deletion committed, in the interactive class, bounded by telegram.CallTimeout and not ended by
// a client that stops waiting: it never fails the deletion, and its outcome is logged with telegram_webhook_deleted
// or telegram_webhook_delete_failed, the error masked of the bot token. The client of the Connection is dropped.
func (s *Service) unhook(ctx context.Context, row dbgen.GetConnectionRow) {
	s.mu.Lock()
	delete(s.tgClients, row.ID)
	delete(s.clients, row.ID)
	s.mu.Unlock()
	if row.Type != TypeTelegram || row.TelegramUpdateMode.String != ModeWebhook {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), telegram.CallTimeout)
	defer cancel()
	c, err := s.telegramClient(row)
	detail := ""
	switch {
	case err != nil:
		detail = err.Error()
	default:
		if r := c.DeleteWebhook(ctx, outbound.ClassInteractive); !r.OK() {
			detail = string(r.Outcome.Error)
		}
	}
	if detail != "" {
		s.cfg.Log.Log(ctx, logging.TelegramWebhookDeleteFailed, logging.F("connection", row.PublicID),
			logging.F("error", detail))
		return
	}
	s.cfg.Log.Log(ctx, logging.TelegramWebhookDeleted, logging.F("connection", row.PublicID))
}

// TelegramTarget is where the Telegram Destination destinationID sends, for the adapter: its channel, the ids of the
// channel and of its discussion group that its Destination check learned, and the client of its Connection, kept until
// the Connection changes. A deleted Destination still has one, for its final edit. A Destination that is unknown, not a
// Telegram one, or whose Connection is deleted is telegram.ErrNoTarget.
func (s *Service) TelegramTarget(ctx context.Context, destinationID int64) (telegram.Target, error) {
	r, err := s.cfg.Store.GetTelegramDestinationTarget(ctx, dbgen.GetTelegramDestinationTargetParams{
		OrgID: s.cfg.OrgID, DestinationID: destinationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return telegram.Target{}, telegram.ErrNoTarget
	}
	if err != nil {
		return telegram.Target{}, fmt.Errorf("read the connection of the destination %d: %w", destinationID, err)
	}
	row := dbgen.GetConnectionRow{ID: r.ID, PublicID: r.PublicID, Type: r.Type, Name: r.Name,
		TelegramBotApiBaseUrl: r.TelegramBotApiBaseUrl, BotTokenCiphertext: r.BotTokenCiphertext,
		BotTokenKeyID: r.BotTokenKeyID, BotTokenUpdatedAt: r.BotTokenUpdatedAt, Proxy: r.Proxy,
		ProxyPasswordCiphertext: r.ProxyPasswordCiphertext, ProxyPasswordKeyID: r.ProxyPasswordKeyID,
		ProxyPasswordUpdatedAt: r.ProxyPasswordUpdatedAt, Version: r.Version}
	c, err := s.cachedTelegramClient(r.ID, r.Version, func() (*telegram.Client, error) { return s.telegramClient(row) })
	if errors.Is(err, errNotTelegram) {
		return telegram.Target{}, telegram.ErrNoTarget
	}
	if err != nil {
		return telegram.Target{}, err
	}
	return telegram.Target{Client: c, ConnectionID: r.ID, Channel: r.TelegramChannelID.String,
		ChannelID: r.TelegramChannelChatID.Int64, GroupID: r.TelegramDiscussionChatID.Int64}, nil
}

// CheckTelegramChannel runs the Destination check of a Telegram Destination being saved or checked, for the
// Destination write path (C-14.FR-2, FR-14): through the Telegram Connection that the check names, on the interactive
// path, limited by the Destination when it exists and by the Connection otherwise. A Connection that is missing,
// deleted or not a Telegram one is destinations.ErrUnknownConnection.
func (s *Service) CheckTelegramChannel(ctx context.Context, in destinations.TelegramChannelCheck) (
	destinations.TelegramChannelChecked, error) {
	row, err := s.row(ctx, s.cfg.Store, in.Connection)
	if errors.Is(err, ErrNotFound) || (err == nil && row.Type != TypeTelegram) {
		return destinations.TelegramChannelChecked{}, destinations.ErrUnknownConnection
	}
	if err != nil {
		return destinations.TelegramChannelChecked{}, err
	}
	c, err := s.cachedTelegramClient(row.ID, row.Version, func() (*telegram.Client, error) {
		return s.telegramClient(row)
	})
	if err != nil {
		return destinations.TelegramChannelChecked{}, err
	}
	conn := row.ID
	subject := delivery.Subject{Connection: &conn}
	if in.Destination != nil {
		subject = delivery.Subject{Destination: &delivery.Destination{ID: *in.Destination, Type: TypeTelegram,
			Connection: &conn}}
	}
	res, err := telegram.CheckDestination(ctx, c, telegram.Interactive(s.cfg.Interactive, subject), in.ChannelID)
	if err != nil {
		return destinations.TelegramChannelChecked{}, err
	}
	return destinations.TelegramChannelChecked{ConnectionID: row.ID, Check: res}, nil
}

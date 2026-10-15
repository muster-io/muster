// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package accountlinks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// fakeTG is the Telegram bot of a Connection without a stored username.
type fakeTG struct {
	username string
	err      error
	calls    []string
}

func (f *fakeTG) Username(_ context.Context, id string) (string, error) {
	f.calls = append(f.calls, id)
	return f.username, f.err
}

// replies records the bot's replies, failing when err is set.
type replies struct {
	texts []string
	err   error
}

func (r *replies) reply(_ context.Context, text string) error {
	r.texts = append(r.texts, text)
	return r.err
}

func (r *replies) last() string {
	if len(r.texts) == 0 {
		return ""
	}
	return r.texts[len(r.texts)-1]
}

var deepLink = regexp.MustCompile(`^https://t\.me/muster_dev_bot\?start=([A-Za-z0-9_-]{43})$`)

const invalidText = "This link is invalid or expired. Start again from your Muster profile."

// start is a started Telegram link of r through the Telegram Connection of the fixtures, and its token.
func (e *env) start(t *testing.T, r Requester) string {
	t.Helper()
	st, err := e.s.StartTelegram(t.Context(), r, tgPID)
	if err != nil {
		t.Fatal(err)
	}
	i := strings.LastIndex(st.DeepLink, "=")
	return st.DeepLink[i+1:]
}

func (e *env) slash(t *testing.T, connID int64, connPID, token string, from TelegramAccount, r *replies) {
	t.Helper()
	if err := e.s.LinkTelegram(t.Context(), TelegramConnection{ID: connID, PublicID: connPID}, token, from,
		r.reply); err != nil {
		t.Fatal(err)
	}
}

// lastEntry is the latest Audit log entry.
func (e *env) lastEntry() entry {
	got := entries(e.store)
	if len(got) == 0 {
		return entry{}
	}
	return got[len(got)-1]
}

// TestStartTelegram is C-18.FR-2: the deep link carries a token of 32 random bytes in base64url, 43 characters, of
// which only the SHA-256 is stored with the User, the Connection and an expiry of account_link.telegram_token_ttl;
// the bot's username is the stored one or, without one, getMe's, and a getMe that fails stores nothing. Only a
// Telegram Connection that is not deleted can be named.
func TestStartTelegram(t *testing.T) {
	e := newEnv(t)
	st, err := e.s.StartTelegram(t.Context(), alice, strings.ToLower(tgPID))
	if err != nil {
		t.Fatal(err)
	}
	m := deepLink.FindStringSubmatch(st.DeepLink)
	if m == nil || !st.ExpiresAt.Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("StartTelegram = %+v", st)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(m[1])
	sum := sha256.Sum256([]byte(m[1]))
	r := e.store.reqs[0]
	if len(raw) != 32 || len(e.store.reqs) != 1 || !bytes.Equal(r.hash, sum[:]) || r.userID != aliceID ||
		r.connID != tgID || r.messenger != MessengerTelegram || !r.expires.Equal(st.ExpiresAt) ||
		!strings.HasPrefix(r.publicID, "AR") || len(e.tg.calls) != 0 {
		t.Fatalf("stored %+v", r)
	}
	if strings.Contains(e.log.String(), m[1]) {
		t.Fatal("the token reached the log")
	}
	st, err = e.s.StartTelegram(t.Context(), alice, tg2PID)
	if err != nil || !strings.HasPrefix(st.DeepLink, "https://t.me/fallback_bot?start=") ||
		len(e.tg.calls) != 1 || e.tg.calls[0] != tg2PID {
		t.Fatalf("a Connection without a stored username = %+v, %v", st, err)
	}
	e.tg.err = &MessengerError{Call: "getMe"}
	if _, err := e.s.StartTelegram(t.Context(), alice, tg2PID); !errors.As(err, new(*MessengerError)) ||
		len(e.store.reqs) != 2 {
		t.Errorf("a failed getMe = %v, %d requests", err, len(e.store.reqs))
	}
	for _, id := range []string{mmPID, gonePID, "CNAAAAAAAAAAZZ", "nope"} {
		var fe *FieldError
		if _, err := e.s.StartTelegram(t.Context(), alice, id); !errors.As(err, &fe) || fe.Pointer != "/connection_id" ||
			fe.Code != CodeUnknownID {
			t.Errorf("StartTelegram(%s) = %v", id, err)
		}
	}
	for name, fail := range map[string]func(){
		"GetLinkConnection": func() { e.store.fail["GetLinkConnection"] = errBoom },
		"InsertLinkRequest": func() { e.store.fail["InsertLinkRequest"] = errBoom },
		"random":            func() { e.s.cfg.Random = bytes.NewReader(nil) },
	} {
		fail()
		if _, err := e.s.StartTelegram(t.Context(), alice, tgPID); err == nil {
			t.Errorf("a failed %s stored a request", name)
		}
		e.store.fail = map[string]error{}
		e.s.cfg.Random = &counter{}
	}
}

// TestLinkTelegram is C-18.FR-2, AC-1 and FR-9: `/start <token>` links the account that sent it, the bot replies with
// the User's name and login, the link is recorded and the hint that names it goes to the User; the same token again
// links nothing, is answered that the link is invalid or expired and recorded as link_rejected with token_used, as
// are an unknown token, an expired one and one of another Connection.
func TestLinkTelegram(t *testing.T) {
	e := newEnv(t)
	tok := e.start(t, alice)
	var r replies
	e.slash(t, tgID, tgPID, tok, TelegramAccount{ID: 7001, Username: "alice_t"}, &r)
	if r.last() != "Linked to Muster user Alice Smith (alice)" || len(e.store.links) != 1 ||
		e.store.links[0].external != "7001" || *e.store.links[0].username != "alice_t" ||
		e.store.links[0].userID != aliceID || e.store.reqs[0].consumed == nil {
		t.Fatalf("reply %q, links %+v", r.last(), e.store.links)
	}
	if got := e.lastEntry(); got.Action != ActionLink || got.Actor != "user:1" || got.Transport != "telegram" ||
		got.Address != "" || got.Details["external_id"] != "7001" || got.Details["username"] != "alice_t" ||
		got.Details["connection"] != tgPID || got.Details["messenger"] != MessengerTelegram || len(got.Diff) != 4 {
		t.Fatalf("entry %+v", got)
	}
	if len(e.store.hints) != 1 || e.store.hints[0].User != alicePID || e.store.hints[0].Type != "account-links" ||
		e.store.hints[0].ID != e.store.links[0].publicID {
		t.Errorf("hints %+v", e.store.hints)
	}
	if !strings.Contains(e.log.String(), `"event":"account_link_linked","messenger":"telegram","connection":"`+tgPID+
		`","user":"`+alicePID+`"}`) || strings.Contains(e.log.String(), tok) {
		t.Errorf("log %s", e.log)
	}
	// C-18.AC-1: the token works once.
	e.slash(t, tgID, tgPID, tok, TelegramAccount{ID: 7002, Username: "mallory"}, &r)
	got := e.lastEntry()
	if r.last() != invalidText || len(e.store.links) != 1 || got.Action != ActionLinkRejected ||
		got.Details["reason"] != ReasonTokenUsed || got.Details["external_id"] != "7002" ||
		got.Details["user"] != alicePID || got.Actor != "system" || got.Transport != "telegram" ||
		got.ResourceType != "connection" || got.Resource != tgPID {
		t.Fatalf("a used token: reply %q, entry %+v", r.last(), got)
	}
	if !strings.Contains(e.log.String(), `"event":"account_link_rejected","messenger":"telegram","connection":"`+
		tgPID+`","reason":"token_used"`) {
		t.Errorf("log %s", e.log)
	}
	// An unknown token, in English whoever sends it.
	e.slash(t, tgID, tgPID, "no-such-token", TelegramAccount{ID: 7002}, &r)
	if got := e.lastEntry(); r.last() != invalidText || got.Details["reason"] != ReasonTokenUnknown ||
		got.Details["user"] != nil || got.Details["username"] != nil {
		t.Errorf("an unknown token: %q, %+v", r.last(), got)
	}
	// An expired token.
	tok = e.start(t, alice)
	e.clock.Advance(10*time.Minute + time.Second)
	e.slash(t, tgID, tgPID, tok, TelegramAccount{ID: 7001}, &r)
	if got := e.lastEntry(); r.last() != invalidText || got.Details["reason"] != ReasonTokenExpired {
		t.Errorf("an expired token: %q, %+v", r.last(), got)
	}
	// A token of another Connection is not consumed: it still works with its own bot.
	tok = e.start(t, alice)
	e.slash(t, tg2ID, tg2PID, tok, TelegramAccount{ID: 7004, Username: "alice_new"}, &r)
	if got := e.lastEntry(); r.last() != invalidText || got.Details["reason"] != ReasonWrongConnection ||
		got.Resource != tg2PID || e.store.reqs[len(e.store.reqs)-1].consumed != nil {
		t.Errorf("another Connection: %q, %+v", r.last(), got)
	}
	// C-18.FR-4: a new Telegram link of the User replaces the previous one.
	before := len(e.store.audit)
	e.slash(t, tgID, tgPID, tok, TelegramAccount{ID: 7004, Username: "alice_new"}, &r)
	all := entries(e.store)[before:]
	if len(e.store.links) != 1 || e.store.links[0].external != "7004" || len(all) != 2 ||
		all[0].Action != ActionUnlink || all[0].Details["reason"] != ReasonReplaced ||
		all[0].Details["external_id"] != "7001" || all[1].Action != ActionLink ||
		all[1].Details["replaced"] != all[0].Resource {
		t.Fatalf("a replacement: links %+v, entries %+v", e.store.links, all)
	}
	if !strings.Contains(e.log.String(), `"event":"account_link_unlinked","messenger":"telegram","connection":"`+tgPID+
		`","user":"`+alicePID+`","reason":"replaced"`) {
		t.Errorf("log %s", e.log)
	}
}

// TestLinkSameAccount is C-18.FR-4 for an account linked again by its own User: the link is kept with its id, its
// username and Connection become the new ones, the request is consumed, account_link.link is recorded once with what
// changed and nothing is unlinked or replaced; the hint names the kept link and the bot replies as for a link. A
// Mattermost code for the account already linked does the same.
func TestLinkSameAccount(t *testing.T) {
	e := newEnv(t)
	used := t0.Add(-time.Hour)
	l := e.addLink(aliceID, MessengerTelegram, tgID, "7001", ptr("alice_t"))
	e.store.links[0].lastUsed = &used
	st, err := e.s.StartTelegram(t.Context(), alice, tg2PID)
	if err != nil {
		t.Fatal(err)
	}
	tok := st.DeepLink[strings.LastIndex(st.DeepLink, "=")+1:]
	var r replies
	e.slash(t, tg2ID, tg2PID, tok, TelegramAccount{ID: 7001, Username: "alice_t2"}, &r)
	got := entries(e.store)
	if r.last() != "Linked to Muster user Alice Smith (alice)" || len(e.store.links) != 1 ||
		e.store.links[0].publicID != l.publicID || *e.store.links[0].username != "alice_t2" ||
		e.store.links[0].connID != tg2ID || e.store.reqs[0].consumed == nil || len(e.store.hints) != 1 ||
		e.store.hints[0].ID != l.publicID {
		t.Fatalf("reply %q, links %+v, hints %+v", r.last(), e.store.links, e.store.hints)
	}
	if len(got) != 1 || got[0].Action != ActionLink || got[0].Resource != l.publicID || len(got[0].Diff) != 2 ||
		got[0].Details["replaced"] != nil || got[0].Details["username"] != "alice_t2" ||
		got[0].Details["connection"] != tg2PID {
		t.Fatalf("entries %+v", got)
	}
	if !strings.Contains(e.log.String(), `"event":"account_link_linked","messenger":"telegram","connection":"`+
		tg2PID+`","user":"`+alicePID+`"}`) || strings.Contains(e.log.String(), "account_link_unlinked") {
		t.Errorf("log %s", e.log)
	}
	e.addLink(aliceID, MessengerMattermost, mmID, "u-dana", ptr("dana"))
	ms := e.startMM(t, alice, "dana")
	ml, err := e.s.ConfirmMattermost(t.Context(), alice, ms.RequestPublicID, e.mm.code(t))
	got = entries(e.store)
	if err != nil || ml.PublicID != e.store.links[1].publicID || len(e.store.links) != 2 || ml.LastUsedAt != nil ||
		!ml.CreatedAt.Equal(t0) || len(got) != 2 || got[1].Action != ActionLink || len(got[1].Diff) != 0 ||
		e.mm.sent[len(e.mm.sent)-1].text != "Linked to Muster user Alice Smith" {
		t.Fatalf("Confirm = %+v, %v; entries %+v", ml, err, got)
	}
	e.store.fail["UpdateAccountLink"] = errBoom
	tok = e.start(t, alice)
	if err := e.s.LinkTelegram(t.Context(), TelegramConnection{ID: tgID, PublicID: tgPID}, tok,
		TelegramAccount{ID: 7001}, r.reply); !errors.Is(err, errBoom) || *e.store.links[0].username != "alice_t2" ||
		len(e.store.audit) != 2 {
		t.Errorf("a failed update = %v, links %+v", err, e.store.links)
	}
}

// TestLinkTelegramConflict is C-18.FR-5: an account linked to another User is refused with the reply that it is linked
// to another user and recorded as link_rejected with conflict; the request stays usable, and the bot answers in the
// User's language.
func TestLinkTelegramConflict(t *testing.T) {
	e := newEnv(t)
	e.addLink(aliceID, MessengerTelegram, tgID, "7001", ptr("alice_t"))
	tok := e.start(t, bob)
	var r replies
	e.slash(t, tgID, tgPID, tok, TelegramAccount{ID: 7001, Username: "alice_t"}, &r)
	if got := e.lastEntry(); r.last() != "Эта учётная запись связана с другим пользователем. Отвязать её может "+
		"владелец или администратор." || got.Details["reason"] != ReasonConflict || got.Details["user"] != bobPID ||
		e.store.reqs[0].consumed != nil || e.store.reqs[0].voided != nil || len(e.store.links) != 1 ||
		e.store.links[0].userID != aliceID {
		t.Fatalf("a conflict: %q, %+v, links %+v", r.last(), got, e.store.links)
	}
	// A link without a username, that a reply failing to arrive leaves in place.
	r.err = &MessengerError{Call: "the reply", Outcome: "fatal", Status: 403}
	e.slash(t, tgID, tgPID, tok, TelegramAccount{ID: 7002}, &r)
	if r.last() != "Учётная запись связана с пользователем Muster Bob (bob)" || len(e.store.links) != 2 ||
		e.store.links[1].username != nil {
		t.Fatalf("reply %q, links %+v", r.last(), e.store.links)
	}
	if !strings.Contains(e.log.String(), `"event":"account_link_linked","messenger":"telegram","connection":"`+tgPID+
		`","user":"`+bobPID+`","error":"the messenger refused the reply (fatal, status 403)"`) {
		t.Errorf("log %s", e.log)
	}
	e.slash(t, tgID, tgPID, "x", TelegramAccount{ID: 7002}, &r)
	if !strings.Contains(e.log.String(), `"reason":"token_unknown","error":"the messenger refused the reply`) {
		t.Errorf("log %s", e.log)
	}
}

// TestLinkTelegramFailures: a database failure links nothing, replies nothing and leaves the update to come again.
func TestLinkTelegramFailures(t *testing.T) {
	for _, name := range []string{"GetLinkConnection", "GetTelegramRequestByHash", "LockLinkUser", "InsertAuditEntry",
		"GetAccountLinkBySpace", "GetUserLinkInSpace", "DeleteAccountLink", "InsertAccountLink", "ConsumeLinkRequest",
		"Notify"} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.addLink(aliceID, MessengerTelegram, tgID, "7000", nil)
			tok := e.start(t, alice)
			e.store.fail[name] = errBoom
			var r replies
			err := e.s.LinkTelegram(t.Context(), TelegramConnection{ID: tgID, PublicID: tgPID}, tok,
				TelegramAccount{ID: 7001}, r.reply)
			if !errors.Is(err, errBoom) || len(r.texts) != 0 || len(e.store.links) != 1 ||
				e.store.links[0].external != "7000" || len(e.store.audit) != 0 {
				t.Errorf("= %v, replies %v, links %+v", err, r.texts, e.store.links)
			}
		})
	}
}

// TestLinkTelegramConcurrentConflict is C-18.FR-5 for an account that another User linked after the check: the insert
// fails on account_links_account_key, the savepoint rolls back the replacement of the User's previous link, and the
// link is refused as a conflict — the reply, link_rejected with conflict, the request kept and the update confirmed.
// A failure on the other unique constraint, which the lock of the User rules out, stays an error.
func TestLinkTelegramConcurrentConflict(t *testing.T) {
	e := newEnv(t)
	prev := e.addLink(aliceID, MessengerTelegram, tgID, "7000", ptr("alice_old"))
	tok := e.start(t, alice)
	e.store.fail["InsertAccountLink"] = fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: uniqueViolation,
		ConstraintName: accountKey})
	var r replies
	e.slash(t, tgID, tgPID, tok, TelegramAccount{ID: 7001, Username: "taken"}, &r)
	got := entries(e.store)
	if r.last() != "This account is linked to another user. Its owner or an admin can unlink it." ||
		len(got) != 1 || got[0].Action != ActionLinkRejected || got[0].Details["reason"] != ReasonConflict ||
		got[0].Details["user"] != alicePID || got[0].Details["external_id"] != "7001" ||
		e.store.reqs[0].consumed != nil || e.store.reqs[0].voided != nil || len(e.store.hints) != 0 ||
		len(e.store.links) != 1 || e.store.links[0] != prev || e.store.savepoints != 1 {
		t.Fatalf("a concurrent conflict: %q, entries %+v, links %+v", r.last(), got, e.store.links)
	}
	if !strings.Contains(e.log.String(), `"event":"account_link_rejected","messenger":"telegram","connection":"`+
		tgPID+`","reason":"conflict"`) || strings.Contains(e.log.String(), "account_link_unlinked") {
		t.Errorf("log %s", e.log)
	}
	e.store.fail["InsertAccountLink"] = &pgconn.PgError{Code: uniqueViolation,
		ConstraintName: "account_links_user_space_key"}
	err := e.s.LinkTelegram(t.Context(), TelegramConnection{ID: tgID, PublicID: tgPID}, tok,
		TelegramAccount{ID: 7001}, r.reply)
	if pe, ok := errors.AsType[*pgconn.PgError](err); !ok || pe.ConstraintName != "account_links_user_space_key" ||
		len(e.store.audit) != 1 || len(r.texts) != 1 {
		t.Errorf("the other constraint = %v", err)
	}
	delete(e.store.fail, "InsertAccountLink")
	e.slash(t, tgID, tgPID, tok, TelegramAccount{ID: 7001}, &r)
	if r.last() != "Linked to Muster user Alice Smith (alice)" || len(e.store.links) != 1 ||
		e.store.links[0].external != "7001" || e.store.reqs[0].consumed == nil {
		t.Errorf("the kept request links: %q, links %+v", r.last(), e.store.links)
	}
}

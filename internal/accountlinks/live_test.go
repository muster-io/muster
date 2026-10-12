// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package accountlinks_test

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/accountlinks"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

var t0 = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

// TestLiveLookup reads Account links from PostgreSQL: a Mattermost account in the identity space of its Connection,
// not in another one; a disabled User with its status; a deleted User and an unknown account are not linked.
func TestLiveLookup(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		conn := config.Database{URL: logging.Secret(s.NewDatabase(t)), SSLMode: "disable"}
		d, err := db.Open(ctx, conn, conn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(d.Close)
		logger := logging.New(&bytes.Buffer{}, logging.LevelInfo)
		if err := d.Migrate(ctx, logger); err != nil {
			t.Fatal(err)
		}
		if err := organization.Ensure(ctx, organization.NewStore(d.Pool), logger, t0); err != nil {
			t.Fatal(err)
		}
		org, err := organization.NewStore(d.Pool).GetOrganization(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var connID int64
		if err := d.Pool.QueryRow(ctx, `INSERT INTO connections (org_id, public_id, type, name, mattermost_server_url,
			bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds,
			created_at, updated_at) VALUES ($1, 'CNAAAAAAAAAAA1', 'mattermost', 'mm', 'https://mm.example.org', '\x00',
			'k1', $2, 10, 1, $2, $2) RETURNING id`, org.ID, t0).Scan(&connID); err != nil {
			t.Fatal(err)
		}
		users := map[string]int64{}
		for i, u := range []struct{ login, status string }{{"bob", "active"}, {"carol", "disabled"},
			{"dave", "deleted"}} {
			var deleted *time.Time
			if u.status == "deleted" {
				deleted = &t0
			}
			var id int64
			if err := d.Pool.QueryRow(ctx, `INSERT INTO users (org_id, public_id, login, name, role, source, status,
				deleted_at, created_at, updated_at) VALUES ($1, $2, $3, $3, 'responder', 'local', $4, $5, $6, $6)
				RETURNING id`, org.ID, "SRAAAAAAAAAAB"+string(rune('1'+i)), u.login, u.status, deleted,
				t0).Scan(&id); err != nil {
				t.Fatal(err)
			}
			users[u.login] = id
			if _, err := d.Pool.Exec(ctx, `INSERT INTO account_links (org_id, public_id, user_id, messenger,
				connection_id, external_id, username, created_at) VALUES ($1, $2, $3, 'mattermost', $4, $5, $6, $7)`,
				org.ID, "AKAAAAAAAAAAB"+string(rune('1'+i)), id, connID, "u-"+u.login, u.login, t0); err != nil {
				t.Fatal(err)
			}
		}
		links := accountlinks.New(accountlinks.Config{OrgID: org.ID, Store: accountlinks.NewStore(d.Pool),
			Business: clock.NewManual(t0), Log: logger})
		space := accountlinks.SpaceMattermost(connID)
		u, err := links.Lookup(ctx, space, "u-bob")
		if err != nil || u.ID != users["bob"] || u.PublicID != "SRAAAAAAAAAAB1" || u.Login != "bob" ||
			u.Role != "responder" || u.Status != accountlinks.StatusActive {
			t.Fatalf("Lookup(bob) = %+v, %v", u, err)
		}
		if u, err := links.Lookup(ctx, space, "u-carol"); err != nil || u.Status != accountlinks.StatusDisabled {
			t.Errorf("Lookup(carol) = %+v, %v", u, err)
		}
		for _, c := range [][2]string{{space, "u-dave"}, {space, "u-erin"}, {accountlinks.SpaceMattermost(connID + 1),
			"u-bob"}, {accountlinks.SpaceTelegram, "u-bob"}} {
			if _, err := links.Lookup(ctx, c[0], c[1]); !errors.Is(err, accountlinks.ErrNotLinked) {
				t.Errorf("Lookup(%s, %s) = %v, want ErrNotLinked", c[0], c[1], err)
			}
		}
		other := accountlinks.New(accountlinks.Config{OrgID: org.ID + 1, Store: accountlinks.NewStore(d.Pool),
			Business: clock.NewManual(t0), Log: logger})
		if _, err := other.Lookup(ctx, space, "u-bob"); !errors.Is(err, accountlinks.ErrNotLinked) {
			t.Errorf("another Organization = %v", err)
		}
	})
}

// liveBot is the Mattermost bot of TestLiveLinks: it knows bob and dana, or anyone with all, and keeps the direct
// messages.
type liveBot struct {
	mu   sync.Mutex
	all  bool
	sent []string
}

func (b *liveBot) Session(ctx context.Context, _ string, f func(context.Context, accountlinks.MattermostBot) error) error {
	return f(ctx, b)
}

func (b *liveBot) FindUser(_ context.Context, username string) (accountlinks.MattermostAccount, error) {
	if !b.all && username != "bob" && username != "dana" {
		return accountlinks.MattermostAccount{}, accountlinks.ErrMessengerUserNotFound
	}
	return accountlinks.MattermostAccount{ID: "u-" + username, Username: username}, nil
}

func (b *liveBot) SendDirect(_ context.Context, _, text string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sent = append(b.sent, text)
	return nil
}

var codeRe = regexp.MustCompile(`[2-9A-HJKMNP-Z]{8}`)

// TestLiveLinks runs every query of linking on PostgreSQL: Telegram deep links found by their hash and consumed once,
// replacement in an identity space, a conflict with the unique account, Mattermost codes with their attempts and
// request counts, the lists, unlinking with the Audit log and the hint, the removal of a deleted User's links in the
// transaction of the deletion, the last use, and the pruning of expired requests.
func TestLiveLinks(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		conn := config.Database{URL: logging.Secret(s.NewDatabase(t)), SSLMode: "disable"}
		d, err := db.Open(ctx, conn, conn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(d.Close)
		var log bytes.Buffer
		logger := logging.New(&log, logging.LevelInfo)
		if err := d.Migrate(ctx, logger); err != nil {
			t.Fatal(err)
		}
		if err := organization.Ensure(ctx, organization.NewStore(d.Pool), logger, t0); err != nil {
			t.Fatal(err)
		}
		org, err := organization.NewStore(d.Pool).GetOrganization(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Pool.Exec(ctx, `CREATE TABLE audit_log_p202610 PARTITION OF audit_log
			FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2026-11-01 00:00:00+00')`); err != nil {
			t.Fatal(err)
		}
		conns := map[string]int64{}
		for _, c := range []struct{ id, typ, name string }{{"CNAAAAAAAAAAT1", "telegram", "Dev Telegram"},
			{"CNAAAAAAAAAAT2", "telegram", "Second"}, {"CNAAAAAAAAAAM1", "mattermost", "mm"}} {
			var id int64
			if err := d.Pool.QueryRow(ctx, `INSERT INTO connections (org_id, public_id, type, name,
				mattermost_server_url, telegram_bot_api_base_url, telegram_update_mode, bot_token_ciphertext,
				bot_token_key_id, bot_token_updated_at, bot_username, limiter_limit, limiter_per_seconds, created_at,
				updated_at) VALUES ($1, $2, $3, $4, CASE WHEN $3 = 'mattermost' THEN 'https://mm.example.org' END,
				CASE WHEN $3 = 'telegram' THEN 'https://api.telegram.org' END,
				CASE WHEN $3 = 'telegram' THEN 'long_polling' END, '\x00', 'k1', $5, 'muster_dev_bot', 10, 1, $5, $5)
				RETURNING id`, org.ID, c.id, c.typ, c.name, t0).Scan(&id); err != nil {
				t.Fatal(err)
			}
			conns[c.id] = id
		}
		users := map[string]int64{}
		for i, login := range []string{"alice", "bob"} {
			var id int64
			if err := d.Pool.QueryRow(ctx, `INSERT INTO users (org_id, public_id, login, name, role, source, status,
				created_at, updated_at) VALUES ($1, $2, $3, $3, 'responder', 'local', 'active', $4, $4) RETURNING id`,
				org.ID, "SRAAAAAAAAAAC"+string(rune('1'+i)), login, t0).Scan(&id); err != nil {
				t.Fatal(err)
			}
			users[login] = id
		}
		business := clock.NewManual(t0)
		bot := &liveBot{}
		svc := accountlinks.New(accountlinks.Config{OrgID: org.ID, Store: accountlinks.NewStore(d.Pool),
			Audit: audit.NewWriter(logger, business), Business: business, Log: logger, Mattermost: bot})
		as := func(login string) accountlinks.Requester {
			return accountlinks.Requester{UserID: users[login], UserPublicID: "SRAAAAAAAAAAC" +
				map[string]string{"alice": "1", "bob": "2"}[login], Actor: audit.User(users[login], "x"),
				Transport: audit.TransportUI, Address: netip.MustParseAddr("192.0.2.1")}
		}
		count := func(q string, args ...any) int {
			var n int
			if err := d.Pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		token := func(r accountlinks.Requester, c string) string {
			st, err := svc.StartTelegram(ctx, r, c)
			if err != nil {
				t.Fatal(err)
			}
			_, tok, _ := strings.Cut(st.DeepLink, "?start=")
			return tok
		}
		var replies []string
		var replyErr error
		reply := func(_ context.Context, text string) error { replies = append(replies, text); return replyErr }
		slash := func(c, tok string, from int64, username string) {
			t.Helper()
			if err := svc.LinkTelegram(ctx, accountlinks.TelegramConnection{ID: conns[c], PublicID: c}, tok,
				accountlinks.TelegramAccount{ID: from, Username: username}, reply); err != nil {
				t.Fatal(err)
			}
		}
		// Telegram: a link, the same token again, a token of another Connection, a replacement, a conflict.
		tok := token(as("alice"), "CNAAAAAAAAAAT1")
		slash("CNAAAAAAAAAAT1", tok, 7001, "alice_t")
		slash("CNAAAAAAAAAAT1", tok, 7002, "mallory")
		other := token(as("alice"), "CNAAAAAAAAAAT1")
		slash("CNAAAAAAAAAAT2", other, 7004, "alice_new")
		slash("CNAAAAAAAAAAT1", other, 7004, "alice_new")
		slash("CNAAAAAAAAAAT1", token(as("bob"), "CNAAAAAAAAAAT1"), 7004, "x")
		if len(replies) != 5 || replies[0] != "Linked to Muster user alice (alice)" ||
			replies[1] != "This link is invalid or expired. Start again from your Muster profile." ||
			replies[3] != replies[0] || !strings.HasPrefix(replies[4], "This account is linked to another user.") {
			t.Fatalf("replies %q", replies)
		}
		if n := count(`SELECT count(*) FROM account_links WHERE external_id = '7004' AND username = 'alice_new'`); n != 1 ||
			count(`SELECT count(*) FROM account_links`) != 1 {
			t.Fatalf("links after the replacement: %d", n)
		}
		if n := count(`SELECT count(*) FROM audit_log WHERE action = 'account_link.link_rejected' AND
			details->>'reason' IN ('token_used', 'wrong_connection', 'conflict')`); n != 3 {
			t.Errorf("rejections %d", n)
		}
		// The same account linked again keeps its link, with the new username, and replaces nothing.
		var kept string
		if err := d.Pool.QueryRow(ctx, `SELECT public_id FROM account_links`).Scan(&kept); err != nil {
			t.Fatal(err)
		}
		slash("CNAAAAAAAAAAT1", token(as("alice"), "CNAAAAAAAAAAT1"), 7004, "alice_newer")
		if count(`SELECT count(*) FROM account_links WHERE public_id = $1 AND username = 'alice_newer'`, kept) != 1 ||
			count(`SELECT count(*) FROM account_links`) != 1 || count(`SELECT count(*) FROM audit_log WHERE
			action = 'account_link.unlink'`) != 1 || count(`SELECT count(*) FROM audit_log WHERE
			action = 'account_link.link' AND resource_public_id = $1`, kept) != 2 {
			t.Errorf("the same account linked again")
		}
		// Mattermost: five wrong codes void the request, then a new request links with its code.
		started, err := svc.StartMattermost(ctx, as("bob"), "CNAAAAAAAAAAM1", "@Bob")
		if err != nil {
			t.Fatal(err)
		}
		code := codeRe.FindString(bot.sent[0])
		for range 5 {
			if _, err := svc.ConfirmMattermost(ctx, as("bob"), started.RequestPublicID, "22222222"); err == nil {
				t.Fatal("a wrong code linked")
			}
		}
		if _, err := svc.ConfirmMattermost(ctx, as("bob"), started.RequestPublicID, code); err == nil ||
			count(`SELECT count(*) FROM account_link_requests WHERE attempts = 5 AND voided_at IS NOT NULL`) != 1 {
			t.Fatalf("the sixth attempt = %v", err)
		}
		started, err = svc.StartMattermost(ctx, as("bob"), "CNAAAAAAAAAAM1", "bob")
		if err != nil {
			t.Fatal(err)
		}
		l, err := svc.ConfirmMattermost(ctx, as("bob"), started.RequestPublicID, strings.ToLower(codeRe.FindString(
			bot.sent[len(bot.sent)-1])))
		if err != nil || l.ExternalID != "u-bob" || l.ConnectionName != "mm" {
			t.Fatalf("Confirm = %+v, %v", l, err)
		}
		if _, err := svc.StartMattermost(ctx, as("alice"), "CNAAAAAAAAAAM1", "bob"); !errors.Is(err,
			accountlinks.ErrLinkedElsewhere) {
			t.Errorf("a conflict = %v", err)
		}
		for range 2 {
			if _, err := svc.StartMattermost(ctx, as("alice"), "CNAAAAAAAAAAM1", "dana"); err != nil {
				t.Fatal(err)
			}
		}
		var limited *accountlinks.RateLimitedError
		if _, err := svc.StartMattermost(ctx, as("bob"), "CNAAAAAAAAAAM1", "dana"); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.StartMattermost(ctx, as("bob"), "CNAAAAAAAAAAM1", "dana"); !errors.As(err, &limited) ||
			limited.Seconds() != 3600 {
			t.Errorf("the fourth request for dana = %v", err)
		}
		if n := count(`SELECT count(*) FROM account_link_requests WHERE user_id = $1 AND voided_at IS NULL AND
			consumed_at IS NULL`, users["alice"]); n != 1 {
			t.Errorf("alice's pending requests %d", n)
		}
		// Lists, the Connections to link through, the last use.
		mine, err := svc.List(ctx, users["alice"])
		if err != nil || len(mine) != 1 || mine[0].ExternalID != "7004" || mine[0].ConnectionName != "Dev Telegram" {
			t.Fatalf("List = %+v, %v", mine, err)
		}
		cs, err := svc.Connections(ctx)
		if err != nil || len(cs) != 3 || cs[0].Name != "mm" || cs[1].Name != "Dev Telegram" {
			t.Errorf("Connections = %+v, %v", cs, err)
		}
		if _, err := svc.Lookup(ctx, accountlinks.SpaceTelegram, "7004"); err != nil ||
			count(`SELECT count(*) FROM account_links WHERE last_used_at = $1`, t0) != 1 {
			t.Errorf("Lookup = %v", err)
		}
		// Unlinking by the User, with the hint in its transaction; the deletion of bob removes his links.
		listen, err := d.Pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer listen.Release()
		if _, err := listen.Exec(ctx, "LISTEN "+db.HintChannel); err != nil {
			t.Fatal(err)
		}
		if err := svc.Unlink(ctx, as("alice"), mine[0].PublicID); err != nil {
			t.Fatal(err)
		}
		wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		n, err := listen.Conn().WaitForNotification(wctx)
		cancel()
		if err != nil || !strings.Contains(n.Payload, `"type":"account-links","user":"SRAAAAAAAAAAC1"`) {
			t.Errorf("hint %v, %v", n, err)
		}
		tx, err := d.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		after, err := svc.RemoveUserLinks(ctx, tx, users["bob"], audit.System, audit.TransportUI, netip.Addr{})
		if err != nil || after == nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		after(ctx)
		if count(`SELECT count(*) FROM account_links`) != 0 || count(`SELECT count(*) FROM account_link_requests
			WHERE user_id = $1 AND voided_at IS NULL AND consumed_at IS NULL`, users["bob"]) != 0 ||
			count(`SELECT count(*) FROM audit_log WHERE action = 'account_link.unlink' AND
			details->>'reason' = 'user_deleted'`) != 1 {
			t.Error("the removal of bob's links")
		}
		// Pruning: requests expired a day ago go, newer ones stay.
		total := count(`SELECT count(*) FROM account_link_requests`)
		pruned, err := accountlinks.NewPruner(d.Pool).Requests(ctx, org.ID, t0.Add(10*time.Minute+24*time.Hour+
			time.Second), 3)
		if err != nil || pruned != 3 || count(`SELECT count(*) FROM account_link_requests`) != total-3 {
			t.Errorf("pruned %d of %d, %v", pruned, total, err)
		}
		if pruned, err := accountlinks.NewPruner(d.Pool).Requests(ctx, org.ID+1, t0.Add(48*time.Hour), 1000); err != nil ||
			pruned != 0 {
			t.Errorf("another Organization pruned %d, %v", pruned, err)
		}
	})
}

// TestLiveConcurrentCodeRequests is C-18.FR-3 under concurrency on PostgreSQL: of ten simultaneous code requests of
// one User exactly five are stored and the rest refused, and of five simultaneous requests of five other Users for one account
// exactly three, since both limits of account_link.mattermost_code_requests are counted in the transaction that stores
// the request, under the lock of the User and of the target account.
func TestLiveConcurrentCodeRequests(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		conn := config.Database{URL: logging.Secret(s.NewDatabase(t)), SSLMode: "disable"}
		d, err := db.Open(ctx, conn, conn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(d.Close)
		logger := logging.New(&bytes.Buffer{}, logging.LevelInfo)
		if err := d.Migrate(ctx, logger); err != nil {
			t.Fatal(err)
		}
		if err := organization.Ensure(ctx, organization.NewStore(d.Pool), logger, t0); err != nil {
			t.Fatal(err)
		}
		org, err := organization.NewStore(d.Pool).GetOrganization(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Pool.Exec(ctx, `CREATE TABLE audit_log_p202610 PARTITION OF audit_log
			FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2026-11-01 00:00:00+00')`); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Pool.Exec(ctx, `INSERT INTO connections (org_id, public_id, type, name, mattermost_server_url,
			bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds,
			created_at, updated_at) VALUES ($1, 'CNAAAAAAAAAAM1', 'mattermost', 'mm', 'https://mm.example.org', '\x00',
			'k1', $2, 10, 1, $2, $2)`, org.ID, t0); err != nil {
			t.Fatal(err)
		}
		var users []int64
		for i := range 6 {
			var id int64
			if err := d.Pool.QueryRow(ctx, `INSERT INTO users (org_id, public_id, login, name, role, source, status,
				created_at, updated_at) VALUES ($1, $2, $3, $3, 'responder', 'local', 'active', $4, $4) RETURNING id`,
				org.ID, "SRAAAAAAAAAAD"+strconv.Itoa(i+1), "user"+strconv.Itoa(i+1), t0).Scan(&id); err != nil {
				t.Fatal(err)
			}
			users = append(users, id)
		}
		business := clock.NewManual(t0)
		svc := accountlinks.New(accountlinks.Config{OrgID: org.ID, Store: accountlinks.NewStore(d.Pool),
			Audit: audit.NewWriter(logger, business), Business: business, Log: logger,
			Mattermost: &liveBot{all: true}})
		as := func(i int) accountlinks.Requester {
			return accountlinks.Requester{UserID: users[i], UserPublicID: "SRAAAAAAAAAAD" + strconv.Itoa(i+1),
				Actor: audit.User(users[i], "x"), Transport: audit.TransportUI, Address: netip.MustParseAddr("192.0.2.1")}
		}
		race := func(n int, start func(i int) error) (stored, limited int) {
			t.Helper()
			errs := make([]error, n)
			var wg sync.WaitGroup
			for i := range n {
				wg.Go(func() { errs[i] = start(i) })
			}
			wg.Wait()
			for _, err := range errs {
				var rl *accountlinks.RateLimitedError
				switch {
				case err == nil:
					stored++
				case errors.As(err, &rl):
					limited++
				default:
					t.Fatalf("a concurrent request = %v", err)
				}
			}
			return stored, limited
		}
		if stored, limited := race(10, func(i int) error {
			_, err := svc.StartMattermost(ctx, as(0), "CNAAAAAAAAAAM1", "target"+strconv.Itoa(i))
			return err
		}); stored != 5 || limited != 5 {
			t.Errorf("one User's concurrent requests: %d stored, %d limited", stored, limited)
		}
		if stored, limited := race(5, func(i int) error {
			_, err := svc.StartMattermost(ctx, as(i+1), "CNAAAAAAAAAAM1", "dana")
			return err
		}); stored != 3 || limited != 2 {
			t.Errorf("concurrent requests for one account: %d stored, %d limited", stored, limited)
		}
	})
}

// TestLiveConcurrentLinks is C-18.FR-5 under concurrency on PostgreSQL. A Mattermost confirmation whose account
// another User links in a transaction that commits while the insert waits on the unique index is refused as
// ErrLinkedElsewhere: one link, a link_rejected entry with conflict, the confirming User's previous link in place
// and the request neither consumed nor void. Then two Users who send `/start` from one Telegram account at once,
// round after round, end with one link of the account each round and the other refused as a conflict, never an error.
func TestLiveConcurrentLinks(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		conn := config.Database{URL: logging.Secret(s.NewDatabase(t)), SSLMode: "disable"}
		d, err := db.Open(ctx, conn, conn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(d.Close)
		logger := logging.New(&bytes.Buffer{}, logging.LevelInfo)
		if err := d.Migrate(ctx, logger); err != nil {
			t.Fatal(err)
		}
		if err := organization.Ensure(ctx, organization.NewStore(d.Pool), logger, t0); err != nil {
			t.Fatal(err)
		}
		org, err := organization.NewStore(d.Pool).GetOrganization(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Pool.Exec(ctx, `CREATE TABLE audit_log_p202610 PARTITION OF audit_log
			FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2026-11-01 00:00:00+00')`); err != nil {
			t.Fatal(err)
		}
		conns := map[string]int64{}
		for _, c := range []struct{ id, typ string }{{"CNAAAAAAAAAAT1", "telegram"}, {"CNAAAAAAAAAAM1", "mattermost"}} {
			var id int64
			if err := d.Pool.QueryRow(ctx, `INSERT INTO connections (org_id, public_id, type, name,
				mattermost_server_url, telegram_bot_api_base_url, telegram_update_mode, bot_token_ciphertext,
				bot_token_key_id, bot_token_updated_at, bot_username, limiter_limit, limiter_per_seconds, created_at,
				updated_at) VALUES ($1, $2, $3, $3, CASE WHEN $3 = 'mattermost' THEN 'https://mm.example.org' END,
				CASE WHEN $3 = 'telegram' THEN 'https://api.telegram.org' END,
				CASE WHEN $3 = 'telegram' THEN 'long_polling' END, '\x00', 'k1', $4, 'muster_dev_bot', 10, 1, $4, $4)
				RETURNING id`, org.ID, c.id, c.typ, t0).Scan(&id); err != nil {
				t.Fatal(err)
			}
			conns[c.typ] = id
		}
		users := map[string]int64{}
		pids := map[string]string{"alice": "SRAAAAAAAAAAE1", "bob": "SRAAAAAAAAAAE2"}
		for _, login := range []string{"alice", "bob"} {
			var id int64
			if err := d.Pool.QueryRow(ctx, `INSERT INTO users (org_id, public_id, login, name, role, source, status,
				created_at, updated_at) VALUES ($1, $2, $3, $3, 'responder', 'local', 'active', $4, $4) RETURNING id`,
				org.ID, pids[login], login, t0).Scan(&id); err != nil {
				t.Fatal(err)
			}
			users[login] = id
		}
		business := clock.NewManual(t0)
		bot := &liveBot{}
		svc := accountlinks.New(accountlinks.Config{OrgID: org.ID, Store: accountlinks.NewStore(d.Pool),
			Audit: audit.NewWriter(logger, business), Business: business, Log: logger, Mattermost: bot})
		as := func(login string) accountlinks.Requester {
			return accountlinks.Requester{UserID: users[login], UserPublicID: pids[login],
				Actor: audit.User(users[login], pids[login]), Transport: audit.TransportUI,
				Address: netip.MustParseAddr("192.0.2.1")}
		}
		count := func(q string, args ...any) int {
			var n int
			if err := d.Pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		insertLink := func(q interface {
			Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
		}, publicID, login, external string) {
			t.Helper()
			if _, err := q.Exec(ctx, `INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id,
				external_id, username, created_at) VALUES ($1, $2, $3, 'mattermost', $4, $5, $5, $6)`, org.ID, publicID,
				users[login], conns["mattermost"], external, t0); err != nil {
				t.Fatal(err)
			}
		}
		// Mattermost: bob's link of u-dana commits while alice's confirmation waits on it.
		insertLink(d.Pool, "AKAAAAAAAAAAE1", "alice", "u-erin")
		started, err := svc.StartMattermost(ctx, as("alice"), "CNAAAAAAAAAAM1", "dana")
		if err != nil {
			t.Fatal(err)
		}
		code := codeRe.FindString(bot.sent[len(bot.sent)-1])
		tx, err := d.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		insertLink(tx, "AKAAAAAAAAAAE2", "bob", "u-dana")
		done := make(chan error, 1)
		go func() {
			_, err := svc.ConfirmMattermost(ctx, as("alice"), started.RequestPublicID, code)
			done <- err
		}()
		for i := 0; count(`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND
			wait_event_type = 'Lock' AND query LIKE '%INSERT INTO account_links (%'`) == 0; i++ {
			if i == 500 {
				t.Fatal("the confirmation never waited on bob's link")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, accountlinks.ErrLinkedElsewhere) {
			t.Fatalf("a confirmation that lost the race = %v", err)
		}
		if count(`SELECT count(*) FROM account_links WHERE external_id = 'u-dana' AND user_id = $1`, users["bob"]) != 1 ||
			count(`SELECT count(*) FROM account_links WHERE external_id = 'u-erin' AND user_id = $1`,
				users["alice"]) != 1 || count(`SELECT count(*) FROM account_links`) != 2 ||
			count(`SELECT count(*) FROM audit_log WHERE action = 'account_link.link_rejected' AND
				details->>'reason' = 'conflict' AND details->>'user' = $1 AND details->>'external_id' = 'u-dana'`,
				pids["alice"]) != 1 ||
			count(`SELECT count(*) FROM audit_log WHERE action IN ('account_link.link', 'account_link.unlink')`) != 0 ||
			count(`SELECT count(*) FROM account_link_requests WHERE public_id = $1 AND consumed_at IS NULL AND
				voided_at IS NULL AND attempts = 0`, started.RequestPublicID) != 1 {
			t.Fatal("the state after the lost race")
		}
		// Telegram: alice and bob send `/start` from one account at once.
		var mu sync.Mutex
		var replies []string
		reply := func(_ context.Context, text string) error {
			mu.Lock()
			defer mu.Unlock()
			replies = append(replies, text)
			return nil
		}
		const rounds = 5
		for round := range rounds {
			from := int64(7100 + round)
			var toks []string
			for _, login := range []string{"alice", "bob"} {
				st, err := svc.StartTelegram(ctx, as(login), "CNAAAAAAAAAAT1")
				if err != nil {
					t.Fatal(err)
				}
				_, tok, _ := strings.Cut(st.DeepLink, "?start=")
				toks = append(toks, tok)
			}
			errs := make([]error, 2)
			var wg sync.WaitGroup
			for i, tok := range toks {
				wg.Go(func() {
					errs[i] = svc.LinkTelegram(ctx, accountlinks.TelegramConnection{ID: conns["telegram"],
						PublicID: "CNAAAAAAAAAAT1"}, tok, accountlinks.TelegramAccount{ID: from}, reply)
				})
			}
			wg.Wait()
			if err := errors.Join(errs...); err != nil {
				t.Fatalf("round %d = %v", round, err)
			}
			if n := count(`SELECT count(*) FROM account_links WHERE messenger = 'telegram' AND external_id = $1`,
				strconv.FormatInt(from, 10)); n != 1 {
				t.Fatalf("round %d: %d links of the account", round, n)
			}
		}
		conflicts := 0
		for _, r := range replies {
			if strings.HasPrefix(r, "This account is linked to another user.") {
				conflicts++
			}
		}
		if len(replies) != 2*rounds || conflicts != rounds || count(`SELECT count(*) FROM audit_log WHERE
			action = 'account_link.link_rejected' AND details->>'reason' = 'conflict' AND
			details->>'messenger' = 'telegram'`) != rounds || count(`SELECT count(*) FROM account_link_requests
			WHERE messenger = 'telegram' AND consumed_at IS NULL AND voided_at IS NULL`) != rounds {
			t.Errorf("replies %q", replies)
		}
	})
}

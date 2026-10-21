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
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/accountlinks"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/routing"
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
		for _, c := range []struct{ id, typ, name string }{{"CNAAAAAAAAAAT1", "telegram", "Example Telegram"},
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
		for i := range 5 {
			_, err := svc.ConfirmMattermost(ctx, as("bob"), started.RequestPublicID, "22222222")
			if fe, ok := errors.AsType[*accountlinks.FieldError](err); !ok || fe.AttemptsRemaining == nil ||
				*fe.AttemptsRemaining != 4-i {
				t.Fatalf("wrong code %d = %v", i+1, err)
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
		if err != nil || len(mine) != 1 || mine[0].ExternalID != "7004" || mine[0].ConnectionName != "Example Telegram" {
			t.Fatalf("List = %+v, %v", mine, err)
		}
		cs, err := svc.Connections(ctx)
		if err != nil || len(cs) != 3 || cs[0].Name != "mm" || cs[1].Name != "Example Telegram" {
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
		if err != nil || !strings.Contains(n.Payload, `"type":"account-links","id":"`+mine[0].PublicID+
			`","user":"SRAAAAAAAAAAC1"`) {
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

// TestLivePruneBatches is account_link.request_prune_after on PostgreSQL: each run deletes exactly its batch of the
// Organization's requests that expired a day before, the lowest ids first, also planned as a nested loop over the table
// it deletes from, where a batch chosen in a subquery ran once per row and deleted more than the batch; a request that
// another transaction holds is skipped and taken at the next run; requests that have not expired, and those of another
// Organization, stay.
func TestLivePruneBatches(t *testing.T) {
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
		var other int64
		if err := d.Pool.QueryRow(ctx, `INSERT INTO organizations (public_id, name, time_zone, severity_label,
			severity_mapping, severity_styles, critical_is_urgent, instance_labels, retention_stored_snapshots_days,
			retention_alert_details_days, retention_alert_group_summaries_days, retention_audit_log_days,
			totp_required, oidc_token_grace_seconds, created_at, updated_at)
			SELECT 'RG0000000000ZZ', 'Other', time_zone, severity_label, severity_mapping, severity_styles,
				critical_is_urgent, instance_labels, retention_stored_snapshots_days, retention_alert_details_days,
				retention_alert_group_summaries_days, retention_audit_log_days, totp_required,
				oidc_token_grace_seconds, created_at, updated_at
			FROM organizations WHERE id = $1
			RETURNING id`, org.ID).Scan(&other); err != nil {
			t.Fatal(err)
		}
		type owner struct{ user, conn int64 }
		owners := map[int64]owner{}
		for _, o := range []int64{org.ID, other} {
			var w owner
			if err := d.Pool.QueryRow(ctx, `INSERT INTO connections (org_id, public_id, type, name,
				telegram_bot_api_base_url, telegram_update_mode, bot_token_ciphertext, bot_token_key_id,
				bot_token_updated_at, bot_username, limiter_limit, limiter_per_seconds, created_at, updated_at)
				VALUES ($1, 'CNAAAAAAAAAAP1', 'telegram', 'tg', 'https://api.telegram.org', 'long_polling', '\x00', 'k1',
				$2, 'muster_dev_bot', 10, 1, $2, $2) RETURNING id`, o, t0).Scan(&w.conn); err != nil {
				t.Fatal(err)
			}
			if err := d.Pool.QueryRow(ctx, `INSERT INTO users (org_id, public_id, login, name, role, source, status,
				created_at, updated_at) VALUES ($1, 'SRAAAAAAAAAAP1', 'pat', 'Pat', 'responder', 'local', 'active', $2,
				$2) RETURNING id`, o, t0).Scan(&w.user); err != nil {
				t.Fatal(err)
			}
			owners[o] = w
		}
		next := 0
		add := func(o int64, n int, expires time.Time) {
			t.Helper()
			if _, err := d.Pool.Exec(ctx, `INSERT INTO account_link_requests (org_id, public_id, user_id, messenger,
				connection_id, secret_hash, created_at, expires_at)
				SELECT $1::bigint, 'AR' || lpad(i::text, 12, '0'), $2::bigint, 'telegram', $3::bigint,
					sha256(convert_to(i::text, 'UTF8')), $4::timestamptz, $5::timestamptz
				FROM generate_series($6::int, $6::int + $7::int - 1) i`, o, owners[o].user, owners[o].conn,
				t0.Add(-10*time.Minute), expires, next+1, n); err != nil {
				t.Fatal(err)
			}
			next += n
		}
		fresh := t0.Add(48 * time.Hour)
		add(org.ID, 4, t0)
		add(other, 3, t0)
		add(org.ID, 2, fresh)
		add(org.ID, 3, t0)
		now := t0.Add(accountlinks.RequestPruneAfter + time.Hour)
		count := func(o int64, expired bool) int {
			t.Helper()
			var n int
			if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM account_link_requests WHERE org_id = $1 AND
				(expires_at < $2) = $3`, o, t0.Add(time.Hour), expired).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		want := func(step string, pruned int64, err error, wantPruned int64, expiredLeft int) {
			t.Helper()
			if err != nil || pruned != wantPruned || count(org.ID, true) != expiredLeft || count(org.ID, false) != 2 ||
				count(other, true) != 3 {
				t.Errorf("%s: pruned %d, %v; %d expired and %d fresh left, %d of the other Organization", step, pruned,
					err, count(org.ID, true), count(org.ID, false), count(other, true))
			}
		}
		var pruned int64
		loops := dbtest.LockRowsLoops(t, d.Pool.Config().ConnString(), func(ctx context.Context, c *pgx.Conn) {
			pruned, err = accountlinks.NewPruner(c).Requests(ctx, org.ID, now, 3)
		})
		want("a nested loop", pruned, err, 3, 4)
		if loops != 1 {
			t.Errorf("the prune chose its batch %d times in one run", loops)
		}
		tx, err := d.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
		var held int64
		if err := tx.QueryRow(ctx, `SELECT id FROM account_link_requests WHERE org_id = $1 AND expires_at < $2
			ORDER BY id LIMIT 1 FOR UPDATE`, org.ID, t0.Add(time.Hour)).Scan(&held); err != nil {
			t.Fatal(err)
		}
		pruned, err = accountlinks.NewPruner(d.Pool).Requests(ctx, org.ID, now, 3)
		want("a held request", pruned, err, 3, 1)
		if err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		pruned, err = accountlinks.NewPruner(d.Pool).Requests(ctx, org.ID, now, 3)
		want("the held request released", pruned, err, 1, 0)
		pruned, err = accountlinks.NewPruner(d.Pool).Requests(ctx, org.ID, now, 3)
		want("nothing left", pruned, err, 0, 0)
		if pruned, err := accountlinks.NewPruner(d.Pool).Requests(ctx, other, now, 2); err != nil || pruned != 2 ||
			count(other, true) != 1 || count(org.ID, false) != 2 {
			t.Errorf("the other Organization pruned %d, %v", pruned, err)
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

// liveBridgeBot is the Bridge bot of TestLiveBridgeLinks: it offers a code by email, finds anyone by their address and
// keeps the direct messages.
type liveBridgeBot struct {
	mu   sync.Mutex
	sent []string
}

func (b *liveBridgeBot) Session(ctx context.Context, _ string, f func(context.Context, accountlinks.BridgeBot) error) error {
	return f(ctx, b)
}

func (b *liveBridgeBot) Offer() accountlinks.BridgeOffer {
	return accountlinks.BridgeOffer{Read: true, UserLookup: accountlinks.LookupEmail, BotCanStart: true}
}

func (b *liveBridgeBot) FindUser(_ context.Context, handle string) (accountlinks.BridgeAccount, error) {
	local, _, _ := strings.Cut(handle, "@")
	return accountlinks.BridgeAccount{ExternalID: "u-" + local, DisplayName: strings.ToUpper(local[:1]) + local[1:] +
		" Brown", Handle: handle}, nil
}

func (b *liveBridgeBot) SendDirectMessage(_ context.Context, _, _, text string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sent = append(b.sent, text)
	return nil
}

// TestLiveBridgeLinks runs the queries of Bridge links on PostgreSQL (C-22.FR-15): the link methods from the stored
// info; a token found by its hash among the Bridge tokens alone and consumed once when two events carry it at once; a
// Telegram token and one of another Connection link nothing; the link's display name and handle; a code with its
// target's display name; both limits of account_link.bridge_code_requests under concurrency; the identity space of
// each Connection; unlinking with the display name in the Audit log; and the pruning of Bridge requests.
func TestLiveBridgeLinks(t *testing.T) {
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
		const info = `{"protocol_version": "1", "user_lookup": "email", "capabilities": {"direct_messages":
			{"bot_can_start": true}}, "link_url_template": "https://chat.example.org/bot?start={token}"}`
		conns := map[string]int64{}
		for _, c := range []struct {
			id, typ, name string
			info          *string
		}{{"CNAAAAAAAAAAT1", "telegram", "tg", nil}, {"CNAAAAAAAAAAB1", "bridge", "Example Bridge", new(info)},
			{"CNAAAAAAAAAAB2", "bridge", "br", nil}} {
			var id int64
			if err := d.Pool.QueryRow(ctx, `INSERT INTO connections (org_id, public_id, type, name,
				telegram_bot_api_base_url, telegram_update_mode, bridge_base_url, bridge_event_delivery, bridge_info,
				bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, bot_username, limiter_limit,
				limiter_per_seconds, created_at, updated_at) VALUES ($1, $2, $3, $4,
				CASE WHEN $3 = 'telegram' THEN 'https://api.telegram.org' END,
				CASE WHEN $3 = 'telegram' THEN 'long_polling' END, CASE WHEN $3 = 'bridge' THEN 'https://br.example.org' END,
				CASE WHEN $3 = 'bridge' THEN 'poll' END, $6::jsonb, '\x00', 'k1', $5, 'muster_dev_bot', 10, 1, $5, $5)
				RETURNING id`, org.ID, c.id, c.typ, c.name, t0, c.info).Scan(&id); err != nil {
				t.Fatal(err)
			}
			conns[c.id] = id
		}
		users := map[string]int64{}
		pid := func(login string) string { return "SRAAAAAAAAAAF" + strings.ToUpper(login[:1]) }
		for _, login := range []string{"alice", "bob", "carol", "dave", "erin", "frank", "gina", "hank"} {
			var id int64
			if err := d.Pool.QueryRow(ctx, `INSERT INTO users (org_id, public_id, login, name, role, source, status,
				created_at, updated_at) VALUES ($1, $2, $3, $3, 'responder', 'local', 'active', $4, $4) RETURNING id`,
				org.ID, pid(login), login, t0).Scan(&id); err != nil {
				t.Fatal(err)
			}
			users[login] = id
		}
		business := clock.NewManual(t0)
		bot := &liveBridgeBot{}
		svc := accountlinks.New(accountlinks.Config{OrgID: org.ID, Store: accountlinks.NewStore(d.Pool),
			Audit: audit.NewWriter(logger, business), Business: business, Log: logger, Bridge: bot})
		as := func(login string) accountlinks.Requester {
			return accountlinks.Requester{UserID: users[login], UserPublicID: pid(login),
				Actor: audit.User(users[login], pid(login)), Transport: audit.TransportUI,
				Address: netip.MustParseAddr("192.0.2.1")}
		}
		count := func(q string, args ...any) int {
			var n int
			if err := d.Pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		// The Connections with the link methods of their stored info.
		cs, err := svc.Connections(ctx)
		if err != nil || len(cs) != 3 || cs[0].Name != "br" || !slices.Equal(cs[0].LinkMethods, []string{"token"}) ||
			cs[0].HandleKind != "" || cs[1].Name != "Example Bridge" ||
			!slices.Equal(cs[1].LinkMethods, []string{"token", "code"}) || cs[1].HandleKind != "email" ||
			cs[2].Messenger != "telegram" || cs[2].LinkMethods != nil {
			t.Fatalf("Connections = %+v, %v", cs, err)
		}
		// A token: its link from the stored template, and two events with it at once link once.
		st, err := svc.StartBridge(ctx, as("alice"), "CNAAAAAAAAAAB1")
		if err != nil || st.LinkURL == nil || *st.LinkURL != "https://chat.example.org/bot?start="+st.Token {
			t.Fatalf("StartBridge = %+v, %v", st, err)
		}
		b1 := accountlinks.BridgeConnection{ID: conns["CNAAAAAAAAAAB1"], PublicID: "CNAAAAAAAAAAB1"}
		b2 := accountlinks.BridgeConnection{ID: conns["CNAAAAAAAAAAB2"], PublicID: "CNAAAAAAAAAAB2"}
		answers := make([]string, 2)
		var wg sync.WaitGroup
		for i, ext := range []string{"u-alice", "u-mallory"} {
			wg.Go(func() {
				out, err := svc.LinkBridge(ctx, b1, st.Token, accountlinks.BridgeAccount{ExternalID: ext,
					DisplayName: "Alice Smith", Handle: "alice@example.org"})
				if err != nil {
					t.Error(err)
				}
				out.Log(ctx, nil)
				answers[i] = out.Answer
			})
		}
		wg.Wait()
		slices.Sort(answers)
		if answers[0] != "Linked to Muster user alice (alice)" ||
			answers[1] != "This link is invalid or expired. Start again from your Muster profile." ||
			count(`SELECT count(*) FROM account_links WHERE messenger = 'bridge' AND identity_space = $1 AND
				display_name = 'Alice Smith' AND username = 'alice@example.org'`,
				accountlinks.SpaceBridge(conns["CNAAAAAAAAAAB1"])) != 1 {
			t.Fatalf("two events with one token: %q", answers)
		}
		// A Telegram token and a token of another Connection link nothing.
		tg, err := svc.StartTelegram(ctx, as("bob"), "CNAAAAAAAAAAT1")
		if err != nil {
			t.Fatal(err)
		}
		_, tgTok, _ := strings.Cut(tg.DeepLink, "?start=")
		other, err := svc.StartBridge(ctx, as("bob"), "CNAAAAAAAAAAB1")
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct {
			conn  accountlinks.BridgeConnection
			token string
		}{{b1, tgTok}, {b2, other.Token}} {
			out, err := svc.LinkBridge(ctx, c.conn, c.token, accountlinks.BridgeAccount{ExternalID: "u-bob",
				DisplayName: "Bob"})
			if err != nil || out.Answer != "This link is invalid or expired. Start again from your Muster profile." {
				t.Errorf("LinkBridge(%s) = %+v, %v", c.conn.PublicID, out, err)
			}
		}
		if count(`SELECT count(*) FROM audit_log WHERE action = 'account_link.link_rejected' AND
			details->>'messenger' = 'bridge' AND details->>'reason' IN ('token_used', 'token_unknown',
			'wrong_connection')`) != 3 || count(`SELECT count(*) FROM account_link_requests WHERE messenger = 'bridge'
			AND method = 'token' AND consumed_at IS NULL`) != 1 {
			t.Error("the rejected tokens")
		}
		// A code: the link takes the target's display name and handle; the second Connection is another space.
		started, err := svc.StartBridgeCode(ctx, as("bob"), "CNAAAAAAAAAAB1", "bob@example.org")
		if err != nil {
			t.Fatal(err)
		}
		if count(`SELECT count(*) FROM account_link_requests WHERE public_id = $1 AND method = 'code' AND
			target_external_id = 'u-bob' AND target_username = 'bob@example.org' AND target_display_name = 'Bob Brown'`,
			started.RequestPublicID) != 1 {
			t.Error("the stored code request")
		}
		l, err := svc.ConfirmBridge(ctx, as("bob"), started.RequestPublicID, codeRe.FindString(bot.sent[0]))
		if err != nil || l.ExternalID != "u-bob" || *l.DisplayName != "Bob Brown" || *l.Username != "bob@example.org" {
			t.Fatalf("ConfirmBridge = %+v, %v", l, err)
		}
		mine, err := svc.List(ctx, users["bob"])
		if err != nil || len(mine) != 1 || *mine[0].DisplayName != "Bob Brown" {
			t.Errorf("List = %+v, %v", mine, err)
		}
		// The footer of an Alert Group bob acknowledged names him in the identity space of the Connection by the display
		// name of his link on one line, also without a username, and by its username without a display name or with a
		// blank one (C-22.FR-16).
		if err := routing.EnsureDefault(ctx, routing.NewStore(d.Pool), org.ID, t0); err != nil {
			t.Fatal(err)
		}
		var group int64
		if err := d.Pool.QueryRow(ctx, `INSERT INTO alert_groups (org_id, public_id, number, route_id,
			group_key_labels, group_key_values, group_key_sha256, title, common_labels, common_annotations,
			integration_ids, status, severity_level, urgent, owner_user_id, acknowledged_at, created_at,
			last_changed_at)
			SELECT $1, 'AGAAAAAAAAAAF1', 1, r.id, '{alertname}', '{}', sha256('footer'::bytea), 'Latency', '{}', '{}',
				'{}', 'acknowledged', 'critical', false, $2, $3, $3, $3
			FROM routes r WHERE r.org_id = $1 AND r.is_default RETURNING id`, org.ID, users["bob"], t0).Scan(
			&group); err != nil {
			t.Fatal(err)
		}
		space := accountlinks.SpaceBridge(conns["CNAAAAAAAAAAB1"])
		for _, step := range []struct {
			set, want string
		}{
			{"", "Bob Brown"},
			{"username = NULL", "Bob Brown"},
			{"display_name = NULL, username = 'bob@example.org'", "bob@example.org"},
			{"display_name = E'Bob\\n  Brown\\r\\n'", "Bob Brown"},
			{"display_name = E' \\n'", "bob@example.org"},
			{"display_name = 'Bob Brown'", "Bob Brown"},
		} {
			if step.set != "" {
				if _, err := d.Pool.Exec(ctx, `UPDATE account_links SET `+step.set+` WHERE org_id = $1 AND
					user_id = $2 AND messenger = 'bridge'`, org.ID, users["bob"]); err != nil {
					t.Fatal(err)
				}
			}
			names, err := mentions.New(org.ID, d.Pool).FooterNames(ctx, nil, group)
			if err != nil || len(names) != 1 || names[space] != step.want {
				t.Errorf("footer names with %q = %v, %v", step.set, names, err)
			}
		}
		if u, err := svc.Lookup(ctx, accountlinks.SpaceBridge(conns["CNAAAAAAAAAAB1"]), "u-bob"); err != nil ||
			u.ID != users["bob"] {
			t.Errorf("Lookup = %+v, %v", u, err)
		}
		if _, err := svc.Lookup(ctx, accountlinks.SpaceBridge(conns["CNAAAAAAAAAAB2"]), "u-bob"); !errors.Is(err,
			accountlinks.ErrNotLinked) {
			t.Errorf("another Connection's space = %v", err)
		}
		// Ten code requests of one User at once store five; five Users' requests for one account store three.
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
			_, err := svc.StartBridgeCode(ctx, as("carol"), "CNAAAAAAAAAAB1", "t"+strconv.Itoa(i)+"@example.org")
			return err
		}); stored != 5 || limited != 5 {
			t.Errorf("one User's concurrent requests: %d stored, %d limited", stored, limited)
		}
		if stored, limited := race(5, func(i int) error {
			login := []string{"dave", "erin", "frank", "gina", "hank"}[i]
			_, err := svc.StartBridgeCode(ctx, as(login), "CNAAAAAAAAAAB1", "dana@example.org")
			return err
		}); stored != 3 || limited != 2 {
			t.Errorf("concurrent requests for one account: %d stored, %d limited", stored, limited)
		}
		// Unlinking records the display name; Bridge requests are pruned as any other.
		if err := svc.Unlink(ctx, as("bob"), l.PublicID); err != nil {
			t.Fatal(err)
		}
		if count(`SELECT count(*) FROM audit_log WHERE action = 'account_link.unlink' AND
			details->>'display_name' = 'Bob Brown' AND details->>'messenger' = 'bridge'`) != 1 {
			t.Error("the unlink of a Bridge link")
		}
		bridgeReqs := count(`SELECT count(*) FROM account_link_requests WHERE messenger = 'bridge'`)
		pruned, err := accountlinks.NewPruner(d.Pool).Requests(ctx, org.ID, t0.Add(48*time.Hour), 1000)
		if err != nil || bridgeReqs != 11 || pruned != 12 || count(`SELECT count(*) FROM account_link_requests`) != 0 {
			t.Errorf("pruned %d with %d Bridge requests and bob's Telegram one, %v", pruned, bridgeReqs, err)
		}
		if strings.Contains(log.String(), st.Token) || strings.Contains(log.String(), other.Token) {
			t.Error("a token in the log")
		}
	})
}

// liveSlackBot is the Slack bot of TestLiveSlackLinks: it finds anyone by email or name — U0 and the name in
// capitals, the name with " Brown" as the display name, none for dana — and keeps the direct messages.
type liveSlackBot struct {
	mu   sync.Mutex
	sent []string
}

func (b *liveSlackBot) Session(ctx context.Context, _ string, f func(context.Context, accountlinks.SlackBot) error) error {
	return f(ctx, b)
}

func (b *liveSlackBot) FindUser(_ context.Context, handle string) (accountlinks.SlackAccount, error) {
	local, _, _ := strings.Cut(strings.ToLower(handle), "@")
	a := accountlinks.SlackAccount{ID: "U0" + strings.ToUpper(local), Name: local}
	if local != "dana" {
		a.DisplayName = strings.ToUpper(local[:1]) + local[1:] + " Brown"
	}
	return a, nil
}

func (b *liveSlackBot) SendCode(_ context.Context, _ string, _ logging.Secret, text string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sent = append(b.sent, text)
	return nil
}

func (b *liveSlackBot) SendLinked(_ context.Context, _, text string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sent = append(b.sent, text)
	return nil
}

// code is the code of the last direct message with one.
func (b *liveSlackBot) code() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := len(b.sent) - 1; i >= 0; i-- {
		if c := codeRe.FindString(b.sent[i]); c != "" {
			return c
		}
	}
	return ""
}

// TestLiveSlackLinks runs the queries of Slack links on PostgreSQL (C-24.FR-12, C-24.FR-6): the Slack Connections that
// are not deleted among those to link through; a request stored with its target's user id, name and display name and
// no method; a link in the identity space of its Connection with them; a second link of the User through the
// Connection replacing the first; an account linked to another User refused; a link through another Slack Connection
// in a space of its own; five wrong codes voiding a request; both limits of account_link.slack_code_requests under
// concurrency; the footer naming the User by the display name of their link for the Connection, without one by none;
// and the pruning of Slack requests.
func TestLiveSlackLinks(t *testing.T) {
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
		for _, c := range []struct {
			id, name string
			deleted  bool
		}{{"CNAAAAAAAAAAS1", "sl", false}, {"CNAAAAAAAAAAS2", "Example Slack", false}, {"CNAAAAAAAAAAS3", "gone", true}} {
			var id int64
			if err := d.Pool.QueryRow(ctx, `INSERT INTO connections (org_id, public_id, type, name, slack_event_mode,
				bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, slack_app_token_ciphertext,
				slack_app_token_key_id, slack_app_token_updated_at, limiter_limit, limiter_per_seconds, created_at,
				updated_at, deleted_at) VALUES ($1, $2, 'slack', $3, 'socket', '\x00', 'k1', $4, '\x00', 'k1', $4, 10, 1,
				$4, $4, CASE WHEN $5 THEN $4::timestamptz END) RETURNING id`, org.ID, c.id, c.name, t0,
				c.deleted).Scan(&id); err != nil {
				t.Fatal(err)
			}
			conns[c.id] = id
		}
		users := map[string]int64{}
		pid := func(login string) string { return "SRAAAAAAAAAAS" + strings.ToUpper(login[:1]) }
		for _, login := range []string{"alice", "bob", "carol", "dave", "erin", "frank", "gina", "hank"} {
			var id int64
			if err := d.Pool.QueryRow(ctx, `INSERT INTO users (org_id, public_id, login, name, role, source, status,
				created_at, updated_at) VALUES ($1, $2, $3, $3, 'responder', 'local', 'active', $4, $4) RETURNING id`,
				org.ID, pid(login), login, t0).Scan(&id); err != nil {
				t.Fatal(err)
			}
			users[login] = id
		}
		business := clock.NewManual(t0)
		bot := &liveSlackBot{}
		svc := accountlinks.New(accountlinks.Config{OrgID: org.ID, Store: accountlinks.NewStore(d.Pool),
			Audit: audit.NewWriter(logger, business), Business: business, Log: logger, Slack: bot})
		as := func(login string) accountlinks.Requester {
			return accountlinks.Requester{UserID: users[login], UserPublicID: pid(login),
				Actor: audit.User(users[login], pid(login)), Transport: audit.TransportUI,
				Address: netip.MustParseAddr("192.0.2.1")}
		}
		count := func(q string, args ...any) int {
			var n int
			if err := d.Pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		s1, s2 := accountlinks.SpaceSlack(conns["CNAAAAAAAAAAS1"]), accountlinks.SpaceSlack(conns["CNAAAAAAAAAAS2"])
		// The Slack Connections that are not deleted, by name, without link methods.
		cs, err := svc.Connections(ctx)
		if err != nil || len(cs) != 2 || cs[0].Name != "Example Slack" || cs[1].Name != "sl" ||
			cs[0].Messenger != accountlinks.MessengerSlack || cs[0].LinkMethods != nil || cs[0].HandleKind != "" {
			t.Fatalf("Connections = %+v, %v", cs, err)
		}
		// By email: the request keeps the target, the link takes it.
		started, err := svc.StartSlack(ctx, as("alice"), "CNAAAAAAAAAAS1", "Alice@Example.org")
		if err != nil {
			t.Fatal(err)
		}
		if count(`SELECT count(*) FROM account_link_requests WHERE public_id = $1 AND messenger = 'slack' AND
			method IS NULL AND target_external_id = 'U0ALICE' AND target_username = 'alice' AND
			target_display_name = 'Alice Brown' AND length(secret_hash) = 32`, started.RequestPublicID) != 1 {
			t.Error("the stored request")
		}
		first, err := svc.ConfirmSlack(ctx, as("alice"), started.RequestPublicID, bot.code())
		if err != nil || first.ExternalID != "U0ALICE" || *first.Username != "alice" || *first.DisplayName != "Alice Brown" {
			t.Fatalf("ConfirmSlack = %+v, %v", first, err)
		}
		if count(`SELECT count(*) FROM account_links WHERE messenger = 'slack' AND identity_space = $1 AND
			external_id = 'U0ALICE' AND display_name = 'Alice Brown'`, s1) != 1 ||
			count(`SELECT count(*) FROM audit_log WHERE action = 'account_link.link' AND
			details->>'messenger' = 'slack' AND details->>'display_name' = 'Alice Brown' AND transport = 'ui'`) != 1 ||
			bot.sent[len(bot.sent)-1] != "Linked to Muster user alice" {
			t.Errorf("the link through sl, sent %q", bot.sent)
		}
		// A second link of alice through sl replaces the first; dana's account has no display name.
		started, err = svc.StartSlack(ctx, as("alice"), "CNAAAAAAAAAAS1", "dana")
		if err != nil {
			t.Fatal(err)
		}
		l, err := svc.ConfirmSlack(ctx, as("alice"), started.RequestPublicID, bot.code())
		if err != nil || l.ExternalID != "U0DANA" || l.DisplayName != nil || count(`SELECT count(*) FROM account_links
			WHERE user_id = $1 AND identity_space = $2`, users["alice"], s1) != 1 || count(`SELECT count(*) FROM
			audit_log WHERE action = 'account_link.unlink' AND details->>'reason' = 'replaced' AND
			details->>'messenger' = 'slack'`) != 1 {
			t.Fatalf("the replacing link = %+v, %v", l, err)
		}
		// Bob cannot link dana's account; alice links through Example Slack, a space of its own.
		if _, err := svc.StartSlack(ctx, as("bob"), "CNAAAAAAAAAAS1", "dana@example.org"); !errors.Is(err,
			accountlinks.ErrLinkedElsewhere) || count(`SELECT count(*) FROM audit_log WHERE
			action = 'account_link.link_rejected' AND details->>'reason' = 'conflict' AND
			details->>'messenger' = 'slack'`) != 1 {
			t.Errorf("a conflict = %v", err)
		}
		started, err = svc.StartSlack(ctx, as("alice"), "CNAAAAAAAAAAS2", "alice")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ConfirmSlack(ctx, as("alice"), started.RequestPublicID, bot.code()); err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct {
			space, external string
			linked          bool
		}{{s1, "U0DANA", true}, {s2, "U0ALICE", true}, {s1, "U0ALICE", false}, {s2, "U0DANA", false}} {
			u, err := svc.Lookup(ctx, c.space, c.external)
			if c.linked != (err == nil && u.ID == users["alice"]) {
				t.Errorf("Lookup(%s, %s) = %+v, %v", c.space, c.external, u, err)
			}
		}
		mine, err := svc.List(ctx, users["alice"])
		if err != nil || len(mine) != 2 || mine[0].Messenger != accountlinks.MessengerSlack {
			t.Errorf("List = %+v, %v", mine, err)
		}
		// Five wrong codes void a request; the right one is refused then.
		started, err = svc.StartSlack(ctx, as("bob"), "CNAAAAAAAAAAS2", "bob")
		if err != nil {
			t.Fatal(err)
		}
		code := bot.code()
		for range accountlinks.SlackMaxAttempts {
			if _, err := svc.ConfirmSlack(ctx, as("bob"), started.RequestPublicID, "22222222"); err == nil {
				t.Fatal("a wrong code linked")
			}
		}
		var fe *accountlinks.FieldError
		if _, err := svc.ConfirmSlack(ctx, as("bob"), started.RequestPublicID, code); !errors.As(err, &fe) ||
			*fe.AttemptsRemaining != 0 || count(`SELECT count(*) FROM account_link_requests WHERE public_id = $1 AND
			attempts = 5 AND voided_at IS NOT NULL`, started.RequestPublicID) != 1 {
			t.Errorf("the right code after five wrong ones = %v", err)
		}
		// The footer of an Alert Group alice acknowledged: by the display name of her link through Example Slack, and
		// through sl, whose link has none, by none, which leaves her Muster display name.
		if err := routing.EnsureDefault(ctx, routing.NewStore(d.Pool), org.ID, t0); err != nil {
			t.Fatal(err)
		}
		var group int64
		if err := d.Pool.QueryRow(ctx, `INSERT INTO alert_groups (org_id, public_id, number, route_id,
			group_key_labels, group_key_values, group_key_sha256, title, common_labels, common_annotations,
			integration_ids, status, severity_level, urgent, owner_user_id, acknowledged_at, created_at,
			last_changed_at)
			SELECT $1, 'AGAAAAAAAAAAS1', 1, r.id, '{alertname}', '{}', sha256('footer'::bytea), 'Latency', '{}', '{}',
				'{}', 'acknowledged', 'critical', false, $2, $3, $3, $3
			FROM routes r WHERE r.org_id = $1 AND r.is_default RETURNING id`, org.ID, users["alice"], t0).Scan(
			&group); err != nil {
			t.Fatal(err)
		}
		names, err := mentions.New(org.ID, d.Pool).FooterNames(ctx, nil, group)
		if err != nil || len(names) != 1 || names[s2] != "Alice Brown" {
			t.Errorf("footer names = %v, %v", names, err)
		}
		// Ten code requests of one User at once store five; five Users' requests for one account store three.
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
			_, err := svc.StartSlack(ctx, as("carol"), "CNAAAAAAAAAAS1", "t"+strconv.Itoa(i)+"@example.org")
			return err
		}); stored != 5 || limited != 5 {
			t.Errorf("one User's concurrent requests: %d stored, %d limited", stored, limited)
		}
		if stored, limited := race(5, func(i int) error {
			login := []string{"dave", "erin", "frank", "gina", "hank"}[i]
			_, err := svc.StartSlack(ctx, as(login), "CNAAAAAAAAAAS1", "zed")
			return err
		}); stored != 3 || limited != 2 {
			t.Errorf("concurrent requests for one account: %d stored, %d limited", stored, limited)
		}
		// Slack requests are pruned as any other.
		slackReqs := count(`SELECT count(*) FROM account_link_requests WHERE messenger = 'slack'`)
		pruned, err := accountlinks.NewPruner(d.Pool).Requests(ctx, org.ID, t0.Add(48*time.Hour), 1000)
		if err != nil || slackReqs != 12 || pruned != 12 || count(`SELECT count(*) FROM account_link_requests`) != 0 {
			t.Errorf("pruned %d of %d Slack requests, %v", pruned, slackReqs, err)
		}
		for _, m := range bot.sent {
			if c := codeRe.FindString(m); c != "" && strings.Contains(log.String(), c) {
				t.Error("a code in the log")
			}
		}
	})
}

// The lock order of a token link against the deletion of its User: the deletion (users.Admin.Delete) locks the User
// (LockUser) and then, through RemoveUserLinks, voids the User's pending link requests, while a token link reads its
// request by the token's hash and locks the User before the request. Each test runs a real deletion transaction, held
// after its lock of the User until the link waits for it, then removes the User's links in it and commits, while the
// real link runs; neither may fail with a deadlock, and the link, which reads the request's state from its locked
// row, is rejected as token_used. The deletion leaves the User's status alone, so that only the voided request can
// reject the link.

// TestLiveTelegramTokenLinkAgainstUserDeletionLockOrder: `/start <token>` while the token's User is deleted.
func TestLiveTelegramTokenLinkAgainstUserDeletionLockOrder(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupUserDeletion(t, s)
		st, err := l.svc.StartTelegram(t.Context(), l.bob, "CNAAAAAAAAAAT1")
		if err != nil {
			t.Fatal(err)
		}
		_, tok, _ := strings.Cut(st.DeepLink, "?start=")
		var reply string
		l.againstUserDeletion(t, func(ctx context.Context) error {
			return l.svc.LinkTelegram(ctx, accountlinks.TelegramConnection{ID: l.conns["CNAAAAAAAAAAT1"],
				PublicID: "CNAAAAAAAAAAT1"}, tok, accountlinks.TelegramAccount{ID: 7201},
				func(_ context.Context, text string) error {
					reply = text
					return nil
				})
		})
		if reply != "This link is invalid or expired. Start again from your Muster profile." {
			t.Errorf("reply %q", reply)
		}
		l.rejectedAsUsed(t, "telegram")
	})
}

// TestLiveBridgeTokenLinkAgainstUserDeletionLockOrder: an account_link event with a token while the token's User is
// deleted.
func TestLiveBridgeTokenLinkAgainstUserDeletionLockOrder(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupUserDeletion(t, s)
		st, err := l.svc.StartBridge(t.Context(), l.bob, "CNAAAAAAAAAAB1")
		if err != nil {
			t.Fatal(err)
		}
		var answer string
		l.againstUserDeletion(t, func(ctx context.Context) error {
			out, err := l.svc.LinkBridge(ctx, accountlinks.BridgeConnection{ID: l.conns["CNAAAAAAAAAAB1"],
				PublicID: "CNAAAAAAAAAAB1"}, st.Token, accountlinks.BridgeAccount{ExternalID: "u-bob",
				DisplayName: "Bob Brown"})
			if err != nil {
				return err
			}
			out.Log(ctx, nil)
			answer = out.Answer
			return nil
		})
		if answer != "This link is invalid or expired. Start again from your Muster profile." {
			t.Errorf("answer %q", answer)
		}
		l.rejectedAsUsed(t, "bridge")
	})
}

// userDeletionLive is the database of the lock-order tests of token links: a Telegram and a Bridge Connection, and
// bob, whose deletion the tests race.
type userDeletionLive struct {
	d     *db.DB
	org   int64
	bobID int64
	bob   accountlinks.Requester
	svc   *accountlinks.Service
	conns map[string]int64
	log   *bytes.Buffer
}

func setupUserDeletion(t *testing.T, s dbtest.Server) *userDeletionLive {
	t.Helper()
	ctx := t.Context()
	conn := config.Database{URL: logging.Secret(s.NewDatabase(t)), SSLMode: "disable"}
	d, err := db.Open(ctx, conn, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	l := &userDeletionLive{d: d, conns: map[string]int64{}, log: &bytes.Buffer{}}
	logger := logging.New(l.log, logging.LevelInfo)
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
	l.org = org.ID
	if _, err := d.Pool.Exec(ctx, `CREATE TABLE audit_log_p202610 PARTITION OF audit_log
		FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2026-11-01 00:00:00+00')`); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ id, typ string }{{"CNAAAAAAAAAAT1", "telegram"}, {"CNAAAAAAAAAAB1", "bridge"}} {
		var id int64
		if err := d.Pool.QueryRow(ctx, `INSERT INTO connections (org_id, public_id, type, name,
			telegram_bot_api_base_url, telegram_update_mode, bridge_base_url, bridge_event_delivery,
			bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, bot_username, limiter_limit,
			limiter_per_seconds, created_at, updated_at) VALUES ($1, $2, $3, $3,
			CASE WHEN $3 = 'telegram' THEN 'https://api.telegram.org' END,
			CASE WHEN $3 = 'telegram' THEN 'long_polling' END, CASE WHEN $3 = 'bridge' THEN 'https://br.example.org' END,
			CASE WHEN $3 = 'bridge' THEN 'poll' END, '\x00', 'k1', $4, 'muster_dev_bot', 10, 1, $4, $4)
			RETURNING id`, org.ID, c.id, c.typ, t0).Scan(&id); err != nil {
			t.Fatal(err)
		}
		l.conns[c.id] = id
	}
	const bobPID = "SRAAAAAAAAAAG1"
	if err := d.Pool.QueryRow(ctx, `INSERT INTO users (org_id, public_id, login, name, role, source, status,
		created_at, updated_at) VALUES ($1, $2, 'bob', 'Bob', 'responder', 'local', 'active', $3, $3) RETURNING id`,
		org.ID, bobPID, t0).Scan(&l.bobID); err != nil {
		t.Fatal(err)
	}
	l.bob = accountlinks.Requester{UserID: l.bobID, UserPublicID: bobPID, Actor: audit.User(l.bobID, bobPID),
		Transport: audit.TransportUI, Address: netip.MustParseAddr("192.0.2.1")}
	business := clock.NewManual(t0)
	l.svc = accountlinks.New(accountlinks.Config{OrgID: org.ID, Store: accountlinks.NewStore(d.Pool),
		Audit: audit.NewWriter(logger, business), Business: business, Log: logger})
	return l
}

// againstUserDeletion deletes bob's links as users.Admin.Delete does — one transaction that locks bob (LockUser) and
// then calls RemoveUserLinks — and holds that transaction after the lock while link runs, until link waits for a lock
// the transaction holds or ends; then the deletion goes on and commits. Both must succeed.
func (l *userDeletionLive) againstUserDeletion(t *testing.T, link func(ctx context.Context) error) {
	t.Helper()
	ctx := t.Context()
	tx, err := l.d.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var held int32
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM users WHERE org_id = $1 AND public_id = $2 FOR NO KEY UPDATE`, l.org,
		l.bob.UserPublicID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- link(ctx) }()
	var linkErr error
	ended := false
	for i := 0; ; i++ {
		select {
		case linkErr = <-done:
			ended = true
		default:
		}
		if ended {
			t.Error("the link ended without waiting for the deletion")
			break
		}
		var n int
		if err := l.d.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND $1::int4 = ANY(pg_blocking_pids(pid))`, held).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			break
		}
		if i == 1000 {
			t.Fatal("the link never waited for the deletion")
		}
		time.Sleep(10 * time.Millisecond)
	}
	after, err := l.svc.RemoveUserLinks(ctx, tx, l.bobID, audit.System, audit.TransportUI, netip.Addr{})
	if err == nil {
		err = tx.Commit(ctx)
	} else {
		_ = tx.Rollback(ctx)
	}
	if err != nil {
		t.Errorf("the deletion: %v", err)
	}
	if after != nil {
		after(ctx)
	}
	if !ended {
		linkErr = <-done
	}
	if linkErr != nil {
		t.Errorf("the link: %v", linkErr)
	}
}

// rejectedAsUsed checks that the link through messenger linked nothing and was rejected as token_used for bob, with
// his request voided by the deletion and not consumed.
func (l *userDeletionLive) rejectedAsUsed(t *testing.T, messenger string) {
	t.Helper()
	count := func(q string, args ...any) int {
		var n int
		if err := l.d.Pool.QueryRow(t.Context(), q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count(`SELECT count(*) FROM audit_log WHERE action = 'account_link.link_rejected' AND
		details->>'reason' = 'token_used' AND details->>'messenger' = $1 AND details->>'user' = $2`, messenger,
		l.bob.UserPublicID) != 1 || count(`SELECT count(*) FROM audit_log WHERE action = 'account_link.link'`) != 0 ||
		count(`SELECT count(*) FROM account_links`) != 0 || count(`SELECT count(*) FROM account_link_requests
		WHERE user_id = $1 AND voided_at IS NOT NULL AND consumed_at IS NULL`, l.bobID) != 1 {
		t.Errorf("the state after the race:\n%s", l.log)
	}
	if strings.Contains(l.log.String(), "deadlock") {
		t.Errorf("the log has a deadlock:\n%s", l.log)
	}
}

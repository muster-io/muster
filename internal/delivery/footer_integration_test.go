// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package delivery_test

import (
	"context"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/accountlinks"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/destinations"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/mattermost"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/tokens"
	"github.com/muster-io/muster/internal/users"
)

// footerLive is the live delivery runtime with the services that change whom a footer names (C-12.FR-12): the
// administration and the profiles of Users, the Service accounts and the Account links, with a Mattermost bot that
// knows every username and keeps its direct messages.
type footerLive struct {
	*live
	admin   *users.Admin
	profile *users.Service
	tokens  *tokens.Service
	links   *accountlinks.Service
	bot     *footerBot
	// aliceID is the User of the fixtures, robot a Service account that may snooze, bob a second User.
	aliceID int64
	robot   groups.Caller
	bob     groups.Caller
	snooze  groups.Caller
}

func setupFooterLive(t *testing.T, s dbtest.Server) *footerLive {
	t.Helper()
	l := setupLive(t, s)
	w := audit.NewWriter(l.logger, l.business)
	public, err := url.Parse("http://localhost:8080")
	if err != nil {
		t.Fatal(err)
	}
	f := &footerLive{live: l, admin: users.NewAdmin(l.orgID, users.NewAdminStore(l.d.Pool), w, l.business, public),
		profile: users.NewService(l.orgID, users.NewStore(l.d.Pool), w, l.business),
		tokens: tokens.New(l.orgID, tokens.NewStore(l.d.Pool), w, l.business,
			auth.Roles{auth.RoleResponder: {groups.PermissionSnooze}}, nil),
		bot: &footerBot{}, aliceID: l.alice.Actor.ID}
	f.links = accountlinks.New(accountlinks.Config{OrgID: l.orgID, Store: accountlinks.NewStore(l.d.Pool), Audit: w,
		Business: l.business, Log: l.logger, Mattermost: f.bot})
	perms := []auth.Permission{groups.PermissionAcknowledge, groups.PermissionResolve, groups.PermissionSnooze}
	f.snooze = groups.Caller{Actor: l.alice.Actor, Transport: audit.TransportUI, Permissions: perms}
	sa, err := f.tokens.CreateServiceAccount(t.Context(), tokens.Requester{Actor: audit.System,
		Transport: audit.TransportAPI}, tokens.ServiceAccountInput{Name: "robot", Role: auth.RoleResponder})
	if err != nil {
		t.Fatal(err)
	}
	f.robot = groups.Caller{Actor: audit.ServiceAccount(sa.ID, sa.PublicID), Transport: audit.TransportAPI,
		Permissions: perms}
	var bob int64
	if err := l.d.Pool.QueryRow(t.Context(), `INSERT INTO users (org_id, public_id, login, name, role, source,
		status, created_at, updated_at) VALUES ($1, 'SRAAAAAAAAAAA2', 'bob', 'bob', 'responder', 'local', 'active', $2,
		$2) RETURNING id`, l.orgID, t0).Scan(&bob); err != nil {
		t.Fatal(err)
	}
	f.bob = groups.Caller{Actor: audit.User(bob, "SRAAAAAAAAAAA2"), Transport: audit.TransportUI, Permissions: perms}
	return f
}

// footerBot is the Mattermost bot of the footer tests: it finds anyone and keeps the codes it sends.
type footerBot struct {
	mu   sync.Mutex
	sent []string
}

func (b *footerBot) Session(ctx context.Context, _ string,
	f func(context.Context, accountlinks.MattermostBot) error) error {
	return f(ctx, b)
}

func (b *footerBot) FindUser(_ context.Context, username string) (accountlinks.MattermostAccount, error) {
	return accountlinks.MattermostAccount{ID: "u-" + username, Username: username}, nil
}

func (b *footerBot) SendDirect(_ context.Context, _, text string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sent = append(b.sent, text)
	return nil
}

var footerCode = regexp.MustCompile(`[2-9A-HJKMNP-Z]{8}`)

// linkMattermost links alice's account username of the Mattermost Connection of the fixtures, as her profile does.
func (f *footerLive) linkMattermost(t *testing.T, username string) {
	t.Helper()
	r := accountlinks.Requester{UserID: f.aliceID, UserPublicID: f.alice.Actor.PublicID, Actor: f.alice.Actor,
		Transport: audit.TransportUI}
	st, err := f.links.StartMattermost(t.Context(), r, "CNAAAAAAAAAAA1", username)
	if err != nil {
		t.Fatal(err)
	}
	f.bot.mu.Lock()
	code := footerCode.FindString(f.bot.sent[len(f.bot.sent)-1])
	f.bot.mu.Unlock()
	if _, err := f.links.ConfirmMattermost(t.Context(), r, st.RequestPublicID, code); err != nil {
		t.Fatal(err)
	}
}

// TestIntegrationFooterUserRerender is C-12.FR-12 and C-11.FR-3 on PostgreSQL, with the renderer, the Account links
// and delivery of the runtime: in two Mattermost Destinations, alice owns the open Alert Group FuA and snoozed FuB, the
// Service account robot snoozed FuC, bob owns FuD, FuE fires, and alice resolved FuF. A rename of alice by an Admin
// and through her profile, a Mattermost Account link she makes, a new username of it and its unlink, and a rename of
// robot each write one open_group_updates row in the transaction of the change, whose run edits each Root message
// whose footer names that person once, Quietly, with no Mention and no new message — the new name, then the link's
// username as the footer's Mention — and no other Root message. The same values again write no row, two quick renames
// write one, and a second run of each row edits nothing.
func TestIntegrationFooterUserRerender(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		f := setupFooterLive(t, s)
		ctx := t.Context()
		f.fresh(t, 1000, 1)
		f.attach(t, f.dests...)
		w := f.worker("fu")
		updates := f.updates()
		f.fire(t, "fu", "FuA/a", "FuB/a", "FuC/a", "FuD/a", "FuE/a", "FuF/a")
		until := groups.SnoozeEnd{Until: new(f.business.Now().Add(4 * time.Hour))}
		for _, c := range []struct {
			name string
			do   func(id string) error
		}{
			{"FuA", func(id string) error { _, err := f.groups.Acknowledge(ctx, f.snooze, id); return err }},
			{"FuB", func(id string) error { _, err := f.groups.Snooze(ctx, f.snooze, id, until); return err }},
			{"FuC", func(id string) error { _, err := f.groups.Snooze(ctx, f.robot, id, until); return err }},
			{"FuD", func(id string) error { _, err := f.groups.Acknowledge(ctx, f.bob, id); return err }},
			{"FuF", func(id string) error { _, err := f.groups.Resolve(ctx, f.snooze, id, nil); return err }},
		} {
			if err := c.do(f.group(t, c.name)); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
		}
		f.round(t, w)
		if n := f.count(t, `SELECT count(*) FROM deliveries WHERE state = 'delivered'`); n != 12 {
			t.Fatalf("%d of 12 delivered\n%s", n, f.log)
		}
		space := accountlinks.SpaceMattermost(f.conn)
		rows := func(t *testing.T, kind string) int64 {
			t.Helper()
			return f.count(t, `SELECT count(*) FROM open_group_updates WHERE org_id = $1 AND kind = $2`, f.orgID, kind)
		}
		run := func(t *testing.T) []deliverytest.Call {
			t.Helper()
			f.rec.Reset()
			if _, err := updates.Round(ctx); err != nil {
				t.Fatal(err)
			}
			f.round(t, w)
			if n := f.count(t, `SELECT count(*) FROM open_group_updates`); n != 0 {
				t.Fatalf("%d rows left\n%s", n, f.log)
			}
			return f.rec.Calls()
		}
		entries := f.count(t, `SELECT count(*) FROM timeline_entries`)
		// edited checks that the run edited the Root messages of the Alert Groups named, each in both Destinations
		// once, Quietly, with no Mention, showing what shows says, and nothing else.
		edited := func(t *testing.T, what string, calls []deliverytest.Call, names []string,
			shows func(m delivery.Message) bool) {
			t.Helper()
			seen := map[string]int{}
			for _, c := range calls {
				if c.Method != deliverytest.MethodUpdate || c.Loudness != groups.Quiet || len(c.Mentions) != 0 ||
					!shows(c.Message) {
					t.Errorf("%s: %s %s, %s, mentions %v, footer %q %v", what, c.Method, head(c.Message), c.Loudness,
						c.Mentions, c.Message.Footer, c.Message.FooterUsers)
				}
				if fs := strings.Fields(head(c.Message)); len(fs) > 1 {
					seen[fs[1]]++
				}
			}
			if len(calls) != 2*len(names) {
				t.Errorf("%s: %d calls %s, want %d edits", what, len(calls), methods(calls), 2*len(names))
			}
			for _, n := range names {
				if seen[n] != 2 {
					t.Errorf("%s: %s edited %d times, want once in each destination; %v", what, n, seen[n], seen)
				}
			}
		}
		// again inserts a row of kind for the person by hand and runs it: it edits nothing.
		again := func(t *testing.T, what, kind string, person groups.Caller) {
			t.Helper()
			column := "user_id"
			if kind == groups.KindServiceAccountName {
				column = "service_account_id"
			}
			f.exec(t, `INSERT INTO open_group_updates (org_id, kind, `+column+`, requested_at) VALUES ($1, $2, $3, $4)`,
				f.orgID, kind, person.Actor.ID, f.business.Now())
			if calls := run(t); len(calls) != 0 {
				t.Errorf("%s: a second run made %s", what, methods(calls))
			}
		}
		footer := func(name string) func(m delivery.Message) bool {
			return func(m delivery.Message) bool {
				return strings.Contains(m.Footer, " by "+name) && m.FooterUsers == nil
			}
		}
		ab := []string{"FuA", "FuB"}

		admin := users.Requester{Actor: audit.System, Transport: audit.TransportAPI}
		rename := func(t *testing.T, name string) {
			t.Helper()
			if _, err := f.admin.Update(ctx, admin, f.alice.Actor.PublicID, nil,
				users.Changes{Name: name, Role: auth.RoleResponder}); err != nil {
				t.Fatal(err)
			}
		}
		rename(t, "Alice Renamed")
		if n := rows(t, groups.KindUserName); n != 1 {
			t.Fatalf("a rename: %d rows of user_name", n)
		}
		edited(t, "a rename", run(t), ab, footer("Alice Renamed"))
		rename(t, "Alice Renamed")
		if n := rows(t, groups.KindUserName); n != 0 {
			t.Errorf("the same name again wrote %d rows", n)
		}
		again(t, "a rename", groups.KindUserName, f.snooze)

		// Two quick changes before a worker claims the row write one; its run renders the name as it is then.
		if _, err := f.profile.UpdateProfile(ctx, f.aliceID, f.alice.Actor, users.Profile{Name: "Alice P"},
			netip.Addr{}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.profile.UpdateProfile(ctx, f.aliceID, f.alice.Actor, users.Profile{Name: "Alice Profile"},
			netip.Addr{}); err != nil {
			t.Fatal(err)
		}
		if n := rows(t, groups.KindUserName); n != 1 {
			t.Fatalf("two profile updates: %d rows of user_name", n)
		}
		edited(t, "a profile update", run(t), ab, footer("Alice Profile"))

		// An Account link makes the footer mention alice in its identity space.
		mention := func(username string) func(m delivery.Message) bool {
			return func(m delivery.Message) bool {
				u, ok := m.FooterUsers[space]
				return ok && u.Username == username && u.Name == "Alice Profile" &&
					strings.Contains(m.Footers[space], messages.FooterUserToken)
			}
		}
		f.linkMattermost(t, "alice_mm")
		if n := rows(t, groups.KindAccountLinks); n != 1 {
			t.Fatalf("a link: %d rows of account_links", n)
		}
		edited(t, "a link", run(t), ab, mention("alice_mm"))
		f.linkMattermost(t, "alice_mm")
		if n := rows(t, groups.KindAccountLinks); n != 0 {
			t.Errorf("the same account again wrote %d rows", n)
		}
		again(t, "a link", groups.KindAccountLinks, f.snooze)
		// A rename and a new link before a worker claims the row write one row.
		rename(t, "Alice Linked")
		f.linkMattermost(t, "alice_new")
		if n := f.count(t, `SELECT count(*) FROM open_group_updates`); n != 1 {
			t.Fatalf("a rename and a new link: %d rows", n)
		}
		edited(t, "a rename and a new link", run(t), ab, func(m delivery.Message) bool {
			u, ok := m.FooterUsers[space]
			return ok && u.Username == "alice_new" && u.Name == "Alice Linked"
		})
		var link string
		if err := f.d.Pool.QueryRow(ctx, `SELECT public_id FROM account_links WHERE user_id = $1`,
			f.aliceID).Scan(&link); err != nil {
			t.Fatal(err)
		}
		if err := f.links.Unlink(ctx, accountlinks.Requester{UserID: f.aliceID, UserPublicID: f.alice.Actor.PublicID,
			Actor: f.alice.Actor, Transport: audit.TransportUI}, link); err != nil {
			t.Fatal(err)
		}
		if n := rows(t, groups.KindAccountLinks); n != 1 {
			t.Fatalf("an unlink: %d rows of account_links", n)
		}
		edited(t, "an unlink", run(t), ab, footer("Alice Linked"))

		sa := tokens.Requester{Actor: audit.System, Transport: audit.TransportAPI}
		if _, err := f.tokens.UpdateServiceAccount(ctx, sa, f.robot.Actor.PublicID, nil,
			tokens.ServiceAccountInput{Name: "terraform", Role: auth.RoleResponder}); err != nil {
			t.Fatal(err)
		}
		if n := rows(t, groups.KindServiceAccountName); n != 1 {
			t.Fatalf("a rename of the service account: %d rows", n)
		}
		edited(t, "a rename of the service account", run(t), []string{"FuC"}, footer("terraform"))
		if _, err := f.tokens.UpdateServiceAccount(ctx, sa, f.robot.Actor.PublicID, nil,
			tokens.ServiceAccountInput{Name: "terraform", Role: auth.RoleResponder}); err != nil {
			t.Fatal(err)
		}
		if n := rows(t, groups.KindServiceAccountName); n != 0 {
			t.Errorf("the same service account name again wrote %d rows", n)
		}
		again(t, "a rename of the service account", groups.KindServiceAccountName, f.robot)

		// A re-render has no lifecycle event, so it records nothing in the Timeline.
		if n := f.count(t, `SELECT count(*) FROM timeline_entries`); n != entries {
			t.Errorf("the re-renders recorded %d timeline entries", n-entries)
		}
		f.noDeadlock(t)
	})
}

// TestSnoozeAgainstRenameLockOrder is C-12.FR-12 against the lock order of a Snooze: while a Snooze by alice, and one by
// the Service account robot, holds its Alert Group and is about to render the footer with the old name, alice, and
// robot, are renamed, and alice unlinks her Account link. A Snooze takes the row of who snoozes FOR SHARE before the
// Alert Group (sharePerson), so each change waits for it instead of committing its row before the Snooze's Alert Group
// is visible; the run of that row then edits the Root messages with the new footer. Neither ends in a deadlock.
func TestSnoozeAgainstRenameLockOrder(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		f := setupFooterLive(t, s)
		ctx := t.Context()
		f.fresh(t, 1000, 1)
		updates := f.updates()
		f.fire(t, "fl", "FlA/a", "FlB/a", "FlC/a")
		f.round(t, f.a)
		f.linkMattermost(t, "alice_mm")
		if _, err := updates.Round(ctx); err != nil {
			t.Fatal(err)
		}
		var link string
		if err := f.d.Pool.QueryRow(ctx, `SELECT public_id FROM account_links WHERE user_id = $1`,
			f.aliceID).Scan(&link); err != nil {
			t.Fatal(err)
		}
		until := groups.SnoozeEnd{Until: new(f.business.Now().Add(4 * time.Hour))}
		// Each change edits the Root messages whose footer names the person, the one just snoozed included.
		for _, c := range []struct {
			name, group string
			edited      []string
			who         groups.Caller
			change      func(ctx context.Context) error
			shows       func(m delivery.Message) bool
		}{
			{"a rename of the user", "FlA", []string{"FlA"}, f.snooze, func(ctx context.Context) error {
				_, err := f.admin.Update(ctx, users.Requester{Actor: audit.System, Transport: audit.TransportAPI},
					f.alice.Actor.PublicID, nil, users.Changes{Name: "Alice Held", Role: auth.RoleResponder})
				return err
			}, func(m delivery.Message) bool {
				return m.FooterUsers[accountlinks.SpaceMattermost(f.conn)].Name == "Alice Held"
			}},
			{"an unlink", "FlB", []string{"FlA", "FlB"}, f.snooze, func(ctx context.Context) error {
				return f.links.Unlink(ctx, accountlinks.Requester{UserID: f.aliceID,
					UserPublicID: f.alice.Actor.PublicID, Actor: f.alice.Actor, Transport: audit.TransportUI}, link)
			}, func(m delivery.Message) bool {
				return m.FooterUsers == nil && strings.Contains(m.Footer, " by Alice Held")
			}},
			{"a rename of the service account", "FlC", []string{"FlC"}, f.robot, func(ctx context.Context) error {
				_, err := f.tokens.UpdateServiceAccount(ctx, tokens.Requester{Actor: audit.System,
					Transport: audit.TransportAPI}, f.robot.Actor.PublicID, nil,
					tokens.ServiceAccountInput{Name: "robot held", Role: auth.RoleResponder})
				return err
			}, func(m delivery.Message) bool { return strings.Contains(m.Footer, " by robot held") }},
		} {
			g := newTxGate()
			f.groups.SetRerender(func(ctx context.Context, tx groups.DBTX, r groups.Rendering) error {
				if err := g.hold(ctx, tx); err != nil {
					return err
				}
				return f.svc.Enqueue(ctx, tx, r)
			})
			id := f.group(t, c.group)
			blocked, snoozeErr, changeErr := f.against(t, g, func(ctx context.Context) error {
				_, err := f.groups.Snooze(ctx, c.who, id, until)
				return err
			}, c.change)
			f.groups.SetRerender(f.svc.Enqueue)
			if !blocked || snoozeErr != nil || changeErr != nil {
				t.Fatalf("%s: waited %v; snooze %v; change %v", c.name, blocked, snoozeErr, changeErr)
			}
			f.round(t, f.a)
			f.rec.Reset()
			if _, err := updates.Round(ctx); err != nil {
				t.Fatal(err)
			}
			f.round(t, f.a)
			calls := f.rec.Calls()
			if len(calls) != len(c.edited) {
				t.Errorf("%s: calls %s, footer %q, want edits of %v", c.name, methods(calls), footerOf(calls), c.edited)
			}
			seen := map[string]int{}
			for _, call := range calls {
				if fs := strings.Fields(head(call.Message)); len(fs) > 1 {
					seen[fs[1]]++
				}
				if call.Method != deliverytest.MethodUpdate || call.Loudness != groups.Quiet || len(call.Mentions) != 0 ||
					!c.shows(call.Message) {
					t.Errorf("%s: %s %s, footer %q %v", c.name, call.Method, head(call.Message), call.Message.Footer,
						call.Message.FooterUsers)
				}
			}
			for _, n := range c.edited {
				if seen[n] != 1 {
					t.Errorf("%s: %s edited %d times, want once; %v", c.name, n, seen[n], seen)
				}
			}
		}
		f.noDeadlock(t)
	})
}

// moveChecker passes the Destination check of any channel of the Mattermost Connections of the fixtures, as the
// Connections do for a bot that is in the channel.
type moveChecker struct{ l *live }

func (c moveChecker) CheckChannel(ctx context.Context, in destinations.ChannelCheck) (destinations.ChannelChecked,
	error) {
	var id int64
	if err := c.l.d.Pool.QueryRow(ctx, `SELECT id FROM connections WHERE org_id = $1 AND public_id = $2`, c.l.orgID,
		in.Connection).Scan(&id); err != nil {
		return destinations.ChannelChecked{}, destinations.ErrUnknownConnection
	}
	return destinations.ChannelChecked{ConnectionID: id, Check: mattermost.Check{TeamName: "team",
		ChannelName: in.ChannelID, Outcome: delivery.Outcome{Kind: delivery.OutcomeOK},
		Steps: []mattermost.Step{{Name: mattermost.StepToken, OK: true}}}}, nil
}

// anyMentions takes every Mention setting, and reads and writes the Mention entries as mentions does.
type anyMentions struct{ *mentions.Service }

func (anyMentions) Validate(context.Context, string, mentions.Settings, []mentions.EntryInput) error {
	return nil
}

// moveLive is the footer runtime with a second Mattermost Connection B, where alice has an Account link, and the
// destinations service that moves the Destination of the fixtures between Connections; the open Alert Group CcA,
// acknowledged by alice, has its Root message published in that Destination of Connection A, where she has no link,
// and an Alert queued an edit of it that was not sent yet.
type moveLive struct {
	*footerLive
	connB   int64
	saves   *destinations.Service
	w       *delivery.Worker
	updates *groups.UpdateWorker
	r       accountlinks.Requester
}

func setupMoveLive(t *testing.T, s dbtest.Server) *moveLive {
	t.Helper()
	f := setupFooterLive(t, s)
	ctx := t.Context()
	m := &moveLive{footerLive: f}
	if err := f.d.Pool.QueryRow(ctx, `INSERT INTO connections (org_id, public_id, type, name, mattermost_server_url,
		bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds, created_at,
		updated_at) VALUES ($1, 'CNAAAAAAAAAAB2', 'mattermost', 'bot b', 'https://mm-b.example.org', '\x00', 'k1', $2,
		1000, 1, $2, $2) RETURNING id`, f.orgID, t0).Scan(&m.connB); err != nil {
		t.Fatal(err)
	}
	m.saves = destinations.New(f.orgID, destinations.NewStore(f.d.Pool))
	m.saves.SetWriter(destinations.WriterConfig{Writer: destinations.NewWriter(f.d.Pool),
		Audit: audit.NewWriter(f.logger, f.business), Business: f.business, Mentions: anyMentions{mentions.New(f.orgID, f.d.Pool)},
		Mattermost: moveChecker{l: f.live}, Log: f.logger})
	m.r = accountlinks.Requester{UserID: f.aliceID, UserPublicID: f.alice.Actor.PublicID, Actor: f.alice.Actor,
		Transport: audit.TransportUI}
	st, err := f.links.StartMattermost(ctx, m.r, "CNAAAAAAAAAAB2", "alice_b")
	if err != nil {
		t.Fatal(err)
	}
	f.bot.mu.Lock()
	code := footerCode.FindString(f.bot.sent[len(f.bot.sent)-1])
	f.bot.mu.Unlock()
	if _, err := f.links.ConfirmMattermost(ctx, m.r, st.RequestPublicID, code); err != nil {
		t.Fatal(err)
	}
	f.fresh(t, 1000, 1)
	f.attach(t, f.dests[0])
	m.w, m.updates = f.worker("cc"), f.updates()
	f.fire(t, "cc", "CcA/a")
	if _, err := f.groups.Acknowledge(ctx, f.snooze, f.group(t, "CcA")); err != nil {
		t.Fatal(err)
	}
	m.runUpdates(t)
	f.round(t, m.w)
	if calls := f.rec.Calls(); len(calls) == 0 || mentionIn(calls[len(calls)-1]) != "" {
		t.Fatalf("the root message in A: %s\n%s", methods(calls), f.log)
	}
	f.fire(t, "cc", "CcA/a", "CcA/b")
	return m
}

// move moves the Destination of the fixtures to Connection B through the destinations service; again, it is an update
// that keeps the Connection.
func (m *moveLive) move(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	d, err := m.saves.Get(ctx, "DSAAAAAAAAAAA1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.saves.Update(ctx, destinations.Requester{Actor: audit.System, Transport: audit.TransportAPI},
		d.PublicID, nil, destinations.Input{Type: destinations.TypeMattermost, Name: d.Name,
			Mentions: mentions.Settings{}, Limiter: destinations.Limiter{Limit: 1000, PerSeconds: 1},
			Mattermost: &destinations.MattermostInput{Connection: "CNAAAAAAAAAAB2", TeamID: "team",
				ChannelID: *d.MattermostChannelID}}); err != nil {
		t.Fatal(err)
	}
}

// unlink removes alice's link of B, which writes a row account_links.
func (m *moveLive) unlink(t *testing.T) {
	t.Helper()
	var link string
	if err := m.d.Pool.QueryRow(t.Context(), `SELECT public_id FROM account_links WHERE user_id = $1`,
		m.aliceID).Scan(&link); err != nil {
		t.Fatal(err)
	}
	if err := m.links.Unlink(t.Context(), m.r, link); err != nil {
		t.Fatal(err)
	}
}

func (m *moveLive) runUpdates(t *testing.T) {
	t.Helper()
	if _, err := m.updates.Round(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := m.count(t, `SELECT count(*) FROM open_group_updates`); n != 0 {
		t.Fatalf("%d rows left\n%s", n, m.log)
	}
}

// edits are the edits of Root messages the recorder made since its last reset; the rounds also send Thread replies.
func (m *moveLive) edits() []deliverytest.Call {
	var out []deliverytest.Call
	for _, c := range m.rec.Calls() {
		if c.Method == deliverytest.MethodUpdate {
			out = append(out, c)
		}
	}
	return out
}

// right reports whether an edit names alice by her name, Quietly, with no Mention, through Connection B.
func (m *moveLive) right(c deliverytest.Call) bool {
	return c.Loudness == groups.Quiet && len(c.Mentions) == 0 && mentionIn(c) == "" &&
		*c.Destination.Connection == m.connB && c.Message.FooterUsers[accountlinks.SpaceMattermost(m.connB)].Name == "" &&
		strings.Contains(c.Message.Footer, " by alice")
}

func mentionIn(c deliverytest.Call) string {
	space := mentions.IdentitySpace(c.Destination.Type, c.Destination.Connection)
	return c.Message.WithFooterIn(space, func(u mentions.User) string { return "@" + u.Username }).FooterMention
}

// TestIntegrationConnectionChangeRerender is the case of #549, C-12.FR-12, on PostgreSQL with the destinations,
// the Account links, the renderer and delivery of the runtime (setupMoveLive): alice removes her link of B, and the run
// of its row edits nothing in A but refreshes the stored message without her footer of B. An update of the Destination
// that moves it to B records /connection_id in destination.updated and writes one row destination_connection in its
// transaction; the queued edit, sent after the move, names alice by her name with no Mention, and the run of the row
// edits nothing more, since her footer reads the same in B. A second run edits nothing, and an update that keeps the
// Connection writes no row.
func TestIntegrationConnectionChangeRerender(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		m := setupMoveLive(t, s)
		m.unlink(t)
		m.runUpdates(t)
		if n := m.count(t, `SELECT count(*) FROM deliveries WHERE state = 'pending'
			AND desired_payload::text NOT LIKE '%alice_b%'`); n != 1 {
			t.Fatalf("the queued edit after the unlink: %d pending without the old mention of B", n)
		}
		audits := m.count(t, `SELECT count(*) FROM audit_log WHERE action = 'destination.updated'
			AND diff::text LIKE '%/connection_id%' AND diff::text LIKE '%CNAAAAAAAAAAB2%'`)
		m.move(t)
		if n := m.count(t, `SELECT count(*) FROM open_group_updates WHERE kind = 'destination_connection'
			AND destination_id = $1`, m.dests[0]); n != 1 {
			t.Fatalf("the move wrote %d rows of destination_connection", n)
		}
		if n := m.count(t, `SELECT count(*) FROM audit_log WHERE action = 'destination.updated'
			AND diff::text LIKE '%/connection_id%' AND diff::text LIKE '%CNAAAAAAAAAAB2%'`); n != audits+1 {
			t.Errorf("the move recorded %d entries with the connection", n-audits)
		}
		m.rec.Reset()
		m.round(t, m.w)
		m.runUpdates(t)
		m.round(t, m.w)
		if edits := m.edits(); len(edits) != 1 || !m.right(edits[0]) {
			t.Fatalf("the edits around the move: %s %+v\n%s", methods(m.rec.Calls()), edits, m.log)
		}

		m.rec.Reset()
		m.exec(t, `INSERT INTO open_group_updates (org_id, kind, destination_id, requested_at) VALUES ($1,
			'destination_connection', $2, $3)`, m.orgID, m.dests[0], m.business.Now())
		m.runUpdates(t)
		m.round(t, m.w)
		if calls := m.rec.Calls(); len(calls) != 0 {
			t.Errorf("a second run made %s", methods(calls))
		}
		m.move(t)
		if n := m.count(t, `SELECT count(*) FROM open_group_updates`); n != 0 {
			t.Errorf("an update that keeps the connection wrote %d rows", n)
		}
		m.noDeadlock(t)
	})
}

// TestIntegrationConnectionChangeAfterStaleSend is C-12.FR-12 on PostgreSQL for a send before the run of an unlink
// (setupMoveLive): alice removes her link of B, and the Destination moves to B before either row runs, so the queued
// edit goes out with her old Mention of B. Her footer reads the same in A and in B now, so the hashes are equal, but
// the run finds the stored message reading otherwise in B and edits the Root message once more, Quietly, with no
// Mention; a second run edits nothing.
func TestIntegrationConnectionChangeAfterStaleSend(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		m := setupMoveLive(t, s)
		m.unlink(t)
		m.move(t)
		m.rec.Reset()
		m.round(t, m.w)
		if edits := m.edits(); len(edits) != 1 || mentionIn(edits[0]) != "@alice_b" {
			t.Fatalf("the queued edit before the runs: %s", methods(m.rec.Calls()))
		}
		m.rec.Reset()
		m.runUpdates(t)
		m.round(t, m.w)
		if edits := m.edits(); len(edits) != 1 || !m.right(edits[0]) {
			t.Fatalf("the repair: %s %+v\n%s", methods(m.rec.Calls()), edits, m.log)
		}
		m.rec.Reset()
		m.exec(t, `INSERT INTO open_group_updates (org_id, kind, destination_id, requested_at) VALUES ($1,
			'destination_connection', $2, $3)`, m.orgID, m.dests[0], m.business.Now())
		m.runUpdates(t)
		m.round(t, m.w)
		if calls := m.rec.Calls(); len(calls) != 0 {
			t.Errorf("a second run made %s", methods(calls))
		}
		m.noDeadlock(t)
	})
}

// TestIntegrationConnectionChangeAlone is C-12.FR-12 on PostgreSQL for the walk of a move on its own
// (setupMoveLive): alice keeps her link of B, and the queued edit is delivered in A, where she has none, before the
// Destination moves to B. Nothing else renders the Alert Group, so only the run of the row destination_connection that
// the move writes edits its Root message into her Mention of B, once, Quietly, with no Mention of the message.
func TestIntegrationConnectionChangeAlone(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		m := setupMoveLive(t, s)
		m.round(t, m.w)
		m.rec.Reset()
		m.move(t)
		if n := m.count(t, `SELECT count(*) FROM open_group_updates WHERE kind = 'destination_connection'`); n != 1 {
			t.Fatalf("the move wrote %d rows of destination_connection", n)
		}
		m.runUpdates(t)
		m.round(t, m.w)
		edits := m.edits()
		if len(edits) != 1 || mentionIn(edits[0]) != "@alice_b" || edits[0].Loudness != groups.Quiet ||
			len(edits[0].Mentions) != 0 || *edits[0].Destination.Connection != m.connB {
			t.Fatalf("the walk: %s %+v\n%s", methods(m.rec.Calls()), edits, m.log)
		}
		m.noDeadlock(t)
	})
}

// TestIntegrationChainsStopped is C-29.FR-1 item 7 and C-32.AC-2 for the read of a Root message on PostgreSQL: the
// footer of an acknowledgement says "· Escalation chains stopped" only when the acknowledgement stopped a run of the
// current start of the chains — not when every chain ran out before it, nor for a run an earlier start's
// acknowledgement stopped — and the Owner has no Team.
func TestIntegrationChainsStopped(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		ctx := t.Context()
		l.fire(t, "stopped", "Stopped/a")
		gid := l.group(t, "Stopped")
		if _, err := l.groups.Acknowledge(ctx, l.alice, gid); err != nil {
			t.Fatal(err)
		}
		var id int64
		if err := l.d.Pool.QueryRow(ctx, `UPDATE alert_groups SET chain_generation = 2 WHERE public_id = $1
			RETURNING id`, gid).Scan(&id); err != nil {
			t.Fatal(err)
		}
		run := func(publicID string, generation int, state, stop string) {
			t.Helper()
			if _, err := l.d.Pool.Exec(ctx, `INSERT INTO escalation_runs (org_id, public_id, alert_group_id,
				chain_name, sources, steps, state, generation, started_at, ended_at, stop_command)
				VALUES ($1, $2, $3, 'devops', '[{"kind":"route"}]', '[]', $4, $5, $6, $6, nullif($7, ''))`, l.orgID,
				publicID, id, state, generation, t0, stop); err != nil {
				t.Fatal(err)
			}
		}
		footer := func() string {
			t.Helper()
			src, err := l.renderer.Load(ctx, l.d.Pool, id)
			if err != nil {
				t.Fatal(err)
			}
			return l.renderer.Root(src, messages.MarkupMarkdown).Message.Footer
		}
		if f := footer(); strings.Contains(f, "Escalation chains stopped") {
			t.Errorf("without runs: %q", f)
		}
		// Every chain of the current start ran out before the acknowledgement, and an earlier start's acknowledgement
		// stopped its run: the acknowledgement stopped nothing.
		run("ER0000000000R1", 2, "ran_out", "")
		run("ER0000000000S1", 1, "stopped", "acknowledge")
		if f := footer(); strings.Contains(f, "Escalation chains stopped") {
			t.Errorf("every chain ran out: %q", f)
		}
		run("ER0000000000S2", 2, "stopped", "acknowledge")
		if f := footer(); !strings.HasSuffix(f, " · Escalation chains stopped") || strings.Contains(f, "(") {
			t.Errorf("a stopped chain: %q", f)
		}
	})
}

// footerOf is the footer of the first call, for an error message.
func footerOf(calls []deliverytest.Call) string {
	if len(calls) == 0 {
		return ""
	}
	return calls[0].Message.Footer
}

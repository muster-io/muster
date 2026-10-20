// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package connections_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/bridge"
	"github.com/muster-io/muster/internal/bridge/gen"
	"github.com/muster-io/muster/internal/connections"
	"github.com/muster-io/muster/internal/connections/dbgen"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/fakes/fakeslack"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/mattermost"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/outbound"
)

// ListMattermostDestinations names each Destination of targets dest-<id>.
func (s *memStore) ListMattermostDestinations(_ context.Context, org int64) ([]dbgen.ListMattermostDestinationsRow,
	error) {
	if err := s.fail["ListMattermostDestinations"]; err != nil || org != 1 {
		return nil, err
	}
	var out []dbgen.ListMattermostDestinationsRow
	for _, id := range slices.Sorted(maps.Keys(s.targets)) {
		d := s.targets[id]
		out = append(out, dbgen.ListMattermostDestinationsRow{ID: id, PublicID: fmt.Sprintf("DS%012d", id),
			Name: fmt.Sprintf("dest-%d", id), ConnectionID: pgtype.Int8{Int64: d.connection, Valid: true},
			MattermostTeamID:    pgtype.Text{String: d.teamID, Valid: true},
			MattermostChannelID: pgtype.Text{String: d.channelID, Valid: true}})
	}
	return out, nil
}

// ListTelegramDestinations names each Destination of tgTargets tg-dest-<id>.
func (s *memStore) ListTelegramDestinations(_ context.Context, org int64) ([]dbgen.ListTelegramDestinationsRow,
	error) {
	if err := s.fail["ListTelegramDestinations"]; err != nil || org != 1 {
		return nil, err
	}
	var out []dbgen.ListTelegramDestinationsRow
	for _, id := range slices.Sorted(maps.Keys(s.tgTargets)) {
		d := s.tgTargets[id]
		out = append(out, dbgen.ListTelegramDestinationsRow{ID: id, PublicID: fmt.Sprintf("DS%012d", id),
			Name: fmt.Sprintf("tg-dest-%d", id), ConnectionID: pgtype.Int8{Int64: d.connection, Valid: true},
			TelegramChannelID: pgtype.Text{String: d.channel, Valid: true}})
	}
	return out, nil
}

// ListBridgeDestinations names each Destination of brTargets br-dest-<id>.
func (s *memStore) ListBridgeDestinations(_ context.Context, org int64) ([]dbgen.ListBridgeDestinationsRow, error) {
	if err := s.fail["ListBridgeDestinations"]; err != nil || org != 1 {
		return nil, err
	}
	var out []dbgen.ListBridgeDestinationsRow
	for _, id := range slices.Sorted(maps.Keys(s.brTargets)) {
		d := s.brTargets[id]
		out = append(out, dbgen.ListBridgeDestinationsRow{ID: id, PublicID: fmt.Sprintf("DS%012d", id),
			Name: fmt.Sprintf("br-dest-%d", id), ConnectionID: pgtype.Int8{Int64: d.connection, Valid: true},
			BridgeChannelID: pgtype.Text{String: d.channel, Valid: true}})
	}
	return out, nil
}

// ListSlackDestinations lists no Slack Destination; slackDoctorStore lists them for the tests of Slack.
func (s *memStore) ListSlackDestinations(_ context.Context, org int64) ([]dbgen.ListSlackDestinationsRow, error) {
	if err := s.fail["ListSlackDestinations"]; err != nil || org != 1 {
		return nil, err
	}
	return nil, nil
}

// slackDoctorStore is the memStore with the Slack Destinations that muster doctor lists.
type slackDoctorStore struct {
	*memStore
	dests []dbgen.ListSlackDestinationsRow
}

func (s slackDoctorStore) ListSlackDestinations(ctx context.Context, org int64) ([]dbgen.ListSlackDestinationsRow,
	error) {
	if _, err := s.memStore.ListSlackDestinations(ctx, org); err != nil {
		return nil, err
	}
	return s.dests, nil
}

// slackDest is the Slack Destination sl-dest-<id> on channel through the Connection connection.
func slackDest(id, connection int64, channel string) dbgen.ListSlackDestinationsRow {
	return dbgen.ListSlackDestinationsRow{ID: id, PublicID: fmt.Sprintf("DS%012d", id), Name: fmt.Sprintf("sl-dest-%d", id),
		ConnectionID: pgtype.Int8{Int64: connection, Valid: true}, SlackChannelID: pgtype.Text{String: channel, Valid: true}}
}

func findings(t *testing.T, svc *connections.Service, q connections.DoctorQueries) string {
	t.Helper()
	found, err := svc.Doctor(t.Context(), q, 1500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range found {
		line := f.Kind + " " + f.Name + ": ok"
		if f.OK() && f.Warning != "" {
			line = f.Kind + " " + f.Name + ": warning: " + f.Warning
		}
		if !f.OK() {
			line = f.Kind + " " + f.Name + ": " + f.Message
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// TestDoctor is C-02.FR-14: muster doctor checks every Connection and Mattermost Destination that is not deleted in the
// background class, and records nothing; a failing step gives its message — a Telegram Connection's with the step's
// name — and a bot that may not make ephemeral posts the hint that press answers show in the Thread (D284).
func TestDoctor(t *testing.T) {
	e := newEnv(t)
	mm := e.create(t, "mm")
	broken := e.create(t, "broken")
	e.store.rows[broken.ID].row.BotTokenCiphertext = []byte("garbage")
	gone := e.create(t, "gone")
	e.store.rows[gone.ID].deleted = true
	e.store.addTelegram()
	e.store.targets[7] = memTarget{connection: mm.ID, teamID: fakemattermost.TeamID,
		channelID: fakemattermost.ChannelAlerts}
	e.store.targets[8] = memTarget{connection: mm.ID, teamID: fakemattermost.TeamID,
		channelID: fakemattermost.ChannelNoBot}
	e.store.targets[9] = memTarget{connection: broken.ID, teamID: "t", channelID: "c"}
	e.store.targets[10] = memTarget{connection: gone.ID, teamID: "t", channelID: "c"}
	e.store.targets[11] = memTarget{connection: mm.ID, teamID: "team-other", channelID: fakemattermost.ChannelAlerts}
	background := metrics.ClientRequests.With(string(outbound.ClassBackground), string(outbound.OutcomeOK))
	before, audits, txs := background.Get(), len(e.store.audit), e.store.tx

	got := findings(t, e.svc, e.store)
	open := "open the bot token of the connection " + broken.PublicID + ": cannot decrypt"
	want := []string{"connection mm: warning: " + mattermost.HintPressAnswersInThread, "connection broken: " + open,
		"connection tg-" + telegramID + `: dry_probe: Get "http://127.0.0.1:1/bot0:x/getMe": dial tcp`,
		"destination dest-7: ok",
		"destination dest-8: " + mattermost.MessageNotMember, "destination dest-9: its Connection failed its check: " + open,
		"destination dest-10: its Connection is deleted", "destination dest-11: " + mattermost.MessageOtherTeam}
	lines := strings.Split(got, "\n")
	if len(lines) != len(want) {
		t.Fatalf("findings:\n%s", got)
	}
	for i, w := range want {
		if !strings.HasPrefix(lines[i], w) {
			t.Errorf("line %d = %q, want %q", i, lines[i], w)
		}
	}
	if n := background.Get() - before; n < 5 {
		t.Errorf("%d background requests", n)
	}
	if len(e.store.audit) != audits || e.store.tx != txs || len(e.store.bots) != 0 || len(e.path.subjects) != 0 {
		t.Errorf("the doctor wrote: audit %d, transactions %d, bots %d, interactive %d", len(e.store.audit)-audits,
			e.store.tx-txs, len(e.store.bots), len(e.path.subjects))
	}

	e.configure(t, `{"bot_system_admin":true}`)
	if got := findings(t, e.svc, e.store); !strings.HasPrefix(got, "connection mm: ok\n") {
		t.Errorf("an admin bot:\n%s", got)
	}
	e.configure(t, `{"bot_system_admin":false}`)
	if err := e.fake.SetFault(fakeserver.Fault{Path: "/api/v4/roles/names", Status: 503, Times: 100}); err != nil {
		t.Fatal(err)
	}
	if got := findings(t, e.svc, e.store); !strings.HasPrefix(got, "connection mm: ok\n") {
		t.Errorf("roles unreadable:\n%s", got)
	}
	e.fake.ResetFaults()
	e.configure(t, `{"revoked_tokens":["`+botToken+`"]}`)
	if got := findings(t, e.svc, e.store); !strings.HasPrefix(got, "connection mm: "+mattermost.MessageTokenInvalid) ||
		!strings.Contains(got, "destination dest-7: its Connection failed its check: "+
			mattermost.MessageTokenInvalid) {
		t.Errorf("revoked:\n%s", got)
	}
	e.configure(t, `{"revoked_tokens":[]}`)
	if err := e.fake.SetFault(fakeserver.Fault{Path: "/api/v4/users/me", Status: 503, Times: 100}); err != nil {
		t.Fatal(err)
	}
	if got := findings(t, e.svc, e.store); !strings.HasPrefix(got, "connection mm: ") ||
		!strings.Contains(strings.SplitN(got, "\n", 2)[0], "answered 503") {
		t.Errorf("unavailable:\n%s", got)
	}
	if strings.Contains(e.log.String(), botToken) {
		t.Error("the bot token reached the log")
	}

	for _, name := range []string{"ListConnections", "ListMattermostDestinations", "ListTelegramDestinations",
		"ListBridgeDestinations", "ListSlackDestinations"} {
		e.store.fail[name] = errors.New("down")
		if _, err := e.svc.Doctor(t.Context(), e.store, time.Second); err == nil || !strings.Contains(err.Error(), "down") {
			t.Errorf("%s failing = %v", name, err)
		}
		delete(e.store.fail, name)
	}

	noKeys := connections.New(connections.Config{OrgID: 1})
	if got := findings(t, noKeys, e.store); !strings.HasPrefix(got,
		"connection mm: the bot token cannot be opened: the master keys could not be loaded\n") {
		t.Errorf("without a keyring:\n%s", got)
	}
}

// TestDoctorBridge is C-02.FR-14 and C-22.FR-3 for Bridges: muster doctor checks each Bridge Connection with getInfo
// and whether the info offers its event delivery mode, and each Bridge Destination with checkChannel, in the background
// class; a failing one names the step or the item channel with its message, and a Destination whose Connection failed
// or is deleted says so. It stores nothing: no info, no Audit log entry, no transaction, nothing on the interactive
// path.
func TestDoctorBridge(t *testing.T) {
	e := newEnv(t)
	f := startFakeBridge(t)
	cfg := f.Config()
	cfg.EventDelivery = []gen.InfoEventDelivery{gen.Poll}
	cfg.Revoked = []string{fakebridge.SecondToken}
	if err := f.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	br := e.createFakeBridge(t, f, "br", fakebridge.Token, connections.EventDeliveryPoll)
	revoked := e.createFakeBridge(t, f, "revoked", fakebridge.SecondToken, connections.EventDeliveryPoll)
	push := e.createFakeBridge(t, f, "push", fakebridge.Token, connections.EventDeliveryPush)
	gone := e.createFakeBridge(t, f, "gone", fakebridge.Token, connections.EventDeliveryPoll)
	e.store.rows[gone.ID].deleted = true
	e.store.brTargets[21] = memBRTarget{connection: br.ID, channel: fakebridge.ChannelAlerts}
	e.store.brTargets[22] = memBRTarget{connection: br.ID, channel: fakebridge.ChannelNoBot}
	e.store.brTargets[23] = memBRTarget{connection: revoked.ID, channel: fakebridge.ChannelAlerts}
	e.store.brTargets[24] = memBRTarget{connection: push.ID, channel: fakebridge.ChannelAlerts}
	e.store.brTargets[25] = memBRTarget{connection: gone.ID, channel: fakebridge.ChannelAlerts}
	background := metrics.ClientRequests.With(string(outbound.ClassBackground), string(outbound.OutcomeOK))
	before, audits, txs, infos := background.Get(), len(e.store.audit), e.store.tx, len(e.store.infos)
	f.ResetRequests()

	got := findings(t, e.svc, e.store)
	notOffered := "event_delivery: The bridge does not offer push event delivery."
	want := []string{"connection br: ok", "connection revoked: info: " + bridge.MessageTokenInvalid,
		"connection push: " + notOffered,
		"destination br-dest-21: ok", "destination br-dest-22: channel: " + fakebridge.MessageNotMember,
		"destination br-dest-23: its Connection failed its check: info: " + bridge.MessageTokenInvalid,
		"destination br-dest-24: its Connection failed its check: " + notOffered,
		"destination br-dest-25: its Connection is deleted"}
	if got != strings.Join(want, "\n") {
		t.Fatalf("findings:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
	var paths []string
	for _, r := range f.Requests() {
		paths = append(paths, r.Method+" "+r.Path)
	}
	wantPaths := []string{"GET /v1/info", "GET /v1/info", "GET /v1/info", "POST /v1/channels/ch-alerts/check",
		"POST /v1/channels/ch-nobot/check"}
	if !slices.Equal(paths, wantPaths) {
		t.Errorf("requests %q, want %q", paths, wantPaths)
	}
	if n := background.Get() - before; n < 3 {
		t.Errorf("%d background requests", n)
	}
	if len(e.store.audit) != audits || e.store.tx != txs || len(e.store.infos) != infos || len(e.path.subjects) != 0 {
		t.Errorf("the doctor wrote: audit %d, transactions %d, infos %d, interactive %d", len(e.store.audit)-audits,
			e.store.tx-txs, len(e.store.infos)-infos, len(e.path.subjects))
	}

	cfg.Channels = map[string]*string{}
	if err := f.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if got := findings(t, e.svc, e.store); !strings.Contains(got, "destination br-dest-21: channel: "+
		fakebridge.MessageNotMember) {
		t.Errorf("a channel the bot lost:\n%s", got)
	}
	e.store.rows[br.ID].row.BotTokenCiphertext = []byte("garbage")
	if got := findings(t, e.svc, e.store); !strings.HasPrefix(got, "connection br: open the token of the connection") {
		t.Errorf("a token that cannot be opened:\n%s", got)
	}
	noKeys := connections.New(connections.Config{OrgID: 1})
	if got := findings(t, noKeys, e.store); !strings.HasPrefix(got,
		"connection br: the bot token cannot be opened: the master keys could not be loaded\n") {
		t.Errorf("without a keyring:\n%s", got)
	}
}

// TestDoctorSlack is C-02.FR-14 and C-24.FR-2, FR-3 for Slack: muster doctor checks each Slack Connection with the
// steps of its check — token, scopes and in socket mode app_token — and each Slack Destination with conversations.info,
// in the background class at once, never on the interactive path; a failing one names the step or the item channel
// with its message, the text of bot_not_in_channel naming the bot the check found; a Destination whose Connection
// failed or is deleted says so. It stores nothing: no workspace, no Audit log entry, no transaction.
func TestDoctorSlack(t *testing.T) {
	e, f := newSlackEnv(t)
	sl := e.createSlack(t, slackInput("sl", connections.EventModeSocket))
	bad := slackInput("bad", connections.EventModeHTTP)
	bad.BotToken = keyring.Replace("xoxb-muster-doctor-unknown")
	revoked := e.createSlack(t, bad)
	gone := e.createSlack(t, slackInput("gone", connections.EventModeHTTP))
	e.store.rows[gone.ID].deleted = true
	q := slackDoctorStore{memStore: e.store, dests: []dbgen.ListSlackDestinationsRow{
		slackDest(31, sl.ID, fakeslack.ChannelAlerts), slackDest(32, sl.ID, fakeslack.ChannelOther),
		slackDest(33, revoked.ID, fakeslack.ChannelAlerts), slackDest(34, gone.ID, fakeslack.ChannelAlerts),
		slackDest(35, sl.ID, "C0FAKEGONE")}}
	background := metrics.ClientRequests.With(string(outbound.ClassBackground), string(outbound.OutcomeOK))
	before, audits, txs, ids := background.Get(), len(e.store.audit), e.store.tx, len(e.store.slackIDs)
	subjects := len(e.path.subjects)

	got := findings(t, e.svc, q)
	want := []string{"connection sl: ok", "connection bad: token: Slack refused the token.",
		"destination sl-dest-31: ok",
		"destination sl-dest-32: channel: Invite the bot to the channel: /invite @muster-dev-bot",
		"destination sl-dest-33: its Connection failed its check: token: Slack refused the token.",
		"destination sl-dest-34: its Connection is deleted",
		"destination sl-dest-35: channel: The channel was not found, or the bot cannot see it."}
	if got != strings.Join(want, "\n") {
		t.Fatalf("findings:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
	var paths []string
	for _, r := range f.Requests() {
		paths = append(paths, r.Path)
	}
	wantPaths := []string{"/api/auth.test", "/api/apps.connections.open", "/api/auth.test", "/api/bots.info",
		"/api/auth.test", "/api/conversations.info", "/api/conversations.info", "/api/conversations.info"}
	if !slices.Equal(paths, wantPaths) {
		t.Errorf("requests %q, want %q", paths, wantPaths)
	}
	if n := background.Get() - before; n < 5 {
		t.Errorf("%d background requests", n)
	}
	if len(e.store.audit) != audits || e.store.tx != txs || len(e.store.slackIDs) != ids ||
		len(e.path.subjects) != subjects {
		t.Errorf("the doctor wrote: audit %d, transactions %d, identities %d, interactive %d",
			len(e.store.audit)-audits, e.store.tx-txs, len(e.store.slackIDs)-ids, len(e.path.subjects)-subjects)
	}

	cfg := fakeslack.DefaultConfig()
	cfg.Scopes[fakeslack.AppA] = "chat:write,channels:read,groups:read,im:write,users:read"
	if err := f.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if got := findings(t, e.svc, q); !strings.HasPrefix(got, "connection sl: scopes: The bot token lacks the scopes "+
		"users:read.email.\n") || !strings.Contains(got, "destination sl-dest-31: its Connection failed its check: "+
		"scopes:") {
		t.Errorf("a missing scope:\n%s", got)
	}
	if err := f.SetConfig(fakeslack.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	row := &e.store.rows[sl.ID].row
	v := row.SlackAppTokenCiphertext
	row.SlackAppTokenCiphertext = []byte("garbage")
	if got := findings(t, e.svc, q); !strings.HasPrefix(got, "connection sl: open the app-level token of the connection") {
		t.Errorf("a secret that cannot be opened:\n%s", got)
	}
	row.SlackAppTokenCiphertext = v
	e.store.fail["ListSlackDestinations"] = errors.New("down")
	if _, err := e.svc.Doctor(t.Context(), q, time.Second); err == nil || !strings.Contains(err.Error(), "down") {
		t.Errorf("ListSlackDestinations failing = %v", err)
	}
	delete(e.store.fail, "ListSlackDestinations")
	noKeys := connections.New(connections.Config{OrgID: 1})
	if got := findings(t, noKeys, q); !strings.HasPrefix(got,
		"connection sl: the bot token cannot be opened: the master keys could not be loaded\n") ||
		!strings.Contains(got, "destination sl-dest-31: its Connection failed its check: the bot token cannot be opened") {
		t.Errorf("without a keyring:\n%s", got)
	}
}

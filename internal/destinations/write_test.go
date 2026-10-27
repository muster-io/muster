// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package destinations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	auditdb "github.com/muster-io/muster/internal/audit/dbgen"
	"github.com/muster-io/muster/internal/bridge"
	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/destinations/dbgen"
	"github.com/muster-io/muster/internal/keyring"
	keyringdb "github.com/muster-io/muster/internal/keyring/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mattermost"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/proxyconf"
	"github.com/muster-io/muster/internal/slack"
	"github.com/muster-io/muster/internal/telegram"
	"github.com/muster-io/muster/internal/templates"
	"github.com/muster-io/muster/internal/webhooks"
)

// The deletion's fake answers the queries of a save as a database without Mattermost Connections would.
func (f *fakeWriter) LockMattermostConnection(context.Context, dbgen.LockMattermostConnectionParams) (int64,
	error) {
	return 0, pgx.ErrNoRows
}

func (f *fakeWriter) InsertMattermostDestination(context.Context, dbgen.InsertMattermostDestinationParams) (int64,
	error) {
	return 0, errBoom
}

func (f *fakeWriter) UpdateMattermostDestination(context.Context, dbgen.UpdateMattermostDestinationParams) error {
	return errBoom
}

func (f *fakeWriter) LockTelegramConnection(context.Context, dbgen.LockTelegramConnectionParams) (int64, error) {
	return 0, pgx.ErrNoRows
}

func (f *fakeWriter) InsertTelegramDestination(context.Context, dbgen.InsertTelegramDestinationParams) (int64,
	error) {
	return 0, errBoom
}

func (f *fakeWriter) UpdateTelegramDestination(context.Context, dbgen.UpdateTelegramDestinationParams) error {
	return errBoom
}

func (f *fakeWriter) LockBridgeConnection(context.Context, dbgen.LockBridgeConnectionParams) (int64, error) {
	return 0, pgx.ErrNoRows
}

func (f *fakeWriter) InsertBridgeDestination(context.Context, dbgen.InsertBridgeDestinationParams) (int64, error) {
	return 0, errBoom
}

func (f *fakeWriter) UpdateBridgeDestination(context.Context, dbgen.UpdateBridgeDestinationParams) error {
	return errBoom
}

func (f *fakeWriter) LockSlackConnection(context.Context, dbgen.LockSlackConnectionParams) (int64, error) {
	return 0, pgx.ErrNoRows
}

func (f *fakeWriter) InsertSlackDestination(context.Context, dbgen.InsertSlackDestinationParams) (int64, error) {
	return 0, errBoom
}

func (f *fakeWriter) UpdateSlackDestination(context.Context, dbgen.UpdateSlackDestinationParams) error {
	return errBoom
}

func (f *fakeWriter) InsertWebhookDestination(context.Context, dbgen.InsertWebhookDestinationParams) (int64, error) {
	return 0, errBoom
}

func (f *fakeWriter) UpdateWebhookDestination(context.Context, dbgen.UpdateWebhookDestinationParams) error {
	return errBoom
}

func (f *fakeWriter) LockOwningTeam(context.Context, dbgen.LockOwningTeamParams) (dbgen.LockOwningTeamRow, error) {
	return dbgen.LockOwningTeamRow{}, errBoom
}

func (f *fakeWriter) SetDestinationOwningTeam(context.Context, dbgen.SetDestinationOwningTeamParams) error {
	return errBoom
}

func (f *fakeWriter) InsertDestinationButtonsUpdate(context.Context, dbgen.InsertDestinationButtonsUpdateParams) error {
	return errBoom
}

func (f *fakeWriter) NotifyOpenGroupUpdates(context.Context, string) error { return errBoom }

// saveWriter is the database of the saves in memory: the Mattermost Connections that are not deleted, the inserted
// and updated rows, the versions the locks read, the Audit log and the hints.
type saveWriter struct {
	conns    map[int64]bool
	versions map[string]int64
	inserted []dbgen.InsertMattermostDestinationParams
	updated  []dbgen.UpdateMattermostDestinationParams
	tgIns    []dbgen.InsertTelegramDestinationParams
	tgSets   []dbgen.UpdateTelegramDestinationParams
	brIns    []dbgen.InsertBridgeDestinationParams
	brSets   []dbgen.UpdateBridgeDestinationParams
	slIns    []dbgen.InsertSlackDestinationParams
	slSets   []dbgen.UpdateSlackDestinationParams
	hooks    []dbgen.InsertWebhookDestinationParams
	hookSets []dbgen.UpdateWebhookDestinationParams
	audit    []auditdb.InsertAuditEntryParams
	hints    []db.Hint
	fail     map[string]error
	auditErr error
	// teams are the Teams that are not deleted by public_id, and owners the owning Teams written.
	teams  map[string]dbgen.LockOwningTeamRow
	owners []dbgen.SetDestinationOwningTeamParams
	// buttonUpdates are the open_group_updates rows destination_buttons, and woken the notifications on their channel.
	buttonUpdates []dbgen.InsertDestinationButtonsUpdateParams
	woken         []string
}

func (w *saveWriter) LockOwningTeam(_ context.Context, arg dbgen.LockOwningTeamParams) (dbgen.LockOwningTeamRow,
	error) {
	if err := w.fail["LockOwningTeam"]; err != nil {
		return dbgen.LockOwningTeamRow{}, err
	}
	t, ok := w.teams[arg.PublicID]
	if !ok || arg.OrgID != 1 {
		return dbgen.LockOwningTeamRow{}, pgx.ErrNoRows
	}
	return t, nil
}

func (w *saveWriter) SetDestinationOwningTeam(_ context.Context, arg dbgen.SetDestinationOwningTeamParams) error {
	if err := w.fail["SetDestinationOwningTeam"]; err != nil {
		return err
	}
	w.owners = append(w.owners, arg)
	return nil
}

func (w *saveWriter) InsertDestinationButtonsUpdate(_ context.Context,
	arg dbgen.InsertDestinationButtonsUpdateParams) error {
	if err := w.fail["InsertDestinationButtonsUpdate"]; err != nil {
		return err
	}
	w.buttonUpdates = append(w.buttonUpdates, arg)
	return nil
}

func (w *saveWriter) NotifyOpenGroupUpdates(_ context.Context, channel string) error {
	if err := w.fail["NotifyOpenGroupUpdates"]; err != nil {
		return err
	}
	w.woken = append(w.woken, channel)
	return nil
}

func (w *saveWriter) InTx(_ context.Context, fn func(TxQueries) error) error { return fn(w) }

func (w *saveWriter) LockDestination(_ context.Context, arg dbgen.LockDestinationParams) (dbgen.LockDestinationRow,
	error) {
	v, ok := w.versions[arg.PublicID]
	if !ok {
		return dbgen.LockDestinationRow{}, pgx.ErrNoRows
	}
	return dbgen.LockDestinationRow{PublicID: arg.PublicID, Version: v}, w.fail["LockDestination"]
}

func (w *saveWriter) MarkDestinationDeleted(context.Context, dbgen.MarkDestinationDeletedParams) error {
	return errBoom
}

func (w *saveWriter) DeleteDestinationRoutes(context.Context, dbgen.DeleteDestinationRoutesParams) error {
	return errBoom
}

func (w *saveWriter) LockMattermostConnection(_ context.Context, arg dbgen.LockMattermostConnectionParams) (int64,
	error) {
	if err := w.fail["LockMattermostConnection"]; err != nil {
		return 0, err
	}
	if !w.conns[arg.ID] {
		return 0, pgx.ErrNoRows
	}
	return arg.ID, nil
}

func (w *saveWriter) InsertMattermostDestination(_ context.Context, arg dbgen.InsertMattermostDestinationParams) (
	int64, error) {
	if err := w.fail["InsertMattermostDestination"]; err != nil {
		return 0, err
	}
	w.inserted = append(w.inserted, arg)
	return int64(10 + len(w.inserted)), nil
}

func (w *saveWriter) UpdateMattermostDestination(_ context.Context, arg dbgen.UpdateMattermostDestinationParams) error {
	if err := w.fail["UpdateMattermostDestination"]; err != nil {
		return err
	}
	w.updated = append(w.updated, arg)
	return nil
}

func (w *saveWriter) LockTelegramConnection(_ context.Context, arg dbgen.LockTelegramConnectionParams) (int64,
	error) {
	if err := w.fail["LockTelegramConnection"]; err != nil {
		return 0, err
	}
	if !w.conns[arg.ID] {
		return 0, pgx.ErrNoRows
	}
	return arg.ID, nil
}

func (w *saveWriter) InsertTelegramDestination(_ context.Context, arg dbgen.InsertTelegramDestinationParams) (int64,
	error) {
	if err := w.fail["InsertTelegramDestination"]; err != nil {
		return 0, err
	}
	w.tgIns = append(w.tgIns, arg)
	return int64(30 + len(w.tgIns)), nil
}

func (w *saveWriter) UpdateTelegramDestination(_ context.Context, arg dbgen.UpdateTelegramDestinationParams) error {
	if err := w.fail["UpdateTelegramDestination"]; err != nil {
		return err
	}
	w.tgSets = append(w.tgSets, arg)
	return nil
}

func (w *saveWriter) LockBridgeConnection(_ context.Context, arg dbgen.LockBridgeConnectionParams) (int64, error) {
	if err := w.fail["LockBridgeConnection"]; err != nil {
		return 0, err
	}
	if !w.conns[arg.ID] {
		return 0, pgx.ErrNoRows
	}
	return arg.ID, nil
}

func (w *saveWriter) InsertBridgeDestination(_ context.Context, arg dbgen.InsertBridgeDestinationParams) (int64,
	error) {
	if err := w.fail["InsertBridgeDestination"]; err != nil {
		return 0, err
	}
	w.brIns = append(w.brIns, arg)
	return int64(40 + len(w.brIns)), nil
}

func (w *saveWriter) UpdateBridgeDestination(_ context.Context, arg dbgen.UpdateBridgeDestinationParams) error {
	if err := w.fail["UpdateBridgeDestination"]; err != nil {
		return err
	}
	w.brSets = append(w.brSets, arg)
	return nil
}

func (w *saveWriter) LockSlackConnection(_ context.Context, arg dbgen.LockSlackConnectionParams) (int64, error) {
	if err := w.fail["LockSlackConnection"]; err != nil {
		return 0, err
	}
	if !w.conns[arg.ID] {
		return 0, pgx.ErrNoRows
	}
	return arg.ID, nil
}

func (w *saveWriter) InsertSlackDestination(_ context.Context, arg dbgen.InsertSlackDestinationParams) (int64,
	error) {
	if err := w.fail["InsertSlackDestination"]; err != nil {
		return 0, err
	}
	w.slIns = append(w.slIns, arg)
	return int64(50 + len(w.slIns)), nil
}

func (w *saveWriter) UpdateSlackDestination(_ context.Context, arg dbgen.UpdateSlackDestinationParams) error {
	if err := w.fail["UpdateSlackDestination"]; err != nil {
		return err
	}
	w.slSets = append(w.slSets, arg)
	return nil
}

func (w *saveWriter) InsertWebhookDestination(_ context.Context, arg dbgen.InsertWebhookDestinationParams) (int64,
	error) {
	if err := w.fail["InsertWebhookDestination"]; err != nil {
		return 0, err
	}
	w.hooks = append(w.hooks, arg)
	return int64(20 + len(w.hooks)), nil
}

func (w *saveWriter) UpdateWebhookDestination(_ context.Context, arg dbgen.UpdateWebhookDestinationParams) error {
	if err := w.fail["UpdateWebhookDestination"]; err != nil {
		return err
	}
	w.hookSets = append(w.hookSets, arg)
	return nil
}

func (w *saveWriter) InsertAuditEntry(_ context.Context, arg auditdb.InsertAuditEntryParams) error {
	if w.auditErr != nil {
		return w.auditErr
	}
	w.audit = append(w.audit, arg)
	return nil
}

func (w *saveWriter) Notify(_ context.Context, h db.Hint) error {
	w.hints = append(w.hints, h)
	return nil
}

func (w *saveWriter) DB() dbgen.DBTX { return nil }

// fakeChecker stands for the Connections: the Destination check of a channel, by its id.
type fakeChecker struct {
	calls []ChannelCheck
	links []outbound.Links
	err   error
}

func (c *fakeChecker) CheckChannel(ctx context.Context, in ChannelCheck) (ChannelChecked, error) {
	c.calls, c.links = append(c.calls, in), append(c.links, outbound.LinksOf(ctx))
	if c.err != nil {
		return ChannelChecked{}, c.err
	}
	if in.Connection != "CNAAAAAAAAAAA1" {
		return ChannelChecked{}, ErrUnknownConnection
	}
	ok := delivery.Outcome{Kind: delivery.OutcomeOK}
	fatal := delivery.Outcome{Kind: delivery.OutcomeFatal, Error: "403"}
	switch in.ChannelID {
	case "ch-nobot":
		return ChannelChecked{ConnectionID: 5, Check: mattermost.Check{Outcome: fatal, Steps: []mattermost.Step{
			{Name: mattermost.StepToken, OK: true},
			{Name: mattermost.StepBotInChannel, Message: mattermost.MessageNotMember, Code: mattermost.CodeNotMember,
				Outcome: fatal}}}}, nil
	case "revoked":
		return ChannelChecked{ConnectionID: 5, Check: mattermost.Check{Outcome: fatal, Steps: []mattermost.Step{
			{Name: mattermost.StepToken, Message: mattermost.MessageTokenInvalid, Code: mattermost.CodeTokenInvalid,
				Outcome: fatal}}}}, nil
	}
	return ChannelChecked{ConnectionID: 5, Check: mattermost.Check{Outcome: ok, TeamName: "dev",
		ChannelName: "alerts", Steps: []mattermost.Step{{Name: mattermost.StepToken, OK: true},
			{Name: mattermost.StepBotInChannel, OK: true}}}}, nil
}

// fakeTelegram stands for the Connections: the Destination check of a Telegram channel through the Connection
// CNAAAAAAAAAAA2, by its channel.
type fakeTelegram struct {
	calls []TelegramChannelCheck
	links []outbound.Links
	err   error
	title string
}

func (c *fakeTelegram) CheckTelegramChannel(ctx context.Context, in TelegramChannelCheck) (TelegramChannelChecked,
	error) {
	c.calls, c.links = append(c.calls, in), append(c.links, outbound.LinksOf(ctx))
	if c.err != nil {
		return TelegramChannelChecked{}, c.err
	}
	if in.Connection != "CNAAAAAAAAAAA2" {
		return TelegramChannelChecked{}, ErrUnknownConnection
	}
	fail := func(steps []telegram.DestinationStep) (TelegramChannelChecked, error) {
		last := steps[len(steps)-1]
		return TelegramChannelChecked{ConnectionID: 6, Check: telegram.DestinationCheck{Steps: steps,
			Outcome: last.Outcome}}, nil
	}
	fatal := func(name, code, message string) telegram.DestinationStep {
		return telegram.DestinationStep{Name: name, Message: message, Code: code,
			Outcome: delivery.Outcome{Kind: delivery.OutcomeFatal, Error: outbound.Untrusted(message)}}
	}
	ok := func(name string) telegram.DestinationStep { return telegram.DestinationStep{Name: name, OK: true} }
	switch in.ChannelID {
	case "@no_comments":
		return fail([]telegram.DestinationStep{ok(telegram.StepChannelExists),
			fatal(telegram.StepDiscussionGroup, telegram.CodeNoComments, telegram.MessageNoComments)})
	case "@no_group_admin":
		return fail([]telegram.DestinationStep{ok(telegram.StepChannelExists), ok(telegram.StepDiscussionGroup),
			ok(telegram.StepBotRightsChannel),
			fatal(telegram.StepBotRightsGroup, telegram.CodeGroupNotAdmin, telegram.MessageGroupNotAdmin("Muster alerts Chat"))})
	case "@revoked":
		return fail([]telegram.DestinationStep{fatal(telegram.StepChannelExists, telegram.CodeTokenInvalid,
			telegram.MessageTokenInvalid)})
	}
	title := c.title
	if title == "" {
		title = "Muster alerts"
	}
	return TelegramChannelChecked{ConnectionID: 6, Check: telegram.DestinationCheck{
		Steps: []telegram.DestinationStep{ok(telegram.StepChannelExists), ok(telegram.StepDiscussionGroup),
			ok(telegram.StepBotRightsChannel), ok(telegram.StepBotRightsGroup)},
		ChannelID: -1001000000001, ChannelTitle: title, GroupID: -1001000000002, GroupTitle: "Muster alerts Chat",
		Outcome: delivery.Outcome{Kind: delivery.OutcomeOK}}}, nil
}

// fakeBridge stands for the Connections: the Destination check of a Bridge channel through the Connection
// CNAAAAAAAAAAA3, by its channel id: ch-nobot is forbidden with the fake bridge's detail, revoked has its token
// refused, and any other channel passes with the name name, "alerts" when it is empty.
type fakeBridge struct {
	calls []BridgeChannelCheck
	links []outbound.Links
	err   error
	name  string
}

func (c *fakeBridge) CheckBridgeChannel(ctx context.Context, in BridgeChannelCheck) (BridgeChannelChecked, error) {
	c.calls, c.links = append(c.calls, in), append(c.links, outbound.LinksOf(ctx))
	if c.err != nil {
		return BridgeChannelChecked{}, c.err
	}
	if in.Connection != "CNAAAAAAAAAAA3" {
		return BridgeChannelChecked{}, ErrUnknownConnection
	}
	fatal := func(code, message string) BridgeChannelChecked {
		return BridgeChannelChecked{ConnectionID: 7, Check: bridge.ChannelCheck{Message: message, Code: code,
			Outcome: delivery.Outcome{Kind: delivery.OutcomeFatal, Error: outbound.Untrusted(message)}}}
	}
	switch in.ChannelID {
	case "ch-nobot":
		return fatal(bridge.CodeChannelForbidden, "The bot is not a member of this channel."), nil
	case "revoked":
		return fatal(bridge.CodeTokenInvalid, bridge.MessageTokenInvalid), nil
	}
	name := c.name
	if name == "" {
		name = "alerts"
	}
	return BridgeChannelChecked{ConnectionID: 7, Check: bridge.ChannelCheck{Name: name,
		Outcome: delivery.Outcome{Kind: delivery.OutcomeOK}}}, nil
}

// fakeSlack stands for the Connections: the Destination check of a Slack channel through the Connection CNAAAAAAAAAAA8,
// by its channel id: C0FAKEOTHER is without the bot, C0FAKEARCH archived, C0FAKEGONE unknown, CREVOKED has its token
// refused and CSCOPE lacks channels:read; any other channel passes with the name name, "alerts" when it is empty.
type fakeSlack struct {
	calls []SlackChannelCheck
	links []outbound.Links
	err   error
	name  string
}

func (c *fakeSlack) CheckSlackChannel(ctx context.Context, in SlackChannelCheck) (SlackChannelChecked, error) {
	c.calls, c.links = append(c.calls, in), append(c.links, outbound.LinksOf(ctx))
	if c.err != nil {
		return SlackChannelChecked{}, c.err
	}
	if in.Connection != "CNAAAAAAAAAAA8" {
		return SlackChannelChecked{}, ErrUnknownConnection
	}
	fatal := func(code, message string, scopes ...string) SlackChannelChecked {
		return SlackChannelChecked{ConnectionID: 8, Check: slack.ChannelCheck{Message: message, Code: code,
			MissingScopes: scopes, Outcome: delivery.Outcome{Kind: delivery.OutcomeFatal,
				Error: outbound.Untrusted(message)}}}
	}
	switch in.ChannelID {
	case "C0FAKEOTHER":
		return fatal(slack.CodeBotNotInChannel, "Invite the bot to the channel: /invite @muster-dev-bot-b"), nil
	case "C0FAKEARCH":
		return fatal(slack.CodeChannelArchived, slack.MessageChannelArchived), nil
	case "C0FAKEGONE":
		return fatal(slack.CodeChannelNotFound, slack.MessageChannelNotFound), nil
	case "CREVOKED":
		return fatal(slack.CodeTokenInvalid, slack.MessageTokenInvalid), nil
	case "CSCOPE":
		return fatal(slack.CodeMissingScope, slack.MissingScopesMessage([]string{"channels:read"}), "channels:read"), nil
	}
	name := c.name
	if name == "" {
		name = "alerts"
	}
	return SlackChannelChecked{ConnectionID: 8, Check: slack.ChannelCheck{Name: name,
		Outcome: delivery.Outcome{Kind: delivery.OutcomeOK}}}, nil
}

// fakeMentions refuses the User SR0000000000ZZ, as mentions.Validate does for a User that does not exist.
type fakeMentions struct{}

func (fakeMentions) Validate(_ context.Context, _ string, set mentions.Settings) error {
	for _, ids := range set {
		for _, id := range ids.UserIDs {
			if id == "SR0000000000ZZ" {
				return &mentions.FieldError{Pointer: "/mentions/new_alerts/user_ids/0", Code: CodeUnknownID,
					Detail: "No such user."}
			}
		}
	}
	return nil
}

func newSaver(t *testing.T) (*Service, *fakeStore, *saveWriter, *fakeChecker, *[]int64) {
	t.Helper()
	store := newStore()
	w := &saveWriter{conns: map[int64]bool{5: true, 6: true, 7: true, 8: true},
		versions: map[string]int64{"DSAAAAAAAAAAA1": 2, "DSAAAAAAAAAAA3": 4}, fail: map[string]error{},
		teams: map[string]dbgen.LockOwningTeamRow{"TM0000000000PA": {ID: 41, PublicID: "TM0000000000PA",
			Name: "Payments", Slug: "payments"}}}
	checker := &fakeChecker{}
	var healed []int64
	s := New(1, store)
	logger := logging.New(&bytes.Buffer{}, logging.LevelInfo)
	s.SetWriter(WriterConfig{Writer: w, Audit: audit.NewWriter(logger, clock.NewManual(t0)),
		Business: clock.NewManual(t0), Mentions: fakeMentions{}, Mattermost: checker, Telegram: &fakeTelegram{},
		Bridge: &fakeBridge{}, Slack: &fakeSlack{}, Keyring: activeKeyring(t),
		Templates: templates.New(clock.NewManual(t0), clock.Real{}),
		Healthy: func(_ context.Context, id int64) error {
			healed = append(healed, id)
			if id == 99 {
				return errBoom
			}
			for i := range store.rows {
				if store.rows[i].ID == id {
					store.rows[i].Health = "healthy"
				}
			}
			return nil
		}})
	return s, store, w, checker, &healed
}

func mattermostInput(name, channel string) Input {
	set := mentions.Settings{}
	for _, k := range mentions.Kinds {
		set[k] = mentions.Setting{Everyone: mentions.EveryoneNone, UserIDs: []string{}, Groups: []string{}}
	}
	n := set[mentions.Kinds[1]]
	n.Everyone = mentions.EveryoneChannel
	set[mentions.Kinds[1]] = n
	return Input{Type: TypeMattermost, Name: name, Mentions: set, Limiter: Limiter{Limit: 5, PerSeconds: 1},
		Mattermost: &MattermostInput{Connection: "cnaaaaaaaaaaa1", TeamID: "team-dev", ChannelID: channel}}
}

var saver = Requester{Actor: audit.User(1, "USAAAAAAAAAAA1"), Transport: audit.TransportAPI}

// TestCreateMattermost is createDestination of type mattermost (C-13.FR-2, FR-3, C-11.FR-18, C-12.FR-8): the check
// runs on the Connection before anything is saved; a passing one stores the team and channel names it read, the
// Mention settings and the limiter, and records destination.created with the hint.
func TestCreateMattermost(t *testing.T) {
	s, _, w, checker, _ := newSaver(t)
	d, err := s.Create(t.Context(), saver, mattermostInput(" alerts ", "ch-alerts"))
	if err != nil {
		t.Fatal(err)
	}
	if len(checker.calls) != 1 || checker.calls[0].Destination != nil || checker.calls[0].Connection != "CNAAAAAAAAAAA1" {
		t.Errorf("checks %+v", checker.calls)
	}
	if d.ID != 11 || d.Name != "alerts" || *d.Connection != "CNAAAAAAAAAAA1" || *d.MattermostTeamName != "dev" ||
		*d.MattermostChannelName != "alerts" || d.LimiterLimit != 5 || d.Health.State != "healthy" || d.Version != 1 {
		t.Errorf("created %+v", d)
	}
	var set mentions.Settings
	if err := json.Unmarshal(d.Mentions, &set); err != nil || set["new_alerts"].Everyone != "channel" {
		t.Errorf("mentions %s", d.Mentions)
	}
	ins := w.inserted[0]
	if ins.ConnectionID.Int64 != 5 || ins.TeamName.String != "dev" || ins.ChannelName.String != "alerts" ||
		ins.ChannelID.String != "ch-alerts" {
		t.Errorf("inserted %+v", ins)
	}
	if len(w.audit) != 1 || w.audit[0].Action != ActionCreated || len(w.hints) != 1 || w.hints[0].Type != Hint {
		t.Errorf("audit %+v hints %+v", w.audit, w.hints)
	}
	if got := counterSeries(d.PublicID); !slices.Equal(got, zeroCounterSeries(d.PublicID, TypeMattermost)) {
		t.Errorf("counter series %q", got)
	}
}

// TestCreateRefusals: a failing check is destination_check_failed at the field of each failing check and saves
// nothing; an unknown Connection, a User that does not exist, a taken name, an unsupported type and empty fields are
// refused before or instead of the save.
func TestCreateRefusals(t *testing.T) {
	s, _, w, checker, _ := newSaver(t)
	ctx := t.Context()
	_, err := s.Create(ctx, saver, mattermostInput("nobot", "ch-nobot"))
	var failed *CheckFailedError
	if !errors.As(err, &failed) || len(failed.Items) != 1 || failed.Items[0].Pointer != "/channel_id" ||
		failed.Items[0].Message != mattermost.MessageNotMember || failed.Items[0].Name != "bot_in_channel" ||
		failed.Items[0].Code != "bot_not_in_channel" ||
		!stringsContain(failed.Error(), "bot_in_channel") {
		t.Errorf("no bot = %v", err)
	}
	if _, err = s.Create(ctx, saver, mattermostInput("revoked", "revoked")); !errors.As(err, &failed) ||
		failed.Items[0].Pointer != "/connection_id" || failed.Items[0].Code != "token_invalid" {
		t.Errorf("revoked = %v", err)
	}
	in := mattermostInput("bad", "ch-alerts")
	in.Mattermost.Connection = "CNAAAAAAAAAAA9"
	assertField(t, "unknown connection", func() error { _, err := s.Create(ctx, saver, in); return err },
		"/connection_id", CodeUnknownID)
	in.Mattermost.Connection = "not-an-id"
	assertField(t, "malformed connection", func() error { _, err := s.Create(ctx, saver, in); return err },
		"/connection_id", CodeUnknownID)
	in = mattermostInput("bad", "ch-alerts")
	in.Mentions["new_alerts"] = mentions.Setting{Everyone: "none", UserIDs: []string{"SR0000000000ZZ"},
		Groups: []string{}}
	_, err = s.Create(ctx, saver, in)
	if fe, ok := errors.AsType[*mentions.FieldError](err); !ok || fe.Code != CodeUnknownID {
		t.Errorf("unknown user = %v", err)
	}
	for name, mut := range map[string]func(*Input){
		"telegram":     func(in *Input) { in.Type = TypeTelegram },
		"webhook":      func(in *Input) { in.Type = TypeWebhook },
		"no fields":    func(in *Input) { in.Mattermost = nil },
		"empty name":   func(in *Input) { in.Name = " " },
		"long name":    func(in *Input) { in.Name = string(bytes.Repeat([]byte("n"), 201)) },
		"limiter":      func(in *Input) { in.Limiter.Limit = 0 },
		"no team":      func(in *Input) { in.Mattermost.TeamID = "" },
		"no channel":   func(in *Input) { in.Mattermost.ChannelID = "" },
		"unknown type": func(in *Input) { in.Type = "slack" },
	} {
		in := mattermostInput("x", "ch-alerts")
		mut(&in)
		if _, err := s.Create(ctx, saver, in); !errorAsField(err) {
			t.Errorf("%s = %v", name, err)
		}
	}
	if len(w.inserted) != 0 {
		t.Fatalf("a refused save inserted %+v", w.inserted)
	}
	w.fail["InsertMattermostDestination"] = &pgconn.PgError{Code: uniqueViolation, ConstraintName: nameIndex}
	if _, err := s.Create(ctx, saver, mattermostInput("ops", "ch-alerts")); !errors.Is(err, ErrNameTaken) {
		t.Errorf("name taken = %v", err)
	}
	w.fail["InsertMattermostDestination"] = errBoom
	if _, err := s.Create(ctx, saver, mattermostInput("ops", "ch-alerts")); !errors.Is(err, errBoom) {
		t.Errorf("insert failed = %v", err)
	}
	delete(w.conns, 5)
	assertField(t, "connection deleted since the check", func() error {
		_, err := s.Create(ctx, saver, mattermostInput("late", "ch-alerts"))
		return err
	}, "/connection_id", CodeUnknownID)
	w.fail["LockMattermostConnection"] = errBoom
	if _, err := s.Create(ctx, saver, mattermostInput("late", "ch-alerts")); !errors.Is(err, errBoom) {
		t.Errorf("lock failed = %v", err)
	}
	checker.err = &delivery.LimitedError{RetryAfter: 2}
	if _, err := s.Create(ctx, saver, mattermostInput("busy", "ch-alerts")); !errors.As(err,
		new(*delivery.LimitedError)) {
		t.Errorf("limited = %v", err)
	}
}

// TestUpdateMattermost is updateDestination: the check runs limited by the Destination; the change is recorded with
// its diff; a stale If-Match, a changed type and a refused check change nothing; a passing save of a Broken
// Destination ends its Broken state.
func TestUpdateMattermost(t *testing.T) {
	s, store, w, checker, healed := newSaver(t)
	ctx := t.Context()
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA1", new(int64(1)), mattermostInput("ops", "chan")); !errors.Is(err,
		ErrVersionMismatch) {
		t.Errorf("stale = %v", err)
	}
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA9", nil, mattermostInput("ops", "chan")); !errors.Is(err,
		ErrNotFound) {
		t.Errorf("unknown = %v", err)
	}
	in := mattermostInput("ops", "chan")
	in.Type = TypeTelegram
	assertField(t, "type change", func() error { _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA1", nil, in); return err },
		"/type", CodeInvalidFormat)
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA1", nil, mattermostInput("ops", "ch-nobot")); !errors.As(err,
		new(*CheckFailedError)) {
		t.Errorf("refused = %v", err)
	}
	var renamed []string
	s.writer.Renamed = func(_ context.Context, _ dbgen.DBTX, publicID, name string) error {
		renamed = append(renamed, publicID+"="+name)
		return nil
	}
	d, err := s.Update(ctx, saver, "DSAAAAAAAAAAA1", new(int64(2)), mattermostInput("ops-renamed", "chan"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(renamed, []string{"DSAAAAAAAAAAA1=ops-renamed"}) {
		t.Errorf("rename hook = %v", renamed)
	}
	last := checker.calls[len(checker.calls)-1]
	if last.Destination == nil || *last.Destination != 1 {
		t.Errorf("the check is not limited by the destination: %+v", last)
	}
	if d.Name != "ops-renamed" || d.Version != 3 || len(w.updated) != 1 || w.updated[0].Name != "ops-renamed" ||
		len(w.audit) != 1 || w.audit[0].Action != ActionUpdated || len(*healed) != 0 {
		t.Errorf("updated %+v, %+v, %+v", d, w.updated, w.audit)
	}
	// The same values again: the names are already stored, nothing is written.
	store.rows[0].Name, store.rows[0].Version = "ops-renamed", 3
	store.rows[0].MattermostTeamName, store.rows[0].MattermostChannelName = txt("dev"), txt("alerts")
	store.rows[0].Mentions, _ = json.Marshal(mattermostInput("x", "chan").Mentions)
	store.rows[0].LimiterLimit, store.rows[0].LimiterPerSeconds = 5, 1
	store.rows[0].MattermostTeamID = txt("team-dev")
	w.versions["DSAAAAAAAAAAA1"] = 3
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA1", nil, mattermostInput("ops-renamed", "chan")); err != nil ||
		len(w.updated) != 1 {
		t.Errorf("an unchanged save wrote: %v %d", err, len(w.updated))
	}
	store.rows[0].Health = "broken"
	d, err = s.Update(ctx, saver, "DSAAAAAAAAAAA1", nil, mattermostInput("ops-renamed", "chan"))
	if err != nil || len(*healed) != 1 || d.Health.State != "healthy" {
		t.Errorf("broken save = %+v, %v, healed %v", d, err, *healed)
	}
	// A version that moved between the read and the lock, and failed writes.
	w.versions["DSAAAAAAAAAAA1"] = 7
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA1", nil, mattermostInput("moved", "chan")); !errors.Is(err,
		ErrVersionMismatch) {
		t.Errorf("moved = %v", err)
	}
	delete(w.versions, "DSAAAAAAAAAAA1")
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA1", nil, mattermostInput("gone", "chan")); !errors.Is(err,
		ErrNotFound) {
		t.Errorf("gone = %v", err)
	}
	w.versions["DSAAAAAAAAAAA1"] = 3
	w.fail["LockDestination"] = errBoom
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA1", nil, mattermostInput("x", "chan")); !errors.Is(err, errBoom) {
		t.Errorf("lock = %v", err)
	}
	delete(w.fail, "LockDestination")
	delete(w.conns, 5)
	assertField(t, "connection gone", func() error {
		_, err := s.Update(ctx, saver, "DSAAAAAAAAAAA1", nil, mattermostInput("y", "chan"))
		return err
	}, "/connection_id", CodeUnknownID)
	w.conns[5] = true
	w.fail["UpdateMattermostDestination"] = &pgconn.PgError{Code: uniqueViolation, ConstraintName: nameIndex}
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA1", nil, mattermostInput("hook", "chan")); !errors.Is(err,
		ErrNameTaken) {
		t.Errorf("name taken = %v", err)
	}
	delete(w.fail, "UpdateMattermostDestination")
	store.rows[0].ID, store.rows[0].Health = 99, "broken"
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA1", nil, mattermostInput("z", "chan")); !errors.Is(err, errBoom) {
		t.Errorf("a failed end of the broken state = %v", err)
	}
}

// TestButtonSets is C-28.FR-8, FR-9 and FR-10 on the service, for every messenger type: a Destination created without
// a set is "As in the Organization", stored as null, and one created with its own set stores its items, both in the
// diff of destination.created; an invalid set is refused at the item's pointer — duplicate, required, out_of_range —
// and so is own without items, saving nothing; an update with a stale version is ErrVersionMismatch; an update to its
// own set records /buttons in the diff of destination.updated and requests the re-render of its open Root messages —
// one open_group_updates row destination_buttons with the Destination, at the business time of the save, and a
// notification on muster_open_group_updates; an update that omits the set keeps it and requests nothing; back to the
// Organization's set requests the re-render again. An outgoing webhook takes no set.
func TestButtonSets(t *testing.T) {
	day, tomorrow, zero := int64(86400), buttons.PresetTomorrow, int64(0)
	own := &ButtonSetInput{Own: true, Items: []buttons.ItemInput{{Button: buttons.ItemAcknowledge},
		{Button: buttons.ItemSnooze, SnoozeSeconds: &day}, {Button: buttons.ItemSnoozePreset, Preset: &tomorrow}}}
	const stored = `[{"button":"acknowledge"},{"button":"snooze","snooze_seconds":86400},` +
		`{"button":"snooze_preset","preset":"tomorrow"}]`
	for _, c := range []struct {
		typ, id  string
		dbID     int64
		input    func(name string) Input
		setup    func(*fakeStore, *saveWriter) *dbgen.GetDestinationRow
		inserted func(*saveWriter) []byte
		updated  func(*saveWriter) []byte
	}{
		{TypeMattermost, "DSAAAAAAAAAAA1", 1, func(n string) Input { return mattermostInput(n, "chan") },
			func(f *fakeStore, _ *saveWriter) *dbgen.GetDestinationRow { return &f.rows[0] },
			func(w *saveWriter) []byte { return w.inserted[len(w.inserted)-1].Buttons },
			func(w *saveWriter) []byte { return w.updated[len(w.updated)-1].Buttons }},
		{TypeTelegram, "DSAAAAAAAAAAA2", 2, func(n string) Input { return telegramInput(n, "@muster_alerts") },
			func(f *fakeStore, w *saveWriter) *dbgen.GetDestinationRow {
				w.versions["DSAAAAAAAAAAA2"] = 1
				return &f.rows[1]
			},
			func(w *saveWriter) []byte { return w.tgIns[len(w.tgIns)-1].Buttons },
			func(w *saveWriter) []byte { return w.tgSets[len(w.tgSets)-1].Buttons }},
		{TypeBridge, "DSAAAAAAAAAAA4", 4, func(n string) Input { return bridgeInput(n, "ch-alerts") }, withBridge,
			func(w *saveWriter) []byte { return w.brIns[len(w.brIns)-1].Buttons },
			func(w *saveWriter) []byte { return w.brSets[len(w.brSets)-1].Buttons }},
		{TypeSlack, "DSAAAAAAAAAAA5", 5, func(n string) Input { return slackInput(n, "C0FAKEALERTS") }, withSlack,
			func(w *saveWriter) []byte { return w.slIns[len(w.slIns)-1].Buttons },
			func(w *saveWriter) []byte { return w.slSets[len(w.slSets)-1].Buttons }},
	} {
		t.Run(c.typ, func(t *testing.T) {
			s, store, w, _, _ := newSaver(t)
			ctx := t.Context()
			row := c.setup(store, w)
			d, err := s.Create(ctx, saver, c.input("bs-follows"))
			if err != nil || d.Buttons.Set || c.inserted(w) != nil ||
				!bytes.Contains(w.audit[0].Diff, []byte(`{"pointer":"/buttons","after":{"source":"organization"}}`)) {
				t.Fatalf("created as in the organization = %+v, %v, stored %s, diff %s", d.Buttons, err, c.inserted(w),
					w.audit[0].Diff)
			}
			in := c.input("bs-own")
			in.Buttons = own
			d, err = s.Create(ctx, saver, in)
			if err != nil || !d.Buttons.Set || len(d.Buttons.Items) != 3 || string(c.inserted(w)) != stored ||
				!bytes.Contains(w.audit[1].Diff, []byte(`"after":{"source":"own","items":`+stored+`}`)) {
				t.Fatalf("created with its own set = %+v, %v, stored %s, diff %s", d.Buttons, err, c.inserted(w),
					w.audit[1].Diff)
			}
			for _, r := range []struct {
				pointer, code string
				set           *ButtonSetInput
			}{
				{"/buttons/items", CodeRequired, &ButtonSetInput{Own: true}},
				{"/buttons/items/1", buttons.CodeDuplicate, &ButtonSetInput{Own: true, Items: []buttons.ItemInput{
					{Button: buttons.ItemResolve}, {Button: buttons.ItemResolve}}}},
				{"/buttons/items/0", CodeRequired, &ButtonSetInput{Own: true, Items: []buttons.ItemInput{
					{Button: buttons.ItemSnooze}}}},
				{"/buttons/items/0", buttons.CodeOutOfRange, &ButtonSetInput{Own: true, Items: []buttons.ItemInput{
					{Button: buttons.ItemSnooze, SnoozeSeconds: &zero}}}},
				{"/buttons/items/1", CodeRequired, &ButtonSetInput{Own: true, Items: []buttons.ItemInput{
					{Button: buttons.ItemResolve}, {Button: buttons.ItemSnoozePreset}}}},
			} {
				in := c.input("bs-refused")
				in.Buttons = r.set
				assertField(t, "create "+r.pointer+" "+r.code, func() error {
					_, err := s.Create(ctx, saver, in)
					return err
				}, r.pointer, r.code)
				assertField(t, "update "+r.pointer+" "+r.code, func() error {
					_, err := s.Update(ctx, saver, c.id, nil, in)
					return err
				}, r.pointer, r.code)
			}
			if len(w.audit) != 2 || len(w.buttonUpdates) != 0 {
				t.Fatalf("a refused set was saved: %d entries, %d re-renders", len(w.audit), len(w.buttonUpdates))
			}

			version := row.Version
			in = c.input(row.Name)
			in.Buttons = own
			if _, err := s.Update(ctx, saver, c.id, new(version+1), in); !errors.Is(err, ErrVersionMismatch) {
				t.Errorf("a stale If-Match = %v", err)
			}
			if _, err := s.Update(ctx, saver, c.id, new(version), in); err != nil {
				t.Fatal(err)
			}
			if string(c.updated(w)) != stored || !bytes.Contains(w.audit[2].Diff,
				[]byte(`{"pointer":"/buttons","before":{"source":"organization"},"after":{"source":"own","items":`+
					stored+`}}`)) {
				t.Errorf("updated to its own set: stored %s, diff %s", c.updated(w), w.audit[2].Diff)
			}
			if len(w.buttonUpdates) != 1 || w.buttonUpdates[0] != (dbgen.InsertDestinationButtonsUpdateParams{
				OrgID: 1, DestinationID: pgtype.Int8{Int64: c.dbID, Valid: true}, RequestedAt: t0}) ||
				!slices.Equal(w.woken, []string{OpenGroupUpdatesChannel}) {
				t.Errorf("re-render requested %+v, woken %v", w.buttonUpdates, w.woken)
			}

			row.Buttons, row.Version, row.Health = []byte(stored), version+1, "healthy"
			w.versions[c.id] = version + 1
			in = c.input("bs-renamed")
			if _, err := s.Update(ctx, saver, c.id, nil, in); err != nil || string(c.updated(w)) != stored ||
				len(w.buttonUpdates) != 1 || bytes.Contains(w.audit[len(w.audit)-1].Diff, []byte("/buttons")) {
				t.Errorf("an update that omits the set: %v, stored %s, %d re-renders", err, c.updated(w),
					len(w.buttonUpdates))
			}
			row.Name, row.Version = "bs-renamed", version+2
			w.versions[c.id] = version + 2
			in.Buttons = &ButtonSetInput{Items: own.Items}
			if _, err := s.Update(ctx, saver, c.id, nil, in); err != nil || c.updated(w) != nil ||
				len(w.buttonUpdates) != 2 {
				t.Errorf("back to the organization's set: %v, stored %s, %d re-renders", err, c.updated(w),
					len(w.buttonUpdates))
			}
			row.Buttons, row.Version = nil, version+3
			w.versions[c.id] = version + 3
			in.Buttons = own
			w.fail["NotifyOpenGroupUpdates"] = errBoom
			if _, err := s.Update(ctx, saver, c.id, nil, in); !errors.Is(err, errBoom) {
				t.Errorf("a failed wake = %v", err)
			}
			w.fail = map[string]error{"InsertDestinationButtonsUpdate": errBoom}
			if _, err := s.Update(ctx, saver, c.id, nil, in); !errors.Is(err, errBoom) {
				t.Errorf("a failed request = %v", err)
			}
		})
	}
	s, _, w, _, _ := newSaver(t)
	in := webhookInput("hook-bs", "https://example.org/hook")
	in.Buttons = own
	if _, err := s.Create(t.Context(), saver, in); err != nil ||
		bytes.Contains(w.audit[0].Diff, []byte("/buttons")) {
		t.Errorf("an outgoing webhook with a set: %v, diff %s", err, w.audit[0].Diff)
	}
}

// TestCheckDestination is checkDestination (C-13.FR-10): each check with its result, limited by the Destination; a
// passing check of a Broken Destination ends its Broken state; a type without a check is check_not_supported.
func TestCheckDestination(t *testing.T) {
	s, store, _, checker, healed := newSaver(t)
	ctx := t.Context()
	store.rows[0].MattermostChannelID = txt("ch-nobot")
	res, err := s.Check(ctx, "DSAAAAAAAAAAA1")
	if err != nil || res.OK || len(res.Items) != 2 || res.Items[1].Message != mattermost.MessageNotMember ||
		res.Items[1].Code != "bot_not_in_channel" || res.Items[0].Name != "token" || !res.Items[0].OK ||
		res.Items[0].Code != "" || res.Health.State != "healthy" {
		t.Errorf("failing = %+v, %v", res, err)
	}
	if c := checker.calls[0]; c.Destination == nil || *c.Destination != 1 || c.ChannelID != "ch-nobot" {
		t.Errorf("check %+v", c)
	}
	store.rows[0].MattermostChannelID = txt("ch-alerts")
	store.rows[0].Health = "broken"
	res, err = s.Check(ctx, "DSAAAAAAAAAAA1")
	if err != nil || !res.OK || res.Health.State != "healthy" || len(*healed) != 1 {
		t.Errorf("recovered = %+v, %v, %v", res, err, *healed)
	}
	assertField(t, "webhook", func() error { _, err := s.Check(ctx, "DSAAAAAAAAAAA3"); return err },
		"/path/destination_id", CodeCheckNotSupported)
	if _, err := s.Check(ctx, "DSAAAAAAAAAAA9"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown = %v", err)
	}
	checker.err = errBoom
	if _, err := s.Check(ctx, "DSAAAAAAAAAAA1"); !errors.Is(err, errBoom) {
		t.Errorf("failed check = %v", err)
	}
	checker.err = nil
	store.rows[0].Health, store.rows[0].ID = "broken", 99
	if _, err := s.Check(ctx, "DSAAAAAAAAAAA1"); !errors.Is(err, errBoom) {
		t.Errorf("failed end of the broken state = %v", err)
	}
	s.writer.Healthy = nil
	if _, err := s.Check(ctx, "DSAAAAAAAAAAA1"); err != nil {
		t.Errorf("without the hook = %v", err)
	}
}

// TestDestinationCheckRuns covers the kind and run of the Destination checks (C-23.FR-2, FR-5): checkDestination and
// the check of a save of each messenger type run under destination_check with the Destination, none before it exists,
// each under a run of its own, and checkDestination names its run.
func TestDestinationCheckRuns(t *testing.T) {
	s, store, w, checker, _ := newSaver(t)
	tg, br, sl := s.writer.Telegram.(*fakeTelegram), s.writer.Bridge.(*fakeBridge), s.writer.Slack.(*fakeSlack)
	withBridge(store, w)
	withSlack(store, w)
	w.versions["DSAAAAAAAAAAA2"] = 1
	ctx := t.Context()
	runRe := regexp.MustCompile(`^CR[0-9A-HJKMNP-TV-Z]{12}$`)
	runs := map[string]bool{}
	last := func(name string, links []outbound.Links, destination int64, run string) {
		t.Helper()
		if len(links) == 0 {
			t.Fatalf("%s: no check", name)
		}
		l := links[len(links)-1]
		if l.Destination != destination || l.AlertGroup != 0 || l.Check != "destination_check" ||
			!runRe.MatchString(l.Run) || runs[l.Run] || run != "" && l.Run != run {
			t.Errorf("%s: links %+v, run %q", name, l, run)
		}
		runs[l.Run] = true
	}
	for _, tc := range []struct {
		id          string
		links       *[]outbound.Links
		destination int64
	}{{"DSAAAAAAAAAAA1", &checker.links, 1}, {"DSAAAAAAAAAAA2", &tg.links, 2}, {"DSAAAAAAAAAAA4", &br.links, 4},
		{"DSAAAAAAAAAAA5", &sl.links, 5}} {
		res, err := s.Check(ctx, tc.id)
		if err != nil {
			t.Fatal(err)
		}
		last("checkDestination "+tc.id, *tc.links, tc.destination, res.Run)
	}
	if _, err := s.Create(ctx, saver, mattermostInput("new", "ch-alerts")); err != nil {
		t.Fatal(err)
	}
	last("a new Mattermost Destination", checker.links, 0, "")
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA1", nil, mattermostInput("ops", "ch-nobot")); !errors.As(err,
		new(*CheckFailedError)) {
		t.Errorf("refused = %v", err)
	}
	last("a refused save of a Mattermost Destination", checker.links, 1, "")
	if _, err := s.Create(ctx, saver, telegramInput("new", "@muster_alerts")); err != nil {
		t.Fatal(err)
	}
	last("a new Telegram Destination", tg.links, 0, "")
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA2", nil, telegramInput("alerts", "@muster_alerts")); err != nil {
		t.Fatal(err)
	}
	last("a save of a Telegram Destination", tg.links, 2, "")
	if _, err := s.Create(ctx, saver, bridgeInput("new", "ch-alerts")); err != nil {
		t.Fatal(err)
	}
	last("a new Bridge Destination", br.links, 0, "")
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA4", nil, bridgeInput("alerts", "ch-alerts-prod")); err != nil {
		t.Fatal(err)
	}
	last("a save of a Bridge Destination", br.links, 4, "")
	if _, err := s.Create(ctx, saver, slackInput("new", "C0FAKEALERTS")); err != nil {
		t.Fatal(err)
	}
	last("a new Slack Destination", sl.links, 0, "")
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA5", nil, slackInput("alerts", "C0FAKEOTHER")); !errors.As(err,
		new(*CheckFailedError)) {
		t.Errorf("refused = %v", err)
	}
	last("a refused save of a Slack Destination", sl.links, 5, "")
}

func assertField(t *testing.T, name string, f func() error, pointer, code string) {
	t.Helper()
	fe, ok := errors.AsType[*FieldError](f())
	if !ok || fe.Pointer != pointer || fe.Code != code || fe.Error() == "" {
		t.Errorf("%s = %+v, want %s at %s", name, fe, code, pointer)
	}
}

func errorAsField(err error) bool {
	_, ok := errors.AsType[*FieldError](err)
	return ok
}

func stringsContain(s, sub string) bool { return bytes.Contains([]byte(s), []byte(sub)) }

// keyState is keyring_state in memory.
type keyState struct {
	keyring.Store
	row *keyringdb.GetKeyringStateRow
}

func (s *keyState) GetKeyringState(context.Context) (keyringdb.GetKeyringStateRow, error) {
	if s.row == nil {
		return keyringdb.GetKeyringStateRow{}, pgx.ErrNoRows
	}
	return *s.row, nil
}

func (s *keyState) CreateKeyringState(_ context.Context, p keyringdb.CreateKeyringStateParams) (int64, error) {
	s.row = &keyringdb.GetKeyringStateRow{ActiveKeyID: p.ActiveKeyID, CanaryKeyID: p.ActiveKeyID,
		CanaryCiphertext: p.CanaryCiphertext}
	return 1, nil
}

// activeKeyring is a Keyring of one key, active.
func activeKeyring(t *testing.T) *keyring.Keyring {
	t.Helper()
	k, err := keyring.New([][]byte{bytes.Repeat([]byte{'w'}, keyring.KeySize)}, false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := k.Establish(t.Context(), &keyState{}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Open(t.Context(), logging.New(&bytes.Buffer{}, logging.LevelInfo), st); err != nil {
		t.Fatal(err)
	}
	return k
}

func webhookInput(name, url string, headers ...webhooks.Header) Input {
	in := mattermostInput(name, "")
	in.Type, in.Mattermost = TypeWebhook, nil
	in.Webhook = &WebhookInput{Mode: webhooks.ModeEvents, Events: &webhooks.EventsConfig{URL: url, Headers: headers}}
	return in
}

// TestCreateWebhook is createDestination of type webhook in the mode events (C-15.FR-1, FR-5, C-11.FR-18,
// C-01.FR-13): no Destination check; the request, the proxy with its password, the Mention settings and the limiter
// are stored with the first Signing secret, encrypted, which the Destination returned carries once; the creation is
// recorded with the password marked changed, never its value.
func TestCreateWebhook(t *testing.T) {
	s, _, w, checker, _ := newSaver(t)
	in := webhookInput(" auto ", " http://127.0.0.1:18093/hook/auto ",
		webhooks.Header{Name: "Authorization", Value: "Bearer {{ .Secrets.token }}"})
	user, typ, addr := "u", "http", "proxy:3128"
	in.Webhook.Proxy = proxyconf.Input{Enabled: true, Type: &typ, Address: &addr, UsernameSet: true, Username: &user,
		Password: keyring.Replace("proxy-pass")}
	d, err := s.Create(t.Context(), saver, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(checker.calls) != 0 || len(w.hooks) != 1 {
		t.Fatalf("checks %d, inserts %d", len(checker.calls), len(w.hooks))
	}
	got := w.hooks[0]
	if got.Name != "auto" || got.WebhookMode.String != "events" ||
		string(got.WebhookEventsConfig) != `{"url":"http://127.0.0.1:18093/hook/auto","headers":[{"name":"Authorization","value":"Bearer {{ .Secrets.token }}"}]}` ||
		string(got.Proxy) != `{"enabled":true,"type":"http","address":"proxy:3128","username":"u"}` ||
		len(got.ProxyPasswordCiphertext) == 0 || !got.ProxyPasswordUpdatedAt.Valid || got.LimiterLimit != 5 {
		t.Fatalf("insert %+v", got)
	}
	k := s.writer.Keyring
	plain, err := k.OpenSecret(webhooks.FieldSigningSecret, keyring.StoredSecret{Ciphertext: got.SigningSecretCiphertext,
		KeyID: got.SigningSecretKeyID.String})
	if err != nil || plain != d.SigningSecretOnce || !stringsContain(string(plain), webhooks.SigningSecretPrefix) {
		t.Fatalf("signing secret %v", err)
	}
	if d.ID != 21 || !d.SigningSecret.Set || !d.Proxy.PasswordSet || *d.WebhookMode != "events" {
		t.Errorf("destination %+v", d)
	}
	if got := counterSeries(d.PublicID); !slices.Equal(got, zeroCounterSeries(d.PublicID, TypeWebhook)) {
		t.Errorf("counter series %q", got)
	}
	if len(w.audit) != 1 || w.audit[0].Action != ActionCreated || stringsContain(string(w.audit[0].Diff), "proxy-pass") ||
		!stringsContain(string(w.audit[0].Diff), "/proxy/password") || stringsContain(string(w.audit[0].Diff),
		string(d.SigningSecretOnce)) {
		t.Errorf("audit %+v", w.audit)
	}
	w.fail["InsertWebhookDestination"] = errBoom
	if _, err := s.Create(t.Context(), saver, webhookInput("b", "https://example.org")); !errors.Is(err, errBoom) {
		t.Errorf("insert failed = %v", err)
	}
}

// TestCreateWebhookRefusals: the modes template and both are not supported yet; the request of the events mode is
// required, and its URL and headers are parsed and run on a dry run, a template error naming its line and column; the
// proxy and its password follow their rules.
func TestCreateWebhookRefusals(t *testing.T) {
	s, _, w, _, _ := newSaver(t)
	for name, c := range map[string]struct {
		mut     func(in *Input)
		pointer string
		code    string
	}{
		"template":     {func(in *Input) { in.Webhook.Mode = webhooks.ModeTemplate }, "/template", CodeRequired},
		"both":         {func(in *Input) { in.Webhook.Mode = webhooks.ModeBoth }, "/template", CodeRequired},
		"bad mode":     {func(in *Input) { in.Webhook.Mode = "x" }, "/mode", CodeInvalidFormat},
		"no request":   {func(in *Input) { in.Webhook.Events = nil }, "/events", CodeRequired},
		"no url":       {func(in *Input) { in.Webhook.Events.URL = " " }, "/events/url", CodeRequired},
		"syntax":       {func(in *Input) { in.Webhook.Events.URL = "https://x/{{ .Secrets.a " }, "/events/url", templates.CodeSyntax},
		"not http":     {func(in *Input) { in.Webhook.Events.URL = "ftp://x" }, "/events/url", CodeInvalidFormat},
		"empty name":   {func(in *Input) { in.Name = "" }, "/name", CodeRequired},
		"proxy":        {func(in *Input) { in.Webhook.Proxy.Enabled = true }, "/proxy/type", CodeRequired},
		"empty secret": {func(in *Input) { in.Webhook.Proxy.Password = keyring.Replace("") }, "/proxy/password", CodeRequired},
		"reserved header": {func(in *Input) {
			in.Webhook.Events.Headers = []webhooks.Header{{Name: "webhook-id", Value: "x"}}
		}, "/events/headers/0/name", webhooks.CodeReserved},
	} {
		in := webhookInput("x", "https://example.org/hook")
		c.mut(&in)
		_, err := s.Create(t.Context(), saver, in)
		fe, ok := errors.AsType[*FieldError](err)
		if !ok || fe.Pointer != c.pointer || fe.Code != c.code {
			t.Errorf("%s = %v", name, err)
		}
	}
	in := webhookInput("x", "https://x/\n{{ nofunc }}")
	_, err := s.Create(t.Context(), saver, in)
	if fe, ok := errors.AsType[*FieldError](err); !ok || fe.Line != 2 || fe.Column != 4 {
		t.Errorf("position = %+v", err)
	}
	if len(w.hooks) != 0 {
		t.Fatalf("a refused save inserted %+v", w.hooks)
	}
}

// TestUpdateRenameHook: a save that changes the name of a Destination of any type runs the rename hook in the
// transaction of the save; a save that keeps the name does not; a failing hook fails the save.
func TestUpdateRenameHook(t *testing.T) {
	s, _, w, _, _ := newSaver(t)
	ctx := t.Context()
	var renamed []string
	s.writer.Renamed = func(_ context.Context, _ dbgen.DBTX, publicID, name string) error {
		renamed = append(renamed, publicID+"="+name)
		return nil
	}
	w.versions["DSAAAAAAAAAAA2"] = 1
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA3", nil, webhookInput("hook-2", "https://example.org/v2")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA2", nil, telegramInput("alerts-2", "@muster_alerts")); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(renamed, []string{"DSAAAAAAAAAAA3=hook-2", "DSAAAAAAAAAAA2=alerts-2"}) {
		t.Errorf("rename hook = %v", renamed)
	}
	s.writer.Renamed = func(context.Context, dbgen.DBTX, string, string) error { return errBoom }
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA1", nil, mattermostInput("ops-3", "chan")); !errors.Is(err, errBoom) {
		t.Errorf("failing hook = %v", err)
	}
}

// TestUpdateWebhook is updateDestination of an outgoing webhook: the request, the proxy, the Mention settings and the
// limiter are replaced with the diff recorded; a password left out is kept, null clears it; the type cannot change, a
// stale If-Match and an unchanged save write nothing.
func TestUpdateWebhook(t *testing.T) {
	s, store, w, _, _ := newSaver(t)
	ctx := t.Context()
	in := webhookInput("hook", "https://example.org/v2")
	addr := "proxy:3128"
	in.Webhook.Proxy = proxyconf.Input{Enabled: true, Address: &addr}
	d, err := s.Update(ctx, saver, "DSAAAAAAAAAAA3", new(int64(4)), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.hookSets) != 1 || w.hookSets[0].PasswordGiven || w.hookSets[0].ID != 3 ||
		string(w.hookSets[0].Proxy) != `{"enabled":true,"type":"http","address":"proxy:3128","username":"u"}` ||
		d.Version != 5 || !d.Proxy.PasswordSet {
		t.Fatalf("update %+v, destination %+v", w.hookSets, d)
	}
	if len(w.audit) != 1 || !stringsContain(string(w.audit[0].Diff), "/events/url") {
		t.Errorf("audit %+v", w.audit)
	}
	in.Webhook.Events.URL = "https://example.org"
	set, _ := json.Marshal(in.Mentions)
	store.rows[2].Mentions, store.rows[2].LimiterLimit, store.rows[2].LimiterPerSeconds = set, 5, 1
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA3", nil, in); err != nil || len(w.hookSets) != 1 {
		t.Fatalf("unchanged save wrote %d (%v)", len(w.hookSets), err)
	}
	in.Webhook.Proxy.Password = keyring.Clear
	if d, err = s.Update(ctx, saver, "DSAAAAAAAAAAA3", nil, in); err != nil || len(w.hookSets) != 2 ||
		!w.hookSets[1].PasswordGiven || w.hookSets[1].ProxyPasswordCiphertext != nil || d.Proxy.PasswordSet {
		t.Fatalf("clear %+v (%v)", w.hookSets, err)
	}
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA3", new(int64(9)), in); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("stale = %v", err)
	}
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA1", nil, in); !errorAsField(err) {
		t.Errorf("type changed = %v", err)
	}
	in.Webhook.Events.URL = "https://example.org/v3"
	w.versions["DSAAAAAAAAAAA3"] = 7
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA3", nil, in); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("raced = %v", err)
	}
	w.versions["DSAAAAAAAAAAA3"] = 4
	w.fail["UpdateWebhookDestination"] = errBoom
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA3", nil, in); !errors.Is(err, errBoom) {
		t.Errorf("update failed = %v", err)
	}
	w.fail["LockDestination"] = errBoom
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA3", nil, in); !errors.Is(err, errBoom) {
		t.Errorf("lock failed = %v", err)
	}
	delete(w.versions, "DSAAAAAAAAAAA3")
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA3", nil, in); !errors.Is(err, ErrNotFound) {
		t.Errorf("gone = %v", err)
	}
	in.Webhook.Proxy.Password = keyring.Replace("")
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA3", nil, in); !errorAsField(err) {
		t.Errorf("empty password = %v", err)
	}
	in = webhookInput("hook", "https://example.org/v2")
	in.Webhook.Proxy = proxyconf.Input{Enabled: true, Address: new("no-port")}
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA3", nil, in); !errorAsField(err) {
		t.Errorf("bad proxy = %v", err)
	}
}

func telegramInput(name, channel string) Input {
	in := mattermostInput(name, "")
	in.Type, in.Mattermost = TypeTelegram, nil
	in.Telegram = &TelegramInput{Connection: "cnaaaaaaaaaaa2", ChannelID: channel}
	return in
}

// TestCreateTelegram is createDestination of type telegram (C-14.FR-2, FR-14, C-14.AC-13, C-11.FR-18): the channel
// alone is entered; the check finds its discussion group, which the Destination shows read-only, and the ids and titles
// it found are stored.
func TestCreateTelegram(t *testing.T) {
	s, _, w, _, _ := newSaver(t)
	tg := s.writer.Telegram.(*fakeTelegram)
	d, err := s.Create(t.Context(), saver, telegramInput(" alerts ", " @muster_alerts "))
	if err != nil {
		t.Fatal(err)
	}
	if len(tg.calls) != 1 || tg.calls[0].Destination != nil || tg.calls[0].Connection != "CNAAAAAAAAAAA2" ||
		tg.calls[0].ChannelID != "@muster_alerts" {
		t.Errorf("checks %+v", tg.calls)
	}
	if d.ID != 31 || d.Name != "alerts" || *d.Connection != "CNAAAAAAAAAAA2" || *d.TelegramChannelID != "@muster_alerts" ||
		*d.TelegramChannelChatID != "-1001000000001" || *d.TelegramDiscussionGroupID != "-1001000000002" || *d.TelegramDiscussionGroupTitle != "Muster alerts Chat" ||
		*d.TelegramChannelTitle != "Muster alerts" || d.Health.State != "healthy" || d.Version != 1 {
		t.Errorf("created %+v", d)
	}
	ins := w.tgIns[0]
	if ins.ConnectionID.Int64 != 6 || ins.ChannelID.String != "@muster_alerts" || ins.ChannelChatID.Int64 != -1001000000001 ||
		ins.DiscussionChatID.Int64 != -1001000000002 || ins.ChannelTitle.String != "Muster alerts" ||
		ins.DiscussionGroupTitle.String != "Muster alerts Chat" {
		t.Errorf("inserted %+v", ins)
	}
	if len(w.audit) != 1 || w.audit[0].Action != ActionCreated || !bytes.Contains(w.audit[0].Diff, []byte("@muster_alerts")) ||
		len(w.hints) != 1 {
		t.Errorf("audit %+v hints %+v", w.audit, w.hints)
	}
	if got := counterSeries(d.PublicID); !slices.Equal(got, zeroCounterSeries(d.PublicID, TypeTelegram)) {
		t.Errorf("counter series %q", got)
	}
}

// TestCreateTelegramRefusals is C-14.AC-13: a channel without comments and a discussion group whose admins do not
// include the bot are refused with the texts of reference.md, and nothing is saved; so are an invalid channel, an
// unknown Connection and a refused token.
func TestCreateTelegramRefusals(t *testing.T) {
	s, _, w, _, _ := newSaver(t)
	tg := s.writer.Telegram.(*fakeTelegram)
	ctx := t.Context()
	for channel, want := range map[string]CheckItem{
		"@no_comments": {Name: "discussion_group", Message: telegram.MessageNoComments, Code: "comments_disabled",
			Pointer: "/channel_id"},
		"@no_group_admin": {Name: "bot_rights_group", Pointer: "/channel_id", Code: "bot_not_group_admin",
			Message: "The bot is not an admin of the discussion group Muster alerts Chat. Make the bot an admin there, " +
				"allowed to post messages."},
		"@revoked": {Name: "channel_exists", Message: telegram.MessageTokenInvalid, Code: "token_invalid",
			Pointer: "/connection_id"},
	} {
		_, err := s.Create(ctx, saver, telegramInput("x", channel))
		failed, ok := errors.AsType[*CheckFailedError](err)
		if !ok || len(failed.Items) != 1 || !reflect.DeepEqual(failed.Items[0], want) {
			t.Errorf("%s = %v", channel, err)
		}
	}
	in := telegramInput("x", "@muster_alerts")
	in.Telegram.Connection = "CNAAAAAAAAAAA1"
	assertField(t, "a mattermost connection", func() error { _, err := s.Create(ctx, saver, in); return err },
		"/connection_id", CodeUnknownID)
	in.Telegram.Connection = "not-an-id"
	assertField(t, "malformed connection", func() error { _, err := s.Create(ctx, saver, in); return err },
		"/connection_id", CodeUnknownID)
	for name, channel := range map[string]string{"empty": " ", "spaces": "@muster alerts", "short": "@abc",
		"url": "https://t.me/muster_alerts", "zero": "0"} {
		in := telegramInput("x", channel)
		if _, err := s.Create(ctx, saver, in); !errorAsField(err) {
			t.Errorf("channel %s = %v", name, err)
		}
	}
	in = telegramInput("", "@muster_alerts")
	assertField(t, "empty name", func() error { _, err := s.Create(ctx, saver, in); return err }, "/name", CodeRequired)
	in = telegramInput("x", "@muster_alerts")
	in.Mentions["new_alerts"] = mentions.Setting{Everyone: "none", UserIDs: []string{"SR0000000000ZZ"}, Groups: []string{}}
	if _, err := s.Create(ctx, saver, in); !errors.As(err, new(*mentions.FieldError)) {
		t.Errorf("unknown user = %v", err)
	}
	if len(w.tgIns) != 0 {
		t.Fatalf("a refused save inserted %+v", w.tgIns)
	}
	w.fail["InsertTelegramDestination"] = &pgconn.PgError{Code: uniqueViolation, ConstraintName: nameIndex}
	if _, err := s.Create(ctx, saver, telegramInput("ops", "@muster_alerts")); !errors.Is(err, ErrNameTaken) {
		t.Errorf("name taken = %v", err)
	}
	delete(w.fail, "InsertTelegramDestination")
	delete(w.conns, 6)
	assertField(t, "connection deleted since the check", func() error {
		_, err := s.Create(ctx, saver, telegramInput("late", "@muster_alerts"))
		return err
	}, "/connection_id", CodeUnknownID)
	w.fail["LockTelegramConnection"] = errBoom
	if _, err := s.Create(ctx, saver, telegramInput("late", "@muster_alerts")); !errors.Is(err, errBoom) {
		t.Errorf("lock failed = %v", err)
	}
	tg.err = &delivery.LimitedError{RetryAfter: 2}
	if _, err := s.Create(ctx, saver, telegramInput("busy", "@muster_alerts")); !errors.As(err,
		new(*delivery.LimitedError)) {
		t.Errorf("limited = %v", err)
	}
}

// TestUpdateTelegram is updateDestination of type telegram: the check runs limited by the Destination; a change is
// recorded with its diff, a new title the check found is stored without an Audit log entry, an unchanged save writes
// nothing, and a passing save of a Broken Destination ends its Broken state.
func TestUpdateTelegram(t *testing.T) {
	s, store, w, _, healed := newSaver(t)
	tg := s.writer.Telegram.(*fakeTelegram)
	ctx := t.Context()
	w.versions["DSAAAAAAAAAAA2"] = 1
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA2", nil, telegramInput("alerts", "@no_comments")); !errors.As(err,
		new(*CheckFailedError)) {
		t.Errorf("refused = %v", err)
	}
	d, err := s.Update(ctx, saver, "DSAAAAAAAAAAA2", new(int64(1)), telegramInput("alerts", "@muster_alerts"))
	if err != nil {
		t.Fatal(err)
	}
	if last := tg.calls[len(tg.calls)-1]; last.Destination == nil || *last.Destination != 2 {
		t.Errorf("the check is not limited by the destination: %+v", last)
	}
	if d.Health.State != "healthy" || len(*healed) != 1 || len(w.tgSets) != 1 || w.tgSets[0].ChannelChatID.Int64 != -1001000000001 ||
		w.tgSets[0].ChannelID.String != "@muster_alerts" || w.tgSets[0].DiscussionChatID.Int64 != -1001000000002 ||
		len(w.audit) != 1 || w.audit[0].Action != ActionUpdated {
		t.Errorf("updated %+v, sets %+v, audit %+v", d, w.tgSets, w.audit)
	}
	// Stored as the check found it: the same save writes nothing; a new title is stored with a hint only.
	r := &store.rows[1]
	r.TelegramChannelID, r.Version, r.Health = txt("@muster_alerts"), 2, "healthy"
	r.Mentions, _ = json.Marshal(telegramInput("x", "").Mentions)
	r.LimiterLimit, r.LimiterPerSeconds = 5, 1
	r.TelegramChannelChatID.Int64, r.TelegramDiscussionChatID.Int64 = -1001000000001, -1001000000002
	r.TelegramChannelTitle, r.TelegramDiscussionGroupTitle = txt("Muster alerts"), txt("Muster alerts Chat")
	w.versions["DSAAAAAAAAAAA2"] = 2
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA2", nil, telegramInput("alerts", "@muster_alerts")); err != nil ||
		len(w.tgSets) != 1 {
		t.Errorf("an unchanged save wrote: %v %d", err, len(w.tgSets))
	}
	tg.title = "Renamed channel"
	hints := len(w.hints)
	if d, err := s.Update(ctx, saver, "DSAAAAAAAAAAA2", nil, telegramInput("alerts", "@muster_alerts")); err != nil ||
		len(w.tgSets) != 2 || len(w.audit) != 1 || len(w.hints) != hints+1 || *d.TelegramChannelTitle != "Renamed channel" {
		t.Errorf("a new title = %+v %v", d, err)
	}
	tg.title, r.TelegramChannelChatID.Int64 = "", -1001000000009
	if d, err := s.Update(ctx, saver, "DSAAAAAAAAAAA2", nil, telegramInput("alerts", "@muster_alerts")); err != nil ||
		len(w.tgSets) != 3 || w.tgSets[2].ChannelChatID.Int64 != -1001000000001 || len(w.audit) != 1 ||
		*d.TelegramChannelChatID != "-1001000000001" {
		t.Errorf("a new channel chat id = %+v %v", d, err)
	}
	w.versions["DSAAAAAAAAAAA2"] = 9
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA2", nil, telegramInput("moved", "@muster_alerts")); !errors.Is(err,
		ErrVersionMismatch) {
		t.Errorf("moved = %v", err)
	}
	delete(w.versions, "DSAAAAAAAAAAA2")
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA2", nil, telegramInput("gone", "@muster_alerts")); !errors.Is(err,
		ErrNotFound) {
		t.Errorf("gone = %v", err)
	}
	w.versions["DSAAAAAAAAAAA2"] = 2
	w.fail["LockDestination"] = errBoom
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA2", nil, telegramInput("x", "@muster_alerts")); !errors.Is(err,
		errBoom) {
		t.Errorf("lock = %v", err)
	}
	delete(w.fail, "LockDestination")
	delete(w.conns, 6)
	assertField(t, "connection gone", func() error {
		_, err := s.Update(ctx, saver, "DSAAAAAAAAAAA2", nil, telegramInput("y", "@muster_alerts"))
		return err
	}, "/connection_id", CodeUnknownID)
	w.conns[6] = true
	w.fail["UpdateTelegramDestination"] = &pgconn.PgError{Code: uniqueViolation, ConstraintName: nameIndex}
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA2", nil, telegramInput("ops", "@muster_alerts")); !errors.Is(err,
		ErrNameTaken) {
		t.Errorf("name taken = %v", err)
	}
	delete(w.fail, "UpdateTelegramDestination")
	r.ID, r.Health = 99, "broken"
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA2", nil, telegramInput("z", "@muster_alerts")); !errors.Is(err,
		errBoom) {
		t.Errorf("a failed end of the broken state = %v", err)
	}
	r.ID = 2
	tg.err = errBoom
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA2", nil, telegramInput("z", "@muster_alerts")); !errors.Is(err,
		errBoom) {
		t.Errorf("a failed check = %v", err)
	}
}

// TestCheckTelegramDestination is checkDestination of a Telegram Destination (C-14.FR-14): each step with its result,
// limited by the Destination; a passing check of a Broken Destination ends its Broken state.
func TestCheckTelegramDestination(t *testing.T) {
	s, store, _, _, healed := newSaver(t)
	tg := s.writer.Telegram.(*fakeTelegram)
	ctx := t.Context()
	store.rows[1].TelegramChannelID = txt("@no_comments")
	res, err := s.Check(ctx, "DSAAAAAAAAAAA2")
	if err != nil || res.OK || len(res.Items) != 2 || res.Items[1].Message != telegram.MessageNoComments ||
		res.Items[1].Code != "comments_disabled" || res.Health.State != "broken" {
		t.Errorf("failing = %+v, %v", res, err)
	}
	if c := tg.calls[0]; c.Destination == nil || *c.Destination != 2 || c.Connection != "CNAAAAAAAAAAA2" {
		t.Errorf("check %+v", c)
	}
	store.rows[1].TelegramChannelID = txt("@muster_alerts")
	// The stored discussion group (-100200) is not the one the check finds: the check fails until a save.
	res, err = s.Check(ctx, "DSAAAAAAAAAAA2")
	if err != nil || res.OK || res.Items[1].Message != telegram.MessageMoved || res.Items[1].Code != "changed_since_saved" ||
		len(*healed) != 0 {
		t.Errorf("moved = %+v, %v", res, err)
	}
	store.rows[1].TelegramDiscussionChatID.Int64 = -1001000000002
	res, err = s.Check(ctx, "DSAAAAAAAAAAA2")
	if err != nil || !res.OK || len(res.Items) != 4 || res.Health.State != "healthy" || len(*healed) != 1 {
		t.Errorf("recovered = %+v, %v, %v", res, err, *healed)
	}
	tg.err = errBoom
	if _, err := s.Check(ctx, "DSAAAAAAAAAAA2"); !errors.Is(err, errBoom) {
		t.Errorf("failed check = %v", err)
	}
}

func bridgeInput(name, channel string) Input {
	in := mattermostInput(name, "")
	in.Type, in.Mattermost = TypeBridge, nil
	n := in.Mentions[mentions.Kinds[1]]
	n.Everyone = mentions.EveryoneNone
	in.Mentions[mentions.Kinds[1]] = n
	in.Bridge = &BridgeInput{Connection: "cnaaaaaaaaaaa3", ChannelID: channel}
	return in
}

// withBridge adds the Bridge Destination DSAAAAAAAAAAA4 on ch-alerts, Broken, to the store, and returns its row.
func withBridge(store *fakeStore, w *saveWriter) *dbgen.GetDestinationRow {
	store.rows = append(store.rows, bridgeRow())
	w.versions["DSAAAAAAAAAAA4"] = 1
	return &store.rows[len(store.rows)-1]
}

// TestCreateBridge is createDestination of type bridge (C-22.FR-3, AC-2): the channel id, trimmed, and the Connection
// are checked through checkChannel before anything is saved, not limited by a Destination; a passing check stores the
// channel's name it read with the Mention settings and the limiter, and records destination.created with the hint.
func TestCreateBridge(t *testing.T) {
	s, _, w, _, _ := newSaver(t)
	br := s.writer.Bridge.(*fakeBridge)
	d, err := s.Create(t.Context(), saver, bridgeInput(" alerts ", " ch-alerts "))
	if err != nil {
		t.Fatal(err)
	}
	if len(br.calls) != 1 || br.calls[0].Destination != nil || br.calls[0].Connection != "CNAAAAAAAAAAA3" ||
		br.calls[0].ChannelID != "ch-alerts" {
		t.Errorf("checks %+v", br.calls)
	}
	if d.ID != 41 || d.Type != TypeBridge || d.Name != "alerts" || *d.Connection != "CNAAAAAAAAAAA3" ||
		*d.BridgeChannelID != "ch-alerts" || *d.BridgeChannelName != "alerts" || d.Health.State != "healthy" ||
		d.LimiterLimit != 5 || d.Version != 1 {
		t.Errorf("created %+v", d)
	}
	if len(w.brIns) != 1 {
		t.Fatalf("inserts %+v", w.brIns)
	}
	ins := w.brIns[0]
	if ins.ConnectionID.Int64 != 7 || ins.ChannelID.String != "ch-alerts" || ins.ChannelName.String != "alerts" ||
		ins.Name != "alerts" || ins.LimiterLimit != 5 || ins.LimiterPerSeconds != 1 || !ins.Now.Equal(t0) ||
		!bytes.Contains(ins.Mentions, []byte(`"new_alerts":{"everyone":"none"`)) {
		t.Errorf("inserted %+v", ins)
	}
	if len(w.audit) != 1 || w.audit[0].Action != ActionCreated ||
		!bytes.Contains(w.audit[0].Diff, []byte(`"/channel_id"`)) ||
		!bytes.Contains(w.audit[0].Diff, []byte("ch-alerts")) || len(w.hints) != 1 {
		t.Errorf("audit %+v hints %+v", w.audit, w.hints)
	}
	if got := counterSeries(d.PublicID); !slices.Equal(got, zeroCounterSeries(d.PublicID, TypeBridge)) {
		t.Errorf("counter series %q", got)
	}
	long := strings.Repeat("я", maxBridgeChannelLength)
	if d, err := s.Create(t.Context(), saver, bridgeInput("long", long)); err != nil || *d.BridgeChannelID != long {
		t.Errorf("a channel id of 512 characters = %v", err)
	}
}

// TestCreateBridgeRefusals is C-22.AC-2: a channel the bridge answers forbidden for is refused at /channel_id with the
// bridge's detail and the check code channel_forbidden, and nothing is saved; a refused token is at /connection_id; a
// Connection of another type or deleted, an empty or too long channel id and Mention settings that are refused are
// refused before anything is saved.
func TestCreateBridgeRefusals(t *testing.T) {
	s, _, w, _, _ := newSaver(t)
	br := s.writer.Bridge.(*fakeBridge)
	ctx := t.Context()
	for channel, want := range map[string]CheckItem{
		"ch-nobot": {Name: bridge.ItemChannel, Message: "The bot is not a member of this channel.",
			Code: "channel_forbidden", Pointer: "/channel_id"},
		"revoked": {Name: bridge.ItemChannel, Message: bridge.MessageTokenInvalid, Code: "token_invalid",
			Pointer: "/connection_id"},
	} {
		_, err := s.Create(ctx, saver, bridgeInput("x", channel))
		failed, ok := errors.AsType[*CheckFailedError](err)
		if !ok || len(failed.Items) != 1 || !reflect.DeepEqual(failed.Items[0], want) || failed.Error() == "" {
			t.Errorf("%s = %v", channel, err)
		}
	}
	calls := len(br.calls)
	for name, c := range map[string]struct {
		mut     func(in *Input)
		pointer string
		code    string
	}{
		"empty channel":    {func(in *Input) { in.Bridge.ChannelID = " \t" }, "/channel_id", CodeRequired},
		"long channel":     {func(in *Input) { in.Bridge.ChannelID = strings.Repeat("x", 513) }, "/channel_id", CodeTooLong},
		"malformed id":     {func(in *Input) { in.Bridge.Connection = "not-an-id" }, "/connection_id", CodeUnknownID},
		"no bridge fields": {func(in *Input) { in.Bridge = nil }, "/type", CodeInvalidFormat},
		"empty name":       {func(in *Input) { in.Name = " " }, "/name", CodeRequired},
		"no limiter":       {func(in *Input) { in.Limiter = Limiter{} }, "/limiter", CodeInvalidFormat},
	} {
		in := bridgeInput("x", "ch-alerts")
		c.mut(&in)
		_, err := s.Create(ctx, saver, in)
		fe, ok := errors.AsType[*FieldError](err)
		if !ok || fe.Pointer != c.pointer || fe.Code != c.code {
			t.Errorf("%s = %v", name, err)
		}
	}
	in := bridgeInput("x", "ch-alerts")
	in.Mentions["new_alerts"] = mentions.Setting{Everyone: "none", UserIDs: []string{"SR0000000000ZZ"}, Groups: []string{}}
	if _, err := s.Create(ctx, saver, in); !errors.As(err, new(*mentions.FieldError)) {
		t.Errorf("unknown user = %v", err)
	}
	if len(br.calls) != calls {
		t.Errorf("a refused field ran the check: %+v", br.calls[calls:])
	}
	in = bridgeInput("x", "ch-alerts")
	in.Bridge.Connection = "CNAAAAAAAAAAA2"
	_, err := s.Create(ctx, saver, in)
	if fe, ok := errors.AsType[*FieldError](err); !ok || fe.Pointer != "/connection_id" || fe.Code != CodeUnknownID ||
		fe.Detail != "No such Bridge Connection." {
		t.Errorf("a telegram connection = %v", err)
	}
	if len(w.brIns) != 0 {
		t.Fatalf("a refused save inserted %+v", w.brIns)
	}
	w.fail["InsertBridgeDestination"] = &pgconn.PgError{Code: uniqueViolation, ConstraintName: nameIndex}
	if _, err := s.Create(ctx, saver, bridgeInput("ops", "ch-alerts")); !errors.Is(err, ErrNameTaken) {
		t.Errorf("name taken = %v", err)
	}
	delete(w.fail, "InsertBridgeDestination")
	delete(w.conns, 7)
	assertField(t, "connection deleted since the check", func() error {
		_, err := s.Create(ctx, saver, bridgeInput("late", "ch-alerts"))
		return err
	}, "/connection_id", CodeUnknownID)
	w.fail["LockBridgeConnection"] = errBoom
	if _, err := s.Create(ctx, saver, bridgeInput("late", "ch-alerts")); !errors.Is(err, errBoom) {
		t.Errorf("lock failed = %v", err)
	}
	br.err = &delivery.LimitedError{RetryAfter: 2}
	if _, err := s.Create(ctx, saver, bridgeInput("busy", "ch-alerts")); !errors.As(err,
		new(*delivery.LimitedError)) {
		t.Errorf("limited = %v", err)
	}
	if len(w.brIns) != 0 || len(w.audit) != 0 {
		t.Fatalf("a failed save wrote %+v %+v", w.brIns, w.audit)
	}
}

// TestUpdateBridge is updateDestination of type bridge (C-22.FR-3): the check runs limited by the Destination; a
// change is recorded with its diff, a new channel name the check read is stored without an Audit log entry, an
// unchanged save writes nothing, and a passing save of a Broken Destination ends its Broken state.
func TestUpdateBridge(t *testing.T) {
	s, store, w, _, healed := newSaver(t)
	br := s.writer.Bridge.(*fakeBridge)
	ctx := t.Context()
	r := withBridge(store, w)
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA4", nil, bridgeInput("alerts", "ch-nobot")); !errors.As(err,
		new(*CheckFailedError)) || len(w.brSets) != 0 || len(*healed) != 0 {
		t.Errorf("refused = %v", err)
	}
	d, err := s.Update(ctx, saver, "DSAAAAAAAAAAA4", new(int64(1)), bridgeInput("alerts", "ch-alerts-prod"))
	if err != nil {
		t.Fatal(err)
	}
	if last := br.calls[len(br.calls)-1]; last.Destination == nil || *last.Destination != 4 ||
		last.ChannelID != "ch-alerts-prod" {
		t.Errorf("the check is not limited by the destination: %+v", last)
	}
	if d.Health.State != "healthy" || !slices.Equal(*healed, []int64{4}) || len(w.brSets) != 1 ||
		w.brSets[0].ID != 4 || w.brSets[0].ConnectionID.Int64 != 7 || w.brSets[0].ChannelID.String != "ch-alerts-prod" ||
		w.brSets[0].ChannelName.String != "alerts" || len(w.audit) != 1 || w.audit[0].Action != ActionUpdated ||
		!bytes.Contains(w.audit[0].Diff, []byte("ch-alerts-prod")) {
		t.Errorf("updated %+v, sets %+v, audit %+v", d, w.brSets, w.audit)
	}
	// Stored as saved: the same save writes nothing; a new channel name is stored with a hint only.
	r.Name, r.BridgeChannelID, r.Version, r.Health = "alerts", txt("ch-alerts-prod"), 2, "healthy"
	r.Mentions, _ = json.Marshal(bridgeInput("x", "").Mentions)
	r.LimiterLimit, r.LimiterPerSeconds = 5, 1
	w.versions["DSAAAAAAAAAAA4"] = 2
	if d, err := s.Update(ctx, saver, "DSAAAAAAAAAAA4", nil, bridgeInput("alerts", "ch-alerts-prod")); err != nil ||
		len(w.brSets) != 1 || d.Version != 2 {
		t.Errorf("an unchanged save wrote: %v %d", err, len(w.brSets))
	}
	br.name = "alerts-renamed"
	hints := len(w.hints)
	if d, err := s.Update(ctx, saver, "DSAAAAAAAAAAA4", nil, bridgeInput("alerts", "ch-alerts-prod")); err != nil ||
		len(w.brSets) != 2 || w.brSets[1].ChannelName.String != "alerts-renamed" || len(w.audit) != 1 ||
		len(w.hints) != hints+1 || *d.BridgeChannelName != "alerts-renamed" || d.Version != 3 {
		t.Errorf("a new channel name = %+v %v", d, err)
	}
	w.versions["DSAAAAAAAAAAA4"] = 9
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA4", nil, bridgeInput("moved", "ch-alerts")); !errors.Is(err,
		ErrVersionMismatch) {
		t.Errorf("raced = %v", err)
	}
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA4", new(int64(1)), bridgeInput("moved", "ch-alerts")); !errors.Is(
		err, ErrVersionMismatch) {
		t.Errorf("stale = %v", err)
	}
	delete(w.versions, "DSAAAAAAAAAAA4")
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA4", nil, bridgeInput("gone", "ch-alerts")); !errors.Is(err,
		ErrNotFound) {
		t.Errorf("gone = %v", err)
	}
	w.versions["DSAAAAAAAAAAA4"] = 2
	w.fail["LockDestination"] = errBoom
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA4", nil, bridgeInput("x", "ch-alerts")); !errors.Is(err, errBoom) {
		t.Errorf("lock = %v", err)
	}
	delete(w.fail, "LockDestination")
	assertField(t, "type changed", func() error {
		_, err := s.Update(ctx, saver, "DSAAAAAAAAAAA1", nil, bridgeInput("x", "ch-alerts"))
		return err
	}, "/type", CodeInvalidFormat)
	delete(w.conns, 7)
	assertField(t, "connection gone", func() error {
		_, err := s.Update(ctx, saver, "DSAAAAAAAAAAA4", nil, bridgeInput("y", "ch-alerts"))
		return err
	}, "/connection_id", CodeUnknownID)
	w.conns[7] = true
	w.fail["UpdateBridgeDestination"] = &pgconn.PgError{Code: uniqueViolation, ConstraintName: nameIndex}
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA4", nil, bridgeInput("ops", "ch-alerts")); !errors.Is(err,
		ErrNameTaken) {
		t.Errorf("name taken = %v", err)
	}
	delete(w.fail, "UpdateBridgeDestination")
	r.ID, r.Health = 99, "broken"
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA4", nil, bridgeInput("z", "ch-alerts")); !errors.Is(err, errBoom) {
		t.Errorf("a failed end of the broken state = %v", err)
	}
	r.ID = 4
	br.err = errBoom
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA4", nil, bridgeInput("z", "ch-alerts")); !errors.Is(err, errBoom) {
		t.Errorf("a failed check = %v", err)
	}
}

// TestCheckBridgeDestination is checkDestination of a Bridge Destination (C-22.FR-3): its one item channel, limited by
// the Destination, with the code of a failure; a passing check of a Broken Destination ends its Broken state, and a
// check stores no channel name it reads.
func TestCheckBridgeDestination(t *testing.T) {
	s, store, w, _, healed := newSaver(t)
	br := s.writer.Bridge.(*fakeBridge)
	ctx := t.Context()
	r := withBridge(store, w)
	r.BridgeChannelID = txt("ch-nobot")
	res, err := s.Check(ctx, "DSAAAAAAAAAAA4")
	want := CheckItem{Name: "channel", Message: "The bot is not a member of this channel.", Code: "channel_forbidden"}
	if err != nil || res.OK || len(res.Items) != 1 || !reflect.DeepEqual(res.Items[0], want) ||
		res.Health.State != "broken" ||
		len(*healed) != 0 {
		t.Errorf("failing = %+v, %v", res, err)
	}
	if c := br.calls[0]; c.Destination == nil || *c.Destination != 4 || c.Connection != "CNAAAAAAAAAAA3" ||
		c.ChannelID != "ch-nobot" {
		t.Errorf("check %+v", c)
	}
	r.BridgeChannelID = txt("ch-alerts")
	br.name = "renamed"
	res, err = s.Check(ctx, "DSAAAAAAAAAAA4")
	if err != nil || !res.OK || len(res.Items) != 1 ||
		!reflect.DeepEqual(res.Items[0], CheckItem{Name: "channel", OK: true}) ||
		res.Health.State != "healthy" || !slices.Equal(*healed, []int64{4}) {
		t.Errorf("recovered = %+v, %v, %v", res, err, *healed)
	}
	if len(w.brSets) != 0 || len(w.hints) != 0 || r.BridgeChannelName.String != "alerts" {
		t.Errorf("a check stored the name it read: %+v", w.brSets)
	}
	br.err = errBoom
	if _, err := s.Check(ctx, "DSAAAAAAAAAAA4"); !errors.Is(err, errBoom) {
		t.Errorf("failed check = %v", err)
	}
}

// TestTypeWithoutTesterIsErrNoTester: the test env registers no Tester for the Bridge type, so a Bridge Destination
// stands for any type without a Tester, and none for the Slack type, which has none in this build until S-099; their
// test or preview is ErrNoTester, which the API answers with 501, and nothing is recorded. A Tester without samples is
// a failure of Muster.
func TestTypeWithoutTesterIsErrNoTester(t *testing.T) {
	e := newTestEnv(t)
	withBridge(e.store, e.w)
	withSlack(e.store, e.w)
	for _, id := range []string{"DSAAAAAAAAAAA4", "DSAAAAAAAAAAA5"} {
		if _, err := e.s.Test(t.Context(), saver, id, Source{Kind: SourceExample}); !errors.Is(err, ErrNoTester) {
			t.Errorf("test %s = %v", id, err)
		}
		if _, err := e.s.Preview(t.Context(), id, Source{Kind: SourceExample}); !errors.Is(err, ErrNoTester) {
			t.Errorf("preview %s = %v", id, err)
		}
	}
	if len(e.w.audit) != 0 || len(e.tester.ins) != 0 {
		t.Errorf("recorded %+v, tested %+v", e.w.audit, e.tester.ins)
	}
	e.s.writer.Samples = nil
	if _, err := e.s.Preview(t.Context(), "DSAAAAAAAAAAA1", Source{Kind: SourceExample}); err == nil ||
		errors.Is(err, ErrNoTester) {
		t.Errorf("no samples = %v", err)
	}
}

func slackInput(name, channel string) Input {
	in := mattermostInput(name, "")
	in.Type, in.Mattermost = TypeSlack, nil
	n := in.Mentions[mentions.Kinds[0]]
	n.Everyone = mentions.EveryoneHere
	in.Mentions[mentions.Kinds[0]] = n
	in.Slack = &SlackInput{Connection: "cnaaaaaaaaaaa8", ChannelID: channel}
	return in
}

// withSlack adds the Slack Destination DSAAAAAAAAAAA5 on C0FAKEALERTS, Broken, to the store, and returns its row.
func withSlack(store *fakeStore, w *saveWriter) *dbgen.GetDestinationRow {
	store.rows = append(store.rows, slackRow())
	w.versions["DSAAAAAAAAAAA5"] = 1
	return &store.rows[len(store.rows)-1]
}

// TestCreateSlack is createDestination of type slack (C-24.FR-3, AC-4): the channel id, trimmed, and the Connection are
// checked through conversations.info before anything is saved, not limited by a Destination; a passing check stores the
// channel's name it read with the channel id as entered, the Mention settings and the limiter, and records
// destination.created with the hint.
func TestCreateSlack(t *testing.T) {
	s, _, w, _, _ := newSaver(t)
	sl := s.writer.Slack.(*fakeSlack)
	d, err := s.Create(t.Context(), saver, slackInput(" alerts ", " C0FAKEALERTS "))
	if err != nil {
		t.Fatal(err)
	}
	if len(sl.calls) != 1 || sl.calls[0].Destination != nil || sl.calls[0].Connection != "CNAAAAAAAAAAA8" ||
		sl.calls[0].ChannelID != "C0FAKEALERTS" {
		t.Errorf("checks %+v", sl.calls)
	}
	if d.ID != 51 || d.Type != TypeSlack || d.Name != "alerts" || *d.Connection != "CNAAAAAAAAAAA8" ||
		*d.SlackChannelID != "C0FAKEALERTS" || *d.SlackChannelName != "alerts" || d.Health.State != "healthy" ||
		d.LimiterLimit != 5 || d.Version != 1 || d.BridgeChannelID != nil || d.MattermostChannelID != nil {
		t.Errorf("created %+v", d)
	}
	if len(w.slIns) != 1 {
		t.Fatalf("inserts %+v", w.slIns)
	}
	ins := w.slIns[0]
	if ins.ConnectionID.Int64 != 8 || ins.ChannelID.String != "C0FAKEALERTS" || ins.ChannelName.String != "alerts" ||
		ins.Name != "alerts" || ins.LimiterLimit != 5 || ins.LimiterPerSeconds != 1 || !ins.Now.Equal(t0) ||
		!bytes.Contains(ins.Mentions, []byte(`"new_alert_group":{"everyone":"here"`)) {
		t.Errorf("inserted %+v", ins)
	}
	if len(w.audit) != 1 || w.audit[0].Action != ActionCreated ||
		!bytes.Contains(w.audit[0].Diff, []byte(`"/channel_id"`)) ||
		!bytes.Contains(w.audit[0].Diff, []byte("C0FAKEALERTS")) || len(w.hints) != 1 {
		t.Errorf("audit %+v hints %+v", w.audit, w.hints)
	}
	if got := counterSeries(d.PublicID); !slices.Equal(got, zeroCounterSeries(d.PublicID, TypeSlack)) {
		t.Errorf("counter series %q", got)
	}
	if d, err := s.Create(t.Context(), saver, slackInput("legacy", "G0FAKESECRET")); err != nil ||
		*d.SlackChannelID != "G0FAKESECRET" {
		t.Errorf("a G… channel = %v", err)
	}
}

// TestCreateSlackRefusals is C-24.AC-4: a channel without the bot is refused at /channel_id with "Invite the bot to the
// channel: /invite @{bot}" and the check code bot_not_in_channel, an archived one with channel_archived, an unknown one
// with channel_not_found, and nothing is saved; a refused token and a missing scope are at /connection_id, the scope
// kept as missing_scopes (D396); an id that is not C… or G… — a direct chat's, a user's, a #name, lower case — is
// invalid_format at /channel_id before any request; a Connection of another type or deleted, an empty channel and
// Mention settings Slack does not take are refused before anything is saved.
func TestCreateSlackRefusals(t *testing.T) {
	s, _, w, _, _ := newSaver(t)
	sl := s.writer.Slack.(*fakeSlack)
	ctx := t.Context()
	for channel, want := range map[string]CheckItem{
		"C0FAKEOTHER": {Name: slack.ItemChannel, Message: "Invite the bot to the channel: /invite @muster-dev-bot-b",
			Code: "bot_not_in_channel", Pointer: "/channel_id"},
		"C0FAKEARCH": {Name: slack.ItemChannel, Message: "The channel is archived.", Code: "channel_archived",
			Pointer: "/channel_id"},
		"C0FAKEGONE": {Name: slack.ItemChannel, Message: slack.MessageChannelNotFound, Code: "channel_not_found",
			Pointer: "/channel_id"},
		"CREVOKED": {Name: slack.ItemChannel, Message: slack.MessageTokenInvalid, Code: "token_invalid",
			Pointer: "/connection_id"},
		"CSCOPE": {Name: slack.ItemChannel, Message: "The bot token lacks the scopes channels:read.",
			Code: "missing_scope", MissingScopes: []string{"channels:read"}, Pointer: "/connection_id"},
	} {
		_, err := s.Create(ctx, saver, slackInput("x", channel))
		failed, ok := errors.AsType[*CheckFailedError](err)
		if !ok || len(failed.Items) != 1 || !reflect.DeepEqual(failed.Items[0], want) || failed.Error() == "" {
			t.Errorf("%s = %+v", channel, err)
		}
	}
	calls := len(sl.calls)
	for name, c := range map[string]struct {
		mut     func(in *Input)
		pointer string
		code    string
	}{
		"a direct chat":   {func(in *Input) { in.Slack.ChannelID = "D0FAKEDM" }, "/channel_id", CodeInvalidFormat},
		"a user":          {func(in *Input) { in.Slack.ChannelID = "U0MUSTERUSR1" }, "/channel_id", CodeInvalidFormat},
		"a name":          {func(in *Input) { in.Slack.ChannelID = "#alerts" }, "/channel_id", CodeInvalidFormat},
		"lower case":      {func(in *Input) { in.Slack.ChannelID = "c0fakealerts" }, "/channel_id", CodeInvalidFormat},
		"empty channel":   {func(in *Input) { in.Slack.ChannelID = " \t" }, "/channel_id", CodeRequired},
		"malformed id":    {func(in *Input) { in.Slack.Connection = "not-an-id" }, "/connection_id", CodeUnknownID},
		"no slack fields": {func(in *Input) { in.Slack = nil }, "/type", CodeInvalidFormat},
		"empty name":      {func(in *Input) { in.Name = " " }, "/name", CodeRequired},
		"no limiter":      {func(in *Input) { in.Limiter = Limiter{} }, "/limiter", CodeInvalidFormat},
	} {
		in := slackInput("x", "C0FAKEALERTS")
		c.mut(&in)
		_, err := s.Create(ctx, saver, in)
		fe, ok := errors.AsType[*FieldError](err)
		if !ok || fe.Pointer != c.pointer || fe.Code != c.code {
			t.Errorf("%s = %v", name, err)
		}
	}
	in := slackInput("x", "C0FAKEALERTS")
	in.Mentions["new_alerts"] = mentions.Setting{Everyone: "none", UserIDs: []string{"SR0000000000ZZ"}, Groups: []string{}}
	if _, err := s.Create(ctx, saver, in); !errors.As(err, new(*mentions.FieldError)) {
		t.Errorf("unknown user = %v", err)
	}
	if len(sl.calls) != calls {
		t.Errorf("a refused field ran the check: %+v", sl.calls[calls:])
	}
	in = slackInput("x", "C0FAKEALERTS")
	in.Slack.Connection = "CNAAAAAAAAAAA3"
	_, err := s.Create(ctx, saver, in)
	if fe, ok := errors.AsType[*FieldError](err); !ok || fe.Pointer != "/connection_id" || fe.Code != CodeUnknownID ||
		fe.Detail != "No such Slack Connection." {
		t.Errorf("a bridge connection = %v", err)
	}
	if len(w.slIns) != 0 {
		t.Fatalf("a refused save inserted %+v", w.slIns)
	}
	w.fail["InsertSlackDestination"] = &pgconn.PgError{Code: uniqueViolation, ConstraintName: nameIndex}
	if _, err := s.Create(ctx, saver, slackInput("ops", "C0FAKEALERTS")); !errors.Is(err, ErrNameTaken) {
		t.Errorf("name taken = %v", err)
	}
	delete(w.fail, "InsertSlackDestination")
	delete(w.conns, 8)
	assertField(t, "connection deleted since the check", func() error {
		_, err := s.Create(ctx, saver, slackInput("late", "C0FAKEALERTS"))
		return err
	}, "/connection_id", CodeUnknownID)
	w.fail["LockSlackConnection"] = errBoom
	if _, err := s.Create(ctx, saver, slackInput("late", "C0FAKEALERTS")); !errors.Is(err, errBoom) {
		t.Errorf("lock failed = %v", err)
	}
	sl.err = &delivery.LimitedError{RetryAfter: 2}
	if _, err := s.Create(ctx, saver, slackInput("busy", "C0FAKEALERTS")); !errors.As(err,
		new(*delivery.LimitedError)) {
		t.Errorf("limited = %v", err)
	}
	if len(w.slIns) != 0 || len(w.audit) != 0 {
		t.Fatalf("a failed save wrote %+v %+v", w.slIns, w.audit)
	}
}

// TestUpdateSlack is updateDestination of type slack (C-24.FR-3): the check runs limited by the Destination; a change
// is recorded with its diff, a new channel name the check read is stored without an Audit log entry, an unchanged save
// writes nothing, and a passing save of a Broken Destination ends its Broken state.
func TestUpdateSlack(t *testing.T) {
	s, store, w, _, healed := newSaver(t)
	sl := s.writer.Slack.(*fakeSlack)
	ctx := t.Context()
	r := withSlack(store, w)
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA5", nil, slackInput("alerts", "C0FAKEOTHER")); !errors.As(err,
		new(*CheckFailedError)) || len(w.slSets) != 0 || len(*healed) != 0 {
		t.Errorf("refused = %v", err)
	}
	d, err := s.Update(ctx, saver, "DSAAAAAAAAAAA5", new(int64(1)), slackInput("alerts", "C0FAKEPROD"))
	if err != nil {
		t.Fatal(err)
	}
	if last := sl.calls[len(sl.calls)-1]; last.Destination == nil || *last.Destination != 5 ||
		last.ChannelID != "C0FAKEPROD" {
		t.Errorf("the check is not limited by the destination: %+v", last)
	}
	if d.Health.State != "healthy" || !slices.Equal(*healed, []int64{5}) || len(w.slSets) != 1 ||
		w.slSets[0].ID != 5 || w.slSets[0].ConnectionID.Int64 != 8 || w.slSets[0].ChannelID.String != "C0FAKEPROD" ||
		w.slSets[0].ChannelName.String != "alerts" || len(w.audit) != 1 || w.audit[0].Action != ActionUpdated ||
		!bytes.Contains(w.audit[0].Diff, []byte("C0FAKEPROD")) {
		t.Errorf("updated %+v, sets %+v, audit %+v", d, w.slSets, w.audit)
	}
	// Stored as saved: the same save writes nothing; a new channel name is stored with a hint only.
	r.Name, r.SlackChannelID, r.Version, r.Health = "alerts", txt("C0FAKEPROD"), 2, "healthy"
	r.Mentions, _ = json.Marshal(slackInput("x", "").Mentions)
	r.LimiterLimit, r.LimiterPerSeconds = 5, 1
	w.versions["DSAAAAAAAAAAA5"] = 2
	if d, err := s.Update(ctx, saver, "DSAAAAAAAAAAA5", nil, slackInput("alerts", "C0FAKEPROD")); err != nil ||
		len(w.slSets) != 1 || d.Version != 2 {
		t.Errorf("an unchanged save wrote: %v %d", err, len(w.slSets))
	}
	sl.name = "alerts-renamed"
	hints := len(w.hints)
	if d, err := s.Update(ctx, saver, "DSAAAAAAAAAAA5", nil, slackInput("alerts", "C0FAKEPROD")); err != nil ||
		len(w.slSets) != 2 || w.slSets[1].ChannelName.String != "alerts-renamed" || len(w.audit) != 1 ||
		len(w.hints) != hints+1 || *d.SlackChannelName != "alerts-renamed" || d.Version != 3 {
		t.Errorf("a new channel name = %+v %v", d, err)
	}
	w.versions["DSAAAAAAAAAAA5"] = 9
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA5", nil, slackInput("moved", "C0FAKEALERTS")); !errors.Is(err,
		ErrVersionMismatch) {
		t.Errorf("raced = %v", err)
	}
	delete(w.versions, "DSAAAAAAAAAAA5")
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA5", nil, slackInput("gone", "C0FAKEALERTS")); !errors.Is(err,
		ErrNotFound) {
		t.Errorf("gone = %v", err)
	}
	w.versions["DSAAAAAAAAAAA5"] = 2
	w.fail["LockDestination"] = errBoom
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA5", nil, slackInput("x", "C0FAKEALERTS")); !errors.Is(err,
		errBoom) {
		t.Errorf("lock = %v", err)
	}
	delete(w.fail, "LockDestination")
	assertField(t, "type changed", func() error {
		_, err := s.Update(ctx, saver, "DSAAAAAAAAAAA1", nil, slackInput("x", "C0FAKEALERTS"))
		return err
	}, "/type", CodeInvalidFormat)
	assertField(t, "an id that is not a channel", func() error {
		_, err := s.Update(ctx, saver, "DSAAAAAAAAAAA5", nil, slackInput("y", "D0FAKEDM"))
		return err
	}, "/channel_id", CodeInvalidFormat)
	delete(w.conns, 8)
	assertField(t, "connection gone", func() error {
		_, err := s.Update(ctx, saver, "DSAAAAAAAAAAA5", nil, slackInput("y", "C0FAKEALERTS"))
		return err
	}, "/connection_id", CodeUnknownID)
	w.conns[8] = true
	w.fail["UpdateSlackDestination"] = &pgconn.PgError{Code: uniqueViolation, ConstraintName: nameIndex}
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA5", nil, slackInput("ops", "C0FAKEALERTS")); !errors.Is(err,
		ErrNameTaken) {
		t.Errorf("name taken = %v", err)
	}
	delete(w.fail, "UpdateSlackDestination")
	r.ID, r.Health = 99, "broken"
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA5", nil, slackInput("z", "C0FAKEALERTS")); !errors.Is(err,
		errBoom) {
		t.Errorf("a failed end of the broken state = %v", err)
	}
	r.ID = 5
	sl.err = errBoom
	if _, err := s.Update(ctx, saver, "DSAAAAAAAAAAA5", nil, slackInput("z", "C0FAKEALERTS")); !errors.Is(err,
		errBoom) {
		t.Errorf("a failed check = %v", err)
	}
}

// TestCheckSlackDestination is checkDestination of a Slack Destination (C-24.FR-3): its one item channel, limited by
// the Destination, with the code of a failure and, for missing_scope, the scopes (D396); a passing check of a Broken
// Destination ends its Broken state, and a check stores no channel name it reads.
func TestCheckSlackDestination(t *testing.T) {
	s, store, w, _, healed := newSaver(t)
	sl := s.writer.Slack.(*fakeSlack)
	ctx := t.Context()
	r := withSlack(store, w)
	r.SlackChannelID = txt("C0FAKEOTHER")
	res, err := s.Check(ctx, "DSAAAAAAAAAAA5")
	want := CheckItem{Name: "channel", Message: "Invite the bot to the channel: /invite @muster-dev-bot-b",
		Code: "bot_not_in_channel"}
	if err != nil || res.OK || len(res.Items) != 1 || !reflect.DeepEqual(res.Items[0], want) ||
		res.Health.State != "broken" || len(*healed) != 0 || res.Run == "" {
		t.Errorf("failing = %+v, %v", res, err)
	}
	if c := sl.calls[0]; c.Destination == nil || *c.Destination != 5 || c.Connection != "CNAAAAAAAAAAA8" ||
		c.ChannelID != "C0FAKEOTHER" {
		t.Errorf("check %+v", c)
	}
	r.SlackChannelID = txt("CSCOPE")
	res, err = s.Check(ctx, "DSAAAAAAAAAAA5")
	if err != nil || res.OK || !reflect.DeepEqual(res.Items[0], CheckItem{Name: "channel", Code: "missing_scope",
		Message: "The bot token lacks the scopes channels:read.", MissingScopes: []string{"channels:read"}}) {
		t.Errorf("missing scope = %+v, %v", res, err)
	}
	r.SlackChannelID = txt("C0FAKEALERTS")
	sl.name = "renamed"
	res, err = s.Check(ctx, "DSAAAAAAAAAAA5")
	if err != nil || !res.OK || len(res.Items) != 1 ||
		!reflect.DeepEqual(res.Items[0], CheckItem{Name: "channel", OK: true}) || res.Health.State != "healthy" ||
		!slices.Equal(*healed, []int64{5}) {
		t.Errorf("recovered = %+v, %v, %v", res, err, *healed)
	}
	if len(w.slSets) != 0 || len(w.hints) != 0 || r.SlackChannelName.String != "alerts" {
		t.Errorf("a check stored the name it read: %+v", w.slSets)
	}
	sl.err = errBoom
	if _, err := s.Check(ctx, "DSAAAAAAAAAAA5"); !errors.Is(err, errBoom) {
		t.Errorf("failed check = %v", err)
	}
}

// TestDeleteSlack is deleteDestination of a Slack Destination (C-11.FR-14): the deletion does not depend on the type —
// it is marked deleted, leaves its Routes, gets its final edits and destination.deleted, as any other.
func TestDeleteSlack(t *testing.T) {
	s, f, called := newDeleter(t)
	f.rows = append(f.rows, &fakeDestination{id: 5, publicID: "DSAAAAAAAAAAA5", name: "alerts-sl", version: 1,
		routes: []int64{9}})
	if err := s.Delete(t.Context(), deleter, "DSAAAAAAAAAAA5", new(int64(1))); err != nil {
		t.Fatal(err)
	}
	d := f.find(5)
	if !d.deleted || d.version != 2 || d.routes != nil || !slices.Equal(*called, []int64{5}) ||
		!slices.Equal(f.bumped, []int64{9}) || len(f.audit) != 1 || f.audit[0].Action != ActionDeleted ||
		f.audit[0].ResourcePublicID.String != "DSAAAAAAAAAAA5" {
		t.Errorf("deleted %+v, retired %v, routes %v, audit %+v", d, *called, f.bumped, f.audit)
	}
}

// TestOwningTeam is C-25.FR-2 and C-08.FR-1 revised for Destinations: every type takes the owning Team, read in share
// mode in the save's transaction and written with it, recorded in the Audit log diff and read back with its name and
// slug; an update that leaves it out keeps it, an empty one clears it, and an unknown, malformed or deleted Team is
// unknown_id at /owning_team_id. The list filters by a Team and by the Teams of a User.
func TestOwningTeam(t *testing.T) {
	s, store, w, _, _ := newSaver(t)
	ctx := t.Context()
	owned := func(in Input, id string) Input {
		in.OwningTeamID = &id
		return in
	}
	d, err := s.Create(ctx, saver, owned(mattermostInput("alerts", "ch-alerts"), "tm0000000000pa"))
	if err != nil {
		t.Fatal(err)
	}
	if o := d.OwningTeam; o == nil || o.ID != 41 || o.PublicID != "TM0000000000PA" || o.Name != "Payments" ||
		o.Slug != "payments" {
		t.Fatalf("owning team %+v", d.OwningTeam)
	}
	if len(w.owners) != 1 || w.owners[0].ID != d.ID || w.owners[0].OwningTeamID.Int64 != 41 {
		t.Errorf("owners %+v", w.owners)
	}
	if diff := string(w.audit[0].Diff); !strings.Contains(diff,
		`{"pointer":"/owning_team_id","after":"TM0000000000PA"}`) {
		t.Errorf("created diff %s", diff)
	}
	for _, in := range []Input{webhookInput("hook", "https://example.org/x"), telegramInput("tg", "@muster_alerts"),
		bridgeInput("br", "ch-alerts"), slackInput("sl", "C0FAKEALERTS")} {
		got, err := s.Create(ctx, saver, owned(in, "TM0000000000PA"))
		if err != nil || got.OwningTeam == nil || got.OwningTeam.Slug != "payments" {
			t.Errorf("%s: %+v, %v", in.Type, got.OwningTeam, err)
		}
	}

	for _, id := range []string{"TM0000000000ZZ", "DS0000000000PA", "x"} {
		assertField(t, id, func() error {
			_, err := s.Create(ctx, saver, owned(mattermostInput("bad", "ch-alerts"), id))
			return err
		}, "/owning_team_id", CodeUnknownID)
	}
	w.fail["LockOwningTeam"] = errBoom
	if _, err := s.Create(ctx, saver, owned(mattermostInput("bad", "ch-alerts"), "TM0000000000PA")); !errors.Is(err,
		errBoom) {
		t.Errorf("failed lock = %v", err)
	}
	delete(w.fail, "LockOwningTeam")
	w.fail["SetDestinationOwningTeam"] = errBoom
	if _, err := s.Create(ctx, saver, mattermostInput("bad", "ch-alerts")); !errors.Is(err, errBoom) {
		t.Errorf("failed write = %v", err)
	}
	delete(w.fail, "SetDestinationOwningTeam")

	// DSAAAAAAAAAAA1 is owned by Payments: an update without the owner keeps it, an empty one clears it.
	store.rows[0].OwningTeamID = pgtype.Int8{Int64: 41, Valid: true}
	store.rows[0].OwningTeamPublicID, store.rows[0].OwningTeamName = txt("TM0000000000PA"), txt("Payments")
	store.rows[0].OwningTeamSlug = txt("payments")
	w.owners = nil
	kept, err := s.Update(ctx, saver, "DSAAAAAAAAAAA1", nil, mattermostInput("ops", "chan"))
	if err != nil || kept.OwningTeam == nil || kept.OwningTeam.PublicID != "TM0000000000PA" || len(w.owners) != 1 ||
		w.owners[0].OwningTeamID.Int64 != 41 {
		t.Fatalf("kept = %+v, %+v, %v", kept.OwningTeam, w.owners, err)
	}
	cleared, err := s.Update(ctx, saver, "DSAAAAAAAAAAA1", nil, owned(mattermostInput("ops", "chan"), ""))
	if err != nil || cleared.OwningTeam != nil || w.owners[1].OwningTeamID.Valid {
		t.Fatalf("cleared = %+v, %+v, %v", cleared.OwningTeam, w.owners, err)
	}
	if diff := string(w.audit[len(w.audit)-1].Diff); !strings.Contains(diff,
		`{"pointer":"/owning_team_id","before":"TM0000000000PA"}`) {
		t.Errorf("cleared diff %s", diff)
	}
	assertField(t, "update", func() error {
		_, err := s.Update(ctx, saver, "DSAAAAAAAAAAA1", nil, owned(mattermostInput("ops", "chan"), "TM0000000000ZZ"))
		return err
	}, "/owning_team_id", CodeUnknownID)

	page, err := s.List(ctx, ListFilter{Team: new("nonsense"), Limit: 10})
	if err != nil || len(page.Destinations) != 0 {
		t.Errorf("a Team that cannot exist: %+v, %v", page, err)
	}
	lists := len(store.lists)
	if _, err := s.List(ctx, ListFilter{Team: new("tm0000000000pa"), TeamUser: new(int64(5)), Limit: 10}); err != nil {
		t.Fatal(err)
	}
	if got := store.lists[lists]; got.Team.String != "TM0000000000PA" || got.TeamUser.Int64 != 5 {
		t.Errorf("list params %+v", got)
	}
	ref := s.Get
	if got, err := ref(ctx, "DSAAAAAAAAAAA1"); err != nil || got.OwningTeam == nil || got.OwningTeam.Name != "Payments" {
		t.Errorf("read %+v, %v", got.OwningTeam, err)
	}
}

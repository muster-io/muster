// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package destinations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/audit"
	auditdb "github.com/muster-io/muster/internal/audit/dbgen"
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

func (f *fakeWriter) InsertWebhookDestination(context.Context, dbgen.InsertWebhookDestinationParams) (int64, error) {
	return 0, errBoom
}

func (f *fakeWriter) UpdateWebhookDestination(context.Context, dbgen.UpdateWebhookDestinationParams) error {
	return errBoom
}

// saveWriter is the database of the saves in memory: the Mattermost Connections that are not deleted, the inserted
// and updated rows, the versions the locks read, the Audit log and the hints.
type saveWriter struct {
	conns    map[int64]bool
	versions map[string]int64
	inserted []dbgen.InsertMattermostDestinationParams
	updated  []dbgen.UpdateMattermostDestinationParams
	tgIns    []dbgen.InsertTelegramDestinationParams
	tgSets   []dbgen.UpdateTelegramDestinationParams
	hooks    []dbgen.InsertWebhookDestinationParams
	hookSets []dbgen.UpdateWebhookDestinationParams
	audit    []auditdb.InsertAuditEntryParams
	hints    []db.Hint
	fail     map[string]error
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
	err   error
}

func (c *fakeChecker) CheckChannel(_ context.Context, in ChannelCheck) (ChannelChecked, error) {
	c.calls = append(c.calls, in)
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
			{Name: mattermost.StepBotInChannel, Message: mattermost.MessageNotMember, Outcome: fatal}}}}, nil
	case "revoked":
		return ChannelChecked{ConnectionID: 5, Check: mattermost.Check{Outcome: fatal, Steps: []mattermost.Step{
			{Name: mattermost.StepToken, Message: mattermost.MessageTokenInvalid, Outcome: fatal}}}}, nil
	}
	return ChannelChecked{ConnectionID: 5, Check: mattermost.Check{Outcome: ok, TeamName: "dev",
		ChannelName: "alerts", Steps: []mattermost.Step{{Name: mattermost.StepToken, OK: true},
			{Name: mattermost.StepBotInChannel, OK: true}}}}, nil
}

// fakeTelegram stands for the Connections: the Destination check of a Telegram channel through the Connection
// CNAAAAAAAAAAA2, by its channel.
type fakeTelegram struct {
	calls []TelegramChannelCheck
	err   error
	title string
}

func (c *fakeTelegram) CheckTelegramChannel(_ context.Context, in TelegramChannelCheck) (TelegramChannelChecked,
	error) {
	c.calls = append(c.calls, in)
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
	fatal := func(name, message string) telegram.DestinationStep {
		return telegram.DestinationStep{Name: name, Message: message,
			Outcome: delivery.Outcome{Kind: delivery.OutcomeFatal, Error: outbound.Untrusted(message)}}
	}
	ok := func(name string) telegram.DestinationStep { return telegram.DestinationStep{Name: name, OK: true} }
	switch in.ChannelID {
	case "@no_comments":
		return fail([]telegram.DestinationStep{ok(telegram.StepChannelExists),
			fatal(telegram.StepDiscussionGroup, telegram.MessageNoComments)})
	case "@no_group_admin":
		return fail([]telegram.DestinationStep{ok(telegram.StepChannelExists), ok(telegram.StepDiscussionGroup),
			ok(telegram.StepBotRightsChannel),
			fatal(telegram.StepBotRightsGroup, telegram.MessageGroupNotAdmin("Muster alerts Chat"))})
	case "@revoked":
		return fail([]telegram.DestinationStep{fatal(telegram.StepChannelExists, telegram.MessageTokenInvalid)})
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
	w := &saveWriter{conns: map[int64]bool{5: true, 6: true}, versions: map[string]int64{"DSAAAAAAAAAAA1": 2,
		"DSAAAAAAAAAAA3": 4}, fail: map[string]error{}}
	checker := &fakeChecker{}
	var healed []int64
	s := New(1, store)
	logger := logging.New(&bytes.Buffer{}, logging.LevelInfo)
	s.SetWriter(WriterConfig{Writer: w, Audit: audit.NewWriter(logger, clock.NewManual(t0)),
		Business: clock.NewManual(t0), Mentions: fakeMentions{}, Mattermost: checker, Telegram: &fakeTelegram{},
		Keyring:   activeKeyring(t),
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
		!stringsContain(failed.Error(), "bot_in_channel") {
		t.Errorf("no bot = %v", err)
	}
	if _, err = s.Create(ctx, saver, mattermostInput("revoked", "revoked")); !errors.As(err, &failed) ||
		failed.Items[0].Pointer != "/connection_id" {
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

// TestCheckDestination is checkDestination (C-13.FR-10): each check with its result, limited by the Destination; a
// passing check of a Broken Destination ends its Broken state; a type without a check is check_not_supported.
func TestCheckDestination(t *testing.T) {
	s, store, _, checker, healed := newSaver(t)
	ctx := t.Context()
	store.rows[0].MattermostChannelID = txt("ch-nobot")
	res, err := s.Check(ctx, "DSAAAAAAAAAAA1")
	if err != nil || res.OK || len(res.Items) != 2 || res.Items[1].Message != mattermost.MessageNotMember ||
		res.Items[0].Name != "token" || !res.Items[0].OK || res.Health.State != "healthy" {
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
		*d.TelegramDiscussionGroupID != "-1001000000002" || *d.TelegramDiscussionGroupTitle != "Muster alerts Chat" ||
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
}

// TestCreateTelegramRefusals is C-14.AC-13: a channel without comments and a discussion group whose admins do not
// include the bot are refused with the texts of reference.md, and nothing is saved; so are an invalid channel, an
// unknown Connection and a refused token.
func TestCreateTelegramRefusals(t *testing.T) {
	s, _, w, _, _ := newSaver(t)
	tg := s.writer.Telegram.(*fakeTelegram)
	ctx := t.Context()
	for channel, want := range map[string]CheckItem{
		"@no_comments": {Name: "discussion_group", Message: telegram.MessageNoComments, Pointer: "/channel_id"},
		"@no_group_admin": {Name: "bot_rights_group", Pointer: "/channel_id",
			Message: "The bot is not an admin of the discussion group Muster alerts Chat. Make the bot an admin there, " +
				"allowed to post messages."},
		"@revoked": {Name: "channel_exists", Message: telegram.MessageTokenInvalid, Pointer: "/connection_id"},
	} {
		_, err := s.Create(ctx, saver, telegramInput("x", channel))
		failed, ok := errors.AsType[*CheckFailedError](err)
		if !ok || len(failed.Items) != 1 || failed.Items[0] != want {
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
	if d.Health.State != "healthy" || len(*healed) != 1 || len(w.tgSets) != 1 ||
		w.tgSets[0].ChannelID.String != "@muster_alerts" || w.tgSets[0].DiscussionChatID.Int64 != -1001000000002 ||
		len(w.audit) != 1 || w.audit[0].Action != ActionUpdated {
		t.Errorf("updated %+v, sets %+v, audit %+v", d, w.tgSets, w.audit)
	}
	// Stored as the check found it: the same save writes nothing; a new title is stored with a hint only.
	r := &store.rows[1]
	r.TelegramChannelID, r.Version, r.Health = txt("@muster_alerts"), 2, "healthy"
	r.Mentions, _ = json.Marshal(telegramInput("x", "").Mentions)
	r.LimiterLimit, r.LimiterPerSeconds = 5, 1
	r.TelegramDiscussionChatID.Int64 = -1001000000002
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
		res.Health.State != "broken" {
		t.Errorf("failing = %+v, %v", res, err)
	}
	if c := tg.calls[0]; c.Destination == nil || *c.Destination != 2 || c.Connection != "CNAAAAAAAAAAA2" {
		t.Errorf("check %+v", c)
	}
	store.rows[1].TelegramChannelID = txt("@muster_alerts")
	// The stored discussion group (-100200) is not the one the check finds: the check fails until a save.
	res, err = s.Check(ctx, "DSAAAAAAAAAAA2")
	if err != nil || res.OK || res.Items[1].Message != telegram.MessageMoved || len(*healed) != 0 {
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

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package destinations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/audit"
	auditdb "github.com/muster-io/muster/internal/audit/dbgen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/destinations/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mattermost"
	"github.com/muster-io/muster/internal/mentions"
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

// saveWriter is the database of the saves in memory: the Mattermost Connections that are not deleted, the inserted
// and updated rows, the versions the locks read, the Audit log and the hints.
type saveWriter struct {
	conns    map[int64]bool
	versions map[string]int64
	inserted []dbgen.InsertMattermostDestinationParams
	updated  []dbgen.UpdateMattermostDestinationParams
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
	w := &saveWriter{conns: map[int64]bool{5: true}, versions: map[string]int64{"DSAAAAAAAAAAA1": 2},
		fail: map[string]error{}}
	checker := &fakeChecker{}
	var healed []int64
	s := New(1, store)
	logger := logging.New(&bytes.Buffer{}, logging.LevelInfo)
	s.SetWriter(WriterConfig{Writer: w, Audit: audit.NewWriter(logger, clock.NewManual(t0)),
		Business: clock.NewManual(t0), Mentions: fakeMentions{}, Mattermost: checker,
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
	d, err := s.Update(ctx, saver, "DSAAAAAAAAAAA1", new(int64(2)), mattermostInput("ops-renamed", "chan"))
	if err != nil {
		t.Fatal(err)
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

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit/dbgen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
)

type fakeStore struct {
	rows []dbgen.InsertAuditEntryParams
	err  error
}

func (s *fakeStore) InsertAuditEntry(_ context.Context, arg dbgen.InsertAuditEntryParams) error {
	if s.err != nil {
		return s.err
	}
	s.rows = append(s.rows, arg)
	return nil
}

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func newWriter() (*Writer, *bytes.Buffer) {
	var log bytes.Buffer
	return NewWriter(logging.New(&log, logging.LevelInfo), clock.NewManual(t0)), &log
}

// TestRecord is C-03.FR-14's writer: the entry is appended with its actor, Transport, action, resource, diff and
// details on the business clock, and copied to the log as audit_entry.
func TestRecord(t *testing.T) {
	w, log := newWriter()
	s := &fakeStore{}
	addr := netip.MustParseAddr("192.0.2.1")
	err := w.Record(t.Context(), s, Entry{
		OrgID: 7, Actor: Actor{Kind: ActorUser, ID: 3, PublicID: "SRAAAAAAAAAAAA", TokenID: 9, TokenName: "ci"},
		Transport: TransportUI, Action: ActionProfileUpdated,
		Resource: Resource{Type: ResourceUser, PublicID: "SRAAAAAAAAAAAA", Name: "Alice"},
		Diff:     Changed("/name", "A", "Alice"), Details: map[string]any{"n": 1}, SourceAddress: addr,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := s.rows[0]
	if r.OrgID != 7 || !strings.HasPrefix(r.PublicID, "AE") || !r.At.Equal(t0) || r.ActorKind != "user" ||
		r.ActorUserID.Int64 != 3 || r.ActorServiceAccountID.Valid || r.ApiTokenID.Int64 != 9 ||
		r.TokenName.String != "ci" || r.Transport != "ui" || r.Action != "user.profile_updated" ||
		r.ResourceType.String != "user" || r.ResourceName.String != "Alice" || *r.SourceAddress != addr {
		t.Errorf("row = %+v", r)
	}
	if string(r.Diff) != `[{"pointer":"/name","before":"A","after":"Alice"}]` || string(r.Details) != `{"n":1}` {
		t.Errorf("diff %s details %s", r.Diff, r.Details)
	}
	var line map[string]any
	if err := json.Unmarshal(log.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]any{"event": "audit_entry", "level": "INFO", "actor_kind": "user",
		"actor": "SRAAAAAAAAAAAA", "action": "user.profile_updated", "resource_name": "Alice", "transport": "ui",
		"source_address": "192.0.2.1", "token_name": "ci", "entry": r.PublicID} {
		if line[k] != v {
			t.Errorf("log %s = %v, want %v", k, line[k], v)
		}
	}
}

func TestRecordEmptyDiffAndDetails(t *testing.T) {
	w, log := newWriter()
	s := &fakeStore{}
	if err := w.Record(t.Context(), s, Entry{OrgID: 1, Actor: Bootstrap, Transport: TransportSystem,
		Action: ActionUserCreated}); err != nil {
		t.Fatal(err)
	}
	if r := s.rows[0]; string(r.Diff) != "[]" || string(r.Details) != "{}" || r.ActorUserID.Valid ||
		r.SourceAddress != nil || r.ResourceType.Valid {
		t.Errorf("row = %+v", r)
	}
	if strings.Contains(log.String(), `"diff"`) || strings.Contains(log.String(), `"resource"`) {
		t.Errorf("empty fields were logged: %s", log)
	}
	sa := Actor{Kind: ActorServiceAccount, ID: 4}
	if err := w.Record(t.Context(), s, Entry{OrgID: 1, Actor: sa, Transport: TransportAPI, Action: "x.y"}); err != nil {
		t.Fatal(err)
	}
	if r := s.rows[1]; r.ActorServiceAccountID.Int64 != 4 || r.ActorUserID.Valid {
		t.Errorf("service account row = %+v", r)
	}
	cli := Actor{Kind: ActorCLI, Name: "ops-alice"}
	if err := w.Record(t.Context(), s, Entry{OrgID: 1, Actor: cli, Transport: TransportCLI, Action: "x.y"}); err != nil {
		t.Fatal(err)
	}
	if r := s.rows[2]; r.ActorName.String != "ops-alice" {
		t.Errorf("cli row = %+v", r)
	}
}

// TestRecordRefuses entries that break the rules of audit_log before writing anything.
func TestRecordRefuses(t *testing.T) {
	w, _ := newWriter()
	for name, e := range map[string]Entry{
		"action":            {Actor: System, Transport: TransportSystem, Action: "Created"},
		"user without id":   {Actor: Actor{Kind: ActorUser}, Transport: TransportUI, Action: "a.b"},
		"cli without name":  {Actor: Actor{Kind: ActorCLI}, Transport: TransportCLI, Action: "a.b"},
		"cli over ui":       {Actor: Actor{Kind: ActorCLI, Name: "x"}, Transport: TransportUI, Action: "a.b"},
		"system with id":    {Actor: Actor{Kind: ActorSystem, ID: 1}, Transport: TransportSystem, Action: "a.b"},
		"unknown actor":     {Actor: Actor{Kind: "robot"}, Transport: TransportSystem, Action: "a.b"},
		"unknown transport": {Actor: System, Transport: "smoke", Action: "a.b"},
		"details":           {Actor: System, Transport: TransportSystem, Action: "a.b", Details: map[string]any{"f": func() {}}},
		"diff":              {Actor: System, Transport: TransportSystem, Action: "a.b", Diff: []Change{{After: func() {}}}},
	} {
		s := &fakeStore{}
		if err := w.Record(t.Context(), s, e); err == nil || len(s.rows) != 0 {
			t.Errorf("%s: %v, %d rows", name, err, len(s.rows))
		}
	}
	boom := errors.New("boom")
	if err := w.Record(t.Context(), &fakeStore{err: boom}, Entry{Actor: System, Transport: TransportSystem,
		Action: "a.b"}); !errors.Is(err, boom) {
		t.Errorf("a failed insert: %v", err)
	}
}

func TestChangedAndActors(t *testing.T) {
	if Changed("/a", 1, 1) != nil || len(Changed("/a", "x", "y")) != 1 {
		t.Error("Changed")
	}
	if a := User(5, "SRX"); a.Kind != ActorUser || a.ID != 5 || a.PublicID != "SRX" {
		t.Errorf("User = %+v", a)
	}
	if NewStore(nil) == nil {
		t.Error("NewStore")
	}
}

type proxy struct {
	Address  string `json:"address"`
	Password string `json:"password" audit:"secret"`
}

type settings struct {
	Name     string            `json:"name"`
	Email    *string           `json:"email"`
	Proxy    proxy             `json:"proxy"`
	Next     *proxy            `json:"next"`
	Labels   map[string]string `json:"labels"`
	Path     string            `json:"a/b~c"`
	Untagged int
	Skipped  string `json:"-"`
	private  string
	Token    string `json:"token" audit:"secret"`
}

// TestDiff is the before/after diff of C-03.FR-14 with the Secret rule of C-03.FR-21: changed values at their JSON
// pointer, nested objects walked, Secrets only marked as changed.
func TestDiff(t *testing.T) {
	email := "a@example.org"
	before := settings{Name: "A", Proxy: proxy{Address: "p:1", Password: "old"}, Next: &proxy{Address: "n"},
		Labels: map[string]string{"a": "1"}, Path: "x", Untagged: 1, Skipped: "s", private: "p", Token: "t1"}
	after := before
	after.Name, after.Email, after.Proxy.Password, after.Next = "B", &email, "new", &proxy{Address: "m"}
	after.Labels, after.Path, after.Untagged, after.Skipped, after.private = map[string]string{"a": "2"}, "y", 2, "t", "q"
	got := Diff(before, after)
	b, _ := json.Marshal(got)
	want := `[{"pointer":"/name","before":"A","after":"B"},{"pointer":"/email","after":"a@example.org"},` +
		`{"pointer":"/proxy/password","secret_changed":true},{"pointer":"/next/address","before":"n","after":"m"},` +
		`{"pointer":"/labels","before":{"a":"1"},"after":{"a":"2"}},{"pointer":"/a~1b~0c","before":"x","after":"y"},` +
		`{"pointer":"/Untagged","before":1,"after":2}]`
	if string(b) != want {
		t.Errorf("diff\n %s\nwant\n %s", b, want)
	}
	if strings.Contains(string(b), "old") || strings.Contains(string(b), "new") {
		t.Error("a Secret value is in the diff")
	}
	if d := Diff(before, before); d != nil {
		t.Errorf("no change = %+v", d)
	}
	after = before
	before.Next.Password = "secret-value"
	after.Next, after.Token = nil, "t2"
	b, _ = json.Marshal(Diff(before, after))
	if string(b) != `[{"pointer":"/next/address","before":"n","after":""},{"pointer":"/next/password","secret_changed":true},`+
		`{"pointer":"/token","secret_changed":true}]` {
		t.Errorf("a removed object = %s", b)
	}
	b, _ = json.Marshal(Diff(after, before))
	if !strings.Contains(string(b), `{"pointer":"/next/password","secret_changed":true}`) || strings.Contains(string(b), "secret-value") {
		t.Errorf("an added object = %s", b)
	}
	b, _ = json.Marshal(Created(settings{Name: "C", Token: "t"}))
	if string(b) != `[{"pointer":"/name","after":"C"},{"pointer":"/token","secret_changed":true}]` {
		t.Errorf("created = %s", b)
	}
	if d := Diff(time.Time{}, t0); len(d) != 1 || d[0].Pointer != "" {
		t.Errorf("a time = %+v", d)
	}
	if a := CLI("ops"); a.Kind != ActorCLI || a.Name != "ops" {
		t.Errorf("CLI = %+v", a)
	}
}

// fakeList answers the list queries from rows in memory, newest first, and records the parameters.
type fakeList struct {
	rows  []dbgen.ListAuditEntriesRow
	users map[string]int64
	sas   map[string]int64
	arg   dbgen.ListAuditEntriesParams
	err   error
}

func (f *fakeList) FindUserActor(_ context.Context, arg dbgen.FindUserActorParams) (int64, error) {
	if id, ok := f.users[arg.PublicID]; ok && arg.OrgID == 7 {
		return id, nil
	}
	return 0, f.notFound()
}

func (f *fakeList) FindServiceAccountActor(_ context.Context, arg dbgen.FindServiceAccountActorParams) (int64, error) {
	if id, ok := f.sas[arg.PublicID]; ok && arg.OrgID == 7 {
		return id, nil
	}
	return 0, f.notFound()
}

func (f *fakeList) notFound() error {
	if f.err != nil {
		return f.err
	}
	return pgx.ErrNoRows
}

func (f *fakeList) ListAuditEntries(_ context.Context, arg dbgen.ListAuditEntriesParams) ([]dbgen.ListAuditEntriesRow,
	error) {
	f.arg = arg
	if f.err != nil {
		return nil, f.err
	}
	var out []dbgen.ListAuditEntriesRow
	for _, r := range f.rows {
		if arg.BeforeID.Valid && r.ID >= arg.BeforeID.Int64 {
			continue
		}
		out = append(out, r)
	}
	return out[:min(len(out), int(arg.PageSize))], nil
}

func txt(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

func entryRow(id int64, kind, action string) dbgen.ListAuditEntriesRow {
	return dbgen.ListAuditEntriesRow{ID: id, PublicID: fmt.Sprintf("AE%012d", id), At: t0.Add(time.Duration(id) * time.Minute),
		ActorKind: kind, Transport: "ui", Action: action, Diff: []byte(`[]`), Details: []byte(`{}`)}
}

// TestList is C-03.FR-15's reader: newest first, pages by a cursor on time and id, the filters passed on, actors
// resolved by public_id and named by their current name.
func TestList(t *testing.T) {
	user := entryRow(5, "user", "user.deleted")
	user.ActorUserPublicID, user.ActorUserName = txt("SR0000000000AA"), txt("deleted-user-SR0000000000BB")
	user.ResourceType, user.ResourcePublicID, user.ResourceName = txt("user"), txt("SR0000000000BB"), txt("x")
	user.ResourceUserName = txt("deleted-user-SR0000000000BB")
	user.Diff, user.Details = []byte(`[{"pointer":"/status","before":"active","after":"deleted"}]`), []byte(`{"n":1}`)
	sa := entryRow(4, "service_account", "a.b")
	sa.ActorServiceAccountPublicID, sa.ActorServiceAccountName, sa.TokenPublicID, sa.TokenName = txt("SA0000000000AA"),
		txt("ci"), txt("ST0000000000AA"), txt("deploy")
	cli := entryRow(3, "cli", "user.password_reset")
	cli.ActorName = txt("ops")
	f := &fakeList{rows: []dbgen.ListAuditEntriesRow{user, sa, cli, entryRow(2, "system", "a.b"),
		entryRow(1, "bootstrap", "user.created")}, users: map[string]int64{"SR0000000000AA": 11},
		sas: map[string]int64{"SA0000000000AA": 12}}
	r := NewReader(7, f)
	page, err := r.List(t.Context(), Filter{Limit: 3})
	if err != nil || len(page.Entries) != 3 || page.Next == nil || *page.Next != (Cursor{At: cli.At, ID: 3}) {
		t.Fatalf("page = %+v, %v", page, err)
	}
	if e := page.Entries[0]; e.Actor != (ListedActor{Kind: ActorUser, PublicID: "SR0000000000AA",
		Name: "deleted-user-SR0000000000BB"}) || e.ResourceID != "SR0000000000BB" ||
		e.ResourceName != "deleted-user-SR0000000000BB" || e.Diff[0].After != "deleted" ||
		e.Details["n"] != 1.0 {
		t.Errorf("user entry = %+v", e)
	}
	if e := page.Entries[1]; e.Actor.Name != "ci" || e.Actor.PublicID != "SA0000000000AA" || e.TokenPublicID != "ST0000000000AA" {
		t.Errorf("service account entry = %+v", e)
	}
	if e := page.Entries[2]; e.Actor.Name != "ops" || e.Actor.PublicID != "" {
		t.Errorf("cli entry = %+v", e)
	}
	page, err = r.List(t.Context(), Filter{Limit: 3, After: page.Next})
	if err != nil || len(page.Entries) != 2 || page.Next != nil || page.Entries[0].Actor.Name != SystemName ||
		page.Entries[1].Actor.Name != BootstrapName || len(page.Entries[1].Diff) != 0 || page.Entries[1].Details == nil {
		t.Errorf("last page = %+v, %v", page, err)
	}
	if f.arg.BeforeID.Int64 != 3 || !f.arg.BeforeAt.Time.Equal(cli.At) || f.arg.PageSize != 4 {
		t.Errorf("cursor parameters = %+v", f.arg)
	}
	from, to := t0, t0.Add(time.Hour)
	if _, err := r.List(t.Context(), Filter{From: &from, To: &to, Actor: "sr0000000000aa", Action: "a.b",
		ResourceType: "user", ResourceID: "sr00000000000o", Limit: 50}); err != nil {
		t.Fatal(err)
	}
	if a := f.arg; !a.From.Time.Equal(from) || !a.To.Time.Equal(to) || a.ActorUserID.Int64 != 11 ||
		a.ActorServiceAccountID.Valid || a.Action.String != "a.b" || a.ResourceType.String != "user" ||
		a.ResourceID.String != "SR000000000000" || a.OrgID != 7 || a.BeforeID.Valid {
		t.Errorf("filter parameters = %+v", a)
	}
	if _, err := r.List(t.Context(), Filter{Actor: "SA0000000000AA", ResourceID: "x", Limit: 50}); err != nil {
		t.Fatal(err)
	}
	if a := f.arg; a.ActorServiceAccountID.Int64 != 12 || a.ActorUserID.Valid || a.ResourceID.String != "x" {
		t.Errorf("service account parameters = %+v", a)
	}
	for _, actor := range []string{"SR0000000000ZZ", "SA0000000000ZZ", "nonsense"} {
		f.arg = dbgen.ListAuditEntriesParams{}
		page, err := r.List(t.Context(), Filter{Actor: actor, Limit: 50})
		if err != nil || len(page.Entries) != 0 || page.Entries == nil || f.arg.OrgID != 0 {
			t.Errorf("actor %s = %+v, %v", actor, page, err)
		}
	}
	if NewListQueries(nil) == nil {
		t.Error("NewListQueries")
	}
}

func TestListFailures(t *testing.T) {
	boom := errors.New("boom")
	for _, actor := range []string{"", "SR0000000000ZZ", "SA0000000000ZZ"} {
		r := NewReader(7, &fakeList{err: boom})
		if _, err := r.List(t.Context(), Filter{Actor: actor, Limit: 1}); !errors.Is(err, boom) {
			t.Errorf("actor %q: %v", actor, err)
		}
	}
	for name, row := range map[string]dbgen.ListAuditEntriesRow{
		"diff":    {Diff: []byte(`{`), Details: []byte(`{}`)},
		"details": {Diff: []byte(`[]`), Details: []byte(`[`)},
	} {
		r := NewReader(7, &fakeList{rows: []dbgen.ListAuditEntriesRow{row}})
		if _, err := r.List(t.Context(), Filter{Limit: 1}); err == nil {
			t.Errorf("a broken %s was read", name)
		}
	}
}

// TestMaskErased is C-03.FR-13 at read time: the entries about a deleted user show [erased] for the email, the name
// and the login it had, before and after, while other fields, absent values and the pseudonym stay; a user who is not
// deleted, and other resources, are shown as written.
func TestMaskErased(t *testing.T) {
	const pseudonym = "deleted-user-SR0000000000BB"
	diff := `[{"pointer":"/name","after":"Bob"},{"pointer":"/login","after":"bob"},` +
		`{"pointer":"/email","before":"bob@example.org","after":"b@example.org"},{"pointer":"/role","before":"viewer","after":"admin"},` +
		`{"pointer":"/password","secret_changed":true}]`
	row := func(status string) dbgen.ListAuditEntriesRow {
		r := entryRow(1, "system", "user.updated")
		r.ResourceType, r.ResourcePublicID, r.Diff = txt("user"), txt("SR0000000000BB"), []byte(diff)
		r.ResourceUserName, r.ResourceUserStatus = txt(pseudonym), txt(status)
		return r
	}
	deleted := row("deleted")
	deletion := entryRow(2, "user", "user.deleted")
	deletion.ResourceType, deletion.ResourcePublicID = txt("user"), txt("SR0000000000BB")
	deletion.ResourceUserName, deletion.ResourceUserStatus = txt(pseudonym), txt("deleted")
	deletion.Diff = []byte(`[{"pointer":"/status","before":"active","after":"deleted"},{"pointer":"/name","after":"` +
		pseudonym + `"}]`)
	page, err := NewReader(7, &fakeList{rows: []dbgen.ListAuditEntriesRow{deletion, deleted, row("active")}}).
		List(t.Context(), Filter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(page.Entries[1].Diff)
	want := `[{"pointer":"/name","after":"[erased]"},{"pointer":"/login","after":"[erased]"},` +
		`{"pointer":"/email","before":"[erased]","after":"[erased]"},{"pointer":"/role","before":"viewer","after":"admin"},` +
		`{"pointer":"/password","secret_changed":true}]`
	if string(b) != want {
		t.Errorf("deleted user's diff\n %s\nwant\n %s", b, want)
	}
	if b, _ := json.Marshal(page.Entries[0].Diff); !strings.Contains(string(b), `"after":"`+pseudonym+`"`) ||
		!strings.Contains(string(b), `"before":"active"`) {
		t.Errorf("the deletion's diff = %s", b)
	}
	if b, _ := json.Marshal(page.Entries[2].Diff); string(b) != diff {
		t.Errorf("an active user's diff = %s", b)
	}
	if string(deleted.Diff) != diff {
		t.Error("the row was changed")
	}
}

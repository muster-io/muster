// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/groups/dbgen"
)

func (f *fakeDB) InsertNote(_ context.Context, arg dbgen.InsertNoteParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("InsertNote"); err != nil {
		return err
	}
	id := f.id()
	if f.noteRows == nil {
		f.noteRows = map[int64]dbgen.InsertNoteParams{}
	}
	f.noteRows[id] = arg
	f.notes = append(f.notes, dbgen.ListTimelineNotesRow{ID: id, PublicID: arg.PublicID, CreatedAt: arg.CreatedAt,
		Body: arg.Body, ActorKind: arg.ActorKind, ActorUserID: arg.ActorUserID,
		ActorServiceAccountID: arg.ActorServiceAccountID, TokenName: arg.TokenName, Transport: arg.Transport})
	return nil
}

// noteRow is the notes row of the Note publicID.
func (h *harness) noteRow(t *testing.T, publicID string) dbgen.InsertNoteParams {
	t.Helper()
	for _, r := range h.db.noteRows {
		if r.PublicID == publicID {
			return r
		}
	}
	t.Fatalf("no note %s", publicID)
	return dbgen.InsertNoteParams{}
}

// TestAddNote is C-10.FR-8, C-10.FR-1, C-04.AC-6 and C-10.AC-18: a Note of 1 to alert_group.note_max_length
// characters is added through the dispatcher in any status, with its author — a User or a Service account, with the
// token it used — and Transport, as the Quiet note_added numbered among the lifecycle events; it is recorded in the
// Audit log as alert_group.note_added and changes nothing else. A Viewer is refused by the Permission step.
func TestAddNote(t *testing.T) {
	h := newHarness(t)
	h.people()
	g := h.firing(t, "x")
	h.clock.Advance(time.Minute)
	entries, seq, status := len(h.db.entries), g.EventSeq, g.Status
	n, err := h.svc.AddNote(t.Context(), robot, g.PublicID, "Restarted the disk daemon.")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(n.PublicID, "NE") || n.Body != "Restarted the disk daemon." || n.Author == nil ||
		n.Author.Kind != "service_account" || n.Author.Name != "robot" || n.Transport != "api" ||
		!n.CreatedAt.Equal(h.clock.Now()) {
		t.Errorf("note = %+v %+v", n, n.Author)
	}
	row := h.noteRow(t, n.PublicID)
	if row.EventSeq != seq+1 || g.EventSeq != seq+1 || row.ActorKind != "service_account" ||
		row.ActorServiceAccountID != i8(robotID) || row.ActorUserID.Valid || row.ApiTokenID != i8(32) ||
		row.TokenName != txt("ci") || row.Transport != "api" || row.AlertGroupID != g.ID {
		t.Errorf("notes row %+v, alert group at %d", row, g.EventSeq)
	}
	if len(h.db.entries) != entries || g.Status != status || !g.LastChangedAt.Equal(h.clock.Now()) {
		t.Errorf("a note wrote %d timeline entries, status %s", len(h.db.entries)-entries, g.Status)
	}
	if a := h.db.audit[len(h.db.audit)-1]; a.Action != ActionNoteAdded || a.ResourcePublicID != txt(g.PublicID) ||
		!strings.Contains(string(a.Details), n.PublicID) || a.Transport != "api" {
		t.Errorf("audit %+v %s", a, a.Details)
	}
	if !strings.Contains(h.log.String(), `"event":"command_executed","command":"add_note","group":"`+g.PublicID) {
		t.Errorf("log %s", h.log)
	}

	// Any status: acknowledged, snoozed, resolved by a person and by the system.
	for name, set := range map[string]func(){
		"acknowledged": func() { h.acknowledge(g) },
		"snoozed":      func() { h.snooze(g, h.clock.Now().Add(time.Hour), false) },
		"resolved":     func() { h.personResolve(g, time.Minute) },
	} {
		set()
		if _, err := h.svc.AddNote(t.Context(), bob, g.PublicID, name); err != nil {
			t.Errorf("a note while %s = %v", name, err)
		}
	}

	// The length limit counts characters.
	if _, err := h.svc.AddNote(t.Context(), alice, g.PublicID, strings.Repeat("я", NoteMaxLength)); err != nil {
		t.Errorf("a note of %d characters = %v", NoteMaxLength, err)
	}
	for _, c := range []struct{ body, want string }{
		{strings.Repeat("x", NoteMaxLength+1), "/body too_long"},
		{"", "/body required"},
		{" \n\t", "/body required"},
	} {
		if _, err := h.svc.AddNote(t.Context(), alice, g.PublicID, c.body); code(err) != c.want {
			t.Errorf("a note of %d characters = %v, want %s", len(c.body), err, c.want)
		}
	}

	// A Viewer is refused before anything is read; an unknown Alert Group is not found.
	locks, notes := h.db.calls["LockGroups"], len(h.db.noteRows)
	_, err = h.svc.AddNote(t.Context(), carol, g.PublicID, "x")
	if f, ok := errors.AsType[*ForbiddenError](err); !ok || f.Permission != PermissionNote {
		t.Errorf("a viewer's note = %v", err)
	}
	if h.db.calls["LockGroups"] != locks || len(h.db.noteRows) != notes {
		t.Error("a refused note read or wrote")
	}
	if !strings.Contains(h.log.String(), `"event":"command_refused","command":"add_note","group":"`+g.PublicID+
		`","actor":"SRAAAAAAAAAAB1","transport":"ui","code":"forbidden"`) {
		t.Errorf("log %s", h.log)
	}
	if _, err := h.svc.AddNote(t.Context(), alice, "AGZZZZZZZZZZZZ", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a note on an unknown alert group = %v", err)
	}
	// The author cannot be named: the Note is added, the answer fails.
	h.db.fail["ListUserRefs"] = errBoom
	if _, err := h.svc.AddNote(t.Context(), alice, g.PublicID, "x"); !errors.Is(err, errBoom) {
		t.Errorf("a note whose author cannot be read = %v", err)
	}
	delete(h.db.fail, "ListUserRefs")
	h.db.fail["InsertNote"] = errBoom
	if _, err := h.svc.AddNote(t.Context(), alice, g.PublicID, "x"); !errors.Is(err, errBoom) {
		t.Errorf("a failing insert = %v", err)
	}
}

// TestNoteAfterDetailsRemoved is C-10.FR-8 and C-09.FR-16: a Note is added to an Alert Group whose details were
// removed, and the Notes and the Timeline, which then shows only the Notes, list it.
func TestNoteAfterDetailsRemoved(t *testing.T) {
	h := newHarness(t)
	h.people()
	g := h.firing(t, "x")
	h.personResolve(g, time.Minute)
	h.clock.Advance(91 * 24 * time.Hour)
	n, err := h.svc.AddNote(t.Context(), alice, g.PublicID, "Post-mortem filed.")
	if err != nil {
		t.Fatal(err)
	}
	page, err := h.svc.Notes(t.Context(), g.PublicID, nil, 10)
	if err != nil || len(page.Notes) != 1 || page.Notes[0].PublicID != n.PublicID || page.Notes[0].Author == nil ||
		page.Notes[0].Author.Name != "Alice" || page.Next != nil {
		t.Errorf("notes = %v %+v", err, page)
	}
	tl, err := h.svc.Timeline(t.Context(), g.PublicID, TimelineFilter{Limit: 10})
	if err != nil || len(tl.Entries) != 1 || tl.Entries[0].Kind != KindNotes || tl.Entries[0].Event != "note_added" {
		t.Errorf("timeline = %v %+v", err, tl.Entries)
	}
}

// TestResolveWithNote is C-10.FR-1, C-10.FR-15 and C-10.AC-16: Resolve with a Note records resolved and then
// note_added in one transaction, each with its Audit log entry; the Note needs alert-groups:note, also in a bulk
// Resolve, and a refusal adds no Note.
func TestResolveWithNote(t *testing.T) {
	h := newHarness(t)
	h.people()
	g := h.firing(t, "x")
	h.clock.Advance(time.Minute)
	audits := len(h.db.audit)
	res, err := h.svc.Resolve(t.Context(), bobToken, g.PublicID, ptr("Rolled back."))
	if err != nil || res.Group.Status != StatusResolved {
		t.Fatalf("resolve = %v %+v", err, res.Group)
	}
	resolved := h.last(t, g)
	var note dbgen.InsertNoteParams
	for _, r := range h.db.noteRows {
		note = r
	}
	if resolved.Event.String != "resolved" || note.Body != "Rolled back." || note.EventSeq != resolved.EventSeq.Int64+1 ||
		!note.CreatedAt.Equal(resolved.At) || note.ActorUserID != i8(bobID) || note.TokenName != txt("script") {
		t.Errorf("resolved %+v, note %+v", resolved.InsertTimelineEntryParams, note)
	}
	var actions []string
	for _, a := range h.db.audit[audits:] {
		actions = append(actions, a.Action)
	}
	if !slices.Equal(actions, []string{ActionResolved, ActionNoteAdded}) {
		t.Errorf("audit %v", actions)
	}
	tl, err := h.svc.Timeline(t.Context(), g.PublicID, TimelineFilter{Limit: 2})
	if err != nil || len(tl.Entries) != 2 || tl.Entries[0].Event != "note_added" || tl.Entries[1].Event != "resolved" ||
		tl.Entries[0].Loudness != Quiet || len(tl.Entries[0].Mentions) != 0 {
		t.Errorf("timeline = %v %+v", err, tl.Entries)
	}

	// Resolve without alert-groups:note may resolve, but not with a Note.
	resolver := Caller{Actor: carol.Actor, Transport: carol.Transport,
		Permissions: []auth.Permission{PermissionResolve}}
	o := h.firing(t, "y")
	notes := len(h.db.noteRows)
	_, err = h.svc.Resolve(t.Context(), resolver, o.PublicID, ptr("x"))
	if f, ok := errors.AsType[*ForbiddenError](err); !ok || f.Permission != PermissionNote || o.Status != "firing" ||
		len(h.db.noteRows) != notes {
		t.Errorf("resolve with a note without the permission = %v, %s", err, o.Status)
	}
	if _, err := h.svc.Bulk(t.Context(), resolver, BulkRequest{Command: CommandResolve, IDs: []string{o.PublicID},
		Note: ptr("x")}); code(err) != CodeForbidden {
		t.Errorf("bulk resolve with a note without the permission = %v", err)
	}
	// A bulk Resolve with a Note adds it to each Alert Group it resolves; a refused one gets none.
	items, err := h.svc.Bulk(t.Context(), alice, BulkRequest{Command: CommandResolve,
		IDs: []string{o.PublicID, g.PublicID}, Note: ptr("Deploy reverted.")})
	if err != nil || len(items) != 2 || items[0].Outcome != BulkDone || items[1].Outcome != BulkRefused ||
		len(h.db.noteRows) != notes+1 {
		t.Errorf("bulk resolve with a note = %v %+v, %d notes", err, items, len(h.db.noteRows)-notes)
	}
	if _, err := h.svc.Bulk(t.Context(), alice, BulkRequest{Command: CommandResolve, IDs: []string{o.PublicID},
		Note: ptr(strings.Repeat("x", NoteMaxLength+1))}); code(err) != "/note too_long" {
		t.Errorf("bulk resolve with a note too long = %v", err)
	}
	// A Note that fails to be written fails the Resolve.
	p := h.firing(t, "z")
	h.db.fail["InsertNote"] = errBoom
	if _, err := h.svc.Resolve(t.Context(), alice, p.PublicID, ptr("x")); !errors.Is(err, errBoom) {
		t.Errorf("resolve with a failing note = %v", err)
	}
}

// TestNotes is listAlertGroupNotes (C-10.FR-8): the Notes of one Alert Group in the order they were added, by page,
// each with its author; a deleted author is deactivated (C-03.FR-13).
func TestNotes(t *testing.T) {
	h := newHarness(t)
	h.people()
	g, other := h.firing(t, "x"), h.firing(t, "y")
	for i, c := range []Caller{alice, robot, bob} {
		h.clock.Advance(time.Minute)
		if _, err := h.svc.AddNote(t.Context(), c, g.PublicID, strings.Repeat("n", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.svc.AddNote(t.Context(), alice, other.PublicID, "elsewhere"); err != nil {
		t.Fatal(err)
	}
	bobRow := h.db.users[bobID]
	bobRow.Status, bobRow.Name, bobRow.Login = "deleted", "deleted-user-SRAAAAAAAAAAB0", "deleted-user-SRAAAAAAAAAAB0"
	h.db.users[bobID] = bobRow
	first, err := h.svc.Notes(t.Context(), g.PublicID, nil, 2)
	if err != nil || len(first.Notes) != 2 || first.Next == nil || first.Notes[0].Body != "n" ||
		first.Notes[0].Author.Name != "Alice" || first.Notes[1].Author.Kind != "service_account" {
		t.Fatalf("first page = %v %+v", err, first)
	}
	second, err := h.svc.Notes(t.Context(), g.PublicID, first.Next, 2)
	if err != nil || len(second.Notes) != 1 || second.Next != nil || second.Notes[0].Body != "nnn" ||
		!second.Notes[0].Author.Deactivated || second.Notes[0].Author.Name != "deleted-user-SRAAAAAAAAAAB0" {
		t.Fatalf("second page = %v %+v", err, second)
	}
	if _, err := h.svc.Notes(t.Context(), "AGZZZZZZZZZZZZ", nil, 2); !errors.Is(err, ErrNotFound) {
		t.Errorf("notes of an unknown alert group = %v", err)
	}
	for _, q := range []string{"ListTimelineNotes", "ListUserRefs"} {
		h.db.fail[q] = errBoom
		if _, err := h.svc.Notes(t.Context(), g.PublicID, nil, 2); !errors.Is(err, errBoom) {
			t.Errorf("%s failing = %v", q, err)
		}
		delete(h.db.fail, q)
	}
}

// TestNoteBySystem: only a User or a Service account writes a Note.
func TestNoteBySystem(t *testing.T) {
	h := newHarness(t)
	g := h.firing(t, "x")
	system := Caller{Actor: audit.System, Transport: audit.TransportSystem, Permissions: []auth.Permission{PermissionNote}}
	if _, err := h.svc.AddNote(t.Context(), system, g.PublicID, "x"); err == nil ||
		!strings.Contains(err.Error(), "only a user or a service account") || len(h.db.noteRows) != 0 {
		t.Errorf("a note by the system = %v", err)
	}
}

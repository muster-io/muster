// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/groups"
)

// fakeCommands stands for the command layer of internal/groups: it records each Command with its caller and answers
// err, or the Alert Group of the fake of the reads.
type fakeCommands struct {
	view    groups.View
	calls   []string
	callers []groups.Caller
	ends    []groups.SnoozeEnd
	bulks   []groups.BulkRequest
	items   []groups.BulkItem
	notes   []string
	err     error
}

func (f *fakeCommands) record(name string, c groups.Caller, id string) (groups.Result, error) {
	f.calls, f.callers = append(f.calls, name+" "+id), append(f.callers, c)
	if f.err != nil {
		return groups.Result{}, f.err
	}
	if !slices.Contains(c.Permissions, groups.Command(name).Permission()) {
		return groups.Result{}, &groups.ForbiddenError{Permission: groups.Command(name).Permission()}
	}
	return groups.Result{Outcome: groups.OutcomeDone, Group: f.view}, nil
}

func (f *fakeCommands) Acknowledge(_ context.Context, c groups.Caller, id string) (groups.Result, error) {
	return f.record("acknowledge", c, id)
}

func (f *fakeCommands) Unacknowledge(_ context.Context, c groups.Caller, id string) (groups.Result, error) {
	return f.record("unacknowledge", c, id)
}

func (f *fakeCommands) Resolve(_ context.Context, c groups.Caller, id string, note *string) (groups.Result, error) {
	if note != nil {
		f.notes = append(f.notes, *note)
	}
	return f.record("resolve", c, id)
}

func (f *fakeCommands) Unresolve(_ context.Context, c groups.Caller, id string) (groups.Result, error) {
	return f.record("unresolve", c, id)
}

func (f *fakeCommands) Snooze(_ context.Context, c groups.Caller, id string, end groups.SnoozeEnd) (groups.Result,
	error) {
	f.ends = append(f.ends, end)
	if (end.Until == nil) != end.NoEnd {
		return groups.Result{}, &groups.FieldError{Pointer: "", Code: groups.CodeOneOfRequired, Detail: "x"}
	}
	return f.record("snooze", c, id)
}

func (f *fakeCommands) Unsnooze(_ context.Context, c groups.Caller, id string) (groups.Result, error) {
	return f.record("unsnooze", c, id)
}

func (f *fakeCommands) AddNote(_ context.Context, c groups.Caller, id, body string) (groups.NoteView, error) {
	f.notes = append(f.notes, body)
	if _, err := f.record("add_note", c, id); err != nil {
		return groups.NoteView{}, err
	}
	return groups.NoteView{PublicID: "NEAAAAAAAAAAAA", Body: body, Transport: string(c.Transport), CreatedAt: t0,
		Author: &groups.ActorRef{Kind: "service_account", PublicID: "SAAAAAAAAAAAAA", Name: "robot"}}, nil
}

func (f *fakeCommands) Bulk(_ context.Context, c groups.Caller, r groups.BulkRequest) ([]groups.BulkItem, error) {
	f.bulks, f.callers = append(f.bulks, r), append(f.callers, c)
	return f.items, f.err
}

func newCommandsAPI(t *testing.T) (*testAPI, *fakeCommands) {
	t.Helper()
	x, fg, _ := newAlertGroupsAPI(t)
	view := fg.view
	view.Status, view.Resolution, view.Notices = groups.StatusAcknowledged, nil, nil
	view.Owner = &groups.ActorRef{Kind: "user", PublicID: "SRAAAAAAAAAAAA", Name: "admin", Login: "admin@example.org"}
	fc := &fakeCommands{view: view}
	x.srv.commands = fc
	return x, fc
}

// TestCommandsAPI is C-10.FR-3 and C-10.AC-5 on the API: each Command operation hands the dispatcher the caller —
// the web session's User with the Transport ui, a token with api, its Permissions and the client address — and
// answers the outcome with the Alert Group.
func TestCommandsAPI(t *testing.T) {
	x, fc := newCommandsAPI(t)
	for _, cmd := range []string{"acknowledge", "unacknowledge", "resolve", "unresolve", "unsnooze"} {
		a := x.as(t, fullToken, http.MethodPost, "/api/v1/alert-groups/"+groupID+"/"+cmd, "")
		var out gen.CommandResult
		decodeInto(t, a, &out)
		if a.status != http.StatusOK || out.Outcome != gen.CommandOutcomeDone || out.AlertGroup.Id != groupID ||
			out.AlertGroup.Owner == nil || out.AlertGroup.Owner.Name != "admin" {
			t.Errorf("%s = %d %s", cmd, a.status, a.body)
		}
	}
	a := x.as(t, fullToken, http.MethodPost, "/api/v1/alert-groups/"+groupID+"/snooze", `{"no_end":true}`)
	if a.status != http.StatusOK || !fc.ends[0].NoEnd || fc.ends[0].Until != nil {
		t.Errorf("snooze = %d %s %+v", a.status, a.body, fc.ends)
	}
	c := fc.callers[0]
	if c.Actor.Kind != audit.ActorUser || c.Actor.TokenName != "full" || c.Transport != audit.TransportAPI ||
		!c.Address.IsValid() || len(fc.calls) != 6 || fc.calls[0] != "acknowledge "+groupID {
		t.Errorf("caller %+v, calls %v", c, fc.calls)
	}
	a = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/alert-groups/"+groupID+"/acknowledge", "")
	if a.status != http.StatusOK || fc.callers[len(fc.callers)-1].Transport != audit.TransportUI {
		t.Errorf("session = %d %s", a.status, a.body)
	}
	a = x.as(t, serviceToken, http.MethodPost, "/api/v1/alert-groups/"+groupID+"/resolve", `{}`)
	if c := fc.callers[len(fc.callers)-1]; a.status != http.StatusOK || c.Actor.Kind != audit.ActorServiceAccount ||
		c.Actor.TokenName != "ci" {
		t.Errorf("service account = %d %+v", a.status, c)
	}
	// The Snooze names its choice; Resolve takes a Note.
	if a := x.as(t, fullToken, http.MethodPost, "/api/v1/alert-groups/"+groupID+"/snooze", `{}`); a.status !=
		http.StatusUnprocessableEntity || !strings.Contains(string(a.body), `"one_of_required"`) {
		t.Errorf("snooze {} = %d %s", a.status, a.body)
	}
	until := t0.Add(time.Hour).Format(time.RFC3339)
	if a := x.as(t, fullToken, http.MethodPost, "/api/v1/alert-groups/"+groupID+"/snooze",
		`{"until":"`+until+`"}`); a.status != http.StatusOK || fc.ends[len(fc.ends)-1].Until == nil {
		t.Errorf("snooze until = %d %s", a.status, a.body)
	}
	if a := x.as(t, fullToken, http.MethodPost, "/api/v1/alert-groups/"+groupID+"/resolve", `{"note":"rolled back"}`); a.status !=
		http.StatusOK || len(fc.notes) != 1 || fc.notes[0] != "rolled back" {
		t.Errorf("resolve with a note = %d %s", a.status, a.body)
	}
	if a := x.as(t, "mstr_pat_old", http.MethodPost, "/api/v1/alert-groups/"+groupID+"/acknowledge", ""); a.status !=
		http.StatusUnauthorized {
		t.Errorf("without credentials = %d", a.status)
	}
}

// TestCommandProblems is C-10.FR-2 and C-10.AC-4 on the API: a refusal is 409 command-refused with its code, message
// and the newer Alert Group; a missing Permission, which the dispatcher reports, is the 403 the middleware answers.
func TestCommandProblems(t *testing.T) {
	x, fc := newCommandsAPI(t)
	fc.err = &groups.RefusedError{Code: groups.CodeNewerGroupExists, Message: "a newer open Alert Group #415 exists",
		Related: &groups.GroupRef{PublicID: "AGCCCCCCCCCCCC", Number: 415}}
	a := x.as(t, fullToken, http.MethodPost, "/api/v1/alert-groups/"+groupID+"/unresolve", "")
	var p gen.Problem
	decodeInto(t, a, &p)
	if a.status != http.StatusConflict || p.Type != problemBase+"command-refused" || p.Title != "Command refused" ||
		*p.Code != "newer_alert_group_exists" || *p.Detail != "A newer open Alert Group #415 exists." ||
		p.RelatedAlertGroup == nil || p.RelatedAlertGroup.Number != 415 || p.RelatedAlertGroup.Id != "AGCCCCCCCCCCCC" {
		t.Errorf("refusal = %d %s", a.status, a.body)
	}
	fc.err = &groups.RefusedError{Code: groups.CodeAlreadyResolved, Message: "already resolved"}
	var resolved gen.Problem
	decodeInto(t, x.as(t, fullToken, http.MethodPost, "/api/v1/alert-groups/"+groupID+"/acknowledge", ""), &resolved)
	if *resolved.Code != "already_resolved" || resolved.RelatedAlertGroup != nil || *resolved.Detail != "Already resolved." {
		t.Errorf("already resolved = %+v", resolved)
	}
	fc.err = nil
	// A Viewer's Command reaches the dispatcher, which refuses it with the Permission.
	calls := len(fc.calls)
	a = x.mutate(t, viewerCookie, http.MethodPost, "/api/v1/alert-groups/"+groupID+"/acknowledge", "")
	if a.status != http.StatusForbidden || len(fc.calls) != calls+1 || a.json(t)["type"] != problemBase+"forbidden" ||
		a.json(t)["detail"] != "This needs the Permission alert-groups:acknowledge." {
		t.Errorf("viewer = %d %s", a.status, a.body)
	}
	fc.err = groups.ErrNotFound
	if a := x.as(t, fullToken, http.MethodPost, "/api/v1/alert-groups/"+groupID+"/unsnooze", ""); a.status !=
		http.StatusNotFound {
		t.Errorf("not found = %d", a.status)
	}
	// Each Command handler returns the error of the dispatcher.
	fc.err = errBoom
	for _, cmd := range []string{"acknowledge", "unacknowledge", "resolve", "unresolve", "unsnooze"} {
		if a := x.as(t, fullToken, http.MethodPost, "/api/v1/alert-groups/"+groupID+"/"+cmd, ""); a.status !=
			http.StatusInternalServerError {
			t.Errorf("%s failing = %d", cmd, a.status)
		}
	}
	if a := x.as(t, fullToken, http.MethodPost, "/api/v1/alert-groups/"+groupID+"/snooze", `{"no_end":true}`); a.status !=
		http.StatusInternalServerError {
		t.Errorf("snooze failing = %d", a.status)
	}
	if a := x.as(t, fullToken, http.MethodPost, "/api/v1/alert-groups/bulk-commands",
		`{"command":"unsnooze","alert_group_ids":["`+groupID+`"]}`); a.status != http.StatusInternalServerError {
		t.Errorf("bulk failing = %d", a.status)
	}
}

// TestBulkCommandAPI is runBulkCommand (C-10.FR-14): the command, the ids and the Snooze reach the command layer, and
// each outcome comes back in request order with its #N, code and message, null where there is none.
func TestBulkCommandAPI(t *testing.T) {
	x, fc := newCommandsAPI(t)
	n := int64(412)
	fc.items = []groups.BulkItem{{PublicID: groupID, Number: &n, Outcome: groups.BulkDone},
		{PublicID: "AGBBBBBBBBBBBB", Number: &n, Outcome: groups.BulkSkipped, Code: groups.CodeOwnedByOther,
			Message: "skipped: owned by Alice"},
		{PublicID: "AGZZZZZZZZZZZZ", Outcome: groups.BulkFailed, Code: groups.CodeNotFound, Message: "not found"}}
	a := x.as(t, fullToken, http.MethodPost, "/api/v1/alert-groups/bulk-commands",
		`{"command":"snooze","alert_group_ids":["`+groupID+`","AGBBBBBBBBBBBB","AGZZZZZZZZZZZZ"],"snooze":{"no_end":true}}`)
	var out gen.BulkCommandResult
	decodeInto(t, a, &out)
	if a.status != http.StatusOK || len(out.Results) != 3 || out.Results[0].Outcome != gen.BulkOutcomeDone ||
		!out.Results[0].Code.IsNull() || out.Results[0].Number.MustGet() != 412 ||
		out.Results[1].Message.MustGet() != "skipped: owned by Alice" || !out.Results[2].Number.IsNull() ||
		out.Results[2].Code.MustGet() != "not_found" {
		t.Fatalf("bulk = %d %s", a.status, a.body)
	}
	r := fc.bulks[0]
	if r.Command != groups.CommandSnooze || len(r.IDs) != 3 || r.Snooze == nil || !r.Snooze.NoEnd {
		t.Errorf("request %+v", r)
	}
	if a := x.as(t, fullToken, http.MethodPost, "/api/v1/alert-groups/bulk-commands",
		`{"command":"resolve","alert_group_ids":["`+groupID+`"],"note":"fixed"}`); a.status != http.StatusOK ||
		fc.bulks[1].Note == nil || *fc.bulks[1].Note != "fixed" {
		t.Errorf("bulk resolve with a note = %d %s", a.status, a.body)
	}
	if a := x.as(t, fullToken, http.MethodPost, "/api/v1/alert-groups/bulk-commands",
		`{"command":"resolve","alert_group_ids":[]}`); a.status != http.StatusBadRequest {
		t.Errorf("no ids = %d %s", a.status, a.body)
	}
	// The Permission of the chosen command is the dispatcher's: a Viewer reaches it.
	fc.err = &groups.ForbiddenError{Permission: groups.PermissionResolve}
	a = x.mutate(t, viewerCookie, http.MethodPost, "/api/v1/alert-groups/bulk-commands",
		`{"command":"resolve","alert_group_ids":["`+groupID+`"]}`)
	if a.status != http.StatusForbidden || len(fc.bulks) != 3 ||
		a.json(t)["detail"] != "This needs the Permission alert-groups:resolve." {
		t.Errorf("viewer = %d %s", a.status, a.body)
	}
}

// TestAlertGroupCommandFields is C-09.FR-1 and C-10.FR-16 on the reads: the Owner, the Snooze and who set it, the
// allowed Commands of the caller and the notice of a newer Alert Group.
func TestAlertGroupCommandFields(t *testing.T) {
	x, fg, _ := newAlertGroupsAPI(t)
	until := t0.Add(time.Hour)
	fg.view.Status, fg.view.Resolution = groups.StatusSnoozed, nil
	fg.view.SnoozeUntil = &until
	fg.view.SnoozedBy = &groups.ActorRef{Kind: "service_account", PublicID: saPublicID, Name: "bot"}
	fg.view.Notices = append(fg.view.Notices, groups.Notice{Kind: groups.NoticeNewerAlertGroupExists,
		Related: &groups.GroupRef{PublicID: "AGCCCCCCCCCCCC", Number: 415}})
	var g gen.AlertGroup
	decodeInto(t, x.as(t, fullToken, http.MethodGet, "/api/v1/alert-groups/"+groupID, ""), &g)
	if !g.SnoozeUntil.MustGet().Equal(until) || g.SnoozedBy == nil || g.SnoozedBy.Name != "bot" ||
		!slices.Equal(g.AllowedCommands, []gen.CommandName{gen.CommandNameAcknowledge, gen.CommandNameResolve,
			gen.CommandNameSnooze, gen.CommandNameUnsnooze}) || (*g.Notices)[3].RelatedAlertGroup.Number != 415 {
		t.Errorf("snoozed = %+v", g)
	}
	fg.view.SnoozeUntil = nil
	a := x.as(t, fullToken, http.MethodGet, "/api/v1/alert-groups/"+groupID, "")
	if !strings.Contains(string(a.body), `"snooze_until":null`) {
		t.Errorf("no end = %s", a.body)
	}
	// The allowed Commands follow the caller: a Responder's session.
	x.sessions.sessions["responder-cookie"] = session(5, user(5, "SRCCCCCCCCCCCC", "rita", "responder"), "active")
	x.sessions.cookies[5] = "responder-cookie"
	fg.view.Status, fg.view.SnoozedBy = groups.StatusFiring, nil
	var list gen.AlertGroupList
	decodeInto(t, x.call(t, http.MethodGet, "/api/v1/alert-groups", "", "Cookie", "responder-cookie"), &list)
	if len(list.Items) != 1 || len(list.Items[0].AllowedCommands) != 1 ||
		list.Items[0].AllowedCommands[0] != gen.CommandNameAcknowledge || list.Items[0].SnoozeUntil.IsSpecified() {
		t.Errorf("list for a responder = %+v", list.Items)
	}
}

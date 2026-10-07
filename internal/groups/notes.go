// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/publicid"
)

// NoteMaxLength is alert_group.note_max_length: the most characters of a Note (P-15).
const NoteMaxLength = 4000

// CodeTooLong is the field code of a Note longer than NoteMaxLength.
const CodeTooLong = "too_long"

// noteEntry is a Note that a note_added writes: its public_id and text, and the time the dispatcher added it.
type noteEntry struct {
	PublicID  string
	Body      string
	CreatedAt time.Time
}

// checkNote refuses a Note that is empty, only white space, or longer than NoteMaxLength characters; pointer is where
// it is in the request.
func checkNote(pointer, body string) error {
	if strings.TrimSpace(body) == "" {
		return &FieldError{Pointer: pointer, Code: CodeRequired, Detail: "The Note is empty."}
	}
	if utf8.RuneCountInString(body) > NoteMaxLength {
		return &FieldError{Pointer: pointer, Code: CodeTooLong,
			Detail: fmt.Sprintf("A Note has at most %d characters.", NoteMaxLength)}
	}
	return nil
}

// addNote is Add Note (C-10.FR-1, FR-8): allowed in every status, also once the details of the Alert Group were
// removed; it records a Quiet note_added without Mentions and changes nothing else.
type addNote struct {
	note *noteEntry
}

func (addNote) precondition(*Group) error { return nil }

func (t addNote) apply(_ context.Context, _ Queries, _ *Group, c *change) error {
	c.action, c.details = ActionNoteAdded, map[string]any{"note": t.note.PublicID}
	c.add(entry{Event: EventNoteAdded, Note: t.note})
	return nil
}

// newNote is a Note to add with its new public_id.
func newNote(body string) *noteEntry {
	return &noteEntry{PublicID: publicid.New(publicid.Note), Body: body}
}

// AddNote is the Command Add Note of the Alert Group publicID by the caller (C-10.FR-8): through the dispatcher, with
// the Permission alert-groups:note, 1 to NoteMaxLength characters, in any status. It returns the Note with its author,
// a User or a Service account, and its Transport.
func (s *Service) AddNote(ctx context.Context, c Caller, publicID, body string) (NoteView, error) {
	n := newNote(body)
	if _, _, err := s.run(ctx, c, CommandAddNote, publicID, args{note: &body, noteAt: "/body", added: n}); err != nil {
		return NoteView{}, err
	}
	user, account := c.person()
	refs, err := s.refs(ctx, []int64{deref(user)}, []int64{deref(account)})
	if err != nil {
		return NoteView{}, err
	}
	return NoteView{PublicID: n.PublicID, Body: n.Body, Author: refs.actor(nullInt(user), nullInt(account)),
		Transport: string(c.Transport), CreatedAt: n.CreatedAt}, nil
}

// NotePosition is the place of a Note in the Notes of an Alert Group: the time it was added and its id.
type NotePosition struct {
	At time.Time
	ID int64
}

// NotePage is a page of Notes; Next is nil on the last page.
type NotePage struct {
	Notes []NoteView
	Next  *NotePosition
}

// Notes lists the Notes of the Alert Group publicID in the order they were added, after the position when given
// (C-10.FR-8), each with its author and Transport. Notes are kept with the summary row, so they are listed also once
// the details were removed (C-09.FR-16).
func (s *Service) Notes(ctx context.Context, publicID string, after *NotePosition, limit int) (NotePage, error) {
	gid, _, err := s.groupID(ctx, publicID)
	if err != nil {
		return NotePage{}, err
	}
	at, id := pgtype.Timestamptz{}, int64(0)
	if after != nil {
		at, id = pgtype.Timestamptz{Time: after.At, Valid: true}, after.ID
	}
	entries, err := s.notes(ctx, gid, false, at, sourceNotes, id, int32(limit+1)) //nolint:gosec // G115: limit is at most the page size
	if err != nil {
		return NotePage{}, err
	}
	out := NotePage{Notes: make([]NoteView, 0, min(len(entries), limit))}
	if len(entries) > limit {
		entries = entries[:limit]
		last := entries[limit-1].position
		out.Next = &NotePosition{At: last.At, ID: last.ID}
	}
	if err := s.actors(ctx, entries); err != nil {
		return NotePage{}, err
	}
	for _, e := range entries {
		out.Notes = append(out.Notes, *e.Note)
	}
	return out, nil
}

func deref(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

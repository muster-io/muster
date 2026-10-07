// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"context"
	"errors"
	"fmt"

	"github.com/muster-io/muster/internal/groups/dbgen"
)

// BulkMax is alert_group.bulk_max: the most Alert Groups of one bulk command.
const BulkMax = 100

// BulkOutcome is the outcome of a bulk command for one Alert Group (BulkOutcome).
type BulkOutcome string

// The outcomes of a bulk command per Alert Group.
const (
	BulkDone      BulkOutcome = "done"
	BulkUnchanged BulkOutcome = "unchanged"
	BulkRefused   BulkOutcome = "refused"
	BulkSkipped   BulkOutcome = "skipped"
	BulkFailed    BulkOutcome = "failed"
)

// BulkRequest is a bulk command (C-10.FR-14): Acknowledge, Resolve, Snooze or Unsnooze on the Alert Groups IDs, with
// the end of a Snooze, or the Note of a Resolve.
type BulkRequest struct {
	Command Command
	IDs     []string
	Snooze  *SnoozeEnd
	Note    *string
}

// BulkItem is the result of a bulk command for one Alert Group: its #N when it exists, the outcome and, for refused,
// skipped and failed, a code and a message.
type BulkItem struct {
	PublicID string
	Number   *int64
	Outcome  BulkOutcome
	Code     string
	Message  string
}

// Bulk runs a bulk command (C-10.FR-14): the caller needs the Permission of the command, checked once before the
// arguments and any Alert Group; then each Alert Group, in the order of the request, goes through the dispatcher in
// a transaction of its own, with its own Audit log and Timeline entries, and a refusal of one never stops the others. A bulk Acknowledge
// skips an Alert Group another user owns, so it never makes a Takeover; an id that names no Alert Group fails with
// not found.
func (s *Service) Bulk(ctx context.Context, c Caller, r BulkRequest) ([]BulkItem, error) {
	switch r.Command {
	case CommandAcknowledge, CommandResolve, CommandSnooze, CommandUnsnooze:
	case CommandUnacknowledge, CommandUnresolve, CommandAddNote:
		return nil, &FieldError{Pointer: "/command", Code: CodeInvalid, Detail: "This command has no bulk form."}
	default:
		return nil, &FieldError{Pointer: "/command", Code: CodeInvalid, Detail: "No such command."}
	}
	if err := s.permit(ctx, c, r.Command, "", len(r.IDs)); err != nil {
		return nil, err
	}
	if err := s.permitNote(ctx, c, r.Command, args{note: r.Note}, "", len(r.IDs)); err != nil {
		return nil, err
	}
	if len(r.IDs) == 0 || len(r.IDs) > BulkMax {
		return nil, &FieldError{Pointer: "/alert_group_ids", Code: CodeOutOfRange,
			Detail: fmt.Sprintf("Give from 1 to %d Alert Groups.", BulkMax)}
	}
	a := args{bulk: true, checked: true}
	switch r.Command {
	case CommandSnooze:
		if r.Snooze == nil {
			return nil, &FieldError{Pointer: "/snooze", Code: CodeRequired, Detail: "A bulk Snooze needs its end."}
		}
		a.end = r.Snooze
	case CommandResolve:
		a.note = r.Note
	case CommandAcknowledge, CommandUnacknowledge, CommandUnresolve, CommandUnsnooze, CommandAddNote:
	}
	if err := a.check("/snooze", s.clock.Now()); err != nil {
		return nil, err
	}
	out := make([]BulkItem, 0, len(r.IDs))
	for _, id := range r.IDs {
		outcome, number, err := s.run(ctx, c, r.Command, id, a)
		item := BulkItem{PublicID: id, Outcome: BulkOutcome(outcome)}
		if number != 0 {
			item.Number = ptr(number)
		}
		if err != nil {
			if err := s.bulkFailure(ctx, &item, err); err != nil {
				return nil, err
			}
		}
		out = append(out, item)
	}
	return out, nil
}

// bulkFailure fills in the outcome of an Alert Group that a bulk command did not change; an error it does not know
// fails the request.
func (s *Service) bulkFailure(ctx context.Context, item *BulkItem, err error) error {
	if r, ok := errors.AsType[*RefusedError](err); ok {
		item.Outcome, item.Code, item.Message = BulkRefused, r.Code, r.Message
		return nil
	}
	if sk, ok := errors.AsType[*skippedError](err); ok {
		name, err := s.userName(ctx, sk.owner)
		if err != nil {
			return err
		}
		item.Outcome, item.Code, item.Message = BulkSkipped, CodeOwnedByOther, "skipped: owned by "+name
		return nil
	}
	if errors.Is(err, ErrNotFound) {
		item.Outcome, item.Code, item.Message = BulkFailed, CodeNotFound, "not found"
		return nil
	}
	return err
}

// userName is the name of a User, "another user" for one without a row.
func (s *Service) userName(ctx context.Context, id int64) (string, error) {
	rows, err := s.store.ListUserRefs(ctx, dbgen.ListUserRefsParams{OrgID: s.orgID, Ids: []int64{id}})
	if err != nil {
		return "", fmt.Errorf("read the owner: %w", err)
	}
	if len(rows) == 0 {
		return "another user", nil
	}
	return rows[0].Name, nil
}

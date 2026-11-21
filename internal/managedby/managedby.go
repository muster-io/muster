// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package managedby holds the mark managed_by of C-33.FR-11: the name of the tool that keeps an object of
// configuration as code, such as terraform, or none. It is a mark only: the domain packages store it with their
// objects, check its value with Validate and record a change of it in the Audit log like any field, and nothing else
// reads it.
package managedby

import (
	"errors"
	"regexp"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
)

// Pointer is the field in the request bodies, where a value outside the format is refused with invalid_format.
const Pointer = "/managed_by"

// format is 1 to 32 characters — lowercase letters, digits, ., _ and - — starting with a letter or a digit, the
// pattern of the field in api/openapi.yaml.
var format = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

// ErrInvalidFormat refuses a value outside the format.
var ErrInvalidFormat = errors.New("managed_by is 1 to 32 characters — lowercase letters, digits, ., _ and - — " +
	"starting with a letter or a digit")

// Detail is the detail of the invalid_format at Pointer that the domain packages answer for a value outside the format.
const Detail = "The tool that manages it is 1 to 32 characters — lowercase letters, digits, ., _ and - — starting " +
	"with a letter or a digit."

// Validate refuses a value outside the format with ErrInvalidFormat.
func Validate(s string) error {
	if !format.MatchString(s) {
		return ErrInvalidFormat
	}
	return nil
}

// Input is the field in a write: Given is false when the request leaves it out, which keeps the stored mark on an
// update and stores none on a creation, and a nil Value is null, which clears it.
type Input struct {
	Given bool
	Value *string
}

// Set is the input of the field set to value.
func Set(value string) Input {
	return Input{Given: true, Value: &value}
}

// Clear is the input of the field set to null.
var Clear = Input{Given: true}

// Check validates the value given, if any.
func (in Input) Check() error {
	if in.Given && in.Value != nil {
		return Validate(*in.Value)
	}
	return nil
}

// Apply is the mark after the write over stored, the mark before it, nil on a creation.
func (in Input) Apply(stored *string) *string {
	if !in.Given {
		return stored
	}
	return in.Value
}

// Only reports whether a write's Audit log diff changes the mark and nothing else. Such a write skips what the object's
// other fields cause, such as a re-render of the open Root messages, since the mark changes no behaviour.
func Only(changes []audit.Change) bool {
	for _, c := range changes {
		if c.Pointer != Pointer {
			return false
		}
	}
	return len(changes) > 0
}

// Text is the mark as a query parameter.
func Text(mark *string) pgtype.Text {
	if mark == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *mark, Valid: true}
}

// Of is the mark of a column.
func Of(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

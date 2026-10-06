// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/muster-io/muster/internal/api/gen"
)

// The page size of the lists (api.page_size): 50 by default; the specification caps limit at 500.
const (
	defaultPageSize = 50
	maxPageSize     = 500
)

const fieldInvalidCursor = "invalid_cursor"

var errInvalidCursor = fieldProblem(http.StatusBadRequest, "/query/cursor", fieldInvalidCursor,
	"The cursor is malformed or belongs to another list.")

// pageSize is the limit of a list request, or the default.
func pageSize(limit *gen.Limit) int {
	if limit == nil || *limit < 1 {
		return defaultPageSize
	}
	return min(*limit, maxPageSize)
}

// cursorEnvelope is the content of an opaque cursor: the list it belongs to and the sort key of the last item of the
// previous page, after which the next page starts. A cursor is a position, never an offset (NFR-13), so items
// inserted before it do not shift the pages that follow.
type cursorEnvelope struct {
	List string          `json:"l"`
	Key  json.RawMessage `json:"k"`
}

// encodeCursor makes the cursor of list after the sort key key.
func encodeCursor(list string, key any) string {
	k, _ := json.Marshal(key) // sort keys are plain structs
	b, _ := json.Marshal(cursorEnvelope{List: list, Key: k})
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor reads a cursor of list into key and reports whether there was one. A cursor that does not decode, or that
// belongs to another list, is a 400 invalid_cursor.
func decodeCursor(cursor *gen.Cursor, list string, key any) (bool, error) {
	if cursor == nil || *cursor == "" {
		return false, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(*cursor)
	if err != nil {
		return false, errInvalidCursor
	}
	var env cursorEnvelope
	if err := json.Unmarshal(raw, &env); err != nil || env.List != list || len(env.Key) == 0 {
		return false, errInvalidCursor
	}
	if err := json.Unmarshal(env.Key, key); err != nil {
		return false, errInvalidCursor
	}
	return true, nil
}

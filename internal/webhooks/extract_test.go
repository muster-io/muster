// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package webhooks

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

// TestExtract is C-15.FR-3 and FR-4: each rule picks the first value its JSONPath selects — a string, a number as
// written, a boolean — and a rule that selects nothing, null, an object or an array, a value too long, a path that does
// not parse, or a response that is not one JSON value finds nothing.
func TestExtract(t *testing.T) {
	body := []byte(`{"data":{"id":"m1","n":12345678901234567890,"ok":true,"none":null,"obj":{},"list":[1,2],` +
		`"long":"` + strings.Repeat("x", maxValueLength+1) + `"},"items":[{"id":"a"},{"id":"b"}]}`)
	rules := []ExtractionRule{{"id", "$.data.id"}, {"n", "$.data.n"}, {"ok", "$['data']['ok']"},
		{"first", "$.items[*].id"}, {"filtered", "$.items[?@.id == 'b'].id"}, {"none", "$.data.none"},
		{"obj", "$.data.obj"}, {"list", "$.data.list"}, {"long", "$.data.long"}, {"absent", "$.nothing.here"},
		{"broken", "$.["}}
	found, missing := extract(body, rules)
	want := map[string]string{"id": "m1", "n": "12345678901234567890", "ok": "true", "first": "a", "filtered": "b"}
	if !maps.Equal(found, want) || !slices.Equal(missing, []string{"none", "obj", "list", "long", "absent", "broken"}) {
		t.Errorf("found %v, missing %v", found, missing)
	}
	for _, b := range []string{"", "not json", `{"data":{"id":"m1"}} {}`, `{"data":`, "<html>"} {
		if found, missing := extract([]byte(b), rules[:1]); len(found) != 0 || !slices.Equal(missing, []string{"id"}) {
			t.Errorf("%q: %v %v", b, found, missing)
		}
	}
	if found, missing := extract(body, nil); len(found) != 0 || missing != nil {
		t.Errorf("no rules: %v %v", found, missing)
	}
}

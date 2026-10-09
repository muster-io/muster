// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package webhooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"

	"github.com/theory/jsonpath"
)

// extract applies the extraction rules of a request to the body of its response (C-15.FR-3, FR-4): each rule's
// JSONPath (RFC 9535) picks the first node it selects in the body as JSON, and a string, number or boolean is stored
// as a string by the rule's name, numbers as written. A rule finds nothing when the body is not one JSON value, its
// path selects nothing, null, an object or an array, or the value is longer than maxValueLength bytes;
// found are the values by rule name and missing the names of the rules that found nothing, in their order. The body
// was read by internal/outbound up to its limit, so the work is bounded.
func extract(body []byte, rules []ExtractionRule) (found map[string]string, missing []string) {
	found = map[string]string{}
	if len(rules) == 0 {
		return found, nil
	}
	doc, ok := decodeJSON(body)
	for _, r := range rules {
		v, hit := "", false
		if ok {
			v, hit = pick(doc, r.Path)
		}
		if hit {
			found[r.Name] = v
		} else {
			missing = append(missing, r.Name)
		}
	}
	return found, missing
}

// decodeJSON reads body as exactly one JSON value, numbers kept as written.
func decodeJSON(body []byte) (any, bool) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, false // more than one value, or text after it
	}
	return doc, true
}

// pick is the scalar at path in doc as a string.
func pick(doc any, path string) (string, bool) {
	p, err := jsonpath.Parse(path)
	if err != nil {
		return "", false
	}
	nodes := p.Select(doc)
	if len(nodes) == 0 {
		return "", false
	}
	var s string
	switch v := nodes[0].(type) {
	case string:
		s = v
	case json.Number:
		s = v.String()
	case bool:
		s = strconv.FormatBool(v)
	default:
		return "", false
	}
	if len(s) > maxValueLength {
		return "", false
	}
	return s, true
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"bytes"
	"os"
	"slices"
	"strings"
	"testing"
)

// TestSpec: the embedded specification is api/openapi.yaml byte for byte (C-03.AC-12).
func TestSpec(t *testing.T) {
	want, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(Spec(), want) {
		t.Error("the embedded specification differs from openapi.yaml")
	}
}

// TestPathTemplates reads the keys of the paths object: every one starts with /, none repeats, and the scan stops at
// the next top-level key. internal/api compares them with the parsed document.
func TestPathTemplates(t *testing.T) {
	paths := PathTemplates()
	if len(paths) < 100 || paths[0] != "/sign-in-options" {
		t.Fatalf("%d paths, first %v", len(paths), paths[:1])
	}
	for _, p := range paths {
		if !strings.HasPrefix(p, "/") || strings.HasSuffix(p, ":") {
			t.Errorf("path %q", p)
		}
	}
	if !slices.Contains(paths, "/alert-groups/{alert_group_id}") || slices.Contains(paths, "/") {
		t.Error("a path template is missing or a key outside paths was read")
	}
	sorted := slices.Clone(paths)
	slices.Sort(sorted)
	if len(slices.Compact(sorted)) != len(paths) {
		t.Error("a path repeats")
	}
}

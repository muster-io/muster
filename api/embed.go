// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package api embeds the API specification, api/openapi.yaml, that the binary is built from: the app listener serves
// it byte for byte at /api/v1/openapi.yaml (C-03.FR-23), request validation reads it, and the API metrics take their
// route_pattern values from its path templates.
package api

import (
	"bytes"
	_ "embed"
	"slices"
	"strings"
)

//go:embed openapi.yaml
var spec []byte

// Spec returns the specification as checked in; the caller must not modify it.
func Spec() []byte {
	return spec
}

// PathTemplates returns the keys of the specification's paths object, such as /alert-groups/{alert_group_id}, in
// the order of the file. It reads the lines of the document instead of parsing it, so that the metric registry can
// call it at start-up; a test compares the result with the parsed document.
func PathTemplates() []string {
	var paths []string
	inPaths := false
	for line := range bytes.Lines(spec) {
		l := strings.TrimRight(string(line), "\r\n")
		switch {
		case l == "paths:":
			inPaths = true
		case inPaths && l != "" && l[0] != ' ' && l[0] != '#':
			return paths
		case inPaths && strings.HasPrefix(l, "  /") && strings.HasSuffix(l, ":") && !strings.HasPrefix(l, "   "):
			p := strings.TrimSuffix(strings.TrimPrefix(l, "  "), ":")
			p = strings.Trim(p, `'"`)
			if !slices.Contains(paths, p) {
				paths = append(paths, p)
			}
		}
	}
	return paths
}

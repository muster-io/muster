// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package web embeds the single-page application that `make build` writes to web/dist. Before the first build dist
// holds only the checked-in .gitkeep, which keeps the embed pattern valid, so the package builds in any checkout.
package web

import (
	"embed"
	"io/fs"
)

// The all: prefix also embeds files whose names start with a dot, which .gitkeep needs to match the pattern.
//
//go:embed all:dist
var dist embed.FS

// Dist returns the built SPA, with index.html at its root.
func Dist() fs.FS {
	// fs.Sub fails only on an invalid path, and "dist" is a valid one.
	sub, _ := fs.Sub(dist, "dist")
	return sub
}

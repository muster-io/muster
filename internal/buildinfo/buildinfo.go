// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package buildinfo holds the version and commit injected at build time with -ldflags -X.
package buildinfo

var (
	Version = "0.0.0-dev"
	Commit  = "unknown"
)

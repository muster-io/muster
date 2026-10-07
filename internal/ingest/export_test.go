// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import "testing"

// SetFiringPage makes the reads of the Group key preview list n distinct bodies a page until the test ends.
func SetFiringPage(t testing.TB, n int32) {
	page := firingPage
	firingPage = n
	t.Cleanup(func() { firingPage = page })
}

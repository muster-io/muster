// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build !race

package schedules

// raceDetector reports whether the tests run with the race detector, which makes timings meaningless.
const raceDetector = false

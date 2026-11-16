// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package connections

import "time"

// SetGroupPeopleBudget replaces destination.telegram.group_people_budget for the tests of s.
func SetGroupPeopleBudget(s *Service, d time.Duration) { s.groupPeopleBudget = d }

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Complete Team reads for the web tests: a Team, the reference to a Team that an object it owns carries, and a person
// in a Team. Every required field holds a neutral value — a Team that follows the Organization's Working hours, with
// nobody in it; a Responder added by hand who linked no messenger — and a test passes, field by field, only what it
// cares about. A required field added to a read is set here, the one place the TypeScript check names, instead of in
// every test that builds a Team.

import type { Team, TeamRef, TeamUser, WorkingHours } from "../../api/gen/model";

/** The Organization's Working hours by default: Monday to Friday, 09:00 to 18:00, UTC. */
export function workingHours(overrides: Partial<WorkingHours> = {}): WorkingHours {
  return {
    days: ["monday", "tuesday", "wednesday", "thursday", "friday"],
    start: "09:00",
    end: "18:00",
    time_zone: "UTC",
    ...overrides,
  };
}

export function team(overrides: Partial<Team> = {}): Team {
  return {
    id: "TM0000000000PA",
    name: "Payments",
    slug: "payments",
    description: "",
    own_working_hours: false,
    working_hours: workingHours(),
    user_count: 0,
    created_at: "2026-10-13T09:00:00Z",
    etag: '"1"',
    ...overrides,
  };
}

export function teamRef(overrides: Partial<TeamRef> = {}): TeamRef {
  return {
    id: "TM0000000000PA",
    name: "Payments",
    slug: "payments",
    deleted: false,
    ...overrides,
  };
}

export function teamUser(overrides: Partial<TeamUser> = {}): TeamUser {
  return {
    user: { id: "SR0000000000BB", name: "bob", login: "bob", deactivated: false },
    role: "responder",
    sources: ["by_hand"],
    linked_messengers: [],
    ...overrides,
  };
}

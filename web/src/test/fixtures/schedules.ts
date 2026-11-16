// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Complete Schedule reads for the web tests: the people of the Schedule "Main" of scenario 1 of C-27 (petr, sergey,
// olga), a Rotation layer, a Schedule, the Result of a period, a segment, a Shift and an Override. A test passes, field
// by field, only what it cares about, and a required field added to a read is set here, the one place the TypeScript
// check names.

import { type QueryClient } from "@tanstack/react-query";
import { expect, vi } from "vitest";

import type {
  OnCallSegment,
  Permission,
  RotationLayer,
  RotationLayerShifts,
  RotationShift,
  Schedule,
  ScheduleOnCall,
  ScheduleOverride,
  ScheduleWarning,
  Session,
  UserRef,
} from "../../api/gen/model";
import { SESSION_QUERY_KEY, type SessionRead } from "../../lib/api";
import { teamRef } from "./teams";

export function person(login: string, overrides: Partial<UserRef> = {}): UserRef {
  return {
    id: `SR00000000${login.toUpperCase().padEnd(4, "0").slice(0, 4)}`,
    name: login,
    login,
    deactivated: false,
    ...overrides,
  };
}

export const PETR = person("petr");
export const SERGEY = person("sergey");
export const OLGA = person("olga");

export function rotationLayer(overrides: Partial<RotationLayer> = {}): RotationLayer {
  return {
    id: "RL0000000000AA",
    number: 1,
    name: "Around the clock",
    user_ids: [PETR.id, SERGEY.id, OLGA.id],
    users: [PETR, SERGEY, OLGA],
    shift_length: "week",
    handover_time: "10:00",
    handover_day: "monday",
    start_date: "2026-11-16",
    limits: null,
    ...overrides,
  };
}

export function schedule(overrides: Partial<Schedule> = {}): Schedule {
  return {
    id: "SC0000000000MA",
    team_id: "TM0000000000PA",
    team: teamRef({ name: "payments", slug: "payments" }),
    name: "Main",
    time_zone: "Europe/Berlin",
    main: true,
    gap_alert: "by_default",
    gap_alert_active: true,
    next_gap: null,
    rotation_layers: [rotationLayer()],
    created_at: "2026-11-01T09:00:00Z",
    etag: '"1"',
    ...overrides,
  };
}

export function segment(
  start: string,
  end: string,
  user: UserRef | null,
  layer: { id: string; name: string } | null = null,
): OnCallSegment {
  return {
    start,
    end,
    user,
    source:
      user === null
        ? null
        : {
            kind: "rotation_layer",
            rotation_layer: layer ?? { id: "RL0000000000AA", name: "Around the clock" },
          },
  };
}

export function shift(
  start: string,
  end: string,
  user: UserRef,
  skipped: RotationShift["skipped"] = null,
): RotationShift {
  return { start, end, user, skipped };
}

export function layerShifts(
  name: string,
  shifts: RotationShift[],
  id = "RL0000000000AA",
): RotationLayerShifts {
  return { rotation_layer: { id, name }, shifts };
}

export function scheduleOverride(overrides: Partial<ScheduleOverride> = {}): ScheduleOverride {
  return {
    id: "OV0000000000AA",
    user_id: PETR.id,
    user: PETR,
    replaces_user_id: OLGA.id,
    replaces_user: OLGA,
    start: "2026-11-18T10:00:00Z",
    end: "2026-11-18T18:00:00Z",
    created_by: { kind: "user", id: OLGA.id, name: "olga", login: "olga", deactivated: false },
    created_at: "2026-11-16T09:00:00Z",
    etag: '"1"',
    ...overrides,
  };
}

export function warning(overrides: Partial<ScheduleWarning>): ScheduleWarning {
  return {
    kind: "gap",
    user: null,
    rotation_layer: null,
    override_id: null,
    start: null,
    end: null,
    destination: null,
    messenger: null,
    reason: null,
    ...overrides,
  };
}

export function onCall(overrides: Partial<ScheduleOnCall> = {}): ScheduleOnCall {
  return {
    from: "2026-11-15T23:00:00Z",
    to: "2026-11-29T23:00:00Z",
    segments: [],
    rotation_layers: [],
    overrides: [],
    warnings: [],
    ...overrides,
  };
}

/** Seeds the session of the signed-in person, whom the pages read for their Permissions and who they are. */
export function seedSession(
  queryClient: QueryClient,
  who: UserRef,
  role: Session["user"]["role"],
  permissions: Permission[],
): void {
  const read: SessionRead = {
    session: {
      state: "active",
      user: {
        id: who.id,
        name: who.name,
        login: who.login ?? who.name,
        role,
        source: "local",
        sign_in_method: "local",
        status: "active",
        totp_enabled: false,
        time_zone: "Europe/Berlin",
        created_at: "2026-10-13T09:00:00Z",
        etag: '"1"',
      },
      csrf_token: "csrf",
      expires_at: "2026-11-30T19:00:00Z",
      idle_expires_at: "2026-11-30T19:00:00Z",
      docs_version: "latest",
      method: "local",
      permissions,
    },
    ended: false,
  };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
}

/** A JSON answer, or a Problem for a status of 400 or more. */
export function reply(status: number, body?: unknown): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: { "Content-Type": status >= 400 ? "application/problem+json" : "application/json" },
  });
}

/** A Problem with its errors, as the API answers a refusal. */
export function problem(
  status: number,
  extra: Record<string, unknown> = {},
): Record<string, unknown> {
  return { type: "about:blank", title: `HTTP ${status}`, status, ...extra };
}

/** Waits until the text of an element holds a text: toHaveTextContent with a string needs all of it. */
export const hasText = (el: { element: () => Element }, text: string) =>
  vi.waitFor(() => expect(el.element().textContent).toContain(text));

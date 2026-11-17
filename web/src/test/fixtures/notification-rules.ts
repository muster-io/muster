// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// A stub of the Notification rules API for the component tests: the rules of one person in the four states as
// internal/notificationrules computes them from the Organization's default rules, their confirmed contacts, and the
// Notification gateways. A test sets what it cares about and reads `calls`.

import { vi } from "vitest";

import type {
  GatewayContact,
  NotificationRuleAction,
  NotificationRules,
  NotificationRulesUpdate,
} from "../../api/gen/model";
import { bridgeConnection } from "./connections";
import { problem, reply } from "./schedules";

export const GATEWAY = bridgeConnection({
  id: "CN00000000GW01",
  name: "Example Gateway",
  roles: ["gateway"],
});
export const SECOND_GATEWAY = bridgeConnection({
  id: "CN00000000GW02",
  name: "Backup Gateway",
  roles: ["gateway"],
});

export const mention: NotificationRuleAction = { kind: "mention" };
export const wait = (minutes: number): NotificationRuleAction => ({
  kind: "wait",
  wait_seconds: minutes * 60,
});

export function callAt(gateway: { id: string; name: string }): NotificationRuleAction {
  return {
    kind: "call",
    gateway_id: gateway.id,
    gateway: { id: gateway.id, name: gateway.name },
    available: true,
    unavailable_reason: null,
  };
}

export function contactAt(
  gateway: { id: string; name: string },
  contact: string,
  overrides: Partial<GatewayContact> = {},
): GatewayContact {
  return {
    id: `GC0000000${gateway.id.slice(-5)}`,
    connection: { id: gateway.id, name: gateway.name },
    contact,
    confirmed: true,
    confirmed_at: "2026-11-17T10:01:00Z",
    replaces: null,
    created_at: "2026-11-17T10:00:00Z",
    ...overrides,
  };
}

export interface RulesApi {
  /** The path of the rules, "/api/v1/me/notification-rules" or the User's. */
  path: string;
  /** The path of the contacts. */
  contactsPath: string;
  gateways: (typeof GATEWAY)[];
  contacts: GatewayContact[];
  /** The Organization's rules, which the state `organization` shows. */
  organization: { normal: NotificationRuleAction[]; important: NotificationRuleAction[] };
  stored: NotificationRules;
  version: number;
  /** The status the next update answers instead of the change. */
  updateStatus?: number;
  calls: { method: string; url: string; body: unknown; ifMatch: string | null }[];
}

export function newRulesApi(path = "/api/v1/me/notification-rules"): RulesApi {
  const api: RulesApi = {
    path,
    contactsPath: path.startsWith("/api/v1/me/")
      ? "/api/v1/me/gateway-contacts"
      : path.replace("/notification-rules", "/gateway-contacts"),
    gateways: [GATEWAY],
    contacts: [],
    organization: { normal: [mention], important: [mention, wait(5), mention] },
    stored: {
      state: "organization",
      gateway: null,
      normal: [],
      important: [],
      etag: '"1"',
    },
    version: 1,
    calls: [],
  };
  return api;
}

/** The rules a person reads, from their stored state and the Organization's. */
export function reading(api: RulesApi): NotificationRules {
  const rules = api.stored;
  const org = rules.state === "organization";
  const mark = (actions: NotificationRuleAction[]) =>
    actions.map((a) => {
      if (a.kind !== "call") {
        return a;
      }
      const confirmed = api.contacts.some((c) => c.confirmed && c.connection.id === a.gateway_id);
      return {
        ...a,
        available: confirmed,
        unavailable_reason: confirmed ? null : ("no_confirmed_contact" as const),
      };
    });
  return {
    ...rules,
    normal: mark(org ? api.organization.normal : rules.normal),
    important: mark(org ? api.organization.important : rules.important),
    etag: `"${api.version}"`,
  };
}

function apply(api: RulesApi, change: NotificationRulesUpdate): NotificationRules | Response {
  switch (change.state) {
    case "organization":
      api.stored = {
        ...api.stored,
        state: "organization",
        gateway: null,
        normal: [],
        important: [],
      };
      break;
    case "no_call":
      api.stored = {
        ...api.stored,
        state: "no_call",
        gateway: null,
        normal: api.organization.normal,
        important: api.organization.important,
      };
      break;
    case "call": {
      const gateway = api.gateways.find((g) => g.id === change.gateway_id);
      if (gateway === undefined) {
        return reply(422, problem(422, { errors: [{ pointer: "/gateway_id", code: "required" }] }));
      }
      const [first, ...rest] = api.organization.important;
      api.stored = {
        ...api.stored,
        state: "call",
        gateway: { id: gateway.id, name: gateway.name },
        normal: api.organization.normal,
        important: [callAt(gateway), ...(first === undefined ? [] : [first]), ...rest],
      };
      break;
    }
    default:
      api.stored = {
        ...api.stored,
        state: "custom",
        gateway: null,
        normal: change.normal ?? [],
        important: change.important ?? [],
      };
  }
  api.version += 1;
  return reading(api);
}

/** Answers the rules, their contacts and the gateways; anything else is 404. */
export function installRulesApi(api: RulesApi): void {
  vi.spyOn(window, "fetch").mockImplementation((input, init) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const method = init?.method ?? "GET";
    const headers = new Headers(init?.headers);
    const body: NotificationRulesUpdate | undefined =
      typeof init?.body === "string" ? JSON.parse(init.body) : undefined;
    api.calls.push({ method, url, body, ifMatch: headers.get("If-Match") });
    if (url.startsWith("/api/v1/connections")) {
      return Promise.resolve(reply(200, { items: api.gateways, next_cursor: null }));
    }
    if (url === api.contactsPath && method === "GET") {
      return Promise.resolve(reply(200, { items: api.contacts }));
    }
    if (url === api.path && method === "GET") {
      return Promise.resolve(reply(200, reading(api)));
    }
    if (url === `${api.path}/reset` && method === "POST") {
      return Promise.resolve(reply(200, apply(api, { state: "organization" })));
    }
    if (url === api.path && method === "PUT") {
      if (api.updateStatus !== undefined) {
        return Promise.resolve(reply(api.updateStatus, problem(api.updateStatus)));
      }
      const result = body === undefined ? reply(400, problem(400)) : apply(api, body);
      return Promise.resolve(result instanceof Response ? result : reply(200, result));
    }
    return Promise.resolve(reply(404, problem(404)));
  });
}

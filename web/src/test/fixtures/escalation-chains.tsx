// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Complete Escalation chain reads for the web tests: the chain "payments" of scenario 1 of C-32 with its steps, the
// runs "How it went" lists with their Pages, and a stub of the API the chain page reads (the Schedules, the people, the
// Teams, the Notification gateways, the Destinations and the Working hours). A test passes, field by field, only what
// it cares about, and a required field added to a read is set here, the one place the TypeScript check names.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRouter,
} from "@tanstack/react-router";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { vi } from "vitest";
import { render } from "vitest-browser-react";

import type {
  Connection,
  Destination,
  EscalationChain,
  EscalationRun,
  EscalationRunPage,
  EscalationRunStep,
  EscalationStep,
  Permission,
  Schedule,
  Team,
  User,
} from "../../api/gen/model";
import i18n from "../../i18n";
import { PETR, reply, schedule, seedSession } from "./schedules";
import { team, teamRef, workingHours } from "./teams";

export const PAYMENTS = team({
  id: "TM0000000000PA",
  name: "payments",
  slug: "payments",
  own_working_hours: true,
  working_hours: workingHours({ start: "11:00", end: "19:00" }),
});
export const DEVOPS = team({ id: "TM00000000DEVO", name: "devops", slug: "devops" });

export const MAIN = schedule({ id: "SC0000000000MA", name: "Main" });
export const SECOND_LINE = schedule({ id: "SC00000000SECL", name: "Second line" });

export function user(login: string, overrides: Partial<User> = {}): User {
  return {
    id: `SR00000000${login.toUpperCase().padEnd(4, "0").slice(0, 4)}`,
    name: login,
    login,
    role: "responder",
    source: "local",
    sign_in_method: "local",
    status: "active",
    totp_enabled: false,
    created_at: "2026-10-13T09:00:00Z",
    etag: '"1"',
    ...overrides,
  };
}

export const IVAN = user("ivan");
export const PETR_USER = user("petr");

/** A step as the API reads it: the fields of its kind, its number and the objects it names. */
export function step(number: number, overrides: Partial<EscalationStep>): EscalationStep {
  return {
    kind: "wait",
    number,
    ...overrides,
  };
}

/** The steps of the payments chain of scenario 1: seven of them, the working hours on the sixth. */
export function paymentsSteps(): EscalationStep[] {
  const main = { id: MAIN.id, name: MAIN.name, team: teamRef({ name: "payments" }) };
  const second = {
    id: SECOND_LINE.id,
    name: SECOND_LINE.name,
    team: teamRef({ name: "payments" }),
  };
  return [
    step(1, { kind: "page_on_call", schedule_id: MAIN.id, schedule: main, importance: "normal" }),
    step(2, { kind: "wait", wait_seconds: 600 }),
    step(3, {
      kind: "page_on_call",
      schedule_id: MAIN.id,
      schedule: main,
      importance: "important",
      severity_condition: "critical",
    }),
    step(4, { kind: "wait", wait_seconds: 900 }),
    step(5, {
      kind: "page_on_call",
      schedule_id: SECOND_LINE.id,
      schedule: second,
      importance: "important",
      severity_condition: "critical",
    }),
    step(6, {
      kind: "page_team",
      team_id: PAYMENTS.id,
      team: teamRef({ name: "payments" }),
      working_hours_only: true,
    }),
    step(7, { kind: "repeat", repeat_from: 3, repeat_times: 2 }),
  ];
}

/** One step of the payments chain by its number, from 1. */
export function paymentsStep(number: number): EscalationStep {
  const found = paymentsSteps()[number - 1];
  if (found === undefined) {
    throw new Error(`the payments chain has no step ${number}`);
  }
  return found;
}

export function chain(overrides: Partial<EscalationChain> = {}): EscalationChain {
  return {
    id: "EC0000000PAYM1",
    name: "payments",
    description: "",
    owning_team_id: PAYMENTS.id,
    owning_team: teamRef({ name: "payments" }),
    steps: paymentsSteps(),
    referrers: { routes: [], teams: [] },
    destinations: [],
    warnings: [],
    created_at: "2026-11-17T09:00:00Z",
    etag: '"1"',
    ...overrides,
  };
}

export function runPage(overrides: Partial<EscalationRunPage> = {}): EscalationRunPage {
  return {
    kind: "mention",
    gateway: null,
    users: [PETR],
    team: null,
    everyone: null,
    outcome: "delivered",
    reason: null,
    ...overrides,
  };
}

export function runStep(
  number: number,
  overrides: Partial<EscalationRunStep> = {},
): EscalationRunStep {
  return {
    number,
    kind: "page_on_call",
    at: "2026-11-17T03:20:00Z",
    outcome: "made",
    reason: null,
    pages: [],
    ...overrides,
  };
}

export function run(overrides: Partial<EscalationRun> = {}): EscalationRun {
  return {
    id: "ER00000000RUN1",
    alert_group: {
      id: "AG0000000000A1",
      number: 12,
      title: "KubernetesContainerOOMKilled",
      severity_level: "warning",
    },
    started_at: "2026-11-17T03:20:00Z",
    state: "running",
    ended_at: null,
    stopped_by: null,
    steps: [],
    ...overrides,
  };
}

/** What the stub answers; a test changes what it cares about before it renders. */
export interface ChainApi {
  chains: EscalationChain[];
  runs: EscalationRun[];
  schedules: Schedule[];
  users: User[];
  teams: Team[];
  /** The Connections of the type bridge in the role gateway. */
  gateways: Connection[];
  /** Every Connection. */
  connections: Connection[];
  destinations: Destination[];
  /** Answers a call instead of the stub: a Response, or undefined to let the stub answer. */
  respond?: (call: ApiCall) => Response | undefined;
  calls: ApiCall[];
}

export interface ApiCall {
  method: string;
  url: string;
  body: unknown;
  ifMatch: string | null;
}

export function newChainApi(): ChainApi {
  return {
    chains: [chain()],
    runs: [],
    schedules: [MAIN, SECOND_LINE],
    users: [IVAN, PETR_USER],
    teams: [PAYMENTS, DEVOPS],
    gateways: [],
    connections: [],
    destinations: [],
    calls: [],
  };
}

function json(body: unknown, status = 200): Promise<Response> {
  return Promise.resolve(reply(status, body));
}

/** Answers what the chain page, the Route form and the Team page read; anything else is 404. */
export function installChainApi(api: ChainApi): void {
  vi.spyOn(window, "fetch").mockImplementation((input, init) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const method = init?.method ?? "GET";
    const headers = new Headers(init?.headers);
    const body: unknown = typeof init?.body === "string" ? JSON.parse(init.body) : undefined;
    const call: ApiCall = { method, url, body, ifMatch: headers.get("If-Match") };
    api.calls.push(call);
    const own = api.respond?.(call);
    if (own !== undefined) {
      return Promise.resolve(own);
    }
    const path = url.split("?")[0] ?? url;
    const query = url.includes("?") ? url.slice(url.indexOf("?")) : "";
    const runs = /^\/api\/v1\/escalation-chains\/([^/]+)\/runs$/.exec(path);
    if (runs !== null) {
      return json({ items: api.runs, next_cursor: null });
    }
    const one = /^\/api\/v1\/escalation-chains\/([^/]+)$/.exec(path);
    if (one !== null) {
      const found = api.chains.find((c) => c.id === one[1]);
      return found === undefined ? json({ status: 404 }, 404) : json(found);
    }
    if (path === "/api/v1/escalation-chains") {
      return json({ items: api.chains, next_cursor: null });
    }
    if (path === "/api/v1/schedules") {
      return json({ items: api.schedules, next_cursor: null });
    }
    if (path === "/api/v1/users") {
      return json({ items: api.users, next_cursor: null });
    }
    const oneTeam = /^\/api\/v1\/teams\/([^/]+)$/.exec(path);
    if (oneTeam !== null) {
      const found = api.teams.find((t) => t.id === oneTeam[1]);
      return found === undefined ? json({ status: 404 }, 404) : json(found);
    }
    if (path === "/api/v1/teams") {
      return json({ items: api.teams, next_cursor: null });
    }
    if (path === "/api/v1/connections") {
      return json({
        items: query.includes("role=gateway") ? api.gateways : api.connections,
        next_cursor: null,
      });
    }
    if (path === "/api/v1/destinations") {
      return json({ items: api.destinations, next_cursor: null });
    }
    return Promise.resolve(new Response("{}", { status: 404 }));
  });
}

export const ADMIN_PERMISSIONS: Permission[] = [
  "escalation-chains:read",
  "escalation-chains:write",
  "schedules:read",
  "users:read",
  "teams:read",
  "teams:write",
  "connections:read",
  "connections:write",
  "destinations:read",
  "routes:read",
  "routes:write",
  "organization:read",
];

export const RESPONDER_PERMISSIONS: Permission[] = [
  "escalation-chains:read",
  "schedules:read",
  "teams:read",
  "routes:read",
];

/** Renders an element in a router, a query client with the session of a person with the Permissions, and i18n. */
export async function renderWithApp(
  element: ReactNode,
  permissions: Permission[] = ADMIN_PERMISSIONS,
) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  seedSession(
    queryClient,
    PETR,
    permissions.includes("teams:write") ? "admin" : "responder",
    permissions,
  );
  const root = createRootRoute({ component: () => element });
  const router = createRouter({
    routeTree: root,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  return render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
}

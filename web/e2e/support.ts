// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Helpers of the end-to-end specs and of the documentation screenshots: the API as the development Admin, the database
// of the run, the fake IdP, the fake bridge and the fake Slack and Telegram servers of `muster dev`, the realistic
// addresses its routes send to the fakes, the removal of what a spec created, TOTP codes and the layout checks.

import { createHmac } from "node:crypto";

import { expect, request, type APIRequestContext, type Page } from "@playwright/test";
import { Client, type QueryResult } from "pg";

export const APP = "http://localhost:8080";
export const FAKE_IDP = "http://127.0.0.1:18090";
export const ADMIN_LOGIN = "admin@example.org";
/** The control API of the fake Alertmanager of `muster dev`. */
export const FAM = "http://127.0.0.1:19093/_fake";
/** The control APIs of the fake Telegram server and of the two fake Mattermost servers of `muster dev`. */
export const FTG = "http://127.0.0.1:18081/_fake";
export const FMM = "http://127.0.0.1:18065/_fake";
export const FMM2 = "http://127.0.0.1:18066/_fake";
/** The published token of the first Mattermost bot, the one of the demo Connection "Example Mattermost". */
export const MM_TOKEN = "muster-dev-mattermost-token";
/** The published tokens of the second bots of the fake servers, for which `muster dev` creates no Connection. */
export const MM_SECOND_TOKEN = "muster-dev-mattermost-token-b";
export const TG_SECOND_TOKEN = "234567:dev-telegram-token-b";
/** The fake bridge of `muster dev` and its control API. */
export const BRIDGE_URL = "http://127.0.0.1:18094";
export const FBR = `${BRIDGE_URL}/_fake`;
/** The published token of the fake bridge's first bot, the one of the demo Connection "Example Bridge". */
export const BRIDGE_TOKEN = "muster-dev-bridge-token";
/** The published token of the fake bridge's second bot, for which `muster dev` creates no Connection. */
export const BRIDGE_SECOND_TOKEN = "muster-dev-bridge-token-b";
/** The fake Slack server of `muster dev` and its control API. */
export const SLACK_URL = "http://127.0.0.1:18097";
export const FSL = `${SLACK_URL}/_fake`;
/** The published tokens of the fake Slack server's second app, b, for which `muster dev` creates no Connection. */
export const SLACK_SECOND_BOT_TOKEN = "xoxb-muster-dev-slack-token-b";
export const SLACK_SECOND_APP_TOKEN = "xapp-muster-dev-slack-token-b";
/**
 * The realistic addresses that `muster dev` routes to its fake servers for their published tokens (internal/devmode):
 * the default Bot API base URL, a Mattermost server and a bridge, which the documentation screenshots show instead of a
 * fake server's loopback address.
 */
export const TELEGRAM_BOT_API = "https://api.telegram.org";
export const CHAT_URL = "https://chat.example.org";
export const BRIDGE_EXAMPLE_URL = "https://bridge.example.org";
/** The ingestion endpoint that the fake Alertmanager's receivers send to. */
export const INGEST_URL = "http://localhost:8081/api/v1/ingest";
const CLOCK = "http://localhost:8082/_dev/clock";
// The published development password of `muster dev`.
export const ADMIN_PASSWORD = "muster-dev-password";

/**
 * How far the development clock runs ahead of the real time, in seconds. The specs of a run share one `muster dev`,
 * so a spec that compares a time it writes with the business clock adds this offset.
 */
export async function devClockOffset(): Promise<number> {
  const res = await fetch("http://localhost:8082/_dev/clock");
  expect(res.ok).toBe(true);
  const body = parse<{ offset_seconds: number }>(await res.text());
  return body.offset_seconds;
}

/** Moves the development clock forward; it moves the business clock of `muster dev`, never the real clock. */
export async function advance(seconds: number): Promise<void> {
  const res = await fetch(CLOCK, {
    method: "POST",
    body: JSON.stringify({ advance_seconds: seconds }),
  });
  expect(res.ok).toBe(true);
}

/** Calls the control API of the fake Alertmanager: groups, their Alerts, receivers and notifications. */
export async function fam(method: string, path: string, body?: unknown): Promise<void> {
  const res = await fetch(`${FAM}${path}`, {
    method,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  expect(res.ok, `${method} ${path}: ${res.status} ${await res.text()}`).toBe(true);
}

/**
 * Sends a notification of a fake group with its options (reason, list, max_alerts, …) and waits until Muster processed
 * every Snapshot of the Integration it goes to.
 */
export async function notify(
  integrationId: string,
  group: string,
  options: Record<string, unknown>,
): Promise<void> {
  await fam("POST", `/groups/${group}/notify`, options);
  await processed(integrationId);
}

/** Waits until Muster processed every Snapshot of the Integration integrationId that it stored. */
export async function processed(integrationId: string): Promise<void> {
  const admin = await adminApi();
  try {
    await expect
      .poll(async () => {
        const page = await admin.call<{ items: unknown[] }>(
          "GET",
          `/api/v1/stored-snapshots?integration=${integrationId}&state=pending`,
        );
        return page.items.length;
      })
      .toBe(0);
  } finally {
    await admin.dispose();
  }
}

/**
 * Creates an Integration without Static labels, a token for it and a receiver of the fake Alertmanager with the same
 * name that sends to it. Returns the Integration's id.
 */
export async function fakeIntegration(name: string): Promise<string> {
  const admin = await adminApi();
  try {
    const integration = await admin.call<{ id: string }>("POST", "/api/v1/integrations", {
      name,
      connection_mode: "webhook_only",
      static_labels: {},
      duplicate_window_seconds: 45,
      heartbeat: { enabled: false },
    });
    const token = await admin.call<{ value: string }>(
      "POST",
      `/api/v1/integrations/${integration.id}/tokens`,
      { name: "fake" },
    );
    await fam("POST", "/receivers", { name, url: INGEST_URL, token: token.value });
    return integration.id;
  } finally {
    await admin.dispose();
  }
}

/** Creates a Route with the On-call profile for the alerts with team=<team>, grouped by groupKey. Returns its id. */
export async function createRoute(name: string, team: string, groupKey: string[]): Promise<string> {
  const admin = await adminApi();
  try {
    const profiles = await admin.call<{ items: { id: string; policy: unknown }[] }>(
      "GET",
      "/api/v1/route-profiles",
    );
    const route = await admin.call<{ id: string }>("POST", "/api/v1/routes", {
      name,
      matchers: [{ label: "team", op: "=", value: team }],
      urgent: false,
      group_key: groupKey,
      destination_ids: [],
      policy: profiles.items.find((p) => p.id === "on_call")?.policy,
    });
    return route.id;
  } finally {
    await admin.dispose();
  }
}

/**
 * Moves the open Alert Groups of a Route to the Default route and deletes the Route, so that the specs after this one
 * see only their own Routes.
 */
export async function deleteRoute(id: string): Promise<void> {
  const admin = await adminApi();
  try {
    await admin.call("POST", `/api/v1/routes/${id}/move-open-alert-groups`);
    await admin.call("DELETE", `/api/v1/routes/${id}`);
  } finally {
    await admin.dispose();
  }
}

/** Calls the control API of the fake bridge and returns its answer, null for none. */
export async function fbr<T = unknown>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`${FBR}${path}`, {
    method,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  expect(res.ok, `${method} ${path}: ${res.status} ${text}`).toBe(true);
  return parse<T>(text === "" ? "null" : text);
}

/**
 * Puts back what the specs change in the fake bridge's configuration, which both of its bots share: protocol version
 * 1, user lookup by login, no link template, edits that never expire, both event delivery modes and nobody the bots
 * cannot write to.
 */
export async function resetFakeBridge(): Promise<void> {
  await fbr("PUT", "/config", {
    protocol_version: "1",
    user_lookup: "login",
    link_url_template: null,
    limits: { max_body_length: 4000, edit_max_age_seconds: null },
    event_delivery: ["poll", "push"],
    revoked: [],
    dm_blocked: [],
  });
}

/** Calls the control API of the fake Slack server and returns its answer, null for none. */
export async function fsl<T = unknown>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`${FSL}${path}`, {
    method,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  expect(res.ok, `${method} ${path}: ${res.status} ${text}`).toBe(true);
  return parse<T>(text === "" ? "null" : text);
}

/**
 * Puts back what the specs change in the configuration of the fake Slack server's second app: its default scopes and
 * channels — "alerts" with the bot, "other" without —, no other program connected to it, no request_url.
 */
export async function resetFakeSlack(): Promise<void> {
  await fsl("PUT", "/config", {
    scopes: { b: null },
    channels: { b: null },
    extra_connections: { b: null },
    request_url: { b: null },
    revoked: [],
  });
}

/**
 * Creates the Slack Connection name to the fake Slack server's second app, in Socket Mode, and checks it, so that Muster
 * knows its bot, workspace and app. Returns its id.
 */
export async function createSlackConnection(admin: Api, name = "ui"): Promise<string> {
  const created = await admin.call<{ id: string }>("POST", "/api/v1/connections", {
    type: "slack",
    name,
    event_mode: "socket",
    bot_token: SLACK_SECOND_BOT_TOKEN,
    app_token: SLACK_SECOND_APP_TOKEN,
    proxy: { enabled: false },
    limiter: { limit: 50, per_seconds: 60 },
  });
  await checkConnection(admin, created.id);
  return created.id;
}

interface NamedConnection {
  id: string;
  name: string;
}

/** The id of the Connection name; it fails when there is none. */
export async function connectionNamed(admin: Api, name: string): Promise<string> {
  const page = await admin.call<{ items: NamedConnection[] }>(
    "GET",
    "/api/v1/connections?limit=500",
  );
  const found = page.items.find((c) => c.name === name);
  expect(found, `the Connection ${name}`).toBeDefined();
  return found?.id ?? "";
}

/** Runs the check of a Connection, which stores the info a bridge answers, and expects it to pass. */
export async function checkConnection(admin: Api, id: string): Promise<void> {
  const result = await admin.call<{ ok: boolean }>("POST", `/api/v1/connections/${id}/checks`);
  expect(result.ok, `the check of ${id}`).toBe(true);
}

/**
 * Creates the Bridge Connection name to the fake bridge's second bot, polling, and checks it, so that Muster has read
 * its info. Returns its id.
 */
export async function createBridgeConnection(admin: Api, name = "br"): Promise<string> {
  const created = await admin.call<{ id: string }>("POST", "/api/v1/connections", {
    type: "bridge",
    name,
    base_url: BRIDGE_URL,
    bot_token: BRIDGE_SECOND_TOKEN,
    event_delivery: "poll",
    proxy: { enabled: false },
    limiter: { limit: 10, per_seconds: 1 },
  });
  await checkConnection(admin, created.id);
  return created.id;
}

/** Deletes every Connection named name, after the Destinations that use it, whatever an earlier run left. */
export async function removeBridgeConnection(admin: Api, name = "br"): Promise<void> {
  await removeConnectionNamed(admin, name);
}

/** Deletes every Slack Connection named name, after the Destinations that use it, whatever an earlier run left. */
export async function removeSlackConnection(admin: Api, name = "ui"): Promise<void> {
  await removeConnectionNamed(admin, name);
}

/** Calls the control API of the fake Mattermost server and returns its answer, null for none. */
export async function fmm<T = unknown>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`${FMM}${path}`, {
    method,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  expect(res.ok, `${method} ${path}: ${res.status} ${text}`).toBe(true);
  return parse<T>(text === "" ? "null" : text);
}

/** Calls the control API of the fake Telegram server and returns its answer, null for none. */
export async function ftg<T = unknown>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`${FTG}${path}`, {
    method,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  expect(res.ok, `${method} ${path}: ${res.status} ${text}`).toBe(true);
  return parse<T>(text === "" ? "null" : text);
}

/** Deletes every Integration named name, whatever an earlier run left. */
export async function removeIntegration(admin: Api, name: string): Promise<void> {
  const page = await admin.call<{ items: NamedConnection[] }>(
    "GET",
    "/api/v1/integrations?limit=500",
  );
  for (const i of page.items.filter((x) => x.name === name)) {
    await admin.call("DELETE", `/api/v1/integrations/${i.id}`);
  }
}

/**
 * Deletes every item named name of the collection path, such as /api/v1/destinations, whatever an earlier run left;
 * query is added to the read of the list, such as ?limit=50.
 */
export async function removeNamed(
  admin: Api,
  path: string,
  name: string,
  query = "",
): Promise<void> {
  const page = await admin.call<{ items: NamedConnection[] }>("GET", `${path}${query}`);
  for (const item of page.items.filter((x) => x.name === name)) {
    await admin.call("DELETE", `${path}/${item.id}`);
  }
}

/** Deletes every Route named name, after moving its open Alert Groups to the Default route, as deleteRoute does. */
export async function removeRoute(admin: Api, name: string): Promise<void> {
  const page = await admin.call<{ items: NamedConnection[] }>("GET", "/api/v1/routes");
  for (const r of page.items.filter((x) => x.name === name)) {
    await admin.call("POST", `/api/v1/routes/${r.id}/move-open-alert-groups`);
    await admin.call("DELETE", `/api/v1/routes/${r.id}`);
  }
}

/** Deletes the Account links of the signed-in user of admin in one messenger, such as telegram. */
export async function removeAccountLinks(admin: Api, messenger: string): Promise<void> {
  const page = await admin.call<{ items: { id: string; messenger: string }[] }>(
    "GET",
    "/api/v1/me/account-links",
  );
  for (const l of page.items.filter((x) => x.messenger === messenger)) {
    await admin.call("DELETE", `/api/v1/me/account-links/${l.id}`);
  }
}

/** Deletes every Connection named name, after the Destinations that use it, whatever an earlier run left. */
export async function removeConnection(admin: Api, name: string): Promise<void> {
  await removeConnectionNamed(admin, name);
}

/** Deletes every Connection named name, after the Destinations that use it. */
async function removeConnectionNamed(admin: Api, name: string): Promise<void> {
  const connections = await admin.call<{ items: NamedConnection[] }>(
    "GET",
    "/api/v1/connections?limit=500",
  );
  const ids = connections.items.filter((c) => c.name === name).map((c) => c.id);
  if (ids.length === 0) {
    return;
  }
  const destinations = await admin.call<{ items: { id: string; connection_id?: string }[] }>(
    "GET",
    "/api/v1/destinations?limit=500",
  );
  for (const d of destinations.items) {
    if (d.connection_id !== undefined && ids.includes(d.connection_id)) {
      await admin.call("DELETE", `/api/v1/destinations/${d.id}`);
    }
  }
  for (const id of ids) {
    await admin.call("DELETE", `/api/v1/connections/${id}`);
  }
}

/** Runs SQL on the database of the run, which the global setup created. */
export async function sql(query: string, values: unknown[] = []): Promise<QueryResult> {
  const url = process.env.MUSTER_E2E_WEB_DATABASE_URL;
  if (url === undefined) {
    throw new Error(
      "MUSTER_E2E_WEB_DATABASE_URL is not set: run the specs through playwright.config.ts",
    );
  }
  const client = new Client({ connectionString: url });
  await client.connect();
  try {
    return await client.query(query, values);
  } finally {
    await client.end();
  }
}

/** Parses an answer of the API as the type the caller names; the specs check the values they use. */
// oxlint-disable-next-line typescript/no-unnecessary-type-parameters -- the caller names the type of the body
function parse<T>(text: string): T {
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the spec describes the body; the assertions check it
  return JSON.parse(text) as T;
}

/** The first column of the first row of a query. */
export async function scalar(query: string, values: unknown[] = []): Promise<unknown> {
  const result = await sql(query, values);
  const row: unknown = result.rows[0];
  return row !== null && typeof row === "object" ? Object.values(row)[0] : undefined;
}

/** An API client signed in as a local user, sending the CSRF token of its session. */
export class Api {
  readonly context: APIRequestContext;
  private readonly csrf: string;

  private constructor(context: APIRequestContext, csrf: string) {
    this.context = context;
    this.csrf = csrf;
  }

  static async signIn(login: string, password: string): Promise<Api> {
    const context = await request.newContext({ baseURL: APP });
    const res = await context.post("/api/v1/sessions", { data: { login, password } });
    expect(res.status(), await res.text()).toBe(201);
    const session = parse<{ csrf_token: string }>(await res.text());
    return new Api(context, session.csrf_token);
  }

  async call<T = unknown>(
    method: string,
    path: string,
    data?: unknown,
    headers: Record<string, string> = {},
  ): Promise<T> {
    const res = await this.context.fetch(path, {
      method,
      data,
      headers: { "X-CSRF-Token": this.csrf, ...headers },
    });
    expect(res.ok(), `${method} ${path}: ${res.status()} ${await res.text()}`).toBe(true);
    const text = await res.text();
    return parse<T>(text === "" ? "null" : text);
  }

  /** Creates a local user and returns its id and the token of its password setup link. */
  async createUser(login: string, role = "responder"): Promise<{ id: string; token: string }> {
    const created = await this.call<{ user: { id: string }; password_setup_link: { url: string } }>(
      "POST",
      "/api/v1/users",
      { name: login, login, role },
    );
    const token = new URL(created.password_setup_link.url).hash.replace(/^#token=/, "");
    return { id: created.user.id, token };
  }

  /** Sets the Organization's "TOTP required" policy. */
  async setTotpPolicy(policy: "nobody" | "local_users" | "everyone"): Promise<void> {
    const res = await this.context.get("/api/v1/organization");
    const org = parse<
      Record<string, unknown> & { outgoing_heartbeat: { proxy: Record<string, unknown> } }
    >(await res.text());
    const { id: _id, etag: _etag, outgoing_heartbeat: heartbeat, ...rest } = org;
    const { password_status: _password, ...proxy } = heartbeat.proxy;
    await this.call(
      "PUT",
      "/api/v1/organization",
      { ...rest, totp_required: policy, outgoing_heartbeat: { proxy } },
      { "If-Match": res.headers().etag ?? "" },
    );
  }

  async dispose(): Promise<void> {
    await this.context.dispose();
  }
}

/** Scripts the person the fake IdP approves next. */
export async function nextIdpUser(sub: string, login: string, groups: string[]): Promise<void> {
  const res = await fetch(`${FAKE_IDP}/_fake/next-user`, {
    method: "POST",
    body: JSON.stringify({ sub, preferred_username: login, groups }),
  });
  expect(res.status).toBe(204);
}

/** Disables a person at the fake IdP: their refresh token answers invalid_grant from now on. */
export async function disableIdpUser(sub: string): Promise<void> {
  const res = await fetch(`${FAKE_IDP}/_fake/users/${encodeURIComponent(sub)}/disable`, {
    method: "POST",
  });
  expect(res.status).toBe(204);
}

function base32Decode(text: string): Buffer {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = 0;
  let value = 0;
  const out: number[] = [];
  for (const c of text.replace(/[\s=]/g, "").toUpperCase()) {
    const index = alphabet.indexOf(c);
    if (index < 0) {
      throw new Error(`not base32: ${c}`);
    }
    value = (value << 5) | index;
    bits += 5;
    if (bits >= 8) {
      out.push((value >>> (bits - 8)) & 0xff);
      bits -= 8;
    }
  }
  return Buffer.from(out);
}

/** The current RFC 6238 code (SHA-1, 30 s, 6 digits) of a base32 secret, as `oathtool --totp -b` prints it. */
export function totpCode(secret: string, at: number = Date.now()): string {
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(Math.floor(at / 1000 / 30)));
  const mac = createHmac("sha1", base32Decode(secret)).update(counter).digest();
  const offset = mac[mac.length - 1]! & 0x0f;
  const binary = mac.readUInt32BE(offset) & 0x7fffffff;
  return String(binary % 1_000_000).padStart(6, "0");
}

/** The secret the enrolment shows, without the spaces that group it. */
export async function shownSecret(page: Page): Promise<string> {
  const text = await page.getByTestId("totp-secret").textContent();
  return (text ?? "").replace(/\s/g, "");
}

/**
 * A code for the step offset steps from now; Muster accepts one step either way and each step once, so successive
 * codes of one user take increasing offsets. Near the end of a step it waits for the next one (at most 3 s), so that
 * the server checks the code in the step it was made for.
 */
export async function freshCode(page: Page, secret: string, offset = 0): Promise<string> {
  const left = 30_000 - (Date.now() % 30_000);
  if (left < 3_000) {
    await page.waitForTimeout(left + 100);
  }
  return totpCode(secret, Date.now() + offset * 30_000);
}

/**
 * How far a measured box may pass a limit and still be within it. Boxes are fractional, and the browser's layout units
 * can put an edge that sits on a limit a tiny fraction of a pixel past it, such as a dialog 360.0000038 pixels wide.
 * Whole numbers, such as scrollWidth and clientWidth, need no tolerance.
 */
export const SUBPIXEL = 0.5;

/** The page scrolls vertically only: nothing is wider than the viewport. */
export async function expectNoHorizontalScroll(page: Page): Promise<void> {
  const widths = await page.evaluate(() => ({
    scroll: document.documentElement.scrollWidth,
    client: document.documentElement.clientWidth,
  }));
  expect(
    widths.scroll,
    `scrollWidth ${widths.scroll} > clientWidth ${widths.client}`,
  ).toBeLessThanOrEqual(widths.client);
}

/** Collects the Content Security Policy violations of a page, to assert that there are none. */
export function watchCsp(page: Page): string[] {
  const violations: string[] = [];
  page.on("console", (message) => {
    if (message.type() === "error" && message.text().includes("Content Security Policy")) {
      violations.push(message.text());
    }
  });
  return violations;
}

/** What a page did once it deleted an entity. */
export interface Deletion {
  /** The requests to the entity's path that went out after its DELETE. */
  reads: string[];
  /** The console errors of the page, and a live hint of the deletion that never reached it. */
  errors: string[];
}

/**
 * Watches a page delete the entity at path, such as `/api/v1/connections/CN…`, whose live hint is {type, id}. The
 * answer to the DELETE is held back until the hint of the deletion has reached the page, so that the page is still
 * open when the hint invalidates the entity's query; every request to path after the DELETE and every console error
 * is recorded. A page reads an entity no more once its deletion starts, as a read would answer 404.
 */
export async function watchDeletion(
  page: Page,
  path: string,
  hint: { type: string; id: string },
): Promise<Deletion> {
  const deletion: Deletion = { reads: [], errors: [] };
  let deleted = false;
  page.on("request", (r) => {
    if (new URL(r.url()).pathname !== path) {
      return;
    }
    if (r.method() === "DELETE") {
      deleted = true;
    } else if (deleted) {
      deletion.reads.push(`${r.method()} ${path}`);
    }
  });
  page.on("console", (message) => {
    if (message.type() === "error") {
      deletion.errors.push(message.text());
    }
  });
  await page.route(
    (url) => url.pathname === path,
    async (route) => {
      if (route.request().method() !== "DELETE") {
        await route.fallback();
        return;
      }
      // A stream of its own, open before the DELETE reaches the server, hears the hint of the deletion that the
      // page's stream hears too.
      const listener = await page.evaluateHandle(
        ({ url, type, id }) =>
          new Promise<{ heard: Promise<boolean> }>((opened, failed) => {
            const source = new EventSource(url);
            const heard = new Promise<boolean>((resolve) => {
              const timer = setTimeout(() => {
                source.close();
                resolve(false);
              }, 10_000);
              source.addEventListener("hint", (event) => {
                const data: unknown =
                  typeof event.data === "string" ? JSON.parse(event.data) : null;
                if (
                  typeof data === "object" &&
                  data !== null &&
                  "type" in data &&
                  "id" in data &&
                  data.type === type &&
                  data.id === id
                ) {
                  clearTimeout(timer);
                  source.close();
                  resolve(true);
                }
              });
            });
            source.addEventListener("open", () => opened({ heard }), { once: true });
            source.addEventListener("error", () => failed(new Error("no live-updates stream")), {
              once: true,
            });
          }),
        { url: "/api/v1/live-updates", ...hint },
      );
      const response = await route.fetch();
      if (!(await listener.evaluate((l) => l.heard))) {
        deletion.errors.push(`no live hint ${hint.type} ${hint.id} within 10 s`);
      }
      await listener.dispose();
      await route.fulfill({ response });
    },
  );
  return deletion;
}

/** Signs in through the local form. */
export async function signInLocally(page: Page, login: string, password: string): Promise<void> {
  await page.getByLabel("Login").fill(login);
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
}

/** The refusal of the sign-in form while sign-in throttling holds a login back. */
const TOO_MANY_ATTEMPTS = /^Too many attempts\. Try again in (\d+) seconds?\.$/;

/**
 * Signs a local user in through the form and waits for the shell. A spec that refuses sign-ins may leave the source
 * address throttled; a success resets it, so a refused attempt is made again once the wait the page names has passed.
 */
export async function signIn(page: Page, login: string, password: string): Promise<void> {
  await page.goto("/sign-in");
  const tooMany = page.getByText(TOO_MANY_ATTEMPTS);
  const menu = page.getByTestId("user-menu-name");
  for (let attempt = 0; attempt < 5; attempt++) {
    await signInLocally(page, login, password);
    await expect(menu.or(tooMany)).toBeVisible();
    if (await menu.isVisible()) {
      return;
    }
    const seconds = Number(TOO_MANY_ATTEMPTS.exec((await tooMany.textContent()) ?? "")?.[1] ?? "1");
    await page.waitForTimeout(seconds * 1000 + 200);
  }
  await expect(menu).toBeVisible();
}

/** Signs in as the development Admin through the local form, retrying after sign-in throttling. */
export async function signInAdmin(page: Page): Promise<void> {
  await signIn(page, ADMIN_LOGIN, ADMIN_PASSWORD);
  await expect(page.getByTestId("user-menu-name")).toHaveText("admin");
}

/**
 * The API as the development Admin. A spec that refuses sign-ins may leave the source address throttled for a moment,
 * so a refused sign-in is tried again shortly.
 */
export async function adminApi(): Promise<Api> {
  for (let attempt = 0; attempt < 10; attempt++) {
    try {
      return await Api.signIn(ADMIN_LOGIN, ADMIN_PASSWORD);
    } catch {
      await new Promise((done) => setTimeout(done, 1500));
    }
  }
  return Api.signIn(ADMIN_LOGIN, ADMIN_PASSWORD);
}

/** Opens the Profile from the user menu, the only way to it besides its address; name is its text in the language shown. */
export async function openProfile(page: Page, name = "Profile"): Promise<void> {
  await page.getByTestId("user-menu-name").click();
  await page.getByRole("menuitem", { name, exact: true }).click();
}

/** Saves a screenshot for the pull request next to the other test output. */
export async function shot(page: Page, name: string): Promise<void> {
  const dir = process.env.MUSTER_E2E_SCREENSHOTS;
  if (dir !== undefined && dir !== "") {
    await page.screenshot({ path: `${dir}/${name}.png`, fullPage: true });
  }
}

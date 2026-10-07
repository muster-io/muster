// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Helpers of the end-to-end specs: the API as the development Admin, the database of the run, the fake IdP of
// `muster dev`, TOTP codes and the layout checks.

import { createHmac } from "node:crypto";

import { expect, request, type APIRequestContext, type Page } from "@playwright/test";
import { Client, type QueryResult } from "pg";

export const APP = "http://localhost:8080";
export const FAKE_IDP = "http://127.0.0.1:18090";
export const ADMIN_LOGIN = "admin@example.org";
/** The control API of the fake Alertmanager of `muster dev`. */
export const FAM = "http://127.0.0.1:19093/_fake";
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

/** Saves a screenshot for the pull request next to the other test output. */
export async function shot(page: Page, name: string): Promise<void> {
  const dir = process.env.MUSTER_E2E_SCREENSHOTS;
  if (dir !== undefined && dir !== "") {
    await page.screenshot({ path: `${dir}/${name}.png`, fullPage: true });
  }
}

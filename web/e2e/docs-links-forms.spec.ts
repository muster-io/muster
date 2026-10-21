// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The documentation links next to complex fields and problems (C-21.FR-11, C-21.AC-8) against `muster dev`, a
// development build whose documentation version is "latest", and its fakes: "Learn more" beside the Matchers, the
// Group key and the message template of a new Route and beside the event mode and the tokens of a new Slack Connection;
// "How to fix" beside a Slack token the check refuses, a Telegram channel whose comments the fake turned off, a
// Destination the fake Mattermost server's faults made Broken — on its page, on its Route and in the Delivery section
// of its Alert Group —, a Snapshot the fake Alertmanager truncated and an Integration without a Heartbeat. A check
// that passes and an empty state have none; the links read «Подробнее» and «Как исправить» in Russian; the Route
// editor, the Slack create form and the failed Telegram check keep no horizontal scrollbar at 360 px. A link is
// compared by the origin, the path and the anchor of its address.

import { type Browser, type Locator, type Page, expect, test } from "@playwright/test";

import {
  type Api,
  adminApi,
  advance,
  expectNoHorizontalScroll,
  fakeIntegration,
  fam,
  notify,
  removeIntegration,
  signIn,
  signInAdmin,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

const DOCS_ORIGIN = "https://muster-io.github.io";
const DOCS_PATH = "/muster/latest/";
const FMM = "http://127.0.0.1:18065/_fake";
const FTG = "http://127.0.0.1:18081/_fake";
const SERVER_URL = "http://127.0.0.1:18065";
const CHANNEL_ID = -1001000000001;
const GROUP_ID = -1001000000002;
const TEAM = "dlf-team";
const INTEGRATION = "dlf-ui";
const ROUTE = "dlf";
const CONNECTION = "dlf-mm";
const DESTINATIONS = ["dlf-alerts", "dlf-telegram", "dlf-template"];
const ROUTES = [ROUTE, "dlf-template"];
const FWH = "http://127.0.0.1:18093";
const RU_ADMIN = "dlf-admin-ru";
const NO_COMMENTS = "Comments are not enabled for this channel.";

const NOBODY = { everyone: "none", user_ids: [], groups: [] };
const MENTIONS = Object.fromEntries(
  ["new_alert_group", "new_alerts", "reopen", "ack_timeout", "snooze_ended", "rise_to_urgent"].map(
    (k) => [k, NOBODY],
  ),
);

interface Named {
  id: string;
  name: string;
}

const ids = {
  integration: "",
  route: "",
  alerts: "",
  telegram: "",
  ruAdmin: "",
};

/** A link to the documentation by its accessible name, which says what it is about and that it opens a new tab. */
function docsLink(scope: Page | Locator, kind: string, about: string): Locator {
  return scope.getByRole("link", { name: `${kind}: ${about} (opens in a new tab)`, exact: true });
}

/** The link opens the page and section of the running version in a new tab. */
async function expectTarget(link: Locator, path: string, hash = ""): Promise<void> {
  await expect(link).toBeVisible();
  const href = await link.getAttribute("href");
  const url = new URL(href ?? "");
  expect(url.origin, href ?? "").toBe(DOCS_ORIGIN);
  expect(url.pathname, href ?? "").toBe(DOCS_PATH + path);
  expect(url.hash, href ?? "").toBe(hash);
  await expect(link).toHaveAttribute("target", "_blank");
  const rel = (await link.getAttribute("rel")) ?? "";
  expect(rel.split(" ")).toEqual(expect.arrayContaining(["noopener", "noreferrer"]));
}

/** The "How to fix" links of a page or a part of it, whatever they are about. */
function fixLinks(scope: Page | Locator): Locator {
  return scope.getByRole("link", { name: /^How to fix: / });
}

/** Every documentation link of a page: the Guide, "Learn more" and "How to fix". */
function anyDocsLink(page: Page): Locator {
  return page.getByRole("link", {
    name: /^(Guide|Learn more|How to fix): .* \(opens in a new tab\)$/,
  });
}

async function expectNarrow(page: Page, what: string): Promise<void> {
  await expectNoHorizontalScroll(page);
  const width = await page.evaluate(() => document.documentElement.scrollWidth);
  expect(width, what).toBeLessThanOrEqual(360);
}

async function fakeTelegram(method: string, path: string, body?: unknown): Promise<void> {
  const res = await fetch(`${FTG}${path}`, {
    method,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  expect(res.ok, `${method} ${path}: ${res.status} ${await res.text()}`).toBe(true);
}

/** Comments on the Telegram channel: on with its discussion group, or off. */
async function comments(on: boolean): Promise<void> {
  await fakeTelegram("PUT", `/chats/${CHANNEL_ID}`, { linked_chat_id: on ? GROUP_ID : 0 });
}

async function health(id: string): Promise<string> {
  const admin = await adminApi();
  try {
    return (await admin.call<{ health: { state: string } }>("GET", `/api/v1/destinations/${id}`))
      .health.state;
  } finally {
    await admin.dispose();
  }
}

/** The Alert Group of the Alert with the label cluster. */
async function groupOf(cluster: string): Promise<string> {
  const admin = await adminApi();
  try {
    const page = await admin.call<{ items: { alert_group?: { id: string } }[] }>(
      "GET",
      `/api/v1/integrations/${ids.integration}/alerts?label=${encodeURIComponent(`cluster="${cluster}"`)}`,
    );
    const group = page.items[0]?.alert_group;
    expect(group, `the alert group of ${cluster}`).toBeDefined();
    return group?.id ?? "";
  } finally {
    await admin.dispose();
  }
}

/** Deletes what an earlier run left: the Route, the Destinations, the Connection and the Russian Admin. */
async function cleanUp(admin: Api): Promise<void> {
  const routes = await admin.call<{ items: Named[] }>("GET", "/api/v1/routes");
  for (const r of routes.items.filter((x) => ROUTES.includes(x.name))) {
    await admin.call("POST", `/api/v1/routes/${r.id}/move-open-alert-groups`);
    await admin.call("DELETE", `/api/v1/routes/${r.id}`);
  }
  const destinations = await admin.call<{ items: Named[] }>(
    "GET",
    "/api/v1/destinations?limit=500",
  );
  for (const d of destinations.items.filter((x) => DESTINATIONS.includes(x.name))) {
    await admin.call("DELETE", `/api/v1/destinations/${d.id}`);
  }
  const connections = await admin.call<{ items: Named[] }>("GET", "/api/v1/connections?limit=500");
  for (const c of connections.items.filter((x) => x.name === CONNECTION)) {
    await admin.call("DELETE", `/api/v1/connections/${c.id}`);
  }
  const users = await admin.call<{ items: (Named & { login: string })[] }>(
    "GET",
    "/api/v1/users?limit=500",
  );
  for (const u of users.items.filter((x) => x.login === RU_ADMIN)) {
    await admin.call("DELETE", `/api/v1/users/${u.id}`);
  }
}

test.beforeAll(async () => {
  test.setTimeout(120_000);
  await fetch(`${FMM}/faults`, { method: "DELETE" });
  await comments(true);
  const admin = await adminApi();
  try {
    await cleanUp(admin);
    await removeIntegration(admin, INTEGRATION);
    const connection = await admin.call<{ id: string }>("POST", "/api/v1/connections", {
      type: "mattermost",
      name: CONNECTION,
      server_url: SERVER_URL,
      bot_token: "mm-dev-token",
      proxy: { enabled: false },
      limiter: { limit: 50, per_seconds: 1 },
    });
    ids.alerts = (
      await admin.call<{ destination: Named }>("POST", "/api/v1/destinations", {
        type: "mattermost",
        name: "dlf-alerts",
        connection_id: connection.id,
        team_id: "team-dev",
        channel_id: "ch-alerts-prod",
        mentions: MENTIONS,
        limiter: { limit: 50, per_seconds: 1 },
      })
    ).destination.id;
    const connections = await admin.call<{ items: Named[] }>(
      "GET",
      "/api/v1/connections?limit=500",
    );
    ids.telegram = (
      await admin.call<{ destination: Named }>("POST", "/api/v1/destinations", {
        type: "telegram",
        name: "dlf-telegram",
        connection_id: connections.items.find((c) => c.name === "Dev Telegram")?.id,
        channel_id: "@muster_alerts",
        mentions: MENTIONS,
        limiter: { limit: 30, per_seconds: 1 },
      })
    ).destination.id;
    const profiles = await admin.call<{ items: { id: string; policy: unknown }[] }>(
      "GET",
      "/api/v1/route-profiles",
    );
    ids.route = (
      await admin.call<{ id: string }>("POST", "/api/v1/routes", {
        name: ROUTE,
        matchers: [{ label: "team", op: "=", value: TEAM }],
        urgent: false,
        group_key: ["alertname", "cluster"],
        destination_ids: [ids.alerts],
        policy: profiles.items.find((p) => p.id === "on_call")?.policy,
      })
    ).id;
    const ru = await admin.createUser(RU_ADMIN, "admin");
    ids.ruAdmin = ru.id;
    await admin.call("POST", "/api/v1/password-setups", {
      token: ru.token,
      password: "dlf-admin-ru-1",
    });
  } finally {
    await admin.dispose();
  }
  ids.integration = await fakeIntegration(INTEGRATION);
});

test.afterAll(async () => {
  await fetch(`${FMM}/faults`, { method: "DELETE" });
  await comments(true);
  const admin = await adminApi();
  try {
    await cleanUp(admin);
  } finally {
    await admin.dispose();
  }
});

/** Routes → New → On-call: the editor of a new Route. */
async function newRoute(page: Page): Promise<void> {
  await page.goto("/routes/new");
  await expectTarget(
    docsLink(page, "Learn more", "Route profiles"),
    "concepts/routes-and-group-keys/",
    "#route-profiles",
  );
  await page.getByTestId("route-profile-on_call").click();
  await expect(page.getByRole("group", { name: "Matchers", exact: true })).toBeVisible();
}

test("the Route editor has Learn more beside its complex fields", async ({ page, browser }) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  await newRoute(page);
  for (const [about, path, hash] of [
    ["Matchers", "concepts/routes-and-group-keys/", "#matchers"],
    ["Group key", "concepts/routes-and-group-keys/", "#group-keys"],
    ["Urgent", "concepts/routes-and-group-keys/", "#urgent"],
    ["Message templates", "configure/messages-and-templates/", "#the-template-language"],
    ["Loud and Quiet delivery", "concepts/loud-and-quiet/", ""],
    [
      "Ack timeout and Reminders",
      "concepts/statuses-and-commands/",
      "#ack-timeouts-reminders-and-auto-unacknowledge",
    ],
    ["Snooze durations", "concepts/statuses-and-commands/", "#snooze-presets"],
    ["Lifecycle of an Alert Group", "concepts/alert-groups/", "#reopening"],
  ] as const) {
    await expectTarget(docsLink(page, "Learn more", about), path, hash);
  }
  // The sections keep their names: the link is beside the title, not part of it.
  await expect(page.getByRole("group", { name: "Message", exact: true })).toBeVisible();
  await expect(fixLinks(page)).toHaveCount(0);
  expect(csp).toEqual([]);

  await narrow(browser, async (phone) => {
    await newRoute(phone);
    await expect(docsLink(phone, "Learn more", "Group key")).toBeVisible();
    await expectNarrow(phone, "the Route editor");
  });
});

/** Connections → New → Slack. */
async function newSlack(page: Page): Promise<void> {
  await page.goto("/connections/new");
  await page.getByRole("radio", { name: "Slack", exact: true }).check();
  await expect(page.getByTestId("slack-manifest-hint")).toHaveText(
    "Create the Slack app from the manifest first, then install it and copy its tokens. Learn more",
  );
}

async function wrongSlackToken(page: Page): Promise<void> {
  await page
    .getByTestId("secret-connection-bot-token")
    .getByRole("button", { name: /^(Set a value|Replace)$/ })
    .click();
  await page.getByLabel("Bot token", { exact: true }).fill("xoxb-wrong");
  await page.getByRole("button", { name: "Check connection" }).click();
  await expect(page.getByTestId("connection-check-summary")).toHaveText("The check failed.");
  const token = page.getByTestId("connection-check-step").first();
  await expect(token.getByTestId("connection-check-step-reason")).toHaveText(
    "The bot token is not valid.",
  );
  await expectTarget(
    docsLink(token, "How to fix", "The token is not valid"),
    "messengers/slack/",
    "#install-the-app-and-create-the-tokens",
  );
}

test("a new Slack Connection links its event mode and tokens, and a refused token to How to fix", async ({
  page,
  browser,
}) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  await newSlack(page);
  await expectTarget(
    docsLink(page, "Learn more", "Slack app manifest"),
    "messengers/slack/",
    "#create-the-app-from-a-manifest",
  );
  await expectTarget(
    docsLink(page, "Learn more", "Slack event mode"),
    "messengers/slack/",
    "#socket-mode-or-http",
  );
  for (const about of ["Slack bot token", "Slack app-level token"]) {
    await expectTarget(
      docsLink(page, "Learn more", about),
      "messengers/slack/",
      "#install-the-app-and-create-the-tokens",
    );
  }
  await expectTarget(
    docsLink(page, "Learn more", "Proxy"),
    "messengers/proxies/",
    "#where-you-set-a-proxy",
  );
  // The event mode keeps its name: the link is beside its title.
  await expect(page.getByRole("group", { name: "Event mode", exact: true })).toBeVisible();
  await wrongSlackToken(page);
  // Only the step that failed has one.
  await expect(fixLinks(page)).toHaveCount(1);
  expect(csp).toEqual([]);

  await narrow(browser, async (phone) => {
    await newSlack(phone);
    await wrongSlackToken(phone);
    await expectNarrow(phone, "the Slack create form");
  });
});

async function failedTelegramCheck(page: Page): Promise<void> {
  await page.goto(`/destinations/${ids.telegram}`);
  await page.getByRole("button", { name: "Check", exact: true }).click();
  await expect(page.getByTestId("destination-check-summary")).toHaveText("Check failed");
  const item = page
    .getByTestId("destination-check-item")
    .filter({ hasText: /^Discussion group: / });
  await expect(item.getByTestId("destination-check-item-reason")).toContainText(NO_COMMENTS);
  await expectTarget(
    docsLink(item, "How to fix", "Comments are not enabled"),
    "messengers/telegram/",
    "#enable-comments-on-the-channel",
  );
}

test("a Telegram channel without comments fails its check with How to fix", async ({
  page,
  browser,
}) => {
  const csp = watchCsp(page);
  await comments(false);
  try {
    await signInAdmin(page);
    await failedTelegramCheck(page);
    // The checks that passed have none.
    await expect(
      fixLinks(page.getByTestId("destination-check-item").filter({ hasText: /: ok$/ })),
    ).toHaveCount(0);
    await narrow(browser, async (phone) => {
      await failedTelegramCheck(phone);
      await expectNarrow(phone, "the failed Telegram check");
    });
  } finally {
    await comments(true);
  }
  expect(csp).toEqual([]);
});

test("a Broken Destination links to its recovery on its page, on its Route and on its Alert Group", async ({
  page,
}) => {
  test.setTimeout(90_000);
  const csp = watchCsp(page);
  // The fake Mattermost server refuses every new post with 403, which makes the Destination Broken.
  const res = await fetch(`${FMM}/faults`, {
    method: "POST",
    body: JSON.stringify({
      path: "/api/v4/posts",
      status: 403,
      body: '{"id":"api.context.permissions.app_error","message":"You do not have the appropriate permissions.","status_code":403}',
      times: 1,
    }),
  });
  expect(res.ok, `POST /_fake/faults: ${res.status}`).toBe(true);
  try {
    await fam("PUT", "/groups/dlf", {
      receiver: INTEGRATION,
      route: "{}",
      labels: { alertname: "Dlf" },
    });
    await fam("PUT", "/groups/dlf/alerts/b1", { labels: { team: TEAM, cluster: "b1" } });
    await notify(ids.integration, "dlf", { reason: "first notification" });
    await expect.poll(() => health(ids.alerts), { timeout: 30_000 }).toBe("broken");

    await signInAdmin(page);
    await page.goto(`/destinations/${ids.alerts}`);
    const banner = page.getByTestId("broken-banner");
    await expect(banner).toContainText("Broken since");
    await expectTarget(
      docsLink(banner, "How to fix", "Broken Destination"),
      "concepts/broken-destinations/",
      "#recovery",
    );

    await page.goto(`/routes/${ids.route}`);
    const onRoute = page.getByTestId("route-destination").getByTestId("broken-banner");
    await expect(onRoute).toContainText("Broken since");
    await expectTarget(
      docsLink(onRoute, "How to fix", "Broken Destination"),
      "concepts/broken-destinations/",
      "#recovery",
    );

    await page.goto(`/alert-groups/${await groupOf("b1")}`);
    const row = page.getByTestId("alert-group-deliveries").getByTestId("delivery-row");
    await expect(row.getByTestId("delivery-state")).toHaveText(
      /^(Not delivered: .*|Waiting: dlf-alerts is Broken)$/,
    );
    await expectTarget(fixLinks(row), "concepts/broken-destinations/", "#recovery");
    await expect(fixLinks(row)).toHaveCount(1);
  } finally {
    await fetch(`${FMM}/faults`, { method: "DELETE" });
  }
  expect(csp).toEqual([]);
});

test("a truncated Snapshot and an Integration without a Heartbeat link to How to fix", async ({
  page,
}) => {
  const csp = watchCsp(page);
  await fam("PUT", "/groups/dlf-trunc", {
    receiver: INTEGRATION,
    route: "{}",
    labels: { alertname: "DlfTrunc" },
  });
  for (const pod of ["p1", "p2", "p3"]) {
    await fam("PUT", `/groups/dlf-trunc/alerts/${pod}`, { labels: { team: "dlf-other", pod } });
  }
  await notify(ids.integration, "dlf-trunc", { reason: "first notification", max_alerts: 1 });

  await signInAdmin(page);
  await page.goto(`/integrations/${ids.integration}`);
  const warning = page
    .getByTestId("integration-warning")
    .filter({ hasText: "Alertmanager truncates Snapshots" });
  await expectTarget(
    docsLink(warning, "How to fix", "Truncated Snapshots"),
    "integrations/alertmanager/",
    "#send-every-alert-of-the-group",
  );
  const heartbeat = page.getByTestId("heartbeat-banner");
  await expect(heartbeat).toHaveAttribute("data-kind", "heartbeat_not_configured");
  await expectTarget(
    docsLink(heartbeat, "How to fix", "No Heartbeat"),
    "integrations/heartbeat/",
    "#turn-it-on",
  );
  await expectTarget(
    docsLink(page, "Learn more", "Static labels"),
    "integrations/alertmanager/",
    "#static-labels",
  );
  expect(csp).toEqual([]);

  // An untruncated Snapshot ends the truncation and resolves its Internal alert, which the later specs do not expect.
  await advance(60);
  await notify(ids.integration, "dlf-trunc", { reason: "repeat interval elapsed" });
  await expect.poll(() => firingTruncation(), { timeout: 30_000 }).toBe(0);
});

/** The firing MusterSnapshotTruncated alerts of the Integration of this spec, in the built-in Integration. */
async function firingTruncation(): Promise<number> {
  const admin = await adminApi();
  try {
    const integrations = await admin.call<{ items: (Named & { builtin: boolean })[] }>(
      "GET",
      "/api/v1/integrations?limit=500",
    );
    const builtin = integrations.items.find((i) => i.builtin)?.id ?? "";
    const label = encodeURIComponent(`integration_name="${INTEGRATION}"`);
    const alerts = await admin.call<{ items: { state: string }[] }>(
      "GET",
      `/api/v1/integrations/${builtin}/alerts?label=${label}`,
    );
    return alerts.items.filter((a) => a.state === "firing").length;
  } finally {
    await admin.dispose();
  }
}

/** The firing MusterTemplateError alerts of a Destination, in the built-in Integration. */
async function firingTemplateErrors(admin: Api, destination: string): Promise<number> {
  const integrations = await admin.call<{ items: (Named & { builtin: boolean })[] }>(
    "GET",
    "/api/v1/integrations?limit=500",
  );
  const builtin = integrations.items.find((i) => i.builtin)?.id ?? "";
  const alerts = await admin.call<{ items: { state: string; labels: Record<string, string> }[] }>(
    "GET",
    `/api/v1/integrations/${builtin}/alerts?state=firing&label=${encodeURIComponent('alertname="MusterTemplateError"')}`,
  );
  return alerts.items.filter(
    (a) =>
      a.state === "firing" &&
      a.labels.alertname === "MusterTemplateError" &&
      Object.values(a.labels).includes(destination),
  ).length;
}

test("a Destination whose template keeps failing links its banner to the runbook", async ({
  page,
}) => {
  test.setTimeout(90_000);
  const csp = watchCsp(page);
  const admin = await adminApi();
  let destination = "";
  try {
    // The create request keeps no id, which the update request reads: the update after the acknowledgement, whose body
    // names the new status, is not sent.
    destination = (
      await admin.call<{ destination: Named }>("POST", "/api/v1/destinations", {
        type: "webhook",
        name: "dlf-template",
        mode: "template",
        template: {
          create: {
            method: "POST",
            url: `${FWH}/chat/dlf/messages`,
            headers: [],
            body: '{"status":{{ .AlertGroup.Status | toJson }}}',
            extract: [{ name: "id", path: "$.nothing.here" }],
          },
          update: {
            method: "PUT",
            url: `${FWH}/chat/dlf/messages/{{ .Response.id }}`,
            headers: [],
            body: '{"status":{{ .AlertGroup.Status | toJson }}}',
          },
        },
        mentions: MENTIONS,
        proxy: { enabled: false },
        limiter: { limit: 50, per_seconds: 1 },
      })
    ).destination.id;
    const profiles = await admin.call<{ items: { id: string; policy: unknown }[] }>(
      "GET",
      "/api/v1/route-profiles",
    );
    await admin.call("POST", "/api/v1/routes", {
      name: "dlf-template",
      matchers: [{ label: "team", op: "=", value: "dlf-template" }],
      urgent: false,
      group_key: ["alertname", "cluster"],
      destination_ids: [destination],
      policy: profiles.items.find((p) => p.id === "on_call")?.policy,
    });
    await fam("PUT", "/groups/dlf-tpl", {
      receiver: INTEGRATION,
      route: "{}",
      labels: { alertname: "DlfTemplate" },
    });
    await fam("PUT", "/groups/dlf-tpl/alerts/t1", {
      labels: { team: "dlf-template", cluster: "t1" },
    });
    await notify(ids.integration, "dlf-tpl", { reason: "first notification" });
    const group = await groupOf("t1");
    await expect
      .poll(async () => {
        const deliveries = await admin.call<{ items: { state: string }[] }>(
          "GET",
          `/api/v1/alert-groups/${group}/deliveries`,
        );
        return deliveries.items[0]?.state;
      })
      .toBe("delivered");
    await admin.call("POST", `/api/v1/alert-groups/${group}/acknowledge`);
    await expect
      .poll(
        async () =>
          (
            await admin.call<{ template_error?: { fallback: string } }>(
              "GET",
              `/api/v1/destinations/${destination}`,
            )
          ).template_error?.fallback,
        { timeout: 30_000 },
      )
      .toBe("not_sent");

    await signInAdmin(page);
    await page.goto(`/destinations/${destination}`);
    const banner = page.getByTestId("template-error-banner");
    await expect(banner).toContainText("Template error since");
    await expect(banner).toContainText("Requests are not sent.");
    const link = docsLink(banner, "How to fix", "Template error");
    await expect(link).toHaveText("How to fix");
    const href = new URL((await link.getAttribute("href")) ?? "");
    expect(href.origin).toBe(DOCS_ORIGIN);
    expect(href.pathname).toBe("/muster/latest/operations/runbooks/MusterTemplateError/");
    expect(href.hash).toBe("");
    expect(csp).toEqual([]);
  } finally {
    // Deleting the Destination resolves its MusterTemplateError, which the later specs do not expect.
    const routes = await admin.call<{ items: Named[] }>("GET", "/api/v1/routes");
    for (const r of routes.items.filter((x) => x.name === "dlf-template")) {
      await admin.call("POST", `/api/v1/routes/${r.id}/move-open-alert-groups`);
      await admin.call("DELETE", `/api/v1/routes/${r.id}`);
    }
    if (destination !== "") {
      await admin.call("DELETE", `/api/v1/destinations/${destination}`);
      await expect
        .poll(() => firingTemplateErrors(admin, "dlf-template"), { timeout: 30_000 })
        .toBe(0);
    }
    await admin.dispose();
  }
});

test("a check that passes and an empty state show no link of their own", async ({ page }) => {
  await signInAdmin(page);
  const admin = await adminApi();
  let telegram = "";
  try {
    const connections = await admin.call<{ items: Named[] }>(
      "GET",
      "/api/v1/connections?limit=500",
    );
    telegram = connections.items.find((c) => c.name === "Dev Telegram")?.id ?? "";
  } finally {
    await admin.dispose();
  }
  await page.goto(`/connections/${telegram}`);
  await page.getByRole("button", { name: "Check connection" }).click();
  await expect(page.getByTestId("connection-check-summary")).toHaveText(/^Connected as /);
  await expect(page.getByTestId("connection-check-step")).toHaveText([
    /^Dry probe without the token: ok /,
    /^getMe: ok /,
    /^getWebhookInfo: ok /,
  ]);
  // The check has none; the http warning of the base URL above it has its own.
  await expect(fixLinks(page.getByTestId("connection-check-result"))).toHaveCount(0);
  await expect(fixLinks(page)).toHaveCount(1);
  await expectTarget(
    docsLink(page.getByTestId("telegram-http-warning"), "How to fix", "Clear text over http"),
    "messengers/telegram-restricted-networks/",
  );

  await page.goto("/alert-groups?q=no-such-alert-group-dlf");
  await expect(page.getByText("No Alert Groups match these filters.")).toBeVisible();
  await expect(anyDocsLink(page)).toHaveCount(1);
  await expect(docsLink(page, "Guide", "Alert Groups")).toBeVisible();
});

test("the links read «Подробнее» and «Как исправить» in Russian", async ({ browser }) => {
  const context = await browser.newContext();
  try {
    await russian(await context.newPage());
  } finally {
    await context.close();
  }
});

async function russian(page: Page): Promise<void> {
  await signIn(page, RU_ADMIN, "dlf-admin-ru-1");
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();

  await page.goto("/routes/new");
  await page.getByTestId("route-profile-on_call").click();
  const matchers = page.getByRole("link", {
    name: "Подробнее: Матчеры (откроется в новой вкладке)",
    exact: true,
  });
  await expect(matchers).toHaveText("Подробнее");
  await expectTarget(matchers, "concepts/routes-and-group-keys/", "#matchers");

  await page.goto(`/integrations/${ids.integration}`);
  const fix = page.getByTestId("heartbeat-banner").getByRole("link", {
    name: "Как исправить: Нет Heartbeat (откроется в новой вкладке)",
    exact: true,
  });
  await expect(fix).toHaveText("Как исправить");
  await expectTarget(fix, "integrations/heartbeat/", "#turn-it-on");
}

/** Runs the steps in a new context at 360 px wide, signed in as the Admin. */
async function narrow(browser: Browser, steps: (page: Page) => Promise<void>): Promise<void> {
  const context = await browser.newContext({ viewport: { width: 360, height: 740 } });
  const page = await context.newPage();
  const csp = watchCsp(page);
  await signInAdmin(page);
  await steps(page);
  expect(csp).toEqual([]);
  await context.close();
}

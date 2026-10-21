// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The documentation links next to the page titles (C-21.FR-11, C-21.AC-8) against `muster dev`, a development build
// whose documentation version is "latest": every page of Alert Groups, Integrations, Routes, Connections and
// Destinations has the Guide link to its row of reference.md, a Connection or a Destination the one of its type or
// mode; a link opens the page in a new tab, which the spec answers itself, so that the run needs no network, and which
// receives no Referer; nothing is loaded from the site before; the pages outside an active, full session (sign-in,
// password setup, the TOTP enrolment and prompt), the page about a missing Permission and the empty states have none;
// the link reads «Руководство» in Russian; and the pages keep no horizontal scrollbar at 360 px.

import { type Browser, type BrowserContext, type Page, expect, test } from "@playwright/test";

import {
  type Api,
  adminApi,
  expectNoHorizontalScroll,
  fakeIntegration,
  fam,
  freshCode,
  notify,
  signIn,
  signInAdmin,
  signInLocally,
  shownSecret,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

const SITE = "https://muster-io.github.io/muster";
const DOCS = `${SITE}/latest/`;
const FWH = "http://127.0.0.1:18093";
const NONE = { everyone: "none", user_ids: [], groups: [] };
const MENTIONS = {
  new_alert_group: NONE,
  new_alerts: NONE,
  reopen: NONE,
  ack_timeout: NONE,
  snooze_ended: NONE,
  rise_to_urgent: NONE,
};
const DESTINATIONS = ["docs-telegram", "docs-slack", "docs-template", "docs-events"];

interface Named {
  id: string;
  name: string;
}

const ids = {
  integration: "",
  alertGroup: "",
  snapshot: "",
  route: "",
  connections: {} as Record<string, string>,
  destinations: {} as Record<string, string>,
  ruAdmin: "",
};

/** The site's pages the context opened, with whether the request carried a Referer. */
interface SiteRequest {
  url: string;
  referer: string | undefined;
}

/** Answers the documentation site inside the context, so that nothing leaves the machine; records the requests. */
async function answerSite(context: BrowserContext): Promise<SiteRequest[]> {
  const requests: SiteRequest[] = [];
  await context.route(`${SITE}/**`, async (route) => {
    const headers = await route.request().allHeaders();
    requests.push({ url: route.request().url(), referer: headers.referer });
    await route.fulfill({
      status: 200,
      contentType: "text/html",
      body: "<!doctype html><title>Muster documentation</title><h1>Documentation</h1>",
    });
  });
  return requests;
}

/** The links of a page to the documentation site. */
function siteLinks(page: Page) {
  return page.locator(`a[href^="${SITE}"]`);
}

/** The Guide link beside the title, by its accessible name. */
function guide(page: Page, about: string) {
  return page.getByRole("link", { name: `Guide: ${about} (opens in a new tab)`, exact: true });
}

/** The page shows its title with exactly one Guide link, to target, beside it. */
async function expectGuide(
  page: Page,
  title: string | RegExp,
  about: string,
  target: string,
): Promise<void> {
  const heading = page.getByRole("heading", { level: 1, name: title, exact: true });
  await expect(heading).toBeVisible();
  const link = guide(page, about);
  await expect(link).toHaveText("Guide");
  await expect(link).toHaveAttribute("href", DOCS + target);
  await expect(link).toHaveAttribute("target", "_blank");
  const rel = (await link.getAttribute("rel")) ?? "";
  expect(rel.split(" ")).toEqual(expect.arrayContaining(["noopener", "noreferrer"]));
  await expect(page.getByTestId("page-title").filter({ has: heading })).toContainText("Guide");
  // One Guide; the fields and problems of a page add their own "Learn more" and "How to fix".
  await expect(page.getByRole("link", { name: /^Guide: / })).toHaveCount(1);
}

test.beforeAll(async () => {
  test.setTimeout(120_000);
  ids.integration = await fakeIntegration("docs-links");
  await fam("PUT", "/groups/dl", {
    receiver: "docs-links",
    route: "{}",
    labels: { alertname: "DocsLinks" },
  });
  await fam("PUT", "/groups/dl/alerts/a1", {
    labels: { alertname: "DocsLinks", instance: "a1" },
    annotations: { summary: "Documentation links" },
  });
  await notify(ids.integration, "dl", { reason: "first notification" });

  const admin = await adminApi();
  try {
    await removeDestinations(admin);
    const alerts = await admin.call<{ items: { alert_group?: { id: string } }[] }>(
      "GET",
      `/api/v1/integrations/${ids.integration}/alerts`,
    );
    ids.alertGroup = alerts.items[0]?.alert_group?.id ?? "";
    expect(ids.alertGroup, "the Alert Group of the spec").not.toBe("");
    const snapshots = await admin.call<{ items: Named[] }>(
      "GET",
      `/api/v1/stored-snapshots?integration=${ids.integration}`,
    );
    ids.snapshot = snapshots.items[0]?.id ?? "";
    expect(ids.snapshot, "the Stored Snapshot of the spec").not.toBe("");
    const routes = await admin.call<{ items: (Named & { is_default: boolean })[] }>(
      "GET",
      "/api/v1/routes?limit=500",
    );
    ids.route = routes.items.find((r) => r.is_default)?.id ?? "";
    const connections = await admin.call<{ items: Named[] }>(
      "GET",
      "/api/v1/connections?limit=500",
    );
    for (const c of connections.items) {
      ids.connections[c.name] = c.id;
    }
    for (const name of ["Dev Mattermost", "Dev Telegram", "Dev Slack", "Dev Bridge"]) {
      expect(ids.connections[name], `the demo Connection ${name}`).toBeDefined();
    }

    const create = async (body: Record<string, unknown>) =>
      (
        await admin.call<{ destination: Named }>("POST", "/api/v1/destinations", {
          mentions: MENTIONS,
          ...body,
        })
      ).destination.id;
    ids.destinations.telegram = await create({
      type: "telegram",
      name: "docs-telegram",
      connection_id: ids.connections["Dev Telegram"],
      channel_id: "@muster_alerts",
      limiter: { limit: 30, per_seconds: 1 },
    });
    ids.destinations.slack = await create({
      type: "slack",
      name: "docs-slack",
      connection_id: ids.connections["Dev Slack"],
      channel_id: "C0FAKEALERTS",
      limiter: { limit: 1, per_seconds: 1 },
    });
    ids.destinations.template = await create({
      type: "webhook",
      name: "docs-template",
      mode: "template",
      template: {
        create: { method: "POST", url: `${FWH}/docs/messages`, headers: [], body: "{}" },
        update: { method: "PUT", url: `${FWH}/docs/messages/1`, headers: [], body: "{}" },
      },
      proxy: { enabled: false },
      limiter: { limit: 50, per_seconds: 1 },
    });
    ids.destinations.events = await create({
      type: "webhook",
      name: "docs-events",
      mode: "events",
      events: { url: `${FWH}/docs/events`, headers: [] },
      proxy: { enabled: false },
      limiter: { limit: 50, per_seconds: 1 },
    });

    const viewer = await admin.createUser("docs-viewer", "viewer");
    await admin.call("POST", "/api/v1/password-setups", {
      token: viewer.token,
      password: "docs-viewer-1",
    });
    const ru = await admin.createUser("docs-admin-ru", "admin");
    ids.ruAdmin = ru.id;
    await admin.call("POST", "/api/v1/password-setups", {
      token: ru.token,
      password: "docs-admin-ru-1",
    });
  } finally {
    await admin.dispose();
  }
});

test.afterAll(async () => {
  const admin = await adminApi();
  try {
    await removeDestinations(admin);
    if (ids.ruAdmin !== "") {
      await admin.call("DELETE", `/api/v1/users/${ids.ruAdmin}`);
    }
  } finally {
    await admin.dispose();
  }
});

async function removeDestinations(admin: Api): Promise<void> {
  const list = await admin.call<{ items: Named[] }>("GET", "/api/v1/destinations?limit=500");
  for (const d of list.items.filter((x) => DESTINATIONS.includes(x.name))) {
    await admin.call("DELETE", `/api/v1/destinations/${d.id}`);
  }
}

test("the Guide link opens the page of the running version in a new tab without a Referer", async ({
  page,
  context,
}) => {
  const requests = await answerSite(context);
  const csp = watchCsp(page);
  await signInAdmin(page);
  await page.goto("/routes");
  await expectGuide(page, "Routes", "Routes", "concepts/routes-and-group-keys/");
  expect(requests, "nothing is loaded from the site before the link is followed").toEqual([]);
  await expect(page.locator('meta[http-equiv="x-dns-prefetch-control"]')).toHaveAttribute(
    "content",
    "off",
  );

  const opened = context.waitForEvent("page");
  await guide(page, "Routes").click();
  const docs = await opened;
  await docs.waitForLoadState();
  expect(docs.url()).toBe(`${DOCS}concepts/routes-and-group-keys/`);
  await expect(docs.getByRole("heading", { name: "Documentation" })).toBeVisible();
  expect(requests).toEqual([{ url: `${DOCS}concepts/routes-and-group-keys/`, referer: undefined }]);
  expect(await docs.evaluate(() => window.opener)).toBeNull();
  await docs.close();
  expect(csp).toEqual([]);
});

test("every page of Alert Groups, Integrations, Routes, Connections and Destinations links to its guide", async ({
  page,
  context,
}) => {
  test.setTimeout(180_000);
  const requests = await answerSite(context);
  const csp = watchCsp(page);
  await signInAdmin(page);

  await page.goto("/alert-groups");
  await expectGuide(page, "Alert Groups", "Alert Groups", "concepts/alert-groups/");
  await page.goto(`/alert-groups/${ids.alertGroup}`);
  await expectGuide(
    page,
    /DocsLinks/,
    "Alert Group statuses and commands",
    "concepts/statuses-and-commands/",
  );

  await page.goto("/integrations");
  await expectGuide(page, "Integrations", "Integrations", "integrations/alertmanager/");
  await page.goto("/integrations/new");
  await expectGuide(page, "Create integration", "Integrations", "integrations/alertmanager/");
  await page.goto(`/integrations/${ids.integration}`);
  await expectGuide(page, "docs-links", "Integrations", "integrations/alertmanager/");
  await page.goto(`/integrations/${ids.integration}/edit`);
  await expectGuide(page, "Edit integration", "Integrations", "integrations/alertmanager/");
  await page.goto(`/integrations/${ids.integration}/snapshots/${ids.snapshot}`);
  await expectGuide(page, "Stored Snapshot", "Stored Snapshots", "concepts/snapshot-semantics/");

  await page.goto("/routes/new");
  await expectGuide(page, "Create route", "Routes", "concepts/routes-and-group-keys/");
  await page.goto(`/routes/${ids.route}`);
  await expectGuide(page, "Default", "Routes", "concepts/routes-and-group-keys/");

  await page.goto("/connections");
  await expectGuide(page, "Connections", "Connections", "messengers/");
  await page.goto("/connections/new");
  await expectGuide(page, "New Connection", "Connections", "messengers/");
  for (const [name, about, target] of [
    ["Dev Telegram", "Telegram Connection", "messengers/telegram/"],
    ["Dev Mattermost", "Mattermost Connection", "messengers/mattermost/"],
    ["Dev Slack", "Slack Connection", "messengers/slack/"],
    ["Dev Bridge", "Bridge Connection", "messengers/bridge/"],
  ] as const) {
    await page.goto(`/connections/${ids.connections[name]}`);
    await expectGuide(page, name, about, target);
  }

  await page.goto("/destinations");
  await expectGuide(
    page,
    "Destinations",
    "Destinations",
    "messengers/#what-each-destination-type-can-do",
  );
  await page.goto("/destinations/new");
  await expectGuide(
    page,
    "New Destination",
    "Destinations",
    "messengers/#what-each-destination-type-can-do",
  );
  for (const [key, name, about, target] of [
    ["telegram", "docs-telegram", "Telegram Destination", "messengers/telegram/#add-a-destination"],
    [
      "slack",
      "docs-slack",
      "Slack Destination",
      "messengers/slack/#invite-the-bot-and-create-destinations",
    ],
    ["template", "docs-template", "Outgoing webhook templates", "outgoing-webhooks/templates/"],
    ["events", "docs-events", "Outgoing webhook events", "outgoing-webhooks/events/"],
  ] as const) {
    await page.goto(`/destinations/${ids.destinations[key]}`);
    await expectGuide(page, name, about, target);
  }

  // From the list: Connections → the demo Telegram Connection.
  await page.goto("/connections");
  await page.getByRole("link", { name: "Dev Telegram", exact: true }).click();
  await expect(guide(page, "Telegram Connection")).toHaveAttribute(
    "href",
    `${DOCS}messengers/telegram/`,
  );

  expect(requests, "nothing is loaded from the site while the pages are read").toEqual([]);
  expect(csp).toEqual([]);
});

test("an empty state has no link of its own", async ({ page }) => {
  await signInAdmin(page);
  await page.goto("/alert-groups?q=no-such-alert-group-docs-links");
  await expect(page.getByText("No Alert Groups match these filters.")).toBeVisible();
  await expect(siteLinks(page)).toHaveCount(1);
  await expect(guide(page, "Alert Groups")).toBeVisible();
});

test("the pages keep no horizontal scrollbar at 360 px", async ({ browser }) => {
  await narrow(browser);
});

async function narrow(browser: Browser): Promise<void> {
  const context = await browser.newContext({ viewport: { width: 360, height: 740 } });
  const page = await context.newPage();
  await answerSite(context);
  await signInAdmin(page);
  for (const [address, about] of [
    ["/routes", "Routes"],
    [`/connections/${ids.connections["Dev Telegram"]}`, "Telegram Connection"],
    [`/destinations/${ids.destinations.slack}`, "Slack Destination"],
    [`/destinations/${ids.destinations.template}`, "Outgoing webhook templates"],
    [`/alert-groups/${ids.alertGroup}`, "Alert Group statuses and commands"],
    ["/alert-groups", "Alert Groups"],
    [`/integrations/${ids.integration}`, "Integrations"],
  ] as const) {
    await page.goto(address);
    await expect(guide(page, about)).toBeVisible();
    await expectNoHorizontalScroll(page);
    const width = await page.evaluate(() => document.documentElement.scrollWidth);
    expect(width, address).toBeLessThanOrEqual(360);
  }
  await context.close();
}

test("the link reads «Руководство» in Russian", async ({ browser }) => {
  const context = await browser.newContext();
  const page = await context.newPage();
  await answerSite(context);
  await signIn(page, "docs-admin-ru", "docs-admin-ru-1");
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();
  await page.goto("/routes");
  await expect(
    page.getByRole("heading", { level: 1, name: "Маршруты", exact: true }),
  ).toBeVisible();
  const link = page.getByRole("link", {
    name: "Руководство: Маршруты (откроется в новой вкладке)",
    exact: true,
  });
  await expect(link).toHaveText("Руководство");
  await expect(link).toHaveAttribute("href", `${DOCS}concepts/routes-and-group-keys/`);
  await page.goto(`/connections/${ids.connections["Dev Telegram"]}`);
  await expect(
    page.getByRole("link", {
      name: "Руководство: Подключение Telegram (откроется в новой вкладке)",
      exact: true,
    }),
  ).toHaveText("Руководство");
  await page.goto("/routes");
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await context.close();
});

test("no link outside an active, full session: sign-in, password setup, TOTP enrolment and prompt, no Permission", async ({
  browser,
}) => {
  const context = await browser.newContext();
  const page = await context.newPage();
  await answerSite(context);

  // Signed out: the sign-in page.
  await page.goto("/sign-in");
  await expect(page.getByLabel("Login")).toBeVisible();
  await expect(siteLinks(page)).toHaveCount(0);

  const admin = await adminApi();
  await admin.setTotpPolicy("everyone");
  try {
    const user = await admin.createUser("docs-totp");
    // The password setup page.
    await page.goto(`/password-setup#token=${user.token}`);
    await expect(page.getByLabel("New password")).toBeVisible();
    await expect(siteLinks(page)).toHaveCount(0);
    await page.getByLabel("New password").fill("docs-totp-password-1");
    await page.getByLabel("Repeat the password").fill("docs-totp-password-1");
    await page.getByRole("button", { name: "Set password" }).click();
    await page.getByRole("link", { name: "Sign in" }).click();

    // A user under a TOTP policy who has not enrolled reaches only the enrolment, which has no link.
    await signInLocally(page, "docs-totp", "docs-totp-password-1");
    await expect(page).toHaveURL(/\/totp-enrolment$/);
    await expect(
      page.getByRole("img", { name: "QR code for your authenticator app" }),
    ).toBeVisible();
    await expect(siteLinks(page)).toHaveCount(0);

    // Once enrolled, the TOTP prompt of the next sign-in has no link either.
    const secret = await shownSecret(page);
    await page.getByLabel("Code from the app").fill(await freshCode(page, secret, 0));
    await page.getByRole("button", { name: "Confirm" }).click();
    await expect(page.getByText("Save these recovery codes")).toBeVisible();
    await expect(siteLinks(page)).toHaveCount(0);
    await page.getByRole("button", { name: "Continue" }).click();
    await expect(page.getByTestId("user-menu-name")).toHaveText("docs-totp");
    await page.getByRole("button", { name: /docs-totp/ }).click();
    await page.getByRole("menuitem", { name: "Sign out" }).click();
    await signInLocally(page, "docs-totp", "docs-totp-password-1");
    await expect(page).toHaveURL(/\/sign-in\/totp$/);
    await expect(page.getByText("Enter the code from your authenticator app.")).toBeVisible();
    await expect(siteLinks(page)).toHaveCount(0);
  } finally {
    await admin.setTotpPolicy("nobody");
    await admin.dispose();
  }
  await context.close();

  // A Viewer opening a page of the Admin sees the page about the missing Permission, without a link.
  const viewerContext = await browser.newContext();
  const viewer = await viewerContext.newPage();
  await answerSite(viewerContext);
  await signIn(viewer, "docs-viewer", "docs-viewer-1");
  for (const address of [
    "/connections/new",
    "/destinations/new",
    "/routes/new",
    "/integrations/new",
  ]) {
    await viewer.goto(address);
    await expect(
      viewer.getByRole("heading", { level: 1, name: "No access", exact: true }),
    ).toBeVisible();
    await expect(siteLinks(viewer)).toHaveCount(0);
  }
  // The pages a Viewer may read do have the link.
  await viewer.goto("/routes");
  await expect(guide(viewer, "Routes")).toBeVisible();
  await viewerContext.close();
});

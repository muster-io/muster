// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Slack Destination pages against `muster dev` and its fake Slack server: Verification step 4 of their story, with
// the Request log of a Socket Mode envelope (C-24.AC-17), the Transport "Slack" in the Timeline and a Broken Slack
// Destination that "Check" ends (C-24.AC-9). On the spec's own Connection "ui", to the fake's second app in Socket Mode,
// Destinations → New → Slack lists the bot's channel "alerts" and not "other"; the channel C0FAKEOTHER, typed by hand,
// is refused with "Invite the bot to the channel: /invite @muster-dev-bot-b"; "alerts" picked from the list saves
// "#alerts"; "Check" passes with the item "Channel"; "Send test message" posts one message and says that Slack does not
// let a bot press its own button; "Preview" draws the red bar, the green "Ack", "Resolve" and "Snooze…". A press of the
// test message through the socket is recorded with Muster's confirmation and no status. The Admin's Slack account,
// linked through the API, acknowledges an Alert Group, and its Timeline names the Transport "Slack". With the bot out
// of the channel the next Alert Group makes the Destination Broken; with the bot back, "Check" ends it at once. The
// form fits 360 px; no Content Security Policy violation. The spec deletes what it made and puts the fake back.

import { type Locator, type Page, expect, test } from "@playwright/test";

import {
  type Api,
  adminApi,
  createSlackConnection,
  expectNoHorizontalScroll,
  fakeIntegration,
  fam,
  fsl,
  notify,
  removeSlackConnection,
  resetFakeSlack,
  shot,
  signInAdmin,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

const TEAM = "slack-dest";
const ROUTE = "slack-dest";
const CHANNEL = "C0FAKEALERTS";
const ALICE = "U0FAKEALICE";
const INVITE = "Invite the bot to the channel: /invite @muster-dev-bot-b";
const NOTE =
  "Slack does not let a bot press its own button, so this test does not check presses. Press a button on the test message: once the press reaches Muster, it answers that nothing was changed.";

let connectionId = "";
let destinationId = "";
let integrationId = "";
let routeId = "";
let linkId = "";
const groups: string[] = [];

interface Message {
  ts: string;
  text: string;
}

interface Named {
  id: string;
  name: string;
}

function nav(page: Page, name: string) {
  return page.getByRole("navigation", { name: "Main" }).getByRole("link", { name, exact: true });
}

/** The messages of the channel "alerts" of the fake's second app, oldest first. */
async function messages(): Promise<Message[]> {
  return fsl<Message[]>("GET", `/messages?app=b&channel=${CHANNEL}`);
}

/** Puts the bot of the fake's second app into the channel "alerts", or takes it out. */
async function member(inChannel: boolean): Promise<void> {
  await fsl("PUT", "/config", {
    channels: { b: { [CHANNEL]: { name: "alerts", member: inChannel } } },
  });
}

async function health(admin: Api): Promise<string> {
  return (
    await admin.call<{ health: { state: string } }>("GET", `/api/v1/destinations/${destinationId}`)
  ).health.state;
}

/** Links the Admin's Slack account, alice of the fake's second app, through "ui" with the code the bot sent. */
async function linkAdmin(admin: Api): Promise<void> {
  const before = (await fsl<Message[]>("GET", `/dms?user=${ALICE}&app=b`)).length;
  const started = await admin.call<{ request_id: string }>(
    "POST",
    "/api/v1/me/account-links/slack",
    { connection_id: connectionId, handle: "alice@example.org" },
  );
  let code = "";
  await expect
    .poll(async () => {
      const dms = await fsl<Message[]>("GET", `/dms?user=${ALICE}&app=b`);
      code =
        dms.length > before
          ? (/\b([2-9A-HJKMNP-Z]{8})\b/.exec(dms.at(-1)?.text ?? "")?.[1] ?? "")
          : "";
      return code;
    })
    .not.toBe("");
  const link = await admin.call<{ id: string }>(
    "POST",
    "/api/v1/me/account-links/slack/confirmation",
    { request_id: started.request_id, code },
  );
  linkId = link.id;
}

/** Presses a button of the message ts of "alerts" as alice, as an envelope through the socket of "ui". */
async function press(ts: string, action = "ack"): Promise<void> {
  const result = await fsl<{ transport: string; confirmed?: boolean }>("POST", "/press", {
    app: "b",
    channel: CHANNEL,
    ts,
    action,
    user: ALICE,
    transport: "socket",
  });
  expect(result.transport).toBe("socket");
  expect(result.confirmed).toBe(true);
}

async function cleanUp(): Promise<void> {
  await resetFakeSlack();
  const admin = await adminApi();
  try {
    for (const id of groups) {
      await admin.call("POST", `/api/v1/alert-groups/${id}/resolve`, {}).catch(() => undefined);
    }
    if (linkId !== "") {
      await admin.call("DELETE", `/api/v1/me/account-links/${linkId}`).catch(() => undefined);
    }
    const routes = await admin.call<{ items: Named[] }>("GET", "/api/v1/routes");
    for (const r of routes.items.filter((x) => x.name === ROUTE)) {
      await admin.call("POST", `/api/v1/routes/${r.id}/move-open-alert-groups`);
      await admin.call("DELETE", `/api/v1/routes/${r.id}`);
    }
    await removeSlackConnection(admin, "ui");
  } finally {
    await admin.dispose();
  }
}

test.beforeAll(async () => {
  await cleanUp();
  const admin = await adminApi();
  try {
    connectionId = await createSlackConnection(admin, "ui");
  } finally {
    await admin.dispose();
  }
});

test.afterAll(cleanUp);

/** The element of the frame of the preview that draws the message. */
function frame(page: Page): Locator {
  return page.frameLocator('[data-testid="preview-frame"]').locator("body");
}

test("creates a Slack Destination from the bot's channels, checks, tests and previews it", async ({
  page,
}) => {
  test.setTimeout(150_000);
  const csp = watchCsp(page);
  await signInAdmin(page);

  // 4. Destinations → "Create destination" → Slack → Connection "ui": the bot's channels, "alerts" and not "other".
  await nav(page, "Destinations").click();
  await page.getByRole("link", { name: "Create destination" }).click();
  await page.getByRole("radio", { name: "Slack", exact: true }).check();
  const fields = page.getByTestId("slack-fields");
  await fields.getByLabel("Connection", { exact: true }).selectOption({ label: "ui" });
  const picker = fields.getByLabel("Channel", { exact: true });
  await expect(picker.getByRole("option", { name: "#alerts" })).toHaveCount(1);
  await expect(picker.getByRole("option", { name: "#other" })).toHaveCount(0);
  // The filter drops a leading #.
  await fields.getByLabel("Filter channels").fill("#ale");
  await expect(picker.getByRole("option", { name: "#alerts" })).toHaveCount(1);
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await shot(page, "slack-destination-new-360");
  await page.setViewportSize({ width: 1280, height: 800 });

  // A channel without the bot, typed by hand, is refused with the check's text.
  await page.getByRole("textbox", { name: "Name", exact: true }).fill("slack-alerts");
  await fields.getByLabel("Channel id").fill("C0FAKEOTHER");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByTestId("destination-type-channel-error-text")).toHaveText(INVITE);
  await shot(page, "slack-destination-refused");

  // "alerts" from the list saves, and the page shows "#alerts".
  await picker.selectOption({ label: "#alerts" });
  await expect(fields.getByLabel("Channel id")).toHaveValue(CHANNEL);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page).toHaveURL(/\/destinations\/DS[0-9A-Z]+$/);
  destinationId = new URL(page.url()).pathname.split("/").pop() ?? "";
  await expect(page.getByRole("heading", { name: "slack-alerts", level: 1 })).toBeVisible();
  await expect(page.getByTestId("destination-type")).toHaveText("Slack");
  await expect(page.getByTestId("slack-channel-name")).toHaveText("#alerts");

  // "Check": the item "Channel" passes.
  await page.getByRole("button", { name: "Check", exact: true }).click();
  await expect(page.getByTestId("destination-check-summary")).toHaveText("Check passed");
  await expect(page.getByTestId("destination-check-item")).toHaveText("Channel: ok");

  // "Send test message": the one step, with the note that the test cannot press its own button.
  const before = (await messages()).length;
  await page.getByRole("button", { name: "Send test message" }).click();
  await expect(page.getByTestId("test-summary")).toHaveText("Test passed");
  await expect(page.getByTestId("test-step-title")).toHaveText(["Message: Sent"]);
  await expect(page.getByTestId("test-note")).toHaveText(NOTE);
  await shot(page, "slack-destination-test");
  await expect.poll(async () => (await messages()).length).toBe(before + 1);
  expect((await messages()).at(-1)?.text ?? "").toContain("🧪");

  // "Preview": the message drawn from its JSON, the red bar of a firing Alert Group and the green "Ack".
  await page.getByRole("tab", { name: "Preview" }).click();
  const body = frame(page);
  await expect(body.getByRole("button", { name: "Ack", exact: true })).toBeVisible();
  await expect(body.getByRole("button", { name: "Resolve", exact: true })).toBeVisible();
  await expect(body.getByRole("button", { name: "Snooze… ▾" })).toBeVisible();
  const ack = await body
    .getByRole("button", { name: "Ack", exact: true })
    .evaluate((e) => getComputedStyle(e).backgroundColor);
  expect(ack).toBe("rgb(0, 122, 90)");
  const bar = await body
    .locator(".border-l-4")
    .first()
    .evaluate((e) => getComputedStyle(e).borderLeftColor);
  expect(bar).toBe("rgb(211, 47, 47)");
  await shot(page, "slack-destination-preview");
  expect(csp).toEqual([]);
});

test("records a press through the socket with Muster's confirmation and no status", async ({
  page,
}) => {
  expect(destinationId).not.toBe("");
  const csp = watchCsp(page);
  // The test message of the first test, pressed by alice through the socket of "ui".
  const sent = (await messages()).at(-1);
  expect(sent?.text ?? "").toContain("🧪");
  await press(sent?.ts ?? "");

  await signInAdmin(page);
  await page.goto(`/connections/${connectionId}?tab=request-log`);
  const table = page.getByRole("table", { name: "Request log" });
  const row = table
    .locator("tbody tr")
    .filter({ has: page.getByRole("button", { name: "interactive", exact: true }) })
    .first();
  await expect(row.getByTestId("request-status")).toHaveText("Confirmed");
  await expect(row.getByTestId("request-status")).not.toHaveClass(/text-destructive/);
  await row.getByRole("button", { name: "interactive", exact: true }).click();
  const dialog = page.getByTestId("request-record");
  await expect(dialog.getByTestId("record-request-line")).toHaveText(/^WEBSOCKET \S+/);
  await expect(dialog.getByTestId("record-request-body")).toContainText('"type": "interactive"');
  await expect(dialog.getByTestId("record-status")).toHaveText(
    "Muster confirmed this Socket Mode envelope. A confirmation has no status.",
  );
  await expect(dialog.getByTestId("record-response-body")).toContainText('"envelope_id"');
  await expect(dialog).not.toContainText("No answer");
  await shot(page, "slack-request-log-envelope");
  expect(csp).toEqual([]);
});

test("names the Transport Slack, and Check ends a Broken Slack Destination at once", async ({
  page,
}) => {
  test.setTimeout(150_000);
  expect(destinationId).not.toBe("");
  const csp = watchCsp(page);
  const admin = await adminApi();
  try {
    await linkAdmin(admin);
    integrationId = await fakeIntegration("slack-dest");
    const profiles = await admin.call<{ items: { id: string; policy: unknown }[] }>(
      "GET",
      "/api/v1/route-profiles",
    );
    routeId = (
      await admin.call<{ id: string }>("POST", "/api/v1/routes", {
        name: ROUTE,
        matchers: [{ label: "team", op: "=", value: TEAM }],
        urgent: false,
        group_key: ["alertname", "cluster"],
        destination_ids: [destinationId],
        policy: profiles.items.find((p) => p.id === "on_call")?.policy,
      })
    ).id;
    await fam("PUT", "/groups/sld", {
      receiver: "slack-dest",
      route: "{}",
      labels: { alertname: "SlackDisk" },
    });
    await fam("PUT", "/groups/sld/alerts/a", {
      labels: { team: TEAM, cluster: "a", severity: "warning" },
    });
    const before = (await messages()).length;
    await notify(integrationId, "sld", { reason: "first notification" });
    await expect.poll(async () => (await messages()).length).toBeGreaterThan(before);
    const root = (await messages()).at(-1);
    const listed = await admin.call<{ items: { id: string; status: string }[] }>(
      "GET",
      `/api/v1/alert-groups?route=${routeId}`,
    );
    const groupId = listed.items[0]?.id ?? "";
    expect(groupId).not.toBe("");
    groups.push(groupId);

    // Alice, linked to the Admin, presses "Ack" through the socket: the Timeline names the Transport Slack.
    await press(root?.ts ?? "");
    await expect
      .poll(
        async () =>
          (await admin.call<{ status: string }>("GET", `/api/v1/alert-groups/${groupId}`)).status,
      )
      .toBe("acknowledged");
    await signInAdmin(page);
    await page.goto(`/alert-groups/${groupId}`);
    await expect(
      page.getByTestId("timeline-actor").filter({ hasText: "admin via Slack" }),
    ).not.toHaveCount(0);
    await shot(page, "slack-timeline");

    // The bot leaves the channel: the next Alert Group makes the Destination Broken.
    await member(false);
    await fam("PUT", "/groups/sld/alerts/b", {
      labels: { team: TEAM, cluster: "b", severity: "warning" },
    });
    await notify(integrationId, "sld", { reason: "new alerts added" });
    await expect.poll(() => health(admin), { timeout: 30_000 }).toBe("broken");
    const all = await admin.call<{ items: { id: string }[] }>(
      "GET",
      `/api/v1/alert-groups?route=${routeId}`,
    );
    groups.push(...all.items.map((g) => g.id).filter((id) => !groups.includes(id)));
    await page.goto(`/destinations/${destinationId}`);
    await expect(page.getByTestId("broken-banner")).toBeVisible();
    await page.getByRole("button", { name: "Check", exact: true }).click();
    await expect(page.getByTestId("destination-check-item")).toHaveText(
      `Channel: failed ${INVITE} How to fix`,
    );

    // The bot is back: "Check" ends the Broken state at once.
    await member(true);
    await page.getByRole("button", { name: "Check", exact: true }).click();
    await expect(page.getByTestId("destination-check-summary")).toHaveText("Check passed");
    await expect(page.getByTestId("broken-banner")).toHaveCount(0);
    await expect(page.getByTestId("destination-health").first()).toHaveText("Healthy");
    await shot(page, "slack-destination-check-healthy");
    expect(csp).toEqual([]);
  } finally {
    await admin.dispose();
  }
});

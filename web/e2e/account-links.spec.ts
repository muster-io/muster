// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Messenger accounts in the profile and Account links on the user page against `muster dev`: the Verification steps of
// their story. Alice links Telegram through the deep link (the fake Telegram server sends her /start) and Mattermost
// with the code the bot sent (read from the fake Mattermost server), Bob links his Mattermost account so that Alice's
// try is refused, Alice unlinks, the Admin removes her last link from her user page — her open "Relink Telegram" dialog
// stays open, as the hint names a link that went away —, and the section and both dialogs fit 360 pixels. The logins are link-alice and link-bob, as the profile spec takes "Alice" and "bob".
// Through Bridges (Verification step 3 of their story): Bob links the fake bridge's u-bob through the spec's own "br",
// the second bot, by sending the token to it, and through the demo Connection "Dev Bridge", the first bot, with the
// code it sent to his email once the bridge finds people by email and a check read that; the Admin's user page lists
// both links by the bridge's messenger; and an Alert Group of a Route to a Bridge Destination of "br" that Bob
// acknowledges through the fake shows "Bob via Bridge" in its Timeline. The fake's configuration is put back and
// "Dev Bridge" checked again at the end.

import { type Browser, type Page, expect, test } from "@playwright/test";

import {
  type Api,
  adminApi,
  checkConnection,
  connectionNamed,
  createBridgeConnection,
  deleteRoute,
  expectNoHorizontalScroll,
  fakeIntegration,
  fam,
  fbr,
  notify,
  removeBridgeConnection,
  resetFakeBridge,
  shot,
  signIn,
  signInAdmin,
  watchCsp,
} from "./support";

/** The control APIs of the fake Telegram and Mattermost servers of `muster dev`. */
const FTG = "http://127.0.0.1:18081/_fake";
const FMM = "http://127.0.0.1:18065/_fake";
const SERVER_URL = "http://127.0.0.1:18065";
const TG_TOKEN = "123456:dev-telegram-token";
const PASSWORD = "link-password-1";
const CODE = /\b([2-9A-HJKMNP-Z]{8})\b/;
const LINKED_ELSEWHERE =
  "This account is linked to another user. Its owner or an admin can unlink it.";

interface Named {
  id: string;
  name: string;
}

interface Dm {
  user_id: string;
  message: string;
}

/** A direct message of a bot of the fake bridge. */
interface BridgeDm {
  bot: number;
  user_external_id: string;
  body: string;
}

/** A message of a bot of the fake bridge. */
interface BridgeMessage {
  id: string;
  bot: number;
  channel_id: string;
  envelope: { kind: string; title: string };
}

/** The Route, team and Destination of the Alert Group that Bob acknowledges through the bridge. */
const BRIDGE_TEAM = "bridge-links";
const BRIDGE_DESTINATION = "br-links";
const NOBODY = { everyone: "none", user_ids: [], groups: [] };
const MENTIONS = Object.fromEntries(
  ["new_alert_group", "new_alerts", "reopen", "ack_timeout", "snooze_ended", "rise_to_urgent"].map(
    (k) => [k, NOBODY],
  ),
);

test.describe.configure({ mode: "serial" });

let aliceId = "";
let bobId = "";
let brId = "";
let devBridgeId = "";
let bridgeGroupId = "";

/** A person writes to the bot of the demo Telegram Connection in their private chat. */
async function priv(id: number, username: string, text: string): Promise<void> {
  const res = await fetch(`${FTG}/private`, {
    method: "POST",
    body: JSON.stringify({ token: TG_TOKEN, from: { id, username }, text }),
  });
  expect(res.ok, `private message: ${res.status}`).toBe(true);
}

/** The direct messages the Mattermost bot sent to a person. */
async function dms(userId: string): Promise<Dm[]> {
  const res = await fetch(`${FMM}/dms`);
  expect(res.ok).toBe(true);
  const all: unknown = await res.json();
  return Array.isArray(all)
    ? all.filter(
        (d): d is Dm =>
          typeof d === "object" &&
          d !== null &&
          "user_id" in d &&
          d.user_id === userId &&
          "message" in d &&
          typeof d.message === "string",
      )
    : [];
}

/** The code of the first direct message to the person after the before ones. */
async function newCode(userId: string, before: number): Promise<string> {
  let code = "";
  await expect
    .poll(async () => {
      const list = await dms(userId);
      code = list.length > before ? (CODE.exec(list.at(-1)?.message ?? "")?.[1] ?? "") : "";
      return code;
    })
    .not.toBe("");
  return code;
}

/** The direct messages the first bot of the fake bridge, the one of "Dev Bridge", sent to a person. */
async function bridgeDms(userId: string): Promise<BridgeDm[]> {
  const all = await fbr<BridgeDm[]>("GET", "/direct-messages");
  return all.filter((d) => d.bot === 1 && d.user_external_id === userId);
}

/** The code of the first direct message of the bridge's first bot to the person after the before ones. */
async function newBridgeCode(userId: string, before: number): Promise<string> {
  let code = "";
  await expect
    .poll(async () => {
      const list = await bridgeDms(userId);
      code = list.length > before ? (CODE.exec(list.at(-1)?.body ?? "")?.[1] ?? "") : "";
      return code;
    })
    .not.toBe("");
  return code;
}

/** Deletes the Route of the Bridge part, whatever an earlier run left. */
async function deleteBridgeRoute(admin: Api): Promise<void> {
  const routes = await admin.call<{ items: Named[] }>("GET", "/api/v1/routes");
  for (const r of routes.items.filter((x) => x.name === BRIDGE_TEAM)) {
    await deleteRoute(r.id);
  }
}

/** Creates a local Responder with its password and returns its id. */
async function user(admin: Api, login: string, name: string): Promise<string> {
  const created = await admin.call<{
    user: { id: string };
    password_setup_link: { url: string };
  }>("POST", "/api/v1/users", { name, login, role: "responder" });
  const token = new URL(created.password_setup_link.url).hash.replace(/^#token=/, "");
  await admin.call("POST", "/api/v1/password-setups", { token, password: PASSWORD });
  return created.user.id;
}

async function deleteMm(admin: Api): Promise<void> {
  const connections = await admin.call<{ items: Named[] }>("GET", "/api/v1/connections?limit=500");
  for (const c of connections.items.filter((x) => x.name === "mm")) {
    await admin.call("DELETE", `/api/v1/connections/${c.id}`);
  }
}

/**
 * Signs a user in, in a context of their own, and opens the profile; csp collects the page's Content Security Policy
 * violations from the start.
 */
async function profileOf(browser: Browser, login: string): Promise<{ page: Page; csp: string[] }> {
  const context = await browser.newContext();
  const page = await context.newPage();
  const csp = watchCsp(page);
  await signIn(page, login, PASSWORD);
  await page.goto("/profile");
  await expect(page.getByRole("heading", { name: "Messenger accounts" })).toBeVisible();
  return { page, csp };
}

function rows(page: Page) {
  return page.getByTestId("account-link-row");
}

/** "Link Mattermost" with the Connection "mm" and the username, up to "Send code". */
async function askCode(page: Page, username: string): Promise<void> {
  await page.getByRole("button", { name: "Link Mattermost" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Connection").selectOption({ label: "mm" });
  await dialog.getByLabel("Your Mattermost username").fill(username);
  await dialog.getByRole("button", { name: "Send code" }).click();
}

/** Links the Mattermost account of the person userId with the code the bot sends. */
async function linkMattermost(page: Page, username: string, userId: string): Promise<void> {
  const before = (await dms(userId)).length;
  await askCode(page, username);
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText("5 attempts left")).toBeVisible();
  await dialog
    .getByLabel("Enter the code the bot sent you in Mattermost")
    .fill(await newCode(userId, before));
  await dialog.getByRole("button", { name: "Link", exact: true }).click();
  await expect(dialog).toBeHidden();
}

test.beforeAll(async () => {
  const admin = await adminApi();
  try {
    await deleteMm(admin);
    await admin.call("POST", "/api/v1/connections", {
      type: "mattermost",
      name: "mm",
      server_url: SERVER_URL,
      bot_token: "mm-dev-token",
      proxy: { enabled: false },
      limiter: { limit: 50, per_seconds: 1 },
    });
    aliceId = await user(admin, "link-alice", "Alice Smith");
    bobId = await user(admin, "link-bob", "Bob");
    await resetFakeBridge();
    await deleteBridgeRoute(admin);
    await removeBridgeConnection(admin);
    brId = await createBridgeConnection(admin);
    devBridgeId = await connectionNamed(admin, "Dev Bridge");
  } finally {
    await admin.dispose();
  }
});

test.afterAll(async () => {
  const admin = await adminApi();
  try {
    await deleteMm(admin);
    if (bridgeGroupId !== "") {
      await admin.call("POST", `/api/v1/alert-groups/${bridgeGroupId}/resolve`, {});
    }
    await deleteBridgeRoute(admin);
    await removeBridgeConnection(admin);
    // "Dev Bridge" reads the fake's configuration again, so that its stored info finds people by login once more.
    await resetFakeBridge();
    await checkConnection(admin, devBridgeId);
  } finally {
    await admin.dispose();
  }
});

test("links, unlinks and removes messenger accounts", async ({ browser, page }) => {
  const { page: alice, csp } = await profileOf(browser, "link-alice");

  // 1. The empty section with both link buttons.
  await expect(alice.getByText("No messenger accounts linked yet.")).toBeVisible();
  await expect(alice.getByRole("button", { name: "Link Telegram" })).toBeVisible();
  await expect(alice.getByRole("button", { name: "Link Mattermost" })).toBeVisible();
  await shot(alice, "account-links-empty");

  // 2. Telegram: the deep link and its QR code; the bot links the account and the dialog closes.
  await alice.getByRole("button", { name: "Link Telegram" }).click();
  const tg = alice.getByRole("dialog");
  // Exactly one Telegram Connection: no picker, the link is asked for at once.
  await expect(tg.getByLabel("Connection")).toBeHidden();
  const open = tg.getByRole("link", { name: "Open Telegram" });
  await expect(open).toHaveAttribute(
    "href",
    /^https:\/\/t\.me\/muster_dev_bot\?start=[A-Za-z0-9_-]{43}$/,
  );
  await expect(tg.getByText(/^This link works once and expires at \d\d:\d\d\.$/)).toBeVisible();
  await expect(tg.getByRole("img", { name: "QR code of the Telegram link" })).toBeVisible();
  await shot(alice, "account-links-telegram");
  const token = new URL((await open.getAttribute("href")) ?? "").searchParams.get("start") ?? "";
  await priv(7001, "alice_t", `/start ${token}`);
  await expect(tg).toBeHidden({ timeout: 15_000 });
  await expect(alice.getByTestId("account-link-summary")).toHaveText(
    "Telegram · @alice_t · for all Telegram bots",
  );
  await expect(alice.getByTestId("account-link-via")).toHaveText("linked via Dev Telegram · Never");
  await expect(alice.getByRole("button", { name: "Relink Telegram" })).toBeVisible();

  // 3. Mattermost: a wrong code counts down the attempts, the right one links.
  const before = (await dms("u-alice")).length;
  await askCode(alice, "@alice");
  const mm = alice.getByRole("dialog");
  await expect(mm.getByLabel("Enter the code the bot sent you in Mattermost")).toBeVisible();
  await expect(mm.getByText("5 attempts left")).toBeVisible();
  await mm.getByLabel("Enter the code the bot sent you in Mattermost").fill("22222222");
  await mm.getByRole("button", { name: "Link", exact: true }).click();
  await expect(
    mm.getByText("The code is wrong or no longer valid. Request a new code."),
  ).toBeVisible();
  await expect(mm.getByText("4 attempts left")).toBeVisible();
  await shot(alice, "account-links-mattermost-code");
  await mm
    .getByLabel("Enter the code the bot sent you in Mattermost")
    .fill(await newCode("u-alice", before));
  await mm.getByRole("button", { name: "Link", exact: true }).click();
  await expect(mm).toBeHidden();
  await expect(rows(alice).filter({ hasText: "Mattermost · mm · @alice" })).toHaveCount(1);
  await expect(rows(alice)).toHaveCount(2);
  await shot(alice, "account-links-list");

  // 4. Bob links his own account; Alice's try for it is refused.
  const { page: bob, csp: bobCsp } = await profileOf(browser, "link-bob");
  await linkMattermost(bob, "@bob", "u-bob");
  await expect(rows(bob).filter({ hasText: "Mattermost · mm · @bob" })).toHaveCount(1);
  expect(bobCsp).toEqual([]);
  await bob.context().close();
  await askCode(alice, "@bob");
  await expect(alice.getByRole("dialog").getByText(LINKED_ELSEWHERE)).toBeVisible();
  await alice.keyboard.press("Escape");
  await expect(alice.getByRole("dialog")).toBeHidden();

  // 5. Unlink the Mattermost row after the confirmation.
  await alice.getByRole("button", { name: "Unlink @alice", exact: true }).click();
  const unlink = alice.getByRole("dialog");
  await expect(
    unlink.getByText(
      "Unlink @alice from Muster? Presses from this account will no longer act as you.",
    ),
  ).toBeVisible();
  await unlink.getByRole("button", { name: "Unlink", exact: true }).click();
  await expect(unlink).toBeHidden();
  await expect(rows(alice)).toHaveCount(1);

  // 6. The Admin's user page lists the link with "Remove" and no way to link; removing it empties Alice's list live,
  // and Alice's open "Relink Telegram" dialog stays open: the hint names a link that went away, not a new Telegram one.
  await alice.getByRole("button", { name: "Relink Telegram" }).click();
  const relink = alice.getByRole("dialog");
  await expect(relink.getByRole("link", { name: "Open Telegram" })).toBeVisible();
  const adminCsp = watchCsp(page);
  await signInAdmin(page);
  await page.goto(`/admin/users/${aliceId}`);
  await expect(page.getByRole("heading", { name: "Account links" })).toBeVisible();
  await expect(rows(page)).toHaveCount(1);
  await expect(rows(page)).toContainText("Telegram · @alice_t · for all Telegram bots");
  await expect(rows(page)).toContainText("linked via Dev Telegram");
  await expect(page.getByRole("button", { name: /^Link/ })).toHaveCount(0);
  await shot(page, "account-links-admin");
  await page.getByRole("button", { name: "Remove @alice_t", exact: true }).click();
  const remove = page.getByRole("dialog");
  await expect(
    remove.getByText("Remove this link? Presses from @alice_t will no longer act as Alice Smith."),
  ).toBeVisible();
  await remove.getByRole("button", { name: "Remove", exact: true }).click();
  await expect(remove).toBeHidden();
  await expect(page.getByText("No messenger accounts linked.")).toBeVisible();
  await expect(alice.getByText("No messenger accounts linked yet.")).toBeVisible();
  await expect(relink.getByRole("link", { name: "Open Telegram" })).toBeVisible();
  await shot(alice, "account-links-telegram-dialog-stays");
  await alice.keyboard.press("Escape");
  await expect(alice.getByRole("dialog")).toBeHidden();

  // 7. At 360 pixels the section and both dialogs fit.
  await alice.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(alice);
  await shot(alice, "account-links-phone");
  await alice.getByRole("button", { name: "Link Telegram" }).click();
  await expect(alice.getByRole("img", { name: "QR code of the Telegram link" })).toBeVisible();
  await expectNoHorizontalScroll(alice);
  await shot(alice, "account-links-phone-telegram");
  await alice.keyboard.press("Escape");
  await expect(alice.getByRole("dialog")).toBeHidden();
  await alice.getByRole("button", { name: "Link Mattermost" }).click();
  await expect(alice.getByRole("dialog").getByLabel("Your Mattermost username")).toBeVisible();
  await expectNoHorizontalScroll(alice);
  await alice.getByRole("dialog").getByLabel("Your Mattermost username").fill("@alice");
  await alice.getByRole("dialog").getByRole("button", { name: "Send code" }).click();
  await expect(alice.getByRole("dialog").getByText("5 attempts left")).toBeVisible();
  await expectNoHorizontalScroll(alice);
  await shot(alice, "account-links-phone-mattermost");
  await alice.keyboard.press("Escape");
  await expect(alice.getByRole("dialog")).toBeHidden();

  expect(csp).toEqual([]);
  expect(adminCsp).toEqual([]);
  await alice.context().close();
});

test("links accounts through Bridges by token and by code, and acknowledges through the bridge", async ({
  browser,
  page,
}) => {
  test.setTimeout(180_000);
  const { page: bob, csp } = await profileOf(browser, "link-bob");

  // 1. "Link through br" → the token with "Copy", the instruction to send it to the bot and when it expires; "br" has
  // no link template, so no link. The dialog fits 360 pixels.
  await bob.setViewportSize({ width: 360, height: 740 });
  await bob.getByRole("button", { name: "Link through br", exact: true }).click();
  const tokenDialog = bob.getByRole("dialog");
  await expect(tokenDialog.getByRole("tab", { name: "Token" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  const tokenBlock = tokenDialog.getByTestId("link-bridge-token");
  await expect(tokenBlock).toHaveText(/^[A-Za-z0-9_-]{43}$/);
  await expect(tokenDialog.getByRole("button", { name: "Copy" })).toBeVisible();
  await expect(tokenDialog.getByTestId("link-bridge-instruction")).toHaveText(
    "Send this token to the bot in a direct message.",
  );
  await expect(tokenDialog.getByTestId("link-bridge-expires")).toHaveText(
    /^This token works once and expires at \d\d:\d\d\.$/,
  );
  await expect(tokenDialog.getByTestId("link-bridge-open")).toHaveCount(0);
  await expectNoHorizontalScroll(bob);
  await shot(bob, "account-links-bridge-token-360");
  await bob.setViewportSize({ width: 1280, height: 800 });

  // 2. Bob sends the token to the bot of "br": the dialog closes and the row appears without a reload.
  const token = (await tokenBlock.textContent()) ?? "";
  await fbr("POST", "/link", { user_id: "u-bob", token, bot: 2 });
  await expect(tokenDialog).toBeHidden({ timeout: 15_000 });
  await expect(rows(bob).filter({ hasText: "Fake chat · Bob Brown · via br" })).toHaveCount(1);
  await expect(
    rows(bob).getByTestId("account-link-summary").filter({ hasText: "via br" }),
  ).toHaveText("Fake chat · Bob Brown · via br");

  // 3. The bridge finds people by email; once a check of "Dev Bridge" read that, Bob links through it with the code
  // the bot sent him, after a wrong one.
  await fbr("PUT", "/config", { user_lookup: "email" });
  const admin = await adminApi();
  try {
    await checkConnection(admin, devBridgeId);
  } finally {
    await admin.dispose();
  }
  await bob.reload();
  await bob.getByRole("button", { name: "Link through Dev Bridge", exact: true }).click();
  const codeDialog = bob.getByRole("dialog");
  await codeDialog.getByRole("tab", { name: "Code" }).click();
  const before = (await bridgeDms("u-bob")).length;
  await codeDialog.getByLabel("Email", { exact: true }).fill("bob@example.org");
  await codeDialog.getByRole("button", { name: "Send code" }).click();
  await expect(codeDialog.getByTestId("link-bridge-attempts")).toHaveText("5 attempts left");
  const codeInput = codeDialog.getByLabel("Enter the code the bot sent you");
  await codeInput.fill("22222222");
  await codeDialog.getByRole("button", { name: "Link", exact: true }).click();
  await expect(codeDialog.getByTestId("link-bridge-code-error")).toHaveText(
    "The code is wrong or no longer valid. Request a new code.",
  );
  await expect(codeDialog.getByTestId("link-bridge-attempts")).toHaveText("4 attempts left");
  await shot(bob, "account-links-bridge-code");
  await codeInput.fill(await newBridgeCode("u-bob", before));
  await codeDialog.getByRole("button", { name: "Link", exact: true }).click();
  await expect(codeDialog).toBeHidden();
  await expect(rows(bob).filter({ hasText: "Fake chat · Bob Brown · via Dev Bridge" })).toHaveCount(
    1,
  );
  await shot(bob, "account-links-bridge-list");

  // The Admin's user page lists both Bridge links by the bridge's messenger.
  const adminCsp = watchCsp(page);
  await signInAdmin(page);
  await page.goto(`/admin/users/${bobId}`);
  await expect(page.getByRole("heading", { name: "Account links" })).toBeVisible();
  await expect(rows(page).filter({ hasText: "Fake chat · Bob Brown · via br" })).toHaveCount(1);
  await expect(
    rows(page).filter({ hasText: "Fake chat · Bob Brown · via Dev Bridge" }),
  ).toHaveCount(1);

  // 4. An Alert Group delivered to a Bridge Destination of "br"; Bob presses "Ack" in the fake, and its Timeline names
  // the Transport "Bridge".
  const integrationId = await fakeIntegration("bridge-links");
  const api = await adminApi();
  try {
    const created = await api.call<{ destination: { id: string } }>(
      "POST",
      "/api/v1/destinations",
      {
        type: "bridge",
        name: BRIDGE_DESTINATION,
        connection_id: brId,
        channel_id: "ch-alerts",
        mentions: MENTIONS,
        limiter: { limit: 30, per_seconds: 60 },
      },
    );
    const profiles = await api.call<{ items: { id: string; policy: unknown }[] }>(
      "GET",
      "/api/v1/route-profiles",
    );
    await api.call("POST", "/api/v1/routes", {
      name: BRIDGE_TEAM,
      matchers: [{ label: "team", op: "=", value: BRIDGE_TEAM }],
      urgent: false,
      group_key: ["alertname"],
      destination_ids: [created.destination.id],
      policy: profiles.items.find((p) => p.id === "on_call")?.policy,
    });
    await fam("PUT", `/groups/${BRIDGE_TEAM}`, {
      receiver: "bridge-links",
      route: "{}",
      labels: { alertname: "BridgeLinks" },
    });
    await fam("PUT", `/groups/${BRIDGE_TEAM}/alerts/a`, {
      labels: { team: BRIDGE_TEAM, cluster: "bl1" },
    });
    await notify(integrationId, BRIDGE_TEAM, { reason: "first notification" });
    let group = { id: "", number: 0 };
    await expect
      .poll(async () => {
        const alerts = await api.call<{
          items: { alert_group?: { id: string; number: number } }[];
        }>(
          "GET",
          `/api/v1/integrations/${integrationId}/alerts?label=${encodeURIComponent('cluster="bl1"')}`,
        );
        group = alerts.items[0]?.alert_group ?? group;
        return group.id;
      })
      .not.toBe("");
    bridgeGroupId = group.id;
    let root: BridgeMessage | undefined;
    await expect
      .poll(
        async () => {
          root = (await fbr<BridgeMessage[]>("GET", "/messages")).find(
            (m) =>
              m.bot === 2 &&
              m.channel_id === "ch-alerts" &&
              m.envelope.kind === "root" &&
              m.envelope.title.startsWith(`#${group.number} `),
          );
          return root !== undefined;
        },
        { timeout: 30_000 },
      )
      .toBe(true);
    await fbr("POST", "/press", { message_id: root?.id, user_id: "u-bob", button: "Ack" });
    await expect
      .poll(
        async () =>
          (await api.call<{ status: string }>("GET", `/api/v1/alert-groups/${group.id}`)).status,
        { timeout: 30_000 },
      )
      .toBe("acknowledged");
  } finally {
    await api.dispose();
  }
  await bob.goto(`/alert-groups/${bridgeGroupId}`);
  await expect(
    bob.getByTestId("timeline-actor").filter({ hasText: "Bob via Bridge" }).first(),
  ).toBeVisible();
  await shot(bob, "account-links-bridge-timeline");

  expect(csp).toEqual([]);
  expect(adminCsp).toEqual([]);
  await bob.context().close();
});

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Organization settings pages against `muster dev`: Verification steps 1 to 7 of their story. The main navigation
// has one "Organization" entry whose sub-navigation leads to every settings page; General saves the time zone of
// messages, and the start of the working day and the first day of the week, which the Snooze dialog's presets then
// name in English and in Russian, and shows the API's refusal of an empty start at its field (step 4 of the story of
// the Snooze presets in the web UI); Retention shows the server's retention_order refusal at the summaries field;
// Severity levels lists the unmapped value P5 and maps it; Outbound addresses saves a denied network and refuses an
// entry that is not one; Keyring follows an activation by the CLI without a reload; Security saves the token grace and
// shows a conflict; a Responder sees only Lookup tables and Link rules. The spec puts the Organization, the Admin's
// language and the outbound address policy back as it found them, and no Content Security Policy violation occurs.

import { spawnSync } from "node:child_process";
import { resolve } from "node:path";

import { expect, test, type Page } from "@playwright/test";

import {
  type Api,
  adminApi,
  fakeIntegration,
  fam,
  notify,
  shot,
  signIn,
  signInAdmin,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

const SUB_ENTRIES = [
  "General",
  "Severity levels",
  "Alert handling",
  "Security",
  "Outbound addresses",
  "Retention",
  "Keyring",
  "Outgoing heartbeat",
  "Lookup tables",
  "Link rules",
  "System status",
  "Audit log",
];
const MOVED = [
  "Security",
  "Outgoing heartbeat",
  "Lookup tables",
  "Link rules",
  "System status",
  "Audit log",
];
const STALE = "Someone else changed these settings. Reload to see them.";
const RETENTION_ORDER = "Keep Alert Group summaries at least as long as alert details.";
const P5_BANNER = "These values have no mapping and count as warning: P5.";
const NO_PERMISSION = "You do not have permission to see this page.";
const WORKING_WEEK_HINT = "Snooze presets end at this time and on this day.";
const INVALID_FORMAT = "This value has the wrong format.";
const WEEKDAYS = ["Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"];

type Organization = Record<string, unknown> & {
  outgoing_heartbeat: { proxy: Record<string, unknown> };
};
interface OutboundPolicy {
  policy: string;
  allowed: string[];
  denied: string[];
}
interface KeyringKey {
  id: string;
  active: boolean;
  held_by: string[];
  state: string;
}

let savedOrganization: Organization | undefined;
let savedPolicy: OutboundPolicy | undefined;

async function withAdmin<T>(run: (admin: Api) => Promise<T>): Promise<T> {
  const admin = await adminApi();
  try {
    return await run(admin);
  } finally {
    await admin.dispose();
  }
}

/** The Organization as an update: everything as read, the stored Secrets kept. */
function organizationInput(org: Organization): Record<string, unknown> {
  const { id: _id, etag: _etag, outgoing_heartbeat: heartbeat, ...rest } = org;
  const { password_status: _password, ...proxy } = heartbeat.proxy;
  return { ...rest, outgoing_heartbeat: { proxy } };
}

test.beforeAll(async () => {
  await withAdmin(async (admin) => {
    savedOrganization = await admin.call<Organization>("GET", "/api/v1/organization");
    savedPolicy = await admin.call<OutboundPolicy>("GET", "/api/v1/organization/outbound-policy");
  });
});

test.afterAll(async () => {
  await withAdmin(async (admin) => {
    if (savedOrganization !== undefined) {
      const current = await admin.call<{ etag: string }>("GET", "/api/v1/organization");
      await admin.call("PUT", "/api/v1/organization", organizationInput(savedOrganization), {
        "If-Match": current.etag,
      });
    }
    if (savedPolicy !== undefined) {
      const current = await admin.call<{ etag: string }>(
        "GET",
        "/api/v1/organization/outbound-policy",
      );
      const { policy, allowed, denied } = savedPolicy;
      await admin.call(
        "PUT",
        "/api/v1/organization/outbound-policy",
        { policy, allowed, denied },
        { "If-Match": current.etag },
      );
    }
  });
});

/** The language of a profile; null follows the browser. */
type Language = "en" | "ru" | null;

/** The start of the working day and the first day of the week of the Organization. */
interface WorkingWeek {
  workday_start: string;
  week_start: string;
}

/** Sets the language of the Admin's profile, keeping the rest of it. */
async function adminLanguage(language: Language): Promise<void> {
  await withAdmin(async (admin) => {
    const me = await admin.call<{ user: { name: string } }>("GET", "/api/v1/me");
    await admin.call("PUT", "/api/v1/me", { name: me.user.name, language });
  });
}

/** Sets the start of the working day and the first day of the week, keeping the rest of the Organization. */
async function setWorkingWeek({ workday_start, week_start }: WorkingWeek): Promise<void> {
  await withAdmin(async (admin) => {
    const org = await admin.call<Organization & { etag: string }>("GET", "/api/v1/organization");
    await admin.call(
      "PUT",
      "/api/v1/organization",
      { ...organizationInput(org), workday_start, week_start },
      { "If-Match": org.etag },
    );
  });
}

function nav(page: Page, name: string) {
  return page.getByRole("navigation", { name: "Main" }).getByRole("link", { name, exact: true });
}

function subNav(page: Page) {
  return page.getByRole("navigation", { name: "Organization" });
}

function sub(page: Page, name: string) {
  return subNav(page).getByRole("link", { name, exact: true });
}

function save(page: Page) {
  return page.getByRole("button", { name: "Save", exact: true }).click();
}

test("one Organization entry leads to General, which saves the time zone of messages", async ({
  page,
}) => {
  const csp = watchCsp(page);
  await signInAdmin(page);

  // 1. "Organization" and none of the moved entries → the twelve sub-entries → General → "Europe/Berlin" → "Saved.".
  await expect(nav(page, "Organization")).toBeVisible();
  for (const entry of MOVED) {
    await expect(nav(page, entry)).toHaveCount(0);
  }
  await nav(page, "Organization").click();
  await expect(page).toHaveURL(/\/admin\/organization\/general$/);
  await expect(nav(page, "Organization")).toHaveAttribute("aria-current", "page");
  await expect(subNav(page).getByRole("link")).toHaveText(SUB_ENTRIES);
  await expect(page.getByRole("heading", { name: "General", level: 1 })).toBeVisible();
  const zone = page.getByLabel("Time zone of messages");
  await expect(zone).toHaveValue("UTC");
  await expect(zone).toHaveAccessibleDescription(
    "Times in messages use this time zone. Each user sees the web UI in their own time zone.",
  );
  await page.getByLabel("Search time zones").fill("berl");
  await expect(
    zone.locator("option", { hasText: /^Europe\/Berlin \(UTC\+0[12]:00\)$/ }),
  ).toHaveCount(1);
  await zone.selectOption("Europe/Berlin");
  await save(page);
  await expect(page.getByTestId("general-status")).toHaveText("Saved.");
  await shot(page, "organization-general");
  const org = await withAdmin((admin) =>
    admin.call<{ time_zone: string }>("GET", "/api/v1/organization"),
  );
  expect(org.time_zone).toBe("Europe/Berlin");

  await sub(page, "Audit log").click();
  await expect(page).toHaveURL(/\/admin\/audit-log$/);
  await expect(nav(page, "Organization")).toHaveAttribute("aria-current", "page");
  await page.getByLabel("Action", { exact: true }).fill("organization.updated");
  await page.getByLabel("Action", { exact: true }).press("Enter");
  await expect(page).toHaveURL(/[?&]action=organization\.updated/);
  const newest = page.getByRole("table", { name: "Audit log" }).getByRole("row").nth(1);
  await expect(newest).toContainText("organization.updated");
  await expect(newest.getByTestId("audit-diff")).toContainText("Europe/Berlin");
  expect(csp).toEqual([]);
});

test("General saves the working day and week, which the Snooze presets name, and refuses an empty start", async ({
  page,
}) => {
  test.setTimeout(120_000);
  const csp = watchCsp(page);
  // A firing Alert Group, whose Snooze dialog offers the presets.
  const integration = await fakeIntegration("org-week");
  await fam("PUT", "/groups/owk", {
    receiver: "org-week",
    route: "{}",
    labels: { alertname: "OrgWeek" },
  });
  await fam("PUT", "/groups/owk/alerts/w1", { labels: { team: "org-week" } });
  await notify(integration, "owk", { reason: "first notification" });
  const group = await withAdmin(
    async (admin) =>
      (
        await admin.call<{ items: { alert_group?: { id: string; number: number } }[] }>(
          "GET",
          `/api/v1/integrations/${integration}/alerts`,
        )
      ).items[0]?.alert_group ?? { id: "", number: 0 },
  );
  // What the test changes, read first and put back exactly as read.
  const saved = await withAdmin(async (admin) => {
    const me = await admin.call<{ user: { language?: Language } }>("GET", "/api/v1/me");
    const org = await admin.call<WorkingWeek>("GET", "/api/v1/organization");
    return {
      language: me.user.language ?? null,
      week: { workday_start: org.workday_start, week_start: org.week_start },
    };
  });
  await signInAdmin(page);
  try {
    // 4. "Start of the working day" 08:30 and "First day of the week" Sunday under the time zone → "Saved.".
    await page.goto("/admin/organization/general");
    const start = page.getByLabel("Start of the working day");
    const week = page.getByLabel("First day of the week");
    await expect(start).toHaveValue("09:00");
    await expect(week).toHaveValue("monday");
    await expect(week.locator("option")).toHaveText(WEEKDAYS);
    await expect(start).toHaveAccessibleDescription(WORKING_WEEK_HINT);
    await expect(week).toHaveAccessibleDescription(WORKING_WEEK_HINT);
    await start.fill("08:30");
    await week.selectOption("sunday");
    await save(page);
    await expect(page.getByTestId("general-status")).toHaveText("Saved.");
    await shot(page, "general-working-week-en");
    const org = await withAdmin((admin) =>
      admin.call<{ workday_start: string; week_start: string }>("GET", "/api/v1/organization"),
    );
    expect([org.workday_start, org.week_start]).toEqual(["08:30", "sunday"]);

    // The Snooze dialog of an Alert Group names them.
    await page.goto(`/alert-groups/${group.id}`);
    await page.getByRole("button", { name: "Snooze", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: `Snooze #${group.number}` });
    await expect(dialog.getByTestId("snooze-preset-end-week_start")).toContainText("08:30");
    for (const name of ["Until tomorrow 08:30", "Until Sunday 08:30"]) {
      await expect(dialog.getByRole("radio", { name, exact: true })).toBeAttached();
    }
    await shot(page, "general-working-week-snooze-en");

    // "Start of the working day" cleared → "Save" → the API's refusal at the field.
    await page.goto("/admin/organization/general");
    await start.fill("");
    await save(page);
    await expect(start).toHaveAttribute("aria-invalid", "true");
    await expect(start).toHaveAccessibleDescription(`${INVALID_FORMAT} ${WORKING_WEEK_HINT}`);
    await expect(page.getByText(INVALID_FORMAT, { exact: true })).toHaveCount(1);
    await expect(start).toBeFocused();
    await expect(page.getByTestId("general-status")).toHaveText("");
    await shot(page, "general-working-week-refused-en");

    // In Russian: the General page and «До завтра 08:30», «До воскресенья 08:30».
    await adminLanguage("ru");
    await page.reload();
    await expect(page.getByLabel("Начало рабочего дня")).toHaveValue("08:30");
    await expect(page.getByLabel("Первый день недели").locator("option:checked")).toHaveText(
      "Воскресенье",
    );
    await expect(
      page.getByText("В это время и в этот день заканчиваются пресеты откладывания."),
    ).toBeVisible();
    await shot(page, "general-working-week-ru");
    await page.goto(`/alert-groups/${group.id}`);
    await page.getByRole("button", { name: "Отложить", exact: true }).click();
    const russian = page.getByRole("dialog", { name: `Отложить #${group.number}` });
    await expect(russian.getByTestId("snooze-preset-end-tomorrow")).toContainText("Закончится");
    for (const name of ["До завтра 08:30", "До воскресенья 08:30"]) {
      await expect(russian.getByRole("radio", { name, exact: true })).toBeAttached();
    }
    await shot(page, "general-working-week-snooze-ru");
  } finally {
    await adminLanguage(saved.language);
    await setWorkingWeek(saved.week);
  }
  expect(csp).toEqual([]);
});

test("Retention shows the order refusal at the summaries field, then saves", async ({ page }) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  await page.goto("/admin/organization/retention");

  // 2. "Alert Group summaries" 30 while "Alert details" is 90 → the refusal at the field → 90 → "Saved.".
  const details = page.getByLabel("Alert details (days)");
  const summaries = page.getByLabel("Alert Group summaries (days)");
  await details.fill("90");
  await summaries.fill("30");
  await save(page);
  await expect(summaries).toHaveAttribute("aria-invalid", "true");
  await expect(summaries).toHaveAccessibleDescription(
    new RegExp(RETENTION_ORDER.replace(".", "\\.")),
  );
  await expect(page.getByText(RETENTION_ORDER)).toHaveCount(1);
  await expect(summaries).toBeFocused();
  await shot(page, "organization-retention-order");
  await summaries.fill("90");
  await save(page);
  await expect(page.getByTestId("retention-status")).toHaveText("Saved.");
  await expect(page.getByText(RETENTION_ORDER)).toHaveCount(0);
  expect(csp).toEqual([]);
});

test("Severity levels lists the unmapped value P5 and maps it", async ({ page }) => {
  test.setTimeout(120_000);
  const csp = watchCsp(page);
  // An Alert with severity="P5", which the default mapping does not name.
  const integration = await fakeIntegration("org-sev");
  await fam("PUT", "/groups/osv", {
    receiver: "org-sev",
    route: "{}",
    labels: { alertname: "OrgSeverity" },
  });
  await fam("PUT", "/groups/osv/alerts/p5", {
    labels: { team: "org-sev", severity: "P5" },
  });
  await notify(integration, "osv", { reason: "first notification" });
  await signInAdmin(page);
  await page.goto("/admin/organization/severity-levels");

  // 3. The banner and the row "P5 · 1 · warning · No mapping — counts as warning" → "Map" → "info" → "Saved." → the
  // row shows info and the banner is gone.
  await expect(page.getByText(P5_BANNER, { exact: true })).toBeVisible();
  const values = page.getByRole("table", { name: "Values seen" });
  await expect(
    page.getByRole("heading", { name: "Values seen in the last 14 days" }),
  ).toBeVisible();
  const p5 = values.getByRole("row").filter({ hasText: "P5" });
  await expect(p5.getByRole("cell").nth(1)).toHaveText("1");
  await expect(p5.getByText("warning", { exact: true })).toBeVisible();
  await expect(p5.getByText("No mapping — counts as warning")).toBeVisible();
  await shot(page, "organization-severity-unmapped");
  await p5.getByRole("button", { name: "Map" }).click();
  const level = page.getByLabel("Level of P5");
  await expect(level).toBeFocused();
  await level.selectOption("info");
  await save(page);
  await expect(page.getByTestId("severity-levels-status")).toHaveText("Saved.");
  await expect(p5.getByText("info", { exact: true })).toBeVisible();
  await expect(p5.getByText("No mapping — counts as warning")).toHaveCount(0);
  await expect(page.getByText(P5_BANNER)).toHaveCount(0);

  // A value mapped twice is refused next to it.
  await page.getByRole("button", { name: "Add value" }).click();
  await page
    .getByRole("textbox", { name: /^Value \d+$/ })
    .last()
    .fill("P5");
  await expect(page.getByText("This value is already mapped.")).toHaveCount(1);
  await save(page);
  await expect(page.getByTestId("severity-levels-status")).toHaveText("");
  await page.reload();
  await expect(page.getByText("This value is already mapped.")).toHaveCount(0);
  expect(csp).toEqual([]);
});

test("Outbound addresses saves a denied network and refuses an entry that is not one", async ({
  page,
}) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  await page.goto("/admin/organization/outbound");

  // 4. "Always blocked" → 10.20.0.0/16 denied → "Saved." → removed → "Saved."; "not a network" is refused next to it.
  // 127.0.0.0/8 is never touched: the fake servers listen on loopback.
  const blocked = page.getByRole("region", { name: "Always blocked" });
  for (const rule of [/link-local/i, /cloud metadata/i, /unspecified/i, /multicast/i]) {
    await expect(blocked).toContainText(rule);
  }
  await expect(page.getByText("A denied entry always wins.")).toBeVisible();
  await expect(
    page.getByRole("radio", { name: "Standard — private addresses allowed, loopback blocked" }),
  ).toBeChecked();
  await expect(
    page.getByRole("radio", { name: "Strict — only public addresses and the allowed entries" }),
  ).toBeVisible();

  await page.getByRole("button", { name: "Add denied entry" }).click();
  const denied = page.getByRole("textbox", { name: /^Denied, entry \d+$/ }).last();
  await expect(denied).toBeFocused();
  await denied.fill("10.20.0.0/16");
  await save(page);
  await expect(page.getByTestId("outbound-status")).toHaveText("Saved.");
  const saved = await withAdmin((admin) =>
    admin.call<OutboundPolicy>("GET", "/api/v1/organization/outbound-policy"),
  );
  expect(saved.denied).toContain("10.20.0.0/16");
  await shot(page, "organization-outbound");

  const deniedName = await denied.getAttribute("aria-label");
  await page.getByRole("button", { name: `Remove ${deniedName ?? ""}`, exact: true }).click();
  await save(page);
  await expect(page.getByTestId("outbound-status")).toHaveText("Saved.");
  const removed = await withAdmin((admin) =>
    admin.call<OutboundPolicy>("GET", "/api/v1/organization/outbound-policy"),
  );
  expect(removed.denied).not.toContain("10.20.0.0/16");

  await page.getByRole("button", { name: "Add allowed entry" }).click();
  const allowed = page.getByRole("textbox", { name: /^Allowed, entry \d+$/ }).last();
  await allowed.fill("not a network");
  await save(page);
  await expect(allowed).toHaveAttribute("aria-invalid", "true");
  await expect(allowed).toHaveAccessibleDescription("This value has the wrong format.");
  await expect(page.getByText("This value has the wrong format.")).toHaveCount(1);
  await shot(page, "organization-outbound-refused");
  const allowedName = await allowed.getAttribute("aria-label");
  await page.getByRole("button", { name: `Remove ${allowedName ?? ""}`, exact: true }).click();
  expect(csp).toEqual([]);
});

test("Keyring follows the activation of a key without a reload", async ({ page }) => {
  test.setTimeout(120_000);
  const csp = watchCsp(page);
  await signInAdmin(page);
  await nav(page, "Organization").click();
  await sub(page, "Keyring").click();
  await expect(page).toHaveURL(/\/admin\/organization\/keyring$/);

  // 5. Two keys: one active and held by the one replica, the other not active yet and not removable.
  const keyring = await withAdmin((admin) =>
    admin.call<{ keys: KeyringKey[] }>("GET", "/api/v1/organization/keyring"),
  );
  expect(keyring.keys).toHaveLength(2);
  const first = keyring.keys.find((k) => k.active);
  const other = keyring.keys.find((k) => !k.active);
  if (first === undefined || other === undefined) {
    throw new Error(`no active and inactive key: ${JSON.stringify(keyring)}`);
  }
  expect(first.held_by).toHaveLength(1);
  const table = page.getByRole("table", { name: "Keys" });
  const row = (id: string) => table.getByRole("row").filter({ hasText: id });
  await expect(table.getByTestId("keyring-key")).toHaveCount(2);
  await expect(row(first.id).getByTestId("keyring-key-state")).toHaveText("Active");
  await expect(row(first.id)).toContainText(`Held by ${first.held_by.join(", ")}`);
  await expect(row(other.id).getByTestId("keyring-key-state")).toHaveText("Not active yet");
  await expect(table).not.toContainText("can be removed");
  await expect(
    page.getByText("muster secrets rotate-key --activate <id> --actor <your name>"),
  ).toBeVisible();
  await shot(page, "organization-keyring");

  // The activation through the CLI, with the environment of the server.
  const run = spawnSync(
    resolve(process.env.MUSTER_E2E_BINARY ?? "../bin/muster"),
    ["dev", "secrets", "rotate-key", "--activate", other.id, "--actor", "ops"],
    {
      env: {
        ...process.env,
        MUSTER_DATABASE_URL: process.env.MUSTER_E2E_WEB_DATABASE_URL,
        MUSTER_SECRET_KEYS: process.env.MUSTER_E2E_SECRET_KEYS,
      },
      encoding: "utf8",
      timeout: 60_000,
    },
  );
  expect(run.status, run.stderr).toBe(0);
  expect(run.stderr).toContain(`key ${other.id} is active`);

  // Without a reload: the other key is active, and the first one is still needed until the open Root messages are
  // edited, then removable.
  await expect(row(other.id).getByTestId("keyring-key-state")).toHaveText("Active", {
    timeout: 30_000,
  });
  // A removable key says so in its own sentence, with "How to fix" beside it to the key rotation guide.
  const state = row(first.id).getByTestId("keyring-key-state");
  const removable = state.getByText(
    `Key ${first.id} is no longer used and can be removed from MUSTER_SECRET_KEYS.`,
    { exact: true },
  );
  await expect(state.getByText("Still needed", { exact: true }).or(removable)).toBeVisible();
  if (await removable.isVisible()) {
    const fix = state.getByRole("link", {
      name: "How to fix: Removing an unused master key (opens in a new tab)",
      exact: true,
    });
    await expect(fix).toHaveText("How to fix");
    const href = new URL((await fix.getAttribute("href")) ?? "");
    expect(href.origin).toBe("https://muster-io.github.io");
    expect(href.pathname).toBe("/muster/latest/operations/key-rotation/");
    expect(href.hash).toBe("");
  } else {
    await expect(state.getByRole("link")).toHaveCount(0);
  }
  await shot(page, "organization-keyring-activated");
  expect(csp).toEqual([]);
});

test("Security saves the token grace and shows a save over a newer version", async ({
  browser,
  page,
}) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  await page.goto("/admin/organization/security");

  // 6. "Token grace for OIDC users without an offline token" 3 days → "Saved."; a save made meanwhile elsewhere → the
  // conflict.
  const grace = page.getByLabel("Token grace for OIDC users without an offline token");
  await expect(grace).toHaveValue("7");
  await expect(page.getByRole("radio", { name: "Nobody" })).toBeChecked();
  await grace.fill("3");
  await save(page);
  await expect(page.getByTestId("security-status")).toHaveText("Saved.");
  const org = await withAdmin((admin) =>
    admin.call<{ oidc_token_grace_seconds: number }>("GET", "/api/v1/organization"),
  );
  expect(org.oidc_token_grace_seconds).toBe(3 * 86_400);
  await shot(page, "organization-security");

  const context = await browser.newContext();
  try {
    const second = await context.newPage();
    await signInAdmin(second);
    await second.goto("/admin/organization/security");
    const otherGrace = second.getByLabel("Token grace for OIDC users without an offline token");
    await expect(otherGrace).toHaveValue("3");
    await otherGrace.fill("5");
    await save(second);
    await expect(second.getByTestId("security-status")).toHaveText("Saved.");
  } finally {
    await context.close();
  }
  await grace.fill("4");
  await save(page);
  await expect(page.getByText(STALE)).toBeVisible();
  await page.getByRole("button", { name: "Reload", exact: true }).click();
  await expect(grace).toHaveValue("5");
  await expect(page.getByText(STALE)).toHaveCount(0);
  expect(csp).toEqual([]);
});

test("a Responder sees only Lookup tables and Link rules under Organization", async ({ page }) => {
  const csp = watchCsp(page);
  await withAdmin(async (admin) => {
    const { token } = await admin.createUser("org-rita", "responder");
    await admin.call("POST", "/api/v1/password-setups", { token, password: "org-rita-password-1" });
  });
  await signIn(page, "org-rita", "org-rita-password-1");

  // 7. "Organization" → only "Lookup tables" and "Link rules"; /admin/organization leads to Lookup tables; General
  // refuses.
  await expect(nav(page, "Organization")).toBeVisible();
  await nav(page, "Organization").click();
  await expect(page).toHaveURL(/\/admin\/organization\/lookup-tables$/);
  await expect(subNav(page).getByRole("link")).toHaveText(["Lookup tables", "Link rules"]);
  await page.goto("/admin/organization");
  await expect(page).toHaveURL(/\/admin\/organization\/lookup-tables$/);
  await page.goto("/admin/organization/general");
  await expect(page.getByText(NO_PERMISSION)).toBeVisible();
  await expect(subNav(page).getByRole("link")).toHaveText(["Lookup tables", "Link rules"]);
  expect(csp).toEqual([]);
});

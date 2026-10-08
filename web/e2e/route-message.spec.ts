// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Message section of the Route editor against `muster dev`: Verification steps 1 to 3 and 9 of its story. The fake
// Alertmanager sends HighLatency (pods a and b in cluster prod) to the Integration "msg-lab"; "Create route" offers the
// language and the three templates, "Alert line" → "Custom" starts from the built-in template and previews the typed
// one against the built-in example and then against the Alert Group, "Root message" → "Custom" → "Telegram" shows the
// built-in body as Telegram HTML, `{{ env "HOME" }}` is marked at line 1, column 4 and refused on save. A stored
// Route's editor previews against its most recent Stored Snapshots by default. A Route whose template keeps failing
// shows the banner and its mark in the Routes list. No Content Security Policy violation.

import { expect, test, type Locator, type Page } from "@playwright/test";

import {
  adminApi,
  createRoute,
  deleteRoute,
  devClockOffset,
  expectNoHorizontalScroll,
  fakeIntegration,
  fam,
  notify,
  shot,
  signInAdmin,
  sql,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

const LINE = "{{ .Labels.pod }} on {{ .Labels.cluster }}";
const UNKNOWN = "Line 1, column 4: Unknown function: env";

let integrationId = "";
let alertGroup = { id: "", number: 0 };
let podsRouteId = "";
let ownRouteId = "";
let oneRouteId = "";

/** The Alert Group of the Alert with pod="b", as the Integration's Alerts view names it. */
async function groupOfB(): Promise<{ id: string; number: number }> {
  const admin = await adminApi();
  try {
    const page = await admin.call<{ items: { alert_group?: { id: string; number: number } }[] }>(
      "GET",
      `/api/v1/integrations/${integrationId}/alerts?label=${encodeURIComponent('pod="b"')}`,
    );
    const group = page.items[0]?.alert_group;
    expect(group, "the alert group of pod b").toBeDefined();
    return group ?? { id: "", number: 0 };
  } finally {
    await admin.dispose();
  }
}

async function routeNames(): Promise<string[]> {
  const admin = await adminApi();
  try {
    const list = await admin.call<{ items: { name: string }[] }>("GET", "/api/v1/routes");
    return list.items.map((r) => r.name);
  } finally {
    await admin.dispose();
  }
}

function template(page: Page, name: "root_message" | "line" | "ack_timeout_notice"): Locator {
  return page.getByTestId(`template-${name}`);
}

test.beforeAll(async () => {
  integrationId = await fakeIntegration("msg-lab");
  // A Route of its own, so that the Alerts do not join a HighLatency Alert Group of an earlier spec.
  oneRouteId = await createRoute("msg-one", "msg-one", ["alertname"]);
  await fam("PUT", "/groups/msg1", {
    receiver: "msg-lab",
    route: "{}",
    labels: { alertname: "HighLatency", team: "msg-one" },
  });
  // The Alerts start at the business time, which earlier specs moved ahead of the real time, so that the Alert Group
  // is among the most recent ones the sample picker offers.
  const startsAt = new Date(Date.now() + (await devClockOffset()) * 1000).toISOString();
  await fam("PUT", "/groups/msg1/alerts/a", {
    labels: { cluster: "prod", namespace: "api" },
    starts_at: startsAt,
  });
  await fam("PUT", "/groups/msg1/alerts/b", {
    labels: { cluster: "prod", namespace: "api", pod: "b" },
    starts_at: startsAt,
  });
  await notify(integrationId, "msg1", { reason: "first notification" });
  alertGroup = await groupOfB();
});

test.afterAll(async () => {
  for (const id of [podsRouteId, ownRouteId, oneRouteId]) {
    if (id !== "") {
      await deleteRoute(id);
    }
  }
});

test("edits the message templates with live previews and refuses an unknown function", async ({
  page,
}) => {
  test.setTimeout(180_000);
  const csp = watchCsp(page);
  await signInAdmin(page);

  // 1. Routes → "Create route" → On-call → Message: the language and "Root message" built in.
  await page
    .getByRole("navigation", { name: "Main" })
    .getByRole("link", { name: "Routes" })
    .click();
  await page.getByRole("link", { name: "Create route" }).click();
  await page.getByRole("button", { name: "On-call" }).click();
  const message = page.getByTestId("route-message");
  await expect(message.getByRole("group", { name: "Message" })).toBeVisible();
  const language = message.getByLabel("Language");
  await expect(language.locator("option")).toHaveText(["English", "Русский"]);
  await expect(language).toHaveValue("en");
  await expect(
    template(page, "root_message").getByRole("radio", { name: "Built-in" }),
  ).toBeChecked();
  await expect(template(page, "line").getByRole("radio", { name: "Built-in" })).toBeChecked();
  await expect(
    template(page, "ack_timeout_notice").getByRole("radio", { name: "Built-in" }),
  ).toBeChecked();
  // The preview of a built-in template renders against the built-in example: a new Route has no snapshots.
  await expect(template(page, "line").getByTestId("template-preview-output")).toContainText(
    "pod: checkout-1",
  );

  // 2. "Alert line" → "Custom": the built-in line template; the typed one renders under it as it changes.
  const line = template(page, "line");
  await line.getByRole("radio", { name: "Custom" }).check();
  const lineEditor = line.getByRole("textbox", { name: "Alert line" });
  await expect(lineEditor).toHaveValue(/^\{\{ range \(\.Labels\.Remove \(stringSlice "alertname"/);
  await lineEditor.fill(LINE);
  await expect(line.getByTestId("template-preview-output")).toContainText("checkout-1 on prod");
  await expect(line.getByTestId("sample-picker").locator("option").first()).toHaveText(
    "Recent snapshots of this route",
  );
  await line.getByTestId("sample-picker").selectOption({ value: `alert_group:${alertGroup.id}` });
  await expect(line.getByTestId("template-preview-output")).toContainText("b on prod");
  await expect(line.getByTestId("template-preview-output")).not.toContainText("checkout-1");
  await shot(page, "route-message-line");

  // 3. "Root message" → "Custom" → "Telegram": the built-in body as Telegram HTML.
  const root = template(page, "root_message");
  await root.getByRole("radio", { name: "Custom" }).check();
  const rootEditor = root.getByRole("textbox", { name: "Root message" });
  await expect(rootEditor).toHaveValue(/Started \{\{ \.AlertGroup\.StartedAt/);
  await root.getByRole("radio", { name: "Telegram" }).check();
  const rootOutput = root.getByTestId("template-preview-output");
  await expect(rootOutput).toContainText("Error rate above 5%");
  await expect(rootOutput).toContainText("Started");
  await expect(rootOutput).toContainText("<a href=");
  await shot(page, "route-message-root-telegram");

  // {{ env "HOME" }}: marked at line 1, column 4, and the preview shows the error instead of a message.
  await rootEditor.fill('{{ env "HOME" }}');
  await expect(root.getByTestId("template-errors")).toHaveText(UNKNOWN);
  await expect(root.locator('[data-error="true"]')).toHaveText("env");
  await expect(root.getByTestId("template-preview-error")).toContainText(UNKNOWN);
  await expect(rootOutput).toHaveCount(0);
  await expect(rootEditor).toHaveAttribute("aria-invalid", "true");

  // "Save" refuses the Route with the same error at "Root message".
  await page.getByRole("textbox", { name: "Name" }).fill("msg-new");
  const refused = page.waitForResponse(
    (r) => r.url().endsWith("/api/v1/routes") && r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Create", exact: true }).click();
  const answer = await refused;
  expect(answer.status()).toBe(422);
  expect(await answer.json()).toMatchObject({
    errors: [
      { pointer: "/policy/templates/root_message", code: "unknown_function", line: 1, column: 4 },
    ],
  });
  await expect(root.getByTestId("template-errors")).toHaveText(UNKNOWN);
  await expect(rootEditor).toBeFocused();
  await expect(page).toHaveURL(/\/routes\/new$/);
  expect(await routeNames()).not.toContain("msg-new");

  // At phone width the editors scroll inside themselves; the page does not scroll sideways.
  await page.setViewportSize({ width: 360, height: 740 });
  await expect(rootEditor).toBeVisible();
  await expectNoHorizontalScroll(page);
  await shot(page, "route-message-360");
  expect(csp).toEqual([]);
});

test("shows a Route whose template keeps failing in the editor and the Routes list", async ({
  page,
}) => {
  const csp = watchCsp(page);
  podsRouteId = await createRoute("msg-pods", "msg-pods", ["alertname"]);
  // 9. No Destination type exists yet to make a template fail at delivery: the error state is set directly.
  await sql(
    `UPDATE routes SET template_error_since = now(), template_error = $1, template_error_template = 'line'
     WHERE public_id = $2`,
    ['map has no entry for key "pod"', podsRouteId],
  );
  await signInAdmin(page);
  await page.goto("/routes");
  const row = page
    .getByTestId("route-row")
    .filter({ has: page.getByRole("link", { name: "msg-pods", exact: true }) });
  await expect(row.getByTestId("route-template-error")).toHaveText("Template error");
  await row.getByRole("link", { name: "msg-pods", exact: true }).click();
  await expect(page.getByTestId("template-error-banner")).toHaveText(
    /^Template error since \d{2}:\d{2}: map has no entry for key "pod"\. Messages use the fallback template\.$/,
  );
  await shot(page, "route-template-error");
  expect(csp).toEqual([]);
});

test("previews against the most recent Stored Snapshots of a stored Route by default", async ({
  page,
}) => {
  const csp = watchCsp(page);
  // The Route "msg-own" takes HighLatency with team=msg-own: pods c and d in cluster prod.
  ownRouteId = await createRoute("msg-own", "msg-own", ["alertname"]);
  await fam("PUT", "/groups/msg2", {
    receiver: "msg-lab",
    route: "{}",
    labels: { alertname: "HighLatency", team: "msg-own" },
  });
  await fam("PUT", "/groups/msg2/alerts/c", { labels: { cluster: "prod", pod: "c" } });
  await fam("PUT", "/groups/msg2/alerts/d", { labels: { cluster: "prod", pod: "d" } });
  await notify(integrationId, "msg2", { reason: "first notification" });
  await signInAdmin(page);
  await page.goto(`/routes/${ownRouteId}`);
  const line = template(page, "line");
  await expect(line.getByTestId("sample-picker")).toHaveValue("default");
  await line.getByRole("radio", { name: "Custom" }).check();
  const editor = line.getByRole("textbox", { name: "Alert line" });
  await expect(editor).toHaveValue(/^\{\{ range/);
  await editor.fill(LINE);
  const output = line.getByTestId("template-preview-output");
  await expect(output).toContainText("c on prod");
  await expect(output).toContainText("d on prod");
  await expect(output).not.toContainText("checkout");
  await expect(line.getByText("Rendered against a built-in example", { exact: false })).toHaveCount(
    0,
  );
  expect(csp).toEqual([]);
});

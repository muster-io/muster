// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Routes list against `muster dev`: Verification steps 1 and 5 to 8 of the Routes story. The fake Alertmanager
// sends the group rr2 (severity none, P5 and none at all) to the Integration "lab-routes" while only "disk" and the
// Default route exist; then "info" (severity="info") is created with the Informational profile and dragged above
// "disk", newly firing Disk alerts with severity="info" follow the order, a second browser moves "disk" back and a third move over the old order is refused; the Heartbeat
// suggestion creates its Route, is dismissed for one user only, and a Responder sees everything read-only.

import { expect, test, type Locator, type Page } from "@playwright/test";

import { adminApi, expectNoHorizontalScroll, shot, signIn, signInAdmin, watchCsp } from "./support";

const FAM = "http://127.0.0.1:19093/_fake";
const INGEST_URL = "http://localhost:8081/api/v1/ingest";
const SUGGESTION =
  "Muster raises MusterHeartbeatLost when an Integration loses its Heartbeat, and only the Default route takes it now.";
const STALE_ORDER = "Someone else changed the order. Reload to see it.";

test.describe.configure({ mode: "serial" });

let labId = "";

interface ApiRoute {
  id: string;
  name: string;
  is_default: boolean;
}

async function fam(method: string, path: string, body: unknown): Promise<void> {
  const res = await fetch(`${FAM}${path}`, { method, body: JSON.stringify(body) });
  expect(res.ok, `${method} ${path}: ${res.status} ${await res.text()}`).toBe(true);
}

/** Sends a notification of a fake group and waits until Muster processed every Snapshot of "lab-routes". */
async function notify(name: string, reason: string): Promise<void> {
  await fam("POST", `/groups/${name}/notify`, { reason });
  const admin = await adminApi();
  try {
    await expect
      .poll(async () => {
        const page = await admin.call<{ items: unknown[] }>(
          "GET",
          `/api/v1/stored-snapshots?integration=${labId}&state=pending`,
        );
        return page.items.length;
      })
      .toBe(0);
  } finally {
    await admin.dispose();
  }
}

/** The Route that took the Alert of "lab-routes" with the label node, as the API reads it. */
async function routeOfAlert(node: string): Promise<string | undefined> {
  const admin = await adminApi();
  try {
    const page = await admin.call<{ items: { route?: { name: string } }[] }>(
      "GET",
      `/api/v1/integrations/${labId}/alerts?label=${encodeURIComponent(`node="${node}"`)}`,
    );
    return page.items[0]?.route?.name;
  } finally {
    await admin.dispose();
  }
}

async function routes(): Promise<ApiRoute[]> {
  const admin = await adminApi();
  try {
    return (await admin.call<{ items: ApiRoute[] }>("GET", "/api/v1/routes")).items;
  } finally {
    await admin.dispose();
  }
}

/** "lab-routes" with its fake receiver, the group rr2 sent once, a "disk" Route and an Integration with a Heartbeat. */
async function prepare(): Promise<void> {
  const admin = await adminApi();
  const lab = await admin.call<{ id: string }>("POST", "/api/v1/integrations", {
    name: "lab-routes",
    connection_mode: "webhook_only",
    static_labels: {},
    duplicate_window_seconds: 45,
    heartbeat: { enabled: false },
  });
  labId = lab.id;
  const token = await admin.call<{ value: string }>(
    "POST",
    `/api/v1/integrations/${lab.id}/tokens`,
    {
      name: "fake",
    },
  );
  await admin.call("POST", "/api/v1/integrations", {
    name: "hb-routes",
    connection_mode: "webhook_only",
    static_labels: {},
    duplicate_window_seconds: 45,
    heartbeat: { enabled: true, timeout_seconds: 300 },
  });
  // "disk" as the Routes story's preview spec leaves it, when this spec runs alone.
  const existing = await admin.call<{ items: ApiRoute[] }>("GET", "/api/v1/routes");
  if (!existing.items.some((r) => r.name === "disk")) {
    const profiles = await admin.call<{ items: { id: string; policy: unknown }[] }>(
      "GET",
      "/api/v1/route-profiles",
    );
    await admin.call("POST", "/api/v1/routes", {
      name: "disk",
      matchers: [{ label: "alertname", op: "=", value: "Disk" }],
      urgent: false,
      group_key: ["alertname", "cluster"],
      destination_ids: [],
      policy: profiles.items.find((p) => p.id === "on_call")?.policy,
    });
  }
  await admin.dispose();
  const res = await fetch(`${FAM}/receivers`, {
    method: "POST",
    body: JSON.stringify({ name: "lab-routes", url: INGEST_URL, token: token.value }),
  });
  expect(res.status).toBe(204);
  await fam("PUT", "/groups/rr2", {
    receiver: "lab-routes",
    route: "{}",
    labels: { alertname: "Sev" },
  });
  await fam("PUT", "/groups/rr2/alerts/n", { labels: { severity: "none", k: "none" } });
  await fam("PUT", "/groups/rr2/alerts/p", { labels: { severity: "P5", k: "p5" } });
  await fam("PUT", "/groups/rr2/alerts/m", { labels: { k: "missing" } });
  await notify("rr2", "first notification");
  await fam("PUT", "/groups/rdisk", {
    receiver: "lab-routes",
    route: "{}",
    labels: { alertname: "Disk" },
  });
}

function rows(page: Page): Locator {
  return page.getByTestId("route-row");
}

function routeRow(page: Page, name: string): Locator {
  return rows(page).filter({ has: page.getByRole("link", { name, exact: true }) });
}

function names(page: Page): Locator {
  return rows(page).getByRole("link");
}

/** Drags a Route by its handle onto the upper half of another Route's row and waits for the answer of its save. */
async function dragAbove(page: Page, name: string, target: string): Promise<void> {
  const saved = page.waitForResponse(
    (r) => r.url().endsWith("/api/v1/route-order") && r.request().method() === "PUT",
  );
  await routeRow(page, name)
    .getByTestId("drag-handle")
    .dragTo(routeRow(page, target), { targetPosition: { x: 40, y: 4 } });
  await saved;
}

/** A new alert of the group rdisk with severity="info", sent and processed. */
async function newDiskAlert(node: string): Promise<void> {
  await fam("PUT", `/groups/rdisk/alerts/${node}`, { labels: { node, severity: "info" } });
  await notify("rdisk", "new alerts added");
}

test("lists the Routes, reorders them by dragging and refuses a move over a newer order", async ({
  page,
  browser,
}) => {
  test.setTimeout(240_000);
  const csp = watchCsp(page);
  await prepare();
  // The first browser gets no live hints, as if it missed the second browser's move.
  await page.route("**/api/v1/live-updates", (route) => route.abort());
  await signInAdmin(page);

  // 1. The list ends with "Default", which has no drag handle and no move buttons.
  await page
    .getByRole("navigation", { name: "Main" })
    .getByRole("link", { name: "Routes" })
    .click();
  await expect(names(page).last()).toHaveText("Default");
  const defaultRow = routeRow(page, "Default");
  await expect(defaultRow).toHaveAttribute("data-default", "true");
  await expect(defaultRow.getByTestId("drag-handle")).toHaveCount(0);
  await expect(defaultRow.getByRole("button")).toHaveCount(0);
  await expect(defaultRow).toContainText("Takes every alert no other route took. Always last.");
  await expect(routeRow(page, "disk").getByTestId("drag-handle")).toHaveCount(1);
  await shot(page, "routes-list");

  // 5. "Create route" → "Informational" → "info" for severity="info": it goes just before the Default route.
  await page.getByRole("link", { name: "Create route" }).click();
  await page.getByRole("button", { name: "Informational" }).click();
  await page.getByRole("textbox", { name: "Name" }).fill("info");
  await page.getByRole("button", { name: "Add matcher" }).click();
  await page.getByRole("textbox", { name: "Label of matcher 1" }).fill("severity");
  await page.getByRole("textbox", { name: "Value of matcher 1" }).fill("info");
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page).toHaveURL(/\/routes$/);
  await expect(names(page).nth(-2)).toHaveText("info");
  await expect(routeRow(page, "info").getByTestId("route-matchers")).toHaveText('severity="info"');

  // Dragging "info" above "disk" saves the order; it holds over a reload.
  await dragAbove(page, "info", "disk");
  await expect(names(page)).toHaveText(["info", "disk", "Default"]);
  await expect
    .poll(async () => (await routes()).map((r) => r.name))
    .toEqual(["info", "disk", "Default"]);
  await page.reload();
  await expect(names(page)).toHaveText(["info", "disk", "Default"]);
  // A newly firing Disk alert with severity="info" matches both; it now goes to "info", which comes first.
  await newDiskAlert("order-1");
  expect(await routeOfAlert("order-1")).toBe("info");

  // A second browser drags "disk" back above "info"; the next such alert goes to "disk".
  const other = await browser.newContext();
  const second = await other.newPage();
  const secondCsp = watchCsp(second);
  await signInAdmin(second);
  await second.goto("/routes");
  await expect(names(second)).toHaveText(["info", "disk", "Default"]);
  await dragAbove(second, "disk", "info");
  await expect(names(second)).toHaveText(["disk", "info", "Default"]);
  await expect
    .poll(async () => (await routes()).map((r) => r.name))
    .toEqual(["disk", "info", "Default"]);
  await newDiskAlert("order-2");
  expect(await routeOfAlert("order-2")).toBe("disk");
  expect(await routeOfAlert("order-1")).toBe("info");
  expect(secondCsp).toEqual([]);
  await other.close();

  // The first browser still shows the old order; moving over it is refused and the order it read comes back.
  await expect(names(page)).toHaveText(["info", "disk", "Default"]);
  await dragAbove(page, "disk", "info");
  await expect(page.getByTestId("route-order-stale")).toHaveText(`${STALE_ORDER}Reload`);
  await expect(names(page)).toHaveText(["info", "disk", "Default"]);
  await shot(page, "routes-order-conflict");
  await page.getByRole("button", { name: "Reload" }).click();
  await expect(names(page)).toHaveText(["disk", "info", "Default"]);
  await expect(page.getByTestId("route-order-stale")).toHaveCount(0);
  // The refusal is gone with its banner: no other error takes its place.
  await expect(
    page.getByText("Someone else changed these settings. Reload to see them."),
  ).toHaveCount(0);
  await expect(page.getByRole("alert")).toHaveCount(0);

  // The keyboard moves a Route with "Move down"; the focus stays on the button and the move is announced.
  const down = page.getByRole("button", { name: "Move disk down" });
  await down.focus();
  await page.keyboard.press("Enter");
  await expect(names(page)).toHaveText(["info", "disk", "Default"]);
  await expect(page.getByRole("button", { name: "Move disk down" })).toBeFocused();
  await expect(
    page.getByRole("status").filter({ hasText: "disk moved to position 2 of 3." }),
  ).toBeAttached();
  await page.keyboard.press("Enter");
  await expect(names(page)).toHaveText(["info", "disk", "Default"]);
  await expect(page.getByRole("button", { name: "Move disk down" })).toHaveAttribute(
    "aria-disabled",
    "true",
  );
  await page.getByRole("button", { name: "Move disk up" }).click();
  await expect(names(page)).toHaveText(["disk", "info", "Default"]);
  await expect
    .poll(async () => (await routes()).map((r) => r.name))
    .toEqual(["disk", "info", "Default"]);

  // 360 px wide with the move controls: the page scrolls vertically only.
  await page.setViewportSize({ width: 360, height: 800 });
  await expect(page.getByRole("button", { name: "Move info up" })).toBeVisible();
  await expectNoHorizontalScroll(page);
  await shot(page, "routes-list-360");
  expect(csp).toEqual([]);
});

test("the Heartbeat suggestion creates its Route at the top, and a dismissal holds for one user", async ({
  page,
  browser,
}) => {
  test.setTimeout(120_000);
  const csp = watchCsp(page);
  await signInAdmin(page);
  await page.goto("/routes");

  // 6. The suggestion is on top; "Create the route" adds "Muster: Heartbeat lost" first and the suggestion is gone.
  const suggestion = page.getByTestId("route-suggestion");
  await expect(suggestion).toContainText(SUGGESTION);
  await shot(page, "routes-suggestion");
  await suggestion.getByRole("button", { name: "Create the route" }).click();
  await expect(names(page).first()).toHaveText("Muster: Heartbeat lost");
  await expect(names(page).first()).toBeFocused();
  await expect(suggestion).toHaveCount(0);
  await expect(routeRow(page, "Muster: Heartbeat lost").getByTestId("route-matchers")).toHaveText(
    'alertname="MusterHeartbeatLost"',
  );
  await page.reload();
  await expect(names(page).first()).toHaveText("Muster: Heartbeat lost");
  await expect(suggestion).toHaveCount(0);

  // Deleting the Route brings the suggestion back; "Dismiss" hides it for the Admin only.
  await routeRow(page, "Muster: Heartbeat lost").getByRole("link").click();
  await page.getByRole("button", { name: "Delete" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading")).toHaveText("Delete route Muster: Heartbeat lost?");
  await expect(dialog).toContainText("Alerts it would take go to the next matching Route.");
  await dialog.getByRole("button", { name: "Delete" }).click();
  await expect(page).toHaveURL(/\/routes$/);
  await expect(routeRow(page, "Muster: Heartbeat lost")).toHaveCount(0);
  await expect(suggestion).toContainText(SUGGESTION);
  await suggestion.getByRole("button", { name: "Dismiss" }).click();
  await expect(suggestion).toHaveCount(0);
  await expect(page.getByRole("heading", { level: 1, name: "Routes" })).toBeFocused();
  await page.reload();
  await expect(names(page).first()).toHaveText("disk");
  await expect(suggestion).toHaveCount(0);

  // 8. A Responder: the suggestion without its actions, no "Create route", no move controls, "disk" read-only.
  const admin = await adminApi();
  const { token } = await admin.createUser("routes-responder", "responder");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "routes-responder-1" });
  await admin.dispose();
  const context = await browser.newContext({ viewport: { width: 360, height: 800 } });
  const responder = await context.newPage();
  const responderCsp = watchCsp(responder);
  await signIn(responder, "routes-responder", "routes-responder-1");
  await responder
    .getByRole("navigation", { name: "Main" })
    .getByRole("link", { name: "Routes" })
    .click();
  await expect(names(responder)).toHaveText(["disk", "info", "Default"]);
  await expect(responder.getByTestId("route-suggestion")).toContainText(SUGGESTION);
  await expect(responder.getByTestId("route-suggestion").getByRole("button")).toHaveCount(0);
  await expect(responder.getByRole("link", { name: "Create route" })).toHaveCount(0);
  await expect(responder.getByTestId("drag-handle")).toHaveCount(0);
  await expect(responder.getByTestId("route-list").getByRole("button")).toHaveCount(0);
  await expectNoHorizontalScroll(responder);
  await shot(responder, "routes-list-responder-360");
  await routeRow(responder, "disk").getByRole("link").click();
  await expect(responder.getByTestId("route-read-only")).toBeVisible();
  await expect(responder.getByText("You can view this route but not change it.")).toBeVisible();
  await expect(responder.getByTestId("matcher-text")).toHaveText(['alertname="Disk"']);
  for (const name of ["Save", "Delete", "Preview", "Add matcher"]) {
    await expect(responder.getByRole("button", { name })).toHaveCount(0);
  }
  await expect(responder.getByRole("textbox")).toHaveCount(0);
  await expectNoHorizontalScroll(responder);
  await responder.goto("/routes/new");
  await expect(responder.getByText("You do not have permission to see this page.")).toBeVisible();
  expect(responderCsp).toEqual([]);
  await context.close();
  expect(csp).toEqual([]);
});

test("the Alerts view shows each Alert's Route and Severity level", async ({ page }) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  // 7. Integrations → "lab-routes" → Alerts: severity P5 shows "warning (P5)", none and a missing one show "info".
  await page
    .getByRole("navigation", { name: "Main" })
    .getByRole("link", { name: "Integrations" })
    .click();
  await page.getByRole("link", { name: "lab-routes", exact: true }).click();
  const table = page.getByRole("table", { name: "Alerts" });
  await expect(table.getByRole("columnheader", { name: "Route" })).toBeVisible();
  await expect(table.getByRole("columnheader", { name: "Severity" })).toBeVisible();
  const alert = (k: string) => table.getByRole("row").filter({ hasText: `k=${k}` });
  await expect(alert("p5").getByTestId("alert-route")).toHaveText("Default");
  await expect(alert("p5").getByTestId("alert-severity")).toHaveText("warning (P5)");
  await expect(alert("none").getByTestId("alert-severity")).toHaveText("info");
  await expect(alert("missing").getByTestId("alert-severity")).toHaveText("info");
  const order2 = table.getByRole("row").filter({ hasText: "node=order-2" });
  await expect(order2.getByTestId("alert-route")).toHaveText("disk");
  await shot(page, "routes-alerts-view");
  await order2.getByTestId("alert-route").click();
  await expect(page.getByRole("heading", { level: 1, name: "disk" })).toBeVisible();
  expect(csp).toEqual([]);
});

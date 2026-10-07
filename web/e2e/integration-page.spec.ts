// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Integration page against `muster dev`: the Verification steps of the story on learned Alertmanager routes,
// warnings and the Alerts view. The fake Alertmanager sends groups to the Integration "lab" and the development clock
// moves between them: the built-in Integration "Muster", learned repeat intervals, the long-interval warning with its
// snippet, truncation in the page and the list, the Static label warning, a Gone Alert, the Matcher filter with the
// server's error, label values with markup shown as text, then the pages in Russian and 360 px wide.

import { expect, test, type Locator, type Page } from "@playwright/test";

import {
  APP,
  INGEST_URL,
  adminApi,
  advance,
  expectNoHorizontalScroll,
  fam,
  notify,
  shot,
  signIn,
  signInAdmin,
  watchCsp,
} from "./support";

const MARKUP = "<img src=x onerror=alert(1)>";
const TEMPLATE = "{{count}} $t(errors.generic)";
const MARKUP_ROUTE = '{}/{team="<img src=x onerror=alert(2)>"}';
const GONE_TEXT =
  "Alertmanager no longer reports this alert — it resolved without notice, was silenced or inhibited in Alertmanager, or the Alertmanager routing changed";

test.describe.configure({ mode: "serial" });

let labId = "";
let builtinId = "";

async function group(
  name: string,
  route: string,
  labels: Record<string, string>,
  alerts: Record<string, Record<string, string>>,
): Promise<void> {
  await fam("PUT", `/groups/${name}`, { receiver: "lab", route, labels });
  for (const [id, alertLabels] of Object.entries(alerts)) {
    await fam("PUT", `/groups/${name}/alerts/${id}`, { labels: alertLabels });
  }
}

/** "lab" with the Static label cluster=b and a token that the fake Alertmanager's receiver "lab" sends with. */
async function prepare(): Promise<void> {
  const admin = await adminApi();
  const lab = await admin.call<{ id: string }>("POST", "/api/v1/integrations", {
    name: "lab",
    connection_mode: "webhook_only",
    static_labels: { cluster: "b" },
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
  const list = await admin.call<{ items: { id: string; builtin: boolean }[] }>(
    "GET",
    "/api/v1/integrations",
  );
  builtinId = list.items.find((i) => i.builtin)?.id ?? "";
  expect(builtinId).not.toBe("");
  await admin.dispose();
  await fam("POST", "/receivers", { name: "lab", url: INGEST_URL, token: token.value });
}

function integrationRow(page: Page, name: string, table = "Integrations"): Locator {
  return page
    .getByRole("table", { name: table })
    .getByRole("row")
    .filter({ has: page.getByRole("link", { name, exact: true }) });
}

function routeRow(page: Page, path: string): Locator {
  return page
    .getByTestId("alertmanager-route")
    .filter({ has: page.getByTestId("route-path").getByText(path, { exact: true }) });
}

function alertRows(page: Page, table = "Alerts"): Locator {
  return page
    .getByRole("table", { name: table })
    .getByRole("row")
    .filter({
      has: page.getByRole("cell"),
    });
}

/** The cell of a row of the Alerts view in the column with this header, wherever the column is. */
async function alertCell(page: Page, row: Locator, header: string): Promise<Locator> {
  const headers = await page
    .getByRole("table", { name: "Alerts" })
    .getByRole("columnheader")
    .allTextContents();
  const index = headers.indexOf(header);
  expect(index, `the column ${header} among ${headers.join(", ")}`).toBeGreaterThanOrEqual(0);
  return row.getByRole("cell").nth(index);
}

async function addMatcher(page: Page, matcher: string): Promise<void> {
  await page.getByRole("textbox", { name: "Label filters" }).fill(matcher);
  await page.getByRole("button", { name: "Add matcher" }).click();
  await expect(page.getByTestId("matcher-chip").getByText(matcher, { exact: true })).toBeVisible();
}

test("shows learned routes, warnings, the built-in Integration and the Alerts view", async ({
  page,
}) => {
  test.setTimeout(240_000);
  const csp = watchCsp(page);
  const dialogs: string[] = [];
  page.on("dialog", (d) => {
    dialogs.push(d.message());
    void d.dismiss();
  });
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
  await prepare();
  await signInAdmin(page);

  // 1. The list shows "Muster" marked "Built-in" and "lab"; the page of "Muster" explains it and has no controls.
  await page
    .getByRole("navigation", { name: "Main" })
    .getByRole("link", { name: "Integrations" })
    .click();
  await expect(integrationRow(page, "Muster").getByTestId("builtin-badge")).toHaveText("Built-in");
  await expect(integrationRow(page, "lab")).toBeVisible();
  await expect(integrationRow(page, "lab").getByTestId("builtin-badge")).toHaveCount(0);
  await integrationRow(page, "Muster").getByRole("link").click();
  await expect(page).toHaveURL(`${APP}/integrations/${builtinId}`);
  await expect(page.getByTestId("builtin-explanation")).toHaveText(
    "Muster raises its Internal alerts through this Integration. It has no tokens and cannot be changed.",
  );
  await expect(page.getByRole("table", { name: "Alerts" })).toBeVisible();
  await expect(page.getByRole("table", { name: "Stored Snapshots" })).toBeVisible();
  for (const name of ["Edit", "Delete", "Create token"]) {
    await expect(
      page.getByRole("button", { name }).or(page.getByRole("link", { name })),
    ).toHaveCount(0);
  }
  await expect(page.getByTestId("alertmanager-routes")).toHaveCount(0);

  // 2. Group g6 four times 300 s apart: the route {}/{team="web"} learns 5 minutes, resolving by absence after 15.
  await group("g6", '{}/{team="web"}', { alertname: "Slow" }, { s: { host: "web-1" } });
  await notify(labId, "g6", { reason: "first notification" });
  for (let k = 0; k < 3; k++) {
    await advance(300);
    await notify(labId, "g6", { reason: "repeat interval elapsed" });
  }
  // 3. Group g7 once: not learned yet; again two hours later: the long-interval warning with its snippet.
  await group(
    "g7",
    '{}/{kind="info"}',
    { alertname: "CertExpiry" },
    { c: { domain: "example.org" } },
  );
  await notify(labId, "g7", { reason: "first notification" });
  await page.goto(`/integrations/${labId}`);
  const web = routeRow(page, '{}/{team="web"}');
  await expect(web.getByTestId("route-repeat-interval")).toHaveText("5 min");
  await expect(web.getByTestId("route-resolves-after")).toHaveText("15 min");
  await expect(page.getByRole("columnheader", { name: "Resolves by absence after" })).toBeVisible();
  await expect(routeRow(page, '{}/{kind="info"}').getByTestId("route-repeat-interval")).toHaveText(
    "Not learned yet",
  );
  await expect(routeRow(page, '{}/{kind="info"}').getByTestId("route-resolves-after")).toHaveText(
    "25 h",
  );

  await advance(7200);
  await notify(labId, "g7", { reason: "repeat interval elapsed" });
  await page.reload();
  const longText =
    'Alertmanager route {}/{kind="info"} repeats every 2 h; Muster can resolve its alerts by absence only after 6 h. Use a repeat interval of 5–15 minutes on the route to Muster.';
  const warning = page.getByTestId("route-long-interval");
  await expect(warning).toContainText(longText);
  await expect(page.getByTestId("integration-warning").filter({ hasText: longText })).toHaveCount(
    1,
  );
  await expect(page.getByTestId("route-snippet")).toContainText("repeat_interval: 10m");
  const admin = await adminApi();
  const routes = await admin.call<{
    items: { route_path: string; recommended_snippet: string | null }[];
  }>("GET", `/api/v1/integrations/${labId}/alertmanager-routes`);
  const snippet = routes.items.find(
    (r) => r.route_path === '{}/{kind="info"}',
  )?.recommended_snippet;
  expect(snippet).toContain("repeat_interval: 10m");
  await warning.getByRole("button", { name: "Copy" }).click();
  await expect(warning.getByRole("button", { name: "Copied" })).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(snippet);
  await shot(page, "lab-page-routes");

  // 4. Group g2 cut to two Alerts: the truncation warning (one group) on the page and the mark in the list; the
  // built-in Integration lists MusterSnapshotTruncated as firing.
  await group(
    "g2",
    "{}",
    { alertname: "PodDown" },
    Object.fromEntries(Array.from({ length: 10 }, (_, i) => [`x${i}`, { pod: `p${i}` }])),
  );
  await notify(labId, "g2", { reason: "first notification" });
  await advance(60);
  await notify(labId, "g2", { reason: "repeat interval elapsed", max_alerts: 2 });
  const truncated =
    "Alertmanager truncates Snapshots for 1 group. Set max_alerts: 0 on the Alertmanager receiver.";
  await page.reload();
  await expect(
    page.getByTestId("integration-warning").filter({ hasText: truncated }),
  ).toBeVisible();
  await expect(routeRow(page, "{}").getByTestId("route-truncated-groups")).toHaveText("1");

  await page.goto("/integrations");
  const mark = integrationRow(page, "lab").getByTestId("integration-warning-mark");
  await expect(mark.locator("summary")).toHaveText("2 warnings");
  await expect(mark.locator("summary")).toHaveAttribute("title", `${truncated}\n${longText}`);
  await mark.locator("summary").click();
  await expect(mark.getByText(truncated)).toBeVisible();
  await expect(mark.getByText(longText)).toBeVisible();
  await expect(integrationRow(page, "Muster").getByTestId("integration-warning-mark")).toHaveCount(
    0,
  );
  await shot(page, "integrations-list-warnings");
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await shot(page, "integrations-list-warnings-360");
  await page.setViewportSize({ width: 1280, height: 800 });

  await integrationRow(page, "Muster").getByRole("link").click();
  const internal = alertRows(page).filter({ hasText: "alertname=MusterSnapshotTruncated" });
  await expect(internal.getByTestId("alert-state")).toHaveText("Firing");
  await expect(internal.getByTestId("alert-label").first()).toHaveText(
    "alertname=MusterSnapshotTruncated",
  );
  await expect(internal.getByText("integration_name=lab", { exact: true })).toBeVisible();
  await shot(page, "builtin-integration");
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await shot(page, "builtin-integration-360");
  await page.setViewportSize({ width: 1280, height: 800 });

  // 5. Group g1: db-a carries cluster=a, so the Static label cluster=b is not applied to it.
  await group(
    "g1",
    '{}/{team="db"}',
    { alertname: "DiskFull" },
    { a: { instance: "db-a", cluster: "a" }, b: { instance: "db-b" }, c: { instance: "db-c" } },
  );
  await notify(labId, "g1", { reason: "first notification" });
  // A group whose route and labels carry markup and template syntax: shown as text.
  await group(
    "g8",
    MARKUP_ROUTE,
    { alertname: "Markup" },
    { m: { payload: MARKUP, note: TEMPLATE } },
  );
  await notify(labId, "g8", { reason: "first notification" });

  await page.goto(`/integrations/${labId}`);
  await expect(page.getByRole("tab", { name: "Firing" })).toHaveAttribute("aria-selected", "true");
  await addMatcher(page, 'instance="db-a"');
  await expect(alertRows(page)).toHaveCount(1);
  const dbA = alertRows(page).first();
  await expect(dbA.getByText("cluster=a", { exact: true })).toBeVisible();
  // startsAt and the time last seen, in the browser's time zone.
  const shown = /^[A-Z][a-z]{2} \d{1,2}, \d{4}, \d{2}:\d{2}$/;
  await expect(await alertCell(page, dbA, "Started")).toHaveText(shown);
  await expect(await alertCell(page, dbA, "Last seen")).toHaveText(shown);
  await expect((await alertCell(page, dbA, "Alert Group")).getByRole("link")).toHaveText(/^#\d+$/);
  await expect(dbA.getByTestId("static-label-warning")).toHaveText(
    "Static label cluster not applied: the alert has its own value.",
  );
  await expect(dbA.getByTestId("alert-label").first()).toHaveText("alertname=DiskFull");
  await dbA.getByTestId("alert-groups").locator("summary").click();
  await expect(
    dbA.getByTestId("alert-groups").getByText('{}/{team="db"}:{alertname="DiskFull"}'),
  ).toBeVisible();
  await expect(page).toHaveURL(/alerts_label=/);

  // 6. db-c missing from Snapshots past processing.gone_min_absence: Gone, with the full reason on hover.
  await advance(20);
  await notify(labId, "g1", { reason: "repeat interval elapsed", list: ["a", "b"] });
  await advance(60);
  await notify(labId, "g1", { reason: "repeat interval elapsed", list: ["a", "b"] });
  await advance(300);
  await notify(labId, "g1", { reason: "repeat interval elapsed", list: ["a", "b"] });
  await page.getByRole("button", { name: 'Remove matcher instance="db-a"' }).click();
  await expect(page.getByTestId("matcher-chip")).toHaveCount(0);
  await page.getByRole("tab", { name: "Resolved" }).click();
  await expect(page).toHaveURL(/alerts_state=resolved/);
  await addMatcher(page, 'instance="db-c"');
  await expect(alertRows(page)).toHaveCount(1);
  const gone = alertRows(page).first().getByTestId("alert-state");
  await expect(gone).toHaveText("Resolved: Gone");
  await expect(gone).toHaveAttribute("title", GONE_TEXT);
  await gone.hover();
  await expect(gone).toBeVisible();
  // ...and by a tap, for a phone: the details under the state open with the full reason (C-09.FR-24).
  const reason = alertRows(page).first().getByTestId("alert-reason");
  await expect(reason).toBeHidden();
  await gone.click();
  await expect(reason).toHaveText(GONE_TEXT);
  await expect(alertRows(page).first().locator("time")).not.toHaveCount(0);

  // 7. The syntax is checked as typed; a regular expression that does not compile gets the server's error under the
  // field, against the value as typed, and the rows and the URL stay.
  const url = page.url();
  const field = page.getByRole("textbox", { name: "Label filters" });
  await field.fill('pod=~"[');
  await expect(page.getByTestId("matcher-error")).toHaveText(
    "Close the quoted value with a quote.",
  );
  await expect(page.getByRole("button", { name: "Add matcher" })).toBeDisabled();
  await field.fill('pod=~"["');
  await page.getByRole("button", { name: "Add matcher" }).click();
  await expect(page.getByTestId("matcher-error")).toHaveText(
    "The regular expression is not valid: error parsing regexp: missing closing ]: `[`",
  );
  await expect(field).toHaveAttribute("aria-invalid", "true");
  await expect(field).toHaveValue('pod=~"["');
  await expect(alertRows(page)).toHaveCount(1);
  await expect(gone).toBeVisible();
  expect(page.url()).toBe(url);
  await shot(page, "alerts-view-matcher-error");

  // State tabs, text search and the sort; markup in labels and routes is inert text.
  await field.fill("");
  await page.getByRole("button", { name: 'Remove matcher instance="db-c"' }).click();
  await page.getByRole("tab", { name: "All" }).click();
  await expect(alertRows(page).filter({ hasText: "instance=db-c" })).toHaveCount(1);
  await expect(alertRows(page).filter({ hasText: "instance=db-a" })).toHaveCount(1);
  await page.getByRole("tab", { name: "Firing" }).click();
  await expect(alertRows(page).filter({ hasText: "instance=db-c" })).toHaveCount(0);
  await page.getByLabel("Search").fill("web-1");
  await expect(alertRows(page)).toHaveCount(1);
  await expect(alertRows(page).first().getByText("host=web-1", { exact: true })).toBeVisible();
  await expect(page).toHaveURL(/alerts_q=web-1/);
  await page.getByLabel("Search").fill("");
  await page.getByLabel("Sort by").selectOption({ label: "Started, oldest first" });
  await expect(page).toHaveURL(/alerts_sort=starts_at/);

  const markup = alertRows(page).filter({ hasText: "alertname=Markup" });
  await expect(markup.getByText(`payload=${MARKUP}`, { exact: true })).toBeVisible();
  await expect(markup.getByText(`note=${TEMPLATE}`, { exact: true })).toBeVisible();
  await expect(routeRow(page, MARKUP_ROUTE)).toBeVisible();
  await expect(page.locator("main img")).toHaveCount(0);
  await shot(page, "alerts-view");

  // At 360 px the page scrolls vertically only; the tables scroll inside their own boxes.
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await shot(page, "lab-page-360");
  await page.setViewportSize({ width: 1280, height: 800 });

  await admin.dispose();
  expect(dialogs).toEqual([]);
  expect(csp).toEqual([]);
});

test("shows the routes, the warnings and the Alerts view in Russian", async ({ page }) => {
  const csp = watchCsp(page);
  const admin = await adminApi();
  // A Viewer: an extra Admin would change what later specs see as the last active Admin.
  const { token } = await admin.createUser("ilya", "viewer");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "ilya-password-1" });
  await admin.dispose();
  await signIn(page, "ilya", "ilya-password-1");
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();

  await page.goto("/integrations");
  await expect(
    integrationRow(page, "Muster", "Интеграции").getByTestId("builtin-badge"),
  ).toHaveText("Встроенная");
  await expect(
    integrationRow(page, "lab", "Интеграции")
      .getByTestId("integration-warning-mark")
      .locator("summary"),
  ).toHaveText("2 предупреждения");
  await shot(page, "integrations-list-warnings-ru");

  await page.goto(`/integrations/${labId}`);
  await expect(
    page.getByTestId("integration-warning").filter({
      hasText:
        "Alertmanager обрезает снимки для 1 группы. Задайте max_alerts: 0 в получателе Alertmanager.",
    }),
  ).toBeVisible();
  await expect(page.getByTestId("route-long-interval")).toContainText(
    'У маршрута Alertmanager {}/{kind="info"} интервал повтора 2 ч; Muster сможет закрывать его алерты по отсутствию только через 6 ч.',
  );
  await expect(routeRow(page, '{}/{team="web"}').getByTestId("route-repeat-interval")).toHaveText(
    "5 мин",
  );
  // A Viewer reads the routes and the Alerts view, and has no Stored Snapshots, tokens to create, Edit or Delete.
  await expect(page.getByRole("table", { name: "Сохранённые снимки" })).toHaveCount(0);
  for (const name of ["Изменить", "Удалить", "Создать токен"]) {
    await expect(
      page
        .getByRole("button", { name, exact: true })
        .or(page.getByRole("link", { name, exact: true })),
    ).toHaveCount(0);
  }
  await page.getByRole("tab", { name: "Закрытые" }).click();
  await expect(
    alertRows(page, "Алерты").filter({ hasText: "instance=db-c" }).getByTestId("alert-state"),
  ).toContainText("Закрыт: пропал");
  await page.getByRole("tab", { name: "Горят" }).click();
  await expect(
    alertRows(page, "Алерты")
      .filter({ hasText: "instance=db-a" })
      .getByTestId("static-label-warning"),
  ).toHaveText("Статическая метка cluster не применена: у алерта своё значение.");
  await expect(
    alertRows(page, "Алерты")
      .filter({ hasText: "instance=db-a" })
      .getByTestId("alert-groups")
      .locator("summary"),
  ).toHaveText("1 группа");
  await shot(page, "lab-page-ru");
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await shot(page, "lab-page-ru-360");
  await page.setViewportSize({ width: 1280, height: 800 });

  await page.goto(`/integrations/${builtinId}`);
  await expect(page.getByTestId("builtin-explanation")).toHaveText(
    "Через эту интеграцию Muster поднимает свои внутренние алерты. У неё нет токенов, и её нельзя изменить.",
  );
  await shot(page, "builtin-integration-ru");

  // An untruncated Snapshot of the group ends the truncation warning, on the page and in the list.
  await advance(60);
  await notify(labId, "g2", { reason: "repeat interval elapsed" });
  await page.goto(`/integrations/${labId}`);
  await expect(page.getByTestId("route-long-interval")).toBeVisible();
  await expect(page.getByTestId("integration-warning")).toHaveCount(1);
  await expect(page.getByText("Alertmanager обрезает снимки", { exact: false })).toHaveCount(0);
  await page.goto("/integrations");
  await expect(
    integrationRow(page, "lab", "Интеграции")
      .getByTestId("integration-warning-mark")
      .locator("summary"),
  ).toHaveText("1 предупреждение");
  expect(csp).toEqual([]);
});

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Message section of the Route editor against `muster dev`: Verification steps 1 to 3 and 9 of its story. The fake
// Alertmanager sends HighLatency (pods a and b in cluster prod) to the Integration "msg-lab"; "Create route" offers the
// language and the three templates, "Alert line" → "Custom" starts from the built-in template and previews the typed
// one against the built-in example and then against the Alert Group, "Root message" → "Custom" → "Telegram" shows the
// built-in body as Telegram HTML, `{{ env "HOME" }}` is marked at line 1, column 4 and refused on save. A stored
// Route's editor previews against its most recent Stored Snapshots by default. A Route whose template keeps failing
// shows the banner and its mark in the Routes list. No Content Security Policy violation.
//
// "Labels in the message" and the preview of the compact layout (the last test): three firing Alerts of one Alert Group
// in the fake Mattermost server; the Route's "Message" lists the chips system, team and namespace; "+ label" offers
// `cluster` and `pod` with example values; the unsaved `cluster` is not in the preview yet, "Save" edits the open Root
// message Quietly to show `cluster=prod-eu`; the preview has the tabs Mattermost, Slack, Telegram and Bridge with the
// length against the limit and the layout JSON; "Reset to default" goes back; an empty list, a duplicate, Russian at
// 360 px, and the Audit log entry.

import { expect, test, type Locator, type Page } from "@playwright/test";

import {
  FMM,
  TEAM_SPEC_PASSWORD,
  adminApi,
  alertGroupOf,
  createRoute,
  createRoutingRoute,
  deleteRoute,
  devClockOffset,
  dropRouting,
  ensureLocalUser,
  expectNoHorizontalScroll,
  fakeIntegration,
  fam,
  notify,
  removeIntegration,
  removeRoute,
  seedRouting,
  shot,
  signIn,
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
  // A rerun on the same database starts from the same state.
  const admin = await adminApi();
  try {
    for (const name of ["msg-one", "msg-pods", "msg-own"]) {
      await removeRoute(admin, name);
    }
    await removeIntegration(admin, "msg-lab");
  } finally {
    await admin.dispose();
  }
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

  // 1. Routes → "Create route" → Paging → Message: the language and "Root message" built in.
  await page
    .getByRole("navigation", { name: "Main" })
    .getByRole("link", { name: "Routes" })
    .click();
  await page.getByRole("link", { name: "Create route" }).click();
  await page.getByRole("button", { name: "Paging" }).click();
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
  await expect(rootEditor).toHaveValue(/^\{\{ with \.AlertGroup\.Summary \}\}/);
  await root.getByRole("tab", { name: "Telegram" }).click();
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
    /^Template error since \d{2}:\d{2}: map has no entry for key "pod"\. Messages use the fallback template\. How to fix$/,
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

interface MattermostPost {
  id: string;
  channel_id: string;
  edit_at: number;
  message: string;
  props: unknown;
}

/** The posts of a channel of the fake Mattermost server whose attachment names the Alert. */
async function postsOf(channel: string, alertname: string): Promise<MattermostPost[]> {
  const res = await fetch(`${FMM}/posts`);
  expect(res.ok).toBe(true);
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the fake's own list of posts
  const posts = (await res.json()) as MattermostPost[];
  return posts.filter(
    (p) => p.channel_id === channel && JSON.stringify(p.props ?? {}).includes(alertname),
  );
}

/** What the fake server notified about a post: an edit notifies nobody. */
async function notificationsOf(postId: string): Promise<number> {
  const res = await fetch(`${FMM}/notifications`);
  expect(res.ok).toBe(true);
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the fake's own list of notifications
  const list = (await res.json()) as { post_id: string }[];
  return list.filter((n) => n.post_id === postId).length;
}

const labelNames = (page: Page) =>
  page.getByTestId("message-label").evaluateAll((els) => els.map((e) => e.textContent?.trim()));

test("chooses the labels in the message, previews the compact layout for every markup and edits the open message", async ({
  page,
  browser,
}) => {
  test.setTimeout(240_000);
  const csp = watchCsp(page);
  const seed = await seedRouting("msglab");
  const channel = "ch-infra-notice";
  const alertname = seed.alertName("OOMKilled");
  try {
    const target = seed.destination("infra-notice");
    const routeId = await createRoutingRoute(seed, "Infra-msglab", {
      entries: [{ destination_id: target.id }],
      deliverToTeams: false,
      groupKey: ["alertname", "system", "team"],
    });
    // Three firing Alerts of one Alert Group, with the labels the Route will offer.
    for (const pod of ["checkout-api-7d9f4", "checkout-api-5b2c1", "checkout-worker-8e6a0"]) {
      await fam("PUT", `/groups/msglab-routing/alerts/${alertname}-${pod}`, {
        labels: {
          alertname,
          category: seed.category,
          team: seed.slugs.pay,
          system: "kubernetes",
          severity: "warning",
          namespace: "checkout",
          cluster: "prod-eu",
          pod,
        },
        annotations: {
          summary: "Container restarted for lack of memory",
          description: "A container went over its memory limit.",
        },
      });
    }
    await notify(seed.integrationId, "msglab-routing", { reason: "first notification" });
    let root: MattermostPost | undefined;
    await expect
      .poll(async () => {
        root = (await postsOf(channel, alertname))[0];
        return root?.id ?? "";
      })
      .not.toBe("");
    const rootId = root?.id ?? "";
    const before = JSON.stringify(root?.props);
    expect(before).toContain("system=kubernetes");
    expect(before).not.toContain("cluster=");
    const editedBefore = root?.edit_at ?? 0;
    const notifiedBefore = await notificationsOf(rootId);

    // 1. Routes → the Route → "Message" → "Labels in the message": the chips and the hint.
    await signInAdmin(page);
    await page.goto(`/routes/${routeId}`);
    const labels = page.getByTestId("message-labels");
    await expect(labels.getByRole("heading", { name: "Labels in the message" })).toBeVisible();
    await expect(page.getByTestId("message-label").first()).toBeVisible();
    expect(await labelNames(page)).toEqual(["system", "team", "namespace"]);
    await expect(labels).toContainText(
      "By default: the Group key without alertname and the severity label, the Team label and namespace. Labels without a value in the Alert Group are not shown.",
    );
    await expect(labels.getByTestId("docs-link")).toHaveAttribute(
      "href",
      "https://muster-io.github.io/muster/latest/configure/messages-and-templates/#labels-in-the-message",
    );
    await expect(labels.getByRole("button", { name: "Reset to default" })).toHaveCount(0);

    // 2. "+ label": the suggestions with an example value; a duplicate is refused; cluster is chosen.
    await labels.getByRole("button", { name: "+ label" }).click();
    await expect(labels.getByText("From recent Alert Groups of the Route")).toBeVisible();
    await expect(
      labels.getByRole("button", { name: "cluster — e.g. prod-eu", exact: true }),
    ).toBeVisible();
    await expect(labels.getByRole("button", { name: /^pod — e\.g\. checkout-/ })).toBeVisible();
    // `namespace` is in the message already, so it is not offered.
    await expect(labels.getByRole("button", { name: /^namespace — e\.g\./ })).toHaveCount(0);
    await labels.getByRole("textbox", { name: "Label name" }).fill("team");
    await expect(labels.getByText("This label is already in the message.")).toBeVisible();
    await expect(labels.getByRole("button", { name: "Add label" })).toBeDisabled();
    await labels.getByRole("textbox", { name: "Label name" }).fill("");
    await labels.getByRole("button", { name: "cluster — e.g. prod-eu", exact: true }).click();
    expect(await labelNames(page)).toEqual(["system", "team", "namespace", "cluster"]);
    await expect(labels.getByRole("button", { name: "Reset to default" })).toBeVisible();

    // The preview renders the labels saved on the Route, and says so while these are not saved.
    const rootTemplate = page.getByTestId("template-root_message");
    await expect(rootTemplate.getByTestId("template-preview-labels-unsaved")).toContainText(
      "The preview shows the labels saved on the Route. Save to see the change.",
    );
    await expect(rootTemplate.getByTestId("layout-mattermost")).not.toContainText("cluster=");

    // 3. "Save": the open Root message is edited Quietly to show cluster=prod-eu.
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page).toHaveURL(/\/routes$/);
    await expect
      .poll(async () => JSON.stringify((await postsOf(channel, alertname))[0]?.props))
      .toContain("cluster=prod-eu");
    const after = (await postsOf(channel, alertname))[0];
    expect(after?.id, "the same post, edited").toBe(rootId);
    expect(after?.edit_at ?? 0).toBeGreaterThan(editedBefore);
    expect(await postsOf(channel, alertname)).toHaveLength(1);
    expect(await notificationsOf(rootId), "an edit notifies nobody").toBe(notifiedBefore);
    console.log("MATTERMOST ROOT AFTER SAVE", JSON.stringify(after?.props));

    // The Audit log names the field.
    await page.goto(
      `/admin/audit-log?action=route.updated&resource_type=route&resource_id=${routeId}`,
    );
    const diff = page.getByTestId("audit-diff").first();
    await expect(diff).toContainText("Labels in the message");
    await expect(diff).toContainText('["system","team","namespace","cluster"]');

    // 4. The editor again: the chips, and the preview in every markup.
    await page.goto(`/routes/${routeId}`);
    await expect(page.getByTestId("message-label").first()).toBeVisible();
    expect(await labelNames(page)).toEqual(["system", "team", "namespace", "cluster"]);
    const preview = rootTemplate.getByTestId("template-preview");
    await expect(rootTemplate.getByRole("tab")).toHaveText([
      "Mattermost",
      "Slack",
      "Telegram",
      "Bridge",
    ]);
    await expect(rootTemplate.getByTestId("template-preview-labels-unsaved")).toHaveCount(0);
    // The sample is the Alert Group itself, which has its Teams; a Stored Snapshot has none to show.
    const groupId = await alertGroupOf(seed.integrationId, alertname);
    await rootTemplate
      .getByTestId("sample-picker")
      .selectOption({ value: `alert_group:${groupId}` });
    const mm = rootTemplate.getByTestId("layout-mattermost");
    await expect(mm.getByTestId("layout-title")).toContainText(
      new RegExp(`^#\\d+ ${alertname}.* — Firing$`),
    );
    for (const field of [
      "Severity",
      "warning",
      "Route",
      "Infra-msglab",
      "Teams",
      seed.slugs.pay,
      "Started",
    ]) {
      await expect(mm.getByTestId("layout-fields")).toContainText(field);
    }
    await expect(mm.getByTestId("layout-labels")).toContainText(
      `system=kubernetes · team=${seed.slugs.pay} · namespace=checkout · cluster=prod-eu`,
    );
    await expect(mm.getByTestId("layout-alerts")).toContainText("pod: checkout-api-");
    await expect(rootTemplate.getByTestId("template-preview-length")).toContainText(
      /of 16383 characters$/,
    );

    await rootTemplate.getByRole("tab", { name: "Slack" }).click();
    const slack = rootTemplate.getByTestId("layout-slack");
    await expect(slack.getByTestId("layout-title")).toContainText("— Firing");
    await expect(slack.getByTestId("layout-labels")).toContainText("cluster=prod-eu");
    await expect(slack.getByTestId("layout-alerts")).toContainText("pod:");
    await expect(rootTemplate.getByTestId("template-preview-length")).toContainText(
      /of 3000 characters$/,
    );

    await rootTemplate.getByRole("tab", { name: "Telegram" }).click();
    const telegram = rootTemplate.getByTestId("layout-telegram");
    await expect(telegram.getByTestId("layout-title")).toContainText("— Firing");
    await expect(telegram.getByTestId("layout-fields")).toContainText("Severity: warning");
    await expect(telegram.getByTestId("layout-labels")).toContainText("cluster=prod-eu");
    await expect(telegram.getByTestId("layout-alerts")).toContainText("pod:");
    await expect(rootTemplate.getByTestId("template-preview-length")).toContainText(
      /of 4096 characters$/,
    );

    await rootTemplate.getByRole("tab", { name: "Bridge" }).click();
    const json = rootTemplate.getByTestId("layout-json");
    await expect(json).toContainText('"key": "firing"');
    await expect(json).toContainText('"key": "cluster"');
    await expect(json).toContainText('"value": "prod-eu"');
    await expect(rootTemplate.getByTestId("layout-body")).not.toBeEmpty();
    await expect(rootTemplate.getByTestId("template-preview-length")).toContainText(
      /characters; the bridge sets its own limit$/,
    );
    await expect(preview).toBeVisible();
    await shot(page, "route-message-labels");

    // 5. "Reset to default" → "Save": the chips are the computed list again, and the message follows.
    await resetLabels(page);
    await expect
      .poll(async () => JSON.stringify((await postsOf(channel, alertname))[0]?.props))
      .not.toContain("cluster=");
    await page.goto(`/routes/${routeId}`);
    await expect(page.getByTestId("message-label").first()).toBeVisible();
    expect(await labelNames(page)).toEqual(["system", "team", "namespace"]);
    await expect(
      page.getByTestId("message-labels").getByRole("button", { name: "Reset to default" }),
    ).toHaveCount(0);

    // 6. Negative: an empty own list says so, saves, and the message shows no key labels; then back to the default.
    const section = page.getByTestId("message-labels");
    for (const name of ["system", "team", "namespace"]) {
      await section.getByRole("button", { name: `Remove ${name} from the message` }).click();
    }
    await expect(section.getByTestId("message-labels-empty")).toHaveText(
      "No labels: the message has no line of key labels.",
    );
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page).toHaveURL(/\/routes$/);
    await expect
      .poll(async () => JSON.stringify((await postsOf(channel, alertname))[0]?.props))
      .not.toContain("system=kubernetes");
    await page.goto(`/routes/${routeId}`);
    await expect(page.getByTestId("message-labels-empty")).toBeVisible();
    await resetLabels(page);
    await expect
      .poll(async () => JSON.stringify((await postsOf(channel, alertname))[0]?.props))
      .toContain("system=kubernetes");

    // 7. Phone width: no sideways scroll, and the layout JSON scrolls inside itself.
    await page.setViewportSize({ width: 360, height: 740 });
    await page.goto(`/routes/${routeId}`);
    await expect(page.getByTestId("message-label").first()).toBeVisible();
    await page.getByTestId("template-root_message").getByRole("tab", { name: "Bridge" }).click();
    await expect(page.getByTestId("layout-json")).toBeVisible();
    await expectNoHorizontalScroll(page);
    await shot(page, "route-message-labels-360");

    // 8. Russian at 360 px.
    const admin = await adminApi();
    try {
      await ensureLocalUser(admin, "msglab-ru", "admin");
    } finally {
      await admin.dispose();
    }
    const context = await browser.newContext({ viewport: { width: 360, height: 800 } });
    try {
      const ru = await context.newPage();
      const ruCsp = watchCsp(ru);
      await signIn(ru, "msglab-ru", TEAM_SPEC_PASSWORD);
      await ru.goto("/profile");
      await ru.getByLabel(/^(Language|Язык)$/).selectOption("ru");
      await expect(ru.getByRole("navigation", { name: "Основная" })).toBeVisible();
      await ru.goto(`/routes/${routeId}`);
      const ruLabels = ru.getByTestId("message-labels");
      await expect(ruLabels.getByRole("heading", { name: "Метки в сообщении" })).toBeVisible();
      await expect(ruLabels).toContainText(
        "По умолчанию: ключ группировки без alertname и метки важности, метку команды и namespace. Метки без значения в группе не показываются.",
      );
      expect(await labelNames(ru)).toEqual(["system", "team", "namespace"]);
      await ruLabels.getByRole("button", { name: "+ метка" }).click();
      await expect(ruLabels.getByText("Из недавних групп маршрута")).toBeVisible();
      await expect(
        ruLabels.getByRole("button", { name: "cluster — напр. prod-eu", exact: true }),
      ).toBeVisible();
      const ruRoot = ru.getByTestId("template-root_message");
      await ruRoot.getByRole("tab", { name: "Telegram" }).click();
      await expect(ruRoot.getByTestId("template-preview-length")).toContainText(
        /из 4096 символов$/,
      );
      await expect(ruRoot.getByTestId("layout-telegram")).toContainText("— Firing");
      await expectNoHorizontalScroll(ru);
      await shot(ru, "route-message-labels-ru-360");
      expect(ruCsp).toEqual([]);
    } finally {
      await context.close();
    }
    expect(csp).toEqual([]);
  } finally {
    await dropRouting("msglab");
  }
});

/** "Reset to default" → "Save" on the Route editor that is open. */
async function resetLabels(page: Page): Promise<void> {
  await page
    .getByTestId("message-labels")
    .getByRole("button", { name: "Reset to default" })
    .click();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page).toHaveURL(/\/routes$/);
}

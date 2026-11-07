// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Route editor and the Group key preview against `muster dev`: Verification steps 2 to 4 of the Routes story. The
// fake Alertmanager sends the group rk1 (alertname Disk, two alerts with cluster=a and two without a cluster) to the
// Integration "pv"; "Create route" with the Paging profile previews the unsaved Route, creates "disk", and its
// editor previews the current and the proposed Group key and puts the server's regular expression error under its
// row. Then the editor of a Route changed elsewhere, and the editor in Russian and 360 px wide.

import { expect, test, type Locator, type Page } from "@playwright/test";

import {
  INGEST_URL,
  adminApi,
  TEAM_SPEC_PASSWORD,
  dropRouting,
  ensureLocalUser,
  expectNoHorizontalScroll,
  fam,
  notify,
  seedRouting,
  setTeamDestinations,
  shot,
  signIn,
  signInAdmin,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

let pvId = "";

/** "pv" without Static labels, its fake receiver, and the group rk1 sent once. */
async function prepare(): Promise<void> {
  const admin = await adminApi();
  const pv = await admin.call<{ id: string }>("POST", "/api/v1/integrations", {
    name: "pv",
    connection_mode: "webhook_only",
    static_labels: {},
    duplicate_window_seconds: 45,
    heartbeat: { enabled: false },
  });
  pvId = pv.id;
  const token = await admin.call<{ value: string }>(
    "POST",
    `/api/v1/integrations/${pv.id}/tokens`,
    {
      name: "fake",
    },
  );
  await admin.dispose();
  await fam("POST", "/receivers", { name: "pv", url: INGEST_URL, token: token.value });
  await fam("PUT", "/groups/rk1", { receiver: "pv", route: "{}", labels: { alertname: "Disk" } });
  for (const node of ["a1", "a2"]) {
    await fam("PUT", `/groups/rk1/alerts/${node}`, { labels: { cluster: "a", node } });
  }
  for (const node of ["m1", "m2"]) {
    await fam("PUT", `/groups/rk1/alerts/${node}`, { labels: { node } });
  }
  await notify(pvId, "rk1", { reason: "first notification" });
}

function routeRow(page: Page, name: string): Locator {
  return page
    .getByTestId("route-row")
    .filter({ has: page.getByRole("link", { name, exact: true }) });
}

async function removeGroupKeyLabel(page: Page, label: string): Promise<void> {
  await page.getByRole("button", { name: `Remove ${label} from the Group key` }).click();
  await expect(page.getByTestId("group-key-label").filter({ hasText: label })).toHaveCount(0);
}

async function addGroupKeyLabel(page: Page, label: string): Promise<void> {
  await page.getByRole("textbox", { name: "Group key" }).fill(label);
  await page.getByRole("button", { name: "Add label" }).click();
  await expect(page.getByTestId("group-key-label").filter({ hasText: label })).toBeVisible();
}

function previewRows(page: Page, side: "current" | "proposed"): Locator {
  return page
    .getByTestId("group-key-preview")
    .getByRole("table", { name: side === "current" ? /^Current:/ : /^Proposed:/ })
    .getByTestId("preview-example");
}

test("creates a Route from a profile and previews its Group key", async ({ page }) => {
  test.setTimeout(180_000);
  const csp = watchCsp(page);
  await prepare();
  await signInAdmin(page);

  // 2. "Create route" → "Paging": the editor has the profile's values; the unsaved Route previews the proposed side.
  await page
    .getByRole("navigation", { name: "Main" })
    .getByRole("link", { name: "Routes" })
    .click();
  await page.getByRole("link", { name: "Create route" }).click();
  await expect(page.getByRole("button", { name: "Paging" })).toHaveAccessibleDescription(
    "For alerts someone must act on: ack timeout and Reminders on.",
  );
  await expect(page.getByRole("button", { name: "Informational" })).toHaveAccessibleDescription(
    "For alerts to read later: no ack timeout, no Reminders, long Snooze durations.",
  );
  await shot(page, "routes-profile-choice");
  await page.getByRole("button", { name: "Paging" }).click();
  await expect(page.getByTestId("route-profile")).toContainText("Profile: Paging");
  await expect(page.getByRole("textbox", { name: "Name" })).toBeFocused();
  await expect(page.getByTestId("group-key-label")).toHaveText([
    "alertname",
    "severity",
    "cluster",
  ]);
  await expect(page.getByTestId("route-minimum-severity")).toHaveValue("none");
  await page.getByRole("textbox", { name: "Name" }).fill("disk");
  await page.getByRole("button", { name: "Add matcher" }).click();
  await expect(page.getByRole("textbox", { name: "Label of matcher 1" })).toBeFocused();
  await page.getByRole("textbox", { name: "Label of matcher 1" }).fill("alertname");
  await expect(page.getByRole("combobox", { name: "Operator of matcher 1" })).toHaveValue("=");
  await expect(
    page.getByRole("combobox", { name: "Operator of matcher 1" }).locator("option"),
  ).toHaveText(["=", "!=", "=~", "!~"]);
  await page.getByRole("textbox", { name: "Value of matcher 1" }).fill("Disk");
  await removeGroupKeyLabel(page, "severity");
  await removeGroupKeyLabel(page, "cluster");
  await expect(page.getByRole("combobox", { name: "Period" })).toHaveValue("86400");
  await page.getByRole("button", { name: "Preview" }).click();
  await expect(page.getByTestId("preview-proposed")).toHaveText("Proposed: 1 Alert Group");
  await expect(page.getByTestId("preview-current")).toHaveCount(0);
  await expect(previewRows(page, "proposed")).toHaveText(["Disk4"]);
  await shot(page, "routes-create-preview");
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page).toHaveURL(/\/routes$/);
  const names = page.getByTestId("route-row").getByRole("link");
  await expect(names.last()).toHaveText("Default");
  await expect(names.nth(-2)).toHaveText("disk");
  await expect(routeRow(page, "disk").getByTestId("route-matchers")).toHaveText('alertname="Disk"');
  await expect(routeRow(page, "disk").getByTestId("route-group-key")).toHaveText(
    "Group key: alertname",
  );

  // 3. "disk" with cluster added to the Group key: the current key makes 1 Alert Group, the proposed one 2.
  await routeRow(page, "disk").getByRole("link", { name: "disk" }).click();
  await expect(page.getByRole("heading", { level: 1, name: "disk" })).toBeVisible();
  await addGroupKeyLabel(page, "cluster");
  await page.getByRole("button", { name: "Preview" }).click();
  await expect(page.getByTestId("preview-current")).toHaveText("Current: 1 Alert Group");
  await expect(page.getByTestId("preview-proposed")).toHaveText("Proposed: 2 Alert Groups");
  const proposed = previewRows(page, "proposed");
  await expect(proposed).toHaveCount(2);
  for (const cluster of ["a", ""]) {
    const cells = proposed.filter({
      has: page.getByRole("cell", { name: cluster === "" ? "empty" : cluster, exact: true }),
    });
    await expect(cells).toHaveCount(1);
    await expect(cells.getByRole("cell").last()).toHaveText("2");
  }
  await expect(
    page.getByTestId("group-key-preview").getByRole("columnheader", { name: "cluster" }),
  ).toBeVisible();
  await shot(page, "routes-editor-preview");

  // The period: 14 days previews; the result says when inputs changed since.
  await page.getByRole("combobox", { name: "Period" }).selectOption("1209600");
  await expect(
    page.getByText("The Matchers, the Group key or the period changed since this preview."),
  ).toBeVisible();
  await page.getByRole("button", { name: "Preview" }).click();
  await expect(page.getByTestId("preview-proposed")).toHaveText("Proposed: 2 Alert Groups");

  // 4. A Matcher whose regular expression does not compile: the server's error under that row's value.
  await page.getByRole("button", { name: "Add matcher" }).click();
  await page.getByRole("textbox", { name: "Label of matcher 2" }).fill("pod");
  await page.getByRole("combobox", { name: "Operator of matcher 2" }).selectOption("=~");
  await page.getByRole("textbox", { name: "Value of matcher 2" }).fill("api-(");
  await page.getByRole("button", { name: "Save" }).click();
  const second = page.getByTestId("matcher-row").nth(1);
  await expect(second.getByTestId("matcher-value-error")).toHaveText(
    "The regular expression is not valid: error parsing regexp: missing closing ): `api-(`",
  );
  await expect(page.getByRole("textbox", { name: "Value of matcher 2" })).toBeFocused();
  await expect(page.getByRole("textbox", { name: "Value of matcher 2" })).toHaveAttribute(
    "aria-invalid",
    "true",
  );
  await expect(
    page.getByTestId("matcher-row").nth(0).getByTestId("matcher-value-error"),
  ).toHaveCount(0);
  await shot(page, "routes-matcher-error");
  // Editing the row clears its error; removing the row lets the Route save.
  await page.getByRole("textbox", { name: "Value of matcher 2" }).fill("api-.*");
  await expect(second.getByTestId("matcher-value-error")).toHaveCount(0);
  await page.getByRole("button", { name: "Remove matcher pod" }).click();
  await expect(page.getByTestId("matcher-row")).toHaveCount(1);
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page).toHaveURL(/\/routes$/);
  await expect(routeRow(page, "disk").getByTestId("route-group-key")).toHaveText(
    "Group key: alertname, cluster",
  );
  expect(csp).toEqual([]);
});

test("an open editor says when its Route changed elsewhere, and a save over it is refused", async ({
  page,
}) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  const admin = await adminApi();
  const list = await admin.call<{ items: { id: string; name: string }[] }>("GET", "/api/v1/routes");
  const disk = list.items.find((r) => r.name === "disk");
  expect(disk).toBeDefined();
  const id = disk?.id ?? "";
  const update = async (description: string) => {
    const route = await admin.call<Record<string, unknown> & { etag: string }>(
      "GET",
      `/api/v1/routes/${id}`,
    );
    await admin.call(
      "PUT",
      `/api/v1/routes/${id}`,
      {
        name: route.name,
        description,
        matchers: route.matchers,
        minimum_severity: route.minimum_severity,
        group_key: route.group_key,
        destination_ids: route.destination_ids,
        policy: route.policy,
      },
      { "If-Match": route.etag },
    );
  };

  await page.goto(`/routes/${id}`);
  await expect(page.getByRole("textbox", { name: "Description" })).toHaveValue("");
  // Untouched: the editor takes the new version and says so.
  await update("changed by the API");
  await expect(page.getByTestId("route-replaced")).toHaveText("This route was changed elsewhere.");
  await expect(page.getByRole("textbox", { name: "Description" })).toHaveValue(
    "changed by the API",
  );
  // With changes: the editor keeps them, says so, and the save is refused.
  await page.getByTestId("route-minimum-severity").selectOption("critical");
  await update("changed again");
  await expect(
    page
      .getByRole("alert")
      .filter({ hasText: "This route was changed elsewhere." })
      .getByRole("button", { name: "Reload" }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Save" }).click();
  await expect(
    page.getByText("Someone else changed this route. Reload to see the changes."),
  ).toBeVisible();
  await shot(page, "routes-editor-conflict");
  await page.getByRole("button", { name: "Reload" }).click();
  await expect(page.getByRole("textbox", { name: "Description" })).toHaveValue("changed again");
  await expect(page.getByTestId("route-minimum-severity")).toHaveValue("none");
  await admin.dispose();
  expect(csp).toEqual([]);
});

test("the editor and the preview in Russian, 360 px wide", async ({ browser }) => {
  const admin = await adminApi();
  const { token } = await admin.createUser("routes-ru", "admin");
  await admin.call("POST", "/api/v1/password-setups", { token, password: "routes-ru-password-1" });
  const list = await admin.call<{ items: { id: string; name: string }[] }>("GET", "/api/v1/routes");
  const id = list.items.find((r) => r.name === "disk")?.id ?? "";
  await admin.dispose();
  const context = await browser.newContext({ viewport: { width: 360, height: 800 } });
  const page = await context.newPage();
  const csp = watchCsp(page);
  await signIn(page, "routes-ru", "routes-ru-password-1");
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();

  await page.goto(`/routes/${id}`);
  await expect(page.getByRole("heading", { level: 1, name: "disk" })).toBeVisible();
  await expect(page.getByText("Ключ группировки", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Показать" }).click();
  await expect(page.getByTestId("preview-current")).toHaveText("Сейчас: 2 группы алертов");
  await expect(page.getByTestId("preview-proposed")).toHaveText("С новым ключом: 2 группы алертов");
  await page.getByRole("button", { name: "Убрать cluster из ключа группировки" }).click();
  await page.getByRole("button", { name: "Показать" }).click();
  await expect(page.getByTestId("preview-proposed")).toHaveText("С новым ключом: 1 группа алертов");
  await expectNoHorizontalScroll(page);
  await shot(page, "routes-editor-ru-360");

  await page.goto("/routes/new");
  await expect(page.getByRole("button", { name: "С призывом" })).toHaveAccessibleDescription(
    "Для алертов, на которые кто-то должен отреагировать: тайм-аут подтверждения и напоминания включены.",
  );
  await page.getByRole("button", { name: "Информационный" }).click();
  await page.getByRole("button", { name: "Добавить матчер" }).click();
  await expect(page.getByRole("textbox", { name: "Метка матчера 1" })).toBeVisible();
  await expectNoHorizontalScroll(page);
  await shot(page, "routes-create-ru-360");
  expect(csp).toEqual([]);
  await context.close();
});

const PREFIX = "rpv";

/** The text of the Destinations a preview group lists, sorted. */
async function reached(group: Locator): Promise<string[]> {
  const texts = await group.getByTestId("delivery-preview-destination").allInnerTexts();
  return texts.map((t) => t.replace(/\s+/g, " ").trim()).toSorted();
}

test("the preview Where alerts go lists the Alert Groups with their Teams and Destinations, and follows the unsaved settings", async ({
  page,
  browser,
}) => {
  const csp = watchCsp(page);
  const seed = await seedRouting(PREFIX);
  const critical = seed.destination("infra-critical");
  const notice = seed.destination("infra-notice");
  const paymentsNotice = seed.destination("payments-notice");
  const searchNotice = seed.destination("search-notice");
  const paying = seed.alertName("Pay");
  const ops = seed.alertName("Ops");
  const rise = seed.alertName("Rise");
  const shared = seed.alertName("Shared");
  try {
    const admin = await adminApi();
    try {
      await ensureLocalUser(admin, "rpv-ru", "admin");
      await setTeamDestinations(admin, seed.slugs.pay, [
        { destination_id: paymentsNotice.id, severity_condition: "below_critical" },
      ]);
      await setTeamDestinations(admin, seed.slugs.sea, [
        { destination_id: searchNotice.id, severity_condition: "any" },
      ]);
    } finally {
      await admin.dispose();
    }
    // payments warns; devops (a value of the Team label that names no Team) is critical; one payments Alert Group rises
    // from warning to critical; one Alert Group has the Alerts of two Teams.
    await seed.fire([
      { alertname: paying, team: seed.slugs.pay, severity: "warning" },
      { alertname: ops, team: seed.slugs.ops, severity: "critical" },
      {
        alertname: rise,
        id: "rise-w",
        team: seed.slugs.pay,
        severity: "warning",
        startedMinutesAgo: 30,
      },
      {
        alertname: rise,
        id: "rise-c",
        team: seed.slugs.pay,
        severity: "critical",
        startedMinutesAgo: 10,
      },
      { alertname: shared, id: "shared-p", team: seed.slugs.pay, severity: "warning" },
      { alertname: shared, id: "shared-s", team: seed.slugs.sea, severity: "warning" },
    ]);

    await signInAdmin(page);
    await page.goto("/routes/new");
    await page.getByTestId("route-profile-paging").click();
    const preview = page.getByTestId("route-delivery-preview");
    await expect(preview.getByRole("heading", { name: "Where alerts go" })).toBeVisible();
    // A new Route without a Matcher asks for one and asks the API nothing.
    await expect(preview.getByTestId("delivery-preview-needs-matchers")).toHaveText(
      "Add a Matcher to see where alerts go.",
    );
    await page.getByRole("textbox", { name: "Name" }).fill(`Preview-${PREFIX}`);
    await page.getByRole("button", { name: "Add matcher" }).click();
    await page.getByRole("textbox", { name: "Label of matcher 1" }).fill("category");
    await page.getByRole("textbox", { name: "Value of matcher 1" }).fill(seed.category);
    // The key is alertname and system, as the Verification has it; the Destinations are not chosen yet.
    while ((await page.getByTestId("group-key-label").count()) > 0) {
      await page.getByTestId("group-key-label").first().getByRole("button").click();
    }
    await addGroupKeyLabel(page, "alertname");
    await addGroupKeyLabel(page, "system");
    // Without a Destination and without the option every Alert Group reaches nobody, which the preview says.
    await expect(
      preview
        .getByTestId("delivery-group")
        .filter({ hasText: paying })
        .getByText("No Destinations: no messages for this Alert Group."),
    ).toBeVisible();

    await page.getByLabel("Add a Destination").selectOption(critical.id);
    await page.getByLabel("Add a Destination").selectOption(notice.id);
    await page.getByTestId(`route-destination-condition-${critical.id}`).selectOption("critical");
    await page
      .getByTestId(`route-destination-condition-${notice.id}`)
      .selectOption("below_critical");
    await page.getByTestId("route-deliver-to-teams").check();

    const group = (name: string) => preview.getByTestId("delivery-group").filter({ hasText: name });
    // Alert Groups: payments warns, devops is critical.
    await expect(group(paying)).toHaveCount(1);
    await expect(group(paying).getByTestId("delivery-group-teams")).toHaveText(
      `Teams: ${seed.slugs.pay}`,
    );
    await expect
      .poll(() => reached(group(paying)))
      .toEqual([notice.name, `${paymentsNotice.name} via ${seed.slugs.pay}`]);
    await expect(group(ops).getByTestId("delivery-group-teams")).toHaveText("Teams: no Team");
    await expect.poll(() => reached(group(ops))).toEqual([critical.name]);
    // The rise: warning, then critical, reaches all three.
    await expect
      .poll(() => reached(group(rise)))
      .toEqual(
        [critical.name, notice.name, `${paymentsNotice.name} via ${seed.slugs.pay}`].toSorted(),
      );
    await expect(group(rise)).toContainText("2 alerts · critical");
    // The Alerts of two Teams share an Alert Group while the key lacks the label: both Teams' Destinations.
    await expect(group(shared).getByTestId("delivery-group-teams")).toContainText(seed.slugs.pay);
    await expect(group(shared).getByTestId("delivery-group-teams")).toContainText(seed.slugs.sea);
    await expect
      .poll(() => reached(group(shared)))
      .toEqual(
        [
          notice.name,
          `${paymentsNotice.name} via ${seed.slugs.pay}`,
          `${searchNotice.name} via ${seed.slugs.sea}`,
        ].toSorted(),
      );
    // How many Alert Groups reach each Destination.
    const count = (name: string) => preview.getByTestId("delivery-count").filter({ hasText: name });
    await expect(count(critical.name)).toHaveText(/reached by 2 of 4$/);
    await expect(count(notice.name)).toHaveText(/reached by 3 of 4$/);
    await expect(count(paymentsNotice.name)).toHaveText(/reached by 3 of 4$/);
    await expect(count(searchNotice.name)).toHaveText(/reached by 1 of 4$/);
    // The hint of the API that Teams share an Alert Group, and the one under the key.
    await expect(preview.getByTestId("preview-shared-key-warning")).toContainText(
      "Alerts of different Teams with the same key share one Alert Group",
    );
    await expect(page.getByTestId("shared-key-hint")).toBeVisible();
    await shot(page, "route-delivery-preview");

    // "Add team to the key" splits the shared group by Team and ends both hints.
    await page.getByRole("button", { name: "Add team to the key" }).click();
    await expect(preview.getByTestId("preview-shared-key-warning")).toHaveCount(0);
    await expect(page.getByTestId("shared-key-hint")).toHaveCount(0);
    await expect(group(shared)).toHaveCount(2);
    await expect(
      group(shared).filter({ hasText: seed.slugs.sea }).getByTestId("delivery-group-teams"),
    ).toHaveText(`Teams: ${seed.slugs.sea}`);
    await expect
      .poll(() => reached(group(shared).filter({ hasText: seed.slugs.sea })))
      .toEqual([notice.name, `${searchNotice.name} via ${seed.slugs.sea}`]);

    // The unsaved option turned off: no Team Destination is listed or counted.
    await page.getByTestId("route-deliver-to-teams").uncheck();
    await expect(
      preview.getByTestId("delivery-count").filter({ hasText: paymentsNotice.name }),
    ).toHaveCount(0);
    await expect.poll(() => reached(group(paying))).toEqual([notice.name]);
    await expect(group(paying).getByTestId("delivery-group-teams")).toHaveText(
      `Teams: ${seed.slugs.pay}`,
    );
    // A condition changed in the form changes the list: infra-notice for any severity reaches the critical groups too.
    await page.getByTestId(`route-destination-condition-${notice.id}`).selectOption("any");
    await expect.poll(() => reached(group(ops))).toEqual([critical.name, notice.name].toSorted());

    // A Matcher no Alert meets: no Alert Group, said in words.
    await page.getByRole("textbox", { name: "Value of matcher 1" }).fill(`${seed.category}-none`);
    await expect(preview.getByTestId("delivery-preview-empty")).toHaveText(
      "No recent Alerts would reach this Route.",
    );
    await page.getByRole("textbox", { name: "Value of matcher 1" }).fill(seed.category);
    await expect(group(paying)).toHaveCount(1);

    // Russian at 360 px.
    const context = await browser.newContext({ viewport: { width: 360, height: 800 } });
    const ru = await context.newPage();
    const ruCsp = watchCsp(ru);
    await signIn(ru, "rpv-ru", TEAM_SPEC_PASSWORD);
    await ru.goto("/profile");
    await ru.getByLabel(/^(Language|Язык)$/).selectOption("ru");
    await expect(ru.getByRole("navigation", { name: "Основная" })).toBeVisible();
    await ru.goto("/routes/new");
    await ru.getByRole("button", { name: "С призывом" }).click();
    await ru.getByRole("button", { name: "Добавить матчер" }).click();
    await ru.getByRole("textbox", { name: "Метка матчера 1" }).fill("category");
    await ru.getByRole("textbox", { name: "Значение матчера 1" }).fill(seed.category);
    await ru.getByLabel("Добавить место доставки").selectOption(notice.id);
    await ru.getByLabel("Добавить место доставки").selectOption(critical.id);
    await ru.getByTestId("route-deliver-to-teams").check();
    const ruPreview = ru.getByTestId("route-delivery-preview");
    await expect(ruPreview.getByRole("heading", { name: "Куда придут алерты" })).toBeVisible();
    const ruGroup = ruPreview.getByTestId("delivery-group").filter({ hasText: paying });
    await expect(ruGroup.getByTestId("delivery-group-teams")).toHaveText(
      `Команды: ${seed.slugs.pay}`,
    );
    await expect(ruGroup.getByTestId("delivery-preview-via")).toHaveText(`через ${seed.slugs.pay}`);
    await expect(ruPreview.getByTestId("delivery-count").first()).toContainText("доходит");
    await expect(ru.getByTestId("preview-shared-key-warning").first()).toContainText(
      "Алерты разных команд",
    );
    await expectNoHorizontalScroll(ru);
    await shot(ru, "route-delivery-preview-ru-360");
    expect(ruCsp).toEqual([]);
    await context.close();
  } finally {
    await dropRouting(PREFIX);
  }
  expect(csp).toEqual([]);
});

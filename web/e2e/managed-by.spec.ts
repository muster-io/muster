// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The "Managed by" mark on the configuration pages against `muster dev` (C-33.FR-12, C-33.AC-4): Verification steps 1 to
// 6 of its story, in English and in Russian. Objects are marked `terraform` through the API: a Route "mb-route" is
// renamed and saved with no extra step and still reads `managed_by` `terraform`, its delete dialog adds the line of
// C-33.FR-12 and a Route without a mark shows nothing of it; an Integration, a Team with its Schedule and Escalation
// chain, a Connection with a Destination, a Lookup table and a Link rule show the mark next to the title and the
// warning; the Default route and the Organization are marked for the length of the spec and cleared in the end; the
// Outbound address policy and the OIDC settings show no mark until they are marked, and are cleared in the end too. At
// 360 × 740 the marked Route page does not scroll sideways.

import { expect, test, type Page } from "@playwright/test";

import {
  type Api,
  adminApi,
  expectNoHorizontalScroll,
  removeNamed,
  removeTeamsBySlug,
  signInAdmin,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

const TOOL = "terraform";
const NAMES = {
  route: "mb-route",
  plain: "mb-plain",
  team: "mb-team",
  schedule: "mb-sched",
  chain: "mb-chain",
  integration: "mb-int",
  connection: "mb-conn",
  destination: "mb-dest",
  table: "mb-table",
  rule: "mb-rule",
};

const EN = {
  save: "Save",
  cancel: "Cancel",
  remove: "Delete",
  nameField: "Name",
  loading: "Loading…",
  badge: `Managed by ${TOOL}`,
  warning: (object: string) =>
    `This ${object} is managed by ${TOOL}. Changes you save here are undone by the next run of ${TOOL}: change its code instead.`,
  deleteLine: `${TOOL} creates it again on its next run.`,
};
const RU = {
  save: "Сохранить",
  cancel: "Отмена",
  remove: "Удалить",
  nameField: "Название",
  loading: "Загрузка…",
  badge: `Управляется ${TOOL}`,
  warning: (object: string) =>
    `Этим объектом (${object}) управляет ${TOOL}. Изменения, сохранённые здесь, отменит следующий запуск ${TOOL}: меняйте его код.`,
  deleteLine: `${TOOL} создаст его снова при следующем запуске.`,
};

interface Ids {
  route: string;
  plain: string;
  defaultRoute: string;
  team: string;
  schedule: string;
  chain: string;
  integration: string;
  connection: string;
  destination: string;
  table: string;
  rule: string;
}
const ids: Ids = {
  route: "",
  plain: "",
  defaultRoute: "",
  team: "",
  schedule: "",
  chain: "",
  integration: "",
  connection: "",
  destination: "",
  table: "",
  rule: "",
};

type Body = Record<string, unknown>;

const NOBODY = { everyone: "none", user_ids: [], groups: [] };
const MENTIONS = Object.fromEntries(
  ["new_alert_group", "new_alerts", "reopen", "ack_timeout", "snooze_ended", "rise_to_urgent"].map(
    (k) => [k, NOBODY],
  ),
);

/** Reads an object with its version, which the next write sends as If-Match. */
async function read(admin: Api, path: string): Promise<{ body: Body; etag: string }> {
  const res = await admin.context.get(path);
  expect(res.ok(), `GET ${path}: ${res.status()}`).toBe(true);
  const body: Body = await res.json();
  return { body, etag: res.headers().etag ?? "" };
}

/** Writes an object back as an update at the version read, with the mark `managed_by`. */
async function mark(
  admin: Api,
  path: string,
  toInput: (read: Body) => Body,
  managedBy: string | null,
): Promise<void> {
  const { body, etag } = await read(admin, path);
  await admin.call("PUT", path, { ...toInput(body), managed_by: managedBy }, { "If-Match": etag });
}

function without(body: Body, keys: string[]): Body {
  return Object.fromEntries(Object.entries(body).filter(([k]) => !keys.includes(k)));
}

/** The id of the Team a read Route names as its owner, null for none. */
function ownerId(owner: unknown): string | null {
  if (
    typeof owner === "object" &&
    owner !== null &&
    "id" in owner &&
    typeof owner.id === "string"
  ) {
    return owner.id;
  }
  return null;
}

const routeInput = (r: Body): Body => ({
  ...without(r, [
    "id",
    "owning_team",
    "position",
    "is_default",
    "destinations",
    "chains",
    "template_error",
    "storm_active",
    "storm",
    "open_alert_group_count",
    "created_at",
    "etag",
    "managed_by",
  ]),
  owning_team_id: ownerId(r.owning_team),
});
/** The proxy of a read, without the status of its stored password, as the app's update sends it. */
function proxyOf(owner: unknown): Body {
  if (typeof owner === "object" && owner !== null && "proxy" in owner) {
    const proxy: Body = { ...Object(owner.proxy) };
    delete proxy.password_status;
    return proxy;
  }
  return { enabled: false };
}

const organizationInput = (o: Body): Body => ({
  ...without(o, ["id", "etag", "managed_by", "critical_is_urgent", "outgoing_heartbeat"]),
  outgoing_heartbeat: { proxy: proxyOf(o.outgoing_heartbeat) },
});
const policyInput = (p: Body): Body => ({
  policy: p.policy,
  allowed: p.allowed,
  denied: p.denied,
});
/** The OIDC settings as an update: the read without its derived fields; the write-only secret is left out and kept. */
const oidcInput = (s: Body): Body => ({
  ...without(s, [
    "etag",
    "client_secret_status",
    "warnings",
    "updated_at",
    "claims_supported",
    "managed_by",
  ]),
  proxy: proxyOf(s),
});

async function removeAll(admin: Api): Promise<void> {
  const rules = await admin.call<{ items: { id: string; name: string }[] }>(
    "GET",
    "/api/v1/link-rules?limit=500",
  );
  for (const r of rules.items.filter((x) => x.name === NAMES.rule)) {
    await admin.call("DELETE", `/api/v1/link-rules/${r.id}`);
  }
  await removeNamed(admin, "/api/v1/lookup-tables", NAMES.table, "?limit=500");
  await removeNamed(admin, "/api/v1/destinations", NAMES.destination, "?limit=500");
  await removeNamed(admin, "/api/v1/connections", NAMES.connection, "?limit=500");
  await removeNamed(admin, "/api/v1/integrations", NAMES.integration, "?limit=500");
  await removeNamed(admin, "/api/v1/escalation-chains", NAMES.chain, "?limit=500");
  const schedules = await admin.call<{ items: { id: string; name: string }[] }>(
    "GET",
    "/api/v1/schedules?limit=500",
  );
  for (const s of schedules.items.filter((x) => x.name === NAMES.schedule)) {
    await admin.call("DELETE", `/api/v1/schedules/${s.id}`);
  }
  await removeTeamsBySlug(admin, [NAMES.team]);
  const routes = await admin.call<{ items: { id: string; name: string }[] }>(
    "GET",
    "/api/v1/routes",
  );
  for (const r of routes.items.filter(
    (x) => x.name.startsWith(NAMES.route) || x.name === NAMES.plain,
  )) {
    await admin.call("POST", `/api/v1/routes/${r.id}/move-open-alert-groups`);
    await admin.call("DELETE", `/api/v1/routes/${r.id}`);
  }
}

/** Clears the marks of the objects that exist once, and of the Default route; one failing does not stop the rest. */
async function unmarkSingletons(admin: Api): Promise<void> {
  const clears: (() => Promise<void>)[] = [
    () => mark(admin, "/api/v1/organization", organizationInput, null),
    () => mark(admin, "/api/v1/organization/outbound-policy", policyInput, null),
    () => mark(admin, "/api/v1/oidc-settings", oidcInput, null),
  ];
  if (ids.defaultRoute !== "") {
    clears.push(() => mark(admin, `/api/v1/routes/${ids.defaultRoute}`, routeInput, null));
  }
  const errors: unknown[] = [];
  for (const clear of clears) {
    try {
      await clear();
    } catch (err) {
      errors.push(err);
    }
  }
  if (errors.length > 0) {
    throw errors[0];
  }
}

async function setLanguage(admin: Api, language: "en" | "ru" | null): Promise<void> {
  const me = await admin.call<{ user: { name: string } }>("GET", "/api/v1/me");
  await admin.call("PUT", "/api/v1/me", { name: me.user.name, language });
}

test.beforeAll(async () => {
  const admin = await adminApi();
  try {
    await removeAll(admin);
    const profiles = await admin.call<{ items: { id: string; policy: unknown }[] }>(
      "GET",
      "/api/v1/route-profiles",
    );
    const policy = profiles.items.find((p) => p.id === "paging")?.policy;
    const route = (name: string, extra: Body) =>
      admin.call<{ id: string }>("POST", "/api/v1/routes", {
        name,
        matchers: [{ label: "team", op: "=", value: name }],
        group_key: ["alertname"],
        destination_ids: [],
        policy,
        ...extra,
      });
    ids.route = (await route(NAMES.route, { managed_by: TOOL })).id;
    ids.plain = (await route(NAMES.plain, {})).id;

    const routes = await admin.call<{ items: { id: string; is_default: boolean }[] }>(
      "GET",
      "/api/v1/routes",
    );
    ids.defaultRoute = routes.items.find((r) => r.is_default)?.id ?? "";

    const me = await admin.call<{ user: { id: string } }>("GET", "/api/v1/sessions/current");
    ids.team = (
      await admin.call<{ id: string }>("POST", "/api/v1/teams", {
        name: NAMES.team,
        slug: NAMES.team,
        own_working_hours: false,
        matchers: [],
        managed_by: TOOL,
      })
    ).id;
    await admin.call("POST", `/api/v1/teams/${ids.team}/users`, { user_id: me.user.id });
    const start = new Date(Date.now() - 14 * 86_400_000).toISOString().slice(0, 10);
    ids.schedule = (
      await admin.call<{ id: string }>("POST", "/api/v1/schedules", {
        team_id: ids.team,
        name: NAMES.schedule,
        time_zone: "UTC",
        rotation_layers: [
          {
            name: "Week",
            user_ids: [me.user.id],
            shift_length: "week",
            handover_time: "10:00",
            handover_day: "monday",
            start_date: start,
            limits: null,
          },
        ],
        managed_by: TOOL,
      })
    ).id;
    ids.chain = (
      await admin.call<{ id: string }>("POST", "/api/v1/escalation-chains", {
        name: NAMES.chain,
        owning_team_id: ids.team,
        steps: [{ kind: "page_team" }],
        managed_by: TOOL,
      })
    ).id;
    ids.integration = (
      await admin.call<{ id: string }>("POST", "/api/v1/integrations", {
        name: NAMES.integration,
        connection_mode: "webhook_only",
        static_labels: {},
        duplicate_window_seconds: 45,
        heartbeat: { enabled: false },
        managed_by: TOOL,
      })
    ).id;
    ids.connection = (
      await admin.call<{ id: string }>("POST", "/api/v1/connections", {
        type: "mattermost",
        name: NAMES.connection,
        server_url: "http://127.0.0.1:18065",
        bot_token: "mm-dev-token",
        proxy: { enabled: false },
        limiter: { limit: 50, per_seconds: 1 },
        managed_by: TOOL,
      })
    ).id;
    ids.destination = (
      await admin.call<{ destination: { id: string } }>("POST", "/api/v1/destinations", {
        type: "webhook",
        name: NAMES.destination,
        mode: "events",
        events: { url: "http://127.0.0.1:18093/hook/mb", headers: [] },
        proxy: { enabled: false },
        mentions: MENTIONS,
        limiter: { limit: 5, per_seconds: 1 },
        managed_by: TOOL,
      })
    ).destination.id;
    ids.table = (
      await admin.call<{ id: string }>("POST", "/api/v1/lookup-tables", {
        name: NAMES.table,
        description: "",
        columns: ["address"],
        entries: [{ key: "prod", values: { address: "https://example.org" } }],
        managed_by: TOOL,
      })
    ).id;
    ids.rule = (
      await admin.call<{ id: string }>("POST", "/api/v1/link-rules", {
        name: NAMES.rule,
        matchers: [{ label: "cluster", op: "=~", value: ".+" }],
        scope: { type: "alert_group" },
        url_template: "https://example.org/d",
        managed_by: TOOL,
      })
    ).id;
  } finally {
    await admin.dispose();
  }
});

test.afterAll(async () => {
  const admin = await adminApi();
  try {
    try {
      await setLanguage(admin, null);
    } finally {
      try {
        await unmarkSingletons(admin);
      } finally {
        await removeAll(admin);
      }
    }
  } finally {
    await admin.dispose();
  }
});

/** The mark next to the title and the warning of the page, with the texts of a language. */
async function expectMarked(page: Page, texts: typeof EN, object: string): Promise<void> {
  await expect(page.getByTestId("managed-by-badge")).toHaveCount(1);
  await expect(page.getByTestId("managed-by-badge")).toHaveText(texts.badge);
  await expect(page.getByTestId("managed-by-warning")).toHaveCount(1);
  await expect(page.getByTestId("managed-by-warning")).toHaveText(texts.warning(object));
}

async function expectUnmarked(page: Page): Promise<void> {
  await expect(page.getByTestId("managed-by-badge")).toHaveCount(0);
  await expect(page.getByTestId("managed-by-warning")).toHaveCount(0);
}

/** Waits until the page has read its data, so that an absence asserted next is not asserted before the read returns. */
async function dataLoaded(page: Page, texts: typeof EN): Promise<void> {
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  await expect(page.getByText(texts.loading)).toHaveCount(0);
}

let routeName = NAMES.route;

for (const language of ["en", "ru"] as const) {
  test(`a marked Route shows the mark and the warning, saves with no extra step and keeps the mark (${language})`, async ({
    page,
  }) => {
    const texts = language === "en" ? EN : RU;
    const object = language === "en" ? "route" : "маршрут";
    const csp = watchCsp(page);
    const admin = await adminApi();
    try {
      await setLanguage(admin, language);
      await signInAdmin(page);

      // 1. The mark next to the title and the warning above the form.
      await page.goto(`/routes/${ids.route}`);
      await expect(page.getByRole("heading", { name: routeName, level: 1 })).toBeVisible();
      await expectMarked(page, texts, object);

      // 2. Rename and Save: nothing is asked, and the Route still reads `managed_by` `terraform`.
      const renamed = `${NAMES.route}-${language}`;
      await page.getByRole("textbox", { name: texts.nameField }).fill(renamed);
      await page.getByRole("button", { name: texts.save, exact: true }).click();
      await expect(page).toHaveURL(/\/routes$/);
      await expect
        .poll(async () => (await read(admin, `/api/v1/routes/${ids.route}`)).body)
        .toMatchObject({ name: renamed, managed_by: TOOL });
      routeName = renamed;
      await page.goto(`/routes/${ids.route}`);
      await expect(page.getByRole("heading", { name: renamed, level: 1 })).toBeVisible();
      await expectMarked(page, texts, object);

      // 3. The delete dialog adds the line; Cancel leaves the Route.
      await page.getByRole("button", { name: texts.remove, exact: true }).click();
      const dialog = page.getByRole("dialog");
      await expect(dialog.getByTestId("managed-by-delete-line")).toHaveText(texts.deleteLine);
      await dialog.getByRole("button", { name: texts.cancel }).click();
      await expect(dialog).toHaveCount(0);

      // 4. A Route without a mark shows none of the three.
      await page.goto(`/routes/${ids.plain}`);
      await expect(page.getByRole("heading", { name: NAMES.plain, level: 1 })).toBeVisible();
      await dataLoaded(page, texts);
      await expect(page.getByRole("button", { name: texts.remove, exact: true })).toBeVisible();
      await expectUnmarked(page);
      await page.getByRole("button", { name: texts.remove, exact: true }).click();
      await expect(page.getByRole("dialog")).toBeVisible();
      await expect(page.getByRole("dialog").getByTestId("managed-by-delete-line")).toHaveCount(0);
      await page.getByRole("dialog").getByRole("button", { name: texts.cancel }).click();
      expect(csp).toEqual([]);
    } finally {
      await setLanguage(admin, null);
      await admin.dispose();
    }
  });
}

test("the pages of the other kinds show the mark and the warning, in English and in Russian", async ({
  page,
}) => {
  const admin = await adminApi();
  try {
    await mark(admin, "/api/v1/organization", organizationInput, TOOL);
    await mark(admin, `/api/v1/routes/${ids.defaultRoute}`, routeInput, TOOL);
    await signInAdmin(page);
    const kinds: { path: () => string; en: string; ru: string }[] = [
      { path: () => `/integrations/${ids.integration}`, en: "integration", ru: "интеграция" },
      { path: () => `/integrations/${ids.integration}/edit`, en: "integration", ru: "интеграция" },
      { path: () => `/routes/${ids.defaultRoute}`, en: "route", ru: "маршрут" },
      { path: () => `/teams/${ids.team}`, en: "team", ru: "команда" },
      { path: () => `/schedules/${ids.schedule}`, en: "schedule", ru: "расписание" },
      {
        path: () => `/escalation-chains/${ids.chain}`,
        en: "escalation chain",
        ru: "цепочка эскалации",
      },
      { path: () => `/connections/${ids.connection}`, en: "connection", ru: "подключение" },
      { path: () => `/destinations/${ids.destination}`, en: "destination", ru: "место доставки" },
      {
        path: () => `/admin/organization/lookup-tables/${ids.table}`,
        en: "lookup table",
        ru: "таблица соответствий",
      },
      {
        path: () => `/admin/organization/link-rules/${ids.rule}`,
        en: "link rule",
        ru: "правило ссылок",
      },
    ];
    for (const language of ["en", "ru"] as const) {
      await setLanguage(admin, language);
      const texts = language === "en" ? EN : RU;
      for (const kind of kinds) {
        await page.goto(kind.path());
        await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
        // The Organization is marked as well: the page of one of its objects shows only its own mark.
        if (kind.path() === `/integrations/${ids.integration}`) {
          // The read-only page of an Integration shows the mark; its warning waits on the edit page, which saves.
          await expect(page.getByTestId("managed-by-badge")).toHaveText(texts.badge);
          await dataLoaded(page, texts);
          await expect(page.getByRole("button", { name: texts.remove, exact: true })).toBeVisible();
          await expect(page.getByTestId("managed-by-warning")).toHaveCount(0);
          continue;
        }
        await expectMarked(page, texts, language === "en" ? kind.en : kind.ru);
        if (kind.path() === `/routes/${ids.defaultRoute}`) {
          // The Default route takes the mark like any Route, and still has no Delete.
          await expect(page.getByRole("button", { name: texts.remove, exact: true })).toHaveCount(
            0,
          );
        }
      }
    }
  } finally {
    await setLanguage(admin, null);
    await admin.dispose();
  }
});

test("the delete dialogs of the other kinds add the line, in English and in Russian", async ({
  page,
}) => {
  const admin = await adminApi();
  try {
    await signInAdmin(page);
    const dialogs: { path: string; en: string; ru: string }[] = [
      { path: `/integrations/${ids.integration}`, en: "Delete", ru: "Удалить" },
      { path: `/teams/${ids.team}`, en: "Delete Team", ru: "Удалить команду" },
      { path: `/schedules/${ids.schedule}`, en: "Delete Schedule", ru: "Удалить расписание" },
      { path: `/escalation-chains/${ids.chain}`, en: "Delete chain", ru: "Удалить цепочку" },
      { path: `/connections/${ids.connection}`, en: "Delete", ru: "Удалить" },
      { path: `/destinations/${ids.destination}`, en: "Delete", ru: "Удалить" },
      { path: `/admin/organization/lookup-tables/${ids.table}`, en: "Delete", ru: "Удалить" },
      { path: `/admin/organization/link-rules/${ids.rule}`, en: "Delete", ru: "Удалить" },
    ];
    for (const language of ["en", "ru"] as const) {
      await setLanguage(admin, language);
      const texts = language === "en" ? EN : RU;
      for (const entry of dialogs) {
        await page.goto(entry.path);
        await page.getByRole("button", { name: entry[language], exact: true }).click();
        const dialog = page.getByRole("dialog");
        await expect(dialog.getByTestId("managed-by-delete-line")).toHaveText(texts.deleteLine);
        await dialog.getByRole("button", { name: texts.cancel }).click();
        await expect(dialog).toHaveCount(0);
      }
    }
  } finally {
    await setLanguage(admin, null);
    await admin.dispose();
  }
});

test("the Organization's pages show the Organization's mark; Outbound and OIDC show their own", async ({
  page,
}) => {
  const admin = await adminApi();
  try {
    await mark(admin, "/api/v1/organization", organizationInput, TOOL);
    await signInAdmin(page);
    for (const language of ["en", "ru"] as const) {
      await setLanguage(admin, language);
      const texts = language === "en" ? EN : RU;
      const object = language === "en" ? "organization" : "организация";
      for (const path of [
        "general",
        "severity-levels",
        "alert-handling",
        "security",
        "retention",
        "outgoing-heartbeat",
      ]) {
        await page.goto(`/admin/organization/${path}`);
        await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
        await expectMarked(page, texts, object);
      }

      // Pages that do not edit the Organization show none of its mark: Keyring reads its own data first.
      await page.goto("/admin/organization/keyring");
      await dataLoaded(page, texts);
      await expect(page.locator("code").first()).toBeVisible();
      await expectUnmarked(page);

      // The Outbound address policy and the OIDC settings carry no mark of their own: none is shown, once read.
      await page.goto("/admin/organization/outbound");
      await dataLoaded(page, texts);
      await expect(page.locator("#outbound-always-blocked")).toBeVisible();
      await expectUnmarked(page);
      await page.goto("/admin/oidc");
      await dataLoaded(page, texts);
      await expect(page.getByTestId("oidc-status")).toBeAttached();
      await expectUnmarked(page);
    }

    // Marked through the API, each shows its own mark; the Organization's mark never appears on them.
    await setLanguage(admin, "en");
    await mark(admin, "/api/v1/organization/outbound-policy", policyInput, TOOL);
    await page.goto("/admin/organization/outbound");
    await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
    await expectMarked(page, EN, "outbound address policy");
    await mark(admin, "/api/v1/oidc-settings", oidcInput, TOOL);
    await page.goto("/admin/oidc");
    await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
    await expectMarked(page, EN, "OIDC configuration");
    expect((await read(admin, "/api/v1/oidc-settings")).body.managed_by).toBe(TOOL);
    await setLanguage(admin, "ru");
    await page.goto("/admin/oidc");
    await expectMarked(page, RU, "настройки OIDC");
    await setLanguage(admin, "en");

    // Saving a page of the Organization keeps its mark. The change is valid for any retention read, and the retention
    // read first is written back through the API in the end.
    const before = (await read(admin, "/api/v1/organization")).body.retention;
    try {
      await page.goto("/admin/organization/retention");
      await expect(page.getByTestId("managed-by-badge")).toHaveText(EN.badge);
      const details = page.getByLabel("Alert details (days)");
      const summaries = page.getByLabel("Alert Group summaries (days)");
      const detailsBefore = await details.inputValue();
      const summariesBefore = await summaries.inputValue();
      const unchanged = detailsBefore === "90" && summariesBefore === "90";
      await details.fill(unchanged ? "89" : "90");
      await summaries.fill("90");
      await page.getByRole("button", { name: "Save" }).click();
      await expect(page.getByTestId("retention-status")).toHaveText("Saved.");
      expect((await read(admin, "/api/v1/organization")).body.managed_by).toBe(TOOL);
      await expect(page.getByTestId("managed-by-badge")).toHaveText(EN.badge);
    } finally {
      await mark(
        admin,
        "/api/v1/organization",
        (o) => ({ ...organizationInput(o), retention: before }),
        TOOL,
      );
    }
  } finally {
    try {
      await setLanguage(admin, null);
    } finally {
      try {
        await unmarkSingletons(admin);
      } finally {
        await admin.dispose();
      }
    }
  }
});

test("the marked Route page does not scroll sideways at 360 pixels", async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 740 });
  await signInAdmin(page);
  await page.goto(`/routes/${ids.route}`);
  await expect(page.getByTestId("managed-by-badge")).toBeVisible();
  await expect(page.getByTestId("managed-by-warning")).toBeVisible();
  await expectNoHorizontalScroll(page);
});

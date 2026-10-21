// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The documentation links of the Statistics, Profile, Access and Organization pages (C-21.FR-11, C-21.AC-8) against
// `muster dev`, a development build whose documentation version is "latest": the Guide beside the title of every page
// of Access and of Organization's sub-navigation, of their items and new forms, of Statistics and of the Profile, also
// for a Responder; "Learn more" beside OIDC's group mapping, the TOTP policy, a Service account's Permissions, a Link
// rule's template, a Lookup table's rows and the allowed networks; "How to fix" beside the OIDC warnings that nobody
// can sign in and that the client secret expires, and beside the missing outgoing heartbeat of the System status. The
// banners and the Keyring's notices, which `muster dev` cannot bring about here, are covered by their component tests.
// The links read «Руководство», «Подробнее» and «Как исправить» in Russian, and the pages keep no horizontal scrollbar
// at 360 px. A link is compared by the origin, the path and the anchor of its address.

import { type Browser, type Locator, type Page, expect, test } from "@playwright/test";

import {
  type Api,
  adminApi,
  expectNoHorizontalScroll,
  signIn,
  signInAdmin,
  watchCsp,
} from "./support";

test.describe.configure({ mode: "serial" });

const DOCS_ORIGIN = "https://muster-io.github.io";
const DOCS_PATH = "/muster/latest/";
const PREFIX = "dla-";
const RESPONDER = "dla-responder";
const RU_ADMIN = "dla-admin-ru";
const PASSWORDS: Record<string, string> = {
  [RESPONDER]: "dla-responder-1",
  [RU_ADMIN]: "dla-admin-ru-1",
};
const SERVICE_ACCOUNT = "dla-terraform";
const LOOKUP_TABLE = "dla-grafana";
const NOBODY = "Nobody will be able to sign in through OIDC.";
const SUGGESTION = "Set up an outgoing heartbeat so that a stopped Muster is noticed.";

interface Named {
  id: string;
  name: string;
}

const ids = { admin: "", serviceAccount: "", lookupTable: "", linkRule: "" };

/** A link to the documentation by its accessible name, which says what it is about and that it opens a new tab. */
function docsLink(scope: Page | Locator, kind: string, about: string): Locator {
  return scope.getByRole("link", { name: `${kind}: ${about} (opens in a new tab)`, exact: true });
}

/** The link opens the page and section of the running version in a new tab. */
async function expectTarget(link: Locator, path: string, hash = ""): Promise<void> {
  await expect(link).toBeVisible();
  const href = await link.getAttribute("href");
  const url = new URL(href ?? "");
  expect(url.origin, href ?? "").toBe(DOCS_ORIGIN);
  expect(url.pathname, href ?? "").toBe(DOCS_PATH + path);
  expect(url.hash, href ?? "").toBe(hash);
  await expect(link).toHaveAttribute("target", "_blank");
  const rel = (await link.getAttribute("rel")) ?? "";
  expect(rel.split(" ")).toEqual(expect.arrayContaining(["noopener", "noreferrer"]));
}

/** The page's title shows exactly one Guide link, the one to the guide named about. */
async function expectGuide(page: Page, about: string, path: string, hash = ""): Promise<void> {
  const title = page.getByTestId("page-title");
  await expect(title).toHaveCount(1);
  await expect(title.getByRole("heading", { level: 1 })).toBeVisible();
  const link = docsLink(title, "Guide", about);
  await expect(link).toHaveText("Guide");
  await expectTarget(link, path, hash);
  await expect(title.getByRole("link")).toHaveCount(1);
}

/** Every documentation link of a page: the Guide, "Learn more" and "How to fix". */
function anyDocsLink(page: Page): Locator {
  return page.getByTestId("docs-link");
}

/** The pages of Organization's sub-navigation and the guide each title links to, by the keys of organization-nav. */
const ORGANIZATION: readonly (readonly [string, string, string, string])[] = [
  [
    "/admin/organization/general",
    "General Organization settings",
    "configure/organization/",
    "#general",
  ],
  [
    "/admin/organization/severity-levels",
    "Severity levels",
    "configure/organization/",
    "#severity-levels",
  ],
  [
    "/admin/organization/alert-handling",
    "Alert handling",
    "configure/organization/",
    "#alert-handling",
  ],
  ["/admin/organization/security", "Security settings", "configure/organization/", "#security"],
  [
    "/admin/organization/outbound",
    "Outbound addresses",
    "configure/organization/",
    "#outbound-addresses",
  ],
  ["/admin/organization/retention", "Retention", "configure/organization/", "#retention"],
  ["/admin/organization/keyring", "Key rotation", "operations/key-rotation/", ""],
  [
    "/admin/organization/outgoing-heartbeat",
    "Outgoing heartbeat",
    "operations/outgoing-heartbeat/",
    "",
  ],
  [
    "/admin/organization/lookup-tables",
    "Lookup tables",
    "configure/messages-and-templates/",
    "#add-a-lookup-table",
  ],
  [
    "/admin/organization/link-rules",
    "Link rules",
    "configure/messages-and-templates/",
    "#add-a-link-rule",
  ],
  ["/admin/system-status", "System status", "operations/monitoring/", "#system-status"],
  ["/admin/audit-log", "Audit log", "configure/organization/", "#audit-log"],
];

async function cleanUp(admin: Api): Promise<void> {
  const users = await admin.call<{ items: (Named & { login: string; status: string })[] }>(
    "GET",
    "/api/v1/users?limit=500",
  );
  for (const u of users.items.filter((x) => x.login.startsWith(PREFIX) && x.status !== "deleted")) {
    await admin.call("DELETE", `/api/v1/users/${u.id}`);
  }
  const accounts = await admin.call<{ items: (Named & { status: string })[] }>(
    "GET",
    "/api/v1/service-accounts?limit=500",
  );
  for (const a of accounts.items.filter(
    (x) => x.name.startsWith(PREFIX) && x.status !== "deleted",
  )) {
    await admin.call("DELETE", `/api/v1/service-accounts/${a.id}`);
  }
  const tables = await admin.call<{ items: Named[] }>("GET", "/api/v1/lookup-tables?limit=500");
  for (const t of tables.items.filter((x) => x.name.startsWith(PREFIX))) {
    await admin.call("DELETE", `/api/v1/lookup-tables/${t.id}`);
  }
}

async function createUser(admin: Api, login: string, role: string): Promise<void> {
  const user = await admin.createUser(login, role);
  await admin.call("POST", "/api/v1/password-setups", {
    token: user.token,
    password: PASSWORDS[login],
  });
}

test.beforeAll(async () => {
  test.setTimeout(120_000);
  const admin = await adminApi();
  try {
    await cleanUp(admin);
    await createUser(admin, RESPONDER, "responder");
    await createUser(admin, RU_ADMIN, "admin");
    const users = await admin.call<{ items: (Named & { login: string })[] }>(
      "GET",
      "/api/v1/users?limit=500",
    );
    ids.admin = users.items.find((u) => u.login === "admin@example.org")?.id ?? "";
    ids.serviceAccount = (
      await admin.call<{ id: string }>("POST", "/api/v1/service-accounts", {
        name: SERVICE_ACCOUNT,
        role: "viewer",
      })
    ).id;
    ids.lookupTable = (
      await admin.call<{ id: string }>("POST", "/api/v1/lookup-tables", {
        name: LOOKUP_TABLE,
        columns: ["address"],
        entries: [{ key: "prod", values: { address: "https://grafana.example.org" } }],
      })
    ).id;
    const rules = await admin.call<{ items: Named[] }>("GET", "/api/v1/link-rules?limit=500");
    ids.linkRule = rules.items[0]?.id ?? "";
  } finally {
    await admin.dispose();
  }
  expect(ids.admin, "the development Admin").not.toBe("");
  expect(ids.linkRule, "a built-in Link rule").not.toBe("");
});

test.afterAll(async () => {
  const admin = await adminApi();
  try {
    await cleanUp(admin);
  } finally {
    await admin.dispose();
  }
});

test("every page of Access and Organization, Statistics and the Profile link to their guides", async ({
  page,
}) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  for (const [address, about, path, hash] of [
    ["/statistics", "Statistics", "concepts/alert-groups/", "#statistics"],
    ["/profile", "Your profile", "sign-in/", "#your-profile"],
    ["/admin/users", "Adding Users", "sign-in/", "#add-a-user"],
    [`/admin/users/${ids.admin}`, "Adding Users", "sign-in/", "#add-a-user"],
    [
      "/admin/service-accounts",
      "Service accounts",
      "configure/api-tokens/",
      "#create-a-service-account",
    ],
    [
      `/admin/service-accounts/${ids.serviceAccount}`,
      "Service accounts",
      "configure/api-tokens/",
      "#create-a-service-account",
    ],
    ["/admin/oidc", "OIDC sign-in settings", "sign-in/oidc-keycloak/", "#4-configure-muster"],
    ...ORGANIZATION,
    [
      "/admin/organization/lookup-tables/new",
      "Lookup tables",
      "configure/messages-and-templates/",
      "#add-a-lookup-table",
    ],
    [
      `/admin/organization/lookup-tables/${ids.lookupTable}`,
      "Lookup tables",
      "configure/messages-and-templates/",
      "#add-a-lookup-table",
    ],
    [
      "/admin/organization/link-rules/new",
      "Link rules",
      "configure/messages-and-templates/",
      "#add-a-link-rule",
    ],
    [
      `/admin/organization/link-rules/${ids.linkRule}`,
      "Link rules",
      "configure/messages-and-templates/",
      "#add-a-link-rule",
    ],
  ] as const) {
    await page.goto(address);
    await expectGuide(page, about, path, hash);
  }

  // The pages of the sub-navigation take their key from its entries: the Guide changes with the entry followed.
  await page.goto("/admin/organization/general");
  const sub = page.getByRole("navigation", { name: "Organization" });
  await sub.getByRole("link", { name: "Retention", exact: true }).click();
  await expect(page).toHaveURL(/\/admin\/organization\/retention$/);
  await expectGuide(page, "Retention", "configure/organization/", "#retention");
  await sub.getByRole("link", { name: "Keyring", exact: true }).click();
  await expectGuide(page, "Key rotation", "operations/key-rotation/");
  expect(csp).toEqual([]);
});

test("Learn more beside the fields of the admin forms", async ({ page }) => {
  await signInAdmin(page);
  for (const [address, about, path, hash] of [
    ["/admin/oidc", "Group mapping", "sign-in/oidc-keycloak/", "#2-add-the-groups-claim"],
    ["/admin/organization/security", "TOTP policy", "sign-in/totp/", "#the-policy"],
    [
      `/admin/service-accounts/${ids.serviceAccount}`,
      "Service account Permissions",
      "configure/api-tokens/",
      "#two-kinds-of-token",
    ],
    [
      "/admin/organization/link-rules/new",
      "Link rule templates",
      "configure/messages-and-templates/",
      "#links",
    ],
    [
      "/admin/organization/lookup-tables/new",
      "Lookup table rows",
      "configure/messages-and-templates/",
      "#links",
    ],
    [
      `/admin/organization/lookup-tables/${ids.lookupTable}`,
      "Lookup table rows",
      "configure/messages-and-templates/",
      "#links",
    ],
    [
      "/admin/organization/outbound",
      "Allowed networks",
      "messengers/proxies/",
      "#what-the-outbound-address-policy-checks",
    ],
  ] as const) {
    await page.goto(address);
    const link = docsLink(page, "Learn more", about);
    await expect(link).toHaveText("Learn more");
    await expectTarget(link, path, hash);
  }

  // The fields keep their names: the link adds nothing to the name of the group it sits in.
  await page.goto("/admin/organization/security");
  await expect(page.getByRole("group", { name: "TOTP required", exact: true })).toBeVisible();
  await page.goto("/admin/organization/outbound");
  await expect(page.getByRole("group", { name: "Allowed", exact: true })).toBeVisible();
  await page.goto("/admin/organization/lookup-tables/new");
  await expect(page.getByRole("group", { name: "Rows", exact: true })).toBeVisible();
});

/** The "Learn more" about the Permissions a Service account's Role gives. */
function permissionsLink(scope: Page | Locator): Locator {
  return docsLink(scope, "Learn more", "Service account Permissions");
}

test("a Service account's Permissions have one Learn more, beside the Role a reader cannot change too", async ({
  page,
}) => {
  await signInAdmin(page);
  await page.goto(`/admin/service-accounts/${ids.serviceAccount}`);
  await expect(page.getByRole("heading", { name: "Role", level: 2 })).toBeVisible();
  await expect(page.getByTestId("sa-role")).toHaveText("Viewer");
  await expect(permissionsLink(page)).toHaveCount(1);

  // No Role has service-accounts:read without service-accounts:write, so the session is narrowed in the browser: the
  // Role card goes, and the link shows beside the Role in the details.
  await page.route("**/api/v1/sessions/current", async (route) => {
    const response = await route.fetch();
    const body: unknown = await response.json();
    if (typeof body === "object" && body !== null && "permissions" in body) {
      const permissions = Array.isArray(body.permissions) ? body.permissions : [];
      body.permissions = permissions.filter((p) => p !== "service-accounts:write");
    }
    await route.fulfill({ response, json: body });
  });
  await page.reload();
  await expect(page.getByTestId("sa-role")).toContainText("Viewer");
  await expect(page.getByRole("heading", { name: "Role", level: 2 })).toHaveCount(0);
  await expect(permissionsLink(page)).toHaveCount(1);
  await expectTarget(
    permissionsLink(page.getByTestId("sa-role")),
    "configure/api-tokens/",
    "#two-kinds-of-token",
  );
  await page.unroute("**/api/v1/sessions/current");
});

/** The fields of the OIDC settings this spec changes, as read. */
const OIDC_FIELDS = ["group_mappings", "unmatched_role", "client_secret_expires_on"] as const;

/**
 * Updates the OIDC settings: everything as read, the stored secrets kept, the changes applied. Returns the changed
 * fields as they were, to put them back.
 */
async function updateOidc(
  admin: Api,
  change: Record<string, unknown>,
): Promise<Record<string, unknown>> {
  const read = await admin.call<
    Record<string, unknown> & { etag: string; proxy: Record<string, unknown> }
  >("GET", "/api/v1/oidc-settings");
  const {
    etag,
    warnings: _warnings,
    client_secret_status: _status,
    updated_at: _updated,
    proxy,
    ...rest
  } = read;
  const { password_status: _password, ...proxyInput } = proxy;
  await admin.call(
    "PUT",
    "/api/v1/oidc-settings",
    { ...rest, proxy: proxyInput, ...change },
    { "If-Match": etag },
  );
  return Object.fromEntries(OIDC_FIELDS.map((f) => [f, rest[f] ?? null]));
}

test("How to fix beside the OIDC warnings and the missing outgoing heartbeat", async ({ page }) => {
  const soon = new Date(Date.now() + 10 * 24 * 60 * 60 * 1000).toISOString().slice(0, 10);
  const admin = await adminApi();
  const before = await updateOidc(admin, {
    group_mappings: [],
    unmatched_role: "none",
    client_secret_expires_on: soon,
  });
  try {
    await signInAdmin(page);
    await page.goto("/admin/oidc");
    const nobody = page.getByTestId("oidc-warning").filter({ hasText: NOBODY });
    await expect(nobody).toBeVisible();
    await expectTarget(
      docsLink(nobody, "How to fix", "Nobody can sign in through OIDC"),
      "sign-in/oidc-keycloak/",
      "#2-add-the-groups-claim",
    );
    await expect(docsLink(nobody, "How to fix", "Nobody can sign in through OIDC")).toHaveText(
      "How to fix",
    );
    const expiring = page
      .getByTestId("oidc-warning")
      .filter({ hasText: "The client secret expires on" });
    await expect(expiring).toBeVisible();
    await expectTarget(
      docsLink(expiring, "How to fix", "Expiring client secret"),
      "operations/runbooks/MusterOIDCSecretExpiring/",
    );
  } finally {
    await updateOidc(admin, before);
    await admin.dispose();
  }
  await page.reload();
  await expect(page.getByTestId("oidc-warning").filter({ hasText: NOBODY })).toHaveCount(0);

  await page.goto("/admin/system-status");
  const heartbeat = page.getByTestId("system-status-heartbeat");
  await expect(heartbeat.getByText(SUGGESTION)).toBeVisible();
  const fix = docsLink(heartbeat, "How to fix", "No outgoing heartbeat");
  await expect(fix).toHaveText("How to fix");
  await expectTarget(fix, "operations/outgoing-heartbeat/");
  await expect(heartbeat.getByRole("link", { name: SUGGESTION })).toBeVisible();
});

test("a Responder sees the Guide of Statistics and the Profile, and none on a page of the Admin", async ({
  browser,
}) => {
  const context = await browser.newContext();
  const page = await context.newPage();
  await signIn(page, RESPONDER, PASSWORDS[RESPONDER] ?? "");
  await page.goto("/statistics");
  await expectGuide(page, "Statistics", "concepts/alert-groups/", "#statistics");
  await page.goto("/profile");
  await expectGuide(page, "Your profile", "sign-in/", "#your-profile");
  await page.goto("/admin/organization/retention");
  await expect(page.getByText("You do not have permission to see this page.")).toBeVisible();
  await expect(anyDocsLink(page)).toHaveCount(0);
  await context.close();
});

test("the links read «Руководство», «Подробнее» and «Как исправить» in Russian", async ({
  browser,
}) => {
  const context = await browser.newContext();
  const page = await context.newPage();
  await signIn(page, RU_ADMIN, PASSWORDS[RU_ADMIN] ?? "");
  await page.goto("/profile");
  await page.getByLabel("Language").selectOption("ru");
  await expect(page.getByRole("navigation", { name: "Основная" })).toBeVisible();

  await page.goto("/admin/organization/retention");
  const guide = page.getByRole("link", {
    name: "Руководство: Хранение (откроется в новой вкладке)",
    exact: true,
  });
  await expect(guide).toHaveText("Руководство");
  await expectTarget(guide, "configure/organization/", "#retention");

  await page.goto("/admin/organization/security");
  const learnMore = page.getByRole("link", {
    name: "Подробнее: Политика TOTP (откроется в новой вкладке)",
    exact: true,
  });
  await expect(learnMore).toHaveText("Подробнее");
  await expectTarget(learnMore, "sign-in/totp/", "#the-policy");

  await page.goto("/admin/system-status");
  const fix = page.getByRole("link", {
    name: "Как исправить: Нет исходящего heartbeat (откроется в новой вкладке)",
    exact: true,
  });
  await expect(fix).toHaveText("Как исправить");
  await expectTarget(fix, "operations/outgoing-heartbeat/");

  await page.goto("/statistics");
  await expect(
    page.getByRole("link", {
      name: "Руководство: Статистика (откроется в новой вкладке)",
      exact: true,
    }),
  ).toHaveText("Руководство");
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNarrow(page, "/statistics in Russian");
  await context.close();
});

async function expectNarrow(page: Page, what: string): Promise<void> {
  await expectNoHorizontalScroll(page);
  const width = await page.evaluate(() => document.documentElement.scrollWidth);
  expect(width, what).toBeLessThanOrEqual(360);
}

async function narrow(browser: Browser): Promise<void> {
  const context = await browser.newContext({ viewport: { width: 360, height: 740 } });
  const page = await context.newPage();
  await signInAdmin(page);
  for (const [address, about] of [
    ["/statistics", "Statistics"],
    ["/admin/users", "Adding Users"],
    ["/admin/service-accounts", "Service accounts"],
    [`/admin/service-accounts/${ids.serviceAccount}`, "Service accounts"],
    ["/admin/oidc", "OIDC sign-in settings"],
    ...ORGANIZATION.map(([a, b]) => [a, b] as const),
    [`/admin/organization/lookup-tables/${ids.lookupTable}`, "Lookup tables"],
    ["/admin/organization/link-rules/new", "Link rules"],
  ] as const) {
    await page.goto(address);
    await expect(docsLink(page.getByTestId("page-title"), "Guide", about)).toBeVisible();
    await expectNarrow(page, address);
  }
  await context.close();
}

test("the Organization, Access and Statistics pages keep no horizontal scrollbar at 360 px", async ({
  browser,
}) => {
  await narrow(browser);
});

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The shell in a real browser: the navigation filtered by Permissions, and the banner area that a system-notices hint
// updates without a reload.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRouter,
} from "@tanstack/react-router";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { render } from "vitest-browser-react";

import { getListSystemNoticesQueryKey } from "../api/gen/endpoints/system/system";
import type { Permission, Session, SystemNotice } from "../api/gen/model";
import i18n from "../i18n";
import { type SessionRead, safeReturnTo } from "../lib/api";
import { connectLiveUpdates } from "../lib/live";
import { browserTimeZone, formatTime } from "../lib/time";
import { guardTarget } from "../routes/__root";
import { NAVIGATION, Navigation, type NavEntry, visibleEntries } from "./app-shell";
import { NoticeBanners } from "./notice-banners";

/** A stand-in for the browser's EventSource that the test drives. */
class FakeEventSource extends EventTarget {
  static last: FakeEventSource | undefined;
  readyState: number = EventSource.CONNECTING;
  readonly url: string;
  constructor(url: string) {
    super();
    this.url = url;
    FakeEventSource.last = this;
  }
  open() {
    this.readyState = EventSource.OPEN;
    this.dispatchEvent(new Event("open"));
  }
  hint(type: string) {
    this.dispatchEvent(new MessageEvent("hint", { data: JSON.stringify({ type, id: null }) }));
  }
  fail() {
    this.readyState = EventSource.CLOSED;
    this.dispatchEvent(new Event("error"));
  }
  close() {
    this.readyState = EventSource.CLOSED;
  }
}

let notices: SystemNotice[] = [];

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": status < 300 ? "application/json" : "application/problem+json" },
  });
}

beforeEach(async () => {
  notices = [];
  await i18n.changeLanguage("en");
  vi.spyOn(window, "fetch").mockImplementation((input) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    if (url.endsWith("/api/v1/system-notices")) {
      return Promise.resolve(json(200, { items: notices }));
    }
    return Promise.resolve(
      json(401, { type: "unauthenticated", title: "Unauthenticated", status: 401 }),
    );
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

function withProviders(queryClient: QueryClient, children: ReactNode) {
  return (
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    </I18nextProvider>
  );
}

describe("navigation", () => {
  const entries: NavEntry[] = [
    { to: "/statistics", label: "statistics" },
    { to: "/alert-groups", label: "alertGroups", permission: "alert-groups:read" },
  ];

  test("visibleEntries keeps the entries without a Permission and those the session holds", () => {
    expect(visibleEntries(entries, []).map((e) => e.to)).toEqual(["/statistics"]);
    expect(visibleEntries(entries, ["alert-groups:read"]).map((e) => e.to)).toEqual([
      "/statistics",
      "/alert-groups",
    ]);
  });

  async function renderNavigation(
    permissions: Permission[],
    shownEntries: readonly NavEntry[] = entries,
    path = "/",
  ) {
    const rootRoute = createRootRoute({
      component: () => <Navigation entries={shownEntries} permissions={permissions} />,
    });
    const router = createRouter({
      routeTree: rootRoute,
      history: createMemoryHistory({ initialEntries: [path] }),
    });
    return render(withProviders(new QueryClient(), <RouterProvider router={router} />));
  }

  test("shows an entry only to a session that holds its Permission", async () => {
    const without = await renderNavigation([]);
    await expect.element(without.getByRole("link", { name: "Statistics" })).toBeVisible();
    await expect
      .element(without.getByRole("link", { name: "Alert Groups" }))
      .not.toBeInTheDocument();
    await without.unmount();

    const holding = await renderNavigation(["alert-groups:read"]);
    await expect.element(holding.getByRole("link", { name: "Alert Groups" })).toBeVisible();
  });

  test("Connections follows Routes and needs connections:read", async () => {
    const index = NAVIGATION.findIndex((e) => e.to === "/connections");
    expect(NAVIGATION[index - 1]?.to).toBe("/routes");
    expect(NAVIGATION[index]).toEqual({
      to: "/connections",
      label: "connections",
      permission: "connections:read",
    });

    const shown = await renderNavigation(["connections:read"], NAVIGATION);
    await expect.element(shown.getByRole("link", { name: "Connections" })).toBeVisible();
    await shown.unmount();

    const without = await renderNavigation(["routes:read"], NAVIGATION);
    await expect.element(without.getByRole("link", { name: "Routes" })).toBeVisible();
    await expect
      .element(without.getByRole("link", { name: "Connections" }))
      .not.toBeInTheDocument();
  });

  test("Destinations follows Connections and needs destinations:read", async () => {
    const index = NAVIGATION.findIndex((e) => e.to === "/destinations");
    expect(NAVIGATION[index - 1]?.to).toBe("/connections");
    expect(NAVIGATION[index]).toEqual({
      to: "/destinations",
      label: "destinations",
      permission: "destinations:read",
    });

    const shown = await renderNavigation(["destinations:read"], NAVIGATION);
    await expect.element(shown.getByRole("link", { name: "Destinations" })).toBeVisible();
    await shown.unmount();

    const without = await renderNavigation(["connections:read"], NAVIGATION);
    await expect.element(without.getByRole("link", { name: "Connections" })).toBeVisible();
    await expect
      .element(without.getByRole("link", { name: "Destinations" }))
      .not.toBeInTheDocument();
  });

  test("eight entries, Access and Organization last, in place of the pages they group", () => {
    expect(NAVIGATION.map((e) => e.to)).toEqual([
      "/alert-groups",
      "/statistics",
      "/integrations",
      "/routes",
      "/connections",
      "/destinations",
      "/admin/access",
      "/admin/organization",
    ]);
    expect(NAVIGATION).toHaveLength(8);
    const moved = [
      "/profile",
      "/admin/users",
      "/admin/service-accounts",
      "/admin/oidc",
      "/admin/audit-log",
      "/admin/organization/security",
      "/admin/organization/lookup-tables",
      "/admin/system-status",
    ];
    expect(NAVIGATION.filter((e) => moved.includes(e.to))).toEqual([]);
    const access = NAVIGATION.find((e) => e.to === "/admin/access");
    expect(new Set(access?.permission)).toEqual(
      new Set(["users:read", "service-accounts:read", "oidc:read"]),
    );
    const organization = NAVIGATION.find((e) => e.to === "/admin/organization");
    expect(new Set(organization?.permission)).toEqual(
      new Set([
        "organization:write",
        "organization:read",
        "lookup-tables:read",
        "link-rules:read",
        "system-status:read",
        "audit-log:read",
      ]),
    );
  });

  test("an Admin sees eight entries, a Responder and a Viewer seven without Access", async () => {
    const admin: Permission[] = [
      "alert-groups:read",
      "integrations:read",
      "routes:read",
      "connections:read",
      "destinations:read",
      "users:read",
      "service-accounts:read",
      "oidc:read",
      "organization:read",
      "organization:write",
      "lookup-tables:read",
      "link-rules:read",
      "system-status:read",
      "audit-log:read",
    ];
    const responder: Permission[] = [
      "alert-groups:read",
      "alert-groups:acknowledge",
      "integrations:read",
      "routes:read",
      "connections:read",
      "destinations:read",
      "lookup-tables:read",
      "link-rules:read",
    ];
    const viewer: Permission[] = [
      "alert-groups:read",
      "integrations:read",
      "routes:read",
      "connections:read",
      "destinations:read",
      "lookup-tables:read",
      "link-rules:read",
    ];
    const seven = [
      "Alert Groups",
      "Statistics",
      "Integrations",
      "Routes",
      "Connections",
      "Destinations",
      "Organization",
    ];
    for (const [permissions, expected] of [
      [admin, [...seven.slice(0, 6), "Access", "Organization"]],
      [responder, seven],
      [viewer, seven],
    ] as const) {
      const shown = await renderNavigation(permissions, NAVIGATION);
      const nav = shown.getByRole("navigation", { name: "Main" });
      await expect.element(nav).toBeVisible();
      expect(
        nav
          .getByRole("link")
          .elements()
          .map((e) => e.textContent),
      ).toEqual(expected);
      await shown.unmount();
    }

    const auditOnly = await renderNavigation(["audit-log:read"], NAVIGATION);
    await expect.element(auditOnly.getByRole("link", { name: "Organization" })).toBeVisible();
    await expect.element(auditOnly.getByRole("link", { name: "Access" })).not.toBeInTheDocument();
  });

  test("an entry with several Permissions shows when the session holds any of them", async () => {
    const organization: NavEntry = {
      to: "/admin/organization",
      label: "organization",
      permission: ["organization:write", "link-rules:read"],
    };
    expect(visibleEntries([organization], ["link-rules:read"])).toEqual([organization]);
    expect(visibleEntries([organization], ["organization:write"])).toEqual([organization]);
    expect(visibleEntries([organization], [])).toEqual([]);

    const shown = await renderNavigation(["link-rules:read"], NAVIGATION);
    await expect.element(shown.getByRole("link", { name: "Organization" })).toBeVisible();
    await shown.unmount();

    const without = await renderNavigation(["alert-groups:read"], NAVIGATION);
    await expect
      .element(without.getByRole("link", { name: "Organization" }))
      .not.toBeInTheDocument();
  });

  test("Organization is active on every page of its sub-navigation, System status and Audit log included", async () => {
    const all: Permission[] = [
      "organization:write",
      "system-status:read",
      "audit-log:read",
      "users:read",
      "service-accounts:read",
      "oidc:read",
    ];
    const status = await renderNavigation(all, NAVIGATION, "/admin/system-status");
    await expect
      .element(status.getByRole("link", { name: "Organization" }))
      .toHaveAttribute("aria-current", "page");
    await expect
      .element(status.getByRole("link", { name: "Access" }))
      .not.toHaveAttribute("aria-current");
    await status.unmount();

    const keyring = await renderNavigation(all, NAVIGATION, "/admin/organization/keyring");
    await expect
      .element(keyring.getByRole("link", { name: "Organization" }))
      .toHaveAttribute("data-status", "active");
    await keyring.unmount();

    const audit = await renderNavigation(all, NAVIGATION, "/admin/audit-log");
    await expect
      .element(audit.getByRole("link", { name: "Organization" }))
      .toHaveAttribute("aria-current", "page");
    await audit.unmount();
  });

  test("Access is active on /admin/access and on every page of its sub-navigation", async () => {
    const all: Permission[] = [
      "users:read",
      "service-accounts:read",
      "oidc:read",
      "organization:write",
      "audit-log:read",
    ];
    for (const path of [
      "/admin/access",
      "/admin/users",
      "/admin/users/abc",
      "/admin/service-accounts",
      "/admin/service-accounts/abc",
      "/admin/oidc",
    ]) {
      const shown = await renderNavigation(all, NAVIGATION, path);
      await expect
        .element(shown.getByRole("link", { name: "Access" }))
        .toHaveAttribute("aria-current", "page");
      await expect
        .element(shown.getByRole("link", { name: "Organization" }))
        .not.toHaveAttribute("aria-current");
      await shown.unmount();
    }

    const sibling = await renderNavigation(all, NAVIGATION, "/admin/users-archive");
    await expect
      .element(sibling.getByRole("link", { name: "Access" }))
      .not.toHaveAttribute("aria-current");
    await sibling.unmount();
  });

  test("Statistics follows Alert Groups and needs alert-groups:read", async () => {
    const index = NAVIGATION.findIndex((e) => e.to === "/statistics");
    expect(NAVIGATION[index - 1]?.to).toBe("/alert-groups");
    expect(NAVIGATION[index]?.permission).toBe("alert-groups:read");

    const shown = await renderNavigation(["alert-groups:read"], NAVIGATION);
    await expect.element(shown.getByRole("link", { name: "Statistics" })).toBeVisible();
    await shown.unmount();

    const without = await renderNavigation([], NAVIGATION);
    await expect.element(without.getByRole("link", { name: "Statistics" })).not.toBeInTheDocument();
  });
});

describe("banners", () => {
  test("a system-notices hint shows the recovery banner and removes it, without a reload", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const close = connectLiveUpdates(queryClient, {
      onClosed: () => {},
      eventSource: FakeEventSource,
    });
    const source = FakeEventSource.last!;
    source.open();
    const screen = await render(withProviders(queryClient, <NoticeBanners />));
    const until = "2026-10-06T14:05:00Z";
    const text = `Muster is recovering after downtime. Data may be incomplete until ${formatTime(until, browserTimeZone(), "en")}.`;
    await vi.waitFor(() =>
      expect(queryClient.getQueryState(getListSystemNoticesQueryKey())?.status).toBe("success"),
    );
    await expect.element(screen.getByRole("status")).toBeEmptyDOMElement();

    notices = [{ kind: "recovering_after_downtime", audience: "all", since: null, until }];
    source.hint("system-notices");
    await expect.element(screen.getByText(text)).toBeVisible();

    notices = [];
    source.hint("system-notices");
    await expect.element(screen.getByText(text)).not.toBeInTheDocument();
    close();
  });

  test("shows the notice for Admins when the API returns it", async () => {
    notices = [{ kind: "no_replica_leading", audience: "admins", since: null, until: null }];
    const screen = await render(withProviders(new QueryClient(), <NoticeBanners />));
    await expect
      .element(
        screen.getByText(
          "No replica is leading: Telegram polling, Heartbeat and Stale checks are paused.",
        ),
      )
      .toBeVisible();
  });

  test("a reconnect invalidates every query, and a closed stream reports the end", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const onClosed = vi.fn();
    const close = connectLiveUpdates(queryClient, { onClosed, eventSource: FakeEventSource });
    const source = FakeEventSource.last!;
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    source.open();
    expect(invalidate).not.toHaveBeenCalled();
    source.open();
    expect(invalidate).toHaveBeenCalledWith();
    source.fail();
    expect(onClosed).toHaveBeenCalledTimes(1);
    close();
  });
});

describe("translations", () => {
  test("Russian plural forms", () => {
    const tr = i18n.getFixedT("ru");
    expect(tr("errors.tooManyAttempts", { count: 1 })).toContain("1 секунду");
    expect(tr("errors.tooManyAttempts", { count: 3 })).toContain("3 секунды");
    expect(tr("errors.tooManyAttempts", { count: 5 })).toContain("5 секунд");
    expect(tr("errors.tooManyAttempts", { count: 21 })).toContain("21 секунду");
  });

  test("the open Alert Groups of the delete dialogs, in both languages", () => {
    const en = i18n.getFixedT("en");
    expect(en("routes.delete.openAlertGroups", { count: 2 })).toBe(
      "This route has 2 open Alert Groups. Move them to the Default route to delete it.",
    );
    expect(en("integrations.delete.openAlertGroups", { count: 2 })).toBe(
      "2 open Alert Groups will be resolved.",
    );
    expect(en("integrations.delete.openAlertGroups", { count: 1 })).toBe(
      "1 open Alert Group will be resolved.",
    );
    const ru = i18n.getFixedT("ru");
    expect(ru("integrations.delete.openAlertGroups", { count: 1 })).toBe(
      "Будет закрыта 1 открытая группа алертов.",
    );
    expect(ru("integrations.delete.openAlertGroups", { count: 2 })).toBe(
      "Будут закрыты 2 открытые группы алертов.",
    );
    expect(ru("integrations.delete.openAlertGroups", { count: 5 })).toBe(
      "Будут закрыты 5 открытых групп алертов.",
    );
    expect(ru("routes.delete.openAlertGroups", { count: 21 })).toContain(
      "21 открытая группа алертов",
    );
  });
});

/** A session read in a state; the guard looks at nothing else. */
function session(state: Session["state"]): SessionRead {
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the guard reads only the state
  return { session: { state } as Session, ended: false };
}

describe("session guard", () => {
  const none = { session: null, ended: false };
  const ended = { session: null, ended: true };

  test.each([
    // [path, search, read, target]
    ["/", "", none, "/sign-in"],
    ["/profile", "?tab=x", none, "/sign-in?return_to=%2Fprofile%3Ftab%3Dx"],
    ["/profile", "", ended, "/sign-in?reason=session_ended&return_to=%2Fprofile"],
    ["/sign-in", "", none, undefined],
    ["/sign-in/totp", "", none, "/sign-in"],
    ["/totp-enrolment", "", none, "/sign-in"],
    ["/password-setup", "", none, undefined],
    ["/password-setup", "", session("active"), undefined],
    ["/", "", session("totp_required"), "/sign-in/totp"],
    [
      "/sign-in",
      "?return_to=%2Fprofile",
      session("totp_required"),
      "/sign-in/totp?return_to=%2Fprofile",
    ],
    ["/sign-in/totp", "", session("totp_required"), undefined],
    ["/totp-enrolment", "", session("totp_required"), "/sign-in/totp"],
    ["/profile", "", session("totp_enrolment_required"), "/totp-enrolment"],
    [
      "/sign-in",
      "?return_to=%2Fprofile",
      session("totp_enrolment_required"),
      "/totp-enrolment?return_to=%2Fprofile",
    ],
    ["/totp-enrolment", "", session("totp_enrolment_required"), undefined],
    ["/", "", session("active"), undefined],
    ["/no-such-page", "", session("active"), undefined],
    ["/sign-in", "", session("active"), "/"],
    ["/sign-in", "?return_to=%2Fprofile", session("active"), "/profile"],
    ["/sign-in", "?return_to=%2F%2Fevil.example", session("active"), "/"],
    ["/sign-in/totp", "", session("active"), "/"],
    ["/totp-enrolment", "", session("active"), "/"],
  ] as const)("%s%s with %o leads to %s", (path, search, read, target) => {
    expect(guardTarget(path, search, read)).toBe(target);
  });

  test("return_to stays a path of this application", () => {
    expect(safeReturnTo("/profile?x=1")).toBe("/profile?x=1");
    for (const bad of [
      "//evil.example",
      "/\\evil.example",
      "https://evil.example",
      "profile",
      "",
      "/a\u0000b",
      "/\nx",
      undefined,
      1,
    ]) {
      expect(safeReturnTo(bad)).toBeUndefined();
    }
    expect(safeReturnTo(`/${"a".repeat(2000)}`)).toBeUndefined();
  });
});

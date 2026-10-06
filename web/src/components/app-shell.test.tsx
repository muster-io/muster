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
import { Navigation, type NavEntry, visibleEntries } from "./app-shell";
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
    { to: "/", label: "home", exact: true },
    { to: "/profile", label: "profile", permission: "users:read" },
  ];

  test("visibleEntries keeps the entries without a Permission and those the session holds", () => {
    expect(visibleEntries(entries, []).map((e) => e.to)).toEqual(["/"]);
    expect(visibleEntries(entries, ["users:read"]).map((e) => e.to)).toEqual(["/", "/profile"]);
  });

  async function renderNavigation(permissions: Permission[]) {
    const rootRoute = createRootRoute({
      component: () => <Navigation entries={entries} permissions={permissions} />,
    });
    const router = createRouter({
      routeTree: rootRoute,
      history: createMemoryHistory({ initialEntries: ["/"] }),
    });
    return render(withProviders(new QueryClient(), <RouterProvider router={router} />));
  }

  test("shows an entry only to a session that holds its Permission", async () => {
    const without = await renderNavigation([]);
    await expect.element(without.getByRole("link", { name: "Home" })).toBeVisible();
    await expect.element(without.getByRole("link", { name: "Profile" })).not.toBeInTheDocument();
    await without.unmount();

    const holding = await renderNavigation(["users:read"]);
    await expect.element(holding.getByRole("link", { name: "Profile" })).toBeVisible();
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

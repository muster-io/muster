// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The application shell (C-03.FR-18) that every signed-in page lives in: the navigation, filtered by the Permissions
// of the session, the mark of the live updates (C-09.FR-26), the user menu, the banner area and the live updates.

import { useQueryClient } from "@tanstack/react-query";
import { Link, useRouterState } from "@tanstack/react-router";
import { useEffect, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import type { Permission } from "../api/gen/model";
import {
  SESSION_ENDED,
  SESSION_QUERY_KEY,
  type SessionRead,
  sessionQuery,
  signInHref,
  useSession,
} from "../lib/api";
import { connectLiveUpdates } from "../lib/live";
import { liveStatus } from "../lib/live-status";
import { LiveIndicator } from "./live-indicator";
import { NoticeBanners } from "./notice-banners";
import { ACCESS_NAVIGATION } from "./access-nav";
import { ORGANIZATION_NAVIGATION } from "./organization-nav";
import type { SectionEntry } from "./section-nav";
import { cn } from "./ui/utils";
import { UserMenu } from "./user-menu";

/** The Muster mark: the lines take the brand indigo of the theme (light indigo when dark), the point is amber. */
export function BrandMark({ className, title }: { className?: string; title?: string }) {
  return (
    <svg
      viewBox="0 0 64 64"
      className={cn("size-8 shrink-0", className)}
      role={title === undefined ? "presentation" : "img"}
      aria-hidden={title === undefined ? true : undefined}
      aria-label={title}
    >
      <g className="stroke-brand-line" strokeWidth="8" strokeLinecap="round" fill="none">
        <path d="M8 12 26 22" />
        <path d="M8 32H26" />
        <path d="M8 52 26 42" />
      </g>
      <circle cx="44" cy="32" r="12" className="fill-brand-point" />
    </svg>
  );
}

/** The frame of the pages without the shell: sign-in, second factor, enrolment and password setup. */
export function AuthLayout({ children, wide = false }: { children: ReactNode; wide?: boolean }) {
  return (
    <main className="flex min-h-dvh flex-col items-center px-4 py-10 sm:justify-center sm:py-16">
      <div className={cn("flex w-full flex-col gap-6", wide ? "max-w-lg" : "max-w-sm")}>
        <div className="flex items-center justify-center gap-3">
          <BrandMark className="size-10" />
          <span className="text-2xl font-semibold tracking-tight">Muster</span>
        </div>
        {children}
      </div>
    </main>
  );
}

/** A navigation entry; it is shown only when the session holds its Permission, or any of its Permissions. */
export interface NavEntry {
  to: string;
  /** The key of the entry's label under nav. */
  label: NavLabel;
  permission?: Permission | readonly Permission[];
  /** Marks the entry active only on its exact address. */
  exact?: boolean;
  /** Marks the entry active on every address under these paths as well, such as the pages of a sub-navigation. */
  activeOn?: readonly string[];
}

/** The labels of the entries; each page's story adds its own. */
export type NavLabel =
  | "alertGroups"
  | "statistics"
  | "integrations"
  | "routes"
  | "connections"
  | "destinations"
  | "access"
  | "organization";

/**
 * The entry of a section with a sub-navigation: shown when the session may open at least one of its sub-entries, and
 * active on its own address and on each of theirs.
 */
function sectionEntry<L extends string>(
  to: string,
  label: NavLabel,
  entries: readonly SectionEntry<L>[],
): NavEntry {
  return {
    to,
    label,
    permission: [...new Set(entries.map((e) => e.permission))],
    activeOn: [to, ...entries.map((e) => e.to)],
  };
}

/** The pages register their entries here, in the order of the navigation. */
export const NAVIGATION: readonly NavEntry[] = [
  { to: "/alert-groups", label: "alertGroups", permission: "alert-groups:read" },
  { to: "/statistics", label: "statistics", permission: "alert-groups:read" },
  { to: "/integrations", label: "integrations", permission: "integrations:read" },
  { to: "/routes", label: "routes", permission: "routes:read" },
  { to: "/connections", label: "connections", permission: "connections:read" },
  { to: "/destinations", label: "destinations", permission: "destinations:read" },
  sectionEntry("/admin/access", "access", ACCESS_NAVIGATION),
  sectionEntry("/admin/organization", "organization", ORGANIZATION_NAVIGATION),
];

/**
 * The entries whose Permission the session holds; an entry with several Permissions is shown when the session holds
 * any of them, and an entry without one is shown to everybody.
 */
export function visibleEntries(
  entries: readonly NavEntry[],
  permissions: readonly Permission[],
): NavEntry[] {
  return entries.filter((e) => {
    if (e.permission === undefined) {
      return true;
    }
    const any: readonly Permission[] =
      typeof e.permission === "string" ? [e.permission] : e.permission;
    return any.some((p) => permissions.includes(p));
  });
}

/** Whether an address lies under one of the paths of an entry's activeOn. */
function activeOn(entry: NavEntry, pathname: string): boolean {
  return (entry.activeOn ?? []).some((p) => pathname === p || pathname.startsWith(`${p}/`));
}

function navLabel(t: (key: string) => string, label: NavLabel): string {
  switch (label) {
    case "alertGroups":
      return t("nav.alertGroups");
    case "statistics":
      return t("nav.statistics");
    case "integrations":
      return t("nav.integrations");
    case "routes":
      return t("nav.routes");
    case "connections":
      return t("nav.connections");
    case "destinations":
      return t("nav.destinations");
    case "access":
      return t("nav.access");
    default:
      return t("nav.organization");
  }
}

/** Whether the session holds a Permission; the pages hide what it does not allow. */
export function useCan(permission: Permission): boolean {
  const session = useSession();
  return session?.permissions.includes(permission) === true;
}

/** What a page shows to a session without the Permission to see it. */
export function NoPermission() {
  const { t } = useTranslation();
  return (
    <section className="flex flex-col gap-2 py-6">
      <h1 className="text-xl font-semibold">{t("errors.noPermissionTitle")}</h1>
      <p className="text-muted-foreground">{t("errors.noPermission")}</p>
    </section>
  );
}

/** Shows a page only to a session that holds its Permission (C-03.FR-18); the API refuses the rest anyway. */
export function RequirePermission({
  permission,
  children,
}: {
  permission: Permission;
  children: ReactNode;
}) {
  if (!useCan(permission)) {
    return <NoPermission />;
  }
  return children;
}

export function Navigation({
  entries,
  permissions,
}: {
  entries: readonly NavEntry[];
  permissions: readonly Permission[];
}) {
  const { t } = useTranslation();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  return (
    <nav aria-label={t("nav.label")} className="min-w-0 overflow-x-auto">
      <ul className="flex items-center gap-1 py-1">
        {visibleEntries(entries, permissions).map((e) => (
          <li key={e.to}>
            <Link
              to={e.to}
              activeOptions={{ exact: e.exact === true }}
              {...(activeOn(e, pathname)
                ? { "data-status": "active", "aria-current": "page" as const }
                : {})}
              className="rounded-md px-2.5 py-1.5 text-sm font-medium whitespace-nowrap text-muted-foreground outline-none hover:bg-accent hover:text-accent-foreground focus-visible:ring-2 focus-visible:ring-ring data-[status=active]:bg-accent data-[status=active]:text-foreground"
            >
              {navLabel(t, e.label)}
            </Link>
          </li>
        ))}
      </ul>
    </nav>
  );
}

/** How long the shell waits before it opens the stream again after the server closed it for a reason other than the end of the session. */
const RECONNECT_DELAY_MS = 10_000;

/**
 * Opens the live updates for the signed-in session. The server closes the stream for good when the session ends; the
 * browser's reconnect then gets 401, which may be the answer that carried the reason. The shell reads the session
 * again: without one it leaves the page, with one it opens the stream again a little later. The state that the mark in
 * the header shows starts at connecting and spans the reopens.
 */
function useLiveUpdates(active: boolean) {
  const queryClient = useQueryClient();
  useEffect(() => {
    if (!active) {
      return undefined;
    }
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let close: (() => void) | undefined;
    liveStatus.start();
    const retry = () => {
      if (!stopped) {
        timer = setTimeout(open, RECONNECT_DELAY_MS);
      }
    };
    const open = () => {
      close = connectLiveUpdates(queryClient, {
        status: liveStatus,
        onClosed: () => {
          // A session that is gone leaves the page (see below); any other answer opens the stream again later.
          queryClient
            .fetchQuery({ ...sessionQuery, staleTime: 0 })
            .then((read) => {
              if (read.session !== null) {
                retry();
              }
            })
            .catch(retry);
        },
      });
    };
    open();
    return () => {
      stopped = true;
      clearTimeout(timer);
      close?.();
      liveStatus.stop();
    };
  }, [active, queryClient]);
}

export function AppShell({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  const session = useSession();
  const queryClient = useQueryClient();
  const active = session?.state === "active";
  useLiveUpdates(active);

  // The shell only shows for a signed-in session, so a session read as gone ended while the page was open: the first
  // answer after the end may have carried the reason, so the sign-in page always explains it.
  useEffect(() => {
    return queryClient.getQueryCache().subscribe((event) => {
      if (event.query.queryKey[0] !== SESSION_QUERY_KEY[0] || event.type !== "updated") {
        return;
      }
      const read = queryClient.getQueryData<SessionRead>(SESSION_QUERY_KEY);
      if (read !== undefined && read.session === null) {
        window.location.assign(signInHref(SESSION_ENDED));
      }
    });
  }, [queryClient]);

  return (
    <div className="flex min-h-dvh flex-col">
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:absolute focus:top-2 focus:left-2 focus:z-50 focus:rounded-md focus:bg-background focus:px-3 focus:py-2"
      >
        {t("shell.skipToContent")}
      </a>
      <header className="border-b bg-background">
        <div className="mx-auto flex h-14 max-w-7xl items-center gap-2 px-3 sm:gap-4 sm:px-6">
          <Link
            to="/alert-groups"
            className="flex shrink-0 items-center gap-2 rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <BrandMark className="size-7" />
            <span className="hidden text-base font-semibold tracking-tight sm:inline">Muster</span>
            <span className="sr-only sm:hidden">Muster</span>
          </Link>
          <Navigation entries={NAVIGATION} permissions={session?.permissions ?? []} />
          <div className="ml-auto flex shrink-0 items-center gap-1">
            <LiveIndicator />
            {session !== null && <UserMenu user={session.user} />}
          </div>
        </div>
      </header>
      <NoticeBanners />
      <main id="main" className="mx-auto w-full max-w-6xl flex-1 px-3 py-6 sm:px-6">
        {children}
      </main>
    </div>
  );
}

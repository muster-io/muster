// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The sub-navigation of a section of the main navigation, such as Organization and Access: its entries, each shown only
// with its Permission, the first page the session can open, and the layout that puts it above a page. It wraps onto
// several lines on a narrow screen instead of scrolling, so every entry stays in view.

import { Link, redirect } from "@tanstack/react-router";
import type { QueryClient } from "@tanstack/react-query";
import type { ComponentType, ReactNode } from "react";

import type { Permission } from "../api/gen/model";
import { SESSION_QUERY_KEY, type SessionRead, useSession } from "../lib/api";

/** A sub-entry: its address, the key of its label and the Permission that shows it. */
export interface SectionEntry<L extends string> {
  to: string;
  label: L;
  permission: Permission;
}

/** The sub-entries whose Permission the session holds. */
export function allowedEntries<L extends string>(
  entries: readonly SectionEntry<L>[],
  permissions: readonly Permission[],
): SectionEntry<L>[] {
  return entries.filter((e) => permissions.includes(e.permission));
}

/** The first sub-entry the session can open, where the section's address leads; undefined when there is none. */
export function firstAllowedPage<L extends string>(
  entries: readonly SectionEntry<L>[],
  permissions: readonly Permission[],
): string | undefined {
  return allowedEntries(entries, permissions)[0]?.to;
}

/**
 * The beforeLoad of a section's address: it leads to the first sub-entry the session can open. The root route has read
 * the session already; without a sub-entry to open the route renders its component, which says there is no access.
 */
export function leadToFirstPage<L extends string>(entries: readonly SectionEntry<L>[]) {
  return ({ context }: { context: { queryClient: QueryClient } }) => {
    const read = context.queryClient.getQueryData<SessionRead>(SESSION_QUERY_KEY);
    const to = firstAllowedPage(entries, read?.session?.permissions ?? []);
    if (to !== undefined) {
      throw redirect({ href: to, replace: true });
    }
  };
}

/** The sub-navigation landmark; a session that can open none of its entries gets none. */
export function SectionNav<L extends string>({
  name,
  entries,
  permissions,
  entryLabel,
}: {
  name: string;
  entries: readonly SectionEntry<L>[];
  permissions: readonly Permission[];
  entryLabel: (label: L) => string;
}) {
  const shown = allowedEntries(entries, permissions);
  if (shown.length === 0) {
    return null;
  }
  return (
    <nav aria-label={name} className="min-w-0">
      <ul className="flex flex-wrap gap-1">
        {shown.map((e) => (
          <li key={e.to}>
            <Link
              to={e.to}
              className="block rounded-md px-2.5 py-1.5 text-sm font-medium whitespace-nowrap text-muted-foreground outline-none hover:bg-accent hover:text-accent-foreground focus-visible:ring-2 focus-visible:ring-ring data-[status=active]:bg-accent data-[status=active]:text-foreground"
            >
              {entryLabel(e.label)}
            </Link>
          </li>
        ))}
      </ul>
    </nav>
  );
}

/** The frame of a section's pages: its sub-navigation, given the session's Permissions, above the page. */
export function SectionLayout({
  nav: Nav,
  children,
}: {
  nav: ComponentType<{ permissions: readonly Permission[] }>;
  children: ReactNode;
}) {
  const session = useSession();
  return (
    <div className="flex min-w-0 flex-col gap-6">
      <Nav permissions={session?.permissions ?? []} />
      {children}
    </div>
  );
}

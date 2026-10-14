// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The sub-navigation of Organization (C-20.FR-1): the settings pages of the Organization, each shown only with its
// Permission. It wraps onto several lines on a narrow screen instead of scrolling, so every entry stays in view.

import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import type { Permission } from "../api/gen/model";
import { useSession } from "../lib/api";

/** The labels of the sub-entries, under organization.nav. */
export type OrganizationNavLabel =
  | "general"
  | "severityLevels"
  | "alertHandling"
  | "security"
  | "outbound"
  | "retention"
  | "keyring"
  | "outgoingHeartbeat"
  | "lookupTables"
  | "linkRules"
  | "systemStatus";

export interface OrganizationEntry {
  to: string;
  label: OrganizationNavLabel;
  permission: Permission;
}

/** The sub-entries, in the order of the sub-navigation. */
export const ORGANIZATION_NAVIGATION: readonly OrganizationEntry[] = [
  { to: "/admin/organization/general", label: "general", permission: "organization:write" },
  {
    to: "/admin/organization/severity-levels",
    label: "severityLevels",
    permission: "organization:write",
  },
  {
    to: "/admin/organization/alert-handling",
    label: "alertHandling",
    permission: "organization:write",
  },
  { to: "/admin/organization/security", label: "security", permission: "organization:write" },
  { to: "/admin/organization/outbound", label: "outbound", permission: "organization:read" },
  { to: "/admin/organization/retention", label: "retention", permission: "organization:write" },
  { to: "/admin/organization/keyring", label: "keyring", permission: "organization:read" },
  {
    to: "/admin/organization/outgoing-heartbeat",
    label: "outgoingHeartbeat",
    permission: "organization:write",
  },
  {
    to: "/admin/organization/lookup-tables",
    label: "lookupTables",
    permission: "lookup-tables:read",
  },
  { to: "/admin/organization/link-rules", label: "linkRules", permission: "link-rules:read" },
  { to: "/admin/system-status", label: "systemStatus", permission: "system-status:read" },
];

/** The sub-entries whose Permission the session holds. */
export function organizationEntries(permissions: readonly Permission[]): OrganizationEntry[] {
  return ORGANIZATION_NAVIGATION.filter((e) => permissions.includes(e.permission));
}

/** The first sub-entry the session can open, where /admin/organization leads; undefined when there is none. */
export function firstOrganizationPage(permissions: readonly Permission[]): string | undefined {
  return organizationEntries(permissions)[0]?.to;
}

/** The title of a sub-entry, which is also the heading of its page. */
export function organizationNavLabel(
  t: (key: string) => string,
  label: OrganizationNavLabel,
): string {
  switch (label) {
    case "general":
      return t("organization.nav.general");
    case "severityLevels":
      return t("organization.nav.severityLevels");
    case "alertHandling":
      return t("organization.nav.alertHandling");
    case "security":
      return t("organization.nav.security");
    case "outbound":
      return t("organization.nav.outbound");
    case "retention":
      return t("organization.nav.retention");
    case "keyring":
      return t("organization.nav.keyring");
    case "outgoingHeartbeat":
      return t("organization.nav.outgoingHeartbeat");
    case "lookupTables":
      return t("organization.nav.lookupTables");
    case "linkRules":
      return t("organization.nav.linkRules");
    default:
      return t("organization.nav.systemStatus");
  }
}

export function OrganizationNav({ permissions }: { permissions: readonly Permission[] }) {
  const { t } = useTranslation();
  return (
    <nav aria-label={t("organization.nav.label")} className="min-w-0">
      <ul className="flex flex-wrap gap-1">
        {organizationEntries(permissions).map((e) => (
          <li key={e.to}>
            <Link
              to={e.to}
              className="block rounded-md px-2.5 py-1.5 text-sm font-medium whitespace-nowrap text-muted-foreground outline-none hover:bg-accent hover:text-accent-foreground focus-visible:ring-2 focus-visible:ring-ring data-[status=active]:bg-accent data-[status=active]:text-foreground"
            >
              {organizationNavLabel(t, e.label)}
            </Link>
          </li>
        ))}
      </ul>
    </nav>
  );
}

/** The frame of every Organization page: the sub-navigation above the page. */
export function OrganizationLayout({ children }: { children: ReactNode }) {
  const session = useSession();
  return (
    <div className="flex min-w-0 flex-col gap-6">
      <OrganizationNav permissions={session?.permissions ?? []} />
      {children}
    </div>
  );
}

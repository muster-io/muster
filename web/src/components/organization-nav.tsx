// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The sub-navigation of Organization (C-20.FR-1): the settings pages of the Organization and the Audit log, each shown
// only with its Permission, on the shared sub-navigation.

import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import type { Permission } from "../api/gen/model";
import {
  type SectionEntry,
  SectionLayout,
  SectionNav,
  allowedEntries,
  firstAllowedPage,
} from "./section-nav";

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
  | "systemStatus"
  | "auditLog";

export type OrganizationEntry = SectionEntry<OrganizationNavLabel>;

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
  { to: "/admin/audit-log", label: "auditLog", permission: "audit-log:read" },
];

/** The sub-entries whose Permission the session holds. */
export function organizationEntries(permissions: readonly Permission[]): OrganizationEntry[] {
  return allowedEntries(ORGANIZATION_NAVIGATION, permissions);
}

/** The first sub-entry the session can open, where /admin/organization leads; undefined when there is none. */
export function firstOrganizationPage(permissions: readonly Permission[]): string | undefined {
  return firstAllowedPage(ORGANIZATION_NAVIGATION, permissions);
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
    case "systemStatus":
      return t("organization.nav.systemStatus");
    default:
      return t("organization.nav.auditLog");
  }
}

export function OrganizationNav({ permissions }: { permissions: readonly Permission[] }) {
  const { t } = useTranslation();
  return (
    <SectionNav
      name={t("organization.nav.label")}
      entries={ORGANIZATION_NAVIGATION}
      permissions={permissions}
      entryLabel={(label) => organizationNavLabel(t, label)}
    />
  );
}

/** The frame of every Organization page: the sub-navigation above the page. */
export function OrganizationLayout({ children }: { children: ReactNode }) {
  return <SectionLayout nav={OrganizationNav}>{children}</SectionLayout>;
}

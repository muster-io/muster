// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The sub-navigation of Organization (C-20.FR-1): the settings pages of the Organization and the Audit log, each shown
// only with its Permission, on the shared sub-navigation, and each with the guide its title links to (C-21.FR-11).

import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import type { Permission } from "../api/gen/model";
import type { PageDocsKey } from "./docs-link";
import { type SectionEntry, SectionLayout, SectionNav, firstAllowedPage } from "./section-nav";

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

export interface OrganizationEntry extends SectionEntry<OrganizationNavLabel> {
  /** The entry of docs-links.json that the Guide link beside the page's title opens. */
  docs: PageDocsKey;
}

/** The sub-entries, in the order of the sub-navigation. */
export const ORGANIZATION_NAVIGATION: readonly OrganizationEntry[] = [
  {
    to: "/admin/organization/general",
    label: "general",
    permission: "organization:write",
    docs: "page.organization.general",
  },
  {
    to: "/admin/organization/severity-levels",
    label: "severityLevels",
    permission: "organization:write",
    docs: "page.organization.severity_levels",
  },
  {
    to: "/admin/organization/alert-handling",
    label: "alertHandling",
    permission: "organization:write",
    docs: "page.organization.alert_handling",
  },
  {
    to: "/admin/organization/security",
    label: "security",
    permission: "organization:write",
    docs: "page.organization.security",
  },
  {
    to: "/admin/organization/outbound",
    label: "outbound",
    permission: "organization:read",
    docs: "page.organization.outbound_addresses",
  },
  {
    to: "/admin/organization/retention",
    label: "retention",
    permission: "organization:write",
    docs: "page.organization.retention",
  },
  {
    to: "/admin/organization/keyring",
    label: "keyring",
    permission: "organization:read",
    docs: "page.organization.keyring",
  },
  {
    to: "/admin/organization/outgoing-heartbeat",
    label: "outgoingHeartbeat",
    permission: "organization:write",
    docs: "page.organization.outgoing_heartbeat",
  },
  {
    to: "/admin/organization/lookup-tables",
    label: "lookupTables",
    permission: "lookup-tables:read",
    docs: "page.lookup_tables",
  },
  {
    to: "/admin/organization/link-rules",
    label: "linkRules",
    permission: "link-rules:read",
    docs: "page.link_rules",
  },
  {
    to: "/admin/system-status",
    label: "systemStatus",
    permission: "system-status:read",
    docs: "page.system_status",
  },
  {
    to: "/admin/audit-log",
    label: "auditLog",
    permission: "audit-log:read",
    docs: "page.audit_log",
  },
];

/**
 * The guide of an Organization page: that of its entry, which the page at the entry's address and the pages under it
 * (a Lookup table, a new Link rule) share.
 */
export function organizationDocs(pathname: string): PageDocsKey | undefined {
  return ORGANIZATION_NAVIGATION.find((e) => pathname === e.to || pathname.startsWith(`${e.to}/`))
    ?.docs;
}

/** The sub-entries whose Permission the session holds. */
export function organizationEntries(permissions: readonly Permission[]): OrganizationEntry[] {
  return ORGANIZATION_NAVIGATION.filter((e) => permissions.includes(e.permission));
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

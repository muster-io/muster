// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The sub-navigation of Access (C-03.FR-18): Users, Service accounts and OIDC, each shown only with its Permission, on
// the shared sub-navigation.

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

/** The labels of the sub-entries, under access.nav. */
export type AccessNavLabel = "users" | "serviceAccounts" | "oidc";

export type AccessEntry = SectionEntry<AccessNavLabel>;

/** The sub-entries, in the order of the sub-navigation. */
export const ACCESS_NAVIGATION: readonly AccessEntry[] = [
  { to: "/admin/users", label: "users", permission: "users:read" },
  { to: "/admin/service-accounts", label: "serviceAccounts", permission: "service-accounts:read" },
  { to: "/admin/oidc", label: "oidc", permission: "oidc:read" },
];

/** The sub-entries whose Permission the session holds. */
export function accessEntries(permissions: readonly Permission[]): AccessEntry[] {
  return allowedEntries(ACCESS_NAVIGATION, permissions);
}

/** The first sub-entry the session can open, where /admin/access leads; undefined when there is none. */
export function firstAccessPage(permissions: readonly Permission[]): string | undefined {
  return firstAllowedPage(ACCESS_NAVIGATION, permissions);
}

/** The title of a sub-entry. */
export function accessNavLabel(t: (key: string) => string, label: AccessNavLabel): string {
  switch (label) {
    case "users":
      return t("access.nav.users");
    case "serviceAccounts":
      return t("access.nav.serviceAccounts");
    default:
      return t("access.nav.oidc");
  }
}

export function AccessNav({ permissions }: { permissions: readonly Permission[] }) {
  const { t } = useTranslation();
  return (
    <SectionNav
      name={t("access.nav.label")}
      entries={ACCESS_NAVIGATION}
      permissions={permissions}
      entryLabel={(label) => accessNavLabel(t, label)}
    />
  );
}

/** The frame of every Access page: the sub-navigation above the page. */
export function AccessLayout({ children }: { children: ReactNode }) {
  return <SectionLayout nav={AccessNav}>{children}</SectionLayout>;
}

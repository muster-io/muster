// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// /admin/organization leads to the first page of the sub-navigation the session can open. The root route has read the
// session already; a session that can open none of them sees that it has no access.

import { createFileRoute, redirect } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";

import { firstOrganizationPage } from "../components/organization-nav";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";

export const Route = createFileRoute("/admin/organization/")({
  staticData: { shell: true },
  beforeLoad: ({ context }) => {
    const read = context.queryClient.getQueryData<SessionRead>(SESSION_QUERY_KEY);
    const to = firstOrganizationPage(read?.session?.permissions ?? []);
    if (to !== undefined) {
      throw redirect({ href: to, replace: true });
    }
  },
  component: NoOrganizationPage,
});

function NoOrganizationPage() {
  const { t } = useTranslation();
  return (
    <section className="flex flex-col gap-2 py-6">
      <h1 className="text-xl font-semibold">{t("errors.noPermissionTitle")}</h1>
      <p className="text-muted-foreground">{t("errors.noPermission")}</p>
    </section>
  );
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The System status (C-19.FR-10, C-19.AC-6): a read-only page for Admins that lists what needs attention and links to
// where it is fixed. It is read again on its live hints and every 30 seconds; a failed read keeps the last one shown.
// It keeps its own address, to which other pages link, and shows the sub-navigation of Organization.

import { createFileRoute } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";

import { RequirePermission, useCan } from "../components/app-shell";
import { PageTitle } from "../components/docs-link";
import { OrganizationLayout } from "../components/organization-nav";
import { SystemStatusSections, useSystemStatus } from "../components/system-status-sections";
import { problemText } from "../lib/api";

export const Route = createFileRoute("/admin/system-status")({
  staticData: { shell: true },
  component: SystemStatusPage,
});

function SystemStatusView() {
  const { t } = useTranslation();
  const query = useSystemStatus();
  const canSetUpHeartbeat = useCan("organization:write");
  return (
    <div className="flex min-w-0 flex-col gap-6">
      <div className="flex flex-col gap-1">
        <PageTitle docs="page.system_status">{t("systemStatus.title")}</PageTitle>
        <p className="text-muted-foreground">{t("systemStatus.hint")}</p>
      </div>
      {query.data === undefined ? (
        <p className="text-sm text-muted-foreground" role="status">
          {query.isError ? problemText(t, query.error) : t("common.loading")}
        </p>
      ) : (
        <SystemStatusSections status={query.data} canSetUpHeartbeat={canSetUpHeartbeat} />
      )}
    </div>
  );
}

function SystemStatusPage() {
  return (
    <OrganizationLayout>
      <RequirePermission permission="system-status:read">
        <SystemStatusView />
      </RequirePermission>
    </OrganizationLayout>
  );
}

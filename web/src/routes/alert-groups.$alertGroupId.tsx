// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// An Alert Group's page (C-09.FR-14, FR-10, FR-16, FR-20, FR-24): the header first, then its notices, its Alerts
// (alerts:read) and its Timeline; beside them on a desktop, below them on a phone, its labels and annotations and the
// previous Alert Groups with the same key. Live hints read each part again as the Alert Group changes. Once its details
// were removed by retention, the page shows the summary and the notice, and no Alerts.

import { createFileRoute } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";

import { useGetAlertGroup } from "../api/gen/endpoints/alert-groups/alert-groups";
import { AlertGroupAlerts } from "../components/alert-group-alerts";
import { AlertGroupHeader } from "../components/alert-group-header";
import { AlertGroupLabels } from "../components/alert-group-labels";
import { AlertGroupNotices } from "../components/alert-group-notices";
import { RequirePermission, useCan } from "../components/app-shell";
import { RelatedAlertGroups } from "../components/related-alert-groups";
import { Timeline } from "../components/timeline";
import { problemText } from "../lib/api";

export const Route = createFileRoute("/alert-groups/$alertGroupId")({
  staticData: { shell: true },
  component: AlertGroupPage,
});

function AlertGroupView({ alertGroupId }: { alertGroupId: string }) {
  const { t } = useTranslation();
  const canAlerts = useCan("alerts:read");
  const query = useGetAlertGroup(alertGroupId);
  const group = query.data;
  if (group === undefined) {
    return (
      <p className="text-sm text-muted-foreground" role="status">
        {query.isError ? problemText(t, query.error) : t("common.loading")}
      </p>
    );
  }
  return (
    <div className="flex min-w-0 flex-col gap-6">
      <AlertGroupHeader group={group} />
      <AlertGroupNotices notices={group.notices ?? []} />
      <div className="grid min-w-0 grid-cols-1 gap-6 lg:grid-cols-3">
        <div className="flex min-w-0 flex-col gap-6 lg:col-span-2">
          {canAlerts && !group.details_removed && <AlertGroupAlerts alertGroupId={group.id} />}
          <Timeline alertGroupId={group.id} />
        </div>
        <div className="flex min-w-0 flex-col gap-6">
          <AlertGroupLabels group={group} />
          <RelatedAlertGroups alertGroupId={group.id} />
        </div>
      </div>
    </div>
  );
}

function AlertGroupPage() {
  const { alertGroupId } = Route.useParams();
  return (
    <RequirePermission permission="alert-groups:read">
      <AlertGroupView alertGroupId={alertGroupId} />
    </RequirePermission>
  );
}

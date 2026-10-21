// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Organization → Outgoing heartbeat (C-19.FR-7): the URL of the dead man's switch and its proxy, and the result of the
// last call as the System status reads it, for a user who may read that.

import { createFileRoute } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";

import { useGetOrganization } from "../api/gen/endpoints/organization/organization";
import { RequirePermission, useCan } from "../components/app-shell";
import { PageTitle } from "../components/docs-link";
import {
  OutgoingHeartbeatForm,
  OutgoingHeartbeatLastResult,
} from "../components/outgoing-heartbeat-form";
import { useSystemStatus } from "../components/system-status-sections";
import { problemText } from "../lib/api";

export const Route = createFileRoute("/admin/organization/outgoing-heartbeat")({
  staticData: { shell: true },
  component: OutgoingHeartbeatPage,
});

function OutgoingHeartbeatView() {
  const { t } = useTranslation();
  const query = useGetOrganization();
  const canRead = useCan("system-status:read");
  const status = useSystemStatus(canRead);
  return (
    <div className="flex min-w-0 flex-col gap-6">
      <PageTitle docs="page.organization.outgoing_heartbeat">
        {t("outgoingHeartbeat.title")}
      </PageTitle>
      {query.data === undefined ? (
        <p className="text-sm text-muted-foreground" role="status">
          {query.isError ? problemText(t, query.error) : t("common.loading")}
        </p>
      ) : (
        <div className="flex max-w-2xl min-w-0 flex-col gap-6">
          <OutgoingHeartbeatForm organization={query.data} />
          {canRead && status.data !== undefined && (
            <OutgoingHeartbeatLastResult result={status.data.outgoing_heartbeat} />
          )}
        </div>
      )}
    </div>
  );
}

function OutgoingHeartbeatPage() {
  return (
    <RequirePermission permission="organization:write">
      <OutgoingHeartbeatView />
    </RequirePermission>
  );
}

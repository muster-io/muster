// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// An Integration's page (C-05.FR-1, FR-2, FR-7, FR-8): the details with the Connection mode and its precision, the
// Static labels and the duplicate window; the last Snapshot and the number received; the tokens; the Stored Snapshots
// for stored-snapshots:read; Edit and Delete for integrations:write.

import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { ArrowLeftIcon, PencilIcon } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { useGetIntegration } from "../api/gen/endpoints/integrations/integrations";
import type { Integration } from "../api/gen/model";
import { RequirePermission, useCan } from "../components/app-shell";
import { IntegrationDeleteDialog } from "../components/integration-delete-dialog";
import { ConnectionMode } from "../components/integration-form";
import { IntegrationTokens } from "../components/integration-tokens";
import {
  type SnapshotSearch,
  StoredSnapshots,
  snapshotSearchSchema,
} from "../components/stored-snapshots";
import { buttonVariants } from "../components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "../components/ui/card";
import { problemText } from "../lib/api";
import { useTimeFormat } from "../lib/time";

export const Route = createFileRoute("/integrations/$integrationId/")({
  validateSearch: snapshotSearchSchema,
  staticData: { shell: true },
  component: IntegrationPage,
});

/** The Static labels as name=value chips. */
function StaticLabels({ labels }: { labels: Record<string, string> }) {
  const { t } = useTranslation();
  const names = Object.keys(labels).toSorted();
  if (names.length === 0) {
    return <span className="text-muted-foreground">{t("integrations.noStaticLabels")}</span>;
  }
  return (
    <ul className="flex flex-wrap gap-1" aria-label={t("integrations.fields.staticLabels")}>
      {names.map((name) => (
        <li
          key={name}
          className="rounded-md bg-muted px-1.5 py-0.5 font-mono text-xs wrap-anywhere"
          data-testid="static-label"
        >
          {name}={labels[name]}
        </li>
      ))}
    </ul>
  );
}

function Details({ integration }: { integration: Integration }) {
  const { t } = useTranslation();
  const rows: [string, ReactNode, string][] = [
    [t("integrations.fields.connectionMode"), <ConnectionMode key="mode" />, "connection-mode"],
    [
      t("integrations.fields.staticLabels"),
      <StaticLabels key="labels" labels={integration.static_labels} />,
      "static-labels",
    ],
    [
      t("integrations.fields.duplicateWindow"),
      t("integrations.seconds", { count: integration.duplicate_window_seconds }),
      "duplicate-window",
    ],
  ];
  if (integration.ingest_url) {
    rows.push([
      t("integrations.fields.ingestUrl"),
      <code key="url" className="font-mono text-xs wrap-anywhere">
        {integration.ingest_url}
      </code>,
      "ingest-url",
    ]);
  }
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2>{t("integrations.page.details")}</h2>
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {integration.description && (
          <p
            className="text-sm break-words whitespace-pre-line"
            data-testid="integration-description"
          >
            {integration.description}
          </p>
        )}
        <dl className="grid gap-x-4 gap-y-3 text-sm sm:grid-cols-[auto_1fr]">
          {rows.map(([label, value, id]) => (
            <div key={id} className="contents">
              <dt className="text-muted-foreground">{label}</dt>
              <dd className="min-w-0 break-words" data-testid={`integration-${id}`}>
                {value}
              </dd>
            </div>
          ))}
        </dl>
      </CardContent>
    </Card>
  );
}

function SnapshotsSummary({ integration }: { integration: Integration }) {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2>{t("integrations.page.snapshots")}</h2>
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-2 text-sm">
        <p data-testid="integration-last-snapshot">
          {integration.last_snapshot_at
            ? t("integrations.page.lastSnapshot", { time: dateTime(integration.last_snapshot_at) })
            : t("integrations.page.lastSnapshotNever")}
        </p>
        <p data-testid="integration-snapshot-count">
          {t("integrations.page.snapshotCount", { number: integration.snapshot_count })}
        </p>
      </CardContent>
    </Card>
  );
}

function IntegrationView({ integrationId }: { integrationId: string }) {
  const { t } = useTranslation();
  const canWrite = useCan("integrations:write");
  const canReadSnapshots = useCan("stored-snapshots:read");
  const search = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const query = useGetIntegration(integrationId);
  const integration = query.data;
  const onSearch = (patch: Partial<SnapshotSearch>) =>
    void navigate({ search: (prev) => ({ ...prev, ...patch }), replace: true });
  const writable = canWrite && integration?.builtin === false;
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Link
          to="/integrations"
          className={buttonVariants({ variant: "link", className: "w-fit px-0" })}
        >
          <ArrowLeftIcon aria-hidden="true" />
          {t("integrations.title")}
        </Link>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h1 className="min-w-0 text-2xl font-semibold tracking-tight break-words">
            {integration?.name ?? t("integrations.page.title")}
          </h1>
          {integration !== undefined && writable && (
            <div className="flex flex-wrap gap-2">
              <Link
                to="/integrations/$integrationId/edit"
                params={{ integrationId: integration.id }}
                className={buttonVariants({ variant: "outline" })}
              >
                <PencilIcon aria-hidden="true" />
                {t("integrations.page.edit")}
              </Link>
              <IntegrationDeleteDialog integration={integration} />
            </div>
          )}
        </div>
      </div>
      {integration === undefined ? (
        <p className="text-sm text-muted-foreground" role="status">
          {query.isError ? problemText(t, query.error) : t("common.loading")}
        </p>
      ) : (
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
          <Details integration={integration} />
          <div className="flex flex-col gap-6">
            <SnapshotsSummary integration={integration} />
            {!integration.builtin && (
              <IntegrationTokens integration={integration} canWrite={canWrite} />
            )}
          </div>
          {canReadSnapshots && (
            <StoredSnapshots integrationId={integration.id} search={search} onSearch={onSearch} />
          )}
        </div>
      )}
    </div>
  );
}

function IntegrationPage() {
  const { integrationId } = Route.useParams();
  return (
    <RequirePermission permission="integrations:read">
      <IntegrationView integrationId={integrationId} />
    </RequirePermission>
  );
}

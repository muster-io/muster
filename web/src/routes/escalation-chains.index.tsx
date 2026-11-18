// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Escalation chains (C-32.FR-1, FR-2; C-25.FR-2): every chain with its owning Team, its steps and the Routes and Teams
// that name it, page by page, filtered by Team or "My Teams" (the search parameter `team`); "New chain" with
// escalation-chains:write. The list has no entry in the main navigation: the Teams page, each Team page and the Route
// form link to it.

import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import {
  getListEscalationChainsQueryKey,
  listEscalationChains,
} from "../api/gen/endpoints/escalation-chains/escalation-chains";
import type { EscalationChain } from "../api/gen/model";
import { RequirePermission, useCan } from "../components/app-shell";
import { type DataColumn, DataTable, useCursorList } from "../components/data-table";
import { PageTitle } from "../components/docs-link";
import { MY_TEAMS, TeamFilter } from "../components/team-filter";
import { buttonVariants } from "../components/ui/button";

// The filter by Team is in the URL: the id of a Team, or "mine" for the Teams of the signed-in User.
const searchSchema = z.object({
  team: z.string().min(1).optional().catch(undefined),
});

export const Route = createFileRoute("/escalation-chains/")({
  validateSearch: searchSchema,
  staticData: { shell: true },
  component: EscalationChainsPage,
});

function NameCell({ row }: { row: EscalationChain }) {
  return (
    <Link
      to="/escalation-chains/$chainId"
      params={{ chainId: row.id }}
      className="font-medium wrap-anywhere text-primary underline-offset-4 hover:underline focus-visible:underline"
    >
      {row.name}
    </Link>
  );
}

function TeamCell({ row }: { row: EscalationChain }) {
  const { t } = useTranslation();
  if (row.owning_team === null) {
    return <span className="text-muted-foreground">{t("escalation.form.noOwner")}</span>;
  }
  return (
    <Link
      to="/teams/$teamId"
      params={{ teamId: row.owning_team.id }}
      className="wrap-anywhere text-primary underline-offset-4 hover:underline focus-visible:underline"
    >
      {row.owning_team.name}
    </Link>
  );
}

function NamedByCell({ row }: { row: EscalationChain }) {
  const { t } = useTranslation();
  const names = [
    ...row.referrers.routes.map((r) => t("escalation.runFor.route", { name: r.route.name })),
    ...row.referrers.teams.map((r) => t("escalation.runFor.team", { name: r.team.name })),
  ];
  return names.length === 0 ? (
    <span className="text-muted-foreground">{t("escalation.list.unused")}</span>
  ) : (
    <span className="wrap-anywhere">{names.join(", ")}</span>
  );
}

function ChainsTable({ team }: { team: string | undefined }) {
  const { t } = useTranslation();
  const filter = team === undefined ? {} : { team };
  const list = useCursorList<EscalationChain>(
    getListEscalationChainsQueryKey(filter),
    (cursor, signal) => listEscalationChains({ cursor, ...filter }, { signal }),
  );
  const columns = useMemo(
    (): DataColumn<EscalationChain>[] => [
      { id: "name", header: t("escalation.form.name"), className: "min-w-32", Cell: NameCell },
      { id: "team", header: t("escalation.form.owner"), className: "min-w-32", Cell: TeamCell },
      {
        id: "steps",
        header: t("escalation.steps.title"),
        text: (row) => String(row.steps.length),
      },
      {
        id: "named-by",
        header: t("escalation.list.namedBy"),
        className: "min-w-40",
        Cell: NamedByCell,
      },
    ],
    [t],
  );
  return (
    <DataTable
      label={t("escalation.title")}
      columns={columns}
      list={list}
      rowId={(row) => row.id}
      empty={team === undefined ? t("escalation.list.empty") : t("escalation.list.emptyFiltered")}
    />
  );
}

function EscalationChainsPage() {
  const { t } = useTranslation();
  const canWrite = useCan("escalation-chains:write");
  const { team } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  return (
    <RequirePermission permission="escalation-chains:read">
      <div className="flex flex-col gap-6">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex min-w-0 flex-col gap-1">
            <PageTitle docs="page.escalation_chain">{t("escalation.title")}</PageTitle>
            <p className="text-muted-foreground">{t("escalation.list.hint")}</p>
          </div>
          {canWrite && (
            <Link
              to="/escalation-chains/new"
              search={team === undefined || team === MY_TEAMS ? {} : { team }}
              className={buttonVariants()}
            >
              {t("escalation.list.create")}
            </Link>
          )}
        </div>
        <TeamFilter
          value={team}
          onChange={(next) => void navigate({ search: { team: next }, replace: true })}
        />
        <ChainsTable team={team} />
      </div>
    </RequirePermission>
  );
}

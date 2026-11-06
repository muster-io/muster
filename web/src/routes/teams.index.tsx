// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Teams (C-25.FR-3, C-25.AC-10): every Team with its name, its slug and how many people are in it, page by page; every
// Role reads the list, and "New Team" needs teams:write. A Team grants no access of its own.

import { Link, createFileRoute } from "@tanstack/react-router";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";

import { getListTeamsQueryKey, listTeams } from "../api/gen/endpoints/teams/teams";
import type { Team } from "../api/gen/model";
import { RequirePermission, useCan } from "../components/app-shell";
import { type DataColumn, DataTable, useCursorList } from "../components/data-table";
import { PageTitle } from "../components/docs-link";
import { buttonVariants } from "../components/ui/button";

export const Route = createFileRoute("/teams/")({
  staticData: { shell: true },
  component: TeamsPage,
});

function NameCell({ row }: { row: Team }) {
  return (
    <Link
      to="/teams/$teamId"
      params={{ teamId: row.id }}
      className="font-medium wrap-anywhere text-primary underline-offset-4 hover:underline focus-visible:underline"
    >
      {row.name}
    </Link>
  );
}

function SlugCell({ row }: { row: Team }) {
  return <code className="font-mono text-xs wrap-anywhere">{row.slug}</code>;
}

function PeopleCell({ row }: { row: Team }) {
  const { t } = useTranslation();
  return (
    <span data-testid="team-people-count">{t("teams.list.people", { count: row.user_count })}</span>
  );
}

function TeamsTable() {
  const { t } = useTranslation();
  const list = useCursorList<Team>(getListTeamsQueryKey(), (cursor, signal) =>
    listTeams({ cursor }, { signal }),
  );
  const columns = useMemo(
    (): DataColumn<Team>[] => [
      { id: "name", header: t("teams.fields.name"), className: "min-w-32", Cell: NameCell },
      { id: "slug", header: t("teams.fields.slug"), className: "min-w-28", Cell: SlugCell },
      { id: "people", header: t("teams.fields.people"), Cell: PeopleCell },
    ],
    [t],
  );
  return (
    <DataTable
      label={t("teams.title")}
      columns={columns}
      list={list}
      rowId={(row) => row.id}
      empty={t("teams.list.empty")}
    />
  );
}

function TeamsPage() {
  const { t } = useTranslation();
  const canWrite = useCan("teams:write");
  return (
    <RequirePermission permission="teams:read">
      <div className="flex flex-col gap-6">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex min-w-0 flex-col gap-1">
            <PageTitle docs="page.teams">{t("teams.title")}</PageTitle>
            <p className="text-muted-foreground">{t("teams.hint")}</p>
          </div>
          {canWrite && (
            <Link to="/teams/new" className={buttonVariants()}>
              {t("teams.create.start")}
            </Link>
          )}
        </div>
        <TeamsTable />
      </div>
    </RequirePermission>
  );
}

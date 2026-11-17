// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Teams (C-25.FR-3, FR-7, C-25.AC-10): the Team label with its hint, every Team with its name, its slug and how many
// people are in it, page by page, and the values of the Team label that name no Team; every Role reads them, and "New
// Team" and "Create Team" need teams:write. A Team grants no access of its own.

import { Link, createFileRoute } from "@tanstack/react-router";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";

import { getListTeamsQueryKey, listTeams } from "../api/gen/endpoints/teams/teams";
import type { Team } from "../api/gen/model";
import { RequirePermission, useCan } from "../components/app-shell";
import { type DataColumn, DataTable, useCursorList } from "../components/data-table";
import { OnCallCell } from "../components/on-call-now";
import { PageTitle } from "../components/docs-link";
import { TeamLabelLine, TeamLabelValues } from "../components/team-label-values";
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

function MentionsCell({ row }: { row: Team }) {
  const { t } = useTranslation();
  const mentions = row.mention_connections.flatMap((mc) =>
    mc.team_mention === null ? [] : [mc.team_mention],
  );
  if (mentions.length === 0) {
    return (
      <span className="text-muted-foreground" data-testid="team-mention-none">
        {t("teams.list.noTeamMention")}
      </span>
    );
  }
  return (
    <ul className="flex flex-col gap-0.5" data-testid="team-mentions-cell">
      {mentions.map((m) => (
        <li
          key={m.connection_id}
          className={
            m.available
              ? "font-mono text-xs wrap-anywhere"
              : "font-mono text-xs wrap-anywhere text-muted-foreground line-through"
          }
          title={m.connection.name}
        >
          @{m.group}
        </li>
      ))}
    </ul>
  );
}

function OnCallNowCell({ row }: { row: Team }) {
  return <OnCallCell teamId={row.id} />;
}

function TeamsTable() {
  const { t } = useTranslation();
  const canSchedules = useCan("schedules:read");
  const list = useCursorList<Team>(getListTeamsQueryKey(), (cursor, signal) =>
    listTeams({ cursor }, { signal }),
  );
  const columns = useMemo(
    (): DataColumn<Team>[] => [
      { id: "name", header: t("teams.fields.name"), className: "min-w-32", Cell: NameCell },
      { id: "slug", header: t("teams.fields.slug"), className: "min-w-28", Cell: SlugCell },
      { id: "people", header: t("teams.fields.people"), Cell: PeopleCell },
      {
        id: "mentions",
        header: t("teams.fields.teamMention"),
        className: "min-w-32",
        Cell: MentionsCell,
      },
      ...(canSchedules
        ? [
            {
              id: "on-call",
              header: t("onCallNow.title"),
              className: "min-w-40",
              Cell: OnCallNowCell,
            },
          ]
        : []),
    ],
    [t, canSchedules],
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
  const canSchedules = useCan("schedules:read");
  return (
    <RequirePermission permission="teams:read">
      <div className="flex flex-col gap-6">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex min-w-0 flex-col gap-1">
            <PageTitle docs="page.teams">{t("teams.title")}</PageTitle>
            <p className="text-muted-foreground">{t("teams.hint")}</p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {canSchedules && (
              <Link to="/schedules" className={buttonVariants({ variant: "outline" })}>
                {t("teams.schedules.all")}
              </Link>
            )}
            {canWrite && (
              <Link to="/teams/new" className={buttonVariants()}>
                {t("teams.create.start")}
              </Link>
            )}
          </div>
        </div>
        <TeamLabelLine />
        <TeamsTable />
        <TeamLabelValues />
      </div>
    </RequirePermission>
  );
}

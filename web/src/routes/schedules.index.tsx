// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Schedules (C-25.FR-2, C-27.FR-1): every Schedule with its Team and "main", page by page, filtered by Team or "My
// Teams" (the search parameter `team`); "New Schedule" with schedules:write. The list has no entry in the main
// navigation: the Teams page links to it as "All Schedules".

import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import { getListSchedulesQueryKey, listSchedules } from "../api/gen/endpoints/schedules/schedules";
import type { Schedule } from "../api/gen/model";
import { RequirePermission, useCan } from "../components/app-shell";
import { type DataColumn, DataTable, useCursorList } from "../components/data-table";
import { PageTitle } from "../components/docs-link";
import { MY_TEAMS, TeamFilter } from "../components/team-filter";
import { buttonVariants } from "../components/ui/button";

// The filter by Team is in the URL: the id of a Team, or "mine" for the Teams of the signed-in User.
const searchSchema = z.object({
  team: z.string().min(1).optional().catch(undefined),
});

export const Route = createFileRoute("/schedules/")({
  validateSearch: searchSchema,
  staticData: { shell: true },
  component: SchedulesPage,
});

function NameCell({ row }: { row: Schedule }) {
  return (
    <Link
      to="/schedules/$scheduleId"
      params={{ scheduleId: row.id }}
      className="font-medium wrap-anywhere text-primary underline-offset-4 hover:underline focus-visible:underline"
    >
      {row.name}
    </Link>
  );
}

function TeamCell({ row }: { row: Schedule }) {
  return (
    <Link
      to="/teams/$teamId"
      params={{ teamId: row.team.id }}
      className="wrap-anywhere text-primary underline-offset-4 hover:underline focus-visible:underline"
    >
      {row.team.name}
    </Link>
  );
}

function MainCell({ row }: { row: Schedule }) {
  const { t } = useTranslation();
  return row.main ? (
    <span
      className="rounded-md bg-primary/10 px-2 py-0.5 text-xs font-medium text-primary"
      data-testid="schedule-main"
    >
      {t("schedule.main")}
    </span>
  ) : null;
}

function SchedulesTable({ team }: { team: string | undefined }) {
  const { t } = useTranslation();
  const filter = team === undefined ? {} : { team };
  const list = useCursorList<Schedule>(getListSchedulesQueryKey(filter), (cursor, signal) =>
    listSchedules({ cursor, ...filter }, { signal }),
  );
  const columns = useMemo(
    (): DataColumn<Schedule>[] => [
      { id: "name", header: t("schedule.fields.name"), className: "min-w-32", Cell: NameCell },
      { id: "team", header: t("schedule.fields.team"), className: "min-w-32", Cell: TeamCell },
      { id: "main", header: t("schedule.fields.main"), Cell: MainCell },
      {
        id: "layers",
        header: t("schedule.fields.layers"),
        text: (row) => String(row.rotation_layers.length),
      },
      { id: "zone", header: t("schedule.fields.timeZone"), text: (row) => row.time_zone },
    ],
    [t],
  );
  return (
    <DataTable
      label={t("schedule.list.title")}
      columns={columns}
      list={list}
      rowId={(row) => row.id}
      empty={team === undefined ? t("schedule.list.empty") : t("schedule.list.emptyFiltered")}
    />
  );
}

function SchedulesPage() {
  const { t } = useTranslation();
  const canWrite = useCan("schedules:write");
  const { team } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  return (
    <RequirePermission permission="schedules:read">
      <div className="flex flex-col gap-6">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex min-w-0 flex-col gap-1">
            <PageTitle docs="page.schedule">{t("schedule.list.title")}</PageTitle>
            <p className="text-muted-foreground">{t("schedule.list.hint")}</p>
          </div>
          {canWrite && (
            <Link
              to="/schedules/new"
              search={team === undefined || team === MY_TEAMS ? {} : { team }}
              className={buttonVariants()}
            >
              {t("schedule.create.start")}
            </Link>
          )}
        </div>
        <TeamFilter
          value={team}
          onChange={(next) => void navigate({ search: { team: next }, replace: true })}
        />
        <SchedulesTable team={team} />
      </div>
    </RequirePermission>
  );
}

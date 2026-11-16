// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "New Schedule" (C-27.FR-1, FR-2, FR-13): the Team, the name, the time zone and the Rotation layers; the Schedule's
// page opens after it is created. The first Schedule of a Team is its main Schedule. Only Admins (schedules:write).

import { useQueryClient } from "@tanstack/react-query";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { ArrowLeftIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import { createSchedule, getListSchedulesQueryKey } from "../api/gen/endpoints/schedules/schedules";
import { RequirePermission } from "../components/app-shell";
import { PageTitle } from "../components/docs-link";
import { ScheduleForm } from "../components/schedule-form";
import { buttonVariants } from "../components/ui/button";

const searchSchema = z.object({
  team: z.string().min(1).optional().catch(undefined),
});

export const Route = createFileRoute("/schedules/new")({
  staticData: { shell: true },
  validateSearch: searchSchema,
  component: NewSchedulePage,
});

function NewSchedule() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const { team } = Route.useSearch();
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Link
          to="/schedules"
          search={team === undefined ? {} : { team }}
          className={buttonVariants({ variant: "link", className: "w-fit px-0" })}
        >
          <ArrowLeftIcon aria-hidden="true" />
          {t("schedule.list.title")}
        </Link>
        <PageTitle docs="page.schedule">{t("schedule.create.title")}</PageTitle>
      </div>
      <ScheduleForm
        {...(team === undefined ? {} : { initialTeam: team })}
        submitLabel={t("common.save")}
        onCancel={() =>
          void navigate({ to: "/schedules", search: team === undefined ? {} : { team } })
        }
        save={async (input) => {
          const created = await createSchedule(input);
          void queryClient.invalidateQueries({ queryKey: getListSchedulesQueryKey() });
          await navigate({ to: "/schedules/$scheduleId", params: { scheduleId: created.id } });
        }}
      />
    </div>
  );
}

function NewSchedulePage() {
  return (
    <RequirePermission permission="schedules:write">
      <NewSchedule />
    </RequirePermission>
  );
}

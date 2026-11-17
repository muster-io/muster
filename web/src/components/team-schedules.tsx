// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Schedules of a Team's page (C-27.FR-1): each Schedule with "main", its Rotation layers, its On-call now and its
// warnings, linked to its page, "New Schedule" for Admins (schedules:write) and the hint that the main Schedule names
// the Team's On-call, while Escalation steps may Page the On-call of any of them. The On-call and the warnings are
// those of the Result from now on, read for each Schedule as its own page reads it.

import { useQueries, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { PlusIcon, TriangleAlertIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  getListSchedulesQueryKey,
  getScheduleOnCall,
  listSchedules,
} from "../api/gen/endpoints/schedules/schedules";
import type { Schedule, ScheduleOnCall } from "../api/gen/model";
import { problemText } from "../lib/api";
import { stampNear } from "../lib/schedule-time";
import { useCan } from "./app-shell";
import { personLabel } from "./schedule-view";
import { distinctWarnings, warningText } from "./schedule-warnings";
import { buttonVariants } from "./ui/button";

/** How often the section reads the Schedules again, so that it follows the business clock. */
const REFRESH_MS = 30_000;

/** The most Schedules of a Team one read brings, which is the API's largest page. */
const SCHEDULES_LIMIT = 500;

function ScheduleRow({ schedule, data }: { schedule: Schedule; data: ScheduleOnCall | undefined }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const zone = schedule.time_zone;
  const now = data === undefined ? null : new Date(data.from);
  const current =
    data === undefined || now === null
      ? undefined
      : data.segments.find((s) => new Date(s.end) > now);
  const lasts =
    data !== undefined && current !== undefined && new Date(current.end) < new Date(data.to);
  const warnings = data === undefined ? [] : distinctWarnings(data.warnings);
  const who = current?.user == null ? null : personLabel(current.user);
  return (
    <li
      className="flex flex-col gap-1.5 px-3.5 py-3 not-last:border-b"
      data-testid="team-schedule"
      data-schedule={schedule.name}
    >
      <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <Link
          to="/schedules/$scheduleId"
          params={{ scheduleId: schedule.id }}
          className="font-semibold wrap-anywhere text-primary underline-offset-4 hover:underline focus-visible:underline"
        >
          {schedule.name}
        </Link>
        {schedule.main && (
          <span
            className="rounded-md bg-primary/10 px-2 py-0.5 text-xs font-medium text-primary"
            data-testid="schedule-main"
          >
            {t("schedule.main")}
          </span>
        )}
      </span>
      <span className="text-sm text-muted-foreground" data-testid="team-schedule-layers">
        {t("teams.schedules.layers", { count: schedule.rotation_layers.length })}
        {schedule.rotation_layers.length > 0 && (
          <> · {schedule.rotation_layers.map((layer) => layer.name).join(", ")}</>
        )}
      </span>
      {data !== undefined && now !== null && (
        <span className="text-sm" data-testid="team-schedule-on-call">
          {who === null ? t("teams.schedules.nobody") : t("teams.schedules.now", { who })}
          {current !== undefined && lasts && (
            <span className="text-muted-foreground">
              {" · "}
              {t("onCallNow.until", { time: stampNear(current.end, now, zone, locale) })}
            </span>
          )}
        </span>
      )}
      {data !== undefined &&
        (warnings.length === 0 ? (
          <span className="text-sm text-muted-foreground" data-testid="team-schedule-no-warnings">
            {t("teams.schedules.noWarnings")}
          </span>
        ) : (
          <ul className="flex flex-col gap-0.5" data-testid="team-schedule-warnings">
            {warnings.map((w) => (
              <li
                key={`${w.kind}-${w.user?.id}-${w.start}-${w.destination?.id}-${w.reason}-${w.rotation_layer?.id}-${w.override_id}`}
                className="flex items-start gap-2 text-sm"
                data-kind={w.kind}
              >
                <TriangleAlertIcon
                  aria-hidden="true"
                  className={
                    w.kind === "gap"
                      ? "mt-0.5 size-4 shrink-0 text-destructive"
                      : "mt-0.5 size-4 shrink-0 text-muted-foreground"
                  }
                />
                <span className="wrap-anywhere">{warningText(t, w, zone, locale)}</span>
              </li>
            ))}
          </ul>
        ))}
    </li>
  );
}

export function TeamSchedules({ teamId }: { teamId: string }) {
  const { t } = useTranslation();
  const canWrite = useCan("schedules:write");
  const filter = { team: teamId, limit: SCHEDULES_LIMIT };
  const list = useQuery({
    queryKey: getListSchedulesQueryKey(filter),
    queryFn: ({ signal }) => listSchedules(filter, { signal }),
    refetchInterval: REFRESH_MS,
    staleTime: 0,
  });
  const schedules = list.data?.items ?? [];
  const reads = useQueries({
    queries: schedules.map((schedule) => ({
      queryKey: ["schedule-now", schedule.id],
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        getScheduleOnCall(schedule.id, undefined, { signal }),
      refetchInterval: REFRESH_MS,
      staleTime: 0,
    })),
  });
  return (
    <section
      className="flex min-w-0 flex-col gap-3"
      aria-labelledby="team-schedules-title"
      data-testid="team-schedules"
    >
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex min-w-0 flex-col gap-1">
          <h2 id="team-schedules-title" className="text-lg font-semibold">
            {t("teams.schedules.title")}
          </h2>
          <p className="text-sm text-muted-foreground">{t("teams.schedules.hint")}</p>
        </div>
        {canWrite && (
          <Link to="/schedules/new" search={{ team: teamId }} className={buttonVariants()}>
            <PlusIcon aria-hidden="true" />
            {t("schedule.create.start")}
          </Link>
        )}
      </div>
      {list.isError ? (
        <p className="text-sm text-destructive" role="alert">
          {problemText(t, list.error)}
        </p>
      ) : list.data === undefined ? (
        <p className="text-sm text-muted-foreground" role="status">
          {t("common.loading")}
        </p>
      ) : schedules.length === 0 ? (
        <p className="text-sm text-muted-foreground" data-testid="team-schedules-empty">
          {t("teams.schedules.none")}
        </p>
      ) : (
        <ul className="flex flex-col rounded-lg border">
          {schedules.map((schedule, index) => (
            <ScheduleRow key={schedule.id} schedule={schedule} data={reads[index]?.data} />
          ))}
        </ul>
      )}
    </section>
  );
}

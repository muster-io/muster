// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// A Schedule (C-27.FR-1 to FR-8, FR-13; C-27.AC-2, AC-6, AC-9): its header with "main", "On call now" with the
// unreachable marks and what comes next, "Warnings", the two-week view with the row "Result", "Overrides" with the
// Override form, and "Rotation layers" with the details of the Schedule, which only Admins edit (schedules:write). The
// view reads the Result of two weeks from the Monday of the week of now by the business clock, so the development clock
// moves it; "On call now" reads the Result from now on, whose first segment is the current one. The page keeps the
// version of the Schedule it was read at and sends it as If-Match with each save.

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { ArrowLeftIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  deleteSchedule,
  getGetScheduleQueryKey,
  getListSchedulesQueryKey,
  getScheduleOnCall,
  updateSchedule,
  useGetSchedule,
} from "../api/gen/endpoints/schedules/schedules";
import type { OnCallSegment, Schedule } from "../api/gen/model";
import { RequirePermission, useCan } from "../components/app-shell";
import { PageTitle } from "../components/docs-link";
import { OverrideForm } from "../components/override-form";
import { RotationLayersSummary, useTeamPeople } from "../components/rotation-layers-editor";
import { IcalSubscription } from "../components/ical-subscription";
import { ScheduleForm } from "../components/schedule-form";
import { ScheduleOnCallNow } from "../components/schedule-on-call-now";
import { ScheduleOverrides } from "../components/schedule-overrides";
import { ScheduleView } from "../components/schedule-view";
import { ScheduleWarnings } from "../components/schedule-warnings";
import { Alert, AlertDescription } from "../components/ui/alert";
import { Button, buttonVariants } from "../components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../components/ui/dialog";
import { isApiError, problemText, useSession } from "../lib/api";
import { viewRange } from "../lib/schedule-time";

export const Route = createFileRoute("/schedules/$scheduleId")({
  staticData: { shell: true },
  component: SchedulePage,
});

/** How often the page reads the Result again, so that "On call now" follows the business clock. */
const REFRESH_MS = 30_000;

function DeleteSchedule({ schedule }: { schedule: Schedule }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const remove = useMutation({
    mutationFn: () => deleteSchedule(schedule.id, { headers: { "If-Match": schedule.etag } }),
    onSuccess: async () => {
      void queryClient.invalidateQueries({ queryKey: getListSchedulesQueryKey() });
      await navigate({ to: "/schedules", search: { team: schedule.team_id } });
    },
    // A refusal means the Schedule changed meanwhile: read it again, so that the next try sends its version.
    onError: () => {
      void queryClient.invalidateQueries({ queryKey: getGetScheduleQueryKey(schedule.id) });
    },
  });
  return (
    <section className="flex flex-col gap-2">
      <div>
        <Button type="button" variant="destructive" onClick={() => setOpen(true)}>
          {t("schedule.delete.action")}
        </Button>
      </div>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("schedule.delete.title", { name: schedule.name })}</DialogTitle>
            <DialogDescription>{t("schedule.delete.description")}</DialogDescription>
          </DialogHeader>
          {remove.isError && (
            <Alert variant="destructive">
              <AlertDescription className="text-current">
                {isApiError(remove.error) && remove.error.code === "schedule_in_use"
                  ? t("schedule.delete.inUse")
                  : problemText(t, remove.error)}
              </AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <DialogClose render={<Button variant="outline" />}>{t("common.cancel")}</DialogClose>
            <Button
              type="button"
              variant="destructive"
              disabled={remove.isPending}
              onClick={() => remove.mutate()}
            >
              {t("schedule.delete.confirm")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  );
}

function ScheduleBody({ schedule }: { schedule: Schedule }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const session = useSession();
  const canWrite = useCan("schedules:write");
  const canOverride = useCan("schedule-overrides:write");
  const zone = schedule.time_zone;
  const { people, members } = useTeamPeople(schedule.team_id);
  const [weekOffset, setWeekOffset] = useState(0);
  const [adding, setAdding] = useState(false);
  const [picked, setPicked] = useState<OnCallSegment | null>(null);
  const [saved, setSaved] = useState(false);
  const [formVersion, setFormVersion] = useState(0);

  // The Result from now on: its `from` is the business clock's now.
  const current = useQuery({
    queryKey: ["schedule-now", schedule.id],
    queryFn: ({ signal }) => getScheduleOnCall(schedule.id, undefined, { signal }),
    refetchInterval: REFRESH_MS,
    staleTime: 0,
  });
  const now = current.data === undefined ? null : new Date(current.data.from);
  const range = viewRange(now ?? new Date(0), zone, weekOffset);
  const viewParams = { from: range.start.toISOString(), to: range.end.toISOString() };
  const view = useQuery({
    queryKey: ["schedule-view", schedule.id, viewParams],
    queryFn: ({ signal }) => getScheduleOnCall(schedule.id, viewParams, { signal }),
    enabled: now !== null,
    refetchInterval: REFRESH_MS,
    staleTime: 0,
    placeholderData: (previous) => previous,
  });
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ["schedule-now", schedule.id] });
    void queryClient.invalidateQueries({ queryKey: ["schedule-view", schedule.id] });
    void queryClient.invalidateQueries({
      queryKey: [`/api/v1/schedules/${schedule.id}/overrides`],
    });
  };
  const me = session?.user;
  const inTeam = me !== undefined && members.some((m) => m.user.id === me.id);
  const canAdd = canOverride && me !== undefined && (me.role === "admin" || inTeam);
  const close = () => {
    setAdding(false);
    setPicked(null);
  };
  const update = useMutation({
    mutationFn: (input: Parameters<typeof updateSchedule>[1]) =>
      updateSchedule(schedule.id, input, { headers: { "If-Match": schedule.etag } }),
    onSuccess: (updated) => {
      queryClient.setQueryData(getGetScheduleQueryKey(schedule.id), updated);
      void queryClient.invalidateQueries({ queryKey: getListSchedulesQueryKey() });
      refresh();
      setSaved(true);
      setFormVersion((n) => n + 1);
    },
  });
  return (
    <div className="flex min-w-0 flex-col gap-8">
      <div className="flex flex-col gap-2">
        <Link
          to="/schedules"
          search={{ team: schedule.team_id }}
          className={buttonVariants({ variant: "link", className: "w-fit px-0" })}
        >
          <ArrowLeftIcon aria-hidden="true" />
          {t("schedule.list.title")}
        </Link>
        <div className="flex flex-wrap items-center gap-3">
          <PageTitle docs="page.schedule">
            {t("schedule.header", { team: schedule.team.name, name: schedule.name })}
          </PageTitle>
          {schedule.main && (
            <span
              className="rounded-md bg-primary/10 px-2 py-0.5 text-xs font-medium text-primary"
              data-testid="schedule-main"
            >
              {t("schedule.main")}
            </span>
          )}
        </div>
        <p className="text-muted-foreground">
          {t("schedule.hint", { count: schedule.rotation_layers.length })}
        </p>
      </div>
      {current.data !== undefined && <ScheduleOnCallNow data={current.data} zone={zone} />}
      {view.data !== undefined && <ScheduleWarnings warnings={view.data.warnings} zone={zone} />}
      {(current.isError || view.isError) && (
        <p className="text-sm text-destructive" role="alert" data-testid="schedule-read-error">
          {problemText(t, current.error ?? view.error)}
        </p>
      )}
      {view.data !== undefined && (
        <ScheduleView
          zone={zone}
          data={view.data}
          range={range}
          now={now}
          onPrevious={() => setWeekOffset((n) => n - 1)}
          onToday={() => setWeekOffset(0)}
          onNext={() => setWeekOffset((n) => n + 1)}
          loading={view.isPlaceholderData}
          {...(canAdd && !view.isPlaceholderData
            ? {
                onPick: (segment: OnCallSegment) => {
                  setPicked(segment);
                  setAdding(true);
                },
              }
            : {})}
          picked={picked}
        />
      )}
      {now !== null && (
        <ScheduleOverrides
          scheduleId={schedule.id}
          zone={zone}
          now={now}
          canAdd={canAdd}
          adding={adding}
          onAdd={() => setAdding(true)}
          people={people}
          refresh={refresh}
        >
          {adding && me !== undefined && (
            <OverrideForm
              scheduleId={schedule.id}
              zone={zone}
              now={now}
              me={{ id: me.id, name: me.name, login: me.login, deactivated: false }}
              people={people}
              segment={picked}
              onCancel={close}
              onSaved={() => {
                close();
                refresh();
              }}
            />
          )}
        </ScheduleOverrides>
      )}
      <IcalSubscription />
      {canWrite ? (
        <ScheduleForm
          key={formVersion}
          schedule={schedule}
          submitLabel={t("common.save")}
          save={(input) => update.mutateAsync(input)}
          saved={saved}
          onReload={async () => {
            // The form starts again from the version that has been read, not from the one it was opened at.
            try {
              await queryClient.refetchQueries({ queryKey: getGetScheduleQueryKey(schedule.id) });
            } finally {
              setFormVersion((n) => n + 1);
            }
          }}
        />
      ) : (
        <section className="flex flex-col gap-3" aria-labelledby="schedule-layers-title">
          <h2 id="schedule-layers-title" className="text-lg font-semibold">
            {t("schedule.layers.title")}
          </h2>
          <RotationLayersSummary layers={schedule.rotation_layers} />
        </section>
      )}
      {canWrite && <DeleteSchedule schedule={schedule} />}
    </div>
  );
}

function ScheduleContent() {
  const { t } = useTranslation();
  const { scheduleId } = Route.useParams();
  const schedule = useGetSchedule(scheduleId);
  if (schedule.isError) {
    return (
      <p className="text-destructive" role="alert">
        {isApiError(schedule.error) && schedule.error.status === 404
          ? t("schedule.notFound")
          : problemText(t, schedule.error)}
      </p>
    );
  }
  if (schedule.data === undefined) {
    return <p className="text-muted-foreground">{t("common.loading")}</p>;
  }
  return <ScheduleBody schedule={schedule.data} />;
}

function SchedulePage() {
  return (
    <RequirePermission permission="schedules:read">
      <ScheduleContent />
    </RequirePermission>
  );
}

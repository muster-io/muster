// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Overrides" of a Schedule (C-27.FR-3, FR-6): the Overrides that have not ended, each with who gives what to whom
// ("@olga → @petr", the person whose Shift it takes when it names one), when, and who made it; "Any Responder of the
// Team; no messages." says who may and that nobody is told. Change and delete are for a Responder of the Team and an
// Admin (`schedule-overrides:write`); "Add Override" opens the form, which works on a segment of the row "Result".

import { useMutation, useQuery } from "@tanstack/react-query";
import { PlusIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  deleteScheduleOverride,
  getListScheduleOverridesQueryKey,
  listScheduleOverrides,
} from "../api/gen/endpoints/schedules/schedules";
import type { ScheduleOverride, UserRef } from "../api/gen/model";
import { isApiError, problemText } from "../lib/api";
import { span } from "../lib/schedule-time";
import { DocsLink } from "./docs-link";
import { OverrideEditForm } from "./override-form";
import { personLabel } from "./schedule-view";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "./ui/dialog";

/** The Overrides a page shows at most. */
const LIMIT = 100;

function OverrideRow({
  scheduleId,
  override,
  zone,
  now,
  canChange,
  people,
  refresh,
}: {
  scheduleId: string;
  override: ScheduleOverride;
  zone: string;
  now: Date;
  canChange: boolean;
  people: readonly UserRef[];
  refresh: () => void;
}) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const [editing, setEditing] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const remove = useMutation({
    mutationFn: () =>
      deleteScheduleOverride(scheduleId, override.id, { headers: { "If-Match": override.etag } }),
    onSuccess: () => {
      setConfirming(false);
      refresh();
    },
    // A refusal means the Override is not as this page read it: read the Overrides and the Result again.
    onError: refresh,
  });
  const who =
    override.replaces_user === null
      ? personLabel(override.user)
      : `${personLabel(override.replaces_user)} → ${personLabel(override.user)}`;
  if (editing) {
    return (
      <li data-testid="schedule-override">
        <OverrideEditForm
          scheduleId={scheduleId}
          zone={zone}
          now={now}
          override={override}
          people={people}
          onCancel={() => setEditing(false)}
          onSaved={() => {
            setEditing(false);
            refresh();
          }}
          onReload={() => {
            setEditing(false);
            refresh();
          }}
        />
      </li>
    );
  }
  return (
    <li
      className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 rounded-lg border p-3 text-sm"
      data-testid="schedule-override"
    >
      <div className="flex min-w-0 flex-col gap-0.5">
        <span className="font-medium wrap-anywhere" data-testid="schedule-override-who">
          {who}
        </span>
        <span className="text-muted-foreground" data-testid="schedule-override-when">
          {span(override.start, override.end, zone, locale)}
        </span>
        <span className="text-muted-foreground" data-testid="schedule-override-by">
          {t("schedule.overrides.madeBy", { who: personLabel(override.created_by) })}
        </span>
      </div>
      {canChange && (
        <div className="flex items-center gap-2">
          <Button type="button" variant="outline" size="sm" onClick={() => setEditing(true)}>
            {t("schedule.overrides.change")}
          </Button>
          <Button type="button" variant="outline" size="sm" onClick={() => setConfirming(true)}>
            {t("schedule.overrides.delete")}
          </Button>
        </div>
      )}
      <Dialog open={confirming} onOpenChange={setConfirming}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("schedule.overrides.deleteTitle")}</DialogTitle>
            <DialogDescription>
              {new Date(override.start) <= now
                ? t("schedule.overrides.deleteBegun")
                : t("schedule.overrides.deleteDescription")}
            </DialogDescription>
          </DialogHeader>
          {remove.isError && (
            <Alert variant="destructive">
              <AlertDescription className="text-current">
                {isApiError(remove.error) && remove.error.code === "override_ended"
                  ? t("schedule.override.ended")
                  : problemText(t, remove.error)}
              </AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <DialogClose render={<Button type="button" variant="outline" />}>
              {t("common.cancel")}
            </DialogClose>
            <Button
              type="button"
              variant="destructive"
              disabled={remove.isPending}
              onClick={() => remove.mutate()}
            >
              {t("schedule.overrides.deleteConfirm")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </li>
  );
}

export function ScheduleOverrides({
  scheduleId,
  zone,
  now,
  canAdd,
  adding,
  onAdd,
  people,
  refresh,
  children,
}: {
  scheduleId: string;
  zone: string;
  now: Date;
  /** Whether the caller may make and change Overrides: `schedule-overrides:write`, in the Team or an Admin. */
  canAdd: boolean;
  /** The form is open. */
  adding: boolean;
  onAdd: () => void;
  people: readonly UserRef[];
  /** Reads the Result and the Overrides again. */
  refresh: () => void;
  /** The Override form, below the list while it is open. */
  children?: React.ReactNode;
}) {
  const { t } = useTranslation();
  const params = { limit: LIMIT };
  const list = useQuery({
    queryKey: getListScheduleOverridesQueryKey(scheduleId, params),
    queryFn: ({ signal }) => listScheduleOverrides(scheduleId, params, { signal }),
  });
  return (
    <section className="flex min-w-0 flex-col gap-3" aria-labelledby="schedule-overrides-title">
      <div className="flex flex-wrap items-baseline justify-between gap-3">
        <div className="flex min-w-0 flex-col gap-1">
          <div className="flex flex-wrap items-baseline gap-x-3">
            <h2 id="schedule-overrides-title" className="text-lg font-semibold">
              {t("schedule.overrides.title")}
            </h2>
            <DocsLink
              to="field.overrides"
              kind="learn_more"
              about={t("schedule.overrides.title")}
            />
          </div>
          <p className="text-sm text-muted-foreground">{t("schedule.overrides.hint")}</p>
        </div>
        {canAdd && !adding && (
          <Button type="button" onClick={onAdd}>
            <PlusIcon aria-hidden="true" />
            {t("schedule.overrides.add")}
          </Button>
        )}
      </div>
      {children}
      {list.isError ? (
        <p className="text-sm text-destructive" role="alert">
          {problemText(t, list.error)}
        </p>
      ) : (list.data?.items.length ?? 0) === 0 ? (
        <p className="text-sm text-muted-foreground" data-testid="schedule-no-overrides">
          {t("schedule.overrides.none")}
        </p>
      ) : (
        <ul className="flex flex-col gap-2" data-testid="schedule-overrides">
          {list.data?.items.map((override) => (
            <OverrideRow
              key={override.id}
              scheduleId={scheduleId}
              override={override}
              zone={zone}
              now={now}
              canChange={canAdd}
              people={people}
              refresh={refresh}
            />
          ))}
        </ul>
      )}
    </section>
  );
}

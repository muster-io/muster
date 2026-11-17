// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The details and the Rotation layers of a Schedule (C-27.FR-1, FR-2, FR-7, FR-13): its Team on creation, name, time
// zone, whether it is the Team's main Schedule, the gap alert — "By default", "On" or "Off" — and the Rotation layers.
// One form creates a Schedule and edits one; a refusal of the server shows at the field it names, and a person refused
// as `not_pageable` at the person. Only Admins (schedules:write) see it.

import { useMutation } from "@tanstack/react-query";
import { useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import type { Schedule, ScheduleGapAlert, ScheduleInput, UserRef } from "../api/gen/model";
import { fieldErrorText, isApiError, isStale, problemText } from "../lib/api";
import { dayIn, effectiveTimeZone } from "../lib/time";
import { DocsLink } from "./docs-link";
import {
  type LayerIssues,
  type LayerValues,
  RotationLayersEditor,
  checkLayers,
  layerIssuesOf,
  layerValues,
  layersInput,
  newLayer,
  useTeamPeople,
} from "./rotation-layers-editor";
import { useTeamList } from "./team-filter";
import { TimeZonePicker } from "./time-zone-picker";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

const NAME_MAX = 200;

type DetailField = "team_id" | "name" | "time_zone";

export interface ScheduleFormProps {
  /** The Schedule of an edit; none creates one. */
  schedule?: Schedule;
  /** The Team a new Schedule starts with. */
  initialTeam?: string;
  submitLabel: string;
  /** Sends the Schedule; a refusal is shown by the form. */
  save: (input: ScheduleInput) => Promise<unknown>;
  onCancel?: () => void;
  /** Shown beside Save once a save has gone through. */
  saved?: boolean;
  /** Reads the Schedule again, which a change another person made meanwhile asks for. */
  onReload?: () => void | Promise<void>;
}

function initialLayers(
  t: ReturnType<typeof useTranslation>["t"],
  schedule: Schedule | undefined,
  today: string,
) {
  if (schedule !== undefined) {
    return schedule.rotation_layers.toReversed().map(layerValues);
  }
  return [newLayer(t("schedule.layers.defaultName", { number: 1 }), today)];
}

export function ScheduleForm({
  schedule,
  initialTeam,
  submitLabel,
  save,
  onCancel,
  saved = false,
  onReload,
}: ScheduleFormProps) {
  const editing = schedule !== undefined;
  const { t } = useTranslation();
  const id = useId();
  const formElement = useRef<HTMLFormElement>(null);
  const zoneOfNow = effectiveTimeZone(null);
  const [teamId, setTeamId] = useState(schedule?.team_id ?? initialTeam ?? "");
  const [name, setName] = useState(schedule?.name ?? "");
  const [timeZone, setTimeZone] = useState(schedule?.time_zone ?? zoneOfNow);
  const [main, setMain] = useState(schedule?.main ?? false);
  const [gapAlert, setGapAlert] = useState<ScheduleGapAlert>(schedule?.gap_alert ?? "by_default");
  const [opened] = useState(() => new Date());
  const today = dayIn(opened, timeZone);
  const [layers, setLayers] = useState<LayerValues[]>(() => initialLayers(t, schedule, today));
  const [details, setDetails] = useState<Partial<Record<DetailField, string>>>({});
  const [issues, setIssues] = useState<LayerIssues>({});
  // The codes of the refusals the form has no field for, which it shows as a whole.
  const [unplaced, setUnplaced] = useState<string[]>([]);
  const [dirty, setDirty] = useState(false);
  const teams = useTeamList();
  const { people: teamPeople } = useTeamPeople(teamId);
  const known = new Map<string, UserRef>();
  for (const l of schedule?.rotation_layers ?? []) {
    for (const u of l.users) {
      known.set(u.id, u);
    }
  }
  for (const u of teamPeople) {
    known.set(u.id, u);
  }
  const submit = useMutation({
    mutationFn: save,
    onSuccess: () => setDirty(false),
    onError: (err) => {
      if (!isApiError(err)) {
        return;
      }
      const errors = err.errors ?? [];
      const next: Partial<Record<DetailField, string>> = {};
      const loose: string[] = [];
      if (err.status === 409 && errors.length === 0 && err.code === "name_taken") {
        next.name = "name_taken";
      }
      for (const e of errors.filter((x) => !x.pointer.startsWith("/rotation_layers"))) {
        const field = e.pointer.slice(1);
        if (field === "name" || field === "time_zone" || (field === "team_id" && !editing)) {
          next[field] = e.code;
        } else {
          loose.push(e.code);
        }
      }
      const { issues: refused, rest } = layerIssuesOf(
        errors.filter((x) => x.pointer.startsWith("/rotation_layers")),
        layers.length,
      );
      setDetails(next);
      setIssues(refused);
      setUnplaced([...loose, ...rest]);
      requestAnimationFrame(() =>
        formElement.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus(),
      );
    },
  });
  const change =
    <T,>(set: (value: T) => void) =>
    (value: T) => {
      set(value);
      setDirty(true);
    };
  const detailText = (field: DetailField): string | undefined => {
    const code = details[field];
    if (code === undefined) {
      return undefined;
    }
    return code === "name_taken" ? t("schedule.form.nameTaken") : fieldErrorText(t, code);
  };
  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    setUnplaced([]);
    const next: Partial<Record<DetailField, string>> = {};
    if (teamId === "") {
      next.team_id = "required";
    }
    if (name.trim() === "") {
      next.name = "required";
    } else if (name.length > NAME_MAX) {
      next.name = "too_long";
    }
    const found = checkLayers(layers);
    setDetails(next);
    setIssues(found);
    if (Object.keys(next).length > 0 || Object.keys(found).length > 0) {
      requestAnimationFrame(() =>
        formElement.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus(),
      );
      return;
    }
    submit.mutate({
      team_id: teamId,
      name: name.trim(),
      time_zone: timeZone,
      main,
      gap_alert: gapAlert,
      rotation_layers: layersInput(layers),
    });
  };
  const shownByField =
    isApiError(submit.error) &&
    ((submit.error.errors?.length ?? 0) > 0 || submit.error.code === "name_taken") &&
    !isStale(submit.error) &&
    unplaced.length === 0;
  return (
    <form
      ref={formElement}
      noValidate
      className="flex min-w-0 flex-col gap-6"
      onSubmit={onSubmit}
      data-testid="schedule-form"
    >
      <section className="flex min-w-0 flex-col gap-4" aria-labelledby={`${id}-details`}>
        <h2 id={`${id}-details`} className="text-lg font-semibold">
          {t("schedule.form.details")}
        </h2>
        {schedule === undefined && (
          <div className="flex min-w-0 flex-col gap-1.5">
            <Label htmlFor={`${id}-team`}>{t("schedule.form.team")}</Label>
            <NativeSelect
              id={`${id}-team`}
              className="w-full sm:w-64"
              value={teamId}
              aria-invalid={details.team_id !== undefined}
              aria-describedby={details.team_id === undefined ? undefined : `${id}-team-error`}
              onChange={(e) => {
                setTeamId(e.target.value);
                setLayers((current) => current.map((l) => ({ ...l, userIds: [] })));
                setDirty(true);
              }}
            >
              <NativeSelectOption value="">{t("schedule.form.chooseTeam")}</NativeSelectOption>
              {(teams.data ?? []).map((team) => (
                <NativeSelectOption key={team.id} value={team.id}>
                  {team.name}
                </NativeSelectOption>
              ))}
            </NativeSelect>
            {details.team_id !== undefined && (
              <p id={`${id}-team-error`} className="text-sm text-destructive">
                {detailText("team_id")}
              </p>
            )}
          </div>
        )}
        <div className="flex max-w-md min-w-0 flex-col gap-1.5">
          <Label htmlFor={`${id}-name`}>{t("schedule.form.name")}</Label>
          <Input
            id={`${id}-name`}
            autoComplete="off"
            maxLength={NAME_MAX}
            value={name}
            aria-invalid={details.name !== undefined}
            aria-describedby={details.name === undefined ? undefined : `${id}-name-error`}
            onChange={(e) => change(setName)(e.target.value)}
          />
          {details.name !== undefined && (
            <p id={`${id}-name-error`} className="text-sm text-destructive">
              {detailText("name")}
            </p>
          )}
        </div>
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor={`${id}-zone`}>{t("schedule.form.timeZone")}</Label>
          <TimeZonePicker
            id={`${id}-zone`}
            value={timeZone}
            onChange={change(setTimeZone)}
            invalid={details.time_zone !== undefined}
          />
          <p className="text-sm text-muted-foreground">{t("schedule.form.timeZoneHint")}</p>
          {details.time_zone !== undefined && (
            <p className="text-sm text-destructive">{detailText("time_zone")}</p>
          )}
        </div>
        <div className="flex max-w-2xl min-w-0 items-start gap-2">
          <input
            id={`${id}-main`}
            type="checkbox"
            className="mt-0.5 size-4 shrink-0 accent-primary focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
            checked={main}
            disabled={schedule?.main === true}
            aria-describedby={`${id}-main-hint`}
            onChange={(e) => change(setMain)(e.target.checked)}
          />
          <div className="flex min-w-0 flex-col gap-0.5">
            <Label htmlFor={`${id}-main`} className="leading-snug">
              {t("schedule.form.main")}
            </Label>
            <p id={`${id}-main-hint`} className="text-xs text-muted-foreground">
              {schedule?.main === true ? t("schedule.form.mainIs") : t("schedule.form.mainHint")}
            </p>
          </div>
        </div>
        <div className="flex min-w-0 flex-col gap-1.5">
          <div className="flex flex-wrap items-baseline gap-x-3">
            <Label htmlFor={`${id}-gap`}>{t("schedule.form.gapAlert")}</Label>
            <DocsLink to="field.gap_alert" kind="learn_more" about={t("schedule.form.gapAlert")} />
          </div>
          <NativeSelect
            id={`${id}-gap`}
            className="w-full sm:w-96"
            value={gapAlert}
            aria-describedby={`${id}-gap-hint`}
            onChange={(e) => {
              const next = (["by_default", "on", "off"] as const).find((v) => v === e.target.value);
              if (next !== undefined) {
                change(setGapAlert)(next);
              }
            }}
          >
            <NativeSelectOption value="by_default">
              {t("schedule.form.gapByDefault")}
            </NativeSelectOption>
            <NativeSelectOption value="on">{t("schedule.form.gapOn")}</NativeSelectOption>
            <NativeSelectOption value="off">{t("schedule.form.gapOff")}</NativeSelectOption>
          </NativeSelect>
          <p id={`${id}-gap-hint`} className="text-sm text-muted-foreground">
            {t("schedule.form.gapHint")}
          </p>
        </div>
      </section>
      <section className="flex min-w-0 flex-col gap-3" aria-labelledby={`${id}-layers`}>
        <h2 id={`${id}-layers`} className="text-lg font-semibold">
          {t("schedule.layers.title")}
        </h2>
        <RotationLayersEditor
          layers={layers}
          onChange={(next) => {
            setLayers(next);
            setDirty(true);
            // What the server said about a person or a layer belongs to the list it saw, not to this one.
            setIssues({});
          }}
          people={[...known.values()]}
          team={teamPeople}
          issues={issues}
          today={today}
        />
      </section>
      <div className="flex flex-col gap-3">
        {submit.isError && !shownByField && (
          <Alert variant="destructive">
            <AlertDescription className="text-current">
              {isStale(submit.error)
                ? t("schedule.form.stale")
                : unplaced[0] !== undefined
                  ? fieldErrorText(t, unplaced[0])
                  : isApiError(submit.error) && submit.error.status === 409
                    ? t("schedule.form.conflict")
                    : problemText(t, submit.error)}
            </AlertDescription>
            {isStale(submit.error) && onReload !== undefined && (
              <Button type="button" variant="outline" size="sm" onClick={() => void onReload()}>
                {t("common.reload")}
              </Button>
            )}
          </Alert>
        )}
        <div className="flex flex-wrap items-center gap-3">
          <Button type="submit" disabled={submit.isPending}>
            {submitLabel}
          </Button>
          {onCancel !== undefined && (
            <Button type="button" variant="outline" onClick={onCancel}>
              {t("common.cancel")}
            </Button>
          )}
          <span
            role="status"
            className="text-sm text-muted-foreground"
            data-testid="schedule-status"
          >
            {saved && !dirty ? t("common.saved") : ""}
          </span>
        </div>
      </div>
    </form>
  );
}

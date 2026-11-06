// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Organization → General (C-20.FR-1, FR-9): the name of the Organization, the time zone of messages, the Team label
// (C-25.FR-5) whose value names a Team, the start of the
// working day and the first day of the week at which the Snooze presets end, and the Organization's Working hours —
// the working days and the end of the working day beside its start — which every Team without its own follows
// (C-25.FR-9). Times go to the API as entered; a value it refuses, such as an end not later than the start, shows at
// its field.

import { zodResolver } from "@hookform/resolvers/zod";
import { createFileRoute } from "@tanstack/react-router";
import { Controller } from "react-hook-form";
import { useTranslation } from "react-i18next";

import { z } from "zod";

import { type Organization, Weekday } from "../api/gen/model";
import { DocsLink } from "../components/docs-link";
import {
  Field,
  errorText,
  OrganizationPage,
  SaveBar,
  useOrganizationForm,
} from "../components/organization-form";
import { TimeZonePicker } from "../components/time-zone-picker";
import { Card, CardContent } from "../components/ui/card";
import { Input } from "../components/ui/input";
import { NativeSelect, NativeSelectOption } from "../components/ui/native-select";
import {
  WEEKDAYS,
  WeekdayChecklist,
  hoursErrorText,
  weekdayName,
} from "../components/working-hours-editor";

export const Route = createFileRoute("/admin/organization/general")({
  staticData: { shell: true },
  component: GeneralPage,
});

/** The format of `organization.team_label`, a label name. */
const LABEL_NAME = /^[a-zA-Z_][a-zA-Z0-9_]*$/;

const schema = z.object({
  name: z.string().trim().min(1, "required"),
  time_zone: z.string().min(1, "required"),
  // The API takes a label name, as Prometheus does; it also refuses the labels that an Internal alert sets itself.
  team_label: z.string().trim().min(1, "required").regex(LABEL_NAME, "invalid_format"),
  // The API checks the start of the working day, HH:MM on the 24-hour clock.
  workday_start: z.string(),
  workday_end: z.string(),
  working_days: z.array(z.enum(Weekday)).min(1, "required"),
  week_start: z.enum(Weekday),
});
type GeneralValues = z.infer<typeof schema>;

const WORKING_WEEK_HINT = "organization-working-week-hint";

/** The ids that describe a control: its field's own hint and error, and the hint the two working-week fields share. */
function describedBy(own: string | undefined): string {
  return own === undefined ? WORKING_WEEK_HINT : `${own} ${WORKING_WEEK_HINT}`;
}

function GeneralForm({ organization }: { organization: Organization }) {
  const { t } = useTranslation();
  const { form, formRef, onSubmit, state } = useOrganizationForm<GeneralValues>({
    organization,
    values: (org) => ({
      name: org.name,
      time_zone: org.time_zone,
      team_label: org.team_label,
      workday_start: org.workday_start,
      workday_end: org.workday_end,
      working_days: [...org.working_days],
      week_start: org.week_start,
    }),
    patch: (v) => ({
      name: v.name.trim(),
      time_zone: v.time_zone,
      team_label: v.team_label.trim(),
      workday_start: v.workday_start,
      workday_end: v.workday_end,
      working_days: v.working_days,
      week_start: v.week_start,
    }),
    fields: () => [
      "name",
      "time_zone",
      "team_label",
      "workday_start",
      "workday_end",
      "working_days",
      "week_start",
    ],
    resolver: zodResolver(schema),
  });
  const { errors } = form.formState;
  const teamLabelError = (error: { message?: string } | undefined) => {
    if (error === undefined) {
      return undefined;
    }
    return error.message === "reserved_label"
      ? t("organization.general.teamLabelReserved")
      : errorText(t, error);
  };
  const hoursError = (error: { message?: string } | undefined) =>
    error === undefined ? undefined : hoursErrorText(t, error.message ?? "");
  return (
    <form ref={formRef} noValidate className="flex flex-col gap-6" onSubmit={onSubmit}>
      <Card>
        <CardContent className="flex flex-col gap-4">
          <Field
            id="organization-name"
            label={t("organization.general.name")}
            error={errorText(t, errors.name)}
          >
            {(control) => <Input {...control} autoComplete="off" {...form.register("name")} />}
          </Field>
          <Field
            id="organization-time-zone"
            label={t("organization.general.timeZone")}
            hint={t("organization.general.timeZoneHint")}
            error={errorText(t, errors.time_zone)}
          >
            {(control) => (
              <Controller
                control={form.control}
                name="time_zone"
                render={({ field }) => (
                  <TimeZonePicker
                    id={control.id}
                    value={field.value}
                    invalid={control["aria-invalid"]}
                    describedBy={control["aria-describedby"]}
                    onChange={field.onChange}
                  />
                )}
              />
            )}
          </Field>
          <Field
            id="organization-team-label"
            label={t("organization.general.teamLabel")}
            hint={
              <>
                {t("organization.general.teamLabelHint")}{" "}
                <DocsLink
                  to="field.team_label"
                  kind="learn_more"
                  about={t("docs.fields.teamLabel")}
                />
              </>
            }
            error={teamLabelError(errors.team_label)}
          >
            {(control) => (
              <Input
                {...control}
                autoComplete="off"
                spellCheck={false}
                className="font-mono sm:max-w-xs"
                {...form.register("team_label")}
              />
            )}
          </Field>
          <div className="flex min-w-0 flex-col gap-1.5">
            <div className="grid min-w-0 gap-4 sm:grid-cols-2">
              <Field
                id="organization-workday-start"
                label={t("organization.general.workdayStart")}
                error={hoursError(errors.workday_start)}
              >
                {(control) => (
                  <Input
                    {...control}
                    type="time"
                    className="min-w-0"
                    aria-describedby={describedBy(control["aria-describedby"])}
                    {...form.register("workday_start")}
                  />
                )}
              </Field>
              <Field
                id="organization-week-start"
                label={t("organization.general.weekStart")}
                error={errorText(t, errors.week_start)}
              >
                {(control) => (
                  <NativeSelect
                    {...control}
                    className="w-full"
                    aria-describedby={describedBy(control["aria-describedby"])}
                    {...form.register("week_start")}
                  >
                    {WEEKDAYS.map((day) => (
                      <NativeSelectOption key={day} value={day}>
                        {weekdayName(t, day)}
                      </NativeSelectOption>
                    ))}
                  </NativeSelect>
                )}
              </Field>
            </div>
            <p id={WORKING_WEEK_HINT} className="text-sm text-muted-foreground">
              {t("organization.general.workingWeekHint")}
            </p>
          </div>
          <div className="flex min-w-0 flex-col gap-4">
            <div className="grid min-w-0 gap-4 sm:grid-cols-2">
              <Field
                id="organization-workday-end"
                label={t("organization.general.workdayEnd")}
                hint={t("organization.general.workingHoursHint")}
                error={hoursError(errors.workday_end)}
              >
                {(control) => (
                  <Input
                    {...control}
                    type="time"
                    className="min-w-0"
                    {...form.register("workday_end")}
                  />
                )}
              </Field>
            </div>
            <Controller
              control={form.control}
              name="working_days"
              render={({ field }) => (
                <WeekdayChecklist
                  id="organization-working-days"
                  legend={t("organization.general.workingDays")}
                  value={field.value}
                  error={errorText(t, errors.working_days)}
                  onChange={field.onChange}
                />
              )}
            />
          </div>
        </CardContent>
      </Card>
      <SaveBar state={state} testId="general-status" />
    </form>
  );
}

function GeneralPage() {
  const { t } = useTranslation();
  return (
    <OrganizationPage title={t("organization.nav.general")}>
      {(organization) => <GeneralForm organization={organization} />}
    </OrganizationPage>
  );
}

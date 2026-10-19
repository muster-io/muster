// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Organization → General (C-20.FR-1, FR-9): the name of the Organization, the time zone of messages, and the start of
// the working day and the first day of the week at which the Snooze presets end. The start of the working day goes to
// the API as entered; a value it refuses shows at its field.

import { zodResolver } from "@hookform/resolvers/zod";
import { createFileRoute } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { Controller } from "react-hook-form";
import { useTranslation } from "react-i18next";

import { z } from "zod";

import { type Organization, Weekday } from "../api/gen/model";
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

export const Route = createFileRoute("/admin/organization/general")({
  staticData: { shell: true },
  component: GeneralPage,
});

const schema = z.object({
  name: z.string().trim().min(1, "required"),
  time_zone: z.string().min(1, "required"),
  // The API checks the start of the working day, HH:MM on the 24-hour clock.
  workday_start: z.string(),
  week_start: z.enum(Weekday),
});
type GeneralValues = z.infer<typeof schema>;

/** The days of the week from Monday, as the select of the first day of the week lists them. */
const WEEKDAYS = Object.values(Weekday);

/** The name of a day of the week. */
function weekdayName(t: TFunction, day: Weekday): string {
  switch (day) {
    case "monday":
      return t("weekdays.monday");
    case "tuesday":
      return t("weekdays.tuesday");
    case "wednesday":
      return t("weekdays.wednesday");
    case "thursday":
      return t("weekdays.thursday");
    case "friday":
      return t("weekdays.friday");
    case "saturday":
      return t("weekdays.saturday");
    default:
      return t("weekdays.sunday");
  }
}

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
      workday_start: org.workday_start,
      week_start: org.week_start,
    }),
    patch: (v) => ({
      name: v.name.trim(),
      time_zone: v.time_zone,
      workday_start: v.workday_start,
      week_start: v.week_start,
    }),
    fields: () => ["name", "time_zone", "workday_start", "week_start"],
    resolver: zodResolver(schema),
  });
  const { errors } = form.formState;
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
          <div className="flex min-w-0 flex-col gap-1.5">
            <div className="grid min-w-0 gap-4 sm:grid-cols-2">
              <Field
                id="organization-workday-start"
                label={t("organization.general.workdayStart")}
                error={errorText(t, errors.workday_start)}
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

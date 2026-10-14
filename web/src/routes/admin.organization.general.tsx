// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Organization → General (C-20.FR-1): the name of the Organization and the time zone of messages.

import { zodResolver } from "@hookform/resolvers/zod";
import { createFileRoute } from "@tanstack/react-router";
import { Controller } from "react-hook-form";
import { useTranslation } from "react-i18next";

import { z } from "zod";

import type { Organization } from "../api/gen/model";
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

export const Route = createFileRoute("/admin/organization/general")({
  staticData: { shell: true },
  component: GeneralPage,
});

const schema = z.object({
  name: z.string().trim().min(1, "required"),
  time_zone: z.string().min(1, "required"),
});
type GeneralValues = z.infer<typeof schema>;

function GeneralForm({ organization }: { organization: Organization }) {
  const { t } = useTranslation();
  const { form, formRef, onSubmit, state } = useOrganizationForm<GeneralValues>({
    organization,
    values: (org) => ({ name: org.name, time_zone: org.time_zone }),
    patch: (v) => ({ name: v.name.trim(), time_zone: v.time_zone }),
    fields: () => ["name", "time_zone"],
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

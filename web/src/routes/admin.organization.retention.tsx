// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Organization → Retention (C-20.FR-1): how many days Muster keeps Stored Snapshots, alert details, Alert Group
// summaries and the Audit log. The server keeps the summaries at least as long as the details and says so at the
// summaries field.

import { zodResolver } from "@hookform/resolvers/zod";
import { createFileRoute } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import type { Organization, RetentionSettings } from "../api/gen/model";
import {
  Field,
  OrganizationPage,
  SaveBar,
  errorText,
  useOrganizationForm,
} from "../components/organization-form";
import { Card, CardContent } from "../components/ui/card";
import { Input } from "../components/ui/input";

export const Route = createFileRoute("/admin/organization/retention")({
  staticData: { shell: true },
  component: RetentionPage,
});

const MAX_DAYS = 36500;

const days = z
  .number({ error: "required" })
  .int("invalid_format")
  .min(1, "out_of_range")
  .max(MAX_DAYS, "out_of_range");
const schema = z.object({
  retention: z.object({
    stored_snapshots_days: days,
    alert_details_days: days,
    alert_group_summaries_days: days,
    audit_log_days: days,
  }),
});
type RetentionValues = z.infer<typeof schema>;

const PERIODS = [
  "stored_snapshots_days",
  "alert_details_days",
  "alert_group_summaries_days",
  "audit_log_days",
] as const satisfies readonly (keyof RetentionSettings)[];

function RetentionForm({ organization }: { organization: Organization }) {
  const { t } = useTranslation();
  const { form, formRef, onSubmit, state } = useOrganizationForm<RetentionValues>({
    organization,
    values: (org) => ({ retention: { ...org.retention } }),
    patch: (v) => ({ retention: v.retention }),
    fields: () => PERIODS.map((p) => `retention.${p}` as const),
    resolver: zodResolver(schema),
  });
  const { errors } = form.formState;
  const label = (period: (typeof PERIODS)[number]) => {
    switch (period) {
      case "stored_snapshots_days":
        return t("organization.retention.storedSnapshots");
      case "alert_details_days":
        return t("organization.retention.alertDetails");
      case "alert_group_summaries_days":
        return t("organization.retention.summaries");
      default:
        return t("organization.retention.auditLog");
    }
  };
  const hint = (period: (typeof PERIODS)[number]) => {
    switch (period) {
      case "stored_snapshots_days":
        return t("organization.retention.storedSnapshotsHint");
      case "alert_details_days":
        return t("organization.retention.alertDetailsHint");
      case "alert_group_summaries_days":
        return t("organization.retention.summariesHint");
      default:
        return t("organization.retention.auditLogHint");
    }
  };
  return (
    <form ref={formRef} noValidate className="flex flex-col gap-6" onSubmit={onSubmit}>
      <Card>
        <CardContent className="flex flex-col gap-4">
          {PERIODS.map((period) => (
            <Field
              key={period}
              id={`retention-${period}`}
              label={label(period)}
              hint={hint(period)}
              error={errorText(t, errors.retention?.[period])}
            >
              {(control) => (
                <Input
                  {...control}
                  type="number"
                  inputMode="numeric"
                  min={1}
                  max={MAX_DAYS}
                  step={1}
                  className="max-w-40"
                  {...form.register(`retention.${period}`, { valueAsNumber: true })}
                />
              )}
            </Field>
          ))}
        </CardContent>
      </Card>
      <SaveBar state={state} testId="retention-status" />
    </form>
  );
}

function RetentionPage() {
  const { t } = useTranslation();
  return (
    <OrganizationPage title={t("organization.nav.retention")}>
      {(organization) => <RetentionForm organization={organization} />}
    </OrganizationPage>
  );
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Organization → Alert handling (C-20.FR-1): whether a critical Severity level makes an Alert Group Urgent, and the
// Instance labels, whose change alone makes a new Alert a Replacement (C-09.FR-7).

import { zodResolver } from "@hookform/resolvers/zod";
import { createFileRoute } from "@tanstack/react-router";
import { Controller, useWatch } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import type { Organization } from "../api/gen/model";
import { LABEL_NAME } from "../components/labels-editor";
import { NetworkListEditor } from "../components/network-list-editor";
import {
  OrganizationPage,
  SaveBar,
  errorText,
  useOrganizationForm,
} from "../components/organization-form";
import { Card, CardContent } from "../components/ui/card";
import { Label } from "../components/ui/label";

export const Route = createFileRoute("/admin/organization/alert-handling")({
  staticData: { shell: true },
  component: AlertHandlingPage,
});

const schema = z
  .object({ critical_is_urgent: z.boolean(), instance_labels: z.array(z.string()) })
  .superRefine((v, ctx) => {
    const seen = new Set<string>();
    v.instance_labels.forEach((raw, index) => {
      const name = raw.trim();
      const path = ["instance_labels", index];
      if (name === "") {
        ctx.addIssue({ code: "custom", path, message: "required" });
      } else if (!LABEL_NAME.test(name)) {
        ctx.addIssue({ code: "custom", path, message: "invalid_format" });
      } else if (seen.has(name)) {
        ctx.addIssue({ code: "custom", path, message: "duplicate" });
      }
      seen.add(name);
    });
  });
type AlertHandlingValues = z.infer<typeof schema>;

function AlertHandlingForm({ organization }: { organization: Organization }) {
  const { t } = useTranslation();
  const { form, formRef, onSubmit, state } = useOrganizationForm<AlertHandlingValues>({
    organization,
    values: (org) => ({
      critical_is_urgent: org.critical_is_urgent,
      instance_labels: org.instance_labels,
    }),
    patch: (v) => ({
      critical_is_urgent: v.critical_is_urgent,
      instance_labels: v.instance_labels.map((l) => l.trim()),
    }),
    fields: (v) => [
      "critical_is_urgent",
      "instance_labels",
      ...v.instance_labels.map((_, i) => `instance_labels.${i}` as const),
    ],
    resolver: zodResolver(schema),
  });
  const { errors } = form.formState;
  const labelErrors = useWatch({ control: form.control, name: "instance_labels" }).map((_, i) =>
    errorText(t, errors.instance_labels?.[i]),
  );
  return (
    <form ref={formRef} noValidate className="flex flex-col gap-6" onSubmit={onSubmit}>
      <Card>
        <CardContent className="flex items-start gap-2">
          <input
            id="critical-is-urgent"
            type="checkbox"
            role="switch"
            className="mt-0.5 size-4 shrink-0 accent-primary focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
            aria-describedby="critical-is-urgent-hint"
            {...form.register("critical_is_urgent")}
          />
          <div className="flex min-w-0 flex-col gap-0.5">
            <Label htmlFor="critical-is-urgent" className="leading-snug">
              {t("organization.alertHandling.criticalIsUrgent")}
            </Label>
            <p id="critical-is-urgent-hint" className="text-sm text-muted-foreground">
              {t("organization.alertHandling.criticalIsUrgentHint")}
            </p>
          </div>
        </CardContent>
      </Card>
      <Card>
        <CardContent>
          <Controller
            control={form.control}
            name="instance_labels"
            render={({ field }) => (
              <NetworkListEditor
                id="instance-labels"
                label={t("organization.alertHandling.instanceLabels")}
                hint={t("organization.alertHandling.instanceLabelsHint")}
                addLabel={t("organization.alertHandling.addInstanceLabel")}
                placeholder="pod"
                value={field.value}
                errors={labelErrors}
                onChange={field.onChange}
              />
            )}
          />
        </CardContent>
      </Card>
      <p className="text-sm text-muted-foreground">{t("organization.evaluationNote")}</p>
      <SaveBar state={state} testId="alert-handling-status" />
    </form>
  );
}

function AlertHandlingPage() {
  const { t } = useTranslation();
  return (
    <OrganizationPage title={t("organization.nav.alertHandling")}>
      {(organization) => <AlertHandlingForm organization={organization} />}
    </OrganizationPage>
  );
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Organization → Severity levels (C-20.FR-1, C-20.AC-4): the label that carries severity, the mapping of its values to
// Severity levels, the emoji and colour of each level, and the values seen recently, where an unmapped value can be
// added to the mapping.

import { zodResolver } from "@hookform/resolvers/zod";
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { Controller, useWatch } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import {
  getListSeverityValuesQueryKey,
  useListSeverityValues,
} from "../api/gen/endpoints/organization/organization";
import type { Organization } from "../api/gen/model";
import { useCan } from "../components/app-shell";
import { LABEL_NAME } from "../components/labels-editor";
import {
  Field,
  OrganizationPage,
  SaveBar,
  errorText,
  useOrganizationForm,
} from "../components/organization-form";
import {
  type MappingRowErrors,
  SeverityMappingEditor,
  SeverityStylesEditor,
  type StyleErrors,
  duplicateIndexes,
} from "../components/severity-mapping-editor";
import { SeverityValues } from "../components/severity-values";
import { Card, CardContent, CardHeader, CardTitle } from "../components/ui/card";
import { Input } from "../components/ui/input";
import { fieldErrorText, problemText } from "../lib/api";

export const Route = createFileRoute("/admin/organization/severity-levels")({
  staticData: { shell: true },
  component: SeverityLevelsPage,
});

const level = z.enum(["critical", "warning", "info"]);
const schema = z
  .object({
    severity_label: z.string().trim().regex(LABEL_NAME, "invalid_format"),
    severity_mapping: z.array(z.object({ value: z.string(), level })),
    severity_styles: z.array(
      z.object({
        level,
        emoji: z.string().trim().min(1, "required"),
        color: z.string().regex(/^#[0-9a-fA-F]{6}$/, "invalid_format"),
      }),
    ),
  })
  .superRefine((v, ctx) => {
    v.severity_mapping.forEach((row, index) => {
      if (row.value.trim() === "") {
        ctx.addIssue({
          code: "custom",
          path: ["severity_mapping", index, "value"],
          message: "required",
        });
      }
    });
    for (const index of duplicateIndexes(v.severity_mapping)) {
      ctx.addIssue({
        code: "custom",
        path: ["severity_mapping", index, "value"],
        message: "duplicate",
      });
    }
  });
type SeverityValuesForm = z.infer<typeof schema>;

function SeverityForm({ organization }: { organization: Organization }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const canRead = useCan("organization:read");
  const values = useListSeverityValues({ query: { enabled: canRead } });
  const [focusLevelOf, setFocusLevelOf] = useState<{ index: number; seq: number }>();
  const { form, formRef, onSubmit, state } = useOrganizationForm<SeverityValuesForm>({
    organization,
    values: (org) => ({
      severity_label: org.severity_label,
      severity_mapping: org.severity_mapping.map((m) => ({ ...m })),
      severity_styles: org.severity_styles.map((s) => ({ ...s })),
    }),
    patch: (v) => ({
      severity_label: v.severity_label.trim(),
      severity_mapping: v.severity_mapping.map((m) => ({ value: m.value.trim(), level: m.level })),
      severity_styles: v.severity_styles.map((s) => ({ ...s, emoji: s.emoji.trim() })),
    }),
    fields: (v) => [
      "severity_label",
      "severity_mapping",
      "severity_styles",
      ...v.severity_mapping.flatMap((_, i) => [
        `severity_mapping.${i}.value` as const,
        `severity_mapping.${i}.level` as const,
      ]),
      ...v.severity_styles.flatMap((_, i) => [
        `severity_styles.${i}.emoji` as const,
        `severity_styles.${i}.color` as const,
      ]),
    ],
    resolver: zodResolver(schema),
    // The server works out the level of each value seen from the mapping just saved.
    onSaved: () => {
      void queryClient.invalidateQueries({ queryKey: getListSeverityValuesQueryKey() });
    },
  });
  const { errors } = form.formState;
  const mapping = useWatch({ control: form.control, name: "severity_mapping" });
  const styles = useWatch({ control: form.control, name: "severity_styles" });
  // A value mapped twice has the editor's own text; every other code is worded as anywhere else.
  const mappingText = (error: { message?: string } | undefined) =>
    error?.message === "duplicate"
      ? t("organization.severity.mappingDuplicate")
      : errorText(t, error);
  const mappingErrors: (MappingRowErrors | undefined)[] = mapping.map((_, i) => ({
    value: mappingText(errors.severity_mapping?.[i]?.value),
    level: errorText(t, errors.severity_mapping?.[i]?.level),
  }));
  const styleErrors: (StyleErrors | undefined)[] = styles.map((_, i) => ({
    emoji: errorText(t, errors.severity_styles?.[i]?.emoji),
    color: errorText(t, errors.severity_styles?.[i]?.color),
  }));
  const listError = errors.severity_mapping?.root ?? errors.severity_mapping;
  const stylesError = errors.severity_styles?.root ?? errors.severity_styles;
  const map = (value: string) => {
    const next = [...mapping, { value, level: "warning" as const }];
    form.setValue("severity_mapping", next, { shouldDirty: true });
    setFocusLevelOf((current) => ({ index: next.length - 1, seq: (current?.seq ?? 0) + 1 }));
  };
  return (
    <>
      <form ref={formRef} noValidate className="flex flex-col gap-6" onSubmit={onSubmit}>
        <Card>
          <CardContent>
            <Field
              id="severity-label"
              label={t("organization.severity.label")}
              hint={t("organization.severity.labelHint")}
              error={errorText(t, errors.severity_label)}
            >
              {(control) => (
                <Input
                  {...control}
                  spellCheck={false}
                  autoComplete="off"
                  className="max-w-60 font-mono"
                  {...form.register("severity_label")}
                />
              )}
            </Field>
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>
              <h2>{t("organization.severity.mappingTitle")}</h2>
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            <p className="text-sm text-muted-foreground">
              {t("organization.severity.mappingHint")}
            </p>
            <Controller
              control={form.control}
              name="severity_mapping"
              render={({ field }) => (
                <SeverityMappingEditor
                  value={field.value}
                  errors={mappingErrors}
                  focusLevelOf={focusLevelOf}
                  onChange={field.onChange}
                />
              )}
            />
            {listError?.message !== undefined && (
              <p className="text-sm text-destructive">{fieldErrorText(t, listError.message)}</p>
            )}
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>
              <h2>{t("organization.severity.stylesTitle")}</h2>
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            <Controller
              control={form.control}
              name="severity_styles"
              render={({ field }) => (
                <SeverityStylesEditor
                  value={field.value}
                  errors={styleErrors}
                  onChange={field.onChange}
                />
              )}
            />
            {stylesError?.message !== undefined && (
              <p className="text-sm text-destructive">{fieldErrorText(t, stylesError.message)}</p>
            )}
          </CardContent>
        </Card>
        <p className="text-sm text-muted-foreground">{t("organization.evaluationNote")}</p>
        <SaveBar state={state} testId="severity-levels-status" />
      </form>
      {canRead && values.data !== undefined && (
        <SeverityValues list={values.data} mapping={mapping} onMap={map} />
      )}
      {canRead && values.data === undefined && values.isError && (
        <p className="text-sm text-muted-foreground" role="status">
          {problemText(t, values.error)}
        </p>
      )}
    </>
  );
}

function SeverityLevelsPage() {
  const { t } = useTranslation();
  return (
    <OrganizationPage title={t("organization.nav.severityLevels")}>
      {(organization) => <SeverityForm organization={organization} />}
    </OrganizationPage>
  );
}

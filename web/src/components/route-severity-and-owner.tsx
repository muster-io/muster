// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The section "Severity, Escalation chains and owner" of the Route editor (C-26.FR-8, FR-9): until the owning Team and
// the Escalation chains join it, only the Minimum severity of the Route, which raises the Severity level of its Alert
// Groups. "Learn more" sits beside the field.

import type { TFunction } from "i18next";
import type { UseFormRegisterReturn } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import { MinimumSeverity } from "../api/gen/model";
import { fieldErrorText } from "../lib/api";
import { DocsLink } from "./docs-link";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

/** The field of the section in the form of the Route editor. */
export const severityAndOwnerSchema = {
  minimum_severity: z.enum(["none", "warning", "critical"]),
};

export interface SeverityAndOwnerValues {
  minimum_severity: MinimumSeverity;
}

const LEVELS = [
  MinimumSeverity.none,
  MinimumSeverity.warning,
  MinimumSeverity.critical,
] as const satisfies readonly MinimumSeverity[];

/** The Minimum severity in the language of the page: "None", and the Severity levels by their own names. */
export function minimumSeverityName(t: TFunction, level: MinimumSeverity): string {
  switch (level) {
    case "none":
      return t("routes.severity.none");
    case "warning":
      return t("routes.severity.warning");
    case "critical":
      return t("routes.severity.critical");
    default:
      return level satisfies never;
  }
}

export function RouteSeverityAndOwner({
  id,
  minimumSeverity,
  error,
}: {
  id: string;
  minimumSeverity: UseFormRegisterReturn;
  /** The error code of the field. */
  error?: string;
}) {
  const { t } = useTranslation();
  const described = [`${id}-hint`, error === undefined ? null : `${id}-error`]
    .filter((v) => v !== null)
    .join(" ");
  return (
    <fieldset
      aria-labelledby={`${id}-title`}
      className="flex min-w-0 flex-col gap-4"
      data-testid="route-severity-section"
    >
      <legend className="mb-2 text-base font-semibold">
        <span id={`${id}-title`}>{t("routes.severity.title")}</span>
      </legend>
      <div className="flex min-w-0 flex-col gap-2">
        <div className="flex flex-wrap items-baseline gap-x-2">
          <Label htmlFor={`${id}-minimum-severity`}>{t("routes.severity.minimum")}</Label>
          <DocsLink
            to="field.minimum_severity"
            kind="learn_more"
            about={t("docs.fields.minimumSeverity")}
          />
        </div>
        <NativeSelect
          id={`${id}-minimum-severity`}
          data-testid="route-minimum-severity"
          className="w-48"
          aria-invalid={error !== undefined}
          aria-describedby={described}
          {...minimumSeverity}
        >
          {LEVELS.map((level) => (
            <NativeSelectOption key={level} value={level}>
              {minimumSeverityName(t, level)}
            </NativeSelectOption>
          ))}
        </NativeSelect>
        <p id={`${id}-hint`} className="text-sm text-muted-foreground">
          {t("routes.severity.hint")}
        </p>
        {error !== undefined && (
          <p id={`${id}-error`} className="text-sm text-destructive">
            {fieldErrorText(t, error)}
          </p>
        )}
      </div>
    </fieldset>
  );
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The banner of a Route whose template keeps failing (C-12.FR-6; reference.md, banners): "Template error since HH:MM:
// {error}." with "Messages use the fallback template." — or "Requests are not sent." for an outgoing webhook — the
// time in the user's time zone and the error as untrusted text.

import { TriangleAlertIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { TemplateErrorState } from "../api/gen/model";
import { useTimeFormat } from "../lib/time";

export function TemplateErrorBanner({ state }: { state: TemplateErrorState }) {
  const { t } = useTranslation();
  const { time } = useTimeFormat();
  return (
    <div
      role="status"
      className="flex items-start gap-2 rounded-lg border border-warning/60 bg-warning-surface px-3 py-2 text-sm text-foreground"
      data-testid="template-error-banner"
    >
      <TriangleAlertIcon className="mt-0.5 size-4 shrink-0 text-warning" aria-hidden="true" />
      <p className="min-w-0 wrap-anywhere">
        {t("routes.templateError.since", { time: time(state.since), error: state.error })}{" "}
        {state.fallback === "not_sent"
          ? t("routes.templateError.notSent")
          : t("routes.templateError.fallback")}
      </p>
    </div>
  );
}

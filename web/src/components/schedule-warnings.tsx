// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The warnings of a Schedule (C-27.FR-5, FR-7, FR-8): a gap of the next two weeks, a person whose Shifts or Override are
// skipped because they may not be Paged — disabled, a Viewer, or not in the Team —, and an On-call whom a Destination
// of the Team cannot mention. The texts are those of reference.md; "Unreachable in {messenger}" names the messenger of
// the missing Account link, and the other reasons name the Destination.

import type { TFunction } from "i18next";
import { TriangleAlertIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { ScheduleWarning } from "../api/gen/model";
import { stamp, stampNear } from "../lib/schedule-time";
import { unreachable } from "./destination-test-panel";
import { destinationTypeName } from "./destination-health";
import { personLabel } from "./schedule-view";

type Reachable = Pick<ScheduleWarning, "destination" | "messenger" | "reason">;

/** The text of an unreachable On-call: "Unreachable in Mattermost: no linked account." and the other reasons. */
export function unreachableText(t: TFunction, warning: Reachable): string {
  const { destination, messenger, reason } = warning;
  if (destination === null || messenger === null || reason === null) {
    return "";
  }
  switch (reason) {
    case "no_account_link":
      return t("schedule.warnings.noAccountLink", { messenger: destinationTypeName(t, messenger) });
    case "bridge_no_native_mentions":
      return t("schedule.warnings.bridgeNoMentions", { destination: destination.name });
    case "webhook_no_thread_reply":
      return t("schedule.warnings.webhookNoReply", { destination: destination.name });
    case "not_in_discussion_group":
      return t("schedule.warnings.notInGroup", { destination: destination.name });
    default:
      return unreachable(reason);
  }
}

/** The text of a warning of one of the five kinds. */
export function warningText(
  t: TFunction,
  warning: ScheduleWarning,
  zone: string,
  locale: string,
): string {
  const who = warning.user === null ? "" : personLabel(warning.user);
  switch (warning.kind) {
    case "gap":
      return warning.start === null || warning.end === null
        ? ""
        : t("schedule.warnings.gap", {
            from: stamp(warning.start, zone, locale),
            to: stampNear(warning.end, warning.start, zone, locale),
          });
    case "disabled":
      return warning.rotation_layer === null
        ? t("schedule.warnings.disabledInOverride", { who })
        : t("schedule.warnings.disabled", { who, layer: warning.rotation_layer.name });
    case "viewer":
      return t("schedule.warnings.viewer", { who });
    case "not_in_team":
      return t("schedule.warnings.notInTeam", { who });
    case "unreachable":
      return t("schedule.warnings.unreachableFor", { who, reason: unreachableText(t, warning) });
    default:
      return unreachable(warning.kind);
  }
}

/** One line per gap, one per person who may not be Paged and one per On-call a Destination cannot reach. */
export function distinctWarnings(warnings: readonly ScheduleWarning[]): ScheduleWarning[] {
  const seen = new Set<string>();
  return warnings.filter((w) => {
    const key =
      w.kind === "gap"
        ? `gap|${w.start}|${w.end}`
        : w.kind === "unreachable"
          ? `unreachable|${w.user?.id}|${w.destination?.id}|${w.reason}`
          : `${w.kind}|${w.user?.id}|${w.rotation_layer?.id}|${w.override_id}`;
    if (seen.has(key)) {
      return false;
    }
    seen.add(key);
    return true;
  });
}

export function ScheduleWarnings({
  warnings,
  zone,
}: {
  warnings: readonly ScheduleWarning[];
  zone: string;
}) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const shown = distinctWarnings(warnings);
  return (
    <section className="flex flex-col gap-2" aria-labelledby="schedule-warnings-title">
      <h2 id="schedule-warnings-title" className="text-lg font-semibold">
        {t("schedule.warnings.title")}
      </h2>
      {shown.length === 0 ? (
        <p className="text-sm text-muted-foreground" data-testid="schedule-no-warnings">
          {t("schedule.warnings.none")}
        </p>
      ) : (
        <ul className="flex flex-col gap-1" data-testid="schedule-warnings">
          {shown.map((w) => (
            <li
              key={`${w.kind}-${w.user?.id}-${w.start}-${w.destination?.id}-${w.reason}-${w.rotation_layer?.id}-${w.override_id}`}
              className="flex items-start gap-2 text-sm"
              data-kind={w.kind}
            >
              <TriangleAlertIcon
                aria-hidden="true"
                className={
                  w.kind === "gap"
                    ? "mt-0.5 size-4 shrink-0 text-destructive"
                    : "mt-0.5 size-4 shrink-0 text-muted-foreground"
                }
              />
              <span className="wrap-anywhere">
                {warningText(t, w, zone, locale)}
                {w.kind === "unreachable" && w.gateway_reaches === true && (
                  <>
                    {" "}
                    <span data-testid="schedule-gateway-reaches">{t("gatewayReaches.note")}</span>
                  </>
                )}
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

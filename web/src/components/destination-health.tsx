// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The health of a Destination (C-13.FR-9, C-11.FR-9; reference.md, banners): "Healthy", or "Broken" with the banner
// "Broken since HH:MM: {reason}. Muster tries again every {interval}." in the user's time zone. The reason comes from
// the messenger and is shown as text only. The list, the Destination page and the Route editor's Destinations section
// show the same banner.

import type { TFunction } from "i18next";
import { CircleCheckIcon, TriangleAlertIcon } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import type { DestinationHealth, DestinationType } from "../api/gen/model";
import { formatDuration, useTimeFormat } from "../lib/time";
import { cn } from "./ui/utils";

/** delivery.broken_probe_interval: built in, 5 minutes. */
export const BROKEN_PROBE_INTERVAL_SECONDS = 300;

/** The name of a Destination type; the messengers' names are not translated. */
export function destinationTypeName(t: TFunction, type: DestinationType): string {
  switch (type) {
    case "mattermost":
      return "Mattermost";
    case "telegram":
      return "Telegram";
    default:
      return t("destinations.types.webhook");
  }
}

export function healthText(t: TFunction, health: DestinationHealth): string {
  return health.state === "broken"
    ? t("destinations.health.broken")
    : t("destinations.health.healthy");
}

/** The reason without a closing full stop, which the sentence of the banner adds. */
function reasonOf(t: TFunction, health: DestinationHealth): string {
  const reason = (health.reason ?? "").trim().replace(/\.$/, "");
  return reason === "" ? t("destinations.health.noReason") : reason;
}

/** The text of the Broken banner, with the time in the time zone the formatter uses. */
export function brokenText(
  t: TFunction,
  health: DestinationHealth,
  time: (iso: string) => string,
): string {
  const interval = formatDuration(t, BROKEN_PROBE_INTERVAL_SECONDS);
  const reason = reasonOf(t, health);
  return health.since
    ? t("destinations.health.brokenSince", { time: time(health.since), reason, interval })
    : t("destinations.health.brokenNoSince", { reason, interval });
}

/** "Healthy" or "Broken" as a small badge. */
export function HealthBadge({
  health,
  className,
}: {
  health: DestinationHealth;
  className?: string;
}) {
  const { t } = useTranslation();
  const broken = health.state === "broken";
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 text-sm whitespace-nowrap",
        broken ? "font-medium text-destructive" : "text-emerald-700 dark:text-emerald-400",
        className,
      )}
      data-testid="destination-health"
      data-state={health.state}
    >
      {broken ? (
        <TriangleAlertIcon aria-hidden="true" className="size-3.5 shrink-0" />
      ) : (
        <CircleCheckIcon aria-hidden="true" className="size-3.5 shrink-0" />
      )}
      {healthText(t, health)}
    </span>
  );
}

/** The Broken banner of a Broken Destination; nothing for a healthy one. name says which, where several show. */
export function BrokenBanner({ health, name }: { health: DestinationHealth; name?: ReactNode }) {
  const { t } = useTranslation();
  const { time } = useTimeFormat();
  if (health.state !== "broken") {
    return null;
  }
  return (
    <div
      className="flex items-start gap-2 rounded-lg border border-destructive/40 bg-card px-3 py-2 text-sm"
      data-testid="broken-banner"
    >
      <TriangleAlertIcon aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-destructive" />
      <p className="min-w-0 wrap-anywhere">
        {name !== undefined && (
          <>
            <span className="font-medium">{name}</span>
            {": "}
          </>
        )}
        <span data-testid="broken-banner-text">{brokenText(t, health, time)}</span>
      </p>
    </div>
  );
}

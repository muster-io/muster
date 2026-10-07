// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Heartbeat state of an Integration as a badge (C-07.FR-3): "Not configured" and "Waiting" neutral, "Live" calm,
// "Lost" in the colour of a problem. Each state has its own text and icon, so the badge never relies on colour alone.

import type { TFunction } from "i18next";
import { CircleDashedIcon, HeartCrackIcon, HeartPulseIcon, HourglassIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { HeartbeatState } from "../api/gen/model";
import { cn } from "./ui/utils";

/** The text of a Heartbeat state. */
export function heartbeatStateText(t: TFunction, state: HeartbeatState): string {
  switch (state) {
    case "waiting":
      return t("heartbeat.state.waiting");
    case "live":
      return t("heartbeat.state.live");
    case "lost":
      return t("heartbeat.state.lost");
    default:
      return t("heartbeat.state.notConfigured");
  }
}

const STYLE: Record<HeartbeatState, string> = {
  not_configured: "border-border text-muted-foreground",
  waiting: "border-border bg-muted text-foreground",
  live: "border-emerald-700/40 bg-emerald-50 text-emerald-800 dark:border-emerald-400/40 dark:bg-emerald-950 dark:text-emerald-300",
  lost: "border-destructive/50 bg-destructive/10 text-destructive dark:bg-destructive/20",
};

const ICON = {
  not_configured: CircleDashedIcon,
  waiting: HourglassIcon,
  live: HeartPulseIcon,
  lost: HeartCrackIcon,
} as const satisfies Record<HeartbeatState, unknown>;

/**
 * The badge of a Heartbeat state. labelled prefixes the state with "Heartbeat:" for screen readers, where no column
 * header names what the badge is.
 */
export function HeartbeatBadge({
  state,
  labelled = false,
}: {
  state: HeartbeatState;
  labelled?: boolean;
}) {
  const { t } = useTranslation();
  const Icon = ICON[state];
  return (
    <span
      className={cn(
        "inline-flex w-fit items-center gap-1 rounded-md border px-1.5 py-0.5 text-xs font-medium whitespace-nowrap",
        STYLE[state],
      )}
      data-testid="heartbeat-badge"
      data-state={state}
    >
      <Icon aria-hidden="true" className="size-3.5 shrink-0" />
      {labelled && <span className="sr-only">{t("heartbeat.badgeLabel")} </span>}
      <span>{heartbeatStateText(t, state)}</span>
    </span>
  );
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The mark of the live updates in the header (C-09.FR-26): a dot and the text of the state in a status region, so that
// a screen reader reads each change out and the state is never in the dot's colour alone for it. Below the sm
// breakpoint only the dot shows, in every state, so that the navigation keeps its width; the text stays in the region
// for screen readers and is the title of the popover, which pressing the mark opens: it says what the state means and,
// in offline, offers to reload the page. The status region holds the button rather than sitting in it, because the
// content of a button is presentational; the popover itself is portalled out of the region, a dialog named by its title
// and described by its explanation. The mark sends nothing to the server and records nothing (NFR-7).

import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";

import {
  type LiveSnapshot,
  type LiveState,
  type LiveStatus,
  useLiveStatus,
} from "../lib/live-status";
import { useTimeFormat } from "../lib/time";
import { Button } from "./ui/button";
import {
  Popover,
  PopoverContent,
  PopoverDescription,
  PopoverHeader,
  PopoverTitle,
  PopoverTrigger,
} from "./ui/popover";
import { cn } from "./ui/utils";

function stateText(t: TFunction, state: LiveState): string {
  switch (state) {
    case "connecting":
      return t("liveUpdates.connecting");
    case "live":
      return t("liveUpdates.live");
    case "reconnecting":
      return t("liveUpdates.reconnecting");
    default:
      return t("liveUpdates.offline");
  }
}

const DOT: Record<LiveState, string> = {
  connecting: "bg-muted-foreground",
  live: "bg-emerald-600 dark:bg-emerald-400",
  reconnecting: "animate-pulse bg-warning motion-reduce:animate-none",
  offline: "bg-destructive",
};

/**
 * Where the text shows: below the sm breakpoint for screen readers only, in every state; from it up beside the dot,
 * except while connecting until live.reconnecting_after has passed, so that a page load does not flash it.
 */
function textClass({ state, overdue }: LiveSnapshot): string {
  if (state === "connecting" && !overdue) {
    return "sr-only";
  }
  return "sr-only sm:not-sr-only";
}

function reloadPage(): void {
  window.location.reload();
}

/** The mark for a state; reload replaces the reload of the page in tests. */
export function LiveMark({
  snapshot,
  reload = reloadPage,
}: {
  snapshot: LiveSnapshot;
  reload?: () => void;
}) {
  const { t } = useTranslation();
  const { time } = useTimeFormat();
  const { state } = snapshot;
  const text = stateText(t, state);
  const explanation =
    state === "live"
      ? t("liveUpdates.liveExplained")
      : state === "connecting"
        ? t("liveUpdates.connectingExplained")
        : t("liveUpdates.lostExplained", { time: time(snapshot.lostAt) });
  return (
    <div
      role="status"
      aria-live="polite"
      data-testid="live-indicator"
      data-state={state}
      className="flex"
    >
      <Popover>
        <PopoverTrigger
          render={<Button variant="ghost" size="sm" className="h-8 min-w-8 px-2 sm:h-7" />}
        >
          <span
            aria-hidden="true"
            data-testid="live-indicator-dot"
            className={cn("size-2 shrink-0 rounded-full", DOT[state])}
          />
          <span data-testid="live-indicator-text" className={textClass(snapshot)}>
            {text}
          </span>
        </PopoverTrigger>
        <PopoverContent align="end" className="max-w-[calc(100vw-2rem)]">
          <PopoverHeader>
            <PopoverTitle data-testid="live-indicator-title">{text}</PopoverTitle>
            <PopoverDescription>{explanation}</PopoverDescription>
          </PopoverHeader>
          {state === "offline" && (
            <Button size="sm" className="self-start" onClick={reload}>
              {t("liveUpdates.reload")}
            </Button>
          )}
        </PopoverContent>
      </Popover>
    </div>
  );
}

/** The mark of the page's live updates, or of another store's. */
export function LiveIndicator({ status }: { status?: LiveStatus }) {
  return <LiveMark snapshot={useLiveStatus(status)} />;
}

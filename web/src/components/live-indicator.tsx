// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The mark of the live updates in the header (C-09.FR-26): a dot and the text of the state in a status region, so that
// a screen reader reads each change out and the state is never in the dot's colour alone for it. Below the sm
// breakpoint only the dot shows, in every state, so that the navigation keeps its width; so that the dot does not tell
// the states by colour alone either (WCAG 1.4.1), each state has its own shape at the same 8 px: a filled dot in live,
// a ring while connecting, a half-filled ring while reconnecting and a ring with a slash offline. The text stays in the
// region for screen readers and is the title of the popover, which pressing the mark opens: it says what the state
// means and, in offline, offers to reload the page. The status region holds the button rather than sitting in it,
// because the content of a button is presentational; the popover itself is portalled out of the region, a dialog named
// by its title and described by its explanation. The mark sends nothing to the server and records nothing (NFR-7).

import type { TFunction } from "i18next";
import type { ReactNode } from "react";
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

type Shape = "filled" | "ring" | "half" | "slashed";

/** The shape and the colour of the dot in each state: no two states share a shape. */
const DOT: Record<LiveState, { shape: Shape; className: string }> = {
  connecting: { shape: "ring", className: "text-muted-foreground" },
  live: { shape: "filled", className: "text-emerald-600 dark:text-emerald-400" },
  reconnecting: {
    shape: "half",
    className: "animate-pulse text-warning motion-reduce:animate-none",
  },
  offline: { shape: "slashed", className: "text-destructive" },
};

/**
 * The shapes on a viewBox of 8 × 8, one unit per CSS pixel. The ring's stroke of 1.25 runs from the edge to a radius of
 * 2.75, the hole wide enough to see at 1×; the half fills the ring's left; the slash, 1 wide so that it stays apart from
 * the ring at 1×, crosses it from top left to bottom right and ends on its centre line, inside the outer edge.
 */
const RING = (
  <circle cx="4" cy="4" r="3.375" fill="none" stroke="currentColor" strokeWidth="1.25" />
);

const SHAPES: Record<Shape, ReactNode> = {
  filled: <circle cx="4" cy="4" r="4" fill="currentColor" />,
  ring: RING,
  half: (
    <>
      {RING}
      <path d="M4 .625a3.375 3.375 0 0 0 0 6.75z" fill="currentColor" />
    </>
  ),
  slashed: (
    <>
      {RING}
      <path d="M1.61 1.61 6.39 6.39" fill="none" stroke="currentColor" strokeWidth="1" />
    </>
  ),
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
          <svg
            viewBox="0 0 8 8"
            aria-hidden="true"
            focusable="false"
            data-testid="live-indicator-dot"
            data-shape={DOT[state].shape}
            className={cn("size-2 shrink-0", DOT[state].className)}
          >
            {SHAPES[DOT[state].shape]}
          </svg>
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

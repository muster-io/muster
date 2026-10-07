// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Tabs that pick what a list shows, such as the status tabs of the Alert Group list with their counts or the
// Firing/Resolved filter of its Alerts: one tab stop, the arrow keys, Home and End move between them, and they wrap
// onto more lines on a narrow screen instead of making the page scroll sideways.

import { type KeyboardEvent, useRef } from "react";

import { cn } from "./ui/utils";

export interface StatusTabsProps<T extends string> {
  tabs: readonly T[];
  value: T;
  /** The accessible name of the tab list. */
  label: string;
  /** The id of the panel the tabs control; each tab is `${panelId}-tab-${tab}`. */
  panelId: string;
  labelOf: (tab: T) => string;
  /** The count shown beside a tab's name; none while it is unknown. */
  countOf?: (tab: T) => number | undefined;
  onSelect: (tab: T) => void;
}

export function StatusTabs<T extends string>({
  tabs,
  value,
  label,
  panelId,
  labelOf,
  countOf,
  onSelect,
}: StatusTabsProps<T>) {
  const refs = useRef<(HTMLButtonElement | null)[]>([]);
  const onKeyDown = (e: KeyboardEvent, index: number) => {
    const step = { ArrowRight: 1, ArrowLeft: -1 }[e.key];
    let next: number | undefined;
    if (step !== undefined) {
      next = (index + step + tabs.length) % tabs.length;
    } else if (e.key === "Home") {
      next = 0;
    } else if (e.key === "End") {
      next = tabs.length - 1;
    }
    const tab = next === undefined ? undefined : tabs[next];
    if (next !== undefined && tab !== undefined) {
      e.preventDefault();
      onSelect(tab);
      refs.current[next]?.focus();
    }
  };
  return (
    <div
      role="tablist"
      aria-label={label}
      className="flex w-fit max-w-full flex-wrap gap-0.5 rounded-lg border bg-muted/50 p-0.5"
    >
      {tabs.map((tab, index) => {
        const count = countOf?.(tab);
        return (
          <button
            key={tab}
            ref={(el) => {
              refs.current[index] = el;
            }}
            type="button"
            role="tab"
            id={`${panelId}-tab-${tab}`}
            aria-selected={tab === value}
            aria-controls={panelId}
            tabIndex={tab === value ? 0 : -1}
            className={cn(
              "inline-flex items-center gap-1.5 rounded-md px-2.5 py-1 text-sm font-medium whitespace-nowrap outline-none focus-visible:ring-2 focus-visible:ring-ring",
              tab === value
                ? "bg-background text-foreground shadow-sm"
                : "text-muted-foreground hover:text-foreground",
            )}
            onClick={() => onSelect(tab)}
            onKeyDown={(e) => onKeyDown(e, index)}
          >
            {labelOf(tab)}
            {count !== undefined && (
              <span
                className="rounded-full bg-muted px-1.5 text-xs text-muted-foreground tabular-nums"
                data-testid="tab-count"
              >
                {count}
              </span>
            )}
          </button>
        );
      })}
    </div>
  );
}

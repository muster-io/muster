// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The state of the live-updates stream that the mark in the header shows (C-09.FR-26): connecting from the page load
// until the stream first opens, live while it is open, reconnecting once it has been lost for live.reconnecting_after
// and offline once it has been lost for live.offline_after. Before the first open both are counted from the page load:
// live.reconnecting_after leaves the state at connecting and only makes its text show (overdue), and live.offline_after
// turns it offline. A loss shorter than live.reconnecting_after, such as a replica restarting, leaves the state as it
// was. connectLiveUpdates reports every open and error of the stream here, those of the shell's new EventSource after a
// close for good included, so that a reopen continues the same state instead of starting at connecting again. An open
// after a loss, or the first one after the stream failed before it ever opened, means every query is read again. The
// timers run on the page's clock. Nothing here is sent anywhere or recorded (NFR-7).

import { useSyncExternalStore } from "react";

/** live.reconnecting_after, provisional (P-59): a loss this long turns the mark to "Reconnecting…". */
export const RECONNECTING_AFTER_MS = 5_000;
/** live.offline_after, provisional (P-59): a loss this long turns the mark to "Offline". */
export const OFFLINE_AFTER_MS = 30_000;

/** EventSource.OPEN; the store does not read the global, which tests replace. */
const OPEN = 1;

export type LiveState = "connecting" | "live" | "reconnecting" | "offline";

export interface LiveSnapshot {
  state: LiveState;
  /** When the stream was last lost (RFC 3339), or the page load while it has never opened. */
  lostAt: string;
  /** Whether connecting has lasted live.reconnecting_after, from when the mark says "Connecting…". */
  overdue: boolean;
}

export interface LiveStatus {
  /** Starts at connecting, as on a page load, with both timers counting. */
  start: () => void;
  /**
   * Stops the timers and goes back to connecting, so that a mark shown again before the next start shows no old state.
   */
  stop: () => void;
  /**
   * Reports that the stream opened. Returns whether it had opened or failed before since start, so that a loss came
   * between, or the page has not been live since it loaded, and every query is read again.
   */
  opened: () => boolean;
  /** Reports an error of the stream with the EventSource's readyState. */
  failed: (readyState: number) => void;
  getSnapshot: () => LiveSnapshot;
  subscribe: (listener: () => void) => () => void;
}

function now(): string {
  return new Date(Date.now()).toISOString();
}

/** A store of its own; the page uses liveStatus, tests make their own. */
export function createLiveStatus(): LiveStatus {
  let snapshot: LiveSnapshot = { state: "connecting", lostAt: now(), overdue: false };
  let openedBefore = false;
  let failedBefore = false;
  let reconnectingTimer: ReturnType<typeof setTimeout> | undefined;
  let offlineTimer: ReturnType<typeof setTimeout> | undefined;
  const listeners = new Set<() => void>();

  const set = (next: Partial<LiveSnapshot>) => {
    snapshot = { ...snapshot, ...next };
    for (const listener of listeners) {
      listener();
    }
  };
  const clearTimers = () => {
    clearTimeout(reconnectingTimer);
    clearTimeout(offlineTimer);
    reconnectingTimer = undefined;
    offlineTimer = undefined;
  };
  const arm = () => {
    clearTimers();
    reconnectingTimer = setTimeout(() => {
      reconnectingTimer = undefined;
      // Before the first open the state stays connecting, and the mark starts to say so.
      set(snapshot.state === "connecting" ? { overdue: true } : { state: "reconnecting" });
    }, RECONNECTING_AFTER_MS);
    offlineTimer = setTimeout(() => {
      offlineTimer = undefined;
      set({ state: "offline", overdue: false });
    }, OFFLINE_AFTER_MS);
  };

  return {
    start: () => {
      openedBefore = false;
      failedBefore = false;
      set({ state: "connecting", lostAt: now(), overdue: false });
      arm();
    },
    stop: () => {
      clearTimers();
      set({ state: "connecting", lostAt: now(), overdue: false });
    },
    opened: () => {
      const again = openedBefore || failedBefore;
      openedBefore = true;
      failedBefore = false;
      clearTimers();
      if (snapshot.state !== "live" || snapshot.overdue) {
        set({ state: "live", overdue: false });
      }
      return again;
    },
    failed: (readyState) => {
      if (readyState === OPEN) {
        return;
      }
      // Hints may have been missed since the page loaded, even if the stream has never opened.
      failedBefore = true;
      // A loss while live starts the timers; later errors of the same loss leave them counting.
      if (snapshot.state !== "live" || offlineTimer !== undefined) {
        return;
      }
      set({ lostAt: now() });
      arm();
    },
    getSnapshot: () => snapshot,
    subscribe: (listener) => {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
  };
}

/** The page's live-updates state, which the shell starts and the mark shows. */
export const liveStatus: LiveStatus = createLiveStatus();

/** The state of a store, liveStatus by default, re-rendering on every change. */
export function useLiveStatus(status: LiveStatus = liveStatus): LiveSnapshot {
  return useSyncExternalStore(status.subscribe, status.getSnapshot);
}

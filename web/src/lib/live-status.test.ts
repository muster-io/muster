// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The state of the live-updates stream (C-09.FR-26), driven through connectLiveUpdates with a stand-in EventSource and
// fake timers: connecting until the first open, live on every open, reconnecting after live.reconnecting_after and
// offline after live.offline_after, a loss shorter than live.reconnecting_after, the shell's new EventSource after a
// close for good, which continues the same state, and a stream that failed before it first opened, whose first open
// reads everything again.

import { QueryClient } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import { connectLiveUpdates } from "./live";
import {
  type LiveStatus,
  OFFLINE_AFTER_MS,
  RECONNECTING_AFTER_MS,
  createLiveStatus,
} from "./live-status";

const CONNECTING = 0;
const OPEN = 1;
const CLOSED = 2;

/** A stand-in for the browser's EventSource that the test drives. */
class StandInEventSource extends EventTarget {
  static last: StandInEventSource | undefined;
  readyState = CONNECTING;
  readonly url: string;
  constructor(url: string) {
    super();
    this.url = url;
    StandInEventSource.last = this;
  }
  open() {
    this.readyState = OPEN;
    this.dispatchEvent(new Event("open"));
  }
  /** The stream is lost: the browser tries again (CONNECTING) or gives up (CLOSED). */
  lose(readyState = CONNECTING) {
    this.readyState = readyState;
    this.dispatchEvent(new Event("error"));
  }
  close() {
    this.readyState = CLOSED;
  }
}

const LOAD = new Date("2026-10-19T02:00:00.000Z");

function at(ms: number): string {
  return new Date(LOAD.getTime() + ms).toISOString();
}

let queryClient: QueryClient;
let status: LiveStatus;
const closes: (() => void)[] = [];

/** Opens the stream as the shell does, with the test's store; returns the new stand-in. */
function connect(onClosed: () => void = () => {}): StandInEventSource {
  closes.push(
    connectLiveUpdates(queryClient, { onClosed, eventSource: StandInEventSource, status }),
  );
  const source = StandInEventSource.last;
  if (source === undefined) {
    throw new Error("no EventSource");
  }
  return source;
}

function state() {
  return status.getSnapshot().state;
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
  vi.setSystemTime(LOAD);
  queryClient = new QueryClient();
  status = createLiveStatus();
  status.start();
});

afterEach(() => {
  for (const close of closes.splice(0)) {
    close();
  }
  status.stop();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("live-updates state", () => {
  test("connecting from the page load: its text after 5 s, offline after 30 s without an open", () => {
    const source = connect();
    expect(status.getSnapshot()).toEqual({ state: "connecting", lostAt: at(0), overdue: false });

    vi.advanceTimersByTime(3_000);
    // The browser's attempts fail; the timers keep counting from the page load.
    source.lose();
    vi.advanceTimersByTime(RECONNECTING_AFTER_MS - 3_000 - 100);
    expect(status.getSnapshot()).toEqual({ state: "connecting", lostAt: at(0), overdue: false });
    vi.advanceTimersByTime(100);
    expect(status.getSnapshot()).toEqual({ state: "connecting", lostAt: at(0), overdue: true });

    source.lose();
    vi.advanceTimersByTime(OFFLINE_AFTER_MS - RECONNECTING_AFTER_MS - 100);
    expect(state()).toBe("connecting");
    vi.advanceTimersByTime(100);
    expect(status.getSnapshot()).toEqual({ state: "offline", lostAt: at(0), overdue: false });

    source.open();
    expect(status.getSnapshot()).toEqual({ state: "live", lostAt: at(0), overdue: false });
  });

  test("the first open, with no error before it, turns live and reads nothing again", () => {
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const source = connect();
    vi.advanceTimersByTime(1_000);
    source.open();
    expect(state()).toBe("live");
    expect(invalidate).not.toHaveBeenCalled();
    // The timers of the page load are cancelled.
    vi.advanceTimersByTime(60_000);
    expect(status.getSnapshot()).toEqual({ state: "live", lostAt: at(0), overdue: false });
  });

  test.each([
    ["the browser's reconnect of the same EventSource", CONNECTING],
    ["the shell's new EventSource after a close for good", CLOSED],
  ] as const)(
    "a stream that failed before it first opened reads everything again when %s opens",
    (_, readyState) => {
      const invalidate = vi.spyOn(queryClient, "invalidateQueries");
      const first = connect();
      vi.advanceTimersByTime(1_000);
      first.lose(readyState);
      vi.advanceTimersByTime(RECONNECTING_AFTER_MS);
      expect(status.getSnapshot()).toEqual({ state: "connecting", lostAt: at(0), overdue: true });
      const source = readyState === CLOSED ? connect() : first;
      expect(invalidate).not.toHaveBeenCalled();

      source.open();
      expect(state()).toBe("live");
      expect(invalidate).toHaveBeenCalledTimes(1);
      expect(invalidate).toHaveBeenCalledWith();
    },
  );

  test("an error before the first open makes that open one after a loss; an error of an open stream does not", () => {
    status.failed(OPEN);
    expect(status.opened()).toBe(false);

    status.start();
    status.failed(CONNECTING);
    expect(state()).toBe("connecting");
    expect(status.opened()).toBe(true);
  });

  test("a loss turns reconnecting after 5 s and offline after 30 s; the next open returns to live and reads everything again", () => {
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const source = connect();
    source.open();
    vi.advanceTimersByTime(10_000);

    source.lose();
    expect(status.getSnapshot()).toEqual({ state: "live", lostAt: at(10_000), overdue: false });
    vi.advanceTimersByTime(RECONNECTING_AFTER_MS - 100);
    expect(state()).toBe("live");
    vi.advanceTimersByTime(100);
    expect(status.getSnapshot()).toEqual({
      state: "reconnecting",
      lostAt: at(10_000),
      overdue: false,
    });

    // The browser's reconnects fail every 3 s; they neither start the timers again nor move the time of the loss.
    for (let i = 0; i < 8; i++) {
      vi.advanceTimersByTime(3_000);
      source.lose();
    }
    expect(state()).toBe("reconnecting");
    vi.advanceTimersByTime(OFFLINE_AFTER_MS - RECONNECTING_AFTER_MS - 24_000 - 100);
    expect(state()).toBe("reconnecting");
    vi.advanceTimersByTime(100);
    expect(status.getSnapshot()).toEqual({ state: "offline", lostAt: at(10_000), overdue: false });

    vi.advanceTimersByTime(60_000);
    expect(invalidate).not.toHaveBeenCalled();
    source.open();
    expect(state()).toBe("live");
    expect(invalidate).toHaveBeenCalledTimes(1);
    expect(invalidate).toHaveBeenCalledWith();
  });

  test("a loss of 4 s leaves live throughout", () => {
    const changes = vi.fn();
    status.subscribe(changes);
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const source = connect();
    source.open();
    changes.mockClear();

    source.lose();
    for (let i = 0; i < 4; i++) {
      vi.advanceTimersByTime(1_000);
      expect(state()).toBe("live");
    }
    source.open();
    expect(state()).toBe("live");
    expect(invalidate).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(OFFLINE_AFTER_MS * 2);
    expect(state()).toBe("live");
    // Only the time of the loss changed; the state never left live.
    expect(changes).toHaveBeenCalledTimes(1);
  });

  test("the shell's new EventSource after a close for good continues the state, and its first open reads everything again", () => {
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const onClosed = vi.fn();
    const first = connect(onClosed);
    first.open();

    first.lose(CLOSED);
    expect(onClosed).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(10_000);
    expect(state()).toBe("reconnecting");

    // The shell opens a new EventSource 10 s later; while it connects, the state stays reconnecting.
    const second = connect(onClosed);
    expect(second).not.toBe(first);
    expect(state()).toBe("reconnecting");
    second.lose(CLOSED);
    expect(onClosed).toHaveBeenCalledTimes(2);
    vi.advanceTimersByTime(10_000);
    expect(state()).toBe("reconnecting");

    const third = connect(onClosed);
    vi.advanceTimersByTime(10_000);
    expect(status.getSnapshot()).toEqual({ state: "offline", lostAt: at(0), overdue: false });
    expect(invalidate).not.toHaveBeenCalled();

    third.open();
    expect(state()).toBe("live");
    expect(invalidate).toHaveBeenCalledTimes(1);
    expect(invalidate).toHaveBeenCalledWith();
  });

  test("an error while the stream is still open is no loss", () => {
    const source = connect();
    source.open();
    status.failed(OPEN);
    vi.advanceTimersByTime(OFFLINE_AFTER_MS);
    expect(state()).toBe("live");
    source.lose();
    vi.advanceTimersByTime(RECONNECTING_AFTER_MS);
    expect(state()).toBe("reconnecting");
  });

  test("stop clears the timers, and start begins at connecting again", () => {
    status.stop();
    vi.advanceTimersByTime(OFFLINE_AFTER_MS * 2);
    expect(status.getSnapshot()).toEqual({ state: "connecting", lostAt: at(0), overdue: false });

    status.start();
    expect(status.opened()).toBe(false);
    expect(status.opened()).toBe(true);
    status.failed(CONNECTING);
    vi.advanceTimersByTime(RECONNECTING_AFTER_MS);
    expect(state()).toBe("reconnecting");

    // A new start, as when the shell is shown again: connecting, and the next open is the first one.
    status.start();
    expect(status.getSnapshot()).toEqual({
      state: "connecting",
      lostAt: at(OFFLINE_AFTER_MS * 2 + RECONNECTING_AFTER_MS),
      overdue: false,
    });
    expect(status.opened()).toBe(false);
    expect(state()).toBe("live");
  });

  test("stop goes back to connecting from any state, so that a mark shown again shows nothing old", () => {
    const listener = vi.fn();
    status.subscribe(listener);
    status.opened();
    status.failed(CONNECTING);
    vi.advanceTimersByTime(OFFLINE_AFTER_MS);
    expect(status.getSnapshot()).toEqual({ state: "offline", lostAt: at(0), overdue: false });
    listener.mockClear();

    status.stop();
    expect(listener).toHaveBeenCalledTimes(1);
    expect(status.getSnapshot()).toEqual({
      state: "connecting",
      lostAt: at(OFFLINE_AFTER_MS),
      overdue: false,
    });

    // Overdue is cleared too, and no timer is left to change it afterwards.
    status.start();
    vi.advanceTimersByTime(RECONNECTING_AFTER_MS);
    expect(status.getSnapshot().overdue).toBe(true);
    status.stop();
    expect(status.getSnapshot().overdue).toBe(false);
    listener.mockClear();
    vi.advanceTimersByTime(OFFLINE_AFTER_MS * 2);
    expect(listener).not.toHaveBeenCalled();
    expect(state()).toBe("connecting");
  });

  test("subscribers hear every change until they unsubscribe", () => {
    const listener = vi.fn();
    const unsubscribe = status.subscribe(listener);
    status.opened();
    expect(listener).toHaveBeenCalledTimes(1);
    status.opened();
    expect(listener).toHaveBeenCalledTimes(1);
    unsubscribe();
    status.failed(CLOSED);
    vi.advanceTimersByTime(OFFLINE_AFTER_MS);
    expect(state()).toBe("offline");
    expect(listener).toHaveBeenCalledTimes(1);
  });
});

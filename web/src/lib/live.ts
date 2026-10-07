// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Live hints (ADR-0009): one EventSource on the live-updates stream. A hint {type, id} names what changed and
// invalidates the queries registered for its type, so the SPA reads the change through the API again; a page that must
// decide itself what a hint changes, such as the Alert Group list that holds its rows still, listens to it instead.
// After a reconnect every query is invalidated, as hints may have been missed. When the session ends, the server closes the
// stream and the browser's reconnect gets 401, which closes the EventSource for good.

import type { QueryClient, QueryKey } from "@tanstack/react-query";

import {
  getGetAlertGroupCountsQueryKey,
  getGetAlertGroupQueryKey,
  getGetAlertGroupTimelineQueryKey,
  getListAlertGroupAlertsQueryKey,
  getListRelatedAlertGroupsQueryKey,
} from "../api/gen/endpoints/alert-groups/alert-groups";
import {
  getGetIntegrationQueryKey,
  getListIntegrationTokensQueryKey,
  getListIntegrationsQueryKey,
} from "../api/gen/endpoints/integrations/integrations";
import { getGetOrganizationQueryKey } from "../api/gen/endpoints/organization/organization";
import {
  getGetRouteQueryKey,
  getListRouteSuggestionsQueryKey,
  getListRoutesQueryKey,
} from "../api/gen/endpoints/routes/routes";
import { getListSystemNoticesQueryKey } from "../api/gen/endpoints/system/system";
import { HintEventType, type HintEvent } from "../api/gen/model";

const HINT_TYPES: readonly HintEventType[] = Object.values(HintEventType);

export const LIVE_UPDATES_URL = "/api/v1/live-updates";

/** The queries a hint invalidates; id is the public_id of the changed entity or null for a whole collection. */
export type HintQueries = (id: string | null) => QueryKey[];

const registry = new Map<HintEventType, HintQueries[]>();

/** Registers the queries a hint type invalidates; later pages add their own. Returns the function that removes it. */
export function registerHint(type: HintEventType, queries: HintQueries): () => void {
  const list = registry.get(type) ?? [];
  list.push(queries);
  registry.set(type, list);
  return () => {
    const current = registry.get(type) ?? [];
    registry.set(
      type,
      current.filter((q) => q !== queries),
    );
  };
}

registerHint("system-notices", () => [getListSystemNoticesQueryKey()]);
registerHint("organization", () => [getGetOrganizationQueryKey()]);
// An Integration or its tokens changed: the list, and the Integration's page with its tokens; a Heartbeat turned on
// or off changes the Route suggestions.
registerHint("integration", (id) =>
  id === null
    ? [getListIntegrationsQueryKey(), getListRouteSuggestionsQueryKey()]
    : [
        getListIntegrationsQueryKey(),
        getGetIntegrationQueryKey(id),
        getListIntegrationTokensQueryKey(id),
        getListRouteSuggestionsQueryKey(),
      ],
);
// A Route changed, or the order of all of them: the list with its order, the suggestions it may answer, and an open
// editor, which then says that the Route was changed elsewhere.
registerHint("route", (id) =>
  id === null
    ? [getListRoutesQueryKey(), getListRouteSuggestionsQueryKey()]
    : [getListRoutesQueryKey(), getListRouteSuggestionsQueryKey(), getGetRouteQueryKey(id)],
);

// An Alert Group changed: the counts of the list, and its page with its Alerts, Timeline and previous Alert Groups.
// The rows of the list follow it through a listener (onHint), so that the list does not move.
registerHint("alert-group", (id) =>
  id === null
    ? [getGetAlertGroupCountsQueryKey()]
    : [
        getGetAlertGroupCountsQueryKey(),
        getGetAlertGroupQueryKey(id),
        getListAlertGroupAlertsQueryKey(id),
        getGetAlertGroupTimelineQueryKey(id),
        getListRelatedAlertGroupsQueryKey(id),
      ],
);
// New Alert Groups may match: the counts; the list announces them as "N new" through a listener.
registerHint("alert-groups", () => [getGetAlertGroupCountsQueryKey()]);

/** What a page does with a hint itself; id is as in HintQueries. */
export type HintListener = (id: string | null) => void;

const listeners = new Map<HintEventType, Set<HintListener>>();

/** Calls the listener with every hint of the type from now on. Returns the function that stops it. */
export function onHint(type: HintEventType, listener: HintListener): () => void {
  const set = listeners.get(type) ?? new Set<HintListener>();
  set.add(listener);
  listeners.set(type, set);
  return () => {
    set.delete(listener);
  };
}

/** Invalidates the queries registered for a hint and calls its listeners. */
export function applyHint(queryClient: QueryClient, hint: HintEvent): void {
  for (const queries of registry.get(hint.type) ?? []) {
    for (const queryKey of queries(hint.id)) {
      void queryClient.invalidateQueries({ queryKey });
    }
  }
  for (const listener of listeners.get(hint.type) ?? []) {
    listener(hint.id);
  }
}

function parseHint(data: unknown): HintEvent | undefined {
  if (typeof data !== "string") {
    return undefined;
  }
  try {
    const value: unknown = JSON.parse(data);
    if (
      typeof value !== "object" ||
      value === null ||
      !("type" in value) ||
      typeof value.type !== "string"
    ) {
      return undefined;
    }
    const type = HINT_TYPES.find((t) => t === value.type);
    const id = "id" in value && typeof value.id === "string" ? value.id : null;
    return type === undefined ? undefined : { type, id };
  } catch {
    // A malformed event is ignored.
  }
  return undefined;
}

/** What the stream needs of an EventSource, so that tests can drive a stand-in. */
export interface EventSourceLike {
  readonly readyState: number;
  addEventListener(type: string, listener: (event: Event) => void): void;
  close(): void;
}

export interface LiveOptions {
  /** Called once the stream is closed for good, which happens when the session ended. */
  onClosed: () => void;
  /** The EventSource constructor, replaced in tests. */
  eventSource?: new (url: string) => EventSourceLike;
}

/** Opens the stream and returns the function that closes it. */
export function connectLiveUpdates(queryClient: QueryClient, options: LiveOptions): () => void {
  const Source = options.eventSource ?? EventSource;
  const source = new Source(LIVE_UPDATES_URL);
  let opened = false;
  let closed = false;
  source.addEventListener("hint", (event) => {
    const hint = parseHint(event instanceof MessageEvent ? event.data : undefined);
    if (hint !== undefined) {
      applyHint(queryClient, hint);
    }
  });
  source.addEventListener("open", () => {
    if (opened) {
      void queryClient.invalidateQueries();
    }
    opened = true;
  });
  source.addEventListener("error", () => {
    if (source.readyState === EventSource.CLOSED && !closed) {
      closed = true;
      options.onClosed();
    }
  });
  return () => {
    closed = true;
    source.close();
  };
}

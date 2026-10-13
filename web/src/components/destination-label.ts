// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The label of a Destination across the SPA (D309): its Connection and its channel — Mattermost "#<channel name>",
// Telegram the channel title or the channel as entered —, or the host of an outgoing webhook's URL. The full label
// "<name> · <messenger> · via <Connection> · <channel>" names a Destination in the Route editor, its pickers and the
// tooltips of the Alert Group page. The chat a Destination posts in tells the Route editor when two Destinations would
// post the same Alert Group twice into one chat (D305): one Telegram chat id, or one Mattermost server and channel.

import type { TFunction } from "i18next";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";

import { useListConnections } from "../api/gen/endpoints/connections/connections";
import { useListDestinations } from "../api/gen/endpoints/destinations/destinations";
import type { Connection, Destination, WebhookDestination } from "../api/gen/model";
import { useCan } from "./app-shell";
import { destinationTypeName } from "./destination-health";

/**
 * The host (hostname and port) of an outgoing webhook's URL — `events.url`, or `template.create.url` in the mode
 * `template` — when the text before its first `{{` is an http(s) URL whose authority is complete literal text. Never
 * user information, path or query; undefined when the host comes from a template or the URL does not parse.
 */
export function webhookHost(
  d: Pick<WebhookDestination, "mode" | "events" | "template">,
): string | undefined {
  const url = (d.mode === "template" ? d.template?.create.url : d.events?.url)?.trim();
  if (url === undefined || url === "") {
    return undefined;
  }
  const brace = url.indexOf("{{");
  const literal = brace === -1 ? url : url.slice(0, brace);
  // With a template in the URL, the authority must end before it: a "/", "?" or "#" follows "://".
  if (brace !== -1 && !/^[a-z][a-z0-9+.-]*:\/\/[^/?#]*[/?#]/i.test(literal)) {
    return undefined;
  }
  try {
    const parsed = new URL(literal);
    if ((parsed.protocol !== "http:" && parsed.protocol !== "https:") || parsed.hostname === "") {
      return undefined;
    }
    return parsed.host;
  } catch {
    return undefined;
  }
}

/** Where a Destination posts: "#<channel name>" (or the channel id), the Telegram channel, or the webhook's host. */
export function destinationPlace(d: Destination): string | undefined {
  switch (d.type) {
    case "mattermost":
      return d.channel_name ? `#${d.channel_name}` : d.channel_id;
    case "telegram":
      return d.channel_title || d.channel_id;
    default:
      return webhookHost(d);
  }
}

function connectionOf(
  d: Destination,
  connections: ReadonlyMap<string, Connection>,
): Connection | undefined {
  return d.type === "webhook" ? undefined : connections.get(d.connection_id);
}

/** "<Connection> · <channel>" of the Destinations list; an outgoing webhook shows its host only. */
export function connectionPlace(
  d: Destination,
  connections: ReadonlyMap<string, Connection>,
): string | undefined {
  const parts = [connectionOf(d, connections)?.name, destinationPlace(d)].filter(
    (p): p is string => p !== undefined && p !== "",
  );
  return parts.length > 0 ? parts.join(" · ") : undefined;
}

/** The parts of the full label: name, messenger, "via <Connection>" and the channel; missing parts are left out. */
export function labelParts(
  t: TFunction,
  d: Destination,
  connections: ReadonlyMap<string, Connection>,
): string[] {
  const connection = connectionOf(d, connections);
  return [
    d.name,
    destinationTypeName(t, d.type),
    connection === undefined
      ? undefined
      : t("destinations.label.via", { connection: connection.name }),
    destinationPlace(d),
  ].filter((p): p is string => p !== undefined && p !== "");
}

/** "<name> · <messenger> · via <Connection> · <channel>", or "<name> · Outgoing webhook · <host>". */
export function fullLabel(
  t: TFunction,
  d: Destination,
  connections: ReadonlyMap<string, Connection>,
): string {
  return labelParts(t, d, connections).join(" · ");
}

/** A Mattermost server URL as one key: scheme and host lower-cased by URL, the path kept without its trailing "/". */
export function normalizedServer(serverUrl: string): string {
  try {
    const u = new URL(serverUrl.trim());
    return `${u.protocol}//${u.host}${u.pathname.replace(/\/+$/, "")}`;
  } catch {
    return serverUrl.trim().replace(/\/+$/, "");
  }
}

/**
 * The chat a Destination posts in: Telegram its numeric chat id once a check found it, else the channel as entered,
 * lower-cased; Mattermost its Connection's server and the channel id. An outgoing webhook has none.
 */
export function chatKey(
  d: Destination,
  connections: ReadonlyMap<string, Connection>,
): string | undefined {
  switch (d.type) {
    case "telegram":
      return `tg:${d.channel_chat_id ?? d.channel_id.trim().toLowerCase()}`;
    case "mattermost": {
      const connection = connections.get(d.connection_id);
      // Without its Connection, only Destinations of that one Connection are known to share a server.
      const server =
        connection?.type === "mattermost"
          ? normalizedServer(connection.server_url)
          : `connection:${d.connection_id}`;
      return `mm:${server}:${d.channel_id}`;
    }
    default:
      return undefined;
  }
}

/** The names of the Destinations of each chat that two or more of them post in, in the order given. */
export function duplicateChatGroups(
  destinations: readonly Destination[],
  connections: ReadonlyMap<string, Connection>,
): string[][] {
  const byChat = new Map<string, string[]>();
  for (const d of destinations) {
    const key = chatKey(d, connections);
    if (key !== undefined) {
      byChat.set(key, [...(byChat.get(key) ?? []), d.name]);
    }
  }
  return [...byChat.values()].filter((names) => names.length > 1);
}

/** The Connections by public_id, from the same query as the Destination form; empty without connections:read. */
export function useConnectionsById(): ReadonlyMap<string, Connection> {
  const canRead = useCan("connections:read");
  const list = useListConnections({ limit: 500 }, { query: { enabled: canRead } });
  return useMemo(() => new Map((list.data?.items ?? []).map((c) => [c.id, c])), [list.data]);
}

export interface DestinationLabel {
  destination: Destination;
  connection?: Connection;
  /** The full label. */
  full: string;
  /** The label after the name: "<messenger> · via <Connection> · <channel>". */
  rest: string;
}

/**
 * The labels of every Destination by public_id, from the same queries as the Route editor (page size 500, the
 * maximum). Without destinations:read the map is empty and callers show the name alone.
 */
export function useDestinationLabels(): ReadonlyMap<string, DestinationLabel> {
  const { t } = useTranslation();
  const canRead = useCan("destinations:read");
  const list = useListDestinations({ limit: 500 }, { query: { enabled: canRead } });
  const connections = useConnectionsById();
  return useMemo(
    () =>
      new Map(
        (list.data?.items ?? []).map((d) => {
          const parts = labelParts(t, d, connections);
          return [
            d.id,
            {
              destination: d,
              connection: connectionOf(d, connections),
              full: parts.join(" · "),
              rest: parts.slice(1).join(" · "),
            },
          ];
        }),
      ),
    [list.data, connections, t],
  );
}

// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The links of the web UI to the documentation site (C-21.FR-11). The site's address is a build constant, named here
// and nowhere else in the SPA; the version segment comes from the session, which the server derives from the build it
// runs ("X.Y" for a release, "latest" for any other version); the pages and anchors are the entries of
// docs-links.json (C-21.FR-12), which make docs-check checks against the built site.

import type { ConnectionType, Destination, Session, WebhookDestination } from "../api/gen/model";
import { useSession } from "./api";
import links from "./docs-links.json";

/** The documentation site, without a trailing slash. */
export const DOCS_SITE = "https://muster-io.github.io/muster";

/** The version segment the links take before the session has been read. */
export const DOCS_LATEST = "latest";

/** A key of docs-links.json; an unknown key does not compile. */
export type DocsKey = keyof typeof links;

const VERSION = /^(latest|[0-9]+\.[0-9]+)$/;

/** The version segment of a session: its docs_version, or "latest" before the session has been read. */
export function docsVersion(session: Pick<Session, "docs_version"> | null | undefined): string {
  const version = session?.docs_version;
  return version !== undefined && VERSION.test(version) ? version : DOCS_LATEST;
}

/**
 * The address of an entry of docs-links.json in a version of the site: "<path>.md#<anchor>" becomes
 * "<site>/<version>/<path>/#<anchor>", an index.md its directory, the anchor kept as written.
 */
export function docsUrl(key: DocsKey, version: string = DOCS_LATEST): string {
  const target: string = links[key];
  const hash = target.indexOf("#");
  const file = hash < 0 ? target : target.slice(0, hash);
  const anchor = hash < 0 ? "" : target.slice(hash);
  const page = file.replace(/\.md$/, "");
  const path =
    page === "index" ? "" : page.endsWith("/index") ? page.slice(0, -"index".length) : `${page}/`;
  return `${DOCS_SITE}/${docsVersion({ docs_version: version })}/${path}${anchor}`;
}

/** The guide of a Connection's page: the page of its messenger. */
export function connectionDocs(type: ConnectionType): `page.connection.${ConnectionType}` {
  return `page.connection.${type}`;
}

/**
 * The guide of a Destination's page: the section of its messenger, or for an outgoing webhook the page of its mode;
 * the mode both, which sends the template requests too, takes the template page, which describes it.
 */
export function destinationDocs(
  destination: Pick<Destination, "type"> & Partial<Pick<WebhookDestination, "mode">>,
): Extract<DocsKey, `page.destination.${string}`> {
  if (destination.type === "webhook") {
    return destination.mode === "events"
      ? "page.destination.webhook_events"
      : "page.destination.webhook_template";
  }
  return `page.destination.${destination.type}`;
}

/** The documentation version of the current session, as the root route last read it. */
export function useDocsVersion(): string {
  return docsVersion(useSession());
}

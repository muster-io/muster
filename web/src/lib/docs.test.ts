// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The documentation URLs of the web UI (C-21.FR-11): the site's address, the session's docs_version ("latest" until
// the session has been read, and for anything that is not a version segment) and an entry of docs-links.json; a page
// becomes its directory with a trailing slash, an index.md the directory it is in, and the anchor is kept as written.

import { describe, expect, test } from "vitest";

import { DOCS_SITE, type DocsKey, docsUrl, docsVersion } from "./docs";
import links from "./docs-links.json";

describe("docsVersion", () => {
  test.each([
    [null, "latest"],
    [undefined, "latest"],
    [{ docs_version: "latest" }, "latest"],
    [{ docs_version: "0.1" }, "0.1"],
    [{ docs_version: "1.12" }, "1.12"],
    [{ docs_version: "0.1.0" }, "latest"],
    [{ docs_version: "../evil" }, "latest"],
    [{ docs_version: "" }, "latest"],
  ])("the session %j gives %s", (session, want) => {
    expect(docsVersion(session)).toBe(want);
  });
});

describe("docsUrl", () => {
  test("the site is the build constant", () => {
    expect(DOCS_SITE).toBe("https://muster-io.github.io/muster");
  });

  test("a page becomes its directory under the version", () => {
    expect(docsUrl("page.alert_groups")).toBe(
      "https://muster-io.github.io/muster/latest/concepts/alert-groups/",
    );
    expect(docsUrl("page.routes", "latest")).toBe(
      "https://muster-io.github.io/muster/latest/concepts/routes-and-group-keys/",
    );
  });

  test("an index.md becomes its directory and the anchor is kept", () => {
    expect(docsUrl("page.connections")).toBe(
      "https://muster-io.github.io/muster/latest/messengers/",
    );
    expect(docsUrl("page.destinations")).toBe(
      "https://muster-io.github.io/muster/latest/messengers/#what-each-destination-type-can-do",
    );
    expect(docsUrl("page.destination.slack")).toBe(
      "https://muster-io.github.io/muster/latest/messengers/slack/#invite-the-bot-and-create-destinations",
    );
  });

  test("a release session links under its major.minor", () => {
    const version = docsVersion({ docs_version: "0.1" });
    expect(docsUrl("page.routes", version)).toBe(
      "https://muster-io.github.io/muster/0.1/concepts/routes-and-group-keys/",
    );
    expect(docsUrl("page.connection.telegram", version)).toBe(
      "https://muster-io.github.io/muster/0.1/messengers/telegram/",
    );
  });

  test("a version that is not a segment falls back to latest", () => {
    expect(docsUrl("page.routes", "https://elsewhere.example")).toBe(
      "https://muster-io.github.io/muster/latest/concepts/routes-and-group-keys/",
    );
  });

  test("every entry is a page of docs/ with an optional anchor, and the list is sorted by key", () => {
    const keys = Object.keys(links);
    expect(keys).toEqual(keys.toSorted());
    for (const [key, target] of Object.entries(links)) {
      // A runbook page is named after its Internal alert, such as operations/runbooks/MusterTemplateError.md.
      expect(target, key).toMatch(/^[a-z0-9-]+(\/[A-Za-z0-9-]+)*\.md(#[a-z0-9-]+)?$/);
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the keys of the JSON are its DocsKeys
      const url = docsUrl(key as DocsKey);
      const parsed = new URL(url);
      const site = new URL(DOCS_SITE);
      expect(parsed.origin, url).toBe(site.origin);
      expect(parsed.pathname.startsWith(`${site.pathname}/latest/`), url).toBe(true);
      expect(url, key).not.toContain(".md");
    }
  });
});

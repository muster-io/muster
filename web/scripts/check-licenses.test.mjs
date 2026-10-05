// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { describe, it } from "node:test";

import {
  TOOLING_DATA_ONLY,
  TOOLING_OSI,
  TOOLING_REVIEW,
  checkLicenses,
  checkManifest,
  classifyTooling,
  formatGoTooling,
  formatResult,
  isShippable,
  parseExpression,
  readGoReport,
  readLicenseList,
} from "./check-licenses.mjs";

// An entry of `pnpm licenses list --json` without install paths, so that pnpm's own licence value is read.
const entry = (name, license, ...versions) => ({ name, versions, paths: [], license });

describe("isShippable", () => {
  const cases = [
    ["MIT", true],
    ["MPL-2.0 OR Apache-2.0", true],
    ["MIT OR GPL-3.0", true],
    ["(MIT OR GPL-2.0) AND BSD-3-Clause", true],
    ["(MIT OR CC0-1.0)", true],
    // An exception only grants permissions beyond its licence, so the expression is as acceptable as Apache-2.0.
    ["Apache-2.0 WITH LLVM-exception", true],
    // Operators and ids match case-insensitively.
    ["mit or gpl-3.0", true],
    ["Mit And Isc", true],
    // AND binds tighter than OR.
    ["MIT OR GPL-3.0 AND BSD-3-Clause", true],
    ["GPL-3.0 OR MIT AND GPL-2.0", false],
    ["GPL-3.0-or-later", false],
    ["GPL-2.0-only", false],
    ["GPL-2.0+", false],
    ["LGPL-2.1-or-later WITH Classpath-exception-2.0", false],
    ["MIT AND GPL-3.0", false],
    ["(MIT OR GPL-2.0) AND AGPL-3.0-only", false],
    ["CC-BY-4.0", false],
    ["SEE LICENSE IN LICENSE.txt", false],
    ["UNLICENSED", false],
    ["Unknown", false],
    ["LicenseRef-Proprietary", false],
    ["", false],
    ["MIT OR", false],
    ["(MIT", false],
    ["MIT Apache-2.0", false],
    ["MIT/Apache-2.0", false],
    ["(MIT OR Apache-2.0) WITH LLVM-exception", false],
  ];
  for (const [license, want] of cases) {
    it(`${JSON.stringify(license)} is ${want ? "allowed" : "denied"}`, () => {
      assert.equal(isShippable(license), want);
    });
  }

  it("denies legacy and missing forms that are not a string", () => {
    assert.equal(isShippable(["MIT"]), false);
    assert.equal(isShippable([{ type: "MIT" }, { type: "Apache-2.0" }]), false);
    assert.equal(isShippable({ type: "MIT", url: "https://example.org/license" }), false);
    assert.equal(isShippable(undefined), false);
    assert.equal(isShippable(null), false);
  });
});

describe("parseExpression", () => {
  it("builds a tree with WITH tighter than AND, and AND tighter than OR", () => {
    assert.deepEqual(parseExpression("MIT or Apache-2.0 WITH LLVM-exception AND GPL-2.0+"), {
      op: "OR",
      left: { id: "MIT", plus: false },
      right: {
        op: "AND",
        left: { id: "Apache-2.0", plus: false, exception: "LLVM-exception" },
        right: { id: "GPL-2.0", plus: true },
      },
    });
  });

  it("reads a LicenseRef", () => {
    assert.deepEqual(parseExpression("DocumentRef-spdx:LicenseRef-Custom"), {
      ref: "DocumentRef-spdx:LicenseRef-Custom",
    });
  });

  it("rejects what is not an expression", () => {
    for (const bad of [
      "SEE LICENSE IN LICENSE",
      "",
      "AND",
      "(MIT",
      "MIT)",
      "MIT WITH",
      "MIT WITH OR",
      ["MIT"],
    ]) {
      assert.throws(() => parseExpression(bad), SyntaxError, JSON.stringify(bad));
    }
  });
});

describe("classifyTooling", () => {
  const cases = [
    ["vite", "MIT", null],
    ["lightningcss", "MPL-2.0", null],
    ["argparse", "Python-2.0", TOOLING_OSI],
    ["elkjs", "EPL-2.0 OR GPL-3.0-or-later", TOOLING_OSI],
    ["some-tool", "GPL-3.0-only", TOOLING_OSI],
    ["some-tool", "BlueOak-1.0.0", TOOLING_OSI],
    ["some-tool", "MIT AND Zlib", TOOLING_OSI],
    ["caniuse-lite", "CC-BY-4.0", TOOLING_DATA_ONLY],
    ["some-code", "CC-BY-4.0", TOOLING_REVIEW],
    ["some-tool", "BUSL-1.1", TOOLING_REVIEW],
    ["some-tool", "SSPL-1.0", TOOLING_REVIEW],
    ["some-tool", "WTFPL", TOOLING_REVIEW],
    ["some-tool", "Unknown", TOOLING_REVIEW],
    ["some-tool", "SEE LICENSE IN LICENSE", TOOLING_REVIEW],
  ];
  for (const [name, license, want] of cases) {
    it(`${name} under ${JSON.stringify(license)} is ${want ?? "not reported"}`, () => {
      assert.equal(classifyTooling(name, license), want);
    });
  }
});

describe("checkLicenses", () => {
  const production = readLicenseList({
    MIT: [entry("react", "MIT", "19.3.0")],
    "GPL-2.0-or-later": [entry("ckeditor5", "GPL-2.0-or-later", "47.0.0")],
    "(MPL-2.0 OR Apache-2.0)": [entry("dompurify", "(MPL-2.0 OR Apache-2.0)", "3.3.0")],
  });
  const installed = readLicenseList({
    MIT: [entry("react", "MIT", "19.3.0"), entry("vite", "MIT", "8.3.2")],
    "GPL-2.0-or-later": [entry("ckeditor5", "GPL-2.0-or-later", "47.0.0")],
    "(MPL-2.0 OR Apache-2.0)": [entry("dompurify", "(MPL-2.0 OR Apache-2.0)", "3.3.0")],
    "Python-2.0": [entry("argparse", "Python-2.0", "2.0.1")],
    "CC-BY-4.0": [entry("caniuse-lite", "CC-BY-4.0", "1.0.30001814")],
    "SEE LICENSE IN LICENSE": [entry("odd-tool", "SEE LICENSE IN LICENSE", "1.0.0", "2.0.0")],
  });

  it("flattens one entry per installed version", () => {
    assert.equal(installed.length, 8);
    assert.deepEqual(
      installed.filter((p) => p.name === "odd-tool").map((p) => p.version),
      ["1.0.0", "2.0.0"],
    );
  });

  it("fails shipped packages and reports tooling outside the shipped list", () => {
    const result = checkLicenses(production, installed);
    assert.equal(result.shipped, 3);
    assert.equal(result.tooling, 5);
    assert.deepEqual(formatResult(result), [
      "ckeditor5@47.0.0 GPL-2.0-or-later is not allowed for shipped artifacts",
      "argparse@2.0.1 Python-2.0: OSI-approved (tooling)",
      "caniuse-lite@1.0.30001814 CC-BY-4.0: CC-BY-4.0, data-only package (tooling)",
      "odd-tool@1.0.0 SEE LICENSE IN LICENSE: needs review: not OSI-approved",
      "odd-tool@2.0.0 SEE LICENSE IN LICENSE: needs review: not OSI-approved",
      "shipped: 3 packages, 1 not allowed; tooling: 5 packages, 4 outside the shipped list " +
        "(1 OSI-approved, 1 CC-BY-4.0 data-only, 2 needing review)",
    ]);
  });

  it("reads the licence each package declares, not the one pnpm guesses", () => {
    const manifests = {
      "/store/ckeditor5": { version: "48.5.2", license: "SEE LICENSE IN LICENSE.md" },
      "/store/none": { version: "0.1.0" },
      "/store/old-array": { version: "1.0.0", licenses: [{ type: "MIT" }] },
      "/store/old-object": { version: "2.0.0", license: { type: "MIT" } },
      "/store/react": { version: "19.3.0", license: "MIT" },
    };
    const at = (name, license, path) => ({
      name,
      versions: [manifests[path].version],
      paths: [path],
      license,
    });
    const list = readLicenseList(
      {
        "ISC OR MIT": [at("ckeditor5", "ISC OR MIT", "/store/ckeditor5")],
        MIT: [
          at("old-array", "MIT", "/store/old-array"),
          at("old-object", "MIT", "/store/old-object"),
          at("react", "MIT", "/store/react"),
        ],
        Unknown: [at("none", "Unknown", "/store/none")],
      },
      (path) => manifests[path],
    );
    assert.deepEqual(formatResult(checkLicenses(list, list)).slice(0, -1), [
      "ckeditor5@48.5.2 SEE LICENSE IN LICENSE.md is not allowed for shipped artifacts",
      "none@0.1.0 (no licence) is not allowed for shipped artifacts",
      'old-array@1.0.0 [{"type":"MIT"}] is not allowed for shipped artifacts',
      'old-object@2.0.0 {"type":"MIT"} is not allowed for shipped artifacts',
    ]);
  });

  it("passes when every shipped package is allowed", () => {
    const allowed = production.filter((p) => p.name !== "ckeditor5");
    assert.deepEqual(checkLicenses(allowed, allowed).violations, []);
  });
});

describe("checkManifest", () => {
  it("fails optionalDependencies, which pnpm licenses list --prod leaves out", () => {
    const manifest = {
      dependencies: { react: "19.3.0" },
      optionalDependencies: { flickity: "3.0.0", fsevents: "2.3.3" },
    };
    assert.deepEqual(checkManifest(manifest), [
      "web/package.json declares optionalDependencies (flickity, fsevents), which the licence check cannot see; " +
        "move them to dependencies",
    ]);
  });

  it("passes without optionalDependencies", () => {
    assert.deepEqual(checkManifest({ dependencies: { react: "19.3.0" } }), []);
    assert.deepEqual(checkManifest({ optionalDependencies: {} }), []);
  });

  it("passes the SPA's package.json", () => {
    const manifest = JSON.parse(readFileSync(new URL("../package.json", import.meta.url), "utf8"));
    assert.deepEqual(checkManifest(manifest), []);
  });
});

describe("Go tooling report", () => {
  const csv = [
    "golang.org/x/vuln,https://cs.opensource.google/go/x/vuln/+/v1.8.0:LICENSE,BSD-3-Clause",
    "github.com/golangci/golangci-lint/v2,https://github.com/golangci/golangci-lint/blob/v2.14.0/LICENSE,GPL-3.0",
    "github.com/alecthomas/chroma/v2,https://github.com/alecthomas/chroma/blob/v2.27.0/COPYING,OFL-1.1",
    "github.com/golangci/gofmt,Unknown,Unknown",
    "",
  ].join("\n");

  it("reads one library per line and keeps a library listed by two reports once", () => {
    const libraries = readGoReport(`${csv}\n${csv}`);
    assert.deepEqual(
      libraries.map((p) => `${p.name} ${p.license}`),
      [
        "github.com/alecthomas/chroma/v2 OFL-1.1",
        "github.com/golangci/gofmt Unknown",
        "github.com/golangci/golangci-lint/v2 GPL-3.0",
        "golang.org/x/vuln BSD-3-Clause",
      ],
    );
  });

  it("reports the libraries outside the shipped list", () => {
    assert.deepEqual(formatGoTooling(readGoReport(csv)), [
      "github.com/alecthomas/chroma/v2 OFL-1.1: OSI-approved (tooling)",
      "github.com/golangci/gofmt Unknown: needs review: not OSI-approved",
      "github.com/golangci/golangci-lint/v2 GPL-3.0: OSI-approved (tooling)",
      "Go tooling: 4 libraries, 3 outside the shipped list (2 OSI-approved, 1 needing review)",
    ]);
  });

  it("says none when every library is on the shipped list", () => {
    assert.deepEqual(formatGoTooling(readGoReport(csv.split("\n")[0])), [
      "Go tooling: 1 library, none outside the shipped list",
    ]);
  });
});

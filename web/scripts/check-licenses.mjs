// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Checks the licences of the SPA's npm packages against the dependency policy of ADR-0001. Production dependencies
// end up in the shipped bundle: each must be under an allowed licence, read as an SPDX expression, or the check
// fails. Every other installed package is build tooling: a licence outside the shipped list is reported, never
// failed. `pnpm licenses list --json` says which packages are installed and where; the licence of each is the one
// its own package.json declares. Legacy forms (an object, a `licenses` array), "SEE LICENSE IN ..." and a missing
// licence are not SPDX expressions and never pass.
//
// With --go-tools <csv>..., it classifies the Go build tools instead, from the CSV of `go-licenses report`, by the
// same tooling rules, and only reports.

import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

/** Licences that shipped artifacts may depend on; MPL-2.0 only for files Muster does not modify. */
export const SHIPPED_LICENSES = Object.freeze([
  "MIT",
  "MIT-0",
  "BSD-2-Clause",
  "BSD-3-Clause",
  "Apache-2.0",
  "ISC",
  "0BSD",
  "Unlicense",
  "CC0-1.0",
  "MPL-2.0",
]);

/**
 * OSI-approved licences, which build tooling may use. Ids are listed without the -only and -or-later suffixes,
 * which are ignored when matching.
 */
export const OSI_APPROVED = Object.freeze([
  "0BSD",
  "AAL",
  "AFL-1.1",
  "AFL-1.2",
  "AFL-2.0",
  "AFL-2.1",
  "AFL-3.0",
  "AGPL-3.0",
  "APL-1.0",
  "APSL-1.0",
  "APSL-1.1",
  "APSL-1.2",
  "APSL-2.0",
  "Apache-1.1",
  "Apache-2.0",
  "Artistic-1.0",
  "Artistic-1.0-Perl",
  "Artistic-1.0-cl8",
  "Artistic-2.0",
  "BSD-1-Clause",
  "BSD-2-Clause",
  "BSD-2-Clause-Patent",
  "BSD-3-Clause",
  "BSD-3-Clause-LBNL",
  "BSL-1.0",
  "BlueOak-1.0.0",
  "CAL-1.0",
  "CAL-1.0-Combined-Work-Exception",
  "CATOSL-1.1",
  "CDDL-1.0",
  "CECILL-2.1",
  "CERN-OHL-P-2.0",
  "CERN-OHL-S-2.0",
  "CERN-OHL-W-2.0",
  "CNRI-Python",
  "CPAL-1.0",
  "CPL-1.0",
  "CUA-OPL-1.0",
  "ECL-1.0",
  "ECL-2.0",
  "EFL-1.0",
  "EFL-2.0",
  "EPL-1.0",
  "EPL-2.0",
  "EUDatagrid",
  "EUPL-1.1",
  "EUPL-1.2",
  "Entessa",
  "Fair",
  "Frameworx-1.0",
  "GPL-2.0",
  "GPL-3.0",
  "HPND",
  "ICU",
  "IPA",
  "IPL-1.0",
  "ISC",
  "Intel",
  "Jam",
  "LGPL-2.0",
  "LGPL-2.1",
  "LGPL-3.0",
  "LPL-1.0",
  "LPL-1.02",
  "LPPL-1.3c",
  "LiLiQ-P-1.1",
  "LiLiQ-R-1.1",
  "LiLiQ-Rplus-1.1",
  "MIT",
  "MIT-0",
  "MIT-Modern-Variant",
  "MPL-1.0",
  "MPL-1.1",
  "MPL-2.0",
  "MPL-2.0-no-copyleft-exception",
  "MS-PL",
  "MS-RL",
  "MirOS",
  "Motosoto",
  "MulanPSL-2.0",
  "Multics",
  "NASA-1.3",
  "NCSA",
  "NGPL",
  "NPOSL-3.0",
  "NTP",
  "Naumen",
  "Nokia",
  "OCLC-2.0",
  "OFL-1.1",
  "OFL-1.1-RFN",
  "OFL-1.1-no-RFN",
  "OGTSL",
  "OLDAP-2.8",
  "OLFL-1.3",
  "OSET-PL-2.1",
  "OSL-1.0",
  "OSL-2.0",
  "OSL-2.1",
  "OSL-3.0",
  "PHP-3.0",
  "PHP-3.01",
  "PostgreSQL",
  "Python-2.0",
  "QPL-1.0",
  "QPL-1.0-INRIA-2004",
  "RPL-1.1",
  "RPL-1.5",
  "RPSL-1.0",
  "RSCPL",
  "SISSL",
  "SPL-1.0",
  "SimPL-2.0",
  "Sleepycat",
  "UCL-1.0",
  "UPL-1.0",
  "Unicode-3.0",
  "Unicode-DFS-2016",
  "Unlicense",
  "VSL-1.0",
  "W3C",
  "Watcom-1.0",
  "Xnet",
  "ZPL-2.0",
  "ZPL-2.1",
  "Zlib",
]);

/** Packages that contain only data and no code, which build tooling may also use under CC-BY-4.0. */
export const DATA_ONLY_PACKAGES = Object.freeze(["caniuse-db", "caniuse-lite"]);

export const TOOLING_OSI = "OSI-approved (tooling)";
export const TOOLING_DATA_ONLY = "CC-BY-4.0, data-only package (tooling)";
export const TOOLING_REVIEW = "needs review: not OSI-approved";

const ID = /^[A-Za-z0-9.-]+$/;
const LICENSE_REF = /^(?:DocumentRef-[A-Za-z0-9.-]+:)?LicenseRef-[A-Za-z0-9.-]+$/;
const OPERATORS = new Set(["AND", "OR", "WITH"]);
const isOperator = (token) => token !== undefined && OPERATORS.has(token.toUpperCase());

/**
 * Parses an SPDX licence expression into a tree of `{ op: "AND" | "OR", left, right }` and leaves
 * `{ id, plus, exception? }` or `{ ref }`. Operators are case-insensitive; WITH binds tighter than AND, and AND
 * tighter than OR. Anything else, such as "SEE LICENSE IN LICENSE" or a non-string, throws a SyntaxError.
 */
export function parseExpression(expression) {
  if (typeof expression !== "string") {
    throw new SyntaxError(`licence ${JSON.stringify(expression)} is not an SPDX expression`);
  }
  const tokens = expression.match(/[()]|[^\s()]+/g) ?? [];
  let pos = 0;
  function fail(want) {
    const found = pos < tokens.length ? `"${tokens[pos]}"` : "the end";
    throw new SyntaxError(`${JSON.stringify(expression)}: want ${want}, found ${found}`);
  }
  function accept(token) {
    if (tokens[pos]?.toUpperCase() !== token) {
      return false;
    }
    pos++;
    return true;
  }
  function parseLicense() {
    const token = tokens[pos];
    if (token === undefined || token === "(" || token === ")" || isOperator(token)) {
      fail("a licence id");
    }
    pos++;
    if (LICENSE_REF.test(token)) {
      return { ref: token };
    }
    const plus = token.endsWith("+");
    const id = plus ? token.slice(0, -1) : token;
    if (!ID.test(id)) {
      pos--;
      fail("a licence id");
    }
    return { id, plus };
  }
  function parseSimple() {
    if (accept("(")) {
      const node = parseOr();
      if (!accept(")")) {
        fail('")"');
      }
      return node;
    }
    const license = parseLicense();
    if (!accept("WITH")) {
      return license;
    }
    const exception = tokens[pos];
    if (
      license.ref !== undefined ||
      exception === undefined ||
      isOperator(exception) ||
      !ID.test(exception)
    ) {
      fail("an exception id");
    }
    pos++;
    return { ...license, exception };
  }
  function parseAnd() {
    let node = parseSimple();
    while (accept("AND")) {
      node = { op: "AND", left: node, right: parseSimple() };
    }
    return node;
  }
  function parseOr() {
    let node = parseAnd();
    while (accept("OR")) {
      node = { op: "OR", left: node, right: parseAnd() };
    }
    return node;
  }

  const tree = parseOr();
  if (pos < tokens.length) {
    fail("an operator");
  }
  return tree;
}

const baseId = (id) => id.toLowerCase().replace(/-(?:only|or-later)$/, "");
const idSet = (ids) => new Set(ids.map(baseId));
const SHIPPED_IDS = idSet(SHIPPED_LICENSES);
const TOOLING_IDS = idSet([...OSI_APPROVED, ...SHIPPED_LICENSES]);
const DATA_ONLY_IDS = new Set([...TOOLING_IDS, baseId("CC-BY-4.0")]);

// `A OR B` needs one side, `A AND B` both. Ids match case-insensitively. A `+` or -or-later suffix admits later
// versions as well as the named one, so it counts as the named version. An exception (WITH) only grants permissions
// beyond its licence, so `Apache-2.0 WITH LLVM-exception` is as acceptable as Apache-2.0. A LicenseRef names terms
// outside the SPDX list and never matches.
function satisfies(node, ids) {
  switch (node.op) {
    case "OR":
      return satisfies(node.left, ids) || satisfies(node.right, ids);
    case "AND":
      return satisfies(node.left, ids) && satisfies(node.right, ids);
    default:
      return node.id !== undefined && ids.has(baseId(node.id));
  }
}

function allows(license, ids) {
  try {
    return satisfies(parseExpression(license), ids);
  } catch {
    return false;
  }
}

/** Reports whether a package under this licence may be part of a shipped artifact. */
export function isShippable(license) {
  return allows(license, SHIPPED_IDS);
}

/**
 * Classifies a build-tooling package: null when its licence is on the shipped list, otherwise TOOLING_OSI,
 * TOOLING_DATA_ONLY or TOOLING_REVIEW.
 */
export function classifyTooling(name, license) {
  if (isShippable(license)) {
    return null;
  }
  if (allows(license, TOOLING_IDS)) {
    return TOOLING_OSI;
  }
  if (DATA_ONLY_PACKAGES.includes(name) && allows(license, DATA_ONLY_IDS)) {
    return TOOLING_DATA_ONLY;
  }
  return TOOLING_REVIEW;
}

const packageKey = (p) => `${p.name}@${p.version}`;

/**
 * Flattens the output of `pnpm licenses list --json`, an object from licence to packages with their versions and
 * install paths, into one `{ name, version, license }` per package version, sorted by name and version. The licence
 * is the one declared in the package's package.json, read through readManifest(path): for "SEE LICENSE IN <file>"
 * pnpm reports a licence it guesses from the file, which can be wrong (it read the GPL notice of ckeditor5, which
 * lists the MIT and ISC code it bundles, as "ISC OR MIT"). pnpm's value is used only for an entry without a path.
 */
export function readLicenseList(list, readManifest = readInstalledManifest) {
  const packages = new Map();
  for (const [license, entries] of Object.entries(list)) {
    for (const entry of entries) {
      const found = entry.paths?.length
        ? entry.paths.map((path) => {
            const manifest = readManifest(path);
            return {
              name: entry.name,
              version: manifest.version,
              license: manifest.license ?? manifest.licenses,
            };
          })
        : (entry.versions ?? []).map((version) => ({
            name: entry.name,
            version,
            license: entry.license ?? license,
          }));
      for (const p of found) {
        packages.set(packageKey(p), p);
      }
    }
  }
  return [...packages.values()].toSorted(
    (a, b) => a.name.localeCompare(b.name) || a.version.localeCompare(b.version),
  );
}

function readInstalledManifest(path) {
  return JSON.parse(readFileSync(join(path, "package.json"), "utf8"));
}

/**
 * Checks the shipped packages (production) and reports the tooling ones, which are every installed package that
 * production does not include.
 */
export function checkLicenses(production, installed) {
  const shipped = new Set(production.map(packageKey));
  const tooling = installed.filter((p) => !shipped.has(packageKey(p)));
  return {
    shipped: production.length,
    tooling: tooling.length,
    violations: production.filter((p) => !isShippable(p.license)),
    reported: tooling.flatMap((p) => {
      const category = classifyTooling(p.name, p.license);
      return category === null ? [] : [{ ...p, category }];
    }),
  };
}

/**
 * Checks the SPA's own package.json. `pnpm licenses list --prod` leaves out optionalDependencies, so a package
 * declared there would ship without its licence being checked: shipped code belongs in dependencies.
 */
export function checkManifest(manifest) {
  const optional = Object.keys(manifest.optionalDependencies ?? {});
  return optional.length === 0
    ? []
    : [
        `web/package.json declares optionalDependencies (${optional.join(", ")}), which the licence check ` +
          "cannot see; move them to dependencies",
      ];
}

const licenseText = (license) =>
  typeof license === "string"
    ? license
    : license === undefined
      ? "(no licence)"
      : JSON.stringify(license);

/** Formats the result as report lines; the violations come first. */
export function formatResult(result) {
  const lines = [];
  for (const p of result.violations) {
    lines.push(
      `${p.name}@${p.version} ${licenseText(p.license)} is not allowed for shipped artifacts`,
    );
  }
  for (const p of result.reported) {
    lines.push(`${p.name}@${p.version} ${licenseText(p.license)}: ${p.category}`);
  }
  const count = (category) => result.reported.filter((p) => p.category === category).length;
  lines.push(
    `shipped: ${result.shipped} packages, ${result.violations.length} not allowed; ` +
      `tooling: ${result.tooling} packages, ${result.reported.length} outside the shipped list ` +
      `(${count(TOOLING_OSI)} OSI-approved, ${count(TOOLING_DATA_ONLY)} CC-BY-4.0 data-only, ` +
      `${count(TOOLING_REVIEW)} needing review)`,
  );
  return lines;
}

/**
 * Reads the CSV that `go-licenses report` prints, one `library,licence URL,licence name` line per Go library, into
 * `{ name, license }` sorted by name; a library listed by more than one report is kept once. go-licenses names a
 * licence it cannot identify "Unknown".
 */
export function readGoReport(csv) {
  const libraries = new Map();
  for (const line of csv.split("\n")) {
    const fields = line.trim().split(",");
    if (fields.length >= 3) {
      const library = { name: fields[0], license: fields.at(-1) };
      libraries.set(`${library.name} ${library.license}`, library);
    }
  }
  return [...libraries.values()].toSorted((a, b) => a.name.localeCompare(b.name));
}

/** Formats the report of the Go build tools: the libraries outside the shipped list, then a summary. */
export function formatGoTooling(libraries) {
  const reported = libraries.flatMap((p) => {
    const category = classifyTooling(p.name, p.license);
    return category === null ? [] : [{ ...p, category }];
  });
  const lines = reported.map((p) => `${p.name} ${p.license}: ${p.category}`);
  const count = (category) => reported.filter((p) => p.category === category).length;
  const total = `${libraries.length} ${libraries.length === 1 ? "library" : "libraries"}`;
  lines.push(
    reported.length === 0
      ? `Go tooling: ${total}, none outside the shipped list`
      : `Go tooling: ${total}, ${reported.length} outside the shipped list ` +
          `(${count(TOOLING_OSI)} OSI-approved, ${count(TOOLING_REVIEW)} needing review)`,
  );
  return lines;
}

function pnpmLicenses(...flags) {
  const out = execFileSync("pnpm", ["licenses", "list", "--json", ...flags], {
    cwd: fileURLToPath(new URL("..", import.meta.url)),
    encoding: "utf8",
    maxBuffer: 64 << 20,
    stdio: ["ignore", "pipe", "inherit"],
  });
  return readLicenseList(JSON.parse(out));
}

function checkSPA() {
  const problems = checkManifest(
    JSON.parse(readFileSync(new URL("../package.json", import.meta.url), "utf8")),
  );
  const result = checkLicenses(pnpmLicenses("--prod"), pnpmLicenses());
  for (const line of [...problems, ...formatResult(result)]) {
    console.log(line);
  }
  return problems.length === 0 && result.violations.length === 0;
}

function reportGoTools(files) {
  const csv = files.map((file) => readFileSync(file, "utf8")).join("\n");
  for (const line of formatGoTooling(readGoReport(csv))) {
    console.log(line);
  }
  return true;
}

if (import.meta.main) {
  const [mode, ...files] = process.argv.slice(2);
  const ok = mode === "--go-tools" ? reportGoTools(files) : checkSPA();
  process.exitCode = ok ? 0 : 1;
}

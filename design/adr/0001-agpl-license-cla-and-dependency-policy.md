# 0001. AGPL-3.0-only with a CLA, and a dependency license policy

- Status: Accepted
- Date: 2026-10-02
- Amended: 2026-10-05 — the base layer of the container image (the distroless static image: Debian data packages such
  as `base-files`, `netbase`, `tzdata` and `ca-certificates`) is aggregated with Muster, not linked into it, and is outside
  the list below; its packages and licenses appear in the image SBOM
- Amended: 2026-10-14 — the Bridge protocol specification (`api/bridge.yaml`) and the example bridge are licensed MIT,
  so that bridges derived from them may stay closed; the example is its own Go module, never part of the shipped
  artifacts, and is listed in `NOTICE` ([ADR-0017](0017-bridge-protocol-for-messengers-without-native-support.md))
- Amended: 2026-10-17 — the example bridge moves out of this repository: it lives, with the bridge libraries for Go and
  Python, in repositories of their own under MIT without a CLA, and `NOTICE` names only `api/bridge.yaml` as an MIT
  part of this repository ([ADR-0017](0017-bridge-protocol-for-messengers-without-native-support.md))
- Amended: 2026-10-26 — for L2 (journal D492, D506, D535): the API specification `api/openapi.yaml` becomes MIT as well,
  like `api/bridge.yaml`, with the L2 change of the specification, so that the Terraform provider — MIT, in a repository
  of its own — generates its client from a copy of it; the cost is that anyone may generate a closed server compatible
  with Muster's API from it; until that change, its `info.license`, `api/README.md` and `NOTICE` still say AGPL-3.0-only

## Context

Muster is a self-hosted alert grouping and on-call service published as open source. Two goals shape the license.
First, the code must stay open, including when someone runs a modified Muster as a hosted service for others. Second,
the project wants to keep the option of offering other terms later — a commercial license, or closed add-on modules for
a hosted edition — without having to ask every past contributor. How Muster might make money is deliberately undecided;
the license must not close that door.

The second goal also constrains what code may enter the repository. If Muster contains code it has no right to
relicense — copied from a copyleft project, or pulled in through a dependency whose license conflicts with the product
license — dual licensing becomes impossible, and in the worst case the shipped artifacts cannot be distributed at all.
For reference, the two projects Muster is most often compared with differ here: Grafana OnCall is licensed under
AGPL-3.0, KeepHQ is MIT-licensed outside its enterprise directory.

## Decision

**Product license.** All Muster source code is licensed under **AGPL-3.0-only**, not "-or-later": the project, not a
future revision of the license, decides which terms apply. Every source file carries
`SPDX-License-Identifier: AGPL-3.0-only` and a copyright line, and CI fails if a file lacks them. Third-party files
copied into the repository under a permissive license (for example shadcn/ui components under MIT, kept in
`web/src/components/ui/`) keep their own SPDX header, are excluded from the check by path and are listed in `NOTICE`.

**Contributor License Agreement.** Contributions are accepted under a CLA modelled on the Apache Individual Contributor
License Agreement: the contributor grants the project a broad license to use and relicense the contribution; copyright is
not assigned. The text lives in `CLA.md`. Signing is a one-click step in the pull request, handled by the hosted
**cla-assistant.io** service; a pull request without a signature cannot be merged. If the hosted service shuts down, the
fallback is a fork of the archived CLA Assistant Lite GitHub Action (Apache-2.0) in the project's GitHub organization,
storing signatures in a branch of the repository.

**No copied copyleft code.** Code is never copied from copyleft projects, Grafana OnCall included; such projects may
only be read as a source of ideas. Code from permissively licensed projects (for example the MIT-licensed parts of
KeepHQ) may be ported with attribution in `NOTICE`.

**Specifications under MIT.** The Bridge protocol specification `api/bridge.yaml` (ADR-0017) is licensed MIT, as an
exception to the product license, and the API specification `api/openapi.yaml` becomes MIT with the L2 change of the
specification, which changes its `info.license`, `NOTICE` and `api/README.md` at the same time; until then they say
AGPL-3.0-only. Both are MIT so that bridges, the Terraform provider and any other client generated from them may carry
any license (journal D506). Contributions to them fall under the CLA like any other file, which is what allows the
change. The Terraform provider lives in a repository of its own, `muster-io/terraform-provider-muster`, under MIT
without a CLA, like the bridge libraries: it keeps a copy of the specification that its CI compares with Muster's,
generates its client from that copy, and states which versions of Muster it works with (journal D492, D535).

**Dependencies of shipped artifacts.** Everything Muster distributes — the binary, the container image, the embedded
SPA bundle and the Helm chart — may depend, directly or transitively, only on these licenses:

| License | Condition |
|---|---|
| MIT, MIT-0 | — |
| BSD-2-Clause, BSD-3-Clause | — |
| Apache-2.0 | — |
| ISC | — |
| 0BSD, Unlicense, CC0-1.0 | — |
| MPL-2.0 | only while Muster does not modify the MPL-covered files |

Anything else — GPL, LGPL, AGPL, SSPL, BSL, source-available terms, or no license at all — needs its own ADR before it
is used. The base layer of the container image is the one exception: the distroless static image contains only data files and
configuration from Debian packages, which are aggregated with Muster rather than linked into it; the image SBOM lists
them. CI enforces the list and blocks the merge: `go-licenses` over the Go binary and an equivalent scanner over the
SPA's production dependencies. The scanner must understand SPDX expressions with `OR`; a package licensed
`MPL-2.0 OR Apache-2.0` is used under Apache-2.0.

**Dependencies of build tooling and the documentation site.** Tools that are not distributed with Muster — compilers,
bundlers, linters, test runners, the static site generator and the site's client-side code — may use **any
OSI-approved license**. Packages that contain only data and no code (for example browser-support tables) may also use
**CC-BY-4.0**. CI reports these licenses but does not block. No code from these tools is copied into the repository.

## Consequences

- The network clause of the AGPL means a modified Muster offered as a service must publish its changes; closed hosted
  forks are not possible.
- The CLA and the "no foreign copyleft code" rule keep dual licensing possible. Until monetization is decided, anything
  that might become a paid feature is designed to plug in behind an interface owned by its consumer (key provider,
  login provider, Destination adapters, LLM provider, ingestion queue), so a closed module would be just another
  implementation.
- Contributors face one extra click and a heavier legal text than a Developer Certificate of Origin; some corporate
  contributors will need their employer's approval first.
- CLA checks depend on a third-party hosted service; the fallback is documented above.
- Known tooling dependencies outside the shipped list: Mermaid on the documentation site pulls in `elkjs`
  (`EPL-2.0 OR GPL-3.0-or-later`); Vite and Tailwind use `lightningcss` (MPL-2.0); the bundler's browser-support data
  comes from `caniuse-lite` (CC-BY-4.0, a data-only package). All three are allowed as tooling.
- Adding a dependency is a reviewed decision: the pull request states its license, latest release date and maintenance
  status.
- From L2 anyone may generate a closed server compatible with Muster's API from its MIT specification; what such a
  server would have to rebuild — the behaviour behind the API — stays under the AGPL (journal D535).

## Alternatives considered

- **A permissive license (Apache-2.0 or MIT).** Simplest for adopters, but anyone could run a closed, modified Muster as
  a hosted product, and dual licensing would have nothing to offer.
- **AGPL-3.0-or-later.** Lets future license versions written by a third party apply to the code; "-only" keeps that
  decision with the project.
- **A Developer Certificate of Origin instead of a CLA.** Lighter for contributors, but it grants no right to relicense
  and would rule out dual licensing.
- **A CLA with copyright assignment.** Stronger for the project, but a much higher barrier than a license grant, and not
  needed for the goals above.
- **A self-hosted bot that checks pull request authors against a list of signers collected by a web form**, as some
  large projects run. More infrastructure than a project of this size needs.
- **Applying the strict list to build tooling and the documentation site.** Would exclude common tools (for example
  anything that renders Mermaid diagrams) that are never distributed with Muster.
- **Only OSI-approved licenses for tooling, with no exception for data.** CC-BY-4.0 is not an OSI license, yet it is
  the usual license of browser-support data that every front-end bundler reads; an exception limited to packages
  without code keeps the rule simple and honest.

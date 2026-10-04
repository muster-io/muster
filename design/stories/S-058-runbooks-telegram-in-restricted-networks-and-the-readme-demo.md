---
id: S-058
title: Runbooks, Telegram in restricted networks and the README demo
capability: C-21
kind: docs
layer: L1
depends_on: [S-056, S-057]
covers: [C-21.FR-2, C-21.FR-3, C-21.FR-5, C-21.FR-7, C-21.AC-1, C-21.AC-3, C-19.FR-9, C-19.AC-4, C-14.FR-9, C-01.FR-14]
files_touched:
  - docs/operations/runbooks/index.md
  - docs/operations/runbooks/MusterDown.md
  - docs/operations/runbooks/MusterNoLeader.md
  - docs/operations/runbooks/MusterDeliveryFailing.md
  - docs/operations/runbooks/MusterDestinationBroken.md
  - docs/operations/runbooks/MusterHeartbeatLost.md
  - docs/operations/runbooks/MusterDeliverySlow.md
  - docs/operations/runbooks/MusterDeliveryQueueGrowing.md
  - docs/operations/runbooks/MusterIngestBacklog.md
  - docs/operations/runbooks/MusterIngestRejected.md
  - docs/operations/runbooks/MusterTemplateError.md
  - docs/operations/runbooks/MusterClockSkew.md
  - docs/operations/runbooks/MusterLoginFailures.md
  - docs/operations/runbooks/MusterSnapshotTruncated.md
  - docs/operations/runbooks/MusterOIDCSecretExpiring.md
  - internal/tools/runbookcheck/main.go
  - internal/tools/runbookcheck/runbookcheck_test.go
  - internal/tools/refgen/main.go
  - docs/messengers/telegram-restricted-networks.md
  - docs/messengers/telegram.md
  - web/e2e/demo.spec.ts
  - README.md
  - docs/index.md
  - mkdocs.yml
  - Makefile
acceptance:
  - "[C-21.FR-3, C-21.FR-2, C-21.FR-5] `docs/operations/runbooks/` — the Runbooks section, in English — has exactly one page per name in the union of the Internal alert registry and the chart rules of reference.md — 14 pages — each with the sections Meaning, Impact, Diagnosis and Fix; `make docs-check` fails on a missing, extra or incomplete page."
  - "[C-19.FR-9, C-19.AC-4, C-21.AC-1] The `runbook_url` of every Internal alert points at a page of the built site under its version, and the generated Internal alerts reference links each alert to its runbook."
  - "[C-21.FR-7, C-21.AC-3, C-21.FR-2, C-14.FR-9] \"Telegram in restricted networks\" recommends, in this order, a proxy on the Connection, a reverse proxy under the installation's own domain with nginx and Caddy recipes, and only then a self-hosted Bot API server, and contains every warning of C-21.FR-7, checked against the list in Verification; the Telegram page links to it."
  - "[C-01.FR-14] `make demo` records a short animation of the web UI in `muster dev` — an Alert Group arriving, acknowledged from its page and resolving — and the README and the home page of the site show it."
verify: "make ci docs-check"
operator_attention: true
issue: null
---

# S-058. Runbooks, Telegram in restricted networks and the README demo

## Scope

**IN**

- One runbook page per chart rule and Internal alert, the runbook index and the check that they match one to one.
- "Telegram in restricted networks".
- The README demo animation, planned since S-001.

**OUT**

- The remaining sections — Quick start, Install, the rest of Alertmanager and Sign-in, Concepts, Operations (S-060,
  split from this story).
- The chart rules themselves; S-059 extends the check to the rules the chart renders.

## Contracts

- **Runbook pages** (C-21.FR-3, C-19.FR-9): `docs/operations/runbooks/<alertname>.md` for the twelve chart rules of
  [reference.md](../prd/l1/reference.md#chart-rules) and the Internal alerts of the
  [registry](../prd/l1/reference.md#internal-alerts) — `MusterDestinationBroken`, `MusterHeartbeatLost` and
  `MusterTemplateError` are both and share one page (ADR-0014). Each page has the headings `## Meaning` (the condition,
  the default expression or the raising capability, the labels), `## Impact` (what users notice and what keeps
  working), `## Diagnosis` (the metric, log event, page or `muster doctor` line to look at, in order) and `## Fix`; an
  alert that is both says so and covers both sources. Critical rules repeat that they must reach people through an
  Alertmanager route that bypasses Muster. The index lists every page with severity and source.
- **Runbook check** (C-21.FR-3; `internal/tools/runbookcheck`, in `make docs-check`): the set of names from the Internal
  alert registry and from the Chart rules table of reference.md must equal the set of runbook pages; every page must
  have the four headings; a difference fails with the names. S-059 adds the rules rendered by `helm template` and their
  `runbook_url`s to the same check.
- **Internal alerts reference** (`internal/tools/refgen`): `docs/reference/internal-alerts.md` gains a "Runbook" column
  linking each alert to its page, so the strict build of S-057 checks every link.
- **Version paths** (C-21.AC-1): pages build to `operations/runbooks/<alertname>/` in each published version, which is
  the path of the `runbook_url` of S-053 and S-059.
- **Telegram in restricted networks** (C-21.FR-7, C-14.FR-9; `docs/messengers/telegram-restricted-networks.md`): in this
  order — (1) a proxy on the Connection (HTTP, HTTPS or SOCKS5, C-03.FR-19); (2) a reverse proxy under the
  installation's own domain in front of `api.telegram.org`, with an nginx and a Caddy recipe that set the upstream SNI
  and `Host`, timeouts above the long-polling timeout, the request body size, access logs off or without the request
  URI, and a secret path prefix or an address allowlist, used as the Connection's Bot API base URL; (3) only then a
  self-hosted Bot API server, with every warning of C-21.FR-7, and the manual procedure for moving a bot between
  servers (Muster has no wizard). `docs/messengers/telegram.md` links to it where it mentions restricted networks and
  where it describes the base URL.
- **README demo** (C-01.FR-14; `web/e2e/demo.spec.ts`, `make demo`): a Playwright script, run against `muster dev` at
  1280 × 720, that sends an Alert Group through the fake Alertmanager, opens it from the list, acknowledges it, shows the
  Timeline and lets it resolve, with pauses for the viewer; Playwright records the video and `make demo` converts it with
  ffmpeg into `docs/assets/demo.gif` (at most 30 seconds and 3 MB). The animation is a generated, checked-in asset, not
  a hand-written file; `make demo` is run by hand when the UI changes, never in CI. The README shows it under the first
  paragraph and the home page of the site below its introduction.
- **Navigation** (`mkdocs.yml`): Operations → Runbooks; Messengers → Telegram in restricted networks.

## Steps

1. Write `runbookcheck` and wire it into `make docs-check`. Check: it lists the 14 missing pages.
2. Write the runbook pages and the index, and add the links of the Internal alerts reference. Check: `make docs-check`
   passes.
3. Write "Telegram in restricted networks" and link it. Check: the list in Verification.
4. Write the demo script and record it. Check: `make demo` writes the animation; the README and the home page show it.

## Verification

```sh
make docs-check; echo "exit=$?"                              # exit=0
ls docs/operations/runbooks/Muster*.md | wc -l               # 14
for f in docs/operations/runbooks/Muster*.md; do grep -cE '^## (Meaning|Impact|Diagnosis|Fix)$' $f; done | sort -u   # 4
mv docs/operations/runbooks/MusterClockSkew.md /tmp/
make docs-check 2>&1 | grep -m1 'runbook'                    # missing runbook page: MusterClockSkew
mv /tmp/MusterClockSkew.md docs/operations/runbooks/

# C-19.AC-4, C-21.AC-1: a live Internal alert's runbook_url lands on a built page
make dev > dev.log 2>&1 &   # then raise MusterOIDCSecretExpiring as in S-053
URL=$(curl -s -b jar "localhost:8080/api/v1/integrations/$(curl -s -b jar localhost:8080/api/v1/integrations | jq -r '.items[] | select(.builtin) | .id')/alerts?state=firing" | jq -r '.items[0].annotations.runbook_url')
test -f "site/${URL#https://muster-io.github.io/muster/latest/}index.html" && echo found   # found
grep -c 'operations/runbooks/MusterOIDCSecretExpiring' site/reference/internal-alerts/index.html   # 1

# C-21.FR-7, C-21.AC-3: every warning of the list is on the page
P=docs/messengers/telegram-restricted-networks.md
for w in 'proxy on the Connection' 'reverse proxy' 'nginx' 'Caddy' 'SNI' 'Host' 'long-polling timeout' 'body size' \
         'access log' 'secret path prefix' 'allowlist' 'api_id' 'api_hash' 'dedicated' 'one server at a time' 'logOut' \
         '10 minutes' 'getMe' 'no authentication' 'statistics port' '--local' 'digest' 'MusterDestinationBroken' 'manual'; do
  grep -qi -- "$w" $P || echo "missing: $w"; done            # (no output)
grep -c 'telegram-restricted-networks.md' docs/messengers/telegram.md   # 2

# C-01.FR-14: the demo
make demo && ls -l docs/assets/demo.gif | awk '{print ($5 < 3145728)}'   # 1
grep -c 'docs/assets/demo.gif' README.md                     # 1
```

The pull request carries the checklist of C-21.AC-3: each warning of C-21.FR-7 with the sentence of the page that
covers it.

**Optional measurement** (the operator's test bot): what happens to messages sent before a bot moves to a self-hosted
Bot API server and back, and what Telegram answers during the 10 minutes after `logOut` ([L1 open question
9](../prd/L1.md#51-test-environment-facts)). If measured, the result is added to the facts and the page cites it;
otherwise the page states only the documented behaviour.

## Open questions

None.

## Notes

- Suggested commit: `docs: add runbooks, telegram in restricted networks and the readme demo`.
- Split: the remaining documentation sections moved to S-060, so that this story stays at the pages that other checks
  depend on — the runbooks — plus the two items earlier stories planned here.
- `operator_attention`: the optional measurement of question 9 and a look at the recorded demo before it goes into the
  README.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-21.FR-2 | partial | the Runbooks section and "Telegram in restricted networks"; the remaining sections are S-060 |
| C-21.FR-3 | partial | the pages and the check against the registry and reference.md; the rendered chart rules join with S-059 |
| C-21.FR-5 | partial | the pages of this story |
| C-21.FR-7 | full | |
| C-21.AC-1 | partial | Internal alerts land on their pages; the chart's `runbook_url`s are checked by S-059 |
| C-21.AC-3 | full | |
| C-19.FR-9 | partial | every Internal alert and chart rule has a page; the chart's URLs are S-059 |
| C-19.AC-4 | partial | the pages exist; the rendered rules are checked by S-059 |
| C-14.FR-9 | full | together with S-042: the section on restricted networks |
| C-01.FR-14 | partial | the README demo animation; the rest is S-001 |

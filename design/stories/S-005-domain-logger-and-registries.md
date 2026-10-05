---
id: S-005
title: Domain logger, log event and metric registries (BE)
capability: C-02
kind: be
layer: L1
depends_on: [S-002]
covers: [C-02.FR-17, C-02.FR-18, C-02.FR-19, C-02.AC-12, C-01.FR-12]
files_touched:
  - internal/logging/logger.go
  - internal/logging/events.go
  - internal/logging/logger_test.go
  - internal/metrics/metrics.go
  - internal/metrics/catalogue.go
  - internal/metrics/handler.go
  - internal/metrics/metrics_test.go
  - internal/tools/refgen/main.go
  - internal/tools/refgen/refgen_test.go
  - internal/archlint/secretleak.go
  - internal/archlint/archlint_test.go
  - internal/archlint/testdata/rule5/probes/probes.go
  - Makefile
  - go.mod
acceptance:
  - "[C-02.FR-17, C-02.FR-18, C-02.AC-12] Code that logs an event not declared in the registry does not compile, so `make build` fails; logging a field the event does not declare fails the test that does it."
  - "[C-02.FR-18, C-02.AC-12] `make generate` writes docs/reference/log-events.md with every event's name, level, fields and owning capability; a registry change without regenerating fails `make generate-check` in CI."
  - "[C-02.FR-18] A field of the secret type is written as `[redacted]`; the lint-5 probe that logs a known secret through the domain logger finds it in no output."
  - "[C-02.FR-19, C-02.AC-12] `make generate` writes docs/reference/metrics.md from the metric registry; a registry change without regenerating fails `make generate-check`."
  - "[C-02.FR-19] The registry test fails for a label that is neither an entity label (`integration`, `route`, `destination`) nor declared with a closed value set, other than the labels of an `*_info` gauge, and the registry offers only `le` histograms with explicit buckets."
  - "[C-01.FR-12] The `/metrics` handler writes `muster_build_info{version=\"…\",commit=\"…\"} 1` together with process and Go runtime metrics."
verify: "make ci"
operator_attention: false
issue: 5
---

# S-005. Domain logger, log event and metric registries (BE)

## Scope

**IN**

- The domain logger: structured JSON lines on stdout, written only through registered events.
- The closed log event registry and the metric registry in code (ADR-0014), with generated reference pages and the CI
  check that they are current.
- The `/metrics` handler with `muster_build_info`, process and Go runtime metrics; the listener that serves it is S-006.
- Lints 5, 7 and 8 pointed at the real logger and metrics packages.

**OUT**

- The listeners, `MUSTER_LOG_LEVEL` parsing and the platform metrics that need the database or the Leader:
  database pool (S-006), `muster_leader` and `muster_clock_skew_seconds` (S-008), the `muster_client_*` metrics (S-009).
- The cross-check of the registry against the catalogue of reference.md (C-19.FR-2, S-053).
- The Internal alert registry (C-06, S-021).

## Contracts

- **Domain logger** (C-02.FR-17, ADR-0014): one JSON object per line on stdout with `time` (RFC 3339, UTC), `level`,
  `event` and the event's fields. The level threshold is a parameter of the logger (S-006 feeds it from
  `MUSTER_LOG_LEVEL`). Callers pass an `Event` value and fields; the logger is the only code that imports `log/slog`.
- **Log event registry** (C-02.FR-18): `internal/logging/events.go` is the closed list. Events are created only there,
  through an unexported constructor, so code outside the registry cannot make one — an unregistered event is a compile
  error. Each event has a snake_case name, a level with the meaning of C-02.FR-18 (ERROR — someone must look within the
  hour; WARN — needed for an investigation; INFO — an investigation is impossible without it), its field names and the
  owning capability. A field the event does not declare is refused (tests fail); the zero `Event` value compiles but is
  refused the same way. Lines are self-contained: entities appear as `group=#412`, `route=<public_id>`,
  `integration=<public_id>` with their names where useful. This story registers `process_started` (INFO: `version`,
  `commit`) and `library_message` (WARN: `message`), the lines that third-party libraries write to the standard
  library logger once the process logger captures it (C-02.FR-17); every later story registers its own events.
- **Secrets in logs**: a field of the type `logging.Secret` is always written as `[redacted]`; the lint-5 harness gets a
  probe that logs known secret values through the logger and asserts they appear nowhere.
- **Metric registry** (C-02.FR-19): VictoriaMetrics/metrics. `internal/metrics/catalogue.go` declares every metric —
  name, type, help, labels, the closed value set of every label that is not an entity label, the owning capability and
  whether only the Leader exports it. Entity labels (`integration`, `route`, `destination`) carry the entity's
  `public_id`; the labels of an `*_info` gauge carry what it describes (a name, the version, the commit) and no other
  metric may have such a label; alert labels, Alert Group numbers, users and the Organization never appear. Histograms
  are built only with explicit `le` buckets per kind of latency. This story declares
  `muster_build_info{version,commit}`.
- **`/metrics` handler**: writes the registry, process metrics and Go runtime metrics; pull only. It takes a function
  that reports whether the replica leads, and writes the Leader-only metrics only while it does (S-008 passes it).
- **Reference pages** (generated by `internal/tools/refgen` in `make generate`, checked by `make generate-check`):
  `docs/reference/log-events.md` (event, level, fields, capability) and `docs/reference/metrics.md` (metric, type,
  labels with their value sets, capability, Leader only). The columns say "capability", not "owner", which is a
  glossary term.
- **Lints**: `.golangci.yml` already points rule 7 at `internal/logging` and rule 8 at the VictoriaMetrics `vmrange`
  constructors (S-001); both now apply to the real packages. A rule 5 probe's `Run` takes the test's
  `context.Context`, since the logger takes one and rule 6 forbids `context.Background()` in `internal/archlint`.

## Steps

1. Write the logger and the event registry with `process_started`. Check: the logger tests show the JSON shape, level
   filtering, `[redacted]` secrets and the refusal of undeclared fields.
2. Write the metric registry, the label rules and the handler. Check: the registry tests reject a free-form label and a
   non-`le` histogram; the handler test sees `muster_build_info`.
3. Write `refgen` and wire it into `make generate`. Check: both pages are generated and `make generate-check` passes.
4. Point lints 5, 7 and 8 at the new packages. Check: `make lint lint-arch` pass, and the lint-5 probe for the logger
   passes.

## Verification

```sh
make generate
grep -F '| `process_started` |' docs/reference/log-events.md
# | `process_started` | INFO | version, commit | C-02 | ...
grep -F '| `muster_build_info` |' docs/reference/metrics.md
# | `muster_build_info` | gauge | version, commit | C-02 | ...

# an ad-hoc event cannot be created outside the registry
printf '%s\n' '// SPDX-License-Identifier: AGPL-3.0-only' '// Copyright The Muster Authors' 'package cli' \
  'import "github.com/muster-io/muster/internal/logging"' 'var _ = logging.newEvent("adhoc")' > internal/cli/zz_adhoc.go
make build; echo "exit=$?"
# internal/cli/zz_adhoc.go:5:17: undefined: logging.newEvent
# exit=2
rm internal/cli/zz_adhoc.go

# a stale reference page fails the check
echo '<!-- edited -->' >> docs/reference/metrics.md; make generate-check; echo "exit=$?"
# stale generated files: docs/reference/metrics.md
# exit=2
git checkout docs/reference/metrics.md
```

The live `curl` of `/metrics` happens in S-006, when the internal listener exists.

## Open questions

None.

## Notes

- Suggested commit: `feat(logging): add domain logger with log event and metric registries`.
- Entity labels use the `public_id` because internal ids never leave the server (ADR-0008); names come from the
  `*_info` metrics of later stories.
- Each capability registers its own events and metrics in these two files; review them like any other contract.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-02.FR-17 | full | `MUSTER_LOG_LEVEL` parsing is S-006 |
| C-02.FR-18 | full | each capability adds its events |
| C-02.FR-19 | partial | registry, label rules, handler; serving `/metrics` and the database pool metrics are S-006, Leader-only gauges S-008 |
| C-02.AC-12 | full | |
| C-01.FR-12 | partial | `muster_build_info` declared and written; served from S-006 |

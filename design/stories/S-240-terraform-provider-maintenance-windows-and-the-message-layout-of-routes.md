---
id: S-240
title: "muster_maintenance_window and the Route's message_layout in the Terraform provider (BE)"
capability: C-33
kind: be
layer: L2
repository: muster-io/terraform-provider-muster
depends_on: [S-187, S-190, S-210]
covers: [C-34.FR-23, C-38.FR-15]
files_touched:
  - api/openapi.yaml
  - api/MUSTER_VERSION
  - internal/provider/provider.go
  - internal/provider/version.go
  - internal/provider/version_test.go
  - internal/provider/instant.go
  - internal/provider/instant_test.go
  - internal/provider/resource_maintenance_window.go
  - internal/provider/resource_maintenance_window_test.go
  - internal/provider/route_common.go
  - internal/provider/resource_route_test.go
  - internal/provider/resource_default_route_test.go
  - internal/provider/complete_test.go
  - internal/acctest/acctest.go
  - examples/complete/main.tf
  - examples/resources/muster_maintenance_window/resource.tf
  - examples/resources/muster_maintenance_window/import.sh
  - examples/resources/muster_route/resource.tf
acceptance:
  - "[C-34.FR-23] `muster_maintenance_window` on a `muster_team`, planned from one to three hours ahead with a reason and `urgent_passes` true and its times written with a `+03:00` offset, applies; `getMaintenanceWindow` reads it planned, with its times in UTC, and a second plan shows no change, because `start` and `end` compare as instants (`TestAccMaintenanceWindow_basic`, `TestInstant`)."
  - "[C-34.FR-23] A planned window changes in place in `start`, `end`, `reason` and `urgent_passes`; a change of `object_type` or `object_id` plans a replacement; destroying a planned window deletes it with `deleteMaintenanceWindow` (`TestAccMaintenanceWindow_update`)."
  - "[C-34.FR-23] A window whose `start` is the business clock's now applies and reads active; a change of its `end`, `reason` or `urgent_passes` updates it in place, and a change of its `start` fails with Muster's `422` as a diagnostic at `start`; destroying it closes it with `endMaintenanceWindow`, and `getMaintenanceWindow` then reads it ended (`TestAccMaintenanceWindow_active`)."
  - "[C-34.FR-23] Once the development clock has passed a window's end, a plan shows no change; a new `start` and `end` plan a replacement, which forgets the ended window without a request and creates a new one; a read that finds an ended window gone, after `retention.audit_log`, keeps it in the state as it was and never creates it again, while a planned or active window gone from Muster leaves the state as every resource does (`TestAccMaintenanceWindow_ended`, `TestMaintenanceWindowReadGone` against a stub server)."
  - "[C-34.FR-23] The `end` of a planned window changed through the API shows in the next plan and is undone by the apply; a window deleted through the API is created again; the window imports by its `public_id` and plans without changes (`TestAccMaintenanceWindow_drift`, `TestAccMaintenanceWindow_import`)."
  - "[C-38.FR-15] `muster_route` with `own_message_layout` true and a `message_layout` with the order `alerts`, `text`, `fields`, `labels`, the fields Severity, Teams and the label `cluster`, `show_description` false and `alerts_listed` 5 applies; `getRoute` reads that layout as the Route's own, and a second plan shows no change (`TestAccRoute_messageLayout`)."
  - "[C-38.FR-15] A `muster_route` without `own_message_layout` reads Muster's default layout into the computed `message_layout` and never sends it back, so `getRoute` still reads `own_message_layout` false after every apply; a layout saved through the API on that Route shows in the next plan, and the apply returns the Route to Muster's default; turning `own_message_layout` from true to false does the same (`TestAccRoute_messageLayoutDefault`, `TestAccRoute_drift`)."
  - "[C-38.FR-15] Muster's `422` for a layout — a field named twice, `alerts_listed` out of its range — is a diagnostic at `message_layout.fields[1]` and at `message_layout.alerts_listed`; `muster_default_route` takes `own_message_layout` and `message_layout` as `muster_route` does (`TestAccRoute_messageLayoutBounds`, `TestAccDefaultRoute_messageLayout`)."
  - "[C-34.FR-23, C-38.FR-15] Against a Muster whose version is older than the release that brings Maintenance windows and message layouts, a `muster_maintenance_window` and a `muster_route` with `own_message_layout` true fail with a diagnostic that names Muster's version and the version they need, and a `muster_route` without `own_message_layout` plans as before; a development build warns and goes on (`TestFeatureVersions` against a stub `getSystemStatus`)."
  - "[C-34.FR-23, C-38.FR-15] `examples/complete/main.tf` gains a planned window on a Team, whose times `TestAccComplete` passes as variables, and a Route with a layout of its own, and `TestAccComplete` passes; `make docs-check` finds `docs/` current with the page of `muster_maintenance_window` and the new attributes of `muster_route` and `muster_default_route`."
verify: "make ci testacc"
operator_attention: false
issue: null
---

# S-240. muster_maintenance_window and the Route's message_layout in the Terraform provider (BE)

## Scope

**IN**

- The re-pin of the provider's copy of the specification to the first `0.2.x` release of Muster that carries S-190
  and S-210, and the regenerated client.
- `muster_maintenance_window`, the resource of C-34.FR-23 that the provider adds to those of C-33.FR-5: one window on
  one Integration, Route or Team, managed through the operations of `maintenance-windows`.
- `own_message_layout` and `message_layout` on `muster_route` and `muster_default_route` (`route_common.go` of
  S-187), so that the provider reads and writes a Route's layout like any other Route setting (C-38.FR-15).
- The minimum Muster version of each of the two features, beside the provider's own minimum of C-33.FR-4.

**OUT**

- Muster's side: the windows and their API (S-190), what a window keeps back (S-191 to S-193), the layout and its API
  (S-210). This story changes nothing in Muster's repository.
- A data source of windows, and a list of the Alert Groups a window keeps back: windows are written, not looked up.
- Muster's documentation: the page of S-196 shows the API; naming the resource there once the provider's release
  carries it is a documentation change in Muster's repository.

## Contracts

- **Specification and client** (`api/openapi.yaml`, `api/MUSTER_VERSION`): `make spec-sync VERSION=<x.y.z>` with the
  first release of Muster that carries both S-190 and S-210, and `make generate`. The provider calls
  `createMaintenanceWindow`, `getMaintenanceWindow`, `updateMaintenanceWindow`, `deleteMaintenanceWindow` and
  `endMaintenanceWindow` (schemas `MaintenanceWindow` and `MaintenanceWindowInput`), and reads and writes
  `own_message_layout` and `message_layout` (`RouteMessageLayout`) of `Route` and `RouteInput`. The problems
  `maintenance_window_ended`, `maintenance_window_active`, `maintenance_object_builtin` and `out_of_range` at `/start`
  and `/end` become diagnostics through `resourcekit` (S-182), at the attribute of their pointer.
- **Versions** (`version.go`): the provider's minimum stays `0.2.0` (C-33.FR-4); two constants name the release that
  brings Maintenance windows and the one that brings message layouts, both the version of the re-pin. `Configure`
  keeps Muster's version from `getSystemStatus`; a `muster_maintenance_window`, and a `muster_route` or
  `muster_default_route` whose `own_message_layout` is true, fail on plan against an older Muster with "Muster {version}
  has no {Maintenance windows|message layouts}; they need Muster {minimum} or later". A version that cannot be compared,
  such as a development build, warns as C-33.AC-1 says and goes on.
- **`muster_maintenance_window`** (C-34.FR-1 to C-34.FR-4, C-34.FR-21):
  - attributes `object_type` (`integration`, `route` or `team`) and `object_id` (the object's `public_id`), both
    required and replacing the window when they change, since the object of a window never changes (C-34.FR-3);
    `start` and `end`, required RFC 3339 times with an offset, of a type whose semantic equality compares instants, so
    that the UTC that Muster returns is no change; `reason`, optional; `urgent_passes`, optional, `false` by default
    (`maintenance.urgent_passes`); computed `id` and `etag`. The window's state (`planned`, `active`, `ended`) is kept
    in the resource's private state from each read, never as an attribute, and so are neither `kept_back_count` nor
    `created_by`, which change without the configuration.
  - create and update send `MaintenanceWindowInput` with `If-Match` on update (`resourcekit`); bounds and the rules of
    C-34.FR-3 are Muster's (`maintenance.limits`, a planned start not in the past, an active window's start), never the
    provider's.
  - a window whose last read was ended plans a replacement for any change of its attributes, because an ended window
    no longer changes (C-34.FR-3): the replacement forgets the ended one and creates a new window.
  - delete reads the window first: a planned one is deleted with `deleteMaintenanceWindow`; an active one is closed
    with `endMaintenanceWindow`, never deleted (C-34.FR-3), and stays in Muster as ended; an ended one is forgotten
    without a request. A `409` of a delete because the window started meanwhile is answered by closing it.
  - a read that gets `404` for a window whose last read was ended keeps the state as it was: retention removed it after
    `retention.audit_log`, and creating a past window again would be refused. A `404` for a planned or active window,
    deleted through the API or with its object, removes it from the state as `resourcekit` does.
  - import by `public_id` (`ImportStatePassthroughID`).
  - windows carry no `managed_by`: the resource sends none, and `resourcekit`'s mark of C-33.FR-13 is not applied to it
    (Open question 1).
- **`own_message_layout` and `message_layout`** (`route_common.go`, used by `muster_route` and `muster_default_route`):
  `own_message_layout`, optional, `false` by default; `message_layout`, a nested attribute with the properties of
  `RouteMessageLayout` — `order`, `fields` (each `field` and, for `label`, `label`), `show_summary`,
  `show_description`, `show_labels`, `show_alerts`, `alerts_listed` and `show_links` — required when
  `own_message_layout` is true and computed otherwise. With `own_message_layout` false the provider sends
  `own_message_layout` false and no `message_layout`, so that a configuration never turns Muster's default into a
  layout of the Route's own (C-38.FR-15), and the read fills `message_layout` with the effective layout; a change of
  Muster's default between releases is no planned change. Against a Muster older than message layouts the provider
  sends neither field and reads `own_message_layout` as false.
- **Acceptance harness** (`internal/acctest`): a helper that reads and advances the development clock of `muster dev`
  (`GET` and `POST :8082/_dev/clock`), the first acceptance tests to move it; the tests of ended windows that use it
  run one after another, never in parallel with the others, and advance it by no more than a window's length.
- **Documentation**: `examples/resources/muster_maintenance_window/resource.tf` (a Team and a planned window on it,
  whose `start` and `end` are variables that a pipeline sets, since a fixed time would soon be in the past and a time
  computed at plan would change on every plan) and `import.sh`; the Route example gains a layout of its own;
  tfplugindocs generates the pages (S-182).

## Steps

1. Re-pin the specification and regenerate the client. Check: `make spec-check`, `make generate-check`.
2. Write the version constants and their check. Check: `TestFeatureVersions`.
3. Write the instant type and `muster_maintenance_window`. Check: `TestInstant`, `TestMaintenanceWindowReadGone` and
   the acceptance tests `TestAccMaintenanceWindow_*`.
4. Add `own_message_layout` and `message_layout` to `route_common.go`. Check: `TestAccRoute_messageLayout*`,
   `TestAccDefaultRoute_messageLayout`, `TestAccRoute_drift`.
5. Extend the examples and the complete example. Check: `make docs-check`, `make ci testacc` on the four CLI versions.

## Verification

In a clone of the provider, with `eval "$(test/muster-dev.sh up)"` on the image of the re-pin and the provider
installed as in S-182:

```sh
A=(-H "Authorization: Bearer $MUSTER_TOKEN"); API=$MUSTER_ENDPOINT/api/v1; CLK=http://127.0.0.1:8082/_dev/clock
NOW=$(curl -s $CLK | jq -r .now)
export TF_VAR_start=$(date -u -d "$NOW +1 hour" +%FT%T+00:00) TF_VAR_end=$(date -u -d "$NOW +3 hours" +%FT%T+00:00)
cd examples/resources/muster_maintenance_window
terraform init > /dev/null && terraform apply -auto-approve > apply.log; tail -1 apply.log
# Apply complete! Resources: 2 added, 0 changed, 0 destroyed.         (the Team and its planned window)
W=$(terraform show -json | jq -r '.values.root_module.resources[] | select(.type == "muster_maintenance_window") | .values.id')
curl -s "${A[@]}" $API/maintenance-windows/$W | jq -c '{state, object: .object.type, start, end, managed_by}'
# {"state":"planned","object":"team","start":"…Z","end":"…Z","managed_by":null}
terraform plan -detailed-exitcode > /dev/null; echo "exit=$?"                                         # exit=0
curl -s -X POST $CLK -d '{"advance_seconds": 3600}' > /dev/null                                       # the window starts
terraform plan -detailed-exitcode > /dev/null; echo "exit=$?"                                         # exit=0
terraform destroy -auto-approve > /dev/null
curl -s "${A[@]}" $API/maintenance-windows/$W | jq -r .state                                          # ended
cd ../muster_route
terraform init > /dev/null && terraform apply -auto-approve > /dev/null
R=$(terraform show -json | jq -r '.values.root_module.resources[] | select(.type == "muster_route") | .values.id')
curl -s "${A[@]}" $API/routes/$R | jq -c '{own_message_layout, order: .message_layout.order}'
# {"own_message_layout":true,"order":["alerts","text","fields","labels"]}
terraform plan -detailed-exitcode > /dev/null; echo "exit=$?"                                         # exit=0
```

The pull request carries this output and the acceptance run on the four CLI versions.

## Open questions

1. **`managed_by` on windows.** C-33.FR-13 marks the configuration objects of C-33.FR-5, and S-190 gives windows no
   `managed_by`: a window is short-lived, and CI jobs open windows as often as Terraform does. Recommended: no mark on
   windows, and the resource sends none; the alternative is a `managed_by` column on `maintenance_windows`, added by a
   story of Muster before this one.
2. **An ended window in the state.** A window ends by the clock and is removed by retention a year later. Recommended,
   as the Contracts say: an ended window stays in the state, a change of it plans a replacement, and a `404` for it
   keeps the state, so that a configuration that keeps a past window plans nothing; the alternative, removing it from
   the state, makes the next plan try to create a window in the past, which Muster refuses.

## Notes

- Suggested commit: `feat: manage maintenance windows and the message layout of routes`.
- Implementation starts after the release of Muster that carries S-190 and S-210, whose version the re-pin names; the
  provider releases it as a minor version of its own `0.x` (S-188).
- A recurring window is a scheduled CI job or a module that a pipeline applies with new times; the resource is one
  window, as C-34.FR-1 has no repeating windows.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-34.FR-23 | partial | the Terraform resource for windows; with S-196 complete |
| C-38.FR-15 | partial | the layout as a setting of `muster_route` and `muster_default_route`; with S-210 and S-217 complete |

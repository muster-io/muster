# C-33. Terraform provider

[L2 index](../L2.md) · Release: Muster 0.2.x and the provider's own 0.x · UI: yes · Depends on: C-03, C-04, C-05,
C-08, C-12, C-13, C-14, C-15, C-20, C-22, C-24, C-25, C-26, C-27, C-31, C-32

**Goal.** An installation keeps its whole configuration as code: a Terraform provider, which works with OpenTofu too,
creates, changes, imports and deletes Integrations, Routes, Connections, Destinations, Teams, Schedules, Escalation
chains, the Organization's settings with its default Notification rules, the OIDC settings with the Role mapping, Lookup
tables and Link rules through Muster's API, with an Admin's Service account token. Muster marks what the provider
manages, shows the mark and warns before a change in the web UI, and never refuses one. The provider is MIT, lives in
a repository of its own and is published in the Terraform Registry and the OpenTofu Registry (ADR-0001, journal D492,
D506, D757). Alert Groups, Overrides and everything personal stay out of it: they are operations, not configuration.

## Scenarios

1. The Admin creates the Service account "terraform" with the Admin Role and a token (C-04), and the pipeline of the
   infrastructure repository sets `MUSTER_ENDPOINT` and `MUSTER_TOKEN`. One `terraform apply` creates an Integration and
   its Integration token, a Mattermost Connection, two Destinations, the Teams "payments" and "search" with their
   Schedules, an Escalation chain and two Routes; the same apply writes the token into the Alertmanager configuration.
2. Ivan changes the name of a Route that Terraform created. The Route page says "Managed by terraform" and warns that
   the next run of terraform undoes changes saved here; he saves anyway. The next `terraform plan` shows the name, and
   the apply puts it back.
3. Terraform adds alice to the Team "payments" by hand; bob is in it through the identity provider. Applying never
   touches bob, and removing alice's resource takes away only her addition by hand.
4. The Mattermost bot token comes from a secret store through an ephemeral resource into `bot_token_wo`, so it is
   never written to the state; after a rotation the Admin raises `bot_token_wo_version` and applies.
5. A saved plan waits for approval while somebody edits the same Escalation chain in the web UI; the apply stops with
   "Escalation chain "payments" changed in Muster since Terraform last read it" and asks for a new plan.
6. The provider is configured against Muster 0.1.4 and stops at once: "Muster 0.1.4 is older than 0.2.0, the oldest
   version this provider works with".
7. An installation that grew in the web UI writes `import` blocks with the `public_id` of each object, and the next
   plan shows nothing to change.

## Functional requirements

- **C-33.FR-1** The provider lives in `muster-io/terraform-provider-muster` (default branch `master`), MIT without a
  CLA, like the bridge libraries (ADR-0001, ADR-0017); its stories are written in Muster's repository with
  `repository: muster-io/terraform-provider-muster`. It is built on terraform-plugin-framework with the plugin protocol
  version 6, its address is `registry.terraform.io/muster-io/muster` (`muster-io/muster` in the OpenTofu Registry),
  and it has its own `0.x` versions, released by release-please, independent of Muster's. The lowest supported CLIs
  are Terraform 1.11 and OpenTofu 1.11, the first with write-only attributes (FR-10); the documentation states them,
  and the acceptance tests run them.
- **C-33.FR-2** Muster's API specification `api/openapi.yaml` is MIT, as ADR-0001 decided for L2: its
  `info.license`, `NOTICE` and `api/README.md` say so. The provider keeps a byte-for-byte copy of the specification
  of the Muster version it is built against, which it names, and generates its client from that copy with oapi-codegen;
  its CI fails when the copy differs from the specification of that version in Muster's repository, and moving to a
  newer Muster version is a pull request that replaces the copy and regenerates the client.
- **C-33.FR-3** The provider takes `endpoint`, Muster's public URL, and `token`, a sensitive attribute, each falling
  back to `MUSTER_ENDPOINT` and `MUSTER_TOKEN`; a value in the configuration wins over the environment. The token is
  meant to be a Service account token with the Admin Role (C-04). When it is configured, the provider reads the
  version of the server from `getSystemStatus` and stops with an error when it is older than the provider's minimum
  version (FR-4), naming both; there is no upper bound, because `0.2.x` releases are additive (NFR-11). A version
  that is not a release version, such as that of a development build, is accepted with a warning. `401` stops with an
  error that names `MUSTER_TOKEN` or `token`; `403` says that the token needs the Admin Role. `getSystemStatus` is
  chosen on purpose: it needs a Permission only Admins have, so the same call checks the version and the Role.
- **C-33.FR-4** The minimum version is Muster 0.2.0, which carries the `managed_by` marker of FR-11: the marker ships
  in 0.2.0 (journal D757 and the maintainer's answers to this specification). A field that a newer Muster adds and the
  provider does not know is never sent, so an update keeps its stored value (the API's rule for omitted fields).
- **C-33.FR-5** The first release manages the whole configuration, each object as one resource:

  | Resource | Object | API |
  |---|---|---|
  | `muster_organization` | The Organization's settings, with the default Notification rules (C-20, C-31) | `getOrganization`, `updateOrganization` |
  | `muster_outbound_policy` | The Outbound address policy (C-20) | `getOutboundPolicy`, `updateOutboundPolicy` |
  | `muster_oidc_settings` | The OIDC settings, with the Role mapping (C-03) | `getOidcSettings`, `updateOidcSettings` |
  | `muster_integration` | An Integration (C-05) | the Integration operations |
  | `muster_integration_token` | An Integration token (C-05) | `createIntegrationToken`, `revokeIntegrationToken` |
  | `muster_mattermost_connection`, `muster_telegram_connection`, `muster_bridge_connection`, `muster_slack_connection` | A Connection of each type (C-13, C-14, C-22, C-24) | the Connection operations |
  | `muster_mattermost_destination`, `muster_telegram_destination`, `muster_bridge_destination`, `muster_slack_destination`, `muster_webhook_destination` | A Destination of each type, with the Secrets of an outgoing webhook (C-13 to C-15, C-22, C-24, C-28) | the Destination operations, `setDestinationSecret`, `deleteDestinationSecret` |
  | `muster_lookup_table`, `muster_link_rule` | Lookup tables and Link rules (C-12) | their operations |
  | `muster_team` | A Team with its Team matchers, its identity provider entries, its Team mentions and Working hours (C-25) | the Team operations |
  | `muster_team_destinations`, `muster_team_escalation_chains` | The Destination entries and the Escalation chains of a Team (C-26, C-32; FR-16) | `getTeam`, `updateTeam` |
  | `muster_team_member` | One person added to a Team by hand (FR-15) | `addTeamUser`, `removeTeamUser` |
  | `muster_schedule` | A Schedule with its Rotation layers (C-27) | the Schedule operations |
  | `muster_escalation_chain` | An Escalation chain with its steps (C-32) | the Escalation chain operations |
  | `muster_route` | A Route with its Matchers, Destinations, chains, message templates and policy (C-08, C-12, C-26) | the Route operations |
  | `muster_default_route` | The Default route (FR-14) | `getRoute`, `updateRoute` |
  | `muster_route_order` | The order of the Routes (FR-16) | `listRoutes`, `reorderRoutes` |

  Templates are attributes of their objects — the message templates of a Route's policy, the request templates of an
  outgoing webhook Destination and the URL template of a Link rule — with the Lookup tables they read. Out of scope:
  Alert Groups and their Commands; Overrides, which Responders make while they work (C-27.FR-3); everything personal —
  the profile, a person's own Notification rules, contacts at Notification gateways, Account links, Personal access
  tokens and iCal feeds; Users and Service accounts, which only the data source `muster_user` reads; the checks, tests
  and previews of Connections and Destinations; and the rotation of an outgoing webhook's Signing secret, which stays
  in the web UI and the API in the first release.
- **C-33.FR-6** Data sources find objects that Terraform does not create: `muster_user` by `login`, `email` or `id`
  (`listUsers`, `getUser`), `muster_connection` by `name` or `id`, of any type (`listConnections`, `getConnection`), and
  `muster_team` by `slug` or `id` (`listTeams`, `getTeam`). Each answers one object, and an error when none or more
  than one matches.
- **C-33.FR-7** Attributes follow the API: the names of its fields, durations in `*_seconds`, the `public_id` as `id`,
  and references to other objects as their `public_id` in `*_id` and `*_ids` attributes, so that Terraform orders the
  work by them. Only the current form of a field that L2 changes in expand and contract steps is offered — the
  Destination entries and Minimum severity of a Route, the Mention entries of a Destination — never the fields that
  0.3.0 removes (`urgent`, `destination_ids`, `mentions`, `critical_is_urgent`). State that the server computes and that
  changes by itself — health, warnings, counts, Storms, the next gap of a Schedule, the On-call — is not part of a
  resource, so that it never shows as a change. Except on the objects that exist once (FR-14), an optional attribute
  that the configuration leaves out takes the default that the resource's documentation states — the API's default
  where it has one — so that the configuration describes the whole object and a change made elsewhere is undone.
  The provider checks only the types and enumerations of the
  specification; every bound — lengths, ranges, limits, the provisional values `P-NN` — is the server's, and a `422`
  is reported at the attribute that its problem's `errors` point at, so that a bound the server changes needs no new
  provider release.
- **C-33.FR-8** Every read stores the object's `etag` in the state; every update and delete sends it as `If-Match`.
  Where two resources write one object — a Team and its lists, the Routes and their order (FR-16) — the stored ETag
  would be stale after the other's write in the same apply, so each of them holds the provider's lock for that object,
  reads it right before writing, stops with the error below when the part it manages differs from its state, and sends
  the ETag of that read. A `412` stops the apply with an error that names the object and
  says it changed in Muster since Terraform last read it, and asks for a new plan; the provider never sends the request
  again with a newer ETag by itself. A plan refreshes the state first, so a change made in the web UI before the plan
  shows as a difference that the apply undoes.
- **C-33.FR-9** Every resource is imported by the `public_id` of its object, with `terraform import` and with `import`
  blocks; `muster_team_member` by `<team id>/<user id>`; the objects that exist once — `muster_organization`,
  `muster_outbound_policy`, `muster_oidc_settings`, `muster_default_route` and `muster_route_order` — by any id, which
  they ignore; `muster_team_destinations` and `muster_team_escalation_chains` by the Team's `public_id`. An import
  leaves Secrets and the values Muster shows only once (FR-10) unknown to the state: the next
  apply sets the Secrets that the configuration holds.
- **C-33.FR-10** Every write-only Secret of the API can be set in two ways — the bot token, push secret, app-level
  token and Slack signing secret of Connections, the proxy passwords of Connections, Destinations and the OIDC settings,
  the OIDC client secret, the URL of the Outgoing heartbeat and the named Secrets of an outgoing webhook Destination:
  - `<name>`, a sensitive attribute, stored in the state and hidden in plans;
  - `<name>_wo`, a write-only attribute that is never stored in the plan or the state, with `<name>_wo_version`, a
    number whose change sends the value again; the provider cannot see a new value otherwise.

  A configuration sets at most one of the two. Write-only attributes need Terraform 1.11 or OpenTofu 1.11 or later; on
  an older CLI, a configuration that sets one fails validation with an error that names those versions, and the
  sensitive attributes keep working. The documentation of each resource with a Secret states both ways and those
  versions. Changes of a Secret outside Terraform cannot be seen, since the API never returns its value — only
  `*_status` with `set` and `updated_at` — so they show no difference; with a sensitive attribute, a Secret that the
  server reports as not set is set again by the next apply. The values that Muster shows only once — the value of an
  Integration token with its Alertmanager snippet, and the first Signing secret of an outgoing webhook Destination —
  are sensitive attributes that the provider stores when it creates the object.
- **C-33.FR-11** _Muster._ Integrations, Routes, Teams, Schedules, Escalation chains, Connections, Destinations, Lookup
  tables, Link rules, the Organization's settings, the Outbound address policy and the OIDC settings have the field
  `managed_by` in their reads and writes: the name of the tool that keeps the object as code, or null. A value is 1 to
  32 lowercase letters, digits, `.`, `_` and `-`, starting with a letter or digit; another is refused with `422` and
  the error `invalid_format` at `/managed_by`. A create that leaves it out stores null; an update that leaves it out
  keeps it, and null clears it. It is a mark only: it changes no behaviour of Muster and never refuses a write, from
  the web UI or the API; a change of it is recorded in the Audit log with the object's diff, like any field.
- **C-33.FR-12** _Muster._ The page of an object with `managed_by` shows "Managed by {tool}" next to its title. Its
  form shows the warning "This {object} is managed by {tool}. Changes you save here are undone by the next run of
  {tool}: change its code instead." and saves as usual; the delete dialog adds "{tool} creates it again on its next
  run.". The pages of the Organization's settings show the Organization's mark, the Outbound address policy page its
  own, and the OIDC settings page its own. Saving from the web UI keeps the mark. The texts exist in English and
  Russian.
- **C-33.FR-13** The provider sends `managed_by` `terraform` with every create and update of an object it manages, so
  that an imported object gets the mark at its next apply; it writes `terraform` under OpenTofu too, and the value is
  not configurable. When a resource is removed from the state without being
  destroyed, the mark stays until somebody clears it.
- **C-33.FR-14** The objects that exist once — the Organization's settings, the Outbound address policy, the OIDC
  settings — and the Default route are never created or deleted by the provider. Creating their resource reads the
  object and writes the attributes of the configuration onto it; an attribute that the configuration leaves out keeps
  the server's value and is read into the state. Destroying it changes nothing but clears `managed_by`, then removes the
  resource from the state with a warning that the object stays in Muster. The built-in Link rule "Explore" may be
  imported into `muster_link_rule`, and its destroy does the same, since it cannot be deleted; the built-in "Muster"
  Integration cannot be edited and has no resource.
- **C-33.FR-15** Team membership is not authoritative: `muster_team` has no members, and each `muster_team_member`
  stands for one person added by hand (`by_hand`). Creating it adds the User by hand, and a User already added by hand
  is taken over as it is; it exists while the User has the source `by_hand` in the Team; destroying it takes away only
  that source, and a person who is in the Team through the identity provider stays in it (C-25.FR-4). People added by
  hand in the web UI and people of the identity provider are never touched.
- **C-33.FR-16** Three resources hold a list that belongs to another object. `muster_route_order` holds every Route but
  the Default route, in order, and applies it with `reorderRoutes`; a Route that is not in the list, such as one made in
  the web UI, makes the apply fail with an error that names it, and without this resource a Route that the provider
  creates goes last before the Default route, as any new Route does (C-08). `muster_team_destinations` and
  `muster_team_escalation_chains` hold a Team's Destination entries and its Escalation chains, with their Severity
  conditions: Destinations and chains name their owning Team, so a Team that named them in its own resource would make
  a cycle that Terraform cannot order. Each is authoritative for its list only and leaves the rest of the Team alone;
  `muster_team` leaves those lists alone in turn. The three resources of one Team, and `muster_route_order` with the
  Routes that a create or delete of `muster_route` changes, share their object's ETag, so they write it as FR-8 says for
  shared objects: one at a time, each after a fresh read; a change made in Muster is still found by the refresh and
  shown in the plan.
- **C-33.FR-17** Errors of the API reach the person as diagnostics with the problem's title, detail and `code`, at the
  attribute that its `errors` point at where there is one. A refusal that protects data — a Connection used by
  Destinations, a Team with Schedules, a Route with open Alert Groups — is reported as Muster gives it, and the
  provider never works around it: destroying a Route with open Alert Groups ends with Muster's `409`
  `route-has-open-alert-groups`, and moving them first (`moveOpenAlertGroups`) is left to the person. `429` is
  retried after its `Retry-After`; reads are retried on transient failures, and writes are never retried except after
  `429`. An object that answers `404` on a read, deleted or soft-deleted in
  Muster, is removed from the state, so that the next plan creates it again.
- **C-33.FR-18** The documentation of the provider is generated with tfplugindocs from the schema's descriptions, with
  an example and the import of every resource and data source, and has guides: getting started with a Service account
  and its token, the two ways of Secrets with the CLI versions they need, the `managed_by` mark and what the web UI
  does with it, and the versions of Muster, Terraform and OpenTofu each release works with. Muster's documentation
  site has a page on configuration as code that links to the provider's documentation and explains the mark.
- **C-33.FR-19** The provider's CI runs unit tests, and acceptance tests of every resource and data source — create,
  update, import, drift and destroy — against `muster dev` from Muster's image of the version it is built against,
  with PostgreSQL, on the lowest and the newest supported versions of Terraform and of OpenTofu, and daily against the
  development image `edge`, so that a change of Muster that breaks the provider shows at once. A job with Terraform
  1.10 and OpenTofu 1.10 runs the tests that an older CLI refuses the write-only attributes (FR-10).
- **C-33.FR-20** A release is built with GoReleaser for the platforms of the Registries, with a `SHA256SUMS` file
  signed with the project's GPG key and the Registry manifest, and published in the Terraform Registry and the OpenTofu
  Registry; the maintainer provides the signing key, the namespace and key in the Terraform Registry, and the
  submission of the provider and its key to the OpenTofu Registry. Its release notes name the Muster
  versions it works with. Work on the provider starts after Muster's 0.2.0 tag (journal D757).

## UI

Every configuration object with `managed_by` — the Integration, Route, Team, Schedule, Escalation chain, Connection and
Destination pages, a Lookup table and a Link rule, the Organization's pages, the Outbound address policy page and the
OIDC settings page: "Managed by {tool}" next to the title; the warning in its form "This {object} is managed by {tool}.
Changes you save here are undone by the next run of {tool}: change its code instead."; in the delete dialog "{tool}
creates it again on its next run.". The Russian texts follow the translations of the UI.

## API surface

`managed_by` on the inputs and reads of `Integration`, `Route`, `Team`, `Schedule`, `EscalationChain`, each
`Connection` and `Destination` type, `LookupTable`, `LinkRule`, `Organization`, `OutboundPolicy` and `OidcSettings`;
`info.license` of `api/openapi.yaml` MIT. The provider uses the existing operations of FR-5, FR-6 and `getSystemStatus`.

## Acceptance

Muster's part is checked against `muster dev`; the provider's in its own repository, with acceptance tests against
`muster dev` from Muster's image and its fake servers.

- **C-33.AC-1** With `MUSTER_ENDPOINT` and `MUSTER_TOKEN` set, a configuration with only the provider block plans; a
  configuration that reads a resource or data source of the provider — Terraform and OpenTofu never configure a
  provider that nothing uses — stops against Muster 0.1.4 with "Muster 0.1.4 is older than 0.2.0, the oldest version
  this provider works with"; a wrong token names `MUSTER_TOKEN`; a Responder's token says that the Admin Role is
  needed; a development build plans with a warning.
- **C-33.AC-2** An example configuration that uses every resource and data source applies; a second plan shows no
  change; `terraform destroy` deletes every object it created, leaves the Organization's settings, the Outbound
  address policy, the OIDC settings and the Default route as they were, and clears their `managed_by`.
- **C-33.AC-3** _Muster._ An object created with `managed_by` `terraform` reads it back on every one of the twelve
  kinds of FR-11; an update without the field keeps it, one with null clears it, and `Terraform` or a value of 33
  characters is refused with `422` and `invalid_format` at `/managed_by`; the Audit log shows each change in the
  object's diff; a write from the web session to a marked object succeeds.
- **C-33.AC-4** _Muster._ In a real browser, in English and in Russian, a marked Route shows "Managed by terraform"
  next to its title and the warning in its form, saves a new name and keeps the mark; its delete dialog shows the
  line of FR-12; a Route without the mark shows neither; the Organization's pages, the Outbound address policy page and
  the OIDC settings page show their marks.
- **C-33.AC-5** A Route renamed through the API after an apply shows the name as a change in the next plan, and the
  apply restores it; the same holds for a member of a list attribute, such as a Matcher.
- **C-33.AC-6** An apply of a saved plan whose Integration was changed through the API after the plan stops with the
  error of FR-8 and changes nothing; a new plan and apply succeed.
- **C-33.AC-7** Every resource imported by its `public_id` — `muster_team_member` by `<team id>/<user id>` — plans
  without changes, apart from the Secrets that the configuration holds.
- **C-33.AC-8** A Mattermost Connection with `bot_token` keeps the token in the state, hidden in the plan; with
  `bot_token_wo` the state file does not contain the token, raising `bot_token_wo_version` makes the fake Mattermost
  server receive the new token, and an apply without raising it sends nothing; the same configuration with
  `bot_token_wo` on Terraform 1.10 and on OpenTofu 1.10 fails validation naming 1.11; setting both attributes fails
  validation; with `bot_token`, a Secret cleared in Muster is set again by the next apply.
- **C-33.AC-9** `muster_integration_token` gives its value and Alertmanager snippet to the rest of the configuration
  after its create, and they are unknown after an import; destroying it revokes the token, which then fails ingestion
  with `401`.
- **C-33.AC-10** With alice added by `muster_team_member` and bob in the Team through the identity provider of the
  fake OIDC server, applying and destroying alice's resource leaves bob in the Team; alice, also matched by the
  identity provider, stays in it with the source `idp` after her resource is destroyed; a person added by hand in the
  web UI is not touched.
- **C-33.AC-11** `muster_route_order` applies the order of three Routes; a fourth Route created through the API makes
  the next apply fail with an error naming it. A Team whose Destination and Escalation chain name it as their owning
  Team takes both through `muster_team_destinations` and `muster_team_escalation_chains` in one apply, without a cycle;
  a Destination entry added to the Team in the web UI shows in the next plan and is removed by the apply, and a change
  of the Team's name by `muster_team` and of both lists in the same apply are all kept. Creating two Routes and the
  order that names them in one apply succeeds.
- **C-33.AC-12** `muster_organization` that sets only `name` and `notification_rules` leaves every other setting as
  it was and reads it into the state; the default Notification rules apply to a User without rules of their own, and a
  User's own rules are not touched (C-31.FR-1).
- **C-33.AC-13** A step of an Escalation chain with a wait above the server's limit is refused, and the diagnostic is
  at the attribute of that step's wait; a refusal that protects data, such as deleting a Connection used by a
  Destination outside Terraform, is reported with its `code`.
- **C-33.AC-14** A `muster_schedule` with two Rotation layers applies; an Override made in the web UI stays after the
  next apply and shows no change; an Escalation chain that names the Schedule, two Users found by `muster_user`, a
  Team found by `muster_team` and the fake gateway's Connection found by `muster_connection` applies, and a Route that
  names the chain and a Destination applies after both.
- **C-33.AC-15** A change of one byte in the provider's copy of `api/openapi.yaml` fails its CI; the generated client
  is current.
- **C-33.AC-16** A release tag produces signed archives, `SHA256SUMS`, its signature and the Registry manifest;
  `terraform init` installs the release from the Terraform Registry and `tofu init` from the OpenTofu Registry, each
  verifying the signature.
- **C-33.AC-17** The Registry shows a page for every resource and data source with an example and its import, and the
  guides of FR-18; Muster's documentation site has the page on configuration as code, and the web UI's mark links to
  it.

## Related ADRs

ADR-0001, ADR-0010, ADR-0011, ADR-0016, ADR-0017, ADR-0018.

## Depends on

C-03 — the OIDC settings and the Role mapping; C-04 — Service accounts and their tokens; C-05, C-08, C-12 to C-15,
C-22, C-24 — the configuration objects of L1; C-20 — the Organization's settings and the Outbound address policy;
C-25 to C-27, C-31, C-32 — Teams, Routes to Teams, Schedules, the default Notification rules and Escalation chains.

## Suggested story split

- **BE** — in Muster: the specification under MIT and `managed_by` on the first group of objects, then on the rest.
- **FE** — in Muster: the mark and the warnings on every configuration page.
- In `muster-io/terraform-provider-muster` — the skeleton with CI, the client, the provider's configuration and the
  release pipeline; then the resources by group: the objects that exist once; Integrations and Connections;
  Destinations, Lookup tables and Link rules; Teams, members and Schedules; Escalation chains and Routes; and the
  publication in both Registries.
- **docs** — in Muster: configuration as code on the documentation site.

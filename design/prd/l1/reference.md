# L1 cross-cutting reference

[L1 index](../L1.md)

Tables that several capabilities share. A capability's requirements point here instead of repeating a table; the
capability named in each row is the one that implements it. Values of the form `area.setting` are defaults from
[defaults.md](defaults.md).

Contents: [Loud and Quiet](#loud-and-quiet) · [Buttons and links by status](#buttons-and-links-by-status) ·
[Banners, warnings and notices](#banners-warnings-and-notices) · [Roles and Permissions](#roles-and-permissions) ·
[Internal alerts](#internal-alerts) · [Metrics catalogue](#metrics-catalogue) · [Chart rules](#chart-rules)

## Loud and Quiet

A Loud event is always a new message; an edit never rings anyone (ADR-0005; checked in both messengers,
[F-012](../../facts.md#notifications) and [F-029](../../facts.md#edits-and-notifications)). Loudness is fixed in L1.
Every lifecycle event — its name, loudness, Mentions, Timeline kind and how it appears in a messenger — is a row of the
lifecycle event tables of the capabilities that record them:
[C-09.FR-22](C-09-alert-group-lifecycle.md#lifecycle-events), [C-10.FR-15](C-10-commands.md#functional-requirements) and
[C-17.FR-11](C-17-timers.md#functional-requirements). Delivery turns lifecycle events into messages (C-11.FR-20) and
outgoing webhook events (C-15.FR-2).

Mentions are symbolic: `owner`, `previous_owner`, or the name of a Destination setting of C-12.FR-8 (`new_alert_group`,
`new_alerts`, `reopen`, `ack_timeout`, `snooze_ended`, `rise_to_urgent`), which each Destination turns into real
Mentions. A Reopen into acknowledged mentions only the Owner, whatever the Destination's settings.

Delivery adds the rows below for one Destination (C-11). They are not lifecycle events: those about one Alert Group are
recorded in its Timeline with the kind `delivery`, and none is sent as an outgoing webhook event.

| Delivery event | Loud or Quiet | How it appears | Mentions |
|---|---|---|---|
| Publication into a newly added Destination | Quiet | new Root message | — |
| Storm summary, first publication | Loud | new message | `new_alert_group` |
| Storm summary updates and final state | Quiet | message update | — |
| Gradual publication after a Storm | Quiet | new Root messages | — |
| Delivered late (resolved while its first Publication waited for a `RetryAfter` or for `Transient` retries within the budget) | Quiet | new Root message with a note | — |
| Publication after a Broken Destination recovers | Loud if the Alert Group is firing, otherwise Quiet | new Root message | `new_alert_group` when Loud |
| Root message update after a Broken Destination recovers | Quiet | one edit to the current state | — |
| Thread replies that came due while a Destination was Broken | not sent | — | — |
| Alert Group that resolved while its Destination was Broken, never published there | not published | Timeline and UI only | — |
| Republication after a deleted Root message | Quiet | new Root message with a note; Thread starts over | — |
| Final edit after the Destination left the Route | Quiet | one edit: "No longer updated here" | — |

Telegram makes a Quiet new message with `disable_notification`, which silences the push but does not remove it
(C-14.FR-6). In Telegram a Thread reply, Loud or Quiet and with or without a Mention, reaches only members of the
channel's discussion group, and the automatic copy of a Root message rings them even when the Root message is Quiet
(C-14.FR-9). In Mattermost a Thread reply notifies the Thread's followers by their own settings, so Quiet means "without
a Mention" (C-13.FR-8); a Mention also makes the person a follower of the Thread
([F-027](../../facts.md#threads-and-direct-messages)).

## Buttons and links by status

| Root message status | Buttons |
|---|---|
| firing | Ack · Resolve · Snooze (the Route's durations, `route.snooze_durations`) |
| acknowledged | Unack · Resolve · Snooze |
| snoozed | Ack · Unsnooze · Resolve |
| resolved | none; a link "Open in Muster" |

Other messages: a Reminder carries "Still on it" and "Unack"; a Storm summary and ack timeout notices carry only links;
a test message carries the buttons of its status, answered with "This is a test message; nothing was changed". The UI
and the API offer every command allowed by the status, including Unresolve, which messengers never offer. Snooze
durations appear as separate buttons or a menu, as the messenger allows. In Mattermost every link, "Open in Muster"
included, is a link in the post, never a button (C-13.FR-3).

## Banners, warnings and notices

| Where | Shown while | Text (English) | Capability |
|---|---|---|---|
| UI, every page | recovering after downtime | "Muster is recovering after downtime. Data may be incomplete until HH:MM." | C-02, C-03 |
| UI, Admins | no replica is leading | "No replica is leading: Telegram polling, Heartbeat and Stale checks are paused." | C-02, C-03 |
| OIDC settings | mapping empty or no groups claim | "Nobody will be able to sign in through OIDC." | C-03 |
| OIDC settings | the client secret expires within `oidc.secret_expiry_lead` | "The client secret expires on {date}. Issue a new one in the identity provider and enter it here." | C-03 |
| OIDC settings | Role sync would lower the last active Admin | "{user} stays Admin: they are the last active Admin, and the identity provider maps them to {role}. Make another user Admin first." | C-03 |
| Token list | token without expiry | "This token never expires." | C-04 |
| Integration | a `groupKey` is truncated | "Alertmanager truncates Snapshots for N groups. Set `max_alerts: 0` on the Alertmanager receiver." | C-06 |
| Integration | a learned repeat interval exceeds `processing.long_repeat_warning` | "Alertmanager route {route} repeats every {interval}; Muster can resolve its alerts by absence only after {3 × interval}. Use a repeat interval of 5–15 minutes on the route to Muster." | C-06 |
| Integration | Heartbeat not configured | "No Heartbeat: Muster will not notice when Alertmanager goes quiet, and Stale resolution is off." | C-07 |
| Integration | waiting for the first Heartbeat | "Waiting for the first Heartbeat signal. Stale resolution is off until it arrives." | C-07 |
| Integration | Heartbeat lost | "No contact with Alertmanager since HH:MM. Nothing is resolved as Stale until contact returns." | C-07 |
| Alert Group page | a Replacement happened | "Alerts were replaced because `<label>` changed. Consider removing Instance labels from the rule, for example `without(pod, instance)`." | C-09 |
| Alert Group page | its details were removed by retention | "Alerts and Timeline of this Alert Group were removed after {period}; only its summary and Notes are kept." | C-09 |
| Root message and Alert Group page | resolved by a person with Alerts still firing | "⚠ N alerts still firing in Alertmanager" | C-09, C-12 |
| Root message and Alert Group page | Reopen count > 0 | "🔁 Reopened ×N" | C-09, C-12 |
| Snooze dialog | no end chosen | "This Alert Group stays snoozed until someone unsnoozes it." | C-10 |
| Destination and its Routes | Broken | "Broken since HH:MM: {reason}. Muster tries again every {interval}." — the reason is the error, or "unavailable after repeated failures: {last error}" | C-11, C-13 |
| Alert Group page, delivery state | as applicable | "Not delivered: {error}" · "Waiting: {Destination} is Broken" · "Deleted in the messenger" · "Thread not attached to the post" · "Possible duplicate" · "Not posted: resolved during a Storm or while the Destination was Broken" · "No longer updated here" | C-11, C-13 |
| Root message | delivered late | "Delivered late: started HH:MM, resolved HH:MM while this Destination was unavailable." | C-11, C-12 |
| Root message | republished after deletion | "The previous message was deleted at HH:MM." | C-11, C-12 |
| Root message | Destination removed from the Route | "No longer updated here; current state in Muster: {link}" | C-11, C-12 |
| Route | Storm active | "Storm since HH:MM: N new Alert Groups. Destinations receive a Storm summary." | C-11 |
| Route or Destination | a template keeps failing | "Template error since HH:MM: {error}." plus "Messages use the fallback template." or "Requests are not sent." | C-12, C-15 |
| Mattermost Connection | always (hint) | "Mattermost calls {callback address} when someone presses a button. If that address is internal, add its host to AllowedUntrustedInternalConnections in the Mattermost server settings; otherwise presses fail with 'Action integration error'. A test message to a Destination checks it." | C-13 |
| Telegram Connection, base URL field | always (hint) | "A self-hosted Bot API server keeps the bot on that server only. Do not call api.telegram.org with this token from anywhere else: even getMe moves the bot back to Telegram's cloud, and presses and comments start to go missing without an error." | C-14 |
| Telegram Connection | the base URL uses `http` | "The bot token travels in clear text over http. Use https unless this path is a private network or a tunnel." | C-14 |
| Telegram Destination check | the channel has no linked discussion group | "Comments are not enabled for this channel. Enable comments in the channel settings in Telegram; this creates its discussion group." | C-14 |
| Telegram Destination check | the bot is not an admin of the discussion group | "The bot is not an admin of the discussion group {group}. Make the bot an admin there, allowed to post messages." | C-14 |
| Outgoing webhook Destination | previous Signing secret not retired | "The previous secret still signs, since {date}." | C-15 |
| Outgoing webhook Destination | a literal credential in a header or the URL | "Store credentials as Secrets: this value is shown to everyone who can read Destinations." | C-15 |
| Root message and Alert Group page | Unclaimed | "⚠ Nobody has taken this" | C-17 |
| Thread reply | its Owner was disabled or deleted (C-03.FR-13) | "The owner was disabled — this Alert Group has no owner now." · "The owner was deleted — this Alert Group has no owner now." | C-03, C-09 |
| Telegram bot, private chat | a `/start` whose token is unknown, used, expired or of another Connection | "This link is invalid or expired. Start again from your Muster profile." | C-18 |
| System status | outgoing heartbeat not set | "Set up an outgoing heartbeat so that a stopped Muster is noticed." | C-19 |
| Severity levels | unmapped values seen | "These values have no mapping and count as warning: {values}." | C-20 |
| Keyring | an older key is no longer needed | "Key {id} is no longer used and can be removed from MUSTER_SECRET_KEYS." | C-20 |
| Keyring | activation refused | "Replicas {names} do not hold key {id} yet." | C-20 |

## Roles and Permissions

Permissions are named `<resource>:<verb>`. The list below is final and is the `Permission` schema of the API
specification; every operation names the Permission it needs. The allocation to Roles is the L1 default, confirmed
with the C-03 sign-in story (P-42).

| Permission | Allows | Admin | Responder | Viewer |
|---|---|---|---|---|
| `alert-groups:read` | Read Alert Groups, Timelines, deliveries, statistics, counts and the user directory | ✓ | ✓ | ✓ |
| `alerts:read` | Read Alerts of Alert Groups and the Alerts view of Integrations | ✓ | ✓ | ✓ |
| `alert-groups:acknowledge` | Acknowledge, Unacknowledge, Takeover and answer Reminders ("Still on it") | ✓ | ✓ | — |
| `alert-groups:resolve` | Resolve and Unresolve | ✓ | ✓ | — |
| `alert-groups:snooze` | Snooze and Unsnooze | ✓ | ✓ | — |
| `alert-groups:note` | Add Notes | ✓ | ✓ | — |
| `integrations:read`, `routes:read`, `connections:read`, `destinations:read`, `link-rules:read`, `lookup-tables:read` | Read them, secret values masked (see below); `routes:read` also covers Route profiles, suggestions and dismissing one; picking a Connection to link an account through does not need `connections:read` (see below) | ✓ | ✓ | ✓ |
| `integrations:write`, `routes:write`, `connections:write`, `destinations:write`, `link-rules:write`, `lookup-tables:write` | Create, edit and delete them; `connections:write` also lists a Connection's channels, which calls out with the bot token | ✓ | — | — |
| `destinations:test` | Check, test and preview Destinations | ✓ | — | — |
| `templates:preview` | Dry-run and preview templates | ✓ | — | — |
| `stored-snapshots:read` | Read Stored Snapshots | ✓ | — | — |
| `users:read`, `users:write` | Read and manage Users, their Account links and password setup links; list Roles | ✓ | — | — |
| `oidc:read`, `oidc:write` | Read and change the OIDC settings, run its check | ✓ | — | — |
| `service-accounts:read`, `service-accounts:write` | Read and manage Service accounts and their tokens | ✓ | — | — |
| `organization:read` | Read the outbound address policy, the Keyring status and the severity values seen | ✓ | — | — |
| `organization:write` | Change Organization settings, the outbound address policy and the outgoing heartbeat | ✓ | — | — |
| `audit-log:read`, `system-status:read` | Read the Audit log and the System status | ✓ | — | — |

Without a Permission, for every signed-in identity: own profile, TOTP, Account links and Personal access tokens (the
operations that change them need the web session, C-03.FR-27), the profile's short list of Connections to link an
account through (only the id, the name and the messenger of each, C-18.FR-10), the basic read of the `organization`
resource (the outgoing heartbeat URL masked), system notices and live updates. A bulk command needs the Permission of
the command it runs. A Personal access token can only narrow its owner's Permissions; a Service account has its Role's
Permissions but cannot become an Owner (C-04.FR-2).

**Secret masking.** Secret values are write-only for everyone, Admins included (C-03.FR-21): bot tokens, the OIDC
client secret, proxy passwords, outgoing webhook Secrets and Signing secrets, the outgoing heartbeat URL and master
keys. Reads return only whether a value is set and when it last changed; Audit log diffs show only that it changed;
test results, previews, Timeline errors and logs show it masked. Outgoing webhook URL and header templates are readable
by everyone who can read Destinations because they contain only references (`{{ .Secrets.<name> }}`), never the values
(C-15.FR-10). A Personal access token can only narrow its owner's Permissions.

## Internal alerts

The closed L1 registry (C-06.FR-14). Each is an Alert of the built-in "Muster" Integration, routed by ordinary Routes,
deduplicated by fingerprint, resolved explicitly and never Gone or Stale. A configuration entity appears by its immutable
id, with its current name in a separate `*_name` label; the fingerprint is computed from `alertname` and the id labels
only, so renaming the entity updates the name label of the open Internal alert instead of starting a new one. An
Internal alert and a chart rule for the same condition share one name and one runbook page.

| Internal alert | Severity | Raised while | Labels besides `alertname` and `severity` | Raised by |
|---|---|---|---|---|
| `MusterSnapshotTruncated` | warning | an Integration has a truncated `groupKey` | `integration`, `integration_name` | C-06 |
| `MusterHeartbeatLost` | critical | an Integration is Heartbeat lost | `integration`, `integration_name`, the Integration's Static labels | C-07 |
| `MusterDestinationBroken` | critical | a Destination is Broken | `destination`, `destination_name` | C-11 |
| `MusterTemplateError` | warning | a template of a Route or Destination keeps failing | `route` and `route_name`, or `destination` and `destination_name`; `template` | C-12, C-15 |
| `MusterOIDCSecretExpiring` | warning | the OIDC client secret expires within `oidc.secret_expiry_lead` | — | C-19 |

## Metrics catalogue

The L1 catalogue. The generated reference page (C-02.FR-19) is built from the registry in code; CI checks it against
this table (C-19.FR-2). Entity labels carry immutable ids; names come from the `*_info` metrics. Every other label takes
its values from a closed set defined in code (ADR-0014); where this table lists the values, the list is the whole set.
Histograms use `le` buckets; time to acknowledge and time to resolve use buckets from 1 minute to 24 hours.
`muster_alert_group_time_to_ack_seconds` is observed once per Alert Group, at its first acknowledgement: Takeover,
Unacknowledge and Reopen do not observe it again, and an Alert Group closed without an acknowledgement is not observed.

| Metric | Type | Labels | Exported from |
|---|---|---|---|
| `muster_build_info` | gauge | `version`, `commit` | C-02 |
| `muster_leader` | gauge | — | C-02 |
| `muster_clock_skew_seconds` | gauge | — | C-02 |
| `muster_client_requests_total` | counter | `client` (`delivery`, `interactive`, `background`, `heartbeat` — the client classes of ADR-0015), `outcome` (`ok`, `retry_after`, `transient`, `fatal`, `unknown`, `blocked`, `redirect`) | C-02 |
| `muster_client_request_duration_seconds` | histogram | `client`, `outcome` — the same sets | C-02 |
| `muster_db_pool_connections` | gauge | `state` (`acquired`, `idle`, `constructing`) — connections of the replica's main database pool | C-02 |
| `muster_db_pool_max_connections` | gauge | — | C-02 |
| `muster_db_pool_acquires_total` | counter | — | C-02 |
| `muster_db_pool_acquire_wait_seconds_total` | counter | — (the total time spent waiting for a free connection) | C-02 |
| process and Go runtime metrics (`process_*`, `go_*`) | — | — | C-02 |
| `muster_api_requests_total` | counter | `route_pattern`, `method`, `code` | C-03 |
| `muster_api_request_duration_seconds` | histogram | `route_pattern`, `method` | C-03 |
| `muster_login_failures_total` | counter | `method` (`local` — a wrong login or password; `oidc` — a refused OIDC sign-in; `totp` — a wrong TOTP or recovery code) | C-03 |
| `muster_oidc_checks_total` | counter | `outcome` (`ok`, `refused`, `unavailable`, `skipped`) — background re-checks of OIDC users at the IdP; `skipped` when the user has no live session and no usable Personal access token (C-03.FR-30) | C-03 |
| `muster_ingest_requests_total` | counter | `integration` (`unknown` without a matching token), `outcome` (`accepted`, `unauthorized`, `too_large`) | C-05 |
| `muster_ingest_request_duration_seconds` | histogram | — | C-05 |
| `muster_integration_info` | gauge | `integration`, `name` | C-05 |
| `muster_ingest_processing_delay_seconds` | histogram | `integration` | C-06 |
| `muster_ingest_backlog` | gauge (Leader) | — | C-06 |
| `muster_ingest_truncated_snapshots_total` | counter | `integration` | C-06 |
| `muster_ingest_failed_snapshots_total` | counter | `integration` — a Stored Snapshot that could not be processed (C-06.FR-20) | C-06 |
| `muster_alerts_resolved_total` | counter | `integration`, `reason` (`resolved`, `gone`, `stale`, `integration_deleted`) | C-06 |
| `muster_ingest_resolved_dropped_total` | counter | `integration` — a `resolved` for a fingerprint that fires nowhere (C-06.FR-4) | C-06 |
| `muster_heartbeat_lost` | gauge (Leader) | `integration` | C-07 |
| `muster_route_info` | gauge | `route`, `name` | C-08 |
| `muster_alert_groups` | gauge (Leader) | `route`, `status` | C-09 |
| `muster_alert_groups_created_total` | counter | `route` | C-09 |
| `muster_alert_groups_reopened_total` | counter | `route` | C-09 |
| `muster_alert_groups_resolved_total` | counter | `route`, `by` (`user`, `system`) | C-09 |
| `muster_alert_group_time_to_resolve_seconds` | histogram | `route` | C-09 |
| `muster_alert_group_time_to_ack_seconds` | histogram | `route` | C-10 |
| `muster_commands_total` | counter | `command` (`acknowledge`, `unacknowledge`, `resolve`, `unresolve`, `snooze`, `unsnooze`, `add_note`, `still_on_it`), `transport` (`ui`, `api`, `mattermost`, `telegram`, `system`), `outcome` (`done`, `unchanged`, `refused`, `skipped`, `failed`) | C-10 |
| `muster_delivery_attempts_total` | counter | `destination`; `kind` (`publication`, `update`, `thread_reply`, `storm_summary`, `final_edit`, `webhook_event`); `outcome` (`delivered`, `markup_rejected`, `retry_after`, `transient`, `fatal`, `unknown`, `template_error`) | C-11 |
| `muster_delivery_latency_seconds` | histogram | `destination` | C-11 |
| `muster_delivery_queue` | gauge (Leader) | `destination` | C-11 |
| `muster_destination_broken` | gauge (Leader) | `destination` | C-11 |
| `muster_storm_active` | gauge (Leader) | `route` | C-11 |
| `muster_destination_info` | gauge | `destination`, `name` | C-11 |
| `muster_template_errors_total` | counter | `route`, `destination` (one of them empty), `template` (`root_message`, `line`, `ack_timeout_notice`, `link_rule`, `webhook_request`) | C-12 |
| `muster_template_render_duration_seconds` | histogram | `template` (as above) | C-12 |

## Chart rules

Rendered by the chart when the Prometheus or VictoriaMetrics CRDs are enabled (C-19.FR-3). Each rule has a
`description` and a `runbook_url`; thresholds, windows and the job selector (`job="muster"` below) are chart values.
The default expressions are covered by rule unit tests (C-19.AC-8). The documentation tells
users to send the critical rules through an Alertmanager route that bypasses Muster.

| Rule | Severity | Default expression | For | Meaning |
|---|---|---|---|---|
| `MusterDown` | critical | `up{job="muster"} == 0` | 2m | the scrape target is down |
| `MusterNoLeader` | critical | `max(muster_leader{job="muster"}) == 0` | 2m | no replica leads: Telegram polling, Heartbeat lost and Stale checks, partitions and the outgoing heartbeat have stopped; ingestion, delivery and timers keep working |
| `MusterDeliveryFailing` | critical | `max by (destination) (muster_delivery_queue) > 0 unless on (destination) sum by (destination) (increase(muster_delivery_attempts_total{outcome="delivered"}[10m])) > 0` | 5m | a Destination has queued deliveries and no successful delivery for 10 minutes |
| `MusterDestinationBroken` | critical | `max by (destination) (muster_destination_broken) == 1` | 5m | a Destination is Broken |
| `MusterHeartbeatLost` | critical | `max by (integration) (muster_heartbeat_lost) == 1` | 1m | an Integration is Heartbeat lost; the backup for the Internal alert |
| `MusterDeliverySlow` | warning | `histogram_quantile(0.95, sum by (le) (rate(muster_delivery_latency_seconds_bucket[15m]))) > 5` | 15m | delivery p95 above the objective of NFR-2 |
| `MusterDeliveryQueueGrowing` | warning | `max by (destination) (muster_delivery_queue) > 100 and max by (destination) (deriv(muster_delivery_queue[15m])) > 0` | 15m | a delivery queue keeps growing |
| `MusterIngestBacklog` | warning | `max(muster_ingest_backlog) > 1000 or histogram_quantile(0.95, sum by (le) (rate(muster_ingest_processing_delay_seconds_bucket[5m]))) > 60` | 5m | processing falls behind |
| `MusterIngestRejected` | warning | `sum by (integration, outcome) (increase(muster_ingest_requests_total{outcome="too_large"}[15m])) > 0 or sum by (integration, outcome) (increase(muster_ingest_requests_total{outcome="unauthorized"}[15m])) > 10` | 0m | an oversized ingestion request, or more than 10 unauthorized ones, arrived in the last 15 minutes |
| `MusterTemplateError` | warning | `sum by (route, destination, template) (increase(muster_template_errors_total[15m])) > 0` | 0m | templates failed in the last 15 minutes |
| `MusterClockSkew` | warning | `max by (instance) (abs(muster_clock_skew_seconds)) > 2` | 5m | a replica's clock is off |
| `MusterLoginFailures` | warning | `sum by (method) (increase(muster_login_failures_total[10m])) > 20` | 0m | a burst of failed sign-ins |

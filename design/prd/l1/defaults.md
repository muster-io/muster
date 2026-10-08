# L1 defaults

[L1 index](../L1.md)

The single list of configurable values and built-in limits of L1. Requirements cite a value by its name — for example
"the Reopen window (`route.reopen_window`)" — and never repeat the number. "Set on" says where the value can be changed:
the environment, the Helm chart, the Organization, an Integration, a Route, a Connection, a Destination, a token, a
request, or nowhere (built in). The last column is "decided", or the item `P-NN` of the
[open questions](../L1.md#5-open-questions) when the value is _provisional_. A story that changes a value updates this
table in the same pull request.

## Environment

| Name | Default | Set on | Capability | Status |
|---|---|---|---|---|
| `MUSTER_DATABASE_URL` | unset; required unless the `MUSTER_DATABASE_*` fields are set; wins over them, with a warning | environment | C-02 | decided |
| `MUSTER_DATABASE_HOST`, `_NAME`, `_USER`, `_PASSWORD` / `_PASSWORD_FILE` | unset | environment | C-02 | decided |
| `MUSTER_DATABASE_PORT` | `5432` | environment | C-02 | decided |
| `MUSTER_DATABASE_SSLMODE` | `prefer`; `muster doctor` and the startup log report whether each connection is encrypted | environment | C-02 | decided |
| `MUSTER_DATABASE_SESSION_URL`, or `MUSTER_DATABASE_SESSION_HOST` and `_PORT` | the main connection; other fields inherited | environment | C-02 | decided |
| `MUSTER_SECRET_KEYS` / `_FILE` | none (required) | environment | C-02 | decided |
| `MUSTER_PUBLIC_URL` | none (required) | environment | C-02 | decided |
| `MUSTER_INGEST_URL` | `MUSTER_PUBLIC_URL` | environment | C-02 | decided |
| `MUSTER_LISTEN_APP` / `MUSTER_LISTEN_INGEST` / `MUSTER_LISTEN_INTERNAL` | `:8080` / `:8081` / `:8082` (not `:9090`, which Prometheus uses by default); the compose example sets ingest to the app address | environment | C-02 | decided |
| `MUSTER_LOG_LEVEL` | `info`; one of `info`, `warn` and `error`, the levels of the log event registry | environment | C-02 | decided |
| `MUSTER_MIGRATE_ON_START` | off; on in the compose example; the chart migrates in an init container | environment | C-02 | decided |
| `MUSTER_BOOTSTRAP_ADMIN_EMAIL`, `MUSTER_BOOTSTRAP_ADMIN_PASSWORD` / `_FILE` | unset; the email is also the bootstrap Admin's login | environment | C-02, C-03 | decided |
| `MUSTER_TRUSTED_PROXIES` | empty: a request's client address is its socket address; with CIDR networks listed (comma-separated), a request whose socket address is inside them takes the first address of `X-Forwarded-For`, read from the right, that is outside them | environment | C-02, C-03, C-04 | decided |
| `MUSTER_RUNBOOK_BASE_URL` | `https://muster-io.github.io/muster`, the published documentation site; the base of the `runbook_url` of Internal alerts; the chart sets it from `alerting.runbookBaseURL` | environment | C-02, C-19 | decided |

## Process and runtime

| Name | Default | Set on | Capability | Status |
|---|---|---|---|---|
| `leader.ping_interval` / `leader.fencing_timeout` / `leader.server_bound` | 5 s / 15 s / 30 s | built in | C-02 | decided |
| `leader.alive_mark_interval` | 30 s | built in | C-02 | decided |
| `leader.absence_notice` | 2 min without a Leader | built in | C-02 | decided |
| `replica.key_record_refresh` / `replica.live_expiry` | 30 s / 2 min | built in | C-02 | decided |
| `replica.prune_after` | 1 h without a refresh; then the Leader removes the replica's record | built in | C-02 | decided |
| `recovery.banner_duration` | from the takeover of the Leader that records the downtime until the longest learned repeat interval has passed, at most 1 h; 15 min when nothing is learned | built in | C-02 | decided |
| `process.clock_skew_warning` | 2 s | built in | C-02 | decided |
| `process.shutdown_grace` | 20 s (the chart's termination grace period is 30 s) | built in | C-02 | decided |
| `live.check_interval` | 5 s: each replica checks that the sessions of its live-updates streams are still usable, closing the others, and evaluates the Organization-wide notices, sending a hint when they changed | built in | C-02, C-03 | decided |
| `live.keepalive_interval` / `live.retry` | 25 s / 3 s: a stream without hints sends a comment every 25 s; the stream asks the browser to reconnect 3 s after it closes | built in | C-03, C-09 | decided |
| `live.max_streams` / `live.max_streams_per_session` | 1000 per replica / 10 per session (one per open tab); a stream beyond them gets `429`, and a stream that falls 32 hints behind is closed, so that its client reconnects and reads everything again | built in | C-03, C-09 | decided |
| Helm `replicas` | 1 | Helm chart | C-01 | decided |

## Sign-in and tokens

| Name | Default | Set on | Capability | Status |
|---|---|---|---|---|
| `organization.totp_required` | nobody | Organization | C-03 | decided |
| `auth.session_idle_timeout` / `auth.session_lifetime` | 12 h / 7 days | built in | C-03 | decided |
| `auth.oidc_recheck_interval` | 15 min; a background re-check of every OIDC user with an offline token and a live session or a usable Personal access token | built in | C-03 | decided |
| `auth.oidc_fallback_session_lifetime` | 12 h from sign-in, for an OIDC session of a user without an offline token | built in | C-03 | decided |
| `auth.oidc_token_grace` | 7 days since the owner's last OIDC sign-in; only for OIDC accounts without an offline token | Organization | C-04, C-20 | decided |
| `auth.password_setup_link_ttl` | single use, 24 h | built in | C-03 | decided |
| `auth.password_setup_prune_after` | 7 days after a password setup link expired, used and superseded links included, so that a late click still answers `410` instead of `404`; then the Leader deletes the row (`short_lived_pruning`, hourly) | built in | C-03 | decided |
| `auth.password_min_length` | 12 characters | built in | C-03 | decided |
| `auth.signin_throttle` | after 3 consecutive failures per account or source address, each attempt waits twice as long as the previous, from 1 s up to 60 s; a password, TOTP code or recovery code counts alike, and while one attempt of an account is evaluated, another one of it waits 1 s, so that parallel guesses cannot pass the block | built in | C-03 | decided |
| `auth.session_prune_after` / `auth.signin_throttle_prune_after` | 7 days after a session ended, by sign-out, an administrative action or its idle timeout or lifetime / 24 h after a throttle's last failure, once its block has passed; then the Leader deletes the row (`short_lived_pruning`, hourly) | built in | C-02, C-03 | decided |
| `auth.totp_recovery_codes` | 10 single-use codes of ten characters, argon2id-hashed; regenerating replaces the unused ones | built in | C-03 | decided |
| `oidc.unmatched_role` | none (refuse sign-in) | OIDC settings | C-03 | decided |
| `oidc.sync_role` | on | OIDC settings | C-03 | decided |
| `oidc.skip_totp_with_idp_mfa` | off | OIDC settings | C-03 | decided |
| `oidc.secret_expiry_lead` | 14 days before the entered expiry date | built in | C-03, C-19 | decided |
| `oidc.auth_request_ttl` | 10 min for an OIDC redirect (sign-in or link) to come back to the callback | built in | C-03 | decided |
| `token.expiry` | none (the UI warns) | token | C-04 | decided |
| `api.rate_limit` | 20 requests per second per token, bursts of 100, counted on each replica; web sessions and ingestion are not limited | built in | C-04 | decided |
| `api.page_size` | 50, maximum 500 | request | C-09 | decided |

## Integrations and processing

| Name | Default | Set on | Capability | Status |
|---|---|---|---|---|
| `integration.connection_mode` | `webhook-only` (the only mode in L1) | Integration | C-05 | decided |
| Static labels | none | Integration | C-05 | decided |
| `integration.duplicate_window` | 45 s (the copies of an HA pair came 0 to 15 s apart, [F-038 to F-040](../../facts.md#ha-pairs)) | Integration | C-05 | decided |
| `ingest.body_limit` | 16 MB | built in | C-05 | decided |
| `snippet.repeat_interval` | 10 min (recommended range 5–15 min) | snippet | C-05 | decided |
| `processing.gone_min_absence` | 5 min, from the first Snapshot that missed the Alert (Prometheus re-sends firing alerts only every 2 min at a 1-min evaluation interval, [F-043](../../facts.md#restarts-and-resolves)) | built in | C-06 | decided |
| `processing.stale_after_factor` | 3 × the learned repeat interval | built in | C-06 | decided |
| `processing.stale_after_unlearned` | 25 h | built in | C-06 | decided |
| `processing.repeat_samples` | the last 9 observed gaps of an Alertmanager route; the learned repeat interval is their median | built in | C-06 | decided |
| `processing.long_repeat_warning` | learned repeat interval above 1 h | built in | C-06 | decided |
| `integration.heartbeat` | off (the UI warns) | Integration | C-07 | decided |
| `integration.heartbeat_timeout` | 5 min | Integration | C-07 | decided |
| `snippet.heartbeat_repeat_interval` | 1 min | snippet | C-07 | decided |

## Routing and lifecycle

Route policy fields with their values for the two Route profiles (C-08.FR-7). A profile only pre-fills a new Route.

| Name | On-call (default) | Informational | Capability | Status |
|---|---|---|---|---|
| Matchers | none (the Default route) | — | C-08 | decided |
| `route.urgent` | off | off | C-08 | decided |
| `route.group_key` | `alertname`, `severity`, `cluster` | same | C-08 | decided |
| `route.reopen_window` | 15 min | 15 min | C-09 | decided |
| `route.grace_period` | 15 min | 15 min | C-09 | decided |
| `route.urgent_rise_removes_ack` | on | on | C-09 | decided |
| `route.snooze_durations` | 1 h / 4 h / 24 h | 1 d / 3 d / 7 d | C-10 | decided |
| `route.thread_batching_window` | 60 s | 60 s | C-11 | decided |
| `route.storm_threshold` | 20 new Alert Groups per minute | same | C-11 | decided |
| `route.language` | English | English | C-12 | decided |
| Route templates | built in (English and Russian) | same | C-12 | decided |
| `route.ack_timeout` | on; first interval 15 min, doubling, at most 3 notices | off | C-17 | decided |
| `route.reminders` | on; first after 4 h, doubling up to 24 h | off | C-17 | decided |
| `route.auto_unacknowledge` | off | off | C-17 | decided |

Other lifecycle values:

| Name | Default | Set on | Capability | Status |
|---|---|---|---|---|
| `routing.group_key_preview_period` | last 24 h (at most `retention.stored_snapshots`) | request | C-08 | decided |
| `routing.group_key_preview_examples` | the 5 largest Alert Groups for each key | built in | C-08 | decided |
| `routing.group_key_preview_max_alerts` | 10,000 Alerts, the newest first (the active Alerts of NFR-1) | built in | C-08 | decided |
| `alert_group.list_range` | last 7 days (lifetime overlapping the range) | request | C-09 | decided |
| `alert_group.note_max_length` | 4,000 characters | built in | C-10 | decided |
| `alert_group.bulk_max` | 100 Alert Groups per bulk command | built in | C-10 | decided |
| `timers.auto_unacknowledge_after` | 2 unanswered Reminders in a row | built in | C-17 | decided |

## Delivery and messages

| Name | Default | Set on | Capability | Status |
|---|---|---|---|---|
| `delivery.interactive_budget` | 5 s | built in | C-11 | decided |
| `delivery.transient_backoff` | exponential with jitter, from 2 s up to 5 min | built in | C-11 | decided |
| `delivery.transient_budget` | 10 attempts within 30 min; then the Destination is Broken | built in | C-11 | decided |
| `delivery.broken_probe_interval` | 5 min; the probe is the oldest waiting delivery, or the Destination check when nothing waits | built in | C-11 | decided |
| `delivery.storm_calm_period` | 5 min below the threshold | built in | C-11 | decided |
| `delivery.thread_alerts_listed` | first 10 new Alerts, then "…and K more" | built in | C-11 | decided |
| `message.alerts_listed` | 10 lines; above that, the distinct values of each differing label, at most 10 per label | built in | C-12 | decided |
| `template.dry_run_sample` | up to 20 most recent Stored Snapshots of the Route | built in | C-12 | decided |
| `template.output_cap` | about 50,000 characters | built in | C-12 | decided |
| `message.value_cap` | 4 KB per label or annotation value | built in | C-12 | decided |
| `lookup_table.size_max` | 50 columns and 10,000 rows per Lookup table; a name or a column name up to 200 characters, the description up to 2,000, a key or a value up to 4,096; above that `422` (`too_long`) | built in | C-12 | P-46 |
| `template.lookups_per_render` | 100 rows read by `lookup` in one render; the 101st is a template error | built in | C-12 | P-47 |
| `lookup_table.page_max` | 50 Lookup tables per page of `listLookupTables`, since each carries its rows; a larger `limit` is lowered to it | built in | C-12 | P-48 |
| `destination.mentions` | nobody, for every kind of Loud event | Destination | C-12 | decided |

## Connections and Destinations

| Name | Default | Set on | Capability | Status |
|---|---|---|---|---|
| Proxy (each client) | off | Connection, Destination, OIDC settings, Organization (outgoing heartbeat) | C-02, C-03 | decided |
| `connection.mattermost.limiter` | 5 requests/s (half of the 10 per second per client address that Mattermost's rate limit allows when an admin turns it on — it is off by default; Connections of one installation to the same server usually share that bucket, [F-030](../../facts.md#rate-limit-and-server-settings)) | Connection | C-13 | decided |
| `destination.mattermost.limiter` | the same as its Connection's (Mattermost limits per client address, not per channel, [F-030](../../facts.md#rate-limit-and-server-settings)) | Destination | C-13 | decided |
| `connection.telegram.limiter` | 15 messages/s | Connection | C-14 | P-28 |
| `destination.telegram.limiter` | 10 requests/min, sends and edits together, in the channel and its discussion group (half of Telegram's measured limit of about 20 per minute per chat, [F-016](../../facts.md#limits)) | Destination | C-14 | decided |
| `connection.telegram.update_mode` | long polling | Connection | C-14 | decided |
| `connection.telegram.bot_api_base_url` | `https://api.telegram.org` | Connection | C-14 | decided |
| `telegram.copy_wait` | 60 s for the automatic copy of a post | built in | C-14 | P-30 |
| `telegram.press_max_age` | presses older than 1 h are dropped | built in | C-14 | decided |
| `destination.webhook.limiter` | 5 requests/s | Destination | C-15 | decided |

## Account links

| Name | Default | Set on | Capability | Status |
|---|---|---|---|---|
| `account_link.telegram_token_ttl` | single use, 10 min | built in | C-18 | decided |
| `account_link.mattermost_code` | 8 characters without look-alikes, valid 10 min, at most 5 attempts | built in | C-18 | decided |
| `account_link.mattermost_code_requests` | 5 per hour per requesting User; 3 per hour per target account | built in | C-18 | decided |

## Organization

| Name | Default | Set on | Capability | Status |
|---|---|---|---|---|
| `organization.name` | `Muster` | Organization | C-02, C-20 | decided |
| `organization.time_zone` | UTC (for messages) | Organization | C-02, C-20 | decided |
| `organization.severity_label` | `severity` | Organization | C-02, C-20 | decided |
| `organization.severity_mapping` | `critical` → critical; `warning` → warning; `info` and `none` → info; any other value → warning, shown as received; label missing → info | Organization | C-08, C-20 | decided |
| `organization.severity_styles` | the emoji and colour of each Severity level: critical 🟥 `#d32f2f`, warning 🟧 `#f57c00`, info 🟦 `#1976d2` (squares, so that they never look like the status colours of messages) | Organization | C-02, C-20 | decided |
| `organization.severity_values_window` | values seen in the last 14 days | built in | C-20 | decided |
| `organization.critical_is_urgent` | on | Organization | C-08, C-20 | decided |
| `organization.instance_labels` | `pod`, `instance`, `container`, `endpoint` | Organization | C-09, C-20 | decided |
| `retention.stored_snapshots` | 14 days | Organization | C-05, C-20 | decided |
| `retention.alert_details` | 90 days (Alerts in Alert Groups, Timeline entries, delivery events; never Notes) | Organization | C-09, C-20 | decided |
| `retention.alert_group_summaries` | 2 years (summary rows with their Notes); at least `retention.alert_details`, otherwise `422` | Organization | C-09, C-20 | decided |
| `retention.audit_log` | 1 year | Organization | C-03, C-20 | decided |
| `organization.outbound_policy` | standard (private allowed, loopback blocked) | Organization | C-02, C-20 | decided |
| `organization.outbound_allowed` / `organization.outbound_denied` | empty | Organization | C-02, C-20 | decided |

## Self-observation and chart

| Name | Default | Set on | Capability | Status |
|---|---|---|---|---|
| `outgoing_heartbeat.url` | unset (nothing is sent) | Organization | C-19 | decided |
| `outgoing_heartbeat.interval` | 1 min | built in | C-19 | decided |
| `outgoing_heartbeat.method` | `GET` | built in | C-19 | decided |
| `outgoing_heartbeat.timeout` | 10 s for one request; no retry, the next tick is the retry | built in | C-19 | decided |
| `alerting.runbookBaseURL` | `https://muster-io.github.io/muster`; the base of the chart rules' `runbook_url`, passed to Muster as `MUSTER_RUNBOOK_BASE_URL` | Helm chart | C-19 | decided |
| Chart rule thresholds, windows and job selector | as in [reference.md](reference.md#chart-rules) | Helm chart | C-19 | decided |

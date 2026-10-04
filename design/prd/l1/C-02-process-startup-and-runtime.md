# C-02. Process startup and runtime

[L1 index](../L1.md) · Stage: Foundation · UI: none · Depends on: C-01

**Goal.** Muster starts from bootstrap settings in the environment, migrates its schema, refuses to run with the wrong
keys, elects a Leader, reports health, recovers cleanly after downtime, and gives every later capability the shared
machinery it builds on: the log event and metric registries, the outbound HTTP package with the outbound address
policy and per-client proxies, and the Organization with its default settings.

Names in the form `area.setting` and `MUSTER_*` refer to rows of [defaults.md](defaults.md).

## Scenarios

1. An Admin installs the chart with an existing Secret. An init container runs `muster migrate`, the pod becomes ready
   and the bootstrap admin can sign in once C-03 is merged.
2. A replica starts with master keys that belong to another database and stops with "master key does not match the
   database".
3. An installation whose main database connection goes through a transaction-pooling PgBouncer, with no separate
   session connection, fails at startup with an error that names `MUSTER_DATABASE_SESSION_URL`.
4. Two replicas run; the node of the Leader disappears; the other replica leads within a minute.
5. Muster was down for two hours. The Leader records the period, and the UI says for a while that data may be
   incomplete.
6. After restoring a backup, the Admin runs `muster doctor`, which checks the database, the pooler mode, TLS of the
   database connections, the key canary and clock skew.
7. The chart passes the database password from a Secret key as `MUSTER_DATABASE_PASSWORD_FILE`, with host, name and
   user as plain values.

## Functional requirements

- **C-02.FR-1** The environment holds only bootstrap and infrastructure settings (ADR-0010), all named `MUSTER_*`:
  - the database, as `MUSTER_DATABASE_URL` or as the fields `MUSTER_DATABASE_HOST`, `MUSTER_DATABASE_PORT`,
    `MUSTER_DATABASE_NAME`, `MUSTER_DATABASE_USER`, `MUSTER_DATABASE_PASSWORD` (or `MUSTER_DATABASE_PASSWORD_FILE`) and
    `MUSTER_DATABASE_SSLMODE`; when both forms are set, the URL wins and a warning naming both is logged;
  - the session-capable connection, as `MUSTER_DATABASE_SESSION_URL` or as `MUSTER_DATABASE_SESSION_HOST` and
    `MUSTER_DATABASE_SESSION_PORT` with every other field inherited from the main connection; without either, the main
    connection is used;
  - `MUSTER_SECRET_KEYS` or `MUSTER_SECRET_KEYS_FILE`; `MUSTER_PUBLIC_URL`; `MUSTER_INGEST_URL`;
  - `MUSTER_LISTEN_INGEST`, `MUSTER_LISTEN_APP`, `MUSTER_LISTEN_INTERNAL`; `MUSTER_LOG_LEVEL`;
    `MUSTER_MIGRATE_ON_START`;
  - `MUSTER_TRUSTED_PROXIES`, a comma-separated list of CIDR networks of the reverse proxies in front of Muster, empty
    by default. A request's client address is its socket address, unless that address is inside a listed network:
    then it is taken from `X-Forwarded-For`, read from the right, as the first address outside the listed networks
    (the left-most one when all of them are listed), so a client cannot choose it by sending the header itself. What
    acts per source address uses the client address: sign-in throttling (C-03.FR-4) and the last use of a token
    (C-04.FR-3);
  - `MUSTER_RUNBOOK_BASE_URL`, the base URL of the runbook pages that Internal alerts link to (C-19.FR-9), by default
    the published documentation site;
  - `MUSTER_BOOTSTRAP_ADMIN_EMAIL` with `MUSTER_BOOTSTRAP_ADMIN_PASSWORD` or `MUSTER_BOOTSTRAP_ADMIN_PASSWORD_FILE`,
    parsed here and used by C-03.FR-22.

  Any secret can come from a `*_FILE` variable. An invalid value stops startup with an error naming the variable;
  nothing falls back silently. `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY` are ignored.
- **C-02.FR-2** `MUSTER_PUBLIC_URL` is required and is used for links to people (messages, profile, OIDC redirect).
  `MUSTER_INGEST_URL` is used for machines (Mattermost button callbacks, Alertmanager receiver snippets, Heartbeat
  URLs, the Telegram webhook) and defaults to `MUSTER_PUBLIC_URL`. Neither is derived from an ingress.
- **C-02.FR-3** Muster serves three listeners: **ingest** (ingestion, Heartbeat, Mattermost callbacks, Telegram
  webhook), **app** (UI and API) and **internal** (`/metrics`, `/health/live`, `/health/ready`). When
  `MUSTER_LISTEN_INGEST` and `MUSTER_LISTEN_APP` hold the same address, both are served on that one port; the compose
  example merges them, the chart keeps them apart with a Service each.
- **C-02.FR-4** `/health/live` performs no checks; `/health/ready` checks only the database. The chart defines a startup
  probe so that slow starts are not killed.
- **C-02.FR-5** At startup Muster refuses PostgreSQL older than 14 and verifies that advisory locks and `LISTEN` work
  over the session connection; either failure stops startup with an error that names the cause and, for a pooler, the
  session variables.
- **C-02.FR-6** Migrations run through `muster migrate` or `MUSTER_MIGRATE_ON_START` (on in compose, off in the chart,
  where an init container runs `muster migrate`), under a session advisory lock, logging the versions applied. A binary
  refuses to start on a schema newer than it knows, naming both versions.
- **C-02.FR-7** The Keyring is loaded from the environment; each key's id is derived from its material. On a new
  database the first key becomes active and the key canary is written under the migration lock. Every start decrypts
  the canary; failure stops the replica with "master key does not match the database". Without any key, or with the
  compose placeholder, Muster refuses to start (ADR-0011).
- **C-02.FR-8** Each replica records the key ids it holds every `replica.key_record_refresh` and counts as live while
  its record is younger than `replica.live_expiry`. A running replica that sees an active key it does not hold stops
  with the canary error.
- **C-02.FR-10** One replica becomes Leader through a session advisory lock and runs only the singleton tasks: Telegram
  polling, the Heartbeat lost and Stale checks, partition maintenance, database gauges, the "alive" mark, the outgoing
  heartbeat and the OIDC client secret expiry check that raises `MusterOIDCSecretExpiring` (C-19.FR-5) — each added by
  its capability; nothing else runs only on the Leader. The Leader pings over the lock session every
  `leader.ping_interval` and stops all singleton work if no ping succeeded for `leader.fencing_timeout`; the server ends
  a silent lock session within `leader.server_bound` (ADR-0007).
- **C-02.FR-11** The Leader creates partitions ahead of time — Stored Snapshots daily, Timeline entries and other events
  monthly — and enforces the retention periods of the Organization (`retention.*`) by dropping whole partitions.
- **C-02.FR-12** The Leader writes an "alive" mark every `leader.alive_mark_interval`. After downtime, the next Leader
  records the unavailability period, logs it as a registered event and opens a recovery window that lasts
  `recovery.banner_duration`; while it is open, the Organization-wide notice "recovering after downtime" is active
  (shown by C-03). Every timer row that became overdue during the downtime fires once, collapsed (C-09, C-17). The
  Timeline entries for open Alert Groups are C-09.FR-18.
- **C-02.FR-13** Muster exports `muster_clock_skew_seconds` (replica clock against the database clock) and logs a
  warning when the skew exceeds `process.clock_skew_warning`.
- **C-02.FR-14** `muster doctor` only reads. It checks database reachability, the PostgreSQL version, the pooler mode of
  both connections, whether each connection uses TLS, the key canary, the Keyring status (which keys are still needed),
  clock skew and — once they exist (C-13, C-14) — the check of every Connection and every Destination that has one; it
  prints one line per check and exits non-zero if any fails. Because `prefer` falls back to an unencrypted connection
  without an error, the TLS line names the mode in effect and warns, without failing, when a connection is unencrypted;
  at startup Muster logs the same as a registered event, at WARN when a connection is unencrypted.
- **C-02.FR-15** The CLI offers only emergency, bootstrap and diagnostic subcommands: `muster migrate` and
  `muster doctor` here; `muster admin reset-password` and `muster admin reset-totp` (C-03); `muster ingest replay`
  (C-06); `muster secrets rotate-key --activate <id>` (C-20). Those that change data on a person's request require
  `--actor <name>` and write it to the Audit log; `migrate` and `doctor` take no `--actor`.
- **C-02.FR-16** On SIGTERM, Muster logs a warning, stops claiming new work, finishes or releases what it holds within
  `process.shutdown_grace`, and exits.
- **C-02.FR-17** Logs are structured JSON lines on stdout, written only through the domain logger and only as
  registered log events (FR-18).
- **C-02.FR-18** Log events form a closed registry in code (ADR-0014): the logger accepts only registered events; a
  reference page listing each event with its level and fields is generated from the registry; CI fails on an
  unregistered event or a stale page. Levels: ERROR — someone must look within the hour; WARN — needed for an
  investigation; INFO — an investigation is impossible without it. Lines are self-contained (`group=#412`, `route=…`,
  `integration=…`) and never carry a secret. Each capability registers its own events.
- **C-02.FR-19** Metrics are scraped from `/metrics` on the internal listener; Muster never pushes. Metrics form a
  registry in code from which a reference page is generated; CI checks that the page is current. Labels follow
  ADR-0014: configuration entities by immutable id (`integration`, `route`, `destination`) with names in `*_info`
  metrics; never alert labels, Alert Group numbers, users or the Organization; all other labels from closed sets.
  Histograms use `le` buckets only. Database gauges are exported only by the Leader. Each capability exports the
  metrics that [reference.md](reference.md#metrics-catalogue) assigns to it; this one exports the platform metrics
  assigned to C-02.
- **C-02.FR-20** Every outbound HTTP request goes through one outbound package (ADR-0015): clients are built per client
  class (delivery, interactive, background, heartbeat) with a connect timeout and an overall timeout; redirects are
  never followed and the error names the target; `Retry-After` and Telegram's `retry_after` are honoured exactly;
  unknown status codes are not retried; an error in the body of a successful response is classified like a status
  code; the provider's error text is kept as a separate, untrusted field. Requests are counted in
  `muster_client_requests_total{client,outcome}` and `muster_client_request_duration_seconds{client,outcome}`. Secrets
  that travel in URLs are redacted from every log line and every returned error, including the URL inside Go's
  `url.Error`.
- **C-02.FR-21** The outbound package enforces the Organization's outbound address policy
  (`organization.outbound_policy`) on every address a name resolves to, inside the dialer, and connects only to an
  address that passed. **Standard** allows private addresses and blocks loopback unless an allowed network includes it;
  **strict** allows only public addresses plus allowed networks. In both, link-local addresses, known cloud metadata
  addresses, unspecified and multicast addresses are always blocked, and no allowed entry opens them. The Organization's
  lists of allowed and denied networks, host names and domains (`organization.outbound_allowed`,
  `organization.outbound_denied`) apply; a denied entry always wins. A blocked request fails without retry, and the
  error names the rule. Editing the policy is C-20.
- **C-02.FR-22** Each outbound client configuration may carry its own proxy: type (HTTP, HTTPS or SOCKS5), address, and
  optional username and password, the password encrypted with the Keyring. Through a proxy, Muster resolves the target
  too and checks every address it gets; where only the proxy can resolve names, the target name is checked against the
  lists instead (a denied name is blocked; in strict mode only an allowed name passes) and the request is logged as sent
  without an address check. The proxy's own address is always checked by the same policy. Connection checks take the
  same path as real traffic. The settings object and form arrive with the first client that has a UI (C-03.FR-19).
- **C-02.FR-23** The single Organization exists from the first start with the defaults of [defaults.md](defaults.md):
  name, time zone, Severity levels with their styles, "critical is Urgent", Instance labels, retention periods, the outbound address policy and
  its lists, and the TOTP policy. Later capabilities read these settings; C-03, C-19 and C-20 add editing.
- **C-02.FR-24** The Organization-wide notices "recovering after downtime" (FR-12) and "no replica is leading" (the
  Leader's "alive" mark is older than `leader.absence_notice`) are kept as state that C-03 exposes to the UI.

## UI

None. The banners for the notices of FR-24 are part of the application shell (C-03).

## API surface

No resources. Health endpoints and `/metrics` on the internal listener; the CLI subcommands `muster migrate` and
`muster doctor`.

## Acceptance

- **C-02.AC-1** With a Keyring that cannot decrypt the canary, the replica exits non-zero and logs the canary error;
  with the right Keyring it becomes ready.
- **C-02.AC-2** Starting against PostgreSQL 13, or with the session connection going through a transaction pooler,
  fails with an error naming the cause.
- **C-02.AC-3** With two replicas, killing the Leader's network makes the other replica export `muster_leader 1` within
  60 seconds, and the old Leader stops its singleton tasks within `leader.fencing_timeout`.
- **C-02.AC-4** After stopping Muster for 10 minutes and starting it again, the Leader logs the registered downtime
  event with a period of about 10 minutes, and the recovery notice is active for no longer than
  `recovery.banner_duration` allows.
- **C-02.AC-5** `muster migrate` on a newer schema than the binary knows refuses, naming both versions; `up → down → up`
  of every migration passes in CI.
- **C-02.AC-6** The compose example refuses to start while the master key placeholder is in place, and becomes ready
  once it is replaced.
- **C-02.AC-7** With only the `MUSTER_DATABASE_*` fields set (password from a file), Muster connects; with both forms
  set, it uses the URL and logs a warning; with `MUSTER_DATABASE_PORT=abc`, it stops with an error naming that
  variable.
- **C-02.AC-8** With `MUSTER_LISTEN_INGEST` and `MUSTER_LISTEN_APP` set to the same address, the ingestion route and the
  API answer on that one port.
- **C-02.AC-9** Through the outbound package, a request to `169.254.169.254` and a request answered with `302` both fail
  without retry, with errors that name the blocking rule and the redirect target; a request to a loopback address is
  refused under the default policy.
- **C-02.AC-10** A request whose URL carries a known secret and fails with a network error leaves that secret in no log
  line and no returned error (checked by the secret lint of ADR-0016).
- **C-02.AC-11** A request through a fake SOCKS5 proxy reaches its target only through the proxy; a proxy whose address
  is `169.254.169.254` is refused, naming the rule.
- **C-02.AC-12** Using an unregistered log event fails the build, and CI fails when a generated reference page for log
  events or metrics is stale.
- **C-02.AC-13** Against a PostgreSQL without TLS and with `MUSTER_DATABASE_SSLMODE=prefer`, `muster doctor` reports
  both connections as unencrypted with a warning and exits zero when every other check passes, and startup logs the
  unencrypted connections at WARN.

## Related ADRs

ADR-0006, ADR-0007, ADR-0010, ADR-0011, ADR-0014, ADR-0015, ADR-0016.

## Depends on

C-01 — repository, CI and image.

## Suggested story split

BE only, in three stories: the runtime core (environment, database, migrations, Keyring, Leader, health, downtime,
`doctor`); the log event and metric registries; the outbound HTTP package with the address policy and proxies.

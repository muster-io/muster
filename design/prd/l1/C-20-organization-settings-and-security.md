# C-20. Organization settings and security

[L1 index](../L1.md) · Stage: Operations · UI: yes · Depends on: C-03, C-11, C-13

**Goal.** Admins change Organization-wide behaviour — time zone, the start of the working day and of the week, Severity
levels, urgency, Instance labels, retention and the outbound address policy — whose defaults exist since C-02, and
rotate the master keys safely.

Names in the form `area.setting` refer to rows of [defaults.md](defaults.md).

## Scenarios

1. The Admin sets the Organization's time zone to Europe/Berlin; message times follow.
2. The Admin maps `P1` and `page` to critical for the label `severity`, after seeing among the values seen that `P1`
   is currently counted as warning.
3. The Admin switches the outbound address policy to strict and allows the network of the internal chat server.
4. The Admin rotates the master key: adds the new key to every replica's environment, activates it, waits for "the old
   key is no longer needed", and removes it.
5. The Admin of a team whose week starts on Sunday and whose working day starts at 08:30 sets both; messages then offer
   "Until tomorrow 08:30" and "Until Sunday 08:30" (C-10.FR-17).

## Functional requirements

- **C-20.FR-1** Admins edit the Organization settings: name; time zone for messages (`organization.time_zone`); Severity
  levels — the label carrying severity (`organization.severity_label`), the mapping of its values to the levels
  critical, warning and info (`organization.severity_mapping`), and the emoji and colour of each level; "critical is
  Urgent" (`organization.critical_is_urgent`); Instance labels (`organization.instance_labels`); the retention periods
  (`retention.*`). `retention.alert_group_summaries` must be at least `retention.alert_details`, because details are
  always removed before the summary rows they belong to; an update that breaks this is refused with `422` and the code
  `retention_order`. Notes are kept with the summary rows, not with the details (C-09.FR-16). The Severity levels page
  lists the values of the severity label seen in the last `organization.severity_values_window` with their counts, and
  marks those without a mapping, which count as warning. On the Security page Admins also edit the grace period of
  Personal access tokens of OIDC users without an offline token (`auth.oidc_token_grace`, C-04.FR-8). The TOTP policy
  (C-03) and the outgoing heartbeat (C-19) are edited in their capabilities.
- **C-20.FR-2** Changing Severity levels or Instance labels affects later evaluations only and never changes the status
  of an open Alert Group.
- **C-20.FR-3** Admins edit the outbound address policy (standard or strict) and its lists of allowed and denied
  networks, host names and domains; the rules they control are C-02.FR-21. The editor shows which rules always apply and
  that a denied entry always wins.
- **C-20.FR-6** The Keyring page shows every key id in the environment, which one is active, which live replicas hold
  which keys, and for each older key whether any secret or open Root message still depends on it. When none does, it
  says the key can be removed from `MUSTER_SECRET_KEYS`. Only a key that was active before is ever called removable; a
  key that was never active, such as one added for a rotation, is shown as not active yet.
- **C-20.FR-7** `muster secrets rotate-key --activate <id> --actor <name>` activates a key only if every live replica
  holds it, and otherwise refuses, naming the replicas that lack it. On activation it re-encrypts all secrets and the
  key canary; replicas switch without a restart; after activation the Leader recomputes the Desired state of every open
  Alert Group whose buttons another key signed, so their buttons are re-signed. The activation is recorded in the Audit
  log.
- **C-20.FR-8** Every settings change is audited with a before/after diff and uses optimistic locking.
- **C-20.FR-9** Admins set the start of the working day (`organization.workday_start`, a time of day `HH:MM` on the
  24-hour clock) and the first day of the week (`organization.week_start`, `monday` to `sunday`) on the General page,
  next to the time zone; the Snooze presets end at them (C-10.FR-17). The `organization` read carries both for every
  signed-in identity, so that the UI can label the presets; an update that omits one keeps it. A change applies to
  Snoozes set afterwards and never moves one already set; Root messages show the new labels from their next re-render
  (C-10.FR-18).

## UI

One "Organization" entry in the main navigation opens a sub-navigation of the Organization pages: General (name, time
zone, the start of the working day and the first day of the week), Severity levels (mapping, emoji and colours, values
seen), Alert handling ("critical is Urgent", Instance labels), Security (the TOTP policy of C-03; the token grace period
for OIDC users without an offline token), Outbound addresses (outbound address policy, allowed and denied entries),
Retention, Keyring (Keyring status), and the pages of other capabilities kept with them: Outgoing heartbeat and System
status (C-19), Lookup tables and Link rules (C-12), and the Audit log (C-03).

## API surface

`organization` (read, update); `organization/outbound-policy` (read, update); `organization/keyring` (read status);
`organization/severity-values` (list values seen) — the three sub-resources are read with the Permission
`organization:read`, which only Admins hold; the CLI subcommand `muster secrets rotate-key`.

## Acceptance

- **C-20.AC-1** Under the standard policy a webhook to `127.0.0.1` is refused until `127.0.0.0/8` is allowed;
  `169.254.169.254` stays refused even when `169.254.0.0/16` is allowed.
- **C-20.AC-2** `rotate-key --activate` refuses while a live replica lacks the key, and succeeds after it is added; the
  Keyring page then reports when the old key is no longer needed.
- **C-20.AC-3** Mapping `P1` to critical makes a new Alert with `severity="P1"` Urgent; an open Alert Group's status
  does not change.
- **C-20.AC-4** After Alerts with `severity="P5"` arrive, the Severity levels page lists `P5` among the values seen,
  marked as unmapped and counted as warning.
- **C-20.AC-5** After a key activation, the open Root messages in the fake Mattermost server are edited with buttons
  signed by the new key, and presses on them work.
- **C-20.AC-6** An update that sets `retention.alert_group_summaries` below `retention.alert_details` gets `422` with
  `retention_order` and changes nothing; equal periods are accepted.
- **C-20.AC-7** An update with `workday_start` `24:00` or `9:00`, or with `week_start` `weekend`, is refused with
  `invalid_format` at the field and changes nothing; `08:30` with `sunday` is accepted, recorded in the Audit log with
  its before/after diff, and the next `snooze-presets` read ends `week_start` at 08:30 on the next Sunday.

## Related ADRs

ADR-0003, ADR-0006, ADR-0011, ADR-0015.

## Depends on

C-03 — Organization resource and Audit log; C-11 — re-rendering of open Root messages; C-13 — a messenger to check
re-signed buttons.

## Suggested story split

- **BE** — Organization settings update, values seen, outbound policy editing.
- **BE** — Keyring status, `rotate-key` and the re-render after a rotation.
- **FE** — Organization settings pages.
- Added with the Snooze presets: the start of the working day and of the week, in the API (**BE**) and on the General
  page (**FE**), with the stories of C-10.FR-17.

# 0011. Application-level encryption of secrets with a keyring

- Status: Accepted
- Date: 2026-10-02
- Amended: 2026-10-03 — token prefixes name the token kind; an outgoing webhook Signing secret is generated when the
  Destination is created
- Amended: 2026-10-04 — a third sub-key purpose: the CSRF token of a web session is derived from the session token
  with its own sub-key and never stored

## Context

Muster's database holds secrets with real power: Mattermost and Telegram bot tokens, the OIDC client secret,
credentials for outgoing webhooks and proxies, TOTP seeds, and later LLM API keys. Database dumps and backups travel
further than the running service — to backup storage, to a laptop for debugging — and disk or volume encryption does not
protect them.

Muster also needs keys for signatures. Mattermost delivers button presses as unsigned HTTP requests, so each button must
carry something only Muster can produce and check; Telegram limits button data to 64 bytes, so that value must be
compact. Outgoing webhooks are signed so that the receiving side can verify them; HMAC is symmetric, so whoever can
verify a signature can also forge one.

All keys must be rotatable without breaking the buttons on messages already posted, and rotation must work with several
replicas being restarted one after another: a replica must never encrypt with a key that another live replica cannot
read. Muster never generates master keys and never writes Secrets (ADR-0010), so new keys always arrive through the
environment.

## Decision

**AES-256-GCM at the application level.** Muster encrypts every secret field before it reaches the database. Each
ciphertext is stored together with the id of the key that produced it.

**Keyring.** The master keys come from the bootstrap environment as a list: `MUSTER_SECRET_KEYS` holds one or more
base64-encoded 32-byte keys separated by commas, and `MUSTER_SECRET_KEYS_FILE` names a file with one key per line. Each
key's id is derived from the key material itself, so the same key has the same id on every replica and no id has to be
configured. All keys in the list form the Keyring; **which key is active is stored in the database**, not in the
environment. On a new database the first key in the list becomes active. The active key encrypts and signs; every key
in the Keyring can still decrypt and verify. From each master key Muster derives a separate sub-key per purpose —
**encryption**, **button signatures** and **CSRF tokens** of web sessions — so material used for one purpose is never
used for another.

**Signed buttons.** A button carries only an opaque action id with an HMAC made with the button-signature sub-key, plus
the key id. A press is accepted if the signature verifies with the key it names, provided that key is in the Keyring.

**Rotation.**

1. The operator generates a new key outside Muster, adds it to `MUSTER_SECRET_KEYS` next to the current one and rolls
   the change out to every replica. Each running replica records in the database the ids of the keys it holds and
   refreshes that record every 30 seconds; a replica whose record has not been refreshed for 2 minutes no longer counts
   as live.
2. `muster secrets rotate-key --activate <id>` makes that key active — but only when every live replica holds it;
   otherwise it refuses and names the replicas that lack it. It then re-encrypts all secrets and the key canary with the
   new key and recomputes the Desired state of the messages of every open Alert Group, so reconciliation (ADR-0005)
   re-renders them with buttons signed by the new key. Replicas switch to the new active key without a restart.
3. Muster shows, in the UI and in `muster doctor`, when no secret and no Root message of an open Alert Group depends on
   an older key any more. Only then does the operator remove that key from the environment.

Resolved Alert Groups have no buttons, so old messages keep no signatures that matter. Buttons in Thread replies, such
as the ones on Reminders, are not reconciled and keep the signature they were sent with; they do not hold back the
removal of the old key and stop working once it is gone. A press on such a button is answered privately that the button
has expired, with a pointer to the Root message.

**Key canary.** On first start Muster encrypts a known value with the active key and stores it, under the migration
lock, so that replicas starting together on a new database agree on one canary; every later start decrypts it.
Activating a key re-encrypts the canary with that key, so removing the old key later never breaks startup. If
decryption fails — the active key is missing from the replica's Keyring, or the keys belong to another database — the
replica stops with a clear error instead of running with secrets it cannot read. A running replica that sees an active
key it does not hold, which can happen only after its record expired, stops with the same error.

**Outgoing webhook signatures.** Each outgoing webhook Destination has **its own random Signing secret**. An admin
generates it when setting up the Destination and can regenerate it later; it is stored encrypted like any other secret
and shown once, when generated. Requests carry headers in the style of the Standard Webhooks specification: a message
id, a timestamp and a signature header that may hold several signatures, each an HMAC-SHA256 over the id, the
timestamp and the body. When a secret is regenerated, every request is signed with both the new and the previous secret
until the admin retires the previous one, so the receiving side can switch without a gap; meanwhile the UI shows that
the previous secret still signs, and since when. Master key rotation does not change these secrets.

**API and logs.** Secret fields are write-only in the API; reads return whether a field is set and when it last changed.
A CI test pushes known secret values through the code and fails if any of them reaches a log line (ADR-0016).

**Tokens Muster issues are hashed, not encrypted.** Personal access tokens and Service account tokens (prefix `mstr_`
and the kind: `mstr_pat_`, `mstr_sat_`) and Integration tokens (`mstr_int_`) are stored only as hashes and shown once, when created.

**Key provider interface.** Key material is obtained through an interface. The first implementation reads the
environment; external providers such as Vault transit or a cloud KMS can be added later without changing the callers.

**Backups.** The documentation tells users to back up the database and the master keys separately, describes the
restore order, and recommends running `muster doctor`, which includes the canary check, after a restore.

## Consequences

- A stolen dump or backup does not reveal bot tokens or OIDC secrets without the master keys.
- Losing the master keys makes stored secrets unrecoverable; they must be entered again. The canary turns a wrong or
  missing key into an immediate, explicit failure.
- Rotation takes two deliberate steps — a rollout that adds the key everywhere, then an activation — and a rolling
  restart never leaves a replica unable to read what another one wrote.
- Two keys live in the environment during a rotation; Muster says when the old one can go.
- One leaked Signing secret affects one Destination only; regenerating it touches nobody else.
- Buttons on Reminders and other Thread replies posted before a rotation stop working when the old key is removed; the
  buttons on Root messages of open Alert Groups keep working, because they are re-rendered.
- Secrets are decrypted in memory when used, so the process memory is as sensitive as the keys.
- Moving to an external KMS later means a new key-provider implementation, not changes in every caller.

## Alternatives considered

- **Rely on database or disk encryption.** Does not protect dumps and backups, and does not provide signing keys.
- **One key for every purpose.** Rotating or exposing it for one purpose would affect all others; derived sub-keys keep
  them independent.
- **Rotation by replacing the key outright.** Old ciphertexts become unreadable and every signed button on open Alert
  Groups breaks at once.
- **A rotation command that generates and adds the new key itself**, or **activating the newest key in the environment
  at startup.** Muster cannot write the key to the environment of other replicas; during a rolling restart a replica
  with the new key would encrypt data that replicas without it cannot read.
- **One Organization-wide webhook signing key derived from the master key.** Every receiving endpoint could forge
  requests to every other one, a single receiving endpoint could not be cut off, and rotating the master key would
  change the secret for all of them at once.
- **Asymmetric webhook signatures.** They would remove the forgery problem, but receiving endpoints widely expect HMAC;
  a secret per Destination gives the needed isolation with the common format.
- **Encrypting tokens Muster issues instead of hashing them.** Muster only needs to verify these tokens, never to read
  them back; a hash is enough and safer.
- **Generating the master key automatically.** See ADR-0010.
- **An external KMS in the first release.** Adds a dependency to every installation; it stays behind the key provider
  interface.

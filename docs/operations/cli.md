# The command line

The `muster` binary is the server and its command line. Without a command it runs the server. The commands are for
emergencies, for setup and for diagnosis; everything else is in the web UI and the API. `muster help` lists them; its
output, abridged:

```text
Usage: muster [command]

Commands:
  migrate   Apply the database migrations under the migration lock
  doctor    Check the database, its connections, the master keys and the clock; only reads
  admin     Emergency access with --actor <name> <login>: admin reset-password sets a password read from standard
            input, admin reset-totp removes a lost second factor
  ingest    ingest replay --since <duration> [--integration <name>] --actor <name> reprocesses Stored Snapshots
  secrets   secrets rotate-key --activate <id> --actor <name> activates a master key that every live replica holds
  bridge    bridge check <url> --token-file <file> --channel <id> runs the Bridge protocol against a bridge, without a
            database; muster bridge help lists its flags
  dev       Development mode: fake servers and development defaults
  version   Print the version and commit
  help      Show this help
```

Every command exits 0 on success, 1 on a failure and 2 on a usage error, such as a missing flag.

## Environment

The commands that use the database read the same `MUSTER_*` variables as the server. Run them in the environment of the
server, not in a bare shell:

```sh
kubectl -n muster exec deploy/muster -- muster doctor
docker compose exec muster muster doctor
```

The image has no shell, so run the binary directly. A command that reads a password from standard input needs
`kubectl exec -i` or `docker compose exec -T`.

| Command | Needs |
|---|---|
| `doctor`, `migrate`, `secrets rotate-key` | the database settings, `MUSTER_PUBLIC_URL` and the master keys (`MUSTER_SECRET_KEYS` or `MUSTER_SECRET_KEYS_FILE`) |
| `admin`, `ingest replay` | the database settings and `MUSTER_PUBLIC_URL`; they do not open the Keyring |
| `bridge check`, `version` | nothing |

The [environment reference](../reference/environment.md) lists the variables, and [the database](../install/database.md)
the ways to set the connection.

## `--actor`

The commands that **change data** require `--actor <name>`: `admin reset-password`, `admin reset-totp`, `ingest replay`
and `secrets rotate-key`. The name says who runs the command. Muster writes it, with the Transport `cli`, to the Audit
log, so that a change made from a terminal has a person next to it like a change made in the web UI. Without `--actor` the
command exits 2 before it connects, and nothing changes. `migrate` and `doctor` take no `--actor`.

## `muster doctor`

```text
muster doctor
```

Checks and **only reads**. Each check prints one line, `OK`, `WARN` or `FAIL`, then its name and detail. The command
exits 1 when a check fails. A warning does not fail it.

| Check | Looks at |
|---|---|
| `database` | the main connection works |
| `postgresql_version` | PostgreSQL 14 or newer |
| `session_connection` | the session connection supports advisory locks and `LISTEN`, and the mode of each pooler |
| `tls_main`, `tls_session` | whether each connection is encrypted; `WARN`, not `FAIL`, when it is not |
| `key_canary` | the master keys can decrypt the canary, so they belong to this database |
| `keyring` | the active key is held, and which other keys are still needed, removable or not active yet |
| `clock_skew` | the replica's clock against the database clock, `WARN` above 2 seconds |
| `connection <name>`, `destination <name>` | the check of every Connection and every Destination that has one |

Run it after an install, after a [restore](backup-and-restore.md), after an [upgrade](upgrades.md), and when you suspect
the database, a pooler or the keys.

## `muster migrate`

```text
muster migrate
```

Applies the migrations under a session advisory lock and logs the versions it applied. It refuses to run on a schema
newer than the binary knows and names both versions. The Helm chart runs it in an init container. See
[Upgrades](upgrades.md#migrations).

## `muster admin`

```text
muster admin reset-password --actor <name> <login>
muster admin reset-totp --actor <name> <login>
```

Emergency access when no Admin can sign in. Flags come before the login.

- `reset-password` reads the new password from standard input: without echo after a prompt on a terminal, otherwise the
  first line. It sets it on the User with that login and ends the User's sessions. On a User who signs in through
  OpenID Connect, it also removes the OIDC identity, so the User signs in with the password from then on.
- `reset-totp` removes the TOTP and the recovery codes of the User, for a lost second factor, and ends their sessions.
  Under a TOTP policy that covers the User, they enrol again at the next sign-in. See [TOTP](../sign-in/totp.md).

```sh
printf '%s\n' "$NEW_PASSWORD" | kubectl -n muster exec -i deploy/muster -- \
  muster admin reset-password --actor alice admin@example.org
```

## `muster ingest replay`

```text
muster ingest replay --since <duration> [--integration <name>] --actor <name>
```

Reprocesses Stored Snapshots, for example after a bug in processing was fixed.

- `--since` is the period back from now, such as `1h` or `90m`. A period longer than the retention of Stored Snapshots
  is cut to it.
- `--integration` limits the replay to the Integration with that name.

The command sets the Stored Snapshots of the period back to pending in one transaction and records `ingest.replayed` in
the Audit log; the running replicas process them again in arrival order. Processing is idempotent per Alert, so Snapshots
that were processed change nothing, while one that failed is processed with the current code. The command prints how many
Stored Snapshots it replayed.

## `muster secrets rotate-key`

```text
muster secrets rotate-key --activate <id> --actor <name>
```

Activates a master key, re-encrypts the Secrets and the key canary, and makes the Leader re-sign the buttons of open Root
messages. It refuses while a live replica does not hold the key. `--activate` is the key id that **Organization →
Keyring** shows. The two steps around it, and what to do afterwards, are in [Key rotation](key-rotation.md).

## `muster bridge check`

```text
muster bridge check <url> --token-file <file> --channel <id> [flags]
```

Runs the Bridge protocol against a bridge, step by step, and prints one line per step: `PASS`, `FAIL` with the reason, or
`SKIP`. It needs no database, master key or configuration, writes nothing, and never changes the bridge's configuration.
It exits 0 when no step failed, 1 when one did, and 2 on a usage error.

Flags: `--token-file` and `--channel` are required; `--forbidden-channel`, `--handle`, `--push-listen`, `--push-url`,
`--push-secret-file` and `--no-interactive` are optional. Their meaning, the steps and a worked example are in [Write a
bridge](../messengers/write-a-bridge.md#muster-bridge-check).

## `muster version`

```text
muster version
```

Prints the version and the commit, in the form `muster <version> (commit <sha>)`. The metric `muster_build_info` carries the
same two labels.

## `muster dev`

`muster dev` runs Muster with fake messenger servers and a published development master key. It is for working on Muster; do
not use it for an installation.

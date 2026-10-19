# Backup and restore

Muster keeps all its state in PostgreSQL, and encrypts its secrets with the [master keys](key-rotation.md) from the
environment. Neither the Helm chart nor the compose example backs up anything, so you back up two things, **separately**:

The database
:   Holds Integrations, Routes, Alert Groups, people, the Audit log and every Secret, encrypted.

    Without it, everything is gone.

The master keys (`MUSTER_SECRET_KEYS`)
:   Hold the keys that decrypt the Secrets and sign the buttons in messages.

    Without them, Muster refuses to start on the database, and its encrypted Secrets cannot be read.

## Why they are kept apart

A database backup alone reveals no bot token, OIDC client secret or proxy password, because Muster encrypts them before
they reach the database. A backup together with the master keys reveals all of them. Store the two in different places
and give them different access: the dump in your backup storage, the keys where you keep your other secrets, such as a
password manager or the secret store of your cluster.

Without the master key that wrote the database, Muster refuses to start on it, and nobody can read the Secrets in it.
Muster cannot recover them, and it has no way to start that database again with a new key: keep the key safe.

After a [key rotation](key-rotation.md), back up the new key list too. A backup taken before the rotation needs the old
key to restore.

## Back up the database

Muster has no backup format of its own. Use the tools of PostgreSQL:

- **A dump.** `pg_dump` writes a consistent copy while Muster runs:

    ```sh
    pg_dump --format=custom --file=muster-$(date +%F).dump "$MUSTER_DATABASE_URL"
    ```

    With the compose example, run it in the database container:

    ```sh
    docker compose exec -T postgres pg_dump -U muster --format=custom muster > muster-$(date +%F).dump
    ```

- **Continuous backup.** With CloudNativePG, add `spec.backup` to the `Cluster`; see [the
  database](../install/database.md#cloudnativepg). A managed PostgreSQL offers its own snapshots and point-in-time
  recovery. Either is better than a nightly dump when you cannot lose an hour of Alert Groups.

Take a backup before every [upgrade](upgrades.md), and test a restore before you rely on it.

## Restore

1. Stop every Muster replica. In Kubernetes, scale the Deployment to 0; with compose, stop the `muster` service.
2. Restore the database into an empty database. For a dump:

    ```sh
    pg_restore --clean --if-exists --no-owner --dbname="$MUSTER_DATABASE_URL" muster-2026-10-19.dump
    ```

    A CloudNativePG or managed restore creates a new cluster from the backup; point `database.host` at it.
3. Put the **same master keys** back into `MUSTER_SECRET_KEYS`. The key that was active when the backup was taken must be
   in the list. Keys are identified by their material, so any copy of the same key works.
4. Start Muster, in the version that wrote the backup or in a newer one that the [upgrade rules](upgrades.md#the-rules)
   allow. It applies the migrations that are
   missing (see [Upgrades](upgrades.md#migrations)) and checks the key canary. A wrong or missing key stops the start with
   "master key does not match the database".
5. Run `muster doctor` with the same environment as the server, and read every line. See [the
   CLI](cli.md#muster-doctor).

After the restore, Muster treats the time since the last "alive" mark as downtime. For a while, the web UI says that
Muster is recovering and its data may be incomplete, and overdue timers fire once, collapsed. That is expected.

## What to check

- `muster doctor` exits 0. The `key_canary` line is `OK`, which proves that the keys match the database. The `keyring`
  line is `OK` when no older key is in use; it is `WARN` while a Secret or an open Root message still needs an older key,
  and it says when a key is no longer used and can be [removed](key-rotation.md#remove-the-old-key).
- **Organization → System status** shows one Leader and every replica, and no Destination you did not expect as Broken.
- Open an Alert Group and press a button in its messenger message. A press works only when the master keys are right.
- Messengers and Alertmanagers reach the restored Muster at `MUSTER_PUBLIC_URL` and `MUSTER_INGEST_URL`. If you
  restored into an installation with other addresses, change them in the environment and check the Connections.

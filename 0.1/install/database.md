# The database

Muster keeps everything in PostgreSQL 14 or newer, and nothing else stateful: no Redis and no queue. At start Muster
refuses an older PostgreSQL and stops with an error that names the cause. It needs one extension, `pg_trgm`, which ships
with PostgreSQL and which the database owner can create (a trusted extension since PostgreSQL 13); the first migration
creates it. Neither the Helm chart nor the compose example backs up the database: see
[Backup and restore](../operations/backup-and-restore.md), and keep the master key apart from the backups.

## As a URL or as fields

Give the database to Muster in one of two forms. The settings are in the
[environment reference](../reference/environment.md).

A URL
:   `MUSTER_DATABASE_URL`, such as `postgres://muster:<password>@db.example.org:5432/muster?sslmode=require`

Fields
:   `MUSTER_DATABASE_HOST`, `MUSTER_DATABASE_PORT` (default `5432`), `MUSTER_DATABASE_NAME`, `MUSTER_DATABASE_USER`,
    `MUSTER_DATABASE_PASSWORD` or `MUSTER_DATABASE_PASSWORD_FILE`, `MUSTER_DATABASE_SSLMODE` (default `prefer`)

When both forms are set, the URL wins and Muster logs a warning that names both. Muster ignores every `PG*` variable,
such as `PGHOST`, `PGSSLMODE`, `PGSSLROOTCERT`, `PGOPTIONS` and `PGPASSFILE`, and the password file `~/.pgpass`. Give
any other libpq setting as a parameter of the URL instead, such as `sslrootcert`, `options` or `target_session_attrs`.
Percent-encode the values as libpq requires, `=` as `%3D` and a space as `%20`, for example
`options=-c%20search_path%3Dmuster`. A URL that libpq rejects stops Muster at start with an error that names the
parameter; the URL in the error includes the `sslmode` that Muster appends when the URL names none. Only libpq's
default certificate files still apply when the URL names none: `~/.postgresql/root.crt`, and
`~/.postgresql/postgresql.crt` with `postgresql.key`; these are files, not variables, and the Muster image has none. Any
secret can come from a `*_FILE` variable.

Prefer the fields when the host, user and name are plain values and only the password is a secret: the Helm chart does
exactly that, with `database.host`, `database.port`, `database.name`, `database.user` and `database.sslmode` as values
and the password from a key of the Secret as `MUSTER_DATABASE_PASSWORD_FILE`. Use the URL when you need a setting that
has no field, such as `pool_max_conns` or `sslrootcert`; in the chart, `database.urlKey` names the key of the Secret that
holds the whole URL, and the field values are then ignored.

A replica's main pool holds `max(10, CPUs)` connections unless the URL sets `pool_max_conns`. The `pool_*` parameters
size the main pool only: a session connection derived from the URL drops them, and one given by
`MUSTER_DATABASE_SESSION_URL` ignores them. On top of that, each replica keeps about three session connections (see
[PgBouncer](#pgbouncer)). Size `max_connections` of PostgreSQL for all replicas together.

Create the database and its user before the first start. The user owns the database, because Muster creates its tables,
indexes and partitions itself:

```sql
CREATE ROLE muster LOGIN PASSWORD '<password>';
CREATE DATABASE muster OWNER muster;
```

Muster applies its migrations at the first start: the compose example sets `MUSTER_MIGRATE_ON_START`, and the chart runs
`muster migrate` in an init container.

## CloudNativePG

[CloudNativePG](https://cloudnative-pg.io) runs PostgreSQL clusters on Kubernetes. A `Cluster` for Muster creates the
database and its owner:

```yaml
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: muster-db
  namespace: muster
spec:
  instances: 3
  storage:
    size: 10Gi
  bootstrap:
    initdb:
      database: muster
      owner: muster
```

CloudNativePG serves the primary at the Service `muster-db-rw` (and the replicas read-only at `muster-db-ro`) and enables
TLS on its own. It generates the password and keeps it in the Secret `muster-db-app`, with the keys `username`,
`password`, `host`, `port`, `dbname` and `uri` among others. The chart reads the database from that Secret when you name
it in `database.existingSecret`; the Secret `muster` of [Install with Helm](helm.md#create-the-secret) then holds only
the master keys and the password of the first Admin:

```yaml
existingSecret: muster
database:
  existingSecret: muster-db-app
  urlKey: uri
```

The `uri` key holds a URL such as `postgresql://muster:<password>@muster-db-rw.muster:5432/muster`. It names no
`sslmode`, so Muster connects with its default, `prefer`, which uses the TLS of CloudNativePG but does not insist on
it. To require TLS, give the connection as fields and take only the password from the Secret, which also keeps the
password out of the pod's environment:

```yaml
existingSecret: muster
database:
  existingSecret: muster-db-app
  host: muster-db-rw.muster.svc
  name: muster
  user: muster
  sslmode: require
  passwordKey: password
```

When you give the `Cluster` a Secret of your own under `bootstrap.initdb.secret`, CloudNativePG generates no
`muster-db-app`, and your Secret holds no `uri`: use the fields form above, with your Secret in `database.existingSecret`
and `passwordKey: password`.

Add an `spec.backup` section to the `Cluster` (see the CloudNativePG documentation) and test a restore, together with the
master key, before you rely on it.

## Managed PostgreSQL

A managed PostgreSQL service works when it offers version 14 or newer and a role that owns an empty database. Check:

- **Version.** PostgreSQL 14 or newer. Muster is tested on 14 and 17.
- **Extensions.** Only `pg_trgm`; allow it where the service keeps an allow-list of extensions. Muster does not use
  `pg_partman`; it creates its partitions itself.
- **Session features.** Muster needs session advisory locks and `LISTEN/NOTIFY`. Plain endpoints of managed services
  provide them. A pooler endpoint that your provider adds in transaction mode does not: see [PgBouncer](#pgbouncer).
- **Failover.** Name the endpoint that follows the primary, not an instance.
- **TLS.** Most services require it. Set `sslmode` as [below](#tls).
- **Connections.** Check the connection limit of the plan against the pool sizes above.

Run `muster doctor` against the database after you set it up. It checks the version, the session features, the pooler
mode and whether each connection is encrypted.

## PgBouncer

Muster uses two kinds of database connections:

- The **main connection** carries every query. It may go through PgBouncer in transaction mode.
- The **session connection** carries what needs a real session: the advisory lock of the Leader, the `LISTEN`
  connection that wakes the workers and feeds live updates to the web UI, partition maintenance and the migrations. A
  transaction pooler would lose their session state between statements.

Without a separate setting, the session connection is the main connection. At start, Muster takes an advisory lock over
it and completes a `LISTEN/NOTIFY` round trip. Behind a transaction pooler this fails within five seconds, and Muster
stops with an error that names `MUSTER_DATABASE_SESSION_URL`.

So when the main connection goes through PgBouncer in transaction mode, give Muster a session connection that reaches
PostgreSQL directly or through a pooler in session mode:

`MUSTER_DATABASE_SESSION_URL`
:   A whole URL for the session connection.

`MUSTER_DATABASE_SESSION_HOST`
`MUSTER_DATABASE_SESSION_PORT`
:   Only the host and the port; the user, password, database, `sslmode` and the other parameters are those of the main
    connection, without its `pool_*` parameters.

In the chart, these are `database.sessionHost` and `database.sessionPort`. With CloudNativePG, the main connection can
use a `Pooler` object in transaction mode, whose Service takes the `Pooler`'s name, here `muster-db-pooler-rw`, and the
session connection the `-rw` Service of the `Cluster`:

```yaml
database:
  host: muster-db-pooler-rw.muster.svc
  sessionHost: muster-db-rw.muster.svc
  sessionPort: 5432
```

!!! note "PgBouncer 1.21 or newer"
    Muster's driver uses prepared statements on the main connection. In transaction mode, PgBouncer supports them from
    version **1.21**, and only when `max_prepared_statements` is above 0. An older PgBouncer, or a value of 0, makes
    queries fail with errors about missing prepared statements. Set `max_prepared_statements` to a few hundred, such as
    `200`. A pooler in session mode, or no pooler, needs no such setting.

PgBouncer is optional. Most installations connect straight to PostgreSQL, and then no session setting is needed.

## TLS

`MUSTER_DATABASE_SSLMODE` (or `sslmode` in the URL) takes the values of PostgreSQL's client library:

`disable`
:   encrypts: never · checks the server: nothing

`allow`
:   encrypts: only when the server insists · checks the server: nothing

`prefer` (default)
:   encrypts: when the server offers TLS, else falls back to plain text · checks the server: nothing

`require`
:   encrypts: always · checks the server: nothing: protects against listening in, not against an impostor

`verify-ca`
:   encrypts: always · checks the server: the certificate chain against a trusted CA

`verify-full`
:   encrypts: always · checks the server: the chain, and that the certificate names the host you connect to

`prefer` makes a fresh installation work, but it also connects without encryption to a server that has none. Muster tells
you: `muster doctor` reports each connection as encrypted or not, and a start with an unencrypted connection logs a
warning. For anything but a single machine, set `require` at least, and `verify-full` where you can: the certificate then
proves that the server is yours.

`verify-ca` and `verify-full` check against the file named by `sslrootcert` in `MUSTER_DATABASE_URL`, which must exist
in the container; without it, against `~/.postgresql/root.crt` when that file exists, and otherwise against the root
certificates of the system. The fields form has no such setting. With
`MUSTER_DATABASE_SESSION_HOST` and `MUSTER_DATABASE_SESSION_PORT`, the session connection takes the `sslmode` of the
main connection; a `MUSTER_DATABASE_SESSION_URL` carries its own, `prefer` when it names none.

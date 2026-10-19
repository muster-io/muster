# Upgrades

Muster follows semantic versioning (`major.minor.patch`). A release publishes the container image
`ghcr.io/muster-io/muster`, binaries, and the Helm chart as an OCI artifact in the same registry. The changelog of each
release is on the releases page of the repository, written from the commit messages.

## The rules

- **Patch and minor versions.** Update to the next version, or to any patch version of the same minor version.
- **Rolling updates only between adjacent minor versions.** From 1.4.x to 1.5.x, replica by replica, is supported. From
  1.4.x to 1.7.x is not: go through 1.5 and 1.6, or stop every replica before you start the new version.
- **Back up first.** Take a [database backup](backup-and-restore.md) before every upgrade, because of the next rule.
- **No downgrades.** Migrations only go forward in production. A binary refuses to start on a schema newer than it knows
  and names both versions. To go back, restore the backup you took before the upgrade, together with the master keys of
  that time, and run the old version on it. Everything written since is lost.
- Read the release notes of every version you skip. Settings that change are named there.

## Upgrade with Helm

1. Take a backup.
2. Change the version: `helm upgrade muster oci://ghcr.io/muster-io/charts/muster --version <version> -n muster -f values.yaml`.
   The chart's `image.tag` defaults to its `appVersion`, so the chart and the image move together. If you pin
   `image.tag`, change it too.
3. Watch the rollout: `kubectl -n muster rollout status deploy/muster`.
4. Run `muster doctor` (see [the CLI](cli.md#muster-doctor)) and open **Organization → System status**: it shows the
   version and commit that serve the page, and under *Replicas* which replica is the Leader.

The chart does not migrate in the server. Each new pod runs `muster migrate` in an **init container** before its server
container starts, and `MUSTER_MIGRATE_ON_START` stays off. The pods of a rollout do not migrate at once, because
migrations run under a database lock. Kubernetes starts a new pod before it stops an old one, so the web UI and
ingestion stay up with one replica too; run two replicas with a
[PodDisruptionBudget](high-availability.md#two-replicas) if you also want to survive the loss of a node.

## Upgrade with Docker Compose

The compose example sets `MUSTER_MIGRATE_ON_START` to `true`: the server migrates when it starts. Take a backup, change
the image (set `MUSTER_IMAGE`, or download the compose file of the new release), and run `docker compose up -d`. With a
single replica nothing else is needed.

## Migrations

Migrations are SQL files built into the binary. They run through `muster migrate` or, when
`MUSTER_MIGRATE_ON_START` is `true`, at server start, in both cases under a session advisory lock and with the
applied versions in the log.

Every migration follows **expand and contract**: a release only adds what the previous release can ignore, and removes
what the previous release no longer uses in a later release. That is why a rolling update between adjacent minor
versions works: while the new schema is in place, the replicas of the old version still run on it.

To migrate before you update the servers, run the command of the new version with the environment of the server:

```sh
muster migrate
```

It needs the database settings and `MUSTER_SECRET_KEYS` of the server (see [the CLI](cli.md#environment)). It exits non-zero
on a schema newer than the binary knows.

## During a rolling update

Replicas of two adjacent minor versions serve at the same time, and either may be the Leader. Do not leave an
installation in this state: finish the rollout.

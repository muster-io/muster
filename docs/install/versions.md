# Requirements and tested versions

What Muster needs, which versions it was checked against, how its own versions are numbered, and how to verify that a
release you download is the one the project built. "Tested" means different things for different rows, so each row says
which.

- **CI** means an automated job that runs on the project's pull requests or nightly, and fails the build.
- **Development stand** means a person ran the behaviour against a real server of that version and recorded what it did,
  but no job repeats it. These facts are the ground the messenger and Alertmanager features rest on; the fake servers
  that CI uses imitate them.

## Requirements

**PostgreSQL**
:   14 or newer, directly or or through a pooler whose session connection is session-capable. Nothing else
    stateful: no Redis, no queue. See [The database](database.md).

**Where Muster runs**
:   Binaries and container images for Linux; binaries also for macOS; both on amd64 and arm64. The container image is
    `linux/amd64` and `linux/arm64`. The [Quick start](../quick-start.md) needs Docker with the Compose plugin.

**Kubernetes**
:   1.30 or newer, for the [Helm chart](helm.md).

**A browser**
:   For the web UI, one that keeps a `Secure` cookie: over HTTPS, or on `localhost`. See
    [Sign-in needs HTTPS or localhost](../sign-in/index.md#sign-in-needs-https-or-localhost).

## Tested versions

PostgreSQL
:   14 and 17 in **CI**: `make test-integration` runs the integration tests on both, in containers, on every pull
    request. The compose example and the development database run PostgreSQL 17.

Kubernetes and Helm
:   Kubernetes 1.33.0 in **CI**: `make helm-check` lints the chart, renders it with the values files of the repository
    and validates the result strictly against the schemas of 1.33.0. Helm v4.3.0 does the rendering, and also packages
    the chart for a release. Nothing is run in a cluster by CI, and other versions from 1.30 are not checked.

Alertmanager
:   v0.34.1 on a **development stand**, with Prometheus v3.15.0 and vmalert v1.153.0: how it repeats, groups, restarts
    and truncates is measured there. Other versions of Alertmanager send the same webhook; if one behaves differently,
    the [Snapshot semantics](../concepts/snapshot-semantics.md) page and the Heartbeat are how you notice.

Grafana
:   OSS 13.2.4 on a **development stand**, as one instance on SQLite and as a pair of replicas on PostgreSQL 17, through
    the webhook contact point of its built-in Alertmanager. See [Connect Grafana](../integrations/grafana.md).

Mattermost
:   11.2.2, a test server on the **development stand**, through REST API v4 with a bot account. CI runs the same
    behaviour against a fake Mattermost server. See [Mattermost](../messengers/mattermost.md).

Slack
:   Slack's cloud Web API and Socket Mode, on a **development stand**: a free workspace with a test app and a test
    channel. CI runs against a fake. See [Slack](../messengers/slack.md).

Telegram
:   The cloud Bot API 10.3, on a **development stand**, with a test bot and a test channel. CI runs against a fake. See
    [Telegram](../messengers/telegram.md).

The web UI
:   Its component tests and its end-to-end tests run in Chromium in **CI**. Other browsers are not tested.

## Version numbers

Muster's versions are `0.N.x`, and 1.0 is a long way off.

- **`0.N` is layer N of the [roadmap](https://github.com/muster-io/muster#roadmap).** L1, Alert Groups in messengers,
  is released as `0.1.x`; L2 will be `0.2.x`, L3 `0.3.x`, and so on. Layer N+1 starts with the release `0.(N+1).0`,
  which the maintainers set with release-please's `release-as`.
- **`0.N.x` releases bring fixes and small changes**, and nothing that breaks an installation: settings, the API under
  `/api/v1` and the database keep working without a change on your side.
- **Breaking changes come only with the next layer's minor version**, and its release notes say what to change before
  you upgrade. See [Upgrades](../operations/upgrades.md) for how to upgrade.

## Upgrade notes

0.2.0
:   The urgent mark of a Route (`route.urgent`) and the Organization's "critical is Urgent"
    (`organization.critical_is_urgent`) no longer act: an Alert Group is Urgent exactly when its Severity level is
    critical, and a Route raises the level of its Alert Groups with its
    [Minimum severity](../concepts/routes-and-group-keys.md#minimum-severity).

    - Routes marked urgent got the Minimum severity critical, which also makes their Alert Groups critical, also for
      Severity conditions.
    - An Organization that had turned "critical is Urgent" off now sees its critical Alert Groups as Urgent: a rise to
      critical ends their Snoozes and, where `route.urgent_rise_removes_ack` is on, their acknowledgements.
    - Migration 0021 computes the Severity level and the urgency of the open Alert Groups and of their deliveries once,
      by the new rule: without lifecycle events or Mentions, without ending a Snooze or an acknowledgement, and without
      publishing anywhere new.
    - `urgent` in the input of a Route and `critical_is_urgent` in the input of the Organization are optional. A Route
      request with `urgent: true` and no `minimum_severity`, as a 0.1 client sends it, sets the Minimum severity
      critical; `urgent: false`, `urgent` beside `minimum_severity`, and `critical_is_urgent` change nothing. Reads keep
      both until 0.3.0 removes them: `urgent` is `true` exactly when the Minimum severity is critical, and
      `critical_is_urgent` is always `true`.
    - Migration 0022 adds [Teams](../concepts/teams.md) and the Organization's Working hours: Monday to Friday, from
      the start of the working day to its end, `18:00`. The end must be later than the start on the same day, so an
      Organization whose working day started at 18:00 or later gets the end `23:59` — and one that started at 23:59,
      the latest time with no later end, the start `23:58`. Check the Organization's settings after the upgrade if
      yours did.

    **The Route profile On-call is now Paging.** `route-profiles` (`listRouteProfiles`) returns the id `paging`, named
    Paging, instead of `on_call`, which breaks clients that type the enum of the profile ids; during a rolling update a
    0.1 replica still answers `on_call`, and a page of the 0.1 web UI names both profiles Informational, so reload the
    web UI after the rollout. Profiles only fill in a new Route and are not stored, so nothing stored changes.

    **Rolling upgrade from 0.1.** Every migration of 0.2.0 applies when the first 0.2.0 replica starts, while 0.1
    replicas still run until the rollout ends. A 0.1 replica ignores the Minimum severity: it stores a lower Severity
    level on the Alert Groups it changes, with `urgent` true, since 0.2.0 writes `routes.urgent`. Until a later change
    of their Alerts or of the Route's Minimum severity computes the level again, the API and the list show such an Alert
    Group as not Urgent, while its messages and outgoing webhooks treat it as Urgent. Do not edit Routes or "critical is
    Urgent" until every replica runs 0.2.0: a 0.1 replica writes the urgent mark and the switch, which 0.2.0 reads
    differently. A 0.1 replica also does not know the end of the working day: its `updateOrganization` that sets the
    start of the working day at or after the end fails the CHECK of migration 0022 with `500` until the rollout ends;
    save the settings again on a 0.2.0 replica.

    Run the upgrade at a quiet time: migration 0021 computes the open Alert Groups again under locks of a few seconds,
    and migration 0022 adds columns to Routes, Destinations and the Organization; each gives up after 5 seconds of
    waiting for a lock (`lock_timeout`) or on a deadlock with the running replicas, and the 0.2.0 replica then refuses
    to start with `schema_dirty`. The changes of 0021 are one transaction, so a failure leaves the schema as 0.1.4 left
    it, recorded as dirty at version 21. To recover, with the same database role as Muster:

    1. Check whether the migration took effect:

        ```sql
        SELECT count(*) FROM information_schema.columns
         WHERE table_schema = current_schema() AND table_name = 'routes' AND column_name = 'minimum_severity';
        ```

    2. If it returns 0, clear the failed attempt: `UPDATE schema_migrations SET version = 20, dirty = false;`. If it
       returns 1, the migration committed and only its record failed: `UPDATE schema_migrations SET dirty = false;`.
    3. Start the 0.2.0 replica again, or run `muster migrate`, at a quieter time; it applies migration 0021 again.

    Migration 0022 runs in two transactions: the first adds the Teams and the Working hours, the second validates the
    CHECKs of the Working hours. A failure leaves the schema recorded as dirty at version 22. To recover, with the same
    database role as Muster:

    1. Check whether the first transaction took effect: `SELECT to_regclass('teams');`
    2. If it returns NULL, clear the failed attempt: `UPDATE schema_migrations SET version = 21, dirty = false;`.
    3. If it returns `teams`, finish the second transaction and clear the mark:

        ```sql
        ALTER TABLE organizations VALIDATE CONSTRAINT organizations_working_days_check;
        ALTER TABLE organizations VALIDATE CONSTRAINT organizations_workday_end_check;
        ALTER TABLE organizations VALIDATE CONSTRAINT organizations_workday_order_check;
        UPDATE schema_migrations SET dirty = false;
        ```

    4. Start the 0.2.0 replica again, or run `muster migrate`.

0.1.1
:   Migration 0020 is expand only: it adds the column `oidc_settings.groups_claim_seen_at`, and replicas of 0.1.0 that
    are already running keep working on it during a rolling update. 0.1.0 refuses to start on schema 20, so going back
    to 0.1.0 needs the database backup taken before the upgrade, as [Upgrades](../operations/upgrades.md) describes.

## Image tags

The images are `ghcr.io/muster-io/muster` and the tags are:

- `vX.Y.Z`: a release. Use it, so that an upgrade is a decision you make.
- `latest`: the newest release that is not a prerelease. A prerelease tag, such as `v1.4.0-rc.1`, never moves it.
- `edge`: the newest commit on `master`, a development image.
- `sha-<7 hex digits>`: the development image of one commit.

## Verify a release

A release is built by the workflow `release.yml` of the repository, on a tag `vX.Y.Z`. It signs the container image and
the Helm chart with [cosign](https://docs.sigstore.dev/cosign/), keyless, with the identity of that workflow run, so a
valid signature proves that the image or the chart was built by that workflow on that tag. The development images,
`edge` and `sha-…`, are **not signed** and carry no SBOM attestation: the workflow that builds them skips signing.
Install cosign v3 or newer (the release signs with v3.1.3, in the new signature format), then verify the image:

```sh
VERSION=vX.Y.Z
cosign verify ghcr.io/muster-io/muster:$VERSION \
  --certificate-identity "https://github.com/muster-io/muster/.github/workflows/release.yml@refs/tags/$VERSION" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

It prints the verified signature and exits `0`. To accept any release tag, replace `--certificate-identity` with
`--certificate-identity-regexp '^https://github\.com/muster-io/muster/\.github/workflows/release\.yml@refs/tags/v'`.
The signature covers the multi-architecture index and the image of each platform. To pin what you run, deploy the digest
you verified, `ghcr.io/muster-io/muster@sha256:…`, rather than the tag.

**The Helm chart** is signed the same way, by the same workflow run. Its tag is the version without the `v`:

```sh
VERSION=vX.Y.Z
cosign verify ghcr.io/muster-io/charts/muster:${VERSION#v} \
  --certificate-identity "https://github.com/muster-io/muster/.github/workflows/release.yml@refs/tags/$VERSION" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

To install exactly the chart you verified, verify it by its digest and install by the same digest. `helm pull` prints
the digest as `Digest: sha256:…`:

```sh
helm pull oci://ghcr.io/muster-io/charts/muster --version ${VERSION#v}
cosign verify ghcr.io/muster-io/charts/muster@sha256:… \
  --certificate-identity "https://github.com/muster-io/muster/.github/workflows/release.yml@refs/tags/$VERSION" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
helm install muster oci://ghcr.io/muster-io/charts/muster@sha256:… \
  --namespace muster --create-namespace -f values.yaml
```

**The SBOM of the image** is an attestation that BuildKit attaches when the image is pushed, not a cosign attestation.
Read it with Docker:

```sh
docker buildx imagetools inspect ghcr.io/muster-io/muster:$VERSION --format '{{ json .SBOM }}'
```

It prints the software bill of materials, one per platform.

**The archives** are attached to the GitHub release: `muster_<version>_<os>_<arch>.tar.gz` for Linux and macOS on amd64
and arm64, `checksums.txt` with their SHA-256 sums, and one SPDX JSON software bill of materials per archive. Download
the archive and `checksums.txt` into one directory and check it:

```sh
sha256sum --ignore-missing -c checksums.txt
```

On macOS use `shasum -a 256 -c checksums.txt --ignore-missing`. `checksums.txt` is not signed: it finds a broken or
swapped download from the same release, not a swapped release. For that, verify the image.

# Requirements and tested versions

What Muster needs, which versions it was checked against, and how to verify that a release you download is the one the
project built. "Tested" means different things for different rows, so each row says which.

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

Telegram
:   The cloud Bot API 10.3, on a **development stand**, with a test bot and a test channel. CI runs against a fake. See
    [Telegram](../messengers/telegram.md).

The web UI
:   Its component tests and its end-to-end tests run in Chromium in **CI**. Other browsers are not tested.

## Image tags

The images are `ghcr.io/muster-io/muster` and the tags are:

- `vX.Y.Z`: a release. Use it, so that an upgrade is a decision you make.
- `latest`: the newest release that is not a prerelease. A prerelease tag, such as `v1.4.0-rc.1`, never moves it.
- `edge`: the newest commit on `master`, a development image.
- `sha-<7 hex digits>`: the development image of one commit.

## Verify a release

!!! note "Before the first release"
    No release exists yet, so there is nothing to verify, and the commands below have not been run against a real
    release. The development images, `edge` and `sha-…`, are **not signed** and carry no SBOM attestation: the workflow
    that builds them skips signing. Use these commands from the first release on, with its tag.

A release is built by the workflow `release.yml` of the repository, on a tag `vX.Y.Z`. It signs the container image
with [cosign](https://docs.sigstore.dev/cosign/), keyless, with the identity of that workflow run, so a valid signature
proves that the image was built by that workflow on that tag. Install cosign v3 or newer (the release signs with v3.1.3,
in the new signature format), then:

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

The Helm chart is pushed with the release to `oci://ghcr.io/muster-io/charts` and is not signed by the release workflow.

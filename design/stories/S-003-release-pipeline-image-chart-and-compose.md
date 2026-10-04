---
id: S-003
title: Release pipeline, container image, Helm chart and compose example
capability: C-01
kind: infra
layer: L1
depends_on: [S-002]
covers: [C-01.FR-1, C-01.FR-9, C-01.FR-10, C-01.FR-11, C-01.FR-15, C-01.AC-2, C-01.AC-3, C-01.AC-5]
files_touched:
  - .goreleaser.yaml
  - Dockerfile
  - .github/workflows/release-please.yml
  - .github/workflows/release.yml
  - release-please-config.json
  - .release-please-manifest.json
  - deploy/helm/muster/Chart.yaml
  - deploy/helm/muster/values.yaml
  - deploy/helm/muster/values.schema.json
  - deploy/helm/muster/.helmignore
  - deploy/helm/muster/templates/_helpers.tpl
  - deploy/helm/muster/templates/deployment.yaml
  - deploy/helm/muster/templates/services.yaml
  - deploy/helm/muster/templates/serviceaccount.yaml
  - deploy/helm/muster/templates/ingress.yaml
  - deploy/helm/muster/templates/httproute.yaml
  - deploy/helm/muster/templates/pdb.yaml
  - deploy/helm/muster/templates/networkpolicy.yaml
  - deploy/helm/muster/templates/servicemonitor.yaml
  - deploy/helm/muster/templates/NOTES.txt
  - deploy/helm/muster/ci/all-options-values.yaml
  - deploy/compose/docker-compose.yml
  - deploy/compose/.env.example
  - Makefile
  - .github/workflows/ci.yml
acceptance:
  - "[C-01.FR-1, C-01.FR-9, C-01.AC-2] A release tag produces binaries for linux and darwin on amd64 and arm64, a multi-arch image ghcr.io/muster-io/muster whose amd64 and arm64 images pass `cosign verify` against the release workflow's identity, an SBOM attached to the release, and the chart oci://ghcr.io/muster-io/charts/muster."
  - "[C-01.FR-9] The image is distroless, runs as a non-root user and has `muster` as its entry point; the changelog of the release is written by release-please from conventional commits."
  - "[C-01.FR-10, C-01.AC-2] The chart installs from the OCI registry with only `existingSecret` set; without it, rendering fails with a message that the master keys are never generated."
  - "[C-01.FR-10] The chart defaults `image.tag` to its appVersion and `replicas` to 1, always creates the app, ingest and internal Services, renders Ingress, HTTPRoute, ServiceMonitor, NetworkPolicy and PodDisruptionBudget only when enabled, and `make helm-check` renders it with default and all-options values and validates both with kubeconform."
  - "[C-01.FR-11] CI scans the image with Trivy and fails on high and critical vulnerabilities that have a fix."
  - "[C-01.AC-3] `docker compose -f deploy/compose/docker-compose.yml config` passes, and the image the example references prints its version and commit with `muster version`."
  - "[C-01.FR-15, C-01.AC-5] The compose example sets a memory limit on the PostgreSQL container, and PostgreSQL started from it reports the example's `shared_buffers`, `work_mem` and `max_connections` instead of its built-in defaults."
verify: "make ci helm-check compose-check"
operator_attention: true
issue: 3
---

# S-003. Release pipeline, container image, Helm chart and compose example

## Scope

**IN**

- release-please keeps a release pull request open; merging it tags the release.
- goreleaser builds the binaries, the multi-arch distroless image, the SBOM and keyless cosign signatures; the release
  workflow publishes them and pushes the chart to the OCI registry.
- Trivy on the image in CI.
- The Helm chart skeleton with every value of C-01.FR-10 and its CI rendering checks.
- The docker-compose example with Muster and a memory-capped PostgreSQL.

**OUT**

- The init container that runs `muster migrate`, the probes and `MUSTER_MIGRATE_ON_START` in compose (S-006); the
  master key placeholder refusal (S-007). Until S-006 merges, a pod or a compose service starts the binary but it has
  nothing to serve.
- Chart alert rules and the dashboard (S-059); the nightly footprint measurement (S-004).

## Contracts

- **Names** (C-01.FR-1): image `ghcr.io/muster-io/muster`, chart `oci://ghcr.io/muster-io/charts/muster`.
- **Release flow** (C-01.FR-9): `.github/workflows/release-please.yml` keeps the release pull request with the
  changelog from conventional commits; the tag `vX.Y.Z` triggers `.github/workflows/release.yml`, which runs goreleaser
  (`.goreleaser.yaml`): binaries for linux and darwin on amd64 and arm64 with `-ldflags` version and commit, the image
  per architecture plus the multi-arch manifest, an SPDX SBOM, keyless cosign signatures of the image digests
  (GitHub OIDC), checksums; then `helm package` and `helm push` of the chart with `version` and `appVersion` set to the
  release.
- **Image**: `Dockerfile` copies the goreleaser binary into `gcr.io/distroless/static-debian12:nonroot`; entry point
  `muster`, user `nonroot`, no shell.
- **Helm chart** (C-01.FR-10), main values:

  | Value | Default | Meaning |
  |---|---|---|
  | `image.repository`, `image.tag` | `ghcr.io/muster-io/muster`, the chart's `appVersion` | |
  | `replicas` | `1` | `2` is an option, together with `podDisruptionBudget.enabled` |
  | `existingSecret` | required | the Secret with the master keys, the database password and the bootstrap Admin password; rendering fails without it ("the master keys are never generated: create a Secret and set existingSecret") |
  | `database.host`, `.port`, `.name`, `.user`, `.sslmode`, `database.passwordKey` | — | plain values; the password is mounted from the Secret key and passed as `MUSTER_DATABASE_PASSWORD_FILE`; `database.urlKey` instead passes a whole `MUSTER_DATABASE_URL` from the Secret |
  | `database.sessionHost`, `.sessionPort` | unset | the session connection for PgBouncer installations |
  | `publicURL`, `ingestURL` | required, optional | `MUSTER_PUBLIC_URL`, `MUSTER_INGEST_URL` |
  | `bootstrapAdmin.email`, `bootstrapAdmin.passwordKey` | unset | `MUSTER_BOOTSTRAP_ADMIN_EMAIL`, `MUSTER_BOOTSTRAP_ADMIN_PASSWORD_FILE` |
  | `ingress.*`, `httpRoute.*` | disabled | expose the app and ingest Services separately |
  | `serviceMonitor.enabled`, `networkPolicy.enabled`, `podDisruptionBudget.enabled` | false | objects a cluster may lack render only when enabled |

  The app, ingest and internal Services always exist. The chart has no chart dependencies and no PostgreSQL (ADR-0006).
- **Chart checks**: `make helm-check` runs `helm lint`, `helm template` with the default values plus an
  `existingSecret`, and with `ci/all-options-values.yaml`, and validates the output with kubeconform (including the
  Gateway API and ServiceMonitor schemas); CI runs it on every pull request.
- **Compose example** (`deploy/compose/`): services `postgres` (PostgreSQL 17) and `muster` (the released image).
  PostgreSQL gets a container memory limit and its settings on the command line — starting values `mem_limit: 160m`,
  `shared_buffers=32MB`, `work_mem=2MB`, `max_connections=30` — sized for the footprint of NFR-3 and provisional
  (P-43) until the nightly load test measures them. `.env.example` holds the master key placeholder that S-007 refuses
  and the bootstrap Admin; `make compose-check` runs `docker compose config`.
- **Trivy** (C-01.FR-11): CI builds the image with goreleaser's snapshot mode and scans it; high and critical
  vulnerabilities with a fix fail the job.

## Steps

1. Add the Dockerfile and `.goreleaser.yaml`. Check: `goreleaser release --snapshot --clean` produces four binaries and
   two images, and `docker run --rm <snapshot image> version` prints the version and commit.
2. Add the release-please configuration and both release workflows. Check: after merging a conventional commit to
   `master`, release-please opens a release pull request with the changelog entry.
3. Add the Trivy job. Check: the job runs on a pull request and reports the scanned image.
4. Write the chart. Check: `make helm-check` passes; rendering without `existingSecret` fails with the message above.
5. Write the compose example. Check: `make compose-check` passes and the PostgreSQL settings are reported as in
   Verification.
6. Operator: allow the release workflow to push to GHCR, then cut a pre-release (for example `v0.0.1-rc.1`). Check: the
   published artifacts pass the checks of Verification.

## Verification

```sh
make helm-check && echo ok
# ok
helm template muster deploy/helm/muster --set publicURL=https://muster.example.org; echo "exit=$?"
# Error: ... the master keys are never generated: create a Secret and set existingSecret
# exit=1

make compose-check && echo ok
# ok
cp deploy/compose/.env.example deploy/compose/.env
docker compose -f deploy/compose/docker-compose.yml up -d postgres
docker compose -f deploy/compose/docker-compose.yml exec postgres \
  psql -U muster -Atc "SHOW shared_buffers; SHOW work_mem; SHOW max_connections"
# 32MB
# 2MB
# 30
docker inspect -f '{{.HostConfig.Memory}}' "$(docker compose -f deploy/compose/docker-compose.yml ps -q postgres)"
# 167772160

goreleaser release --snapshot --clean
docker run --rm ghcr.io/muster-io/muster:<snapshot tag>-amd64 version
# muster 0.0.1-SNAPSHOT-<sha> (commit <sha>)
```

After the operator's pre-release tag:

```sh
cosign verify ghcr.io/muster-io/muster:v0.0.1-rc.1 \
  --certificate-identity-regexp 'https://github.com/muster-io/muster/.github/workflows/release.yml@refs/tags/v.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com > /dev/null && echo verified
# verified        (repeated with the amd64 and arm64 digests from `docker manifest inspect`)
docker run --rm ghcr.io/muster-io/muster:v0.0.1-rc.1 version
# muster 0.0.1-rc.1 (commit <sha>)
kubectl create namespace muster && kubectl -n muster create secret generic muster \
  --from-literal=secret-keys="$(openssl rand -base64 32)" --from-literal=database-password=x
helm install muster oci://ghcr.io/muster-io/charts/muster --version 0.0.1-rc.1 -n muster \
  --set existingSecret=muster --set publicURL=https://muster.example.org --set database.host=db
kubectl -n muster get deploy,svc
# deployment.apps/muster ... service/muster-app, service/muster-ingest, service/muster-internal
```

The GitHub release lists the four archives, the checksums and the SBOM.

## Open questions

1. The first published version: a pre-release `v0.0.1-rc.1` exercises the pipeline without promising anything. The
   operator decides when the first real release (`v0.1.0`) is cut.

## Notes

- Suggested commit: `build: add release pipeline, container image, Helm chart and compose example`.
- `operator_attention: true` — GHCR permissions, the first tag and the kind cluster for the install check.
- The compose memory values are provisional (P-43); S-004's nightly load test measures the footprint, and a change
  updates the compose file and closes P-43.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-01.FR-1 | partial | image and chart names; repository and module are S-001 |
| C-01.FR-9 | full | |
| C-01.FR-10 | full | the startup probe and the init container of C-02 are S-006 |
| C-01.FR-11 | partial | Trivy; the other scanners are S-002 |
| C-01.FR-15 | partial | the capped PostgreSQL; the nightly footprint measurement is S-004 |
| C-01.AC-2 | full | the pod becomes ready from S-006 on |
| C-01.AC-3 | full | |
| C-01.AC-5 | full | |

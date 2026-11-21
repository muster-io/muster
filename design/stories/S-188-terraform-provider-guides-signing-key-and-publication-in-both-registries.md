---
id: S-188
title: "The Terraform provider's guides, its signing key, and its first release in the Terraform Registry and the OpenTofu Registry"
capability: C-33
kind: infra
layer: L2
repository: muster-io/terraform-provider-muster
depends_on: [S-187]
covers: [C-33.FR-10, C-33.FR-18, C-33.FR-20, C-33.AC-16, C-33.AC-17]
files_touched:
  - README.md
  - templates/index.md.tmpl
  - templates/guides/getting-started.md.tmpl
  - templates/guides/secrets.md.tmpl
  - templates/guides/managed-by.md.tmpl
  - templates/guides/compatibility.md.tmpl
  - internal/provider/docs_test.go
  - .github/workflows/release.yml
  - test/registry-install.sh
acceptance:
  - "[C-33.FR-18, C-33.AC-17] `make docs` renders the guides \"Getting started\" (the Service account with the Admin Role, its token, `MUSTER_ENDPOINT` and `MUSTER_TOKEN`, the first `terraform apply` of an Integration and its token), \"Secrets\" (both ways of every Secret, with Terraform 1.11 and OpenTofu 1.11 for the write-only one, what an older CLI answers, and that changes of a Secret outside Terraform cannot be seen), \"Managed by\" (what the mark does in the web UI, that it never blocks, and how the objects that exist once are adopted and forgotten) and \"Compatibility\" (each provider version with the Muster, Terraform and OpenTofu versions it works with); every resource and data source page has its example and its import; `make docs-check` passes."
  - "[C-33.FR-10] The \"Secrets\" guide lists every resource attribute that is a Secret pair and every value shown once; `docs_test.go` walks the provider's schema and fails naming a Secret pair or a shown-once value that the guide's table lacks."
  - "[C-33.FR-20, C-33.AC-16] The tag `v0.1.0`, made by merging the release pull request, runs `release.yml`, which publishes ten archives, `terraform-provider-muster_0.1.0_SHA256SUMS`, `terraform-provider-muster_0.1.0_SHA256SUMS.sig` and `terraform-provider-muster_0.1.0_manifest.json` on the GitHub release; `gpg --verify` of the signature with the public key of the README passes."
  - "[C-33.FR-20, C-33.AC-16] `test/registry-install.sh 0.1.0` runs, in empty directories, `terraform init` with `source = \"muster-io/muster\"` and `tofu init` with the same source, each installing 0.1.0 and reporting it as signed with the project's key ID; the Terraform Registry and the OpenTofu Registry show the provider with its documentation."
verify: "make ci"
operator_attention: true
issue: 706
---

# S-188. The Terraform provider's guides, its signing key, and its first release in the Terraform Registry and the OpenTofu Registry

## Scope

**IN**

- The guides of C-33.FR-18 and the final index page and README, with the installation from both Registries.
- The signing key in the repository's secrets, the provider's namespace and key in both Registries, and the first
  release `v0.1.0`.

**OUT**

- Muster's own page on configuration as code and the link from the web UI's mark (S-189).

## Contracts

- **Guides** (`templates/guides/*.md.tmpl`, rendered by tfplugindocs into `docs/guides/`): the four guides of the
  acceptance statements; "Getting started" and "Managed by" use `examples/complete/main.tf` (S-187) in parts; the
  "Secrets" guide's table of Secret pairs and shown-once values is checked against the provider's schema by
  `internal/provider/docs_test.go`, so that a new Secret cannot be left out of it. "Compatibility" states, for
  `0.1.x`, Muster from the minimum of C-33.FR-4 with no upper bound, Terraform and OpenTofu from 1.11 tested, and the
  sensitive attributes on older CLIs untested.
- **Signing** (D757): an RSA key of 4096 bits — the Terraform Registry does not accept other key types — made by the
  maintainer, with the identity "Muster Authors"; its armoured private key and passphrase are the repository secrets
  `GPG_PRIVATE_KEY` and `PASSPHRASE`, and its public key and ID are in the README. `release.yml` (S-182) signs with it;
  this story adds nothing to it but the check that the secrets are set, which already fails clearly without them.
- **Registries**: the Terraform Registry — the maintainer signs in with GitHub, authorises the Registry for the
  `muster-io` organisation, adds the public key to the namespace `muster-io` and publishes the repository, after which
  the Registry takes every release through its webhook; the OpenTofu Registry — the maintainer submits the provider
  `muster-io/muster` and its signing key through the forms of `opentofu/registry`, which then reads the GitHub releases.
- **Release**: the release pull request that release-please keeps open since S-182 is merged by the maintainer, which
  makes `v0.1.0`; the release notes name the Muster versions it works with.
- **Install check** (`test/registry-install.sh <version>`): writes a `required_providers` block with
  `source = "muster-io/muster"` and `version = "<version>"`, runs `terraform init` and `tofu init` in separate empty
  directories and checks that each installed that version and printed it as signed.

## Steps

1. Write the guides, the index and the README. Check: `make docs-check`.
2. The maintainer makes the key and the Registry entries (Notes). Check: the namespace pages show the key.
3. The maintainer merges the release pull request. Check: the release's assets; `gpg --verify`.
4. Run `test/registry-install.sh 0.1.0`. Check: both installs report the signature.

## Verification

```sh
make ci                                               # docs-check among it
gh release view v0.1.0 --json assets -q '.assets[].name' | sort | tail -3
# terraform-provider-muster_0.1.0_SHA256SUMS
# terraform-provider-muster_0.1.0_SHA256SUMS.sig
# terraform-provider-muster_0.1.0_manifest.json
gh release download v0.1.0 -p '*SHA256SUMS*' -D /tmp/rel && gpg --verify /tmp/rel/*.sig /tmp/rel/*SHA256SUMS
# Good signature from "Muster Authors …"
test/registry-install.sh 0.1.0
# terraform: - Installed muster-io/muster v0.1.0 (signed, key ID …)
# tofu:      - Installed muster-io/muster v0.1.0 (signed, key ID …)
```

The pull request with the guides carries `make ci`; the release, its verification and the install check are recorded in
a comment on this story's issue, since they follow the merge.

## Open questions

None.

## Notes

- Suggested commit: `docs: add the guides and the installation from both registries`.
- **Maintainer** (operator attention, journal D757): make the signing key and add the secrets `GPG_PRIVATE_KEY` and
  `PASSPHRASE` to the repository (never in a log or an issue); sign in to the Terraform Registry, add the public key to
  the namespace `muster-io` and publish `muster-io/terraform-provider-muster`; submit the provider and the key to the
  OpenTofu Registry; merge the release pull request when the guides are merged. The agent does none of these and lists
  them in the pull request.
- A release that the Registries reject — a wrong key type, a missing manifest — is fixed by a new patch release, never
  by moving a tag.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-33.FR-10 | partial | the documentation of the Secret pairs and the CLI versions |
| C-33.FR-18 | partial | the guides; Muster's page is S-189 |
| C-33.FR-20 | partial | the key, the Registries and the first release; with S-182 complete |
| C-33.AC-16 | full | |
| C-33.AC-17 | partial | the Registry pages and guides; Muster's page and the link are S-189 |

---
id: S-189
title: "Documentation: configuration as code with the Terraform provider, and the link from the \"Managed by\" mark"
capability: C-33
kind: docs
layer: L2
depends_on: [S-181, S-188]
covers: [C-33.FR-18, C-33.AC-17]
files_touched:
  - docs/configure/terraform.md
  - docs/configure/api-tokens.md
  - mkdocs.yml
  - web/src/lib/docs-links.json
  - web/src/components/managed-by.tsx
  - web/src/components/managed-by.test.tsx
  - web/e2e/managed-by.spec.ts
acceptance:
  - "[C-33.FR-18, C-33.AC-17] Configure → \"Configuration as code\" (`configure/terraform.md`) says what the provider manages and what it leaves out (C-33.FR-5), links to its pages in the Terraform Registry and the OpenTofu Registry, shows the provider block with a Service account token with the Admin Role and the first resources, explains the `managed_by` mark with the section `#the-managed-by-mark` — what the web UI shows, that it never blocks a change, that the next apply undoes a change made in the web UI, and how to clear the mark through the API — and states the minimum Muster version; `api-tokens.md` links to it from the Service accounts section; `make docs-check` passes."
  - "[C-33.AC-17] The mark \"Managed by {tool}\" of S-181 links to `configure/terraform.md#the-managed-by-mark` through the entry `field.managed_by` of `docs-links.json`, which `docslinkcheck` checks against the built site; in a real browser the link opens that section of the documentation of the running version (`managed-by.test.tsx`, `managed-by.spec.ts`)."
verify: "make docs-check && make e2e-web"
operator_attention: false
issue: 707
---

# S-189. Documentation: configuration as code with the Terraform provider, and the link from the "Managed by" mark

## Scope

**IN**

- The page "Configuration as code" on Muster's documentation site, in the Configure section, and a link to it from the
  Service accounts section.
- The documentation link of the web UI's mark (S-181), through the documentation links of S-108.

**OUT**

- The provider's own documentation and guides in the Registries (S-182 to S-188).

## Contracts

- **Page** (`docs/configure/terraform.md`): sections "What the provider manages" (the resources and data sources of
  C-33.FR-5 and C-33.FR-6, and what stays out), "Set it up" (the Service account, its token, the provider block, the
  minimum Muster version and the Registries' links), "The Managed by mark" (anchor `#the-managed-by-mark`) and
  "Secrets" (one paragraph and the link to the provider's guide). It quotes the web UI's texts of C-33.FR-12 in English.
  No screenshot is needed: the mark is a short text next to a title, which the paragraph quotes.
- **Navigation** (`mkdocs.yml`): `configure/terraform.md` after `configure/api-tokens.md`.
- **Link** (`docs-links.json`, `managed-by.tsx`): the entry `field.managed_by` →
  `configure/terraform.md#the-managed-by-mark`; `ManagedByBadge` renders the mark as a link to it, in the way the
  other title links of S-108 do.

## Steps

1. Write the page and its navigation entry, and link it from `api-tokens.md`. Check: `make docs-check`.
2. Add the link entry and make the mark a link, with its tests. Check: `make test-web`, `make e2e-web` with
   `managed-by.spec.ts`.

## Verification

```sh
make docs && grep -c 'id="the-managed-by-mark"' site/configure/terraform/index.html     # 1
make docs-check                                                                          # passes, docslinkcheck included
```

Then `make dev`, mark a Route through the API as in S-181, and in Playwright at 1280 × 800 open the Route: the mark
"Managed by terraform" is a link whose `href` ends in `configure/terraform/#the-managed-by-mark` of the documentation
version the server reports; `make e2e-web` runs this in `managed-by.spec.ts`.

## Open questions

None.

## Notes

- Suggested commit: `docs: describe configuration as code with the terraform provider`.
- The page follows the provider's first release (S-188), so that its links to the Registries resolve.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-33.FR-18 | partial | Muster's page; the provider's documentation is S-182 and S-188 |
| C-33.AC-17 | partial | Muster's page and the link; the Registry pages are S-188 |

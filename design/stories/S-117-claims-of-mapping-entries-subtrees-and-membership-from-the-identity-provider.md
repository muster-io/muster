---
id: S-117
title: The claim of every mapping entry and subtrees for Roles and Teams, and Team membership from the identity provider (BE)
capability: C-25
kind: be
layer: L2
depends_on: [S-115, S-116]
covers: [C-25.FR-1, C-25.FR-4, C-25.FR-11, C-25.FR-12, C-25.AC-3, C-25.AC-4, C-03.FR-5, C-03.FR-6, C-03.FR-7, C-03.FR-30, C-03.AC-28]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/0025_identity_provider_claims_and_team_entries.up.sql
  - internal/db/migrations/0025_identity_provider_claims_and_team_entries.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/oidc/claims.go
  - internal/oidc/claims_test.go
  - internal/oidc/settings.go
  - internal/oidc/settings_test.go
  - internal/oidc/signin.go
  - internal/oidc/signin_test.go
  - internal/oidc/link.go
  - internal/oidc/link_test.go
  - internal/oidc/recheck.go
  - internal/oidc/recheck_test.go
  - internal/oidc/query.sql
  - internal/oidc/oidc_integration_test.go
  - internal/api/oidc.go
  - internal/api/oidc_test.go
  - internal/teams/teams.go
  - internal/teams/idp.go
  - internal/teams/idp_test.go
  - internal/teams/query.sql
  - internal/teams/teams_test.go
  - internal/teams/teams_integration_test.go
  - internal/api/teams.go
  - internal/api/teams_test.go
  - internal/runtime/runtime.go
  - internal/fakes/fakeoidc/fakeoidc.go
  - internal/fakes/fakeoidc/fakeoidc_test.go
  - test/e2e/oidc_test.go
  - test/e2e/team_membership_test.go
  - web/src/components/group-mapping-editor.tsx
  - web/src/components/group-mapping-editor.test.tsx
  - web/src/test/fixtures/teams.ts
  - web/src/components/oidc-warnings.tsx
  - web/src/components/oidc-warnings.test.tsx
  - internal/archlint/secretleak.go
  - docs/sign-in/oidc-gitlab.md
  - docs/sign-in/oidc-keycloak.md
  - docs/concepts/teams.md
  - docs/install/versions.md
  - AGENTS.md
  - design/prd/l1/C-03-sign-in-and-access.md
  - design/prd/l2/C-25-teams.md
acceptance:
  - "[C-03.FR-5, C-03.AC-28] Every entry of the Role mapping (`OidcGroupMapping`) takes an optional `claim`, by default `groups_claim`; a claim is read from the ID token, then from userinfo; an entry `a/b/*` matches `a/b` and every group below it at `/` boundaries only, any other entry matches exactly, and the highest Role found over all entries wins: against the fake OIDC server in its GitLab mode, which puts the role claims and `groups` in userinfo only (F-117), `https://gitlab.org/claims/groups/owner` `example/platform/*` → Admin and `groups` `example/platform/*` → Viewer make an Owner of `example/platform` (whose direct-only claim lists just `example/platform`, F-118) and an Owner of `example/platform/infra` Admins and a person in `example/platform/apps` a Viewer, and `example/platformx` matches neither; a claim found in the ID token (the Keycloak flavor) is read the same way (`claims_test.go`, `oidc_test.go`)."
  - "[C-03.FR-6] `getOidcSettings` carries one warning `groups_claim_missing` per claim that an entry names, the groups claim included, each with `claim`; a connection check that finds a claim not listed in discovery's `claims_supported` raises its warning, one that finds it listed clears it, and a sign-in whose tokens carry it clears it for good; `claims_supported` of the last check is read back on the settings for the editors to suggest (`settings_test.go`)."
  - "[C-03.FR-7, C-03.AC-28] A refused sign-in writes `session.oidc_refused` with `claims`, the values of every claim the mapping read, by claim (`signin_test.go`)."
  - "[C-25.FR-4, C-25.AC-3] A Team takes `idp_entries` (`{claim, group}`, the claim optional), matched as the Role mapping's; at every OIDC sign-in and every successful background re-check, with or without `oidc.sync_role`, a User joins each Team with an entry that matches them and leaves each Team they are in only through entries that no longer match; a person added by hand stays and, when an entry also matches, has both sources: against the fake OIDC server, `teams/payments/*` takes in a User with `teams/payments` and one with `teams/payments/oncall`, not one with `teams/paymentsx`; after the second User's group is removed, their next sign-in takes them out; the Audit log has `team.user_added` and `team.user_removed` by the system with the source `idp` and the entry; the Roles of all three are unchanged (`idp_test.go`, `team_membership_test.go`)."
  - "[C-25.FR-4, C-25.AC-4, C-03.FR-30] An entry naming `https://gitlab.org/claims/groups/maintainer` matches a User whose userinfo carries that claim with the group, where GitLab puts it (F-117), and also a User whose ID token carries it, with a provider that does; with an identity provider that grants no offline token, membership changes only at sign-in; a refused or unavailable re-check changes no membership (`idp_test.go`, `recheck_test.go`)."
  - "[C-25.FR-4] `removeTeamUser` takes away only the source `by_hand`: a person also in the Team through the identity provider stays in it with the source `idp`, and `team.user_removed` records the sources before and after; a person in the Team only through the identity provider, with no source `by_hand` to take away, is `404` (`teams_test.go`, `team_membership_test.go`)."
  - "[C-25.FR-1, C-25.FR-11] `idp_entries` are part of `TeamInput`, saved with `If-Match` and recorded in the diff of `team.updated`; `listTeamUsers` gives a person the source `idp` with the entries that matched them; a change of a Team's entries changes nobody's membership until their next sign-in or re-check (`teams_test.go`)."
  - "[C-03.FR-5] Until S-119 gives the Role mapping editor its claim column, the L1 editor of the OIDC settings keeps the `claim` of every entry it loads and sends it back unchanged on save, so an entry of another claim is never saved against the groups claim (`group-mapping-editor.test.tsx`)."
  - "[C-25.FR-12] The documentation of GitLab sign-in maps Roles with GitLab's role claims and subtrees as the facts of S-116 say, the Keycloak page names the claim of an entry, and Concepts → Teams has the section \"Membership from the identity provider\"; `make docs-check` passes."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: false
issue: 406
---

# S-117. The claim of every mapping entry and subtrees for Roles and Teams, and Team membership from the identity provider (BE)

## Scope

**IN**

- The revised C-03.FR-5 to FR-7 (issue #369): the claim of every entry of the Role mapping, read from the ID token and
  then from userinfo; subtrees `a/b/*`; a warning `groups_claim_missing` per claim; the refusal's Audit log with every
  claim read.
- Team entries of the identity provider with the same matching, and Team membership that follows them at every OIDC
  sign-in and successful re-check (C-25.FR-4, C-03.FR-30 revised).
- `removeTeamUser` taking away only the source `by_hand`, so that removing a person by hand never removes the membership
  the identity provider gives them (C-25.FR-4).
- The rewording of C-03.AC-28 and C-25.AC-4 by the facts of S-116 (a subtree entry against a direct-only role claim,
  the claim found in userinfo for GitLab), which the maintainer settled for this pull request.
- The GitLab mode of the fake OIDC server with GitLab's role claims where the facts of S-116 put them.
- The documentation of both.

**OUT**

- The group mapping editor with a claim per entry and the claims' suggestions, and the Team page's "Identity provider
  groups": S-119.
- Teams by label and matchers: S-118. Schedules, which skip the Shifts of people no longer in the Team: S-131.

## Contracts

- **Facts** (S-116, F-116 to F-120): GitLab lists the three role claims in discovery; the role claims and `groups` come
  only from userinfo and the ID token has only `groups_direct`; a role claim lists only the groups where the person
  holds the role directly, never inherited subgroups, and a person has only the claim of their own role. The reading
  order stays the ID token, then userinfo, which finds GitLab's claims in userinfo. An entry `a/b/*` against a
  direct-only role claim must still match the subgroups of a direct group, because the subtree rule does that; an exact
  entry `a/b/c` does not match an Owner of `a/b`, and the documentation says to use a subtree entry.
- **Spec** (`api/openapi.yaml`, changed first, then generated):
  - `OidcGroupMapping` gains `claim` (string, optional; omitted or empty reads `groups_claim`); `group` is described
    with the subtree rule; `OidcWarning` gains `claim` (string, set for `groups_claim_missing`); `OidcSettings` gains
    the read-only `claims_supported` (string[], what the last connection check found in discovery, empty before one).
  - `TeamInput` gains `idp_entries` (`TeamIdpEntry`[] {`claim` (optional), `group`}); `TeamUser` gains `idp_entries`,
    the entries that matched the person at their last sign-in or re-check; `TeamUser.sources` takes `idp`.
  - Refusals: an empty `group`, or `*` anywhere but a trailing `/*`, is `422` `invalid_format` at the entry's pointer,
    in both mappings.
  - `removeTeamUser` clears the source `by_hand`; the row stays while `by_idp` is set. Its `404` is a User with no
    source `by_hand` in the Team, including a person who is in it only through the identity provider.
- **Schema** (migration `0025_identity_provider_claims_and_team_entries`, expand only; `schema.md` §4.3 and §4.15):
  - `oidc_settings.claims_seen jsonb NOT NULL DEFAULT '{}'` (claim → when a sign-in first carried it),
    `oidc_settings.claims_missing text[] NOT NULL DEFAULT '{}'` (claims the last check found not advertised) and
    `oidc_settings.claims_supported text[] NOT NULL DEFAULT '{}'`; `groups_claim_seen_at` and
    `groups_claim_missing_since` stay and are written in step for the groups claim, so a 0.1 replica shows the same
    warning for it.
  - `oidc_settings.role_mappings jsonb NOT NULL DEFAULT '[]'` holds the entries of L2 (`{claim, group, role}`), filled
    by the migration from `group_mappings`; 0.2.0 reads only it. The old column `group_mappings`, which a 0.1 replica
    reads against the groups claim, is written with only the entries such a replica reads the same way — the groups
    claim and an exact group — so that during a rolling update a 0.1 replica never grants a Role through an entry of
    another claim or a subtree; it matches fewer people instead (Notes). 0.3.0 drops the old column.
  - `team_idp_entries`: `id`, `org_id`, `team_id`, `claim` (nullable), `group_name`, `position`; replaced as a whole on
    update, like `route_matchers`.
  - `team_users.by_idp boolean NOT NULL DEFAULT false` and `team_users.idp_matches jsonb NOT NULL DEFAULT '[]'` (the
    entries that matched); `CHECK (by_hand OR by_idp)` added `NOT VALID` and validated in a second transaction.
- **OIDC** (`internal/oidc`): `claims.go` reads every claim the mappings name from the verified ID token, then from
  userinfo for those it lacks (one userinfo call per sign-in, as for the groups claim), as string lists, and matches
  entries — exact, or the subtree of `a/b/*` at `/` boundaries, no other globs; `MapRole` takes the claims and the
  entries and keeps "the highest Role wins"; sign-in, linking and the background re-check use it; `session.oidc_refused`
  details carry `claims` {claim: values}; the warnings per claim follow C-03.FR-6 for each claim; the check stores
  `claims_supported`. On a successful sign-in or re-check the service calls the membership hook with the User and the
  claims, in the same transaction.
- **Teams** (`internal/teams/idp.go`): `SyncMembership(ctx, tx, user, claims)` — for each Team with a matching entry,
  add the User with `by_idp` (or set `by_idp` on a person added by hand) and record the matches; for each Team where
  the User is `by_idp` and no entry matches any more, clear `by_idp` and delete the row when not `by_hand`; each change
  is an Audit log entry `team.user_added` or `team.user_removed` by the system with the details `{"source": "idp",
  "entry": {claim, group}}`, and the hint `team`. It never changes a Role. A refused or unavailable re-check does not
  call it.
- **Fake OIDC server** (`internal/fakes/fakeoidc`): in the GitLab mode a scripted user may carry the claims
  `https://gitlab.org/claims/groups/owner`, `…/maintainer` and `…/developer`, placed in userinfo only, with `groups`, while the ID
  token carries `groups_direct` alone (F-117); a scripted user's `role_claims` hold the direct groups of each role, only
  the claims of the roles they hold are present (F-118, F-119), and `/_fake/next-user` takes `role_claims`; the
  Keycloak flavor keeps putting its claims in the ID token, so that both readings are tested; discovery lists the three
  role claims, `groups` and `groups_direct` in `claims_supported` (F-116).
- **AGENTS.md**: the paragraph on development mode names the GitLab mode of the fake OIDC server and the role claims it
  serves.
- **Role mapping editor of L1** (`web/src/components/group-mapping-editor.tsx`, used by
  `web/src/routes/admin.oidc.tsx`): `MappingRow` gains `claim`, which `mappingRows` takes from each entry and
  `mappingsOf` sends back unchanged; the page shows no claim until S-119. Without it, saving the OIDC page between this
  story and S-119 would re-save an entry such as `…/owner` `example/platform/infra` → Admin without its claim, against
  the groups claim, making Admin anyone whose groups list that group.
- **Web test fixtures** (`web/src/test/fixtures/teams.ts`, S-115): the builders of Team reads set `idp_entries` to `[]`
  and those of `TeamUser` reads `idp_entries` to `[]`, so that `make generate` keeps the TypeScript check green.
- **Settings**: `oidc.sync_role`, `oidc.unmatched_role`, `auth.oidc_recheck_interval`.
- **Log events, metrics**: none new; `muster_oidc_checks_total` counts the re-checks as before.
- **Documentation**: `docs/sign-in/oidc-gitlab.md` — "Map groups to Roles" with the role claims and subtrees;
  `docs/sign-in/oidc-keycloak.md` — the claim of an entry; `docs/concepts/teams.md` — "Membership from the identity
  provider", with the claim of each entry, subtrees, sign-in and re-checks, people added by hand and GitLab without an
  offline token; `docs/install/versions.md` — the rolling-update note of 0.2.0 below, in the paragraph on the rolling
  upgrade from 0.1 (S-111).

## Steps

1. Read S-116's facts and set the GitLab mode of the fake OIDC server by them. Check: `fakeoidc_test.go` covers both
   placements and discovery.
2. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up; a 0020 database with a
   mapping and the warning keeps both.
3. Write `claims.go` and change sign-in, linking, re-checks, the warnings and the refusal. Check: `claims_test.go`
   covers exact, subtree and boundary cases (`a/b`, `a/b/c`, `a/bx`, `a/b/*` vs `a/*`), claims in either token, and
   the highest Role; `signin_test.go`, `link_test.go`, `recheck_test.go`, `settings_test.go`.
4. Write the Team entries and `SyncMembership`. Check: `idp_test.go` covers joining, leaving, both sources, a refused
   re-check and the Audit log; `teams_test.go` covers the entries in the API.
5. Carry `claim` through the L1 Role mapping editor and set the new Team fields in the fixtures. Check:
   `group-mapping-editor.test.tsx`; `make lint`.
6. Write the end-to-end tests and the documentation. Check: the Verification below; `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &     # the fake IdP on 127.0.0.1:18090 and the demo OIDC configuration of S-013
# the Admin's Personal access token A as in S-032
API=localhost:8080/api/v1; IDP=localhost:18090/_fake
PUTOS() { O=$(curl -s "${A[@]}" $API/oidc-settings); curl -s "${A[@]}" -X PUT -H "If-Match: $(jq -r .etag <<<"$O")" \
  $API/oidc-settings -d "$(jq -c "del(.etag, .warnings, .client_secret_status, .updated_at, .claims_supported) | $1" <<<"$O")" > /dev/null; }
SIGNIN() { curl -s -X POST $IDP/next-user -d "$2" > /dev/null; curl -s -c $1 -b $1 -L -o /dev/null "$API/sessions/oidc/start"; }
curl -s -X POST $IDP/config -d '{"flavor":"gitlab"}' > /dev/null
PUTOS '.groups_claim = "groups" | .unmatched_role = "none" | .sync_role = true | .group_mappings = [
  {"claim":"https://gitlab.org/claims/groups/owner","group":"example/platform/*","role":"admin"},
  {"group":"example/platform/*","role":"viewer"}]'

# C-03.AC-28: the role claim from userinfo, direct groups only, matched as a subtree; a near miss
SIGNIN u1 '{"sub":"g-1","preferred_username":"ann","groups":["example/platform","example/platform/infra"],"role_claims":{"owner":["example/platform"]}}'
curl -s -b u1 $API/me | jq -r .user.role                                                                  # admin
SIGNIN u2 '{"sub":"g-2","preferred_username":"ben","groups":["example/platform/infra"],"role_claims":{"owner":["example/platform/infra"]}}'
curl -s -b u2 $API/me | jq -r .user.role                                                                  # admin
SIGNIN u3 '{"sub":"g-3","preferred_username":"cid","groups":["example/platform/apps"]}'
curl -s -b u3 $API/me | jq -r .user.role                                                                  # viewer
SIGNIN u4 '{"sub":"g-4","preferred_username":"dan","groups":["example/platformx"]}'
curl -s "${A[@]}" "$API/audit-log?action=session.oidc_refused&limit=1" | jq -c '.items[0].details.claims'
# {"groups":["example/platformx"],"https://gitlab.org/claims/groups/owner":[]}

# C-03.FR-6: a warning per claim
curl -s "${A[@]}" -X POST $API/oidc-settings/checks > /dev/null
curl -s "${A[@]}" $API/oidc-settings | jq -c '[.warnings[] | select(.kind == "groups_claim_missing") | .claim]'
# the claims the facts say discovery does not list, else []

# C-25.AC-3: Team membership from the identity provider
TP=$(curl -s "${A[@]}" $API/teams -d '{"name":"Payments","slug":"payments","own_working_hours":false,
  "idp_entries":[{"group":"teams/payments/*"}]}' | jq -r .id)
PUTOS '.group_mappings += [{"group":"teams/*","role":"responder"}]'
SIGNIN p1 '{"sub":"t-1","preferred_username":"olga","groups":["teams/payments"]}'
SIGNIN p2 '{"sub":"t-2","preferred_username":"petr","groups":["teams/payments/oncall"]}'
SIGNIN p3 '{"sub":"t-3","preferred_username":"rita","groups":["teams/paymentsx"]}'
curl -s "${A[@]}" $API/teams/$TP/users | jq -c '[.items[] | {u: .user.name, sources}] | sort_by(.u)'
# [{"u":"olga","sources":["idp"]},{"u":"petr","sources":["idp"]}]
RITA=$(curl -s "${A[@]}" "$API/users?q=rita" | jq -r '.items[0].id'); curl -s "${A[@]}" $API/teams/$TP/users -d "{\"user_id\":\"$RITA\"}" > /dev/null
SIGNIN p2 '{"sub":"t-2","preferred_username":"petr","groups":["teams/other"]}'
curl -s "${A[@]}" $API/teams/$TP/users | jq -c '[.items[].user.name] | sort'                       # ["olga","rita"]
curl -s "${A[@]}" "$API/audit-log?action=team.user_removed&limit=1" | jq -c '.items[0] | {k: .actor.kind, s: .details.source, e: .details.entry.group}'
# {"k":"system","s":"idp","e":"teams/payments/*"}
curl -s "${A[@]}" "$API/users?q=petr" | jq -r '.items[0].role'                                           # responder
```

`test/e2e/oidc_test.go` and `test/e2e/team_membership_test.go` repeat these checks and add the re-check: with the
Keycloak flavor and an offline token, the IdP's groups change and an advance of the development clock by
`auth.oidc_recheck_interval` (`curl -s -X POST localhost:8082/_dev/clock -d '{"advance_seconds": 900}'`, after the
Personal access token A was made) makes the re-check due, since `oidc_checks.deadline` is on the business clock; the
User leaves the Team without signing in. `team_membership_test.go` also removes by hand a person who has both sources
and finds them still in the Team with the source `idp`.

The maintainer's own setup is checked live as well: `groups` `corp/*` → Responder and
`https://gitlab.org/claims/groups/owner` `corp` → Admin make an Owner of `corp` an Admin, a Developer of `corp/team-a`
only (whose `groups` lists the inherited `corp`) a Responder, and refuse someone outside `corp` with `unmatched_role`
`none`; a Team with the entry `corp/payments/*` takes in a person of `corp/payments/oncall`.

## Open questions

None. S-116's facts (F-116 to F-120) do not contradict C-03.FR-5 or C-25.FR-4. The pull request of S-116 proposed the
rewording of C-03.AC-28 and C-25.AC-4, which still named an exact entry and the ID token; the maintainer settled it,
and this pull request rewords both.

## Notes

- Suggested commit: `feat(oidc): read a claim per mapping entry and sync team membership from the identity provider`.
- A 0.1 replica reads every entry of `group_mappings` against the groups claim. Had the new entries gone into that
  column, an entry such as `…/owner` `example/platform/infra` → Admin would make anyone whose groups claim lists that
  group an Admin on a 0.1 replica during a rolling update. The entries of another claim or with a subtree therefore
  live only in `role_mappings`; on a 0.1 replica they match nobody until the rollout ends, which refuses or lowers some
  sign-ins for minutes but never raises a Role. The upgrade notes of 0.2.0 say so.
- The two sources of a person in a Team are kept apart, so that an identity provider never removes a person an Admin
  added (C-25.FR-4).
- A change of a Team's entries is not applied to everyone at once: membership changes at each User's next sign-in or
  re-check, as the Role mapping does (C-25.FR-4).
- Migration `0025`: the story map gave this story `0024`, but S-118, merged first, took it with
  `0024_team_label_and_matchers`, so this story renumbered its migration to the next free number, as S-131's Notes
  say.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-25.FR-1 | partial | the identity provider entries |
| C-25.FR-4 | partial | the API and the sync; the Team page is S-119 |
| C-25.FR-11 | partial | the entries' locking and Audit log |
| C-25.FR-12 | partial | the section on membership from the identity provider |
| C-25.AC-3 | full | |
| C-25.AC-4 | full | |
| C-03.FR-5 | partial | revised for L2: the claim per entry and subtrees; the editor is S-119 |
| C-03.FR-6 | full | revised for L2 |
| C-03.FR-7 | full | revised for L2 |
| C-03.FR-30 | full | revised for L2 |
| C-03.AC-28 | full | added for L2 |

# Sign-in with GitLab

This page sets up sign-in through GitLab with OpenID Connect (OIDC), on `https://gitlab.com` or on GitLab
self-managed. GitLab is an OIDC provider for its own users: a person signs in to Muster with their GitLab account, and
the groups they belong to in GitLab, and their role in them, decide their Role.

GitLab differs from [Keycloak](oidc-keycloak.md) in two ways that matter here:

- it puts group **paths** such as `platform/sre` into its claims: two groups claims, `groups` and `groups_direct`,
  and one claim per GitLab role — Owner, Maintainer and Developer;
- it does not offer the scope `offline_access`, so Muster cannot
  [re-check its users in the background](#no-offline-access).

Muster talks to GitLab over the **back channel**: it fetches the provider metadata (discovery) and the signing keys,
and exchanges the authorization code for tokens itself. Muster must therefore reach GitLab over the network, directly
or through the proxy of the OIDC settings. If it cannot, use
[local users with TOTP](oidc-keycloak.md#when-muster-cannot-reach-the-identity-provider) instead.

In the examples, GitLab self-managed runs at `https://gitlab.example.org`, and `MUSTER_PUBLIC_URL` is
`https://muster.example.org`. On `https://gitlab.com`, replace the first with `https://gitlab.com`.

## 1. Create the application

Muster is an OAuth application in GitLab. Create it where it suits you; in recent GitLab versions the pages are:

- **instance-wide**, on GitLab self-managed: **Admin → Applications → New application**;
- **for a group**: the group's **Settings → Applications → Add new application**;
- **for a user**: **Edit profile → Access → Applications → Add new application**.

Whoever owns the application, every user of that GitLab can authorize it; the Role mapping of
[step 3](#3-map-groups-to-roles) decides who gets in.

!!! warning "Every GitLab account can reach the sign-in"
    On `https://gitlab.com`, any gitlab.com account can authorize Muster's application. Keep **Role for users without
    a matching group** at **None** and map only groups of your own, so that a stranger's sign-in is refused. On GitLab
    self-managed the same applies to every user of the instance.

Fill in the form:

<!-- tablecheck: allow the names fit; the long values are URLs to copy, longer than a phone line in any layout -->

| Setting | Value |
|---|---|
| Name | `Muster` — GitLab shows it when it asks a person to authorize the application |
| Redirect URI | `https://muster.example.org/api/v1/sessions/oidc/callback` and `https://muster.example.org/api/v1/me/oidc-identity/callback`, one per line |
| Confidential | checked: Muster keeps the secret on the server |
| Trusted | optional, in the Admin area only: checked skips the screen where a person authorizes Muster at their first sign-in |
| Scopes | `openid`, `profile` and `email` |

Both redirect URIs are `MUSTER_PUBLIC_URL` followed by a path: `/api/v1/sessions/oidc/callback` ends a sign-in, and
`/api/v1/me/oidc-identity/callback` ends linking an existing account to GitLab from its profile. Register both, with
exactly the scheme, host and port of `MUSTER_PUBLIC_URL`.

Select **Save application**, then copy the **Application ID** and the **Secret**. GitLab shows the secret in full only
now; **Renew secret** makes a new one later.

## 2. Configure Muster

Sign in to Muster as an Admin and open **Access → OIDC**, or use `PUT /api/v1/oidc-settings`:

<!-- tablecheck: allow the names fit; the values are short or URLs to copy -->

| Setting | Value |
|---|---|
| Issuer URL | `https://gitlab.example.org`: the base URL of GitLab, including its relative URL path if it has one (for example `https://example.org/gitlab`), or `https://gitlab.com` |
| Client ID | the **Application ID** of step 1 |
| Client secret | the **Secret** of step 1; it is write-only: Muster shows only whether it is set and when it changed |
| Display name | the name on the button "Sign in with {name}", for example `GitLab`; by default the host of the issuer URL |
| Scopes | `profile email`; `openid` is always added, and `offline_access` is not, because GitLab does not offer it |
| Groups claim | `groups` or `groups_direct`, see [step 3](#3-map-groups-to-roles) |
| Group mapping | group paths, each of a claim, to Roles, see [step 3](#3-map-groups-to-roles) |

Every scope in the list must be one of the application's scopes: GitLab refuses a sign-in that asks for any other
scope with `invalid_scope`, so never add `offline_access` to the list for GitLab. The **Role for users without a
matching group** stays **None**, as the warning in [step 1](#1-create-the-application) says. The other settings — Role
sync, skipping TOTP and the proxy — work as on the [Keycloak page](oidc-keycloak.md#4-configure-muster).

## 3. Map groups to Roles

GitLab names each group by its full path, such as `platform` or `platform/sre`, and offers these claims:

`groups`
:   The paths of every group the person is a member of, directly or through an ancestor group: a member of
    `platform` is listed with `platform` and with each of its subgroups, such as `platform/sre`, but a member of
    `platform/sre` alone is not listed with `platform`: the groups above a person's own never appear. GitLab puts it
    in the userinfo answer, not in the ID token, and Muster reads it from there.

`groups_direct`
:   The paths of the groups the person is a direct member of, without the subgroups they reach through an ancestor.
    GitLab puts it in the ID token.

`https://gitlab.org/claims/groups/owner`, `…/maintainer` and `…/developer`
:   The role claims: each lists the groups where the person holds that GitLab role **directly** — never the subgroups
    they reach through an ancestor — and a person has only the claims of the roles they hold, so an Owner of
    `platform` has the owner claim `["platform"]` and no maintainer claim. GitLab lists all three in discovery and puts
    them in the userinfo answer, where Muster reads them.

Every entry of the mapping names a group and, optionally, the claim it reads; without a claim it reads the **Groups
claim** of the settings. A group is matched exactly, letter case included, so copy it from the group's URL, except a
**subtree** `a/b/*`, which matches `a/b` and every group below it at `/` boundaries: `platform/*` matches `platform`
and `platform/sre`, never `platformx`. There are no other wildcards. When the entries map a person to several Roles,
the highest wins.

For Admin, map the owner claim to your top group **exactly**. For example, to make the Owners of the group `corp`
Admins and everyone else in it a Responder:

`groups` `corp/*`
:   Responder: every member of `corp` or of any group below it. `groups` lists the groups a person is a member of
    and those below them, never the groups above, so a Developer of `corp/team-a` alone has `groups`
    `["corp/team-a"]`, which `corp/*` matches.

`https://gitlab.org/claims/groups/owner` `corp`
:   Admin: the people who hold Owner on `corp` itself.

A Developer of `corp/team-a` alone is a Responder, and a person outside `corp` matches no entry and is refused while
**Role for users without a matching group** is **None**.

!!! danger "A subtree entry of a role claim also matches the Owners of new subgroups"
    A role claim lists the groups where the person holds the role directly, and GitLab makes whoever creates a
    subgroup its direct Owner. A subtree entry such as the owner claim `corp/*` → Admin therefore makes an Admin of
    everyone allowed to create a subgroup below `corp` — by default its Maintainers too — once they create one. Map
    the owner claim, and the other role claims, to exact groups for Roles; if you do use a subtree entry, restrict
    **Roles allowed to create subgroups** in the settings of the top group (**Settings → General → Permissions and
    group features**) to Owners. An exact entry `corp/sre` does not match an Owner of `corp`, who holds that role in
    `corp/sre` only through its parent: add an entry for each group whose Owners should have the Role.

    Subtree entries are harmless in the identity provider entries of a Team: a Team grants no Permission and no Role
    ([Teams](../concepts/teams.md#membership-from-the-identity-provider)).

The OIDC page of Muster shows the claim of each entry under its group, read-only, by a short name such as `owner`
with the full claim in a tooltip, and keeps it when you save there; to add or change an entry of another claim, use
`PUT /api/v1/oidc-settings`:

```json
"group_mappings": [
  {"group": "corp/*", "role": "responder"},
  {"claim": "https://gitlab.org/claims/groups/owner", "group": "corp", "role": "admin"}
]
```

With only the groups claims, choose `groups` when membership of a parent group should count for its subgroups, as it
does in GitLab itself, and `groups_direct` when a Role should follow only the groups a person was added to by name.
For example, with the groups claim `groups`:

| IdP group | Role |
|---|---|
| `platform/muster-admins` | Admin |
| `platform/sre` | Responder |
| `platform` | Viewer |

Every member of `platform` is a Viewer at least, and also a member of `platform/sre` through it, so a Responder. To keep
`platform` members Viewers, use `groups_direct`: then only the people added to `platform/sre` itself are Responders.

Teams take their people from GitLab the same way: see
[Membership from the identity provider](../concepts/teams.md#membership-from-the-identity-provider).

## 4. Check the connection

Press **Check connection**. The check fetches discovery through the saved settings and proxy and shows how the
request travelled, how long it took, the endpoints GitLab announced, the **Requested scopes** of a sign-in
(`openid profile email` with the scopes above) and that **Offline access** is not offered. That is expected for GitLab
and is not a problem: it only means the rules of [No offline access](#no-offline-access) apply. The check also keeps
the claims GitLab lists, the role claims among them, and warns about each claim of the mapping that GitLab does not
list, until a sign-in carries it.

## 5. Sign in

Open Muster and press **Sign in with {display name}**. GitLab asks the person to authorize Muster once, unless the
application is trusted, and the first sign-in creates their Muster user with the Role of their groups. The login is
GitLab's username (`preferred_username`). As with any provider, Muster never merges accounts by login or email, and
a person whom no entry matches is refused; the [Keycloak page](oidc-keycloak.md#5-sign-in) lists the errors of
the sign-in page.

## No offline access

GitLab's discovery does not list `offline_access` in `scopes_supported`, so Muster does not request it. GitLab still
returns a refresh token, but without `offline_access` Muster does not keep it. Sign-in works, and for every person who
signs in through GitLab:

- there are no background re-checks;
- each OIDC session ends `auth.oidc_fallback_session_lifetime` (12 hours) after sign-in, even while in use, so that a
  person blocked or removed in GitLab is signed out within that time;
- their Personal access tokens work only while their last OIDC sign-in is within `auth.oidc_token_grace` (7 days);
  after that, the tokens are refused until they sign in through GitLab again.

The [Keycloak page](oidc-keycloak.md#without-offline_access) describes the same rules; see also
[Ways to sign in](index.md#ways-to-sign-in).

## Troubleshooting

`idp_error` with `invalid_scope` in the log event `oidc_sign_in_failed`
:   GitLab refused a scope Muster asked for: a scope in Muster's **Scopes** that the application does not have, or
    `offline_access` typed into that list. Remove it from the list, or add it to the application in GitLab.

The Role is not the one you expect
:   Check the claim of each entry: with `groups_direct`, membership through a parent group does not count, and a role
    claim lists only the groups where the role is held directly, so an exact entry of a subgroup does not match an
    Owner of its parent: add an exact entry for the parent rather than a subtree entry, which would also match the
    Owner of any new subgroup (see [step 3](#3-map-groups-to-roles)). The Audit log entry `session.oidc_refused` of a
    refused sign-in lists the values GitLab sent for every claim the mapping read.

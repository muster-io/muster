---
id: S-220
title: The theme of a person in the profile, with its migration and API (BE)
capability: C-39
kind: be
layer: L2
depends_on: [S-010]
covers: [C-39.FR-1, C-39.FR-2, C-39.FR-6, C-39.AC-1]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/00NN_user_theme.up.sql
  - internal/db/migrations/00NN_user_theme.down.sql
  - design/db/schema.md
  - internal/db/migrate_test.go
  - internal/users/users.go
  - internal/users/users_test.go
  - internal/users/query.sql
  - internal/users/admin_integration_test.go
  - internal/api/profile.go
  - internal/api/users_test.go
  - test/e2e/profile_theme_test.go
acceptance:
  - "[C-39.FR-1, C-39.AC-1] `GET /me` of a User who never chose gives `theme` `system`; `PUT /me` with `dark` stores it and a later `GET /me` from another session of the same User gives `dark`; `PUT /me` without `theme` keeps it, and a `PUT /me` that only changes the name keeps the theme as it keeps the time zone and the language an omitted field keeps; `sepia` gets `400` `invalid_format` from request validation (`users_test.go` of the package, `internal/api/users_test.go`, `test/e2e/profile_theme_test.go`)."
  - "[C-39.FR-2, C-39.AC-1] `theme` is at the top level of `Me`, not on `User`: `listUsers` and `getUser`, which Admins read, carry no `theme`; the change is made only by the User from a web session, so that a Personal access token gets `403` `session_required` on `PUT /me`, and an Admin's `PUT /users/{id}` cannot set it (`internal/api/users_test.go`, `test/e2e/profile_theme_test.go`)."
  - "[C-39.FR-2] The change is recorded in the Audit log as `user.profile_updated` with the diff `/theme` before and after, and a change of the theme alone triggers no re-render of Root messages (`users_test.go` of the package, `admin_integration_test.go`)."
  - "[C-39.FR-6] A Service account has no profile and no theme: its tokens get `403` (`service_account_not_allowed`) on `getMe` and `updateMe` as before, and no operation of Service accounts mentions `theme` (`internal/api/users_test.go`)."
  - "[C-39.FR-1] After the migration every existing User reads `system`, and a column default of `system` is what a replica of the earlier version, which never writes it, leaves (`migrate_test.go`)."
verify: "make ci test-integration e2e-go"
operator_attention: false
issue: null
---

# S-220. The theme of a person in the profile, with its migration and API (BE)

## Scope

**IN**

- `theme` (`system`, `light`, `dark`) on the profile: the column, `getMe` and `updateMe`, the Audit log diff
  (C-39.FR-1, FR-2, FR-6).

**OUT**

- The switch, the application of the theme before the first paint, the dark variants and the documentation: S-221.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated): a schema `Theme`, an enum `system`, `light`, `dark`;
  `Me` gains `theme` (required, in every read, at the top level beside `user` and `permissions` — not on `User`, which
  the list of Users also returns to Admins, C-39 API surface); `MeUpdate` gains an optional `theme` (an update that
  omits it keeps it; an unknown value is refused by request validation with `400` `invalid_format`). `updateMe`
  keeps `sessionCookie` and `csrf` as its only security, so that a token is refused as C-03.FR-27 says.
- **Schema** (a new migration, expand only; the number is assigned by the orchestrator at dispatch; `schema.md` §4.3):
  `users.theme text NOT NULL DEFAULT 'system'` with a CHECK of the three values. No CHECK replaces another and
  nothing is
  backfilled; a replica of the earlier version neither reads nor writes the column, so that a profile it saves keeps
  the theme already there. The down migration drops the column.
- **Package** (`internal/users`): `Profile` gains `Theme`; `UpdateProfile` writes it with the name, time zone and
  language in the same statement of `query.sql`, takes an omitted value as the stored one, adds `/theme` to the Audit
  log diff of `user.profile_updated`, and never inserts the re-render row of a name change for a theme change alone.
  `Validate` refuses a value outside the three, defensively, behind request validation.
- **API handlers** (`internal/api/profile.go`): `GetMe` and `UpdateMe` map `theme` in `gen.Me`; the Admin's user
  operations (`internal/api/users.go`) do not touch it.
- **Settings and registries**: no new setting, Permission, metric, log event or Internal alert.

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up; `migrate_test.go` finds
   every existing User with `system` and the CHECK refusing `sepia`.
2. Add `Theme` to the profile, its query, validation and diff. Check: `users_test.go`, `admin_integration_test.go`.
3. Add the spec and the handlers. Check: `make generate-check lint`; `internal/api/users_test.go` (the omitted field,
   the
   unknown value, the token's `403`, `Me` against `User`).
4. Write `test/e2e/profile_theme_test.go`. Check: the Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
API=localhost:8080/api/v1   # S: the Admin's session cookie jar and CSRF header, as in S-010; T: a Personal access token
curl -s -b jar $API/me | jq -c '{theme, userTheme: .user.theme}'                                  # {"theme":"system","userTheme":null}
curl -s -b jar -H "$CSRF" -X PUT $API/me -d '{"name":"Dana Lee","theme":"dark"}' | jq -c .theme  # "dark"
# another session of the same User (signed in again as in S-010, into jar2)
curl -s -b jar2 $API/me | jq -r .theme                                                             # dark
# omitted keeps it
curl -s -b jar -H "$CSRF" -X PUT $API/me -d '{"name":"Dana Lee"}' | jq -r .theme                  # dark
# unknown is 400, a token is 403
curl -s -b jar -H "$CSRF" -X PUT $API/me -d '{"name":"Dana Lee","theme":"sepia"}' | jq -c '{status, code: .errors[0].code}'   # {"status":400,"code":"invalid_format"}
curl -s -H "Authorization: Bearer $T" -X PUT $API/me -d '{"name":"x","theme":"light"}' | jq -c '{status, code}'               # 403 session_required
# Admins' reads of Users carry no theme
curl -s -b jar "$API/users?limit=1" | jq -c '.items[0] | has("theme")'                              # false
# the Audit log
curl -s -b jar "$API/audit-log?limit=1" | jq -c '.items[0] | {action, p: [.diff[].pointer]}'        # {"action":"user.profile_updated","p":["/theme"]}
psql "$MUSTER_DB" -c "select login, theme from users order by id"                                  # the table agrees with the API
# a Service account's token on /me
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $SA" $API/me                    # 403
```

`test/e2e/profile_theme_test.go` repeats these and checks, before the migration case in `migrate_test.go`, that a User
who existed before it reads `system`.

## Open questions

None.

## Notes

- Suggested commit: `feat(users): add the theme to the profile of a person`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. The migration number is
  assigned by the orchestrator at dispatch; the migration replaces no CHECK and needs no re-versioning.
- The theme is the person's preference and nothing of the Organization's: no Organization setting, no default other than
  `system`, and no Admin operation that sets it.
- A User of an identity provider has the same column; the sign-in by OIDC reads it in the profile like any other
  field.
- Until S-221 merges the field is stored and returned and the UI ignores it, which is true to the order of delivery.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-39.FR-1 | partial | the value, its default and `system` for the existing Users; what the UI does with it is S-221 |
| C-39.FR-2 | partial | the profile setting, the web-session rule and the Audit log; the immediate application in the tab is S-221 |
| C-39.FR-6 | full | |
| C-39.AC-1 | full | |

# MusterLoginFailures

## Meaning

A burst of failed sign-ins: more than 20 for one method within 10 minutes. The chart rule is:

```promql
sum by (method) (increase(muster_login_failures_total{job="muster"}[10m])) > 20
```

It fires at once (`for: 0m`), with severity `warning` and the label `method`: `local` for a wrong login or password,
`totp` for a wrong TOTP or recovery code, `oidc` for a refused OIDC sign-in. It is a chart rule only. Attempts that
the sign-in throttle refuses before evaluating them are not counted.

## Impact

- Someone may be guessing passwords or codes. The sign-in throttle (`auth.signin_throttle`) slows them down: after 3
  consecutive failures per account or source address, each attempt waits twice as long as the previous one, up to 60
  seconds.
- Legitimate users of the same account or address wait longer between attempts.
- With `oidc`, people may be unable to sign in at all because the identity provider or its settings fail.

## Diagnosis

1. See the method: `sum by (method) (increase(muster_login_failures_total{job="muster"}[10m]))`.
2. For `local` and `totp`, open the **Audit log** (Organization → **Audit log**, `/admin/audit-log`) and look for the
   actions `session.sign_in_failed` and `session.second_factor_failed`: each names the account, when it is known, and
   the source address.
3. For `oidc`, read the log lines `oidc_sign_in_failed` (`reason`, `error`): the identity provider reported an error,
   could not be reached, or answered with an ID token that did not verify. A clock that is off also makes ID tokens
   fail; see [MusterClockSkew](MusterClockSkew.md).
4. Tell an attack from a mistake: many accounts from few addresses look like guessing; one account from its usual
   address looks like a forgotten password or a script with an old one.

## Fix

- Guessing from outside: block the source addresses at the ingress or the firewall, and restrict who may reach the
  UI.
- A forgotten password or a lost authenticator: on the user's page (`/admin/users/<id>`), create a **New password
  setup link** or **Reset TOTP**. Without an Admin who can sign in, use the emergency commands
  `muster admin reset-password --actor <name> <login>` and `muster admin reset-totp --actor <name> <login>`.
- A script or a tool with old credentials: give it a Personal access token or an API token of a Service account
  instead of a password.
- `oidc` failures: fix the identity provider, or the **OIDC settings** (`/admin/oidc`) and their **Check
  connection**; see [Sign-in with Keycloak](../../sign-in/oidc-keycloak.md). Local sign-in stays available meanwhile.

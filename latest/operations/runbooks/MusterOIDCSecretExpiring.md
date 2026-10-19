# MusterOIDCSecretExpiring

## Meaning

The OIDC client secret expires soon: the date entered as **Client secret expires on** in the OIDC settings is within
`oidc.secret_expiry_lead` (14 days).

It is an **Internal alert** only, severity `warning`, with no labels besides `alertname` and `severity`. The Leader
checks the date every 5 minutes and raises it as an Alert of the built-in "Muster" Integration; it is resolved when
the date is changed to a later one or cleared.

## Impact

- Nothing yet. Once the secret expires, the identity provider refuses Muster's back-channel requests, and OIDC
  sign-in fails for everyone.
- Local sign-in keeps working, so an Admin with a local password can still fix the settings.

## Diagnosis

1. Open the **OIDC settings** (`/admin/oidc`): the banner says "The client secret expires on {date}. Issue a new one in
   the identity provider and enter it here."
2. The log line `oidc_secret_expiring` (`expires_on`) is written once per raise.
3. In the identity provider, look up the client's secret and its real expiry date; the date in Muster is only what
   someone entered.

## Fix

1. Issue a new client secret in the identity provider, for Keycloak under the client's **Credentials**; see
   [Sign-in with Keycloak](../../sign-in/oidc-keycloak.md).
2. Enter the new secret in the OIDC settings, set **Client secret expires on** to its new expiry date, or clear it if
   the new secret does not expire, and save. With the API, `PUT /api/v1/oidc-settings` takes the fields
   `client_secret` and `client_secret_expires_on`.
3. Press **Check connection**, then sign in once through OIDC to confirm.

The alert resolves within 5 minutes, at the Leader's next check.

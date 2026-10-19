# Closed networks

OpenID Connect needs a **back channel**: Muster itself calls the identity provider, to read its configuration and keys,
to exchange the code for tokens, and to check the sessions again later. Where Muster is outside the network of the
identity provider and cannot reach it, OpenID Connect cannot work, even though the people's browsers can. Typical cases
are an identity provider inside a company network and Muster in a cloud or a different site.

If a proxy can bring Muster to the provider, set it in the OIDC settings first: see [Proxies](../messengers/proxies.md).
When it cannot, use **local users with TOTP**. The [Keycloak page](oidc-keycloak.md#when-muster-cannot-reach-the-identity-provider)
describes the symptoms of an unreachable provider and does not repeat them here.

## Set it up

1. Sign in as the bootstrap Admin, whose email and password come from `MUSTER_BOOTSTRAP_ADMIN_EMAIL` and its password
   variable at the first start.
2. Under **Access** → **Users**, create a user for each person, with a login and a Role.
3. Hand each person the one-time setup link that Muster shows. It is valid for 24 hours and works once. Muster sends no
   email, so you pass it on yourself. The person sets a password of at least 12 characters.
4. Under Organization → Security, set **TOTP required** to **local users**, or to **everyone**. With the policy, the
   person enrols [TOTP](totp.md) right after the first sign-in and saves the 10 recovery codes. Without it, the policy
   would only be a wish: enrolment would be each person's choice.
5. Add [Personal access tokens](index.md#ways-to-sign-in) for scripts and Service accounts for automation. They do not
   depend on the identity provider.

Take care of the way back: keep at least two Admins, and keep the recovery codes of each. If the last Admin loses the
second factor, `muster admin reset-totp --actor <name> <login>` resets it from the command line, and
`muster admin reset-password --actor <name> <login>` sets a new password.

Local sign-in and OpenID Connect can run side by side. If the provider becomes reachable later, configure OIDC and let
people link it from their profile; their TOTP stays.

# Sign-in

Everything in the web UI and the API works only after sign-in. No page and no read of the API is open to someone who has
not signed in. What is open is a short list of endpoints that cannot ask for a session:

- the sign-in itself: the list of sign-in methods, the password sign-in, the OpenID Connect redirect and its callback,
  and the page where a person sets a password from a setup link;
- [ingestion](../integrations/alertmanager.md) and the [Heartbeat](../integrations/heartbeat.md), which take an
  Integration token;
- the callbacks that Mattermost, Telegram and a [Bridge](../messengers/bridge.md) send to Muster, which each carry a
  secret of their own;
- the served API specification;
- the health checks and `/metrics`, on the internal listener, which is not meant to be reachable from outside the
  cluster.

Everything else answers `401` without a session or a token.

## Roles

Each User and Service account has one of three Roles, each a fixed set of Permissions:

| Role | May |
|---|---|
| Admin | everything: also configures Integrations, Routes, Connections and Destinations, manages Users, the OIDC settings, Service accounts, the Organization's settings, and reads the Audit log and the System status |
| Responder | reads everything that concerns Alert Groups, and acts on them: acknowledges, resolves, snoozes and adds Notes |
| Viewer | reads Alert Groups, Alerts, and the configuration, with secret values masked |

Every signed-in person also manages their own profile: password, TOTP, Account links and Personal access tokens. Every operation in the [API reference](../reference/api.md) names the Permission it needs.

## Ways to sign in

- **Local users.** An Admin creates a user with a login, and Muster returns a single-use link, valid for 24 hours, where
  the person sets a password. Muster sends no email; the Admin hands the link over. The first Admin comes from
  `MUSTER_BOOTSTRAP_ADMIN_EMAIL` and its password at the first start, as the [Quick start](../quick-start.md) shows.
- **OpenID Connect.** People sign in at your identity provider, and a mapping from the groups in their token to Roles
  decides what they may do. [Sign-in with Keycloak](oidc-keycloak.md) is a complete walkthrough.
- **TOTP.** A second step with a one-time code from an authenticator app, for local and OIDC users alike; the
  Organization can require it. See [TOTP](totp.md).
- **Personal access tokens.** A person creates them in the profile for scripts. A token acts as its owner, with at most
  the owner's Permissions, and can be narrowed to fewer. It can expire, and the Audit log names the token with the
  person.
- **Service accounts.** An identity that is not a person, for automation such as a pipeline. It has a Role and any number
  of tokens, cannot sign in to the web UI, and cannot become the Owner of an Alert Group.

Operations that change one's own account, such as the password, TOTP, tokens and Account links, need the web session; a
token is refused there.

When the identity provider is in a network that Muster cannot reach, see [Closed networks](closed-networks.md).

## Link your messenger accounts

A button in Mattermost or Telegram acts as a Muster User only if the person who presses it has linked their messenger
account to their User. People do it themselves, from their profile, as [Link your messenger
accounts](../messengers/account-links.md) describes. Admins cannot do it for them: they see the links of every User and
can remove them, but they cannot create one for someone else. A press from an unlinked account changes nothing, and the
person is answered privately with a link to their profile.

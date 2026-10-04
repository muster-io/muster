# 0013. Account links only from a signed-in web session

- Status: Accepted
- Date: 2026-10-02
- Amended: 2026-10-04 — after a live Mattermost test ([verified facts](../facts.md#mattermost)): how presses fail
  when Muster's address is not allowed, and where answers to presses appear

## Context

People act on Alert Groups from messengers: they press Acknowledge in Mattermost or Telegram. Muster must attribute each
press to a Muster User — for permissions, ownership and the Audit log — so every messenger account needs an Account link
to exactly one User.

The dangerous failure is not a link that fails but a wrong one: an attacker's messenger account linked to a victim's
User lets the attacker acknowledge and resolve as the victim. Schemes in which a link starts in chat and is confirmed in
the web ("open this link to connect your chat account") can be phished by construction: the victim confirms someone
else's chat account with one click. RFC 8628, section 5.4, describes the same risk for device flows, and confirmation
codes do not remove it. Matching by email is only as good as the messenger's email verification; Mattermost does not
require verified addresses by default, and its API does not tell a bot whether an address was verified.

Facts the design relies on: a Mattermost button press arrives as a POST from the Mattermost server with a trustworthy
`user_id`, because the server removes the button's integration data before sending messages to clients; Mattermost does
not sign these requests. A Telegram user id is the same for every bot. A Telegram deep link passes up to 64 characters
to the bot as `/start <parameter>`.

## Decision

**Every Account link starts in the user's signed-in Muster web session**, and the secret travels from that session to
the owner of the messenger account — never the other way round.

- **Telegram.** The profile shows a link `t.me/<bot>?start=<token>`. The token is 32 random bytes in base64url
  (43 characters), single-use, valid for 10 minutes, stored as a hash and bound to the User and the Connection. When the
  Connection's bot receives `/start <token>`, it links that Telegram account and replies with the name and login of the
  Muster User it is now linked to, so a wrong link is noticed at once; the profile shows the new link immediately.
- **Mattermost.** The user enters their `@username` in the profile; Muster finds it through the API, and the
  Connection's bot sends a direct message with a code, saying that it was requested from Muster and must not be shared.
  The user types the code into the profile. The code has 8 characters with no look-alike characters, is valid for
  10 minutes and allows at most 5 attempts. A typo in the username is harmless, because the code is useless without the
  requester's web session.

**Rules.**

1. A messenger account is linked to exactly one User. A User has at most one account per identity space (rule 5) —
   one Telegram account in all, one account per Mattermost Connection; a new link replaces the old one, and the
   replacement is audited.
2. If the account is already linked to another User, the link is refused and the refusal says that its owner or an
   admin can unlink it; nothing is transferred silently.
3. Unlinking is done by the User in the profile or by an admin, never from chat. An admin can see and remove anyone's
   links but cannot create a link on someone else's behalf.
4. Linking asks for no extra re-authentication or TOTP; the session is already signed in, and TOTP is checked at
   sign-in.
5. Identity spaces: Telegram links are global, so one Telegram account works through any Muster bot; Mattermost links
   belong to one Connection.
6. Presses by a disabled User are refused; deleting a User removes their links. Deactivation inside Mattermost is not
   tracked in the first release.
7. The Audit log records `link`, `unlink` (by whom, from where) and `link_rejected` (conflict, expired token, wrong
   code), with the User, the messenger, the external id and username, and the Connection.

**Presses from unlinked accounts** do nothing except answer privately — in Mattermost with an ephemeral message, in
Telegram with `answerCallbackQuery` — that the account is not linked, with a link to the Muster profile. The link
carries no secret. A Viewer is told that the action is not permitted. Telegram presses older than one hour, for example
queued while Muster was down, are dropped. These answers and the bot's messages during linking are sent through the
interactive path, not the delivery queue (ADR-0005).

**Mattermost only through its REST API with a bot account.** Each Mattermost Connection uses the bot account's token for
posting, editing, direct messages and ephemeral replies. Muster does not use Mattermost incoming webhooks and does not
ship a Mattermost plugin. Button presses reach Muster at `MUSTER_INGEST_URL`, and each button carries only an opaque,
signed action id (ADR-0011). If Muster has an internal address, it must be listed in Mattermost's
`AllowedUntrustedInternalConnections`, because Mattermost calls integrations with its untrusted HTTP client; otherwise a
press shows the person only a generic "Action integration error", and only the Mattermost server log says why. The
documentation says so, and the Connection page reminds the admin. Muster answers a press with an empty JSON object and
sends anything the person must read as a separate ephemeral post: an ephemeral text in the answer itself would appear
only inside the Thread of the Root message, out of sight in the channel.

**Not in the first release:** linking through Mattermost OAuth or Telegram Login.

## Consequences

- An attacker cannot get a chat account linked to someone else's User without that person's web session.
- Each person visits their Muster profile once per messenger; nobody is linked automatically, even when email addresses
  match.
- An admin cannot prepare links for a team; everyone links their own account.
- Muster sends no email, so codes travel as messenger direct messages; the Mattermost bot must be allowed to message
  users directly.
- Mattermost links are per Connection: two Connections to the same Mattermost server need two links, because Muster
  cannot reliably tell that they point at the same server.

## Alternatives considered

- **Start in chat, confirm in the web**: an unlinked press shows a one-time link that a signed-in user confirms.
  Phishable by construction.
- **Confirm in chat**: after a username is entered in the profile, the bot sends that user a "Confirm" button. A typo in
  the username, or a stranger who presses "Confirm", gives the requester's rights to the wrong account.
- **Automatic linking by email.** Mattermost addresses are not necessarily verified, and the API does not say whether
  they are.
- **Slash commands for linking.** Need setup on every Mattermost server and still start the link in chat.
- **Mattermost OAuth or Telegram Login.** Strong — both identities are proven in one browser — but they need extra setup
  on every messenger server and are not needed for the first release; they may come later as an option.
- **A Mattermost plugin.** Another artifact to build, sign and install on every server; the REST API with a bot account
  covers what Muster needs.
- **Mattermost incoming webhooks.** They can only create messages, not edit them.

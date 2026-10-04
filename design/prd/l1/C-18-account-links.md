# C-18. Account links

[L1 index](../L1.md) · Stage: Act · UI: yes · Depends on: C-03, C-13, C-14, C-17

**Goal.** Every button press in a messenger is attributed to exactly one Muster User, through links that cannot be
phished.

Names in the form `area.setting` refer to rows of [defaults.md](defaults.md).

## Scenarios

1. Alice opens her profile, clicks "Link Telegram", follows the `t.me` link and presses Start; the bot replies "Linked
   to Muster user Alice Smith (alice)", and the profile shows the link.
2. Bob enters `@bob` for the Mattermost Connection "corp"; the bot sends him a direct message with a code; he types it
   into the profile.
3. Carol presses Ack before linking; she privately gets "Your Mattermost account is not linked to Muster. Link it in
   your profile: {link}".
4. Dave tries to link an account already linked to Erin; he is refused with "This account is linked to another user. Its
   owner or an admin can unlink it."
5. An Admin removes a link of a user who changed teams.

## Functional requirements

- **C-18.FR-1** Every Account link starts in the user's signed-in web session; the secret travels from that session to
  the messenger account, never the other way (ADR-0013). Starting, confirming and removing one's own link accept the web
  session only (C-03.FR-27).
- **C-18.FR-2** Telegram: the profile shows a link `t.me/<bot>?start=<token>` for a chosen Telegram Connection. The
  token is 32 random bytes in base64url (43 characters), single-use, valid for `account_link.telegram_token_ttl`, stored
  as a hash and bound to the User and the Connection. On `/start <token>` the bot links that Telegram account and replies
  with the User's display name and login; the profile shows the link without reloading. A `/start` whose token is
  unknown, used, expired or bound to another Connection links nothing, and the bot replies "This link is invalid or
  expired. Start again from your Muster profile." ([reference.md](reference.md#banners-warnings-and-notices)).
- **C-18.FR-3** Mattermost: the user enters an `@username` for a chosen Mattermost Connection; Muster finds it through
  the API and the bot sends a direct message, which a bot can do knowing only the user's id
  ([F-028](../../facts.md#threads-and-direct-messages)), with a code and the warning "You requested a link to Muster. Do
  not share this code." The code follows `account_link.mattermost_code` (length without look-alike characters, validity,
  attempts); code requests are limited by `account_link.mattermost_code_requests`, per requesting User and per target
  account. After the code is entered, the bot confirms "Linked to Muster user {name}".
- **C-18.FR-4** A messenger account is linked to exactly one User. A User has at most one account per identity space —
  one Telegram account in all, one account per Mattermost Connection; a new link replaces the previous one.
- **C-18.FR-5** Linking an account already linked to another User is refused; nothing moves silently.
- **C-18.FR-6** Users unlink their own accounts in the profile; Admins see and remove any User's links but cannot create
  a link for someone else. Nothing can be unlinked from chat.
- **C-18.FR-7** Linking needs no re-authentication or TOTP beyond the signed-in session.
- **C-18.FR-8** Presses from linked accounts run the command as the linked User with the Transport of the messenger.
  Presses from unlinked accounts change nothing and are answered privately with a link to the profile that carries no
  secret (C-13.FR-4, C-14.FR-5); presses by Viewers get "You are not permitted to do this"; presses by disabled Users get
  "Your Muster account is disabled". Deleting a User removes their links.
- **C-18.FR-9** The Audit log records `link`, `unlink` (by whom, from where) and `link_rejected` (conflict, expired
  token, wrong code) with the User, the messenger, the external id and username, and the Connection.
- **C-18.FR-10** The profile lists links with the messenger, Connection, username and last use. It picks the
  Connection to link through from its own short list — only the id, the name and the messenger of each Connection that
  is not deleted — which needs no Permission, independently of `connections:read`.
- **C-18.FR-11** Bot messages during linking go through the interactive path.

## UI

Profile → Messenger accounts (link Telegram, link Mattermost per Connection, code entry, list, unlink); Users → user page
→ Account links (list, remove).

## API surface

`me/account-links` (list, delete); `me/account-links/connections` (list of the Connections to link through: id, name,
messenger); `me/account-links/telegram` (create → deep link); `me/account-links/mattermost` (create → code sent; confirm
with code); `users/{id}/account-links` (list, delete).

## Acceptance

Checked against the fake Mattermost and Telegram servers.

- **C-18.AC-1** A Telegram token works once; the second `/start` with it is rejected, answered with "This link is
  invalid or expired. Start again from your Muster profile." and recorded as `link_rejected`.
- **C-18.AC-2** After five wrong Mattermost codes, the sixth attempt is refused even with the correct code, and the code
  is void.
- **C-18.AC-3** After linking, a press from that account runs the command as that User with the Transport of the
  messenger.
- **C-18.AC-4** No API operation creates a link for another User: the starting operations act on the signed-in User
  only, and with an API token — an Admin's included — they are refused (`403`, `session_required`); the account links
  of a User (`users/{id}/account-links`) can be listed and deleted, and a `POST` there is not allowed (`405`).
- **C-18.AC-5** After linking, pressing "Still on it" on a Reminder records the answer for that User, and pressing
  "Unack" unacknowledges the Alert Group.
- **C-18.AC-6** A Viewer's press and a disabled User's press change nothing and get their refusal texts.
- **C-18.AC-7** After Alice links the Telegram account `@alice_t` and no Mattermost account, an Alert Group she
  acknowledged shows "Acknowledged by @alice_t" in the Telegram Root message and her display name in the Mattermost one,
  and a Destination that mentions her for new Alert Groups mentions `@alice_t` in Telegram (C-12.FR-12).

## Related ADRs

ADR-0005, ADR-0013.

## Depends on

C-03 — Users and the profile; C-13 and C-14 — the bots and callbacks; C-17 — Reminder buttons.

## Suggested story split

- **BE** — Telegram deep links, Mattermost codes and their limits, linking rules, press attribution, names in footers
  and Mentions, Audit log.
- **FE** — Messenger accounts in the profile, Account links on the user page.

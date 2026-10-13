# Link your messenger accounts

An **Account link** ties your Telegram account, or your account on a Mattermost server, to your Muster User. With it,
a button you press under an Alert Group in a messenger runs as you — the Timeline names you and shows the messenger
you pressed in — and Mentions and footers in the messenger name you.

A Telegram account belongs to one User across every Telegram Connection. A Mattermost account belongs to one User per
Mattermost Connection, since each Connection is its own server. You have at most one Telegram account and one account
per Mattermost Connection; linking another one replaces the earlier link, and linking the same account again keeps
its link and updates its username.

## Why links start in your profile

Every link starts in your signed-in Muster session, and the secret travels from there to your messenger account —
never the other way. Nobody can link your account by typing your name into a chat, and nobody can link their account
to your User: only the account that receives the secret Muster gave you in your own session gets linked. That is why
linking needs no extra password or TOTP code, and why an API token, an Admin's included, cannot start, confirm or
remove a link of its own User.

## Link Telegram

1. In your profile, open **Messenger accounts**, choose the Telegram Connection and select **Link Telegram** — or
   **Relink Telegram** once you have a Telegram link, which the new link replaces.
2. Muster shows a link `https://t.me/<bot>?start=<token>`. Open it on the device where you use Telegram, within 10
   minutes; the link works once.
3. Telegram opens a chat with the bot. Press **Start**.
4. The bot answers "Linked to Muster user {your name} ({your login})", and your profile shows the link at once.

If the bot answers "This link is invalid or expired. Start again from your Muster profile.", the link was used
already, expired, belongs to another Telegram bot of Muster, or was never issued: start again from your profile. If it
answers "This account is linked to another user. Its owner or an admin can unlink it.", that Telegram account is
linked to someone else; nothing changed.

## Link Mattermost

1. In your profile, open **Messenger accounts**, choose the Mattermost Connection, enter your Mattermost username —
   with or without the `@` — and select **Send code**.
2. The Connection's bot sends you a direct message: "You requested a link to Muster. Do not share this code." with an
   8-character code. If you did not ask for it, ignore it.
3. Type the code into your profile within 10 minutes; capital and small letters are the same. The bot confirms
   "Linked to Muster user {your name}".

A code allows 5 attempts; after five wrong codes it stops working, even with the right code, and you request a new one.
A new request replaces your earlier one. You can request 5 codes an hour, and one Mattermost account gets at most 3
codes an hour, whoever asks; beyond that, wait until the oldest request is an hour old. A username the server does not
know, a bot account and a deactivated account cannot be linked. An account that is linked to another User is refused.

The bot must be allowed to open direct messages with you; see [Direct messages](mattermost.md#direct-messages).

## Several Connections of one messenger

An Organization may have several Connections of one messenger, such as two Mattermost bots on one server or a second
Telegram bot. What a link covers differs by messenger:

- **Mattermost links are per Connection.** Each Mattermost Connection is its own Identity space, even when two
  Connections use bots on the same server: two bots on one server need two links, made one after the other from
  **Link Mattermost** with each Connection. Your profile lists one row per Connection.
- **A Telegram link is valid for all Telegram bots.** Telegram has one Identity space, so the link you make through any
  Telegram Connection lets you press buttons sent by every Telegram bot of Muster. Your profile shows it as
  "Telegram · @<username> · for all Telegram bots", with "linked via <Connection>" below.

One chat reached through two Connections is two Destinations, and each posts its own message for every Alert Group.
This is allowed, but the Route editor warns when two or more of a Route's Destinations deliver to the same chat — one
Telegram chat, or one Mattermost server and channel — since their messages appear twice. The Destinations list, the
Route editor and the Alert Group page name each Destination's Connection and channel, so you can tell them apart.

## Unlink

In your profile, under **Messenger accounts**, remove the link. Nothing can be unlinked from a chat. Removing a link,
like making one, is recorded in the Audit log with who did it and from where.

## What Admins can do

Admins see the links of every User on the user page, under **Account links**, and can remove any of them — their own
from the web session only, as above. They cannot create a link for someone else. Deleting a User removes their links and voids the links they had started.

## What a press answers

| Who pressed | What happens | The private answer |
|---|---|---|
| An account without a link | Nothing changes | "Your Telegram account is not linked to Muster. Link it in your profile: {link}" — or the same for Mattermost; the link goes to your profile and carries no secret |
| A linked Viewer | Nothing changes | "You are not permitted to do this" |
| A linked User who is disabled | Nothing changes | "Your Muster account is disabled" |
| A linked Responder or Admin | The Command runs as that User | "Done: {command}", or why it was refused |

The Audit log records `account_link.link`, `account_link.unlink` and `account_link.link_rejected` with the User, the
messenger, the account's id and username, and the Connection; a rejection says why: `token_unknown`, `token_used`,
`token_expired`, `wrong_connection`, `wrong_code` or `conflict`.

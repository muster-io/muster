# Link your messenger accounts

An **Account link** ties your Telegram account, your account on a Mattermost server, your account in a Slack workspace,
or your account on a messenger that Muster reaches through a [Bridge](bridge.md), to your Muster User. With it, a button
you press under an Alert Group in a messenger runs as you — the Timeline names you and shows the messenger you pressed
in — and Mentions and footers in the messenger name you.

A Telegram account belongs to one User across every Telegram Connection. A Mattermost account belongs to one User per
Mattermost Connection, since each Connection is its own server, and so does a Slack account, per Slack Connection,
since each Connection is its own workspace, and an account reached through a Bridge, per Bridge Connection. You have at
most one Telegram account and one account per Mattermost, Slack or Bridge Connection; linking another one replaces the
earlier link, and linking the same account again keeps its link and updates its username and name.

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
   minutes (`account_link.telegram_token_ttl`); the link works once.
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
3. Type the code into your profile within 10 minutes (`account_link.mattermost_code`); capital and small letters are the same. The bot confirms
   "Linked to Muster user {your name}".

A code allows 5 attempts; after five wrong codes it stops working, even with the right code, and you request a new one.
A new request replaces your earlier one. You can request 5 codes an hour, and one Mattermost account gets at most 3
codes an hour, whoever asks (`account_link.mattermost_code_requests`); beyond that, wait until the oldest request is an hour old. A username the server does not
know, a bot account and a deactivated account cannot be linked. An account that is linked to another User is refused.

The bot must be allowed to open direct messages with you; see [Direct messages](mattermost.md#direct-messages).

## Link Slack

1. In your profile, open **Messenger accounts**, choose the Slack Connection, enter your Slack email or name and select
   **Send code**. An email finds your account for sure; a name is compared with the name and the display name of each
   account, regardless of case.
2. The Connection's bot sends you a direct message: "You requested a link to Muster. Do not share this code." with an
   8-character code. It shows in the **Messages** tab of the bot's app, which is read-only, so you can read the code and
   cannot write to the bot. If you did not ask for it, ignore it.
3. Type the code into your profile within 10 minutes (`account_link.slack_code`); capital and small letters are the
   same. The bot confirms "Linked to Muster user {your name}".

If Muster answers "No Slack account with this email or name was found.", check what you typed. If it answers "Several
Slack accounts have this name. Enter the email instead.", do. A bot, a deleted account and Slackbot cannot be linked,
and an account that is linked to another User is refused.

A code allows 5 attempts; after five wrong codes it stops working, even with the right code, and you request a new one.
A new request replaces your earlier one. You can request 5 codes an hour, and one Slack account gets at most 3 codes an
hour, whoever asks (`account_link.slack_code_requests`); beyond that, wait until the oldest request is an hour old.

The bot can send the code only while the Messages tab of its app is on. When it is off, Slack refuses the direct message
and Muster answers that the Messages tab of the Slack app is turned off; the problem is `messages_tab_disabled`. Ask the
admin of the Slack app to turn the tab on under **App Home**, then request a new code. The refused request still counts
toward the limits above, so if you tried several times before the tab was on, you may have to wait up to an hour. See
[Connect Slack](slack.md#what-the-manifest-sets).

## Link through a Bridge

A Bridge Connection reaches a messenger through a bridge, which tells Muster in its info what it can do for Account
links. Every bridge takes a **token** that you send to its bot. A bridge that can look people up and start a direct
chat with them also offers a **code** that its bot sends you, as Mattermost does. Your profile offers what the
Connection's bridge declares.

Once linked, a Mention of you in a message of that Connection names you by the name the bridge reported for your
account, which a bridge with native mentions turns into its messenger's mention, and the footer of a message you
acknowledged, snoozed or resolved names you by that name. Without a link, a Mention is your Muster name as plain text,
which notifies nobody.

### By a token sent to the bot

1. In your profile, open **Messenger accounts**, choose the Bridge Connection and select **Link**.
2. Muster shows a token of 43 characters and, when the bridge declares a link that opens a chat with its bot, that link
   as well. Within 10 minutes (`account_link.bridge_token_ttl`), send the token to the bot of that Connection in a direct message, or open the link,
   which hands the token to the bot. The token works once, and only with the bot of the Connection you chose.
3. The bot answers "Linked to Muster user {your name} ({your login})", and your profile shows the link with the name
   and the handle the bridge reported for your account.

If the bot answers "This link is invalid or expired. Start again from your Muster profile.", the token was used
already, expired, was started for another Connection — another bridge, or another bot of the same bridge — or was
never issued: start again from your profile. If it answers "This account is linked to another user. Its owner or an
admin can unlink it.", that account is linked to someone else; nothing changed.

### By a code by direct message

The code is offered only when the bridge declares how it finds people — its **lookup kind**, one of email address,
login, username or user id — and that its bot can start a direct chat. Your profile names the field after that kind:
**Email**, **Login**, **Username** or **User id**.

1. In your profile, choose the Bridge Connection, enter your email address, login, username or user id in the
   messenger, as the field asks, and select **Send code**.
2. The bot sends you a direct message: "You requested a link to Muster. Do not share this code." with an 8-character
   code. If you did not ask for it, ignore it.
3. Type the code into your profile within 10 minutes (`account_link.bridge_code`); capital and small letters are the same. The link is made at
   once, and the bot sends no confirmation.

If the bridge finds no one, Muster answers "No account with this email was found." — or login, username or user id. If
the bot may not write to you, Muster answers "The bot cannot send you a direct message. Send it the token instead."
The limits are those of Mattermost: 5 attempts per code, a new request replaces your earlier one, 5 code requests an
hour for you and at most 3 codes an hour for one account, whoever asks (`account_link.bridge_code_requests`). An account
that is linked to another User is refused.

## Several Connections of one messenger

An Organization may have several Connections of one messenger, such as two Mattermost bots on one server, two Slack apps
or a second Telegram bot. What a link covers differs by messenger:

- **Mattermost links are per Connection.** Each Mattermost Connection is its own Identity space, even when two
  Connections use bots on the same server: two bots on one server need two links, made one after the other from
  **Link Mattermost** with each Connection. Your profile lists one row per Connection.
- **Slack links are per Connection.** Each Slack Connection is its own workspace, and so its own Identity space: two
  Connections need two links, made one after the other from **Link Slack** with each Connection. Your profile lists one
  row per Connection.
- **Bridge links are per Connection.** Each Bridge Connection is its own Identity space, also when two Connections
  reach the same bridge with two bots: two Connections need two links, made one after the other, and a token started
  for one of them links nothing when you send it to the other's bot. Your profile lists one row per Connection.
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
| An account without a link | Nothing changes | "Your Telegram account is not linked to Muster. Link it in your profile: {link}" — or the same for Mattermost or Slack, and "Your account is not linked to Muster. Link it in your profile: {link}" through a Bridge; the link goes to your profile and carries no secret |
| A linked Viewer | Nothing changes | "You are not permitted to do this" |
| A linked User who is disabled | Nothing changes | "Your Muster account is disabled" |
| A linked Responder or Admin | The Command runs as that User | "Done: {command}", or why it was refused |

The Audit log records `account_link.link`, `account_link.unlink` and `account_link.link_rejected` with the User, the
messenger, the account's id and username, and the Connection; a rejection says why: `token_unknown`, `token_used`,
`token_expired`, `wrong_connection`, `wrong_code` or `conflict`.

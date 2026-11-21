# Notification rules

A **Notification rule** says how a Page reaches a person. Every User has two: the **normal** rule and the
**important** rule. A rule is a short sequence of actions:

- **Mention**: a Thread reply that mentions the person;
- **Call**: a call through a [Notification gateway](../messengers/notification-gateways.md) to the person's confirmed
  contact there;
- **Wait**: a pause of whole minutes before the next action.

Actions without a wait between them happen at the same moment. Muster sends no direct messages to Page anyone. An
[Escalation step](escalation-chains.md) decides whom to Page and when; the rule decides how, and
[How an alert reaches people](how-an-alert-reaches-people.md) shows the two working together. The rules are read and
changed through the API under `/api/v1/me/notification-rules` and `/api/v1/users/{user_id}/notification-rules`, listed
in the [API reference](../reference/api.md).

## Normal and important

The Organization sets the default rules, the field `notification_rules` of the Organization
(`organization.notification_rules`):

- **normal**: a Mention;
- **important**: a Mention, a wait of 5 minutes and a second Mention.

In 0.2.0 an Admin changes them only through the API, with `updateOrganization`; the change is recorded in the Audit
log as `organization.updated`, at `/notification_rules/normal` and `/notification_rules/important`. A call in the
Organization's rules needs only an existing Notification gateway.

A rule has at least one Mention or call and at most 10 actions, and a wait lasts from 1 to 60 minutes
(`notification_rule.limits`). The API gives a wait in seconds, `wait_seconds`, a multiple of 60 from 60 to 3600: a wait
of 5 minutes is `{"kind": "wait", "wait_seconds": 300}`. A rule outside the limits is refused with `422`: `out_of_range`
at the rule for more than 10 actions or at its `wait_seconds` for a wait such as 90 or 3660, and `required` at the rule
when it has no Mention or call.

A call names its gateway with `gateway_id`, which may be left out while only one Notification gateway exists. A call
whose gateway is deleted later, or at which the person no longer has a confirmed contact, stays in the rule: a read
shows it with `available` false and `unavailable_reason` `gateway_deleted` or `no_confirmed_contact`.

## The switch

Each person's rules are in one of four states. The switch "Also call through the Notification gateway" chooses among
the first three:

`organization`, "As in the Organization"
:   The default. The person follows the Organization's current rules: when an Admin changes them, the change reaches
    the person at once.

`call`, "Call"
:   The Organization's rules as they were when the person chose "Call", with a call through the gateway at the start
    of the important rule, at the same moment as its first action. With the default rules, the important rule becomes
    a call and a Mention, a wait of 5 minutes and a Mention; the normal rule stays a Mention. When the Organization's
    important rule already calls through that gateway, nothing is added.

`no_call`, "Don't call"
:   The Organization's rules as they were when the person chose "Don't call", without any call. A rule left without a
    Mention gets one in place of its first call and keeps its waits.

A later change of the Organization's rules does not reach a person in "Call" or "Don't call": they keep the rules of
the moment they chose. "Reset to the Organization's rules", `resetMyNotificationRules`, returns a person to "As in the
Organization".

"Call" needs a Notification gateway and a confirmed contact of the person there
([Contact for calls and SMS](../messengers/notification-gateways.md#contact-for-calls-and-sms)). With contacts at
several gateways, `gateway_id` names the one; without it the answer is `422` `required` at `/gateway_id`. While no
gateway exists the answer is `422` `gateway_required`, and for a gateway without the person's confirmed contact
`contact_required`.

A person changes their own rules from their web session only: a Personal access token gets `403`
(`session_required`) on `/me/notification-rules`. An Admin sees and changes any User's rules with
`getUserNotificationRules` and `updateUserNotificationRules`, by session or token, except their own, which they change
from their web session. Every change is recorded in the Audit log as `notification_rules.updated` with a before/after
diff of the state, the gateway and both rules. A change needs `If-Match` with the ETag of the last read, and a stale
one gets `412`.

## Custom rules

The state `custom` takes any normal and important rule within the limits, through the API:

```json
{
  "state": "custom",
  "normal": [{"kind": "mention"}, {"kind": "wait", "wait_seconds": 600}, {"kind": "mention"}],
  "important": [{"kind": "mention"}, {"kind": "call", "gateway_id": "CN0000000000G1"}]
}
```

A call in a person's custom rules needs their confirmed contact at its gateway (`contact_required` at the action), and
a call while no gateway exists is `gateway_required` at the action. Custom rules stay as they are when the
Organization's rules change. "Reset to the Organization's rules" brings back "As in the Organization".

Deleting a User deletes their rules. How the rules run when an Escalation step Pages a person is described below.

## In the web UI

In **Profile**, the section **Personal notification rules** shows "Normal" and "Important" each in words, as the
person's own sequence — "a Mention in the Thread of the Root message" for the normal rule with the default rules — with
the switch "Also call through the Notification gateway":
"As in the Organization", "Call" and "Don't call", the contact a call goes to ("to +10000000001") and, with several
gateways, "Call through" to choose one. Without a confirmed contact the section says "Confirm your contact for calls
and SMS below first.", and without a gateway "Unavailable: a Notification gateway is needed (a bridge for calls and
SMS)". Custom rules show as "These rules are custom: they were set through the API or by an Admin." and **Reset to the
Organization's rules** brings back the default. An Admin sees and changes the rules of any User on the User's page.
**Linked accounts**, the first section of the Profile, says what a Mention needs: "A Page is a Mention in the Thread; it
reaches only a linked account."

## With the steps of a chain

An [Escalation step](escalation-chains.md#steps) that Pages people — "Page the On-call of a Schedule", "Page people" —
Pages each person by their own rule, the one named by the step's importance: `normal` or `important`. The step
decides whom and when; the person's rule decides how and how often. A step "Page the whole Team" Pages nobody by a
rule: it writes the Team mention, or the word for everyone in the chat, once. A step "Call through a Notification
gateway" calls the persons it names through its gateway, whatever their rules say.

The rule belongs to the Page that started it, and stops with it. It stops when the Alert Group is acknowledged,
snoozed or resolved, and when its chain starts over; a Mention or a call that comes due after that is not made. A chain
that has only run out does not stop its rules: the later Mention of an important Page still comes due. The rule is read
when the Page starts, so a change of a person's rules reaches their next Page and not the one that runs. The same person
Paged by two steps, or by two chains, has two runs of their rule, each of its own.

A person who has to be reached by a call at night puts a call in the rule their Pages run — in the state "Call", the
important rule. A chain then needs no call step for them: a step that Pages them importantly rings their phone.

## Mentions and Account links

A Mention of a rule is a Thread reply that names the person as their messenger understands it, and Muster can name
them only through an [Account link](../messengers/account-links.md) in the Identity space of the Connection of the
Destination in whose Thread the reply is written. A person with no Account link there is named in a line "Page not
delivered: @petr has no linked Mattermost account" instead, which mentions nobody, and their rule's Mentions of that
Destination do not make them an Assignee. Their calls are not affected: a call needs a confirmed contact at a gateway,
not an Account link.

An Identity space is all of Telegram, one Mattermost Connection, one Slack Connection or one Bridge. A person with
Account links in several of them is Mentioned in each Destination whose Connection lies in one of their spaces, and a
person linked in one Mattermost Connection is not Mentioned in the Destinations of another. Even where they are
Mentioned, a Mention is a message in a chat: whether it also pushes a notification to the phone depends on the
messenger, its app, the phone's settings and the push mode, so at night only a call through a Notification gateway is
reliable ([At night](../messengers/notification-gateways.md#at-night)).

## Calls in rules

When an [Escalation step](escalation-chains.md) Pages a person normally or importantly, their rule of that name runs:
its Mentions are Thread replies in the chain's Threads, and each call action is a call through its gateway to their
confirmed contact there, a Page of the same step. In the state "Call" with the default rules, an important Page
Mentions the person and calls them at once, then Mentions them again 5 minutes later unless someone acknowledged
meanwhile; a normal Page only Mentions them. The call records the Quiet `paged` in the Timeline, naming the person
and the gateway, and writes nothing in the Thread.

A call action whose gateway was deleted, or that finds the person without a confirmed contact there — such as one
removed after they chose "Call" —, is not made: the Timeline records `escalation_step_skipped` for the step with the
person and the reason, `gateway_deleted` or `no_confirmed_contact`, and the person still gets the Mentions of the
rule. The rule goes on with its next action.

The calls of rules and of call steps go through one queue per person and gateway: the first call goes at once, and
the Pages that come due while a call to the person waits, is being retried, or within `gateway.combine_window` after
the previous call ended join the next call, which goes once that window has passed, as one call about all of their
Alert Groups. Mentions are never combined. A call drops, before its first attempt, the Alert Groups on which the
rule run that called stopped — its Alert Group acknowledged, snoozed or resolved, or its chain stopped or started
over —, and is not sent when none is left. A call the gateway accepted makes the person an Assignee of the chain;
one that ends Not delivered records `page_not_delivered` and lets the chain and the rules go on
([Calls through a Notification gateway](escalation-chains.md#calls-through-a-notification-gateway)). When a rule
comes due late, as after Muster was down, its overdue Mentions are made as one, and each overdue call is still made.

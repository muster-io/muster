# C-31. Notification rules

[L2 index](../L2.md) · Release: 0.2.0 · UI: yes · Depends on: C-03, C-18, C-30

**Goal.** Each person decides how a Page reaches them: a normal and an important rule, each a short sequence of Mentions
in the Thread and calls through a Notification gateway with waits between them. An Escalation step Pages a person by one
of the two. Muster sends no direct messages to Page anyone (ADR-0018).

Names in the form `area.setting` refer to rows of [defaults.md](../l1/defaults.md).

## Scenarios

1. Ivan keeps the Organization's rules: Paged normally, he gets a Mention in the Thread; Paged importantly, a Mention
   and, if nobody has acknowledged five minutes later, a second Mention.
2. Petr confirmed his phone number with the Notification gateway and sets "Also call through the Notification gateway"
   to "Call"; an important Page Mentions him and calls him at once. When the Admin later changes the Organization's
   important rule, Ivan's follows it at once, while Petr's stays as he chose it.
3. An installation without a gateway shows the switch unavailable; the important rule stays a Mention and a repeat.
4. The Admin looks at Sergey's rules after he missed Pages and sets his normal rule to the important one's sequence; the
   Audit log records the change.

## Functional requirements

- **C-31.FR-1** Every User has two Notification rules, normal and important. A rule is an ordered list of actions:
  "Mention in the Thread", "Call through a Notification gateway", which names the gateway when there is more than one,
  and "Wait N minutes". A rule has at least one action that Pages, and its lengths stay within
  `notification_rule.limits`.
- **C-31.FR-2** The Organization sets the default rules (`organization.notification_rules`): normal — a Mention in the
  Thread; important — a Mention, a wait of 5 minutes and a second Mention. A User's rules are in one of four states. "As
  in the Organization", the default: the Organization's current rules, so that an Admin's change of them applies to the
  User at once. "Call": the Organization's rules as they were when the User chose it, with a call through the
  Notification gateway at the start of the important rule, together with its first action, unless that rule already
  calls through that gateway. "Don't call": the Organization's rules as they were when the User chose it, without any
  call. Custom: sequences set through the API or by an Admin. A later change of the Organization's rules touches none of
  the last three, as journal D569 decided; "Reset to the Organization's rules" returns a User to "As in the
  Organization".
- **C-31.FR-3** When an Escalation step Pages a person normally or importantly (C-32.FR-3), Muster runs that rule for
  them: a Mention action is a Thread reply in the chain's Threads that mentions them (C-32.FR-8); a call action is a
  call through the gateway to their confirmed contact there (C-30.FR-4); a wait delays the next action. The run stops
  when the Alert Group is acknowledged, snoozed or resolved, and when its chain starts over or stops; an action that
  comes due after that is not made. A call action for a person without a confirmed contact at that gateway, or whose
  gateway is gone, is skipped and recorded with its reason (C-32.FR-14). Each Mention and call a run makes is a Page of
  its step.
- **C-31.FR-4** Each person changes their own rules in the profile from their web session (C-03.FR-27); an Admin sees
  and changes any User's rules on the User's page — the switch, or sequences, which make the rules custom — and the
  change is recorded in the Audit log with a before/after diff. A call is offered only while a Notification gateway
  exists (C-30.FR-9) and can be chosen only for a gateway at which the User has a confirmed contact.
- **C-31.FR-5** The profile shows each rule in words and edits them with the switch "Also call through the Notification
  gateway", which has three states: "As in the Organization", "Call" and "Don't call" (FR-2). With the default rules,
  "Call" makes the important rule a Mention with a call at the same time, a wait of 5 minutes and a second Mention, and
  the normal rule stays a Mention. The call goes through the gateway at which the User has a confirmed contact; with
  several, the profile asks which one next to the switch. The API also takes any sequence within the limits; custom
  rules are shown in words without the switch, with "Reset to the Organization's rules". An editor of whole sequences
  comes later in L2.
- **C-31.FR-6** The documentation describes the rules, their defaults, how they combine with the steps of a chain, and
  that a Mention reaches a person only through an Account link in the Identity space of the Destination's Connection.

## UI

Profile → "Linked accounts" first: "A Page is a Mention in the Thread; it reaches only a linked account.", with the
Account links of C-18. Then "Personal notification rules": "What happens when an Escalation step calls you 'normally' or
'importantly'. Muster sends no direct messages." — "Normal: a Mention in the Thread of the Root message." and
"Important: a Mention in the Thread and again after 5 minutes if you did not answer.", each as the User's current
sequence in words; the switch "Also call through the Notification gateway" — "As in the Organization", "Call", "Don't
call" — with "to {contact}" and, with several gateways, the choice of one, "Confirm your contact for calls and SMS below
first." or "Unavailable: a Notification gateway is needed (a bridge for calls and SMS)"; and the contact block of C-30.
Users → a User's page: their rules, editable by Admins. Organization → Alert handling: the default rules, in words;
in 0.2.0 they change only through the API.

## API surface

`me/notification-rules` (read; update the state of the switch with its gateway, or custom sequences; reset);
`users/{id}/notification-rules` (read, update); the defaults as a field of `organization`.

## Acceptance

Checked with a virtual clock and the recording test adapter of C-11; until C-32 Pages people, the tests start the run of
a rule for a person directly.

- **C-31.AC-1** With the default rules, a normal Page makes one Thread reply that mentions the person; an important Page
  makes a second one 5 minutes later, and none when the Alert Group was acknowledged in between.
- **C-31.AC-2** An important rule with a call action calls the person's confirmed contact through the fake gateway at
  once; a person without a confirmed contact gets the Mentions, and the Timeline records the skipped call with its
  reason.
- **C-31.AC-3** Saving a call action without a gateway, or for a gateway without a confirmed contact, gets `422`; a
  Personal access token gets `403` `session_required` on `me/notification-rules`; an Admin changes another User's rules
  and the Audit log shows the diff.
- **C-31.AC-4** A User whose rules are "As in the Organization" follows a change of `organization.notification_rules` at
  once; the rules of a User with "Call" or "Don't call", and custom rules, stay as they were; "Call" on an
  Organization's important rule that already calls through that gateway adds no second call; after "Reset to the
  Organization's rules" a User follows the Organization's rules again.
- **C-31.AC-5** In a real browser, the profile shows both rules in words, the switch unavailable without a gateway and
  available after a contact is confirmed, in English and in Russian.
- **C-31.AC-6** With the default rules, the switch "Call" makes the important rule exactly a Mention with a call, a wait
  of 5 minutes and a Mention, and an important Page then calls the fake gateway at once; "As in the Organization" gives
  back the Organization's rules. With two gateways at which the User has confirmed contacts, the profile asks which one
  the call uses. After an Admin sets a sequence of three Mentions through the API, the profile shows it in words without
  the switch, and "Reset to the Organization's rules" brings the switch back to "As in the Organization".

## Related ADRs

ADR-0005, ADR-0013, ADR-0018.

## Depends on

C-03 — profiles and the web session; C-18 — Account links; C-30 — Notification gateways and contacts.

## Suggested story split

- **BE** — the rules, their defaults and their runs for Pages.
- **FE** — the rules in the profile and on the User's page, the defaults on the Organization page.

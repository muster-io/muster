# Notification rules

A **Notification rule** says how a Page reaches a person. Every User has two: the **normal** rule and the
**important** rule. A rule is a short sequence of actions:

- **Mention**: a Thread reply that mentions the person;
- **Call**: a call through a [Notification gateway](../messengers/notification-gateways.md) to the person's confirmed
  contact there;
- **Wait**: a pause of whole minutes before the next action.

Actions without a wait between them happen at the same moment. Muster sends no direct messages to Page anyone. The
rules are read and changed through the API under `/api/v1/me/notification-rules` and
`/api/v1/users/{user_id}/notification-rules`, listed in the [API reference](../reference/api.md).

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

Deleting a User deletes their rules. How the rules run when an Escalation step Pages a person is described with the
Escalation chains.

# C-41. An editor of whole Notification rule sequences

[L2 index](../L2.md) · Release: a 0.2.x patch release after 0.2.0 · UI: yes · Depends on: C-30, C-31, C-32

**Goal.** A person builds both of their Notification rules in the profile as whole sequences — Mentions in the Thread,
calls through a Notification gateway and waits between them — and sees, before saving, what happens when an Escalation
step Pages them. The API takes any sequence within the limits from 0.2.0 (C-31.FR-5); until now the profile offered
only the switch "Also call through the Notification gateway", and custom rules could be set only through the API or by
an Admin (journal D567, D569).

Names in the form `area.setting` refer to rows of [defaults.md](../l1/defaults.md). This capability is a draft: where
its decisions are still open, a requirement names the [open question](#open-questions) that settles it.

## Scenarios

1. Petr wants his phone to ring only if a Mention does not work: he opens "Edit sequences" in his profile and makes the
   important rule a Mention, a wait of 3 minutes, a call through the Notification gateway, a wait of 10 minutes and a
   second call. The preview reads "At once: a Mention in the Thread · After 3 min: a call to +7 900 *** ** 00 through
   Phone gateway · After 13 min: the call again", and "Stops as soon as someone acknowledges, snoozes or resolves the
   Alert Group, or the chain stops or starts over". He saves; the profile shows his rules in words as custom, with
   "Reset to the Organization's rules".
2. Anna's important rule has 10 actions; "Add action" is unavailable with "A rule has at most 10 actions, waits
   included." She sets a wait of 90 minutes and the field says "A wait is 1 to 60 minutes."
3. Ivan removes every action from his normal rule; "Save" stays unavailable with "A rule needs a Mention or a call."
4. The gateway of one of Sergey's calls was deleted. The editor shows that call as "unavailable: the Notification
   gateway was deleted"; he may keep it, which reaches nobody, or remove it.
5. An Admin opens Sergey's page after missed Pages, edits his normal rule with the same editor and saves; the Audit log
   records the change with a before/after diff.
6. On a phone 360 CSS pixels wide, Ivan moves an action up and down with buttons, without dragging.

## Functional requirements

### The editor

- **C-41.FR-1** "Personal notification rules" in the profile (C-31.FR-5) gains "Edit sequences", which opens an editor
  of the normal and the important rule together, each starting from the sequence in effect — the Organization's rules,
  those of "Call" or "Don't call", or the custom ones (C-31.FR-2). The switch "Also call through the Notification
  gateway" stays as the short way for the common case, and custom rules keep "Reset to the Organization's rules".
- **C-41.FR-2** Each rule is an ordered list of actions (C-31.FR-1): "Mention in the Thread"; "Call through a
  Notification gateway", which names the gateway when more than one exists; and "Wait N minutes". The editor adds an
  action at the end or after any action, removes one, changes its kind, and moves it up or down with buttons that work
  with the keyboard and at phone width; dragging, where offered, is never the only way. Actions without a wait between
  them happen at the same moment, and the editor shows them as one moment.
- **C-41.FR-3** A call is offered only while a Notification gateway exists (C-30.FR-9), and only for gateways at which
  the person has a confirmed contact; the other gateways are listed as unavailable — in the profile with "Confirm your
  contact for calls and SMS below first.", on a User's page with "{name} has no confirmed contact for calls and SMS."
  — and without any gateway the action reads "Unavailable: a Notification gateway is needed (a bridge for calls and
  SMS)". A call whose gateway was deleted or whose contact is gone shows the reason the API reads (`gateway_deleted`,
  `no_confirmed_contact`). Where the person's rules are stored — "Call", "Don't call" or custom — such a call may be
  kept, since the API lets as many calls through a gateway pass as the stored rules have (`updateMyNotificationRules`,
  `NotificationRuleSet`), or removed. Where the editor starts from the Organization's rules, which the person keeps
  none of, a call there without the person's confirmed contact is shown as a problem before "Save", as the API would
  refuse it (`contact_required`).

### Checks and preview

- **C-41.FR-4** The editor checks each rule as the API does (`notification_rule.limits`, C-31.FR-1): at most 10
  actions, waits included, so "Add action" is unavailable at 10; each wait a whole number of minutes from 1 to 60; at
  least one Mention or call. It shows each problem at its action or rule while the person edits, keeps "Save"
  unavailable while one remains, and shows the API's answer at the same places when the server refuses (`422`, with
  its field pointers). A wait at the end of a rule and two waits in a row are allowed, as the API allows them, and the
  editor says what they do ([open question 2](#open-questions)).
- **C-41.FR-5** Beside each rule, the editor previews what happens when an Escalation step Pages the person with it: a
  list of moments from the Page — "At once", "After N min" — each with what is made then: a Mention in the Thread, a
  call to the masked contact through the named gateway, or the same again; then "Stops as soon as someone acknowledges,
  snoozes or resolves the Alert Group, or the chain stops or starts over" (C-31.FR-3). An unavailable call reads
  "a call that reaches nobody: {reason}". The preview follows every change at once, needs no request, and uses the
  words of the rules in the profile, in English and Russian with Russian plurals.

### Saving

- **C-41.FR-6** "Save" stores both rules as custom (`state` `custom`, C-31.FR-2) from the web session (C-03.FR-27),
  with `If-Match` (`updateMyNotificationRules`); before the first save of a person who follows the Organization, the
  editor says "Your rules will no longer follow the Organization's rules. 'Reset to the Organization's rules' brings
  that back.". A stale `If-Match` shows "The rules changed in the meantime. They are shown as they are now; try again."
  with the current rules and the person's edits kept beside them. "Cancel" leaves the rules as they were, and leaving
  the profile with unsaved edits asks first. The change is recorded in the Audit log as `notification_rules.updated`
  with a before/after diff, as C-31.FR-4 says, and a save that changes nothing records nothing.
- **C-41.FR-7** An Admin edits any User's rules with the same editor on the User's page (C-31.FR-4), saving through
  `users/{id}/notification-rules`; a call is offered for the gateways at which that User has a confirmed contact. The
  person is not notified of an Admin's change; the profile shows the rules as custom. The profile's text for custom
  rules, today "These rules are custom: they were set through the API or by an Admin.", says that the person may also
  have set them.

### Organization defaults and Terraform

- **C-41.FR-8** A person's and a User's rules are never managed by the Terraform provider, which manages only the
  Organization's default rules (journal D757); the editor of a person's rules shows no marker of it. Whether the same
  editor also edits the Organization's default rules on Organization → Alert handling, which in 0.2.0 change only
  through the API (C-31 UI), is [open question 1](#open-questions); where it does, it shows the marker of an object
  that Terraform manages, which the capability of the Terraform provider specifies — a badge and a warning, without
  refusing the change (journal D757).
- **C-41.FR-9** The editor is usable from a viewport 360 CSS pixels wide: one rule under the other, the preview under
  each rule, and every control reachable without horizontal scrolling of the page.
- **C-41.FR-10** The documentation's page on Notification rules describes the editor, the checks, the preview, what
  custom rules mean for later changes of the Organization's rules and how to return to them, with a screenshot of the
  editor (C-21.FR-9). C-31.FR-5, which says that an editor of whole sequences comes later in L2, gets a revision marker
  pointing at this capability with the story that implements it.

## UI

Profile → "Personal notification rules": "Edit sequences" next to the rules in words. The editor: "Normal" and
"Important", each a list of actions with "Mention in the Thread", "Call through a Notification gateway" with "Call
through" and the gateway, "Wait" with "minutes", the buttons "Move up", "Move down", "Remove" and "Add action"; under
each rule "What happens when you are Paged {importance}": the moments and the stop line; "Save", "Cancel". The same
editor on a User's page for Admins.

## API surface

None new: the editor saves through `me/notification-rules` and `users/{id}/notification-rules` with the state `custom`
(C-31), and reads the gateways and the person's contacts through the operations of C-30. Answering
[open question 2](#open-questions) with a refusal would change the validation of `NotificationRuleSet`.

## Acceptance

- **C-41.AC-1** In a real browser, a person with a confirmed contact at the fake gateway builds the important rule
  Mention, wait 3 minutes, call, wait 10 minutes, call; the preview reads the three moments; after "Save" the API
  returns that rule with the state `custom`, and an important Page of an Escalation step, advanced with the
  development clock, Mentions the person at once, calls the fake gateway after 3 minutes and again after 13.
- **C-41.AC-2** At 10 actions "Add action" is unavailable; a wait of 0 or 61 minutes and a rule without a Mention or a
  call show their problem at the action or rule concerned, with "Save" unavailable; a request that bypasses the editor
  gets the same `422` with the same pointers, which the editor shows at the same places.
- **C-41.AC-3** Without a Notification gateway the call action is unavailable with its reason; with two gateways and a
  confirmed contact at one, only that one is offered and the other is listed as unavailable, with the profile's text
  in the profile and the User's page text on a User's page.
- **C-41.AC-4** A stored custom call whose gateway was deleted shows its reason; saving the rules with it kept
  succeeds, and adding a second call through that gateway is refused. A person in the state "As in the Organization"
  whose Organization's important rule calls through a gateway where they have no confirmed contact sees that call as a
  problem in the editor, and "Save" stays unavailable until it is removed.
- **C-41.AC-5** A stale `If-Match` shows the current rules with the person's edits kept; "Reset to the Organization's
  rules" after a save returns the state `organization`.
- **C-41.AC-6** An Admin saves a User's rules with the editor; the Audit log shows `notification_rules.updated` with the
  diff, and a Viewer's or Responder's attempt at another User's rules gets `403`.
- **C-41.AC-7** In a real browser 360 CSS pixels wide, the editor works with the buttons alone and the page does not
  scroll sideways, in English and in Russian.

## Open questions

Each is for the maintainer to settle before the story that depends on it; the recommendation is the agent's.

1. **Does the editor also edit the Organization's default rules in the UI?** Recommendation: yes, for Admins on
   Organization → Alert handling, with the Terraform marker when Terraform manages them, never a refusal (journal
   D757); the words alone stay for everyone else. The alternative is to keep them API-only, as 0.2.0 does, which
   leaves the API and Terraform as the only ways to change them.
2. **A wait at the end of a rule and two waits in a row.** Both are accepted by the API and do nothing useful.
   Recommendation: keep the API as it is and let the editor warn — "A wait at the end does nothing" — and offer to merge
   two waits into one; refusing them now would refuse rules that are stored already.
3. **Saving rules equal to the Organization's.** Recommendation: they are stored as custom like any other and stop
   following later changes, and the editor says so when they are equal, offering "Reset to the Organization's rules"
   instead (C-31.FR-2).

## Related ADRs

ADR-0005, ADR-0013, ADR-0018.

## Depends on

C-30 — Notification gateways and contacts; C-31 — Notification rules, their states, limits and API; C-32 — the Pages
of Escalation steps that run the rules.

## Suggested story split

- **FE** — the editor in the profile and on the User's page, with its checks, preview and phone layout, the new text
  for custom rules (and, if open question 1 says so, on Organization → Alert handling).
- **docs** — the editor on the page on Notification rules, with the screenshot; the revision marker on C-31.FR-5.

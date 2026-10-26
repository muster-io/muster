# C-25. Teams

[L2 index](../L2.md) · Release: 0.2.0 · UI: yes · Depends on: C-03, C-08, C-20

**Goal.** Teams say who owns what and which Alerts concern whom: a Team has a slug that names it in the Team label, its
own Team matchers for Alerts whose rules set no team, people added by hand or through the groups of the identity
provider, a Team mention for each Connection that can mention a messenger group, and Working hours. A Team grants no
access: Roles stay global (ADR-0018).

Names in the form `area.setting` refer to rows of [defaults.md](../l1/defaults.md); the lifecycle events are rows of the
[L2 lifecycle event table](../l1/reference.md#lifecycle-events-added-in-l2).

## Scenarios

1. The Admin creates the Team "payments" with the slug `payments`. Alerts whose Team label `team` is `payments` belong
   to it; the Alert Group page shows "Teams: payments — by the Team label".
2. The rules of the checkout services set no `team` label. The Team "checkout" gets the Team matchers
   `namespace=~"checkout|billing"` and, for the shared test clusters where every team uses one namespace,
   `pod=~"checkout-.*"`; an Alert that one of them picks belongs to "checkout", and its Timeline names the matcher.
3. The Team "payments" names the identity provider group `teams/payments/*`. Olga, in `teams/payments/oncall`, signs in
   through OIDC and is in the Team; Petr, added by hand, stays when the groups change.
4. The Team "search" has nobody in it and no Schedule: it is only a name for Alerts, its Destinations and its Team
   mention.
5. Alerts arrive with `team="checkout"` and `team="Payments"`. No Team has those slugs: nothing is added to their Alert
   Groups, the Timeline records the unknown values, and the Teams page lists both under "Unknown values of team".
   "Create Team" next to `checkout` opens the new Team form with that slug; `Payments` cannot be a slug, so the form
   says that a Team gets those Alerts only through a Team matcher such as `team="Payments"`.
6. "payments" sets its Team mention `payments_team` for the Mattermost Connection. For the Slack Connection the field
   says that the scope `usergroups:read` is needed; for Telegram it says that Telegram has no group mentions.
7. "payments" works 11:00–19:00 Monday to Friday in Europe/Berlin and sets its own Working hours; "devops" keeps the
   Organization's.

## Functional requirements

- **C-25.FR-1** A Team has a name, unique in the Organization; a slug, unique in the Organization, which is the value of
  the Team label that names it (`team.slug_format`); a description; the people in it (FR-3, FR-4); its Team matchers
  (FR-5); its Team mentions (FR-8); its Working hours (FR-9); and, from the capabilities that add them, its Destinations
  (C-26.FR-1), Schedules (C-27.FR-1) and Escalation chains (C-32.FR-2). A Team may have nobody in it. A Team grants no
  Permission and limits none: every check stays a check of the User's Role (C-03.FR-2), and the only check that looks at
  a Team is the precondition of Overrides (C-27.FR-6).
- **C-25.FR-2** Ownership and delivery are two relations. Routes, Destinations and Escalation chains may name the Team
  that owns them, and every Schedule belongs to one; the owning Team only organizes: lists of these objects and of Alert
  Groups filter by Team and by "My Teams" — the Teams the signed-in User is in. Owning a Destination never makes it one
  of the Team's Destinations, and a Route's owning Team never adds delivery (C-26.FR-7).
- **C-25.FR-3** An Admin adds Users to a Team and removes them, in the UI or through the API. A User may be in several
  Teams. Deleting a User removes them from every Team; disabling a User keeps them in their Teams, and their Shifts
  count as gaps (C-27.FR-5).
- **C-25.FR-4** A Team may name groups of the identity provider: each entry has a group and, optionally, the claim it
  reads, by default the groups claim of the OIDC settings, and matches as the entries of the Role mapping do
  (C-03.FR-5): exactly, or as a subtree when it ends in `/*`. At every OIDC sign-in and at every successful background
  re-check (C-03.FR-30), Muster puts the User into each Team with an entry that matches them and takes them out of each
  Team they are in only through entries that no longer match; the people an Admin added by hand are never taken out. A
  person added by hand who also matches keeps both sources. A claim is read from the ID token, then from userinfo; with
  an identity provider that grants no offline token, such as GitLab, membership changes only at sign-in. Changing a
  Team's entries applies to each User at their next sign-in or re-check. Membership never changes a Role. Each change
  made by the identity provider is recorded in the Audit log with the source `idp` and the entry that matched.
- **C-25.FR-5** The Team label is an Organization setting (`organization.team_label`). An Alert belongs to a Team when
  the value of its Team label, with the Static labels of its Integration (C-05), equals the Team's slug — exactly, so
  `Payments` is not `payments` — or when one of the Team's Team matchers picks it. Team matchers use the syntax of Route
  Matchers (C-08.FR-2) on any label, and each is enough on its own: an Alert that any one of them picks belongs to the
  Team. An Alert may belong to several Teams. Muster decides the Teams of an Alert when the Alert starts firing and
  records them, with the rule that matched — the Team label, or the matcher by its text — on the Alert; the Alerts view
  of its Integration (C-06.FR-19) and the Alert Group page show them (_provisional_, P-62: an Alert keeps the Teams it
  was given; a later change of the Team label, a slug or Team matchers applies to Alerts that start firing afterwards).
- **C-25.FR-6** The Teams of an Alert Group are the Teams of all the Alerts that ever joined it. They only grow: a Team
  stays among them after its Alerts resolve (ADR-0018). Each Team that joins records the lifecycle event `team_joined`,
  which names the Team and the rule that brought it. The Teams of an Alert Group are shown in the header of the Alert
  Group page, in the field "Teams" of its Root message (C-29.FR-1), as a column and a filter of the Alert Group list
  (C-09.FR-13) with the quick filter "My Teams", and in the `alert_group` of outgoing webhook events (C-15.FR-2). They
  exist whatever the Route says; whether they also receive the Alert Group is the Route's choice (C-26.FR-2).
- **C-25.FR-7** A value of the Team label that names no Team adds nothing to the Alert Group: it goes to its Route's
  Destinations as before, and its Timeline records the lifecycle event `unknown_team_value` once per value. The Teams
  page lists the values of the Team label seen without a Team over `organization.team_values_window`, each with when it
  was last seen and "Create Team", which opens the new Team form with the value as its slug; a value that is not a valid
  slug (`team.slug_format`) is left for the Admin to change, with the note that a Team then gets these Alerts only
  through a Team matcher.
- **C-25.FR-8** A Team has a Team mention for each Connection whose messenger can mention a group: a Mattermost
  Connection, by the name of a Mattermost group; a Slack Connection, by a Slack user group chosen from those the
  Connection reads, once its check has found the scope `usergroups:read` (C-24.FR-1); a Bridge Connection, by a group
  name, when its bridge declares group mentions in `info` (C-22.FR-2). Telegram has no group mentions, so a Telegram
  Connection offers no Team mention. The Team page lists every Connection with the field or, where it cannot have one,
  the reason. Mentioning a Team in a Destination whose Connection has no Team mention for it mentions nobody; it never
  mentions the Team's On-call instead (C-28.FR-3). Saving a Team mention for a Connection that cannot carry one is
  refused with `422` and the code `group_mentions_unsupported`.
- **C-25.FR-9** Working hours are days of the week, a start and an end on the 24-hour clock, and a time zone. The
  Organization's (`organization.working_hours`) start at `organization.workday_start`, the start of the working day that
  the Snooze presets use (C-10.FR-17); a Team uses them unless it sets its own (`team.working_hours`). The end is later
  than the start on the same day (_provisional_, P-63). Escalation steps limited to Working hours use those of the Team
  that owns their chain, else the Organization's (C-32.FR-3). Holidays are not part of Working hours.
- **C-25.FR-10** Deleting a Team that has Schedules or owns Escalation chains is refused with `409` and the code
  `team_in_use`, naming them; otherwise the Team, its people, entries, Team mentions and its list of Destinations are
  removed, the Routes, Destinations and chains it owned keep no owner, and Alert Groups that had it among their Teams
  keep its name, marked deleted (_provisional_, P-64). Its slug becomes free.
- **C-25.FR-11** Teams use optimistic locking (`If-Match`), and every change of a Team, its people, entries, Team
  mentions and Working hours is recorded in the Audit log with a before/after diff. Each Team exports
  `muster_team_info{team,name}`; the stories register their log events and add the Teams' lifecycle events to the [L2
  lifecycle event table](../l1/reference.md#lifecycle-events-added-in-l2).
- **C-25.FR-12** The documentation has a page on Teams: what a Team owns and what it does not grant, the Team label and
  Team matchers, a worked example with labels such as `category`, `team` and `system` set by a rule generator, Teams
  without people that only name Alerts, test clusters where every team shares one namespace (match `pod`, an app label,
  or put the team label on the workloads), membership from the identity provider with the claim of each entry and
  subtrees, and Team mentions per messenger.

## UI

Teams in the main navigation: the list of Teams, each with its On-call now (C-27.FR-9), how many people are in it and
its Team mentions, and above it the Team label with its hint "An Alert belongs to a Team by the value of the Team label
or by the Team's matchers. Teams do not limit access: Roles are the same for the whole Organization." and a link to
change it in the Organization settings; the section "Unknown values of team" (named after the Team label) with each
value, when it was last seen and "Create Team". The Team page: name, slug, description; "On call now"; Working hours —
the Organization's, shown as "Organization default: Mon–Fri 09:00–18:00, Europe/Berlin", or its own with days, from, to
and time zone; "Which Alerts belong to this Team" — the Team label value (the slug) and the Team matchers, with the hint
"A matcher may look at any label of the Alert, not only `namespace`. Any one matcher is enough." and "On test clusters
with a shared namespace, match by `pod` or `app`, or put the team label on the pods", and an example of how an Alert
Group shows it; People — User, Role, source ("added by hand" or the identity provider group), Account links, and
"Remove" for people added by hand; "Identity provider groups" with claim and group per entry — the claims of discovery
offered as suggestions, as in the Role mapping (C-03.FR-5) — and the hint "Checked at every sign-in, like the Role
mapping: each entry reads its own claim, and `a/b/*` takes the whole subtree. The Role of the people does not change.";
"How to mention the Team" — one field per Connection, or the reason it has none ("needs the scope usergroups:read", "the
bridge declares no group mentions", "Telegram cannot mention everyone or groups"), with the hint "The entry 'Team' of a
Destination's Mentions is this Team mention. Without one, nobody is mentioned, and the On-call is not mentioned
instead."; and the sections of C-26 (Destinations of the Team), C-27 (Schedules) and C-32 (Escalation chains). Lists of
Routes, Destinations, Schedules, Escalation chains and Alert Groups gain the Team filter and "My Teams".

## API surface

`teams` (list, create, read, update, delete); `teams/{id}/users` (list, add, remove); identity provider entries, Team
matchers, Team mentions and Working hours as fields of `teams`; `team-label-values` (list the values seen without a
Team); the owning Team as a field of `routes`, `destinations` and `escalation-chains`; the Teams of an Alert and of an
Alert Group, with the rule that brought each, on `alert-groups` and `alerts`; the Team filters of the list operations.

## Acceptance

Checked with the fake Alertmanager and the fake OIDC server, through the API and the UI.

- **C-25.AC-1** With the Team "payments" (slug `payments`), a newly firing Alert with `team="payments"` belongs to it by
  the Team label, and its Alert Group lists the Team; an Alert with `team="Payments"` belongs to no Team, its Alert
  Group's Timeline records `unknown_team_value` once, and `team-label-values` lists `Payments` with the time last seen.
- **C-25.AC-2** A Team with the Team matchers `namespace=~"checkout|billing"` and `pod=~"checkout-.*"` gets an Alert
  with `namespace="billing"` and one with `pod="checkout-api-1"`, each with the matcher recorded, and not one with
  `namespace="search"`; an Alert that both a slug and a matcher of two different Teams pick belongs to both.
- **C-25.AC-3** Against the fake OIDC server, a Team with the entry `teams/payments/*` takes in a User whose groups
  claim carries `teams/payments` and one with `teams/payments/oncall`, not one with `teams/paymentsx`; after the second
  User's group is removed, their next sign-in takes them out of the Team, while a User added by hand stays, and the
  Audit log records the change with the source `idp`; the Roles of all three are unchanged.
- **C-25.AC-4** An entry naming the claim `https://gitlab.org/claims/groups/maintainer` matches a User whose ID token
  carries that claim with the group, and also when only userinfo carries it.
- **C-25.AC-5** The Alert Group of an Alert of "payments" that is later joined by an Alert of "search" lists both Teams,
  with two `team_joined` entries naming the rule; after the Alert of "search" resolves, both Teams are still listed.
- **C-25.AC-6** A Responder who is in a Team gets `403` on changing it, and a Viewer in a Team is refused Acknowledge as
  any Viewer is.
- **C-25.AC-7** A Team mention for a Telegram Connection, and for a Slack Connection whose last check found no
  `usergroups:read`, is refused with `422` `group_mentions_unsupported`; one for a Mattermost Connection is saved.
- **C-25.AC-8** Working hours whose end is not later than their start are refused with `422`; a Team without its own
  reads the Organization's, and a change of `organization.workday_start` moves their start.
- **C-25.AC-9** Deleting a Team with a Schedule gets `409` `team_in_use` naming it; after the Schedule is deleted, the
  Team is deleted, the Route it owned has no owning Team, and an Alert Group that listed it shows it as deleted.
- **C-25.AC-10** In a real browser, the Teams page shows "Unknown values of team" with `checkout` and `Payments`;
  "Create Team" opens the form with the slug `checkout`, and for `Payments` with the note about Team matchers; the Team
  page shows each person's source and the Team mention field of each Connection or its reason, in English and in
  Russian.

## Related ADRs

ADR-0003, ADR-0010, ADR-0016, ADR-0018.

## Depends on

C-03 — OIDC sign-in, the Role mapping and its re-check; C-08 — Matchers and routing; C-20 — Organization settings.

## Suggested story split

- **BE** — Teams, people by hand and from the identity provider, the Team label and Team matchers, the Teams of Alerts
  and Alert Groups with their lifecycle events, unknown values, Team mentions, Working hours, the owning Team on Routes,
  Destinations and chains, filters.
- **FE** — the Teams list and the Team page, the Team filters of the lists, the Teams on the Alert Group page.

---
id: S-171
title: Group mentions in Slack, and group mentions and mentions of everyone through bridges, with the kind of a Mention in the Bridge protocol (BE)
capability: C-28
kind: be
layer: L2
depends_on: [S-140]
covers: [C-28.FR-2, C-25.FR-8, C-22.FR-16, C-24.FR-6]
files_touched:
  - api/bridge.yaml
  - internal/mentions/mentions.go
  - internal/mentions/mentions_test.go
  - internal/slack/layout.go
  - internal/slack/layout_test.go
  - internal/bridge/envelope.go
  - internal/bridge/envelope_test.go
  - internal/bridge/delivery.go
  - internal/fakes/fakebridge/messages.go
  - internal/fakes/fakebridge/fakebridge_test.go
  - test/e2e/mention_kinds_test.go
  - AGENTS.md
  - docs/messengers/bridge.md
  - docs/messengers/slack.md
  - docs/concepts/mentions.md
acceptance:
  - "[C-24.FR-6, C-25.FR-8, C-28.FR-2] In Slack a user group entry and a Team mention are written as `<!subteam^ID>`, in the first line of a Loud message, Root message or Thread reply alike; the fake Slack server records them (`layout_test.go`, `mention_kinds_test.go`)."
  - "[C-22.FR-16, C-25.FR-8, C-28.FR-2] Through a bridge that declares group mentions and `here`, a group entry, a Team mention and `here` are entries of the envelope's `mentions` with the `kind` `group` or `everyone` — `external_id` and `display_name` carrying the group's name or the symbolic name — placed in the body by the Mention marker; a person stays of the kind `user`, the default; a bridge that declares none receives no such entry (`envelope_test.go`, `mention_kinds_test.go`)."
  - "[C-22.FR-16] `api/bridge.yaml` gives `Mention` the optional `kind` (`user`, the default; `group`; `everyone`), additively in protocol version 1; `make lint` passes Redocly and oasdiff reports no breaking change; the fake bridge records the `kind` of each Mention in `/_fake/messages` (`fakebridge_test.go`)."
  - "[C-22.FR-16] The Bridge page of the documentation says how a group and a mention of everyone arrive in `mentions`; `make docs-check` passes."
verify: "make ci e2e-go docs-check"
operator_attention: false
issue: 460
---

# S-171. Group mentions in Slack, and group mentions and mentions of everyone through bridges, with the kind of a Mention in the Bridge protocol (BE)

## Scope

**IN**

- Writing the group and everyone targets that S-140 resolves where L1 had no form for them: in Slack, `<!subteam^ID>`
  for a user group or a Team mention (C-24.FR-6 revised), Slack having no `everyone` entry (C-28.FR-2, F-127); through a
  bridge, entries of the envelope's `mentions` of their own kind (C-22.FR-16 revised), with `Mention.kind` added to
  `api/bridge.yaml` (ADR-0017).
- The fake bridge recording that kind.

**OUT**

- The entries, their validation and their resolution, and Mattermost's `@group`: S-140. Undelivered Mentions: S-165.
- The libraries' types for `Mention.kind` and the `info` fields of S-139: S-141, S-142, which follow this story.
- The documentation of whom a message mentions: S-144.

## Contracts

- **Protocol** (`api/bridge.yaml`, MIT): `Mention` gains the optional `kind` (`user`, the default; `group`; `everyone`);
  for `group` and `everyone`, `external_id` and `display_name` carry the group's name or the symbolic name (`channel`,
  `here`, `all`, `everyone`), so the required fields stay as they are; Muster sends them only to a bridge that declared
  them (S-139); additive in version 1.
- **Mentions** (`internal/mentions/mentions.go`): a resolved target carries what the adapters need — the Slack user
  group's id, the group's name, the symbolic name — and the Slack adapter no longer leaves user groups out, nor the
  Bridge adapter group and everyone targets (S-140's Contracts).
- **Slack** (`internal/slack/layout.go`): `<!subteam^{id}>` in the first line of a Loud message, with its other
  Mentions; Quiet messages and edits carry no Mentions (ADR-0005). `<!subteam^{id}>` in a `section` block of a Root
  message and of a Thread reply mentions the user group's members (F-126, which sent it in the blocks only, not in
  `text`). Slack has no `everyone` entry (C-28.FR-2): `<!everyone>` from a bot mentions only in the workspace's general
  channel (F-127), where `<!channel>` reaches the same people, so the adapter never writes it.
- **Bridge** (`internal/bridge/envelope.go`): `kind` omitted on a `user` entry, so that a bridge that declares no group
  or everyone mentions never sees the new field; `mentions` entries of the kind `group` or `everyone`, each with its ref
  and marker, beside the `user` entries of C-22.FR-16.
- **Fake bridge** (`internal/fakes/fakebridge/messages.go`): each recorded message keeps the `kind` of its `mentions`.
- **AGENTS.md**: the paragraph on development mode says that the fake bridge records the `kind` of each Mention.
- **Settings, log events, metrics**: none new.
- **Documentation**: `docs/messengers/bridge.md` — "What Muster posts" names the kinds of `mentions`.

## Steps

1. Add `kind` to `Mention` in `api/bridge.yaml`. Check: `make generate-check` (the models of `internal/bridge/gen/`);
   `make lint`; the two comparisons of the CI step "oasdiff breaking, api/bridge.yaml" (`.github/workflows/ci.yml`) run
   locally — `oasdiff breaking --fail-on ERR --flatten-allof` of `origin/master:api/bridge.yaml` against the new file,
   and the reverse with the step's `--severity-levels` — report no ERR: run on a draft of this change with oasdiff
   v1.33.0 in the plan review, the reverse direction reports `request-property-removed` for `mentions/items/kind` on
   `POST /v1/messages` and its Thread replies as WARN only. It would still break a bridge that refuses unknown fields,
   so Muster writes `kind` only where a bridge asked for it: a `user` Mention is sent without `kind`, which defaults to
   `user`, and the kinds `group` and `everyone` go only to a bridge that declares `group_mentions` or the token in
   `everyone_mentions` (S-139), which S-140's validation and resolution already ensure.
2. Write the Slack and Bridge forms and the fake bridge's record. Check: `layout_test.go`, `envelope_test.go`,
   `mentions_test.go`, `fakebridge_test.go`.
3. Write `test/e2e/mention_kinds_test.go` and the documentation. Check: the Verification below; `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-139 and S-140: A, NOTIFY, PUTA, AG; the Team "payments" (TP) with the Team mentions payments-team
# (S0FAKEPAYTEAM) for the demo Slack Connection (CS), after its check found usergroups:read, and payments_team for the
# demo Bridge Connection (CB), whose fake bridge declares group mentions and "here"; the Route "Infrastructure" (R) with
# the demo Slack Destination (DS) and the demo Bridge Destination (DB), each with the entries "Team from the label"
# and here for new_alert_group.
FAM=127.0.0.1:19093/_fake; FSL=127.0.0.1:18097/_fake; FB=127.0.0.1:18094/_fake
curl -s -X PUT $FAM/groups/k1 -d '{"receiver":"sev","route":"{}","labels":{"alertname":"QueueBacklog"}}' > /dev/null
PUTA k1 a '{"labels":{"category":"infra","team":"payments","severity":"warning"}}'; NOTIFY k1 '{"reason":"first notification"}'; sleep 2
curl -s $FSL/messages | jq -r 'last | tostring | test("<!subteam\\^S0FAKEPAYTEAM>") and test("<!here>")'            # true
curl -s $FB/messages | jq -c 'last | [.envelope | .. | objects | select(has("external_id")) | {kind, external_id}]'
# [{"kind":"group","external_id":"payments_team"},{"kind":"everyone","external_id":"here"}]
```

`test/e2e/mention_kinds_test.go` repeats this and adds a person through the bridge (kind `user`), and a
bridge that declares no group mentions, whose envelope carries no `group` entry.

## Open questions

None. What `<!subteam^ID>` mentions is recorded by the spike S-138 (F-126); `<!everyone>`, which mentions only in the
workspace's general channel (F-127), is not offered in Slack (C-28.FR-2, the maintainer's decision on S-138).

## Notes

- `files_touched` gained `internal/bridge/delivery.go`, whose Publish and Reply hand the info of the Connection to the
  envelope's Mentions, and `docs/messengers/slack.md` and `docs/concepts/mentions.md`, which said that Muster does not
  write user groups, group mentions and mentions of everyone yet; S-144 still rewrites both pages.
- A new rule of this story: the Mention line of a Bridge message keeps, in their order, the whole Mentions that fit
  half of `limits.max_body_length`, leaving out each one that does not fit, with its entry, so that a long line can
  neither cut a marker nor crowd out the text: the same budget as Slack's Thread-reply Mentions, half of its length
  limit; unlike Slack's `fitMentions`, it skips a Mention that does not fit. Neither C-12.FR-11 nor `defaults.md` has
  it yet; the maintainer decides in the pull request.
- Suggested commit: `feat(mentions): write group mentions in slack, and group and everyone mentions through bridges`.
- Split from S-140 by the review, at the boundary between deciding whom a message mentions and writing it in the
  messengers that L1 could not write it for; it takes the next free ID, S-171, follows S-140, and the bridge libraries
  S-141 and S-142 follow it, since it changes `api/bridge.yaml`. No migration.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-28.FR-2 | partial | writing group mentions in Slack, and group and everyone mentions through bridges; the validation is S-140 |
| C-25.FR-8 | partial | Team mentions in Slack and through bridges; the rest is S-139 and S-140 |
| C-22.FR-16 | partial | revised for L2: group and everyone entries in `mentions`; the record of the plain name is S-165 |
| C-24.FR-6 | full | revised for L2: `<!subteam^ID>`; no `<!everyone>` |

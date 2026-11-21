---
id: S-231
title: Test runs of a Destination with their steps, the people a step would mention, expiry and the Audit log, for Mattermost (BE)
capability: C-42
kind: be
layer: L2
depends_on: [S-047, S-061, S-114, S-125, S-140, S-049, S-068, S-198]
covers: [C-42.FR-1, C-42.FR-2, C-42.FR-3, C-42.FR-4, C-42.FR-5, C-42.FR-6, C-42.FR-10, C-42.AC-1, C-42.AC-4, C-42.AC-5, C-42.AC-8, C-42.AC-10]
files_touched:
  - api/openapi.yaml
  - api/README.md
  - internal/db/migrations/00NN_destination_test_runs.up.sql
  - internal/db/migrations/00NN_destination_test_runs.down.sql
  - internal/db/migrate_test.go
  - design/db/schema.md
  - internal/publicid/publicid.go
  - internal/publicid/publicid_test.go
  - internal/destinations/query.sql
  - internal/destinations/testruns.go
  - internal/destinations/testruns_test.go
  - internal/destinations/testruns_integration_test.go
  - internal/destinations/testmentions.go
  - internal/destinations/testmentions_test.go
  - internal/destinations/test.go
  - internal/delivery/teststeps.go
  - internal/delivery/teststeps_test.go
  - internal/delivery/destinationtest.go
  - internal/messages/teststeps.go
  - internal/messages/teststeps_test.go
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/mattermost/teststeps.go
  - internal/mattermost/teststeps_test.go
  - internal/api/destinations.go
  - internal/api/destinations_test.go
  - internal/api/server.go
  - internal/runtime/runtime.go
  - internal/logging/events.go
  - internal/fakes/fakemattermost/posts.go
  - internal/fakes/fakemattermost/fakemattermost_test.go
  - test/e2e/destination_test_runs_test.go
acceptance:
  - "[C-42.FR-1, C-42.AC-5] `createDestinationTestRun` (`POST /destinations/{id}/test-runs`) takes a source — a recent Alert Group of one of the Destination's Routes or the built-in example, as `testDestination` does — and a Severity level, by default the source's, makes the run, a row with its `org_id`, and answers it with the result of its first step; several runs, of one Admin or of several, go on at once on one Destination and share nothing; a run ends when its last step is made, on `deleteDestinationTestRun`, or once it has made no step for `destination_test.idle_timeout` (1 h) on the business clock, ended lazily — the next step, read or new run of that Destination that finds it expired deletes it, with no Leader task — and a step or read of an ended run is `404`; with the development clock advanced by 61 minutes the next step gets `404`, the row is gone and its Test messages stay in the fake server (`testruns_test.go`, `testruns_integration_test.go`, `test/e2e/destination_test_runs_test.go`)."
  - "[C-42.FR-2, C-42.AC-1] `createDestinationTestRunStep` (`POST …/test-runs/{run_id}/steps`) makes the next step of a Mattermost run in this order: Created, New Alert, Acknowledge, Reminder, Takeover, Snooze, Resolve, Unresolve, each a valid transition of the step before it; the run ends with the eighth, and a ninth call is `404`; the first is the Publication of the Root message, Loud, marked \"🧪 Test message\", the second an edit to two Alerts with a Thread reply, Acknowledge an edit by the Admin who runs the test as the Owner — by \"Example person\" when a Service account runs it — with the buttons of an acknowledged Root message from the Destination's button set, Reminder a Thread reply to the Owner with \"Still on it\" and \"Unack\", Takeover by \"Example person\" (\"Second example person\" for a Service account) naming the previous Owner as C-28.FR-4 says, Snooze for the first Snooze duration of the Destination's button set or 1 hour, Resolve by the Admin, and Unresolve Firing again with no Owner; each sends what delivery sends for that lifecycle event, the edit, the reply or both as the Loud and Quiet table says, with the compact layout and the button set, and never on its own (`teststeps_test.go` of `messages` and `delivery`, `testruns_test.go`, `test/e2e/destination_test_runs_test.go`)."
  - "[C-42.FR-3, C-42.AC-1] Every step goes through the interactive path under `delivery.interactive_budget`, and its result shows each request with Secrets, the Signing secret and tokens masked, the answer truncated, as untrusted text and masked, the duration and the error class of `DeliveryErrorClass`; a step that gets no limiter token in time ends `limited` and may be tried again; a failed step stops the run: the next call of `createDestinationTestRunStep` makes that same step again, which is \"Try again\", until it succeeds or the run is ended; the first step counts as a Test message for C-16.FR-7: when it succeeds on a Broken Destination its Broken state ends, and a failed one changes nothing (`teststeps_test.go`, `testruns_test.go`, `test/e2e/destination_test_runs_test.go`)."
  - "[C-42.FR-4, C-42.AC-4, C-42.AC-10] For each Loud step the result lists whom the Destination's Mention entries for that event would mention — each person, messenger group, Team and On-call by name — and each entry that would mention nobody with its reason, such as \"has no linked Mattermost account\", and for the Reminder and the Takeover the Owner and the previous Owner that L1 mentions whatever the entries; the Test message carries no Mention markup in the fake Mattermost server — the names stand after \"Would mention:\" as plain text — a step sends no Mention of an entry or of an Owner, no \"Mention not delivered\" is recorded and no warning of the Destination changes (`testmentions_test.go`, `testruns_integration_test.go`, `test/e2e/destination_test_runs_test.go`)."
  - "[C-42.FR-5] The buttons of the Test message are those its status shows from the Destination's button set; the presses on them are S-234's, and until it merges the run's result lists none (`teststeps_test.go`)."
  - "[C-42.FR-6, C-42.AC-1] A run writes no Alert Group, Alert, Timeline, Note or Assignee — `internal/groups` writes nothing (lint 2) —, its Test messages are never reconciled, it adds no row of `deliveries`, `thread_replies` or `webhook_events`, and `muster_delivery_attempts_total`, `muster_commands_total` and `muster_pages_total` stay as they were while its requests count only in `muster_client_requests_total` with the client `interactive`; its Test messages stay in the Destination when it ends and Muster never deletes them (`testruns_integration_test.go`, `test/e2e/destination_test_runs_test.go`, `make lint-arch`)."
  - "[C-42.FR-10, C-42.AC-8] The four operations need `destinations:test`, from the web session or a token: a Responder and a Viewer get `403` on each, without authentication `401`; each step that sends is recorded in the Audit log as `destination.tested` with the run and the step, and ending or expiring a run records nothing; a run started with an Admin Service account's token acknowledges and takes over as the example people; a Destination of a type without a stepper in this build answers `501` until S-232 and S-233 merge (`destinations_test.go`, `testruns_test.go`, `test/e2e/destination_test_runs_test.go`)."
  - "[C-42.FR-1, C-42.FR-6] `getDestinationTestRun` reads the run with its source, Severity level, the steps made with their results and, from S-234, its presses; a run of another Destination's id or of another Organization is `404` and every query filters by `org_id` (lint 1) (`testruns_test.go`, `make lint-arch`)."
verify: "make ci test-integration e2e-go"
operator_attention: false
issue: null
---

# S-231. Test runs of a Destination with their steps, the people a step would mention, expiry and the Audit log, for Mattermost (BE)

## Scope

**IN**

- The run as a row of the database, its four operations, the order of the eight steps with the interactive path and the
  masked results, the lazy expiry and the Audit log (C-42.FR-1 to FR-3, FR-6, FR-10).
- "Would mention:" for every Loud step with the reason of each entry that mentions nobody, and the Test message
  without any Mention markup (C-42.FR-4).
- The first Destination type, Mattermost, end to end, which fixes the shape of a stepper for the others.

**OUT**

- The steps for Telegram and Slack: S-232; for a Bridge and an outgoing webhook: S-233. Until they merge a run on those
  types is `501`, which the Notes name.
- The presses on the Test message and the bot's press of the first button: S-234.
- The accelerated run of the Escalation chains: S-235, S-236. The page: S-237, S-238. The documentation: S-239.
- The one-message test of C-16 (`testDestination`): unchanged.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated; each `x-permission: destinations:test`, `x-listener:
  app`): `createDestinationTestRun` (`POST /destinations/{destination_id}/test-runs`, the source and the Severity level
  as `TestSource` and `severity_level`, → `DestinationTestRun` with the result of its first step),
  `getDestinationTestRun` (`GET …/test-runs/{run_id}`), `deleteDestinationTestRun` (`DELETE …`, ends it) and
  `createDestinationTestRunStep` (`POST …/test-runs/{run_id}/steps`, no body, → `DestinationTestStepResult` of the next
  step). New schemas: `DestinationTestRun` (`id`, `source`, `severity_level`, `steps`, `next_step`, `presses`, `created_at`,
  `expires_at`), `DestinationTestStepResult` (the step's name as a `TestRunStepName` enum — `created`, `new_alert`,
  `acknowledge`, `reminder`, `takeover`, `snooze`, `resolve`, `unresolve` —, the requests as the existing `TestStep`
  shape with `DeliveryErrorClass`, `would_mention`, `buttons`), `WouldMention` (`entries`, each `{kind, name, reason}`).
  A call after a failed step makes that step again; a call after the last step finds the run ended, `404`, as a call on
  an expired or deleted one does. `api/README.md` says so.
- **Schema** (a new migration, expand only; the number is assigned by the orchestrator at dispatch; `schema.md` §4.6):
  `destination_test_runs` — `id`, `org_id`, `public_id` (a new prefix in `internal/publicid`), `destination_id`
  (`ON DELETE CASCADE`), `created_by_user_id` or `created_by_service_account_id`, `source` (jsonb), `severity_level`,
  `step_number`, `results` (jsonb: the steps made), `message_ref` (the Root message's identifier in its messenger, null
  before the first step), `created_at`, `last_step_at`; an index on `(org_id, destination_id)` and one on
  `(org_id, last_step_at)` for the lazy expiry. No CHECK replaces another; down drops the table. Rows are deleted by the
  lazy expiry, by `deleteDestinationTestRun`, with the Destination, and with the Organization's cascade.
- **Packages** (ADR-0016: flat CRUD, `org_id` in every query): `internal/destinations/testruns.go` is the service — create,
  step, read, end, the expiry on the business clock (injected), the Audit log row of each step — beside `test.go`, which
  it reuses for the source and the interactive limiter; `testmentions.go` computes "Would mention" from the Destination's
  Mention entries for the step's event through `internal/mentions` and the Account links, reading and sending nothing.
  `internal/delivery/teststeps.go` holds the stepper contract, declared by its consumer (`delivery.Stepper`: the step
  after the previous one, given the run and the source), the order of the steps, the masking and the class mapping of
  `destinationtest.go`; `internal/messages/teststeps.go` builds the `messages.Source` of each step (statuses, two Alerts,
  Owner, the example people, the Snooze) and renders it with the built-in texts in the Route's language
  (`route.language`; for the built-in example that of the first Route, by name, naming the Destination), adding two texts
  to `texts/en.json` and `ru.json` ("Test message" marker and "Would mention:"); `internal/mattermost/teststeps.go`
  implements the stepper for Mattermost — the create, the edit, the Thread reply and the buttons — with the mention
  markup stripped into plain text. A step records nothing in `deliveries`, `thread_replies` or any Timeline.
- **Log events** (`internal/logging/events.go`, the closed registry, regenerating the reference pages):
  `destination_test_run_started`, `destination_test_step_made` and `destination_test_run_ended`; no metric and no
  Internal alert is added.
- **Settings**: `destination_test.idle_timeout` (1 h on the business clock, `defaults.md`, decided);
  `delivery.interactive_budget` as C-16 uses it.
- **Fake server**: `internal/fakes/fakemattermost/posts.go` records the edits and Thread replies of a post in the order
  they came, if it does not, so that the e2e test reads the eight steps; its `/_fake/requests` and faults as before.

## Steps

1. Write the migration, `schema.md`, the public id prefix. Check: `make test-integration` runs up, down and up;
   `migrate_test.go`.
2. Write the spec parts and `api/README.md`. Check: `make generate-check`, `make lint`.
3. Write `messages/teststeps.go` and its texts. Check: `teststeps_test.go` of `messages` (eight sources in order, each
   a valid transition, the Service account's example people, the languages).
4. Write `delivery/teststeps.go` and `mattermost/teststeps.go`. Check: `teststeps_test.go` of both (the requests per step,
   masking, `limited`, a failed step stopping the run, no Mention markup).
5. Write `testmentions.go`. Check: `testmentions_test.go` (a person without an Account link, a Team, an On-call, a
   messenger group, the Reminder's and the Takeover's Owners).
6. Write `testruns.go`, the handlers, the log events and the wiring. Check: `testruns_test.go`,
   `testruns_integration_test.go` (expiry, `404`, parallel runs, no writes), `destinations_test.go`.
7. Write `test/e2e/destination_test_runs_test.go`. Check: the Verification below; `make lint-arch`.

## Verification

```sh
make dev > dev.log 2>&1 &
API=localhost:8080/api/v1; FMM=127.0.0.1:18065/_fake; CLK=localhost:8082/_dev/clock
# A: the Admin's header array, V: a Viewer's, SA: an Admin Service account's token, as in S-047
D=$(curl -s "${A[@]}" $API/destinations | jq -r '.items[] | select(.name=="infra-critical") | .id')
# C-42.AC-1: the run, step by step
R=$(curl -s -X POST "${A[@]}" $API/destinations/$D/test-runs -d '{"source":{"kind":"example"},"severity_level":"critical"}')
RID=$(jq -r .id <<<"$R"); jq -c '{s: .steps[0].name, w: .steps[0].would_mention.entries[].name}' <<<"$R"
# {"s":"created","w":"@devops"} ...
curl -s $FMM/posts | jq -r '.[] | select(.root_id=="") | .message' | head -3   # contains "🧪 Test message"
for i in 2 3 4 5 6 7 8; do curl -s -X POST "${A[@]}" $API/destinations/$D/test-runs/$RID/steps | jq -r '.name + " " + (.requests[0].status|tostring)'; done
# new_alert 200 / acknowledge 200 / reminder 200 / takeover 200 / snooze 200 / resolve 200 / unresolve 200
curl -s -o /dev/null -w '%{http_code}\n' -X POST "${A[@]}" $API/destinations/$D/test-runs/$RID/steps   # 404: the eighth step ended the run
# the Root message after each step as the fake server holds it, and the replies of the Thread, in order
curl -s $FMM/posts | jq -r '[.[] | select(.root_id!="")] | map(.message)[]' | sed 's/\xe2\x80\x8b//g'
curl -s localhost:8082/metrics | grep -E '^muster_(delivery_attempts|commands|pages)_total' | md5sum   # the same before and after
psql "$MUSTER_DB" -c "select count(*) from alert_groups where title like '%test%'"   # 0: no Alert Group was made
# C-42.AC-4 and AC-10: an entry naming a person without an Account link
# the Destination's Mention entries name olga, who has none: the first result says "olga: has no linked Mattermost account"
curl -s $FMM/posts | jq -r '.[0].message' | grep -c '@olga'        # 0: no Mention markup, the names are plain text after "Would mention:"
curl -s "${A[@]}" $API/destinations/$D | jq -c '.warnings'          # unchanged
# C-42.AC-5: a run left alone ends after the idle timeout
R2=$(curl -s -X POST "${A[@]}" $API/destinations/$D/test-runs -d '{"source":{"kind":"example"},"severity_level":"critical"}'); ID2=$(jq -r .id <<<"$R2")
curl -s -X POST $CLK -d '{"advance_seconds": 3660}'
curl -s -o /dev/null -w '%{http_code}\n' -X POST "${A[@]}" $API/destinations/$D/test-runs/$ID2/steps   # 404
psql "$MUSTER_DB" -c "select count(*) from destination_test_runs where public_id='$ID2'"               # 0
curl -s $FMM/posts | jq length                                                                         # the Test messages stay
# C-42.AC-8: Permissions, the Audit log, a Service account
curl -s -o /dev/null -w '%{http_code}\n' -X POST "${V[@]}" $API/destinations/$D/test-runs -d '{"source":{"kind":"example"}}'   # 403
curl -s "${A[@]}" "$API/audit-log?limit=10" | jq -c '[.items[] | select(.action=="destination.tested")] | length'              # one per step that sent
R3=$(curl -s -X POST "${SA[@]}" $API/destinations/$D/test-runs -d '{"source":{"kind":"example"}}'); jq -r .id <<<"$R3" > /dev/null
# step 3 by the Service account: "acknowledged by Example person"
make lint-arch
```

`test/e2e/destination_test_runs_test.go` repeats these through the generated client, runs two runs of two Admins in
parallel on one Destination and checks they share nothing, and fails a step by a scripted fault of the fake server
(`/_fake/faults`) to see the run stop and the retry succeed.

## Open questions

- **The public id prefix and the names of the schemas.** The PRD fixes the four paths and what they do, not the schema
  names or the prefix of a run's `public_id`; this story names them as above and adds the prefix to `internal/publicid`.
  Say if the review wants others before the generated clients carry them.
- **What a run stores of its messages.** C-42.FR-6 says that Test messages stay in the Destination and are never
  deleted; this story keeps the Root message's identifier in `message_ref` only while the run lives, which S-234 needs to
  match a press to its run, and the identifier of a Thread reply in the result. Confirm that nothing longer-lived is
  wanted.

## Notes

- Suggested commit: `feat(destinations): add step-by-step test runs of a destination with the people each step would mention`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. The migration number
  is assigned by the orchestrator at dispatch; the migration replaces no CHECK and needs no re-versioning.
- Between this story and S-233 a run on a Telegram, Slack, Bridge or outgoing webhook Destination answers `501`
  (`ErrNoTester`'s way: the build has no stepper of that type), as S-047's `testDestination` did between its parts; the
  page of S-237 follows S-233, so no one sees it on `edge` through the UI.
- A Test message never mentions people (C-42.FR-4): a test at night must not ring anyone's phone. The text after "Would
  mention:" uses the names as the Mention entries print them, without the `@` that Mattermost would turn into a Mention
  — the fake server's check in the Verification is the proof.
- A step is a valid transition of the step before it, so a Reminder comes right after Acknowledge: after Unresolve an
  Alert Group has no Owner to remind (C-42.FR-2). The end of a Snooze, an ack timeout notice and a rise to critical are
  not steps.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-42.FR-1 | partial | the run, its operations and expiry; the page's start form is S-237 |
| C-42.FR-2 | partial | the eight steps for Mattermost; Telegram and Slack are S-232, a Bridge and a webhook S-233 |
| C-42.FR-3 | partial | the interactive path, masking, `limited`, the failed step and the Broken state for Mattermost; the others are S-232, S-233 |
| C-42.FR-4 | partial | "Would mention" and the plain text for Mattermost; the others are S-232, S-233 |
| C-42.FR-5 | partial | the buttons from the button set; the presses are S-234 |
| C-42.FR-6 | partial | no writes and no counts, for Mattermost; the others are S-232, S-233 |
| C-42.FR-10 | partial | the Permission and the Audit log of a step; playing the chains is S-235, S-236 |
| C-42.AC-1 | full | |
| C-42.AC-4 | partial | the entry that would mention nobody, on Mattermost; Telegram and the others are S-232, S-233 |
| C-42.AC-5 | full | |
| C-42.AC-8 | partial | the Permissions, the Audit log of a step and the Service account; playing the chains adds none (S-235, S-236) |
| C-42.AC-10 | partial | no Mention markup in Mattermost; playing the chains adds no reply (S-236) |

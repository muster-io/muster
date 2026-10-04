# C-16. Destination test

[L1 index](../L1.md) · Stage: Shadow · UI: yes · Depends on: C-13, C-14, C-15

**Goal.** Let an Admin check a Destination before relying on it, without touching real Alert Groups.

## Scenarios

1. The Admin sends a test message to a new Mattermost Destination; a post marked 🧪 appears, and the UI shows the
   request, the response, the status and whether a press of its button, which Muster makes through the bot, reached
   Muster.
2. A test of an outgoing webhook shows the values extracted from the response, with its Secrets masked.
3. The Admin previews how a recent Alert Group would look in a Destination without sending anything.
4. A Destination is Broken because the bot was removed from the channel. The Admin adds the bot back and sends a test
   message; it arrives, and the Destination is healthy again.
5. Muster runs on an internal address that the Mattermost server does not allow. The test message is posted, but the
   result says that its button press did not reach Muster and names `AllowedUntrustedInternalConnections`; once the
   Mattermost admin adds the address, the next test reports that presses arrive.

## Functional requirements

- **C-16.FR-1** "Send test message" makes one "create" request to the chosen Destination, built from a recent Alert
  Group of one of its Routes or from a built-in example, marked "🧪 Test message", through the interactive path
  (C-11.FR-2). For an outgoing webhook it sends the event `test` (C-15.FR-2) in events mode and the "create" request in
  template mode — both, one after the other, when the Destination uses both.
- **C-16.FR-2** The result shows the request (Secrets, the Signing secret and tokens masked), the response status and
  body (truncated, shown as untrusted text, masked), extracted values, duration and the error class. When no limiter
  token is free within `delivery.interactive_budget`, nothing is sent and the step's error class is `limited`.
- **C-16.FR-3** Test messages are never reconciled and never change an Alert Group; pressing a button on one is answered
  privately with "This is a test message; nothing was changed". A test of a Mattermost Destination also checks the
  path of a press: once the test message is posted, Muster presses its first button itself through the bot
  (`POST /api/v4/posts/{post_id}/actions/{action_id}`, [F-054](../../facts.md#button-presses-and-answers)), as a second
  request on the interactive path, and a separate step of the result, after the message, says whether the press reached
  Muster's callback (C-13.FR-4). When it
  did not — the Mattermost server cannot call `MUSTER_INGEST_URL` and answers the bot with "Action integration error" —
  the result says to add the host of `MUSTER_INGEST_URL` to `ServiceSettings.AllowedUntrustedInternalConnections` on the
  Mattermost server, the most common cause, which only the Mattermost server log names (C-13.FR-7, C-13.FR-13,
  [F-022](../../facts.md#button-presses-and-answers)). Muster recognises its own press by the test message's action id
  and the bot's `user_id`, and answers it with no private message.
- **C-16.FR-4** "Preview" renders the Root message (or webhook request) for a chosen Alert Group or the example without
  sending it.
- **C-16.FR-5** Sending a test message is recorded in the Audit log.
- **C-16.FR-6** Testing the full lifecycle on a test message (update, Thread replies, commands) is not part of L1.
- **C-16.FR-7** A successful test of a Broken Destination ends its Broken state and resolves `MusterDestinationBroken`,
  like a successful probe (C-11.FR-9); a failed test changes nothing.

## UI

"Test" and "Preview" on every Destination page, with the source selector and the result panel.

## API surface

`destinations/{id}/tests` (create → result); `destinations/{id}/previews` (create → rendered output).

## Acceptance

- **C-16.AC-1** A test message appears in the fake messenger with the 🧪 mark, and no Alert Group's Timeline changes.
- **C-16.AC-2** A test against an unreachable endpoint returns within `delivery.interactive_budget` with the error
  class.
- **C-16.AC-3** A test of an outgoing webhook shows the extracted values and shows every Secret value as masked.
- **C-16.AC-4** Pressing a button on a test message in the fake Mattermost or Telegram server changes nothing and is
  answered with "This is a test message; nothing was changed".
- **C-16.AC-5** A successful test of a Broken Destination makes it healthy and resolves `MusterDestinationBroken`; a
  failed test leaves it Broken.
- **C-16.AC-6** A test of an outgoing webhook in events mode delivers one `test` event with `version` `1` and
  `test: true`, which the fake endpoint verifies with the Signing secret; no Alert Group's events change.
- **C-16.AC-7** A test of a Mattermost Destination presses the test message's button through the fake Mattermost
  server's actions endpoint. When the fake server delivers the press to Muster's callback, the result says that presses
  reach Muster and no ephemeral post is sent; when it answers "Action integration error" instead, the result says that
  the press did not reach Muster and names `AllowedUntrustedInternalConnections`.

## Related ADRs

ADR-0005, ADR-0015.

## Depends on

C-13, C-14, C-15 — the Destination types under test.

## Suggested story split

- **BE** — test and preview operations for each Destination type, the `test` event, ending the Broken state, masking,
  Audit log.
- **FE** — "Test" and "Preview" panels on the Destination page.

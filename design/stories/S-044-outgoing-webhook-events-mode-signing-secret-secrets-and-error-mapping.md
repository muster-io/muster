---
id: S-044
title: Outgoing webhook events mode, Signing secret, Secrets and error mapping (BE)
capability: C-15
kind: be
layer: L1
depends_on: [S-039]
covers: [C-15.FR-1, C-15.FR-2, C-15.FR-5, C-15.FR-6, C-15.FR-8, C-15.FR-9, C-15.FR-10, C-15.FR-11, C-15.FR-12, C-15.AC-1, C-15.AC-2, C-15.AC-4, C-15.AC-5, C-15.AC-6, C-15.AC-7, C-15.AC-8, C-15.AC-9, C-15.AC-10, C-15.AC-11, C-15.AC-12, C-15.AC-13, C-11.FR-6, C-11.FR-9, C-11.FR-14, C-11.FR-18, C-11.FR-19, C-12.FR-8, C-01.FR-13, C-11.FR-8]
files_touched:
  - internal/webhooks/destination.go
  - internal/webhooks/signing.go
  - internal/webhooks/secrets.go
  - internal/webhooks/body.go
  - internal/webhooks/mentions.go
  - internal/webhooks/adapter.go
  - internal/webhooks/query.sql
  - internal/webhooks/signing_test.go
  - internal/webhooks/secrets_test.go
  - internal/webhooks/body_test.go
  - internal/webhooks/adapter_test.go
  - internal/delivery/webhookevents.go
  - internal/delivery/enqueue.go
  - internal/delivery/broken.go
  - internal/delivery/membership.go
  - internal/delivery/query.sql
  - internal/delivery/webhookevents_test.go
  - internal/delivery/membership_test.go
  - internal/destinations/write.go
  - internal/destinations/delete.go
  - internal/destinations/write_test.go
  - internal/api/destinations.go
  - internal/api/destinations_test.go
  - internal/fakes/fakewebhook/fakewebhook.go
  - internal/fakes/fakewebhook/fakewebhook_test.go
  - internal/devmode/devmode.go
  - internal/leader/tasks.go
  - internal/runtime/runtime.go
  - internal/logging/events.go
  - internal/archlint/secretleak.go
  - docs/outgoing-webhooks/events.md
  - test/e2e/webhook_events_test.go
acceptance:
  - "[C-15.FR-1, C-15.FR-5, C-15.AC-1, C-11.FR-18, C-01.FR-13] `createDestination` of type `webhook` in mode `events` stores its URL and header templates, proxy, Mention settings and limiter (`destination.webhook.limiter`) and returns its Signing secret once in `signing_secret`; every request carries `webhook-id`, `webhook-timestamp` and `webhook-signature`, which the fake endpoint verifies with that secret."
  - "[C-15.FR-5, C-15.AC-2] After `generateSigningSecret` the new secret is shown once and requests carry two signatures — new and previous — while the Destination shows the warning `previous_signing_secret_active` with its date; after `retirePreviousSigningSecret` they carry one; master key rotation does not change Signing secrets."
  - "[C-15.FR-2, C-15.AC-11] Every row of the lifecycle event tables produces exactly one events-mode request whose `event` is the row's name and whose `notify` is its loudness, Quiet rows and rows with nothing to show in a messenger included, with `version` 1, `sequence` the Alert Group's lifecycle event number, `actor`, `alert_group` and `alerts[]` (a table-driven test; the C-17 rows set up directly); delivery events are never sent."
  - "[C-15.FR-2, C-15.AC-6] `created`, `acknowledged` and `resolved` of one Alert Group arrive in this order, each with its own `webhook-id`; when the endpoint answers `acknowledged` once with `503` and `Retry-After`, it is retried with the same `webhook-id` and `resolved` is not sent before it succeeds; events of another Alert Group do not wait."
  - "[C-15.FR-6, C-15.AC-7, C-11.FR-8] `400`, `413` and `422` end that event as Not delivered with the Destination healthy, and the next event of the Alert Group follows; `404` makes the Destination Broken."
  - "[C-15.FR-6, C-15.AC-5] A `302` ends that event as Not delivered, the redirect is not followed, and the error names the redirect target."
  - "[C-15.FR-8, C-15.AC-4] A URL that resolves to `169.254.169.254` is refused under the standard and under the strict policy, with an error naming the link-local rule, and no request leaves Muster."
  - "[C-15.FR-10, C-15.AC-8] A Secret used in a header reaches the endpoint, and its value appears neither in the Destination as read by a Viewer, nor in the Timeline error of a failed delivery, nor in any log line; a header `Authorization` with a literal value gives the warning `literal_credential` naming the header."
  - "[C-15.FR-2, C-15.FR-11, C-15.AC-12, C-12.FR-8] With Mention settings that mention Alice for new Alert Groups, the `created` event carries `mentions` with Alice's id, name and login, and an `alerts_added` event of an acknowledged Alert Group carries none."
  - "[C-15.FR-2, C-15.AC-9, C-11.FR-19] After a Broken period, every event that came due during it is delivered, in order per Alert Group, none dropped and with no age limit."
  - "[C-15.FR-2, C-15.AC-10, C-11.FR-6] During a Storm of 30 new Alert Groups on a Route with threshold 20, an events-mode Destination of that Route receives 30 `created` events."
  - "[C-15.FR-12, C-15.AC-13, C-11.FR-14] Deleting an events-mode Destination while its endpoint is down with three events queued sends nothing more to the endpoint, ends the three events as Not delivered and leaves the Destination with no Signing secret and no Secrets."
  - "[C-15.FR-9] The documentation of the events mode covers every event name and the body schema of version 1, the versioning rule, ordering per Alert Group, dropping duplicates by `webhook-id`, and signature verification with examples in Go, Python and a shell."
  - "[C-11.FR-9] An outgoing webhook has no Destination check: `checkDestination` answers 422 `check_not_supported`, and the Broken probe attempts the oldest waiting event, or — with nothing waiting — attempts the next event at once when it comes due."
verify: "make ci test-integration e2e"
operator_attention: false
issue: 44
---

# S-044. Outgoing webhook events mode, Signing secret, Secrets and error mapping (BE)

## Scope

**IN**

- Outgoing webhook Destinations in the mode `events`: the request templates, proxy, Mention settings and limiter.
- The events queue: one request per lifecycle event, in order per Alert Group, at least once, never collapsed, never
  subject to Storms, kept through Broken periods; the version 1 body with Mentions as data.
- The Signing secret in the Standard Webhooks style with regeneration and retirement; named Secrets with masking and the
  literal-credential warning.
- The response mapping, the outbound address policy and proxy, deletion with abandoned events and wiped secrets.
- The fake receiving endpoint and the documentation of the events mode.

**OUT**

- The mode `template` and `both`, extraction rules, threads, Storm summaries through templates, template errors
  (S-045); the `test` event (S-047); the form (S-046).

## Contracts

- **Operations implemented**: `createDestination`, `updateDestination` for the type `webhook` with `mode` `events`
  (`template` and `both` answer `422 unsupported` at `/mode` until S-045); `listDestinationSecrets`,
  `setDestinationSecret`, `deleteDestinationSecret`; `getSigningSecret`, `generateSigningSecret`,
  `retirePreviousSigningSecret`; `checkDestination` answers `422 check_not_supported` for a webhook. Schemas:
  `WebhookDestination(Base, Input)`, `WebhookEventsConfig`, `HeaderTemplate`, `DestinationWarning`, `DestinationSecret`,
  `DestinationSecretList`, `DestinationSecretInput`, `SigningSecretStatus`, `SigningSecretGenerated`,
  `DestinationCreated.signing_secret`; the `webhooks` entry `deliverAlertGroupEvent` with the body
  `OutgoingWebhookEvent` and `WebhookEventName`, `WebhookMentionTarget`, `WebhookActor`, `WebhookUser`,
  `WebhookAlertGroup`, `WebhookAlert`.
- **Destination** (C-15.FR-1; `destinations.webhook_*`, `internal/webhooks/destination.go`): `mode`; `events` — `url`
  and `headers`, Go templates rendered in the sandbox of S-036 with `.Secrets`, parsed and dry-run on save (`422` with
  `line` and `column`); `proxy` (the Destination's own, `internal/proxyconf`); `mentions` (every choice accepted:
  Mentions are data); `limiter` (default `destination.webhook.limiter`). No Connection and no Destination check.
- **Signing secret** (C-15.FR-5, ADR-0011; `signing.go`): `whsec_` and 32 random bytes in base64, generated at creation
  and returned once in `DestinationCreated.signing_secret`; encrypted in `signing_secret_*`. Every request carries
  `webhook-id` (the event's id), `webhook-timestamp` (Unix seconds of the real clock of S-006, which the signature
  covers) and `webhook-signature` `v1,<base64 HMAC-SHA256 of "{id}.{timestamp}.{body}">`. `generateSigningSecret` moves
  the current secret to `previous_signing_secret_*` with `previous_signing_secret_since` and returns the new one once
  (`201`); while a previous secret exists the header holds both signatures separated by a space and the Destination
  carries the warning `previous_signing_secret_active` with `since`; `retirePreviousSigningSecret` wipes it. Audit log
  entries `destination.signing_secret_generated` and `.signing_secret_retired`. Key rotation re-encrypts these values
  but never changes them.
- **Secrets** (C-15.FR-10, C-03.FR-21; `destination_secrets`, `secrets.go`): names are template identifiers; values are
  write-only; `listDestinationSecrets` returns names with `set` and `updated_at` and an `ETag`; `setDestinationSecret`
  and `deleteDestinationSecret` take an optional `If-Match`; all three answer `409 not_webhook_destination` for other
  types. Templates read them as `{{ .Secrets.<name> }}`; a reference to a missing Secret is a template error. Secret
  values and the Signing secrets are registered for redaction, so test results, previews, delivery errors, the Timeline
  and logs show `[redacted]`. Reads show the URL and header templates as written, with references only. A header named
  `Authorization`, or a URL whose query or user information holds a literal value instead of a `.Secrets` reference,
  adds the warning `literal_credential` with the JSON Pointer of the field.
- **Events queue** (C-15.FR-2, ADR-0005; `webhook_events`, `internal/delivery/webhookevents.go`): `delivery.Enqueue`
  also inserts, for each events-mode Destination of the Alert Group's Route, one row per lifecycle event — every row of
  the tables, Quiet ones and those with nothing to show in a messenger included, never a delivery event — with
  `sequence` = the event's `event_seq`, a new `webhook_id`, `notify` = Loud, `occurred_at`, and the version 1 body
  rendered at that moment. Only the head row of each Alert Group and Destination is claimed (`design/db/schema.md` §5);
  the next waits until it is `delivered` or `not_delivered`; other Alert Groups do not wait. Retries keep the
  `webhook_id` and the body. The events mode is never collapsed and never subject to Storms; a Destination added to a
  Route later receives events from then on.
- **Body** (C-15.FR-2, FR-11; `body.go`, `mentions.go`): `version` 1, `event`, `notify`, `mentions` — the targets of
  S-037's `Resolve` as `{"type":"everyone"}`, `{"type":"group","name":…}` or `{"type":"user","id":…,"name":…,
  "login":…}` for a Loud event and `[]` for a Quiet one — `sequence`, `occurred_at`, `actor` (a User or Service account
  with the Transport, or `system` with the reason), `alert_group` (number, `id`, title, `summary`, status, Owner, Route,
  Urgent, Severity level, times, URL) and `alerts[]`, as `OutgoingWebhookEvent` says; `Content-Type:
  application/json`.
- **Response mapping** (C-15.FR-6; `adapter.go`): `2xx` → delivered; `429` or `503` with `Retry-After` →
  `retry_after`; `408`, other `5xx`, timeouts and network errors → `transient`; `401`, `403`, `404`, `410` → `fatal`;
  `400`, `413`, `422`, every redirect (never followed; "redirect to {target} refused") and any other status → `unknown`,
  which ends that event `not_delivered` and lets the next one go. A request blocked by the outbound address policy
  (C-15.FR-8) is `fatal` (C-15.FR-6): the Destination turns Broken with the rule as the reason, because every later
  request would be blocked too.
- **Broken and recovery** (C-11.FR-9, FR-19; `internal/delivery/broken.go`): while the Destination is Broken its events
  stay `pending`, none dropped and no age limit; the probe attempts the oldest waiting event, and with nothing
  waiting it marks the Destination so that the next event is attempted as soon as it is due (C-11.FR-9); after recovery
  the
  events go out in order per Alert Group.
- **Deletion** (C-15.FR-12, C-11.FR-14; `internal/destinations/delete.go`): deleting an events-mode Destination ends its
  `pending` events as `not_delivered` ("the Destination was deleted") in the same transaction, sends no final event,
  and wipes the Signing secrets, the Secrets and the proxy password at once.
- **Retention**: a Leader task deletes `delivered` and `not_delivered` `webhook_events` older than
  `retention.alert_details`, in batches.
- **Metrics and log events**: `muster_delivery_attempts_total{kind="webhook_event"}` and the latency of S-034;
  `webhook_event_not_delivered` (WARN: `destination`, `group`, `event`, `status`).
- **Fake receiving endpoint** (C-01.FR-13; `internal/fakes/fakewebhook`, `127.0.0.1:18093`, started by `muster dev`):
  `POST /hook/{name}` and any method under `/hook/{name}/…` record method, path, headers, body and arrival time and
  answer `200` unless a fault of the harness scripts a status, `Retry-After`, a `Location` or a delay;
  `PUT /_fake/secrets/{name}` registers the secrets to verify with, and each record carries `signatures_valid` — how
  many signatures of `webhook-signature` verify, per Standard Webhooks — and `webhook_id`; `GET /_fake/received/{name}`
  lists the records.
- **Documentation** (C-15.FR-9; `docs/outgoing-webhooks/events.md`): the events mode, every event name and the body
  schema of version 1, the versioning rule (new names and fields within version 1; removing or changing one needs a new
  version), ordering per Alert Group and at-least-once delivery with dropping duplicates by `webhook-id`, `sequence`
  ("greater means newer"), signature verification with examples in Go, Python and a shell, regenerating and retiring the
  Signing secret, Secrets and the literal-credential warning.
- **Defaults**: `destination.webhook.limiter`; the events kept through a Broken period have no age limit.

## Steps

1. Write the fake receiving endpoint with signature verification. Check: `fakewebhook_test.go` verifies one and two
   signatures.
2. Write the Destination type, Secrets and the Signing secret with masking and warnings. Check: `secrets_test.go`,
   `signing_test.go`.
3. Write the body and Mentions as data. Check: `body_test.go` checks every lifecycle event row against the schema of
   `OutgoingWebhookEvent`.
4. Write the events queue with the head-of-line claim, the mapping, Broken and deletion. Check:
   `webhookevents_test.go` and `adapter_test.go` cover C-15.AC-6, AC-7, AC-9, AC-11 and AC-13.
5. Add the documentation, the secret probe, the retention task and the end-to-end test. Check: Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
# the Admin's session (`jar`, `H`), a Viewer's token in V, the Integration "lab" with NOTIFY, ADV and AG, the On-call
# policy in P, as in S-025 and S-032; Alice is a User
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake; FWH=127.0.0.1:18093
ALICE=$(curl -s -b jar "$API/user-directory?q=alice" | jq -r '.items[0].id')
M0='{"everyone":"none","user_ids":[],"groups":[]}'
M="{\"new_alert_group\":{\"everyone\":\"none\",\"user_ids\":[\"$ALICE\"],\"groups\":[]},\"new_alerts\":$M0,\"reopen\":$M0,\"ack_timeout\":$M0,\"snooze_ended\":$M0,\"rise_to_urgent\":$M0}"
EV() { curl -s $FWH/_fake/received/$1 | jq -c "$2"; }

# C-15.FR-1, FR-5, FR-10: create with a Secret in a header; the secret is shown once
CR=$(curl -s "${H[@]}" $API/destinations -d "{\"type\":\"webhook\",\"name\":\"auto\",\"mode\":\"events\",
  \"events\":{\"url\":\"http://127.0.0.1:18093/hook/auto\",\"headers\":[{\"name\":\"Authorization\",\"value\":\"Bearer {{ .Secrets.token }}\"}]},
  \"proxy\":{\"enabled\":false},\"mentions\":$M,\"limiter\":{\"limit\":5,\"per_seconds\":1}}")
D=$(jq -r .destination.id <<<"$CR"); SEC=$(jq -r .signing_secret <<<"$CR"); echo ${SEC:0:6}   # whsec_
curl -s "${H[@]}" -X PUT $API/destinations/$D/secrets/token -d '{"value":"s3cr3t-token-value"}' | jq -c '{name, set}'   # {"name":"token","set":true}
curl -s -X PUT $FWH/_fake/secrets/auto -d "[\"$SEC\"]" > /dev/null
curl -s "${H[@]}" $API/routes -d "{\"name\":\"wh\",\"matchers\":[{\"label\":\"team\",\"op\":\"=\",\"value\":\"wh\"}],\"urgent\":false,
  \"group_key\":[\"alertname\"],\"destination_ids\":[\"$D\"],\"policy\":$P}" > /dev/null

# C-15.AC-1, AC-12, AC-8: created carries Alice; the Secret reaches the endpoint; signatures verify
curl -s -X PUT $FAM/groups/w1 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"JobFailed"}}' > /dev/null
curl -s -X PUT $FAM/groups/w1/alerts/a -d '{"labels":{"team":"wh","job":"backup"}}' > /dev/null
NOTIFY w1 '{"reason":"first notification"}'; sleep 1; G=$(AG 'job%3D%22backup%22')
EV auto '.[0] | {event: .body.event, notify: .body.notify, v: .body.version, m: [.body.mentions[] | {type, login}], auth: .headers.Authorization, sig: .signatures_valid}'
# {"event":"created","notify":true,"v":1,"m":[{"type":"user","login":"alice"}],"auth":"Bearer s3cr3t-token-value","sig":1}

# C-15.AC-6: order, the same webhook-id on retry, nothing overtakes
curl -s -X POST $FWH/_fake/faults -d '{"path":"/hook/auto","status":503,"retry_after_seconds":2,"times":1}'
curl -s "${H[@]}" -X POST $API/alert-groups/$G/acknowledge > /dev/null
curl -s "${H[@]}" -X POST $API/alert-groups/$G/resolve > /dev/null; sleep 5
EV auto '[.[] | {e: .body.event, id: .webhook_id, s: .status}]'
# [{"e":"created","id":"msg_…1","s":200},{"e":"acknowledged","id":"msg_…2","s":503},{"e":"acknowledged","id":"msg_…2","s":200},{"e":"resolved","id":"msg_…3","s":200}]

# C-15.AC-12: an alerts_added of an acknowledged Alert Group carries no Mentions
curl -s -X PUT $FAM/groups/w2 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"Lag"}}' > /dev/null
curl -s -X PUT $FAM/groups/w2/alerts/a -d '{"labels":{"team":"wh","topic":"t1"}}' > /dev/null
NOTIFY w2 '{"reason":"first notification"}'; G2=$(AG 'topic%3D%22t1%22')
curl -s "${H[@]}" -X POST $API/alert-groups/$G2/acknowledge > /dev/null
curl -s -X PUT $FAM/groups/w2/alerts/b -d '{"labels":{"team":"wh","topic":"t2"}}' > /dev/null
NOTIFY w2 '{"reason":"new alerts added"}'; sleep 1
EV auto '[.[] | select(.body.event == "alerts_added")][0].body | {notify, mentions}'   # {"notify":false,"mentions":[]}

# C-15.AC-2: two signatures until the previous secret is retired
NEW=$(curl -s "${H[@]}" -X POST $API/destinations/$D/signing-secret | jq -r .secret)
curl -s -X PUT $FWH/_fake/secrets/auto -d "[\"$SEC\",\"$NEW\"]" > /dev/null
curl -s -b jar $API/destinations/$D | jq -c '[.warnings[] | .kind]'           # ["previous_signing_secret_active"]
curl -s "${H[@]}" -X POST $API/alert-groups/$G2/unacknowledge > /dev/null; sleep 1
EV auto '.[-1].signatures_valid'                                              # 2
curl -s "${H[@]}" -X DELETE $API/destinations/$D/signing-secret/previous > /dev/null
curl -s "${H[@]}" -X POST $API/alert-groups/$G2/acknowledge > /dev/null; sleep 1
EV auto '.[-1] | (.headers["webhook-signature"] | split(" ") | length)'      # 1

# C-15.AC-7, AC-5: 400, 413, 422 and a redirect are Not delivered; the Destination stays healthy
for st in 400 413 422; do
  curl -s -X POST $FWH/_fake/faults -d "{\"path\":\"/hook/auto\",\"status\":$st,\"times\":1}" > /dev/null
  curl -s "${H[@]}" -X POST $API/alert-groups/$G2/unacknowledge > /dev/null; sleep 1
  curl -s "${H[@]}" -X POST $API/alert-groups/$G2/acknowledge > /dev/null; sleep 1
done
curl -s -X POST $FWH/_fake/faults -d '{"path":"/hook/auto","status":302,"location":"http://elsewhere.example.org/x","times":1}' > /dev/null
curl -s "${H[@]}" -X POST $API/alert-groups/$G2/unacknowledge > /dev/null; sleep 1
curl -s -b jar "$API/alert-groups/$G2/timeline?kind=delivery&limit=1" | jq -c '.items[0] | {delivery_event, error}'
# {"delivery_event":"not_delivered","error":"redirect to http://elsewhere.example.org/x refused"}
curl -s -b jar $API/destinations/$D | jq -r .health.state                    # healthy
EV auto '[.[] | select(.body.event == "acknowledged")] | length >= 4'         # true   (each Not delivered let the next go)

# C-15.AC-8: the Secret appears nowhere but at the endpoint
curl -s -H "Authorization: Bearer $V" $API/destinations/$D | grep -c "s3cr3t-token-value"          # 0
curl -s -b jar "$API/alert-groups/$G2/timeline?kind=delivery" | grep -c "s3cr3t-token-value"      # 0
grep -c "s3cr3t-token-value" dev.log                                                                 # 0

# C-15.AC-4: the metadata address, under both policies
MD=$(curl -s "${H[@]}" $API/destinations -d "{\"type\":\"webhook\",\"name\":\"meta\",\"mode\":\"events\",\"events\":{\"url\":\"http://169.254.169.254/latest\",\"headers\":[]},
  \"proxy\":{\"enabled\":false},\"mentions\":$M,\"limiter\":{\"limit\":5,\"per_seconds\":1}}" | jq -r .destination.id)
R2=$(curl -s "${H[@]}" $API/routes -d "{\"name\":\"meta\",\"matchers\":[{\"label\":\"team\",\"op\":\"=\",\"value\":\"meta\"}],\"urgent\":false,
  \"group_key\":[\"alertname\"],\"destination_ids\":[\"$MD\"],\"policy\":$P}" | jq -r .id)
curl -s -X PUT $FAM/groups/m1 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"Meta"}}' > /dev/null
curl -s -X PUT $FAM/groups/m1/alerts/a -d '{"labels":{"team":"meta"}}' > /dev/null
NOTIFY m1 '{"reason":"first notification"}'; sleep 1
curl -s -b jar $API/destinations/$MD | jq -r .health.reason                   # blocked by the outbound address policy: 169.254.169.254 is link-local (always blocked)
psql "$MUSTER_DATABASE_URL" -qc "UPDATE outbound_policies SET policy = 'strict'"; sleep 11
curl -s "${H[@]}" -X POST $API/alert-groups/$(AG 'alertname%3D%22Meta%22')/acknowledge > /dev/null; ADV 300; sleep 2
curl -s -b jar $API/destinations/$MD | jq -r .health.reason                   # blocked by the outbound address policy: 169.254.169.254 is link-local (always blocked)
psql "$MUSTER_DATABASE_URL" -qc "UPDATE outbound_policies SET policy = 'standard'"

# C-15.AC-9: a Broken period (404), events of the period delivered in order afterwards
curl -s -X POST $FWH/_fake/faults -d '{"path":"/hook/auto","status":404}' > /dev/null
curl -s "${H[@]}" -X POST $API/alert-groups/$G2/unacknowledge > /dev/null; sleep 1
curl -s -b jar $API/destinations/$D | jq -r .health.state                    # broken
curl -s "${H[@]}" -X POST $API/alert-groups/$G2/acknowledge > /dev/null
curl -s "${H[@]}" -X POST $API/alert-groups/$G2/resolve > /dev/null
curl -s -X DELETE $FWH/_fake/faults; ADV 300; sleep 3
EV auto '[.[] | select(.status == 200) | .body.event] | .[-3:]'              # ["unacknowledged","acknowledged","resolved"]

# C-15.AC-13: delete while the endpoint is down with three events queued
curl -s -X POST $FWH/_fake/faults -d '{"path":"/hook/auto","status":404}' > /dev/null
for c in unresolve acknowledge resolve; do curl -s "${H[@]}" -X POST $API/alert-groups/$G2/$c > /dev/null; done; sleep 1
N0=$(EV auto 'length')
curl -s -o /dev/null -w '%{http_code}\n' "${H[@]}" -X DELETE $API/destinations/$D   # 204
ADV 600; sleep 2; EV auto 'length' | xargs test $N0 -eq && echo "nothing more sent"  # nothing more sent
psql "$MUSTER_DATABASE_URL" -Atc "SELECT count(*) FROM webhook_events e JOIN destinations d ON d.id = e.destination_id
  WHERE d.public_id = '$D' AND e.state = 'not_delivered' AND e.last_error = 'the Destination was deleted'"   # 3
psql "$MUSTER_DATABASE_URL" -Atc "SELECT signing_secret_ciphertext IS NULL, (SELECT count(*) FROM destination_secrets s WHERE s.destination_id = d.id)
  FROM destinations d WHERE d.public_id = '$D'"                                # t|0
curl -s "${H[@]}" -X POST $API/destinations/$MD/checks | jq -c '[.status, .errors[0].code]'   # [422,"check_not_supported"]
```

`test/e2e/webhook_events_test.go` adds C-15.AC-10 (30 new Alert
Groups in a minute on a Route with threshold 20: 30 `created` events while the messengers get one Storm summary),
C-15.AC-11 (one request per lifecycle event row through the API where the row is reachable) and a literal
`Authorization: Bearer abc` header giving `literal_credential` at `/events/headers/0/value`.

## Open questions

None.

## Notes

- Suggested commit: `feat(webhooks): add the outgoing webhook events mode with signing and secrets`.
- The body is rendered when the event is queued, so a retry hours later still carries the state the event described.
- `deleteDestination` must keep the secrets of a Destination that was deleted while one of its calls holds a lease until
  that call ends: a late Publication still in flight runs its final edit with them, and a signed webhook cannot be
  signed without the Signing secret. Today `RetireDestination` in `internal/delivery/membership.go` wipes the secrets in
  the deleting transaction once no delivery is pending, which is harmless for Mattermost and Telegram. The wipe is
  deferred until no delivery of that Destination holds a lease, and the worker that ends the last such call performs
  it.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-15.FR-1 | partial | the events mode in the API; the template mode is S-045, the form S-046 |
| C-15.FR-2 | partial | everything but the `test` event (S-047) |
| C-15.FR-5 | partial | the API; the actions on the page are S-046 |
| C-15.FR-6 | full | |
| C-15.FR-8 | full | |
| C-15.FR-9 | partial | the events mode; the template recipes are S-045 |
| C-15.FR-10 | partial | the API, masking and the warning; the page is S-046, masking in tests S-047 |
| C-15.FR-11 | partial | Mentions in the event body; in templates S-045 |
| C-15.FR-12 | partial | the events mode; the template mode and `both` are S-045 |
| C-15.AC-1 | full | |
| C-15.AC-2 | full | |
| C-15.AC-4 | full | |
| C-15.AC-5 | full | |
| C-15.AC-6 | full | |
| C-15.AC-7 | full | |
| C-15.AC-8 | full | |
| C-15.AC-9 | partial | the events mode; the template mode is S-045 |
| C-15.AC-10 | partial | the events mode; the template mode is S-045 |
| C-15.AC-11 | partial | every row in a table-driven test, the C-17 rows set up directly; the running timers produce theirs in S-049 |
| C-15.AC-12 | full | |
| C-15.AC-13 | full | |
| C-11.FR-6 | partial | the events mode is not subject to Storms |
| C-11.FR-9 | partial | the probe of a type without a check |
| C-11.FR-14 | partial | deleting an events-mode Destination |
| C-11.FR-18 | partial | the outgoing webhook fields |
| C-11.FR-19 | partial | the events-mode exception |
| C-12.FR-8 | partial | Mention settings of outgoing webhooks |
| C-01.FR-13 | partial | the fake receiving endpoint |
| C-11.FR-8 | partial | the outgoing webhook response mapping |

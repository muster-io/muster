# Outgoing webhook: events mode

An outgoing webhook Destination in the **events mode** sends one HTTP `POST` for every lifecycle event of every Alert
Group on its Routes: an ordered, signed stream of versioned JSON events that an automation can record, react to or
forward. Muster renders nothing for a chat here; for a chat with a REST API, use the template mode instead.

In the examples, `MUSTER_PUBLIC_URL` is `https://muster.example.org` and the receiving endpoint is
`https://automation.example.org/muster`.

## Create the Destination

Create a Destination of type `webhook` with the mode `events`:

```json
{
  "type": "webhook",
  "name": "automation",
  "mode": "events",
  "events": {
    "url": "https://automation.example.org/muster",
    "headers": [{ "name": "Authorization", "value": "Bearer {{ .Secrets.token }}" }]
  },
  "proxy": { "enabled": false },
  "mentions": { "...": "Mention settings, as for any Destination" },
  "limiter": { "limit": 5, "per_seconds": 1 }
}
```

- The **URL** and the **header values** are Go templates. They can read the Destination's Secrets as
  `{{ .Secrets.<name> }}` (or `{{ $.Secrets.<name> }}`, or `{{ index .Secrets "<name>" }}`) and nothing else; any
  other use of `.Secrets`, such as `{{ with .Secrets }}` or `{{ $s := .Secrets }}`, is refused, because Muster could
  not check that the Secret exists. They are parsed and run on a dry run when you save; an error names the field, its
  line and its column.
- Muster sets `Content-Type`, `webhook-id`, `webhook-timestamp` and `webhook-signature` itself; a header template may
  not name them.
- The **proxy** is the Destination's own; the requests never read `HTTP_PROXY` or `HTTPS_PROXY`.
- The **limiter** defaults to 5 requests per second in the form (`destination.webhook.limiter`).
- An outgoing webhook has no Connection and no Destination check: checking it answers `422 check_not_supported`.

The answer to the creation carries the **Signing secret** once, in `signing_secret`. Store it where the endpoint verifies the signatures; Muster
never shows it again.

## Secrets

Credentials the endpoint needs go into the Destination's **Secrets**, never into the URL or a header as text:

```sh
curl -X PUT https://muster.example.org/api/v1/destinations/DSK7M3QX9P2RTA/secrets/token \
  -H "Authorization: Bearer $MUSTER_TOKEN" -H 'Content-Type: application/json' \
  -d '{"value":"the-token-of-the-endpoint"}'
```

- A Secret name is a letter or an underscore followed by letters, digits and underscores.
- Values are write-only: `GET …/secrets` lists names, whether they are set and when they last changed, with an `ETag`
  that `If-Match` on `PUT` and `DELETE` compares with.
- Everyone who can read Destinations sees the URL and header templates as written, with the references only.
- Secret values and the Signing secrets are masked as `[redacted]` in delivery errors, the Timeline and the logs.
- A template that reads a Secret the Destination does not have fails: that event is not sent and ends as Not
  delivered.

A header named `Authorization`, or a URL whose user information or query holds a literal value instead of a
`{{ .Secrets.<name> }}` reference, gives the Destination the warning `literal_credential` with the JSON Pointer of the
field, for example `/events/headers/0/value`: the value is shown to everyone who can read Destinations.

## What is sent

One `POST` per lifecycle event, with the version 1 body as `application/json`:

```json
{
  "version": 1,
  "event": "created",
  "notify": true,
  "mentions": [{ "type": "user", "id": "SR2K9D1VW3MPTE", "name": "Alice Smith", "login": "alice" }],
  "sequence": 1,
  "occurred_at": "2026-10-03T09:41:07Z",
  "actor": { "kind": "system", "transport": "system" },
  "alert_group": {
    "number": 412,
    "id": "AGK7M3QX9P2RTA",
    "title": "HighErrorRate payments-api",
    "summary": "Error rate above 5% for 10 minutes",
    "status": "firing",
    "route": { "id": "RT8Q2M4X7K1P3B", "name": "payments" },
    "urgent": false,
    "severity_level": "critical",
    "started_at": "2026-10-03T09:41:07Z",
    "resolved_at": null,
    "snooze_until": null,
    "reopen_count": 0,
    "url": "https://muster.example.org/alert-groups/AGK7M3QX9P2RTA"
  },
  "alerts": [
    {
      "fingerprint": "6f1c2b0e9d4a7c31",
      "status": "firing",
      "labels": { "alertname": "HighErrorRate", "service": "payments-api" },
      "annotations": { "summary": "Error rate above 5%" },
      "starts_at": "2026-10-03T09:31:07Z",
      "resolved_at": null,
      "resolve_reason": null,
      "generator_url": "https://prometheus.example.org/graph?g0.expr=…"
    }
  ]
}
```

| Field | Meaning |
|---|---|
| `version` | `1`. |
| `event` | The lifecycle event, one of the names below. |
| `notify` | `true` exactly when the event is Loud. |
| `mentions` | Who the event mentions under the Destination's Mention settings, as data: `{"type": "everyone"}`, `{"type": "group", "name": …}` or `{"type": "user", "id": …, "name": …, "login": …}`. Always `[]` for a Quiet event. |
| `sequence` | The Alert Group's lifecycle event number. It grows with every lifecycle event of the Alert Group, may have gaps, and does not start at 1 for a Destination added to the Route later: rely only on "greater means newer". |
| `occurred_at` | When the event was recorded. |
| `actor` | Who made the change: a `user` or `service_account` with `id`, `name` (and the user's `login`, or the `token_name` used) and the `transport` (`ui`, `api`, `mattermost`, `telegram`, `cli`), or `system` with the `reason`, for example `timer`. |
| `alert_group` | The Alert Group as it was when the event was recorded: number, `id`, title, `summary`, status, the `owner` (`id`, `name`, `login`) while acknowledged, Route, Urgent, Severity level, times, Reopen count and its page in Muster. |
| `alerts` | One element per Alert, by fingerprint, with its status, labels, annotations, times and the resolve reason. |

The body is rendered when the event is recorded, so a retry hours later still carries the state the event described.

### Event names

Every row of the lifecycle event tables is one event, Quiet ones and those with nothing to show in a messenger
included. What delivery itself does — a Publication, a Storm summary, a final edit — is not a lifecycle event and is
never sent.

| `event` | `notify` |
|---|---|
| `created` | true |
| `alerts_added` | true while firing; false while acknowledged or snoozed |
| `alert_replaced`, `alert_resolved`, `alert_continued`, `annotations_changed`, `severity_raised` | false |
| `urgency_raised` | true when the rise ends a Snooze or removes the acknowledgement; false otherwise |
| `reopened` | true while firing or acknowledged; false while snoozed |
| `snooze_ended` | true |
| `resolved` | false |
| `moved_to_default_route` | false |
| `acknowledged` | false |
| `takeover` | true |
| `unacknowledged` | true when disabling or deleting the Owner released it; false for the Command |
| `unresolved`, `snoozed`, `unsnoozed`, `note_added` | false |
| `ack_timeout` | true |
| `unclaimed` | false |
| `reminder` | true |
| `reminder_answered` | false |
| `auto_unacknowledged` | true |
| `notices_missed` | true |

The event `test` is sent only by a Destination test: it carries `test: true` and `sequence` 0 and belongs to no Alert
Group's order.

### Versioning

New event names and new fields are added within `version` 1. Removing a field or changing its meaning needs a new
`version`. Ignore event names and fields you do not know.

## Order and duplicates

- The events of one Alert Group are sent **in order**: the next one waits until the previous one is delivered or ends
  as Not delivered. Events of different Alert Groups do not wait for each other.
- Delivery is **at least once** and never collapsed: a retry sends the same event again with the same `webhook-id`
  and the same body. **Drop duplicates by `webhook-id`.**
- Events are never subject to Storms: during a Storm every event is sent, `created` of every Alert Group included.
- While the Destination is Broken its events wait; after it recovers, every event that came due meanwhile is sent in
  order per Alert Group, none dropped and with no age limit.

## How the answer counts

| Answer | Outcome |
|---|---|
| `2xx` | Delivered. |
| `429` or `503` with `Retry-After` | The same event is sent again exactly when asked. |
| `408`, any other `5xx`, a timeout or a network error | Retried with backoff; after `delivery.transient_budget` the Destination becomes Broken. |
| `401`, `403`, `404`, `410` | The Destination becomes Broken; the event waits and is the probe. |
| `400`, `413`, `422`, a redirect, any other answer | That event ends as Not delivered and the next one follows; the Destination stays healthy. A redirect is never followed; the error names its target. |

Requests go through the outbound address policy: a URL that resolves to a link-local, cloud metadata, multicast or
unspecified address is refused under every policy, and loopback and private addresses need an allowed network. A
refused request makes the Destination Broken with the rule as the reason, because every later request would be
refused too. A Broken outgoing webhook is probed with its oldest waiting event; with none waiting, the next event is
attempted as soon as it comes due.

## Verify the signature

Every request carries three headers, in the style of the
[Standard Webhooks](https://www.standardwebhooks.com/) specification:

- `webhook-id` — the id of the event, kept across retries;
- `webhook-timestamp` — Unix seconds when the request was signed;
- `webhook-signature` — one or more signatures `v1,<base64>`, separated by a space.

Each signature is the base64 HMAC-SHA256 of `{webhook-id}.{webhook-timestamp}.{body}` — the raw body, byte for
byte — with the key that is the base64 after `whsec_` in the Signing secret. Accept a request when one of its
signatures matches, compare in constant time, and refuse a timestamp more than five minutes from your clock.

Go:

```go
func verify(secret, id, timestamp string, body []byte, header string) bool {
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil {
		return false
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || math.Abs(time.Since(time.Unix(ts, 0)).Seconds()) > 300 {
		return false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + timestamp + "."))
	mac.Write(body)
	want := []byte(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	for _, sig := range strings.Fields(header) {
		if v, s, ok := strings.Cut(sig, ","); ok && v == "v1" && hmac.Equal([]byte(s), want) {
			return true
		}
	}
	return false
}
```

Python:

```python
import base64, hashlib, hmac, time

def verify(secret: str, msg_id: str, timestamp: str, body: bytes, header: str) -> bool:
    key = base64.b64decode(secret.removeprefix("whsec_"))
    try:
        if abs(time.time() - int(timestamp)) > 300:
            return False
    except ValueError:
        return False
    mac = hmac.new(key, f"{msg_id}.{timestamp}.".encode() + body, hashlib.sha256)
    want = base64.b64encode(mac.digest()).decode()
    for sig in header.split():
        version, _, value = sig.partition(",")
        if version == "v1" and hmac.compare_digest(value, want):
            return True
    return False
```

A shell, with `openssl`, to check a recorded request by hand — it compares neither in constant time nor the
timestamp, so do not use it in a receiver — for a body saved in `body.json`:

```sh
KEY=$(printf '%s' "${SECRET#whsec_}" | base64 -d | od -An -v -tx1 | tr -d ' \n')
EXPECTED="v1,$( { printf '%s.%s.' "$WEBHOOK_ID" "$WEBHOOK_TIMESTAMP"; cat body.json; } |
  openssl dgst -sha256 -mac HMAC -macopt "hexkey:$KEY" -binary | base64)"
case " $WEBHOOK_SIGNATURE " in *" $EXPECTED "*) echo valid ;; *) echo invalid ;; esac
```

## Regenerate and retire the Signing secret

`POST /api/v1/destinations/{id}/signing-secret` makes a new Signing secret and shows it once. Until you retire the
previous one, requests carry two signatures — with the new and with the previous secret — and the Destination shows the
warning `previous_signing_secret_active` with the date since when: deploy the new secret to the receiving endpoint, then retire
the previous one with `DELETE /api/v1/destinations/{id}/signing-secret/previous`. Requests then carry one signature.
Both changes are recorded in the Audit log, never with the secret. Rotating the master keys re-encrypts the Signing
secrets but never changes them.

## Delete the Destination

Deleting an events-mode Destination sends nothing more: its waiting events end as Not delivered with "the Destination
was deleted", no final event is sent, and its secrets — the Signing secrets, the Secrets and the proxy password — are
wiped at once, or as soon as a request that was already being sent ends.

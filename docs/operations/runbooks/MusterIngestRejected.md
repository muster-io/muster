# MusterIngestRejected

## Meaning

Muster refused ingestion requests: an oversized one, or more than 10 without a valid token, within 15 minutes. The
chart rule is:

```promql
sum by (integration, outcome) (increase(muster_ingest_requests_total{job="muster",outcome="too_large"}[15m])) > 0 or sum by (integration, outcome) (increase(muster_ingest_requests_total{job="muster",outcome="unauthorized"}[15m])) > 10
```

It fires at once (`for: 0m`), with severity `warning` and the labels `integration` — the id of the token's
Integration, or `unknown` when the token matched none — and `outcome`. It is a chart rule only.

- `too_large`: the body was larger than `ingest.body_limit` (16 MB) and was refused with `413`.
- `unauthorized`: the token was missing, wrong or revoked, belonged to a deleted Integration, or was an API token
  instead of an Integration token; refused with `401`.

## Impact

- The refused requests are not stored or processed. When they come from an Alertmanager, its Alerts do not reach
  Muster, and with a Heartbeat on, the Integration soon becomes Heartbeat lost.
- Requests with valid tokens keep working.

## Diagnosis

1. See which Integration and outcome:
   `sum by (integration, outcome) (increase(muster_ingest_requests_total{job="muster"}[15m]))`.
2. Read the log lines `ingest_rejected` (`integration`, `outcome`, `route_pattern`, `client_address`): the route
   pattern tells ingestion from the Heartbeat, and the client address which host sent it. Muster never logs a token.
3. For `unauthorized`, open the Integration page (`/integrations/<id>`) and compare its tokens and their last use with
   the Alertmanager receivers that send to it. `integration="unknown"` means the token matched no Integration at all:
   a mistyped token, one of a deleted Integration, an API token, or a scanner probing the ingest listener.
4. For `too_large`, find the Alertmanager group that produces such a Snapshot: a group with very many Alerts or very
   long annotations.

## Fix

- A revoked or old token still in use: put a current token into the Alertmanager receiver and reload Alertmanager;
  see [Token rotation](../../integrations/alertmanager.md#token-rotation).
- An API token in a receiver: replace it with a token of the Integration; ingestion accepts only Integration tokens.
- Traffic from unknown addresses: restrict who may reach the ingest listener (`MUSTER_INGEST_URL`) with a network
  policy or the ingress.
- `too_large`: split the Alertmanager group with a finer `group_by`, or shorten the annotations that make it large.
